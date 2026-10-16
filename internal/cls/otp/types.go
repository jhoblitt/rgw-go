package otp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/rgwtext"
)

// Type is OTPType, the kind of password a device makes. It holds an int, as
// the C++ enum does after decode_json casts any int to it; the wire carries
// its low byte.
type Type int32

// The OTPType values. HOTP is unsupported: the class checks every device as
// TOTP, whatever its type.
const (
	TypeUnknown Type = 0
	TypeHOTP    Type = 1
	TypeTOTP    Type = 2
)

// SeedType is SeedType, how Seed spells the secret.
type SeedType uint8

// The SeedType values. The class parses any seed type but base32 as hex
// (parse_seed,
// https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L268-L299 at v19.2.7 and v20.2.4).
const (
	SeedUnknown SeedType = 0
	SeedHex     SeedType = 1
	SeedBase32  SeedType = 2
)

// CheckResult is OTPCheckResult, the outcome of one otp_check.
type CheckResult uint8

// The OTPCheckResult values.
const (
	CheckUnknown CheckResult = 0
	CheckSuccess CheckResult = 1
	CheckFail    CheckResult = 2
)

// OTPInfo is otp_info_t, one MFA device
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp_types.h#L31-L77 at v19.2.7 and v20.2.4).
//
// Seed and SeedBin are the device's secret: whoever reads either can make
// the user's MFA codes. SeedBin is the seed parsed, which otp_set computes
// and stores itself, so whatever a request carries there is replaced.
// MarshalJSON writes Seed, as radosgw's metadata get does, so treat that
// JSON as a credential.
//
// Format, String, GoString and LogValue leave both out, under every fmt verb
// and wherever fmt reaches the device through its methods: a pointer, a
// slice, an exported field. Three paths print them anyway, and no method
// here can close them, so callers keep devices off them:
//   - a struct that holds devices in an unexported field, whose fields fmt
//     prints without calling their methods, under %v and %+v as under any
//     other verb;
//   - %p on a value that is not a pointer, and %w on anything, which fmt
//     reports as bad verbs, printing the value's fields without calling a
//     method;
//   - slog's JSON handler, which marshals a value holding devices as JSON,
//     as LogValue applies only to the value logged itself.
//
// UnmarshalJSON's errors name a member, never a value, when it is handed a
// document itself. json.Unmarshal and json.Decoder check a document's syntax
// before they call it, so a malformed device, alone, in a slice or in an
// enclosing document, fails with encoding/json's own *json.SyntaxError,
// which can quote the bytes around the fault, the seed among them. A caller
// decoding devices that way replaces that error before it returns or logs
// it. A well-formed document they reject for a member's value fails with
// UnmarshalJSON's error, unwrapped.
//
// The C++ constructor's defaults, TOTP with a step of 30 seconds and a window
// of 2, are not the zero OTPInfo.
type OTPInfo struct { //nolint:revive // named for otp_info_t, the name radosgw's MFA code uses
	Type     Type
	ID       string
	Seed     string
	SeedType SeedType
	SeedBin  []byte
	TimeOfs  int32
	StepSize uint32
	Window   uint32
}

// Encode mirrors otp_info_t::encode, ENCODE_START(1, 1): the type and the
// seed type each cast to uint8_t.
func (i OTPInfo) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	e.U8(uint8(i.Type)) //nolint:gosec // otp_info_t::encode casts the enum to uint8_t
	e.String(i.ID)
	e.String(i.Seed)
	e.U8(uint8(i.SeedType))
	e.Bytes32(i.SeedBin)
	e.I32(i.TimeOfs)
	e.U32(i.StepSize)
	e.U32(i.Window)
	e.EndStruct(f)
}

// DecodeOTPInfo mirrors otp_info_t::decode, DECODE_START(1). Any type or
// seed type byte is kept.
func DecodeOTPInfo(d *denc.Decoder) OTPInfo {
	h := d.BeginStruct(1)
	i := OTPInfo{
		Type:     Type(d.U8()),
		ID:       d.String(),
		Seed:     d.String(),
		SeedType: SeedType(d.U8()),
		SeedBin:  d.Bytes32(),
		TimeOfs:  d.I32(),
		StepSize: d.U32(),
		Window:   d.U32(),
	}
	d.EndStruct(h)
	return i
}

// otpInfoJSON is otp_info_t::dump's members, in its order.
type otpInfoJSON struct {
	Type     int32  `json:"type"`
	ID       string `json:"id"`
	Seed     string `json:"seed"`
	SeedType string `json:"seed_type"`
	TimeOfs  int32  `json:"time_ofs"`
	StepSize uint32 `json:"step_size"`
	Window   uint32 `json:"window"`
}

