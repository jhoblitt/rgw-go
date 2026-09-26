package meta

import (
	"fmt"
	"strings"

	"github.com/jhoblitt/rgw-go/internal/denc"
)

// ObjKey is rgw_obj_key: name, version instance and namespace.
type ObjKey struct {
	Name     string `json:"name"`
	Instance string `json:"instance"`
	NS       string `json:"ns"`
}

// nullInstance is the version instance of an object written while versioning
// was suspended; it never appears in an object name.
const nullInstance = "null"

// escapeName prepends "_" to a name that starts with one, so that it cannot
// be mistaken for a namespaced name.
func escapeName(name string) string {
	if strings.HasPrefix(name, "_") {
		return "_" + name
	}
	return name
}

// IndexKeyName is rgw_obj_key::get_index_key_name, the key's name in the
// bucket index: the escaped name, or "_<ns>_<name>" in a namespace.
func (k ObjKey) IndexKeyName() string {
	if k.NS == "" {
		return escapeName(k.Name)
	}
	return "_" + k.NS + "_" + k.Name
}

// encodesInstance is rgw_obj_key::need_to_encode_instance.
func (k ObjKey) encodesInstance() bool {
	return k.Instance != "" && k.Instance != nullInstance
}

// OID is rgw_obj_key::get_oid, the key's part of its head object name: the
// escaped name, or "_<ns>[:<instance>]_<name>" when namespaced or versioned.
func (k ObjKey) OID() string {
	if k.NS == "" && !k.encodesInstance() {
		return escapeName(k.Name)
	}
	oid := "_" + k.NS
	if k.encodesInstance() {
		oid += ":" + k.Instance
	}
	return oid + "_" + k.Name
}

// Locator is rgw_obj_key::get_loc: the name when it starts with "_" outside
// a namespace, kept for objects older releases wrote with a locator, and ""
// otherwise.
func (k ObjKey) Locator() string {
	if k.NS == "" && strings.HasPrefix(k.Name, "_") {
		return k.Name
	}
	return ""
}

// Encode mirrors rgw_obj_key::encode, ENCODE_START(2, 1).
func (k ObjKey) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(2, 1)
	e.String(k.Name)
	e.String(k.Instance)
	e.String(k.NS)
	e.EndStruct(f)
}

// DecodeObjKey mirrors rgw_obj_key::decode, DECODE_START(2).
func DecodeObjKey(d *denc.Decoder) ObjKey {
	h := d.BeginStruct(2)
	var k ObjKey
	k.Name = d.String()
	k.Instance = d.String()
	if h.Version >= 2 {
		k.NS = d.String()
	}
	d.EndStruct(h)
	return k
}

// Obj is rgw_obj: a bucket and a key.
type Obj struct {
	Bucket BucketID `json:"bucket"`
	Key    ObjKey   `json:"key"`
}

// Encode mirrors rgw_obj::encode, ENCODE_START(6, 6). The key is written as
// its three strings, not as an rgw_obj_key struct.
func (o Obj) Encode(e *denc.Encoder, r denc.Release) {
	f := e.BeginStruct(6, 6)
	o.Bucket.Encode(e, r)
	e.String(o.Key.NS)
	e.String(o.Key.Name)
	e.String(o.Key.Instance)
	e.EndStruct(f)
}

// DecodeObj mirrors rgw_obj::decode, DECODE_START_LEGACY_COMPAT_LEN(6, 3, 3).
func DecodeObj(d *denc.Decoder) Obj {
	h := d.BeginStructLegacy(6, 3, 3, 0)
	o := decodeObjBody(d, h.Version)
	d.EndStruct(h)
	return o
}

// decodeObjBody reads the fields of an rgw_obj at struct version v, after its
// header. Below version 6 the key name is stored mangled, and is unmangled as
// rgw_obj::decode does.
func decodeObjBody(d *denc.Decoder, v uint8) Obj {
	var o Obj
	if v >= 6 {
		o.Bucket = DecodeBucketID(d)
		o.Key.NS = d.String()
		o.Key.Name = d.String()
		o.Key.Instance = d.String()
		return o
	}
	o.Bucket.Name = d.String()
	_ = d.String() // the locator, which old releases stored and rgw_obj::decode drops
	o.Key.NS = d.String()
	o.Key.Name = d.String()
	if v >= 2 {
		o.Bucket = DecodeBucketID(d)
	}
	if v >= 4 {
		o.Key.Instance = d.String()
	}
	if o.Key.NS == "" && o.Key.Instance == "" {
		o.Key.Name = strings.TrimPrefix(o.Key.Name, "_")
		return o
	}
	if v >= 5 {
		o.Key.Name = d.String()
		return o
	}
	// The name is "_<ns>[:<instance>]_<name>"; the C++ searches for the
	// second "_" from offset 1 and throws when there is none.
	i := -1
	if len(o.Key.Name) > 1 {
		i = strings.IndexByte(o.Key.Name[1:], '_')
	}
	if i < 0 {
		d.Fail(fmt.Errorf("%w: rgw_obj v%d name %q has no namespace separator", denc.ErrMalformed, v, o.Key.Name))
		return Obj{}
	}
	o.Key.Name = o.Key.Name[i+2:]
	return o
}
