package meta_test

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/formatter"
	"github.com/jhoblitt/rgw-go/internal/meta"
)

// ceph-dencoder registers none of the website types, so their encodings are
// built here field by field from RGWBucketWebsiteConf's encode and its
// sub-structs', each ENCODE_START(1, 1) but the configuration's (2, 1)
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_website.h#L26-L243 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_website.h#L26-L243 at v20.2.4,
// the same file at both).

func redirectBytes(protocol, hostname string, code uint16) []byte {
	return encoded(func(e *denc.Encoder) {
		beginEnd(e, 1, 1, func() {
			e.String(protocol)
			e.String(hostname)
			e.U16(code)
		})
	})
}

func ruleRedirectBytes(redirect []byte, prefixWith, keyWith string) []byte {
	return encoded(func(e *denc.Encoder) {
		beginEnd(e, 1, 1, func() {
			e.Raw(redirect)
			e.String(prefixWith)
			e.String(keyWith)
		})
	})
}

func conditionBytes(prefix string, code uint16) []byte {
	return encoded(func(e *denc.Encoder) {
		beginEnd(e, 1, 1, func() {
			e.String(prefix)
			e.U16(code)
		})
	})
}

func ruleBytes(condition, redirect []byte) []byte {
	return encoded(func(e *denc.Encoder) {
		beginEnd(e, 1, 1, func() {
			e.Raw(condition)
			e.Raw(redirect)
		})
	})
}

func rulesBytes(rules ...[]byte) []byte {
	return encoded(func(e *denc.Encoder) {
		beginEnd(e, 1, 1, func() {
			e.U32(uint32(len(rules))) //nolint:gosec // a handful of rules
			for _, r := range rules {
				e.Raw(r)
			}
		})
	})
}

// websiteV1Bytes is a configuration as version 1 wrote it, before
// subdir_marker, listing_css_doc and listing_enabled.
func websiteV1Bytes(index, errorDoc string, rules, redirectAll []byte) []byte {
	return encoded(func(e *denc.Encoder) {
		beginEnd(e, 1, 1, func() {
			e.String(index)
			e.String(errorDoc)
			e.Raw(rules)
			e.Raw(redirectAll)
		})
	})
}

func websiteV2Bytes(index, errorDoc string, rules, redirectAll []byte, subdir, css string, listing bool) []byte {
	return encoded(func(e *denc.Encoder) {
		beginEnd(e, 2, 1, func() {
			e.String(index)
			e.String(errorDoc)
			e.Raw(rules)
			e.Raw(redirectAll)
			e.String(subdir)
			e.String(css)
			e.Bool(listing)
		})
	})
}

func docsRule() meta.RoutingRule {
	return meta.RoutingRule{
		Condition: meta.RoutingCondition{KeyPrefixEquals: "docs/", HTTPErrorCodeReturnedEquals: 404},
		RedirectInfo: meta.RoutingRedirect{
			Redirect:             meta.RedirectInfo{Protocol: "https", Hostname: "example.com", HTTPRedirectCode: 301},
			ReplaceKeyPrefixWith: "documents/",
		},
	}
}

func docsRuleBytes() []byte {
	return ruleBytes(conditionBytes("docs/", 404), ruleRedirectBytes(redirectBytes("https", "example.com", 301), "documents/", ""))
}

func imagesRule() meta.RoutingRule {
	return meta.RoutingRule{
		Condition:    meta.RoutingCondition{KeyPrefixEquals: "images/"},
		RedirectInfo: meta.RoutingRedirect{ReplaceKeyWith: "folderdeleted.html"},
	}
}

func imagesRuleBytes() []byte {
	return ruleBytes(conditionBytes("images/", 0), ruleRedirectBytes(redirectBytes("", "", 0), "", "folderdeleted.html"))
}

// indexSite is an index document, an error document and two routing rules,
// as PutBucketWebsite stores them.
func indexSite() meta.WebsiteConf {
	return meta.WebsiteConf{
		IndexDocSuffix: "index.html",
		ErrorDoc:       "error.html",
		RoutingRules:   meta.RoutingRules{docsRule(), imagesRule()},
	}
}

func indexSiteV2() []byte {
	return websiteV2Bytes("index.html", "error.html", rulesBytes(docsRuleBytes(), imagesRuleBytes()), redirectBytes("", "", 0), "", "", false)
}

// redirectSite redirects every request, as RedirectAllRequestsTo stores it.
func redirectSite() meta.WebsiteConf {
	return meta.WebsiteConf{RedirectAll: meta.RedirectInfo{Protocol: "https", Hostname: "example.com"}}
}

