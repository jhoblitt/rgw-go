package meta

// Attr names radosgw writes on heads and buckets, from src/rgw/rgw_common.h.
const (
	AttrPrefix       = "user.rgw."
	AttrACL          = AttrPrefix + "acl"
	AttrETag         = AttrPrefix + "etag"
	AttrIDTag        = AttrPrefix + "idtag"
	AttrTailTag      = AttrPrefix + "tail_tag"
	AttrManifest     = AttrPrefix + "manifest"
	AttrPGVer        = AttrPrefix + "pg_ver"
	AttrSourceZone   = AttrPrefix + "source_zone"
	AttrContentType  = AttrPrefix + "content_type"
	AttrStorageClass = AttrPrefix + "storage_class"
	AttrCompression  = AttrPrefix + "compression"
	AttrMetaPrefix   = AttrPrefix + "x-amz-meta-"
	// AttrObjVersion is cls_version's VERSION_ATTR, which the object class
	// keeps outside the user.rgw. namespace.
	AttrObjVersion = "ceph.objclass.version"
)

// Attr names the write path sets or strips on heads, from
// src/rgw/rgw_common.h. The tagging attr is tags.Attr.
const (
	AttrDeleteAt         = AttrPrefix + "delete_at"
	AttrShadowObj        = AttrPrefix + "shadow_name"
	AttrOLHPrefix        = AttrPrefix + "olh."
	AttrOLHInfo          = AttrOLHPrefix + "info"
	AttrOLHVer           = AttrOLHPrefix + "ver"
	AttrOLHIDTag         = AttrOLHPrefix + "idtag"
	AttrObjectRetention  = AttrPrefix + "object-retention"
	AttrObjectLegalHold  = AttrPrefix + "object-legal-hold"
	AttrReplicationTrace = AttrPrefix + "replication-trace"
	AttrReplicatedAt     = AttrPrefix + "replicated-at"
	AttrCloudTierType    = AttrPrefix + "cloud_tier_type"
	AttrCloudTierConfig  = AttrPrefix + "cloud_tier_config"
)

// AttrOLHPendingPrefix is RGW_ATTR_OLH_PENDING_PREFIX
// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.h#L131-L136 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.h#L150-L155 at v20.2.4):
// an OLH head holds one attr under it, named by its op tag, for each
// modification begun and not yet applied.
const AttrOLHPendingPrefix = AttrOLHPrefix + "pending."

// AttrRateLimit is RGW_ATTR_RATELIMIT
// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.h#L81 at v19.2.6,
// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.h#L86 at v20.2.4),
// the RateLimitInfo a user or bucket carries.
const AttrRateLimit = AttrPrefix + "ratelimit"
