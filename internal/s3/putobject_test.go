package s3_test

import (
	"context"
	"crypto/md5" //nolint:gosec // a Content-MD5 is an MD5
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/authz"
	"github.com/jhoblitt/rgw-go/internal/cephconf"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/s3"
	"github.com/jhoblitt/rgw-go/internal/tags"
)

// writeMtime is the write world's store clock, on a whole second.
var writeMtime = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

// writeWorld is the memstore the object write specs run on: alice's bucket
// "plain", whose ACL is her private default under "Alice", holding "src"
// ("hello", alice's ACL), and bob, who owns nothing, authorized by the
// policy evaluator.
type writeWorld struct {
	store      *memstore.Store
	env        *op.Env
	conf       cephconf.MapGetter
	alice, bob *op.UserRecord
}

func newWriteWorld(ctx context.Context, rel denc.Release) *writeWorld {
	GinkgoHelper()
	return newWriteWorldAt(ctx, rel, writeMtime)
}

// newWriteWorldAt is newWriteWorld with the store's clock at mtime.
func newWriteWorldAt(ctx context.Context, rel denc.Release, mtime time.Time) *writeWorld {
	GinkgoHelper()
	store := memstore.New(memstore.Config{Release: rel, Now: func() time.Time { return mtime }})
	w := &writeWorld{store: store, conf: cephconf.MapGetter{}}
	w.alice = store.AddUser(meta.UserInfo{UserID: meta.UserID{ID: "alice"}, DisplayName: "Alice", OpMask: op.OpTypeAll})
	w.bob = store.AddUser(meta.UserInfo{UserID: meta.UserID{ID: "bob"}, DisplayName: "Bob", OpMask: op.OpTypeAll})
	w.env = testEnv(store)
	w.env.Authz = authz.New(authz.DefaultConfig(rel))
	w.env.Conf = cephconf.NewOptions(w.conf)
	owner := meta.UserOwner(w.alice.Info.UserID)
	_, err := store.CreateBucket(ctx, op.CreateBucketParams{
		Name: "plain", Owner: owner, Placement: meta.PlacementRule{Name: "default-placement"},
		Attrs: map[string][]byte{meta.AttrACL: encodeACL(acl.DefaultPolicy(owner, "Alice"))},
	})
	Expect(err).NotTo(HaveOccurred())
	w.put(ctx, "src", "hello", nil)
	return w
}

// handler is the world's handler as who, built after the spec set its
// configuration.
func (w *writeWorld) handler(who *op.UserRecord) http.Handler {
	return s3.NewHandler(w.env, authAs(who), testConfig(s3.Config{}))
}

// send serves method and target as who with body, sent with its length
// when it is not empty; hdr alternates header names and values.
func (w *writeWorld) send(who *op.UserRecord, method, target, body string, hdr ...string) *httptest.ResponseRecorder {
	var b io.Reader
	if body != "" {
		b = strings.NewReader(body)
	}
	return serveReq(w.handler(who), method, target, b, hdr...)
}

// sendTampered serves alice's method and target with body, whose payload
// check fails where the body ends.
func (w *writeWorld) sendTampered(method, target, body string, hdr ...string) *httptest.ResponseRecorder {
	auth := s3.AuthenticatorFunc(func(context.Context, *http.Request, op.PayloadForms) (*op.AuthResult, error) {
		return &op.AuthResult{
			Identity:      op.Identity{User: &w.alice.Info, Owner: meta.UserOwner(w.alice.Info.UserID), OpMask: w.alice.Info.OpMask},
			Body:          &failingBody{r: strings.NewReader(body), err: op.ErrContentSHA256Mismatch},
			ContentLength: int64(len(body)),
		}, nil
	})
	return serveReq(s3.NewHandler(w.env, auth, testConfig(s3.Config{})), method, target, strings.NewReader(body), hdr...)
}

// bucket is plain as stored.
func (w *writeWorld) bucket(ctx context.Context) *op.BucketRecord {
	GinkgoHelper()
	rec, err := w.store.GetBucket(ctx, "", "plain")
	Expect(err).NotTo(HaveOccurred())
	return rec
}

