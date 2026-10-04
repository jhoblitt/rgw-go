package auth

import (
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // radosgw's SigV2 signs with HMAC-SHA1; the protocol fixes the hash
	"encoding/base64"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/vhost"
)

var (
	// signedSubresourcesV2 is signed_subresources (rgw_auth_s3.cc:31-60 at
	// v19.2.6, :32-61 at v20.2.4): the SigV2 canonical resource appends the
	// ones the request carries, in this order. RGWHTTPArgs::append never files
	// encryption or object-lock as a sub-resource (rgw_common.cc:925-975 at
	// v19.2.6, :938-988 at v20.2.4), so neither is ever signed, as in radosgw
	// (docs/ceph-upstream-bugs.md).
	signedSubresourcesV2 = []string{
		"acl", "cors", "delete", "encryption", "lifecycle", "location", "logging",
		"notification", "partNumber", "policy", "policyStatus", "publicAccessBlock", "requestPayment",
		"response-cache-control", "response-content-disposition", "response-content-encoding",
		"response-content-language", "response-content-type", "response-expires", "tagging", "torrent",
		"uploadId", "uploads", "versionId", "versioning", "versions", "website", "object-lock",
	}

	// amzHeaderPrefixes is meta_prefixes (rgw_common.cc:413-420 at v19.2.6,
	// :426-433 at v20.2.4) as header names: a header with any of them is filed
	// under x-amz- and the rest of its name.
	amzHeaderPrefixes = []string{"x-amz-", "x-goog-", "x-dho-", "x-rgw-", "x-object-", "x-container-", "x-account-"}
)

// authDataV2 is AWSGeneralAbstractor::get_auth_data_v2 (rgw_rest_s3.cc
// :6033-6113 at v19.2.6, :6604-6684 at v20.2.4). Without a non-empty
// Authorization header the credentials are the query's AWSAccessKeyId and
// Signature, and Expires must be present and later than now in whole seconds;
// otherwise the header after "AWS " splits at its last ':' into key and
// signature, both empty without one. An x-amz-security-token present but
// empty, in the query or the headers respectively, is refused. Only a
// header-signed request has its date checked for skew. Every refusal but the
// skew is EPERM, with no message.
func authDataV2(rv *requestView, cfg *Config, now time.Time) (*authData, error) {
	d := &authData{payload: payloadClass{kind: payloadNone}}
	if a, _ := rv.header("authorization"); a == "" {
		d.presigned = true
		d.accessKey, _ = rv.param("AWSAccessKeyId")
		d.signature, _ = rv.param("Signature")
		expires, _ := rv.param("Expires")
		if expires == "" {
			return nil, fmt.Errorf("%w: presigned url without Expires", op.ErrAccessDenied)
		}
		if now.Unix() >= atoll(expires) {
			return nil, fmt.Errorf("%w: presigned url expired", op.ErrAccessDenied)
		}
		if tok, ok := rv.param("x-amz-security-token"); ok && tok == "" {
			return nil, fmt.Errorf("%w: empty security token", op.ErrAccessDenied)
		}
	} else {
		// discoverFlavour routes here only on the "AWS " prefix, which
		// radosgw skips unread.
		creds := strings.TrimPrefix(a, "AWS ")
		if i := strings.LastIndexByte(creds, ':'); i >= 0 {
			d.accessKey, d.signature = creds[:i], creds[i+1:]
		}
		if tok, ok := rv.header("x-amz-security-token"); ok && tok == "" {
			return nil, fmt.Errorf("%w: empty security token", op.ErrAccessDenied)
		}
	}
	sts, headerTime, err := canonicalStringV2(rv, d.presigned, cfg.DNSNames)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", op.ErrAccessDenied, err)
	}
	if !d.presigned && !timeSkewOK(now, headerTime) {
		return nil, op.ErrRequestTimeTooSkewed
	}
	d.stringToSign = sts
	d.sign = func(secret string) (string, error) { return signatureV2(secret, sts) }
	return d, nil
}

