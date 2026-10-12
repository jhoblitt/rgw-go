package admin_test

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/admin"
	"github.com/jhoblitt/rgw-go/internal/meta"
)

var _ = Describe("admin realm and period routes", func() {
	const (
		realmJSON = `{"id":"R","name":"r1","current_period":"P","epoch":2}`
		// tierSecret is a made-up cloud tier secret key, which
		// RGWZoneGroupPlacementTierS3::dump writes into a period.
		tierSecret = "made-up-tier-secret"
	)
	var fx *fixture
	BeforeEach(func() {
		fx = newFixture(admin.Config{})
		fx.store.AddRealm(meta.Realm{ID: "R", Name: "r1", CurrentPeriod: "P", Epoch: 2})
		fx.store.SetDefaultRealm("R")
		zone := meta.NewZone()
		zone.ID, zone.Name = "zid", "z1"
		tier := meta.NewZoneGroupPlacementTier()
		tier.TierType, tier.StorageClass = meta.TierTypeCloudS3, "CLOUD"
		tier.S3.Key = meta.AccessKey{ID: "made-up-tier-key", Secret: tierSecret}
		zg := meta.ZoneGroup{
			ID: "G", Name: "zg1", IsMaster: true, MasterZone: "zid", Zones: map[string]meta.Zone{"zid": zone}, RealmID: "R",
			PlacementTargets: map[string]meta.ZoneGroupPlacementTarget{"default-placement": {
				Name: "default-placement", StorageClasses: []string{"CLOUD", "STANDARD"},
				TierTargets: map[string]meta.ZoneGroupPlacementTier{"CLOUD": tier},
			}},
		}
		for _, epoch := range []uint32{1, 2} {
			p := meta.NewPeriod()
			p.ID, p.Epoch, p.RealmID = "P", epoch, "R"
			p.PeriodMap = meta.PeriodMap{ID: "P", ZoneGroups: map[string]meta.ZoneGroup{"G": zg}}
			fx.store.AddPeriod(p)
		}
		zg.SyncPolicy = meta.SyncPolicy{Raw: []byte{1, 1, 8, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0}}
		p := meta.NewPeriod()
		p.ID, p.Epoch, p.RealmID = "P", 3, "R"
		p.PeriodMap = meta.PeriodMap{ID: "P", ZoneGroups: map[string]meta.ZoneGroup{"G": zg}}
		fx.store.AddPeriod(p)
	})
	addUser := func(id, caps string) {
		GinkgoHelper()
		info := meta.NewUserInfo()
		info.UserID = meta.UserID{ID: id}
		if caps != "" {
			Expect(info.Caps.AddString(caps)).To(Succeed())
		}
		fx.store.AddUser(info)
	}
	// typed asserts the headers a getter that names application/json and
	// its length sends, and returns the body.
	typed := func(res *http.Response) string {
		GinkgoHelper()
		b := body(res)
		Expect(res.StatusCode).To(Equal(http.StatusOK), b)
		Expect(res.Header.Get("Content-Type")).To(Equal("application/json"))
		Expect(res.Header.Get("Content-Length")).To(Equal(strconv.Itoa(len(b))))
		Expect(res.Header.Get("Accept-Ranges")).To(Equal("bytes"), "dump_content_length sends it")
		return b
	}

	It("serves the realm by default, id and name as RGWRealm::dump writes it", func() {
		for _, path := range []string{"/admin/realm", "/admin/realm?id=R", "/admin/realm?name=r1", "/admin/realm?id=R&name=nosuch"} {
			Expect(typed(fx.get(path, "admin"))).To(Equal(realmJSON), path)
		}
	})
	It("names application/json for the realm whatever the format, with no XML declaration", func() {
		Expect(typed(fx.get("/admin/realm?format=xml", "admin"))).To(Equal(`<realm><id>R</id><name>r1</name><current_period>P</current_period><epoch>2</epoch></realm>`))
	})
	It("answers 404 NoSuchKey for a realm it cannot find, in the request's format", func() {
		res := fx.get("/admin/realm?name=nosuch&format=xml", "admin")
		Expect(res.StatusCode).To(Equal(http.StatusNotFound))
		Expect(res.Header.Get("Content-Type")).To(Equal("application/xml"))
		Expect(body(res)).To(HavePrefix(`<?xml version="1.0" encoding="UTF-8"?><Error><Code>NoSuchKey</Code>`))
	})
	It("lists the realms with the default", func() {
		Expect(typed(fx.get("/admin/realm?list", "admin"))).To(Equal(`{"default_info":"R","realms":["r1"]}`))
		Expect(typed(fx.get("/admin/realm?list&format=xml", "admin"))).To(Equal(`<realms_list><default_info>R</default_info><realms><obj>r1</obj></realms></realms_list>`))
	})
	It("serves a period at the epoch named, in JSON and in XML", func() {
		b := typed(fx.get("/admin/realm/period?epoch=2", "admin"))
		var doc map[string]json.RawMessage
		Expect(json.Unmarshal([]byte(b), &doc)).To(Succeed())
		Expect(doc).To(HaveKeyWithValue("id", json.RawMessage(`"P"`)))
		Expect(doc).To(HaveKeyWithValue("epoch", json.RawMessage(`2`)))
		Expect(doc).To(HaveKeyWithValue("realm_id", json.RawMessage(`"R"`)))
		Expect(doc).To(HaveKey("period_map"))
		Expect(doc).To(HaveKey("period_config"))
		Expect(b).To(HavePrefix(`{"id":"P","epoch":2,"predecessor_uuid":"","sync_status":[],"period_map":{"id":"P","zonegroups":[{"id":"G","name":"zg1",`))
		Expect(b).To(HaveSuffix(`"realm_id":"R","realm_epoch":1}`))
		x := typed(fx.get("/admin/realm/period?epoch=1&format=xml", "admin"))
		Expect(x).To(HavePrefix(`<period><id>P</id><epoch>1</epoch>`))
		Expect(x).To(ContainSubstring(`<zonegroups><obj><id>G</id>`))
	})
	It("writes a cloud tier's secret key into the period for a zone=read caller, as radosgw does", func() {
		addUser("caller", "zone=read")
		b := typed(fx.get("/admin/realm/period?epoch=2", "caller"))
		Expect(strings.Contains(b, `"access_key":"made-up-tier-key","secret":"`+tierSecret+`"`)).To(BeTrue(),
			"RGWZoneGroupPlacementTierS3::dump writes the secret (docs/ceph-upstream-bugs.md)")
	})
	It("reads the latest epoch for an epoch it cannot parse, as radosgw ignores get_uint32's result", func() {
		res := fx.get("/admin/realm/period?epoch=abc", "admin")
		Expect(res.StatusCode).To(Equal(http.StatusNotImplemented), "the latest epoch, 3, holds sync policy groups")
		Expect(typed(fx.get("/admin/realm/period?realm_id=R&period_id=P&epoch=2", "admin"))).To(HavePrefix(`{"id":"P","epoch":2,`))
	})
	It("answers 501 NotImplemented for a period whose zonegroup holds sync policy groups", func() {
		res := fx.get("/admin/realm/period", "admin")
		Expect(res.StatusCode).To(Equal(http.StatusNotImplemented))
		Expect(body(res)).To(HavePrefix(`{"Code":"NotImplemented",`))
	})
	It("answers 404 NoSuchKey for an epoch it cannot find", func() {
		res := fx.get("/admin/realm/period?epoch=9", "admin")
		Expect(res.StatusCode).To(Equal(http.StatusNotFound))
		Expect(body(res)).To(HavePrefix(`{"Code":"NoSuchKey",`))
	})
	It("answers 405 for the period POST, excluded with multisite", func() {
		Expect(fx.request(http.MethodPost, "/admin/realm/period", "admin").StatusCode).To(Equal(http.StatusMethodNotAllowed))
	})
	DescribeTable("refuses a caller without zone=read, before reading anything",
		func(path, caps string) {
			addUser("caller", caps)
			res := fx.get(path, "caller")
			Expect(res.StatusCode).To(Equal(http.StatusForbidden))
			b := body(res)
			Expect(b).To(HavePrefix(`{"Code":"AccessDenied",`))
			Expect(strings.Contains(b, tierSecret)).To(BeFalse(), "a refused caller gets no tier secret")
		},
		Entry("realm without caps", "/admin/realm", ""),
		Entry("realm with zone=write", "/admin/realm", "zone=write"),
		Entry("realm with another type", "/admin/realm", "users=*;ratelimit=*"),
		Entry("list without caps", "/admin/realm?list", ""),
		Entry("list with zone=write", "/admin/realm?list", "zone=write"),
		Entry("list with another type", "/admin/realm?list", "metadata=*"),
		Entry("period without caps", "/admin/realm/period?epoch=1", ""),
		Entry("period with zone=write", "/admin/realm/period?epoch=1", "zone=write"),
		Entry("period with another type", "/admin/realm/period?epoch=1", "info=*"),
		Entry("period with every other admin type", "/admin/realm/period?epoch=2",
			"users=*;buckets=*;metadata=*;usage=*;info=*;accounts=*;ratelimit=*;user-info-without-keys=*"),
		Entry("a missing realm without caps, refused before the lookup", "/admin/realm?name=nosuch", ""),
	)
	DescribeTable("admits a caller with zone=read",
		func(path string) {
			addUser("caller", "zone=read")
			Expect(typed(fx.get(path, "caller"))).NotTo(BeEmpty())
		},
		Entry("realm", "/admin/realm"),
		Entry("list", "/admin/realm?list"),
		Entry("period", "/admin/realm/period?epoch=1"),
	)
})
