package fakerados_test

import (
	"context"
	"maps"
	"slices"
	"strconv"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	rgwcls "github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

// olhShard is a bucket index shard holding the name "k" and the helpers the
// OLH, bi_get and attrs specs share. Each spec builds its own in BeforeEach.
type olhShard struct {
	c     *fakerados.Cluster
	p     radosclient.Pool
	mtime time.Time
}

const (
	olhIndex = "zone.rgw.buckets.index"
	olhOID   = ".dir.zone.4155.1.0"
	olhTag   = "olh-tag"
)

func newOLHShard(ctx context.Context) *olhShard {
	GinkgoHelper()
	s := &olhShard{c: fakerados.New(), mtime: time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)}
	s.c.RegisterClassMethods("rgw", fakerados.RGWClass(), fakerados.RGWMethods, fakerados.RGWWriteMethods)
	var err error
	s.p, err = s.c.Pool(ctx, olhIndex, "")
	Expect(err).NotTo(HaveOccurred())
	s.c.Put(olhIndex, "", olhOID, nil)
	return s
}

func (s *olhShard) omap() map[string][]byte { return s.c.Object(olhIndex, "", olhOID).Omap }

func (s *olhShard) keys() []string { return slices.Sorted(maps.Keys(s.omap())) }

// vkey is the name "k" with instance id.
func vkey(id string) rgwcls.ObjKey { return rgwcls.ObjKey{Name: "k", Instance: id} }

// version is the entry a versioned write's index complete leaves for id: an
// existing object flagged as a version, with no versioned epoch yet.
func (s *olhShard) version(id string) rgwcls.DirEntry {
	en := rgwcls.NewDirEntry()
	en.Key = vkey(id)
	en.Ver = rgwcls.EntryVer{Pool: 7, Epoch: 11}
	en.Exists = true
	en.Meta = rgwcls.DirEntryMeta{Category: rgwcls.CategoryMain, Size: 5, AccountedSize: 5, ETag: "etag-" + id, Mtime: s.mtime}
	en.Tag = "write-" + id
	en.Flags = rgwcls.FlagVer
	return en
}

// plain is the entry an unversioned write leaves under the plain key.
func (s *olhShard) plain() rgwcls.DirEntry {
	en := s.version("")
	en.Tag, en.Flags = "write-plain", 0
	return en
}

// stage stores en under the index key its instance takes.
func (s *olhShard) stage(en rgwcls.DirEntry) {
	s.omap()[rgwcls.InstanceIndexKey(en.Key, false)] = encodeV(en)
}

func (s *olhShard) stageOLH(en rgwcls.OLHEntry) { s.omap()[rgwcls.OLHDataKey("k")] = encodeV(en) }

func (s *olhShard) entry(k string) rgwcls.DirEntry {
	GinkgoHelper()
	en, ok := s.c.Entry(olhIndex, "", olhOID, k)
	Expect(ok).To(BeTrue(), "index key %q", k)
	return en
}

func (s *olhShard) olh() rgwcls.OLHEntry {
	GinkgoHelper()
	b, ok := s.omap()[rgwcls.OLHDataKey("k")]
	Expect(ok).To(BeTrue(), "the OLH entry of k")
	d := denc.NewDecoder(b)
	en := rgwcls.DecodeOLHEntry(d)
	Expect(d.Err()).NotTo(HaveOccurred())
	return en
}

func (s *olhShard) write(ctx context.Context, build func(op *radosclient.WriteOp)) error {
	return writeErr(ctx, s.p, olhOID, func(op *radosclient.WriteOp) {
		op.AssertExists()
		build(op)
	})
}

func (s *olhShard) link(ctx context.Context, l rgwcls.LinkOLHOp) error {
	return s.write(ctx, func(op *radosclient.WriteOp) { rgwcls.LinkOLH(op, l, denc.Squid) })
}

func (s *olhShard) mustLink(ctx context.Context, l rgwcls.LinkOLHOp) {
	GinkgoHelper()
	Expect(s.link(ctx, l)).To(Succeed())
}

func (s *olhShard) unlink(ctx context.Context, u rgwcls.UnlinkInstanceOp) error {
	return s.write(ctx, func(op *radosclient.WriteOp) { rgwcls.UnlinkInstance(op, u, denc.Squid) })
}

func (s *olhShard) readLog(ctx context.Context, q rgwcls.ReadOLHLogOp) (rgwcls.ReadOLHLogRet, error) {
	r := radosclient.NewReadOp()
	res := rgwcls.ReadOLHLog(r, q, denc.Squid)
	if _, err := s.p.Read(ctx, olhOID, r, radosclient.OpFlagNone); err != nil {
		return rgwcls.ReadOLHLogRet{}, err
	}
	return res.Result()
}

// linkOp links id under tag olhTag, with an op tag naming it.
func linkOp(id string) rgwcls.LinkOLHOp {
	return rgwcls.LinkOLHOp{Key: vkey(id), OLHTag: olhTag, OpTag: "op-" + id}
}

// markerOp links a delete marker for id, owned by o.
func markerOp(id string) rgwcls.LinkOLHOp {
	l := linkOp(id)
	l.DeleteMarker, l.Meta = true, rgwcls.DirEntryMeta{Owner: "o", OwnerDisplayName: "O"}
	return l
}

func unlinkOp(id string) rgwcls.UnlinkInstanceOp {
	return rgwcls.UnlinkInstanceOp{Key: vkey(id), OLHTag: olhTag, OpTag: "op-u" + id}
}

func atEpoch(l rgwcls.LinkOLHOp, epoch uint64) rgwcls.LinkOLHOp {
	l.OLHEpoch = epoch
	return l
}

// asVersion is en with the flags and versioned epoch the class gives it.
func asVersion(en rgwcls.DirEntry, flags uint16, epoch uint64) rgwcls.DirEntry {
	en.Flags, en.VersionedEpoch = flags, epoch
	return en
}

// asMarker is en as the class turns it into a delete marker: the request's
// meta, the delete-marker tag, and the flags and epoch given.
func asMarker(en rgwcls.DirEntry, flags uint16, epoch uint64) rgwcls.DirEntry {
	en = asVersion(en, flags|rgwcls.FlagDeleteMarker, epoch)
	en.Meta, en.Tag = markerOp("").Meta, "delete-marker"
	return en
}

func olhLogged(epoch uint64, op rgwcls.OLHLogOp, opTag string, k rgwcls.ObjKey, deleteMarker bool) rgwcls.OLHLogEntry {
	return rgwcls.OLHLogEntry{Epoch: epoch, Op: op, OpTag: opTag, Key: k, DeleteMarker: deleteMarker}
}

// versionMarker is the entry the class writes under the plain key of a name
// it versions.
func versionMarker() rgwcls.DirEntry {
	en := rgwcls.NewDirEntry()
	en.Key, en.Flags = vkey(""), rgwcls.FlagVerMarker
	return en
}

const (
	flagsCurrent    = rgwcls.FlagVer | rgwcls.FlagCurrent
	flagsNoncurrent = rgwcls.FlagVer
)

