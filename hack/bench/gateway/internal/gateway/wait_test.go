package gateway_test

import (
	"errors"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/hack/bench/gateway/internal/gateway"
)

// The command lines the waits poll with.
const (
	toolbox     = "ROOKET_NAME=rgw-go-tentacle rooket kubectl -n rook-ceph exec deploy/rook-ceph-tools -- "
	site        = " --rgw-realm=ceph-objectstore --rgw-zonegroup=ceph-objectstore --rgw-zone=ceph-objectstore"
	dfLine      = toolbox + "ceph --connect-timeout=20 --rados-mon-op-timeout=20 df -f json\n"
	gcLines     = toolbox + "radosgw-admin gc process --include-all" + site + "\n" + toolbox + "radosgw-admin gc list --include-all" + site + "\n"
	dumpLine    = toolbox + "ceph --connect-timeout=20 --rados-mon-op-timeout=20 service dump -f json\n"
	podIPLine   = "ROOKET_NAME=rgw-go-tentacle rooket kubectl -n rook-ceph get pods -l app=rook-ceph-rgw -o jsonpath={.items[*].status.podIP}\n"
	shortOfRoom = 100000000
)

// The world's clock moves only by the Runner's sleeps, so each wait below
// reaches its deadline after a fixed count of polls: one at the start and one
// after each sleep, the last at the deadline itself.
var _ = Describe("the run's waits", func() {
	Context("for the data pool's free space", func() {
		It("reads ceph df again 10 s later while the pool is short, and runs the profile once it fits", func(ctx SpecContext) {
			w := newWorld("rgw-go")
			// The run's idle read, then small-put's first wait short, then room.
			w.avail = []int64{free, shortOfRoom, free}
			Expect(w.run(ctx, w.config(gateway.Settings{Smoke: "1", Profiles: []string{"small-put"}}))).To(Succeed())
			Expect(w.trace()).To(ContainSubstring("sleep 2\n" + dfLine + "sleep 10\n" + dfLine + "AWS_SECRET_ACCESS_KEY="))
			Expect(strings.Count(w.trace(), "sleep 10\n")).To(Equal(1))
		})

		It("fails a profile after 180 s of a pool too short for twice its dataset, and still undoes the run", func(ctx SpecContext) {
			w := newWorld("rgw-go")
			w.avail = []int64{free, shortOfRoom}
			err := w.run(ctx, w.config(gateway.Settings{Smoke: "1", Profiles: []string{"small-put"}}))
			Expect(err).To(MatchError("small-put: the data pool can take 100000000 B, under twice the 98304000 B its dataset holds"))
			Expect(gateway.ExitStatus(ctx, err)).To(Equal(1))
			trace := w.trace()
			Expect(strings.Count(trace, "sleep 10\n")).To(Equal(18), "a poll every 10 s from 0 to 180 s")
			Expect(strings.Count(trace, dfLine)).To(Equal(1+19+1), "the idle read, the wait's 19 polls and the exit's")
			Expect(trace).NotTo(ContainSubstring("--jsonfile"), "no phase ran")
			Expect(trace).To(HaveSuffix(exitTail("rgw-go")), "the user removed, rgw-go stopped, the OSD compacted")
			Expect(w.readFile("run.log")).To(ContainSubstring(`"level":"ERROR","msg":"the run failed","error":"small-put: the data pool can take`))
		})
	})

	Context("for the garbage collection queue", func() {
		It("runs gc process again 5 s later while entries are queued, and goes on once the queue is empty", func(ctx SpecContext) {
			w := newWorld("rgw-go")
			w.queued = []int{2, 0}
			Expect(w.run(ctx, w.config(gateway.Settings{Smoke: "1", Profiles: []string{"list"}}))).To(Succeed())
			Expect(w.trace()).To(ContainSubstring(gcLines+"sleep 5\n"+gcLines+dfLine), "the queued garbage collected before the idle read")
			Expect(strings.Count(w.trace(), "sleep 5\n")).To(Equal(1))
		})

		It("fails a profile whose garbage does not drain in 180 s, and still undoes the run", func(ctx SpecContext) {
			w := newWorld("rgw-go")
			// The queue is empty at the start, and holds three entries from then on.
			w.queued = []int{0, 3}
			err := w.run(ctx, w.config(gateway.Settings{Smoke: "1", Profiles: []string{"small-put"}}))
			runLog := filepath.Join(w.dir(), "run.log")
			Expect(err).To(MatchError(ContainSubstring(
				"small-put: the garbage collection queue did not drain in 180 s; see " + runLog + ": the queue still holds 3 entries")))
			// The exit's own drain, before its compaction, does not drain either.
			Expect(err).To(MatchError(ContainSubstring(
				"collecting the garbage or compacting the OSD failed; see " + runLog + ": the queue still holds 3 entries")))
			Expect(gateway.ExitStatus(ctx, err)).To(Equal(1))
			trace := w.trace()
			Expect(strings.Count(trace, "sleep 5\n")).To(Equal(2*36), "a pass every 5 s from 0 to 180 s, in the profile's drain and the exit's")
			Expect(trace).To(ContainSubstring("ROOKET=rooket REPO/hack/admin/remove-user.sh tentacle bench\nROOKET=rooket REPO/hack/rooket/rgw-go-down.sh tentacle\n"))
			Expect(trace).NotTo(ContainSubstring("compact"), "no compaction over garbage still queued")
		})
	})

	Context("for the service map to list one radosgw", func() {
		It("reads it again 5 s later while it lists two, and goes on once it lists one", func(ctx SpecContext) {
			w := newWorld("radosgw")
			w.radosgws = []int{2, 1}
			Expect(w.run(ctx, w.config(gateway.Settings{Smoke: "1", Profiles: []string{"list"}}))).To(Succeed())
			Expect(w.trace()).To(HavePrefix("REPO/hack/bench/elbencho-build.sh\n" + dumpLine + "sleep 5\n" + dumpLine + podIPLine))
		})

		DescribeTable("fails after 180 s, before anything is there to undo",
			func(ctx SpecContext, radosgws int, fail error, want string) {
				w := newWorld("radosgw")
				w.radosgws = []int{radosgws}
				if fail != nil {
					w.failWith["service dump"] = fail
				}
				err := w.run(ctx, w.config(gateway.Settings{Smoke: "1", Profiles: []string{"list"}}))
				Expect(err).To(MatchError(ContainSubstring(want)))
				Expect(gateway.ExitStatus(ctx, err)).To(Equal(1))
				trace := w.trace()
				Expect(strings.Count(trace, "sleep 5\n")).To(Equal(36), "a read every 5 s from 0 to 180 s")
				Expect(trace).To(Equal("REPO/hack/bench/elbencho-build.sh\n"+strings.Repeat(dumpLine+"sleep 5\n", 36)+dumpLine),
					"no user created and no rgw-go started, so the exit has nothing to undo")
				Expect(w.readFile("run.log")).To(ContainSubstring(`"level":"ERROR","msg":"the run failed","error":"the service map did not settle`))
			},
			Entry("when it lists two radosgws", 2, nil,
				`the service map did not settle to one radosgw in 180s (radosgw entries: [{"start_stamp":"2026-10-12T21:00:00.000000+0000","metadata":`),
			Entry("when it lists none", 0, nil,
				"the service map did not settle to one radosgw in 180s (radosgw entries: []); "+
					"the manager drops a stopped radosgw within about a minute, so none is running or more than one is"),
			Entry("when ceph service dump fails", 1, errors.New("exit status 110: timed out"),
				"the service map did not settle to one radosgw in 180s: ceph service dump failed, so the service map "+
					"could not be read: exit status 110: timed out"),
		)
	})
})
