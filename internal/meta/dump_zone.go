package meta

import (
	"maps"
	"slices"
	"strings"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/formatter"
)

// Dump is RGWZoneParams::dump
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_zone.cc#L309-L334 at v19.2.6;
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_zone.cc#L312-L340 at v20.2.4,
// which adds dedup_pool, bucket_logging_pool and restore_pool). Neither
// release dumps the OIDC or vector pool.
func (z ZoneParams) Dump(f formatter.Formatter, rel denc.Release) {
	// RGWSystemMetaObj::dump
	// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_zone.cc#L812-L816 at v19.2.6).
	f.DumpString("id", z.ID)
	f.DumpString("name", z.Name)
	dumpPool(f, "domain_root", z.DomainRoot)
	dumpPool(f, "control_pool", z.ControlPool)
	if rel >= denc.Tentacle {
		dumpPool(f, "dedup_pool", z.DedupPool)
	}
	for _, p := range []struct {
		name string
		pool Pool
	}{
		{"gc_pool", z.GCPool},
		{"lc_pool", z.LCPool},
		{"log_pool", z.LogPool},
		{"intent_log_pool", z.IntentLogPool},
		{"usage_log_pool", z.UsageLogPool},
		{"roles_pool", z.RolesPool},
		{"reshard_pool", z.ReshardPool},
		{"user_keys_pool", z.UserKeysPool},
		{"user_email_pool", z.UserEmailPool},
		{"user_swift_pool", z.UserSwiftPool},
		{"user_uid_pool", z.UserUIDPool},
		{"otp_pool", z.OTPPool},
		{"notif_pool", z.NotifPool},
		{"topics_pool", z.TopicsPool},
		{"account_pool", z.AccountPool},
		{"group_pool", z.GroupPool},
	} {
		dumpPool(f, p.name, p.pool)
	}
	if rel >= denc.Tentacle {
		dumpPool(f, "bucket_logging_pool", z.BucketLoggingPool)
	}
	// encode_json_plain: RGWAccessKey::dump_plain in an object section
	// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_zone.cc#L43-L48,
	// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L2968-L2972 at v19.2.6).
	f.OpenObjectSection("system_key")
	f.DumpString("access_key", z.SystemKey.ID)
	f.DumpString("secret_key", z.SystemKey.Secret)
	f.CloseSection()
	dumpMap(f, "placement_pools", z.PlacementPools,
		func(f formatter.Formatter, k string) { f.DumpString("key", k) },
		func(f formatter.Formatter, p ZonePlacementInfo) { dumpSection(f, "val", p, rel) })
	z.TierConfig.DumpAs(f, "tier_config")
	f.DumpString("realm_id", z.RealmID)
	if rel >= denc.Tentacle {
		dumpPool(f, "restore_pool", z.RestorePool)
	}
}

// MaskZoneSecrets is z with every credential in its tier config masked: each
// object member named secret or password, at any depth and in arrays too,
// that renders anything but the empty string is written as MaskedTierSecret.
// Those are the names under which the cloud sync module's
// AWSSyncConfig_Connection::init reads an endpoint's secret
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_sync_module_aws.cc#L234-L256
// at v19.2.7 and v20.2.4), wherever AWSSyncConfig::init finds a connection
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_sync_module_aws.cc#L523-L555
// at v19.2.7 and v20.2.4), and whose own dump_conf writes the secret so
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_sync_module_aws.cc#L257-L271
// at v19.2.7 and v20.2.4), and under which the elasticsearch module reads the
// password it puts in its default_headers
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_sync_module_es.cc#L191-L195 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sync_module_es.cc#L192-L196 at v20.2.4).
// A member named endpoint keeps every byte of its URL but its userinfo's
// password, which is written as MaskedTierSecret, and one it cannot split
// cleanly is masked whole: the elasticsearch module hands its endpoint to
// an RGWRESTConn as it is
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_sync_module_es.cc#L179-L181 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sync_module_es.cc#L180-L182 at v20.2.4),
// whose request URL curl takes as CURLOPT_URL
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_http_client.cc#L588 at v19.2.7
// and v20.2.4), userinfo and all.
// Every map and slice of the tier config is copied, so the caller's zone
// keeps its credentials; the system key is not masked.
func MaskZoneSecrets(z ZoneParams) ZoneParams {
	z.TierConfig = z.TierConfig.maskSecrets()
	return z
}

func (j JSONFormattable) maskSecrets() JSONFormattable {
	j.Array = slices.Clone(j.Array)
	for i, x := range j.Array {
		j.Array[i] = x.maskSecrets()
	}
	j.Object = maps.Clone(j.Object)
	for k, x := range j.Object {
		switch {
		case (k == "secret" || k == "password") && x.rendersValue():
			j.Object[k] = JSONFormattable{Type: FormattableValue, Value: MaskedTierSecret, Quoted: true}
		case k == "endpoint" && x.Type == FormattableValue:
			x = x.maskSecrets()
			x.Value = maskURLPassword(x.Value)
			j.Object[k] = x
		default:
			j.Object[k] = x.maskSecrets()
		}
	}
	return j
}

