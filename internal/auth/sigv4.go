package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/jhoblitt/rgw-go/internal/op"
)

const (
	// aws4Request is the literal the signing key's last HMAC takes, whatever
	// the scope's fourth field says, and which a credential must contain
	// somewhere.
	aws4Request = "aws4_request"
	// authGrace is RGW_AUTH_GRACE (rgw_auth_s3.h:30 at v19.2.6 and v20.2.4).
	authGrace = 15 * time.Minute
	// unsignedPayload is AWS4_UNSIGNED_PAYLOAD_HASH (rgw_auth_s3.h:527 at
	// v19.2.6, :530 at v20.2.4).
	unsignedPayload = "UNSIGNED-PAYLOAD"
	// maxPresignExpires is the longest X-Amz-Expires, seven days in seconds
	// (rgw_auth_s3.cc:309 at v19.2.6, :312 at v20.2.4).
	maxPresignExpires = 7 * 24 * 60 * 60
)

// v4Credentials is what parse_v4_credentials yields (rgw_auth_s3.cc:538-584
// at v19.2.6, :544-590 at v20.2.4).
type v4Credentials struct {
	accessKey string // before the credential's first '/'
	scope     string // after it: "YYYYMMDD/region/service/aws4_request", as sent
	// signedHeaders goes into the canonical request as sent.
	signedHeaders string
	signature     string // hex, as sent
	date          string // the x-amz-date or Date value, as sent
	// sessionToken is x-amz-security-token, which the local engine ignores.
	sessionToken string
}

// authData is AWSEngine::VersionAbstractor::auth_data_t: what an engine
// needs to compare signatures, and the payload hash the body is held to.
type authData struct {
	accessKey string
	signature string // the client's
	// canonicalRequest and stringToSign are kept for the specs; like
	// everything here that a signature is derived from, they are never
	// logged.
	canonicalRequest string
	stringToSign     string
	// sign is the signature_factory: the server's signature for a secret.
	sign func(secret string) (string, error)
	// altSign is a fallback signature, nil when there is none.
	altSign     func(secret string) (string, error)
	v4          *v4Credentials // nil for v2
	presigned   bool
	payloadHash string // x-amz-content-sha256 or UNSIGNED-PAYLOAD
	payload     payloadClass
}

// verify reports whether the client's signature is the server's for secret,
// or else the fallback's. radosgw compares the strings byte for byte, so
// upper-case hex never matches (rgw_rest_s3.cc:6362-6375 at v19.2.6,
// :6933-6946 at v20.2.4); here the comparison runs in constant time.
func (d *authData) verify(secret string) (bool, error) {
	got, err := d.sign(secret)
	if err != nil {
		return false, err
	}
	if subtle.ConstantTimeCompare([]byte(got), []byte(d.signature)) == 1 {
		return true, nil
	}
	if d.altSign == nil {
		return false, nil
	}
	alt, err := d.altSign(secret)
	if err != nil {
		return false, err
	}
	return subtle.ConstantTimeCompare([]byte(alt), []byte(d.signature)) == 1, nil
}

// timeSkewOK is is_time_skew_ok (rgw_auth_s3.cc:265-278 at v19.2.6, :268-281
// at v20.2.4): t within authGrace of now either way, the bound included.
func timeSkewOK(now, t time.Time) bool { return now.Sub(t).Abs() <= authGrace }

