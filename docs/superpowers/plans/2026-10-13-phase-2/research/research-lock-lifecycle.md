# Phase 2 research: lock-lifecycle (object lock and lifecycle)

Area: object lock (bucket configuration, default retention, object retention in governance and
compliance, the bypass header and its permission, legal hold, and the deletes and overwrites they
block) and lifecycle (the configuration ops and the worker: expiration, noncurrent expiration,
abort of incomplete multipart uploads, in-cluster storage-class transition).

Notes with every verified fact: `research-lock-lifecycle.notes.md` beside this file.

## 1. Scope

Spec §9, phase 2: "object lock with retention and legal hold, lifecycle with the worker for
expiration, noncurrent expiration, multipart abort and in-cluster storage-class transition".
§7 adds lifecycle to the workers. `docs/exclusions.md` keeps cloud transition and restore out
("Storage-class transitions inside the cluster are not excluded").

Defect handling follows the owner's rule of 2026-10-13, which replaces the brief's PP-3 bullet:
compatibility means RADOS state both ways. Where radosgw's S3 behaviour is wrong, rgw-go does
what the S3 API specifies, provided its RADOS writes stay compatible. rgw-go also prevents
corruption of shared data that radosgw allows. Every difference becomes an exclusion.

### Object lock: S3 surface

