package driver

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/jhoblitt/rgw-go/internal/cksum"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// uploadChecksum is the checksum type and flags of the upload whose info is
// info, as get_info reads them into the upload
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L4573-L4574 at v20.2.4).
// On Squid it is zero: Squid's multipart_upload_info has neither field, and
// its radosgw decodes none from an info that carries them
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.h#L1508-L1552 at v19.2.7).
func (s *Store) uploadChecksum(info meta.MultipartUploadInfo) op.UploadChecksum {
	if s.release < denc.Tentacle {
		return op.UploadChecksum{}
	}
	return op.UploadChecksum{Type: cksum.Type(info.CksumType), Flags: info.CksumFlags}
}

// storedPartChecksum is the checksum part's record holds, nil for none, as
// Tentacle's RGWUploadPartInfo carries it: the optional's presence byte, then
// the record. A record cksum.DecodeCksum refuses is an error.
func storedPartChecksum(part meta.UploadPartInfo) (*cksum.Cksum, error) {
	if len(part.Cksum) == 0 {
		return nil, nil
	}
	c, err := decodeAttr(part.Cksum[1:], cksum.DecodeCksum)
	if err != nil {
		return nil, fmt.Errorf("decoding part %d's checksum: %w", part.Num, err)
	}
	return &c, nil
}

// listedPartChecksum is storedPartChecksum for ListParts, which shows a
// record it cannot read as no checksum, logged at debug level, as
// storedChecksum shows an object's. On Squid it is nil, as uploadChecksum is
// zero: Squid's RGWUploadPartInfo decodes no checksum from a version 6 record
// a Tentacle radosgw wrote
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_basic_types.h#L279-L296 at v19.2.7).
func (s *Store) listedPartChecksum(ctx context.Context, part meta.UploadPartInfo) *cksum.Cksum {
	if s.release < denc.Tentacle {
		return nil
	}
	c, err := storedPartChecksum(part)
	if err != nil {
		slog.DebugContext(ctx, "the part's stored checksum does not decode; the part shows none", slog.Any("error", err))
		return nil
	}
	return c
}

// combineChecksums is try_sum_part_cksums
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7043-L7161 at v20.2.4)
// and the comparison with the value find_hdr_cksum finds that follows it
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7295-L7341 at v20.2.4)
// for an upload of type uc, once its completion's parts are matched: the
// checksum of named, the stored records the request's parts matched in
// number order, through cksum.NewCombiner, each part counting its
// accounted size, as radosgw's get_size gives it; nil for an upload
// without a type. radosgw instead combines the last page list_parts
// returns for the request's part count, before the parts are matched
// (docs/ceph-upstream-bugs.md, "radosgw's CompleteMultipartUpload combines
// the checksums of the parts it lists, not of those it completes").
//
// A type rgw-go does not compute is ErrNotImplemented; a named part without
// a checksum, or with one of another type or one that does not decode,
// ErrInvalidRequest. A value supplied must be the combination's armor, with
// "-<parts>" when it is composite, or a composite's armor alone, which
// radosgw accepts for minio's clients, and of the upload's type; any other,
// the empty value included, is ErrBadDigest. radosgw compares only a
// non-empty value, of any type but crc64nvme, and accepts one that starts
// with the armor (docs/exclusions.md, "CompleteMultipartUpload combines the
// parts it completes and compares the whole supplied checksum").
func combineChecksums(uc op.UploadChecksum, named []meta.UploadPartInfo, supplied *op.ChecksumRequest) (*cksum.Cksum, error) {
	if uc.Type == cksum.None {
		return nil, nil
	}
	if err := writableUpload(uc); err != nil {
		return nil, err
	}
	c := cksum.NewCombiner(uc.Type, uc.Flags)
	for i := range named {
		p := &named[i]
		part, err := storedPartChecksum(*p)
		switch {
		case err != nil:
			return nil, fmt.Errorf("%w: %w", op.ErrInvalidRequest, err)
		case part == nil:
			return nil, fmt.Errorf("%w: part %d of a %s upload has no checksum", op.ErrInvalidRequest, p.Num, uc.Type.Info().Name)
		}
		if err := c.Append(*part, p.AccountedSize); err != nil {
			return nil, fmt.Errorf("%w: part %d: %w", op.ErrInvalidRequest, p.Num, err)
		}
	}
	sum := c.Final()
	if err := compareSupplied(sum, len(named), supplied); err != nil {
		return nil, err
	}
	return &sum, nil
}

// writableUpload refuses with ErrNotImplemented an upload whose checksum type
// rgw-go does not compute, xxh3 or blake3 from a Tentacle radosgw or a type a
// newer radosgw added, as the op layer refuses its parts.
func writableUpload(uc op.UploadChecksum) error {
	if uc.Type != cksum.None && !uc.Type.Writable() {
		return fmt.Errorf("%w: the upload's checksum type %d is not one rgw-go computes", op.ErrNotImplemented, uc.Type)
	}
	return nil
}

// compareSupplied holds the value req supplies to sum, the combination of n
// parts, as combineChecksums says. Nothing is compared when req supplies no
// value.
func compareSupplied(sum cksum.Cksum, n int, req *op.ChecksumRequest) error {
	if req == nil || req.Expected == nil {
		return nil
	}
	got, ok := req.Expected()
	if !ok {
		return nil
	}
	armor := sum.Armor()
	want := armor
	if sum.Composite() {
		want += "-" + strconv.Itoa(n)
	}
	if req.Type == sum.Type && (got == want || sum.Composite() && got == armor) {
		return nil
	}
	return fmt.Errorf("%w: the supplied %s checksum %q is not the combined %s checksum %s",
		op.ErrBadDigest, req.Type.Info().Name, got, sum.Type.Info().Name, want)
}
