package rgw_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/denc/goldentest"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// soleExec asserts the op holds exactly one exec step on class rgw calling
// method, and returns it.
func soleExec(steps []radosclient.Step, method string) *radosclient.ExecStep {
	GinkgoHelper()
	Expect(steps).To(HaveLen(1))
	step, ok := steps[0].(*radosclient.ExecStep)
	Expect(ok).To(BeTrue(), "step is %T, not an exec", steps[0])
	Expect(step.Class).To(Equal("rgw"))
	Expect(step.Method).To(Equal(method))
	return step
}

// goldens loads a type's corpus cases, failing when there are none.
func goldens(typ string) []goldentest.Case {
	GinkgoHelper()
	cs, err := goldentest.Load("testdata", typ)
	Expect(err).NotTo(HaveOccurred())
	Expect(cs).NotTo(BeEmpty())
	return cs
}

// eachGolden decodes every corpus case of typ, passes it to call on a fresh
// write op, and asserts the op's sole exec carries the case's Squid
// re-encoding.
func eachGolden[T any](typ, method string, dec func(*denc.Decoder) T, call func(op *radosclient.WriteOp, v T)) {
	GinkgoHelper()
	for _, c := range goldens(typ) {
		op := radosclient.NewWriteOp()
		call(op, decodeWhole(c.Bin, dec))
		Expect(soleExec(op.Steps(), method).In).To(Equal(c.ReEnc), "%s/%s/%s", typ, c.Archive, c.Name)
	}
}

// guardBytes is cls_rgw_guard_bucket_resharding_op{ret_err -2300}, built by
// hand: radosgw passes -ERR_BUSY_RESHARDING and the class returns it verbatim.
func guardBytes() []byte {
	return build(func(e *denc.Encoder) {
		f := e.BeginStruct(1, 1)
		e.I32(-2300)
		e.EndStruct(f)
	})
}

