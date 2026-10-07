package s3

import (
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/jhoblitt/rgw-go/internal/cephconf"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// metaPrefixes is req_info::init_meta_info's meta_prefixes (rgw_common.cc
// :413-420 at v19.2.6, :426-433 at v20.2.4): the environment names of the
// headers that become x_meta_map entries.
var metaPrefixes = [...]string{
	"HTTP_X_AMZ_", "HTTP_X_GOOG_", "HTTP_X_DHO_", "HTTP_X_RGW_", "HTTP_X_OBJECT_", "HTTP_X_CONTAINER_", "HTTP_X_ACCOUNT_",
}

// metaBlocklist is the x_meta_map entries rgw_get_request_metadata does not
// store: v20.2.4's list (rgw_op.h:2344-2357), which adds the copy-source
// SSE-C headers, x-amz-content-sha256, x-amz-checksum-algorithm and
// x-amz-date to v19.2.6's four (:2177-2182), applied on both releases, and
// x-amz-security-token, which both releases store. v19.2.6 stores a
// CopyObject's copy-source SSE-C key in the clear, and both store a
// request's session token (docs/ceph-upstream-bugs.md, "radosgw stores
// request credentials as object attrs"; docs/exclusions.md).
var metaBlocklist = map[string]bool{
	"x-amz-server-side-encryption-customer-algorithm":             true,
	"x-amz-server-side-encryption-customer-key":                   true,
	"x-amz-server-side-encryption-customer-key-md5":               true,
	"x-amz-copy-source-server-side-encryption-customer-algorithm": true,
	"x-amz-copy-source-server-side-encryption-customer-key":       true,
	"x-amz-copy-source-server-side-encryption-customer-key-md5":   true,
	"x-amz-storage-class":                                         true,
	"x-amz-content-sha256":                                        true,
	"x-amz-checksum-algorithm":                                    true,
	"x-amz-date":                                                  true,
	"x-amz-security-token":                                        true,
}

// genericAttr is one entry of generic_attrs_map: the environment name of a
// header and the attr populate_with_generic_attrs stores its value under.
type genericAttr struct{ env, attr string }

// baseGenericAttrs is rgw_rest.cc's generic_attrs (:123-131 at v19.2.6 and
// v20.2.4).
var baseGenericAttrs = []genericAttr{
	{"CONTENT_TYPE", meta.AttrContentType},
	{"HTTP_CONTENT_LANGUAGE", meta.AttrContentLang},
	{"HTTP_EXPIRES", meta.AttrExpires},
	{"HTTP_CACHE_CONTROL", meta.AttrCacheControl},
	{"HTTP_CONTENT_DISPOSITION", meta.AttrContentDisp},
	{"HTTP_CONTENT_ENCODING", meta.AttrContentEnc},
	{"HTTP_X_ROBOTS_TAG", meta.AttrXRobotsTag},
}

// genericAttrsFor is generic_attrs_map as rgw_rest_init builds it
// (rgw_rest.cc:185-209 at v19.2.6 and v20.2.4): baseGenericAttrs, plus each
// name rgw_extended_http_attrs lists, its header's environment name upper
// cased with dashes as underscores and its attr lowercased with dashes as
// underscores, in the map's order of environment names.
func genericAttrsFor(conf *cephconf.Options) []genericAttr {
	byEnv := map[string]string{}
	for _, g := range baseGenericAttrs {
		byEnv[g.env] = g.attr
	}
	if conf != nil {
		if names, err := conf.List("rgw_extended_http_attrs"); err == nil {
			for _, n := range names {
				byEnv["HTTP_"+uppercaseUnderscore(n)] = meta.AttrPrefix + lowercaseUnderscore(n)
			}
		}
	}
	attrs := make([]genericAttr, 0, len(byEnv))
	for _, env := range slices.Sorted(maps.Keys(byEnv)) {
		attrs = append(attrs, genericAttr{env, byEnv[env]})
	}
	return attrs
}

// uppercaseUnderscore is uppercase_underscore_http_attr (rgw_rest.cc
// :163-179 at v19.2.6).
func uppercaseUnderscore(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c == '-' {
			b[i] = '_'
		} else {
			b[i] = upperASCII(c)
		}
	}
	return string(b)
}

