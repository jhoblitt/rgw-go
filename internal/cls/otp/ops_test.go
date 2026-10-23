package otp_test

import (
	"syscall"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cls/otp"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/testutil/otptest"
)

// execStep asserts steps is a single exec of otp.method and returns it.
func execStep(steps []radosclient.Step, method string) *radosclient.ExecStep {
	GinkgoHelper()
	Expect(steps).To(HaveLen(1))
	s, ok := steps[0].(*radosclient.ExecStep)
	Expect(ok).To(BeTrue(), "step is %T, not an exec", steps[0])
	Expect(s.Class).To(Equal("otp"))
	Expect(s.Method).To(Equal(method))
	return s
}

// The requests OTP::set, OTP::get_all, OTP::remove and OTP::get_current_time
// send
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp_client.cc#L33-L188 at v19.2.7;
// the same at v20.2.4 but for otp_check).
const (
	// cls_otp_set_otp_op (1,1), 44 bytes: one entry, devA.
	setDevAHex = "01012c000000" + "01000000" + devAHex
	// cls_otp_get_otp_op (1,1), 5 bytes: get_all true, no ids, as OTP::get_all sends it.
	getAllHex = "010105000000" + "01" + "00000000"
	// cls_otp_remove_otp_op (1,1), 12 bytes: one id, "dev1", as OTP::remove sends it.
	removeDev1Hex = "01010c000000" + "01000000" + "04000000" + "64657631"
	// cls_otp_get_current_time_op (1,1), empty.
	getCurrentTimeHex = "010100000000"
	// cls_otp_get_otp_reply (1,1), 95 bytes: devA and devB as stored.
	getReplyHex = "01015f000000" + "02000000" + devAStoredHex + devBHex
	// cls_otp_get_current_time_reply (1,1), 8 bytes.
	currentTimeReplyHex = "010108000000" + stampHex
)

var _ = Describe("OTPObject", func() {
	DescribeTable("names the user's object as get_mfa_oid does",
		func(uid meta.UserID, want string) {
			Expect(otp.OTPObject(uid)).To(Equal(want))
		},
		Entry("a plain user", meta.UserID{ID: "alice"}, "user:alice"),
		Entry("a tenant user", meta.UserID{Tenant: "acme", ID: "alice"}, "user:acme$alice"),
	)
})

