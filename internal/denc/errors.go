package denc

import "errors"

// Decoder failures wrap one of these; match them with errors.Is.
var (
	ErrShortBuffer  = errors.New("denc: short buffer")
	ErrIncompatible = errors.New("denc: struct compat version newer than decoder")
	ErrOverread     = errors.New("denc: decoded past struct end")
	// ErrMalformed stands in for Ceph's buffer::malformed_input: the encoding
	// is well framed, but a type's decoder found its contents invalid. Type
	// decoders record it with Decoder.Fail.
	ErrMalformed = errors.New("denc: malformed input")
)
