package lifecycle

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/rgwtext"
	"github.com/jhoblitt/rgw-go/internal/strptime"
	"github.com/jhoblitt/rgw-go/internal/tags"
	"github.com/jhoblitt/rgw-go/internal/xmltext"
)

// The refusals Parse makes, each wrapped with its reason. The S3 error each
// stands for is the one radosgw's RGWPutLC::execute answers
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L5991-L6006 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L6657-L6672 at v20.2.4).
var (
	// ErrMalformedXML is -ERR_MALFORMED_XML, 400 MalformedXML: a body that
	// does not parse, or one the grammar refuses.
	ErrMalformedXML = errors.New("lifecycle: malformed xml")
	// ErrInvalidArgument is -EINVAL, 400 InvalidArgument: a rule
	// LCRule::valid refuses, or a second rule under one ID.
	ErrInvalidArgument = errors.New("lifecycle: invalid argument")
	// ErrInvalidRequest is -ERR_INVALID_REQUEST, 400 InvalidRequest: a tag
	// filter on a rule that expires delete markers or aborts uploads.
	ErrInvalidRequest = errors.New("lifecycle: invalid request")
)

// DefaultMaxRules is rgw_lc_max_rules' default
// (https://github.com/ceph/ceph/blob/v19.2.7/src/common/options/rgw.yaml.in#L438-L446 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/common/options/rgw.yaml.in#L464-L472 at v20.2.4).
const DefaultMaxRules = 1000

// maxIDLen is MAX_ID_LEN, the longest rule ID LCRule::valid takes, in bytes
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.h#L28 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.h#L28 at v20.2.4).
const maxIDLen = 255

// generated IDs are gen_rand_alphanumeric_lower's table and the length
// RGWLifecycleConfiguration_S3::decode_xml asks of it
// (https://github.com/ceph/ceph/blob/v19.2.7/src/common/random_string.cc#L62-L76 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/common/random_string.cc#L62-L76 at v20.2.4;
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc_s3.cc#L98-L104 at v19.2.7 and v20.2.4).
const (
	idAlphabet  = "0123456789abcdefghijklmnopqrstuvwxyz"
	generatedID = 48
)

const xmlnsS3 = "http://s3.amazonaws.com/doc/2006-03-01/"

// Parse reads a PutBucketLifecycleConfiguration body as RGWPutLC::execute
// does once it has the body: RGWLifecycleConfiguration_S3::decode_xml
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc_s3.cc#L19-L308 at v19.2.7 and v20.2.4),
// then rebuild's check_and_add_rule over the rules in ID order
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc_s3.cc#L346-L360 at v19.2.7 and v20.2.4;
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L173-L192 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L173-L192 at v20.2.4).
// It returns the configuration to store. maxRules is rgw_lc_max_rules; the
// body's size is the caller's to bound, as radosgw's read of it is.
//
// The refusals and their order are radosgw's, but for what S3's model
// refuses and radosgw takes: a root other than LifecycleConfiguration, and a
// value outside the type and range the model gives it, which radosgw stores
// with another meaning than its sender's (docs/exclusions.md,
// "PutBucketLifecycleConfiguration refuses a document whose root is not
// LifecycleConfiguration", "... holds a rule's numbers to S3's types" and
// "... holds a Date and ExpiredObjectDeleteMarker to S3's types"). A number
// is stored in decimal and a Date as written. Element names are matched as
// written, prefix included, because expat runs without namespace
// processing.
func Parse(body []byte, maxRules int) (Configuration, error) {
	root, err := xmltext.Parse(body)
	if err != nil {
		return Configuration{}, fmt.Errorf("%w: %s", ErrMalformedXML, clip(err.Error()))
	}
	if root.Name != "LifecycleConfiguration" {
		return Configuration{}, fmt.Errorf("%w: the root is %s, not LifecycleConfiguration", ErrMalformedXML, quote(root.Name))
	}
	var rules []Rule
	for el := range root.Find("Rule") {
		r, err := decodeRuleXML(el)
		if err != nil {
			return Configuration{}, fmt.Errorf("%w: Rule %d: %w", ErrMalformedXML, len(rules)+1, err)
		}
		if r.ID == "" {
			r.ID = meta.ChooseFrom(idAlphabet, generatedID)
		}
		rules = append(rules, r)
	}
	if len(rules) == 0 {
		return Configuration{}, fmt.Errorf("%w: missing mandatory field Rule", ErrMalformedXML)
	}
	if maxRules < len(rules) {
		return Configuration{}, fmt.Errorf("%w: %d rules exceed rgw_lc_max_rules, %d", ErrMalformedXML, len(rules), maxRules)
	}
	slices.SortStableFunc(rules, byID)
	return rebuild(rules)
}

