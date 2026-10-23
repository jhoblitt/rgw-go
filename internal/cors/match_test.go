package cors_test

import (
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cors"
)

func oneRule(r cors.Rule) *cors.Configuration { return &cors.Configuration{Rules: []cors.Rule{r}} }

// pairs collects what Headers.All yields.
func pairs(h cors.Headers) [][2]string {
	var out [][2]string
	for k, v := range h.All() {
		out = append(out, [2]string{k, v})
	}
	return out
}

var _ = Describe("Match", func() {
	// The configuration and requests of s3-tests' test_cors_origin_response at
	// the s3-tests pin, hack/s3tests/PIN
	// (https://github.com/ceph/ceph/blob/ab228b1b9f0dc8cbc52f6dcbc649519212421c8d/src/test/rgw/s3-tests/s3tests/functional/test_s3.py#L7207-L7280).
	// Every request there is anonymous; method is
	// Access-Control-Request-Method where the test sends one, else the
	// request's own.
	Describe("test_cors_origin_response's table", func() {
		var c *cors.Configuration
		BeforeEach(func() {
			c = &cors.Configuration{Rules: []cors.Rule{
				{MaxAge: cors.MaxAgeInvalid, Methods: cors.MethodGet, AllowedOrigins: []string{"*suffix"}},
				{MaxAge: cors.MaxAgeInvalid, Methods: cors.MethodGet, AllowedOrigins: []string{"start*end"}},
				{MaxAge: cors.MaxAgeInvalid, Methods: cors.MethodGet, AllowedOrigins: []string{"prefix*"}},
				{MaxAge: cors.MaxAgeInvalid, Methods: cors.MethodPut, AllowedOrigins: []string{"*.put"}},
			}}
		})
		DescribeTable("answers each origin and method as the test expects",
			func(origin, method string, want int) {
				r := c.Match(origin, method, nil)
				if want < 0 {
					Expect(r).To(BeNil())
					return
				}
				Expect(r).To(BeIdenticalTo(&c.Rules[want]))
				Expect(pairs(r.ResponseHeaders(origin, method, nil, false))).To(Equal([][2]string{
					{"Access-Control-Allow-Origin", origin}, {"Vary", "Origin"}, {"Access-Control-Allow-Methods", method},
				}))
			},
			Entry("foo.suffix GET, a leading wildcard", "foo.suffix", "GET", 0),
			Entry("foo.bar GET, no rule", "foo.bar", "GET", -1),
			Entry("foo.get.suffix GET", "foo.get.suffix", "GET", 0),
			Entry("startend GET, an empty middle", "startend", "GET", 1),
			Entry("start1end GET", "start1end", "GET", 1),
			Entry("start12end GET", "start12end", "GET", 1),
			Entry("0start12end GET, a byte before the prefix", "0start12end", "GET", -1),
			Entry("prefix GET, a trailing wildcard with an empty tail", "prefix", "GET", 2),
			Entry("prefix.suffix GET, which the first rule takes", "prefix.suffix", "GET", 0),
			Entry("bla.prefix GET", "bla.prefix", "GET", -1),
			Entry("foo.suffix.get GET", "foo.suffix.get", "GET", -1),
			Entry("foo.suffix PUT, an origin whose rules allow only GET", "foo.suffix", "PUT", -1),
			Entry("foo.suffix DELETE", "foo.suffix", "DELETE", -1),
			Entry("foo.put PUT", "foo.put", "PUT", 3),
			Entry("foo.put GET, an origin whose rule allows only PUT", "foo.put", "GET", -1),
		)
	})

	// test_cors_origin_wildcard and test_cors_header_option
	// (https://github.com/ceph/ceph/blob/ab228b1b9f0dc8cbc52f6dcbc649519212421c8d/src/test/rgw/s3-tests/s3tests/functional/test_s3.py#L7282-L7331).
	It("answers test_cors_origin_wildcard's anonymous request with *", func() {
		c := oneRule(cors.Rule{MaxAge: cors.MaxAgeInvalid, Methods: cors.MethodGet, AllowedOrigins: []string{"*"}})
		r := c.Match("example.origin", "GET", nil)
		Expect(r).NotTo(BeNil())
		Expect(pairs(r.ResponseHeaders("example.origin", "GET", nil, false))).To(Equal([][2]string{
			{"Access-Control-Allow-Origin", "*"}, {"Access-Control-Allow-Methods", "GET"},
		}))
	})
	It("refuses test_cors_header_option's preflight for a header the rule does not allow", func() {
		c := oneRule(cors.Rule{
			MaxAge: cors.MaxAgeInvalid, Methods: cors.MethodGet, AllowedOrigins: []string{"*"},
			ExposeHeaders: []string{"x-amz-meta-header1"},
		})
		Expect(c.Match("example.origin", "GET", []string{"x-amz-meta-header2"})).To(BeNil())
	})

	// host_name_rule, the floors' choice, takes the first rule whose origin
	// matches and then tests its method and headers; main's match_rule tests
	// all three on each rule.
	Describe("where the floors' host_name_rule and main's match_rule part", func() {
		var c *cors.Configuration
		BeforeEach(func() {
			c = &cors.Configuration{Rules: []cors.Rule{
				{MaxAge: 1, Methods: cors.MethodGet, AllowedOrigins: []string{"https://app.example"}, AllowedHeaders: []string{"content-type"}},
				{MaxAge: 2, Methods: cors.MethodGet | cors.MethodPut, AllowedOrigins: []string{"https://app.example"}, AllowedHeaders: []string{"*"}},
			}}
		})
		It("takes a later rule for a method the first rule of the origin lacks", func() {
			Expect(c.Match("https://app.example", "PUT", nil)).To(BeIdenticalTo(&c.Rules[1]))
		})
		It("takes a later rule for a requested header the first rule of the origin lacks", func() {
			Expect(c.Match("https://app.example", "GET", []string{"authorization"})).To(BeIdenticalTo(&c.Rules[1]))
		})
		It("takes the first rule when it allows the request", func() {
			Expect(c.Match("https://app.example", "GET", []string{"Content-Type"})).To(BeIdenticalTo(&c.Rules[0]))
		})
	})

	DescribeTable("maps the method by its exact name, as get_cors_method_flags does",
		func(method string, matched bool) {
			c := oneRule(cors.Rule{Methods: 0xff, AllowedOrigins: []string{"*"}})
			if matched {
				Expect(c.Match("o", method, nil)).NotTo(BeNil())
			} else {
				Expect(c.Match("o", method, nil)).To(BeNil())
			}
		},
		Entry("GET", "GET", true),
		Entry("PUT", "PUT", true),
		Entry("HEAD", "HEAD", true),
		Entry("POST", "POST", true),
		Entry("DELETE", "DELETE", true),
		Entry("COPY, which no request method maps to", "COPY", false),
		Entry("a lower-case name", "get", false),
		Entry("an empty method", "", false),
		Entry("OPTIONS", "OPTIONS", false),
		Entry("a list of methods", "GET,PUT", false),
	)
	It("matches each method only to a rule that allows it", func() {
		c := oneRule(cors.Rule{Methods: cors.MethodHead, AllowedOrigins: []string{"*"}})
		Expect(c.Match("o", "HEAD", nil)).NotTo(BeNil())
		Expect(c.Match("o", "GET", nil)).To(BeNil())
	})
	It("matches no rule for an empty origin, as main's RGWCORSRule::matches", func() {
		Expect(oneRule(cors.Rule{Methods: cors.MethodGet, AllowedOrigins: []string{"*"}}).Match("", "GET", nil)).To(BeNil())
	})
	It("matches no rule in an empty configuration", func() {
		Expect((&cors.Configuration{}).Match("o", "GET", nil)).To(BeNil())
	})

	DescribeTable("matches an origin case-sensitively, against * or one wildcard",
		func(allowed, origin string, matched bool) {
			c := oneRule(cors.Rule{Methods: cors.MethodGet, AllowedOrigins: []string{allowed}})
			if matched {
				Expect(c.Match(origin, "GET", nil)).NotTo(BeNil())
			} else {
				Expect(c.Match(origin, "GET", nil)).To(BeNil())
			}
		},
		Entry("* matches any origin", "*", "https://anything", true),
		Entry("an exact origin", "http://a.example", "http://a.example", true),
		Entry("an origin in another case", "http://a.example", "http://A.example", false),
		Entry("a subdomain wildcard", "https://*.example.com", "https://x.example.com", true),
		Entry("a subdomain wildcard against another scheme", "https://*.example.com", "http://x.example.com", false),
		Entry("a subdomain wildcard against the bare domain", "https://*.example.com", "https://example.com", false),
		Entry("a prefix and suffix that would overlap", "ab*bc", "abc", false),
		Entry("a prefix and suffix that meet", "ab*bc", "abbc", true),
		Entry("whitespace in a pattern, kept literally", "* x", "foox", false),
		Entry("whitespace in a pattern, matched literally", "* x", "foo x", true),
		Entry("a stored pattern with two wildcards, never a wildcard match", "*a*", "xa", false),
		Entry("a stored pattern with two wildcards, its tail not read as a suffix", "*a*", "xa*", false),
		Entry("a stored pattern with two wildcards, still an exact match", "*a*", "*a*", true),
	)

	DescribeTable("matches a requested header ASCII-case-insensitively",
		func(allowed, requested []string, matched bool) {
			c := oneRule(cors.Rule{Methods: cors.MethodGet, AllowedOrigins: []string{"*"}, AllowedHeaders: allowed})
			if matched {
				Expect(c.Match("o", "GET", requested)).NotTo(BeNil())
			} else {
				Expect(c.Match("o", "GET", requested)).To(BeNil())
			}
		},
		Entry("no requested header against a rule that allows none", nil, nil, true),
		Entry("a requested header against a rule that allows none", nil, []string{"x"}, false),
		Entry("an allowed header in another case", []string{"Content-Type"}, []string{"content-TYPE"}, true),
		Entry("a wildcard header in another case", []string{"X-Amz-*"}, []string{"x-amz-date"}, true),
		Entry("* allows any header", []string{"*"}, []string{"anything", "else"}, true),
		Entry("every requested header must be allowed", []string{"a", "b"}, []string{"a", "c"}, false),
		Entry("a header prefix and suffix that would overlap", []string{"ab*bc"}, []string{"abc"}, false),
		Entry("a stored header pattern with two wildcards, its tail not read as a suffix", []string{"x-*-*"}, []string{"x-a-*"}, false),
		Entry("a stored header pattern with two wildcards, still an exact match in another case", []string{"X-*-*"}, []string{"x-*-*"}, true),
		Entry("a Kelvin sign, which only Unicode folding takes for k", []string{"k"}, []string{"K"}, false),
	)
})

