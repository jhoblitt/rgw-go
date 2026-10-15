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
	// Body, when non-nil, replaces the request body: the payload after
	// aws-chunked decoding, verified as it is read. The Read that ends the
	// payload returns the verification error instead of io.EOF (an
	// XAmzContentSHA256Mismatch, SignatureDoesNotMatch, LimitExceeded or
	// InvalidArgument *Error; a truncated stream is io.ErrUnexpectedEOF), so
	// an op that stores a body reads it to EOF before it completes.
	Body io.Reader
	// ContentLength is the length of Body when Body is non-nil: the
	// x-amz-decoded-content-length of an aws-chunked payload, which radosgw
	// makes the request's content length (AWSv4ComplMulti::modify_request_state,
	// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_s3.cc#L1532-L1542 at v19.2.6,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L1509-L1519 at v20.2.4),
	// otherwise the request's own Content-Length; -1 when unknown. Ignored when
	// Body is nil. An authenticator that returns a Body sets it: the zero value
	// is an empty body, not an unknown one.
	ContentLength int64
	// PayloadSHA256 is the x-amz-content-sha256 value, "UNSIGNED-PAYLOAD" when
	// the header is absent, as radosgw takes it (get_v4_exp_payload_hash,
	// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_s3.h#L638-L661 at v19.2.6,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.h#L641-L664 at v20.2.4).
	PayloadSHA256 string
	// QueryMeta is the x-amz-meta-* parameters and x-amz-security-token a
	// SigV2 presigned request signs in its query, by lower-case name, each
	// value joined as the string to sign joins it. That string runs the
	// query's lines on from the headers', so a name can move between the two
	// with the signature kept; the protocol handler moves these into the
	// request's headers and out of its query, so that every read takes the
	// two as one set (docs/exclusions.md). Empty for every other request.
	QueryMeta map[string]string
	// Route is the route the request authenticated on, which says what of
	// its query its signature covers. An authenticator sets it on every
	// result; one left unset is RouteUnknown, which reads nothing from the
	// query that a signature may leave out.
	Route AuthRoute
}

// AuthRoute is the signature route a request authenticated on. Its zero
// value is RouteUnknown, so that a result naming no route fails closed.
type AuthRoute uint8

// The routes.
const (
	// RouteUnknown is a result that names no route. It reads neither the
	// query's meta nor its encryption settings.
	RouteUnknown AuthRoute = iota
	// RouteAnonymous is an unsigned request, which signs nothing and may
	// send the same names as headers.
	RouteAnonymous
	// RouteSigV2Header is SigV2 in the Authorization header, whose string to
	// sign takes nothing from the query but the sub-resources
	// (rgw_auth_s3.cc:215-216 at v19.2.7, :217-218 at v20.2.4).
	RouteSigV2Header
	// RouteSigV2Query is presigned SigV2, whose string to sign takes the
	// query's x-amz-meta-* and x-amz-security-token alone (get_v2_qs_map,
	// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_auth_s3.cc#L175-L187 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L176-L188 at v20.2.4).
	RouteSigV2Query
	// RouteSigV4Header and RouteSigV4Query are SigV4, whose canonical query
	// names every parameter.
	RouteSigV4Header
	RouteSigV4Query

	numAuthRoutes
)

// ReadsQueryMeta reports whether a request on the route has its query's
// x-amz-meta-* parameters stored, as radosgw's map_qs_metadata merges them
// on every route
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2589-L2603 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2742-L2756 at v20.2.4):
// the routes that sign them, and an anonymous request. SigV2's header
// route signs none of them (docs/exclusions.md), and any other route reads
// none.
func (rt AuthRoute) ReadsQueryMeta() bool {
	switch rt {
	case RouteAnonymous, RouteSigV2Query, RouteSigV4Header, RouteSigV4Query:
		return true
	default:
		return false
	}
}

// ReadsQueryEncryption reports whether a request on the route takes
// encryption settings from its query's x-amz-server-side-encryption*
// parameters, which map_qs_metadata sets over the headers' on every route:
// SigV4, which signs them, and an anonymous request. Neither SigV2 route
// signs them (docs/exclusions.md), and any other route reads none.
func (rt AuthRoute) ReadsQueryEncryption() bool {
	switch rt {
	case RouteAnonymous, RouteSigV4Header, RouteSigV4Query:
		return true
	default:
		return false
	}
}

// PayloadForms is the set of signed payload forms an op accepts. radosgw's
// SigV4 abstractor chooses the payload completer by op type and throws
// ERR_NOT_IMPLEMENTED for a form the op is not listed for, before the access
// key is looked up (get_auth_data_v4,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L5899-L5969 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L6466-L6540 at v20.2.4).
// An empty body and an unsigned payload need no form.
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
