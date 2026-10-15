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
