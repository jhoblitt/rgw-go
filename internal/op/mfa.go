package op

import (
	"context"

	"github.com/jhoblitt/rgw-go/internal/meta"
)

//counterfeiter:generate . MFAVerifier

// MFAVerifier checks an x-amz-mfa code against a user's MFA devices, as
// RadosUser::verify_mfa does once it has split the header into serial and
// pin
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_sal_rados.cc#L284-L313 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L301-L330 at v20.2.4).
type MFAVerifier interface {
	// VerifyMFA reports whether pin is the current code of user's device
	// serial. A serial the user has no device for, or a code that fails the
	// check, is false with a nil error; an error means the check could not
	// run.
	VerifyMFA(ctx context.Context, user *meta.UserInfo, serial, pin string) (bool, error)
}

// OwnerMFAVerified reports whether r's x-amz-mfa verified a device of the
// owner of the bucket r loaded, the one MFA delete answers to: the user that
// owns it, or the root user of the account that owns it. The S3 User
// Guide's "Configuring MFA delete"
// (https://docs.aws.amazon.com/AmazonS3/latest/userguide/MultiFactorAuthenticationDelete.html):
// "only the bucket owner (root account) can enable MFA delete", and "the
// bucket owner must include two forms of authentication in any request to
// delete a version or change the versioning state". The code verified a
// device of r.Identity.User, so the user record decides, not the owner a
// system request's rgwx-uid names. radosgw takes the verified code of any
// requester its permissions let act, the requester's own device
// (docs/ceph-upstream-bugs.md, "radosgw lets a requester's own MFA device
// satisfy another owner's MFA delete"; docs/exclusions.md, "Only the bucket
// owner's x-amz-mfa satisfies MFA delete").
func OwnerMFAVerified(r *Request) bool {
	if !r.MFAVerified || r.BucketRec == nil || r.Identity.User == nil {
		return false
	}
	u, owner := r.Identity.User, r.BucketRec.Info.Owner
	if owner.User != nil {
		return u.UserID == *owner.User
	}
	return u.Type == meta.IdentityRoot && owner.Account != "" && u.AccountID == owner.Account
}
