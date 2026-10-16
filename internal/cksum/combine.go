package cksum

import (
	"fmt"
	"hash"
	"hash/crc32"
	"math/bits"
	"sync"
)

// Combiner combines a multipart upload's part checksums into the object's,
// as radosgw's DigestCombiner and CRCCombiner do
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum.cc#L96-L155 at v20.2.4).
type Combiner interface {
	// Append adds the next part's record. partSize is the part's plaintext
	// size: radosgw passes the part's get_size
	// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7138 at v20.2.4),
	// its accounted_size
	// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.h#L828 at v20.2.4).
	// Append refuses a part of another type than the upload's, or one Valid
	// refuses, and leaves the combination as it was.
	Append(part Cksum, partSize uint64) error
	// Final is the object's checksum over the parts appended so far.
	Final() Cksum
}

// NewCombiner returns the combiner for an upload of type t created with
// flags. A CRC upload gets crc(a‖b) from crc(a), crc(b) and len(b), stored
// V2|COMBINED|FULL_OBJECT; one whose flags say COMPOSITE, and any other
// type, get the type's digest over the parts' digests, stored
// V2|COMBINED|COMPOSITE. radosgw's CombinerFactory ignores the flags, and
// its CRCCombiner keeps a single part's own flags, which lack V2
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum.cc#L121-L155 at v20.2.4;
// docs/exclusions.md).
//
// Callers check t.Writable() first: an upload whose stored type rgw-go
// cannot compute answers 501, while a part of another type answers 400, and
// Append's errors do not tell the two apart. For a type that is not
// Writable every Append fails, as a backstop, and Final is the zero Cksum.
func NewCombiner(t Type, flags uint16) Combiner {
	if !t.Writable() {
		return unwritable{typ: t}
	}
	if flags&FlagComposite == 0 {
		switch t {
		case CRC32:
			return &crcCombiner[uint32]{typ: t, ops: crc32Ops()}
		case CRC32C:
			return &crcCombiner[uint32]{typ: t, ops: crc32cOps()}
		case CRC64NVME:
			return &crcCombiner[uint64]{typ: t, ops: crc64NVMEOps()}
		}
	}
	return &compositeCombiner{typ: t, h: newHash(t)}
}

func checkPart(t Type, part Cksum) error {
	if part.Type != t {
		return fmt.Errorf("combining a %s part checksum into a %s one", typeName(part.Type), typeName(t))
	}
	if err := part.Valid(); err != nil {
		return fmt.Errorf("combining a part checksum: %w", err)
	}
	return nil
}

// crcCombiner holds the CRC of the parts appended so far, starting from the
// CRC of no bytes, 0.
type crcCombiner[W crcWord] struct {
	typ Type
	ops *shiftOps[W]
	crc W
}

func (c *crcCombiner[W]) Append(part Cksum, partSize uint64) error {
	if err := checkPart(c.typ, part); err != nil {
		return err
	}
	var crc W
	for _, b := range part.Digest {
		crc = crc<<8 | W(b)
	}
	c.crc = c.ops.combine(c.crc, crc, partSize)
	return nil
}

func (c *crcCombiner[W]) Final() Cksum {
	digest := make([]byte, c.typ.Info().DigestSize)
	crc := c.crc
	for i := len(digest) - 1; i >= 0; i-- {
		digest[i] = byte(crc)
		crc >>= 8
	}
	return Cksum{Type: c.typ, Digest: digest, Flags: FlagV2 | FlagCombined | FlagFullObject}
}

// compositeCombiner digests the parts' digests in their stored order, a
// CRC's big-endian.
type compositeCombiner struct {
	typ Type
	h   hash.Hash
}

func (c *compositeCombiner) Append(part Cksum, _ uint64) error {
	if err := checkPart(c.typ, part); err != nil {
		return err
	}
	c.h.Write(part.Digest)
	return nil
}

func (c *compositeCombiner) Final() Cksum {
	return Finish(c.typ, c.h, FlagV2|FlagCombined|FlagComposite)
}

type unwritable struct{ typ Type }

func (u unwritable) Append(Cksum, uint64) error {
	return fmt.Errorf("combining %s part checksums: rgw-go does not compute the type", typeName(u.typ))
}

func (unwritable) Final() Cksum { return Cksum{} }

// crcWord is a CRC register.
type crcWord interface{ ~uint32 | ~uint64 }

// shiftOps holds the GF(2) operators that run a reflected CRC's register
// over zero bytes: ops[k] runs it over 2^k of them. An operator has one
// row per register bit, the register that bit alone becomes.
type shiftOps[W crcWord] [64][]W

var (
	crc32Ops     = sync.OnceValue(func() *shiftOps[uint32] { return newShiftOps[uint32](crc32.IEEE) })
	crc32cOps    = sync.OnceValue(func() *shiftOps[uint32] { return newShiftOps[uint32](crc32.Castagnoli) })
	crc64NVMEOps = sync.OnceValue(func() *shiftOps[uint64] { return newShiftOps[uint64](crc64NVME) })
)

// newShiftOps builds poly's operators. One zero bit shifts the register
// right, XORing poly in when the bit shifted out was set; squaring an
// operator doubles the zeros it runs over.
func newShiftOps[W crcWord](poly W) *shiftOps[W] {
	width := bits.Len64(uint64(^W(0)))
	op := make([]W, width)
	op[0] = poly
	for i := 1; i < width; i++ {
		op[i] = 1 << (i - 1)
	}
	for range 3 {
		op = square(op)
	}
	ops := new(shiftOps[W])
	for k := range ops {
		ops[k] = op
		op = square(op)
	}
	return ops
}

// combine is crc(a‖b): crc(a) run over len(b) zero bytes, XOR crc(b). That
// holds for a CRC whose init and xorout are equal, as all three are.
func (ops *shiftOps[W]) combine(crcA, crcB W, lenB uint64) W {
	for ; lenB != 0; lenB &= lenB - 1 {
		crcA = apply(ops[bits.TrailingZeros64(lenB)], crcA)
	}
	return crcA ^ crcB
}

// apply is op applied to v, the XOR of op's rows for v's set bits.
func apply[W crcWord](op []W, v W) W {
	var r W
	for ; v != 0; v &= v - 1 {
		r ^= op[bits.TrailingZeros64(uint64(v))]
	}
	return r
}

func square[W crcWord](op []W) []W {
	sq := make([]W, len(op))
	for i, row := range op {
		sq[i] = apply(op, row)
	}
	return sq
}
