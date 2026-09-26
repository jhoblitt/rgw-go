package user_test

import (
	. "github.com/onsi/ginkgo/v2"

	"github.com/jhoblitt/rgw-go/internal/cls/user"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/denc/goldentest"
)

var (
	// squid checks re-encodings and JSON against goldens from the v19
	// dencoder image; radosgw-admin prints these types.
	squid = goldentest.Options{Release: denc.Squid}
	// squidNoJSON is for types no radosgw-admin command prints.
	squidNoJSON = goldentest.Options{Release: denc.Squid, SkipJSON: true}
)

var _ = Describe("corpus goldens", func() {
	const dir = "testdata"
	It("cls_user_header", func() {
		goldentest.RoundTrip(dir, "cls_user_header", squid, user.DecodeHeader,
			func(e *denc.Encoder, v user.Header, r denc.Release) { v.Encode(e, r) })
	})
	It("cls_user_stats", func() {
		goldentest.RoundTrip(dir, "cls_user_stats", squidNoJSON, user.DecodeStats,
			func(e *denc.Encoder, v user.Stats, r denc.Release) { v.Encode(e, r) })
	})
	It("cls_user_bucket", func() {
		goldentest.RoundTrip(dir, "cls_user_bucket", squidNoJSON, user.DecodeBucket,
			func(e *denc.Encoder, v user.Bucket, r denc.Release) { v.Encode(e, r) })
	})
	It("cls_user_bucket_entry", func() {
		goldentest.RoundTrip(dir, "cls_user_bucket_entry", squid, user.DecodeBucketEntry,
			func(e *denc.Encoder, v user.BucketEntry, r denc.Release) { v.Encode(e, r) })
	})
	It("cls_user_set_buckets_op", func() {
		goldentest.RoundTrip(dir, "cls_user_set_buckets_op", squidNoJSON, user.DecodeSetBucketsOp,
			func(e *denc.Encoder, v user.SetBucketsOp, r denc.Release) { v.Encode(e, r) })
	})
	It("cls_user_remove_bucket_op", func() {
		goldentest.RoundTrip(dir, "cls_user_remove_bucket_op", squidNoJSON, user.DecodeRemoveBucketOp,
			func(e *denc.Encoder, v user.RemoveBucketOp, r denc.Release) { v.Encode(e, r) })
	})
	It("cls_user_list_buckets_op", func() {
		goldentest.RoundTrip(dir, "cls_user_list_buckets_op", squidNoJSON, user.DecodeListBucketsOp,
			func(e *denc.Encoder, v user.ListBucketsOp, r denc.Release) { v.Encode(e, r) })
	})
	It("cls_user_list_buckets_ret", func() {
		goldentest.RoundTrip(dir, "cls_user_list_buckets_ret", squidNoJSON, user.DecodeListBucketsRet,
			func(e *denc.Encoder, v user.ListBucketsRet, r denc.Release) { v.Encode(e, r) })
	})
	It("cls_user_get_header_op", func() {
		goldentest.RoundTrip(dir, "cls_user_get_header_op", squidNoJSON, user.DecodeGetHeaderOp,
			func(e *denc.Encoder, v user.GetHeaderOp, r denc.Release) { v.Encode(e, r) })
	})
	It("cls_user_get_header_ret", func() {
		goldentest.RoundTrip(dir, "cls_user_get_header_ret", squidNoJSON, user.DecodeGetHeaderRet,
			func(e *denc.Encoder, v user.GetHeaderRet, r denc.Release) { v.Encode(e, r) })
	})
	It("cls_user_complete_stats_sync_op", func() {
		goldentest.RoundTrip(dir, "cls_user_complete_stats_sync_op", squidNoJSON, user.DecodeCompleteStatsSyncOp,
			func(e *denc.Encoder, v user.CompleteStatsSyncOp, r denc.Release) { v.Encode(e, r) })
	})
})
