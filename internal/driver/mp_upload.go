package driver

import (
	"context"
	"fmt"

	"github.com/jhoblitt/rgw-go/internal/cksum"
	"github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// uploadIDLen is the random part of an upload id: gen_rand_alphanumeric
// given 32 bytes of buffer writes 31 characters and the NUL
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L3278-L3283;
// v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L4122-L4127;
// common/random_string.cc).
const uploadIDLen = 31

// CreateUpload implements op.MultipartStore as RadosMultipartUpload::init
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L3270-L3327;
// v20.2.4
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L4114-L4173):
// a v2 upload id, and the meta object written as write_meta writes a head,
// but never set atomic and with log_op false: in the data-extra pool,
// holding the encoded multipart_upload_info naming p.Placement and p's
// attrs, its MultiMeta index entry of size the info's length and accounted
// size 0 on the shard of the upload's key, and one object and no bytes
// added to the quota cache. The info carries p's retention and legal hold,
// each existing when it is set, as init sets them in the upload_info it
// writes
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_sal_rados.cc#L3314-L3321 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L4156-L4163 at v20.2.4),
// for the completion to write as the object's attrs. On
// Tentacle it carries p.Checksum's type and flags, as RGWInitMultipart::execute
// hands them to init
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7003-L7010 at v20.2.4),
// which sets them in the upload_info it writes
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L4150-L4154 at v20.2.4),
// and a type rgw-go does not compute is InvalidArgument before anything is
// written, so that no caller that skipped the op's parsing stores a type a
// v20.2.4 radosgw would look up past the end of its table. Squid's info has
// no field for either, so a Squid upload has none. init retries a new id on
// EEXIST, which its create(false) never meets, so one attempt is the loop
// (docs/ceph-upstream-bugs.md, "radosgw creates a multipart upload's meta
// object without the exclusive create its flags ask for").
func (s *Store) CreateUpload(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, p op.UploadParams) (*op.Upload, error) {
	var uc op.UploadChecksum
	if s.release >= denc.Tentacle {
		uc = p.Checksum
		if uc.Type != cksum.None && !uc.Type.Writable() {
			return nil, fmt.Errorf("%w: creating an upload of %s/%s: rgw-go computes no checksum of type %d",
				op.ErrInvalidArgument, rec.Info.Bucket.Name, key.Name, uc.Type)
		}
	}
	id := meta.MultipartUploadIDPrefix + s.w.rand(uploadIDLen)
	ref, err := s.metaRef(ctx, rec, key, id)
	if err != nil {
		return nil, err
	}
	data := encodeAt(meta.MultipartUploadInfo{
		DestPlacement: p.Placement, Retention: p.Retention, LegalHold: p.LegalHold, CksumType: uint16(uc.Type), CksumFlags: uc.Flags,
	}, s.release)
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
		Initiated: now, Placement: p.Placement, Attrs: p.Attrs, Checksum: uc,
	}, nil
}

// GetUpload implements op.MultipartStore: one read of the meta object, as
// get_info with the attrs makes it (readMeta), with the checksum type and
// flags the info stores (uploadChecksum).
func (s *Store) GetUpload(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, uploadID string) (*op.Upload, error) {
	ref, err := s.metaRef(ctx, rec, key, uploadID)
	if err != nil {
		return nil, err
	}
	st, err := s.readMeta(ctx, ref)
	if err != nil {
		return nil, err
	}
	up := uploadFromMeta(rec, key, uploadID, st)
	up.Checksum = s.uploadChecksum(st.info)
	return up, nil
}
