package s3

import (
	"context"
	"net/http"

	"github.com/jhoblitt/rgw-go/internal/objectlock"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// putObjectRetention is put_obj_retention: x-amz-bypass-governance-retention
// read as get_params reads it (op.BypassGovernanceHeader), the op, which
// reads the body, then 200 with no body and no type, as send_response ends
// the header with neither
// (RGWPutObjRetention_ObjStore_S3::send_response,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4534-L4541 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5045-L5052 at v20.2.4).
func putObjectRetention(ctx context.Context, w http.ResponseWriter, r *op.Request) error {
	v, _ := op.HeaderValue(r.Header, "X-Amz-Bypass-Governance-Retention")
	if err := op.Run(ctx, &op.PutObjectRetention{BypassHeader: op.BypassGovernanceHeader(v)}, r); err != nil {
		return err
	}
	writeEmpty(w, r, http.StatusOK)
	return nil
}

// getObjectRetention is get_obj_retention: the version's retention, without
// S3's namespace, after the XML declaration dump_start writes, as
// application/xml; an error is its error document alone
// (RGWGetObjRetention_ObjStore_S3::send_response,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4543-L4557 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5054-L5068 at v20.2.4).
func getObjectRetention(ctx context.Context, w http.ResponseWriter, r *op.Request) error {
	o := &op.GetObjectRetention{}
	if err := op.Run(ctx, o, r); err != nil {
		return err
	}
	writeDocument(w, r, objectlock.MarshalRetention(o.Retention))
	return nil
}

// putObjectLegalHold is put_obj_legal_hold, whose op reads the body itself:
// 200 with no body and no type
// (RGWPutObjLegalHold_ObjStore_S3::send_response,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4559-L4566 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5070-L5077 at v20.2.4).
func putObjectLegalHold(ctx context.Context, w http.ResponseWriter, r *op.Request) error {
	if err := op.Run(ctx, &op.PutObjectLegalHold{}, r); err != nil {
		return err
	}
	writeEmpty(w, r, http.StatusOK)
	return nil
}

// getObjectLegalHold is get_obj_legal_hold, getObjectRetention's with the
// legal hold
// (RGWGetObjLegalHold_ObjStore_S3::send_response,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4568-L4582 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5079-L5093 at v20.2.4).
func getObjectLegalHold(ctx context.Context, w http.ResponseWriter, r *op.Request) error {
	o := &op.GetObjectLegalHold{}
	if err := op.Run(ctx, o, r); err != nil {
		return err
	}
	writeDocument(w, r, objectlock.MarshalLegalHold(o.LegalHold))
	return nil
}
