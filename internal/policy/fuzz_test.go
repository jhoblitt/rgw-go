package policy_test

import (
	"errors"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

// FuzzParse parses arbitrary text on both releases, with and without a
// tenant and with invalid principals rejected and not. Parse must not panic,
// must keep an accepted text whole, and must fail only with a *ParseError
// whose offset lies within the text up to its first NUL, where the parse
// ends. go test runs the seeds; go test -fuzz=FuzzParse explores from them.
func FuzzParse(f *testing.F) {
	files, err := filepath.Glob(filepath.Join("testdata", "*.json"))
	if err != nil {
		f.Fatal(err)
	}
	for _, name := range files {
		b, err := os.ReadFile(name)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(string(b))
	}
	for _, s := range []string{
		``,
		`{"Statement": "x"}`,
		`{"Statement": [{"Effect": "Allow", "Condition": [{"Bool": {"k": [1, "${a}", true]}}], "Effect": "Deny"}]}`,
		`{"Statement": {"Principal": {"AWS": ["*", "arn:aws:iam::t:user/u", "t"]}, "NotPrincipal": [{"Service": "x"}]}}`,
		`{"Statement": {"Principal": {"Federated": "arn:aws:iam::t:oidc-provider/a"}, "Action": "*x", "Resource": "arn:aws:s3::o:b"}}`,
		`/* c */ {"Id": "é😀", "Version": "2012-10-17"} // x`,
		`{"Statement": {"Condition": {"NumericEquals": {"k": [-1.5e300, 0.0001e-5, 12345678901234567890123.5, 1e309]}}}}`,
		"{\"Id\": \"\xff\"}\x00{",
	} {
		f.Add(s)
	}
	// Dropped principals log a warning each; keep them out of the output.
	oldLogger, oldWriter, oldFlags := slog.Default(), log.Writer(), log.Flags()
	slog.SetDefault(slog.New(slog.DiscardHandler))
	f.Cleanup(func() {
		slog.SetDefault(oldLogger)
		log.SetOutput(oldWriter)
		log.SetFlags(oldFlags)
	})
	tenant := "t"
	f.Fuzz(func(t *testing.T, s string) {
		end := len(s)
		if i := strings.IndexByte(s, 0); i >= 0 {
			end = i
		}
		for _, r := range []denc.Release{denc.Squid, denc.Tentacle} {
			for _, opts := range []policy.ParseOptions{
				{Tenant: &tenant, RejectInvalidPrincipals: true, Release: r},
				{Release: r},
			} {
				p, err := policy.Parse(s, opts)
				if err == nil {
					if p.Text != s {
						t.Fatalf("on %s: Text %q, parsed from %q", r, p.Text, s)
					}
					continue
				}
				pe, ok := errors.AsType[*policy.ParseError](err)
				if !ok {
					t.Fatalf("on %s: %v is not a *policy.ParseError", r, err)
				}
				if pe.Offset < 0 || pe.Offset > int64(end) {
					t.Fatalf("on %s: offset %d outside [0, %d]: %v", r, pe.Offset, end, err)
				}
			}
		}
	})
}
