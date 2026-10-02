package rgw_test

import (
	"encoding/binary"
	"encoding/json"
	"slices"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/denc"
)

// The fixtures below are built field by field from the C++ encode bodies,
// never through the Go encoders, for the versions the corpus lacks and for
// the fields it only ever holds at their defaults: the header's tag timeout,
// master version, sync flag, reshard status and Tentacle reshard log count;
// the entry meta's user data, appendable flag and main's restore fields; the
// list marker; removed objects' instances; usage payers, s3select and op
// counters; gc object instances and times, and gc tags ending in the NUL
// radosgw's object tags carry; and the packed-value widths.

func decodeWhole[T any](b []byte, dec func(*denc.Decoder) T) T {
	GinkgoHelper()
	d := denc.NewDecoder(b)
	v := dec(d)
	Expect(d.Err()).NotTo(HaveOccurred())
	Expect(d.Remaining()).To(BeZero())
	return v
}

func encodeAt(v encoder, r denc.Release) []byte {
	e := denc.NewEncoder()
	v.Encode(e, r)
	return e.Bytes()
}

func build(f func(e *denc.Encoder)) []byte {
	e := denc.NewEncoder()
	f(e)
	return e.Bytes()
}

// releases are the releases an encoder must agree on when its type's
// encoding does not change between them.
var releases = []denc.Release{denc.Squid, denc.Tentacle}

var (
	t1 = time.Date(2026, 9, 26, 1, 2, 3, 400005000, time.UTC)
	t2 = time.Date(2025, 1, 2, 3, 4, 5, 6000, time.UTC)
)

// objKey writes cls_rgw_obj_key v1.
func objKey(e *denc.Encoder, name, instance string) {
	f := e.BeginStruct(1, 1)
	e.String(name)
	e.String(instance)
	e.EndStruct(f)
}

// metaFields is every field rgw_bucket_dir_entry_meta writes.
type metaFields struct {
	category                            uint8
	size                                uint64
	mtime                               time.Time
	etag, owner, ownerName, contentType string
	accounted                           uint64
	userData, storageClass              string
	appendable                          bool
	restoreStatus                       uint8
	restoreExpiry                       time.Time
}

// entryMeta writes rgw_bucket_dir_entry_meta at version v: 7 as Squid and
// Tentacle write it, 8 as main does, 2 without compat byte or length.
func entryMeta(e *denc.Encoder, v uint8, m metaFields) {
	var f denc.Frame
	if v < 3 {
		e.U8(v)
	} else {
		f = e.BeginStruct(v, 3)
	}
	e.U8(m.category)
	e.U64(m.size)
	e.Time(m.mtime)
	e.String(m.etag)
	e.String(m.owner)
	e.String(m.ownerName)
	e.String(m.contentType)
	if v >= 4 {
		e.U64(m.accounted)
	}
	if v >= 5 {
		e.String(m.userData)
	}
	if v >= 6 {
		e.String(m.storageClass)
	}
	if v >= 7 {
		e.Bool(m.appendable)
	}
	if v >= 8 {
		e.U8(m.restoreStatus)
		e.Time(m.restoreExpiry)
	}
	if v >= 3 {
		e.EndStruct(f)
	}
}

var fullMeta = metaFields{
	category: rgw.CategoryMultiMeta, size: 1001, mtime: t1, etag: "etag", owner: "owner",
	ownerName: "Owner Name", contentType: "text/plain", accounted: 1002, userData: "user-data",
	storageClass: "COLD", appendable: true, restoreStatus: 2, restoreExpiry: t2,
}

func (m metaFields) value(v uint8) rgw.DirEntryMeta {
	dm := rgw.DirEntryMeta{
		Category: m.category, Size: m.size, Mtime: m.mtime, ETag: m.etag, Owner: m.owner,
		OwnerDisplayName: m.ownerName, ContentType: m.contentType, AccountedSize: m.accounted,
		UserData: m.userData, StorageClass: m.storageClass, AppendableValue: m.appendable,
	}
	if v >= 8 {
		dm.RestoreStatus = m.restoreStatus
		dm.RestoreExpiryDate = m.restoreExpiry
	}
	return dm
}

// pendingInfo writes rgw_bucket_pending_info v2.
func pendingInfo(e *denc.Encoder, state uint8, ts time.Time, op uint8) {
	f := e.BeginStruct(2, 2)
	e.U8(state)
	e.Time(ts)
	e.U8(op)
	e.EndStruct(f)
}

// entryVerBytes writes rgw_bucket_entry_ver v1 around already packed values.
func entryVerBytes(e *denc.Encoder, packed ...byte) {
	f := e.BeginStruct(1, 1)
	e.Raw(packed)
	e.EndStruct(f)
}

