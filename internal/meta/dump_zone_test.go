package meta_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/formatter"
	"github.com/jhoblitt/rgw-go/internal/meta"
)

var _ = Describe("MaskZoneSecrets", func() {
	str := func(v string) meta.JSONFormattable {
		return meta.JSONFormattable{Type: meta.FormattableValue, Value: v, Quoted: true}
	}
	type members = map[string]meta.JSONFormattable
	obj := func(m members) meta.JSONFormattable {
		return meta.JSONFormattable{Type: meta.FormattableObject, Object: m}
	}
	arr := func(xs ...meta.JSONFormattable) meta.JSONFormattable {
		return meta.JSONFormattable{Type: meta.FormattableArray, Array: xs}
	}
	// cloud is a cloud sync zone's tier config holding a credential at each
	// place AWSSyncConfig::init reads one: the root connection, the default
	// profile's, a connections entry and a profile's
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_sync_module_aws.cc#L524-L555
	// at v19.2.7 and v20.2.4), each secret written as secret, in its member
	// and in its endpoint's userinfo.
	cloud := func(secret func(name string) string) meta.JSONFormattable {
		endpoint := func(name string) meta.JSONFormattable {
			return str("http://AK-" + name + ":" + secret(name) + "@" + name + ".example:8000/rgw?a=1")
		}
		conn := func(name string) meta.JSONFormattable {
			return obj(members{"access_key": str("AK-" + name), "endpoint": endpoint(name), "secret": str(secret(name))})
		}
		return obj(members{
			"connection": conn("root"),
			"default":    obj(members{"connection": conn("default"), "target_path": str("rgw-${zonegroup}")}),
			"connections": arr(obj(members{
				"access_key": str("AK-c1"), "endpoint": endpoint("c1"), "id": str("c1"), "secret": str(secret("c1")),
			})),
			"profiles": arr(obj(members{"connection": conn("profile"), "connection_id": str("c1"), "source_bucket": str("b*")})),
		})
	}
	inClear := func(name string) string { return "secret-" + name }
	maskedAll := func(string) string { return meta.MaskedTierSecret }
	render := func(z meta.ZoneParams, f formatter.Formatter) string {
		GinkgoHelper()
		f.OpenObjectSection("zone_params")
		z.Dump(f, denc.Squid)
		f.CloseSection()
		Expect(f.Err()).NotTo(HaveOccurred())
		return string(f.Bytes())
	}
	zone := func(tc meta.JSONFormattable) meta.ZoneParams {
		return meta.ZoneParams{ID: "zid", Name: "z1", SystemKey: meta.AccessKey{ID: "SYSKEY", Secret: "system-secret"}, TierConfig: tc}
	}

	DescribeTable("masks a cloud zone's secret at each place the cloud module reads one, in its member and its endpoint, and nothing else",
		func(f func() formatter.Formatter) {
			Expect(render(meta.MaskZoneSecrets(zone(cloud(inClear))), f())).To(Equal(render(zone(cloud(maskedAll)), f())))
		},
		Entry("in JSON", func() formatter.Formatter { return formatter.NewJSON(false) }),
		Entry("in XML", func() formatter.Formatter { return formatter.NewXML(false, false) }),
	)

	It("masks an elasticsearch zone's password, in its member and in its endpoint's userinfo, and keeps its username", func() {
		es := func(pw string) meta.ZoneParams {
			return zone(obj(members{
				"endpoint": str("https://elastic:" + pw + "@es:9200"), "num_shards": {Type: meta.FormattableValue, Value: "16"},
				"password": str(pw), "username": str("elastic"),
			}))
		}
		Expect(render(meta.MaskZoneSecrets(es("es-password")), formatter.NewJSON(false))).To(Equal(render(es(meta.MaskedTierSecret), formatter.NewJSON(false))))
	})

	DescribeTable("masks a secret or password member by what it renders",
		func(name string, val meta.JSONFormattable, want string) {
			got := render(meta.MaskZoneSecrets(zone(obj(members{name: val}))), formatter.NewJSON(false))
			Expect(got).To(ContainSubstring(`"tier_config":{` + want + `}`))
		},
		Entry("an empty secret stays empty", "secret", str(""), `"secret":""`),
		Entry("an empty password stays empty", "password", str(""), `"password":""`),
		Entry("an unquoted secret becomes the quoted mask", "secret",
			meta.JSONFormattable{Type: meta.FormattableValue, Value: "12345"}, `"secret":"******"`),
		Entry("a secret holding an object becomes the mask", "secret", obj(members{"key": str("x")}), `"secret":"******"`),
		Entry("a secret holding an array becomes the mask", "secret", arr(str("x")), `"secret":"******"`),
		Entry("a secret that renders nothing stays as stored", "secret", meta.JSONFormattable{}, ``),
		Entry("a member named otherwise is kept", "Secret", str("kept"), `"Secret":"kept"`),
	)

	// libcurl takes a URL's userinfo as the request's credentials, and
	// radosgw hands an endpoint to curl as the CURLOPT_URL it requests
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_http_client.cc#L588 at v19.2.7
	// and v20.2.4). curl parses URLs Go's url.Parse refuses and skips a run of
	// slashes after the scheme, so what the mask cannot split cleanly it masks
	// whole.
	DescribeTable("masks the password of an endpoint it splits cleanly, keeping every other byte, and masks any other endpoint holding an at sign whole",
		func(name, in, want string) {
			got := render(meta.MaskZoneSecrets(zone(obj(members{name: str(in)}))), formatter.NewJSON(false))
			Expect(got).To(ContainSubstring(`"tier_config":{"` + name + `":"` + want + `"}`))
		},
		Entry("a user and a password", "endpoint", "http://elastic:pw@es:9200", "http://elastic:******@es:9200"),
		Entry("a user, a password and a path", "endpoint", "https://u:p@h/x", "https://u:******@h/x"),
		Entry("an escaped userinfo, a port, a path, a query and a fragment", "endpoint",
			"https://u%40x:p%3Aw@es.example:9243/prefix/?a=1#f", "https://u%40x:******@es.example:9243/prefix/?a=1#f"),
		Entry("a password holding an at sign, which the authority's last one ends", "endpoint",
			"http://u:p@ss@es:9200", "http://u:******@es:9200"),
		Entry("a userinfo with two at signs, the user holding one", "endpoint", "https://u@x:p@h", "https://u@x:******@h"),
		Entry("a space in the password, which url.Parse refuses", "endpoint", "https://user:pa ss@es:9200", "https://user:******@es:9200"),
		Entry("a stray percent and a space in the host, which url.Parse refuses", "endpoint",
			"https://user:pa%zz@es host:9200/x", "https://user:******@es host:9200/x"),
		Entry("no userinfo", "endpoint", "http://es:9200", "http://es:9200"),
		Entry("a user without a password", "endpoint", "http://elastic@es:9200", "http://elastic@es:9200"),
		Entry("an empty password", "endpoint", "http://elastic:@es:9200", "http://elastic:@es:9200"),
		Entry("three slashes after the scheme, masked whole", "endpoint", "http:///u:pw@h", "******"),
		Entry("a slash in the password, masked whole", "endpoint", "http://u:p/w@h", "******"),
		Entry("an at sign in the path, masked whole", "endpoint", "https://h/path@x", "******"),
		Entry("an at sign in the query or the fragment, masked whole", "endpoint", "http://es:9200?a=b@c#d", "******"),
		Entry("an at sign past the authority as well as in it, masked whole", "endpoint", "http://u:p@h/a@b", "******"),
		Entry("no scheme, masked whole", "endpoint", "elastic:pw@es:9200", "******"),
		Entry("a scheme with an at sign, masked whole", "endpoint", "u:p@h://x", "******"),
		Entry("an empty scheme, masked whole", "endpoint", "://u:p@h", "******"),
		Entry("no at sign, though url.Parse refuses it", "endpoint", "http://es host:9200/%zz", "http://es host:9200/%zz"),
		Entry("no at sign and no scheme", "endpoint", "es.example:9200", "es.example:9200"),
		Entry("a member named otherwise", "url", "http://elastic:pw@es:9200", "http://elastic:pw@es:9200"),
	)

	It("leaves the caller's zone params and the maps and slices they share unmasked, the system key with them", func() {
		z := zone(cloud(inClear))
		want := render(z, formatter.NewJSON(false))
		masked := render(meta.MaskZoneSecrets(z), formatter.NewJSON(false))
		Expect(render(z, formatter.NewJSON(false))).To(Equal(want))
		for _, name := range []string{"root", "default", "c1", "profile"} {
			Expect(want).To(ContainSubstring(`"secret":"secret-`+name+`"`), "Dump of the caller's zone writes %s's secret", name)
			Expect(want).To(ContainSubstring(`:secret-`+name+`@`), "Dump of the caller's zone writes %s's endpoint password", name)
			Expect(masked).NotTo(ContainSubstring("secret-"+name), "the masked zone's %s secret", name)
		}
		Expect(masked).To(ContainSubstring(`"system_key":{"access_key":"SYSKEY","secret_key":"system-secret"}`),
			"the system key is written as radosgw writes it")
	})
})
