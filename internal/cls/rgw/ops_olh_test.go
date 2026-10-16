package rgw_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/denc/goldentest"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// The class registers bucket_read_olh_log RD and the other OLH methods
// RD|WR, so the read is added to a read op and the rest to any op.
var (
	_ func(radosclient.Execer, rgw.LinkOLHOp, denc.Release)                           = rgw.LinkOLH
	_ func(radosclient.Execer, rgw.UnlinkInstanceOp, denc.Release)                    = rgw.UnlinkInstance
	_ func(*radosclient.ReadOp, rgw.ReadOLHLogOp, denc.Release) *rgw.ReadOLHLogResult = rgw.ReadOLHLog
	_ func(radosclient.Execer, rgw.TrimOLHLogOp, denc.Release)                        = rgw.TrimOLHLog
	_ func(radosclient.Execer, rgw.ClearOLHOp, denc.Release)                          = rgw.ClearOLH
)

// tentacleSkipJSON checks re-encodings for Tentacle against the v19 goldens,
// for the types v20.2.4's ceph-dencoder re-encodes to the same bytes.
var tentacleSkipJSON = goldentest.Options{Release: denc.Tentacle, SkipJSON: true}

// linkHead writes rgw_cls_link_olh_op's fields up to bilog_flags, which no
// version changes. delete_marker and log_op differ, and high_precision_time
// differs from delete_marker in one spec and from log_op in another, so a
// bool written in another bool's place changes the bytes.
func linkHead(e *denc.Encoder) {
	objKey(e, "obj", "inst")
	e.String("tag\x00")
	e.Bool(true)
	e.String("op-tag")
	entryMeta(e, 7, fullMeta)
	e.U64(5)
	e.Bool(false)
	e.U16(rgw.BILogNullVersion)
}

// linkHeadValue is the LinkOLHOp linkHead writes.
func linkHeadValue() rgw.LinkOLHOp {
	return rgw.LinkOLHOp{
		Key: rgw.ObjKey{Name: "obj", Instance: "inst"}, OLHTag: "tag\x00", DeleteMarker: true,
		OpTag: "op-tag", Meta: fullMeta.value(7), OLHEpoch: 5, BILogFlags: rgw.BILogNullVersion,
	}
}

var _ = Describe("the OLH request and reply goldens", func() {
	for _, o := range []goldentest.Options{skipJSON, tentacleSkipJSON} {
		It("rgw_cls_link_olh_op at "+o.Release.String(), func() {
			goldentest.RoundTrip("testdata", "rgw_cls_link_olh_op", o, rgw.DecodeLinkOLHOp, enc[rgw.LinkOLHOp])
		})
		It("rgw_cls_unlink_instance_op at "+o.Release.String(), func() {
			goldentest.RoundTrip("testdata", "rgw_cls_unlink_instance_op", o, rgw.DecodeUnlinkInstanceOp, enc[rgw.UnlinkInstanceOp])
		})
		It("rgw_cls_read_olh_log_op at "+o.Release.String(), func() {
			goldentest.RoundTrip("testdata", "rgw_cls_read_olh_log_op", o, rgw.DecodeReadOLHLogOp, enc[rgw.ReadOLHLogOp])
		})
		It("rgw_cls_read_olh_log_ret at "+o.Release.String(), func() {
			goldentest.RoundTrip("testdata", "rgw_cls_read_olh_log_ret", o, rgw.DecodeReadOLHLogRet, enc[rgw.ReadOLHLogRet])
		})
		It("rgw_cls_trim_olh_log_op at "+o.Release.String(), func() {
			goldentest.RoundTrip("testdata", "rgw_cls_trim_olh_log_op", o, rgw.DecodeTrimOLHLogOp, enc[rgw.TrimOLHLogOp])
		})
		It("rgw_cls_bucket_clear_olh_op at "+o.Release.String(), func() {
			goldentest.RoundTrip("testdata", "rgw_cls_bucket_clear_olh_op", o, rgw.DecodeClearOLHOp, enc[rgw.ClearOLHOp])
		})
	}
})

