package authz

import (
	"errors"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/policy"
	"github.com/jhoblitt/rgw-go/internal/tags"
)

// msgOwnerMismatch is the message RGWPutACLs::execute sets with
// acl.ErrOwnerMismatch's -EPERM (rgw_op.cc:5861 at v19.2.6, :6507 at
// v20.2.4).
const msgOwnerMismatch = "Cannot modify ACL Owner"

// ErrorFor maps the sentinels of acl, policy and tags to the S3 errors
// radosgw answers them with, and returns any other error, an *op.Error among
// them, unchanged:
//   - acl.ErrGranteeNotFound is NoSuchKey, the 404 of radosgw's -ENOENT;
//   - acl.ErrUnresolvableEmail is UnresolvableGrantByEmailAddress;
//   - acl.ErrInvalid and acl.ErrNoSuchOwner are InvalidArgument;
//   - acl.ErrOwnerMismatch is AccessDenied, "Cannot modify ACL Owner";
//   - acl.ErrTooManyGrants is LimitExceeded;
//   - a *policy.ParseError is InvalidArgument with its Error(), the
//     e.what() RGWPutBucketPolicy::execute answers (rgw_op.cc:8116-8120 at
//     v19.2.6, :9047-9051 at v20.2.4);
//   - tags.ErrInvalidTag is InvalidTag and tags.ErrMalformedXML is
//     MalformedXML.
//
// An acl sentinel's message, except ErrOwnerMismatch's, is the Message of
// the *acl.ParseError carrying it, none without one. The error returned for a sentinel is a new S3 error
// holding no text of err, which may carry a request header's value.
func ErrorFor(err error) error {
	if err == nil || errors.As(err, new(*op.Error)) {
		return err
	}
	if pe, ok := errors.AsType[*policy.ParseError](err); ok {
		return op.ErrInvalidArgument.WithMessage(pe.Error())
	}
	var msg string
	if pe, ok := errors.AsType[*acl.ParseError](err); ok {
		msg = pe.Message
	}
	switch {
	case errors.Is(err, acl.ErrGranteeNotFound):
		return op.ErrNoSuchKey.WithMessage("")
	case errors.Is(err, acl.ErrUnresolvableEmail):
		return op.ErrUnresolvableGrantByEmail.WithMessage(msg)
	case errors.Is(err, acl.ErrInvalid), errors.Is(err, acl.ErrNoSuchOwner):
		return op.ErrInvalidArgument.WithMessage(msg)
	case errors.Is(err, acl.ErrOwnerMismatch):
		return op.ErrAccessDenied.WithMessage(msgOwnerMismatch)
	case errors.Is(err, acl.ErrTooManyGrants):
		return op.ErrLimitExceeded.WithMessage(msg)
	case errors.Is(err, tags.ErrInvalidTag):
		return op.ErrInvalidTag.WithMessage("")
	case errors.Is(err, tags.ErrMalformedXML):
		return op.ErrMalformedXML.WithMessage("")
	}
	return err
}