var _ = Describe("Match, on the distinct requested headers", func() {
	var c *cors.Configuration
	BeforeEach(func() {
		c = oneRule(cors.Rule{Methods: cors.MethodGet, AllowedOrigins: []string{"*"}, AllowedHeaders: []string{"*"}})
	})
	It("matches a preflight naming MaxRequestHeaders of them", func() {
		Expect(c.Match("o", "GET", headerNames(cors.MaxRequestHeaders))).NotTo(BeNil())
	})
	It("matches no rule for a preflight naming one more", func() {
		Expect(c.Match("o", "GET", headerNames(cors.MaxRequestHeaders+1))).To(BeNil())
	})
	It("counts a header named again, or in another case, once", func() {
		hs := headerNames(cors.MaxRequestHeaders)
		hs = append(hs, hs...)
		hs = append(hs, strings.ToUpper(hs[0]))
		Expect(c.Match("o", "GET", hs)).NotTo(BeNil())
	})
})

// A preflight is unauthenticated and chooses both the bucket and the headers
// it names, so its cost is bounded whatever the bucket's configuration. The
// bound is loose: the work is milliseconds, and the unbounded matcher took
// seconds.
var _ = Describe("Match, on the cost of one preflight", func() {
	It("is bounded for 64 KiB of one repeated header against 100 rules of 251 headers", func() {
		c := &cors.Configuration{}
		for r := range 100 {
			rule := cors.Rule{MaxAge: cors.MaxAgeInvalid, Methods: cors.MethodGet, AllowedOrigins: []string{"*"}}
			for m := range 250 {
				rule.AllowedHeaders = append(rule.AllowedHeaders, fmt.Sprintf("a%02d-%04d", r, m))
			}
			rule.AllowedHeaders = append(rule.AllowedHeaders, "x*")
			if r == 99 {
				rule.AllowedHeaders = append(rule.AllowedHeaders, "z")
			}
			c.Rules = append(c.Rules, rule)
		}
		hs := cors.ParseRequestHeaders(strings.Repeat("x,", 32735) + "z")
		start := time.Now()
		got := c.Match("https://o.example", "GET", hs)
		Expect(time.Since(start)).To(BeNumerically("<", time.Second))
		Expect(got).To(BeIdenticalTo(&c.Rules[99]))
	})
	// The matcher tries each one-* AllowedHeader against each distinct
	// requested name at most once, so the work is bounded by the cap times
	// the configuration's one-* members, however the request repeats them.
	// The matcher before the cap tried each member against every name as
	// sent.
	It("tries each one-* member against each distinct requested name at most once", func() {
		c, wildcards := densest()
		got, tries := cors.MatchTries(c, "https://o.example", "GET", realisticRequest())
		Expect(got).To(BeIdenticalTo(&c.Rules[len(c.Rules)-1]))
		Expect(tries).To(BeNumerically("<=", cors.MaxRequestHeaders*wildcards))
	})
	It("refuses 64 KiB of distinct headers against that configuration, as it refuses no matching rule", func() {
		c, _ := densest()
		var hs []string
		for n, i := 0, 0; n < 65000; i++ {
			h := fmt.Sprintf("X-Amz-Meta-%04d", i)
			hs = append(hs, h)
			n += len(h) + 1
		}
		start := time.Now()
		got, tries := cors.MatchTries(c, "https://o.example", "GET", hs)
		Expect(time.Since(start)).To(BeNumerically("<", time.Second))
		Expect(got).To(BeNil())
		Expect(tries).To(BeZero())
	})
})

