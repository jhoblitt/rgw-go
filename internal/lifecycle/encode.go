package lifecycle

import (
	"cmp"
	"fmt"
	"slices"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/tags"
)

// Every encoder here writes what both releases write: rgw_lc.h's encoders
// are the same at v19.2.7 and v20.2.4, so the release is passed through
// unread.

// Encode is LCExpiration::encode
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.h#L52-L58 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.h#L52-L58 at v20.2.4):
// ENCODE_START(4, 2), days, date and newer_noncurrent.
func (x Expiration) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(4, 2)
	e.String(x.Days)
	e.String(x.Date)
	e.String(x.NewerNoncurrent)
	e.EndStruct(f)
}

// DecodeExpiration is LCExpiration::decode
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.h#L59-L69 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.h#L59-L69 at v20.2.4):
// DECODE_START_LEGACY_COMPAT_LEN(4, 2, 2), whose v1 carries neither compat
// nor length; days, then date from v3 and newer_noncurrent from v4.
func DecodeExpiration(d *denc.Decoder) Expiration {
	h := d.BeginStructLegacy(4, 2, 2, 0)
	var x Expiration
	x.Days = d.String()
	if h.Version >= 3 {
		x.Date = d.String()
	}
	if h.Version >= 4 {
		x.NewerNoncurrent = d.String()
	}
	d.EndStruct(h)
	return x
}

// Encode is LCTransition::encode
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.h#L149-L155 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.h#L149-L155 at v20.2.4):
// ENCODE_START(1, 1), days, date and storage_class.
func (t Transition) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	e.String(t.Days)
	e.String(t.Date)
	e.String(t.StorageClass)
	e.EndStruct(f)
}

// DecodeTransition is LCTransition::decode
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.h#L157-L163 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.h#L157-L163 at v20.2.4):
// DECODE_START(1), then all three fields whatever the version.
func DecodeTransition(d *denc.Decoder) Transition {
	h := d.BeginStruct(1)
	var t Transition
	t.Days = d.String()
	t.Date = d.String()
	t.StorageClass = d.String()
	d.EndStruct(h)
	return t
}

// Encode is LCFilter::encode
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.h#L288-L296 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.h#L288-L296 at v20.2.4):
// ENCODE_START(4, 1), prefix, the tags as RGWObjTags, flags, size_gt and
// size_lt.
func (f Filter) Encode(e *denc.Encoder, r denc.Release) {
	fr := e.BeginStruct(4, 1)
	e.String(f.Prefix)
	f.Tags.Encode(e, r)
	e.U32(f.Flags)
	e.String(f.SizeGT)
	e.String(f.SizeLT)
	e.EndStruct(fr)
}

// DecodeFilter is LCFilter::decode
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.h#L297-L311 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.h#L297-L311 at v20.2.4):
// prefix, then the tags from v2, the flags from v3 and the sizes from v4.
// v19.2.7 opens it with DECODE_START(3) and v20.2.4 with DECODE_START(4),
// which StructVChecker needs for its v4 test; they differ only on a compat
// of 4, which no encoder at either release writes, and this reads it as
// v20.2.4 does. The tags take tags.Decode's fallback to stored text, as
// RGWObjTags::decode does.
func DecodeFilter(d *denc.Decoder) Filter {
	h := d.BeginStruct(4)
	var f Filter
	f.Prefix = d.String()
	if h.Version >= 2 {
		f.Tags = tags.Decode(d)
	}
	if h.Version >= 3 {
		f.Flags = d.U32()
	}
	if h.Version >= 4 {
		f.SizeGT = d.String()
		f.SizeLT = d.String()
	}
	d.EndStruct(h)
	return f
}

// Encode is LCRule::encode
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.h#L423-L436 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.h#L423-L436 at v20.2.4):
// ENCODE_START(6, 1), id, prefix, status, the three expirations,
// dm_expiration, the filter, then the transitions and the noncurrent
// transitions, each map in key order.
func (r Rule) Encode(e *denc.Encoder, rel denc.Release) {
	f := e.BeginStruct(6, 1)
	e.String(r.ID)
	e.String(r.Prefix)
	e.String(r.Status)
	r.Expiration.Encode(e, rel)
	r.NoncurExpiration.Encode(e, rel)
	r.MPExpiration.Encode(e, rel)
	e.Bool(r.DMExpiration)
	r.Filter.Encode(e, rel)
	encodeTransitions(e, r.Transitions, rel)
	encodeTransitions(e, r.NoncurTransitions, rel)
	e.EndStruct(f)
}

