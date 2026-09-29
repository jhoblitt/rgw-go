package goceph_test

import (
	"bytes"
	"errors"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"

	"github.com/ceph/go-ceph/rados"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/radosclient/goceph"
)

// recorder records the go-ceph builder calls the translator makes, in order,
// and the buffers it hands to Read.
type recorder struct {
	calls []string
	bufs  [][]byte
}

func (r *recorder) add(call string) { r.calls = append(r.calls, call) }

func (r *recorder) AssertExists()          { r.add("AssertExists") }
func (r *recorder) AssertVersion(v uint64) { r.add("AssertVersion " + strconv.FormatUint(v, 10)) }
func (r *recorder) CmpXattr(name string, op rados.CmpXattrOp, value []byte) {
	r.add("CmpXattr " + name + " " + strconv.Itoa(int(op)) + " " + string(value))
}

func (r *recorder) Read(offset uint64, buf []byte) *rados.ReadOpReadStep {
	r.add("Read " + strconv.FormatUint(offset, 10) + " " + strconv.Itoa(len(buf)))
	r.bufs = append(r.bufs, buf)
	return new(rados.ReadOpReadStep)
}

func (r *recorder) Stat() *rados.ReadOpStatStep {
	r.add("Stat")
	return new(rados.ReadOpStatStep)
}

func (r *recorder) GetXattrs() *rados.ReadOpGetXattrsStep {
	r.add("GetXattrs")
	return new(rados.ReadOpGetXattrsStep)
}

func (r *recorder) GetOmapValues(startAfter, filterPrefix string, maxReturn uint64) *rados.GetOmapStep {
	r.add("GetOmapValues " + startAfter + " " + filterPrefix + " " + strconv.FormatUint(maxReturn, 10))
	return new(rados.GetOmapStep)
}

func (r *recorder) GetOmapValuesByKeys(keys []string) *rados.ReadOpOmapGetValsByKeysStep {
	r.add("GetOmapValuesByKeys " + strconv.Itoa(len(keys)))
	return new(rados.ReadOpOmapGetValsByKeysStep)
}

func (r *recorder) GetOmapKeys(startAfter string, maxReturn uint64) *rados.ReadOpOmapGetKeysStep {
	r.add("GetOmapKeys " + startAfter + " " + strconv.FormatUint(maxReturn, 10))
	return new(rados.ReadOpOmapGetKeysStep)
}

func (r *recorder) Exec(class, method string, in []byte) *rados.ReadOpExecStep {
	r.add("Exec " + class + "." + method + " " + string(in))
	return new(rados.ReadOpExecStep)
}

// writeRecorder is the write-op counterpart of recorder; its Exec returns nothing.
type writeRecorder struct{ recorder }

func (w *writeRecorder) Exec(class, method string, in []byte) {
	w.add("Exec " + class + "." + method + " " + string(in))
}

func (w *writeRecorder) Create(exclusive rados.CreateOption) {
	w.add("Create " + strconv.FormatBool(exclusive == rados.CreateExclusive))
}
func (w *writeRecorder) Remove() { w.add("Remove") }
func (w *writeRecorder) SetFlags(flags rados.OpFlags) {
	w.add("SetFlags " + strconv.FormatUint(uint64(flags), 10))
}

func (w *writeRecorder) WriteFull(b []byte) {
	w.add("WriteFull " + strconv.Itoa(len(b)))
}

func (w *writeRecorder) Write(b []byte, offset uint64) {
	w.add("Write " + strconv.Itoa(len(b)) + " " + strconv.FormatUint(offset, 10))
}
func (w *writeRecorder) Append(b []byte) { w.add("Append " + strconv.Itoa(len(b))) }
func (w *writeRecorder) Zero(offset, length uint64) {
	w.add("Zero " + strconv.FormatUint(offset, 10) + " " + strconv.FormatUint(length, 10))
}

func (w *writeRecorder) Truncate(offset uint64) { w.add("Truncate " + strconv.FormatUint(offset, 10)) }

