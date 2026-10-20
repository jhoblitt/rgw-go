package lifecycle_test

import (
	"math"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/lifecycle"
	"github.com/jhoblitt/rgw-go/internal/tags"
)

func prefixes(m []lifecycle.PrefixOp) []string {
	out := make([]string, len(m))
	for i, p := range m {
		out[i] = p.Prefix + " " + p.Op.ID
	}
	return out
}

var _ = Describe("PrefixMap", func() {
	It("holds one op per rule, ordered by prefix and, for one prefix, by rule ID", func() {
		c := lifecycle.Configuration{Rules: []lifecycle.Rule{
			{ID: "a", Prefix: "z/", Status: "Enabled"},
			{ID: "b", Prefix: "m/", Status: "Enabled", Filter: lifecycle.Filter{Prefix: "a/"}},
			{ID: "c", Prefix: "a/", Status: "Enabled"},
			{ID: "d", Status: "Enabled", Filter: lifecycle.Filter{Flags: lifecycle.FlagArchiveZone}},
		}}
		Expect(prefixes(c.PrefixMap())).To(Equal([]string{" d", "a/ b", "a/ c", "z/ a"}))
	})

	It("takes the filter's prefix over the rule's", func() {
		m := lifecycle.Configuration{Rules: []lifecycle.Rule{{ID: "r", Prefix: "rule/", Filter: lifecycle.Filter{Prefix: "filter/"}}}}.PrefixMap()
		Expect(m).To(HaveLen(1))
		Expect(m[0].Prefix).To(Equal("filter/"))
	})

	It("reads every action, condition and flag of a rule as _add_rule does", func() {
		date := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		m := lifecycle.Configuration{Rules: []lifecycle.Rule{{
			ID:               "r",
			Status:           "Enabled",
			Expiration:       lifecycle.Expiration{Date: "2026-01-01T00:00:00Z"},
			NoncurExpiration: lifecycle.Expiration{Days: "7", NewerNoncurrent: "3"},
			MPExpiration:     lifecycle.Expiration{Days: "2"},
			DMExpiration:     true,
			Filter: lifecycle.Filter{
				Tags:   tags.Set{Tags: []tags.Tag{{Key: "k", Value: "v"}}},
				Flags:  lifecycle.FlagArchiveZone,
				SizeGT: " 100",
				SizeLT: "2000x",
			},
			Transitions: map[string]lifecycle.Transition{
				"":     {Days: "30"},
				"COLD": {Date: "2026-01-01", StorageClass: "COLD"},
			},
			NoncurTransitions: map[string]lifecycle.Transition{"WARM": {Days: "5", StorageClass: "WARM"}},
		}}}.PrefixMap()
		Expect(m).To(Equal([]lifecycle.PrefixOp{{Prefix: "", Op: lifecycle.Op{
			ID:               "r",
			Enabled:          true,
			DMExpiration:     true,
			ExpirationDate:   &date,
			NoncurExpiration: 7,
			NewerNoncurrent:  3,
			MPExpiration:     2,
			SizeGT:           new(int64(100)),
			SizeLT:           new(int64(2000)),
			Tags:             &tags.Set{Tags: []tags.Tag{{Key: "k", Value: "v"}}},
			Flags:            lifecycle.FlagArchiveZone,
			Transitions: map[string]lifecycle.TransitionAction{
				"":     {Days: 30, StorageClass: "STANDARD"},
				"COLD": {Days: -1, Date: &date, StorageClass: "COLD"},
			},
			NoncurTransitions: map[string]lifecycle.TransitionAction{"WARM": {Days: 5, StorageClass: "WARM"}},
		}}}))
	})

	It("leaves out what a rule does not set", func() {
		m := lifecycle.Configuration{Rules: []lifecycle.Rule{{ID: "r", Status: "Disabled", Expiration: lifecycle.Expiration{Days: "9"}}}}.PrefixMap()
		Expect(m).To(Equal([]lifecycle.PrefixOp{{Op: lifecycle.Op{ID: "r", Expiration: 9}}}))
	})

	It("reads numbers as atoi and stoull do", func() {
		m := lifecycle.Configuration{Rules: []lifecycle.Rule{{
			ID:               "r",
			NoncurExpiration: lifecycle.Expiration{Days: "x", NewerNoncurrent: "-1"},
			Filter:           lifecycle.Filter{SizeGT: "none", SizeLT: "18446744073709551615"},
		}}}.PrefixMap()
		Expect(m).To(HaveLen(1))
		op := m[0].Op
		Expect(op.NoncurExpiration).To(Equal(0), "atoi of no digits")
		Expect(op.NewerNoncurrent).To(Equal(uint64(math.MaxUint64)), "atoi's -1 held in a uint64_t, a count that never acts")
		Expect(op.SizeGT).To(Equal(new(int64(0))), "stoull throws on no digits and get_size_gt answers 0")
		Expect(op.SizeLT).To(Equal(new(int64(math.MaxInt64))), "a size past MaxInt64 is held at it")
		m = lifecycle.Configuration{Rules: []lifecycle.Rule{{ID: "r", Filter: lifecycle.Filter{SizeGT: "99999999999999999999", SizeLT: "-99999999999999999999"}}}}.PrefixMap()
		Expect(m).To(HaveLen(1))
		Expect(m[0].Op.SizeGT).To(Equal(new(int64(0))), "stoull throws out_of_range past 2^64-1 and get_size_gt answers 0")
		Expect(m[0].Op.SizeLT).To(Equal(new(int64(0))), "and so on a negative one past it")
	})

	It("reads a stored NewerNoncurrentVersions of 0 as one never set, which holds back no version", func() {
		for _, newer := range []string{"0", ""} {
			m := lifecycle.Configuration{Rules: []lifecycle.Rule{{
				ID: "r", NoncurExpiration: lifecycle.Expiration{Days: "1", NewerNoncurrent: newer},
			}}}.PrefixMap()
			Expect(m).To(HaveLen(1))
			Expect(m[0].Op.NewerNoncurrent).To(Equal(uint64(0)), newer)
		}
	})

	DescribeTable("reads a stored size by its sign, where radosgw's stoull reads -n as 2^64 - n",
		func(stored string, want int64) {
			m := lifecycle.Configuration{Rules: []lifecycle.Rule{{ID: "r", Filter: lifecycle.Filter{SizeLT: stored}}}}.PrefixMap()
			Expect(m).To(HaveLen(1))
			Expect(m[0].Op.SizeLT).To(Equal(&want))
		},
		Entry("-1", "-1", int64(-1)),
		Entry("white space and a sign before the digits, as strtoull skips them", " \t-20x", int64(-20)),
		Entry("-0, which is no negative", "-0", int64(0)),
		Entry("the most negative int64", "-9223372036854775808", int64(math.MinInt64)),
		Entry("past it, held at it", "-9223372036854775809", int64(math.MinInt64)),
		Entry("a plus sign", "+7", int64(7)),
	)

	DescribeTable("matches an object's size as pass_size_limit_checks does, each bound compared signed",
		func(gt, lt *int64, size uint64, want bool) {
			Expect(lifecycle.Op{SizeGT: gt, SizeLT: lt}.SizeMatches(size)).To(Equal(want))
		},
		Entry("no bound", nil, nil, uint64(0), true),
		Entry("larger than GT", new(int64(10)), nil, uint64(11), true),
		Entry("at GT", new(int64(10)), nil, uint64(10), false),
		Entry("smaller than LT", nil, new(int64(10)), uint64(9), true),
		Entry("at LT", nil, new(int64(10)), uint64(10), false),
		Entry("a negative GT, which every object passes where radosgw's passes none", new(int64(-1)), nil, uint64(0), true),
		Entry("a negative GT and a huge object", new(int64(-1)), nil, uint64(math.MaxUint64), true),
		Entry("a negative LT, which no object passes where radosgw's passes every one", nil, new(int64(-1)), uint64(0), false),
		Entry("an LT of 0", nil, new(int64(0)), uint64(0), false),
		Entry("both bounds", new(int64(1)), new(int64(3)), uint64(2), true),
		Entry("both bounds, above them", new(int64(1)), new(int64(3)), uint64(3), false),
	)

	It("copies the rule's tags, so editing an Op leaves the configuration as it was", func() {
		c := lifecycle.Configuration{Rules: []lifecycle.Rule{{ID: "r", Filter: lifecycle.Filter{
			Tags: tags.Set{Tags: []tags.Tag{{Key: "k", Value: "v"}}},
		}}}}
		m := c.PrefixMap()
		Expect(m).To(HaveLen(1))
		m[0].Op.Tags.Tags[0].Value = "changed"
		Expect(c.Rules[0].Filter.Tags.Tags).To(Equal([]tags.Tag{{Key: "k", Value: "v"}}))
	})

	It("reads a stored configuration's numbers as radosgw's worker does, though Parse would refuse them", func() {
		stored := lifecycle.Configuration{Rules: []lifecycle.Rule{{
			ID:           "r",
			Status:       "Enabled",
			Expiration:   lifecycle.Expiration{Days: " 5 days"},
			MPExpiration: lifecycle.Expiration{Days: "+2x"},
			Filter:       lifecycle.Filter{SizeGT: "10", SizeLT: "9 bytes"},
			Transitions:  map[string]lifecycle.Transition{"COLD": {Days: "1e3", StorageClass: "COLD"}},
		}}}
		_, err := lifecycle.Parse(lifecycle.Marshal(stored), lifecycle.DefaultMaxRules)
		Expect(err).To(MatchError(lifecycle.ErrMalformedXML), "PUT holds these numbers to S3's types")
		c, err := lifecycle.Decode(encode(stored, lifecycle.Configuration.Encode, denc.Squid))
		Expect(err).NotTo(HaveOccurred())
		m := c.PrefixMap()
		Expect(m).To(HaveLen(1))
		op := m[0].Op
		Expect(op.Expiration).To(Equal(5), "atoi skips the space and stops at the first non-digit")
		Expect(op.MPExpiration).To(Equal(2))
		Expect(op.SizeGT).To(Equal(new(int64(10))))
		Expect(op.SizeLT).To(Equal(new(int64(9))), "stoull stops at the first non-digit")
		Expect(op.Transitions).To(Equal(map[string]lifecycle.TransitionAction{"COLD": {Days: 1, StorageClass: "COLD"}}))
	})

	DescribeTable("holds no date for a stored date from_iso_8601 refuses or real_time cannot hold",
		func(stored string) {
			c, err := lifecycle.Decode(encode(lifecycle.Configuration{Rules: []lifecycle.Rule{{
				ID: "r", Status: "Enabled", Expiration: lifecycle.Expiration{Date: stored},
				Transitions: map[string]lifecycle.Transition{"COLD": {Date: stored, StorageClass: "COLD"}},
			}}}, lifecycle.Configuration.Encode, denc.Squid))
			Expect(err).NotTo(HaveOccurred())
			m := c.PrefixMap()
			Expect(m).To(HaveLen(1))
			Expect(m[0].Op.ExpirationDate).To(BeNil())
			Expect(m[0].Op.Transitions).To(Equal(map[string]lifecycle.TransitionAction{"COLD": {Days: -1, StorageClass: "COLD"}}))
		},
		Entry("text that is no date", "never"),
		Entry("radosgw's crafted wrap, which from_iso_8601 makes 2015-06-14T00:00:00Z and check_date takes",
			"2600-01-01T23:34:33.709551616Z"),
		Entry("a midnight past real_time's last instant", "2554-07-22"),
		Entry("a day before the epoch, which from_iso_8601 wraps forward", "1970-01-00"),
	)

	DescribeTable("reads a stored date as from_iso_8601 reads it",
		func(stored string, want time.Time) {
			m := lifecycle.Configuration{Rules: []lifecycle.Rule{{ID: "r", Expiration: lifecycle.Expiration{Date: stored}}}}.PrefixMap()
			Expect(m).To(HaveLen(1))
			Expect(m[0].Op.ExpirationDate).To(Equal(&want))
		},
		Entry("fractional seconds", "2026-06-01T12:30:00.25Z", time.Date(2026, 6, 1, 12, 30, 0, 250_000_000, time.UTC)),
		Entry("an hour alone", "2026-06-01T12Z", time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)),
		Entry("a month alone", "2026-06", time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)),
		Entry("the 31st of April, which carries into May", "2026-04-31", time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)),
		Entry("a time ended by white space", "2026-06-01T12:30Z later", time.Date(2026, 6, 1, 12, 30, 0, 0, time.UTC)),
	)

	It("is empty for an empty configuration", func() {
		Expect(lifecycle.Configuration{}.PrefixMap()).To(BeEmpty())
	})
})
