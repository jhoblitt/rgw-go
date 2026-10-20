package fakerados_test

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	rgwcls "github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

var _ = Describe("RGWClass's index methods on versioned keys", func() {
	var (
		s   *olhShard
		now time.Time
	)
	BeforeEach(func(ctx SpecContext) {
		s = newOLHShard(ctx)
		now = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
		s.c.SetClock(func() time.Time { return now })
	})
	header := func() rgwcls.DirHeader { return s.c.Header(olhIndex, "", olhOID) }
	seedHeader := func(h rgwcls.DirHeader) { s.c.Object(olhIndex, "", olhOID).OmapHdr = encodeV(h) }
	mainStats := func(n, size uint64) map[uint8]rgwcls.CategoryStats {
		return map[uint8]rgwcls.CategoryStats{rgwcls.CategoryMain: {NumEntries: n, TotalSize: size, TotalSizeRounded: 4096 * n, ActualSize: size}}
	}
	// listReply runs bucket_list on a read op and decodes the reply the class
	// encodes whatever it returns; a negative value also fails the op.
	listReply := func(ctx context.Context, l rgwcls.ListOp) (ret rgwcls.ListRet, rval int32) {
		GinkgoHelper()
		r := radosclient.NewReadOp()
		res := r.Exec(rgwcls.Class, "bucket_list", encodeV(l))
		_, err := s.p.Read(ctx, olhOID, r, radosclient.OpFlagNone)
		out, rval, rerr := res.Reply()
		Expect(rerr).NotTo(HaveOccurred())
		if rval < 0 {
			Expect(err).To(haveErrno(syscall.Errno(-rval)))
		} else {
			Expect(err).NotTo(HaveOccurred())
		}
		d := denc.NewDecoder(out)
		ret = rgwcls.DecodeListRet(d)
		Expect(d.Err()).NotTo(HaveOccurred())
		return ret, rval
	}
	const (
		eAcute  = "\xc3\xa9"
		uUmlaut = "\xc3\xbc"
	)
	// plain stores an unversioned write's entry under the plain key name.
	plain := func(name string) {
		en := s.plain()
		en.Key = rgwcls.ObjKey{Name: name}
		s.omap()[name] = encodeV(en)
	}
	// markerAt stores a version marker under the plain key name.
	markerAt := func(name string) {
		m := versionMarker()
		m.Key.Name = name
		s.omap()[name] = encodeV(m)
	}

	Describe("bucket_prepare_op and bucket_complete_op", func() {
		prepare := func(ctx context.Context, key rgwcls.ObjKey, tag string) error {
			return s.write(ctx, func(op *radosclient.WriteOp) {
				rgwcls.GuardBucketResharding(op, denc.Squid)
				rgwcls.BucketPrepareOp(op, rgwcls.PrepareOp{Op: rgwcls.OpAdd, Key: key, Tag: tag, Locator: "loc"}, denc.Squid)
			})
		}
		complete := func(ctx context.Context, co rgwcls.CompleteOp) error {
			return s.write(ctx, func(op *radosclient.WriteOp) {
				rgwcls.GuardBucketResharding(op, denc.Squid)
				rgwcls.BucketCompleteOp(op, co, denc.Squid)
			})
		}
		// add's epoch is above the staged versions' 11 on the same pool, so
		// the class does not turn it into a cancel.
		add := func(key rgwcls.ObjKey, tag string, size uint64) rgwcls.CompleteOp {
			return rgwcls.CompleteOp{
				Op: rgwcls.OpAdd, Key: key, Tag: tag, Ver: rgwcls.EntryVer{Pool: 7, Epoch: 12},
				Meta: rgwcls.DirEntryMeta{Category: rgwcls.CategoryMain, Size: size, AccountedSize: size, ETag: "etag-" + tag},
			}
		}
		pending := func(tag string) []rgwcls.PendingEntry {
			return []rgwcls.PendingEntry{{Tag: tag, Info: rgwcls.PendingInfo{State: rgwcls.PendingModify, Timestamp: now, Op: uint8(rgwcls.OpAdd)}}}
		}

		// Both read and write the entry under encode_obj_index_key, a version's
		// instance key
		// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L339-L346 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L389-L396 at v20.2.4):
		// the prepare through read_key_entry
		// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L839-L875 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L971-L1005 at v20.2.4),
		// and the complete's CLS_RGW_OP_ADD
		// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1168-L1197 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1301-L1329 at v20.2.4).
		It("prepares and completes a version under its instance key, writing no plain or list entry", func(ctx SpecContext) {
			Expect(prepare(ctx, vkey("A"), "t")).To(Succeed())
			Expect(s.keys()).To(ConsistOf(verInstKey("A")))
			Expect(s.entry(verInstKey("A"))).To(Equal(rgwcls.DirEntry{
				Key: vkey("A"), Ver: rgwcls.EntryVer{Pool: -1}, Locator: "loc", PendingMap: pending("t"),
			}), "cls_rgw.cc:851-867")

			Expect(complete(ctx, add(vkey("A"), "t", 5))).To(Succeed())
			Expect(s.keys()).To(ConsistOf(verInstKey("A")), "a complete on an instance key leaves no list entry")
			Expect(s.c.ListEntries(olhIndex, "", olhOID, "k")).To(BeEmpty())
			Expect(s.entry(verInstKey("A"))).To(Equal(rgwcls.DirEntry{
				Key: vkey("A"), Ver: rgwcls.EntryVer{Pool: 7, Epoch: 12}, Exists: true, Locator: "loc", Tag: "t",
				Meta: rgwcls.DirEntryMeta{Category: rgwcls.CategoryMain, Size: 5, AccountedSize: 5, ETag: "etag-t"},
			}))
			Expect(header()).To(Equal(rgwcls.DirHeader{Ver: 1, Stats: mainStats(1, 5)}))
		})

		// The completion keeps only FLAG_VER of the stored entry, which may
		// have been a delete marker, and writes only the key it read
		// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1061-L1064 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1196-L1199 at v20.2.4).
		It("keeps only the versioned flag of the version it completes and leaves its list entry", func(ctx SpecContext) {
			old := asMarker(s.version("A"), flagsCurrent, 4)
			s.stage(old)
			s.omap()[verListKey("A", 4)] = encodeV(old)
			listed := s.omap()[verListKey("A", 4)]
			Expect(prepare(ctx, vkey("A"), "t")).To(Succeed())
			Expect(s.entry(verInstKey("A")).Flags).To(Equal(flagsCurrent|rgwcls.FlagDeleteMarker), "prepare changes no flag")
			Expect(complete(ctx, add(vkey("A"), "t", 5))).To(Succeed())
			en := s.entry(verInstKey("A"))
			Expect(en.Flags).To(Equal(rgwcls.FlagVer))
			Expect(en.VersionedEpoch).To(BeEquivalentTo(4), "the stored entry's epoch stays")
			Expect(s.omap()[verListKey("A", 4)]).To(Equal(listed))
		})

		// A name without an instance whose plain entry is a version marker
		// resolves through read_key_entry to the null version's data key,
		// which is also the key it writes when there is no entry there
		// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L946-L978 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1076-L1108 at v20.2.4).
		DescribeTable("follows a version marker to the null version's data entry",
			func(ctx SpecContext, staged bool) {
				s.omap()["k"] = encodeV(versionMarker())
				want := rgwcls.DirEntry{Key: vkey(""), Ver: rgwcls.EntryVer{Pool: -1}, Locator: "loc"}
				if staged {
					want = s.version("")
					s.stage(want)
				}
				marker := s.omap()["k"]
				Expect(prepare(ctx, vkey(""), "t")).To(Succeed())
				Expect(s.keys()).To(ConsistOf("k", verInstKey("")))
				want.PendingMap = pending("t")
				Expect(s.entry(verInstKey(""))).To(Equal(want))
				Expect(s.omap()["k"]).To(Equal(marker), "the version marker stays")

				Expect(complete(ctx, add(vkey(""), "t", 7))).To(Succeed())
				Expect(s.keys()).To(ConsistOf("k", verInstKey("")))
				Expect(s.entry(verInstKey("")).Meta.Size).To(BeEquivalentTo(7))
				Expect(s.omap()["k"]).To(Equal(marker))
			},
			Entry("that exists", true),
			Entry("that does not exist, creating it there", false),
		)

		// complete_remove_obj reads each key through read_key_entry, so an
		// instance names its instance entry and a versioned name's bare key
		// its null data, and skips one it cannot read
		// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L981-L1004 and
		// https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1215-L1227 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1111-L1134 and
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1346-L1358 at v20.2.4).
		It("removes and unaccounts the versions remove_objs names", func(ctx SpecContext) {
			s.omap()["k"] = encodeV(versionMarker())
			s.stage(s.version(""))
			s.stage(s.version("B"))
			seedHeader(rgwcls.DirHeader{Ver: 2, Stats: mainStats(2, 10)})
			Expect(prepare(ctx, vkey("A"), "t")).To(Succeed())
			co := add(vkey("A"), "t", 3)
			co.RemoveObjs = []rgwcls.ObjKey{vkey("B"), vkey(""), vkey("missing")}
			Expect(complete(ctx, co)).To(Succeed())
			Expect(s.keys()).To(ConsistOf("k", verInstKey("A")), "B's and the null data's instance entries go, the marker stays")
			Expect(header()).To(Equal(rgwcls.DirHeader{Ver: 3, Stats: mainStats(1, 3)}))
		})

		// A stored entry that does not decode is read_omap_entry's EIO
		// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L914-L932 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1044-L1062 at v20.2.4),
		// which the prepare and the complete return from read_key_entry
		// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L839-L845 and
		// https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1047-L1059 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L971-L977 and
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1182-L1194 at v20.2.4).
		DescribeTable("fails with EIO on a version's entry that does not decode, writing nothing",
			func(ctx SpecContext, run func(context.Context) error) {
				s.omap()[verInstKey("A")] = []byte{9}
				Expect(run(ctx)).To(haveErrno(syscall.EIO))
				Expect(s.keys()).To(ConsistOf(verInstKey("A")))
				Expect(header()).To(Equal(rgwcls.DirHeader{}))
			},
			Entry("prepare", func(ctx context.Context) error { return prepare(ctx, vkey("A"), "t") }),
			Entry("complete", func(ctx context.Context) error { return complete(ctx, add(vkey("A"), "t", 1)) }),
		)
	})

	// The change is read under encode_obj_index_key
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L2249-L2271 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2419-L2441 at v20.2.4)
	// and CEPH_RGW_UPDATE and CEPH_RGW_REMOVE write that key alone
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L2330-L2389 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2500-L2557 at v20.2.4),
	// so a version's suggestion reaches its instance entry and never the list
	// entry the listing found.
	DescribeTable("applies dir_suggest_changes for a version to its instance entry only",
		func(ctx SpecContext, op byte, wantKeys []string, wantSize uint64) {
			v := asVersion(s.version("A"), flagsCurrent, 2)
			s.stage(v)
			s.omap()[verListKey("A", 2)] = encodeV(v)
			listed := s.omap()[verListKey("A", 2)]
			seedHeader(rgwcls.DirHeader{Ver: 4, Stats: mainStats(1, 5)})
			change := v
			change.Meta.Size, change.Meta.AccountedSize = 9, 9
			Expect(writeErr(ctx, s.p, olhOID, func(w *radosclient.WriteOp) {
				rgwcls.SuggestChanges(w, []rgwcls.Suggestion{{Op: op, Entry: change}}, denc.Squid)
			})).To(Succeed())
			Expect(s.keys()).To(ConsistOf(wantKeys))
			Expect(s.omap()[verListKey("A", 2)]).To(Equal(listed), "the list entry is left as it was")
			Expect(header().Stats[rgwcls.CategoryMain].TotalSize).To(Equal(wantSize))
			if op == rgwcls.SuggestUpdate {
				change.IndexVer = 4
				Expect(s.entry(verInstKey("A"))).To(Equal(change))
			}
		},
		Entry("an update", rgwcls.SuggestUpdate, []string{verInstKey("A"), verListKey("A", 2)}, uint64(9)),
		Entry("a removal", rgwcls.SuggestRemove, []string{verListKey("A", 2)}, uint64(0)),
	)

	Describe("bucket_list", func() {
		// The name k holds A at epoch 2, the null version at 3 and B at 4,
		// current, as the class links them; a, z, é and ü are plain entries,
		// é and ü sorting after the 0x80 namespace that holds k's instance
		// and OLH entries.
		BeforeEach(func(ctx SpecContext) {
			for _, id := range []string{"A", "", "B"} {
				s.stage(s.version(id))
				s.mustLink(ctx, linkOp(id))
			}
			for _, n := range []string{"a", "z", eAcute, uUmlaut} {
				plain(n)
			}
			Expect(verListKey("B", 4) < verListKey("", 3)).To(BeTrue(), "a newer version lists first")
		})
		listOp := func(start rgwcls.ObjKey, n uint32, versions bool) rgwcls.ListOp {
			return rgwcls.ListOp{StartObj: start, NumEntries: n, ListVersions: versions}
		}
		keys := func(ret rgwcls.ListRet) []string { return slices.Sorted(maps.Keys(ret.Dir.Entries)) }
		listed := func(ctx context.Context, l rgwcls.ListOp) []string {
			GinkgoHelper()
			ret, rval := listReply(ctx, l)
			Expect(rval).To(BeZero())
			return keys(ret)
		}

		// Version markers are never listed (is_valid); without list_versions
		// a version that is not visible, or one named as the start key, is
		// skipped with every version of its name; get_obj_vals never returns
		// the 0x80 namespace
		// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L604-L623 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L654-L673 at v20.2.4).
		DescribeTable("lists with versions on and off over instance, list and marker entries",
			func(ctx SpecContext, marker, versions bool, want []string) {
				if marker {
					s.mustLink(ctx, markerOp("C"))
				}
				Expect(listed(ctx, listOp(rgwcls.ObjKey{}, 20, versions))).To(Equal(want))
			},
			Entry("versions off: the current version only", false, false,
				[]string{"a", verListKey("B", 4), "z", eAcute, uUmlaut}),
			Entry("versions on: every version", false, true,
				[]string{"a", verListKey("B", 4), verListKey("", 3), verListKey("A", 2), "z", eAcute, uUmlaut}),
			Entry("versions off, a current delete marker: nothing of the name", true, false,
				[]string{"a", "z", eAcute, uUmlaut}),
			Entry("versions on, a current delete marker: it and every version", true, true,
				[]string{"a", verListKey("C", 5), verListKey("B", 4), verListKey("", 3), verListKey("A", 2), "z", eAcute, uUmlaut}),
		)

		// encode_list_index_key gives the name; for an instance, "null" read
		// as the empty one, the stored instance entry's list key, or the name
		// and 0x01 when there is none
		// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L358-L390 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L408-L440 at v20.2.4).
		// The listing discards what its encode_list_index_key call returns
		// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L528-L529 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L578-L579 at v20.2.4),
		// so an entry that does not decode leaves the start key empty.
		DescribeTable("resolves the start key through encode_list_index_key",
			func(ctx SpecContext, start rgwcls.ObjKey, versions bool, want []string) {
				s.omap()[rgwcls.InstanceIndexKey(vkey("bad"), false)] = []byte{9}
				Expect(listed(ctx, listOp(start, 20, versions))).To(Equal(want))
			},
			Entry("no instance: after the name", vkey(""), true,
				[]string{verListKey("B", 4), verListKey("", 3), verListKey("A", 2), "z", eAcute, uUmlaut}),
			Entry("no instance, versions off: past the name's current version too", vkey(""), false,
				[]string{"z", eAcute, uUmlaut}),
			Entry("null: after the null version's list key", vkey("null"), true,
				[]string{verListKey("A", 2), "z", eAcute, uUmlaut}),
			Entry("an instance with an entry: after its list key", vkey("B"), true,
				[]string{verListKey("", 3), verListKey("A", 2), "z", eAcute, uUmlaut}),
			Entry("an instance without one: after every version of the name", vkey("gone"), true,
				[]string{"z", eAcute, uUmlaut}),
			Entry("an instance whose entry does not decode: from the shard's first key", vkey("bad"), true,
				[]string{"a", verListKey("B", 4), verListKey("", 3), verListKey("A", 2), "z", eAcute, uUmlaut}),
		)

		// get_obj_vals drops the 0x80 keys a read meets and, when the read
		// was truncated short of the count, reads again after them
		// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L188-L265 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L238-L315 at v20.2.4).
		DescribeTable("reads past the 0x80 namespace",
			func(ctx SpecContext, l rgwcls.ListOp, want []string, truncated bool) {
				ret, rval := listReply(ctx, l)
				Expect(rval).To(BeZero())
				Expect(keys(ret)).To(Equal(want))
				Expect(ret.IsTruncated).To(Equal(truncated))
				if truncated {
					Expect(ret.Marker).To(Equal(rgwcls.ObjKey{Name: want[len(want)-1]}))
				}
			},
			Entry("a read of 0x80 keys alone, topped up after them", listOp(rgwcls.ObjKey{Name: "z"}, 1, true),
				[]string{eAcute}, true),
			Entry("a read across them, topped up after them", listOp(vkey("A"), 2, true),
				[]string{"z", eAcute}, true),
			Entry("a read of the rest of the shard", listOp(rgwcls.ObjKey{Name: "z"}, 20, true),
				[]string{eAcute, uUmlaut}, false),
		)
	})

	// Each of the max_attempts omap reads one call makes
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L492 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L542 at v20.2.4)
	// can list nothing, and a truncated reply of no entries is
	// RGWBIAdvanceAndRetryError, its reply encoded with the marker to go on
	// from
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L680-L693 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L730-L743 at v20.2.4).
	DescribeTable("answers advance-and-retry after eight reads that list nothing",
		func(ctx SpecContext, names []string, rval int32, truncated bool, marker rgwcls.ObjKey) {
			seedHeader(rgwcls.DirHeader{Ver: 6})
			for _, n := range names {
				markerAt(n)
				dm := asMarker(s.version("dm"), flagsCurrent, 2)
				dm.Key.Name = n
				s.omap()[rgwcls.ListIndexKey(dm.Key, 2)] = encodeV(dm)
			}
			ret, got := listReply(ctx, rgwcls.ListOp{NumEntries: 1})
			Expect(got).To(Equal(rval))
			Expect(ret).To(Equal(rgwcls.ListRet{Dir: rgwcls.Dir{Header: rgwcls.DirHeader{Ver: 6}}, IsTruncated: truncated, Marker: marker}))
		},
		Entry("with a fifth name still to read: EFBIG and the marker past the fourth's versions",
			[]string{"d0", "d1", "d2", "d3", "d4"}, -int32(syscall.EFBIG), true, rgwcls.ObjKey{Name: "d3\x01"}),
		Entry("when the eighth read reached the end: 0, untruncated",
			[]string{"d0", "d1", "d2", "d3"}, int32(0), false, rgwcls.ObjKey{}),
	)

	// When the read that met the 0x80 namespace kept a key that sorts after
	// it, get_obj_vals reads the rest after that key rather than after 0x81
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L248-L250 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L298-L300 at v20.2.4).
	// Over version markers each read lists nothing, so a call ends after its
	// max_attempts reads, and the marker counts the keys they read: the first
	// keeps é00 and reads é01 and é02 after it, the other seven two markers
	// each. A second read after 0x81 would read é00 again and end at é15.
	It("reads the rest of a read that met the 0x80 namespace after the last key it kept", func(ctx SpecContext) {
		s.stage(s.version("A"))
		s.stage(s.version("B"))
		plain(eAcute + "00")
		for i := 1; i <= 30; i++ {
			markerAt(fmt.Sprintf("%s%02d", eAcute, i))
		}
		ret, rval := listReply(ctx, rgwcls.ListOp{NumEntries: 3, ListVersions: true})
		Expect(rval).To(BeZero())
		Expect(slices.Sorted(maps.Keys(ret.Dir.Entries))).To(Equal([]string{eAcute + "00"}))
		Expect(ret.IsTruncated).To(BeTrue())
		Expect(ret.Marker).To(Equal(rgwcls.ObjKey{Name: eAcute + "16"}))
	})

	// check_index counts each existing plain entry without flags and each
	// existing instance entry whatever its flags, and reads no OLH entry
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L3035-L3114 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L3361-L3434 at v20.2.4);
	// list_plain_entries covers the keys below the 0x80 namespace and after
	// BI_PREFIX_END
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L2794-L2849 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L3069-L3124 at v20.2.4).
	// bucket_rebuild_index writes that header with its version counted
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L3116-L3126 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L3436-L3458 at v20.2.4).
	Describe("bucket_check_index and bucket_rebuild_index on a versioned shard", func() {
		BeforeEach(func(ctx SpecContext) {
			seedHeader(rgwcls.DirHeader{Ver: 9, TagTimeout: 30, Stats: mainStats(1, 1)})
			for _, id := range []string{"A", "B"} {
				s.stage(s.version(id))
				s.mustLink(ctx, linkOp(id))
			}
			s.mustLink(ctx, markerOp("C"))
			pending := s.version("D")
			pending.Exists = false
			s.stage(pending)
			for _, n := range []string{"a", eAcute, "\x80zz"} {
				plain(n)
			}
		})
		// The plain entries a, é and "\x80zz", after BI_PREFIX_END, and the
		// instance entries A and B; not D, which does not exist, nor the
		// delete marker C, whose instance entry the class writes without
		// exists (init_as_delete_marker), nor k's list entries and version
		// marker, which carry flags; at the header version ver.
		counted := func(ver uint64) rgwcls.DirHeader {
			return rgwcls.DirHeader{Ver: ver, TagTimeout: 30, Stats: map[uint8]rgwcls.CategoryStats{
				rgwcls.CategoryMain: {NumEntries: 5, TotalSize: 25, TotalSizeRounded: 5 * 4096, ActualSize: 25},
			}}
		}
		It("counts the existing instance entries beside the plain ones", func(ctx SpecContext) {
			r := radosclient.NewReadOp()
			res := rgwcls.BucketCheckIndex(r, denc.Squid)
			_, err := s.p.Read(ctx, olhOID, r, radosclient.OpFlagNone)
			Expect(err).NotTo(HaveOccurred())
			ret, err := res.Result()
			Expect(err).NotTo(HaveOccurred())
			Expect(ret.ExistingHeader).To(Equal(rgwcls.DirHeader{Ver: 9, TagTimeout: 30, Stats: mainStats(1, 1)}))
			Expect(ret.CalculatedHeader).To(Equal(counted(9)))
		})
		It("writes the count as the header, its version counted", func(ctx SpecContext) {
			Expect(s.write(ctx, func(op *radosclient.WriteOp) { rgwcls.BucketRebuildIndex(op) })).To(Succeed())
			Expect(header()).To(Equal(counted(10)))
		})
		It("fails with EIO on an instance entry that does not decode", func(ctx SpecContext) {
			s.omap()[verInstKey("E")] = []byte{9}
			r := radosclient.NewReadOp()
			rgwcls.BucketCheckIndex(r, denc.Squid)
			_, err := s.p.Read(ctx, olhOID, r, radosclient.OpFlagNone)
			Expect(err).To(haveErrno(syscall.EIO))
		})
	})

	Describe("the shard readers", func() {
		It("decode a version's instance and list entries and the name's OLH entry", func(ctx SpecContext) {
			s.stage(s.version("A"))
			s.mustLink(ctx, linkOp("A"))
			other := asVersion(s.version("A"), flagsCurrent, 2)
			other.Key.Name = "kk"
			s.omap()[rgwcls.ListIndexKey(other.Key, 2)] = encodeV(other)

			want := asVersion(s.version("A"), flagsCurrent, 2)
			en, ok := s.c.InstanceEntry(olhIndex, "", olhOID, vkey("A"))
			Expect(ok).To(BeTrue())
			Expect(en).To(Equal(want))
			Expect(s.c.ListEntries(olhIndex, "", olhOID, "k")).To(Equal([]rgwcls.DirEntry{want}), "not kk's")
			olh, ok := s.c.OLHEntry(olhIndex, "", olhOID, "k")
			Expect(ok).To(BeTrue())
			Expect(olh).To(Equal(s.olh()))
		})

		It("report a missing shard or entry as absent", func() {
			for _, oid := range []string{olhOID, "absent"} {
				_, ok := s.c.InstanceEntry(olhIndex, "", oid, vkey("A"))
				Expect(ok).To(BeFalse(), oid)
				Expect(s.c.ListEntries(olhIndex, "", oid, "k")).To(BeNil(), oid)
				_, ok = s.c.OLHEntry(olhIndex, "", oid, "k")
				Expect(ok).To(BeFalse(), oid)
			}
		})

		It("panic on an entry that does not decode", func() {
			s.omap()[verInstKey("A")] = []byte{9}
			s.omap()[verListKey("A", 2)] = []byte{9}
			s.omap()[rgwcls.OLHDataKey("k")] = []byte{9}
			Expect(func() { s.c.InstanceEntry(olhIndex, "", olhOID, vkey("A")) }).To(PanicWith(ContainSubstring("index entry")))
			Expect(func() { s.c.ListEntries(olhIndex, "", olhOID, "k") }).To(PanicWith(ContainSubstring("list entry")))
			Expect(func() { s.c.OLHEntry(olhIndex, "", olhOID, "k") }).To(PanicWith(ContainSubstring("OLH entry")))
		})
	})
})