| Op or behaviour | Request | Action checked |
|---|---|---|
| PutObjectLockConfiguration | `PUT /{b}?object-lock`, body `ObjectLockConfiguration` | s3:PutBucketObjectLockConfiguration |
| GetObjectLockConfiguration | `GET /{b}?object-lock` | s3:GetBucketObjectLockConfiguration |
| CreateBucket with object lock | `x-amz-bucket-object-lock-enabled: true`; the bucket is created versioned with lock enabled | s3:CreateBucket (phase 1) |
| PutObjectRetention | `PUT /{b}/{k}?retention[&versionId]`, body `Retention`, header `x-amz-bypass-governance-retention` | s3:PutObjectRetention, plus s3:BypassGovernanceRetention when the header is sent |
| GetObjectRetention | `GET ?retention[&versionId]` | s3:GetObjectRetention |
| PutObjectLegalHold / GetObjectLegalHold | `?legal-hold[&versionId]`, body `LegalHold` | s3:PutObjectLegalHold / s3:GetObjectLegalHold |
| Lock headers on writes | `x-amz-object-lock-mode`, `-retain-until-date`, `-legal-hold` on PutObject, CopyObject, CreateMultipartUpload (UploadPart parses them too); phase 1 checks the values, and refuses them on a bucket without lock | stored as `user.rgw.object-retention` and `user.rgw.object-legal-hold` |
| Default retention | a create write (PUT, append, the streaming copy, CompleteMultipartUpload, a transition's rewrite) on a bucket with a rule gets a retention when it names none | none |
| Enforcement | DeleteObject naming a version, and each key of DeleteObjects naming a version; `x-amz-bypass-governance-retention` with s3:BypassGovernanceRetention unlocks GOVERNANCE | s3:DeleteObjectVersion, s3:BypassGovernanceRetention |
| Read headers | GET and HEAD send the three lock headers from the attrs when the requester may s3:GetObjectRetention or s3:GetObjectLegalHold | as named |
| Versioning lock-in | PutBucketVersioning refuses anything but Enabled on a lock bucket (versioning area's op) | none |

"The deletes and overwrites they block": radosgw never blocks an overwrite. Object lock
requires versioning, so a write makes a new version. The lock blocks three things: the
permanent removal of a version, the shortening of a retention, and a mode change, as below.

### Lifecycle: S3 surface

| Op or behaviour | Request |
|---|---|
| PutBucketLifecycleConfiguration | `PUT /{b}?lifecycle`, body `LifecycleConfiguration`, optional `Content-MD5`; s3:PutLifecycleConfiguration |
| GetBucketLifecycleConfiguration | `GET ?lifecycle`; s3:GetLifecycleConfiguration |
| DeleteBucketLifecycle | `DELETE ?lifecycle` answering 204; s3:PutLifecycleConfiguration, as radosgw checks it |
| `x-amz-expiration` | on GET, HEAD and PutObject responses |
| `x-amz-abort-date`, `x-amz-abort-rule-id` | on CreateMultipartUpload responses |
| Rule grammar | ID, Status, Filter (Prefix, Tag, And, ObjectSizeGreaterThan/LessThan, the ArchiveZone extension) or the legacy Prefix; Expiration (Days, Date, ExpiredObjectDeleteMarker); NoncurrentVersionExpiration (NoncurrentDays, NewerNoncurrentVersions); AbortIncompleteMultipartUpload (DaysAfterInitiation); Transition and NoncurrentVersionTransition (Days or Date, StorageClass) |
| Worker | per lc shard and bucket: current expiration, delete-marker expiration, noncurrent expiration, in-cluster transition to a placement storage class, abort of incomplete uploads |
| Side effects | bucket deletion drops the bucket's lc entry; a `bucket.instance` metadata put sets or drops it |

Admin: no admin REST route reads or writes lifecycle at either tag (searched). `radosgw-admin
lc list/get/process/reshard fix` act on RADOS directly, so they work against rgw-go only if
rgw-go writes the same records. Phase 2 adds no admin op in this area. Excluded and unchanged:
cloud-s3 tiers, restore, and the restore expirer v20.2.4 calls after each pass.

## 2. radosgw's behaviour

Line ranges are paths from the ceph root. `rgw_object_lock.{h,cc}` and `rgw_lc_s3.cc` are the
same blob at both tags, as are the cls_rgw lc methods, their op structs and their stored types
(each checked by diff). A citation given for one tag holds for both only where this says so.

### 2.1 Stored forms

- **RGWObjectLock**, the bucket instance's `obj_lock`, is written only while the flags carry
  `BUCKET_OBJ_LOCK_ENABLED`. It is v1: `enabled`, `rule_exist`, then the rule if one exists. The
  rule is `ObjectLockRule` v1, holding `DefaultRetention` v1 {mode, days int, years int}.
  `src/rgw/rgw_object_lock.h:11-149@v19.2.6`. rgw-go keeps it opaque today
  (`meta.BucketInfo.ObjLock`, a RawStruct).
- **RGWObjectRetention** v2 holds the mode and the date, then the date again at nanosecond
  precision. **RGWObjectLegalHold** v1 holds the status.
  `src/rgw/rgw_object_lock.h:151-230@v19.2.6`. rgw-go has both (`internal/meta/objectlock.go`).
- **RGWLifecycleConfiguration** v1 is a multimap of id to LCRule.
  `src/rgw/rgw_lc.h:510-563@v19.2.6#RGWLifecycleConfiguration`.
  - **LCRule** v6, legacy compat: id, prefix, status, expiration, noncur_expiration,
    mp_expiration, dm_expiration, filter, transitions and noncur_transitions, each map keyed by
    storage class. `src/rgw/rgw_lc.h:316-465@v19.2.6#LCRule`.
  - **LCExpiration** v4, compat 2: days, date and newer_noncurrent, all strings.
    `src/rgw/rgw_lc.h:41-105@v19.2.6#LCExpiration`.
  - **LCTransition** v1: days, date and storage_class, all strings.
    `src/rgw/rgw_lc.h:107-170@v19.2.6#LCTransition`.
  - **LCFilter** v4: prefix, RGWObjTags, flags u32, size_gt and size_lt (strings).
    `src/rgw/rgw_lc.h:187-314@v19.2.6#LCFilter`. Its decoder is `DECODE_START(3)` at v19.2.6
    and `(4)` at v20.2.4. The compat byte is 1, so both read v4. Encode v4 on both releases.
- **The lc shards** are `lc.0` to `lc.<n-1>`, where n = min(`rgw_lc_max_objs`, 7877). They
  live in the zone's `lc_pool`, by default the log pool in namespace `lc`.
  `src/rgw/rgw_lc.cc:233-254@v19.2.6#RGWLC::initialize`; `src/rgw/rgw_zone.cc:582@v19.2.6`.
  - A bucket's shard is `ceph_str_hash_linux(key) % 7877 % n`, keyed by
    `tenant:name:marker`. `src/rgw/rgw_lc.cc:1966-1992@v19.2.6`.
  - Each shard's omap holds one `cls_rgw_lc_entry` v1 per bucket {bucket key, start_time u64,
    status u32: 0 uninitial, 1 processing, 2 failed, 3 complete}, under the bucket key.
  - Its omap header holds `cls_rgw_lc_obj_head` v2 {start_date, marker, shard_rollover_date},
    each time 8 bytes. `src/cls/rgw/cls_rgw_types.h:1264-1333@v19.2.6`,
    `src/cls/rgw/cls_rgw_types.h:1301-1370@v20.2.4`.
- **cls_rgw lc methods.** The read methods are `lc_get_entry`, `lc_get_next_entry`,
  `lc_get_head` and `lc_list_entries`. The read-write methods are `lc_set_entry`,
  `lc_rm_entry` and `lc_put_head`. `src/cls/rgw/cls_rgw.cc:4734-4740@v19.2.6`,
  `src/cls/rgw/cls_rgw.cc:5153-5159@v20.2.4`. Bodies at `src/cls/rgw/cls_rgw.cc:4175-4359@v19.2.6`, and the same at
  `src/cls/rgw/cls_rgw.cc:4577-4761@v20.2.4`.
  - get_next_entry reads one key after the marker, and an empty shard gives an empty entry.
  - list_entries replies in the request's struct_v, the request being `ENCODE_START(3,1)`. It
    reads legacy `pair<string,int>` values too.
  - get_head of an empty header is a zero head.
  - The ops are v2 for every entry carrier and v1 for the rest
    (`src/cls/rgw/cls_rgw_ops.h:1183-1456@v19.2.6`).
  - The client runs every call as a standalone synchronous exec
    (`src/cls/rgw/cls_rgw_client.cc:943-1067@v19.2.6`).

### 2.2 Object-lock configuration and per-object ops

All of these load the bucket first, so a missing bucket is NoSuchBucket. Each answers 403 when
its action is refused. The bodies are read by `read_all_input` up to `rgw_max_put_param_size`
(`src/rgw/rgw_rest.cc:1479-1497@v19.2.6`). rgw-go's `op.ReadParamBody` already verifies the
signed hash there (registry, "radosgw ignores a payload-hash mismatch...", a security defect
rgw-go does not reproduce).

- **PutObjectLockConfiguration** (`src/rgw/rgw_op.cc:8230-8293@v19.2.6#RGWPutBucketObjectLock::execute`,
  `src/rgw/rgw_op.cc:9178-9241@v20.2.4#RGWPutBucketObjectLock::execute`), in order:
  1. Versioning not Enabled: 409 InvalidBucketState, "Object lock cannot be enabled unless
     the bucket has versioning enabled". This comes before the body is read.
  2. The body is read.
  3. An XML parse failure is 400 MalformedXML.
  4. A missing or invalid `ObjectLockConfiguration` is MalformedXML. Invalid means:
     `ObjectLockEnabled` is not "Enabled"; or the Rule's DefaultRetention has a Mode outside
     GOVERNANCE/COMPLIANCE; or it has neither or both of Days and Years; or a number does not
     parse as an int (`src/rgw/rgw_object_lock.cc:8-66@v19.2.6`;
     `src/rgw/rgw_xml.cc:282-377@v19.2.6`).
  5. A rule whose period is not exactly one of Days and Years positive is 400
     InvalidRetentionPeriod, "retention period must be a positive integer value"
     (`src/rgw/rgw_object_lock.h:112-117@v19.2.6#retention_period_valid`).
  6. The forward to the master, a no-op here.
  7. Under retry_raced_bucket_write: set `BUCKET_OBJ_LOCK_ENABLED` if missing, after checking
     versioning again; replace `obj_lock`; then `put_info`.
  - The response is 200 with no body.
- **GetObjectLockConfiguration** (`src/rgw/rgw_op.cc:8313-8319@v19.2.6`, `src/rgw/rgw_op.cc:9261-9267@v20.2.4#RGWGetBucketObjectLock::execute`):
  without lock it is 404 ObjectLockConfigurationNotFoundError. Otherwise it is
  `<ObjectLockConfiguration>` with no xmlns, holding `ObjectLockEnabled` and the Rule if one
  exists. Days is written when positive, otherwise Years
  (`src/rgw/rgw_rest_s3.cc:4474-4488@v19.2.6`; `src/rgw/rgw_object_lock.cc:29-75@v19.2.6`).
- **CreateBucket with lock** sets `BUCKET_VERSIONED | BUCKET_OBJ_LOCK_ENABLED` and writes the
  default `obj_lock`, enabled with no rule. `src/rgw/driver/rados/rgw_rados.cc:2391-2393@v19.2.6`,
  `src/rgw/driver/rados/rgw_rados.cc:2496-2498@v20.2.4`. Phase 1 already parses the header and answers 501.
- **PutObjectRetention.**
  - verify_permission (`src/rgw/rgw_op.cc:8321-8338@v19.2.6`, `src/rgw/rgw_op.cc:9269-9286@v20.2.4#RGWPutObjRetention::verify_permission`):
    s3:PutObjectRetention. get_params then reads the bypass header (url-decoded, "true" in
    any case) and the body. When the header is set, bypass_perm =
    verify_object_permission(s3:BypassGovernanceRetention).
  - execute (`src/rgw/rgw_op.cc:8345-8422@v19.2.6#RGWPutObjRetention::execute`,
    `src/rgw/rgw_op.cc:9293-9383@v20.2.4#RGWPutObjRetention::execute`), in order:
    1. No lock on the bucket: 400 InvalidRequest.
    2. MalformedXML, as for the bucket configuration: a Mode, or a `RetainUntilDate` that
       `from_iso_8601` cannot read.
    3. A date in a past second: 400 InvalidArgument, "the retain-until date must be in the
       future".
    4. The object's attrs are read. A missing object, version or delete marker is 404.
    5. An existing retention that does not decode is 500.
    6. A new date earlier, in whole seconds, than the old one needs the old mode GOVERNANCE,
       the header and bypass_perm, or it is 403.
    7. Otherwise, the same mode passes.
    8. Otherwise, COMPLIANCE to GOVERNANCE is 403.
    9. Otherwise, GOVERNANCE to COMPLIANCE needs the bypass, or it is 403.
    10. `modify_obj_attrs` writes the retention.
  - Nothing checks whether the old retention has already expired.
  - v20.2.4 adds a bucket-logging record first (phase 3). The response is 200.
- **modify_obj_attrs** (`src/rgw/driver/rados/rgw_sal_rados.cc:2378-2419@v19.2.6`) resolves
  the OLH target and rewrites the whole attr set plus the one attr, moving the mtime on 1 ns.
  rgw-go's `SetObjectAttrs` already does this for unversioned objects.
- **GetObjectRetention / GetObjectLegalHold** (`src/rgw/rgw_op.cc:8441-8471@v19.2.6`,
  `:8545-8575`; `src/rgw/rgw_op.cc:9402-9432@v20.2.4#RGWGetObjRetention::execute`, `:9526-9556`):
  1. No lock: 400 InvalidRequest, "bucket object lock not configured".
  2. The attrs are read; a missing object is 404.
  3. No attr: 404 ObjectLockConfigurationNotFoundError.
  4. An attr that does not decode: 500.
  - Otherwise `<Retention>` or `<LegalHold>`, with no xmlns. The date is
    `to_iso_8601`'s 9-digit nanoseconds with a Z (`src/common/iso_8601.h:38-43@v19.2.6`).
- **PutObjectLegalHold** (`src/rgw/rgw_op.cc:8490-8526@v19.2.6`, `src/rgw/rgw_op.cc:9451-9507@v20.2.4#RGWPutObjLegalHold::execute`):
  1. No lock: InvalidRequest.
  2. The body is read and parsed.
  3. A missing LegalHold, or a Status outside ON/OFF, is MalformedXML.
  4. `modify_obj_attrs`. v20.2.4 first reads the attrs for its logging record; v19.2.6's
     `modify_obj_attrs` reads them anyway, so a missing object is 404 on both.
- **Delete enforcement**: `verify_object_lock`
  (`src/rgw/rgw_common.cc:1639-1667@v19.2.6#verify_object_lock`,
  `src/rgw/rgw_common.cc:1702-1730@v20.2.4#verify_object_lock`).
  - A retention attr that does not decode is EIO.
  - A retain-until second after now is locked, unless the mode is GOVERNANCE and both the
    header and the permission are present.
  - A legal hold that does not decode is EIO. ON is locked.
  - It runs only when the request names a version (`null` included) and the bucket has lock.
    State loading ENOENT, a delete marker, skips it.
    `src/rgw/rgw_op.cc:5173-5221@v19.2.6#RGWDeleteObj::execute`,
    `src/rgw/rgw_op.cc:5563-5610@v20.2.4#RGWDeleteObj::execute`.
  - DeleteObject checks the bypass on the object's ARN
    (`src/rgw/rgw_op.cc:5138-5166@v19.2.6#RGWDeleteObj::verify_permission`).
  - DeleteObjects checks it once, on the bucket ARN
    (`src/rgw/rgw_op.cc:6768-6780@v19.2.6#RGWDeleteMultiObj::verify_permission`,
    `src/rgw/rgw_op.cc:7707-7719@v20.2.4#RGWDeleteMultiObj::verify_permission`;
    `src/rgw/rgw_common.cc:1469-1476@v19.2.6`). Each key then checks
    (`src/rgw/rgw_op.cc:6818-6866@v19.2.6#RGWDeleteMultiObj::handle_individual_object`).
  - The refusal is 403 AccessDenied, with message "forbidden by object lock" on DeleteObject.
  - `verify_object_lock` has only these two callers at both tags (searched). Admin removals
    and purges never check a lock.
- **Default retention**: `_do_write_meta` applies it when the bucket has lock and a rule, the
  flags are exactly `PUT_OBJ_CREATE`, and the attrs carry no retention. It writes {rule mode,
  `get_lock_until_date(set_mtime)`}. `src/rgw/driver/rados/rgw_rados.cc:3179-3189@v19.2.6`,
  `src/rgw/driver/rados/rgw_rados.cc:3308-3318@v20.2.4`.
  - `get_lock_until_date` is mtime + days × 86400 s, or mtime + `std::chrono::years`
    (365.2425 days) × years. `src/rgw/rgw_object_lock.cc:87-95@v19.2.6#RGWObjectLock::get_lock_until_date`.
    The sum is in u64 nanoseconds and wraps (§6).
  - `PUT_OBJ_CREATE` is set by: the atomic and append processors
    (`src/rgw/driver/rados/rgw_putobj_processor.cc:384@v19.2.6`, `:745`); `copy_obj_data`
    (`src/rgw/driver/rados/rgw_rados.cc:4976@v19.2.6`, `src/rgw/driver/rados/rgw_rados.cc:5236@v20.2.4`), which serves the
    streaming copy and the transition; and the multipart completion
    (`src/rgw/driver/rados/rgw_sal_rados.cc:3629@v19.2.6`, `src/rgw/driver/rados/rgw_sal_rados.cc:4477@v20.2.4`).
  - Not set by part heads or the multipart meta object.
- **Write headers**: PutObject and CopyObject store the header retention and legal hold
  (`src/rgw/rgw_op.cc:4542-4551@v19.2.6`, `:5584-5593`). CreateMultipartUpload stores them in
  `multipart_upload_info` (`obj_retention_exist` and the rest), and the completion writes them
  as attrs (`src/rgw/driver/rados/rgw_sal_rados.cc:3310-3317@v19.2.6`, `:3597-3606`;
  `src/rgw/driver/rados/rgw_sal_rados.cc:4156-4163@v20.2.4`, `:4445-4454`).
- **Read headers** (`src/rgw/rgw_op.cc:1007-1010@v19.2.6`; `src/rgw/rgw_rest_s3.cc:595-613@v19.2.6`,
  `src/rgw/rgw_rest_s3.cc:712-730@v20.2.4`): attrs that do not decode are skipped. v20.2.4 also refuses a system
  request that lacks either permission (`src/rgw/rgw_op.cc:1148-1164@v20.2.4`), which is
  replication and so excluded.
- **Versioning lock-in**: PutBucketVersioning other than Enabled on a lock bucket is 409
  InvalidBucketState (`src/rgw/rgw_op.cc:2788-2793@v19.2.6`, `src/rgw/rgw_op.cc:3020-3025@v20.2.4`).

### 2.3 Lifecycle configuration ops

- **PutBucketLifecycleConfiguration**
  (`src/rgw/rgw_op.cc:5930-6025@v19.2.6#RGWPutLC::execute`,
  `src/rgw/rgw_op.cc:6592-6695@v20.2.4#RGWPutLC::execute`), in order:
  1. **[v20.2.4 only]** An indexless current index is 405 MethodNotAllowed, "Indexless
     buckets do not support lifecycle policy".
  2. A `Content-MD5` that `from_base64` refuses is 400 BadDigest.
  3. The body is read.
  4. An MD5 that differs is BadDigest. radosgw compares 16 bytes whatever length the header
     decoded to.
  5. An XML parse failure is MalformedXML.
  6. `decode_xml("LifecycleConfiguration")` is not mandatory, so a wrong root is an empty
     configuration. Grammar errors are MalformedXML (`src/rgw/rgw_lc_s3.cc:42-308@v19.2.6`).
     - Status is mandatory and Enabled or Disabled.
     - Filter is optional, or else a top-level Prefix is mandatory.
     - At least one action.
     - Expiration takes exactly one of Days, Date or ExpiredObjectDeleteMarker. A Date must
       be ISO 8601 at exact midnight UTC (`src/rgw/rgw_lc_s3.cc:19-30@v19.2.6#check_date`).
     - NoncurrentDays and DaysAfterInitiation are mandatory. A Transition takes exactly one
       of Days or Date, and a StorageClass. A repeated StorageClass is an error.
     - An `ObjectSizeLessThan` at or below `ObjectSizeGreaterThan` is an error.
     - More rules than `rgw_lc_max_rules` is an error. An empty ID gets 48 random lowercase
       alphanumerics.
  7. `rebuild` → `check_and_add_rule` (`src/rgw/rgw_lc.cc:173-192@v19.2.6`):
     - An invalid rule is EINVAL, which is 400 InvalidArgument. Invalid means an ID over 255,
       no action, Days of 0 or less, a negative transition day, or Days and Date mixed across
       Expiration and the Transitions (`src/rgw/rgw_lc.cc:60-95@v19.2.6#LCRule::valid`).
     - A duplicate ID is EINVAL.
     - Tags with ExpiredObjectDeleteMarker or AbortIncompleteMultipartUpload is InvalidRequest.
  8. The forward.
  9. `set_bucket_config` (`src/rgw/rgw_lc.cc:2636-2666@v19.2.6#RGWLC::set_bucket_config`,
     `src/rgw/rgw_lc.cc:2703-2733@v20.2.4#RGWLC::set_bucket_config`):
     - It encodes the config into `user.rgw.lc` with `merge_and_store_attrs`, one attempt
       guarded by the objv. A lost race fails the request.
     - Then `guard_lc_modify` takes the `lc_process` exclusive lock on the bucket's shard,
       for `rgw_lc_lock_max_time` under the RGWLC instance's random 16-character cookie. It
       retries EBUSY and EEXIST every 100 ms up to 500 times, then fails.
     - It sets the entry {key, 0, uninitial} and unlocks
       (`src/rgw/rgw_lc.cc:2583-2634@v19.2.6#guard_lc_modify`).
  - The attr is stored even when the lock then fails.
  - The response is 200 with an empty document.
- **GetBucketLifecycleConfiguration**: no attr is 404 NoSuchLifecycleConfiguration; an attr
  that does not decode is 500. Otherwise
  `<LifecycleConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/">` with the rules in
  rule_map order (`src/rgw/rgw_rest_s3.cc:3658-3696@v19.2.6`; `src/rgw/rgw_lc_s3.cc:310-369@v19.2.6`).
  Phase 1 already serves the 404.
- **DeleteBucketLifecycle**: `remove_bucket_config(update_attrs=true)` erases the attr with
  `put_info`, then removes the entry under `guard_lc_modify`. The answer is 204 even when
  there was no configuration (`src/rgw/rgw_op.cc:6027-6044@v19.2.6#RGWDeleteLC::execute`;
  `src/rgw/rgw_lc.cc:2668-2693@v19.2.6#RGWLC::remove_bucket_config`).
- **Elsewhere**:
  - Bucket removal drops the entry when the bucket had `user.rgw.lc`, ignoring the result
    (`src/rgw/driver/rados/rgw_sal_rados.cc:402-407@v19.2.6`, `src/rgw/driver/rados/rgw_sal_rados.cc:419-424@v20.2.4`).
  - A `bucket.instance` metadata put sets the entry when the new attrs carry `user.rgw.lc`.
    Otherwise it drops it: always at v19.2.6, only when the old instance had the attr at
    v20.2.4 (`src/rgw/driver/rados/rgw_bucket.cc:2900-2928@v19.2.6`, `src/rgw/driver/rados/rgw_bucket.cc:3010-3040@v20.2.4`).
- **Headers**:
  - `x-amz-expiration` on GET, HEAD and PutObject: `expiry-date="<RFC 1123 GMT>",
    rule-id="<id>"` from the earliest matching enabled rule
    (`src/rgw/rgw_lc.cc:2750-2885@v19.2.6#s3_expiration_header`; call sites
    `src/rgw/rgw_rest_s3.cc:389@v19.2.6`, `:462`, `:2742`; `src/rgw/rgw_rest_s3.cc:400@v20.2.4`, `:498`, `:2903`).
    A rule matches on prefix and tags. Days count from midnight UTC after the mtime.
  - CreateMultipartUpload sends `x-amz-abort-date` and `x-amz-abort-rule-id` from
    AbortIncompleteMultipartUpload, matching on prefix only
    (`src/rgw/rgw_lc.cc:2887-2951@v19.2.6#s3_multipart_abort_header`;
    `src/rgw/rgw_rest_s3.cc:4038-4041@v19.2.6`, `src/rgw/rgw_rest_s3.cc:4535-4538@v20.2.4`).

### 2.4 The worker

**Startup.** LC runs when `rgw_enable_lc_threads` is set, true by default
(`src/rgw/driver/rados/rgw_rados.cc:1326-1330@v19.2.6`; `src/rgw/rgw_appmain.cc:231-233@v19.2.6`).
`rgw_lc_max_worker` threads start, 3 by default. Each has a workpool of `rgw_lc_max_wp_worker`
threads with queues of 512 (`src/rgw/rgw_lc.cc:2467-2477@v19.2.6`, `:734-879`). Ceph's option
text says the thread is "required on at least one RGW ... Having the thread enabled on multiple
RGW processes within the same zone can spread some of the maintenance work between them"
(`src/common/options/rgw.yaml.in:244-260@v19.2.6`). The other options, at the same defaults on
both tags (`src/common/options/rgw.yaml.in:374-458@v19.2.6`, `src/common/options/rgw.yaml.in:400-485@v20.2.4`, `:3880-3895`,
`:4172-4187`):
- `rgw_lifecycle_work_time` "00:00-06:00", local time;
- `rgw_lc_lock_max_time` 90 s;
- `rgw_lc_thread_delay` 0 ms;
- `rgw_lc_max_objs` 32;
- `rgw_lc_max_rules` 1000;
- `rgw_lc_debug_interval` -1;
- `rgwlc_auto_session_clear` true;
- `rgwlc_skip_bucket_step` false.

**Schedule.** Each worker loops (`src/rgw/rgw_lc.cc:201-231@v19.2.6#RGWLC::LCWorker::entry`).
When `should_work` holds, it makes one pass over every shard in a fresh random order. It then
sleeps until the next local window start, or for one debug interval.
`should_work` (`src/rgw/rgw_lc.cc:2510-2539@v19.2.6`) and the start time (`:2541-2570`) use
`localtime_r`, so the gateway's time zone decides both.

**Shard pass** (`src/rgw/rgw_lc.cc:2252-2465@v19.2.6#RGWLC::process`,
`src/rgw/rgw_lc.cc:2317-2532@v20.2.4#RGWLC::process`):
1. Lock `lc_process` on `lc.<i>` for `rgw_lc_lock_max_time`, with cookie
   `"lc_thrd: <ix>"`. Retry up to 5 times, backing off from 0 ms in 100 ms steps. A shard
   another worker holds is skipped.
2. `lc_get_head`. A shard object that does not exist fails the read, so the shard is skipped.
3. When the head's marker is empty and rollover is allowed, take the first entry: set the
   marker, set start_date to now, and clear the rollover date. Rollover is allowed when there
   is no rollover date, it is over 24 h old, or debug is on (`:2134-2147`). Otherwise
   `lc_get_entry(marker)`; ENOENT advances the head and loops.
4. Skip an entry that is processing, unless its session expired: `start + 2 × interval < now`
   with auto-clear on (`:2025-2043`). Skip an entry completed earlier today; today means since
   local midnight, or within a debug interval (`:2149-2171`).
5. Set the entry processing with start_time now, then advance the head to the next entry
   (`lc_get_next_entry`, `lc_put_head`) (`:2173-2201`).
6. **Unlock**, then process the bucket.
7. Lock again with the backoff; failure ends the pass, leaving the entry processing.
8. ENOENT drops the entry, an error marks it failed, and success marks it complete.
9. When the head's marker is empty, stamp the rollover date and end. Loop otherwise.

The lock covers only the bookkeeping, never a bucket's processing. The bucket's claim is the
entry's status and start time, together with the head's position.

**Bucket pass** (`src/rgw/rgw_lc.cc:1679-1830@v19.2.6#RGWLC::bucket_lc_process`,
`src/rgw/rgw_lc.cc:1735-1886@v20.2.4#RGWLC::bucket_lc_process`):
- The key is split into tenant, name and marker, and the bucket is loaded.
- A marker mismatch is ENOENT, so the entry is dropped. No `user.rgw.lc` is success. A config
  that does not decode is -1, so the entry is marked failed.
- Each rule of the prefix-sorted prefix_map is taken in turn. A rule is skipped when it is
  disabled, has no object action (`:1009-1018#is_valid_op`; an abort-only rule skips this
  pass), or fails `zone_check` (the ArchiveZone flag against an archive zone, `:1020-1028`).
- The listing (`LCObjsLister`, `:351-446`) is unordered under the rule's prefix, with versions
  when the bucket is versioned, in pages of 1000 with `rgw_lc_thread_delay` between pages.
  It counts `num_noncurrent`, earlier entries of the same name, and keeps the previous entry.
- Each entry goes to the workpool with the rule's state and is drained at the rule's end.
- Then `handle_multipart_expiration` runs (`:886-1000`). It lists namespace `multipart` with
  MultipartMetaFilter under each enabled rule's prefix that has DaysAfterInitiation. An entry
  whose meta mtime has expired is aborted through the SAL multipart abort, the same path as
  AbortMultipartUpload. Tags and size are not filtered.
- Expiry (`:272-302#obj_has_expired`): `round_to_day(now) - mtime_secs >= days × 86400`.
  `round_to_day` is **local** midnight (`src/include/utime.h:218-227@v19.2.6`). In debug mode
  it is `now - mtime >= days × interval`. A Date is due once `now >= date`.

**Per-entry rule order.** `LCOpRule::build` (`src/rgw/rgw_lc.cc:1582-1608@v19.2.6#LCOpRule::build`)
adds the actions in this order:
1. CurrentExpiration, when Days > 0 or a Date is set;
2. DMExpiration;
3. NonCurrentExpiration;
4. a CurrentTransition for each storage class;
5. a NonCurrentTransition for each storage class.

`process` (`:1617-1677#LCOpRule::process`) keeps every action whose `check()` holds and runs
only the one whose due time is strictly latest. A transition whose target class equals the
entry's class is dropped (`should_process`). The tag filter runs last: it reads
`user.rgw.x-amz-tagging`, and a delete marker passes it (`:1030-1114`). The checks:
- **CurrentExpiration** (`src/rgw/rgw_lc.cc:1116-1207@v19.2.6#LCOpAction_CurrentExpiration`,
  `src/rgw/rgw_lc.cc:1136-1227@v20.2.4#LCOpAction_CurrentExpiration`): current entries only.
  - A current delete marker with a following entry of the same name is skipped. Otherwise it
    is due by Days or Date like an object, and is removed.
  - A size filter stats the head.
  - Removal is permanent (`remove_indeed`) unless versioning is *Enabled*. Enabled makes a
    delete marker. Suspended and unversioned delete permanently.
  - **No lock check.**
- **DMExpiration** (`:1263-1308`): a delete marker with no following same-name entry, due at
  once. It is removed.
- **NonCurrentExpiration** (`:1209-1261`): non-current entries, due by the *previous* entry's
  mtime, which is when the version stopped being current. It also needs
  `num_noncurrent > NewerNoncurrentVersions`, the size filter, and `pass_object_lock_check`
  (`:304-349`). That check reads the head: a retention date after now, a legal hold ON, or an
  attr that does not decode keeps the version. ENOENT passes. The version is then removed.
- **Transition** (`src/rgw/rgw_lc.cc:1310-1532@v19.2.6#LCOpAction_Transition`,
  `src/rgw/rgw_lc.cc:1332-1588@v20.2.4#LCOpAction_Transition`): not on a delete marker.
  - CloudTiered entries are skipped. The target is the bucket's placement with the class. A
    cloud-s3 tier is excluded; otherwise `valid_placement` must hold, or EINVAL.
  - Then `RGWRados::transition_obj` (`src/rgw/driver/rados/rgw_rados.cc:5117-5184@v19.2.6#RGWRados::transition_obj`,
    `src/rgw/driver/rados/rgw_rados.cc:5417-5490@v20.2.4#RGWRados::transition_obj`):
    1. Read the head, attrs and size. An mtime other than the listed one is ECANCELED.
    2. Clear a "null" instance.
    3. Drop `user.rgw.idtag` and `user.rgw.tail_tag`.
    4. Take the owner from the ACL.
    5. `copy_obj_data` to the target placement (`:5034-5115`): a new 32-character tag, the
       raw stored bytes streamed through the atomic processor, the same mtime, the attrs
       (compression info kept, so data is not recompressed), and `olh_epoch` set to the
       entry's versioned epoch.
  - A versioned target goes through `set_olh` (`:3280-3343`). Old tails reach the GC through
    the atomic write.
  - Default retention reapplies when the object had none (`PUT_OBJ_CREATE`).
- **Removal** (`src/rgw/rgw_lc.cc:601-658@v19.2.6#remove_expired_obj`,
  `src/rgw/rgw_lc.cc:608-679@v20.2.4#remove_expired_obj`):
  - A delete-marker-making removal clears the instance. A permanent one of an empty instance
    uses "null".
  - The delete runs with the bucket's versioning status, the entry's owner as the object
    owner, and the bucket owner. `unmod_since` is the listed mtime, so an object rewritten
    since the listing survives.
  - It uses `FLAG_LOG_OP` for a delete marker or a multi-type zonegroup. A success publishes
    a notification (phase 3).
  - At v19.2.6 a head that does not exist reads as state with `exists=false` and the delete
    goes on (`src/rgw/driver/rados/rgw_rados.cc:6153-6167@v19.2.6`). At v20.2.4,
    `load_obj_state` ENOENT is tolerated only for delete markers (commit 5c82cbe78be,
    tracker #70853). The net behaviour for delete markers is the same.

**Differences between the tags in the worker**, besides notifications:
- **Page boundary.** At v19.2.6, `next_key_name` at a page's last entry answers "none" even
  when the listing is truncated (`src/rgw/rgw_lc.cc:434-443@v19.2.6`). A current delete
  marker at the end of a 1000-entry page is then removed even though older versions follow
  on the next page. v20.2.4 answers the entry's own name, so it is kept (`src/rgw/rgw_lc.cc:434-451@v20.2.4#next_key_name`;
  commit 874d2403f5e, tracker #73539; no squid backport through origin/squid). See §6.
- **Restore expirer.** v20.2.4 runs `process_expired_objects`, the cloud-restore expirer,
  after each full pass (`src/rgw/rgw_lc.cc:2079-2083@v20.2.4`). It is excluded.
- **Instance put.** The `bucket.instance` put's entry removal condition, above.

**Interactions.**
- *Versioning*: delete markers, null instances (`lc_op_ctx` gives an empty-instance
  non-current entry "null", `:487-506`), NewerNoncurrentVersions, effective mtime.
- *Object lock*: the lock wins only in noncurrent expiration and cloud transition. Current
  expiration on a lock bucket makes delete markers only, because versioning cannot leave
  Enabled there.
- *Multipart*: abort through the upload's own abort; a completed multipart object expires
  like any object; the abort headers.
- *GC*: removals and transitions enqueue tails through the ordinary write and delete paths.
- *Quota and stats*: through the index completes of those paths.
- *Cache*: none. Bucket attrs are re-read by `load_bucket`.

## 3. What rgw-go already has

At origin/main 0239625, read with `git show`/`git grep`:

| Seam | Where | Phase 2 change |
|---|---|---|
| `meta.ObjectRetention`, `meta.ObjectLegalHold`, encode/decode | `internal/meta/objectlock.go` | reuse |
| `BucketInfo.ObjLock` as an opaque RawStruct; `defaultObjLock`; `ObjLockEnabled()`; flags | `internal/meta/bucket_info.go:77-110,129-130,185-190,284-285` | add typed RGWObjectLock decode/encode in a new file, keeping the field a RawStruct, so the hot `bucket_info.go` is not edited |
| CreateBucket parses `x-amz-bucket-object-lock-enabled`, answers 501 for true | `internal/op/bucketops.go:85-88,210-216`; `internal/s3/bucket.go:116` | serve it: set both flags and the default `obj_lock` |
| Routes for `?lifecycle` (GET/PUT/DELETE), `?object-lock` (GET/PUT), `?retention` and `?legal-hold` (GET/PUT), with the payload rows | `internal/s3/dispatch.go:138,141,167,169,182,212-213,228-229,327-335` | none; only handlers are added |
| `get_lifecycle` handler: 404 without the attr, 501 with | `internal/s3/bucketsubres.go:130-136`; `internal/op/bucketsubres.go:536-576`; `op.AttrLC` | replace the 501 with rendering |
| The other routes answer 501 because no handler is bound | `internal/s3/handler.go:59-104` (`unitHandlers`, `bind`) | add handler maps |
| Lock header value checks; refusal on a bucket without lock; 501 on a lock bucket | `internal/s3/putobject.go:173-232`, `internal/s3/multipart.go:289-305`, `internal/s3/copyobject.go:80-118` | store the headers (attrs and `multipart_upload_info`) |
| 501 for writes to versioned or lock buckets | `internal/op/putobject.go:278`, `internal/driver/delete.go:34`, `internal/driver/mp_complete.go:270`, `internal/memstore/object.go:133` | versioning lifts it; this area adds the lock checks on its paths |
| Errors: InvalidRetentionPeriod, NoSuchLifecycleConfiguration, ObjectLockConfigurationNotFoundError, InvalidBucketState, BadDigest, MethodNotAllowed | `internal/op/errors.go:66-129` | reuse |
| Actions and ACL mapping for every lock and LC action | `internal/policy/action.go:61-78,161-178`; `internal/acl/perm.go:28-99`; `internal/authz/tagrule.go:140` | reuse |
| `op.ReadParamBody` (reads to EOF, verifies the signed hash) | `internal/op/parambody.go` | reuse for every body here |
| `op.RetryRacedBucketWrite`, `retryBucketWrite` (re-authorized) | `internal/op/bucketsubres.go:40-87` | reuse for PutObjectLockConfiguration |
| `SetObjectAttrs` with the 1 ns nudge | `internal/driver/attrs.go` | versioning makes it version-aware; retention and legal hold reuse it |
| Options: `lcMaxObjs` read, floored to 1 and capped at 7877 | `internal/driver/options.go:35,90,99`; `lcHashPrime` | add the other LC options |
| `ZoneParams.LCPool` decoded, defaulting to log pool + ":lc" | `internal/meta/zone_params.go:22,150,243,269` | reuse |
| `meta.StrHashLinux` | `internal/meta/bucket_layout.go:449-460` | reuse for the shard index |
| Worker registry `AddWorker`/`Run`; the GC worker's lock pattern | `internal/driver/run.go`; `internal/driver/gc.go:153-180` | the LC worker follows it |
| Seam `Pool.LockExclusive/Unlock` (EBUSY, EEXIST); `internal/cls/lock` | `internal/radosclient/cluster.go:112-119` | reuse |
| cls/rgw: no lc methods; `const.go` is a shared method-name list (`ops_mp.go` keeps its own constant) | `internal/cls/rgw/` | add `ops_lc.go` and `types_lc.go`, constants in the file |
| fakerados `RGWClass()` switch plus the `RGWWriteMethods` list; lock emulator with a settable clock | `internal/testutil/fakerados/cls_rgw.go:25-110`; `cls_lock.go` | hot spot (PP-5), §7 T0 |
| Two ISO 8601 parsers, whole seconds only: `s3.fromISO8601` (ws_terminates) and `policy.fromISO8601` (not) | `internal/s3/putobject.go:233+`; `internal/policy/convert.go:596` | retention needs nanoseconds, and output in `to_iso_8601`'s YMDhmsn |
| `ListObjectsParams.ListVersions`/`AllowUnordered`, `NameFilter`, `ObjectEntry` with IsLatest/DeleteMarker | `internal/op/bucket.go:34-87` | versioning implements versioned listing; LC needs unordered versions |
| `MultipartStore.GetUpload`/`Abort` (the completion-record abort) | `internal/op/multipart.go:194-210`; `internal/driver/mp_abort.go` | LC's abort calls these |
| A bucket instance metadata put leaves the lc entry alone, logged | `internal/driver/metadata.go:454-455,566` | set or drop it |
| Bucket deletion does not touch the lc entry | `internal/driver/bucket.go` (no `user.rgw.lc` handling) | drop it |
| s3-tests: markers lifecycle, lifecycle_expiration, lifecycle_transition, object_lock excluded; 38 object-lock node ids deselected; parity options switch LC threads off on both gateways | `hack/s3tests/run.sh`, `hack/s3tests/deselect-phase2.txt`, `hack/rooket/lib.sh:17` | harness work, §5 |
| Admin suite: `TestBucket/enable_bucket_object_lock` deselected | `hack/admin/deselect-phase2.txt` | free it |

Exclusions that phase 2 retires or rewrites in this area:
- "CreateBucket with object lock answers 501 until phase 2";
- "A stored lifecycle configuration answers 501 until phase 2";
- "No x-amz-expiration" and "No x-amz-expiration on a write";
- "No object-lock headers";
- "A multipart upload with a retention or a legal hold on a bucket with object lock answers
  501";
- the lifecycle half of "The instance's lifecycle configuration and topic mappings, which
  radosgw updates after the write, are not kept by rgw-go yet";
- the lock half of "Writes to a versioned or object-lock bucket answer 501", which versioning
  retires with it.

"CopyObject refuses an object lock on a bucket without object lock" stays.

## 4. Coexistence

### What both gateways must read and write alike

- **Bucket attrs and fields.**
  - `user.rgw.lc` holds the RGWLifecycleConfiguration. Write LCRule v6, LCExpiration v4,
    LCTransition v1 and LCFilter v4 (with RGWObjTags as `internal/tags` encodes it). The
    multimap keeps rule_map's key order.
  - `obj_lock` and `BUCKET_OBJ_LOCK_ENABLED` in the instance, written by `put_info` under the
    objv. CreateBucket with lock also sets `BUCKET_VERSIONED`.
- **Object attrs.** `user.rgw.object-retention` (v2, with the nanosecond date) and
  `user.rgw.object-legal-hold` (v1), on the version's head. A default retention is written
  in the head write itself, in the same op, as `_do_write_meta` does.
  `multipart_upload_info`'s four lock fields on the meta object (P's encoder).
- **The lc index.**
  - The same pool and namespace (`ZoneParams.LCPool`), the same shard count
    (`rgw_lc_max_objs`, read from the same mon config), and the same shard function and key
    (`tenant:name:marker`).
  - Entries are `cls_rgw_lc_entry` v1 with radosgw's status values. start_time is in whole
    seconds.
  - The head is `cls_rgw_lc_obj_head` v2, written only through the class methods. That keeps
    `radosgw-admin lc list` and either worker reading what the other wrote.
- **The lock.** `lc_process`, exclusive, on `lc.<i>` in the lc pool, for
  `rgw_lc_lock_max_time`.
  - Cookies: `"lc_thrd: <ix>"` for the workers; one random cookie per process for the
    configuration writes.
  - cls_lock names a holder by client and cookie. rgw-go is one RADOS client per process, so
    distinct worker cookies are what keep its own workers apart, as radosgw's are.
  - The lock may be held **only around shard bookkeeping**, never across a bucket's
    processing. A longer hold would starve radosgw's PutBucketLifecycle, whose
    `guard_lc_modify` gives up after 500 tries 100 ms apart (about 50 s).
- **Time.**
  - "Today", the work window and an object's age all use the gateway's *local* time zone
    (`localtime_r`, `round_to_day`). rgw-go uses Go's `time.Local`.
  - Mixed gateways with different TZ settings would each run a bucket once per their own
    day: harmless duplicate passes, not corruption. Under Rook every daemon runs the same
    image with TZ unset. This is a deployment obligation the exclusions entry states.

### Can both workers run? Yes, by radosgw's design

Ceph documents multiple LC threads per zone as supported. They spread the work
(`src/common/options/rgw.yaml.in:244-260@v19.2.6`). The arbitration:
1. **Per shard,** the `lc_process` exclusive lock serializes head and entry bookkeeping.
2. **Per bucket,** the claim is an entry set to `processing` with a start time, under that
   lock, before the head moves past it. Another worker skips a processing entry until its
   session expires: two days, or two debug intervals.
3. **Per day,** a completed entry with a start time since local midnight is skipped. The
   head's rollover date stops a second full cycle of a shard within 24 h.
4. **Within an object,** every action guards against a racing write:
   - removals carry `unmod_since` = the listed mtime;
   - transition compares the read mtime with the listed one, and its atomic head write is
     guarded by the idtag it read;
   - abort takes P's `RGWCompleteMultipart` lock and completion record.

So rgw-go's worker coexists with radosgw's when it reproduces 1-4 exactly, and does no
action outside the claim. **Recommendation:** rgw-go honours `rgw_enable_lc_threads` like
radosgw, default on, and runs the same protocol, so either or both may run. Nothing requires
exactly one.

Gaps in radosgw's protocol, each a double pass and not corruption given 4:
- a worker that stalls past the 90 s lock between the claim and the head advance;
- a re-lock failure that leaves an entry processing for two days.

rgw-go adds one guard. Before acting on a bucket it re-reads its claim under the shard lock
and finds it still its own (same status, start time and bucket). It stops a pass whose
remaining budget, `thread_stop_at`, is spent.

### Where radosgw's worker can corrupt shared data, and rgw-go does not

Each is in §6, with an exclusion:
1. Removing a current delete marker at a page boundary, which v19.2.6 does and v20.2.4 fixed.
2. Permanently deleting the current version on a versioning-suspended bucket, where S3 adds a
   null delete marker.
3. Deleting a locked object outright on a bucket whose flags carry lock without
   `BUCKET_VERSIONED`. Current expiration has no lock check.

rgw-go's worker never removes data whose retention or legal hold is in force. A delete marker
on top is permitted, because the version stays.

### Cross-gateway gate cases

Each runs one way and then the other, on one zone.
1. A lifecycle configuration (rules with Filter, And of tags, size, Noncurrent and Newer,
   Abort, Transition) PUT through one gateway:
   - GET through the other is byte-identical XML;
   - `user.rgw.lc` is byte-identical;
   - `radosgw-admin lc list` shows the entry on the same shard.
   DELETE through the other removes both.
2. PutObjectLockConfiguration through one, GetObjectLockConfiguration through the other. The
   instance's `obj_lock` bytes are identical. A CreateBucket with lock through rgw-go reads
   in radosgw as versioned with lock enabled, and the reverse.
3. A version locked through one (GOVERNANCE, COMPLIANCE, legal hold) refuses DeleteObject and
   DeleteObjects through the other. A bypass with the permission succeeds through either.
   A mode change and a shortening are refused alike.
4. Default retention: a rule set through radosgw, a PUT, copy or multipart completion through
   rgw-go. The retention decodes to {mode, mtime + period} as radosgw computes it, with Years
   as 365.2425 days. Then the reverse.
5. A worker pass by rgw-go over objects radosgw wrote, then read through radosgw:
   - expiration (a delete marker on versioned, removal on unversioned);
   - noncurrent expiration with NewerNoncurrentVersions;
   - delete-marker expiration;
   - abort of an upload radosgw initiated;
   - transition, with the object readable through radosgw from the new class's pool and
     `radosgw-admin bucket check` clean.
   Then the same with radosgw's worker over rgw-go's objects.
6. **Mixed workers.** Both workers on, with `rgw_lc_debug_interval` set, over a population of
   buckets across shards, for several debug days. Each entry is complete with one start time
   per day; no action happens twice (one delete marker per expired key); no locked version is
   removed; the head cycles.
7. **Worker crash.** Kill rgw-go mid-bucket. radosgw skips the processing entry until the
   session expires, then reclaims it, and the reverse.
8. **Side effects.** A bucket with lifecycle deleted through rgw-go leaves no lc entry. A
   `bucket.instance` metadata put through rgw-go's admin API with and without `user.rgw.lc`
   sets and drops the entry as radosgw does.

Cases 5-7 need a disposable cluster with both gateways' LC threads on. The phase 1 parity
options switch them off, so these runs use a separate configuration.

## 5. Tests

### s3-tests (pin f6f2529dd9ef, `hack/s3tests/PIN`)

Markers this area removes from `EXCLUDED_MARKERS`: `lifecycle`, `lifecycle_expiration`,
`lifecycle_transition` and `object_lock`. `object_lock` marks no test at the pin, but should
go too. `cloud_transition`, `cloud_restore` and `target_by_bucket` stay, under "Cloud
transition and restore": 21 tests.

Lifecycle tests freed, 41. The 12 marked * also carry `versioning`, so they run only once the
versioning area drops that marker.
- Configuration:
  `test_lifecycle_set`, `_get`, `_delete`, `_get_no_id`, `_id_too_long`, `_same_id`,
  `_invalid_status`, `_set_date`, `_set_invalid_date`, `_set_noncurrent`,
  `_set_deletemarker`, `_set_filter`, `_set_empty_filter`, `_set_multipart`,
  `_transition_set_invalid_date`, `_set_noncurrent_transition`.
- Worker:
  `test_lifecycle_expiration`, `test_lifecyclev2_expiration`,
  `_expiration_versioning_enabled`*, `_expiration_tags1`, `_expiration_tags2`,
  `_expiration_versioned_tags2`*, `_expiration_noncur_tags1`*,
  `_expiration_newer_noncurrent`*, `_expiration_size_gt`*, `_expiration_size_lt`*,
  `_expiration_date`, `_expiration_days0`, `_noncur_expiration`*, `_deletemarker_expiration`*,
  `_deletemarker_expiration_with_days_tag`*, `_multipart_expiration`, `_transition`,
  `_transition_single_rule_multi_trans`, `_noncur_transition`*,
  `_plain_null_version_current_transition`*, `test_delete_marker_expiration`*.
- Headers:
  `_expiration_header_put`, `_expiration_header_head`, `_expiration_header_tags_head`,
  `_expiration_header_and_tags_head`.

Object-lock tests freed: the 38 lines under "# object lock" in
`hack/s3tests/deselect-phase2.txt`, plus `test_object_lock_put_obj_lock_enable_after_create`,
which carries `versioning`. Every one creates a bucket with lock, so all of them wait for
versioned writes.

**No radosgw outcome is recorded** for any of these at either floor: they are outside the
phase 1 set (searched `test/s3tests/baseline/{squid,tentacle}.json`). The task that frees
them records them with `make s3tests-record-tests` on both releases. So whether radosgw fails
any of them at a floor is unknown until then. Expect the time-driven worker tests to be
recorded `unstable` where two runs disagree.

Harness changes they need, a T-layer task on a cluster:
- `rgw_lc_debug_interval=10` for both entities, and `lc_debug_interval` left at s3-tests'
  default of 10 (`s3tests/functional/__init__.py:288-290` at the pin).
- LC threads **on for the gateway under test only**. The parity options switch them off on
  both (`hack/rooket/lib.sh:17`), and radosgw reads them only at startup, so the radosgw is
  restarted between modes.
- `storage_classes` in `[s3 main]` naming two extra classes the zone has. The transition
  tests skip below three (`test_lifecycle_transition` at the pin). Plain classes are added to
  the default placement by `hack/rooket/populate.sh`, which today adds compression classes
  only.

Known differences to add (`test/s3tests/known-differences-*.txt`), each backed by an
exclusion: the header-shape and suspended-expiration choices of §6, where a recorded test
touches them. None is known to be touched before the radosgw outcomes are recorded.

### go-ceph rgw/admin suite

- `TestRadosGWTestSuite/TestBucket/enable_bucket_object_lock`, freed from
  `hack/admin/deselect-phase2.txt`. It does CreateBucket with lock, then checks that
  `GetBucketInfo` reports `object_lock_enabled`. Phase 1's admin dump already renders the
  flag (`internal/admin/bucket.go:152`).
- No admin case touches lifecycle.

### Rook

The `bucket/lifecycle` sub-test (`tests/integration/object/bucket/lifecycle/lifecycle.go` in
rook) puts, updates and removes an OBC `bucketLifecycle` through Put, Get and
DeleteBucketLifecycle. Its rules have `Prefix: ""` outside Filter, Abort, and Expiration
Days. D1 left it to phase 2: drop it from the workflow's patch once PUT and DELETE are served.

### Specs the plan must require

- **fakerados**: the seven lc methods over a shard's omap and header, as §2.1 gives them:
  - ENOENT on a missing object for the reads, while set_entry creates it;
  - get_next_entry after the marker;
  - list_entries replying at the request's struct_v and reading legacy values;
  - write flags registered as radosgw's.
  The lock emulator and its clock exist.
- **Encodings**: corpus goldens for `RGWObjectLock`, `ObjectLockRule`, `DefaultRetention`,
  `RGWLifecycleConfiguration`, `LCRule`, `LCExpiration`, `LCFilter`, `cls_rgw_lc_entry`,
  `cls_rgw_lc_get_entry_ret`, `cls_rgw_lc_set_entry_op` and `cls_rgw_lc_rm_entry_op`. All are
  in the ceph checkout's `ceph-object-corpus`, LCFilter from 15.0.0 and cls_rgw_lc_entry from
  16.2.0. Generated vectors for `LCTransition`, `cls_rgw_lc_obj_head` and the
  get_next_entry, list_entries, put_head and get_head structs, which the corpus lacks.
- **XML**:
  - the object-lock and lifecycle grammars, error by error, in radosgw's order (§2.2-2.3);
  - the dumps byte for byte: element order, Days-or-Years, Filter-or-Prefix, And when there
    is more than one condition, 9-digit dates;
  - the wrong-root LifecycleConfiguration read as empty;
  - integers per `decode_xml_obj`;
  - a 48-character random ID.
- **Evaluation** (pure, table-driven):
  - `retention_period_valid`;
  - `get_lock_until_date` with days, years as 365.2425 days, and the overflow choice of §6;
  - `verify_object_lock`'s matrix (mode × bypass header × permission × past or future × legal
    hold × an attr that does not decode);
  - the PutObjectRetention transition matrix of §2.2;
  - `obj_has_expired` against local midnight and debug mode;
  - `LCOpRule` selection: latest due wins, transition same-class dropped, tag filter after
    the actions, a delete marker passing the filter;
  - NewerNoncurrentVersions counting; effective mtime from the previous entry; the
    page-boundary rule of v20.2.4 on both releases.
