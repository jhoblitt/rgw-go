package driver

import (
	"context"

	"github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// uploadIDLen is the random part of an upload id: gen_rand_alphanumeric
// given 32 bytes of buffer writes 31 characters and the NUL
// (rgw_sal_rados.cc:3278-3283; v20.2.4 :4122-4127; common/random_string.cc).
const uploadIDLen = 31

// CreateUpload implements op.MultipartStore as RadosMultipartUpload::init
// (rgw_sal_rados.cc:3270-3327; v20.2.4 :4114-4173): a v2 upload id, and the
// meta object written as write_meta writes a head, but never set atomic and
// with log_op false: in the data-extra pool, holding the encoded
// multipart_upload_info naming p.Placement and p's attrs, its MultiMeta
// index entry of size the info's length and accounted size 0 on the shard
// of the upload's key, and one object and no bytes added to the quota
// cache. The info carries no object lock settings and, on Tentacle, no
// checksum type (docs/exclusions.md, "Client checksums are ignored until
// phase 2"). init retries a new id on EEXIST, which its create(false) never
// meets, so one attempt is the loop (docs/ceph-upstream-bugs.md, "radosgw
// creates a multipart upload's meta object without the exclusive create its
// flags ask for").
func (s *Store) CreateUpload(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, p op.UploadParams) (*op.Upload, error) {
	id := meta.MultipartUploadIDPrefix + s.w.rand(uploadIDLen)
	ref, err := s.metaRef(ctx, rec, key, id)
	if err != nil {
		return nil, err
	}
	data := encodeAt(meta.MultipartUploadInfo{DestPlacement: p.Placement}, s.release)
	now := s.now()
	hw := &headWrite{
		rec: rec, key: ref.key, data: data, attrs: p.Attrs, mtime: now, create: true,
		nonAtomic: true, pool: ref.pool, oid: ref.oid, loc: ref.loc,
		category: rgw.CategoryMultiMeta, size: uint64(len(data)),
	}
	x := s.newIndexOp(rec, ref.key, "")
	x.hashName = key.Name
	x.noLog = true
	if _, err := s.writeMeta(ctx, hw, x); err != nil {
		return nil, err
	}
	return &op.Upload{
		ID: id, Bucket: rec, Key: key, Owner: p.Owner, OwnerName: p.OwnerName,
		Initiated: now, Placement: p.Placement, Attrs: p.Attrs,
	}, nil
}

// GetUpload implements op.MultipartStore: one read of the meta object, as
// get_info with the attrs makes it (readMeta).
func (s *Store) GetUpload(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, uploadID string) (*op.Upload, error) {
	ref, err := s.metaRef(ctx, rec, key, uploadID)
	if err != nil {
		return nil, err
	}
	st, err := s.readMeta(ctx, ref)
	if err != nil {
		return nil, err
	}
	return uploadFromMeta(rec, key, uploadID, st), nil
}
