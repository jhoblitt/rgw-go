package op

import (
	"fmt"
	"strings"
	"time"
)

// TransID is RGWSI_ZoneUtils::unique_trans_id: "tx", the sequence number as
// 21 hex digits, a dash, the Unix time as 10 hex digits, and the suffix.
func TransID(seq uint64, now time.Time, suffix string) string {
	return fmt.Sprintf("tx%021x-%010x%s", seq, uint64(now.Unix()), suffix) //nolint:gosec // radosgw casts time_t to unsigned long long
}

// TransIDSuffix is radosgw's trans_id_suffix: "-<instance id>-<zone name>",
// URL-encoded with URLEncode(…, true), radosgw's url_encode (svc_zone_utils.cc:22-63).
func TransIDSuffix(instanceID uint64, zone string) string {
	return URLEncode(fmt.Sprintf("-%d-%s", instanceID, zone), true)
}

// HostID is RGWSI_ZoneUtils::gen_host_id: "<instance id>-<zone>-<zonegroup>".
func HostID(instanceID uint64, zone, zonegroup string) string {
	return fmt.Sprintf("%d-%s-%s", instanceID, zone, zonegroup)
}

// URLEncode is radosgw's url_encode(src, encode_slash) (rgw_common.cc:1775-1785
// at v19.2.6, :1838 at v20.2.4) over char_needs_url_encoding (:1743-1773;
// :1806): a byte <= 0x20 or >= 0x7F, or one of " # % & + , / : ; < = > ? @ [ \ ]
// ^ ` { }, is %XX; everything else — alphanumerics and ! $ ' ( ) * - . _ | ~ —
// passes through. With encodeSlash false '/' passes too, dump_urlsafe's form for
// listing prefixes and delimiters (rgw_rest.h:712, encode_slash defaults true).
// It serves the transaction-id suffix (encodeSlash true) and the object and
// multipart-upload listings' encoding-type=url; nothing else in rgw-go
// URL-encodes by hand.
func URLEncode(s string, encodeSlash bool) string {
	var b strings.Builder
	for i := range len(s) {
		c := s[i]
		if urlByteNeedsEncoding(c) && (c != '/' || encodeSlash) {
			fmt.Fprintf(&b, "%%%02X", c)
		} else {
			b.WriteByte(c)
		}
	}
	return b.String()
}

// urlByteNeedsEncoding is char_needs_url_encoding, the switch transcribed.
func urlByteNeedsEncoding(c byte) bool {
	if c <= 0x20 || c >= 0x7F {
		return true
	}
	switch c {
	case 0x22, 0x23, 0x25, 0x26, 0x2B, 0x2C, 0x2F, 0x3A, 0x3B, 0x3C, 0x3D, 0x3E, 0x3F, 0x40, 0x5B, 0x5C, 0x5D, 0x5E, 0x60, 0x7B, 0x7D:
		return true
	}
	return false
}