func (w *writeRecorder) SetXattr(name string, value []byte) {
	w.add("SetXattr " + name + " " + strconv.Itoa(len(value)))
}
func (w *writeRecorder) RmXattr(name string) { w.add("RmXattr " + name) }
func (w *writeRecorder) SetOmap(pairs map[string][]byte) {
	w.add("SetOmap " + strconv.Itoa(len(pairs)))
}
func (w *writeRecorder) RmOmapKeys(keys []string) { w.add("RmOmapKeys " + strconv.Itoa(len(keys))) }
func (w *writeRecorder) CleanOmap()               { w.add("CleanOmap") }
func (w *writeRecorder) OmapCmp(key string, op rados.CmpXattrOp, value []byte) {
	w.add("OmapCmp " + key + " " + strconv.Itoa(int(op)) + " " + string(value))
}

func (w *writeRecorder) SetAllocationHint(objectSize, writeSize uint64, flags rados.AllocHintFlags) {
	w.add("SetAllocationHint " + strconv.FormatUint(objectSize, 10) + " " +
		strconv.FormatUint(writeSize, 10) + " " + strconv.FormatUint(uint64(flags), 10))
}

// codeErr stands in for go-ceph's errno-carrying error, which reports its
// errno, negative as librados returns it, through ErrorCode.
type codeErr int

func (e codeErr) Error() string  { return "errno " + strconv.Itoa(int(e)) }
func (e codeErr) ErrorCode() int { return int(e) }

