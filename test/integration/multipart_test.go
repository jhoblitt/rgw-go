//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // radosgw's ETag is an MD5, not a security control
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/auth"
	"github.com/jhoblitt/rgw-go/internal/authz"
	"github.com/jhoblitt/rgw-go/internal/cephconf"
	"github.com/jhoblitt/rgw-go/internal/cls/lock"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/metrics"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/radosclient/goceph"
	"github.com/jhoblitt/rgw-go/internal/s3"
	"github.com/jhoblitt/rgw-go/internal/testutil/cephtest"
)

const (
	// mpBucket is the bucket the multipart oracle writes, created through
	// radosgw by alice.
	mpBucket = "mp-oracle"
	// completeLockName is the cls_lock lock RGWCompleteMultipart::execute
	// takes on the meta object (rgw_op.cc:6434 at v19.2.6, :7248 at
	// v20.2.4).
	completeLockName = "RGWCompleteMultipart"
	// completionInProgress is the message radosgw answers a completion
	// whose lock is held with (rgw_op.cc:6444 at v19.2.6, :7258 at v20.2.4).
	completionInProgress = "This multipart completion is already in progress"
	// uploadIDShape is a v2 upload id: "2~" and 31 characters of
	// gen_rand_alphanumeric's alphabet.
	uploadIDShape = `^2~[A-Za-z0-9_-]{31}$`
)

// rgwgoGateway is rgw-go's S3 handler served from the spec process over a
// RADOS connection of its own, with the authenticator, authorizer and
// metrics serve gives it, so that the RADOS operations its requests cost
// are counted apart from the specs' own reads.
type rgwgoGateway struct {
	url   string
	stats radosclient.StatsReporter
}

// serveRGWGo starts rgw-go's S3 handler on a loopback listener over a store
// of its own, its workers running, and stops it when the container ends.
func serveRGWGo(ctx context.Context) rgwgoGateway {
	GinkgoHelper()
	c := connect(ctx)
	s := open(ctx, c)
	runCtx, stop := context.WithCancel(context.WithoutCancel(ctx))
	done := make(chan error, 1)
	go func() { done <- s.Run(runCtx) }()
	DeferCleanup(func() {
		stop()
		Eventually(done).WithTimeout(30 * time.Second).WithPolling(100 * time.Millisecond).Should(Receive(Succeed()))
	})
	sr, ok := c.(radosclient.StatsReporter)
	Expect(ok).To(BeTrue(), "%T reports no RADOS stats", c)

	conf := cephconf.NewOptions(c)
	env := s.Env()
	azCfg, err := authz.ConfigFrom(conf, s.Release())
	Expect(err).NotTo(HaveOccurred())
	env.Authz = authz.New(azCfg)
	reg := metrics.New()
	reg.WatchRADOS(string(goceph.ModeCallback), sr)
	env.Metrics = reg
	zone := s.Zone()
	env.HostID = op.HostID(c.InstanceID(), zone.Name, s.ZoneGroup().Name)
	authCfg, err := auth.ConfigFrom(conf, nil)
	Expect(err).NotTo(HaveOccurred())
	srv := httptest.NewServer(s3.NewHandler(env, auth.New(authCfg, s, s), s3.Config{
		TransIDSuffix: op.TransIDSuffix(c.InstanceID(), zone.Name),
		ServerHeader:  "Ceph Object Gateway (" + s.Release().String() + ")",
	}))
	DeferCleanup(srv.Close)
	return rgwgoGateway{url: srv.URL, stats: sr}
}

// errNoSuchBucket is how radosgw-admin reports ERR_NO_SUCH_BUCKET
// (rgw_common.h:254 at v19.2.6, :276 at v20.2.4).
const errNoSuchBucket = "(2002)"

// removeLeftoverBucket removes bucket with its objects, its uploads and
// their garbage when a run that died before its cleanup left it. radosgw
// answers its owner's CreateBucket of a bucket that exists with 200
// (rgw_rest_s3.cc:2546 at v19.2.6, :2707 at v20.2.4), so the oracle would
// otherwise go on in a dirty bucket, and radosgw-admin bucket rm exits 0
// whether or not it removed anything (docs/ceph-upstream-bugs.md,
// "radosgw-admin bucket rm exits 0 when it removes nothing"), so bucket
// stats tells whether there is one and whether it went.
func removeLeftoverBucket(ctx context.Context, bucket string) {
	GinkgoHelper()
	out, err := cephtest.RadosgwAdmin(ctx, conf, "bucket", "stats", "--bucket", bucket)
	if err != nil {
		Expect(err).To(MatchError(ContainSubstring(errNoSuchBucket)), "bucket stats %s", bucket)
		return
	}
	var stats struct {
		Marker string `json:"marker"`
	}
	Expect(json.Unmarshal(out, &stats)).To(Succeed(), "bucket stats %s: %s", bucket, out)
	AddReportEntry("removed a leftover bucket", bucket)
	admin(ctx, "bucket", "rm", "--bucket", bucket, "--purge-objects")
	_, err = cephtest.RadosgwAdmin(ctx, conf, "bucket", "stats", "--bucket", bucket)
	Expect(err).To(MatchError(ContainSubstring(errNoSuchBucket)), "bucket stats %s after its removal", bucket)
	gcProcess(ctx)
	Expect(markerOIDs(ctx, stats.Marker)).To(BeEmpty(), "the leftover %s's data after its removal", bucket)
}

// mpClient is an S3 client of alice's against the gateway at url that
// sends a checksum only where an operation requires one, and never retries,
// so that each call is one request.
func mpClient(url string) *awss3.Client {
	u := alice()
	return awss3.New(awss3.Options{
		Region:                     "us-east-1",
		BaseEndpoint:               aws.String(url),
		UsePathStyle:               true,
		Credentials:                credentials.NewStaticCredentialsProvider(u.AccessKey, u.SecretKey, ""),
		RetryMaxAttempts:           1,
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	})
}

