package rgw

import (
	"github.com/jhoblitt/rgw-go/internal/cls/internal/clsutil"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// MPUploadPartInfoUpdateOp is cls_rgw_mp_upload_part_info_update_op
// (cls/rgw/cls_rgw_ops.h:1457-1480 at v19.2.6, :1495-1518 at v20.2.4), the
// input of mp_upload_part_info_update: the meta object's omap key for a
// part and the part's RGWUploadPartInfo. ceph-dencoder does not register
// it.
type MPUploadPartInfoUpdateOp struct {
	PartKey string
	// Info is the RGWUploadPartInfo as encoded, header included, carried
	// whole as this package does not decode it.
	Info []byte
}

// Encode mirrors cls_rgw_mp_upload_part_info_update_op::encode,
// ENCODE_START(1, 1). The part info is written as given, at whatever
// version it was encoded.
func (o MPUploadPartInfoUpdateOp) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	e.String(o.PartKey)
	e.Raw(o.Info)
	e.EndStruct(f)
}

// DecodeMPUploadPartInfoUpdateOp mirrors
// cls_rgw_mp_upload_part_info_update_op::decode, DECODE_START(1).
func DecodeMPUploadPartInfoUpdateOp(d *denc.Decoder) MPUploadPartInfoUpdateOp {
	h := d.BeginStruct(1)
	var o MPUploadPartInfoUpdateOp
	o.PartKey = d.String()
	o.Info = clsutil.ReadFramed(d)
	d.EndStruct(h)
	return o
}

// MPUploadPartInfoUpdate adds mp_upload_part_info_update, which registers a
// part with its upload (cls/rgw/cls_rgw.cc:4360-4400 at v19.2.6,
// :4762-4802 at v20.2.4). The class reads the part info stored under
// partKey, adds that info's manifest prefix, when its manifest is not
// empty, and its past prefixes to info's past prefixes, and fails with
// EEXIST when info's own manifest prefix is among them; otherwise it stores
// the merged info under partKey, re-encoded at the OSD's release. info is
// the part's RGWUploadPartInfo encoded at r. Both releases register the
// method RD|WR.
func MPUploadPartInfoUpdate(op radosclient.Execer, partKey string, info []byte, r denc.Release) {
	op.Exec(Class, methodMPUploadPartInfoUpdate, clsutil.Encode(MPUploadPartInfoUpdateOp{PartKey: partKey, Info: info}, r))
}
