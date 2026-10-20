package s3

import (
	"context"
	"fmt"
	"net/http"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// publicAccessRoutes is the bucket public access block routes:
// rgw_rest_s3.cc's RGWGetBucketPublicAccessBlock_ObjStore_S3 and
// RGWPutBucketPublicAccessBlock_ObjStore_S3, and
// RGWDeleteBucketPublicAccessBlock, which has no S3 subclass. Each is
// authorized by its op alone.
func publicAccessRoutes(*op.Env) []routeSet {
	return []routeSet{{scope: op.ScopeBucket, handlers: map[string]HandlerFunc{
		"get_bucket_public_access_block":    getPublicAccessBlock,
		"put_bucket_public_access_block":    putPublicAccessBlock,
		"delete_bucket_public_access_block": deletePublicAccessBlock,
	}}}
}

// getPublicAccessBlock is get_bucket_public_access_block
// (RGWGetBucketPublicAccessBlock_ObjStore_S3::send_response,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4613-L4624 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5124-L5135 at v20.2.4):
// the stored block after the XML declaration, as application/xml. An error
// is its error document alone. radosgw writes the block whatever op_ret is,
// so once end_header has sent the error document and its Content-Length
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L620-L625 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L625-L630 at v20.2.4),
// a block follows it past that length (docs/exclusions.md, "A
// GetPublicAccessBlock error is its error document alone"). After the 404
// every setting is false. After the 500 for a block that does not decode,
// which radosgw answers whoever verify_permission admits, as its authorizer
// takes such a block for none (get_public_access_conf_from_attr,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L341-L355 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L371-L385 at v20.2.4),
// it holds the settings decoded before the throw, all four stored ones when
// DECODE_FINISH is what throws.
func getPublicAccessBlock(ctx context.Context, w http.ResponseWriter, r *op.Request) error {
	o := &op.GetPublicAccessBlock{}
	if err := op.Run(ctx, o, r); err != nil {
		return err
	}
	writeDocument(w, r, o.Block.MarshalS3XML())
	return nil
}

// putPublicAccessBlock is put_bucket_public_access_block: the op's Params
// is RGWPutBucketPublicAccessBlock::get_params, the body read under
// rgw_max_put_param_size, which execute calls once verify_permission has
// passed
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8626-L8644 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9594-L9612 at v20.2.4),
// then acl.ParsePublicAccessBlockXML, whose every refusal is MalformedXML;
// 200 with no body and no type, as send_response ends the header with
// neither
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4604-L4611 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5115-L5122 at v20.2.4).
func putPublicAccessBlock(ctx context.Context, w http.ResponseWriter, r *op.Request) error {
	o := &op.PutPublicAccessBlock{Params: func(_ context.Context, o *op.PutPublicAccessBlock) error {
		body, err := readParamBody(r, invalidRange)
		if err != nil {
			return err
		}
		if o.Block, err = acl.ParsePublicAccessBlockXML(body); err != nil {
			return fmt.Errorf("%w: %w", op.ErrMalformedXML, err)
		}
		return nil
	}}
	if err := op.Run(ctx, o, r); err != nil {
		return err
	}
	writeEmpty(w, r, http.StatusOK)
	return nil
}

// deletePublicAccessBlock is delete_bucket_public_access_block: 204 with no
// body and no type (RGWDeleteBucketPublicAccessBlock::send_response,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8715-L8725 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9683-L9693 at v20.2.4).
func deletePublicAccessBlock(ctx context.Context, w http.ResponseWriter, r *op.Request) error {
	if err := op.Run(ctx, &op.DeletePublicAccessBlock{}, r); err != nil {
		return err
	}
	writeEmpty(w, r, http.StatusNoContent)
	return nil
}