- **Worker protocol** on fakerados with a fake clock:
  - shard order random;
  - lock skip on EBUSY;
  - the claim (processing plus start time, then the head advance) under the lock, and the
    unlock before bucket work;
  - post-states (ENOENT removes, error fails, else complete);
  - rollover after 24 h only; already-run-today skip; expired-session reclaim;
  - debug interval, work window and next start in a non-UTC `time.Local`.
- **Two workers in one fakerados cluster** (two RADOS clients), standing in for radosgw and
  rgw-go: no bucket processed twice per day, every bucket processed.
- **Config ops** on counterfeiter fakes: each error in order; `guard_lc_modify`'s retry
  budget; the attr stored before the entry; DELETE's 204 on a bucket without one.
- **Integration** (`//go:build integration`, disposable cluster): the gate cases of §4, 1-8.
  The `radosgw-admin lc list/get` output agrees with what rgw-go wrote.

## 6. Floor defects

The owner's rule of 2026-10-13 applies (§1). Every new entry starts **unreproduced**:
derived from the source at both tags, and not run on a cluster. Each task that records one
budgets a reproduction on a disposable cluster (PP-11). A security entry goes to the private
channel once it reproduces (code-conventions `bug-reports.md`).

### New, for `docs/ceph-upstream-bugs.md`

