package auth

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/op"
)

// v4Vector is one testdata/v4 file: a request, how it is signed, and what
// authDataV4 must make of it.
type v4Vector struct {
	Name    string              `json:"name"`
	Method  string              `json:"method"`
	Target  string              `json:"target"`
	Host    string              `json:"host"`
	Headers map[string][]string `json:"headers"`
	Body    string              `json:"body"`
	// Presigned selects the query route; a header vector gets an
	// Authorization header, verbatim when Authorization is set and built
	// from SignedHeaders, Scope and Signature otherwise.
	Presigned     bool   `json:"presigned"`
	Authorization string `json:"authorization"`
	SignedHeaders string `json:"signed_headers"`
	Scope         string `json:"scope"`
	Secret        string `json:"secret"`
	// Signature is the client's; empty means the one Expect's canonical
	// request yields, so a canonicalization that differs from the recorded
	// text fails on the canonical_request assertion.
	Signature string `json:"signature"`
	// Note says where a given Signature came from; the runner ignores it.
	Note     string    `json:"note"`
	Now      time.Time `json:"now"`
	Insecure bool      `json:"insecure"`
	Expect   struct {
		CanonicalRequest string `json:"canonical_request"`
		Date             string `json:"date"`
		AccessKey        string `json:"access_key"`
		// Error is the op.Error code authDataV4 fails with.
		Error string `json:"error"`
		// VerifyFalse expects the request to parse and verify to refuse it.
		VerifyFalse bool `json:"verify_false"`
	} `json:"expect"`
}

func (v *v4Vector) authorization() string {
	if v.Authorization != "" {
		return v.Authorization
	}
	sig := v.Signature
	if sig == "" {
		sig = signatureV4(signingKeyV4(v.Secret, v.Scope), stringToSignV4(v.Expect.Date, v.Scope, v.Expect.CanonicalRequest))
	}
	return aws4Algorithm + " Credential=" + v.Expect.AccessKey + "/" + v.Scope +
		", SignedHeaders=" + v.SignedHeaders + ", Signature=" + sig
}

func loadV4Vector(path string) *v4Vector {
	raw, err := os.ReadFile(path)
	Expect(err).NotTo(HaveOccurred())
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	v := &v4Vector{}
	Expect(dec.Decode(v)).To(Succeed(), "decoding %s", path)
	Expect(v.Name).To(Equal(strings.TrimSuffix(filepath.Base(path), ".json")), "a vector is named after its file")
	return v
}

func runV4Vector(ctx SpecContext, path string) {
	v := loadV4Vector(path)
	req := httptest.NewRequestWithContext(ctx, v.Method, "http://"+v.Host+v.Target, strings.NewReader(v.Body))
	for name, values := range v.Headers {
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}
	if !v.Presigned {
		req.Header.Set("Authorization", v.authorization())
	}
	cfg := DefaultConfig()
	cfg.Insecure = v.Insecure

	d, err := authDataV4(newRequestView(req), v.Presigned, &cfg, v.Now)
	if v.Expect.Error != "" {
		opErr, _ := errors.AsType[*op.Error](err)
		Expect(opErr).To(HaveField("Code", v.Expect.Error), "error %v", err)
		return
	}
	Expect(err).NotTo(HaveOccurred())
	Expect(d.canonicalRequest).To(Equal(v.Expect.CanonicalRequest))
	Expect(d.v4.date).To(Equal(v.Expect.Date))
	Expect(d.accessKey).To(Equal(v.Expect.AccessKey))
	ok, err := d.verify(v.Secret)
	Expect(err).NotTo(HaveOccurred())
	Expect(ok).To(Equal(!v.Expect.VerifyFalse), "verify")
}

// vectorFiles lists testdata/<dir>/*.json. Glob fails only on a malformed
// pattern, which a constant one is not.
func vectorFiles(dir string) []string {
	paths, err := filepath.Glob(filepath.Join("testdata", dir, "*.json"))
	if err != nil {
		panic(err)
	}
	return paths
}

func vectorEntries(dir string) []TableEntry {
	var entries []TableEntry
	for _, path := range vectorFiles(dir) {
		entries = append(entries, Entry(strings.TrimSuffix(filepath.Base(path), ".json"), path))
	}
	return entries
}

var _ = Describe("signature vectors", func() {
	It("finds the SigV4 vectors", func() {
		Expect(vectorFiles("v4")).NotTo(BeEmpty())
	})

	DescribeTable("testdata/v4", runV4Vector, vectorEntries("v4"))
})
