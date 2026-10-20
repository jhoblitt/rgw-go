package postpolicy_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jhoblitt/rgw-go/internal/postpolicy"
)

// fuzzSeeds are s3-tests' POST policy documents (test_s3.py at the pinned
// s3-tests), as json.JSONEncoder writes them, and the forms JSONParser reads
// differently from Go's Unmarshal.
func fuzzSeeds() []string {
	exp := `"expiration": "2030-01-01T00:00:00Z"`
	base := `{"bucket": "bkt"}, ["starts-with", "$key", "foo"], {"acl": "private"}, ["starts-with", "$Content-Type", "text/plain"]`
	return []string{
		`{` + exp + `, "conditions": [` + base + `, ["content-length-range", 0, 1024]]}`,
		`{` + exp + `, "conditions": [{"bUcKeT": "bkt"}, ["StArTs-WiTh", "$KeY", "foo"], {"AcL": "private"}, ["StArTs-WiTh", "$CoNtEnT-TyPe", "text/plain"], ["content-length-range", 0, 1024]]}`,
		`{` + exp + `, "conditions": [{"bucket": "bkt"}, ["starts-with", "$key", "\\$foo"], {"acl": "private"}]}`,
		`{` + exp + `, "conditions": [` + base + `, ["eq", "$success_action_redirect", "http://h/"], ["content-length-range", 0, 1024]]}`,
		`{"expiration": "2029-12-31 00:00:00.000000+00:00", "conditions": [` + base + `]}`,
		`{` + exp + `, "conditions": [` + base + `, ["starts-with", "$x-amz-meta-foo",  "bar"]]}`,
		`{` + exp + `, "CONDITIONS": [` + base + `]}`,
		`{"EXPIRATION": "2030-01-01T00:00:00Z", "conditions": [` + base + `]}`,
		`{"conditions": [` + base + `]}`,
		`{` + exp + `}`,
		`{` + exp + `, "conditions": [` + base + `, ["content-length-range", 0, 0]]}`,
		`{` + exp + `, "conditions": [` + base + `, ["content-length-range", 0]]}`,
		`{` + exp + `, "conditions": [` + base + `, ["content-length-range", -1, 0]]}`,
		`{` + exp + `, "conditions": [` + base + `, ["content-length-range", 4194304, 12582912]]}`,
		`{` + exp + `, "conditions": [{ }]}`,
		`{` + exp + `, "conditions": [` + base + `, ["eq", "$x-amz-meta-foo",  ""], ["starts-with", "$tagging", ""]]}`,
		`{` + exp + `, ` + exp + `, "conditions": {"b": ["eq", "$key", 1e3], "a": {"z": 0.3, "acl": [true, null, {"k": -0}]}}}`,
		`{` + exp + `, "conditions": [["eq", "$x", 18446744073709551615], ["eq", "$y", -9223372036854775808], ["eq", "$z", 1.5e-7]]}`,
		`{` + exp + `, "conditions": [["EQ` + u("0000") + `", "$k` + u("0000") + `x", "` + u("d83d") + u("de00") + `"]]} trailing`,
		`[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]`,
		`"x"`, `5`, ``, `1e3` + strings.Repeat(" ", 14),
	}
}

// errText is err's text, "" for nil.
func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// knownMessage reports whether msg is one radosgw sets for a policy.
func knownMessage(msg string) bool {
	switch msg {
	case "Malformed JSON", "Policy missing expiration", "Failed to parse policy expiration",
		"Policy missing conditions", "Bad condition array, expecting 3 arguments",
		"Bad content-length-range param", "Failed to parse policy", "Policy expired":
		return true
	}
	for _, prefix := range []string{
		"Invalid condition: ", "Policy check failed, variable not found: ",
		"Policy check failed, variable not met condition: ", "Policy condition failed: eq: ",
		"Policy condition failed: starts-with: ", "Policy missing condition: ",
	} {
		if strings.HasPrefix(msg, prefix) {
			return true
		}
	}
	return false
}

// FuzzParse reads arbitrary documents as a POST policy, which any holder of
// a key can sign. A parse must not panic and must answer the same twice. A
// refusal must be a *postpolicy.Error naming ErrInvalidArgument with one of
// radosgw's messages. A policy it accepts keeps 0 <= MinLength, and Check
// against a form holding the fuzzed field must answer nil or a
// *postpolicy.Error naming ErrAccessDenied with one of radosgw's messages,
// the same on both of two calls. go test runs the seeds; go test
// -fuzz=FuzzParse explores from them.
func FuzzParse(f *testing.F) {
	for _, seed := range fuzzSeeds() {
		f.Add([]byte(seed), "key", "foo.txt")
	}
	at := time.Date(2029, 12, 31, 0, 0, 0, 0, time.UTC)
	f.Fuzz(func(t *testing.T, doc []byte, name, value string) {
		p, err := postpolicy.Parse(doc)
		again, err2 := postpolicy.Parse(doc)
		if !reflect.DeepEqual(p, again) || errText(err) != errText(err2) {
			t.Fatalf("%q parses as %+v, %v and then as %+v, %v", doc, p, err, again, err2)
		}
		if err != nil {
			pe, ok := errors.AsType[*postpolicy.Error](err)
			if !ok || !errors.Is(err, postpolicy.ErrInvalidArgument) || !knownMessage(pe.Message) {
				t.Fatalf("%q is refused with %T %v", doc, err, err)
			}
			return
		}
		if p.MinLength < 0 {
			t.Fatalf("%q gives the minimum length %d", doc, p.MinLength)
		}
		var answers [2]error
		for i := range answers {
			env := &postpolicy.Env{}
			env.Add(name, value)
			env.Add("bucket", "bkt")
			answers[i] = p.Check(env, at)
		}
		if errText(answers[0]) != errText(answers[1]) {
			t.Fatalf("%q checks as %v and then as %v", doc, answers[0], answers[1])
		}
		if err := answers[0]; err != nil {
			pe, ok := errors.AsType[*postpolicy.Error](err)
			if !ok || !errors.Is(err, postpolicy.ErrAccessDenied) || !knownMessage(pe.Message) {
				t.Fatalf("%q with %q=%q is refused with %T %v", doc, name, value, err, err)
			}
		}
	})
}
