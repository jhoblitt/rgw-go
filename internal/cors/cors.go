package cors

import (
	"fmt"
	"math"
	"slices"

	"github.com/jhoblitt/rgw-go/internal/denc"
)

// Methods is the methods a rule allows, a set of radosgw's RGW_CORS_* flags
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_cors.h#L22-L35 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cors.h#L22-L35 at v20.2.4).
// A stored rule may carry bits radosgw names no method for; they are kept.
type Methods uint8

// The RGW_CORS_* flags.
const (
	MethodGet    Methods = 0x01
	MethodPut    Methods = 0x02
	MethodHead   Methods = 0x04
	MethodPost   Methods = 0x08
	MethodDelete Methods = 0x10
	MethodCopy   Methods = 0x20
)

// MaxAgeInvalid is CORS_MAX_AGE_INVALID, the max age of a rule that sets
// none. GetBucketCors writes no MaxAgeSeconds for it, and no
// Access-Control-Max-Age is sent.
const MaxAgeInvalid uint32 = math.MaxUint32

// Rule is RGWCORSRule. AllowedOrigins and AllowedHeaders are std::sets:
// Decode and ParseXML return them sorted bytewise with each string once, and
// Encode and MarshalS3XML write them so whatever order a caller built them
// in. ExposeHeaders is a std::list, kept in document order with its repeats.
type Rule struct {
	ID             string
	MaxAge         uint32
	Methods        Methods
	AllowedOrigins []string
	AllowedHeaders []string
	ExposeHeaders  []string
}

// Configuration is RGWCORSConfiguration, its rules in document order.
type Configuration struct {
	Rules []Rule
}

// Encode is RGWCORSConfiguration::encode, the bytes radosgw stores under
// user.rgw.cors
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_cors.h#L102-L106 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cors.h#L102-L106 at v20.2.4):
// ENCODE_START(1, 1) around the list of rules.
func (c *Configuration) Encode() []byte {
	e := denc.NewEncoder()
	f := e.BeginStruct(1, 1)
	denc.EncodeSlice(e, c.Rules, func(e *denc.Encoder, r Rule) { r.encode(e) })
	e.EndStruct(f)
	return e.Bytes()
}

// Decode is RGWCORSConfiguration::decode
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_cors.h#L107-L111 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cors.h#L107-L111 at v20.2.4),
// DECODE_START(1) around the list of rules. Bytes after the configuration
// are not read, as read_bucket_cors reads none
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L1483-L1512 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L1720-L1749 at v20.2.4),
// whose EIO for an attr that does not decode is the error this returns.
func Decode(b []byte) (*Configuration, error) {
	d := denc.NewDecoder(b)
	h := d.BeginStruct(1)
	rules := denc.DecodeSlice(d, decodeRule)
	d.EndStruct(h)
	if err := d.Err(); err != nil {
		return nil, fmt.Errorf("decoding the CORS configuration: %w", err)
	}
	return &Configuration{Rules: rules}, nil
}

// encode is RGWCORSRule::encode
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_cors.h#L63-L72 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cors.h#L63-L72 at v20.2.4):
// ENCODE_START(1, 1), max_age, allowed_methods, id, the allowed-header set,
// the allowed-origin set and the expose-header list.
func (r *Rule) encode(e *denc.Encoder) {
	f := e.BeginStruct(1, 1)
	e.U32(r.MaxAge)
	e.U8(uint8(r.Methods))
	e.String(r.ID)
	denc.EncodeSlice(e, set(r.AllowedHeaders), (*denc.Encoder).String)
	denc.EncodeSlice(e, set(r.AllowedOrigins), (*denc.Encoder).String)
	denc.EncodeSlice(e, r.ExposeHeaders, (*denc.Encoder).String)
	e.EndStruct(f)
}

// decodeRule is RGWCORSRule::decode
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_cors.h#L73-L82 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cors.h#L73-L82 at v20.2.4).
// A std::set decodes through emplace_hint, which sorts what it reads and
// drops a repeat.
func decodeRule(d *denc.Decoder) Rule {
	h := d.BeginStruct(1)
	var r Rule
	r.MaxAge = d.U32()
	r.Methods = Methods(d.U8())
	r.ID = d.String()
	r.AllowedHeaders = set(denc.DecodeSlice(d, (*denc.Decoder).String))
	r.AllowedOrigins = set(denc.DecodeSlice(d, (*denc.Decoder).String))
	r.ExposeHeaders = denc.DecodeSlice(d, (*denc.Decoder).String)
	d.EndStruct(h)
	return r
}

// set returns s as a std::set<std::string> holds it, sorted bytewise as
// std::string's operator< sorts with each string once; s itself when it
// already is.
func set(s []string) []string {
	ordered := true
	for i := 1; i < len(s) && ordered; i++ {
		ordered = s[i-1] < s[i]
	}
	if ordered {
		return s
	}
	t := slices.Clone(s)
	slices.Sort(t)
	return slices.Compact(t)
}