// envName is the name beast files a header under in RGWEnv
// (rgw_asio_client.cc:36-66 at v19.2.6 and v20.2.4): Content-Length and
// Content-Type under their CGI names, every other header under HTTP_ and its
// name upper cased with each dash an underscore and each underscore a dash.
func envName(header string) string {
	switch {
	case strings.EqualFold(header, "Content-Length"):
		return "CONTENT_LENGTH"
	case strings.EqualFold(header, "Content-Type"):
		return "CONTENT_TYPE"
	}
	b := []byte("HTTP_" + header)
	for i := len("HTTP_"); i < len(b); i++ {
		switch b[i] {
		case '-':
			b[i] = '_'
		case '_':
			b[i] = '-'
		default:
			b[i] = upperASCII(b[i])
		}
	}
	return string(b)
}

// requestEnv is the headers as RGWEnv holds them: each environment name with
// the last value of its header, as RGWEnv::set keeps the last (rgw_env.cc
// :22-25 at v19.2.6 and v20.2.4), and the names in its map's order.
func requestEnv(h http.Header) (names []string, vals map[string]string) {
	vals = map[string]string{}
	for _, k := range slices.Sorted(maps.Keys(h)) {
		if vs := h[k]; len(vs) > 0 {
			vals[envName(k)] = vs[len(vs)-1]
		}
	}
	return slices.Sorted(maps.Keys(vals)), vals
}

// lowercaseDash is lowercase_dash_http_attr with bidirection, as S3 calls it
// (rgw_common.cc:2198-2220 at v19.2.6, :2261-2283 at v20.2.4): each
// underscore a dash, each dash an underscore, and letters lowercased.
func lowercaseDash(s string) string {
	b := []byte(s)
	for i, c := range b {
		switch c {
		case '_':
			b[i] = '-'
		case '-':
			b[i] = '_'
		default:
			b[i] = lowerASCII(c)
		}
	}
	return string(b)
}

// addMeta is rgw_add_amz_meta_header (rgw_common.cc:472-487 at v19.2.6,
// :485-500 at v20.2.4), the join init_meta_info also makes: a name already
// present keeps its value, trailing white space trimmed, then a comma and
// v.
func addMeta(xmeta map[string]string, name, v string) {
	if old, ok := xmeta[name]; ok {
		xmeta[name] = strings.TrimRight(old, " \t\n\v\f\r") + "," + v
		return
	}
	xmeta[name] = v
}

// headerMeta is the x_meta_map req_info::init_meta_info builds from the
// headers (rgw_common.cc:413-464 at v19.2.6, :426-477 at v20.2.4): each
// header whose environment name starts with a meta prefix, under x-amz- and
// the rest of its name lowercased with each underscore a dash, a name
// reached twice joining its values with a comma.
func headerMeta(h http.Header) map[string]string {
	names, vals := requestEnv(h)
	xmeta := map[string]string{}
	for _, name := range names {
		for _, p := range metaPrefixes {
			if rest, ok := strings.CutPrefix(name, p); ok {
				addMeta(xmeta, lowercaseDash("X_AMZ_"+rest), vals[name])
			}
		}
	}
	return xmeta
}

// cryptPrefix is the prefix init_meta_info files an x_meta_map name into
// crypt_attribute_map under: its first 20 bytes, as strncmp compares them
// (rgw_common.cc:455-457 at v19.2.6, :468-470 at v20.2.4).
const cryptPrefix = "x-amz-server-side-en"

// copySourceCryptPrefix is the prefix of the copy source's SSE-C headers,
// which v20.2.4 reads to decrypt a copy's source.
const copySourceCryptPrefix = "x-amz-copy-source-server-side-encryption"

// isCryptName reports whether an x_meta_map name asks for encryption or
// names a copy source's encryption.
func isCryptName(name string) bool {
	return strings.HasPrefix(name, cryptPrefix) || strings.HasPrefix(name, copySourceCryptPrefix)
}

