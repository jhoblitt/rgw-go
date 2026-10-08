package admin

import (
	"cmp"
	"context"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/formatter"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// newUserHandlers are the user routes that create, read, modify and remove
// a user.
func newUserHandlers() map[string]HandlerFunc {
	return map[string]HandlerFunc{
		"get_user_info": getUserInfo,
		"create_user":   createUser,
		"modify_user":   modifyUser,
		"remove_user":   removeUser,
	}
}

// boolArg is RESTArgs::get_bool as the read-only RGWOp_User_Info calls it,
// unchecked, so a value it refuses is def.
func boolArg(a Args, name string, def bool) bool {
	v, _, err := a.Bool(name, def)
	if err != nil {
		return def
	}
	return v
}

// flags reads the boolean arguments of a route that changes something.
// radosgw's RESTArgs::get_bool takes a value it cannot parse as its default
// and the bodies never check it (rgw_rest.cc:1002-1032 at v19.2.6, :1007-1037
// at v20.2.4), so "active=flase" reactivates a key and "suspended=ture"
// lifts a suspension (docs/ceph-upstream-bugs.md, "radosgw's admin API takes
// an unparsable boolean argument as its default"). flags records the first
// such argument instead, which the route answers with ErrInvalidArgument
// after the op's permission check (runChecked).
type flags struct {
	a   Args
	err error
}

// get is the argument's value, def when absent or unparsable.
func (f *flags) get(name string, def bool) bool {
	v, _ := f.present(name, def)
	return v
}

// present is get and whether the request names the argument.
func (f *flags) present(name string, def bool) (v, ok bool) {
	v, ok, err := f.a.Bool(name, def)
	if err != nil && f.err == nil {
		f.err = err
	}
	return v, ok
}

// ptr is the argument's value when the request names it, nil otherwise.
func (f *flags) ptr(name string) *bool {
	v, ok := f.present(name, false)
	if !ok {
		return nil
	}
	return &v
}

// refused is an op whose arguments were refused: its permission check runs,
// so a caller radosgw refuses is refused first, then argErr is the answer.
type refused struct {
	op.Op
	argErr error
}

func (o refused) Execute(context.Context, *op.Request) error { return o.argErr }

// runChecked is op.Run, answering argErr in place of o's execution when it
// is not nil.
func runChecked(ctx context.Context, o op.Op, r *op.Request, argErr error) error {
	if argErr != nil {
		o = refused{Op: o, argErr: argErr}
	}
	return op.Run(ctx, o, r)
}

// int32Arg is RESTArgs::get_int32 as the bodies call it, unchecked too.
func int32Arg(a Args, name string, def int32) (v int32, present bool) {
	v, present, err := a.Int32(name, def)
	if err != nil {
		return def, present
	}
	return v, present
}

// createUser is RGWOp_User_Create::execute's argument handling
// (driver/rados/rgw_rest_user.cc:147-284 at v19.2.6, :147-290 at v20.2.4).
// The body's own refusals, the system flag and the placement, run in the
// op after the cap check, as they run in execute after verify_permission.
func createUser(ctx context.Context, w http.ResponseWriter, r *op.Request, q Request) error {
	a := q.Args
	o := op.NewCreateUser()
	uid, _ := a.String("uid", "")
	o.UID = meta.ParseUserID(uid)
	if tenant, _ := a.String("tenant", ""); tenant != "" {
		o.UID.Tenant = tenant
	}
	o.DisplayName, _ = a.String("display-name", "")
	o.Email, _ = a.String("email", "")
	o.Caps, _ = a.String("user-caps", "")
	fl := &flags{a: a}
	readKey(a, fl, &o.Key, true)
	def := op.DefaultMaxBuckets(r.Env)
	if mb, _ := int32Arg(a, "max-buckets", def); mb != def {
		o.MaxBuckets = new(max(mb, -1))
	}
	o.Suspended = fl.ptr("suspended")
	o.System = fl.ptr("system")
	o.AccountRoot = fl.ptr("account-root")
	fl.get("exclusive", false)
	o.UserOpMask = opMask(a)
	o.DefaultPlacement = placement(a, r.Env.Zone.Release())
	o.PlacementTags = placementTags(a)
	o.AccountID, _ = a.String("account-id", "")
	o.Path, _ = a.String("path", "")
	if err := runChecked(ctx, o, r, fl.err); err != nil {
		return err
	}
	rel := r.Env.Zone.Release()
	return WriteBody(w, r, q, func(f formatter.Formatter) { dumpUserInfo(f, o.Result, true, nil, rel) })
}

// modifyUser is RGWOp_User_Modify::execute's argument handling
// (driver/rados/rgw_rest_user.cc:301-438 at v19.2.6, :306-449 at v20.2.4):
// max-buckets and email apply when the request names them, and
// generate-key defaults to false.
func modifyUser(ctx context.Context, w http.ResponseWriter, r *op.Request, q Request) error {
	a := q.Args
	o := op.NewModifyUser()
	uid, _ := a.String("uid", "")
	o.UID = meta.ParseUserID(uid)
	o.DisplayName, _ = a.String("display-name", "")
	if email, ok := a.String("email", ""); ok {
		o.Email = &email
	}
	fl := &flags{a: a}
	readKey(a, fl, &o.Key, false)
	if mb, ok := int32Arg(a, "max-buckets", meta.DefaultMaxBuckets); ok {
		o.MaxBuckets = new(max(mb, -1))
	}
	o.Suspended = fl.ptr("suspended")
	o.System = fl.ptr("system")
	o.AccountRoot = fl.ptr("account-root")
	o.UserOpMask = opMask(a)
	o.DefaultPlacement = placement(a, r.Env.Zone.Release())
	o.PlacementTags = placementTags(a)
	o.AccountID, _ = a.String("account-id", "")
	o.Path, _ = a.String("path", "")
	if err := runChecked(ctx, o, r, fl.err); err != nil {
		return err
	}
	rel := r.Env.Zone.Release()
	return WriteBody(w, r, q, func(f formatter.Formatter) { dumpUserInfo(f, o.Result, true, nil, rel) })
}

// getUserInfo is RGWOp_User_Info::execute (driver/rados/rgw_rest_user.cc:90-131
// at v19.2.6 and v20.2.4).
func getUserInfo(ctx context.Context, w http.ResponseWriter, r *op.Request, q Request) error {
	a := q.Args
	o := op.NewGetUserInfo()
	uid, _ := a.String("uid", "")
	o.UID = meta.ParseUserID(uid)
	o.AccessKey, _ = a.String("access-key", "")
	o.FetchStats = boolArg(a, "stats", false)
	o.SyncStats = boolArg(a, "sync", false)
	if err := op.Run(ctx, o, r); err != nil {
		return err
	}
	rel := r.Env.Zone.Release()
	return WriteBody(w, r, q, func(f formatter.Formatter) { dumpUserInfo(f, o.Result, o.DumpKeys, o.Stats, rel) })
}

// removeUser is RGWOp_User_Remove::execute (driver/rados/rgw_rest_user.cc:454-479
// at v19.2.6, :464-489 at v20.2.4), which sends no body.
func removeUser(ctx context.Context, w http.ResponseWriter, r *op.Request, q Request) error {
	o := op.NewRemoveUser()
	uid, _ := q.Args.String("uid", "")
	o.UID = meta.ParseUserID(uid)
	fl := &flags{a: q.Args}
	o.PurgeData = fl.get("purge-data", false)
	if err := runChecked(ctx, o, r, fl.err); err != nil {
		return err
	}
	WriteEmpty(w, r, http.StatusOK)
	return nil
}

// readKey reads the key arguments create and modify share: the access and
// secret keys, key-type when non-empty, and generate-key with its default.
func readKey(a Args, fl *flags, k *op.UserKeyParams, genDefault bool) {
	k.AccessKey, _ = a.String("access-key", "")
	k.SecretKey, _ = a.String("secret-key", "")
	if t, _ := a.String("key-type", ""); t != "" {
		k.Type = op.ParseKeyType(t)
	}
	k.GenerateKey = fl.get("generate-key", genDefault)
}

// opMask is the op-mask argument through rgw_parse_op_type_list, when
// non-empty.
func opMask(a Args) *uint32 {
	s, _ := a.String("op-mask", "")
	if s == "" {
		return nil
	}
	return new(meta.ParseOpTypeList(s))
}

// placement is the default-placement argument when non-empty: Squid's
// rgw_placement_rule::from_str, "name/storage-class", and Tentacle's name
// with default-storage-class (rgw_rest_user.cc:254-263 at v20.2.4).
func placement(a Args, rel denc.Release) *meta.PlacementRule {
	s, _ := a.String("default-placement", "")
	if s == "" {
		return nil
	}
	if rel < denc.Tentacle {
		return new(meta.ParsePlacementRule(s))
	}
	sc, _ := a.String("default-storage-class", "")
	return &meta.PlacementRule{Name: s, StorageClass: sc}
}

// placementTags is the placement-tags argument split as get_str_list
// splits it on ",", empty items dropped, when non-empty.
func placementTags(a Args) []string {
	s, _ := a.String("placement-tags", "")
	if s == "" {
		return nil
	}
	tags := strings.FieldsFunc(s, func(r rune) bool { return r == ',' })
	if tags == nil {
		return []string{}
	}
	return tags
}

// dumpUserInfo is dump_user_info (driver/rados/rgw_user.cc:132-196 at
// v19.2.6, :133-201 at v20.2.4), the admin API's user document: the keys
// only with dumpKeys, the stats only when given. Tentacle adds
// full_user_id first and a namespace after the tenant when the user has
// one.
func dumpUserInfo(f formatter.Formatter, info meta.UserInfo, dumpKeys bool, stats *op.Stats, rel denc.Release) {
	f.OpenObjectSection("user_info")
	if rel >= denc.Tentacle {
		f.DumpString("full_user_id", info.UserID.String())
	}
	f.DumpString("tenant", info.UserID.Tenant)
	if rel >= denc.Tentacle && info.UserID.NS != "" {
		f.DumpString("namespace", info.UserID.NS)
	}
	f.DumpString("user_id", info.UserID.ID)
	f.DumpString("display_name", info.DisplayName)
	f.DumpString("email", info.Email)
	f.DumpInt("suspended", int64(info.Suspended))
	f.DumpInt("max_buckets", int64(info.MaxBuckets))
	dumpSubusers(f, info)
	if dumpKeys {
		dumpAccessKeys(f, info)
		dumpSwiftKeys(f, info)
	}
	info.Caps.DumpAs(f, "caps")
	f.DumpString("op_mask", meta.OpTypeString(info.OpMask))
	f.DumpBool("system", info.System != 0)
	f.DumpBool("admin", info.Admin != 0)
	f.DumpString("default_placement", info.DefaultPlacement.Name)
	f.DumpString("default_storage_class", info.DefaultPlacement.StorageClass)
	dumpObjs(f, "placement_tags", info.PlacementTags)
	f.OpenObjectSection("bucket_quota")
	info.BucketQuota.Dump(f, rel)
	f.CloseSection()
	f.OpenObjectSection("user_quota")
	info.UserQuota.Dump(f, rel)
	f.CloseSection()
	if dumpKeys {
		dumpTempURLKeys(f, info)
	}
	f.DumpString("type", info.Type.DumpName())
	dumpObjs(f, "mfa_ids", slices.Compact(slices.Sorted(slices.Values(info.MFAIDs))))
	f.DumpString("account_id", info.AccountID)
	f.DumpString("path", info.Path)
	f.DumpStream("create_date", info.CreateDate.Gmtime())
	// A std::multimap: key order, equal keys as inserted.
	tags := slices.Clone(info.Tags)
	slices.SortStableFunc(tags, func(a, b meta.UserTag) int { return cmp.Compare(a.Key, b.Key) })
	f.OpenArraySection("tags")
	for _, t := range tags {
		f.OpenObjectSection("entry")
		f.DumpString("key", t.Key)
		f.DumpString("val", t.Value)
		f.CloseSection()
	}
	f.CloseSection()
	dumpObjs(f, "group_ids", slices.Compact(slices.Sorted(slices.Values(info.GroupIDs))))
	if stats != nil {
		f.OpenObjectSection("stats")
		dumpStats(f, *stats)
		f.CloseSection()
	}
	f.CloseSection()
}

// dumpTempURLKeys is encode_json("temp_url_keys", ...), a std::map's
// "entry" sections of "key" and "val" (ceph_json.h:596-607 at v19.2.6).
// They are the Swift TempURL signing secrets, which radosgw dumps whether
// or not the caller may see keys (rgw_user.cc:162 at v19.2.6, :167 at
// v20.2.4; docs/ceph-upstream-bugs.md); rgw-go withholds them with the
// other keys.
func dumpTempURLKeys(f formatter.Formatter, info meta.UserInfo) {
	f.OpenArraySection("temp_url_keys")
	for _, k := range slices.Sorted(maps.Keys(info.TempURLKeys)) {
		f.OpenObjectSection("entry")
		f.DumpInt("key", int64(k))
		f.DumpString("val", info.TempURLKeys[k])
		f.CloseSection()
	}
	f.CloseSection()
}

// dumpObjs is encode_json of a list, vector or set of strings: entries named
// "obj" (ceph_json.h:534-594 at v19.2.6).
func dumpObjs(f formatter.Formatter, name string, v []string) {
	f.OpenArraySection(name)
	for _, s := range v {
		f.DumpString("obj", s)
	}
	f.CloseSection()
}

// dumpSubusers is dump_subusers_info (driver/rados/rgw_user.cc:74-91 at
// v19.2.6, :75-92 at v20.2.4): "user" sections in std::map order.
func dumpSubusers(f formatter.Formatter, info meta.UserInfo) {
	f.OpenArraySection("subusers")
	for _, name := range slices.Sorted(maps.Keys(info.SubUsers)) {
		f.OpenObjectSection("user")
		f.DumpString("id", info.UserID.String()+":"+info.SubUsers[name].Name)
		f.DumpString("permissions", meta.PermString(info.SubUsers[name].Perm))
		f.CloseSection()
	}
	f.CloseSection()
}

// keyUser is dump_format("user", "%s%s%s", uid, sep, subuser)
// (driver/rados/rgw_user.cc:99-104 at v19.2.6).
func keyUser(info meta.UserInfo, k meta.AccessKey) string {
	if k.Subuser == "" {
		return info.UserID.String()
	}
	return info.UserID.String() + ":" + k.Subuser
}

// dumpAccessKeys is dump_access_keys_info (:93-111 at v19.2.6): "key" sections in
// std::map order, with the secret, which only callers dump_keys admits see.
func dumpAccessKeys(f formatter.Formatter, info meta.UserInfo) {
	f.OpenArraySection("keys")
	for _, id := range slices.Sorted(maps.Keys(info.AccessKeys)) {
		k := info.AccessKeys[id]
		f.OpenObjectSection("key")
		f.DumpString("user", keyUser(info, k))
		f.DumpString("access_key", k.ID)
		f.DumpString("secret_key", k.Secret)
		f.DumpBool("active", k.Active)
		f.CloseSection()
	}
	f.CloseSection()
}

// dumpSwiftKeys is dump_swift_keys_info (:113-130 at v19.2.6).
func dumpSwiftKeys(f formatter.Formatter, info meta.UserInfo) {
	f.OpenArraySection("swift_keys")
	for _, id := range slices.Sorted(maps.Keys(info.SwiftKeys)) {
		k := info.SwiftKeys[id]
		f.OpenObjectSection("key")
		f.DumpString("user", keyUser(info, k))
		f.DumpString("secret_key", k.Secret)
		f.DumpBool("active", k.Active)
		f.CloseSection()
	}
	f.CloseSection()
}

// dumpStats is RGWStorageStats::dump with dump_utilized true
// (rgw_common.cc:3102-3115 at v19.2.6, :3164-3177 at v20.2.4); a user's
// stats carry no utilized size, which load_stats leaves 0.
func dumpStats(f formatter.Formatter, s op.Stats) {
	f.DumpUnsigned("size", s.Size)
	f.DumpUnsigned("size_actual", s.SizeRounded)
	f.DumpUnsigned("size_utilized", 0)
	f.DumpUnsigned("size_kb", roundedKB(s.Size))
	f.DumpUnsigned("size_kb_actual", roundedKB(s.SizeRounded))
	f.DumpUnsigned("size_kb_utilized", 0)
	f.DumpUnsigned("num_objects", s.NumObjects)
}

// roundedKB is rgw_rounded_kb: bytes in KiB, rounded up.
func roundedKB(n uint64) uint64 { return (n + 1023) / 1024 }