// rebuild is check_and_add_rule over rules, which are in rule_map order.
func rebuild(rules []Rule) (Configuration, error) {
	for i := range rules {
		r := &rules[i]
		refusal := r.outOfRange()
		switch {
		case !r.valid():
			return Configuration{}, fmt.Errorf("%w: rule %s is not valid", ErrInvalidArgument, quote(r.ID))
		case refusal != "":
			return Configuration{}, fmt.Errorf("%w: rule %s: %s", ErrInvalidArgument, quote(r.ID), refusal)
		case i > 0 && rules[i-1].ID == r.ID:
			return Configuration{}, fmt.Errorf("%w: a second rule %s", ErrInvalidArgument, quote(r.ID))
		case r.Filter.Tags.Len() > 0 && (r.DMExpiration || !r.MPExpiration.empty()):
			return Configuration{}, fmt.Errorf("%w: rule %s filters by tag and expires delete markers or aborts uploads", ErrInvalidRequest, quote(r.ID))
		}
	}
	return Configuration{Rules: rules}, nil
}

// outOfRange is why S3 refuses a value of r that radosgw stores and whose
// stored form its worker reads with another meaning, or "": a negative
// object size, which stoull reads near 2^64, and a NewerNoncurrentVersions
// outside S3's 1 to 100 (aws-sdk-go v1.55.8, service/s3/api.go:32236-32242),
// which a negative count's uint64_t makes an action that never runs.
func (r Rule) outOfRange() string {
	for _, size := range []struct{ name, value string }{
		{"ObjectSizeGreaterThan", r.Filter.SizeGT}, {"ObjectSizeLessThan", r.Filter.SizeLT},
	} {
		if strings.HasPrefix(size.value, "-") {
			return fmt.Sprintf("%s %s is negative", size.name, quote(size.value))
		}
	}
	if n := r.NoncurExpiration.NewerNoncurrent; n != "" {
		if v := atoi(n); v < 1 || v > 100 {
			return fmt.Sprintf("NewerNoncurrentVersions %s is not 1 to 100", quote(n))
		}
	}
	return ""
}

// text is the first child of el named name, as RGWXMLDecoder::decode_xml
// reads a string: its text when there is one, and whether there is.
func text(el *xmltext.Element, name string) (string, bool) {
	c := el.FindFirst(name)
	if c == nil {
		return "", false
	}
	return c.Text, true
}

