//go:build integration

package goceph_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/radosclient/goceph"
)

// testPool is the pool hack/cluster/up.sh creates for this suite.
const testPool = "rgw-go-test"

// cephConf resolves RGW_GO_TEST_CEPH_CONF, taking a relative path from the
// module root so the documented hack/cluster/out/<release>/ceph.conf works
// from the package directory go test runs in.
func cephConf() string {
	conf := os.Getenv("RGW_GO_TEST_CEPH_CONF")
	if conf == "" || filepath.IsAbs(conf) {
		return conf
	}
	dir, err := os.Getwd()
	Expect(err).NotTo(HaveOccurred())
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, conf)
		}
		parent := filepath.Dir(dir)
		Expect(parent).NotTo(Equal(dir), "no go.mod above the working directory")
		dir = parent
	}
}

// expectedRelease is the release the cluster under test runs: the directory
// up.sh wrote its ceph.conf to, unless RGW_GO_TEST_CEPH_RELEASE names it.
func expectedRelease(conf string) string {
	if r := os.Getenv("RGW_GO_TEST_CEPH_RELEASE"); r != "" {
		return r
	}
	return filepath.Base(filepath.Dir(conf))
}

// osThreads reads this process's OS thread count.
func osThreads() int {
	f, err := os.Open("/proc/self/status")
	Expect(err).NotTo(HaveOccurred())
	defer func() { Expect(f.Close()).To(Succeed()) }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "Threads:"); ok {
			n, err := strconv.Atoi(strings.TrimSpace(v))
			Expect(err).NotTo(HaveOccurred())
			return n
		}
	}
	Expect(sc.Err()).NotTo(HaveOccurred())
	Fail("no Threads line in /proc/self/status")
	return 0
}

// encodeVersionSet encodes cls_version_set_op{obj_version{ver, tag}}.
func encodeVersionSet(ver uint64, tag string) []byte {
	e := denc.NewEncoder()
	op := e.BeginStruct(1, 1)
	objv := e.BeginStruct(1, 1)
	e.U64(ver)
	e.String(tag)
	e.EndStruct(objv)
	e.EndStruct(op)
	return e.Bytes()
}

// decodeVersionRead decodes cls_version_read_ret{obj_version{ver, tag}}.
func decodeVersionRead(b []byte) (uint64, string) {
	d := denc.NewDecoder(b)
	ret := d.BeginStruct(1)
	objv := d.BeginStruct(1)
	ver := d.U64()
	tag := d.String()
	d.EndStruct(objv)
	d.EndStruct(ret)
	Expect(d.Err()).NotTo(HaveOccurred())
	return ver, tag
}

// encodeGuardResharding encodes cls_rgw_guard_bucket_resharding_op{ret_err}.
func encodeGuardResharding(retErr int32) []byte {
	e := denc.NewEncoder()
	op := e.BeginStruct(1, 1)
	e.I32(retErr)
	e.EndStruct(op)
	return e.Bytes()
}

// removeObject deletes a scratch object a spec wrote.
func removeObject(p radosclient.Pool, oid string) {
	w := radosclient.NewWriteOp()
	w.Remove()
	_, err := p.Write(context.Background(), oid, w, radosclient.OpFlagNone)
	Expect(err).NotTo(HaveOccurred())
}

// isClosed reports whether err is the seam error a closed Pool or Cluster returns.
func isClosed(err error) bool {
	var seamErr *radosclient.Error
	return errors.As(err, &seamErr) && seamErr.Errno == int32(syscall.ENOTCONN)
}

// cancelAfterCheck passes the first Err check, so the operation starts, and
// is canceled from then on.
type cancelAfterCheck struct {
	context.Context
	checked atomic.Bool
	done    chan struct{}
}

func newCancelAfterCheck(parent context.Context) *cancelAfterCheck {
	c := &cancelAfterCheck{Context: parent, done: make(chan struct{})}
	close(c.done)
	return c
}

func (c *cancelAfterCheck) Done() <-chan struct{} { return c.done }

func (c *cancelAfterCheck) Err() error {
	if c.checked.Swap(true) {
		return context.Canceled
	}
	return nil
}

