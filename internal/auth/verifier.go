package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// CredentialStore is the part of op.UserStore the verifier reads: the key
// index (LocalEngine::authenticate) and the user a system request names in
// rgwx-uid (SysReqApplier). op.UserStore satisfies it.
type CredentialStore interface {
	GetUser(ctx context.Context, id meta.UserID) (*op.UserRecord, error)
	GetUserByAccessKey(ctx context.Context, key string) (*op.UserRecord, error)
}

// Verifier implements s3.Authenticator: radosgw's s3_main strategy with the
// anonymous and local engines (Keystone, LDAP and STS are excluded).
type Verifier struct {
	cfg      Config
	creds    CredentialStore
	accounts op.AccountStore
	now      func() time.Time
}

// New builds a verifier. accounts may be nil: an account user is then denied,
// as radosgw denies one whose account does not load (rgw_rest_s3.cc:6343-6345
// at v19.2.6, :6914-6916 at v20.2.4).
func New(cfg Config, creds CredentialStore, accounts op.AccountStore) *Verifier {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Verifier{cfg: cfg, creds: creds, accounts: accounts, now: now}
}

// Authenticate resolves req to an identity or an op.Error, in the order of
// RGW_Auth_S3::authorize, the anonymous engine, get_auth_data and the local
// engine. payloads is the dispatched route's forms: a signed payload in a
// form outside them is ErrNotImplemented, answered before the access key is
// looked up, where get_auth_data_v4 throws it. Every result carries
// ContentLength; a Body is the payload's verifying reader, which reports the
// payload's verdict on the Read that ends it.
func (v *Verifier) Authenticate(ctx context.Context, req *http.Request, payloads op.PayloadForms) (*op.AuthResult, error) {
	// Without Keystone and LDAP, rgw_s3_auth_use_rados off leaves radosgw no
	// backend, and it denies every request, anonymous ones included
	// (rgw_rest_s3.cc:5119-5125 at v19.2.6, :5679-5685 at v20.2.4).
	if !v.cfg.UseRados {
		return nil, fmt.Errorf("%w: no authentication backend enabled", op.ErrAccessDenied)
	}
	rv := newRequestView(req)
	ver, rt := discoverFlavour(rv)
	// S3AnonymousEngine::is_applicable (rgw_rest_s3.cc:6614-6628 at v19.2.6,
	// :7222-7236 at v20.2.4).
	if ver == versionUnknown && (rt == routeQuery || req.Method == http.MethodOptions) {
		return &op.AuthResult{Identity: op.Anonymous(), ContentLength: req.ContentLength, PayloadSHA256: unsignedPayload}, nil
	}
	// get_auth_data refuses every signed request before it reads one, and
	// Strategy::apply gives the refusal its message (rgw_rest_s3.cc:5607-5610
	// and rgw_auth.cc:505-509 at v19.2.6, :6163-6166 and :520-524 at v20.2.4).
	if v.cfg.DisablePresignedURLs {
		return nil, op.ErrAccessDenied.WithMessage("Presigned URLs are disabled by admin")
	}
	d, err := v.parseAuthData(rv, ver, rt, payloads)
	if err != nil {
		return nil, err
	}
	// AWSEngine::authenticate (rgw_rest_s3.cc:6179-6180 at v19.2.6,
	// :6750-6751 at v20.2.4).
	if d.accessKey == "" || d.signature == "" {
		return nil, fmt.Errorf("%w: missing access key or signature", op.ErrInvalidArgument)
	}
	rec, key, account, err := v.lookup(ctx, d.accessKey)
	if err != nil {
		return nil, err
	}
	match, err := d.verify(key.Secret)
	if err != nil {
		return nil, err
	}
	if !match {
		return nil, op.ErrSignatureDoesNotMatch
	}
	id, err := v.identity(ctx, rv, rec, d.accessKey, key, account)
	if err != nil {
		return nil, err
	}
	res := &op.AuthResult{Identity: id, ContentLength: req.ContentLength, PayloadSHA256: unsignedPayload, Presigned: d.presigned}
	if d.v4 != nil {
		res.PayloadSHA256 = d.payloadHash
		// The completer's modify_request_state runs once the strategy has
		// granted (rgw_auth.cc:526-528 at v19.2.6, :541-543 at v20.2.4), so a
		// missing x-amz-decoded-content-length is reported after the
		// signature has verified.
		if res.Body, res.ContentLength, err = d.completer(rv, key.Secret); err != nil {
			return nil, err
		}
	}
	return res, nil
}

