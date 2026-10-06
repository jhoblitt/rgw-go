package admin

import (
	"context"
	"net/http"

	"github.com/jhoblitt/rgw-go/internal/formatter"
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
	// RGWOp_Info_Get::execute (rgw_rest_info.cc:23-44 at v19.2.6 and
	// v20.2.4): the two "dummy" sections show only in XML and HTML.
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
	// RGWOp_ZoneConfig_Get::send_response (rgw_rest_config.cc:35-47 at
	// v19.2.6 and v20.2.4): the headers go out before the dump, so no
	// declaration or status page precedes it.
	return WriteDumped(w, r, q, func(f formatter.Formatter) {
		f.OpenObjectSection("zone_params")
		o.Params.Dump(f, r.Env.Zone.Release())
		f.CloseSection()
	})
}
