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
	"github.com/jhoblitt/rgw-go/internal/rgwtext"
	"github.com/jhoblitt/rgw-go/internal/vhost"
)

// signedSubresourcesV2 is signed_subresources
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_s3.cc#L31-L60 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L32-L61 at v20.2.4):
// the SigV2 canonical resource appends the ones the request carries, in
// this order. RGWHTTPArgs::append never files encryption or object-lock as
// a sub-resource
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L925-L975 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L938-L988 at v20.2.4),
// so neither is ever signed, as in radosgw (docs/ceph-upstream-bugs.md).
var signedSubresourcesV2 = []string{
	"acl", "cors", "delete", "encryption", "lifecycle", "location", "logging",
	"notification", "partNumber", "policy", "policyStatus", "publicAccessBlock", "requestPayment",
	"response-cache-control", "response-content-disposition", "response-content-encoding",
	"response-content-language", "response-content-type", "response-expires", "tagging", "torrent",
	"uploadId", "uploads", "versionId", "versioning", "versions", "website", "object-lock",
}

// authDataV2 is AWSGeneralAbstractor::get_auth_data_v2
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L6033-L6113 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L6604-L6684 at v20.2.4).
// Without a non-empty Authorization header the credentials are the query's
// AWSAccessKeyId and Signature, and Expires must be present and later than
// now in whole seconds; otherwise the header after "AWS " splits at its last
// ':' into key and signature, both empty without one. An x-amz-security-token
// present but empty, in the query or the headers respectively, is refused.
// Only a header-signed request has its date checked for skew. Every refusal
// but the skew and a preflight's method is EPERM, with no message.
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
		if now.Unix() >= rgwtext.Atoll(expires) {
			return nil, fmt.Errorf("%w: presigned url expired", op.ErrAccessDenied)
		}
		if tok, ok := rv.param("x-amz-security-token"); ok && tok == "" {
			return nil, fmt.Errorf("%w: empty security token", op.ErrAccessDenied)
		}
	} else {
		// discoverFlavour routes here only on the "AWS " prefix, which
		// radosgw skips unread.
		creds := strings.TrimPrefix(a, "AWS ")
		if accessKey, signature, ok := strings.CutLast(creds, ":"); ok {
			d.accessKey, d.signature = accessKey, signature
		}
		if tok, ok := rv.header("x-amz-security-token"); ok && tok == "" {
			return nil, fmt.Errorf("%w: empty security token", op.ErrAccessDenied)
		}
	}
	sts, headerTime, err := canonicalStringV2(rv, d.presigned, cfg.DNSNames)
	if err != nil {
		return nil, err
	}
	if !d.presigned && !timeSkewOK(now, headerTime) {
		return nil, op.ErrRequestTimeTooSkewed
	}
	d.stringToSign = sts
	d.sign = func(secret string) (string, error) { return signatureV2(secret, sts) }
	return d, nil
}

// canonicalStringV2 is rgw_create_s3_canonical_header
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_s3.cc#L193-L260 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L194-L263 at v20.2.4)
// and the string it builds
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_s3.cc#L130-L169,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L131-L170):
// the method, Content-MD5 and Content-Type as sent or empty, and the date
// line, each ended by a newline; then the amz headers, on the query route
// the query's meta, and the canonical resource. A Content-MD5 with a byte
// that is neither base64 nor whitespace is refused. On the query route the
// date line is Expires, cut at a NUL as radosgw appends it as a C string. On
// the header route an x-amz-date empties the date line and is the request
// date, which is otherwise the Date header, and that is also the date line.
// headerTime is the request date's instant, zero on the query route. The
// method line is canonicalMethod's, read once Content-MD5 and the date have
// passed, as v20.2.4 reads it
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L258 at v20.2.4),
// so a preflight's missing method is refused before the caller's skew check.
// v19.2.7 signs a SigV2 preflight over OPTIONS and refuses no method
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_auth_s3.cc#L256 at v19.2.7);
// rgw-go takes Tentacle's rule on both releases (docs/exclusions.md). The
// method's refusal is EINVAL, every other EPERM.
func canonicalStringV2(rv *requestView, presigned bool, dnsNames []string) (sts string, headerTime time.Time, err error) {
	md5, _ := rv.header("content-md5")
	if !isBase64Charset(md5) {
		return "", time.Time{}, fmt.Errorf("%w: content-md5 is not base64", op.ErrAccessDenied)
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
				return "", time.Time{}, fmt.Errorf("%w: missing request date", op.ErrAccessDenied)
			}
			date = reqDate
		}
		if headerTime, err = v2RequestTime(reqDate); err != nil {
			return "", time.Time{}, fmt.Errorf("%w: %w", op.ErrAccessDenied, err)
		}
	}
	method, err := canonicalMethod(rv)
	if err != nil {
		return "", time.Time{}, err
	}
	var b strings.Builder
	for _, line := range []string{method, md5, ctype, rgwtext.CString(date)} {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	writeCanonAmz(&b, amzHeadersV2(rv))
	writeCanonAmz(&b, qs)
	b.WriteString(canonicalResourceV2(rv, dnsNames))
	return b.String(), headerTime, nil
}

