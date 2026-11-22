package auth

import (
	"crypto/hmac"
	"crypto/sha256"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// This pins docs/differences.md, "A secret key signs with its own bytes":
// the SigV4 signing key is the HMAC chain over the secret's bytes, as AWS
// clients compute it, where radosgw's transform_secret_key re-encodes each
// byte as a UTF-8 code point first.
var _ = Describe("the SigV4 signing key of a secret holding a byte above 0x7f", func() {
	It("is the HMAC chain keyed with the secret's own bytes", func() {
		const secret = "s\xe9cr\xffet"
		mac := func(key []byte, msg string) []byte {
			h := hmac.New(sha256.New, key)
			h.Write([]byte(msg))
			return h.Sum(nil)
		}
		want := mac(mac(mac(mac([]byte("AWS4"+secret), "20150830"), "us-east-1"), "s3"), "aws4_request")
		Expect(signingKeyV4(secret, "20150830/us-east-1/s3/aws4_request")).To(Equal(want))
	})
})
