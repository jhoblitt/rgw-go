package op

import (
	"errors"
	"math"
	"strings"

	"github.com/jhoblitt/rgw-go/internal/rgwtext"
	"github.com/jhoblitt/rgw-go/internal/xmltext"
)

// errBadPart is RGWMultiPart::xml_end's false, which fails the parse.
var errBadPart = errors.New("op: a Part without a PartNumber or an ETag")

// ParseCompleteMultipart reads a CompleteMultipartUpload document as
// RGWCompleteMultipart::execute reads it through RGWMultiXMLParser
// (rgw_op.cc:6383-6407 at v19.2.6, :7179-7203 at v20.2.4; rgw_multi.cc:24-71
// and rgw_xml.cc, identical at both tags). Every element named Part, at any
// depth, must hold a PartNumber child whose own text is not empty and an
// ETag child; the first of each counts, the number read as C's atoi reads
// it and the ETag's text kept as it is, quotes included. The parts are the
// root's Part children, of a CompleteMultipartUpload root or else of a
// CompletedMultipartUpload one: alloc_obj also builds a MultipartUpload
// root, but execute never looks one up. They come back sorted by number, the
// last of a repeated number kept, as the std::map<int, string> holds them. A
// document the reader refuses, any other root, a bad Part or no parts is
// ErrMalformedXML. The caller bounds doc, as read_all_input bounds it by
// rgw_max_put_param_size; the reader keeps no recursion and allocates in
// proportion to doc alone.
func ParseCompleteMultipart(doc []byte) ([]CompletePart, error) {
	root, err := xmltext.ParseFunc(doc, func(e *xmltext.Element) error {
		if e.Name != "Part" {
			return nil
		}
		if num := e.FindFirst("PartNumber"); num == nil || num.Text == "" || e.FindFirst("ETag") == nil {
			return errBadPart
		}
		return nil
	})
	if err != nil {
		return nil, ErrMalformedXML
	}
	switch root.Name {
	case "CompleteMultipartUpload", "CompletedMultipartUpload":
	default:
		return nil, ErrMalformedXML
	}
	var parts []CompletePart
	for p := range root.Find("Part") {
		parts = append(parts, CompletePart{Number: atoi(p.FindFirst("PartNumber").Text), ETag: p.FindFirst("ETag").Text})
	}
	if len(parts) == 0 {
		return nil, ErrMalformedXML
	}
	return SortCompleteParts(parts), nil
}

// atoi is glibc's atoi on s as a C string: strtol's value, saturated at the
// bounds of a 64-bit long and 0 without digits, cut to an int's low 32 bits.
func atoi(s string) int {
	return int(int32(rgwtext.Atoll(rgwtext.CString(s)))) //nolint:gosec // atoi returns strtol's long as an int
}

// ParseCopySourceRange reads x-amz-copy-source-range as
// RGWPutObj::init_processing does (rgw_op.cc:3868-3900 at v19.2.6,
// :4077-4109 at v20.2.4): "bytes=" at the start, then two runs of digits
// split at the first "-", each run possibly empty, which strtoull reads as 0;
// anything else is ErrInvalidArgument. The bounds are held in an off_t, so a
// value of 2^63 or more turns negative, and a first bound past the last as
// off_t compares is ErrInvalidRange (-ERANGE). A first bound that turns
// negative and is not past the last passes that comparison in radosgw,
// whose range_to_ofs then reads it as a suffix (docs/ceph-upstream-bugs.md,
// "radosgw holds a copy-source range's bounds in an off_t, so a bound past
// 2^63 reads as a suffix"); rgw-go refuses it with ErrInvalidRange, as a
// range that starts past any object (docs/exclusions.md, "A copy-source
// range past 2^63 answers 416").
func ParseCopySourceRange(v string) (first, last uint64, err error) {
	rest, ok := strings.CutPrefix(rgwtext.CString(v), "bytes=")
	if !ok {
		return 0, 0, ErrInvalidArgument
	}
	a, b, ok := strings.Cut(rest, "-")
	if !ok || !allDigits(a) || !allDigits(b) {
		return 0, 0, ErrInvalidArgument
	}
	first, _, _ = rgwtext.Strtoull(a)
	last, _, _ = rgwtext.Strtoull(b)
	switch {
	case int64(first) > int64(last): //nolint:gosec // copy_source_range_fst and _lst are off_t
		return 0, 0, ErrInvalidRange
	case first > math.MaxInt64:
		return 0, 0, ErrInvalidRange
	}
	return first, last, nil
}

// allDigits is find_first_not_of("0123456789") == npos, true for "".
func allDigits(s string) bool {
	return strings.Trim(s, "0123456789") == ""
}
