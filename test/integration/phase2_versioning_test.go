//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	rgwcls "github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// versioningUser owns the versioning gate's buckets. Its name and keys are
// fixed, so a run that stopped partway leaves it for the next run to remove
// first.
const versioningUser = "versioning-gate"

var versioningKey = keyPair{access: "VERGATEEXAMPLEKEY001", secret: "verGateExampleSecret" + strings.Repeat("0", 19) + "1"}

// versionIDShape is a version id as both gateways generate it: radosgw
// hands gen_rand_alphanumeric_no_underscore a buffer of OBJ_INSTANCE_LEN,
// 32 bytes
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8868-L8877 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9614-L9623 at v20.2.4),
// which it fills with 31 characters and a NUL
// (https://github.com/ceph/ceph/blob/v19.2.7/src/common/random_string.cc#L96-L100 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/common/random_string.cc#L96-L100 at v20.2.4).
const versionIDShape = `^[A-Za-z0-9.-]{31}$`

// allUsers is the grantee URI of a public-read grant.
const allUsers = "http://acs.amazonaws.com/groups/global/AllUsers"

// vGateway is one gateway as the versioning gate reaches it, as one user:
// its S3 endpoint, the user's keys, and an S3 client of the user's.
type vGateway struct {
	name, url string
	key       keyPair
	s3        *awss3.Client
}

// newVGateways is the Rook store's radosgw and the rgw-go make rgw-go-up
// started beside it, each as the user of k.
func newVGateways(k keyPair) map[string]vGateway {
	GinkgoHelper()
	base := rgwgoAdminEndpoint()
	return map[string]vGateway{
		"radosgw": {name: "radosgw", url: endpoint, key: k, s3: s3At(endpoint, k)},
		"rgw-go":  {name: "rgw-go", url: base, key: k, s3: s3At(base, k)},
	}
}

// olhInjections are radosgw's options that fail a versioned link after its
// index entry, and the cancel that follows, with their values while set and
// their defaults.
var olhInjections = []radosgwOption{
	{name: "rgw_debug_inject_set_olh_err", value: "2", def: "0"},
	{name: "rgw_debug_inject_olh_cancel_modification_err", value: "true", def: "false"},
}

// vRequest sends method on pathQuery to g as its user with no body,
// signed over UNSIGNED-PAYLOAD, and returns the status and the body. It
// asserts nothing, so that racing goroutines can send it.
func vRequest(ctx context.Context, g vGateway, method, pathQuery string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, g.url+pathQuery, http.NoBody)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("X-Amz-Content-Sha256", unsignedPayloadHash)
	if err = v4.NewSigner().SignHTTP(ctx, g.key.creds(), req, unsignedPayloadHash, "s3", "us-east-1", time.Now()); err != nil {
		return 0, nil, err
	}
	resp, err := rawClient.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("%s %s on %s: %w", method, pathQuery, g.name, err)
	}
	b, err := io.ReadAll(resp.Body)
	return resp.StatusCode, b, errors.Join(err, resp.Body.Close())
}

// vCall is vRequest, which must reach the gateway.
func vCall(ctx context.Context, g vGateway, method, pathQuery string) (int, []byte) {
	GinkgoHelper()
	status, body, err := vRequest(ctx, g, method, pathQuery)
	Expect(err).NotTo(HaveOccurred())
	return status, body
}

// versionPath is the path of key's version in bucket, the current one for "".
func versionPath(bucket, key, version string) string {
	p := "/" + bucket + "/" + url.PathEscape(key)
	if version != "" {
		p += "?versionId=" + url.QueryEscape(version)
	}
	return p
}

// tryListVersions is bucket's ListObjectVersions through g, pages of
// maxKeys followed by their next markers until one is not truncated.
func tryListVersions(ctx context.Context, g vGateway, bucket string, maxKeys int) ([]listedVersion, error) {
	var out []listedVersion
	q := "?versions&max-keys=" + strconv.Itoa(maxKeys)
	for range 10_000 {
		status, body, err := vRequest(ctx, g, http.MethodGet, "/"+bucket+q)
		if err != nil {
			return nil, err
		}
		if status != http.StatusOK {
			return nil, fmt.Errorf("ListObjectVersions of %s through %s: %d %s", bucket, g.name, status, body)
		}
		p, err := parseVersionsPage(body)
		if err != nil {
			return nil, fmt.Errorf("%s's answer: %w: %s", g.name, err, body)
		}
		out = append(out, p.Versions...)
		if !p.Truncated {
			return out, nil
		}
		q = "?versions&max-keys=" + strconv.Itoa(maxKeys) + "&key-marker=" + url.QueryEscape(p.NextKeyMarker) +
			"&version-id-marker=" + url.QueryEscape(p.NextVersionIDMarker)
	}
	return nil, fmt.Errorf("ListObjectVersions of %s through %s did not end", bucket, g.name)
}

// listVersions is tryListVersions, which must succeed.
func listVersions(ctx context.Context, g vGateway, bucket string, maxKeys int) []listedVersion {
	GinkgoHelper()
	ls, err := tryListVersions(ctx, g, bucket, maxKeys)
	Expect(err).NotTo(HaveOccurred())
	return ls
}

// expectSameVersions lists bucket's versions through both gateways, in one
// page and paged a version at a time by the markers each page names,
// requires the four to be one listing, and returns it.
func expectSameVersions(ctx context.Context, gateways map[string]vGateway, bucket string) []listedVersion {
	GinkgoHelper()
	want := listVersions(ctx, gateways["radosgw"], bucket, 1000)
	for _, name := range []string{"radosgw", "rgw-go"} {
		Expect(listVersions(ctx, gateways[name], bucket, 1000)).To(Equal(want), "%s's versions listing", name)
		Expect(listVersions(ctx, gateways[name], bucket, 1)).To(Equal(want), "%s's versions listing a version a page", name)
	}
	return want
}

// tryBucketIndex is radosgw-admin bi list of bucket, decoded.
func tryBucketIndex(ctx context.Context, bucket string) (bucketIndex, error) {
	out, err := radosgwAdmin(ctx, "bi", "list", "--bucket", bucket)
	if err != nil {
		return bucketIndex{}, err
	}
	var entries []biEntry
	if err := json.Unmarshal(out, &entries); err != nil {
		return bucketIndex{}, fmt.Errorf("decoding bi list of %s: %w: %s", bucket, err, out)
	}
	return decodeIndex(entries)
}

// bucketIndexOf is tryBucketIndex, which must succeed.
func bucketIndexOf(ctx context.Context, bucket string) bucketIndex {
	GinkgoHelper()
	ix, err := tryBucketIndex(ctx, bucket)
	Expect(err).NotTo(HaveOccurred())
	return ix
}

// expectQuiescent waits up to a minute for bucket's index to agree with its
// versions listing (indexFindings): an index completion or a replay's
// removal may still be landing when the last request answers. It then
// requires both gateways to list the same versions, and returns the listing
// and the index.
func expectQuiescent(ctx context.Context, gateways map[string]vGateway, bucket string) ([]listedVersion, bucketIndex) {
	GinkgoHelper()
	var ix bucketIndex
	Eventually(func(g Gomega) []string {
		ls, err := tryListVersions(ctx, gateways["radosgw"], bucket, 1000)
		g.Expect(err).NotTo(HaveOccurred())
		ix, err = tryBucketIndex(ctx, bucket)
		g.Expect(err).NotTo(HaveOccurred())
		return indexFindings(ix, ls)
	}).WithContext(ctx).WithTimeout(time.Minute).WithPolling(time.Second).Should(BeEmpty(), "%s's index against its versions listing", bucket)
	return expectSameVersions(ctx, gateways, bucket), ix
}

