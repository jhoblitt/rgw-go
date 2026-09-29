package op

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"syscall"

	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// Error is radosgw's rgw_err: the S3 error code, its HTTP status and an
// optional message. Sentinels are compared with errors.Is, which matches the
// code and status and ignores the message, so an op may return
// ErrX.WithMessage("...") and callers still match ErrX.
type Error struct {
	Code    string
	Status  int
	Message string
	// Errno is the magnitude of rgw_err::ret radosgw would carry: an errno or
	// an ERR_* number from rgw_common.h. It is informational.
	Errno int
}

func (e *Error) Error() string {
	if e.Message == "" {
		return e.Code
	}
	return e.Code + ": " + e.Message
}

// Is reports whether target is an *Error with the same code and status. The
// sentinels that reproduce radosgw's duplicate table rows (InvalidRequest/400,
// AccessDenied/403, NoSuchEntity/404, NoSuchCORSConfiguration/404) therefore
// match one another; Errno tells them apart.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Code == e.Code && t.Status == e.Status
}

// WithMessage returns a copy of e carrying msg as its message.
func (e *Error) WithMessage(msg string) *Error {
	c := *e
	c.Message = msg
	return &c
}

// The S3 error set, one sentinel per row of radosgw's rgw_http_s3_errors at
// v19.2.6 (rgw_common.cc:51-144) and v20.2.4 (:52-146, which adds only
// RestoreAlreadyInProgress), with the ret values of rgw_common.h. Four rows
// share a sentinel with the row of the same code and status: EPERM
// (ErrAccessDenied), EBUSY (ErrServiceUnavailable), ERR_ZERO_IN_URL
// (ErrInvalidRequest) and EDQUOT (ErrInsufficientCapacity). Rows ceph main
// added later (ExpiredToken 2501, AccessControlListNotSupported 2227,
// InvalidBucketAclWithObjectOwnership 2228, BucketSuspended 2101,
// OwnershipControlsNotFoundError 2229) are absent: no floor release can emit
// them. ErrUnknown is no row but set_req_state_err's fallback for a ret the
// table lacks.
var (
	ErrPermanentRedirect            = &Error{Code: "PermanentRedirect", Status: 301, Errno: 2024}
	ErrWebsiteRedirect              = &Error{Code: "WebsiteRedirect", Status: 301, Errno: 2038}
	ErrNotModified                  = &Error{Code: "NotModified", Status: 304, Errno: 2016}
	ErrInvalidArgument              = &Error{Code: "InvalidArgument", Status: 400, Errno: 22}
	ErrInvalidRequest               = &Error{Code: "InvalidRequest", Status: 400, Errno: 2021}
	ErrInvalidDigest                = &Error{Code: "InvalidDigest", Status: 400, Errno: 2004}
	ErrBadDigest                    = &Error{Code: "BadDigest", Status: 400, Errno: 2005}
	ErrInvalidLocationConstraint    = &Error{Code: "InvalidLocationConstraint", Status: 400, Errno: 2208}
	ErrIllegalLocationConstraint    = &Error{Code: "IllegalLocationConstraintException", Status: 400, Errno: 2226}
	ErrZonegroupPlacementMisconfig  = &Error{Code: "ZonegroupDefaultPlacementMisconfiguration", Status: 400, Errno: 2213}
	ErrInvalidBucketName            = &Error{Code: "InvalidBucketName", Status: 400, Errno: 2000}
	ErrInvalidObjectName            = &Error{Code: "InvalidObjectName", Status: 400, Errno: 2001}
	ErrUnresolvableGrantByEmail     = &Error{Code: "UnresolvableGrantByEmailAddress", Status: 400, Errno: 2006}
	ErrInvalidPart                  = &Error{Code: "InvalidPart", Status: 400, Errno: 2007}
	ErrInvalidPartOrder             = &Error{Code: "InvalidPartOrder", Status: 400, Errno: 2008}
	ErrRequestTimeout               = &Error{Code: "RequestTimeout", Status: 400, Errno: 2010}
	ErrEntityTooLarge               = &Error{Code: "EntityTooLarge", Status: 400, Errno: 2019}
	ErrEntityTooSmall               = &Error{Code: "EntityTooSmall", Status: 400, Errno: 2022}
	ErrTooManyBuckets               = &Error{Code: "TooManyBuckets", Status: 400, Errno: 2020}
	ErrMalformedXML                 = &Error{Code: "MalformedXML", Status: 400, Errno: 2029}
	ErrContentSHA256Mismatch        = &Error{Code: "XAmzContentSHA256Mismatch", Status: 400, Errno: 2040}
	ErrMalformedPolicy              = &Error{Code: "MalformedPolicyDocument", Status: 400, Errno: 2204}
	ErrInvalidTag                   = &Error{Code: "InvalidTag", Status: 400, Errno: 2210}
	ErrMalformedACL                 = &Error{Code: "MalformedACLError", Status: 400, Errno: 2212}
	ErrInvalidCORSRules             = &Error{Code: "InvalidRequest", Status: 400, Errno: 2215}
	ErrInvalidWebsiteRoutingRules   = &Error{Code: "InvalidRequest", Status: 400, Errno: 2217}
	ErrInvalidEncryptionAlgorithm   = &Error{Code: "InvalidEncryptionAlgorithmError", Status: 400, Errno: 2214}
	ErrInvalidRetentionPeriod       = &Error{Code: "InvalidRetentionPeriod", Status: 400, Errno: 2047}
	ErrInvalidSecretKey             = &Error{Code: "InvalidSecretKey", Status: 400, Errno: 2034}
	ErrInvalidKeyType               = &Error{Code: "InvalidKeyType", Status: 400, Errno: 2035}
	ErrInvalidCapability            = &Error{Code: "InvalidCapability", Status: 400, Errno: 2036}
	ErrInvalidTenantName            = &Error{Code: "InvalidTenantName", Status: 400, Errno: 2037}
	ErrAccessDenied                 = &Error{Code: "AccessDenied", Status: 403, Errno: 13}
	ErrMFARequired                  = &Error{Code: "AccessDenied", Status: 403, Errno: 2044}
	ErrAuthorization                = &Error{Code: "AuthorizationError", Status: 403, Errno: 2225}
	ErrSignatureDoesNotMatch        = &Error{Code: "SignatureDoesNotMatch", Status: 403, Errno: 2027}
	ErrInvalidAccessKeyID           = &Error{Code: "InvalidAccessKeyId", Status: 403, Errno: 2028}
	ErrUserSuspended                = &Error{Code: "UserSuspended", Status: 403, Errno: 2100}
	ErrRequestTimeTooSkewed         = &Error{Code: "RequestTimeTooSkewed", Status: 403, Errno: 2012}
	ErrQuotaExceeded                = &Error{Code: "QuotaExceeded", Status: 403, Errno: 2026}
	ErrInvalidObjectState           = &Error{Code: "InvalidObjectState", Status: 403, Errno: 2222}
	ErrNoSuchKey                    = &Error{Code: "NoSuchKey", Status: 404, Errno: 2}
	ErrNoSuchBucket                 = &Error{Code: "NoSuchBucket", Status: 404, Errno: 2002}
	ErrNoSuchWebsiteConfiguration   = &Error{Code: "NoSuchWebsiteConfiguration", Status: 404, Errno: 2039}
	ErrNoSuchUpload                 = &Error{Code: "NoSuchUpload", Status: 404, Errno: 2009}
	ErrNotFound                     = &Error{Code: "NotFound", Status: 404, Errno: 2023}
	ErrNoSuchLifecycleConfiguration = &Error{Code: "NoSuchLifecycleConfiguration", Status: 404, Errno: 2041}
	ErrNoSuchBucketPolicy           = &Error{Code: "NoSuchBucketPolicy", Status: 404, Errno: 2207}
	ErrNoSuchUser                   = &Error{Code: "NoSuchUser", Status: 404, Errno: 2042}
	ErrNoRoleFound                  = &Error{Code: "NoSuchEntity", Status: 404, Errno: 2205}
	ErrNoCORSFound                  = &Error{Code: "NoSuchCORSConfiguration", Status: 404, Errno: 2216}
	ErrNoSuchSubUser                = &Error{Code: "NoSuchSubUser", Status: 404, Errno: 2043}
	ErrNoSuchEntity                 = &Error{Code: "NoSuchEntity", Status: 404, Errno: 2301}
	ErrNoSuchCORSConfiguration      = &Error{Code: "NoSuchCORSConfiguration", Status: 404, Errno: 2045}
	ErrNoSuchObjectLockConfig       = &Error{Code: "ObjectLockConfigurationNotFoundError", Status: 404, Errno: 2046}
	ErrNoSuchTagSet                 = &Error{Code: "NoSuchTagSet", Status: 404, Errno: 2402}
	ErrNoSuchBucketEncryption       = &Error{Code: "ServerSideEncryptionConfigurationNotFoundError", Status: 404, Errno: 2048}
	ErrNoSuchPublicAccessBlock      = &Error{Code: "NoSuchPublicAccessBlockConfiguration", Status: 404, Errno: 2049}
	ErrMethodNotAllowed             = &Error{Code: "MethodNotAllowed", Status: 405, Errno: 2003}
	ErrRequestTimedOut              = &Error{Code: "RequestTimeout", Status: 408, Errno: 110}
	ErrBucketAlreadyExists          = &Error{Code: "BucketAlreadyExists", Status: 409, Errno: 17}
	ErrUserAlreadyExists            = &Error{Code: "UserAlreadyExists", Status: 409, Errno: 2030}
	ErrEmailExists                  = &Error{Code: "EmailExists", Status: 409, Errno: 2032}
	ErrKeyExists                    = &Error{Code: "KeyExists", Status: 409, Errno: 2033}
	ErrTagConflict                  = &Error{Code: "OperationAborted", Status: 409, Errno: 2209}
	ErrPositionNotEqualToLength     = &Error{Code: "PositionNotEqualToLength", Status: 409, Errno: 2219}
	ErrObjectNotAppendable          = &Error{Code: "ObjectNotAppendable", Status: 409, Errno: 2220}
	ErrInvalidBucketState           = &Error{Code: "InvalidBucketState", Status: 409, Errno: 2221}
	ErrBucketNotEmpty               = &Error{Code: "BucketNotEmpty", Status: 409, Errno: 39}
	ErrLimitExceeded                = &Error{Code: "LimitExceeded", Status: 409, Errno: 2302}
	ErrAccountAlreadyExists         = &Error{Code: "AccountAlreadyExists", Status: 409, Errno: 2403}
	ErrRestoreAlreadyInProgress     = &Error{Code: "RestoreAlreadyInProgress", Status: 409, Errno: 2500} // v20.2.4 only (rgw_common.h:364, table row :142); no Squid row
	ErrConcurrentModification       = &Error{Code: "ConcurrentModification", Status: 409, Errno: 125}
	ErrMissingContentLength         = &Error{Code: "MissingContentLength", Status: 411, Errno: 2011}
	ErrPreconditionFailed           = &Error{Code: "PreconditionFailed", Status: 412, Errno: 2015}
	ErrInvalidRange                 = &Error{Code: "InvalidRange", Status: 416, Errno: 34}
	ErrUnprocessableEntity          = &Error{Code: "UnprocessableEntity", Status: 422, Errno: 2018}
	ErrLocked                       = &Error{Code: "Locked", Status: 423, Errno: 2025}
	ErrInternalError                = &Error{Code: "InternalError", Status: 500, Errno: 2200}
	ErrUnknown                      = &Error{Code: "UnknownError", Status: 500, Errno: 0}
	ErrNotImplemented               = &Error{Code: "NotImplemented", Status: 501, Errno: 2201}
	ErrServiceUnavailable           = &Error{Code: "ServiceUnavailable", Status: 503, Errno: 2202}
	ErrSlowDown                     = &Error{Code: "SlowDown", Status: 503, Errno: 2218}
	ErrInsufficientCapacity         = &Error{Code: "InsufficientCapacity", Status: 507, Errno: 28}
)