func redirectSiteV2() []byte {
	return websiteV2Bytes("", "", rulesBytes(), redirectBytes("https", "example.com", 0), "", "", false)
}

// swiftSite carries the members only Swift's container metadata sets.
func swiftSite() meta.WebsiteConf {
	return meta.WebsiteConf{
		IndexDocSuffix: "index.html",
		ErrorDoc:       "404.html",
		SubdirMarker:   "application/directory",
		ListingCSSDoc:  "listing.css",
		ListingEnabled: true,
	}
}

func swiftSiteV2() []byte {
	return websiteV2Bytes("index.html", "404.html", rulesBytes(), redirectBytes("", "", 0), "application/directory", "listing.css", true)
}

var bothReleases = []denc.Release{denc.Squid, denc.Tentacle}

var _ = Describe("WebsiteConf encoding", func() {
	It("writes RGWRedirectInfo's protocol, hostname and u16 code", func() {
		v := meta.RedirectInfo{Protocol: "https", Hostname: "example.com", HTTPRedirectCode: 302}
		for _, r := range bothReleases {
			expectOrder(v, redirectBytes("https", "example.com", 302), func(e *denc.Encoder) { v.Encode(e, r) }, meta.DecodeRedirectInfo)
		}
	})
	It("writes RGWBWRedirectInfo's redirect, replace_key_prefix_with and replace_key_with", func() {
		v := meta.RoutingRedirect{Redirect: meta.RedirectInfo{Hostname: "h"}, ReplaceKeyPrefixWith: "p/", ReplaceKeyWith: "k"}
		want := ruleRedirectBytes(redirectBytes("", "h", 0), "p/", "k")
		for _, r := range bothReleases {
			expectOrder(v, want, func(e *denc.Encoder) { v.Encode(e, r) }, meta.DecodeRoutingRedirect)
		}
	})
	It("writes RGWBWRoutingRuleCondition's key_prefix_equals and u16 http_error_code_returned_equals", func() {
		v := meta.RoutingCondition{KeyPrefixEquals: "docs/", HTTPErrorCodeReturnedEquals: 404}
		for _, r := range bothReleases {
			expectOrder(v, conditionBytes("docs/", 404), func(e *denc.Encoder) { v.Encode(e, r) }, meta.DecodeRoutingCondition)
		}
	})
	It("writes RGWBWRoutingRule's condition then redirect_info", func() {
		v := docsRule()
		for _, r := range bothReleases {
			expectOrder(v, docsRuleBytes(), func(e *denc.Encoder) { v.Encode(e, r) }, meta.DecodeRoutingRule)
		}
	})
	It("writes RGWBWRoutingRules' list in order, and reads an empty one as nil", func() {
		v := meta.RoutingRules{docsRule(), imagesRule()}
		for _, r := range bothReleases {
			expectOrder(v, rulesBytes(docsRuleBytes(), imagesRuleBytes()), func(e *denc.Encoder) { v.Encode(e, r) }, meta.DecodeRoutingRules)
			expectOrder(meta.RoutingRules(nil), rulesBytes(), func(e *denc.Encoder) { meta.RoutingRules(nil).Encode(e, r) }, meta.DecodeRoutingRules)
		}
	})
	DescribeTable("writes RGWBucketWebsiteConf as version 2 and reads it back",
		func(v meta.WebsiteConf, want []byte) {
			for _, r := range bothReleases {
				expectOrder(v, want, func(e *denc.Encoder) { v.Encode(e, r) }, meta.DecodeWebsiteConf)
			}
		},
		Entry("an index document, an error document and routing rules", indexSite(), indexSiteV2()),
		Entry("a redirect of every request", redirectSite(), redirectSiteV2()),
		Entry("Swift's listing members", swiftSite(), swiftSiteV2()),
		Entry("RGWBucketWebsiteConf()", meta.WebsiteConf{}, websiteV2Bytes("", "", rulesBytes(), redirectBytes("", "", 0), "", "", false)),
	)
	It("reads version 1 with the members it lacks at their defaults, and writes it as version 2", func() {
		v1 := websiteV1Bytes("index.html", "error.html", rulesBytes(docsRuleBytes(), imagesRuleBytes()), redirectBytes("", "", 0))
		got := decodeWhole(v1, meta.DecodeWebsiteConf)
		Expect(got).To(Equal(indexSite()))
		Expect(encodeWith(denc.Squid, got.Encode)).To(Equal(indexSiteV2()))
	})
	It("skips what a later version appends, and writes version 2", func() {
		later := reframe(swiftSiteV2(), 3, []byte("later"))
		got := decodeWhole(later, meta.DecodeWebsiteConf)
		Expect(got).To(Equal(swiftSite()))
		Expect(encodeWith(denc.Tentacle, got.Encode)).To(Equal(swiftSiteV2()))
	})
	It("skips what a later version of each sub-struct appends, read alone", func() {
		later := []byte("later")
		Expect(decodeWhole(reframe(redirectBytes("https", "example.com", 301), 2, later), meta.DecodeRedirectInfo)).To(Equal(docsRule().RedirectInfo.Redirect))
		Expect(decodeWhole(reframe(ruleRedirectBytes(redirectBytes("https", "example.com", 301), "documents/", ""), 2, later), meta.DecodeRoutingRedirect)).
			To(Equal(docsRule().RedirectInfo))
		Expect(decodeWhole(reframe(conditionBytes("docs/", 404), 2, later), meta.DecodeRoutingCondition)).To(Equal(docsRule().Condition))
		Expect(decodeWhole(reframe(docsRuleBytes(), 2, later), meta.DecodeRoutingRule)).To(Equal(docsRule()))
		Expect(decodeWhole(reframe(rulesBytes(docsRuleBytes()), 2, later), meta.DecodeRoutingRules)).To(Equal(meta.RoutingRules{docsRule()}))
	})
	DescribeTable("skips what a later version of a sub-struct appends",
		func(b []byte) {
			Expect(decodeWhole(b, meta.DecodeWebsiteConf)).To(Equal(indexSite()))
		},
		Entry("redirect_all", websiteV2Bytes("index.html", "error.html", rulesBytes(docsRuleBytes(), imagesRuleBytes()),
			reframe(redirectBytes("", "", 0), 2, []byte("later")), "", "", false)),
		Entry("the rule list", websiteV2Bytes("index.html", "error.html", reframe(rulesBytes(docsRuleBytes(), imagesRuleBytes()), 2, []byte("later")),
			redirectBytes("", "", 0), "", "", false)),
		Entry("a rule", websiteV2Bytes("index.html", "error.html", rulesBytes(reframe(docsRuleBytes(), 2, []byte("later")), imagesRuleBytes()),
			redirectBytes("", "", 0), "", "", false)),
		Entry("a condition", websiteV2Bytes("index.html", "error.html", rulesBytes(docsRuleBytes(),
			ruleBytes(reframe(conditionBytes("images/", 0), 2, []byte("later")), ruleRedirectBytes(redirectBytes("", "", 0), "", "folderdeleted.html"))),
			redirectBytes("", "", 0), "", "", false)),
		Entry("a rule's redirect", websiteV2Bytes("index.html", "error.html", rulesBytes(docsRuleBytes(),
			ruleBytes(conditionBytes("images/", 0), reframe(ruleRedirectBytes(redirectBytes("", "", 0), "", "folderdeleted.html"), 2, []byte("later")))),
			redirectBytes("", "", 0), "", "", false)),
		Entry("the redirect inside a rule's redirect", websiteV2Bytes("index.html", "error.html", rulesBytes(ruleBytes(conditionBytes("docs/", 404),
			ruleRedirectBytes(reframe(redirectBytes("https", "example.com", 301), 2, []byte("later")), "documents/", "")), imagesRuleBytes()),
			redirectBytes("", "", 0), "", "", false)),
	)
	DescribeTable("refuses a struct whose compat is past what its decoder reads",
		func(b []byte) {
			Expect(decodeErr(b, meta.DecodeWebsiteConf)).To(MatchError(denc.ErrIncompatible))
		},
		Entry("the configuration at compat 3", encoded(func(e *denc.Encoder) { beginEnd(e, 3, 3, func() { e.Raw(indexSiteV2()[6:]) }) })),
		Entry("its redirect_all at compat 2",
			websiteV2Bytes("", "", rulesBytes(), encoded(func(e *denc.Encoder) { beginEnd(e, 2, 2, func() { e.Raw(redirectBytes("", "", 0)[6:]) }) }), "", "", false)),
		Entry("its rule list at compat 2",
			websiteV2Bytes("", "", encoded(func(e *denc.Encoder) { beginEnd(e, 2, 2, func() { e.Raw(rulesBytes()[6:]) }) }), redirectBytes("", "", 0), "", "", false)),
		Entry("a rule at compat 2",
			websiteV2Bytes("", "", rulesBytes(encoded(func(e *denc.Encoder) { beginEnd(e, 2, 2, func() { e.Raw(docsRuleBytes()[6:]) }) })), redirectBytes("", "", 0), "", "", false)),
		Entry("a condition at compat 2",
			websiteV2Bytes("", "", rulesBytes(ruleBytes(encoded(func(e *denc.Encoder) { beginEnd(e, 2, 2, func() { e.Raw(conditionBytes("", 0)[6:]) }) }),
				ruleRedirectBytes(redirectBytes("", "", 0), "", ""))), redirectBytes("", "", 0), "", "", false)),
		Entry("a rule's redirect at compat 2",
			websiteV2Bytes("", "", rulesBytes(ruleBytes(conditionBytes("", 0),
				encoded(func(e *denc.Encoder) {
					beginEnd(e, 2, 2, func() { e.Raw(ruleRedirectBytes(redirectBytes("", "", 0), "", "")[6:]) })
				}))),
				redirectBytes("", "", 0), "", "", false)),
	)
	It("refuses every truncation of an encoding, without a panic", func() {
		full := indexSiteV2()
		for n := range full {
			Expect(decodeErr(full[:n], meta.DecodeWebsiteConf)).To(MatchError(denc.ErrShortBuffer), "the first %d of %d bytes", n, len(full))
		}
	})
	It("refuses a rule count past what the encoding holds, and one that runs into the next member", func() {
		hostile := encoded(func(e *denc.Encoder) {
			beginEnd(e, 1, 1, func() {
				e.U32(0xffffffff)
				e.Raw(imagesRuleBytes())
			})
		})
		Expect(decodeErr(hostile, meta.DecodeRoutingRules)).To(MatchError(denc.ErrShortBuffer))
		b := websiteV2Bytes("", "", hostile, redirectBytes("", "", 0), "", "", false)
		Expect(decodeErr(b, meta.DecodeWebsiteConf)).To(MatchError(denc.ErrOverread))
	})
	It("refuses a sub-struct that reads past its own end", func() {
		short := encoded(func(e *denc.Encoder) { beginEnd(e, 1, 1, func() { e.String("https") }) })
		b := websiteV2Bytes("", "", rulesBytes(), short, "", "", false)
		Expect(decodeErr(b, meta.DecodeWebsiteConf)).To(MatchError(denc.ErrOverread))
	})
})

