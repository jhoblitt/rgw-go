package meta

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/jhoblitt/rgw-go/internal/denc"
)

// ObjectRetention is RGWObjectRetention (rgw_object_lock.h:151-197 at v19.2.6
// and v20.2.4): an object's retention mode and the date it is retained until.
type ObjectRetention struct {
	Mode        string
	RetainUntil time.Time
}

// Encode mirrors RGWObjectRetention::encode, ENCODE_START(2, 1): the mode,
// the date as a real_time, and the date again through round_trip_encode,
// which writes its u64 nanosecond count (include/encoding.h:393-420 at
// v19.2.6, :394-421 at v20.2.4) and so keeps the seconds past 2106 that the
// first form's u32 drops.
func (o ObjectRetention) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(2, 1)
	e.String(o.Mode)
	e.Time(o.RetainUntil)
	e.U64(realTimeNanos(o.RetainUntil))
	e.EndStruct(f)
}

// DecodeObjectRetention mirrors RGWObjectRetention::decode, DECODE_START(2):
// from version 2 the date is the nanosecond count's, whatever the first form
// said.
func DecodeObjectRetention(d *denc.Decoder) ObjectRetention {
	h := d.BeginStruct(2)
	var o ObjectRetention
	o.Mode = d.String()
	o.RetainUntil = d.Time()
	if h.Version >= 2 {
		o.RetainUntil = realTimeFromNanos(d.U64())
	}
	d.EndStruct(h)
	return o
}

// realTimeNanos is a real_time's count of nanoseconds since the epoch, which
// is a u64; the zero time.Time is the epoch, as denc writes it.
func realTimeNanos(t time.Time) uint64 {
	if t.IsZero() {
		return 0
	}
	return uint64(t.Unix())*uint64(time.Second) + uint64(t.Nanosecond()) //nolint:gosec // a real_time's count is a u64, wrapping as C++ does
}

// realTimeFromNanos is the instant n nanoseconds after the epoch in UTC, the
// zero time.Time for the epoch itself, as denc reads an unset time.
func realTimeFromNanos(n uint64) time.Time {
	if n == 0 {
		return time.Time{}
	}
	sec := n / uint64(time.Second)
	return time.Unix(int64(sec), int64(n-sec*uint64(time.Second))).UTC() //nolint:gosec // seconds of a u64 nanosecond count fit an int64
}

// ObjectLegalHold is RGWObjectLegalHold (rgw_object_lock.h:199-230 at
// v19.2.6 and v20.2.4): an object's legal hold status.
type ObjectLegalHold struct{ Status string }

// Encode mirrors RGWObjectLegalHold::encode, ENCODE_START(1, 1).
func (l ObjectLegalHold) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	e.String(l.Status)
	e.EndStruct(f)
}

// DecodeObjectLegalHold mirrors RGWObjectLegalHold::decode, DECODE_START(1).
func DecodeObjectLegalHold(d *denc.Decoder) ObjectLegalHold {
	h := d.BeginStruct(1)
	l := ObjectLegalHold{Status: d.String()}
	d.EndStruct(h)
	return l
}

// DefaultRetention is DefaultRetention (rgw_object_lock.h:12-55 at v19.2.7,
// :11-52 at v20.2.4): the retention mode and period a bucket's object lock
// rule gives the objects written to it, in days or in years.
type DefaultRetention struct {
	Mode  string
	Days  int32
	Years int32
}

// Encode mirrors DefaultRetention::encode, ENCODE_START(1, 1).
func (r DefaultRetention) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	e.String(r.Mode)
	e.I32(r.Days)
	e.I32(r.Years)
	e.EndStruct(f)
}

// DecodeDefaultRetention mirrors DefaultRetention::decode, DECODE_START(1).
func DecodeDefaultRetention(d *denc.Decoder) DefaultRetention {
	h := d.BeginStruct(1)
	var r DefaultRetention
	r.Mode = d.String()
	r.Days = d.I32()
	r.Years = d.I32()
	d.EndStruct(h)
	return r
}

// MarshalJSON writes Dump's members as JSON.
func (r DefaultRetention) MarshalJSON() ([]byte, error) {
	if r.Days > 0 {
		return json.Marshal(struct {
			Mode string `json:"mode"`
			Days int32  `json:"days"`
		}{r.Mode, r.Days})
	}
	return json.Marshal(struct {
		Mode  string `json:"mode"`
		Years int32  `json:"years"`
	}{r.Mode, r.Years})
}

// UnmarshalJSON is DefaultRetention::decode_json at v19.2.7
// (rgw_object_lock.cc:8-12): mode, days and years, an absent one its zero
// value.
func (r *DefaultRetention) UnmarshalJSON(data []byte) error {
	f, err := newJSONFields(data)
	if err != nil {
		return err
	}
	var out DefaultRetention
	out.Mode = f.str("mode")
	out.Days = f.int32v("days")
	out.Years = f.int32v("years")
	if f.err != nil {
		return f.err
	}
	*r = out
	return nil
}