// header writes rgw_bucket_dir_header at version 7 or 8 with every field set.
func header(e *denc.Encoder, v, status uint8) {
	f := e.BeginStruct(v, 2)
	e.U32(2)
	e.U8(rgw.CategoryMain)
	cs := e.BeginStruct(3, 2)
	e.U64(10)
	e.U64(4096)
	e.U64(3)
	e.U64(9)
	e.EndStruct(cs)
	e.U8(rgw.CategoryMultiMeta)
	cs = e.BeginStruct(3, 2)
	e.U64(20)
	e.U64(8192)
	e.U64(4)
	e.U64(19)
	e.EndStruct(cs)
	e.U64(600)                // tag_timeout
	e.U64(0x8000000000000001) // ver, dumped as a negative int
	e.U64(42)                 // master_ver
	e.String("max-marker")
	ie := e.BeginStruct(3, 1)
	e.U8(status)
	e.String("")
	e.I32(-1)
	e.EndStruct(ie)
	e.Bool(true) // syncstopped
	if v >= 8 {
		e.U32(7) // reshardlog_entries
	}
	e.EndStruct(f)
}

func headerValue(status uint8, reshardLog uint32) rgw.DirHeader {
	return rgw.DirHeader{
		Ver: 0x8000000000000001, MasterVer: 42,
		Stats: map[uint8]rgw.CategoryStats{
			rgw.CategoryMain:      {TotalSize: 10, TotalSizeRounded: 4096, NumEntries: 3, ActualSize: 9},
			rgw.CategoryMultiMeta: {TotalSize: 20, TotalSizeRounded: 8192, NumEntries: 4, ActualSize: 19},
		},
		MaxMarker: "max-marker", TagTimeout: 600, NewInstance: rgw.InstanceEntry{ReshardStatus: status},
		SyncStopped: true, ReshardLogEntries: reshardLog,
	}
}

var _ = Describe("rgw_bucket_entry_ver packed values", func() {
	DescribeTable("mirror encode_packed_val and decode_packed_val",
		func(v uint64, packed []byte, decoded uint64) {
			b := build(func(e *denc.Encoder) { entryVerBytes(e, append(append([]byte{}, packed...), packed...)...) })
			got := decodeWhole(b, rgw.DecodeEntryVer)
			Expect(got).To(Equal(rgw.EntryVer{Pool: int64(decoded), Epoch: decoded})) //nolint:gosec // test values
			for _, r := range releases {
				Expect(encodeAt(rgw.EntryVer{Pool: int64(v), Epoch: v}, r)).To(Equal(b)) //nolint:gosec // test values
			}
		},
		Entry("0", uint64(0), []byte{0x00}, uint64(0)),
		Entry("0x7f in one byte", uint64(0x7f), []byte{0x7f}, uint64(0x7f)),
		Entry("0x80 as u8", uint64(0x80), []byte{0x81, 0x80}, uint64(0x80)),
		Entry("0xff as u8", uint64(0xff), []byte{0x81, 0xff}, uint64(0xff)),
		Entry("0x100 as u16", uint64(0x100), []byte{0x82, 0x00, 0x01}, uint64(0x100)),
		Entry("0xffff as u16", uint64(0xffff), []byte{0x82, 0xff, 0xff}, uint64(0xffff)),
		Entry("0x10000 truncated to a u16 0, as C++ does", uint64(0x10000), []byte{0x82, 0x00, 0x00}, uint64(0)),
		Entry("0x10001 as u32", uint64(0x10001), []byte{0x84, 0x01, 0x00, 0x01, 0x00}, uint64(0x10001)),
		Entry("0x1000000 as u32", uint64(0x1000000), []byte{0x84, 0x00, 0x00, 0x00, 0x01}, uint64(0x1000000)),
		Entry("0x1000001 as u64", uint64(0x1000001), []byte{0x88, 0x01, 0x00, 0x00, 0x01, 0, 0, 0, 0}, uint64(0x1000001)),
		Entry("0xffffffff as u64", uint64(0xffffffff), []byte{0x88, 0xff, 0xff, 0xff, 0xff, 0, 0, 0, 0}, uint64(0xffffffff)),
		Entry("2^64-1, pool -1, as u64", ^uint64(0), []byte{0x88, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}, ^uint64(0)),
	)

	It("decodes a pool of -1 from its u64 form", func() {
		b := build(func(e *denc.Encoder) {
			entryVerBytes(e, 0x88, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x81, 0x90)
		})
		Expect(decodeWhole(b, rgw.DecodeEntryVer)).To(Equal(rgw.EntryVer{Pool: -1, Epoch: 0x90}))
		Expect(rgw.NewEntryVer()).To(Equal(rgw.EntryVer{Pool: -1}))
	})

	It("rejects a marker byte with no width", func() {
		b := build(func(e *denc.Encoder) { entryVerBytes(e, 0x83, 0x00, 0x00, 0x00, 0x00) })
		d := denc.NewDecoder(b)
		rgw.DecodeEntryVer(d)
		Expect(d.Err()).To(MatchError(denc.ErrMalformed))
	})
})

