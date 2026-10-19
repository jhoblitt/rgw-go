package otp_test

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"log/slog"
	"strings"
	"syscall"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cls/otp"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// The goldens are hand-built, as fixtures_test.go's are, from
// cls_otp_check_otp_op and cls_otp_get_result_reply
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp_ops.h#L28-L83 at v19.2.7 and v20.2.4).

// checkReq is a check as OTP::check builds it: the device id, the code and a
// token of 16 random characters from radosgw's 64-character table,
// A-Za-z0-9-_
// (https://github.com/ceph/ceph/blob/v19.2.7/src/common/random_string.cc#L45-L51 at v19.2.7 and v20.2.4).
func checkReq() otp.CheckOp {
	return otp.CheckOp{ID: "dev1", Val: "123456", Token: "AbCdEfGh01234567"}
}

const (
	tokenHex = "10000000" + "41624364456647683031323334353637" // "AbCdEfGh01234567"

	// cls_otp_check_otp_op (1,1), 38 bytes.
	checkReqHex = "010126000000" +
		"04000000" + "64657631" + // id "dev1"
		"06000000" + "313233343536" + // val "123456"
		tokenHex
)

// replyHex is cls_otp_get_result_reply (1,1), 35 bytes, holding otp_check_t
// (1,1), 29 bytes: checkReq's token, stamp, and the result byte given.
func replyHex(result string) string {
	return "010123000000" + "01011d000000" + tokenHex + stampHex + result
}

// recorded is the check replyHex holds.
func recorded(res otp.CheckResult) otp.OTPCheck {
	return otp.OTPCheck{Token: checkReq().Token, Timestamp: stamp, Result: res}
}

var _ = Describe("the check methods", func() {
	It("names them as CLS_INIT registers them", func() {
		Expect([]string{otp.MethodCheck, otp.MethodGetResult}).To(Equal([]string{"otp_check", "otp_get_result"}))
	})
})

var _ = Describe("cls_otp_check_otp_op", func() {
	It("encodes and decodes the hand-built golden", func() {
		Expect(encode(checkReq())).To(Equal(unhex(checkReqHex)))
		Expect(decodeAll(unhex(checkReqHex), otp.DecodeCheckOp)).To(Equal(checkReq()))
	})

	It("skips the fields a later struct version appends", func() {
		b := unhex(checkReqHex)
		b[0] = 2
		b[2]++
		b = append(b, 0xaa)
		Expect(decodeAll(b, otp.DecodeCheckOp)).To(Equal(checkReq()))
	})
})

var _ = Describe("cls_otp_get_result_reply", func() {
	DescribeTable("encodes and decodes the hand-built golden, whatever the result",
		func(res otp.CheckResult, b string) {
			want := otp.GetResultReply{Result: recorded(res)}
			Expect(encode(want)).To(Equal(unhex(replyHex(b))))
			Expect(decodeAll(unhex(replyHex(b)), otp.DecodeGetResultReply)).To(Equal(want))
		},
		Entry("unknown", otp.CheckUnknown, "00"),
		Entry("success", otp.CheckSuccess, "01"),
		Entry("fail", otp.CheckFail, "02"),
		Entry("a byte no enumerator names", otp.CheckResult(0xff), "ff"),
	)

	It("skips the fields a later struct version appends", func() {
		b := unhex(replyHex("01"))
		b[0] = 2
		b[2]++
		b = append(b, 0xaa)
		Expect(decodeAll(b, otp.DecodeGetResultReply)).To(Equal(otp.GetResultReply{Result: recorded(otp.CheckSuccess)}))
	})
})

// decoders are the two wire types' decoders, by name.
var decoders = map[string]func([]byte) error{
	"check op": func(b []byte) error {
		d := denc.NewDecoder(b)
		otp.DecodeCheckOp(d)
		return d.Err()
	},
	"reply": func(b []byte) error {
		d := denc.NewDecoder(b)
		otp.DecodeGetResultReply(d)
		return d.Err()
	},
}

