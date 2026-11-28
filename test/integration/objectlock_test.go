//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/objectlock"
)

// objectLockUser owns the object-lock gate's buckets. Its name and keys are
// fixed, so a run that stopped partway leaves it for the next run to remove
// first.
const objectLockUser = "objectlock-gate"

var objectLockKey = keyPair{access: "OLGATEEXAMPLEKEY0001", secret: "olGateExampleSecret" + strings.Repeat("0", 20) + "1"}

// The periods get_lock_until_date adds to a version's mtime:
// std::chrono::days, and std::chrono::years, the Gregorian year of 365.2425
// days
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_object_lock.cc#L103-L111 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_object_lock.cc#L87-L95 at v20.2.4).
const (
	lockDay  = 86400 * time.Second
	lockYear = 31556952 * time.Second
)

// defaultObjLock is RGWObjectLock() as its encode writes it, the obj_lock of
// a bucket created with object lock: ENCODE_START(1, 1) over 2 bytes,
// enabled true and rule_exist false
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_object_lock.h#L94-L154 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_object_lock.h#L90-L149 at v20.2.4).
var defaultObjLock = meta.RawStruct{1, 1, 2, 0, 0, 0, 1, 0}

// The messages radosgw sets in s->err.message for a delete its object lock
// refuses
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L5213-L5219 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L5603-L5609 at v20.2.4)
// and for a retention change its PutObjectRetention refuses
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8413-L8429 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9348-L9364 at v20.2.4).
const (
	lockMsgForbidden      = "forbidden by object lock"
	lockMsgShortens       = "proposed retain-until date shortens an existing retention period and governance bypass check failed"
	lockMsgToGovernance   = "can't change retention mode from COMPLIANCE to GOVERNANCE"
	lockMsgFromGovernance = "can't change retention mode from GOVERNANCE without governance bypass"
)

// lockAnswer is a gateway's answer to a request the gate expects it may
// refuse: the zero value for a success, else the status and the error
// document's code and message.
type lockAnswer struct {
	status        int
	code, message string
}

// answerOf is an S3 client's err as a lockAnswer.
func answerOf(err error) lockAnswer {
	if err == nil {
		return lockAnswer{}
	}
	a := lockAnswer{status: statusOf(err), code: err.Error()}
	if ae, ok := errors.AsType[apiError](err); ok {
		a.code, a.message = ae.ErrorCode(), ae.ErrorMessage()
	}
	return a
}

// lockRefused is a 403 AccessDenied with message.
func lockRefused(message string) lockAnswer {
	return lockAnswer{status: http.StatusForbidden, code: "AccessDenied", message: message}
}

// lockGateBucket creates a bucket of objectLockUser's through g, with object
// lock when locked. When the spec ends radosgw-admin removes it with its
// objects, which no object lock governs, and none of its data may be left
// once the collector has run.
func lockGateBucket(ctx context.Context, g vGateway, locked bool) string {
	GinkgoHelper()
	name := randName("lock")
	in := &awss3.CreateBucketInput{Bucket: aws.String(name)}
	if locked {
		in.ObjectLockEnabledForBucket = aws.Bool(true)
	}
	_, err := g.s3.CreateBucket(ctx, in)
	Expect(err).NotTo(HaveOccurred(), "creating %s through %s", name, g.name)
	marker := bucketMarker(ctx, name)
	DeferCleanup(func(ctx context.Context) {
		admin(ctx, "bucket", "rm", "--bucket", name, "--purge-objects")
		gcProcess(ctx)
		Expect(markerOIDs(ctx, marker)).To(BeEmpty(), "%s's data after its removal", name)
	}, NodeTimeout(5*time.Minute))
	return name
}

// instanceOf is bucket's instance as RADOS holds it.
func instanceOf(ctx context.Context, bucket string) meta.BucketInfo {
	GinkgoHelper()
	rec, err := store.GetBucket(ctx, "", bucket)
	Expect(err).NotTo(HaveOccurred(), "GetBucket %s", bucket)
	return decodeInstance(rawInstance(ctx, rec.Info.Bucket))
}

// lockSend sends method on pathQuery to g as its user with no body, signed
// over UNSIGNED-PAYLOAD, and returns the status, the headers and the body.
func lockSend(ctx context.Context, g vGateway, method, pathQuery string) (int, http.Header, []byte) {
	GinkgoHelper()
	req, err := http.NewRequestWithContext(ctx, method, g.url+pathQuery, http.NoBody)
	Expect(err).NotTo(HaveOccurred())
	req.Header.Set("X-Amz-Content-Sha256", unsignedPayloadHash)
	Expect(v4.NewSigner().SignHTTP(ctx, g.key.creds(), req, unsignedPayloadHash, "s3", "us-east-1", time.Now())).To(Succeed())
	resp, err := rawClient.Do(req)
	Expect(err).NotTo(HaveOccurred(), "%s %s on %s", method, pathQuery, g.name)
	b, err := io.ReadAll(resp.Body)
	Expect(errors.Join(err, resp.Body.Close())).NotTo(HaveOccurred(), "reading %s %s on %s", method, pathQuery, g.name)
	return resp.StatusCode, resp.Header, b
}

// expectSameDocument requires both gateways to answer a GET of pathQuery
// with 200 and the same body, and returns it.
func expectSameDocument(ctx context.Context, gateways map[string]vGateway, pathQuery string) []byte {
	GinkgoHelper()
	status, _, want := lockSend(ctx, gateways["radosgw"], http.MethodGet, pathQuery)
	Expect(status).To(Equal(http.StatusOK), "radosgw's GET %s: %s", pathQuery, want)
	status, _, got := lockSend(ctx, gateways["rgw-go"], http.MethodGet, pathQuery)
	Expect(status).To(Equal(http.StatusOK), "rgw-go's GET %s: %s", pathQuery, got)
	Expect(string(got)).To(Equal(string(want)), "rgw-go's GET %s against radosgw's", pathQuery)
	return want
}

