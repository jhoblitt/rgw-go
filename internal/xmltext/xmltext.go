// Package xmltext writes and reads XML as radosgw does. It escapes element
// text as ceph's XMLFormatter does, so the XML documents rgw-go writes, the S3
// responses and the admin API's XML alike, carry radosgw's bytes, and it reads
// a request body into the element tree radosgw's RGWXMLParser builds, refusing
// what expat refuses. It imports only the standard library, so any package
// that renders or parses XML may use it.
package xmltext

import (
	"encoding/xml"
	"strings"
)

// Escape is xml_stream_escaper (src/common/escape.cc:134-169 at v19.2.6 and
// v20.2.4), the escaping XMLFormatter gives every string it writes as element
// text (Formatter.cc:516-571 at v19.2.6, :536-591 at v20.2.4): &, <, >, ' and
// " become &amp;, &lt;, &gt;, &apos; and &quot;; a byte below 0x20 other than
// tab and newline, and 0x7f, becomes a character reference with two lowercase
// hex digits, &#x0d; for a carriage return; every other byte is copied, those
// from 0x80 up whether or not they are valid UTF-8.
func Escape(s string) string {
	var b strings.Builder
	last := 0
	for i := range len(s) {
		esc := escapes[s[i]]
		if esc == "" {
			continue
		}
		if last == 0 {
			b.Grow(len(s) + 16)
		}
		b.WriteString(s[last:i])
		b.WriteString(esc)
		last = i + 1
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// escapes is what xml_stream_escaper writes in place of each byte, "" for a
// byte it copies; the control-byte test is escape.cc:157's.
var escapes = func() (t [256]string) {
	const hexDigits = "0123456789abcdef"
	for c := range t {
		if (c < 0x20 && c != '\t' && c != '\n') || c == 0x7f {
			t[c] = "&#x" + string(hexDigits[c>>4]) + string(hexDigits[c&0x0f]) + ";"
		}
	}
	t['&'], t['<'], t['>'], t['\''], t['"'] = "&amp;", "&lt;", "&gt;", "&apos;", "&quot;"
	return t
}()

// Text is element text that encoding/xml writes escaped as Escape escapes it;
// every string field of an S3 XML document is a Text. encoding/xml's own
// escaping is not radosgw's: it writes &#34; and &#39; for the quotes, escapes
// tab, newline and carriage return, copies 0x7f, and turns the other control
// bytes and invalid UTF-8 into U+FFFD, so a key holding such a byte would be
// listed under a name other than the one stored.
//
// encoding/xml calls MarshalXML only for a Text it writes as an element; a
// Text in a chardata or attr field gets encoding/xml's own escaping. omitempty
// applies to a Text as to a string.
type Text string

// MarshalXML writes start, the text escaped by Escape and the end element.
func (t Text) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	return e.EncodeElement(struct {
		Escaped string `xml:",innerxml"`
	}{Escape(string(t))}, start)
}
