package fakerados_test

import (
	"context"
	"fmt"
	"maps"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cls/otp"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
	"github.com/jhoblitt/rgw-go/internal/testutil/otptest"
)

// rfcDevice holds RFC 6238 Appendix B's SHA-1 seed, the ASCII
// "12345678901234567890", with 30-second steps counted from the epoch.
func rfcDevice() otp.OTPInfo {
	return otp.OTPInfo{
		Type: otp.TypeTOTP, ID: "rfc", Seed: "3132333435363738393031323334353637383930", SeedType: otp.SeedHex,
		SeedBin: []byte("12345678901234567890"), StepSize: 30, Window: 2,
	}
}

// storedInstance is a device's otp_instance as the object stores it.
type storedInstance struct {
	OTP         otp.OTPInfo
	LastChecks  []otp.OTPCheck
	LastSuccess uint64
}

func decodeStoredInstance(b []byte) storedInstance {
	GinkgoHelper()
	d := denc.NewDecoder(b)
	f := d.BeginStruct(1)
	i := storedInstance{
		OTP:         otp.DecodeOTPInfo(d),
		LastChecks:  denc.DecodeSlice(d, otp.DecodeOTPCheck),
		LastSuccess: d.U64(),
	}
	d.EndStruct(f)
	Expect(d.Err()).NotTo(HaveOccurred())
	return i
}

// at is the instant sec seconds and nsec nanoseconds past the epoch, as a
// decoded real_time reads.
func at(sec, nsec int64) time.Time { return time.Unix(sec, nsec).UTC() }

