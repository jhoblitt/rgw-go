package fakerados_test

import (
	"context"
	"encoding/hex"
	"maps"
	"slices"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cls/otp"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

const otpOID = "user:alice"

// The bytes the otp class stores
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L45-L95 at v19.2.7 and v20.2.4).
const (
	// otp_header (1,1), 20 bytes: the ids dev1 and dev2.
	otpHeaderDev1Dev2Hex = "010114000000" + "02000000" + "04000000" + "64657631" + "04000000" + "64657632"
	// otp_header (1,1), 4 bytes: no ids.
	otpHeaderEmptyHex = "010104000000" + "00000000"
	// otp_instance (1,1), 63 bytes: otpDev1 with seed_bin "12345", no checks, last_success 0.
	otpInstanceDev1Hex = "01013f000000" +
		// otp_info_t (1,1), 45 bytes: TOTP, "dev1", seed "3132333435" as hex, seed_bin "12345",
		// time_ofs 0, step_size 30, window 2.
		"01012d000000" + "02" + "04000000" + "64657631" + "0a000000" + "33313332333333343335" +
		"01" + "05000000" + "3132333435" + "00000000" + "1e000000" + "02000000" +
		"00000000" + "0000000000000000"
)

// otpCheck is a check a stored device records.
func otpCheck() otp.OTPCheck {
	return otp.OTPCheck{Token: "tok", Timestamp: time.Unix(1760000000, 0).UTC(), Result: otp.CheckSuccess}
}

// otpDev1 is a hex-seeded device whose seed parses to "12345".
func otpDev1() otp.OTPInfo {
	return otp.OTPInfo{Type: otp.TypeTOTP, ID: "dev1", Seed: "3132333435", SeedType: otp.SeedHex, StepSize: 30, Window: 2}
}

// otpDev2 is a base32-seeded device whose seed, lower case and spaced, parses
// to "12345" too.
func otpDev2() otp.OTPInfo {
	return otp.OTPInfo{Type: otp.TypeTOTP, ID: "dev2", Seed: "gezd gnbv", SeedType: otp.SeedBase32, StepSize: 30, Window: 2}
}

// parsed is dev with the seed_bin the class stores for it.
func parsed(dev otp.OTPInfo, seedBin string) otp.OTPInfo {
	dev.SeedBin = []byte(seedBin)
	return dev
}

func unhexOTP(s string) []byte {
	GinkgoHelper()
	b, err := hex.DecodeString(s)
	Expect(err).NotTo(HaveOccurred())
	return b
}

// encodeOTPHeader encodes otp_header with ids, which must be in set order.
func encodeOTPHeader(ids ...string) []byte {
	e := denc.NewEncoder()
	f := e.BeginStruct(1, 1)
	denc.EncodeSlice(e, ids, (*denc.Encoder).String)
	e.EndStruct(f)
	return e.Bytes()
}

// encodeOTPInstance encodes otp_instance: the device, its checks and the
// counter of its last successful check.
func encodeOTPInstance(dev otp.OTPInfo, checks []otp.OTPCheck, lastSuccess uint64) []byte {
	e := denc.NewEncoder()
	f := e.BeginStruct(1, 1)
	dev.Encode(e, denc.Squid)
	denc.EncodeSlice(e, checks, func(e *denc.Encoder, c otp.OTPCheck) { c.Encode(e, denc.Squid) })
	e.U64(lastSuccess)
	e.EndStruct(f)
	return e.Bytes()
}

// otpRequest is a request of the otp class.
type otpRequest interface {
	Encode(e *denc.Encoder, r denc.Release)
}

func encodeOTPRequest(v otpRequest) []byte {
	e := denc.NewEncoder()
	v.Encode(e, denc.Squid)
	return e.Bytes()
}

