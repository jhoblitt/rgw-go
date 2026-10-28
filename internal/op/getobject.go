package op

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // SSE-C names its key by the key's MD5
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/cksum"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

// GetObject is RGWGetObj for S3 (rgw_op.cc RGWGetObj::* and rgw_rest_s3.cc
// RGWGetObj_ObjStore_S3, at v19.2.6 and v20.2.4): GET and HEAD of an object.
// The handler fills the inputs from the request, runs the op, and renders the
// results; the op reads the object and writes its body to Sink.
type GetObject struct {
	// GetData is false for HEAD, which radosgw serves as RGWGetObj without
	// get_data.
	GetData bool
	// Versioned is that the request names a version instance, which selects
	// the action. Init sets it from the request.
	Versioned bool
	// Range, IfMatch, IfNoneMatch, IfModifiedSince and IfUnmodifiedSince are
	// the headers' values, "" when absent.
	Range             string
	IfMatch           string
	IfNoneMatch       string
	IfModifiedSince   string
	IfUnmodifiedSince string
	// PartNumber is ?partNumber as the handler parsed it, nil without one.
	PartNumber *int
	// Torrent is ?torrent.
	Torrent bool
	// ResponseOverrides maps each response header a response-* parameter
	// overrides to the parameter's value.
	ResponseOverrides map[string]string
	// SSEHeader is that the request carries x-amz-server-side-encryption.
	SSEHeader bool
	// SSECAlgorithm, SSECKey and SSECKeyMD5 are the
	// x-amz-server-side-encryption-customer-algorithm, -key and -key-MD5
	// headers, "" when absent.
	SSECAlgorithm, SSECKey, SSECKeyMD5 string
	// Secure is rgw_transport_is_secure: TLS, or a forwarded protocol radosgw
	// trusts.
	Secure bool
	// ChecksumMode is x-amz-checksum-mode, "" when absent.
	ChecksumMode string
	// Retarget is the website endpoint's retarget, which Init runs between
	// its bucket load and its object read (website_get.go). It is nil for
	// S3, and for the error document's read, which serve_errordoc makes
	// without one
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L5477-L5489 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L6003-L6015 at v20.2.4).
	Retarget *WebsiteRetarget
	// Sink receives the status, through the handler's wrapper, and the body.
	Sink Sink

	// State is the state served: the object's, or with PartNumber the part
	// head's, carrying the object's crypt attrs where the part has none.
	State *ObjectState
	// CondState is the object's own state: the multipart head when
	// PartNumber redirected, else State. If-Match and If-None-Match compare
	// its ETag.
	CondState *ObjectState
	// Offset and Length are the bytes served; Length is the Content-Length.
	Offset, Length uint64
	// Partial is that a range was asked for: 206 with Content-Range.
	Partial bool
	// ObjSize is the size the range resolved against: the uncompressed size
	// of a compressed object.
	ObjSize uint64
	// PartsCount is x-amz-mp-parts-count, nil when radosgw sends none.
	PartsCount *int
	// VersionID is x-amz-version-id: the id of the version read
	// (readVersionID), "" for none.
	VersionID string
	// Status is 200, or 206 for a range.
	Status int
	// Checksum is the stored checksum the response reports, nil for none
	// (readChecksum); set only once every check has passed, as radosgw
	// renders it only on success.
	Checksum *ReadChecksum
}

// ReadChecksum is a stored checksum as a read reports it: a GET's or HEAD's
// header and x-amz-checksum-type, or GetObjectAttributes' Checksum element
// and its ChecksumType.
type ReadChecksum struct {
	// Type names the header and the element (cksum.Type.Info).
	Type cksum.Type
	// Value is the armored digest, followed by "-<parts>" for a COMPOSITE
	// checksum of a whole multipart object.
	Value string
	// ChecksumType is cksum.FullObjectType or cksum.CompositeType.
	ChecksumType string
}

var _ Op = (*GetObject)(nil)

// Name is RGWGetObj::name, for GET and HEAD alike.
func (o *GetObject) Name() string { return "get_obj" }

// Action is the action RGWGetObj::verify_permission authorizes
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L989-L1001 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L1195-L1199 at v20.2.4).
func (o *GetObject) Action() policy.Action {
	switch {
	case o.Torrent && o.Versioned:
		return policy.S3GetObjectVersionTorrent
	case o.Torrent:
		return policy.S3GetObjectTorrent
	case o.Versioned:
		return policy.S3GetObjectVersion
	default:
		return policy.S3GetObject
	}
}