// v2RequestTime is the header time rgw_create_s3_canonical_header takes from
// the request date
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_s3.cc#L230-L243 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L232-L245 at v20.2.4):
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

// writeCanonAmz is get_canon_amz_hdrs
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_s3.cc#L66-L86 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L67-L87 at v20.2.4):
// "key:value\n" in key order.
func writeCanonAmz(b *strings.Builder, m map[string]string) {
	for _, k := range slices.Sorted(maps.Keys(m)) {
		b.WriteString(k)
		b.WriteByte(':')
		b.WriteString(m[k])
		b.WriteByte('\n')
	}
}

// amzHeadersV2 is the x_meta_map req_info::init_meta_info builds
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L422-L464 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L435-L477 at v20.2.4)
// from the headers whose name begins x-amz- in any case, each under its name
// lower-cased, as beast's HTTP_ name, upper-cased with '-' and '_' swapped,
// comes back from it
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_asio_client.cc#L36-L66 at v19.2.6
// and v20.2.4). init_meta_info files a header under six other meta prefixes
// there too, under its x-amz- name; rgw-go's S3 API reads none of them, so
// none is signed (docs/exclusions.md). The value is the header's last, the one
// RGWEnv::set keeps. beast files spellings of one name under one HTTP_ name,
// where the last set wins; of spellings the header map keeps apart, which
// net/http's server does not as it canonicalizes each name, the last in byte
// order wins, as internal/s3's requestEnv keeps one, so the line signed is the
// value stored. x-amz-date is among them, which is how it reaches the string
// to sign.
func amzHeadersV2(rv *requestView) map[string]string {
	m := map[string]string{}
	for _, name := range slices.Sorted(maps.Keys(rv.req.Header)) {
		vals := rv.req.Header[name]
		if lower := asciiLower(name); strings.HasPrefix(lower, "x-amz-") && len(vals) > 0 {
			m[lower] = vals[len(vals)-1]
		}
	}
	return m
}

// qsMetaV2 is get_v2_qs_map
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_s3.cc#L175-L187 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L176-L188 at v20.2.4).
// It walks RGWHTTPArgs' std::map, in byte order of the names as parsed: a
// parameter whose lower-cased name starts with x-amz-meta- joins that name
// as rgw_add_amz_meta_header joins, and one whose lower-cased name is
// x-amz-security-token replaces any before it.
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

// queryMetaV2 is a presigned SigV2 request's qsMetaV2, for
// AuthResult.QueryMeta, once Verifier.Authenticate has authenticated the
// request. The string to sign runs the query's lines straight on from the
// headers', each set sorted on its own
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_auth_s3.cc#L164-L165 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L165-L166 at v20.2.4),
// so a run of x-amz-meta-* or x-amz-security-token lines can move between the end of
// the headers and the start of the query with the signature kept. radosgw stores
// query meta on a PutObject or an UploadPart alone and reads the session token from
// the header alone
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L904-L909 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L939-L944 at v20.2.4),
// so the two forms mean different things (docs/ceph-upstream-bugs.md). rgw-go has
// every read take them as one set, on both releases (docs/exclusions.md), and refuses
// what does not make one: a name the string to sign carries from both sides, and an
// empty session token, in the headers or under a query name in any case, where
// authDataV2 refuses only the query's x-amz-security-token spelt so. It also refuses
// a query line whose name or value, url-decoded, holds a CR or LF: get_canon_amz_hdrs
// writes each line unescaped
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_auth_s3.cc#L66-L86 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L67-L87 at v20.2.4),
// so such a parameter signs as the several lines it spells, signed header lines or a
// token among them, while every read takes it as one (docs/ceph-upstream-bugs.md). A
// header value cannot carry either: net/http joins a continuation line with a space
// and answers 400 to a value holding any other control character. And it refuses a
// query meta name holding a colon, which moves the line's first colon, the end of its
// name, into the name: x-amz-meta-a:x=y signs as an x-amz-meta-a of x:y. No
// header name holds one. The separator refusals cover the packed form alone: a signer
// who signs a value holding a line break has also signed the request with its
// lines split out, which carries the same signature and is served.
func queryMetaV2(rv *requestView) (map[string]string, error) {
	qs := qsMetaV2(rv)
	hdr := amzHeadersV2(rv)
	for _, name := range slices.Sorted(maps.Keys(qs)) {
		if strings.ContainsAny(name, "\r\n") || strings.ContainsAny(qs[name], "\r\n") {
			return nil, fmt.Errorf("query parameter %q signs a line break", name)
		}
		if strings.Contains(name, ":") {
			return nil, fmt.Errorf("query parameter %q signs a colon in its name", name)
		}
		if _, ok := hdr[name]; ok {
			return nil, fmt.Errorf("%s is signed in both the headers and the query", name)
		}
	}
	if tok, ok := rv.header("x-amz-security-token"); ok && tok == "" {
		return nil, errors.New("empty security token")
	}
	if tok, ok := qs["x-amz-security-token"]; ok && tok == "" {
		return nil, errors.New("empty security token in the query")
	}
	return qs, nil
}

