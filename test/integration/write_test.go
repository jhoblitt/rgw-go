//go:build integration

package integration_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/cls/gc"
	"github.com/jhoblitt/rgw-go/internal/cls/refcount"
	rgwcls "github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/cls/version"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/s3"
)

const (
	kib = 1 << 10
	mib = 1 << 20
)

// perRequest are the attrs whose values differ between two writes of the
// same request: the write tags, the placement group version the head write
// returned and the manifest, whose prefix is random.
var perRequest = []string{meta.AttrIDTag, meta.AttrTailTag, meta.AttrPGVer, meta.AttrManifest}

// squidStoredHeaders are the request headers a v19.2.6 radosgw stores as
// attrs and rgw-go never does, since it applies v20.2.4's blocklist on both
// releases (docs/exclusions.md, "Request headers stored as attrs follow
// Tentacle's blocklist").
var squidStoredHeaders = []string{"user.rgw.x-amz-content-sha256", "user.rgw.x-amz-date"}

// attrsView is attrs with the values of the names in blank emptied, and
// the names in drop removed.
func attrsView(attrs map[string][]byte, blank, drop []string) map[string]string {
	out := map[string]string{}
	for k, v := range attrs {
		switch {
		case slices.Contains(drop, k):
		case slices.Contains(blank, k):
			out[k] = ""
		default:
			out[k] = string(v)
		}
	}
	return out
}

// statusOf is the HTTP status of an S3 client's error.
func statusOf(err error) int {
	if re, ok := errors.AsType[*awshttp.ResponseError](err); ok {
		return re.HTTPStatusCode()
	}
	return 0
}

// roundedSize is a size as rgw_rounded_objsize rounds it for the bucket
// stats: up to 4 KiB.
func roundedSize(n uint64) uint64 { return (n + 4095) &^ 4095 }