// realisticNames is the cap's worth of distinct header names a browser
// upload might send: X-Amz-Meta-00 to X-Amz-Meta-30, then X-Amz-Date.
func realisticNames() []string {
	out := make([]string, 0, cors.MaxRequestHeaders)
	for i := range cors.MaxRequestHeaders - 1 {
		out = append(out, fmt.Sprintf("X-Amz-Meta-%02d", i))
	}
	return append(out, "X-Amz-Date")
}

// realisticRequest is 64 KiB of realisticNames' metadata names, repeated in
// lower and upper case, then X-Amz-Date, which densest's rules refuse but
// the last.
func realisticRequest() []string {
	names := realisticNames()
	meta := names[:len(names)-1]
	var hs []string
	for n, i := 0, 0; n < 65000; i++ {
		h := meta[i%len(meta)]
		switch i / len(meta) % 3 {
		case 1:
			h = strings.ToLower(h)
		case 2:
			h = strings.ToUpper(h)
		}
		hs = append(hs, h)
		n += len(h) + 1
	}
	return append(hs, names[len(names)-1])
}

// densest is the configuration a 1 MiB PutBucketCors body holds that costs
// the most to match realisticRequest against: 100 rules, the cap
// rgw_cors_rules_max_num sets by default, each of as many two- and
// three-byte one-* AllowedHeaders as fit, none of which allows a requested
// name, then x-amz-meta-*; the last rule also allows x-amz-date. Every
// member sorts before x-amz-meta-*, so every rule but the last fails on
// X-Amz-Date only after trying every member against every distinct name.
// It returns the configuration and its count of one-* members.
func densest() (*cors.Configuration, int) {
	GinkgoHelper()
	const (
		rules     = 100
		open      = "<CORSConfiguration>"
		closing   = "</CORSConfiguration>"
		ruleOpen  = "<CORSRule><AllowedOrigin>*</AllowedOrigin><AllowedMethod>GET</AllowedMethod>"
		ruleClose = "<AllowedHeader>x-amz-meta-*</AllowedHeader></CORSRule>"
		date      = "<AllowedHeader>x-amz-date</AllowedHeader>"
		member    = len("<AllowedHeader></AllowedHeader>")
	)
	budget := (1<<20-len(open)-len(closing)-len(date))/rules - len(ruleOpen) - len(ruleClose)
	var members []string
	for _, p := range wildcardPool() {
		if budget < len(p)+member {
			break
		}
		members = append(members, p)
		budget -= len(p) + member
	}
	var b strings.Builder
	b.WriteString(open)
	for r := range rules {
		b.WriteString(ruleOpen)
		for _, p := range members {
			b.WriteString("<AllowedHeader>" + p + "</AllowedHeader>")
		}
		if r == rules-1 {
			b.WriteString(date)
		}
		b.WriteString(ruleClose)
	}
	b.WriteString(closing)
	Expect(b.Len()).To(BeNumerically("<=", 1<<20))
	c, err := cors.ParseXML([]byte(b.String()))
	Expect(err).NotTo(HaveOccurred())
	return c, rules * (len(members) + 1)
}

