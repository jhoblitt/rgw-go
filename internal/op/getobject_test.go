package op_test

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // SSE-C sends the MD5 of its key, and an S3 ETag is an MD5
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/cephconf"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

// payload is n bytes that differ from offset to offset.
func payload(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i % 251)
	}
	return b
}

func md5Hex(b []byte) string {
	sum := md5.Sum(b) //nolint:gosec // an S3 ETag is an MD5
	return hex.EncodeToString(sum[:])
}

// captureSink records what GetObject hands its sink.
type captureSink struct {
	status  int
	headers int
	body    bytes.Buffer
	flushed bool
}

func (s *captureSink) WriteHeader(status int, _ http.Header) {
	s.status = status
	s.headers++
}

func (s *captureSink) Write(p []byte) (int, error) { return s.body.Write(p) }

func (s *captureSink) Flush() error {
	s.flushed = true
	return nil
}

// smallMtime is when memstore writes the specs' objects.
func smallMtime() time.Time { return time.Date(2026, 9, 27, 1, 2, 3, 456_000_000, time.UTC) }

// sseCKey is the customer key the SSE specs use: 32 bytes of 1.
func sseCKey() []byte { return bytes.Repeat([]byte{1}, 32) }

// sseCKeyMD5 is sseCKey's MD5.
func sseCKeyMD5() []byte {
	sum := md5.Sum(sseCKey()) //nolint:gosec // SSE-C sends the MD5 of its key
	return sum[:]
}

// dataReads counts the RADOS reads ReadObject makes for rng of st, as
// get_obj_iterate_cb makes them and the driver after it: one per stripe the
// range touches, except a stripe of head, whose object offsets st.Head holds,
// which it serves from the prefetch. radosgw and the driver split a stripe
// larger than rgw_get_obj_max_req_size into several reads; the specs' stripes
// are 4 MiB, the option's default, so one read a stripe agrees with them.
func dataReads(st *op.ObjectState, rng op.ByteRange, head meta.Obj) int {
	GinkgoHelper()
	stripes, err := st.Manifest.Stripes()
	Expect(err).NotTo(HaveOccurred())
	reads := 0
	for _, s := range stripes {
		lo, hi := max(s.Ofs, rng.Offset), min(s.Ofs+s.Size, rng.Offset+rng.Length)
		if lo >= hi || s.Obj == head && hi <= uint64(len(st.Head)) {
			continue
		}
		reads++
	}
	return reads
}

// decodeGoldenManifest decodes a manifest radosgw wrote, from meta's goldens.
func decodeGoldenManifest(name string) meta.Manifest {
	GinkgoHelper()
	b, err := os.ReadFile(filepath.Join("..", "meta", "testdata", "manifests", name+".bin"))
	Expect(err).NotTo(HaveOccurred())
	d := denc.NewDecoder(b)
	m := meta.DecodeManifest(d)
	Expect(d.Err()).NotTo(HaveOccurred())
	return m
}

