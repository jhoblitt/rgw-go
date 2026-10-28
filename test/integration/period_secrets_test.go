package integration_test

import (
	"regexp"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// setTierSecret is a cloud tier's set S3 secret in a period document, in JSON
// or in XML: a tier's s3 section holds the one member named secret a period
// has.
var setTierSecret = regexp.MustCompile(`"secret":"(?:[^"\\]|\\.)+"|<secret>[^<]+</secret>`)

// maskTierSecrets writes each set tier secret in radosgw's period as rgw-go's
// period getter masks it for a caller that is not a system user
// (docs/exclusions.md, "The period getter masks a cloud tier's S3 secret").
func maskTierSecrets(b []byte) []byte {
	return setTierSecret.ReplaceAllFunc(b, func(m []byte) []byte {
		if m[0] == '"' {
			return []byte(`"secret":"******"`)
		}
		return []byte(`<secret>******</secret>`)
	})
}

var _ = Describe("maskTierSecrets", func() {
	DescribeTable("masks a set tier secret and leaves an empty one, the access key and the rest of the period",
		func(in, want string) {
			Expect(string(maskTierSecrets([]byte(in)))).To(Equal(want))
		},
		Entry("in JSON",
			`{"s3":{"access_key":"x","secret":"y\"z","region":""},"t":{"s3":{"access_key":"a","secret":""}}}`,
			`{"s3":{"access_key":"x","secret":"******","region":""},"t":{"s3":{"access_key":"a","secret":""}}}`),
		Entry("in XML",
			`<s3><access_key>x</access_key><secret>y</secret></s3><s3><access_key>a</access_key><secret></secret></s3>`,
			`<s3><access_key>x</access_key><secret>******</secret></s3><s3><access_key>a</access_key><secret></secret></s3>`),
		Entry("a period without a set secret, which the gate's strip check then fails",
			`{"s3":{"secret":""}}`, `{"s3":{"secret":""}}`),
	)
})
