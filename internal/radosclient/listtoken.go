package radosclient

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"math/bits"
	"strings"
)

// RJenkins is ceph_str_hash_rjenkins, Bob Jenkins' lookup2 hash, which a
// pool of the default object_hash places objects by
// (src/common/ceph_hash.cc:22-78 at v19.2.6 and v20.2.4).
func RJenkins(k []byte) uint32 {
	a, b, c := uint32(0x9e3779b9), uint32(0x9e3779b9), uint32(0)
	length := uint32(len(k)) //nolint:gosec // the C takes the length as an unsigned int
	for len(k) >= 12 {
		a += binary.LittleEndian.Uint32(k[0:4])
		b += binary.LittleEndian.Uint32(k[4:8])
		c += binary.LittleEndian.Uint32(k[8:12])
		a, b, c = mix(a, b, c)
		k = k[12:]
	}
	// The length goes into c's low byte, so the tail's bytes for c start at
	// its second.
	c += length
	switch len(k) {
	case 11:
		c += uint32(k[10]) << 24
		fallthrough
	case 10:
		c += uint32(k[9]) << 16
		fallthrough
	case 9:
		c += uint32(k[8]) << 8
		fallthrough
	case 8:
		b += uint32(k[7]) << 24
		fallthrough
	case 7:
		b += uint32(k[6]) << 16
		fallthrough
	case 6:
		b += uint32(k[5]) << 8
		fallthrough
	case 5:
		b += uint32(k[4])
		fallthrough
	case 4:
		a += uint32(k[3]) << 24
		fallthrough
	case 3:
		a += uint32(k[2]) << 16
		fallthrough
	case 2:
		a += uint32(k[1]) << 8
		fallthrough
	case 1:
		a += uint32(k[0])
	}
	_, _, c = mix(a, b, c)
	return c
}

// mix is lookup2's mix macro, returning the state it leaves.
func mix(a0, b0, c0 uint32) (a, b, c uint32) {
	a, b, c = a0, b0, c0
	a -= b
	a -= c
	a ^= c >> 13
	b -= c
	b -= a
	b ^= a << 8
	c -= a
	c -= b
	c ^= b >> 13
	a -= b
	a -= c
	a ^= c >> 12
	b -= c
	b -= a
	b ^= a << 16
	c -= a
	c -= b
	c ^= b >> 5
	a -= b
	a -= c
	a ^= c >> 3
	b -= c
	b -= a
	b ^= a << 10
	c -= a
	c -= b
	c ^= b >> 15
	return a, b, c
}

// PlacementHash is the hash RADOS places an object without a locator by:
// pg_pool_t::hash_key under the default object_hash, rjenkins of ns, a 0x1f
// and oid, or of oid alone in the default namespace (src/osd/osd_types.cc
// :1785-1796 at v19.2.6, :1794-1805 at v20.2.4). An object with a locator
// is placed by the locator's hash instead.
func PlacementHash(ns, oid string) uint32 {
	if ns == "" {
		return RJenkins([]byte(oid))
	}
	return RJenkins([]byte(ns + "\x1f" + oid))
}

// listTokenPrefix versions the token form.
const listTokenPrefix = "1:"

// EncodeListToken is the token that resumes a listing after lastOID:
// unpadded base64url of "1:" and the name.
func EncodeListToken(lastOID string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(listTokenPrefix + lastOID))
}

// DecodeListToken returns the name EncodeListToken put in token. Any other
// token, one naming no object among them, is ErrBadOp: no listing delivers
// an object without a name.
func DecodeListToken(token string) (lastOID string, err error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err == nil {
		var ok bool
		if lastOID, ok = strings.CutPrefix(string(raw), listTokenPrefix); ok && lastOID != "" {
			return lastOID, nil
		}
	}
	return "", fmt.Errorf("radosclient: list token %q: %w", token, ErrBadOp)
}

// ListAfter reports whether the object hashing to h and named oid comes
// after the resume point (lastHash, lastOID) in hobject order, the order a
// listing of one namespace without locators follows: by the bit-reversed
// hash, then by name (cmp, src/common/hobject.cc:335-370 at v19.2.6,
// :331-366 at v20.2.4).
func ListAfter(h uint32, oid string, lastHash uint32, lastOID string) bool {
	if h != lastHash {
		return bits.Reverse32(h) > bits.Reverse32(lastHash)
	}
	return oid > lastOID
}
