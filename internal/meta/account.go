package meta

import "github.com/jhoblitt/rgw-go/internal/denc"

// AccountInfo is RGWAccountInfo, a user account. Its JSON form is
// RGWAccountInfo::dump.
type AccountInfo struct {
	ID            string `json:"id"`
	Tenant        string `json:"tenant"`
	Name          string `json:"name"`
	Email         string `json:"email"`
	Quota         Quota  `json:"quota"`
	BucketQuota   Quota  `json:"bucket_quota"`
	MaxUsers      int32  `json:"max_users"`
	MaxRoles      int32  `json:"max_roles"`
	MaxGroups     int32  `json:"max_groups"`
	MaxBuckets    int32  `json:"max_buckets"`
	MaxAccessKeys int32  `json:"max_access_keys"`
}

// The RGWAccountInfo DEFAULT_*_LIMIT constants.
const (
	DefaultAccountUserLimit      = 1000
	DefaultAccountRoleLimit      = 1000
	DefaultAccountGroupLimit     = 1000
	DefaultAccountBucketLimit    = 1000
	DefaultAccountAccessKeyLimit = 4
)

// NewAccountInfo returns the value RGWAccountInfo's member initializers give.
func NewAccountInfo() AccountInfo {
	return AccountInfo{
		Quota:         defaultQuota(),
		BucketQuota:   defaultQuota(),
		MaxUsers:      DefaultAccountUserLimit,
		MaxRoles:      DefaultAccountRoleLimit,
		MaxGroups:     DefaultAccountGroupLimit,
		MaxBuckets:    DefaultAccountBucketLimit,
		MaxAccessKeys: DefaultAccountAccessKeyLimit,
	}
}

// Encode mirrors RGWAccountInfo::encode, ENCODE_START(2, 1).
func (a AccountInfo) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(2, 1)
	e.String(a.ID)
	e.String(a.Tenant)
	e.String(a.Name)
	e.String(a.Email)
	a.Quota.Encode(e, r)
	e.I32(a.MaxUsers)
	e.I32(a.MaxRoles)
	e.I32(a.MaxGroups)
	e.I32(a.MaxBuckets)
	e.I32(a.MaxAccessKeys)
	a.BucketQuota.Encode(e, r)
	e.EndStruct(f)
}

// DecodeAccountInfo mirrors RGWAccountInfo::decode, DECODE_START(2), into a
// NewAccountInfo: version 1 had no bucket quota.
func DecodeAccountInfo(d *denc.Decoder) AccountInfo {
	h := d.BeginStruct(2)
	a := NewAccountInfo()
	a.ID = d.String()
	a.Tenant = d.String()
	a.Name = d.String()
	a.Email = d.String()
	a.Quota = DecodeQuota(d)
	a.MaxUsers = d.I32()
	a.MaxRoles = d.I32()
	a.MaxGroups = d.I32()
	a.MaxBuckets = d.I32()
	a.MaxAccessKeys = d.I32()
	if h.Version >= 2 {
		a.BucketQuota = DecodeQuota(d)
	}
	d.EndStruct(h)
	return a
}