// wildcardPool is every two- and three-byte one-* header pattern over
// lower-case letters, digits and -, that allows none of realisticNames and
// sorts before x-amz-meta-*, shortest first.
func wildcardPool() []string {
	const chars = "-0123456789abcdefghijklmnopqrstuvwxyz"
	var two, three []string
	keep := func(out *[]string, p string) {
		if p < "x-amz-meta-*" && !allowsAny(p, realisticNames()) {
			*out = append(*out, p)
		}
	}
	for _, a := range chars {
		keep(&two, string(a)+"*")
		keep(&two, "*"+string(a))
		for _, b := range chars {
			keep(&three, string(a)+string(b)+"*")
			keep(&three, string(a)+"*"+string(b))
			keep(&three, "*"+string(a)+string(b))
		}
	}
	return append(two, three...)
}

// allowsAny reports whether the one-* pattern p allows any of names in
// ASCII lower case.
func allowsAny(p string, names []string) bool {
	prefix, suffix, _ := strings.Cut(p, "*")
	for _, n := range names {
		n = strings.ToLower(n)
		if len(n) >= len(prefix)+len(suffix) && strings.HasPrefix(n, prefix) && strings.HasSuffix(n, suffix) {
			return true
		}
	}
	return false
}

// headerNames is n distinct header names: z0, z1 and so on, then q.
func headerNames(n int) []string {
	out := make([]string, 0, n)
	for i := range n - 1 {
		out = append(out, fmt.Sprintf("z%d", i))
	}
	return append(out, "q")
}

