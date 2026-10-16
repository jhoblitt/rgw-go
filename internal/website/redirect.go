package website

import (
	"errors"
	"fmt"
	"strings"

	"github.com/jhoblitt/rgw-go/internal/meta"
)

// redirectCode is WebsiteRedirect's status
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L58 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L59 at v20.2.4),
// a redirect's code when its rule names none.
const redirectCode = 301

// EffectiveKey is RGWBucketWebsiteConf::get_effective_key
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_website.cc#L116-L133 at v19.2.7 and v20.2.4):
// the object a website request for key reads. Without an index suffix there
// is none. The empty key is the suffix, a key ending in "/" is key+suffix, a
// key that is not a file is key+"/"+suffix, and a file is the key itself.
func EffectiveKey(conf meta.WebsiteConf, key string, isFile bool) (string, bool) {
	suffix := conf.IndexDocSuffix
	switch {
	case suffix == "":
		return "", false
	case key == "":
		return suffix, true
	case strings.HasSuffix(key, "/"):
		return key + suffix, true
	case !isFile:
		return key + "/" + suffix, true
	}
	return key, true
}

// Match is RGWBucketWebsiteConf::should_redirect
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_website.cc#L98-L114 at v19.2.7 and v20.2.4):
// a RedirectAllRequestsTo that names a host, as a rule that redirects every
// key; else the first routing rule whose KeyPrefixEquals prefixes key and
// whose HttpErrorCodeReturnedEquals is status, compared as radosgw compares
// them, the status cast to u16
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_website.h#L105-L107 at v19.2.7 and v20.2.4),
// so a rule naming no error code matches only status 0.
//
// The redirect-all rule carries the code radosgw answers. should_redirect
// copies the stored code into the rule, which apply_rule answers when it is
// above 0, and then writes 301 into the configuration
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_website.cc#L101-L106
// and
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_website.cc#L61-L62 at v19.2.7 and v20.2.4).
// With no index suffix stored, as S3's PutBucketWebsite stores a
// redirect-all, retarget fails at get_effective_key
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L5409-L5414 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5935-L5940 at v20.2.4),
// so the website error handler's call is the first, and the stored code
// stands; one of 0 leaves WebsiteRedirect's 301. With a suffix stored,
// retarget's call comes first, and the error handler, which runs on the
// WebsiteRedirect it returns, copies the 301 written into the configuration
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L5545-L5567 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L6071-L6093 at v20.2.4).
func Match(conf meta.WebsiteConf, key string, status int) (meta.RoutingRule, bool) {
	if ra := conf.RedirectAll; ra.Hostname != "" {
		if conf.IndexDocSuffix != "" {
			ra.HTTPRedirectCode = redirectCode
		}
		return meta.RoutingRule{RedirectInfo: meta.RoutingRedirect{Redirect: ra}}, true
	}
	code := uint16(status) //nolint:gosec // radosgw casts the int status to uint16_t
	for _, r := range conf.RoutingRules {
		if strings.HasPrefix(key, r.Condition.KeyPrefixEquals) && r.Condition.HTTPErrorCodeReturnedEquals == code {
			return r, true
		}
	}
	return meta.RoutingRule{}, false
}