var _ = Describe("request marshaling", func() {
	It("guard_bucket_resharding always sends -ERR_BUSY_RESHARDING", func() {
		for _, r := range releases {
			op := radosclient.NewWriteOp()
			rgw.GuardBucketResharding(op, r)
			Expect(soleExec(op.Steps(), "guard_bucket_resharding").In).To(Equal(guardBytes()))
		}
	})

	It("set_bucket_resharding wraps the instance entry", func() {
		op := radosclient.NewWriteOp()
		rgw.SetBucketResharding(op, rgw.InstanceEntry{ReshardStatus: rgw.ReshardInProgress}, denc.Squid)
		want := build(func(e *denc.Encoder) {
			f := e.BeginStruct(1, 1)
			ie := e.BeginStruct(3, 1)
			e.U8(rgw.ReshardInProgress)
			e.String("")
			e.I32(-1)
			e.EndStruct(ie)
			e.EndStruct(f)
		})
		Expect(soleExec(op.Steps(), "set_bucket_resharding").In).To(Equal(want))
		Expect(decodeWhole(want, rgw.DecodeSetReshardingOp)).To(Equal(rgw.SetReshardingOp{
			Entry: rgw.InstanceEntry{ReshardStatus: rgw.ReshardInProgress},
		}))
	})

	It("bucket_init_index sends no input", func() {
		op := radosclient.NewWriteOp()
		rgw.BucketInitIndex(op)
		Expect(soleExec(op.Steps(), "bucket_init_index").In).To(BeEmpty())
	})

	It("bucket_set_tag_timeout", func() {
		eachGolden("rgw_cls_tag_timeout_op", "bucket_set_tag_timeout", rgw.DecodeTagTimeoutOp,
			func(op *radosclient.WriteOp, v rgw.TagTimeoutOp) {
				rgw.BucketSetTagTimeout(op, v.TagTimeout, denc.Squid)
			})
	})

	It("bucket_prepare_op", func() {
		eachGolden("rgw_cls_obj_prepare_op", "bucket_prepare_op", rgw.DecodePrepareOp,
			func(op *radosclient.WriteOp, v rgw.PrepareOp) { rgw.BucketPrepareOp(op, v, denc.Squid) })
	})

	It("bucket_complete_op", func() {
		eachGolden("rgw_cls_obj_complete_op", "bucket_complete_op", rgw.DecodeCompleteOp,
			func(op *radosclient.WriteOp, v rgw.CompleteOp) { rgw.BucketCompleteOp(op, v, denc.Squid) })
	})

	It("bucket_list", func() {
		for _, c := range goldens("rgw_cls_list_op") {
			op := radosclient.NewReadOp()
			rgw.BucketList(op, decodeWhole(c.Bin, rgw.DecodeListOp), denc.Squid)
			Expect(soleExec(op.Steps(), "bucket_list").In).To(Equal(c.ReEnc), "%s/%s", c.Archive, c.Name)
		}
	})

	It("bucket_list for the header asks for no entries", func() {
		op := radosclient.NewReadOp()
		rgw.GetDirHeader(op, denc.Squid)
		Expect(soleExec(op.Steps(), "bucket_list").In).To(Equal(build(func(e *denc.Encoder) {
			f := e.BeginStruct(6, 4)
			e.U32(0)
			e.String("")
			objKey(e, "", "")
			e.Bool(false)
			e.String("")
			e.EndStruct(f)
		})))
	})

	It("dir_suggest_changes concatenates op bytes and entries with no framing", func() {
		cs := goldens("rgw_bucket_dir_entry")
		var changes []rgw.Suggestion
		var want []byte
		for i, c := range cs {
			s := rgw.Suggestion{Op: rgw.SuggestUpdate, Entry: decodeWhole(c.Bin, rgw.DecodeDirEntry)}
			b := byte('u')
			if i%2 == 1 {
				s.Op, b = rgw.SuggestRemove, 'r'
			}
			if i%3 == 0 {
				s.Log = true
				b |= 0x80
			}
			changes = append(changes, s)
			want = append(append(want, b), c.ReEnc...)
		}
		op := radosclient.NewWriteOp()
		rgw.SuggestChanges(op, changes, denc.Squid)
		Expect(soleExec(op.Steps(), "dir_suggest_changes").In).To(Equal(want))
	})

	It("bucket_check_index sends no input", func() {
		op := radosclient.NewReadOp()
		rgw.BucketCheckIndex(op, denc.Squid)
		Expect(soleExec(op.Steps(), "bucket_check_index").In).To(BeEmpty())
	})

	It("bucket_rebuild_index sends no input", func() {
		op := radosclient.NewWriteOp()
		rgw.BucketRebuildIndex(op)
		Expect(soleExec(op.Steps(), "bucket_rebuild_index").In).To(BeEmpty())
	})

	It("obj_remove", func() {
		eachGolden("rgw_cls_obj_remove_op", "obj_remove", rgw.DecodeObjRemoveOp,
			func(op *radosclient.WriteOp, v rgw.ObjRemoveOp) { rgw.ObjRemove(op, v.KeepAttrPrefixes, denc.Squid) })
	})

	It("obj_store_pg_ver", func() {
		eachGolden("rgw_cls_obj_store_pg_ver_op", "obj_store_pg_ver", rgw.DecodeStorePGVerOp,
			func(op *radosclient.WriteOp, v rgw.StorePGVerOp) { rgw.ObjStorePGVer(op, v.Attr, denc.Squid) })
	})

	It("obj_check_attrs_prefix", func() {
		eachGolden("rgw_cls_obj_check_attrs_prefix", "obj_check_attrs_prefix", rgw.DecodeCheckAttrsPrefixOp,
			func(op *radosclient.WriteOp, v rgw.CheckAttrsPrefixOp) {
				rgw.ObjCheckAttrsPrefix(op, v.CheckPrefix, v.FailIfExist, denc.Squid)
			})
	})

	It("obj_check_mtime, which has no golden", func() {
		op := radosclient.NewWriteOp()
		rgw.ObjCheckMtime(op, t1, rgw.MtimeLE, true, denc.Squid)
		Expect(soleExec(op.Steps(), "obj_check_mtime").In).To(Equal(build(func(e *denc.Encoder) {
			f := e.BeginStruct(2, 1)
			e.Time(t1)
			e.U8(2)
			e.Bool(true)
			e.EndStruct(f)
		})))
	})

	It("user_usage_log_add sends no user, as radosgw does", func() {
		for _, c := range goldens("rgw_cls_usage_log_add_op") {
			v := decodeWhole(c.Bin, rgw.DecodeUsageAddOp)
			Expect(v.User).To(BeEmpty(), "%s/%s carries a user", c.Archive, c.Name)
			op := radosclient.NewWriteOp()
			rgw.UsageLogAdd(op, v.Info, denc.Squid)
			Expect(soleExec(op.Steps(), "user_usage_log_add").In).To(Equal(c.ReEnc), "%s/%s", c.Archive, c.Name)
		}
	})

	It("user_usage_log_read", func() {
		for _, c := range goldens("rgw_cls_usage_log_read_op") {
			op := radosclient.NewReadOp()
			rgw.UsageLogRead(op, decodeWhole(c.Bin, rgw.DecodeUsageReadOp), denc.Squid)
			Expect(soleExec(op.Steps(), "user_usage_log_read").In).To(Equal(c.ReEnc), "%s/%s", c.Archive, c.Name)
		}
	})

	It("user_usage_log_trim", func() {
		eachGolden("rgw_cls_usage_log_trim_op", "user_usage_log_trim", rgw.DecodeUsageTrimOp,
			func(op *radosclient.WriteOp, v rgw.UsageTrimOp) { rgw.UsageLogTrim(op, v, denc.Squid) })
	})

	It("usage_log_clear sends no input", func() {
		op := radosclient.NewWriteOp()
		rgw.UsageLogClear(op)
		Expect(soleExec(op.Steps(), "usage_log_clear").In).To(BeEmpty())
	})

	It("gc_set_entry", func() {
		eachGolden("cls_rgw_gc_set_entry_op", "gc_set_entry", rgw.DecodeGCSetEntryOp,
			func(op *radosclient.WriteOp, v rgw.GCSetEntryOp) {
				rgw.GCSetEntry(op, v.ExpirationSecs, v.Info, denc.Squid)
			})
	})
})

