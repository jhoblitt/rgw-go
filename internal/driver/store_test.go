package driver_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

// recordingCluster is a fakerados cluster that counts its required-release
// reads, fails them with releaseErr when it is set, fails a read or write op
// on an object readErrs or writeErrs names with its error, and records the
// pool handles it opens.
type recordingCluster struct {
	*fakerados.Cluster
	releaseReads int
	releaseErr   error
	readErrs     map[string]error
	writeErrs    map[string]error
	opened       []*recordingPool
}

func (c *recordingCluster) RequiredOSDRelease(ctx context.Context) (string, error) {
	c.releaseReads++
	if c.releaseErr != nil {
		return "", c.releaseErr
	}
	return c.Cluster.RequiredOSDRelease(ctx)
}

func (c *recordingCluster) Pool(ctx context.Context, pool, namespace string) (radosclient.Pool, error) {
	p, err := c.Cluster.Pool(ctx, pool, namespace)
	if err != nil {
		return nil, err
	}
	rp := &recordingPool{Pool: p, readErrs: c.readErrs, writeErrs: c.writeErrs}
	c.opened = append(c.opened, rp)
	return rp, nil
}

// recordingPool counts Close calls on a pool handle and fails a read or
// write op on an object readErrs or writeErrs names with its error.
type recordingPool struct {
	radosclient.Pool
	readErrs  map[string]error
	writeErrs map[string]error
	closes    int
}

func (p *recordingPool) Read(ctx context.Context, oid string, op *radosclient.ReadOp, flags radosclient.OpFlags) (uint64, error) {
	if err := p.readErrs[oid]; err != nil {
		return 0, err
	}
	return p.Pool.Read(ctx, oid, op, flags)
}

func (p *recordingPool) Write(ctx context.Context, oid string, op *radosclient.WriteOp, flags radosclient.OpFlags) (uint64, error) {
	if err := p.writeErrs[oid]; err != nil {
		return 0, err
	}
	return p.Pool.Write(ctx, oid, op, flags)
}

func (p *recordingPool) Close() error {
	p.closes++
	return p.Pool.Close()
}

