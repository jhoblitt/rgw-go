package version_test

import (
	"errors"
	"syscall"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cls/version"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/denc/goldentest"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// execStep asserts steps is a single exec of version.method and returns it.
func execStep(steps []radosclient.Step, method string) *radosclient.ExecStep {
	GinkgoHelper()
	Expect(steps).To(HaveLen(1))
	s, ok := steps[0].(*radosclient.ExecStep)
	Expect(ok).To(BeTrue(), "step is %T, not an exec", steps[0])
	Expect(s.Class).To(Equal("version"))
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

func encoded(v interface {
	Encode(*denc.Encoder, denc.Release)
},
) []byte {
	e := denc.NewEncoder()
	v.Encode(e, denc.Squid)
	return e.Bytes()
}

var _ = Describe("requests", func() {
	It("set sends every corpus cls_version_set_op byte for byte", func() {
		cs, ops := goldens("cls_version_set_op", version.DecodeSetOp)
		for i, c := range cs {
			w := radosclient.NewWriteOp()
			version.Set(w, ops[i].Objv, denc.Squid)
			Expect(execStep(w.Steps(), "set").In).To(Equal(c.ReEnc), c.Name)
		}
	})

	It("inc_conds and check_conds send the corpus ops whose one condition names objv", func() {
		for _, typ := range []string{"cls_version_inc_op", "cls_version_check_op"} {
			cs, ops := goldens(typ, version.DecodeCheckOp)
			matched := 0
			for i, c := range cs {
				op := ops[i]
				if len(op.Conds) != 1 || op.Conds[0].Ver != op.Objv {
					continue
				}
				matched++
				w := radosclient.NewWriteOp()
				if typ == "cls_version_inc_op" {
					version.IncConds(w, op.Objv, op.Conds[0].Cond, denc.Squid)
					Expect(execStep(w.Steps(), "inc_conds").In).To(Equal(c.ReEnc), c.Name)
				} else {
					version.Check(w, op.Objv, op.Conds[0].Cond, denc.Squid)
					Expect(execStep(w.Steps(), "check_conds").In).To(Equal(c.ReEnc), c.Name)
				}
			}
			Expect(matched).To(BeNumerically(">", 0), typ)
		}
	})

	It("inc sends an empty cls_version_inc_op", func() {
		w := radosclient.NewWriteOp()
		version.Inc(w, denc.Squid)
		// Hand-built: header, an empty obj_version, then an empty condition list.
		e := denc.NewEncoder()
		f := e.BeginStruct(1, 1)
		objv := e.BeginStruct(1, 1)
		e.U64(0)
		e.String("")
		e.EndStruct(objv)
		e.U32(0)
		e.EndStruct(f)
		Expect(execStep(w.Steps(), "inc").In).To(Equal(e.Bytes()))
	})

	It("check_conds encodes the condition as obj_version_cond with a u32 condition", func() {
		r := radosclient.NewReadOp()
		version.Check(r, version.ObjVersion{Ver: 7, Tag: "t"}, version.CondTagNE, denc.Squid)
		e := denc.NewEncoder()
		f := e.BeginStruct(1, 1)
		objv := e.BeginStruct(1, 1)
		e.U64(7)
		e.String("t")
		e.EndStruct(objv)
		e.U32(1)
		cond := e.BeginStruct(1, 1)
		objv = e.BeginStruct(1, 1)
		e.U64(7)
		e.String("t")
		e.EndStruct(objv)
		e.U32(7)
		e.EndStruct(cond)
		e.EndStruct(f)
		Expect(execStep(r.Steps(), "check_conds").In).To(Equal(e.Bytes()))
	})

	Describe("read", func() {
		var (
			r   *radosclient.ReadOp
			res *version.ReadResult
		)
		BeforeEach(func() {
			r = radosclient.NewReadOp()
			res = version.Read(r, denc.Squid)
		})

		It("sends no input", func() {
			Expect(execStep(r.Steps(), "read").In).To(BeEmpty())
		})
		It("decodes every corpus cls_version_read_ret", func() {
			cs, rets := goldens("cls_version_read_ret", version.DecodeReadRet)
			for i, c := range cs {
				execStep(r.Steps(), "read").Result.Set(c.Bin, 0)
				v, err := res.Version()
				Expect(err).NotTo(HaveOccurred())
				Expect(v).To(Equal(rets[i].Objv))
			}
		})
		It("reports the method's error", func() {
			execStep(r.Steps(), "read").Result.Set(nil, -int32(syscall.ENOENT))
			_, err := res.Version()
			Expect(err).To(MatchError(radosclient.ErrNotFound))
		})
		It("reports a reply that does not decode", func() {
			execStep(r.Steps(), "read").Result.Set([]byte{1, 1, 4, 0, 0, 0}, 0)
			_, err := res.Version()
			Expect(errors.Is(err, denc.ErrShortBuffer)).To(BeTrue(), "%v", err)
		})
		It("reports a read before the op ran", func() {
			_, err := res.Version()
			Expect(err).To(MatchError(radosclient.ErrIncomplete))
		})
	})
})

var _ = Describe("types", func() {
	It("keeps a condition beyond VER_COND_TAG_NE", func() {
		c := version.Condition{Ver: version.ObjVersion{Ver: 1, Tag: "x"}, Cond: 99}
		d := denc.NewDecoder(encoded(c))
		Expect(version.DecodeCondition(d)).To(Equal(c))
		Expect(d.Err()).NotTo(HaveOccurred())
	})
})
