package lock

import (
	"cmp"
	"encoding/binary"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jhoblitt/rgw-go/internal/cls/internal/clsutil"
	"github.com/jhoblitt/rgw-go/internal/denc"
)

// Type is ClsLockType (cls_lock_types.h). TypeExclusiveEphemeral is decoded
// but never requested by rgw-go: the class removes an ephemeral lock's object
// when the last holder expires, and get_info and assert_locked, registered
// read-only, then fail with EIO instead of reporting it (tracker #80993).
type Type uint8

// The ClsLockType values. The class refuses any other with EINVAL.
const (
	TypeNone               Type = 0
	TypeExclusive          Type = 1
	TypeShared             Type = 2
	TypeExclusiveEphemeral Type = 3
)

// Flags are the LOCK_FLAG_* bits: MayRenew makes a lock the caller already
// holds idempotent; MustRenew fails with ENOENT unless the caller holds it.
// Both set is EINVAL (lock_obj, cls_lock.cc:145-163).
type Flags uint8

// The LOCK_FLAG_* bits.
const (
	FlagMayRenew  Flags = 0x1
	FlagMustRenew Flags = 0x2
)

// EntityName is entity_name_t: the holder's type and number, DENC'd as u8
// then i64 (msg_types.h:93-96 at v19.2.6). A client's number is its global
// id, the seam's Cluster.InstanceID.
type EntityName struct {
	Type uint8
	Num  int64
}

// EntityTypeClient is CEPH_ENTITY_TYPE_CLIENT (msgr.h:94).
const EntityTypeClient uint8 = 0x08

// entityTypeNames is ceph_entity_type_name (ceph_strings.cc:8-19).
var entityTypeNames = map[uint8]string{0x01: "mon", 0x02: "mds", 0x04: "osd", 0x08: "client", 0x10: "mgr", 0x20: "auth"}

// parseTypes are the prefixes entity_name_t::parse accepts
// (msg_types.cc:15-45), which leave out auth.
var parseTypes = map[string]uint8{"mon": 0x01, "osd": 0x04, "mds": 0x02, "client": 0x08, "mgr": 0x10}

// Encode mirrors entity_name_t's DENC: the type, then the number.
func (n EntityName) Encode(e *denc.Encoder, _ denc.Release) {
	e.U8(n.Type)
	e.I64(n.Num)
}

// DecodeEntityName mirrors entity_name_t's DENC.
func DecodeEntityName(d *denc.Decoder) EntityName {
	return EntityName{Type: d.U8(), Num: d.I64()}
}

// String is entity_name_t's operator<<: "<type>.<num>", e.g. "client.4123",
// with "?" for a negative number; ParseEntityName inverts it, which is how a
// Locker's Client string from the seam's ListLockers becomes a break_lock
// argument.
func (n EntityName) String() string {
	name, ok := entityTypeNames[n.Type]
	if !ok {
		name = "unknown"
	}
	if n.Num < 0 {
		return name + ".?"
	}
	return name + "." + strconv.FormatInt(n.Num, 10)
}

// ParseEntityName is entity_name_t::parse: one of mon, osd, mds, client or
// mgr, a dot, then a decimal number that may carry a sign.
func ParseEntityName(s string) (EntityName, bool) {
	prefix, num, ok := strings.Cut(s, ".")
	if !ok {
		return EntityName{}, false
	}
	typ, ok := parseTypes[prefix]
	if !ok {
		return EntityName{}, false
	}
	n, err := strconv.ParseInt(num, 10, 64)
	if err != nil {
		return EntityName{}, false
	}
	return EntityName{Type: typ, Num: n}, true
}

// compareEntityNames is entity_name_t's operator<: the type, then the number.
func compareEntityNames(a, b EntityName) int {
	return cmp.Or(cmp.Compare(a.Type, b.Type), cmp.Compare(a.Num, b.Num))
}

// encodeUtime writes d as a utime_t: u32 seconds, then u32 nanoseconds.
func encodeUtime(e *denc.Encoder, d time.Duration) {
	e.U32(uint32(d / time.Second)) //nolint:gosec // utime_t's seconds are a u32
	e.U32(uint32(d % time.Second)) //nolint:gosec // within [0, 1e9) for a non-negative duration
}

func decodeUtime(d *denc.Decoder) time.Duration {
	s := d.U32()
	ns := d.U32()
	return time.Duration(s)*time.Second + time.Duration(ns)
}

// LockOp is cls_lock_lock_op, ENCODE_START(1, 1): name, type as u8, cookie,
// tag, description, duration as utime_t (u32 seconds, u32 nanoseconds), flags
// as u8. Duration zero means the lock never expires.
type LockOp struct { //nolint:revive // named for cls_lock_lock_op, beside UnlockOp, BreakOp and AssertOp
	Name        string
	Type        Type
	Cookie      string
	Tag         string
	Description string
	Duration    time.Duration
	Flags       Flags
}

