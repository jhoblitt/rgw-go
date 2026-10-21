package lifecycle_test

import (
	"net/http"
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
			Expect(lifecycle.ExpirationHeader(rules(expire("r", "1", "")), plain, "", tags.Set{}, utc(mtime))).To(Equal(header(want, "r")))
		},
		Entry("a second before midnight", "2026-06-14T23:59:59Z", "Tue, 16 Jun 2026 00:00:00 GMT"),
		Entry("its fraction, which the header drops", "2026-06-14T23:59:59.9Z", "Tue, 16 Jun 2026 00:00:00 GMT"),
		Entry("midnight itself, where radosgw names the day after", "2026-06-15T00:00:00Z", "Tue, 16 Jun 2026 00:00:00 GMT"),
		Entry("within midnight's second, which to_time_t drops as the worker does", "2026-06-15T00:00:00.5Z", "Tue, 16 Jun 2026 00:00:00 GMT"),
		Entry("a second after midnight", "2026-06-15T00:00:01Z", "Wed, 17 Jun 2026 00:00:00 GMT"),
	)

	DescribeTable("names the day a UTC gateway's worker first finds the object due",
		func(mtime string) {
			got := lifecycle.ExpirationHeader(rules(expire("r", "3", "")), plain, "", tags.Set{}, utc(mtime))
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
		Expect(lifecycle.ExpirationHeader(rules(expire("r", "", "2026-07-01T00:00:00Z")), plain, "", tags.Set{}, utc("2026-06-14T12:00:00Z"))).To(
			Equal(header("Wed, 01 Jul 2026 00:00:00 GMT", "r")))
	})

	It("names the rule whose date is earliest", func() {
		Expect(lifecycle.ExpirationHeader(rules(expire("a", "30", ""), expire("b", "10", ""), expire("c", "", "2026-12-01T00:00:00Z")),
			plain, "", tags.Set{}, utc("2026-06-14T12:00:00Z"))).To(Equal(header("Thu, 25 Jun 2026 00:00:00 GMT", "b")))
	})

	It("gives a tie to the first rule by ID", func() {
		Expect(lifecycle.ExpirationHeader(rules(expire("b", "10", ""), expire("a", "10", "")), plain, "", tags.Set{}, utc("2026-06-14T12:00:00Z"))).To(
			Equal(header("Thu, 25 Jun 2026 00:00:00 GMT", "a")))
	})

	It("compares a Days date with the mtime's fraction, as radosgw's real_time does", func() {
		mtime := utc("2026-06-14T12:00:00.5Z")
		Expect(lifecycle.ExpirationHeader(rules(expire("a", "1", ""), expire("b", "", "2026-06-16T00:00:00Z")), plain, "", tags.Set{}, mtime)).To(
			Equal(header("Tue, 16 Jun 2026 00:00:00 GMT", "b")), "b's date is half a second before a's")
	})

	It("takes the request's versionId, not the instance the read resolves, so a versioned bucket's current version gets the header", func() {
		Expect(lifecycle.ExpirationHeader(rules(expire("r", "1", "")), plain, "", tags.Set{}, utc("2026-06-14T12:00:00Z"))).To(
			Equal(header("Tue, 16 Jun 2026 00:00:00 GMT", "r")))
	})

	It("gives none for a current object under a noncurrent-only rule", func() {
		r := lifecycle.Rule{ID: "r", NoncurExpiration: lifecycle.Expiration{Days: "1"}}
		Expect(lifecycle.ExpirationHeader(rules(r), plain, "", tags.Set{}, utc("2026-06-14T12:00:00Z"))).To(BeEmpty())
	})

	DescribeTable("gives none for a request that names a version",
		func(versionID string) {
			Expect(lifecycle.ExpirationHeader(rules(expire("r", "1", "")), "logs/a", versionID, tags.Set{},
				utc("2026-06-14T12:00:00Z"))).To(BeEmpty())
		},
		Entry("a version id", "v1"),
		Entry("null", "null"),
	)

	It("gives none without an enabled rule", func() {
		r := expire("r", "1", "")
		r.Status = "Disabled"
		Expect(lifecycle.ExpirationHeader(lifecycle.Configuration{Rules: []lifecycle.Rule{r}}, plain, "", tags.Set{}, utc("2026-06-14T12:00:00Z"))).To(BeEmpty())
		Expect(lifecycle.ExpirationHeader(lifecycle.Configuration{}, plain, "", tags.Set{}, utc("2026-06-14T12:00:00Z"))).To(BeEmpty())
	})

	It("matches the key's prefix, the filter's over the rule's", func() {
		other := expire("other", "1", "")
		other.Prefix = "data/"
		filtered := expire("filtered", "2", "")
		filtered.Prefix = "logs/"
		filtered.Filter.Prefix = "data/"
		mine := expire("mine", "3", "")
		mine.Prefix = "logs/"
		Expect(lifecycle.ExpirationHeader(rules(other, filtered, mine), plain, "", tags.Set{}, utc("2026-06-14T12:00:00Z"))).To(
			Equal(header("Thu, 18 Jun 2026 00:00:00 GMT", "mine")))
	})

	DescribeTable("matches the object's tags",
		func(obj tags.Set, matches bool) {
			r := expire("r", "1", "")
			r.Filter.Tags = tagSet(tags.Tag{Key: "k", Value: "v"}, tags.Tag{Key: "z", Value: "1"})
			got := lifecycle.ExpirationHeader(rules(r), plain, "", obj, utc("2026-06-14T12:00:00Z"))
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
			plain, "", tags.Set{}, utc("2026-06-14T12:00:00Z"))).To(Equal(header("Sat, 20 Jun 2026 00:00:00 GMT", "b")))
		Expect(lifecycle.ExpirationHeader(rules(expire("a", "", "never")), plain, "", tags.Set{}, utc("2026-06-14T12:00:00Z"))).To(BeEmpty())
	})

	It("gives the date Days past real_time's range name, where radosgw's arithmetic wraps", func() {
		want := time.Date(2026, 6, 14+200001, 0, 0, 0, 0, time.UTC).Format(http.TimeFormat)
		Expect(lifecycle.ExpirationHeader(rules(expire("r", "200000", "")), plain, "", tags.Set{}, utc("2026-06-14T12:00:00Z"))).To(Equal(header(want, "r")))
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
