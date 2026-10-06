package driver

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// The multipart listings are RadosMultipartUpload::list_parts and
// RadosBucket::list_multiparts (driver/rados/rgw_sal_rados.cc:3329-3431 and
// :915-956 at v19.2.6, :4175-4277 and :935-976 at v20.2.4, the same code).

// omapGetAllPage is MAX_OMAP_GET_ENTRIES, the page RGWSI_SysObj_Core::
// omap_get_all reads an omap by (services/svc_sys_obj_core.cc at v19.2.6 and
// v20.2.4).
const omapGetAllPage = 1024

// metaOmap reads the meta object's omap after startAfter as the sysobj omap
// calls do: omap_get_vals for count entries, asking again while the OSD has
// more and count is not met, or, with all, omap_get_all, by omapGetAllPage
// until the OSD has no more. A missing meta object is NoSuchUpload.
func (s *Store) metaOmap(ctx context.Context, ref mpRef, startAfter string, count uint64, all bool) (map[string][]byte, error) {
	if all {
		count = omapGetAllPage
	}
	out := map[string][]byte{}
	for count > 0 {
		rop := radosclient.NewReadOp()
		res := rop.OmapGetVals(startAfter, "", count)
		if _, err := ref.pool.Read(ctx, ref.oid, rop, radosclient.OpFlagNone); err != nil {
			return nil, op.FromRADOS(err, op.ScopeUpload)
		}
		if res.Err != nil {
			return nil, op.FromRADOS(res.Err, op.ScopeUpload)
		}
		if len(res.Values) == 0 {
			break
		}
		maps.Copy(out, res.Values)
		startAfter = slices.Max(slices.Collect(maps.Keys(res.Values)))
		if !all {
			count -= uint64(len(res.Values))
		}
		if !res.More {
			break
		}
	}
	return out, nil
}

// decodePartInfo decodes the part info stored under omap key k; one that
// does not decode is list_parts' -EIO (:3378-3384), which radosgw's error
// table lacks, so UnknownError.
func decodePartInfo(k string, b []byte) (meta.UploadPartInfo, error) {
	d := denc.NewDecoder(b)
	info := meta.DecodeUploadPartInfo(d)
	if err := d.Err(); err != nil {
		return meta.UploadPartInfo{}, fmt.Errorf("%w: decoding the part info under %q: %w", op.ErrUnknown, k, err)
	}
	return info, nil
}

// lastPartNumber is the NextPartNumberMarker ListParts renders: cur_max, the
// highest number of the page as an int, 0 for an empty page
// (rgw_rest_s3.cc:4135-4149 at v19.2.6, :4671-4685 at v20.2.4). execute
// passes list_parts no next_marker (rgw_op.cc:6693 at v19.2.6, :7632 at
// v20.2.4), whose last_num is not always the page's.
func lastPartNumber(parts []meta.UploadPartInfo) int {
	if len(parts) == 0 {
		return 0
	}
	return int(int32(parts[len(parts)-1].Num)) //nolint:gosec // cur_max is an int
}

