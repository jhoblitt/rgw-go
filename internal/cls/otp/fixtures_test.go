package otp_test

import (
	"encoding/hex"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cls/otp"
)

// The goldens are hand-built from the C++ encoders: ceph-object-corpus holds
// no otp type and ceph-dencoder registers none at v19.2.7 or v20.2.4.
// cls_otp_types.h and cls_otp_ops.h are identical at both tags: otp_info_t
// and otp_check_t
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp_types.h#L31-L107 at v19.2.7 and v20.2.4),
// cls_otp_set_otp_op to cls_otp_get_current_time_reply
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp_ops.h#L11-L167 at v19.2.7 and v20.2.4).
// Each frame is u8 struct_v, u8 compat, u32 length; integers are little-endian.

// devA is a device as a client sends it: a hex seed, no seed_bin, a negative
// time offset.
func devA() otp.OTPInfo {
	return otp.OTPInfo{Type: otp.TypeTOTP, ID: "dev1", Seed: "3132", SeedType: otp.SeedHex, TimeOfs: -30, StepSize: 30, Window: 2}
}

const devAHex = "010122000000" + // otp_info_t (1,1), 34 bytes
	"02" + // type TOTP
	"04000000" + "64657631" + // id "dev1"
	"04000000" + "33313332" + // seed "3132"
	"01" + // seed_type hex
	"00000000" + // seed_bin, empty
	"e2ffffff" + // time_ofs -30
	"1e000000" + // step_size 30
	"02000000" // window 2

// devAStored is devA as otp_get returns it, with the seed_bin otp_set parsed
// from the hex seed.
func devAStored() otp.OTPInfo {
	d := devA()
	d.SeedBin = []byte("12")
	return d
}

const devAStoredHex = "010124000000" + "02" + "04000000" + "64657631" + "04000000" + "33313332" + "01" +
	"02000000" + "3132" + // seed_bin 0x31 0x32
	"e2ffffff" + "1e000000" + "02000000"

// devB is a stored device with a base32 seed, "12345" in base32.
func devB() otp.OTPInfo {
	return otp.OTPInfo{Type: otp.TypeTOTP, ID: "dev2", Seed: "GEZDGNBV", SeedType: otp.SeedBase32, SeedBin: []byte("12345"), StepSize: 30, Window: 2}
}

const devBHex = "01012b000000" + // 43 bytes
	"02" + "04000000" + "64657632" +
	"08000000" + "47455a44474e4256" + // seed "GEZDGNBV"
	"02" + // seed_type base32
	"05000000" + "3132333435" + // seed_bin "12345"
	"00000000" + "1e000000" + "02000000"

// secretDev carries a seed and seed_bin no other fixture holds, so output
// that names neither is shown not to leak them.
func secretDev() otp.OTPInfo {
	return otp.OTPInfo{Type: otp.TypeTOTP, ID: "phone", Seed: "SEEDSECRET", SeedType: otp.SeedBase32, SeedBin: []byte("BINSECRET"), StepSize: 30, Window: 2}
}

// stamp is 1700000000.123456789, encoded as u32 seconds 0x6553f100 and u32
// nanoseconds 0x075bcd15.
var stamp = time.Unix(1700000000, 123456789).UTC()

const stampHex = "00f15365" + "15cd5b07"

func unhex(s string) []byte {
	GinkgoHelper()
	b, err := hex.DecodeString(s)
	Expect(err).NotTo(HaveOccurred())
	return b
}
