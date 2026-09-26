package denc

import (
	"encoding/binary"
	"fmt"
)

// Frame marks an open ENCODE_START block for EndStruct to close.
type Frame struct {
	// start is the offset of the first payload byte, just past the header.
	start int
}

// BeginStruct writes the standard six-byte header, ENCODE_START(version,
// compat), with a placeholder length that EndStruct patches. Encoders always
// emit this form, whichever decoder macro the type uses.
func (e *Encoder) BeginStruct(version, compat uint8) Frame {
	e.U8(version)
	e.U8(compat)
	e.U32(0)
	return Frame{start: e.Len()}
}

// EndStruct patches the length of the struct opened by f to the bytes
// written since.
func (e *Encoder) EndStruct(f Frame) {
	binary.LittleEndian.PutUint32(e.buf[f.start-4:f.start], checkedU32(e.Len()-f.start))
}

// Header describes a struct opened by BeginStruct or BeginStructLegacy.
type Header struct {
	Version uint8
	// Compat is 0 when a legacy encoding carried no compat byte.
	Compat uint8
	// end: the offset where the struct ends, or 0 when the encoding carried no length.
	end int
}

// BeginStruct reads the standard header, DECODE_START(maxVersion). It fails
// with ErrIncompatible when struct_compat exceeds maxVersion and with
// ErrShortBuffer when struct_len exceeds the remaining bytes.
func (d *Decoder) BeginStruct(maxVersion uint8) Header {
	var h Header
	h.Version = d.U8()
	h.Compat = d.U8()
	d.checkCompat(maxVersion, h)
	h.end = d.structEnd()
	if d.err != nil {
		return Header{}
	}
	return h
}

// BeginStructLegacy reads a header written by any version of a type decoded
// with DECODE_START_LEGACY_COMPAT_LEN(maxVersion, compatVersion, lenVersion),
// or its _32 variant when skip is 3. A struct_v below compatVersion carried no
// compat byte, and skip bytes follow it instead; a struct_v below lenVersion
// carried no length, so EndStruct does nothing.
func (d *Decoder) BeginStructLegacy(maxVersion, compatVersion, lenVersion uint8, skip int) Header {
	var h Header
	h.Version = d.U8()
	if compatVersion <= h.Version {
		h.Compat = d.U8()
		d.checkCompat(maxVersion, h)
	} else if skip != 0 {
		d.take(skip)
	}
	if lenVersion <= h.Version {
		h.end = d.structEnd()
	}
	if d.err != nil {
		return Header{}
	}
	return h
}

// EndStruct closes the struct h describes: it skips any fields this decoder
// did not read, and fails with ErrOverread when it read past the struct end.
// A header without a length leaves the offset alone.
func (d *Decoder) EndStruct(h Header) {
	if d.err != nil || h.end == 0 {
		return
	}
	if d.off > h.end {
		d.err = fmt.Errorf("%w: offset %d, struct ends at %d", ErrOverread, d.off, h.end)
		return
	}
	d.off = h.end
}

func (d *Decoder) checkCompat(maxVersion uint8, h Header) {
	if d.err == nil && h.Compat > maxVersion {
		d.err = fmt.Errorf("%w: struct_v %d compat %d, decoder %d", ErrIncompatible, h.Version, h.Compat, maxVersion)
	}
}

// structEnd reads struct_len and returns the offset the payload ends at.
func (d *Decoder) structEnd() int {
	n := int(d.U32())
	if d.err != nil {
		return 0
	}
	if n > d.Remaining() {
		d.err = fmt.Errorf("%w: struct_len %d at offset %d exceeds %d remaining bytes", ErrShortBuffer, n, d.off, d.Remaining())
		return 0
	}
	return d.off + n
}
