package rgw

// Class is the object class every method in this package calls, RGW_CLASS.
const Class = "rgw"

// The cls_rgw method names this package calls, from cls_rgw_const.h.
const (
	methodBucketInitIndex       = "bucket_init_index"
	methodBucketSetTagTimeout   = "bucket_set_tag_timeout"
	methodBucketList            = "bucket_list"
	methodBucketCheckIndex      = "bucket_check_index"
	methodBucketRebuildIndex    = "bucket_rebuild_index"
	methodBucketPrepareOp       = "bucket_prepare_op"
	methodBucketCompleteOp      = "bucket_complete_op"
	methodObjRemove             = "obj_remove"
	methodObjStorePGVer         = "obj_store_pg_ver"
	methodObjCheckAttrsPrefix   = "obj_check_attrs_prefix"
	methodObjCheckMtime         = "obj_check_mtime"
	methodDirSuggestChanges     = "dir_suggest_changes"
	methodUserUsageLogAdd       = "user_usage_log_add"
	methodUserUsageLogRead      = "user_usage_log_read"
	methodUserUsageLogTrim      = "user_usage_log_trim"
	methodUsageLogClear         = "usage_log_clear"
	methodGCSetEntry            = "gc_set_entry"
	methodGCDeferEntry          = "gc_defer_entry"
	methodGCList                = "gc_list"
	methodGCRemove              = "gc_remove"
	methodGuardBucketResharding = "guard_bucket_resharding"
	methodSetBucketResharding   = "set_bucket_resharding"
	methodGetBucketResharding   = "get_bucket_resharding"
)

// ErrBusyResharding is CLS_RGW_ERR_BUSY_RESHARDING as a positive errno.
// GuardBucketResharding sends its negation, -2300, as radosgw does, and the
// seam maps the failure to radosclient.ErrBusyResharding.
const ErrBusyResharding = 2300

// ModifyOp is RGWModifyOp, the index operation a prepare or complete names.
// It travels as a u8.
type ModifyOp uint8

// The RGWModifyOp values.
const (
	OpAdd            ModifyOp = 0
	OpDel            ModifyOp = 1
	OpCancel         ModifyOp = 2
	OpUnknown        ModifyOp = 3
	OpLinkOLH        ModifyOp = 4
	OpLinkOLHDM      ModifyOp = 5
	OpUnlinkInstance ModifyOp = 6
	OpSyncStop       ModifyOp = 7
	OpResync         ModifyOp = 8
)

// The RGWPendingState values a pending_map entry carries.
const (
	PendingModify  uint8 = 0
	PendingDone    uint8 = 1
	PendingUnknown uint8 = 2
)

// The RGWObjCategory values that key a header's stats and label an entry.
const (
	CategoryNone        uint8 = 0
	CategoryMain        uint8 = 1
	CategoryShadow      uint8 = 2
	CategoryMultiMeta   uint8 = 3
	CategoryCloudTiered uint8 = 4
)

// The rgw_bucket_dir_entry flags.
const (
	FlagVer          uint16 = 0x1
	FlagCurrent      uint16 = 0x2
	FlagDeleteMarker uint16 = 0x4
	FlagVerMarker    uint16 = 0x8
	FlagCommonPrefix uint16 = 0x8000
)

// The RGWBILogFlags bits a prepare or complete carries.
const (
	BILogFlagVersionedOp uint16 = 0x1
	BILogNullVersion     uint16 = 0x2
)

// The cls_rgw_reshard_status values an InstanceEntry carries. Squid defines
// 0 to 2; Tentacle adds ReshardInLogRecord. The status is a bare u8 in every
// release, so no encoding depends on it.
const (
	ReshardNone        uint8 = 0
	ReshardInProgress  uint8 = 1
	ReshardDone        uint8 = 2
	ReshardInLogRecord uint8 = 3
)

// MtimeCheck is RGWCheckMTimeType, the comparison obj_check_mtime applies to
// the object's mtime. It travels as a u8.
type MtimeCheck uint8

// The RGWCheckMTimeType values.
const (
	MtimeEQ MtimeCheck = 0
	MtimeLT MtimeCheck = 1
	MtimeLE MtimeCheck = 2
	MtimeGT MtimeCheck = 3
	MtimeGE MtimeCheck = 4
)

// The dir_suggest_changes operations, CEPH_RGW_REMOVE and CEPH_RGW_UPDATE.
// SuggestLog is CEPH_RGW_DIR_SUGGEST_LOG_OP, or'ed in to have the change
// written to the bucket index log.
const (
	SuggestRemove byte = 'r'
	SuggestUpdate byte = 'u'
	SuggestLog    byte = 0x80
)
