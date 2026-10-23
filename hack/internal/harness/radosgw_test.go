package harness_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing/fstest"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/hack/internal/harness"
	"github.com/jhoblitt/rgw-go/hack/internal/harness/harnessfakes"
)

// entry is a radosgw's service map entry with the gid and start_stamp given.
func entry(gid, started string) string {
	return `{"gid": ` + gid + `, "start_stamp": "` + started + `", "metadata": {"id": "ceph.objectstore.a",
	  "realm_name": "ceph-objectstore", "zonegroup_name": "ceph-objectstore", "zone_name": "ceph-objectstore",
	  "frontend_config#0": "beast port=80"}}`
}

// dump is ceph service dump -f json's output listing entries by gid.
func dump(entries ...string) string {
	daemons := []string{`"summary": ""`}
	for _, e := range entries {
		gid := strings.TrimSpace(strings.SplitN(strings.SplitN(e, ":", 2)[1], ",", 2)[0])
		daemons = append(daemons, `"`+gid+`": `+e)
	}
	return `{"services": {"rgw": {"daemons": {` + strings.Join(daemons, ", ") + `}}}}`
}

var _ = Describe("RadosgwEntries", func() {
	It("decodes each radosgw's entry in gid order, keeping its compacted text, and skips the summary", func() {
		got, err := harness.RadosgwEntries([]byte(dump(entry("4721", "2026-10-22T22:47:23.340603+0000"),
			entry("4100", "2026-10-22T22:40:00.000000+0000"))))
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(HaveLen(2))
		Expect([]int64{got[0].GID, got[1].GID}).To(Equal([]int64{4100, 4721}))
		Expect(got[1].StartStamp).To(Equal("2026-10-22T22:47:23.340603+0000"))
		Expect(got[1].Metadata).To(HaveKeyWithValue("id", "ceph.objectstore.a"))
		Expect(got[0].Raw).To(HavePrefix(`{"gid":4100,"start_stamp":"2026-10-22T22:40:00.000000+0000","metadata":{"id":`))
	})

	It("lists none for a map without radosgws", func() {
		got, err := harness.RadosgwEntries([]byte(`{"services": {}}`))
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(BeEmpty())
	})

	It("refuses output that is not JSON", func() {
		_, err := harness.RadosgwEntries([]byte("Error ETIMEDOUT"))
		Expect(err).To(MatchError(ContainSubstring("decoding ceph service dump")))
	})
})

var _ = Describe("Radosgw", func() {
	It("gives the site it serves and its frontends", func() {
		entries, err := harness.RadosgwEntries([]byte(dump(entry("4100", "t"))))
		Expect(err).NotTo(HaveOccurred())
		realm, zonegroup, zone, err := entries[0].Site()
		Expect(err).NotTo(HaveOccurred())
		Expect([]string{realm, zonegroup, zone}).To(Equal([]string{"ceph-objectstore", "ceph-objectstore", "ceph-objectstore"}))
		Expect(entries[0].Frontends()).To(Equal("beast port=80"))
	})

	It("refuses a site it does not name whole", func() {
		_, _, _, err := harness.Radosgw{Metadata: map[string]string{"realm_name": "r"}, Raw: "{raw}"}.Site()
		Expect(err).To(MatchError("the radosgw reports no realm, zonegroup or zone: {raw}"))
	})
})