// put stores key in plain with data, alice's ACL and attrs.
func (w *writeWorld) put(ctx context.Context, key, data string, attrs map[string][]byte) {
	GinkgoHelper()
	all := map[string][]byte{meta.AttrACL: encodeACL(acl.DefaultPolicy(meta.UserOwner(w.alice.Info.UserID), "Alice"))}
	maps.Copy(all, attrs)
	_, err := w.store.PutObject(ctx, w.bucket(ctx), meta.ObjKey{Name: key}, strings.NewReader(data), op.PutParams{Size: int64(len(data)), Attrs: all})
	Expect(err).NotTo(HaveOccurred())
}

// stat is key's state in plain.
func (w *writeWorld) stat(ctx context.Context, key string) *op.ObjectState {
	GinkgoHelper()
	st, err := w.store.StatObject(ctx, w.bucket(ctx), meta.ObjKey{Name: key})
	Expect(err).NotTo(HaveOccurred())
	return st
}

// setBucketAttrs sets attrs on plain.
func (w *writeWorld) setBucketAttrs(ctx context.Context, set map[string][]byte) {
	GinkgoHelper()
	Expect(w.store.PutBucketAttrs(ctx, w.bucket(ctx), set, nil)).To(Succeed())
}

// setBucketInfo changes plain's info through edit.
func (w *writeWorld) setBucketInfo(ctx context.Context, edit func(*meta.BucketInfo)) {
	GinkgoHelper()
	rec := w.bucket(ctx)
	edit(&rec.Info)
	Expect(w.store.PutBucketInfo(ctx, rec)).To(Succeed())
}

// grantBob gives bob perm on plain's ACL beside alice's FULL_CONTROL.
func (w *writeWorld) grantBob(ctx context.Context, perm acl.Permission) {
	GinkgoHelper()
	p := acl.DefaultPolicy(meta.UserOwner(w.alice.Info.UserID), "Alice")
	p.ACL.AddGrant(acl.Grant{Type: acl.GranteeCanonUser, ID: "bob", Name: "Bob", Permission: perm})
	w.setBucketAttrs(ctx, map[string][]byte{meta.AttrACL: encodeACL(p)})
}

func md5Hex(s string) string {
	sum := md5.Sum([]byte(s)) //nolint:gosec // an ETag is an MD5
	return hex.EncodeToString(sum[:])
}

func md5B64(s string) string {
	sum := md5.Sum([]byte(s)) //nolint:gosec // a Content-MD5 is an MD5
	return base64.StdEncoding.EncodeToString(sum[:])
}

// expectError checks rec is status with code in radosgw's error document,
// with its length and Accept-Ranges.
func expectError(rec *httptest.ResponseRecorder, status int, code string) {
	GinkgoHelper()
	Expect(rec.Code).To(Equal(status), rec.Body.String())
	Expect(rec.Body.String()).To(ContainSubstring("<Code>"+code+"</Code>"), "status %d", status)
	Expect(rec.Header().Get("Accept-Ranges")).To(Equal("bytes"), "end_header's error branch, rgw_rest.cc:620-624 at v19.2.6")
}