// maskURLPassword is v with the password of its URL's userinfo written as
// MaskedTierSecret, failing closed, since curl reads URLs url.Parse refuses
// and skips a run of slashes after the scheme. A value with no '@' is kept.
// One that is exactly a scheme, "://" and an authority running to the first
// '/', '?' or '#', with every '@' inside that authority, has what follows
// its userinfo's first ':' masked, the userinfo ending at the authority's
// last '@', and every other byte kept; a userinfo without a password, or
// with an empty one, is kept. Any other value with an '@' is masked whole.
func maskURLPassword(v string) string {
	if !strings.Contains(v, "@") {
		return v
	}
	scheme, rest, ok := strings.Cut(v, "://")
	if !ok || !isScheme(scheme) {
		return MaskedTierSecret
	}
	end := len(rest)
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		end = i
	}
	if strings.Contains(rest[end:], "@") {
		return MaskedTierSecret
	}
	authority := rest[:end]
	at := strings.LastIndexByte(authority, '@')
	colon := strings.IndexByte(authority[:at], ':')
	if colon < 0 || colon+1 == at {
		return v
	}
	userinfo := len(scheme) + len("://")
	return v[:userinfo+colon+1] + MaskedTierSecret + v[userinfo+at:]
}

// isScheme reports whether s is an RFC 3986 scheme: a letter, then letters,
// digits, '+', '-' or '.'.
func isScheme(s string) bool {
	for i, c := range s {
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z':
		case i > 0 && ('0' <= c && c <= '9' || c == '+' || c == '-' || c == '.'):
		default:
			return false
		}
	}
	return s != ""
}

// rendersValue reports whether DumpAs writes anything for j but the empty
// string.
func (j JSONFormattable) rendersValue() bool {
	switch j.Type {
	case FormattableValue:
		return j.Value != ""
	case FormattableArray, FormattableObject:
		return true
	default:
		return false
	}
}

// Dump is RGWZonePlacementInfo::dump
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_zone.cc#L763-L773 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_zone.cc#L771-L781 at v20.2.4).
func (p ZonePlacementInfo) Dump(f formatter.Formatter, rel denc.Release) {
	dumpPool(f, "index_pool", p.IndexPool)
	dumpSection(f, "storage_classes", p.StorageClasses, rel)
	dumpPool(f, "data_extra_pool", p.DataExtraPool)
	f.DumpUnsigned("index_type", uint64(p.IndexType))
	f.DumpBool("inline_data", p.InlineData)
}

// Dump is RGWZoneStorageClasses::dump
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_zone.cc#L869-L874 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_zone.cc#L884-L889 at v20.2.4):
// one section per class, named by the class, in key order.
func (c ZoneStorageClasses) Dump(f formatter.Formatter, rel denc.Release) {
	for _, name := range slices.Sorted(maps.Keys(c)) {
		dumpSection(f, name, c[name], rel)
	}
}

// Dump is RGWZoneStorageClass::dump
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_zone.cc#L926-L934 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_zone.cc#L966-L974 at v20.2.4).
func (s ZoneStorageClass) Dump(f formatter.Formatter, _ denc.Release) {
	if s.DataPool != nil {
		dumpPool(f, "data_pool", *s.DataPool)
	}
	if s.CompressionType != nil {
		f.DumpString("compression_type", *s.CompressionType)
	}
}

// DumpAs is encode_json(name, JSONFormattable)
// (https://github.com/ceph/ceph/blob/v19.2.6/src/common/ceph_json.cc#L922-L946 at v19.2.6):
// a value quoted or bare as it was stored
// (https://github.com/ceph/ceph/blob/v19.2.6/src/common/ceph_json.cc#L29-L36),
// an array whose entries are named "obj", an object whose members carry
// their keys, in key order; nothing for FMT_NONE or a type byte past
// FMT_OBJ.
func (j JSONFormattable) DumpAs(f formatter.Formatter, name string) {
	switch j.Type {
	case FormattableValue:
		if j.Quoted {
			f.DumpString(name, j.Value)
		} else {
			f.DumpUnquoted(name, j.Value)
		}
	case FormattableArray:
		f.OpenArraySection(name)
		for _, e := range j.Array {
			e.DumpAs(f, "obj")
		}
		f.CloseSection()
	case FormattableObject:
		f.OpenObjectSection(name)
		for _, k := range slices.Sorted(maps.Keys(j.Object)) {
			j.Object[k].DumpAs(f, k)
		}
		f.CloseSection()
	}
}
