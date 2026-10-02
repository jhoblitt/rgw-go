package policy_test

import (
	"fmt"
	"math"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

// leastSubnormal times 10^-324 is exactly 2^-1074, the least subnormal
// double.
const leastSubnormal = "4.940656458412465441765687928682213723650598026143247644255856825006755072702087518652998363616359923797965646954457177309266567103559397963987747960107818781263007131903114045278458171678489821036887186360569987307230500063874091535649843873124733972731696151400317153853980741262385655911710266585566867681870395603106249319452715914924553293054565444011274801297099995419319894090804165633245247571478690147267801593552386115501348035264934720193790268107107491703332226844753335720832431936092382893458368060106011506169809753078342277318329247904982524730776375927247874656084778203734469699533647017972677717585125660551199131504891101451037862738167250955837389733598993664809941164205702637090279242767544565229087538682506419718265533447265625"

// envOf builds an Env from key, value pairs, added in order.
func envOf(kv ...string) policy.Env {
	var e policy.Env
	for i := 0; i+1 < len(kv); i += 2 {
		e.Add(kv[i], kv[i+1])
	}
	return e
}

// fillers is n key, value pairs under keys of their own.
func fillers(n int) []string {
	kv := make([]string, 0, 2*n)
	for i := range n {
		kv = append(kv, fmt.Sprintf("filler%02d", i), "v")
	}
	return kv
}

func cond(op policy.Operator, key string, vals ...string) policy.Condition {
	return policy.Condition{Op: op, Key: key, Values: vals}
}

func ifExists(c policy.Condition) policy.Condition {
	c.IfExists = true
	return c
}

func runtime(c policy.Condition) policy.Condition {
	c.IsRuntime = true
	return c
}

// unixNanos is the instant n nanoseconds after the epoch.
func unixNanos(n uint64) time.Time {
	return time.Unix(int64(n/1e9), int64(n%1e9)).UTC()
}

var _ = Describe("Operator", func() {
	var names []string

	BeforeEach(func() {
		// rgw_iam_policy_keywords.gperf's cond_op rows, in TokenID order.
		names = []string{
			"StringEquals", "StringNotEquals", "StringEqualsIgnoreCase", "StringNotEqualsIgnoreCase",
			"StringLike", "StringNotLike", "ForAllValues:StringEquals", "ForAnyValue:StringEquals",
			"ForAllValues:StringLike", "ForAnyValue:StringLike", "ForAllValues:StringEqualsIgnoreCase",
			"ForAnyValue:StringEqualsIgnoreCase", "NumericEquals", "NumericNotEquals", "NumericLessThan",
			"NumericLessThanEquals", "NumericGreaterThan", "NumericGreaterThanEquals", "DateEquals",
			"DateNotEquals", "DateLessThan", "DateLessThanEquals", "DateGreaterThan",
			"DateGreaterThanEquals", "Bool", "BinaryEquals", "IpAddress", "NotIpAddress", "ArnEquals",
			"ArnNotEquals", "ArnLike", "ArnNotLike", "Null",
		}
	})

	It("names each operator as the keyword table spells it, in TokenID order", func() {
		for i, name := range names {
			op := policy.Operator(i)
			Expect(op.String()).To(Equal(name), "operator %d", i)
			parsed, ok := policy.ParseOperator(name)
			Expect(ok).To(BeTrue(), "ParseOperator(%q)", name)
			Expect(parsed).To(Equal(op), "ParseOperator(%q)", name)
		}
		Expect(policy.OpStringEquals).To(Equal(policy.Operator(0)))
		Expect(policy.OpNull).To(Equal(policy.Operator(len(names) - 1)))
	})

	It("renders no name for a value past the table", func() {
		Expect(policy.Operator(len(names)).String()).To(BeEmpty())
	})

	DescribeTable("parses only the exact spelling",
		func(s string) {
			op, ok := policy.ParseOperator(s)
			Expect(ok).To(BeFalse(), "ParseOperator(%q) gave %v", s, op)
		},
		Entry("an empty name", ""),
		Entry("another case", "stringequals"),
		Entry("a qualifier without its colon", "ForAllValuesStringEquals"),
		Entry("the IfExists suffix, which the parser strips first", "StringEqualsIfExists"),
		Entry("a statement keyword", "Condition"),
		Entry("a qualifier on its own", "ForAllValues:"),
	)
})

var _ = Describe("MaskedIP", func() {
	parsed := func(s string) policy.MaskedIP {
		m, ok := policy.ParseMaskedIP(s)
		Expect(ok).To(BeTrue(), "ParseMaskedIP(%q)", s)
		return m
	}

	// IPPolicyTest (src/test/rgw/test_rgw_iam_policy.cc at v19.2.6 and v20.2.4).
	It("parses an IPv4 range into the low 32 bits", func() {
		Expect(parsed("192.168.1.0/24")).To(Equal(policy.MaskedIP{
			Addr: [16]byte{12: 192, 13: 168, 14: 1}, Prefix: 24,
		}))
	})

	It("gives an IPv4 address with no prefix a prefix of 32", func() {
		Expect(parsed("192.168.1.1")).To(Equal(policy.MaskedIP{
			Addr: [16]byte{12: 192, 13: 168, 14: 1, 15: 1}, Prefix: 32,
		}))
	})

	It("parses an IPv6 address into all 128 bits", func() {
		Expect(parsed("2001:0db8:85a3:0000:0000:8a2e:0370:7334")).To(Equal(policy.MaskedIP{
			V6: true,
			Addr: [16]byte{
				0x20, 0x01, 0x0d, 0xb8, 0x85, 0xa3, 0x00, 0x00,
				0x00, 0x00, 0x8a, 0x2e, 0x03, 0x70, 0x73, 0x34,
			},
			Prefix: 128,
		}))
	})

	DescribeTable("parses and prints as as_network and operator<< do",
		func(s string, ok bool, printed string) {
			m, got := policy.ParseMaskedIP(s)
			Expect(got).To(Equal(ok), "ParseMaskedIP(%q) gave %v", s, m)
			if ok {
				Expect(m.String()).To(Equal(printed), "ParseMaskedIP(%q)", s)
			}
		},
		Entry("an IPv4 range", "192.168.1.0/24", true, "192.168.1.0/24"),
		Entry("an IPv4 address", "192.168.1.1", true, "192.168.1.1/32"),
		Entry("an IPv6 range", "2001:db8:85a3:0:0:8a2e:370:7330/124", true, "2001:db8:85a3:0:0:8a2e:370:7330/124"),
		Entry("an IPv6 address with leading zeros", "2001:0db8:85a3:0000:0000:8a2e:0370:7334", true, "2001:db8:85a3:0:0:8a2e:370:7334/128"),
		Entry("the IPv6 loopback, printed uncompressed", "::1", true, "0:0:0:0:0:0:0:1/128"),
		Entry("an empty string", "", false, ""),
		Entry("an IPv4 prefix over 32", "192.168.1.1/33", false, ""),
		Entry("an IPv6 prefix over 128", "2001:db8:85a3:0:0:8a2e:370:7334/129", false, ""),
		Entry("a colon making an IPv4 address IPv6", "192.168.1.1:", false, ""),
		Entry("an octet over 255", "1.2.3.10000", false, ""),
		Entry("an empty prefix, strtoul's 0", "192.168.1.0/", true, "192.168.1.0/0"),
		Entry("a signed prefix", "1.2.3.4/+24", true, "1.2.3.4/24"),
		Entry("whitespace before the prefix", "1.2.3.4/ 24", true, "1.2.3.4/24"),
		Entry("a negative zero prefix", "1.2.3.4/-0", true, "1.2.3.4/0"),
		Entry("a prefix truncated to 32 bits", "1.2.3.4/4294967320", true, "1.2.3.4/24"),
		Entry("a negated prefix truncated to 32 bits", "1.2.3.4/-4294967272", true, "1.2.3.4/24"),
		Entry("a prefix past unsigned long", "1.2.3.4/99999999999999999999999", false, ""),
		Entry("text after the prefix", "1.2.3.4/24x", false, ""),
		Entry("a second slash", "1.2.3.4/32/1", false, ""),
		Entry("a NUL ending the prefix", "1.2.3.4/24\x00x", true, "1.2.3.4/24"),
		Entry("a NUL ending the address", "1.2.3.4\x00x", true, "1.2.3.4/32"),
		Entry("a colon past a NUL making the address IPv6", "1.2.3.4\x00:", false, ""),
		Entry("an octet with a leading zero", "01.2.3.4", false, ""),
		Entry("three octets", "1.2.3", false, ""),
		Entry("five octets", "1.2.3.4.5", false, ""),
		Entry("an octet of 256", "256.1.1.1", false, ""),
		Entry("trailing whitespace", "1.2.3.4 ", false, ""),
		Entry("an IPv4-mapped address", "::ffff:1.2.3.4", true, "0:0:0:0:0:ffff:102:304/128"),
		Entry("the unspecified address", "::", true, "0:0:0:0:0:0:0:0/128"),
		Entry("a trailing ::", "1:2:3:4:5:6:7::", true, "1:2:3:4:5:6:7:0/128"),
		Entry("a :: standing for no group", "1:2:3:4:5:6:7::8", false, ""),
		Entry("a zone", "fe80::1%eth0", false, ""),
		Entry("an embedded IPv4 address", "1:2:3:4:5:6:1.2.3.4", true, "1:2:3:4:5:6:102:304/128"),
		Entry("an embedded IPv4 address with no room", "1:2:3:4:5:6:7:1.2.3.4", false, ""),
		Entry("an embedded IPv4 octet with a leading zero", "::1.2.3.04", false, ""),
		Entry("a single leading colon", ":1::", false, ""),
		Entry("two ::", "1::2::3", false, ""),
		Entry("five hex digits", "12345::", false, ""),
		Entry("upper-case hex", "ABCD::Ef", true, "abcd:0:0:0:0:0:0:ef/128"),
		Entry("nine groups", "1:2:3:4:5:6:7:8:9", false, ""),
		Entry("a trailing single colon", "1::2:", false, ""),
		Entry("an IPv6 prefix of 0", "::/0", true, "0:0:0:0:0:0:0:0/0"),
	)

	DescribeTable("compares after shifting both by the wider host part",
		func(a, b string, want bool) {
			Expect(parsed(a).Equal(parsed(b))).To(Equal(want), "%s == %s", a, b)
			Expect(parsed(b).Equal(parsed(a))).To(Equal(want), "%s == %s", b, a)
		},
		Entry("an IPv4 address in a range", "192.168.1.0/24", "192.168.1.1/32", true),
		Entry("an IPv6 address in a range", "2001:db8:85a3:0:0:8a2e:370:7330/124", "2001:db8:85a3:0:0:8a2e:370:7334", true),
		Entry("v4 and v6 holding the same 128 bits", "::1", "0.0.0.1", true),
		Entry("v4 and v6 holding different bits", "::1", "0.0.0.2", false),
		Entry("disjoint ranges", "10.0.0.0/8", "11.0.0.0/8", false),
		Entry("an IPv4 /0", "0.0.0.0/0", "255.255.255.255", true),
		Entry("an IPv6 /0", "::/0", "ffff::", true),
		Entry("an IPv4 range against an IPv4-mapped address", "192.168.1.0/24", "::ffff:192.168.1.7", false),
		Entry("an IPv4 range against an IPv4-compatible address", "192.168.1.0/24", "::192.168.1.7", true),
		Entry("an IPv4 /0 against high IPv6 bits", "1.2.3.4/0", "ffff::", false),
	)
})

var _ = Describe("typed conversions", func() {
	DescribeTable("AsNumber is std::stod over the whole string",
		func(s string, ok bool, want float64) {
			got, gotOK := policy.AsNumber(s)
			Expect(gotOK).To(Equal(ok), "AsNumber(%q) gave %v", s, got)
			switch {
			case !ok:
			case math.IsNaN(want):
				Expect(math.IsNaN(got)).To(BeTrue(), "AsNumber(%q) gave %v", s, got)
			default:
				Expect(got).To(Equal(want), "AsNumber(%q)", s)
			}
		},
		Entry("an integer", "5", true, 5.0),
		Entry("a signed fraction", "-3.5", true, -3.5),
		Entry("a plus sign", "+2", true, 2.0),
		Entry("leading C-locale whitespace", " \t\n\v\f\r7", true, 7.0),
		Entry("trailing whitespace", "7 ", false, 0.0),
		Entry("an exponent", "1E-2", true, 0.01),
		Entry("no integer digits", ".5", true, 0.5),
		Entry("no fraction digits", "5.", true, 5.0),
		Entry("a hex integer", "0x10", true, 16.0),
		Entry("a hex float", "0X1.8p1", true, 3.0),
		Entry("a hex float with no exponent", "0x1.8", true, 1.5),
		Entry("a hex float with no integer digits", "0x.8p1", true, 1.0),
		Entry("a hex prefix with no digits", "0x", false, 0.0),
		Entry("a hex point with no digits", "0x.p1", false, 0.0),
		Entry("an exponent with no digits", "1e+", false, 0.0),
		Entry("a second point", "1.2.3", false, 0.0),
		Entry("infinity", "-Infinity", true, math.Inf(-1)),
		Entry("a cut-short infinity", "infini", false, 0.0),
		Entry("NaN with a payload", "-NaN(abc_1)", true, math.NaN()),
		Entry("NaN with an unclosed payload", "nan(", false, 0.0),
		Entry("an overflow, ERANGE", "1e309", false, 0.0),
		Entry("an underflow to zero, ERANGE", "1e-400", false, 0.0),
		Entry("an inexact subnormal, ERANGE", "4.9e-324", false, 0.0),
		Entry("an exact subnormal", "0x1p-1074", true, 0x1p-1074),
		Entry("the least subnormal written out exactly", leastSubnormal+"e-324", true, 0x1p-1074),
		Entry("the least subnormal past 800 significant digits",
			leastSubnormal+strings.Repeat("0", 1000)+"e-324", true, 0x1p-1074),
		Entry("a subnormal inexact past 800 significant digits, ERANGE",
			leastSubnormal+strings.Repeat("0", 1000)+"1e-324", false, 0.0),
		Entry("a subnormal after 1000 leading zeros, ERANGE", "0."+strings.Repeat("0", 1000)+"1e+679", false, 0.0),
		Entry("a value rounding up to the least normal from tiny, ERANGE", "0x1.fffffffffffffp-1023", false, 0.0),
		Entry("a value rounding up to the least normal from normal precision", "0x1.fffffffffffffcp-1023", true, 0x1p-1022),
		Entry("an exact zero with a tiny exponent", "0e-400", true, 0.0),
		Entry("a huge exponent", "1e99999999999", false, 0.0),
		Entry("a huge negative exponent", "1e-99999999999", false, 0.0),
		Entry("an empty string", "", false, 0.0),
		Entry("whitespace alone", " ", false, 0.0),
		Entry("a word", "abc", false, 0.0),
		Entry("an underscore", "1_000", false, 0.0),
		Entry("an underscore in a hex float", "0x1_0p0", false, 0.0),
		Entry("a decimal comma", "1,5", false, 0.0),
		Entry("a NUL", "1\x002", false, 0.0),
	)

	DescribeTable("AsDate is epoch seconds for a whole-string number, else from_iso_8601",
		func(s string, ok bool, want time.Time) {
			got, gotOK := policy.AsDate(s)
			Expect(gotOK).To(Equal(ok), "AsDate(%q) gave %v", s, got)
			if ok {
				Expect(got).To(BeTemporally("==", want), "AsDate(%q)", s)
			}
		},
		Entry("zero", "0", true, time.Unix(0, 0)),
		Entry("epoch seconds", "1700000000", true, time.Unix(1700000000, 0)),
		Entry("fractional epoch seconds", "1700000000.5", true, time.Unix(1700000000, 5e8)),
		Entry("hex epoch seconds", "0x10", true, time.Unix(16, 0)),
		Entry("a four-digit number, which is seconds and not a year", "2024", true, time.Unix(2024, 0)),
		Entry("a four-digit number after whitespace", " 2024", true, time.Unix(2024, 0)),
		Entry("seconds past 2^63 nanoseconds, wrapping", "1e10", true, unixNanos(10000000000000000000)),
		Entry("seconds past 2^64 nanoseconds, wrapping", "1e19", true, unixNanos(4477988020393345024)),
		Entry("2^64 seconds, which converts to 0", "18446744073709551616", true, unixNanos(0)),
		Entry("negative seconds, through x86-64's conversion", "-1", true, unixNanos(9223372035854775808)),
		Entry("negative fractional seconds", "-5.5", true, unixNanos(9223372031854775808)),
		Entry("NaN", "nan", true, unixNanos(9223372036854775808)),
		Entry("infinity", "inf", true, unixNanos(0)),
		Entry("negative infinity", "-inf", true, unixNanos(9223372036854775808)),
		Entry("an overflowing number", "1e400", false, time.Time{}),
		Entry("a year and month", "2024-01", true, time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)),
		Entry("a date", "2024-03-04", true, time.Date(2024, 3, 4, 0, 0, 0, 0, time.UTC)),
		Entry("an hour", "2024-03-04T05Z", true, time.Date(2024, 3, 4, 5, 0, 0, 0, time.UTC)),
		Entry("minutes", "2024-03-04T05:06Z", true, time.Date(2024, 3, 4, 5, 6, 0, 0, time.UTC)),
		Entry("seconds", "2024-03-04T05:06:07Z", true, time.Date(2024, 3, 4, 5, 6, 7, 0, time.UTC)),
		Entry("one fraction digit", "2024-01-01T00:00:00.5Z", true, time.Date(2024, 1, 1, 0, 0, 0, 5e8, time.UTC)),
		Entry("nine fraction digits", "2024-01-01T00:00:00.123456789Z", true, time.Date(2024, 1, 1, 0, 0, 0, 123456789, time.UTC)),
		Entry("ten fraction digits", "2024-01-01T00:00:00.1234567891Z", false, time.Time{}),
		Entry("no Z", "2024-01-01T00:00:00", false, time.Time{}),
		Entry("whitespace after the Z", "2024-01-01T00:00:00Z ", false, time.Time{}),
		Entry("a Z after the date", "2024-01-01Z", false, time.Time{}),
		Entry("an offset", "2024-01-01T00:00:00+00:00", false, time.Time{}),
		Entry("a year before 1970", "1969-12-31", false, time.Time{}),
		Entry("a time one second before the epoch, timegm's -1", "1970-00-31T23:59:59Z", false, time.Time{}),
		Entry("a day before the epoch, wrapping", "1970-01-00", true, unixNanos(18446657673709551616)),
		Entry("month 13", "2024-13-01", true, time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)),
		Entry("month 0", "2024-00-15", true, time.Date(2023, 12, 15, 0, 0, 0, 0, time.UTC)),
		Entry("February 30", "2024-02-30", true, time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)),
		Entry("hour 25, minute 61 and second 61", "2024-01-01T25:61:61Z", true, time.Date(2024, 1, 2, 2, 2, 1, 0, time.UTC)),
		Entry("a date past 2^64 nanoseconds, wrapping", "9999-12-31", true, unixNanos(13594541441775828992)),
		Entry("a date past 2554, wrapping before today", "2600-01-01", true, unixNanos(1434155126290448384)),
		Entry("a one-digit month", "2024-1-01", false, time.Time{}),
		Entry("trailing whitespace", "2024 ", false, time.Time{}),
		Entry("epoch seconds with trailing whitespace", "1700000000 ", false, time.Time{}),
		Entry("a word", "abc", false, time.Time{}),
		Entry("an empty string", "", false, time.Time{}),
	)

	DescribeTable("AsBool is false for empty, false or a whole-string zero or NaN",
		func(s string, want bool) {
			Expect(policy.AsBool(s)).To(Equal(want), "AsBool(%q)", s)
		},
		Entry("an empty string", "", false),
		Entry("false", "false", false),
		Entry("false in any case", "fAlSe", false),
		Entry("true", "true", true),
		Entry("zero", "0", false),
		Entry("one", "1", true),
		Entry("a zero fraction", "0.0", false),
		Entry("negative zero", "-0", false),
		Entry("hex zero", "0x0", false),
		Entry("zero after whitespace", " 0", false),
		Entry("NaN", "nan", false),
		Entry("two", "2", true),
		Entry("an underflowing number, which std::stod refuses", "1e-400", true),
		Entry("zero with trailing whitespace", "0 ", true),
		Entry("zero before a NUL", "0\x00", true),
		Entry("false with trailing whitespace", "false ", true),
		Entry("no", "no", true),
		Entry("infinity", "inf", true),
	)

	DescribeTable("AsBinary is ceph_unarmor",
		func(s string, ok bool, want []byte) {
			got, gotOK := policy.AsBinary(s)
			Expect(gotOK).To(Equal(ok), "AsBinary(%q) gave %q", s, got)
			if ok {
				Expect(got).To(BeEquivalentTo(want), "AsBinary(%q)", s)
			}
		},
		Entry("padded base64", "aGk=", true, []byte("hi")),
		Entry("unpadded base64", "aGk", false, nil),
		Entry("unpadded, a whole group short", "YWJjZA", false, nil),
		Entry("a whole group", "YWJj", true, []byte("abc")),
		Entry("an empty string", "", true, []byte{}),
		Entry("text after the padding, ignored", "aGk=garbage!", true, []byte("hi")),
		Entry("a second encoded value after the padding, ignored", "aGk=aGk=", true, []byte("hi")),
		Entry("a third byte after =", "aG=x", true, []byte("h")),
		Entry("= in the second place", "a===", true, []byte("h")),
		Entry("= in the first place", "====", true, []byte{0}),
		Entry("the URL-safe alphabet", "-_-_", true, []byte{0xfb, 0xff, 0xbf}),
		Entry("the standard alphabet", "+/+/", true, []byte{0xfb, 0xff, 0xbf}),
		Entry("a newline between groups", "YWJj\nZA==", true, []byte("abcd")),
		Entry("a newline before the first group", "\naGk=", true, []byte("hi")),
		Entry("a newline inside a group", "aG\nk=", false, nil),
		Entry("a space between groups", "YWJj ZA==", false, nil),
		Entry("non-zero bits past the last byte", "aGl=", true, []byte("hi")),
		Entry("a NUL", "Y\x00Jj", false, nil),
		Entry("newlines alone", "\n\n", true, []byte{}),
	)
})