// OpMask is RGW_OP_TYPE_READ.
func (o *GetObject) OpMask() uint32 { return OpTypeRead }

// Init loads the bucket and the object's head, as rgw_build_bucket_policies
// and rgw_build_object_policies do before verify_permission
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L494 and
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L631-L649 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L524 and
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L661-L679 at v20.2.4).
// A missing bucket is NoSuchBucket before any object is read. With Retarget
// set on a website request, the website endpoint's retarget runs between the
// two loads. The head is read with the prefetch prefetch_data asks for, which
// Read::prepare later finds in its cache.
//
// The version id is that of the key the read resolved (readVersionID).
func (o *GetObject) Init(ctx context.Context, r *Request) error {
	o.Versioned = r.Object.Instance != ""
	if err := o.initBucket(ctx, r); err != nil {
		return err
	}
	st, err := o.readHead(ctx, r.Env.Objects, r.BucketRec, r.Object)
	if err != nil {
		return err
	}
	r.ObjState, o.State, o.CondState = st, st, st
	if st != nil {
		o.VersionID = readVersionID(r.BucketRec, st.Key)
	}
	return nil
}

// readVersionID is the version id a read of key in rec names, key being the
// key the read resolved: the version current on a key named without one, as
// RGWGetObj takes version_id from the object Read::prepare followed to
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L2259-L2260 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L2495-L2496 at v20.2.4).
// It is the key's instance, null when the request named the null version,
// and for an empty instance ResponseVersionID's: null on a bucket whose
// versioning was ever enabled, where radosgw names none
// (docs/exclusions.md, "GET, HEAD and GetObjectAttributes name the version
// they read, null for the null version").
func readVersionID(rec *BucketRecord, key meta.ObjKey) string {
	if key.Instance != "" {
		return key.Instance
	}
	return ResponseVersionID(rec, "")
}

// VerifyPermission is RGWGetObj::verify_permission. On Tentacle a refusal of
// an object that exists says which permission is missing
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L1201-L1209 at v20.2.4);
// radosgw refuses a missing object earlier, in read_permissions, without one.
// A delete marker's own version is refused as such, with its Last-Modified
// (deleteMarkerVersion).
func (o *GetObject) VerifyPermission(ctx context.Context, r *Request) error {
	a := o.Action()
	err := deleteMarkerVersion(ctx, r, VerifyObjectPermission(ctx, r, a, acl.PermFor(a)), true, a)
	if err == nil || IsBeforeVerify(err) || !errors.Is(err, ErrAccessDenied) ||
		r.ObjState == nil || !r.ObjState.Exists || r.Env.Zone.Release() < denc.Tentacle {
		return err
	}
	return fmt.Errorf("%w: %w", ErrAccessDenied.WithMessage("missing "+a.String()+" permission"), err)
}

// Execute is RGWGetObj::execute
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L2214-L2462 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L2449-L2694 at v20.2.4)
// in its order: init_common's parse of the Range and the dates,
// Read::prepare's part redirect and conditionals, the torrent, compression,
// cloud-tier and Swift large object checks, the range against the object,
// rgw_s3_prepare_decrypt, the response-* parameters send_response_data
// checks, and the read. The handler's Sink wrapper renders the headers on the
// first Write or WriteHeader, so an error before the first byte reaches the
// handler as radosgw's send_response_data_error does.
func (o *GetObject) Execute(ctx context.Context, r *Request) error {
	in, err := o.initCommon(r)
	if err != nil {
		return err
	}
	return o.serve(ctx, r, in)
}

// commonInputs is what init_common parses: the range and the two dates.
type commonInputs struct {
	ofs, end          int64
	since, unmodSince *time.Time
}

// initCommon is RGWGetObj::init_common
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L2464-L2487 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L2696-L2719 at v20.2.4).
func (o *GetObject) initCommon(r *Request) (commonInputs, error) {
	ofs, end, err := o.parseRange(r)
	if err != nil {
		return commonInputs{}, err
	}
	since, err := parseCondTime(o.IfModifiedSince)
	if err != nil {
		return commonInputs{}, err
	}
	unmodSince, err := parseCondTime(o.IfUnmodifiedSince)
	if err != nil {
		return commonInputs{}, err
	}
	return commonInputs{ofs: ofs, end: end, since: since, unmodSince: unmodSince}, nil
}