var _ = Describe("OTPClassAt", func() {
	var (
		c     *fakerados.Cluster
		p     radosclient.Pool
		clock time.Time
	)
	BeforeEach(func(ctx SpecContext) {
		c, p = newCluster(ctx)
		clock = time.Date(2026, 10, 19, 12, 30, 15, 250, time.UTC)
		c.RegisterClassMethods(otp.Class, fakerados.OTPClassAt(func() time.Time { return clock }), fakerados.OTPMethods, fakerados.OTPWriteMethods)
	})
	stored := func() *fakerados.Object { return c.Object(poolName, ns, otpOID) }
	// seed stores an object holding the header with ids and each instance.
	seed := func(ids []string, instances map[string][]byte) {
		c.Put(poolName, ns, otpOID, nil)
		stored().Omap["header"] = encodeOTPHeader(ids...)
		for id, b := range instances {
			stored().Omap["otp/"+id] = b
		}
	}
	set := func(ctx context.Context, devs ...otp.OTPInfo) error {
		return writeErr(ctx, p, otpOID, func(op *radosclient.WriteOp) { otp.Set(op, devs, denc.Squid) })
	}
	getAll := func(ctx context.Context) ([]otp.OTPInfo, error) {
		op := radosclient.NewReadOp()
		res := otp.GetAll(op, denc.Squid)
		if _, err := p.Read(ctx, otpOID, op, radosclient.OpFlagNone); err != nil {
			return nil, err
		}
		return res.Devices()
	}
	get := func(ctx context.Context, req otp.GetOp) ([]otp.OTPInfo, error) {
		op := radosclient.NewReadOp()
		res := op.Exec(otp.Class, otp.MethodGet, encodeOTPRequest(req))
		if _, err := p.Read(ctx, otpOID, op, radosclient.OpFlagNone); err != nil {
			return nil, err
		}
		out, err := res.Bytes()
		Expect(err).NotTo(HaveOccurred())
		d := denc.NewDecoder(out)
		reply := otp.DecodeGetReply(d)
		Expect(d.Err()).NotTo(HaveOccurred())
		return reply.FoundEntries, nil
	}
	remove := func(ctx context.Context, ids ...string) error {
		return writeErr(ctx, p, otpOID, func(op *radosclient.WriteOp) { otp.Remove(op, ids, denc.Squid) })
	}

	Describe("otp_set", func() {
		It("creates a missing object with each device under otp/<id>, its seed parsed, and the header listing their ids", func(ctx SpecContext) {
			Expect(set(ctx, otpDev2(), otpDev1())).To(Succeed())
			Expect(stored().Omap).To(HaveLen(3))
			Expect(stored().Omap).To(HaveKeyWithValue("header", unhexOTP(otpHeaderDev1Dev2Hex)), "otp_set_op, cls_otp.cc:301-348")
			Expect(stored().Omap).To(HaveKeyWithValue("otp/dev1", unhexOTP(otpInstanceDev1Hex)))
			Expect(stored().Omap).To(HaveKeyWithValue("otp/dev2", encodeOTPInstance(parsed(otpDev2(), "12345"), nil, 0)))
			Expect(stored().OmapHdr).To(BeEmpty(), "the header is the omap key \"header\", not the omap header")
		})

		It("writes the header even with no devices, creating the object", func(ctx SpecContext) {
			Expect(set(ctx)).To(Succeed())
			Expect(stored()).NotTo(BeNil())
			Expect(stored().Omap).To(Equal(map[string][]byte{"header": unhexOTP(otpHeaderEmptyHex)}))
		})

		It("replaces a stored device's otp_info_t but keeps its checks and last success, replacing the request's seed_bin", func(ctx SpecContext) {
			old := parsed(otpDev1(), "\x00")
			old.Seed = "00"
			seed([]string{"dev1"}, map[string][]byte{"dev1": encodeOTPInstance(old, []otp.OTPCheck{otpCheck()}, 1234)})
			next := otpDev1()
			next.Window = 5
			next.SeedBin = []byte("from the request")
			Expect(set(ctx, next)).To(Succeed())
			next.SeedBin = []byte("12345")
			Expect(stored().Omap["otp/dev1"]).To(Equal(encodeOTPInstance(next, []otp.OTPCheck{otpCheck()}, 1234)),
				"get_otp_instance before the replacement, cls_otp.cc:320-327")
		})

		It("adds its ids to those the header already lists", func(ctx SpecContext) {
			seed([]string{"dev0", "dev2"}, map[string][]byte{
				"dev0": encodeOTPInstance(parsed(otpDev1(), "12345"), nil, 0),
				"dev2": encodeOTPInstance(parsed(otpDev2(), "12345"), nil, 0),
			})
			Expect(set(ctx, otpDev1())).To(Succeed())
			Expect(stored().Omap["header"]).To(Equal(encodeOTPHeader("dev0", "dev1", "dev2")))
		})

		It("reads the header and devices as stored across radosgw's reset put, so replay state survives and a dropped device's id stays listed", func(ctx SpecContext) {
			seed([]string{"dev1", "dev2"}, map[string][]byte{
				"dev1": encodeOTPInstance(parsed(otpDev1(), "12345"), []otp.OTPCheck{otpCheck()}, 1234),
				"dev2": encodeOTPInstance(parsed(otpDev2(), "12345"), nil, 0),
			})
			// RGWSI_Cls::MFA::set_mfa with reset_obj
			// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/services/svc_cls.cc#L188-L216 at v19.2.7 and v20.2.4).
			// The OSD serves the class's cls_cxx_map_get_val from the stored
			// omap, which the earlier remove leaves in place until the op
			// commits.
			Expect(writeErr(ctx, p, otpOID, func(op *radosclient.WriteOp) {
				op.Remove()
				op.SetStepFlags(radosclient.StepFlagFailOK)
				op.Create(false)
				otp.Set(op, []otp.OTPInfo{otpDev1()}, denc.Squid)
			})).To(Succeed())
			Expect(stored().Omap).To(Equal(map[string][]byte{
				"header":   encodeOTPHeader("dev1", "dev2"),
				"otp/dev1": encodeOTPInstance(parsed(otpDev1(), "12345"), []otp.OTPCheck{otpCheck()}, 1234),
			}))
			_, err := getAll(ctx)
			Expect(err).To(MatchError(radosclient.ErrNotFound), "otp_get_op's get_otp_instance for dev2, cls_otp.cc:433-437")
		})

		It("reads a device's stored record after an OmapClear once its own first write has set FLAG_OMAP again", func(ctx SpecContext) {
			seed([]string{"dev0", "dev1", "dev2"}, map[string][]byte{
				"dev0": encodeOTPInstance(parsed(otpDev1(), "12345"), nil, 0),
				"dev1": encodeOTPInstance(parsed(otpDev1(), "12345"), []otp.OTPCheck{otpCheck()}, 11),
				"dev2": encodeOTPInstance(parsed(otpDev2(), "12345"), []otp.OTPCheck{otpCheck()}, 22),
			})
			// The clear hides the stored omap from the header read and the
			// first device's read; the class's write of dev1's record sets
			// FLAG_OMAP again, so the OSD serves dev2's read from the store.
			Expect(writeErr(ctx, p, otpOID, func(op *radosclient.WriteOp) {
				op.OmapClear()
				otp.Set(op, []otp.OTPInfo{otpDev1(), otpDev2()}, denc.Squid)
			})).To(Succeed())
			Expect(stored().Omap).To(Equal(map[string][]byte{
				"header":   encodeOTPHeader("dev1", "dev2"),
				"otp/dev1": encodeOTPInstance(parsed(otpDev1(), "12345"), nil, 0),
				"otp/dev2": encodeOTPInstance(parsed(otpDev2(), "12345"), []otp.OTPCheck{otpCheck()}, 22),
			}))
		})

		It("fails with EINVAL on a seed that does not parse, writing nothing", func(ctx SpecContext) {
			seed([]string{"dev2"}, map[string][]byte{"dev2": encodeOTPInstance(parsed(otpDev2(), "12345"), nil, 0)})
			before := maps.Clone(stored().Omap)
			bad := otpDev2()
			bad.Seed = "not base32"
			Expect(set(ctx, otpDev1(), bad)).To(MatchError(radosclient.ErrInvalid), "parse_seed, cls_otp.cc:268-299")
			Expect(stored().Omap).To(Equal(before))
		})

		It("fails with EIO on a stored device that does not decode", func(ctx SpecContext) {
			seed([]string{"dev1"}, map[string][]byte{"dev1": {1}})
			Expect(set(ctx, otpDev1())).To(haveErrno(syscall.EIO), "get_otp_instance, cls_otp.cc:184-190")
		})

		DescribeTable("parses the seed by its type as liboath does",
			func(ctx SpecContext, seedText string, seedType otp.SeedType, want string, parses bool) {
				dev := otpDev1()
				dev.Seed, dev.SeedType = seedText, seedType
				err := set(ctx, dev)
				if !parses {
					Expect(err).To(MatchError(radosclient.ErrInvalid), "parse_seed, cls_otp.cc:268-299")
					Expect(stored()).To(BeNil())
					return
				}
				Expect(err).NotTo(HaveOccurred())
				devs, err := getAll(ctx)
				Expect(err).NotTo(HaveOccurred())
				Expect(devs).To(HaveLen(1))
				Expect(string(devs[0].SeedBin)).To(Equal(want))
			},
			Entry("hex in lower case", "0a1b", otp.SeedHex, "\x0a\x1b", true),
			Entry("hex in upper case", "0A1B", otp.SeedHex, "\x0a\x1b", true),
			Entry("hex up to its first NUL, as oath_hex2bin reads a C string", "0a\x00zz", otp.SeedHex, "\x0a", true),
			Entry("an empty hex seed", "", otp.SeedHex, "", true),
			Entry("an unknown seed type, parsed as hex", "0a", otp.SeedUnknown, "\x0a", true),
			Entry("a seed type outside the enum, parsed as hex", "0a", otp.SeedType(7), "\x0a", true),
			Entry("base32 in lower case with spaces", "gezd gnbv", otp.SeedBase32, "12345", true),
			Entry("base32 of 2 characters, padded with 6", "MY", otp.SeedBase32, "f", true),
			Entry("base32 of 4 characters, padded with 4", "MZXQ", otp.SeedBase32, "fo", true),
			Entry("base32 of 5 characters, padded with 3", "MZXW6", otp.SeedBase32, "foo", true),
			Entry("base32 of 7 characters, padded with 1", "MZXW6YQ", otp.SeedBase32, "foob", true),
			Entry("base32 carrying its own padding", "MZXW6===", otp.SeedBase32, "foo", true),
			Entry("base32 with a padded block before another, as gnulib decodes block by block", "MY======MZXW6===", otp.SeedBase32, "ffoo", true),
			Entry("an empty base32 seed", "", otp.SeedBase32, "", true),
			Entry("a base32 seed of spaces alone", "   ", otp.SeedBase32, "", true),
			Entry("hex of an odd length", "0a1", otp.SeedHex, "", false),
			Entry("hex with a character outside it", "0g", otp.SeedHex, "", false),
			Entry("hex with a space", "0a 1b", otp.SeedHex, "", false),
			Entry("base32 given as hex", "GEZDGNBV", otp.SeedHex, "", false),
			Entry("base32 of 1 character", "M", otp.SeedBase32, "", false),
			Entry("base32 of 3 characters", "MZX", otp.SeedBase32, "", false),
			Entry("base32 of 6 characters", "MZXW6Y", otp.SeedBase32, "", false),
			Entry("base32 with padding where no block can end", "MZX=====", otp.SeedBase32, "", false),
			Entry("base32 with a digit outside its alphabet", "GEZDGNB1", otp.SeedBase32, "", false),
			Entry("base32 with a newline, which gnulib's decoder without a context refuses", "GEZDGNBV\n", otp.SeedBase32, "", false),
			Entry("base32 with a NUL, which oath_base32_decode reads by length", "GEZDGNB\x00", otp.SeedBase32, "", false),
		)
	})

	Describe("otp_get", func() {
		BeforeEach(func(ctx SpecContext) {
			Expect(set(ctx, otpDev2(), otpDev1())).To(Succeed())
		})

		It("answers every device the header lists, in its order, with the seed_bin stored", func(ctx SpecContext) {
			Expect(getAll(ctx)).To(Equal([]otp.OTPInfo{parsed(otpDev1(), "12345"), parsed(otpDev2(), "12345")}))
		})

		It("ignores the ids of a get_all request", func(ctx SpecContext) {
			Expect(get(ctx, otp.GetOp{GetAll: true, IDs: []string{"dev2"}})).To(HaveLen(2), "otp_get_op, cls_otp.cc:419-424")
		})

		It("answers the named ids the header lists, in the request's order and as often as named", func(ctx SpecContext) {
			Expect(get(ctx, otp.GetOp{IDs: []string{"dev2", "absent", "dev2", "dev1"}})).To(Equal([]otp.OTPInfo{
				parsed(otpDev2(), "12345"), parsed(otpDev2(), "12345"), parsed(otpDev1(), "12345"),
			}), "otp_get_op, cls_otp.cc:426-440")
		})

		It("answers no devices for an empty header value", func(ctx SpecContext) {
			c.Put(poolName, ns, otpOID, nil)
			stored().Omap["header"] = []byte{}
			Expect(getAll(ctx)).To(BeEmpty(), "read_header, cls_otp.cc:238-241")
		})

		It("answers no devices for an object without a header", func(ctx SpecContext) {
			c.Put(poolName, ns, otpOID, nil)
			Expect(getAll(ctx)).To(BeEmpty(), "read_header, cls_otp.cc:228-232")
		})

		It("fails with ENOENT on a listed id whose device is missing", func(ctx SpecContext) {
			delete(stored().Omap, "otp/dev2")
			_, err := getAll(ctx)
			Expect(err).To(MatchError(radosclient.ErrNotFound), "otp_get_op, cls_otp.cc:433-437")
		})

		It("fails with EIO on a listed device that does not decode", func(ctx SpecContext) {
			stored().Omap["otp/dev2"] = []byte{1, 1}
			_, err := getAll(ctx)
			Expect(err).To(haveErrno(syscall.EIO), "get_otp_instance, cls_otp.cc:184-190")
		})

		It("fails with EIO on a header that does not decode", func(ctx SpecContext) {
			stored().Omap["header"] = []byte{1}
			_, err := getAll(ctx)
			Expect(err).To(haveErrno(syscall.EIO), "read_header, cls_otp.cc:243-249")
		})

		It("fails with ENOENT on a missing object, which the OSD refuses before the class runs", func(ctx SpecContext) {
			c.Remove(poolName, ns, otpOID)
			_, err := getAll(ctx)
			Expect(err).To(MatchError(radosclient.ErrNotFound))
		})
	})

	Describe("otp_remove", func() {
		It("removes the devices it names that the header lists, and their ids", func(ctx SpecContext) {
			Expect(set(ctx, otpDev1(), otpDev2())).To(Succeed())
			Expect(remove(ctx, "dev1", "absent")).To(Succeed())
			Expect(stored().Omap).To(Equal(map[string][]byte{
				"header":   encodeOTPHeader("dev2"),
				"otp/dev2": encodeOTPInstance(parsed(otpDev2(), "12345"), nil, 0),
			}), "otp_remove_op, cls_otp.cc:350-394")
		})

		It("writes nothing when the header lists none of its ids", func(ctx SpecContext) {
			seed(nil, nil)
			// Bytes past the header's frame, which a rewrite would drop.
			stored().Omap["header"] = append(encodeOTPHeader("dev9"), 0xff)
			stored().Omap["otp/dev1"] = []byte("unlisted")
			before := maps.Clone(stored().Omap)
			Expect(remove(ctx, "dev1")).To(Succeed())
			Expect(stored().Omap).To(Equal(before), "removed_existing, cls_otp.cc:386-391")
		})

		It("leaves a missing object missing", func(ctx SpecContext) {
			Expect(remove(ctx, "dev1")).To(Succeed())
			Expect(stored()).To(BeNil())
		})

		It("fails with ENOENT when an earlier step removed the object, as OMAPRMKEYS does", func(ctx SpecContext) {
			Expect(set(ctx, otpDev1())).To(Succeed())
			err := writeErr(ctx, p, otpOID, func(op *radosclient.WriteOp) {
				op.Remove()
				otp.Remove(op, []string{"dev1"}, denc.Squid)
			})
			Expect(err).To(MatchError(radosclient.ErrNotFound))
			Expect(stored().Omap).To(HaveKey("otp/dev1"))
		})
	})

	Describe("get_current_time", func() {
		It("answers the class's clock rather than the cluster's", func(ctx SpecContext) {
			c.SetClock(func() time.Time { return time.Unix(1, 0) })
			c.Put(poolName, ns, otpOID, nil)
			op := radosclient.NewReadOp()
			res := otp.GetCurrentTime(op, denc.Squid)
			_, err := p.Read(ctx, otpOID, op, radosclient.OpFlagNone)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Time()).To(Equal(clock), "otp_get_current_time_op, cls_otp.cc:513-531")
		})

		It("reads time.Now through OTPClass", func(ctx SpecContext) {
			c.RegisterClassMethods(otp.Class, fakerados.OTPClass(), fakerados.OTPMethods, fakerados.OTPWriteMethods)
			c.Put(poolName, ns, otpOID, nil)
			before := time.Now()
			op := radosclient.NewReadOp()
			res := otp.GetCurrentTime(op, denc.Squid)
			_, err := p.Read(ctx, otpOID, op, radosclient.OpFlagNone)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Time()).To(BeTemporally(">=", before))
			Expect(res.Time()).To(BeTemporally("<=", time.Now()))
		})
	})

	DescribeTable("fails with EINVAL on a request that does not decode",
		func(ctx SpecContext, method string, write bool) {
			c.Put(poolName, ns, otpOID, nil)
			var err error
			if write {
				err = writeErr(ctx, p, otpOID, func(op *radosclient.WriteOp) { op.Exec(otp.Class, method, []byte{1}) })
			} else {
				op := radosclient.NewReadOp()
				op.Exec(otp.Class, method, []byte{1})
				_, err = p.Read(ctx, otpOID, op, radosclient.OpFlagNone)
			}
			Expect(err).To(MatchError(radosclient.ErrInvalid))
			Expect(stored().Omap).To(BeEmpty())
		},
		Entry("otp_set, cls_otp.cc:305-311", otp.MethodSet, true),
		Entry("otp_remove, cls_otp.cc:354-360", otp.MethodRemove, true),
		Entry("otp_get, cls_otp.cc:400-406", otp.MethodGet, false),
		Entry("get_current_time, cls_otp.cc:517-523", otp.MethodGetCurrentTime, false),
	)

	It("names the methods the class registers with CLS_METHOD_WR as its writes", func() {
		// https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L557-L575 at v19.2.7 and v20.2.4
		Expect(fakerados.OTPWriteMethods).To(ContainElements(otp.MethodSet, otp.MethodRemove))
		Expect(fakerados.OTPWriteMethods).NotTo(ContainElements(otp.MethodGet, otp.MethodGetCurrentTime))
		Expect(fakerados.OTPMethods).To(ContainElements(otp.MethodSet, otp.MethodGet, otp.MethodRemove, otp.MethodGetCurrentTime))
		Expect(fakerados.OTPMethods).To(Equal(slices.Sorted(slices.Values(fakerados.OTPMethods))), "in name order")
	})

	It("refuses a method it does not emulate at op setup, changing nothing", func(ctx SpecContext) {
		Expect(set(ctx, otpDev1())).To(Succeed())
		before := maps.Clone(stored().Omap)
		// FailOK would let the op succeed past a call the class refused when
		// its step ran; the OSD refuses the call before any step.
		err := writeErr(ctx, p, otpOID, func(op *radosclient.WriteOp) {
			op.SetXattr("touched", []byte{1})
			op.Exec(otp.Class, "otp_no_such_method", nil)
			op.SetStepFlags(radosclient.StepFlagFailOK)
		})
		Expect(err).To(MatchError(radosclient.ErrNotSupported))
		Expect(stored().Omap).To(Equal(before))
		Expect(stored().Xattrs).NotTo(HaveKey("touched"))
	})

	It("has a method set, so RegisterClass refuses it", func() {
		Expect(func() { c.RegisterClass(otp.Class, fakerados.OTPClass(), fakerados.OTPWriteMethods...) }).
			To(PanicWith(`fakerados: class "otp" has a method set: register it with RegisterClassMethods`))
	})
})