// bucketWithWebsite is normalBucket carrying w.
func bucketWithWebsite(w meta.WebsiteConf) meta.BucketInfo {
	b := normalBucket()
	b.Website = &w
	return b
}

var _ = Describe("BucketInfo website", func() {
	It("writes has_website and the configuration after the tenant, and reads them back, on both releases", func() {
		for _, r := range bothReleases {
			b := bucketWithWebsite(indexSite())
			enc := encodeWith(r, b.Encode)
			Expect(bytes.Contains(enc, append([]byte("\x01\x00\x00\x00t\x01"), indexSiteV2()...))).To(BeTrue(),
				"%s: has_website follows the owner's tenant and precedes the configuration", r)
			got := decodeWhole(enc, meta.DecodeBucketInfo)
			Expect(got).To(Equal(b), "%s", r)
			Expect(got.HasWebsite()).To(BeTrue(), "%s", r)
		}
	})
	It("writes has_website false and no configuration without one", func() {
		b := normalBucket()
		Expect(b.HasWebsite()).To(BeFalse())
		got := decodeWhole(encodeWith(denc.Squid, b.Encode), meta.DecodeBucketInfo)
		Expect(got.Website).To(BeNil())
	})
	It("carries RGWBucketWebsiteConf() under has_website", func() {
		b := bucketWithWebsite(meta.WebsiteConf{})
		got := decodeWhole(encodeWith(denc.Tentacle, b.Encode), meta.DecodeBucketInfo)
		Expect(got.Website).To(Equal(&meta.WebsiteConf{}))
		Expect(got.HasWebsite()).To(BeTrue())
	})
	It("rewrites a stored version 1 configuration as version 2, as RGWBucketInfo::encode does", func() {
		b := bucketWithWebsite(indexSite())
		v2 := encodeWith(denc.Squid, b.Encode)
		v1 := websiteV1Bytes("index.html", "error.html", rulesBytes(docsRuleBytes(), imagesRuleBytes()), redirectBytes("", "", 0))
		Expect(bytes.Count(v2, indexSiteV2())).To(Equal(1))
		stored := reframe(bytes.Replace(v2, indexSiteV2(), v1, 1), 24, nil)
		got := decodeWhole(stored, meta.DecodeBucketInfo)
		Expect(got.Website).To(Equal(new(indexSite())))
		Expect(encodeWith(denc.Squid, got.Encode)).To(Equal(v2))
	})
	It("refuses a bucket whose configuration does not decode", func() {
		b := bucketWithWebsite(indexSite())
		enc := encodeWith(denc.Squid, b.Encode)
		bad := reframe(bytes.Replace(enc, indexSiteV2(), indexSiteV2()[:len(indexSiteV2())-1], 1), 24, nil)
		Expect(decodeErr(bad, meta.DecodeBucketInfo)).To(HaveOccurred())
	})
})

