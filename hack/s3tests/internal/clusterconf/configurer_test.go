package clusterconf_test

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/hack/internal/harness"
	"github.com/jhoblitt/rgw-go/hack/s3tests/internal/clusterconf"
)

// patchTo is the rgwConfig patch that sets radosgw's option to value.
func patchTo(value string) string {
	return `kubectl -n rook-ceph patch cephobjectstore ceph-objectstore --type merge -p ` +
		`{"spec":{"gateway":{"rgwConfig":{"rgw_enable_lc_threads":"` + value + `"}}}}`
}

const restartRadosgw = "kubectl -n rook-ceph rollout restart deploy -l app=rook-ceph-rgw"

// upLine is make rgw-go-up's event, with the option rgw-go takes and the
// serve flags it restarts with.
func (w *world) upLine(value string, flags ...string) string {
	return "RGW_GO_LC_THREADS=" + value + " make -C " + w.root + " rgw-go-up RELEASE=squid ROOKET=rooket RGW_GO_FLAGS=" +
		strings.Join(flags, " ")
}

// inMode puts the world in the mode whose workers radosgw and rgwGo say,
// each gateway started after its option last changed.
func (w *world) inMode(radosgw, rgwGo string) {
	w.rgwConfig = radosgw
	w.change(radosgwEntity, radosgw, -90*time.Minute)
	w.change(rgwGoEntity, rgwGo, -90*time.Minute)
	w.radosgws[0].lc = radosgw
}

var _ = Describe("ParseMode", func() {
	DescribeTable("names the gateways whose workers each mode runs",
		func(name string, radosgw, rgwGo bool) {
			m, err := clusterconf.ParseMode(name)
			Expect(err).NotTo(HaveOccurred())
			Expect([]bool{m.Radosgw(), m.RgwGo()}).To(Equal([]bool{radosgw, rgwGo}), "mode %s", name)
		},
		Entry("off, the parity mode, runs neither", "off", false, false),
		Entry("radosgw runs radosgw's alone", "radosgw", true, false),
		Entry("rgw-go runs rgw-go's alone", "rgw-go", false, true),
		Entry("both runs both", "both", true, true),
	)

	It("refuses any other name", func() {
		_, err := clusterconf.ParseMode("on")
		Expect(err).To(MatchError(`--mode must be off, radosgw, rgw-go or both, not "on"`))
	})
})

