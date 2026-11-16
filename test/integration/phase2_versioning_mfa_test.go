//go:build integration

package integration_test

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cls/otp"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// The versioning gate's MFA delete and admin removal cases run as users of
// their own. mfaGateUser owns the MFA delete bucket and its TOTP devices;
// versioningAdminUser holds the buckets cap the admin API's removals need,
// and owns the buckets they remove. Their names and keys are fixed, so a run
// that stopped partway leaves them for the next run to remove first.
const (
	mfaGateUser         = "versioning-mfa-gate"
	versioningAdminUser = "versioning-admin-gate"
)

var (
	mfaGateKey         = keyPair{access: "VERMFAEXAMPLEKEY0001", secret: "verMfaExampleSecret" + strings.Repeat("0", 20) + "1"}
	versioningAdminKey = keyPair{access: "VERADMINEXAMPLEKEY01", secret: "verAdminExampleSecret" + strings.Repeat("0", 18) + "1"}
)

// mfaGateSecondSeed is RFC 6238's SHA-256 test key,
// "12345678901234567890123456789012", in hex: a second device's seed, apart
// from otpGateHexSeed's.
const mfaGateSecondSeed = "3132333435363738393031323334353637383930313233343536373839303132"

// removeVersioningMFAFixtures removes the two users as removeUserFixtures
// does, and mfaGateUser's otp object, which a user's removal leaves.
func removeVersioningMFAFixtures(ctx context.Context) {
	removeOTPUser(ctx, mfaGateUser)
	removeUserFixtures(ctx, mfaGateUser)
	removeUserFixtures(ctx, versioningAdminUser)
}

// otpRecord is cls_otp's record of uid's device dev, otp_instance, read
// from the user's MFA object in the zone's otp pool: the device, its
// recorded checks and its last success.
func otpRecord(ctx context.Context, uid string, dev otp.OTPInfo) otp.Instance {
	GinkgoHelper()
	key := otp.InstanceKey(dev.ID)
	rop := radosclient.NewReadOp()
	res := rop.OmapGetValsByKeys([]string{key})
	_, err := pool(ctx, store.ZoneParams().OTPPool).Read(ctx, otp.OTPObject(meta.UserID{ID: uid}), rop, radosclient.OpFlagNone)
	Expect(err).NotTo(HaveOccurred(), "reading %s's otp object", uid)
	Expect(res.Err).NotTo(HaveOccurred(), "reading %s's record", dev.ID)
	Expect(res.Values).To(HaveKey(key), "%s's record", dev.ID)
	d := denc.NewDecoder(res.Values[key])
	rec := otp.DecodeInstance(d)
	Expect(d.Err()).NotTo(HaveOccurred(), "decoding %s's record", dev.ID)
	return rec
}

// checkResults is the result of each check rec records, oldest first.
func checkResults(rec otp.Instance) []otp.CheckResult {
	out := make([]otp.CheckResult, 0, len(rec.LastChecks))
	for _, c := range rec.LastChecks {
		out = append(out, c.Result)
	}
	return out
}

// stepCode is dev's code for the step steps after the one that holds at.
func stepCode(dev otp.OTPInfo, at time.Time, steps int) string {
	return otpCode(dev, at.Add(time.Duration(steps)*time.Duration(dev.StepSize)*time.Second))
}

// wrongCode is a six-digit code dev accepts at no step of its window around
// at.
func wrongCode(dev otp.OTPInfo, at time.Time) string {
	GinkgoHelper()
	near := map[string]bool{}
	for d := -int(dev.Window) - 1; d <= int(dev.Window)+1; d++ {
		near[stepCode(dev, at, d)] = true
	}
	for i := range 1_000_000 {
		if code := fmt.Sprintf("%06d", i); !near[code] {
			return code
		}
	}
	Fail("no six-digit code is outside the window")
	return ""
}

// mfaHeader is x-amz-mfa naming dev and code.
func mfaHeader(dev otp.OTPInfo, code string) *string { return aws.String(dev.ID + " " + code) }

// deleteWithMFA is DeleteObject of key's version through g with the
// x-amz-mfa mfa, none for nil, and returns the status it answered.
func deleteWithMFA(ctx context.Context, g vGateway, bucket, key, version string, mfa *string) int {
	GinkgoHelper()
	_, err := g.s3.DeleteObject(ctx, &awss3.DeleteObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(key), VersionId: aws.String(version), MFA: mfa,
	})
	if err == nil {
		return http.StatusNoContent
	}
	Expect(statusOf(err)).NotTo(BeZero(), "DeleteObject %s?versionId=%s through %s: %v", key, version, g.name, err)
	return statusOf(err)
}