var _ = Describe("the OLH methods", func() {
	It("bucket_link_olh sends every corpus request on a write op", func() {
		eachGolden("rgw_cls_link_olh_op", "bucket_link_olh", rgw.DecodeLinkOLHOp,
			func(op *radosclient.WriteOp, v rgw.LinkOLHOp) { rgw.LinkOLH(op, v, denc.Squid) })
	})

	It("bucket_unlink_instance sends every corpus request on a write op", func() {
		eachGolden("rgw_cls_unlink_instance_op", "bucket_unlink_instance", rgw.DecodeUnlinkInstanceOp,
			func(op *radosclient.WriteOp, v rgw.UnlinkInstanceOp) { rgw.UnlinkInstance(op, v, denc.Squid) })
	})

	It("bucket_read_olh_log sends every corpus request on a read op", func() {
		for _, c := range goldens("rgw_cls_read_olh_log_op") {
			op := radosclient.NewReadOp()
			rgw.ReadOLHLog(op, decodeWhole(c.Bin, rgw.DecodeReadOLHLogOp), denc.Squid)
			Expect(soleExec(op.Steps(), "bucket_read_olh_log").In).To(Equal(c.ReEnc), "%s/%s", c.Archive, c.Name)
		}
	})

	It("bucket_trim_olh_log sends every corpus request on a write op", func() {
		eachGolden("rgw_cls_trim_olh_log_op", "bucket_trim_olh_log", rgw.DecodeTrimOLHLogOp,
			func(op *radosclient.WriteOp, v rgw.TrimOLHLogOp) { rgw.TrimOLHLog(op, v, denc.Squid) })
	})

	It("bucket_clear_olh sends every corpus request on a write op", func() {
		eachGolden("rgw_cls_bucket_clear_olh_op", "bucket_clear_olh", rgw.DecodeClearOLHOp,
			func(op *radosclient.WriteOp, v rgw.ClearOLHOp) { rgw.ClearOLH(op, v, denc.Squid) })
	})
})

var _ = Describe("bucket_link_olh's request", func() {
	It("writes unmod_since twice, as to_time_t seconds and as real_time, and the tag's bytes whole", func() {
		l := linkHeadValue()
		l.UnmodSince = t1
		l.HighPrecisionTime = true
		l.ZonesTrace = []string{"zone"}
		want := build(func(e *denc.Encoder) {
			f := e.BeginStruct(5, 1)
			linkHead(e)
			e.U64(uint64(t1.Unix()))
			e.Time(t1)
			e.Bool(true)
			e.U32(1)
			e.String("zone")
			e.EndStruct(f)
		})
		for _, r := range releases {
			op := radosclient.NewWriteOp()
			rgw.LinkOLH(op, l, r)
			Expect(soleExec(op.Steps(), "bucket_link_olh").In).To(Equal(want), "%v", r)
		}
		Expect(decodeWhole(want, rgw.DecodeLinkOLHOp)).To(Equal(l))
	})

	It("writes an unset unmod_since as zero seconds both times", func() {
		want := build(func(e *denc.Encoder) {
			f := e.BeginStruct(5, 1)
			linkHead(e)
			e.U64(0)
			e.U32(0)
			e.U32(0)
			e.Bool(false)
			e.U32(0)
			e.EndStruct(f)
		})
		Expect(encodeAt(linkHeadValue(), denc.Squid)).To(Equal(want))
		Expect(decodeWhole(want, rgw.DecodeLinkOLHOp)).To(Equal(linkHeadValue()))
	})

	It("reads a version 2 request's seconds as unmod_since", func() {
		b := build(func(e *denc.Encoder) {
			f := e.BeginStruct(2, 1)
			linkHead(e)
			e.U64(1700000000)
			e.EndStruct(f)
		})
		want := linkHeadValue()
		want.UnmodSince = time.Unix(1700000000, 0).UTC()
		Expect(decodeWhole(b, rgw.DecodeLinkOLHOp)).To(Equal(want))
	})

	It("reads a version 3 request's real_time and skips its seconds", func() {
		b := build(func(e *denc.Encoder) {
			f := e.BeginStruct(3, 1)
			linkHead(e)
			e.U64(12345)
			e.Time(t1)
			e.EndStruct(f)
		})
		want := linkHeadValue()
		want.UnmodSince = t1
		Expect(decodeWhole(b, rgw.DecodeLinkOLHOp)).To(Equal(want))
	})

	It("reads a version 4 request's high precision flag, before zones_trace existed", func() {
		b := build(func(e *denc.Encoder) {
			f := e.BeginStruct(4, 1)
			linkHead(e)
			e.U64(uint64(t2.Unix()))
			e.Time(t2)
			e.Bool(true)
			e.EndStruct(f)
		})
		want := linkHeadValue()
		want.UnmodSince = t2
		want.HighPrecisionTime = true
		Expect(decodeWhole(b, rgw.DecodeLinkOLHOp)).To(Equal(want))
	})
})