var _ = Describe("goceph against a cluster", Label("integration"), func() {
	for _, mode := range []goceph.Mode{goceph.ModeSync, goceph.ModeCallback, goceph.ModePipe} {
		Context(string(mode), func() {
			var (
				conf    string
				cluster radosclient.Cluster
				pool    radosclient.Pool
				oid     string
			)

			BeforeEach(func(ctx SpecContext) {
				conf = cephConf()
				if conf == "" {
					Skip("RGW_GO_TEST_CEPH_CONF is not set")
				}
				var err error
				cluster, err = goceph.Connect(ctx, goceph.Config{ConfigFile: conf, Mode: mode})
				Expect(err).NotTo(HaveOccurred())
				DeferCleanup(func() { Expect(cluster.Close()).To(Succeed()) })
				pool, err = cluster.Pool(ctx, testPool, "")
				Expect(err).NotTo(HaveOccurred())
				DeferCleanup(func() { Expect(pool.Close()).To(Succeed()) })
				oid = fmt.Sprintf("%s-%d-%d", mode, CurrentSpecReport().LeafNodeLocation.LineNumber, time.Now().UnixNano())
			})

			It("writes then reads a zero-length object", func(ctx SpecContext) {
				w := radosclient.NewWriteOp()
				w.Create(true)
				_, err := pool.Write(ctx, oid, w, radosclient.OpFlagNone)
				Expect(err).NotTo(HaveOccurred())

				r := radosclient.NewReadOp()
				st := r.Stat()
				Expect(pool.Read(ctx, oid, r, radosclient.OpFlagNone)).Error().To(Succeed())
				Expect(st.Err).NotTo(HaveOccurred())
				Expect(st.Size).To(BeZero())
				Expect(st.ModTime).NotTo(BeZero())

				again := radosclient.NewWriteOp()
				again.Create(true)
				_, err = pool.Write(ctx, oid, again, radosclient.OpFlagNone)
				Expect(err).To(MatchError(radosclient.ErrExists))
			})

			It("sets and reads an empty xattr", func(ctx SpecContext) {
				w := radosclient.NewWriteOp()
				w.Create(false)
				w.SetXattr("user.rgw.etag", nil)
				_, err := pool.Write(ctx, oid, w, radosclient.OpFlagNone)
				Expect(err).NotTo(HaveOccurred())

				r := radosclient.NewReadOp()
				xs := r.GetXattrs()
				Expect(pool.Read(ctx, oid, r, radosclient.OpFlagNone)).Error().To(Succeed())
				Expect(xs.Err).NotTo(HaveOccurred())
				Expect(xs.Xattrs).To(HaveKeyWithValue("user.rgw.etag", BeEmpty()))
			})

			It("composes stat, xattrs and read in one op", func(ctx SpecContext) {
				w := radosclient.NewWriteOp()
				w.WriteFull([]byte("hello"))
				w.SetXattr("user.rgw.etag", []byte("e1"))
				_, err := pool.Write(ctx, oid, w, radosclient.OpFlagNone)
				Expect(err).NotTo(HaveOccurred())

				r := radosclient.NewReadOp()
				// A true comparison makes the OSD return 1, which is success.
				r.CmpXattr("user.rgw.etag", radosclient.CmpEQ, []byte("e1"))
				st := r.Stat()
				xs := r.GetXattrs()
				data := r.Read(0, 16)
				Expect(pool.Read(ctx, oid, r, radosclient.OpFlagNone)).Error().To(Succeed())
				Expect(st.Err).NotTo(HaveOccurred())
				Expect(st.Size).To(BeEquivalentTo(5))
				Expect(xs.Xattrs).To(HaveKeyWithValue("user.rgw.etag", []byte("e1")))
				Expect(data.Err).NotTo(HaveOccurred())
				Expect(data.N).To(Equal(5))
				Expect(string(data.Data)).To(Equal("hello"))
			})

			It("reads into a caller's buffer", func(ctx SpecContext) {
				w := radosclient.NewWriteOp()
				w.WriteFull([]byte("hello, buffer"))
				_, err := pool.Write(ctx, oid, w, radosclient.OpFlagNone)
				Expect(err).NotTo(HaveOccurred())

				buf := make([]byte, 64)
				r := radosclient.NewReadOp()
				data := r.ReadInto(7, buf)
				Expect(pool.Read(ctx, oid, r, radosclient.OpFlagNone)).Error().To(Succeed())
				Expect(data.Err).NotTo(HaveOccurred())
				Expect(data.N).To(Equal(6))
				Expect(string(data.Data)).To(Equal("buffer"))
				Expect(string(buf[:data.N])).To(Equal("buffer"), "the data landed in the caller's buffer")
			})

			It("reports a missing object as ErrNotFound on every result", func(ctx SpecContext) {
				r := radosclient.NewReadOp()
				st := r.Stat()
				data := r.Read(0, 4)
				Expect(pool.Read(ctx, oid, r, radosclient.OpFlagNone)).Error().To(MatchError(radosclient.ErrNotFound))
				Expect(st.Err).To(MatchError(radosclient.ErrNotFound))
				Expect(data.Err).To(MatchError(radosclient.ErrNotFound))
			})

			It("fails a guarded write with ErrCanceled on a tag mismatch", func(ctx SpecContext) {
				w := radosclient.NewWriteOp()
				w.WriteFull([]byte("original"))
				w.SetXattr("user.rgw.idtag", []byte("a"))
				_, err := pool.Write(ctx, oid, w, radosclient.OpFlagNone)
				Expect(err).NotTo(HaveOccurred())

				guarded := radosclient.NewWriteOp()
				guarded.CmpXattr("user.rgw.idtag", radosclient.CmpEQ, []byte("b"))
				guarded.WriteFull([]byte("clobbered"))
				_, err = pool.Write(ctx, oid, guarded, radosclient.OpFlagNone)
				Expect(err).To(MatchError(radosclient.ErrCanceled))

				r := radosclient.NewReadOp()
				data := r.Read(0, 64)
				Expect(pool.Read(ctx, oid, r, radosclient.OpFlagNone)).Error().To(Succeed())
				Expect(string(data.Data)).To(Equal("original"))
			})

			It("overwrites an object with a remove that may fail, then a create, in one op", func(ctx SpecContext) {
				overwrite := func(value string) error {
					w := radosclient.NewWriteOp()
					w.Remove()
					w.SetStepFlags(radosclient.StepFlagFailOK)
					w.Create(false)
					w.SetXattr("user.rgw."+value, []byte(value))
					_, err := pool.Write(ctx, oid, w, radosclient.OpFlagNone)
					return err
				}
				Expect(overwrite("first")).To(Succeed(), "onto a missing object")
				Expect(overwrite("second")).To(Succeed(), "onto an existing object")

				r := radosclient.NewReadOp()
				xs := r.GetXattrs()
				Expect(pool.Read(ctx, oid, r, radosclient.OpFlagNone)).Error().To(Succeed())
				Expect(xs.Xattrs).To(Equal(map[string][]byte{"user.rgw.second": []byte("second")}),
					"the remove cleared the first write's xattr")

				// Without the flag, the remove of a missing object fails the op.
				w := radosclient.NewWriteOp()
				w.Remove()
				w.Create(false)
				_, err := pool.Write(ctx, oid+"-missing", w, radosclient.OpFlagNone)
				Expect(err).To(MatchError(radosclient.ErrNotFound))
			})

			It("returns the version after a write", func(ctx SpecContext) {
				w := radosclient.NewWriteOp()
				w.WriteFull([]byte("v1"))
				v1, err := pool.Write(ctx, oid, w, radosclient.OpFlagNone)
				Expect(err).NotTo(HaveOccurred())
				Expect(v1).To(BeNumerically(">", 0))

				w2 := radosclient.NewWriteOp()
				w2.WriteFull([]byte("v2"))
				v2, err := pool.Write(ctx, oid, w2, radosclient.OpFlagNone)
				Expect(err).NotTo(HaveOccurred())
				Expect(v2).To(BeNumerically(">", v1))

				r := radosclient.NewReadOp()
				r.AssertVersion(v2)
				Expect(pool.Read(ctx, oid, r, radosclient.OpFlagNone)).Error().To(Succeed())
			})

			It("returns the object's version from a read, and 0 with an error", func(ctx SpecContext) {
				w := radosclient.NewWriteOp()
				w.WriteFull([]byte("v1"))
				written, err := pool.Write(ctx, oid, w, radosclient.OpFlagNone)
				Expect(err).NotTo(HaveOccurred())

				r := radosclient.NewReadOp()
				r.Stat()
				read, err := pool.Read(ctx, oid, r, radosclient.OpFlagNone)
				Expect(err).NotTo(HaveOccurred())
				Expect(read).To(Equal(written), "a stat's version against the write's")

				missing := radosclient.NewReadOp()
				missing.Stat()
				read, err = pool.Read(ctx, oid+"-missing", missing, radosclient.OpFlagNone)
				Expect(err).To(MatchError(radosclient.ErrNotFound))
				Expect(read).To(BeZero())
			})

			It("reports the pool's id as the OSD map has it", func(ctx SpecContext) {
				out, _, err := cluster.MonCommand(ctx, []byte(`{"prefix":"osd dump","format":"json"}`))
				Expect(err).NotTo(HaveOccurred())
				var dump struct {
					Pools []struct {
						ID   int64  `json:"pool"`
						Name string `json:"pool_name"`
					} `json:"pools"`
				}
				Expect(json.Unmarshal(out, &dump)).To(Succeed())
				Expect(dump.Pools).To(ContainElement(HaveField("Name", testPool)))
				for _, p := range dump.Pools {
					if p.Name == testPool {
						Expect(pool.ID()).To(Equal(p.ID), "pool %s", testPool)
						Expect(pool.WithLocator("loc").ID()).To(Equal(p.ID), "pool %s with a locator", testPool)
					}
				}
			})

			It("stamps the modification time a write op sets, asynchronously in the async modes", func(ctx SpecContext) {
				mtime := time.Date(2024, 5, 6, 7, 8, 9, 123456789, time.UTC)
				w := radosclient.NewWriteOp()
				w.WriteFull([]byte("m"))
				w.SetMtime(mtime)
				_, err := pool.Write(ctx, oid, w, radosclient.OpFlagNone)
				Expect(err).NotTo(HaveOccurred())

				r := radosclient.NewReadOp()
				st := r.Stat()
				Expect(pool.Read(ctx, oid, r, radosclient.OpFlagNone)).Error().To(Succeed())
				Expect(st.ModTime.Equal(mtime)).To(BeTrue(), "mtime %v", st.ModTime)
			})

			It("documents that a read in the same op sees the object as it was before the op", func(ctx SpecContext) {
				w := radosclient.NewWriteOp()
				w.Create(false)
				w.Exec("version", "set", encodeVersionSet(7, "tag-7"))
				_, err := pool.Write(ctx, oid, w, radosclient.OpFlagNone)
				Expect(err).NotTo(HaveOccurred())

				r := radosclient.NewReadOp()
				set := r.Exec("version", "set", encodeVersionSet(8, "tag-8"))
				read := r.Exec("version", "read", nil)
				Expect(pool.Read(ctx, oid, r, radosclient.OpFlagReturnVec)).Error().To(Succeed())
				_, err = set.Bytes()
				Expect(err).NotTo(HaveOccurred())
				out, err := read.Bytes()
				Expect(err).NotTo(HaveOccurred())
				// A replicated pool's OSD reads xattrs from the object store
				// (PrimaryLogPG::getattr_maybe_cache), so the read in the same
				// op sees the version from before the op, not the one it set.
				ver, tag := decodeVersionRead(out)
				Expect(ver).To(BeEquivalentTo(7))
				Expect(tag).To(Equal("tag-7"))

				// The read op's set persisted.
				again := radosclient.NewReadOp()
				reread := again.Exec("version", "read", nil)
				Expect(pool.Read(ctx, oid, again, radosclient.OpFlagNone)).Error().To(Succeed())
				out, err = reread.Bytes()
				Expect(err).NotTo(HaveOccurred())
				ver, tag = decodeVersionRead(out)
				Expect(ver).To(BeEquivalentTo(8))
				Expect(tag).To(Equal("tag-8"))
			})

			It("returns a modifying class method's output through a ReturnVec read op", func(ctx SpecContext) {
				w := radosclient.NewWriteOp()
				w.Create(false)
				_, err := pool.Write(ctx, oid, w, radosclient.OpFlagNone)
				Expect(err).NotTo(HaveOccurred())

				// cls_hello write_return_data sets xattr foo=bar, returns
				// "you might see this" and a positive rval of 42.
				r := radosclient.NewReadOp()
				res := r.Exec("hello", "write_return_data", nil)
				Expect(pool.Read(ctx, oid, r, radosclient.OpFlagReturnVec)).Error().To(Succeed())
				out, err := res.Bytes()
				Expect(err).NotTo(HaveOccurred())
				Expect(string(out)).To(Equal("you might see this"))

				after := radosclient.NewReadOp()
				xs := after.GetXattrs()
				Expect(pool.Read(ctx, oid, after, radosclient.OpFlagNone)).Error().To(Succeed())
				Expect(xs.Err).NotTo(HaveOccurred())
				Expect(xs.Xattrs).To(HaveKeyWithValue("foo", []byte("bar")))
			})

			It("reports a write op whose class method returns a positive value as a success", func(ctx SpecContext) {
				w := radosclient.NewWriteOp()
				w.Create(false)
				_, err := pool.Write(ctx, oid, w, radosclient.OpFlagNone)
				Expect(err).NotTo(HaveOccurred())
				DeferCleanup(removeObject, pool, oid)

				// cls_hello write_return_data sets xattr foo=bar and returns
				// 42, which librados passes through only with ReturnVec set.
				wr := radosclient.NewWriteOp()
				res := wr.Exec("hello", "write_return_data", nil)
				_, err = pool.Write(ctx, oid, wr, radosclient.OpFlagReturnVec)
				Expect(err).NotTo(HaveOccurred())
				_, err = res.Bytes()
				Expect(err).NotTo(HaveOccurred())

				after := radosclient.NewReadOp()
				xs := after.GetXattrs()
				Expect(pool.Read(ctx, oid, after, radosclient.OpFlagNone)).Error().To(Succeed())
				Expect(xs.Err).NotTo(HaveOccurred())
				Expect(xs.Xattrs).To(HaveKeyWithValue("foo", []byte("bar")))
			})

			It("passes a write guarded by guard_bucket_resharding with a positive ret_err on a shard not resharding", func(ctx SpecContext) {
				ns, err := cluster.Pool(ctx, testPool, "guard-ns-"+oid)
				Expect(err).NotTo(HaveOccurred())
				DeferCleanup(func() { Expect(ns.Close()).To(Succeed()) })

				initOp := radosclient.NewWriteOp()
				initOp.Create(true)
				initRes := initOp.Exec("rgw", "bucket_init_index", nil)
				_, err = ns.Write(ctx, oid, initOp, radosclient.OpFlagNone)
				Expect(err).NotTo(HaveOccurred())
				_, err = initRes.Bytes()
				Expect(err).NotTo(HaveOccurred())
				DeferCleanup(removeObject, ns, oid)

				guarded := radosclient.NewWriteOp()
				guardRes := guarded.Exec("rgw", "guard_bucket_resharding", encodeGuardResharding(2300))
				guarded.SetXattr("user.rgw.guarded", []byte("yes"))
				_, err = ns.Write(ctx, oid, guarded, radosclient.OpFlagNone)
				Expect(err).NotTo(HaveOccurred())
				_, err = guardRes.Bytes()
				Expect(err).NotTo(HaveOccurred())

				after := radosclient.NewReadOp()
				xs := after.GetXattrs()
				Expect(ns.Read(ctx, oid, after, radosclient.OpFlagNone)).Error().To(Succeed())
				Expect(xs.Xattrs).To(HaveKeyWithValue("user.rgw.guarded", []byte("yes")))
			})

			It("reports a class method's errno through its result", func(ctx SpecContext) {
				r := radosclient.NewReadOp()
				res := r.Exec("version", "no_such_method", nil)
				Expect(pool.Read(ctx, oid, r, radosclient.OpFlagNone)).Error().To(HaveOccurred())
				_, err := res.Bytes()
				Expect(err).To(HaveOccurred())
			})

			It("keeps OS thread count flat with 512 reads in flight", func(ctx SpecContext) {
				w := radosclient.NewWriteOp()
				w.WriteFull(make([]byte, 4096))
				_, err := pool.Write(ctx, oid, w, radosclient.OpFlagNone)
				Expect(err).NotTo(HaveOccurred())

				burst := func() int {
					var wg sync.WaitGroup
					var failures atomic.Int32
					stop := make(chan struct{})
					peak := make(chan int, 1)
					go func() {
						hi := osThreads()
						for {
							select {
							case <-stop:
								peak <- hi
								return
							case <-time.After(time.Millisecond):
								hi = max(hi, osThreads())
							}
						}
					}()
					for range 512 {
						wg.Go(func() {
							r := radosclient.NewReadOp()
							data := r.Read(0, 4096)
							if _, err := pool.Read(ctx, oid, r, radosclient.OpFlagNone); err != nil || data.N != 4096 {
								failures.Add(1)
							}
						})
					}
					wg.Wait()
					close(stop)
					Expect(failures.Load()).To(BeZero())
					return <-peak
				}

				// The first burst starts the runtime's and librados's lazily
				// created threads, which belong in the baseline.
				burst()
				baseline := osThreads()
				during := burst()
				after := osThreads()
				AddReportEntry("threads", fmt.Sprintf("baseline %d, during %d, after %d", baseline, during, after))
				if mode != goceph.ModeSync {
					Expect(during).To(BeNumerically("<=", baseline+16))
					Expect(after).To(BeNumerically("<=", baseline+16))
				}
			})

			It("returns ctx.Err when the context ends before the op completes", func(ctx SpecContext) {
				if mode == goceph.ModeSync {
					Skip("a synchronous op is not abandoned once started")
				}
				w := radosclient.NewWriteOp()
				w.WriteFull(make([]byte, 4096))
				_, err := pool.Write(ctx, oid, w, radosclient.OpFlagNone)
				Expect(err).NotTo(HaveOccurred())

				canceled := 0
				for range 64 {
					r := radosclient.NewReadOp()
					r.Read(0, 4096)
					_, err := pool.Read(newCancelAfterCheck(ctx), oid, r, radosclient.OpFlagNone)
					if err != nil {
						Expect(err).To(MatchError(context.Canceled))
						canceled++
					}
				}
				Expect(canceled).To(BeNumerically(">", 0))
				// Close waits for the reaped completions; the cleanup would hang otherwise.
			})

			It("reports the required OSD release", func(ctx SpecContext) {
				name, err := cluster.RequiredOSDRelease(ctx)
				Expect(err).NotTo(HaveOccurred())
				Expect(name).To(Equal(expectedRelease(conf)))

				rel, err := goceph.Release(ctx, cluster)
				Expect(err).NotTo(HaveOccurred())
				want, ok := denc.ParseRelease(name)
				Expect(ok).To(BeTrue())
				Expect(rel).To(Equal(want))
			})

			It("sets the object locator on a WithLocator pool", func(ctx SpecContext) {
				loc := pool.WithLocator("locator-" + oid)
				w := radosclient.NewWriteOp()
				w.WriteFull([]byte("located"))
				_, err := loc.Write(ctx, oid, w, radosclient.OpFlagNone)
				Expect(err).NotTo(HaveOccurred())

				r := radosclient.NewReadOp()
				data := r.Read(0, 16)
				Expect(loc.Read(ctx, oid, r, radosclient.OpFlagNone)).Error().To(Succeed())
				Expect(string(data.Data)).To(Equal("located"))

				plain := radosclient.NewReadOp()
				plain.Stat()
				Expect(pool.Read(ctx, oid, plain, radosclient.OpFlagNone)).Error().To(MatchError(radosclient.ErrNotFound))
			})

			It("takes and releases an exclusive lock", func(ctx SpecContext) {
				Expect(pool.LockExclusive(ctx, oid, "lk", "c1", "d", time.Minute, 0)).To(Succeed())
				lockers, err := pool.ListLockers(ctx, oid, "lk")
				Expect(err).NotTo(HaveOccurred())
				Expect(lockers).To(HaveLen(1))
				Expect(lockers[0].Cookie).To(Equal("c1"))
				Expect(pool.LockExclusive(ctx, oid, "lk", "c1", "d", time.Minute, 0)).
					To(MatchError(radosclient.ErrExists))
				Expect(pool.LockExclusive(ctx, oid, "lk", "c1", "d", time.Minute, radosclient.LockRenew)).To(Succeed())
				Expect(pool.Unlock(ctx, oid, "lk", "c1")).To(Succeed())
				Expect(pool.Unlock(ctx, oid, "lk", "c1")).To(MatchError(radosclient.ErrNotFound))
			})

			It("delivers a notify to a watcher and returns its ack", func(ctx SpecContext) {
				w := radosclient.NewWriteOp()
				w.Create(false)
				_, err := pool.Write(ctx, oid, w, radosclient.OpFlagNone)
				Expect(err).NotTo(HaveOccurred())

				got := make(chan []byte, 1)
				watch, err := pool.Watch(ctx, oid, func(_, _ uint64, payload []byte) { got <- payload })
				Expect(err).NotTo(HaveOccurred())
				DeferCleanup(func() { Expect(watch.Close()).To(Succeed()) })

				acks, err := pool.Notify(ctx, oid, []byte("ping"), 10*time.Second)
				Expect(err).NotTo(HaveOccurred())
				Expect(acks).To(HaveLen(1))
				Eventually(got).Should(Receive(Equal([]byte("ping"))))
			})

			It("reads a zero-length range as the whole object, which fits only when empty", func(ctx SpecContext) {
				empty := radosclient.NewWriteOp()
				empty.Create(false)
				_, err := pool.Write(ctx, oid, empty, radosclient.OpFlagNone)
				Expect(err).NotTo(HaveOccurred())
				r := radosclient.NewReadOp()
				data := r.Read(0, 0)
				Expect(pool.Read(ctx, oid, r, radosclient.OpFlagNone)).Error().To(Succeed())
				Expect(data.Err).NotTo(HaveOccurred())
				Expect(data.N).To(BeZero())

				full := radosclient.NewWriteOp()
				full.WriteFull([]byte("hello"))
				_, err = pool.Write(ctx, oid, full, radosclient.OpFlagNone)
				Expect(err).NotTo(HaveOccurred())
				r = radosclient.NewReadOp()
				data = r.Read(0, 0)
				Expect(pool.Read(ctx, oid, r, radosclient.OpFlagNone)).Error().To(Succeed())
				Expect(data.Err).To(MatchError(radosclient.ErrRange))
				Expect(data.N).To(BeZero())
			})

			It("keeps locators apart when operations share pooled I/O contexts", func(ctx SpecContext) {
				locs := []radosclient.Pool{pool.WithLocator("loc-a-" + oid), pool.WithLocator("loc-b-" + oid), pool}
				var wg sync.WaitGroup
				var failures atomic.Int32
				for i := range 96 {
					wg.Go(func() {
						p := locs[i%len(locs)]
						name := fmt.Sprintf("%s-%d", oid, i)
						body := []byte(name)
						w := radosclient.NewWriteOp()
						w.WriteFull(body)
						if _, err := p.Write(ctx, name, w, radosclient.OpFlagNone); err != nil {
							failures.Add(1)
							return
						}
						for j, other := range locs {
							r := radosclient.NewReadOp()
							data := r.Read(0, 64)
							_, err := other.Read(ctx, name, r, radosclient.OpFlagNone)
							if j == i%len(locs) {
								if err != nil || string(data.Data) != name {
									failures.Add(1)
								}
							} else if !errors.Is(err, radosclient.ErrNotFound) {
								failures.Add(1)
							}
						}
					})
				}
				wg.Wait()
				Expect(failures.Load()).To(BeZero())
			})

			It("closes the cluster with an open pool and operations in flight", func(ctx SpecContext) {
				w := radosclient.NewWriteOp()
				w.WriteFull(make([]byte, 4096))
				_, err := pool.Write(ctx, oid, w, radosclient.OpFlagNone)
				Expect(err).NotTo(HaveOccurred())

				if mode != goceph.ModeSync {
					// Abandoned reads leave completions in flight for Close.
					for range 16 {
						r := radosclient.NewReadOp()
						r.Read(0, 4096)
						if _, readErr := pool.Read(newCancelAfterCheck(ctx), oid, r, radosclient.OpFlagNone); readErr != nil {
							Expect(readErr).To(MatchError(context.Canceled))
						}
					}
				}
				var wg sync.WaitGroup
				var closedErrs atomic.Int32
				stop := make(chan struct{})
				for range 32 {
					wg.Go(func() {
						for {
							select {
							case <-stop:
								return
							default:
							}
							r := radosclient.NewReadOp()
							r.Read(0, 4096)
							if _, readErr := pool.Read(ctx, oid, r, radosclient.OpFlagNone); readErr != nil {
								var seamErr *radosclient.Error
								if errors.As(readErr, &seamErr) && seamErr.Errno == int32(syscall.ENOTCONN) {
									closedErrs.Add(1)
								}
								return
							}
						}
					})
				}
				time.Sleep(20 * time.Millisecond)
				Expect(cluster.Close()).To(Succeed())
				close(stop)
				wg.Wait()
				Expect(closedErrs.Load()).To(BeNumerically(">", 0))

				r := radosclient.NewReadOp()
				r.Stat()
				_, err = pool.Read(ctx, oid, r, radosclient.OpFlagNone)
				var seamErr *radosclient.Error
				Expect(errors.As(err, &seamErr)).To(BeTrue())
				Expect(seamErr.Errno).To(BeEquivalentTo(syscall.ENOTCONN))
				_, err = cluster.Pool(ctx, testPool, "")
				Expect(err).To(HaveOccurred())
				Expect(pool.Close()).To(Succeed())
			})

			It("races Pool.Close and Cluster.Close with operations and pool opens in flight", func(ctx SpecContext) {
				for range 8 {
					c2, err := goceph.Connect(ctx, goceph.Config{ConfigFile: conf, Mode: mode})
					Expect(err).NotTo(HaveOccurred())
					p2, err := c2.Pool(ctx, testPool, "")
					Expect(err).NotTo(HaveOccurred())
					w := radosclient.NewWriteOp()
					w.WriteFull(make([]byte, 4096))
					_, err = p2.Write(ctx, oid, w, radosclient.OpFlagNone)
					Expect(err).NotTo(HaveOccurred())

					if mode != goceph.ModeSync {
						for range 8 {
							r := radosclient.NewReadOp()
							r.Read(0, 4096)
							if _, readErr := p2.Read(newCancelAfterCheck(ctx), oid, r, radosclient.OpFlagNone); readErr != nil {
								Expect(readErr).To(MatchError(context.Canceled))
							}
						}
					}

					var unexpected atomic.Int32
					var wg sync.WaitGroup
					for range 16 {
						wg.Go(func() {
							for {
								r := radosclient.NewReadOp()
								r.Read(0, 4096)
								if _, readErr := p2.Read(ctx, oid, r, radosclient.OpFlagNone); readErr != nil {
									if !isClosed(readErr) {
										unexpected.Add(1)
									}
									return
								}
							}
						})
					}
					wg.Go(func() {
						for {
							p3, openErr := c2.Pool(ctx, testPool, "")
							if openErr != nil {
								if !isClosed(openErr) {
									unexpected.Add(1)
								}
								return
							}
							if closeErr := p3.Close(); closeErr != nil {
								unexpected.Add(1)
							}
						}
					})

					time.Sleep(5 * time.Millisecond)
					closes := make(chan error, 2)
					go func() { closes <- p2.Close() }()
					go func() { closes <- c2.Close() }()
					Eventually(closes).WithTimeout(30 * time.Second).Should(Receive(Succeed()))
					Eventually(closes).WithTimeout(30 * time.Second).Should(Receive(Succeed()))
					wg.Wait()
					Expect(unexpected.Load()).To(BeZero())
					Expect(p2.Close()).To(Succeed())
					Expect(c2.Close()).To(Succeed())
				}
			})

			It("refuses cluster calls after Close", func(ctx SpecContext) {
				c2, err := goceph.Connect(ctx, goceph.Config{ConfigFile: conf, Mode: mode})
				Expect(err).NotTo(HaveOccurred())
				Expect(c2.Close()).To(Succeed())

				_, _, err = c2.MonCommand(ctx, []byte(`{"prefix":"osd dump","format":"json"}`))
				Expect(isClosed(err)).To(BeTrue(), "%v", err)
				_, err = c2.ConfigGet("fsid")
				Expect(isClosed(err)).To(BeTrue(), "%v", err)
				_, err = c2.RequiredOSDRelease(ctx)
				Expect(isClosed(err)).To(BeTrue(), "%v", err)
				_, err = c2.Pool(ctx, testPool, "")
				Expect(isClosed(err)).To(BeTrue(), "%v", err)
			})

			It("leaves the parent open when a WithLocator pool closes, even for the empty locator", func(ctx SpecContext) {
				w := radosclient.NewWriteOp()
				w.WriteFull([]byte("still here"))
				_, err := pool.Write(ctx, oid, w, radosclient.OpFlagNone)
				Expect(err).NotTo(HaveOccurred())

				Expect(pool.WithLocator("").Close()).To(Succeed())
				Expect(pool.WithLocator("some-key").Close()).To(Succeed())

				r := radosclient.NewReadOp()
				data := r.Read(0, 16)
				Expect(pool.WithLocator("").Read(ctx, oid, r, radosclient.OpFlagNone)).Error().To(Succeed())
				Expect(string(data.Data)).To(Equal("still here"))
			})

			It("stops a watch whose unwatch fails", func(ctx SpecContext) {
				w := radosclient.NewWriteOp()
				w.Create(false)
				_, err := pool.Write(ctx, oid, w, radosclient.OpFlagNone)
				Expect(err).NotTo(HaveOccurred())
				watch, err := pool.Watch(ctx, oid, func(_, _ uint64, _ []byte) {})
				Expect(err).NotTo(HaveOccurred())

				rm := radosclient.NewWriteOp()
				rm.Remove()
				_, err = pool.Write(ctx, oid, rm, radosclient.OpFlagNone)
				Expect(err).NotTo(HaveOccurred())

				closed := make(chan error, 1)
				go func() { closed <- watch.Close() }()
				Eventually(closed).WithTimeout(30 * time.Second).Should(Receive(MatchError(radosclient.ErrNotFound)))
				Eventually(goceph.WatchStopped(watch)).Should(BeClosed())
				Expect(watch.Close()).To(MatchError(radosclient.ErrNotFound))

				poolClosed := make(chan error, 1)
				go func() { poolClosed <- pool.Close() }()
				Eventually(poolClosed).WithTimeout(30 * time.Second).Should(Receive(Succeed()))
			})

			It("reports a watch the OSD disconnects when its object is removed", func(ctx SpecContext) {
				w := radosclient.NewWriteOp()
				w.Create(false)
				_, err := pool.Write(ctx, oid, w, radosclient.OpFlagNone)
				Expect(err).NotTo(HaveOccurred())
				watch, err := pool.Watch(ctx, oid, func(_, _ uint64, _ []byte) {})
				Expect(err).NotTo(HaveOccurred())
				Consistently(watch.Err()).WithTimeout(time.Second).ShouldNot(Receive(), "a healthy watch")

				rm := radosclient.NewWriteOp()
				rm.Remove()
				_, err = pool.Write(ctx, oid, rm, radosclient.OpFlagNone)
				Expect(err).NotTo(HaveOccurred())

				var broken error
				Eventually(watch.Err()).WithTimeout(30 * time.Second).Should(Receive(&broken))
				var seamErr *radosclient.Error
				Expect(errors.As(broken, &seamErr)).To(BeTrue(), "%v", broken)
				Expect(seamErr.Errno).To(BeEquivalentTo(syscall.ENOTCONN), "%v", broken)

				Expect(watch.Close()).To(MatchError(radosclient.ErrNotFound))
				Expect(watch.Err()).To(BeClosed())
			})

			It("closes an outstanding watch when the pool closes", func(ctx SpecContext) {
				w := radosclient.NewWriteOp()
				w.Create(false)
				_, err := pool.Write(ctx, oid, w, radosclient.OpFlagNone)
				Expect(err).NotTo(HaveOccurred())
				watch, err := pool.Watch(ctx, oid, func(_, _ uint64, _ []byte) {})
				Expect(err).NotTo(HaveOccurred())

				poolClosed := make(chan error, 1)
				go func() { poolClosed <- pool.Close() }()
				Eventually(poolClosed).WithTimeout(30 * time.Second).Should(Receive(Succeed()))
				Expect(goceph.WatchStopped(watch)).To(BeClosed())
				Expect(watch.Close()).To(Succeed())
			})

			It("lists an object with its locator, which then reads it back", func(ctx SpecContext) {
				ns, err := cluster.Pool(ctx, testPool, "loc-ns-"+oid)
				Expect(err).NotTo(HaveOccurred())
				DeferCleanup(func() { Expect(ns.Close()).To(Succeed()) })

				w := radosclient.NewWriteOp()
				w.WriteFull([]byte("located"))
				_, err = ns.WithLocator("key-"+oid).Write(ctx, "obj", w, radosclient.OpFlagNone)
				Expect(err).NotTo(HaveOccurred())
				plain := radosclient.NewWriteOp()
				plain.Create(false)
				_, err = ns.Write(ctx, "plain", plain, radosclient.OpFlagNone)
				Expect(err).NotTo(HaveOccurred())

				listed := map[string]string{}
				Expect(ns.ListObjects(ctx, func(oid, locator string) error {
					listed[oid] = locator
					return nil
				})).To(Succeed())
				Expect(listed).To(Equal(map[string]string{"obj": "key-" + oid, "plain": ""}))

				r := radosclient.NewReadOp()
				data := r.Read(0, 16)
				Expect(ns.WithLocator(listed["obj"]).Read(ctx, "obj", r, radosclient.OpFlagNone)).Error().To(Succeed())
				Expect(string(data.Data)).To(Equal("located"))
			})

			It("lists the objects in the namespace", func(ctx SpecContext) {
				ns, err := cluster.Pool(ctx, testPool, "ns-"+oid)
				Expect(err).NotTo(HaveOccurred())
				DeferCleanup(func() { Expect(ns.Close()).To(Succeed()) })
				for _, name := range []string{"a", "b"} {
					w := radosclient.NewWriteOp()
					w.Create(false)
					_, err := ns.Write(ctx, name, w, radosclient.OpFlagNone)
					Expect(err).NotTo(HaveOccurred())
				}
				var names []string
				Expect(ns.ListObjects(ctx, func(oid, _ string) error {
					names = append(names, oid)
					return nil
				})).To(Succeed())
				Expect(names).To(ConsistOf("a", "b"))
			})
		})
	}
})