// serve is execute past init_common.
func (o *GetObject) serve(ctx context.Context, r *Request, in commonInputs) error {
	if err := o.prepare(ctx, r, in.since, in.unmodSince); err != nil {
		return err
	}
	if o.Torrent {
		return o.torrent(ctx, r)
	}
	if err := o.setObjSize(r); err != nil {
		return err
	}
	if o.GetData {
		if err := o.checkCloudTier(ctx, r); err != nil {
			return err
		}
	}
	if err := o.refuseLargeObject(ctx, r); err != nil {
		return err
	}
	if err := o.resolveRange(r, in.ofs, in.end); err != nil {
		return err
	}
	if err := o.prepareDecrypt(ctx, r); err != nil {
		return err
	}
	if err := o.checkOverrides(r); err != nil {
		return err
	}
	ck, err := o.readChecksum(r)
	if err != nil {
		return err
	}
	o.Checksum = ck
	return o.send(ctx, r)
}

// readChecksum is the stored checksum send_response_data reports for a
// request with x-amz-checksum-mode enabled and no Range
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L314-L318 and
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L548-L586 at v20.2.4):
// a partNumber read's part head's (partReadChecksum), else the object's
// (objectReadChecksum). Squid's radosgw shows none, and rgw-go shows what it
// stores there too (docs/exclusions.md, "Squid clusters show the checksums
// they store").
func (o *GetObject) readChecksum(r *Request) (*ReadChecksum, error) {
	if o.Range != "" || !checksumModeEnabled(o.ChecksumMode) {
		return nil, nil
	}
	if o.State != o.CondState {
		return partReadChecksum(o.State.Checksum), nil
	}
	return o.objectReadChecksum(r)
}

// checksumModeEnabled is get_params' checksum_mode: v is "enabled", ASCII
// case folded as boost::iequals folds it in radosgw's C locale.
func checksumModeEnabled(v string) bool {
	const enabled = "enabled"
	return len(v) == len(enabled) && strncaseEqual(v, enabled, len(v))
}

// objectReadChecksum is the object's checksum, typed by get_checksum_type
// for its parts count
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum.h#L382-L399 at v20.2.4),
// the value "<armor>-<parts>" when that is COMPOSITE. A composite on an
// object without parts shows nothing: its value combines parts the object
// no longer has, where radosgw shows it as the object's FULL_OBJECT
// checksum (docs/ceph-upstream-bugs.md, "radosgw carries a composite
// checksum to a copy without parts, which Tentacle reports as its
// FULL_OBJECT checksum").
func (o *GetObject) objectReadChecksum(r *Request) (*ReadChecksum, error) {
	c := shownChecksum(o.CondState.Checksum)
	if c == nil {
		return nil, nil
	}
	parts, err := o.objectPartsCount(r)
	if err != nil {
		return nil, err
	}
	if parts == 0 && c.Composite() {
		return nil, nil
	}
	rc := &ReadChecksum{Type: c.Type, Value: c.Armor(), ChecksumType: c.ObjectType(parts > 0)}
	if rc.ChecksumType == cksum.CompositeType {
		rc.Value += "-" + strconv.Itoa(parts)
	}
	return rc, nil
}

// partReadChecksum is a part head's checksum as main reports it for a
// partNumber read: its armor alone, typed by get_part_checksum_type
// (https://github.com/ceph/ceph/blob/f6f2529dd9efccf7710c8a2b561e5a2ab9d48bd3/src/rgw/rgw_rest_s3.cc#L569-L608
// at main), where v20.2.4 types it as the whole object's and adds the parts
// count (docs/ceph-upstream-bugs.md, "radosgw reports a partNumber read's
// checksum as the multipart object's composite").
func partReadChecksum(c *cksum.Cksum) *ReadChecksum {
	c = shownChecksum(c)
	if c == nil {
		return nil
	}
	return &ReadChecksum{Type: c.Type, Value: c.Armor(), ChecksumType: c.PartType()}
}

// shownChecksum is c, or nil for a record of type none, which holds no
// digest and which no radosgw writes.
func shownChecksum(c *cksum.Cksum) *cksum.Cksum {
	if c == nil || c.Type == cksum.None {
		return nil
	}
	return c
}

