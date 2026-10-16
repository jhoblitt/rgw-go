package website_test

import (
	"bytes"
	"errors"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/website"
)

// FuzzParseXML reads arbitrary bodies as a PutBucketWebsite document, which
// any client authorized for the bucket can send. A parse must not panic. A
// refusal must be a *website.ParseError naming one of the three sentinels,
// whose Message is one radosgw sends and whose text stays short. A document it
// accepts must render to one it reads back as the same configuration, which
// renders to the same bytes. go test runs the seeds; go test
// -fuzz=FuzzParseXML explores from them.
func FuzzParseXML(f *testing.F) {
	for _, seed := range []string{
		"",
		string(doc(index)),
		string(doc("<RedirectAllRequestsTo><HostName>example.com</HostName><Protocol>https</Protocol></RedirectAllRequestsTo>")),
		string(doc(index + "<ErrorDocument><Key>e&amp;<![CDATA[<x>]]></Key></ErrorDocument>" + routingRules(
			routingRule("<Condition><KeyPrefixEquals>docs/</KeyPrefixEquals><HttpErrorCodeReturnedEquals> 404</HttpErrorCodeReturnedEquals></Condition>"+
				"<Redirect><Protocol>http</Protocol><HostName>h</HostName><HttpRedirectCode>+302</HttpRedirectCode><ReplaceKeyPrefixWith>d/</ReplaceKeyPrefixWith></Redirect>"),
			routingRule("<Redirect><ReplaceKeyWith/></Redirect>"),
		))),
		string(doc(index + nRules(4))),
		string(doc("<RedirectAllRequestsTo><HostName></HostName></RedirectAllRequestsTo>")),
		string(doc("<IndexDocument><Suffix/></IndexDocument>")),
		string(doc(index + routingRules(routingRule("<Redirect><ReplaceKeyWith>a</ReplaceKeyWith><ReplaceKeyPrefixWith>b</ReplaceKeyPrefixWith></Redirect>")))),
		string(doc(redirAll + index)),
		string(doc(index + "<ErrorDocument><Key/></ErrorDocument>")),
		"<WebsiteConfiguration>\n <IndexDocument> <Suffix>i</Suffix> </IndexDocument>\n <!-- c --> </WebsiteConfiguration>",
		`<!DOCTYPE a [<!ENTITY e "x">]><WebsiteConfiguration>&e;</WebsiteConfiguration>`,
		`<s3:WebsiteConfiguration xmlns:s3="` + s3NS + `"/>`,
		"\xef\xbb\xbf<?xml version=\"1.0\"?><WebsiteConfiguration xmlns=\"\"><A><B><C><D><E/></D></C></B></A></WebsiteConfiguration>",
	} {
		f.Add([]byte(seed), 3)
	}
	f.Fuzz(func(t *testing.T, body []byte, maxRules int) {
		conf, err := website.ParseXML(body, maxRules)
		if err != nil {
			checkRefusal(t, err)
			return
		}
		out := website.MarshalS3XML(conf)
		again, err := website.ParseXML(out, maxRules)
		if err != nil {
			t.Fatalf("%+v renders as %q, which is refused: %v", conf, out, err)
		}
		if !reflect.DeepEqual(again, conf) {
			t.Fatalf("%+v renders as %q, which reads as %+v", conf, out, again)
		}
		if out2 := website.MarshalS3XML(again); !bytes.Equal(out2, out) {
			t.Fatalf("%q reads back and renders as %q", out, out2)
		}
	})
}

// FuzzLocation builds the Location of a redirect-all for an arbitrary request
// key, which any client chooses. The Location must hold no byte a header
// field or a URI does not carry raw, and must parse back to the same host and
// the same key, with no query or fragment. It also applies a rule naming an
// arbitrary host (checkRuleHost).
func FuzzLocation(f *testing.F) {
	for _, seed := range []string{"", "a/b", "a\r\nSet-Cookie: x=y", "a?b#c", "100%", "//evil.com", "caf\xc3\xa9"} {
		f.Add(seed, "")
		f.Add(seed, "h\r\nSet-Cookie: a=b")
	}
	conf := meta.WebsiteConf{RedirectAll: meta.RedirectInfo{Hostname: "example.com"}}
	f.Fuzz(func(t *testing.T, key, host string) {
		checkRuleHost(t, key, host)
		loc, _, ok, err := website.Redirect(conf, key, 0, "https", "bucket.example.com")
		if !ok || err != nil {
			t.Fatalf("a redirect-all does not redirect %q: %v", key, err)
		}
		for i := range len(loc) {
			if c := loc[i]; c <= 0x20 || c >= 0x7f {
				t.Fatalf("the Location of %q holds the byte %#x: %q", key, c, loc)
			}
		}
		u, err := url.Parse(loc)
		if err != nil {
			t.Fatalf("the Location of %q, %q, does not parse: %v", key, loc, err)
		}
		if u.Scheme != "https" || u.Host != "example.com" || u.Path != "/"+key || u.RawQuery != "" || u.Fragment != "" {
			t.Fatalf("the Location of %q, %q, reads as %#v", key, loc, u)
		}
	})
}

// checkRuleHost applies a rule naming host, which a bucket's owner chooses,
// or radosgw stored. A host holding a byte at or below 0x20, or 0x7F, must be
// declined; any other must leave no such byte in the Location.
func checkRuleHost(t *testing.T, key, host string) {
	t.Helper()
	r := meta.RoutingRule{RedirectInfo: meta.RoutingRedirect{Redirect: meta.RedirectInfo{Hostname: host}}}
	loc, _, err := website.Apply(r, "https", "bucket.example.com", key)
	if strings.ContainsFunc(host, func(c rune) bool { return c <= 0x20 || c == 0x7f }) {
		if !errors.Is(err, website.ErrUnsafeRedirect) {
			t.Fatalf("the host %q gives the Location %q, %v", host, loc, err)
		}
		return
	}
	if err != nil {
		t.Fatalf("the host %q is declined: %v", host, err)
	}
	for i := range len(loc) {
		if c := loc[i]; c <= 0x20 || c == 0x7f {
			t.Fatalf("the Location for host %q holds the byte %#x: %q", host, c, loc)
		}
	}
}

// maxRefusalText bounds a refusal's Error(), which may quote the body.
const maxRefusalText = 512

var tooManyRulesMessage = regexp.MustCompile(`^[0-9]+ routing rules provided, the number of routing rules in a website configuration is limited to [0-9]+\.$`)

func checkRefusal(t *testing.T, err error) {
	t.Helper()
	pe, ok := errors.AsType[*website.ParseError](err)
	if !ok {
		t.Fatalf("refusal %v is a %T, not a *website.ParseError", err, err)
	}
	if len(err.Error()) > maxRefusalText {
		t.Fatalf("refusal text is %d bytes: %.80q", len(err.Error()), err.Error())
	}
	switch {
	case errors.Is(err, website.ErrMalformedXML):
		if pe.Message != "" {
			t.Fatalf("MalformedXML carries the message %q", pe.Message)
		}
	case errors.Is(err, website.ErrInvalidArgument):
		switch pe.Message {
		case "", msgNoHost, msgNoIndex, msgSuffixMissing:
		default:
			t.Fatalf("InvalidArgument carries the message %q", pe.Message)
		}
	case errors.Is(err, website.ErrTooManyRules):
		if !tooManyRulesMessage.MatchString(pe.Message) {
			t.Fatalf("InvalidRequest carries the message %q", pe.Message)
		}
	default:
		t.Fatalf("refusal %v names no website sentinel", err)
	}
}