var _ = Describe("requests", func() {
	BeforeEach(otptest.RedactDevices)

	It("set sends cls_otp_set_otp_op with the devices as given", func() {
		w := radosclient.NewWriteOp()
		otp.Set(w, []otp.OTPInfo{devA()}, denc.Squid)
		Expect(execStep(w.Steps(), "otp_set").In).To(Equal(unhex(setDevAHex)))
	})

	It("set sends several devices in order, seed_bin included", func() {
		w := radosclient.NewWriteOp()
		otp.Set(w, []otp.OTPInfo{devB(), devA()}, denc.Tentacle)
		// 4 + 49 + 40 bytes.
		Expect(execStep(w.Steps(), "otp_set").In).To(Equal(unhex("01015d000000" + "02000000" + devBHex + devAHex)))
	})

	It("set sends an empty list for no devices", func() {
		w := radosclient.NewWriteOp()
		otp.Set(w, nil, denc.Squid)
		Expect(execStep(w.Steps(), "otp_set").In).To(Equal(unhex("010104000000" + "00000000")))
	})

	It("remove sends cls_otp_remove_otp_op", func() {
		w := radosclient.NewWriteOp()
		otp.Remove(w, []string{"dev1"}, denc.Squid)
		Expect(execStep(w.Steps(), "otp_remove").In).To(Equal(unhex(removeDev1Hex)))
	})

	It("remove sends several ids in order", func() {
		w := radosclient.NewWriteOp()
		otp.Remove(w, []string{"b", "a"}, denc.Tentacle)
		Expect(execStep(w.Steps(), "otp_remove").In).To(Equal(unhex(
			"01010e000000" + "02000000" + "01000000" + "62" + "01000000" + "61")))
	})

	It("decodes each request as the class does", func() {
		Expect(decodeAll(unhex(setDevAHex), otp.DecodeSetOp)).To(Equal(otp.SetOp{Entries: []otp.OTPInfo{devA()}}))
		Expect(decodeAll(unhex(getAllHex), otp.DecodeGetOp)).To(Equal(otp.GetOp{GetAll: true}))
		Expect(decodeAll(unhex(removeDev1Hex), otp.DecodeRemoveOp)).To(Equal(otp.RemoveOp{IDs: []string{"dev1"}}))
		Expect(decodeAll(unhex(getCurrentTimeHex), otp.DecodeGetCurrentTimeOp)).To(Equal(otp.GetCurrentTimeOp{}))
	})

	It("encodes a get of named ids, which the class also reads", func() {
		Expect(encode(otp.GetOp{IDs: []string{"dev1"}})).To(Equal(unhex(
			"01010d000000" + "00" + "01000000" + "04000000" + "64657631")))
	})

	It("encodes each reply as the class does", func() {
		Expect(encode(otp.GetReply{FoundEntries: []otp.OTPInfo{devAStored(), devB()}})).To(Equal(unhex(getReplyHex)))
		Expect(encode(otp.GetCurrentTimeReply{Time: stamp})).To(Equal(unhex(currentTimeReplyHex)))
	})

	Describe("get_all", func() {
		var (
			r   *radosclient.ReadOp
			res *otp.GetResult
		)
		BeforeEach(func() {
			r = radosclient.NewReadOp()
			res = otp.GetAll(r, denc.Squid)
		})

		It("sends cls_otp_get_otp_op with get_all and no ids", func() {
			Expect(execStep(r.Steps(), "otp_get").In).To(Equal(unhex(getAllHex)))
		})
		It("decodes cls_otp_get_otp_reply's devices, seed_bin included", func() {
			execStep(r.Steps(), "otp_get").Result.Set(unhex(getReplyHex), 0)
			devs, err := res.Devices()
			Expect(err).NotTo(HaveOccurred())
			Expect(devs).To(Equal([]otp.OTPInfo{devAStored(), devB()}))
		})
		It("decodes an object without devices as none", func() {
			execStep(r.Steps(), "otp_get").Result.Set(unhex("010104000000"+"00000000"), 0)
			devs, err := res.Devices()
			Expect(err).NotTo(HaveOccurred())
			Expect(devs).To(BeEmpty())
		})
		It("reports the method's error", func() {
			execStep(r.Steps(), "otp_get").Result.Set(nil, -int32(syscall.ENOENT))
			_, err := res.Devices()
			Expect(err).To(MatchError(radosclient.ErrNotFound))
		})
		It("reports a reply that does not decode, naming no seed", func() {
			b := unhex(getReplyHex)
			execStep(r.Steps(), "otp_get").Result.Set(b[:len(b)-20], 0)
			_, err := res.Devices()
			Expect(err).To(MatchError(denc.ErrShortBuffer))
			Expect(err.Error()).NotTo(ContainSubstring("GEZDGNBV"))
		})
		It("reports a read before the op ran", func() {
			_, err := res.Devices()
			Expect(err).To(MatchError(radosclient.ErrIncomplete))
		})
	})

	Describe("get_current_time", func() {
		var (
			r   *radosclient.ReadOp
			res *otp.TimeResult
		)
		BeforeEach(func() {
			r = radosclient.NewReadOp()
			res = otp.GetCurrentTime(r, denc.Tentacle)
		})

		It("sends an empty cls_otp_get_current_time_op, not an empty input", func() {
			Expect(execStep(r.Steps(), "get_current_time").In).To(Equal(unhex(getCurrentTimeHex)))
		})
		It("decodes the OSD's time", func() {
			execStep(r.Steps(), "get_current_time").Result.Set(unhex(currentTimeReplyHex), 0)
			t, err := res.Time()
			Expect(err).NotTo(HaveOccurred())
			Expect(t).To(Equal(stamp))
		})
		It("reports the method's error", func() {
			execStep(r.Steps(), "get_current_time").Result.Set(nil, -int32(syscall.EINVAL))
			_, err := res.Time()
			Expect(err).To(MatchError(radosclient.ErrInvalid))
		})
		It("reports a reply that does not decode", func() {
			execStep(r.Steps(), "get_current_time").Result.Set(unhex("010108000000"+"00f1"), 0)
			_, err := res.Time()
			Expect(err).To(MatchError(denc.ErrShortBuffer))
		})
		It("reports a read before the op ran", func() {
			_, err := res.Time()
			Expect(err).To(MatchError(radosclient.ErrIncomplete))
		})
	})
})