// objectPartsCount is the parts count of the object read, CondState: the
// count Tentacle's Read::prepare reports, taken from the manifest on Squid,
// where prepare reports none.
func (o *GetObject) objectPartsCount(r *Request) (int, error) {
	if o.PartsCount != nil {
		return *o.PartsCount, nil
	}
	m := o.CondState.Manifest
	if m == nil {
		return 0, nil
	}
	n, err := m.PartsCount()
	if err != nil {
		return 0, manifestError(r, err)
	}
	return n, nil
}

// Complete does nothing: the handler logs usage once the response is written.
func (o *GetObject) Complete(context.Context, *Request) {}

// resolveRange resolves the parsed range against ObjSize, as execute does
// after its checks
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L2396-L2406 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L2629-L2639 at v20.2.4):
// any Range header on an empty object is InvalidRange, and Length is
// total_len, 0 for an empty object.
func (o *GetObject) resolveRange(r *Request, ofs, end int64) error {
	if o.Range != "" && o.ObjSize == 0 {
		return fmt.Errorf("%w: a range of the empty object %s", ErrInvalidRange, r.Object.Name)
	}
	first, last, err := RangeToOfs(o.ObjSize, ofs, end)
	if err != nil {
		return err
	}
	o.Offset = first
	if o.ObjSize > 0 {
		o.Length = last + 1 - first
	}
	o.Status = http.StatusOK
	if o.Partial {
		o.Status = http.StatusPartialContent
	}
	return nil
}

// send writes the status alone for HEAD and for nothing to read, and
// otherwise streams the bytes to Sink
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L2436-L2457 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L2668-L2689 at v20.2.4).
func (o *GetObject) send(ctx context.Context, r *Request) error {
	if !o.GetData || o.Length == 0 {
		o.Sink.WriteHeader(o.Status, nil)
		return nil
	}
	if err := r.Env.Objects.ReadObject(ctx, o.State, ByteRange{Offset: o.Offset, Length: o.Length}, o.Sink); err != nil {
		return err
	}
	return o.Sink.Flush()
}

// prefetch is RGWGetObj::prefetch_data
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L2176-L2191 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L2411-L2426 at v20.2.4):
// a GET without a Range header reads the head's first chunk with its stat.
func (o *GetObject) prefetch() bool { return o.GetData && o.Range == "" }

func (o *GetObject) readHead(ctx context.Context, objects ObjectStore, rec *BucketRecord, key meta.ObjKey) (*ObjectState, error) {
	if o.prefetch() {
		return objects.PrefetchObject(ctx, rec, key)
	}
	return objects.StatObject(ctx, rec, key)
}

// parseRange is init_common's parse of the Range header
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L2464-L2471 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L2696-L2703 at v20.2.4):
// with rgw_ignore_get_invalid_range set, a range parse_range refuses is the
// whole object.
func (o *GetObject) parseRange(r *Request) (ofs, end int64, err error) {
	if o.Range == "" {
		return 0, -1, nil
	}
	ofs, end, o.Partial, err = ParseRange(o.Range)
	if err != nil && confBool(r, "rgw_ignore_get_invalid_range", false) {
		return 0, -1, nil
	}
	return ofs, end, err
}

// parseCondTime parses an If-Modified-Since or If-Unmodified-Since value,
// nil when the header is absent.
func parseCondTime(v string) (*time.Time, error) {
	if v == "" {
		return nil, nil //nolint:nilnil // an absent header is no date and no error
	}
	t, err := ParseHTTPTime(v)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// prepare is RGWRados::Object::Read::prepare
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6847-L7000 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7686-L7849 at v20.2.4)
// over the head Init read. Tentacle counts the parts of any multipart object;
// both releases count them for a part redirect, and radosgw reports the count
// only when prepare succeeds. Past the part redirect, where prepare hands the
// attrs to execute, a website request's object may answer with its redirect
// (websiteObjectRedirect).
func (o *GetObject) prepare(ctx context.Context, r *Request, since, unmodSince *time.Time) error {
	head := o.CondState
	var count *int
	if head != nil && head.Manifest != nil && r.Env.Zone.Release() >= denc.Tentacle {
		n, err := head.Manifest.PartsCount()
		if err != nil {
			return manifestError(r, err)
		}
		if n > 0 {
			count = &n
		}
	}
	if head == nil || !head.Exists {
		return fmt.Errorf("%w: %s", ErrNoSuchKey, r.Object.Name)
	}
	if o.PartNumber != nil {
		n, err := o.redirectToPart(ctx, r)
		if err != nil {
			return err
		}
		if n > 0 {
			count = &n
		}
	}
	if err := o.websiteObjectRedirect(r, o.checkConditions(r, since, unmodSince)); err != nil {
		return err
	}
	o.PartsCount = count
	return nil
}

