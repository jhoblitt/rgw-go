package op

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/policy"
	"github.com/jhoblitt/rgw-go/internal/rgwtext"
)

// DeleteObject is RGWDeleteObj (rgw_op.cc:5129-5327 at v19.2.6, :5519-5726
// at v20.2.4; rgw_rest_s3.cc:3427-3470 at v19.2.6, :3685-3750 at v20.2.4).
// The handler fills the headers' values, url-decoded where radosgw decodes
// them, and nil for a header that is absent. A header present with an empty
// value is a condition radosgw checks, and fails.
type DeleteObject struct {
	// Versioned is that the request names a version instance, which selects
	// the action. Init sets it from the request.
	Versioned bool
	// UnmodifiedSince is x-amz-delete-if-unmodified-since.
	UnmodifiedSince *string
	// IfMatch, IfMatchSize and IfMatchLastModified are If-Match,
	// x-amz-if-match-size and x-amz-if-match-last-modified-time, which
	// v20.2.4 reads and rgw-go reads on both releases.
	IfMatch             *string
	IfMatchSize         *string
	IfMatchLastModified *string

	// VersionID and DeleteMarker are del_op->result: empty and false for the
	// unversioned deletes rgw-go performs.
	VersionID    string
	DeleteMarker bool

	params DeleteParams
}

var _ Op = (*DeleteObject)(nil)

// Name is RGWDeleteObj::name.
func (o *DeleteObject) Name() string { return "delete_obj" }

// Action is the action RGWDeleteObj::verify_permission authorizes.
func (o *DeleteObject) Action() policy.Action {
	if o.Versioned {
		return policy.S3DeleteObjectVersion
	}
	return policy.S3DeleteObject
}

// OpMask is RGW_OP_TYPE_DELETE.
func (o *DeleteObject) OpMask() uint32 { return OpTypeDelete }

// Init loads the bucket, as init_permissions does, and then parses the
// conditions as RGWDeleteObj_ObjStore_S3::get_params does during
// init_processing, before verify_permission (rgw_rest_s3.cc:3427-3453 at
// v19.2.6, :3685-3733 at v20.2.4): x-amz-delete-if-unmodified-since by
// utime_t::parse_date, x-amz-if-match-last-modified-time by parse_time and
// x-amz-if-match-size by strict_strtoll, each that does not parse, an empty
// one included, InvalidArgument. v19.2.6 reads only the first; rgw-go reads all three on
// both releases (docs/exclusions.md, "Delete conditions are Tentacle's, and
// a delete is guarded, on both releases").
func (o *DeleteObject) Init(ctx context.Context, r *Request) error {
	o.Versioned = r.Object.Instance != ""
	rec, err := r.Env.Buckets.GetBucket(ctx, r.Tenant, r.Bucket)
	if err != nil {
		return err
	}
	r.BucketRec = rec
	o.params = DeleteParams{IfMatch: storeCondition(o.IfMatch)}
	if o.UnmodifiedSince != nil {
		t, ok := parseDate(*o.UnmodifiedSince)
		if !ok {
			return fmt.Errorf("%w: %q is not a date parse_date takes", ErrInvalidArgument, *o.UnmodifiedSince)
		}
		o.params.UnmodifiedSince = t
	}
	if o.IfMatchLastModified != nil {
		t, err := ParseHTTPTime(*o.IfMatchLastModified)
		if err != nil {
			return err
		}
		o.params.IfMatchLastModified = t
	}
	if o.IfMatchSize != nil {
		v, ok := rgwtext.StrictStrtoll(rgwtext.CString(*o.IfMatchSize))
		if !ok {
			return fmt.Errorf("%w: bad size %q", ErrInvalidArgument, *o.IfMatchSize)
		}
		o.params.IfMatchSize = new(uint64(v)) //nolint:gosec // size_match = uint64_t(size_tmp)
	}
	return nil
}

// VerifyPermission is RGWDeleteObj::verify_permission (rgw_op.cc:5138-5166 at
// v19.2.6, :5528-5556 at v20.2.4): the bucket permission, then MFA for a
// delete of a version in a bucket with MFA delete, which radosgw lets through
// only when the request's x-amz-mfa verified. rgw-go verifies none, so it
// refuses every such delete, an admin's included: radosgw returns the
// permission's refusal first, which its admin override then passes without
// the MFA check (docs/exclusions.md). ERR_MFA_REQUIRED is no access denial
// op.Run overrides.
func (o *DeleteObject) VerifyPermission(ctx context.Context, r *Request) error {
	a := o.Action()
	err := VerifyBucketPermission(ctx, r, a, acl.PermFor(a))
	if IsBeforeVerify(err) {
		return err
	}
	if mfaEnabled(r.BucketRec) && r.Object.Instance != "" {
		return ErrMFARequired
	}
	return err
}

// Execute is RGWDeleteObj::execute's unversioned path (rgw_op.cc:5173-5327 at
// v19.2.6, :5563-5726 at v20.2.4): the delete, where a missing key is the
// 204 RGWDeleteObj_ObjStore_S3::send_response turns -ENOENT into
// (rgw_rest_s3.cc:3457-3461 at v19.2.6, :3737-3741 at v20.2.4), and a lost
// race is success (rgw_op.cc:5302-5304 at v19.2.6, :5701-5703 at v20.2.4).
func (o *DeleteObject) Execute(ctx context.Context, r *Request) error {
	if err := versioningUnserved(r.BucketRec, r.Object); err != nil {
		return err
	}
	if r.Object.Name == "" {
		return ErrInvalidArgument
	}
	err := r.Env.Objects.DeleteObject(ctx, r.BucketRec, r.Object, o.params)
	if errors.Is(err, ErrNoSuchKey) || errors.Is(err, ErrConcurrentModification) {
		return nil
	}
	return err
}

