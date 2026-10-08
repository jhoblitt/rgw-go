package s3

import (
	"context"
	"crypto/md5" //nolint:gosec // Content-MD5 is an MD5
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/authz"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/rgwtext"
	"github.com/jhoblitt/rgw-go/internal/strptime"
	"github.com/jhoblitt/rgw-go/internal/tags"
)

// The object write routes are rgw_rest_s3.cc's RGWPutObj_ObjStore_S3,
// RGWDeleteObj_ObjStore_S3, RGWDeleteMultiObj_ObjStore_S3,
// RGWCopyObj_ObjStore_S3, RGWPutACLs_ObjStore_S3 at object scope,
// RGWPutObjTags_ObjStore_S3 and RGWDeleteObjTags_ObjStore_S3. A bare line
// number is v19.2.6's rgw_rest_s3.cc; [T] marks v20.2.4's.

// putObject is put_obj without a copy source (RGWPutObj_ObjStore_S3,
// get_params :2597-2704, [T] :2758-2865; send_response :2730-2782, [T]
// :2891-2952). A request whose uploadId is not empty is a part, which
// uploadPart serves; RGWPutObj::execute takes an empty one for none
// (rgw_op.cc:4214 at v19.2.6, :4423 at v20.2.4). A PUT naming a copy source
// that is no copy and an append are not served yet. The op reads the
// request's parameters through putObjectParams once the bucket is loaded,
// as init_processing reads them, and makes the checks of stored state
// through putObjectChecks once the requester is authorized, so that a
// refused requester learns nothing the store holds (docs/exclusions.md).
// The success is send_response's: the ETag, the Content-Length
// of 0 with Accept-Ranges that dump_content_length sends (:2747, [T] :2911),
// x-amz-version-id when there is one, Rgwx-Mtime for a system request, and
// the status rgw_s3_success_create_obj_status names.
func (wr *objectWrites) putObject(ctx context.Context, w http.ResponseWriter, r *op.Request) error {
	switch {
	case r.Query.Get("uploadId") != "":
		return wr.uploadPart(ctx, w, r)
	case hasHeader(r, "X-Amz-Copy-Source"):
		return fmt.Errorf("%w: a PUT that names a copy source but is not a copy", op.ErrNotImplemented)
	case r.Query.Has("append"):
		return fmt.Errorf("%w: appendable objects are not served yet", op.ErrNotImplemented)
	}
	o := &op.PutObject{
		Body: r.Body, Size: r.ContentLength,
		StorageClass: header(r, "X-Amz-Storage-Class"), CannedACL: header(r, "X-Amz-Acl"),
		IfMatch: headerPtr(r, "If-Match"), IfNoneMatch: headerPtr(r, "If-None-Match"),
	}
	o.Params = func(_ context.Context, o *op.PutObject) error { return wr.putObjectParams(r, o) }
	o.Authorized = func(ctx context.Context, o *op.PutObject) error { return putObjectChecks(ctx, r, o) }
	if err := op.Run(ctx, o, r); err != nil {
		return err
	}
	SetCommonHeaders(w, r)
	h := w.Header()
	h.Set("ETag", `"`+o.ETag+`"`)
	SetContentLength(h, 0)
	if o.VersionID != "" {
		h.Set("x-amz-version-id", o.VersionID)
	}
	setSystemMtime(h, r, o.Mtime)
	w.WriteHeader(successStatus(r))
	return nil
}

// putObjectParams is the part of get_params that reads the request alone,
// in its order, and then the two checks RGWPutObj::execute makes of the
// request alone: a body without a Content-Length that is not chunked is
// MissingContentLength; a request that asks for encryption is refused
// (refuseEncryption); x-amz-tagging that set_from_string refuses is
// InvalidArgument, as radosgw answers any such refusal on a PUT (:2626-2637,
// [T] :2787-2798); the object-lock headers' values are checked
// (objectLockHeaders); a Content-MD5 that ceph_unarmor does not decode to 16
// bytes is InvalidDigest (rgw_op.cc:4184-4198 at v19.2.6, :4393-4407 at
// v20.2.4); and the attrs are requestAttrs'. radosgw checks the last two
// after authorizing, and the attrs once the body is stored
// (docs/exclusions.md).
func (wr *objectWrites) putObjectParams(r *op.Request, o *op.PutObject) error {
	if r.ContentLength == 0 && r.Header.Get("Content-Length") == "" {
		return op.ErrMissingContentLength
	}
	if err := refuseEncryption(r, true); err != nil {
		return err
	}
	if v, ok := op.HeaderValue(r.Header, "X-Amz-Tagging"); ok {
		set, terr := tags.ParseHeader(v, tags.MaxObjectTags)
		if terr != nil {
			return op.ErrInvalidArgument
		}
		o.Tags = &set
	}
	if err := objectLockHeaders(r, false); err != nil {
		return err
	}
	if v, ok := op.HeaderValue(r.Header, "Content-MD5"); ok {
		sum, ok := cephUnarmor(rgwtext.CString(v))
		if !ok || len(sum) != md5.Size {
			return op.ErrInvalidDigest
		}
		o.ContentMD5 = sum
	}
	var err error
	o.Attrs, err = requestAttrs(r, wr.generic, true)
	return err
}