var _ = Describe("ParseRequestHeaders", func() {
	DescribeTable("splits as get_str_vec does",
		func(v string, want []string) {
			got := cors.ParseRequestHeaders(v)
			if len(want) == 0 {
				Expect(got).To(BeEmpty())
				return
			}
			Expect(got).To(Equal(want))
		},
		Entry("one header", "X-Amz-Date", []string{"X-Amz-Date"}),
		Entry("every separator", "a, b;c=d\te", []string{"a", "b", "c", "d", "e"}),
		Entry("runs of separators", ",,a  ;;b,", []string{"a", "b"}),
		Entry("an empty value", "", nil),
		Entry("separators alone", " ,;=\t", nil),
	)
})

var _ = Describe("ResponseHeaders", func() {
	It("sends * to an anonymous request on a rule that allows every origin", func() {
		r := cors.Rule{MaxAge: cors.MaxAgeInvalid, Methods: cors.MethodGet, AllowedOrigins: []string{"https://a", "*"}}
		Expect(r.ResponseHeaders("https://a", "GET", nil, false).AllowOrigin).To(Equal("*"))
	})
	It("echoes the origin to a request with an Authorization header", func() {
		r := cors.Rule{MaxAge: cors.MaxAgeInvalid, Methods: cors.MethodGet, AllowedOrigins: []string{"*"}}
		Expect(pairs(r.ResponseHeaders("https://a", "GET", nil, true))).To(Equal([][2]string{
			{"Access-Control-Allow-Origin", "https://a"}, {"Vary", "Origin"}, {"Access-Control-Allow-Methods", "GET"},
		}))
	})
	It("echoes the origin to an anonymous request on a rule without a bare *", func() {
		r := cors.Rule{MaxAge: cors.MaxAgeInvalid, Methods: cors.MethodGet, AllowedOrigins: []string{"https://*"}}
		Expect(r.ResponseHeaders("https://a", "GET", nil, false).AllowOrigin).To(Equal("https://a"))
	})
	It("allows the requested headers the rule allows, as sent and comma-joined", func() {
		r := cors.Rule{MaxAge: cors.MaxAgeInvalid, AllowedOrigins: []string{"*"}, AllowedHeaders: []string{"x-foo", "Content-*"}}
		h := r.ResponseHeaders("o", "PUT", []string{"X-Foo", "x-bar", "content-type", "X-Foo"}, true)
		Expect(h.AllowHeaders).To(Equal("X-Foo,content-type,X-Foo"))
	})
	It("escapes a newline and a carriage return in the expose headers", func() {
		r := cors.Rule{MaxAge: cors.MaxAgeInvalid, AllowedOrigins: []string{"*"}, ExposeHeaders: []string{"a\nb", "c\r\nSet-Cookie: x"}}
		Expect(r.ResponseHeaders("o", "GET", nil, true).ExposeHeaders).To(Equal(`a\nb,c\r\nSet-Cookie: x`))
	})
	It("escapes every other control byte but a tab in the expose headers", func() {
		r := cors.Rule{MaxAge: cors.MaxAgeInvalid, AllowedOrigins: []string{"*"}, ExposeHeaders: []string{"a\x00b\x01\x1b\x1f\x7f\tc\x80"}}
		Expect(r.ResponseHeaders("o", "GET", nil, true).ExposeHeaders).To(Equal(`a\x00b\x01\x1b\x1f\x7f` + "\tc\x80"))
	})
	It("escapes a DEL a PutBucketCors body carries as a reference", func() {
		c, err := cors.ParseXML(doc(rule(origin, el("ExposeHeader", "a&#127;b"))))
		Expect(err).NotTo(HaveOccurred())
		Expect(c.Rules[0].ExposeHeaders).To(Equal([]string{"a\x7fb"}))
		Expect(c.Rules[0].ResponseHeaders("o", "GET", nil, true).ExposeHeaders).To(Equal(`a\x7fb`))
	})
	It("allows none of the requested headers when they name more than MaxRequestHeaders", func() {
		r := cors.Rule{MaxAge: cors.MaxAgeInvalid, AllowedOrigins: []string{"*"}, AllowedHeaders: []string{"*"}}
		Expect(r.ResponseHeaders("o", "GET", headerNames(cors.MaxRequestHeaders), true).AllowHeaders).
			To(Equal(strings.Join(headerNames(cors.MaxRequestHeaders), ",")))
		Expect(r.ResponseHeaders("o", "GET", headerNames(cors.MaxRequestHeaders+1), true).AllowHeaders).To(BeEmpty())
	})
	It("joins the expose headers as format_exp_headers does, with no comma before the first non-empty one", func() {
		r := cors.Rule{MaxAge: cors.MaxAgeInvalid, AllowedOrigins: []string{"*"}, ExposeHeaders: []string{"", "a", "", "b"}}
		Expect(r.ResponseHeaders("o", "GET", nil, true).ExposeHeaders).To(Equal("a,,b"))
	})
	It("sends every header in dump_access_control's order", func() {
		r := cors.Rule{MaxAge: 0, AllowedOrigins: []string{"o"}, AllowedHeaders: []string{"*"}, ExposeHeaders: []string{"e1", "e2"}}
		Expect(pairs(r.ResponseHeaders("o", "DELETE", []string{"h1", "h2"}, false))).To(Equal([][2]string{
			{"Access-Control-Allow-Origin", "o"},
			{"Vary", "Origin"},
			{"Access-Control-Allow-Methods", "DELETE"},
			{"Access-Control-Allow-Headers", "h1,h2"},
			{"Access-Control-Expose-Headers", "e1,e2"},
			{"Access-Control-Max-Age", "0"},
		}))
	})
})