// Errors returns every sentinel above, for tests and for the ops log's table.
func Errors() []*Error { return slices.Clone(errorSet) }

var errorSet = []*Error{
	ErrPermanentRedirect, ErrWebsiteRedirect, ErrNotModified,
	ErrInvalidArgument, ErrInvalidRequest, ErrInvalidDigest, ErrBadDigest,
	ErrInvalidLocationConstraint, ErrIllegalLocationConstraint, ErrZonegroupPlacementMisconfig,
	ErrInvalidBucketName, ErrInvalidObjectName, ErrUnresolvableGrantByEmail,
	ErrInvalidPart, ErrInvalidPartOrder, ErrRequestTimeout, ErrEntityTooLarge, ErrEntityTooSmall,
	ErrTooManyBuckets, ErrMalformedXML, ErrContentSHA256Mismatch, ErrMalformedPolicy, ErrInvalidTag,
	ErrMalformedACL, ErrInvalidCORSRules, ErrInvalidWebsiteRoutingRules, ErrInvalidEncryptionAlgorithm,
	ErrInvalidRetentionPeriod, ErrInvalidSecretKey, ErrInvalidKeyType, ErrInvalidCapability,
	ErrInvalidTenantName,
	ErrAccessDenied, ErrMFARequired, ErrAuthorization, ErrSignatureDoesNotMatch, ErrInvalidAccessKeyID,
	ErrUserSuspended, ErrRequestTimeTooSkewed, ErrQuotaExceeded, ErrInvalidObjectState,
	ErrNoSuchKey, ErrNoSuchBucket, ErrNoSuchWebsiteConfiguration, ErrNoSuchUpload, ErrNotFound,
	ErrNoSuchLifecycleConfiguration, ErrNoSuchBucketPolicy, ErrNoSuchUser, ErrNoRoleFound, ErrNoCORSFound,
	ErrNoSuchSubUser, ErrNoSuchEntity, ErrNoSuchCORSConfiguration, ErrNoSuchObjectLockConfig,
	ErrNoSuchTagSet, ErrNoSuchBucketEncryption, ErrNoSuchPublicAccessBlock,
	ErrMethodNotAllowed, ErrRequestTimedOut,
	ErrBucketAlreadyExists, ErrUserAlreadyExists, ErrEmailExists, ErrKeyExists, ErrTagConflict,
	ErrPositionNotEqualToLength, ErrObjectNotAppendable, ErrInvalidBucketState, ErrBucketNotEmpty,
	ErrLimitExceeded, ErrAccountAlreadyExists, ErrRestoreAlreadyInProgress, ErrConcurrentModification,
	ErrMissingContentLength, ErrPreconditionFailed, ErrInvalidRange, ErrUnprocessableEntity, ErrLocked,
	ErrInternalError, ErrUnknown, ErrNotImplemented, ErrServiceUnavailable, ErrSlowDown,
	ErrInsufficientCapacity,
}

