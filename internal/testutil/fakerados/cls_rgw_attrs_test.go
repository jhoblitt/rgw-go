package fakerados_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	rgwcls "github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

var _ = Describe("RGWClass's obj_check_attrs_prefix and the OLH head's cmpxattr", func() {
	const (
		data    = "zone.rgw.buckets.data"
		head    = "4155.1_k"
		pending = "user.rgw.olh.pending."
	)
	var (
		c *fakerados.Cluster
		p radosclient.Pool
	)
	BeforeEach(func(ctx SpecContext) {
		c = fakerados.New()
		c.RegisterClassMethods("rgw", fakerados.RGWClass(), fakerados.RGWMethods, fakerados.RGWWriteMethods)
		var err error
		p, err = c.Pool(ctx, data, "")
		Expect(err).NotTo(HaveOccurred())
	})
	putHead := func(xattrs ...string) {
		c.Put(data, "", head, []byte("head"))
		for _, x := range xattrs {
			c.Object(data, "", head).Xattrs[x] = []byte("v")
		}
	}
	check := func(ctx context.Context, prefix string, failIfExist bool, before ...func(op *radosclient.WriteOp)) error {
		return writeErr(ctx, p, head, func(op *radosclient.WriteOp) {
			for _, b := range before {
				b(op)
			}
			rgwcls.ObjCheckAttrsPrefix(op, prefix, failIfExist, denc.Squid)
		})
	}

	// ECANCELED when whether an xattr has the prefix equals fail_if_exist.
	// The walk from the prefix's lower bound stops at the first xattr whose
	// head sorts past the prefix, so only an xattr with the prefix counts
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L2509-L2551 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2677-L2719 at v20.2.4).
	DescribeTable("fails with ECANCELED exactly when an xattr having the prefix is what fail_if_exist names",
		func(ctx SpecContext, xattrs []string, failIfExist, canceled bool) {
			putHead(xattrs...)
			err := check(ctx, pending, failIfExist)
			if canceled {
				Expect(err).To(MatchError(radosclient.ErrCanceled))
			} else {
				Expect(err).NotTo(HaveOccurred())
			}
		},
		Entry("one has it, fail if one exists", []string{"user.rgw.olh.info", pending + "0000000067e3a9c0abcdefgh"}, true, true),
		Entry("one has it, fail if none exists", []string{pending + "0000000067e3a9c0abcdefgh"}, false, false),
		Entry("none has it, fail if one exists", []string{"user.rgw.olh.info"}, true, false),
		Entry("none has it, fail if none exists", []string{"user.rgw.olh.info"}, false, true),
		Entry("only near misses on either side, fail if one exists",
			[]string{"user.rgw.olh.pending", "user.rgw.olh.pendinh", "user.rgw.olh.ver"}, true, false),
		Entry("only near misses on either side, fail if none exists",
			[]string{"user.rgw.olh.pending", "user.rgw.olh.pendinh", "user.rgw.olh.ver"}, false, true),
	)

	// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L2522-L2524 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2690-L2692 at v20.2.4)
	It("refuses an empty prefix with EINVAL", func(ctx SpecContext) {
		putHead(pending + "x")
		Expect(check(ctx, "", true)).To(MatchError(radosclient.ErrInvalid))
		Expect(check(ctx, "", false)).To(MatchError(radosclient.ErrInvalid))
	})

	// cls_cxx_getxattrs reads the store through getattrs_maybe_cache, so an
	// earlier step's xattr is not seen, and the ENOENT of an object that is
	// not there is taken for no xattrs
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L2526-L2531 and
	// https://github.com/ceph/ceph/blob/v19.2.7/src/osd/PrimaryLogPG.cc#L15801-L15820 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2694-L2699 and
	// https://github.com/ceph/ceph/blob/v20.2.4/src/osd/PrimaryLogPG.cc#L16015-L16034 at v20.2.4).
	It("checks the stored xattrs, not those an earlier step sets, and a missing object as having none", func(ctx SpecContext) {
		setPending := func(op *radosclient.WriteOp) { op.SetXattr(pending+"x", []byte("v")) }
		putHead("user.rgw.olh.info")
		Expect(check(ctx, pending, true, setPending)).To(Succeed())
		Expect(c.Object(data, "", head).Xattrs).To(HaveKey(pending + "x"))
		Expect(check(ctx, pending, true)).To(MatchError(radosclient.ErrCanceled), "once stored")

		create := func(op *radosclient.WriteOp) { op.Create(false) }
		c.Remove(data, "", head)
		Expect(check(ctx, pending, true, create)).To(Succeed())
		c.Remove(data, "", head)
		Expect(check(ctx, pending, false, create)).To(MatchError(radosclient.ErrCanceled))
		Expect(c.Object(data, "", head)).To(BeNil())
	})

	// CMPXATTR in string mode reads a missing xattr as empty, so EQ ""
	// holds for it
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/osd/PrimaryLogPG.cc#L6393-L6421 and
	// https://github.com/ceph/ceph/blob/v19.2.7/src/osd/PrimaryLogPG.cc#L4937-L4981 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/osd/PrimaryLogPG.cc#L6462-L6490 and
	// https://github.com/ceph/ceph/blob/v20.2.4/src/osd/PrimaryLogPG.cc#L5005-L5049 at v20.2.4).
	It("matches an absent xattr with a string-mode cmpxattr EQ the empty string", func(ctx SpecContext) {
		putHead()
		cmp := func(op *radosclient.WriteOp) { op.CmpXattr("user.rgw.olh.info", radosclient.CmpEQ, []byte("")) }
		Expect(writeErr(ctx, p, head, cmp)).To(Succeed())
		c.Object(data, "", head).Xattrs["user.rgw.olh.info"] = []byte("v")
		Expect(writeErr(ctx, p, head, cmp)).To(MatchError(radosclient.ErrCanceled))
	})
})
