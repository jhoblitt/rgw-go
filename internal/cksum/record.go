package cksum

import (
	"encoding/base64"
	"fmt"

	"github.com/jhoblitt/rgw-go/internal/denc"
)

// Cksum is rgw::cksum::Cksum: a type, its digest and the record's flags.
type Cksum struct {
	Type Type
	// Digest is the raw digest, the type's digest size long. A CRC's is
	// big-endian, as radosgw stores it
	// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_crc_digest.h#L41-L121 at v20.2.4).
	Digest []byte
	Flags  uint16
}

// Encode mirrors Cksum::encode, ENCODE_START(2, 1)
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum.h#L258-L266 at v20.2.4).
// It panics on a Cksum that Valid refuses rather than write a record whose
// type or size radosgw's decoder would take unchecked.
func (c Cksum) Encode(e *denc.Encoder, _ denc.Release) {
	if err := c.Valid(); err != nil {
		panic(fmt.Sprintf("cksum: encoding %v", err))
	}
	f := e.BeginStruct(2, 1)
	e.U16(uint16(c.Type))
	e.U16(uint16(len(c.Digest))) //nolint:gosec // Valid holds it to the type's size, at most 64
	e.Raw(c.Digest)
	e.U16(c.Flags)
	e.EndStruct(f)
}

// DecodeCksum mirrors Cksum::decode, DECODE_START(2); a version 1 record has
// no flags and decodes with 0
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum.h#L268-L281 at v20.2.4).
// Unlike radosgw's, it fails with denc.ErrMalformed on a type with no id or a
// digest size other than the type's (docs/ceph-upstream-bugs.md). On any
// failure it returns the zero Cksum.
func DecodeCksum(d *denc.Decoder) Cksum {
	h := d.BeginStruct(2)
	t := Type(d.U16())
	size := int(d.U16())
	if d.Err() != nil {
		return Cksum{}
	}
	if err := check(t, size); err != nil {
		d.Fail(fmt.Errorf("%w: %w", denc.ErrMalformed, err))
		return Cksum{}
	}
	c := Cksum{Type: t, Digest: d.Raw(size)}
	if h.Version >= 2 {
		c.Flags = d.U16()
	}
	d.EndStruct(h)
	if d.Err() != nil {
		return Cksum{}
	}
	return c
}

// Valid refuses what DecodeCksum refuses: a type with no id, or a digest
// whose length is not the type's digest size.
func (c Cksum) Valid() error {
	return check(c.Type, len(c.Digest))
}

func check(t Type, size int) error {
	if t >= numTypes {
		return fmt.Errorf("checksum type %d has no id", t)
	}
	if want := infos[t].DigestSize; size != want {
		return fmt.Errorf("%s digest of %d bytes, not %d", infos[t].Name, size, want)
	}
	return nil
}

// Armor is the checksum as a response carries it, the base64 of the raw
// digest (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum.h#L199-L207 at v20.2.4).
func (c Cksum) Armor() string {
	return base64.StdEncoding.EncodeToString(c.Digest)
}

// Composite reports whether c was combined from its parts' digests rather
// than as a full-object CRC: both COMPOSITE and COMBINED are set
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum.h#L163-L175 at v20.2.4).
func (c Cksum) Composite() bool {
	const mask = FlagComposite | FlagCombined
	return c.Flags&mask == mask
}

const (
	fullObject = "FULL_OBJECT"
	composite  = "COMPOSITE"
)

// ObjectType is get_checksum_type, an object's x-amz-checksum-type:
// FULL_OBJECT for an object without parts, and for a multipart object whose
// record has V2 and is not composite. A multipart object's record without V2
// is COMPOSITE whatever its flags, radosgw taking it for one written before
// CRC combining
// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum.h#L382-L399 at v20.2.4).
func (c Cksum) ObjectType(multipart bool) string {
	if !multipart || c.Flags&FlagV2 != 0 && !c.Composite() {
		return fullObject
	}
	return composite
}

// PartType is main's get_part_checksum_type, a part's x-amz-checksum-type:
// COMPOSITE only when its flags say so
// (https://github.com/ceph/ceph/blob/f6f2529dd9efccf7710c8a2b561e5a2ab9d48bd3/src/rgw/rgw_cksum.h#L417-L424
// at main). A part's record lacks V2, so ObjectType would call it COMPOSITE.
func (c Cksum) PartType() string {
	if c.Flags&FlagComposite != 0 {
		return composite
	}
	return fullObject
}
