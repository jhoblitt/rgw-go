package fakerados

import (
	"encoding/base32"
	"encoding/hex"
	"maps"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/jhoblitt/rgw-go/internal/cls/otp"
	"github.com/jhoblitt/rgw-go/internal/denc"
)

// OTPMethods are every method OTPClassAt emulates, in name order, which
// RegisterClassMethods takes with the class and OTPWriteMethods.
var OTPMethods []string

// OTPWriteMethods are the methods OTPClassAt emulates that the otp class
// registers with CLS_METHOD_WR, in name order, which RegisterClassMethods
// takes with the class and OTPMethods.
var OTPWriteMethods []string

func init() {
	_, OTPMethods, OTPWriteMethods = assembleClass(otpClassTables(time.Now)...)
	tableClasses[otp.Class] = true
}

// otpClassTables are the otp class's method tables on the clock now: the
// admin methods here, and the check methods of cls_otp_check.go.
func otpClassTables(now func() time.Time) []methodTable {
	return []methodTable{otpAdminMethods(now), otpCheckMethods(now)}
}

// OTPClassAt emulates the otp class (src/cls/otp/cls_otp.cc, the same at
// v19.2.7 and v20.2.4) on the clock now, which stands for the OSD's
// real_clock::now(). Its methods are those of otpClassTables, and every
// other method is EOPNOTSUPP. It keeps a user's devices as the class does,
// in the object's omap: the omap key "header" holds otp_header, the set of
// device ids, and the key "otp/<id>" each device's otp_instance. It reads
// both from the stored object through ClassCall.StoredOmap, as
// cls_cxx_map_get_val does, so radosgw's metadata put, which removes and
// recreates the object before its otp_set in one op, still finds the
// devices stored before it.
func OTPClassAt(now func() time.Time) ClassFunc {
	methods, _, _ := assembleClass(otpClassTables(now)...)
	return methods.dispatch()
}

// OTPClass is OTPClassAt on time.Now.
func OTPClass() ClassFunc { return OTPClassAt(time.Now) }

// The methods OTPClassAt emulates of those that keep a user's devices,
// otp_set, otp_get, otp_remove and get_current_time. Each write flag is the
// method's CLS_METHOD_WR in the class's registrations
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L557-L575 at v19.2.7 and v20.2.4).
func otpAdminMethods(now func() time.Time) methodTable {
	return methodTable{
		otp.MethodSet:    {fn: noOutput(otpSet), write: true},
		otp.MethodGet:    {fn: otpGet},
		otp.MethodRemove: {fn: noOutput(otpRemove), write: true},
		otp.MethodGetCurrentTime: {fn: func(call *ClassCall) ([]byte, int32) {
			return otpGetCurrentTime(call, now)
		}},
	}
}

// The omap keys of the header and of a device's instance, which is the
// prefix and the device's id
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L45-L46 at v19.2.7 and v20.2.4).
const (
	otpHeaderKey = "header"
	otpKeyPrefix = "otp/"
)

// otpHeader is otp_header, ENCODE_START(1, 1): the ids of the object's
// devices, a set
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L48-L64 at v19.2.7 and v20.2.4).
type otpHeader struct {
	IDs map[string]bool
}

// Encode mirrors otp_header::encode: the set in order.
func (h otpHeader) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	denc.EncodeSlice(e, slices.Sorted(maps.Keys(h.IDs)), (*denc.Encoder).String)
	e.EndStruct(f)
}

// decodeOTPHeader mirrors otp_header::decode, DECODE_START(1); an id the
// bytes repeat is in the set once.
func decodeOTPHeader(d *denc.Decoder) otpHeader {
	f := d.BeginStruct(1)
	h := otpHeader{IDs: map[string]bool{}}
	for _, id := range denc.DecodeSlice(d, (*denc.Decoder).String) {
		h.IDs[id] = true
	}
	d.EndStruct(f)
	return h
}

// otpInstance is otp_instance, ENCODE_START(1, 1): a device, the checks of
// its codes in the current window, and the TOTP counter of its last
// successful check
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L66-L95 at v19.2.7 and v20.2.4).
// Its fields are exported so that fmt prints OTP through otp.OTPInfo's
// Format, which leaves the seed and seed_bin out.
type otpInstance struct {
	OTP         otp.OTPInfo
	LastChecks  []otp.OTPCheck
	LastSuccess uint64
}

// Encode mirrors otp_instance::encode.
func (i otpInstance) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(1, 1)
	i.OTP.Encode(e, r)
	denc.EncodeSlice(e, i.LastChecks, func(e *denc.Encoder, c otp.OTPCheck) { c.Encode(e, r) })
	e.U64(i.LastSuccess)
	e.EndStruct(f)
}

