package fakerados_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cls/refcount"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

var _ = Describe("RefcountClass", func() {
	const data = "zone.rgw.buckets.data"
	var (
		c *fakerados.Cluster
		p radosclient.Pool
	)
	BeforeEach(func(ctx SpecContext) {
		c = fakerados.New()
		c.RegisterClass("refcount", fakerados.RefcountClass(), fakerados.RefcountWriteMethods...)
		var err error
		p, err = c.Pool(ctx, data, "")
		Expect(err).NotTo(HaveOccurred())
		c.Put(data, "", "tail", []byte("x"))
	})
	refs := func(ctx context.Context, implicit bool) []string {
		GinkgoHelper()
		op := radosclient.NewReadOp()
		res := refcount.Read(op, implicit, denc.Squid)
		_, err := p.Read(ctx, "tail", op, radosclient.OpFlagNone)
		Expect(err).NotTo(HaveOccurred())
		got, err := res.Refs()
		Expect(err).NotTo(HaveOccurred())
		return got
	}
	put := func(ctx context.Context, tag string, implicit bool) error {
		return writeErr(ctx, p, "tail", func(op *radosclient.WriteOp) { refcount.Put(op, tag, implicit, denc.Squid) })
	}
	get := func(ctx context.Context, tag string, implicit bool) error {
		return writeErr(ctx, p, "tail", func(op *radosclient.WriteOp) { refcount.Get(op, tag, implicit, denc.Squid) })
	}

	It("names the methods the class registers as writes", func() {
		Expect(fakerados.RefcountWriteMethods).To(ConsistOf("get", "put", "set"), "cls_refcount.cc:210-213")
	})

	It("adds the wildcard ahead of the first implicit get, and reads the tags sorted", func(ctx SpecContext) {
		Expect(refs(ctx, true)).To(Equal([]string{""}), "read_refcount's wildcard for an object without the xattr")
		Expect(refs(ctx, false)).To(BeEmpty())
		Expect(get(ctx, "t2", true)).To(Succeed())
		Expect(get(ctx, "t1", false)).To(Succeed())
		Expect(refs(ctx, false)).To(Equal([]string{"", "t1", "t2"}))
	})

	It("puts the wildcard for an implicit tag it does not hold, retires the tag, and removes the object with its last ref", func(ctx SpecContext) {
		Expect(put(ctx, "gc-tag\x00", true)).To(Succeed())
		Expect(c.Object(data, "", "tail")).To(BeNil(), "cls_refcount.cc:130-132")
	})

	It("drops a held tag once, keeping the object while refs remain", func(ctx SpecContext) {
		Expect(get(ctx, "a", true)).To(Succeed())
		Expect(put(ctx, "a", false)).To(Succeed())
		Expect(refs(ctx, false)).To(Equal([]string{""}))
		Expect(get(ctx, "a", false)).To(Succeed())
		Expect(put(ctx, "a", false)).To(Succeed())
		Expect(refs(ctx, false)).To(Equal([]string{"", "a"}), "a retired tag is not dropped again")
		Expect(put(ctx, "missing", false)).To(Succeed(), "a tag not held changes nothing")
		Expect(refs(ctx, false)).To(Equal([]string{"", "a"}))
	})

	It("refuses a put on an object with no refs at all with EINVAL, and get, put and read on a missing one with ENOENT", func(ctx SpecContext) {
		Expect(put(ctx, "t", false)).To(MatchError(radosclient.ErrInvalid), "cls_refcount.cc:105-108")
		for _, method := range []string{"get", "put"} {
			err := writeErr(ctx, p, "absent", func(op *radosclient.WriteOp) {
				if method == "get" {
					refcount.Get(op, "t", true, denc.Squid)
				} else {
					refcount.Put(op, "t", true, denc.Squid)
				}
			})
			Expect(err).To(MatchError(radosclient.ErrNotFound), method)
		}
		op := radosclient.NewReadOp()
		refcount.Read(op, true, denc.Squid)
		_, err := p.Read(ctx, "absent", op, radosclient.OpFlagNone)
		Expect(err).To(MatchError(radosclient.ErrNotFound), "read")
		Expect(c.Object(data, "", "absent")).To(BeNil())
	})

	It("replaces the refs with set, and removes the object with an empty set", func(ctx SpecContext) {
		Expect(get(ctx, "a", true)).To(Succeed())
		Expect(writeErr(ctx, p, "tail", func(op *radosclient.WriteOp) { refcount.Set(op, []string{"y", "x"}, denc.Squid) })).To(Succeed())
		Expect(refs(ctx, false)).To(Equal([]string{"x", "y"}))
		Expect(writeErr(ctx, p, "tail", func(op *radosclient.WriteOp) { refcount.Set(op, nil, denc.Squid) })).To(Succeed())
		Expect(c.Object(data, "", "tail")).To(BeNil(), "cls_refcount.cc:153-155")
	})
})
