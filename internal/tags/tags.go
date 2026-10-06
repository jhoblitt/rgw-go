package tags

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/rgwtext"
)

// Attr is RGW_ATTR_TAGS (rgw_common.h:115 at v19.2.6, :126 at v20.2.4), the
// attr an object's or a bucket's tag set is stored under.
const Attr = meta.AttrPrefix + "x-amz-tagging"

// The limits of check_and_add_tag (rgw_tag.h:18-20): an object's tag set holds
// at most MaxObjectTags tags, and RGWPutBucketTags_ObjStore_S3::get_params
// (rgw_rest_s3.cc:898 at v19.2.6, :980 at v20.2.4) allows a bucket
// MaxBucketTags. Lengths are in bytes.
const (
	MaxObjectTags = 10
	MaxBucketTags = 50
	MaxKeyLen     = 128
	MaxValueLen   = 256
)

// Tag is one entry of RGWObjTags' std::multimap<string,string>.
type Tag struct{ Key, Value string }

// Set is RGWObjTags. Tags is kept in multimap order, sorted by key with equal
// keys in insertion order, by every function of this package that builds a
// Set; Encode, MarshalJSON and MarshalS3XML write that order whatever order
// a caller built Tags in.
type Set struct { //nolint:recvcheck // Add mutates the set, while it encodes and renders by value as every stored type does
	Tags []Tag
}

// ErrInvalidTag is radosgw's -ERR_INVALID_TAG: a tag check_and_add_tag
// refuses.
var ErrInvalidTag = errors.New("tags: invalid tag")

// Add is check_and_add_tag (rgw_tag.cc:23-34) with limit as max_obj_tags: an
// empty key, a key over MaxKeyLen, a value over MaxValueLen or a set already
// holding limit tags is ErrInvalidTag. radosgw compares the size for equality;
// a set built only through Add never exceeds its limit, so the two agree.
func (s *Set) Add(key, value string, limit int) error {
	switch {
	case len(s.Tags) >= limit:
		return fmt.Errorf("%w: the set already holds %d tags", ErrInvalidTag, len(s.Tags))
	case key == "":
		return fmt.Errorf("%w: empty key", ErrInvalidTag)
	case len(key) > MaxKeyLen:
		return fmt.Errorf("%w: key of %d bytes exceeds %d", ErrInvalidTag, len(key), MaxKeyLen)
	case len(value) > MaxValueLen:
		return fmt.Errorf("%w: value of %d bytes exceeds %d", ErrInvalidTag, len(value), MaxValueLen)
	}
	s.insert(key, value)
	return nil
}

// Len is the number of tags, RGWObjTags::count.
func (s Set) Len() int { return len(s.Tags) }

// insert adds a tag where std::multimap::insert puts it: after every tag
// whose key does not sort after it.
func (s *Set) insert(key, value string) {
	i := sort.Search(len(s.Tags), func(i int) bool { return s.Tags[i].Key > key })
	s.Tags = slices.Insert(s.Tags, i, Tag{Key: key, Value: value})
}

func byKey(a, b Tag) int { return strings.Compare(a.Key, b.Key) }

// multimapOrder sorts tags read in insertion order into the order a
// std::multimap inserting them one by one holds: the sort is stable, so equal
// keys keep their insertion order. One sort replaces an insert per tag, which
// costs quadratic time on a large set in descending order.
func multimapOrder(t []Tag) {
	slices.SortStableFunc(t, byKey)
}

// ordered returns the tags in multimap order, sorting a copy when a caller
// built Tags out of order.
func (s Set) ordered() []Tag {
	if slices.IsSortedFunc(s.Tags, byKey) {
		return s.Tags
	}
	t := slices.Clone(s.Tags)
	multimapOrder(t)
	return t
}

// Encode is RGWObjTags::encode (rgw_tag.h:26-30): ENCODE_START(1, 1) around
// the multimap, a u32 count then each key and value.
func (s Set) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	denc.EncodeSlice(e, s.ordered(), func(e *denc.Encoder, t Tag) {
		e.String(t.Key)
		e.String(t.Value)
	})
	e.EndStruct(f)
}

