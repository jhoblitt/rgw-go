package website_test

import (
	"net/url"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/website"
)

func withIndex() meta.WebsiteConf { return meta.WebsiteConf{IndexDocSuffix: "index.html"} }

// docsRule redirects every key under docs/ to documents/.
func docsRule() meta.RoutingRule {
	return meta.RoutingRule{
		Condition:    meta.RoutingCondition{KeyPrefixEquals: "docs/"},
		RedirectInfo: meta.RoutingRedirect{ReplaceKeyPrefixWith: "documents/"},
	}
}

// notFoundRule redirects any key that is not found to an error page on
// another host, with its own protocol and code.
func notFoundRule() meta.RoutingRule {
	return meta.RoutingRule{
		Condition: meta.RoutingCondition{HTTPErrorCodeReturnedEquals: 404},
		RedirectInfo: meta.RoutingRedirect{
			Redirect:       meta.RedirectInfo{Protocol: "https", Hostname: "errors.example.com", HTTPRedirectCode: 307},
			ReplaceKeyWith: "404.html",
		},
	}
}

func rulesConf() meta.WebsiteConf {
	return meta.WebsiteConf{IndexDocSuffix: "index.html", RoutingRules: meta.RoutingRules{docsRule(), notFoundRule()}}
}

var _ = Describe("EffectiveKey", func() {
	DescribeTable("is get_effective_key",
		func(conf meta.WebsiteConf, key string, isFile bool, want string, wantOK bool) {
			got, ok := website.EffectiveKey(conf, key, isFile)
			Expect([]any{got, ok}).To(Equal([]any{want, wantOK}), "key %q, isFile %t", key, isFile)
		},
		Entry("no index suffix", meta.WebsiteConf{ErrorDoc: "e"}, "a", true, "", false),
		Entry("no index suffix and an empty key", meta.WebsiteConf{}, "", false, "", false),
		Entry("an empty key is the suffix", withIndex(), "", false, "index.html", true),
		Entry("an empty key is the suffix even when it is a file", withIndex(), "", true, "index.html", true),
		Entry("a key ending in a slash", withIndex(), "docs/", false, "docs/index.html", true),
		Entry("a key ending in a slash that is a file", withIndex(), "docs/", true, "docs/index.html", true),
		Entry("a key that is not a file", withIndex(), "docs", false, "docs/index.html", true),
		Entry("a key that is a file", withIndex(), "docs/a.html", true, "docs/a.html", true),
	)
})

