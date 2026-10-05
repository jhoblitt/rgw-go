package op

import (
	"context"

	"github.com/jhoblitt/rgw-go/internal/meta"
)

// GetInfo is RGWOp_Info_Get (rgw_rest_info.cc:10-44 at v19.2.6 and
// v20.2.4): the cluster's fsid under cap info=read.
type GetInfo struct {
	AdminOp
	// ClusterID is the result, the RADOS fsid.
	ClusterID string
}

// NewGetInfo returns a GetInfo.
func NewGetInfo() *GetInfo { return &GetInfo{} }

// Name is get_info.
func (o *GetInfo) Name() string { return "get_info" }

// VerifyPermission is RGWOp_Info_Get::check_caps.
func (o *GetInfo) VerifyPermission(_ context.Context, r *Request) error {
	return CheckCaps(r, "info", meta.CapRead)
}

// Execute reads the fsid radosgw's driver reports as its cluster id.
func (o *GetInfo) Execute(_ context.Context, r *Request) error {
	o.ClusterID = r.Env.ClusterID
	return nil
}

// GetZoneConfig is RGWOp_ZoneConfig_Get (rgw_rest_config.h:22-38 at v19.2.6
// and v20.2.4): the zone's parameters under cap zone=read.
type GetZoneConfig struct {
	AdminOp
	// Params is the result.
	Params meta.ZoneParams
}

// NewGetZoneConfig returns a GetZoneConfig.
func NewGetZoneConfig() *GetZoneConfig { return &GetZoneConfig{} }

// Name is get_zone_config.
func (o *GetZoneConfig) Name() string { return "get_zone_config" }

// VerifyPermission is RGWOp_ZoneConfig_Get::check_caps.
func (o *GetZoneConfig) VerifyPermission(_ context.Context, r *Request) error {
	return CheckCaps(r, "zone", meta.CapRead)
}

// Execute reads the zone parameters the gateway loaded at startup, as
// radosgw's send_response reads its zone service's.
func (o *GetZoneConfig) Execute(_ context.Context, r *Request) error {
	o.Params = r.Env.Zone.ZoneParams()
	return nil
}
