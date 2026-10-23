package driver_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/admin"
	"github.com/jhoblitt/rgw-go/internal/cls/otp"
	"github.com/jhoblitt/rgw-go/internal/cls/version"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/driver"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/s3"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
	"github.com/jhoblitt/rgw-go/internal/testutil/otptest"
)

// otpKey is alice's otp object, the otp section's key for her.
const otpKey = "user:alice"

// rfcHexSeed is RFC 6238's SHA-1 test key in hex.
const rfcHexSeed = "3132333435363738393031323334353637383930"

// otpStamp is the mtime the otp specs write and seed.
var otpStamp = time.Date(2026, 10, 20, 1, 2, 3, 0, time.UTC)

// headerAt is the header name as the handler spelled it, which
// http.Header's canonicalizing lookups would not find.
func headerAt(h http.Header, name string) []string { return h[name] }

// otpDevice is a TOTP device with rfcHexSeed.
func otpDevice(id string) otp.OTPInfo {
	return otp.OTPInfo{Type: otp.TypeTOTP, ID: id, Seed: rfcHexSeed, SeedType: otp.SeedHex, StepSize: 30, Window: 2}
}

// otpSteps names a write op's steps as the otp specs compare them.
func otpSteps(steps []radosclient.Step) []string {
	var out []string
	for _, st := range steps {
		switch s := st.(type) {
		case *radosclient.CreateStep:
			out = append(out, fmt.Sprintf("create(%t)", s.Exclusive))
		case *radosclient.RemoveStep:
			out = append(out, "remove")
		case *radosclient.StepFlagsStep:
			if s.Flags == radosclient.StepFlagFailOK {
				out = append(out, "failok")
			} else {
				out = append(out, fmt.Sprintf("flags(%#x)", uint32(s.Flags)))
			}
		case *radosclient.ExecStep:
			out = append(out, s.Class+"."+s.Method)
		case *radosclient.OmapSetStep:
			keys := make([]string, 0, len(s.Values))
			for k := range s.Values {
				keys = append(keys, k)
			}
			out = append(out, "omap_set("+strings.Join(keys, ",")+")")
		case *radosclient.StatStep:
			out = append(out, "stat")
		default:
			out = append(out, fmt.Sprintf("%T", st))
		}
	}
	return out
}