var _ = Describe("Redirect", func() {
	type answer struct {
		URL  string
		Code int
		OK   bool
	}
	redirect := func(conf meta.WebsiteConf, key string, status int, proto, host string) answer {
		url, code, ok, err := website.Redirect(conf, key, status, proto, host)
		Expect(err).NotTo(HaveOccurred())
		return answer{url, code, ok}
	}

	DescribeTable("is should_redirect with apply_rule",
		func(conf meta.WebsiteConf, key string, status int, want answer) {
			Expect(redirect(conf, key, status, "http", "bucket.example.com")).To(Equal(want), "key %q, status %d", key, status)
		},
		Entry("no redirect-all and no rule", meta.WebsiteConf{IndexDocSuffix: "i"}, "a", 0, answer{}),
		Entry("a redirect-all, in the request's protocol", meta.WebsiteConf{RedirectAll: meta.RedirectInfo{Hostname: "example.com"}},
			"a/b.html", 0, answer{"http://example.com/a/b.html", 301, true}),
		Entry("a redirect-all in its own protocol", meta.WebsiteConf{RedirectAll: meta.RedirectInfo{Hostname: "example.com", Protocol: "https"}},
			"a", 0, answer{"https://example.com/a", 301, true}),
		Entry("a redirect-all of the empty key", meta.WebsiteConf{RedirectAll: meta.RedirectInfo{Hostname: "example.com"}},
			"", 0, answer{"http://example.com/", 301, true}),
		Entry("a redirect-all whatever the status and the rules", meta.WebsiteConf{RedirectAll: meta.RedirectInfo{Hostname: "example.com"}, RoutingRules: meta.RoutingRules{notFoundRule()}},
			"a", 404, answer{"http://example.com/a", 301, true}),
		Entry("a redirect-all without an index suffix, in the code it stores", meta.WebsiteConf{RedirectAll: meta.RedirectInfo{Hostname: "example.com", HTTPRedirectCode: 302}},
			"a", 0, answer{"http://example.com/a", 302, true}),
		Entry("a redirect-all stored beside an index suffix, at 301 whatever code it stores",
			meta.WebsiteConf{IndexDocSuffix: "i", RedirectAll: meta.RedirectInfo{Hostname: "example.com", HTTPRedirectCode: 302}},
			"a", 0, answer{"http://example.com/a", 301, true}),
		Entry("a key prefix rule, the prefix replaced", rulesConf(), "docs/a/b.html", 0, answer{"http://bucket.example.com/documents/a/b.html", 301, true}),
		Entry("a key prefix rule on the prefix alone", rulesConf(), "docs/", 0, answer{"http://bucket.example.com/documents/", 301, true}),
		Entry("a key shorter than the prefix", rulesConf(), "docs", 0, answer{}),
		Entry("a key whose prefix differs in case", rulesConf(), "Docs/a", 0, answer{}),
		Entry("a rule without an error code at an error status", meta.WebsiteConf{RoutingRules: meta.RoutingRules{docsRule()}}, "docs/a", 404, answer{}),
		Entry("a later rule with the error code when the first has none", rulesConf(), "docs/a", 404,
			answer{"https://errors.example.com/404.html", 307, true}),
		Entry("a rule with an error code at status 0", rulesConf(), "missing", 0, answer{}),
		Entry("a rule with an error code at that status, in its protocol, host and code", rulesConf(), "missing", 404,
			answer{"https://errors.example.com/404.html", 307, true}),
		Entry("a rule with an error code at another status", rulesConf(), "missing", 403, answer{}),
		Entry("an unmapped status of -1 compared as u16, as radosgw casts it",
			meta.WebsiteConf{RoutingRules: meta.RoutingRules{{Condition: meta.RoutingCondition{HTTPErrorCodeReturnedEquals: 65535}}}},
			"k", -1, answer{"http://bucket.example.com/k", 301, true}),
	)

	It("takes the first rule that matches", func() {
		first := meta.RoutingRule{RedirectInfo: meta.RoutingRedirect{ReplaceKeyWith: "first"}}
		second := meta.RoutingRule{RedirectInfo: meta.RoutingRedirect{ReplaceKeyWith: "second"}}
		conf := meta.WebsiteConf{RoutingRules: meta.RoutingRules{first, second}}
		Expect(redirect(conf, "k", 0, "http", "h")).To(Equal(answer{"http://h/first", 301, true}))
	})

	It("uses the Host as it came, an empty one included, as radosgw does", func() {
		conf := meta.WebsiteConf{RoutingRules: meta.RoutingRules{{}}}
		Expect(redirect(conf, "evil.com/x", 0, "https", "")).To(Equal(answer{"https:///evil.com/x", 301, true}))
	})
})

// location is Apply's Location for a rule it accepts.
func location(r meta.RoutingRule, proto, host, key string) string {
	GinkgoHelper()
	loc, _, err := website.Apply(r, proto, host, key)
	Expect(err).NotTo(HaveOccurred())
	return loc
}

