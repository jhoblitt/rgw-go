package rgw_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/denc/goldentest"
)

// The lc vectors below are derived by hand from the C++ encode bodies, the
// same at v19.2.7 and v20.2.4. ceph-dencoder at both tags decodes and
// re-encodes the head, entry, set_entry_op and get_entry_ret vectors to
// these bytes; it registers none of the other lc structs.
var (
	// lcHeadBytes is cls_rgw_lc_obj_head v2 {1729382400, ":b:m", 1729468800}.
	lcHeadBytes = []byte{
		2, 2, 0x18, 0, 0, 0,
		0x00, 0x48, 0x14, 0x67, 0, 0, 0, 0,
		4, 0, 0, 0, ':', 'b', ':', 'm',
		0x80, 0x99, 0x15, 0x67, 0, 0, 0, 0,
	}
	lcHead = rgw.LCHead{StartDate: 1729382400, Marker: ":b:m", ShardRolloverDate: 1729468800}

	// lcEntryBytes is cls_rgw_lc_entry v1 {":b:m", 1729382400, processing}.
	lcEntryBytes = []byte{
		1, 1, 0x14, 0, 0, 0,
		4, 0, 0, 0, ':', 'b', ':', 'm',
		0x00, 0x48, 0x14, 0x67, 0, 0, 0, 0,
		1, 0, 0, 0,
	}
	lcEntry = rgw.LCEntry{Bucket: ":b:m", StartTime: 1729382400, Status: rgw.LCStatusProcessing}

	// lcMarkerBytes is the body get_entry_op and get_next_entry_op share: v1
	// holding the marker ":b:m".
	lcMarkerBytes = []byte{1, 1, 8, 0, 0, 0, 4, 0, 0, 0, ':', 'b', ':', 'm'}

	// lcLegacyPairBytes is an entry carrier at v1, which held the
	// pair<string, int> {":b:m", failed} before the entry existed.
	lcLegacyPairBytes = []byte{1, 1, 0x0c, 0, 0, 0, 4, 0, 0, 0, ':', 'b', ':', 'm', 2, 0, 0, 0}
	lcLegacyPair      = rgw.LCEntry{Bucket: ":b:m", Status: rgw.LCStatusFailed}
)

// lcCarrier wraps an encoded value in a struct header at version v, compat c.
func lcCarrier(v, c uint8, body []byte) []byte {
	return build(func(e *denc.Encoder) {
		f := e.BeginStruct(v, c)
		e.Raw(body)
		e.EndStruct(f)
	})
}