var _ = Describe("decode refusals", func() {
	DescribeTable("refuses every cut of the golden as short",
		func(decoder, golden string) {
			b := unhex(golden)
			for n := range b {
				Expect(decoders[decoder](b[:n])).To(MatchError(denc.ErrShortBuffer), "%d of %d bytes", n, len(b))
			}
		},
		Entry("a check op", "check op", checkReqHex),
		Entry("a reply", "reply", replyHex("01")),
	)

	// edit changes a golden in place at the offset given.
	type edit struct {
		off int
		val byte
	}

	DescribeTable("refuses a frame it cannot read",
		func(decoder, golden string, want error, edits ...edit) {
			b := unhex(golden)
			for _, e := range edits {
				b[e.off] = e.val
			}
			Expect(decoders[decoder](b)).To(MatchError(want))
		},
		Entry("a check op whose compat is newer than version 1",
			"check op", checkReqHex, denc.ErrIncompatible, edit{0, 2}, edit{1, 2}),
		Entry("a reply whose compat is newer than version 1",
			"reply", replyHex("01"), denc.ErrIncompatible, edit{0, 2}, edit{1, 2}),
		Entry("a reply whose check's compat is newer than version 1",
			"reply", replyHex("01"), denc.ErrIncompatible, edit{6, 2}, edit{7, 2}),
		Entry("a check op whose length ends inside its token",
			"check op", checkReqHex, denc.ErrOverread, edit{2, 0x25}),
		Entry("a reply whose length ends inside its check",
			"reply", replyHex("01"), denc.ErrOverread, edit{2, 0x22}),
		Entry("a reply whose check's length ends before its result",
			"reply", replyHex("01"), denc.ErrOverread, edit{8, 0x1c}),
	)
})

var _ = Describe("check", func() {
	It("sends otp_check the check op on a write op", func() {
		w := radosclient.NewWriteOp()
		otp.Check(w, checkReq(), denc.Squid)
		Expect(execStep(w.Steps(), "otp_check").In).To(Equal(unhex(checkReqHex)))
	})

	It("adds the one otp_check exec, whose ENOENT result radosclient maps to ErrNotFound", func() {
		w := radosclient.NewWriteOp()
		otp.Check(w, checkReq(), denc.Tentacle)
		res := execStep(w.Steps(), "otp_check").Result
		res.Set(nil, -int32(syscall.ENOENT))
		_, err := res.Bytes()
		Expect(err).To(MatchError(radosclient.ErrNotFound))
	})
})