// fullDirEntry is the entry fullEntry writes.
func fullDirEntry() rgw.DirEntry {
	return rgw.DirEntry{
		Key:    rgw.ObjKey{Name: "key-name", Instance: "key-instance"},
		Ver:    rgw.EntryVer{Pool: 200, Epoch: 0x11223344},
		Exists: true,
		Meta:   fullMeta.value(7),
		PendingMap: []rgw.PendingEntry{
			{Tag: "tag-a", Info: rgw.PendingInfo{State: rgw.PendingDone, Timestamp: t2, Op: 2}},
			{Tag: "tag-b", Info: rgw.PendingInfo{State: rgw.PendingModify, Timestamp: t1, Op: 1}},
			{Tag: "tag-b", Info: rgw.PendingInfo{State: rgw.PendingUnknown, Timestamp: t2, Op: 0}},
		},
		Locator: "locator", IndexVer: 300, Tag: "op-tag",
		Flags: rgw.FlagVer | rgw.FlagCurrent | rgw.FlagCommonPrefix, VersionedEpoch: 77,
	}
}

var _ = Describe("rgw_bucket_dir_entry fixtures", func() {
	// fullEntry writes a v8 entry with every field distinct, in the C++
	// order: key.name, ver.epoch, exists, meta, pending_map, locator, ver,
	// index_ver, tag, key.instance, flags, versioned_epoch.
	fullEntry := func(loneEpoch uint64) []byte {
		return build(func(e *denc.Encoder) {
			f := e.BeginStruct(8, 3)
			e.String("key-name")
			e.U64(loneEpoch)
			e.Bool(true)
			entryMeta(e, 7, fullMeta)
			e.U32(3)
			e.String("tag-a")
			pendingInfo(e, rgw.PendingDone, t2, 2)
			e.String("tag-b")
			pendingInfo(e, rgw.PendingModify, t1, 1)
			e.String("tag-b")
			pendingInfo(e, rgw.PendingUnknown, t2, 0)
			e.String("locator")
			entryVerBytes(e, 0x81, 0xc8, 0x88, 0x44, 0x33, 0x22, 0x11, 0, 0, 0, 0) // pool 200, epoch 0x11223344 as u64
			e.Raw([]byte{0x82, 0x2c, 0x01})                                        // index_ver 300
			e.String("op-tag")
			e.String("key-instance")
			e.U16(rgw.FlagVer | rgw.FlagCurrent | rgw.FlagCommonPrefix)
			e.U64(77)
			e.EndStruct(f)
		})
	}

	It("pins the field order with every field distinct", func() {
		b := fullEntry(0x11223344)
		full := fullDirEntry()
		Expect(decodeWhole(b, rgw.DecodeDirEntry)).To(Equal(full))
		for _, r := range releases {
			Expect(encodeAt(full, r)).To(Equal(b))
		}
	})

	It("takes the epoch from ver, not the leading copy, from version 4", func() {
		Expect(decodeWhole(fullEntry(9), rgw.DecodeDirEntry).Ver.Epoch).To(Equal(uint64(0x11223344)))
	})

	It("keeps repeated pending tags and writes them sorted, repeats in order", func() {
		full := fullDirEntry()
		shuffled := full
		shuffled.PendingMap = []rgw.PendingEntry{full.PendingMap[1], full.PendingMap[2], full.PendingMap[0]}
		Expect(encodeAt(shuffled, denc.Squid)).To(Equal(fullEntry(0x11223344)))
	})

	It("dumps the pending map as key and val pairs", func() {
		b, err := json.Marshal(fullDirEntry())
		Expect(err).NotTo(HaveOccurred())
		Expect(b).To(MatchJSON(`{
			"name": "key-name", "instance": "key-instance",
			"ver": {"pool": 200, "epoch": 287454020},
			"locator": "locator", "exists": true,
			"meta": {"category": 3, "size": 1001, "mtime": "2026-09-26T01:02:03.400005Z",
				"etag": "etag", "storage_class": "COLD", "owner": "owner",
				"owner_display_name": "Owner Name", "content_type": "text/plain",
				"accounted_size": 1002, "user_data": "user-data", "appendable": true},
			"tag": "op-tag", "flags": 32771,
			"pending_map": [
				{"key": "tag-a", "val": {"state": 1, "timestamp": "2025-01-02T03:04:05.000006Z", "op": 2}},
				{"key": "tag-b", "val": {"state": 0, "timestamp": "2026-09-26T01:02:03.400005Z", "op": 1}},
				{"key": "tag-b", "val": {"state": 2, "timestamp": "2025-01-02T03:04:05.000006Z", "op": 0}}
			],
			"versioned_epoch": 77}`))
	})

	It("decodes a version 2 entry, which has no compat byte or length", func() {
		m := fullMeta
		m.contentType = "ct"
		b := build(func(e *denc.Encoder) {
			e.U8(2)
			e.String("old")
			e.U64(12)
			e.Bool(true)
			entryMeta(e, 2, m)
			e.U32(0)
			e.String("loc")
		})
		want := rgw.DirEntry{
			Key: rgw.ObjKey{Name: "old"}, Ver: rgw.EntryVer{Pool: -1, Epoch: 12}, Exists: true,
			Meta: rgw.DirEntryMeta{
				Category: m.category, Size: m.size, Mtime: m.mtime, ETag: m.etag, Owner: m.owner,
				OwnerDisplayName: m.ownerName, ContentType: "ct", AccountedSize: m.size,
			},
			Locator: "loc",
		}
		Expect(decodeWhole(b, rgw.DecodeDirEntry)).To(Equal(want))
		Expect(rgw.NewDirEntry().Ver.Pool).To(Equal(int64(-1)))
	})
})