// Encode mirrors cls_lock_lock_op::encode, ENCODE_START(1, 1).
func (o LockOp) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	e.String(o.Name)
	e.U8(uint8(o.Type))
	e.String(o.Cookie)
	e.String(o.Tag)
	e.String(o.Description)
	encodeUtime(e, o.Duration)
	e.U8(uint8(o.Flags))
	e.EndStruct(f)
}

// DecodeLockOp mirrors cls_lock_lock_op::decode,
// DECODE_START_LEGACY_COMPAT_LEN(1, 1, 1), as every decoder here does.
func DecodeLockOp(d *denc.Decoder) LockOp {
	h := d.BeginStructLegacy(1, 1, 1, 0)
	o := LockOp{Name: d.String(), Type: Type(d.U8()), Cookie: d.String(), Tag: d.String(), Description: d.String()}
	o.Duration = decodeUtime(d)
	o.Flags = Flags(d.U8())
	d.EndStruct(h)
	return o
}

// UnlockOp is cls_lock_unlock_op: name, cookie.
type UnlockOp struct{ Name, Cookie string }

// Encode mirrors cls_lock_unlock_op::encode, ENCODE_START(1, 1).
func (o UnlockOp) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	e.String(o.Name)
	e.String(o.Cookie)
	e.EndStruct(f)
}

// DecodeUnlockOp mirrors cls_lock_unlock_op::decode.
func DecodeUnlockOp(d *denc.Decoder) UnlockOp {
	h := d.BeginStructLegacy(1, 1, 1, 0)
	o := UnlockOp{Name: d.String(), Cookie: d.String()}
	d.EndStruct(h)
	return o
}

// BreakOp is cls_lock_break_op: name, locker, cookie (in that order).
type BreakOp struct {
	Name   string
	Locker EntityName
	Cookie string
}

// Encode mirrors cls_lock_break_op::encode, ENCODE_START(1, 1).
func (o BreakOp) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(1, 1)
	e.String(o.Name)
	o.Locker.Encode(e, r)
	e.String(o.Cookie)
	e.EndStruct(f)
}

// DecodeBreakOp mirrors cls_lock_break_op::decode.
func DecodeBreakOp(d *denc.Decoder) BreakOp {
	h := d.BeginStructLegacy(1, 1, 1, 0)
	o := BreakOp{Name: d.String(), Locker: DecodeEntityName(d), Cookie: d.String()}
	d.EndStruct(h)
	return o
}

// AssertOp is cls_lock_assert_op: name, type as u8, cookie, tag.
type AssertOp struct {
	Name   string
	Type   Type
	Cookie string
	Tag    string
}

// Encode mirrors cls_lock_assert_op::encode, ENCODE_START(1, 1).
func (o AssertOp) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	e.String(o.Name)
	e.U8(uint8(o.Type))
	e.String(o.Cookie)
	e.String(o.Tag)
	e.EndStruct(f)
}

// DecodeAssertOp mirrors cls_lock_assert_op::decode.
func DecodeAssertOp(d *denc.Decoder) AssertOp {
	h := d.BeginStructLegacy(1, 1, 1, 0)
	o := AssertOp{Name: d.String(), Type: Type(d.U8()), Cookie: d.String(), Tag: d.String()}
	d.EndStruct(h)
	return o
}

// GetInfoOp is cls_lock_get_info_op: name.
type GetInfoOp struct{ Name string }

// Encode mirrors cls_lock_get_info_op::encode, ENCODE_START(1, 1).
func (o GetInfoOp) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	e.String(o.Name)
	e.EndStruct(f)
}

// DecodeGetInfoOp mirrors cls_lock_get_info_op::decode.
func DecodeGetInfoOp(d *denc.Decoder) GetInfoOp {
	h := d.BeginStructLegacy(1, 1, 1, 0)
	o := GetInfoOp{Name: d.String()}
	d.EndStruct(h)
	return o
}

// LockerID is locker_id_t: the holder's entity name and cookie.
type LockerID struct {
	Locker EntityName
	Cookie string
}

// Encode mirrors locker_id_t::encode, ENCODE_START(1, 1).
func (id LockerID) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(1, 1)
	id.Locker.Encode(e, r)
	e.String(id.Cookie)
	e.EndStruct(f)
}

// DecodeLockerID mirrors locker_id_t::decode.
func DecodeLockerID(d *denc.Decoder) LockerID {
	h := d.BeginStructLegacy(1, 1, 1, 0)
	id := LockerID{Locker: DecodeEntityName(d), Cookie: d.String()}
	d.EndStruct(h)
	return id
}

// compareLockerIDs is locker_id_t::operator<, the order std::map encodes the
// holders in: the entity name, then the cookie.
func compareLockerIDs(a, b LockerID) int {
	return cmp.Or(compareEntityNames(a.Locker, b.Locker), strings.Compare(a.Cookie, b.Cookie))
}

// LockerInfo is locker_info_t. Addr is the holder's entity_addr_t as the
// class encoded it for a current client: the marker byte 1, then a framed
// struct. rgw-go carries it opaquely and re-encodes it byte for byte; a
// legacy encoding, which the class writes only for a client without
// CEPH_FEATURE_MSG_ADDR2, decodes to the form the class would have written.
type LockerInfo struct {
	Expiration  time.Time
	Addr        []byte
	Description string
}