func encodeTransitions(e *denc.Encoder, m map[string]Transition, rel denc.Release) {
	denc.EncodeMap(e, m, (*denc.Encoder).String, func(e *denc.Encoder, t Transition) { t.Encode(e, rel) })
}

// DecodeRule is LCRule::decode
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.h#L437-L460 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.h#L437-L460 at v20.2.4):
// DECODE_START_LEGACY_COMPAT_LEN(6, 1, 1), whose v0 carries neither compat
// nor length; id, prefix, status and the expiration, then the noncurrent
// expiration from v2, the multipart one from v3, dm_expiration from v4, the
// filter from v5 and the two transition maps from v6. A map of LCTransition
// decodes each value through operator[], so of a repeated key the last
// value is kept.
func DecodeRule(d *denc.Decoder) Rule {
	h := d.BeginStructLegacy(6, 1, 1, 0)
	var r Rule
	r.ID = d.String()
	r.Prefix = d.String()
	r.Status = d.String()
	r.Expiration = DecodeExpiration(d)
	if h.Version >= 2 {
		r.NoncurExpiration = DecodeExpiration(d)
	}
	if h.Version >= 3 {
		r.MPExpiration = DecodeExpiration(d)
	}
	if h.Version >= 4 {
		r.DMExpiration = d.Bool()
	}
	if h.Version >= 5 {
		r.Filter = DecodeFilter(d)
	}
	if h.Version >= 6 {
		r.Transitions = denc.DecodeMapLast(d, (*denc.Decoder).String, DecodeTransition)
		r.NoncurTransitions = denc.DecodeMapLast(d, (*denc.Decoder).String, DecodeTransition)
	}
	d.EndStruct(h)
	return r
}

func byID(a, b Rule) int { return cmp.Compare(a.ID, b.ID) }

// ordered is c's rules in rule_map order, sorting a copy when a caller built
// Rules out of it.
func (c Configuration) ordered() []Rule {
	if slices.IsSortedFunc(c.Rules, byID) {
		return c.Rules
	}
	rules := slices.Clone(c.Rules)
	slices.SortStableFunc(rules, byID)
	return rules
}

// Encode is RGWLifecycleConfiguration::encode
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.h#L529-L533 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.h#L529-L533 at v20.2.4),
// the bytes radosgw stores under user.rgw.lc: ENCODE_START(1, 1) around the
// rule_map, a u32 count then each rule under its ID, in multimap order.
func (c Configuration) Encode(e *denc.Encoder, rel denc.Release) {
	f := e.BeginStruct(1, 1)
	denc.EncodeSlice(e, c.ordered(), func(e *denc.Encoder, r Rule) {
		e.String(r.ID)
		r.Encode(e, rel)
	})
	e.EndStruct(f)
}

// DecodeConfiguration is RGWLifecycleConfiguration::decode
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.h#L534-L543 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.h#L534-L543 at v20.2.4):
// DECODE_START_LEGACY_COMPAT_LEN(1, 1, 1), whose v0 carries neither compat
// nor length, around the rule_map. A std::multimap inserts each rule after
// those whose keys do not sort after its own, so the rules come out sorted
// by key with equal keys in stored order. The key is the rule's ID in every
// configuration radosgw writes, and Rules keeps the rule alone, so Encode
// writes a stored key that differed from its rule's ID as the ID.
func DecodeConfiguration(d *denc.Decoder) Configuration {
	type entry struct {
		key  string
		rule Rule
	}
	h := d.BeginStructLegacy(1, 1, 1, 0)
	entries := denc.DecodeSlice(d, func(d *denc.Decoder) entry {
		k := d.String()
		return entry{key: k, rule: DecodeRule(d)}
	})
	d.EndStruct(h)
	if d.Err() != nil || len(entries) == 0 {
		return Configuration{}
	}
	slices.SortStableFunc(entries, func(a, b entry) int { return cmp.Compare(a.key, b.key) })
	c := Configuration{Rules: make([]Rule, len(entries))}
	for i := range entries {
		c.Rules[i] = entries[i].rule
	}
	return c
}

// Decode reads a user.rgw.lc attr as RGWGetLC_ObjStore_S3::execute reads it
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3688-L3706 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4139-L4157 at v20.2.4):
// one configuration from the attr's start, the bytes after it unread. An
// attr that does not decode is the error, which radosgw answers with EIO.
func Decode(b []byte) (Configuration, error) {
	d := denc.NewDecoder(b)
	c := DecodeConfiguration(d)
	if err := d.Err(); err != nil {
		return Configuration{}, fmt.Errorf("decoding the lifecycle configuration: %w", err)
	}
	return c, nil
}
