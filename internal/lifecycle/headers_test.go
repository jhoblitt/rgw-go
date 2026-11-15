package lifecycle_test

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/lifecycle"
	"github.com/jhoblitt/rgw-go/internal/tags"
)

func rules(rs ...lifecycle.Rule) lifecycle.Configuration {
	for i := range rs {
		if rs[i].Status == "" {
			rs[i].Status = "Enabled"
		}
	}
	return lifecycle.Configuration{Rules: rs}
}

func expire(id, days, date string) lifecycle.Rule {
	return lifecycle.Rule{ID: id, Expiration: lifecycle.Expiration{Days: days, Date: date}}
}

func header(date, id string) string {
	return `expiry-date="` + date + `", rule-id="` + id + `"`
}

func tagSet(ts ...tags.Tag) tags.Set { return tags.Set{Tags: ts} }

// plain is an object's name.
const plain = "logs/a"

func abort(id, days string) lifecycle.Rule {
	return lifecycle.Rule{ID: id, MPExpiration: lifecycle.Expiration{Days: days}}
}

var _ = Describe("ExpirationHeader", func() {
	DescribeTable("names the first midnight UTC at least Days after the mtime's second, when the worker first acts",
		func(mtime, want string) {
			Expect(lifecycle.ExpirationHeader(rules(expire("r", "1", "")), plain, "", tags.Set{}, utc(mtime), 0)).To(Equal(header(want, "r")))
		},
		Entry("a second before midnight", "2026-06-14T23:59:59Z", "Tue, 16 Jun 2026 00:00:00 GMT"),
		Entry("its fraction, which the header drops", "2026-06-14T23:59:59.9Z", "Tue, 16 Jun 2026 00:00:00 GMT"),
		Entry("midnight itself, where radosgw names the day after", "2026-06-15T00:00:00Z", "Tue, 16 Jun 2026 00:00:00 GMT"),
		Entry("within midnight's second, which to_time_t drops as the worker does", "2026-06-15T00:00:00.5Z", "Tue, 16 Jun 2026 00:00:00 GMT"),
		Entry("a second after midnight", "2026-06-15T00:00:01Z", "Wed, 17 Jun 2026 00:00:00 GMT"),
	)

	DescribeTable("names the day a UTC gateway's worker first finds the object due",
		func(mtime string) {
			got := lifecycle.ExpirationHeader(rules(expire("r", "3", "")), plain, "", tags.Set{}, utc(mtime), 0)
			date, err := time.Parse(`expiry-date="`+http.TimeFormat+`", rule-id="r"`, got)
			Expect(err).NotTo(HaveOccurred(), got)
			Expect(lifecycle.Expired(utc(mtime), 3, date, 0, time.UTC)).To(BeTrue(), "due at the named midnight")
			Expect(lifecycle.Expired(utc(mtime), 3, date.Add(-time.Nanosecond), 0, time.UTC)).To(BeFalse(), "and not before it")
		},
		Entry("an mtime at midnight", "2026-06-15T00:00:00Z"),
		Entry("within its second", "2026-06-15T00:00:00.999Z"),
		Entry("past it", "2026-06-15T00:00:01Z"),
		Entry("late in the day", "2026-06-15T23:59:59.5Z"),
	)

	It("gives a Date as it is", func() {
		Expect(lifecycle.ExpirationHeader(rules(expire("r", "", "2026-07-01T00:00:00Z")), plain, "", tags.Set{}, utc("2026-06-14T12:00:00Z"), 0)).To(
			Equal(header("Wed, 01 Jul 2026 00:00:00 GMT", "r")))
	})

	It("names the rule whose date is earliest", func() {
		Expect(lifecycle.ExpirationHeader(rules(expire("a", "30", ""), expire("b", "10", ""), expire("c", "", "2026-12-01T00:00:00Z")),
			plain, "", tags.Set{}, utc("2026-06-14T12:00:00Z"), 0)).To(Equal(header("Thu, 25 Jun 2026 00:00:00 GMT", "b")))
	})

	It("gives a tie to the first rule by ID", func() {
		Expect(lifecycle.ExpirationHeader(rules(expire("b", "10", ""), expire("a", "10", "")), plain, "", tags.Set{}, utc("2026-06-14T12:00:00Z"), 0)).To(
			Equal(header("Thu, 25 Jun 2026 00:00:00 GMT", "a")))
	})

	It("compares a Days date with the mtime's fraction, as radosgw's real_time does", func() {
		mtime := utc("2026-06-14T12:00:00.5Z")
		Expect(lifecycle.ExpirationHeader(rules(expire("a", "1", ""), expire("b", "", "2026-06-16T00:00:00Z")), plain, "", tags.Set{}, mtime, 0)).To(
			Equal(header("Tue, 16 Jun 2026 00:00:00 GMT", "b")), "b's date is half a second before a's")
	})

	It("takes the request's versionId, not the instance the read resolves, so a versioned bucket's current version gets the header", func() {
		Expect(lifecycle.ExpirationHeader(rules(expire("r", "1", "")), plain, "", tags.Set{}, utc("2026-06-14T12:00:00Z"), 0)).To(
			Equal(header("Tue, 16 Jun 2026 00:00:00 GMT", "r")))
	})

	It("gives none for a current object under a noncurrent-only rule", func() {
		r := lifecycle.Rule{ID: "r", NoncurExpiration: lifecycle.Expiration{Days: "1"}}
		Expect(lifecycle.ExpirationHeader(rules(r), plain, "", tags.Set{}, utc("2026-06-14T12:00:00Z"), 0)).To(BeEmpty())
	})

	DescribeTable("gives none for a request that names a version",
		func(versionID string) {
			Expect(lifecycle.ExpirationHeader(rules(expire("r", "1", "")), "logs/a", versionID, tags.Set{},
				utc("2026-06-14T12:00:00Z"), 0)).To(BeEmpty())
		},
		Entry("a version id", "v1"),
		Entry("null", "null"),
	)

	It("gives none without an enabled rule", func() {
		r := expire("r", "1", "")
		r.Status = "Disabled"
		Expect(lifecycle.ExpirationHeader(lifecycle.Configuration{Rules: []lifecycle.Rule{r}}, plain, "", tags.Set{}, utc("2026-06-14T12:00:00Z"), 0)).To(BeEmpty())
		Expect(lifecycle.ExpirationHeader(lifecycle.Configuration{}, plain, "", tags.Set{}, utc("2026-06-14T12:00:00Z"), 0)).To(BeEmpty())
	})

	It("matches the key's prefix, the filter's over the rule's", func() {
		other := expire("other", "1", "")
		other.Prefix = "data/"
		filtered := expire("filtered", "2", "")
		filtered.Prefix = "logs/"
		filtered.Filter.Prefix = "data/"
		mine := expire("mine", "3", "")
		mine.Prefix = "logs/"
		Expect(lifecycle.ExpirationHeader(rules(other, filtered, mine), plain, "", tags.Set{}, utc("2026-06-14T12:00:00Z"), 0)).To(
			Equal(header("Thu, 18 Jun 2026 00:00:00 GMT", "mine")))
	})

	DescribeTable("matches the object's tags",
		func(obj tags.Set, matches bool) {
			r := expire("r", "1", "")
			r.Filter.Tags = tagSet(tags.Tag{Key: "k", Value: "v"}, tags.Tag{Key: "z", Value: "1"})
			got := lifecycle.ExpirationHeader(rules(r), plain, "", obj, utc("2026-06-14T12:00:00Z"), 0)
			if matches {
				Expect(got).To(Equal(header("Tue, 16 Jun 2026 00:00:00 GMT", "r")))
			} else {
				Expect(got).To(BeEmpty())
			}
		},
		Entry("every tag and more", tagSet(tags.Tag{Key: "a", Value: "b"}, tags.Tag{Key: "k", Value: "v"}, tags.Tag{Key: "z", Value: "1"}), true),
		Entry("one missing", tagSet(tags.Tag{Key: "k", Value: "v"}), false),
		Entry("one that differs", tagSet(tags.Tag{Key: "k", Value: "w"}, tags.Tag{Key: "z", Value: "1"}), false),
		Entry("none", tags.Set{}, false),
		Entry("a repeated key whose second value matches, which radosgw's first-value read refuses",
			tagSet(tags.Tag{Key: "k", Value: "w"}, tags.Tag{Key: "k", Value: "v"}, tags.Tag{Key: "z", Value: "1"}), true),
	)

	It("gives no date for a rule whose Date the prefix map holds as nil, so another rule's Days give it", func() {
		Expect(lifecycle.ExpirationHeader(rules(expire("a", "", "2600-01-01T23:34:33.709551616Z"), expire("b", "5", "")),
			plain, "", tags.Set{}, utc("2026-06-14T12:00:00Z"), 0)).To(Equal(header("Sat, 20 Jun 2026 00:00:00 GMT", "b")))
		Expect(lifecycle.ExpirationHeader(rules(expire("a", "", "never")), plain, "", tags.Set{}, utc("2026-06-14T12:00:00Z"), 0)).To(BeEmpty())
	})

	DescribeTable("counts a rule only for an object whose size passes its size filter, which S3 and the worker compare strictly",
		func(gt, lt string, size uint64, counted bool) {
			r := expire("r", "1", "")
			r.Filter.SizeGT, r.Filter.SizeLT = gt, lt
			got := lifecycle.ExpirationHeader(rules(r), plain, "", tags.Set{}, utc("2026-06-14T12:00:00Z"), size)
			Expect(counted).To(Equal(rules(r).PrefixMap()[0].Op.SizeMatches(size)), "the worker's check agrees")
			if counted {
				Expect(got).To(Equal(header("Tue, 16 Jun 2026 00:00:00 GMT", "r")))
			} else {
				Expect(got).To(BeEmpty())
			}
		},
		Entry("larger than ObjectSizeGreaterThan", "100", "", uint64(101), true),
		Entry("equal to ObjectSizeGreaterThan", "100", "", uint64(100), false),
		Entry("smaller than ObjectSizeGreaterThan", "100", "", uint64(99), false),
		Entry("smaller than ObjectSizeLessThan", "", "100", uint64(99), true),
		Entry("equal to ObjectSizeLessThan", "", "100", uint64(100), false),
		Entry("larger than ObjectSizeLessThan", "", "100", uint64(101), false),
		Entry("between both", "10", "100", uint64(50), true),
		Entry("below both", "10", "100", uint64(5), false),
		Entry("a stored negative ObjectSizeGreaterThan, which every object passes", "-1", "", uint64(0), true),
		Entry("a stored ObjectSizeLessThan of 0, which no object passes", "", "0", uint64(0), false),
	)

	It("names the unfiltered rule for an object a filtered rule beside it excludes, and the filtered one for an object it covers", func() {
		large := expire("large", "1", "")
		large.Filter.SizeGT = "1000"
		mtime := utc("2026-06-14T12:00:00Z")
		c := rules(large, expire("every", "30", ""))
		Expect(lifecycle.ExpirationHeader(c, plain, "", tags.Set{}, mtime, 10)).To(Equal(header("Wed, 15 Jul 2026 00:00:00 GMT", "every")),
			"radosgw names large's day for the 10-byte object, which its worker removes on day 30")
		Expect(lifecycle.ExpirationHeader(c, plain, "", tags.Set{}, mtime, 2000)).To(Equal(header("Tue, 16 Jun 2026 00:00:00 GMT", "large")))
	})

	It("gives the date Days past real_time's range name, where radosgw's arithmetic wraps", func() {
		want := time.Date(2026, 6, 14+200001, 0, 0, 0, 0, time.UTC).Format(http.TimeFormat)
		Expect(lifecycle.ExpirationHeader(rules(expire("r", "200000", "")), plain, "", tags.Set{}, utc("2026-06-14T12:00:00Z"), 0)).To(Equal(header(want, "r")))
	})
})