// The website_conf forms below are built from RGWBucketInfo::dump and
// decode_json
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L2529-L2532
// and
// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L2569-L2572 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L2591-L2594
// and
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L2628-L2631 at v20.2.4)
// and the website types' dump and decode_json
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_website.cc#L135-L212 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_website.cc#L135-L212 at v20.2.4).
var _ = Describe("BucketInfo website_conf", func() {
	dump := func(b meta.BucketInfo, rel denc.Release) (string, error) {
		f := formatter.NewJSON(false)
		f.OpenObjectSection("x")
		b.Dump(f, rel)
		f.CloseSection()
		return string(f.Bytes()), f.Err()
	}
	const indexJSON = `{"index_doc_suffix":"index.html","error_doc":"error.html","routing_rules":{"rules":[` +
		`{"condition":{"key_prefix_equals":"docs/","http_error_code_returned_equals":404},` +
		`"redirect_info":{"redirect":{"protocol":"https","hostname":"example.com","http_redirect_code":301},` +
		`"replace_key_prefix_with":"documents/","replace_key_with":""}},` +
		`{"condition":{"key_prefix_equals":"images/","http_error_code_returned_equals":0},` +
		`"redirect_info":{"redirect":{"protocol":"","hostname":"","http_redirect_code":0},` +
		`"replace_key_prefix_with":"","replace_key_with":"folderdeleted.html"}}]}}`

	DescribeTable("is written between has_website and swift_versioning, by Dump as by MarshalJSON on both releases",
		func(w meta.WebsiteConf, conf string) {
			b := bucketWithWebsite(w)
			want := `"requester_pays":true,"has_website":true,"website_conf":` + conf + `,"swift_versioning":true,`
			data := mustMarshal(b)
			Expect(data).To(ContainSubstring(want))
			for _, r := range bothReleases {
				out, err := dump(b, r)
				Expect(err).NotTo(HaveOccurred())
				Expect(out).To(ContainSubstring(want), "%s", r)
				Expect(out).To(MatchJSON(data), "%s", r)
			}
		},
		Entry("an index document, an error document and rules", indexSite(), indexJSON),
		Entry("no rules, as an empty rules array", meta.WebsiteConf{IndexDocSuffix: "index.html"},
			`{"index_doc_suffix":"index.html","error_doc":"","routing_rules":{"rules":[]}}`),
		Entry("a redirect of every request, as redirect_all alone", redirectSite(),
			`{"redirect_all":{"protocol":"https","hostname":"example.com","http_redirect_code":0}}`),
		Entry("a redirect_all with a hostname and other members, which it leaves out",
			meta.WebsiteConf{RedirectAll: meta.RedirectInfo{Hostname: "h", HTTPRedirectCode: 302}, IndexDocSuffix: "i", RoutingRules: meta.RoutingRules{docsRule()}},
			`{"redirect_all":{"protocol":"","hostname":"h","http_redirect_code":302}}`),
		Entry("a redirect_all without a hostname, which it leaves out",
			meta.WebsiteConf{RedirectAll: meta.RedirectInfo{Protocol: "https"}, ErrorDoc: "e"},
			`{"index_doc_suffix":"","error_doc":"e","routing_rules":{"rules":[]}}`),
		Entry("Swift's listing members, which it leaves out", swiftSite(),
			`{"index_doc_suffix":"index.html","error_doc":"404.html","routing_rules":{"rules":[]}}`),
	)

	It("is not written without has_website", func() {
		data := mustMarshal(normalBucket())
		Expect(data).To(ContainSubstring(`"has_website":false,"swift_versioning":true,`))
		for _, r := range bothReleases {
			out, err := dump(normalBucket(), r)
			Expect(err).NotTo(HaveOccurred())
			Expect(out).To(ContainSubstring(`"has_website":false,"swift_versioning":true,`), "%s", r)
		}
	})

	It("writes the rules as \"obj\" elements in XML, as encode_json of a list does", func() {
		f := formatter.NewXML(false, false)
		f.OpenObjectSection("w")
		meta.WebsiteConf{RoutingRules: meta.RoutingRules{imagesRule()}}.Dump(f, denc.Squid)
		f.CloseSection()
		Expect(string(f.Bytes())).To(Equal(`<w><index_doc_suffix></index_doc_suffix><error_doc></error_doc><routing_rules><rules><obj>` +
			`<condition><key_prefix_equals>images/</key_prefix_equals><http_error_code_returned_equals>0</http_error_code_returned_equals></condition>` +
			`<redirect_info><redirect><protocol></protocol><hostname></hostname><http_redirect_code>0</http_redirect_code></redirect>` +
			`<replace_key_prefix_with></replace_key_prefix_with><replace_key_with>folderdeleted.html</replace_key_with></redirect_info>` +
			`</obj></rules></routing_rules></w>`))
	})

	DescribeTable("is read back under has_website, without what the dump leaves out",
		func(w, want meta.WebsiteConf) {
			var got meta.BucketInfo
			Expect(json.Unmarshal([]byte(mustMarshal(bucketWithWebsite(w))), &got)).To(Succeed())
			Expect(got).To(Equal(bucketWithWebsite(want)))
		},
		Entry("an index document, an error document and rules", indexSite(), indexSite()),
		Entry("a redirect of every request", redirectSite(), redirectSite()),
		Entry("a redirect_all beside other members, which lose them",
			meta.WebsiteConf{RedirectAll: meta.RedirectInfo{Hostname: "h"}, IndexDocSuffix: "i", RoutingRules: meta.RoutingRules{docsRule()}},
			meta.WebsiteConf{RedirectAll: meta.RedirectInfo{Hostname: "h"}}),
		Entry("Swift's listing members, which are lost", swiftSite(), meta.WebsiteConf{IndexDocSuffix: "index.html", ErrorDoc: "404.html"}),
	)

	DescribeTable("reads redirect_all, index_doc_suffix, error_doc and routing_rules whatever the hostname, each absent one at its default",
		func(conf string, want meta.WebsiteConf) {
			var got meta.BucketInfo
			Expect(json.Unmarshal([]byte(`{"bucket":{"name":"b"},"has_website":true,"website_conf":`+conf+`}`), &got)).To(Succeed())
			Expect(got.Website).To(Equal(&want))
		},
		Entry("all four", `{"redirect_all":{"protocol":"http","hostname":"h","http_redirect_code":301},"index_doc_suffix":"i","error_doc":"e",`+
			`"routing_rules":{"rules":[{"condition":{"key_prefix_equals":"images/"},"redirect_info":{"replace_key_with":"folderdeleted.html"}}]}}`,
			meta.WebsiteConf{
				RedirectAll: meta.RedirectInfo{Protocol: "http", Hostname: "h", HTTPRedirectCode: 301}, IndexDocSuffix: "i", ErrorDoc: "e",
				RoutingRules: meta.RoutingRules{imagesRule()},
			}),
		Entry("none", `{}`, meta.WebsiteConf{}),
		Entry("the listing members, which it does not read",
			`{"subdir_marker":"m","listing_css_doc":"c","listing_enabled":true}`, meta.WebsiteConf{}),
		Entry("an empty rules array", `{"routing_rules":{"rules":[]}}`, meta.WebsiteConf{}),
		Entry("the codes in the texts decode_json_obj(int&) reads",
			`{"redirect_all":{"http_redirect_code":"302"},"routing_rules":{"rules":[{"condition":{"http_error_code_returned_equals":" 404"}}]}}`,
			meta.WebsiteConf{
				RedirectAll:  meta.RedirectInfo{HTTPRedirectCode: 302},
				RoutingRules: meta.RoutingRules{{Condition: meta.RoutingCondition{HTTPErrorCodeReturnedEquals: 404}}},
			}),
		Entry("the largest u16 codes", `{"redirect_all":{"http_redirect_code":65535},"routing_rules":{"rules":[{"condition":{"http_error_code_returned_equals":65535}}]}}`,
			meta.WebsiteConf{
				RedirectAll:  meta.RedirectInfo{HTTPRedirectCode: 65535},
				RoutingRules: meta.RoutingRules{{Condition: meta.RoutingCondition{HTTPErrorCodeReturnedEquals: 65535}}},
			}),
	)

	It("reads RGWBucketWebsiteConf() for has_website without website_conf, and nothing without has_website", func() {
		var got meta.BucketInfo
		Expect(json.Unmarshal([]byte(`{"bucket":{"name":"b"},"has_website":true}`), &got)).To(Succeed())
		Expect(got.Website).To(Equal(&meta.WebsiteConf{}))
		Expect(json.Unmarshal([]byte(`{"bucket":{"name":"b"},"has_website":false,"website_conf":{"index_doc_suffix":"i"}}`), &got)).To(Succeed())
		Expect(got.Website).To(BeNil())
		Expect(json.Unmarshal([]byte(`{"bucket":{"name":"b"},"website_conf":{"error_doc":"zzz"}}`), &got)).To(Succeed())
		Expect(got.Website).To(BeNil(), "not even to refuse it")
	})

	DescribeTable("refuses a website_conf member decode_json_obj cannot mean",
		func(conf string) {
			var got meta.BucketInfo
			Expect(json.Unmarshal([]byte(`{"has_website":true,"website_conf":`+conf+`}`), &got)).To(MatchError(meta.ErrBadJSONValue), conf)
		},
		Entry("a hostname that is no string", `{"redirect_all":{"hostname":{}}}`),
		Entry("an index document that is no string", `{"index_doc_suffix":["i"]}`),
		Entry("rules that are no array", `{"routing_rules":{"rules":{}}}`),
		Entry("a rule that is no object", `{"routing_rules":{"rules":[1]}}`),
		Entry("a redirect code past u16's range, which C++ wraps", `{"redirect_all":{"http_redirect_code":65536}}`),
		Entry("a negative redirect code, which C++ wraps", `{"redirect_all":{"http_redirect_code":-1}}`),
		Entry("an error code past int's range", `{"routing_rules":{"rules":[{"condition":{"http_error_code_returned_equals":2147483648}}]}}`),
		Entry("an error code that is no number", `{"routing_rules":{"rules":[{"condition":{"http_error_code_returned_equals":"x"}}]}}`),
		Entry("a replace_key_with that is no string", `{"routing_rules":{"rules":[{"redirect_info":{"replace_key_with":null}}]}}`),
	)
})