var _ = Describe("Apply's host and protocol", func() {
	DescribeTable("declines a Location whose host holds a byte at or below 0x20 or 0x7F, or whose protocol is not http or https",
		func(r meta.RoutingRule, proto, host string) {
			loc, code, err := website.Apply(r, proto, host, "k")
			Expect(err).To(MatchError(website.ErrUnsafeRedirect))
			Expect([]any{loc, code}).To(Equal([]any{"", 0}))
			Expect(len(err.Error())).To(BeNumerically("<", 256))
			_, _, ok, err := website.Redirect(meta.WebsiteConf{RoutingRules: meta.RoutingRules{r}}, "k", 0, proto, host)
			Expect(err).To(MatchError(website.ErrUnsafeRedirect))
			Expect(ok).To(BeFalse(), "Redirect's ok beside its error")
		},
		Entry("a rule's host with CR and LF", meta.RoutingRule{RedirectInfo: meta.RoutingRedirect{Redirect: meta.RedirectInfo{Hostname: "h\r\nSet-Cookie: a=b"}}}, "http", "bucket.example.com"),
		Entry("a rule's host with a space", meta.RoutingRule{RedirectInfo: meta.RoutingRedirect{Redirect: meta.RedirectInfo{Hostname: "a b"}}}, "http", "bucket.example.com"),
		Entry("a rule's host with a control byte", meta.RoutingRule{RedirectInfo: meta.RoutingRedirect{Redirect: meta.RedirectInfo{Hostname: "a\x01b"}}}, "http", "bucket.example.com"),
		Entry("a rule's host with DEL", meta.RoutingRule{RedirectInfo: meta.RoutingRedirect{Redirect: meta.RedirectInfo{Hostname: "a\x7fb"}}}, "http", "bucket.example.com"),
		Entry("a long rule host", meta.RoutingRule{RedirectInfo: meta.RoutingRedirect{Redirect: meta.RedirectInfo{Hostname: long + " "}}}, "http", "bucket.example.com"),
		Entry("the request's host with a space", meta.RoutingRule{}, "http", "a b"),
		Entry("a rule's stored protocol other than http or https", meta.RoutingRule{RedirectInfo: meta.RoutingRedirect{Redirect: meta.RedirectInfo{Protocol: "javascript"}}}, "http", "h"),
		Entry("a rule's protocol in upper case", meta.RoutingRule{RedirectInfo: meta.RoutingRedirect{Redirect: meta.RedirectInfo{Protocol: "HTTP"}}}, "http", "h"),
		Entry("the request's protocol other than http or https", meta.RoutingRule{}, "ftp", "h"),
	)

	It("declines a redirect-all's stored host or protocol the same way", func() {
		for _, ra := range []meta.RedirectInfo{{Hostname: "h\r\nX: y"}, {Hostname: "h", Protocol: "ftp"}} {
			_, _, ok, err := website.Redirect(meta.WebsiteConf{RedirectAll: ra}, "k", 0, "http", "bucket.example.com")
			Expect(ok).To(BeFalse(), "redirect-all %+v", ra)
			Expect(err).To(MatchError(website.ErrUnsafeRedirect), "redirect-all %+v", ra)
		}
	})

	It("writes a host of other bytes as it is, non-ASCII ones included", func() {
		r := meta.RoutingRule{RedirectInfo: meta.RoutingRedirect{Redirect: meta.RedirectInfo{Hostname: "b\xc3\xbccher.example:8080", Protocol: "https"}}}
		Expect(location(r, "http", "h", "k")).To(Equal("https://b\xc3\xbccher.example:8080/k"))
	})
})

var _ = Describe("the Location's key", func() {
	DescribeTable("is percent-encoded as url_encode does with / kept, so the Location names the same key",
		func(key, want string) {
			loc := location(meta.RoutingRule{}, "http", "h", key)
			Expect(loc).To(Equal("http://h/"+want), "key %q", key)
			u, err := url.Parse(loc)
			Expect(err).NotTo(HaveOccurred())
			Expect([]string{u.Scheme, u.Host, u.Path, u.RawQuery, u.Fragment}).To(Equal([]string{"http", "h", "/" + key, "", ""}), "key %q", key)
		},
		Entry("CR and LF", "a\r\nSet-Cookie: x=y", "a%0D%0ASet-Cookie%3A%20x%3Dy"),
		Entry("a question mark and a hash", "a?b#c", "a%3Fb%23c"),
		Entry("a percent sign", "100%", "100%25"),
		Entry("a space and control characters", "a b\x01\x7f", "a%20b%01%7F"),
		Entry("a non-ASCII key", "caf\xc3\xa9", "caf%C3%A9"),
		Entry("slashes, which stay", "a/b//c/", "a/b//c/"),
		Entry("the bytes url_encode passes", "AZaz09!$'()*-._|~", "AZaz09!$'()*-._|~"),
	)

	It("is op.URLEncode's encoding, slash kept, for every byte", func() {
		all := make([]byte, 256)
		for c := range all {
			all[c] = byte(c)
		}
		loc := location(meta.RoutingRule{}, "http", "h", string(all))
		Expect(loc).To(Equal("http://h/" + op.URLEncode(string(all), false)))
	})

	It("encodes the whole new key, the rule's replacement included", func() {
		prefix := meta.RoutingRule{Condition: meta.RoutingCondition{KeyPrefixEquals: "docs/"}, RedirectInfo: meta.RoutingRedirect{ReplaceKeyPrefixWith: "new docs/"}}
		loc := location(prefix, "http", "h", "docs/a b?c")
		Expect(loc).To(Equal("http://h/new%20docs/a%20b%3Fc"))
		whole := meta.RoutingRule{RedirectInfo: meta.RoutingRedirect{ReplaceKeyWith: "gone page#1.html"}}
		loc = location(whole, "http", "h", "k")
		Expect(loc).To(Equal("http://h/gone%20page%231.html"))
	})
})

