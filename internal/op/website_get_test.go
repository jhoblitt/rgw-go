package op_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cephconf"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
	"github.com/jhoblitt/rgw-go/internal/policy"
	"github.com/jhoblitt/rgw-go/internal/website"
)

// statLog records the keys StatObject is asked for, and fails each with err
// when it is set.
type statLog struct {
	op.ObjectStore
	keys []string
	err  error
}

func (s *statLog) StatObject(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey) (*op.ObjectState, error) {
	s.keys = append(s.keys, key.Name)
	if s.err != nil {
		return nil, s.err
	}
	return s.ObjectStore.StatObject(ctx, rec, key)
}

// captureDebugLogs is captureLogs at debug level.
func captureDebugLogs() *bytes.Buffer {
	var buf bytes.Buffer
	oldLogger, oldWriter, oldFlags := slog.Default(), log.Writer(), log.Flags()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	DeferCleanup(func() {
		slog.SetDefault(oldLogger)
		log.SetOutput(oldWriter)
		log.SetFlags(oldFlags)
	})
	return &buf
}

var _ = Describe("GetObject's website retarget", func() {
	const noIndexSuffix = "The IndexDocument Suffix is not configurated or not well formed!"
	var (
		store *memstore.Store
		env   *op.Env
		alice op.Identity
		bob   op.Identity
		rec   *op.BucketRecord
		stats *statLog
	)
	body := func(key string) string { return "body of " + key }
	put := func(ctx context.Context, key string, attrs map[string][]byte) {
		GinkgoHelper()
		b := []byte(body(key))
		_, err := store.PutObject(ctx, rec, meta.ObjKey{Name: key}, bytes.NewReader(b), op.PutParams{Size: int64(len(b)), Attrs: attrs})
		Expect(err).NotTo(HaveOccurred())
	}
	setWebsite := func(ctx context.Context, conf *meta.WebsiteConf) {
		GinkgoHelper()
		r, err := store.GetBucket(ctx, "", "site")
		Expect(err).NotTo(HaveOccurred())
		r.Info.Website = conf
		Expect(store.PutBucketInfo(ctx, r)).To(Succeed())
	}
	// rule is a routing rule for keys under prefix, to example.com with code
	// and the prefix replaced by "new/".
	rule := func(prefix string, code uint16) meta.RoutingRule {
		return meta.RoutingRule{
			Condition:    meta.RoutingCondition{KeyPrefixEquals: prefix},
			RedirectInfo: meta.RoutingRedirect{Redirect: meta.RedirectInfo{Hostname: "example.com", HTTPRedirectCode: code}, ReplaceKeyPrefixWith: "new/"},
		}
	}
	withRules := func(rules ...meta.RoutingRule) *meta.WebsiteConf {
		return &meta.WebsiteConf{IndexDocSuffix: "index.html", RoutingRules: rules}
	}
	BeforeEach(func(ctx SpecContext) {
		store = memstore.New(memstore.Config{Release: denc.Squid, Now: smallMtime})
		stats = &statLog{ObjectStore: store}
		env = &op.Env{
			Zone: store, Users: store, Buckets: store, Objects: stats, Usage: store,
			Authz: op.OwnerOnly{}, Metrics: op.NopMetrics{},
			Conf: cephconf.NewOptions(cephconf.MapGetter{"rgw_ignore_get_invalid_range": "false", "rgw_crypt_require_ssl": "true"}),
		}
		a := store.AddUser(meta.UserInfo{UserID: meta.UserID{ID: "alice"}, OpMask: op.OpTypeAll})
		alice = op.Identity{User: &a.Info, Owner: meta.UserOwner(a.Info.UserID), OpMask: op.OpTypeAll}
		b := store.AddUser(meta.UserInfo{UserID: meta.UserID{ID: "bob"}, OpMask: op.OpTypeAll})
		bob = op.Identity{User: &b.Info, Owner: meta.UserOwner(b.Info.UserID), OpMask: op.OpTypeAll}
		var err error
		rec, err = store.CreateBucket(ctx, op.CreateBucketParams{Name: "site", Owner: alice.Owner, Placement: meta.PlacementRule{Name: "default-placement"}})
		Expect(err).NotTo(HaveOccurred())
		for _, key := range []string{"index.html", "page.html", "dir/index.html", "docs", "docs/index.html", "dir2/", "dir2/index.html", "100%", "100%/index.html"} {
			put(ctx, key, nil)
		}
		setWebsite(ctx, &meta.WebsiteConf{IndexDocSuffix: "index.html", ErrorDoc: "error.html"})
	})
	webReq := func(key string) *op.Request {
		return &op.Request{
			Method: http.MethodGet, Bucket: "site", Object: meta.ObjKey{Name: key}, Website: true,
			HTTPHost: new("site.web.example.com"), Identity: alice, Env: env, Header: http.Header{}, Query: url.Values{},
		}
	}
	run := func(ctx context.Context, r *op.Request, mut func(o *op.GetObject)) (*op.GetObject, *captureSink, error) {
		sink := &captureSink{}
		o := &op.GetObject{GetData: true, Sink: sink, Retarget: &op.WebsiteRetarget{}}
		if mut != nil {
			mut(o)
		}
		return o, sink, op.Run(ctx, o, r)
	}
	redirect := func(err error) *op.WebsiteRedirectError {
		re, _ := errors.AsType[*op.WebsiteRedirectError](err)
		return re
	}

	DescribeTable("reads the object get_effective_key names, rgw_website.cc:116-133 at v19.2.7 and v20.2.4",
		func(ctx SpecContext, key, want string) {
			r := webReq(key)
			_, sink, err := run(ctx, r, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(r.Object).To(Equal(meta.ObjKey{Name: want}))
			Expect(sink.body.String()).To(Equal(body(want)))
		},
		Entry("the bucket's root reads the suffix", "", "index.html"),
		Entry("a key ending in / reads the key and the suffix", "dir/", "dir/index.html"),
		Entry("a key that names no object reads the key, /, and the suffix", "dir", "dir/index.html"),
		Entry("a key that names an object reads it", "page.html", "page.html"),
	)
	DescribeTable("takes a directory object for no file, and reads the index under a key ending in / whatever object it names",
		func(ctx SpecContext, key, want string) {
			_, sink, err := run(ctx, webReq(key), nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(sink.body.String()).To(Equal(body(want)))
		},
		Entry("dir2/ exists and dir2 does not, so dir2 is no file", "dir2", "dir2/index.html"),
		Entry("docs exists and docs/ reads the index", "docs/", "docs/index.html"),
		Entry("docs exists and docs reads it", "docs", "docs"),
	)
	It("stats the key as the request names it, and only where get_effective_key reads the answer", func(ctx SpecContext) {
		for key, want := range map[string][]string{"page.html": {"page.html"}, "dir": {"dir"}, "": nil, "dir/": nil} {
			stats.keys = nil
			_, _, err := run(ctx, webReq(key), nil)
			Expect(err).NotTo(HaveOccurred(), "%q", key)
			Expect(stats.keys).To(Equal(want), "%q", key)
		}
		stats.keys = nil
		setWebsite(ctx, &meta.WebsiteConf{RedirectAll: meta.RedirectInfo{Hostname: "example.org"}})
		_, _, err := run(ctx, webReq("page.html"), nil)
		Expect(err).To(MatchError(op.ErrInvalidArgument))
		Expect(stats.keys).To(BeEmpty(), "no suffix: get_effective_key fails before it reads is_file")
	})
	It("stats a key holding % as it is, where radosgw's web_dir decodes it a second time", func(ctx SpecContext) {
		r := webReq("100%")
		_, sink, err := run(ctx, r, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(stats.keys).To(Equal([]string{"100%"}))
		Expect(r.Object.Name).To(Equal("100%"), "radosgw stats url_decode(\"100%\"), \"100\", and reads 100%/index.html")
		Expect(sink.body.String()).To(Equal(body("100%")))
	})
	It("takes a stat that fails for no file, as web_dir does", func(ctx SpecContext) {
		stats.err = errors.New("the OSD went away")
		r := webReq("page.html")
		_, _, err := run(ctx, r, nil)
		Expect(err).To(MatchError(op.ErrNoSuchKey))
		Expect(r.Object.Name).To(Equal("page.html/index.html"))
	})
	It("logs a failed stat with the bucket, the key and the error", func(ctx SpecContext) {
		logs := captureDebugLogs()
		stats.err = errors.New("the OSD went away")
		_, _, err := run(ctx, webReq("page.html"), nil)
		Expect(err).To(MatchError(op.ErrNoSuchKey))
		var records []map[string]any
		for line := range strings.SplitSeq(strings.TrimSpace(logs.String()), "\n") {
			var rec map[string]any
			Expect(json.Unmarshal([]byte(line), &rec)).To(Succeed(), "%q", line)
			records = append(records, rec)
		}
		Expect(records).To(ContainElement(SatisfyAll(
			HaveKeyWithValue("msg", "website file test failed, taking the key for no file"),
			HaveKeyWithValue("bucket", "site"),
			HaveKeyWithValue("key", "page.html"),
			HaveKeyWithValue("error", "the OSD went away"),
		)))
	})

	Describe("its refusals, before any object is read", func() {
		var objects *opfakes.FakeObjectStore
		BeforeEach(func() {
			objects = &opfakes.FakeObjectStore{}
			env.Objects = objects
		})
		AfterEach(func() {
			Expect(objects.StatObjectCallCount()).To(BeZero())
			Expect(objects.PrefetchObjectCallCount()).To(BeZero())
		})
		It("answers NoSuchBucket for a request that names no bucket, which init_permissions does not load", func(ctx SpecContext) {
			buckets := &opfakes.FakeBucketStore{}
			env.Buckets = buckets
			r := webReq("")
			r.Bucket = ""
			_, _, err := run(ctx, r, nil)
			Expect(err).To(MatchError(op.ErrNoSuchBucket))
			Expect(buckets.GetBucketCallCount()).To(BeZero(), "the OSD refuses the empty oid an entry point read names, PrimaryLogPG.cc:2067-2071 at v19.2.7")
		})
		It("answers NoSuchBucket for a missing bucket", func(ctx SpecContext) {
			r := webReq("page.html")
			r.Bucket = "nope"
			_, _, err := run(ctx, r, nil)
			Expect(err).To(MatchError(op.ErrNoSuchBucket))
		})
		It("answers NoSuchWebsiteConfiguration for a bucket without a website", func(ctx SpecContext) {
			setWebsite(ctx, nil)
			_, _, err := run(ctx, webReq("page.html"), nil)
			Expect(err).To(MatchError(op.ErrNoSuchWebsiteConfiguration))
		})
		It("answers InvalidArgument with radosgw's message for a website without an index suffix", func(ctx SpecContext) {
			setWebsite(ctx, &meta.WebsiteConf{RedirectAll: meta.RedirectInfo{Hostname: "example.org"}})
			_, _, err := run(ctx, webReq("page.html"), nil)
			Expect(err).To(MatchError(op.ErrInvalidArgument))
			Expect(op.AsError(err).Message).To(Equal(noIndexSuffix), "rgw_rest_s3.cc:5409-5414 at v19.2.7")
		})
	})

	Describe("a redirect", func() {
		It("redirects every key under a redirect-all stored with a suffix, 301 whatever code it stores", func(ctx SpecContext) {
			setWebsite(ctx, &meta.WebsiteConf{IndexDocSuffix: "index.html", RedirectAll: meta.RedirectInfo{Protocol: "https", Hostname: "example.org", HTTPRedirectCode: 302}})
			r := webReq("a/b")
			_, sink, err := run(ctx, r, nil)
			Expect(err).To(MatchError(op.ErrWebsiteRedirect))
			Expect(redirect(err)).To(Equal(&op.WebsiteRedirectError{Code: http.StatusMovedPermanently}))
			Expect(r.Redirect).To(Equal("https://example.org/a/b"))
			Expect(sink.headers).To(BeZero())
		})
		DescribeTable("redirects by a routing rule naming no error code, with its code or 301",
			func(ctx SpecContext, code uint16, want int) {
				setWebsite(ctx, withRules(rule("old/", code)))
				r := webReq("old/page")
				_, sink, err := run(ctx, r, nil)
				Expect(err).To(MatchError(op.ErrWebsiteRedirect))
				Expect(redirect(err)).To(Equal(&op.WebsiteRedirectError{Code: want}))
				Expect(r.Redirect).To(Equal("http://example.com/new/page"))
				Expect(r.Object).To(Equal(meta.ObjKey{Name: "old/page"}), "retarget leaves s->object alone when it redirects")
				Expect(sink.headers).To(BeZero())
			},
			Entry("a rule without a code is 301", uint16(0), http.StatusMovedPermanently),
			Entry("a rule's code is its own", uint16(http.StatusFound), http.StatusFound),
		)
		It("passes over a rule naming an error code, which only the error handler matches", func(ctx SpecContext) {
			r404 := rule("", 0)
			r404.Condition.HTTPErrorCodeReturnedEquals = http.StatusNotFound
			setWebsite(ctx, withRules(r404))
			_, sink, err := run(ctx, webReq("page.html"), nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(sink.body.String()).To(Equal(body("page.html")))
		})
		It("matches the effective key and builds the Location from the request's key, rgw_rest_s3.cc:5419-5429 at v19.2.7", func(ctx SpecContext) {
			setWebsite(ctx, withRules(rule("dir/index", 0)))
			r := webReq("dir/")
			_, _, err := run(ctx, r, nil)
			Expect(err).To(MatchError(op.ErrWebsiteRedirect), "dir/index.html starts with dir/index, dir/ does not")
			Expect(r.Redirect).To(Equal("http://example.com/new/"), "apply_rule cuts the prefix's length from dir/, which is shorter")
		})
		It("names https for a secure request", func(ctx SpecContext) {
			setWebsite(ctx, withRules(rule("old/", 0)))
			r := webReq("old/page")
			_, _, err := run(ctx, r, func(o *op.GetObject) { o.Secure = true })
			Expect(err).To(MatchError(op.ErrWebsiteRedirect))
			Expect(r.Redirect).To(Equal("https://example.com/new/page"))
		})
		It("redirects a requester who may not read the bucket, as retarget runs before read_permissions", func(ctx SpecContext) {
			setWebsite(ctx, withRules(rule("old/", 0)))
			r := webReq("old/page")
			r.Identity = bob
			_, _, err := run(ctx, r, nil)
			Expect(err).To(MatchError(op.ErrWebsiteRedirect))
			r = webReq("page.html")
			r.Identity = bob
			_, _, err = run(ctx, r, nil)
			Expect(err).To(MatchError(op.ErrAccessDenied), "the object it serves is read as the requester may read it")
		})
		It("returns website.Apply's refusal of a host holding a control byte", func(ctx SpecContext) {
			bad := rule("old/", 0)
			bad.RedirectInfo.Redirect.Hostname = "example.com\r\nSet-Cookie: k=v"
			setWebsite(ctx, withRules(bad))
			r := webReq("old/page")
			_, _, err := run(ctx, r, nil)
			Expect(err).To(MatchError(website.ErrUnsafeRedirect))
			Expect(r.Redirect).To(BeEmpty())
		})

		Describe("the request's host", func() {
			hostless := func() meta.RoutingRule {
				r := rule("old/", 0)
				r.RedirectInfo.Redirect.Hostname = ""
				return r
			}
			It("is the Host header as sent, its case and port kept", func(ctx SpecContext) {
				setWebsite(ctx, withRules(hostless()))
				r := webReq("old/page")
				r.HTTPHost = new("Site.Web.Example.com:8080")
				_, _, err := run(ctx, r, nil)
				Expect(err).To(MatchError(op.ErrWebsiteRedirect))
				Expect(r.Redirect).To(Equal("http://Site.Web.Example.com:8080/new/page"))
			})
			DescribeTable("is refused 400 InvalidRequest for a request without one, where radosgw writes http:/// and the key",
				func(ctx SpecContext, host *string) {
					setWebsite(ctx, withRules(hostless()))
					r := webReq("old/page")
					r.HTTPHost = host
					_, sink, err := run(ctx, r, nil)
					Expect(err).To(MatchError(website.ErrUnsafeRedirect))
					Expect(op.AsError(err)).To(MatchError(op.ErrInvalidRequest), "the client left out its Host: radosgw's row for a malformed request, rgw_common.cc:62 and :136 at v19.2.7")
					Expect(r.Redirect).To(BeEmpty())
					Expect(sink.headers).To(BeZero())
				},
				Entry("no Host header", nil),
				Entry("an empty Host header", new("")),
			)
			It("is not needed when the rule names a host", func(ctx SpecContext) {
				setWebsite(ctx, withRules(rule("old/", 0)))
				r := webReq("old/page")
				r.HTTPHost = nil
				_, _, err := run(ctx, r, nil)
				Expect(err).To(MatchError(op.ErrWebsiteRedirect))
				Expect(r.Redirect).To(Equal("http://example.com/new/page"))
			})
		})
	})

	It("keeps the request's key for the website error handler once it reads another", func(ctx SpecContext) {
		r := webReq("nodir")
		o, _, err := run(ctx, r, nil)
		Expect(err).To(MatchError(op.ErrNoSuchKey))
		Expect(r.Object.Name).To(Equal("nodir/index.html"))
		Expect(o.Retarget.OriginalKey).To(Equal("nodir"), "radosgw's original_object_name, rgw_rest_s3.cc:5372-5385 at v19.2.7")
	})
	It("reads the effective key's current version as s3:GetObject, whatever version the request names", func(ctx SpecContext) {
		r := webReq("page.html")
		r.Object.Instance = "v1"
		o, sink, err := run(ctx, r, nil)
		Expect(err).NotTo(HaveOccurred(), "memstore has no instance v1")
		Expect(r.Object).To(Equal(meta.ObjKey{Name: "page.html"}))
		Expect(o.Versioned).To(BeFalse())
		Expect(o.VersionID).To(BeEmpty())
		Expect(o.Action()).To(Equal(policy.S3GetObject))
		Expect(sink.body.String()).To(Equal(body("page.html")))
	})
	It("does nothing for a request without the website flag", func(ctx SpecContext) {
		r := webReq("dir/")
		r.Website = false
		_, _, err := run(ctx, r, nil)
		Expect(err).To(MatchError(op.ErrNoSuchKey))
		Expect(r.Object.Name).To(Equal("dir/"))
		Expect(stats.keys).To(BeEmpty())
	})
	DescribeTable("WebsiteRedirect matches one key at a status and builds the Location from another",
		func(status int, ok bool) {
			r404 := rule("old/", 0)
			r404.Condition.HTTPErrorCodeReturnedEquals = http.StatusNotFound
			loc, code, matched, err := op.WebsiteRedirect(webReq("x"), *withRules(r404), "old/page", "old/other", status, false)
			Expect(err).NotTo(HaveOccurred())
			Expect(matched).To(Equal(ok))
			if ok {
				Expect([]any{loc, code}).To(Equal([]any{"http://example.com/new/other", http.StatusMovedPermanently}))
			}
		},
		Entry("the error handler's status matches the rule", http.StatusNotFound, true),
		Entry("retarget's 0 does not", 0, false),
	)

	Describe("an object's x-amz-website-redirect-location", func() {
		const loc = "/new.html"
		BeforeEach(func(ctx SpecContext) {
			put(ctx, "moved.html", map[string][]byte{meta.AttrWebsiteRedirect: []byte(loc + "\x00")})
		})
		objectRedirect := func() *op.WebsiteRedirectError {
			return &op.WebsiteRedirectError{Code: http.StatusMovedPermanently, Object: true}
		}
		It("answers a GET and a HEAD 301 to the attr up to its NUL, sending nothing", func(ctx SpecContext) {
			for _, getData := range []bool{true, false} {
				r := webReq("moved.html")
				_, sink, err := run(ctx, r, func(o *op.GetObject) { o.GetData = getData })
				Expect(err).To(MatchError(op.ErrWebsiteRedirect), "GetData %v", getData)
				Expect(redirect(err)).To(Equal(objectRedirect()), "GetData %v", getData)
				Expect(r.Redirect).To(Equal(loc), "GetData %v", getData)
				Expect([]int{sink.headers, sink.body.Len()}).To(Equal([]int{0, 0}), "GetData %v", getData)
			}
		})
		DescribeTable("answers one 301 for a read that passes its part check, sending nothing",
			func(ctx SpecContext, key string, mut func(o *op.GetObject)) {
				_, err := store.PutObject(ctx, rec, meta.ObjKey{Name: "empty.html"}, bytes.NewReader(nil),
					op.PutParams{Attrs: map[string][]byte{meta.AttrWebsiteRedirect: []byte(loc + "\x00")}})
				Expect(err).NotTo(HaveOccurred())
				r := webReq(key)
				_, sink, err := run(ctx, r, mut)
				Expect(redirect(err)).To(Equal(objectRedirect()))
				Expect(r.Redirect).To(Equal(loc))
				Expect([]int{sink.headers, sink.body.Len()}).To(Equal([]int{0, 0}))
			},
			Entry("an empty object", "empty.html", nil),
			Entry("a Range the object satisfies", "moved.html", func(o *op.GetObject) { o.Range = "bytes=0-3" }),
			Entry("partNumber 1 of an object that is not multipart, which reads it whole", "moved.html", func(o *op.GetObject) { o.PartNumber = new(1) }),
		)
		DescribeTable("answers it in place of a refusal made once the object's attrs are read, as send_response_data_error reaches the same override",
			func(ctx SpecContext, mut func(o *op.GetObject)) {
				r := webReq("moved.html")
				_, _, err := run(ctx, r, mut)
				Expect(redirect(err)).To(Equal(objectRedirect()))
				Expect(r.Redirect).To(Equal(loc))
			},
			Entry("an If-Match that fails", func(o *op.GetObject) { o.IfMatch = `"nope"` }),
			Entry("an If-None-Match that matches", func(o *op.GetObject) { o.IfNoneMatch = `"` + md5Hex([]byte(body("moved.html"))) + `"` }),
			Entry("a Range past the end", func(o *op.GetObject) { o.Range = "bytes=5000-6000" }),
		)
		DescribeTable("leaves a refusal made before the object's attrs are read alone",
			func(ctx SpecContext, mut func(o *op.GetObject), want error) {
				r := webReq("moved.html")
				_, _, err := run(ctx, r, mut)
				Expect(err).To(MatchError(want))
				Expect(r.Redirect).To(BeEmpty())
			},
			Entry("a date init_common refuses", func(o *op.GetObject) { o.IfModifiedSince = "yesterday" }, op.ErrInvalidArgument),
			Entry("a partNumber the object has not, which radosgw's prepare refuses before it copies the attrs, rgw_rados.cc:7076-7092 and :7118-7119 at v19.2.7",
				func(o *op.GetObject) { o.PartNumber = new(2) }, op.ErrInvalidPart),
		)
		It("redirects the error document's read, which runs no retarget", func(ctx SpecContext) {
			r := webReq("moved.html")
			_, _, err := run(ctx, r, func(o *op.GetObject) { o.Retarget = nil })
			Expect(redirect(err)).To(Equal(objectRedirect()))
		})
		It("serves the object to S3", func(ctx SpecContext) {
			r := webReq("moved.html")
			r.Website = false
			_, sink, err := run(ctx, r, func(o *op.GetObject) { o.Retarget = nil })
			Expect(err).NotTo(HaveOccurred())
			Expect(sink.body.String()).To(Equal(body("moved.html")))
			Expect(r.Redirect).To(BeEmpty())
		})
		DescribeTable("takes an attr radosgw's POST did not write as its bytes up to any NUL",
			func(ctx SpecContext, attr, want string) {
				put(ctx, "odd.html", map[string][]byte{meta.AttrWebsiteRedirect: []byte(attr)})
				r := webReq("odd.html")
				_, _, err := run(ctx, r, nil)
				Expect(redirect(err)).To(Equal(objectRedirect()))
				Expect(r.Redirect).To(Equal(want))
			},
			Entry("no NUL", "/x", "/x"),
			Entry("empty, where radosgw assigns c_str()'s null pointer", "", ""),
			Entry("a NUL inside", "/a\x00/b", "/a"),
		)

		Describe("with partNumber", func() {
			const partKey = "multipart.bin.2~vXcCS0UgSATwQOecOwRkZrWyEyux7jO.2"
			var objects *opfakes.FakeObjectStore
			BeforeEach(func() {
				m := decodeGoldenManifest("squid-multipart")
				head := &op.ObjectState{
					Exists: true, Size: m.ObjSize, Manifest: &m, Mtime: time.Unix(1700000200, 0), Key: meta.ObjKey{Name: "k"},
					Attrs: map[string][]byte{meta.AttrETag: []byte("mp-3"), meta.AttrWebsiteRedirect: []byte(loc + "\x00")},
				}
				pm := meta.NewManifest()
				pm.ObjSize = 8 << 20
				part := &op.ObjectState{
					Exists: true, Size: 8 << 20, Manifest: &pm, Mtime: time.Unix(1700000100, 0), Key: meta.ObjKey{Name: partKey, NS: meta.NSMultipart},
					Attrs: map[string][]byte{meta.AttrETag: []byte("part2")},
				}
				objects = &opfakes.FakeObjectStore{}
				env.Objects = objects
				objects.StatObjectReturns(head, nil)
				objects.PrefetchObjectReturnsOnCall(0, head, nil)
				objects.PrefetchObjectReturnsOnCall(1, part, nil)
			})
			It("serves a part whose head holds no redirect, as prepare reads the part's attrs", func(ctx SpecContext) {
				r := webReq("k")
				o, _, err := run(ctx, r, func(o *op.GetObject) { o.PartNumber = new(2) })
				Expect(err).NotTo(HaveOccurred())
				Expect(o.State.Key.Name).To(Equal(partKey))
				Expect(r.Redirect).To(BeEmpty())
			})
			It("answers InvalidPart for a part the object has not, which prepare refuses before it copies the attrs", func(ctx SpecContext) {
				r := webReq("k")
				_, _, err := run(ctx, r, func(o *op.GetObject) { o.PartNumber = new(9) })
				Expect(err).To(MatchError(op.ErrInvalidPart))
				Expect(r.Redirect).To(BeEmpty())
			})
		})
	})
})
