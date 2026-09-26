package refcount_test

import (
	. "github.com/onsi/ginkgo/v2"

	"github.com/jhoblitt/rgw-go/internal/cls/refcount"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/denc/goldentest"
)

// squid checks re-encodings against goldens from the v19 dencoder image. No
// type here is dumped by a radosgw-admin command, so JSON is not compared.
var squid = goldentest.Options{Release: denc.Squid, SkipJSON: true}

var _ = Describe("corpus goldens", func() {
	const dir = "testdata"
	It("obj_refcount", func() {
		goldentest.RoundTrip(dir, "obj_refcount", squid, refcount.DecodeRefcount,
			func(e *denc.Encoder, v refcount.Refcount, r denc.Release) { v.Encode(e, r) })
	})
	It("cls_refcount_get_op", func() {
		goldentest.RoundTrip(dir, "cls_refcount_get_op", squid, refcount.DecodeGetOp,
			func(e *denc.Encoder, v refcount.GetOp, r denc.Release) { v.Encode(e, r) })
	})
	It("cls_refcount_put_op", func() {
		goldentest.RoundTrip(dir, "cls_refcount_put_op", squid, refcount.DecodePutOp,
			func(e *denc.Encoder, v refcount.PutOp, r denc.Release) { v.Encode(e, r) })
	})
	It("cls_refcount_set_op", func() {
		goldentest.RoundTrip(dir, "cls_refcount_set_op", squid, refcount.DecodeSetOp,
			func(e *denc.Encoder, v refcount.SetOp, r denc.Release) { v.Encode(e, r) })
	})
	It("cls_refcount_read_op", func() {
		goldentest.RoundTrip(dir, "cls_refcount_read_op", squid, refcount.DecodeReadOp,
			func(e *denc.Encoder, v refcount.ReadOp, r denc.Release) { v.Encode(e, r) })
	})
	It("cls_refcount_read_ret", func() {
		goldentest.RoundTrip(dir, "cls_refcount_read_ret", squid, refcount.DecodeReadRet,
			func(e *denc.Encoder, v refcount.ReadRet, r denc.Release) { v.Encode(e, r) })
	})
})