var _ = Describe("rgw_bucket_dir_entry_meta fixtures", func() {
	It("decodes main's version 8 restore fields and writes version 7 for every release", func() {
		v8 := build(func(e *denc.Encoder) { entryMeta(e, 8, fullMeta) })
		v7 := build(func(e *denc.Encoder) { entryMeta(e, 7, fullMeta) })
		got := decodeWhole(v8, rgw.DecodeDirEntryMeta)
		Expect(got).To(Equal(fullMeta.value(8)))
		for _, r := range releases {
			Expect(encodeAt(got, r)).To(Equal(v7))
		}
		Expect(decodeWhole(v7, rgw.DecodeDirEntryMeta)).To(Equal(fullMeta.value(7)))
	})
})

var _ = Describe("rgw_bucket_dir_header fixtures", func() {
	It("writes version 7 for Squid and version 8 with the reshard log count for Tentacle", func() {
		v7 := build(func(e *denc.Encoder) { header(e, 7, rgw.ReshardInProgress) })
		v8 := build(func(e *denc.Encoder) { header(e, 8, rgw.ReshardInLogRecord) })
		Expect(decodeWhole(v7, rgw.DecodeDirHeader)).To(Equal(headerValue(rgw.ReshardInProgress, 0)))
		Expect(decodeWhole(v8, rgw.DecodeDirHeader)).To(Equal(headerValue(rgw.ReshardInLogRecord, 7)))
		Expect(encodeAt(headerValue(rgw.ReshardInProgress, 0), denc.Squid)).To(Equal(v7))
		Expect(encodeAt(headerValue(rgw.ReshardInLogRecord, 7), denc.Tentacle)).To(Equal(v8))
		sq := build(func(e *denc.Encoder) { header(e, 7, rgw.ReshardInLogRecord) })
		Expect(encodeAt(headerValue(rgw.ReshardInLogRecord, 7), denc.Squid)).To(Equal(sq))
	})

	It("dumps versions as signed integers and stats as alternating category and totals", func() {
		b, err := json.Marshal(headerValue(rgw.ReshardInLogRecord, 7))
		Expect(err).NotTo(HaveOccurred())
		Expect(b).To(MatchJSON(`{
			"ver": -9223372036854775807, "master_ver": 42,
			"stats": [1, {"total_size": 10, "total_size_rounded": 4096, "num_entries": 3, "actual_size": 9},
				3, {"total_size": 20, "total_size_rounded": 8192, "num_entries": 4, "actual_size": 19}],
			"new_instance": {"reshard_status": "in-logrecord"}}`))
	})

	It("keeps the last value of a repeated category, as ceph-dencoder does", func() {
		b := build(func(e *denc.Encoder) {
			f := e.BeginStruct(7, 2)
			e.U32(2)
			for _, n := range []uint64{1, 2} {
				e.U8(rgw.CategoryMain)
				cs := e.BeginStruct(3, 2)
				e.U64(n)
				e.U64(n)
				e.U64(n)
				e.U64(n)
				e.EndStruct(cs)
			}
			for range 3 {
				e.U64(0)
			}
			e.String("")
			ie := e.BeginStruct(3, 1)
			e.U8(0)
			e.String("")
			e.I32(-1)
			e.EndStruct(ie)
			e.Bool(false)
			e.EndStruct(f)
		})
		Expect(decodeWhole(b, rgw.DecodeDirHeader).Stats).To(Equal(map[uint8]rgw.CategoryStats{
			rgw.CategoryMain: {TotalSize: 2, TotalSizeRounded: 2, NumEntries: 2, ActualSize: 2},
		}))
	})
})