// redirectToPart is get_part_obj_state
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6759-L6845 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7595-L7684 at v20.2.4)
// and prepare's handling of its result
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6880-L6917 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7728-L7765 at v20.2.4).
// It reads part n's head with the request's prefetch and serves it with the
// object's crypt attrs where the part has none, and returns the parts count.
// Part 1 of an object that is not multipart, or has no part 1, is the whole
// object, as "the java sdk's TransferManager.download()" expects, with no
// count. Any other part that is not there, and any failure to read its head,
// is InvalidPart. The version id stays the object's, where radosgw's is the
// instance of the part head's key, which the part's UploadPart request named
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L7076-L7105 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7738-L7767 at v20.2.4;
// docs/exclusions.md, "GET, HEAD and GetObjectAttributes name the version
// they read, null for the null version").
func (o *GetObject) redirectToPart(ctx context.Context, r *Request) (int, error) {
	head, n := o.CondState, *o.PartNumber
	var (
		count     int
		ofs, size uint64
		obj       meta.Obj
		found     bool
	)
	if m := head.Manifest; m != nil {
		var err error
		if count, err = m.PartsCount(); err != nil {
			return 0, manifestError(r, err)
		}
		if count > 0 {
			if ofs, size, obj, found, err = m.PartBounds(n); err != nil {
				return 0, manifestError(r, err)
			}
		}
	}
	if !found {
		if n == 1 {
			return 0, nil
		}
		return 0, fmt.Errorf("%w: %s has no part %d", ErrInvalidPart, r.Object.Name, n)
	}
	st, err := o.readHead(ctx, r.Env.Objects, partBucket(r.BucketRec, obj.Bucket), obj.Key)
	if err != nil {
		// radosgw answers InvalidPart whatever the read failed with, so the
		// cause is kept for the log but not matched.
		return 0, fmt.Errorf("%w: reading the head of part %d of %s: %v", ErrInvalidPart, n, r.Object.Name, err) //nolint:errorlint // see above
	}
	if st == nil || !st.Exists {
		return 0, fmt.Errorf("%w: part %d of %s has no head", ErrInvalidPart, n, r.Object.Name)
	}
	part := *st
	part.Attrs = maps.Clone(st.Attrs)
	if part.Attrs == nil {
		part.Attrs = map[string][]byte{}
	}
	if part.Manifest == nil {
		pm, err := partManifest(head.Manifest, n, ofs, size, obj)
		if err != nil {
			return 0, manifestError(r, err)
		}
		part.Manifest, part.Size = &pm, size
	}
	for k, v := range head.Attrs {
		if _, ok := part.Attrs[k]; !ok && strings.HasPrefix(k, meta.AttrCryptPrefix) {
			part.Attrs[k] = v
		}
	}
	o.State = &part
	return count, nil
}

// partBucket is rec naming bucket instead, as get_obj_state reads a part head:
// named by the manifest's bucket, a copy's source, and placed by the request
// bucket's rule.
func partBucket(rec *BucketRecord, bucket meta.BucketID) *BucketRecord {
	if bucket == rec.Info.Bucket {
		return rec
	}
	cp := *rec
	cp.Info.Bucket = bucket
	return &cp
}

// manifestError is a manifest the part lookups cannot walk, where radosgw's
// iterator would read past its maps.
func manifestError(r *Request, err error) error {
	return fmt.Errorf("%w: walking the manifest of %s: %w", ErrUnknown, r.Object.Name, err)
}