// guardedPrepare and guardedComplete are minimal index writes to guard.
var (
	guardedPrepare  = rgw.PrepareOp{Op: rgw.OpAdd, Key: rgw.ObjKey{Name: "k"}, Tag: "t"}
	guardedComplete = rgw.CompleteOp{Op: rgw.OpAdd, Key: rgw.ObjKey{Name: "k"}, Tag: "t", Ver: rgw.NewEntryVer()}
)

var _ = Describe("the reshard guard", func() {
	DescribeTable("precedes the index writes radosgw guards, in the same op",
		func(method string, add func(op *radosclient.WriteOp)) {
			op := radosclient.NewWriteOp()
			rgw.GuardBucketResharding(op, denc.Squid)
			add(op)
			steps := op.Steps()
			Expect(steps).To(HaveLen(2))
			Expect(soleExec(steps[:1], "guard_bucket_resharding").In).To(Equal(guardBytes()))
			soleExec(steps[1:], method)
		},
		Entry(nil, "bucket_prepare_op", func(op *radosclient.WriteOp) { rgw.BucketPrepareOp(op, guardedPrepare, denc.Squid) }),
		Entry(nil, "bucket_complete_op", func(op *radosclient.WriteOp) { rgw.BucketCompleteOp(op, guardedComplete, denc.Squid) }),
	)
})

// execResult returns the ExecResult the op's sole exec step fills.
func execResult(op *radosclient.ReadOp) *radosclient.ExecResult {
	GinkgoHelper()
	steps := op.Steps()
	Expect(steps).To(HaveLen(1))
	step, ok := steps[0].(*radosclient.ExecStep)
	Expect(ok).To(BeTrue())
	return step.Result
}

var _ = Describe("reply decoding", func() {
	It("decodes every rgw_cls_list_ret golden from a bucket_list result", func() {
		for _, c := range goldens("rgw_cls_list_ret") {
			op := radosclient.NewReadOp()
			res := rgw.BucketList(op, rgw.ListOp{}, denc.Squid)
			execResult(op).Set(c.Bin, 0)
			got, err := res.Result()
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(decodeWhole(c.Bin, rgw.DecodeListRet)))
		}
	})

	It("decodes every rgw_cls_check_index_ret golden from a bucket_check_index result", func() {
		for _, c := range goldens("rgw_cls_check_index_ret") {
			op := radosclient.NewReadOp()
			res := rgw.BucketCheckIndex(op, denc.Squid)
			execResult(op).Set(c.Bin, 0)
			got, err := res.Result()
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(decodeWhole(c.Bin, rgw.DecodeCheckIndexRet)))
		}
	})

	It("decodes every rgw_cls_usage_log_read_ret golden from a user_usage_log_read result", func() {
		for _, c := range goldens("rgw_cls_usage_log_read_ret") {
			op := radosclient.NewReadOp()
			res := rgw.UsageLogRead(op, rgw.UsageReadOp{}, denc.Squid)
			execResult(op).Set(c.Bin, 0)
			got, err := res.Result()
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(decodeWhole(c.Bin, rgw.DecodeUsageReadRet)))
		}
	})

	It("encodes get_bucket_resharding as an empty struct and decodes the entry", func() {
		for _, r := range releases {
			op := radosclient.NewReadOp()
			res := rgw.GetBucketResharding(op, r)
			step := soleExec(op.Steps(), "get_bucket_resharding")
			Expect(step.In).To(Equal([]byte{1, 1, 0, 0, 0, 0}), "ENCODE_START(1, 1) with an empty body at %v", r)
			step.Result.Set(build(func(e *denc.Encoder) {
				f := e.BeginStruct(1, 1)
				rgw.InstanceEntry{ReshardStatus: rgw.ReshardInProgress}.Encode(e, r)
				e.EndStruct(f)
			}), 0)
			got, err := res.Result()
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(rgw.InstanceEntry{ReshardStatus: rgw.ReshardInProgress}), "%v", r)
		}
	})

	It("names get_bucket_resharding in a reply that fails to decode", func() {
		op := radosclient.NewReadOp()
		res := rgw.GetBucketResharding(op, denc.Squid)
		execResult(op).Set([]byte{1, 1, 100, 0, 0, 0}, 0)
		_, err := res.Result()
		Expect(err).To(MatchError(denc.ErrShortBuffer))
		Expect(err).To(MatchError(ContainSubstring("rgw: decoding get_bucket_resharding reply")))
	})

	It("reports an op that has not run", func() {
		op := radosclient.NewReadOp()
		_, err := rgw.GetDirHeader(op, denc.Squid).Result()
		Expect(err).To(MatchError(radosclient.ErrIncomplete))
	})

	It("passes a resharding error through", func() {
		op := radosclient.NewReadOp()
		res := rgw.GetDirHeader(op, denc.Squid)
		execResult(op).Set(nil, -rgw.ErrBusyResharding)
		_, err := res.Result()
		Expect(err).To(MatchError(radosclient.ErrBusyResharding))
	})

	It("reports a truncated reply", func() {
		op := radosclient.NewReadOp()
		res := rgw.GetDirHeader(op, denc.Squid)
		execResult(op).Set([]byte{4, 2, 100, 0, 0, 0}, 0)
		_, err := res.Result()
		Expect(err).To(MatchError(denc.ErrShortBuffer))
	})
})

