package op

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jhoblitt/rgw-go/internal/cls/otp"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/formatter"
	"github.com/jhoblitt/rgw-go/internal/meta"
)

// The otp metadata section is radosgw's otp handler
// (RGWOTPMetadataHandler,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_otp.cc#L57-L158 at v19.2.7;
// MetadataHandler,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_otp.cc#L29-L139 at v20.2.4).
// Each store keeps the devices its own way and serves the section with
// OTPDocument and DecodeOTPDocument, so the document and its refusals are
// the same whichever store holds them.

// MaxOTPSeedLen is the longest seed, in bytes, an otp put writes. otp_set
// declares a stack array as long as each seed it parses
// (docs/ceph-upstream-bugs.md, "cls_otp's otp_set puts an array as long as
// the seed on the OSD's stack"), so the bound keeps a document from
// faulting the OSD. It is 8 times the 128 hex digits of RFC 6238's longest
// key, SHA-512's 64 bytes.
const MaxOTPSeedLen = 1024

// MaxOTPWindow is the largest window an otp put writes. Every check of a
// device runs liboath's walk over its window inside the class on the OSD,
// two HOTP codes for each step of it, so a wrong code costs 2·window+1
// codes, and nothing radosgw runs bounds the window
// (docs/ceph-upstream-bugs.md, "cls_otp verifies any stored window, an OSD
// CPU cost per check"). The bound is 512 times otp_info_t's default of 2,
// which radosgw-admin mfa create keeps unless told otherwise
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp_types.h#L40-L41 at v19.2.7 and v20.2.4),
// over eight hours each way at its 30 second step, and a wrong code then
// costs 2049 codes.
const MaxOTPWindow = 1024

// MaxOTPStepSize is the longest step, in seconds, an otp put writes. The
// class keeps a device's checks for one step, counting the step back from
// the OSD's clock in unsigned nanoseconds, so a step longer than the clock's
// seconds wraps that start past the clock and trims every check, which takes
// the class's five checks per step off the device
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L98-L127 at v19.2.7 and v20.2.4),
// and its lookup then never finds a check, so no code of the device verifies
// (docs/ceph-upstream-bugs.md, "cls_otp never verifies a device whose step
// is longer than the clock's seconds"). The bound is an hour, 120 times RFC 6238's 30 second step, otp_info_t's
// default.
const MaxOTPStepSize = 3600

var _ meta.Dumper = OTPDocument{}

// OTPDocument is the otp section's document, one object's devices.
// MarshalJSON and Dump write each device's seed, as radosgw's get does, so
// what they write is a credential; OTPInfo says which paths print one.
type OTPDocument struct {
	Devices []otp.OTPInfo
}

// Dump is RGWOTPMetadataObject::dump, encode_json("devices", devices): an
// array of "obj" sections, each a device as otp_info_t::dump writes it
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_otp.cc#L47-L49 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_otp.cc#L24-L26 at v20.2.4).
func (d OTPDocument) Dump(f formatter.Formatter, rel denc.Release) {
	f.OpenArraySection("devices")
	for _, dev := range d.Devices {
		f.OpenObjectSection("obj")
		dev.Dump(f, rel)
		f.CloseSection()
	}
	f.CloseSection()
}

// MarshalJSON is Dump in JSON, an empty list as [].
func (d OTPDocument) MarshalJSON() ([]byte, error) {
	devs := d.Devices
	if devs == nil {
		devs = []otp.OTPInfo{}
	}
	b, err := json.Marshal(struct {
		Devices []otp.OTPInfo `json:"devices"`
	}{devs})
	if err != nil {
		return nil, fmt.Errorf("dumping the otp document: %w", err)
	}
	return b, nil
}