// canonicalStringV2 is rgw_create_s3_canonical_header (rgw_auth_s3.cc
// :193-260 at v19.2.6, :194-263 at v20.2.4) and the string it builds
// (:130-169, :131-170): the method, Content-MD5 and Content-Type as sent or
// empty, and the date line, each ended by a newline; then the amz headers, on
// the query route the query's meta, and the canonical resource. A Content-MD5
// with a byte that is neither base64 nor whitespace is refused. On the query
// route the date line is Expires, cut at a NUL as radosgw appends it as a C
// string. On the header route an x-amz-date empties the date line and is the
// request date, which is otherwise the Date header, and that is also the date
// line. headerTime is the request date's instant, zero on the query route.
// The method is the request's; at v20.2.4 radosgw signs an OPTIONS CORS
// request over its Access-Control-Request-Method instead
// (get_canonical_method, :258 and :1749-1776 at v20.2.4).
func canonicalStringV2(rv *requestView, presigned bool, dnsNames []string) (sts string, headerTime time.Time, err error) {
	md5, _ := rv.header("content-md5")
	if !isBase64Charset(md5) {
		return "", time.Time{}, errors.New("content-md5 is not base64")
	}
	ctype, _ := rv.header("content-type")
	var (
		date string
		qs   map[string]string
	)
	if presigned {
		qs = qsMetaV2(rv)
		date, _ = rv.param("Expires")
	} else {
		reqDate, ok := rv.header("x-amz-date")
		if !ok {
			if reqDate, ok = rv.header("date"); !ok {
				return "", time.Time{}, errors.New("missing request date")
			}
			date = reqDate
		}
		if headerTime, err = v2RequestTime(reqDate); err != nil {
			return "", time.Time{}, err
		}
	}
	var b strings.Builder
	for _, line := range []string{rv.req.Method, md5, ctype, cString(date)} {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	writeCanonAmz(&b, amzHeadersV2(rv))
	writeCanonAmz(&b, qs)
	b.WriteString(canonicalResourceV2(rv, dnsNames))
	return b.String(), headerTime, nil
}

// v2RequestTime is the header time rgw_create_s3_canonical_header takes from
// the request date (rgw_auth_s3.cc:230-243 at v19.2.6, :232-245 at v20.2.4):
// parse_rfc2616, else parse_iso8601 without the extended format, and a year
// before 1970 refused as strptime read it, before second 60 or a zone offset
// moves the instant across the year's edge. The instant is kept whole where
// radosgw keeps 32 bits of its seconds, so a Date far in the future is skewed
// here (docs/exclusions.md).
func v2RequestTime(date string) (time.Time, error) {
	t, year, ok := rfc2616Date(date)
	if !ok {
		t, year, ok = iso8601BasicDate(date)
	}
	switch {
	case !ok:
		return time.Time{}, errors.New("unparsable request date")
	case year < 1970:
		return time.Time{}, errors.New("request date before 1970")
	}
	return t, nil
}

// writeCanonAmz is get_canon_amz_hdrs (rgw_auth_s3.cc:66-86 at v19.2.6, :67-87
// at v20.2.4): "key:value\n" in key order.
func writeCanonAmz(b *strings.Builder, m map[string]string) {
	for _, k := range slices.Sorted(maps.Keys(m)) {
		b.WriteString(k)
		b.WriteByte(':')
		b.WriteString(m[k])
		b.WriteByte('\n')
	}
}

// amzHeadersV2 is the x_meta_map req_info::init_meta_info builds
// (rgw_common.cc:422-464 at v19.2.6, :435-477 at v20.2.4) from beast's HTTP_
// names, each header's name upper-cased with '-' and '_' swapped
// (rgw_asio_client.cc:36-66 at v19.2.6 and v20.2.4). A name with a meta prefix
// is filed under x-amz- and the rest of the name, lower-cased with the swap
// undone, so x-goog-meta-q files as x-amz-meta-q. The value is the header's
// last, the one RGWEnv::set keeps. Names filed under one key join as
// rgw_add_amz_meta_header joins, in the order RGWEnv's strcasecmp gives their
// HTTP_ names; they differ only in their prefixes, whose first differing
// bytes are letters, so their lower-cased names sort the same. x-amz-date is
// among them, which is how it reaches the string to sign.
func amzHeadersV2(rv *requestView) map[string]string {
	type entry struct{ lower, key, val string }
	var entries []entry
	for name, vals := range rv.req.Header {
		lower := asciiLower(name)
		for _, prefix := range amzHeaderPrefixes {
			if rest, ok := strings.CutPrefix(lower, prefix); ok && len(vals) > 0 {
				entries = append(entries, entry{lower, "x-amz-" + rest, vals[len(vals)-1]})
				break
			}
		}
	}
	slices.SortFunc(entries, func(a, b entry) int { return strings.Compare(a.lower, b.lower) })
	m := map[string]string{}
	for _, e := range entries {
		addAmzMeta(m, e.key, e.val)
	}
	return m
}

// qsMetaV2 is get_v2_qs_map (rgw_auth_s3.cc:175-187 at v19.2.6, :176-188 at
// v20.2.4). It walks RGWHTTPArgs' std::map, in byte order of the names as
// parsed: a parameter whose lower-cased name starts with x-amz-meta- joins
// that name as rgw_add_amz_meta_header joins, and one whose lower-cased name
// is x-amz-security-token replaces any before it.
func qsMetaV2(rv *requestView) map[string]string {
	m := map[string]string{}
	for _, k := range slices.Sorted(maps.Keys(rv.params)) {
		lower := asciiLower(k)
		switch {
		case strings.HasPrefix(lower, "x-amz-meta-"):
			addAmzMeta(m, lower, rv.params[k])
		case lower == "x-amz-security-token":
			m[lower] = rv.params[k]
		}
	}
	return m
}

// addAmzMeta is rgw_add_amz_meta_header (rgw_common.cc:472-487 at v19.2.6,
// :485-500 at v20.2.4), which init_meta_info repeats inline: a second value
// for k follows the first, right-trimmed, after a comma.
func addAmzMeta(m map[string]string, k, v string) {
	if old, ok := m[k]; ok {
		for old != "" && isSpace(old[len(old)-1]) {
			old = old[:len(old)-1]
		}
		v = old + "," + v
	}
	m[k] = v
}

// canonicalResourceV2 is get_canon_resource (rgw_auth_s3.cc:91-124 at
// v19.2.6, :92-125 at v20.2.4) over req_info.request_uri as
// RGWREST::preprocess leaves it: the path as sent, after '/' and the bucket
// the Host names, the one the S3 handler routes by, with a '/' between them
// when the path lacks one (rgw_rest.cc:2154-2161 at v19.2.6, :2171-2178 at
// v20.2.4). Each sub-resource of signedSubresourcesV2 the request carries
// follows in that order, after '?' for the first and '&' for the rest, with
// '=' and its decoded value unless that is empty.
func canonicalResourceV2(rv *requestView, dnsNames []string) string {
	var b strings.Builder
	if bucket := vhost.Bucket(vhost.Hostname(rv.req.Host), dnsNames); bucket != "" {
		b.WriteByte('/')
		b.WriteString(bucket)
		if !strings.HasPrefix(rv.rawPath, "/") {
			b.WriteByte('/')
		}
	}
	b.WriteString(rv.rawPath)
	sep := byte('?')
	for _, name := range signedSubresourcesV2 {
		v, ok := rv.subres[name]
		if !ok {
			continue
		}
		b.WriteByte(sep)
		sep = '&'
		b.WriteString(name)
		if v != "" {
			b.WriteByte('=')
			b.WriteString(v)
		}
	}
	return b.String()
}

// signatureV2 is get_v2_signature (rgw_auth_s3.cc:1068-1093 at v19.2.6,
// :1045-1070 at v20.2.4): base64 of HMAC-SHA1 over the string to sign, keyed
// with the secret's bytes. An empty secret is EINVAL.
func signatureV2(secret, stringToSign string) (string, error) {
	if secret == "" {
		return "", fmt.Errorf("%w: empty secret key", op.ErrInvalidArgument)
	}
	h := hmac.New(sha1.New, []byte(secret))
	h.Write([]byte(stringToSign))
	return base64.StdEncoding.EncodeToString(h.Sum(nil)), nil
}
