package policy

import "unicode/utf8"

// rapidjson's messages for its parse errors, GetParseError_En
// (src/s3select/rapidjson/include/rapidjson/error/en.h:36-66 at rapidjson
// fcb23c2d, the submodule both tags build radosgw with).
const (
	errDocumentEmpty                 = "The document is empty."
	errDocumentRootNotSingular       = "The document root must not be followed by other values."
	errValueInvalid                  = "Invalid value."
	errObjectMissName                = "Missing a name for object member."
	errObjectMissColon               = "Missing a colon after a name of object member."
	errObjectMissCommaOrCurlyBracket = "Missing a comma or '}' after an object member."
	errArrayMissCommaOrSquareBracket = "Missing a comma or ']' after an array element."
	errStringUnicodeEscapeInvalidHex = "Incorrect hex digit after \\u escape in string."
	errStringUnicodeSurrogateInvalid = "The surrogate pair in string is invalid."
	errStringEscapeInvalid           = "Invalid escape character in string."
	errStringMissQuotationMark       = "Missing a closing quotation mark in string."
	errStringInvalidEncoding         = "Invalid encoding in string."
	errNumberTooBig                  = "Number too big to be stored in double."
	errNumberMissFraction            = "Miss fraction part in number."
	errNumberMissExponent            = "Miss exponent in number."
	errUnspecificSyntaxError         = "Unspecific syntax error."
)

// jsonHandler is rapidjson's Handler concept as the reader below drives it:
// each event returns false to stop the parse, and refusal is then the
// handler's message. literal stands for Null and Bool.
type jsonHandler interface {
	startObject() bool
	endObject() bool
	startArray() bool
	endArray() bool
	key(s string) bool
	str(s string) bool
	rawNumber(s string) bool
	literal() bool
	refusal() string
}

// jsonReader is rapidjson's recursive GenericReader as radosgw runs it,
// Parse<kParseNumbersAsStringsFlag | kParseCommentsFlag> over a
// StringStream (reader.h:559-1765 at rapidjson fcb23c2d). It does not
// validate UTF-8: string bytes pass through as they are. Each method returns
// false once err is set, as rapidjson's early-return macros do.
type jsonReader struct {
	src string
	pos int
	h   jsonHandler
	err *ParseError
}

func (r *jsonReader) peek() byte {
	if r.pos < len(r.src) {
		return r.src[r.pos]
	}
	return 0
}

func (r *jsonReader) consume(c byte) bool {
	if r.peek() == c {
		r.pos++
		return true
	}
	return false
}

func (r *jsonReader) fail(annotation string, offset int) bool {
	r.err = &ParseError{Offset: int64(offset), Annotation: annotation}
	return false
}

// terminate is kParseErrorTermination: the handler refused an event, and
// radosgw reports the handler's annotation.
func (r *jsonReader) terminate(offset int) bool {
	return r.fail(r.h.refusal(), offset)
}

func (r *jsonReader) parse() *ParseError {
	if r.skipWhitespaceAndComments() {
		switch {
		case r.peek() == 0:
			r.fail(errDocumentEmpty, r.pos)
		case r.parseValue() && r.skipWhitespaceAndComments() && r.peek() != 0:
			r.fail(errDocumentRootNotSingular, r.pos)
		}
	}
	return r.err
}

func (r *jsonReader) skipWhitespace() {
	for {
		switch r.peek() {
		case ' ', '\n', '\r', '\t':
			r.pos++
		default:
			return
		}
	}
}

// skipWhitespaceAndComments is SkipWhitespaceAndComments (reader.h:711-736).
func (r *jsonReader) skipWhitespaceAndComments() bool {
	r.skipWhitespace()
	for r.consume('/') {
		switch {
		case r.consume('*'):
			for {
				if r.peek() == 0 {
					return r.fail(errUnspecificSyntaxError, r.pos)
				}
				if !r.consume('*') {
					r.pos++
				} else if r.consume('/') {
					break
				}
			}
		case r.consume('/'):
			for r.peek() != 0 {
				c := r.src[r.pos]
				r.pos++
				if c == '\n' {
					break
				}
			}
		default:
			return r.fail(errUnspecificSyntaxError, r.pos)
		}
		r.skipWhitespace()
	}
	return true
}

