package meta

import (
	"maps"
	"slices"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/formatter"
)

// Dump is RGWUserInfo::dump (rgw_common.cc:2775-2835 at v19.2.6,
// :2837-2897 at v20.2.4). The subusers and keys are encode_json_map with an
// object name and a callback, one named section per entry
// (ceph_json.h:659-690 at v19.2.6), and each key carries its secret, as
// radosgw's metadata get prints it.
func (u UserInfo) Dump(f formatter.Formatter, rel denc.Release) {
	user := u.UserID.String()
	f.DumpString("user_id", user)
	f.DumpString("display_name", u.DisplayName)
	f.DumpString("email", u.Email)
	f.DumpInt("suspended", int64(u.Suspended))
	f.DumpInt("max_buckets", int64(u.MaxBuckets))
	f.OpenArraySection("subusers")
	for _, name := range slices.Sorted(maps.Keys(u.SubUsers)) {
		s := u.SubUsers[name]
		f.OpenObjectSection("subuser")
		f.DumpString("id", user+":"+s.Name)
		f.DumpString("permissions", permString(s.Perm))
		f.CloseSection()
	}
	f.CloseSection()
	dumpUserKeys(f, "keys", user, u.AccessKeys, false)
	dumpUserKeys(f, "swift_keys", user, u.SwiftKeys, true)
	u.Caps.DumpAs(f, "caps")
	f.DumpString("op_mask", maskString(opTypeFlags, u.OpMask))
	// "no need to show it for every user" (rgw_common.cc:2794 at v19.2.6).
	if u.System != 0 {
		f.DumpBool("system", true)
	}
	if u.Admin != 0 {
		f.DumpBool("admin", true)
	}
	f.DumpString("default_placement", u.DefaultPlacement.Name)
	f.DumpString("default_storage_class", u.DefaultPlacement.StorageClass)
	dumpStrings(f, "placement_tags", u.PlacementTags)
	dumpSection(f, "bucket_quota", u.BucketQuota, rel)
	dumpSection(f, "user_quota", u.UserQuota, rel)
	dumpMap(f, "temp_url_keys", u.TempURLKeys,
		func(f formatter.Formatter, k int32) { f.DumpInt("key", int64(k)) },
		func(f formatter.Formatter, v string) { f.DumpString("val", v) })
	f.DumpString("type", u.Type.DumpName())
	dumpStrings(f, "mfa_ids", stringSet(u.MFAIDs))
	f.DumpString("account_id", u.AccountID)
	f.DumpString("path", u.Path)
	dumpTime(f, "create_date", u.CreateDate)
	f.OpenArraySection("tags")
	for _, t := range sortedTags(u.Tags) {
		f.OpenObjectSection("entry")
		f.DumpString("key", t.Key)
		f.DumpString("val", t.Value)
		f.CloseSection()
	}
	f.CloseSection()
	dumpStrings(f, "group_ids", stringSet(u.GroupIDs))
}

// dumpUserKeys is encode_json_map over access_keys or swift_keys with
// user_info_dump_key or user_info_dump_swift_key (rgw_common.cc:2620-2630 at
// v19.2.6, :2682-2692 at v20.2.4): RGWAccessKey::dump(f, user, swift), which
// names the owner with the subuser appended and leaves out a Swift key's id
// (:2974-2988; :3036-3050).
func dumpUserKeys(f formatter.Formatter, name, user string, keys map[string]AccessKey, swift bool) {
	f.OpenArraySection(name)
	for _, id := range slices.Sorted(maps.Keys(keys)) {
		k := keys[id]
		owner := user
		if k.Subuser != "" {
			owner += ":" + k.Subuser
		}
		f.OpenObjectSection("key")
		f.DumpString("user", owner)
		if !swift {
			f.DumpString("access_key", k.ID)
		}
		f.DumpString("secret_key", k.Secret)
		f.DumpBool("active", k.Active)
		dumpTime(f, "create_date", k.CreatedAt)
		f.CloseSection()
	}
	f.CloseSection()
}

// Dump is RGWUserCompleteInfo::dump (driver/rados/rgw_user.h:736-739 at
// v19.2.6, rgw_user.cc:2730-2733 at v20.2.4).
func (c UserCompleteInfo) Dump(f formatter.Formatter, rel denc.Release) {
	c.Info.Dump(f, rel)
	dumpAttrs(f, "attrs", c.Attrs)
}
