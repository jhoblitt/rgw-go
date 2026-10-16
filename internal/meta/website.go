package meta

import (
	"encoding/json"
	"math"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/formatter"
)

// websiteConfVersion is the RGWBucketWebsiteConf version encode writes and
// DECODE_START reads up to.
const websiteConfVersion = 2

// code is a website type's u16 status code, read as an int and refused
// outside u16's range, which RGWRedirectInfo::decode_json and
// RGWBWRoutingRuleCondition::decode_json wrap when they assign the int
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_website.cc#L142-L148
// and
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_website.cc#L169-L174 at v19.2.7 and v20.2.4).
func (f *jsonFields) code(name string) uint16 {
	n := f.int32v(name)
	if n < 0 || n > math.MaxUint16 {
		f.fail(name, "out of u16's range")
		return 0
	}
	return uint16(n)
}

// RedirectInfo is RGWRedirectInfo
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_website.h#L26-L50 at v19.2.7 and v20.2.4):
// the protocol, hostname and status code of a redirect.
type RedirectInfo struct {
	Protocol         string
	Hostname         string
	HTTPRedirectCode uint16
}

// Encode mirrors RGWRedirectInfo::encode, ENCODE_START(1, 1).
func (r RedirectInfo) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	e.String(r.Protocol)
	e.String(r.Hostname)
	e.U16(r.HTTPRedirectCode)
	e.EndStruct(f)
}

// DecodeRedirectInfo mirrors RGWRedirectInfo::decode, DECODE_START(1).
func DecodeRedirectInfo(d *denc.Decoder) RedirectInfo {
	h := d.BeginStruct(1)
	var r RedirectInfo
	r.Protocol = d.String()
	r.Hostname = d.String()
	r.HTTPRedirectCode = d.U16()
	d.EndStruct(h)
	return r
}

// Dump is RGWRedirectInfo::dump
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_website.cc#L135-L140 at v19.2.7 and v20.2.4).
func (r RedirectInfo) Dump(f formatter.Formatter, _ denc.Release) {
	f.DumpString("protocol", r.Protocol)
	f.DumpString("hostname", r.Hostname)
	f.DumpInt("http_redirect_code", int64(r.HTTPRedirectCode))
}

// MarshalJSON writes Dump's members as JSON.
func (r RedirectInfo) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Protocol         string `json:"protocol"`
		Hostname         string `json:"hostname"`
		HTTPRedirectCode uint16 `json:"http_redirect_code"`
	}{r.Protocol, r.Hostname, r.HTTPRedirectCode})
}

// UnmarshalJSON is RGWRedirectInfo::decode_json
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_website.cc#L142-L148 at v19.2.7 and v20.2.4).
func (r *RedirectInfo) UnmarshalJSON(data []byte) error {
	f, err := newJSONFields(data)
	if err != nil {
		return err
	}
	out := RedirectInfo{Protocol: f.str("protocol"), Hostname: f.str("hostname"), HTTPRedirectCode: f.code("http_redirect_code")}
	if f.err != nil {
		return f.err
	}
	*r = out
	return nil
}

// RoutingRedirect is RGWBWRedirectInfo
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_website.h#L53-L79 at v19.2.7 and v20.2.4):
// a routing rule's redirect, with the text that replaces the key's matched
// prefix or the whole key.
type RoutingRedirect struct {
	Redirect             RedirectInfo
	ReplaceKeyPrefixWith string
	ReplaceKeyWith       string
}

// Encode mirrors RGWBWRedirectInfo::encode, ENCODE_START(1, 1).
func (r RoutingRedirect) Encode(e *denc.Encoder, rel denc.Release) {
	f := e.BeginStruct(1, 1)
	r.Redirect.Encode(e, rel)
	e.String(r.ReplaceKeyPrefixWith)
	e.String(r.ReplaceKeyWith)
	e.EndStruct(f)
}

// DecodeRoutingRedirect mirrors RGWBWRedirectInfo::decode, DECODE_START(1).
func DecodeRoutingRedirect(d *denc.Decoder) RoutingRedirect {
	h := d.BeginStruct(1)
	var r RoutingRedirect
	r.Redirect = DecodeRedirectInfo(d)
	r.ReplaceKeyPrefixWith = d.String()
	r.ReplaceKeyWith = d.String()
	d.EndStruct(h)
	return r
}