// putObjectChecks is the rest of get_params in its order, once the requester
// is authorized: the ACL is create_s3_policy's for the requester, whose
// grantees are looked up and whose bucket-owner grants name the bucket's ACL
// owner (:2618-2620, [T] :2779-2781); then a retention or a legal hold on a
// bucket without object lock is refused (objectLock). radosgw makes both
// before verify_permission (docs/exclusions.md).
func putObjectChecks(ctx context.Context, r *op.Request, o *op.PutObject) error {
	p, err := newObjectACL(ctx, r)
	if err != nil {
		return err
	}
	o.ACL = p
	return objectLock(r)
}

// newObjectACL is create_s3_policy for a new object or copy
// (rgw_rest_s3.cc:2408-2422, [T] :2524-2538): the requester's ACL owner, and
// for the bucket-owner canned ACLs the owner of the bucket's ACL,
// s->bucket_owner. Its errors are mapped through authz.ErrorFor.
func newObjectACL(ctx context.Context, r *op.Request) (acl.Policy, error) {
	bucketACL, err := op.BucketACLFor(ctx, r.BucketRec)
	if err != nil {
		return acl.Policy{}, err
	}
	res := authz.UserResolver{Users: r.Env.Users, Accounts: r.Env.Accounts}
	p, err := authz.DefaultACL(ctx, r, res, aclOwner(r.Identity), bucketACL.Owner)
	if err != nil {
		return acl.Policy{}, authz.ErrorFor(err)
	}
	return p, nil
}

// refuseEncryption answers NotImplemented for a write that asks for
// encryption: one carrying a header that init_meta_info files in
// crypt_attribute_map once it has rewritten radosgw's meta prefixes to
// x-amz- (rgw_common.cc:413-464 at v19.2.6, :426-477 at v20.2.4), so
// X-Goog-Server-Side-Encryption-Customer-Key as well as
// X-Amz-Server-Side-Encryption; one naming a copy source's SSE-C key under
// any prefix; and, when query is set, a query parameter map_qs_metadata
// files there for a PutObject (rgw_rest_s3.cc:2591-2593 at v19.2.6,
// :2752-2754 at v20.2.4). radosgw encrypts SSE-C and fails SSE-S3 and
// SSE-KMS without a key server; rgw-go encrypts nothing in phase 1, and
// must not store in the clear what a client asked to have encrypted
// (docs/exclusions.md). A bucket's default encryption is the op's to
// refuse, once the requester is authorized.
func refuseEncryption(r *op.Request, query bool) error {
	for name := range headerMeta(r.Header) {
		if isCryptName(name) {
			return fmt.Errorf("%w: server-side encryption is not served yet", op.ErrNotImplemented)
		}
	}
	if query {
		for k := range r.Query {
			if strings.HasPrefix(lowerASCIIString(k), "x-amz-server-side-encryption") {
				return fmt.Errorf("%w: server-side encryption is not served yet", op.ErrNotImplemented)
			}
		}
	}
	return nil
}

// objectLock is PutObject's get_params refusal of a retention or legal hold
// for a bucket without object lock, InvalidRequest (:2669-2673, [T]
// :2830-2834), which a copy makes too, where radosgw stores them on such a
// bucket (docs/ceph-upstream-bugs.md, "radosgw's CopyObject stores an object
// lock on a bucket without object lock"; docs/exclusions.md). The callers
// make it once the requester is authorized, so that a refused requester
// learns nothing of the bucket's configuration. On a bucket with object lock
// a PutObject or CopyObject answers NotImplemented, and a multipart write
// naming a retention or a legal hold does (multipartObjectLock), until
// versioning is served.
func objectLock(r *op.Request) error {
	if namesObjectLock(r) && r.BucketRec.Info.Flags&meta.BucketObjLockEnabled == 0 {
		return op.ErrInvalidRequest
	}
	return nil
}

