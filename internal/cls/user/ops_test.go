package user_test

import (
	"errors"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cls/user"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/denc/goldentest"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// execStep asserts steps is a single exec of user.method and returns it.
func execStep(steps []radosclient.Step, method string) *radosclient.ExecStep {
	GinkgoHelper()
	Expect(steps).To(HaveLen(1))
	s, ok := steps[0].(*radosclient.ExecStep)
	Expect(ok).To(BeTrue(), "step is %T, not an exec", steps[0])
	Expect(s.Class).To(Equal("user"))
	Expect(s.Method).To(Equal(method))
	return s
}

// goldens returns the corpus cases of typ decoded with decode.
func goldens[T any](typ string, decode func(*denc.Decoder) T) ([]goldentest.Case, []T) {
	GinkgoHelper()
	cs, err := goldentest.Load("testdata", typ)
	Expect(err).NotTo(HaveOccurred())
	Expect(cs).NotTo(BeEmpty())
	vs := make([]T, len(cs))
	for i, c := range cs {
		d := denc.NewDecoder(c.Bin)
		vs[i] = decode(d)
		Expect(d.Err()).NotTo(HaveOccurred())
	}
	return cs, vs
}

var _ = Describe("requests", func() {
	It("set_buckets_info sends every corpus cls_user_set_buckets_op byte for byte", func() {
		cs, ops := goldens("cls_user_set_buckets_op", user.DecodeSetBucketsOp)
		for i, c := range cs {
			w := radosclient.NewWriteOp()
			user.SetBucketsInfo(w, ops[i].Entries, ops[i].Add, ops[i].Time, denc.Squid)
			Expect(execStep(w.Steps(), "set_buckets_info").In).To(Equal(c.ReEnc), c.Name)
		}
	})
	It("remove_bucket sends every corpus cls_user_remove_bucket_op byte for byte", func() {
		cs, ops := goldens("cls_user_remove_bucket_op", user.DecodeRemoveBucketOp)
		for i, c := range cs {
			w := radosclient.NewWriteOp()
			user.RemoveBucket(w, ops[i].Bucket, denc.Squid)
			Expect(execStep(w.Steps(), "remove_bucket").In).To(Equal(c.ReEnc), c.Name)
		}
	})
	It("list_buckets sends every corpus cls_user_list_buckets_op byte for byte", func() {
		cs, ops := goldens("cls_user_list_buckets_op", user.DecodeListBucketsOp)
		for i, c := range cs {
			r := radosclient.NewReadOp()
			user.ListBuckets(r, ops[i].Marker, ops[i].EndMarker, ops[i].MaxEntries, denc.Squid)
			Expect(execStep(r.Steps(), "list_buckets").In).To(Equal(c.ReEnc), c.Name)
		}
	})
	It("list_buckets sends the end marker after max_entries", func() {
		// Hand-built: every corpus op has an empty end marker.
		r := radosclient.NewReadOp()
		user.ListBuckets(r, "a", "z", -1, denc.Squid)
		e := denc.NewEncoder()
		f := e.BeginStruct(2, 1)
		e.String("a")
		e.I32(-1)
		e.String("z")
		e.EndStruct(f)
		Expect(execStep(r.Steps(), "list_buckets").In).To(Equal(e.Bytes()))
	})
	It("get_header sends every corpus cls_user_get_header_op byte for byte", func() {
		cs, _ := goldens("cls_user_get_header_op", user.DecodeGetHeaderOp)
		for _, c := range cs {
			r := radosclient.NewReadOp()
			user.GetHeader(r, denc.Squid)
			Expect(execStep(r.Steps(), "get_header").In).To(Equal(c.ReEnc), c.Name)
		}
	})
	It("complete_stats_sync sends every corpus cls_user_complete_stats_sync_op byte for byte", func() {
		cs, ops := goldens("cls_user_complete_stats_sync_op", user.DecodeCompleteStatsSyncOp)
		for i, c := range cs {
			w := radosclient.NewWriteOp()
			user.CompleteStatsSync(w, ops[i].Time, denc.Squid)
			Expect(execStep(w.Steps(), "complete_stats_sync").In).To(Equal(c.ReEnc), c.Name)
		}
	})
	It("reset_user_stats2 sends cls_user_reset_stats2_op", func() {
		// Hand-built: the corpus has no cls_user_reset_stats2_op.
		t := time.Date(2026, 9, 26, 1, 2, 3, 4000, time.UTC)
		r := radosclient.NewReadOp()
		user.ResetStats2(r, t, "m", user.Stats{TotalEntries: 1, TotalBytes: 2, TotalBytesRounded: 3}, denc.Squid)
		e := denc.NewEncoder()
		f := e.BeginStruct(1, 1)
		e.U32(uint32(t.Unix()))
		e.U32(4000)
		e.String("m")
		s := e.BeginStruct(1, 1)
		e.U64(1)
		e.U64(2)
		e.U64(3)
		e.EndStruct(s)
		e.EndStruct(f)
		Expect(execStep(r.Steps(), "reset_user_stats2").In).To(Equal(e.Bytes()))
	})
})

var _ = Describe("replies", func() {
	It("list_buckets decodes every corpus cls_user_list_buckets_ret", func() {
		cs, rets := goldens("cls_user_list_buckets_ret", user.DecodeListBucketsRet)
		for i, c := range cs {
			r := radosclient.NewReadOp()
			res := user.ListBuckets(r, "", "", 1000, denc.Squid)
			execStep(r.Steps(), "list_buckets").Result.Set(c.Bin, 0)
			entries, marker, truncated, err := res.Entries()
			Expect(err).NotTo(HaveOccurred())
			Expect(entries).To(Equal(rets[i].Entries))
			Expect(marker).To(Equal(rets[i].Marker))
			Expect(truncated).To(Equal(rets[i].Truncated))
		}
	})
	It("get_header decodes every corpus cls_user_get_header_ret", func() {
		cs, rets := goldens("cls_user_get_header_ret", user.DecodeGetHeaderRet)
		for i, c := range cs {
			r := radosclient.NewReadOp()
			res := user.GetHeader(r, denc.Squid)
			execStep(r.Steps(), "get_header").Result.Set(c.Bin, 0)
			h, err := res.Header()
			Expect(err).NotTo(HaveOccurred())
			Expect(h).To(Equal(rets[i].Header))
		}
	})
	It("reset_user_stats2 decodes cls_user_reset_stats2_ret", func() {
		e := denc.NewEncoder()
		f := e.BeginStruct(1, 1)
		e.String("last")
		s := e.BeginStruct(1, 1)
		e.U64(4)
		e.U64(5)
		e.U64(6)
		e.EndStruct(s)
		e.Bool(true)
		e.EndStruct(f)
		r := radosclient.NewReadOp()
		res := user.ResetStats2(r, time.Time{}, "", user.Stats{}, denc.Squid)
		execStep(r.Steps(), "reset_user_stats2").Result.Set(e.Bytes(), 0)
		ret, err := res.Result()
		Expect(err).NotTo(HaveOccurred())
		Expect(ret).To(Equal(user.ResetStats2Ret{
			Marker:    "last",
			AccStats:  user.Stats{TotalEntries: 4, TotalBytes: 5, TotalBytesRounded: 6},
			Truncated: true,
		}))
	})
	It("reports the method's error, an undecodable reply, and a read before the op ran", func() {
		r := radosclient.NewReadOp()
		list := user.ListBuckets(r, "", "", 1, denc.Squid)
		header := user.GetHeader(r, denc.Squid)
		reset := user.ResetStats2(r, time.Time{}, "", user.Stats{}, denc.Squid)
		_, _, _, err := list.Entries()
		Expect(err).To(MatchError(radosclient.ErrIncomplete))
		_, err = header.Header()
		Expect(err).To(MatchError(radosclient.ErrIncomplete))
		_, err = reset.Result()
		Expect(err).To(MatchError(radosclient.ErrIncomplete))

		for i, s := range r.Steps() {
			exec, ok := s.(*radosclient.ExecStep)
			Expect(ok).To(BeTrue())
			switch i {
			case 0:
				exec.Result.Set(nil, -int32(syscall.ENOENT))
			case 1:
				exec.Result.Set([]byte{1, 1}, 0)
			default:
				exec.Result.Set([]byte{2, 2, 0, 0, 0, 0}, 0)
			}
		}
		_, _, _, err = list.Entries()
		Expect(err).To(MatchError(radosclient.ErrNotFound))
		_, err = header.Header()
		Expect(errors.Is(err, denc.ErrShortBuffer)).To(BeTrue(), "%v", err)
		_, err = reset.Result()
		Expect(errors.Is(err, denc.ErrIncompatible)).To(BeTrue(), "%v", err)
	})
})
