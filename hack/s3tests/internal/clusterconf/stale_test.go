package clusterconf_test

import (
	"encoding/json"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/hack/s3tests/internal/clusterconf"
)

var _ = Describe("Reads", func() {
	DescribeTable("tells whether a config key sets the option for the entity",
		func(key string, want bool) {
			Expect(clusterconf.Reads(key, lcOption, radosgwEntity)).To(Equal(want), "key %s", key)
		},
		Entry("global, as a bare option", lcOption, true),
		Entry("global, named", "global/"+lcOption, true),
		Entry("the entity's type", "client/"+lcOption, true),
		Entry("a dotted prefix of the entity", "client.rgw/"+lcOption, true),
		Entry("the entity", radosgwEntity+"/"+lcOption, true),
		Entry("a masked key, whatever its mask", "client.rgw/host:node1/"+lcOption, true),
		Entry("another entity", rgwGoEntity+"/"+lcOption, false),
		Entry("a name the entity's only starts with, not a dotted prefix", "client.rgw.ceph.objectstore.ab/"+lcOption, false),
		Entry("another option", radosgwEntity+"/rgw_lc_debug_interval", false),
	)
})

var _ = Describe("StampTime", func() {
	DescribeTable("reads a utime_t as the mons and the manager print one",
		func(text string, want time.Time) {
			got, err := clusterconf.StampTime(text)
			Expect(err).NotTo(HaveOccurred())
			Expect(got.Equal(want)).To(BeTrue(), "%s read as %s, not %s", text, got, want)
		},
		Entry("the local time with its offset", "2026-10-08T21:58:20.402329+0000",
			time.Date(2026, 10, 8, 21, 58, 20, 402329000, time.UTC)),
		Entry("with another offset", "2026-10-08T23:58:20.402329+0200", time.Date(2026, 10, 8, 21, 58, 20, 402329000, time.UTC)),
		Entry("seconds, within ten years of the epoch", "12.000500", time.Unix(12, 500000).UTC()),
	)

	DescribeTable("refuses any other form", func(text string) {
		_, err := clusterconf.StampTime(text)
		Expect(err).To(MatchError(ContainSubstring("unparsed timestamp")))
	},
		Entry("a date alone", "2026-10-08"),
		Entry("a time without an offset", "2026-10-08T21:58:20.402329"),
		Entry("seconds without a fraction", "12"),
		Entry("nothing", ""),
	)
})

var _ = Describe("ChangedSince", func() {
	var start time.Time
	BeforeEach(func() { start = time.Date(2026, 10, 22, 11, 0, 0, 0, time.UTC) })
	set := func(at time.Time, keys ...string) map[string]any {
		var changes []map[string]string
		for _, k := range keys {
			changes = append(changes, map[string]string{"name": k})
		}
		return map[string]any{"timestamp": stamp(at), "changes": changes}
	}
	changed := func(window int, sets ...map[string]any) bool {
		log, err := json.Marshal(sets)
		Expect(err).NotTo(HaveOccurred())
		got, err := clusterconf.ChangedSince(log, window, lcOption, radosgwEntity, start)
		Expect(err).NotTo(HaveOccurred())
		return got
	}

	It("counts a change of the option for the entity after the start", func() {
		Expect(changed(10, set(start.Add(-time.Hour), "client/"+lcOption), set(start.Add(time.Second), "client.rgw/"+lcOption))).
			To(BeTrue())
	})

	It("counts no change before the start, of another option or of another entity", func() {
		Expect(changed(10,
			set(start.Add(-time.Second), radosgwEntity+"/"+lcOption),
			set(start.Add(time.Second), radosgwEntity+"/rgw_enable_gc_threads", rgwGoEntity+"/"+lcOption),
		)).To(BeFalse())
	})

	It("counts a log as full as the window, every entry after the start, as a change older than it reaches", func() {
		after := set(start.Add(time.Second), "global/mon_max_pg_per_osd")
		Expect(changed(2, after, after)).To(BeTrue())
		Expect(changed(3, after, after)).To(BeFalse(), "a log short of the window reaches back to its first change")
	})

	It("fails on a timestamp it cannot read rather than skipping the entry", func() {
		_, err := clusterconf.ChangedSince([]byte(`[{"timestamp":"yesterday","changes":[]}]`), 10, lcOption, radosgwEntity, start)
		Expect(err).To(MatchError(`the config log: unparsed timestamp "yesterday"`))
	})
})
