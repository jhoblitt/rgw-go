package rgw_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/denc/goldentest"
)

// olhLogEntry writes rgw_bucket_olh_log_entry v1 from its C++ encode body.
func olhLogEntry(e *denc.Encoder, epoch uint64, op uint8, opTag, name, instance string, deleteMarker bool) {
	f := e.BeginStruct(1, 1)
	e.U64(epoch)
	e.U8(op)
	e.String(opTag)
	objKey(e, name, instance)
	e.Bool(deleteMarker)
	e.EndStruct(f)
}

var _ = Describe("the OLH types", func() {
	for _, o := range []goldentest.Options{skipJSON, tentacleSkipJSON} {
		It("rgw_bucket_olh_log_entry corpus goldens at "+o.Release.String(), func() {
			goldentest.RoundTrip("testdata", "rgw_bucket_olh_log_entry", o, rgw.DecodeOLHLogEntry, enc[rgw.OLHLogEntry])
		})

		It("rgw_bucket_olh_entry corpus goldens at "+o.Release.String(), func() {
			goldentest.RoundTrip("testdata", "rgw_bucket_olh_entry", o, rgw.DecodeOLHEntry, enc[rgw.OLHEntry])
		})
	}

	DescribeTable("OLHLogOp carries the class's values",
		func(op rgw.OLHLogOp, want uint8) {
			Expect(uint8(op)).To(Equal(want))
		},
		Entry("CLS_RGW_OLH_OP_UNKNOWN", rgw.OLHOpUnknown, uint8(0)),
		Entry("CLS_RGW_OLH_OP_LINK_OLH", rgw.OLHOpLinkOLH, uint8(1)),
		Entry("CLS_RGW_OLH_OP_UNLINK_OLH", rgw.OLHOpUnlinkOLH, uint8(2)),
		Entry("CLS_RGW_OLH_OP_REMOVE_INSTANCE", rgw.OLHOpRemoveInstance, uint8(3)),
	)

	It("writes a log entry's op as one byte between the epoch and the op tag", func() {
		v := rgw.OLHLogEntry{
			Epoch: 0x0102030405060708, Op: rgw.OLHOpRemoveInstance, OpTag: "tag",
			Key: rgw.ObjKey{Name: "obj", Instance: "inst"}, DeleteMarker: true,
		}
		want := build(func(e *denc.Encoder) { olhLogEntry(e, 0x0102030405060708, 3, "tag", "obj", "inst", true) })
		for _, r := range releases {
			Expect(encodeAt(v, r)).To(Equal(want), "%v", r)
		}
		Expect(decodeWhole(want, rgw.DecodeOLHLogEntry)).To(Equal(v))
	})

	// The two entry specs below give delete_marker, exists and
	// pending_removal values that differ pairwise across them, so a flag
	// written in another flag's place changes the bytes.
	It("writes an OLH entry with its pending log in epoch order", func() {
		v := rgw.OLHEntry{
			Key: rgw.ObjKey{Name: "obj", Instance: "cur"}, Epoch: 9,
			PendingLog: map[uint64][]rgw.OLHLogEntry{
				9: {{Epoch: 9, Op: rgw.OLHOpUnlinkOLH, OpTag: "b", Key: rgw.ObjKey{Name: "obj"}}},
				3: {
					{Epoch: 3, Op: rgw.OLHOpLinkOLH, OpTag: "a", Key: rgw.ObjKey{Name: "obj", Instance: "cur"}},
					{Epoch: 3, Op: rgw.OLHOpRemoveInstance, OpTag: "a", Key: rgw.ObjKey{Name: "obj", Instance: "old"}},
				},
			},
			Tag: "olh-tag\x00", Exists: true,
		}
		want := build(func(e *denc.Encoder) {
			f := e.BeginStruct(1, 1)
			objKey(e, "obj", "cur")
			e.Bool(false)
			e.U64(9)
			e.U32(2)
			e.U64(3)
			e.U32(2)
			olhLogEntry(e, 3, 1, "a", "obj", "cur", false)
			olhLogEntry(e, 3, 3, "a", "obj", "old", false)
			e.U64(9)
			e.U32(1)
			olhLogEntry(e, 9, 2, "b", "obj", "", false)
			e.String("olh-tag\x00")
			e.Bool(true)
			e.Bool(false)
			e.EndStruct(f)
		})
		for _, r := range releases {
			Expect(encodeAt(v, r)).To(Equal(want), "%v", r)
		}
		Expect(decodeWhole(want, rgw.DecodeOLHEntry)).To(Equal(v))
	})

	It("keeps the last log of an epoch the pending log repeats, as the class's map decode does", func() {
		b := build(func(e *denc.Encoder) {
			f := e.BeginStruct(1, 1)
			objKey(e, "obj", "")
			e.Bool(true)
			e.U64(4)
			e.U32(2)
			e.U64(4)
			e.U32(1)
			olhLogEntry(e, 4, 1, "first", "obj", "a", false)
			e.U64(4)
			e.U32(1)
			olhLogEntry(e, 4, 1, "second", "obj", "b", false)
			e.String("")
			e.Bool(true)
			e.Bool(false)
			e.EndStruct(f)
		})
		Expect(decodeWhole(b, rgw.DecodeOLHEntry)).To(Equal(rgw.OLHEntry{
			Key: rgw.ObjKey{Name: "obj"}, DeleteMarker: true, Epoch: 4,
			PendingLog: map[uint64][]rgw.OLHLogEntry{
				4: {{Epoch: 4, Op: rgw.OLHOpLinkOLH, OpTag: "second", Key: rgw.ObjKey{Name: "obj", Instance: "b"}}},
			},
			Exists: true,
		}))
	})

	It("refuses an OLH entry whose compat version is above 1", func() {
		b := build(func(e *denc.Encoder) {
			f := e.BeginStruct(2, 2)
			objKey(e, "obj", "")
			e.EndStruct(f)
		})
		d := denc.NewDecoder(b)
		rgw.DecodeOLHEntry(d)
		Expect(d.Err()).To(MatchError(denc.ErrIncompatible))
	})

	It("refuses an OLH log entry whose compat version is above 1", func() {
		b := build(func(e *denc.Encoder) {
			f := e.BeginStruct(2, 2)
			e.U64(1)
			e.EndStruct(f)
		})
		d := denc.NewDecoder(b)
		rgw.DecodeOLHLogEntry(d)
		Expect(d.Err()).To(MatchError(denc.ErrIncompatible))
	})
})