var _ = Describe("put_obj", func() {
	var w *writeWorld
	BeforeEach(func(ctx SpecContext) { w = newWriteWorld(ctx, denc.Squid) })

	It("stores the object and answers send_response's headers and no body", func(ctx SpecContext) {
		rec := w.send(w.alice, http.MethodPut, "/plain/k", "hello",
			"Content-Type", "text/plain", "X-Amz-Meta-Color", "blue", "X-Amz-Tagging", "a=b")
		Expect(rec.Code).To(Equal(200), rec.Body.String())
		Expect(exactHeader(rec.Header(), "ETag")).To(Equal(`"5d41402abc4b2a76b9719d911017c592"`))
		Expect(rec.Header().Get("Content-Length")).To(Equal("0"), "dump_content_length(s, 0), rgw_rest_s3.cc:2747")
		Expect(rec.Header().Get("Accept-Ranges")).To(Equal("bytes"))
		Expect(rec.Header()).NotTo(HaveKey("Content-Type"), "end_header names no type for a PUT")
		Expect(rec.Header()).NotTo(haveHeaderAnyCase("x-amz-version-id"))
		Expect(rec.Body.Len()).To(BeZero())
		st := w.stat(ctx, "k")
		Expect(st.Attrs).To(HaveKeyWithValue(meta.AttrContentType, []byte("text/plain\x00")))
		Expect(st.Attrs).To(HaveKeyWithValue(meta.AttrMetaPrefix+"color", []byte("blue\x00")))
		set := tags.Set{}
		Expect(set.Add("a", "b", tags.MaxObjectTags)).To(Succeed())
		Expect(st.Attrs).To(HaveKeyWithValue(tags.Attr, encodeTags(set)))
		Expect(st.Attrs).To(HaveKeyWithValue(meta.AttrACL, encodeACL(acl.DefaultPolicy(meta.UserOwner(w.alice.Info.UserID), "Alice"))))
		head := w.send(w.alice, http.MethodHead, "/plain/k", "")
		Expect(exactHeader(head.Header(), "x-amz-meta-color")).To(Equal("blue"))
		Expect(exactHeader(head.Header(), "x-amz-tagging-count")).To(Equal("1"))
	})
	It("builds the ACL of a bucket-owner canned ACL for the bucket's ACL owner", func(ctx SpecContext) {
		w.grantBob(ctx, acl.PermWrite)
		Expect(w.send(w.bob, http.MethodPut, "/plain/k", "x", "X-Amz-Acl", "bucket-owner-full-control").Code).To(Equal(200))
		p, err := op.ObjectACLFor(ctx, w.stat(ctx, "k"), w.bucket(ctx))
		Expect(err).NotTo(HaveOccurred())
		Expect(p.Owner.ID).To(Equal("bob"))
		Expect(p.ACL.Grants).To(ContainElement(HaveField("Grant.ID", "alice")), "create_s3_policy's bucket owner is s->bucket_owner")
	})
	It("stores an empty object sent with its length", func(ctx SpecContext) {
		Expect(w.send(w.alice, http.MethodPut, "/plain/e", "", "Content-Length", "0").Code).To(Equal(200))
		Expect(w.stat(ctx, "e").Size).To(BeZero())
	})
	It("requires a length or a chunked body", func(ctx SpecContext) {
		expectError(w.send(w.alice, http.MethodPut, "/plain/k", ""), 411, "MissingContentLength")
		Expect(w.stat(ctx, "k").Exists).To(BeFalse())
		chunked := serveReq(w.handler(w.alice), http.MethodPut, "/plain/k", io.MultiReader(strings.NewReader("hello")))
		Expect(chunked.Code).To(Equal(200), "a body of unknown length is chunked, rgw_rest_s3.cc:2599-2607")
		Expect(w.stat(ctx, "k").ETag).To(Equal(md5Hex("hello")))
	})
	It("answers a missing bucket before its parameters", func() {
		expectError(w.send(w.alice, http.MethodPut, "/nope/k", "", "X-Amz-Tagging", "&"), 404, "NoSuchBucket")
	})
	Describe("Content-MD5", func() {
		It("stores a body that matches", func() {
			Expect(w.send(w.alice, http.MethodPut, "/plain/k", "hello", "Content-MD5", md5B64("hello")).Code).To(Equal(200))
		})
		It("refuses a mismatch with BadDigest and stores nothing", func(ctx SpecContext) {
			expectError(w.send(w.alice, http.MethodPut, "/plain/k", "hello", "Content-MD5", md5B64("world")), 400, "BadDigest")
			Expect(w.stat(ctx, "k").Exists).To(BeFalse())
		})
		DescribeTable("decodes as ceph_unarmor does, InvalidDigest unless it gives 16 bytes",
			func(v string, want int) {
				rec := w.send(w.alice, http.MethodPut, "/plain/k", "hello", "Content-MD5", v)
				if want == 200 {
					Expect(rec.Code).To(Equal(200), rec.Body.String())
				} else {
					expectError(rec, 400, "InvalidDigest")
				}
			},
			Entry("not base64", "notbase64!", 400),
			Entry("empty", "", 400),
			Entry("a short digest", base64.StdEncoding.EncodeToString(make([]byte, 15)), 400),
			Entry("a long digest, past the 17-byte buffer", base64.StdEncoding.EncodeToString(make([]byte, 18)), 400),
			Entry("no padding, a group cut short", strings.TrimRight(md5B64("hello"), "="), 400),
			Entry("anything after the padding, ignored", md5B64("hello")+"!!", 200),
			Entry("URL-safe digits", strings.NewReplacer("+", "-", "/", "_").Replace(md5B64("hello")), 200),
			Entry("line feeds between groups", md5B64("hello")[:4]+"\n"+md5B64("hello")[4:], 200),
		)
	})
	It("refuses an x-amz-tagging that set_from_string refuses as InvalidArgument", func(ctx SpecContext) {
		expectError(w.send(w.alice, http.MethodPut, "/plain/k", "x", "X-Amz-Tagging", "a=b&"), 400, "InvalidArgument")
		eleven := make([]string, 11)
		for i := range eleven {
			eleven[i] = string(rune('a'+i)) + "=v"
		}
		expectError(w.send(w.alice, http.MethodPut, "/plain/k", "x", "X-Amz-Tagging", strings.Join(eleven, "&")), 400, "InvalidArgument")
		Expect(w.stat(ctx, "k").Exists).To(BeFalse())
	})
	Describe("the object-lock headers", func() {
		DescribeTable("refuse a value get_params refuses as InvalidArgument",
			func(hdr ...string) {
				expectError(w.send(w.alice, http.MethodPut, "/plain/k", "x", hdr...), 400, "InvalidArgument")
			},
			Entry("a legal hold neither ON nor OFF", "X-Amz-Object-Lock-Legal-Hold", "on"),
			Entry("a mode without a date", "X-Amz-Object-Lock-Mode", "GOVERNANCE"),
			Entry("a date without a mode", "X-Amz-Object-Lock-Retain-Until-Date", "2030-01-01T00:00:00Z"),
			Entry("a date in the past", "X-Amz-Object-Lock-Mode", "GOVERNANCE", "X-Amz-Object-Lock-Retain-Until-Date", "2020-01-01T00:00:00Z"),
			Entry("a date from_iso_8601 refuses", "X-Amz-Object-Lock-Mode", "GOVERNANCE", "X-Amz-Object-Lock-Retain-Until-Date", "2030-1-01"),
			Entry("an unknown mode", "X-Amz-Object-Lock-Mode", "governance", "X-Amz-Object-Lock-Retain-Until-Date", "2030-01-01T00:00:00Z"),
		)
		// A date get_params takes reaches the bucket's check, which a bucket
		// without object lock refuses as InvalidRequest; one it refuses is
		// InvalidArgument. The clock is testEnv's, 2025-09-27T08:34:58Z.
		DescribeTable("read the retain-until date as from_iso_8601 reads it, later than the clock's whole second",
			func(date, code string) {
				expectError(w.send(w.alice, http.MethodPut, "/plain/k", "x", "X-Amz-Object-Lock-Mode", "GOVERNANCE",
					"X-Amz-Object-Lock-Retain-Until-Date", date), 400, code)
			},
			Entry("a year alone, its first midnight", "2030", "InvalidRequest"),
			Entry("a year and a month", "2030-06", "InvalidRequest"),
			Entry("a date ended by white space, whatever follows", "2030-06-15\tx", "InvalidRequest"),
			Entry("an hour and Z", "2030-06-15T12Z", "InvalidRequest"),
			Entry("an hour and minutes and Z", "2030-06-15T12:30Z", "InvalidRequest"),
			Entry("a Z ended by white space, whatever follows", "2030-06-15T12:30:45Z x", "InvalidRequest"),
			Entry("one fractional digit", "2030-06-15T12:30:45.1Z", "InvalidRequest"),
			Entry("nine fractional digits", "2030-06-15T12:30:45.123456789Z", "InvalidRequest"),
			Entry("a thirteenth month, carried into the next year", "2030-13-01", "InvalidRequest"),
			Entry("the 31st of February, carried into March", "2030-02-31", "InvalidRequest"),
			Entry("the second after the clock's", "2025-09-27T08:34:59Z", "InvalidRequest"),
			Entry("real_time's last nanosecond", "2554-07-21T23:34:33.709551615Z", "InvalidRequest"),
			// radosgw wraps these two to a later date and takes them;
			// docs/exclusions.md, "A date real_time cannot hold is refused".
			Entry("a date past real_time's range, which radosgw wraps to a later one", "3000-01-01T00:00:00Z", "InvalidArgument"),
			Entry("day 00 of 1970, before the epoch, which radosgw wraps to 2554", "1970-01-00", "InvalidArgument"),
			Entry("an hour without Z", "2030-06-15T12", "InvalidArgument"),
			Entry("a whole second without Z", "2030-06-15T12:30:45", "InvalidArgument"),
			Entry("a zone offset", "2030-06-15T12:30:45+01:00", "InvalidArgument"),
			Entry("a dot without digits", "2030-06-15T12:30:45.Z", "InvalidArgument"),
			Entry("ten fractional digits", "2030-06-15T12:30:45.1234567890Z", "InvalidArgument"),
			Entry("a Z followed by more than white space", "2030-06-15T12:30:45Zx", "InvalidArgument"),
			Entry("leading white space", " 2030-06-15", "InvalidArgument"),
			Entry("an empty value", "", "InvalidArgument"),
			Entry("a year before 1970", "1969-12-31", "InvalidArgument"),
			Entry("the clock's own second", "2025-09-27T08:34:58Z", "InvalidArgument"),
			Entry("the clock's own second, nanoseconds before its end", "2025-09-27T08:34:58.999999999Z", "InvalidArgument"),
			Entry("the epoch", "1970-01-01T00:00:00Z", "InvalidArgument"),
			Entry("the second before the epoch, which internal_timegm makes -1", "1970-01-00T23:59:59Z", "InvalidArgument"),
			Entry("a nanosecond past real_time's range, which wraps to the epoch", "2554-07-21T23:34:33.709551616Z", "InvalidArgument"),
			Entry("a date past real_time's range, which wraps to an earlier one", "2600-01-01T00:00:00Z", "InvalidArgument"),
		)
		It("refuse a retention or a legal hold on a bucket without object lock as InvalidRequest", func() {
			expectError(w.send(w.alice, http.MethodPut, "/plain/k", "x", "X-Amz-Object-Lock-Legal-Hold", "ON"), 400, "InvalidRequest")
			expectError(w.send(w.alice, http.MethodPut, "/plain/k", "x", "X-Amz-Object-Lock-Mode", "GOVERNANCE",
				"X-Amz-Object-Lock-Retain-Until-Date", "2030-01-01 trailing text"), 400, "InvalidRequest",
			)
			expectError(w.send(w.alice, http.MethodPut, "/plain/k", "x", "X-Amz-Object-Lock-Mode", "COMPLIANCE",
				"X-Amz-Object-Lock-Retain-Until-Date", "2030-01-01T00:00:00.5Z"), 400, "InvalidRequest")
		})
		It("are not served on a bucket with object lock", func(ctx SpecContext) {
			w.setBucketInfo(ctx, func(i *meta.BucketInfo) { i.Flags |= meta.BucketObjLockEnabled | meta.BucketVersioned })
			expectError(w.send(w.alice, http.MethodPut, "/plain/k", "x", "X-Amz-Object-Lock-Legal-Hold", "ON"), 501, "NotImplemented")
		})
	})
	// get_params reads partNumber on every put_obj request, a part or not
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2682-L2694 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2835-L2847 at v20.2.4),
	// through strict_strtol
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/common/strtol.cc#L42-L78 at v19.2.7
	// and v20.2.4).
	Describe("partNumber", func() {
		DescribeTable("without an upload id, refuses one strict_strtol refuses as InvalidArgument and stores nothing",
			func(ctx SpecContext, query string) {
				expectError(w.send(w.alice, http.MethodPut, "/plain/k?"+query, "hello"), 400, "InvalidArgument")
				Expect(w.stat(ctx, "k").Exists).To(BeFalse())
			},
			Entry("no digits", "partNumber=x"),
			Entry("a byte after the digits", "partNumber=1x"),
			Entry("a space after the digits", "partNumber=1%20"),
			Entry("a sign alone", "partNumber=-"),
			Entry("white space alone", "partNumber=%20%20"),
			Entry("a hexadecimal number, read in base 10", "partNumber=0x1"),
			Entry("one past int's largest", "partNumber=2147483648"),
			Entry("one below int's smallest", "partNumber=-2147483649"),
			Entry("an overflow of long long, ERANGE", "partNumber=99999999999999999999"),
			Entry("an empty upload id, which is none", "uploadId=&partNumber=x"),
		)
		DescribeTable("without an upload id, stores a PutObject for one strict_strtol reads, or none",
			func(ctx SpecContext, query string) {
				rec := w.send(w.alice, http.MethodPut, "/plain/k?"+query, "hello")
				Expect(rec.Code).To(Equal(200), rec.Body.String())
				Expect(exactHeader(rec.Header(), "ETag")).To(Equal(`"` + md5Hex("hello") + `"`))
				Expect(w.stat(ctx, "k").ETag).To(Equal(md5Hex("hello")))
				Expect(w.uploads(ctx)).To(BeEmpty())
			},
			Entry("an empty value, which get_params skips", "partNumber="),
			Entry("the bare argument", "partNumber"),
			Entry("leading white space, which strtoll skips", "partNumber=%20%09%201"),
			Entry("a plus sign", "partNumber=%2B1"),
			Entry("a negative number", "partNumber=-1"),
			Entry("int's largest", "partNumber=2147483647"),
			Entry("int's smallest", "partNumber=-2147483648"),
			Entry("a NUL, where the C string strict_strtol reads ends", "partNumber=1%00x"),
			Entry("an empty upload id, which is none", "uploadId=&partNumber=1"),
			Entry("an empty upload id without a part number, which is none", "uploadId="),
			Entry("an empty upload id and an empty part number", "uploadId=&partNumber="),
		)
		DescribeTable("with an upload id, refuses an empty one or one strict_strtol refuses as InvalidArgument",
			func(ctx SpecContext, value string) {
				id := w.initUpload(w.alice, "/plain/k")
				expectError(w.send(w.alice, http.MethodPut, "/plain/k?uploadId="+id+"&partNumber="+value, "hello"), 400, "InvalidArgument")
				Expect(w.parts(ctx, "k", id)).To(BeEmpty())
			},
			Entry("no digits", "x"),
			Entry("an empty value", ""),
			Entry("a sign alone", "%2B"),
			Entry("an overflow of long long, ERANGE", "99999999999999999999"),
		)
		DescribeTable("with an upload id, stores the part strict_strtol numbers",
			func(ctx SpecContext, value string, want int) {
				id := w.initUpload(w.alice, "/plain/k")
				rec := w.send(w.alice, http.MethodPut, "/plain/k?uploadId="+id+"&partNumber="+value, "hello")
				Expect(rec.Code).To(Equal(200), rec.Body.String())
				Expect(w.parts(ctx, "k", id)).To(ConsistOf(HaveField("Number", want)))
				Expect(w.stat(ctx, "k").Exists).To(BeFalse())
			},
			Entry("leading white space", "%20%201", 1),
			Entry("a plus sign", "%2B2", 2),
		)
		It("is refused before the permission check, as get_params runs in init_processing", func(ctx SpecContext) {
			expectError(w.send(w.bob, http.MethodPut, "/plain/k?partNumber=x", "hello"), 400, "InvalidArgument")
			expectError(w.send(w.bob, http.MethodPut, "/plain/k?partNumber=1", "hello"), 403, "AccessDenied")
			Expect(w.stat(ctx, "k").Exists).To(BeFalse())
		})
		It("is read after the length and before Content-MD5, which execute reads", func() {
			expectError(w.send(w.alice, http.MethodPut, "/plain/k?partNumber=x", ""), 411, "MissingContentLength")
			expectError(w.send(w.alice, http.MethodPut, "/plain/k?partNumber=x", "hello", "Content-MD5", "nope"), 400, "InvalidArgument")
		})
	})
	DescribeTable("refuses a write that would be encrypted with NotImplemented, storing nothing",
		func(ctx SpecContext, target string, hdr ...string) {
			expectError(w.send(w.alice, http.MethodPut, target, "x", hdr...), 501, "NotImplemented")
			Expect(w.stat(ctx, "k").Exists).To(BeFalse())
		},
		append(sseEntries("/plain/k"),
			Entry("SSE in the query string, which map_qs_metadata reads", "/plain/k?x-amz-server-side-encryption=AES256"),
			Entry("SSE in the query string in another case", "/plain/k?X-Amz-Server-Side-Encryption-Customer-Key=a2V5"),
		),
	)
	DescribeTable("stores a write naming encryption under another meta prefix, an ordinary header, and stores no attr of it",
		func(ctx SpecContext, target string, hdr ...string) {
			rec := w.send(w.alice, http.MethodPut, target, "x", hdr...)
			Expect(rec.Code).To(Equal(200), rec.Body.String())
			st := w.stat(ctx, "k")
			Expect(st.Exists).To(BeTrue())
			Expect(st.Attrs).NotTo(HaveKey(HavePrefix(meta.AttrPrefix + "x-amz-")))
		},
		otherPrefixSSEEntries("/plain/k"),
	)
	DescribeTable("stores no meta, ACL or tags from headers under another meta prefix, which are ordinary headers",
		func(ctx SpecContext, prefix string) {
			rec := w.send(w.alice, http.MethodPut, "/plain/k", "hello",
				prefix+"Meta-Foo", "bar", prefix+"Acl", "public-read", prefix+"Tagging", "a=b")
			Expect(rec.Code).To(Equal(200), rec.Body.String())
			st := w.stat(ctx, "k")
			Expect(st.Attrs).NotTo(HaveKey(HavePrefix(meta.AttrPrefix+"x-amz-")), "no x_meta_map name is stored")
			Expect(st.Attrs).NotTo(HaveKey(tags.Attr))
			Expect(st.Attrs).To(HaveKeyWithValue(meta.AttrACL, encodeACL(acl.DefaultPolicy(meta.UserOwner(w.alice.Info.UserID), "Alice"))))
			head := w.send(w.alice, http.MethodHead, "/plain/k", "")
			Expect(head.Header()).NotTo(haveHeaderAnyCase("x-amz-meta-foo"))
		},
		otherPrefixEntries(),
	)
	It("refuses a write to a bucket with a default encryption with NotImplemented once the requester is authorized", func(ctx SpecContext) {
		w.setBucketAttrs(ctx, map[string][]byte{op.AttrBucketEncryption: {1}})
		expectError(w.send(w.alice, http.MethodPut, "/plain/k", "x"), 501, "NotImplemented")
		expectError(w.send(w.bob, http.MethodPut, "/plain/k", "x"), 403, "AccessDenied")
		Expect(w.stat(ctx, "k").Exists).To(BeFalse())
	})
	DescribeTable("does not serve a PUT naming a copy source that is no copy",
		func(target string, hdr ...string) {
			expectError(w.send(w.alice, http.MethodPut, target, "x", hdr...), 501, "NotImplemented")
		},
		Entry("a copy source with a range", "/plain/k", "X-Amz-Copy-Source", "/plain/src", "X-Amz-Copy-Source-Range", "bytes=0-1"),
		Entry("a copy source with an empty bucket", "/plain/k", "X-Amz-Copy-Source", "//src"),
	)
	Describe("If-Match and If-None-Match", func() {
		It("passes If-None-Match: * and answers 412 on an existing key", func(ctx SpecContext) {
			Expect(w.send(w.alice, http.MethodPut, "/plain/k", "one", "If-None-Match", "*").Code).To(Equal(200))
			expectError(w.send(w.alice, http.MethodPut, "/plain/k", "two", "If-None-Match", "*"), 412, "PreconditionFailed")
			Expect(w.stat(ctx, "k").ETag).To(Equal(md5Hex("one")))
		})
		It("takes an If-Match sent empty as a condition, which fails", func(ctx SpecContext) {
			expectError(w.send(w.alice, http.MethodPut, "/plain/src", "two", "If-Match", ""), 412, "PreconditionFailed")
			Expect(w.stat(ctx, "src").ETag).To(Equal(md5Hex("hello")))
		})
		It("overwrites under an If-None-Match sent empty, which no ETag matches", func(ctx SpecContext) {
			Expect(w.send(w.alice, http.MethodPut, "/plain/src", "two", "If-None-Match", "").Code).To(Equal(200))
			Expect(w.stat(ctx, "src").ETag).To(Equal(md5Hex("two")))
		})
		It("overwrites under the current ETag", func(ctx SpecContext) {
			Expect(w.send(w.alice, http.MethodPut, "/plain/src", "two", "If-Match", `"`+md5Hex("hello")+`"`).Code).To(Equal(200))
		})
	})
	DescribeTable("answers the status rgw_s3_success_create_obj_status names",
		func(value string, want int) {
			w.conf["rgw_s3_success_create_obj_status"] = value
			rec := w.send(w.alice, http.MethodPut, "/plain/k", "x")
			Expect(rec.Code).To(Equal(want))
			Expect(exactHeader(rec.Header(), "ETag")).To(Equal(`"` + md5Hex("x") + `"`))
		},
		Entry("201", "201", 201),
		Entry("204", "204", 204),
		Entry("any other value, 200", "202", 200),
		Entry("0, 200", "0", 200),
	)
	It("marks a requester-pays upload by a non-owner", func(ctx SpecContext) {
		w.grantBob(ctx, acl.PermWrite)
		w.setBucketInfo(ctx, func(i *meta.BucketInfo) { i.RequesterPays = true })
		rec := w.send(w.bob, http.MethodPut, "/plain/k", "x", "X-Amz-Request-Payer", "requester")
		Expect(rec.Code).To(Equal(200), rec.Body.String())
		Expect(exactHeader(rec.Header(), "x-amz-request-charged")).To(Equal("requester"))
		Expect(w.send(w.alice, http.MethodPut, "/plain/k", "x").Header()).NotTo(haveHeaderAnyCase("x-amz-request-charged"))
	})
	It("sends Rgwx-Mtime to a system request", func() {
		system := s3.AuthenticatorFunc(func(context.Context, *http.Request, op.PayloadForms) (*op.AuthResult, error) {
			return &op.AuthResult{Identity: op.Identity{
				User: &w.alice.Info, Owner: meta.UserOwner(w.alice.Info.UserID), OpMask: op.OpTypeAll, System: true, Admin: true,
			}}, nil
		})
		rec := serveReq(s3.NewHandler(w.env, system, testConfig(s3.Config{})), http.MethodPut, "/plain/k", strings.NewReader("x"))
		Expect(rec.Code).To(Equal(200))
		Expect(rec.Header().Get("Rgwx-Mtime")).To(Equal(fmt.Sprintf("%d.000000000", writeMtime.Unix())), "rgw_rest_s3.cc:2779-2781")
		Expect(w.send(w.alice, http.MethodPut, "/plain/k", "x").Header()).NotTo(HaveKey("Rgwx-Mtime"))
	})
	It("refuses a requester the bucket refuses and stores nothing", func(ctx SpecContext) {
		expectError(w.send(w.bob, http.MethodPut, "/plain/k", "x"), 403, "AccessDenied")
		Expect(w.stat(ctx, "k").Exists).To(BeFalse())
	})
	It("refuses a body whose payload hash does not match and stores nothing", func(ctx SpecContext) {
		expectError(w.sendTampered(http.MethodPut, "/plain/k", "hello"), 400, "XAmzContentSHA256Mismatch")
		Expect(w.stat(ctx, "k").Exists).To(BeFalse())
	})
})