var _ = Describe("OTPClassAt's check methods", func() {
	BeforeEach(otptest.RedactDevices)

	var (
		c     *fakerados.Cluster
		p     radosclient.Pool
		clock time.Time
	)
	BeforeEach(func(ctx SpecContext) {
		c, p = newCluster(ctx)
		clock = at(59, 0)
		c.RegisterClassMethods(otp.Class, fakerados.OTPClassAt(func() time.Time { return clock }), fakerados.OTPMethods, fakerados.OTPWriteMethods)
	})
	stored := func() *fakerados.Object { return c.Object(poolName, ns, otpOID) }
	store := func(dev otp.OTPInfo, checks []otp.OTPCheck, lastSuccess uint64) {
		if stored() == nil {
			c.Put(poolName, ns, otpOID, nil)
		}
		stored().Omap["otp/"+dev.ID] = encodeOTPInstance(dev, checks, lastSuccess)
	}
	instance := func(id string) storedInstance {
		GinkgoHelper()
		Expect(stored().Omap).To(HaveKey("otp/" + id))
		return decodeStoredInstance(stored().Omap["otp/"+id])
	}
	// snapshot returns an assertion that the object is still as it is now.
	snapshot := func() func() {
		omap, version := maps.Clone(stored().Omap), stored().Version
		return func() {
			GinkgoHelper()
			Expect(stored().Omap).To(Equal(omap))
			Expect(stored().Version).To(Equal(version), "no write reached the op's transaction")
		}
	}
	checkOn := func(ctx context.Context, id, val, token string) error {
		return writeErr(ctx, p, otpOID, func(op *radosclient.WriteOp) {
			otp.Check(op, otp.CheckOp{ID: id, Val: val, Token: token}, denc.Squid)
		})
	}
	check := func(ctx context.Context, val, token string) error { return checkOn(ctx, "rfc", val, token) }
	lookupIn := func(ctx context.Context, in []byte) (otp.OTPCheck, error) {
		GinkgoHelper()
		op := radosclient.NewReadOp()
		res := op.Exec(otp.Class, otp.MethodGetResult, in)
		if _, err := p.Read(ctx, otpOID, op, radosclient.OpFlagNone); err != nil {
			return otp.OTPCheck{}, err
		}
		out, err := res.Bytes()
		Expect(err).NotTo(HaveOccurred())
		d := denc.NewDecoder(out)
		reply := otp.DecodeGetResultReply(d)
		Expect(d.Err()).NotTo(HaveOccurred())
		return reply.Result, nil
	}
	lookupOn := func(ctx context.Context, id, token string) (otp.OTPCheck, error) {
		return lookupIn(ctx, encodeOTPRequest(otp.CheckOp{ID: id, Token: token}))
	}
	lookup := func(ctx context.Context, token string) (otp.OTPCheck, error) { return lookupOn(ctx, "rfc", token) }

	Describe("otp_check", func() {
		// RFC 6238's vector at T = 20000000000 is left out: the OSD's clock is
		// a real_time, unsigned nanoseconds since the epoch, which ends in 2554.
		DescribeTable("verifies RFC 6238 Appendix B's SHA-1 vectors at their times, storing the step as the last success",
			func(ctx SpecContext, sec int64, code string, step uint64) {
				clock = at(sec, 0)
				store(rfcDevice(), nil, 0)
				Expect(check(ctx, code, "tok")).To(Succeed())
				Expect(instance("rfc")).To(Equal(storedInstance{
					OTP:         rfcDevice(),
					LastChecks:  []otp.OTPCheck{{Token: "tok", Timestamp: clock, Result: otp.CheckSuccess}},
					LastSuccess: step,
				}), "otp_instance::check and verify, cls_otp.cc:108-153")
				Expect(lookup(ctx, "tok")).To(Equal(otp.OTPCheck{Token: "tok", Timestamp: clock, Result: otp.CheckSuccess}))
			},
			Entry("T = 59", int64(59), "94287082", uint64(0x1)),
			Entry("T = 1111111109", int64(1111111109), "07081804", uint64(0x23523EC)),
			Entry("T = 1111111111", int64(1111111111), "14050471", uint64(0x23523ED)),
			Entry("T = 1234567890", int64(1234567890), "89005924", uint64(0x273EF07)),
			Entry("T = 2000000000", int64(2000000000), "69279037", uint64(0x3F940AA)),
		)

		It("records a wrong code as a failure, answering 0 and keeping the last success", func(ctx SpecContext) {
			store(rfcDevice(), nil, 7)
			Expect(check(ctx, "00000000", "tok")).To(Succeed(), "otp_check_op answers 0 whatever the code, cls_otp.cc:470-480")
			Expect(instance("rfc").LastChecks).To(Equal([]otp.OTPCheck{{Token: "tok", Timestamp: clock, Result: otp.CheckFail}}))
			Expect(instance("rfc").LastSuccess).To(Equal(uint64(7)))
			Expect(lookup(ctx, "tok")).To(HaveField("Result", otp.CheckFail))
		})

		It("refuses a code already used in its step, as verify refuses an index at or below the last success", func(ctx SpecContext) {
			clock = at(45, 0)
			store(rfcDevice(), nil, 0)
			Expect(check(ctx, "94287082", "first")).To(Succeed())
			clock = at(55, 0)
			Expect(check(ctx, "94287082", "second")).To(Succeed())
			Expect(instance("rfc").LastChecks).To(Equal([]otp.OTPCheck{
				{Token: "first", Timestamp: at(45, 0), Result: otp.CheckSuccess},
				{Token: "second", Timestamp: at(55, 0), Result: otp.CheckFail},
			}), "cls_otp.cc:145-148")
			Expect(instance("rfc").LastSuccess).To(Equal(uint64(1)))
		})

		It("verifies a used code again in each later step of its window, as the class adds the unsigned window distance", func(ctx SpecContext) {
			store(rfcDevice(), nil, 0)
			for _, step := range []struct {
				sec         int64
				want        otp.CheckResult
				lastSuccess uint64
			}{
				{sec: 59, want: otp.CheckSuccess, lastSuccess: 1},
				{sec: 75, want: otp.CheckSuccess, lastSuccess: 3},
				{sec: 95, want: otp.CheckSuccess, lastSuccess: 5},
				{sec: 125, want: otp.CheckFail, lastSuccess: 5},
			} {
				clock = at(step.sec, 0)
				token := fmt.Sprintf("at %d", step.sec)
				Expect(check(ctx, "94287082", token)).To(Succeed())
				Expect(lookup(ctx, token)).To(HaveField("Result", step.want), "T = %d", step.sec)
				Expect(instance("rfc").LastSuccess).To(Equal(step.lastSuccess), "T = %d", step.sec)
			}
		})

		It("verifies a good code followed by a NUL and junk, as the class hands liboath val.c_str()", func(ctx SpecContext) {
			store(rfcDevice(), nil, 0)
			Expect(check(ctx, "94287082\x00junk", "tok")).To(Succeed())
			Expect(lookup(ctx, "tok")).To(HaveField("Result", otp.CheckSuccess), "cls_otp.cc:133-136")
		})

		DescribeTable("counts the code's digits to its first NUL, as oath_totp_validate2 passes strlen(otp)",
			func(ctx SpecContext, code string, want otp.CheckResult) {
				store(rfcDevice(), nil, 0)
				Expect(check(ctx, code, "tok")).To(Succeed())
				Expect(instance("rfc").LastChecks).To(Equal([]otp.OTPCheck{{Token: "tok", Timestamp: clock, Result: want}}))
			},
			Entry("six digits", "287082", otp.CheckSuccess),
			Entry("seven digits", "4287082", otp.CheckSuccess),
			Entry("five digits, which liboath refuses", "87082", otp.CheckFail),
			Entry("nine digits, which liboath refuses", "094287082", otp.CheckFail),
			Entry("six digits before a NUL, eight bytes in all", "287082\x00x", otp.CheckSuccess),
		)

		DescribeTable("walks the window as liboath does, storing the step plus the distance whichever way the code lies",
			func(ctx SpecContext, sec int64, step uint32, timeOfs int32, window uint32, code string, want otp.CheckResult, lastSuccess uint64) {
				clock = at(sec, 0)
				dev := rfcDevice()
				dev.StepSize, dev.TimeOfs, dev.Window = step, timeOfs, window
				store(dev, nil, 0)
				Expect(check(ctx, code, "tok")).To(Succeed())
				Expect(instance("rfc").LastChecks).To(Equal([]otp.OTPCheck{{Token: "tok", Timestamp: clock, Result: want}}))
				Expect(instance("rfc").LastSuccess).To(Equal(lastSuccess), "cls_otp.cc:143-150")
			},
			Entry("the current step with no window", int64(59), uint32(30), int32(0), uint32(0), "94287082", otp.CheckSuccess, uint64(1)),
			Entry("the step before with no window", int64(89), uint32(30), int32(0), uint32(0), "94287082", otp.CheckFail, uint64(0)),
			Entry("one step ahead", int64(29), uint32(30), int32(0), uint32(2), "94287082", otp.CheckSuccess, uint64(1)),
			Entry("two steps back, at the window's edge, stored as two steps ahead", int64(119), uint32(30), int32(0), uint32(2), "94287082", otp.CheckSuccess, uint64(5)),
			Entry("three steps back, past the window", int64(149), uint32(30), int32(0), uint32(2), "94287082", otp.CheckFail, uint64(0)),
			Entry("a time offset, counting steps from it", int64(89), uint32(30), int32(30), uint32(0), "94287082", otp.CheckSuccess, uint64(1)),
			Entry("a negative time offset", int64(29), uint32(30), int32(-30), uint32(0), "94287082", otp.CheckSuccess, uint64(1)),
			Entry("a time offset past the clock, which liboath divides signed and the class unsigned",
				int64(59), uint32(30), int32(100), uint32(0), "63094451", otp.CheckSuccess, uint64(143165575)),
			Entry("a step of 60 seconds", int64(119), uint32(60), int32(0), uint32(0), "94287082", otp.CheckSuccess, uint64(1)),
			Entry("one step back from counter 0, which liboath walks to counter 2^64-1",
				int64(29), uint32(30), int32(0), uint32(2), "63094451", otp.CheckSuccess, uint64(1)),
			Entry("a match 41 steps ahead whose index, summed in 32 bits, wraps to 0 and is refused",
				int64(59), uint32(1), int32(100), uint32(41), "84755224", otp.CheckFail, uint64(0)),
		)

		It("checks a device of any type as TOTP", func(ctx SpecContext) {
			dev := rfcDevice()
			dev.Type = otp.TypeHOTP
			store(dev, nil, 0)
			Expect(check(ctx, "94287082", "tok")).To(Succeed())
			Expect(lookup(ctx, "tok")).To(HaveField("Result", otp.CheckSuccess), "verify never reads otp.type, cls_otp.cc:129-153")
		})

		It("validates over the stored seed_bin, not the seed", func(ctx SpecContext) {
			dev := rfcDevice()
			dev.Seed = "00"
			store(dev, nil, 0)
			Expect(check(ctx, "94287082", "tok")).To(Succeed())
			Expect(lookup(ctx, "tok")).To(HaveField("Result", otp.CheckSuccess), "cls_otp.cc:133")
		})

		It("fails the current step's code after the step before's, as the class adds the unsigned window distance", func(ctx SpecContext) {
			clock = at(1111111111, 0)
			store(rfcDevice(), nil, 0)
			Expect(check(ctx, "07081804", "before")).To(Succeed())
			Expect(check(ctx, "14050471", "current")).To(Succeed())
			Expect(instance("rfc").LastChecks).To(Equal([]otp.OTPCheck{
				{Token: "before", Timestamp: clock, Result: otp.CheckSuccess},
				{Token: "current", Timestamp: clock, Result: otp.CheckFail},
			}))
			Expect(instance("rfc").LastSuccess).To(Equal(uint64(0x23523EE)), "the step before's index, one step ahead of the clock's")
		})

		It("verifies the code of the low 32 bits of the clock's seconds, as verify casts the time to uint32_t", func(ctx SpecContext) {
			clock = at(1<<32+59, 0)
			store(rfcDevice(), nil, 0)
			Expect(check(ctx, "83350130", "true time")).To(Succeed())
			Expect(instance("rfc").LastChecks).To(Equal([]otp.OTPCheck{{Token: "true time", Timestamp: at(59, 0), Result: otp.CheckFail}}),
				"a real_time encodes 32 bits of seconds")
			Expect(check(ctx, "94287082", "low bits")).To(Succeed())
			Expect(instance("rfc").LastChecks).To(Equal([]otp.OTPCheck{{Token: "low bits", Timestamp: at(59, 0), Result: otp.CheckSuccess}}),
				"trim_expired drops the failure, whose stored time wrapped back to 1970")
			Expect(instance("rfc").LastSuccess).To(Equal(uint64(1)))
			Expect(lookup(ctx, "low bits")).To(HaveField("Result", otp.CheckUnknown), "find trims the success it stored the same way")
		})

		// trim_expired subtracts the step from the clock in real_time's unsigned
		// nanoseconds, so a step longer than the clock's seconds wraps the start
		// past the clock
		// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L98-L106 at v19.2.7 and v20.2.4).
		It("keeps no attempt limit for a step longer than the clock's seconds, and its lookup finds no check of it", func(ctx SpecContext) {
			dev := rfcDevice()
			dev.StepSize = 60
			store(dev, nil, 0)
			for i := range 7 {
				Expect(check(ctx, "00000000", fmt.Sprintf("wrong %d", i))).To(Succeed())
			}
			Expect(check(ctx, "94287082", "good")).To(Succeed())
			Expect(instance("rfc").LastChecks).To(Equal([]otp.OTPCheck{{Token: "good", Timestamp: clock, Result: otp.CheckSuccess}}),
				"each check trims every check before it, so the device never holds five")
			Expect(instance("rfc").LastSuccess).To(Equal(uint64(1)))
			Expect(lookup(ctx, "good")).To(HaveField("Result", otp.CheckUnknown), "find trims the check it stored the same way")
		})

		It("fails with EIO for a code that verifies on a device of step 0, writing nothing, where the OSD divides by zero", func(ctx SpecContext) {
			dev := rfcDevice()
			dev.StepSize = 0
			store(dev, nil, 0)
			unchanged := snapshot()
			Expect(check(ctx, "94287082", "tok")).To(haveErrno(syscall.EIO), "liboath steps by 30 seconds, then cls_otp.cc:143 divides by 0")
			unchanged()
		})

		It("records a wrong code on a device of step 0, which liboath refuses before the division", func(ctx SpecContext) {
			dev := rfcDevice()
			dev.StepSize = 0
			store(dev, nil, 0)
			Expect(check(ctx, "00000000", "tok")).To(Succeed())
			Expect(instance("rfc").LastChecks).To(Equal([]otp.OTPCheck{{Token: "tok", Timestamp: clock, Result: otp.CheckFail}}))
		})

		It("reads the class's clock, never the cluster's", func(ctx SpecContext) {
			c.SetClock(func() time.Time { return at(1_000_000, 0) })
			store(rfcDevice(), nil, 0)
			Expect(check(ctx, "94287082", "tok")).To(Succeed())
			Expect(instance("rfc").LastChecks).To(Equal([]otp.OTPCheck{{Token: "tok", Timestamp: clock, Result: otp.CheckSuccess}}))
			Expect(lookup(ctx, "other")).To(Equal(otp.OTPCheck{Token: "other", Timestamp: clock, Result: otp.CheckUnknown}))
		})

		It("keeps the id and the token whole, NULs and all", func(ctx SpecContext) {
			dev := rfcDevice()
			dev.ID = "rfc\x00two"
			store(dev, nil, 0)
			Expect(checkOn(ctx, "rfc", "94287082", "tok")).To(MatchError(radosclient.ErrNotFound))
			Expect(checkOn(ctx, "rfc\x00two", "94287082", "tok\x00a")).To(Succeed())
			Expect(lookupOn(ctx, "rfc\x00two", "tok\x00b")).To(HaveField("Result", otp.CheckUnknown))
			Expect(lookupOn(ctx, "rfc\x00two", "tok\x00a")).To(HaveField("Result", otp.CheckSuccess))
		})

		It("fails with ENOENT on a device the object does not hold, writing nothing", func(ctx SpecContext) {
			store(rfcDevice(), nil, 0)
			unchanged := snapshot()
			Expect(checkOn(ctx, "absent", "94287082", "tok")).To(MatchError(radosclient.ErrNotFound), "get_otp_instance, cls_otp.cc:465-468")
			unchanged()
		})

		It("fails with ENOENT on a missing object, creating none", func(ctx SpecContext) {
			Expect(check(ctx, "94287082", "tok")).To(MatchError(radosclient.ErrNotFound))
			Expect(stored()).To(BeNil())
		})

		It("fails with EIO on a device that does not decode", func(ctx SpecContext) {
			c.Put(poolName, ns, otpOID, nil)
			stored().Omap["otp/rfc"] = []byte{1}
			Expect(check(ctx, "94287082", "tok")).To(haveErrno(syscall.EIO), "get_otp_instance, cls_otp.cc:184-190")
		})

		Describe("five checks already in the device's last step_size seconds", func() {
			var first time.Time
			BeforeEach(func(ctx SpecContext) {
				first = at(1111111105, 250)
				clock = first
				store(rfcDevice(), nil, 0)
				for i := range 4 {
					Expect(check(ctx, "00000000", fmt.Sprintf("t%d", i+1))).To(Succeed())
				}
				Expect(check(ctx, "07081804", "t5")).To(Succeed())
				Expect(instance("rfc").LastChecks).To(HaveLen(5))
				Expect(instance("rfc").LastSuccess).To(Equal(uint64(0x23523EC)))
			})

			It("decline a sixth in the next TOTP step, answering 0 and writing nothing", func(ctx SpecContext) {
				clock = first.Add(10 * time.Second)
				unchanged := snapshot()
				Expect(check(ctx, "14050471", "t6")).To(Succeed(), "otp_instance::check, cls_otp.cc:113-117")
				unchanged()
			})

			It("decline a sixth at step_size seconds after the first, as trim_expired drops only older checks", func(ctx SpecContext) {
				clock = first.Add(30 * time.Second)
				unchanged := snapshot()
				Expect(check(ctx, "14050471", "t6")).To(Succeed())
				unchanged()
			})

			It("leave the lookup of the declined check OTP_CHECK_UNKNOWN under its fresh token", func(ctx SpecContext) {
				clock = first.Add(10 * time.Second)
				Expect(check(ctx, "14050471", "t6")).To(Succeed())
				Expect(lookup(ctx, "t6")).To(Equal(otp.OTPCheck{Token: "t6", Timestamp: clock, Result: otp.CheckUnknown}))
			})

			It("answer the lookup of a declined check under the fifth's token with the fifth's result", func(ctx SpecContext) {
				clock = first.Add(10 * time.Second)
				Expect(check(ctx, "00000000", "t5")).To(Succeed())
				Expect(lookup(ctx, "t5")).To(Equal(otp.OTPCheck{Token: "t5", Timestamp: first, Result: otp.CheckSuccess}),
					"otp_instance::find, cls_otp.cc:160-165")
			})

			It("admit a check once all five are older than step_size seconds, storing it alone", func(ctx SpecContext) {
				clock = first.Add(30*time.Second + time.Nanosecond)
				Expect(check(ctx, "14050471", "t6")).To(Succeed())
				Expect(instance("rfc").LastChecks).To(Equal([]otp.OTPCheck{{Token: "t6", Timestamp: clock, Result: otp.CheckSuccess}}),
					"trim_expired, cls_otp.cc:98-106")
				Expect(instance("rfc").LastSuccess).To(Equal(uint64(0x23523ED)))
			})
		})
	})

	Describe("otp_get_result", func() {
		It("answers the newest check carrying the token", func(ctx SpecContext) {
			clock = at(45, 0)
			store(rfcDevice(), nil, 0)
			Expect(check(ctx, "94287082", "tok")).To(Succeed())
			clock = at(55, 0)
			Expect(check(ctx, "94287082", "tok")).To(Succeed())
			Expect(lookup(ctx, "tok")).To(Equal(otp.OTPCheck{Token: "tok", Timestamp: at(55, 0), Result: otp.CheckFail}),
				"otp_instance::find walks last_checks from the back, cls_otp.cc:160-165")
		})

		It("answers OTP_CHECK_UNKNOWN at the class's clock for a token no check carries, writing nothing", func(ctx SpecContext) {
			store(rfcDevice(), []otp.OTPCheck{{Token: "tok", Timestamp: at(50, 0), Result: otp.CheckSuccess}}, 1)
			unchanged := snapshot()
			Expect(lookup(ctx, "other")).To(Equal(otp.OTPCheck{Token: "other", Timestamp: clock, Result: otp.CheckUnknown}),
				"cls_otp.cc:166-168")
			unchanged()
		})

		It("trims the checks older than one step from its answer but leaves them stored", func(ctx SpecContext) {
			clock = at(100, 0)
			store(rfcDevice(), []otp.OTPCheck{
				{Token: "old", Timestamp: at(69, 999_999_999), Result: otp.CheckSuccess},
				{Token: "edge", Timestamp: at(70, 0), Result: otp.CheckFail},
			}, 1)
			unchanged := snapshot()
			Expect(lookup(ctx, "old")).To(Equal(otp.OTPCheck{Token: "old", Timestamp: clock, Result: otp.CheckUnknown}))
			Expect(lookup(ctx, "edge")).To(Equal(otp.OTPCheck{Token: "edge", Timestamp: at(70, 0), Result: otp.CheckFail}))
			unchanged()
		})

		It("fails with ENOENT on a device the object does not hold", func(ctx SpecContext) {
			store(rfcDevice(), nil, 0)
			_, err := lookupOn(ctx, "absent", "tok")
			Expect(err).To(MatchError(radosclient.ErrNotFound), "get_otp_instance, cls_otp.cc:501-504")
		})

		It("fails with EIO on a device that does not decode", func(ctx SpecContext) {
			c.Put(poolName, ns, otpOID, nil)
			stored().Omap["otp/rfc"] = []byte{1}
			_, err := lookup(ctx, "tok")
			Expect(err).To(haveErrno(syscall.EIO), "get_otp_instance, cls_otp.cc:184-190")
		})

		It("fails with EINVAL on a cls_otp_get_result_op frame, whose token reads as the id and whose code runs past the frame", func(ctx SpecContext) {
			store(rfcDevice(), nil, 0)
			e := denc.NewEncoder()
			f := e.BeginStruct(1, 1)
			e.String("rfc")
			e.EndStruct(f)
			_, err := lookupIn(ctx, e.Bytes())
			Expect(err).To(MatchError(radosclient.ErrInvalid), "otp_get_result decodes cls_otp_check_otp_op, cls_otp.cc:487-494")
		})
	})

	DescribeTable("fails with EINVAL on a request that does not decode, writing nothing",
		func(ctx SpecContext, method string, write bool) {
			store(rfcDevice(), nil, 0)
			unchanged := snapshot()
			var err error
			if write {
				err = writeErr(ctx, p, otpOID, func(op *radosclient.WriteOp) { op.Exec(otp.Class, method, []byte{1}) })
			} else {
				_, err = lookupIn(ctx, []byte{1})
			}
			Expect(err).To(MatchError(radosclient.ErrInvalid))
			unchanged()
		},
		Entry("otp_check, cls_otp.cc:451-458", otp.MethodCheck, true),
		Entry("otp_get_result, cls_otp.cc:487-494", otp.MethodGetResult, false),
	)

	It("registers otp_check as a write and otp_get_result as a read", func() {
		// https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L564-L569 at v19.2.7 and v20.2.4
		Expect(fakerados.OTPMethods).To(ContainElements(otp.MethodCheck, otp.MethodGetResult))
		Expect(fakerados.OTPWriteMethods).To(ContainElement(otp.MethodCheck))
		Expect(fakerados.OTPWriteMethods).NotTo(ContainElement(otp.MethodGetResult))
	})
})
