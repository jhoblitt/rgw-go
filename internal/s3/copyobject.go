package s3

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/rgwtext"
	"github.com/jhoblitt/rgw-go/internal/tags"
	"github.com/jhoblitt/rgw-go/internal/xmltext"
)

// copyObject is copy_obj (RGWCopyObj_ObjStore_S3, get_params
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L3478-L3551,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L3758-L3831;
// send_partial_response and send_response
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L3566-L3603,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L3846-L3886).
// The source is x-amz-copy-source as parse_copy_location and postauth_init
// read it; the four x-amz-copy-source-if-* conditions are their headers'
// last values, nil when absent; the part of get_params that reads the
// request alone runs through copyObjectParams once the destination bucket is
// loaded, and the checks of stored state through copyObjectChecks once the
// destination and the source allow the request, so that a refused requester
// learns nothing the store holds (docs/differences.md).
//
// The success is framed as radosgw frames it, with the two version headers
// radosgw does not send (setVersionHeaders) and x-amz-expiration, which it
// does not send either (setStoredExpiration): send_partial_response(0)
// sends the common headers and application/xml without a length, so that
// net/http frames the body chunked, then the XML declaration and the
// CopyObjectResult open tag, flushed; send_response then sends LastModified,
// the ETag in the quotes dump_format adds, escaped, and the close tag,
// flushed. LastModified is rgw_to_iso8601's milliseconds on Squid and whole
// seconds on Tentacle (dump_time_exact_seconds,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L3879). A
// local copy sends no Progress element: only fetch_remote_obj reports
// progress
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L4726-L4736 at v19.2.6).
func (wr *objectWrites) copyObject(ctx context.Context, w http.ResponseWriter, r *op.Request) error {
	v, _ := op.HeaderValue(r.Header, "X-Amz-Copy-Source")
	tenant, bucket, key, ok := parseCopySource(v, r.Identity.Tenant)
	if !ok {
		return op.ErrInvalidArgument
	}
	o := &op.CopyObject{
		SrcTenant: tenant, SrcBucket: bucket, SrcKey: key,
		StorageClass:      header(r, "X-Amz-Storage-Class"),
		IfMatch:           headerPtr(r, "X-Amz-Copy-Source-If-Match"),
		IfNoneMatch:       headerPtr(r, "X-Amz-Copy-Source-If-None-Match"),
		IfModifiedSince:   headerPtr(r, "X-Amz-Copy-Source-If-Modified-Since"),
		IfUnmodifiedSince: headerPtr(r, "X-Amz-Copy-Source-If-Unmodified-Since"),
	}
	o.Params = func(_ context.Context, o *op.CopyObject) error { return wr.copyObjectParams(r, o) }
	o.Authorized = func(ctx context.Context, o *op.CopyObject) error { return copyObjectChecks(ctx, r, o) }
	if err := op.Run(ctx, o, r); err != nil {
		return err
	}
	mtime := o.Mtime
	if r.Env.Zone.Release() >= denc.Tentacle {
		mtime = mtime.Truncate(time.Second)
	}
	setVersionHeaders(w.Header(), o.VersionID, o.SrcVersionID)
	setStoredExpiration(w.Header(), r, o.Tags, o.Mtime, o.OrigSize)
	cx, err := startChunkedXML(w, r)
	if err == nil {
		cx.add(xmlHeader + `<CopyObjectResult xmlns="` + xmlnsS3 + `">`)
		err = cx.flush()
	}
	if err == nil {
		cx.add("<LastModified>" + xmltext.Escape(ISO8601(mtime)) + "</LastModified>")
		if o.ETag != "" {
			cx.add("<ETag>" + xmltext.Escape(`"`+o.ETag+`"`) + "</ETag>")
		}
		cx.add("</CopyObjectResult>")
		err = cx.flush()
	}
	if err != nil {
		slog.DebugContext(ctx, "copy result not delivered", slog.Any("error", err))
	}
	return nil
}