func (r *jsonReader) parseValue() bool {
	switch r.peek() {
	case 'n':
		return r.parseLiteral("null")
	case 't':
		return r.parseLiteral("true")
	case 'f':
		return r.parseLiteral("false")
	case '"':
		return r.parseString(false)
	case '{':
		return r.parseObject()
	case '[':
		return r.parseArray()
	}
	return r.parseNumber()
}

// parseLiteral is ParseNull, ParseTrue and ParseFalse (reader.h:856-892).
func (r *jsonReader) parseLiteral(lit string) bool {
	r.pos++
	for i := 1; i < len(lit); i++ {
		if !r.consume(lit[i]) {
			return r.fail(errValueInvalid, r.pos)
		}
	}
	return r.h.literal() || r.terminate(r.pos)
}

// parseObject is ParseObject (reader.h:740-804).
func (r *jsonReader) parseObject() bool {
	r.pos++
	if !r.h.startObject() {
		return r.terminate(r.pos)
	}
	if !r.skipWhitespaceAndComments() {
		return false
	}
	if r.consume('}') {
		return r.h.endObject() || r.terminate(r.pos)
	}
	for {
		if r.peek() != '"' {
			return r.fail(errObjectMissName, r.pos)
		}
		if !r.parseString(true) || !r.skipWhitespaceAndComments() {
			return false
		}
		if !r.consume(':') {
			return r.fail(errObjectMissColon, r.pos)
		}
		if !r.skipWhitespaceAndComments() || !r.parseValue() || !r.skipWhitespaceAndComments() {
			return false
		}
		switch r.peek() {
		case ',':
			r.pos++
			if !r.skipWhitespaceAndComments() {
				return false
			}
		case '}':
			r.pos++
			return r.h.endObject() || r.terminate(r.pos)
		default:
			return r.fail(errObjectMissCommaOrCurlyBracket, r.pos)
		}
	}
}

// parseArray is ParseArray (reader.h:808-853).
func (r *jsonReader) parseArray() bool {
	r.pos++
	if !r.h.startArray() {
		return r.terminate(r.pos)
	}
	if !r.skipWhitespaceAndComments() {
		return false
	}
	if r.consume(']') {
		return r.h.endArray() || r.terminate(r.pos)
	}
	for {
		if !r.parseValue() || !r.skipWhitespaceAndComments() {
			return false
		}
		switch {
		case r.consume(','):
			if !r.skipWhitespaceAndComments() {
				return false
			}
		case r.consume(']'):
			return r.h.endArray() || r.terminate(r.pos)
		default:
			return r.fail(errArrayMissCommaOrSquareBracket, r.pos)
		}
	}
}

// escapes is ParseStringToStream's escape table (reader.h:994-1000).
var escapes = [256]byte{
	'"': '"', '/': '/', '\\': '\\', 'b': '\b', 'f': '\f', 'n': '\n', 'r': '\r', 't': '\t',
}

// parseString is ParseString and ParseStringToStream (reader.h:959-1068). A
// malformed escape is reported at its backslash; a handler that refuses the
// string or key is reported past the closing quote.
func (r *jsonReader) parseString(key bool) bool {
	r.pos++
	var b []byte
	for {
		c := r.peek()
		switch {
		case c == '\\':
			esc := r.pos
			r.pos++
			e := r.peek()
			switch {
			case escapes[e] != 0:
				r.pos++
				b = append(b, escapes[e])
			case e == 'u':
				r.pos++
				cp, ok := r.parseHex4(esc)
				if !ok {
					return false
				}
				if 0xD800 <= cp && cp <= 0xDFFF {
					if cp > 0xDBFF || !r.consume('\\') || !r.consume('u') {
						return r.fail(errStringUnicodeSurrogateInvalid, esc)
					}
					lo, ok := r.parseHex4(esc)
					if !ok {
						return false
					}
					if lo < 0xDC00 || lo > 0xDFFF {
						return r.fail(errStringUnicodeSurrogateInvalid, esc)
					}
					cp = ((cp-0xD800)<<10 | (lo - 0xDC00)) + 0x10000
				}
				b = utf8.AppendRune(b, cp)
			default:
				return r.fail(errStringEscapeInvalid, esc)
			}
		case c == '"':
			r.pos++
			var ok bool
			if key {
				ok = r.h.key(string(b))
			} else {
				ok = r.h.str(string(b))
			}
			return ok || r.terminate(r.pos)
		case c == 0:
			return r.fail(errStringMissQuotationMark, r.pos)
		case c < 0x20:
			return r.fail(errStringInvalidEncoding, r.pos)
		default:
			b = append(b, c)
			r.pos++
		}
	}
}

