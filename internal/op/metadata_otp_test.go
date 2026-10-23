package op_test

import (
	"encoding/json"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cls/otp"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/formatter"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/testutil/otptest"
)

// otpSecret is a seed no other fixture holds, so a refusal's text is shown
// not to carry it.
const otpSecret = "SEEDSECRETSEEDSECRET"

// otpEscapedA is the JSON escape of "a", six bytes of document for one
// byte of seed.
const otpEscapedA = "\\u0061"

// rfcSeed is RFC 6238's SHA-1 test key in hex.
const rfcSeed = "3132333435363738393031323334353637383930"

// otpDevJSON is a device's JSON, a TOTP device with rfcSeed, with the
// members of extra replacing or adding to its own, a nil value removing one.
func otpDevJSON(extra map[string]any) string {
	GinkgoHelper()
	m := map[string]any{
		"type": 2, "id": "phone", "seed": rfcSeed, "seed_type": "hex",
		"time_ofs": 0, "step_size": 30, "window": 2,
	}
	for k, v := range extra {
		if v == nil {
			delete(m, k)
			continue
		}
		m[k] = v
	}
	b, err := json.Marshal(m)
	Expect(err).NotTo(HaveOccurred())
	return string(b)
}

// hideSeeds is s with each of seeds replaced, so a failure comparing it
// prints no seed.
func hideSeeds(s string, seeds ...string) string {
	for _, seed := range seeds {
		s = strings.ReplaceAll(s, seed, "<seed>")
	}
	return s
}

