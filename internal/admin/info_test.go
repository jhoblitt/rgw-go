package admin_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/admin"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
)

// radosgw writes a sync zone's tier config as stored (docs/ceph-upstream-bugs.md,
// "radosgw hands a sync zone's endpoint credentials to any holder of
// zone=read").
var _ = Describe("admin zone config route", func() {
	const (
		// connSecret and profileSecret are made-up secrets of a cloud sync
		// zone's root connection and of one profile's.
		connSecret    = "made-up-conn-secret"
		profileSecret = "made-up-profile-secret"
		systemSecret  = "made-up-system-secret"
		// endpointSecret is a made-up password in the connection
		// endpoint's userinfo.
		endpointSecret = "made-up-endpoint-secret"
	)
	str := func(v string) meta.JSONFormattable {
		return meta.JSONFormattable{Type: meta.FormattableValue, Value: v, Quoted: true}
	}
	obj := func(m map[string]meta.JSONFormattable) meta.JSONFormattable {
		return meta.JSONFormattable{Type: meta.FormattableObject, Object: m}
	}
	var fx *fixture
	BeforeEach(func() {
		fx = newFixtureWith(denc.Squid, admin.Config{}, func(z *meta.ZoneParams) {
			z.SystemKey = meta.AccessKey{ID: "made-up-system-key", Secret: systemSecret}
			z.TierConfig = obj(map[string]meta.JSONFormattable{
				"connection": obj(map[string]meta.JSONFormattable{
					"access_key": str("made-up-conn-key"), "endpoint": str("https://made-up-conn-key:" + endpointSecret + "@cloud.example"),
					"secret": str(connSecret),
				}),
				"connections": {Type: meta.FormattableArray, Array: []meta.JSONFormattable{
					obj(map[string]meta.JSONFormattable{"access_key": str("made-up-empty-key"), "id": str("c1"), "secret": str("")}),
				}},
				"profiles": {Type: meta.FormattableArray, Array: []meta.JSONFormattable{
					obj(map[string]meta.JSONFormattable{"connection": obj(map[string]meta.JSONFormattable{
						"access_key": str("made-up-profile-key"), "secret": str(profileSecret),
					})}),
				}},
			})
		})
	})
	addUser := func(set func(*meta.UserInfo)) {
		GinkgoHelper()
		info := meta.NewUserInfo()
		info.UserID = meta.UserID{ID: "caller"}
		set(&info)
		fx.store.AddUser(info)
	}
	zoneRead := func(u *meta.UserInfo) { Expect(u.Caps.AddString("zone=read")).To(Succeed()) }
	get := func(format string) string {
		GinkgoHelper()
		res := fx.get("/admin/config?type=zone"+format, "caller")
		b := body(res)
		Expect(res.StatusCode).To(Equal(200), b)
		return b
	}

	DescribeTable("masks a sync zone's tier config secrets for a zone=read caller, keeping the access keys and the system key",
		func(format string, want ...string) {
			addUser(zoneRead)
			b := get(format)
			for _, w := range want {
				Expect(b).To(ContainSubstring(w))
			}
			Expect(b).NotTo(ContainSubstring(connSecret))
			Expect(b).NotTo(ContainSubstring(profileSecret))
			Expect(b).NotTo(ContainSubstring(endpointSecret))
		},
		Entry("in JSON", "",
			`"connection":{"access_key":"made-up-conn-key","endpoint":"https://made-up-conn-key:******@cloud.example","secret":"******"}`,
			`"connections":[{"access_key":"made-up-empty-key","id":"c1","secret":""}]`,
			`"profiles":[{"connection":{"access_key":"made-up-profile-key","secret":"******"}}]`,
			`"system_key":{"access_key":"made-up-system-key","secret_key":"`+systemSecret+`"}`),
		Entry("in XML", "&format=xml",
			`<connection><access_key>made-up-conn-key</access_key><endpoint>https://made-up-conn-key:******@cloud.example</endpoint><secret>******</secret></connection>`,
			`<connections><obj><access_key>made-up-empty-key</access_key><id>c1</id><secret></secret></obj></connections>`,
			`<profiles><obj><connection><access_key>made-up-profile-key</access_key><secret>******</secret></connection></obj></profiles>`,
			`<system_key><access_key>made-up-system-key</access_key><secret_key>`+systemSecret+`</secret_key></system_key>`),
	)
	DescribeTable("writes the tier config in the clear for a system user alone",
		func(set func(*meta.UserInfo), inClear bool) {
			addUser(set)
			b := get("")
			if inClear {
				Expect(b).To(ContainSubstring(`"secret":"` + connSecret + `"`))
				Expect(b).To(ContainSubstring(`"secret":"` + profileSecret + `"`))
				Expect(b).To(ContainSubstring(`:` + endpointSecret + `@`))
			} else {
				Expect(b).NotTo(ContainSubstring(connSecret))
				Expect(b).NotTo(ContainSubstring(profileSecret))
				Expect(b).NotTo(ContainSubstring(endpointSecret))
			}
		},
		Entry("a system user", func(u *meta.UserInfo) { u.System = 1 }, true),
		Entry("an admin user that is not a system user", func(u *meta.UserInfo) { u.Admin = 1 }, false),
	)
	It("masks only the document, so the zone the gateway holds keeps its secrets", func() {
		addUser(zoneRead)
		Expect(get("")).NotTo(ContainSubstring(connSecret))
		tc := fx.store.ZoneParams().TierConfig
		Expect(tc.Object["connection"].Object["secret"].Value).To(Equal(connSecret))
		Expect(tc.Object["connection"].Object["endpoint"].Value).To(Equal("https://made-up-conn-key:" + endpointSecret + "@cloud.example"))
		Expect(tc.Object["profiles"].Array[0].Object["connection"].Object["secret"].Value).To(Equal(profileSecret))
	})
})
