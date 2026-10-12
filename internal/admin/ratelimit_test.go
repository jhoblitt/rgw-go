package admin_test

import (
	"context"
	"net/http"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/admin"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

var _ = Describe("admin ratelimit routes", func() {
	const (
		ones  = `{"max_read_ops":1,"max_write_ops":1,"max_read_bytes":1,"max_write_bytes":1,"enabled":true}`
		zeros = `{"max_read_ops":0,"max_write_ops":0,"max_read_bytes":0,"max_write_bytes":0,"enabled":false}`
		setB  = "/admin/ratelimit?ratelimit-scope=bucket&bucket=b&enabled=true&max-read-ops=1&max-write-ops=1&max-read-bytes=1&max-write-bytes=1"
	)
	var fx *fixture
	addUser := func(id, caps string) {
		GinkgoHelper()
		info := meta.NewUserInfo()
		info.UserID = meta.UserID{ID: id}
		if caps != "" {
			Expect(info.Caps.AddString(caps)).To(Succeed())
		}
		fx.store.AddUser(info)
	}
	BeforeEach(func(ctx SpecContext) {
		fx = newFixture(admin.Config{})
		addUser("rl", "ratelimit=*")
		addUser("alice", "")
		_, err := fx.store.CreateBucket(ctx, op.CreateBucketParams{
			Name: "b", Owner: meta.UserOwner(meta.UserID{ID: "alice"}), Placement: meta.PlacementRule{Name: "default-placement"},
		})
		Expect(err).NotTo(HaveOccurred())
	})
	post := func(path, user string) *http.Response {
		GinkgoHelper()
		return fx.request(http.MethodPost, path, user)
	}
	// get asserts a 200 whose body a started flusher wrote, and returns it.
	get := func(path, user string) string {
		GinkgoHelper()
		res := fx.get(path, user)
		b := body(res)
		Expect(res.StatusCode).To(Equal(http.StatusOK), b)
		return b
	}
	attrs := func(ctx context.Context) (user, bucket []byte) {
		GinkgoHelper()
		u, err := fx.store.GetUser(ctx, meta.UserID{ID: "alice"})
		Expect(err).NotTo(HaveOccurred())
		b, err := fx.store.GetBucket(ctx, "", "b")
		Expect(err).NotTo(HaveOccurred())
		return u.Attrs[meta.AttrRateLimit], b.Attrs[meta.AttrRateLimit]
	}

	It("sets a bucket's limits, then reads them as go-ceph's TestBucketRateLimit compares them", func() {
		res := post(setB, "rl")
		Expect(res.StatusCode).To(Equal(http.StatusOK))
		Expect(body(res)).To(BeEmpty())
		res = fx.get("/admin/ratelimit?ratelimit-scope=bucket&bucket=b", "rl")
		Expect(res.Header.Get("Content-Type")).To(Equal("application/json"))
		Expect(body(res)).To(Equal(`{"bucket_ratelimit":` + ones + `}`))
		res = fx.get("/admin/ratelimit?ratelimit-scope=bucket&bucket=b&format=xml", "rl")
		Expect(res.Header.Get("Content-Type")).To(Equal("application/xml"))
		Expect(body(res)).To(Equal(`<?xml version="1.0" encoding="UTF-8"?><bucket_ratelimit><bucket_ratelimit><max_read_ops>1</max_read_ops>` +
			`<max_write_ops>1</max_write_ops><max_read_bytes>1</max_read_bytes><max_write_bytes>1</max_write_bytes><enabled>true</enabled></bucket_ratelimit></bucket_ratelimit>`))
	})
	It("sets and reads a user's limits, ignoring a negative limit", func(ctx SpecContext) {
		Expect(post("/admin/ratelimit?ratelimit-scope=user&uid=alice&max-read-ops=7&max-write-ops=3", "rl").StatusCode).To(Equal(http.StatusOK))
		Expect(post("/admin/ratelimit?ratelimit-scope=user&uid=alice&max-read-ops=-1&enabled=TRUE", "rl").StatusCode).To(Equal(http.StatusOK))
		Expect(get("/admin/ratelimit?ratelimit-scope=user&uid=alice", "rl")).To(Equal(
			`{"user_ratelimit":{"max_read_ops":7,"max_write_ops":3,"max_read_bytes":0,"max_write_bytes":0,"enabled":true}}`))
		Expect(post("/admin/ratelimit?ratelimit-scope=user&uid=alice&max-read-ops=", "rl").StatusCode).To(Equal(http.StatusOK), "strtoll reads an empty value as 0")
		Expect(get("/admin/ratelimit?ratelimit-scope=user&uid=alice", "rl")).To(HavePrefix(`{"user_ratelimit":{"max_read_ops":0,"max_write_ops":3,`))
		_, b := attrs(ctx)
		Expect(b).To(BeNil(), "the bucket is untouched")
	})
	It("sets a global scope in the period config and reads the three in radosgw's order", func() {
		Expect(post("/admin/ratelimit?global=true&ratelimit-scope=anon&max-read-ops=2", "rl").StatusCode).To(Equal(http.StatusOK))
		Expect(get("/admin/ratelimit?global=true", "rl")).To(Equal(`{"bucket_ratelimit":` + zeros + `,"user_ratelimit":` + zeros +
			`,"anonymous_ratelimit":{"max_read_ops":2,"max_write_ops":0,"max_read_bytes":0,"max_write_bytes":0,"enabled":false}}`))
		Expect(get("/admin/ratelimit?global=TRUE&ratelimit-scope=bucket&bucket=b&format=xml", "rl")).To(HavePrefix(
			`<?xml version="1.0" encoding="UTF-8"?><period_config><bucket_ratelimit><max_read_ops>0</max_read_ops>`), "global wins over the bucket scope")
		cfg, err := fx.store.GetPeriodConfig(context.Background(), "")
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg.AnonRateLimit.MaxReadOps).To(BeEquivalentTo(2), "no realm is period_config.default")
	})
	DescribeTable("refuses a malformed argument with 400 InvalidArgument, writing nothing",
		func(ctx SpecContext, method, path string) {
			res := fx.request(method, path, "rl")
			Expect(res.StatusCode).To(Equal(http.StatusBadRequest))
			Expect(body(res)).To(HavePrefix(`{"Code":"InvalidArgument",`))
			u, b := attrs(ctx)
			Expect(u).To(BeNil())
			Expect(b).To(BeNil())
		},
		Entry("a get's global that is not true or false", http.MethodGet, "/admin/ratelimit?global=yes"),
		Entry("a get's empty global, which get_bool would take as true", http.MethodGet, "/admin/ratelimit?global="),
		Entry("a get naming no scope it serves", http.MethodGet, "/admin/ratelimit?ratelimit-scope=anon"),
		Entry("a set with no limit and no enabled", http.MethodPost, "/admin/ratelimit?ratelimit-scope=bucket&bucket=b"),
		Entry("a set with negative limits alone", http.MethodPost, "/admin/ratelimit?ratelimit-scope=bucket&bucket=b&max-read-ops=-1"),
		Entry("a set with a limit that does not parse", http.MethodPost, "/admin/ratelimit?ratelimit-scope=bucket&bucket=b&enabled=true&max-write-bytes=1k"),
		Entry("a set's enabled that is not true or false", http.MethodPost, "/admin/ratelimit?ratelimit-scope=bucket&bucket=b&enabled=1"),
		Entry("a set's global that is not true or false", http.MethodPost, "/admin/ratelimit?ratelimit-scope=bucket&bucket=b&enabled=true&global=0"),
		Entry("a global set of an unknown scope", http.MethodPost, "/admin/ratelimit?ratelimit-scope=group&global=true&enabled=true"),
	)
	It("answers 404 NoSuchKey for a missing bucket or user", func() {
		for _, req := range []struct{ method, path string }{
			{http.MethodGet, "/admin/ratelimit?ratelimit-scope=bucket&bucket=nosuch"},
			{http.MethodGet, "/admin/ratelimit?ratelimit-scope=user&uid=nosuch"},
			{http.MethodPost, "/admin/ratelimit?ratelimit-scope=bucket&bucket=nosuch&enabled=true"},
			{http.MethodPost, "/admin/ratelimit?ratelimit-scope=user&uid=nosuch&enabled=true"},
		} {
			res := fx.request(req.method, req.path, "rl")
			Expect(res.StatusCode).To(Equal(http.StatusNotFound), req.path)
			Expect(body(res)).To(HavePrefix(`{"Code":"NoSuchKey",`), req.path)
		}
	})
	DescribeTable("refuses a caller without radosgw's cap, before reading or writing anything",
		func(ctx SpecContext, method, path, caps string) {
			addUser("caller", caps)
			res := fx.request(method, path, "caller")
			Expect(res.StatusCode).To(Equal(http.StatusForbidden))
			Expect(body(res)).To(HavePrefix(`{"Code":"AccessDenied",`))
			u, b := attrs(ctx)
			Expect(u).To(BeNil())
			Expect(b).To(BeNil())
			_, err := fx.store.GetPeriodConfig(ctx, "")
			Expect(err).To(MatchError(op.ErrNotFound))
		},
		Entry("get without caps", http.MethodGet, "/admin/ratelimit?ratelimit-scope=bucket&bucket=b", ""),
		Entry("get with ratelimit=write", http.MethodGet, "/admin/ratelimit?ratelimit-scope=bucket&bucket=b", "ratelimit=write"),
		Entry("get with another type", http.MethodGet, "/admin/ratelimit?ratelimit-scope=bucket&bucket=b", "buckets=*;zone=*"),
		Entry("get's malformed global without caps, refused for the cap first", http.MethodGet, "/admin/ratelimit?global=yes", ""),
		Entry("set without caps", http.MethodPost, setB, ""),
		Entry("set with ratelimit=read", http.MethodPost, setB, "ratelimit=read"),
		Entry("set with another type", http.MethodPost, setB, "buckets=*;users=*"),
		Entry("a user set with ratelimit=read", http.MethodPost, "/admin/ratelimit?ratelimit-scope=user&uid=alice&enabled=true", "ratelimit=read"),
		Entry("a global set with ratelimit=read", http.MethodPost, "/admin/ratelimit?global=true&ratelimit-scope=anon&enabled=true", "ratelimit=read"),
		Entry("a set's malformed limit with ratelimit=read, refused for the cap first", http.MethodPost, "/admin/ratelimit?ratelimit-scope=bucket&bucket=b&max-read-ops=x", "ratelimit=read"),
	)
	It("admits a caller with ratelimit=read to the get", func() {
		addUser("caller", "ratelimit=read")
		Expect(get("/admin/ratelimit?ratelimit-scope=bucket&bucket=b", "caller")).To(Equal(`{"bucket_ratelimit":` + zeros + `}`))
	})
})
