package fakerados_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

// The OSD reads the stored omap for an omap comparison and a class's map
// reads only while the object carries FLAG_OMAP, as CEPH_OSD_OP_OMAPGETVALSBYKEYS
// does
// (https://github.com/ceph/ceph/blob/v19.2.7/src/osd/PrimaryLogPG.cc#L7779-L7800 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/osd/PrimaryLogPG.cc#L7856-L7877 at v20.2.4).
// CEPH_OSD_OP_OMAPSETVALS and CEPH_OSD_OP_OMAPSETHEADER set the flag and
// CEPH_OSD_OP_OMAPCLEAR clears it
// (https://github.com/ceph/ceph/blob/v19.2.7/src/osd/PrimaryLogPG.cc#L7878-L7957 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/osd/PrimaryLogPG.cc#L7955-L8034 at v20.2.4).
// A class's own cls_cxx_map_set_val and cls_cxx_map_write_header send those
// ops
// (https://github.com/ceph/ceph/blob/v19.2.7/src/osd/objclass.cc#L505-L556 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/osd/objclass.cc#L505-L556 at v20.2.4).
var _ = Describe("the stored omap an op's steps read after an OmapClear", func() {
	var (
		c *fakerados.Cluster
		p radosclient.Pool
	)
	BeforeEach(func(ctx SpecContext) {
		c, p = newCluster(ctx)
		c.Put(poolName, ns, "obj", nil)
		c.Object(poolName, ns, "obj").Omap["k"] = []byte("v")
		c.Object(poolName, ns, "obj").OmapHdr = []byte("h")
		// seen records the stored omap value and header the call read, as
		// xattrs of the op's object. writekey and writehdr record what
		// StoredOmap serves before and after the call writes a key or the
		// omap header.
		c.RegisterClass("probe", func(call *fakerados.ClassCall) ([]byte, int32) {
			o := call.Create()
			if call.Method == "seen" {
				o.Xattrs["seen.k"] = append([]byte("="), call.Stored.Omap["k"]...)
				o.Xattrs["seen.hdr"] = append([]byte("="), call.Stored.OmapHdr...)
				return nil, 0
			}
			omap, _ := call.StoredOmap()
			o.Xattrs["before.k"] = append([]byte("="), omap["k"]...)
			if call.Method == "writekey" {
				o.Omap["new"] = []byte("n")
			} else {
				o.OmapHdr = []byte("H")
			}
			omap, hdr := call.StoredOmap()
			o.Xattrs["after.k"] = append([]byte("="), omap["k"]...)
			o.Xattrs["after.hdr"] = append([]byte("="), hdr...)
			return nil, 0
		}, "seen", "writekey", "writehdr")
	})
	seen := func() map[string][]byte {
		xattrs := c.Object(poolName, ns, "obj").Xattrs
		return map[string][]byte{"k": xattrs["seen.k"], "hdr": xattrs["seen.hdr"]}
	}

	It("hands a class the stored omap and header across a remove and a create", func(ctx SpecContext) {
		Expect(writeErr(ctx, p, "obj", func(op *radosclient.WriteOp) {
			op.Remove()
			op.Create(false)
			op.Exec("probe", "seen", nil)
		})).To(Succeed())
		Expect(seen()).To(Equal(map[string][]byte{"k": []byte("=v"), "hdr": []byte("=h")}), "a remove leaves FLAG_OMAP set")
	})

	It("hands a class no stored omap or header after an OmapClear", func(ctx SpecContext) {
		Expect(writeErr(ctx, p, "obj", func(op *radosclient.WriteOp) {
			op.OmapClear()
			op.Exec("probe", "seen", nil)
		})).To(Succeed())
		Expect(seen()).To(Equal(map[string][]byte{"k": []byte("="), "hdr": []byte("=")}))
	})

	It("hands a class the stored omap again once an OmapSet follows the OmapClear", func(ctx SpecContext) {
		Expect(writeErr(ctx, p, "obj", func(op *radosclient.WriteOp) {
			op.OmapClear()
			op.OmapSet(map[string][]byte{"other": []byte("x")})
			op.Exec("probe", "seen", nil)
		})).To(Succeed())
		Expect(seen()).To(Equal(map[string][]byte{"k": []byte("=v"), "hdr": []byte("=h")}),
			"OMAPSETVALS sets FLAG_OMAP, and the read is the store's, from before the op")
	})

	DescribeTable("hands a class the stored omap again within the call once it writes the omap after an OmapClear",
		func(ctx SpecContext, method string) {
			Expect(writeErr(ctx, p, "obj", func(op *radosclient.WriteOp) {
				op.OmapClear()
				op.Exec("probe", method, nil)
			})).To(Succeed())
			xattrs := c.Object(poolName, ns, "obj").Xattrs
			Expect(map[string]string{"before.k": string(xattrs["before.k"]), "after.k": string(xattrs["after.k"]), "after.hdr": string(xattrs["after.hdr"])}).
				To(Equal(map[string]string{"before.k": "=", "after.k": "=v", "after.hdr": "=h"}))
		},
		Entry("a key, as cls_cxx_map_set_val sends OMAPSETVALS", "writekey"),
		Entry("the omap header, as cls_cxx_map_write_header sends OMAPSETHEADER", "writehdr"),
	)

	It("keeps the stored omap in reach for the op's later steps once a call has written the omap", func(ctx SpecContext) {
		Expect(writeErr(ctx, p, "obj", func(op *radosclient.WriteOp) {
			op.OmapClear()
			op.Exec("probe", "writekey", nil)
			op.Exec("probe", "seen", nil)
			op.OmapCmp("k", radosclient.CmpEQ, []byte("v"))
		})).To(Succeed(), "OMAP_CMP compares the stored value again")
		Expect(seen()).To(Equal(map[string][]byte{"k": []byte("=v"), "hdr": []byte("=h")}))
	})

	It("compares OMAP_CMP against no stored value after an OmapClear", func(ctx SpecContext) {
		cmp := func(clearFirst bool, value string) error {
			return writeErr(ctx, p, "obj", func(op *radosclient.WriteOp) {
				if clearFirst {
					op.OmapClear()
				}
				op.OmapCmp("k", radosclient.CmpEQ, []byte(value))
			})
		}
		Expect(cmp(false, "v")).To(Succeed())
		Expect(cmp(true, "v")).To(MatchError(radosclient.ErrCanceled))
		Expect(cmp(true, "")).To(Succeed(), "a missing key compares as empty")
	})
})
