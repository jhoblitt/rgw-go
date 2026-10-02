package lock_test

import (
	. "github.com/onsi/ginkgo/v2"

	"github.com/jhoblitt/rgw-go/internal/cls/lock"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/denc/goldentest"
)

// squid checks re-encodings against goldens from the v19 dencoder image. No
// type here is dumped by a radosgw-admin command, so JSON is not compared.
var squid = goldentest.Options{Release: denc.Squid, SkipJSON: true}

var _ = Describe("corpus goldens", func() {
	const dir = "testdata"
	It("cls_lock_lock_op", func() {
		goldentest.RoundTrip(dir, "cls_lock_lock_op", squid, lock.DecodeLockOp,
			func(e *denc.Encoder, v lock.LockOp, r denc.Release) { v.Encode(e, r) })
	})
	It("cls_lock_unlock_op", func() {
		goldentest.RoundTrip(dir, "cls_lock_unlock_op", squid, lock.DecodeUnlockOp,
			func(e *denc.Encoder, v lock.UnlockOp, r denc.Release) { v.Encode(e, r) })
	})
	It("cls_lock_break_op", func() {
		goldentest.RoundTrip(dir, "cls_lock_break_op", squid, lock.DecodeBreakOp,
			func(e *denc.Encoder, v lock.BreakOp, r denc.Release) { v.Encode(e, r) })
	})
	It("cls_lock_assert_op", func() {
		goldentest.RoundTrip(dir, "cls_lock_assert_op", squid, lock.DecodeAssertOp,
			func(e *denc.Encoder, v lock.AssertOp, r denc.Release) { v.Encode(e, r) })
	})
	// The archives before v11 hold legacy holder addresses, which ceph-dencoder
	// re-encodes in the msgr2-era form.
	It("cls_lock_get_info_reply", func() {
		goldentest.RoundTrip(dir, "cls_lock_get_info_reply", squid, lock.DecodeInfo,
			func(e *denc.Encoder, v lock.Info, r denc.Release) { v.Encode(e, r) })
	})
	It("entity_name_t", func() {
		goldentest.RoundTrip(dir, "entity_name_t", squid, lock.DecodeEntityName,
			func(e *denc.Encoder, v lock.EntityName, r denc.Release) { v.Encode(e, r) })
	})
})