var _ = Describe("the otp metadata document", func() {
	BeforeEach(otptest.RedactDevices)
	rfc := func() otp.OTPInfo {
		return otp.OTPInfo{Type: otp.TypeTOTP, ID: "phone", Seed: rfcSeed, SeedType: otp.SeedHex, StepSize: 30, Window: 2}
	}

	It("decodes each device as otp_info_t::decode_json does, in the document's order", func() {
		doc, err := op.DecodeOTPDocument([]byte(`{"devices":[` + otpDevJSON(map[string]any{"id": "b", "time_ofs": "-30"}) + `,` +
			otpDevJSON(map[string]any{"id": "a", "seed": "GEZDGNBV", "seed_type": "base32", "window": 0}) + `]}`))
		Expect(err).NotTo(HaveOccurred())
		b, a := rfc(), rfc()
		b.ID, b.TimeOfs = "b", -30
		a.ID, a.Seed, a.SeedType, a.Window = "a", "GEZDGNBV", otp.SeedBase32, 0
		Expect(doc).To(Equal(op.OTPDocument{Devices: []otp.OTPInfo{b, a}}))
	})

	DescribeTable("decodes a document without devices as having none",
		func(data string) {
			doc, err := op.DecodeOTPDocument([]byte(data))
			Expect(err).NotTo(HaveOccurred())
			Expect(doc.Devices).To(BeEmpty())
		},
		Entry("an empty array", `{"devices":[]}`),
		Entry("no devices member, which decode_json resets to an empty list", `{}`),
		Entry("other members alone, which the handler does not read", `{"other":5}`),
	)

	DescribeTable("refuses with InvalidArgument what the section does not write, naming no seed",
		func(data string) {
			_, err := op.DecodeOTPDocument([]byte(data))
			Expect(err).To(MatchError(op.ErrInvalidArgument))
			// encoding/json quotes a bad \u escape with the four bytes
			// after it, so the check is for those four.
			Expect(strings.Contains(err.Error(), "SECR")).To(BeFalse(), "the refusal's text quotes the seed")
		},
		Entry("a document that is not JSON", `{"devices":[{"seed":"`+otpSecret+`"`),
		Entry("a malformed escape after a seed, which encoding/json quotes", `{"devices":[{"seed":"SEED\uSECRET"}]}`),
		Entry("devices given null", `{"devices":null}`),
		Entry("devices given an object, whose members decode_json would take as the list", `{"devices":{"x":`+otpDevJSON(nil)+`}}`),
		Entry("devices given a string", `{"devices":"`+otpSecret+`"}`),
		Entry("a device that is no object", `{"devices":[5]}`),
		Entry("a device whose id is null", `{"devices":[`+otpDevJSON(map[string]any{"id": json.RawMessage(`null`), "seed": otpSecret})+`]}`),
		Entry("a device whose seed is an object", `{"devices":[`+otpDevJSON(map[string]any{"seed": map[string]any{"k": otpSecret}})+`]}`),
		Entry("a device whose seed is an array", `{"devices":[`+otpDevJSON(map[string]any{"seed": []any{otpSecret}})+`]}`),
		Entry("a device whose seed type is an array", `{"devices":[`+otpDevJSON(map[string]any{"seed_type": []any{"hex"}, "seed": otpSecret})+`]}`),
		Entry("a step_size that is no number", `{"devices":[`+otpDevJSON(map[string]any{"step_size": otpSecret, "seed": otpSecret})+`]}`),
		Entry("a step_size of 0, by which the class divides", `{"devices":[`+otpDevJSON(map[string]any{"step_size": 0, "seed": otpSecret})+`]}`),
		Entry("no step_size, which decode_json resets to 0", `{"devices":[`+otpDevJSON(map[string]any{"step_size": nil})+`]}`),
		Entry("an HOTP device", `{"devices":[`+otpDevJSON(map[string]any{"type": 1, "seed": otpSecret})+`]}`),
		Entry("a device of type 0", `{"devices":[`+otpDevJSON(map[string]any{"type": 0})+`]}`),
		Entry("no type, which decode_json resets to 0", `{"devices":[`+otpDevJSON(map[string]any{"type": nil})+`]}`),
		Entry("a seed type of unknown", `{"devices":[`+otpDevJSON(map[string]any{"seed_type": "unknown", "seed": otpSecret})+`]}`),
		Entry("a seed type compared exactly", `{"devices":[`+otpDevJSON(map[string]any{"seed_type": "HEX"})+`]}`),
		Entry("no seed type", `{"devices":[`+otpDevJSON(map[string]any{"seed_type": nil})+`]}`),
		Entry("a device without an id, as mfa create refuses", `{"devices":[`+otpDevJSON(map[string]any{"id": nil, "seed": otpSecret})+`]}`),
		Entry("an empty id", `{"devices":[`+otpDevJSON(map[string]any{"id": ""})+`]}`),
		Entry("an empty seed, which the class parses into an empty key", `{"devices":[`+otpDevJSON(map[string]any{"seed": ""})+`]}`),
		Entry("a hex seed led by a NUL, as oath_hex2bin stops at the NUL", `{"devices":[`+otpDevJSON(map[string]any{"seed": "\x00abcd"})+`]}`),
		Entry("a base32 seed of nothing but spaces, as oath_base32_decode drops spaces", `{"devices":[`+otpDevJSON(map[string]any{"seed": "    ", "seed_type": "base32"})+`]}`),
		Entry("no seed", `{"devices":[`+otpDevJSON(map[string]any{"seed": nil})+`]}`),
		Entry("a seed of 1025 bytes", `{"devices":[`+otpDevJSON(map[string]any{"seed": strings.Repeat("a", 1025)})+`]}`),
		Entry("a seed of 1025 bytes written as 6150 of escapes, as the bound counts the decoded string",
			`{"devices":[{"type":2,"id":"x","seed":"`+strings.Repeat(otpEscapedA, 1025)+`","seed_type":"hex","step_size":30}]}`),
		Entry("a seed of 513 two-byte characters, 1026 bytes",
			`{"devices":[`+otpDevJSON(map[string]any{"seed": strings.Repeat("é", 513)})+`]}`),
		Entry("a second device over the bound after a good one",
			`{"devices":[`+otpDevJSON(nil)+`,`+otpDevJSON(map[string]any{"id": "two", "seed": otpSecret + strings.Repeat("a", 1024)})+`]}`),
		Entry("a good device after a bad one", `{"devices":[`+otpDevJSON(map[string]any{"step_size": 0})+`,`+otpDevJSON(nil)+`]}`),
	)

	DescribeTable("bounds the window and the step, taking each bound and refusing one past it",
		func(extra map[string]any, ok bool) {
			_, err := op.DecodeOTPDocument([]byte(`{"devices":[` + otpDevJSON(extra) + `]}`))
			if ok {
				Expect(err).NotTo(HaveOccurred())
				return
			}
			Expect(err).To(MatchError(op.ErrInvalidArgument))
		},
		Entry("a window of 1024, which costs a wrong code 2049 HOTP codes in the class", map[string]any{"window": 1024}, true),
		Entry("a window of 1025", map[string]any{"window": 1025}, false),
		Entry("a window of UINT_MAX, a walk of 2^33 codes on the OSD", map[string]any{"window": 4294967295}, false),
		Entry("a window of 0, which checks the code's own step alone", map[string]any{"window": 0}, true),
		Entry("a step of 3600", map[string]any{"step_size": 3600}, true),
		Entry("a step of 3601", map[string]any{"step_size": 3601}, false),
		Entry("a step past the clock's seconds, whose trim wraps", map[string]any{"step_size": 4294967295}, false),
		Entry("a time_ofs of INT_MIN, which bounds no cost", map[string]any{"time_ofs": -2147483648}, true),
		Entry("a time_ofs of INT_MAX", map[string]any{"time_ofs": 2147483647}, true),
	)

	DescribeTable("takes a seed whose key is not empty",
		func(seed, seedType string) {
			_, err := op.DecodeOTPDocument([]byte(`{"devices":[` + otpDevJSON(map[string]any{"seed": seed, "seed_type": seedType}) + `]}`))
			Expect(err).NotTo(HaveOccurred())
		},
		Entry("a hex seed", rfcSeed, "hex"),
		Entry("a base32 seed with spaces among its letters", "GEZD GNBV", "base32"),
		Entry("a hex seed whose NUL follows its digits, which the class reads up to it", "3132\x00", "hex"),
	)

	DescribeTable("takes a seed of up to 1024 bytes as decoded",
		func(raw, want string) {
			doc, err := op.DecodeOTPDocument([]byte(`{"devices":[{"type":2,"id":"x","seed":"` + raw + `","seed_type":"hex","step_size":30}]}`))
			Expect(err).NotTo(HaveOccurred())
			Expect(doc.Devices).To(HaveLen(1))
			Expect(len(doc.Devices[0].Seed)).To(Equal(1024))                           //nolint:ginkgolinter // HaveLen would print the seed on a failure
			Expect(doc.Devices[0].Seed == want).To(BeTrue(), "the seed decodes whole") //nolint:ginkgolinter // Equal would print the seed on a failure
		},
		Entry("1024 bytes", strings.Repeat("a", 1024), strings.Repeat("a", 1024)),
		Entry("1024 bytes of two-byte characters", strings.Repeat("é", 512), strings.Repeat("é", 512)),
		Entry("1024 bytes written as 6144 of escapes", strings.Repeat(otpEscapedA, 1024), strings.Repeat("a", 1024)),
	)

	It("dumps as RGWOTPMetadataObject::dump does, each device an obj section, and as JSON the same", func() {
		doc := op.OTPDocument{Devices: []otp.OTPInfo{rfc(), {Type: otp.TypeTOTP, ID: "b", Seed: "GEZDGNBV", SeedType: otp.SeedBase32, SeedBin: []byte("12345"), StepSize: 60}}}
		f := formatter.NewJSON(false)
		f.OpenObjectSection("data")
		doc.Dump(f, denc.Squid)
		f.CloseSection()
		Expect(f.Err()).NotTo(HaveOccurred())
		want := `{"devices":[` +
			`{"type":2,"id":"phone","seed":"` + rfcSeed + `","seed_type":"hex","time_ofs":0,"step_size":30,"window":2},` +
			`{"type":2,"id":"b","seed":"GEZDGNBV","seed_type":"base32","time_ofs":0,"step_size":60,"window":0}]}`
		Expect(hideSeeds(string(f.Bytes()), rfcSeed, "GEZDGNBV")).To(Equal(hideSeeds(want, rfcSeed, "GEZDGNBV")))
		Expect(string(f.Bytes()) == want).To(BeTrue(), "the dump writes each device's seed") //nolint:ginkgolinter // Equal would print the seeds on a failure; the line above compares them hidden
		b, err := json.Marshal(doc)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b) == string(f.Bytes())).To(BeTrue(), "the JSON is the dump") //nolint:ginkgolinter // Equal would print the seeds on a failure
	})

	It("dumps no devices as an empty array, never null", func() {
		b, err := json.Marshal(op.OTPDocument{})
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(Equal(`{"devices":[]}`))
		x := formatter.NewXML(false, false)
		x.OpenObjectSection("data")
		op.OTPDocument{}.Dump(x, denc.Squid)
		x.CloseSection()
		Expect(string(x.Bytes())).To(Equal(`<data><devices></devices></data>`))
	})

	It("round-trips its JSON", func() {
		doc := op.OTPDocument{Devices: []otp.OTPInfo{rfc()}}
		b, err := json.Marshal(doc)
		Expect(err).NotTo(HaveOccurred())
		got, err := op.DecodeOTPDocument(b)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(doc))
	})
})