// Decode is RGWObjTags::decode (rgw_tag.h:32-62):
// DECODE_START_LEGACY_COMPAT_LEN(1, 1, 1) around the multimap. When that
// fails, radosgw rewinds to where the decode began and reads every byte left
// in the buffer, trailing NULs stripped, as the URL-encoded "k=v&k2=v2" text
// some older objects store, through set_from_string with MaxObjectTags. An
// empty or unparsable text fails with the binary decoder's error, and Decode
// then returns the zero Set.
func Decode(d *denc.Decoder) Set {
	if d.Err() != nil {
		return Set{}
	}
	// A copy of a Decoder reads on from the same offset and leaves the
	// original where it was, so d stays at radosgw's start_pos for the
	// fallback.
	bin := *d
	s := decodeBinary(&bin)
	if bin.Err() == nil {
		*d = bin
		return s
	}
	raw := bytes.TrimRight(d.Raw(d.Remaining()), "\x00")
	if len(raw) > 0 {
		var text Set
		if text.addString(string(raw), MaxObjectTags) == nil {
			return text
		}
	}
	d.Fail(bin.Err())
	return Set{}
}

// decodeBinary reads DECODE_START_LEGACY_COMPAT_LEN(1, 1, 1), whose struct_v
// 0 carries neither compat nor length, the multimap and DECODE_FINISH
// (src/include/encoding.h, the same at v19.2.6 and v20.2.4).
func decodeBinary(d *denc.Decoder) Set {
	h := d.BeginStructLegacy(1, 1, 1, 0)
	t := denc.DecodeSlice(d, func(d *denc.Decoder) Tag {
		key := d.String()
		return Tag{Key: key, Value: d.String()}
	})
	d.EndStruct(h)
	if d.Err() != nil {
		return Set{}
	}
	multimapOrder(t)
	return Set{Tags: t}
}

// MarshalJSON is RGWObjTags::dump (rgw_tag.cc:59-66), the form ceph-dencoder
// prints: {"tagset": {key: value, ...}}, a key that occurs twice written
// twice. JSONFormatter writes each value through json_stream_escaper
// (Formatter.cc:195 at v19.2.6, :204 at v20.2.4; escape.cc:254-286 at both),
// and like it this leaves <, > and & as they are; json.Marshal escapes them
// again in what it returns unless the caller's Encoder turns HTML escaping
// off.
//
// The parity is semantic, not byte for byte:
//   - JSONFormatter writes a key raw and cut at its first NUL
//     (dump_string(tag.first.c_str(), ...), rgw_tag.cc:63; print_name,
//     Formatter.cc:210 at v19.2.6, :219 at v20.2.4), so its output is not
//     JSON for a key holding '"', '\' or a control byte, where this escapes
//     the key as it does a value.
//   - encoding/json writes invalid UTF-8 as U+FFFD, where json_stream_escaper
//     copies the bytes.
//   - Some characters take another form that parses to the same string:
//     encoding/json writes \r, \b and \f where json_stream_escaper writes
//     \u000d, \u0008 and \u000c; it copies DEL, 0x7f, which
//     json_stream_escaper writes as \u007f; and it always writes U+2028 and
//     U+2029 as \u2028 and \u2029, which json_stream_escaper copies.
func (s Set) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	str := func(v string) error {
		if err := enc.Encode(v); err != nil {
			return err
		}
		b.Truncate(b.Len() - 1) // Encode ends every value with a newline
		return nil
	}
	b.WriteString(`{"tagset":{`)
	for i, t := range s.ordered() {
		if i > 0 {
			b.WriteByte(',')
		}
		if err := str(t.Key); err != nil {
			return nil, fmt.Errorf("marshaling tag key: %w", err)
		}
		b.WriteByte(':')
		if err := str(t.Value); err != nil {
			return nil, fmt.Errorf("marshaling tag value: %w", err)
		}
	}
	b.WriteString("}}")
	return b.Bytes(), nil
}

// ParseHeader is set_from_string (rgw_tag.cc:36-57), the x-amz-tagging
// header: "&"-separated items, each "key=value" split at its first "=" or a
// bare key with an empty value, each side through url_decode, each tag
// through Add with limit. An empty item, a trailing "&" included, is an empty
// key and so ErrInvalidTag. An empty header is the empty set.
func ParseHeader(h string, limit int) (Set, error) {
	var s Set
	if err := s.addString(h, limit); err != nil {
		return Set{}, err
	}
	return s, nil
}

func (s *Set) addString(input string, limit int) error {
	if input == "" {
		return nil
	}
	for kv := range strings.SplitSeq(input, "&") {
		key, value, _ := strings.Cut(kv, "=")
		if err := s.Add(rgwtext.URLDecode(key, false), rgwtext.URLDecode(value, false), limit); err != nil {
			return err
		}
	}
	return nil
}