// Complete does nothing: the handler logs usage once the response is written.
func (o *DeleteObject) Complete(context.Context, *Request) {}

// mfaEnabled is RGWBucketInfo::mfa_enabled (rgw_common.h:1077 at v19.2.6,
// :1119 at v20.2.4).
func mfaEnabled(rec *BucketRecord) bool { return rec.Info.Flags&meta.BucketMFAEnabled != 0 }

// parseDate is utime_t::parse_date (include/utime.h:397-502 at v19.2.6 and
// v20.2.4) turned into the utime_t its caller builds: a date strptime takes
// as "%Y-%m-%d", alone, with anything but a space or "T" after it ignored,
// or followed by a space or "T" and a time; otherwise "%d.%d", seconds and
// microseconds since the epoch, as sscanf reads them.
func parseDate(date string) (time.Time, bool) {
	date = rgwtext.CString(date)
	tm, rest, ok := strptime(date, "%Y-%m-%d")
	if !ok {
		sec, usec, scanned := scanSecUsec(date)
		if !scanned {
			return time.Time{}, false
		}
		// gmtime_r and internal_timegm give sec back, with no offset.
		return utimeTime(uint64(int64(sec)), uint64(int64(usec))*1000), true //nolint:gosec // the casts radosgw makes
	}
	var (
		gmtoff int64
		nsec   uint64
	)
	if rest != "" && (rest[0] == ' ' || rest[0] == 'T') {
		var clock cTM
		if clock, gmtoff, nsec, ok = parseDateClock(rest[1:]); !ok {
			return time.Time{}, false
		}
		tm.hour, tm.minute, tm.sec = clock.hour, clock.minute, clock.sec
	}
	return utimeTime(uint64(internalTimegm(tm))-uint64(gmtoff), nsec), true //nolint:gosec // epoch -= gmtoff in uint64_t
}

// parseDateClock is parse_date's time: it copies the first 31 bytes of p
// into a format, overwrites the format's first eight bytes with "%H:%M" and
// "%S" around p's sixth, so that the rest of p, after the seconds, must
// match itself literally; skips a "." and digits after the seconds, which it
// reads as up to nine digits of nanoseconds; and turns a sign that follows
// into "%z". A sign at the format's 31st byte makes radosgw write the
// terminating NUL one byte past the format's end
// (docs/ceph-upstream-bugs.md); the format here has room for it. rgw-go
// refuses a "%" of p's that reaches the format, which radosgw's strptime
// would take for a conversion (docs/exclusions.md).
func parseDateClock(p string) (clock cTM, gmtoff int64, nsec uint64, ok bool) {
	var f [33]byte
	copy(f[:31], p)
	copy(f[:5], "%H:%M")
	f[6], f[7] = '%', 'S'
	subsec, q := -1, 8
	if f[q] == '.' {
		subsec, q = 9, 9
		for f[q] != 0 && isCDigit(f[q]) {
			q++
		}
	}
	zone := f[q] == '-' || f[q] == '+'
	if zone {
		f[q], f[q+1], f[q+2] = '%', 'z', 0
	}
	format := string(f[:strings.IndexByte(string(f[:]), 0)])
	for i := range len(format) {
		if format[i] == '%' && i != 0 && i != 3 && i != 6 && (!zone || i != q) {
			return cTM{}, 0, 0, false
		}
	}
	var rest string
	if zone {
		// strptime emulates %z as the last conversion only, so the zone is
		// read from what the format before it leaves.
		if clock, rest, ok = strptime(p, format[:q]); !ok {
			return cTM{}, 0, 0, false
		}
		if _, gmtoff, ok = zoneOffset(rest); !ok {
			return cTM{}, 0, 0, false
		}
	} else if clock, _, ok = strptime(p, format); !ok {
		return cTM{}, 0, 0, false
	}
	if subsec >= 0 {
		digits := []byte("000000000")
		for i := 0; i < 9 && subsec+i < len(p) && isCDigit(p[subsec+i]); i++ {
			digits[i] = p[subsec+i]
		}
		for _, d := range digits {
			nsec = nsec*10 + uint64(d-'0')
		}
	}
	return clock, gmtoff, nsec, true
}

// scanSecUsec is sscanf(date, "%d.%d", &sec, &usec) == 2.
func scanSecUsec(s string) (sec, usec int32, ok bool) {
	sec, rest, ok := rgwtext.ScanInt(s)
	if !ok {
		return 0, 0, false
	}
	if rest, ok = strings.CutPrefix(rest, "."); !ok {
		return 0, 0, false
	}
	if usec, _, ok = rgwtext.ScanInt(rest); !ok {
		return 0, 0, false
	}
	return sec, usec, true
}

// utimeTime is utime_t(epoch, nsec).to_real_time(): the epoch cut to 32-bit
// seconds and the nanoseconds to 32 bits, as utime_t(time_t, int) stores
// them, then normalized.
func utimeTime(epoch, nsec uint64) time.Time {
	sec, ns := utime(int64(epoch), uint32(nsec)) //nolint:gosec // utime_t keeps 32 bits of each
	return time.Unix(int64(sec), int64(ns)).UTC()
}