var _ = Describe("Headers.All", func() {
	It("sends nothing without an origin, as dump_access_control", func() {
		Expect(pairs(cors.Headers{AllowMethods: "GET", ExposeHeaders: "e", MaxAge: 5})).To(BeEmpty())
	})
	It("sends no Vary for *, and leaves out what is empty or invalid", func() {
		Expect(pairs(cors.Headers{AllowOrigin: "*", MaxAge: cors.MaxAgeInvalid})).To(Equal([][2]string{
			{"Access-Control-Allow-Origin", "*"},
		}))
	})
	It("stops when the loop does", func() {
		h := cors.Headers{AllowOrigin: "o", AllowMethods: "GET", AllowHeaders: "h", ExposeHeaders: "e", MaxAge: 1}
		for n := range 6 {
			seen := 0
			for range h.All() {
				if seen == n {
					break
				}
				seen++
			}
			Expect(seen).To(Equal(n))
		}
	})
	It("names in HeaderNames every header it can send, in its order", func() {
		h := cors.Headers{AllowOrigin: "o", AllowMethods: "GET", AllowHeaders: "h", ExposeHeaders: "e", MaxAge: 1}
		var names []string
		for name := range h.All() {
			names = append(names, name)
		}
		Expect(cors.HeaderNames()).To(Equal(names))
	})
})