// parseV4Header is parse_v4_auth_header (rgw_auth_s3.cc:390-467 at v19.2.6,
// :393-470 at v20.2.4) and the credential split of parse_v4_credentials. The
// algorithm name and the byte after it are skipped unread, since the prefix
// chose this route. Each ','-separated token splits at its first '=' with
// both sides trimmed (parse_key_value, rgw_common.cc:681-694 at v19.2.6,
// :694-707 at v20.2.4); a repeated key keeps its last value. The date is
// checked, and its skew, before the credential's shape.
func parseV4Header(rv *requestView, now time.Time) (*v4Credentials, error) {
	a, _ := rv.header("authorization")
	if len(a) < len(aws4Algorithm)+1 {
		return nil, fmt.Errorf("%w: credentials string is too short", op.ErrInvalidArgument)
	}
	kv := map[string]string{}
	for tok := range strings.SplitSeq(a[len(aws4Algorithm)+1:], ",") {
		if tok == "" {
			continue
		}
		k, v, ok := strings.Cut(tok, "=")
		if !ok {
			return nil, fmt.Errorf("%w: malformed authorization header", op.ErrInvalidArgument)
		}
		kv[trimSpace(k)] = trimSpace(v)
	}
	for _, k := range []string{"Credential", "SignedHeaders", "Signature"} {
		if _, ok := kv[k]; !ok {
			return nil, fmt.Errorf("%w: authorization header missing %s", op.ErrInvalidArgument, k)
		}
	}
	c := &v4Credentials{signedHeaders: kv["SignedHeaders"], signature: kv["Signature"]}

	// An X-Amz-Date that does not parse is final: Date is read only when
	// X-Amz-Date is absent.
	d, ok := rv.header("x-amz-date")
	if !ok {
		d, ok = rv.header("date")
	}
	t, parsed := parseISO8601Basic(d)
	if !ok || !parsed {
		return nil, fmt.Errorf("%w: missing or malformed request date", op.ErrAccessDenied)
	}
	c.date = d
	if !timeSkewOK(now, t) {
		return nil, op.ErrRequestTimeTooSkewed
	}
	c.sessionToken, _ = rv.header("x-amz-security-token")
	if err := c.splitCredential(kv["Credential"]); err != nil {
		return nil, err
	}
	return c, nil
}

// splitCredential is parse_v4_credentials' check and split
// (rgw_auth_s3.cc:565-581 at v19.2.6, :571-587 at v20.2.4): exactly four
// '/' and "aws4_request" anywhere, not necessarily at the end.
func (c *v4Credentials) splitCredential(cred string) error {
	if strings.Count(cred, "/") != 4 || !strings.Contains(cred, aws4Request) {
		return fmt.Errorf("%w: malformed credential", op.ErrInvalidArgument)
	}
	c.accessKey, c.scope, _ = strings.Cut(cred, "/")
	return nil
}

// parseV4Query is parse_v4_query_string (rgw_auth_s3.cc:280-339 at v19.2.6,
// :283-342 at v20.2.4) and the credential split of parse_v4_credentials. A
// missing or empty parameter, a malformed date (EACCES on the header route)
// and an X-Amz-Expires that atoll reads outside [1, 604800] are all EPERM,
// as is an X-Amz-Security-Token present but empty. There is no skew check,
// so a URL dated in the future is valid from now. radosgw adds the date and
// the expiry in uint64_t, so a date more than the expiry before 1970 wraps
// past every clock and never expires. An expired URL is refused with the
// message Strategy::apply gives ERR_PRESIGNED_URL_EXPIRED (rgw_auth.cc:500-504
// at v19.2.6, :515-519 at v20.2.4).
func parseV4Query(rv *requestView, now time.Time) (*v4Credentials, error) {
	denied := func(what string) error { return fmt.Errorf("%w: presigned url %s", op.ErrAccessDenied, what) }
	cred, _ := rv.param("x-amz-credential")
	if cred == "" {
		return nil, denied("without a credential")
	}
	c := &v4Credentials{}
	c.date, _ = rv.param("x-amz-date")
	t, ok := parseISO8601Basic(c.date)
	if !ok {
		return nil, denied("without a valid date")
	}
	expires, _ := rv.param("x-amz-expires")
	if expires == "" {
		return nil, denied("without an expiry")
	}
	exp := atoll(expires)
	if exp < 1 || exp > maxPresignExpires {
		return nil, denied("with an expiry out of range")
	}
	reqSec := uint64(t.Unix()) //nolint:gosec // radosgw casts the time_t to uint64_t
	if presignClock(now) >= reqSec+uint64(exp) {
		return nil, op.ErrAccessDenied.WithMessage("The pre-signed URL has expired")
	}
	if c.signedHeaders, _ = rv.param("x-amz-signedheaders"); c.signedHeaders == "" {
		return nil, denied("without signed headers")
	}
	if c.signature, _ = rv.param("x-amz-signature"); c.signature == "" {
		return nil, denied("without a signature")
	}
	if tok, present := rv.param("x-amz-security-token"); present {
		if tok == "" {
			return nil, denied("with an empty security token")
		}
		c.sessionToken = tok
	}
	if err := c.splitCredential(cred); err != nil {
		return nil, err
	}
	return c, nil
}