var _ = Describe("cls_rgw_bucket_instance_entry fixtures", func() {
	It("decodes version 2, which dropped the instance id and shard count", func() {
		b := build(func(e *denc.Encoder) {
			f := e.BeginStruct(2, 1)
			e.U8(rgw.ReshardDone)
			e.EndStruct(f)
		})
		Expect(decodeWhole(b, rgw.DecodeInstanceEntry)).To(Equal(rgw.InstanceEntry{ReshardStatus: rgw.ReshardDone}))
	})

	It("discards a version 1 instance id and writes it back empty", func() {
		v1 := build(func(e *denc.Encoder) {
			f := e.BeginStruct(1, 1)
			e.U8(rgw.ReshardInProgress)
			e.String("new-instance-id")
			e.I32(11)
			e.EndStruct(f)
		})
		v3 := build(func(e *denc.Encoder) {
			f := e.BeginStruct(3, 1)
			e.U8(rgw.ReshardInProgress)
			e.String("")
			e.I32(-1)
			e.EndStruct(f)
		})
		got := decodeWhole(v1, rgw.DecodeInstanceEntry)
		for _, r := range releases {
			Expect(encodeAt(got, r)).To(Equal(v3))
		}
	})
})

var _ = Describe("rgw_cls_list_ret fixtures", func() {
	It("round-trips a marker with an instance", func() {
		b := build(func(e *denc.Encoder) {
			f := e.BeginStruct(4, 2)
			d := e.BeginStruct(2, 2)
			header(e, 7, rgw.ReshardNone)
			e.U32(0)
			e.EndStruct(d)
			e.Bool(true)
			objKey(e, "next", "inst")
			e.EndStruct(f)
		})
		got := decodeWhole(b, rgw.DecodeListRet)
		Expect(got.IsTruncated).To(BeTrue())
		Expect(got.Marker).To(Equal(rgw.ObjKey{Name: "next", Instance: "inst"}))
		Expect(got.Dir.Header).To(Equal(headerValue(rgw.ReshardNone, 0)))
		Expect(encodeAt(got, denc.Squid)).To(Equal(b))
	})
})

var _ = Describe("rgw_cls_check_index_ret fixtures", func() {
	It("round-trips two distinct headers per release", func() {
		for _, r := range releases {
			v := uint8(7)
			if r == denc.Tentacle {
				v = 8
			}
			b := build(func(e *denc.Encoder) {
				f := e.BeginStruct(1, 1)
				header(e, v, rgw.ReshardNone)
				header(e, v, rgw.ReshardDone)
				e.EndStruct(f)
			})
			got := decodeWhole(b, rgw.DecodeCheckIndexRet)
			Expect(got.CalculatedHeader.NewInstance.ReshardStatus).To(Equal(rgw.ReshardDone))
			Expect(encodeAt(got, r)).To(Equal(b))
		}
	})
})

var _ = Describe("rgw_cls_obj_complete_op fixtures", func() {
	It("round-trips removed objects with instances and the full meta", func() {
		b := build(func(e *denc.Encoder) {
			f := e.BeginStruct(9, 7)
			e.U8(uint8(rgw.OpDel))
			e.U64(0x10001)
			entryMeta(e, 7, fullMeta)
			e.String("op-tag")
			e.String("loc")
			e.U32(2)
			objKey(e, "rm1", "i1")
			objKey(e, "rm2", "")
			entryVerBytes(e, 0x03, 0x84, 0x01, 0x00, 0x01, 0x00)
			e.Bool(true)
			objKey(e, "obj", "v1")
			e.U16(rgw.BILogFlagVersionedOp | rgw.BILogNullVersion)
			e.U32(2)
			e.String("zone-a")
			e.String("zone-b:key")
			e.EndStruct(f)
		})
		want := rgw.CompleteOp{
			Op: rgw.OpDel, Key: rgw.ObjKey{Name: "obj", Instance: "v1"}, Locator: "loc",
			Ver: rgw.EntryVer{Pool: 3, Epoch: 0x10001}, Meta: fullMeta.value(7), Tag: "op-tag", LogOp: true,
			BILogFlags: rgw.BILogFlagVersionedOp | rgw.BILogNullVersion,
			RemoveObjs: []rgw.ObjKey{{Name: "rm1", Instance: "i1"}, {Name: "rm2"}},
			ZonesTrace: []string{"zone-a", "zone-b:key"},
		}
		Expect(decodeWhole(b, rgw.DecodeCompleteOp)).To(Equal(want))
		for _, r := range releases {
			Expect(encodeAt(want, r)).To(Equal(b))
		}
	})
})

