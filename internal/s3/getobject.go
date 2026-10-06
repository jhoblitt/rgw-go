package s3

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math"
	"net/http"
	"net/textproto"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jhoblitt/rgw-go/internal/cephconf"
	"github.com/jhoblitt/rgw-go/internal/compression"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/rgwtext"
	"github.com/jhoblitt/rgw-go/internal/tags"
)

// The object read routes are rgw_rest_s3.cc's RGWGetObj_ObjStore_S3,
// RGWGetObjTags_ObjStore_S3, RGWGetACLs_ObjStore_S3 and, at v20.2.4 only,
// RGWGetObjAttrs_ObjStore_S3. A bare line number is v19.2.6's
// rgw_rest_s3.cc; [T] marks v20.2.4's.

// responseAttrParams is resp_attr_params (:128-141; [T] :139-152): each
// response-* query parameter and the header it overrides.
var responseAttrParams = [...]struct{ param, header string }{
	{"response-content-type", "Content-Type"},
	{"response-content-language", "Content-Language"},
	{"response-expires", "Expires"},
	{"response-cache-control", "Cache-Control"},
	{"response-content-disposition", "Content-Disposition"},
	{"response-content-encoding", "Content-Encoding"},
}

// attrHeaders is base_rgw_to_http_attrs (rgw_rest.cc:98-112 at v19.2.6 and
// v20.2.4): the attrs a GET or HEAD renders as headers of their own.
var attrHeaders = map[string]string{
	meta.AttrContentLang:     "Content-Language",
	meta.AttrExpires:         "Expires",
	meta.AttrCacheControl:    "Cache-Control",
	meta.AttrContentDisp:     "Content-Disposition",
	meta.AttrContentEnc:      "Content-Encoding",
	meta.AttrUserManifest:    "X-Object-Manifest",
	meta.AttrXRobotsTag:      "X-Robots-Tag",
	meta.AttrStorageClass:    "X-Amz-Storage-Class",
	meta.AttrWebsiteRedirect: "x-amz-website-redirect-location",
}

// attrSLOIndicator is RGW_ATTR_SLO_UINDICATOR (rgw_common.h:103 at v19.2.6),
// which sits among the user metadata.
const attrSLOIndicator = meta.AttrMetaPrefix + "static-large-object"

// objectReads serves the object read routes with what radosgw reads once at
// startup: rgw_to_http_attrs, which rgw_rest_init builds from
// rgw_extended_http_attrs (rgw_rest.cc:185-209 at v19.2.6).
type objectReads struct {
	attrHeaders map[string]string
}

func newObjectReads(env *op.Env) *objectReads {
	return &objectReads{attrHeaders: attrHeadersFor(env.Conf)}
}

// getObject serves get_obj for GET and HEAD (RGWGetObj_ObjStore_S3). The op
// renders the headers through getObjectSink once every refusal is decided. A
// failure before then is answered with radosgw's error document, or a 304's
// four headers; one after the header went out ends the connection, so the
// short body never passes for a whole one.
func (g *objectReads) getObject(ctx context.Context, w http.ResponseWriter, r *op.Request) error {
	o, paramErr := getObjectInput(r)
	sink := &getObjectSink{rw: w, w: sinkOf(w), r: r, o: o, attrHeaders: g.attrHeaders}
	o.Sink = sink
	err := op.Run(ctx, deferredParams{Op: o, err: paramErr}, r)
	for _, attr := range sink.undecodable {
		slog.WarnContext(ctx, "omitting x-amz-restore: a restore attr does not decode", slog.String("request_id", r.ID),
			slog.String("bucket", r.Bucket), slog.String("key", r.Object.Name), slog.String("attr", attr))
	}
	switch {
	case err == nil:
		return nil
	case sink.wrote:
		slog.WarnContext(ctx, "object read failed after the response started", slog.String("request_id", r.ID),
			slog.String("bucket", r.Bucket), slog.String("key", r.Object.Name), slog.Any("error", err))
		// What was written goes out first, as radosgw's has, and the abort
		// then ends the connection where the body stopped.
		_ = http.NewResponseController(w).Flush() //nolint:errcheck // the connection is ended either way
		panic(http.ErrAbortHandler)
	case errors.Is(err, op.ErrNotModified):
		setNotModifiedHeaders(w.Header(), o.State)
		return err
	}
	return decodeFailure(err)
}