| # | Defect | Releases | Class | rgw-go's choice |
|---|---|---|---|---|
| D1 | A current delete marker at the end of a 1000-entry listing page is removed although older versions follow on the next page. The newest of them becomes current again (`src/rgw/rgw_lc.cc:434-443@v19.2.6`) | v19.2.6. Fixed in v20.2.4 (tracker #73539, commit 874d2403f5e); no squid backport | data corruption | v20.2.4's rule on both releases; exclusion for Squid. Prior art upstream, so nothing to file |
| D2 | Current expiration on a **versioning-suspended** bucket deletes the current version permanently (`remove_indeed = !versioning_enabled()`). An older version may resurface. S3's expiration in a suspended bucket writes a null delete marker (`src/rgw/rgw_lc.cc:1187-1191@v19.2.6`; same at v20.2.4) | both tags | data corruption | S3's: a delete marker with no instance under Suspended, which is the null delete marker radosgw's own DeleteObject writes there, so RADOS-compatible. Exclusion; filed once reproduced. Open question Q2 |
| D3 | Current expiration never checks object lock. Where its removal is permanent it deletes a retained or held object. This happens on a bucket whose flags carry lock without `BUCKET_VERSIONED`, a state a `bucket.instance` metadata put can write (registry, "A purging bucket removal refuses..."), and under D2 (`src/rgw/rgw_lc.cc:1116-1207@v19.2.6#LCOpAction_CurrentExpiration`) | both | data corruption | Every permanent LC removal runs the lock check that noncurrent expiration has: retention date after now, legal hold ON, or an attr that does not decode keeps the object. Exclusion |
| D4 | The lock-until date wraps. `get_lock_until_date` adds the period in u64 nanoseconds, so a default period ending after 2554-07-21 wraps, often to a past date, and the object is unprotected at once. `from_iso_8601` wraps an explicit date past 2554 the same way. Only a wrap into the past is caught, by the "in the future" checks (`src/rgw/rgw_object_lock.cc:87-95@v19.2.6`; registry, "radosgw's date conditions wrap past 2554"). Tracker #56993's 2023 fix (dd87a0ff845) removed an int overflow; the later chrono form wraps again | both, and main | security (fail-open of WORM retention) | Refuse what cannot be represented: a rule whose period from now passes the real_time range is InvalidRetentionPeriod; an explicit date past it is the error its parser gives a bad date; and a default retention computed at write time is saturated at the maximum, fail-closed. Exclusion. Open question Q3 |
| D5 | DeleteObjects checks s3:BypassGovernanceRetention once, on the **bucket** ARN (`src/rgw/rgw_op.cc:6774-6777@v19.2.6`, `src/rgw/rgw_op.cc:7713-7716@v20.2.4`). A policy Deny scoped to `arn:aws:s3:::b/*` does not bind it, while DeleteObject honours the same Deny on the object ARN, so a principal refused the bypass removes GOVERNANCE-locked versions through DeleteObjects | both | security (authorization bypass) | Evaluate the bypass per key on the object's ARN, as DeleteObject does. Exclusion |
| D6 | `x-amz-expiration` reports a NoncurrentVersionExpiration rule for a current object, because the OLH-resolved key carries an instance. It reads that rule's date from the current Expiration (`src/rgw/rgw_lc.cc:2846-2861@v19.2.6`) | both. Fixed after both floors (tracker #77564; squid 4dac1850993, tentacle 30daa120099), in no release | S3-correctness | The fixed behaviour on both: the current Expiration rule only, and no header for a request naming a version. Exclusion |
| D7 | v19.2.6 stores a lifecycle configuration on an indexless bucket, which the worker cannot list. v20.2.4 answers 405 MethodNotAllowed | v19.2.6 | S3-correctness | v20.2.4's 405 on both. Exclusion for Squid |
| D8 | PutObjectRetention refuses COMPLIANCE to GOVERNANCE, and a shorter date without the bypass, even after the existing retention has **expired** (`src/rgw/rgw_op.cc:8400-8417@v19.2.6`) | both | S3-correctness (fail-closed) | Open question Q4. Recommended: treat an expired retention as none, which only ever allows a new protection. Exclusion |
| D9 | PutBucketLifecycle's attr write is one objv-guarded attempt, so a racing bucket write fails the request with 409. The other subresource PUTs retry (`src/rgw/rgw_lc.cc:2649-2653@v19.2.6`) | both | cosmetic | Retry as `op.RetryRacedBucketWrite` does, re-authorized. Exclusion |
| D10 | PutBucketLifecycle compares 16 bytes of the decoded Content-MD5 whatever its decoded length, reading past a short value (`src/rgw/rgw_op.cc:5969-5977@v19.2.6`) | both | cosmetic: the answer is BadDigest either way | A decoded length other than 16 is BadDigest; no S3 difference, so no exclusion |

Reproduced as radosgw does, with no difference: the attr stored when `guard_lc_modify` then
fails to lock (S3-correctness, minor); the lc entry left `processing` for two days after a
failed re-lock; LC's day boundary in local time.

### Existing entries this area meets

- "radosgw faults on a zero rgw_gc_max_objs, rgw_lc_max_objs or rgw_usage_max_shards"
  (#80991): handled by `floorShards`.
- "radosgw's from_base64 reads before an all-'=' input" (security): PutBucketLifecycle's
  Content-MD5 is a caller. rgw-go answers BadDigest through its own decoder.
- "radosgw ignores a payload-hash mismatch on bodies read by read_all_input" (security):
  every body here goes through `ReadParamBody`.
- "radosgw's SigV2 resource lists encryption and object-lock but never signs them":
  reproduced for `?object-lock`.
- "radosgw's bucket instance metadata put drops the bucket's object lock rule" (#71157):
  rgw-go keeps the rule. The typed RGWObjectLock must preserve that.
- "radosgw's CopyObject stores an object lock on a bucket without object lock": rgw-go
  refuses, and keeps refusing.
- "Squid's DeleteObjectTagging stamps the object with the epoch plus one nanosecond": a
  Squid radosgw's stamped object expires as decades old under rgw-go's worker too. It is the
  stored mtime, so rgw-go reads it as radosgw does.
- "Squid labels a copy to another storage class with its source's class": LC reads the
  index entry's class. rgw-go's copies are labelled right.
- "A multipart completion whose meta object outlives its head write..." (#80896): LC's abort
  uses rgw-go's `Abort`, which leaves a completed upload's parts alone.

## 7. Proposed tasks

Prefixes: **L** for object lock, **C** for lifecycle, **T0** for the shared prerequisites this
area proposes to the plan index; the index assigns T0's owner. "Cluster" marks a task that
needs a disposable rooket cluster. Every task records its differences in `docs/exclusions.md`
and its defects in `docs/ceph-upstream-bugs.md` in the same PR.

### Seams with the other areas

What this area needs from **versioning**, by the interface it must expose:
- **V1.** PutBucketVersioning refuses any state but Enabled on a lock bucket: 409
  InvalidBucketState, "bucket versioning cannot be disabled on buckets with object lock
  enabled" (`src/rgw/rgw_op.cc:2788-2793@v19.2.6`, `src/rgw/rgw_op.cc:3020-3025@v20.2.4`). Versioning owns the
  op; L2 supplies the spec case.
- **V2.** Versioned create writes (PUT, the streaming copy, CompleteMultipartUpload, and the
  rewrite of V6) assemble the head's attrs at one point, given the bucket record and the
  `set_mtime` the write stamps. L5 adds the default retention there.
- **V3.** `ObjectStore.DeleteObject` on a versioned bucket:
  - with an instance (`null` included), it removes that version;
  - without one, it writes a delete marker, the null one under Suspended.
  `op.DeleteParams` carries `UnmodifiedSince` (exists) and a new `MarkerOwner meta.Owner`:
  the requester for S3, the entry's owner for LC, as `remove_expired_obj` sets `obj_owner`.
  It also carries `Expect *op.ObjectState`, the state a caller's check read, which guards the
  removal on that state's idtag. L4 needs it so that the lock check and the delete see the
  same head.
- **V4.** `ListObjects` with `ListVersions` and `AllowUnordered`. Each key's entries are
  adjacent and newest first within a shard, a null version's instance is empty, and paging
  resumes mid-key. Each `ObjectEntry` gains `VersionedEpoch`, a current flag (`is_current`)
  and `Category` (to skip CloudTiered entries).
- **V5.** `StatObject` and `SetObjectAttrs` resolve a plain key on a versioned bucket to the
  current version (OLH follow). A delete marker or a missing version is ENOENT. This is
  `modify_obj_attrs`'s semantics (`src/rgw/driver/rados/rgw_sal_rados.cc:2401-2419@v19.2.6#RadosObject::modify_obj_attrs`).
- **V6.** A head write of an existing instance at an explicit `OLHEpoch` that links it
  without promoting it past a newer version (`set_olh` from `_do_write_meta`,
  `src/rgw/driver/rados/rgw_rados.cc:3337-3343@v19.2.6`). It serves the transition of a
  noncurrent version.

From **writepath**:
- a placement and storage-class resolver for a target class within the bucket's placement,
  which is `valid_placement`;
- the constraint that the transition's raw rewrite gets no compression or checksum filter,
  because `copy_obj_data` streams the stored bytes and keeps the compression and checksum
  attrs (`src/rgw/driver/rados/rgw_rados.cc:5034-5115@v19.2.6`).

From **bucketconfig**: nothing beyond the shared handler maps, `PutBucketAttrs` and `xmltext`.

For **phase 3**: a no-op `lifecycle.Notifier` the worker calls where radosgw publishes,
filled by notifications.

### Hot files, and how not to serialize on them

| File | Touched by | Proposal |
|---|---|---|
| `internal/testutil/fakerados/cls_rgw.go` (`RGWClass` switch, `RGWWriteMethods`) | versioning (OLH methods), C4 | **T0a**: a method table, `map[string]rgwMethod{fn, write}`, assembled from per-file tables (`cls_rgw_index.go`, `cls_rgw_olh.go`, `cls_rgw_lc.go` ...), with `RGWWriteMethods` derived from it. Lands first |
| `internal/s3/handler.go` (`unitHandlers`, `NewHandler`) | every area | **T0b**: one predeclared per-area map function each, in its own file (`lockHandlers`, `lifecycleHandlers` ...), stubbed empty |
| `internal/op/env.go` (`Env`) | several areas | T0b adds `Env.Lifecycle op.LifecycleStore`, with the other areas' fields |
| `internal/cls/rgw/const.go` | versioning, C3 | not edited: the method constants live in `ops_lc.go`, as `ops_mp.go` does |
| `internal/meta/bucket_info.go` | L1, bucketconfig (website) | not edited: `ObjLock` stays a RawStruct, and typed accessors live in `meta/objectlock.go` |
| `internal/driver/headwrite.go`, `put.go`, `copy.go`, `mp_complete.go` | versioning (V2), writepath, L5 | versioning's head-write task creates the attrs-finalizing point (V2); L5 adds one call there after it |
| `internal/s3/getobject.go`, `putobject.go`, `multipart.go`, `copyobject.go` | versioning, writepath (checksum headers), L5, C8 | order: versioning → writepath's header task → L5 → C8. Each adds headers in its own helper function, one call line per site |
| `internal/op/deleteobject.go`, `deleteobjects.go` | versioning, L4 | L4 after versioning's delete task |
| `internal/op/bucketsubres.go` (holds `GetBucketLifecycle`) | bucketconfig, C6 | C6 moves the lifecycle op to `op/lifecycle.go` |
| `internal/driver/options.go` | C5, writepath | line-local additions |
| `hack/s3tests/run.sh` (`EXCLUDED_MARKERS`), `deselect-phase2.txt` | every area's last task | line-local; each area deletes only its own lines |
| `docs/exclusions.md`, `docs/ceph-upstream-bugs.md` | every task | the plan index's PP-5 ruling (per-entry fragments or an append-only region) |

### Tasks, in order

**T0a. fakerados rgw class as a table** (emulator; shared with versioning). Convert
`RGWClass` to a table assembled from per-file method tables, each entry with its
`CLS_METHOD_WR` flag. Derive `RGWWriteMethods`, so existing callers are unchanged. Add an empty
`cls_rgw_lc.go`. Specs: the existing `cls_rgw*_test.go` suites pass unchanged; a method absent
from every table is EOPNOTSUPP; the derived write list equals today's list. No dependency.
One PR. About 80 lines.

**T0b. Handler and Env slots** (s3, op; shared with every area). Predeclare a per-area
handler-map function each in its own file, bound by `NewHandler` with the existing panics, and
add each area's `Env` field. Specs: binding a stubbed map changes no route. One PR. About 80
lines.

**L1. Object-lock types, XML and evaluation** (pure: `internal/objectlock`, `internal/iso8601`,
`internal/meta/objectlock.go`).
- `meta.ObjectLock` {Enabled, Rule *DefaultRetention{Mode, Days, Years int32}}: decode and
  encode at v1, parsed from and written to `BucketInfo.ObjLock`, nil meaning the default
  (`src/rgw/rgw_object_lock.h:11-149@v19.2.6`).
- XML: `ObjectLockConfiguration`, `Retention` and `LegalHold`, in and out, with §2.2's errors
  (`src/rgw/rgw_object_lock.cc:8-141@v19.2.6`).
- `RetentionPeriodValid`.
- `LockUntil(rule, mtime)`: days × 86400 s, or years × 31556952 s, saturated at the maximum
  real_time (D4).
- `VerifyDelete(attrs, now, bypassHeader, bypassAllowed)`, which is `verify_object_lock`
  (`src/rgw/rgw_common.cc:1639-1667@v19.2.6`).
- `RetentionChange(old, new, now, bypass)`: §2.2's matrix, with D8's choice.
- `iso8601`: `from_iso_8601` with `ws_terminates` to u64 nanoseconds, and `to_iso_8601`
  YMDhmsn. `s3.fromISO8601` calls it.

Specs: corpus goldens (RGWObjectLock, ObjectLockRule, DefaultRetention, RGWObjectRetention,
RGWObjectLegalHold); XML error tables; the `VerifyDelete` matrix; years as 365.2425 days; the
saturation and its boundary. Depends on nothing. PR split: (a) `iso8601` and the s3 refactor;
(b) the meta encodings with goldens; (c) objectlock XML and evaluation. About 220 lines.

**L2. Bucket object-lock configuration ops and CreateBucket with lock** (op + s3: new
`op/objectlock.go`, `s3/objectlock.go`; edits `op/bucketops.go`).
- PutObjectLockConfiguration in §2.2's order: the versioning check before the body; then
  `ReadParamBody`, the parse and the period. Then under `retryBucketWrite`, set the flag and
  `ObjLock` and call `PutBucketInfo`.
  `src/rgw/rgw_op.cc:8217-8293@v19.2.6`, `src/rgw/rgw_op.cc:9165-9241@v20.2.4`.
- GetObjectLockConfiguration: 404 ObjectLockConfigurationNotFoundError, or the document.
- CreateBucket with lock sets `BUCKET_VERSIONED|BUCKET_OBJ_LOCK_ENABLED` and the default
  `obj_lock` (`src/rgw/driver/rados/rgw_rados.cc:2391-2393@v19.2.6`). It retires the 501
  exclusion.

Specs: each error in order on counterfeiter fakes; the 409 before a malformed body; a
re-authorized retry; memstore and driver round trips; V1's case, written here for
versioning's op. Depends on L1 and T0b. PR split: (a) the configuration ops; (b) CreateBucket
with lock. About 170 lines.

**L3. Object retention and legal hold ops** (op + s3: `op/objectlock_object.go`).
- PutObjectRetention: get_params in verify_permission, the bypass permission only when the
  header is sent, then §2.2's execute order. It writes through `SetObjectAttrs`, which is V5.
  `src/rgw/rgw_op.cc:8321-8422@v19.2.6`, `src/rgw/rgw_op.cc:9269-9383@v20.2.4`.
- GetObjectRetention, PutObjectLegalHold and GetObjectLegalHold as §2.2 gives them; `versionId`
  is honoured. A missing key follows the phase-1 object-read 404 path, including Tentacle's
  `x-amz-delete-marker` (`src/rgw/rgw_rest.cc:1936-1946@v20.2.4`).

Specs: the transition matrix through the op; InvalidRequest without lock; a past date; a
retention attr that does not decode is 500; the 1 ns mtime nudge on the attr write (the
existing registry quirk). Depends on L1, T0b and V5. One PR. About 180 lines.

**L4. Delete enforcement** (op + s3: edits `op/deleteobject.go`, `op/deleteobjects.go`,
`s3/deleteobject.go`, `s3/deleteobjects.go`).
- The handlers read `x-amz-bypass-governance-retention` (url-decoded, "true" in any case).
- When the bucket has lock and the key names a version, the op stats that version. ENOENT
  skips the check. Otherwise it runs `objectlock.VerifyDelete` and deletes with
  `Expect = that state` (V3).
- DeleteObject's bypass permission is on the object ARN. DeleteObjects' is per key on each
  object ARN (D5), where radosgw checks once on the bucket.
- The refusal is 403 AccessDenied, with "forbidden by object lock" on DeleteObject.
- `src/rgw/rgw_op.cc:5138-5221@v19.2.6`, `:6768-6866`; `src/rgw/rgw_op.cc:5528-5610@v20.2.4`, `:7707-7805`.

Specs: the matrix through both ops; a delete marker passes; a delete without versionId is
never checked; a racing retention change fails the guarded delete. Depends on L1 and V3. One
PR. About 160 lines.

**L5. Lock on the write path** (driver + s3: the V2 point, `s3/putobject.go`,
`s3/multipart.go`, `s3/copyobject.go`).
- The header retention and legal hold become attrs on PutObject and CopyObject, as
  `src/rgw/rgw_op.cc:4542-4551@v19.2.6` and `:5584-5593` store them, and fields in
  `op.UploadParams` for CreateMultipartUpload. `meta.MultipartUploadInfo` and the completion
  already carry them (`internal/meta/multipart.go:197-253`, `driver/mp_complete.go:353-357`).
- The default retention is added at V2's point exactly when radosgw's `_do_write_meta` adds
  it: a create write, a rule present and no retention attr
  (`src/rgw/driver/rados/rgw_rados.cc:3179-3189@v19.2.6`, `src/rgw/driver/rados/rgw_rados.cc:3308-3318@v20.2.4`). Its date is
  `LockUntil(set_mtime)`.
- It retires "A multipart upload with a retention or a legal hold ... answers 501" and the
  lock clause of the write refusal.

Specs:
- the attr bytes for each write kind;
- none on part heads or the meta object;
- an explicit header retention wins over the default;
- the default's date from the stamped mtime.

Depends on L1, V2 and versioning's versioned writes. PR split: (a) the driver default
retention; (b) the s3 header storage and the retired refusals. About 190 lines.

**C1. Lifecycle types, encodings and XML** (pure: `internal/lifecycle`).
- Encodings: RGWLifecycleConfiguration, LCRule, LCExpiration, LCTransition and LCFilter at
  §2.1's versions, the decoders taking every version the C++ accepts.
- XML parse, giving §2.3's grammar errors and `rebuild` → `check_and_add_rule` errors in
  order, with `rgw_lc_max_rules`. A wrong root is empty, and an empty ID gets 48 random
  characters.
- Dump in rule_map order with the xmlns.
- `prefix_map` building (`_add_rule`): the canonical storage class, the prefix from the
  filter or the rule.
- `src/rgw/rgw_lc_s3.cc:19-369@v19.2.6`; `src/rgw/rgw_lc.cc:60-199@v19.2.6`.

Specs: corpus goldens (RGWLifecycleConfiguration, LCRule, LCExpiration, LCFilter); generated
vectors for LCTransition; XML error and dump tables; Rook's two documents round-tripping as
Rook's comparison expects. Depends on nothing. PR split: (a) encodings; (b) XML and
validation. About 230 lines.

**C2. Lifecycle evaluation** (pure: `internal/lifecycle`).
- `Expired(mtime, days, now, debugInterval)` against local midnight
  (`src/rgw/rgw_lc.cc:272-302@v19.2.6#obj_has_expired`; `src/include/utime.h:218-227@v19.2.6`).
- The action set and the selection of `LCOpRule::build` and `process`: the latest due wins,
  a same-class transition is dropped, filters run after the actions
  (`src/rgw/rgw_lc.cc:1582-1677@v19.2.6`).
- Per-entry decisions for the four actions (§2.4), given the lister's state (next key name
  with v20.2.4's page-boundary rule, D1; `num_noncurrent`; effective mtime) and an injected
  lock predicate, which also guards permanent current removals (D3) and makes the
  suspended-bucket decision D2.
- The `x-amz-expiration` value in D6's fixed form, and the abort date and rule
  (`src/rgw/rgw_lc.cc:2750-2951@v19.2.6`).

Specs: table-driven over entry sequences, including a delete marker at a page boundary, a
suspended bucket, a lock bucket without `BUCKET_VERSIONED`, NewerNoncurrentVersions 0/1/2,
size and tag filters, header dates around midnight in a non-UTC `time.Local`. Depends on C1
and L1. One PR. About 210 lines.

**C3. cls_rgw lc client** (cls: `internal/cls/rgw/ops_lc.go`, `types_lc.go`). The seven
methods and their op and reply structs, `cls_rgw_lc_entry` and `cls_rgw_lc_obj_head`, at §2.1's
versions. list_entries is sent at struct_v 3 and decodes the reply's legacy forms. Constants
live in the file. `src/cls/rgw/cls_rgw_ops.h:1183-1456@v19.2.6`;
`src/cls/rgw/cls_rgw_client.cc:943-1067@v19.2.6`. Specs: corpus goldens (cls_rgw_lc_entry,
cls_rgw_lc_get_entry_ret, cls_rgw_lc_set_entry_op, cls_rgw_lc_rm_entry_op); generated vectors
for the rest; marshalling against a recording Execer. Depends on nothing. One PR. About 150
lines.

**C4. fakerados lc methods** (emulator: `fakerados/cls_rgw_lc.go`, table entries per T0a).
§2.1's semantics: `src/cls/rgw/cls_rgw.cc:4175-4359@v19.2.6`. Specs: each method against C3's
client; ENOENT on missing shards; get_next_entry ordering; legacy list values. Depends on T0a
and C3. One PR. About 120 lines.

**C5. lc shard store and options** (driver: `driver/lc_shards.go`, `op/lifecycle.go`
interface, `driver/options.go`).
- The shard names and the index function; the key `tenant:name:marker`.
- `guardLCModify`: the RGWLC-cookie lock on the shard, retried 500 times 100 ms apart, then
  the entry write or removal (`src/rgw/rgw_lc.cc:2583-2634@v19.2.6`).
- Head and entry helpers.
- Options: `rgw_enable_lc_threads`, `rgw_lc_lock_max_time`, `rgw_lc_max_worker`,
  `rgw_lc_max_wp_worker`, `rgw_lc_thread_delay`, `rgw_lc_debug_interval`,
  `rgw_lifecycle_work_time` (parsed as `sscanf("%d:%d-%d:%d")`), `rgw_lc_max_rules`,
  `rgwlc_auto_session_clear`, `rgwlc_skip_bucket_step`.
- `op.LifecycleStore`: `SetBucketConfig(ctx, rec, encoded)` and
  `RemoveBucketConfig(ctx, rec, updateAttrs bool)`.

Specs on fakerados: the shard of a known key (vector computed through `meta.StrHashLinux`);
lock contention (EBUSY and EEXIST retried, giving up after the budget with the attr kept);
lifecycle options at defaults and garbage values. Depends on C3, C4 and T0b. One PR. About 180
lines.

**C6. Lifecycle configuration ops** (op + s3: `op/lifecycle.go`, `s3/lifecycle.go`; moves
`GetBucketLifecycle` out of `op/bucketsubres.go`; drops `get_lifecycle` from `s3/bucket.go`).
- PUT in §2.3's order: the v20.2.4 indexless 405 on both releases (D7); Content-MD5 (a decoded
  length other than 16 is BadDigest, D10); the body through `ReadParamBody`; the parse and
  rebuild; the attr through `RetryRacedBucketWrite` (D9); then the entry.
- GET renders the configuration. DELETE erases the attr through `PutBucketInfo`, then the
  entry, and answers 204.
- `src/rgw/rgw_op.cc:5759-6044@v19.2.6`, `src/rgw/rgw_op.cc:6405-6714@v20.2.4`;
  `src/rgw/rgw_rest_s3.cc:3658-3716@v19.2.6`. It retires the 501 exclusion.

Specs: each error in order; Content-MD5 cases including all-'='; wrong root; the entry written
after the attr; 204 without a configuration. Depends on C1, C5 and T0b. PR split: (a) PUT and
DELETE; (b) GET. About 200 lines.

**C7. lc entry side effects** (driver: `driver/bucket.go` deletion, `driver/metadata.go`
instance put).
- Bucket removal drops the entry when the bucket carried `user.rgw.lc`, ignoring the result
  (`src/rgw/driver/rados/rgw_sal_rados.cc:402-407@v19.2.6`).
- An instance put sets it when the new attrs carry the attr, and drops it when only the old
  ones did. That is v20.2.4's condition on both releases; v19.2.6's extra removal of an absent
  key changes no state (`src/rgw/driver/rados/rgw_bucket.cc:2900-2928@v19.2.6`,
  `src/rgw/driver/rados/rgw_bucket.cc:3010-3040@v20.2.4`).
- It retires the exclusion's lifecycle clause.

Specs on fakerados. Depends on C5. One PR. About 100 lines.

**C8. Lifecycle response headers** (s3: `s3/getobject.go`, `s3/putobject.go`, `s3/multipart.go`).
- `x-amz-expiration` on GET, HEAD and PutObject from C2, with no header when the request names
  a version (D6).
- `x-amz-abort-date` (the time-header format) and `x-amz-abort-rule-id` on
  CreateMultipartUpload.
- The call sites are §2.3's. It retires both "No x-amz-expiration" exclusions.

Specs: the s3-tests header cases as unit cases; tags taken from the object's stored tags on
GET, as `rgw_cond_decode_objtags` takes them (`src/rgw/rgw_op.cc:2198-2212@v19.2.6`). Depends
on C2, after writepath's header task (hot files). One PR. About 140 lines.

**C9. Worker: the shard protocol** (driver: `driver/lc_worker.go`; one `AddWorker` line).
`rgw_lc_max_worker` goroutines when `rgw_enable_lc_threads` is set, each running:
- `should_work`, the next start and the debug interval, all in `time.Local`;
- a random shard order per pass;
- §2.4's shard pass exactly: the claim and head advance under the `lc_process` lock with
  cookie `"lc_thrd: <ix>"`; the unlock before bucket work; the post-states; rollover;
  already-run-today; expired sessions; the stop budget (`thread_stop_at`).
- rgw-go's one addition: re-check its claim under the lock before starting the bucket (§4).
- `src/rgw/rgw_lc.cc:1994-2570@v19.2.6`, `src/rgw/rgw_lc.cc:2051-2637@v20.2.4`.
- The bucket pass is an interface C10 fills.

Specs on fakerados with a fake clock (§5 "worker protocol" and "two workers"). Depends on C5.
One PR. About 230 lines.

**C10. Worker: the bucket pass and its actions** (driver: `driver/lc_bucket.go`,
`lc_actions.go`).
- The key split, the bucket load and marker check, the config decode
  (`src/rgw/rgw_lc.cc:1679-1830@v19.2.6`, `src/rgw/rgw_lc.cc:1735-1886@v20.2.4`).
- Per rule: the versioned unordered listing (V4) in pages of 1000 with
  `rgw_lc_thread_delay`, and bounded concurrency of `rgw_lc_max_wp_worker`. The bound is a
  semaphore of goroutines; every entry of a rule finishes before the next rule starts.
- C2's decisions, executed:
  - removals through V3, with `UnmodifiedSince` = the listed mtime and `MarkerOwner` = the
    entry's owner;
  - a delete marker under Enabled, and under Suspended too (D2);
  - permanent removals lock-checked (D3, and noncurrent as radosgw does);
  - the multipart abort through `MultipartStore.GetUpload` and `Abort` for expired meta
    entries (`:886-1000`).
- `rgwlc_skip_bucket_step`. The `Notifier` hook (no-op).

Specs on fakerados and memstore: each action's RADOS effect; the guard against a write
racing the listing; a locked noncurrent kept; abort of an upload with a completion record
leaves its parts. Depends on C9, C2, L1, V3, V4 and P's abort. PR split: (a) expiration and
delete-marker actions; (b) noncurrent; (c) multipart abort. About 250 lines.

**C11. In-cluster transition** (driver: `driver/lc_transition.go`; uses W's raw-copy path).
`transition_obj` as §2.4 gives it (`src/rgw/driver/rados/rgw_rados.cc:5117-5184@v19.2.6`,
`src/rgw/driver/rados/rgw_rados.cc:5417-5490@v20.2.4#RGWRados::transition_obj`):
- the target placement exists (`valid_placement`), else the action fails;
- the mtime check, ECANCELED on mismatch;
- "null" cleared, the idtag and tail tag dropped, the owner from the ACL;
- the raw stream into the target class with a new tag, the same mtime and attrs, V6's
  `OLHEpoch` for a version, and the default retention reapplied when absent;
- old tails to the GC through the atomic head write.

A cloud-s3 target is never transitioned (Q5's ruling). Specs on fakerados: head and manifest
in the new class's pool, the index entry's class, a racing overwrite left untouched;
integration: radosgw reads the transitioned object and `bucket check` is clean (cluster).
Depends on C10, V6 and the writepath placement resolver. One PR. About 200 lines.

**C12. Harness and parity set** (T layer: `hack/rooket`, `hack/s3tests`, `hack/admin`,
`test/s3tests`, the Rook workflow patch). **Cluster.**
- Configure the cluster: `rgw_lc_debug_interval=10`; LC threads per gateway under test, with
  a radosgw restart between modes; two plain storage classes; `storage_classes` in the conf.
- Retire the four markers and the deselect lines.
- Record radosgw's outcomes for every freed test on both releases.
- Free the admin subtest; drop Rook's `bucket/lifecycle` from the patch.

Specs: the ledger checks pass, and Q6's ruling decides where the time-driven tests run.
Depends on L2-L5 for the lock lines, and C6, C8, C10 and C11 for the lifecycle ones.
PR split: one PR per feature's lines (object lock; lifecycle configuration; lifecycle
worker). About 180 lines.

**C13. Cross-gateway and mixed-worker gates** (integration: `test/integration/lc_test.go`,
`objectlock_test.go`). **Cluster.** §4's gate cases 1-8, each one way and then the other,
with both LC workers on, in a configuration separate from the parity options. It reproduces
each new defect of §6 on the floor it names, and updates the registry entries to reproduced
or not. Depends on L2-L5, C6, C7, C10 and C11. PR split: (a) lock cases; (b) configuration
and side-effect cases; (c) the worker and mixed-worker cases. About 200 lines.

Count: 18 tasks in this area (L1-L5, C1-C13), plus the 2 shared prerequisites T0a and T0b.

Parallel start: T0a, T0b, L1, C1 and C3 have no dependencies. Once versioning's V2-V6 have
landed, L3-L5 and C10-C11 can follow. Critical path: C3 → C4 → C5 → C9 → C10 → C11 → C13.

## 8. Open questions

- **Q1. Should both LC workers run on a shared zone?**
  - **Recommended:** yes. rgw-go honours `rgw_enable_lc_threads`, default true, and runs
    radosgw's shard-lock and claim protocol, adding only the claim re-check.
  - **Reason:** radosgw documents several LC threads per zone as supported. Its per-shard
    lock and per-bucket claim already exclude a second worker, and every action is guarded
    against racing writes. Making rgw-go's worker defer to radosgw's would leave lifecycle
    undone in an rgw-go-only zone.
  - **Cost:** with both on, the D1-D3 outcomes depend on which worker claims a bucket that day.
- **Q2. On a versioning-suspended bucket, what does current expiration do?**
  - **Recommended:** S3's: write the null delete marker radosgw's own DeleteObject writes
    there.
  - **Reason:** radosgw instead deletes the current version permanently (D2), which loses
    data and can resurface an older version.
  - **To confirm:** the S3 semantics come from AWS's documentation, not from the Ceph source,
    and need checking at plan time.
- **Q3. How should D4's overflow be handled?**
  - **Recommended:** refuse what cannot be represented, and fail closed at write time:
    - a PutObjectLockConfiguration period that would pass the real_time limit
      (2554-07-21T23:34:33Z) from now is InvalidRetentionPeriod;
    - a retain-until date past the limit gets the error its parser gives an unreadable date:
      MalformedXML in the body, InvalidArgument in the header;
    - a default retention computed at a write is saturated at the limit.
  - **Reason:** radosgw wraps silently to a date that can unprotect the object. A saturated
    date reads in radosgw as a lock to 2554, so the RADOS state stays compatible.
- **Q4. Does an expired retention still constrain PutObjectRetention (D8)?**
  - **Recommended:** no. Treat a retention whose date has passed as absent.
  - **Reason:** it only ever lets a client place new protection, and it matches what a lock
    that has lapsed means in S3.
  - **Alternative:** keep radosgw's stricter refusal, which is fail-closed and needs no
    exclusion.
- **Q5. What does rgw-go's worker do with a bucket whose rules name a cloud-s3 tier class?**
  - **Recommended:** leave such a bucket unclaimed, logging once. The entry is skipped
    without being set to processing, so radosgw's worker transitions it. The exclusion states
    that an rgw-go-only zone never processes such buckets.
  - **Reason:** cloud transition is excluded. If rgw-go claimed the bucket and skipped its
    cloud rules, radosgw's worker would skip it for the day too, and cloud transitions would
    stall on a mixed fleet.
- **Q6. Where do the time-driven lifecycle s3-tests run?**
  - **Recommended:** the worker tests (markers `lifecycle_expiration` and
    `lifecycle_transition`) run in the phase gate and on demand. The configuration and header
    tests join the per-pull-request set.
  - **Reason:** the worker tests sleep in debug days, about 1100 s in total (§5), against a
    per-pull-request check whose set takes about 130 s (spec §9).
  - **Cost:** this is a scope change to the spec's per-pull-request rule.
- **Q7. Do admin removals honour object lock?** These are `DELETE /admin/bucket?object` and
  the purging bucket and user removals, on a lock bucket.
  - **Recommended:** match radosgw, which checks no lock on any admin path. The exclusion says
    so.
  - **Reason:** the admin caps are the operator's tool, and Rook's bucket deletion relies on
    purge. Refusing would strand lock buckets.
  - **To confirm:** this sits against the "prevent corruption" rule, so the owner rules on it.
    Versioning owns the purge paths and needs the answer.
