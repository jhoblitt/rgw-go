package admin_test

import (
	"context"
	"encoding/json"
	"net/http"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/admin"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
)

// accountDoc decodes a JSON AccountInfo document, whose outermost section
// JSONFormatter writes without its name.
func accountDoc(res *http.Response) map[string]any {
	GinkgoHelper()
	b := body(res)
	var doc map[string]any
	Expect(json.Unmarshal([]byte(b), &doc)).To(Succeed(), b)
	return doc
}

var _ = Describe("admin account routes", func() {
	const id = "RGW12345678901234567"
	var fx *fixture
	BeforeEach(func() { fx = newFixtureAt(denc.Tentacle, admin.Config{}) })
	stored := func() meta.AccountInfo {
		GinkgoHelper()
		rec, err := fx.store.GetAccount(context.Background(), id)
		Expect(err).NotTo(HaveOccurred())
		return rec.Info
	}

	It("creates an account, refuses its id again, reads, renames and removes it", func() {
		res := fx.request(http.MethodPost, "/admin/account?id="+id+"&name=test-account", "admin")
		Expect(res.StatusCode).To(Equal(200))
		Expect(res.Header.Get("Content-Type")).To(Equal("application/json"))
		doc := accountDoc(res)
		Expect(doc).To(HaveKeyWithValue("id", id))
		Expect(doc).To(HaveKeyWithValue("name", "test-account"))
		Expect(doc).To(HaveKeyWithValue("max_users", BeEquivalentTo(1000)))

		res = fx.request(http.MethodPost, "/admin/account?id="+id+"&name=test-account", "admin")
		Expect(res.StatusCode).To(Equal(409))
		Expect(errCode(res)).To(Equal("AccountAlreadyExists"), "create maps EEXIST, rgw_rest_account.cc:97-101")

		res = fx.get("/admin/account?id="+id, "admin")
		Expect(res.StatusCode).To(Equal(200))
		Expect(accountDoc(res)).To(HaveKeyWithValue("name", "test-account"))
		res = fx.get("/admin/account?id=nosuch", "admin")
		Expect(res.StatusCode).To(Equal(404))
		Expect(errCode(res)).To(Equal("NoSuchKey"))

		res = fx.request(http.MethodPut, "/admin/account?id="+id+"&name=modified-account", "admin")
		Expect(res.StatusCode).To(Equal(200))
		Expect(accountDoc(res)).To(HaveKeyWithValue("name", "modified-account"))

		res = fx.request(http.MethodDelete, "/admin/account?id="+id, "admin")
		Expect(res.StatusCode).To(Equal(200))
		Expect(body(res)).To(BeEmpty())
		res = fx.request(http.MethodDelete, "/admin/account?id="+id, "admin")
		Expect(res.StatusCode).To(Equal(404))
		Expect(errCode(res)).To(Equal("NoSuchKey"))
	})

	It("generates an id when the create names none", func() {
		res := fx.request(http.MethodPost, "/admin/account?name=quota-test-account", "admin")
		Expect(res.StatusCode).To(Equal(200))
		Expect(accountDoc(res)["id"]).To(MatchRegexp(`^RGW[0-9]{17}$`))
	})

	It("stores the limits and email given", func() {
		res := fx.request(http.MethodPost, "/admin/account?id="+id+"&max-users=5&max-roles=-1&max-buckets=2147483647&email=ops%40acme.example", "admin")
		Expect(res.StatusCode).To(Equal(200))
		got := stored()
		Expect(got.MaxUsers).To(BeEquivalentTo(5))
		Expect(got.MaxRoles).To(BeEquivalentTo(-1))
		Expect(got.MaxBuckets).To(BeEquivalentTo(2147483647))
		Expect(got.MaxGroups).To(BeEquivalentTo(meta.DefaultAccountGroupLimit))
		Expect(got.Email).To(Equal("ops@acme.example"))
	})

	DescribeTable("refuses a limit get_int32 would store as 0 or wrapped, which can mean no limit, writing nothing",
		func(limit, value string) {
			res := fx.request(http.MethodPost, "/admin/account?id="+id+"&"+limit+"="+value, "admin")
			Expect(res.StatusCode).To(Equal(400), "create %s=%s", limit, value)
			Expect(errCode(res)).To(Equal("InvalidArgument"))
			_, err := fx.store.GetAccount(context.Background(), id)
			Expect(err).To(HaveOccurred(), "no account created")
			Expect(fx.request(http.MethodPost, "/admin/account?id="+id, "admin").StatusCode).To(Equal(200))
			before := stored()
			res = fx.request(http.MethodPut, "/admin/account?id="+id+"&"+limit+"="+value, "admin")
			Expect(res.StatusCode).To(Equal(400), "modify %s=%s", limit, value)
			Expect(stored()).To(Equal(before))
		},
		Entry("an unparsable max-buckets, whose 0 is no limit", "max-buckets", "abc"),
		Entry("a max-buckets past int32", "max-buckets", "4294967296"),
		Entry("an unparsable max-users", "max-users", "lots"),
		Entry("a max-users that wraps to -1, no limit", "max-users", "4294967295"),
		Entry("an unparsable max-roles", "max-roles", "1x"),
		Entry("a max-roles past int32", "max-roles", "2147483648"),
		Entry("an unparsable max-groups", "max-groups", ""),
		Entry("a max-groups below int32", "max-groups", "-2147483649"),
		Entry("an unparsable max-access-keys", "max-access-keys", "four"),
		Entry("a max-access-keys past int32", "max-access-keys", "9223372036854775806"),
	)

	It("checks the caps before a limit it cannot parse", func() {
		res := fx.request(http.MethodPost, "/admin/account?id="+id+"&max-buckets=abc", "nocaps")
		Expect(res.StatusCode).To(Equal(403))
	})

	It("renders the AccountInfo section in XML", func() {
		Expect(fx.request(http.MethodPost, "/admin/account?id="+id+"&name=test-account", "admin").StatusCode).To(Equal(200))
		res := fx.get("/admin/account?id="+id+"&format=xml", "admin")
		Expect(res.StatusCode).To(Equal(200))
		b := body(res)
		Expect(b).To(HavePrefix(`<?xml version="1.0" encoding="UTF-8"?><AccountInfo><id>` + id + `</id><tenant></tenant><name>test-account</name><email></email><quota><enabled>false</enabled>`))
		Expect(b).To(HaveSuffix(`<max_users>1000</max_users><max_roles>1000</max_roles><max_groups>1000</max_groups><max_buckets>1000</max_buckets><max_access_keys>4</max_access_keys></AccountInfo>`))
	})

	Describe("the Tentacle quota", func() {
		BeforeEach(func() {
			Expect(fx.request(http.MethodPost, "/admin/account?id="+id+"&name=test-account", "admin").StatusCode).To(Equal(200))
		})
		It("sets the account quota and the bucket quota", func() {
			res := fx.request(http.MethodPut, "/admin/account?quota&id="+id+"&quota-type=account&max-size=1073741824&max-objects=1000&enabled=true", "admin")
			Expect(res.StatusCode).To(Equal(200))
			Expect(accountDoc(res)["quota"]).To(HaveKeyWithValue("max_size", BeEquivalentTo(1073741824)))
			doc := accountDoc(fx.get("/admin/account?id="+id, "admin"))
			Expect(doc["quota"]).To(HaveKeyWithValue("max_size", BeEquivalentTo(1073741824)))
			Expect(doc["quota"]).To(HaveKeyWithValue("max_objects", BeEquivalentTo(1000)))
			Expect(doc["quota"]).To(HaveKeyWithValue("enabled", true))
			res = fx.request(http.MethodPut, "/admin/account?quota&id="+id+"&quota-type=bucket&max-objects=7", "admin")
			Expect(res.StatusCode).To(Equal(200))
			Expect(stored().BucketQuota.MaxObjects).To(BeEquivalentTo(7))
			Expect(stored().Quota.MaxObjects).To(BeEquivalentTo(1000))
		})
		It("takes an empty enabled as true, as get_bool does", func() {
			Expect(fx.request(http.MethodPut, "/admin/account?quota&id="+id+"&quota-type=account&enabled=", "admin").StatusCode).To(Equal(200))
			Expect(stored().Quota.Enabled).To(BeTrue())
		})
		DescribeTable("refuses what radosgw stores wrongly, changing nothing",
			func(query string) {
				Expect(fx.request(http.MethodPut, "/admin/account?quota&id="+id+"&quota-type=account&enabled=true&max-size=1024", "admin").StatusCode).To(Equal(200))
				before := stored()
				res := fx.request(http.MethodPut, "/admin/account?quota&id="+id+"&"+query, "admin")
				Expect(res.StatusCode).To(Equal(400), query)
				Expect(errCode(res)).To(Equal("InvalidArgument"))
				Expect(stored()).To(Equal(before), query)
			},
			Entry("an unknown quota type", "quota-type=bogus&max-size=1"),
			Entry("no quota type", "max-size=1"),
			Entry("an unparsable maximum size, which radosgw stores as 0", "quota-type=account&max-size=1G"),
			Entry("an unparsable object count", "quota-type=account&max-objects=many"),
			Entry("an empty maximum size, which radosgw stores as 0", "quota-type=account&max-size="),
			Entry("a size past int32, which radosgw wraps to no limit", "quota-type=account&max-size=3221225472"),
			Entry("a size below -1, which reads as no limit", "quota-type=account&max-size=-2"),
			Entry("an unparsable enabled, which radosgw takes as false and disables the quota", "quota-type=account&enabled=ture"),
			Entry("a max-size-kb the op does not read", "quota-type=account&max-size-kb=-1"),
			Entry("a max-size-kb past int64 in bytes", "quota-type=account&max-size-kb=9007199254740992"),
		)
		It("refuses a missing id", func() {
			res := fx.request(http.MethodPut, "/admin/account?quota&quota-type=account&max-size=1", "admin")
			Expect(res.StatusCode).To(Equal(400))
		})
		It("is a plain modify on Squid, which reads no quota argument", func() {
			fx = newFixture(admin.Config{})
			Expect(fx.request(http.MethodPost, "/admin/account?id="+id+"&name=test-account", "admin").StatusCode).To(Equal(200))
			res := fx.request(http.MethodPut, "/admin/account?quota&id="+id+"&quota-type=account&max-size=1073741824&max-objects=1000&enabled=true", "admin")
			Expect(res.StatusCode).To(Equal(200))
			Expect(stored().Quota).To(Equal(meta.NewAccountInfo().Quota))
			Expect(fx.request(http.MethodPut, "/admin/account?quota&id="+id+"&quota-type=bogus", "admin").StatusCode).To(Equal(200))
		})
	})

	Describe("caps", func() {
		It("checks the caps before reading an argument or the account", func() {
			for _, rq := range []struct{ method, path string }{
				{http.MethodGet, "/admin/account?id=" + id},
				{http.MethodGet, "/admin/account?id=nosuch"},
				{http.MethodPost, "/admin/account?id=bad"},
				{http.MethodPut, "/admin/account?id=" + id + "&name=x"},
				{http.MethodPut, "/admin/account?quota&id=nosuch&quota-type=bogus&enabled=maybe"},
				{http.MethodDelete, "/admin/account?id=nosuch"},
			} {
				res := fx.request(rq.method, rq.path, "nocaps")
				Expect(res.StatusCode).To(Equal(403), "%s %s", rq.method, rq.path)
				Expect(errCode(res)).To(Equal("AccessDenied"))
			}
		})
		It("refuses the accounts cap holder the Squid get and delete, which check \"account\"", func() {
			fx = newFixture(admin.Config{})
			Expect(fx.request(http.MethodPost, "/admin/account?id="+id+"&name=test-account", "admin").StatusCode).To(Equal(200))
			Expect(fx.get("/admin/account?id="+id, "admin").StatusCode).To(Equal(403))
			Expect(fx.request(http.MethodDelete, "/admin/account?id="+id, "admin").StatusCode).To(Equal(403))
			Expect(fx.request(http.MethodPut, "/admin/account?id="+id+"&name=x", "admin").StatusCode).To(Equal(200))
		})
	})

	It("answers a taken name on modify with radosgw's BucketAlreadyExists row", func() {
		Expect(fx.request(http.MethodPost, "/admin/account?id="+id+"&name=a", "admin").StatusCode).To(Equal(200))
		Expect(fx.request(http.MethodPost, "/admin/account?id=RGW00000000000000002&name=b", "admin").StatusCode).To(Equal(200))
		res := fx.request(http.MethodPut, "/admin/account?id="+id+"&name=b", "admin")
		Expect(res.StatusCode).To(Equal(409))
		Expect(errCode(res)).To(Equal("BucketAlreadyExists"))
	})
})