// getObjectInput is RGWGetObj_ObjStore_S3::get_params (:291-322; [T]
// :296-333) and RGWGetObj_ObjStore::get_params (rgw_rest.cc:836-852 at
// v19.2.6) over r, with the response-* parameters send_response_data reads
// and the SSE inputs rgw_s3_prepare_decrypt reads. Each header is read by its
// last value, as RGWEnv keeps it. A partNumber strict_strtol refuses is
// returned with the op, for the op to answer where radosgw's execute does.
func getObjectInput(r *op.Request) (*op.GetObject, error) {
	_, sse := op.HeaderValue(r.Header, "x-amz-server-side-encryption")
	o := &op.GetObject{
		GetData:           r.Method != http.MethodHead,
		Versioned:         r.Object.Instance != "",
		Range:             header(r, "Range"),
		IfMatch:           header(r, "If-Match"),
		IfNoneMatch:       header(r, "If-None-Match"),
		IfModifiedSince:   header(r, "If-Modified-Since"),
		IfUnmodifiedSince: header(r, "If-Unmodified-Since"),
		Torrent:           r.Query.Has("torrent"),
		SSEHeader:         sse,
		SSECAlgorithm:     header(r, "x-amz-server-side-encryption-customer-algorithm"),
		SSECKey:           header(r, "x-amz-server-side-encryption-customer-key"),
		SSECKeyMD5:        header(r, "x-amz-server-side-encryption-customer-key-MD5"),
		Secure:            secure(r),
	}
	for _, p := range responseAttrParams {
		if v, ok := arg(r.Query, p.param); ok {
			if o.ResponseOverrides == nil {
				o.ResponseOverrides = map[string]string{}
			}
			o.ResponseOverrides[p.header] = v
		}
	}
	if v, ok := arg(r.Query, "partNumber"); ok {
		n, err := parseInt(rgwtext.CString(v))
		if err != nil {
			return o, op.ErrInvalidPart.WithMessage("Invalid partNumber: " + err.Error())
		}
		o.PartNumber = &n
	}
	return o, nil
}

// header is a request header by its last value, "" when absent.
func header(r *op.Request, name string) string {
	v, _ := op.HeaderValue(r.Header, name)
	return v
}

// secure is rgw_transport_is_secure (rgw_common.cc:1071-1093 at v19.2.6,
// :1084-1106 at v20.2.4): a TLS connection or, under
// rgw_trust_forwarded_https, a Forwarded header naming proto=https or an
// X-Forwarded-Proto of exactly https.
func secure(r *op.Request) bool {
	if r.TLS {
		return true
	}
	if !confBool(r, "rgw_trust_forwarded_https") {
		return false
	}
	if v, ok := op.HeaderValue(r.Header, "Forwarded"); ok && strings.Contains(v, "proto=https") {
		return true
	}
	v, ok := op.HeaderValue(r.Header, "X-Forwarded-Proto")
	return ok && v == "https"
}

// deferredParams runs an op whose own parameters failed to parse. radosgw
// parses them in get_params, which RGWGetObj::execute calls first
// (rgw_op.cc:2239 at v19.2.6, :2475 at v20.2.4), so the bucket and object
// loads and the permission check refuse a request before its parameters do.
type deferredParams struct {
	op.Op
	err error
}

func (d deferredParams) Execute(ctx context.Context, r *op.Request) error {
	if d.err != nil {
		return d.err
	}
	return d.Op.Execute(ctx, r)
}

