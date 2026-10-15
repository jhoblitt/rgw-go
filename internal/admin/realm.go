package admin

import (
	"context"
	"net/http"

	"github.com/jhoblitt/rgw-go/internal/formatter"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// realmContentType is the type the realm and period getters name whatever
// the format.
const realmContentType = "application/json"

// newRealmHandlers are RGWHandler_Realm's and RGWHandler_Period's GET
// routes. Each dumps its body without starting the flusher and names
// application/json and the length itself, end_header(s, NULL,
// "application/json", s->formatter->get_len())
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rest_realm.cc#L48-L49,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rest_realm.cc#L301-L302
// and
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rest_realm.cc#L344-L348 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rest_realm.cc#L49-L50,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rest_realm.cc#L322-L323
// and
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rest_realm.cc#L365-L369 at v20.2.4).
func newRealmHandlers() map[string]HandlerFunc {
	return map[string]HandlerFunc{
		"get_realm":   getRealm,
		"list_realms": listRealms,
		"get_period":  getPeriod,
	}
}

// getRealm is RGWOp_Realm_Get: the id and name through get_string, the
// realm in a "realm" section
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rest_realm.cc#L276-L304 at v19.2.6).
func getRealm(ctx context.Context, w http.ResponseWriter, r *op.Request, q Request) error {
	o := &op.GetRealm{}
	o.ID, _ = q.Args.String("id", "")
	o.RealmName, _ = q.Args.String("name", "")
	if err := op.Run(ctx, o, r); err != nil {
		return err
	}
	rel := r.Env.Zone.Release()
	return WriteTyped(w, r, q, realmContentType, func(f formatter.Formatter) {
		f.OpenObjectSection("realm")
		o.Result.Dump(f, rel)
		f.CloseSection()
	})
}

// listRealms is RGWOp_Realm_List: a "realms_list" section holding the
// default id and the names as encode_json writes a std::list<std::string>
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rest_realm.cc#L334-L350 at v19.2.6).
func listRealms(ctx context.Context, w http.ResponseWriter, r *op.Request, q Request) error {
	o := &op.ListRealms{}
	if err := op.Run(ctx, o, r); err != nil {
		return err
	}
	return WriteTyped(w, r, q, realmContentType, func(f formatter.Formatter) {
		f.OpenObjectSection("realms_list")
		f.DumpString("default_info", o.DefaultID)
		f.OpenArraySection("realms")
		for _, name := range o.Names {
			f.DumpString("obj", name)
		}
		f.CloseSection()
		f.CloseSection()
	})
}

// getPeriod is RGWOp_Period_Get: realm_id and period_id through
// get_string, epoch through get_uint32, whose result radosgw ignores, so an
// epoch it cannot parse is 0, the latest
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rest_realm.cc#L66-L80 at v19.2.6);
// the period in a "period" section
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rest_realm.cc#L34-L52).
// A period whose zonegroup holds sync policy groups fails the dump, which
// is answered NotImplemented (docs/exclusions.md).
func getPeriod(ctx context.Context, w http.ResponseWriter, r *op.Request, q Request) error {
	o := &op.GetPeriod{}
	o.RealmID, _ = q.Args.String("realm_id", "")
	o.PeriodID, _ = q.Args.String("period_id", "")
	o.Epoch, _, _ = q.Args.Uint32("epoch", 0) //nolint:errcheck // radosgw ignores get_uint32's result
	if err := op.Run(ctx, o, r); err != nil {
		return err
	}
	rel := r.Env.Zone.Release()
	return WriteTyped(w, r, q, realmContentType, func(f formatter.Formatter) {
		f.OpenObjectSection("period")
		o.Result.Dump(f, rel)
		f.CloseSection()
	})
}
