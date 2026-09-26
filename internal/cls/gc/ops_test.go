package gc_test

import (
	"syscall"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cls/gc"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/denc/goldentest"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

var releases = []denc.Release{denc.Squid, denc.Tentacle}

// onlyExec asserts the op holds exactly one exec step on rgw_gc.method and returns it.
func onlyExec(steps []radosclient.Step, method string) *radosclient.ExecStep {
	GinkgoHelper()
	Expect(steps).To(HaveLen(1))
	st, ok := steps[0].(*radosclient.ExecStep)
	Expect(ok).To(BeTrue(), "step is %T", steps[0])
	Expect(st.Class).To(Equal("rgw_gc"))
	Expect(st.Method).To(Equal(method))
	return st
}

func goldens(typ string) []goldentest.Case {
	GinkgoHelper()
	cs, err := goldentest.Load("testdata", typ)
	Expect(err).NotTo(HaveOccurred())
	Expect(cs).NotTo(BeEmpty())
	return cs
}

var _ = Describe("request marshaling", func() {
	It("names the Squid class and methods", func() {
		Expect(gc.Class).To(Equal("rgw_gc"))
		Expect(gc.MethodQueueInit).To(Equal("rgw_gc_queue_init"))
		Expect(gc.MethodQueueEnqueue).To(Equal("rgw_gc_queue_enqueue"))
		Expect(gc.MethodQueueListEntries).To(Equal("rgw_gc_queue_list_entries"))
		Expect(gc.MethodQueueRemoveEntries).To(Equal("rgw_gc_queue_remove_entries"))
		Expect(gc.MethodQueueUpdateEntry).To(Equal("rgw_gc_queue_update_entry"))
	})

	It("QueueInit sends cls_rgw_gc_queue_init_op", func() {
		for _, r := range releases {
			for _, c := range goldens("cls_rgw_gc_queue_init_op") {
				v := decodeWhole(c.Bin, gc.DecodeQueueInitOp)
				op := radosclient.NewWriteOp()
				gc.QueueInit(op, v.Size, v.NumDeferredEntries, r)
				Expect(onlyExec(op.Steps(), "rgw_gc_queue_init").In).To(Equal(c.ReEnc), c.Name)
			}
		}
	})

	It("composes with the non-exclusive create radosgw puts before QueueInit", func() {
		op := radosclient.NewWriteOp()
		op.Create(false)
		gc.QueueInit(op, 131068*1024, 50, denc.Squid)
		steps := op.Steps()
		Expect(steps).To(HaveLen(2))
		Expect(steps[0]).To(Equal(&radosclient.CreateStep{Exclusive: false}))
		st, ok := steps[1].(*radosclient.ExecStep)
		Expect(ok).To(BeTrue())
		Expect(st.Method).To(Equal("rgw_gc_queue_init"))
		Expect(decodeWhole(st.In, gc.DecodeQueueInitOp)).To(Equal(gc.QueueInitOp{Size: 131068 * 1024, NumDeferredEntries: 50}))
	})

	It("QueueEnqueue sends cls_rgw_gc_set_entry_op, laid out as the defer op is", func() {
		for _, r := range releases {
			for _, c := range loadRaw("cls_rgw_gc_queue_defer_entry_op") {
				v := decodeWhole(c.bin, gc.DecodeQueueDeferEntryOp)
				op := radosclient.NewWriteOp()
				gc.QueueEnqueue(op, v.ExpirationSecs, v.Info, r)
				Expect(onlyExec(op.Steps(), "rgw_gc_queue_enqueue").In).To(Equal(c.bin), c.id)
			}
			b, v := deferFixture()
			op := radosclient.NewWriteOp()
			gc.QueueEnqueue(op, v.ExpirationSecs, v.Info, r)
			Expect(onlyExec(op.Steps(), "rgw_gc_queue_enqueue").In).To(Equal(b))
		}
	})

	It("QueueUpdateEntry sends cls_rgw_gc_queue_defer_entry_op", func() {
		for _, r := range releases {
			for _, c := range loadRaw("cls_rgw_gc_queue_defer_entry_op") {
				v := decodeWhole(c.bin, gc.DecodeQueueDeferEntryOp)
				op := radosclient.NewWriteOp()
				gc.QueueUpdateEntry(op, v.ExpirationSecs, v.Info, r)
				Expect(onlyExec(op.Steps(), "rgw_gc_queue_update_entry").In).To(Equal(c.bin), c.id)
			}
		}
	})

	It("QueueRemoveEntries sends cls_rgw_gc_queue_remove_entries_op, widening the count to 64 bits", func() {
		for _, r := range releases {
			for _, c := range loadRaw("cls_rgw_gc_queue_remove_entries_op") {
				v := decodeWhole(c.bin, gc.DecodeQueueRemoveEntriesOp)
				op := radosclient.NewWriteOp()
				gc.QueueRemoveEntries(op, uint32(v.NumEntries), r) //nolint:gosec // corpus counts are 1 and 2
				Expect(onlyExec(op.Steps(), "rgw_gc_queue_remove_entries").In).To(Equal(c.bin), c.id)
			}
		}
	})

	It("QueueList sends cls_rgw_gc_list_op", func() {
		for _, r := range releases {
			for _, c := range goldens("cls_rgw_gc_list_op") {
				v := decodeWhole(c.Bin, gc.DecodeListOp)
				op := radosclient.NewReadOp()
				gc.QueueList(op, v.Marker, v.Max, v.ExpiredOnly, r)
				Expect(onlyExec(op.Steps(), "rgw_gc_queue_list_entries").In).To(Equal(c.ReEnc), c.Name)
			}
		}
	})
})

var _ = Describe("ListResult", func() {
	run := func() (*radosclient.ExecResult, *gc.ListResult) {
		op := radosclient.NewReadOp()
		res := gc.QueueList(op, "", 0, false, denc.Squid)
		st, ok := op.Steps()[0].(*radosclient.ExecStep)
		Expect(ok).To(BeTrue())
		return st.Result, res
	}

	It("decodes every cls_rgw_gc_list_ret golden", func() {
		for _, c := range goldens("cls_rgw_gc_list_ret") {
			exec, res := run()
			exec.Set(c.Bin, 0)
			got, err := res.Result()
			Expect(err).NotTo(HaveOccurred(), c.Name)
			Expect(got).To(Equal(decodeWhole(c.Bin, gc.DecodeListRet)), c.Name)
		}
	})
	It("reports ErrIncomplete before the op ran", func() {
		_, res := run()
		_, err := res.Result()
		Expect(err).To(MatchError(radosclient.ErrIncomplete))
	})
	It("reports the method's errno", func() {
		exec, res := run()
		exec.Set(nil, -int32(syscall.ENOENT))
		_, err := res.Result()
		Expect(err).To(MatchError(radosclient.ErrNotFound))
	})
	It("reports a malformed reply", func() {
		exec, res := run()
		exec.Set([]byte{2, 1, 0xff, 0, 0, 0}, 0)
		_, err := res.Result()
		Expect(err).To(MatchError(denc.ErrShortBuffer))
	})
})