// decodeOTPInstance mirrors otp_instance::decode, DECODE_START(1).
func decodeOTPInstance(d *denc.Decoder) otpInstance {
	f := d.BeginStruct(1)
	i := otpInstance{
		OTP:         otp.DecodeOTPInfo(d),
		LastChecks:  denc.DecodeSlice(d, otp.DecodeOTPCheck),
		LastSuccess: d.U64(),
	}
	d.EndStruct(f)
	return i
}

// otpReadHeader is read_header: the stored header, an empty one when the
// object, its header key or the key's value is missing, and EIO for one that
// does not decode
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L224-L252 at v19.2.7 and v20.2.4).
func otpReadHeader(call *ClassCall) (h otpHeader, rval int32) {
	omap, _ := call.StoredOmap()
	b := omap[otpHeaderKey]
	if len(b) == 0 {
		return otpHeader{IDs: map[string]bool{}}, 0
	}
	d := denc.NewDecoder(b)
	h = decodeOTPHeader(d)
	if d.Err() != nil {
		return otpHeader{}, -int32(syscall.EIO)
	}
	return h, 0
}

// otpWriteHeader is write_header, which creates a missing object.
func otpWriteHeader(call *ClassCall, h otpHeader) {
	call.Create().Omap[otpHeaderKey] = encodeSquid(h)
}

// otpGetInstance is get_otp_instance: the stored device id names, ENOENT
// when the object holds none, and EIO for one that does not decode
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L171-L193 at v19.2.7 and v20.2.4).
func otpGetInstance(call *ClassCall, id string) (inst otpInstance, rval int32) {
	omap, _ := call.StoredOmap()
	b, ok := omap[otpKeyPrefix+id]
	if !ok {
		return otpInstance{}, -int32(syscall.ENOENT)
	}
	d := denc.NewDecoder(b)
	inst = decodeOTPInstance(d)
	if d.Err() != nil {
		return otpInstance{}, -int32(syscall.EIO)
	}
	return inst, 0
}

// otpWriteInstance is write_otp_instance: the device stored under its id,
// which creates a missing object.
func otpWriteInstance(call *ClassCall, inst otpInstance) {
	call.Create().Omap[otpKeyPrefix+inst.OTP.ID] = encodeSquid(inst)
}

// otpRemoveInstance is remove_otp_instance: the device's key removed, and
// ENOENT when an earlier step of the op removed the object, as the OSD's
// CEPH_OSD_OP_OMAPRMKEYS answers on an object that does not exist
// (https://github.com/ceph/ceph/blob/v19.2.7/src/osd/PrimaryLogPG.cc#L7959-L7972 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/osd/PrimaryLogPG.cc#L8036-L8049 at v20.2.4).
func otpRemoveInstance(call *ClassCall, id string) int32 {
	obj := call.Update()
	if obj == nil {
		return -int32(syscall.ENOENT)
	}
	delete(obj.Omap, otpKeyPrefix+id)
	return 0
}

// otpSet is otp_set_op
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L301-L348 at v19.2.7 and v20.2.4):
// each device replaces the otp_info_t of the one stored under its id, which
// keeps that one's checks and last success, with its seed_bin parsed from
// its seed; then the header lists every id it held and every id the request
// names, and is written even for a request with no devices. A seed that does
// not parse is EINVAL.
func otpSet(call *ClassCall) int32 {
	op, rval := decodeRequest(call.In, otp.DecodeSetOp)
	if rval < 0 {
		return rval
	}
	h, rval := otpReadHeader(call)
	if rval < 0 {
		return rval
	}
	for _, entry := range op.Entries {
		inst, rval := otpGetInstance(call, entry.ID)
		if rval < 0 && rval != -int32(syscall.ENOENT) {
			return rval
		}
		seedBin, ok := otpParseSeed(entry.Seed, entry.SeedType)
		if !ok {
			return -int32(syscall.EINVAL)
		}
		inst.OTP = entry
		inst.OTP.SeedBin = seedBin
		otpWriteInstance(call, inst)
		h.IDs[entry.ID] = true
	}
	otpWriteHeader(call, h)
	return 0
}

