package s3

import (
	"context"
	"net/http"
	"strconv"

	"github.com/jhoblitt/rgw-go/internal/authz"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/tags"
)

// The bucket policy and tagging routes are RGWGetBucketPolicy,
// RGWPutBucketPolicy and RGWDeleteBucketPolicy, which have no S3 subclass,
// and rgw_rest_s3.cc's RGWGetBucketTags_ObjStore_S3,
// RGWPutBucketTags_ObjStore_S3 and RGWDeleteBucketTags_ObjStore_S3.

// invalidRange is read_all_input's -ERANGE as the policy and tagging PUTs
// pass it on: 416 InvalidRange.
func invalidRange(uint64) error { return op.ErrInvalidRange }

// getBucketPolicy is get_bucket_policy (RGWGetBucketPolicy::send_response,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L8123-L8131 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9054-L9062 at v20.2.4):
// the stored text as application/json, with the length radosgw's frontend
// adds and no Accept-Ranges.
func getBucketPolicy(ctx context.Context, w http.ResponseWriter, r *op.Request) error {
	o := &op.GetBucketPolicy{}
	if err := op.Run(ctx, o, r); err != nil {
		return err
	}
	SetCommonHeaders(w, r)
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Content-Length", strconv.Itoa(len(o.JSON)))
	w.WriteHeader(http.StatusOK)
	// A failed write means the client is gone, with no one left to tell.
	_, _ = w.Write(o.JSON) //nolint:errcheck // see above
	return nil
}

// putBucketPolicy is put_bucket_policy: RGWPutBucketPolicy::get_params
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L8073-L8082 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L8999-L9008 at v20.2.4),
// then the parse and the refusal of a public policy under a block of
// public policies, which the evaluator makes and whose errors are mapped
// through authz.ErrorFor; 204 with no body
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L8047-L8058,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L8965-L8976).
func putBucketPolicy(ctx context.Context, w http.ResponseWriter, r *op.Request) error {
	o := &op.PutBucketPolicy{Params: func(_ context.Context, o *op.PutBucketPolicy) error {
		e, err := evaluator(r)
		if err != nil {
			return err
		}
		body, err := readParamBody(r, invalidRange)
		if err != nil {
			return err
		}
		p, err := e.ParseBucketPolicy(r, body)
		if err != nil {
			return authz.ErrorFor(err)
		}
		o.Policy = p
		return nil
	}}
	if err := op.Run(ctx, o, r); err != nil {
		return err
	}
	writeEmpty(w, r, http.StatusNoContent)
	return nil
}

// deleteBucketPolicy is delete_bucket_policy: 204 with no body
// (RGWDeleteBucketPolicy::send_response,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L8169-L8180 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9108-L9119 at v20.2.4).
func deleteBucketPolicy(ctx context.Context, w http.ResponseWriter, r *op.Request) error {
	if err := op.Run(ctx, &op.DeleteBucketPolicy{}, r); err != nil {
		return err
	}
	writeEmpty(w, r, http.StatusNoContent)
	return nil
}

// getBucketTags is get_bucket_tags
// (RGWGetBucketTags_ObjStore_S3::send_response_data,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L839-L866 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L921-L948 at v20.2.4):
// the tag set, an empty TagSet for an empty one.
func getBucketTags(ctx context.Context, w http.ResponseWriter, r *op.Request) error {
	o := &op.GetBucketTagging{}
	if err := op.Run(ctx, o, r); err != nil {
		return err
	}
	writeDocument(w, r, o.Set.MarshalS3XML())
	return nil
}

// putBucketTags is put_bucket_tags: RGWPutBucketTags_ObjStore_S3::get_params
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L868-L913 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L950-L995 at v20.2.4),
// the body parsed into at most fifty tags, its errors mapped through
// authz.ErrorFor; 200 with no body (writeEmptyXML).
func putBucketTags(ctx context.Context, w http.ResponseWriter, r *op.Request) error {
	o := &op.PutBucketTagging{Params: func(_ context.Context, o *op.PutBucketTagging) error {
		body, err := readParamBody(r, invalidRange)
		if err != nil {
			return err
		}
		set, err := tags.ParseXML(body, tags.MaxBucketTags)
		if err != nil {
			return authz.ErrorFor(err)
		}
		o.Set = set
		return nil
	}}
	if err := op.Run(ctx, o, r); err != nil {
		return err
	}
	writeEmptyXML(w, r, http.StatusOK)
	return nil
}

// deleteBucketTags is delete_bucket_tags: 204 with no body
// (RGWDeleteBucketTags_ObjStore_S3::send_response,
// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L924-L935 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L1006-L1017 at v20.2.4),
// under writeEmptyXML's type.
func deleteBucketTags(ctx context.Context, w http.ResponseWriter, r *op.Request) error {
	if err := op.Run(ctx, &op.DeleteBucketTagging{}, r); err != nil {
		return err
	}
	writeEmptyXML(w, r, http.StatusNoContent)
	return nil
}

// writeEmptyXML is writeEmpty with Content-Type: application/xml: a
// send_response that hands end_header to_mime_type's type and calls
// dump_start after it. end_header flushes the formatter while it is still
// empty, and nothing flushes the XML declaration dump_start then puts in it,
// so radosgw sends the type and no body
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L589-L655 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L594-L660 at v20.2.4).
func writeEmptyXML(w http.ResponseWriter, r *op.Request, status int) {
	w.Header().Set("Content-Type", "application/xml")
	writeEmpty(w, r, status)
}
