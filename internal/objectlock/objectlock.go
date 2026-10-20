// Package objectlock is radosgw's S3 object lock without its ops: the
// configuration, retention and legal-hold documents, the default retention a
// bucket's rule gives a write, and the checks a delete and a retention change
// must pass. It reads nothing from RADOS and imports no op, and op maps its
// errors to S3's.
package objectlock

import "errors"

var (
	// ErrMalformedXML is -ERR_MALFORMED_XML, 400 MalformedXML: a document
	// expat cannot parse, without the root radosgw looks for, or that the
	// type's decode_xml throws on.
	ErrMalformedXML = errors.New("objectlock: malformed xml")
	// ErrLocked is verify_object_lock's -EACCES: a version under a retention
	// or legal hold that its delete may not pass.
	ErrLocked = errors.New("objectlock: locked")
	// ErrUndecodable is radosgw's -EIO for a retention or legal-hold attr that
	// does not decode.
	ErrUndecodable = errors.New("objectlock: lock attr does not decode")
	// ErrAccessDenied is PutObjectRetention's -EACCES for a change its
	// existing retention forbids.
	ErrAccessDenied = errors.New("objectlock: access denied")
)

// Error is a refusal. Err is one of the package's errors; Message is the text
// radosgw sets in s->err.message, empty where it sets none; Detail says what
// was refused, a document's as RGWXMLDecoder::err words it, and never holds
// more than a cut of the request's text.
type Error struct {
	Err     error
	Message string
	Detail  string
}

// Error returns Err's text, then Message and Detail where they are set.
func (e *Error) Error() string {
	s := e.Err.Error()
	if e.Message != "" {
		s += ": " + e.Message
	}
	if e.Detail != "" {
		s += ": " + e.Detail
	}
	return s
}

// Unwrap returns Err.
func (e *Error) Unwrap() error { return e.Err }
