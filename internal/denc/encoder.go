package denc

import (
	"encoding/binary"
	"fmt"
	"math"
)

// Encoder appends Ceph-encoded values to a growing buffer. Encoding cannot
// fail; a length that does not fit Ceph's u32 prefix is a programming error
// and panics.
type Encoder struct {
	buf []byte
}

// NewEncoder returns an empty Encoder.
func NewEncoder() *Encoder {
	return &Encoder{}
}

// Bytes returns the encoded bytes. The slice aliases the encoder's buffer
// until the next write.
func (e *Encoder) Bytes() []byte { return e.buf }

// Len returns the number of bytes encoded so far.
func (e *Encoder) Len() int { return len(e.buf) }

// U8 appends one byte.
func (e *Encoder) U8(v uint8) { e.buf = append(e.buf, v) }

// U16 appends a little-endian u16.
func (e *Encoder) U16(v uint16) { e.buf = binary.LittleEndian.AppendUint16(e.buf, v) }

// U32 appends a little-endian u32.
func (e *Encoder) U32(v uint32) { e.buf = binary.LittleEndian.AppendUint32(e.buf, v) }

// U64 appends a little-endian u64.
func (e *Encoder) U64(v uint64) { e.buf = binary.LittleEndian.AppendUint64(e.buf, v) }

// I32 appends a little-endian two's-complement i32.
func (e *Encoder) I32(v int32) { e.U32(uint32(v)) } //nolint:gosec // two's-complement reinterpretation is the wire format

// I64 appends a little-endian two's-complement i64.
func (e *Encoder) I64(v int64) { e.U64(uint64(v)) } //nolint:gosec // two's-complement reinterpretation is the wire format

// Bool appends one byte, 1 for true and 0 for false.
func (e *Encoder) Bool(v bool) {
	if v {
		e.U8(1)
		return
	}
	e.U8(0)
}

// String appends a u32 length then the bytes of s, with no terminator.
func (e *Encoder) String(s string) {
	e.length(len(s))
	e.buf = append(e.buf, s...)
}

// Bytes32 appends a bufferlist: a u32 length then the bytes.
func (e *Encoder) Bytes32(b []byte) {
	e.length(len(b))
	e.buf = append(e.buf, b...)
}

// Raw appends b with no length prefix.
func (e *Encoder) Raw(b []byte) { e.buf = append(e.buf, b...) }

// length appends n as a u32 count or length prefix.
func (e *Encoder) length(n int) {
	e.U32(checkedU32(n))
}

// checkedU32 narrows a Go length to Ceph's u32, panicking when it cannot.
func checkedU32(n int) uint32 {
	if n < 0 || n > math.MaxUint32 {
		panic(fmt.Sprintf("denc: length %d does not fit a u32", n))
	}
	return uint32(n)
}
