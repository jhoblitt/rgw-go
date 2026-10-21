package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jhoblitt/rgw-go/internal/op"
)

// AuthenticateBrowserUpload is radosgw's s3_post strategy, AWSAuthStrategy
// over AWSBrowserUploadAbstractor with the local engine alone, which
// get_policy applies to a POST object form's credentials
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_auth_registry.h#L51-L53 at v19.2.7
// and v20.2.4;
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3190-L3193 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L3326-L3329 at v20.2.4).
// The strategy has no anonymous engine, and get_policy answers any failure
// -EACCES, so every refusal here is ErrAccessDenied, wrapping its reason, and
// none is the anonymous identity.
//
// SigV2 signs the policy with HMAC-SHA1 of the secret. SigV4 takes the key id
// from the credential up to its first '/', the rest as the scope, and signs
// the policy with that scope's signing key; it checks neither the scope's
// date, region or service nor the form's x-amz-date (AWSBrowserUploadAbstractor,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L6157-L6212 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L6687-L6742 at v20.2.4).
// A credential with no '/' is refused, where radosgw takes it whole as both
// the key id and the scope (docs/exclusions.md). An empty key id or
// signature is refused, as AWSEngine::authenticate refuses it
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L6220-L6221 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L6750-L6751 at v20.2.4),
// and signatures compare in constant time.
//
// The key's lookup and the identity are the header path's, the user record
// and the attrs that hold its identity policies included, which replace the
// anonymous ones as Strategy::apply's do
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_auth.cc#L489-L555 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth.cc#L504-L570 at v20.2.4),
// but for a system user's rgwx-uid: the form's signature covers nothing of
// the request's query, so the identity takes none, where radosgw's
// SysReqApplier reads it from the query (docs/exclusions.md). The local
// engine ignores a session token, as on the header path.
// rgw_s3_auth_disable_signature_url does not apply: radosgw reads it in
// AWSGeneralAbstractor alone
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L5637-L5640 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L6163-L6166 at v20.2.4).
func (v *Verifier) AuthenticateBrowserUpload(ctx context.Context, creds op.BrowserUploadCredentials) (*op.AuthResult, error) {
	res, err := v.browserUpload(ctx, creds)
	if err != nil {
		return nil, fmt.Errorf("%w: browser upload: %w", op.ErrAccessDenied, err)
	}
	return res, nil
}

// browserUpload is AuthenticateBrowserUpload with its refusals unwrapped.
func (v *Verifier) browserUpload(ctx context.Context, creds op.BrowserUploadCredentials) (*op.AuthResult, error) {
	if !v.cfg.UseRados {
		return nil, errors.New("no authentication backend enabled")
	}
	d, err := browserUploadAuthData(creds)
	if err != nil {
		return nil, err
	}
	if d.accessKey == "" || d.signature == "" {
		return nil, errors.New("missing access key or signature")
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
	// A view of no request: the identity reads only its rgwx- parameters,
	// and the form signs none.
	id, err := v.identity(ctx, &requestView{}, rec, d.accessKey, key, account)
	if err != nil {
		return nil, err
	}
	return &op.AuthResult{Identity: id, ContentLength: -1, PayloadSHA256: unsignedPayload, Route: op.RouteBrowserUpload}, nil
}

// browserUploadAuthData is AWSBrowserUploadAbstractor::get_auth_data: the
// key id, the client's signature and the signature factory, whose string to
// sign is the policy field.
func browserUploadAuthData(creds op.BrowserUploadCredentials) (*authData, error) {
	policy := creds.Policy
	d := &authData{signature: creds.Signature, stringToSign: policy}
	if creds.Algorithm != aws4Algorithm {
		d.accessKey = creds.AccessKey
		d.sign = func(secret string) (string, error) { return signatureV2(secret, policy) }
		return d, nil
	}
	accessKey, scope, ok := strings.Cut(creds.Credential, "/")
	if !ok {
		return nil, errors.New("credential without a scope")
	}
	d.accessKey = accessKey
	d.sign = func(secret string) (string, error) { return signatureV4(signingKeyV4(secret, scope), policy), nil }
	return d, nil
}
