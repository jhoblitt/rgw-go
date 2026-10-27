package s3

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

	"github.com/jhoblitt/rgw-go/internal/cors"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// corsRoutes is the bucket CORS routes, rgw_rest_s3.cc's
// RGWGetCORS_ObjStore_S3, RGWPutCORS_ObjStore_S3 and
// RGWDeleteCORS_ObjStore_S3, each authorized by its op alone, and the
// preflight, RGWOptionsCORS_ObjStore_S3, which op_options returns at bucket
// and object scope
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4823-L4826 and
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4908-L4911 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5348-L5351 and
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5438-L5441 at v20.2.4).
// Each op takes the request's x-amz-storage-class, which its op checks
// against the bucket's placement.
func corsRoutes(*op.Env) []routeSet {
	return []routeSet{
		{scope: op.ScopeBucket, handlers: map[string]HandlerFunc{
			"get_cors":         getBucketCORS,
			"put_cors":         putBucketCORS,
			"delete_cors":      deleteBucketCORS,
			op.OptionsCORSName: optionsCORS,
		}},
		{scope: op.ScopeObject, handlers: map[string]HandlerFunc{op.OptionsCORSName: optionsCORS}},
	}
}

// getBucketCORS is get_cors (RGWGetCORS_ObjStore_S3::send_response,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3749-L3770 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4200-L4221 at v20.2.4):
// the stored configuration as application/xml, with the length radosgw's
// frontend adds and no Accept-Ranges. radosgw sends no XML declaration
// before it: the one dump_start puts in the formatter after end_header has
// flushed it is never flushed, as dump_body writes the document past the
// formatter. An error is its error document alone.
func getBucketCORS(ctx context.Context, w http.ResponseWriter, r *op.Request) error {
	o := &op.GetBucketCORS{StorageClass: header(r, "X-Amz-Storage-Class")}
	if err := op.Run(ctx, o, r); err != nil {
		return err
	}
	doc := o.Config.MarshalS3XML()
	SetCommonHeaders(w, r)
	h := w.Header()
	h.Set("Content-Type", "application/xml")
	h.Set("Content-Length", strconv.Itoa(len(doc)))
	w.WriteHeader(http.StatusOK)
	// A failed write means the client is gone, with no one left to tell.
	_, _ = w.Write(doc) //nolint:errcheck // see above
	return nil
}

// putBucketCORS is put_cors: the op's Params is
// RGWPutCORS_ObjStore_S3::get_params
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3772-L3832 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4223-L4283 at v20.2.4),
// which RGWPutCORS::execute calls once verify_permission has passed: the
// body read under rgw_max_put_param_size, parsed by cors.ParseXML, whose
// every refusal is MalformedXML, and held to maxCORSRules. 200 with no body
// under writeEmptyXML's type, as send_response ends the header with
// to_mime_type's type and then calls dump_start
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3834-L3841 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4285-L4292 at v20.2.4).
func putBucketCORS(ctx context.Context, w http.ResponseWriter, r *op.Request) error {
	o := &op.PutBucketCORS{
		StorageClass: header(r, "X-Amz-Storage-Class"),
		Params: func(_ context.Context, o *op.PutBucketCORS) error {
			body, err := readParamBody(r, invalidRange)
			if err != nil {
				return err
			}
			c, err := cors.ParseXML(body)
			if err != nil {
				return fmt.Errorf("%w: %w", op.ErrMalformedXML, err)
			}
			if limit := maxCORSRules(r); len(c.Rules) > limit {
				return op.ErrInvalidCORSRules.WithMessage(
					"The number of CORS rules should not exceed allowed limit of " + strconv.Itoa(limit) + " rules.")
			}
			o.Config = c
			return nil
		},
	}
	if err := op.Run(ctx, o, r); err != nil {
		return err
	}
	writeEmptyXML(w, r, http.StatusOK)
	return nil
}

// deleteBucketCORS is delete_cors: 204 with no body and no type, for a
// bucket with a configuration and one without alike
// (RGWDeleteCORS_ObjStore_S3::send_response,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3843-L3852 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4294-L4303 at v20.2.4).
func deleteBucketCORS(ctx context.Context, w http.ResponseWriter, r *op.Request) error {
	if err := op.Run(ctx, &op.DeleteBucketCORS{StorageClass: header(r, "X-Amz-Storage-Class")}, r); err != nil {
		return err
	}
	writeEmpty(w, r, http.StatusNoContent)
	return nil
}

// optionsCORS is options_cors, the preflight: 200 with no body and no
// type, carrying the Access-Control headers SetPreflightHeaders renders
// from the op's results alone, else the op's error
// (RGWOptionsCORS_ObjStore_S3::send_response,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3854-L3875 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4305-L4326 at v20.2.4).
// A signed preflight's identity is its key's user on the key's id alone, so
// nothing here reads r.Identity: the op reads none of it, and
// SetCommonHeaders adds no x-amz-request-charged to an OPTIONS response
// (docs/exclusions.md, "A preflight's answer says nothing of its key's
// user"). At object scope the op reads the bucket alone, as radosgw's
// read_permissions does for OP_OPTIONS
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L1930-L1932 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L1929-L1931 at v20.2.4).
func optionsCORS(ctx context.Context, w http.ResponseWriter, r *op.Request) error {
	o := &op.OptionsCORS{StorageClass: header(r, "X-Amz-Storage-Class")}
	if err := op.Run(ctx, o, r); err != nil {
		return err
	}
	SetPreflightHeaders(w, o)
	writeEmpty(w, r, http.StatusOK)
	return nil
}

// defaultMaxCORSRules is rgw_cors_rules_max_num's default
// (https://github.com/ceph/ceph/blob/v19.2.7/src/common/options/rgw.yaml.in#L26-L30 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/common/options/rgw.yaml.in#L26-L30 at v20.2.4),
// which get_params also takes for a negative one, as CORS_RULES_MAX_NUM.
const defaultMaxCORSRules = 100

// maxCORSRules is rgw_cors_rules_max_num as get_params reads it, its
// default when it cannot be read: the 64-bit option assigned to an int,
// which keeps only its low 32 bits as a signed int, and a negative one
// taken as 100
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3801-L3805 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4252-L4256 at v20.2.4).
// rgw-go mirrors the narrowing, so that both gateways hold a configuration
// to the same limit (docs/ceph-upstream-bugs.md, "radosgw keeps only the
// low 32 bits of rgw_cors_rules_max_num").
func maxCORSRules(r *op.Request) int {
	if r.Env.Conf == nil {
		return defaultMaxCORSRules
	}
	v, err := r.Env.Conf.Int64("rgw_cors_rules_max_num")
	if err != nil {
		return defaultMaxCORSRules
	}
	if n := int(int32(v)); n >= 0 { //nolint:gosec // get_params' int keeps the option's low 32 bits
		return n
	}
	return defaultMaxCORSRules
}