// decodeFailure answers a compressed block its codec refuses before any byte
// went out as radosgw's send_response_data does: the decompress return taken
// for an errno, -1 (EPERM) AccessDenied and -2 (ENOENT) NoSuchKey
// (rgw_common.cc:89 and :97 at v19.2.6).
func decodeFailure(err error) error {
	de, ok := errors.AsType[*compression.DecodeError](err)
	if !ok {
		return err
	}
	switch de.Ret {
	case -1:
		return fmt.Errorf("%w: %w", op.ErrAccessDenied, err)
	case -2:
		return fmt.Errorf("%w: %w", op.ErrNoSuchKey, err)
	}
	return fmt.Errorf("%w: %w", op.ErrUnknown, err)
}

// setNotModifiedHeaders is send_response_data's 304 branch (:623-638; [T]
// :740-755): Last-Modified, the ETag, and the stored Cache-Control and
// Expires, with no body and no type.
func setNotModifiedHeaders(h http.Header, st *op.ObjectState) {
	if st == nil {
		return
	}
	h.Set("Last-Modified", httpDate(st.Mtime))
	setETag(h, st.Attrs)
	if v, ok := st.Attrs[meta.AttrCacheControl]; ok {
		h.Set("Cache-Control", sanitizedHdrval(v))
	}
	if v, ok := st.Attrs[meta.AttrExpires]; ok {
		h.Set("Expires", sanitizedHdrval(v))
	}
}

// getObjectSink is the op.Sink getObject hands the op: it renders radosgw's
// response headers from the op's results on the first Write or WriteHeader
// (send_response_data's sent_header path), then streams the bytes.
type getObjectSink struct {
	rw          http.ResponseWriter
	w           op.Sink
	r           *op.Request
	o           *op.GetObject
	attrHeaders map[string]string
	wrote       bool
	// undecodable names the restore attrs the headers left out.
	undecodable []string
}

// endHeaderNames are the headers end_header sends after
// send_response_data's own (rgw_rest.cc:589-642 at v19.2.6), which the
// handler has already set on the writer.
var endHeaderNames = [...]string{"X-Amz-Request-Id", "X-Amz-Request-Charged", "Server"}

// WriteHeader is the op's headers-only path: a HEAD or nothing to read. A
// name an object's attr shares with end_header's headers carries the attr's
// value first, as radosgw sends both.
func (s *getObjectSink) WriteHeader(status int, _ http.Header) {
	if s.wrote {
		return
	}
	s.wrote = true
	SetCommonHeaders(s.rw, s.r)
	h := s.headers()
	for _, name := range endHeaderNames {
		if v := s.rw.Header()[name]; len(v) > 0 && len(h[name]) > 0 {
			h[name] = append(h[name], v...)
		}
	}
	s.w.WriteHeader(status, h)
}

func (s *getObjectSink) Write(p []byte) (int, error) {
	s.WriteHeader(s.o.Status, nil)
	return s.w.Write(p)
}

func (s *getObjectSink) Flush() error { return s.w.Flush() }

