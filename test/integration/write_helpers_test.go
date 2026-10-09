//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // radosgw's ETag is an MD5, not a security control
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/cls/gc"
	"github.com/jhoblitt/rgw-go/internal/cls/lock"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/s3"
	"github.com/jhoblitt/rgw-go/test/gate"
)

// headerRecorder is an S3 client's HTTP client that keeps the headers of the
// last request it sent, so that rgw-go can be handed the request radosgw got.
type headerRecorder struct {
	mu   sync.Mutex
	last http.Header
}

func (h *headerRecorder) Do(req *http.Request) (*http.Response, error) {
	h.mu.Lock()
	h.last = req.Header.Clone()
	h.mu.Unlock()
	return http.DefaultClient.Do(req)
}

// header is the last request's headers.
func (h *headerRecorder) header() http.Header {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.last.Clone()
}

// recordingClient is s3Client(u) whose requests' headers rec keeps.
func recordingClient(u gate.User, rec *headerRecorder) *awss3.Client {
	return awss3.New(awss3.Options{
		Region:           "us-east-1",
		BaseEndpoint:     aws.String(endpoint),
		UsePathStyle:     true,
		Credentials:      credentials.NewStaticCredentialsProvider(u.AccessKey, u.SecretKey, ""),
		RetryMaxAttempts: 1,
		HTTPClient:       rec,
	})
}

// randBytes is n random bytes.
func randBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

// md5Hex is the ETag radosgw gives an object written in one request.
func md5Hex(b []byte) string {
	sum := md5.Sum(b) //nolint:gosec // radosgw's ETag
	return hex.EncodeToString(sum[:])
}

// aliceOwner is alice as a bucket's or an object's owner.
func aliceOwner() meta.Owner { return meta.UserOwner(userID(alice())) }

// aliceACL is the encoded default policy radosgw gives alice's writes.
func aliceACL() []byte { return encodeAt(acl.DefaultPolicy(aliceOwner(), alice().UID)) }

// writeBucket creates a bucket of alice's through rgw-go, as M's
// CreateBucket writes one, and when the spec ends removes it with its
// objects through radosgw-admin and frees what that left to the garbage
// collector, so that the gate's pool counts see none of it.
func writeBucket(ctx context.Context) *op.BucketRecord {
	GinkgoHelper()
	name := randName("w")
	zg := store.ZoneGroup()
	rec, err := store.CreateBucket(ctx, op.CreateBucketParams{
		Name: name, Owner: aliceOwner(), Zonegroup: zg.ID, Placement: zg.DefaultPlacement,
		Attrs: map[string][]byte{meta.AttrACL: aliceACL()}, Exclusive: true,
	})
	Expect(err).NotTo(HaveOccurred(), "creating %s", name)
	DeferCleanup(func(ctx context.Context) {
		admin(ctx, "bucket", "rm", "--bucket", name, "--purge-objects")
		gcProcess(ctx)
		Expect(markerOIDs(ctx, rec.Info.Bucket.Marker)).To(BeEmpty(), "%s's data after its removal", name)
	})
	return rec
}

// freshBucket is the bucket record as RADOS holds it now, past the driver's
// cache, which a reshard changes behind it.
func freshBucket(ctx context.Context, rec *op.BucketRecord) *op.BucketRecord {
	GinkgoHelper()
	cur, err := store.GetBucketInstance(ctx, rec.Info.Bucket)
	Expect(err).NotTo(HaveOccurred())
	return cur
}

// putRequest is a synthetic PutObject request carrying h, as the S3 layer
// hands one to the op.
func putRequest(h http.Header) *op.Request {
	return &op.Request{Method: http.MethodPut, Header: h, Env: store.Env()}
}

// rgwgoAttrs is what rgw-go's PutObject op stores for a request with
// headers h: the attrs the S3 layer reads from them and alice's default ACL.
func rgwgoAttrs(h http.Header) map[string][]byte {
	GinkgoHelper()
	attrs, err := s3.RequestAttrsForIntegration(putRequest(h), true)
	Expect(err).NotTo(HaveOccurred())
	attrs[meta.AttrACL] = aliceACL()
	return attrs
}

// putRGWGo writes body under key through rgw-go's driver with attrs.
func putRGWGo(ctx context.Context, rec *op.BucketRecord, key string, body []byte, p op.PutParams) *op.PutResult {
	GinkgoHelper()
	p.Size = int64(len(body))
	res, err := store.PutObject(ctx, rec, meta.ObjKey{Name: key}, bytes.NewReader(body), p)
	Expect(err).NotTo(HaveOccurred(), "PutObject %s", key)
	return res
}