// namesObjectLock reports whether the request names a retention mode or a
// legal hold, which get_params keeps as obj_retention or obj_legal_hold.
func namesObjectLock(r *op.Request) bool {
	return hasHeader(r, "X-Amz-Object-Lock-Mode") || hasHeader(r, "X-Amz-Object-Lock-Legal-Hold")
}

// objectLockHeaders is get_params' check of the three object-lock headers
// (:2639-2668, [T] :2800-2829; RGWCopyObj_ObjStore_S3::get_params
// :3480-3509, [T] :3760-3789), which reads nothing the bucket holds: a mode
// with a retain-until date that from_iso_8601 reads as later than now, the
// mode GOVERNANCE or COMPLIANCE; a mode or a date alone; a legal hold of ON
// or OFF; each other value InvalidArgument, with the message a copy sets
// when isCopy is set.
func objectLockHeaders(r *op.Request, isCopy bool) error {
	mode, hasMode := op.HeaderValue(r.Header, "X-Amz-Object-Lock-Mode")
	date, hasDate := op.HeaderValue(r.Header, "X-Amz-Object-Lock-Retain-Until-Date")
	hold, hasHold := op.HeaderValue(r.Header, "X-Amz-Object-Lock-Legal-Hold")
	invalid := func(msg string) error {
		if isCopy {
			return op.ErrInvalidArgument.WithMessage(msg)
		}
		return op.ErrInvalidArgument
	}
	switch {
	case hasMode && hasDate:
		until, ok := fromISO8601(rgwtext.CString(date))
		if !ok || until <= r.Env.Clock().Unix() {
			return invalid("invalid x-amz-object-lock-retain-until-date value")
		}
		if mode = rgwtext.CString(mode); mode != "GOVERNANCE" && mode != "COMPLIANCE" {
			return invalid("invalid x-amz-object-lock-mode value")
		}
	case hasMode || hasDate:
		return invalid("need both x-amz-object-lock-mode and x-amz-object-lock-retain-until-date ")
	}
	if hasHold {
		if hold = rgwtext.CString(hold); hold != "ON" && hold != "OFF" {
			return invalid("invalid x-amz-object-lock-legal-hold value")
		}
	}
	return nil
}

// fromISO8601 is ceph::from_iso_8601 with ws_terminates (src/common/iso_8601.cc
// :51-151 at v19.2.6 and v20.2.4) as RGWObjectRetention's caller then reads
// it, real_clock::to_time_t: the time's whole seconds, through real_time's
// unsigned 64-bit nanoseconds, which wrap for a time after 2554. A date or
// time may end early, or at white space, or after a "Z"; a year before 1970
// does not parse.
func fromISO8601(s string) (int64, bool) {
	p := 0
	digits := func(n int) (int, bool) {
		v := 0
		for range n {
			if p == len(s) || s[p] < '0' || s[p] > '9' {
				return 0, false
			}
			v = v*10 + int(s[p]-'0')
			p++
		}
		return v, true
	}
	space := func(c byte) bool { return c == ' ' || '\t' <= c && c <= '\r' }
	dateEnd := func() bool { return p == len(s) || space(s[p]) }
	timeEnd := func() bool { return p < len(s) && s[p] == 'Z' && (p+1 == len(s) || space(s[p+1])) }
	year, ok := digits(4)
	if !ok || year < 1970 {
		return 0, false
	}
	month, day, hour, minute, sec := 1, 1, 0, 0, 0
	seconds := func(frac uint64) (int64, bool) {
		t := strptime.Tm{Year: year, Mon: month - 1, Mday: day, Hour: hour, Min: minute, Sec: sec}.Timegm().Unix()
		if t == -1 {
			return 0, false
		}
		ns := uint64(t)*uint64(time.Second) + frac   //nolint:gosec // real_time keeps unsigned nanoseconds
		return int64(ns / uint64(time.Second)), true //nolint:gosec // to_time_t of those nanoseconds
	}
	if dateEnd() {
		return seconds(0)
	}
	for _, field := range []struct {
		delim byte
		v     *int
		end   func() bool
	}{{'-', &month, dateEnd}, {'-', &day, dateEnd}, {'T', &hour, timeEnd}, {':', &minute, timeEnd}, {':', &sec, timeEnd}} {
		if p == len(s) || s[p] != field.delim {
			return 0, false
		}
		p++
		if *field.v, ok = digits(2); !ok {
			return 0, false
		}
		if field.end() {
			return seconds(0)
		}
	}
	if p == len(s) || s[p] != '.' {
		return 0, false
	}
	p++
	var frac, scale uint64 = 0, 100_000_000
	for range 9 {
		d, ok := digits(1)
		if !ok {
			return 0, false
		}
		frac += uint64(d) * scale //nolint:gosec // a digit
		scale /= 10
		if timeEnd() {
			return seconds(frac)
		}
	}
	return 0, false
}