// headers renders send_response_data's success headers (:406-644; [T]
// :417-760) for the op's results in the order radosgw sends them: the range,
// the length, the dates and version, the object type, replication, the parts
// count, the ETag, Tentacle's restore state, the user metadata and tagging
// count, then response_attrs, the response-* overrides and the mapped attrs
// in its std::map's order, and last end_header's content type. A name sent
// twice keeps both values in that order. A mapped attr never sets the
// response's framing headers (docs/exclusions.md).
func (s *getObjectSink) headers() http.Header {
	o, r, st := s.o, s.r, s.o.State
	h := http.Header{}
	if o.Range != "" {
		h.Add("Content-Range", contentRange(o))
	}
	SetContentLength(h, o.Length)
	h.Add("Last-Modified", httpDate(st.Mtime))
	if o.VersionID != "" {
		h.Add("x-amz-version-id", o.VersionID)
	}
	if _, ok := st.Attrs[meta.AttrAppendPartNum]; ok {
		h.Add("x-rgw-object-type", "Appendable")
		h.Add("x-rgw-next-append-position", strconv.FormatUint(o.ObjSize, 10))
	} else {
		h.Add("x-rgw-object-type", "Normal")
	}
	setReplicationHeaders(h, st.Attrs)
	if o.PartsCount != nil {
		h.Add("x-amz-mp-parts-count", strconv.Itoa(*o.PartsCount))
	}
	setETag(h, st.Attrs)
	attrs := st.Attrs
	if r.Env.Zone.Release() >= denc.Tentacle {
		attrs = s.setRestoreHeader(h, attrs)
	}
	contentType, typed := o.ResponseOverrides["Content-Type"]
	// responseAttrs is send_response_data's response_attrs: the first value
	// set under a name stays, an override's ahead of any attr's.
	responseAttrs := map[string]string{}
	for name, v := range o.ResponseOverrides {
		if name != "Content-Type" {
			responseAttrs[name] = v
		}
	}
	for _, name := range slices.Sorted(maps.Keys(attrs)) {
		v := attrs[name]
		switch hdr := s.attrHeaders[name]; {
		case hdr != "":
			if _, set := responseAttrs[hdr]; set {
				continue
			}
			val := string(bytes.TrimRight(v, "\x00"))
			if name == meta.AttrContentEnc {
				val = withoutAWSChunked(val)
				if val == "" {
					continue
				}
			}
			responseAttrs[hdr] = val
		case name == meta.AttrContentType:
			if !typed {
				contentType, typed = string(bytes.TrimRight(v, "\x00")), true
			}
		case name == attrSLOIndicator:
			h.Add("X-Object-Meta-Static-Large-Object", "True")
		case strings.HasPrefix(name, meta.AttrMetaPrefix):
			h.Add(strings.TrimPrefix(name, meta.AttrPrefix), sanitizedHdrval(v))
		case name == tags.Attr:
			h.Add("x-amz-tagging-count", strconv.Itoa(taggingCount(v)))
		}
	}
	for _, name := range slices.Sorted(maps.Keys(responseAttrs)) {
		if !framingHeaders[textproto.CanonicalMIMEHeaderKey(name)] {
			h.Add(name, responseAttrs[name])
		}
	}
	if !typed {
		contentType = "binary/octet-stream"
	}
	h.Add("Content-Type", contentType)
	return h
}

// framingHeaders are the framing and hop-by-hop headers, which net/http
// reads from the handler's headers to frame the response, a Trailer
// declaring trailers among them; a mapped attr may not set them.
var framingHeaders = map[string]bool{
	"Content-Length": true, "Transfer-Encoding": true, "Connection": true, "Trailer": true,
	"Keep-Alive": true, "Upgrade": true, "Te": true, "Proxy-Connection": true,
}

// setRestoreHeader is Tentacle's x-amz-restore ([T] :588-628): a restore in
// progress, or a temporary copy's expiry, for which the object reports the
// cloud tier's storage class. It returns attrs with that class in place.
// radosgw decodes the three attrs with nothing to catch a failure, which ends
// the gateway (docs/ceph-upstream-bugs.md); rgw-go omits the header instead.
func (s *getObjectSink) setRestoreHeader(h http.Header, attrs map[string][]byte) map[string][]byte {
	status, ok := attrs[meta.AttrRestoreStatus]
	if !ok {
		return attrs
	}
	if len(status) == 0 {
		s.restoreUndecodable(meta.AttrRestoreStatus)
		return attrs
	}
	if meta.RestoreStatus(status[0]) == meta.RestoreAlreadyInProgress {
		h.Add("x-amz-restore", `ongoing-request="true"`)
		return attrs
	}
	typ, ok := attrs[meta.AttrRestoreType]
	if !ok {
		return attrs
	}
	if len(typ) == 0 {
		s.restoreUndecodable(meta.AttrRestoreType)
		return attrs
	}
	if typ[0] != restoreTemporary {
		return attrs
	}
	var expiry time.Time
	if b, found := attrs[meta.AttrRestoreExpiryDate]; found {
		d := denc.NewDecoder(b)
		expiry = d.Time()
		if d.Err() != nil {
			s.restoreUndecodable(meta.AttrRestoreExpiryDate)
			return attrs
		}
	}
	h.Add("x-amz-restore", `ongoing-request="false", expiry-date="`+httpDate(expiry)+`"`)
	class, ok := attrs[meta.AttrCloudTierStorageClass]
	if !ok {
		return attrs
	}
	attrs = maps.Clone(attrs)
	attrs[meta.AttrStorageClass] = class
	return attrs
}

