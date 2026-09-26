package rgw_test

import (
	. "github.com/onsi/ginkgo/v2"

	"github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/denc/goldentest"
)

// encoder is what every stored and request type in the package implements.
type encoder interface {
	Encode(e *denc.Encoder, r denc.Release)
}

func enc[T encoder](e *denc.Encoder, v T, r denc.Release) { v.Encode(e, r) }

// squid checks re-encodings against goldens from the v19 dencoder image;
// skipJSON does too, for the types whose dump radosgw-admin never prints.
var (
	squid    = goldentest.Options{Release: denc.Squid}
	skipJSON = goldentest.Options{Release: denc.Squid, SkipJSON: true}
)

var _ = Describe("corpus goldens", func() {
	const dir = "testdata"
	It("cls_rgw_obj_key", func() {
		goldentest.RoundTrip(dir, "cls_rgw_obj_key", skipJSON, rgw.DecodeObjKey, enc[rgw.ObjKey])
	})
	It("rgw_bucket_dir_header", func() {
		goldentest.RoundTrip(dir, "rgw_bucket_dir_header", squid, rgw.DecodeDirHeader, enc[rgw.DirHeader])
	})
	It("rgw_bucket_dir_entry", func() {
		goldentest.RoundTrip(dir, "rgw_bucket_dir_entry", squid, rgw.DecodeDirEntry, enc[rgw.DirEntry])
	})
	It("rgw_bucket_dir_entry_meta", func() {
		goldentest.RoundTrip(dir, "rgw_bucket_dir_entry_meta", skipJSON, rgw.DecodeDirEntryMeta, enc[rgw.DirEntryMeta])
	})
	It("rgw_bucket_category_stats", func() {
		goldentest.RoundTrip(dir, "rgw_bucket_category_stats", skipJSON, rgw.DecodeCategoryStats, enc[rgw.CategoryStats])
	})
	It("rgw_bucket_pending_info", func() {
		goldentest.RoundTrip(dir, "rgw_bucket_pending_info", skipJSON, rgw.DecodePendingInfo, enc[rgw.PendingInfo])
	})
	It("rgw_bucket_entry_ver", func() {
		goldentest.RoundTrip(dir, "rgw_bucket_entry_ver", skipJSON, rgw.DecodeEntryVer, enc[rgw.EntryVer])
	})
	It("rgw_bucket_dir", func() {
		goldentest.RoundTrip(dir, "rgw_bucket_dir", skipJSON, rgw.DecodeDir, enc[rgw.Dir])
	})
	It("cls_rgw_bucket_instance_entry", func() {
		goldentest.RoundTrip(dir, "cls_rgw_bucket_instance_entry", skipJSON, rgw.DecodeInstanceEntry, enc[rgw.InstanceEntry])
	})
	It("rgw_cls_obj_prepare_op", func() {
		goldentest.RoundTrip(dir, "rgw_cls_obj_prepare_op", skipJSON, rgw.DecodePrepareOp, enc[rgw.PrepareOp])
	})
	It("rgw_cls_obj_complete_op", func() {
		goldentest.RoundTrip(dir, "rgw_cls_obj_complete_op", skipJSON, rgw.DecodeCompleteOp, enc[rgw.CompleteOp])
	})
	It("rgw_cls_list_op", func() {
		goldentest.RoundTrip(dir, "rgw_cls_list_op", skipJSON, rgw.DecodeListOp, enc[rgw.ListOp])
	})
	It("rgw_cls_list_ret", func() {
		goldentest.RoundTrip(dir, "rgw_cls_list_ret", skipJSON, rgw.DecodeListRet, enc[rgw.ListRet])
	})
	It("rgw_cls_check_index_ret", func() {
		goldentest.RoundTrip(dir, "rgw_cls_check_index_ret", skipJSON, rgw.DecodeCheckIndexRet, enc[rgw.CheckIndexRet])
	})
	It("rgw_cls_obj_remove_op", func() {
		goldentest.RoundTrip(dir, "rgw_cls_obj_remove_op", skipJSON, rgw.DecodeObjRemoveOp, enc[rgw.ObjRemoveOp])
	})
	It("rgw_cls_obj_store_pg_ver_op", func() {
		goldentest.RoundTrip(dir, "rgw_cls_obj_store_pg_ver_op", skipJSON, rgw.DecodeStorePGVerOp, enc[rgw.StorePGVerOp])
	})
	It("rgw_cls_obj_check_attrs_prefix", func() {
		goldentest.RoundTrip(dir, "rgw_cls_obj_check_attrs_prefix", skipJSON, rgw.DecodeCheckAttrsPrefixOp, enc[rgw.CheckAttrsPrefixOp])
	})
	It("cls_rgw_guard_bucket_resharding_op", func() {
		goldentest.RoundTrip(dir, "cls_rgw_guard_bucket_resharding_op", skipJSON, rgw.DecodeGuardOp, enc[rgw.GuardOp])
	})
	It("rgw_cls_tag_timeout_op", func() {
		goldentest.RoundTrip(dir, "rgw_cls_tag_timeout_op", skipJSON, rgw.DecodeTagTimeoutOp, enc[rgw.TagTimeoutOp])
	})
	It("rgw_cls_usage_log_add_op", func() {
		goldentest.RoundTrip(dir, "rgw_cls_usage_log_add_op", skipJSON, rgw.DecodeUsageAddOp, enc[rgw.UsageAddOp])
	})
	It("rgw_cls_usage_log_read_op", func() {
		goldentest.RoundTrip(dir, "rgw_cls_usage_log_read_op", skipJSON, rgw.DecodeUsageReadOp, enc[rgw.UsageReadOp])
	})
	It("rgw_cls_usage_log_read_ret", func() {
		goldentest.RoundTrip(dir, "rgw_cls_usage_log_read_ret", skipJSON, rgw.DecodeUsageReadRet, enc[rgw.UsageReadRet])
	})
	It("rgw_cls_usage_log_trim_op", func() {
		goldentest.RoundTrip(dir, "rgw_cls_usage_log_trim_op", skipJSON, rgw.DecodeUsageTrimOp, enc[rgw.UsageTrimOp])
	})
	It("rgw_usage_log_entry", func() {
		goldentest.RoundTrip(dir, "rgw_usage_log_entry", skipJSON, rgw.DecodeUsageLogEntry, enc[rgw.UsageLogEntry])
	})
	It("rgw_usage_log_info", func() {
		goldentest.RoundTrip(dir, "rgw_usage_log_info", skipJSON, rgw.DecodeUsageLogInfo, enc[rgw.UsageLogInfo])
	})
	It("rgw_usage_data", func() {
		goldentest.RoundTrip(dir, "rgw_usage_data", skipJSON, rgw.DecodeUsageData, enc[rgw.UsageData])
	})
	It("cls_rgw_gc_set_entry_op", func() {
		goldentest.RoundTrip(dir, "cls_rgw_gc_set_entry_op", skipJSON, rgw.DecodeGCSetEntryOp, enc[rgw.GCSetEntryOp])
	})
	It("cls_rgw_gc_obj_info", func() {
		goldentest.RoundTrip(dir, "cls_rgw_gc_obj_info", skipJSON, rgw.DecodeGCObjInfo, enc[rgw.GCObjInfo])
	})
	It("cls_rgw_obj_chain", func() {
		goldentest.RoundTrip(dir, "cls_rgw_obj_chain", skipJSON, rgw.DecodeGCObjChain, rgw.EncodeGCObjChain)
	})
	It("cls_rgw_obj", func() {
		goldentest.RoundTrip(dir, "cls_rgw_obj", skipJSON, rgw.DecodeGCObj, enc[rgw.GCObj])
	})
})