var _ = Describe("the write path against radosgw-admin and the coexisting radosgw", Label("integration"), func() {
	var (
		rec  *op.BucketRecord
		name string
	)

	BeforeEach(func(ctx SpecContext) {
		rec = writeBucket(ctx)
		name = rec.Info.Bucket.Name
	})

	Describe("PUT", func() {
		DescribeTable("writes the object a radosgw PUT of the same request writes",
			func(ctx SpecContext, size int) {
				key, twin := "obj", "obj-radosgw"
				body := randBytes(size)
				rh := &headerRecorder{}
				_, err := recordingClient(alice(), rh).PutObject(ctx, &awss3.PutObjectInput{
					Bucket: aws.String(name), Key: aws.String(twin), Body: bytes.NewReader(body),
					ContentType: aws.String("text/plain"), Metadata: map[string]string{"k": "v"},
				})
				Expect(err).NotTo(HaveOccurred(), "PutObject through radosgw")
				res := putRGWGo(ctx, rec, key, body, op.PutParams{Attrs: rgwgoAttrs(rh.header())})
				Expect(res.ETag).To(Equal(md5Hex(body)))

				st, tst := stat(ctx, rec, key), stat(ctx, rec, twin)
				o, t := objectStat(ctx, name, key), objectStat(ctx, name, twin)
				Expect(o.Size).To(BeEquivalentTo(size), "object stat size")
				Expect(o.ETag).To(Equal(md5Hex(body)), "object stat etag")
				Expect(o.ETag).To(Equal(t.ETag), "the twin's etag")
				Expect(st.ETag).To(Equal(o.ETag), "StatObject's etag")
				Expect(st.Size).To(Equal(o.Size), "StatObject's size")
				Expect(st.Manifest).NotTo(BeNil(), "rgw-go wrote a manifest")
				expectSameJSON("StatObject's manifest", *st.Manifest, canonical(o.Manifest))
				om, tm := o.manifest(), t.manifest()
				Expect(om.ObjSize).To(BeEquivalentTo(size), "manifest obj_size")
				Expect(om.HeadSize).To(Equal(tm.HeadSize), "head_size")
				Expect(om.HeadSize).To(BeEquivalentTo(min(size, 4*mib)), "head_size")
				Expect(om.MaxHeadSize).To(Equal(tm.MaxHeadSize), "max_head_size")
				Expect(om.Rules).To(Equal(tm.Rules), "rules")
				Expect(om.TailPlacement).To(Equal(tm.TailPlacement), "tail_placement")
				Expect(om.Prefix).To(MatchRegexp(prefixShape), "rgw-go's prefix")
				Expect(tm.Prefix).To(MatchRegexp(prefixShape), "radosgw's prefix")
				Expect(om.Prefix).NotTo(Equal(tm.Prefix), "each write draws its own prefix")

				// The attrs, as RADOS holds them on the two heads.
				mine, theirs := rawXattrs(ctx, headOID(st)), rawXattrs(ctx, headOID(tst))
				AddReportEntry("radosgw's head attrs", slices.Sorted(maps.Keys(theirs)))
				AddReportEntry("rgw-go's head attrs", slices.Sorted(maps.Keys(mine)))
				drop := []string{}
				if store.Release() == denc.Squid {
					for _, h := range squidStoredHeaders {
						Expect(theirs).To(HaveKey(h), "v19.2.6 stores %s", h)
					}
					drop = squidStoredHeaders
				}
				Expect(attrsView(mine, perRequest, nil)).To(Equal(attrsView(theirs, perRequest, drop)),
					"rgw-go's head attrs against radosgw's for the same request")
				Expect(o.keys()).To(Equal(slices.DeleteFunc(t.keys(), func(k string) bool { return slices.Contains(drop, k) })),
					"the attrs object stat shows")
				Expect(mine).To(HaveKeyWithValue(meta.AttrETag, []byte(md5Hex(body))), "user.rgw.etag carries no NUL")
				Expect(mine).To(HaveKeyWithValue(meta.AttrIDTag, HaveSuffix("\x00")), "user.rgw.idtag is NUL-terminated")
				Expect(theirs).To(HaveKeyWithValue(meta.AttrIDTag, HaveSuffix("\x00")), "radosgw's idtag is NUL-terminated")

				out, got := getRadosgw(ctx, name, key)
				Expect(got).To(Equal(body), "the bytes read back through radosgw")
				Expect(aws.ToString(out.ETag)).To(Equal(`"`+md5Hex(body)+`"`), "the ETag radosgw serves")
				Expect(out.Metadata).To(Equal(map[string]string{"k": "v"}), "the metadata radosgw serves")
				Expect(aws.ToString(out.ContentType)).To(Equal("text/plain"), "the Content-Type radosgw serves")

				expectBucketCheckClean(ctx, name)
				usage := bucketUsage(ctx, name)
				Expect(usage).To(Equal(adminUsage{Size: 2 * uint64(size), SizeActual: 2 * roundedSize(uint64(size)), NumObjects: 2}),
					"bucket stats")
				Expect(store.BucketStats(ctx, freshBucket(ctx, rec))).To(Equal(op.Stats{Size: usage.Size, SizeRounded: usage.SizeActual, NumObjects: usage.NumObjects}),
					"rgw-go's BucketStats")
			},
			Entry("1 KiB, within the head", 1*kib),
			Entry("exactly 4 MiB, the head full", 4*mib),
			Entry("10 MiB, with two tails", 10*mib),
			Entry("empty", 0),
		)

		It("queues an overwritten object's tails, which radosgw's collector frees", func(ctx SpecContext) {
			putRGWGo(ctx, rec, "obj", randBytes(10*mib), op.PutParams{Attrs: rgwgoAttrs(http.Header{})})
			old := stat(ctx, rec, "obj")
			oldTails := tails(old)
			Expect(oldTails).To(HaveLen(2), "a 10 MiB object's tails")
			tag := string(rawXattrs(ctx, headOID(old))[meta.AttrTailTag])
			Expect(tag).To(HaveSuffix("\x00"), "the tail tag")
			oldPrefix := old.Manifest.Prefix

			body := randBytes(1 * kib)
			putRGWGo(ctx, rec, "obj", body, op.PutParams{Attrs: rgwgoAttrs(http.Header{})})
			Expect(gcChain(gcList(ctx), tag)).To(Equal(oldTails), "the gc entry under the old tail tag")
			m := objectStat(ctx, name, "obj").manifest()
			Expect(m.ObjSize).To(BeEquivalentTo(1*kib), "the new manifest's obj_size")
			Expect(m.HeadSize).To(BeEquivalentTo(1*kib), "the new manifest's head_size")
			Expect(m.Prefix).NotTo(Equal(oldPrefix), "the new manifest's prefix")
			Expect(dataOIDs(ctx, rec)).To(ContainElements(oldTails), "the old tails wait for the collector")

			gcProcess(ctx)
			Expect(dataOIDs(ctx, rec)).NotTo(ContainElements(oldTails[0]), "radosgw's collector freed rgw-go's garbage")
			Expect(dataOIDs(ctx, rec)).NotTo(ContainElements(oldTails[1]), "radosgw's collector freed rgw-go's garbage")
			Expect(gcChain(gcList(ctx), tag)).To(BeNil(), "the gc entry once processed")
			_, got := getRadosgw(ctx, name, "obj")
			Expect(got).To(Equal(body))
		})

		It("writes to a storage class's placement", func(ctx SpecContext) {
			class := slices.Sorted(maps.Keys(manifest.StorageClasses))[0]
			body := randBytes(10 * mib)
			putRGWGo(ctx, rec, "obj", body, op.PutParams{Attrs: rgwgoAttrs(http.Header{}), StorageClass: class})
			o := objectStat(ctx, name, "obj")
			Expect(o.Attrs).To(HaveKeyWithValue(meta.AttrStorageClass, class), "object stat's storage class")
			Expect(o.manifest().TailPlacement.PlacementRule).To(Equal(store.ZoneGroup().DefaultPlacement.Name+"/"+class),
				"the tail placement")
			// Compression on the write path is phase 2 (design §9), so rgw-go
			// stores the class's object as written.
			Expect(o.Compression).To(BeEmpty(), "rgw-go compresses nothing")
			head, err := s3Client(alice()).HeadObject(ctx, &awss3.HeadObjectInput{Bucket: aws.String(name), Key: aws.String("obj")})
			Expect(err).NotTo(HaveOccurred())
			Expect(head.StorageClass).To(BeEquivalentTo(class), "HeadObject through radosgw")
			Expect(bucketList(ctx, name)).To(HaveKeyWithValue("obj", HaveField("Meta.StorageClass", class)), "the index entry's class")
			_, got := getRadosgw(ctx, name, "obj")
			Expect(got).To(Equal(body))
		})

		It("leaves one object and no stray tail when two writers race on a key", func(ctx SpecContext) {
			bodies := [2][]byte{randBytes(5 * mib), randBytes(5 * mib)}
			var wg sync.WaitGroup
			errs := make([][]error, 2)
			for w := range 2 {
				wg.Go(func() {
					defer GinkgoRecover()
					for range 20 {
						_, err := store.PutObject(ctx, rec, meta.ObjKey{Name: "race"}, bytes.NewReader(bodies[w]),
							op.PutParams{Attrs: rgwgoAttrs(http.Header{}), Size: int64(len(bodies[w]))})
						errs[w] = append(errs[w], err)
					}
				})
			}
			wg.Wait()
			for w := range 2 {
				for i, err := range errs[w] {
					Expect(err).NotTo(HaveOccurred(), "writer %d, write %d", w, i)
				}
			}

			expectBucketCheckClean(ctx, name)
			o := objectStat(ctx, name, "race")
			Expect(o.ETag).To(BeElementOf(md5Hex(bodies[0]), md5Hex(bodies[1])), "the surviving write's etag")
			listed := bucketList(ctx, name)
			Expect(listed).To(HaveLen(1), "bucket list")
			Expect(listed).To(HaveKeyWithValue("race", HaveField("Meta.ETag", o.ETag)), "the index entry")
			gcProcess(ctx)
			st := stat(ctx, rec, "race")
			Expect(dataOIDs(ctx, rec)).To(ConsistOf(append(tails(st), headOID(st))), "the data pool holds the survivor alone")
			_, got := getRadosgw(ctx, name, "race")
			Expect(md5Hex(got)).To(Equal(o.ETag), "the survivor read back through radosgw")
		})

		It("answers the write conditions as radosgw does", func(ctx SpecContext) {
			body := randBytes(1 * kib)
			etag := md5Hex(body)
			putRGWGo(ctx, rec, "obj", body, op.PutParams{Attrs: rgwgoAttrs(http.Header{})})
			_, err := store.PutObject(ctx, rec, meta.ObjKey{Name: "obj"}, bytes.NewReader(body),
				op.PutParams{Attrs: rgwgoAttrs(http.Header{}), Size: int64(len(body)), IfNoneMatch: "*"})
			Expect(err).To(MatchError(op.ErrPreconditionFailed), "rgw-go: If-None-Match * on an existing key")
			_, err = store.PutObject(ctx, rec, meta.ObjKey{Name: "missing"}, bytes.NewReader(body),
				op.PutParams{Attrs: rgwgoAttrs(http.Header{}), Size: int64(len(body)), IfMatch: "*"})
			Expect(err).To(MatchError(op.ErrNoSuchKey), "rgw-go: If-Match * on a missing key")
			putRGWGo(ctx, rec, "obj", body, op.PutParams{Attrs: rgwgoAttrs(http.Header{}), IfMatch: `"` + etag + `"`})
			putRGWGo(ctx, rec, "new", body, op.PutParams{Attrs: rgwgoAttrs(http.Header{}), IfNoneMatch: etag})

			s3c := s3Client(alice())
			put := func(key string, set func(*awss3.PutObjectInput)) int {
				in := &awss3.PutObjectInput{Bucket: aws.String(name), Key: aws.String(key), Body: bytes.NewReader(body)}
				set(in)
				_, err := s3c.PutObject(ctx, in)
				if err == nil {
					return http.StatusOK
				}
				return statusOf(err)
			}
			Expect(put("obj", func(in *awss3.PutObjectInput) { in.IfNoneMatch = aws.String("*") })).To(Equal(http.StatusPreconditionFailed),
				"radosgw: If-None-Match * on an existing key")
			Expect(put("obj", func(in *awss3.PutObjectInput) { in.IfMatch = aws.String(etag) })).To(Equal(http.StatusOK),
				"radosgw: a bare If-Match naming the ETag")
			if store.Release() == denc.Squid {
				// docs/exclusions.md, "Write conditions are Tentacle's on Squid
				// too"; docs/ceph-upstream-bugs.md, "Squid's PUT refuses a
				// quoted If-Match that matches the object's ETag" and "Squid's
				// write conditions fail an If-None-Match ETag on a missing key".
				Expect(put("missing", func(in *awss3.PutObjectInput) { in.IfMatch = aws.String("*") })).To(Equal(http.StatusPreconditionFailed),
					"v19.2.6: If-Match * on a missing key")
				Expect(put("obj", func(in *awss3.PutObjectInput) { in.IfMatch = aws.String(`"` + etag + `"`) })).To(Equal(http.StatusPreconditionFailed),
					"v19.2.6: a quoted If-Match naming the ETag")
				Expect(put("absent", func(in *awss3.PutObjectInput) { in.IfNoneMatch = aws.String(etag) })).To(Equal(http.StatusPreconditionFailed),
					"v19.2.6: If-None-Match naming an ETag on a missing key")
			} else {
				Expect(put("missing", func(in *awss3.PutObjectInput) { in.IfMatch = aws.String("*") })).To(Equal(http.StatusNotFound),
					"v20.2.4: If-Match * on a missing key")
				Expect(put("obj", func(in *awss3.PutObjectInput) { in.IfMatch = aws.String(`"` + etag + `"`) })).To(Equal(http.StatusOK),
					"v20.2.4: a quoted If-Match naming the ETag")
				Expect(put("absent", func(in *awss3.PutObjectInput) { in.IfNoneMatch = aws.String(etag) })).To(Equal(http.StatusOK),
					"v20.2.4: If-None-Match naming an ETag on a missing key")
			}
			expectBucketCheckClean(ctx, name)
		})
	})

	Describe("DELETE and GC", func() {
		It("removes the head and queues the tails under the tail tag", func(ctx SpecContext) {
			putRGWGo(ctx, rec, "obj", randBytes(10*mib), op.PutParams{Attrs: rgwgoAttrs(http.Header{})})
			st := stat(ctx, rec, "obj")
			objTails := tails(st)
			tag := string(rawXattrs(ctx, headOID(st))[meta.AttrTailTag])
			Expect(store.DeleteObject(ctx, rec, meta.ObjKey{Name: "obj"}, op.DeleteParams{})).To(Succeed())

			Expect(bucketList(ctx, name)).NotTo(HaveKey("obj"), "bucket list")
			Expect(gcChain(gcList(ctx), tag)).To(Equal(objTails), "the gc entry under the tail tag")
			_, err := rawStat(ctx, headOID(st))
			Expect(isNotFound(err)).To(BeTrue(), "the head is gone: %v", err)
			expectBucketCheckClean(ctx, name)
			Expect(dataOIDs(ctx, rec)).To(Equal(objTails), "the tails wait for the collector")
			gcProcess(ctx)
			Expect(dataOIDs(ctx, rec)).To(BeEmpty(), "radosgw's collector freed the tails")
		})

		It("frees radosgw's garbage with rgw-go's collector", func(ctx SpecContext) {
			_, large := getRadosgw(ctx, "plain", "large.bin")
			s3c := s3Client(alice())
			_, err := s3c.PutObject(ctx, &awss3.PutObjectInput{Bucket: aws.String(name), Key: aws.String("large-copy.bin"), Body: bytes.NewReader(large)})
			Expect(err).NotTo(HaveOccurred())
			st := stat(ctx, rec, "large-copy.bin")
			objTails := tails(st)
			Expect(objTails).To(HaveLen(2), "radosgw's tails")
			tag := string(rawXattrs(ctx, headOID(st))[meta.AttrTailTag])
			_, err = s3c.DeleteObject(ctx, &awss3.DeleteObjectInput{Bucket: aws.String(name), Key: aws.String("large-copy.bin")})
			Expect(err).NotTo(HaveOccurred())
			Expect(gcChain(gcList(ctx), tag)).To(Equal(objTails), "radosgw's gc entry")
			expectBucketCheckClean(ctx, name)

			// Every shard of a cluster radosgw started is in the rgw_gc queue.
			shards, err := configUint(cluster, "rgw_gc_max_objs")
			Expect(err).NotTo(HaveOccurred())
			gcPool := pool(ctx, store.ZoneParams().GCPool)
			for i := range int(shards) {
				rop := radosclient.NewReadOp()
				v := version.Read(rop, store.Release())
				_, err := gcPool.Read(ctx, gc.ShardOID(i), rop, radosclient.OpFlagNone)
				Expect(err).NotTo(HaveOccurred(), "reading %s's version", gc.ShardOID(i))
				Expect(v.Version()).To(HaveField("Ver", BeEquivalentTo(1)), "%s is queue-era", gc.ShardOID(i))
			}

			expectGCShardsFree(ctx)
			Expect(store.GCProcessForTest(ctx, false)).To(Succeed())
			Expect(dataOIDs(ctx, rec)).To(BeEmpty(), "rgw-go's collector freed radosgw's tails")
			Expect(gcList(ctx)).To(BeEmpty(), "gc list --include-all once rgw-go's pass ran")
			var due []gcEntry
			adminJSON(ctx, &due, "gc", "list")
			Expect(due).To(BeEmpty(), "gc list of the shards rgw-go processed")
		})

		It("completes every write across a reshard of the bucket", func(ctx SpecContext) {
			logs := captureLogs()
			const writes = 200
			oldShard := ".dir." + rec.Info.Bucket.ID + "."
			started, blocked, resharded := make(chan struct{}), make(chan struct{}), make(chan struct{})
			done := make(chan error, 1)
			var stale []string
			// The reshard blocks the old index's writes only while it copies
			// the entries, a few milliseconds for these: the writes past the
			// twentieth wait to see it block them, and then follow one
			// another at once, so that one meets the block.
			idx := pool(ctx, meta.Pool{Name: manifest.Pools.Index})
			go func() {
				defer GinkgoRecover()
				defer close(blocked)
				tick := time.NewTicker(time.Millisecond)
				defer tick.Stop()
				for {
					rop := radosclient.NewReadOp()
					res := rgwcls.GetBucketResharding(rop, store.Release())
					if _, err := idx.Read(ctx, oldShard+"0", rop, radosclient.OpFlagNone); err == nil {
						if e, err := res.Result(); err == nil && e.ReshardStatus == rgwcls.ReshardInProgress {
							return
						}
					}
					select {
					case <-resharded:
						return
					case <-ctx.Done():
						return
					case <-tick.C:
					}
				}
			}()
			go func() {
				defer GinkgoRecover()
				body := randBytes(1 * kib)
				// Each write reads the bucket as a request does, through the
				// cache the reshard's notify invalidates.
				put := func(i int) error {
					cur, err := store.GetBucket(ctx, "", name)
					if err != nil {
						return err
					}
					_, err = store.PutObject(ctx, cur, meta.ObjKey{Name: fmt.Sprintf("k%03d", i)}, bytes.NewReader(body),
						op.PutParams{Attrs: rgwgoAttrs(http.Header{}), Size: int64(len(body))})
					return err
				}
				for i := range writes {
					if i == 20 {
						close(started)
						<-blocked
					}
					err := put(i)
					// A write that read the bucket before the reshard committed
					// and prepares once the old index is gone fails with
					// ENOENT, as radosgw's does (docs/ceph-upstream-bugs.md,
					// "radosgw fails a write whose bucket was resharded since
					// the request read it"). The write left nothing, and is
					// sent again, as its client would.
					if errors.Is(err, op.ErrNoSuchKey) && strings.Contains(err.Error(), oldShard) {
						stale = append(stale, err.Error())
						err = put(i)
					}
					if err != nil {
						done <- fmt.Errorf("write %d: %w", i, err)
						return
					}
				}
				done <- nil
			}()
			Eventually(started).WithTimeout(time.Minute).WithPolling(10 * time.Millisecond).Should(BeClosed())
			admin(ctx, "bucket", "reshard", "--bucket", name, "--num-shards", "23", "--yes-i-really-mean-it")
			close(resharded)
			var werr error
			Eventually(done).WithTimeout(5 * time.Minute).WithPolling(100 * time.Millisecond).Should(Receive(&werr))
			Expect(werr).NotTo(HaveOccurred())
			AddReportEntry("writes that met the removed index", stale)
			Expect(len(stale)).To(BeNumerically("<=", 1), "only the write in flight at the commit meets the removed index: %v", stale)
			Expect(freshBucket(ctx, rec).Info.Layout.Current.Layout.Normal.NumShards).To(BeEquivalentTo(23), "the bucket's shards")

			expectBucketCheckClean(ctx, name)
			Expect(bucketUsage(ctx, name).NumObjects).To(BeEquivalentTo(writes), "bucket stats' num_objects")
			Expect(bucketList(ctx, name)).To(HaveLen(writes), "bucket list")
			waits := strings.Count(logs.String(), "bucket index shard is resharding; waiting")
			AddReportEntry("writes that waited out the reshard", waits)
			Expect(waits).To(BeNumerically(">=", 1), "a write met the reshard")
		})
	})

	Describe("COPY and subresources", func() {
		It("shares a same-placement source's tails through the refcount class", func(ctx SpecContext) {
			body := randBytes(10 * mib)
			putRGWGo(ctx, rec, "src", body, op.PutParams{Attrs: rgwgoAttrs(http.Header{})})
			src := prefetch(ctx, rec, "src")
			srcTails := tails(src)
			_, err := store.CopyObject(ctx, src, rec, meta.ObjKey{Name: "dst"}, op.CopyParams{Attrs: map[string][]byte{meta.AttrACL: aliceACL()}})
			Expect(err).NotTo(HaveOccurred())

			o, so := objectStat(ctx, name, "dst"), objectStat(ctx, name, "src")
			Expect(o.manifest().Prefix).To(Equal(so.manifest().Prefix), "the copy names the source's tails")
			Expect(o.ETag).To(Equal(so.ETag), "the copy's etag")
			dstTag := string(rawXattrs(ctx, headOID(stat(ctx, rec, "dst")))[meta.AttrTailTag])
			Expect(dstTag).To(HaveSuffix("\x00"), "the copy's tail tag")
			refs := func(oid string) map[string]bool {
				b := rawXattrs(ctx, oid)[refcount.XattrName]
				Expect(b).NotTo(BeEmpty(), "%s's refcount", oid)
				d := denc.NewDecoder(b)
				rc := refcount.DecodeRefcount(d)
				Expect(d.Err()).NotTo(HaveOccurred(), "decoding %s's refcount", oid)
				return rc.Refs
			}
			for _, t := range srcTails {
				Expect(refs(t)).To(Equal(map[string]bool{"": true, dstTag: true}), "%s's references", t)
			}
			_, got := getRadosgw(ctx, name, "dst")
			Expect(got).To(Equal(body), "the copy read back through radosgw")

			Expect(store.DeleteObject(ctx, rec, meta.ObjKey{Name: "src"}, op.DeleteParams{})).To(Succeed())
			gcProcess(ctx)
			for _, t := range srcTails {
				Expect(refs(t)).To(Equal(map[string]bool{dstTag: true}), "%s's reference left once the source is collected", t)
			}
			_, got = getRadosgw(ctx, name, "dst")
			Expect(got).To(Equal(body), "the copy read back once the source is gone")

			Expect(store.DeleteObject(ctx, rec, meta.ObjKey{Name: "dst"}, op.DeleteParams{})).To(Succeed())
			gcProcess(ctx)
			Expect(dataOIDs(ctx, rec)).To(BeEmpty(), "the shared tails once both are collected")
		})

		It("stores a REPLACE copy's request headers as radosgw does", func(ctx SpecContext) {
			body := randBytes(1 * kib)
			putRGWGo(ctx, rec, "src", body, op.PutParams{Attrs: rgwgoAttrs(http.Header{})})
			rh := &headerRecorder{}
			_, err := recordingClient(alice(), rh).CopyObject(ctx, &awss3.CopyObjectInput{
				Bucket: aws.String(name), Key: aws.String("dst-radosgw"), CopySource: aws.String(name + "/src"),
				MetadataDirective: s3types.MetadataDirectiveReplace, ContentType: aws.String("text/plain"), Metadata: map[string]string{"k": "v"},
			})
			Expect(err).NotTo(HaveOccurred(), "CopyObject through radosgw")
			attrs, err := s3.RequestAttrsForIntegration(putRequest(rh.header()), false)
			Expect(err).NotTo(HaveOccurred())
			attrs[meta.AttrACL] = aliceACL()
			_, err = store.CopyObject(ctx, prefetch(ctx, rec, "src"), rec, meta.ObjKey{Name: "dst-rgwgo"}, op.CopyParams{Attrs: attrs, ReplaceAttrs: true})
			Expect(err).NotTo(HaveOccurred())

			mine, theirs := rawXattrs(ctx, headOID(stat(ctx, rec, "dst-rgwgo"))), rawXattrs(ctx, headOID(stat(ctx, rec, "dst-radosgw")))
			AddReportEntry("radosgw's REPLACE copy's head attrs", slices.Sorted(maps.Keys(theirs)))
			// Every x-amz- header but those the blocklist names is stored, the
			// copy's own among them.
			for _, h := range []string{"user.rgw.x-amz-copy-source", "user.rgw.x-amz-metadata-directive", "user.rgw.x-amz-meta-k"} {
				Expect(theirs).To(HaveKey(h), "radosgw stores %s", h)
			}
			drop := []string{}
			if store.Release() == denc.Squid {
				drop = squidStoredHeaders
			}
			Expect(attrsView(mine, perRequest, nil)).To(Equal(attrsView(theirs, perRequest, drop)), "rgw-go's REPLACE copy against radosgw's")
		})

		It("streams a copy to another storage class into tails of its own", func(ctx SpecContext) {
			class := slices.Sorted(maps.Keys(manifest.StorageClasses))[0]
			body := randBytes(10 * mib)
			putRGWGo(ctx, rec, "src", body, op.PutParams{Attrs: rgwgoAttrs(http.Header{})})
			src := prefetch(ctx, rec, "src")
			_, err := store.CopyObject(ctx, src, rec, meta.ObjKey{Name: "dst"},
				op.CopyParams{Attrs: map[string][]byte{meta.AttrACL: aliceACL()}, StorageClass: class})
			Expect(err).NotTo(HaveOccurred())
			o, so := objectStat(ctx, name, "dst"), objectStat(ctx, name, "src")
			m := o.manifest()
			Expect(m.Prefix).NotTo(Equal(so.manifest().Prefix), "the copy's own tails")
			Expect(m.TailPlacement.PlacementRule).To(Equal(store.ZoneGroup().DefaultPlacement.Name+"/"+class), "the copy's tail placement")
			for _, t := range tails(src) {
				Expect(rawXattrs(ctx, t)).NotTo(HaveKey(refcount.XattrName), "a streamed copy takes no reference on %s", t)
			}
			dst := stat(ctx, rec, "dst")
			Expect(dataOIDs(ctx, rec)).To(ContainElements(tails(dst)), "the copy's tails in the class's pool")
			_, got := getRadosgw(ctx, name, "dst")
			Expect(got).To(Equal(body))
		})

		It("copies a compressed source's stored bytes as a Squid radosgw does", func(ctx SpecContext) {
			plain, err := store.GetBucket(ctx, "", "plain")
			Expect(err).NotTo(HaveOccurred())
			src := prefetch(ctx, plain, "comp-zlib.bin")
			Expect(src.Compression).NotTo(BeNil(), "the corpus object is compressed")
			_, err = store.CopyObject(ctx, src, rec, meta.ObjKey{Name: "cc-rgwgo"},
				op.CopyParams{Attrs: map[string][]byte{meta.AttrACL: aliceACL()}, StorageClass: "STANDARD"})
			Expect(err).NotTo(HaveOccurred())
			_, err = s3Client(alice()).CopyObject(ctx, &awss3.CopyObjectInput{
				Bucket: aws.String(name), Key: aws.String("cc-radosgw"), CopySource: aws.String("plain/comp-zlib.bin"),
				StorageClass: s3types.StorageClassStandard,
			})
			Expect(err).NotTo(HaveOccurred(), "CopyObject through radosgw")

			so := objectStat(ctx, "plain", "comp-zlib.bin")
			mine, theirs := objectStat(ctx, name, "cc-rgwgo"), objectStat(ctx, name, "cc-radosgw")
			Expect(canonical(mine.Compression)).To(Equal(canonical(so.Compression)), "rgw-go's copy keeps the source's compression info")
			Expect(mine.manifest().ObjSize).To(Equal(so.manifest().ObjSize), "rgw-go's copy stores the source's stored size")
			mineRaw, theirsRaw := rawXattrs(ctx, headOID(stat(ctx, rec, "cc-rgwgo"))), rawXattrs(ctx, headOID(stat(ctx, rec, "cc-radosgw")))
			Expect(mineRaw).To(HaveKeyWithValue(meta.AttrStorageClass, []byte("STANDARD")), "the class the copy names")
			listed := bucketList(ctx, name)
			Expect(listed).To(HaveKeyWithValue("cc-rgwgo", HaveField("Meta.AccountedSize", src.Compression.OrigSize)), "rgw-go's accounted_size")
			Expect(listed).To(HaveKeyWithValue("cc-rgwgo", HaveField("Meta.Size", so.manifest().ObjSize)), "rgw-go's size")
			if store.Release() == denc.Squid {
				Expect(canonical(theirs.Compression)).To(Equal(canonical(so.Compression)), "radosgw's copy keeps the source's compression info")
				Expect(theirs.manifest().ObjSize).To(Equal(so.manifest().ObjSize), "radosgw's copy stores the source's stored size")
				Expect(attrsView(mineRaw, perRequest, nil)).To(Equal(attrsView(theirsRaw, perRequest, nil)), "rgw-go's copy's attrs against radosgw's")
				Expect(listed["cc-rgwgo"].Meta).To(Equal(listed["cc-radosgw"].Meta), "the two copies' index entries")
			} else {
				AddReportEntry("cc-radosgw on v20.2.4", listed["cc-radosgw"].Meta)
			}

			// Copied into the placement's default class without naming one,
			// a v19.2.6 radosgw keeps the source's label on a head in
			// STANDARD (docs/ceph-upstream-bugs.md, "Squid labels a copy to
			// another storage class with its source's class"); rgw-go labels
			// the copy by its destination, as v20.2.4 does (docs/exclusions.md,
			// "A copy is labeled with its destination's storage class on both
			// releases").
			_, err = store.CopyObject(ctx, src, rec, meta.ObjKey{Name: "cd-rgwgo"}, op.CopyParams{Attrs: map[string][]byte{meta.AttrACL: aliceACL()}})
			Expect(err).NotTo(HaveOccurred())
			_, err = s3Client(alice()).CopyObject(ctx, &awss3.CopyObjectInput{
				Bucket: aws.String(name), Key: aws.String("cd-radosgw"), CopySource: aws.String("plain/comp-zlib.bin"),
			})
			Expect(err).NotTo(HaveOccurred(), "CopyObject through radosgw")
			Expect(rawXattrs(ctx, headOID(stat(ctx, rec, "cd-rgwgo")))).NotTo(HaveKey(meta.AttrStorageClass), "rgw-go's copy into the default class")
			headClass := func(key string) s3types.StorageClass {
				GinkgoHelper()
				h, err := s3Client(alice()).HeadObject(ctx, &awss3.HeadObjectInput{Bucket: aws.String(name), Key: aws.String(key)})
				Expect(err).NotTo(HaveOccurred())
				return h.StorageClass
			}
			Expect(headClass("cd-rgwgo")).To(BeEmpty(), "HeadObject of rgw-go's copy names no class, as for STANDARD")
			listed = bucketList(ctx, name)
			Expect(listed).To(HaveKeyWithValue("cd-radosgw", HaveField("Meta.StorageClass", "STANDARD")), "radosgw's copy's index entry")
			Expect(listed).To(HaveKeyWithValue("cd-rgwgo", HaveField("Meta.StorageClass", "STANDARD")), "rgw-go's copy's index entry")
			if store.Release() == denc.Squid {
				Expect(rawXattrs(ctx, headOID(stat(ctx, rec, "cd-radosgw")))).To(HaveKeyWithValue(meta.AttrStorageClass, []byte("COMP_ZLIB")),
					"v19.2.6 keeps the source's class")
				Expect(headClass("cd-radosgw")).To(BeEquivalentTo("COMP_ZLIB"), "v19.2.6 reports the source's class")
			}

			_, want := getRadosgw(ctx, "plain", "comp-zlib.bin")
			for _, k := range []string{"cc-rgwgo", "cc-radosgw", "cd-rgwgo", "cd-radosgw"} {
				_, got := getRadosgw(ctx, name, k)
				Expect(got).To(Equal(want), "%s read back through radosgw", k)
			}
		})

		It("changes an object's ACL with a new write tag and the mtime one nanosecond on, as radosgw does", func(ctx SpecContext) {
			body := randBytes(1 * kib)
			putRGWGo(ctx, rec, "obj", body, op.PutParams{Attrs: rgwgoAttrs(http.Header{})})
			_, err := s3Client(alice()).PutObject(ctx, &awss3.PutObjectInput{Bucket: aws.String(name), Key: aws.String("twin"), Body: bytes.NewReader(body)})
			Expect(err).NotTo(HaveOccurred())
			st := stat(ctx, rec, "obj")
			before := objectStat(ctx, name, "obj")
			mtime := func(key string) time.Time {
				GinkgoHelper()
				s, serr := rawStat(ctx, headOID(stat(ctx, rec, key)))
				Expect(serr).NotTo(HaveOccurred())
				return s.ModTime
			}
			mine0, twin0 := mtime("obj"), mtime("twin")
			Expect(st.Mtime).To(BeTemporally("==", mine0), "StatObject's mtime is the head's")

			owner := acl.Owner{ID: aliceOwner().String(), DisplayName: alice().UID}
			pol, err := acl.Canned(owner, owner, "public-read")
			Expect(err).NotTo(HaveOccurred())
			Expect(store.SetObjectAttrs(ctx, st, map[string][]byte{meta.AttrACL: encodeAt(pol)}, nil)).To(Succeed())
			_, err = s3Client(alice()).PutObjectAcl(ctx, &awss3.PutObjectAclInput{Bucket: aws.String(name), Key: aws.String("twin"), ACL: s3types.ObjectCannedACLPublicRead})
			Expect(err).NotTo(HaveOccurred())

			after := objectStat(ctx, name, "obj")
			Expect(after.Tag).NotTo(Equal(before.Tag), "a new write tag")
			Expect(mtime("obj").Sub(mine0)).To(Equal(time.Nanosecond), "rgw-go's mtime")
			Expect(mtime("twin").Sub(twin0)).To(Equal(time.Nanosecond), "radosgw's mtime after PutObjectAcl")
			grants, err := s3Client(alice()).GetObjectAcl(ctx, &awss3.GetObjectAclInput{Bucket: aws.String(name), Key: aws.String("obj")})
			Expect(err).NotTo(HaveOccurred())
			Expect(grants.Grants).To(ContainElement(And(
				HaveField("Grantee.URI", HaveValue(Equal("http://acs.amazonaws.com/groups/global/AllUsers"))),
				HaveField("Permission", s3types.PermissionRead))), "GetObjectAcl through radosgw")
			Expect(bucketList(ctx, name)).To(HaveKeyWithValue("obj", HaveField("Meta.Owner", alice().ID())), "the index entry's owner")
			expectBucketCheckClean(ctx, name)
		})

		It("is answered by radosgw's DeleteObjects early failures with an empty 400", func(ctx SpecContext) {
			var many strings.Builder
			many.WriteString("<Delete>")
			for i := range 1001 {
				many.WriteString("<Object><Key>k" + strconv.Itoa(i) + "</Key></Object>")
			}
			many.WriteString("</Delete>")
			u := alice()
			for _, c := range []struct {
				what string
				body []byte
			}{
				{"1001 keys", []byte(many.String())},
				{"a body that does not parse", []byte("<Delete><Object>")},
				{"an empty body", nil},
			} {
				sum := sha256.Sum256(c.body)
				hash := hex.EncodeToString(sum[:])
				var rd io.Reader = http.NoBody
				if len(c.body) > 0 {
					rd = bytes.NewReader(c.body)
				}
				req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/"+name+"?delete", rd)
				Expect(err).NotTo(HaveOccurred())
				req.ContentLength = int64(len(c.body))
				req.Header.Set("X-Amz-Content-Sha256", hash)
				Expect(v4.NewSigner().SignHTTP(ctx, aws.Credentials{AccessKeyID: u.AccessKey, SecretAccessKey: u.SecretKey},
					req, hash, "s3", "us-east-1", time.Now())).To(Succeed())
				resp, err := http.DefaultClient.Do(req)
				Expect(err).NotTo(HaveOccurred(), c.what)
				got, err := io.ReadAll(resp.Body)
				Expect(err).NotTo(HaveOccurred())
				Expect(resp.Body.Close()).To(Succeed())
				AddReportEntry("DeleteObjects, "+c.what, fmt.Sprintf("%d %v", resp.StatusCode, resp.Header))
				Expect(resp.StatusCode).To(Equal(http.StatusBadRequest), c.what)
				Expect(got).To(BeEmpty(), "%s: the body", c.what)
				Expect(resp.Header.Get("Content-Length")).To(Equal("0"), "%s: Content-Length", c.what)
				// end_header never runs, so none of the headers it sends is
				// there; Date and Connection are the frontend's own.
				for _, h := range []string{"X-Amz-Request-Id", "Server", "Content-Type"} {
					Expect(resp.Header).NotTo(HaveKey(h), "%s: %s", c.what, h)
				}
			}
		})
	})
})