// restoreTemporary is rgw::sal::RGWRestoreType::Temporary ([T] rgw_sal.h:178-182).
const restoreTemporary = 1

func (s *getObjectSink) restoreUndecodable(attr string) {
	s.undecodable = append(s.undecodable, attr)
}

// contentRange is dump_range (rgw_rest.cc:757-778 at v19.2.6): the bytes
// served of the size the range resolved against.
func contentRange(o *op.GetObject) string {
	if o.ObjSize == 0 {
		return "bytes */0"
	}
	return fmt.Sprintf("bytes %d-%d/%d", o.Offset, o.Offset+o.Length-1, o.ObjSize)
}

// httpDate is dump_time_header_impl (rgw_rest.cc:445-459 at v19.2.6): whole
// seconds under gmtime. The zero time is radosgw's unset real_time, the epoch.
func httpDate(t time.Time) string {
	if t.IsZero() {
		t = time.Unix(0, 0)
	}
	return t.UTC().Format(http.TimeFormat)
}

// setETag is dump_etag over the ETag attr (rgw_rest.cc:413-426 at v19.2.6):
// quoted, up to its first NUL as dump_header_quoted's snprintf copies it, and
// absent for an empty attr.
func setETag(h http.Header, attrs map[string][]byte) {
	v, ok := attrs[meta.AttrETag]
	if !ok || len(v) == 0 {
		return
	}
	h.Add("ETag", `"`+rgwtext.CString(string(v))+`"`)
}

// sanitizedHdrval is rgw_sanitized_hdrval (rgw_rest.h:29-47 at v19.2.6), what
// dump_header sends for an attr: its bytes less one trailing NUL.
func sanitizedHdrval(v []byte) string {
	return string(bytes.TrimSuffix(v, []byte{0}))
}

// withoutAWSChunked is content_encoding_without_aws_chunked (:361-378; [T]
// :372-389): the parts other than aws-chunked, joined with ", ". ceph::split
// takes ", " as a set of delimiters and skips empty parts
// (common/split.h:32-38 at v19.2.6 and v20.2.4).
func withoutAWSChunked(v string) string {
	var kept []string
	for _, part := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' }) {
		if part != "aws-chunked" {
			kept = append(kept, part)
		}
	}
	return strings.Join(kept, ", ")
}

// setReplicationHeaders is send_response_data's replication block (:470-493;
// [T] :505-528): the status as stored, each zone of a trace that decodes, and
// the replication time when it decodes.
func setReplicationHeaders(h http.Header, attrs map[string][]byte) {
	if v, ok := attrs[meta.AttrReplicationStatus]; ok {
		h.Add("x-amz-replication-status", sanitizedHdrval(v))
	}
	if v, ok := attrs[meta.AttrReplicationTrace]; ok {
		// rgw_zone_set_entry encodes as the string to_str gives, which
		// from_str and to_str carry through unchanged (cls_rgw_types.cc:14-47).
		d := denc.NewDecoder(v)
		zones := denc.DecodeSlice(d, func(d *denc.Decoder) string { return d.String() })
		if d.Err() == nil {
			for _, z := range zones {
				h.Add("x-rgw-replicated-from", z)
			}
		}
	}
	if v, ok := attrs[meta.AttrReplicatedAt]; ok {
		d := denc.NewDecoder(v)
		t := d.Time()
		if d.Err() == nil {
			h.Add("x-rgw-replicated-at", httpDate(t))
		}
	}
}