// dumpAt is b's Dump at rel as ceph-dencoder's dump_json writes it: one
// pretty object and a newline.
func dumpAt(b meta.BucketInfo, rel denc.Release) string {
	GinkgoHelper()
	f := formatter.NewJSON(true)
	f.OpenObjectSection("object")
	b.Dump(f, rel)
	f.CloseSection()
	Expect(f.Err()).NotTo(HaveOccurred())
	return string(f.Bytes()) + "\n"
}

// readWebsiteFixture is testdata/website/name.
func readWebsiteFixture(name string) []byte {
	GinkgoHelper()
	b, err := os.ReadFile(filepath.Join("testdata", "website", name))
	Expect(err).NotTo(HaveOccurred())
	return b
}

// testdata/website holds, for each case, <case>.bin, the spec's own Squid
// encoding of the bucket below (for "v1", with its configuration spliced
// back to version 1), and what ceph-dencoder in quay.io/ceph/ceph:v19.2.7
// and :v20.2.4 made of it: `ceph-dencoder type RGWBucketInfo import
// <case>.bin decode dump_json` as <case>.<tag>.json, and `decode encode
// export` as <case>.<tag>.reenc. The corpus holds no bucket with a website.
var _ = Describe("BucketInfo website against ceph-dencoder", func() {
	DescribeTable("decodes the input, re-encodes it as each release's dencoder does, and dumps it as each dumps it",
		func(name string, w meta.WebsiteConf) {
			want := bucketWithWebsite(w)
			bin := readWebsiteFixture(name + ".bin")
			if name != "v1" {
				Expect(bin).To(Equal(encodeWith(denc.Squid, want.Encode)), "the input is the spec's encoding")
			}
			got := decodeWhole(bin, meta.DecodeBucketInfo)
			Expect(got).To(Equal(want))
			for _, g := range []struct {
				tag string
				rel denc.Release
			}{{"v19.2.7", denc.Squid}, {"v20.2.4", denc.Tentacle}} {
				Expect(encodeWith(g.rel, got.Encode)).To(Equal(readWebsiteFixture(name+"."+g.tag+".reenc")), "%s re-encoding", g.tag)
				doc := readWebsiteFixture(name + "." + g.tag + ".json")
				Expect(dumpAt(got, g.rel)).To(Equal(string(doc)), "%s dump", g.tag)
				Expect(mustMarshal(got)).To(MatchJSON(doc), "%s JSON", g.tag)
			}
		},
		Entry("an index document, an error document and rules", "index", indexSite()),
		Entry("a stored version 1 configuration, which both write as version 2", "v1", indexSite()),
		Entry("a redirect of every request", "redirect", redirectSite()),
		Entry("a redirect_all beside other members, which the dump leaves out", "both", meta.WebsiteConf{
			RedirectAll:    meta.RedirectInfo{Protocol: "http", Hostname: "h.example", HTTPRedirectCode: 302},
			IndexDocSuffix: "i.html",
			RoutingRules:   meta.RoutingRules{docsRule()},
		}),
		Entry("Swift's listing members, which the dump leaves out and the encoding keeps", "swift", swiftSite()),
		Entry("RGWBucketWebsiteConf()", "empty", meta.WebsiteConf{}),
	)
})