// presignClock is now as parse_v4_query_string reads ceph_clock_now() into a
// uint64_t: through utime_t, which keeps 32 bits of seconds, and its operator
// double (include/utime.h:51, :230-232 at v19.2.6 and v20.2.4), so the
// nanoseconds are added in a double before the truncation to seconds, and a
// time a fraction of a microsecond short of the next second rounds up to it.
func presignClock(now time.Time) uint64 {
	sec := uint32(now.Unix()) //nolint:gosec // utime_t keeps 32 bits of seconds
	return uint64(float64(sec) + float64(now.Nanosecond())/1e9)
}

// boto2HostPort is the port the boto2 engine appends to a signed host
// (rgw_auth_s3.cc:770-783 at v19.2.6, :747-760 at v20.2.4): on a TLS listener
// SERVER_PORT_SECURE unless it is 443, otherwise SERVER_PORT unless it is 80;
// "" when nothing is appended.
func boto2HostPort(rv *requestView) string {
	port, secure := rv.localPort()
	if port == "" || secure && port == "443" || !secure && port == "80" {
		return ""
	}
	return port
}

// canonicalURIV4 is get_v4_canonical_uri (rgw_auth_s3.h:598-612 at v19.2.6,
// :601-615 at v20.2.4) over request_uri_aws4, the path as sent before a
// virtual-hosted bucket is prepended (rgw_rest.cc:2024 at v19.2.6, :2041 at
// v20.2.4). Its '+' to "%20" pass is not transcribed: aws4_uri_encode has
// already turned every '+' into "%2B".
func canonicalURIV4(rawPath string) string {
	if u := aws4Recode(rawPath, false); u != "" {
		return u
	}
	return "/"
}

// canonicalQueryV4 is get_v4_canonical_qs (rgw_auth_s3.cc:604-660 at
// v19.2.6, :610-666 at v20.2.4). Every '+' is a space before the split. A
// token without '=' is a key, untrimmed, with an empty value. A presigned
// query drops its X-Amz-Signature, matched as boost::iequals matches it,
// ignoring ASCII case only. Pairs are ordered by their encoded key alone, as
// a std::multimap orders them, so equal keys keep their order in the query.
func canonicalQueryV4(rawQuery string, presigned bool) string {
	if rawQuery == "" {
		return ""
	}
	type pair struct{ k, v string }
	var pairs []pair
	for tok := range strings.SplitSeq(strings.ReplaceAll(rawQuery, "+", "%20"), "&") {
		if tok == "" {
			continue
		}
		k, v := tok, ""
		if key, val, ok := strings.Cut(tok, "="); ok {
			k, v = trimSpace(key), trimSpace(val)
		}
		if presigned && asciiLower(k) == "x-amz-signature" {
			continue
		}
		pairs = append(pairs, pair{aws4Recode(k, true), aws4Recode(v, true)})
	}
	slices.SortStableFunc(pairs, func(a, b pair) int { return strings.Compare(a.k, b.k) })
	var b strings.Builder
	for i, p := range pairs {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(p.k)
		b.WriteByte('=')
		b.WriteString(p.v)
	}
	return b.String()
}

