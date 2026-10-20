package s3

import (
	"context"
	"errors"
	"net/http"

	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/website"
)

// websiteRoutes is the bucket website configuration routes, rgw_rest_s3.cc's
// RGWGetBucketWebsite_ObjStore_S3, RGWSetBucketWebsite_ObjStore_S3 and
// RGWDeleteBucketWebsite_ObjStore_S3, which Dispatch refuses while
// rgw_enable_static_website is off (websiteConfigDisabled).
func websiteRoutes(*op.Env) []routeSet {
	return []routeSet{{scope: op.ScopeBucket, handlers: map[string]HandlerFunc{
		"get_bucket_website":    getBucketWebsite,
		"set_bucket_website":    putBucketWebsite,
		"delete_bucket_website": deleteBucketWebsite,
	}}}
}

// websiteConfigDisabled reports whether route is a website configuration
// route while rgw_enable_static_website is off. The option is read on every
// request, as radosgw reads its configuration; it is off by default
// (https://github.com/ceph/ceph/blob/v19.2.7/src/common/options/rgw.yaml.in#L2436-L2443 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/common/options/rgw.yaml.in#L2530-L2537 at v20.2.4),
// and off for a request with no environment. op_get, op_put and op_delete
// then find no op for ?website
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4674-L4679,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4732-L4737 and
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4796-L4801 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5186-L5191,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5248-L5253 and
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5311-L5316 at v20.2.4),
// which process_request answers 405 MethodNotAllowed at get_op, before it
// schedules, authenticates or names the bucket
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_process.cc#L325-L329 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_process.cc#L327-L331 at v20.2.4).
func websiteConfigDisabled(r *op.Request, route string) bool {
	switch route {
	case "get_bucket_website", "set_bucket_website", "delete_bucket_website":
		return r.Env == nil || !confBool(r, "rgw_enable_static_website")
	}
	return false
}

// getBucketWebsite is get_bucket_website
// (RGWGetBucketWebsite_ObjStore_S3::send_response,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2365-L2383 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2460-L2478 at v20.2.4):
// the stored configuration as website.MarshalS3XML writes it.
func getBucketWebsite(ctx context.Context, w http.ResponseWriter, r *op.Request) error {
	o := &op.GetBucketWebsite{}
	if err := op.Run(ctx, o, r); err != nil {
		return err
	}
	writeDocument(w, r, website.MarshalS3XML(o.Conf))
	return nil
}

// putBucketWebsite is set_bucket_website:
// RGWSetBucketWebsite_ObjStore_S3::get_params
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2279-L2345 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2374-L2440 at v20.2.4),
// the body read up to rgw_max_put_param_size and parsed by website.ParseXML
// under maxRoutingRules. The op runs it once the request is authorized, as
// RGWSetBucketWebsite::execute calls get_params first
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L2905-L2910 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L3137-L3142 at v20.2.4),
// after verify_permission
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_process.cc#L225-L255 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_process.cc#L225-L258 at v20.2.4),
// so a refused requester costs no read or parse. 200 with no body
// (writeEmptyXML, as send_response ends the header and writes nothing,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2347-L2353 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2442-L2448 at v20.2.4).
func putBucketWebsite(ctx context.Context, w http.ResponseWriter, r *op.Request) error {
	o := &op.SetBucketWebsite{Params: func(_ context.Context, o *op.SetBucketWebsite) error {
		body, err := readParamBody(r, invalidRange)
		if err != nil {
			return err
		}
		conf, err := website.ParseXML(body, maxRoutingRules(r))
		if err != nil {
			return websiteError(err)
		}
		o.Conf = conf
		return nil
	}}
	if err := op.Run(ctx, o, r); err != nil {
		return err
	}
	writeEmptyXML(w, r, http.StatusOK)
	return nil
}

// deleteBucketWebsite is delete_bucket_website: 204 with no body under
// writeEmptyXML's type (RGWDeleteBucketWebsite_ObjStore_S3::send_response,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2355-L2363 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2450-L2458 at v20.2.4).
func deleteBucketWebsite(ctx context.Context, w http.ResponseWriter, r *op.Request) error {
	if err := op.Run(ctx, &op.DeleteBucketWebsite{}, r); err != nil {
		return err
	}
	writeEmptyXML(w, r, http.StatusNoContent)
	return nil
}

// defaultMaxRoutingRules is rgw_website_routing_rules_max_num's default
// (https://github.com/ceph/ceph/blob/v19.2.7/src/common/options/rgw.yaml.in#L46-L53 at v19.2.7 and v20.2.4).
const defaultMaxRoutingRules = 50

// maxRoutingRules is rgw_website_routing_rules_max_num, its default when it
// cannot be read, as get_params reads the 64-bit option into an int
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2327-L2331 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2422-L2426 at v20.2.4),
// keeping only its low 32 bits as a signed int, which rgw-go mirrors so that
// both gateways hold a configuration to the same limit
// (docs/ceph-upstream-bugs.md, "radosgw keeps only the low 32 bits of
// rgw_website_routing_rules_max_num"). website.ParseXML takes a negative
// limit as 50, as get_params does.
func maxRoutingRules(r *op.Request) int {
	if r.Env.Conf == nil {
		return defaultMaxRoutingRules
	}
	v, err := r.Env.Conf.Int64("rgw_website_routing_rules_max_num")
	if err != nil {
		return defaultMaxRoutingRules
	}
	return int(int32(v)) //nolint:gosec // get_params' int keeps the option's low 32 bits
}

// websiteError is the S3 error for a website.ParseXML refusal, with the
// message get_params sets and none of the body's text: MalformedXML
// (docs/exclusions.md, "PutBucketWebsite answers MalformedXML for a document
// that does not parse or fit S3's schema"), InvalidArgument, or 400
// InvalidRequest for too many routing rules,
// -ERR_INVALID_WEBSITE_ROUTING_RULES_ERROR
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L83 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L84 at v20.2.4).
// Any other error comes back unchanged.
func websiteError(err error) error {
	var msg string
	if pe, ok := errors.AsType[*website.ParseError](err); ok {
		msg = pe.Message
	}
	switch {
	case errors.Is(err, website.ErrMalformedXML):
		return op.ErrMalformedXML.WithMessage(msg)
	case errors.Is(err, website.ErrInvalidArgument):
		return op.ErrInvalidArgument.WithMessage(msg)
	case errors.Is(err, website.ErrTooManyRules):
		return op.ErrInvalidWebsiteRoutingRules.WithMessage(msg)
	}
	return err
}