var _ = Describe("rgw_zone_set ordering", func() {
	It("orders entries by zone, an absent location key first, and drops duplicates", func() {
		p := rgw.PrepareOp{ZonesTrace: []string{"b", "a-b", "a:x", "a", "a:x", "a:"}}
		b := build(func(e *denc.Encoder) {
			f := e.BeginStruct(7, 5)
			e.U8(0)
			e.String("")
			e.String("")
			e.Bool(false)
			objKey(e, "", "")
			e.U16(0)
			e.U32(5)
			for _, z := range []string{"a", "a:", "a:x", "a-b", "b"} {
				e.String(z)
			}
			e.EndStruct(f)
		})
		Expect(encodeAt(p, denc.Squid)).To(Equal(b))
		Expect(decodeWhole(b, rgw.DecodePrepareOp).ZonesTrace).To(Equal([]string{"a", "a:", "a:x", "a-b", "b"}))
	})
})

var _ = Describe("rgw_cls_obj_check_mtime fixtures", func() {
	It("round-trips version 2 and decodes version 1 without the precision flag", func() {
		v2 := build(func(e *denc.Encoder) {
			f := e.BeginStruct(2, 1)
			e.Time(t1)
			e.U8(uint8(rgw.MtimeGE))
			e.Bool(true)
			e.EndStruct(f)
		})
		v1 := build(func(e *denc.Encoder) {
			f := e.BeginStruct(1, 1)
			e.Time(t2)
			e.U8(uint8(rgw.MtimeLT))
			e.EndStruct(f)
		})
		want := rgw.CheckMtimeOp{Mtime: t1, Type: rgw.MtimeGE, HighPrecisionTime: true}
		Expect(decodeWhole(v2, rgw.DecodeCheckMtimeOp)).To(Equal(want))
		for _, r := range releases {
			Expect(encodeAt(want, r)).To(Equal(v2))
		}
		Expect(decodeWhole(v1, rgw.DecodeCheckMtimeOp)).To(Equal(rgw.CheckMtimeOp{Mtime: t2, Type: rgw.MtimeLT}))
	})
})

// usageEntry writes rgw_usage_log_entry at version v with every field set.
func usageEntry(e *denc.Encoder, v uint8, owner, payer string) {
	f := e.BeginStruct(v, 1)
	e.String(owner)
	e.String("bucket")
	e.U64(3600)
	e.U64(1)
	e.U64(2)
	e.U64(3)
	e.U64(4)
	if v >= 2 {
		e.U32(1)
		e.String("put_obj")
		u := e.BeginStruct(1, 1)
		e.U64(1)
		e.U64(2)
		e.U64(3)
		e.U64(4)
		e.EndStruct(u)
	}
	if v >= 3 {
		e.String(payer)
	}
	if v >= 4 {
		s := e.BeginStruct(1, 1)
		e.U64(5)
		e.U64(6)
		e.EndStruct(s)
	}
	e.EndStruct(f)
}

// total is the counters usageEntry writes, as the total and as put_obj.
var total = rgw.UsageData{BytesSent: 1, BytesReceived: 2, Ops: 3, SuccessfulOps: 4}

