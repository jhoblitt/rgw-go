package otp_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cls/otp"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/formatter"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/testutil/otptest"
)

func encode(v interface {
	Encode(*denc.Encoder, denc.Release)
},
) []byte {
	e := denc.NewEncoder()
	v.Encode(e, denc.Squid)
	return e.Bytes()
}

// decodeAll decodes b whole with dec, failing on an error or unread bytes.
func decodeAll[T any](b []byte, dec func(*denc.Decoder) T) T {
	GinkgoHelper()
	d := denc.NewDecoder(b)
	v := dec(d)
	Expect(d.Err()).NotTo(HaveOccurred())
	Expect(d.Remaining()).To(BeZero())
	return v
}

var _ = Describe("otp_info_t", func() {
	BeforeEach(otptest.RedactDevices)

	DescribeTable("encodes and decodes the hand-built golden",
		func(dev func() otp.OTPInfo, golden string) {
			Expect(encode(dev())).To(Equal(unhex(golden)))
			Expect(decodeAll(unhex(golden), otp.DecodeOTPInfo)).To(Equal(dev()))
		},
		Entry("a hex device as sent, negative time_ofs and no seed_bin", devA, devAHex),
		Entry("the same device as stored, with seed_bin", devAStored, devAStoredHex),
		Entry("a base32 device as stored", devB, devBHex),
	)

	It("keeps a type and a seed type byte no enumerator names", func() {
		d := devA()
		d.Type, d.SeedType = 0xff, 7
		Expect(decodeAll(encode(d), otp.DecodeOTPInfo)).To(Equal(d))
	})

	It("encodes a type beyond a byte as its low byte, as the C++ cast to uint8_t does", func() {
		d := devA()
		d.Type = 0x102
		Expect(encode(d)).To(Equal(unhex(devAHex)))
	})

	It("skips the fields a later struct version appends", func() {
		b := unhex(devAHex)
		b[0] = 2            // struct_v 2, compat still 1
		b[2]++              // one more byte in the frame
		b = append(b, 0xaa) // that byte
		Expect(decodeAll(b, otp.DecodeOTPInfo)).To(Equal(devA()))
	})

	It("refuses a struct whose compat is newer than version 1", func() {
		b := unhex(devAHex)
		b[0], b[1] = 2, 2
		d := denc.NewDecoder(b)
		otp.DecodeOTPInfo(d)
		Expect(d.Err()).To(MatchError(denc.ErrIncompatible))
	})
})

var _ = Describe("otp_check_t", func() {
	const golden = "010110000000" + // 16 bytes
		"03000000" + "746f6b" + // token "tok"
		stampHex + // timestamp
		"02" // result, encoded as a char: FAIL

	It("encodes and decodes the hand-built golden", func() {
		c := otp.OTPCheck{Token: "tok", Timestamp: stamp, Result: otp.CheckFail}
		Expect(encode(c)).To(Equal(unhex(golden)))
		Expect(decodeAll(unhex(golden), otp.DecodeOTPCheck)).To(Equal(c))
	})

	It("keeps a result byte no enumerator names", func() {
		c := otp.OTPCheck{Token: "t", Timestamp: stamp, Result: 0xff}
		Expect(decodeAll(encode(c), otp.DecodeOTPCheck)).To(Equal(c))
	})

	It("names the check results as the class does", func() {
		Expect([]otp.CheckResult{otp.CheckUnknown, otp.CheckSuccess, otp.CheckFail}).To(Equal([]otp.CheckResult{0, 1, 2}))
	})
})

var _ = Describe("the enums", func() {
	It("number the types and seed types as cls_otp_types.h does", func() {
		Expect([]otp.Type{otp.TypeUnknown, otp.TypeHOTP, otp.TypeTOTP}).To(Equal([]otp.Type{0, 1, 2}))
		Expect([]otp.SeedType{otp.SeedUnknown, otp.SeedHex, otp.SeedBase32}).To(Equal([]otp.SeedType{0, 1, 2}))
	})
})

