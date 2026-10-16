package cors

import "github.com/jhoblitt/rgw-go/internal/denc"

// DecodeRule and EncodeRule expose one RGWCORSRule's coding, which the corpus
// goldens hold, in the shapes goldentest.RoundTrip takes.
var DecodeRule = decodeRule

func EncodeRule(e *denc.Encoder, r Rule, _ denc.Release) { r.encode(e) }

// MatchTries is Match, also returning how many times it tried a one-* member
// of AllowedHeaders against a requested name.
func MatchTries(c *Configuration, origin, method string, reqHeaders []string) (*Rule, int) {
	return c.match(origin, method, reqHeaders)
}
