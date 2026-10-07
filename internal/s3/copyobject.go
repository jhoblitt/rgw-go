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
	"github.com/jhoblitt/rgw-go/internal/xmltext"
)

// copyObject is copy_obj (RGWCopyObj_ObjStore_S3, get_params :3478-3551,
// [T] :3758-3831; send_partial_response and send_response :3566-3603, [T]
// :3846-3886). The source is x-amz-copy-source as parse_copy_location and
// postauth_init read it; the four x-amz-copy-source-if-* conditions are
// their headers' last values, nil when absent; the rest of get_params runs
// through copyObjectParams once the destination bucket is loaded.
//
// The success is framed as radosgw frames it: send_partial_response(0)
// sends the common headers and application/xml without a length, so that
// net/http frames the body chunked, then the XML declaration and the
// CopyObjectResult open tag, flushed; send_response then sends LastModified,
// the ETag in the quotes dump_format adds, escaped, and the close tag,
// flushed. LastModified is rgw_to_iso8601's milliseconds on Squid and whole
// seconds on Tentacle (dump_time_exact_seconds, [T] :3879). A local copy
// sends no Progress element: only fetch_remote_obj reports progress
// (driver/rados/rgw_rados.cc:4726-4736 at v19.2.6).
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
	o.Params = func(ctx context.Context, o *op.CopyObject) error { return wr.copyObjectParams(ctx, r, o) }
	if err := op.Run(ctx, o, r); err != nil {
		return err
	}
	mtime := o.Mtime
	if r.Env.Zone.Release() >= denc.Tentacle {
		mtime = mtime.Truncate(time.Second)
	}
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
		slog.DebugContext(ctx, "copy result not delivered", slog.String("request_id", r.ID), slog.Any("error", err))
	}
	return nil
}

// copyObjectParams is get_params in its order, then init_dest_policy and
// init_common's attrs, which radosgw makes after authorizing
// (docs/exclusions.md): the object-lock headers (objectLock, with the
// messages a copy sets); x-amz-metadata-directive, COPY or REPLACE in any
// case and anything else InvalidArgument, "Unknown metadata directive."; the
// storage-class check of a copy onto itself that does not replace the
// metadata (need_to_check_storage_class); a request that asks for encryption
// refused (refuseEncryption); the destination ACL; and the attrs, the
// headers' alone (requestAttrs).
func (wr *objectWrites) copyObjectParams(ctx context.Context, r *op.Request, o *op.CopyObject) error {
	if err := objectLock(r, true); err != nil {
		return err
	}
	if d, ok := op.HeaderValue(r.Header, "X-Amz-Metadata-Directive"); ok {
		switch d = rgwtext.CString(d); {
		case strings.EqualFold(d, "COPY"):
		case strings.EqualFold(d, "REPLACE"):
			o.Replace = true
		default:
			return op.ErrInvalidArgument.WithMessage("Unknown metadata directive.")
		}
	}
	o.CheckStorageClass = r.Tenant == o.SrcTenant && r.Bucket == o.SrcBucket && r.Object.Name == o.SrcKey.Name &&
		o.SrcKey.Instance == "" && !o.Replace
	if err := refuseEncryption(r, false); err != nil {
		return err
	}
	p, err := newObjectACL(ctx, r)
	if err != nil {
		return err
	}
	o.ACL = p
	o.Attrs, err = requestAttrs(r, wr.generic, false)
	return err
}

// parseCopySource is RGWCopyObj::parse_copy_location (rgw_op.cc:5329-5374 at
// v19.2.6, :5895-5940 at v20.2.4) and postauth_init's rgw_parse_url_bucket
// of its bucket (rgw_rest_s3.cc:4985-4994, [T] :5545-5554; rgw_bucket.cc
// :112-132 at v19.2.6): "[/]bucket/key" up to the first "?", url-decoded,
// the bucket "tenant:name" split at its first colon or in defaultTenant, and
// versionId from what follows the "?" as RGWHTTPArgs reads it. It is false
// for an empty value, one with no "/" or no key, and a bucket with nothing
// after its colon.
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