var _ = Describe("rgw_usage_log_entry fixtures", func() {
	It("round-trips a payer, the s3select counters and every op counter", func() {
		b := build(func(e *denc.Encoder) { usageEntry(e, 4, "t1$owner", "payer") })
		want := rgw.UsageLogEntry{
			Owner: "t1$owner", Payer: "payer", Bucket: "bucket", Epoch: 3600, TotalUsage: total,
			UsageMap:      map[string]rgw.UsageData{"put_obj": total},
			S3SelectUsage: rgw.S3SelectUsage{BytesProcessed: 5, BytesReturned: 6},
		}
		Expect(decodeWhole(b, rgw.DecodeUsageLogEntry)).To(Equal(want))
		for _, r := range releases {
			Expect(encodeAt(want, r)).To(Equal(b))
		}
	})

	It("files a version 1 total under the empty category", func() {
		b := build(func(e *denc.Encoder) { usageEntry(e, 1, "owner", "") })
		got := decodeWhole(b, rgw.DecodeUsageLogEntry)
		Expect(got.UsageMap).To(Equal(map[string]rgw.UsageData{"": total}))
		Expect(got.Payer).To(BeEmpty())
	})

	DescribeTable("canonicalizes users as rgw_user::from_str then to_str does",
		func(in, want string) {
			b := build(func(e *denc.Encoder) { usageEntry(e, 4, in, in) })
			got := decodeWhole(b, rgw.DecodeUsageLogEntry)
			Expect(got.Owner).To(Equal(want))
			Expect(got.Payer).To(Equal(want))
			reenc := build(func(e *denc.Encoder) { usageEntry(e, 4, want, want) })
			Expect(encodeAt(rgw.UsageLogEntry{
				Owner: in, Payer: in, Bucket: "bucket", Epoch: 3600, TotalUsage: total,
				UsageMap:      map[string]rgw.UsageData{"put_obj": total},
				S3SelectUsage: rgw.S3SelectUsage{BytesProcessed: 5, BytesReturned: 6},
			}, denc.Squid)).To(Equal(reenc))
		},
		Entry("a plain id", "alice", "alice"),
		Entry("a tenant", "t1$alice", "t1$alice"),
		Entry("a tenant and namespace", "t1$ns$alice", "t1$ns$alice"),
		Entry("a namespace alone", "$ns$alice", "$ns$alice"),
		Entry("an empty tenant", "$alice", "alice"),
		Entry("an empty namespace", "t1$$alice", "t1$alice"),
	)
})

var _ = Describe("rgw_cls_usage_log_read_ret fixtures", func() {
	It("writes entries in user then bucket order", func() {
		keys := []rgw.UserBucket{{User: "a", Bucket: "z"}, {User: "b", Bucket: "a"}}
		b := build(func(e *denc.Encoder) {
			f := e.BeginStruct(1, 1)
			e.U32(2)
			for _, k := range keys {
				ub := e.BeginStruct(1, 1)
				e.String(k.User)
				e.String(k.Bucket)
				e.EndStruct(ub)
				usageEntry(e, 4, k.User, "")
			}
			e.Bool(true)
			e.String("next")
			e.EndStruct(f)
		})
		got := decodeWhole(b, rgw.DecodeUsageReadRet)
		Expect(got.Usage).To(HaveLen(2))
		Expect(got.Usage[keys[1]].Owner).To(Equal("b"))
		Expect(got.Usage[keys[1]].S3SelectUsage.BytesReturned).To(Equal(uint64(6)))
		Expect(got.Truncated).To(BeTrue())
		Expect(got.NextIter).To(Equal("next"))
		Expect(encodeAt(got, denc.Squid)).To(Equal(b))
	})
})

// A repeated map key keeps its last value wherever the key or value lacks
// denc traits: ceph-dencoder v19.2.6 and v20.2.4 dump the last value for a
// repeated rgw_bucket_dir_header category, rgw_bucket_dir entry and
// rgw_usage_log_entry usage category, each built with two values.
var _ = Describe("repeated map keys", func() {
	It("keep the last usage_map category", func() {
		b := build(func(e *denc.Encoder) {
			f := e.BeginStruct(4, 1)
			e.String("owner")
			e.String("bucket")
			e.U64(0)
			for range 4 {
				e.U64(0)
			}
			e.U32(2)
			for _, n := range []uint64{1, 2} {
				e.String("put_obj")
				u := e.BeginStruct(1, 1)
				for range 4 {
					e.U64(n)
				}
				e.EndStruct(u)
			}
			e.String("")
			s := e.BeginStruct(1, 1)
			e.U64(0)
			e.U64(0)
			e.EndStruct(s)
			e.EndStruct(f)
		})
		Expect(decodeWhole(b, rgw.DecodeUsageLogEntry).UsageMap).To(Equal(map[string]rgw.UsageData{
			"put_obj": {BytesSent: 2, BytesReceived: 2, Ops: 2, SuccessfulOps: 2},
		}))
	})

	It("keep the last usage read entry", func() {
		b := build(func(e *denc.Encoder) {
			f := e.BeginStruct(1, 1)
			e.U32(2)
			for _, owner := range []string{"first", "second"} {
				ub := e.BeginStruct(1, 1)
				e.String("u")
				e.String("b")
				e.EndStruct(ub)
				usageEntry(e, 4, owner, "")
			}
			e.Bool(false)
			e.String("")
			e.EndStruct(f)
		})
		got := decodeWhole(b, rgw.DecodeUsageReadRet).Usage
		Expect(got).To(HaveLen(1))
		Expect(got[rgw.UserBucket{User: "u", Bucket: "b"}].Owner).To(Equal("second"))
	})

	It("keep the last dir entry", func() {
		b := build(func(e *denc.Encoder) {
			f := e.BeginStruct(2, 2)
			header(e, 7, rgw.ReshardNone)
			e.U32(2)
			for _, tag := range []string{"first", "second"} {
				e.String("k")
				en := e.BeginStruct(8, 3)
				e.String("k")
				e.U64(0)
				e.Bool(true)
				entryMeta(e, 7, fullMeta)
				e.U32(0)
				e.String("")
				entryVerBytes(e, 0x01, 0x02)
				e.U8(0)
				e.String(tag)
				e.String("")
				e.U16(0)
				e.U64(0)
				e.EndStruct(en)
			}
			e.EndStruct(f)
		})
		got := decodeWhole(b, rgw.DecodeDir).Entries
		Expect(got).To(HaveLen(1))
		Expect(got["k"].Tag).To(Equal("second"))
	})
})

