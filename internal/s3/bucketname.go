package s3

import (
	"strings"

	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/vhost"
)

// ValidBucketName is valid_s3_bucket_name (rgw_rest_s3.h:838-907 at
// v19.2.6, :868-937 at v20.2.4), which CreateBucket applies to a name it
// creates: 3 to 63 bytes, or 255 under rgw_relaxed_s3_bucket_names; strictly
// lowercase letters, digits, dashes and dots, starting and ending with a
// letter or digit, no dot beside a dot or a dash; relaxed, letters of either
// case, digits, '_', '-' and '.', starting with any of them; never an IP
// address. Letters and digits are ASCII, as C's ctype is in the C locale.
// The character scan stops at a NUL, as radosgw walks the name as a C
// string; the length and the last-character checks see the whole name.
func ValidBucketName(name string, relaxed bool) error {
	maxLen := 63
	if relaxed {
		maxLen = 255
	}
	if len(name) < 3 || len(name) > maxLen {
		return op.ErrInvalidBucketName
	}
	if !isASCIIAlnum(name[0]) && (!relaxed || !strings.ContainsRune("_.-", rune(name[0]))) {
		return op.ErrInvalidBucketName
	}
	if !isASCIIAlnum(name[len(name)-1]) && !relaxed {
		return op.ErrInvalidBucketName
	}
	cstr, _, _ := strings.Cut(name, "\x00")
	for i := range len(cstr) {
		if !validBucketNameByte(name, i, relaxed) {
			return op.ErrInvalidBucketName
		}
	}
	if vhost.LooksLikeIPAddress(cstr) {
		return op.ErrInvalidBucketName
	}
	return nil
}

// validBucketNameByte is valid_s3_bucket_name's test of the byte at i. A
// strict dot is checked against its neighbors, which exist: the first and
// last bytes are letters or digits by then.
func validBucketNameByte(name string, i int, relaxed bool) bool {
	c := name[i]
	switch {
	case '0' <= c && c <= '9', 'a' <= c && c <= 'z', c == '-':
		return true
	case 'A' <= c && c <= 'Z', c == '_':
		return relaxed
	case c == '.':
		if relaxed {
			return true
		}
		p, n := name[i-1], nextByte(name, i)
		return p != '-' && n != '.' && n != '-'
	default:
		return false
	}
}

// nextByte is the byte after i, NUL past the end as a C string has it.
func nextByte(s string, i int) byte {
	if i+1 < len(s) {
		return s[i+1]
	}
	return 0
}
