package admin

import (
	"context"
	"net/http"

	"github.com/jhoblitt/rgw-go/internal/formatter"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// infoHandlers are the routes of /admin/info and /admin/config.
func infoHandlers() map[string]HandlerFunc {
	return map[string]HandlerFunc{
		"get_info":        getInfo,
		"get_zone_config": getZoneConfig,
	}
}

func getInfo(ctx context.Context, w http.ResponseWriter, r *op.Request, q Request) error {
	o := op.NewGetInfo()
	if err := op.Run(ctx, o, r); err != nil {
		return err
	}
	// RGWOp_Info_Get::execute
	// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_info.cc#L23-L44 at v19.2.6
	// and v20.2.4): the two "dummy" sections show only in XML and
	// HTML.
	return WriteBody(w, r, q, func(f formatter.Formatter) {
		f.OpenObjectSection("dummy")
		f.OpenObjectSection("info")
		f.OpenArraySection("storage_backends")
		f.OpenObjectSection("dummy")
		f.DumpString("name", "rados")
		f.DumpString("cluster_id", o.ClusterID)
		f.CloseSection()
		f.CloseSection()
		f.CloseSection()
		f.CloseSection()
	})
}

func getZoneConfig(ctx context.Context, w http.ResponseWriter, r *op.Request, q Request) error {
	o := op.NewGetZoneConfig()
	if err := op.Run(ctx, o, r); err != nil {
		return err
	}
	// RGWOp_ZoneConfig_Get::send_response
	// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_config.cc#L35-L47 at v19.2.6
	// and v20.2.4): the headers go out before the dump, so no
	// declaration or status page precedes it. RGWZoneParams::dump writes a
	// sync zone's tier config as stored
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_zone.cc#L332 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_zone.cc#L337 at v20.2.4),
	// its endpoints' credentials with it; they are masked but for a system
	// user, whom every check lets through, as it does the zone's own key
	// (docs/exclusions.md).
	params := o.Params
	if !r.Identity.System {
		params = meta.MaskZoneSecrets(params)
	}
	return WriteDumped(w, r, q, func(f formatter.Formatter) {
		f.OpenObjectSection("zone_params")
		params.Dump(f, r.Env.Zone.Release())
		f.CloseSection()
	})
}