var _ = Describe("the rule-id of x-amz-expiration", func() {
	DescribeTable("is the rule's ID URL-encoded (RFC 3986 unreserved characters kept), whatever bytes it holds",
		func(id, wantID string) {
			Expect(lifecycle.ExpirationHeader(rules(expire(id, "1", "")), plain, "", tags.Set{}, utc("2026-06-14T12:00:00Z"), 0)).To(
				Equal(header("Tue, 16 Jun 2026 00:00:00 GMT", wantID)))
		},
		Entry("a plain ID, unchanged", "Expire-old_logs.v1~", "Expire-old_logs.v1~"),
		Entry("an empty ID", "", ""),
		Entry("spaces", "rule one  two", "rule%20one%20%20two"),
		Entry("quotes", `a"b"`, "a%22b%22"),
		Entry("commas and equals signs", "a,b=c", "a%2Cb%3Dc"),
		Entry("a percent sign", "100%", "100%25"),
		Entry("non-ASCII", "caf\u00e9 \u65e5\u672c", "caf%C3%A9%20%E6%97%A5%E6%9C%AC"),
		Entry("a byte that is not UTF-8", "a\xffb", "a%FFb"),
		Entry("NUL, tab, CR and LF", "a\x00\t\r\nb", "a%00%09%0D%0Ab"),
		Entry("DEL", "a\x7fb", "a%7Fb"),
	)

	It("holds the rule's own expiry-date alone for an ID that forges another", func() {
		const id = `x", expiry-date="Fri, 01 Jan 2100 00:00:00 GMT`
		got := lifecycle.ExpirationHeader(rules(expire(id, "1", "")), plain, "", tags.Set{}, utc("2026-06-14T12:00:00Z"), 0)
		Expect(got).To(Equal(header("Tue, 16 Jun 2026 00:00:00 GMT", "x%22%2C%20expiry-date%3D%22Fri%2C%2001%20Jan%202100%2000%3A00%3A00%20GMT")))
		Expect(strings.Count(got, "expiry-date=")).To(Equal(1), "no second pair starts inside the ID")
		m := regexp.MustCompile(`^expiry-date="([^"]*)", rule-id="([^"]*)"$`).FindStringSubmatch(got)
		Expect(m).To(HaveLen(3), "two quoted values and nothing else, as a client's parser reads them: %s", got)
		Expect(m[1]).To(Equal("Tue, 16 Jun 2026 00:00:00 GMT"))
		Expect(url.PathUnescape(m[2])).To(Equal(id), "the ID a client decodes is the one stored")
	})
})

