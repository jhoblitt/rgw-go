package website

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"slices"
)

// children is S3's WebsiteConfiguration document as aws-sdk-go v1.55.8
// models it (service/s3/api.go: WebsiteConfiguration :43877-43893, its
// RoutingRules a list of RoutingRule :43892, ErrorDocument :20424-20435,
// IndexDocument :27208-27223, RedirectAllRequestsTo :39566-39577,
// RoutingRule :40688-40703, Condition :14366-14388, Redirect :39480-39514):
// the elements each element holds, each at most once but RoutingRule. A name
// with no entry is a field, which holds text alone.
var children = map[string][]string{
	rootName:                {"ErrorDocument", "IndexDocument", "RedirectAllRequestsTo", "RoutingRules"},
	"ErrorDocument":         {"Key"},
	"IndexDocument":         {"Suffix"},
	"RedirectAllRequestsTo": {"HostName", "Protocol"},
	"RoutingRules":          {"RoutingRule"},
	"RoutingRule":           {"Condition", "Redirect"},
	"Condition":             {"HttpErrorCodeReturnedEquals", "KeyPrefixEquals"},
	"Redirect":              {"HostName", "HttpRedirectCode", "Protocol", "ReplaceKeyPrefixWith", "ReplaceKeyWith"},
}

// xmlSpace is XML's S production, the only text the schema admits between
// elements.
const xmlSpace = " \t\r\n"

// frame is an open element: which of its children have started, by their
// index in children, whether a RoutingRules child ended holding no rule, and,
// for a field, its text so far.
type frame struct {
	name       string
	seen       uint8
	emptyRules bool
	text       []byte
}

// scan reads body's tokens without building a tree and refuses, as
// MalformedXML, the first that S3's schema does not admit; ParseXML's doc
// lists what that is. Every element nests no deeper than the schema allows,
// so a refused body costs a read of the tokens before the refusal, and an
// admitted one a tree of the schema's elements alone.
func scan(body []byte) error {
	d := xml.NewDecoder(bytes.NewReader(body))
	var stack []frame
	for {
		tok, err := d.RawToken()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return malformed("%.128s", err.Error())
		}
		switch t := tok.(type) {
		case xml.Directive:
			return malformed("a declaration %.32q", []byte(t))
		case xml.StartElement:
			name := t.Name.Local
			switch {
			case t.Name.Space != "":
				return malformed("the prefixed element name %.64q", writtenName(t.Name))
			case len(stack) == 0 && name != rootName:
				return malformed("the root is %.64q, not %s", name, rootName)
			case len(stack) > 0:
				if err := stack[len(stack)-1].start(name); err != nil {
					return err
				}
			}
			for _, a := range t.Attr {
				if a.Name != (xml.Name{Local: "xmlns"}) || a.Value != xmlnsS3 {
					return malformed("the attribute %.64q=%.64q on %s", writtenName(a.Name), a.Value, name)
				}
			}
			stack = append(stack, frame{name: name})
		case xml.EndElement:
			if len(stack) == 0 {
				return malformed("an end tag with no start tag")
			}
			top := &stack[len(stack)-1]
			if err := top.end(); err != nil {
				return err
			}
			if top.name == "RoutingRules" && top.seen == 0 && len(stack) > 1 {
				stack[len(stack)-2].emptyRules = true
			}
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if len(stack) == 0 {
				continue
			}
			top := &stack[len(stack)-1]
			if _, holds := children[top.name]; !holds {
				top.text = append(top.text, t...)
			} else if len(bytes.Trim(t, xmlSpace)) > 0 {
				return malformed("text %.32q in %s", []byte(t), top.name)
			}
		}
	}
}

// writtenName is a name as the document writes it, prefix included.
func writtenName(n xml.Name) string {
	if n.Space == "" {
		return n.Local
	}
	return n.Space + ":" + n.Local
}

// start takes child, which f holds, once unless it is a RoutingRule. A field
// holds no element.
func (f *frame) start(child string) error {
	i := slices.Index(children[f.name], child)
	switch {
	case i < 0:
		return malformed("an element %.64q in %s", child, f.name)
	case f.seen&(1<<i) != 0 && child != "RoutingRule":
		return malformed("a second %s in %s", child, f.name)
	}
	f.seen |= 1 << i
	return nil
}

// has reports whether f's child named child has started.
func (f *frame) has(child string) bool {
	i := slices.Index(children[f.name], child)
	return i >= 0 && f.seen&(1<<i) != 0
}

// bit is the bit in f.seen of child, one of the children f holds.
func (f *frame) bit(child string) uint8 {
	return 1 << slices.Index(children[f.name], child)
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
func (f *frame) end() error {
	switch f.name {
	case rootName:
		others := f.seen &^ f.bit("RedirectAllRequestsTo")
		if f.emptyRules {
			others &^= f.bit("RoutingRules")
		}
		if f.has("RedirectAllRequestsTo") && others != 0 {
			return malformed("RedirectAllRequestsTo beside another property")
		}
	case "ErrorDocument":
		if !f.has("Key") {
			return malformed("an ErrorDocument without a Key")
		}
	case "Key":
		if len(f.text) == 0 {
			return malformed("an empty ErrorDocument Key")
		}
	case "Suffix":
		if bytes.IndexByte(f.text, '/') >= 0 {
			return malformed("a Suffix holding a slash")
		}
	case "RoutingRule":
		if !f.has("Redirect") {
			return malformed("a RoutingRule without a Redirect")
		}
	case "Protocol":
		if p := string(f.text); p != "http" && p != "https" {
			return malformed("the Protocol %.64q, not http or https", p)
		}
	}
	return nil
}