// stat is rgw-go's StatObject of key, which must exist.
func stat(ctx context.Context, rec *op.BucketRecord, key string) *op.ObjectState {
	GinkgoHelper()
	st, err := store.StatObject(ctx, rec, meta.ObjKey{Name: key})
	Expect(err).NotTo(HaveOccurred(), "StatObject %s", key)
	Expect(st.Exists).To(BeTrue(), "%s exists", key)
	return st
}

// prefetch is rgw-go's PrefetchObject of key, the state a copy reads its
// source with.
func prefetch(ctx context.Context, rec *op.BucketRecord, key string) *op.ObjectState {
	GinkgoHelper()
	st, err := store.PrefetchObject(ctx, rec, meta.ObjKey{Name: key})
	Expect(err).NotTo(HaveOccurred(), "PrefetchObject %s", key)
	Expect(st.Exists).To(BeTrue(), "%s exists", key)
	return st
}

// adminObject is what radosgw-admin object stat prints of an object: the
// attrs it decodes under their own names, and the rest under attrs with any
// NUL terminator hidden.
type adminObject struct {
	Name        string            `json:"name"`
	Size        uint64            `json:"size"`
	ETag        string            `json:"etag"`
	Tag         string            `json:"tag"`
	Policy      json.RawMessage   `json:"policy"`
	Manifest    json.RawMessage   `json:"manifest"`
	Compression json.RawMessage   `json:"compression"`
	PGVer       *uint64           `json:"pg_ver"`
	SourceZone  *uint32           `json:"source_zone"`
	Attrs       map[string]string `json:"attrs"`
}

// adminManifest is the part of object stat's manifest the specs compare.
type adminManifest struct {
	ObjSize       uint64 `json:"obj_size"`
	HeadSize      uint64 `json:"head_size"`
	MaxHeadSize   uint64 `json:"max_head_size"`
	Prefix        string `json:"prefix"`
	Rules         []any  `json:"rules"`
	TailPlacement struct {
		Bucket struct {
			Name     string `json:"name"`
			Marker   string `json:"marker"`
			BucketID string `json:"bucket_id"`
		} `json:"bucket"`
		PlacementRule string `json:"placement_rule"`
	} `json:"tail_placement"`
}

// manifest decodes o's manifest.
func (o adminObject) manifest() adminManifest {
	GinkgoHelper()
	var m adminManifest
	Expect(json.Unmarshal(o.Manifest, &m)).To(Succeed(), "object stat's manifest: %s", o.Manifest)
	return m
}

// keys is the set of attr names o shows: those object stat decodes under
// their own names, mapped back to the attr, and those under attrs.
func (o adminObject) keys() []string {
	keys := slices.Collect(maps.Keys(o.Attrs))
	for name, present := range map[string]bool{
		meta.AttrACL: len(o.Policy) > 0, meta.AttrManifest: len(o.Manifest) > 0, meta.AttrCompression: len(o.Compression) > 0,
		meta.AttrIDTag: o.Tag != "", meta.AttrETag: o.ETag != "", meta.AttrPGVer: o.PGVer != nil, meta.AttrSourceZone: o.SourceZone != nil,
	} {
		if present {
			keys = append(keys, name)
		}
	}
	slices.Sort(keys)
	return keys
}

// objectStat is radosgw-admin object stat of key in bucket.
func objectStat(ctx context.Context, bucket, key string) adminObject {
	GinkgoHelper()
	var o adminObject
	adminJSON(ctx, &o, "object", "stat", "--bucket", bucket, "--object", key)
	return o
}

// prefixShape is a write's tail prefix: "." and 31 characters of
// gen_rand_alphanumeric's alphabet, then "_" (RGWObjManifest::generator::
// create_begin, driver/rados/rgw_obj_manifest.cc:239-246 at v19.2.6).
const prefixShape = `^\.[A-Za-z0-9_-]{31}_$`

// expectBucketCheckClean requires radosgw-admin bucket check to find no
// invalid multipart entry and a stored header equal to the one it
// calculates from the index.
func expectBucketCheckClean(ctx context.Context, bucket string) {
	GinkgoHelper()
	var check struct {
		InvalidMultipart []any `json:"invalid_multipart_entries"`
		CheckResult      struct {
			Existing   json.RawMessage `json:"existing_header"`
			Calculated json.RawMessage `json:"calculated_header"`
		} `json:"check_result"`
	}
	out := admin(ctx, "bucket", "check", "--bucket", bucket)
	Expect(json.Unmarshal(out, &check)).To(Succeed(), "bucket check: %s", out)
	Expect(check.InvalidMultipart).To(BeEmpty(), "bucket check's invalid multipart entries: %s", out)
	Expect(check.CheckResult.Existing).NotTo(BeEmpty(), "bucket check's existing header: %s", out)
	Expect(nonZeroUsage(check.CheckResult.Existing)).To(Equal(nonZeroUsage(check.CheckResult.Calculated)), "bucket check: %s", out)
}

