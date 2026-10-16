package cksum

import (
	"crypto/sha1" //nolint:gosec // SHA1 is an AWS checksum algorithm, not a security boundary
	"crypto/sha256"
	"crypto/sha512"
	"fmt"
	"hash"
	"hash/crc32"
	"hash/crc64"
	"sync"
)

// crc64NVME is the CRC-64/NVME polynomial 0xAD93D23594C93659, bit-reversed
// as hash/crc64 takes a polynomial. hash/crc64 starts the register at all
// ones and inverts the result, as the NVMe CRC does.
const crc64NVME = 0x9a6c9329ac4bc9b5

// crc64NVMETable is built once: crc64.MakeTable builds a new table on every
// call for a polynomial other than ISO and ECMA.
var crc64NVMETable = sync.OnceValue(func() *crc64.Table { return crc64.MakeTable(crc64NVME) })

// NewHash returns the digest radosgw's digest_factory makes for t
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum_digest.h#L97-L128 at v20.2.4):
// CRC32 is IEEE and CRC32C Castagnoli, as its Crc32 and Crc32c are, each
// with all-ones init and xorout
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_crc_digest.h#L41-L121 at v20.2.4).
// Its Sum is the digest as the record stores it: hash/crc32 and hash/crc64
// append a CRC big-endian, as radosgw stores one. It fails for a type that
// is not Writable.
func NewHash(t Type) (hash.Hash, error) {
	if h := newHash(t); h != nil {
		return h, nil
	}
	return nil, fmt.Errorf("computing a %s checksum: rgw-go computes only CRC32, CRC32C, CRC64NVME, SHA1, SHA256 and SHA512", typeName(t))
}

// newHash is NewHash's digest, or nil for a type that is not Writable.
func newHash(t Type) hash.Hash {
	switch t {
	case CRC32:
		return crc32.NewIEEE()
	case CRC32C:
		return crc32.New(crc32.MakeTable(crc32.Castagnoli))
	case CRC64NVME:
		return crc64.New(crc64NVMETable())
	case SHA1:
		return sha1.New() //nolint:gosec // as at the import
	case SHA256:
		return sha256.New()
	case SHA512:
		return sha512.New()
	}
	return nil
}

// Finish is h's digest as a record of type t, with flags stored exactly as
// given. radosgw's put filter does the same: its finalize replaces the
// record's flags, the constructor's V2 included, with the filter's
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum_pipe.h#L187-L191 at v20.2.4),
// which are 0 for a PutObject or a POST object, and for a part its
// upload's, the multipart_cksum_flags
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4471-L4472 at v20.2.4).
func Finish(t Type, h hash.Hash, flags uint16) Cksum {
	return Cksum{Type: t, Digest: h.Sum(nil), Flags: flags}
}

// typeName is t's name, or its id when it has none.
func typeName(t Type) string {
	if name := t.Info().Name; name != "" {
		return name
	}
	return fmt.Sprintf("type %d", t)
}