// MarshalJSON is otp_info_t::dump
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp_types.cc#L30-L50 at v19.2.7 and v20.2.4):
// the type as an int, the seed type as "hex", "base32" or, for any other
// value, "unknown", and never SeedBin. It writes the seed.
func (i OTPInfo) MarshalJSON() ([]byte, error) {
	st := "unknown"
	switch i.SeedType {
	case SeedHex:
		st = "hex"
	case SeedBase32:
		st = "base32"
	}
	b, err := json.Marshal(otpInfoJSON{
		Type: int32(i.Type), ID: i.ID, Seed: i.Seed, SeedType: st,
		TimeOfs: i.TimeOfs, StepSize: i.StepSize, Window: i.Window,
	})
	if err != nil {
		return nil, fmt.Errorf("otp: dumping device: %w", err)
	}
	return b, nil
}

// errMalformedJSON stands for every syntax error in a document handed to
// UnmarshalJSON itself, whose text may quote the document around the fault,
// the seed among it. encoding/json never hands it a malformed one.
var errMalformedJSON = fmt.Errorf("%w: otp device: malformed JSON", meta.ErrBadJSONValue)

// UnmarshalJSON is otp_info_t::decode_json
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp_types.cc#L52-L71 at v19.2.7 and v20.2.4).
// JSONDecoder::decode_json resets a member the document lacks to its type's
// zero value
// (https://github.com/ceph/ceph/blob/v19.2.7/src/common/ceph_json.h#L334-L357 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/common/ceph_json.h#L339-L362 at v20.2.4),
// so every field is the document's: a type, step_size or window it lacks is
// 0, not the constructor's default. The type and time_ofs are read as ints
// and step_size and window as unsigned ints, a string's contents as readily
// as a number. The seed type is hex for "hex", base32 for "base32" and
// unknown for anything else. SeedBin is never read, so a decoded device has
// none, as radosgw's freshly built one has none. A string member given null,
// an object or an array is refused, as the metadata sections' documents are.
// Every failure it returns wraps meta.ErrBadJSONValue and names a member,
// never a value; through json.Unmarshal a syntax error is encoding/json's
// instead, as OTPInfo says.
func (i *OTPInfo) UnmarshalJSON(b []byte) error {
	m, err := meta.JSONMembers(b)
	if err != nil {
		return errMalformedJSON
	}
	r := jsonReader{m: m}
	out := OTPInfo{
		Type:     Type(r.int32v("type")),
		ID:       r.str("id"),
		Seed:     r.str("seed"),
		TimeOfs:  r.int32v("time_ofs"),
		StepSize: r.uint32v("step_size"),
		Window:   r.uint32v("window"),
	}
	switch r.str("seed_type") {
	case "hex":
		out.SeedType = SeedHex
	case "base32":
		out.SeedType = SeedBase32
	}
	if r.err != nil {
		return r.err
	}
	*i = out
	return nil
}

// String names the device without its seed or seed_bin.
func (i OTPInfo) String() string {
	return fmt.Sprintf("{Type:%d ID:%q SeedType:%d TimeOfs:%d StepSize:%d Window:%d}",
		i.Type, i.ID, i.SeedType, i.TimeOfs, i.StepSize, i.Window)
}

// GoString is String for %#v, so that form leaves the secret out too.
func (i OTPInfo) GoString() string { return "otp.OTPInfo" + i.String() }

// Format prints the device as String does, or as GoString does for %#v,
// under every verb fmt hands it: a string verb keeps its flags, width and
// precision, and any other verb is reported as fmt reports a verb a type does
// not take, with the String form in place of the fields.
func (i OTPInfo) Format(s fmt.State, verb rune) {
	switch {
	case verb == 'v' && s.Flag('#'):
		_, _ = fmt.Fprint(s, i.GoString())
	case verb == 'v' || verb == 's' || verb == 'q' || verb == 'x' || verb == 'X':
		_, _ = fmt.Fprintf(s, fmt.FormatString(s, verb), i.String())
	default:
		_, _ = fmt.Fprintf(s, "%%!%c(otp.OTPInfo=%s)", verb, i.String())
	}
}

// LogValue is the device for slog, without its seed or seed_bin.
func (i OTPInfo) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Int("type", int(i.Type)),
		slog.String("id", i.ID),
		slog.Int("seed_type", int(i.SeedType)),
		slog.Int("time_ofs", int(i.TimeOfs)),
		slog.Uint64("step_size", uint64(i.StepSize)),
		slog.Uint64("window", uint64(i.Window)),
	)
}

