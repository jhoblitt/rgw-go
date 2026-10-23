package otp_test

import (
	"bytes"
	"fmt"
	"log/slog"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cls/otp"
	"github.com/jhoblitt/rgw-go/internal/testutil/otptest"
)

var _ = Describe("otp_instance", func() {
	BeforeEach(otptest.RedactDevices)

	// otp_instance (1,1), 63 bytes, as otp_set stores a fresh device: dev1
	// with seed_bin "12345", no checks, last_success 0.
	const dev1Hex = "01013f000000" +
		"01012d000000" + "02" + "04000000" + "64657631" + "0a000000" + "33313332333333343335" +
		"01" + "05000000" + "3132333435" + "00000000" + "1e000000" + "02000000" +
		"00000000" + // last_checks, empty
		"0000000000000000" // last_success 0
	// otp_instance (1,1), 76 bytes: devAStored, one failed check, and a
	// last_success whose bytes show their order.
	const checkedHex = "01014c000000" +
		devAStoredHex +
		"01000000" + "010110000000" + "03000000" + "746f6b" + stampHex + "02" +
		"0807060504030201"
	dev1 := func() otp.Instance {
		return otp.Instance{OTP: otp.OTPInfo{
			Type: otp.TypeTOTP, ID: "dev1", Seed: "3132333435", SeedType: otp.SeedHex,
			SeedBin: []byte("12345"), StepSize: 30, Window: 2,
		}}
	}
	checked := func() otp.Instance {
		return otp.Instance{
			OTP:         devAStored(),
			LastChecks:  []otp.OTPCheck{{Token: "tok", Timestamp: stamp, Result: otp.CheckFail}},
			LastSuccess: 0x0102030405060708,
		}
	}

	DescribeTable("encodes and decodes the hand-built golden",
		func(inst func() otp.Instance, golden string) {
			Expect(encode(inst())).To(Equal(unhex(golden)))
			got := decodeAll(unhex(golden), otp.DecodeInstance)
			if len(inst().LastChecks) == 0 {
				Expect(got.LastChecks).To(BeEmpty())
				got.LastChecks = nil
			}
			Expect(got).To(Equal(inst()))
		},
		Entry("a device otp_set stored, with no checks", dev1, dev1Hex),
		Entry("a device with a check and a last success", checked, checkedHex),
	)

	It("skips what a newer encoding appends, as DECODE_FINISH does", func() {
		b := unhex(checkedHex)
		b[0] = 2
		b[2]++
		b = append(b, 0xee)
		Expect(decodeAll(b, otp.DecodeInstance)).To(Equal(checked()))
	})

	It("keys a device's record by otp_key_prefix and its whole id", func() {
		Expect(otp.InstanceKey("dev1")).To(Equal("otp/dev1"))
		Expect(otp.InstanceKey("a\x00b")).To(Equal("otp/a\x00b"))
	})

	It("prints and logs the record without its seed or seed_bin", func() {
		inst := otp.Instance{OTP: secretDev(), LastSuccess: 7}
		for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
			for _, v := range []any{inst, &inst} {
				s := fmt.Sprintf(verb, v)
				Expect(s).To(ContainSubstring("phone"), "%s of %T", verb, v)
				Expect(s).NotTo(ContainSubstring("SECRET"), "%s of %T", verb, v)
			}
		}
		var buf bytes.Buffer
		slog.New(slog.NewJSONHandler(&buf, nil)).Info("record", slog.Any("instance", inst))
		Expect(buf.String()).To(ContainSubstring(`"last_success":7`))
		Expect(buf.String()).To(ContainSubstring(`"id":"phone"`))
		Expect(buf.String()).NotTo(ContainSubstring("SECRET"))
	})
})