// Dump is RGWBWRedirectInfo::dump
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_website.cc#L150-L155 at v19.2.7 and v20.2.4).
func (r RoutingRedirect) Dump(f formatter.Formatter, rel denc.Release) {
	dumpSection(f, "redirect", r.Redirect, rel)
	f.DumpString("replace_key_prefix_with", r.ReplaceKeyPrefixWith)
	f.DumpString("replace_key_with", r.ReplaceKeyWith)
}

// MarshalJSON writes Dump's members as JSON.
func (r RoutingRedirect) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Redirect             RedirectInfo `json:"redirect"`
		ReplaceKeyPrefixWith string       `json:"replace_key_prefix_with"`
		ReplaceKeyWith       string       `json:"replace_key_with"`
	}{r.Redirect, r.ReplaceKeyPrefixWith, r.ReplaceKeyWith})
}

// UnmarshalJSON is RGWBWRedirectInfo::decode_json
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_website.cc#L157-L161 at v19.2.7 and v20.2.4).
func (r *RoutingRedirect) UnmarshalJSON(data []byte) error {
	f, err := newJSONFields(data)
	if err != nil {
		return err
	}
	var out RoutingRedirect
	f.decode("redirect", &out.Redirect)
	out.ReplaceKeyPrefixWith = f.str("replace_key_prefix_with")
	out.ReplaceKeyWith = f.str("replace_key_with")
	if f.err != nil {
		return f.err
	}
	*r = out
	return nil
}

// RoutingCondition is RGWBWRoutingRuleCondition
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_website.h#L81-L109 at v19.2.7 and v20.2.4):
// the key prefix and the error status a routing rule matches.
type RoutingCondition struct {
	KeyPrefixEquals             string
	HTTPErrorCodeReturnedEquals uint16
}

// Encode mirrors RGWBWRoutingRuleCondition::encode, ENCODE_START(1, 1).
func (c RoutingCondition) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	e.String(c.KeyPrefixEquals)
	e.U16(c.HTTPErrorCodeReturnedEquals)
	e.EndStruct(f)
}

// DecodeRoutingCondition mirrors RGWBWRoutingRuleCondition::decode,
// DECODE_START(1).
func DecodeRoutingCondition(d *denc.Decoder) RoutingCondition {
	h := d.BeginStruct(1)
	var c RoutingCondition
	c.KeyPrefixEquals = d.String()
	c.HTTPErrorCodeReturnedEquals = d.U16()
	d.EndStruct(h)
	return c
}

// Dump is RGWBWRoutingRuleCondition::dump
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_website.cc#L163-L167 at v19.2.7 and v20.2.4).
func (c RoutingCondition) Dump(f formatter.Formatter, _ denc.Release) {
	f.DumpString("key_prefix_equals", c.KeyPrefixEquals)
	f.DumpInt("http_error_code_returned_equals", int64(c.HTTPErrorCodeReturnedEquals))
}

// MarshalJSON writes Dump's members as JSON.
func (c RoutingCondition) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		KeyPrefixEquals             string `json:"key_prefix_equals"`
		HTTPErrorCodeReturnedEquals uint16 `json:"http_error_code_returned_equals"`
	}{c.KeyPrefixEquals, c.HTTPErrorCodeReturnedEquals})
}

// UnmarshalJSON is RGWBWRoutingRuleCondition::decode_json
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_website.cc#L169-L174 at v19.2.7 and v20.2.4).
func (c *RoutingCondition) UnmarshalJSON(data []byte) error {
	f, err := newJSONFields(data)
	if err != nil {
		return err
	}
	out := RoutingCondition{KeyPrefixEquals: f.str("key_prefix_equals"), HTTPErrorCodeReturnedEquals: f.code("http_error_code_returned_equals")}
	if f.err != nil {
		return f.err
	}
	*c = out
	return nil
}

// RoutingRule is RGWBWRoutingRule
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_website.h#L111-L147 at v19.2.7 and v20.2.4):
// a condition and the redirect taken when it matches.
type RoutingRule struct {
	Condition    RoutingCondition
	RedirectInfo RoutingRedirect
}