// taggingCount is the x-amz-tagging-count send_response_data writes (:586-594;
// [T] :703-711): RGWObjTags::count after decode, whose failure it ignores.
// When the binary form fails, decode reads the attr as the text older objects
// store and keeps every tag it added before the text failed (rgw_tag.h:32-62,
// rgw_tag.cc:36-57).
func taggingCount(b []byte) int {
	d := denc.NewDecoder(b)
	if set := tags.Decode(d); d.Err() == nil {
		return set.Len()
	}
	text := string(bytes.TrimRight(b, "\x00"))
	if text == "" {
		return 0
	}
	// set_from_string stops at the first item check_and_add_tag refuses, and
	// it refuses every item past MaxObjectTags tags.
	items := strings.Split(text, "&")
	n := 0
	for k := 1; k <= min(len(items), tags.MaxObjectTags+1); k++ {
		set, err := tags.ParseHeader(strings.Join(items[:k], "&"), tags.MaxObjectTags)
		if err != nil {
			break
		}
		n = set.Len()
	}
	return n
}

// attrHeadersFor is rgw_to_http_attrs as rgw_rest_init builds it
// (rgw_rest.cc:185-209 at v19.2.6): attrHeaders, plus each name
// rgw_extended_http_attrs lists, its attr lowercased with dashes as
// underscores and its header camel-cased with underscores as dashes.
func attrHeadersFor(conf *cephconf.Options) map[string]string {
	if conf == nil {
		return attrHeaders
	}
	names, err := conf.List("rgw_extended_http_attrs")
	if err != nil || len(names) == 0 {
		return attrHeaders
	}
	m := maps.Clone(attrHeaders)
	for _, n := range names {
		m[meta.AttrPrefix+lowercaseUnderscore(n)] = camelcaseDash(n)
	}
	return m
}

// lowercaseUnderscore is lowercase_underscore_http_attr (rgw_rest.cc:141-157
// at v19.2.6).
func lowercaseUnderscore(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c == '-' {
			b[i] = '_'
		} else {
			b[i] = lowerASCII(c)
		}
	}
	return string(b)
}

// camelcaseDash is camelcase_dash_http_attr with convert2dash
// (rgw_common.cc:2226-2251 at v19.2.6): each separator a dash, and each word
// capitalized.
func camelcaseDash(s string) string {
	b := []byte(s)
	sep := true
	for i, c := range b {
		switch {
		case c == '_' || c == '-':
			b[i], sep = '-', true
		case sep:
			b[i], sep = upperASCII(c), false
		default:
			b[i] = lowerASCII(c)
		}
	}
	return string(b)
}

func upperASCII(c byte) byte {
	if 'a' <= c && c <= 'z' {
		return c - 'a' + 'A'
	}
	return c
}

// parseInt is strict_strtol (src/common/strtol.cc:42-73 at v19.2.6 and
// v20.2.4) in base 10 with radosgw's messages: strict_strtoll's checks, a
// conversion that stops short of the end before an ERANGE, then the int
// range. rgwtext.StrictStrtoll makes the same checks but does not say which
// failed, which the messages need.
func parseInt(s string) (int, error) {
	v, end, erange := rgwtext.Strtoll(s)
	switch {
	case end == 0 || end != len(s):
		return 0, fmt.Errorf("Expected option value to be integer, got '%s'", s) //nolint:staticcheck // radosgw's message
	case erange || v < math.MinInt32 || v > math.MaxInt32:
		return 0, fmt.Errorf("The option value '%s' seems to be invalid", s) //nolint:staticcheck // radosgw's message
	}
	return int(v), nil
}
