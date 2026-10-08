package s3_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/s3"
)

// storeReads records every store read a request makes through the stores
// watch wraps, so a spec can tell what a request looked at beyond the
// destination bucket.
type storeReads struct{ log []string }

type readsBuckets struct {
	op.BucketStore
	c *storeReads
}

func (b readsBuckets) GetBucket(ctx context.Context, tenant, name string) (*op.BucketRecord, error) {
	b.c.log = append(b.c.log, "GetBucket "+name)
	return b.BucketStore.GetBucket(ctx, tenant, name)
}

type readsObjects struct {
	op.ObjectStore
	c *storeReads
}

func (o readsObjects) StatObject(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey) (*op.ObjectState, error) {
	o.c.log = append(o.c.log, "StatObject "+key.Name)
	return o.ObjectStore.StatObject(ctx, rec, key)
}

func (o readsObjects) PrefetchObject(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey) (*op.ObjectState, error) {
	o.c.log = append(o.c.log, "PrefetchObject "+key.Name)
	return o.ObjectStore.PrefetchObject(ctx, rec, key)
}

type readsUsers struct {
	op.UserStore
	c *storeReads
}

func (u readsUsers) GetUser(ctx context.Context, id meta.UserID) (*op.UserRecord, error) {
	u.c.log = append(u.c.log, "GetUser "+id.ID)
	return u.UserStore.GetUser(ctx, id)
}

func (u readsUsers) GetUserByEmail(ctx context.Context, email string) (*op.UserRecord, error) {
	u.c.log = append(u.c.log, "GetUserByEmail "+email)
	return u.UserStore.GetUserByEmail(ctx, email)
}

type readsMultipart struct {
	op.MultipartStore
	c *storeReads
}

func (m readsMultipart) GetUpload(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, id string) (*op.Upload, error) {
	m.c.log = append(m.c.log, "GetUpload "+id)
	return m.MultipartStore.GetUpload(ctx, rec, key, id)
}

// watch routes the world's four stores through c.
func (w *writeWorld) watch(c *storeReads) {
	w.env.Buckets = readsBuckets{BucketStore: w.store, c: c}
	w.env.Objects = readsObjects{ObjectStore: w.store, c: c}
	w.env.Users = readsUsers{UserStore: w.store, c: c}
	w.env.Multipart = readsMultipart{MultipartStore: w.store, c: c}
}

var requestAndHostIDs = regexp.MustCompile(`<(RequestId|HostId)>[^<]*</(RequestId|HostId)>`)

// answerOf is rec's status, headers and body with what differs per request
// removed. Header names are canonicalized, as a client reads them.
func answerOf(rec *httptest.ResponseRecorder) string {
	var hs []string
	for k, v := range rec.Result().Header {
		switch k = http.CanonicalHeaderKey(k); k {
		case "X-Amz-Request-Id", "Date", "X-Amz-Id-2":
			continue
		}
		hs = append(hs, k+"="+strings.Join(v, ","))
	}
	sort.Strings(hs)
	return fmt.Sprintf("%d %s %s", rec.Code, strings.Join(hs, ";"), requestAndHostIDs.ReplaceAllString(rec.Body.String(), ""))
}

// abbreviated is up to six of names, and a count of the rest.
func abbreviated(names []string) []string {
	if len(names) <= 6 {
		return names
	}
	return append(names[:6:6], fmt.Sprintf("... %d more", len(names)-6))
}

// putHeaders are the header sets a PutObject or UploadPart is sent with.
var putHeaders = map[string][]string{
	"none":       nil,
	"class":      {"X-Amz-Storage-Class", "NOPE"},
	"canned":     {"X-Amz-Acl", "public-read"},
	"grant-nob":  {"X-Amz-Grant-Read", `id="nobody"`},
	"grant-mail": {"X-Amz-Grant-Read", `emailAddress="nobody@example.com"`},
	"grant-all":  {"X-Amz-Grant-Read", allUsersURI},
	"hold":       {"X-Amz-Object-Lock-Legal-Hold", "ON"},
	"retention":  {"X-Amz-Object-Lock-Mode", "GOVERNANCE", "X-Amz-Object-Lock-Retain-Until-Date", "2030-01-01T00:00:00Z"},
	"md5":        {"Content-MD5", md5B64("x")},
	"tagging":    {"X-Amz-Tagging", "a=b"},
	"ifnone":     {"If-None-Match", "*"},
	"ifmatch":    {"If-Match", `"nope"`},
	"stored":     storedStateHeaders(),
}

// copySources are the sources a copy names: present and missing keys of the
// buckets seeded above, a missing bucket, and versions.
var copySources = []string{
	"/plain/src", "/plain/nope", "/nope/src", "/bobs/src", "/bobs/nope", "/bobs/broken",
	"/carols/src", "/carols/nope", "/carols/broken", "/susp/src", "/badpol/src", "/badblock/src",
	"/plain/src?versionId=v1", "/bobs/src?versionId=v1",
}