var _ = Describe("Condition", func() {
	var squid, tentacle policy.Semantics

	BeforeEach(func() {
		squid = policy.SemanticsFor(denc.Squid)
		tentacle = policy.SemanticsFor(denc.Tentacle)
	})

	DescribeTable("evaluates as each release's Condition::eval does",
		func(c policy.Condition, kv []string, wantSquid, wantTentacle bool) {
			env := envOf(kv...)
			Expect(c.Eval(env, squid)).To(Equal(wantSquid), "squid: %+v", c)
			Expect(c.Eval(env, tentacle)).To(Equal(wantTentacle), "tentacle: %+v", c)
		},
		// The absent key.
		Entry("absent key, StringEquals", cond(policy.OpStringEquals, "k", "x"), nil, false, false),
		Entry("absent key, IfExists", ifExists(cond(policy.OpStringEquals, "k", "x")), nil, true, true),
		Entry("absent key, ForAllValues:StringEquals", cond(policy.OpForAllValuesStringEquals, "k", "x"), nil, true, true),
		Entry("absent key, ForAllValues:StringLike", cond(policy.OpForAllValuesStringLike, "k", "x"), nil, true, true),
		Entry("absent key, ForAllValues:StringEqualsIgnoreCase", cond(policy.OpForAllValuesStringEqualsIgnoreCase, "k", "x"), nil, true, true),
		Entry("absent key, ForAnyValue:StringEquals", cond(policy.OpForAnyValueStringEquals, "k", "x"), nil, false, false),
		Entry("absent key, a Not operator", cond(policy.OpStringNotEquals, "k", "x"), nil, false, false),
		Entry("present key, IfExists", ifExists(cond(policy.OpStringEquals, "k", "x")), []string{"k", "y"}, false, false),

		// Null.
		Entry("Null, present", cond(policy.OpNull, "k", "true"), []string{"k", "a"}, false, false),
		Entry("Null, absent", cond(policy.OpNull, "k", "true"), nil, true, true),
		Entry("Null, absent, values false", cond(policy.OpNull, "k", "false"), nil, true, false),
		Entry("Null, present, values false", cond(policy.OpNull, "k", "false"), []string{"k", "a"}, false, true),
		Entry("Null, absent, values converted by as_bool", cond(policy.OpNull, "k", "1"), nil, true, true),
		Entry("Null, absent, no values", cond(policy.OpNull, "k"), nil, true, false),
		Entry("Null, absent, IfExists", ifExists(cond(policy.OpNull, "k", "false")), nil, true, false),

		// The string operators.
		Entry("StringEquals", cond(policy.OpStringEquals, "k", "a", "b"), []string{"k", "b"}, true, true),
		Entry("StringEquals, no value equal", cond(policy.OpStringEquals, "k", "a", "b"), []string{"k", "c"}, false, false),
		Entry("StringNotEquals, one of two env values differs", cond(policy.OpStringNotEquals, "k", "a"), []string{"k", "a", "k", "b"}, true, false),
		Entry("StringNotEquals, all differ", cond(policy.OpStringNotEquals, "k", "a"), []string{"k", "c"}, true, true),
		Entry("StringNotEquals, equal", cond(policy.OpStringNotEquals, "k", "a"), []string{"k", "a"}, false, false),
		Entry("StringNotEquals, one of three condition values equal",
			cond(policy.OpStringNotEquals, "aws:UserName", "alice", "bob", "charlie"), []string{"aws:UserName", "bob"}, true, false),
		Entry("StringEqualsIgnoreCase", cond(policy.OpStringEqualsIgnoreCase, "k", "a"), []string{"k", "A"}, true, true),
		Entry("StringEqualsIgnoreCase folds ASCII only", cond(policy.OpStringEqualsIgnoreCase, "k", "k", "é"), []string{"k", "K", "k", "É"}, false, false),
		Entry("ForAnyValue:StringEqualsIgnoreCase", cond(policy.OpForAnyValueStringEqualsIgnoreCase, "k", "ABC"), []string{"k", "x", "k", "abc"}, true, true),
		Entry("StringNotEqualsIgnoreCase, equal but for case", cond(policy.OpStringNotEqualsIgnoreCase, "k", "a", "b"), []string{"k", "B"}, true, false),
		Entry("StringLike", cond(policy.OpStringLike, "k", "a*"), []string{"k", "abc"}, true, true),
		Entry("StringLike is case-sensitive", cond(policy.OpStringLike, "k", "a*"), []string{"k", "ABC"}, false, false),
		Entry("ForAnyValue:StringLike", cond(policy.OpForAnyValueStringLike, "k", "x?"), []string{"k", "abc", "k", "xy"}, true, true),
		Entry("StringNotLike, one pattern matches",
			cond(policy.OpStringNotLike, "s3:prefix", "user/*", "admin/*", "temp/*"), []string{"s3:prefix", "admin/config.txt"}, true, false),
		Entry("StringNotLike, no pattern matches",
			cond(policy.OpStringNotLike, "s3:prefix", "user/*", "admin/*", "temp/*"), []string{"s3:prefix", "public/document.pdf"}, true, true),
		Entry("ForAllValues:StringEquals, every env value equal to some value",
			cond(policy.OpForAllValuesStringEquals, "k", "a", "b"), []string{"k", "b", "k", "a"}, true, true),
		Entry("ForAllValues:StringLike, one env value unmatched", cond(policy.OpForAllValuesStringLike, "k", "a*"), []string{"k", "ab", "k", "zz"}, false, false),
		Entry("ForAllValues:StringEqualsIgnoreCase", cond(policy.OpForAllValuesStringEqualsIgnoreCase, "k", "a", "b"), []string{"k", "A", "k", "B"}, true, true),

		// The numeric operators.
		Entry("NumericEquals", cond(policy.OpNumericEquals, "k", "x", "5.0"), []string{"k", "5"}, true, true),
		Entry("NumericEquals, unconvertible env", cond(policy.OpNumericEquals, "k", "5"), []string{"k", "x"}, false, false),
		Entry("NumericNotEquals, one value equal one not", cond(policy.OpNumericNotEquals, "k", "5", "6"), []string{"k", "5"}, true, false),
		Entry("NumericNotEquals, no value convertible", cond(policy.OpNumericNotEquals, "k", "x"), []string{"k", "5"}, false, true),
		Entry("NumericNotEquals, unconvertible env", cond(policy.OpNumericNotEquals, "k", "5"), []string{"k", "x"}, false, false),
		Entry("NumericNotEquals, NaN", cond(policy.OpNumericNotEquals, "k", "nan"), []string{"k", "nan"}, true, true),
		Entry("NumericLessThan", cond(policy.OpNumericLessThan, "k", "10"), []string{"k", "5"}, true, true),
		Entry("NumericLessThan, the env value on the left", cond(policy.OpNumericLessThan, "k", "5"), []string{"k", "10"}, false, false),
		Entry("NumericLessThanEquals", cond(policy.OpNumericLessThanEquals, "k", "5"), []string{"k", "5"}, true, true),
		Entry("NumericGreaterThan", cond(policy.OpNumericGreaterThan, "k", "5"), []string{"k", "5"}, false, false),
		Entry("NumericGreaterThanEquals", cond(policy.OpNumericGreaterThanEquals, "k", "5"), []string{"k", "0x5"}, true, true),

		// The date operators.
		Entry("DateLessThan, epoch vs ISO", cond(policy.OpDateLessThan, "k", "2024-01-01T00:00:00Z"), []string{"k", "1700000000"}, true, true),
		Entry("DateGreaterThan", cond(policy.OpDateGreaterThan, "k", "1700000000"), []string{"k", "2024-01-01T00:00:00Z"}, true, true),
		Entry("DateEquals, the same instant in both forms", cond(policy.OpDateEquals, "k", "1704067200"), []string{"k", "2024-01-01"}, true, true),
		Entry("DateLessThanEquals", cond(policy.OpDateLessThanEquals, "k", "2024-01-01T00:00:00Z"), []string{"k", "2024-01-01"}, true, true),
		Entry("DateGreaterThanEquals, a wrapped date", cond(policy.OpDateGreaterThanEquals, "k", "2024-01-01"), []string{"k", "1970-01-00"}, true, true),
		Entry("DateLessThan, a date past 2554 wrapping before today",
			cond(policy.OpDateLessThan, "aws:CurrentTime", "2600-01-01T00:00:00Z"), []string{"aws:CurrentTime", "1790510400"}, false, false),
		Entry("DateNotEquals, one of three equal",
			cond(policy.OpDateNotEquals, "aws:CurrentTime", "2023-01-01T00:00:00Z", "2023-06-01T00:00:00Z", "2023-12-01T00:00:00Z"),
			[]string{"aws:CurrentTime", "2023-06-01T00:00:00Z"}, true, false),
		Entry("DateNotEquals, none equal",
			cond(policy.OpDateNotEquals, "aws:CurrentTime", "2023-01-01T00:00:00Z", "2023-06-01T00:00:00Z", "2023-12-01T00:00:00Z"),
			[]string{"aws:CurrentTime", "2024-01-01T00:00:00Z"}, true, true),

		// Bool and BinaryEquals.
		Entry("Bool", cond(policy.OpBool, "k", "true"), []string{"k", "true"}, true, true),
		Entry("Bool, number", cond(policy.OpBool, "k", "true"), []string{"k", "1"}, true, true),
		Entry("Bool, empty", cond(policy.OpBool, "k", "false"), []string{"k", ""}, true, true),
		Entry("Bool, unequal", cond(policy.OpBool, "k", "false"), []string{"k", "yes"}, false, false),
		Entry("BinaryEquals", cond(policy.OpBinaryEquals, "k", "aGk="), []string{"k", "aGk="}, true, true),
		Entry("BinaryEquals, two encodings of one value", cond(policy.OpBinaryEquals, "k", "aGl="), []string{"k", "aGk=x"}, true, true),
		Entry("BinaryEquals, unpadded", cond(policy.OpBinaryEquals, "k", "aGk"), []string{"k", "aGk"}, false, false),

		// IpAddress and NotIpAddress.
		Entry("IpAddress", cond(policy.OpIPAddress, "aws:SourceIp", "192.168.1.0/24"), []string{"aws:SourceIp", "192.168.1.2"}, true, true),
		Entry("IpAddress, out of range", cond(policy.OpIPAddress, "aws:SourceIp", "192.168.1.0/24"), []string{"aws:SourceIp", "192.168.2.2"}, false, false),
		Entry("NotIpAddress, in range",
			cond(policy.OpNotIPAddress, "aws:SourceIp", "192.168.1.1/32", "2001:0db8:85a3:0000:0000:8a2e:0370:7334"),
			[]string{"aws:SourceIp", "192.168.1.1"}, false, false),
		Entry("NotIpAddress, out of range",
			cond(policy.OpNotIPAddress, "aws:SourceIp", "192.168.1.1/32", "2001:0db8:85a3:0000:0000:8a2e:0370:7334"),
			[]string{"aws:SourceIp", "192.168.1.2"}, true, true),
		Entry("NotIpAddress, unparsable env", cond(policy.OpNotIPAddress, "aws:SourceIp", "10.0.0.0/8"), []string{"aws:SourceIp", "bogus"}, false, false),
		Entry("NotIpAddress, no value parsable", cond(policy.OpNotIPAddress, "aws:SourceIp", "bogus"), []string{"aws:SourceIp", "8.8.8.8"}, true, true),

		// The ARN operators.
		Entry("ArnLike", cond(policy.OpArnLike, "aws:SourceArn", "arn:aws:s3:::b/*"), []string{"aws:SourceArn", "arn:aws:s3:::b/k"}, true, true),
		Entry("ArnLike, malformed env", cond(policy.OpArnLike, "aws:SourceArn", "arn:aws:s3:::b/*"), []string{"aws:SourceArn", "b/k"}, false, false),
		Entry("ArnLike, case-sensitive", cond(policy.OpArnLike, "aws:SourceArn", "arn:aws:s3:::b*"), []string{"aws:SourceArn", "arn:aws:s3:::BUCKET"}, false, false),
		Entry("ArnLike, a sixth colon in the env value", cond(policy.OpArnLike, "aws:SourceArn", "arn:aws:s3:::*"), []string{"aws:SourceArn", "arn:aws:s3:::b:k"}, false, false),
		Entry("ArnLike, a pattern and env value matching without five colons", cond(policy.OpArnLike, "aws:SourceArn", "x:*"), []string{"aws:SourceArn", "x:y"}, false, false),
		Entry("ArnEquals matches wildcards as ArnLike does", cond(policy.OpArnEquals, "aws:SourceArn", "arn:aws:s3:::b*"), []string{"aws:SourceArn", "arn:aws:s3:::bucket"}, true, true),
		Entry("ArnNotEquals, one of three equal",
			cond(policy.OpArnNotEquals, "aws:SourceArn", "arn:aws:s3:::bucket1", "arn:aws:s3:::bucket2", "arn:aws:s3:::bucket3"),
			[]string{"aws:SourceArn", "arn:aws:s3:::bucket2"}, true, false),
		Entry("ArnNotLike, none matching", cond(policy.OpArnNotLike, "aws:SourceArn", "arn:aws:s3:::b*"), []string{"aws:SourceArn", "arn:aws:s3:::other"}, true, true),

		// Runtime values.
		Entry("runtime ${s3:ResourceTag/x}",
			runtime(cond(policy.OpStringEquals, "aws:PrincipalTag/x", "${s3:ResourceTag/x}")),
			[]string{"aws:PrincipalTag/x", "v", "s3:ResourceTag/x", "v"}, true, true),
		Entry("runtime, every value of the runtime key",
			runtime(cond(policy.OpStringEquals, "aws:username", "${s3:ResourceTag/owner}")),
			[]string{"aws:username", "bob", "s3:ResourceTag/owner", "alice", "s3:ResourceTag/owner", "bob"}, true, true),
		Entry("runtime, the runtime key absent",
			runtime(cond(policy.OpStringNotEquals, "aws:username", "${s3:ResourceTag/owner}")),
			[]string{"aws:username", "bob"}, false, true),
		Entry("runtime, ForAllValues against an absent runtime key",
			runtime(cond(policy.OpForAllValuesStringEquals, "aws:username", "${x}")),
			[]string{"aws:username", "bob"}, false, false),
		Entry("runtime, the last value names the key, with its first two bytes and last dropped",
			runtime(cond(policy.OpStringEquals, "k", "${unused}", "abcd")),
			[]string{"k", "v", "c", "v"}, true, true),
		Entry("runtime, a two-byte last value gives the empty key",
			runtime(cond(policy.OpStringNotEquals, "k", "${x}", "ab")),
			[]string{"k", "v", "x", "v", "ab", "v"}, false, true),
		Entry("runtime, ArnLike", runtime(cond(policy.OpArnLike, "aws:SourceArn", "${k}")),
			[]string{"aws:SourceArn", "arn:aws:s3:::b/k", "k", "arn:aws:s3:::b/*"}, true, true),
		Entry("runtime, a typed operator reading the condition's own values",
			runtime(cond(policy.OpNumericEquals, "k", "${n}")), []string{"k", "5", "n", "5"}, false, false),
	)

	// radosgw's typed operators read one value through env.find, and its
	// string and ARN operators every value through equal_range; Null reads
	// only whether the key is absent
	// (rgw_iam_policy.cc:856-1006 at v19.2.6, :875-1010 at v20.2.4).
	DescribeTable("reads a key holding several values as each release does",
		func(c policy.Condition, kv []string, wantSquid, wantTentacle bool) {
			env := envOf(kv...)
			Expect(c.Eval(env, squid)).To(Equal(wantSquid), "squid: %+v", c)
			Expect(c.Eval(env, tentacle)).To(Equal(wantTentacle), "tentacle: %+v", c)
		},
		Entry("NumericEquals, the first value", cond(policy.OpNumericEquals, "k", "1"), []string{"k", "1", "k", "2"}, false, true),
		Entry("NumericEquals, the last value", cond(policy.OpNumericEquals, "k", "2"), []string{"k", "1", "k", "2"}, true, false),
		Entry("NumericEquals, values added past 20 pairs",
			cond(policy.OpNumericEquals, "k", "2"), append(fillers(20), "k", "1", "k", "2"), true, true),
		Entry("NumericEquals, the first value past 20 pairs",
			cond(policy.OpNumericEquals, "k", "1"), append(fillers(20), "k", "1", "k", "2"), false, false),
		Entry("NumericNotEquals", cond(policy.OpNumericNotEquals, "k", "1"), []string{"k", "1", "k", "2"}, true, false),
		Entry("DateEquals", cond(policy.OpDateEquals, "k", "1700000000"), []string{"k", "1700000000", "k", "2024-01-01T00:00:00Z"}, false, true),
		Entry("Bool", cond(policy.OpBool, "k", "true"), []string{"k", "false", "k", "true"}, true, false),
		Entry("BinaryEquals", cond(policy.OpBinaryEquals, "k", "YQ=="), []string{"k", "YQ==", "k", "Yg=="}, false, true),
		Entry("IpAddress", cond(policy.OpIPAddress, "aws:SourceIp", "192.168.1.0/24"),
			[]string{"aws:SourceIp", "10.0.0.1", "aws:SourceIp", "192.168.1.1"}, true, false),
		Entry("NotIpAddress", cond(policy.OpNotIPAddress, "aws:SourceIp", "192.168.1.0/24"),
			[]string{"aws:SourceIp", "10.0.0.1", "aws:SourceIp", "192.168.1.1"}, false, true),
		Entry("StringEquals, the first value", cond(policy.OpStringEquals, "k", "1"), []string{"k", "1", "k", "2"}, true, true),
		Entry("StringEquals, the last value", cond(policy.OpStringEquals, "k", "2"), []string{"k", "1", "k", "2"}, true, true),
		Entry("ArnLike, the first value", cond(policy.OpArnLike, "aws:SourceArn", "arn:aws:s3:::b/*"),
			[]string{"aws:SourceArn", "arn:aws:s3:::b/k", "aws:SourceArn", "b/k"}, true, true),
		Entry("Null", cond(policy.OpNull, "k", "false"), []string{"k", "1", "k", "2"}, false, true),
	)

	It("takes each release rule from its own Semantics field", func() {
		env := envOf("k", "1", "k", "2")
		null := cond(policy.OpNull, "absent", "false")
		not := cond(policy.OpStringNotEquals, "k", "1")
		typed := cond(policy.OpNumericEquals, "k", "1")

		only := policy.Semantics{NullTestsValues: true}
		Expect(null.Eval(env, only)).To(BeFalse(), "Null on %+v", only)
		Expect(not.Eval(env, only)).To(BeTrue(), "StringNotEquals on %+v", only)
		Expect(typed.Eval(env, only)).To(BeFalse(), "NumericEquals on %+v", only)

		only = policy.Semantics{NotMeansNone: true}
		Expect(null.Eval(env, only)).To(BeTrue(), "Null on %+v", only)
		Expect(not.Eval(env, only)).To(BeFalse(), "StringNotEquals on %+v", only)
		Expect(typed.Eval(env, only)).To(BeFalse(), "NumericEquals on %+v", only)

		only = policy.Semantics{FindKeepsFirstValue: true}
		Expect(null.Eval(env, only)).To(BeTrue(), "Null on %+v", only)
		Expect(not.Eval(env, only)).To(BeTrue(), "StringNotEquals on %+v", only)
		Expect(typed.Eval(env, only)).To(BeTrue(), "NumericEquals on %+v", only)
	})

	It("evaluates an operator past the table as false", func() {
		c := policy.Condition{Op: policy.OpNull + 1, Key: "k", Values: []string{"a"}}
		Expect(c.Eval(envOf("k", "a"), squid)).To(BeFalse())
		Expect(c.Eval(envOf("k", "a"), tentacle)).To(BeFalse())
	})
})