var _ = Describe("gc fixtures", func() {
	It("round-trips an object with an instance and an entry with a time", func() {
		b := build(func(e *denc.Encoder) {
			f := e.BeginStruct(1, 1)
			e.U32(3600)
			info := e.BeginStruct(1, 1)
			e.String("gc-tag")
			chain := e.BeginStruct(1, 1)
			e.U32(1)
			o := e.BeginStruct(2, 1)
			e.String("default.rgw.buckets.data")
			e.String("oid")
			e.String("loc")
			objKey(e, "oid", "inst")
			e.EndStruct(o)
			e.EndStruct(chain)
			e.Time(t1)
			e.EndStruct(info)
			e.EndStruct(f)
		})
		want := rgw.GCSetEntryOp{ExpirationSecs: 3600, Info: rgw.GCObjInfo{
			Tag:   "gc-tag",
			Chain: []rgw.GCObj{{Pool: "default.rgw.buckets.data", Key: rgw.ObjKey{Name: "oid", Instance: "inst"}, Loc: "loc"}},
			Time:  t1,
		}}
		Expect(decodeWhole(b, rgw.DecodeGCSetEntryOp)).To(Equal(want))
		for _, r := range releases {
			Expect(encodeAt(want, r)).To(Equal(b))
		}
	})

	It("takes the name alone from a version 1 object", func() {
		b := build(func(e *denc.Encoder) {
			o := e.BeginStruct(1, 1)
			e.String("pool")
			e.String("oid")
			e.String("loc")
			e.EndStruct(o)
		})
		Expect(decodeWhole(b, rgw.DecodeGCObj)).To(Equal(rgw.GCObj{Pool: "pool", Key: rgw.ObjKey{Name: "oid"}, Loc: "loc"}))
	})

	It("writes cls_rgw_gc_remove_op's tags as a counted list of strings", func() {
		b := build(func(e *denc.Encoder) {
			e.U8(1)   // struct_v
			e.U8(1)   // struct_compat
			e.U32(19) // length
			e.U32(2)
			e.String("tag-a\x00")
			e.String("b")
		})
		want := rgw.GCRemoveOp{Tags: []string{"tag-a\x00", "b"}}
		Expect(decodeWhole(b, rgw.DecodeGCRemoveOp)).To(Equal(want))
		for _, r := range releases {
			Expect(encodeAt(want, r)).To(Equal(b))
		}
	})

	It("writes cls_rgw_gc_defer_entry_op's expiration before its tag", func() {
		b := build(func(e *denc.Encoder) {
			e.U8(1)   // struct_v
			e.U8(1)   // struct_compat
			e.U32(14) // length
			e.U32(3600)
			e.String("gc-tag")
		})
		want := rgw.GCDeferEntryOp{ExpirationSecs: 3600, Tag: "gc-tag"}
		Expect(decodeWhole(b, rgw.DecodeGCDeferEntryOp)).To(Equal(want))
		for _, r := range releases {
			Expect(encodeAt(want, r)).To(Equal(b))
		}

		mutated := slices.Clone(b)
		binary.LittleEndian.PutUint32(mutated[6:], 0x01020304)
		Expect(decodeWhole(mutated, rgw.DecodeGCDeferEntryOp)).To(Equal(rgw.GCDeferEntryOp{ExpirationSecs: 0x01020304, Tag: "gc-tag"}))
		Expect(encodeAt(rgw.GCDeferEntryOp{ExpirationSecs: 0x01020304, Tag: "gc-tag"}, denc.Squid)).To(Equal(mutated))
	})
})
