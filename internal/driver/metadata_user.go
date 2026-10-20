package driver

import (
	"context"
	"strings"

	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// sectionUser names the section of RGWUserMetadataHandler
// (driver/rados/rgw_user.cc), the users.uid objects
// (services/svc_user_rados.cc).
const sectionUser = "user"

// userSection is the user section, which op serves over any UserStore and
// AccountStore (op.GetMetadataUser, op.PutMetadataUser and
// op.RemoveMetadataUser), so the driver's and the memstore's are one.
type userSection struct{}

func (userSection) get(ctx context.Context, s *Store, key string) (op.MetadataEntry, error) {
	return op.GetMetadataUser(ctx, s, key)
}

func (userSection) put(ctx context.Context, s *Store, key string, e op.MetadataEntry, opts op.PutMetadataOptions) error {
	return op.PutMetadataUser(ctx, s, s, key, e, opts)
}

func (userSection) remove(ctx context.Context, s *Store, key string) error {
	return op.RemoveMetadataUser(ctx, s, s, key)
}

func (userSection) pool(s *Store) meta.Pool { return s.zone.Params.UserUIDPool }

// oidKey is RGWSI_User_Module::is_valid_oid
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_user_rados.cc#L54-L57;
// UserLister, v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/services/svc_user_rados.cc#L65-L81):
// not a user's bucket list.
func (userSection) oidKey(oid string) (string, bool) {
	return oid, !strings.HasSuffix(oid, bucketsSuffix)
}