var _ = Describe("translating seam ops", func() {
	It("turns stat, xattrs and read into three go-ceph steps in the same order", func() {
		op := radosclient.NewReadOp()
		op.Stat()
		op.GetXattrs()
		op.Read(4, 16)

		rec := &recorder{}
		Expect(goceph.TranslateRead(rec, op)).To(Succeed())
		Expect(rec.calls).To(Equal([]string{"Stat", "GetXattrs", "Read 4 16"}))
	})

	It("reads into the caller's buffer, and allocates one otherwise", func() {
		op := radosclient.NewReadOp()
		buf := make([]byte, 64)
		op.ReadInto(0, buf)
		op.Read(64, 32)

		rec := &recorder{}
		Expect(goceph.TranslateRead(rec, op)).To(Succeed())
		Expect(rec.calls).To(Equal([]string{"Read 0 64", "Read 64 32"}))
		Expect(rec.bufs).To(HaveLen(2))
		Expect(&rec.bufs[0][0]).To(BeIdenticalTo(&buf[0]), "the caller's buffer")
		Expect(rec.bufs[1]).To(HaveLen(32), "the allocated buffer")
	})

	It("translates every read step", func() {
		op := radosclient.NewReadOp()
		op.AssertExists()
		op.AssertVersion(7)
		op.CmpXattr("user.rgw.idtag", radosclient.CmpEQ, []byte("t"))
		op.OmapGetVals("a", "p", 5)
		op.OmapGetValsByKeys([]string{"k1", "k2"})
		op.OmapGetKeys("b", 6)
		op.Exec("version", "read", []byte("in"))

		rec := &recorder{}
		Expect(goceph.TranslateRead(rec, op)).To(Succeed())
		Expect(rec.calls).To(Equal([]string{
			"AssertExists",
			"AssertVersion 7",
			"CmpXattr user.rgw.idtag 1 t",
			"GetOmapValues a p 5",
			"GetOmapValuesByKeys 2",
			"GetOmapKeys b 6",
			"Exec version.read in",
		}))
	})

	It("translates every write step in order and passes empty buffers through", func() {
		op := radosclient.NewWriteOp()
		op.AssertExists()
		op.AssertVersion(3)
		op.CmpXattr("user.rgw.idtag", radosclient.CmpNE, []byte("x"))
		op.Create(true)
		op.Create(false)
		op.WriteFull(nil)
		op.Write([]byte("abc"), 9)
		op.Append([]byte("d"))
		op.Zero(1, 2)
		op.Truncate(8)
		op.SetXattr("user.rgw.etag", nil)
		op.RmXattr("user.rgw.old")
		op.OmapSet(map[string][]byte{"k": nil})
		op.OmapRmKeys([]string{"k"})
		op.OmapClear()
		op.OmapCmp("k", radosclient.CmpGT, []byte("v"))
		op.SetAllocHint(4096, 1024, 2)
		op.Exec("version", "set", []byte("in"))
		op.Remove()
		op.SetStepFlags(radosclient.StepFlagFailOK)
		op.Create(false)

		rec := &writeRecorder{}
		Expect(goceph.TranslateWrite(rec, op)).To(Succeed())
		Expect(rec.calls).To(Equal([]string{
			"AssertExists",
			"AssertVersion 3",
			"CmpXattr user.rgw.idtag 2 x",
			"Create true",
			"Create false",
			"WriteFull 0",
			"Write 3 9",
			"Append 1",
			"Zero 1 2",
			"Truncate 8",
			"SetXattr user.rgw.etag 0",
			"RmXattr user.rgw.old",
			"SetOmap 1",
			"RmOmapKeys 1",
			"CleanOmap",
			"OmapCmp k 3 v",
			"SetAllocationHint 4096 1024 2",
			"Exec version.set in",
			"Remove",
			"SetFlags 2",
			"Create false",
		}))
	})

	It("rejects step flags that no step precedes, which librados would abort on", func() {
		op := radosclient.NewWriteOp()
		op.SetStepFlags(radosclient.StepFlagFailOK)
		op.Remove()
		rec := &writeRecorder{}
		Expect(goceph.TranslateWrite(rec, op)).To(MatchError(radosclient.ErrBadOp))
		Expect(rec.calls).To(BeEmpty())
	})

	It("rejects a step flag librados does not pass on", func() {
		op := radosclient.NewWriteOp()
		op.Remove()
		op.SetStepFlags(0x80) // FADVISE_FUA, which librados drops
		Expect(goceph.TranslateWrite(&writeRecorder{}, op)).To(MatchError(radosclient.ErrBadOp))
	})

	It("rejects an unknown comparison operator", func() {
		op := radosclient.NewReadOp()
		op.CmpXattr("n", radosclient.CmpOp(9), nil)
		Expect(goceph.TranslateRead(&recorder{}, op)).To(MatchError(radosclient.ErrBadOp))
	})

	It("maps the seam's op flags onto go-ceph's", func() {
		Expect(goceph.TranslateFlags(radosclient.OpFlagNone)).To(Equal(rados.OperationNoFlag))
		Expect(goceph.TranslateFlags(radosclient.OpFlagReturnVec)).To(Equal(rados.OperationReturnVec))
		Expect(goceph.TranslateFlags(radosclient.OpFlagBalanceReads | radosclient.OpFlagIgnoreCache)).
			To(Equal(rados.OperationBalanceReads | rados.OperationIgnoreCache))
		Expect(goceph.TranslateFlags(radosclient.OpFlagLocalizeReads)).To(Equal(rados.OperationLocalizeReads))
	})
})