// createUpload starts an upload of key through c.
func createUpload(ctx context.Context, c *awss3.Client, key string) string {
	GinkgoHelper()
	out, err := c.CreateMultipartUpload(ctx, &awss3.CreateMultipartUploadInput{Bucket: aws.String(mpBucket), Key: aws.String(key)})
	Expect(err).NotTo(HaveOccurred(), "CreateMultipartUpload %s", key)
	id := aws.ToString(out.UploadId)
	Expect(id).To(MatchRegexp(uploadIDShape), "%s's upload id", key)
	return id
}

// uploadPart uploads body as part n of upload id of key through c.
func uploadPart(ctx context.Context, c *awss3.Client, key, id string, n int32, body []byte) s3types.CompletedPart {
	GinkgoHelper()
	out, err := c.UploadPart(ctx, &awss3.UploadPartInput{
		Bucket: aws.String(mpBucket), Key: aws.String(key), UploadId: aws.String(id), PartNumber: aws.Int32(n),
		Body: bytes.NewReader(body), ContentLength: aws.Int64(int64(len(body))),
	})
	Expect(err).NotTo(HaveOccurred(), "UploadPart %s part %d", key, n)
	Expect(aws.ToString(out.ETag)).To(Equal(`"`+md5Hex(body)+`"`), "%s part %d's ETag", key, n)
	return s3types.CompletedPart{PartNumber: aws.Int32(n), ETag: out.ETag}
}

// uploadParts uploads bodies as parts 1 to len(bodies) of upload id.
func uploadParts(ctx context.Context, c *awss3.Client, key, id string, bodies [][]byte) []s3types.CompletedPart {
	GinkgoHelper()
	parts := make([]s3types.CompletedPart, 0, len(bodies))
	for i, b := range bodies {
		parts = append(parts, uploadPart(ctx, c, key, id, int32(i+1), b)) //nolint:gosec // a handful of parts
	}
	return parts
}

// completeUpload completes upload id of key through c with parts.
func completeUpload(ctx context.Context, c *awss3.Client, key, id string, parts []s3types.CompletedPart) (*awss3.CompleteMultipartUploadOutput, error) {
	return c.CompleteMultipartUpload(ctx, &awss3.CompleteMultipartUploadInput{
		Bucket: aws.String(mpBucket), Key: aws.String(key), UploadId: aws.String(id),
		MultipartUpload: &s3types.CompletedMultipartUpload{Parts: parts},
	})
}

// mustComplete is completeUpload, which must succeed with the ETag the
// bodies make.
func mustComplete(ctx context.Context, c *awss3.Client, key, id string, parts []s3types.CompletedPart, bodies ...[]byte) string {
	GinkgoHelper()
	out, err := completeUpload(ctx, c, key, id, parts)
	Expect(err).NotTo(HaveOccurred(), "CompleteMultipartUpload %s", key)
	etag := mpETag(bodies...)
	Expect(aws.ToString(out.ETag)).To(Equal(`"`+etag+`"`), "%s's completed ETag", key)
	return etag
}

// mpETag is a multipart object's ETag: the MD5 of its parts' MD5s, a dash
// and the part count.
func mpETag(bodies ...[]byte) string {
	h := md5.New() //nolint:gosec // radosgw's ETag
	for _, b := range bodies {
		sum := md5.Sum(b) //nolint:gosec // radosgw's ETag
		h.Write(sum[:])
	}
	return hex.EncodeToString(h.Sum(nil)) + "-" + strconv.Itoa(len(bodies))
}

// apiError is what an S3 client's error says of the error document.
type apiError interface {
	error
	ErrorCode() string
	ErrorMessage() string
}

// expectCompletionInProgress requires err to be radosgw's answer to a
// completion whose RGWCompleteMultipart lock another client holds.
func expectCompletionInProgress(err error, who string) {
	GinkgoHelper()
	Expect(err).To(HaveOccurred(), "%s's completion under the held lock", who)
	Expect(statusOf(err)).To(Equal(http.StatusInternalServerError), "%s: %v", who, err)
	ae, ok := errors.AsType[apiError](err)
	Expect(ok).To(BeTrue(), "%s: %v carries no error document", who, err)
	Expect(ae.ErrorCode()).To(Equal("InternalError"), who)
	Expect(ae.ErrorMessage()).To(Equal(completionInProgress), who)
}

// keyHead is the head object of key in rec's bucket.
func keyHead(rec *op.BucketRecord, key string) string {
	return meta.Stripe{Obj: meta.Obj{Bucket: rec.Info.Bucket, Key: meta.ObjKey{Name: key}}}.OID()
}

// metaOID is the meta object of upload id of key in rec's bucket.
func metaOID(rec *op.BucketRecord, key, id string) string {
	return meta.Stripe{Obj: meta.Obj{Bucket: rec.Info.Bucket, Key: meta.ObjKey{Name: meta.MultipartMetaName(key, id), NS: meta.NSMultipart}}}.OID()
}

// nonECPool opens the placement's data-extra pool, where the meta objects
// are.
func nonECPool(ctx context.Context) radosclient.Pool {
	GinkgoHelper()
	return pool(ctx, meta.Pool{Name: manifest.Pools.NonEC})
}

// metaObjects is the data-extra pool's objects of rec's bucket.
func metaObjects(ctx context.Context, rec *op.BucketRecord) []string {
	GinkgoHelper()
	var oids []string
	for _, oid := range listOIDs(ctx, meta.Pool{Name: manifest.Pools.NonEC}) {
		if strings.HasPrefix(oid, rec.Info.Bucket.Marker+"_") {
			oids = append(oids, oid)
		}
	}
	return oids
}