// nonZeroUsage is a bucket check header's usage without the categories
// whose every counter is zero. The class keeps a category in the stored
// header once its last entry is removed, at zero, while the header bucket
// check calculates from the index lists only the categories its entries
// name, so a radosgw that writes and deletes an object leaves the two
// differing by that zero category alone.
func nonZeroUsage(header json.RawMessage) map[string]map[string]json.Number {
	GinkgoHelper()
	var h struct {
		Usage map[string]map[string]json.Number `json:"usage"`
	}
	d := json.NewDecoder(bytes.NewReader(header))
	d.UseNumber()
	Expect(d.Decode(&h)).To(Succeed(), "bucket check header %s", header)
	out := map[string]map[string]json.Number{}
	for cat, counters := range h.Usage {
		for _, n := range counters {
			if n != "0" {
				out[cat] = counters
				break
			}
		}
	}
	return out
}

// adminUsage is radosgw-admin bucket stats' rgw.main usage of bucket.
type adminUsage struct {
	Size       uint64 `json:"size"`
	SizeActual uint64 `json:"size_actual"`
	NumObjects uint64 `json:"num_objects"`
}

func bucketUsage(ctx context.Context, bucket string) adminUsage {
	GinkgoHelper()
	var stats struct {
		Usage map[string]adminUsage `json:"usage"`
	}
	adminJSON(ctx, &stats, "bucket", "stats", "--bucket", bucket)
	return stats.Usage["rgw.main"]
}

// adminEntry is one entry radosgw-admin bucket list prints.
type adminEntry struct {
	Name string `json:"name"`
	Meta struct {
		Size          uint64 `json:"size"`
		ETag          string `json:"etag"`
		StorageClass  string `json:"storage_class"`
		Owner         string `json:"owner"`
		ContentType   string `json:"content_type"`
		AccountedSize uint64 `json:"accounted_size"`
	} `json:"meta"`
	Tag string `json:"tag"`
}

// bucketList is radosgw-admin bucket list of bucket, by name.
func bucketList(ctx context.Context, bucket string) map[string]adminEntry {
	GinkgoHelper()
	var entries []adminEntry
	adminJSON(ctx, &entries, "bucket", "list", "--bucket", bucket, "--max-entries", "10000")
	byName := map[string]adminEntry{}
	for _, e := range entries {
		Expect(byName).NotTo(HaveKey(e.Name), "bucket list names %s twice", e.Name)
		byName[e.Name] = e
	}
	return byName
}

// gcEntry is one entry radosgw-admin gc list prints (rgw_admin.cc:8797-8811
// at v19.2.6).
type gcEntry struct {
	Tag  string `json:"tag"`
	Objs []struct {
		Pool     string `json:"pool"`
		OID      string `json:"oid"`
		Key      string `json:"key"`
		Instance string `json:"instance"`
	} `json:"objs"`
}

// gcList is radosgw-admin gc list --include-all.
func gcList(ctx context.Context) []gcEntry {
	GinkgoHelper()
	var entries []gcEntry
	adminJSON(ctx, &entries, "gc", "list", "--include-all")
	return entries
}

// gcProcess is radosgw-admin gc process --include-all once no gc shard is
// held by another processor, which it would skip.
func gcProcess(ctx context.Context) {
	GinkgoHelper()
	expectGCShardsFree(ctx)
	admin(ctx, "gc", "process", "--include-all")
}

// expectGCShardsFree waits a little for every gc shard's gc_process lock to
// be free, and fails naming the holders that keep one. A processor holds a
// shard for its pass alone, but one killed during its pass, as
// internal/cli's serve specs kill rgw-go, holds it for
// rgw_gc_processor_max_time, an hour by default, and every collector skips
// the shard until then. It closes the pool it opens, so a cleanup can call
// it.
func expectGCShardsFree(ctx context.Context) {
	GinkgoHelper()
	shards, err := configUint(cluster, "rgw_gc_max_objs")
	Expect(err).NotTo(HaveOccurred())
	p := store.ZoneParams().GCPool
	h, err := cluster.Pool(ctx, p.Name, p.NS)
	Expect(err).NotTo(HaveOccurred())
	defer func() { Expect(h.Close()).To(Succeed()) }()
	Eventually(func(g Gomega) []string {
		var held []string
		for i := range int(shards) {
			rop := radosclient.NewReadOp()
			res := lock.GetInfo(rop, "gc_process", store.Release())
			_, err := h.Read(ctx, gc.ShardOID(i), rop, radosclient.OpFlagNone)
			g.Expect(err).NotTo(HaveOccurred(), "reading %s's lock", gc.ShardOID(i))
			info, err := res.Info()
			g.Expect(err).NotTo(HaveOccurred(), "decoding %s's lock", gc.ShardOID(i))
			for id, li := range info.Lockers {
				held = append(held, fmt.Sprintf("%s by %s until %s", gc.ShardOID(i), id.Locker, li.Expiration.UTC().Format(time.RFC3339)))
			}
		}
		return held
	}).WithContext(ctx).WithTimeout(10*time.Second).WithPolling(time.Second).Should(BeEmpty(),
		"gc shards another processor holds, which gc process skips")
}

