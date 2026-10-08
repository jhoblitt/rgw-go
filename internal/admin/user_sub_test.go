package admin_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/admin"
	"github.com/jhoblitt/rgw-go/internal/meta"
)

// jsonArray decodes a JSON array document.
func jsonArray(b string) []map[string]any {
	GinkgoHelper()
	var v []map[string]any
	Expect(json.Unmarshal([]byte(b), &v)).To(Succeed(), b)
	return v
}

// chunked is a body net/http can only send chunked.
func chunked(s string) io.Reader { return io.MultiReader(strings.NewReader(s)) }

var _ = Describe("admin user key, subuser, caps and quota routes", func() {
	const (
		keyDoc   = `\{"user":"[^"]+","access_key":"[0-9A-Z]{20}","secret_key":"[A-Za-z0-9]{40}","active":true\}`
		quotaDoc = `{"enabled":false,"check_on_raw":false,"max_size":-1,"max_size_kb":0,"max_objects":-1}`
	)
	var fx *fixture
	BeforeEach(func() {
		fx = newFixture(admin.Config{})
		Expect(fx.request(http.MethodPut, "/admin/user?uid=leseb&display-name=L", "admin").StatusCode).To(Equal(200))
	})
	stored := func(uid string) meta.UserInfo {
		GinkgoHelper()
		rec, err := fx.store.GetUser(context.Background(), meta.ParseUserID(uid))
		Expect(err).NotTo(HaveOccurred())
		return rec.Info
	}

	It("PUT and DELETE /admin/user?key create and remove S3 keys, answering the user's keys", func() {
		res := fx.request(http.MethodPut, "/admin/user?key&uid=leseb", "admin")
		Expect(res.StatusCode).To(Equal(200))
		Expect(res.Header.Get("Content-Type")).To(Equal("application/json"))
		b := body(res)
		Expect(b).To(MatchRegexp(`^\[` + keyDoc + `,` + keyDoc + `\]$`))
		Expect(jsonArray(b)).To(HaveEach(HaveKeyWithValue("user", "leseb")))
		b = body(fx.request(http.MethodPut, "/admin/user?key&uid=leseb&access-key=HDNEZQXZAA6NIWOBOL0U", "admin"))
		keys := jsonArray(b)
		Expect(keys).To(HaveLen(3), "generate-key defaults to true and generates only the secret")
		Expect(keys).To(ContainElement(HaveKeyWithValue("access_key", "HDNEZQXZAA6NIWOBOL0U")))
		res = fx.request(http.MethodDelete, "/admin/user?key&uid=leseb&access-key=HDNEZQXZAA6NIWOBOL0U", "admin")
		Expect(res.StatusCode).To(Equal(200))
		Expect(res.Header.Get("Content-Length")).To(Equal("0"))
		Expect(body(res)).To(BeEmpty())
		Expect(stored("leseb").AccessKeys).To(HaveLen(2))
		res = fx.request(http.MethodDelete, "/admin/user?key&uid=leseb&access-key=HDNEZQXZAA6NIWOBOL0U", "admin")
		Expect(res.StatusCode).To(Equal(403))
		Expect(errCode(res)).To(Equal("InvalidAccessKeyId"))
		res = fx.request(http.MethodPut, "/admin/user?key&uid=leseb&access-key=AKIAX&generate-key=false", "admin")
		Expect(res.StatusCode).To(Equal(400))
		Expect(errCode(res)).To(Equal("InvalidSecretKey"))
		res = fx.request(http.MethodPut, "/admin/user?key&uid=nosuch", "admin")
		Expect(res.StatusCode).To(Equal(404))
		Expect(errCode(res)).To(Equal("NoSuchUser"))
	})
	It("answers a Swift key create with the user's Swift keys and sets the active it is given", func() {
		b := body(fx.request(http.MethodPut, "/admin/user?key&uid=leseb&subuser=sw&key-type=swift", "admin"))
		Expect(b).To(MatchRegexp(`^\[\{"user":"leseb:sw","secret_key":"[A-Za-z0-9]{40}","active":true\}\]$`))
		Expect(stored("leseb").SwiftKeys).To(HaveKey("leseb:sw"))
		b = body(fx.request(http.MethodPut, "/admin/user?key&uid=leseb&access-key=AKIAOFF&secret-key=s&active=false", "admin"))
		Expect(jsonArray(b)).To(ContainElement(And(HaveKeyWithValue("access_key", "AKIAOFF"), HaveKeyWithValue("active", false))))
		b = body(fx.request(http.MethodPut, "/admin/user?key&uid=leseb&access-key=AKIAOFF&generate-key=false&active=false", "admin"))
		Expect(jsonArray(b)).To(ContainElement(And(HaveKeyWithValue("access_key", "AKIAOFF"), HaveKeyWithValue("active", false))))
		Expect(stored("leseb").AccessKeys["AKIAOFF"].Secret).To(Equal("s"))
	})
	It("creates, modifies and removes a subuser as Rook and go-ceph call it", func() {
		res := fx.request(http.MethodPut, "/admin/user?uid=leseb&subuser=foo&access=readwrite&key-type=s3&access-key=SUBUSER_ACCESS_KEY&secret-key=SUBUSER_SECRET_KEY&generate-secret=false&gen-access-key=false", "admin")
		Expect(res.StatusCode).To(Equal(200))
		Expect(body(res)).To(Equal(`[{"id":"leseb:foo","permissions":"read-write"}]`))
		u := userDoc(body(fx.get("/admin/user?uid=leseb", "admin")))
		Expect(u["subusers"]).To(Equal([]any{map[string]any{"id": "leseb:foo", "permissions": "read-write"}}))
		Expect(u["swift_keys"]).To(BeEmpty())
		Expect(u["keys"]).To(ContainElement(And(
			HaveKeyWithValue("user", "leseb:foo"),
			HaveKeyWithValue("access_key", "SUBUSER_ACCESS_KEY"),
			HaveKeyWithValue("secret_key", "SUBUSER_SECRET_KEY"),
		)))
		res = fx.request(http.MethodPost, "/admin/user?uid=leseb&subuser=foo&access=read", "admin")
		Expect(res.StatusCode).To(Equal(200))
		Expect(body(res)).To(Equal(`[{"id":"leseb:foo","permissions":"read"}]`))
		res = fx.request(http.MethodPost, "/admin/user?uid=leseb&subuser=foo", "admin")
		Expect(body(res)).To(Equal(`[{"id":"leseb:foo","permissions":"<none>"}]`), "RGWOp_Subuser_Modify always sets the access it reads")
		res = fx.request(http.MethodDelete, "/admin/user?uid=leseb&subuser=foo", "admin")
		Expect(res.StatusCode).To(Equal(200))
		Expect(body(res)).To(BeEmpty())
		u = userDoc(body(fx.get("/admin/user?uid=leseb", "admin")))
		Expect(u["subusers"]).To(BeEmpty())
		Expect(u["keys"]).NotTo(ContainElement(HaveKeyWithValue("access_key", "SUBUSER_ACCESS_KEY")))
		res = fx.request(http.MethodDelete, "/admin/user?uid=leseb&subuser=foo", "admin")
		Expect(res.StatusCode).To(Equal(404))
		Expect(errCode(res)).To(Equal("NoSuchSubUser"))
		res = fx.request(http.MethodPut, "/admin/user?uid=leseb&subuser=bad&access=read-write", "admin")
		Expect(res.StatusCode).To(Equal(400))
		Expect(errCode(res)).To(Equal("InvalidArgument"))
		res = fx.request(http.MethodPut, "/admin/user?uid=leseb&subuser=sw", "admin")
		Expect(body(res)).To(Equal(`[{"id":"leseb:sw","permissions":"<none>"}]`))
		Expect(stored("leseb").SwiftKeys).To(HaveKey("leseb:sw"), "Swift by default")
	})
	It("adds and removes caps, answering the caps array", func() {
		Expect(fx.request(http.MethodPut, "/admin/user?uid=test&display-name=T", "admin").StatusCode).To(Equal(200))
		res := fx.request(http.MethodPut, "/admin/user?caps&uid=test&user-caps=users%3Dread", "admin")
		Expect(res.StatusCode).To(Equal(200))
		Expect(body(res)).To(Equal(`[{"type":"users","perm":"read"}]`))
		res = fx.request(http.MethodDelete, "/admin/user?caps&uid=test&user-caps=users%3Dread", "admin")
		Expect(res.StatusCode).To(Equal(200))
		Expect(body(res)).To(Equal(`[]`))
		res = fx.request(http.MethodPut, "/admin/user?caps&uid=test", "admin")
		Expect(res.StatusCode).To(Equal(400))
		Expect(errCode(res)).To(Equal("InvalidCapability"))
	})
	It("gets and sets quotas through the arguments and through a body", func() {
		res := fx.get("/admin/user?quota&uid=leseb&quota-type=user", "admin")
		Expect(res.StatusCode).To(Equal(200))
		Expect(body(res)).To(Equal(quotaDoc))
		res = fx.request(http.MethodPut, "/admin/user?quota&uid=leseb&quota-type=user&max-objects=100&enabled=true", "admin")
		Expect(res.StatusCode).To(Equal(200))
		Expect(res.Header.Get("Content-Length")).To(Equal("0"))
		Expect(body(fx.get("/admin/user?quota&uid=leseb&quota-type=user", "admin"))).To(Equal(
			`{"enabled":true,"check_on_raw":false,"max_size":-1,"max_size_kb":0,"max_objects":100}`))
		quotas := `{"user_quota":{"max_size_kb":4096,"max_objects":-1,"enabled":false},"bucket_quota":{"max_size_kb":1024,"max_objects":-1,"enabled":true}}`
		res = fx.requestBody(http.MethodPut, "/admin/user?quota&uid=leseb", "admin", strings.NewReader(quotas))
		Expect(res.StatusCode).To(Equal(200))
		Expect(body(fx.get("/admin/user?quota&uid=leseb", "admin"))).To(Equal(
			`{"bucket_quota":{"enabled":true,"check_on_raw":false,"max_size":1048576,"max_size_kb":1024,"max_objects":-1},` +
				`"user_quota":{"enabled":false,"check_on_raw":false,"max_size":4194304,"max_size_kb":4096,"max_objects":-1}}`))
		res = fx.requestBody(http.MethodPut, "/admin/user?quota&uid=leseb&quota-type=bucket", "admin", chunked(`{"max_objects":7}`))
		Expect(res.StatusCode).To(Equal(200))
		Expect(stored("leseb").BucketQuota).To(Equal(meta.Quota{MaxObjects: 7}), "a chunked body, every absent field zero")
		res = fx.request(http.MethodPut, "/admin/user?quota&uid=leseb", "admin")
		Expect(res.StatusCode).To(Equal(400))
		Expect(errCode(res)).To(Equal("InvalidArgument"))
		res = fx.requestBody(http.MethodPut, "/admin/user?quota&uid=leseb&quota-type=user", "admin", strings.NewReader(`{"pad":"`+strings.Repeat("x", 1024)+`"}`))
		Expect(res.StatusCode).To(Equal(416))
		Expect(errCode(res)).To(Equal("InvalidRange"))
		res = fx.get("/admin/user?quota&uid=leseb&quota-type=all", "admin")
		Expect(res.StatusCode).To(Equal(400))
		res = fx.get("/admin/user?quota", "admin")
		Expect(res.StatusCode).To(Equal(400))
		res = fx.get("/admin/user?quota&uid=nosuch", "admin")
		Expect(res.StatusCode).To(Equal(404))
		Expect(errCode(res)).To(Equal("NoSuchUser"))
	})
	It("refuses a quota argument it cannot parse, or a size it cannot store, changing nothing", func() {
		Expect(fx.request(http.MethodPut, "/admin/user?quota&uid=leseb&quota-type=user&max-objects=5&max-size=4096&enabled=true", "admin").StatusCode).To(Equal(200))
		before := stored("leseb")
		for _, args := range []string{
			"max-objects=10k", "max-size=1G", "max-size-kb=1.5", "max-size=1&max-size-kb=bad",
			"enabled=maybe", "max-size-kb=9007199254740992", "max-size=-2",
		} {
			res := fx.request(http.MethodPut, "/admin/user?quota&uid=leseb&quota-type=user&"+args, "admin")
			Expect(res.StatusCode).To(Equal(400), args)
			Expect(errCode(res)).To(Equal("InvalidArgument"), args)
			Expect(fx.request(http.MethodPut, "/admin/user?quota&uid=leseb&quota-type=user&"+args, "nocaps").StatusCode).To(Equal(403), args+": the caps come first")
		}
		Expect(stored("leseb")).To(Equal(before))
		Expect(fx.request(http.MethodPut, "/admin/user?quota&uid=leseb&quota-type=user&max-size-kb=8&max-size=-1", "admin").StatusCode).To(Equal(200))
		Expect(stored("leseb").UserQuota.MaxSize).To(Equal(int64(8192)))
	})
	It("refuses a quota argument it cannot parse even when a body is sent", func() {
		before := stored("leseb")
		res := fx.requestBody(http.MethodPut, "/admin/user?quota&uid=leseb&quota-type=user&max-objects=10k", "admin",
			strings.NewReader(`{"max_objects":5,"enabled":true}`))
		Expect(res.StatusCode).To(Equal(400))
		Expect(errCode(res)).To(Equal("InvalidArgument"))
		Expect(stored("leseb")).To(Equal(before))
	})
	It("refuses a boolean argument it cannot parse, so a typo revives no key and lifts no suspension", func() {
		Expect(fx.request(http.MethodPut, "/admin/user?key&uid=leseb&access-key=AKIAOFF&secret-key=s&active=false", "admin").StatusCode).To(Equal(200))
		Expect(fx.request(http.MethodPut, "/admin/user?uid=sus&display-name=S&suspended=true", "admin").StatusCode).To(Equal(200))
		before := map[string]meta.UserInfo{"leseb": stored("leseb"), "sus": stored("sus")}
		for _, rt := range []struct{ method, path string }{
			{http.MethodPut, "/admin/user?key&uid=leseb&access-key=AKIAOFF&active=flase"},
			{http.MethodPut, "/admin/user?key&uid=leseb&access-key=AKIAOFF&generate-key=false&active="},
			{http.MethodPut, "/admin/user?key&uid=leseb&access-key=AKIAOFF&generate-key=nope"},
			{http.MethodPost, "/admin/user?uid=sus&suspended=ture"},
			{http.MethodPost, "/admin/user?uid=sus&system=yes"},
			{http.MethodPost, "/admin/user?uid=sus&account-root=no"},
			{http.MethodPost, "/admin/user?uid=sus&generate-key=y"},
			{http.MethodPut, "/admin/user?uid=new&display-name=N&exclusive=x"},
			{http.MethodPut, "/admin/user?uid=new&display-name=N&suspended=x"},
			{http.MethodDelete, "/admin/user?uid=sus&purge-data=x"},
			{http.MethodPut, "/admin/user?uid=leseb&subuser=s&generate-secret=x"},
			{http.MethodPut, "/admin/user?uid=leseb&subuser=s&gen-access-key=x"},
			{http.MethodPost, "/admin/user?uid=leseb&subuser=s&generate-secret=x"},
			{http.MethodDelete, "/admin/user?uid=leseb&subuser=s&purge-keys=x"},
		} {
			res := fx.request(rt.method, rt.path, "admin")
			Expect(res.StatusCode).To(Equal(400), rt.method+" "+rt.path)
			Expect(errCode(res)).To(Equal("InvalidArgument"), rt.method+" "+rt.path)
			Expect(fx.request(rt.method, rt.path, "nocaps").StatusCode).To(Equal(403), rt.method+" "+rt.path+": the caps come first")
		}
		Expect(stored("leseb")).To(Equal(before["leseb"]))
		Expect(stored("sus")).To(Equal(before["sus"]))
		Expect(stored("leseb").AccessKeys["AKIAOFF"].Active).To(BeFalse())
		_, err := fx.store.GetUser(context.Background(), meta.UserID{ID: "new"})
		Expect(err).To(HaveOccurred(), "no user created")
	})
	It("renders the XML documents radosgw renders", func() {
		Expect(fx.request(http.MethodPut, "/admin/user?uid=test&display-name=T", "admin").StatusCode).To(Equal(200))
		res := fx.request(http.MethodPut, "/admin/user?caps&uid=test&user-caps=users%3Dread&format=xml", "admin")
		Expect(res.Header.Get("Content-Type")).To(Equal("application/xml"))
		Expect(body(res)).To(Equal(`<?xml version="1.0" encoding="UTF-8"?><caps><cap><type>users</type><perm>read</perm></cap></caps>`))
		res = fx.request(http.MethodPut, "/admin/user?uid=leseb&subuser=foo&access=readwrite&key-type=s3&access-key=SUBUSER_ACCESS_KEY&secret-key=SUBUSER_SECRET_KEY&generate-secret=false&gen-access-key=false&format=xml", "admin")
		Expect(body(res)).To(Equal(`<?xml version="1.0" encoding="UTF-8"?><subusers><user><id>leseb:foo</id><permissions>read-write</permissions></user></subusers>`))
		Expect(fx.request(http.MethodPut, "/admin/user?quota&uid=leseb&quota-type=user&max-objects=100&enabled=true", "admin").StatusCode).To(Equal(200))
		userQuota := `<enabled>true</enabled><check_on_raw>false</check_on_raw><max_size>-1</max_size><max_size_kb>0</max_size_kb><max_objects>100</max_objects>`
		bucketQuota := `<enabled>false</enabled><check_on_raw>false</check_on_raw><max_size>-1</max_size><max_size_kb>0</max_size_kb><max_objects>-1</max_objects>`
		Expect(body(fx.get("/admin/user?quota&uid=leseb&quota-type=user&format=xml", "admin"))).To(Equal(
			`<?xml version="1.0" encoding="UTF-8"?><user_quota>` + userQuota + `</user_quota>`))
		Expect(body(fx.get("/admin/user?quota&uid=leseb&quota-type=bucket&format=xml", "admin"))).To(Equal(
			`<?xml version="1.0" encoding="UTF-8"?><bucket_quota>` + bucketQuota + `</bucket_quota>`))
		Expect(body(fx.get("/admin/user?quota&uid=leseb&format=xml", "admin"))).To(Equal(
			`<?xml version="1.0" encoding="UTF-8"?><quota><bucket_quota>` + bucketQuota + `</bucket_quota><user_quota>` + userQuota + `</user_quota></quota>`))
		b := body(fx.request(http.MethodPut, "/admin/user?key&uid=leseb&format=xml", "admin"))
		Expect(b).To(MatchRegexp(`^<\?xml version="1\.0" encoding="UTF-8"\?><keys>(<key><user>leseb</user><access_key>[0-9A-Z]{20}</access_key><secret_key>[A-Za-z0-9]{40}</secret_key><active>true</active></key>|<key><user>leseb:foo</user><access_key>SUBUSER_ACCESS_KEY</access_key><secret_key>SUBUSER_SECRET_KEY</secret_key><active>true</active></key>){3}</keys>$`))
	})
	It("takes the first sub-resource in the query", func() {
		b := body(fx.request(http.MethodPut, "/admin/user?key&quota&uid=leseb", "admin"))
		Expect(b).To(MatchRegexp(`^\[`+keyDoc+`,`+keyDoc+`\]$`), "the key route answers the keys")
		Expect(stored("leseb").AccessKeys).To(HaveLen(2))
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
		It("refuses a caller without the users cap alike whether or not the uid exists, changing nothing", func() {
			routes := []struct{ method, path string }{
				{http.MethodPut, "/admin/user?key&uid=%s&access-key=AKIANEW&secret-key=" + secret},
				{http.MethodDelete, "/admin/user?key&uid=%s&access-key=AKIANEW"},
				{http.MethodPut, "/admin/user?subuser=s&uid=%s"},
				{http.MethodPost, "/admin/user?subuser=s&uid=%s&access=full"},
				{http.MethodDelete, "/admin/user?subuser=s&uid=%s"},
				{http.MethodPut, "/admin/user?caps&uid=%s&user-caps=users%3D%2A"},
				{http.MethodDelete, "/admin/user?caps&uid=%s&user-caps=users%3Dread"},
				{http.MethodGet, "/admin/user?quota&uid=%s"},
				{http.MethodPut, "/admin/user?quota&uid=%s&quota-type=user&enabled=false"},
			}
			reqID := regexp.MustCompile(`tx[0-9a-f-]+[0-9a-z-]*`)
			before := stored("admin")
			for _, rt := range routes {
				existing := fx.request(rt.method, strings.ReplaceAll(rt.path, "%s", "admin"), "nocaps")
				missing := fx.request(rt.method, strings.ReplaceAll(rt.path, "%s", "nosuch"), "nocaps")
				Expect(existing.StatusCode).To(Equal(403), rt.method+" "+rt.path)
				Expect(missing.StatusCode).To(Equal(403), rt.method+" "+rt.path)
				b := body(existing)
				Expect(b).To(HavePrefix(`{"Code":"AccessDenied",`), rt.method+" "+rt.path)
				Expect(reqID.ReplaceAllString(b, "")).To(Equal(reqID.ReplaceAllString(body(missing), "")))
			}
			Expect(stored("admin")).To(Equal(before))
		})
		It("never echoes a key or secret it refused, and logs none when the write fails", func() {
			Expect(fx.request(http.MethodPut, "/admin/user?key&uid=admin&access-key=AKIAHELD&secret-key="+secret, "admin").StatusCode).To(Equal(200))
			res := fx.request(http.MethodPut, "/admin/user?key&uid=leseb&access-key=AKIAHELD&secret-key=other-"+secret, "admin")
			Expect(res.StatusCode).To(Equal(409))
			var b strings.Builder
			b.WriteString(body(res))
			Expect(b.String()).To(HavePrefix(`{"Code":"KeyExists",`))
			fx.env.Users = failingIndexWrite{Store: fx.store}
			for _, path := range []string{
				"/admin/user?key&uid=leseb&access-key=AKIAINDEXED&secret-key=" + secret,
				"/admin/user?uid=leseb&subuser=sub&key-type=s3&access-key=AKIAINDEXED&secret-key=" + secret,
			} {
				res = fx.request(http.MethodPut, path, "admin")
				Expect(res.StatusCode).To(Equal(500))
				b.WriteString(body(res))
			}
			for _, s := range []string{"AKIAINDEXED", "AKIAHELD", secret} {
				Expect(b.String()).NotTo(ContainSubstring(s))
				Expect(logs.String()).NotTo(ContainSubstring(s))
			}
			Expect(logs.String()).To(ContainSubstring(`"code":"InternalError"`))
		})
		It("puts no key in the subuser, caps or quota documents", func() {
			Expect(fx.request(http.MethodPut, "/admin/user?key&uid=leseb&access-key=AKIAMINE&secret-key="+secret, "admin").StatusCode).To(Equal(200))
			var b strings.Builder
			for _, rt := range []struct{ method, path string }{
				{http.MethodPut, "/admin/user?uid=leseb&subuser=s&key-type=s3&access-key=AKIASUB&secret-key=" + secret},
				{http.MethodPost, "/admin/user?uid=leseb&subuser=s&secret-key=" + secret},
				{http.MethodPut, "/admin/user?caps&uid=leseb&user-caps=usage%3Dread"},
				{http.MethodGet, "/admin/user?quota&uid=leseb"},
			} {
				res := fx.request(rt.method, rt.path, "admin")
				Expect(res.StatusCode).To(Equal(200), rt.path)
				b.WriteString(body(res))
			}
			for _, s := range []string{"AKIAMINE", "AKIASUB", secret} {
				Expect(b.String()).NotTo(ContainSubstring(s))
			}
		})
		It("lets a users=write caller grant itself any cap, as radosgw's design does", func() {
			info := meta.NewUserInfo()
			info.UserID = meta.UserID{ID: "writer"}
			Expect(info.Caps.AddString("users=write")).To(Succeed())
			fx.store.AddUser(info)
			res := fx.request(http.MethodPut, "/admin/user?caps&uid=writer&user-caps=users%3D%2A%3Bbuckets%3D%2A", "writer")
			Expect(res.StatusCode).To(Equal(200))
			Expect(stored("writer").Caps).To(Equal(meta.Caps{"users": meta.CapAll, "buckets": meta.CapAll}))
			reader := meta.NewUserInfo()
			reader.UserID = meta.UserID{ID: "reader"}
			Expect(reader.Caps.AddString("users=read")).To(Succeed())
			fx.store.AddUser(reader)
			res = fx.request(http.MethodPut, "/admin/user?caps&uid=reader&user-caps=users%3D%2A", "reader")
			Expect(res.StatusCode).To(Equal(403))
			Expect(stored("reader").Caps).To(Equal(meta.Caps{"users": meta.CapRead}))
			Expect(fx.get("/admin/user?quota&uid=leseb", "reader").StatusCode).To(Equal(200), "users=read reads quotas")
		})
	})
})