// omapKeys is the omap keys of oid in the data-extra pool, as rados
// listomapkeys prints them.
func omapKeys(ctx context.Context, oid string) []string {
	GinkgoHelper()
	rop := radosclient.NewReadOp()
	res := rop.OmapGetKeys("", 1000)
	_, err := nonECPool(ctx).Read(ctx, oid, rop, radosclient.OpFlagNone)
	Expect(err).NotTo(HaveOccurred(), "listing %s's omap keys", oid)
	Expect(res.Err).NotTo(HaveOccurred())
	Expect(res.More).To(BeFalse())
	return res.Keys
}

// metaInfo decodes the multipart_upload_info the meta object oid holds.
func metaInfo(ctx context.Context, oid string) meta.MultipartUploadInfo {
	GinkgoHelper()
	rop := radosclient.NewReadOp()
	rd := rop.Read(0, 64*kib)
	_, err := nonECPool(ctx).Read(ctx, oid, rop, radosclient.OpFlagNone)
	Expect(err).NotTo(HaveOccurred(), "reading %s", oid)
	d := denc.NewDecoder(rd.Data[:rd.N])
	info := meta.DecodeMultipartUploadInfo(d)
	Expect(d.Err()).NotTo(HaveOccurred(), "decoding %s", oid)
	return info
}

// partObjects is the data pool's objects of rec's bucket that belong to the
// parts named by prefix, "<key>.<unique>": their heads in the multipart
// namespace and their shadow stripes.
func partObjects(ctx context.Context, rec *op.BucketRecord, prefix string) []string {
	GinkgoHelper()
	var oids []string
	for _, oid := range markerOIDs(ctx, rec.Info.Bucket.Marker) {
		rest := strings.TrimPrefix(oid, rec.Info.Bucket.Marker+"_")
		if strings.HasPrefix(rest, "_multipart_"+prefix+".") || strings.HasPrefix(rest, "_shadow_"+prefix+".") {
			oids = append(oids, oid)
		}
	}
	return oids
}

// storedManifest is rgw-go's decoding of the user.rgw.manifest attr of
// key's head, as RADOS holds it.
func storedManifest(ctx context.Context, rec *op.BucketRecord, key string) meta.Manifest {
	GinkgoHelper()
	b := rawXattrs(ctx, keyHead(rec, key))[meta.AttrManifest]
	Expect(b).NotTo(BeEmpty(), "%s's manifest", key)
	d := denc.NewDecoder(b)
	m := meta.DecodeManifest(d)
	Expect(d.Err()).NotTo(HaveOccurred(), "decoding %s's manifest", key)
	return m
}

// mpRule is one rule of object stat's manifest, RGWObjManifestRule::dump.
type mpRule struct {
	StartPartNum   uint32 `json:"start_part_num"`
	StartOfs       uint64 `json:"start_ofs"`
	PartSize       uint64 `json:"part_size"`
	StripeMaxSize  uint64 `json:"stripe_max_size"`
	OverridePrefix string `json:"override_prefix"`
}

// statRules is object stat's manifest rules by the offset each starts at.
func statRules(o adminObject) map[uint64]mpRule {
	GinkgoHelper()
	var m struct {
		Rules []struct {
			Key uint64 `json:"key"`
			Val mpRule `json:"val"`
		} `json:"rules"`
	}
	Expect(json.Unmarshal(o.Manifest, &m)).To(Succeed(), "object stat's manifest: %s", o.Manifest)
	out := map[uint64]mpRule{}
	for _, r := range m.Rules {
		out[r.Key] = r.Val
	}
	return out
}

