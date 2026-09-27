//go:build integration

package version_test

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cls/version"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/radosclient/goceph"
	"github.com/jhoblitt/rgw-go/internal/testutil/cephtest"
)

// manifest is the part of populate.sh's manifest.json these specs read.
type manifest struct {
	Pools   struct{ Meta string }
	Buckets []struct{ Name, ID string }
}

var _ = Describe("version against a cluster", Label("integration"), func() {
	var (
		cluster radosclient.Cluster
		m       manifest
	)

	BeforeEach(func(ctx SpecContext) {
		conf := cephtest.Conf()
		cephtest.ReadManifest(conf, &m)
		var err error
		cluster, err = goceph.Connect(ctx, goceph.Config{ConfigFile: conf})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(cluster.Close()).To(Succeed()) })
	})

	It("reads the version radosgw created on the plain bucket's instance object", func(ctx SpecContext) {
		var id string
		for _, b := range m.Buckets {
			if b.Name == "plain" {
				id = b.ID
			}
		}
		Expect(id).NotTo(BeEmpty())
		pool, err := cluster.Pool(ctx, m.Pools.Meta, "root")
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(pool.Close()).To(Succeed()) })

		op := radosclient.NewReadOp()
		res := version.Read(op, denc.Squid)
		xattrs := op.GetXattrs()
		Expect(pool.Read(ctx, ".bucket.meta.plain:"+id, op, radosclient.OpFlagNone)).Error().To(Succeed())
		v, err := res.Version()
		Expect(err).NotTo(HaveOccurred())
		Expect(v.Ver).To(BeEquivalentTo(1))
		Expect(v.Tag).To(HaveLen(24))

		Expect(xattrs.Xattrs).To(HaveKey(version.XattrName))
		d := denc.NewDecoder(xattrs.Xattrs[version.XattrName])
		Expect(version.DecodeObjVersion(d)).To(Equal(v))
		Expect(d.Err()).NotTo(HaveOccurred())
		Expect(d.Remaining()).To(BeZero())
	})

	It("creates, increments, checks and sets a version", func(ctx SpecContext) {
		pool, err := cluster.Pool(ctx, cephtest.TestPool, "")
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(pool.Close()).To(Succeed()) })
		oid := fmt.Sprintf("cls-version-%d", time.Now().UnixNano())
		DeferCleanup(func(ctx SpecContext) {
			w := radosclient.NewWriteOp()
			w.Remove()
			_, err := pool.Write(ctx, oid, w, radosclient.OpFlagNone)
			Expect(err).NotTo(HaveOccurred())
		})
		read := func() version.ObjVersion {
			GinkgoHelper()
			op := radosclient.NewReadOp()
			res := version.Read(op, denc.Squid)
			Expect(pool.Read(ctx, oid, op, radosclient.OpFlagNone)).Error().To(Succeed())
			v, err := res.Version()
			Expect(err).NotTo(HaveOccurred())
			return v
		}
		write := func(build func(*radosclient.WriteOp)) error {
			w := radosclient.NewWriteOp()
			build(w)
			_, err := pool.Write(ctx, oid, w, radosclient.OpFlagNone)
			return err
		}

		Expect(write(func(w *radosclient.WriteOp) {
			w.Create(true)
			version.Inc(w, denc.Squid)
		})).To(Succeed())
		v1 := read()
		// init_version writes 1, then inc increments it.
		Expect(v1.Ver).To(BeEquivalentTo(2))
		Expect(v1.Tag).To(HaveLen(24))

		Expect(write(func(w *radosclient.WriteOp) { version.IncConds(w, v1, version.CondEQ, denc.Squid) })).To(Succeed())
		v2 := read()
		Expect(v2).To(Equal(version.ObjVersion{Ver: 3, Tag: v1.Tag}))

		Expect(write(func(w *radosclient.WriteOp) { version.IncConds(w, v1, version.CondEQ, denc.Squid) })).
			To(MatchError(radosclient.ErrCanceled))

		check := radosclient.NewReadOp()
		version.Check(check, v1, version.CondGT, denc.Squid)
		Expect(pool.Read(ctx, oid, check, radosclient.OpFlagNone)).Error().To(Succeed())
		check = radosclient.NewReadOp()
		version.Check(check, v2, version.CondTagNE, denc.Squid)
		Expect(pool.Read(ctx, oid, check, radosclient.OpFlagNone)).Error().To(MatchError(radosclient.ErrCanceled))

		set := version.ObjVersion{Ver: 42, Tag: "tag"}
		Expect(write(func(w *radosclient.WriteOp) { version.Set(w, set, denc.Squid) })).To(Succeed())
		Expect(read()).To(Equal(set))
	})
})
