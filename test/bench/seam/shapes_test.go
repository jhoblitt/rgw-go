package seam_test

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/test/bench/seam"
)

// poolID is the pool id the recorder reports.
const poolID = 7

// call is one op handed to the recorder, with the write op itself for what
// is not a step.
type call struct {
	oid   string
	steps []radosclient.Step
	write *radosclient.WriteOp
}

// recorder is a radosclient.Pool that records the ops handed to Read and
// Write and runs none of them. It embeds the interface so that methods the
// seam gains do not break it; one it does not implement panics.
type recorder struct {
	radosclient.Pool
	readErr   error            // what every Read returns
	writeErrs map[string]error // what a Write to the object returns
	listed    []string         // the objects ListObjects reports

	mu    sync.Mutex
	calls []call
}

func (r *recorder) ID() int64 { return poolID }

func (r *recorder) Read(_ context.Context, oid string, op *radosclient.ReadOp, _ radosclient.OpFlags) (uint64, error) {
	r.record(call{oid: oid, steps: op.Steps()})
	return 0, r.readErr
}

func (r *recorder) Write(_ context.Context, oid string, op *radosclient.WriteOp, _ radosclient.OpFlags) (uint64, error) {
	r.record(call{oid: oid, steps: op.Steps(), write: op})
	return 0, r.writeErrs[oid]
}

func (r *recorder) ListObjects(_ context.Context, fn func(oid, locator string) error) error {
	for _, oid := range r.listed {
		if err := fn(oid, ""); err != nil {
			return err
		}
	}
	return nil
}

func (r *recorder) record(c call) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, c)
}

func (r *recorder) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = nil
}

func (r *recorder) recorded() []call {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.calls)
}

func (r *recorder) stepNames() []string {
	var names []string
	for _, c := range r.recorded() {
		for _, s := range c.steps {
			names = append(names, fmt.Sprintf("%T", s))
		}
	}
	return names
}

func (r *recorder) execs() []*radosclient.ExecStep {
	var out []*radosclient.ExecStep
	for _, c := range r.recorded() {
		for _, s := range c.steps {
			if x, ok := s.(*radosclient.ExecStep); ok {
				out = append(out, x)
			}
		}
	}
	return out
}

func (r *recorder) execMethods() []string {
	var names []string
	for _, x := range r.execs() {
		names = append(names, x.Method)
	}
	return names
}

func shapeNamed(name string) seam.Shape {
	GinkgoHelper()
	shapes, err := seam.ByName([]string{name})
	Expect(err).NotTo(HaveOccurred())
	Expect(shapes).To(HaveLen(1))
	return shapes[0]
}

// runOnce prepares sh on rec for one worker, forgets what that recorded, and
// runs iteration i.
func runOnce(ctx context.Context, sh seam.Shape, rec *recorder, i int) {
	GinkgoHelper()
	Expect(sh.Prepare(ctx, rec, denc.Squid, 1)).To(Succeed())
	rec.reset()
	Expect(sh.Run(ctx, rec, 0, i)).To(Succeed())
}