// listPartsPage is list_parts for the meta object ref of upload uploadID.
// marker is an int, as get_params' strict_strtol leaves it. For a v2 upload
// id the omap is read after "part.%08d" of marker for maxParts+1 values, and
// the first maxParts, decoded, must be numbered from marker+1 on; the extra
// one only marks the page truncated. A number out of that sequence, a part
// missing or a key a gateway without sorted keys wrote, sends it to the
// unsorted path, as for any other id: every value is read and decoded, those
// whose number as an int is above marker kept by number, a later key's
// replacing an earlier one's of the same number as the parts map does, and
// the first maxParts of them returned. next is lastPartNumber of the page.
func (s *Store) listPartsPage(ctx context.Context, ref mpRef, uploadID string, marker, maxParts int) (parts []meta.UploadPartInfo, next int, truncated bool, err error) {
	limit := max(maxParts, 0)
	if meta.IsV2UploadID(uploadID) {
		var inOrder bool
		parts, truncated, inOrder, err = s.sortedPartsPage(ctx, ref, marker, limit)
		if err != nil || inOrder {
			return parts, lastPartNumber(parts), truncated, err
		}
	}
	vals, err := s.metaOmap(ctx, ref, "", 0, true)
	if err != nil {
		return nil, 0, false, err
	}
	byNum := map[uint32]meta.UploadPartInfo{}
	for _, k := range slices.Sorted(maps.Keys(vals)) {
		info, derr := decodePartInfo(k, vals[k])
		if derr != nil {
			return nil, 0, false, derr
		}
		if int(int32(info.Num)) > marker { //nolint:gosec // radosgw compares (int)num
			byNum[info.Num] = info
		}
	}
	parts = slices.SortedFunc(maps.Values(byNum), func(a, b meta.UploadPartInfo) int { return cmp.Compare(a.Num, b.Num) })
	truncated = len(parts) > limit
	parts = parts[:min(limit, len(parts))]
	return parts, lastPartNumber(parts), truncated, nil
}

// sortedPartsPage is list_parts' sorted attempt (:3350-3359, :3372-3407):
// inOrder is false when a value of the page is not the number expected.
func (s *Store) sortedPartsPage(ctx context.Context, ref mpRef, marker, limit int) (parts []meta.UploadPartInfo, truncated, inOrder bool, err error) {
	vals, err := s.metaOmap(ctx, ref, fmt.Sprintf("part.%08d", int32(marker)), uint64(limit)+1, false) //nolint:gosec // marker is an int
	if err != nil {
		return nil, false, false, err
	}
	keys := slices.Sorted(maps.Keys(vals))
	expected := uint32(int32(marker) + 1) //nolint:gosec // expected_next is a uint32 of marker + 1
	for _, k := range keys[:min(limit, len(keys))] {
		info, derr := decodePartInfo(k, vals[k])
		if derr != nil {
			return nil, false, false, derr
		}
		if info.Num != expected {
			return nil, false, false, nil
		}
		expected++
		parts = append(parts, info)
	}
	return parts, len(keys) > limit, true, nil
}

// ListParts implements op.MultipartStore as RGWListMultipart::execute lists
// a page of parts (rgw_op.cc:6669-6694 at v19.2.6, :7593-7633 at v20.2.4),
// without its get_info: the op read the meta object already for the policy
// and the placement, so only the omap is read here (listPartsPage), and a
// meta object gone since is NoSuchUpload. A part's Size is its accounted
// size, RadosMultipartPart::get_size, and its Mtime the time the part info
// records.
func (s *Store) ListParts(ctx context.Context, up *op.Upload, marker, maxParts int) (op.ListPartsResult, error) {
	ref, err := s.metaRef(ctx, up.Bucket, up.Key, up.ID)
	if err != nil {
		return op.ListPartsResult{}, err
	}
	infos, next, truncated, err := s.listPartsPage(ctx, ref, up.ID, marker, maxParts)
	if err != nil {
		return op.ListPartsResult{}, err
	}
	res := op.ListPartsResult{Parts: make([]op.Part, 0, len(infos)), NextMarker: next, Truncated: truncated}
	for i := range infos {
		p := &infos[i]
		res.Parts = append(res.Parts, op.Part{Number: int(p.Num), ETag: p.ETag, Size: p.AccountedSize, Mtime: p.Modified})
	}
	return res, nil
}

// ListUploads implements op.MultipartStore as list_multiparts lists uploads:
// the bucket listing op.UploadListing names, as op.UploadsFromListing pages
// it.
func (s *Store) ListUploads(ctx context.Context, rec *op.BucketRecord, p op.ListUploadsParams) (op.ListUploadsResult, error) {
	lr, err := s.ListObjects(ctx, rec, op.UploadListing(p))
	if err != nil {
		return op.ListUploadsResult{}, err
	}
	return op.UploadsFromListing(rec, lr), nil
}
