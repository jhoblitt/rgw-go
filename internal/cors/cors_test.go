package cors_test

import (
	"encoding/hex"
	"os"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cors"
	"github.com/jhoblitt/rgw-go/internal/denc"
)

// unhex reads hex digits, ignoring whitespace and each "#" comment to the end
// of its line.
func unhex(s string) []byte {
	GinkgoHelper()
	var b strings.Builder
	for line := range strings.Lines(s) {
		line, _, _ = strings.Cut(line, "#")
		b.WriteString(strings.Join(strings.Fields(line), ""))
	}
	out, err := hex.DecodeString(b.String())
	Expect(err).NotTo(HaveOccurred())
	return out
}

// twoRulesBin is the RGWCORSConfiguration radosgw stores for
// testdata/vectors/two-rules.xml, built by hand from the encoders
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_cors.h#L63-L111 at v19.2.7
// and v20.2.4): every struct is ENCODE_START(1, 1), a length-prefixed string
// or container a u32 count, and each set sorted bytewise.
const twoRulesBin = `
01 01 ad000000          # RGWCORSConfiguration v1 compat 1, 173 bytes
02000000                # two rules
  01 01 83000000        # rule 1, 131 bytes
    b80b0000            # max_age 3000
    03                  # GET|PUT
    03000000 776562     # id "web"
    02000000            # allowed_hdrs, sorted
      0c000000 436f6e74656e742d54797065                       # Content-Type
      07000000 582d416d7a2d2a                                 # X-Amz-*
    02000000            # allowed_origins, sorted
      15000000 68747470733a2f2f2a2e6578616d706c652e636f6d     # https://*.example.com
      17000000 68747470733a2f2f7777772e6578616d706c652e636f6d # https://www.example.com
    02000000            # exposable_hdrs, in document order
      10000000 782d616d7a2d726571756573742d6964               # x-amz-request-id
      04000000 45546167                                       # ETag
  01 01 1a000000        # rule 2, 26 bytes
    ffffffff            # max_age CORS_MAX_AGE_INVALID
    01                  # GET
    00000000            # id ""
    00000000            # no allowed_hdrs
    01000000 01000000 2a  # allowed_origins: *
    00000000            # no exposable_hdrs
`

func twoRules() *cors.Configuration {
	return &cors.Configuration{Rules: []cors.Rule{
		{
			ID:             "web",
			MaxAge:         3000,
			Methods:        cors.MethodGet | cors.MethodPut,
			AllowedOrigins: []string{"https://*.example.com", "https://www.example.com"},
			AllowedHeaders: []string{"Content-Type", "X-Amz-*"},
			ExposeHeaders:  []string{"x-amz-request-id", "ETag"},
		},
		{MaxAge: cors.MaxAgeInvalid, Methods: cors.MethodGet, AllowedOrigins: []string{"*"}},
	}}
}