var _ = Describe("shapes", func() {
	DescribeTable("compose radosgw's steps",
		func(ctx SpecContext, name string, want []string) {
			rec := &recorder{}
			runOnce(ctx, shapeNamed(name), rec, 0)
			Expect(rec.stepNames()).To(Equal(want), "shape %s", name)
		},
		Entry("headread4k is the version read, xattrs, stat and the first chunk in one op", "headread4k",
			[]string{"*radosclient.ExecStep", "*radosclient.GetXattrsStep", "*radosclient.StatStep", "*radosclient.ReadStep"}),
		// prepare_atomic_modification's create and two tags, then _do_write_meta's data,
		// manifest, the three attrs in name order, pg_ver and source_zone; no alloc hint for
		// an uncompressed object; SetMtime is an op attribute, not a step.
		Entry("headwrite4k is create, two tags, data, four xattrs, one exec and the source zone", "headwrite4k",
			[]string{
				"*radosclient.CreateStep", "*radosclient.SetXattrStep", "*radosclient.SetXattrStep", "*radosclient.WriteFullStep",
				"*radosclient.SetXattrStep", "*radosclient.SetXattrStep", "*radosclient.SetXattrStep", "*radosclient.SetXattrStep",
				"*radosclient.ExecStep", "*radosclient.SetXattrStep",
			}),
		Entry("indexrtt is guard+prepare then guard+complete", "indexrtt",
			[]string{
				"*radosclient.AssertExistsStep", "*radosclient.ExecStep", "*radosclient.ExecStep",
				"*radosclient.AssertExistsStep", "*radosclient.ExecStep", "*radosclient.ExecStep",
			}),
		Entry("read4k is one read", "read4k", []string{"*radosclient.ReadStep"}),
		Entry("write4k is one whole-object write", "write4k", []string{"*radosclient.WriteFullStep"}),
	)

	It("lists every shape in sweep order", func() {
		var names []string
		for _, sh := range seam.Shapes() {
			names = append(names, sh.Name())
		}
		Expect(names).To(Equal([]string{"read4k", "write4k", "read4m", "write4m", "headread4k", "headwrite4k", "indexrtt"}))
	})

	It("returns the named shapes in the order asked", func() {
		shapes, err := seam.ByName([]string{"indexrtt", "read4k"})
		Expect(err).NotTo(HaveOccurred())
		Expect(shapes).To(HaveExactElements(
			HaveField("Name()", "indexrtt"),
			HaveField("Name()", "read4k"),
		))
	})

	It("rejects an unknown shape by name", func() {
		_, err := seam.ByName([]string{"read4k", "bogus"})
		Expect(err).To(MatchError(ContainSubstring("bogus")))
	})

	It("sizes each shape by the data extent it writes or asks to read", func() {
		got := map[string][2]int{}
		for _, sh := range seam.Shapes() {
			got[sh.Name()] = [2]int{sh.Size(), sh.Ops()}
		}
		Expect(got).To(Equal(map[string][2]int{
			"read4k":  {4 << 10, 1},
			"write4k": {4 << 10, 1},
			"read4m":  {4 << 20, 1},
			"write4m": {4 << 20, 1},
			// radosgw asks for the whole first chunk, rgw_max_chunk_size, however
			// small the object, and the objecter charges a read what it asks for.
			"headread4k":  {4 << 20, 1},
			"headwrite4k": {4 << 10, 1},
			"indexrtt":    {0, 2},
		}))
	})

	It("writes or asks to read the data extent its size says", func(ctx SpecContext) {
		for _, sh := range seam.Shapes() {
			rec := &recorder{}
			runOnce(ctx, sh, rec, 0)
			var n uint64
			for _, c := range rec.recorded() {
				for _, s := range c.steps {
					switch x := s.(type) {
					case *radosclient.ReadStep:
						n += x.Length
					case *radosclient.WriteFullStep:
						n += uint64(len(x.Data))
					}
				}
			}
			Expect(n).To(BeEquivalentTo(sh.Size()), "shape %s", sh.Name())
		}
	})

	DescribeTable("prepare an object per worker for the read shapes to read",
		func(ctx SpecContext, name, prefix string, want []string) {
			rec := &recorder{}
			Expect(shapeNamed(name).Prepare(ctx, rec, denc.Squid, 2)).To(Succeed())
			var oids []string
			for _, c := range rec.recorded() {
				oids = append(oids, c.oid)
				var steps []string
				for _, s := range c.steps {
					steps = append(steps, fmt.Sprintf("%T", s))
				}
				Expect(steps).To(Equal(want), "object %s", c.oid)
				Expect(c.steps[0]).To(HaveField("Data", HaveLen(4<<10)), "object %s", c.oid)
			}
			Expect(oids).To(ConsistOf(prefix+"0", prefix+"1"))
		},
		Entry("read4k writes each worker's 4 KiB source", "read4k", "src-", []string{"*radosclient.WriteFullStep"}),
		Entry("headread4k writes each worker's 4 KiB head with seven xattrs", "headread4k", "head-",
			[]string{
				"*radosclient.WriteFullStep", "*radosclient.SetXattrStep", "*radosclient.SetXattrStep",
				"*radosclient.SetXattrStep", "*radosclient.SetXattrStep", "*radosclient.SetXattrStep",
				"*radosclient.SetXattrStep", "*radosclient.SetXattrStep",
			}),
	)

	DescribeTable("name a new object for every iteration and every Prepare",
		func(ctx SpecContext, name string) {
			sh := shapeNamed(name)
			rec := &recorder{}
			Expect(sh.Prepare(ctx, rec, denc.Squid, 2)).To(Succeed())
			Expect(sh.Run(ctx, rec, 0, 0)).To(Succeed())
			Expect(sh.Run(ctx, rec, 0, 1)).To(Succeed())
			Expect(sh.Run(ctx, rec, 1, 0)).To(Succeed())
			Expect(sh.Prepare(ctx, rec, denc.Squid, 2)).To(Succeed())
			Expect(sh.Run(ctx, rec, 0, 0)).To(Succeed())
			var oids []string
			for _, c := range rec.recorded() {
				oids = append(oids, c.oid)
			}
			Expect(oids).To(HaveLen(4))
			Expect(slices.Compact(slices.Sorted(slices.Values(oids)))).To(HaveLen(4), "objects %q", oids)
		},
		Entry("write4k", "write4k"),
		Entry("headwrite4k, whose create is exclusive", "headwrite4k"),
	)

	It("sets radosgw's head xattrs in its order, the write tag NUL-terminated", func(ctx SpecContext) {
		rec := &recorder{}
		runOnce(ctx, shapeNamed("headwrite4k"), rec, 0)
		calls := rec.recorded()
		Expect(calls).To(HaveLen(1))
		var names []string
		values := map[string]string{}
		for _, s := range calls[0].steps {
			switch x := s.(type) {
			case *radosclient.SetXattrStep:
				names = append(names, x.Name)
				values[x.Name] = string(x.Value)
			case *radosclient.ExecStep:
				names = append(names, x.Class+"."+x.Method)
				d := denc.NewDecoder(x.In)
				Expect(rgw.DecodeStorePGVerOp(d).Attr).To(Equal("user.rgw.pg_ver"))
				Expect(d.Err()).NotTo(HaveOccurred())
			}
		}
		Expect(names).To(Equal([]string{
			"user.rgw.idtag", "user.rgw.tail_tag", "user.rgw.manifest", "user.rgw.acl",
			"user.rgw.content_type", "user.rgw.etag", "rgw.obj_store_pg_ver", "user.rgw.source_zone",
		}))
		Expect(values["user.rgw.idtag"]).To(MatchRegexp(`^_[A-Za-z0-9_-]{31}\x00$`))
		Expect(values).To(And(
			HaveKeyWithValue("user.rgw.tail_tag", values["user.rgw.idtag"]),
			HaveKeyWithValue("user.rgw.manifest", HaveLen(180)),
			HaveKeyWithValue("user.rgw.acl", HaveLen(200)),
			HaveKeyWithValue("user.rgw.content_type", "application/octet-stream\x00"),
			HaveKeyWithValue("user.rgw.etag", MatchRegexp(`^[0-9a-f]{32}$`)),
			HaveKeyWithValue("user.rgw.source_zone", HaveLen(4)),
		))
		Expect(calls[0].steps[0]).To(Equal(&radosclient.CreateStep{Exclusive: true}))
		mtime, _ := calls[0].write.Mtime()
		Expect(mtime).To(BeTemporally("~", time.Now(), time.Minute), "radosgw stamps the head's mtime on the op")
	})

	It("has the objecter charge a head write its xattrs beside the data extent its size counts", func(ctx SpecContext) {
		sh := shapeNamed("headwrite4k")
		rec := &recorder{}
		runOnce(ctx, sh, rec, 0)
		// A write op's charge is the input of its write-mode steps: the data,
		// each xattr's name and value. The create carries none, and a class
		// call is charged nothing.
		charge := 0
		for _, c := range rec.recorded() {
			for _, s := range c.steps {
				switch x := s.(type) {
				case *radosclient.WriteFullStep:
					charge += len(x.Data)
				case *radosclient.SetXattrStep:
					charge += len(x.Name) + len(x.Value)
				}
			}
		}
		Expect([]int{sh.Size(), charge}).To(Equal([]int{4096, 4717}))
	})

	It("names the exec methods radosgw calls", func(ctx SpecContext) {
		rec := &recorder{}
		runOnce(ctx, shapeNamed("indexrtt"), rec, 0)
		Expect(rec.execMethods()).To(Equal([]string{"guard_bucket_resharding", "bucket_prepare_op", "guard_bucket_resharding", "bucket_complete_op"}))
	})

	It("prepares and completes the same index entry on the worker's shard", func(ctx SpecContext) {
		rec := &recorder{}
		runOnce(ctx, shapeNamed("indexrtt"), rec, 41)
		calls := rec.recorded()
		Expect(calls).To(HaveLen(2))
		Expect(calls[0].oid).To(MatchRegexp(`^\.dir\.bench-\d+\.0$`))
		Expect(calls[1].oid).To(Equal(calls[0].oid))
		var prep rgw.PrepareOp
		var comp rgw.CompleteOp
		for _, x := range rec.execs() {
			d := denc.NewDecoder(x.In)
			switch x.Method {
			case "guard_bucket_resharding":
				Expect(rgw.DecodeGuardOp(d).RetErr).To(BeEquivalentTo(-rgw.ErrBusyResharding))
			case "bucket_prepare_op":
				prep = rgw.DecodePrepareOp(d)
			case "bucket_complete_op":
				comp = rgw.DecodeCompleteOp(d)
			}
			Expect(d.Err()).NotTo(HaveOccurred(), "decoding %s", x.Method)
		}
		Expect(prep.Op).To(Equal(rgw.OpAdd))
		Expect(prep.Key.Name).To(MatchRegexp(`^key-\d+-0-41$`))
		Expect(prep.Tag).To(MatchRegexp(`^_[A-Za-z0-9_-]{31}$`), "append_rand_alpha's tag, without the NUL the head's xattrs carry")
		Expect(comp.Op).To(Equal(rgw.OpAdd))
		Expect(comp.Key).To(Equal(prep.Key))
		Expect(comp.Tag).To(Equal(prep.Tag))
		Expect(comp.Ver).To(Equal(rgw.EntryVer{Pool: poolID, Epoch: 42}))
		Expect(comp.Meta).To(And(
			HaveField("Category", rgw.CategoryMain),
			HaveField("Size", uint64(4096)),
			HaveField("AccountedSize", uint64(4096)),
			HaveField("ETag", MatchRegexp(`^[0-9a-f]{32}$`)),
			HaveField("Owner", "bench"),
			HaveField("OwnerDisplayName", "bench"),
			HaveField("ContentType", "application/octet-stream"),
		))
	})

	It("creates a missing index shard with an exclusive create and bucket_init_index", func(ctx SpecContext) {
		rec := &recorder{readErr: &radosclient.Error{Errno: int32(syscall.ENOENT), Op: "read"}}
		Expect(shapeNamed("indexrtt").Prepare(ctx, rec, denc.Squid, 2)).To(Succeed())
		var created []string
		for _, c := range rec.recorded() {
			if c.write == nil {
				Expect(c.steps).To(HaveExactElements(BeAssignableToTypeOf(&radosclient.StatStep{})), "object %s", c.oid)
				continue
			}
			Expect(c.steps).To(HaveExactElements(
				Equal(&radosclient.CreateStep{Exclusive: true}),
				HaveField("Method", "bucket_init_index"),
			), "object %s", c.oid)
			created = append(created, c.oid)
		}
		Expect(created).To(ConsistOf(MatchRegexp(`^\.dir\.bench-\d+\.0$`), MatchRegexp(`^\.dir\.bench-\d+\.1$`)))
	})

	It("leaves an existing index shard alone", func(ctx SpecContext) {
		rec := &recorder{}
		Expect(shapeNamed("indexrtt").Prepare(ctx, rec, denc.Squid, 2)).To(Succeed())
		Expect(rec.stepNames()).To(Equal([]string{"*radosclient.StatStep", "*radosclient.StatStep"}))
	})

	It("fails to prepare when a shard's stat fails otherwise", func(ctx SpecContext) {
		eio := &radosclient.Error{Errno: int32(syscall.EIO), Op: "read"}
		rec := &recorder{readErr: eio}
		Expect(shapeNamed("indexrtt").Prepare(ctx, rec, denc.Squid, 1)).To(MatchError(eio))
	})

	It("cleans up by removing every object listed, one already gone included", func(ctx SpecContext) {
		rec := &recorder{
			listed:    []string{"w-1-0-0", "src-0", ".dir.bench-1.0"},
			writeErrs: map[string]error{"src-0": &radosclient.Error{Errno: int32(syscall.ENOENT), Op: "remove"}},
		}
		Expect(shapeNamed("read4k").Cleanup(ctx, rec)).To(Succeed())
		var removed []string
		for _, c := range rec.recorded() {
			Expect(c.steps).To(HaveExactElements(BeAssignableToTypeOf(&radosclient.RemoveStep{})), "object %s", c.oid)
			removed = append(removed, c.oid)
		}
		Expect(removed).To(ConsistOf("w-1-0-0", "src-0", ".dir.bench-1.0"))
	})

	It("fails the cleanup when a removal fails otherwise", func(ctx SpecContext) {
		eio := &radosclient.Error{Errno: int32(syscall.EIO), Op: "remove"}
		rec := &recorder{listed: []string{"a", "b"}, writeErrs: map[string]error{"b": eio}}
		Expect(shapeNamed("indexrtt").Cleanup(ctx, rec)).To(MatchError(eio))
	})
})
