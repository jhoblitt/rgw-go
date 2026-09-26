package denc

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

// Decoder reads Ceph-encoded values. It records the first failure and returns zero
// values from every later call; callers check Err once at the end.
type Decoder struct {
	buf []byte
	off int
	err error
}

// NewDecoder returns a Decoder reading b from its start. The decoder never
// modifies b, and every slice it returns is a copy.
func NewDecoder(b []byte) *Decoder {
	return &Decoder{buf: b}
}

// Err returns the first failure recorded, or nil.
func (d *Decoder) Err() error { return d.err }

// Remaining returns the number of bytes not yet consumed.
func (d *Decoder) Remaining() int { return len(d.buf) - d.off }

// Offset returns the number of bytes consumed so far.
func (d *Decoder) Offset() int { return d.off }

// Fail records an error raised by a caller, e.g. a bad enum value. The first
// recorded error wins, whether it came from Fail or from the decoder itself;
// a nil err is ignored.
func (d *Decoder) Fail(err error) {
	if d.err == nil && err != nil {
		d.err = err
	}
}

// take consumes n bytes and returns them without copying, or records
// ErrShortBuffer and returns nil.
func (d *Decoder) take(n int) []byte {
	if d.err != nil {
		return nil
	}
	if n < 0 || n > d.Remaining() {
		d.err = fmt.Errorf("%w: need %d bytes at offset %d", ErrShortBuffer, n, d.off)
		return nil
	}
	b := d.buf[d.off : d.off+n]
	d.off += n
	return b
}

// U8 reads one byte.
func (d *Decoder) U8() uint8 {
	b := d.take(1)
	if b == nil {
		return 0
	}
	return b[0]
}

// U16 reads a little-endian u16.
func (d *Decoder) U16() uint16 {
	b := d.take(2)
	if b == nil {
		return 0
	}
	return binary.LittleEndian.Uint16(b)
}

// U32 reads a little-endian u32.
func (d *Decoder) U32() uint32 {
	b := d.take(4)
	if b == nil {
		return 0
	}
	return binary.LittleEndian.Uint32(b)
}

// U64 reads a little-endian u64.
func (d *Decoder) U64() uint64 {
	b := d.take(8)
	if b == nil {
		return 0
	}
	return binary.LittleEndian.Uint64(b)
}

// I32 reads a little-endian two's-complement i32.
func (d *Decoder) I32() int32 { return int32(d.U32()) } //nolint:gosec // two's-complement reinterpretation is the wire format

// I64 reads a little-endian two's-complement i64.
func (d *Decoder) I64() int64 { return int64(d.U64()) } //nolint:gosec // two's-complement reinterpretation is the wire format

// Bool reads one byte; any non-zero value is true.
func (d *Decoder) Bool() bool { return d.U8() != 0 }

// String reads a u32 length then that many bytes.
func (d *Decoder) String() string {
	return string(d.lengthPrefixed())
}

// Bytes32 reads a bufferlist: a u32 length then that many bytes, returned as
// a copy. An empty bufferlist decodes to nil.
func (d *Decoder) Bytes32() []byte {
	return clone(d.lengthPrefixed())
}

// Raw reads n bytes with no length prefix, returned as a copy. Raw(0)
// returns nil.
func (d *Decoder) Raw(n int) []byte {
	return clone(d.take(n))
}

// lengthPrefixed reads a u32 length and returns that many bytes uncopied.
func (d *Decoder) lengthPrefixed() []byte {
	n := d.U32()
	if d.err != nil {
		return nil
	}
	return d.take(int(n))
}

// clone copies b, mapping an empty slice to nil so that decoded values
// compare equal to Go zero values.
func clone(b []byte) []byte {
	if len(b) == 0 {
		return nil
	}
	return bytes.Clone(b)
}