// gateIndexSite is the index document, error document and routing rules
// test/integration's website gate writes: one rule redirecting a prefix at
// 302 to the request's host, one redirecting a missing key of another
// prefix to a fixed host and key at the default code.
func gateIndexSite() meta.WebsiteConf {
	return meta.WebsiteConf{
		IndexDocSuffix: "index.html",
		ErrorDoc:       "error.html",
		RoutingRules: meta.RoutingRules{
			{
				Condition:    meta.RoutingCondition{KeyPrefixEquals: "docs/"},
				RedirectInfo: meta.RoutingRedirect{Redirect: meta.RedirectInfo{HTTPRedirectCode: 302}, ReplaceKeyPrefixWith: "documents/"},
			},
			{
				Condition:    meta.RoutingCondition{KeyPrefixEquals: "gone/", HTTPErrorCodeReturnedEquals: 404},
				RedirectInfo: meta.RoutingRedirect{Redirect: meta.RedirectInfo{Protocol: "https", Hostname: "example.com"}, ReplaceKeyWith: "moved.html"},
			},
		},
	}
}

// gateRedirectSite is the website gate's redirect of every request.
func gateRedirectSite() meta.WebsiteConf {
	return meta.WebsiteConf{RedirectAll: meta.RedirectInfo{Protocol: "https", Hostname: "www.example.com"}}
}