// entriesOf is the names bucket list shows of key: its own entry, and the
// entries of its uploads' parts and meta objects.
func entriesOf(listed map[string]adminEntry, key string) []string {
	var names []string
	for name := range listed {
		if name == key || strings.HasPrefix(name, "_multipart_"+key+".") {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

// gcOIDs is the oids of every gc entry tagged tag, sorted.
func gcOIDs(entries []gcEntry, tag string) []string {
	var oids []string
	for _, e := range entries {
		if e.Tag != tag {
			continue
		}
		for _, o := range e.Objs {
			oids = append(oids, o.OID)
		}
	}
	slices.Sort(oids)
	return oids
}

// holdCompleteLock takes the RGWCompleteMultipart lock on the meta object
// oid for the specs' own client, as a completing gateway takes it, and
// returns its release.
func holdCompleteLock(ctx context.Context, oid string) func() {
	GinkgoHelper()
	p := nonECPool(ctx)
	w := radosclient.NewWriteOp()
	lock.LockExisting(w, completeLockName, "", "", time.Minute, store.Release())
	_, err := p.Write(ctx, oid, w, radosclient.OpFlagNone)
	Expect(err).NotTo(HaveOccurred(), "locking %s", oid)
	return func() {
		GinkgoHelper()
		w := radosclient.NewWriteOp()
		lock.Unlock(w, completeLockName, "", store.Release())
		_, err := p.Write(ctx, oid, w, radosclient.OpFlagNone)
		Expect(err).NotTo(HaveOccurred(), "unlocking %s", oid)
	}
}

// expectRadosgwReads requires radosgw to serve key as the object body with
// ETag etag.
func expectRadosgwReads(ctx context.Context, key, etag string, body []byte) {
	GinkgoHelper()
	h, err := s3Client(alice()).HeadObject(ctx, &awss3.HeadObjectInput{Bucket: aws.String(mpBucket), Key: aws.String(key)})
	Expect(err).NotTo(HaveOccurred(), "HeadObject %s through radosgw", key)
	Expect(aws.ToString(h.ETag)).To(Equal(`"`+etag+`"`), "radosgw's ETag of %s", key)
	Expect(aws.ToInt64(h.ContentLength)).To(BeEquivalentTo(len(body)), "radosgw's Content-Length of %s", key)
	_, got := getRadosgw(ctx, mpBucket, key)
	Expect(got).To(Equal(body), "%s read back through radosgw", key)
}

// expectRGWGoReads requires rgw-go to serve key as the object body with
// ETag etag.
func expectRGWGoReads(ctx context.Context, c *awss3.Client, key, etag string, body []byte) {
	GinkgoHelper()
	out, err := c.GetObject(ctx, &awss3.GetObjectInput{Bucket: aws.String(mpBucket), Key: aws.String(key)})
	Expect(err).NotTo(HaveOccurred(), "GetObject %s through rgw-go", key)
	got, err := io.ReadAll(out.Body)
	Expect(errors.Join(err, out.Body.Close())).NotTo(HaveOccurred(), "reading %s through rgw-go", key)
	Expect(aws.ToString(out.ETag)).To(Equal(`"`+etag+`"`), "rgw-go's ETag of %s", key)
	Expect(got).To(Equal(body), "%s read back through rgw-go", key)
}

// expectMultipartObject requires radosgw-admin object stat of key to show
// the manifest rgw-go decodes from its head, with prefix and rules, and
// the attrs a completion writes: the write tag as the tail tag, the ETag,
// and no storage class in the default placement's STANDARD.
func expectMultipartObject(ctx context.Context, rec *op.BucketRecord, key, etag, prefix string, rules map[uint64]mpRule) {
	GinkgoHelper()
	o := objectStat(ctx, mpBucket, key)
	Expect(o.ETag).To(Equal(etag), "object stat's etag of %s", key)
	m := o.manifest()
	Expect(m.Prefix).To(Equal(prefix), "object stat's prefix of %s", key)
	Expect(statRules(o)).To(Equal(rules), "object stat's rules of %s", key)
	expectSameJSON(key+"'s manifest", storedManifest(ctx, rec, key), canonical(o.Manifest))
	head := rawXattrs(ctx, keyHead(rec, key))
	Expect(head).To(HaveKey(meta.AttrIDTag), "%s's idtag", key)
	Expect(head[meta.AttrTailTag]).To(Equal(head[meta.AttrIDTag]), "%s's tail tag is its idtag", key)
	Expect(head).To(HaveKey(meta.AttrETag), "%s's etag attr", key)
	Expect(head).NotTo(HaveKey(meta.AttrStorageClass), "%s's storage class attr", key)
}

// expectListedAlone requires bucket list to show key's one entry, with its
// ETag and size, and nothing of its uploads.
func expectListedAlone(ctx context.Context, key, etag string, size int) {
	GinkgoHelper()
	listed := bucketList(ctx, mpBucket)
	Expect(entriesOf(listed, key)).To(Equal([]string{key}), "bucket list's entries of %s", key)
	Expect(listed[key].Meta.ETag).To(Equal(etag), "bucket list's etag of %s", key)
	Expect(listed[key].Meta.Size).To(BeEquivalentTo(size), "bucket list's size of %s", key)
}

// expectBucketCheckInvalid requires radosgw-admin bucket check to report
// exactly the index keys want as invalid multipart entries, and a stored
// header equal to the one it calculates from the index.
func expectBucketCheckInvalid(ctx context.Context, bucket string, want []string) {
	GinkgoHelper()
	var check struct {
		InvalidMultipart []string `json:"invalid_multipart_entries"`
		CheckResult      struct {
			Existing   json.RawMessage `json:"existing_header"`
			Calculated json.RawMessage `json:"calculated_header"`
		} `json:"check_result"`
	}
	out := admin(ctx, "bucket", "check", "--bucket", bucket)
	Expect(json.Unmarshal(out, &check)).To(Succeed(), "bucket check: %s", out)
	Expect(check.InvalidMultipart).To(ConsistOf(want), "bucket check's invalid multipart entries: %s", out)
	Expect(nonZeroUsage(check.CheckResult.Existing)).To(Equal(nonZeroUsage(check.CheckResult.Calculated)), "bucket check: %s", out)
}

// expectSameListing requires rgw-go and radosgw to answer a GET of path
// with the same document, byte for byte, and returns it; rgwgoURL is
// rgw-go's. Neither listing carries a request id.
func expectSameListing(ctx context.Context, rgwgoURL, what, path string) string {
	GinkgoHelper()
	status, mine := rawRequest(ctx, rgwgoURL, http.MethodGet, path)
	Expect(status).To(Equal(http.StatusOK), "rgw-go's %s: %s", what, mine)
	status, theirs := rawRequest(ctx, endpoint, http.MethodGet, path)
	Expect(status).To(Equal(http.StatusOK), "radosgw's %s: %s", what, theirs)
	Expect(string(mine)).To(Equal(string(theirs)), "%s through rgw-go and through radosgw", what)
	return string(mine)
}

// listParts is ListParts of upload id of key through c.
func listParts(ctx context.Context, c *awss3.Client, key, id string) *awss3.ListPartsOutput {
	GinkgoHelper()
	out, err := c.ListParts(ctx, &awss3.ListPartsInput{Bucket: aws.String(mpBucket), Key: aws.String(key), UploadId: aws.String(id)})
	Expect(err).NotTo(HaveOccurred(), "ListParts %s", key)
	return out
}

// listedUpload is ListMultipartUploads' entry of upload id through c.
func listedUpload(ctx context.Context, c *awss3.Client, id string) s3types.MultipartUpload {
	GinkgoHelper()
	out, err := c.ListMultipartUploads(ctx, &awss3.ListMultipartUploadsInput{Bucket: aws.String(mpBucket)})
	Expect(err).NotTo(HaveOccurred(), "ListMultipartUploads")
	for _, u := range out.Uploads {
		if aws.ToString(u.UploadId) == id {
			return u
		}
	}
	Fail("ListMultipartUploads does not list upload " + id)
	return s3types.MultipartUpload{}
}

// rawRequest sends method on path, which may carry a query, to the gateway
// at url as alice, signed over UNSIGNED-PAYLOAD, and returns the status and
// the body.
func rawRequest(ctx context.Context, url, method, path string) (int, []byte) {
	GinkgoHelper()
	req, err := http.NewRequestWithContext(ctx, method, url+path, nil)
	Expect(err).NotTo(HaveOccurred())
	req.Header.Set("X-Amz-Content-Sha256", "UNSIGNED-PAYLOAD")
	u := alice()
	Expect(v4.NewSigner().SignHTTP(ctx, aws.Credentials{AccessKeyID: u.AccessKey, SecretAccessKey: u.SecretKey},
		req, "UNSIGNED-PAYLOAD", "s3", "us-east-1", time.Now())).To(Succeed())
	resp, err := http.DefaultClient.Do(req)
	Expect(err).NotTo(HaveOccurred(), "%s %s", method, path)
	body, err := io.ReadAll(resp.Body)
	Expect(errors.Join(err, resp.Body.Close())).NotTo(HaveOccurred())
	return resp.StatusCode, body
}

// radosOps is the operations a connection has submitted, reads and writes.
func radosOps(s radosclient.Stats) uint64 { return s.ReadOps + s.WriteOps }

// settledOps waits, for up to a minute, for a second in which rgw-go's
// connection has no RADOS operation in flight and submits none, and returns
// its operation count. The count is the whole connection's, so the workers'
// own reads, the quota syncs among them, keep it moving now and then.
func settledOps(ctx context.Context, g rgwgoGateway) uint64 {
	GinkgoHelper()
	var settled uint64
	Eventually(func(gm Gomega) {
		s := g.stats.Stats()
		gm.Expect(s.InflightOps).To(BeZero(), "RADOS operations in flight")
		start := radosOps(s)
		gm.Consistently(func() radosclient.Stats { return g.stats.Stats() }).
			WithTimeout(time.Second).WithPolling(20 * time.Millisecond).
			Should(And(HaveField("InflightOps", BeZero()), WithTransform(radosOps, Equal(start))))
		settled = start
	}).WithContext(ctx).WithTimeout(time.Minute).WithPolling(100 * time.Millisecond).Should(Succeed())
	return settled
}

var _ = Describe("the multipart oracle against radosgw and radosgw-admin", Ordered, Label("integration"), func() {
	var (
		rec     *op.BucketRecord
		rgwgo   rgwgoGateway
		viaGo   *awss3.Client
		viaRGW  *awss3.Client
		release denc.Release
	)

	BeforeAll(func(ctx SpecContext) {
		removeLeftoverBucket(ctx, mpBucket)
		_, err := s3Client(alice()).CreateBucket(ctx, &awss3.CreateBucketInput{Bucket: aws.String(mpBucket)})
		Expect(err).NotTo(HaveOccurred(), "creating %s through radosgw", mpBucket)
		DeferCleanup(func(ctx context.Context) {
			admin(ctx, "bucket", "rm", "--bucket", mpBucket, "--purge-objects")
			gcProcess(ctx)
			Expect(markerOIDs(ctx, rec.Info.Bucket.Marker)).To(BeEmpty(), "%s's data after its removal", mpBucket)
			Expect(gcList(ctx)).To(BeEmpty(), "gc list --include-all after the oracle")
		})
		rec, err = store.GetBucket(ctx, "", mpBucket)
		Expect(err).NotTo(HaveOccurred())
		rgwgo = serveRGWGo(ctx)
		viaGo, viaRGW = mpClient(rgwgo.url), mpClient(endpoint)
		release = store.Release()
	})

	It("1: serves through radosgw an upload rgw-go writes", func(ctx SpecContext) {
		const key = "k1"
		id := createUpload(ctx, viaGo, key)
		bodies := [][]byte{randBytes(8 * mib), randBytes(8 * mib), randBytes(4 * mib)}
		parts := uploadParts(ctx, viaGo, key, id, bodies)
		etag := mustComplete(ctx, viaGo, key, id, parts, bodies...)

		expectRadosgwReads(ctx, key, etag, slices.Concat(bodies...))
		expectMultipartObject(ctx, rec, key, etag, key+"."+id, map[uint64]mpRule{
			0:        {StartPartNum: 1, StartOfs: 0, PartSize: 8 * mib, StripeMaxSize: 4 * mib},
			16 * mib: {StartPartNum: 3, StartOfs: 16 * mib, PartSize: 4 * mib, StripeMaxSize: 4 * mib},
		})
		expectListedAlone(ctx, key, etag, 20*mib)
		expectBucketCheckClean(ctx, mpBucket)
		Expect(metaObjects(ctx, rec)).NotTo(ContainElement(metaOID(rec, key, id)), "the meta object once completed")
		// Each 8 MiB part is a head and one shadow stripe, the 4 MiB one a
		// head alone.
		Expect(partObjects(ctx, rec, key+"."+id)).To(HaveLen(5), "the parts' objects")
		Expect(gcOIDs(gcList(ctx), id)).To(BeEmpty(), "a completion without history queues nothing")
	})

	It("2: completes through rgw-go an upload radosgw starts", func(ctx SpecContext) {
		const key = "k2"
		id := createUpload(ctx, viaRGW, key)
		bodies := [][]byte{randBytes(6 * mib), randBytes(6 * mib)}
		parts := uploadParts(ctx, viaRGW, key, id, bodies)

		lp := expectSameListing(ctx, rgwgo.url, "ListParts", "/"+mpBucket+"/"+key+"?uploadId="+id)
		Expect(lp).To(ContainSubstring("<StorageClass>STANDARD</StorageClass>"), "rgw-go's ListParts")
		if release >= denc.Tentacle {
			Expect(strings.Count(lp, "<LastModified>")).To(Equal(len(parts)), "ListParts' LastModified elements")
			Expect(regexp.MustCompile(`<LastModified>[0-9T:-]+\.000Z</LastModified>`).FindAllString(lp, -1)).To(HaveLen(len(parts)),
				"v20.2.4's ListParts prints every part's LastModified in whole seconds")
		}
		mine := listParts(ctx, viaGo, key, id)
		Expect(mine.Parts).To(HaveLen(2), "rgw-go's ListParts")
		for i, p := range mine.Parts {
			Expect(aws.ToString(p.ETag)).To(Equal(aws.ToString(parts[i].ETag)), "rgw-go lists part %d with radosgw's ETag", i+1)
			Expect(aws.ToInt64(p.Size)).To(BeEquivalentTo(6*mib), "rgw-go's size of part %d", i+1)
		}
		expectSameListing(ctx, rgwgo.url, "ListMultipartUploads", "/"+mpBucket+"?uploads")
		u := listedUpload(ctx, viaGo, id)
		Expect(aws.ToString(u.Key)).To(Equal(key), "rgw-go's ListMultipartUploads key")
		Expect(aws.ToString(u.Initiator.ID)).To(Equal(alice().ID()), "rgw-go's ListMultipartUploads initiator")
		Expect(aws.ToString(u.Owner.ID)).To(Equal(alice().ID()), "rgw-go's ListMultipartUploads owner")

		etag := mustComplete(ctx, viaGo, key, id, parts, bodies...)
		expectRadosgwReads(ctx, key, etag, slices.Concat(bodies...))
		expectMultipartObject(ctx, rec, key, etag, key+"."+id, map[uint64]mpRule{
			0: {StartPartNum: 1, StartOfs: 0, PartSize: 6 * mib, StripeMaxSize: 4 * mib},
		})
		expectListedAlone(ctx, key, etag, 12*mib)
		expectBucketCheckClean(ctx, mpBucket)
		Expect(metaObjects(ctx, rec)).NotTo(ContainElement(metaOID(rec, key, id)), "the meta object once completed")

		if release >= denc.Tentacle {
			// A checksum radosgw stored with the upload is decoded and
			// ignored: the parts are listed and completed without one, and
			// the object carries none (docs/exclusions.md, "Client checksums
			// are ignored until phase 2").
			const ckey = "k2-crc32"
			out, err := viaRGW.CreateMultipartUpload(ctx, &awss3.CreateMultipartUploadInput{
				Bucket: aws.String(mpBucket), Key: aws.String(ckey), ChecksumAlgorithm: s3types.ChecksumAlgorithmCrc32,
			})
			Expect(err).NotTo(HaveOccurred(), "CreateMultipartUpload with CRC32 through radosgw")
			cid := aws.ToString(out.UploadId)
			cbodies := [][]byte{randBytes(5 * mib), randBytes(1 * mib)}
			var cparts []s3types.CompletedPart
			for i, b := range cbodies {
				n := int32(i + 1) //nolint:gosec // two parts
				po, perr := viaRGW.UploadPart(ctx, &awss3.UploadPartInput{
					Bucket: aws.String(mpBucket), Key: aws.String(ckey), UploadId: aws.String(cid), PartNumber: aws.Int32(n),
					Body: bytes.NewReader(b), ContentLength: aws.Int64(int64(len(b))), ChecksumAlgorithm: s3types.ChecksumAlgorithmCrc32,
				})
				Expect(perr).NotTo(HaveOccurred(), "UploadPart with CRC32 through radosgw")
				cparts = append(cparts, s3types.CompletedPart{PartNumber: aws.Int32(n), ETag: po.ETag})
			}
			status, body := rawRequest(ctx, rgwgo.url, http.MethodGet, "/"+mpBucket+"/"+ckey+"?uploadId="+cid)
			Expect(status).To(Equal(http.StatusOK), "%s", body)
			Expect(string(body)).NotTo(ContainSubstring("<Checksum"), "rgw-go's ListParts of a CRC32 upload")
			Expect(metaInfo(ctx, metaOID(rec, ckey, cid)).CksumType).NotTo(BeZero(), "the checksum type radosgw stored with the upload")
			cout, err := completeUpload(ctx, viaGo, ckey, cid, cparts)
			Expect(err).NotTo(HaveOccurred(), "CompleteMultipartUpload of a CRC32 upload through rgw-go")
			cetag := mpETag(cbodies...)
			Expect(aws.ToString(cout.ETag)).To(Equal(`"`+cetag+`"`), "%s's completed ETag", ckey)
			Expect(cout).To(And(
				HaveField("ChecksumCRC32", BeNil()), HaveField("ChecksumCRC32C", BeNil()), HaveField("ChecksumCRC64NVME", BeNil()),
				HaveField("ChecksumSHA1", BeNil()), HaveField("ChecksumSHA256", BeNil()), HaveField("ChecksumType", BeEmpty()),
			), "rgw-go's completion of a CRC32 upload carries no checksum")
			expectRadosgwReads(ctx, ckey, cetag, slices.Concat(cbodies...))
			Expect(rawXattrs(ctx, keyHead(rec, ckey))).NotTo(HaveKey("user.rgw.cksum"), "the completed object's checksum")
			expectBucketCheckClean(ctx, mpBucket)
		}
	})

	It("3: completes through radosgw an upload rgw-go starts", func(ctx SpecContext) {
		const key = "k3"
		id := createUpload(ctx, viaGo, key)
		bodies := [][]byte{randBytes(6 * mib), randBytes(6 * mib)}
		parts := uploadParts(ctx, viaGo, key, id, bodies)

		moid := metaOID(rec, key, id)
		Expect(metaObjects(ctx, rec)).To(ContainElement(moid), "the meta object in the data-extra pool")
		Expect(omapKeys(ctx, moid)).To(Equal([]string{"part.00000001", "part.00000002"}), "the meta object's omap keys")
		Expect(metaInfo(ctx, moid).CksumType).To(BeZero(), "rgw-go stores no checksum type with the upload")
		prefix := "_multipart_" + key + "." + id
		Expect(entriesOf(bucketList(ctx, mpBucket), key)).To(Equal([]string{prefix + ".1", prefix + ".2", prefix + ".meta"}),
			"bucket list's entries of the live upload")
		expectBucketCheckClean(ctx, mpBucket)
		expectSameListing(ctx, rgwgo.url, "ListParts", "/"+mpBucket+"/"+key+"?uploadId="+id)
		expectSameListing(ctx, rgwgo.url, "ListMultipartUploads", "/"+mpBucket+"?uploads")
		theirs := listParts(ctx, viaRGW, key, id)
		Expect(theirs.Parts).To(HaveLen(2), "radosgw's ListParts")
		for i, p := range theirs.Parts {
			Expect(aws.ToString(p.ETag)).To(Equal(aws.ToString(parts[i].ETag)), "radosgw lists part %d with rgw-go's ETag", i+1)
		}
		u := listedUpload(ctx, viaRGW, id)
		Expect(aws.ToString(u.Initiator.ID)).To(Equal(alice().ID()), "radosgw's ListMultipartUploads initiator")
		Expect(aws.ToString(u.Owner.ID)).To(Equal(alice().ID()), "radosgw's ListMultipartUploads owner")

		etag := mustComplete(ctx, viaRGW, key, id, parts, bodies...)
		expectRGWGoReads(ctx, viaGo, key, etag, slices.Concat(bodies...))
		expectMultipartObject(ctx, rec, key, etag, key+"."+id, map[uint64]mpRule{
			0: {StartPartNum: 1, StartOfs: 0, PartSize: 6 * mib, StripeMaxSize: 4 * mib},
		})
		expectListedAlone(ctx, key, etag, 12*mib)
		expectBucketCheckClean(ctx, mpBucket)
		Expect(metaObjects(ctx, rec)).NotTo(ContainElement(moid), "the meta object once completed")

		// k1, which rgw-go started the same way, rgw-go completed: the two
		// heads differ only in what each write draws or computes.
		blank := append(slices.Clone(perRequest), meta.AttrETag)
		Expect(attrsView(rawXattrs(ctx, keyHead(rec, "k1")), blank, nil)).To(Equal(attrsView(rawXattrs(ctx, keyHead(rec, key)), blank, nil)),
			"the head attrs of rgw-go's completion against radosgw's")
	})

	It("4: queues a re-uploaded part's first upload under the upload id, as radosgw does", func(ctx SpecContext) {
		// Each gateway writes and completes an upload whose part 1 is sent
		// twice, so that radosgw's twin shows what rgw-go's must match.
		type reupload struct {
			key, id        string
			c              *awss3.Client
			firstObjs      []string
			parts          []s3types.CompletedPart
			second, last   []byte
			reuploadedHead string
		}
		ups := []*reupload{{key: "k4", c: viaGo}, {key: "k4-radosgw", c: viaRGW}}
		for _, u := range ups {
			u.id = createUpload(ctx, u.c, u.key)
			u.second, u.last = randBytes(5*mib), randBytes(2*mib)
			uploadPart(ctx, u.c, u.key, u.id, 1, randBytes(6*mib))
			u.firstObjs = partObjects(ctx, rec, u.key+"."+u.id)
			Expect(u.firstObjs).To(HaveLen(2), "the first part 1's head and stripe of %s", u.key)
			u.parts = []s3types.CompletedPart{uploadPart(ctx, u.c, u.key, u.id, 1, u.second), uploadPart(ctx, u.c, u.key, u.id, 2, u.last)}
		}
		listed := bucketList(ctx, mpBucket)
		var reuploaded []string
		for _, u := range ups {
			own := "_multipart_" + u.key + "." + u.id
			live := entriesOf(listed, u.key)
			Expect(live).To(HaveLen(4), "%s: both part 1 heads, part 2's and the meta object: %v", u.key, live)
			Expect(live).To(ContainElements(own+".1", own+".2", own+".meta"), u.key)
			for _, name := range live {
				if !strings.HasPrefix(name, own+".") {
					u.reuploadedHead = name
				}
			}
			Expect(u.reuploadedHead).To(MatchRegexp(`^_multipart_`+u.key+`\.[A-Za-z0-9_-]{32}\.1$`), "%s's re-uploaded part head", u.key)
			reuploaded = append(reuploaded, u.reuploadedHead)
		}
		// bucket check takes a re-uploaded part head, named by its random
		// prefix, for a part without its upload's .meta entry, whichever
		// gateway wrote it (docs/ceph-upstream-bugs.md, "radosgw-admin bucket
		// check reports a live re-uploaded part as an invalid multipart
		// entry").
		expectBucketCheckInvalid(ctx, mpBucket, reuploaded)

		for _, u := range ups {
			etag := mustComplete(ctx, u.c, u.key, u.id, u.parts, u.second, u.last)
			expectRadosgwReads(ctx, u.key, etag, slices.Concat(u.second, u.last))
			// The first part's manifest is adopted whole, so the object's
			// prefix is the re-uploaded part's random one, and part 2's rule
			// names the upload's own.
			m := storedManifest(ctx, rec, u.key)
			Expect(m.Prefix).To(Equal(strings.TrimSuffix(strings.TrimPrefix(u.reuploadedHead, "_multipart_"), ".1")),
				"%s's prefix is its re-uploaded part's", u.key)
			expectMultipartObject(ctx, rec, u.key, etag, m.Prefix, map[uint64]mpRule{
				0:       {StartPartNum: 1, StartOfs: 0, PartSize: 5 * mib, StripeMaxSize: 4 * mib},
				5 * mib: {StartPartNum: 2, StartOfs: 5 * mib, PartSize: 2 * mib, StripeMaxSize: 4 * mib, OverridePrefix: u.key + "." + u.id},
			})
			expectListedAlone(ctx, u.key, etag, 7*mib)
			Expect(gcOIDs(gcList(ctx), u.id)).To(Equal(u.firstObjs), "the gc chain of %s under its upload id", u.key)
			for _, oid := range u.firstObjs {
				_, err := rawStat(ctx, oid)
				Expect(err).NotTo(HaveOccurred(), "%s waits for the collector", oid)
			}
		}
		expectBucketCheckClean(ctx, mpBucket)

		gcProcess(ctx)
		for _, u := range ups {
			for _, oid := range u.firstObjs {
				_, err := rawStat(ctx, oid)
				Expect(isNotFound(err)).To(BeTrue(), "%s once collected: %v", oid, err)
			}
			Expect(gcOIDs(gcList(ctx), u.id)).To(BeEmpty(), "the gc chain of %s once processed", u.key)
			expectRadosgwReads(ctx, u.key, mpETag(u.second, u.last), slices.Concat(u.second, u.last))
		}
		expectBucketCheckClean(ctx, mpBucket)
	})

	It("5: queues an aborted upload's parts under the upload id", func(ctx SpecContext) {
		const key = "k5"
		id := createUpload(ctx, viaGo, key)
		uploadParts(ctx, viaGo, key, id, [][]byte{randBytes(5 * mib), randBytes(5 * mib), randBytes(1 * mib)})
		objs := partObjects(ctx, rec, key+"."+id)
		Expect(objs).To(HaveLen(5), "three part heads and two stripes")
		moid := metaOID(rec, key, id)
		Expect(metaObjects(ctx, rec)).To(ContainElement(moid))

		_, err := viaGo.AbortMultipartUpload(ctx, &awss3.AbortMultipartUploadInput{Bucket: aws.String(mpBucket), Key: aws.String(key), UploadId: aws.String(id)})
		Expect(err).NotTo(HaveOccurred(), "AbortMultipartUpload through rgw-go")
		Expect(gcOIDs(gcList(ctx), id)).To(Equal(objs), "the gc chain under the upload id")
		Expect(entriesOf(bucketList(ctx, mpBucket), key)).To(BeEmpty(), "bucket list's entries of the aborted upload")
		expectBucketCheckClean(ctx, mpBucket)
		Expect(metaObjects(ctx, rec)).NotTo(ContainElement(moid), "the meta object once aborted")
		_, err = viaRGW.ListParts(ctx, &awss3.ListPartsInput{Bucket: aws.String(mpBucket), Key: aws.String(key), UploadId: aws.String(id)})
		Expect(statusOf(err)).To(Equal(http.StatusNotFound), "radosgw's ListParts of the aborted upload: %v", err)

		gcProcess(ctx)
		Expect(partObjects(ctx, rec, key+"."+id)).To(BeEmpty(), "the parts once collected")
	})

	It("6: excludes either gateway's completion while the other holds the lock", func(ctx SpecContext) {
		bodies := [][]byte{randBytes(5 * mib), randBytes(1 * mib)}
		for _, c := range []struct {
			key           string
			starts, comps *awss3.Client
			who           string
		}{
			{"k6", viaGo, viaRGW, "radosgw"},
			{"k7", viaRGW, viaGo, "rgw-go"},
		} {
			id := createUpload(ctx, c.starts, c.key)
			parts := uploadParts(ctx, c.starts, c.key, id, bodies)
			unlock := holdCompleteLock(ctx, metaOID(rec, c.key, id))
			_, err := completeUpload(ctx, c.comps, c.key, id, parts)
			expectCompletionInProgress(err, c.who)
			expectBucketCheckClean(ctx, mpBucket)
			Expect(entriesOf(bucketList(ctx, mpBucket), c.key)).To(ContainElement(HaveSuffix(".meta")), "%s's upload survives the refusal", c.key)
			unlock()
			etag := mustComplete(ctx, c.comps, c.key, id, parts, bodies...)
			expectRadosgwReads(ctx, c.key, etag, slices.Concat(bodies...))
			expectRGWGoReads(ctx, viaGo, c.key, etag, slices.Concat(bodies...))
			expectListedAlone(ctx, c.key, etag, 6*mib)
			expectBucketCheckClean(ctx, mpBucket)
		}
	})

	It("7: costs rgw-go seven RADOS operations for a 5 MiB part", func(ctx SpecContext) {
		const key = "k8"
		id := createUpload(ctx, viaGo, key)
		DeferCleanup(func(ctx context.Context) {
			_, err := viaGo.AbortMultipartUpload(ctx, &awss3.AbortMultipartUploadInput{Bucket: aws.String(mpBucket), Key: aws.String(key), UploadId: aws.String(id)})
			Expect(err).NotTo(HaveOccurred(), "aborting %s", key)
		})
		body := randBytes(5 * mib)
		// The first part puts the bucket, the user and the access key in
		// rgw-go's caches, so the parts measured cost their own operations.
		uploadPart(ctx, viaGo, key, id, 1, body)
		// A worker's operation between the two settled counts can only add
		// to a measurement, so the part's own cost is the least of five, and
		// at least two must find it.
		var counts []uint64
		for n := int32(2); n <= 6; n++ {
			before := settledOps(ctx, rgwgo)
			uploadPart(ctx, viaGo, key, id, n, body)
			counts = append(counts, settledOps(ctx, rgwgo)-before)
		}
		AddReportEntry("RADOS operations of a 5 MiB UploadPart through rgw-go", counts)
		least, found := slices.Min(counts), 0
		for _, c := range counts {
			if c == least {
				found++
			}
		}
		Expect(found).To(BeNumerically(">=", 2), "two of the five measurements find the least: %v", counts)
		// One meta object read, the part head's exclusive create with its
		// first stripe, the shadow stripe, the index prepare, the part head's
		// attrs, the index completion and the part's registration.
		Expect(least).To(BeEquivalentTo(7), "%v", counts)
	})
})