// copyObjectParams is the part of get_params that reads the request alone,
// in its order, then init_common's attrs, which radosgw builds after
// authorizing (docs/differences.md): the object-lock headers' retention and
// legal hold (objectLockHeaders, with the messages a copy sets), which r.Lock
// gives the policies' object-lock keys before the op authorizes;
// x-amz-metadata-directive, COPY or REPLACE in any case and anything else
// InvalidArgument, "Unknown metadata directive."; under REPLACE, an
// x-amz-tagging that does not parse refused InvalidArgument (replaceTags); the
// storage-class check of a copy onto itself that does not replace the metadata
// (need_to_check_storage_class); a request that asks for encryption refused,
// and one that sends a header emptyMessage covers with no value
// (refuseEncryption); and the attrs, the headers' alone (requestAttrs), with
// the retention's and the legal hold's (requestLock.addAttrs), which the copy
// keeps under COPY and REPLACE alike in place of its source's.
func (wr *objectWrites) copyObjectParams(r *op.Request, o *op.CopyObject) error {
	lock, lerr := objectLockHeaders(r, true)
	if lerr != nil {
		return lerr
	}
	r.Lock = lock.conditions()
	if d, ok := op.HeaderValue(r.Header, "X-Amz-Metadata-Directive"); ok {
		switch d = rgwtext.CString(d); {
		case strings.EqualFold(d, "COPY"):
		case strings.EqualFold(d, "REPLACE"):
			o.Replace = true
		default:
			return op.ErrInvalidArgument.WithMessage("Unknown metadata directive.")
		}
	}
	if o.Replace {
		if err := replaceTags(r); err != nil {
			return err
		}
	}
	o.CheckStorageClass = r.Tenant == o.SrcTenant && r.Bucket == o.SrcBucket && r.Object.Name == o.SrcKey.Name &&
		o.SrcKey.Instance == "" && !o.Replace
	if err := refuseEncryption(r, false); err != nil {
		return err
	}
	attrs, err := requestAttrs(r, wr.generic, false)
	if err != nil {
		return err
	}
	lock.addAttrs(attrs)
	o.Attrs = attrs
	return nil
}

// replaceTags refuses, InvalidArgument as PutObject's get_params refuses one
// that does not parse, the x-amz-tagging header of a copy that replaces its
// metadata, and so stores the header, when the header does not parse as a tag
// set or when formatXattr would store it rewritten, as text that is not the
// tag set parsed and that the tag set's decode does not read. radosgw parses no
// tag header for a copy: its get_params reads none
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3508-L3581 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L3758-L3831 at v20.2.4),
// and init_common stores it as sent, once authorized
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L5526 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L6089 at v20.2.4).
// The copy's s3:PutObject and s3:PutObjectTagging checks take the request's
// tag keys from the parsed header, so either would leave the checks judging
// tags other than those stored: a Deny conditioned on a tag unevaluated, or
// an Allow conditioned on one granting a copy that stores none
// (docs/differences.md).
func replaceTags(r *op.Request) error {
	v, ok := op.HeaderValue(r.Header, "X-Amz-Tagging")
	if !ok {
		return nil
	}
	if _, err := tags.ParseHeader(v, tags.MaxObjectTags); err != nil || formatXattr(v) != v {
		return op.ErrInvalidArgument
	}
	return nil
}

// copyObjectChecks is, once the destination and the source allow the
// request, the refusal of a retention or a legal hold on a bucket without
// object lock (objectLock), which radosgw's copy does not make, then
// init_dest_policy's ACL
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5492 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L6058 at v20.2.4),
// create_s3_policy's for the requester, whose grantees are looked up.
func copyObjectChecks(ctx context.Context, r *op.Request, o *op.CopyObject) error {
	if err := objectLock(r); err != nil {
		return err
	}
	p, err := newObjectACL(ctx, r)
	if err != nil {
		return err
	}
	o.ACL = p
	return nil
}

// parseCopySource is RGWCopyObj::parse_copy_location
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5329-L5374 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L5895-L5940 at v20.2.4)
// and postauth_init's rgw_parse_url_bucket of its bucket
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L4985-L4994,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5545-L5554;
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_bucket.cc#L112-L132 at v19.2.6):
// "[/]bucket/key" up to the first "?", url-decoded, the bucket "tenant:name"
// split at its first colon or in defaultTenant, and versionId from what
// follows the "?" as RGWHTTPArgs reads it. It is false for an empty value,
// one with no "/" or no key, and a bucket with nothing after its colon.
func parseCopySource(v, defaultTenant string) (tenant, bucket string, key meta.ObjKey, ok bool) {
	name, params, _ := strings.Cut(v, "?")
	if name == "" {
		return "", "", meta.ObjKey{}, false
	}
	b, k, found := strings.Cut(rgwtext.URLDecode(strings.TrimPrefix(name, "/"), false), "/")
	if !found || k == "" {
		return "", "", meta.ObjKey{}, false
	}
	key = meta.ObjKey{Name: k}
	if params != "" {
		key.Instance = parseArgs(params).Get("versionId")
	}
	tenant, bucket = defaultTenant, b
	if t, name, found := strings.Cut(b, ":"); found {
		if name == "" {
			return "", "", meta.ObjKey{}, false
		}
		tenant, bucket = t, name
	}
	return tenant, bucket, key, true
}