// partManifest is the manifest get_part_obj_state builds for a part head
// that has none, which is every part head radosgw writes
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6815-L6844 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7651-L7683 at v20.2.4):
// one rule for part n from offset 0, cut at the size of the part's first
// stripe, under the part's override prefix or the object's prefix, with the
// part head as an empty head and the part's size. Its stripes are the
// multipart object's stripes of part n, and the first is the part head, so a
// read of the part serves that stripe from the part head's prefetched bytes,
// as get_obj_iterate_cb does
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L7391-L7421 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L8242-L8273 at v20.2.4).
func partManifest(m *meta.Manifest, n int, ofs, size uint64, head meta.Obj) (meta.Manifest, error) {
	it, err := m.Seek(ofs)
	if err != nil {
		return meta.Manifest{}, err
	}
	// The iterator's override prefix is that of the rule holding ofs: the
	// last one to start at or before it.
	var (
		rule  meta.ManifestRule
		begin uint64
		found bool
	)
	for k, r := range m.Rules {
		if k <= ofs && (!found || k > begin) {
			rule, begin, found = r, k, true
		}
	}
	pm := meta.NewManifest()
	pm.Rules = map[uint64]meta.ManifestRule{0: {StartPartNum: uint32(n), StripeMaxSize: it.StripeSize()}} //nolint:gosec // a part number PartBounds found
	pm.Prefix = m.Prefix
	if rule.OverridePrefix != "" {
		pm.Prefix = rule.OverridePrefix
	}
	pm.HeadPlacementRule = m.HeadPlacementRule
	pm.Obj = head
	pm.TailPlacement = meta.BucketPlacement{Bucket: head.Bucket, PlacementRule: m.TailPlacement.PlacementRule.InheritFrom(m.HeadPlacementRule)}
	pm.TailInstance = head.Key.Instance
	pm.ObjSize = size
	return pm, nil
}

// checkConditions is Read::prepare's conditional block
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6945-L6990 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7793-L7839 at v20.2.4).
// The dates are compared with the served state's mtime at a second's
// precision, as for any request that is not a system request. The etags are
// compared with the object's ETag attr, which an unquoted If-Match must start
// with and an If-None-Match must not; "*" is no wildcard. An object without
// the attr fails get_attr with -ENODATA, which the S3 error table lacks.
func (o *GetObject) checkConditions(r *Request, since, unmodSince *time.Time) error {
	mtime := o.State.Mtime.Unix()
	if since != nil && o.IfNoneMatch == "" && since.Unix() >= mtime {
		return ErrNotModified
	}
	if unmodSince != nil && o.IfMatch == "" && unmodSince.Unix() < mtime {
		return ErrPreconditionFailed
	}
	if o.IfMatch == "" && o.IfNoneMatch == "" {
		return nil
	}
	etag, ok := o.CondState.Attrs[meta.AttrETag]
	if !ok {
		return fmt.Errorf("%w: %s has no etag to compare", ErrUnknown, r.Object.Name)
	}
	if o.IfMatch != "" && !strings.HasPrefix(Unquote(o.IfMatch), string(etag)) {
		return ErrPreconditionFailed
	}
	if o.IfNoneMatch != "" && strings.HasPrefix(Unquote(o.IfNoneMatch), string(etag)) {
		return ErrNotModified
	}
	return nil
}

// torrent is execute's ?torrent branch
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L2275-L2299 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L2511-L2535 at v20.2.4).
// rgw-go serves no torrent (docs/exclusions.md).
func (o *GetObject) torrent(ctx context.Context, r *Request) error {
	if string(o.State.Attrs[meta.AttrCryptMode]) == "SSE-C-AES256" {
		return fmt.Errorf("%w: no torrent of %s, which is encrypted with SSE-C", ErrInvalidArgument, r.Object.Name)
	}
	if _, ok := o.State.Attrs[meta.AttrTorrent]; !ok {
		return fmt.Errorf("%w: %s has no torrent", ErrNoSuchKey, r.Object.Name)
	}
	slog.InfoContext(ctx, "refusing a torrent: torrents are not implemented",
		slog.String("bucket", r.Bucket), slog.String("key", r.Object.Name))
	return fmt.Errorf("%w: torrents are not implemented", ErrNotImplemented)
}

// setObjSize is s->obj_size after rgw_compression_info_from_attrset
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_compression.cc#L10-L40 at v19.2.6
// and v20.2.4): the uncompressed size of a compressed object. Compression
// info of type none is served as stored; info with no blocks is -EIO.
func (o *GetObject) setObjSize(r *Request) error {
	o.ObjSize = o.State.Size
	c := o.State.Compression
	if c == nil {
		return nil
	}
	if len(c.Blocks) == 0 {
		return fmt.Errorf("%w: the compression info of %s has no blocks", ErrUnknown, r.Object.Name)
	}
	if c.Type != "none" {
		o.ObjSize = c.OrigSize
	}
	return nil
}

