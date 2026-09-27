//go:build integration

package gc_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cls/gc"
	"github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/cls/version"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/radosclient/goceph"
)

// testPool is the scratch pool hack/cluster/up.sh creates.
const testPool = "rgw-go-test"

// cephConf resolves RGW_GO_TEST_CEPH_CONF, taking a relative path from the
// module root.
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

// logPool reads the zone's log pool from the manifest populate.sh wrote next to conf.
func logPool(conf string) string {
	b, err := os.ReadFile(filepath.Join(filepath.Dir(conf), "manifest.json"))
	Expect(err).NotTo(HaveOccurred(), "run make populate first")
	var m struct {
		Pools struct {
			Log string `json:"log"`
		} `json:"pools"`
	}
	Expect(json.Unmarshal(b, &m)).To(Succeed())
	Expect(m.Pools.Log).NotTo(BeEmpty())
	return m.Pools.Log
}

// decodeObjVersion decodes the cls_version xattr.
func decodeObjVersion(b []byte) version.ObjVersion {
	GinkgoHelper()
	d := denc.NewDecoder(b)
	v := version.DecodeObjVersion(d)
	Expect(d.Err()).NotTo(HaveOccurred())
	return v
}

var _ = Describe("rgw_gc against a cluster", Label("integration"), func() {
	var (
		conf    string
		cluster radosclient.Cluster
	)

	BeforeEach(func(ctx SpecContext) {
		conf = cephConf()
		if conf == "" {
			Skip("RGW_GO_TEST_CEPH_CONF is not set")
		}
		var err error
		cluster, err = goceph.Connect(ctx, goceph.Config{ConfigFile: conf})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(cluster.Close()).To(Succeed()) })
	})

	It("lists and decodes every queue-era GC shard radosgw created", func(ctx SpecContext) {
		pool, err := cluster.Pool(ctx, logPool(conf), "gc")
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(pool.Close()).To(Succeed()) })

		maxObjs, err := cluster.ConfigGet("rgw_gc_max_objs")
		Expect(err).NotTo(HaveOccurred())
		n, err := strconv.Atoi(maxObjs)
		Expect(err).NotTo(HaveOccurred())
		want := make([]string, n)
		for i := range n {
			want[i] = gc.ShardOID(i)
		}
		var got []string
		Expect(pool.ListObjects(ctx, func(oid, _ string) error {
			got = append(got, oid)
			return nil
		})).To(Succeed())
		Expect(got).To(ConsistOf(want))

		total := 0
		for _, oid := range want {
			op := radosclient.NewReadOp()
			xs := op.GetXattrs()
			res := gc.QueueList(op, "", 1000, false, denc.Squid)
			Expect(pool.Read(ctx, oid, op, radosclient.OpFlagNone)).Error().To(Succeed(), oid)
			Expect(xs.Err).NotTo(HaveOccurred(), oid)
			Expect(decodeObjVersion(xs.Xattrs[version.XattrName]).Ver).To(BeEquivalentTo(1), "%s is not queue-era", oid)
			ret, err := res.Result()
			Expect(err).NotTo(HaveOccurred(), oid)
			Expect(ret.Truncated).To(BeFalse(), oid)
			for _, e := range ret.Entries {
				Expect(e.Tag).NotTo(BeEmpty(), oid)
				Expect(e.Chain).NotTo(BeEmpty(), oid)
				Expect(e.Time).NotTo(BeZero(), oid)
			}
			total += len(ret.Entries)
		}
		GinkgoWriter.Printf("%d GC shards, %d queued entries\n", n, total)
	})

	Context("on a scratch queue", func() {
		var (
			pool radosclient.Pool
			oid  string
		)

		BeforeEach(func(ctx SpecContext) {
			var err error
			pool, err = cluster.Pool(ctx, testPool, "gc-it")
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() { Expect(pool.Close()).To(Succeed()) })
			oid = fmt.Sprintf("gc-%d-%d", CurrentSpecReport().LeafNodeLocation.LineNumber, time.Now().UnixNano())
			DeferCleanup(func(ctx SpecContext) {
				op := radosclient.NewWriteOp()
				op.Remove()
				_, err := pool.Write(ctx, oid, op, radosclient.OpFlagNone)
				if err != nil {
					Expect(err).To(MatchError(radosclient.ErrNotFound))
				}
			})
		})

		list := func(ctx SpecContext, expiredOnly bool) gc.ListRet {
			GinkgoHelper()
			op := radosclient.NewReadOp()
			res := gc.QueueList(op, "", 0, expiredOnly, denc.Squid)
			Expect(pool.Read(ctx, oid, op, radosclient.OpFlagNone)).Error().To(Succeed())
			ret, err := res.Result()
			Expect(err).NotTo(HaveOccurred())
			return ret
		}
		write := func(ctx SpecContext, build func(*radosclient.WriteOp)) error {
			op := radosclient.NewWriteOp()
			build(op)
			_, err := pool.Write(ctx, oid, op, radosclient.OpFlagNone)
			return err
		}
		tags := func(ret gc.ListRet) []string {
			var ts []string
			for _, e := range ret.Entries {
				ts = append(ts, e.Tag)
			}
			return ts
		}

		It("answers ENOENT to a list and EINVAL to an enqueue before the queue exists", func(ctx SpecContext) {
			op := radosclient.NewReadOp()
			res := gc.QueueList(op, "", 0, false, denc.Squid)
			Expect(pool.Read(ctx, oid, op, radosclient.OpFlagNone)).Error().To(MatchError(radosclient.ErrNotFound))
			_, err := res.Result()
			Expect(err).To(HaveOccurred())

			Expect(write(ctx, func(op *radosclient.WriteOp) {
				gc.QueueEnqueue(op, 0, rgw.GCObjInfo{Tag: "t"}, denc.Squid)
			})).To(MatchError(radosclient.ErrInvalid))

			st := radosclient.NewReadOp()
			st.Stat()
			Expect(pool.Read(ctx, oid, st, radosclient.OpFlagNone)).Error().To(MatchError(radosclient.ErrNotFound))
		})

		It("creates a missing object on init and refuses a second init with EEXIST", func(ctx SpecContext) {
			Expect(write(ctx, func(op *radosclient.WriteOp) { gc.QueueInit(op, 1<<20, 2, denc.Squid) })).To(Succeed())

			op := radosclient.NewReadOp()
			st := op.Stat()
			Expect(pool.Read(ctx, oid, op, radosclient.OpFlagNone)).Error().To(Succeed())
			Expect(st.Size).To(BeNumerically(">", 0))
			Expect(list(ctx, false)).To(Equal(gc.ListRet{}))

			Expect(write(ctx, func(op *radosclient.WriteOp) {
				op.Create(false)
				gc.QueueInit(op, 1<<20, 2, denc.Squid)
			})).To(MatchError(radosclient.ErrExists))
		})

		It("enqueues, lists, removes and defers entries", func(ctx SpecContext) {
			Expect(write(ctx, func(op *radosclient.WriteOp) {
				op.Create(false)
				gc.QueueInit(op, 1<<20, 2, denc.Squid)
			})).To(Succeed())

			chain := []rgw.GCObj{
				{Pool: "default.rgw.buckets.data", Key: rgw.ObjKey{Name: "tail-1"}},
				{Pool: "default.rgw.buckets.data", Key: rgw.ObjKey{Name: "tail-2", Instance: "v1"}, Loc: "loc"},
			}
			before := time.Now().Add(-time.Minute)
			// One enqueue per op: a class method's head read does not see an
			// earlier method's write in the same op.
			Expect(write(ctx, func(op *radosclient.WriteOp) {
				gc.QueueEnqueue(op, 0, rgw.GCObjInfo{Tag: "now\x00", Chain: chain}, denc.Squid)
			})).To(Succeed())
			Expect(write(ctx, func(op *radosclient.WriteOp) {
				gc.QueueEnqueue(op, 3600, rgw.GCObjInfo{Tag: "later\x00", Chain: chain[:1]}, denc.Squid)
			})).To(Succeed())

			all := list(ctx, false)
			Expect(all.Truncated).To(BeFalse())
			Expect(tags(all)).To(Equal([]string{"now\x00", "later\x00"}))
			Expect(all.Entries[0].Chain).To(Equal(chain))
			Expect(all.Entries[0].Time).To(BeTemporally(">", before))
			Expect(all.Entries[1].Time).To(BeTemporally("~", all.Entries[0].Time.Add(time.Hour), time.Minute))
			Expect(tags(list(ctx, true))).To(Equal([]string{"now\x00"}))

			Expect(write(ctx, func(op *radosclient.WriteOp) { gc.QueueRemoveEntries(op, 1, denc.Squid) })).To(Succeed())
			Expect(tags(list(ctx, false))).To(Equal([]string{"later\x00"}))

			Expect(write(ctx, func(op *radosclient.WriteOp) {
				gc.QueueUpdateEntry(op, 7200, rgw.GCObjInfo{Tag: "later\x00"}, denc.Squid)
			})).To(Succeed())
			Expect(list(ctx, false).Entries).To(BeEmpty())
		})
	})
})