var _ = Describe("the driver", func() {
	// control is the control pool seedRookZone's zone names.
	const control = "ceph-objectstore.rgw.control"
	var (
		cluster *recordingCluster
		opts    *cephconf.Options
	)
	BeforeEach(func() {
		c := fakerados.New()
		c.SetRequiredOSDRelease("squid")
		seedRookZone(c, "ceph-objectstore", true)
		cluster = &recordingCluster{Cluster: c}
		opts = conf(map[string]string{"rgw_realm": "ceph-objectstore", "rgw_zonegroup": "ceph-objectstore", "rgw_zone": "ceph-objectstore"})
	})

	Describe("Open", func() {
		It("detects the release from the cluster's required OSD release", func(ctx SpecContext) {
			s, err := driver.Open(ctx, cluster, opts, driver.Options{})
			Expect(err).NotTo(HaveOccurred())
			Expect(s.Release()).To(Equal(denc.Squid))
			Expect(cluster.releaseReads).To(Equal(1))
		})

		It("honors a release override without reading the cluster's", func(ctx SpecContext) {
			t := denc.Tentacle
			s, err := driver.Open(ctx, cluster, opts, driver.Options{Release: &t})
			Expect(err).NotTo(HaveOccurred())
			Expect(s.Release()).To(Equal(denc.Tentacle))
			Expect(cluster.releaseReads).To(BeZero())
		})

		It("refuses a cluster below the Squid floor", func(ctx SpecContext) {
			cluster.SetRequiredOSDRelease("reef")
			_, err := driver.Open(ctx, cluster, opts, driver.Options{})
			Expect(err).To(MatchError(radosclient.ErrReleaseTooOld))
			Expect(err).To(MatchError(ContainSubstring(`"reef"`)))
		})

		It("encodes for the newest known release on a cluster newer than every known one", func(ctx SpecContext) {
			cluster.SetRequiredOSDRelease("vampire")
			s, err := driver.Open(ctx, cluster, opts, driver.Options{})
			Expect(err).NotTo(HaveOccurred())
			Expect(s.Release()).To(Equal(denc.Tentacle))
		})

		It("fails when the required release cannot be read", func(ctx SpecContext) {
			boom := errors.New("osd dump failed")
			cluster.releaseErr = boom
			_, err := driver.Open(ctx, cluster, opts, driver.Options{})
			Expect(err).To(MatchError(boom))
		})

		DescribeTable("fails when an option naming the zone or its root pools cannot be read",
			func(ctx SpecContext, option string) {
				m := cephconf.MapGetter{
					"rgw_realm": "", "rgw_realm_id": "", "rgw_zonegroup": "", "rgw_zonegroup_id": "",
					"rgw_zone": "ceph-objectstore", "rgw_zone_id": "", "rgw_region": "", "rgw_region_root_pool": ".rgw.root",
					"rgw_realm_root_pool": ".rgw.root", "rgw_zonegroup_root_pool": ".rgw.root",
					"rgw_zone_root_pool": ".rgw.root", "rgw_period_root_pool": ".rgw.root",
				}
				delete(m, option)
				_, err := driver.Open(ctx, cluster, cephconf.NewOptions(m), driver.Options{})
				Expect(err).To(MatchError(cephconf.ErrUnknownOption))
				Expect(err).To(MatchError(ContainSubstring(option)))
			},
			Entry("rgw_realm", "rgw_realm"),
			Entry("rgw_realm_id", "rgw_realm_id"),
			Entry("rgw_zonegroup", "rgw_zonegroup"),
			Entry("rgw_zonegroup_id", "rgw_zonegroup_id"),
			Entry("rgw_zone", "rgw_zone"),
			Entry("rgw_zone_id", "rgw_zone_id"),
			Entry("rgw_region", "rgw_region"),
			Entry("rgw_region_root_pool", "rgw_region_root_pool"),
			Entry("rgw_realm_root_pool", "rgw_realm_root_pool"),
			Entry("rgw_zonegroup_root_pool", "rgw_zonegroup_root_pool"),
			Entry("rgw_zone_root_pool", "rgw_zone_root_pool"),
			Entry("rgw_period_root_pool", "rgw_period_root_pool"),
		)

		It("opens the root pool once for its four options and the control pool, and closes both with Close", func(ctx SpecContext) {
			s, err := driver.Open(ctx, cluster, opts, driver.Options{})
			Expect(err).NotTo(HaveOccurred())
			Expect(cluster.opened).To(HaveLen(2))
			Expect([]string{cluster.opened[0].Name(), cluster.opened[0].Namespace()}).To(Equal([]string{meta.RootPool, ""}))
			Expect([]string{cluster.opened[1].Name(), cluster.opened[1].Namespace()}).To(Equal([]string{control, ""}))
			Expect(s.Close()).To(Succeed())
			Expect(cluster.opened[0].closes).To(Equal(1))
			Expect(cluster.opened[1].closes).To(Equal(1))
		})

		It("creates the control objects in the zone's control pool", func(ctx SpecContext) {
			_, err := driver.Open(ctx, cluster, opts, driver.Options{})
			Expect(err).NotTo(HaveOccurred())
			for i := range 8 {
				Expect(cluster.Object(control, "", fmt.Sprintf("notify.%d", i))).NotTo(BeNil(), "notify.%d", i)
			}
		})

		It("creates as many control objects as rgw_num_control_oids names", func(ctx SpecContext) {
			_, err := driver.Open(ctx, cluster, conf(map[string]string{
				"rgw_realm": "ceph-objectstore", "rgw_zonegroup": "ceph-objectstore", "rgw_zone": "ceph-objectstore",
				"rgw_num_control_oids": "0",
			}), driver.Options{})
			Expect(err).NotTo(HaveOccurred())
			Expect(cluster.Object(control, "", "notify")).NotTo(BeNil(), "0 is the single legacy object")
			Expect(cluster.Object(control, "", "notify.0")).To(BeNil())
		})

		It("fails naming the control pool it cannot open, and closes the pools it opened", func(ctx SpecContext) {
			cluster.FailPool(control)
			_, err := driver.Open(ctx, cluster, opts, driver.Options{})
			Expect(err).To(MatchError(radosclient.ErrNotFound))
			Expect(err).To(MatchError(ContainSubstring(control)))
			Expect(cluster.opened).To(HaveLen(1))
			Expect(cluster.opened[0].closes).To(Equal(1))
		})

		It("fails naming a control object it cannot create, and closes the pools it opened", func(ctx SpecContext) {
			boom := errors.New("no space")
			cluster.writeErrs = map[string]error{"notify.5": boom}
			_, err := driver.Open(ctx, cluster, opts, driver.Options{})
			Expect(err).To(MatchError(boom))
			Expect(err).To(MatchError(ContainSubstring("notify.5")))
			Expect(cluster.opened).To(HaveLen(2))
			Expect(cluster.opened[0].closes).To(Equal(1))
			Expect(cluster.opened[1].closes).To(Equal(1))
		})

		It("closes the pools it opened when the zone does not resolve", func(ctx SpecContext) {
			_, err := driver.Open(ctx, cluster, conf(map[string]string{"rgw_zone": "missing"}), driver.Options{})
			Expect(err).To(MatchError(driver.ErrNoZone))
			Expect(cluster.opened).To(HaveLen(1))
			Expect(cluster.opened[0].closes).To(Equal(1))
		})

		It("fails naming a driver option it cannot read, and closes the pools it opened", func(ctx SpecContext) {
			_, err := driver.Open(ctx, cluster, conf(map[string]string{
				"rgw_realm": "ceph-objectstore", "rgw_zonegroup": "ceph-objectstore", "rgw_zone": "ceph-objectstore",
				"rgw_bucket_quota_ttl": "lots",
			}), driver.Options{})
			Expect(err).To(MatchError(ContainSubstring("rgw_bucket_quota_ttl")))
			Expect(cluster.opened).To(HaveLen(1))
			Expect(cluster.opened[0].closes).To(Equal(1))
		})
	})

	Context("once open", func() {
		var s *driver.Store
		BeforeEach(func(ctx SpecContext) {
			var err error
			s, err = driver.Open(ctx, cluster, opts, driver.Options{})
			Expect(err).NotTo(HaveOccurred())
		})

		DescribeTable("answers NotImplemented from every store method",
			func(ctx SpecContext, call func(context.Context, *driver.Store) error) {
				Expect(call(ctx, s)).To(MatchError(op.ErrNotImplemented))
			},
			Entry("GetAccount", func(ctx context.Context, s *driver.Store) error {
				_, err := s.GetAccount(ctx, "RGW00000000000000001")
				return err
			}),
			Entry("AccountName", func(ctx context.Context, s *driver.Store) error {
				_, err := s.AccountName(ctx, "RGW00000000000000001")
				return err
			}),
			Entry("GetAccountByName", func(ctx context.Context, s *driver.Store) error {
				_, err := s.GetAccountByName(ctx, "", "acme")
				return err
			}),
			Entry("GetAccountByEmail", func(ctx context.Context, s *driver.Store) error {
				_, err := s.GetAccountByEmail(ctx, "ops@acme.example")
				return err
			}),
			Entry("PutAccount", func(ctx context.Context, s *driver.Store) error {
				return s.PutAccount(ctx, &op.AccountRecord{Info: meta.AccountInfo{ID: "RGW00000000000000001", Name: "acme"}}, nil, op.PutAccountOptions{Exclusive: true})
			}),
			Entry("RemoveAccount", func(ctx context.Context, s *driver.Store) error {
				return s.RemoveAccount(ctx, &op.AccountRecord{Info: meta.AccountInfo{ID: "RGW00000000000000001"}})
			}),
			Entry("AddAccountUser", func(ctx context.Context, s *driver.Store) error {
				return s.AddAccountUser(ctx, "RGW00000000000000001", meta.UserInfo{UserID: meta.UserID{ID: "alice"}, DisplayName: "Alice"})
			}),
			Entry("RemoveAccountUser", func(ctx context.Context, s *driver.Store) error {
				return s.RemoveAccountUser(ctx, "RGW00000000000000001", "Alice")
			}),
			Entry("ListAccountUsers", func(ctx context.Context, s *driver.Store) error {
				_, _, err := s.ListAccountUsers(ctx, "RGW00000000000000001", "", 1000)
				return err
			}),

			Entry("ReadUsage", func(ctx context.Context, s *driver.Store) error {
				_, _, err := s.ReadUsage(ctx, "alice", "", 0, ^uint64(0), 1000, &op.UsageIter{})
				return err
			}),
			Entry("TrimUsage", func(ctx context.Context, s *driver.Store) error {
				return s.TrimUsage(ctx, "alice", "", 0, ^uint64(0))
			}),

			Entry("IndexStats", func(ctx context.Context, s *driver.Store) error {
				_, err := s.IndexStats(ctx, &op.BucketRecord{})
				return err
			}),
			Entry("ChangeBucketOwner", func(ctx context.Context, s *driver.Store) error {
				return s.ChangeBucketOwner(ctx, &op.BucketRecord{}, meta.UserOwner(meta.UserID{ID: "alice"}), "Alice", nil)
			}),
			Entry("UnlinkBucketOwner", func(ctx context.Context, s *driver.Store) error {
				return s.UnlinkBucketOwner(ctx, &op.BucketRecord{}, meta.UserOwner(meta.UserID{ID: "alice"}))
			}),
			Entry("CheckIndex", func(ctx context.Context, s *driver.Store) error {
				_, _, err := s.CheckIndex(ctx, &op.BucketRecord{})
				return err
			}),
			Entry("RebuildIndex", func(ctx context.Context, s *driver.Store) error {
				return s.RebuildIndex(ctx, &op.BucketRecord{})
			}),
			Entry("RemoveIndexEntries", func(ctx context.Context, s *driver.Store) error {
				return s.RemoveIndexEntries(ctx, &op.BucketRecord{}, []meta.ObjKey{{Name: "k"}})
			}),
			Entry("ChownBucket", func(ctx context.Context, s *driver.Store) error {
				return s.ChownBucket(ctx, &op.BucketRecord{}, meta.AccountOwner("RGW00000000000000001"), "acme")
			}),
			Entry("SyncOwnerStats", func(ctx context.Context, s *driver.Store) error {
				return s.SyncOwnerStats(ctx, meta.UserOwner(meta.UserID{ID: "alice"}))
			}),
			Entry("PurgeBypassGC", func(ctx context.Context, s *driver.Store) error {
				return s.PurgeBypassGC(ctx, &op.BucketRecord{})
			}),

			Entry("GetRealm", func(ctx context.Context, s *driver.Store) error {
				_, err := s.GetRealm(ctx, "", "")
				return err
			}),
			Entry("ListRealms", func(ctx context.Context, s *driver.Store) error {
				_, _, err := s.ListRealms(ctx)
				return err
			}),
			Entry("GetPeriod", func(ctx context.Context, s *driver.Store) error {
				_, err := s.GetPeriod(ctx, "", "", 0)
				return err
			}),
			Entry("GetPeriodConfig", func(ctx context.Context, s *driver.Store) error {
				_, err := s.GetPeriodConfig(ctx, "")
				return err
			}),
			Entry("PutPeriodConfig", func(ctx context.Context, s *driver.Store) error {
				return s.PutPeriodConfig(ctx, "", meta.PeriodConfig{})
			}),

			Entry("CreateBucket", func(ctx context.Context, s *driver.Store) error {
				_, err := s.CreateBucket(ctx, op.CreateBucketParams{Name: "plain", Owner: meta.UserOwner(meta.UserID{ID: "alice"}), Exclusive: true})
				return err
			}),
			Entry("DeleteBucket", func(ctx context.Context, s *driver.Store) error {
				return s.DeleteBucket(ctx, &op.BucketRecord{})
			}),

			Entry("CopyObject", func(ctx context.Context, s *driver.Store) error {
				_, err := s.CopyObject(ctx, &op.ObjectState{Key: meta.ObjKey{Name: "k"}, Exists: true}, &op.BucketRecord{}, meta.ObjKey{Name: "k2"}, op.CopyParams{})
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

		It("gives an Env whose every store is itself and whose options are Open's", func() {
			env := s.Env()
			Expect([]any{
				env.Zone, env.Users, env.Accounts, env.UsageReader, env.BucketAdmin, env.Realms,
				env.Buckets, env.Objects, env.Multipart, env.Stats, env.Usage, env.Metadata,
			}).To(HaveEach(BeIdenticalTo(s)))
			Expect(env.Conf).To(BeIdenticalTo(opts))
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
				var logs bytes.Buffer
				DeferCleanup(driver.CaptureLog(&logs))
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
				var bare driver.Store
				runCtx, cancel := context.WithCancel(ctx)
				DeferCleanup(cancel)
				done := make(chan error, 1)
				go func() { done <- bare.Run(runCtx) }()
				Consistently(done).WithTimeout(50 * time.Millisecond).WithPolling(5 * time.Millisecond).ShouldNot(Receive())
				cancel()
				Eventually(done).WithTimeout(time.Second).WithPolling(5 * time.Millisecond).Should(Receive(Succeed()))
			})

			It("keeps a watch on every control object until its context ends", func(ctx SpecContext) {
				cancel, done := run(ctx)
				for i := range 8 {
					oid := fmt.Sprintf("notify.%d", i)
					Eventually(func() int { return cluster.Watches(control, "", oid) }).
						WithTimeout(time.Second).WithPolling(time.Millisecond).Should(Equal(1), oid)
				}
				cancel()
				Eventually(done).WithTimeout(time.Second).WithPolling(5 * time.Millisecond).Should(Receive(Succeed()))
				for i := range 8 {
					Expect(cluster.Watches(control, "", fmt.Sprintf("notify.%d", i))).To(BeZero(), "notify.%d", i)
				}
			})
		})
	})
})