// parseAuthData is get_auth_data's choice of abstractor
// (rgw_rest_s3.cc:5612-5619 at v19.2.6, :6168-6175 at v20.2.4), an
// Authorization header of another scheme being EINVAL, and get_auth_data_v4's
// last step, the refusal of a payload form the op does not take. A SigV2
// request has no payload form.
func (v *Verifier) parseAuthData(rv *requestView, ver version, rt route, payloads op.PayloadForms) (*authData, error) {
	var (
		d   *authData
		err error
	)
	switch ver {
	case versionV4:
		d, err = authDataV4(rv, rt == routeQuery, &v.cfg, v.now())
	case versionV2:
		d, err = authDataV2(rv, &v.cfg, v.now())
	default:
		return nil, fmt.Errorf("%w: unknown authorization scheme", op.ErrInvalidArgument)
	}
	if err != nil {
		return nil, err
	}
	if err := d.payload.acceptedBy(payloads); err != nil {
		return nil, err
	}
	return d, nil
}

// lookup is LocalEngine::authenticate up to the signature compare
// (rgw_rest_s3.cc:6325-6352 at v19.2.6, :6896-6923 at v20.2.4): the user the
// key index names, the key in that user's record, and the account of an
// account user. Any lookup failure is ERR_INVALID_ACCESS_KEY. radosgw indexes
// only active keys, so an inactive one fails the lookup; rgw-go checks the
// flag after it, which keeps the answer whatever a store indexes. Stored
// identity policies are decoded at authorization, not here. Neither its
// errors nor its log name the access key, which is the request's. A key
// holding a NUL fails without a lookup, as go-ceph would hand librados the
// key's index object name as a C string that ends at the NUL; radosgw looks
// the whole key up and finds none (docs/exclusions.md).
func (v *Verifier) lookup(ctx context.Context, accessKey string) (*op.UserRecord, meta.AccessKey, *meta.AccountInfo, error) {
	if strings.IndexByte(accessKey, 0) >= 0 {
		return nil, meta.AccessKey{}, nil, fmt.Errorf("%w: the request's access key holds a NUL byte", op.ErrInvalidAccessKeyID)
	}
	rec, err := v.creds.GetUserByAccessKey(ctx, accessKey)
	if err != nil {
		if !errors.Is(err, op.ErrNoSuchUser) {
			slog.Log(ctx, lookupLevel(ctx, err), "access key lookup failed", slog.String("code", op.ErrorCode(err)))
		}
		return nil, meta.AccessKey{}, nil, refusal(ctx, err, fmt.Errorf("%w: the request's access key does not load", op.ErrInvalidAccessKeyID))
	}
	key, ok := rec.Info.AccessKeys[accessKey]
	if !ok {
		return nil, meta.AccessKey{}, nil, fmt.Errorf("%w: the request's access key is not in its user's record", op.ErrAccessDenied)
	}
	if !key.Active {
		return nil, meta.AccessKey{}, nil, fmt.Errorf("%w: the request's access key is inactive", op.ErrInvalidAccessKeyID)
	}
	var account *meta.AccountInfo
	if rec.Info.AccountID != "" {
		if account, err = v.loadAccount(ctx, rec.Info.AccountID); err != nil {
			slog.Log(ctx, lookupLevel(ctx, err), "account lookup failed",
				slog.String("account_id", rec.Info.AccountID), slog.Any("error", err))
			return nil, meta.AccessKey{}, nil, refusal(ctx, err,
				fmt.Errorf("%w: the account of user %s does not load", op.ErrAccessDenied, rec.Info.UserID))
		}
	}
	return rec, key, account, nil
}

var errNoAccountStore = errors.New("no account store")

// loadAccount loads account id. A failure, a missing account store
// included, is the caller's to log and to answer with ErrAccessDenied
// (load_account_and_policies, rgw_auth.cc:143-155 at v19.2.6 and v20.2.4),
// as only the caller knows whether id is the request's rgwx-uid.
func (v *Verifier) loadAccount(ctx context.Context, id string) (*meta.AccountInfo, error) {
	if v.accounts == nil {
		return nil, errNoAccountStore
	}
	rec, err := v.accounts.GetAccount(ctx, id)
	if err != nil {
		return nil, err
	}
	return &rec.Info, nil
}

// lookupLevel is the level a failed lookup behind a refusal is logged at:
// debug when the client went away (op.ClientGone), as the store did nothing
// wrong, and error otherwise.
func lookupLevel(ctx context.Context, err error) slog.Level {
	if op.ClientGone(ctx, err) {
		return slog.LevelDebug
	}
	return slog.LevelError
}

// refusal is refused, wrapping the cancellation as well when cause ended a
// request whose client went away, so the protocol handler ends that request
// instead of answering it. Nothing of cause is kept.
func refusal(ctx context.Context, cause, refused error) error {
	if op.ClientGone(ctx, cause) {
		return fmt.Errorf("%w: %w", refused, context.Canceled)
	}
	return refused
}