var _ = Describe("JSON", func() {
	BeforeEach(otptest.RedactDevices)

	It("dumps as otp_info_t::dump does, with no seed_bin", func() {
		b, err := json.Marshal(devB())
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(Equal(`{"type":2,"id":"dev2","seed":"GEZDGNBV","seed_type":"base32","time_ofs":0,"step_size":30,"window":2}`))
	})

	It("dumps a hex seed type and a negative time_ofs", func() {
		b, err := json.Marshal(devA())
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(Equal(`{"type":2,"id":"dev1","seed":"3132","seed_type":"hex","time_ofs":-30,"step_size":30,"window":2}`))
	})

	DescribeTable("dumps a seed type other than hex or base32 as unknown",
		func(st otp.SeedType) {
			d := devA()
			d.SeedType = st
			b, err := json.Marshal(d)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(b)).To(ContainSubstring(`"seed_type":"unknown"`))
		},
		Entry("unknown", otp.SeedUnknown),
		Entry("a value no enumerator names", otp.SeedType(7)),
	)

	DescribeTable("dumps through a formatter as MarshalJSON writes it, otp_info_t::dump's one form",
		func(dev func() otp.OTPInfo) {
			f := formatter.NewJSON(false)
			f.OpenObjectSection("obj")
			dev().Dump(f, denc.Squid)
			f.CloseSection()
			Expect(f.Err()).NotTo(HaveOccurred())
			want, err := json.Marshal(dev())
			Expect(err).NotTo(HaveOccurred())
			Expect(string(f.Bytes())).To(Equal(string(want)))
		},
		Entry("a hex device with a negative time_ofs", devA),
		Entry("a base32 device with a seed_bin, which neither writes", devB),
		Entry("a seed type no enumerator names, and a type beyond the enum", func() otp.OTPInfo {
			d := devA()
			d.Type, d.SeedType = 258, 7
			return d
		}),
		Entry("the limits of unsigned", func() otp.OTPInfo {
			d := devA()
			d.StepSize, d.Window = 4294967295, 4294967295
			return d
		}),
	)

	It("dumps a type out of the enum's range as the int it holds", func() {
		d := devA()
		d.Type = 258
		b, err := json.Marshal(d)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(HavePrefix(`{"type":258,`))
	})

	It("round-trips a dump, without the seed_bin", func() {
		b, err := json.Marshal(devB())
		Expect(err).NotTo(HaveOccurred())
		var got otp.OTPInfo
		Expect(json.Unmarshal(b, &got)).To(Succeed())
		want := devB()
		want.SeedBin = nil
		Expect(got).To(Equal(want))
	})

	It("replaces every field, the seed_bin included", func() {
		got := devB()
		Expect(json.Unmarshal([]byte(`{"id":"x"}`), &got)).To(Succeed())
		Expect(got).To(Equal(otp.OTPInfo{ID: "x"}))
	})

	DescribeTable("decodes as otp_info_t::decode_json does",
		func(doc string, want otp.OTPInfo) {
			var got otp.OTPInfo
			Expect(json.Unmarshal([]byte(doc), &got)).To(Succeed())
			Expect(got).To(Equal(want))
		},
		Entry("an absent member as its zero value, not the constructor's default",
			`{}`, otp.OTPInfo{}),
		Entry("numbers given as strings, with white space around them",
			`{"type":"2","time_ofs":" -5","step_size":"30 ","window":"\t2\n"}`,
			otp.OTPInfo{Type: otp.TypeTOTP, TimeOfs: -5, StepSize: 30, Window: 2}),
		Entry("a number up to a NUL, as strtol reads the C string",
			`{"step_size":"30\u0000x"}`, otp.OTPInfo{StepSize: 30}),
		Entry("a seed type compared exactly",
			`{"seed_type":"HEX"}`, otp.OTPInfo{SeedType: otp.SeedUnknown}),
		Entry("a hex seed type", `{"seed_type":"hex"}`, otp.OTPInfo{SeedType: otp.SeedHex}),
		Entry("a base32 seed type", `{"seed_type":"base32"}`, otp.OTPInfo{SeedType: otp.SeedBase32}),
		Entry("a string member given a number or a boolean, as its text",
			`{"id":12,"seed":true}`, otp.OTPInfo{ID: "12", Seed: "true"}),
		Entry("a type beyond the enum, kept as the int it is",
			`{"type":258}`, otp.OTPInfo{Type: 258}),
		Entry("the limits of int and unsigned",
			`{"type":-2147483648,"time_ofs":2147483647,"step_size":4294967295,"window":"-0"}`,
			otp.OTPInfo{Type: -2147483648, TimeOfs: 2147483647, StepSize: 4294967295}),
		Entry("the first of a repeated member, as find_first finds it",
			`{"id":"a","id":"b"}`, otp.OTPInfo{ID: "a"}),
		Entry("a document that is no object as having no members",
			`[1,2]`, otp.OTPInfo{}),
	)

	DescribeTable("refuses what decode_json refuses or cannot mean",
		func(doc string) {
			var got otp.OTPInfo
			Expect(json.Unmarshal([]byte(doc), &got)).To(MatchError(meta.ErrBadJSONValue), doc)
		},
		Entry("a type above INT_MAX", `{"type":2147483648}`),
		Entry("a time_ofs that is no number", `{"time_ofs":"x"}`),
		Entry("a negative step_size, which strtoul lifts above UINT_MAX", `{"step_size":-1}`),
		Entry("a step_size above UINT_MAX", `{"step_size":4294967296}`),
		Entry("a window with a fraction", `{"window":1.5}`),
		Entry("a type given a boolean", `{"type":true}`),
		Entry("a type given an empty string, in which strtol reads no digits", `{"type":""}`),
		Entry("an id given null", `{"id":null}`),
		Entry("a seed given an object", `{"seed":{}}`),
		Entry("a seed type given an array", `{"seed_type":[]}`),
	)

	It("names no seed in the errors it returns when handed a document itself", func() {
		var got otp.OTPInfo
		for _, doc := range []string{
			`{"seed":{"k":"SEEDSECRET"}}`,
			`{"seed":"SEEDSECRET","step_size":"SEEDSECRET"}`,
			`{"seed":"SEEDSECRET"`,
			// Go's JSON syntax error quotes a bad escape and the four
			// characters after a \u.
			`{"seed":"SEED\uSECRET"}`,
		} {
			err := got.UnmarshalJSON([]byte(doc))
			Expect(err).To(MatchError(meta.ErrBadJSONValue), doc)
			Expect(err.Error()).NotTo(ContainSubstring("SECR"), doc)
		}
	})

	// encoding/json checks the syntax of the whole document before it calls
	// UnmarshalJSON, so what a caller of json.Unmarshal gets is pinned here.
	DescribeTable("through json.Unmarshal fails a malformed device with encoding/json's syntax error, which quotes the seed",
		func(shape string) {
			err := unmarshal(shape, `{"seed":"SEED\uSECRET"}`)
			var syntax *json.SyntaxError
			Expect(errors.As(err, &syntax)).To(BeTrue(), "%v", err)
			Expect(err).NotTo(MatchError(meta.ErrBadJSONValue))
			Expect(err.Error()).To(ContainSubstring("SECR"))
		},
		shapes,
	)

	DescribeTable("through json.Unmarshal fails a bad member with UnmarshalJSON's error, which names no seed",
		func(shape string) {
			err := unmarshal(shape, `{"seed":"SEEDSECRET","step_size":"SEEDSECRET"}`)
			Expect(err).To(MatchError(meta.ErrBadJSONValue))
			Expect(err.Error()).To(ContainSubstring("step_size"))
			Expect(err.Error()).NotTo(ContainSubstring("SECRET"))
		},
		shapes,
	)
})