var _ = Describe("the stored form", func() {
	It("encodes the hand-built two-rule vector", func() {
		Expect(twoRules().Encode()).To(Equal(unhex(twoRulesBin)))
	})
	It("ships the vector as testdata for the cross-gateway comparison", func() {
		b, err := os.ReadFile("testdata/vectors/two-rules.bin")
		Expect(err).NotTo(HaveOccurred())
		Expect(b).To(Equal(unhex(twoRulesBin)))
	})
	It("decodes the vector", func() {
		c, err := cors.Decode(unhex(twoRulesBin))
		Expect(err).NotTo(HaveOccurred())
		Expect(c).To(Equal(twoRules()))
	})
	It("encodes the sets sorted with each string once, whatever order the rule holds", func() {
		r := twoRules()
		r.Rules[0].AllowedOrigins = []string{"https://www.example.com", "https://*.example.com", "https://www.example.com"}
		r.Rules[0].AllowedHeaders = []string{"X-Amz-*", "Content-Type", "X-Amz-*"}
		Expect(r.Encode()).To(Equal(unhex(twoRulesBin)))
	})
	It("decodes unsorted, repeated set members as std::set holds them", func() {
		e := denc.NewEncoder()
		f := e.BeginStruct(1, 1)
		e.U32(cors.MaxAgeInvalid)
		e.U8(uint8(cors.MethodGet))
		e.String("")
		denc.EncodeSlice(e, []string{"y", "x", "y"}, (*denc.Encoder).String)
		denc.EncodeSlice(e, []string{"b", "a", "b"}, (*denc.Encoder).String)
		e.U32(0)
		e.EndStruct(f)
		d := denc.NewDecoder(e.Bytes())
		got := cors.DecodeRule(d)
		Expect(d.Err()).NotTo(HaveOccurred())
		Expect(got.AllowedHeaders).To(Equal([]string{"x", "y"}))
		Expect(got.AllowedOrigins).To(Equal([]string{"a", "b"}))
	})
	It("round-trips a rule with no method and no max age", func() {
		c := &cors.Configuration{Rules: []cors.Rule{{MaxAge: cors.MaxAgeInvalid, AllowedOrigins: []string{"*"}}}}
		got, err := cors.Decode(c.Encode())
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(c))
		Expect(got.Rules[0].Methods).To(BeZero())
		Expect(got.Rules[0].MaxAge).To(Equal(uint32(0xffffffff)))
	})
	It("keeps method bits radosgw has no name for", func() {
		c := &cors.Configuration{Rules: []cors.Rule{{Methods: 0xc1, AllowedOrigins: []string{"*"}}}}
		got, err := cors.Decode(c.Encode())
		Expect(err).NotTo(HaveOccurred())
		Expect(got.Rules[0].Methods).To(Equal(cors.Methods(0xc1)))
	})
	It("ignores bytes after the configuration, as read_bucket_cors does", func() {
		b := append(unhex(twoRulesBin), 0xde, 0xad)
		c, err := cors.Decode(b)
		Expect(err).NotTo(HaveOccurred())
		Expect(c).To(Equal(twoRules()))
	})
	It("skips a newer rule's trailing fields, as DECODE_FINISH does", func() {
		e := denc.NewEncoder()
		f := e.BeginStruct(2, 1)
		e.U32(1)
		rf := e.BeginStruct(2, 1)
		e.U32(7)
		e.U8(uint8(cors.MethodHead))
		e.String("x")
		e.U32(0)
		denc.EncodeSlice(e, []string{"*"}, (*denc.Encoder).String)
		e.U32(0)
		e.Raw([]byte{1, 2, 3})
		e.EndStruct(rf)
		e.EndStruct(f)
		c, err := cors.Decode(e.Bytes())
		Expect(err).NotTo(HaveOccurred())
		Expect(c.Rules).To(Equal([]cors.Rule{{ID: "x", MaxAge: 7, Methods: cors.MethodHead, AllowedOrigins: []string{"*"}}}))
	})
	DescribeTable("refuses an encoding RGWCORSConfiguration::decode throws on",
		func(b []byte, want error) {
			_, err := cors.Decode(b)
			Expect(err).To(MatchError(want))
		},
		Entry("an empty attr", []byte{}, denc.ErrShortBuffer),
		Entry("a configuration whose compat is newer than 1", []byte{2, 2, 4, 0, 0, 0, 0, 0, 0, 0}, denc.ErrIncompatible),
		Entry("a rule whose compat is newer than 1",
			[]byte{1, 1, 10, 0, 0, 0, 1, 0, 0, 0, 2, 2, 0, 0, 0, 0}, denc.ErrIncompatible),
		Entry("a struct_len past the end", []byte{1, 1, 99, 0, 0, 0, 0, 0, 0, 0}, denc.ErrShortBuffer),
		Entry("a rule count with no rules behind it", []byte{1, 1, 4, 0, 0, 0, 5, 0, 0, 0}, denc.ErrShortBuffer),
		Entry("a rule that reads past its own length",
			append([]byte{1, 1, 31, 0, 0, 0, 1, 0, 0, 0, 1, 1, 1, 0, 0, 0}, make([]byte, 21)...), denc.ErrOverread),
	)
})