var _ = Describe("lookup", func() {
	DescribeTable("sends otp_get_result exactly the bytes check sends otp_check, never cls_otp_get_result_op",
		func(req otp.CheckOp, r denc.Release) {
			w := radosclient.NewWriteOp()
			otp.Check(w, req, r)
			rd := radosclient.NewReadOp()
			otp.LookupCheck(rd, req, r)
			sent := execStep(w.Steps(), "otp_check").In
			Expect(execStep(rd.Steps(), "otp_get_result").In).To(Equal(sent))
			Expect(decodeAll(sent, otp.DecodeCheckOp)).To(Equal(req))
		},
		Entry("the golden check at Squid", checkReq(), denc.Squid),
		Entry("the golden check at Tentacle", checkReq(), denc.Tentacle),
		Entry("a check with every field empty", otp.CheckOp{}, denc.Squid),
		Entry("a check whose fields hold a NUL and bytes outside UTF-8",
			otp.CheckOp{ID: "d\x00v", Val: "12\x00\xff", Token: "\xfe"}, denc.Tentacle),
	)

	Describe("the record", func() {
		var (
			rd *radosclient.ReadOp
			l  *otp.CheckLookup
		)
		BeforeEach(func() {
			rd = radosclient.NewReadOp()
			l = otp.LookupCheck(rd, checkReq(), denc.Squid)
		})

		reply := func(b []byte, rval int32) {
			GinkgoHelper()
			execStep(rd.Steps(), "otp_get_result").Result.Set(b, rval)
		}

		It("sends the golden check op", func() {
			Expect(execStep(rd.Steps(), "otp_get_result").In).To(Equal(unhex(checkReqHex)))
		})

		It("answers success with the check", func() {
			reply(unhex(replyHex("01")), 0)
			Expect(l.Record()).To(Equal(recorded(otp.CheckSuccess)))
		})

		DescribeTable("answers any other result as check_mfa's EACCES, with the check",
			func(res otp.CheckResult, b string) {
				reply(unhex(replyHex(b)), 0)
				got, err := l.Record()
				Expect(err).To(MatchError(otp.ErrCheckRefused))
				Expect(got).To(Equal(recorded(res)))
			},
			Entry("fail, the code did not verify", otp.CheckFail, "02"),
			Entry("unknown, no check under the token, as when the class declined to record one", otp.CheckUnknown, "00"),
			Entry("a byte no enumerator names", otp.CheckResult(0xff), "ff"),
		)

		DescribeTable("keeps the method's error, which is no refusal",
			func(errno syscall.Errno, want error) {
				reply(nil, -int32(errno))
				got, err := l.Record()
				Expect(err).To(MatchError(want))
				Expect(err).NotTo(MatchError(otp.ErrCheckRefused))
				Expect(got).To(BeZero())
			},
			Entry("ENOENT, no device with the id", syscall.ENOENT, radosclient.ErrNotFound),
			Entry("EINVAL, a request the class could not decode", syscall.EINVAL, radosclient.ErrInvalid),
		)

		DescribeTable("refuses a reply that does not decode, never as a success",
			func(b string, want error) {
				reply(unhex(b), 0)
				got, err := l.Record()
				Expect(err).To(MatchError(want))
				Expect(err).NotTo(MatchError(otp.ErrCheckRefused))
				Expect(got).To(BeZero())
			},
			Entry("an empty reply", "", denc.ErrShortBuffer),
			Entry("a reply whose compat is newer than version 1", "0202"+replyHex("01")[4:], denc.ErrIncompatible),
			// The check's length, 28, ends its frame before the result byte, a
			// success the outer frame still holds.
			Entry("a reply whose success byte lies past its check's frame",
				replyHex("01")[:16]+"1c"+replyHex("01")[18:], denc.ErrOverread),
		)

		It("ignores bytes past the reply, as OTP::check does", func() {
			reply(unhex(replyHex("01")+"aa"), 0)
			Expect(l.Record()).To(Equal(recorded(otp.CheckSuccess)))
		})

		It("reports a read before the op ran, never as a success", func() {
			got, err := l.Record()
			Expect(err).To(MatchError(radosclient.ErrIncomplete))
			Expect(got).To(BeZero())
		})
	})
})