var _ = Describe("GetObject", func() {
	var (
		store *memstore.Store
		env   *op.Env
		alice op.Identity
		sink  *captureSink
	)
	BeforeEach(func(ctx SpecContext) {
		store = memstore.New(memstore.Config{Release: denc.Squid, Now: smallMtime})
		env = &op.Env{
			Zone: store, Users: store, Buckets: store, Objects: store, Usage: store,
			Authz: op.OwnerOnly{}, Metrics: op.NopMetrics{},
			Conf: cephconf.NewOptions(cephconf.MapGetter{"rgw_ignore_get_invalid_range": "false", "rgw_crypt_require_ssl": "true"}),
		}
		a := store.AddUser(meta.UserInfo{UserID: meta.UserID{ID: "alice"}, OpMask: op.OpTypeAll})
		alice = op.Identity{User: &a.Info, Owner: meta.UserOwner(a.Info.UserID), OpMask: op.OpTypeAll}
		rec, err := store.CreateBucket(ctx, op.CreateBucketParams{Name: "plain", Owner: alice.Owner, Placement: meta.PlacementRule{Name: "default-placement"}})
		Expect(err).NotTo(HaveOccurred())
		_, err = store.PutObject(ctx, rec, meta.ObjKey{Name: "small"}, bytes.NewReader(payload(1024)),
			op.PutParams{Size: 1024, Attrs: map[string][]byte{meta.AttrContentType: []byte("text/plain")}})
		Expect(err).NotTo(HaveOccurred())
		_, err = store.PutObject(ctx, rec, meta.ObjKey{Name: "empty"}, bytes.NewReader(nil), op.PutParams{Size: 0})
		Expect(err).NotTo(HaveOccurred())
		sink = &captureSink{}
	})
	req := func(key string) *op.Request {
		return &op.Request{Method: http.MethodGet, Bucket: "plain", Object: meta.ObjKey{Name: key}, Identity: alice, Env: env, Header: http.Header{}, Query: url.Values{}}
	}
	get := func(key string, mut func(o *op.GetObject)) (*op.GetObject, *op.Request, error) {
		sink = &captureSink{}
		o := &op.GetObject{GetData: true, Sink: sink}
		if mut != nil {
			mut(o)
		}
		r := req(key)
		return o, r, op.Run(context.Background(), o, r)
	}
	tentacle := func() *opfakes.FakeZoneInfo {
		zone := &opfakes.FakeZoneInfo{}
		zone.ReleaseReturns(denc.Tentacle)
		return zone
	}

	It("is radosgw's get_obj for GET and HEAD, a read", func() {
		for _, getData := range []bool{true, false} {
			o := &op.GetObject{GetData: getData}
			Expect(o.Name()).To(Equal("get_obj"))
			Expect(o.OpMask()).To(Equal(op.OpTypeRead))
		}
	})
	DescribeTable("authorizes the action RGWGetObj::verify_permission picks",
		func(torrent, versioned bool, want policy.Action) {
			o := &op.GetObject{Torrent: torrent, Versioned: versioned}
			Expect(o.Action()).To(Equal(want))
		},
		Entry("a GET", false, false, policy.S3GetObject),
		Entry("a GET of a version", false, true, policy.S3GetObjectVersion),
		Entry("?torrent", true, false, policy.S3GetObjectTorrent),
		Entry("?torrent of a version", true, true, policy.S3GetObjectVersionTorrent),
	)
	It("takes Versioned from the request's version instance and asks for that action's ACL permission", func() {
		authz := &opfakes.FakeAuthorizer{}
		env.Authz = authz
		o := &op.GetObject{GetData: true, Sink: sink}
		r := req("small")
		r.Object.Instance = "v1"
		Expect(op.Run(context.Background(), o, r)).To(MatchError(op.ErrNoSuchKey), "memstore has no instance v1")
		_, _, a, perm := authz.VerifyObjectArgsForCall(0)
		Expect(a).To(Equal(policy.S3GetObjectVersion))
		Expect(perm).To(Equal(acl.PermFor(policy.S3GetObjectVersion)))
		Expect(o.VersionID).To(Equal("v1"))
	})

	It("serves a whole object: 200, the full length, the body, the state, prefetch used", func() {
		o, r, err := get("small", nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(o.Status).To(Equal(http.StatusOK))
		Expect(o.Partial).To(BeFalse())
		Expect(o.Offset).To(BeZero())
		Expect(o.Length).To(BeEquivalentTo(1024))
		Expect(o.ObjSize).To(BeEquivalentTo(1024))
		Expect(o.PartsCount).To(BeNil())
		Expect(o.VersionID).To(BeEmpty())
		Expect(sink.body.Bytes()).To(Equal(payload(1024)))
		Expect(sink.flushed).To(BeTrue())
		Expect(r.ObjState).To(BeIdenticalTo(o.State))
		Expect(o.CondState).To(BeIdenticalTo(o.State))
		Expect(o.State.Head).NotTo(BeNil(), "a GET without Range prefetches")
		Expect(store.Usage()).To(BeEmpty(), "the handler logs usage, not the op")
	})
	It("HEAD stats without a prefetch, writes the header and no body", func() {
		o, _, err := get("small", func(o *op.GetObject) { o.GetData = false })
		Expect(err).NotTo(HaveOccurred())
		Expect(o.State.Head).To(BeNil())
		Expect(sink.status).To(Equal(http.StatusOK))
		Expect(sink.body.Len()).To(BeZero())
		Expect(o.Length).To(BeEquivalentTo(1024))
	})
	It("serves an empty object with its header and nothing to read", func() {
		o, _, err := get("empty", nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(o.Length).To(BeZero())
		Expect(sink.status).To(Equal(http.StatusOK))
		Expect(sink.headers).To(Equal(1))
		Expect(sink.body.Len()).To(BeZero())
	})
	It("answers NoSuchKey for a missing key and NoSuchBucket for a missing bucket", func() {
		_, _, err := get("nope", nil)
		Expect(err).To(MatchError(op.ErrNoSuchKey))
		o := &op.GetObject{GetData: true, Sink: sink}
		r := req("small")
		r.Bucket = "nobucket"
		Expect(op.Run(context.Background(), o, r)).To(MatchError(op.ErrNoSuchBucket))
	})

	Describe("Range", func() {
		It("serves a closed range as 206 without a prefetch", func() {
			o, _, err := get("small", func(o *op.GetObject) { o.Range = "bytes=10-19" })
			Expect(err).NotTo(HaveOccurred())
			Expect(o.State.Head).To(BeNil(), "a ranged GET does not prefetch")
			Expect(o.Status).To(Equal(http.StatusPartialContent))
			Expect(o.Partial).To(BeTrue())
			Expect(o.Offset).To(BeEquivalentTo(10))
			Expect(o.Length).To(BeEquivalentTo(10))
			Expect(sink.body.Bytes()).To(Equal(payload(1024)[10:20]))
		})
		It("serves a suffix range and clamps an end past the object", func() {
			o, _, err := get("small", func(o *op.GetObject) { o.Range = "bytes=-7" })
			Expect(err).NotTo(HaveOccurred())
			Expect([]uint64{o.Offset, o.Length}).To(Equal([]uint64{1017, 7}))
			o, _, err = get("small", func(o *op.GetObject) { o.Range = "bytes=1000-5000" })
			Expect(err).NotTo(HaveOccurred())
			Expect([]uint64{o.Offset, o.Length}).To(Equal([]uint64{1000, 24}))
		})
		It("serves a Range in another unit whole with 200, still without a prefetch", func() {
			o, _, err := get("small", func(o *op.GetObject) { o.Range = "items=0-1" })
			Expect(err).NotTo(HaveOccurred())
			Expect(o.Status).To(Equal(http.StatusOK))
			Expect(o.Length).To(BeEquivalentTo(1024))
			Expect(o.State.Head).To(BeNil())
		})
		It("answers InvalidRange past the end, for an inverted range and on an empty object", func() {
			_, _, err := get("small", func(o *op.GetObject) { o.Range = "bytes=2000-3000" })
			Expect(err).To(MatchError(op.ErrInvalidRange))
			_, _, err = get("small", func(o *op.GetObject) { o.Range = "bytes=10-5" })
			Expect(err).To(MatchError(op.ErrInvalidRange))
			_, _, err = get("empty", func(o *op.GetObject) { o.Range = "bytes=0-1" })
			Expect(err).To(MatchError(op.ErrInvalidRange))
			_, _, err = get("empty", func(o *op.GetObject) { o.Range = "items=0-1" })
			Expect(err).To(MatchError(op.ErrInvalidRange), "any Range header on an empty object")
		})
		It("serves the whole object for an invalid range when rgw_ignore_get_invalid_range is set", func() {
			env.Conf = cephconf.NewOptions(cephconf.MapGetter{"rgw_ignore_get_invalid_range": "true", "rgw_crypt_require_ssl": "true"})
			o, _, err := get("small", func(o *op.GetObject) { o.Range = "bytes=10-5" })
			Expect(err).NotTo(HaveOccurred())
			Expect(o.Status).To(Equal(http.StatusOK))
			Expect(o.Partial).To(BeFalse())
			Expect(o.Length).To(BeEquivalentTo(1024))
		})
		It("still refuses a range past the end when rgw_ignore_get_invalid_range is set", func() {
			env.Conf = cephconf.NewOptions(cephconf.MapGetter{"rgw_ignore_get_invalid_range": "true", "rgw_crypt_require_ssl": "true"})
			_, _, err := get("small", func(o *op.GetObject) { o.Range = "bytes=2000-3000" })
			Expect(err).To(MatchError(op.ErrInvalidRange))
		})
		It("HEAD with a Range is 206 with the range's length and no body", func() {
			o, _, err := get("small", func(o *op.GetObject) { o.GetData, o.Range = false, "bytes=0-9" })
			Expect(err).NotTo(HaveOccurred())
			Expect(o.Status).To(Equal(http.StatusPartialContent))
			Expect(sink.status).To(Equal(http.StatusPartialContent))
			Expect(o.Length).To(BeEquivalentTo(10))
			Expect(sink.body.Len()).To(BeZero())
		})
	})

	Describe("conditionals", func() {
		const (
			atMtime = "Sun, 27 Sep 2026 01:02:03 GMT" // the object's mtime at second precision
			before  = "Sun, 27 Sep 2026 01:02:02 GMT"
			after   = "Sun, 27 Sep 2026 01:02:04 GMT"
		)
		DescribeTable("evaluate as Read::prepare does",
			func(im, inm, ims, ius string, want error) {
				_, _, err := get("small", func(o *op.GetObject) {
					o.IfMatch, o.IfNoneMatch, o.IfModifiedSince, o.IfUnmodifiedSince = im, inm, ims, ius
				})
				if want == nil {
					Expect(err).NotTo(HaveOccurred())
				} else {
					Expect(err).To(MatchError(want))
				}
			},
			Entry("If-Match equal, quoted", `"`+md5Hex(payload(1024))+`"`, "", "", "", nil),
			Entry("If-Match equal, unquoted", md5Hex(payload(1024)), "", "", "", nil),
			Entry("If-Match with the etag as a prefix matches", `"`+md5Hex(payload(1024))+`-extra"`, "", "", "", nil),
			Entry("If-Match shorter than the etag", `"`+md5Hex(payload(1024))[:10]+`"`, "", "", "", op.ErrPreconditionFailed),
			Entry("If-Match different", `"ABCORZ"`, "", "", "", op.ErrPreconditionFailed),
			Entry("If-Match star is literal", "*", "", "", "", op.ErrPreconditionFailed),
			Entry("If-None-Match equal", "", `"`+md5Hex(payload(1024))+`"`, "", "", op.ErrNotModified),
			Entry("If-None-Match different", "", `"ABCORZ"`, "", "", nil),
			Entry("If-None-Match star never matches", "", "*", "", "", nil),
			Entry("If-Modified-Since before the mtime", "", "", before, "", nil),
			Entry("If-Modified-Since equal to the mtime's second", "", "", atMtime, "", op.ErrNotModified),
			Entry("If-Modified-Since after", "", "", after, "", op.ErrNotModified),
			Entry("If-Modified-Since ignored when If-None-Match is present", "", `"ABCORZ"`, after, "", nil),
			Entry("If-Unmodified-Since after the mtime", "", "", "", after, nil),
			Entry("If-Unmodified-Since equal", "", "", "", atMtime, nil),
			Entry("If-Unmodified-Since before", "", "", "", before, op.ErrPreconditionFailed),
			Entry("If-Unmodified-Since ignored when If-Match is present", `"`+md5Hex(payload(1024))+`"`, "", "", before, nil),
			Entry("If-Modified-Since is evaluated before If-Match", `"`+md5Hex(payload(1024))+`"`, "", after, "", op.ErrNotModified),
			Entry("If-Unmodified-Since is evaluated before If-None-Match", "", `"`+md5Hex(payload(1024))+`"`, "", before, op.ErrPreconditionFailed),
			Entry("an unparsable date is InvalidArgument", "", "", "yesterday", "", op.ErrInvalidArgument),
			Entry("a date before 1970 wraps past the mtime", "", "", "Thu, 01 Jan 1960 00:00:00 GMT", "", op.ErrNotModified),
		)
		It("parses the dates before it looks at the object", func() {
			_, _, err := get("nope", func(o *op.GetObject) { o.IfUnmodifiedSince = "yesterday" })
			Expect(err).To(MatchError(op.ErrInvalidArgument))
		})
		It("parses the Range before the dates", func() {
			_, _, err := get("small", func(o *op.GetObject) { o.Range, o.IfModifiedSince = "bytes=5", "yesterday" })
			Expect(err).To(MatchError(op.ErrInvalidRange))
		})
	})

	Describe("on a fake store", func() {
		const etagAttr = "e"
		var objects *opfakes.FakeObjectStore
		state := func(mut func(st *op.ObjectState)) *op.ObjectState {
			st := &op.ObjectState{
				Exists: true, Size: 10 << 20, Mtime: time.Unix(1700000000, 0), ETag: etagAttr,
				Attrs: map[string][]byte{meta.AttrETag: []byte(etagAttr)}, Key: meta.ObjKey{Name: "k"},
			}
			if mut != nil {
				mut(st)
			}
			return st
		}
		BeforeEach(func() {
			objects = &opfakes.FakeObjectStore{}
			env.Objects = objects
		})
		serve := func(st *op.ObjectState, mut func(o *op.GetObject)) (*op.GetObject, error) {
			objects.StatObjectReturns(st, nil)
			objects.PrefetchObjectReturns(st, nil)
			o, _, err := get("k", mut)
			return o, err
		}

		It("answers If-Match on an object without an etag attr as radosgw's -ENODATA", func() {
			_, err := serve(state(func(st *op.ObjectState) { delete(st.Attrs, meta.AttrETag) }), func(o *op.GetObject) { o.IfNoneMatch = "x" })
			Expect(err).To(MatchError(op.ErrUnknown))
			_, err = serve(state(func(st *op.ObjectState) { delete(st.Attrs, meta.AttrETag) }), nil)
			Expect(err).NotTo(HaveOccurred(), "the etag is read only for If-Match and If-None-Match")
		})

		Describe("compression", func() {
			It("reports orig_size for a compressed object and asks ReadObject for the decompressed range", func() {
				st := state(func(st *op.ObjectState) {
					st.Size = 5000
					st.Compression = &meta.CompressionInfo{Type: "zlib", OrigSize: 10 << 20, Blocks: []meta.CompressionBlock{{Len: 5000}}}
				})
				o, err := serve(st, func(o *op.GetObject) { o.Range = "bytes=100-199" })
				Expect(err).NotTo(HaveOccurred())
				Expect(o.ObjSize).To(BeEquivalentTo(10 << 20))
				_, _, rng, _ := objects.ReadObjectArgsForCall(0)
				Expect(rng).To(Equal(op.ByteRange{Offset: 100, Length: 100}))
			})
			It("refuses compression info without blocks as radosgw's -EIO", func() {
				_, err := serve(state(func(st *op.ObjectState) { st.Compression = &meta.CompressionInfo{Type: "zlib", OrigSize: 1} }), nil)
				Expect(err).To(MatchError(op.ErrUnknown))
			})
			It("serves compression info of type none with blocks at the stored size", func() {
				o, err := serve(state(func(st *op.ObjectState) {
					st.Compression = &meta.CompressionInfo{Type: "none", OrigSize: 1, Blocks: []meta.CompressionBlock{{Len: 5000}}}
				}), nil)
				Expect(err).NotTo(HaveOccurred())
				Expect(o.ObjSize).To(BeEquivalentTo(10 << 20))
			})
		})

		Describe("cloud tier", func() {
			tiered := func(tierType, sc string, restore []byte) *op.ObjectState {
				return state(func(st *op.ObjectState) {
					m := meta.NewManifest()
					m.TierType = tierType
					st.Manifest = &m
					st.Attrs[meta.AttrStorageClass] = []byte(sc)
					if restore != nil {
						st.Attrs[meta.AttrRestoreStatus] = restore
					}
				})
			}
			It("Squid: 403 InvalidObjectState on GET, headers on HEAD", func() {
				_, err := serve(tiered(meta.TierTypeCloudS3, "CLOUDTIER", nil), nil)
				Expect(err).To(MatchError(op.ErrInvalidObjectState))
				Expect(op.AsError(err).Message).To(Equal("This object was transitioned to cloud-s3"))
				_, err = serve(tiered(meta.TierTypeCloudS3, "CLOUDTIER", nil), func(o *op.GetObject) { o.GetData = false })
				Expect(err).NotTo(HaveOccurred())
			})
			It("Squid serves a cloud-s3-glacier manifest, which only Tentacle knows", func() {
				_, err := serve(tiered(meta.TierTypeCloudS3Glacier, "CLOUDTIER", nil), nil)
				Expect(err).NotTo(HaveOccurred())
			})
			Context("Tentacle", func() {
				BeforeEach(func() {
					zone := tentacle()
					zone.ZoneGroupReturns(meta.ZoneGroup{PlacementTargets: map[string]meta.ZoneGroupPlacementTarget{"default-placement": {TierTargets: map[string]meta.ZoneGroupPlacementTier{
						"CLOUDTIER": {TierType: meta.TierTypeCloudS3, StorageClass: "CLOUDTIER"},
						"READTHRU":  {TierType: meta.TierTypeCloudS3, StorageClass: "READTHRU", AllowReadThrough: true},
						"GLACIER":   {TierType: meta.TierTypeCloudS3Glacier, StorageClass: "GLACIER"},
						"OTHER":     {TierType: "other", StorageClass: "OTHER"},
					}}}})
					env.Zone = zone
				})
				It("a tier without read-through: 403 with radosgw's message", func() {
					_, err := serve(tiered(meta.TierTypeCloudS3, "CLOUDTIER", nil), nil)
					Expect(err).To(MatchError(op.ErrInvalidObjectState))
					Expect(op.AsError(err).Message).To(Equal("Read through is not enabled for this config"))
				})
				It("a glacier manifest and tier are cloud tiers too", func() {
					_, err := serve(tiered(meta.TierTypeCloudS3Glacier, "GLACIER", nil), nil)
					Expect(op.AsError(err).Message).To(Equal("Read through is not enabled for this config"))
				})
				It("a tier with read-through: 403, restore not implemented", func() {
					_, err := serve(tiered(meta.TierTypeCloudS3, "READTHRU", nil), nil)
					Expect(err).To(MatchError(op.ErrInvalidObjectState))
					Expect(op.AsError(err).Message).To(Equal("This object was transitioned to cloud-s3"))
				})
				It("a restore in progress: 400 RequestTimeout", func() {
					_, err := serve(tiered(meta.TierTypeCloudS3, "CLOUDTIER", []byte{byte(meta.RestoreAlreadyInProgress)}), nil)
					Expect(err).To(MatchError(op.ErrRequestTimeout))
					Expect(op.AsError(err).Status).To(Equal(http.StatusBadRequest))
					Expect(op.AsError(err).Message).To(Equal("restore is still in progress"))
				})
				It("a restored object is served", func() {
					_, err := serve(tiered(meta.TierTypeCloudS3, "CLOUDTIER", []byte{byte(meta.CloudRestored)}), nil)
					Expect(err).NotTo(HaveOccurred())
				})
				It("an empty restore status ends the check, as its decode does, and the object is served", func() {
					_, err := serve(tiered(meta.TierTypeCloudS3, "CLOUDTIER", []byte{}), nil)
					Expect(err).NotTo(HaveOccurred())
				})
				It("a failed restore looks the tier up again", func() {
					_, err := serve(tiered(meta.TierTypeCloudS3, "CLOUDTIER", []byte{byte(meta.RestoreFailed)}), nil)
					Expect(op.AsError(err).Message).To(Equal("Read through is not enabled for this config"))
				})
				It("a storage class without a tier fails the lookup with NoSuchKey", func() {
					_, err := serve(tiered(meta.TierTypeCloudS3, "NOPE", nil), nil)
					Expect(err).To(MatchError(op.ErrNoSuchKey))
				})
				It("a tier that is not an S3 tier is InvalidArgument", func() {
					_, err := serve(tiered(meta.TierTypeCloudS3, "OTHER", nil), nil)
					Expect(err).To(MatchError(op.ErrInvalidArgument))
					Expect(op.AsError(err).Message).To(Equal("failed to restore object"))
				})
				It("takes the bucket rule's storage class when the object has no storage class attr", func() {
					st := tiered(meta.TierTypeCloudS3, "", nil)
					delete(st.Attrs, meta.AttrStorageClass)
					_, err := serve(st, nil)
					Expect(err).To(MatchError(op.ErrNoSuchKey), "the bucket rule names no storage class, and no tier is keyed by none")
				})
				It("serves an object whose manifest names no cloud tier, and checks nothing on HEAD", func() {
					_, err := serve(tiered("", "CLOUDTIER", nil), nil)
					Expect(err).NotTo(HaveOccurred())
					_, err = serve(tiered(meta.TierTypeCloudS3, "CLOUDTIER", nil), func(o *op.GetObject) { o.GetData = false })
					Expect(err).NotTo(HaveOccurred())
				})
			})
		})

		It("refuses Swift large objects with NotImplemented", func() {
			for _, a := range []string{meta.AttrUserManifest, meta.AttrSLOManifest} {
				_, err := serve(state(func(st *op.ObjectState) { st.Attrs[a] = []byte("x") }), nil)
				Expect(err).To(MatchError(op.ErrNotImplemented), a)
			}
		})
		It("?torrent: NoSuchKey without the attr, NotImplemented with it, InvalidArgument for SSE-C", func() {
			_, err := serve(state(nil), func(o *op.GetObject) { o.Torrent = true })
			Expect(err).To(MatchError(op.ErrNoSuchKey))
			_, err = serve(state(func(st *op.ObjectState) { st.Attrs[meta.AttrTorrent] = []byte("d") }), func(o *op.GetObject) { o.Torrent = true })
			Expect(err).To(MatchError(op.ErrNotImplemented))
			_, err = serve(state(func(st *op.ObjectState) { st.Attrs[meta.AttrCryptMode] = []byte("SSE-C-AES256") }), func(o *op.GetObject) { o.Torrent = true })
			Expect(err).To(MatchError(op.ErrInvalidArgument))
		})

		Describe("SSE", func() {
			key := func() string { return base64.StdEncoding.EncodeToString(sseCKey()) }
			keyMD5 := func() string { return base64.StdEncoding.EncodeToString(sseCKeyMD5()) }
			encrypted := func(mode string) *op.ObjectState {
				return state(func(st *op.ObjectState) {
					st.Attrs[meta.AttrCryptMode] = []byte(mode)
					st.Attrs[meta.AttrCryptKeyMD5] = sseCKeyMD5()
					st.Attrs[meta.AttrCryptKeyID] = []byte("kid")
				})
			}
			const (
				badAlgorithm = "Requests specifying Server Side Encryption with Customer provided keys must provide a valid encryption algorithm."
				badKey       = "Requests specifying Server Side Encryption with Customer provided keys must provide an appropriate secret key."
				badKeyMD5    = "Requests specifying Server Side Encryption with Customer provided keys must provide an appropriate secret key md5."
				mismatch     = "The calculated MD5 hash of the key did not match the hash that was provided."
			)
			sseC := func(k, m string) func(o *op.GetObject) {
				return func(o *op.GetObject) { o.SSECAlgorithm, o.SSECKey, o.SSECKeyMD5 = "AES256", k, m }
			}
			DescribeTable("answers as rgw_s3_prepare_decrypt does",
				func(mode string, mut func(o *op.GetObject), want error, msg string) {
					_, err := serve(encrypted(mode), func(o *op.GetObject) {
						o.Secure = true
						if mut != nil {
							mut(o)
						}
					})
					Expect(err).To(MatchError(want))
					Expect(op.AsError(err).Message).To(Equal(msg))
				},
				Entry("an x-amz-server-side-encryption header on GET", "SSE-C-AES256", func(o *op.GetObject) { o.SSEHeader = true }, op.ErrInvalidRequest, ""),
				Entry("an x-amz-server-side-encryption header on an unencrypted object", "", func(o *op.GetObject) { o.SSEHeader = true }, op.ErrInvalidRequest, ""),
				Entry("SSE-C without the algorithm", "SSE-C-AES256", nil, op.ErrInvalidArgument, badAlgorithm),
				Entry("SSE-C with the wrong algorithm", "SSE-C-AES256", func(o *op.GetObject) { o.SSECAlgorithm = "AES128" }, op.ErrInvalidEncryptionAlgorithm, "The requested encryption algorithm is not valid, must be AES256."),
				Entry("SSE-C without a key", "SSE-C-AES256", sseC("", keyMD5()), op.ErrInvalidArgument, badKey),
				Entry("SSE-C with a short key", "SSE-C-AES256", sseC("c2hvcnQ=", keyMD5()), op.ErrInvalidArgument, badKey),
				Entry("SSE-C with a key that is not base64", "SSE-C-AES256", sseC("!"+key()[1:], keyMD5()), op.ErrInvalidArgument, badKey),
				Entry("SSE-C with a key ending in whitespace, which boost refuses", "SSE-C-AES256", sseC(key()[:43]+" ", keyMD5()), op.ErrInvalidArgument, badKey),
				Entry("SSE-C with an = inside the key, which boost reads as a zero", "SSE-C-AES256", sseC(key()[:10]+"="+key()[11:], keyMD5()), op.ErrInvalidArgument, mismatch),
				Entry("SSE-C with a bad md5", "SSE-C-AES256", sseC(key(), "!!"), op.ErrInvalidArgument, badKeyMD5),
				Entry("SSE-C with an md5 of 17 bytes", "SSE-C-AES256", sseC(key(), base64.StdEncoding.EncodeToString(make([]byte, 17))), op.ErrInvalidArgument, badKeyMD5),
				Entry("SSE-C with a mismatching md5", "SSE-C-AES256", sseC(key(), base64.StdEncoding.EncodeToString(make([]byte, 16))), op.ErrInvalidArgument, mismatch),
				Entry("SSE-C with valid headers: decryption is not implemented", "SSE-C-AES256", sseC(key(), keyMD5()), op.ErrNotImplemented, ""),
				Entry("SSE-C with a key boost decodes: unpadded, with whitespace inside", "SSE-C-AES256", sseC(key()[:20]+" \t"+key()[20:43], keyMD5()), op.ErrNotImplemented, ""),
				Entry("SSE-C with an = for an A, extra padding", "SSE-C-AES256", sseC("="+key()[1:]+"==", keyMD5()), op.ErrNotImplemented, ""),
				Entry("SSE-KMS: no key server", "SSE-KMS", nil, op.ErrInvalidArgument, "Failed to retrieve the actual key, kms-keyid: kid"),
				Entry("SSE-S3: no vault", "AES256", nil, op.ErrInvalidArgument, "Failed to retrieve the actual key"),
				Entry("RGW-AUTO: no default key, radosgw's -EIO", "RGW-AUTO", nil, op.ErrUnknown, ""),
			)
			It("checks the md5 against the stored one as well as the key's", func() {
				st := encrypted("SSE-C-AES256")
				st.Attrs[meta.AttrCryptKeyMD5] = make([]byte, 16)
				_, err := serve(st, func(o *op.GetObject) { o.Secure = true; sseC(key(), keyMD5())(o) })
				Expect(op.AsError(err).Message).To(Equal(mismatch))
			})
			It("refuses SSE-C, SSE-KMS and SSE-S3 over plain HTTP on Squid when rgw_crypt_require_ssl is set", func() {
				for _, mode := range []string{"SSE-C-AES256", "SSE-KMS", "AES256"} {
					_, err := serve(encrypted(mode), func(o *op.GetObject) { o.Secure = false })
					Expect(err).To(MatchError(op.ErrInvalidRequest), mode)
				}
			})
			It("takes SSE-S3 over plain HTTP on Tentacle, which checks no transport for it", func() {
				env.Zone = tentacle()
				_, err := serve(encrypted("AES256"), func(o *op.GetObject) { o.Secure = false })
				Expect(op.AsError(err).Message).To(Equal("Failed to retrieve the actual key"))
				_, err = serve(encrypted("SSE-KMS"), func(o *op.GetObject) { o.Secure = false })
				Expect(err).To(MatchError(op.ErrInvalidRequest))
			})
			It("takes plain HTTP when rgw_crypt_require_ssl is unset", func() {
				env.Conf = cephconf.NewOptions(cephconf.MapGetter{"rgw_ignore_get_invalid_range": "false", "rgw_crypt_require_ssl": "false"})
				_, err := serve(encrypted("SSE-KMS"), func(o *op.GetObject) { o.Secure = false })
				Expect(op.AsError(err).Message).To(Equal("Failed to retrieve the actual key, kms-keyid: kid"))
			})
			It("HEAD runs the same checks", func() {
				_, err := serve(encrypted("SSE-KMS"), func(o *op.GetObject) { o.GetData, o.Secure = false, true })
				Expect(err).To(MatchError(op.ErrInvalidArgument))
			})
			It("checks the range before the encryption", func() {
				_, err := serve(encrypted("SSE-KMS"), func(o *op.GetObject) { o.Secure, o.Range = true, "bytes=20000000-" })
				Expect(err).To(MatchError(op.ErrInvalidRange))
			})
		})

		Describe("partNumber", func() {
			const partKey = "multipart.bin.2~vXcCS0UgSATwQOecOwRkZrWyEyux7jO.2"
			multipart := func() *op.ObjectState {
				m := decodeGoldenManifest("squid-multipart")
				return state(func(st *op.ObjectState) {
					st.Manifest = &m
					st.Size = m.ObjSize
					st.ETag = "mp-3"
					st.Attrs[meta.AttrETag] = []byte("mp-3")
					st.Mtime = time.Unix(1700000200, 0)
					st.Attrs[meta.AttrCryptKeyID] = []byte("k")
					st.Attrs[meta.AttrCryptKeyMD5] = []byte("head-md5")
				})
			}
			// partHead is a part head as radosgw writes it, with no manifest
			// of its own; withManifest gives it one.
			partHead := func(exists, withManifest bool) *op.ObjectState {
				st := &op.ObjectState{
					Exists: exists, Size: 4 << 20, ETag: "part2", Key: meta.ObjKey{Name: partKey, NS: meta.NSMultipart},
					Attrs: map[string][]byte{meta.AttrETag: []byte("part2"), meta.AttrCryptKeyMD5: []byte("part-md5")}, Mtime: time.Unix(1700000100, 0),
				}
				if withManifest {
					pm := meta.NewManifest()
					pm.ObjSize = 8 << 20
					st.Manifest, st.Size = &pm, 8<<20
				}
				return st
			}
			part := func(n int) func(o *op.GetObject) { return func(o *op.GetObject) { o.PartNumber = &n } }
			// serveParts runs a GET on a fresh fake store whose first read
			// returns head and second partSt.
			serveParts := func(head, partSt *op.ObjectState, mut func(o *op.GetObject)) (*op.GetObject, error) {
				objects = &opfakes.FakeObjectStore{}
				env.Objects = objects
				objects.PrefetchObjectReturnsOnCall(0, head, nil)
				objects.PrefetchObjectReturnsOnCall(1, partSt, nil)
				objects.StatObjectReturnsOnCall(0, head, nil)
				objects.StatObjectReturnsOnCall(1, partSt, nil)
				o, _, err := get("k", mut)
				return o, err
			}

			It("reads the multipart head, then the part head, both with the request's prefetch", func() {
				_, err := serveParts(multipart(), partHead(true, true), part(2))
				Expect(err).NotTo(HaveOccurred())
				Expect(objects.StatObjectCallCount()).To(BeZero())
				Expect(objects.PrefetchObjectCallCount()).To(Equal(2))
				_, _, key := objects.PrefetchObjectArgsForCall(0)
				Expect(key).To(Equal(meta.ObjKey{Name: "k"}))
				_, _, key = objects.PrefetchObjectArgsForCall(1)
				Expect(key).To(Equal(meta.ObjKey{Name: partKey, NS: meta.NSMultipart}))
			})
			It("reads both heads without a prefetch on HEAD", func() {
				_, err := serveParts(multipart(), partHead(true, true), func(o *op.GetObject) { part(2)(o); o.GetData = false })
				Expect(err).NotTo(HaveOccurred())
				Expect(objects.StatObjectCallCount()).To(Equal(2))
				Expect(objects.PrefetchObjectCallCount()).To(BeZero())
			})
			It("serves a part head with its own manifest: the part's attrs, the count, the head's crypt attrs where it has none", func() {
				o, err := serveParts(multipart(), partHead(true, true), part(2))
				Expect(err).NotTo(HaveOccurred())
				Expect(*o.PartsCount).To(Equal(3))
				Expect(o.State.ETag).To(Equal("part2"))
				Expect(o.CondState.ETag).To(Equal("mp-3"))
				Expect(o.State.Attrs).To(HaveKeyWithValue(meta.AttrCryptKeyID, []byte("k")))
				Expect(o.State.Attrs).To(HaveKeyWithValue(meta.AttrCryptKeyMD5, []byte("part-md5")), "a crypt attr the part has is kept")
				Expect(o.Length).To(BeEquivalentTo(8 << 20))
				_, st, rng, _ := objects.ReadObjectArgsForCall(0)
				Expect(st).To(BeIdenticalTo(o.State))
				Expect(rng).To(Equal(op.ByteRange{Offset: 0, Length: 8 << 20}))
			})
			It("serves a part head without a manifest through the manifest get_part_obj_state builds for it", func() {
				o, err := serveParts(multipart(), partHead(true, false), part(2))
				Expect(err).NotTo(HaveOccurred())
				Expect(o.State.ETag).To(Equal("part2"))
				Expect(o.State.Size).To(BeEquivalentTo(8<<20), "get_part_obj_state sizes the part from the manifest")
				Expect(o.ObjSize).To(BeEquivalentTo(8 << 20))
				Expect([]uint64{o.Offset, o.Length}).To(Equal([]uint64{0, 8 << 20}))
				_, st, rng, _ := objects.ReadObjectArgsForCall(0)
				Expect(st).To(BeIdenticalTo(o.State))
				Expect(rng).To(Equal(op.ByteRange{Offset: 0, Length: 8 << 20}))
				m := st.Manifest
				Expect(m.Rules).To(Equal(map[uint64]meta.ManifestRule{0: {StartPartNum: 2, StripeMaxSize: 4 << 20}}))
				Expect([]uint64{m.ObjSize, m.HeadSize, m.MaxHeadSize}).To(Equal([]uint64{8 << 20, 0, 0}))
				Expect(m.Obj.Key).To(Equal(meta.ObjKey{Name: partKey, NS: meta.NSMultipart}))
				stripes, err := m.Stripes()
				Expect(err).NotTo(HaveOccurred())
				whole, err := o.CondState.Manifest.Stripes()
				Expect(err).NotTo(HaveOccurred())
				var want []string
				for _, w := range whole {
					if w.Ofs >= 8<<20 && w.Ofs < 16<<20 {
						want = append(want, w.OID())
					}
				}
				got := make([]string, 0, len(stripes))
				for _, g := range stripes {
					got = append(got, g.OID())
				}
				Expect(got).To(Equal(want), "the multipart object's stripes of part 2")
				Expect(stripes[0].InHead).To(BeTrue(), "the part head is the part's first stripe")
			})
			It("reads a part's first chunk from its prefetch, as radosgw does", func() {
				p := partHead(true, false)
				p.Head = make([]byte, 4<<20)
				o, err := serveParts(multipart(), p, part(2))
				Expect(err).NotTo(HaveOccurred())
				_, rec, key := objects.PrefetchObjectArgsForCall(1)
				_, st, rng, _ := objects.ReadObjectArgsForCall(0)
				Expect(st.Head).To(HaveLen(4<<20), "the part head's prefetched bytes go to ReadObject")
				partReads := dataReads(st, rng, meta.Obj{Bucket: rec.Info.Bucket, Key: key})
				Expect(objects.PrefetchObjectCallCount()+partReads).To(Equal(3),
					"the multipart head, the part head with its first 4 MiB, and the part's second stripe")
				Expect(dataReads(o.CondState, op.ByteRange{Offset: 8 << 20, Length: 8 << 20}, meta.Obj{Bucket: rec.Info.Bucket, Key: meta.ObjKey{Name: "k"}})).
					To(Equal(2), "the multipart head holds none of the part, so reading through it reads the first chunk again")
			})
			It("applies a Range within the part", func() {
				o, err := serveParts(multipart(), partHead(true, false), func(o *op.GetObject) { part(2)(o); o.Range = "bytes=10-19" })
				Expect(err).NotTo(HaveOccurred())
				Expect(o.Status).To(Equal(http.StatusPartialContent))
				Expect([]uint64{o.Offset, o.Length}).To(Equal([]uint64{10, 10}))
				_, st, rng, _ := objects.ReadObjectArgsForCall(0)
				Expect(st).To(BeIdenticalTo(o.State))
				Expect(rng).To(Equal(op.ByteRange{Offset: 10, Length: 10}))
			})
			It("reads a compressed part with its own compression info, from its start", func() {
				head := multipart()
				head.Compression = &meta.CompressionInfo{Type: "zlib", OrigSize: 30 << 20, Blocks: []meta.CompressionBlock{
					{OldOfs: 0, NewOfs: 0, Len: 8 << 20}, {OldOfs: 12 << 20, NewOfs: 8 << 20, Len: 8 << 20}, {OldOfs: 24 << 20, NewOfs: 16 << 20, Len: 4 << 20},
				}}
				p := partHead(true, false)
				p.Compression = &meta.CompressionInfo{Type: "zlib", OrigSize: 12 << 20, Blocks: []meta.CompressionBlock{{Len: 8 << 20}}}
				o, err := serveParts(head, p, func(o *op.GetObject) { part(2)(o); o.Range = "bytes=100-" })
				Expect(err).NotTo(HaveOccurred())
				Expect(o.ObjSize).To(BeEquivalentTo(12 << 20))
				_, st, rng, _ := objects.ReadObjectArgsForCall(0)
				Expect(st.Compression).To(BeIdenticalTo(p.Compression))
				Expect(rng).To(Equal(op.ByteRange{Offset: 100, Length: 12<<20 - 100}))
			})
			It("compares If-Match with the multipart head's etag and dates with the part's mtime", func() {
				_, err := serveParts(multipart(), partHead(true, false), func(o *op.GetObject) { part(2)(o); o.IfMatch = "mp-3" })
				Expect(err).NotTo(HaveOccurred())
				_, err = serveParts(multipart(), partHead(true, false), func(o *op.GetObject) { part(2)(o); o.IfMatch = "part2" })
				Expect(err).To(MatchError(op.ErrPreconditionFailed))
				o, err := serveParts(multipart(), partHead(true, false), func(o *op.GetObject) {
					part(2)(o)
					o.IfModifiedSince = time.Unix(1700000150, 0).UTC().Format(http.TimeFormat)
				})
				Expect(err).To(MatchError(op.ErrNotModified), "the part's mtime is before the date, the head's after")
				Expect(o.State.ETag).To(Equal("part2"), "a 304 renders the part's headers")
			})
			It("reads the part head through the manifest's bucket", func(ctx SpecContext) {
				rec, err := store.GetBucket(ctx, "", "plain")
				Expect(err).NotTo(HaveOccurred())
				_, err = serveParts(multipart(), partHead(true, false), part(2))
				Expect(err).NotTo(HaveOccurred())
				_, got, _ := objects.PrefetchObjectArgsForCall(1)
				Expect(got.Info.Bucket.ID).To(Equal("e7bceed5-d2a6-4b0b-b484-cac20e0beb53.4156.1"), "a copy's parts live in its source bucket")
				Expect(got.Info.PlacementRule).To(Equal(rec.Info.PlacementRule), "under the request bucket's placement")

				head := multipart()
				head.Manifest.TailPlacement.Bucket = rec.Info.Bucket
				_, err = serveParts(head, partHead(true, false), part(2))
				Expect(err).NotTo(HaveOccurred())
				_, first, _ := objects.PrefetchObjectArgsForCall(0)
				_, second, _ := objects.PrefetchObjectArgsForCall(1)
				Expect(second).To(BeIdenticalTo(first), "the request's bucket record when the part is in it")
			})
			It("answers InvalidPart past the count, for part 2 of a single-part object, and for a missing or unreadable part head", func() {
				_, err := serveParts(multipart(), partHead(true, false), part(5))
				Expect(err).To(MatchError(op.ErrInvalidPart))
				_, err = serveParts(state(nil), nil, part(2))
				Expect(err).To(MatchError(op.ErrInvalidPart))
				_, err = serveParts(multipart(), partHead(false, false), part(2))
				Expect(err).To(MatchError(op.ErrInvalidPart))
				objects = &opfakes.FakeObjectStore{}
				env.Objects = objects
				objects.PrefetchObjectReturnsOnCall(0, multipart(), nil)
				objects.PrefetchObjectReturnsOnCall(1, nil, op.ErrServiceUnavailable)
				_, _, err = get("k", part(2))
				Expect(err).To(MatchError(op.ErrInvalidPart))
				Expect(errors.Is(err, op.ErrServiceUnavailable)).To(BeFalse(), "radosgw answers InvalidPart whatever the read failed with")
			})
			It("answers part 1 of a single-part object with the whole object and no parts count", func() {
				o, err := serveParts(state(nil), nil, part(1))
				Expect(err).NotTo(HaveOccurred())
				Expect(o.PartsCount).To(BeNil())
				Expect(o.Length).To(BeEquivalentTo(10 << 20))
				Expect(objects.PrefetchObjectCallCount()).To(Equal(1))
			})
			It("answers NoSuchKey for a missing object before it looks for the part", func() {
				_, err := serveParts(state(func(st *op.ObjectState) { st.Exists = false }), nil, part(2))
				Expect(err).To(MatchError(op.ErrNoSuchKey))
			})
			It("parses the Range before it reads the part head", func() {
				_, err := serveParts(multipart(), partHead(true, false), func(o *op.GetObject) { part(9)(o); o.Range = "bytes=5" })
				Expect(err).To(MatchError(op.ErrInvalidRange))
				Expect(objects.PrefetchObjectCallCount()).To(BeZero(), "a Range header turns the prefetch off")
				Expect(objects.StatObjectCallCount()).To(Equal(1), "the multipart head alone")
			})
			It("reports the parts count on a plain GET of a multipart object on Tentacle, not on Squid", func() {
				objects.PrefetchObjectReturns(multipart(), nil)
				o, _, err := get("k", nil)
				Expect(err).NotTo(HaveOccurred())
				Expect(o.PartsCount).To(BeNil())
				env.Zone = tentacle()
				o, _, err = get("k", nil)
				Expect(err).NotTo(HaveOccurred())
				Expect(*o.PartsCount).To(Equal(3))
			})
		})

		Describe("response-* overrides", func() {
			It("refuses them from an anonymous request", func() {
				env.Authz = &opfakes.FakeAuthorizer{}
				objects.PrefetchObjectReturns(state(nil), nil)
				o := &op.GetObject{GetData: true, Sink: sink, ResponseOverrides: map[string]string{"Content-Type": "x"}}
				r := req("k")
				r.Identity = op.Anonymous()
				Expect(op.Run(context.Background(), o, r)).To(MatchError(op.ErrInvalidRequest))
			})
			DescribeTable("refuse a value with a byte C's iscntrl takes",
				func(value string, want error) {
					_, err := serve(state(nil), func(o *op.GetObject) { o.ResponseOverrides = map[string]string{"Content-Type": value} })
					if want == nil {
						Expect(err).NotTo(HaveOccurred())
					} else {
						Expect(err).To(MatchError(want))
					}
				},
				Entry("CR LF", "a\r\nb", op.ErrInvalidRequest),
				Entry("NUL", "a\x00b", op.ErrInvalidRequest),
				Entry("DEL", "a\x7fb", op.ErrInvalidRequest),
				Entry("a C1 control, which is above 0x7f and a signed char iscntrl does not take", "a\u0085b", nil),
				Entry("an empty value", "", nil),
			)
		})

		It("returns a read error that happens before the first byte, and the sink saw no header", func() {
			objects.PrefetchObjectReturns(state(nil), nil)
			objects.ReadObjectReturns(op.ErrNoSuchKey)
			_, _, err := get("k", nil)
			Expect(err).To(MatchError(op.ErrNoSuchKey))
			Expect(sink.status).To(BeZero())
			Expect(sink.flushed).To(BeFalse())
		})

		Describe("Tentacle's denial message", func() {
			var authz *opfakes.FakeAuthorizer
			BeforeEach(func() {
				authz = &opfakes.FakeAuthorizer{}
				authz.VerifyObjectReturns(op.ErrAccessDenied)
				env.Authz = authz
				objects.PrefetchObjectReturns(state(nil), nil)
			})
			It("names the missing permission on Tentacle", func() {
				env.Zone = tentacle()
				_, _, err := get("k", func(o *op.GetObject) { o.Torrent = true })
				Expect(err).To(MatchError(op.ErrAccessDenied))
				Expect(op.AsError(err).Message).To(Equal("missing s3:GetObjectTorrent permission"))
			})
			It("says nothing on Squid", func() {
				_, _, err := get("k", nil)
				Expect(err).To(MatchError(op.ErrAccessDenied))
				Expect(op.AsError(err).Message).To(BeEmpty())
			})
			It("says nothing for a missing object, which radosgw refuses before verify_permission", func() {
				env.Zone = tentacle()
				objects.PrefetchObjectReturns(state(func(st *op.ObjectState) { st.Exists = false }), nil)
				_, _, err := get("k", nil)
				Expect(op.AsError(err).Message).To(BeEmpty())
			})
			It("leaves a refusal made before verify_permission alone", func() {
				env.Zone = tentacle()
				authz.VerifyObjectReturns(op.BeforeVerify(op.ErrAccessDenied))
				_, _, err := get("k", nil)
				Expect(op.IsBeforeVerify(err)).To(BeTrue())
				Expect(op.AsError(err).Message).To(BeEmpty())
			})
		})
	})
})