// gcChain is the oids of the gc entry tagged tag, nil when there is none.
func gcChain(entries []gcEntry, tag string) []string {
	for _, e := range entries {
		if e.Tag == tag {
			oids := []string{}
			for _, o := range e.Objs {
				oids = append(oids, o.OID)
			}
			slices.Sort(oids)
			return oids
		}
	}
	return nil
}

// tails is the tail stripes' oids of st, in the data pool.
func tails(st *op.ObjectState) []string {
	GinkgoHelper()
	if st.Manifest == nil {
		return nil
	}
	stripes, err := st.Manifest.Stripes()
	Expect(err).NotTo(HaveOccurred())
	head := headOID(st)
	var oids []string
	for _, s := range stripes {
		if oid := s.OID(); oid != head {
			oids = append(oids, oid)
		}
	}
	slices.Sort(oids)
	return oids
}

// headOID is the head object's oid in the data pool.
func headOID(st *op.ObjectState) string {
	return meta.Stripe{Obj: meta.Obj{Bucket: st.Bucket.Info.Bucket, Key: st.Key}}.OID()
}

// dataPool opens the zone's data pool, every placement's and every storage
// class's on these clusters.
func dataPool(ctx context.Context) radosclient.Pool {
	GinkgoHelper()
	return pool(ctx, meta.Pool{Name: manifest.Pools.Data})
}

// rawXattrs reads oid's xattrs in the data pool as RADOS holds them.
func rawXattrs(ctx context.Context, oid string) map[string][]byte {
	GinkgoHelper()
	rop := radosclient.NewReadOp()
	x := rop.GetXattrs()
	_, err := dataPool(ctx).Read(ctx, oid, rop, radosclient.OpFlagNone)
	Expect(err).NotTo(HaveOccurred(), "reading %s's xattrs", oid)
	Expect(x.Err).NotTo(HaveOccurred())
	return x.Xattrs
}

// rawStat stats oid in the data pool; a missing object is ErrNotFound.
func rawStat(ctx context.Context, oid string) (*radosclient.StatResult, error) {
	rop := radosclient.NewReadOp()
	st := rop.Stat()
	_, err := dataPool(ctx).Read(ctx, oid, rop, radosclient.OpFlagNone)
	if err != nil {
		return nil, err
	}
	return st, st.Err
}

// dataOIDs is the data pool's objects of bucket rec.
func dataOIDs(ctx context.Context, rec *op.BucketRecord) []string {
	GinkgoHelper()
	return markerOIDs(ctx, rec.Info.Bucket.Marker)
}

// markerOIDs is the data pool's objects whose names carry a bucket's
// marker, sorted. It closes the pool it opens before it returns, so that a
// cleanup can call it.
func markerOIDs(ctx context.Context, marker string) []string {
	GinkgoHelper()
	h, err := cluster.Pool(ctx, manifest.Pools.Data, "")
	Expect(err).NotTo(HaveOccurred())
	defer func() { Expect(h.Close()).To(Succeed()) }()
	oids := []string{}
	Expect(h.ListObjects(ctx, func(oid, _ string) error {
		if strings.HasPrefix(oid, marker+"_") {
			oids = append(oids, oid)
		}
		return nil
	})).To(Succeed())
	slices.Sort(oids)
	return oids
}

// getRadosgw reads key back through the coexisting radosgw.
func getRadosgw(ctx context.Context, bucket, key string) (*awss3.GetObjectOutput, []byte) {
	GinkgoHelper()
	out, err := s3Client(alice()).GetObject(ctx, &awss3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	Expect(err).NotTo(HaveOccurred(), "GetObject %s/%s through radosgw", bucket, key)
	defer func() { Expect(out.Body.Close()).To(Succeed()) }()
	b, err := io.ReadAll(out.Body)
	Expect(err).NotTo(HaveOccurred(), "reading %s/%s through radosgw", bucket, key)
	return out, b
}

// logBuffer is a slog handler's output that specs read while the driver
// still writes to it.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *logBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// captureLogs makes the default logger write every record, debug included,
// to the buffer it returns until the spec ends.
func captureLogs() *logBuffer {
	l := &logBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(l, &slog.HandlerOptions{Level: slog.LevelDebug})))
	DeferCleanup(func() { slog.SetDefault(prev) })
	return l
}

// isNotFound reports a RADOS ENOENT.
func isNotFound(err error) bool { return errors.Is(err, radosclient.ErrNotFound) }
