package version_test

import (
	. "github.com/onsi/ginkgo/v2"

	"github.com/jhoblitt/rgw-go/internal/cls/version"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/denc/goldentest"
)

// squid checks re-encodings against goldens from the v19 dencoder image. No
// type here is dumped by a radosgw-admin command, so JSON is not compared.
var squid = goldentest.Options{Release: denc.Squid, SkipJSON: true}

var _ = Describe("corpus goldens", func() {
	const dir = "testdata"
	It("obj_version", func() {
		goldentest.RoundTrip(dir, "obj_version", squid, version.DecodeObjVersion,
			func(e *denc.Encoder, v version.ObjVersion, r denc.Release) { v.Encode(e, r) })
	})
	It("cls_version_check_op", func() {
		goldentest.RoundTrip(dir, "cls_version_check_op", squid, version.DecodeCheckOp,
			func(e *denc.Encoder, v version.CheckOp, r denc.Release) { v.Encode(e, r) })
	})
	It("cls_version_set_op", func() {
		goldentest.RoundTrip(dir, "cls_version_set_op", squid, version.DecodeSetOp,
			func(e *denc.Encoder, v version.SetOp, r denc.Release) { v.Encode(e, r) })
	})
	It("cls_version_inc_op", func() {
		goldentest.RoundTrip(dir, "cls_version_inc_op", squid, version.DecodeIncOp,
			func(e *denc.Encoder, v version.IncOp, r denc.Release) { v.Encode(e, r) })
	})
	It("cls_version_read_ret", func() {
		goldentest.RoundTrip(dir, "cls_version_read_ret", squid, version.DecodeReadRet,
			func(e *denc.Encoder, v version.ReadRet, r denc.Release) { v.Encode(e, r) })
	})
})