// lockConfiguration is ObjectLockConfiguration with rule, none for nil.
func lockConfiguration(rule *meta.DefaultRetention) *s3types.ObjectLockConfiguration {
	c := &s3types.ObjectLockConfiguration{ObjectLockEnabled: s3types.ObjectLockEnabledEnabled}
	if rule == nil {
		return c
	}
	d := &s3types.DefaultRetention{Mode: s3types.ObjectLockRetentionMode(rule.Mode)}
	if rule.Days > 0 {
		d.Days = aws.Int32(rule.Days)
	} else {
		d.Years = aws.Int32(rule.Years)
	}
	c.Rule = &s3types.ObjectLockRule{DefaultRetention: d}
	return c
}

// lockView is an object lock configuration as GetObjectLockConfiguration
// answers it: ObjectLockEnabled, and the rule, nil for none.
type lockView struct {
	enabled string
	rule    *meta.DefaultRetention
}

func lockViewOf(c *s3types.ObjectLockConfiguration) lockView {
	if c == nil {
		return lockView{}
	}
	v := lockView{enabled: string(c.ObjectLockEnabled)}
	if c.Rule != nil && c.Rule.DefaultRetention != nil {
		d := c.Rule.DefaultRetention
		v.rule = &meta.DefaultRetention{Mode: string(d.Mode), Days: aws.ToInt32(d.Days), Years: aws.ToInt32(d.Years)}
	}
	return v
}

// putLockConfiguration is PutObjectLockConfiguration of rule, none for nil,
// on bucket through g.
func putLockConfiguration(ctx context.Context, g vGateway, bucket string, rule *meta.DefaultRetention) error {
	_, err := g.s3.PutObjectLockConfiguration(ctx, &awss3.PutObjectLockConfigurationInput{
		Bucket: aws.String(bucket), ObjectLockConfiguration: lockConfiguration(rule),
	})
	return err
}

// expectLockConfiguration waits for g's GetObjectLockConfiguration of bucket
// to answer the lock enabled with rule, none for nil: another gateway's
// write reaches g's cache through its notify.
func expectLockConfiguration(ctx context.Context, g vGateway, bucket string, rule *meta.DefaultRetention) {
	GinkgoHelper()
	Eventually(func(gm Gomega) lockView {
		out, err := g.s3.GetObjectLockConfiguration(ctx, &awss3.GetObjectLockConfigurationInput{Bucket: aws.String(bucket)})
		gm.Expect(err).NotTo(HaveOccurred())
		return lockViewOf(out.ObjectLockConfiguration)
	}).WithContext(ctx).WithTimeout(10*time.Second).WithPolling(200*time.Millisecond).Should(Equal(lockView{enabled: "Enabled", rule: rule}),
		"GetObjectLockConfiguration of %s through %s", bucket, g.name)
}

// expectNoLockConfiguration requires g's GetObjectLockConfiguration of
// bucket to answer 404 ObjectLockConfigurationNotFoundError.
func expectNoLockConfiguration(ctx context.Context, g vGateway, bucket string) {
	GinkgoHelper()
	_, err := g.s3.GetObjectLockConfiguration(ctx, &awss3.GetObjectLockConfigurationInput{Bucket: aws.String(bucket)})
	a := answerOf(err)
	Expect(a.status).To(Equal(http.StatusNotFound), "GetObjectLockConfiguration of %s through %s: %+v", bucket, g.name, a)
	Expect(a.code).To(Equal("ObjectLockConfigurationNotFoundError"), "GetObjectLockConfiguration of %s through %s", bucket, g.name)
}

// putRetention is PutObjectRetention of key's version through g, with the
// governance bypass header when bypass.
func putRetention(ctx context.Context, g vGateway, bucket, key, version string, mode s3types.ObjectLockRetentionMode, until time.Time, bypass bool) error {
	in := &awss3.PutObjectRetentionInput{
		Bucket: aws.String(bucket), Key: aws.String(key), VersionId: aws.String(version),
		Retention: &s3types.ObjectLockRetention{Mode: mode, RetainUntilDate: aws.Time(until)},
	}
	if bypass {
		in.BypassGovernanceRetention = aws.Bool(true)
	}
	_, err := g.s3.PutObjectRetention(ctx, in)
	return err
}

// putLegalHold is PutObjectLegalHold ON of key's version through g.
func putLegalHold(ctx context.Context, g vGateway, bucket, key, version string) error {
	_, err := g.s3.PutObjectLegalHold(ctx, &awss3.PutObjectLegalHoldInput{
		Bucket: aws.String(bucket), Key: aws.String(key), VersionId: aws.String(version),
		LegalHold: &s3types.ObjectLockLegalHold{Status: s3types.ObjectLockLegalHoldStatusOn},
	})
	return err
}

// deleteLocked is DeleteObject of key's version through g, with the
// governance bypass header when bypass.
func deleteLocked(ctx context.Context, g vGateway, bucket, key, version string, bypass bool) lockAnswer {
	in := &awss3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(key), VersionId: aws.String(version)}
	if bypass {
		in.BypassGovernanceRetention = aws.Bool(true)
	}
	_, err := g.s3.DeleteObject(ctx, in)
	return answerOf(err)
}