// canonicalHeadersV4 is get_v4_canonical_headers (rgw_auth_s3.cc:734-834 at
// v19.2.6, :711-811 at v20.2.4). A signed header the request lacks is
// skipped, and each one present is keyed by its token as sent. A non-empty
// hostPort is appended to the host token's value, as the boto2 fallback
// does. Unless insecure (rgw_sigv4_insecure), host, a Content-Type the
// request carries and every x-amz-* header it carries must be signed
// (CVE-2026-54330, :788-820 at v19.2.6, :765-797 at v20.2.4). Every refusal
// is EPERM.
func canonicalHeadersV4(rv *requestView, signedHeaders string, insecure bool, hostPort string) (string, error) {
	m := map[string]string{}
	for tok := range strings.SplitSeq(signedHeaders, ";") {
		if tok == "" {
			continue
		}
		v, ok := rv.header(tok)
		if !ok {
			continue
		}
		// radosgw compares the token's HTTP_ name, which upper-cases it.
		if asciiLower(tok) == "content-md5" && !isBase64Charset(v) {
			return "", fmt.Errorf("%w: content-md5 is not base64", op.ErrAccessDenied)
		}
		if hostPort != "" && tok == "host" {
			v += ":" + hostPort
		}
		m[tok] = trimSpace(v)
	}
	if !insecure {
		if _, ok := m["host"]; !ok {
			return "", fmt.Errorf("%w: host is not signed", op.ErrAccessDenied)
		}
		if _, ok := m["content-type"]; rv.hasHeader("content-type") && !ok {
			return "", fmt.Errorf("%w: content-type is not signed", op.ErrAccessDenied)
		}
		for _, name := range slices.Sorted(maps.Keys(rv.req.Header)) {
			lower := asciiLower(name)
			if _, ok := m[lower]; strings.HasPrefix(lower, "x-amz-") && !ok {
				return "", fmt.Errorf("%w: %s is not signed", op.ErrAccessDenied, lower)
			}
		}
	}
	var b strings.Builder
	for _, k := range slices.Sorted(maps.Keys(m)) {
		b.WriteString(k)
		b.WriteByte(':')
		b.WriteString(collapseSpace(m[k]))
		b.WriteByte('\n')
	}
	return b.String(), nil
}