var _ = Describe("the otp metadata section", func() {
	const rookControlPool = "ceph-objectstore.rgw.control"
	var (
		c *fakerados.Cluster
		s *driver.Store
	)
	open := func(ctx context.Context, rel denc.Release) *driver.Store {
		GinkgoHelper()
		st, err := driver.Open(ctx, c, conf(map[string]string{"rgw_cache_enabled": "false"}), driver.Options{Release: &rel})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(st.Close)
		return st
	}
	BeforeEach(func(ctx SpecContext) {
		otptest.RedactDevices()
		c = newIndexCluster()
		registerOTPClass(c, time.Now)
		s = open(ctx, denc.Squid)
	})
	object := func(key string) *fakerados.Object { return c.Object(rookMetaPool, rookOTPNS, key) }
	// seedDevices stores devs in key as radosgw-admin mfa create stores one,
	// otp_set under the version v at the mtime t, in one op.
	seedDevices := func(ctx context.Context, key string, v meta.ObjVersion, t time.Time, devs ...otp.OTPInfo) {
		GinkgoHelper()
		p, err := c.Pool(ctx, rookMetaPool, rookOTPNS)
		Expect(err).NotTo(HaveOccurred())
		w := radosclient.NewWriteOp()
		version.Set(w, version.ObjVersion(v), denc.Squid)
		w.SetMtime(t)
		otp.Set(w, devs, denc.Squid)
		_, err = p.Write(ctx, key, w, radosclient.OpFlagNone)
		Expect(err).NotTo(HaveOccurred())
		c.ResetCounters()
	}
	record := func(key, id string) otp.Instance {
		GinkgoHelper()
		Expect(object(key)).NotTo(BeNil(), key)
		b, ok := object(key).Omap[otp.InstanceKey(id)]
		Expect(ok).To(BeTrue(), "%s holds the record of %s", key, id)
		d := denc.NewDecoder(b)
		inst := otp.DecodeInstance(d)
		Expect(d.Err()).NotTo(HaveOccurred())
		return inst
	}
	storedVersion := func(key string) meta.ObjVersion {
		GinkgoHelper()
		Expect(object(key)).NotTo(BeNil(), key)
		d := denc.NewDecoder(object(key).Xattrs[version.XattrName])
		v := version.DecodeObjVersion(d)
		Expect(d.Err()).NotTo(HaveOccurred())
		return meta.ObjVersion(v)
	}
	// classHeader is the header the class itself writes for the devices ids,
	// read from an object only otp_set has written.
	classHeader := func(ctx context.Context, ids ...string) []byte {
		GinkgoHelper()
		devs := make([]otp.OTPInfo, 0, len(ids))
		for _, id := range ids {
			devs = append(devs, otpDevice(id))
		}
		seedDevices(ctx, "header-reference", meta.ObjVersion{Ver: 1, Tag: "r"}, time.Time{}, devs...)
		return object("header-reference").Omap[otp.HeaderKey]
	}
	document := func(devs ...otp.OTPInfo) json.RawMessage {
		GinkgoHelper()
		b, err := json.Marshal(op.OTPDocument{Devices: devs})
		Expect(err).NotTo(HaveOccurred())
		return b
	}
	put := func(ctx context.Context, e op.MetadataEntry) error {
		return s.Put(ctx, "otp", otpKey, e, op.PutMetadataOptions{})
	}
	get := func(ctx context.Context) op.MetadataEntry {
		GinkgoHelper()
		e, err := s.Get(ctx, "otp", otpKey)
		Expect(err).NotTo(HaveOccurred())
		return e
	}
	withoutSeedBin := func(devs ...otp.OTPInfo) []otp.OTPInfo {
		for i := range devs {
			devs[i].SeedBin = nil
		}
		return devs
	}
	notifies := func() []meta.CacheNotifyInfo {
		GinkgoHelper()
		var out []meta.CacheNotifyInfo
		for i := range 8 {
			for _, p := range c.Notifies(rookControlPool, "", "notify."+strconv.Itoa(i)) {
				d := denc.NewDecoder(p)
				out = append(out, meta.DecodeCacheNotifyInfo(d))
				Expect(d.Err()).NotTo(HaveOccurred())
			}
		}
		return out
	}

	Describe("a get", func() {
		It("answers the object's devices in one read op of a stat, the version and otp_get's get_all", func(ctx SpecContext) {
			seedDevices(ctx, otpKey, meta.ObjVersion{Ver: 3, Tag: "t"}, otpStamp, otpDevice("b"), otpDevice("a"))
			e := get(ctx)
			want := op.OTPDocument{Devices: []otp.OTPInfo{otpDevice("a"), otpDevice("b")}}
			Expect(docOf[op.OTPDocument](e)).To(Equal(want), "the header's order, without seed_bin")
			got, err := op.DecodeOTPDocument(e.Data)
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(want), "the JSON is the document")
			Expect(e.Version).To(Equal(meta.ObjVersion{Ver: 3, Tag: "t"}))
			Expect(e.Mtime).To(BeTemporally("==", otpStamp))
			Expect(e.Key).To(Equal(otpKey))
			Expect(c.Reads(rookMetaPool, rookOTPNS, otpKey)).To(Equal(1))
			Expect(otpSteps(c.LastRead(rookMetaPool, rookOTPNS, otpKey).Steps())).To(Equal([]string{"stat", "version.read", "otp.otp_get"}))
			Expect(notifies()).To(BeEmpty(), "radosgw's otp get reads through no cache")
		})

		It("answers a missing object ErrNoSuchKey, and an object whose header names a missing record too, as get_all fails", func(ctx SpecContext) {
			_, err := s.Get(ctx, "otp", otpKey)
			Expect(err).To(MatchError(op.ErrNoSuchKey))
			seedDevices(ctx, otpKey, meta.ObjVersion{Ver: 1, Tag: "t"}, otpStamp, otpDevice("a"), otpDevice("b"))
			delete(object(otpKey).Omap, otp.InstanceKey("b"))
			_, err = s.Get(ctx, "otp", otpKey)
			Expect(err).To(MatchError(op.ErrNoSuchKey))
		})
	})

	It("lists every object of the otp pool, each key its object's name, across pages", func(ctx SpecContext) {
		names := []string{"user:a", "user:b", "user:t$c", "user:d", "not-a-user"}
		for _, n := range names {
			seedDevices(ctx, n, meta.ObjVersion{Ver: 1, Tag: "t"}, otpStamp, otpDevice("x"))
		}
		var all []string
		marker := ""
		for range 10 {
			keys, next, more, err := s.List(ctx, "otp", marker, 2)
			Expect(err).NotTo(HaveOccurred())
			Expect(len(keys)).To(BeNumerically("<=", 2))
			all = append(all, keys...)
			if !more {
				break
			}
			marker = next
		}
		Expect(all).To(ConsistOf(names))
		Expect(s.Sections()).To(ContainElement("otp"))
	})

	Describe("a put", func() {
		It("writes the document in one op: remove allowed to fail, create, version, otp_set, then the header", func(ctx SpecContext) {
			Expect(put(ctx, op.MetadataEntry{Data: document(otpDevice("b"), otpDevice("a")), Version: meta.ObjVersion{Ver: 5, Tag: "doc"}, Mtime: otpStamp})).To(Succeed())
			Expect(c.Writes(rookMetaPool, rookOTPNS, otpKey)).To(Equal(1))
			w := c.LastWrite(rookMetaPool, rookOTPNS, otpKey)
			Expect(otpSteps(w.Steps())).To(Equal([]string{"remove", "failok", "create(false)", "version.set", "otp.otp_set", "omap_set(header)"}))
			mtime, ok := w.Mtime()
			Expect(ok).To(BeTrue(), "the op carries the document's mtime")
			Expect(mtime).To(BeTemporally("==", otpStamp))
			Expect(storedVersion(otpKey)).To(Equal(meta.ObjVersion{Ver: 5, Tag: "doc"}))
			Expect(object(otpKey).Mtime).To(BeTemporally("==", otpStamp))
			Expect(object(otpKey).Omap[otp.HeaderKey]).To(Equal(classHeader(ctx, "a", "b")), "the class's own bytes")
			Expect(bytes.Equal(record(otpKey, "a").OTP.SeedBin, []byte("12345678901234567890"))).To(BeTrue(), "otp_set parsed the seed")
			Expect(docOf[op.OTPDocument](get(ctx)).Devices).To(Equal([]otp.OTPInfo{otpDevice("a"), otpDevice("b")}))
			Expect(notifies()).To(BeEmpty(), "radosgw's otp put writes through no cache")
		})

		It("keeps the stored mtime for a document without one, which the OSD keeps through the op's remove and create", func(ctx SpecContext) {
			seedDevices(ctx, otpKey, meta.ObjVersion{Ver: 1, Tag: "t"}, otpStamp, otpDevice("a"))
			Expect(put(ctx, op.MetadataEntry{Data: document(otpDevice("b"))})).To(Succeed())
			mtime, ok := c.LastWrite(rookMetaPool, rookOTPNS, otpKey).Mtime()
			Expect(ok).To(BeTrue())
			Expect(mtime.IsZero()).To(BeTrue(), "prepare_mfa_write sends the zero mtime as the zero timespec")
			Expect(object(otpKey).Mtime).To(BeTemporally("==", otpStamp))
			Expect(get(ctx).Mtime).To(BeTemporally("==", otpStamp))
		})

		DescribeTable("writes the version prepare_mfa_write does",
			func(ctx SpecContext, stored *meta.ObjVersion, ver meta.ObjVersion, step string, want func(meta.ObjVersion) bool) {
				if stored != nil {
					seedDevices(ctx, otpKey, *stored, otpStamp, otpDevice("a"))
				}
				Expect(put(ctx, op.MetadataEntry{Data: document(otpDevice("a")), Version: ver, Mtime: otpStamp})).To(Succeed())
				Expect(otpSteps(c.LastWrite(rookMetaPool, rookOTPNS, otpKey).Steps())).To(ContainElement(step))
				Expect(want(storedVersion(otpKey))).To(BeTrue(), "stored %+v", storedVersion(otpKey))
			},
			Entry("a fresh tag at version 1 for a ver without a tag, over a stored object too",
				&meta.ObjVersion{Ver: 7, Tag: "old"}, meta.ObjVersion{Ver: 9}, "version.set", func(v meta.ObjVersion) bool {
					return v.Ver == 1 && len(v.Tag) == 24 && v.Tag[0] == '_'
				}),
			Entry("a fresh tag at version 1 for a new object", nil, meta.ObjVersion{}, "version.set", func(v meta.ObjVersion) bool {
				return v.Ver == 1 && len(v.Tag) == 24 && v.Tag[0] == '_'
			}),
			Entry("the document's ver when it has a tag", &meta.ObjVersion{Ver: 7, Tag: "old"}, meta.ObjVersion{Ver: 3, Tag: "doc"}, "version.set",
				func(v meta.ObjVersion) bool { return v == meta.ObjVersion{Ver: 3, Tag: "doc"} }),
			Entry("an increment of the stored version for a tag at version 0, as prepare_op_for_write sends cls_version_inc",
				&meta.ObjVersion{Ver: 7, Tag: "old"}, meta.ObjVersion{Tag: "doc"}, "version.inc",
				func(v meta.ObjVersion) bool { return v == meta.ObjVersion{Ver: 8, Tag: "old"} }),
			Entry("version 2 under the class's fresh tag for a tag at version 0 over nothing, as cls_version_inc initializes then increments",
				nil, meta.ObjVersion{Tag: "doc"}, "version.inc",
				func(v meta.ObjVersion) bool { return v.Ver == 2 && v.Tag != "" && v.Tag != "doc" }),
		)

		It("keeps a remaining device's replay state, and leaves the header exactly the document's ids", func(ctx SpecContext) {
			seedDevices(ctx, otpKey, meta.ObjVersion{Ver: 2, Tag: "t"}, otpStamp, otpDevice("keep"), otpDevice("drop"))
			kept := record(otpKey, "keep")
			kept.LastChecks = []otp.OTPCheck{{Token: "tok", Timestamp: otpStamp, Result: otp.CheckSuccess}}
			kept.LastSuccess = 58_000_000
			e := denc.NewEncoder()
			kept.Encode(e, denc.Squid)
			object(otpKey).Omap[otp.InstanceKey("keep")] = e.Bytes()
			changed := otpDevice("keep")
			changed.Window = 3
			Expect(put(ctx, op.MetadataEntry{Data: document(changed, otpDevice("add")), Mtime: otpStamp})).To(Succeed())
			after := record(otpKey, "keep")
			Expect(after.LastChecks).To(Equal(kept.LastChecks), "otp_set read the record the op's remove dropped")
			Expect(after.LastSuccess).To(Equal(kept.LastSuccess))
			Expect(after.OTP.Window).To(Equal(uint32(3)))
			Expect(slices.Sorted(maps.Keys(object(otpKey).Omap))).To(Equal([]string{otp.HeaderKey, otp.InstanceKey("add"), otp.InstanceKey("keep")}),
				"the dropped device's record is gone")
			Expect(object(otpKey).Omap[otp.HeaderKey]).To(Equal(classHeader(ctx, "add", "keep")), "no id of the dropped device")
			Expect(docOf[op.OTPDocument](get(ctx)).Devices).To(Equal(withoutSeedBin(otpDevice("add"), changed)), "get_all answers the remaining devices")
		})

		It("writes a document without devices as an object with an empty header", func(ctx SpecContext) {
			seedDevices(ctx, otpKey, meta.ObjVersion{Ver: 2, Tag: "t"}, otpStamp, otpDevice("a"))
			Expect(put(ctx, op.MetadataEntry{Data: json.RawMessage(`{"devices":[]}`), Mtime: otpStamp})).To(Succeed())
			Expect(slices.Sorted(maps.Keys(object(otpKey).Omap))).To(Equal([]string{otp.HeaderKey}))
			Expect(object(otpKey).Omap[otp.HeaderKey]).To(Equal(classHeader(ctx)))
			Expect(docOf[op.OTPDocument](get(ctx)).Devices).To(BeEmpty())
		})

		It("takes a seed of exactly 1024 bytes, which the class parses", func(ctx SpecContext) {
			long := otpDevice("long")
			long.Seed = strings.Repeat("ab", 512)
			Expect(put(ctx, op.MetadataEntry{Data: document(long), Mtime: otpStamp})).To(Succeed())
			Expect(len(record(otpKey, "long").OTP.SeedBin)).To(Equal(512)) //nolint:ginkgolinter // HaveLen would print the seed_bin on a failure
		})

		It("takes a window of 1024 and a step of 3600, the bounds", func(ctx SpecContext) {
			wide := otpDevice("wide")
			wide.Window, wide.StepSize = 1024, 3600
			Expect(put(ctx, op.MetadataEntry{Data: document(wide), Mtime: otpStamp})).To(Succeed())
			Expect(record(otpKey, "wide").OTP.Window).To(Equal(uint32(1024)))
			Expect(record(otpKey, "wide").OTP.StepSize).To(Equal(uint32(3600)))
		})

		It("still reads back a device radosgw wrote past the bounds, as only writes are bounded", func(ctx SpecContext) {
			wide := otpDevice("wide")
			wide.Window, wide.StepSize, wide.Seed = 4294967295, 4294967295, strings.Repeat("ab", 1024)
			seedDevices(ctx, otpKey, meta.ObjVersion{Ver: 1, Tag: "t"}, otpStamp, wide)
			devs := docOf[op.OTPDocument](get(ctx)).Devices
			Expect(devs).To(HaveLen(1))
			Expect(devs[0].Window).To(Equal(uint32(4294967295)))
			Expect(len(devs[0].Seed)).To(Equal(2048)) //nolint:ginkgolinter // HaveLen would print the seed on a failure
		})

		DescribeTable("refuses with InvalidArgument before any write",
			func(ctx SpecContext, data string) {
				seedDevices(ctx, otpKey, meta.ObjVersion{Ver: 2, Tag: "t"}, otpStamp, otpDevice("a"))
				Expect(put(ctx, op.MetadataEntry{Data: json.RawMessage(data), Mtime: otpStamp})).To(MatchError(op.ErrInvalidArgument))
				Expect(c.Writes(rookMetaPool, rookOTPNS, otpKey)).To(BeZero())
			},
			Entry("a step_size of 0", `{"devices":[{"type":2,"id":"a","seed":"`+rfcHexSeed+`","seed_type":"hex","step_size":0}]}`),
			Entry("an HOTP device", `{"devices":[{"type":1,"id":"a","seed":"`+rfcHexSeed+`","seed_type":"hex","step_size":30}]}`),
			Entry("a seed type of unknown", `{"devices":[{"type":2,"id":"a","seed":"`+rfcHexSeed+`","seed_type":"unknown","step_size":30}]}`),
			Entry("a seed of 1025 bytes", `{"devices":[{"type":2,"id":"a","seed":"`+strings.Repeat("a", 1025)+`","seed_type":"hex","step_size":30}]}`),
			Entry("a window of 1025", `{"devices":[{"type":2,"id":"a","seed":"`+rfcHexSeed+`","seed_type":"hex","step_size":30,"window":1025}]}`),
			Entry("a step of 3601", `{"devices":[{"type":2,"id":"a","seed":"`+rfcHexSeed+`","seed_type":"hex","step_size":3601}]}`),
			Entry("a second device whose seed is over the bound, the first being good",
				`{"devices":[{"type":2,"id":"a","seed":"`+rfcHexSeed+`","seed_type":"hex","step_size":30},`+
					`{"type":2,"id":"b","seed":"`+strings.Repeat("ab", 513)+`","seed_type":"hex","step_size":30}]}`),
			Entry("an id given null", `{"devices":[{"type":2,"id":null,"seed":"`+rfcHexSeed+`","seed_type":"hex","step_size":30}]}`),
			Entry("an empty seed, which the class would parse into an empty key", `{"devices":[{"type":2,"id":"a","seed":"","seed_type":"hex","step_size":30}]}`),
			Entry("an empty id", `{"devices":[{"type":2,"id":"","seed":"`+rfcHexSeed+`","seed_type":"hex","step_size":30}]}`),
			Entry("a hex seed led by a NUL, which the class would parse into an empty key",
				`{"devices":[{"type":2,"id":"a","seed":"`+"\\u0000"+rfcHexSeed+`","seed_type":"hex","step_size":30}]}`),
			Entry("a base32 seed of nothing but spaces, which the class would parse into an empty key",
				`{"devices":[{"type":2,"id":"a","seed":"    ","seed_type":"base32","step_size":30}]}`),
			Entry("a seed given an object", `{"devices":[{"type":2,"id":"a","seed":{},"seed_type":"hex","step_size":30}]}`),
			Entry("a seed type given an array", `{"devices":[{"type":2,"id":"a","seed":"`+rfcHexSeed+`","seed_type":["hex"],"step_size":30}]}`),
			Entry("devices given an object", `{"devices":{}}`),
			Entry("a document that is not JSON", `{"devices":[`),
		)

		It("refuses a key holding a NUL byte in every verb before it reads or writes", func(ctx SpecContext) {
			key := "user:al\x00ice"
			_, err := s.Get(ctx, "otp", key)
			Expect(err).To(MatchError(op.ErrInvalidArgument))
			Expect(s.Put(ctx, "otp", key, op.MetadataEntry{Data: document(otpDevice("a"))}, op.PutMetadataOptions{})).To(MatchError(op.ErrInvalidArgument))
			Expect(s.Remove(ctx, "otp", key)).To(MatchError(op.ErrInvalidArgument))
			Expect(c.Objects(rookMetaPool, rookOTPNS)).To(BeEmpty())
			Expect(c.Reads(rookMetaPool, rookOTPNS, "user:al")).To(BeZero())
		})

		It("answers a seed the class cannot parse InvalidArgument, the op writing nothing", func(ctx SpecContext) {
			seedDevices(ctx, otpKey, meta.ObjVersion{Ver: 2, Tag: "t"}, otpStamp, otpDevice("a"))
			ver, omap := object(otpKey).Version, maps.Clone(object(otpKey).Omap)
			bad := otpDevice("bad")
			bad.Seed = "zz"
			Expect(put(ctx, op.MetadataEntry{Data: document(otpDevice("a"), bad), Mtime: otpStamp})).To(MatchError(op.ErrInvalidArgument))
			Expect(c.Writes(rookMetaPool, rookOTPNS, otpKey)).To(Equal(1))
			Expect(object(otpKey).Version).To(Equal(ver), "parse_seed's EINVAL fails the whole op")
			Expect(maps.EqualFunc(object(otpKey).Omap, omap, bytes.Equal)).To(BeTrue(), "the object's records and header are as they were")
		})
	})

	Describe("a remove", func() {
		It("removes the object under the version its read found, telling every gateway's cache, as radosgw's sysobj remove does", func(ctx SpecContext) {
			seedDevices(ctx, otpKey, meta.ObjVersion{Ver: 4, Tag: "t"}, otpStamp, otpDevice("a"))
			Expect(s.Remove(ctx, "otp", otpKey)).To(Succeed())
			Expect(object(otpKey)).To(BeNil())
			Expect(c.Reads(rookMetaPool, rookOTPNS, otpKey)).To(Equal(1), "RGWMetadataManager::remove reads first")
			Expect(otpSteps(c.LastWrite(rookMetaPool, rookOTPNS, otpKey).Steps())).To(Equal([]string{"version.check_conds", "version.inc", "remove"}))
			sent := notifies()
			Expect(sent).To(HaveLen(1))
			Expect(sent[0].Op).To(Equal(meta.CacheInvalidateObj))
			Expect(sent[0].Obj).To(Equal(meta.RawObj{Pool: meta.ParsePool(rookMetaPool + ":" + rookOTPNS), OID: otpKey}))
			Expect(s.Remove(ctx, "otp", otpKey)).To(MatchError(op.ErrNoSuchKey))
		})

		It("refuses a remove a write lands under between its read and its removal", func(ctx SpecContext) {
			seedDevices(ctx, otpKey, meta.ObjVersion{Ver: 4, Tag: "t"}, otpStamp, otpDevice("a"))
			c.BeforeWrite(rookMetaPool, rookOTPNS, otpKey, func(o *fakerados.Object) {
				c.BeforeWrite(rookMetaPool, rookOTPNS, otpKey, nil)
				o.Xattrs[version.XattrName] = encode(version.ObjVersion{Ver: 5, Tag: "t"})
			})
			Expect(s.Remove(ctx, "otp", otpKey)).To(MatchError(op.ErrConcurrentModification))
			Expect(object(otpKey)).NotTo(BeNil())
		})
	})

	Describe("through the admin ops", func() {
		run := func(ctx context.Context, st *driver.Store, o op.Op) error {
			u := meta.NewUserInfo()
			u.UserID = meta.UserID{ID: "admin"}
			u.Caps = meta.Caps{"metadata": meta.CapAll}
			return op.Run(ctx, o, &op.Request{Env: st.Env(), Identity: op.Identity{
				User: &u, Owner: meta.UserOwner(u.UserID), OpMask: op.OpTypeAll, Caps: u.Caps,
			}})
		}
		envelope := func(ver meta.ObjVersion, mtime string, data json.RawMessage) []byte {
			GinkgoHelper()
			m := map[string]any{"key": "otp:" + otpKey, "ver": ver, "data": data}
			if mtime != "" {
				m["mtime"] = mtime
			}
			b, err := json.Marshal(m)
			Expect(err).NotTo(HaveOccurred())
			return b
		}

		DescribeTable("answers Squid's applied status and Tentacle's none, reporting the zero version on both",
			func(ctx SpecContext, rel denc.Release, status string) {
				st := open(ctx, rel)
				seedDevices(ctx, otpKey, meta.ObjVersion{Ver: 4, Tag: "t"}, otpStamp, otpDevice("a"))
				o := &op.PutMetadata{Section: "otp", Key: otpKey, Body: envelope(meta.ObjVersion{Ver: 9, Tag: "t"}, "", document(otpDevice("b")))}
				Expect(run(ctx, st, o)).To(Succeed())
				Expect(o.Applied).To(BeTrue())
				Expect(o.UpdateStatus()).To(Equal(status))
				Expect(o.OnDisk).To(Equal(meta.ObjVersion{}), "RGWX_UPDATE_VERSION reports ver:0,tag:")
				Expect(storedVersion(otpKey)).To(Equal(meta.ObjVersion{Ver: 9, Tag: "t"}))
			},
			Entry("on Squid, whose do_put answers STATUS_APPLIED", denc.Squid, "applied"),
			Entry("on Tentacle, whose put answers what the mdlog's complete_entry returns", denc.Tentacle, ""),
		)

		DescribeTable("honors update-type, writing nothing for a put it skips",
			func(ctx SpecContext, rel denc.Release, sync op.SyncType, ver meta.ObjVersion, mtime string, applied bool) {
				st := open(ctx, rel)
				seedDevices(ctx, otpKey, meta.ObjVersion{Ver: 5, Tag: "t"}, otpStamp, otpDevice("a"))
				o := &op.PutMetadata{Section: "otp", Key: otpKey, Sync: sync, Body: envelope(ver, mtime, document(otpDevice("b")))}
				Expect(run(ctx, st, o)).To(Succeed())
				Expect(o.Applied).To(Equal(applied))
				if !applied {
					Expect(o.UpdateStatus()).To(Equal("skipped"))
					Expect(c.Writes(rookMetaPool, rookOTPNS, otpKey)).To(BeZero())
					return
				}
				Expect(record(otpKey, "b").OTP.ID).To(Equal("b"))
			},
			Entry("by version, a lower version on Squid", denc.Squid, op.SyncByVersion, meta.ObjVersion{Ver: 4, Tag: "t"}, "", false),
			Entry("by version, a lower version on Tentacle, whose radosgw applies it", denc.Tentacle, op.SyncByVersion, meta.ObjVersion{Ver: 4, Tag: "t"}, "", false),
			Entry("by version, another tag", denc.Squid, op.SyncByVersion, meta.ObjVersion{Ver: 6, Tag: "u"}, "", false),
			Entry("by version, a higher version", denc.Squid, op.SyncByVersion, meta.ObjVersion{Ver: 6, Tag: "t"}, "", true),
			Entry("by timestamp, an older mtime", denc.Squid, op.SyncByTimestamp, meta.ObjVersion{}, "2026-10-19T00:00:00.000000Z", false),
			Entry("by timestamp, a newer mtime", denc.Squid, op.SyncByTimestamp, meta.ObjVersion{}, "2026-10-21T00:00:00.000000Z", true),
		)

		It("round-trips a document through a get and a put", func(ctx SpecContext) {
			seedDevices(ctx, otpKey, meta.ObjVersion{Ver: 4, Tag: "t"}, otpStamp, otpDevice("b"), otpDevice("a"))
			g := &op.GetMetadata{Section: "otp", Key: otpKey}
			Expect(run(ctx, s, g)).To(Succeed())
			Expect(s.Remove(ctx, "otp", otpKey)).To(Succeed())
			o := &op.PutMetadata{Section: "otp", Key: otpKey, Body: envelope(g.Result.Version, meta.Time{Time: g.Result.Mtime}.Gmtime(), g.Result.Data)}
			Expect(run(ctx, s, o)).To(Succeed())
			again := &op.GetMetadata{Section: "otp", Key: otpKey}
			Expect(run(ctx, s, again)).To(Succeed())
			Expect(again.Result.Doc).To(Equal(g.Result.Doc))
			Expect(again.Result.Version).To(Equal(g.Result.Version))
			Expect(again.Result.Mtime).To(BeTemporally("==", g.Result.Mtime))
		})
	})

	Describe("through the admin API", func() {
		const (
			secret    = "SEEDSECRETSEEDSECRET"
			goodSeed  = "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP"
			goodSeedB = "KRSXG5BAMVXGG4TZOB2GK3TEEBYGK4TP"
		)
		var (
			logs    *syncBuffer
			handler http.Handler
		)
		handlerFor := func(st *driver.Store) http.Handler {
			auth := s3.AuthenticatorFunc(func(context.Context, *http.Request, op.PayloadForms) (*op.AuthResult, error) {
				u := meta.NewUserInfo()
				u.UserID = meta.UserID{ID: "admin"}
				u.Caps = meta.Caps{"metadata": meta.CapAll}
				return &op.AuthResult{Identity: op.Identity{User: &u, Owner: meta.UserOwner(u.UserID), OpMask: op.OpTypeAll, Caps: u.Caps}}, nil
			})
			return admin.NewHandler(st.Env(), auth, admin.Config{Prefix: "admin"})
		}
		BeforeEach(func() {
			logs = &syncBuffer{}
			DeferCleanup(driver.CaptureDebugLog(logs))
			handler = handlerFor(s)
		})
		serve := func(ctx context.Context, h http.Handler, method, body string) *httptest.ResponseRecorder {
			req := httptest.NewRequestWithContext(ctx, method, "/admin/metadata/otp?key="+otpKey, strings.NewReader(body))
			req.ContentLength = int64(len(body))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			return rec
		}
		device := func(seed string, step int) string {
			return `{"type":2,"id":"phone","seed":"` + seed + `","seed_type":"base32","time_ofs":0,"step_size":` + strconv.Itoa(step) + `,"window":2}`
		}
		putBody := func(devs ...string) string { return `{"data":{"devices":[` + strings.Join(devs, ",") + `]}}` }

		DescribeTable("answers a put's status and headers as each release's radosgw does",
			func(ctx SpecContext, rel denc.Release, code int, status []string) {
				rec := serve(ctx, handlerFor(open(ctx, rel)), http.MethodPut, putBody(device(goodSeed, 30)))
				Expect(rec.Code).To(Equal(code))
				Expect(headerAt(rec.Header(), "RGWX_UPDATE_STATUS")).To(Equal(status))
				Expect(headerAt(rec.Header(), "RGWX_UPDATE_VERSION")).To(Equal([]string{"ver:0,tag:"}))
			},
			Entry("Squid's 204 applied", denc.Squid, http.StatusNoContent, []string{"applied"}),
			Entry("Tentacle's 200 without a status", denc.Tentacle, http.StatusOK, nil),
		)

		It("names no seed in a log line or an error, whatever the document", func(ctx SpecContext) {
			for _, body := range []string{
				`{"data":{"devices":[{"seed":"SEED\uSECRET"}]}}`, // encoding/json quotes a bad \u escape
				`{"data":{"devices":[{"seed":"` + secret + `"`,
				putBody(device(secret, 0)),
				putBody(device(strings.Repeat(secret, 60), 30)),
				`{"data":{"devices":[{"type":2,"id":"phone","seed":{"k":"` + secret + `"},"seed_type":"base32","step_size":30}]}}`,
			} {
				rec := serve(ctx, handler, http.MethodPut, body)
				Expect(rec.Code).To(Equal(http.StatusBadRequest))
				Expect(strings.Contains(rec.Body.String(), "SECR")).To(BeFalse(), "a refusal's body quotes the seed")
			}
			c.FailNextWrite(rookMetaPool, rookOTPNS, otpKey, syscall.EIO)
			rec := serve(ctx, handler, http.MethodPut, putBody(device(goodSeedB, 30)))
			Expect(rec.Code).To(BeNumerically(">=", 500), "an EIO from the OSD, which WriteError logs with its cause")
			Expect(serve(ctx, handler, http.MethodPut, putBody(device(goodSeed, 30))).Code).To(Equal(http.StatusNoContent))
			got := serve(ctx, handler, http.MethodGet, "")
			Expect(got.Code).To(Equal(http.StatusOK))
			Expect(strings.Contains(got.Body.String(), goodSeed)).To(BeTrue(), "a get answers the seed, as radosgw's does")
			Expect(serve(ctx, handler, http.MethodDelete, "").Code).To(Equal(http.StatusOK))
			out := logs.String()
			Expect(out).To(ContainSubstring("admin request failed"), "the server error was logged with its cause")
			Expect(out).To(ContainSubstring("admin request done"), "the debug lines were captured")
			for _, seed := range []string{"SECR", goodSeed, goodSeedB} {
				Expect(strings.Contains(out, seed)).To(BeFalse(), "a log line names a seed")
			}
		})
	})
})