// DecodeOTPDocument is the otp handler's get_meta_obj, decode_json of
// "devices" as a list of otp_info_t
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_otp.cc#L83-L92 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_otp.cc#L41-L52 at v20.2.4),
// with rgw-go's refusals of what a put would write. A document without
// devices has none, as decode_json resets an absent member. Every refusal
// is ErrInvalidArgument, and comes once every device is checked:
//   - a document that is not JSON, devices that are not an array, null
//     included, and a device that is no object, as the sections' documents
//     are refused ("The metadata sections write only what keeps a user or
//     bucket whole", docs/exclusions.md);
//   - a device OTPInfo's UnmarshalJSON refuses, a string member given null,
//     an object or an array among them;
//   - a device without an id, or whose seed the class would parse into an
//     empty key, whose codes anyone can make (EmptyOTPKey): radosgw-admin
//     mfa create refuses an empty id or seed
//     (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_admin.cc#L10799-L10807 at v19.2.7,
//     https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/radosgw-admin/radosgw-admin.cc#L11390-L11398 at v20.2.4),
//     but not the seeds liboath decodes into nothing;
//   - a device of a type other than TOTP or a seed type other than hex or
//     base32, as radosgw-admin mfa create refuses
//     (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_admin.cc#L10810-L10817 at v19.2.7,
//     https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/radosgw-admin/radosgw-admin.cc#L11401-L11408 at v20.2.4);
//   - a step_size of 0, by which the class divides
//     (docs/ceph-upstream-bugs.md, "cls_otp divides by a stored step_size
//     that is never validated"), or over MaxOTPStepSize;
//   - a window over MaxOTPWindow;
//   - a seed longer than MaxOTPSeedLen bytes, its decoded length.
//
// time_ofs takes any int: the class only subtracts it from the clock before
// it divides, and no walk or allocation grows with it, so an offset far from
// the clock only keeps the device's codes from verifying.
//
// No error names a value, so none carries a seed: encoding/json's syntax
// errors can quote the document around the fault, so they are replaced.
func DecodeOTPDocument(data []byte) (OTPDocument, error) {
	m, err := meta.JSONMembers(data)
	if err != nil {
		return OTPDocument{}, fmt.Errorf("%w: the otp document is not JSON", ErrInvalidArgument)
	}
	doc := OTPDocument{Devices: []otp.OTPInfo{}}
	v, ok := m["devices"]
	if !ok {
		return doc, nil
	}
	var elems []json.RawMessage
	if jsonKind(v.Raw) != '[' || json.Unmarshal(v.Raw, &elems) != nil {
		return OTPDocument{}, fmt.Errorf("%w: the otp document's devices are not an array", ErrInvalidArgument)
	}
	for i, raw := range elems {
		if jsonKind(raw) != '{' {
			return OTPDocument{}, fmt.Errorf("%w: otp device %d is not an object", ErrInvalidArgument, i)
		}
		var dev otp.OTPInfo
		if err := dev.UnmarshalJSON(raw); err != nil {
			return OTPDocument{}, fmt.Errorf("%w: otp device %d: %w", ErrInvalidArgument, i, err)
		}
		if why := refusedOTPDevice(dev); why != "" {
			return OTPDocument{}, fmt.Errorf("%w: otp device %d %s", ErrInvalidArgument, i, why)
		}
		doc.Devices = append(doc.Devices, dev)
	}
	return doc, nil
}

// EmptyOTPKey reports whether the class's parse_seed would turn seed, of
// type t, into a key of no bytes, whose codes anyone can make. It hands
// liboath the seed's c_str(), so oath_hex2bin reads the digits before the
// first NUL, and oath_base32_decode drops every space before it decodes
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L268-L299 at v19.2.7 and v20.2.4;
// https://github.com/jas4711/oath-toolkit/blob/oath-toolkit-2.6.9/liboath/coding.c#L104-L146 and
// https://github.com/jas4711/oath-toolkit/blob/oath-toolkit-2.6.9/liboath/coding.c#L208-L265).
// Both then answer OATH_OK with nothing decoded, for an empty seed, a hex
// seed led by a NUL and a base32 seed of nothing but spaces. A seed that
// gives fewer than one byte any other way, a lone hex digit, fails the
// parse.
func EmptyOTPKey(seed string, t otp.SeedType) bool {
	if t == otp.SeedBase32 {
		return strings.Trim(seed, " ") == ""
	}
	digits, _, _ := strings.Cut(seed, "\x00")
	return digits == ""
}

// refusedOTPDevice is why DecodeOTPDocument refuses a device it decoded, or
// "" for one it takes.
func refusedOTPDevice(dev otp.OTPInfo) string {
	switch {
	case dev.ID == "":
		return "has no id"
	case dev.Type != otp.TypeTOTP:
		return "is not TOTP"
	case dev.SeedType != otp.SeedHex && dev.SeedType != otp.SeedBase32:
		return "has a seed type other than hex or base32"
	case EmptyOTPKey(dev.Seed, dev.SeedType):
		return "has a seed that parses into no key"
	case dev.StepSize == 0:
		return "has a step_size of 0"
	case dev.StepSize > MaxOTPStepSize:
		return fmt.Sprintf("has a step_size over %d seconds", MaxOTPStepSize)
	case dev.Window > MaxOTPWindow:
		return fmt.Sprintf("has a window over %d steps", MaxOTPWindow)
	case len(dev.Seed) > MaxOTPSeedLen:
		return fmt.Sprintf("has a seed over %d bytes", MaxOTPSeedLen)
	}
	return ""
}
