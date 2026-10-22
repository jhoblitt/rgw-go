package driver_test

import (
	"bytes"
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cls/otp"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/driver"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

// rfcStep is the TOTP step of RFC 6238's vector at T = 1111111109, whose
// code is 07081804; the specs check codes around it, where a step is far
// from 0 as it is today.
const rfcStep = 0x23523EC

// totp is the code of RFC 6238's seed at TOTP step n.
var totp = driver.RFCCode

// stepTime is sec seconds into TOTP step n of 30 seconds.
func stepTime(n uint64, sec int64) time.Time {
	return time.Unix(int64(n)*30+sec, 0).UTC() //nolint:gosec // the specs' steps are near rfcStep
}

// checkOps decodes the request of every exec step of steps calling method.
func checkOps(steps []radosclient.Step, method string) []otp.CheckOp {
	GinkgoHelper()
	var out []otp.CheckOp
	for _, st := range steps {
		if x, ok := st.(*radosclient.ExecStep); ok && x.Method == method {
			d := denc.NewDecoder(x.In)
			out = append(out, otp.DecodeCheckOp(d))
			Expect(d.Err()).NotTo(HaveOccurred())
		}
	}
	return out
}

// onlyExecIn is the input of the one exec step of steps.
func onlyExecIn(steps []radosclient.Step) []byte {
	GinkgoHelper()
	var ins [][]byte
	for _, st := range steps {
		if x, ok := st.(*radosclient.ExecStep); ok {
			ins = append(ins, x.In)
		}
	}
	Expect(ins).To(HaveLen(1))
	return ins[0]
}

var _ = Describe("VerifyMFA", func() {
	const (
		storeName = "ceph-objectstore"
		otpOID    = "user:alice"
		wrongCode = "00000000"
	)
	var (
		c     *fakerados.Cluster
		s     *driver.Store
		clock time.Time
		user  *meta.UserInfo
	)
	BeforeEach(func(ctx SpecContext) {
		c = fakerados.New()
		c.SetRequiredOSDRelease("squid")
		seedRookZone(c, storeName, true)
		clock = stepTime(rfcStep, 5)
		registerOTPClass(c, func() time.Time { return clock })
		var err error
		s, err = driver.Open(ctx, c, conf(map[string]string{"rgw_zone": storeName}), driver.Options{})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(s.Close)
		u := meta.NewUserInfo()
		u.UserID = meta.UserID{ID: "alice"}
		u.MFAIDs = []string{"rfc"}
		user = &u
	})
	rfcDevice := func() otp.OTPInfo {
		return otp.OTPInfo{
			Type: otp.TypeTOTP, ID: "rfc", Seed: "3132333435363738393031323334353637383930", SeedType: otp.SeedHex,
			StepSize: 30, Window: 2,
		}
	}
	handle := func(ctx SpecContext) radosclient.Pool {
		GinkgoHelper()
		p, err := c.Pool(ctx, rookMetaPool, rookOTPNS)
		Expect(err).NotTo(HaveOccurred())
		return p
	}
	// seedDevices stores devs in alice's otp object as radosgw-admin mfa
	// create does, through otp_set, and forgets the op.
	seedDevices := func(ctx SpecContext, devs ...otp.OTPInfo) {
		GinkgoHelper()
		w := radosclient.NewWriteOp()
		otp.Set(w, devs, denc.Squid)
		_, err := handle(ctx).Write(ctx, otpOID, w, radosclient.OpFlagNone)
		Expect(err).NotTo(HaveOccurred())
		c.ResetCounters()
	}
	object := func() *fakerados.Object { return c.Object(rookMetaPool, rookOTPNS, otpOID) }
	record := func() otp.Instance {
		GinkgoHelper()
		Expect(object()).NotTo(BeNil())
		Expect(object().Omap).To(HaveKey(otp.InstanceKey("rfc")))
		d := denc.NewDecoder(object().Omap[otp.InstanceKey("rfc")])
		inst := otp.DecodeInstance(d)
		Expect(d.Err()).NotTo(HaveOccurred())
		return inst
	}
	lastCheck := func() otp.OTPCheck {
		GinkgoHelper()
		r := record()
		Expect(r.LastChecks).NotTo(BeEmpty())
		return r.LastChecks[len(r.LastChecks)-1]
	}
	// racer makes another client's check of val, under the clock at, land
	// after the next write to alice's otp object.
	racer := func(ctx SpecContext, val string, at time.Time) {
		p := handle(ctx)
		c.AfterWrite(rookMetaPool, rookOTPNS, otpOID, func() {
			c.AfterWrite(rookMetaPool, rookOTPNS, otpOID, nil)
			clock = at
			w := radosclient.NewWriteOp()
			otp.Check(w, otp.CheckOp{ID: "rfc", Val: val, Token: "racer-token-0001"}, denc.Squid)
			_, err := p.Write(ctx, otpOID, w, radosclient.OpFlagNone)
			Expect(err).NotTo(HaveOccurred())
		})
	}
	verifyAt := func(ctx SpecContext, at time.Time, pin string) bool {
		GinkgoHelper()
		clock = at
		ok, err := s.VerifyMFA(ctx, user, "rfc", pin)
		Expect(err).NotTo(HaveOccurred())
		return ok
	}
	// verifyEach checks the code of each step in steps, the i-th at the
	// clock at(i), and returns the answers.
	verifyEach := func(ctx SpecContext, steps []uint64, at func(i int) time.Time) []bool {
		GinkgoHelper()
		var got []bool
		for i, step := range steps {
			got = append(got, verifyAt(ctx, at(i), totp(step)))
		}
		return got
	}
	// failingMethod makes method of the otp class fail with errno.
	failingMethod := func(method string, errno syscall.Errno) {
		inner := fakerados.OTPClassAt(func() time.Time { return clock })
		c.RegisterClassMethods(otp.Class, func(call *fakerados.ClassCall) ([]byte, int32) {
			if call.Method == method {
				return nil, -int32(errno)
			}
			return inner(call)
		}, fakerados.OTPMethods, fakerados.OTPWriteMethods)
	}

	It("checks RFC 6238's codes", func() {
		Expect(totp(1)).To(Equal("94287082"))
		Expect(totp(rfcStep)).To(Equal("07081804"))
		Expect(totp(rfcStep + 1)).To(Equal("14050471"))
	})

	Describe("the device", func() {
		It("is not verified for a serial the user has no device for, sending nothing", func(ctx SpecContext) {
			seedDevices(ctx, rfcDevice())
			user.MFAIDs = []string{"other"}
			Expect(s.VerifyMFA(ctx, user, "rfc", totp(rfcStep))).To(BeFalse(), "verify_mfa, rgw_sal_rados.cc:298-302")
			Expect(c.Reads(rookMetaPool, rookOTPNS, otpOID)).To(BeZero())
			Expect(c.Writes(rookMetaPool, rookOTPNS, otpOID)).To(BeZero())
		})

		It("is not verified for no user, sending nothing", func(ctx SpecContext) {
			seedDevices(ctx, rfcDevice())
			Expect(s.VerifyMFA(ctx, nil, "rfc", totp(rfcStep))).To(BeFalse())
			Expect(c.Reads(rookMetaPool, rookOTPNS, otpOID)).To(BeZero())
		})

		It("is not verified for a serial in mfa_ids whose device is gone from the otp object", func(ctx SpecContext) {
			seedDevices(ctx, rfcDevice())
			user.MFAIDs = []string{"gone", "rfc"}
			Expect(s.VerifyMFA(ctx, user, "gone", totp(rfcStep))).To(BeFalse())
			Expect(record().LastChecks).To(BeEmpty())
		})

		It("is not verified for a user with no otp object", func(ctx SpecContext) {
			Expect(s.VerifyMFA(ctx, user, "rfc", totp(rfcStep))).To(BeFalse())
			Expect(object()).To(BeNil())
		})

		It("verifies, as the class does, a code used again on a device whose offset an admin set past the clock", func(ctx SpecContext) {
			const ofs = 1_790_010_000
			dev := rfcDevice()
			dev.TimeOfs = ofs
			seedDevices(ctx, dev)
			at := time.Unix(1_790_000_011, 0).UTC()
			// liboath's step is (secs - ofs) / 30 in signed arithmetic, -332,
			// and the code is the one after it.
			next := int64(-331)
			code := totp(uint64(next))[2:] //nolint:gosec // liboath holds the signed step in a uint64_t
			Expect(verifyAt(ctx, at, code)).To(BeTrue())
			Expect(verifyAt(ctx, at.Add(13*time.Second), code)).To(BeTrue(),
				"the class's unsigned step moves on and its index with it, so it takes the code again")
			Expect(lastCheck().Result).To(Equal(otp.CheckSuccess))
		})

		It("is not verified, the class not called, for a device of step 0, where a good code faults the OSD", func(ctx SpecContext) {
			dev := rfcDevice()
			dev.StepSize = 0
			seedDevices(ctx, dev)
			Expect(s.VerifyMFA(ctx, user, "rfc", totp(rfcStep))).To(BeFalse())
			Expect(c.Writes(rookMetaPool, rookOTPNS, otpOID)).To(BeZero(), "cls_otp.cc:143 divides by the stored step")
		})
	})

	Describe("the code", func() {
		BeforeEach(func(ctx SpecContext) { seedDevices(ctx, rfcDevice()) })

		It("verifies the device's current code", func(ctx SpecContext) {
			Expect(verifyAt(ctx, stepTime(rfcStep, 5), totp(rfcStep))).To(BeTrue())
			Expect(record().LastChecks).To(Equal([]otp.OTPCheck{{Token: lastCheck().Token, Timestamp: stepTime(rfcStep, 5), Result: otp.CheckSuccess}}))
			Expect(record().LastSuccess).To(Equal(uint64(rfcStep)))
		})

		It("is not verified for a wrong code", func(ctx SpecContext) {
			Expect(verifyAt(ctx, stepTime(rfcStep, 5), wrongCode)).To(BeFalse())
			Expect(lastCheck().Result).To(Equal(otp.CheckFail))
		})

		It("is not verified for a code used again in its own step, which the class refuses", func(ctx SpecContext) {
			Expect(verifyAt(ctx, stepTime(rfcStep, 5), totp(rfcStep))).To(BeTrue())
			Expect(verifyAt(ctx, stepTime(rfcStep, 20), totp(rfcStep))).To(BeFalse())
			Expect(lastCheck().Result).To(Equal(otp.CheckFail), "cls_otp.cc:145-148")
			Expect(record().LastSuccess).To(Equal(uint64(rfcStep)))
		})

		It("is not verified for a code used again in each later step of its window, which the class accepts", func(ctx SpecContext) {
			Expect(verifyAt(ctx, stepTime(rfcStep, 5), totp(rfcStep))).To(BeTrue())
			for i, want := range []struct {
				result      otp.CheckResult
				lastSuccess uint64
			}{
				{otp.CheckSuccess, rfcStep + 2},
				{otp.CheckSuccess, rfcStep + 4},
				{otp.CheckFail, rfcStep + 4},
			} {
				step := rfcStep + uint64(i) + 1 //nolint:gosec // i is at most 2
				Expect(verifyAt(ctx, stepTime(step, 5), totp(rfcStep))).To(BeFalse(), "step %d", step)
				Expect(lastCheck().Result).To(Equal(want.result), "step %d: verify adds the unsigned window distance, cls_otp.cc:143", step)
				Expect(record().LastSuccess).To(Equal(want.lastSuccess), "step %d", step)
			}
		})

		It("verifies the next step's code after a used one", func(ctx SpecContext) {
			Expect(verifyAt(ctx, stepTime(rfcStep, 5), totp(rfcStep))).To(BeTrue())
			Expect(verifyAt(ctx, stepTime(rfcStep+1, 5), totp(rfcStep+1))).To(BeTrue())
		})

		It("verifies a code of the step before its check's, used the first time", func(ctx SpecContext) {
			Expect(verifyAt(ctx, stepTime(rfcStep+1, 5), totp(rfcStep))).To(BeTrue())
			Expect(record().LastSuccess).To(Equal(uint64(rfcStep+2)), "one step back, stored as one step ahead")
		})

		It("verifies a code of the step after its check's on a device with no success near it", func(ctx SpecContext) {
			Expect(verifyAt(ctx, stepTime(rfcStep, 25), totp(rfcStep+1))).To(BeTrue())
		})

		It("is not verified for a code of an earlier step, once a code used late raised the last success past it, which the class accepts", func(ctx SpecContext) {
			Expect(verifyAt(ctx, stepTime(rfcStep+1, 5), totp(rfcStep))).To(BeTrue())
			Expect(verifyAt(ctx, stepTime(rfcStep+2, 5), totp(rfcStep+1))).To(BeFalse())
			Expect(lastCheck().Result).To(Equal(otp.CheckSuccess))
			Expect(record().LastSuccess).To(Equal(uint64(rfcStep + 3)))
		})

		It("verifies a code of a later step than its check's soon after a success, as the seed shows it ahead", func(ctx SpecContext) {
			Expect(verifyAt(ctx, stepTime(rfcStep, 5), totp(rfcStep))).To(BeTrue())
			Expect(verifyAt(ctx, stepTime(rfcStep+1, 5), totp(rfcStep+2))).To(BeTrue())
			Expect(record().LastSuccess).To(Equal(uint64(rfcStep+2)), "the code's own step, which the index alone cannot tell from c-d")
		})

		It("is not verified for an unused code of an earlier step after a newer code in one step, which the class accepts", func(ctx SpecContext) {
			Expect(verifyAt(ctx, stepTime(rfcStep, 5), totp(rfcStep))).To(BeTrue())
			Expect(verifyAt(ctx, stepTime(rfcStep, 10), totp(rfcStep-1))).To(BeFalse())
			Expect(lastCheck().Result).To(Equal(otp.CheckSuccess))
			Expect(record().LastSuccess).To(Equal(uint64(rfcStep+1)), "one step back, stored as one step ahead")
		})

		It("is not verified for a code used before a later code's success, which a rule from that success alone would verify", func(ctx SpecContext) {
			Expect(verifyAt(ctx, stepTime(rfcStep+1, 5), totp(rfcStep+1))).To(BeTrue())
			Expect(verifyAt(ctx, stepTime(rfcStep+2, 5), totp(rfcStep))).To(BeFalse(), "an unused code two steps back, at or below the last success")
			Expect(record().LastSuccess).To(Equal(uint64(rfcStep+4)), "the last success's check is of step rfcStep+2 and its code of rfcStep or rfcStep+4")
			Expect(verifyAt(ctx, stepTime(rfcStep+3, 5), totp(rfcStep+1))).To(BeFalse(), "the first code, used again")
			Expect(lastCheck().Result).To(Equal(otp.CheckSuccess), "the class takes it again, at index rfcStep+5")
		})

		It("is not verified for every code after the first of an authenticator one step behind, used once a step, which the class verifies", func(ctx SpecContext) {
			got := verifyEach(ctx, []uint64{rfcStep, rfcStep + 1, rfcStep + 2, rfcStep + 3, rfcStep + 4, rfcStep + 5},
				func(i int) time.Time { return stepTime(rfcStep+uint64(i)+1, 10) }) //nolint:gosec // i is at most 5
			Expect(got).To(Equal([]bool{true, false, false, false, false, false}), "each check stores its step plus one, the step of the next check's code")
			Expect(lastCheck().Result).To(Equal(otp.CheckSuccess))
		})

		It("verifies every code of an authenticator one step behind, used every third step", func(ctx SpecContext) {
			got := verifyEach(ctx, []uint64{rfcStep, rfcStep + 3, rfcStep + 6, rfcStep + 9},
				func(i int) time.Time { return stepTime(rfcStep+3*uint64(i)+1, 10) }) //nolint:gosec // i is at most 3
			Expect(got).To(Equal([]bool{true, true, true, true}))
		})

		It("verifies every code of an authenticator one step ahead, used once a step", func(ctx SpecContext) {
			got := verifyEach(ctx, []uint64{rfcStep + 1, rfcStep + 2, rfcStep + 3, rfcStep + 4, rfcStep + 5, rfcStep + 6},
				func(i int) time.Time { return stepTime(rfcStep+uint64(i), 10) }) //nolint:gosec // i is at most 5
			Expect(got).To(Equal([]bool{true, true, true, true, true, true}))
			Expect(record().LastSuccess).To(Equal(uint64(rfcStep + 6)))
		})

		// skewed is the code an authenticator 5 s behind the OSD shows at t, on
		// a device of offset 0, which radosgw-admin mfa resync stores for that
		// skew at window 2: it fits whole steps.
		skewed := func(t time.Time) string { return totp(uint64(t.Unix()-5) / 30) } //nolint:gosec // the specs' times are positive

		It("is not verified for 7 of 8 codes of an authenticator 5 s behind, used once a step in each step's first 5 s, which the class verifies", func(ctx SpecContext) {
			var got []bool
			for i := range uint64(8) {
				at := stepTime(rfcStep+i, 2)
				got = append(got, verifyAt(ctx, at, skewed(at)))
				Expect(lastCheck().Result).To(Equal(otp.CheckSuccess), "request %d", i)
			}
			Expect(got).To(Equal([]bool{true, false, false, false, false, false, false, false}),
				"each code is the step before's, found one step behind, and each check stores its step plus one")
		})

		It("verifies every code of the same authenticator used once a step past each step's first 5 s", func(ctx SpecContext) {
			var got []bool
			for i := range uint64(8) {
				at := stepTime(rfcStep+i, 10)
				got = append(got, verifyAt(ctx, at, skewed(at)))
			}
			Expect(got).To(Equal([]bool{true, true, true, true, true, true, true, true}))
		})

		It("verifies a code by its last six digits, as the class takes the digit count from the code", func(ctx SpecContext) {
			Expect(verifyAt(ctx, stepTime(rfcStep, 5), totp(rfcStep)[2:])).To(BeTrue(), "liboath/totp.c:251-260 passes strlen(otp)")
		})

		It("is not verified for a sixth check within step_size seconds of five whose fifth is a good code", func(ctx SpecContext) {
			for range 4 {
				Expect(verifyAt(ctx, stepTime(rfcStep, 15), wrongCode)).To(BeFalse())
			}
			Expect(verifyAt(ctx, stepTime(rfcStep, 15), totp(rfcStep))).To(BeTrue())
			five := maps.Clone(object().Omap)
			Expect(verifyAt(ctx, stepTime(rfcStep+1, 5), wrongCode)).To(BeFalse(), "the next TOTP step, twenty seconds after the five")
			Expect(object().Omap).To(Equal(five), "otp_instance::check declines the sixth and writes nothing, cls_otp.cc:113-117")
		})

		It("is not verified for a pin holding a NUL, which the class would verify, and sends nothing", func(ctx SpecContext) {
			Expect(s.VerifyMFA(ctx, user, "rfc", totp(rfcStep)+"\x00junk")).To(BeFalse())
			Expect(c.Reads(rookMetaPool, rookOTPNS, otpOID)).To(BeZero())
			Expect(c.Writes(rookMetaPool, rookOTPNS, otpOID)).To(BeZero())
		})
	})

	Describe("the calls", func() {
		BeforeEach(func(ctx SpecContext) { seedDevices(ctx, rfcDevice()) })

		It("draws a fresh token of 16 characters of gen_rand_alphanumeric's table for each check", func(ctx SpecContext) {
			Expect(verifyAt(ctx, stepTime(rfcStep, 5), wrongCode)).To(BeFalse())
			Expect(verifyAt(ctx, stepTime(rfcStep, 6), wrongCode)).To(BeFalse())
			var tokens []string
			for _, w := range c.WritesTo(rookMetaPool, rookOTPNS, otpOID) {
				for _, op := range checkOps(w.Steps(), otp.MethodCheck) {
					tokens = append(tokens, op.Token)
				}
			}
			Expect(tokens).To(HaveLen(2))
			Expect(tokens).To(HaveEach(MatchRegexp(`^[A-Za-z0-9_-]{16}$`)), "cls_otp_client.cc:65-66, random_string.cc:45-51")
			Expect(tokens[0]).NotTo(Equal(tokens[1]))
			Expect(record().LastChecks).To(HaveExactElements(HaveField("Token", tokens[0]), HaveField("Token", tokens[1])))
		})

		It("sends otp_get_result, on a read op after the check, the bytes it sent otp_check", func(ctx SpecContext) {
			Expect(verifyAt(ctx, stepTime(rfcStep, 5), totp(rfcStep))).To(BeTrue())
			writes := c.WritesTo(rookMetaPool, rookOTPNS, otpOID)
			Expect(writes).To(HaveLen(1))
			Expect(checkOps(writes[0].Steps(), otp.MethodCheck)).To(Equal([]otp.CheckOp{{ID: "rfc", Val: totp(rfcStep), Token: lastCheck().Token}}))
			lookup := c.LastRead(rookMetaPool, rookOTPNS, otpOID).Steps()
			Expect(checkOps(lookup, otp.MethodGetResult)).To(HaveLen(1))
			Expect(onlyExecIn(lookup)).To(Equal(onlyExecIn(writes[0].Steps())), "cls_otp_client.cc:76-84 sends `in` again")
		})

		It("checks the device as it read it, checking again when another check lands in between", func(ctx SpecContext) {
			p := handle(ctx)
			c.BeforeWrite(rookMetaPool, rookOTPNS, otpOID, func(*fakerados.Object) {
				c.BeforeWrite(rookMetaPool, rookOTPNS, otpOID, nil)
				at := clock
				clock = stepTime(rfcStep, 25)
				w := radosclient.NewWriteOp()
				otp.Check(w, otp.CheckOp{ID: "rfc", Val: totp(rfcStep), Token: "racer"}, denc.Squid)
				_, err := p.Write(ctx, otpOID, w, radosclient.OpFlagNone)
				Expect(err).NotTo(HaveOccurred())
				clock = at
			})
			Expect(verifyAt(ctx, stepTime(rfcStep+1, 5), totp(rfcStep))).To(BeFalse(), "the racer used the code first")
			Expect(c.WritesTo(rookMetaPool, rookOTPNS, otpOID)).To(HaveLen(3), "the canceled check, the racer's and the check again")
			Expect(record().LastChecks).To(HaveExactElements(
				otp.OTPCheck{Token: "racer", Timestamp: stepTime(rfcStep, 25), Result: otp.CheckSuccess},
				HaveField("Result", otp.CheckSuccess),
			))
		})

		It("is not verified for a wrong code when another check's success lands between its check and its lookup", func(ctx SpecContext) {
			racer(ctx, totp(rfcStep), stepTime(rfcStep, 5))
			Expect(verifyAt(ctx, stepTime(rfcStep, 5), wrongCode)).To(BeFalse(), "the check's own result is a failure")
			Expect(record().LastChecks).To(HaveExactElements(HaveField("Result", otp.CheckFail), HaveField("Result", otp.CheckSuccess)))
		})

		It("is not verified for a declined check when another check's success lands before its lookup", func(ctx SpecContext) {
			for range 5 {
				Expect(verifyAt(ctx, stepTime(rfcStep, 2), wrongCode)).To(BeFalse())
			}
			racer(ctx, totp(rfcStep+1), stepTime(rfcStep+1, 3))
			Expect(verifyAt(ctx, stepTime(rfcStep, 12), wrongCode)).To(BeFalse(), "the sixth is declined, its lookup OTP_CHECK_UNKNOWN")
			Expect(record().LastChecks).To(HaveExactElements(HaveField("Token", "racer-token-0001")), "the racer's check, once the five expired")
			Expect(lastCheck().Result).To(Equal(otp.CheckSuccess))
		})

		It("verifies a fresh code when another check's success lands between its check and its lookup", func(ctx SpecContext) {
			Expect(verifyAt(ctx, stepTime(rfcStep-1, 5), totp(rfcStep-1))).To(BeTrue())
			racer(ctx, totp(rfcStep+1), stepTime(rfcStep, 25))
			Expect(verifyAt(ctx, stepTime(rfcStep, 25), totp(rfcStep))).To(BeTrue(), "the code's own step, from the seed, not the racer's index")
			Expect(record().LastSuccess).To(Equal(uint64(rfcStep+1)), "the racer's success")
		})

		It("fails after six tries when the device changes before every check", func(ctx SpecContext) {
			c.BeforeWrite(rookMetaPool, rookOTPNS, otpOID, func(o *fakerados.Object) {
				b := slices.Clone(o.Omap[otp.InstanceKey("rfc")])
				b[len(b)-1] ^= 1
				o.Omap[otp.InstanceKey("rfc")] = b
			})
			ok, err := s.VerifyMFA(ctx, user, "rfc", totp(rfcStep))
			Expect(err).To(HaveOccurred())
			Expect(ok).To(BeFalse())
			Expect(c.Writes(rookMetaPool, rookOTPNS, otpOID)).To(Equal(6))
			Expect(record().LastChecks).To(BeEmpty())
		})

		DescribeTable("sends no lookup when otp_check fails",
			func(ctx SpecContext, errno syscall.Errno, fails bool) {
				failingMethod(otp.MethodCheck, errno)
				ok, err := s.VerifyMFA(ctx, user, "rfc", totp(rfcStep))
				Expect(ok).To(BeFalse())
				if fails {
					Expect(err).To(MatchError(ContainSubstring(errno.Error())))
				} else {
					Expect(err).NotTo(HaveOccurred())
				}
				Expect(c.Reads(rookMetaPool, rookOTPNS, otpOID)).To(Equal(1), "the device's read alone, cls_otp_client.cc:72-74")
				Expect(checkOps(c.LastRead(rookMetaPool, rookOTPNS, otpOID).Steps(), otp.MethodGetResult)).To(BeEmpty())
			},
			Entry("ENOENT, no device, is not verified", syscall.ENOENT, false),
			Entry("EIO is the error", syscall.EIO, true),
		)

		It("fails with otp_get_result's error", func(ctx SpecContext) {
			failingMethod(otp.MethodGetResult, syscall.EIO)
			ok, err := s.VerifyMFA(ctx, user, "rfc", totp(rfcStep))
			Expect(err).To(MatchError(ContainSubstring(syscall.EIO.Error())))
			Expect(ok).To(BeFalse())
		})

		It("fails with the error of the device's read, sending no check", func(ctx SpecContext) {
			c.FailNextRead(rookMetaPool, rookOTPNS, otpOID, syscall.EIO)
			ok, err := s.VerifyMFA(ctx, user, "rfc", totp(rfcStep))
			Expect(err).To(MatchError(ContainSubstring(syscall.EIO.Error())))
			Expect(ok).To(BeFalse())
			Expect(c.Writes(rookMetaPool, rookOTPNS, otpOID)).To(BeZero())
		})

		It("fails with the error of opening the zone's otp pool", func(ctx SpecContext) {
			c.FailPool(rookMetaPool)
			ok, err := s.VerifyMFA(ctx, user, "rfc", totp(rfcStep))
			Expect(err).To(MatchError(ContainSubstring(rookMetaPool)))
			Expect(ok).To(BeFalse())
		})
	})

	Describe("the log", func() {
		var logs *bytes.Buffer
		BeforeEach(func(ctx SpecContext) {
			seedDevices(ctx, rfcDevice())
			logs = &bytes.Buffer{}
			DeferCleanup(driver.CaptureDebugLog(logs))
		})
		// lines decodes the log lines that name the otp id.
		lines := func() []map[string]any {
			GinkgoHelper()
			var out []map[string]any
			for line := range strings.SplitSeq(strings.TrimSpace(logs.String()), "\n") {
				var rec map[string]any
				Expect(json.Unmarshal([]byte(line), &rec)).To(Succeed(), line)
				if _, ok := rec["otp_id"]; ok {
					out = append(out, rec)
				}
			}
			return out
		}
		tokens := func() []string {
			var out []string
			for _, w := range c.WritesTo(rookMetaPool, rookOTPNS, otpOID) {
				for _, op := range checkOps(w.Steps(), otp.MethodCheck) {
					out = append(out, op.Token)
				}
			}
			return out
		}

		It("holds the device's id and the check's result alone, as check_mfa logs them", func(ctx SpecContext) {
			Expect(verifyAt(ctx, stepTime(rfcStep, 5), totp(rfcStep))).To(BeTrue())
			Expect(verifyAt(ctx, stepTime(rfcStep, 6), wrongCode)).To(BeFalse())
			Expect(verifyAt(ctx, stepTime(rfcStep+1, 5), totp(rfcStep))).To(BeFalse())
			got := lines()
			Expect(got).To(HaveLen(3))
			for i, want := range []float64{float64(otp.CheckSuccess), float64(otp.CheckFail), float64(otp.CheckSuccess)} {
				Expect(slices.Sorted(maps.Keys(got[i]))).To(Equal([]string{"level", "msg", "otp_id", "result", "time"}), "line %d", i)
				Expect(got[i]).To(HaveKeyWithValue("otp_id", "rfc"))
				Expect(got[i]).To(HaveKeyWithValue("result", want), "line %d, svc_cls.cc:60", i)
			}
			Expect(got[2]["msg"]).NotTo(Equal(got[0]["msg"]), "the used code's refusal says so")
			Expect(tokens()).To(HaveLen(3))
			for _, secret := range append(tokens(), totp(rfcStep), "3132333435363738393031323334353637383930") {
				Expect(logs.String()).NotTo(ContainSubstring(secret))
			}
		})
	})
})
