package op

import (
	"io"

	"github.com/jhoblitt/rgw-go/internal/meta"
)

// AnonymousUserID is RGW_USER_ANON_ID, the user id every unauthenticated request runs as.
const AnonymousUserID = "anonymous"

// Identity is who the request is from, resolved by auth before dispatch.
type Identity struct {
	Anonymous bool
	// User is the resolved RGWUserInfo; for an anonymous request it is the
	// default user info under AnonymousUserID, as radosgw builds it.
	User *meta.UserInfo
	// Owner is the user, or the account for an account user: what buckets and
	// objects this identity creates are owned by.
	Owner meta.Owner
	// Account is set when the user belongs to an account.
	Account *meta.AccountInfo
	SubUser string
	// Tenant is the identity's tenant, the default tenant of the buckets it names.
	Tenant string
	// AccessKey is the key that signed the request, "" for anonymous.
	AccessKey string
	// OpMask is the RGW_OP_TYPE_* bits the user may perform; Run checks it.
	OpMask uint32
	Caps   meta.Caps
	// Admin is radosgw's is_admin(): Run lets such an identity through an
	// unmarked access denial from VerifyPermission.
	Admin bool
	// System is a system user's request: no quota, and it may modify a
	// read-only zone.
	System bool
	// Attrs are the user's stored xattrs. Identity policies live in them and
	// the authorizer decodes them on demand, as radosgw does per request
	// (get_iam_identity_policies_from_attr).
	Attrs map[string][]byte
}

// Anonymous is the identity of an unauthenticated request: a
// default-constructed RGWUserInfo under AnonymousUserID, as
// rgw::auth::AnonymousEngine builds it through rgw_get_anon_user.
func Anonymous() Identity {
	u := meta.NewUserInfo()
	u.UserID = meta.UserID{ID: AnonymousUserID}
	return Identity{
		Anonymous: true,
		User:      &u,
		Owner:     meta.UserOwner(u.UserID),
		OpMask:    u.OpMask,
	}
}

// AuthResult is what authentication resolves a request to.
type AuthResult struct {
	Identity Identity
	// Body replaces the request body when the payload is signed in chunks;
	// nil keeps the request's own body.
	Body io.Reader
	// PayloadSHA256 is the x-amz-content-sha256 value, "UNSIGNED-PAYLOAD" when
	// the header is absent, which is radosgw's fallback.
	PayloadSHA256 string
	Presigned     bool
}

// PayloadForms is the set of signed payload forms an op accepts. radosgw's
// SigV4 abstractor chooses the payload completer by op type and throws
// ERR_NOT_IMPLEMENTED for a form the op is not listed for, before the access
// key is looked up (get_auth_data_v4, rgw_rest_s3.cc:5899-5969 at v19.2.6,
// :6466-6540 at v20.2.4). An empty body and an unsigned payload need no form.
type PayloadForms uint8

// The payload forms.
const (
	// PayloadSigned is a non-empty body whose x-amz-content-sha256 is its
	// SHA-256, verified whole (AWSv4ComplSingle).
	PayloadSigned PayloadForms = 1 << iota
	// PayloadChunked is an aws-chunked body, x-amz-content-sha256 starting
	// "STREAMING-" (AWSv4ComplMulti); radosgw takes it for RGW_OP_PUT_OBJ only.
	PayloadChunked
)