var _ = Describe("Configurer.Apply", func() {
	var w *world

	BeforeEach(func() { w = newWorld() })

	DescribeTable("from the parity mode, runs each gateway's worker exactly where the mode says",
		func(ctx SpecContext, mode clusterconf.Mode, radosgw, rgwGo string, changes func() []string) {
			st, err := w.apply(ctx, mode)
			Expect(err).NotTo(HaveOccurred(), w.trace())
			Expect(w.changes()).To(Equal(changes()), w.trace())
			Expect([]string{st.RadosgwLC, st.RgwGoLC}).To(Equal([]string{radosgw, rgwGo}))
			Expect(w.radosgws[len(w.radosgws)-1].lc).To(Equal(radosgw), "what the running radosgw read")
			Expect(w.mons[rgwGoEntity]).To(Equal(rgwGo), "what the running rgw-go read")
			Expect(st.RgwGoPID).To(Equal(w.rgwGoPid))
		},
		Entry("off changes nothing", clusterconf.ModeOff, "false", "false", func() []string { return nil }),
		Entry("radosgw sets rgwConfig and restarts the radosgw, and leaves rgw-go alone",
			clusterconf.ModeRadosgw, "true", "false", func() []string {
				return []string{patchTo("true"), restartRadosgw}
			}),
		Entry("rgw-go restarts rgw-go with its option, and leaves the radosgw alone",
			clusterconf.ModeRgwGo, "false", "true", func() []string { return []string{w.upLine("true")} }),
		Entry("both does both", clusterconf.ModeBoth, "true", "true", func() []string {
			return []string{patchTo("true"), restartRadosgw, w.upLine("true")}
		}),
	)

	It("restores the parity mode from both", func(ctx SpecContext) {
		w.inMode("true", "true")
		st, err := w.apply(ctx, clusterconf.ModeOff)
		Expect(err).NotTo(HaveOccurred(), w.trace())
		Expect(w.changes()).To(Equal([]string{patchTo("false"), restartRadosgw, w.upLine("false")}))
		Expect([]string{st.RadosgwLC, st.RgwGoLC}).To(Equal([]string{"false", "false"}))
	})

	It("waits for Rook to reconcile the patch before it judges the radosgw", func(ctx SpecContext) {
		w.reconcileAfter = 4
		_, err := w.apply(ctx, clusterconf.ModeRadosgw)
		Expect(err).NotTo(HaveOccurred(), w.trace())
		Expect(w.changes()).To(Equal([]string{patchTo("true"), restartRadosgw}),
			"the radosgw restarts once Rook has written the option, so it reads it")
		Expect(w.radosgws[len(w.radosgws)-1].lc).To(Equal("true"))
	})

	It("leaves a radosgw that started after its option changed alone", func(ctx SpecContext) {
		w.inMode("true", "false")
		_, err := w.apply(ctx, clusterconf.ModeRadosgw)
		Expect(err).NotTo(HaveOccurred(), w.trace())
		Expect(w.changes()).To(BeEmpty())
	})

	It("restarts a radosgw that started before its option changed, though rgwConfig and the mons agree", func(ctx SpecContext) {
		w.inMode("true", "false")
		w.radosgws[0].lc = "false"
		w.change(radosgwEntity, "true", -time.Minute)
		_, err := w.apply(ctx, clusterconf.ModeRadosgw)
		Expect(err).NotTo(HaveOccurred(), w.trace())
		Expect(w.changes()).To(Equal([]string{restartRadosgw}))
		Expect(w.radosgws[len(w.radosgws)-1].lc).To(Equal("true"))
	})

	It("sets the radosgw's entity to what rgwConfig holds when only the mons differ, and restarts it", func(ctx SpecContext) {
		w.inMode("true", "false")
		w.change(radosgwEntity, "false", -2*time.Hour+time.Minute)
		w.radosgws[0].lc = "false"
		_, err := w.apply(ctx, clusterconf.ModeRadosgw)
		Expect(err).NotTo(HaveOccurred(), w.trace())
		Expect(w.changes()).To(Equal([]string{"ceph config set " + radosgwEntity + " " + lcOption + " true", restartRadosgw}))
	})

	It("fails, naming the object store's state, when Rook does not reconcile the patch", func(ctx SpecContext) {
		w.stuck = true
		_, err := w.apply(ctx, clusterconf.ModeRadosgw)
		Expect(err).To(MatchError(`setting radosgw's rgw_enable_lc_threads: the Rook operator did not reconcile the object store ` +
			`ceph-objectstore in 300s: generation 2, observed 1, phase "Ready": reconciling`))
		Expect(w.changes()).To(Equal([]string{patchTo("true")}), "no restart under the old option")
	})

	It("fails when the restarted radosgw never lists as started after the change", func(ctx SpecContext) {
		w.inMode("true", "false")
		w.radosgws[0].lc = "false"
		w.change(radosgwEntity, "true", -time.Minute)
		w.exec.OutputStub = func(ctx context.Context, c harness.Cmd) ([]byte, error) {
			if strings.Contains(c.String(), "rollout restart") {
				w.events = append(w.events, "kubectl -n rook-ceph rollout restart (ignored)")
				return nil, nil
			}
			return w.output(ctx, c)
		}
		_, err := w.apply(ctx, clusterconf.ModeRadosgw)
		Expect(err).To(MatchError(ContainSubstring(
			"the radosgw in the service map started before its rgw_enable_lc_threads changed, 180s after its restart")))
	})

	It("fails when the service map does not settle to one radosgw", func(ctx SpecContext) {
		w.radosgws = append(w.radosgws, instance{gid: w.gid(), started: w.clock, lc: "false"})
		w.lingering = 1 << 20
		_, err := w.apply(ctx, clusterconf.ModeOff)
		Expect(err).To(MatchError(ContainSubstring("the service map did not settle to one radosgw in 180s (radosgw entries: [")))
		Expect(w.changes()).To(BeEmpty())
	})

	Context("rgw-go", func() {
		It("starts rgw-go, with no serve flags, when none runs and the mode runs its worker", func(ctx SpecContext) {
			w.stopRgwGo()
			st, err := w.apply(ctx, clusterconf.ModeRgwGo)
			Expect(err).NotTo(HaveOccurred(), w.trace())
			Expect(w.changes()).To(Equal([]string{w.upLine("true")}))
			Expect(st.RgwGoPID).To(Equal("702"))
		})

		It("restarts rgw-go with the serve flags it was started with", func(ctx SpecContext) {
			w.startRgwGo("--rados-completions=pipe", "--metrics-addr=127.0.0.1:9999")
			_, err := w.apply(ctx, clusterconf.ModeRgwGo)
			Expect(err).NotTo(HaveOccurred(), w.trace())
			Expect(w.changes()).To(Equal([]string{w.upLine("true", "--rados-completions=pipe", "--metrics-addr=127.0.0.1:9999")}))
			Expect(w.rgwGoFlags).To(Equal([]string{"--rados-completions=pipe", "--metrics-addr=127.0.0.1:9999"}))
		})

		It("refuses to restart an rgw-go started with a flag RGW_GO_FLAGS cannot carry, changing nothing", func(ctx SpecContext) {
			w.startRgwGo("--rgw-frontends=beast port=80")
			_, err := w.apply(ctx, clusterconf.ModeBoth)
			Expect(err).To(MatchError(`setting rgw-go's rgw_enable_lc_threads: the running rgw-go was started with the ` +
				`serve flag "--rgw-frontends=beast port=80", which RGW_GO_FLAGS cannot carry to make rgw-go-up: restart it ` +
				`with hack/rooket/rgw-go-up.sh squid once the mode is set`))
			Expect(w.changes()).To(BeEmpty())
		})

		DescribeTable("refuses to restart an rgw-go whose serve flags are not recorded, before either half changes",
			func(ctx SpecContext, from func(), mode clusterconf.Mode) {
				from()
				Expect(os.Remove(filepath.Join(w.out(), "rgw-go.flags"))).To(Succeed())
				rgwConfig, radosgws := w.rgwConfig, len(w.radosgws)
				_, err := w.apply(ctx, mode)
				Expect(err).To(MatchError(ContainSubstring("reading the serve flags the running rgw-go was started with")))
				Expect(err).To(MatchError(ContainSubstring("restart it with make rgw-go-up RELEASE=squid first")))
				Expect(w.changes()).To(BeEmpty(), "no patch, no restart of either gateway")
				Expect([]any{w.rgwConfig, len(w.radosgws)}).To(Equal([]any{rgwConfig, radosgws}))
			},
			Entry("both, from the parity mode", func() {}, clusterconf.ModeBoth),
			Entry("off, from both", func() { w.inMode("true", "true") }, clusterconf.ModeOff),
			Entry("rgw-go, from the parity mode", func() {}, clusterconf.ModeRgwGo),
		)

		It("starts no rgw-go when none runs and the mode runs no worker of its", func(ctx SpecContext) {
			w.stopRgwGo()
			w.mons[rgwGoEntity] = "true"
			st, err := w.apply(ctx, clusterconf.ModeRadosgw)
			Expect(err).NotTo(HaveOccurred(), w.trace())
			Expect(w.changes()).To(Equal([]string{patchTo("true"), restartRadosgw}))
			Expect(st.RgwGoPID).To(BeEmpty())
			Expect(st.Lines()[1]).To(Equal("rgw-go client.rgw.rgw-go: not running"))
		})

		It("does not count a pid whose process is not rgw-go", func(ctx SpecContext) {
			w.proc[w.rgwGoPid+"/comm"].Data = []byte("bash\n")
			_, err := w.apply(ctx, clusterconf.ModeRgwGo)
			Expect(err).NotTo(HaveOccurred(), w.trace())
			Expect(w.changes()).To(Equal([]string{w.upLine("true")}))
		})

		DescribeTable("fails when make rgw-go-up does not leave the rgw-go asked for",
			func(ctx SpecContext, leave func(), want string) {
				w.exec.OutputStub = func(ctx context.Context, c harness.Cmd) ([]byte, error) {
					if c.Name == "make" {
						leave()
						return nil, nil
					}
					return w.output(ctx, c)
				}
				_, err := w.apply(ctx, clusterconf.ModeRgwGo)
				Expect(err).To(MatchError("setting rgw-go's rgw_enable_lc_threads: " + want))
			},
			Entry("running with another option", func() {},
				"make rgw-go-up left client.rgw.rgw-go's rgw_enable_lc_threads false, not true"),
			Entry("running at all", func() { w.stopRgwGo() }, "make rgw-go-up left no rgw-go running against rgw-go-squid"),
		)
	})

	It("fails when the restarted radosgw still runs with another option", func(ctx SpecContext) {
		w.exec.OutputStub = func(ctx context.Context, c harness.Cmd) ([]byte, error) {
			out, err := w.output(ctx, c)
			if strings.Contains(c.String(), "rollout restart") {
				w.radosgws[len(w.radosgws)-1].lc = "false"
			}
			return out, err
		}
		_, err := w.apply(ctx, clusterconf.ModeRadosgw)
		Expect(err).To(MatchError("setting radosgw's rgw_enable_lc_threads: the radosgw client.rgw.ceph.objectstore.a " +
			"runs with rgw_enable_lc_threads false, not true"))
	})

	It("adds no storage class", func(ctx SpecContext) {
		w.zonegroupClasses = []string{"COMP_LZ4", "STANDARD"}
		_, err := w.apply(ctx, clusterconf.ModeRadosgw)
		Expect(err).NotTo(HaveOccurred(), w.trace())
		Expect(w.changes()).To(Equal([]string{patchTo("true"), restartRadosgw}))
	})

	It("reports what each gateway runs with, a line each", func(ctx SpecContext) {
		st, err := w.apply(ctx, clusterconf.ModeOff)
		Expect(err).NotTo(HaveOccurred(), w.trace())
		Expect(st.Lines()).To(Equal([]string{
			"radosgw client.rgw.ceph.objectstore.a: rgw_enable_lc_threads false, started 2026-10-22T11:00:00.000000+0000",
			"rgw-go client.rgw.rgw-go: rgw_enable_lc_threads false, pid 701",
		}))
	})
})

