package admin_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/admin"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// doc decodes a JSON document of any shape.
func doc(b string) any {
	GinkgoHelper()
	var v any
	Expect(json.Unmarshal([]byte(b), &v)).To(Succeed(), b)
	return v
}

var _ = Describe("admin bucket routes", func() {
	var (
		fx           *fixture
		createBucket func(name, owner string) *op.BucketRecord
	)
	setup := func(rel denc.Release) {
		GinkgoHelper()
		fx = newFixtureAt(rel, admin.Config{})
		for _, id := range []string{"test-user1", "test-user2"} {
			info := meta.NewUserInfo()
			info.UserID = meta.UserID{ID: id}
			info.DisplayName = "This is " + id
			fx.store.AddUser(info)
		}
		createBucket("test", "admin")
	}
	createBucket = func(name, owner string) *op.BucketRecord {
		GinkgoHelper()
		o := meta.UserOwner(meta.UserID{ID: owner})
		e := denc.NewEncoder()
		acl.DefaultPolicy(o, "This is "+owner).Encode(e, denc.Squid)
		rec, err := fx.store.CreateBucket(context.Background(), op.CreateBucketParams{
			Name: name, Owner: o, Zonegroup: fx.store.ZoneGroup().ID, Placement: meta.PlacementRule{Name: "default-placement"},
			Attrs: map[string][]byte{meta.AttrACL: e.Bytes()}, Quota: meta.Quota{MaxSize: -1, MaxObjects: -1},
		})
		Expect(err).NotTo(HaveOccurred())
		return rec
	}
	putObject := func(bucket, key string) {
		GinkgoHelper()
		ctx := context.Background()
		rec, err := fx.store.GetBucket(ctx, "", bucket)
		Expect(err).NotTo(HaveOccurred())
		_, err = fx.store.PutObject(ctx, rec, meta.ObjKey{Name: key}, strings.NewReader("hello"), op.PutParams{Size: 5})
		Expect(err).NotTo(HaveOccurred())
	}
	stats := func(path string) map[string]any {
		GinkgoHelper()
		res := fx.get(path, "admin")
		Expect(res.StatusCode).To(Equal(200))
		m, ok := doc(body(res)).(map[string]any)
		Expect(ok).To(BeTrue())
		return m
	}
	BeforeEach(func() { setup(denc.Squid) })

	It("GET /admin/bucket lists the zone's buckets as a bare array of names", func() {
		res := fx.get("/admin/bucket", "admin")
		Expect(res.StatusCode).To(Equal(200))
		Expect(body(res)).To(Equal(`["test"]`))
		res = fx.get("/admin/bucket?format=xml", "admin")
		Expect(body(res)).To(Equal(`<?xml version="1.0" encoding="UTF-8"?><buckets><bucket>test</bucket></buckets>`))
	})
	It("GET /admin/bucket?bucket= answers a missing bucket 404 NoSuchBucket", func() {
		res := fx.get("/admin/bucket?bucket=foo", "admin")
		Expect(res.StatusCode).To(Equal(404))
		Expect(errCode(res)).To(Equal("NoSuchBucket"))
	})
	It("GET /admin/bucket?bucket=test renders bucket_stats in radosgw's order", func() {
		putObject("test", "k1")
		res := fx.get("/admin/bucket?bucket=test", "admin")
		Expect(res.StatusCode).To(Equal(200))
		b := body(res)
		m, ok := doc(b).(map[string]any)
		Expect(ok).To(BeTrue())
		Expect(m).To(HaveKeyWithValue("versioning", "off"))
		Expect(m).To(HaveKeyWithValue("object_lock_enabled", false))
		Expect(m).To(HaveKeyWithValue("owner", "admin"))
		Expect(m).To(HaveKeyWithValue("index_type", "Normal"))
		Expect(m).To(HaveKey("id"))
		Expect(m).To(HaveKey("marker"))
		Expect(m["ver"]).To(MatchRegexp(`^0#`))
		created, ok := m["creation_time"].(string)
		Expect(ok).To(BeTrue())
		ct, err := time.Parse("2006-01-02T15:04:05.000000Z", created)
		Expect(err).NotTo(HaveOccurred())
		Expect(time.Since(ct)).To(BeNumerically("<", time.Minute))
		Expect(m["bucket_quota"]).To(HaveKey("max_size_kb"))
		Expect(m["usage"]).To(HaveKeyWithValue("rgw.main", map[string]any{
			"size": 5.0, "size_actual": 4096.0, "size_utilized": 5.0, "size_kb": 1.0, "size_kb_actual": 4.0, "size_kb_utilized": 1.0, "num_objects": 1.0,
		}))
		Expect(strings.Index(b, `"bucket"`)).To(BeNumerically("<", strings.Index(b, `"tenant"`)))
		Expect(strings.Index(b, `"tenant"`)).To(BeNumerically("<", strings.Index(b, `"versioning"`)))
		Expect(b).To(HavePrefix(`{"bucket":"test","tenant":"","versioning":"off","zonegroup":"default","placement_rule":"default-placement","explicit_placement":{"data_pool":"","data_extra_pool":"","index_pool":""},"id":"`))
		Expect(b).To(ContainSubstring(`"index_type":"Normal","index_generation":0,"object_lock_enabled":false,"mfa_enabled":false,"owner":"admin","num_shards":1,"ver":"0#`))
		for _, k := range []string{"reshard_status", "judge_reshard_lock_time", "read_tracker"} {
			Expect(m).NotTo(HaveKey(k), "Squid has no %s", k)
		}

		res = fx.get("/admin/bucket?bucket=test&format=xml", "admin")
		b = body(res)
		Expect(b).To(HavePrefix(`<?xml version="1.0" encoding="UTF-8"?><stats><bucket>test</bucket><tenant></tenant><versioning>off</versioning><zonegroup>`))
		Expect(b).To(ContainSubstring(`<usage><rgw.main><size>`))
	})
	It("writes Tentacle's reshard fields and read_tracker", func() {
		setup(denc.Tentacle)
		rec, err := fx.store.GetBucket(context.Background(), "", "test")
		Expect(err).NotTo(HaveOccurred())
		b := body(fx.get("/admin/bucket?bucket=test", "admin"))
		Expect(b).To(ContainSubstring(`"index_generation":0,"reshard_status":"None","judge_reshard_lock_time":"0.000000","object_lock_enabled":false`))
		Expect(b).To(HaveSuffix(`,"read_tracker":` + strconv.FormatUint(rec.Version.Ver, 10) + `}`))
	})
	It("GET /admin/bucket?policy renders the policy", func() {
		res := fx.get("/admin/bucket?policy&bucket=foo", "admin")
		Expect(res.StatusCode).To(Equal(404))
		Expect(errCode(res)).To(Equal("NoSuchKey"))
		pol := stats("/admin/bucket?policy&bucket=test")
		Expect(pol).NotTo(HaveKey("policy"), "JSONFormatter drops the name of its outermost section")
		Expect(pol["owner"]).To(HaveKeyWithValue("id", "admin"))
		list, ok := pol["acl"].(map[string]any)
		Expect(ok).To(BeTrue())
		users, ok := list["acl_user_map"].([]any)
		Expect(ok).To(BeTrue())
		Expect(users[0]).To(HaveKeyWithValue("user", "admin"))
		res = fx.get("/admin/bucket?policy&bucket=test&format=xml", "admin")
		Expect(body(res)).To(MatchRegexp(`^` + regexp.QuoteMeta(`<?xml version="1.0" encoding="UTF-8"?><policy><acl><acl_user_map><entry><user>admin</user><acl>15</acl></entry></acl_user_map><acl_group_map></acl_group_map><grant_map><entry><id>admin</id><grant><type><type>0</type></type><id>admin</id><name>`) +
			`[^<]*` + regexp.QuoteMeta(`</name><permission><flags>15</flags></permission></grant></entry></grant_map></acl><owner><id>admin</id><display_name>`) + `[^<]*` + regexp.QuoteMeta(`</display_name></owner></policy>`) + `$`))
	})
	It("PUT /admin/bucket links the bucket to another user, checking the bucket id", func() {
		rec, err := fx.store.GetBucket(context.Background(), "", "test")
		Expect(err).NotTo(HaveOccurred())
		res := fx.request(http.MethodPut, "/admin/bucket?uid=test-user2&bucket-id="+rec.Info.Bucket.ID+"&bucket=test", "admin")
		Expect(res.StatusCode).To(Equal(200))
		Expect(res.Header.Get("Content-Length")).To(Equal("0"))
		Expect(stats("/admin/bucket?bucket=test")).To(HaveKeyWithValue("owner", "test-user2"))
		res = fx.request(http.MethodPut, "/admin/bucket?uid=test-user2&bucket-id=wrong&bucket=test", "admin")
		Expect(res.StatusCode).To(Equal(404))
	})
	It("PUT /admin/bucket renames with new-bucket-name", func() {
		createBucket("initial-name", "test-user1")
		res := fx.request(http.MethodPut, "/admin/bucket?uid=test-user1&bucket=initial-name&new-bucket-name=renamed-name", "admin")
		Expect(res.StatusCode).To(Equal(200))
		res = fx.get("/admin/bucket?bucket=initial-name", "admin")
		Expect(res.StatusCode).To(Equal(404))
		Expect(errCode(res)).To(Equal("NoSuchBucket"))
		Expect(fx.get("/admin/bucket?bucket=renamed-name", "admin").StatusCode).To(Equal(200))
	})
	It("POST /admin/bucket unlinks", func() {
		res := fx.request(http.MethodPost, "/admin/bucket?uid=admin&bucket=test", "admin")
		Expect(res.StatusCode).To(Equal(200))
		res = fx.request(http.MethodPost, "/admin/bucket?bucket=test", "admin")
		Expect(res.StatusCode).To(Equal(400), "requires user or account id")
	})
	It("DELETE /admin/bucket removes, purging when asked", func() {
		putObject("test", "k1")
		res := fx.request(http.MethodDelete, "/admin/bucket?bucket=test", "admin")
		Expect(res.StatusCode).To(Equal(409))
		Expect(errCode(res)).To(Equal("BucketNotEmpty"))
		res = fx.request(http.MethodDelete, "/admin/bucket?bucket=test&purge-objects=true", "admin")
		Expect(res.StatusCode).To(Equal(200))
		res = fx.request(http.MethodDelete, "/admin/bucket?bucket=foo", "admin")
		Expect(res.StatusCode).To(Equal(404))
		Expect(errCode(res)).To(Equal("NoSuchBucket"))
	})
	It("DELETE /admin/bucket with bypass-gc purges without purge-objects", func() {
		putObject("test", "k1")
		res := fx.request(http.MethodDelete, "/admin/bucket?bucket=test&bypass-gc=true", "admin")
		Expect(res.StatusCode).To(Equal(200))
		Expect(res.Header.Get("Content-Length")).To(Equal("0"))
		res = fx.get("/admin/bucket?bucket=test", "admin")
		Expect(res.StatusCode).To(Equal(404))
		Expect(errCode(res)).To(Equal("NoSuchBucket"))
	})
	DescribeTable("refuses a deleting flag it cannot parse or that is empty, removing nothing",
		func(query string) {
			putObject("test", "k1")
			res := fx.request(http.MethodDelete, "/admin/bucket?bucket=test&"+query, "admin")
			Expect(res.StatusCode).To(Equal(400))
			Expect(errCode(res)).To(Equal("InvalidArgument"))
			Expect(fx.get("/admin/bucket?bucket=test", "admin").StatusCode).To(Equal(200))
		},
		Entry("purge-objects=ture", "purge-objects=ture"),
		Entry("an empty purge-objects", "purge-objects="),
		Entry("a bare purge-objects", "purge-objects"),
		Entry("bypass-gc=yes", "bypass-gc=yes"),
		Entry("an empty bypass-gc", "bypass-gc="),
	)
	It("checks the caps before the argument, so a refused caller is refused first", func() {
		res := fx.request(http.MethodDelete, "/admin/bucket?bucket=test&purge-objects=ture", "nocaps")
		Expect(res.StatusCode).To(Equal(403))
	})
	It("PUT /admin/bucket?quota sets the bucket quota", func() {
		res := fx.request(http.MethodPut, "/admin/bucket?quota&uid=admin&bucket=test&max-size-kb=1000000", "admin")
		Expect(res.StatusCode).To(Equal(200))
		Expect(stats("/admin/bucket?bucket=test")["bucket_quota"]).To(HaveKeyWithValue("max_size_kb", 1000000.0))
		Expect(fx.request(http.MethodPut, "/admin/bucket?quota&bucket=test", "admin").StatusCode).To(Equal(400), "no uid")
	})
	DescribeTable("refuses a bucket quota argument that would store garbage or lift the limit",
		func(query, body string) {
			before := stats("/admin/bucket?bucket=test")["bucket_quota"]
			var res *http.Response
			if body == "" {
				res = fx.request(http.MethodPut, "/admin/bucket?quota&uid=admin&bucket=test&"+query, "admin")
			} else {
				res = fx.requestBody(http.MethodPut, "/admin/bucket?quota&uid=admin&bucket=test&"+query, "admin", strings.NewReader(body))
			}
			Expect(res.StatusCode).To(Equal(400))
			Expect(errCode(res)).To(Equal("InvalidArgument"))
			Expect(stats("/admin/bucket?bucket=test")["bucket_quota"]).To(Equal(before))
		},
		Entry("an unparsable max-objects", "max-objects=ten", ""),
		Entry("an unparsable max-size-kb", "max-size-kb=1k", ""),
		Entry("max-size-kb overflowing in bytes", "max-size-kb=9007199254740993", ""),
		Entry("max-size-kb=-1", "max-size-kb=-1", ""),
		Entry("an unparsable enabled", "enabled=ture", ""),
		Entry("a body with an unparsable argument", "max-objects=ten", `{"enabled":true,"max_objects":5}`),
	)
	It("GET /admin/bucket?uid= lists a user's buckets, with stats or paged", func() {
		createBucket("b2", "admin")
		res := fx.get("/admin/bucket?uid=admin&stats=false", "admin")
		Expect(body(res)).To(Equal(`["b2","test"]`))
		arr, ok := doc(body(fx.get("/admin/bucket?uid=admin&stats=true", "admin"))).([]any)
		Expect(ok).To(BeTrue())
		Expect(arr).To(HaveLen(2))
		Expect(arr[0]).To(HaveKeyWithValue("owner", "admin"))
		first, ok := arr[0].(map[string]any)
		Expect(ok).To(BeTrue())
		Expect(first["bucket_quota"]).To(HaveKey("max_size"))
		b := body(fx.get("/admin/bucket?uid=admin&max-entries=1", "admin"))
		Expect(b).To(Equal(`{"buckets":["b2"],"truncated":true,"count":1,"marker":"b2"}`))
		b = body(fx.get("/admin/bucket?uid=admin&max-entries=1&format=xml", "admin"))
		Expect(b).To(MatchRegexp(`^<\?xml version="1\.0" encoding="UTF-8"\?><result><buckets><bucket>[^<]+</bucket></buckets><truncated>true</truncated><count>1</count><marker>[^<]+</marker></result>$`))
	})
	It("DELETE /admin/bucket?object removes an object", func() {
		putObject("test", "k1")
		res := fx.request(http.MethodDelete, "/admin/bucket?object&bucket=test&object=k1", "admin")
		Expect(res.StatusCode).To(Equal(200))
		res = fx.request(http.MethodDelete, "/admin/bucket?object&bucket=test&object=k1", "admin")
		Expect(res.StatusCode).To(Equal(404))
		Expect(errCode(res)).To(Equal("NoSuchKey"))
	})
	It("refuses every bucket route to a caller without the buckets cap", func() {
		for _, rq := range []struct{ method, path string }{
			{http.MethodGet, "/admin/bucket?bucket=test"},
			{http.MethodGet, "/admin/bucket?policy&bucket=test"},
			{http.MethodPut, "/admin/bucket?uid=test-user1&bucket=test"},
			{http.MethodPost, "/admin/bucket?uid=admin&bucket=test"},
			{http.MethodDelete, "/admin/bucket?bucket=test&purge-objects=true"},
			{http.MethodPut, "/admin/bucket?quota&uid=admin&bucket=test&max-objects=1"},
			{http.MethodDelete, "/admin/bucket?object&bucket=test&object=k"},
		} {
			res := fx.request(rq.method, rq.path, "nocaps")
			Expect(res.StatusCode).To(Equal(403), rq.method+" "+rq.path)
			Expect(body(res)).NotTo(ContainSubstring("test"), "an error names no bucket")
		}
		Expect(stats("/admin/bucket?bucket=test")).To(HaveKeyWithValue("owner", "admin"))
	})
	It("never dumps the owner's keys", func() {
		ctx := context.Background()
		u, err := fx.store.GetUser(ctx, meta.UserID{ID: "admin"})
		Expect(err).NotTo(HaveOccurred())
		u.Info.AccessKeys = map[string]meta.AccessKey{"AKIABUCKETOWNER": {ID: "AKIABUCKETOWNER", Secret: "bucketownersecret", Active: true}}
		Expect(fx.store.PutUser(ctx, u, op.PutUserOptions{})).To(Succeed())
		var logs bytes.Buffer
		prev := slog.Default()
		slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
		DeferCleanup(func() { slog.SetDefault(prev) })
		var all strings.Builder
		for _, p := range []string{"/admin/bucket", "/admin/bucket?stats=true", "/admin/bucket?uid=admin&stats=true", "/admin/bucket?bucket=test", "/admin/bucket?policy&bucket=test"} {
			all.WriteString(body(fx.get(p, "admin")))
		}
		all.WriteString(body(fx.request(http.MethodPut, "/admin/bucket?uid=admin&bucket=test", "admin")))
		Expect(all.String()).NotTo(Or(ContainSubstring("AKIABUCKETOWNER"), ContainSubstring("bucketownersecret")))
		Expect(logs.String()).NotTo(Or(ContainSubstring("AKIABUCKETOWNER"), ContainSubstring("bucketownersecret")))
	})
})