// Apply is RGWBWRoutingRule::apply_rule
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_website.cc#L40-L63 at v19.2.7 and v20.2.4):
// the Location r redirects key to, and its code. The Location is the rule's
// protocol, else proto, "://", the rule's host, else host, "/", then the new
// key percent-encoded: ReplaceKeyPrefixWith followed by key less as many
// bytes as KeyPrefixEquals holds, else ReplaceKeyWith, else key, an empty
// replacement counting as none. The code is the rule's HttpRedirectCode,
// else 301.
//
// radosgw matches a website GET's rule on the effective key and applies it to
// the request's key, which need not start with the prefix
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L5419-L5429 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5945-L5955 at v20.2.4),
// so Apply cuts the prefix by its length alone, as apply_rule does.
// apply_rule writes the new key raw, so a key's CR and LF reach the header,
// and its ?, # and % reshape the URL; rgw-go encodes it as urlEncode does
// (docs/exclusions.md, "A website redirect's Location carries the new key
// percent-encoded").
//
// apply_rule writes the protocol and the host raw too
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_website.cc#L45-L59 at v19.2.7 and v20.2.4).
// A host holding a byte at or below 0x20, or 0x7F, or a protocol other than
// http or https, is ErrUnsafeRedirect, as a configuration radosgw stored can
// hold either (docs/exclusions.md, "A website redirect is refused for a host
// holding a control byte or a protocol other than http or https").
func Apply(r meta.RoutingRule, proto, host, key string) (url string, code int, err error) {
	ri := r.RedirectInfo
	if ri.Redirect.Protocol != "" {
		proto = ri.Redirect.Protocol
	}
	if ri.Redirect.Hostname != "" {
		host = ri.Redirect.Hostname
	}
	switch {
	case !hostSafe(host):
		return "", 0, fmt.Errorf("%w: the host %.64q holds a byte at or below 0x20, or 0x7F", ErrUnsafeRedirect, host)
	case proto != "http" && proto != "https":
		return "", 0, fmt.Errorf("%w: the protocol %.64q is not http or https", ErrUnsafeRedirect, proto)
	}
	var newKey string
	switch prefix := r.Condition.KeyPrefixEquals; {
	case ri.ReplaceKeyPrefixWith != "":
		newKey = ri.ReplaceKeyPrefixWith
		if len(key) > len(prefix) {
			newKey += key[len(prefix):]
		}
	case ri.ReplaceKeyWith != "":
		newKey = ri.ReplaceKeyWith
	default:
		newKey = key
	}
	url = proto + "://" + host + "/" + urlEncode(newKey)
	code = redirectCode
	if ri.Redirect.HTTPRedirectCode > 0 {
		code = int(ri.Redirect.HTTPRedirectCode)
	}
	return url, code, nil
}

// Redirect is Match on key and status, then Apply on key: the Location and
// code of the redirect conf answers for key at status, which is 0 for a
// website GET and the error's HTTP status in the website error handler. proto
// is the request's, http or https, and host its Host header, empty when it
// has none. Apply's ErrUnsafeRedirect is returned as err, ok false.
func Redirect(conf meta.WebsiteConf, key string, status int, proto, host string) (url string, code int, ok bool, err error) {
	r, ok := Match(conf, key, status)
	if !ok {
		return "", 0, false, nil
	}
	url, code, err = Apply(r, proto, host, key)
	if err != nil {
		return "", 0, false, err
	}
	return url, code, true, nil
}

// ErrUnsafeRedirect is a redirect Apply declines to build, whose host or
// protocol would put other bytes into the Location header than a host and a
// scheme. radosgw keeps the CR and LF a HostName's character references
// decode to (decode_xml_obj,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_xml.h#L179-L182 at v19.2.7 and v20.2.4),
// and apply_rule writes the host into the Location raw. A caller answers it
// as an internal error, so such a redirect fails closed.
var ErrUnsafeRedirect = errors.New("website: unsafe redirect")

// hostSafe reports whether host holds no byte at or below 0x20 and no 0x7F.
func hostSafe(host string) bool {
	return !strings.ContainsFunc(host, func(c rune) bool { return c <= 0x20 || c == 0x7f })
}

// urlEncode is radosgw's url_encode(s, false), the encoding it gives a key
// elsewhere, as in a listing's encoding-type=url, over
// char_needs_url_encoding
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L1736-L1793 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L1799-L1856 at v20.2.4):
// a byte at or below 0x20, at or above 0x7F, or one of
// " # % & + , : ; < = > ? @ [ \ ] ^ ` { } is %XX in upper-case hex, and every
// other byte, '/' among them, is copied. It is op.URLEncode with the slash
// kept; this package sits below op, so it does not import it.
func urlEncode(s string) string {
	const hexDigits = "0123456789ABCDEF"
	var b strings.Builder
	for i := range len(s) {
		c := s[i]
		if !urlByteNeedsEncoding(c) {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hexDigits[c>>4])
		b.WriteByte(hexDigits[c&0x0f])
	}
	return b.String()
}

func urlByteNeedsEncoding(c byte) bool {
	if c <= 0x20 || c >= 0x7F {
		return true
	}
	return strings.IndexByte(`"#%&+,:;<=>?@[\]^`+"`{}", c) >= 0
}