// expiresLineBreak refuses, once Verifier.Authenticate has authenticated a
// presigned SigV2 request, an Expires holding a CR or LF. Expires is the date
// line, written as sent up to a NUL
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_auth_s3.cc#L217 and
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_auth_s3.cc#L159-L162 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L219 and
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L160-L163 at v20.2.4),
// and its expiry is read by atoll, which stops at the first byte that is no
// digit, so an Expires of 1441000000%0Ax-amz-copy-source:b/src passes and
// signs as the header X-Amz-Copy-Source: b/src does, which the request no
// longer carries (docs/ceph-upstream-bugs.md).
func expiresLineBreak(rv *requestView) error {
	if expires, _ := rv.param("Expires"); strings.ContainsAny(expires, "\r\n") {
		return errors.New("the Expires parameter signs a line break")
	}
	return nil
}

// packedSubresourceV2 refuses, once Verifier.Authenticate has authenticated a
// SigV2 request, a signed sub-resource whose value, url-decoded, spells a
// sub-resource canonicalResourceV2 can write after it: '&', its name, and '&'
// or the value's end, or '=' for one parseQuery files with its value, which an
// admin sub-resource is not. get_canon_resource joins the sub-resources with
// '&' and writes each value unescaped
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_auth_s3.cc#L91-L124 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L92-L125 at v20.2.4),
// so partNumber=1%26uploadId%3DU signs as the partNumber and uploadId of an
// UploadPart while it names no upload, and a response-content-disposition can
// carry a GET's signed versionId away (docs/ceph-upstream-bugs.md). Any other
// '&', as in a filename, still passes, as no other request signs the same
// resource. The refusal covers the packed form alone: a signer who signs a
// value spelling a later sub-resource has also signed the request that carries
// it apart, which is served.
func packedSubresourceV2(rv *requestView) error {
	for i, name := range signedSubresourcesV2 {
		v, ok := rv.subres[name]
		if !ok {
			continue
		}
		for _, later := range signedSubresourcesV2[i+1:] {
			withValue := subResources[later]
			if !withValue && !adminSubResources[later] {
				continue
			}
			for rest := v; ; {
				_, after, found := strings.Cut(rest, "&"+later)
				if !found {
					break
				}
				if after == "" || after[0] == '&' || after[0] == '=' && withValue {
					return fmt.Errorf("sub-resource %s's value spells the sub-resource %s", name, later)
				}
				rest = after
			}
		}
	}
	return nil
}

// addAmzMeta is rgw_add_amz_meta_header
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L472-L487 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L485-L500 at v20.2.4),
// which init_meta_info repeats inline: a second value for k follows the
// first, right-trimmed, after a comma.
func addAmzMeta(m map[string]string, k, v string) {
	if old, ok := m[k]; ok {
		for old != "" && isSpace(old[len(old)-1]) {
			old = old[:len(old)-1]
		}
		v = old + "," + v
	}
	m[k] = v
}

// canonicalResourceV2 is get_canon_resource
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_s3.cc#L91-L124 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L92-L125 at v20.2.4)
// over req_info.request_uri as RGWREST::preprocess leaves it: the path as
// sent, after '/' and the bucket the Host names, the one the S3 handler
// routes by, with a '/' between them when the path lacks one
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L2154-L2161 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L2171-L2178 at v20.2.4).
// Each sub-resource of signedSubresourcesV2 the request carries follows in
// that order, after '?' for the first and '&' for the rest, with '=' and
// its decoded value unless that is empty.
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

// signatureV2 is get_v2_signature
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_s3.cc#L1068-L1093 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L1045-L1070 at v20.2.4):
// base64 of HMAC-SHA1 over the string to sign, keyed with the secret's
// bytes. An empty secret is EINVAL.
func signatureV2(secret, stringToSign string) (string, error) {
	if secret == "" {
		return "", fmt.Errorf("%w: empty secret key", op.ErrInvalidArgument)
	}
	h := hmac.New(sha1.New, []byte(secret))
	h.Write([]byte(stringToSign))
	return base64.StdEncoding.EncodeToString(h.Sum(nil)), nil
}