var _ = Describe("Configurer.EnsureClasses", func() {
	var w *world

	BeforeEach(func() {
		w = newWorld()
		w.zonegroupClasses = []string{"COMP_LZ4", "STANDARD"}
		w.periodClasses = []string{"COMP_LZ4", "STANDARD"}
		w.zoneClasses = map[string]string{"STANDARD": "", "COMP_LZ4": "lz4"}
		w.stopRgwGo()
	})

	ensure := func(ctx context.Context) (clusterconf.ClassState, error) { return w.configurer().EnsureClasses(ctx) }

	It("adds each to the zonegroup and then the zone on STANDARD's pool, and commits the period once", func(ctx SpecContext) {
		gid := w.radosgws[0].gid
		st, err := ensure(ctx)
		Expect(err).NotTo(HaveOccurred(), w.trace())
		Expect(w.changes()).To(Equal([]string{
			"operator zonegroup placement add --placement-id default-placement --storage-class LUKEWARM",
			"operator zone placement add --placement-id default-placement --storage-class LUKEWARM --data-pool " + dataPool,
			"operator zonegroup placement add --placement-id default-placement --storage-class FROZEN",
			"operator zone placement add --placement-id default-placement --storage-class FROZEN --data-pool " + dataPool,
			"operator period update --commit",
		}), "and touches neither gateway's LC option")
		Expect(st.Line()).To(Equal("storage classes LUKEWARM, FROZEN in default-placement on " + dataPool + ": added"))
		Expect(w.radosgws[len(w.radosgws)-1].gid).NotTo(Equal(gid), "the radosgw reloaded the period")
	})

	It("changes nothing when the zonegroup, the zone and the period hold them", func(ctx SpecContext) {
		w.zonegroupClasses = append(w.zonegroupClasses, "FROZEN", "LUKEWARM")
		w.periodClasses = w.zonegroupClasses
		w.zoneClasses["LUKEWARM"], w.zoneClasses["FROZEN"] = "", ""
		st, err := ensure(ctx)
		Expect(err).NotTo(HaveOccurred(), w.trace())
		Expect(w.changes()).To(BeEmpty())
		Expect(st.Line()).To(HaveSuffix(": present"))
	})

	It("commits a period that lacks classes the zonegroup and zone hold", func(ctx SpecContext) {
		w.zonegroupClasses = append(w.zonegroupClasses, "FROZEN", "LUKEWARM")
		w.zoneClasses["LUKEWARM"], w.zoneClasses["FROZEN"] = "", ""
		_, err := ensure(ctx)
		Expect(err).NotTo(HaveOccurred(), w.trace())
		Expect(w.changes()).To(Equal([]string{"operator period update --commit"}))
	})

	It("warns, restarting nothing, when it adds them under a running rgw-go", func(ctx SpecContext) {
		w.startRgwGo()
		var logs strings.Builder
		slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
		_, err := ensure(ctx)
		Expect(err).NotTo(HaveOccurred(), w.trace())
		Expect(w.changes()).NotTo(ContainElement(HavePrefix("RGW_GO_LC_THREADS=")))
		Expect(logs.String()).To(ContainSubstring(
			`level=WARN msg="the running rgw-go reads the zone only when it starts; restart it with make rgw-go-up" pid=702`))
	})

	DescribeTable("refuses a placement it cannot add the classes to as asked",
		func(ctx SpecContext, edit func(), want string) {
			edit()
			_, err := ensure(ctx)
			Expect(err).To(MatchError(ContainSubstring(want)))
		},
		Entry("a zonegroup with no default placement", func() { w.noDefaultPlacement = true },
			"zonegroup ceph-objectstore has no default placement"),
		Entry("a zone with no STANDARD data pool", func() { delete(w.zoneClasses, "STANDARD") },
			"zone ceph-objectstore has no STANDARD data pool in placement default-placement"),
		Entry("a class the zone keeps compressed", func() {
			w.zonegroupClasses = append(w.zonegroupClasses, "FROZEN", "LUKEWARM")
			w.periodClasses = w.zonegroupClasses
			w.zoneClasses["LUKEWARM"], w.zoneClasses["FROZEN"] = "zlib", ""
		}, "zone ceph-objectstore does not keep the storage class LUKEWARM uncompressed on "+dataPool),
		Entry("a class the zone keeps on another pool", func() {
			w.zonegroupClasses = append(w.zonegroupClasses, "FROZEN", "LUKEWARM")
			w.periodClasses = w.zonegroupClasses
			w.zoneClasses["LUKEWARM"], w.zoneClasses["FROZEN"] = "", ""
			w.zonePools = map[string]string{"FROZEN": "ceph-objectstore.rgw.buckets.cold"}
		}, "zone ceph-objectstore does not keep the storage class FROZEN uncompressed on "+dataPool),
		Entry("a period that still lacks them after the commit", func() { w.commitDrops = true },
			"the current period's zonegroup ceph-objectstore does not list the storage classes LUKEWARM, FROZEN"),
	)

	It("fails, naming the restart, when the radosgw does not reload the period", func(ctx SpecContext) {
		w.noReload = true
		_, err := ensure(ctx)
		Expect(err).To(MatchError(ContainSubstring("the radosgw did not finish reloading the committed period in 180s; " +
			"restart it with 'ROOKET_NAME=rgw-go-squid rooket kubectl -n rook-ceph delete pod -l app=rook-ceph-rgw'")))
	})

	It("fails with the service map's last error, not a hung reload, when the map cannot be read", func(ctx SpecContext) {
		w.noReload = true
		w.dumpErrAfterCommit = errors.New("exit status 110: timed out")
		_, err := ensure(ctx)
		Expect(err).To(MatchError("adding the storage classes LUKEWARM, FROZEN: the radosgw's reload of the committed " +
			"period could not be seen in 180s: ceph service dump failed, so the service map could not be read: " +
			"exit status 110: timed out"))
	})
})