var _ = Describe("mapping go-ceph errors", func() {
	DescribeTable("an errno maps to its sentinel",
		func(errno int, sentinel error) {
			err := goceph.ToSeamError("read obj", codeErr(-errno))
			Expect(err).To(MatchError(sentinel))

			var seamErr *radosclient.Error
			Expect(errors.As(err, &seamErr)).To(BeTrue())
			Expect(seamErr.Op).To(Equal("read obj"))

			wrapped := goceph.ToSeamError("write obj", rados.OperationError{OpError: codeErr(-errno)})
			Expect(wrapped).To(MatchError(sentinel))
		},
		Entry("ENOENT", 2, radosclient.ErrNotFound),
		Entry("EEXIST", 17, radosclient.ErrExists),
		Entry("ECANCELED", 125, radosclient.ErrCanceled),
		Entry("ERR_BUSY_RESHARDING", 2300, radosclient.ErrBusyResharding),
	)

	It("picks the first failing step in step order, every time", func() {
		opErr := rados.OperationError{StepErrors: map[int]error{
			7: codeErr(-2), 3: codeErr(-17), 5: codeErr(-125), 4: nil,
		}}
		for range 100 {
			Expect(goceph.ToSeamError("read obj", opErr)).To(MatchError(radosclient.ErrExists))
		}
	})

	It("prefers the op's own error to its steps'", func() {
		opErr := rados.OperationError{OpError: codeErr(-125), StepErrors: map[int]error{0: codeErr(-2)}}
		Expect(goceph.ToSeamError("write obj", opErr)).To(MatchError(radosclient.ErrCanceled))
	})

	It("gives a step its own error when librados did not fail the op", func() {
		stepOnly := rados.OperationError{StepErrors: map[int]error{2: codeErr(-61)}}
		Expect(goceph.StepError("read obj", stepOnly, 2)).To(MatchError(radosclient.ErrNoData))
		Expect(goceph.StepError("read obj", stepOnly, 1)).To(Succeed())
		Expect(goceph.StepError("read obj", nil, 1)).To(Succeed())
	})

	It("gives every step the op's error when librados failed the op", func() {
		// The -EIO is what librados records for an action the OSD never ran.
		failed := rados.OperationError{OpError: codeErr(-2), StepErrors: map[int]error{0: codeErr(-5), 1: codeErr(-2)}}
		Expect(goceph.StepError("read obj", failed, 0)).To(MatchError(radosclient.ErrNotFound))
		Expect(goceph.StepError("read obj", failed, 1)).To(MatchError(radosclient.ErrNotFound))
		Expect(goceph.StepError("read obj", failed, 2)).To(MatchError(radosclient.ErrNotFound))
		Expect(goceph.StepError("read obj", codeErr(-2), 0)).To(MatchError(radosclient.ErrNotFound))
	})

	It("finds a step's errno when the op itself reports none", func() {
		err := goceph.ToSeamError("read obj", rados.OperationError{StepErrors: map[int]error{1: codeErr(-61)}})
		Expect(err).To(MatchError(radosclient.ErrNoData))
	})

	It("wraps an error without an errno", func() {
		err := goceph.ToSeamError("read obj", rados.ErrInvalidIOContext)
		Expect(err).To(MatchError(rados.ErrInvalidIOContext))
	})

	It("leaves nil alone", func() {
		Expect(goceph.ToSeamError("read obj", nil)).To(Succeed())
	})
})

// go-ceph's WriteOp builds the same OperationError for a synchronous Operate
// and for AioCompletion.Err, in callback and pipe mode alike: OpError carries
// librados's return code whenever it is nonzero, and a write step whose
// prval is nonzero reports it under the step's index.
var _ = Describe("a positive librados return", func() {
	var (
		op   *radosclient.WriteOp
		exec *radosclient.ExecResult
	)

	BeforeEach(func() {
		op = radosclient.NewWriteOp()
		op.WriteFull([]byte("x"))
		exec = op.Exec("hello", "write_return_data", nil)
	})

	DescribeTable("is success for a write op and its steps",
		func(err error) {
			Expect(goceph.CompleteWrite(&writeRecorder{}, op, err)).To(Succeed())
			out, execErr := exec.Bytes()
			Expect(execErr).NotTo(HaveOccurred())
			Expect(out).To(BeNil())
		},
		Entry("on the op", rados.OperationError{OpError: codeErr(2300)}),
		Entry("on the op and the exec step's prval",
			rados.OperationError{OpError: codeErr(42), StepErrors: map[int]error{1: codeErr(42)}}),
		Entry("on the exec step's prval alone", rados.OperationError{StepErrors: map[int]error{1: codeErr(42)}}),
		Entry("as a bare errno", codeErr(2300)),
	)

	It("leaves a step's own negative return an error", func() {
		mixed := rados.OperationError{OpError: codeErr(2300), StepErrors: map[int]error{0: codeErr(42), 1: codeErr(-61)}}
		Expect(goceph.CompleteWrite(&writeRecorder{}, op, mixed)).To(MatchError(radosclient.ErrNoData))
		_, err := exec.Bytes()
		Expect(err).To(MatchError(radosclient.ErrNoData))
		Expect(goceph.StepError("write obj", mixed, 0)).To(Succeed())
		Expect(goceph.StepError("write obj", mixed, 1)).To(MatchError(radosclient.ErrNoData))
	})

	DescribeTable("leaves a negative return as it was",
		func(err error) {
			Expect(goceph.CompleteWrite(&writeRecorder{}, op, err)).To(MatchError(radosclient.ErrBusyResharding))
			_, execErr := exec.Bytes()
			Expect(execErr).To(MatchError(radosclient.ErrBusyResharding))
		},
		Entry("on the op", rados.OperationError{OpError: codeErr(-2300)}),
		Entry("on the op, over a positive step",
			rados.OperationError{OpError: codeErr(-2300), StepErrors: map[int]error{1: codeErr(42)}}),
		Entry("on the exec step's prval", rados.OperationError{StepErrors: map[int]error{1: codeErr(-2300)}}),
		Entry("as a bare errno", codeErr(-2300)),
	)
})