// verInstKey is the instance key of k's version id.
func verInstKey(id string) string { return rgwcls.InstanceIndexKey(vkey(id), false) }

// verListKey is the list key of k's version id at epoch.
func verListKey(id string, epoch uint64) string { return rgwcls.ListIndexKey(vkey(id), epoch) }

// nullMarkerKey is the instance key of k's null delete marker.
func nullMarkerKey() string { return rgwcls.InstanceIndexKey(vkey(""), true) }

var _ = Describe("RGWClass's OLH methods", func() {
	var s *olhShard
	BeforeEach(func(ctx SpecContext) { s = newOLHShard(ctx) })

	// The class registers bucket_read_olh_log, obj_check_attrs_prefix and
	// bi_get with CLS_METHOD_RD alone, and the other OLH methods RD|WR
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L4699-L4710 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L5116-L5127 at v20.2.4).
	It("registers bucket_read_olh_log, obj_check_attrs_prefix and bi_get as reads and the other OLH methods as writes", func() {
		methods := []string{
			"bucket_link_olh", "bucket_unlink_instance", "bucket_read_olh_log", "bucket_trim_olh_log",
			"bucket_clear_olh", "obj_check_attrs_prefix", "bi_get",
		}
		Expect(fakerados.RGWMethods).To(ContainElements(methods))
		writes := slices.DeleteFunc(slices.Clone(fakerados.RGWWriteMethods), func(m string) bool { return !slices.Contains(methods, m) })
		Expect(writes).To(ConsistOf("bucket_link_olh", "bucket_unlink_instance", "bucket_trim_olh_log", "bucket_clear_olh"))
	})

	Describe("bucket_link_olh", func() {
		BeforeEach(func() {
			for _, id := range []string{"A", "B", "C"} {
				s.stage(s.version(id))
			}
		})

		// With no OLH entry the class versions the name: a version marker
		// in the plain slot (convert_plain_entry_to_versioned and
		// write_version_marker), the first epoch 2 (start_modify), the OLH
		// entry with its log, then the instance and its list entry, current
		// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1587-L1634,
		// https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1503-L1517 and
		// https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1792-L1827 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1715-L1763,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1630-L1644 and
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1933-L1968 at v20.2.4).
		It("links a version under a new OLH entry at epoch 2 and writes a version marker under the plain key", func(ctx SpecContext) {
			s.mustLink(ctx, linkOp("A"))
			Expect(s.keys()).To(ConsistOf("k", verListKey("A", 2), verInstKey("A"), verInstKey("B"), verInstKey("C"), rgwcls.OLHDataKey("k")))
			Expect(verListKey("A", 2)).To(Equal("k\x00v913\x00iA"))
			Expect(s.entry(verInstKey("A"))).To(Equal(asVersion(s.version("A"), flagsCurrent, 2)))
			Expect(s.entry(verListKey("A", 2))).To(Equal(asVersion(s.version("A"), flagsCurrent, 2)))
			Expect(s.entry("k")).To(Equal(versionMarker()))
			Expect(s.olh()).To(Equal(rgwcls.OLHEntry{
				Key: vkey("A"), Epoch: 2, Tag: olhTag, Exists: true,
				PendingLog: map[uint64][]rgwcls.OLHLogEntry{2: {olhLogged(2, rgwcls.OLHOpLinkOLH, "op-A", vkey("A"), false)}},
			}))
		})

		// A newer epoch promotes, and the version it replaces is demoted
		// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1762-L1791 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1903-L1932 at v20.2.4)
		// through write_entries, which reads its stored entry back, epoch
		// included
		// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1388-L1431 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1514-L1558 at v20.2.4).
		It("links a newer version current at the next epoch and demotes the one it replaces, which keeps its epoch", func(ctx SpecContext) {
			s.mustLink(ctx, linkOp("A"))
			s.mustLink(ctx, linkOp("B"))
			Expect(s.entry(verInstKey("A"))).To(Equal(asVersion(s.version("A"), flagsNoncurrent, 2)))
			Expect(s.entry(verListKey("A", 2))).To(Equal(asVersion(s.version("A"), flagsNoncurrent, 2)))
			Expect(s.entry(verInstKey("B"))).To(Equal(asVersion(s.version("B"), flagsCurrent, 3)))
			Expect(s.entry(verListKey("B", 3))).To(Equal(asVersion(s.version("B"), flagsCurrent, 3)))
			Expect(s.olh()).To(Equal(rgwcls.OLHEntry{
				Key: vkey("B"), Epoch: 3, Tag: olhTag, Exists: true,
				PendingLog: map[uint64][]rgwcls.OLHLogEntry{
					2: {olhLogged(2, rgwcls.OLHOpLinkOLH, "op-A", vkey("A"), false)},
					3: {olhLogged(3, rgwcls.OLHOpLinkOLH, "op-B", vkey("B"), false)},
				},
			}))
		})

		// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1768-L1777 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1909-L1918 at v20.2.4)
		It("refuses another OLH tag with ECANCELED, changing nothing", func(ctx SpecContext) {
			s.mustLink(ctx, linkOp("A"))
			before := maps.Clone(s.omap())
			l := linkOp("B")
			l.OLHTag = "other"
			Expect(s.link(ctx, l)).To(MatchError(radosclient.ErrCanceled), "cls_rgw.cc:1768-1777 at v19.2.7")
			Expect(s.omap()).To(Equal(before))
		})

		// A pending removal means a new OLH instance, so the entry takes the
		// request's tag (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1768-L1791 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1909-L1932 at v20.2.4).
		It("takes another OLH tag on an entry pending removal, which it clears, demoting nothing", func(ctx SpecContext) {
			s.mustLink(ctx, linkOp("A"))
			Expect(s.unlink(ctx, unlinkOp("A"))).To(Succeed())
			l := linkOp("B")
			l.OLHTag = "new-tag"
			s.mustLink(ctx, l)
			o := s.olh()
			Expect(o.Tag).To(Equal("new-tag"))
			Expect(o.PendingRemoval).To(BeFalse())
			Expect(o.Exists).To(BeTrue())
			Expect(o.Key).To(Equal(vkey("B")))
			Expect(o.Epoch).To(BeEquivalentTo(4))
			Expect(o.PendingLog[4]).To(Equal([]rgwcls.OLHLogEntry{olhLogged(4, rgwcls.OLHOpLinkOLH, "op-B", vkey("B"), false)}))
			Expect(s.entry(verInstKey("A"))).To(Equal(asVersion(s.version("A"), flagsCurrent, 2)), "an OLH entry that does not exist demotes nothing")
			Expect(s.entry(verInstKey("B"))).To(Equal(asVersion(s.version("B"), flagsCurrent, 4)))
		})

		// A named epoch below the entry's writes the version noncurrent and
		// returns before the OLH entry is touched
		// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1503-L1517 and
		// https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1751-L1760 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1630-L1644 and
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1892-L1901 at v20.2.4).
		It("writes a version named at an older epoch noncurrent at that epoch and leaves the OLH entry", func(ctx SpecContext) {
			s.mustLink(ctx, atEpoch(linkOp("A"), 5))
			before := s.omap()[rgwcls.OLHDataKey("k")]
			s.mustLink(ctx, atEpoch(linkOp("B"), 3))
			Expect(s.entry(verInstKey("B"))).To(Equal(asVersion(s.version("B"), flagsNoncurrent, 3)))
			Expect(s.entry(verListKey("B", 3))).To(Equal(asVersion(s.version("B"), flagsNoncurrent, 3)))
			Expect(s.entry(verListKey("A", 5))).To(Equal(asVersion(s.version("A"), flagsCurrent, 5)))
			Expect(s.omap()[rgwcls.OLHDataKey("k")]).To(Equal(before))
		})

		// The same branch logs the REMOVE_INSTANCE a delete marker over an
		// existing version calls for, then returns without writing the OLH
		// entry it logged it in, so the log entry is lost and radosgw never
		// removes the version (docs/ceph-upstream-bugs.md)
		// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1732-L1760 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1873-L1901 at v20.2.4).
		It("drops the REMOVE_INSTANCE it logs for a delete marker over an existing version at an older epoch", func(ctx SpecContext) {
			s.mustLink(ctx, atEpoch(linkOp("A"), 5))
			before := s.omap()[rgwcls.OLHDataKey("k")]
			s.mustLink(ctx, atEpoch(markerOp("C"), 3))
			Expect(s.entry(verInstKey("C"))).To(Equal(asMarker(s.version("C"), flagsNoncurrent, 3)))
			Expect(s.omap()[rgwcls.OLHDataKey("k")]).To(Equal(before), "no OLH entry is written, so no REMOVE_INSTANCE for C")
		})

		// For the null version the branch first unlinks the null data's list
		// entry, whatever its epoch, so a current null data newer than the
		// marker leaves the listings while the OLH entry still names it
		// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1712-L1760 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1853-L1901 at v20.2.4).
		It("unlinks a newer current null version's list entry for a null delete marker at an older epoch", func(ctx SpecContext) {
			s.mustLink(ctx, atEpoch(linkOp("A"), 5))
			s.stage(s.version(""))
			s.mustLink(ctx, atEpoch(linkOp(""), 6))
			before := s.omap()[rgwcls.OLHDataKey("k")]
			s.mustLink(ctx, atEpoch(markerOp(""), 3))
			Expect(s.keys()).NotTo(ContainElement(verListKey("", 6)))
			Expect(s.entry(verInstKey(""))).To(Equal(asVersion(s.version(""), flagsCurrent, 6)), "the data entry stays, flagged current")
			Expect(s.entry(nullMarkerKey())).To(Equal(asMarker(s.version(""), flagsNoncurrent, 3)))
			Expect(s.entry(verListKey("", 3))).To(Equal(asMarker(s.version(""), flagsNoncurrent, 3)))
			Expect(s.omap()[rgwcls.OLHDataKey("k")]).To(Equal(before), "the OLH entry still names the null data, and logs no removal")
		})

		// A delete marker over an existing version of its instance replaces it
		// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1732-L1738 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1873-L1879 at v20.2.4),
		// and at a newer epoch the marker logs LINK_OLH, then REMOVE_INSTANCE
		// for the version it replaces
		// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1806-L1815 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1947-L1956 at v20.2.4).
		It("logs LINK_OLH and REMOVE_INSTANCE for a delete marker over an existing version", func(ctx SpecContext) {
			s.mustLink(ctx, linkOp("A"))
			s.mustLink(ctx, markerOp("C"))
			Expect(s.entry(verInstKey("C"))).To(Equal(asMarker(s.version("C"), flagsCurrent, 3)))
			Expect(s.entry(verListKey("C", 3))).To(Equal(asMarker(s.version("C"), flagsCurrent, 3)))
			o := s.olh()
			Expect(o.Key).To(Equal(vkey("C")))
			Expect(o.DeleteMarker).To(BeTrue())
			Expect(o.PendingLog[3]).To(Equal([]rgwcls.OLHLogEntry{
				olhLogged(3, rgwcls.OLHOpLinkOLH, "op-C", vkey("C"), true),
				olhLogged(3, rgwcls.OLHOpRemoveInstance, "op-C", vkey("C"), false),
			}))
		})

		// Only a delete marker may name an instance with no entry; the class
		// creates its entry tagged "delete-marker"
		// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1668-L1688 and
		// https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1350-L1358 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1809-L1829 and
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1476-L1484 at v20.2.4).
		It("creates a delete marker's entry for an instance without one, and refuses a version without one with ENOENT", func(ctx SpecContext) {
			s.mustLink(ctx, linkOp("A"))
			before := maps.Clone(s.omap())
			Expect(s.link(ctx, linkOp("E"))).To(MatchError(radosclient.ErrNotFound), "cls_rgw.cc:1683-1688 at v19.2.7")
			Expect(s.omap()).To(Equal(before))

			s.mustLink(ctx, markerOp("D"))
			dm := rgwcls.NewDirEntry()
			dm.Key = vkey("D")
			dm = asMarker(dm, flagsCurrent, 3)
			Expect(s.entry(verInstKey("D"))).To(Equal(dm))
			Expect(s.entry(verListKey("D", 3))).To(Equal(dm))
			Expect(s.olh().PendingLog[3]).To(Equal([]rgwcls.OLHLogEntry{olhLogged(3, rgwcls.OLHOpLinkOLH, "op-D", vkey("D"), true)}),
				"a marker that replaces no version removes none")
		})

		// The instance entry is read before the OLH entry, so a missing
		// version is ENOENT whatever tag the request carries, where a driver
		// retrying on ECANCELED would retry
		// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1668-L1688 and
		// https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1768-L1777 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1809-L1829 and
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1909-L1918 at v20.2.4).
		It("refuses a version without an entry with ENOENT before it compares the OLH tag", func(ctx SpecContext) {
			s.mustLink(ctx, linkOp("A"))
			before := maps.Clone(s.omap())
			l := linkOp("E")
			l.OLHTag = "other"
			Expect(s.link(ctx, l)).To(MatchError(radosclient.ErrNotFound))
			Expect(s.omap()).To(Equal(before))
		})

		// At an equal epoch the class promotes when the current instance
		// sorts at or after the request's, and logs the link either way
		// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1762-L1815 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1903-L1956 at v20.2.4).
		It("promotes at an equal epoch only an instance sorting at or before the current one", func(ctx SpecContext) {
			for _, id := range []string{"m", "a", "z"} {
				s.stage(s.version(id))
			}
			s.mustLink(ctx, atEpoch(linkOp("m"), 5))
			s.mustLink(ctx, atEpoch(linkOp("z"), 5))
			Expect(s.olh().Key).To(Equal(vkey("m")))
			Expect(s.entry(verInstKey("z"))).To(Equal(asVersion(s.version("z"), flagsNoncurrent, 5)))
			Expect(s.entry(verInstKey("m"))).To(Equal(asVersion(s.version("m"), flagsCurrent, 5)))

			s.mustLink(ctx, atEpoch(linkOp("a"), 5))
			Expect(s.olh().Key).To(Equal(vkey("a")))
			Expect(s.entry(verInstKey("a"))).To(Equal(asVersion(s.version("a"), flagsCurrent, 5)))
			Expect(s.entry(verInstKey("m"))).To(Equal(asVersion(s.version("m"), flagsNoncurrent, 5)))
			Expect(s.olh().PendingLog[5]).To(Equal([]rgwcls.OLHLogEntry{
				olhLogged(5, rgwcls.OLHOpLinkOLH, "op-m", vkey("m"), false),
				olhLogged(5, rgwcls.OLHOpLinkOLH, "op-z", vkey("z"), false),
				olhLogged(5, rgwcls.OLHOpLinkOLH, "op-a", vkey("a"), false),
			}))
		})

		// The tie-break is greater or equal, so the null delete marker linked
		// over the current null data at its epoch, an equal instance, is
		// promoted (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1762-L1815 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1903-L1956 at v20.2.4).
		It("promotes a null delete marker over the current null data at the same epoch", func(ctx SpecContext) {
			s.mustLink(ctx, atEpoch(linkOp("A"), 4))
			s.stage(s.version(""))
			s.mustLink(ctx, atEpoch(linkOp(""), 5))
			s.mustLink(ctx, atEpoch(markerOp(""), 5))
			o := s.olh()
			Expect(o.Key).To(Equal(vkey("")))
			Expect(o.DeleteMarker).To(BeTrue())
			Expect(o.Epoch).To(BeEquivalentTo(5))
			dm := asMarker(s.version(""), flagsCurrent, 5)
			Expect(s.entry(nullMarkerKey())).To(Equal(dm))
			Expect(s.entry(verListKey("", 5))).To(Equal(dm), "the marker takes the data's list key")
			Expect(s.entry(verInstKey(""))).To(Equal(asVersion(s.version(""), flagsCurrent, 5)), "the data entry stays for radosgw's REMOVE_INSTANCE")
			Expect(o.PendingLog[5]).To(Equal([]rgwcls.OLHLogEntry{
				olhLogged(5, rgwcls.OLHOpLinkOLH, "op-", vkey(""), false),
				olhLogged(5, rgwcls.OLHOpLinkOLH, "op-", vkey(""), true),
				olhLogged(5, rgwcls.OLHOpRemoveInstance, "op-", vkey(""), false),
			}))
		})

		// unmod_since: an existing instance no older than it is left alone
		// and the call answers 0; whole seconds unless high_precision_time
		// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1690-L1701 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1831-L1842 at v20.2.4).
		It("answers 0 and changes nothing for an instance no older than unmod_since, in whole seconds unless asked for more", func(ctx SpecContext) {
			before := maps.Clone(s.omap())
			l := linkOp("A")
			l.UnmodSince = s.mtime
			Expect(s.link(ctx, l)).To(Succeed())
			Expect(s.omap()).To(Equal(before), "an equal mtime")

			b := s.version("B")
			b.Meta.Mtime = s.mtime.Add(100 * time.Nanosecond)
			s.stage(b)
			before = maps.Clone(s.omap())
			l = linkOp("B")
			l.UnmodSince = s.mtime.Add(200 * time.Nanosecond)
			Expect(s.link(ctx, l)).To(Succeed())
			Expect(s.omap()).To(Equal(before), "the same second")
			l.HighPrecisionTime = true
			Expect(s.link(ctx, l)).To(Succeed())
			Expect(s.olh().Key).To(Equal(vkey("B")), "100ns is older than 200ns")

			l = linkOp("A")
			l.UnmodSince = s.mtime.Add(time.Second)
			Expect(s.link(ctx, l)).To(Succeed())
			Expect(s.olh().Key).To(Equal(vkey("A")), "a second older")

			l = markerOp("D")
			l.UnmodSince = s.mtime.Add(-time.Hour)
			Expect(s.link(ctx, l)).To(Succeed())
			Expect(s.olh().Key).To(Equal(vkey("D")), "an instance with no entry did not exist to compare")
		})

		// The null version keeps its data and its delete marker under
		// separate keys: linking one unlinks the other's list entry, and
		// removes the other's instance entry unless the link is a marker
		// over data, whose removal it logs instead
		// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1705-L1734 and
		// https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L946-L978 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1846-L1875 and
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1076-L1108 at v20.2.4).
		It("keeps the null data and the null delete marker under separate keys, each link replacing the other", func(ctx SpecContext) {
			s.mustLink(ctx, linkOp("A"))
			s.stage(s.version(""))
			s.mustLink(ctx, linkOp(""))
			Expect(s.entry(verInstKey(""))).To(Equal(asVersion(s.version(""), flagsCurrent, 3)))
			Expect(s.entry(verListKey("", 3))).To(Equal(asVersion(s.version(""), flagsCurrent, 3)))
			Expect(s.entry(verInstKey("A"))).To(Equal(asVersion(s.version("A"), flagsNoncurrent, 2)))
			Expect(s.olh().PendingLog[3]).To(Equal([]rgwcls.OLHLogEntry{olhLogged(3, rgwcls.OLHOpLinkOLH, "op-", vkey(""), false)}),
				"data over data removes nothing")

			s.mustLink(ctx, markerOp(""))
			Expect(s.entry(verInstKey(""))).To(Equal(asVersion(s.version(""), flagsCurrent, 3)), "the data entry stays for radosgw's REMOVE_INSTANCE")
			Expect(s.keys()).NotTo(ContainElement(verListKey("", 3)))
			dm := asMarker(s.version(""), flagsCurrent, 4)
			Expect(s.entry(nullMarkerKey())).To(Equal(dm))
			Expect(s.entry(verListKey("", 4))).To(Equal(dm))
			Expect(s.olh().PendingLog[4]).To(Equal([]rgwcls.OLHLogEntry{
				olhLogged(4, rgwcls.OLHOpLinkOLH, "op-", vkey(""), true),
				olhLogged(4, rgwcls.OLHOpRemoveInstance, "op-", vkey(""), false),
			}))

			s.mustLink(ctx, linkOp(""))
			Expect(s.keys()).NotTo(ContainElement(nullMarkerKey()), "the data link removes the marker's instance entry")
			Expect(s.keys()).NotTo(ContainElement(verListKey("", 4)), "and its list entry")
			Expect(s.entry(verInstKey(""))).To(Equal(asVersion(s.version(""), flagsCurrent, 5)))
			Expect(s.entry(verListKey("", 5))).To(Equal(asVersion(s.version(""), flagsCurrent, 5)))
			Expect(s.olh().PendingLog[5]).To(Equal([]rgwcls.OLHLogEntry{olhLogged(5, rgwcls.OLHOpLinkOLH, "op-", vkey(""), false)}))
		})

		// With no OLH entry a plain entry becomes the null version at epoch
		// 1, noncurrent when the link promotes
		// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1587-L1634 and
		// https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1792-L1804 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1715-L1763 and
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1933-L1945 at v20.2.4).
		DescribeTable("converts a plain entry into the null version when no OLH entry exists",
			func(ctx SpecContext, l rgwcls.LinkOLHOp, wantKeys []string, want map[string]func(s *olhShard) rgwcls.DirEntry) {
				s.omap()["k"] = encodeV(s.plain())
				s.mustLink(ctx, l)
				Expect(s.keys()).To(ConsistOf(append(wantKeys, "k", rgwcls.OLHDataKey("k"), verInstKey("A"), verInstKey("B"), verInstKey("C"))))
				Expect(s.entry("k")).To(Equal(versionMarker()))
				for k, en := range want {
					Expect(s.entry(k)).To(Equal(en(s)), "index key %q", k)
				}
			},
			Entry("under another version, listed at epoch 1", linkOp("A"),
				[]string{verInstKey(""), verListKey("", 1), verListKey("A", 2)},
				map[string]func(s *olhShard) rgwcls.DirEntry{
					verInstKey(""):     func(s *olhShard) rgwcls.DirEntry { return asVersion(s.plain(), flagsNoncurrent, 1) },
					verListKey("", 1):  func(s *olhShard) rgwcls.DirEntry { return asVersion(s.plain(), flagsNoncurrent, 1) },
					verListKey("A", 2): func(s *olhShard) rgwcls.DirEntry { return asVersion(s.version("A"), flagsCurrent, 2) },
				}),
			Entry("under a null delete marker, instance entry only", markerOp(""),
				[]string{verInstKey(""), nullMarkerKey(), verListKey("", 2)},
				map[string]func(s *olhShard) rgwcls.DirEntry{
					verInstKey(""):    func(s *olhShard) rgwcls.DirEntry { return asVersion(s.plain(), flagsNoncurrent, 1) },
					nullMarkerKey():   func(s *olhShard) rgwcls.DirEntry { return asMarker(s.plain(), flagsCurrent, 2) },
					verListKey("", 2): func(s *olhShard) rgwcls.DirEntry { return asMarker(s.plain(), flagsCurrent, 2) },
				}),
			Entry("as the null version it links, whose epoch-1 list entry it removes", linkOp(""),
				[]string{verInstKey(""), verListKey("", 2)},
				map[string]func(s *olhShard) rgwcls.DirEntry{
					verInstKey(""):    func(s *olhShard) rgwcls.DirEntry { return asVersion(s.plain(), flagsCurrent, 2) },
					verListKey("", 2): func(s *olhShard) rgwcls.DirEntry { return asVersion(s.plain(), flagsCurrent, 2) },
				}),
		)

		// A demote reads the current version's entry, so a missing one fails
		// the link (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1778-L1790 and
		// https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1388-L1394 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1919-L1931 and
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1514-L1521 at v20.2.4).
		It("fails with ENOENT when the version it demotes has no entry", func(ctx SpecContext) {
			s.stageOLH(rgwcls.OLHEntry{Key: vkey("gone"), Epoch: 2, Tag: olhTag, Exists: true})
			before := maps.Clone(s.omap())
			Expect(s.link(ctx, linkOp("B"))).To(MatchError(radosclient.ErrNotFound))
			Expect(s.omap()).To(Equal(before))
		})

		// With log_op the class writes a bucket index log entry, which the
		// fake does not keep, and the header with its version counted,
		// unless sync is stopped
		// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1829-L1864 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1970-L1999 at v20.2.4).
		DescribeTable("counts the header's version only for a logged link while sync runs",
			func(ctx SpecContext, logOp, syncStopped bool, want uint64) {
				s.c.Object(olhIndex, "", olhOID).OmapHdr = encodeV(rgwcls.DirHeader{Ver: 7, SyncStopped: syncStopped})
				l := linkOp("A")
				l.LogOp = logOp
				s.mustLink(ctx, l)
				Expect(s.c.Header(olhIndex, "", olhOID).Ver).To(Equal(want))
			},
			Entry("logged", true, false, uint64(8)),
			Entry("logged with sync stopped", true, true, uint64(7)),
			Entry("not logged", false, false, uint64(7)),
		)
	})

	Describe("bucket_unlink_instance", func() {
		BeforeEach(func() {
			for _, id := range []string{"A", "B", "C"} {
				s.stage(s.version(id))
			}
		})

		// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1888-L1894 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2035-L2041 at v20.2.4)
		It("refuses an instance with no entry with ENOENT, changing nothing", func(ctx SpecContext) {
			s.mustLink(ctx, linkOp("A"))
			before := maps.Clone(s.omap())
			Expect(s.unlink(ctx, unlinkOp("E"))).To(MatchError(radosclient.ErrNotFound))
			Expect(s.omap()).To(Equal(before))
		})

		// Unlinking the current version makes find_next_key's version current
		// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1936-L1958 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2083-L2105 at v20.2.4),
		// rewritten from its stored entry at its own epoch
		// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1388-L1427 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1514-L1554 at v20.2.4),
		// and logs the unlinked version's removal and drops its list entry
		// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1970-L1984 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2117-L2131 at v20.2.4).
		It("promotes the next version when it unlinks the current one, logging its removal", func(ctx SpecContext) {
			s.mustLink(ctx, linkOp("A"))
			s.mustLink(ctx, linkOp("B"))
			Expect(s.unlink(ctx, unlinkOp("B"))).To(Succeed())
			Expect(s.keys()).To(ConsistOf("k", verListKey("A", 2), verInstKey("A"), verInstKey("B"), verInstKey("C"), rgwcls.OLHDataKey("k")))
			Expect(s.entry(verInstKey("A"))).To(Equal(asVersion(s.version("A"), flagsCurrent, 2)))
			Expect(s.entry(verListKey("A", 2))).To(Equal(asVersion(s.version("A"), flagsCurrent, 2)))
			Expect(s.entry(verInstKey("B"))).To(Equal(asVersion(s.version("B"), flagsCurrent, 3)), "radosgw removes the instance the log names")
			Expect(s.olh()).To(Equal(rgwcls.OLHEntry{
				Key: vkey("A"), Epoch: 4, Tag: olhTag, Exists: true,
				PendingLog: map[uint64][]rgwcls.OLHLogEntry{
					2: {olhLogged(2, rgwcls.OLHOpLinkOLH, "op-A", vkey("A"), false)},
					3: {olhLogged(3, rgwcls.OLHOpLinkOLH, "op-B", vkey("B"), false)},
					4: {
						olhLogged(4, rgwcls.OLHOpLinkOLH, "op-uB", vkey("A"), false),
						olhLogged(4, rgwcls.OLHOpRemoveInstance, "op-uB", vkey("B"), false),
					},
				},
			}))
		})

		// With no later key of the name the class logs UNLINK_OLH for the
		// bare name and marks the entry pending removal
		// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1959-L1967 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2106-L2114 at v20.2.4).
		It("logs UNLINK_OLH and marks the entry pending removal when it unlinks the only version", func(ctx SpecContext) {
			s.mustLink(ctx, linkOp("A"))
			Expect(s.unlink(ctx, unlinkOp("A"))).To(Succeed())
			Expect(s.keys()).To(ConsistOf("k", verInstKey("A"), verInstKey("B"), verInstKey("C"), rgwcls.OLHDataKey("k")))
			Expect(s.olh()).To(Equal(rgwcls.OLHEntry{
				Key: vkey(""), Epoch: 3, Tag: olhTag, PendingRemoval: true,
				PendingLog: map[uint64][]rgwcls.OLHLogEntry{
					2: {olhLogged(2, rgwcls.OLHOpLinkOLH, "op-A", vkey("A"), false)},
					3: {
						olhLogged(3, rgwcls.OLHOpUnlinkOLH, "op-uA", vkey(""), false),
						olhLogged(3, rgwcls.OLHOpRemoveInstance, "op-uA", vkey("A"), false),
					},
				},
			}))
		})

		// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1931-L1989 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2078-L2136 at v20.2.4)
		It("unlinks a noncurrent version's list entry and logs its removal at the next epoch, leaving the head", func(ctx SpecContext) {
			s.mustLink(ctx, linkOp("A"))
			s.mustLink(ctx, linkOp("B"))
			Expect(s.unlink(ctx, unlinkOp("A"))).To(Succeed())
			Expect(s.keys()).NotTo(ContainElement(verListKey("A", 2)))
			Expect(s.entry(verListKey("B", 3))).To(Equal(asVersion(s.version("B"), flagsCurrent, 3)))
			o := s.olh()
			Expect(o.Key).To(Equal(vkey("B")))
			Expect(o.Epoch).To(BeEquivalentTo(4))
			Expect(o.PendingLog[4]).To(Equal([]rgwcls.OLHLogEntry{olhLogged(4, rgwcls.OLHOpRemoveInstance, "op-uA", vkey("A"), false)}))
		})

		// A delete marker has no object for radosgw to remove, so the class
		// removes its instance entry and logs no REMOVE_INSTANCE
		// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1970-L1979 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2117-L2126 at v20.2.4).
		It("removes a delete marker's instance entry itself, logging no removal", func(ctx SpecContext) {
			s.mustLink(ctx, linkOp("A"))
			s.mustLink(ctx, markerOp("D"))
			Expect(s.unlink(ctx, unlinkOp("D"))).To(Succeed())
			Expect(s.keys()).To(ConsistOf("k", verListKey("A", 2), verInstKey("A"), verInstKey("B"), verInstKey("C"), rgwcls.OLHDataKey("k")))
			Expect(s.entry(verInstKey("A"))).To(Equal(asVersion(s.version("A"), flagsCurrent, 2)))
			o := s.olh()
			Expect(o.Key).To(Equal(vkey("A")))
			Expect(o.DeleteMarker).To(BeFalse())
			Expect(o.PendingLog[4]).To(Equal([]rgwcls.OLHLogEntry{olhLogged(4, rgwcls.OLHOpLinkOLH, "op-uD", vkey("A"), false)}))
		})

		// Unlinking the empty instance, S3's versionId=null, reads the null
		// version with its delete marker first, as init's default asks
		// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1332-L1344 and
		// https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1888-L1894 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1458-L1470 and
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2035-L2041 at v20.2.4),
		// so the null marker's own key goes
		// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1970-L1984 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2117-L2131 at v20.2.4).
		It("unlinks the null delete marker by the empty instance, removing its own key", func(ctx SpecContext) {
			s.mustLink(ctx, linkOp("A"))
			s.mustLink(ctx, markerOp(""))
			Expect(s.keys()).To(ContainElements(nullMarkerKey(), verListKey("", 3)))
			Expect(s.unlink(ctx, unlinkOp(""))).To(Succeed())
			Expect(s.keys()).To(ConsistOf("k", verListKey("A", 2), verInstKey("A"), verInstKey("B"), verInstKey("C"), rgwcls.OLHDataKey("k")))
			Expect(s.entry(verListKey("A", 2))).To(Equal(asVersion(s.version("A"), flagsCurrent, 2)))
			o := s.olh()
			Expect(o.Key).To(Equal(vkey("A")))
			Expect(o.DeleteMarker).To(BeFalse())
			Expect(o.PendingLog[4]).To(Equal([]rgwcls.OLHLogEntry{olhLogged(4, rgwcls.OLHOpLinkOLH, "op-u", vkey("A"), false)}))
		})

		// A named epoch below the entry's unlinks the list entry and logs the
		// removal at that epoch under the entry's own
		// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1917-L1929 and
		// https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1268-L1279 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2064-L2076 and
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1391-L1402 at v20.2.4).
		It("logs the removal of a version unlinked at an older epoch under the entry's epoch", func(ctx SpecContext) {
			s.mustLink(ctx, atEpoch(linkOp("A"), 5))
			s.mustLink(ctx, atEpoch(linkOp("B"), 3))
			u := unlinkOp("B")
			u.OLHEpoch = 3
			Expect(s.unlink(ctx, u)).To(Succeed())
			Expect(s.keys()).NotTo(ContainElement(verListKey("B", 3)))
			Expect(s.olh()).To(Equal(rgwcls.OLHEntry{
				Key: vkey("A"), Epoch: 5, Tag: olhTag, Exists: true,
				PendingLog: map[uint64][]rgwcls.OLHLogEntry{5: {
					olhLogged(5, rgwcls.OLHOpLinkOLH, "op-A", vkey("A"), false),
					olhLogged(3, rgwcls.OLHOpRemoveInstance, "op-uB", vkey("B"), false),
				}},
			}))
		})

		// The same branch returns for a delete marker once its list entry is
		// gone, so its instance entry stays (docs/ceph-upstream-bugs.md)
		// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1917-L1925 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2064-L2072 at v20.2.4).
		It("leaves the instance entry of a delete marker unlinked at an older epoch", func(ctx SpecContext) {
			s.mustLink(ctx, atEpoch(linkOp("A"), 5))
			s.mustLink(ctx, atEpoch(markerOp("D"), 3))
			before := s.omap()[rgwcls.OLHDataKey("k")]
			u := unlinkOp("D")
			u.OLHEpoch = 3
			Expect(s.unlink(ctx, u)).To(Succeed())
			Expect(s.keys()).NotTo(ContainElement(verListKey("D", 3)))
			dm := rgwcls.NewDirEntry()
			dm.Key = vkey("D")
			Expect(s.entry(verInstKey("D"))).To(Equal(asMarker(dm, flagsNoncurrent, 3)))
			Expect(s.omap()[rgwcls.OLHDataKey("k")]).To(Equal(before))
		})

		// The class stores the request's OLH tag only on an entry it creates
		// and never compares it
		// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1867-L2018 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2002-L2159 at v20.2.4).
		It("unlinks whatever OLH tag the request carries", func(ctx SpecContext) {
			s.mustLink(ctx, linkOp("A"))
			u := unlinkOp("A")
			u.OLHTag = "other"
			Expect(s.unlink(ctx, u)).To(Succeed())
			o := s.olh()
			Expect(o.Tag).To(Equal(olhTag))
			Expect(o.PendingRemoval).To(BeTrue())
		})

		// With no OLH entry the class versions the name as a link does, the
		// OLH entry taking the request's tag and the bare key
		// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1903-L1915 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2050-L2062 at v20.2.4).
		It("converts a plain entry when no OLH entry exists, unlinking the null version it becomes", func(ctx SpecContext) {
			s.omap()["k"] = encodeV(s.plain())
			u := unlinkOp("")
			u.OLHTag = "u-tag"
			Expect(s.unlink(ctx, u)).To(Succeed())
			Expect(s.keys()).To(ConsistOf("k", verInstKey(""), verInstKey("A"), verInstKey("B"), verInstKey("C"), rgwcls.OLHDataKey("k")))
			Expect(s.entry("k")).To(Equal(versionMarker()))
			Expect(s.entry(verInstKey(""))).To(Equal(asVersion(s.plain(), flagsNoncurrent, 1)))
			Expect(s.olh()).To(Equal(rgwcls.OLHEntry{
				Key: vkey(""), Epoch: 2, Tag: "u-tag", PendingRemoval: true,
				PendingLog: map[uint64][]rgwcls.OLHLogEntry{2: {
					olhLogged(2, rgwcls.OLHOpUnlinkOLH, "op-u", vkey(""), false),
					olhLogged(2, rgwcls.OLHOpRemoveInstance, "op-u", vkey(""), false),
				}},
			}))
		})

		// find_next_key reads the one key after the current version's list
		// key
		// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1437-L1473 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1564-L1600 at v20.2.4),
		// and decreasing_str sorts the epochs from 2^32 up in ascending
		// order ahead of every lower band
		// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L272-L290 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L322-L340 at v20.2.4),
		// so deleting the 2^32+1 version promotes the epoch-2 one, which
		// keeps its epoch (docs/ceph-upstream-bugs.md, the entry on versions
		// whose epoch is 2^32 or more).
		It("promotes the epoch-2 version, not the 2^32 one, when it unlinks the current 2^32+1 version", func(ctx SpecContext) {
			s.mustLink(ctx, linkOp("A"))
			s.mustLink(ctx, atEpoch(linkOp("B"), 1<<32))
			s.mustLink(ctx, atEpoch(linkOp("C"), 1<<32+1))
			Expect(s.keys()).To(Equal([]string{
				"k", "k\x00v4-0000000004294967296\x00iB", "k\x00v4-0000000004294967297\x00iC", "k\x00v913\x00iA",
				verInstKey("A"), verInstKey("B"), verInstKey("C"), rgwcls.OLHDataKey("k"),
			}))
			Expect(s.unlink(ctx, unlinkOp("C"))).To(Succeed())
			Expect(s.entry(verListKey("A", 2))).To(Equal(asVersion(s.version("A"), flagsCurrent, 2)))
			Expect(s.entry(verListKey("B", 1<<32))).To(Equal(asVersion(s.version("B"), flagsNoncurrent, 1<<32)))
			o := s.olh()
			Expect(o.Key).To(Equal(vkey("A")))
			Expect(o.Epoch).To(BeEquivalentTo(uint64(1<<32 + 2)))
			Expect(o.PendingLog[1<<32+2]).To(Equal([]rgwcls.OLHLogEntry{
				olhLogged(1<<32+2, rgwcls.OLHOpLinkOLH, "op-uC", vkey("A"), false),
				olhLogged(1<<32+2, rgwcls.OLHOpRemoveInstance, "op-uC", vkey("C"), false),
			}))
		})

		// The next key must be of the same name, which the decoded entry's
		// name decides (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1437-L1473 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1564-L1600 at v20.2.4).
		It("promotes no version of another name that sorts next", func(ctx SpecContext) {
			s.mustLink(ctx, linkOp("A"))
			s.omap()["kz"] = encodeV(rgwcls.DirEntry{Key: rgwcls.ObjKey{Name: "kz"}, Ver: rgwcls.NewEntryVer(), Flags: rgwcls.FlagVerMarker})
			Expect(s.unlink(ctx, unlinkOp("A"))).To(Succeed())
			o := s.olh()
			Expect(o.Exists).To(BeFalse())
			Expect(o.PendingLog[3][0]).To(Equal(olhLogged(3, rgwcls.OLHOpUnlinkOLH, "op-uA", vkey(""), false)))
		})

		// The unlink ends as the link does: with log_op the header's version
		// is counted unless sync is stopped
		// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1991-L2017 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2138-L2158 at v20.2.4).
		DescribeTable("counts the header's version only for a logged unlink while sync runs",
			func(ctx SpecContext, logOp, syncStopped bool, want uint64) {
				s.mustLink(ctx, linkOp("A"))
				s.c.Object(olhIndex, "", olhOID).OmapHdr = encodeV(rgwcls.DirHeader{Ver: 7, SyncStopped: syncStopped})
				u := unlinkOp("A")
				u.LogOp = logOp
				Expect(s.unlink(ctx, u)).To(Succeed())
				Expect(s.c.Header(olhIndex, "", olhOID).Ver).To(Equal(want))
			},
			Entry("logged", true, false, uint64(8)),
			Entry("logged with sync stopped", true, true, uint64(7)),
			Entry("not logged", false, false, uint64(7)),
		)
	})

	// olhLog is a pending log of one LINK_OLH per epoch.
	olhLog := func(epochs ...uint64) map[uint64][]rgwcls.OLHLogEntry {
		m := map[uint64][]rgwcls.OLHLogEntry{}
		for _, e := range epochs {
			id := strconv.FormatUint(e, 10)
			m[e] = []rgwcls.OLHLogEntry{olhLogged(e, rgwcls.OLHOpLinkOLH, "op-"+id, vkey(id), false)}
		}
		return m
	}
	epochRange := func(from, to uint64) []uint64 {
		var out []uint64
		for e := from; e <= to; e++ {
			out = append(out, e)
		}
		return out
	}

	Describe("bucket_read_olh_log", func() {
		read := func(ctx context.Context, marker uint64) (rgwcls.ReadOLHLogRet, error) {
			return s.readLog(ctx, rgwcls.ReadOLHLogOp{OLH: vkey(""), VerMarker: marker, OLHTag: olhTag})
		}

		// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L2033-L2050 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2174-L2191 at v20.2.4)
		It("refuses a key with an instance with EINVAL and another tag with ECANCELED, a missing entry's tag being empty", func(ctx SpecContext) {
			_, err := s.readLog(ctx, rgwcls.ReadOLHLogOp{OLH: vkey("A"), OLHTag: olhTag})
			Expect(err).To(MatchError(radosclient.ErrInvalid))
			_, err = s.readLog(ctx, rgwcls.ReadOLHLogOp{OLH: vkey(""), OLHTag: olhTag})
			Expect(err).To(MatchError(radosclient.ErrCanceled), "no entry")
			ret, err := s.readLog(ctx, rgwcls.ReadOLHLogOp{OLH: vkey("")})
			Expect(err).NotTo(HaveOccurred())
			Expect(ret).To(Equal(rgwcls.ReadOLHLogRet{}), "no entry, no tag")
			s.stageOLH(rgwcls.OLHEntry{Tag: olhTag, PendingLog: olhLog(2)})
			_, err = s.readLog(ctx, rgwcls.ReadOLHLogOp{OLH: vkey(""), OLHTag: "other"})
			Expect(err).To(MatchError(radosclient.ErrCanceled))
		})

		// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L2054-L2067 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2195-L2208 at v20.2.4)
		It("answers the epochs above the marker", func(ctx SpecContext) {
			s.stageOLH(rgwcls.OLHEntry{Tag: olhTag, PendingLog: olhLog(2, 3, 4)})
			ret, err := read(ctx, 0)
			Expect(err).NotTo(HaveOccurred())
			Expect(ret).To(Equal(rgwcls.ReadOLHLogRet{Log: olhLog(2, 3, 4)}))
			ret, err = read(ctx, 3)
			Expect(err).NotTo(HaveOccurred())
			Expect(ret).To(Equal(rgwcls.ReadOLHLogRet{Log: olhLog(4)}))
			ret, err = read(ctx, 4)
			Expect(err).NotTo(HaveOccurred())
			Expect(ret).To(Equal(rgwcls.ReadOLHLogRet{}))
		})

		// MAX_OLH_LOG_ENTRIES (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L2054-L2067 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2195-L2208 at v20.2.4).
		DescribeTable("answers at most 1000 epochs, truncated while more remain",
			func(ctx SpecContext, last, marker uint64, want []uint64, truncated bool) {
				s.stageOLH(rgwcls.OLHEntry{Tag: olhTag, PendingLog: olhLog(epochRange(1, last)...)})
				ret, err := read(ctx, marker)
				Expect(err).NotTo(HaveOccurred())
				Expect(ret.IsTruncated).To(Equal(truncated))
				Expect(ret.Log).To(Equal(olhLog(want...)))
			},
			Entry("a log of 1000 whole", uint64(1000), uint64(0), epochRange(1, 1000), false),
			Entry("a log of 1001 cut at 1000", uint64(1001), uint64(0), epochRange(1, 1000), true),
			Entry("the rest of a log of 1001 after its first", uint64(1001), uint64(1), epochRange(2, 1001), false),
		)

		// An empty log's first epoch is undefined in the class, and either
		// branch answers nothing (docs/ceph-upstream-bugs.md, "cls_rgw's
		// read_olh_log reads the first epoch of an empty pending log")
		// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L2054-L2067 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2195-L2208 at v20.2.4).
		It("answers an empty log empty and untruncated", func(ctx SpecContext) {
			s.stageOLH(rgwcls.OLHEntry{Tag: olhTag, Exists: true})
			ret, err := read(ctx, 0)
			Expect(err).NotTo(HaveOccurred())
			Expect(ret).To(Equal(rgwcls.ReadOLHLogRet{}))
		})
	})

	Describe("bucket_trim_olh_log", func() {
		trim := func(ctx context.Context, t rgwcls.TrimOLHLogOp) error {
			return s.write(ctx, func(op *radosclient.WriteOp) { rgwcls.TrimOLHLog(op, t, denc.Squid) })
		}

		// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L2087-L2124 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2228-L2277 at v20.2.4)
		It("erases the epochs at or below the version and keeps the rest of the entry", func(ctx SpecContext) {
			base := rgwcls.OLHEntry{Key: vkey("A"), Epoch: 4, Tag: olhTag, Exists: true}
			o := base
			o.PendingLog = olhLog(2, 3, 4)
			s.stageOLH(o)
			Expect(trim(ctx, rgwcls.TrimOLHLogOp{OLH: vkey(""), Ver: 3, OLHTag: olhTag})).To(Succeed())
			o.PendingLog = olhLog(4)
			Expect(s.olh()).To(Equal(o))
			Expect(trim(ctx, rgwcls.TrimOLHLogOp{OLH: vkey(""), Ver: 4, OLHTag: olhTag})).To(Succeed())
			Expect(s.olh()).To(Equal(base))
		})

		// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L2087-L2105 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2228-L2246 at v20.2.4)
		It("refuses an instance with EINVAL and another tag with ECANCELED, and trims a missing entry with no tag into being", func(ctx SpecContext) {
			Expect(trim(ctx, rgwcls.TrimOLHLogOp{OLH: vkey("A"), Ver: 3, OLHTag: olhTag})).To(MatchError(radosclient.ErrInvalid))
			Expect(trim(ctx, rgwcls.TrimOLHLogOp{OLH: vkey(""), Ver: 3, OLHTag: olhTag})).To(MatchError(radosclient.ErrCanceled))
			Expect(s.keys()).To(BeEmpty())
			Expect(trim(ctx, rgwcls.TrimOLHLogOp{OLH: vkey(""), Ver: 3})).To(Succeed())
			Expect(s.olh()).To(Equal(rgwcls.OLHEntry{}))
		})
	})

	Describe("bucket_clear_olh", func() {
		clearOLH := func(ctx context.Context, c rgwcls.ClearOLHOp) error {
			return s.write(ctx, func(op *radosclient.WriteOp) { rgwcls.ClearOLH(op, c, denc.Squid) })
		}
		BeforeEach(func() {
			s.stageOLH(rgwcls.OLHEntry{Key: vkey(""), Epoch: 3, Tag: olhTag, PendingRemoval: true})
			s.stage(s.version("A"))
		})

		// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L2159-L2189 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2324-L2354 at v20.2.4)
		DescribeTable("removes the OLH entry, and the plain entry only when it is a version marker",
			func(ctx SpecContext, plain *rgwcls.DirEntry, wantKeys []string) {
				if plain != nil {
					s.omap()["k"] = encodeV(*plain)
				}
				Expect(clearOLH(ctx, rgwcls.ClearOLHOp{Key: vkey(""), OLHTag: olhTag})).To(Succeed())
				Expect(s.keys()).To(ConsistOf(wantKeys))
			},
			Entry("a version marker", new(versionMarker()), []string{verInstKey("A")}),
			Entry("a plain entry", new(rgwcls.DirEntry{Key: vkey(""), Ver: rgwcls.NewEntryVer(), Exists: true}), []string{"k", verInstKey("A")}),
			Entry("no plain entry", (*rgwcls.DirEntry)(nil), []string{verInstKey("A")}),
		)

		// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L2139-L2157 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2292-L2322 at v20.2.4)
		It("refuses an instance with EINVAL and another tag with ECANCELED, removing nothing", func(ctx SpecContext) {
			s.omap()["k"] = encodeV(versionMarker())
			before := maps.Clone(s.omap())
			Expect(clearOLH(ctx, rgwcls.ClearOLHOp{Key: vkey("A"), OLHTag: olhTag})).To(MatchError(radosclient.ErrInvalid))
			Expect(clearOLH(ctx, rgwcls.ClearOLHOp{Key: vkey(""), OLHTag: "other"})).To(MatchError(radosclient.ErrCanceled))
			Expect(s.omap()).To(Equal(before))
		})
	})
})