// decodeRuleXML is LCRule_S3::decode_xml
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc_s3.cc#L229-L308 at v19.2.7 and v20.2.4).
func decodeRuleXML(el *xmltext.Element) (Rule, error) {
	var r Rule
	r.ID, _ = text(el, "ID")
	status, ok := text(el, "Status")
	if !ok {
		return Rule{}, errors.New("missing Status in Rule")
	}
	if status != "Enabled" && status != "Disabled" {
		return Rule{}, fmt.Errorf("bad Status in Rule: %s", quote(status))
	}
	r.Status = status
	if f := el.FindFirst("Filter"); f != nil {
		filter, err := decodeFilterXML(f)
		if err != nil {
			return Rule{}, err
		}
		r.Filter = filter
	} else if r.Prefix, ok = text(el, "Prefix"); !ok {
		return Rule{}, errors.New("missing Prefix in Filter")
	}

	var exp expirationXML
	expEl := el.FindFirst("Expiration")
	if expEl != nil {
		var err error
		if exp, err = decodeExpirationXML(expEl); err != nil {
			return Rule{}, err
		}
	}
	noncurEl := el.FindFirst("NoncurrentVersionExpiration")
	if noncurEl != nil {
		days, ok := text(noncurEl, "NoncurrentDays")
		if !ok {
			return Rule{}, errors.New("NoncurrentVersionExpiration: missing mandatory field NoncurrentDays")
		}
		var err error
		if days, err = intField("NoncurrentVersionExpiration", "NoncurrentDays", days); err != nil {
			return Rule{}, err
		}
		newer, hasNewer := text(noncurEl, "NewerNoncurrentVersions")
		if hasNewer {
			if newer, err = intField("NoncurrentVersionExpiration", "NewerNoncurrentVersions", newer); err != nil {
				return Rule{}, err
			}
		}
		r.NoncurExpiration = Expiration{Days: days, NewerNoncurrent: newer}
	}
	mpEl := el.FindFirst("AbortIncompleteMultipartUpload")
	if mpEl != nil {
		days, ok := text(mpEl, "DaysAfterInitiation")
		if !ok {
			return Rule{}, errors.New("AbortIncompleteMultipartUpload: missing mandatory field DaysAfterInitiation")
		}
		days, err := intField("AbortIncompleteMultipartUpload", "DaysAfterInitiation", days)
		if err != nil {
			return Rule{}, err
		}
		r.MPExpiration = Expiration{Days: days}
	}
	var transitions, noncurTransitions []Transition
	for t := range el.Find("Transition") {
		tr, err := decodeTransitionXML(t)
		if err != nil {
			return Rule{}, err
		}
		transitions = append(transitions, tr)
	}
	for t := range el.Find("NoncurrentVersionTransition") {
		tr, err := decodeNoncurTransitionXML(t)
		if err != nil {
			return Rule{}, err
		}
		noncurTransitions = append(noncurTransitions, tr)
	}

	if expEl == nil && noncurEl == nil && mpEl == nil && transitions == nil && noncurTransitions == nil {
		return Rule{}, errors.New("bad Rule: no action")
	}
	if exp.Days != "" || exp.Date != "" {
		r.Expiration = Expiration{Days: exp.Days, Date: exp.Date}
	} else {
		r.DMExpiration = exp.dm
	}
	var err error
	if r.Transitions, err = addTransitions(transitions, "Transition"); err != nil {
		return Rule{}, err
	}
	if r.NoncurTransitions, err = addTransitions(noncurTransitions, "NoncurrentVersionTransition"); err != nil {
		return Rule{}, err
	}
	return r, nil
}

// addTransitions is add_transition or add_noncur_transition over ts:
// std::map::emplace under each storage class, which refuses a class already
// there.
func addTransitions(ts []Transition, what string) (map[string]Transition, error) {
	if len(ts) == 0 {
		return nil, nil
	}
	m := make(map[string]Transition, len(ts))
	for _, t := range ts {
		if _, dup := m[t.StorageClass]; dup {
			return nil, fmt.Errorf("a second %s to storage class %s", what, quote(t.StorageClass))
		}
		m[t.StorageClass] = t
	}
	return m, nil
}

// expirationXML is LCExpiration_S3: an Expiration's days or date, or its
// delete-marker flag.
type expirationXML struct {
	Days, Date string
	dm         bool
}

