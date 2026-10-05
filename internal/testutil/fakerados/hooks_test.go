package fakerados_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

var _ = Describe("AfterWrite, BeforeRead and Objects", func() {
	var (
		c *fakerados.Cluster
		p radosclient.Pool
	)
	BeforeEach(func(ctx SpecContext) {
		c, p = newCluster(ctx)
	})

	It("runs an AfterWrite hook after each write op on the object that succeeds, outside the cluster's lock", func(ctx SpecContext) {
		var runs int
		c.AfterWrite(poolName, ns, "obj", func() {
			runs++
			Expect(c.Object(poolName, ns, "obj")).NotTo(BeNil(), "the op's change is stored before the hook runs")
			c.Remove(poolName, ns, "obj")
		})
		Expect(writeErr(ctx, p, "obj", func(op *radosclient.WriteOp) { op.Create(true) })).To(Succeed())
		Expect(runs).To(Equal(1))
		Expect(c.Object(poolName, ns, "obj")).To(BeNil(), "the hook may call the cluster")
		Expect(writeErr(ctx, p, "obj", func(op *radosclient.WriteOp) { op.AssertExists() })).To(MatchError(radosclient.ErrNotFound))
		Expect(runs).To(Equal(1), "a failed op runs no hook")
		Expect(writeErr(ctx, p, "other", func(op *radosclient.WriteOp) { op.Create(true) })).To(Succeed())
		Expect(runs).To(Equal(1), "another object's op runs no hook")
		c.AfterWrite(poolName, ns, "obj", nil)
		Expect(writeErr(ctx, p, "obj", func(op *radosclient.WriteOp) { op.Create(true) })).To(Succeed())
		Expect(runs).To(Equal(1), "a nil hook removes it")
	})

	It("runs a BeforeRead hook before each read op on the object, outside the cluster's lock", func(ctx SpecContext) {
		var runs int
		c.BeforeRead(poolName, ns, "obj", func() {
			runs++
			c.Put(poolName, ns, "obj", []byte("staged"))
		})
		rop := radosclient.NewReadOp()
		res := rop.Read(0, 16)
		_, err := p.Read(ctx, "obj", rop, radosclient.OpFlagNone)
		Expect(err).NotTo(HaveOccurred(), "the hook may call the cluster, and the read meets what it staged")
		Expect(res.Data).To(Equal([]byte("staged")))
		Expect(writeErr(ctx, p, "obj", func(op *radosclient.WriteOp) { op.WriteFull([]byte("x")) })).To(Succeed())
		Expect(runs).To(Equal(1), "a write op runs no read hook")
		c.BeforeRead(poolName, ns, "obj", nil)
		rop = radosclient.NewReadOp()
		rop.Stat()
		_, err = p.Read(ctx, "obj", rop, radosclient.OpFlagNone)
		Expect(err).NotTo(HaveOccurred())
		Expect(runs).To(Equal(1), "a nil hook removes it")
	})

	It("returns the objects of a namespace by name with Objects, and none for an empty one", func(ctx SpecContext) {
		c.Put(poolName, ns, "a", []byte("1"))
		c.Put(poolName, ns, "b", nil)
		c.Put(poolName, "other", "c", nil)
		objs := c.Objects(poolName, ns)
		Expect(objs).To(HaveLen(2))
		Expect(objs).To(HaveKeyWithValue("a", c.Object(poolName, ns, "a")))
		Expect(objs).To(HaveKey("b"))
		delete(objs, "a")
		Expect(c.Object(poolName, ns, "a")).NotTo(BeNil(), "the map is a copy")
		Expect(c.Objects(poolName, "empty")).To(BeEmpty())
	})
})