var _ = Describe("Match and Apply", func() {
	It("synthesizes the redirect-all rule with its stored code when no index suffix is stored, else 301", func() {
		conf := meta.WebsiteConf{RedirectAll: meta.RedirectInfo{Hostname: "example.com", Protocol: "https", HTTPRedirectCode: 302}}
		r, ok := website.Match(conf, "k", 0)
		want := meta.RoutingRule{RedirectInfo: meta.RoutingRedirect{Redirect: meta.RedirectInfo{Hostname: "example.com", Protocol: "https", HTTPRedirectCode: 302}}}
		Expect([]any{r, ok}).To(Equal([]any{want, true}))
		conf.IndexDocSuffix = "index.html"
		r, ok = website.Match(conf, "k", 0)
		want.RedirectInfo.Redirect.HTTPRedirectCode = 301
		Expect([]any{r, ok}).To(Equal([]any{want, true}))
	})

	It("returns the matching rule itself", func() {
		rule := meta.RoutingRule{Condition: meta.RoutingCondition{KeyPrefixEquals: "a"}, RedirectInfo: meta.RoutingRedirect{ReplaceKeyWith: "b"}}
		r, ok := website.Match(meta.WebsiteConf{RoutingRules: meta.RoutingRules{rule}}, "abc", 0)
		Expect([]any{r, ok}).To(Equal([]any{rule, true}))
	})

	type applied struct {
		URL  string
		Code int
	}
	apply := func(r meta.RoutingRule, key string) applied {
		url, code, err := website.Apply(r, "http", "h", key)
		Expect(err).NotTo(HaveOccurred())
		return applied{url, code}
	}

	DescribeTable("is apply_rule, which replaces the key's prefix by length alone",
		func(r meta.RoutingRule, key string, want applied) {
			Expect(apply(r, key)).To(Equal(want), "key %q", key)
		},
		Entry("a key that does not start with the prefix",
			meta.RoutingRule{Condition: meta.RoutingCondition{KeyPrefixEquals: "docs/"}, RedirectInfo: meta.RoutingRedirect{ReplaceKeyPrefixWith: "x/"}},
			"abcdefgh", applied{"http://h/x/fgh", 301}),
		Entry("a key shorter than the prefix",
			meta.RoutingRule{Condition: meta.RoutingCondition{KeyPrefixEquals: "docs/index"}, RedirectInfo: meta.RoutingRedirect{ReplaceKeyPrefixWith: "x/"}},
			"docs/", applied{"http://h/x/", 301}),
		Entry("ReplaceKeyWith in place of the key",
			meta.RoutingRule{Condition: meta.RoutingCondition{KeyPrefixEquals: "docs/"}, RedirectInfo: meta.RoutingRedirect{ReplaceKeyWith: "gone.html"}},
			"docs/a", applied{"http://h/gone.html", 301}),
		Entry("an empty ReplaceKeyPrefixWith, which leaves the key whole",
			meta.RoutingRule{Condition: meta.RoutingCondition{KeyPrefixEquals: "docs/"}},
			"docs/a", applied{"http://h/docs/a", 301}),
		Entry("the rule's code", meta.RoutingRule{RedirectInfo: meta.RoutingRedirect{Redirect: meta.RedirectInfo{HTTPRedirectCode: 302}}},
			"k", applied{"http://h/k", 302}),
	)
})
