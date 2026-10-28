package website

import (
	"bytes"
	"errors"

	"github.com/jhoblitt/rgw-go/internal/xmltext"
)

// schema is S3's WebsiteConfiguration document as aws-sdk-go v1.55.8 models
// it (service/s3/api.go: WebsiteConfiguration :43877-43893, its RoutingRules
// a list of RoutingRule :43892, ErrorDocument :20424-20435, IndexDocument
// :27208-27223, RedirectAllRequestsTo :39566-39577, RoutingRule
// :40688-40703, Condition :14366-14388, Redirect :39480-39514): the elements
// each element holds, each at most once but RoutingRule, in S3's namespace.
// A name with no entry is a field, which holds text alone.
var schema = &xmltext.Schema{
	Root: rootName,
	Children: map[string][]xmltext.Child{
		rootName:                {{Name: "ErrorDocument"}, {Name: "IndexDocument"}, {Name: "RedirectAllRequestsTo"}, {Name: "RoutingRules"}},
		"ErrorDocument":         {{Name: "Key"}},
		"IndexDocument":         {{Name: "Suffix"}},
		"RedirectAllRequestsTo": {{Name: "HostName"}, {Name: "Protocol"}},
		"RoutingRules":          {{Name: "RoutingRule", Repeats: true}},
		"RoutingRule":           {{Name: "Condition"}, {Name: "Redirect"}},
		"Condition":             {{Name: "HttpErrorCodeReturnedEquals"}, {Name: "KeyPrefixEquals"}},
		"Redirect":              {{Name: "HostName"}, {Name: "HttpRedirectCode"}, {Name: "Protocol"}, {Name: "ReplaceKeyPrefixWith"}, {Name: "ReplaceKeyWith"}},
	},
	Namespaces: []string{xmlnsS3},
}

// scan reads body with xmltext.Scan against schema, building no tree, and
// refuses, as MalformedXML, the first token that S3's schema does not admit
// and the first element whole that S3's model does not (model.end);
// ParseXML's doc lists what that is. A refused body costs a read of the
// tokens before the refusal, and an admitted one a tree of the schema's
// elements alone.
func scan(body []byte) error {
	var m model
	if err := xmltext.Scan(body, schema, m.end); err != nil {
		return &ParseError{Err: ErrMalformedXML, Detail: err.Error()}
	}
	return nil
}

// model is what S3's model asks of a whole element, and what that needs to
// keep from one element to the next: whether RoutingRules ended holding no
// rule.
type model struct {
	emptyRules bool
}

// end checks what S3's model asks of a whole element (aws-sdk-go v1.55.8
// service/s3/api.go): RedirectAllRequestsTo, if present, is the only
// property (:43886-43889); an ErrorDocument's Key is required and at least
// one byte long (:20427-20434); a Suffix holds no slash (:27211-27222); a
// RoutingRule's Redirect is required (:40697-40702); and Protocol is http or
// https, its one enumeration (:39492, :39576, :45615-45619). The model also
// asks a Condition to hold one of its fields (:14369-14387), but an empty
// Condition is read as none: radosgw's GetBucketWebsite renders a rule
// without a condition so (RGWBWRoutingRule::dump_xml,
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_website.cc#L277-L281 at v19.2.7 and v20.2.4),
// and a configuration read from radosgw must put back. An empty RoutingRules
// is no property either, so it may stand beside RedirectAllRequestsTo: both
// SDKs write one for an empty list.
func (m *model) end(e xmltext.Closed) error {
	switch e.Name {
	case rootName:
		others := e.Has("ErrorDocument") || e.Has("IndexDocument") || e.Has("RoutingRules") && !m.emptyRules
		if e.Has("RedirectAllRequestsTo") && others {
			return errors.New("RedirectAllRequestsTo beside another property")
		}
	case "RoutingRules":
		m.emptyRules = !e.Has("RoutingRule")
	case "ErrorDocument":
		if !e.Has("Key") {
			return errors.New("an ErrorDocument without a Key")
		}
	case "Key":
		if len(e.Text) == 0 {
			return errors.New("an empty ErrorDocument Key")
		}
	case "Suffix":
		if bytes.IndexByte(e.Text, '/') >= 0 {
			return errors.New("a Suffix holding a slash")
		}
	case "RoutingRule":
		if !e.Has("Redirect") {
			return errors.New("a RoutingRule without a Redirect")
		}
	case "Protocol":
		if p := string(e.Text); p != "http" && p != "https" {
			return errors.New("the Protocol " + xmltext.Quote(p) + ", not http or https")
		}
	}
	return nil
}