// deleteLockedBatch is one DeleteObjects of the versions of keys through g,
// with the governance bypass header when bypass, and its answer for each
// key: "deleted", or the Error element's code and message.
func deleteLockedBatch(ctx context.Context, g vGateway, bucket string, versions map[string]string, keys []string, bypass bool) map[string]string {
	GinkgoHelper()
	ids := make([]s3types.ObjectIdentifier, 0, len(keys))
	for _, k := range keys {
		ids = append(ids, s3types.ObjectIdentifier{Key: aws.String(k), VersionId: aws.String(versions[k])})
	}
	in := &awss3.DeleteObjectsInput{Bucket: aws.String(bucket), Delete: &s3types.Delete{Objects: ids}}
	if bypass {
		in.BypassGovernanceRetention = aws.Bool(true)
	}
	out, err := g.s3.DeleteObjects(ctx, in)
	Expect(err).NotTo(HaveOccurred(), "DeleteObjects of %v in %s through %s", keys, bucket, g.name)
	got := map[string]string{}
	for _, d := range out.Deleted {
		k := aws.ToString(d.Key)
		Expect(aws.ToString(d.VersionId)).To(Equal(versions[k]), "the version DeleteObjects through %s deleted of %s", g.name, k)
		got[k] = "deleted"
	}
	for _, e := range out.Errors {
		k := aws.ToString(e.Key)
		Expect(aws.ToString(e.VersionId)).To(Equal(versions[k]), "the version DeleteObjects through %s refused of %s", g.name, k)
		Expect(got).NotTo(HaveKey(k), "DeleteObjects through %s answers %s twice", g.name, k)
		got[k] = aws.ToString(e.Code) + " " + aws.ToString(e.Message)
	}
	return got
}

// versionHead is the mtime and the attrs of the head of key's version in
// bucket, as RADOS holds them.
func versionHead(ctx context.Context, bucket, key, version string) (time.Time, map[string][]byte) {
	GinkgoHelper()
	rec, err := store.GetBucket(ctx, "", bucket)
	Expect(err).NotTo(HaveOccurred(), "GetBucket %s", bucket)
	oid := meta.Stripe{Obj: meta.Obj{Bucket: rec.Info.Bucket, Key: meta.ObjKey{Name: key, Instance: version}}}.OID()
	st, err := rawStat(ctx, oid)
	Expect(err).NotTo(HaveOccurred(), "stat of %s", oid)
	return st.ModTime, rawXattrs(ctx, oid)
}

// versionRetention is the retention the head of key's version holds, decoded
// as strictly as radosgw's deletes decode it, and the attr's bytes.
func versionRetention(ctx context.Context, bucket, key, version string) (time.Time, meta.ObjectRetention, []byte) {
	GinkgoHelper()
	mtime, attrs := versionHead(ctx, bucket, key, version)
	Expect(attrs).To(HaveKey(meta.AttrObjectRetention), "%s?versionId=%s's retention", key, version)
	r, err := objectlock.DecodeRetention(attrs[meta.AttrObjectRetention])
	Expect(err).NotTo(HaveOccurred(), "decoding %s?versionId=%s's retention", key, version)
	return mtime, r, attrs[meta.AttrObjectRetention]
}

// lockPeriod is rule's period as get_lock_until_date adds it: its days when
// they are positive, else its years.
func lockPeriod(rule meta.DefaultRetention) time.Duration {
	if rule.Days > 0 {
		return time.Duration(rule.Days) * lockDay
	}
	return time.Duration(rule.Years) * lockYear
}

// expectDefaultRetention requires key's version, written to bucket while its
// rule was rule, to hold the retention _do_write_meta gives a create write
// that names none: the rule's mode, until the head's mtime plus the rule's
// period, in the bytes RGWObjectRetention encodes
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L3207-L3221 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L3304-L3318 at v20.2.4).
// Both gateways' HEAD of the version must then send that retention alike.
func expectDefaultRetention(ctx context.Context, gateways map[string]vGateway, bucket, key, version string, rule meta.DefaultRetention) {
	GinkgoHelper()
	mtime, got, raw := versionRetention(ctx, bucket, key, version)
	until := mtime.Add(lockPeriod(rule)).UTC()
	Expect(got.Mode).To(Equal(rule.Mode), "%s?versionId=%s's retention mode", key, version)
	Expect(got.RetainUntil).To(BeTemporally("==", until), "%s?versionId=%s's retain-until against its mtime %s plus %s",
		key, version, mtime.UTC().Format(time.RFC3339Nano), lockPeriod(rule))
	Expect(raw).To(Equal(objectlock.EncodeRetention(meta.ObjectRetention{Mode: rule.Mode, RetainUntil: until})),
		"%s?versionId=%s's user.rgw.object-retention", key, version)

	heads := map[string]http.Header{}
	for name, g := range gateways {
		status, h, _ := lockSend(ctx, g, http.MethodHead, versionPath(bucket, key, version))
		Expect(status).To(Equal(http.StatusOK), "HEAD of %s?versionId=%s through %s", key, version, name)
		heads[name] = h
	}
	for _, name := range []string{"x-amz-object-lock-mode", "x-amz-object-lock-retain-until-date"} {
		Expect(heads["rgw-go"].Get(name)).To(Equal(heads["radosgw"].Get(name)), "rgw-go's %s of %s?versionId=%s against radosgw's", name, key, version)
	}
	Expect(heads["radosgw"].Get("x-amz-object-lock-mode")).To(Equal(rule.Mode), "radosgw's x-amz-object-lock-mode of %s", key)
	sent, err := time.Parse(time.RFC3339Nano, heads["radosgw"].Get("x-amz-object-lock-retain-until-date"))
	Expect(err).NotTo(HaveOccurred(), "radosgw's x-amz-object-lock-retain-until-date of %s", key)
	Expect(sent).To(BeTemporally("==", until), "the x-amz-object-lock-retain-until-date of %s", key)
}