// shapes are the documents a caller hands json.Unmarshal a device in.
var shapes = []TableEntry{
	Entry("one device", "device"), Entry("a slice", "slice"), Entry(`a {"data":[...]} envelope`, "envelope"),
}

// unmarshal decodes doc, one device's JSON, through json.Unmarshal in the
// shape named.
func unmarshal(shape, doc string) error {
	switch shape {
	case "device":
		var d otp.OTPInfo
		return json.Unmarshal([]byte(doc), &d)
	case "slice":
		var ds []otp.OTPInfo
		return json.Unmarshal([]byte("["+doc+"]"), &ds)
	default:
		var e struct {
			Data []otp.OTPInfo `json:"data"`
		}
		return json.Unmarshal([]byte(`{"data":[`+doc+`]}`), &e)
	}
}

var _ = Describe("redaction", func() {
	// leaks reports whether s shows secretDev's seed or seed_bin in any of
	// the forms fmt prints a string or a byte slice.
	leaks := func(s string) bool {
		return bytes.Contains([]byte(s), []byte("SECRET")) ||
			bytes.Contains([]byte(s), []byte("66 73 78")) || // "BIN" as %v prints bytes
			bytes.Contains([]byte(s), []byte("0x42, 0x49")) || // as %#v prints them
			bytes.Contains([]byte(s), []byte("42494e")) || // as %x prints them
			bytes.Contains([]byte(s), []byte("534543524554")) // "SECRET" as %x prints it
	}

	// holders are the ways fmt reaches a device through its methods, built
	// around d.
	holders := func(d otp.OTPInfo) []any {
		devs := []otp.OTPInfo{d}
		return []any{
			d, &d, devs,
			map[string]otp.OTPInfo{"k": d},
			struct{ Dev otp.OTPInfo }{d},
			otp.SetOp{Entries: devs},
			&otp.SetOp{Entries: devs},
			otp.GetReply{FoundEntries: devs},
		}
	}

	DescribeTable("prints no seed or seed_bin under any verb",
		func(format string) {
			blank := secretDev()
			blank.Seed, blank.SeedBin = "", nil
			secret, other := holders(secretDev()), holders(blank)
			for n := range secret {
				out := fmt.Sprintf(format, secret[n])
				Expect(leaks(out)).To(BeFalse(), "%s printed %T as %q", format, secret[n], out)
				// Output that depends on neither field shows neither, in
				// whatever form the verb would have given them.
				Expect(out).To(Equal(fmt.Sprintf(format, other[n])), "%s printed %T", format, secret[n])
			}
		},
		Entry(nil, "%v"), Entry(nil, "%+v"), Entry(nil, "%#v"), Entry(nil, "%s"), Entry(nil, "%q"),
		Entry(nil, "%x"), Entry(nil, "%X"), Entry(nil, "% x"), Entry(nil, "%#x"), Entry(nil, "%d"),
		Entry(nil, "%t"), Entry(nil, "%o"), Entry(nil, "%O"), Entry(nil, "%b"), Entry(nil, "%c"),
		Entry(nil, "%U"), Entry(nil, "%#U"), Entry(nil, "%e"), Entry(nil, "%E"), Entry(nil, "%f"),
		Entry(nil, "%F"), Entry(nil, "%g"), Entry(nil, "%G"), Entry(nil, "%z"),
		Entry(nil, "%-40.12s"), Entry(nil, "%+q"), Entry(nil, "%08d"),
	)

	It("prints String under a string verb, with its width and precision, GoString under %#v, and reports any other verb", func() {
		d := secretDev()
		Expect(fmt.Sprintf("%.5s|%12.3s", d, d)).To(Equal(d.String()[:5] + "|" + fmt.Sprintf("%12s", d.String()[:3])))
		Expect(fmt.Sprintf("%#v", d)).To(Equal(d.GoString()))
		Expect(fmt.Sprintf("%d", d)).To(Equal("%!d(otp.OTPInfo=" + d.String() + ")"))
	})

	It("names the device in its String", func() {
		d := secretDev()
		Expect(d.String()).To(ContainSubstring("phone"))
		Expect(leaks(d.String())).To(BeFalse(), d.String())
		Expect(leaks(d.GoString())).To(BeFalse(), d.GoString())
	})

	// The paths OTPInfo's godoc tells callers to keep devices off, where fmt
	// prints the fields without calling a method.
	It("cannot keep the seed out of an unexported field, a %p of a value or a %w", func() {
		type holder struct{ devs []otp.OTPInfo }
		d := secretDev()
		Expect(fmt.Sprintf("%v", holder{devs: []otp.OTPInfo{d}})).To(ContainSubstring("SEEDSECRET"))
		// Held in variables, so vet's printf check, which reports a %w in
		// Sprintf, lets the spec build. It reports no %p of a Formatter.
		pointerVerb, wrapVerb := "%p", "%w"
		Expect(fmt.Sprintf(pointerVerb, d)).To(ContainSubstring("SEEDSECRET"))
		Expect(fmt.Sprintf(wrapVerb, &d)).To(ContainSubstring("SEEDSECRET"))
	})

	It("logs a device through slog without its seed or seed_bin", func() {
		var buf bytes.Buffer
		for _, h := range []slog.Handler{slog.NewJSONHandler(&buf, nil), slog.NewTextHandler(&buf, nil)} {
			slog.New(h).Info("device", slog.Any("device", secretDev()))
		}
		Expect(buf.String()).To(ContainSubstring("phone"))
		Expect(leaks(buf.String())).To(BeFalse(), buf.String())
	})
})
