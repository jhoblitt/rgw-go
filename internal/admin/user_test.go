package admin_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/admin"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// errCode is the Code of a JSON error document.
func errCode(res *http.Response) string {
	GinkgoHelper()
	var doc map[string]string
	Expect(json.Unmarshal([]byte(body(res)), &doc)).To(Succeed())
	return doc["Code"]
}

// userDoc decodes a JSON user document.
func userDoc(b string) map[string]any {
	GinkgoHelper()
	var u map[string]any
	Expect(json.Unmarshal([]byte(b), &u)).To(Succeed(), b)
	return u
}

var _ = Describe("admin user routes", func() {
	var fx *fixture
	BeforeEach(func() { fx = newFixture(admin.Config{}) })
	addUser := func(id, caps string) {
		GinkgoHelper()
		info := meta.NewUserInfo()
		info.UserID = meta.UserID{ID: id}
		if caps != "" {
			Expect(info.Caps.AddString(caps)).To(Succeed())
		}
		fx.store.AddUser(info)
	}
	firstKey := func(u map[string]any) map[string]any {
		GinkgoHelper()
		keys, ok := u["keys"].([]any)
		Expect(ok).To(BeTrue(), "keys: %v", u["keys"])
		Expect(keys).NotTo(BeEmpty())
		key, ok := keys[0].(map[string]any)
		Expect(ok).To(BeTrue(), "key: %v", keys[0])
		return key
	}

	It("PUT /admin/user creates and returns dump_user_info", func() {
		res := fx.request(http.MethodPut, "/admin/user?format=json&uid=leseb&display-name=This+is+leseb&email=leseb%40example.com&user-caps=users%3Dread&op-mask=delete&default-placement=default-placement&placement-tags=fast%2Cssd", "admin")
		Expect(res.StatusCode).To(Equal(200))
		Expect(res.Header.Get("Content-Type")).To(Equal("application/json"))
		b := body(res)
		u := userDoc(b)
		Expect(u).To(HaveKeyWithValue("tenant", ""))
		Expect(u).To(HaveKeyWithValue("user_id", "leseb"))
		Expect(u).To(HaveKeyWithValue("display_name", "This is leseb"))
		Expect(u).To(HaveKeyWithValue("email", "leseb@example.com"))
		Expect(u).To(HaveKeyWithValue("op_mask", "delete"))
		Expect(u).To(HaveKeyWithValue("system", false))
		Expect(u).To(HaveKeyWithValue("admin", false))
		Expect(u).To(HaveKeyWithValue("default_placement", "default-placement"))
		Expect(u["caps"]).To(Equal([]any{map[string]any{"type": "users", "perm": "read"}}))
		Expect(u["placement_tags"]).To(Equal([]any{"fast", "ssd"}))
		Expect(u["keys"]).To(HaveLen(1))
		key := firstKey(u)
		Expect(key).To(HaveKeyWithValue("user", "leseb"))
		Expect(key["access_key"]).To(MatchRegexp(`^[A-Z0-9]{20}$`))
		Expect(key["secret_key"]).To(MatchRegexp(`^[A-Za-z0-9]{40}$`))
		Expect(key).To(HaveKeyWithValue("active", true))
		Expect(key).NotTo(HaveKey("create_date"), "dump_access_keys_info has no create_date")
		Expect(u).NotTo(HaveKey("full_user_id"), "Squid")
		Expect(u).NotTo(HaveKey("stats"))
		Expect(strings.Index(b, `"tenant"`)).To(BeNumerically("<", strings.Index(b, `"user_id"`)), "radosgw's field order")
		rec, err := fx.store.GetUser(context.Background(), meta.UserID{ID: "leseb"})
		Expect(err).NotTo(HaveOccurred())
		Expect(rec.Info.OpMask).To(Equal(op.OpTypeDelete))
	})
	It("renders the same document in XML with the element names JSON drops", func() {
		res := fx.request(http.MethodPut, "/admin/user?format=xml&uid=x1&display-name=X&generate-key=false", "admin")
		Expect(res.StatusCode).To(Equal(200))
		Expect(res.Header.Get("Content-Type")).To(Equal("application/xml"))
		Expect(body(res)).To(MatchRegexp(`^<\?xml version="1\.0" encoding="UTF-8"\?><user_info><tenant></tenant><user_id>x1</user_id><display_name>X</display_name><email></email><suspended>0</suspended><max_buckets>1000</max_buckets><subusers></subusers><keys></keys><swift_keys></swift_keys><caps></caps><op_mask>read, write, delete</op_mask><system>false</system><admin>false</admin><default_placement></default_placement><default_storage_class></default_storage_class><placement_tags></placement_tags><bucket_quota><enabled>false</enabled><check_on_raw>false</check_on_raw><max_size>-1</max_size><max_size_kb>0</max_size_kb><max_objects>-1</max_objects></bucket_quota><user_quota><enabled>false</enabled><check_on_raw>false</check_on_raw><max_size>-1</max_size><max_size_kb>0</max_size_kb><max_objects>-1</max_objects></user_quota><temp_url_keys></temp_url_keys><type>rgw</type><mfa_ids></mfa_ids><account_id></account_id><path>/</path><create_date>\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{6}Z</create_date><tags></tags><group_ids></group_ids></user_info>$`))
		res = fx.request(http.MethodPut, "/admin/user?format=xml&uid=x2&display-name=Y&user-caps=users%3Dread", "admin")
		b := body(res)
		Expect(b).To(ContainSubstring(`<keys><key><user>x2</user><access_key>`))
		Expect(b).To(ContainSubstring(`<caps><cap><type>users</type><perm>read</perm></cap></caps>`))
	})
	It("writes full_user_id first and namespace after tenant on Tentacle", func() {
		fx = newFixtureAt(denc.Tentacle, admin.Config{})
		res := fx.request(http.MethodPut, "/admin/user?uid=t%24u2&display-name=d", "admin")
		Expect(res.StatusCode).To(Equal(200))
		b := body(res)
		u := userDoc(b)
		Expect(u).To(HaveKeyWithValue("full_user_id", "t$u2"))
		Expect(u).NotTo(HaveKey("namespace"), "no namespace")
		Expect(b).To(HavePrefix(`{"full_user_id":"t$u2","tenant":"t","user_id":"u2",`), "v20.2.4 rgw_user.cc:136-142")
		res = fx.request(http.MethodPut, "/admin/user?uid=u3&tenant=t9&display-name=d", "admin")
		Expect(body(res)).To(HavePrefix(`{"full_user_id":"t9$u3","tenant":"t9","user_id":"u3",`), "the tenant argument")
	})
	It("GET /admin/user reads by uid or access-key and hides keys without users=read", func() {
		res := fx.request(http.MethodPut, "/admin/user?uid=leseb&display-name=L&access-key=AKIALESEB&secret-key=lesebsecret", "admin")
		Expect(res.StatusCode).To(Equal(200))
		addUser("reader", "user-info-without-keys=read")

		u := userDoc(body(fx.get("/admin/user?uid=leseb", "admin")))
		Expect(firstKey(u)).To(HaveKeyWithValue("secret_key", "lesebsecret"))
		Expect(u).To(HaveKey("swift_keys"))

		res = fx.get("/admin/user?uid=leseb", "reader")
		Expect(res.StatusCode).To(Equal(200))
		b := body(res)
		u = userDoc(b)
		Expect(u).To(HaveKeyWithValue("user_id", "leseb"))
		Expect(u).NotTo(HaveKey("keys"))
		Expect(u).NotTo(HaveKey("swift_keys"))
		Expect(b).NotTo(ContainSubstring("lesebsecret"))
		Expect(b).NotTo(ContainSubstring("AKIALESEB"))

		u = userDoc(body(fx.get("/admin/user?access-key=AKIALESEB", "admin")))
		Expect(u).To(HaveKeyWithValue("user_id", "leseb"))

		b = body(fx.get("/admin/user?uid=leseb&stats=true", "admin"))
		u = userDoc(b)
		Expect(u["stats"]).To(Equal(map[string]any{
			"size": 0.0, "size_actual": 0.0, "size_utilized": 0.0, "size_kb": 0.0, "size_kb_actual": 0.0, "size_kb_utilized": 0.0, "num_objects": 0.0,
		}))
		Expect(b).To(ContainSubstring(`"stats":{"size":0,"size_actual":0,"size_utilized":0,"size_kb":0,"size_kb_actual":0,"size_kb_utilized":0,"num_objects":0}}`))

		res = fx.get("/admin/user", "admin")
		Expect(res.StatusCode).To(Equal(400))
		Expect(errCode(res)).To(Equal("InvalidArgument"))
		res = fx.get("/admin/user?uid=nosuch", "admin")
		Expect(res.StatusCode).To(Equal(404))
		Expect(errCode(res)).To(Equal("NoSuchUser"))
	})
	It("POST /admin/user modifies and honors the Squid/Tentacle placement encodings", func() {
		Expect(fx.request(http.MethodPut, "/admin/user?uid=leseb&display-name=L", "admin").StatusCode).To(Equal(200))
		res := fx.request(http.MethodPost, "/admin/user?uid=leseb&default-placement=default-placement%2FFOO&max-buckets=-5&email=new%40example.com&placement-tags=a,,b", "admin")
		Expect(res.StatusCode).To(Equal(200))
		u := userDoc(body(res))
		Expect(u).To(HaveKeyWithValue("default_placement", "default-placement"))
		Expect(u).To(HaveKeyWithValue("default_storage_class", "FOO"))
		Expect(u).To(HaveKeyWithValue("max_buckets", -1.0), "a negative max-buckets is -1")
		Expect(u).To(HaveKeyWithValue("email", "new@example.com"))
		Expect(u).To(HaveKeyWithValue("display_name", "L"))
		u = userDoc(body(fx.get("/admin/user?uid=leseb", "admin")))
		Expect(u["placement_tags"]).To(Equal([]any{"a", "b"}), "persisted, empty tags dropped")
		Expect(fx.request(http.MethodPost, "/admin/user?uid=leseb&default-placement=nosuch", "admin").StatusCode).To(Equal(400))

		fx = newFixtureAt(denc.Tentacle, admin.Config{})
		Expect(fx.request(http.MethodPut, "/admin/user?uid=leseb&display-name=L", "admin").StatusCode).To(Equal(200))
		res = fx.request(http.MethodPost, "/admin/user?uid=leseb&default-placement=default-placement&default-storage-class=FOO&max-buckets=-1", "admin")
		Expect(res.StatusCode).To(Equal(200))
		u = userDoc(body(res))
		Expect(u).To(HaveKeyWithValue("default_placement", "default-placement"))
		Expect(u).To(HaveKeyWithValue("default_storage_class", "FOO"))
		Expect(u).To(HaveKeyWithValue("max_buckets", -1.0))
		res = fx.request(http.MethodPost, "/admin/user?uid=leseb&default-placement=default-placement%2FFOO", "admin")
		Expect(res.StatusCode).To(Equal(400), "Tentacle takes the whole value as the name")
	})
	It("DELETE /admin/user removes, 409 with buckets unless purge-data", func() {
		ctx := context.Background()
		Expect(fx.request(http.MethodPut, "/admin/user?uid=leseb&display-name=L", "admin").StatusCode).To(Equal(200))
		_, err := fx.store.CreateBucket(ctx, op.CreateBucketParams{Name: "b1", Owner: meta.UserOwner(meta.UserID{ID: "leseb"})})
		Expect(err).NotTo(HaveOccurred())
		res := fx.request(http.MethodDelete, "/admin/user?uid=leseb", "admin")
		Expect(res.StatusCode).To(Equal(409))
		Expect(errCode(res)).To(Equal("BucketAlreadyExists"))
		res = fx.request(http.MethodDelete, "/admin/user?uid=leseb&purge-data=true", "admin")
		Expect(res.StatusCode).To(Equal(200))
		Expect(res.Header.Get("Content-Length")).To(Equal("0"))
		Expect(body(res)).To(BeEmpty())
		res = fx.get("/admin/user?uid=leseb", "admin")
		Expect(res.StatusCode).To(Equal(404))
		Expect(errCode(res)).To(Equal("NoSuchUser"))
		_, err = fx.store.GetBucket(ctx, "", "b1")
		Expect(err).To(MatchError(op.ErrNoSuchBucket))
	})
	It("409 UserAlreadyExists for an existing uid", func() {
		res := fx.request(http.MethodPut, "/admin/user?uid=admin&display-name=Admin+user", "admin")
		Expect(res.StatusCode).To(Equal(409))
		Expect(errCode(res)).To(Equal("UserAlreadyExists"))
	})
	It("rejects system=true from a non-system caller", func() {
		res := fx.request(http.MethodPut, "/admin/user?uid=s&display-name=s&system=true", "admin")
		Expect(res.StatusCode).To(Equal(400))
		Expect(errCode(res)).To(Equal("InvalidArgument"))
		Expect(fx.request(http.MethodPut, "/admin/user?uid=s&display-name=s&system=false", "admin").StatusCode).To(Equal(200))
	})

	Describe("security", func() {
		const secret = "wJalrXUtnFEMI-K7MDENG-bPxRfiCYEXAMPLEKEY"
		var logs *bytes.Buffer
		BeforeEach(func() {
			logs = &bytes.Buffer{}
			old := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(logs, nil)))
			DeferCleanup(func() { slog.SetDefault(old) })
		})

		It("answers a caller without the users cap the same whether or not the uid exists", func() {
			reqID := regexp.MustCompile(`tx[0-9a-f-]+[0-9a-z-]*`)
			existing := fx.get("/admin/user?uid=admin", "nocaps")
			missing := fx.get("/admin/user?uid=nosuch", "nocaps")
			Expect(existing.StatusCode).To(Equal(403))
			Expect(missing.StatusCode).To(Equal(403))
			Expect(reqID.ReplaceAllString(body(existing), "")).To(Equal(reqID.ReplaceAllString(body(missing), "")))
			for _, req := range []struct{ method, path string }{
				{http.MethodPut, "/admin/user?uid=admin&display-name=A&system=true"},
				{http.MethodPut, "/admin/user?uid=new&display-name=N&default-placement=nosuch"},
				{http.MethodPost, "/admin/user?uid=admin&email=x%40y"},
				{http.MethodPost, "/admin/user?uid=nosuch"},
				{http.MethodDelete, "/admin/user?uid=admin&purge-data=true"},
				{http.MethodDelete, "/admin/user?uid=nosuch"},
			} {
				res := fx.request(req.method, req.path, "nocaps")
				Expect(res.StatusCode).To(Equal(403), req.method+" "+req.path)
				Expect(errCode(res)).To(Equal("AccessDenied"), req.method+" "+req.path)
			}
			_, err := fx.store.GetUser(context.Background(), meta.UserID{ID: "admin"})
			Expect(err).NotTo(HaveOccurred(), "the removal was refused")
			_, err = fx.store.GetUser(context.Background(), meta.UserID{ID: "new"})
			Expect(err).To(MatchError(op.ErrNoSuchUser))
		})
		It("lets only a system requester set the system flag, and no argument set the admin flag", func() {
			ctx := context.Background()
			sys := meta.NewUserInfo()
			sys.UserID = meta.UserID{ID: "sysop"}
			sys.System = 1
			Expect(sys.Caps.AddString("users=*")).To(Succeed())
			fx.store.AddUser(sys)
			Expect(fx.request(http.MethodPut, "/admin/user?uid=u&display-name=U", "admin").StatusCode).To(Equal(200))
			res := fx.request(http.MethodPost, "/admin/user?uid=u&system=true", "admin")
			Expect(res.StatusCode).To(Equal(400), "modify, from a non-system requester")
			rec, err := fx.store.GetUser(ctx, meta.UserID{ID: "u"})
			Expect(err).NotTo(HaveOccurred())
			Expect(rec.Info.System).To(BeZero())
			u := userDoc(body(fx.request(http.MethodPost, "/admin/user?uid=u&system=true", "sysop")))
			Expect(u).To(HaveKeyWithValue("system", true), "a system requester")
			u = userDoc(body(fx.request(http.MethodPut, "/admin/user?uid=v&display-name=V&admin=true", "admin")))
			Expect(u).To(HaveKeyWithValue("admin", false), "radosgw's REST takes no admin argument")
			u = userDoc(body(fx.request(http.MethodPost, "/admin/user?uid=v&admin=1", "sysop")))
			Expect(u).To(HaveKeyWithValue("admin", false))
		})
		It("withholds the Swift TempURL keys with the other keys", func() {
			info := meta.NewUserInfo()
			info.UserID = meta.UserID{ID: "swifty"}
			info.TempURLKeys = map[int32]string{0: "tempurl-" + secret, 1: "second-" + secret}
			fx.store.AddUser(info)
			addUser("reader", "user-info-without-keys=read")
			res := fx.get("/admin/user?uid=swifty", "reader")
			Expect(res.StatusCode).To(Equal(200))
			b := body(res)
			Expect(b).NotTo(ContainSubstring(secret))
			Expect(userDoc(b)).NotTo(HaveKey("temp_url_keys"), "omitted as keys and swift_keys are")
			res = fx.get("/admin/user?uid=swifty&format=xml", "reader")
			Expect(body(res)).NotTo(ContainSubstring(secret))
			u := userDoc(body(fx.get("/admin/user?uid=swifty", "admin")))
			Expect(u["temp_url_keys"]).To(Equal([]any{
				map[string]any{"key": 0.0, "val": "tempurl-" + secret},
				map[string]any{"key": 1.0, "val": "second-" + secret},
			}), "a users=read caller sees them, as radosgw shows them")
		})
		It("logs no access key when its index read fails", func() {
			fx.env.Users = failingKeyRead{Store: fx.store}
			res := fx.get("/admin/user?access-key=AKIAREADFAIL", "admin")
			Expect(res.StatusCode).To(Equal(500))
			b := body(res)
			res = fx.request(http.MethodPut, "/admin/user?uid=u&display-name=U&access-key=AKIAREADFAIL&secret-key=s", "admin")
			Expect(res.StatusCode).To(Equal(500))
			b += body(res)
			Expect(b).NotTo(ContainSubstring("AKIAREADFAIL"))
			Expect(logs.String()).NotTo(ContainSubstring("AKIAREADFAIL"))
			Expect(logs.String()).To(ContainSubstring(`"code":"InternalError"`))
		})
		It("logs no email or access key when a user's index write fails", func() {
			fx.env.Users = failingIndexWrite{Store: fx.store}
			res := fx.request(http.MethodPut, "/admin/user?uid=u&display-name=U&email=victim%40example.com&access-key=AKIAINDEXED&secret-key="+secret, "admin")
			Expect(res.StatusCode).To(Equal(500))
			b := body(res)
			Expect(b).To(HavePrefix(`{"Code":"InternalError",`))
			res = fx.request(http.MethodPost, "/admin/user?uid=admin&email=victim%40example.com&access-key=AKIAINDEXED&secret-key="+secret, "admin")
			Expect(res.StatusCode).To(Equal(500))
			b += body(res)
			for _, s := range []string{"victim@example.com", "AKIAINDEXED", secret} {
				Expect(b).NotTo(ContainSubstring(s))
				Expect(logs.String()).NotTo(ContainSubstring(s))
			}
			Expect(logs.String()).To(ContainSubstring(`"code":"InternalError"`), "the failure is logged, by its code")
		})
		It("never echoes a secret it refused, and logs no credential", func() {
			Expect(fx.request(http.MethodPut, "/admin/user?uid=holder&display-name=H&access-key=AKIAHELD&secret-key="+secret, "admin").StatusCode).To(Equal(200))
			res := fx.request(http.MethodPut, "/admin/user?uid=thief&display-name=T&email=t%40example.com&access-key=AKIAHELD&secret-key=other-"+secret, "admin")
			Expect(res.StatusCode).To(Equal(409))
			b := body(res)
			Expect(b).To(HavePrefix(`{"Code":"KeyExists",`))
			Expect(b).NotTo(ContainSubstring("AKIAHELD"))
			Expect(b).NotTo(ContainSubstring(secret))
			Expect(b).NotTo(ContainSubstring("t@example.com"))
			res = fx.request(http.MethodPost, "/admin/user?uid=admin&access-key=AKIAHELD&secret-key=x", "admin")
			Expect(res.StatusCode).To(Equal(409))
			Expect(body(res)).NotTo(ContainSubstring("AKIAHELD"))
			Expect(logs.String()).NotTo(ContainSubstring(secret))
			Expect(logs.String()).NotTo(ContainSubstring("AKIAHELD"))
		})
	})
})

// failingIndexWrite is the memstore's user store failing every PutUser as
// the driver fails an index write: an InternalError whose text names the
// index objects, the request's email and access key.
type failingIndexWrite struct{ *memstore.Store }

func (f failingIndexWrite) PutUser(_ context.Context, rec *op.UserRecord, _ op.PutUserOptions) error {
	var keys []string
	for id := range rec.Info.AccessKeys {
		keys = append(keys, id)
	}
	return fmt.Errorf("writing users.email/%s and users.keys/%v: %w", rec.Info.Email, keys, op.ErrInternalError)
}

// failingKeyRead is the memstore's user store failing every read through
// the access key index, with an error that names the key.
type failingKeyRead struct{ *memstore.Store }

func (failingKeyRead) GetUserByAccessKey(_ context.Context, key string) (*op.UserRecord, error) {
	return nil, fmt.Errorf("reading users.keys/%s: %w", key, op.ErrInternalError)
}