var _ = Describe("Cluster's radosgw lookup", func() {
	var (
		fake  *harnessfakes.FakeExecer
		cl    harness.Cluster
		clock time.Time
		poll  harness.Poll
		// dumps are what each service dump answers in turn, the last from then on.
		dumps  []string
		sleeps int
	)

	BeforeEach(func() {
		fake = &harnessfakes.FakeExecer{}
		cl = harness.Cluster{Exec: fake, Rooket: "rooket", Release: "squid", CallTimeout: time.Minute}
		clock = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
		sleeps = 0
		poll = harness.Poll{
			Now: func() time.Time { return clock },
			Sleep: func(_ context.Context, d time.Duration) error {
				sleeps++
				clock = clock.Add(d)
				return nil
			},
			Wait: 3 * time.Minute, Every: 5 * time.Second,
		}
		dumps = nil
		fake.OutputStub = func(_ context.Context, c harness.Cmd) ([]byte, error) {
			line := strings.Join(c.Args, " ")
			switch {
			case strings.HasSuffix(line, "service dump -f json"):
				d := dumps[0]
				if len(dumps) > 1 {
					dumps = dumps[1:]
				}
				if d == "fail" {
					return nil, errors.New("exit status 110: timed out")
				}
				return []byte(d), nil
			case strings.HasSuffix(line, "config show client.rgw.ceph.objectstore.a rgw_frontends"):
				return []byte("beast port=80\n"), nil
			}
			return nil, errors.New("no answer to " + line)
		}
	})

	It("reads the service map again every poll until it lists one radosgw", func(ctx SpecContext) {
		dumps = []string{dump(entry("4100", "a"), entry("4721", "b")), dump(entry("4721", "b"))}
		rgw, err := cl.OneRadosgw(ctx, poll)
		Expect(err).NotTo(HaveOccurred())
		Expect(rgw.GID).To(BeEquivalentTo(4721))
		Expect(sleeps).To(Equal(1))
	})

	DescribeTable("fails once the wait has passed, polling until then",
		func(ctx SpecContext, answer, want string) {
			dumps = []string{answer}
			_, err := cl.OneRadosgw(ctx, poll)
			Expect(err).To(MatchError(want))
			Expect(sleeps).To(Equal(36), "a read every 5 s from 0 to 180 s")
		},
		Entry("listing none", dump(), "the service map did not settle to one radosgw in 180s (radosgw entries: []); "+
			"the manager drops a stopped radosgw within about a minute, so none is running or more than one is"),
		Entry("when ceph service dump fails", "fail", "the service map did not settle to one radosgw in 180s: "+
			"ceph service dump failed, so the service map could not be read: exit status 110: timed out"),
	)

	It("lists the entries it saw when the map holds more than one", func(ctx SpecContext) {
		dumps = []string{dump(entry("4100", "a"), entry("4721", "b"))}
		_, err := cl.OneRadosgw(ctx, poll)
		Expect(err).To(MatchError(ContainSubstring(`(radosgw entries: [{"gid":4100,"start_stamp":"a",`)))
	})

	It("names the entity the running radosgw reads, checked by its frontends", func(ctx SpecContext) {
		dumps = []string{dump(entry("4100", "a"))}
		rgw, err := cl.OneRadosgw(ctx, poll)
		Expect(err).NotTo(HaveOccurred())
		Expect(cl.RadosgwEntity(ctx, rgw)).To(Equal("client.rgw.ceph.objectstore.a"))
	})

	DescribeTable("refuses an entity it cannot check",
		func(ctx SpecContext, rgw harness.Radosgw, want string) {
			_, err := cl.RadosgwEntity(ctx, rgw)
			Expect(err).To(MatchError(ContainSubstring(want)))
		},
		Entry("with no id", harness.Radosgw{Metadata: map[string]string{}, Raw: "{raw}"}, "the radosgw reports no id: {raw}"),
		Entry("that no running daemon reads", harness.Radosgw{Metadata: map[string]string{"id": "other"}},
			"no running daemon reads the config section client.rgw.other"),
		Entry("whose daemon reports other frontends",
			harness.Radosgw{Metadata: map[string]string{"id": "ceph.objectstore.a", "frontend_config#0": "beast port=8080"}},
			"client.rgw.ceph.objectstore.a reports the frontends 'beast port=80', the radosgw 'beast port=8080'"),
	)
})

var _ = Describe("RgwGoPID", func() {
	var pidFile string

	BeforeEach(func() { pidFile = filepath.Join(GinkgoT().TempDir(), "rgw-go.pid") })

	DescribeTable("counts only a pid whose process is rgw-go",
		func(text *string, comm, want string) {
			if text != nil {
				Expect(os.WriteFile(pidFile, []byte(*text), 0o600)).To(Succeed())
			}
			proc := fstest.MapFS{"777/comm": {Data: []byte(comm)}}
			Expect(harness.RgwGoPID(proc, pidFile)).To(Equal(want))
		},
		Entry("rgw-go's", new("777\n"), "rgw-go\n", "777"),
		Entry("a reused pid's", new("777\n"), "bash\n", ""),
		Entry("a pid with no process", new("778\n"), "rgw-go\n", ""),
		Entry("a file that is not a pid", new("seven\n"), "rgw-go\n", ""),
		Entry("no pid file", nil, "rgw-go\n", ""),
	)
})
