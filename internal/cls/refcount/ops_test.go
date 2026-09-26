package refcount_test

import (
	"errors"
	"syscall"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cls/refcount"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/denc/goldentest"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// execStep asserts steps is a single exec of refcount.method and returns it.
func execStep(steps []radosclient.Step, method string) *radosclient.ExecStep {
	GinkgoHelper()
	Expect(steps).To(HaveLen(1))
	s, ok := steps[0].(*radosclient.ExecStep)
	Expect(ok).To(BeTrue(), "step is %T, not an exec", steps[0])
	Expect(s.Class).To(Equal("refcount"))
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

func decodeWhole[T any](b []byte, decode func(*denc.Decoder) T) T {
	GinkgoHelper()
	d := denc.NewDecoder(b)
	v := decode(d)
	Expect(d.Err()).NotTo(HaveOccurred())
	Expect(d.Remaining()).To(BeZero())
	return v
}

var _ = Describe("requests", func() {
	It("get sends every corpus cls_refcount_get_op byte for byte", func() {
		cs, ops := goldens("cls_refcount_get_op", refcount.DecodeGetOp)
		for i, c := range cs {
			w := radosclient.NewWriteOp()
			refcount.Get(w, ops[i].Tag, ops[i].ImplicitRef, denc.Squid)
			Expect(execStep(w.Steps(), "get").In).To(Equal(c.ReEnc), c.Name)
		}
	})
	It("put sends every corpus cls_refcount_put_op byte for byte", func() {
		cs, ops := goldens("cls_refcount_put_op", refcount.DecodePutOp)
		for i, c := range cs {
			w := radosclient.NewWriteOp()
			refcount.Put(w, ops[i].Tag, ops[i].ImplicitRef, denc.Squid)
			Expect(execStep(w.Steps(), "put").In).To(Equal(c.ReEnc), c.Name)
		}
	})
	It("set sends every corpus cls_refcount_set_op byte for byte", func() {
		cs, ops := goldens("cls_refcount_set_op", refcount.DecodeSetOp)
		for i, c := range cs {
			w := radosclient.NewWriteOp()
			refcount.Set(w, ops[i].Refs, denc.Squid)
			Expect(execStep(w.Steps(), "set").In).To(Equal(c.ReEnc), c.Name)
		}
	})
	It("keeps a set list's order and duplicates, as std::list does", func() {
		w := radosclient.NewWriteOp()
		refcount.Set(w, []string{"b", "a", "b"}, denc.Squid)
		e := denc.NewEncoder()
		f := e.BeginStruct(1, 1)
		e.U32(3)
		e.String("b")
		e.String("a")
		e.String("b")
		e.EndStruct(f)
		Expect(execStep(w.Steps(), "set").In).To(Equal(e.Bytes()))
	})

	Describe("read", func() {
		It("sends every corpus cls_refcount_read_op byte for byte", func() {
			cs, ops := goldens("cls_refcount_read_op", refcount.DecodeReadOp)
			for i, c := range cs {
				r := radosclient.NewReadOp()
				refcount.Read(r, ops[i].ImplicitRef, denc.Squid)
				Expect(execStep(r.Steps(), "read").In).To(Equal(c.ReEnc), c.Name)
			}
		})
		It("decodes every corpus cls_refcount_read_ret", func() {
			cs, rets := goldens("cls_refcount_read_ret", refcount.DecodeReadRet)
			for i, c := range cs {
				r := radosclient.NewReadOp()
				res := refcount.Read(r, false, denc.Squid)
				execStep(r.Steps(), "read").Result.Set(c.Bin, 0)
				refs, err := res.Refs()
				Expect(err).NotTo(HaveOccurred())
				Expect(refs).To(Equal(rets[i].Refs))
			}
		})
		It("reports the method's error and an undecodable reply", func() {
			r := radosclient.NewReadOp()
			res := refcount.Read(r, true, denc.Squid)
			_, err := res.Refs()
			Expect(err).To(MatchError(radosclient.ErrIncomplete))
			execStep(r.Steps(), "read").Result.Set(nil, -int32(syscall.ENOENT))
			_, err = res.Refs()
			Expect(err).To(MatchError(radosclient.ErrNotFound))
			execStep(r.Steps(), "read").Result.Set([]byte{1}, 0)
			_, err = res.Refs()
			Expect(errors.Is(err, denc.ErrShortBuffer)).To(BeTrue(), "%v", err)
		})
	})
})

var _ = Describe("obj_refcount", func() {
	It("reads version 1, which had no retired refs", func() {
		// Hand-built: the corpus's version 1 objects never hold an inactive ref.
		e := denc.NewEncoder()
		f := e.BeginStruct(1, 1)
		e.U32(2)
		e.String("a\x00")
		e.Bool(false)
		e.String("b")
		e.Bool(true)
		e.EndStruct(f)
		Expect(decodeWhole(e.Bytes(), refcount.DecodeRefcount)).To(Equal(refcount.Refcount{
			Refs: map[string]bool{"a\x00": false, "b": true},
		}))
	})
	It("writes refs sorted and retired refs as a sorted set", func() {
		rc := refcount.Refcount{
			Refs:        map[string]bool{"z": true, "a": false},
			RetiredRefs: []string{"y", "b", "y"},
		}
		e := denc.NewEncoder()
		f := e.BeginStruct(2, 1)
		e.U32(2)
		e.String("a")
		e.Bool(false)
		e.String("z")
		e.Bool(true)
		e.U32(2)
		e.String("b")
		e.String("y")
		e.EndStruct(f)
		got := denc.NewEncoder()
		rc.Encode(got, denc.Squid)
		Expect(got.Bytes()).To(Equal(e.Bytes()))
	})
	It("reads retired refs as std::set does, sorted with the first of each duplicate", func() {
		e := denc.NewEncoder()
		f := e.BeginStruct(2, 1)
		e.U32(0)
		e.U32(3)
		e.String("y")
		e.String("b")
		e.String("y")
		e.EndStruct(f)
		Expect(decodeWhole(e.Bytes(), refcount.DecodeRefcount).RetiredRefs).To(Equal([]string{"b", "y"}))
	})
})