// mfaChecks reports whether radosgw-admin mfa check of uid's device dev
// accepts code.
func mfaChecks(ctx context.Context, uid string, dev otp.OTPInfo, code string) bool {
	GinkgoHelper()
	err := otpCheck(ctx, uid, dev.ID, code)
	if err != nil {
		Expect(err).To(MatchError(ContainSubstring("MFA check failed")), "radosgw-admin mfa check of %s", dev.ID)
	}
	return err == nil
}

// deleteObjects is DeleteObjects of ids from bucket through g.
func deleteObjects(ctx context.Context, g vGateway, bucket string, ids ...s3types.ObjectIdentifier) (*awss3.DeleteObjectsOutput, error) {
	return g.s3.DeleteObjects(ctx, &awss3.DeleteObjectsInput{Bucket: aws.String(bucket), Delete: &s3types.Delete{Objects: ids}})
}

// adminOf is g's admin API, signed as the user of g's keys.
func adminOf(g vGateway) adminGateway { return adminGateway{name: g.name, url: g.url} }

var _ = Describe("the versioning gate's MFA delete and admin removals", Ordered, ContinueOnFailure, Label("integration", "rgw-go", "phase2", "versioning"), func() {
	var mfaGateways, adminGateways map[string]vGateway

	BeforeAll(func(ctx SpecContext) {
		clearOLHInjections(ctx)
		removeVersioningMFAFixtures(ctx)
		DeferCleanup(removeVersioningMFAFixtures)
		createUser(ctx, mfaGateUser, mfaGateKey)
		createUser(ctx, versioningAdminUser, versioningAdminKey, "--caps", "buckets=*")
		mfaGateways, adminGateways = newVGateways(mfaGateKey), newVGateways(versioningAdminKey)
	})

	// Two devices of radosgw-admin mfa create's, step 120 s and window 2.
	// cls_otp's otp_instance::check keeps the checks of the last step_size
	// seconds, which trim_expired slides, and records at most
	// ATTEMPTS_PER_WINDOW, five, of them
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L98-L127 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/otp/cls_otp.cc#L98-L127 at v20.2.4).
	// So device a carries rgw-go's four checks and radosgw-admin's check of
	// the code rgw-go verified, and device b radosgw-admin's and radosgw's
	// checks and rgw-go's two refusals of their codes, five and four. They
	// all run in the step the spec starts in, so that each of the next
	// step's codes is used before the step after it begins: the class
	// verifies a used code again when it is checked a step after its own
	// (docs/differences.md, "An MFA code the device may have accepted before
	// does not verify").
	It("verifies MFA codes through rgw-go as radosgw counts them, and holds radosgw to the MFA delete rgw-go set", func(ctx SpecContext) {
		rg, rgwgo := mfaGateways["radosgw"], mfaGateways["rgw-go"]
		devA := otpGateDevice("versioning-gate-a", otp.SeedHex, otpGateHexSeed, 120, 2)
		devB := otpGateDevice("versioning-gate-b", otp.SeedHex, mfaGateSecondSeed, 120, 2)
		otpCreate(ctx, mfaGateUser, devA)
		otpCreate(ctx, mfaGateUser, devB)
		bucket := mfaBucket(ctx, rg)
		setVersioning(ctx, rg, bucket, s3types.BucketVersioningStatusEnabled)
		var obj []string
		for i := range 3 {
			obj = append(obj, putVersion(ctx, mfaGateways[[]string{"radosgw", "rgw-go"}[i%2]], bucket, "obj", []byte(fmt.Sprintf("obj's version %d", i))))
		}
		k1 := putVersion(ctx, rg, bucket, "k1", []byte("k1's version"))
		k2 := putVersion(ctx, rgwgo, bucket, "k2", []byte("k2's version"))

		stepEnd := waitStepRoom(ctx, devA, 90*time.Second)
		now := time.Now()

		By("setting MFA delete through rgw-go with device a's code")
		_, err := rgwgo.s3.PutBucketVersioning(ctx, &awss3.PutBucketVersioningInput{
			Bucket: aws.String(bucket), MFA: mfaHeader(devA, stepCode(devA, now, 0)),
			VersioningConfiguration: &s3types.VersioningConfiguration{Status: s3types.BucketVersioningStatusEnabled, MFADelete: s3types.MFADeleteEnabled},
		})
		Expect(err).NotTo(HaveOccurred(), "rgw-go's PutBucketVersioning of MFA delete")
		got, err := rg.s3.GetBucketVersioning(ctx, &awss3.GetBucketVersioningInput{Bucket: aws.String(bucket)})
		Expect(err).NotTo(HaveOccurred(), "radosgw's GetBucketVersioning")
		Expect(got.MFADelete).To(Equal(s3types.MFADeleteStatusEnabled), "the MFA delete radosgw reads")
		Expect(got.Status).To(Equal(s3types.BucketVersioningStatusEnabled), "the versioning radosgw reads")
		for _, g := range mfaGateways {
			Expect(deleteWithMFA(ctx, g, bucket, "obj", obj[0], nil)).To(Equal(http.StatusForbidden), "%s's delete of a version without a code", g.name)
		}

		// What each check answered is asserted once the step is known not to
		// have turned, since a used code verifies again after it turns.
		type answer struct {
			what      string
			got, want any
		}
		var answers []answer
		answered := func(what string, got, want any) { answers = append(answers, answer{what, got, want}) }

		By("rgw-go refusing a wrong and a reused code of device a, and verifying a fresh one")
		answered("rgw-go's delete with a wrong code", deleteWithMFA(ctx, rgwgo, bucket, "obj", obj[0], mfaHeader(devA, wrongCode(devA, now))),
			http.StatusForbidden)
		answered("rgw-go's delete with the code PutBucketVersioning used",
			deleteWithMFA(ctx, rgwgo, bucket, "obj", obj[0], mfaHeader(devA, stepCode(devA, now, 0))), http.StatusForbidden)
		answered("rgw-go's delete with the next step's code, unused",
			deleteWithMFA(ctx, rgwgo, bucket, "obj", obj[0], mfaHeader(devA, stepCode(devA, now, 1))), http.StatusNoContent)
		answered("radosgw-admin mfa check of the code rgw-go verified", mfaChecks(ctx, mfaGateUser, devA, stepCode(devA, now, 1)), false)

		By("rgw-go refusing device b's codes radosgw-admin and radosgw verified")
		answered("radosgw-admin mfa check of a fresh code", mfaChecks(ctx, mfaGateUser, devB, stepCode(devB, now, 0)), true)
		answered("rgw-go's delete with the code radosgw-admin's check used",
			deleteWithMFA(ctx, rgwgo, bucket, "obj", obj[1], mfaHeader(devB, stepCode(devB, now, 0))), http.StatusForbidden)
		answered("radosgw's delete with the next step's code",
			deleteWithMFA(ctx, rg, bucket, "obj", obj[1], mfaHeader(devB, stepCode(devB, now, 1))), http.StatusNoContent)
		answered("rgw-go's delete with the code radosgw's delete used",
			deleteWithMFA(ctx, rgwgo, bucket, "obj", obj[2], mfaHeader(devB, stepCode(devB, now, 1))), http.StatusForbidden)
		Expect(time.Now()).To(BeTemporally("<", stepEnd), "the codes were checked in the step that began the spec; past its end a used code verifies again")
		for _, a := range answers {
			Expect(a.got).To(Equal(a.want), a.what)
		}

		// The class records every check it is sent, refusals included, and
		// a code's index as its check's step plus the distance it was found
		// at, so each device's next-step code leaves the step after the
		// spec's as its last success (otp_instance::check and verify,
		// https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L108-L153 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/otp/cls_otp.cc#L108-L153 at v20.2.4).
		step := uint64(now.Unix()) / uint64(devA.StepSize)
		recA, recB := otpRecord(ctx, mfaGateUser, devA), otpRecord(ctx, mfaGateUser, devB)
		Expect(checkResults(recA)).To(Equal([]otp.CheckResult{otp.CheckSuccess, otp.CheckFail, otp.CheckFail, otp.CheckSuccess, otp.CheckFail}),
			"device a's recorded checks: rgw-go's PutBucketVersioning, wrong code, used code and next step's code, then radosgw-admin's")
		Expect(recA.LastSuccess).To(Equal(step+1), "device a's last success, the next step's code rgw-go verified")
		Expect(checkResults(recB)).To(Equal([]otp.CheckResult{otp.CheckSuccess, otp.CheckFail, otp.CheckSuccess, otp.CheckFail}),
			"device b's recorded checks: radosgw-admin's, rgw-go's refusal, radosgw's delete and rgw-go's refusal")
		Expect(recB.LastSuccess).To(Equal(step+1), "device b's last success, the next step's code radosgw verified")

		// rgw-go asks for MFA when a key names a version (docs/differences.md,
		// "DeleteObjects asks for MFA for the keys that name a version");
		// radosgw's check is inverted on both releases
		// (docs/ceph-upstream-bugs.md, "radosgw's DeleteObjects asks for MFA
		// for the keys that need none and not for the versions that do").
		By("DeleteObjects without a code through each gateway")
		_, err = deleteObjects(ctx, rgwgo, bucket, s3types.ObjectIdentifier{Key: aws.String("k1"), VersionId: aws.String(k1)})
		Expect(statusOf(err)).To(Equal(http.StatusForbidden), "rgw-go's DeleteObjects naming a version without a code: %v", err)
		out, err := deleteObjects(ctx, rgwgo, bucket, s3types.ObjectIdentifier{Key: aws.String("k2")})
		Expect(err).NotTo(HaveOccurred(), "rgw-go's DeleteObjects naming no version without a code")
		Expect(out.Errors).To(BeEmpty(), "rgw-go's DeleteObjects naming no version")
		Expect(out.Deleted).To(HaveExactElements(And(HaveField("Key", HaveValue(Equal("k2"))), HaveField("DeleteMarker", HaveValue(BeTrue())))),
			"rgw-go's DeleteObjects naming no version writes a marker")
		m2 := aws.ToString(out.Deleted[0].DeleteMarkerVersionId)
		out, err = deleteObjects(ctx, rg, bucket, s3types.ObjectIdentifier{Key: aws.String("k1"), VersionId: aws.String(k1)})
		Expect(err).NotTo(HaveOccurred(), "radosgw's DeleteObjects naming a version without a code, which it serves")
		Expect(out.Errors).To(BeEmpty(), "radosgw's DeleteObjects naming a version")
		Expect(out.Deleted).To(HaveExactElements(HaveField("VersionId", HaveValue(Equal(k1)))), "radosgw's DeleteObjects deletes the version")
		_, err = deleteObjects(ctx, rg, bucket, s3types.ObjectIdentifier{Key: aws.String("k2")})
		Expect(statusOf(err)).To(Equal(http.StatusForbidden), "radosgw's DeleteObjects naming no version without a code, which it refuses: %v", err)

		Expect(expectSameVersions(ctx, mfaGateways, bucket)).To(Equal([]listedVersion{
			listedMarker("k2", m2, true), listedData("k2", k2, false, []byte("k2's version")),
			listedData("obj", obj[2], true, []byte("obj's version 2")),
		}))
	}, SpecTimeout(8*time.Minute))

	It("removes an object of a versioned bucket through rgw-go's admin API as radosgw's admin API does, as a delete marker both list", func(ctx SpecContext) {
		rg, rgwgo := adminGateways["radosgw"], adminGateways["rgw-go"]
		bucket := versioningBucket(ctx, rg)
		setVersioning(ctx, rg, bucket, s3types.BucketVersioningStatusEnabled)
		for name, g := range adminGateways {
			key := "removed-by-" + name
			putVersion(ctx, rg, bucket, key, []byte("the first version"))
			putVersion(ctx, rgwgo, bucket, key, []byte("the second version"))
			r := adminCall(ctx, adminOf(g), versioningAdminKey, http.MethodDelete,
				"/admin/bucket?object&bucket="+bucket+"&object="+url.QueryEscape(key))
			Expect(r.status).To(Equal(http.StatusOK), "%s's admin object removal of %s: %s", name, key, r.body)
		}
		ls, ix := expectQuiescent(ctx, adminGateways, bucket)
		// By construction the two keys differ only in their names, which KEY
		// replaces, and their version ids, which indexShape labels. One user
		// writes the same bodies to both through the same gateways.
		shapes := map[string][]string{}
		for _, line := range indexShape(ix, ls) {
			for _, name := range []string{"radosgw", "rgw-go"} {
				if key := "removed-by-" + name; strings.Contains(line, " "+key+"#") || strings.Contains(line, " "+key+" ") {
					shapes[name] = append(shapes[name], strings.ReplaceAll(line, key, "KEY"))
				}
			}
		}
		Expect(shapes["rgw-go"]).NotTo(BeEmpty(), "the index entries of the key rgw-go removed")
		Expect(shapes["rgw-go"]).To(Equal(shapes["radosgw"]), "the index entries of the key rgw-go removed against radosgw's")
		for _, v := range ls {
			if v.Latest {
				Expect(v.Marker).To(BeTrue(), "%s's current version, once an admin removal removed it", v.Key)
				expectOLHHeadOn(ctx, bucket, v)
			}
		}
		Expect(ls).To(HaveLen(6), "two versions and a marker of each key")
		expectBucketChecksClean(ctx, bucket)
	}, SpecTimeout(6*time.Minute))

	It("purges through rgw-go's admin API a bucket radosgw filled with versions, leaving no index object and no data once the collector has run", func(ctx SpecContext) {
		rg := adminGateways["radosgw"]
		bucket := randName("ver-purge")
		_, err := rg.s3.CreateBucket(ctx, &awss3.CreateBucketInput{Bucket: aws.String(bucket)})
		Expect(err).NotTo(HaveOccurred(), "creating %s through radosgw", bucket)
		DeferCleanup(func(ctx context.Context) {
			if !bucketGone(ctx, bucket) {
				tryAdmin(ctx, "bucket", "rm", "--bucket", bucket, "--purge-objects")
				gcProcess(ctx)
			}
		}, NodeTimeout(5*time.Minute))
		var stats struct {
			ID     string `json:"id"`
			Marker string `json:"marker"`
		}
		adminJSON(ctx, &stats, "bucket", "stats", "--bucket", bucket)

		putKey(ctx, rg, bucket, "before-versioning", []byte("written before versioning"))
		setVersioning(ctx, rg, bucket, s3types.BucketVersioningStatusEnabled)
		for i := range 3 {
			putVersion(ctx, rg, bucket, "a", []byte(fmt.Sprintf("a's version %d", i)))
			// Past rgw_max_chunk_size, 4 MiB, so that each has a tail stripe.
			putVersion(ctx, rg, bucket, "b", randBytes(5<<20))
		}
		deleteKey(ctx, rg, bucket, "a")
		setVersioning(ctx, rg, bucket, s3types.BucketVersioningStatusSuspended)
		putKey(ctx, rg, bucket, "c", []byte("c's null version"))
		putKey(ctx, rg, bucket, "a", []byte("a's null version"))
		Expect(listVersions(ctx, rg, bucket, 1000)).To(HaveLen(10), "the versions radosgw wrote")

		r := adminCall(ctx, adminOf(adminGateways["rgw-go"]), versioningAdminKey, http.MethodDelete,
			"/admin/bucket?bucket="+bucket+"&purge-objects=true")
		Expect(r.status).To(Equal(http.StatusOK), "rgw-go's purge: %s", r.body)
		Expect(bucketGone(ctx, bucket)).To(BeTrue(), "%s after rgw-go's purge", bucket)
		Expect(listOIDs(ctx, meta.Pool{Name: manifest.Pools.Index})).NotTo(ContainElement(HavePrefix(".dir."+stats.ID)),
			"the bucket's index objects after rgw-go's purge")
		gcProcess(ctx)
		Expect(gcMarkerOIDs(ctx, stats.Marker)).To(BeEmpty(), "the GC entries of the bucket's data once the collector ran")
		Expect(markerOIDs(ctx, stats.Marker)).To(BeEmpty(), "the bucket's data once the collector ran")
	}, SpecTimeout(6*time.Minute))
})

// mfaBucket creates a bucket of g's user through radosgw. When the spec
// ends radosgw-admin removes it with its objects, which no MFA delete
// governs, and none of its data may be left once the collector has run.
func mfaBucket(ctx context.Context, radosgw vGateway) string {
	GinkgoHelper()
	name := randName("ver-mfa")
	_, err := radosgw.s3.CreateBucket(ctx, &awss3.CreateBucketInput{Bucket: aws.String(name)})
	Expect(err).NotTo(HaveOccurred(), "creating %s through radosgw", name)
	marker := bucketMarker(ctx, name)
	DeferCleanup(func(ctx context.Context) {
		admin(ctx, "bucket", "rm", "--bucket", name, "--purge-objects")
		gcProcess(ctx)
		Expect(markerOIDs(ctx, marker)).To(BeEmpty(), "%s's data after its removal", name)
	}, NodeTimeout(5*time.Minute))
	return name
}