// Encode mirrors RGWBWRoutingRule::encode, ENCODE_START(1, 1).
func (r RoutingRule) Encode(e *denc.Encoder, rel denc.Release) {
	f := e.BeginStruct(1, 1)
	r.Condition.Encode(e, rel)
	r.RedirectInfo.Encode(e, rel)
	e.EndStruct(f)
}

// DecodeRoutingRule mirrors RGWBWRoutingRule::decode, DECODE_START(1).
func DecodeRoutingRule(d *denc.Decoder) RoutingRule {
	h := d.BeginStruct(1)
	var r RoutingRule
	r.Condition = DecodeRoutingCondition(d)
	r.RedirectInfo = DecodeRoutingRedirect(d)
	d.EndStruct(h)
	return r
}

// Dump is RGWBWRoutingRule::dump
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_website.cc#L176-L180 at v19.2.7 and v20.2.4).
func (r RoutingRule) Dump(f formatter.Formatter, rel denc.Release) {
	dumpSection(f, "condition", r.Condition, rel)
	dumpSection(f, "redirect_info", r.RedirectInfo, rel)
}

// MarshalJSON writes Dump's members as JSON.
func (r RoutingRule) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Condition    RoutingCondition `json:"condition"`
		RedirectInfo RoutingRedirect  `json:"redirect_info"`
	}{r.Condition, r.RedirectInfo})
}

// UnmarshalJSON is RGWBWRoutingRule::decode_json
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_website.cc#L182-L185 at v19.2.7 and v20.2.4).
func (r *RoutingRule) UnmarshalJSON(data []byte) error {
	f, err := newJSONFields(data)
	if err != nil {
		return err
	}
	out := readRoutingRule(f)
	if f.err != nil {
		return f.err
	}
	*r = out
	return nil
}

func readRoutingRule(f *jsonFields) RoutingRule {
	var r RoutingRule
	f.decode("condition", &r.Condition)
	f.decode("redirect_info", &r.RedirectInfo)
	return r
}

// RoutingRules is RGWBWRoutingRules
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_website.h#L149-L174 at v19.2.7 and v20.2.4):
// a website's routing rules in the order they are tried.
type RoutingRules []RoutingRule

// Encode mirrors RGWBWRoutingRules::encode, ENCODE_START(1, 1) around the
// list.
func (rs RoutingRules) Encode(e *denc.Encoder, rel denc.Release) {
	f := e.BeginStruct(1, 1)
	denc.EncodeSlice(e, rs, func(e *denc.Encoder, r RoutingRule) { r.Encode(e, rel) })
	e.EndStruct(f)
}

// DecodeRoutingRules mirrors RGWBWRoutingRules::decode, DECODE_START(1); no
// rules is nil.
func DecodeRoutingRules(d *denc.Decoder) RoutingRules {
	h := d.BeginStruct(1)
	rs := denc.DecodeSlice(d, DecodeRoutingRule)
	d.EndStruct(h)
	return rs
}

// Dump is RGWBWRoutingRules::dump
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_website.cc#L187-L190 at v19.2.7 and v20.2.4):
// the list as encode_json writes one, an array of "obj" sections.
func (rs RoutingRules) Dump(f formatter.Formatter, rel denc.Release) {
	f.OpenArraySection("rules")
	for _, r := range rs {
		dumpSection(f, "obj", r, rel)
	}
	f.CloseSection()
}

// MarshalJSON writes Dump's members as JSON.
func (rs RoutingRules) MarshalJSON() ([]byte, error) {
	rules := []RoutingRule(rs)
	if rules == nil {
		rules = []RoutingRule{}
	}
	return json.Marshal(struct {
		Rules []RoutingRule `json:"rules"`
	}{rules})
}

// UnmarshalJSON is RGWBWRoutingRules::decode_json
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_website.cc#L192-L194 at v19.2.7 and v20.2.4).
func (rs *RoutingRules) UnmarshalJSON(data []byte) error {
	f, err := newJSONFields(data)
	if err != nil {
		return err
	}
	var out RoutingRules
	f.objects("rules", func(_ int, e *jsonFields) { out = append(out, readRoutingRule(e)) })
	if f.err != nil {
		return f.err
	}
	*rs = out
	return nil
}

