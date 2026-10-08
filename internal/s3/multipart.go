package s3

import (
	"context"
	"crypto/md5" //nolint:gosec // Content-MD5 is an MD5
	"encoding/xml"
	"fmt"
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

// The multipart routes are rgw_rest_s3.cc's RGWInitMultipart_ObjStore_S3,
// RGWCompleteMultipart_ObjStore_S3, RGWAbortMultipart_ObjStore_S3,
// RGWListMultipart_ObjStore_S3 and RGWListBucketMultiparts_ObjStore_S3, and
// RGWPutObj_ObjStore_S3 for a PUT naming an upload. A bare line number is
// v19.2.6's rgw_rest_s3.cc; [T] marks v20.2.4's.

// multipartHandlers is the multipart upload routes over env's options; each
// entry binds to the one scope Dispatch routes its name in. UploadPart and
// UploadPartCopy ride put_obj, which putObject hands to uploadPart.
func multipartHandlers(env *op.Env) map[string]HandlerFunc {
	writes := newObjectWrites(env)
	return map[string]HandlerFunc{
		"init_multipart":         writes.initMultipart,
		"complete_multipart":     completeMultipart,
		"abort_multipart":        abortMultipart,
		"list_multipart":         listParts,
		"list_bucket_multiparts": listMultipartUploads,
	}
}

// initiateMultipartUploadResult is RGWInitMultipart_ObjStore_S3's document
// (:4045-4052, [T] :4546-4553).
type initiateMultipartUploadResult struct {
	XMLName  xml.Name     `xml:"InitiateMultipartUploadResult"`
	Xmlns    string       `xml:"xmlns,attr"`
	Tenant   xmltext.Text `xml:"Tenant,omitempty"`
	Bucket   xmltext.Text `xml:"Bucket"`
	Key      xmltext.Text `xml:"Key"`
	UploadID xmltext.Text `xml:"UploadId"`
}

// initMultipart is init_multipart, CreateMultipartUpload. The storage class
// is x-amz-storage-class, which the op checks against the zone once the
// requester is authorized; the rest of get_params runs through
// initMultipartParams then too, as RGWInitMultipart::execute calls it
// (rgw_op.cc:6299 at v19.2.6, :6968 at v20.2.4). The success is
// send_response's (:4029-4055, [T] :4526-4556): 200 and the document, whose
// length radosgw's frontend adds, without x-amz-abort-date while lifecycle
// is not served.
func (wr *objectWrites) initMultipart(ctx context.Context, w http.ResponseWriter, r *op.Request) error {
	o := &op.InitMultipart{StorageClass: header(r, "X-Amz-Storage-Class")}
	o.Params = func(ctx context.Context, o *op.InitMultipart) error { return wr.initMultipartParams(ctx, r, o) }
	if err := op.Run(ctx, o, r); err != nil {
		return err
	}
	WriteXML(w, r, http.StatusOK, initiateMultipartUploadResult{
		Xmlns: xmlnsS3, Tenant: xmltext.Text(r.Tenant), Bucket: xmltext.Text(r.Bucket),
		Key: xmltext.Text(r.Object.Name), UploadID: xmltext.Text(o.UploadID),
	})
	return nil
}

// initMultipartParams is RGWInitMultipart_ObjStore_S3::get_params
// (:3969-4027, [T] :4450-4524) and the reads RGWInitMultipart::execute makes
// after it (rgw_op.cc:6293-6343 at v19.2.6, :6963-7019 at v20.2.4), in their
// order, once the op has refused a bucket's default encryption, as
// get_encryption_defaults comes first: the ACL, create_s3_policy's for the
// requester, its bucket-owner grants naming the bucket's ACL owner;
// x-amz-tagging, any refusal InvalidArgument; the object-lock headers
// (multipartObjectLock); a request that asks for encryption, which
// prepare_encryption reads; and the attrs, the headers' alone, as no
// map_qs_metadata runs.
func (wr *objectWrites) initMultipartParams(ctx context.Context, r *op.Request, o *op.InitMultipart) error {
	p, aclErr := newObjectACL(ctx, r)
	if aclErr != nil {
		return aclErr
	}
	o.ACL = p
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
	if err := multipartObjectLock(r); err != nil {
		return err
	}
	if err := refuseEncryption(r, false); err != nil {
		return err
	}
	attrs, err := requestAttrs(r, wr.generic, false)
	o.Attrs = attrs
	return err
}

// copyPartResult is send_response's document for a part copied from a
// source (:2761-2770, [T] :2931-2940).
type copyPartResult struct {
	XMLName      xml.Name     `xml:"CopyPartResult"`
	Xmlns        string       `xml:"xmlns,attr"`
	LastModified xmltext.Text `xml:"LastModified"`
	ETag         xmltext.Text `xml:"ETag"`
}

// uploadPart serves put_obj requests carrying uploadId: UploadPart and
// UploadPartCopy. putObject calls it before anything else. The storage class
// and the canned ACL are their headers'; the copy source is
// x-amz-copy-source as RGWPutObj::init_processing reads it (partCopySource)
// and x-amz-copy-source-range as sent; the four x-amz-copy-source-if-*
// conditions are their headers' last values, nil when absent. The part of
// get_params that reads the request alone runs through uploadPartParams
// before the permission check, as init_processing calls get_params; the
// rest, and the checks radosgw makes in execute, run through
// uploadPartChecks once the requester is authorized, so that a refused
// requester learns nothing the store holds (docs/exclusions.md).
//
// The success is send_response's (:2730-2782, [T] :2891-2952), whose status
// rgw_s3_success_create_obj_status names for both forms: for a part with a
// body the ETag, the Content-Length of 0 with Accept-Ranges that
// dump_content_length sends, and Rgwx-Mtime for a system request; for a
// copied part CopyPartResult, its LastModified the mtime's whole seconds with
// a literal .000Z, as strftime's "%Y-%m-%dT%T.000Z" writes it, and its
// length the frontend's.
func (wr *objectWrites) uploadPart(ctx context.Context, w http.ResponseWriter, r *op.Request) error {
	o := &op.UploadPart{
		UploadID: r.Query.Get("uploadId"), Body: r.Body, Size: r.ContentLength,
		StorageClass: header(r, "X-Amz-Storage-Class"), CannedACL: header(r, "X-Amz-Acl"),
	}
	if v, _ := op.HeaderValue(r.Header, "X-Amz-Copy-Source"); v != "" {
		tenant, bucket, key, ok := partCopySource(v, r.Identity.Tenant)
		if ok {
			o.CopySource, o.SrcTenant, o.SrcBucket, o.SrcKey, o.Body = true, tenant, bucket, key, nil
			o.Range = headerPtr(r, "X-Amz-Copy-Source-Range")
			o.IfMatch = headerPtr(r, "X-Amz-Copy-Source-If-Match")
			o.IfNoneMatch = headerPtr(r, "X-Amz-Copy-Source-If-None-Match")
			o.IfModifiedSince = headerPtr(r, "X-Amz-Copy-Source-If-Modified-Since")
			o.IfUnmodifiedSince = headerPtr(r, "X-Amz-Copy-Source-If-Unmodified-Since")
		} else {
			o.SourceErr = op.ErrInvalidArgument
		}
	}
	var set *tags.Set
	o.Params = func(_ context.Context, o *op.UploadPart) error {
		var err error
		set, err = uploadPartParams(r, o)
		return err
	}
	o.Authorized = func(ctx context.Context, o *op.UploadPart) error { return wr.uploadPartChecks(ctx, r, o, set) }
	if err := op.Run(ctx, o, r); err != nil {
		return err
	}
	status := successStatus(r)
	if o.CopySource {
		WriteXML(w, r, status, copyPartResult{
			Xmlns: xmlnsS3, LastModified: xmltext.Text(o.Mtime.UTC().Truncate(time.Second).Format("2006-01-02T15:04:05") + ".000Z"),
			ETag: quotedETag(o.ETag),
		})
		return nil
	}
	SetCommonHeaders(w, r)
	h := w.Header()
	SetHeader(h, "ETag", `"`+o.ETag+`"`)
	SetContentLength(h, 0)
	setSystemMtime(h, r, o.Mtime)
	w.WriteHeader(status)
	return nil
}

// uploadPartParams is the part of RGWPutObj_ObjStore_S3::get_params for a
// part (:2597-2704, [T] :2758-2865) that reads the request alone, in its
// order, with the refusal putObjectParams makes beside it: a body without a
// Content-Length that is not chunked is MissingContentLength; a request that
// asks for encryption, by a header or by the query, is refused;
// x-amz-tagging, any refusal InvalidArgument, whose tags it returns; the
// object-lock headers' values (objectLockHeaders); and partNumber, absent
// InvalidArgument and otherwise strict_strtol's.
func uploadPartParams(r *op.Request, o *op.UploadPart) (*tags.Set, error) {
	if r.ContentLength == 0 && r.Header.Get("Content-Length") == "" {
		return nil, op.ErrMissingContentLength
	}
	if err := refuseEncryption(r, true); err != nil {
		return nil, err
	}
	var set *tags.Set
	if v, ok := op.HeaderValue(r.Header, "X-Amz-Tagging"); ok {
		s, err := tags.ParseHeader(v, tags.MaxObjectTags)
		if err != nil {
			return nil, op.ErrInvalidArgument
		}
		set = &s
	}
	if err := objectLockHeaders(r, false); err != nil {
		return nil, err
	}
	v := r.Query.Get("partNumber")
	if v == "" {
		return nil, fmt.Errorf("%w: an upload id without a part number", op.ErrInvalidArgument)
	}
	n, err := parseInt(rgwtext.CString(v))
	if err != nil {
		return nil, fmt.Errorf("%w: part number %q: %w", op.ErrInvalidArgument, v, err)
	}
	o.PartNumber = n
	return set, nil
}

// uploadPartChecks is the rest of get_params and what RGWPutObj::execute
// makes of a part's request (rgw_op.cc:4142-4531 at v19.2.6, :4351-4799 at
// v20.2.4), once the requester is authorized: the ACL, create_s3_policy's,
// whose grantees are looked up; a retention or a legal hold, refused for
// what the bucket holds (multipartObjectLock); a Content-MD5 that
// ceph_unarmor does not decode to 16 bytes, InvalidDigest (:4184-4198,
// :4393-4407); then the attrs, requestAttrs' with the query's x-amz-meta-*
// included, as map_qs_metadata adds them for put_obj, and the tag set, as
// encode_obj_tags_attr stores it on the part's head (:4531, :4799). radosgw
// builds the ACL and checks the object lock in get_params, before
// verify_permission, and the attrs once the part is stored
// (docs/exclusions.md).
func (wr *objectWrites) uploadPartChecks(ctx context.Context, r *op.Request, o *op.UploadPart, set *tags.Set) error {
	p, aclErr := newObjectACL(ctx, r)
	if aclErr != nil {
		return aclErr
	}
	o.ACL = p
	if err := multipartObjectLock(r); err != nil {
		return err
	}
	if v, ok := op.HeaderValue(r.Header, "Content-MD5"); ok {
		sum, ok := cephUnarmor(rgwtext.CString(v))
		if !ok || len(sum) != md5.Size {
			return op.ErrInvalidDigest
		}
		o.ContentMD5 = sum
	}
	attrs, err := requestAttrs(r, wr.generic, true)
	if err != nil {
		return err
	}
	if set != nil && set.Len() > 0 {
		e := denc.NewEncoder()
		set.Encode(e, r.Env.Zone.Release())
		attrs[tags.Attr] = e.Bytes()
	}
	o.Attrs = attrs
	return nil
}

// partCopySource is RGWPutObj::init_processing's read of a non-empty
// x-amz-copy-source (rgw_op.cc:3807-3851 at v19.2.6, :4016-4060 at v20.2.4),
// which differs from parse_copy_location's: the whole value is url-decoded
// first, less one leading "/", then split at its first "/", and the key is
// cut at its first "?versionId=", whatever follows being the version; a "?"
// that starts nothing else stays in the key. The bucket is "tenant:name"
// split at its first colon, or in defaultTenant. It is false for a value
// with no "/", a key that is empty, and a tenant with no bucket after it.
func partCopySource(v, defaultTenant string) (tenant, bucket string, key meta.ObjKey, ok bool) {
	src := strings.TrimPrefix(rgwtext.URLDecode(v, false), "/")
	bucket, name, found := strings.Cut(src, "/")
	if !found {
		return "", "", meta.ObjKey{}, false
	}
	name, instance, _ := strings.Cut(name, "?versionId=")
	if name == "" {
		return "", "", meta.ObjKey{}, false
	}
	tenant = defaultTenant
	if t, b, found := strings.Cut(bucket, ":"); found {
		if b == "" {
			return "", "", meta.ObjKey{}, false
		}
		tenant, bucket = t, b
	}
	return tenant, bucket, meta.ObjKey{Name: name, Instance: instance}, true
}

// multipartObjectLock is a multipart write's refusal of a retention or a
// legal hold for what the bucket holds: on a bucket without object lock
// InvalidRequest, as get_params answers (:2669-2673 and :4021-4024, [T]
// :2830-2834 and :4502-4505); on one with it NotImplemented, as radosgw
// keeps them on the upload and the parts' heads and rgw-go serves no object
// lock until versioning is served (docs/exclusions.md). The callers make it
// once the requester is authorized, so that a refused requester learns
// nothing of the bucket's configuration.
func multipartObjectLock(r *op.Request) error {
	switch {
	case !namesObjectLock(r):
		return nil
	case r.BucketRec.Info.Flags&meta.BucketObjLockEnabled == 0:
		return op.ErrInvalidRequest
	}
	return fmt.Errorf("%w: object lock on a multipart upload is not served yet", op.ErrNotImplemented)
}

// completeMultipartUploadResult is RGWCompleteMultipart_ObjStore_S3's
// document (:4084-4106, [T] :4614-4642): Location first, as dump_format
// writes it, then the tenant of a tenanted bucket.
type completeMultipartUploadResult struct {
	XMLName  xml.Name     `xml:"CompleteMultipartUploadResult"`
	Xmlns    string       `xml:"xmlns,attr"`
	Location xmltext.Text `xml:"Location"`
	Tenant   xmltext.Text `xml:"Tenant,omitempty"`
	Bucket   xmltext.Text `xml:"Bucket"`
	Key      xmltext.Text `xml:"Key"`
	ETag     xmltext.Text `xml:"ETag"`
}

// completeMultipart is complete_multipart, CompleteMultipartUpload. The op
// reads uploadId and, once the requester is authorized, the body; If-Match
// and If-None-Match are their headers' last values, nil when absent, which
// v20.2.4 reads (:4572-4573) and rgw-go applies on both releases
// (docs/exclusions.md). The success is send_response's (:4076-4108, [T]
// :4604-4644): 200 and the document, whose length radosgw's frontend adds,
// the ETag in the quotes dump_format adds.
func completeMultipart(ctx context.Context, w http.ResponseWriter, r *op.Request) error {
	o := &op.CompleteMultipart{
		UploadID: r.Query.Get("uploadId"), IfMatch: headerPtr(r, "If-Match"), IfNoneMatch: headerPtr(r, "If-None-Match"),
	}
	if err := op.Run(ctx, o, r); err != nil {
		return err
	}
	WriteXML(w, r, http.StatusOK, completeMultipartUploadResult{
		Xmlns: xmlnsS3, Location: xmltext.Text(completeLocation(r)), Tenant: xmltext.Text(r.Tenant),
		Bucket: xmltext.Text(r.Bucket), Key: xmltext.Text(r.Object.Name), ETag: quotedETag(o.ETag),
	})
	return nil
}

// completeLocation is RGWCompleteMultipart_ObjStore_S3::send_response's
// Location (rgw_rest_s3.cc:4086-4101, [T] :4616-4631) over
// compute_domain_uri (rgw_rest.h:805-818 at v19.2.6, :817-830 at v20.2.4).
// Its base is s->info.domain when that is not empty, with no scheme:
// RGWREST::preprocess sets it to the configured name the Host matched, as
// cut from the Host, and otherwise to rgw_dns_name as configured, a list of
// names included (rgw_rest.cc:2163-2165 and :2178-2180 at v19.2.6,
// :2180-2182 and :2200-2202 at v20.2.4). Otherwise it is the scheme
// rgw_transport_is_secure names and HTTP_HOST, beast setting no
// SERVER_NAME, or "<HTTP_HOST>" for a request that sent no Host
// (docs/ceph-upstream-bugs.md, "radosgw's CompleteMultipartUpload Location
// has no scheme under a configured domain"). Then "/<bucket>/<key>", or
// "/<tenant>:<bucket>/<key>" for a tenanted bucket, each name up to its
// first NUL as "%s" reads it and the key not url-encoded.
func completeLocation(r *op.Request) string {
	base := r.Domain
	if base == "" && r.Env.Conf != nil {
		base, _ = r.Env.Conf.String("rgw_dns_name") //nolint:errcheck // an option that cannot be read is radosgw's empty default
	}
	if base == "" {
		host := "<HTTP_HOST>"
		if r.HTTPHost != nil {
			host = *r.HTTPHost
		}
		base = "http://" + host
		if secure(r) {
			base = "https://" + host
		}
	}
	bucket, key := rgwtext.CString(r.Bucket), rgwtext.CString(r.Object.Name)
	if r.Tenant != "" {
		return base + "/" + rgwtext.CString(r.Tenant) + ":" + bucket + "/" + key
	}
	return base + "/" + bucket + "/" + key
}

// abortMultipart is abort_multipart, AbortMultipartUpload: 204 with no body
// (RGWAbortMultipart_ObjStore_S3::send_response, :4110-4119, [T] :4646-4655).
func abortMultipart(ctx context.Context, w http.ResponseWriter, r *op.Request) error {
	if err := op.Run(ctx, &op.AbortMultipart{UploadID: r.Query.Get("uploadId")}, r); err != nil {
		return err
	}
	writeEmpty(w, r, http.StatusNoContent)
	return nil
}

// multipartOwner is dump_owner's section (rgw_rest.cc:506-517 at v19.2.6,
// :511-522 at v20.2.4): the ID, and the display name when it is not empty.
type multipartOwner struct {
	ID          xmltext.Text `xml:"ID"`
	DisplayName xmltext.Text `xml:"DisplayName,omitempty"`
}

// listPartsPart is one Part section of ListPartsResult.
type listPartsPart struct {
	LastModified xmltext.Text `xml:"LastModified"`
	PartNumber   int          `xml:"PartNumber"`
	ETag         xmltext.Text `xml:"ETag"`
	Size         uint64       `xml:"Size"`
}

// listPartsResult is RGWListMultipart_ObjStore_S3's document (:4130-4168, [T]
// :4666-4716), without the Initiator radosgw never writes.
type listPartsResult struct {
	XMLName              xml.Name        `xml:"ListPartsResult"`
	Xmlns                string          `xml:"xmlns,attr"`
	Tenant               xmltext.Text    `xml:"Tenant,omitempty"`
	Bucket               xmltext.Text    `xml:"Bucket"`
	Key                  xmltext.Text    `xml:"Key"`
	UploadID             xmltext.Text    `xml:"UploadId"`
	StorageClass         xmltext.Text    `xml:"StorageClass"`
	PartNumberMarker     int             `xml:"PartNumberMarker"`
	NextPartNumberMarker int             `xml:"NextPartNumberMarker"`
	MaxParts             int             `xml:"MaxParts"`
	IsTruncated          bool            `xml:"IsTruncated"`
	Owner                multipartOwner  `xml:"Owner"`
	Parts                []listPartsPart `xml:"Part"`
}

// listParts is list_multipart, ListParts, GET and HEAD alike. Its parameters
// are RGWListMultipart_ObjStore::get_params' (rgw_rest.cc:1597-1622 at
// v19.2.6, :1602-1627 at v20.2.4), which RGWListMultipart::execute reads
// once the requester is authorized: part-number-marker through strict_strtol,
// a refusal InvalidArgument and a negative number taken; max-parts through
// parse_value_and_bound under rgw_max_listing_results, 1000 when empty. The
// document is framed as send_response frames it (:4121-4171, [T]
// :4657-4719), chunked after end_header with the whole document in its one
// closing flush. NextPartNumberMarker is the highest part number listed, 0
// when none is; LastModified is rgw_to_iso8601's milliseconds on Squid and
// whole seconds on Tentacle (dump_time_exact_seconds, [T] :4704).
func listParts(ctx context.Context, w http.ResponseWriter, r *op.Request) error {
	o := &op.ListParts{UploadID: r.Query.Get("uploadId")}
	var perr error
	if v := r.Query.Get("part-number-marker"); v != "" {
		var err error
		if o.Marker, err = parseInt(rgwtext.CString(v)); err != nil {
			perr = fmt.Errorf("%w: part-number-marker %q: %w", op.ErrInvalidArgument, v, err)
		}
	}
	if perr == nil {
		o.MaxParts, perr = op.ParseValueAndBound(r.Query.Get("max-parts"), 0, op.MaxListingResults(r), defaultMaxParts)
	}
	if err := op.Run(ctx, deferredParams{Op: o, err: perr}, r); err != nil {
		return err
	}
	tentacle := r.Env.Zone.Release() >= denc.Tentacle
	doc := listPartsResult{
		Xmlns: xmlnsS3, Tenant: xmltext.Text(r.Tenant), Bucket: xmltext.Text(r.Bucket), Key: xmltext.Text(r.Object.Name),
		UploadID: xmltext.Text(o.UploadID), StorageClass: xmltext.Text(o.StorageClass),
		PartNumberMarker: o.Marker, MaxParts: o.MaxParts, IsTruncated: o.Result.Truncated,
		Owner: multipartOwner{ID: xmltext.Text(o.Owner.ID), DisplayName: xmltext.Text(o.Owner.DisplayName)},
	}
	for _, p := range o.Result.Parts {
		mtime := p.Mtime
		if tentacle {
			mtime = mtime.Truncate(time.Second)
		}
		doc.NextPartNumberMarker = max(doc.NextPartNumberMarker, p.Number)
		doc.Parts = append(doc.Parts, listPartsPart{
			LastModified: xmltext.Text(ISO8601(mtime)), PartNumber: p.Number, ETag: quotedETag(p.ETag), Size: p.Size,
		})
	}
	writeChunkedXML(ctx, w, r, doc)
	return nil
}

// defaultMaxParts is RGWListMultipart's max_parts before get_params reads
// max-parts (rgw_op.h:1924 at v19.2.6, :2103 at v20.2.4), and
// defaultMaxUploads RGWListBucketMultiparts_ObjStore_S3's default_max
// (rgw_rest_s3.h:505 at v19.2.6, :529 at v20.2.4).
const (
	defaultMaxParts   = 1000
	defaultMaxUploads = 1000
)

// multipartUpload is one Upload section of ListMultipartUploadsResult
// (:4209-4217, [T] :4757-4765): the initiator twice, as Initiator and as
// Owner, and STANDARD whatever the upload's class.
type multipartUpload struct {
	Key          xmltext.Text   `xml:"Key"`
	UploadID     xmltext.Text   `xml:"UploadId"`
	Initiator    multipartOwner `xml:"Initiator"`
	Owner        multipartOwner `xml:"Owner"`
	StorageClass xmltext.Text   `xml:"StorageClass"`
	Initiated    xmltext.Text   `xml:"Initiated"`
}

// multipartPrefixes is the one CommonPrefixes section that holds every
// prefix (:4219-4225, [T] :4767-4773).
type multipartPrefixes struct {
	Prefix []xmltext.Text `xml:"Prefix"`
}

// listMultipartUploadsResult is RGWListBucketMultiparts_ObjStore_S3's
// document (:4186-4227, [T] :4734-4775): the markers, Prefix and Delimiter
// only when not empty, each as sent.
type listMultipartUploadsResult struct {
	XMLName            xml.Name           `xml:"ListMultipartUploadsResult"`
	Xmlns              string             `xml:"xmlns,attr"`
	Tenant             xmltext.Text       `xml:"Tenant,omitempty"`
	Bucket             xmltext.Text       `xml:"Bucket"`
	Prefix             xmltext.Text       `xml:"Prefix,omitempty"`
	KeyMarker          xmltext.Text       `xml:"KeyMarker,omitempty"`
	UploadIDMarker     xmltext.Text       `xml:"UploadIdMarker,omitempty"`
	NextKeyMarker      xmltext.Text       `xml:"NextKeyMarker,omitempty"`
	NextUploadIDMarker xmltext.Text       `xml:"NextUploadIdMarker,omitempty"`
	MaxUploads         int                `xml:"MaxUploads"`
	Delimiter          xmltext.Text       `xml:"Delimiter,omitempty"`
	IsTruncated        bool               `xml:"IsTruncated"`
	Uploads            []multipartUpload  `xml:"Upload"`
	CommonPrefixes     *multipartPrefixes `xml:"CommonPrefixes"`
}

// listMultipartUploads is list_bucket_multiparts, ListMultipartUploads, GET
// and HEAD alike. Its parameters are RGWListBucketMultiparts_ObjStore::get_params'
// (rgw_rest.cc:1624-1660 at v19.2.6, :1629-1665 at v20.2.4), which
// RGWListBucketMultiparts::execute reads once the requester is authorized:
// delimiter and prefix; max-uploads through parse_value_and_bound under
// rgw_max_listing_results, 1000 when empty; encoding-type, present and not
// "url" in any case InvalidArgument with radosgw's message; and the markers,
// the upload id marker kept only beside a key marker, as RGWMPObj takes the
// two. The document is framed as send_response frames it (:4173-4229, [T]
// :4721-4777); under encoding-type=url each upload's key and each common
// prefix is url_encode'd keeping the slash, and no other element is.
// Initiated is rgw_to_iso8601's milliseconds on both releases.
func listMultipartUploads(ctx context.Context, w http.ResponseWriter, r *op.Request) error {
	q := r.Query
	o := &op.ListMultipartUploads{Prefix: q.Get("prefix"), Delimiter: q.Get("delimiter")}
	var perr error
	o.MaxUploads, perr = op.ParseValueAndBound(q.Get("max-uploads"), 0, op.MaxListingResults(r), defaultMaxUploads)
	if v, ok := arg(q, "encoding-type"); perr == nil && ok {
		if strings.EqualFold(rgwtext.CString(v), "url") {
			o.EncodingURL = true
		} else {
			perr = op.ErrInvalidArgument.WithMessage("Invalid Encoding Method specified in Request")
		}
	}
	if o.KeyMarker = q.Get("key-marker"); o.KeyMarker != "" {
		o.UploadIDMarker = q.Get("upload-id-marker")
	}
	if err := op.Run(ctx, deferredParams{Op: o, err: perr}, r); err != nil {
		return err
	}
	res := o.Result
	doc := listMultipartUploadsResult{
		Xmlns: xmlnsS3, Tenant: xmltext.Text(r.Tenant), Bucket: xmltext.Text(r.Bucket), Prefix: xmltext.Text(o.Prefix),
		KeyMarker: xmltext.Text(o.KeyMarker), UploadIDMarker: xmltext.Text(o.UploadIDMarker),
		NextKeyMarker: xmltext.Text(res.NextKeyMarker), NextUploadIDMarker: xmltext.Text(res.NextUploadIDMarker),
		MaxUploads: o.MaxUploads, Delimiter: xmltext.Text(o.Delimiter), IsTruncated: res.Truncated,
	}
	urlsafe := func(s string) xmltext.Text {
		if o.EncodingURL {
			return xmltext.Text(urlEncodeKey(s))
		}
		return xmltext.Text(s)
	}
	for i := range res.Uploads {
		up := &res.Uploads[i]
		owner := multipartOwner{ID: xmltext.Text(up.Owner.String()), DisplayName: xmltext.Text(up.OwnerName)}
		doc.Uploads = append(doc.Uploads, multipartUpload{
			Key: urlsafe(up.Key.Name), UploadID: xmltext.Text(up.ID), Initiator: owner, Owner: owner,
			StorageClass: meta.StorageClassStandard, Initiated: xmltext.Text(ISO8601(up.Initiated)),
		})
	}
	if len(res.CommonPrefixes) > 0 {
		doc.CommonPrefixes = &multipartPrefixes{}
		for _, p := range res.CommonPrefixes {
			doc.CommonPrefixes.Prefix = append(doc.CommonPrefixes.Prefix, urlsafe(p))
		}
	}
	writeChunkedXML(ctx, w, r, doc)
	return nil
}

// urlEncodeKey is op.URLEncode(s, false): url_encode(val, encode_slash=false),
// dump_urlsafe's form for ListMultipartUploads' Key and its common prefixes
// (:4210 and :4222, [T] :4758 and :4770, pass encode_slash=false).
func urlEncodeKey(s string) string { return op.URLEncode(s, false) }