// unlinkedInstance is one entry of radosgw-admin bucket check unlinked
// --dump-keys
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_bucket.cc#L765-L768 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_bucket.cc#L916-L919 at v20.2.4).
type unlinkedInstance struct {
	Name     string `json:"name"`
	Instance string `json:"instance"`
}

// unlinkedInstances is what radosgw-admin bucket check unlinked finds in
// bucket at any age: instance entries no link made listable.
func unlinkedInstances(ctx context.Context, bucket string) []unlinkedInstance {
	GinkgoHelper()
	out := []unlinkedInstance{}
	adminJSON(ctx, &out, "bucket", "check", "unlinked", "--bucket", bucket, "--min-age-hours", "0", "--dump-keys", "--hide-progress")
	return out
}

// leftoverOLH is what radosgw-admin bucket check olh finds in bucket: OLH
// entries of keys whose last version went, left pending removal.
func leftoverOLH(ctx context.Context, bucket string) []string {
	GinkgoHelper()
	out := []string{}
	adminJSON(ctx, &out, "bucket", "check", "olh", "--bucket", bucket, "--dump-keys", "--hide-progress")
	return out
}

// expectBucketChecksClean requires radosgw-admin's three bucket checks to
// find nothing in bucket.
func expectBucketChecksClean(ctx context.Context, bucket string) {
	GinkgoHelper()
	expectBucketCheckClean(ctx, bucket)
	Expect(unlinkedInstances(ctx, bucket)).To(BeEmpty(), "bucket check unlinked of %s", bucket)
	Expect(leftoverOLH(ctx, bucket)).To(BeEmpty(), "bucket check olh of %s", bucket)
}

// olhHeadOf reads key's OLH head in bucket.
func olhHeadOf(ctx context.Context, bucket, key string) olhHead {
	GinkgoHelper()
	attrs := rawXattrs(ctx, wpHeadOf(ctx, bucket, key))
	Expect(attrs).To(HaveKey(meta.AttrOLHInfo), "%s/%s's OLH head", bucket, key)
	d := denc.NewDecoder(attrs[meta.AttrOLHInfo])
	h := olhHead{info: meta.DecodeOLHInfo(d), ver: string(attrs[meta.AttrOLHVer])}
	Expect(d.Err()).NotTo(HaveOccurred(), "decoding %s/%s's olh.info", bucket, key)
	for name := range attrs {
		if strings.HasPrefix(name, meta.AttrOLHPendingPrefix) {
			h.pending = append(h.pending, name)
		}
	}
	slices.Sort(h.pending)
	return h
}

// expectOLHHeadOn requires key's OLH head to name v, as the latest version
// a listing shows, with no pending attr.
func expectOLHHeadOn(ctx context.Context, bucket string, v listedVersion) {
	GinkgoHelper()
	h := olhHeadOf(ctx, bucket, v.Key)
	Expect(indexInstance(h.info.Target.Key.Instance)).To(Equal(indexInstance(v.VersionID)), "%s/%s's olh.info", bucket, v.Key)
	Expect(h.info.Removed).To(Equal(v.Marker), "%s/%s's olh.info names a delete marker", bucket, v.Key)
	Expect(h.pending).To(BeEmpty(), "%s/%s's pending attrs", bucket, v.Key)
}

// versioningBucket creates a bucket of versioningUser's through radosgw.
// When the spec ends its unlinked instances and leftover OLH entries are
// fixed by radosgw-admin, every version is deleted through radosgw and the
// bucket with them, and none of its data may be left once the collector
// has run.
func versioningBucket(ctx context.Context, radosgw vGateway) string {
	GinkgoHelper()
	name := randName("ver")
	_, err := radosgw.s3.CreateBucket(ctx, &awss3.CreateBucketInput{Bucket: aws.String(name)})
	Expect(err).NotTo(HaveOccurred(), "creating %s through radosgw", name)
	marker := bucketMarker(ctx, name)
	DeferCleanup(func(ctx context.Context) {
		// The fixes run with no pending timeout, so that a pending attr a
		// failed write left does not keep its OLH head.
		tryAdmin(ctx, "bucket", "check", "unlinked", "--bucket", name, "--fix", "--min-age-hours", "0",
			"--rgw-olh-pending-timeout-sec", "0", "--hide-progress")
		for _, v := range listVersions(ctx, radosgw, name, 1000) {
			status, body := vCall(ctx, radosgw, http.MethodDelete, versionPath(name, v.Key, v.VersionID))
			Expect(status).To(Equal(http.StatusNoContent), "deleting %s?versionId=%s through radosgw: %s", v.Key, v.VersionID, body)
		}
		tryAdmin(ctx, "bucket", "check", "olh", "--bucket", name, "--fix", "--rgw-olh-pending-timeout-sec", "0", "--hide-progress")
		_, err := radosgw.s3.DeleteBucket(ctx, &awss3.DeleteBucketInput{Bucket: aws.String(name)})
		Expect(err).NotTo(HaveOccurred(), "deleting %s through radosgw", name)
		gcProcess(ctx)
		Expect(markerOIDs(ctx, marker)).To(BeEmpty(), "%s's data after its removal", name)
	}, NodeTimeout(5*time.Minute))
	return name
}

// setVersioning is PutBucketVersioning of bucket through g.
func setVersioning(ctx context.Context, g vGateway, bucket string, status s3types.BucketVersioningStatus) {
	GinkgoHelper()
	_, err := g.s3.PutBucketVersioning(ctx, &awss3.PutBucketVersioningInput{
		Bucket: aws.String(bucket), VersioningConfiguration: &s3types.VersioningConfiguration{Status: status},
	})
	Expect(err).NotTo(HaveOccurred(), "PutBucketVersioning %s of %s through %s", status, bucket, g.name)
}

// putKey writes body as key through g and returns the version id the
// answer names, "" for none.
func putKey(ctx context.Context, g vGateway, bucket, key string, body []byte) string {
	GinkgoHelper()
	out, err := g.s3.PutObject(ctx, &awss3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String(key), Body: bytes.NewReader(body)})
	Expect(err).NotTo(HaveOccurred(), "PutObject %s/%s through %s", bucket, key, g.name)
	return aws.ToString(out.VersionId)
}

// putVersion is putKey on a bucket whose versioning is enabled, which
// answers a new version's id.
func putVersion(ctx context.Context, g vGateway, bucket, key string, body []byte) string {
	GinkgoHelper()
	v := putKey(ctx, g, bucket, key, body)
	Expect(v).To(MatchRegexp(versionIDShape), "the version PutObject of %s through %s names", key, g.name)
	return v
}

