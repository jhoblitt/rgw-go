// Package clsutil holds the request encoding and reply decoding that every
// object-class package shares.
package clsutil

import (
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