// WebsiteConf is RGWBucketWebsiteConf
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_website.h#L176-L243 at v19.2.7 and v20.2.4),
// a bucket's static website configuration. SubdirMarker, ListingCSSDoc and
// ListingEnabled are set only from Swift container metadata
// (filter_out_website,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L3377-L3414 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L3605-L3642 at v20.2.4).
// RGWBucketWebsiteConf's is_redirect_all and is_set_index_doc are never
// stored, so they have no field.
type WebsiteConf struct {
	RedirectAll    RedirectInfo
	IndexDocSuffix string
	ErrorDoc       string
	SubdirMarker   string
	ListingCSSDoc  string
	ListingEnabled bool
	RoutingRules   RoutingRules
}

// Encode mirrors RGWBucketWebsiteConf::encode, ENCODE_START(2, 1), whatever
// version the configuration was read at.
func (w WebsiteConf) Encode(e *denc.Encoder, rel denc.Release) {
	f := e.BeginStruct(websiteConfVersion, 1)
	e.String(w.IndexDocSuffix)
	e.String(w.ErrorDoc)
	w.RoutingRules.Encode(e, rel)
	w.RedirectAll.Encode(e, rel)
	e.String(w.SubdirMarker)
	e.String(w.ListingCSSDoc)
	e.Bool(w.ListingEnabled)
	e.EndStruct(f)
}

// DecodeWebsiteConf mirrors RGWBucketWebsiteConf::decode, DECODE_START(2):
// version 1 carries none of the Swift members, which keep their defaults.
func DecodeWebsiteConf(d *denc.Decoder) WebsiteConf {
	h := d.BeginStruct(websiteConfVersion)
	var w WebsiteConf
	w.IndexDocSuffix = d.String()
	w.ErrorDoc = d.String()
	w.RoutingRules = DecodeRoutingRules(d)
	w.RedirectAll = DecodeRedirectInfo(d)
	if h.Version >= 2 {
		w.SubdirMarker = d.String()
		w.ListingCSSDoc = d.String()
		w.ListingEnabled = d.Bool()
	}
	d.EndStruct(h)
	return w
}

// Dump is RGWBucketWebsiteConf::dump
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_website.cc#L196-L205 at v19.2.7 and v20.2.4):
// redirect_all alone when it names a hostname, else index_doc_suffix,
// error_doc and routing_rules, and never the Swift members.
func (w WebsiteConf) Dump(f formatter.Formatter, rel denc.Release) {
	if w.RedirectAll.Hostname != "" {
		dumpSection(f, "redirect_all", w.RedirectAll, rel)
		return
	}
	f.DumpString("index_doc_suffix", w.IndexDocSuffix)
	f.DumpString("error_doc", w.ErrorDoc)
	dumpSection(f, "routing_rules", w.RoutingRules, rel)
}

// MarshalJSON writes Dump's members as JSON.
func (w WebsiteConf) MarshalJSON() ([]byte, error) {
	if w.RedirectAll.Hostname != "" {
		return json.Marshal(struct {
			RedirectAll RedirectInfo `json:"redirect_all"`
		}{w.RedirectAll})
	}
	return json.Marshal(struct {
		IndexDocSuffix string       `json:"index_doc_suffix"`
		ErrorDoc       string       `json:"error_doc"`
		RoutingRules   RoutingRules `json:"routing_rules"`
	}{w.IndexDocSuffix, w.ErrorDoc, w.RoutingRules})
}

// UnmarshalJSON is RGWBucketWebsiteConf::decode_json
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_website.cc#L207-L212 at v19.2.7 and v20.2.4):
// redirect_all, index_doc_suffix, error_doc and routing_rules, whatever the
// hostname. It reads none of the Swift members, which keep the defaults a
// newly decoded RGWBucketInfo gives them.
func (w *WebsiteConf) UnmarshalJSON(data []byte) error {
	f, err := newJSONFields(data)
	if err != nil {
		return err
	}
	var out WebsiteConf
	f.decode("redirect_all", &out.RedirectAll)
	out.IndexDocSuffix = f.str("index_doc_suffix")
	out.ErrorDoc = f.str("error_doc")
	f.decode("routing_rules", &out.RoutingRules)
	if f.err != nil {
		return f.err
	}
	*w = out
	return nil
}