// sseEntries is one entry each for SSE-S3, SSE-KMS, SSE-C and a copy
// source's SSE-C key, each a request to target.
func sseEntries(target string) []TableEntry {
	return []TableEntry{
		Entry("SSE-S3", target, "X-Amz-Server-Side-Encryption", "AES256"),
		Entry("SSE-KMS", target, "X-Amz-Server-Side-Encryption", "aws:kms", "X-Amz-Server-Side-Encryption-Aws-Kms-Key-Id", "key"),
		Entry("SSE-C", target, "X-Amz-Server-Side-Encryption-Customer-Algorithm", "AES256",
			"X-Amz-Server-Side-Encryption-Customer-Key", "a2V5", "X-Amz-Server-Side-Encryption-Customer-Key-Md5", "bWQ1"),
		Entry("a copy source's SSE-C key", target, "X-Amz-Copy-Source-Server-Side-Encryption-Customer-Key", "a2V5"),
	}
}

// otherPrefixes are the meta prefixes beside X-Amz- that radosgw's
// init_meta_info files under x-amz- names
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L413-L420 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L426-L433 at v20.2.4)
// and rgw-go reads as ordinary headers.
var otherPrefixes = []string{"X-Goog-", "X-Dho-", "X-Rgw-", "X-Object-", "X-Container-", "X-Account-"}

func otherPrefixEntries() []TableEntry {
	entries := make([]TableEntry, 0, len(otherPrefixes))
	for _, p := range otherPrefixes {
		entries = append(entries, Entry(p, p))
	}
	return entries
}

// otherPrefixSSEEntries is one entry per prefix of otherPrefixes, a request
// to target carrying under it the headers of SSE-S3, SSE-KMS, SSE-C and a
// copy source's SSE-C key.
func otherPrefixSSEEntries(target string) []TableEntry {
	var entries []TableEntry
	for _, p := range otherPrefixes {
		entries = append(entries, Entry("under "+p, target,
			p+"Server-Side-Encryption", "aws:kms", p+"Server-Side-Encryption-Aws-Kms-Key-Id", "key",
			p+"Server-Side-Encryption-Customer-Algorithm", "AES256",
			p+"Server-Side-Encryption-Customer-Key", "a2V5", p+"Server-Side-Encryption-Customer-Key-Md5", "bWQ1",
			p+"Copy-Source-Server-Side-Encryption-Customer-Key", "a2V5"))
	}
	return entries
}