// decodeExpirationXML is LCExpiration_S3::decode_xml
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc_s3.cc#L42-L63 at v19.2.7 and v20.2.4),
// with ExpiredObjectDeleteMarker read as the xs:boolean S3's model makes it
// (aws-sdk-go v1.55.8, service/s3/api.go:28330-28334), where radosgw takes
// the exact text true for the action and any other text for none, and the
// Date held by s3Date.
func decodeExpirationXML(el *xmltext.Element) (expirationXML, error) {
	var x expirationXML
	days, hasDays := text(el, "Days")
	date, hasDate := text(el, "Date")
	dm, hasDM := text(el, "ExpiredObjectDeleteMarker")
	if n := btoi(hasDays) + btoi(hasDate) + btoi(hasDM); n != 1 {
		return expirationXML{}, fmt.Errorf("Expiration: needs exactly one of Days, Date and ExpiredObjectDeleteMarker, not %d", n)
	}
	if hasDays {
		var err error
		if days, err = intField("Expiration", "Days", days); err != nil {
			return expirationXML{}, err
		}
	}
	if hasDate && !s3Date(date) {
		return expirationXML{}, fmt.Errorf("Expiration: bad Date %s", quote(date))
	}
	if hasDM {
		var ok bool
		if x.dm, ok = xsBoolean(dm); !ok {
			return expirationXML{}, fmt.Errorf("Expiration: ExpiredObjectDeleteMarker %s is not a boolean", quote(dm))
		}
	}
	x.Days, x.Date = days, date
	return x, nil
}

// decodeTransitionXML is LCTransition_S3::decode_xml
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc_s3.cc#L186-L202 at v19.2.7 and v20.2.4).
func decodeTransitionXML(el *xmltext.Element) (Transition, error) {
	days, hasDays := text(el, "Days")
	date, hasDate := text(el, "Date")
	if hasDays == hasDate {
		return Transition{}, errors.New("Transition: needs exactly one of Days and Date")
	}
	if hasDays {
		var err error
		if days, err = intField("Transition", "Days", days); err != nil {
			return Transition{}, err
		}
	}
	if hasDate && !s3Date(date) {
		return Transition{}, fmt.Errorf("Transition: bad Date %s", quote(date))
	}
	class, ok := text(el, "StorageClass")
	if !ok {
		return Transition{}, errors.New("Transition: missing StorageClass")
	}
	return Transition{Days: days, Date: date, StorageClass: class}, nil
}

// decodeNoncurTransitionXML is LCNoncurTransition_S3::decode_xml
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc_s3.cc#L213-L221 at v19.2.7 and v20.2.4).
func decodeNoncurTransitionXML(el *xmltext.Element) (Transition, error) {
	days, ok := text(el, "NoncurrentDays")
	if !ok {
		return Transition{}, errors.New("NoncurrentVersionTransition: missing NoncurrentDays")
	}
	days, err := intField("NoncurrentVersionTransition", "NoncurrentDays", days)
	if err != nil {
		return Transition{}, err
	}
	class, ok := text(el, "StorageClass")
	if !ok {
		return Transition{}, errors.New("NoncurrentVersionTransition: missing StorageClass")
	}
	return Transition{Days: days, StorageClass: class}, nil
}

// decodeFilterXML is LCFilter_S3::decode_xml
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc_s3.cc#L146-L184 at v19.2.7 and v20.2.4),
// with the sizes read as S3's xs:long and compared as numbers. radosgw
// compares their text, bytewise, so ObjectSizeGreaterThan 9 with
// ObjectSizeLessThan 10 is refused there and 10 with 9 is taken
// (docs/ceph-upstream-bugs.md, "radosgw's lifecycle grammar reads its
// numbers as text").
func decodeFilterXML(el *xmltext.Element) (Filter, error) {
	o := el.FindFirst("And")
	if o == nil {
		o = el
	}
	var f Filter
	f.Prefix, _ = text(o, "Prefix")
	if o.FindFirst("ArchiveZone") != nil {
		f.Flags |= FlagArchiveZone
	}
	var gt, lt int64
	var err error
	if size, ok := text(o, "ObjectSizeGreaterThan"); ok {
		if gt, err = schemaInt("Filter", "ObjectSizeGreaterThan", size, 64); err != nil {
			return Filter{}, err
		}
		f.SizeGT = strconv.FormatInt(gt, 10)
	}
	if size, ok := text(o, "ObjectSizeLessThan"); ok {
		if lt, err = schemaInt("Filter", "ObjectSizeLessThan", size, 64); err != nil {
			return Filter{}, err
		}
		f.SizeLT = strconv.FormatInt(lt, 10)
	}
	if f.SizeGT != "" && f.SizeLT != "" && lt <= gt {
		return Filter{}, errors.New("Filter: ObjectSizeLessThan is not above ObjectSizeGreaterThan")
	}
	var ts []tags.Tag
	for t := range o.Find("Tag") {
		key, _ := text(t, "Key")
		value, _ := text(t, "Value")
		ts = append(ts, tags.Tag{Key: key, Value: value})
	}
	slices.SortStableFunc(ts, func(a, b tags.Tag) int { return cmp.Compare(a.Key, b.Key) })
	f.Tags = tags.Set{Tags: ts}
	return f, nil
}