// cSpace is isspace in the C locale.
const cSpace = " \t\n\v\f\r"

// jsonReader reads members through decode_json_obj's conversions, keeping
// the first failure and returning zero values after it.
type jsonReader struct {
	m   map[string]meta.JSONMember
	err error
}

func (r *jsonReader) fail(name, why string) {
	if r.err == nil {
		r.err = fmt.Errorf("%w: otp device: %s: %s", meta.ErrBadJSONValue, name, why)
	}
}

func (r *jsonReader) member(name string) (meta.JSONMember, bool) {
	if r.err != nil {
		return meta.JSONMember{}, false
	}
	v, ok := r.m[name]
	return v, ok
}

// str is decode_json_obj(std::string&): a string's contents, or a number's
// or a boolean's text.
func (r *jsonReader) str(name string) string {
	v, ok := r.member(name)
	if !ok {
		return ""
	}
	if t := bytes.TrimLeft(v.Raw, " \t\r\n"); len(t) == 0 || t[0] == 'n' || t[0] == '{' || t[0] == '[' {
		r.fail(name, "not a string")
		return ""
	}
	return v.Text
}

// int32v is decode_json_obj(int&): strtol over the text as a C string,
// refused when it reads no digits, saturates, leaves anything but white
// space, or falls outside int's range
// (https://github.com/ceph/ceph/blob/v19.2.7/src/common/ceph_json.cc#L306-L332 and
// https://github.com/ceph/ceph/blob/v19.2.7/src/common/ceph_json.cc#L418-L429 at v19.2.7 and v20.2.4).
func (r *jsonReader) int32v(name string) int32 {
	v, ok := r.member(name)
	if !ok {
		return 0
	}
	s := rgwtext.CString(v.Text)
	n, end, erange := rgwtext.Strtoll(s)
	if end == 0 || erange || strings.TrimLeft(s[end:], cSpace) != "" || n > math.MaxInt32 || n < math.MinInt32 {
		r.fail(name, "not an int")
		return 0
	}
	return int32(n)
}

// uint32v is decode_json_obj(unsigned&): strtoul over the text as a C
// string, whose minus negates in unsigned arithmetic, refused as int32v
// refuses and above UINT_MAX
// (https://github.com/ceph/ceph/blob/v19.2.7/src/common/ceph_json.cc#L334-L360 and
// https://github.com/ceph/ceph/blob/v19.2.7/src/common/ceph_json.cc#L431-L442 at v19.2.7 and v20.2.4).
func (r *jsonReader) uint32v(name string) uint32 {
	v, ok := r.member(name)
	if !ok {
		return 0
	}
	s := rgwtext.CString(v.Text)
	n, end, erange := rgwtext.Strtoull(s)
	if end == 0 || erange || strings.TrimLeft(s[end:], cSpace) != "" || n > math.MaxUint32 {
		r.fail(name, "not an unsigned int")
		return 0
	}
	return uint32(n)
}

// OTPCheck is otp_check_t, one check the class records for a device
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp_types.h#L86-L107 at v19.2.7 and v20.2.4).
type OTPCheck struct { //nolint:revive // named for otp_check_t, beside OTPInfo
	Token     string
	Timestamp time.Time
	Result    CheckResult
}

// Encode mirrors otp_check_t::encode, ENCODE_START(1, 1): the timestamp as
// a real_time and the result cast to a char.
func (c OTPCheck) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	e.String(c.Token)
	e.Time(c.Timestamp)
	e.U8(uint8(c.Result))
	e.EndStruct(f)
}

// DecodeOTPCheck mirrors otp_check_t::decode, DECODE_START(1). Any result
// byte is kept.
func DecodeOTPCheck(d *denc.Decoder) OTPCheck {
	h := d.BeginStruct(1)
	c := OTPCheck{Token: d.String(), Timestamp: d.Time(), Result: CheckResult(d.U8())}
	d.EndStruct(h)
	return c
}

// The requests and replies of otp_set, otp_get, otp_remove and
// get_current_time, among cls_otp_set_otp_op to cls_otp_get_current_time_reply
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp_ops.h#L11-L167 at v19.2.7 and v20.2.4).
// Each is (1,1).

// SetOp is cls_otp_set_otp_op, the otp_set request.
type SetOp struct {
	Entries []OTPInfo
}

// Encode mirrors cls_otp_set_otp_op::encode, ENCODE_START(1, 1).
func (o SetOp) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(1, 1)
	encodeInfos(e, r, o.Entries)
	e.EndStruct(f)
}