// Encode mirrors locker_info_t::encode, ENCODE_START(1, 1).
func (li LockerInfo) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	e.Time(li.Expiration)
	e.Raw(li.Addr)
	e.String(li.Description)
	e.EndStruct(f)
}

// DecodeLockerInfo mirrors locker_info_t::decode.
func DecodeLockerInfo(d *denc.Decoder) LockerInfo {
	h := d.BeginStructLegacy(1, 1, 1, 0)
	li := LockerInfo{Expiration: d.Time()}
	li.Addr = decodeAddr(d)
	li.Description = d.String()
	d.EndStruct(h)
	return li
}

// The entity_addr_t encoding (msg_types.h, entity_addr_t::encode and decode).
const (
	addrMarkerLegacy = 0
	addrMarker       = 1
	// legacyAddrLen follows the marker: a second marker byte and a u16 that
	// make up the u32 0, the nonce, then sockaddr_storage's 128 bytes.
	legacyAddrLen = 3 + 4 + 128

	addrTypeNone   = 0
	addrTypeLegacy = 1

	familyUnspec = 0
	familyInet   = 2
	familyInet6  = 10
	sockaddrIn   = 16 // sizeof(sockaddr_in)
	sockaddrIn6  = 28 // sizeof(sockaddr_in6), and of entity_addr_t's union
)

// decodeAddr reads an entity_addr_t and returns it in the form the class
// writes for a current client. It fails as entity_addr_t::decode does on a
// marker other than 0 or 1 and on a struct whose compat exceeds 1, which
// DECODE_START(1) refuses.
func decodeAddr(d *denc.Decoder) []byte {
	switch marker := d.U8(); marker {
	case addrMarker:
		framed := clsutil.ReadFramed(d)
		if framed == nil {
			return nil
		}
		if framed[1] > 1 {
			d.Fail(fmt.Errorf("%w: entity_addr_t compat %d, decoder 1", denc.ErrIncompatible, framed[1]))
			return nil
		}
		return append([]byte{addrMarker}, framed...)
	case addrMarkerLegacy:
		return legacyAddr(d.Raw(legacyAddrLen))
	default:
		d.Fail(fmt.Errorf("%w: entity_addr_t marker %d", denc.ErrMalformed, marker))
		return nil
	}
}

// legacyAddr converts what follows a legacy entity_addr_t's marker to the
// msgr2-era form: decode_legacy_addr_after_marker keeps the nonce and, for
// AF_INET and AF_INET6 alone, the sockaddr, typed TYPE_LEGACY; anything else
// leaves an AF_UNSPEC address typed TYPE_NONE. encode then writes the
// sockaddr's length for its family, the family little-endian, and the rest of
// the sockaddr.
func legacyAddr(b []byte) []byte {
	if b == nil {
		return nil
	}
	nonce := binary.LittleEndian.Uint32(b[3:7])
	ss := b[7:]
	family := binary.BigEndian.Uint16(ss)
	typ, size := uint32(addrTypeLegacy), sockaddrIn6
	switch family {
	case familyInet:
		size = sockaddrIn
	case familyInet6:
	default:
		family, typ, ss = familyUnspec, addrTypeNone, make([]byte, sockaddrIn6)
	}
	e := denc.NewEncoder()
	e.U8(addrMarker)
	f := e.BeginStruct(1, 1)
	e.U32(typ)
	e.U32(nonce)
	e.U32(uint32(size))
	e.U16(family)
	e.Raw(ss[2:size])
	e.EndStruct(f)
	return e.Bytes()
}

// Info is lock_info_t / cls_lock_get_info_reply: the holders, the type, the tag.
type Info struct {
	Lockers map[LockerID]LockerInfo
	Type    Type
	Tag     string
}

// Encode mirrors cls_lock_get_info_reply::encode, ENCODE_START(1, 1), the
// holders in locker_id_t order.
func (info Info) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(1, 1)
	ids := slices.SortedFunc(maps.Keys(info.Lockers), compareLockerIDs)
	e.U32(uint32(len(ids))) //nolint:gosec // a lock's holders number far below 2^32
	for _, id := range ids {
		id.Encode(e, r)
		info.Lockers[id].Encode(e, r)
	}
	e.U8(uint8(info.Type))
	e.String(info.Tag)
	e.EndStruct(f)
}

// DecodeInfo mirrors cls_lock_get_info_reply::decode, and lock_info_t's,
// which is the same. Neither key nor value has denc traits, so the legacy
// std::map decode keeps the last of a repeated holder. A reply without
// holders decodes to an empty map rather than nil.
func DecodeInfo(d *denc.Decoder) Info {
	h := d.BeginStructLegacy(1, 1, 1, 0)
	lockers := denc.DecodeMapLast(d, DecodeLockerID, DecodeLockerInfo)
	info := Info{Lockers: lockers, Type: Type(d.U8()), Tag: d.String()}
	d.EndStruct(h)
	if info.Lockers == nil && d.Err() == nil {
		info.Lockers = map[LockerID]LockerInfo{}
	}
	return info
}