// readSteps and writeSteps build an op and return its steps.
func readSteps(build func(op *radosclient.ReadOp)) []radosclient.Step {
	op := radosclient.NewReadOp()
	build(op)
	return op.Steps()
}

func writeSteps(build func(op *radosclient.WriteOp)) []radosclient.Step {
	op := radosclient.NewWriteOp()
	build(op)
	return op.Steps()
}

var _ = Describe("an op's payload weight", func() {
	DescribeTable("is what the limiter takes in bytes, beside one operation",
		func(ctx SpecContext, steps []radosclient.Step, want int64) {
			Expect(goceph.Weight(steps)).To(Equal(want))
			l := goceph.NewLimiter(1, 1<<30)
			Expect(l.Acquire(ctx, goceph.Weight(steps))).To(Succeed())
			Expect(l.Stats()).To(Equal(radosclient.Stats{InflightOps: 1, InflightBytes: want}))
		},
		Entry("a read's length, and nothing for a stat",
			readSteps(func(op *radosclient.ReadOp) {
				op.Read(0, 4<<20)
				op.Stat()
			}), int64(4<<20)),
		Entry("the length of a caller's read buffer",
			readSteps(func(op *radosclient.ReadOp) { op.ReadInto(8, make([]byte, 100)) }), int64(100)),
		Entry("a read op's exec input",
			readSteps(func(op *radosclient.ReadOp) { op.Exec("version", "read", []byte("1234567")) }), int64(7)),
		Entry("write-full and append data and a write op's exec input",
			writeSteps(func(op *radosclient.WriteOp) {
				op.WriteFull([]byte("abc"))
				op.Append([]byte("de"))
				op.Exec("version", "set", []byte("12345"))
			}), int64(10)),
		Entry("the data of a write at an offset",
			writeSteps(func(op *radosclient.WriteOp) { op.Write([]byte("abcd"), 1<<20) }), int64(4)),
		Entry("nothing, for an op with no payload, which still counts one operation",
			writeSteps(func(op *radosclient.WriteOp) {
				op.Remove()
				op.SetStepFlags(radosclient.StepFlagFailOK)
				op.Create(false)
			}), int64(0)),
	)
})

// unsetenv removes key from the environment until the spec ends.
func unsetenv(key string) {
	if v, ok := os.LookupEnv(key); ok {
		Expect(os.Unsetenv(key)).To(Succeed())
		DeferCleanup(os.Setenv, key, v)
	}
}