// realTime is the instant a real_time of ns nanoseconds since the epoch
// names.
func realTime(ns uint64) time.Time {
	return time.Unix(int64(ns/uint64(time.Second)), int64(ns%uint64(time.Second))).UTC() //nolint:gosec // a u64's seconds fit an int64
}

// realTimeSum is mtime plus rule's years of std::chrono::years as radosgw's
// get_lock_until_date adds them: the period converted to real_time's u64
// nanoseconds, then added to mtime's, each step modulo 2^64.
func realTimeSum(mtime time.Time, rule meta.DefaultRetention) time.Time {
	return realTime(uint64(mtime.UnixNano()) + uint64(rule.Years)*uint64(lockYear)) //nolint:gosec // radosgw's u64 nanoseconds, wrapping as it does
}

// realTimeOfSecond is real_clock::from_time_t of sec, the second converted to
// real_time's u64 nanoseconds modulo 2^64, as from_iso_8601 converts a date.
func realTimeOfSecond(sec int64) time.Time {
	return realTime(uint64(sec) * uint64(time.Second)) //nolint:gosec // radosgw's u64 nanoseconds, wrapping as it does
}

// The object-lock gate holds rgw-go and the Rook store's radosgw, on one
// zone, to one another's object lock: a bucket's lock configuration, a
// version's retention and legal hold, and a bucket's default retention, each
// set through one gateway, are what the other serves, enforces and applies,
// in the bytes radosgw stores. Where radosgw's own defects meet the cases,
// rgw-go's answer is asserted and radosgw's recorded, the reproductions
// docs/ceph-upstream-bugs.md names.
var _ = Describe("the object-lock gate", Ordered, ContinueOnFailure, Label("integration", "rgw-go", "phase2", "lifecycle", "objectlock"), func() {
	var gateways map[string]vGateway

	BeforeAll(func(ctx SpecContext) {
		clearOLHInjections(ctx)
		removeUserFixtures(ctx, objectLockUser)
		DeferCleanup(removeUserFixtures, objectLockUser)
		createUser(ctx, objectLockUser, objectLockKey)
		gateways = newVGateways(objectLockKey)
	})

	// A bucket created with object lock through one gateway is versioned with
	// lock through the other: versioning Enabled, which the other refuses to
	// suspend, the lock enabled with no rule, and the flags and obj_lock
	// radosgw's CreateBucket gives it
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L2404-L2406 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L2496-L2498 at v20.2.4).
	It("reads a bucket created with object lock through either gateway as versioned with lock through the other", func(ctx SpecContext) {
		type created struct {
			flags   uint32
			objLock meta.RawStruct
		}
		run := func(creator string) created {
			GinkgoHelper()
			r := gateways[wpOtherGateway(creator)]
			bucket := lockGateBucket(ctx, gateways[creator], true)

			v, err := r.s3.GetBucketVersioning(ctx, &awss3.GetBucketVersioningInput{Bucket: aws.String(bucket)})
			Expect(err).NotTo(HaveOccurred(), "GetBucketVersioning of %s through %s", bucket, r.name)
			Expect(v.Status).To(Equal(s3types.BucketVersioningStatusEnabled), "the versioning %s reads of the bucket %s created with lock", r.name, creator)
			expectLockConfiguration(ctx, r, bucket, nil)
			expectSameDocument(ctx, gateways, "/"+bucket+"?object-lock")
			_, err = r.s3.PutBucketVersioning(ctx, &awss3.PutBucketVersioningInput{
				Bucket: aws.String(bucket), VersioningConfiguration: &s3types.VersioningConfiguration{Status: s3types.BucketVersioningStatusSuspended},
			})
			Expect(answerOf(err)).To(Equal(lockAnswer{
				status: http.StatusConflict, code: "InvalidBucketState", message: "bucket versioning cannot be disabled on buckets with object lock enabled",
			}), "suspending versioning through %s on the bucket %s created with lock", r.name, creator)
			putVersion(ctx, r, bucket, "obj", []byte("a version"))

			info := instanceOf(ctx, bucket)
			Expect(info.VersioningEnabled()).To(BeTrue(), "the flags %#x of the bucket %s created with lock enable versioning", info.Flags, creator)
			Expect(info.ObjLockEnabled()).To(BeTrue(), "the flags %#x of the bucket %s created with lock enable object lock", info.Flags, creator)
			Expect(info.ObjLock).To(Equal(defaultObjLock), "the obj_lock of the bucket %s created with lock", creator)
			return created{flags: info.Flags, objLock: info.ObjLock}
		}
		byRadosgw, byRGWGo := run("radosgw"), run("rgw-go")
		Expect(byRGWGo).To(Equal(byRadosgw), "the flags and obj_lock of the bucket rgw-go created with lock against radosgw's")
	}, SpecTimeout(5*time.Minute))

	// PutObjectLockConfiguration through one gateway, on a bucket whose
	// versioning radosgw enabled, enables the lock and stores obj_lock, which
	// the other then serves in the same document; the same sequence through
	// the other gateway must store the same bytes: a rule in days, its
	// replacement in years, and its removal.
	It("stores one obj_lock for a PutObjectLockConfiguration through either gateway, which the other serves", func(ctx SpecContext) {
		rules := []*meta.DefaultRetention{{Mode: "GOVERNANCE", Days: 3}, {Mode: "COMPLIANCE", Years: 2}, nil}
		type stored struct {
			flags   []uint32
			objLock []meta.RawStruct
		}
		run := func(writer string) stored {
			GinkgoHelper()
			w, r := gateways[writer], gateways[wpOtherGateway(writer)]
			bucket := lockGateBucket(ctx, gateways["radosgw"], false)
			setVersioning(ctx, gateways["radosgw"], bucket, s3types.BucketVersioningStatusEnabled)
			// Each gateway caches the bucket here, so the other's write
			// reaches it only through that gateway's notify.
			for _, g := range gateways {
				expectNoLockConfiguration(ctx, g, bucket)
			}
			var s stored
			for _, rule := range rules {
				what := "no rule"
				if rule != nil {
					what = fmt.Sprintf("%+v", *rule)
				}
				By("putting " + what + " through " + writer)
				Expect(putLockConfiguration(ctx, w, bucket, rule)).To(Succeed(), "PutObjectLockConfiguration of %s through %s", what, writer)
				expectLockConfiguration(ctx, r, bucket, rule)
				expectLockConfiguration(ctx, w, bucket, rule)
				expectSameDocument(ctx, gateways, "/"+bucket+"?object-lock")

				info := instanceOf(ctx, bucket)
				Expect(info.VersioningEnabled() && info.ObjLockEnabled()).To(BeTrue(), "the flags %#x after %s's put of %s", info.Flags, writer, what)
				l, err := info.ObjectLock()
				Expect(err).NotTo(HaveOccurred(), "decoding the obj_lock %s stored for %s", writer, what)
				want := meta.ObjectLock{Enabled: true}
				if rule != nil {
					want.Rule = &meta.ObjectLockRule{DefaultRetention: *rule}
				}
				Expect(l).To(Equal(want), "the obj_lock %s stored for %s", writer, what)
				s.flags = append(s.flags, info.Flags)
				s.objLock = append(s.objLock, info.ObjLock)
			}
			Expect(s.objLock[len(s.objLock)-1]).To(Equal(defaultObjLock), "the obj_lock %s stored once the rule was removed", writer)
			return s
		}
		byRadosgw, byRGWGo := run("radosgw"), run("rgw-go")
		Expect(byRGWGo).To(Equal(byRadosgw), "the flags and obj_lock each of rgw-go's puts stored against radosgw's")
	}, SpecTimeout(5*time.Minute))

	// Versions locked through one gateway, GOVERNANCE, COMPLIANCE and a legal
	// hold, are refused by the other's DeleteObject and DeleteObjects, but for
	// a GOVERNANCE one deleted with the bypass by the bucket's owner, who
	// holds s3:BypassGovernanceRetention on both where no policy decides; the
	// other refuses a mode change and a shortening as PutObjectRetention does
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8403-L8430 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9338-L9365 at v20.2.4).
	// Both directions answer the same requests alike.
	It("refuses through either gateway the deletes and retention changes a lock set through the other forbids, and lets a bypass with the permission through", func(ctx SpecContext) {
		const (
			govSingle, govBatch, govChange = "governance-single", "governance-batch", "governance-change"
			compliance, held               = "compliance", "held"
		)
		governance, complianceMode := s3types.ObjectLockRetentionModeGovernance, s3types.ObjectLockRetentionModeCompliance
		forbidden := "AccessDenied AccessDenied"
		run := func(locker string) {
			GinkgoHelper()
			w, r := gateways[locker], gateways[wpOtherGateway(locker)]
			bucket := lockGateBucket(ctx, w, true)
			keys := []string{compliance, govBatch, govChange, govSingle, held}
			versions := map[string]string{}
			for _, k := range keys {
				versions[k] = putVersion(ctx, w, bucket, k, []byte(k+"'s version"))
			}
			// The dates are whole seconds, as radosgw compares them, and the
			// shorter one still in the future, which PutObjectRetention checks
			// before it compares the two.
			now := time.Now().UTC().Truncate(time.Second)
			until, shorter := now.Add(2*time.Hour), now.Add(time.Hour)

			By("locking them through " + locker)
			for _, k := range []string{govSingle, govBatch, govChange} {
				Expect(putRetention(ctx, w, bucket, k, versions[k], governance, until, false)).To(Succeed(), "GOVERNANCE on %s through %s", k, locker)
			}
			Expect(putRetention(ctx, w, bucket, compliance, versions[compliance], complianceMode, until, false)).To(Succeed(), "COMPLIANCE through %s", locker)
			Expect(putLegalHold(ctx, w, bucket, held, versions[held])).To(Succeed(), "the legal hold through %s", locker)
			expectLockConfiguration(ctx, r, bucket, nil)

			By("deleting them through " + r.name + " without the bypass")
			for _, k := range []string{govSingle, compliance, held} {
				Expect(deleteLocked(ctx, r, bucket, k, versions[k], false)).To(Equal(lockRefused(lockMsgForbidden)),
					"DeleteObject of %s?versionId=%s through %s", k, versions[k], r.name)
			}
			want := map[string]string{}
			for _, k := range keys {
				want[k] = forbidden
			}
			Expect(deleteLockedBatch(ctx, r, bucket, versions, keys, false)).To(Equal(want), "DeleteObjects of every version through %s", r.name)

			By("deleting them through " + r.name + " with the bypass, as the bucket's owner")
			Expect(deleteLocked(ctx, r, bucket, govSingle, versions[govSingle], true)).To(BeZero(), "DeleteObject of the GOVERNANCE version with the bypass through %s", r.name)
			for _, k := range []string{compliance, held} {
				Expect(deleteLocked(ctx, r, bucket, k, versions[k], true)).To(Equal(lockRefused(lockMsgForbidden)),
					"DeleteObject of %s?versionId=%s with the bypass through %s", k, versions[k], r.name)
			}
			Expect(deleteLockedBatch(ctx, r, bucket, versions, []string{govBatch, compliance, held}, true)).To(Equal(map[string]string{
				govBatch: "deleted", compliance: forbidden, held: forbidden,
			}), "DeleteObjects with the bypass through %s", r.name)

			By("changing the retentions through " + r.name)
			for _, c := range []struct {
				what, key string
				mode      s3types.ObjectLockRetentionMode
				until     time.Time
				bypass    bool
				want      lockAnswer
			}{
				{"COMPLIANCE to GOVERNANCE", compliance, governance, until, false, lockRefused(lockMsgToGovernance)},
				{"COMPLIANCE to GOVERNANCE with the bypass", compliance, governance, until, true, lockRefused(lockMsgToGovernance)},
				{"a shorter COMPLIANCE", compliance, complianceMode, shorter, false, lockRefused(lockMsgShortens)},
				{"a shorter COMPLIANCE with the bypass", compliance, complianceMode, shorter, true, lockRefused(lockMsgShortens)},
				{"a shorter GOVERNANCE", govChange, governance, shorter, false, lockRefused(lockMsgShortens)},
				{"GOVERNANCE to COMPLIANCE", govChange, complianceMode, until, false, lockRefused(lockMsgFromGovernance)},
				{"a shorter GOVERNANCE with the bypass", govChange, governance, shorter, true, lockAnswer{}},
			} {
				Expect(answerOf(putRetention(ctx, r, bucket, c.key, versions[c.key], c.mode, c.until, c.bypass))).To(Equal(c.want),
					"PutObjectRetention of %s through %s", c.what, r.name)
			}

			By("reading the retentions back through both")
			for k, mode := range map[string]string{compliance: "COMPLIANCE", govChange: "GOVERNANCE"} {
				doc := expectSameDocument(ctx, gateways, versionPath(bucket, k, versions[k])+"&retention")
				Expect(xmlElement(doc, "Mode")).To(Equal(mode), "%s's retention mode", k)
				date, err := time.Parse(time.RFC3339Nano, xmlElement(doc, "RetainUntilDate"))
				Expect(err).NotTo(HaveOccurred(), "%s's RetainUntilDate in %s", k, doc)
				wantDate := until
				if k == govChange {
					wantDate = shorter
				}
				Expect(date).To(BeTemporally("==", wantDate), "%s's retain-until", k)
			}
			doc := expectSameDocument(ctx, gateways, versionPath(bucket, held, versions[held])+"&legal-hold")
			Expect(xmlElement(doc, "Status")).To(Equal("ON"), "%s's legal hold", held)

			Expect(expectSameVersions(ctx, gateways, bucket)).To(Equal([]listedVersion{
				listedData(compliance, versions[compliance], true, []byte(compliance+"'s version")),
				listedData(govChange, versions[govChange], true, []byte(govChange+"'s version")),
				listedData(held, versions[held], true, []byte(held+"'s version")),
			}), "the versions left once the two the bypass let through went")
			expectBucketChecksClean(ctx, bucket)
		}
		run("radosgw")
		run("rgw-go")
	}, SpecTimeout(5*time.Minute))

	// A COMPLIANCE retention that has lapsed protects nothing, so rgw-go lets
	// it become GOVERNANCE (docs/differences.md, "A lapsed retention binds no
	// retention change"). radosgw holds the lapsed mode, which its answer,
	// recorded, reproduces or not (docs/ceph-upstream-bugs.md, "radosgw's
	// PutObjectRetention holds a lapsed retention's mode and date").
	It("lets a lapsed COMPLIANCE retention set through radosgw become GOVERNANCE through rgw-go, and records radosgw's answer to the same", func(ctx SpecContext) {
		radosgw, rgwgo := gateways["radosgw"], gateways["rgw-go"]
		bucket := lockGateBucket(ctx, radosgw, true)
		versions := map[string]string{}
		for _, changer := range []string{"radosgw", "rgw-go"} {
			key := "lapsed-" + changer
			versions[changer] = putVersion(ctx, gateways[wpOtherGateway(changer)], bucket, key, []byte(key))
		}
		// A few seconds ahead, as both gateways refuse a retain-until second
		// already past.
		lapse := time.Now().UTC().Truncate(time.Second).Add(4 * time.Second)
		for _, changer := range []string{"radosgw", "rgw-go"} {
			setter, key := gateways[wpOtherGateway(changer)], "lapsed-"+changer
			Expect(putRetention(ctx, setter, bucket, key, versions[changer], s3types.ObjectLockRetentionModeCompliance, lapse, false)).To(Succeed(),
				"COMPLIANCE until %s on %s through %s", lapse.Format(time.RFC3339), key, setter.name)
		}
		Eventually(time.Now).WithTimeout(10*time.Second).WithPolling(200*time.Millisecond).Should(BeTemporally(">", lapse.Add(time.Second)),
			"the clock past the COMPLIANCE retentions' date")

		until := time.Now().UTC().Truncate(time.Second).Add(time.Hour)
		Expect(putRetention(ctx, rgwgo, bucket, "lapsed-rgw-go", versions["rgw-go"], s3types.ObjectLockRetentionModeGovernance, until, false)).To(Succeed(),
			"GOVERNANCE through rgw-go over the lapsed COMPLIANCE retention radosgw set")
		_, r, _ := versionRetention(ctx, bucket, "lapsed-rgw-go", versions["rgw-go"])
		Expect(r.Mode).To(Equal("GOVERNANCE"), "the retention rgw-go stored over the lapsed one")
		Expect(r.RetainUntil).To(BeTemporally("==", until), "the retain-until rgw-go stored over the lapsed one")

		a := answerOf(putRetention(ctx, radosgw, bucket, "lapsed-radosgw", versions["radosgw"], s3types.ObjectLockRetentionModeGovernance, until, false))
		AddReportEntry("radosgw's PutObjectRetention holds a lapsed retention's mode and date: radosgw's answer to GOVERNANCE over a lapsed COMPLIANCE retention rgw-go set",
			fmt.Sprintf("%+v; reproduced: %t", a, a == lockRefused(lockMsgToGovernance)))
	}, SpecTimeout(3*time.Minute))

	// A bucket policy's Deny of s3:BypassGovernanceRetention on the keys' ARN
	// binds both of rgw-go's deletes (docs/differences.md, "DeleteObjects
	// checks s3:BypassGovernanceRetention for each key, on the key's ARN").
	// radosgw's DeleteObjects checks the bypass on the bucket's ARN
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L6780-L6783 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7713-L7716 at v20.2.4),
	// which the Deny does not name; its answer is recorded, the reproduction
	// of docs/ceph-upstream-bugs.md, "radosgw's DeleteObjects checks
	// s3:BypassGovernanceRetention on the bucket's ARN, not each key's".
	It("refuses the governance bypass a Deny on the keys' ARN forbids to DeleteObject through both gateways and to rgw-go's DeleteObjects, and records radosgw's DeleteObjects", func(ctx SpecContext) {
		radosgw, rgwgo := gateways["radosgw"], gateways["rgw-go"]
		bucket := lockGateBucket(ctx, radosgw, true)
		policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Principal":"*",` +
			`"Action":"s3:BypassGovernanceRetention","Resource":"arn:aws:s3:::` + bucket + `/*"}]}`
		_, err := radosgw.s3.PutBucketPolicy(ctx, &awss3.PutBucketPolicyInput{Bucket: aws.String(bucket), Policy: aws.String(policy)})
		Expect(err).NotTo(HaveOccurred(), "PutBucketPolicy of %s through radosgw", bucket)
		until := time.Now().UTC().Truncate(time.Second).Add(time.Hour)
		keys := []string{"batch-radosgw", "batch-rgw-go", "single"}
		versions := map[string]string{}
		for _, k := range keys {
			versions[k] = putVersion(ctx, radosgw, bucket, k, []byte(k))
			Expect(putRetention(ctx, radosgw, bucket, k, versions[k], s3types.ObjectLockRetentionModeGovernance, until, false)).To(Succeed(), "GOVERNANCE on %s", k)
		}
		// rgw-go loads the bucket here first, its policy with it.
		out, err := rgwgo.s3.GetBucketPolicy(ctx, &awss3.GetBucketPolicyInput{Bucket: aws.String(bucket)})
		Expect(err).NotTo(HaveOccurred(), "GetBucketPolicy of %s through rgw-go", bucket)
		Expect(aws.ToString(out.Policy)).To(ContainSubstring("s3:BypassGovernanceRetention"), "the policy rgw-go reads")

		for _, g := range []vGateway{rgwgo, radosgw} {
			Expect(deleteLocked(ctx, g, bucket, "single", versions["single"], true)).To(Equal(lockRefused(lockMsgForbidden)),
				"DeleteObject of the GOVERNANCE version with the bypass the policy denies, through %s", g.name)
		}
		Expect(deleteLockedBatch(ctx, rgwgo, bucket, versions, []string{"batch-rgw-go"}, true)).To(Equal(map[string]string{
			"batch-rgw-go": "AccessDenied AccessDenied",
		}), "rgw-go's DeleteObjects of the GOVERNANCE version with the bypass the policy denies")

		got := deleteLockedBatch(ctx, radosgw, bucket, versions, []string{"batch-radosgw"}, true)
		left := map[string]bool{}
		for _, v := range listVersions(ctx, radosgw, bucket, 1000) {
			left[v.Key] = true
		}
		Expect(left).To(HaveKey("single"), "the version both DeleteObjects refused")
		Expect(left).To(HaveKey("batch-rgw-go"), "the version rgw-go's DeleteObjects refused")
		AddReportEntry("radosgw's DeleteObjects checks s3:BypassGovernanceRetention on the bucket's ARN, not each key's: "+
			"radosgw's DeleteObjects of a GOVERNANCE version with the bypass a Deny on the keys' ARN forbids",
			fmt.Sprintf("answer %q; version still listed: %t; reproduced: %t", got["batch-radosgw"], left["batch-radosgw"], got["batch-radosgw"] == "deleted"))
	}, SpecTimeout(3*time.Minute))

	// A rule set through one gateway gives each create write through the
	// other, a PUT, a copy and a multipart completion, its retention: the
	// rule's mode until the write's mtime plus the rule's period, Years being
	// 365.2425 days. A copy takes no lock of its source's, so the rule's
	// applies to the copy's own mtime.
	It("gives a PUT, a copy and a completion through either gateway the default retention a rule set through the other names, mtime plus its period", func(ctx SpecContext) {
		rules := []meta.DefaultRetention{{Mode: "GOVERNANCE", Days: 2}, {Mode: "COMPLIANCE", Years: 1}}
		part := bytes.Repeat([]byte("a part "), 1000)
		run := func(setter string) {
			GinkgoHelper()
			w, r := gateways[setter], gateways[wpOtherGateway(setter)]
			bucket := lockGateBucket(ctx, w, true)
			// r caches the bucket here, so each rule reaches it through its
			// notify.
			expectLockConfiguration(ctx, r, bucket, nil)
			for i, rule := range rules {
				By(fmt.Sprintf("setting %+v through %s and writing through %s", rule, setter, r.name))
				Expect(putLockConfiguration(ctx, w, bucket, &rule)).To(Succeed(), "PutObjectLockConfiguration of %+v through %s", rule, setter)
				expectLockConfiguration(ctx, r, bucket, &rule)

				put, cp, mp := "put-"+strconv.Itoa(i), "copy-"+strconv.Itoa(i), "upload-"+strconv.Itoa(i)
				written := map[string]string{put: putVersion(ctx, r, bucket, put, []byte(put))}
				out, err := r.s3.CopyObject(ctx, &awss3.CopyObjectInput{
					Bucket: aws.String(bucket), Key: aws.String(cp), CopySource: aws.String(bucket + "/" + put + "?versionId=" + written[put]),
				})
				Expect(err).NotTo(HaveOccurred(), "CopyObject of %s to %s through %s", put, cp, r.name)
				written[cp] = aws.ToString(out.VersionId)
				id := createUpload(ctx, r.s3, bucket, mp, "")
				done, err := completeUpload(ctx, r.s3, bucket, mp, id, []s3types.CompletedPart{uploadPart(ctx, r.s3, bucket, mp, id, 1, part)})
				Expect(err).NotTo(HaveOccurred(), "CompleteMultipartUpload of %s through %s", mp, r.name)
				written[mp] = aws.ToString(done.VersionId)

				for _, key := range slices.Sorted(maps.Keys(written)) {
					Expect(written[key]).To(MatchRegexp(versionIDShape), "the version %s's write of %s names", r.name, key)
					expectDefaultRetention(ctx, gateways, bucket, key, written[key], rule)
				}
			}
		}
		run("radosgw")
		run("rgw-go")
	}, SpecTimeout(5*time.Minute))

	// A default retention whose period from now passes real_time's last
	// instant, and a retain-until date past it, are refused by rgw-go, and a
	// default stored by radosgw is clamped there by rgw-go's writes
	// (docs/differences.md, "A date real_time cannot hold is refused").
	// radosgw stores both and wraps the date; what it stores is recorded, the
	// reproduction of docs/ceph-upstream-bugs.md, "radosgw's object-lock
	// dates wrap past 2554".
	It("refuses a default retention or a retain-until date past real_time's range, clamps a default radosgw stored, and records what radosgw stores", func(ctx SpecContext) {
		radosgw, rgwgo := gateways["radosgw"], gateways["rgw-go"]
		bucket := lockGateBucket(ctx, radosgw, true)
		far := meta.DefaultRetention{Mode: "GOVERNANCE", Years: 600}

		Expect(answerOf(putLockConfiguration(ctx, rgwgo, bucket, &far))).To(Equal(lockAnswer{
			status: http.StatusBadRequest, code: "InvalidRetentionPeriod", message: "retention period must be a positive integer value",
		}), "rgw-go's PutObjectLockConfiguration of %+v", far)
		stored := answerOf(putLockConfiguration(ctx, radosgw, bucket, &far))
		AddReportEntry("radosgw's object-lock dates wrap past 2554: radosgw's PutObjectLockConfiguration of a rule of Years 600", fmt.Sprintf("%+v", stored))
		if stored == (lockAnswer{}) {
			expectLockConfiguration(ctx, rgwgo, bucket, &far)
			v := putVersion(ctx, rgwgo, bucket, "far-rgw-go", []byte("far-rgw-go"))
			_, r, raw := versionRetention(ctx, bucket, "far-rgw-go", v)
			clamped := meta.ObjectRetention{Mode: "GOVERNANCE", RetainUntil: meta.MaxRealTime}
			Expect(r.Mode).To(Equal(clamped.Mode), "the retention mode rgw-go gave its write under the rule radosgw stored")
			Expect(r.RetainUntil).To(BeTemporally("==", clamped.RetainUntil), "the retain-until rgw-go gave its write under the rule radosgw stored")
			Expect(raw).To(Equal(objectlock.EncodeRetention(clamped)), "the retention rgw-go gave its write under the rule radosgw stored")

			v = putVersion(ctx, radosgw, bucket, "far-radosgw", []byte("far-radosgw"))
			mtime, r, _ := versionRetention(ctx, bucket, "far-radosgw", v)
			wrapped := realTimeSum(mtime, far)
			AddReportEntry("radosgw's object-lock dates wrap past 2554: the retention radosgw gave its write under a rule of Years 600",
				fmt.Sprintf("%s until %s, mtime %s; the wrapped sum %s; reproduced: %t", r.Mode, r.RetainUntil.Format(time.RFC3339Nano),
					mtime.UTC().Format(time.RFC3339Nano), wrapped.Format(time.RFC3339Nano), r.RetainUntil.Equal(wrapped)))
		}

		date := time.Date(3000, 1, 1, 0, 0, 0, 0, time.UTC)
		in := func(key string) *awss3.PutObjectInput {
			return &awss3.PutObjectInput{
				Bucket: aws.String(bucket), Key: aws.String(key), Body: strings.NewReader(key),
				ObjectLockMode: s3types.ObjectLockModeGovernance, ObjectLockRetainUntilDate: aws.Time(date),
			}
		}
		_, err := rgwgo.s3.PutObject(ctx, in("dated-rgw-go"))
		a := answerOf(err)
		Expect(a.status).To(Equal(http.StatusBadRequest), "rgw-go's PutObject retained until %s: %+v", date.Format(time.RFC3339), a)
		Expect(a.code).To(Equal("InvalidArgument"), "rgw-go's PutObject retained until %s", date.Format(time.RFC3339))

		put, err := radosgw.s3.PutObject(ctx, in("dated-radosgw"))
		record := fmt.Sprintf("%+v", answerOf(err))
		if err == nil {
			_, r, _ := versionRetention(ctx, bucket, "dated-radosgw", aws.ToString(put.VersionId))
			wrapped := realTimeOfSecond(date.Unix())
			record = fmt.Sprintf("%s until %s; the wrapped date %s; reproduced: %t", r.Mode, r.RetainUntil.Format(time.RFC3339Nano),
				wrapped.Format(time.RFC3339Nano), r.RetainUntil.Equal(wrapped))
		}
		AddReportEntry("radosgw's object-lock dates wrap past 2554: the retention radosgw gave a PutObject retained until "+date.Format(time.RFC3339), record)
	}, SpecTimeout(3*time.Minute))
})