var _ = Describe("the omap-era gc methods", func() {
	It("gc_list sends cls_rgw_gc_list_op and decodes cls_rgw_gc_list_ret", func() {
		op := radosclient.NewReadOp()
		res := rgw.GCList(op, "m", 128, true, denc.Squid)
		step := soleExec(op.Steps(), "gc_list")
		Expect(decodeWhole(step.In, rgw.DecodeGCListOp)).To(Equal(rgw.GCListOp{Marker: "m", Max: 128, ExpiredOnly: true}))
		want := rgw.GCListRet{Entries: []rgw.GCObjInfo{{Tag: "t\x00"}}, NextMarker: "n", Truncated: true}
		step.Result.Set(encodeAt(want, denc.Squid), 0)
		got, err := res.Result()
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(want))
	})

	It("gc_remove and gc_defer_entry go in order on one write op", func() {
		op := radosclient.NewWriteOp()
		rgw.GCRemove(op, []string{"a\x00", "b\x00"}, denc.Squid)
		rgw.GCDeferEntry(op, 7200, "a\x00", denc.Squid)
		steps := op.Steps()
		Expect(steps).To(HaveLen(2))
		remove := soleExec(steps[:1], "gc_remove")
		Expect(decodeWhole(remove.In, rgw.DecodeGCRemoveOp)).To(Equal(rgw.GCRemoveOp{Tags: []string{"a\x00", "b\x00"}}))
		deferred := soleExec(steps[1:], "gc_defer_entry")
		Expect(decodeWhole(deferred.In, rgw.DecodeGCDeferEntryOp)).To(Equal(rgw.GCDeferEntryOp{ExpirationSecs: 7200, Tag: "a\x00"}))
	})

	It("gc_remove", func() {
		eachGolden("cls_rgw_gc_remove_op", "gc_remove", rgw.DecodeGCRemoveOp,
			func(op *radosclient.WriteOp, v rgw.GCRemoveOp) { rgw.GCRemove(op, v.Tags, denc.Squid) })
	})

	It("gc_defer_entry", func() {
		eachGolden("cls_rgw_gc_defer_entry_op", "gc_defer_entry", rgw.DecodeGCDeferEntryOp,
			func(op *radosclient.WriteOp, v rgw.GCDeferEntryOp) {
				rgw.GCDeferEntry(op, v.ExpirationSecs, v.Tag, denc.Squid)
			})
	})

	It("sends expired_only false and names gc_list in a reply that fails to decode", func() {
		op := radosclient.NewReadOp()
		res := rgw.GCList(op, "", 0, false, denc.Squid)
		step := soleExec(op.Steps(), "gc_list")
		Expect(decodeWhole(step.In, rgw.DecodeGCListOp)).To(Equal(rgw.GCListOp{Max: 0, ExpiredOnly: false}))
		step.Result.Set([]byte{2, 1, 100, 0, 0, 0}, 0)
		_, err := res.Result()
		Expect(err).To(MatchError(denc.ErrShortBuffer))
		Expect(err).To(MatchError(ContainSubstring("rgw: decoding gc_list reply")))
	})
})