// schemaInt reads a lifecycle number as S3's schema types it: an xs:int
// when bits is 32 and an xs:long when it is 64, as the AWS SDK for Go
// models each member integer or long (aws-sdk-go v1.55.8,
// service/s3/api.go: DaysAfterInitiation :12222, Expiration's Days :28328,
// the And's sizes :28530-28533, the Filter's :28618-28621,
// NoncurrentVersionExpiration's NewerNoncurrentVersions and NoncurrentDays
// :32242-32249, NoncurrentVersionTransition's NoncurrentDays :32305,
// Transition's Days :42703). The lexical form is an optional sign, then
// decimal digits, with XML whitespace around them collapsed, within the
// type's range. Anything else is refused, naming the section and field.
func schemaInt(section, field, text string, bits int) (int64, error) {
	v, err := strconv.ParseInt(strings.Trim(text, xmlSpace), 10, bits)
	if err != nil {
		return 0, fmt.Errorf("%s: %s %s is not an integer", section, field, quote(text))
	}
	return v, nil
}

// intField reads a day or version count, an xs:int, and returns it in
// decimal without sign or leading zero, the form S3 and the SDKs write and
// the text a rule stores.
func intField(section, field, text string) (string, error) {
	v, err := schemaInt(section, field, text, 32)
	if err != nil {
		return "", err
	}
	return strconv.FormatInt(v, 10), nil
}

// xmlSpace is XML's S production, the whitespace xs:int's collapse removes
// around a value.
const xmlSpace = " \t\r\n"

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

// xsBoolean reads an xs:boolean: true, false, 1 or 0, the XML whitespace
// around it collapsed.
func xsBoolean(s string) (v, ok bool) {
	switch strings.Trim(s, xmlSpace) {
	case "true", "1":
		return true, true
	case "false", "0":
		return false, true
	}
	return false, false
}

// s3Date reports whether a PUT's Date is S3's, an ISO 8601 timestamp at
// midnight UTC (aws-sdk-go v1.55.8, service/s3/api.go:28322-28324,
// :42697-42699), in a form radosgw's from_iso_8601 reads alike: the text
// from_iso_8601 reads, written YYYY-MM-DDThh:mm:ss with an optional
// fraction and then Z, with nothing but XML whitespace after it, holding a
// calendar date no field of which internal_timegm carries, at 00:00:00 UTC,
// which real_time holds.
//
// check_date
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc_s3.cc#L19-L30 at v19.2.7 and v20.2.4)
// asks only that from_iso_8601's instant be at midnight, so radosgw takes
// a bare date or a year, which it gives back on GET in a form aws-sdk-go-v2
// cannot parse, text after white space, carried fields such as 2026-13-01,
// and a time past real_time's last instant whose wrap lands at midnight:
// 2600-01-01T23:34:33.709551616Z wraps to 2015-06-14T00:00:00Z, which its
// worker treats as long past (docs/ceph-upstream-bugs.md, "radosgw's
// lifecycle Date wraps past 2554 and expires at once").
func s3Date(s string) bool {
	r, ok := readISO8601(s)
	switch {
	case !ok || !r.full || strings.Trim(s[r.end:], xmlSpace) != "":
		return false
	case r.month < 1 || r.month > 12 || r.day < 1 || r.day > time.Date(r.year, time.Month(r.month)+1, 0, 0, 0, 0, 0, time.UTC).Day():
		return false
	case r.hour != 0 || r.minute != 0 || r.second != 0 || r.nsec != 0:
		return false
	}
	return realTime(r.t)
}

