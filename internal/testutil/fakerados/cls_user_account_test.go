package fakerados_test

import (
	"context"
	"fmt"
	"syscall"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cls/user"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

// squidValue is a value the user class encodes.
type squidValue interface {
	Encode(e *denc.Encoder, r denc.Release)
}

// encodeV encodes v at the Squid release, as UserClass stores it.
func encodeV(v squidValue) []byte {
	e := denc.NewEncoder()
	v.Encode(e, denc.Squid)
	return e.Bytes()
}

var _ = Describe("UserClass account resources", func() {
	const oid = "users.RGW00000000000000001"
	var (
		c *fakerados.Cluster
		p radosclient.Pool
	)
	BeforeEach(func(ctx SpecContext) {
		c, p = newCluster(ctx)
		c.RegisterClass("user", fakerados.UserClass(), fakerados.UserWriteMethods...)
	})
	stored := func() *fakerados.Object { return c.Object(poolName, ns, oid) }
	resource := func(name string) user.AccountResource {
		return user.AccountResource{Name: name, Path: "/", Metadata: encodeV(user.ResourceMetadata{UserID: "id-" + name})}
	}
	add := func(ctx context.Context, r user.AccountResource, exclusive bool, limit uint32) error {
		return writeErr(ctx, p, oid, func(op *radosclient.WriteOp) { user.AccountResourceAdd(op, r, exclusive, limit, denc.Squid) })
	}
	rm := func(ctx context.Context, name string) error {
		return writeErr(ctx, p, oid, func(op *radosclient.WriteOp) { user.AccountResourceRm(op, name, denc.Squid) })
	}
	get := func(ctx context.Context, name string) (user.AccountResource, error) {
		op := radosclient.NewReadOp()
		res := user.AccountResourceGet(op, name, denc.Squid)
		if _, err := p.Read(ctx, oid, op, radosclient.OpFlagNone); err != nil {
			return user.AccountResource{}, err
		}
		return res.Entry()
	}
	type page struct {
		entries   []user.AccountResource
		truncated bool
		marker    string
	}
	list := func(ctx context.Context, marker, prefix string, maxEntries uint32) (page, error) {
		op := radosclient.NewReadOp()
		res := user.AccountResourceList(op, marker, prefix, maxEntries, denc.Squid)
		if _, err := p.Read(ctx, oid, op, radosclient.OpFlagNone); err != nil {
			return page{}, err
		}
		e, t, m, err := res.Result()
		return page{e, t, m}, err
	}
	count := func() uint32 {
		GinkgoHelper()
		d := denc.NewDecoder(stored().OmapHdr)
		h := user.DecodeAccountHeader(d)
		Expect(d.Err()).NotTo(HaveOccurred())
		return h.Count
	}

	It("names add and rm as the account methods that write", func() {
		Expect(fakerados.UserWriteMethods).To(ContainElements("account_resource_add", "account_resource_rm"), "CLS_METHOD_WR, cls_user.cc:755-762")
		Expect(fakerados.UserWriteMethods).NotTo(ContainElements("account_resource_get", "account_resource_list"))
	})

	It("adds under the lower-cased name, counting new entries and overwriting a case variant", func(ctx SpecContext) {
		Expect(add(ctx, resource("Alice"), false, 10)).To(Succeed())
		Expect(stored().Omap).To(HaveKeyWithValue("alice", encodeV(resource("Alice"))))
		Expect(count()).To(Equal(uint32(1)))
		Expect(add(ctx, resource("ALICE"), false, 10)).To(Succeed(), "not exclusive")
		Expect(stored().Omap).To(HaveLen(1))
		Expect(stored().Omap).To(HaveKeyWithValue("alice", encodeV(resource("ALICE"))))
		Expect(count()).To(Equal(uint32(1)), "an overwrite does not count")
	})

	It("refuses an existing name with exclusive and a new name at the limit", func(ctx SpecContext) {
		Expect(add(ctx, resource("a"), false, 2)).To(Succeed())
		Expect(add(ctx, resource("A"), true, 2)).To(MatchError(radosclient.ErrExists))
		Expect(add(ctx, resource("b"), true, 2)).To(Succeed())
		Expect(add(ctx, resource("c"), false, 2)).To(haveErrno(syscall.EUSERS))
		Expect(stored().Omap).To(HaveLen(2))
		Expect(count()).To(Equal(uint32(2)))
		Expect(add(ctx, resource("b"), false, 2)).To(Succeed(), "an existing name is no new entry, whatever the count")
	})

	It("fails an add over a header that does not decode with EIO", func(ctx SpecContext) {
		c.Put(poolName, ns, oid, nil)
		stored().OmapHdr = []byte{9}
		Expect(add(ctx, resource("a"), false, 10)).To(haveErrno(syscall.EIO))
		Expect(stored().Omap).To(BeEmpty())
	})

	It("removes under the lower-cased name, ENOENT for a name or an object it lacks", func(ctx SpecContext) {
		Expect(rm(ctx, "alice")).To(MatchError(radosclient.ErrNotFound), "no object")
		Expect(stored()).To(BeNil(), "a failed op creates nothing")
		Expect(add(ctx, resource("Alice"), false, 10)).To(Succeed())
		Expect(add(ctx, resource("bob"), false, 10)).To(Succeed())
		Expect(rm(ctx, "carol")).To(MatchError(radosclient.ErrNotFound))
		Expect(rm(ctx, "ALICE")).To(Succeed())
		Expect(stored().Omap).NotTo(HaveKey("alice"))
		Expect(count()).To(Equal(uint32(1)))
	})

	It("never counts below zero on a removal", func(ctx SpecContext) {
		c.Put(poolName, ns, oid, nil)
		stored().Omap["x"] = encodeV(resource("x"))
		Expect(rm(ctx, "x")).To(Succeed())
		Expect(count()).To(BeZero())
	})

	It("gets under the lower-cased name, ENOENT for a name or an object it lacks, EIO for an entry that does not decode", func(ctx SpecContext) {
		_, err := get(ctx, "alice")
		Expect(err).To(MatchError(radosclient.ErrNotFound))
		Expect(add(ctx, resource("Alice"), false, 10)).To(Succeed())
		got, err := get(ctx, "aLiCe")
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(resource("Alice")))
		_, err = get(ctx, "bob")
		Expect(err).To(MatchError(radosclient.ErrNotFound))
		stored().Omap["bad"] = []byte{1}
		_, err = get(ctx, "bad")
		Expect(err).To(haveErrno(syscall.EIO))
	})

	It("lists after the raw marker, filters by path, and reports the last raw key read", func(ctx SpecContext) {
		_, err := list(ctx, "", "", 10)
		Expect(err).To(MatchError(radosclient.ErrNotFound), "no object")
		for _, n := range []string{"a", "b", "c", "d"} {
			r := resource(n)
			if n == "b" || n == "d" {
				r.Path = "/eng/"
			}
			Expect(add(ctx, r, false, 10)).To(Succeed())
		}
		pg, err := list(ctx, "", "/eng/", 3)
		Expect(err).NotTo(HaveOccurred())
		Expect(pg.entries).To(HaveExactElements(HaveField("Name", "b")))
		Expect(pg.truncated).To(BeTrue())
		Expect(pg.marker).To(Equal("c"), "the last key read, whose entry did not match")
		pg, err = list(ctx, pg.marker, "/eng/", 3)
		Expect(err).NotTo(HaveOccurred())
		Expect(pg.entries).To(HaveExactElements(HaveField("Name", "d")))
		Expect(pg.truncated).To(BeFalse())
		Expect(pg.marker).To(Equal("d"))
		pg, err = list(ctx, "d", "", 3)
		Expect(err).NotTo(HaveOccurred())
		Expect(pg).To(Equal(page{}), "nothing after the last key: no marker")
	})

	It("lists at most 1000 entries a call", func(ctx SpecContext) {
		c.Put(poolName, ns, oid, nil)
		for i := range 1001 {
			n := fmt.Sprintf("u%04d", i)
			stored().Omap[n] = encodeV(resource(n))
		}
		pg, err := list(ctx, "", "", 5000)
		Expect(err).NotTo(HaveOccurred())
		Expect(pg.entries).To(HaveLen(1000))
		Expect(pg.truncated).To(BeTrue())
		Expect(pg.marker).To(Equal("u0999"))
	})

	It("fails a listing over an entry that does not decode with EIO", func(ctx SpecContext) {
		Expect(add(ctx, resource("a"), false, 10)).To(Succeed())
		stored().Omap["b"] = []byte{1}
		_, err := list(ctx, "", "", 10)
		Expect(err).To(haveErrno(syscall.EIO))
	})
})