// testdata/website/radosgw-<case>.<tag>.bin are bucket instances radosgw
// <tag> wrote, as test/integration's website gate captures them on a
// disposable cluster: index and redirect after its PutBucketWebsite, and v1
// the index one with its configuration written back by hand as version 1.
// radosgw-<case>.<tag>.json is that release's ceph-dencoder dump_json of
// it, and radosgw-v1.<tag>.reenc that ceph-dencoder's re-encoding; it
// re-encodes index and redirect to their own bytes.
var _ = Describe("BucketInfo website as radosgw wrote it", func() {
	DescribeTable("decodes radosgw's instance whole, re-encodes it as radosgw does, and dumps it as ceph-dencoder does",
		func(name, tag string, rel denc.Release, w meta.WebsiteConf) {
			bin := readWebsiteFixture("radosgw-" + name + "." + tag + ".bin")
			got := decodeWhole(bin, meta.DecodeBucketInfo)
			Expect(got.Website).To(Equal(&w), "the gate's configuration")
			want := bin
			if name == "v1" {
				// RGWBucketWebsiteConf's header, at the same offset in both
				// releases' instances of this bucket.
				Expect(bin[263:265]).To(Equal([]byte{1, 1}), "the input configuration's version and compat")
				Expect(binary.LittleEndian.Uint32(bin[265:269])).To(BeEquivalentTo(196), "the input configuration's length")
				want = readWebsiteFixture("radosgw-v1." + tag + ".reenc")
				Expect(want).To(HaveLen(len(bin)+9), "the version 2 members' 9 bytes")
			}
			Expect(encodeWith(rel, got.Encode)).To(Equal(want), "%s re-encoding", tag)
			doc := readWebsiteFixture("radosgw-" + name + "." + tag + ".json")
			Expect(dumpAt(got, rel)).To(Equal(string(doc)), "%s dump", tag)
			Expect(mustMarshal(got)).To(MatchJSON(doc), "%s JSON", tag)
		},
		Entry("Squid's index document, error document and rules", "index", "v19.2.7", denc.Squid, gateIndexSite()),
		Entry("Squid's redirect of every request", "redirect", "v19.2.7", denc.Squid, gateRedirectSite()),
		Entry("Squid's instance with the configuration at version 1, written back as version 2", "v1", "v19.2.7", denc.Squid, gateIndexSite()),
		Entry("Tentacle's index document, error document and rules", "index", "v20.2.4", denc.Tentacle, gateIndexSite()),
		Entry("Tentacle's redirect of every request", "redirect", "v20.2.4", denc.Tentacle, gateRedirectSite()),
		Entry("Tentacle's instance with the configuration at version 1, written back as version 2", "v1", "v20.2.4", denc.Tentacle, gateIndexSite()),
	)
})