// lastRealTime is real_time's last instant, 2^64-1 nanoseconds after the
// epoch.
var lastRealTime = time.Unix(18446744073, 709551615).UTC()

// realTime reports whether real_time's u64 nanoseconds hold t.
func realTime(t time.Time) bool {
	return !t.Before(time.Unix(0, 0)) && !t.After(lastRealTime)
}

// isoReading is what from_iso_8601 read: each field as written, the instant
// internal_timegm makes of them, the bytes of the text it read, its Z
// included and the white space that may end a date not, and whether that
// text ran to the seconds and a Z.
type isoReading struct {
	year, month, day, hour, minute, second, nsec int
	end                                          int
	full                                         bool
	t                                            time.Time
}

// readISO8601 is ceph::from_iso_8601 with ws_terminates
// (https://github.com/ceph/ceph/blob/v19.2.7/src/common/iso_8601.cc#L39-L151 at v19.2.7 and v20.2.4)
// reading s, ok false where it answers none. The instant is
// internal_timegm's, carried fields rolled forward, before real_time's
// modulo 2^64 nanoseconds, which realTime checks.
//
// TODO: switch to internal/iso8601 once it exists; this stands in for its
// one reading of from_iso_8601 until then.
func readISO8601(s string) (isoReading, bool) {
	p := 0
	digits := func(n int) (int, bool) {
		v := 0
		for range n {
			if p == len(s) || s[p] < '0' || s[p] > '9' {
				return 0, false
			}
			v = v*10 + int(s[p]-'0')
			p++
		}
		return v, true
	}
	space := func(c byte) bool { return c == ' ' || '\t' <= c && c <= '\r' }
	dateEnd := func() bool { return p == len(s) || space(s[p]) }
	timeEnd := func() bool { return p < len(s) && s[p] == 'Z' && (p+1 == len(s) || space(s[p+1])) }
	year, ok := digits(4)
	if !ok || year < 1970 {
		return isoReading{}, false
	}
	f := [5]int{1, 1, 0, 0, 0} // month, day, hour, minute, second
	at := func(nsec, end int, full bool) (isoReading, bool) {
		t := strptime.Tm{Year: year, Mon: f[0] - 1, Mday: f[1], Hour: f[2], Min: f[3], Sec: f[4]}.Timegm().Add(time.Duration(nsec))
		return isoReading{year: year, month: f[0], day: f[1], hour: f[2], minute: f[3], second: f[4], nsec: nsec, end: end, full: full, t: t}, true
	}
	if dateEnd() {
		return at(0, p, false)
	}
	const delims = "--T::"
	for i := range f {
		if p == len(s) || s[p] != delims[i] {
			return isoReading{}, false
		}
		p++
		if f[i], ok = digits(2); !ok {
			return isoReading{}, false
		}
		if i < 2 && dateEnd() {
			return at(0, p, false)
		}
		if i >= 2 && timeEnd() {
			return at(0, p+1, i == len(f)-1)
		}
	}
	if p == len(s) || s[p] != '.' {
		return isoReading{}, false
	}
	p++
	nsec, scale := 0, 100_000_000
	for range 9 {
		d, ok := digits(1)
		if !ok {
			return isoReading{}, false
		}
		nsec += d * scale
		scale /= 10
		if timeEnd() {
			return at(nsec, p+1, true)
		}
	}
	return isoReading{}, false
}

// maxQuoted is the most of a client's text a refusal repeats, in bytes.
const maxQuoted = 512

// quote is s cut to at most maxQuoted bytes, wherever that falls, then
// quoted, and marked when cut.
func quote(s string) string {
	if len(s) <= maxQuoted {
		return strconv.Quote(s)
	}
	return strconv.Quote(s[:maxQuoted]) + "..."
}

