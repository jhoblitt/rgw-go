package fakerados_test

import (
	"context"
	"maps"
	"slices"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	rgwcls "github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

var _ = Describe("RGWClass's listing and suggestions", func() {
	const (
		index = "zone.rgw.buckets.index"
		shard = ".dir.zone.4155.1.0"
	)
	var (
		c   *fakerados.Cluster
		p   radosclient.Pool
		now time.Time
	)
	encode := func(v interface {
		Encode(*denc.Encoder, denc.Release)
	},
	) []byte {
		e := denc.NewEncoder()
		v.Encode(e, denc.Squid)
		return e.Bytes()
	}
	seed := func(en rgwcls.DirEntry) {
		c.Object(index, "", shard).Omap[en.Key.Name] = encode(en)
	}
	entry := func(name string, size uint64) rgwcls.DirEntry {
		en := rgwcls.NewDirEntry()
		en.Key = rgwcls.ObjKey{Name: name}
		en.Exists = true
		en.Meta = rgwcls.DirEntryMeta{Category: rgwcls.CategoryMain, Size: size, AccountedSize: size}
		return en
	}
	BeforeEach(func(ctx SpecContext) {
		c = fakerados.New()
		c.RegisterClass("rgw", fakerados.RGWClass(), fakerados.RGWWriteMethods...)
		now = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
		c.SetClock(func() time.Time { return now })
		var err error
		p, err = c.Pool(ctx, index, "")
		Expect(err).NotTo(HaveOccurred())
		c.Put(index, "", shard, nil)
		c.Object(index, "", shard).OmapHdr = encode(rgwcls.DirHeader{Ver: 3, Stats: map[uint8]rgwcls.CategoryStats{
			rgwcls.CategoryMain: {NumEntries: 1, TotalSize: 5, TotalSizeRounded: 4096, ActualSize: 5},
		}})
	})
	list := func(ctx context.Context, l rgwcls.ListOp) (rgwcls.ListRet, error) {
		rop := radosclient.NewReadOp()
		res := rgwcls.BucketList(rop, l, denc.Squid)
		if _, err := p.Read(ctx, shard, rop, radosclient.OpFlagNone); err != nil {
			return rgwcls.ListRet{}, err
		}
		return res.Result()
	}
	names := func(ret rgwcls.ListRet) []string {
		return slices.Sorted(maps.Keys(ret.Dir.Entries))
	}

	Describe("bucket_list", func() {
		BeforeEach(func() {
			for _, n := range []string{"a", "b", "dir/x", "dir/y", "e"} {
				seed(entry(n, 1))
			}
		})
		It("lists the entries after the start key, truncated at the count with the last key as the marker", func(ctx SpecContext) {
			ret, err := list(ctx, rgwcls.ListOp{StartObj: rgwcls.ObjKey{Name: "a"}, NumEntries: 2})
			Expect(err).NotTo(HaveOccurred())
			Expect(names(ret)).To(Equal([]string{"b", "dir/x"}))
			Expect(ret.IsTruncated).To(BeTrue())
			Expect(ret.Marker).To(Equal(rgwcls.ObjKey{Name: "dir/x"}), "cls_rgw.cc:680-683 at v19.2.6")
			Expect(ret.Dir.Header.Ver).To(BeEquivalentTo(3))
			ret, err = list(ctx, rgwcls.ListOp{StartObj: rgwcls.ObjKey{Name: "dir/x"}, NumEntries: 10})
			Expect(err).NotTo(HaveOccurred())
			Expect(names(ret)).To(Equal([]string{"dir/y", "e"}))
			Expect(ret.IsTruncated).To(BeFalse())
		})
		It("rolls names up to one common-prefix entry per delimiter and skips past the subdirectory", func(ctx SpecContext) {
			ret, err := list(ctx, rgwcls.ListOp{NumEntries: 10, Delimiter: "/"})
			Expect(err).NotTo(HaveOccurred())
			Expect(names(ret)).To(Equal([]string{"a", "b", "dir/", "e"}))
			Expect(ret.Dir.Entries["dir/"].Flags).To(Equal(rgwcls.FlagCommonPrefix), "cls_rgw.cc:639-645")
			Expect(ret.Dir.Entries["dir/"].Key).To(Equal(rgwcls.ObjKey{Name: "dir/"}))
			ret, err = list(ctx, rgwcls.ListOp{StartObj: rgwcls.ObjKey{Name: "dir/"}, NumEntries: 10, Delimiter: "/"})
			Expect(err).NotTo(HaveOccurred())
			Expect(names(ret)).To(Equal([]string{"e"}), "a start key ending in the delimiter skips the subdirectory, :548-554")
		})
		It("filters by prefix", func(ctx SpecContext) {
			ret, err := list(ctx, rgwcls.ListOp{NumEntries: 10, FilterPrefix: "dir/"})
			Expect(err).NotTo(HaveOccurred())
			Expect(names(ret)).To(Equal([]string{"dir/x", "dir/y"}))
		})
		It("skips entries that are not visible unless versions are listed", func(ctx SpecContext) {
			dm := entry("c", 0)
			dm.Flags = rgwcls.FlagDeleteMarker
			seed(dm)
			ret, err := list(ctx, rgwcls.ListOp{StartObj: rgwcls.ObjKey{Name: "b"}, NumEntries: 2})
			Expect(err).NotTo(HaveOccurred())
			Expect(names(ret)).To(Equal([]string{"dir/x", "dir/y"}))
			ret, err = list(ctx, rgwcls.ListOp{StartObj: rgwcls.ObjKey{Name: "b"}, NumEntries: 2, ListVersions: true})
			Expect(err).NotTo(HaveOccurred())
			Expect(names(ret)).To(Equal([]string{"c", "dir/x"}))
		})
		It("starts after every version of a start key that names an instance", func(ctx SpecContext) {
			ret, err := list(ctx, rgwcls.ListOp{StartObj: rgwcls.ObjKey{Name: "a", Instance: "null"}, NumEntries: 1})
			Expect(err).NotTo(HaveOccurred())
			Expect(names(ret)).To(Equal([]string{"b"}), "encode_list_index_key finds no instance entry, :375-381")
		})
	})

	Describe("dir_suggest_changes", func() {
		suggest := func(ctx context.Context, changes ...rgwcls.Suggestion) error {
			return writeErr(ctx, p, shard, func(op *radosclient.WriteOp) { rgwcls.SuggestChanges(op, changes, denc.Squid) })
		}
		It("is registered as a write", func() {
			Expect(fakerados.RGWWriteMethods).To(ContainElement("dir_suggest_changes"))
		})
		It("removes an entry and unaccounts it", func(ctx SpecContext) {
			seed(entry("gone", 5))
			Expect(suggest(ctx, rgwcls.Suggestion{Op: rgwcls.SuggestRemove, Entry: entry("gone", 5)})).To(Succeed())
			_, found := c.Entry(index, "", shard, "gone")
			Expect(found).To(BeFalse())
			h := c.Header(index, "", shard)
			Expect(h.Stats[rgwcls.CategoryMain]).To(Equal(rgwcls.CategoryStats{}))
			Expect(h.Ver).To(BeEquivalentTo(4))
			Expect(c.Suggestions(index, "", shard)).To(ConsistOf(HaveField("Op", rgwcls.SuggestRemove)))
		})
		It("updates an entry and accounts the suggested size", func(ctx SpecContext) {
			seed(entry("k", 5))
			upd := entry("k", 7)
			upd.Meta.ETag = "fresh"
			Expect(suggest(ctx, rgwcls.Suggestion{Op: rgwcls.SuggestUpdate, Log: true, Entry: upd})).To(Succeed())
			en, found := c.Entry(index, "", shard, "k")
			Expect(found).To(BeTrue())
			Expect(en.Meta.ETag).To(Equal("fresh"))
			Expect(en.IndexVer).To(BeEquivalentTo(3), "cur_change.index_ver = header.ver before the header write, cls_rgw.cc:2367")
			Expect(c.Header(index, "", shard).Stats[rgwcls.CategoryMain].TotalSize).To(BeEquivalentTo(7))
			Expect(c.Suggestions(index, "", shard)).To(ConsistOf(And(HaveField("Op", rgwcls.SuggestUpdate), HaveField("Log", true))))
		})
		It("leaves an entry with an unexpired pending op and one it cannot find", func(ctx SpecContext) {
			pending := entry("p", 5)
			pending.PendingMap = []rgwcls.PendingEntry{{Tag: "t", Info: rgwcls.PendingInfo{Timestamp: now.Add(-time.Minute)}}}
			seed(pending)
			Expect(suggest(ctx,
				rgwcls.Suggestion{Op: rgwcls.SuggestRemove, Entry: entry("p", 5)},
				rgwcls.Suggestion{Op: rgwcls.SuggestRemove, Entry: entry("absent", 5)},
			)).To(Succeed())
			_, found := c.Entry(index, "", shard, "p")
			Expect(found).To(BeTrue(), "a pending op younger than the 120 s tag timeout blocks it, cls_rgw.cc:2283-2316")
			Expect(c.Header(index, "", shard).Ver).To(BeEquivalentTo(3))
			Expect(c.Suggestions(index, "", shard)).To(HaveLen(2))
		})
		It("drops an expired pending op and applies the change", func(ctx SpecContext) {
			pending := entry("p", 5)
			pending.PendingMap = []rgwcls.PendingEntry{{Tag: "t", Info: rgwcls.PendingInfo{Timestamp: now.Add(-3 * time.Minute)}}}
			seed(pending)
			Expect(suggest(ctx, rgwcls.Suggestion{Op: rgwcls.SuggestRemove, Entry: entry("p", 5)})).To(Succeed())
			_, found := c.Entry(index, "", shard, "p")
			Expect(found).To(BeFalse())
		})
	})
})
