package meta

// Attr names the read path meets on heads, from src/rgw/rgw_common.h. A name
// marked v20.2.4 only is absent from v19.2.6's header.
const (
	AttrCryptPrefix = AttrPrefix + "crypt."
	// AttrCryptMode holds "SSE-C-AES256", "SSE-KMS", "AES256" or "RGW-AUTO",
	// as rgw_crypt.cc sets it.
	AttrCryptMode             = AttrCryptPrefix + "mode"
	AttrCryptKeyMD5           = AttrCryptPrefix + "keymd5"
	AttrCryptKeyID            = AttrCryptPrefix + "keyid"
	AttrUserManifest          = AttrPrefix + "user_manifest"
	AttrSLOManifest           = AttrPrefix + "slo_manifest"
	AttrTorrent               = AttrPrefix + "torrent"
	AttrAppendPartNum         = AttrPrefix + "append_part_num"
	AttrReplicationStatus     = AttrPrefix + "amz-replication-status"
	AttrRestoreStatus         = AttrPrefix + "restore-status"          // v20.2.4 only
	AttrRestoreType           = AttrPrefix + "restore-type"            // v20.2.4 only
	AttrRestoreExpiryDate     = AttrPrefix + "restore-expiry-date"     // v20.2.4 only
	AttrCloudTierStorageClass = AttrPrefix + "cloudtier_storage_class" // v20.2.4 only
	AttrCacheControl          = AttrPrefix + "cache_control"
	AttrContentDisp           = AttrPrefix + "content_disposition"
	AttrContentEnc            = AttrPrefix + "content_encoding"
	AttrContentLang           = AttrPrefix + "content_language"
	AttrExpires               = AttrPrefix + "expires"
	AttrXRobotsTag            = AttrPrefix + "x-robots-tag"
	AttrWebsiteRedirect       = AttrPrefix + "x-amz-website-redirect-location"
	AttrCksum                 = AttrPrefix + "cksum" // v20.2.4 only
)

// RestoreStatus is rgw::sal::RGWRestoreStatus (v20.2.4 rgw_sal.h), which
// AttrRestoreStatus holds as its single underlying byte.
type RestoreStatus uint8

// RestoreStatus values; RGWRestoreStatus::None is RestoreNone.
const (
	RestoreNone              RestoreStatus = 0
	RestoreAlreadyInProgress RestoreStatus = 1
	CloudRestored            RestoreStatus = 2
	RestoreFailed            RestoreStatus = 3
)