// deleteKey is DeleteObject of key through g with no version: a delete
// marker on a versioned bucket. It returns the answer.
func deleteKey(ctx context.Context, g vGateway, bucket, key string) *awss3.DeleteObjectOutput {
	GinkgoHelper()
	out, err := g.s3.DeleteObject(ctx, &awss3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	Expect(err).NotTo(HaveOccurred(), "DeleteObject %s/%s through %s", bucket, key, g.name)
	return out
}

// deleteVersion deletes key's version through g, which must answer 204.
func deleteVersion(ctx context.Context, g vGateway, bucket, key, version string) {
	GinkgoHelper()
	status, body := vCall(ctx, g, http.MethodDelete, versionPath(bucket, key, version))
	Expect(status).To(Equal(http.StatusNoContent), "DeleteObject %s?versionId=%s through %s: %s", key, version, g.name, body)
}

// getVersion reads key's version through g, the current one for "", and
// returns its body and the version the answer names.
func getVersion(ctx context.Context, g vGateway, bucket, key, version string) ([]byte, string) {
	GinkgoHelper()
	in := &awss3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)}
	if version != "" {
		in.VersionId = aws.String(version)
	}
	out, err := g.s3.GetObject(ctx, in)
	Expect(err).NotTo(HaveOccurred(), "GetObject %s?versionId=%s through %s", key, version, g.name)
	b, err := io.ReadAll(out.Body)
	Expect(errors.Join(err, out.Body.Close())).NotTo(HaveOccurred(), "reading %s?versionId=%s through %s", key, version, g.name)
	return b, aws.ToString(out.VersionId)
}

// expectReadsBack requires each gateway to read body as key's version, and
// as its current version when current.
func expectReadsBack(ctx context.Context, gateways map[string]vGateway, bucket, key, version string, body []byte, current bool) {
	GinkgoHelper()
	for _, g := range gateways {
		got, _ := getVersion(ctx, g, bucket, key, version)
		Expect(md5Hex(got)).To(Equal(md5Hex(body)), "%s?versionId=%s through %s", key, version, g.name)
		if current {
			got, _ = getVersion(ctx, g, bucket, key, "")
			Expect(md5Hex(got)).To(Equal(md5Hex(body)), "%s's current version through %s", key, g.name)
		}
	}
}

// currentObjects is ListObjectsV2 of bucket through g, as key and ETag.
func currentObjects(ctx context.Context, g vGateway, bucket string) []string {
	GinkgoHelper()
	out, err := g.s3.ListObjectsV2(ctx, &awss3.ListObjectsV2Input{Bucket: aws.String(bucket)})
	Expect(err).NotTo(HaveOccurred(), "ListObjectsV2 of %s through %s", bucket, g.name)
	Expect(aws.ToBool(out.IsTruncated)).To(BeFalse(), "ListObjectsV2 of %s through %s", bucket, g.name)
	var objs []string
	for _, o := range out.Contents {
		objs = append(objs, aws.ToString(o.Key)+" "+strings.Trim(aws.ToString(o.ETag), `"`))
	}
	return objs
}

// listedData is the listing entry of a data version of key written with body.
func listedData(key, id string, latest bool, body []byte) listedVersion {
	return listedVersion{Key: key, VersionID: id, Latest: latest, ETag: md5Hex(body), Size: int64(len(body))}
}

// listedMarker is the listing entry of a delete marker of key.
func listedMarker(key, id string, latest bool) listedVersion {
	return listedVersion{Key: key, VersionID: id, Latest: latest, Marker: true}
}

// versioningAttrs is what rgw-go's PutObject stores for a request without
// headers by versioningUser: the attrs the S3 layer reads and the user's
// default ACL.
func versioningAttrs() map[string][]byte {
	GinkgoHelper()
	attrs := rgwgoAttrs(http.Header{})
	attrs[meta.AttrACL] = encodeAt(acl.DefaultPolicy(meta.UserOwner(meta.UserID{ID: versioningUser}), "admin gate "+versioningUser))
	return attrs
}

// radosgwOption is one of radosgw's options a spec sets with ceph config
// set, and the value config show reads for it once it is removed.
type radosgwOption struct{ name, value, def string }

// radosgwShows waits for the running radosgw to report want for o.
func radosgwShows(ctx context.Context, entity string, o radosgwOption, want string) {
	GinkgoHelper()
	Eventually(func(g Gomega) string {
		out, err := cephOut(ctx, "config", "show", entity, o.name)
		g.Expect(err).NotTo(HaveOccurred())
		return out
	}).WithContext(ctx).WithTimeout(time.Minute).WithPolling(time.Second).Should(Equal(want), "radosgw's %s", o.name)
}

// removeRadosgwOptions removes opts from radosgw's config entity and waits
// for the running radosgw to report their defaults. A config rm of an
// option that is not set succeeds, so it can run any number of times.
func removeRadosgwOptions(ctx context.Context, entity string, opts ...radosgwOption) {
	GinkgoHelper()
	for _, o := range opts {
		tryCeph(ctx, "config", "rm", entity, o.name)
	}
	for _, o := range opts {
		radosgwShows(ctx, entity, o, o.def)
	}
}

// clearOLHInjections removes olhInjections from radosgw's config entity and
// waits for radosgw to report their defaults. A container that writes
// versions through radosgw runs it first: Ginkgo shuffles the containers,
// and a run killed while an injection was set leaves every versioned write
// of radosgw's failing.
func clearOLHInjections(ctx context.Context) {
	GinkgoHelper()
	_, entity := radosgwDeployment(ctx)
	removeRadosgwOptions(ctx, entity, olhInjections...)
}

// setRadosgwOptions sets opts on radosgw's config entity and waits for the
// running radosgw to report each, which it reads on each request once it
// holds it. They are removed when the spec ends, whatever a removal the
// spec made itself did, so that the specs after it meet a radosgw without
// them.
func setRadosgwOptions(ctx context.Context, entity string, opts ...radosgwOption) {
	GinkgoHelper()
	DeferCleanup(func(ctx context.Context) { removeRadosgwOptions(ctx, entity, opts...) }, NodeTimeout(3*time.Minute))
	for _, o := range opts {
		cephCmd(ctx, "config", "set", entity, o.name, o.value)
	}
	for _, o := range opts {
		radosgwShows(ctx, entity, o, o.value)
	}
}

// abRun is what one direction of the cross-gateway version history leaves:
// its bucket, listing and index, and its keys' OLH heads.
type abRun struct {
	bucket string
	ls     []listedVersion
	ix     bucketIndex
	heads  map[string]olhHead
}

