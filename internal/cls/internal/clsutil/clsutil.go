// Package clsutil holds the request encoding and reply decoding that every
// object-class package shares.
package clsutil

import (
	"encoding/binary"
	"fmt"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// Request is a class method's input, which encodes itself for a release.
type Request interface {
	Encode(e *denc.Encoder, r denc.Release)
}

// Encode returns v encoded for release r.
func Encode(v Request, r denc.Release) []byte {
	e := denc.NewEncoder()
	v.Encode(e, r)
	return e.Bytes()
}

// ReadFramed returns the bytes of one ENCODE_START-framed value at the
// decoder's position, header included, and advances past it: u8 version, u8
// compat, u32 length, then length bytes. It lets a class package carry a
// nested struct it does not decode. It returns nil once the decoder has
// failed.
func ReadFramed(d *denc.Decoder) []byte {
	v := d.U8()
	c := d.U8()
	n := d.U32()
	body := d.Raw(int(n))
	if d.Err() != nil {
		return nil
	}
	out := make([]byte, 0, 6+len(body))
	out = append(out, v, c)
	out = binary.LittleEndian.AppendUint32(out, n)
	return append(out, body...)
}

// DecodeReply decodes the output of class.method with dec once the op has
// run. As the C++ clients do, it ignores bytes past the decoded value. A
// decoding failure reads "<class>: decoding <method> reply: <cause>".
func DecodeReply[T any](res *radosclient.ExecResult, class, method string, dec func(*denc.Decoder) T) (T, error) {
	var zero T
	b, err := res.Bytes()
	if err != nil {
		return zero, err
	}
	d := denc.NewDecoder(b)
	v := dec(d)
	if err := d.Err(); err != nil {
		return zero, fmt.Errorf("%s: decoding %s reply: %w", class, method, err)
	}
	return v, nil
}
