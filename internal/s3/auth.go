package s3

import (
	"context"
	"net/http"
	"strings"

	"github.com/jhoblitt/rgw-go/internal/op"
)

// Authenticator resolves a request's identity. auth.Verifier implements it;
// AnonymousOnly serves the handler specs.
type Authenticator interface {
	// Authenticate returns the identity, or an op.Error such as
	// ErrInvalidAccessKeyID or ErrSignatureDoesNotMatch. payloads is the
	// dispatched route's Payloads: radosgw answers a signed payload in any
	// other form with ErrNotImplemented from inside authentication, before
	// the key is looked up (get_auth_data_v4, rgw_rest_s3.cc:5899-5969 at
	// v19.2.6, :6466-6540 at v20.2.4).
	Authenticate(ctx context.Context, r *http.Request, payloads op.PayloadForms) (*op.AuthResult, error)
}

// AuthenticatorFunc adapts a function to Authenticator.
type AuthenticatorFunc func(ctx context.Context, r *http.Request, payloads op.PayloadForms) (*op.AuthResult, error)

// Authenticate calls f.
func (f AuthenticatorFunc) Authenticate(ctx context.Context, r *http.Request, payloads op.PayloadForms) (*op.AuthResult, error) {
	return f(ctx, r, payloads)
}

// AnonymousOnly authenticates unsigned requests as anonymous and answers
// ErrNotImplemented to any request carrying credentials: an Authorization
// header, or an X-Amz-Signature, X-Amz-Credential, AWSAccessKeyId or
// Signature query parameter, its name compared without regard to case. It
// never serves a signed request as anonymous. It ignores payloads: an
// anonymous request has no signed payload.
type AnonymousOnly struct{}

var _ Authenticator = AnonymousOnly{}

// credentialParams are the query parameters that carry a presigned URL's
// credentials.
var credentialParams = [...]string{"X-Amz-Signature", "X-Amz-Credential", "AWSAccessKeyId", "Signature"}

// Authenticate implements Authenticator.
func (AnonymousOnly) Authenticate(_ context.Context, r *http.Request, _ op.PayloadForms) (*op.AuthResult, error) {
	if len(r.Header.Values("Authorization")) > 0 {
		return nil, op.ErrNotImplemented
	}
	for name := range r.URL.Query() {
		for _, p := range credentialParams {
			if strings.EqualFold(name, p) {
				return nil, op.ErrNotImplemented
			}
		}
	}
	return &op.AuthResult{Identity: op.Anonymous(), PayloadSHA256: "UNSIGNED-PAYLOAD"}, nil
}