// The versioning gate holds rgw-go and the Rook store's radosgw, on one
// zone, to one another: each writes and deletes versions the other reads,
// lists and deletes, and radosgw-admin's index and its bucket checks must
// show one history whichever gateway made it.
var _ = Describe("the versioning gate", Ordered, ContinueOnFailure, Label("integration", "rgw-go", "phase2", "versioning"), func() {
	var gateways map[string]vGateway

	BeforeAll(func(ctx SpecContext) {
		clearOLHInjections(ctx)
		removeUserFixtures(ctx, versioningUser)
		DeferCleanup(removeUserFixtures, versioningUser)
		createUser(ctx, versioningUser, versioningKey)
		gateways = newVGateways(versioningKey)
	})

	// One gateway writes two versions of obj and a delete marker over them,
	// one version of gone, and with versioning suspended the null version of
	// obj and a null delete marker of gone, which it then deletes. The other
	// reads every version, lists them whole and a version at a time, deletes
	// obj's first version and makes its second public-read. Both directions
	// run, and must leave one index.
	It("keeps one version history whichever gateway writes it and whichever reads, lists, deletes and sets an ACL on it", func(ctx SpecContext) {
		run := func(writer string) abRun {
			GinkgoHelper()
			w, r := gateways[writer], gateways[wpOtherGateway(writer)]
			bucket := versioningBucket(ctx, gateways["radosgw"])
			b1, b2, b3, b4 := []byte("obj's first version"), []byte("obj's second version"), []byte("obj's null version"), []byte("gone's version")

			setVersioning(ctx, w, bucket, s3types.BucketVersioningStatusEnabled)
			v1 := putVersion(ctx, w, bucket, "obj", b1)
			v2 := putVersion(ctx, w, bucket, "obj", b2)
			dm := deleteKey(ctx, w, bucket, "obj")
			Expect(aws.ToBool(dm.DeleteMarker)).To(BeTrue(), "the delete %s wrote is a marker", writer)
			m1 := aws.ToString(dm.VersionId)
			Expect(m1).To(MatchRegexp(versionIDShape), "the marker's version")
			g1 := putVersion(ctx, w, bucket, "gone", b4)
			setVersioning(ctx, w, bucket, s3types.BucketVersioningStatusSuspended)
			putKey(ctx, w, bucket, "obj", b3)
			Expect(aws.ToBool(deleteKey(ctx, w, bucket, "gone").DeleteMarker)).To(BeTrue(), "the null delete marker %s wrote", writer)
			deleteVersion(ctx, w, bucket, "gone", "null")

			By("reading every version through " + r.name)
			expectReadsBack(ctx, map[string]vGateway{r.name: r}, bucket, "obj", v1, b1, false)
			expectReadsBack(ctx, map[string]vGateway{r.name: r}, bucket, "obj", v2, b2, false)
			expectReadsBack(ctx, map[string]vGateway{r.name: r}, bucket, "obj", "null", b3, true)
			expectReadsBack(ctx, map[string]vGateway{r.name: r}, bucket, "gone", g1, b4, true)

			By("listing them through both, whole and a version at a time")
			Expect(expectSameVersions(ctx, gateways, bucket)).To(Equal([]listedVersion{
				listedData("gone", g1, true, b4),
				listedData("obj", "null", true, b3), listedMarker("obj", m1, false), listedData("obj", v2, false, b2), listedData("obj", v1, false, b1),
			}))
			for _, g := range gateways {
				Expect(currentObjects(ctx, g, bucket)).To(Equal([]string{"gone " + md5Hex(b4), "obj " + md5Hex(b3)}), "the current objects through %s", g.name)
			}

			By("deleting obj's noncurrent first version and setting an ACL on its second through " + r.name)
			deleteVersion(ctx, r, bucket, "obj", v1)
			_, err := r.s3.PutObjectAcl(ctx, &awss3.PutObjectAclInput{
				Bucket: aws.String(bucket), Key: aws.String("obj"), VersionId: aws.String(v2), ACL: s3types.ObjectCannedACLPublicRead,
			})
			Expect(err).NotTo(HaveOccurred(), "PutObjectAcl of obj?versionId=%s through %s", v2, r.name)
			out, err := w.s3.GetObjectAcl(ctx, &awss3.GetObjectAclInput{Bucket: aws.String(bucket), Key: aws.String("obj"), VersionId: aws.String(v2)})
			Expect(err).NotTo(HaveOccurred(), "GetObjectAcl of obj?versionId=%s through %s", v2, w.name)
			var grants []string
			for _, gr := range out.Grants {
				if gr.Grantee != nil {
					grants = append(grants, aws.ToString(gr.Grantee.URI)+" "+string(gr.Permission))
				}
			}
			Expect(grants).To(ContainElement(allUsers+" "+string(s3types.PermissionRead)),
				"%s's grants of the version %s made public-read", w.name, r.name)
			anonymous := func(path string) int {
				req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.url+path, http.NoBody)
				Expect(err).NotTo(HaveOccurred())
				resp, err := rawClient.Do(req)
				Expect(err).NotTo(HaveOccurred())
				Expect(resp.Body.Close()).To(Succeed())
				return resp.StatusCode
			}
			Expect(anonymous(versionPath(bucket, "obj", v2))).To(Equal(http.StatusOK), "an anonymous GET of the public version through %s", w.name)
			Expect(anonymous(versionPath(bucket, "obj", ""))).To(Equal(http.StatusForbidden), "an anonymous GET of the private current version through %s", w.name)

			ls, ix := expectQuiescent(ctx, gateways, bucket)
			Expect(ls).To(Equal([]listedVersion{
				listedData("gone", g1, true, b4),
				listedData("obj", "null", true, b3), listedMarker("obj", m1, false), listedData("obj", v2, false, b2),
			}))
			// cls_rgw's start_modify gives a key's links and unlinks the
			// epochs from 2 in turn: obj's two versions, its marker, its null
			// version and the unlink of its first version; gone's version,
			// the null marker and its unlink
			// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1503-L1517 at v19.2.7,
			// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1630-L1644 at v20.2.4).
			// rgw_bucket_unlink_instance's unlink of the current marker makes
			// g1 current through a BIVerObjEntry that has not read g1's entry
			// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1936-L1958 at v19.2.7,
			// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2083-L2105 at v20.2.4):
			// its write sets the unlink's epoch, and then write_entries' init
			// reads the stored entry over it, so g1 keeps its own epoch, 2
			// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1388-L1427 at v19.2.7,
			// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1514-L1554 at v20.2.4).
			Expect(epochs(ix, ls)).To(Equal(map[string]uint64{
				"gone#0": 2, "obj#0": 5, "obj#1": 4, "obj#2": 3, "olh gone": 4, "olh obj": 6,
			}), "the versioned epochs %s's history left", writer)
			heads := map[string]olhHead{}
			for _, v := range ls {
				if v.Latest {
					expectOLHHeadOn(ctx, bucket, v)
					heads[v.Key] = olhHeadOf(ctx, bucket, v.Key)
				}
			}
			expectBucketChecksClean(ctx, bucket)
			return abRun{bucket: bucket, ls: ls, ix: ix, heads: heads}
		}

		byRadosgw, byRGWGo := run("radosgw"), run("rgw-go")
		// By construction the two runs differ only in their buckets and
		// version ids, and in what the shapes leave out. Both sign as
		// versioningUser and write the same bodies, so the owners, ETags and
		// sizes the shapes keep must match.
		Expect(listingShape(byRGWGo.ls)).To(Equal(listingShape(byRadosgw.ls)), "the listing of the history rgw-go wrote against radosgw's")
		Expect(indexShape(byRGWGo.ix, byRGWGo.ls)).To(Equal(indexShape(byRadosgw.ix, byRadosgw.ls)),
			"bi list of the history rgw-go wrote against radosgw's")
		for key, h := range byRadosgw.heads {
			Expect(olhShape(byRGWGo.heads[key], byRGWGo.ls)).To(Equal(olhShape(h, byRadosgw.ls)), "%s's OLH head", key)
		}

		// radosgw-admin olh get decodes olh.ver as an RGWOLHInfo on Squid,
		// which fails on the decimal epoch the head holds, and olh.info on
		// Tentacle (docs/ceph-upstream-bugs.md, "radosgw-admin olh get
		// decodes olh.ver on Squid").
		for _, r := range []abRun{byRadosgw, byRGWGo} {
			out, err := radosgwAdmin(ctx, "olh", "get", "--bucket", r.bucket, "--object", "obj")
			if store.Release() < denc.Tentacle {
				// The decode failure's own log line lands on stderr between
				// the message and its errno.
				Expect(err).To(MatchError(And(ContainSubstring("failed reading olh: "), ContainSubstring("(5) Input/output error"))),
					"Squid's olh get of %s/obj, olh.ver %q: %s", r.bucket, r.heads["obj"].ver, out)
				continue
			}
			Expect(err).NotTo(HaveOccurred(), "olh get of %s/obj", r.bucket)
			var got struct {
				Target struct {
					Key meta.ObjKey `json:"key"`
				} `json:"target"`
				Removed bool `json:"removed"`
			}
			Expect(json.Unmarshal(out, &got)).To(Succeed(), "olh get: %s", out)
			want := r.heads["obj"].info
			Expect(got.Target.Key).To(Equal(want.Target.Key), "olh get's target of %s/obj against its olh.info", r.bucket)
			Expect(got.Removed).To(Equal(want.Removed), "olh get's removed of %s/obj", r.bucket)
		}
	}, SpecTimeout(12*time.Minute))

	It("keeps every version of one key that both gateways write and delete at once", func(ctx SpecContext) {
		const key = "raced"
		bucket := versioningBucket(ctx, gateways["radosgw"])
		setVersioning(ctx, gateways["rgw-go"], bucket, s3types.BucketVersioningStatusEnabled)
		via := func(i int) vGateway { return gateways[[]string{"radosgw", "rgw-go"}[i%2]] }

		type outcome struct {
			version         string
			deleted, marker bool
			status          int
			err             error
		}
		// race sends n requests at once, each as do makes it, and waits for
		// all of them to answer.
		race := func(n int, do func(i int) outcome) []outcome {
			GinkgoHelper()
			got := make([]outcome, n)
			start, done := make(chan struct{}), make(chan struct{})
			var wg sync.WaitGroup
			for i := range n {
				wg.Go(func() {
					<-start
					got[i] = do(i)
				})
			}
			go func() {
				wg.Wait()
				close(done)
			}()
			close(start)
			Eventually(done).WithTimeout(3 * time.Minute).WithPolling(100 * time.Millisecond).Should(BeClosed())
			return got
		}
		bodies := map[string][]byte{}
		var markers []string
		put := func(i int) outcome {
			out, err := via(i).s3.PutObject(ctx, &awss3.PutObjectInput{
				Bucket: aws.String(bucket), Key: aws.String(key), Body: bytes.NewReader(racedBody(i)),
			})
			if err != nil {
				return outcome{err: err}
			}
			return outcome{version: aws.ToString(out.VersionId)}
		}
		record := func(what string, i int, o outcome) {
			GinkgoHelper()
			Expect(o.err).NotTo(HaveOccurred(), "%s %d through %s", what, i, via(i).name)
			Expect(o.version).To(MatchRegexp(versionIDShape), "%s %d through %s", what, i, via(i).name)
			Expect(o.marker).To(Equal(o.deleted), "%s %d through %s answers a delete marker", what, i, via(i).name)
			if o.marker {
				markers = append(markers, o.version)
				return
			}
			Expect(bodies).NotTo(HaveKey(o.version), "%s %d's version", what, i)
			bodies[o.version] = racedBody(i)
		}

		By("twelve PUTs of the key, split across the gateways")
		for i, o := range race(12, put) {
			record("PUT", i, o)
		}

		By("two PUTs and two DELETEs of the key, one of each through each gateway")
		for i, o := range race(4, func(i int) outcome {
			if i < 2 {
				return put(100 + i)
			}
			out, err := via(i).s3.DeleteObject(ctx, &awss3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
			if err != nil {
				return outcome{err: err, deleted: true}
			}
			return outcome{version: aws.ToString(out.VersionId), deleted: true, marker: aws.ToBool(out.DeleteMarker)}
		}) {
			if i < 2 {
				i += 100
			}
			record("PUT or DELETE", i, o)
		}

		By("two DELETEs of the current version, one through each gateway")
		current := listVersions(ctx, gateways["radosgw"], bucket, 1000)[0]
		deletes := race(2, func(i int) outcome {
			status, _, err := vRequest(ctx, via(i), http.MethodDelete, versionPath(bucket, key, current.VersionID))
			return outcome{status: status, err: err}
		})
		for i, o := range deletes {
			Expect(o.err).NotTo(HaveOccurred(), "DELETE %d of %s through %s", i, current.VersionID, via(i).name)
			Expect(o.status).To(Equal(http.StatusNoContent), "DELETE %d of %s through %s", i, current.VersionID, via(i).name)
		}

		ls, _ := expectQuiescent(ctx, gateways, bucket)
		want := slices.Concat(slices.Collect(maps.Keys(bodies)), markers)
		want = slices.DeleteFunc(want, func(v string) bool { return v == current.VersionID })
		var listed []string
		for _, v := range ls {
			listed = append(listed, v.VersionID)
		}
		Expect(listed).To(ConsistOf(want), "every version the racing requests wrote, but the one deleted twice")
		expectOLHHeadOn(ctx, bucket, ls[0])
		for _, v := range ls {
			if !v.Marker {
				expectReadsBack(ctx, gateways, bucket, key, v.VersionID, bodies[v.VersionID], v.Latest)
			}
		}
		if ls[0].Marker {
			for _, g := range gateways {
				_, err := g.s3.GetObject(ctx, &awss3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
				Expect(statusOf(err)).To(Equal(http.StatusNotFound), "the current delete marker through %s", g.name)
			}
		}
		expectBucketChecksClean(ctx, bucket)
	}, SpecTimeout(10*time.Minute))

	Describe("interrupted writers", func() {
		// radosgw's set_olh fails its link with the errno
		// rgw_debug_inject_set_olh_err holds, after the version's head and
		// instance entry are written and its OLH modification began
		// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8707-L8714 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9453-L9455 at v20.2.4),
		// and with rgw_debug_inject_olh_cancel_modification_err set its
		// olh_cancel_modification leaves the modification's pending attr
		// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L7758-L7761 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L8421 at v20.2.4).
		// The upstream workunits inject ENOENT. The PUT's RadosWriter then
		// removes the tails it wrote but not the head, which the head write
		// made
		// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_putobj_processor.cc#L184-L228 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_putobj_processor.cc#L212-L257 at v20.2.4),
		// so a body within the head leaves a whole version no listing shows.
		DescribeTable("reads, lists and writes past a version whose link radosgw failed, as radosgw does",
			func(ctx SpecContext, keepPending bool, next string) {
				rg := gateways["radosgw"]
				bucket := versioningBucket(ctx, rg)
				setVersioning(ctx, rg, bucket, s3types.BucketVersioningStatusEnabled)
				first, failed, third := []byte("the version before the failed link"), []byte("the version whose link fails"),
					[]byte("the next write, through "+next)
				v1 := putVersion(ctx, rg, bucket, "obj", first)

				_, entity := radosgwDeployment(ctx)
				opts := olhInjections[:1]
				if keepPending {
					opts = olhInjections
				}
				setRadosgwOptions(ctx, entity, opts...)
				_, err := rg.s3.PutObject(ctx, &awss3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String("obj"), Body: bytes.NewReader(failed)})
				Expect(err).To(HaveOccurred(), "radosgw's PUT whose link fails")
				AddReportEntry("radosgw's status for the PUT whose link failed", statusOf(err))
				removeRadosgwOptions(ctx, entity, opts...)

				ls := listVersions(ctx, rg, bucket, 1000)
				Expect(ls).To(Equal([]listedVersion{listedData("obj", v1, true, first)}), "the versions listing once the link failed")
				var orphans []biDir
				for _, d := range bucketIndexOf(ctx, bucket).Instance {
					if d.Instance != v1 {
						orphans = append(orphans, d)
					}
				}
				Expect(orphans).To(HaveExactElements(And(HaveField("VersionedEpoch", BeZero()), HaveField("Meta.ETag", md5Hex(failed)))),
					"the instance entry of the version whose link failed")
				orphan := orphans[0].Instance
				before := olhHeadOf(ctx, bucket, "obj")
				Expect(indexInstance(before.info.Target.Key.Instance)).To(Equal(v1), "olh.info once the link failed")
				if keepPending {
					Expect(before.pending).To(HaveLen(1), "the pending attr the failed cancel left")
				} else {
					Expect(before.pending).To(BeEmpty(), "the pending attrs once the cancel ran")
				}

				By("reading and listing through rgw-go")
				rgwgo := gateways["rgw-go"]
				body, id := getVersion(ctx, rgwgo, bucket, "obj", "")
				Expect(body).To(Equal(first), "rgw-go's read of obj")
				Expect(id).To(Equal(v1), "the version rgw-go's read names")
				Expect(listVersions(ctx, rgwgo, bucket, 1000)).To(Equal(ls), "rgw-go's versions listing")
				Expect(currentObjects(ctx, rgwgo, bucket)).To(Equal([]string{"obj " + md5Hex(first)}), "rgw-go's listing")

				By("writing the next version through " + next)
				v3 := putVersion(ctx, gateways[next], bucket, "obj", third)
				ls = expectSameVersions(ctx, gateways, bucket)
				Expect(ls).To(Equal([]listedVersion{listedData("obj", v3, true, third), listedData("obj", v1, false, first)}))
				expectReadsBack(ctx, gateways, bucket, "obj", v3, third, true)
				Eventually(func(g Gomega) []string {
					ix, err := tryBucketIndex(ctx, bucket)
					g.Expect(err).NotTo(HaveOccurred())
					return indexFindings(ix, ls)
				}).WithContext(ctx).WithTimeout(time.Minute).WithPolling(time.Second).Should(ConsistOf(
					"obj has 2 list and 3 instance entries for 2 listed versions"), "the index against the listing: the unlinked instance alone")
				expectBucketCheckClean(ctx, bucket)
				Expect(unlinkedInstances(ctx, bucket)).To(ConsistOf(unlinkedInstance{Name: "obj", Instance: orphan}), "bucket check unlinked")
				Expect(leftoverOLH(ctx, bucket)).To(BeEmpty(), "bucket check olh")
				after := olhHeadOf(ctx, bucket, "obj")
				Expect(indexInstance(after.info.Target.Key.Instance)).To(Equal(v3), "olh.info once %s wrote", next)
				// No log entry names the failed write's op tag, so no replay
				// removes its pending attr before rgw_olh_pending_timeout_sec,
				// an hour by default.
				Expect(after.pending).To(Equal(before.pending), "the pending attrs once %s wrote", next)
			},
			Entry("the next write rgw-go's", false, "rgw-go", SpecTimeout(8*time.Minute)),
			Entry("the next write radosgw's own", false, "radosgw", SpecTimeout(8*time.Minute)),
			Entry("its pending attr left too, the next write rgw-go's", true, "rgw-go", SpecTimeout(8*time.Minute)),
			Entry("its pending attr left too, the next write radosgw's own", true, "radosgw", SpecTimeout(8*time.Minute)),
		)

		// radosgw's read of a key whose OLH head holds a pending modification
		// replays the key's log and then serves the version olh.info named
		// before the replay (docs/ceph-upstream-bugs.md, "radosgw's
		// follow_olh answers the version that was current before the replay
		// it runs"): the previous version after a link, and, after the
		// unlink of the current version, which the replay removes, 404
		// NoSuchKey.
		DescribeTable("finishes rgw-go's versioned write stopped after its link on radosgw's next read",
			func(ctx SpecContext, deletes bool) {
				rg := gateways["radosgw"]
				bucket := versioningBucket(ctx, rg)
				setVersioning(ctx, rg, bucket, s3types.BucketVersioningStatusEnabled)
				first, second := []byte("the first version"), []byte("the second version, "+strconv.FormatBool(deletes))
				v1 := putVersion(ctx, rg, bucket, "obj", first)
				var v2 string
				if deletes {
					v2 = putVersion(ctx, rg, bucket, "obj", second)
				}

				stopped := open(ctx, connect(ctx))
				stopped.StopAfterOLHLinkForTest(true)
				rec, err := stopped.GetBucket(ctx, "", bucket)
				Expect(err).NotTo(HaveOccurred())
				before := v1
				if deletes {
					before = v2
					_, err = stopped.DeleteObject(ctx, rec, meta.ObjKey{Name: "obj", Instance: v2}, op.DeleteParams{})
					Expect(err).NotTo(HaveOccurred(), "rgw-go's delete of %s, stopped after its unlink", v2)
				} else {
					v2 = meta.NewInstance()
					_, err = stopped.PutObject(ctx, rec, meta.ObjKey{Name: "obj", Instance: v2}, bytes.NewReader(second),
						op.PutParams{Attrs: versioningAttrs(), Size: int64(len(second))})
					Expect(err).NotTo(HaveOccurred(), "rgw-go's write of %s, stopped after its link", v2)
				}
				h := olhHeadOf(ctx, bucket, "obj")
				Expect(h.pending).To(HaveLen(1), "the stopped writer's pending attr")
				Expect(indexInstance(h.info.Target.Key.Instance)).To(Equal(before), "olh.info before any replay")
				ix := bucketIndexOf(ctx, bucket)
				Expect(ix.OLH).To(HaveExactElements(HaveField("PendingLog", HaveLen(1))), "the OLH entry's log before any replay")

				By("reading obj through radosgw")
				// What the replaying read answers is checked last, so that the
				// replay's own state is checked whatever it answered.
				out, readErr := rg.s3.GetObject(ctx, &awss3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("obj")})
				var read []byte
				if readErr == nil {
					var rerr error
					read, rerr = io.ReadAll(out.Body)
					Expect(errors.Join(rerr, out.Body.Close())).NotTo(HaveOccurred())
				}

				ls, _ := expectQuiescent(ctx, gateways, bucket)
				if deletes {
					Expect(ls).To(Equal([]listedVersion{listedData("obj", v1, true, first)}))
					expectReadsBack(ctx, gateways, bucket, "obj", v1, first, true)
					_, err := rawStat(ctx, meta.Stripe{Obj: meta.Obj{Bucket: rec.Info.Bucket, Key: meta.ObjKey{Name: "obj", Instance: v2}}}.OID())
					Expect(isNotFound(err)).To(BeTrue(), "the deleted version's head: %v", err)
				} else {
					Expect(ls).To(Equal([]listedVersion{listedData("obj", v2, true, second), listedData("obj", v1, false, first)}))
					expectReadsBack(ctx, gateways, bucket, "obj", v2, second, true)
				}
				expectOLHHeadOn(ctx, bucket, ls[0])
				expectBucketChecksClean(ctx, bucket)

				if deletes {
					Expect(statusOf(readErr)).To(Equal(http.StatusNotFound), "radosgw's read that replays the unlink of %s: %v", v2, readErr)
					return
				}
				Expect(readErr).NotTo(HaveOccurred(), "radosgw's read that replays the link of %s", v2)
				Expect(read).To(Equal(first), "radosgw's read that replays the link of %s", v2)
				Expect(aws.ToString(out.VersionId)).To(Equal(v1), "the version radosgw's replaying read names")
			},
			Entry("a write", false, SpecTimeout(6*time.Minute)),
			Entry("a delete of the current version", true, SpecTimeout(6*time.Minute)),
		)
	})

	DescribeTable("versions an object one gateway wrote before versioning, as the other enables versioning and writes",
		func(ctx SpecContext, writer string) {
			w, o := gateways[writer], gateways[wpOtherGateway(writer)]
			bucket := versioningBucket(ctx, gateways["radosgw"])
			old, newer := []byte("written by "+w.name+" before versioning"), []byte("the first version, written by "+o.name)
			Expect(putKey(ctx, w, bucket, "obj", old)).To(BeEmpty(), "the version a PUT on a never-versioned bucket names")
			setVersioning(ctx, o, bucket, s3types.BucketVersioningStatusEnabled)
			v1 := putVersion(ctx, o, bucket, "obj", newer)

			ls, ix := expectQuiescent(ctx, gateways, bucket)
			Expect(ls).To(Equal([]listedVersion{listedData("obj", v1, true, newer), listedData("obj", "null", false, old)}))
			// The plain entry becomes the null version at epoch 1, and the
			// key's first link takes 2
			// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1606 and
			// https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1510-L1511 at v19.2.7,
			// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1735 and
			// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1637-L1638 at v20.2.4).
			Expect(epochs(ix, ls)).To(Equal(map[string]uint64{"obj#0": 2, "obj#1": 1, "olh obj": 2}))
			expectOLHHeadOn(ctx, bucket, ls[0])
			expectReadsBack(ctx, gateways, bucket, "obj", "null", old, false)
			expectReadsBack(ctx, gateways, bucket, "obj", v1, newer, true)

			By("deleting the null version through " + o.name)
			deleteVersion(ctx, o, bucket, "obj", "null")
			ls, _ = expectQuiescent(ctx, gateways, bucket)
			Expect(ls).To(Equal([]listedVersion{listedData("obj", v1, true, newer)}))
			for _, g := range gateways {
				_, err := g.s3.GetObject(ctx, &awss3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("obj"), VersionId: aws.String("null")})
				Expect(statusOf(err)).To(Equal(http.StatusNotFound), "the deleted null version through %s", g.name)
			}
			expectReadsBack(ctx, gateways, bucket, "obj", v1, newer, true)
			expectBucketChecksClean(ctx, bucket)
		},
		Entry("written by radosgw, versioned by rgw-go", "radosgw", SpecTimeout(6*time.Minute)),
		Entry("written by rgw-go, versioned by radosgw", "rgw-go", SpecTimeout(6*time.Minute)),
	)

	DescribeTable("takes PutBucketVersioning through one gateway on the other's next PUT",
		func(ctx SpecContext, setter string) {
			s, p := gateways[setter], gateways[wpOtherGateway(setter)]
			bucket := versioningBucket(ctx, gateways["radosgw"])
			b0, b1, b2, b3 := []byte("before versioning"), []byte("the first version"), []byte("the suspended null version"), []byte("the version after")
			// The PUT before versioning has p read the bucket, so that the
			// changes below reach a bucket p holds.
			Expect(putKey(ctx, p, bucket, "obj", b0)).To(BeEmpty(), "the version a PUT on a never-versioned bucket names")
			setVersioning(ctx, s, bucket, s3types.BucketVersioningStatusEnabled)
			v1 := putVersion(ctx, p, bucket, "obj", b1)
			setVersioning(ctx, s, bucket, s3types.BucketVersioningStatusSuspended)
			putKey(ctx, p, bucket, "obj", b2)
			setVersioning(ctx, s, bucket, s3types.BucketVersioningStatusEnabled)
			v3 := putVersion(ctx, p, bucket, "obj", b3)
			out, err := p.s3.GetBucketVersioning(ctx, &awss3.GetBucketVersioningInput{Bucket: aws.String(bucket)})
			Expect(err).NotTo(HaveOccurred(), "GetBucketVersioning through %s", p.name)
			Expect(out.Status).To(Equal(s3types.BucketVersioningStatusEnabled), "GetBucketVersioning through %s", p.name)

			ls, _ := expectQuiescent(ctx, gateways, bucket)
			Expect(ls).To(Equal([]listedVersion{listedData("obj", v3, true, b3), listedData("obj", "null", false, b2), listedData("obj", v1, false, b1)}),
				"the suspended PUT replaced the null version the PUT before versioning made")
			expectOLHHeadOn(ctx, bucket, ls[0])
			expectReadsBack(ctx, gateways, bucket, "obj", "null", b2, false)
			expectReadsBack(ctx, gateways, bucket, "obj", v1, b1, false)
			expectReadsBack(ctx, gateways, bucket, "obj", v3, b3, true)
			expectBucketChecksClean(ctx, bucket)
		},
		Entry("set through radosgw, written through rgw-go", "radosgw", SpecTimeout(6*time.Minute)),
		Entry("set through rgw-go, written through radosgw", "rgw-go", SpecTimeout(6*time.Minute)),
	)

	It("keeps every version rgw-go writes and deletes while radosgw-admin reshards the bucket", func(ctx SpecContext) {
		rgwgo := gateways["rgw-go"]
		bucket := versioningBucket(ctx, gateways["radosgw"])
		// Canceling the spec's context ends admin.sh but not the toolbox's
		// radosgw-admin, so a spec that fails while the reshard runs waits
		// for it before the bucket's removal.
		resharded, reshardStarted := make(chan struct{}), false
		DeferCleanup(func(ctx SpecContext) {
			if reshardStarted {
				Eventually(resharded).WithContext(ctx).WithTimeout(2*time.Minute).WithPolling(time.Second).Should(BeClosed(),
					"the reshard, before the bucket's removal")
			}
		}, NodeTimeout(3*time.Minute))
		setVersioning(ctx, rgwgo, bucket, s3types.BucketVersioningStatusEnabled)
		keys := []string{"k0", "k1", "k2", "k3"}
		type written struct{ key, version string }
		var live []written
		bodies := map[string][]byte{}
		put := func(i int) {
			GinkgoHelper()
			key := keys[i%len(keys)]
			body := []byte(key + " written by rgw-go, " + strconv.Itoa(i))
			v := putVersion(ctx, rgwgo, bucket, key, body)
			live = append(live, written{key, v})
			bodies[v] = body
		}
		for i := range 2 * len(keys) {
			put(i)
		}

		// The reshard waits in do_reshard for four seconds, which on Squid
		// holds the old index's writes blocked and on Tentacle has them
		// logged for the copy, so that rgw-go's requests meet it
		// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_reshard.cc#L972-L977 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_reshard.cc#L1294-L1299 at v20.2.4;
		// InjectDelay,
		// https://github.com/ceph/ceph/blob/v19.2.7/src/common/fault_injector.h#L39-L45 at v19.2.7).
		// rgw-go's requests run back to back from before the reshard starts
		// until four after it returns.
		var (
			reshardOut []byte
			reshardErr error
		)
		reshardStarted = true
		go func() {
			defer close(resharded)
			reshardOut, reshardErr = radosgwAdmin(ctx, "bucket", "reshard", "--bucket", bucket, "--num-shards", "23",
				"--yes-i-really-mean-it", "--inject-delay-at", "do_reshard", "--inject-delay-ms", "4000")
		}()
		deadline := time.Now().Add(5 * time.Minute)
		for i, after := 2*len(keys), 0; after < 4; i++ {
			select {
			case <-resharded:
				after++
			default:
			}
			Expect(time.Now()).To(BeTemporally("<", deadline), "the reshard outlasted rgw-go's writes")
			if i%3 == 2 {
				gone := live[0]
				live = live[1:]
				deleteVersion(ctx, rgwgo, bucket, gone.key, gone.version)
				delete(bodies, gone.version)
			} else {
				put(i)
			}
		}
		Eventually(resharded).WithTimeout(time.Minute).WithPolling(100 * time.Millisecond).Should(BeClosed())
		Expect(reshardErr).NotTo(HaveOccurred(), "bucket reshard: %s", reshardOut)

		var stats struct {
			NumShards uint32 `json:"num_shards"`
		}
		adminJSON(ctx, &stats, "bucket", "stats", "--bucket", bucket)
		Expect(stats.NumShards).To(BeEquivalentTo(23), "bucket stats' num_shards")
		ls, _ := expectQuiescent(ctx, gateways, bucket)
		var listed, want []string
		for _, v := range ls {
			listed = append(listed, v.Key+" "+v.VersionID)
		}
		for _, w := range live {
			want = append(want, w.key+" "+w.version)
		}
		Expect(listed).To(ConsistOf(want), "the versions rgw-go wrote and did not delete")
		for _, v := range ls {
			expectReadsBack(ctx, gateways, bucket, v.Key, v.VersionID, bodies[v.VersionID], v.Latest)
			if v.Latest {
				expectOLHHeadOn(ctx, bucket, v)
			}
		}
		expectBucketChecksClean(ctx, bucket)
	}, SpecTimeout(15*time.Minute))

	// Forty names each hold a version and, current, a delete marker, before
	// one visible version, on one shard. Each bucket_list call makes eight
	// omap reads
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L492 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L542 at v20.2.4);
	// asked for eight entries, they list nothing, and the class answers
	// RGWBIAdvanceAndRetryError with its reply and the marker to go on from
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L680-L693 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L730-L743 at v20.2.4).
	It("completes a listing without versions over more than eight reads of a real shard's markers and noncurrent versions", func(ctx SpecContext) {
		bucket := versioningBucket(ctx, gateways["radosgw"])
		admin(ctx, "bucket", "reshard", "--bucket", bucket, "--num-shards", "1", "--yes-i-really-mean-it")
		setVersioning(ctx, gateways["radosgw"], bucket, s3types.BucketVersioningStatusEnabled)
		for i := range 40 {
			g, name := gateways[[]string{"radosgw", "rgw-go"}[i%2]], fmt.Sprintf("d%02d", i)
			putVersion(ctx, g, bucket, name, []byte(name+" through "+g.name))
			Expect(aws.ToBool(deleteKey(ctx, g, bucket, name).DeleteMarker)).To(BeTrue(), "%s's delete marker", name)
		}
		zz := []byte("the one visible version")
		putVersion(ctx, gateways["rgw-go"], bucket, "zz", zz)

		rec, err := store.GetBucket(ctx, "", bucket)
		Expect(err).NotTo(HaveOccurred())
		rec = freshBucket(ctx, rec)
		Expect(rec.Info.Layout.Current.Layout.Normal.NumShards).To(BeEquivalentTo(1), "the bucket's shards")
		rop := radosclient.NewReadOp()
		r := rgwcls.BucketList(rop, rgwcls.ListOp{NumEntries: 8}, store.Release())
		_, err = pool(ctx, meta.Pool{Name: manifest.Pools.Index}).Read(ctx, rec.Info.IndexShardOID(rec.Info.Layout.Current, 0), rop, radosclient.OpFlagNone)
		Expect(err).To(MatchError(radosclient.ErrTooBig), "the class answers advance-and-retry with -EFBIG")
		ret, retry, err := r.Reply()
		Expect(err).NotTo(HaveOccurred())
		Expect(retry).To(BeTrue(), "the shard's first read answers advance-and-retry")
		Expect(ret.Dir.Entries).To(BeEmpty(), "the advance-and-retry reply's entries")
		Expect(ret.Marker.Name).To(MatchRegexp(`^d[0-9]{2}\x01$`), "the reply's marker, past a name's versions")

		// rgw_list_bucket_min_readahead of 1 has the driver ask the shard for
		// eight entries a round.
		small := open(ctx, connect(ctx, "--rgw-list-bucket-min-readahead=1"))
		srec, err := small.GetBucket(ctx, "", bucket)
		Expect(err).NotTo(HaveOccurred())
		for _, unordered := range []bool{false, true} {
			res, err := small.ListObjects(ctx, srec, op.ListObjectsParams{MaxKeys: 1, AllowUnordered: unordered})
			Expect(err).NotTo(HaveOccurred(), "rgw-go's listing, unordered %t", unordered)
			Expect(res.Entries).To(HaveExactElements(HaveField("Key.Name", "zz")), "rgw-go's listing, unordered %t", unordered)
			Expect(res.Truncated).To(BeFalse(), "rgw-go's listing, unordered %t", unordered)
		}
		for _, g := range gateways {
			Expect(currentObjects(ctx, g, bucket)).To(Equal([]string{"zz " + md5Hex(zz)}), "ListObjectsV2 through %s", g.name)
		}
		ls, _ := expectQuiescent(ctx, gateways, bucket)
		Expect(ls).To(HaveLen(81), "forty versions and markers, and zz")
		expectBucketChecksClean(ctx, bucket)
	}, SpecTimeout(12*time.Minute))
})

// racedBody is the body of racing write i, of a length of its own.
func racedBody(i int) []byte {
	return []byte(fmt.Sprintf("racing write %d %s", i, strings.Repeat("x", i)))
}