// clip is a message cut to at most maxQuoted bytes, for a parser's message
// that may repeat the client's text.
func clip(s string) string {
	if len(s) <= maxQuoted {
		return s
	}
	return s[:maxQuoted] + "..."
}

// atoi is glibc's atoi, (int) strtol(s, NULL, 10): white space, a sign and
// digits, saturated at a 64-bit long, then cut to 32 bits.
func atoi(s string) int {
	v, _, _ := rgwtext.Strtoll(s)
	return int(int32(v)) //nolint:gosec // atoi casts strtol's long to int
}

func (x Expiration) empty() bool { return x.Days == "" && x.Date == "" }

// valid is LCExpiration::valid
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.h#L95-L103 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.h#L95-L103 at v20.2.4).
func (x Expiration) valid() bool {
	if x.Days != "" && x.Date != "" {
		return false
	}
	return x.Days == "" || atoi(x.Days) > 0
}

// valid is LCTransition::valid
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.h#L139-L147 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.h#L139-L147 at v20.2.4).
func (t Transition) valid() bool {
	if t.Days != "" && t.Date != "" {
		return false
	}
	return t.Days == "" || atoi(t.Days) >= 0
}

// valid is LCRule::valid
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L60-L95 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L60-L95 at v20.2.4).
// The days-or-dates test spans the expiration and the current transitions
// alone, and holds only when the rule has a transition.
func (r Rule) valid() bool {
	switch {
	case len(r.ID) > maxIDLen:
		return false
	case r.Expiration.empty() && r.NoncurExpiration.empty() && r.MPExpiration.empty() && !r.DMExpiration &&
		len(r.Transitions) == 0 && len(r.NoncurTransitions) == 0:
		return false
	case !r.Expiration.valid() || !r.NoncurExpiration.valid() || !r.MPExpiration.valid():
		return false
	}
	usingDays, usingDate := r.Expiration.Days != "", r.Expiration.Date != ""
	for _, t := range r.Transitions {
		if !t.valid() {
			return false
		}
		usingDays = usingDays || t.Days != ""
		usingDate = usingDate || t.Date != ""
		if usingDays && usingDate {
			return false
		}
	}
	for _, t := range r.NoncurTransitions {
		if !t.valid() {
			return false
		}
	}
	return true
}

func (f Filter) empty() bool {
	return f.Prefix == "" && f.Tags.Len() == 0 && f.Flags == 0 && f.SizeGT == "" && f.SizeLT == ""
}

// multiCondition is has_multi_condition
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.h#L237-L242 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.h#L237-L242 at v20.2.4):
// whether the filter needs And, the two sizes counting as one condition.
func (f Filter) multiCondition() bool {
	return f.Tags.Len()+btoi(f.Prefix != "")+btoi(f.Flags != 0)+btoi(f.SizeGT != "" || f.SizeLT != "") > 1
}

// Marshal is the body GetBucketLifecycleConfiguration writes,
// RGWGetLC_ObjStore_S3::send_response's
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3708-L3726 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4159-L4177 at v20.2.4)
// without the XML declaration the op writes before it: the
// LifecycleConfiguration section in the S3 namespace, then each rule in
// rule_map order as LCRule_S3::dump_xml writes it
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc_s3.cc#L310-L344 at v19.2.7 and v20.2.4):
// ID; the Filter when it is not empty, else the rule's Prefix; Status;
// Expiration, as the delete marker, else Days, else Date; the
// NoncurrentVersionExpiration, NewerNoncurrentVersions first when set; the
// AbortIncompleteMultipartUpload; the Transitions, each Days, else Date,
// then StorageClass; and the NoncurrentVersionTransitions; each map in key
// order. A Filter holds Prefix, the tags, ArchiveZone and the sizes, each
// when set, inside And when has_multi_condition says so
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc_s3.cc#L117-L144 at v19.2.7 and v20.2.4).
// Every value goes through xmltext.Escape, as encode_xml writes a string
// with the formatter's dump_string, and XMLFormatter closes every section
// with an end tag.
func Marshal(c Configuration) []byte {
	var b bytes.Buffer
	b.WriteString(`<LifecycleConfiguration xmlns="` + xmlnsS3 + `">`)
	rules := c.ordered()
	for i := range rules {
		writeRule(&b, &rules[i])
	}
	b.WriteString("</LifecycleConfiguration>")
	return b.Bytes()
}

