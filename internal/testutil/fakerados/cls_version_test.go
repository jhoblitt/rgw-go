package fakerados_test

import (
	"context"
	"syscall"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cls/version"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

var _ = Describe("VersionClass", func() {
	var (
		c *fakerados.Cluster
		p radosclient.Pool
	)
	BeforeEach(func(ctx SpecContext) {
		c, p = newCluster(ctx)
		c.RegisterClass("version", fakerados.VersionClass(), fakerados.VersionWriteMethods...)
	})
	stored := func(oid string) *fakerados.Object { return c.Object(poolName, ns, oid) }
	seed := func(oid string, v version.ObjVersion) {
		c.Put(poolName, ns, oid, []byte("d"))
		stored(oid).Xattrs[version.XattrName] = encodeVersion(v)
	}
	// read runs cls_version's read on oid.
	read := func(ctx context.Context, oid string) (version.ObjVersion, error) {
		op := radosclient.NewReadOp()
		res := version.Read(op, denc.Squid)
		if _, err := p.Read(ctx, oid, op, radosclient.OpFlagNone); err != nil {
			return version.ObjVersion{}, err
		}
		return res.Version()
	}

	It("sets the version it is given, creating a missing object", func(ctx SpecContext) {
		v := version.ObjVersion{Ver: 7, Tag: "_tag"}
		Expect(writeErr(ctx, p, "obj", func(op *radosclient.WriteOp) { version.Set(op, v, denc.Squid) })).To(Succeed())
		Expect(stored("obj")).NotTo(BeNil())
		Expect(decodeVersion(stored("obj").Xattrs[version.XattrName])).To(Equal(v))
		Expect(read(ctx, "obj")).To(Equal(v))
	})

	It("initializes a missing version to 1 with a 24-character base64 tag before incrementing it", func(ctx SpecContext) {
		Expect(writeErr(ctx, p, "obj", func(op *radosclient.WriteOp) { version.Inc(op, denc.Squid) })).To(Succeed())
		got := decodeVersion(stored("obj").Xattrs[version.XattrName])
		Expect(got.Ver).To(BeEquivalentTo(2), "init_version then inc, cls_version.cc:37-52, :161-168")
		Expect(got.Tag).To(MatchRegexp(`^[A-Za-z0-9+/]{24}$`))
	})

	It("increments the stored version and keeps its tag", func(ctx SpecContext) {
		seed("obj", version.ObjVersion{Ver: 3, Tag: "t"})
		Expect(writeErr(ctx, p, "obj", func(op *radosclient.WriteOp) { version.Inc(op, denc.Squid) })).To(Succeed())
		Expect(read(ctx, "obj")).To(Equal(version.ObjVersion{Ver: 4, Tag: "t"}))
	})

	It("increments a stored version 0 rather than initializing it, since only a missing xattr is initialized", func(ctx SpecContext) {
		seed("obj", version.ObjVersion{Ver: 0, Tag: "t"})
		Expect(writeErr(ctx, p, "obj", func(op *radosclient.WriteOp) { version.Inc(op, denc.Squid) })).To(Succeed())
		Expect(read(ctx, "obj")).To(Equal(version.ObjVersion{Ver: 1, Tag: "t"}), "cls_version.cc:58-66")
	})

	It("increments under inc_conds only when the conditions hold", func(ctx SpecContext) {
		seed("obj", version.ObjVersion{Ver: 3, Tag: "t"})
		err := writeErr(ctx, p, "obj", func(op *radosclient.WriteOp) {
			version.IncConds(op, version.ObjVersion{Ver: 3, Tag: "other"}, version.CondEQ, denc.Squid)
		})
		Expect(err).To(MatchError(radosclient.ErrCanceled), "cls_version.cc:165-167")
		Expect(read(ctx, "obj")).To(Equal(version.ObjVersion{Ver: 3, Tag: "t"}))
		Expect(writeErr(ctx, p, "obj", func(op *radosclient.WriteOp) {
			version.IncConds(op, version.ObjVersion{Ver: 3, Tag: "t"}, version.CondEQ, denc.Squid)
		})).To(Succeed())
		Expect(read(ctx, "obj")).To(Equal(version.ObjVersion{Ver: 4, Tag: "t"}))
	})

	DescribeTable("checks a condition against the stored version, failing with ECANCELED",
		func(ctx SpecContext, cond version.Cond, against version.ObjVersion, holds bool) {
			seed("obj", version.ObjVersion{Ver: 3, Tag: "t"})
			err := writeErr(ctx, p, "obj", func(op *radosclient.WriteOp) {
				version.Check(op, against, cond, denc.Squid)
				op.SetXattr("touched", []byte{1})
			})
			if holds {
				Expect(err).NotTo(HaveOccurred())
				return
			}
			Expect(err).To(MatchError(radosclient.ErrCanceled))
			Expect(stored("obj").Xattrs).NotTo(HaveKey("touched"))
		},
		Entry("EQ on ver and tag", version.CondEQ, version.ObjVersion{Ver: 3, Tag: "t"}, true),
		Entry("EQ with another tag", version.CondEQ, version.ObjVersion{Ver: 3, Tag: "u"}, false),
		Entry("EQ with another ver", version.CondEQ, version.ObjVersion{Ver: 2, Tag: "t"}, false),
		Entry("GT on ver alone", version.CondGT, version.ObjVersion{Ver: 2, Tag: "u"}, true),
		Entry("GT at the same ver", version.CondGT, version.ObjVersion{Ver: 3}, false),
		Entry("GE at the same ver", version.CondGE, version.ObjVersion{Ver: 3}, true),
		Entry("GE below", version.CondGE, version.ObjVersion{Ver: 4}, false),
		Entry("LT", version.CondLT, version.ObjVersion{Ver: 4}, true),
		Entry("LT at the same ver", version.CondLT, version.ObjVersion{Ver: 3}, false),
		Entry("LE at the same ver", version.CondLE, version.ObjVersion{Ver: 3}, true),
		Entry("LE above", version.CondLE, version.ObjVersion{Ver: 2}, false),
		Entry("TAG_EQ on the tag alone", version.CondTagEQ, version.ObjVersion{Ver: 9, Tag: "t"}, true),
		Entry("TAG_EQ with another tag", version.CondTagEQ, version.ObjVersion{Ver: 3, Tag: "u"}, false),
		Entry("TAG_NE with another tag", version.CondTagNE, version.ObjVersion{Ver: 3, Tag: "u"}, true),
		Entry("TAG_NE with the same tag", version.CondTagNE, version.ObjVersion{Ver: 3, Tag: "t"}, false),
		Entry("NONE", version.CondNone, version.ObjVersion{}, true),
		Entry("an undefined condition, which no case of check_conds matches", version.Cond(42), version.ObjVersion{}, true),
	)

	It("reads an object without a version as the zero version, and checks it as such", func(ctx SpecContext) {
		c.Put(poolName, ns, "obj", []byte("d"))
		Expect(read(ctx, "obj")).To(Equal(version.ObjVersion{}), "read_version answers ENODATA with ver 0, cls_version.cc:59-65")
		Expect(writeErr(ctx, p, "obj", func(op *radosclient.WriteOp) {
			version.Check(op, version.ObjVersion{Ver: 1, Tag: "t"}, version.CondEQ, denc.Squid)
		})).To(MatchError(radosclient.ErrCanceled))
	})

	It("fails a read of a missing object with ENOENT", func(ctx SpecContext) {
		_, err := read(ctx, "absent")
		Expect(err).To(MatchError(radosclient.ErrNotFound))
	})

	It("reads the stored version across radosgw's overwrite and writes through the new object", func(ctx SpecContext) {
		seed("obj", version.ObjVersion{Ver: 3, Tag: "t"})
		err := writeErr(ctx, p, "obj", func(op *radosclient.WriteOp) {
			op.Remove()
			op.SetStepFlags(radosclient.StepFlagFailOK)
			op.Create(false)
			version.Check(op, version.ObjVersion{Ver: 3, Tag: "t"}, version.CondEQ, denc.Squid)
			version.Inc(op, denc.Squid)
			op.WriteFull([]byte("new"))
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(stored("obj").Data).To(Equal([]byte("new")))
		Expect(read(ctx, "obj")).To(Equal(version.ObjVersion{Ver: 4, Tag: "t"}))
	})

	It("fails with EIO on a stored version that does not decode", func(ctx SpecContext) {
		c.Put(poolName, ns, "obj", []byte("d"))
		stored("obj").Xattrs[version.XattrName] = []byte{1}
		_, err := read(ctx, "obj")
		Expect(err).To(haveErrno(syscall.EIO), "cls_version.cc:70-76")
	})

	It("fails with EINVAL on a request that does not decode", func(ctx SpecContext) {
		err := writeErr(ctx, p, "obj", func(op *radosclient.WriteOp) { op.Exec("version", "set", []byte{1}) })
		Expect(err).To(MatchError(radosclient.ErrInvalid), "cls_version.cc:86-92")
	})

	It("names the methods that write, so check_conds and read cannot", func() {
		Expect(fakerados.VersionWriteMethods).To(ConsistOf("set", "inc", "inc_conds"), "CLS_METHOD_WR, cls_version.cc:230-234")
	})
})