// ObjectLockRule is ObjectLockRule (rgw_object_lock.h:57-92 at v19.2.7,
// :54-88 at v20.2.4): a bucket's default retention.
type ObjectLockRule struct{ DefaultRetention DefaultRetention }

// Encode mirrors ObjectLockRule::encode, ENCODE_START(1, 1).
func (r ObjectLockRule) Encode(e *denc.Encoder, rel denc.Release) {
	f := e.BeginStruct(1, 1)
	r.DefaultRetention.Encode(e, rel)
	e.EndStruct(f)
}

// DecodeObjectLockRule mirrors ObjectLockRule::decode, DECODE_START(1).
func DecodeObjectLockRule(d *denc.Decoder) ObjectLockRule {
	h := d.BeginStruct(1)
	r := ObjectLockRule{DefaultRetention: DecodeDefaultRetention(d)}
	d.EndStruct(h)
	return r
}

// MarshalJSON writes Dump's members as JSON.
func (r ObjectLockRule) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		DefaultRetention DefaultRetention `json:"defaultRetention"`
	}{r.DefaultRetention})
}

// UnmarshalJSON is ObjectLockRule::decode_json at v19.2.7
// (rgw_object_lock.cc:44-46).
func (r *ObjectLockRule) UnmarshalJSON(data []byte) error {
	f, err := newJSONFields(data)
	if err != nil {
		return err
	}
	var out ObjectLockRule
	f.decode("defaultRetention", &out.DefaultRetention)
	if f.err != nil {
		return f.err
	}
	*r = out
	return nil
}

// ObjectLock is RGWObjectLock (rgw_object_lock.h:94-154 at v19.2.7, :90-149
// at v20.2.4), a bucket's object lock configuration, whose Rule is nil when
// rule_exist is false. The zero value is disabled; RGWObjectLock() is
// enabled with no rule.
type ObjectLock struct {
	Enabled bool
	Rule    *ObjectLockRule
}

// Encode mirrors RGWObjectLock::encode, ENCODE_START(1, 1).
func (l ObjectLock) Encode(e *denc.Encoder, rel denc.Release) {
	f := e.BeginStruct(1, 1)
	e.Bool(l.Enabled)
	e.Bool(l.Rule != nil)
	if l.Rule != nil {
		l.Rule.Encode(e, rel)
	}
	e.EndStruct(f)
}

// DecodeObjectLock mirrors RGWObjectLock::decode, DECODE_START(1).
func DecodeObjectLock(d *denc.Decoder) ObjectLock {
	h := d.BeginStruct(objLockVersion)
	var l ObjectLock
	l.Enabled = d.Bool()
	if d.Bool() {
		r := DecodeObjectLockRule(d)
		l.Rule = &r
	}
	d.EndStruct(h)
	return l
}

// MarshalJSON writes Dump's members as JSON.
func (l ObjectLock) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Enabled   bool            `json:"enabled,omitzero"`
		RuleExist bool            `json:"rule_exist"`
		Rule      *ObjectLockRule `json:"rule,omitzero"`
	}{l.Enabled, l.Rule != nil, l.Rule})
}

// UnmarshalJSON is RGWObjectLock::decode_json at v19.2.7
// (rgw_object_lock.cc:65-71): the rule only when rule_exist. An absent
// enabled is false, unlike RGWObjectLock()'s.
func (l *ObjectLock) UnmarshalJSON(data []byte) error {
	f, err := newJSONFields(data)
	if err != nil {
		return err
	}
	out := ObjectLock{Enabled: f.boolean("enabled")}
	if f.boolean("rule_exist") {
		out.Rule = &ObjectLockRule{}
		f.decode("rule", out.Rule)
	}
	if f.err != nil {
		return f.err
	}
	*l = out
	return nil
}

// ObjectLock is ObjLock decoded while the flags enable object lock, nil
// being RGWObjectLock(), and the zero ObjectLock without the flag, as the
// instance then stores none.
func (b BucketInfo) ObjectLock() (ObjectLock, error) {
	if !b.ObjLockEnabled() {
		return ObjectLock{}, nil
	}
	raw := b.ObjLock
	if raw == nil {
		raw = defaultObjLock
	}
	d := denc.NewDecoder(raw)
	l := DecodeObjectLock(d)
	if err := d.Err(); err != nil {
		return ObjectLock{}, fmt.Errorf("decoding the object lock configuration: %w", err)
	}
	return l, nil
}

// SetObjectLock sets ObjLock to l's encoding, which is written only while the
// flags enable object lock; the flags are the caller's. An ObjLock nobody
// sets keeps the bytes it was decoded from.
func (b *BucketInfo) SetObjectLock(l ObjectLock) {
	e := denc.NewEncoder()
	// RGWObjectLock encodes the same at every release.
	l.Encode(e, denc.Squid)
	b.ObjLock = RawStruct(e.Bytes())
}