func element(b *bytes.Buffer, name, value string) {
	b.WriteString("<" + name + ">")
	b.WriteString(xmltext.Escape(value))
	b.WriteString("</" + name + ">")
}

func writeRule(b *bytes.Buffer, r *Rule) {
	b.WriteString("<Rule>")
	element(b, "ID", r.ID)
	if !r.Filter.empty() {
		b.WriteString("<Filter>")
		writeFilter(b, r.Filter)
		b.WriteString("</Filter>")
	} else {
		element(b, "Prefix", r.Prefix)
	}
	element(b, "Status", r.Status)
	if x := r.Expiration; !x.empty() || r.DMExpiration {
		b.WriteString("<Expiration>")
		switch {
		case r.DMExpiration:
			element(b, "ExpiredObjectDeleteMarker", "true")
		case x.Days != "":
			element(b, "Days", x.Days)
		default:
			element(b, "Date", x.Date)
		}
		b.WriteString("</Expiration>")
	}
	if x := r.NoncurExpiration; !x.empty() {
		b.WriteString("<NoncurrentVersionExpiration>")
		if x.NewerNoncurrent != "" {
			element(b, "NewerNoncurrentVersions", x.NewerNoncurrent)
		}
		element(b, "NoncurrentDays", x.Days)
		b.WriteString("</NoncurrentVersionExpiration>")
	}
	if x := r.MPExpiration; !x.empty() {
		b.WriteString("<AbortIncompleteMultipartUpload>")
		element(b, "DaysAfterInitiation", x.Days)
		b.WriteString("</AbortIncompleteMultipartUpload>")
	}
	for _, class := range slices.Sorted(maps.Keys(r.Transitions)) {
		t := r.Transitions[class]
		b.WriteString("<Transition>")
		if t.Days != "" {
			element(b, "Days", t.Days)
		} else {
			element(b, "Date", t.Date)
		}
		element(b, "StorageClass", t.StorageClass)
		b.WriteString("</Transition>")
	}
	for _, class := range slices.Sorted(maps.Keys(r.NoncurTransitions)) {
		t := r.NoncurTransitions[class]
		b.WriteString("<NoncurrentVersionTransition>")
		element(b, "NoncurrentDays", t.Days)
		element(b, "StorageClass", t.StorageClass)
		b.WriteString("</NoncurrentVersionTransition>")
	}
	b.WriteString("</Rule>")
}

func writeFilter(b *bytes.Buffer, f Filter) {
	multi := f.multiCondition()
	if multi {
		b.WriteString("<And>")
	}
	if f.Prefix != "" {
		element(b, "Prefix", f.Prefix)
	}
	for _, t := range sortedTags(f.Tags) {
		b.WriteString("<Tag>")
		element(b, "Key", t.Key)
		element(b, "Value", t.Value)
		b.WriteString("</Tag>")
	}
	if f.Flags&FlagArchiveZone != 0 {
		element(b, "ArchiveZone", "")
	}
	if f.SizeGT != "" {
		element(b, "ObjectSizeGreaterThan", f.SizeGT)
	}
	if f.SizeLT != "" {
		element(b, "ObjectSizeLessThan", f.SizeLT)
	}
	if multi {
		b.WriteString("</And>")
	}
}

// sortedTags is s's tags in multimap order, sorted by key with equal keys in
// the order they were added, whatever order a caller built them in.
func sortedTags(s tags.Set) []tags.Tag {
	byKey := func(a, b tags.Tag) int { return cmp.Compare(a.Key, b.Key) }
	if slices.IsSortedFunc(s.Tags, byKey) {
		return s.Tags
	}
	t := slices.Clone(s.Tags)
	slices.SortStableFunc(t, byKey)
	return t
}