// otpGet is otp_get_op
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L396-L445 at v19.2.7 and v20.2.4):
// the devices of the ids the header lists, in its order for get_all, which
// ignores the request's ids, and otherwise in the request's order, an id
// named twice answered twice. An id the header lacks is skipped; a listed
// id whose device is missing fails the call with ENOENT.
func otpGet(call *ClassCall) (out []byte, rval int32) {
	op, rval := decodeRequest(call.In, otp.DecodeGetOp)
	if rval < 0 {
		return nil, rval
	}
	h, rval := otpReadHeader(call)
	if rval < 0 {
		return nil, rval
	}
	ids := op.IDs
	if op.GetAll {
		ids = slices.Sorted(maps.Keys(h.IDs))
	}
	var reply otp.GetReply
	for _, id := range ids {
		if !h.IDs[id] {
			continue
		}
		inst, rval := otpGetInstance(call, id)
		if rval < 0 {
			return nil, rval
		}
		reply.FoundEntries = append(reply.FoundEntries, inst.OTP)
	}
	return encodeSquid(reply), 0
}

// otpRemove is otp_remove_op
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L350-L394 at v19.2.7 and v20.2.4):
// the devices of the ids the header lists are removed with their ids, an id
// it lacks is skipped, and the header is written only when one was removed.
func otpRemove(call *ClassCall) int32 {
	op, rval := decodeRequest(call.In, otp.DecodeRemoveOp)
	if rval < 0 {
		return rval
	}
	h, rval := otpReadHeader(call)
	if rval < 0 {
		return rval
	}
	removed := false
	for _, id := range op.IDs {
		if !h.IDs[id] {
			continue
		}
		removed = true
		if rval := otpRemoveInstance(call, id); rval < 0 {
			return rval
		}
		delete(h.IDs, id)
	}
	if removed {
		otpWriteHeader(call, h)
	}
	return 0
}

// otpGetCurrentTime is otp_get_current_time_op, the class's clock
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L513-L531 at v19.2.7 and v20.2.4).
func otpGetCurrentTime(call *ClassCall, now func() time.Time) (out []byte, rval int32) {
	if _, rval := decodeRequest(call.In, otp.DecodeGetCurrentTimeOp); rval < 0 {
		return nil, rval
	}
	return encodeSquid(otp.GetCurrentTimeReply{Time: now()}), 0
}

// otpParseSeed is parse_seed: a base32 seed through oath_base32_decode, and a
// seed of any other type through oath_hex2bin, reporting false for one that
// does not parse, which the class answers EINVAL
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L268-L299 at v19.2.7 and v20.2.4).
func otpParseSeed(seed string, t otp.SeedType) ([]byte, bool) {
	if t == otp.SeedBase32 {
		return oathBase32Decode(seed)
	}
	return oathHex2Bin(seed)
}

// oathHex2Bin is liboath's oath_hex2bin given the seed's c_str(): the hex
// digits before the first NUL, in either case, two to a byte, and false for
// any other character or an odd count
// (https://github.com/jas4711/oath-toolkit/blob/oath-toolkit-2.6.9/liboath/coding.c#L33-L146).
func oathHex2Bin(seed string) ([]byte, bool) {
	digits, _, _ := strings.Cut(seed, "\x00")
	b, err := hex.DecodeString(digits)
	if err != nil {
		return nil, false
	}
	return b, true
}

// oathBase32Decode is liboath's oath_base32_decode given all of the seed's
// bytes: spaces dropped, letters upper-cased, the padding a final block of
// 2, 4, 5 or 7 characters lacks added, then gnulib's base32_decode_alloc
// without a context, which decodes each block of 8 characters on its own,
// so a padded block may precede another, and refuses a newline as any other
// character outside the alphabet
// (https://github.com/jas4711/oath-toolkit/blob/oath-toolkit-2.6.9/liboath/coding.c#L207-L266,
// and gnulib's copy in liboath,
// https://github.com/jas4711/oath-toolkit/blob/oath-toolkit-2.6.9/liboath/gl/base32.c#L358-L546).
// encoding/base32 decodes one block as decode_8 does, but strips newlines
// and refuses data after padding, so each block goes to it alone once it is
// known to hold none.
func oathBase32Decode(seed string) ([]byte, bool) {
	in := make([]byte, 0, len(seed)+6)
	for _, c := range []byte(seed) {
		switch {
		case c == ' ':
			continue
		case 'a' <= c && c <= 'z':
			c -= 'a' - 'A'
		}
		in = append(in, c)
	}
	switch len(in) % 8 {
	case 2:
		in = append(in, "======"...)
	case 4:
		in = append(in, "===="...)
	case 5:
		in = append(in, "==="...)
	case 7:
		in = append(in, '=')
	}
	out := []byte{}
	for block := range slices.Chunk(in, 8) {
		if len(block) < 8 || strings.ContainsAny(string(block), "\r\n") {
			return nil, false
		}
		var b [5]byte
		n, err := base32.StdEncoding.Decode(b[:], block)
		if err != nil {
			return nil, false
		}
		out = append(out, b[:n]...)
	}
	return out, true
}
