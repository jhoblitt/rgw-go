package cksum

import (
	"slices"
	"strings"
)

// Type is rgw::cksum::Type, the u16 id a record stores
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum.h#L41-L52 at v20.2.4).
type Type uint16

// Tentacle's nine ids. Main adds md5 9, xxhash64 10 and xxhash128 11
// (https://github.com/ceph/ceph/blob/f6f2529dd9efccf7710c8a2b561e5a2ab9d48bd3/src/rgw/rgw_cksum.h#L41-L56
// at main), which a v20.2.4 radosgw would look up past the end of its table.
const (
	None Type = iota
	CRC32
	CRC32C
	XXH3
	SHA1
	SHA256
	SHA512
	BLAKE3
	CRC64NVME

	numTypes
)

// Info is a type's row of radosgw's table and the names derived from it.
type Info struct {
	// Name is the type's lower-case name, which ParseName matches.
	Name string
	// Header carries the armored checksum in a response.
	Header string
	// Element carries it in an XML document.
	Element    string
	DigestSize int
	// AWS is set for a type AWS defines, whose header is x-amz-checksum-<name>.
	// Any other's is x-rgw-checksum-<name>.
	AWS bool
	// CRC is set for the CRCs, whose part checksums combine into a
	// full-object CRC.
	CRC bool
}

// infos is Tentacle's table
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum.h#L88-L99 at v20.2.4)
// under AWS's names, as main has them: type 3 is xxhash3, and it and sha512
// are AWS types
// (https://github.com/ceph/ceph/blob/f6f2529dd9efccf7710c8a2b561e5a2ab9d48bd3/src/rgw/rgw_cksum.h#L93-L107
// at main). A v20.2.4 radosgw names them xxh3 and x-rgw-checksum-sha512.
var infos = [numTypes]Info{
	None:      info("none", 0, false, false),
	CRC32:     info("crc32", 4, true, true),
	CRC32C:    info("crc32c", 4, true, true),
	XXH3:      info("xxhash3", 8, true, false),
	SHA1:      info("sha1", 20, true, false),
	SHA256:    info("sha256", 32, true, false),
	SHA512:    info("sha512", 64, true, false),
	BLAKE3:    info("blake3", 32, false, false),
	CRC64NVME: info("crc64nvme", 8, true, true),
}

// info derives a row's header and element as header_name and element_name do
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum.h#L177-L192 at v20.2.4).
func info(name string, size int, aws, crc bool) Info {
	header := "x-rgw-checksum-" + name
	if aws {
		header = "x-amz-checksum-" + name
	}
	return Info{Name: name, Header: header, Element: "Checksum" + strings.ToUpper(name), DigestSize: size, AWS: aws, CRC: crc}
}

// Info returns t's row, or the zero Info for a type with no id.
func (t Type) Info() Info {
	if t >= numTypes {
		return Info{}
	}
	return infos[t]
}

// Writable reports whether rgw-go computes t for a write: the AWS types whose
// digests the Go standard library computes. It does not limit which stored
// types are read.
func (t Type) Writable() bool {
	switch t {
	case CRC32, CRC32C, CRC64NVME, SHA1, SHA256, SHA512:
		return true
	}
	return false
}

// ParseName returns the algorithm name names, matched with ASCII case folded
// as parse_cksum_type's boost::iequals matches it
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum.h#L293-L300 at v20.2.4).
// The names are Tentacle's eight algorithms and main's xxhash3 for type 3;
// any other gives false. That includes none: radosgw's callers take it as
// naming no algorithm, and an algorithm header naming it is EINVAL
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum_pipe.cc#L46-L61 at v20.2.4).
func ParseName(name string) (Type, bool) {
	if equalFoldASCII(name, "xxh3") {
		return XXH3, true
	}
	for t := range numTypes {
		if t != None && equalFoldASCII(name, infos[t].Name) {
			return t, true
		}
	}
	return None, false
}

// foreignNames are main's types with no Tentacle id.
var foreignNames = []string{"md5", "xxhash64", "xxhash128"}

// ForeignName reports whether name, matched as ParseName matches, is md5,
// xxhash64 or xxhash128: a type main defines and Tentacle has no id for.
func ForeignName(name string) bool {
	return slices.ContainsFunc(foreignNames, func(n string) bool { return equalFoldASCII(name, n) })
}

// The record's flags
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum.h#L104-L113 at v20.2.4).
const (
	FlagV2         uint16 = 0x1
	FlagComposite  uint16 = 0x2
	FlagFullObject uint16 = 0x4
	FlagCombined   uint16 = 0x8
)

// FlagsOf is cksum_flags_of, an upload's flags when CreateMultipartUpload
// sends no x-amz-checksum-type: FULL_OBJECT for a CRC, 0 for none, and
// COMPOSITE for any other type
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum.h#L302-L316 at v20.2.4,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum_pipe.h#L138-L154 at v20.2.4).
func FlagsOf(t Type) uint16 {
	switch {
	case t == None:
		return 0
	case t.Info().CRC:
		return FlagFullObject
	}
	return FlagComposite
}

// Permitted is permitted_cksum_algo_and_type, CreateMultipartUpload's check of
// an algorithm against its checksum type: none passes; COMPOSITE passes for
// any type but CRC64NVME; FULL_OBJECT, or no type flag, passes only for a CRC
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum.h#L340-L356 at v20.2.4).
// A type with no id never passes.
func Permitted(t Type, flags uint16) bool {
	switch {
	case t == None:
		return true
	case t >= numTypes:
		return false
	case flags&FlagComposite != 0:
		return t != CRC64NVME
	}
	return t.Info().CRC
}

func equalFoldASCII(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range len(a) {
		if lowerASCII(a[i]) != lowerASCII(b[i]) {
			return false
		}
	}
	return true
}

func lowerASCII(c byte) byte {
	if 'A' <= c && c <= 'Z' {
		return c + 'a' - 'A'
	}
	return c
}