// checkCloudTier is handle_cloudtier_obj for a GET
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L938-L968 at v19.2.6;
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L989-L1122 at v20.2.4,
// with read-through). Squid refuses an object transitioned to cloud-s3.
// Tentacle, for either S3 tier type, serves a restored object, answers a
// restore in progress, and otherwise looks the tier up by the bucket's
// placement and the object's storage class. A tier that allows read-through
// would start a restore, which rgw-go does not implement
// (docs/exclusions.md).
func (o *GetObject) checkCloudTier(ctx context.Context, r *Request) error {
	m := o.State.Manifest
	if m == nil {
		return nil
	}
	if r.Env.Zone.Release() < denc.Tentacle {
		if m.TierType != meta.TierTypeCloudS3 {
			return nil
		}
		return ErrInvalidObjectState.WithMessage("This object was transitioned to cloud-s3")
	}
	if !isS3Tier(m.TierType) {
		return nil
	}
	if b, ok := o.State.Attrs[meta.AttrRestoreStatus]; ok {
		if len(b) == 0 {
			// Decoding nothing throws end_of_buffer, which
			// handle_cloudtier_obj takes for an object that is not tiered.
			return nil
		}
		switch meta.RestoreStatus(b[0]) {
		case meta.RestoreAlreadyInProgress:
			return ErrRequestTimeout.WithMessage("restore is still in progress")
		case meta.CloudRestored:
			return nil
		}
	}
	rule := r.BucketRec.Info.PlacementRule
	if b, ok := o.State.Attrs[meta.AttrStorageClass]; ok {
		rule.StorageClass = string(b)
	}
	tier, ok := r.Env.Zone.ZoneGroup().PlacementTargets[rule.Name].TierTargets[rule.StorageClass]
	if !ok {
		// get_placement_tier's -ENOENT.
		return fmt.Errorf("%w: no cloud tier for placement %q and storage class %q", ErrNoSuchKey, rule.Name, rule.StorageClass)
	}
	if !isS3Tier(tier.TierType) {
		return ErrInvalidArgument.WithMessage("failed to restore object")
	}
	if !tier.AllowReadThrough {
		return ErrInvalidObjectState.WithMessage("Read through is not enabled for this config")
	}
	slog.ErrorContext(ctx, "cloud read-through restore is not implemented",
		slog.String("bucket", r.Bucket), slog.String("key", r.Object.Name), slog.String("storage_class", rule.StorageClass))
	return ErrInvalidObjectState.WithMessage("This object was transitioned to cloud-s3")
}

// isS3Tier is is_tier_type_s3.
func isS3Tier(t string) bool { return t == meta.TierTypeCloudS3 || t == meta.TierTypeCloudS3Glacier }

// refuseLargeObject refuses a Swift dynamic or static large object, which
// radosgw composes from its segments
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L2373-L2394 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L2606-L2627 at v20.2.4)
// and rgw-go does not (docs/exclusions.md).
func (o *GetObject) refuseLargeObject(ctx context.Context, r *Request) error {
	for _, a := range []string{meta.AttrUserManifest, meta.AttrSLOManifest} {
		if _, ok := o.State.Attrs[a]; ok {
			slog.InfoContext(ctx, "refusing a swift large object: composing one is not implemented",
				slog.String("bucket", r.Bucket), slog.String("key", r.Object.Name), slog.String("attr", a))
			return fmt.Errorf("%w: %s is a Swift large object", ErrNotImplemented, r.Object.Name)
		}
	}
	return nil
}

// The messages rgw_s3_prepare_decrypt sets for an SSE-C request it refuses.
const (
	sseCAlgorithmMissing = "Requests specifying Server Side Encryption with Customer provided keys must provide a valid encryption algorithm."
	sseCAlgorithmInvalid = "The requested encryption algorithm is not valid, must be AES256."
	sseCKeyInvalid       = "Requests specifying Server Side Encryption with Customer provided keys must provide an appropriate secret key."
	sseCKeyMD5Invalid    = "Requests specifying Server Side Encryption with Customer provided keys must provide an appropriate secret key md5."
	sseCKeyMismatch      = "The calculated MD5 hash of the key did not match the hash that was provided."
)

// sseCKeySize is AES_256_CBC::AES_256_KEYSIZE.
const sseCKeySize = 32