var _ = Describe("EncodeRuleID", func() {
	// everyByte holds each of the 256 byte values once.
	everyByte := func() string {
		b := make([]byte, 256)
		for i := range b {
			b[i] = byte(i)
		}
		return string(b)
	}

	It("keeps the RFC 3986 unreserved bytes and writes every other byte as %XX in upper-case hex", func() {
		for _, c := range []byte(everyByte()) {
			unreserved := c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || strings.IndexByte("-._~", c) >= 0
			if unreserved {
				Expect(lifecycle.EncodeRuleID(string([]byte{c}))).To(Equal(string([]byte{c})), "byte %#02x", c)
			} else {
				Expect(lifecycle.EncodeRuleID(string([]byte{c}))).To(Equal(fmt.Sprintf("%%%02X", c)), "byte %#02x", c)
			}
		}
	})

	It("writes nothing a strict HTTP parser refuses or that ends the header's quoted value", func() {
		Expect(lifecycle.EncodeRuleID(everyByte())).To(MatchRegexp(`^[A-Za-z0-9._~%-]*$`))
	})

	It("reads back through the path and the query decoder alike, whichever a client applies", func() {
		id := everyByte()
		enc := lifecycle.EncodeRuleID(id)
		Expect(url.PathUnescape(enc)).To(Equal(id), "url.PathUnescape")
		Expect(url.QueryUnescape(enc)).To(Equal(id), "url.QueryUnescape, which would turn a raw + into a space")
	})
})