// copyHeaders are the header sets a copy is sent with.
var copyHeaders = map[string][]string{
	"none":    nil,
	"class":   {"X-Amz-Storage-Class", "NOPE"},
	"canned":  {"X-Amz-Acl", "public-read"},
	"grant":   {"X-Amz-Grant-Read", `id="nobody"`},
	"hold":    {"X-Amz-Object-Lock-Legal-Hold", "ON"},
	"replace": {"X-Amz-Metadata-Directive", "REPLACE"},
	"ifmatch": {"X-Amz-Copy-Source-If-Match", `"nope"`},
	"stored":  storedStateHeaders(),
}

var _ = Describe("what the answer to a write the destination refuses reveals of its stored state", func() {
	var (
		w     *writeWorld
		carol *op.UserRecord
		reads *storeReads
		id    string
	)
	BeforeEach(func(ctx SpecContext) {
		w = newWriteWorld(ctx, denc.Squid)
		// carol may write plain by its ACL, but her op mask holds only read.
		carol = w.store.AddUser(meta.UserInfo{UserID: meta.UserID{ID: "carol"}, DisplayName: "Carol", OpMask: op.OpTypeRead})
		p := acl.DefaultPolicy(meta.UserOwner(w.alice.Info.UserID), "Alice")
		p.ACL.AddGrant(acl.Grant{Type: acl.GranteeCanonUser, ID: "carol", Name: "Carol", Permission: acl.PermWrite})
		w.setBucketAttrs(ctx, map[string][]byte{meta.AttrACL: encodeACL(p)})

		// Each bucket below is a source whose state the destination's refusal
		// must not reveal: a present and a broken object, a suspended bucket,
		// a bucket policy that does not parse and a block that does not decode.
		bob := meta.UserOwner(w.bob.Info.UserID)
		bobACL := encodeACL(acl.DefaultPolicy(bob, "Bob"))
		carolOwner := meta.UserOwner(carol.Info.UserID)
		carolACL := encodeACL(acl.DefaultPolicy(carolOwner, "Carol"))
		w.addBucket(ctx, "bobs", bob, map[string][]byte{meta.AttrACL: bobACL}, map[string][]byte{"src": bobACL, "broken": {0xff}})
		w.addBucket(ctx, "carols", carolOwner, map[string][]byte{meta.AttrACL: carolACL}, map[string][]byte{"src": carolACL, "broken": {0xff}})
		susp := w.addBucket(ctx, "susp", bob, map[string][]byte{meta.AttrACL: bobACL}, map[string][]byte{"src": bobACL})
		susp.Info.Flags |= meta.BucketSuspended
		Expect(w.store.PutBucketInfo(ctx, susp)).To(Succeed())
		w.addBucket(ctx, "badpol", bob, map[string][]byte{meta.AttrACL: bobACL, op.AttrIAMPolicy: []byte("{")}, map[string][]byte{"src": bobACL})
		w.addBucket(ctx, "badblock", bob, map[string][]byte{meta.AttrACL: bobACL, op.AttrPublicAccess: {0xff}}, map[string][]byte{"src": bobACL})

		id = w.initUpload(w.alice, "/plain/k")
		Expect(w.send(w.alice, http.MethodPut, "/plain/k?uploadId="+id+"&partNumber=1", "x").Code).To(Equal(200))

		reads = &storeReads{}
		w.watch(reads)
	})

	type request struct {
		name, method, target, body string
		hdr                        []string
	}
	requestsOf := func(opName string) []request {
		var rs []request
		switch opName {
		case "PutObject":
			for _, key := range []string{"k", "src"} {
				for hn, h := range putHeaders {
					rs = append(rs, request{key + "/" + hn, http.MethodPut, "/plain/" + key, "x", h})
				}
			}
		case "CopyObject":
			for _, src := range copySources {
				for hn, h := range copyHeaders {
					rs = append(rs, request{src + "/" + hn, http.MethodPut, "/plain/dst", "", append([]string{"X-Amz-Copy-Source", src}, h...)})
				}
			}
			rs = append(rs, request{"onto itself", http.MethodPut, "/plain/src", "", []string{"X-Amz-Copy-Source", "/plain/src"}},
				request{"onto itself, missing", http.MethodPut, "/plain/nope", "", []string{"X-Amz-Copy-Source", "/plain/nope"}})
		case "UploadPart":
			for _, up := range []string{id, "2~nope", "a.b"} {
				for hn, h := range putHeaders {
					rs = append(rs, request{up + "/" + hn, http.MethodPut, "/plain/k?uploadId=" + up + "&partNumber=2", "x", h})
				}
			}
		case "UploadPartCopy":
			for _, up := range []string{id, "2~nope"} {
				for _, src := range copySources {
					for hn, h := range copyHeaders {
						rs = append(rs, request{
							up + src + "/" + hn, http.MethodPut, "/plain/k?uploadId=" + up + "&partNumber=2", "",
							append([]string{"Content-Length", "0", "X-Amz-Copy-Source", src}, h...),
						})
					}
				}
			}
		}
		return rs
	}

	for _, opName := range []string{"PutObject", "CopyObject", "UploadPart", "UploadPartCopy"} {
		for _, who := range []string{"bob (destination ACL refuses)", "carol (op mask refuses)", "anonymous (destination ACL refuses)"} {
			It(fmt.Sprintf("answers %s on %s one answer and reads nothing past the destination, whatever the store holds", who, opName), func(ctx SpecContext) {
				requester := w.bob
				if strings.HasPrefix(who, "carol") {
					requester = carol
				}
				answers := map[string][]string{}
				beyond := map[string][]string{}
				run := func(state string) {
					for _, rq := range requestsOf(opName) {
						reads.log = nil
						var rec *httptest.ResponseRecorder
						if strings.HasPrefix(who, "anonymous") {
							var b io.Reader
							if rq.body != "" {
								b = strings.NewReader(rq.body)
							}
							anon := s3.AuthenticatorFunc(func(context.Context, *http.Request, op.PayloadForms) (*op.AuthResult, error) {
								return &op.AuthResult{Identity: op.Anonymous()}, nil
							})
							rec = serveReq(s3.NewHandler(w.env, anon, testConfig(s3.Config{})), rq.method, rq.target, b, rq.hdr...)
						} else {
							rec = w.send(requester, rq.method, rq.target, rq.body, rq.hdr...)
						}
						a := answerOf(rec)
						answers[a] = append(answers[a], state+":"+rq.name)
						for _, c := range reads.log {
							if c != "GetBucket plain" {
								beyond[c] = append(beyond[c], state+":"+rq.name)
							}
						}
					}
				}
				run("plain")
				w.configureStoredState(ctx)
				run("configured")
				if len(answers) != 1 {
					for a, names := range answers {
						GinkgoWriter.Printf("ANSWER %q\n   for %v\n", a, abbreviated(names))
					}
				}
				for c, names := range beyond {
					GinkgoWriter.Printf("READ %s for %v\n", c, abbreviated(names))
				}
				Expect(answers).To(HaveLen(1), "distinct answers")
				Expect(beyond).To(BeEmpty(), "store reads beyond the destination bucket")
			})
		}
	}

	It("records the source reads of an authorized copy, so an empty list above means something", func() {
		rec := w.send(w.alice, http.MethodPut, "/plain/dst", "", "X-Amz-Copy-Source", "/plain/src", "X-Amz-Grant-Read", `id="bob"`)
		Expect(rec.Code).To(Equal(200), rec.Body.String())
		Expect(reads.log).To(ContainElements("GetBucket plain", "PrefetchObject src", "GetUser bob"))
	})

	It("defers a destination denial for an admin alone, who is answered as an authorized requester", func() {
		root := w.store.AddUser(meta.UserInfo{UserID: meta.UserID{ID: "root"}, DisplayName: "Root", OpMask: op.OpTypeAll, Admin: 1})
		h := s3.NewHandler(w.env, s3.AuthenticatorFunc(func(context.Context, *http.Request, op.PayloadForms) (*op.AuthResult, error) {
			return &op.AuthResult{Identity: op.Identity{User: &root.Info, Owner: meta.UserOwner(root.Info.UserID), OpMask: op.OpTypeAll, Admin: true}}, nil
		}), testConfig(s3.Config{}))
		send := func(hdr ...string) *httptest.ResponseRecorder {
			return serveReq(h, http.MethodPut, "/plain/dst", nil, hdr...)
		}
		expectError(send("X-Amz-Copy-Source", "/nope/src"), 404, "NoSuchBucket")
		expectError(send("X-Amz-Copy-Source", "/plain/src", "X-Amz-Storage-Class", "NOPE"), 400, "InvalidArgument")
		Expect(send("X-Amz-Copy-Source", "/plain/src").Code).To(Equal(200))
		expectError(w.send(w.bob, http.MethodPut, "/plain/dst", "", "X-Amz-Copy-Source", "/nope/src"), 403, "AccessDenied")
	})
})