// captureLogs sends slog's default logger to a buffer until the spec ends.
// slog.SetDefault also points the log package's output at the new handler,
// and restoring the old default logger leaves it there, so the cleanup puts
// log's writer and flags back too.
func captureLogs() *bytes.Buffer {
	var buf bytes.Buffer
	oldLogger, oldWriter, oldFlags := slog.Default(), log.Writer(), log.Flags()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	DeferCleanup(func() {
		slog.SetDefault(oldLogger)
		log.SetOutput(oldWriter)
		log.SetFlags(oldFlags)
	})
	return &buf
}

var _ = Describe("Config", func() {
	It("defaults to the ceph cluster, client.admin and callback completions", func() {
		cfg := goceph.WithDefaults(goceph.Config{})
		Expect(cfg.Cluster).To(Equal("ceph"))
		Expect(cfg.Name).To(Equal("client.admin"))
		Expect(cfg.Mode).To(Equal(goceph.ModeCallback))
	})

	It("keeps what the caller set", func() {
		cfg := goceph.WithDefaults(goceph.Config{Cluster: "c", Name: "client.rgw.a", Mode: goceph.ModePipe})
		Expect(cfg).To(Equal(goceph.Config{Cluster: "c", Name: "client.rgw.a", Mode: goceph.ModePipe}))
	})

	DescribeTable("refuses a negative in-flight limit before touching librados",
		func(ctx SpecContext, cfg goceph.Config) {
			cfg.ConfigFile = "/nonexistent/ceph.conf"
			cluster, err := goceph.Connect(ctx, cfg)
			Expect(err).To(MatchError(radosclient.ErrBadOp))
			Expect(cluster).To(BeNil())
		},
		Entry("operations", goceph.Config{MaxInflightOps: -1}),
		Entry("bytes", goceph.Config{MaxInflightBytes: -1}),
	)

	Describe("reading the config file", func() {
		// cluster names the files librados searches for by default,
		// $home/.ceph/<cluster>.conf among them, so no file on the host
		// matches.
		const cluster = "rgwgoconftest"

		var home string

		BeforeEach(func() {
			unsetenv("CEPH_CONF")
			home = GinkgoT().TempDir()
			GinkgoT().Setenv("HOME", home)
			Expect(os.Mkdir(filepath.Join(home, ".ceph"), 0o700)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(home, ".ceph", cluster+".conf"),
				[]byte("[global]\nobjecter_inflight_ops = 77\n"), 0o600)).To(Succeed())
		})

		It("reads the first file of the default search", func(ctx SpecContext) {
			Expect(goceph.ConfiguredOption(ctx, goceph.Config{Cluster: cluster}, "objecter_inflight_ops")).To(Equal("77"))
		})

		It("skips the default search under NoConfigFile", func(ctx SpecContext) {
			Expect(goceph.ConfiguredOption(ctx, goceph.Config{Cluster: cluster, NoConfigFile: true}, "objecter_inflight_ops")).
				To(Equal("1024"), "librados's default")
		})

		It("still reads a named file under NoConfigFile, as ceph reads -c under --no-config-file", func(ctx SpecContext) {
			cfg := goceph.Config{Cluster: "ceph", ConfigFile: filepath.Join(home, ".ceph", cluster+".conf"), NoConfigFile: true}
			Expect(goceph.ConfiguredOption(ctx, cfg, "objecter_inflight_ops")).To(Equal("77"))
		})

		It("continues with librados's defaults and a warning when the default search finds no file", func(ctx SpecContext) {
			logs := captureLogs()
			Expect(goceph.ConfiguredOption(ctx, goceph.Config{Cluster: cluster + "-missing"}, "objecter_inflight_ops")).To(Equal("1024"))
			Expect(logs.String()).To(ContainSubstring(`"msg":"no ceph.conf found, continuing with defaults"`))
		})

		It("fails when a named file is missing", func(ctx SpecContext) {
			_, err := goceph.ConfiguredOption(ctx, goceph.Config{Cluster: cluster, ConfigFile: filepath.Join(home, "missing.conf")}, "fsid")
			Expect(err).To(MatchError(radosclient.ErrNotFound))
		})
	})
})