var _ = Describe("AbortHeader", func() {
	It("gives the earliest DaysAfterInitiation date, counted from the midnight UTC after the mtime, and its rule", func() {
		date, id, ok := lifecycle.AbortHeader(rules(abort("a", "7"), abort("b", "3")), "logs/a", utc("2026-06-14T23:59:59.5Z"))
		Expect(ok).To(BeTrue())
		Expect(date).To(BeTemporally("==", utc("2026-06-18T00:00:00.5Z")), "the mtime's fraction, which radosgw's real_time keeps")
		Expect(id).To(Equal("b"))
	})

	It("names the midnight itself for an mtime at midnight, where radosgw names the day after", func() {
		date, _, ok := lifecycle.AbortHeader(rules(abort("a", "1")), "k", utc("2026-06-15T00:00:00Z"))
		Expect(ok).To(BeTrue())
		Expect(date).To(BeTemporally("==", utc("2026-06-16T00:00:00Z")))
	})

	It("matches the prefix only, and ignores the rule's tags", func() {
		tagged := abort("tagged", "2")
		tagged.Filter.Tags = tagSet(tags.Tag{Key: "k", Value: "v"})
		elsewhere := abort("elsewhere", "1")
		elsewhere.Filter.Prefix = "data/"
		_, id, ok := lifecycle.AbortHeader(rules(tagged, elsewhere), "logs/a", utc("2026-06-14T12:00:00Z"))
		Expect(ok).To(BeTrue())
		Expect(id).To(Equal("tagged"))
	})

	It("gives none without an enabled rule that aborts uploads", func() {
		disabled := abort("d", "1")
		disabled.Status = "Disabled"
		_, _, ok := lifecycle.AbortHeader(lifecycle.Configuration{Rules: []lifecycle.Rule{disabled, expire("e", "1", "")}}, "k", utc("2026-06-14T12:00:00Z"))
		Expect(ok).To(BeFalse())
	})

	It("gives the date past 24855 days, where radosgw's int product wraps", func() {
		date, _, ok := lifecycle.AbortHeader(rules(abort("a", "30000")), "k", utc("2026-06-14T12:00:00Z"))
		Expect(ok).To(BeTrue())
		Expect(date).To(BeTemporally("==", time.Date(2026, 6, 14+30001, 0, 0, 0, 0, time.UTC)))
	})
})