// prepareDecrypt is rgw_s3_prepare_decrypt
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_crypt.cc#L1303-L1505 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_crypt.cc#L1318-L1527 at v20.2.4),
// which GET and HEAD call alike, by the object's stored mode. rgw-go has no
// key server, so SSE-KMS, SSE-S3 and RGW-AUTO answer as radosgw does without
// one (docs/exclusions.md); it does not decrypt SSE-C yet, so an SSE-C
// request that passes every check is NotImplemented. rgw_crypt_require_ssl,
// true when it cannot be read, refuses a request that is not secure, except
// for SSE-S3 on Tentacle.
func (o *GetObject) prepareDecrypt(ctx context.Context, r *Request) error {
	if o.SSEHeader {
		return fmt.Errorf("%w: x-amz-server-side-encryption on a read", ErrInvalidRequest)
	}
	insecure := confBool(r, "rgw_crypt_require_ssl", true) && !o.Secure
	errInsecure := fmt.Errorf("%w: rgw_crypt_require_ssl refuses an insecure request", ErrInvalidRequest)
	switch mode := string(o.State.Attrs[meta.AttrCryptMode]); mode {
	case "SSE-C-AES256":
		if insecure {
			return errInsecure
		}
		return o.checkSSEC(ctx, r)
	case "SSE-KMS":
		if insecure {
			return errInsecure
		}
		return ErrInvalidArgument.WithMessage("Failed to retrieve the actual key, kms-keyid: " + string(o.State.Attrs[meta.AttrCryptKeyID]))
	case "RGW-AUTO":
		// rgw_crypt_default_encryption_key, unset, does not decode to 32
		// bytes: -EIO.
		return fmt.Errorf("%w: no default encryption key for %s", ErrUnknown, r.Object.Name)
	case "AES256":
		if insecure && r.Env.Zone.Release() < denc.Tentacle {
			return errInsecure
		}
		return ErrInvalidArgument.WithMessage("Failed to retrieve the actual key")
	}
	return nil
}

// checkSSEC is rgw_s3_prepare_decrypt's SSE-C branch: the algorithm, the key
// and its MD5, which must match both the key and the MD5 stored with the
// object.
func (o *GetObject) checkSSEC(ctx context.Context, r *Request) error {
	switch o.SSECAlgorithm {
	case "":
		return ErrInvalidArgument.WithMessage(sseCAlgorithmMissing)
	case "AES256":
	default:
		return ErrInvalidEncryptionAlgorithm.WithMessage(sseCAlgorithmInvalid)
	}
	key, ok := fromBase64(o.SSECKey)
	if !ok || len(key) != sseCKeySize {
		return ErrInvalidArgument.WithMessage(sseCKeyInvalid)
	}
	keyMD5, ok := fromBase64(o.SSECKeyMD5)
	if !ok || len(keyMD5) != md5.Size {
		return ErrInvalidArgument.WithMessage(sseCKeyMD5Invalid)
	}
	sum := md5.Sum(key) //nolint:gosec // SSE-C names its key by the key's MD5
	if !bytes.Equal(sum[:], keyMD5) || !bytes.Equal(o.State.Attrs[meta.AttrCryptKeyMD5], keyMD5) {
		return ErrInvalidArgument.WithMessage(sseCKeyMismatch)
	}
	slog.InfoContext(ctx, "refusing an sse-c read: decryption is not implemented",
		slog.String("bucket", r.Bucket), slog.String("key", r.Object.Name))
	return fmt.Errorf("%w: SSE-C decryption is not implemented", ErrNotImplemented)
}

// checkOverrides is send_response_data's check of the response-* parameters
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L513-L533 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L630-L650 at v20.2.4):
// none from an anonymous request, and no value with a byte iscntrl takes.
// radosgw tests each char, signed, so a byte above 0x7f passes. radosgw
// makes the check while it writes the headers and mishandles its refusal;
// rgw-go answers it with a plain 400 (docs/exclusions.md).
func (o *GetObject) checkOverrides(r *Request) error {
	if len(o.ResponseOverrides) == 0 {
		return nil
	}
	if r.Identity.Anonymous {
		return fmt.Errorf("%w: response-* parameters on an anonymous request", ErrInvalidRequest)
	}
	for name, v := range o.ResponseOverrides {
		if strings.ContainsFunc(v, func(c rune) bool { return c < 0x20 || c == 0x7f }) {
			return fmt.Errorf("%w: a control character in the %s override", ErrInvalidRequest, name)
		}
	}
	return nil
}

// confBool reads a boolean option, def when it cannot be read.
func confBool(r *Request, name string, def bool) bool {
	if r.Env.Conf == nil {
		return def
	}
	v, err := r.Env.Conf.Bool(name)
	if err != nil {
		return def
	}
	return v
}
