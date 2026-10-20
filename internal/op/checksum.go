package op

import "github.com/jhoblitt/rgw-go/internal/cksum"

// ChecksumRequest is the checksum a write computes over its plaintext body,
// as radosgw's RGWPutObj_Cksum filter computes it
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum_pipe.h#L157-L215 at v20.2.4).
type ChecksumRequest struct {
	Type cksum.Type
	// Flags are the stored record's, exactly as given, since finalize
	// replaces the digest's flags with the filter's
	// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum_pipe.h#L187-L191 at v20.2.4):
	// 0 for an object, the upload's for a part.
	Flags uint16
	// Expected returns the armored value the client supplied, or false when
	// it supplied none and the computed checksum is stored unverified; nil
	// supplies none. The store calls it once the body is read, as radosgw
	// reads the value from the request's environment after the body
	// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4757-L4790 at v20.2.4),
	// where an aws-chunked trailer has put it by then
	// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L1459-L1488 at v20.2.4).
	Expected func() (string, bool)
}

// UploadChecksum is a multipart upload's checksum type and flags, the
// cksum_type and cksum_flags of Tentacle's multipart_upload_info
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.h#L1552-L1609 at v20.2.4).
// Squid's has neither
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.h#L1508-L1552 at v19.2.7),
// so a Squid upload's is zero, whatever CreateUpload was given.
type UploadChecksum struct {
	Type  cksum.Type
	Flags uint16
}