// errZeroInURL is ERR_ZERO_IN_URL, the one folded row whose ret no sentinel
// carries and syscall does not name.
const errZeroInURL = 2211

// byErrno is rgw_http_s3_errors keyed as search_err looks it up: by the
// magnitude of the ret value, the folded rows included.
var byErrno = func() map[int]*Error {
	m := make(map[int]*Error, len(errorSet)+4)
	for _, e := range errorSet {
		m[e.Errno] = e
	}
	m[int(syscall.EPERM)] = ErrAccessDenied
	m[int(syscall.EBUSY)] = ErrServiceUnavailable
	m[int(syscall.EDQUOT)] = ErrInsufficientCapacity
	m[errZeroInURL] = ErrInvalidRequest
	return m
}()

// AsError returns the *Error in err's chain. An error that carries none is a
// bug in an op and renders as InternalError; a context error renders as the
// 408 RequestTimeout, which is what a client that is still connected sees.
func AsError(err error) *Error {
	if e, ok := errors.AsType[*Error](err); ok {
		return e
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return ErrRequestTimedOut
	}
	return ErrInternalError
}

// FromRADOS maps a seam error to the S3 error radosgw's rgw_http_s3_errors
// table implies for scope: ENOENT is NoSuchBucket in a bucket op, NoSuchKey in
// an object op, NoSuchUser and NoSuchUpload likewise, and radosgw's own ENOENT
// row (NoSuchKey) otherwise; EEXIST is UserAlreadyExists for a user and the
// table's BucketAlreadyExists row otherwise. Every other errno is the table's
// row for it, ECANCELED the 409 ConcurrentModification row
// (rgw_common.cc:141 at v19.2.6, :143 at v20.2.4). An errno the table does not
// list, ERR_BUSY_RESHARDING among them, is UnknownError, as set_req_state_err
// resorts to (rgw_common.cc:322-354 at v19.2.6); a failure that carries no
// errno, the seam's local errors among them, is InternalError. The result
// wraps both the S3 error and err, so errors.Is matches either and the RADOS
// cause reaches the log; an err that already carries an *Error is returned
// unchanged.
func FromRADOS(err error, scope Scope) error {
	if err == nil {
		return nil
	}
	if errors.As(err, new(*Error)) {
		return err
	}
	var mapped *Error
	switch {
	case errors.Is(err, radosclient.ErrNotFound):
		mapped = notFound(scope)
	case errors.Is(err, radosclient.ErrExists):
		mapped = ErrBucketAlreadyExists
		if scope == ScopeUser {
			mapped = ErrUserAlreadyExists
		}
	case errors.Is(err, radosclient.ErrPermission):
		mapped = ErrAccessDenied
	case errors.Is(err, radosclient.ErrInvalid):
		mapped = ErrInvalidArgument
	case errors.Is(err, radosclient.ErrRange):
		mapped = ErrInvalidRange
	case errors.Is(err, radosclient.ErrTimedOut):
		mapped = ErrRequestTimedOut
	case errors.Is(err, radosclient.ErrNoSpace):
		mapped = ErrInsufficientCapacity
	case errors.Is(err, radosclient.ErrCanceled):
		mapped = ErrConcurrentModification
	case errors.Is(err, radosclient.ErrBusyResharding), errors.Is(err, radosclient.ErrNoData),
		errors.Is(err, radosclient.ErrTooBig), errors.Is(err, radosclient.ErrNotSupported):
		mapped = ErrUnknown
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		mapped = ErrRequestTimedOut
	default:
		mapped = ErrInternalError
		if re, ok := errors.AsType[*radosclient.Error](err); ok {
			mapped = fromErrno(re.Errno)
		}
	}
	return fmt.Errorf("%w: %w", mapped, err)
}

func notFound(scope Scope) *Error {
	switch scope {
	case ScopeBucket:
		return ErrNoSuchBucket
	case ScopeUser:
		return ErrNoSuchUser
	case ScopeUpload:
		return ErrNoSuchUpload
	default:
		return ErrNoSuchKey
	}
}

// fromErrno is search_err over rgw_http_s3_errors for a librados return code,
// UnknownError when the table lacks it.
func fromErrno(errno int32) *Error {
	n := int(errno)
	if n < 0 {
		n = -n
	}
	if e, ok := byErrno[n]; ok {
		return e
	}
	return ErrUnknown
}