var _ = Describe("what a copy onto itself reveals to a requester the destination authorizes", func() {
	// The residual of the order the specs above pin: bob may write plain but
	// cannot read src. radosgw answers the same way. RGWCopyObj::verify_permission
	// reads the source's head and calls check_storage_class before the source's
	// own permission check (rgw_op.cc:5420-5435 at v19.2.6, :5986-5997 at
	// v20.2.4), and RGWCopyObj_ObjStore_S3::check_storage_class refuses a copy
	// that changes nothing with InvalidRequest (rgw_rest_s3.cc:3553-3564 at
	// v19.2.6, :3833-3844 at v20.2.4).
	It("answers 400 for an object that exists in the class asked for and 403 for one that does not exist, ahead of the source's own check", func(ctx SpecContext) {
		w := newWriteWorld(ctx, denc.Squid)
		w.grantBob(ctx, acl.PermWrite)
		itself := func(key string, hdr ...string) *httptest.ResponseRecorder {
			return w.send(w.bob, http.MethodPut, "/plain/"+key, "", append([]string{"X-Amz-Copy-Source", "/plain/" + key}, hdr...)...)
		}
		expectError(itself("src"), 400, "InvalidRequest")
		expectError(itself("nope"), 403, "AccessDenied")
		expectError(itself("src", "X-Amz-Metadata-Directive", "REPLACE"), 403, "AccessDenied")
	})
})