// cephUnarmor is ceph_unarmor (src/common/armor.c:107-137 at v19.2.6 and
// v20.2.4) into the 17-byte buffer RGWPutObj::execute passes: groups of four
// base64 digits, "-" and "_" read as "+" and "/" and "=" as a zero digit,
// line feeds between groups skipped; an "=" in a group's third or fourth
// place ends the decode there, whatever follows. A group cut short, a byte
// that is no digit and output past the buffer fail.
func cephUnarmor(src string) ([]byte, bool) {
	const capacity = md5.Size + 1
	dst := make([]byte, 0, capacity)
	put := func(c int) bool {
		if len(dst) == capacity {
			return false
		}
		dst = append(dst, byte(c)) //nolint:gosec // the low byte, as SET_DST stores a char
		return true
	}
	for i := 0; i < len(src); {
		if src[i] == '\n' {
			i++
			continue
		}
		if i+4 > len(src) {
			return nil, false
		}
		a, b, c, d := armorBits(src[i]), armorBits(src[i+1]), armorBits(src[i+2]), armorBits(src[i+3])
		if a < 0 || b < 0 || c < 0 || d < 0 {
			return nil, false
		}
		if !put(a<<2 | b>>4) {
			return nil, false
		}
		if src[i+2] == '=' {
			return dst, true
		}
		if !put((b&15)<<4 | c>>2) {
			return nil, false
		}
		if src[i+3] == '=' {
			return dst, true
		}
		if !put((c&3)<<6 | d) {
			return nil, false
		}
		i += 4
	}
	return dst, true
}

// armorBits is armor.c's decode_bits: a base64 digit's value, -1 for a byte
// that is none.
func armorBits(c byte) int {
	switch {
	case 'A' <= c && c <= 'Z':
		return int(c - 'A')
	case 'a' <= c && c <= 'z':
		return int(c-'a') + 26
	case '0' <= c && c <= '9':
		return int(c-'0') + 52
	case c == '+' || c == '-':
		return 62
	case c == '/' || c == '_':
		return 63
	case c == '=':
		return 0
	}
	return -1
}

// successStatus is get_success_retcode over rgw_s3_success_create_obj_status
// (:2719-2728 and :2736-2740, [T] :2880-2889 and :2897-2901): 201 or 204 when
// the option names it, otherwise 200.
func successStatus(r *op.Request) int {
	if r.Env.Conf != nil {
		if v, err := r.Env.Conf.Int64("rgw_s3_success_create_obj_status"); err == nil && (v == http.StatusCreated || v == http.StatusNoContent) {
			return int(v)
		}
	}
	return http.StatusOK
}

// setSystemMtime is send_response's Rgwx-Mtime for a system request
// (:2779-2781, [T] :2949-2951; dump_epoch_header, rgw_rest.cc:487-496 at
// v19.2.6): the mtime's seconds and nanoseconds.
func setSystemMtime(h http.Header, r *op.Request, mtime time.Time) {
	if r.Identity.System && !mtime.IsZero() {
		h.Set("Rgwx-Mtime", fmt.Sprintf("%d.%09d", mtime.Unix(), mtime.Nanosecond()))
	}
}

// hasHeader reports whether the request carries the header, empty or not.
func hasHeader(r *op.Request, name string) bool {
	_, ok := op.HeaderValue(r.Header, name)
	return ok
}

// headerPtr is a header's last value, nil when it is absent: a header
// present with an empty value is a condition radosgw checks.
func headerPtr(r *op.Request, name string) *string {
	if v, ok := op.HeaderValue(r.Header, name); ok {
		return &v
	}
	return nil
}