var _ = Describe("CheckOp redaction", func() {
	// secretOp's code is text no other field holds, so output that names it
	// in none of the forms fmt prints a string is shown not to leak it.
	secretOp := func() otp.CheckOp {
		return otp.CheckOp{ID: "dev1", Val: "SECRETCODE", Token: "AbCdEfGh01234567"}
	}
	leaks := func(s string) bool {
		return strings.Contains(s, "SECR") ||
			strings.Contains(strings.ToLower(s), "53454352") || // as %x and %X print it
			strings.Contains(strings.ToLower(s), "53 45 43 52") // as % x prints it
	}

	// holders are the ways fmt reaches a check through its methods.
	holders := func(o otp.CheckOp) []any {
		return []any{o, &o, []otp.CheckOp{o}, map[string]otp.CheckOp{"k": o}, struct{ Op otp.CheckOp }{o}}
	}

	DescribeTable("prints no code under any verb",
		func(format string) {
			blank := secretOp()
			blank.Val = ""
			secret, other := holders(secretOp()), holders(blank)
			for n := range secret {
				out := fmt.Sprintf(format, secret[n])
				Expect(leaks(out)).To(BeFalse(), "%s printed %T as %q", format, secret[n], out)
				// Output that does not depend on the code shows it in no form.
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

	It("prints the id and token under a string verb, with its width and precision, the Go form under %#v, and reports any other verb", func() {
		o := secretOp()
		const plain = `{ID:"dev1" Token:"AbCdEfGh01234567"}`
		Expect(fmt.Sprintf("%v|%.5s|%40s", o, o, o)).To(Equal(plain + "|" + plain[:5] + "|" + fmt.Sprintf("%40s", plain)))
		Expect(fmt.Sprintf("%#v", o)).To(Equal(`otp.CheckOp{ID:"dev1", Token:"AbCdEfGh01234567"}`))
		Expect(fmt.Sprintf("%d", o)).To(Equal("%!d(otp.CheckOp=" + plain + ")"))
	})

	It("logs a check through slog without its code", func() {
		var buf bytes.Buffer
		for _, h := range []slog.Handler{slog.NewJSONHandler(&buf, nil), slog.NewTextHandler(&buf, nil)} {
			slog.New(h).Info("check", slog.Any("check", secretOp()))
		}
		Expect(buf.String()).To(ContainSubstring("AbCdEfGh01234567"))
		Expect(leaks(buf.String())).To(BeFalse(), buf.String())
	})

	It("writes JSON with the id and token alone", func() {
		o := secretOp()
		for _, v := range []any{o, &o} {
			b, err := json.Marshal(v)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(b)).To(Equal(`{"id":"dev1","token":"AbCdEfGh01234567"}`), "%T", v)
		}
	})

	// slog's JSON handler calls LogValue only on the value logged itself, and
	// marshals what holds a check with encoding/json.
	DescribeTable("hides the code from slog's JSON handler inside another value",
		func(held func(otp.CheckOp) any) {
			var buf bytes.Buffer
			slog.New(slog.NewJSONHandler(&buf, nil)).Info("check", slog.Any("held", held(secretOp())))
			Expect(buf.String()).To(ContainSubstring("AbCdEfGh01234567"))
			Expect(leaks(buf.String())).To(BeFalse(), buf.String())
		},
		Entry("a struct field", func(o otp.CheckOp) any { return struct{ Op otp.CheckOp }{o} }),
		Entry("a pointer field", func(o otp.CheckOp) any { return struct{ Op *otp.CheckOp }{&o} }),
		Entry("a slice", func(o otp.CheckOp) any { return []otp.CheckOp{o} }),
		Entry("a map", func(o otp.CheckOp) any { return map[string]otp.CheckOp{"k": o} }),
		Entry("a map of pointers", func(o otp.CheckOp) any { return map[string]*otp.CheckOp{"k": &o} }),
	)

	// The paths CheckOp's godoc tells callers to keep checks off, where the
	// code is written without a method of CheckOp's being called.
	It("cannot keep the code out of an unexported field, a %p of a value, a %w, or encoding/xml", func() {
		type holder struct{ op otp.CheckOp }
		o := secretOp()
		Expect(fmt.Sprintf("%v", holder{op: o})).To(ContainSubstring("SECRETCODE"))
		var buf bytes.Buffer
		slog.New(slog.NewTextHandler(&buf, nil)).Info("check", slog.Any("held", holder{op: o}))
		Expect(buf.String()).To(ContainSubstring("SECRETCODE"))
		// Held in variables, so vet's printf check, which reports a %w in
		// Sprintf, lets the spec build.
		pointerVerb, wrapVerb := "%p", "%w"
		Expect(fmt.Sprintf(pointerVerb, o)).To(ContainSubstring("SECRETCODE"))
		Expect(fmt.Sprintf(wrapVerb, &o)).To(ContainSubstring("SECRETCODE"))
		b, err := xml.Marshal(o)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(ContainSubstring("SECRETCODE"))
	})
})
