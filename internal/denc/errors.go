package denc

import "errors"

// Decoder failures wrap one of these; match them with errors.Is.
var (
	ErrShortBuffer  = errors.New("denc: short buffer")
	ErrIncompatible = errors.New("denc: struct compat version newer than decoder")
	ErrOverread     = errors.New("denc: decoded past struct end")
)