// DecodeSetOp mirrors cls_otp_set_otp_op::decode, DECODE_START(1).
func DecodeSetOp(d *denc.Decoder) SetOp {
	h := d.BeginStruct(1)
	o := SetOp{Entries: denc.DecodeSlice(d, DecodeOTPInfo)}
	d.EndStruct(h)
	return o
}

// GetOp is cls_otp_get_otp_op, the otp_get request: every device when GetAll
// is set, IDs ignored, and otherwise the devices IDs names.
type GetOp struct {
	GetAll bool
	IDs    []string
}

// Encode mirrors cls_otp_get_otp_op::encode, ENCODE_START(1, 1).
func (o GetOp) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	e.Bool(o.GetAll)
	denc.EncodeSlice(e, o.IDs, (*denc.Encoder).String)
	e.EndStruct(f)
}

// DecodeGetOp mirrors cls_otp_get_otp_op::decode, DECODE_START(1).
func DecodeGetOp(d *denc.Decoder) GetOp {
	h := d.BeginStruct(1)
	o := GetOp{GetAll: d.Bool(), IDs: denc.DecodeSlice(d, (*denc.Decoder).String)}
	d.EndStruct(h)
	return o
}

// GetReply is cls_otp_get_otp_reply, the otp_get reply: the devices found,
// each with the seed_bin the class stored.
type GetReply struct {
	FoundEntries []OTPInfo
}

// Encode mirrors cls_otp_get_otp_reply::encode, ENCODE_START(1, 1).
func (o GetReply) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(1, 1)
	encodeInfos(e, r, o.FoundEntries)
	e.EndStruct(f)
}

// DecodeGetReply mirrors cls_otp_get_otp_reply::decode, DECODE_START(1).
func DecodeGetReply(d *denc.Decoder) GetReply {
	h := d.BeginStruct(1)
	o := GetReply{FoundEntries: denc.DecodeSlice(d, DecodeOTPInfo)}
	d.EndStruct(h)
	return o
}

func encodeInfos(e *denc.Encoder, r denc.Release, infos []OTPInfo) {
	denc.EncodeSlice(e, infos, func(e *denc.Encoder, i OTPInfo) { i.Encode(e, r) })
}

// RemoveOp is cls_otp_remove_otp_op, the otp_remove request.
type RemoveOp struct {
	IDs []string
}

// Encode mirrors cls_otp_remove_otp_op::encode, ENCODE_START(1, 1).
func (o RemoveOp) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	denc.EncodeSlice(e, o.IDs, (*denc.Encoder).String)
	e.EndStruct(f)
}

// DecodeRemoveOp mirrors cls_otp_remove_otp_op::decode, DECODE_START(1).
func DecodeRemoveOp(d *denc.Decoder) RemoveOp {
	h := d.BeginStruct(1)
	o := RemoveOp{IDs: denc.DecodeSlice(d, (*denc.Decoder).String)}
	d.EndStruct(h)
	return o
}

// GetCurrentTimeOp is cls_otp_get_current_time_op, the get_current_time
// request, a frame with no fields. The class decodes it, so an empty input
// fails with EINVAL.
type GetCurrentTimeOp struct{}

// Encode mirrors cls_otp_get_current_time_op::encode, ENCODE_START(1, 1).
func (GetCurrentTimeOp) Encode(e *denc.Encoder, _ denc.Release) {
	e.EndStruct(e.BeginStruct(1, 1))
}

// DecodeGetCurrentTimeOp mirrors cls_otp_get_current_time_op::decode,
// DECODE_START(1).
func DecodeGetCurrentTimeOp(d *denc.Decoder) GetCurrentTimeOp {
	d.EndStruct(d.BeginStruct(1))
	return GetCurrentTimeOp{}
}

// GetCurrentTimeReply is cls_otp_get_current_time_reply, the OSD's clock.
type GetCurrentTimeReply struct {
	Time time.Time
}

// Encode mirrors cls_otp_get_current_time_reply::encode, ENCODE_START(1, 1).
func (o GetCurrentTimeReply) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	e.Time(o.Time)
	e.EndStruct(f)
}

// DecodeGetCurrentTimeReply mirrors cls_otp_get_current_time_reply::decode,
// DECODE_START(1).
func DecodeGetCurrentTimeReply(d *denc.Decoder) GetCurrentTimeReply {
	h := d.BeginStruct(1)
	o := GetCurrentTimeReply{Time: d.Time()}
	d.EndStruct(h)
	return o
}
