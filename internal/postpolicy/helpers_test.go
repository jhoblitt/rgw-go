package postpolicy_test

import (
	"errors"
	"slices"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/postpolicy"
)

// future is an expiration after now, past one before it.
const (
	future = "2030-01-01T00:00:00Z"
	past   = "2020-01-01T00:00:00Z"
)

var (
	now = time.Date(2029, 12, 31, 0, 0, 0, 0, time.UTC)
	// futureTime is future as a time.
	futureTime = time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
)

// baseConds are the conditions most of s3-tests' POST policies carry
// (test_post_object_authenticated_request, test_s3.py at the pinned
// s3-tests).
var baseConds = []string{
	`{"bucket": "bkt"}`,
	`["starts-with", "$key", "foo"]`,
	`{"acl": "private"}`,
	`["starts-with", "$Content-Type", "text/plain"]`,
	`["content-length-range", 0, 1024]`,
}

// policyDoc is a policy as s3-tests writes one, json.JSONEncoder's
// separators included.
func policyDoc(expiration string, conds ...string) []byte {
	return []byte(`{"expiration": "` + expiration + `", "conditions": [` + strings.Join(conds, ", ") + `]}`)
}

// u is the JSON escape of the UTF-16 code unit whose hex digits are h,
// spelled out so the spec's source holds the escape and not the character.
func u(h string) string { return `\` + "u" + h }

// withBase is baseConds followed by more, in a new slice.
func withBase(more ...string) []string { return slices.Concat(baseConds, more) }

// baseFields are the form fields of test_post_object_authenticated_request
// but the file, which never enters the environment.
var baseFields = []string{
	"key", "foo.txt",
	"AWSAccessKeyId", "AKID",
	"acl", "private",
	"signature", "SIG",
	"policy", "POLICY",
	"Content-Type", "text/plain",
}

// v2Env is the environment radosgw's get_params and get_policy build for a
// SigV2 upload of the form fields to bucket: each field, then bucket, the key
// and a non-empty Content-Type, with the signature fields and the policy
// marked checked.
func v2Env(bucket string, fields ...string) *postpolicy.Env {
	env := &postpolicy.Env{}
	for i := 0; i+1 < len(fields); i += 2 {
		env.Add(fields[i], fields[i+1])
	}
	env.Add("bucket", bucket)
	if key, ok := env.Get("key"); ok {
		env.Add("key", key)
	}
	if ct, ok := env.Get("Content-Type"); ok && ct != "" {
		env.Add("Content-Type", ct)
	}
	env.MarkChecked("AWSAccessKeyId")
	env.MarkChecked("signature")
	env.MarkChecked("policy")
	return env
}

// mustParse parses doc, failing the spec on a refusal.
func mustParse(doc []byte) *postpolicy.Policy {
	GinkgoHelper()
	p, err := postpolicy.Parse(doc)
	Expect(err).NotTo(HaveOccurred(), "%s", doc)
	return p
}

// refusal returns err's message and sentinel, failing the spec unless err is
// a *postpolicy.Error.
func refusal(err error) (message string, sentinel error) {
	GinkgoHelper()
	pe, ok := errors.AsType[*postpolicy.Error](err)
	Expect(ok).To(BeTrue(), "%v is a %T, not a *postpolicy.Error", err, err)
	switch {
	case errors.Is(err, postpolicy.ErrInvalidArgument):
		return pe.Message, postpolicy.ErrInvalidArgument
	case errors.Is(err, postpolicy.ErrAccessDenied):
		return pe.Message, postpolicy.ErrAccessDenied
	}
	return pe.Message, pe.Err
}

// parseRefusal is Parse's refusal of doc: it must be 400 InvalidArgument.
func parseRefusal(doc []byte) string {
	GinkgoHelper()
	_, err := postpolicy.Parse(doc)
	Expect(err).To(HaveOccurred(), "%s", doc)
	msg, sentinel := refusal(err)
	Expect(sentinel).To(Equal(postpolicy.ErrInvalidArgument), "%s: %v", doc, err)
	return msg
}

// checkRefusal is Check's refusal: it must be 403 AccessDenied, and nil
// gives "".
func checkRefusal(p *postpolicy.Policy, env *postpolicy.Env, at time.Time) string {
	GinkgoHelper()
	err := p.Check(env, at)
	if err == nil {
		return ""
	}
	msg, sentinel := refusal(err)
	Expect(sentinel).To(Equal(postpolicy.ErrAccessDenied), "%v", err)
	return msg
}