// lcEntryA is a second entry, sorted before lcEntry, for the list replies.
var (
	lcEntryA      = rgw.LCEntry{Bucket: ":a:1"}
	lcEntryABytes = []byte{1, 1, 0x14, 0, 0, 0, 4, 0, 0, 0, ':', 'a', ':', '1', 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	lcComplete    = rgw.LCEntry{Bucket: ":b:m", StartTime: 1729382400, Status: rgw.LCStatusComplete}
	lcCompleteRaw = append(append([]byte{}, lcEntryBytes[:22]...), 3, 0, 0, 0)
)

// lcListV3Bytes is cls_rgw_lc_list_entries_ret v3, compat 1, holding
// lcComplete then lcEntryA, truncated.
func lcListV3Bytes() []byte {
	body := append([]byte{2, 0, 0, 0}, lcCompleteRaw...)
	body = append(body, lcEntryABytes...)
	return lcCarrier(3, 1, append(body, 1))
}

// lcListV3 is the reply lcListV3Bytes encodes.
func lcListV3() rgw.LCListEntriesRet {
	return rgw.LCListEntriesRet{Entries: []rgw.LCEntry{lcComplete, lcEntryA}, Truncated: true, Version: new(uint8(3))}
}

// lcListOpBytes is cls_rgw_lc_list_entries_op v3, compat 1, {":b:m", 100}.
var lcListOpBytes = []byte{3, 1, 0x0c, 0, 0, 0, 4, 0, 0, 0, ':', 'b', ':', 'm', 100, 0, 0, 0}

// lcLegacyMap is the map<string, int> versions 1 and 2 of the list reply
// carry: {":a:1": 0, ":b:m": 3}, in key order.
var lcLegacyMap = []byte{
	2, 0, 0, 0,
	4, 0, 0, 0, ':', 'a', ':', '1', 0, 0, 0, 0,
	4, 0, 0, 0, ':', 'b', ':', 'm', 3, 0, 0, 0,
}

var _ = Describe("the lc types", func() {
	for _, o := range []goldentest.Options{skipJSON, tentacleSkipJSON} {
		It("cls_rgw_lc_entry corpus goldens at "+o.Release.String(), func() {
			goldentest.RoundTrip("testdata", "cls_rgw_lc_entry", o, rgw.DecodeLCEntry, enc[rgw.LCEntry])
		})
		It("cls_rgw_lc_get_entry_ret corpus goldens at "+o.Release.String(), func() {
			goldentest.RoundTrip("testdata", "cls_rgw_lc_get_entry_ret", o, rgw.DecodeLCGetEntryRet, enc[rgw.LCGetEntryRet])
		})
		It("cls_rgw_lc_set_entry_op corpus goldens at "+o.Release.String()+", the pre-16.2 pair included", func() {
			goldentest.RoundTrip("testdata", "cls_rgw_lc_set_entry_op", o, rgw.DecodeLCSetEntryOp, enc[rgw.LCSetEntryOp])
		})
	}

	It("cls_rgw_lc_rm_entry_op corpus objects round-trip byte for byte", func() {
		for _, c := range loadRaw("cls_rgw_lc_rm_entry_op") {
			v := decodeWhole(c.bin, rgw.DecodeLCRmEntryOp)
			Expect(v.Entry.Bucket).To(HavePrefix(":"), "%s: a tenant:bucket:marker key", c.id)
			for _, r := range releases {
				Expect(encodeAt(v, r)).To(Equal(c.bin), "%s at %v", c.id, r)
			}
		}
	})

	DescribeTable("the LC_BUCKET_STATUS values",
		func(got, want uint32) { Expect(got).To(Equal(want)) },
		Entry("lc_uninitial", rgw.LCStatusUninitial, uint32(0)),
		Entry("lc_processing", rgw.LCStatusProcessing, uint32(1)),
		Entry("lc_failed", rgw.LCStatusFailed, uint32(2)),
		Entry("lc_complete", rgw.LCStatusComplete, uint32(3)),
	)

	It("writes an entry's bucket, start time and status in order", func() {
		for _, r := range releases {
			Expect(encodeAt(lcEntry, r)).To(Equal(lcEntryBytes), "%v", r)
		}
		Expect(decodeWhole(lcEntryBytes, rgw.DecodeLCEntry)).To(Equal(lcEntry))
	})

	It("writes the head at version 2 with both dates in 8 bytes", func() {
		for _, r := range releases {
			Expect(encodeAt(lcHead, r)).To(Equal(lcHeadBytes), "%v", r)
		}
		Expect(decodeWhole(lcHeadBytes, rgw.DecodeLCHead)).To(Equal(lcHead))
	})

	It("reads a version 1 head, which has no rollover date", func() {
		b := lcCarrier(1, 1, lcHeadBytes[6:22])
		Expect(decodeWhole(b, rgw.DecodeLCHead)).To(Equal(rgw.LCHead{StartDate: 1729382400, Marker: ":b:m"}))
	})

	It("refuses a head whose compat version is above 2", func() {
		d := denc.NewDecoder(lcCarrier(3, 3, lcHeadBytes[6:]))
		rgw.DecodeLCHead(d)
		Expect(d.Err()).To(MatchError(denc.ErrIncompatible))
	})

	DescribeTable("wraps the entry at version 2, compat 2",
		func(v encoder, dec func([]byte) any) {
			want := lcCarrier(2, 2, lcEntryBytes)
			for _, r := range releases {
				Expect(encodeAt(v, r)).To(Equal(want), "%v", r)
			}
			Expect(dec(want)).To(Equal(v))
		},
		Entry("cls_rgw_lc_set_entry_op", rgw.LCSetEntryOp{Entry: lcEntry},
			func(b []byte) any { return decodeWhole(b, rgw.DecodeLCSetEntryOp) }),
		Entry("cls_rgw_lc_rm_entry_op", rgw.LCRmEntryOp{Entry: lcEntry},
			func(b []byte) any { return decodeWhole(b, rgw.DecodeLCRmEntryOp) }),
		Entry("cls_rgw_lc_get_entry_ret", rgw.LCGetEntryRet{Entry: lcEntry},
			func(b []byte) any { return decodeWhole(b, rgw.DecodeLCGetEntryRet) }),
		Entry("cls_rgw_lc_get_next_entry_ret", rgw.LCGetNextEntryRet{Entry: lcEntry},
			func(b []byte) any { return decodeWhole(b, rgw.DecodeLCGetNextEntryRet) }),
	)

	DescribeTable("reads version 1 of an entry carrier as the legacy pair, its start time zero",
		func(dec func([]byte) rgw.LCEntry) {
			Expect(dec(lcLegacyPairBytes)).To(Equal(lcLegacyPair))
		},
		Entry("cls_rgw_lc_set_entry_op", func(b []byte) rgw.LCEntry { return decodeWhole(b, rgw.DecodeLCSetEntryOp).Entry }),
		Entry("cls_rgw_lc_rm_entry_op", func(b []byte) rgw.LCEntry { return decodeWhole(b, rgw.DecodeLCRmEntryOp).Entry }),
		Entry("cls_rgw_lc_get_next_entry_ret", func(b []byte) rgw.LCEntry { return decodeWhole(b, rgw.DecodeLCGetNextEntryRet).Entry }),
		Entry("cls_rgw_lc_get_entry_ret, once the entry fails to decode", func(b []byte) rgw.LCEntry {
			return decodeWhole(b, rgw.DecodeLCGetEntryRet).Entry
		}),
	)

	It("reads a version 1 get_entry reply that holds an entry as the entry", func() {
		Expect(decodeWhole(lcCarrier(1, 1, lcEntryBytes), rgw.DecodeLCGetEntryRet)).To(Equal(rgw.LCGetEntryRet{Entry: lcEntry}))
	})

	It("fails a version 1 get_entry reply that is neither an entry nor the pair", func() {
		d := denc.NewDecoder(lcCarrier(1, 1, []byte{4, 0, 0, 0, ':', 'b'}))
		rgw.DecodeLCGetEntryRet(d)
		Expect(d.Err()).To(MatchError(denc.ErrShortBuffer))
	})

	DescribeTable("writes the marker alone at version 1",
		func(v encoder, dec func([]byte) any) {
			for _, r := range releases {
				Expect(encodeAt(v, r)).To(Equal(lcMarkerBytes), "%v", r)
			}
			Expect(dec(lcMarkerBytes)).To(Equal(v))
		},
		Entry("cls_rgw_lc_get_entry_op", rgw.LCGetEntryOp{Marker: ":b:m"},
			func(b []byte) any { return decodeWhole(b, rgw.DecodeLCGetEntryOp) }),
		Entry("cls_rgw_lc_get_next_entry_op", rgw.LCGetNextEntryOp{Marker: ":b:m"},
			func(b []byte) any { return decodeWhole(b, rgw.DecodeLCGetNextEntryOp) }),
	)

	DescribeTable("wraps the head at version 1",
		func(v encoder, dec func([]byte) any) {
			want := lcCarrier(1, 1, lcHeadBytes)
			Expect(want[:6]).To(Equal([]byte{1, 1, 0x1e, 0, 0, 0}))
			for _, r := range releases {
				Expect(encodeAt(v, r)).To(Equal(want), "%v", r)
			}
			Expect(dec(want)).To(Equal(v))
		},
		Entry("cls_rgw_lc_put_head_op", rgw.LCPutHeadOp{Head: lcHead},
			func(b []byte) any { return decodeWhole(b, rgw.DecodeLCPutHeadOp) }),
		Entry("cls_rgw_lc_get_head_ret", rgw.LCGetHeadRet{Head: lcHead},
			func(b []byte) any { return decodeWhole(b, rgw.DecodeLCGetHeadRet) }),
	)

	Describe("cls_rgw_lc_list_entries_op", func() {
		It("goes at version 3, compat 1, whatever version a decoded request carried", func() {
			for _, v := range []uint8{0, 1, 3} {
				for _, r := range releases {
					Expect(encodeAt(rgw.LCListEntriesOp{Marker: ":b:m", MaxEntries: 100, Version: v}, r)).To(Equal(lcListOpBytes), "version %d at %v", v, r)
				}
			}
			Expect(decodeWhole(lcListOpBytes, rgw.DecodeLCListEntriesOp)).To(Equal(rgw.LCListEntriesOp{Marker: ":b:m", MaxEntries: 100, Version: 3}))
		})

		It("records the version an older client sent, 0 included, which the class replies at", func() {
			for _, v := range []uint8{0, 1} {
				b := append([]byte{v}, lcListOpBytes[1:]...)
				Expect(decodeWhole(b, rgw.DecodeLCListEntriesOp)).To(Equal(rgw.LCListEntriesOp{Marker: ":b:m", MaxEntries: 100, Version: v}), "version %d", v)
			}
		})
	})

	Describe("cls_rgw_lc_list_entries_ret", func() {
		It("goes at version 3 as a vector of entries, in the order given, then the truncation flag", func() {
			Expect(lcListV3Bytes()[:10]).To(Equal([]byte{3, 1, 0x39, 0, 0, 0, 2, 0, 0, 0}))
			for _, r := range releases {
				Expect(encodeAt(lcListV3(), r)).To(Equal(lcListV3Bytes()), "%v", r)
			}
			Expect(decodeWhole(lcListV3Bytes(), rgw.DecodeLCListEntriesRet)).To(Equal(lcListV3()))
		})

		It("goes at version 3 when no version is set, as the class's default", func() {
			unset := lcListV3()
			unset.Version = nil
			Expect(encodeAt(unset, denc.Squid)).To(Equal(lcListV3Bytes()))
		})

		It("goes at version 0 as the map when a version 0 request sets it, as the class replies to one", func() {
			want := lcCarrier(0, 1, append(append([]byte{}, lcLegacyMap...), 1))
			Expect(want[:6]).To(Equal([]byte{0, 1, 0x1d, 0, 0, 0}))
			v0 := rgw.LCListEntriesRet{Entries: []rgw.LCEntry{lcComplete, lcEntryA}, Truncated: true, Version: new(uint8(0))}
			Expect(encodeAt(v0, denc.Squid)).To(Equal(want))
			Expect(decodeWhole(want, rgw.DecodeLCListEntriesRet)).To(Equal(rgw.LCListEntriesRet{
				Entries: []rgw.LCEntry{lcEntryA, {Bucket: ":b:m", Status: rgw.LCStatusComplete}},
				Version: new(uint8(0)),
			}))
		})

		It("goes at version 2 as the map of bucket to status, keyed in order", func() {
			want := lcCarrier(2, 1, append(append([]byte{}, lcLegacyMap...), 1))
			Expect(want[:6]).To(Equal([]byte{2, 1, 0x1d, 0, 0, 0}))
			v2 := rgw.LCListEntriesRet{Entries: []rgw.LCEntry{lcComplete, lcEntryA}, Truncated: true, Version: new(uint8(2))}
			Expect(encodeAt(v2, denc.Squid)).To(Equal(want))
			Expect(decodeWhole(want, rgw.DecodeLCListEntriesRet)).To(Equal(rgw.LCListEntriesRet{
				Entries:   []rgw.LCEntry{lcEntryA, {Bucket: ":b:m", Status: rgw.LCStatusComplete}},
				Truncated: true, Version: new(uint8(2)),
			}))
		})

		It("keeps the first status for a bucket repeated in a legacy reply, as std::map::insert does", func() {
			v1 := rgw.LCListEntriesRet{Entries: []rgw.LCEntry{lcEntryA, lcComplete, {Bucket: ":a:1", Status: 2}}, Version: new(uint8(1))}
			want := lcCarrier(1, 1, append(append([]byte{}, lcLegacyMap...), 0))
			Expect(encodeAt(v1, denc.Squid)).To(Equal(want))
		})

		It("reads a version 1 reply without a truncation flag, and ignores one written after the map", func() {
			want := rgw.LCListEntriesRet{
				Entries: []rgw.LCEntry{lcEntryA, {Bucket: ":b:m", Status: rgw.LCStatusComplete}},
				Version: new(uint8(1)),
			}
			Expect(decodeWhole(lcCarrier(1, 1, lcLegacyMap), rgw.DecodeLCListEntriesRet)).To(Equal(want))
			Expect(decodeWhole(lcCarrier(1, 1, append(append([]byte{}, lcLegacyMap...), 1)), rgw.DecodeLCListEntriesRet)).To(Equal(want))
		})

		It("keeps the first status of a key a legacy reply repeats, as the class's map decode does", func() {
			m := []byte{
				2, 0, 0, 0,
				4, 0, 0, 0, ':', 'a', ':', '1', 1, 0, 0, 0,
				4, 0, 0, 0, ':', 'a', ':', '1', 2, 0, 0, 0,
			}
			Expect(decodeWhole(lcCarrier(2, 1, append(m, 0)), rgw.DecodeLCListEntriesRet).Entries).
				To(Equal([]rgw.LCEntry{{Bucket: ":a:1", Status: rgw.LCStatusProcessing}}))
		})

		It("refuses a reply whose compat version is above 3", func() {
			d := denc.NewDecoder(lcCarrier(4, 4, []byte{0, 0, 0, 0, 0}))
			rgw.DecodeLCListEntriesRet(d)
			Expect(d.Err()).To(MatchError(denc.ErrIncompatible))
		})
	})
})