// canonicalRequestV4 is the text get_v4_canon_req_hash hashes
// (rgw_auth_s3.cc:902-930 at v19.2.6, :879-907 at v20.2.4).
func canonicalRequestV4(method, uri, qs, hdrs, signedHeaders, payloadHash string) string {
	return strings.Join([]string{method, uri, qs, hdrs, signedHeaders, payloadHash}, "\n")
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func hmacSHA256(key, msg []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(msg)
	return h.Sum(nil)
}

// stringToSignV4 is get_v4_string_to_sign (rgw_auth_s3.cc:937-959 at v19.2.6,
// :914-936 at v20.2.4).
func stringToSignV4(date, scope, canonicalRequest string) string {
	return strings.Join([]string{aws4Algorithm, date, scope, sha256Hex(canonicalRequest)}, "\n")
}

// parseCredScope is parse_cred_scope (rgw_auth_s3.cc:962-982 at v19.2.6,
// :939-959 at v20.2.4): date, region and service, whatever region or service
// the client named. A field with no '/' after it leaves the rest whole for
// the next, since substr(npos + 1) is substr(0).
func parseCredScope(scope string) (date, region, service string) {
	next := func(s string) (field, rest string) {
		if f, r, ok := strings.Cut(s, "/"); ok {
			return f, r
		}
		return s, s
	}
	date, rest := next(scope)
	region, rest = next(rest)
	service, _ = next(rest)
	return date, region, service
}

// signingKeyV4 is get_v4_signing_key (rgw_auth_s3.cc:1009-1033 at v19.2.6,
// :986-1010 at v20.2.4). transform_secret_key re-encodes each secret byte as
// a UTF-8 code point, which leaves ASCII as it is and is undefined above 0x7f;
// the secret's own bytes key the HMAC here, as AWS clients key it
// (docs/exclusions.md).
func signingKeyV4(secret, scope string) []byte {
	date, region, service := parseCredScope(scope)
	k := hmacSHA256([]byte("AWS4"+secret), []byte(date))
	k = hmacSHA256(k, []byte(region))
	k = hmacSHA256(k, []byte(service))
	return hmacSHA256(k, []byte(aws4Request))
}

// signatureV4 is get_v4_signature's digest in lower-case hex
// (rgw_auth_s3.cc:1044-1066 at v19.2.6, :1021-1043 at v20.2.4).
func signatureV4(key []byte, stringToSign string) string {
	return hex.EncodeToString(hmacSHA256(key, []byte(stringToSign)))
}

// expectedPayloadHash is get_v4_exp_payload_hash (rgw_auth_s3.h:638-661 at
// v19.2.6, :641-664 at v20.2.4): x-amz-content-sha256, or UNSIGNED-PAYLOAD
// when it is absent, as go-ceph's admin client relies on.
func expectedPayloadHash(rv *requestView) string {
	if h, ok := rv.header("x-amz-content-sha256"); ok {
		return h
	}
	return unsignedPayload
}

// authDataV4 is get_auth_data_v4 (rgw_rest_s3.cc:5771-6000 at v19.2.6,
// :6338-6571 at v20.2.4) but for its per-op payload whitelist, which needs
// the route and is acceptedBy, and for its non-S3 ops (is_non_s3_op: the STS,
// IAM, OIDC and topic ops), whose payload hash radosgw takes from the
// PayloadHash query parameter and never completes (:5807-5818 and :5882,
// :6374-6385 and :6449); rgw-go does not route those ops. The method is the
// request's: radosgw signs an OPTIONS CORS request over its
// Access-Control-Request-Method instead.
func authDataV4(rv *requestView, presigned bool, cfg *Config, now time.Time) (*authData, error) {
	var (
		c   *v4Credentials
		err error
	)
	if presigned {
		c, err = parseV4Query(rv, now)
	} else {
		c, err = parseV4Header(rv, now)
	}
	if err != nil {
		return nil, err
	}
	hdrs, err := canonicalHeadersV4(rv, c.signedHeaders, cfg.Insecure, "")
	if err != nil {
		return nil, err
	}
	payloadHash := expectedPayloadHash(rv)
	pc, err := classifyPayload(rv, payloadHash)
	if err != nil {
		return nil, err
	}
	uri, qs := canonicalURIV4(rv.rawPath), canonicalQueryV4(rv.rawQuery, presigned)
	creq := canonicalRequestV4(rv.req.Method, uri, qs, hdrs, c.signedHeaders, payloadHash)
	sts := stringToSignV4(c.date, c.scope, creq)
	d := &authData{
		accessKey: c.accessKey, signature: c.signature, canonicalRequest: creq, stringToSign: sts,
		v4: c, presigned: presigned, payloadHash: payloadHash, payload: pc,
	}
	d.sign = func(secret string) (string, error) { return signatureV4(signingKeyV4(secret, c.scope), sts), nil }
	// When the first engine of radosgw's S3 strategy denies a request, it
	// tries a boto2 engine whose only difference is that a presigned
	// request's signed host has the listener's port appended
	// (rgw_auth_registry.h:28-49 at v19.2.6 and v20.2.4; rgw_rest_s3.cc:6022-6030
	// at v19.2.6, :6593-6601 at v20.2.4). The local engine denies a signature
	// mismatch (rgw_rest_s3.cc:6374 at v19.2.6, :6945 at v20.2.4). A rejection
	// ends the strategy instead (rgw_auth.cc:367-369 at v19.2.6, :382-384 at
	// v20.2.4), and the STS engine rejects a request carrying a session token
	// whose signature does not match (rgw_rest_s3.cc:6530 at v19.2.6, :7138 at
	// v20.2.4), so such a request never reaches the fallback. verify tries
	// altSign for every caller, so an STS engine must not inherit it. When
	// the boto2 engine is denied too, radosgw reports the first engine's
	// result (rgw_auth.cc:396-397 at v19.2.6, :411-412 at v20.2.4), which a
	// refusal here leaves standing.
	if port := boto2HostPort(rv); presigned && port != "" && slices.Contains(strings.Split(c.signedHeaders, ";"), "host") {
		if altHdrs, err := canonicalHeadersV4(rv, c.signedHeaders, cfg.Insecure, port); err == nil {
			altSts := stringToSignV4(c.date, c.scope, canonicalRequestV4(rv.req.Method, uri, qs, altHdrs, c.signedHeaders, payloadHash))
			d.altSign = func(secret string) (string, error) { return signatureV4(signingKeyV4(secret, c.scope), altSts), nil }
		}
	}
	return d, nil
}
