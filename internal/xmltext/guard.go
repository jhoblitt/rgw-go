package xmltext

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
)

// ErrStartTagTooLong is a document with a start tag of more than MaxStartTag
// bytes, refused as the decoder reads the tag, before it builds the tag's
// attributes.
var ErrStartTagTooLong = errors.New("xmltext: start tag too long")

// tagGuard serves a document to encoding/xml's decoder a byte at a time and
// fails the read that takes a start tag past MaxStartTag bytes. The decoder
// returns a start tag only once it holds every attribute of it, 48 bytes
// each in a slice it grows by appending, so a check made on the returned tag
// has paid for a tag of the whole body. A token is a start tag when its "<"
// is followed by anything but "/", "?" or "!", which makes it an end tag, a
// processing instruction, a comment, CDATA or a declaration. The decoder
// decides where a token ends, a ">" inside a quoted value included, and
// rawToken tells the guard where each token starts. The guard keeps its
// refusal: a decoder that has read a quoted value to the refused byte
// replaces its own error with one about the value's UTF-8 when the byte
// ends it inside a rune.
type tagGuard struct {
	doc []byte
	pos int   // the offset of the next byte served
	tok int   // the offset of the first byte of the token being read
	tag bool  // whether that token is a start tag
	err error // the refusal, once a start tag has passed MaxStartTag
}

// newDecoder is a decoder over doc and the guard that bounds its start tags.
func newDecoder(doc []byte) (*xml.Decoder, *tagGuard) {
	g := &tagGuard{doc: doc}
	return xml.NewDecoder(g), g
}

// rawToken is d.RawToken for the token at d's input offset.
func (g *tagGuard) rawToken(d *xml.Decoder) (xml.Token, error) {
	g.tok, g.tag = int(d.InputOffset()), false
	tok, err := d.RawToken()
	if g.err != nil {
		return nil, g.err
	}
	return tok, err
}

// ReadByte serves the next byte of the document, or the error of a start tag
// that holds more than MaxStartTag bytes, "<" and ">" counted.
func (g *tagGuard) ReadByte() (byte, error) {
	if g.err != nil {
		return 0, g.err
	}
	if g.pos >= len(g.doc) {
		return 0, io.EOF
	}
	c := g.doc[g.pos]
	if g.pos == g.tok+1 {
		g.tag = g.doc[g.tok] == '<' && c != '/' && c != '?' && c != '!'
	}
	g.pos++
	if g.tag && g.pos-g.tok > MaxStartTag {
		g.err = fmt.Errorf("%w: more than %d bytes", ErrStartTagTooLong, MaxStartTag)
		return 0, g.err
	}
	return c, nil
}

// Read serves p a byte at a time, so that no read bypasses the bound. The
// decoder reads through ReadByte.
func (g *tagGuard) Read(p []byte) (int, error) {
	for i := range p {
		b, err := g.ReadByte()
		if err != nil {
			return i, err
		}
		p[i] = b
	}
	return len(p), nil
}