var _ = Describe("bucket_unlink_instance's request", func() {
	It("writes the key, op tag, epoch, log flag, bilog flags, OLH tag and zones in that order", func() {
		u := rgw.UnlinkInstanceOp{
			Key: rgw.ObjKey{Name: "obj", Instance: "inst"}, OpTag: "op-tag", OLHEpoch: 7, LogOp: true,
			BILogFlags: rgw.BILogFlagVersionedOp | rgw.BILogNullVersion, OLHTag: "olh-tag\x00", ZonesTrace: []string{"z1"},
		}
		want := build(func(e *denc.Encoder) {
			f := e.BeginStruct(3, 1)
			objKey(e, "obj", "inst")
			e.String("op-tag")
			e.U64(7)
			e.Bool(true)
			e.U16(3)
			e.String("olh-tag\x00")
			e.U32(1)
			e.String("z1")
			e.EndStruct(f)
		})
		for _, r := range releases {
			op := radosclient.NewWriteOp()
			rgw.UnlinkInstance(op, u, r)
			Expect(soleExec(op.Steps(), "bucket_unlink_instance").In).To(Equal(want), "%v", r)
		}
		Expect(decodeWhole(want, rgw.DecodeUnlinkInstanceOp)).To(Equal(u))
	})

	It("reads a version 2 request's OLH tag, before zones_trace existed", func() {
		b := build(func(e *denc.Encoder) {
			f := e.BeginStruct(2, 1)
			objKey(e, "obj", "inst")
			e.String("op-tag")
			e.U64(7)
			e.Bool(false)
			e.U16(0)
			e.String("olh-tag")
			e.EndStruct(f)
		})
		Expect(decodeWhole(b, rgw.DecodeUnlinkInstanceOp)).To(Equal(rgw.UnlinkInstanceOp{
			Key: rgw.ObjKey{Name: "obj", Instance: "inst"}, OpTag: "op-tag", OLHEpoch: 7, OLHTag: "olh-tag",
		}))
	})
})

var _ = Describe("bucket_read_olh_log's reply", func() {
	It("decodes every rgw_cls_read_olh_log_ret golden", func() {
		for _, c := range goldens("rgw_cls_read_olh_log_ret") {
			op := radosclient.NewReadOp()
			res := rgw.ReadOLHLog(op, rgw.ReadOLHLogOp{}, denc.Squid)
			execResult(op).Set(c.Bin, 0)
			got, err := res.Result()
			Expect(err).NotTo(HaveOccurred(), "%s/%s", c.Archive, c.Name)
			Expect(got).To(Equal(decodeWhole(c.Bin, rgw.DecodeReadOLHLogRet)), "%s/%s", c.Archive, c.Name)
		}
	})

	It("passes the class's ECANCELED for a tag mismatch through", func() {
		op := radosclient.NewReadOp()
		res := rgw.ReadOLHLog(op, rgw.ReadOLHLogOp{OLH: rgw.ObjKey{Name: "obj"}, OLHTag: "stale"}, denc.Squid)
		execResult(op).Set(nil, -125)
		_, err := res.Result()
		Expect(err).To(MatchError(radosclient.ErrCanceled))
	})

	It("names bucket_read_olh_log in a reply that fails to decode", func() {
		op := radosclient.NewReadOp()
		res := rgw.ReadOLHLog(op, rgw.ReadOLHLogOp{}, denc.Squid)
		execResult(op).Set([]byte{1, 1, 100, 0, 0, 0}, 0)
		_, err := res.Result()
		Expect(err).To(MatchError(denc.ErrShortBuffer))
		Expect(err).To(MatchError(ContainSubstring("rgw: decoding bucket_read_olh_log reply")))
	})
})