// requestAttrs is populate_with_generic_attrs (rgw_op.cc:3307-3316 at
// v19.2.6, :3535-3544 at v20.2.4) and rgw_get_request_metadata (rgw_op.h
// :2171-2233 at v19.2.6, :2338-2408 at v20.2.4) over the x_meta_map that
// req_info::init_meta_info builds from the headers (rgw_common.cc:422-464 at
// v19.2.6, :435-477 at v20.2.4) and, when query is set, map_qs_metadata adds
// from the query string (rgw_rest_s3.cc:2581-2595 at v19.2.6, :2742-2756 at
// v20.2.4), which PutObject calls and CopyObject does not:
//   - each generic header present, an empty one included, is its attr;
//   - each header named under a meta prefix is x-amz- and the rest of its
//     name, lowercased, and so is each x-amz-meta-* query parameter; a name
//     reached twice joins its values with a comma;
//   - each such name metaBlocklist does not list is the attr user.rgw. and
//     the name, its value through formatXattr; a name that asks for
//     encryption is never stored, as refuseEncryption refuses every write
//     carrying one.
//
// Every value gets a trailing NUL. In the names' order, an attr name longer
// than rgw_max_attr_name_len is ErrMetadataNameTooLong (-ENAMETOOLONG), a
// value longer than rgw_max_attr_size, -EFBIG, and the attr past
// rgw_max_attrs_num_in_req, -E2BIG, are UnknownError, as no S3 row names
// those errnos; a limit of 0 is none. No error names a header or carries its
// value.
func requestAttrs(r *op.Request, generic []genericAttr, query bool) (map[string][]byte, error) {
	_, vals := requestEnv(r.Header)
	attrs := map[string][]byte{}
	for _, g := range generic {
		if v, ok := vals[g.env]; ok {
			attrs[g.attr] = append([]byte(v), 0)
		}
	}
	xmeta := headerMeta(r.Header)
	if query {
		for _, k := range slices.Sorted(maps.Keys(r.Query)) {
			if low := lowerASCIIString(k); strings.HasPrefix(low, "x-amz-meta-") {
				addMeta(xmeta, low, r.Query.Get(k))
			}
		}
	}
	maxName := confUint(r, "rgw_max_attr_name_len", (*cephconf.Options).Size)
	maxSize := confUint(r, "rgw_max_attr_size", (*cephconf.Options).Size)
	maxNum := confUint(r, "rgw_max_attrs_num_in_req", (*cephconf.Options).Uint64)
	var count uint64
	for _, name := range slices.Sorted(maps.Keys(xmeta)) {
		if metaBlocklist[name] || isCryptName(name) {
			continue
		}
		v := formatXattr(xmeta[name])
		attr := meta.AttrPrefix + name
		if maxName != 0 && uint64(len(attr)) > maxName {
			return nil, fmt.Errorf("%w: a %d-byte attr name over rgw_max_attr_name_len %d", op.ErrMetadataNameTooLong, len(attr), maxName)
		}
		if maxSize != 0 && uint64(len(v)) > maxSize {
			return nil, fmt.Errorf("%w: a %d-byte attr value over rgw_max_attr_size %d", op.ErrUnknown, len(v), maxSize)
		}
		count++
		if maxNum != 0 && count > maxNum {
			return nil, fmt.Errorf("%w: more attrs than rgw_max_attrs_num_in_req %d", op.ErrUnknown, maxNum)
		}
		attrs[attr] = append([]byte(v), 0)
	}
	return attrs, nil
}

// confUint is an unsigned option read through get, 0 when it cannot be read.
func confUint(r *op.Request, name string, get func(*cephconf.Options, string) (uint64, error)) uint64 {
	if r.Env.Conf == nil {
		return 0
	}
	v, err := get(r.Env.Conf, name)
	if err != nil {
		return 0
	}
	return v
}

// formatXattr is format_xattr (rgw_op.h:2141-2160 at v19.2.6, :2308-2327 at
// v20.2.4): a value that is not UTF-8 as check_utf8 reads it, or holds a
// control character, is mime_encode_as_qp's quoted-printable text inside
// "=?UTF-8?Q?" and "?=" (src/common/mime.c:19-50): up to its first NUL, each
// byte with the high bit set, each "=" and each control character as "=XX",
// the rest as it is. Any other value is unchanged.
func formatXattr(v string) string {
	if utf8.ValidString(v) && !hasControlChar(v) {
		return v
	}
	var b strings.Builder
	b.WriteString("=?UTF-8?Q?")
	for i := 0; i < len(v) && v[i] != 0; i++ {
		if c := v[i]; c&0x80 != 0 || c == '=' || isControlChar(c) {
			fmt.Fprintf(&b, "=%02X", c)
		} else {
			b.WriteByte(c)
		}
	}
	b.WriteString("?=")
	return b.String()
}

// hasControlChar is check_for_control_characters (src/common/utf8.c:221-230
// at v19.2.6 and v20.2.4) over the whole value.
func hasControlChar(v string) bool {
	for i := range len(v) {
		if isControlChar(v[i]) {
			return true
		}
	}
	return false
}

// isControlChar is is_control_character (src/common/utf8.c:216-219 at
// v19.2.6 and v20.2.4): a byte below 0x20 other than NUL, or DEL.
func isControlChar(c byte) bool { return c != 0 && c < 0x20 || c == 0x7f }
