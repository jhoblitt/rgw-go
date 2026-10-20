package fakerados

import (
	"fmt"
	"slices"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cls/otp"
)

var _ = Describe("the otp class's method tables", func() {
	It("flags exactly the admin methods the class registers with CLS_METHOD_WR", func() {
		// https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L557-L575 at v19.2.7 and v20.2.4
		admin := otpAdminMethods(time.Now)
		Expect(admin.names()).To(Equal([]string{otp.MethodGetCurrentTime, otp.MethodGet, otp.MethodRemove, otp.MethodSet}))
		Expect(admin.writeMethods()).To(Equal([]string{otp.MethodRemove, otp.MethodSet}))
	})

	It("derives OTPMethods and OTPWriteMethods from every table the class assembles", func() {
		_, names, writes := assembleClass(otpClassTables(time.Now)...)
		Expect(OTPMethods).To(Equal(names))
		Expect(OTPWriteMethods).To(Equal(writes))
		Expect(OTPMethods).To(HaveCap(len(OTPMethods)), "a caller's append copies the list rather than writing into it")
		Expect(OTPWriteMethods).To(HaveCap(len(OTPWriteMethods)), "a caller's append copies the list rather than writing into it")

		extra := methodTable{"a_spec_read": {fn: nopClassMethod}, "spec_write": {fn: nopClassMethod, write: true}}
		_, names, writes = assembleClass(append(otpClassTables(time.Now), extra)...)
		Expect(names).To(Equal(slices.Sorted(slices.Values(append(slices.Clone(OTPMethods), "a_spec_read", "spec_write")))))
		Expect(writes).To(Equal(slices.Sorted(slices.Values(append(slices.Clone(OTPWriteMethods), "spec_write")))))
	})

	It("serves a method a table beside the admin methods holds", func() {
		extra := methodTable{"spec_check": {fn: func(*ClassCall) ([]byte, int32) { return []byte("checked"), 0 }}}
		methods, _, _ := assembleClass(append(otpClassTables(time.Now), extra)...)
		out, rval := methods.dispatch()(&ClassCall{Method: "spec_check"})
		Expect(rval).To(BeZero())
		Expect(out).To(Equal([]byte("checked")))
	})

	It("panics at assembly on a method two tables hold, since the class registers each name once", func() {
		Expect(func() {
			assembleClass(append(otpClassTables(time.Now), methodTable{otp.MethodGet: {fn: nopClassMethod}})...)
		}).To(PanicWith(`fakerados: class method "otp_get" is in two method tables`))
	})

	It("answers EOPNOTSUPP itself for a method no table holds", func() {
		_, rval := OTPClass()(&ClassCall{Method: "otp_no_such_method"})
		Expect(rval).To(Equal(-int32(syscall.EOPNOTSUPP)))
	})

	It("formats a stored device without its seed or seed_bin", func() {
		inst := otpInstance{
			OTP:         otp.OTPInfo{Type: otp.TypeTOTP, ID: "dev1", Seed: "5345435245545345", SeedBin: []byte("SECRETSE")},
			LastChecks:  []otp.OTPCheck{{Token: "tok"}},
			LastSuccess: 7,
		}
		for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
			for _, v := range []any{inst, &inst} {
				s := fmt.Sprintf(verb, v)
				Expect(s).To(ContainSubstring("dev1"), "%s of %T", verb, v)
				Expect(s).NotTo(ContainSubstring(inst.OTP.Seed), "%s of %T", verb, v)
				Expect(s).NotTo(ContainSubstring("SECRETSE"), "%s of %T", verb, v)
				Expect(s).NotTo(ContainSubstring("83 69 67"), "%s of %T: seed_bin's bytes", verb, v)
			}
		}
	})
})
