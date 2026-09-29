package driver_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cephconf"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/driver"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/radosclient/radosclientfakes"
)

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

var _ = Describe("the driver skeleton", func() {
	var (
		cluster *radosclientfakes.FakeCluster
		conf    *cephconf.Options
	)
	BeforeEach(func() {
		cluster = &radosclientfakes.FakeCluster{}
		cluster.RequiredOSDReleaseReturns("squid", nil)
		conf = cephconf.NewOptions(cephconf.MapGetter{
			"rgw_zone":      "ceph-objectstore",
			"rgw_zonegroup": "ceph-objectstore",
			"rgw_realm":     "ceph-objectstore",
		})
	})

	Describe("Open", func() {
		It("detects the release and names the zone from configuration", func(ctx SpecContext) {
			s, err := driver.Open(ctx, cluster, conf, driver.Options{})
			Expect(err).NotTo(HaveOccurred())
			Expect(s.Release()).To(Equal(denc.Squid))
			Expect(s.Zone().Name).To(Equal("ceph-objectstore"))
			Expect(s.ZoneGroup().Name).To(Equal("ceph-objectstore"))
			Expect(s.Realm().Name).To(Equal("ceph-objectstore"))
		})

		It("keeps the zone, zonegroup and realm names apart and resolves nothing else", func(ctx SpecContext) {
			conf = cephconf.NewOptions(cephconf.MapGetter{"rgw_zone": "z", "rgw_zonegroup": "zg", "rgw_realm": "r"})
			s, err := driver.Open(ctx, cluster, conf, driver.Options{})
			Expect(err).NotTo(HaveOccurred())
			Expect(s.Zone()).To(Equal(meta.Zone{Name: "z"}))
			Expect(s.ZoneParams()).To(Equal(meta.ZoneParams{Name: "z"}), "a zone's parameters carry its name")
			Expect(s.ZoneGroup()).To(Equal(meta.ZoneGroup{Name: "zg"}))
			Expect(s.Realm()).To(Equal(meta.Realm{Name: "r"}))
			Expect(s.Period()).To(Equal(meta.Period{}))
		})

		It("honors a release override", func(ctx SpecContext) {
			t := denc.Tentacle
			s, err := driver.Open(ctx, cluster, conf, driver.Options{Release: &t})
			Expect(err).NotTo(HaveOccurred())
			Expect(s.Release()).To(Equal(denc.Tentacle))
			Expect(cluster.RequiredOSDReleaseCallCount()).To(BeZero())
		})

		It("refuses a cluster below the Squid floor", func(ctx SpecContext) {
			cluster.RequiredOSDReleaseReturns("reef", nil)
			_, err := driver.Open(ctx, cluster, conf, driver.Options{})
			Expect(err).To(MatchError(radosclient.ErrReleaseTooOld))
			Expect(err).To(MatchError(ContainSubstring(`"reef"`)))
		})

		It("encodes for the newest known release on a cluster newer than every known one", func(ctx SpecContext) {
			cluster.RequiredOSDReleaseReturns("vampire", nil)
			s, err := driver.Open(ctx, cluster, conf, driver.Options{})
			Expect(err).NotTo(HaveOccurred())
			Expect(s.Release()).To(Equal(denc.Tentacle))
		})

		It("fails when the required release cannot be read", func(ctx SpecContext) {
			boom := errors.New("osd dump failed")
			cluster.RequiredOSDReleaseReturns("", boom)
			_, err := driver.Open(ctx, cluster, conf, driver.Options{})
			Expect(err).To(MatchError(boom))
		})

		DescribeTable("fails when a zone name cannot be read",
			func(ctx SpecContext, option string) {
				m := cephconf.MapGetter{"rgw_zone": "z", "rgw_zonegroup": "zg", "rgw_realm": "r"}
				delete(m, option)
				_, err := driver.Open(ctx, cluster, cephconf.NewOptions(m), driver.Options{})
				Expect(err).To(MatchError(cephconf.ErrUnknownOption))
				Expect(err).To(MatchError(ContainSubstring(option)))
			},
			Entry("rgw_zone", "rgw_zone"),
			Entry("rgw_zonegroup", "rgw_zonegroup"),
			Entry("rgw_realm", "rgw_realm"),
		)
	})

	Context("once open", func() {
		var s *driver.Store
		BeforeEach(func(ctx SpecContext) {
			var err error
			s, err = driver.Open(ctx, cluster, conf, driver.Options{})
			Expect(err).NotTo(HaveOccurred())
		})

		DescribeTable("answers NotImplemented from every store method",
			func(ctx SpecContext, call func(context.Context, *driver.Store) error) {
				Expect(call(ctx, s)).To(MatchError(op.ErrNotImplemented))
			},
			Entry("Placement", func(_ context.Context, s *driver.Store) error {
				_, err := s.Placement(meta.PlacementRule{Name: "default-placement"})
				return err
			}),

			Entry("GetUser", func(ctx context.Context, s *driver.Store) error {
				_, err := s.GetUser(ctx, meta.UserID{ID: "alice"})
				return err
			}),
			Entry("GetUserByAccessKey", func(ctx context.Context, s *driver.Store) error {
				_, err := s.GetUserByAccessKey(ctx, "AK")
				return err
			}),
			Entry("GetUserByEmail", func(ctx context.Context, s *driver.Store) error {
				_, err := s.GetUserByEmail(ctx, "alice@example.com")
				return err
			}),
			Entry("PutUser", func(ctx context.Context, s *driver.Store) error {
				return s.PutUser(ctx, &op.UserRecord{Info: meta.UserInfo{UserID: meta.UserID{ID: "alice"}}}, op.PutUserOptions{Exclusive: true})
			}),
			Entry("RemoveUser", func(ctx context.Context, s *driver.Store) error {
				return s.RemoveUser(ctx, &op.UserRecord{Info: meta.UserInfo{UserID: meta.UserID{ID: "alice"}}})
			}),
			Entry("ListUserBuckets", func(ctx context.Context, s *driver.Store) error {
				_, _, _, err := s.ListUserBuckets(ctx, meta.UserOwner(meta.UserID{ID: "alice"}), "", 1000)
				return err
			}),

			Entry("GetBucket", func(ctx context.Context, s *driver.Store) error {
				_, err := s.GetBucket(ctx, "", "plain")
				return err
			}),
			Entry("GetBucketInstance", func(ctx context.Context, s *driver.Store) error {
				_, err := s.GetBucketInstance(ctx, meta.BucketID{Name: "plain", ID: "zone.1.1"})
				return err
			}),
			Entry("CreateBucket", func(ctx context.Context, s *driver.Store) error {
				_, err := s.CreateBucket(ctx, op.CreateBucketParams{Name: "plain", Owner: meta.UserOwner(meta.UserID{ID: "alice"}), Exclusive: true})
				return err
			}),
			Entry("DeleteBucket", func(ctx context.Context, s *driver.Store) error {
				return s.DeleteBucket(ctx, &op.BucketRecord{})
			}),
			Entry("PutBucketInfo", func(ctx context.Context, s *driver.Store) error {
				return s.PutBucketInfo(ctx, &op.BucketRecord{})
			}),
			Entry("PutBucketAttrs", func(ctx context.Context, s *driver.Store) error {
				return s.PutBucketAttrs(ctx, &op.BucketRecord{}, map[string][]byte{"user.rgw.x-amz-tagging": nil}, nil)
			}),
			Entry("ListObjects", func(ctx context.Context, s *driver.Store) error {
				_, err := s.ListObjects(ctx, &op.BucketRecord{}, op.ListObjectsParams{MaxKeys: 1000})
				return err
			}),

			Entry("StatObject", func(ctx context.Context, s *driver.Store) error {
				_, err := s.StatObject(ctx, &op.BucketRecord{}, meta.ObjKey{Name: "k"})
				return err
			}),
			Entry("ReadObject", func(ctx context.Context, s *driver.Store) error {
				return s.ReadObject(ctx, &op.ObjectState{Key: meta.ObjKey{Name: "k"}, Exists: true, Size: 1}, op.ByteRange{Length: 1}, io.Discard)
			}),
			Entry("PutObject", func(ctx context.Context, s *driver.Store) error {
				_, err := s.PutObject(ctx, &op.BucketRecord{}, meta.ObjKey{Name: "k"}, strings.NewReader("v"), op.PutParams{Size: 1})
				return err
			}),
			Entry("DeleteObject", func(ctx context.Context, s *driver.Store) error {
				return s.DeleteObject(ctx, &op.BucketRecord{}, meta.ObjKey{Name: "k"}, op.DeleteParams{})
			}),
			Entry("CopyObject", func(ctx context.Context, s *driver.Store) error {
				_, err := s.CopyObject(ctx, &op.ObjectState{Key: meta.ObjKey{Name: "k"}, Exists: true}, &op.BucketRecord{}, meta.ObjKey{Name: "k2"}, op.CopyParams{})
				return err
			}),
			Entry("SetObjectAttrs", func(ctx context.Context, s *driver.Store) error {
				return s.SetObjectAttrs(ctx, &op.ObjectState{Key: meta.ObjKey{Name: "k"}, Exists: true}, nil, []string{"user.rgw.x-amz-meta-a"})
			}),

			Entry("CreateUpload", func(ctx context.Context, s *driver.Store) error {
				_, err := s.CreateUpload(ctx, &op.BucketRecord{}, meta.ObjKey{Name: "k"}, op.UploadParams{})
				return err
			}),
			Entry("GetUpload", func(ctx context.Context, s *driver.Store) error {
				_, err := s.GetUpload(ctx, &op.BucketRecord{}, meta.ObjKey{Name: "k"}, "2~upload")
				return err
			}),
			Entry("PutPart", func(ctx context.Context, s *driver.Store) error {
				_, err := s.PutPart(ctx, &op.Upload{ID: "2~upload"}, 1, strings.NewReader("v"), op.PutParams{Size: 1})
				return err
			}),
			Entry("CopyPart", func(ctx context.Context, s *driver.Store) error {
				_, err := s.CopyPart(ctx, &op.Upload{ID: "2~upload"}, 1, &op.ObjectState{Key: meta.ObjKey{Name: "k"}, Exists: true, Size: 1}, op.ByteRange{Length: 1})
				return err
			}),
			Entry("ListParts", func(ctx context.Context, s *driver.Store) error {
				_, err := s.ListParts(ctx, &op.Upload{ID: "2~upload"}, 0, 1000)
				return err
			}),
			Entry("ListUploads", func(ctx context.Context, s *driver.Store) error {
				_, err := s.ListUploads(ctx, &op.BucketRecord{}, op.ListUploadsParams{MaxUploads: 1000})
				return err
			}),
			Entry("Complete", func(ctx context.Context, s *driver.Store) error {
				_, err := s.Complete(ctx, &op.Upload{ID: "2~upload"}, []op.CompletePart{{Number: 1, ETag: `"etag"`}})
				return err
			}),
			Entry("Abort", func(ctx context.Context, s *driver.Store) error {
				return s.Abort(ctx, &op.Upload{ID: "2~upload"})
			}),

			Entry("BucketStats", func(ctx context.Context, s *driver.Store) error {
				_, err := s.BucketStats(ctx, &op.BucketRecord{})
				return err
			}),
			Entry("UserStats", func(ctx context.Context, s *driver.Store) error {
				_, err := s.UserStats(ctx, meta.UserOwner(meta.UserID{ID: "alice"}))
				return err
			}),
			Entry("CheckQuota", func(ctx context.Context, s *driver.Store) error {
				return s.CheckQuota(ctx, &op.BucketRecord{}, meta.UserOwner(meta.UserID{ID: "alice"}), 1, 1)
			}),

			Entry("metadata Get", func(ctx context.Context, s *driver.Store) error {
				_, err := s.Get(ctx, "user", "alice")
				return err
			}),
			Entry("metadata Put", func(ctx context.Context, s *driver.Store) error {
				return s.Put(ctx, "user", "alice", op.MetadataEntry{Key: "alice"}, op.PutMetadataOptions{})
			}),
			Entry("metadata Remove", func(ctx context.Context, s *driver.Store) error {
				return s.Remove(ctx, "user", "alice")
			}),
			Entry("metadata List", func(ctx context.Context, s *driver.Store) error {
				_, _, _, err := s.List(ctx, "user", "", 1000)
				return err
			}),
		)

		It("drops a usage entry, having no usage log to write it to", func(ctx SpecContext) {
			Expect(func() {
				s.Log(ctx, op.UsageEntry{Bucket: "plain", Category: "get_obj", Ops: 1, SuccessfulOps: 1})
			}).NotTo(Panic())
		})

		It("gives an Env whose every store is itself and whose options are Open's", func() {
			env := s.Env()
			Expect([]any{env.Zone, env.Users, env.Buckets, env.Objects, env.Multipart, env.Stats, env.Usage, env.Metadata}).
				To(HaveEach(BeIdenticalTo(s)))
			Expect(env.Conf).To(BeIdenticalTo(conf))
			Expect(env.Authz).To(BeNil(), "authz is the caller's")
			Expect(env.Metrics).To(BeNil(), "metrics are the caller's")
			Expect(env.HostID).To(BeEmpty(), "the host id is the caller's")
		})

		Describe("Run", func() {
			// run starts s.Run on a context the spec cancels, and returns that
			// cancel and the channel Run's result arrives on.
			run := func(ctx context.Context) (context.CancelFunc, <-chan error) {
				runCtx, cancel := context.WithCancel(ctx)
				DeferCleanup(cancel)
				done := make(chan error, 1)
				go func() { done <- s.Run(runCtx) }()
				return cancel, done
			}

			It("runs workers until the context ends and stops them together", func(ctx SpecContext) {
				logs := captureLogs()
				started := make(chan struct{}, 2)
				stopped := make(chan string, 2)
				for _, name := range []string{"a", "b"} {
					s.AddWorker(name, func(ctx context.Context) error {
						started <- struct{}{}
						<-ctx.Done()
						stopped <- name
						return ctx.Err()
					})
				}
				cancel, done := run(ctx)
				Eventually(started).WithTimeout(time.Second).WithPolling(5 * time.Millisecond).Should(HaveLen(2))
				Consistently(done).WithTimeout(50 * time.Millisecond).WithPolling(5 * time.Millisecond).ShouldNot(Receive())
				cancel()
				Eventually(done).WithTimeout(time.Second).WithPolling(5 * time.Millisecond).Should(Receive(Succeed()))
				Expect([]string{<-stopped, <-stopped}).To(ConsistOf("a", "b"), "every worker saw its context end")
				Expect(logs.String()).To(And(
					ContainSubstring(`"msg":"worker started","worker":"a"`),
					ContainSubstring(`"msg":"worker started","worker":"b"`),
				))
			})

			DescribeTable("returns a worker's failure at shutdown whichever worker stops first",
				func(ctx SpecContext, cleanStopFirst bool) {
					flushErr := errors.New("final flush failed")
					// first closes as the worker that stops first returns, and
					// the other waits for it, so the group nearly always sees
					// the two results in that order; the repeats cover the
					// rare other order too.
					first := make(chan struct{})
					inTurn := func(isFirst bool, result func(context.Context) error) func(context.Context) error {
						return func(ctx context.Context) error {
							<-ctx.Done()
							if isFirst {
								defer close(first)
							} else {
								<-first
							}
							return result(ctx)
						}
					}
					s.AddWorker("stopper", inTurn(cleanStopFirst, func(ctx context.Context) error { return ctx.Err() }))
					s.AddWorker("flusher", inTurn(!cleanStopFirst, func(context.Context) error { return flushErr }))
					cancel, done := run(ctx)
					cancel()
					var err error
					Eventually(done).WithTimeout(time.Second).WithPolling(5 * time.Millisecond).Should(Receive(&err))
					Expect(err).To(MatchError(flushErr))
					Expect(err).To(MatchError(ContainSubstring("worker flusher")))
				},
				MustPassRepeatedly(100),
				Entry("when the clean stop returns first", true),
				Entry("when the failure returns first", false),
			)

			It("panics on a worker added once Run has started, naming the worker", func(ctx SpecContext) {
				started := make(chan struct{})
				s.AddWorker("a", func(ctx context.Context) error {
					close(started)
					<-ctx.Done()
					return ctx.Err()
				})
				cancel, done := run(ctx)
				Eventually(started).WithTimeout(time.Second).WithPolling(5 * time.Millisecond).Should(BeClosed())
				Expect(func() { s.AddWorker("late", func(context.Context) error { return nil }) }).
					To(PanicWith(ContainSubstring(`"late"`)))
				cancel()
				Eventually(done).WithTimeout(time.Second).WithPolling(5 * time.Millisecond).Should(Receive(Succeed()))
			})

			It("propagates a worker failure and cancels the others", func(ctx SpecContext) {
				boom := errors.New("boom")
				canceled := make(chan error, 1)
				s.AddWorker("a", func(context.Context) error { return boom })
				s.AddWorker("b", func(ctx context.Context) error {
					<-ctx.Done()
					canceled <- ctx.Err()
					return ctx.Err()
				})
				_, done := run(ctx)
				var err error
				Eventually(done).WithTimeout(time.Second).WithPolling(5 * time.Millisecond).Should(Receive(&err))
				Expect(err).To(MatchError(boom))
				Expect(err).To(MatchError(ContainSubstring("worker a")), "the error names the worker")
				Expect(canceled).To(Receive(MatchError(context.Canceled)), "worker b's context")
			})

			It("reports a worker's own timeout rather than taking it for its context ending", func(ctx SpecContext) {
				s.AddWorker("a", func(context.Context) error { return fmt.Errorf("flush: %w", context.DeadlineExceeded) })
				_, done := run(ctx)
				Eventually(done).WithTimeout(time.Second).WithPolling(5 * time.Millisecond).
					Should(Receive(MatchError(context.DeadlineExceeded)))
			})

			It("runs until its context ends when no worker is registered", func(ctx SpecContext) {
				cancel, done := run(ctx)
				Consistently(done).WithTimeout(50 * time.Millisecond).WithPolling(5 * time.Millisecond).ShouldNot(Receive())
				cancel()
				Eventually(done).WithTimeout(time.Second).WithPolling(5 * time.Millisecond).Should(Receive(Succeed()))
			})
		})
	})
})
