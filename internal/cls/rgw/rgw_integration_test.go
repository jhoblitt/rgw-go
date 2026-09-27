//go:build integration

package rgw_test

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/radosclient/goceph"
	"github.com/jhoblitt/rgw-go/internal/testutil/cephtest"
)

// manifest is the part of hack/cluster/populate.sh's manifest.json these
// specs read.
type manifest struct {
	Pools struct {
		Index string `json:"index"`
	} `json:"pools"`
	Buckets []struct {
		Name      string `json:"name"`
		ID        string `json:"id"`
		NumShards int    `json:"num_shards"`
	} `json:"buckets"`
	Objects []struct {
		Bucket string `json:"bucket"`
		Key    string `json:"key"`
		Size   uint64 `json:"size"`
	} `json:"objects"`
}

// etagRE matches a plain MD5 etag or a multipart one, "<md5>-<parts>".
var etagRE = regexp.MustCompile(`^[0-9a-f]{32}(-[0-9]+)?$`)

// objectName is rgw_obj_key::parse_raw_oid for a key outside any namespace:
// an index key beginning "__" is an escaped name beginning "_".
func objectName(indexKey string) (string, bool) {
	if !strings.HasPrefix(indexKey, "_") {
		return indexKey, true
	}
	if strings.HasPrefix(indexKey, "__") {
		return indexKey[1:], true
	}
	return "", false
}

var _ = Describe("cls rgw against the populated cluster", Label("integration"), func() {
	var (
		m      manifest
		index  radosclient.Pool
		oidFmt string
		shards int
	)

	BeforeEach(func(ctx SpecContext) {
		conf := cephtest.Conf()
		cephtest.ReadManifest(conf, &m)

		cluster, err := goceph.Connect(ctx, goceph.Config{ConfigFile: conf})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(cluster.Close()).To(Succeed()) })
		index, err = cluster.Pool(ctx, m.Pools.Index, "")
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(index.Close()).To(Succeed()) })

		for _, bk := range m.Buckets {
			if bk.Name == "plain" {
				oidFmt = ".dir." + bk.ID + ".%d"
				shards = bk.NumShards
			}
		}
		Expect(shards).To(Equal(11), "the plain bucket's shard count")
	})

	It("reads shard 0's header of the plain bucket", func(ctx SpecContext) {
		op := radosclient.NewReadOp()
		res := rgw.GetDirHeader(op, denc.Squid)
		Expect(index.Read(ctx, fmt.Sprintf(oidFmt, 0), op, radosclient.OpFlagNone)).Error().To(Succeed())
		ret, err := res.Result()
		Expect(err).NotTo(HaveOccurred())
		Expect(ret.Dir.Entries).To(BeEmpty())
		Expect(ret.Dir.Header.Ver).To(BeNumerically(">=", 1))
		Expect(ret.Dir.Header.NewInstance.ReshardStatus).To(Equal(rgw.ReshardNone))
	})

	It("lists exactly the manifest's objects across all eleven shards", func(ctx SpecContext) {
		merged := map[string]rgw.DirEntry{}
		for shard := range shards {
			var start rgw.ObjKey
			for {
				op := radosclient.NewReadOp()
				res := rgw.BucketList(op, rgw.ListOp{StartObj: start, NumEntries: 2}, denc.Squid)
				Expect(index.Read(ctx, fmt.Sprintf(oidFmt, shard), op, radosclient.OpFlagNone)).Error().To(Succeed())
				ret, err := res.Result()
				Expect(err).NotTo(HaveOccurred())
				for k, en := range ret.Dir.Entries {
					Expect(merged).NotTo(HaveKey(k), "index key %q on two shards", k)
					merged[k] = en
				}
				if !ret.IsTruncated {
					break
				}
				start = ret.Marker
			}
		}

		want := map[string]uint64{}
		for _, o := range m.Objects {
			if o.Bucket == "plain" {
				want[o.Key] = o.Size
			}
		}
		got := map[string]uint64{}
		for k, en := range merged {
			Expect(en.Key.Name).To(Equal(k), "entry name under index key %q", k)
			Expect(en.Key.Instance).To(BeEmpty())
			name, ok := objectName(k)
			Expect(ok).To(BeTrue(), "index key %q is namespaced", k)
			Expect(en.Exists).To(BeTrue(), "%s exists", name)
			Expect(en.Meta.ETag).To(MatchRegexp(etagRE.String()), "%s etag", name)
			got[name] = en.Meta.Size
		}
		Expect(got).To(Equal(want))
		Expect(merged).To(HaveKey("__underscore.bin"))
	})

	It("fails a guarded prepare while the shard is resharding and runs it after", func(ctx SpecContext) {
		cluster, err := goceph.Connect(ctx, goceph.Config{ConfigFile: cephtest.Conf()})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(cluster.Close()).To(Succeed()) })
		ns := fmt.Sprintf("cls-rgw-guard-%d", time.Now().UnixNano())
		scratch, err := cluster.Pool(ctx, cephtest.TestPool, ns)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(scratch.Close()).To(Succeed()) })
		const oid = ".dir.scratch.0"

		initOp := radosclient.NewWriteOp()
		initOp.Create(true)
		rgw.BucketInitIndex(initOp)
		_, err = scratch.Write(ctx, oid, initOp, radosclient.OpFlagNone)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func(ctx SpecContext) {
			rm := radosclient.NewWriteOp()
			rm.Remove()
			_, err := scratch.Write(ctx, oid, rm, radosclient.OpFlagNone)
			Expect(err).NotTo(HaveOccurred())
		})

		setStatus := func(status uint8) {
			op := radosclient.NewWriteOp()
			rgw.SetBucketResharding(op, rgw.InstanceEntry{ReshardStatus: status}, denc.Squid)
			_, err := scratch.Write(ctx, oid, op, radosclient.OpFlagNone)
			Expect(err).NotTo(HaveOccurred())
		}
		guardedPrepare := func() error {
			op := radosclient.NewWriteOp()
			rgw.GuardBucketResharding(op, denc.Squid)
			rgw.BucketPrepareOp(op, rgw.PrepareOp{Op: rgw.OpAdd, Key: rgw.ObjKey{Name: "guarded"}, Tag: "guard-tag"}, denc.Squid)
			_, err := scratch.Write(ctx, oid, op, radosclient.OpFlagNone)
			return err
		}
		entries := func() map[string][]byte {
			op := radosclient.NewReadOp()
			vals := op.OmapGetVals("", "", 100)
			Expect(scratch.Read(ctx, oid, op, radosclient.OpFlagNone)).Error().To(Succeed())
			Expect(vals.Err).NotTo(HaveOccurred())
			return vals.Values
		}

		setStatus(rgw.ReshardInProgress)
		Expect(guardedPrepare()).To(MatchError(radosclient.ErrBusyResharding))
		Expect(entries()).To(BeEmpty(), "the guarded prepare left an index entry")

		setStatus(rgw.ReshardNone)
		Expect(guardedPrepare()).To(Succeed())
		vals := entries()
		Expect(vals).To(HaveKey("guarded"))
		d := denc.NewDecoder(vals["guarded"])
		en := rgw.DecodeDirEntry(d)
		Expect(d.Err()).NotTo(HaveOccurred())
		Expect(en.PendingMap).To(HaveLen(1))
		Expect(en.PendingMap[0].Tag).To(Equal("guard-tag"))
	})
})