// parseHex4 is ParseHex4 (reader.h:906-925), reporting a bad digit at the
// escape's backslash.
func (r *jsonReader) parseHex4(esc int) (rune, bool) {
	var cp rune
	for range 4 {
		v, ok := hexValue(r.peek())
		if !ok {
			return 0, r.fail(errStringUnicodeEscapeInvalidHex, esc)
		}
		cp = cp<<4 | rune(v)
		r.pos++
	}
	return cp, true
}

// parseNumber is ParseNumber (reader.h:1468-1748) under
// kParseNumbersAsStringsFlag: the handler gets the number's text, and no
// value is computed. The checks that remain are the grammar's and
// kParseErrorNumberTooBig's bound on a positive exponent, 308 less the
// fraction digits the significand counted, which rapidjson makes before it
// knows it will not convert. A handler that refuses the number is reported
// at its first character.
func (r *jsonReader) parseNumber() bool {
	start := r.pos
	minus := r.consume('-')

	var i uint32
	var i64 uint64
	use64, useDouble := false, false
	digits := 0
	switch c := r.peek(); {
	case c == '0':
		r.pos++
	case '1' <= c && c <= '9':
		i = uint32(c - '0')
		r.pos++
		limit, last := uint32(429496729), byte('5')
		if minus {
			limit, last = 214748364, '8'
		}
		for isDigit(r.peek()) {
			if i >= limit && (i != limit || r.peek() > last) {
				i64, use64 = uint64(i), true
				break
			}
			i = i*10 + uint32(r.peek()-'0')
			r.pos++
			digits++
		}
	default:
		return r.fail(errValueInvalid, r.pos)
	}
	if use64 {
		limit, last := uint64(0x1999999999999999), byte('5')
		if minus {
			limit, last = 0x0CCCCCCCCCCCCCCC, '8'
		}
		for isDigit(r.peek()) {
			if i64 >= limit && (i64 != limit || r.peek() > last) {
				useDouble = true
				break
			}
			i64 = i64*10 + uint64(r.peek()-'0')
			r.pos++
			digits++
		}
	}
	if useDouble {
		for isDigit(r.peek()) {
			r.pos++
		}
	}

	expFrac := 0
	if r.consume('.') {
		if !isDigit(r.peek()) {
			return r.fail(errNumberMissFraction, r.pos)
		}
		if !useDouble {
			if !use64 {
				i64 = uint64(i)
			}
			for isDigit(r.peek()) && i64 < 1<<53 {
				i64 = i64*10 + uint64(r.peek()-'0')
				r.pos++
				expFrac--
				if i64 != 0 {
					digits++
				}
			}
		}
		// The significand is non-zero here: this loop runs only after a
		// big integer or once i64 passed 2^53 - 1.
		for isDigit(r.peek()) {
			if digits < 17 {
				expFrac--
				digits++
			}
			r.pos++
		}
	}

	if r.consume('e') || r.consume('E') {
		expMinus := false
		if !r.consume('+') {
			expMinus = r.consume('-')
		}
		if !isDigit(r.peek()) {
			return r.fail(errNumberMissExponent, r.pos)
		}
		exp := int(r.peek() - '0')
		r.pos++
		if expMinus {
			maxExp := (expFrac + 2147483639) / 10
			for isDigit(r.peek()) {
				exp = exp*10 + int(r.peek()-'0')
				r.pos++
				for exp > maxExp && isDigit(r.peek()) {
					r.pos++
				}
			}
		} else {
			maxExp := 308 - expFrac
			for isDigit(r.peek()) {
				exp = exp*10 + int(r.peek()-'0')
				r.pos++
				if exp > maxExp {
					return r.fail(errNumberTooBig, start)
				}
			}
		}
	}

	return r.h.rawNumber(r.src[start:r.pos]) || r.terminate(start)
}
