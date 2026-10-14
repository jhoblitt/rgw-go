# Phase 2 research: versioning (OLH protocol, ListObjectVersions, MFA delete)

Area: `versioning`. Floors: Squid v19.2.6, Tentacle v20.2.4. Code at origin/main read through
`/home/jhoblitt/github/rgw-go/.claude/worktrees/phase-1-exec`.

Status: every section is written and every fact in it was read from the source at the tag
cited; the per-fact ledger is `research-versioning.notes.md` beside this file. Written
2026-10-13 under the owner's compatibility rule of that day (RADOS state, not bug-for-bug S3).

## 1. Scope

Spec §9, phase 2: "Versioning with the OLH protocol, ListObjectVersions, … MFA". The owner's
rule of 2026-10-13 governs every difference below: compatibility is RADOS state in both
directions; where radosgw's S3 behaviour is wrong rgw-go does the correct thing while writing
compatible state; rgw-go prevents corruption of shared data even where radosgw allows it; each
difference is an exclusion; a serious defect goes upstream once a running release reproduces it.

**S3 ops, subresources and headers in this area**

- `PUT /{bucket}?versioning` (PutBucketVersioning: `Status` Enabled|Suspended, `MfaDelete`
  Enabled|Disabled; action s3:PutBucketVersioning) and `GET /{bucket}?versioning`
  (GetBucketVersioning; s3:GetBucketVersioning). Both routes exist in the dispatch table and
  answer 501 today.
- `GET /{bucket}?versions` (ListObjectVersions; s3:ListBucketVersions): `key-marker`,
  `version-id-marker`, `prefix`, `delimiter`, `max-keys`, `encoding-type`, radosgw's
  `allow-unordered`; the v1 document, and radosgw's `list-type=2&versions` form.
- Object ops on a bucket whose versioning is enabled or suspended, and any op naming
  `?versionId=`: PutObject (a new version; `x-amz-version-id`), CopyObject (source
  `x-amz-copy-source` with `?versionId=`; a new destination version), CompleteMultipartUpload
  (a new version; `x-amz-version-id`), DeleteObject (a delete marker, or one version;
  `x-amz-delete-marker`, `x-amz-version-id`), DeleteObjects (per-key `VersionId`; `DeleteMarker`,
  `DeleteMarkerVersionId`), GET and HEAD (`x-amz-version-id`; the 404 of a delete marker with
  `x-amz-delete-marker`), GetObjectAttributes, GetObjectAcl/PutObjectAcl and
  Get/Put/DeleteObjectTagging on a version (the `s3:*ObjectVersion*` actions, which
  `internal/policy/action.go` already names), the null version (`versionId=null`) and
  suspended-bucket writes. UploadPartCopy's source `?versionId=` is P's op; its refusal lifts
  with the versioned source read.
- MFA delete: the `x-amz-mfa: <serial> <pin>` header, verified once per request against the
  user's `mfa_ids` and the user's otp object through cls_otp; `MfaDelete` on PutBucketVersioning
  (flag `BUCKET_MFA_ENABLED`) and in GetBucketVersioning's document; the 403 refusals of
  DeleteObject and DeleteObjects.

**Admin**

- `DELETE /admin/bucket?object` on a bucket whose versioning was ever enabled (501 today).
- The purging removals, `DELETE /admin/bucket` with `purge-objects` or `bypass-gc` and
  `DELETE /admin/user` with `purge-data`, over versions and delete markers (501 today).
- `GET /admin/bucket` stats already renders `versioning` as off/enabled/suspended
  (`internal/admin/bucket.go:131-138`); the go-ceph suite's `enable_versioning`
  subtest is deselected only because it needs PutBucketVersioning.
- The otp metadata section and the MFA device admin are writepath's ("admin otp"); this area
  consumes the `internal/cls/otp` types it shares with them (hot file, §7).

**Not this area:** object lock (lock-lifecycle), lifecycle's noncurrent expiration and
delete-marker expiry (lock-lifecycle; they build on the seams in §7), POST object (bucketconfig;
it writes through the versioned PUT seam), checksums (writepath), multisite's bilog, datalog and
`rgwx-*` parameters (excluded), Swift versioning (excluded), the `bi_*` index repair tooling
(exclusions; one `bi_get` is needed by the S3 path, §2.4).

## 2. radosgw's behaviour

Paths under `src/` of ceph/ceph; every value is from the source at the tag named. Where the tags
differ the text says so. `rgw_rados.cc` means `src/rgw/driver/rados/rgw_rados.cc`.

### 2.1 The OLH protocol: objects, xattrs and bucket index entries

**Head objects.** A version instance's head is a RADOS object named by
`rgw_obj_key::get_oid`: `_<ns>[:<instance>]_<name>`, so for the empty namespace
`_:<instance>_<name>`; a key without an instance, or with the instance `null`, is the plain
name, escaped with a second `_` when it starts with one
(`src/rgw/rgw_obj_types.h:226-254@v19.2.6#get_oid`, same text at v20.2.4). Its locator is the
name when the name starts with `_` and the namespace is empty, whatever the instance
(`src/rgw/rgw_obj_types.h:208-220@v19.2.6#get_loc`); rgw-go's `meta.ObjKey.OID` and `Locator`
already implement both (`internal/meta/objkey.go`). The instance is 32 random
alphanumerics without underscore (`src/rgw/driver/rados/rgw_rados.cc:8672-8681@v19.2.6#gen_rand_obj_instance_name`,
`src/rgw/driver/rados/rgw_rados.cc:9614-9623@v20.2.4`). The index key of any object is `(get_index_key_name, instance)`
(`src/rgw/rgw_obj_types.h:190-206@v19.2.6`).

**The OLH.** The object logical head is the plain key's head object. It can be colocated with
the null version's data: a suspended-bucket write and a pre-versioning object both live at the
plain oid. Its xattrs (`src/rgw/rgw_common.h:131-136@v19.2.6`, `src/rgw/rgw_common.h:150-155@v20.2.4`):

| xattr | value | written by |
|---|---|---|
| `user.rgw.olh.idtag` | the OLH tag, 32 lower alphanumerics; guards every later OLH op (`cmpxattr EQ`) | `olh_init_modification_impl` when the head has none (`src/rgw/driver/rados/rgw_rados.cc:7650-7670@v19.2.6`); `repair_olh` from the index's olh entry (`src/rgw/driver/rados/rgw_rados.cc:8096-8115@v19.2.6`) |
| `user.rgw.olh.ver` | the last applied log epoch as a decimal string; empty at creation | init writes it empty (`src/rgw/driver/rados/rgw_rados.cc:7672-7673@v19.2.6`); `apply_olh_log` sets `to_string(last_ver)` guarded by `cmpxattr(ver, GTE, last_ver)` in u64 mode (`src/rgw/driver/rados/rgw_rados.cc:8238-8244@v19.2.6`) |
| `user.rgw.olh.info` | `RGWOLHInfo` v1: `rgw_obj target`, `bool removed` (`src/rgw/driver/rados/rgw_rados.h:94-116@v19.2.6`, `src/rgw/driver/rados/rgw_rados.h:96-118@v20.2.4`) | `apply_olh_log` when a LINK_OLH entry wins (`src/rgw/driver/rados/rgw_rados.cc:8323-8331@v19.2.6`); the plain-head-becomes-OLH case sets it to itself (`src/rgw/driver/rados/rgw_rados.cc:7636-7648@v19.2.6`) |
| `user.rgw.olh.pending.<op_tag>` | `RGWOLHPendingInfo` v1: `real_time time` (`src/rgw/driver/rados/rgw_rados.h:118-138@v19.2.6`, `src/rgw/driver/rados/rgw_rados.h:120-140@v20.2.4`); `op_tag` is `%016llx` of the second then random lower alphanumerics to 32 bytes, so names sort by time | `olh_init_modification_impl` before each index link/unlink (`src/rgw/driver/rados/rgw_rados.cc:7676-7695@v19.2.6`); removed by `apply_olh_log` per log entry (`src/rgw/driver/rados/rgw_rados.cc:8309-8311@v19.2.6`), by `olh_cancel_modification` on a failed link (`src/rgw/driver/rados/rgw_rados.cc:7577-7584@v19.2.6`), or by expiry after `rgw_olh_pending_timeout_sec` (default 1 h, `src/common/options/rgw.yaml.in:2383-2393@v19.2.6`, `src/common/options/rgw.yaml.in:2483@v20.2.4`) in `check_pending_olh_entries` (`src/rgw/driver/rados/rgw_rados.cc:8708-8738@v19.2.6`) |
| `user.rgw.idtag` | a fresh 32-char object tag set with the OLH tag when the head is created as an OLH (`src/rgw/driver/rados/rgw_rados.cc:7651-7659@v19.2.6`); a colocated null version's own write tag otherwise | |

`is_olh(attrs)` is "has `user.rgw.olh.ver`" (`src/rgw/driver/rados/rgw_rados.cc:6086-6090@v19.2.6`,
`src/rgw/driver/rados/rgw_rados.cc:6840-6844@v20.2.4`); `has_olh_tag` is "has `user.rgw.olh.idtag`" (`src/rgw/driver/rados/rgw_rados.cc:6092-6096@v19.2.6`). A write over
the OLH head keeps the `user.rgw.olh.` attrs: `prepare_atomic_modification`'s reset is
`remove_rgw_head_obj`, the `obj_remove` class call that keeps that prefix (`src/rgw/driver/rados/rgw_rados.cc:6546-6553@v19.2.6`,
`src/rgw/driver/rados/rgw_rados.cc:7351-7358@v20.2.4`), and `_do_write_meta` re-sets the OLH tag on an OLH head
(`src/rgw/driver/rados/rgw_rados.cc:3191-3193@v19.2.6`, `src/rgw/driver/rados/rgw_rados.cc:3321@v20.2.4`). rgw-go's head write already keeps
the prefix (`internal/driver/headwrite.go:206-216`).

**Delete markers** have no head object: only index entries (an instance entry with
`tag = "delete-marker"` and `FLAG_DELETE_MARKER`, a list entry) and the OLH's `info.removed`.

**Bucket index entries** (one shard's omap; `src/cls/rgw/cls_rgw.cc@v19.2.6`, same shapes at
v20.2.4 with the line numbers in §2.2):

| entry | omap key | value |
|---|---|---|
| plain | `<index name>` | `rgw_bucket_dir_entry`; once the key is versioned it is a `FLAG_VER_MARKER` placeholder (`write_version_marker`, `src/cls/rgw/cls_rgw.cc:1568-1579@v19.2.6`), which listings skip as invalid (`src/cls/rgw/cls_rgw.cc:604-608@v19.2.6`) |
| instance | `0x80 "1000_" <name> "\0i" <instance>`, plus `"\0d"` for a null-instance delete marker (`encode_obj_versioned_data_key`, `src/cls/rgw/cls_rgw.cc:325-337@v19.2.6`); `encode_obj_index_key` picks this for any non-empty instance (`src/cls/rgw/cls_rgw.cc:339-346@v19.2.6`) | the version's `rgw_bucket_dir_entry` with `FLAG_VER`, `FLAG_CURRENT` when current, `FLAG_DELETE_MARKER`, `versioned_epoch` |
| list | `<name> "\0v" decreasing_str(versioned_epoch) "\0i" <instance>` (`get_list_index_key`, `src/cls/rgw/cls_rgw.cc:302-315@v19.2.6`; `decreasing_str`, `src/cls/rgw/cls_rgw.cc:272-290@v19.2.6`, pads so a newer epoch sorts first) | the same `rgw_bucket_dir_entry`; what `bucket_list` returns |
| OLH data | `0x80 "1001_" <name>` (`encode_olh_data_key`, `src/cls/rgw/cls_rgw.cc:348-353@v19.2.6`) | `rgw_bucket_olh_entry` v1: `key` (current instance), `delete_marker`, `epoch`, `pending_log` (`map<u64, vector<rgw_bucket_olh_log_entry>>`), `tag`, `exists`, `pending_removal` (`src/cls/rgw/cls_rgw_types.h:556-593@v19.2.6`); log entries v1: `epoch`, `op` (`OLHLogOp` UNKNOWN 0, LINK_OLH 1, UNLINK_OLH 2, REMOVE_INSTANCE 3), `op_tag`, `key`, `delete_marker` (`src/cls/rgw/cls_rgw_types.h:513-554@v19.2.6`) |

Flags: `FLAG_VER` 0x1, `FLAG_CURRENT` 0x2, `FLAG_DELETE_MARKER` 0x4, `FLAG_VER_MARKER` 0x8,
`FLAG_COMMON_PREFIX` 0x8000; `is_current` = not VER, or VER and CURRENT; `is_visible` =
current and not a delete marker; `is_valid` = not VER_MARKER
(`src/cls/rgw/cls_rgw_types.h:369-463@v19.2.6`, same lines at v20.2.4). The entry encoding is
v8 compat 3 and unchanged between the tags; rgw-go's `rgw.DirEntry` carries every field
(`internal/cls/rgw/types_index.go`). `cls_rgw_after_versions(name)` = name +
`"\1"`, the key past every list entry of a name (`src/cls/rgw/cls_rgw.cc:319-323@v19.2.6`). Special-entry prefixes: 0x80, then
`"0_"` log, `"1000_"` instance, `"1001_"` olh (`src/cls/rgw/cls_rgw.cc:51-66@v19.2.6`); v20.2.4 adds `"2001_"`, the
reshard log the class fills while a shard is `IN_LOGRECORD` (`src/cls/rgw/cls_rgw.cc:51-70@v20.2.4`; `write_entry` and
`remove_entry` mirror every write there, `src/cls/rgw/cls_rgw.cc:752-786@v20.2.4`), which rgw-go never writes itself.

**Epochs.** The class assigns the OLH epoch: a request with `olh_epoch` 0 gets the next one,
starting at 2 because 1 is "reserved to converted plain entries"
(`BIOLHEntry::start_modify`, `src/cls/rgw/cls_rgw.cc:1503-1517@v19.2.6`, `src/cls/rgw/cls_rgw.cc:1630-1644@v20.2.4`);
a nonzero request epoch below the entry's is refused as old. Every S3 request sends 0
(`get_system_versioning_params` returns at once for a non-system request,
`src/rgw/rgw_op.h:2113-2139@v19.2.6`, and the callers initialize `olh_epoch` to 0), so the
exclusions note "OLH epochs differ by OSD release" concerns only main's time-based epochs and
reply; neither floor returns an epoch.

**A pre-versioning object** becomes the null version on the first versioned write of its key:
`convert_plain_entry_to_versioned` rewrites the plain entry as an instance (and list) entry with
`versioned_epoch` 1 and `FLAG_VER`, CURRENT cleared when the new version is promoted, then
writes the version marker at the plain name (`src/cls/rgw/cls_rgw.cc:1587-1634@v19.2.6`, `src/cls/rgw/cls_rgw.cc:1715-1762@v20.2.4`). Its
head stays at the plain oid, which the OLH then shares.

### 2.2 The cls_rgw methods

Registered in `src/cls/rgw/cls_rgw.cc:4699-4703@v19.2.6` and `src/cls/rgw/cls_rgw.cc:5116-5120@v20.2.4`; names in
`src/cls/rgw/cls_rgw_const.h:24-28@v19.2.6`. Request structs in `src/cls/rgw/cls_rgw_ops.h`,
unchanged between the tags (the diff touches none of them; v20.2.4 lines are two lower).

| method | flags | request (encode version) | reply | class behaviour at v19.2.6 | errors |
|---|---|---|---|---|---|
| `bucket_link_olh` | RD\|WR | `rgw_cls_link_olh_op` v5 compat 1: key, olh_tag, delete_marker, op_tag, meta, olh_epoch, log_op, bilog_flags, `u64 time_t(unmod_since)`, unmod_since, high_precision_time, zones_trace (`src/cls/rgw/cls_rgw_ops.h:167-232@v19.2.6`, `src/cls/rgw/cls_rgw_ops.h:165-230@v20.2.4`) | none | `rgw_bucket_link_olh` `src/cls/rgw/cls_rgw.cc:1652-1865@v19.2.6`, `src/cls/rgw/cls_rgw.cc:1781-2000@v20.2.4`: reads the instance entry (ENOENT allowed only for a delete marker), honours `unmod_since` by returning 0 unchanged, handles the null-instance data/marker pair (`src/cls/rgw/cls_rgw.cc:1712-1734@v19.2.6`), reads the olh entry, assigns the epoch, computes promote (newer epoch, or equal epoch and a greater-or-equal instance, `src/cls/rgw/cls_rgw.cc:1764-1766@v19.2.6`), demotes the previous current, converts a plain entry when no olh exists, appends LINK_OLH (+REMOVE_INSTANCE) to the pending log, writes the olh entry then the instance and list entries, logs to the bilog only with `log_op` | -EINVAL decode; -ENOENT instance missing (non-marker); **-ECANCELED** olh tag mismatch unless `pending_removal` (`src/cls/rgw/cls_rgw.cc:1768-1777@v19.2.6`); v20.2.4 adds the class-side reshard guard first (`src/cls/rgw/cls_rgw.cc:1797-1808@v20.2.4`) |
| `bucket_unlink_instance` | RD\|WR | `rgw_cls_unlink_instance_op` v3: key, op_tag, olh_epoch, log_op, bilog_flags, olh_tag, zones_trace (`src/cls/rgw/cls_rgw_ops.h:234-276@v19.2.6`, `src/cls/rgw/cls_rgw_ops.h:232-274@v20.2.4`) | none | `src/cls/rgw/cls_rgw.cc:1867-2018@v19.2.6`, `src/cls/rgw/cls_rgw.cc:2002-2159@v20.2.4`: converts a plain entry when no olh; old epoch → unlink the list entry and log REMOVE_INSTANCE (marker: nothing); when the instance is the current head, `find_next_key` promotes the next version (LINK_OLH) or logs UNLINK_OLH with `exists=false, pending_removal=true` (`src/cls/rgw/cls_rgw.cc:1936-1968@v19.2.6`); a delete marker's instance entry is removed by the class, a data instance's removal is logged for the gateway (`src/cls/rgw/cls_rgw.cc:1970-1979@v19.2.6`); list entry removed; olh written | -ENOENT instance entry missing (`src/cls/rgw/cls_rgw.cc:1888-1894@v19.2.6`); no olh tag check |
| `bucket_read_olh_log` | RD | `rgw_cls_read_olh_log_op` v1: olh (instance must be empty), ver_marker, olh_tag (`src/cls/rgw/cls_rgw_ops.h:278-303@v19.2.6`) | `rgw_cls_read_olh_log_ret` v1: `log` (entries with epoch > marker, at most 1000), `is_truncated` (`src/cls/rgw/cls_rgw_ops.h:306-328@v19.2.6`) | `src/cls/rgw/cls_rgw.cc:2020-2072@v19.2.6`, `src/cls/rgw/cls_rgw.cc:2161-2213@v20.2.4` | -EINVAL non-empty instance; **-ECANCELED** tag mismatch, including a missing olh entry whose tag is "" (`src/cls/rgw/cls_rgw.cc:2047-2050@v19.2.6`) |
| `bucket_trim_olh_log` | RD\|WR | `rgw_cls_trim_olh_log_op` v1: olh, ver, olh_tag (`src/cls/rgw/cls_rgw_ops.h:330-355@v19.2.6`) | none | erases log epochs <= ver, writes the entry (`src/cls/rgw/cls_rgw.cc:2074-2124@v19.2.6`, `src/cls/rgw/cls_rgw.cc:2215-2277@v20.2.4`) | -EINVAL; -ECANCELED tag mismatch; v20.2.4 guards reshard (`src/cls/rgw/cls_rgw.cc:2257-2266@v20.2.4`) |
| `bucket_clear_olh` | RD\|WR | `rgw_cls_bucket_clear_olh_op` v1: key, olh_tag (`src/cls/rgw/cls_rgw_ops.h:357-380@v19.2.6`) | none | removes the olh data entry, then the plain entry only if it is a VER_MARKER (`src/cls/rgw/cls_rgw.cc:2126-2190@v19.2.6`, `src/cls/rgw/cls_rgw.cc:2279-2355@v20.2.4`) | -EINVAL; -ECANCELED tag mismatch; v20.2.4 guards reshard (`src/cls/rgw/cls_rgw.cc:2297-2306@v20.2.4`) |
| `bi_get` | RD | `rgw_cls_bi_get_op`: key, type (Instance 2, OLH 3) | `rgw_cls_bi_get_ret`: one `rgw_cls_bi_entry` | used by `bi_get_instance` (`src/rgw/driver/rados/rgw_rados.cc:9247-9267@v19.2.6`, `src/rgw/driver/rados/rgw_rados.cc:10192@v20.2.4`) on the S3 versioned delete (§2.4) and `bi_get_olh` by `repair_olh` | -ENOENT |

Client builders: `src/cls/rgw/cls_rgw_client.cc:513-534@v19.2.6` (link; `olh_tag` is the
xattr's bytes as a string), `src/cls/rgw/cls_rgw_client.cc:550-566@v19.2.6` (unlink), `src/cls/rgw/cls_rgw_client.cc:568-577@v19.2.6` (log read as a read-op exec with a
decoding completion), `src/cls/rgw/cls_rgw_client.cc:597-606@v19.2.6` (trim), `src/cls/rgw/cls_rgw_client.cc:616-624@v19.2.6` (clear); at v20.2.4 `src/cls/rgw/cls_rgw_client.cc:377-397@v20.2.4`, `src/cls/rgw/cls_rgw_client.cc:414-429@v20.2.4`,
`src/cls/rgw/cls_rgw_client.cc:432-440@v20.2.4`, `src/cls/rgw/cls_rgw_client.cc:461-469@v20.2.4`, `src/cls/rgw/cls_rgw_client.cc:480-487@v20.2.4`. Every write among them is preceded on the gateway by
`assert_exists` and `cls_rgw_guard_bucket_resharding(-ERR_BUSY_RESHARDING)` inside
`guard_reshard` (link `src/rgw/driver/rados/rgw_rados.cc:7957-7969@v19.2.6`, unlink `src/rgw/driver/rados/rgw_rados.cc:8011-8020@v19.2.6`, trim `src/rgw/driver/rados/rgw_rados.cc:8153-8160@v19.2.6`,
clear `src/rgw/driver/rados/rgw_rados.cc:8184-8192@v19.2.6`; v20.2.4 `src/rgw/driver/rados/rgw_rados.cc:8876-9141@v20.2.4` same shape), exactly as phase 1's prepare and complete;
the log read is not guarded (`src/rgw/driver/rados/rgw_rados.cc:8041-8060@v19.2.6`). `bucket_list` and `bucket_complete_op` are phase 1's;
their versioned behaviour: `bucket_list` skips VER_MARKER entries and, without `list_versions`,
skips a non-visible entry or one named like the start key to `cls_rgw_after_versions(name)`
(`src/cls/rgw/cls_rgw.cc:604-623@v19.2.6`, `src/cls/rgw/cls_rgw.cc:654-673@v20.2.4`), and answers `RGWBIAdvanceAndRetryError` (-EFBIG) when
eight omap reads listed nothing (`src/cls/rgw/cls_rgw.cc:688-691@v19.2.6`); `complete_op` resets the entry's flags to `FLAG_VER`
and writes the one key `read_key_entry` resolved, which for an instance key is the instance
entry only (`src/cls/rgw/cls_rgw.cc:1061-1064@v19.2.6`, `src/cls/rgw/cls_rgw.cc:1168-1197@v19.2.6`).

### 2.3 Versioned PUT (and copy, multipart completion)

**Instance assignment (op layer).** `RGWPutObj::execute`: when `versioning_enabled()` (versioned
and not suspended) the object gets the system `version_id` or a random instance
(`src/rgw/rgw_op.cc:4278-4285@v19.2.6`, `src/rgw/rgw_op.cc:4495@v20.2.4`); an append write to a versioned bucket
is `-ERR_INVALID_BUCKET_STATE` (`src/rgw/rgw_op.cc:4269-4272@v19.2.6`). `RGWCopyObj::execute` does the same for the
destination (`src/rgw/rgw_op.cc:5573-5577@v19.2.6`, `src/rgw/rgw_op.cc:6142@v20.2.4`); the source is the object the request named,
with its `?versionId=`, resolved by the read. `RGWCompleteMultipart::execute` likewise
(`src/rgw/rgw_op.cc:6459-6466@v19.2.6`, `src/rgw/rgw_op.cc:7288@v20.2.4`). On a never-versioned or suspended bucket the key keeps
whatever instance the URL carried (`versionId` is read into the key for every object request,
`src/rgw/rgw_rest_s3.cc:4946-4955@v19.2.6`, `src/rgw/rgw_rest_s3.cc:5506-5515@v20.2.4`): that is the registry's
"PutObject naming versionId=null on an unversioned bucket" (§6).

**The write (driver).** `_do_write_meta` (`src/rgw/driver/rados/rgw_rados.cc:3124-3417@v19.2.6`, `src/rgw/driver/rados/rgw_rados.cc:3234@v20.2.4`),
with the head at the instance oid:

1. `versioned_target = (olh_epoch set && > 0) || instance non-empty`; `versioned_op =
   versioning_enabled || is_olh || versioned_target`; a versioned op sets
   `RGW_BILOG_FLAG_VERSIONED_OP` on the index op (`src/rgw/driver/rados/rgw_rados.cc:3280-3287@v19.2.6`, `src/rgw/driver/rados/rgw_rados.cc:3433-3439@v20.2.4`).
2. index `prepare` ADD on the instance key, behind the reshard guard (`src/rgw/driver/rados/rgw_rados.cc:3289-3295@v19.2.6`); the class
   files a pending entry under `0x80 1000_ name \0i instance` (§2.1).
3. the head write op: on an OLH head, `setxattr(olh.idtag)` again (`src/rgw/driver/rados/rgw_rados.cc:3191-3193@v19.2.6`); `mtime2`,
   `write_full`, attrs, manifest, `pg_ver`, `source_zone`, `storage_class`; `prepare_atomic_modification`
   adds the id-tag guard and, for an existing head, `create(false)` + `remove_rgw_head_obj`
   (`src/rgw/driver/rados/rgw_rados.cc:6484-6576@v19.2.6`).
4. `complete_atomic_modification` (the overwritten head's tails to the GC) and the index
   `complete` ADD, which radosgw sends without waiting (`src/rgw/driver/rados/rgw_rados.cc:3314-3324@v19.2.6`); for an instance key the
   class writes the instance entry only (§2.2).
5. `versioned_op && olh_epoch set` → `set_olh(obj, delete_marker=false, meta=NULL, epoch=0,
   unmod_since=0, high_precision=false, ..., add_log)` (`src/rgw/driver/rados/rgw_rados.cc:3337-3343@v19.2.6`,
   `src/rgw/driver/rados/rgw_rados.cc:3492@v20.2.4`): §2.9 gives
   `set_olh`'s sequence; the net effect is the OLH head gains a pending attr, the shard links the
   instance as current (new epoch, list entry, previous current demoted, plain entry converted on
   the first version), and `update_olh` replays the log onto the head (OLH_INFO → this instance,
   OLH_VER → the epoch), removes the pending attr and trims the log.
6. a failed head write cancels the index op as in phase 1 (`src/rgw/driver/rados/rgw_rados.cc:3369-3416@v19.2.6`); a lost race answers
   success without conditions.

`versioned_op` is true for: every write on a versioning-enabled bucket; a write over an OLH
head (a suspended bucket's key that was once versioned, which becomes the null version:
instance empty, so the link's key is the null instance and the head is the OLH head itself,
whose `olh.*` attrs the reset keeps); and any explicit instance. A suspended bucket's key that
never had an OLH is a plain write with no OLH.

**Ordering.** `set_olh`'s `link_olh` reads the instance entry the complete wrote
(`BIVerObjEntry::init`, §2.2). radosgw's complete is asynchronous but librados orders one
client's ops to one object, so the link lands after it. rgw-go's completion manager runs each
completion on its own goroutine with no ordering (`docs/cgo-limitations.md:320-345`),
so a link that reaches the shard first would write the list entry from an empty instance entry
and the complete would then strip `FLAG_CURRENT` from the instance entry: a visibly corrupt
version (no size or ETag in listings). **Decision for the plan:** a versioned write awaits its
ADD completion before `set_olh` (one round trip radosgw does not block on, but which the OSD
serializes anyway); the fire-and-forget complete stays for unversioned writes.

**Tentacle differs** in what the conditions see. v20.2.4 evaluates If-Match/If-None-Match
against the current version, loaded through the OLH when the key has an instance
(`get_current_version_state`, `src/rgw/driver/rados/rgw_rados.cc:7238-7258@v20.2.4`; `check_preconditions`
`src/rgw/driver/rados/rgw_rados.cc:7260-7329@v20.2.4`; the call `src/rgw/driver/rados/rgw_rados.cc:3282-3297@v20.2.4`), and sets the id-tag guard only for an instance-less write
(`set_attr_id_tag`, `src/rgw/driver/rados/rgw_rados.cc:3298-3300@v20.2.4`). v19.2.6 evaluates both against the object being written
(`prepare_atomic_modification`, `src/rgw/driver/rados/rgw_rados.cc:6493-6543@v19.2.6`), which for a versioning-enabled bucket is
the new random instance, never existing: If-Match (any value) always answers 412 and
If-None-Match `*` never fails (§6, defect 3). rgw-go follows v20.2.4's evaluation on both
releases, as phase 1 already does for the unversioned case (owner decision 1, PP-3), and its
checks are not atomic with the link on either release (§2.9).

**Copy** goes through `copy_obj` with `olh_epoch` and the destination version
(`src/rgw/rgw_op.cc:5655-5676@v19.2.6`; `src/rgw/driver/rados/rgw_sal_rados.cc:2686-2716@v19.2.6` sets
`obj_op.meta.olh_epoch`), so the driver sequence is the write above with the shared-tail or
streamed data of phase 1's copy; the source read follows the OLH or names an instance (§2.5).
**Multipart completion** sets `obj_op.meta.olh_epoch` on the assembled head's write
(`src/rgw/driver/rados/rgw_sal_rados.cc:3632@v19.2.6`) and otherwise runs P's completion; rgw-go's
`mp_complete.go` already carries an `attrMPCompletionInstance` seam for the instance
(`internal/driver/mp_complete.go:351`, `internal/driver/mp_complete.go:397`).

**Response headers.** PutObject and CompleteMultipartUpload send `x-amz-version-id` when the
version id is non-empty (`src/rgw/rgw_rest_s3.cc:2748@v19.2.6`, `src/rgw/rgw_rest_s3.cc:2754@v19.2.6`, `src/rgw/rgw_rest_s3.cc:4081@v19.2.6`; `src/rgw/rgw_rest_s3.cc:2891@v20.2.4`,
`src/rgw/rgw_rest_s3.cc:4604@v20.2.4`): the instance on an enabled bucket, nothing on a suspended one, where S3 sends
`null` (§6, defect 8). CopyObject's responses carry no version header at all
(`src/rgw/rgw_rest_s3.cc:3566-3603@v19.2.6`, `src/rgw/rgw_rest_s3.cc:3873@v20.2.4`), where S3 sends `x-amz-version-id` and
`x-amz-copy-source-version-id` (§6, defect 5).

### 2.4 Versioned DELETE and delete markers

**DeleteObject, op layer** (`src/rgw/rgw_op.cc@v19.2.6`; v20.2.4 in brackets):

- `verify_permission` (`src/rgw/rgw_op.cc:5138-5166@v19.2.6` [`src/rgw/rgw_op.cc:5528-5556@v20.2.4`]): the action is `s3:DeleteObjectVersion`
  when the key names an instance, else `s3:DeleteObject` (`src/rgw/rgw_op.cc:5144-5151@v19.2.6`); then the
  bypass-governance permission when the bucket has object lock (`src/rgw/rgw_op.cc:5153-5156@v19.2.6`, lock-lifecycle's);
  then MFA: `mfa_enabled() && instance non-empty && !mfa_verified` → `-ERR_MFA_REQUIRED`, 403
  AccessDenied (`src/rgw/rgw_op.cc:5158-5163@v19.2.6`). rgw-go's `DeleteObject.VerifyPermission` already has this shape
  with the MFA refusal unconditional (`internal/op/deleteobject.go:106-116`).
- `execute` (`src/rgw/rgw_op.cc:5173-5327@v19.2.6` [`src/rgw/rgw_op.cc:5563@v20.2.4`]): `check_obj_lock = have_instance && obj_lock_enabled`
  (`src/rgw/rgw_op.cc:5186@v19.2.6`), `null_verid = instance == "null"` (`src/rgw/rgw_op.cc:5187@v19.2.6`); the state is loaded following the OLH
  (`src/rgw/rgw_op.cc:5189@v19.2.6`); a missing object drops the lock check (`src/rgw/rgw_op.cc:5195-5203@v19.2.6`); `verify_object_lock` on the
  version's attrs is lock-lifecycle's seam (`src/rgw/rgw_op.cc:5211-5221@v19.2.6`); the delete op gets
  `versioning_status`, `unmod_since`, `olh_epoch` (0), `marker_version_id` (system only),
  `null_verid` (`src/rgw/rgw_op.cc:5278-5288@v19.2.6`); `-ECANCELED` → 0 (`src/rgw/rgw_op.cc:5302-5304@v19.2.6`); the result's `delete_marker` and
  `version_id` feed the response.
- Response (`src/rgw/rgw_rest_s3.cc:3455-3470@v19.2.6` [`src/rgw/rgw_rest_s3.cc:3735@v20.2.4`]): `-ENOENT` → 204; 204;
  `x-amz-version-id` when non-empty; `x-amz-delete-marker: true` when `delete_marker`.

**The driver's versioned branch** (`Object::Delete::delete_obj`, `src/rgw/driver/rados/rgw_rados.cc:5757-5843@v19.2.6`
[`src/rgw/driver/rados/rgw_rados.cc:6445-6577@v20.2.4`]), taken when `versioning_status & BUCKET_VERSIONED` (enabled or
suspended) or a system marker id is given (`src/rgw/driver/rados/rgw_rados.cc:5774@v19.2.6`); the `null` instance is cleared first
(`src/rgw/driver/rados/rgw_rados.cc:5768-5770@v19.2.6`):

- *No instance: a delete marker.* The marker key's instance is random unless the bucket is
  suspended, where it stays empty, the null marker (`src/rgw/driver/rados/rgw_rados.cc:5777-5787@v19.2.6`); `result.version_id` is the
  instance or `"null"`, `result.delete_marker` true (`src/rgw/driver/rados/rgw_rados.cc:5789-5793@v19.2.6`); the marker's index meta
  carries the requester as owner and the request time (`src/rgw/driver/rados/rgw_rados.cc:5795-5804@v19.2.6`); `set_olh(marker,
  delete_marker=true, &meta, olh_epoch, unmod_since, high_precision_time, ...)` (`src/rgw/driver/rados/rgw_rados.cc:5806-5808@v19.2.6`).
  The class creates the marker's instance entry itself (`init_as_delete_marker`, §2.2) and
  promotes it; `update_olh` sets `OLH_INFO.removed = true`. A marker on top of a marker is
  allowed at both floors (registry: fixed before v19.2.6). v20.2.4 first checks the
  conditional-delete headers against the current version (`src/rgw/driver/rados/rgw_rados.cc:6494-6509@v20.2.4`).
- *An instance: unlink it.* `bi_get_instance` reads the instance entry (`src/rgw/driver/rados/rgw_rados.cc:5815@v19.2.6`; `bi_get`,
  `src/rgw/driver/rados/rgw_rados.cc:9247-9267@v19.2.6`, `-ENOENT` → the request ends in 204 with no headers); `result.delete_marker =
  dirent.is_delete_marker()`; `unlink_obj_instance(obj, olh_epoch, bilog_flags, null_verid,
  ...)` (`src/rgw/driver/rados/rgw_rados.cc:5820-5823@v19.2.6`); `result.version_id` = the instance (`src/rgw/driver/rados/rgw_rados.cc:5827@v19.2.6`). v20.2.4 checks If-Match,
  size and last-modified against the dirent's meta before unlinking (`src/rgw/driver/rados/rgw_rados.cc:6520-6554@v20.2.4`).
  `unlink_obj_instance` (`src/rgw/driver/rados/rgw_rados.cc:8568-8670@v19.2.6` [`src/rgw/driver/rados/rgw_rados.cc:9510@v20.2.4`]) runs §2.9's sequence with
  `bucket_index_unlink_instance`; its `update_olh` → `apply_olh_log` then deletes the unlinked
  data instance's head through `delete_obj(instance, versioning_status 0, …,
  RGW_BILOG_FLAG_VERSIONED_OP, skip_olh_obj_update=true)` (`src/rgw/driver/rados/rgw_rados.cc:8334-8345@v19.2.6`), that is phase 1's
  unversioned delete on the instance head: prepare DEL on the instance key, `obj_remove`
  keeping the `olh.` prefix, complete_del, tails to the GC. A delete marker has no head and the
  class removed its instance entry already, so nothing is deleted for it.
- Both then add a datalog entry only when the zone logs data (`src/rgw/driver/rados/rgw_rados.cc:5837-5840@v19.2.6`), which a single
  zone never does.

**Both floors** register the `bi_get` method the S3 delete uses (§2.2); `docs/exclusions.md`'s
"the `bi_*` index-entry methods … are out on both sides unless an index-repair path in scope
turns out to need them" needs amending: the versioned delete needs `bi_get` of one instance
entry (§8, Q9).

**DeleteObjects** (`src/rgw/rgw_op.cc:7007-7106@v19.2.6` [`src/rgw/rgw_op.cc:7921@v20.2.4`]): after the parse and the count
check, the MFA check refuses the whole request with `-ERR_MFA_REQUIRED` when the bucket has MFA
delete, any key names a version and the request did not verify (`src/rgw/rgw_op.cc:7055-7068@v19.2.6`); v20.2.4
inverts the per-key test (`src/rgw/rgw_op.cc:7969-7983@v20.2.4`; registry, §6 defect 1). On a versioned bucket
`handle_versioned_objects` groups keys by name and deletes all but the last of each group with
`FLAG_SKIP_UPDATE_OLH`, then the last with the OLH update (`src/rgw/rgw_op.cc:6912-6965@v19.2.6`;
`handle_objects` chooses it by `bucket->versioned()`, `src/rgw/rgw_op.cc:6996-7005@v19.2.6`). Each key runs
`handle_individual_object` (`src/rgw/rgw_op.cc:6818-6910@v19.2.6` [`src/rgw/rgw_op.cc:7743@v20.2.4`]): the per-key action and permission, the
state read and lock check as DeleteObject, `versioning_status`, an empty `marker_version_id`,
`-ENOENT` → 0, and the partial response with `result.delete_marker` and `result.version_id`.
The `Deleted` element carries `Key`, `VersionId` when the key named one, and `DeleteMarker` +
`DeleteMarkerVersionId` when a marker was made (`src/rgw/rgw_rest_s3.cc:4273-4301@v19.2.6`
[`src/rgw/rgw_rest_s3.cc:4821@v20.2.4`]); rgw-go's `deleteobjects.go` renders these fields already. The skip leaves the
intermediate pending attrs on the OLH head for the last delete's `update_olh` to apply; rgw-go
may delete each key with its own update instead, which yields the same final state (one
reconciliation per key rather than one per name); the plan picks radosgw's grouping to keep the
op count comparable.

**The admin object removal** (`RGWBucket::remove_object`, `src/rgw/driver/rados/rgw_bucket.cc:274-289@v19.2.6`,
`src/rgw/driver/rados/rgw_bucket.cc:275-290@v20.2.4`) deletes the plain key with the bucket's versioning status, so on a versioned
bucket it writes a delete marker: rgw-go's `RemoveObjectAdmin` refusal lifts onto the same
driver delete. The purging removals list with `list_versions = true` and delete each entry by
its key with its instance (`src/rgw/driver/rados/rgw_sal_rados.cc:362-364@v19.2.6`, `src/rgw/driver/rados/rgw_sal_rados.cc:500@v19.2.6`).

### 2.5 Versioned GET and HEAD

**State resolution** is `get_obj_state_impl` (`src/rgw/driver/rados/rgw_rados.cc:6120-6297@v19.2.6`,
`src/rgw/driver/rados/rgw_rados.cc:6874@v20.2.4`, identical text): `need_follow_olh = follow_olh && instance empty` (`src/rgw/driver/rados/rgw_rados.cc:6129@v19.2.6`);
the head's `olh.idtag` is kept as `olh_tag` whether or not it is an OLH (`src/rgw/driver/rados/rgw_rados.cc:6278-6280@v19.2.6`); a head
with `olh.ver` is an OLH: with no instance named the state becomes the target's
(`get_olh_target_state`, `src/rgw/driver/rados/rgw_rados.cc:6098-6118@v19.2.6`, `follow_olh` §2.9); with `versionId=null` named and no
manifest on the head, the null version does not exist: `exists=false`, `-ENOENT`
(`src/rgw/driver/rados/rgw_rados.cc:6289-6293@v19.2.6`). A named instance reads `_:<instance>_<name>` directly; a delete marker's
instance has no head, so it is `-ENOENT`.

`Object::Read::prepare` (`src/rgw/driver/rados/rgw_rados.cc:6847@v19.2.6#RGWRados::Object::Read::prepare`, `src/rgw/driver/rados/rgw_rados.cc:7686@v20.2.4`)
loads with `follow_olh=true`, answers `-ENOENT` for a missing object (`src/rgw/driver/rados/rgw_rados.cc:6866-6872@v19.2.6`), and leaves
the resolved key in `state.obj`; `RadosReadOp::prepare` copies the instance back to the request's
object (`src/rgw/driver/rados/rgw_sal_rados.cc:2863@v19.2.6`, `src/rgw/driver/rados/rgw_sal_rados.cc:3678@v20.2.4`), and
`RGWGetObj::execute` takes `version_id = s->object->get_instance()` from it
(`src/rgw/rgw_op.cc:2259-2260@v19.2.6`). So a plain GET of a versioned key answers the current
version with its `x-amz-version-id` (`src/rgw/rgw_rest_s3.cc:461@v19.2.6`), and a GET of the
null version answers `x-amz-version-id: null` because the request named it; a plain GET whose
current version is the null version sends no header (§6, defect 8). HEAD is the same op.

**Permissions first.** `read_obj_policy` reads the object's ACL before `execute`
(`src/rgw/rgw_op.cc:423-447@v19.2.6`, `src/rgw/rgw_op.cc:453-477@v20.2.4`): a missing object, which includes a
current delete marker (the OLH follow returns `-ENOENT`) and a delete marker's instance, answers
`-ENOENT` to an admin or a requester allowed `s3:ListBucket` on the bucket, else `-EACCES`.
v20.2.4's `read_permissions` then adds `x-amz-delete-marker` to that 404 of a GET or HEAD,
`true` when the key's current version is a delete marker: it reloads the state following the
OLH and reads `is_delete_marker()` (`src/rgw/rgw_rest.cc:1936-1944@v20.2.4`), which `follow_olh`
sets from `OLH_INFO.removed` (`src/rgw/driver/rados/rgw_rados.cc:9760-9764@v20.2.4`, copied at
`src/rgw/driver/rados/rgw_sal_rados.cc:2942@v20.2.4`). v19.2.6 sends no such header. A GET or HEAD that names a
delete marker's own instance is a plain 404 NoSuchKey at both tags, with the header `false` at
v20.2.4 (the instance is named, so nothing is followed), where S3 answers 405 MethodNotAllowed
with `x-amz-delete-marker: true` (§6, defect 6).

**Conditions** (`If-Match`, ranges) apply to the resolved state as in phase 1.
GetObjectAttributes, GetObjectAcl and GetObjectTagging resolve the same way (their actions are
the `Version` ones when an instance is named, `src/rgw/rgw_op.cc:1039@v19.2.6`, `src/rgw/rgw_op.cc:1078@v19.2.6`, `src/rgw/rgw_op.cc:1120@v19.2.6`,
`src/rgw/rgw_op.cc:5700@v19.2.6`); the `?versionId=` of a UploadPartCopy and CopyObject source resolves through
the same read.

**Writes of attrs on a version** (PutObjectAcl, Put/DeleteObjectTagging, lock-lifecycle's
retention and legal hold): `modify_obj_attrs` resolves the target through a read that follows
the OLH and writes the instance (`src/rgw/driver/rados/rgw_sal_rados.cc:2401-2419@v19.2.6`, `src/rgw/driver/rados/rgw_sal_rados.cc:2994@v20.2.4`);
`RGWRados::set_attrs` (`src/rgw/driver/rados/rgw_rados.cc:6593-6757@v19.2.6`, `src/rgw/driver/rados/rgw_rados.cc:7393@v20.2.4`) clears a `null`
instance (`src/rgw/driver/rados/rgw_rados.cc:6601-6603@v19.2.6`), answers `-ENOENT` for a null version without a manifest (`src/rgw/driver/rados/rgw_rados.cc:6620-6622@v19.2.6`),
guards on the version head's write tag, draws a fresh write tag, prepares ADD on the instance
key, writes the attrs and the new `idtag` with the kept mtime (plus 1 ns when the zone logs,
`src/rgw/driver/rados/rgw_sal_rados.cc:2384@v19.2.6`), and completes ADD on the instance key with the state's size
and the attrs' etag, content type and storage class (`src/rgw/driver/rados/rgw_rados.cc:6665-6725@v19.2.6`). The class's complete resets
the instance entry's flags to `FLAG_VER`, which is radosgw's state too (the list entry keeps
`CURRENT`); rgw-go's `SetObjectAttrs` takes the resolved state and needs only the instance key
and the removal of its refusals.

### 2.6 Enabling and suspending; null versions

**PutBucketVersioning** (`RGWSetBucketVersioning`, `src/rgw/rgw_op.cc:2759-2860@v19.2.6`,
`src/rgw/rgw_op.cc:2991-3092@v20.2.4` by the grep at `src/rgw/rgw_op.cc:2991@v20.2.4` and `src/rgw/rgw_op.cc:3009@v20.2.4`):

1. `verify_permission`: bucket tags into the environment when a policy asks, then
   `s3:PutBucketVersioning` (`src/rgw/rgw_op.cc:2759-2770@v19.2.6`).
2. `get_params` (`src/rgw/rgw_rest_s3.cc:2204-2261@v19.2.6`, `src/rgw/rgw_rest_s3.cc:2307@v20.2.4`): the body up to
   `rgw_max_put_param_size`; an unparsable document or no `VersioningConfiguration` →
   `-EINVAL` (400 InvalidArgument); `Status` `Enabled` → enabled, `Suspended` → suspended,
   anything else → invalid → `-EINVAL`; `MfaDelete` `Enabled`/`Disabled` sets `mfa_set_status`
   and `mfa_status`, any other value `-EINVAL` (`ver_config_status::decode_xml`,
   `src/rgw/rgw_rest_s3.cc:2171-2202@v19.2.6`).
3. `execute` (`src/rgw/rgw_op.cc:2777-2860@v19.2.6`): NoSuchBucket; a bucket with object lock refuses any status
   but Enabled with `-ERR_INVALID_BUCKET_STATE` and the message "bucket versioning cannot be
   disabled on buckets with object lock enabled" (`src/rgw/rgw_op.cc:2788-2793@v19.2.6`), lock-lifecycle's interface;
   MFA: `mfa_set_status` is dropped when it equals the current flag (`src/rgw/rgw_op.cc:2797@v19.2.6`); changing it
   requires a verified request (`src/rgw/rgw_op.cc:2799-2803@v19.2.6`); when MFA is already enabled, flipping between
   Enabled and Suspended requires a verified request (`src/rgw/rgw_op.cc:2805-2817@v19.2.6`); the forward to the master
   zone is a no-op in a single zone (`src/rgw/rgw_op.cc:2819-2824@v19.2.6`); under `retry_raced_bucket_write`, the MFA
   flag is set or cleared, Enabled sets `BUCKET_VERSIONED` and clears
   `BUCKET_VERSIONS_SUSPENDED`, Suspended sets both, and the instance is written with
   `put_info` (`src/rgw/rgw_op.cc:2828-2849@v19.2.6`). Nothing in the index changes: versioning is a flag on the bucket
   instance, which phase 1's `PutBucketInfo` writes under the version tracker with the cache
   notify.

**GetBucketVersioning** (`src/rgw/rgw_op.cc:2729-2757@v19.2.6`, `src/rgw/rgw_op.cc:2979@v20.2.4`): `s3:GetBucketVersioning`;
NoSuchBucket; the document holds `Status` (Enabled/Suspended) and `MfaDelete`
(Enabled/Disabled) only when the bucket has ever been versioned, else an empty
`VersioningConfiguration` (`src/rgw/rgw_rest_s3.cc:2152-2169@v19.2.6`, `src/rgw/rgw_rest_s3.cc:2255@v20.2.4`).

**Flags** (`src/rgw/rgw_common.h:1022-1026@v19.2.6`, `src/rgw/rgw_common.h:1074-1079@v19.2.6`; `src/rgw/rgw_common.h:1057-1061@v19.2.6`,
`src/rgw/rgw_common.h:1116-1121@v20.2.4`): `versioned()` is `BUCKET_VERSIONED`; `versioning_enabled()` is
versioned and not suspended; suspending keeps `BUCKET_VERSIONED`, so "ever versioned" is what
rgw-go's `neverVersioned` tests today (`internal/op/putobject.go:277-279`).

**Null versions.** Three things carry the `null` version id: a pre-versioning object (converted
on the key's first versioned write, §2.1), a suspended-bucket write (the instance stays empty,
the head is the plain oid, and the OLH links the empty instance, §2.3), and a suspended-bucket
delete marker (the empty instance with the `"\0d"` instance key, §2.1). The class keeps the data
null version and the null delete marker as two instance entries so the two can coexist
(`rgw_bucket_link_olh`, `src/cls/rgw/cls_rgw.cc:1705-1734@v19.2.6`). A request's
`versionId=null` is cleared before the OLH ops and the head read (`src/rgw/driver/rados/rgw_rados.cc:5768-5770@v19.2.6`,
`src/rgw/driver/rados/rgw_rados.cc:6601-6603@v19.2.6`; `get_oid` never encodes it), and `null_verid` only adds
`RGW_BILOG_NULL_VERSION` to the bilog flags (`src/rgw/driver/rados/rgw_rados.cc:8618-8622@v19.2.6`), which the single zone never writes.
rgw-go's `meta.NullInstance` and the attr-path clearing exist (`internal/driver/attrs.go:30-31`,
`internal/driver/delete.go:38-39`).

**Suspending while versions exist** changes no index entry: later writes become null versions
over the OLH head, listings keep showing the old versions, and a delete without an instance
writes the null marker (replacing a null data version's list entry, `src/cls/rgw/cls_rgw.cc:1712-1731@v19.2.6`).
**Re-enabling** likewise only flips the flag.

### 2.7 ListObjectVersions

**Parameters** (`RGWListBucket_ObjStore_S3::get_common_params` and `get_params`,
`src/rgw/rgw_rest_s3.cc:1710-1754@v19.2.6`, `src/rgw/rgw_rest_s3.cc:1821@v19.2.6`, `src/rgw/rgw_rest_s3.cc:1852@v20.2.4`): `versions` present
selects the versions listing (`src/rgw/rgw_rest_s3.cc:1712@v19.2.6`); `prefix`, `allow-unordered`, `delimiter`, `max-keys`
through `parse_max_keys`, `encoding-type` are the v1 listing's; the marker is
`(key-marker, version-id-marker)` as an `rgw_obj_key` (`src/rgw/rgw_rest_s3.cc:1750-1751@v19.2.6`). The op is `RGWListBucket`
with `list_versions` (`src/rgw/rgw_op.cc:3078-3121@v19.2.6`, `src/rgw/rgw_op.cc:3312@v20.2.4`); its action is
`s3:ListBucketVersions`; v20.2.4 refuses an indexless bucket first (`src/rgw/rgw_op.cc:3319-3324@v20.2.4`),
which rgw-go already does for both listings. The same `RadosBucket::list` serves it with
`params.list_versions` (`src/rgw/driver/rados/rgw_sal_rados.cc:877-904@v19.2.6`,
`src/rgw/driver/rados/rgw_sal_rados.cc:897@v20.2.4`).

**What the class returns.** With `list_versions` the shard returns every valid list entry in
list-key order: current and noncurrent versions, delete markers, and unversioned objects (their
plain entries), newest version first within a name (§2.1's `decreasing_str`), skipping only the
`FLAG_VER_MARKER` placeholders (`rgw_bucket_list`, `src/cls/rgw/cls_rgw.cc:604-608@v19.2.6`).
Without it, non-visible entries are skipped to the name's end (`src/cls/rgw/cls_rgw.cc:611-623@v19.2.6`); the gateway drops
non-visible entries again in `list_objects_ordered` (`src/rgw/driver/rados/rgw_rados.cc:1920-1924@v19.2.6`,
`src/rgw/driver/rados/rgw_rados.cc:1905@v20.2.4`) and rgw-go's `visible` filter mirrors it (`internal/driver/list.go:125-127`,
`internal/driver/list.go:225-234`).

**The marker.** The class resolves `(name, instance)` to a list key through
`encode_list_index_key` (`src/cls/rgw/cls_rgw.cc:358-390@v19.2.6`, `src/cls/rgw/cls_rgw.cc:408@v20.2.4`): no instance → the name (so the
listing resumes after every version of that name only if the name's plain key sorts before
them; in practice the versions of `name` sort after `name` itself, so a key-marker without a
version-id-marker lists the key's versions again — the same as S3); `null` → the empty
instance; an instance whose instance entry exists → that version's exact list key; one that does
not → `name + "\x01"`, past every version of the name (a stale or wrong `version-id-marker`
skips the rest of that key). The next marker is the last entry visited, name and instance
(`src/rgw/driver/rados/rgw_rados.cc:1998-2001@v19.2.6`; `next_marker = index_key`), which the response renders as
`NextKeyMarker` and `NextVersionIdMarker`, `null` for an empty instance
(`src/rgw/rgw_rest_s3.cc:1809-1817@v19.2.6`).

**Reconciliation.** `cls_bucket_list_ordered` checks an entry against its head when the entry
does not exist and is neither a delete marker nor a common prefix, or has pending ops
(`src/rgw/driver/rados/rgw_rados.cc:9823-9827@v19.2.6`); `check_disk_state` reads the key with its instance and never suggests
removing a delete marker (`src/rgw/driver/rados/rgw_rados.cc:10361@v19.2.6`). rgw-go's `needsDiskCheck` and `checkDiskState`
already carry the instance and the marker test (`internal/driver/list.go:399-405`, `internal/driver/list.go:465`,
`internal/driver/list.go:475`).

**The document** (`send_versioned_response`, `src/rgw/rgw_rest_s3.cc:1799-1869@v19.2.6`,
`src/rgw/rgw_rest_s3.cc:1910@v20.2.4`): `ListVersionsResult` with `EncodingType` first when `url`, the common fields,
`KeyMarker` and `VersionIdMarker` as sent, the next markers when truncated, then per entry a
`DeleteMarker` or `Version` section with `Key`, `VersionId` (`null` for an empty instance),
`IsLatest` = `is_current()`, `LastModified`, and for a version `ETag`, `Size` =
`meta.accounted_size`, `StorageClass` (canonical), then `Owner` and `Type`
Normal/Appendable. The `list-type=2&versions` form renders the malformed document the registry
describes; rgw-go renders both already (`internal/s3/listobjects.go:83-150`,
`internal/s3/listobjects.go:385-435`) from `op.ObjectEntry`'s `IsLatest`, `DeleteMarker`, `Key.Instance`; its
`NextVersionIdMarker` is fixed at `null` (`internal/s3/listobjects.go:400`) because `ListObjectsResult.NextMarker` is a
name only.

**Advance-and-retry.** A shard whose eight omap reads found only non-visible entries answers
`RGWBIAdvanceAndRetryError` (-EFBIG) with a marker in its reply, and radosgw's ordered listing
resumes from it (phase 1's exclusion "A shard that answers advance-and-retry fails an ordered
listing"). Versioning is what makes this reachable: a non-versions listing of a key with more
than eight reads' worth of delete markers and noncurrent versions. rgw-go's seam discards a
failed exec's output (`internal/radosclient/results.go:16-22`) although librados
delivers the out buffer with the negative rval, so the fix is a seam change (§7, task V0), not a
difference to keep.

### 2.8 MFA delete

**The header.** `RGWHandler_REST_S3::postauth_init` reads `x-amz-mfa` after the tenant,
bucket and object-name validation and calls `verify_mfa` into `s->mfa_verified`, discarding its
return value (`src/rgw/rgw_rest_s3.cc:4996-5001@v19.2.6`, `src/rgw/rgw_rest_s3.cc:5556@v20.2.4`): a malformed or
failed MFA never fails the request by itself; the op that needs MFA then answers
`-ERR_MFA_REQUIRED` (2044 → 403 AccessDenied, `src/rgw/rgw_common.cc:96@v19.2.6`). The check
runs for every S3 request carrying the header, whether or not the op needs MFA. rgw-go's
`PostAuthInit` notes the gap (`internal/s3/request.go:163-179`).

**verify_mfa** (`src/rgw/driver/rados/rgw_sal_rados.cc:284-312@v19.2.6`, `src/rgw/driver/rados/rgw_sal_rados.cc:301@v20.2.4`): the
header splits on single spaces into exactly two fields, `serial` and `pin`, else `-EINVAL`; the
serial must be in the user's `mfa_ids` (`RGWUserInfo`, `src/rgw/rgw_common.h:598@v19.2.6`,
`src/rgw/rgw_common.h:622@v20.2.4`; rgw-go's `meta.UserInfo.MFAIDs`), else `-EACCES`; then `check_mfa`, any failure
`-EACCES`; success sets `verified`.

**check_mfa** (`src/rgw/services/svc_cls.cc:46-63@v19.2.6`, same at v20.2.4): the otp object is
`user:<uid>` in the zone's `otp_pool` (`src/rgw/services/svc_cls.h:56-58@v19.2.6`; rgw-go's `ZoneParams.OTPPool`,
default `<zone>.rgw.otp`); `OTP::check` is two class calls on it
(`src/cls/otp/cls_otp_client.cc:60-96@v19.2.6`):

1. `otp_check` (RD|WR) with `cls_otp_check_otp_op` v1 `{id = serial, val = pin, token}` where
   `token` is 16 random alphanumerics; v19.2.6 sends it through `IoCtx::exec`, v20.2.4 through
   an `ObjectWriteOperation` (the only cls_otp change between the tags). The class
   (`src/cls/otp/cls_otp.cc:447-481@v19.2.6`) loads the device, records the attempt with the
   result of a TOTP validation against the seed, step, offset and window, unless five attempts
   already fell in the current step window (`ATTEMPTS_PER_WINDOW`, `src/cls/otp/cls_otp.cc:43@v19.2.6`, `src/cls/otp/cls_otp.cc:108-127@v19.2.6`), and
   writes the instance back. The two registry defects live in `verify` (`src/cls/otp/cls_otp.cc:129-153@v19.2.6`): a stored
   `step_size` of 0 faults the OSD, and the replay index takes the window distance unsigned.
2. `otp_get_result` (RD) with **the same `cls_otp_check_otp_op` buffer**: the client passes `in`
   rather than the `cls_otp_get_result_op` it encoded (`src/cls/otp/cls_otp_client.cc:78-83@v19.2.6`), and
   the class decodes a `cls_otp_check_otp_op` and finds the attempt by `op.token`
   (`src/cls/otp/cls_otp.cc:483-511@v19.2.6`), answering `cls_otp_get_result_reply{otp_check_t{token, timestamp,
   result}}`, `OTP_CHECK_UNKNOWN` when no attempt carries the token (rate-limited or evicted).
   `cls_otp_get_result_op` is dead on both sides; a reimplementation that sent it would get
   `-EINVAL`. The two-phase design exists because a write method cannot return data
   (`src/cls/otp/cls_otp.cc:543-553@v19.2.6`).

`check_mfa` is 0 only for `OTP_CHECK_SUCCESS`, else `-EACCES`. A successful check consumes the
code: `last_success` is the window index, and the same code is refused afterwards. Types:
`otp_info_t` v1 (type u8, id, seed, seed_type u8, seed_bin, time_ofs i32, step_size u32,
window u32), `otp_check_t` v1 (token, timestamp, result u8)
(`src/cls/otp/cls_otp_types.h@v19.2.6`); writepath's admin otp shares them.

**Where MFA is required** (all `rgw_op.cc`): DeleteObject naming a version on a bucket with
`BUCKET_MFA_ENABLED` (`src/rgw/rgw_op.cc:5158-5163@v19.2.6`, `src/rgw/rgw_op.cc:5548-5553@v20.2.4`); DeleteObjects when any key
names a version (`src/rgw/rgw_op.cc:7055-7068@v19.2.6`; v20.2.4's inverted test, `src/rgw/rgw_op.cc:7969-7983@v20.2.4`, is the registry's
security defect, §6); PutBucketVersioning when it changes `MfaDelete`, or flips Enabled/Suspended
while MFA is on (`src/rgw/rgw_op.cc:2795-2817@v19.2.6`, `src/rgw/rgw_op.cc:3032-3046@v20.2.4`). An admin's request is not exempt
at the op level, but `rgw_process` lets an admin through a refused permission before the MFA
test is reached (the exclusions entry "Deletes that need MFA are refused" covers the order).

**rgw-go today** verifies no token and refuses every MFA-requiring delete (exclusions); phase 2
replaces the refusal with the real check. The parity settings keep MFA off, so no s3-test
exercises it; the go-ceph admin suite has no MFA case; the specs are rgw-go's own (§5).

### 2.9 Racing writers: the pending/complete protocol and OLH log

**The sequence** (`set_olh`, `src/rgw/driver/rados/rgw_rados.cc:8473-8566@v19.2.6`, `src/rgw/driver/rados/rgw_rados.cc:9415@v20.2.4`;
`unlink_obj_instance`, `src/rgw/driver/rados/rgw_rados.cc:8568-8670@v19.2.6`, `src/rgw/driver/rados/rgw_rados.cc:9510@v20.2.4`), per attempt, up to `MAX_ECANCELED_RETRY`
100 then `-EIO`:

1. Read the OLH head's state without following (`get_obj_state`, `src/rgw/driver/rados/rgw_rados.cc:8498@v19.2.6`), after invalidating
   it when the previous attempt was cancelled.
2. `olh_init_modification` (`src/rgw/driver/rados/rgw_rados.cc:7607-7718@v19.2.6`, `src/rgw/driver/rados/rgw_rados.cc:8462@v20.2.4`), one write op on the OLH
   head: `create(exclusive)` when it does not exist, else `assert_exists` + `mtime2(state mtime)`
   (the mtime is kept); when the head has an OLH tag, `cmpxattr(olh.idtag, EQ, tag)`; when it
   exists without one (a plain object becoming versioned) `cmpxattr(olh.idtag, EQ, "")` and
   `setxattr(olh.info, {target = itself, removed = false})`; when it has no tag, new `idtag`
   and `olh.idtag` (32 lower alphanumerics each) and an empty `olh.ver`; always
   `setxattr(olh.pending.<op_tag>, RGWOLHPendingInfo{now})`. `-EEXIST` (the exclusive create
   lost) becomes `-ECANCELED` → retry.
3. The shard op behind the reshard guard: `bucket_link_olh` (§2.2) with `olh_tag` = the head's
   tag, `op_tag`, the marker meta, epoch 0, the unmod/precision pair; or
   `bucket_unlink_instance` with `bilog_flags | VERSIONED_OP (| NULL_VERSION)`.
4. On the shard op's failure: `olh_cancel_modification` (`src/rgw/driver/rados/rgw_rados.cc:7562-7605@v19.2.6`) removes the pending attr
   under the tag guard and, when the head has no `olh.info`, removes the head under
   `cmpxattr(olh.info, EQ, "")` + `obj_check_attrs_prefix(olh.pending., fail if exists)`; then
   `-ECANCELED` (the shard's tag mismatch) → `repair_olh` rewrites the head's `olh.idtag` and
   `olh.info` from the index's olh entry via `bi_get_olh` under the old tag's guard
   (`src/rgw/driver/rados/rgw_rados.cc:8079-8128@v19.2.6`) and retries; any other error → a best-effort `update_olh` and return.
5. `update_olh` (`src/rgw/driver/rados/rgw_rados.cc:8443-8471@v19.2.6`): loop `bucket_index_read_olh_log(ver_marker)` (tag-guarded by
   the class) + `apply_olh_log` until not truncated. `apply_olh_log` (`src/rgw/driver/rados/rgw_rados.cc:8213-8370@v19.2.6`): one write op
   on the head guarded by `cmpxattr(olh.idtag, EQ, tag)` and `cmpxattr(olh.ver, GTE, last_ver)`
   in u64 mode, setting `olh.ver = last_ver` and the kept mtime; replaying the entries in epoch
   order: LINK_OLH applies when its epoch is above the head's current epoch, or the head has no
   target instance, or the entry's instance sorts above the current one (`src/rgw/driver/rados/rgw_rados.cc:8286-8300@v19.2.6`);
   UNLINK_OLH marks removal; REMOVE_INSTANCE collects instances to delete; each entry's pending
   attr is removed; `olh.info` is set when a link won; the collected instance heads are deleted
   through the unversioned delete before the head op (`src/rgw/driver/rados/rgw_rados.cc:8334-8345@v19.2.6`); then `clear_olh` (the head
   removed under `cmpxattr(olh.idtag, EQ, tag)` + `cmpxattr(olh.ver, EQ, ver)` + no pending
   attrs, then `bucket_clear_olh`, `src/rgw/driver/rados/rgw_rados.cc:8387-8438@v19.2.6`) or `bucket_index_trim_olh_log(last_ver)`.
   `-ECANCELED` anywhere in `update_olh` after a successful link is success: "raced with another
   user" (`src/rgw/driver/rados/rgw_rados.cc:8556-8559@v19.2.6`).
6. A later reader's `follow_olh` (`src/rgw/driver/rados/rgw_rados.cc:8773-8825@v19.2.6`) expires pending attrs older than
   `rgw_olh_pending_timeout_sec` (1 h) in batches of 1000 and runs `update_olh` whenever any
   pending attr remains, so an interrupted writer's work is finished by the next reader, from
   either gateway; `-ECANCELED` there is reported as `-ENOENT`.

**How a race resolves.** Two writers of one key each write their own instance head and
instance entry, each adds its own pending attr (the exclusive create or the tag guard
serializes the head's creation), and the class serializes the links: epochs come from the
olh entry, promote is by epoch then instance, so whichever link the shard executes second is
current. Each writer's `apply_olh_log` sees both entries (the log is per epoch) and applies the
same winner; the `olh.ver GTE` guard makes a stale apply a no-op (a newer apply already moved
`olh.ver` past it) and `-ECANCELED` is success. A writer that dies after its link leaves a
pending attr; the next reader finishes the replay. A writer that dies after the complete but
before the link leaves an orphaned instance entry with no list entry (the
`rgw_debug_inject_set_olh_err` scenario, `src/common/options/rgw.yaml.in:2886@v20.2.4`), which
`bucket check` territory handles; both gateways share this. Concurrent deletes of one instance
end with the second's `unlink_instance` answering `-ENOENT` → 204. A write racing a delete: the
marker and the version are both linked, in class order. These are the same for a radosgw and an
rgw-go writer because every decision is made by the class or by a guarded head op; rgw-go
interoperates by sending the same ops with the same guards and tags.

**Three places rgw-go must differ from radosgw's own code to stay safe or possible:**

- *Completion ordering* (§2.3): await the ADD complete before `set_olh`; radosgw relies on
  librados ordering its own asynchronous complete ahead of the link.
- *The u64 cmpxattr.* The librados C API's `rados_write_op_cmpxattr` and `rados_read_op_cmpxattr`
  compare in string mode only (`src/librados/librados_c.cc:3667-3683@v19.2.6`, `src/librados/librados_c.cc:4148-4164@v19.2.6`);
  the C++ u64 mode parses the stored `olh.ver` as a number, an empty one as 0
  (`src/osd/PrimaryLogPG.cc:4959-4974@v19.2.6`). A string GTE would compare "9" above "10".
  go-ceph cannot add the mode without a librados change. rgw-go therefore guards
  `apply_olh_log`'s head op with `cmpxattr(olh.ver, EQ, <the bytes it read>)` in string mode:
  stricter than radosgw's GTE (it also fails when another writer applied an older-or-equal
  epoch in between), and that failure is `-ECANCELED` → success by the protocol, with the
  pending attrs left for the next reader's replay. `clear_olh`'s EQ on `to_string(ver)` is the
  same bytes in both modes. The state written is identical; a `docs/cgo-limitations.md` entry
  records the missing mode (§7, V4).
- *Conditional writes are check-then-act* on both floors (§2.3): Tentacle evaluates against
  the current version, then links without any guard tying the link to that evaluation; two
  `If-None-Match: *` writers can both succeed, and a `If-Match` write can land after the
  version it matched was replaced. Under the owner's rule this is an S3-correctness race rgw-go
  need not reproduce. It can be closed with the protocol's own tools: evaluate the condition on
  the OLH head state read in step 1, then in step 2 add `cmpxattr(olh.info, EQ, <bytes read, or
  absent>)` and `obj_check_attrs_prefix(olh.pending., fail if exists)`; a racing writer that has
  linked but not yet applied holds a pending attr, so the guard fails with `-ECANCELED` and the
  condition is re-evaluated on the new state. The state written is unchanged; radosgw's writers
  always hold a pending attr during their window, so the guard sees them too. Recommended, as
  an open question (§8, Q1) because it is stricter than S3 requires under contention.

**Floor races rgw-go meets without a choice:** the class's `complete_op` stale-epoch defect
(tracker #80894; registry) applies to instance entries as to plain ones and no client can
avoid it; rgw-go sends radosgw's cancel version (owner decision 2). Tentacle's unguarded head
removal (registry) is on the unversioned delete, which `apply_olh_log` reuses for instance
heads; rgw-go keeps phase 1's guard there.

### 2.10 Interaction with phase 1: reshard, cache invalidation, ACL, policy, quota, multipart

- **Reshard protocol.** `bucket_link_olh`, `bucket_unlink_instance`, `bucket_trim_olh_log` and
  `bucket_clear_olh` run inside `guard_reshard` with `assert_exists` and the guard call on
  every release (§2.2; `RGWRados::guard_reshard`, `src/rgw/driver/rados/rgw_rados.cc:7720-7770@v19.2.6`,
  `src/rgw/driver/rados/rgw_rados.cc:8575@v20.2.4`, re-initializes the shard from the bucket on each try). v20.2.4's class guards
  them itself as well, with the IN_LOGRECORD threshold (`src/cls/rgw/cls_rgw.cc:906-922@v20.2.4`).
  `bucket_read_olh_log` and `bi_get` are reads without the guard. rgw-go's `indexOp.guardReshard`
  and `blockWhileResharding` (`internal/driver/indexop.go:227-320`) are reused for
  the four writes; the shard is the key's hash shard in the current layout, instance included in
  the key but not in the hash (`get_index_key_name` only). A reshard copies instance, list and
  olh entries as opaque `bi` entries, so a resharded bucket's versions keep working from either
  gateway.
- **Cache invalidation.** PutBucketVersioning writes the bucket instance through
  `put_info` under `retry_raced_bucket_write`; phase 1's `PutBucketInfo` with its version
  tracker and the control-pool notify is the path; a coexisting radosgw sees the flags within
  the notify (gate case, §4). Object state is not cached across requests in either gateway
  (`RGWObjectCtx` is per request); the OLH head is re-read per `set_olh` attempt.
- **ACL and policy.** The `s3:*Version*` actions, chosen when the request names an instance
  (`src/rgw/rgw_op.cc:990@v19.2.6`, `src/rgw/rgw_op.cc:1039@v19.2.6`, `src/rgw/rgw_op.cc:1078@v19.2.6`, `src/rgw/rgw_op.cc:1120@v19.2.6`, `src/rgw/rgw_op.cc:5145@v19.2.6`, `src/rgw/rgw_op.cc:5746@v19.2.6`, `src/rgw/rgw_op.cc:6829@v19.2.6`, `src/rgw/rgw_op.cc:7767@v19.2.6`),
  exist in `internal/policy/action.go` and the ops already select them. The object
  ACL evaluated is the resolved version's (the state read follows the OLH). A delete marker
  carries the requester as its index owner (`src/rgw/driver/rados/rgw_rados.cc:5797-5798@v19.2.6`), which the listing renders.
- **Quota and stats.** Every version is an index entry the complete accounts
  (`complete_op` ADD, §2.2) and the unlink's delete unaccounts; delete markers are instance
  entries created by the class without stats; `check_quota` on a PUT counts the bucket's totals
  as radosgw does, so nothing changes in M's enforcement.
- **Multipart.** `CompleteMultipartUpload` is the versioned write (§2.3); parts live in the
  `multipart` namespace and are never versioned (`set_versioning_disabled`,
  `src/rgw/driver/rados/rgw_putobj_processor.cc:372@v19.2.6`); UploadPartCopy's source
  resolution is the versioned read.
- **GC.** An overwritten null version's tails go to the GC through
  `complete_atomic_modification` as in phase 1; an unlinked instance's tails through the
  unversioned delete `apply_olh_log` issues.
- **Listing reconciliation** (M) already carries the instance and the marker rule (§2.7).

## 3. What rgw-go already has

All at origin/main (0239625); the worktree HEAD af535e4 is behind it in files this area does
not touch except `internal/s3/handler.go`, `internal/s3/request.go`,
`internal/driver/mp_complete.go` and the two deselect lists.

**Encodings and names**

- `internal/cls/rgw/const.go`: the method names (no OLH or `bi_get`), `ModifyOp` incl.
  `OpLinkOLH` 4, `OpLinkOLHDM` 5, `OpUnlinkInstance` 6, `BILogFlagVersionedOp` 0x1,
  `BILogNullVersion` 0x2, the five entry flags. `types_index.go`: `DirEntry` with `Flags`,
  `VersionedEpoch`, `Tag`, `PendingMap`; `DirEntryMeta`. `types_key.go`: `ObjKey{Name,
  Instance}`, `EntryVer`. `ops_index.go`: `PrepareOp`/`CompleteOp` with `BILogFlags`,
  `ListOp.ListVersions`, `GuardBucketResharding`. **Missing:** the six OLH request/reply structs,
  `OLHEntry`/`OLHLogEntry`, `bi_get`, and the index key encoders the emulator needs (V1).
- `internal/meta/attrs.go:27-30`: `AttrOLHPrefix`, `AttrOLHInfo`, `AttrOLHVer`, `AttrOLHIDTag`.
  **Missing:** `AttrOLHPendingPrefix`, `OLHInfo`, `OLHPendingInfo` (V2). `objkey.go`:
  `NullInstance`, `OID` (instance-aware), `IndexKeyName`, `Locator`, `ParseIndexKeyName`; all
  match §2.1. `internal/meta/bucket_info.go:16-23`: the flags; `ObjLockEnabled`; `internal/driver/copy.go:247-250` has
  `versioningEnabled` (driver-local, to lift into `meta`). `internal/meta/user.go:286`: `MFAIDs`.
  `internal/meta/zone_params.go:32`: `OTPPool`.

**Driver (`internal/driver`)**

- `internal/driver/stat.go:148-152`: `decodeState` refuses any head with `olh.ver` as 501: the follow goes here
  (V6). `internal/driver/attrs.go:30-31`, `internal/driver/attrs.go:45`: the `null` instance cleared and "null version without manifest
  is ENOENT" already for attr writes.
- `internal/driver/delete.go:28-39`: the unversioned delete, refusing VERSIONED|SUSPENDED|OBJ_LOCK buckets;
  `removeHead` keeps the `olh.` prefix (`internal/driver/delete.go:84`). `internal/driver/headwrite.go:206-216`: the reset keeps the
  prefix; `internal/driver/headwrite.go:315-349` `guardHead` already has "Tentacle guards no versioned instance" and
  Tentacle's conditions on both releases, evaluated on the state passed in: for a versioned
  write that state must become the current version's (V7).
- `internal/driver/indexop.go`: `indexOp` with `prepare` (guarded), `complete`/`completeDel`/`cancel`
  (fire-and-forget through `completionManager`), `guardReshard` (`internal/driver/indexop.go:227-252`),
  `blockWhileResharding`, `refreshIndexOp`; `clsKey` = `(IndexKeyName, Instance)` (`internal/driver/indexop.go:122-124`).
  **Needed:** an awaited complete, and a generic guarded shard write for the four OLH methods
  (V5). `internal/driver/completion.go`: the manager and its retry worker.
- `internal/driver/list.go`: `listObjectsOrdered`/`listOrdered` pass `ListVersions` to the class and filter
  `visible` (`internal/driver/list.go:125-127`); `objectEntry` fills `IsLatest`, `DeleteMarker`, `Key.Instance`
  (`internal/driver/list.go:241-262`); `checkDiskState` carries the instance and the marker rule (`internal/driver/list.go:458-525`).
  **Missing:** a marker instance in and out, and advance-and-retry (V9).
- `internal/driver/mp_complete.go:269-271`: the refusal; `internal/driver/mp_complete.go:351`, `internal/driver/mp_complete.go:397`: `attrMPCompletionInstance`, the seam
  for the completion's instance. `internal/driver/copy.go:149-152`: strips OLH attrs from a copy into a bucket
  that is not versioning-enabled (radosgw's `copy_obj` behaviour, keep). `internal/driver/purge.go`,
  `internal/driver/bucketadmin.go`: the admin removals' driver halves, refusing versioned buckets.
- `internal/driver/cache.go`: the object cache with notify invalidation; `PutBucketInfo` is the write path
  PutBucketVersioning needs (no change).

**Op layer (`internal/op`)**

- `internal/op/object.go:139-163`: `ObjectStore` (`StatObject`, `PrefetchObject`, `ReadObject`,
  `PutObject`, `DeleteObject`, `CopyObject`, `SetObjectAttrs`); `ObjectState` (`internal/op/object.go:12-43`) has no
  delete-marker or resolved-instance field; `PutResult.Version` exists; `DeleteObject` returns
  only `error` (V8 changes it); `DeleteParams` carries Tentacle's conditions (`internal/op/object.go:89-110`).
- `internal/op/putobject.go:213-280`: `versioningUnserved`, `newHeadVersioningUnserved`, `neverVersioned`
  (the 501 gates to remove, task by task); `Execute` `internal/op/putobject.go:131-182` passes `r.Object` as the key.
- `internal/op/deleteobject.go`: `Versioned`, `VersionID`, `DeleteMarker`, the MFA refusal (`internal/op/deleteobject.go:106-116`),
  `mfaEnabled` (`internal/op/deleteobject.go:140-142`). `internal/op/deleteobjects.go:156-190`: the MFA loop as v19.2.6, the
  per-key refusal; the result fields render `DeleteMarker`/`MarkerVersionID`.
- `internal/op/listobjects.go:121-123`: the versions refusal; `internal/op/bucket.go:36-92`: `ListObjectsParams`
  (`Marker` a name, `ListVersions`), `ObjectEntry`, `ListObjectsResult.NextMarker` (a name).
- `internal/op/getobject.go`, `internal/op/objectread.go`, `internal/op/objectattrs.go`: `Versioned` and `VersionID` plumbed.
  `internal/op/request.go:69-70`, `internal/op/request.go:82-84`: `Object.Instance` from `versionId`; `BucketRec`, `ObjState`.
  **Missing:** `Request.MFAVerified` and an `MFAVerifier` in `Env` (V13).
- `internal/op/bucketsubres.go`: the pattern for bucket subresource ops (`RetryRacedBucketWrite`,
  `verifyBucketAction`); no versioning op.
- `internal/policy/action.go`: every `s3:*Version*` action and the two bucket-versioning
  actions.

**S3 layer (`internal/s3`)**

- `internal/s3/dispatch.go:131`, `internal/s3/dispatch.go:161`: the `get_bucket_versioning`/`set_bucket_versioning` rows;
  `internal/s3/dispatch.go:319`: the set's signed-payload form. `internal/s3/handler.go:100-118`: per-op `Register`, no hot file;
  an unregistered route is 501 (`internal/s3/handler.go:377-380`).
- `internal/s3/request.go:93`: `versionId` → `Object.Instance`; `internal/s3/request.go:163-179`: `PostAuthInit` without the
  `x-amz-mfa` step. `internal/s3/copyobject.go:146`, `internal/s3/multipart.go:275`: the copy source's `versionId`.
- `internal/s3/listobjects.go:83-150`, `internal/s3/listobjects.go:385-435`: both versions documents, `versionIDMarker` passed
  through, `NextVersionIdMarker` fixed at `null` (`internal/s3/listobjects.go:400`).
- `internal/s3/deletemarker.go`: `x-amz-delete-marker: false` on Tentacle only, for a BeforeVerify
  NoSuchKey. `internal/s3/deleteobject.go:32-36`, `internal/s3/putobject.go:63-64`, `internal/s3/getobject.go:281-282`,
  `internal/s3/getobjattrs.go:35-36`: the version headers when set.

**Emulator and seam**

- `internal/testutil/fakerados/cls_rgw.go`: a `switch` dispatch (`internal/testutil/fakerados/cls_rgw.go:58-105`); prepare, complete,
  suggest and the list marker refuse any instance with EOPNOTSUPP (`internal/testutil/fakerados/cls_rgw.go:440-441`, `internal/testutil/fakerados/cls_rgw.go:480-481`,
  `internal/testutil/fakerados/cls_rgw.go:998-1003`, `internal/testutil/fakerados/cls_rgw.go:1164-1165`); `rgwVisible`, `rgwDecodeListIndexKey` exist (`internal/testutil/fakerados/cls_rgw.go:1092-1120`).
  `internal/testutil/fakerados/steps.go`: string-mode `cmpxattr` (`internal/testutil/fakerados/steps.go:541-552`), `exec` (`internal/testutil/fakerados/steps.go:720`). No `cls_otp.go`.
- `internal/radosclient/results.go:16-22`: `ExecResult.Bytes` drops the output on a negative
  rval (V0). `goceph/translate.go:163-171`: string-mode `CmpXattr` only (no u64 mode exists in
  the C API, §2.9).

**Admin and harness**

- `internal/admin/bucket.go:131-138`: `versioning` off/enabled/suspended in bucket stats.
  `internal/op/adminbucket.go` (`RemoveObjectAdmin`), `adminpurge.go` (`refuseVersionedPurge`):
  the 501 gates.
- `hack/s3tests/run.sh`: `EXCLUDED_MARKERS` has `versioning` and `delete_marker`;
  `hack/s3tests/deselect-phase2.txt:11-12` names `test_versioning_obj_create_read_remove` and `_head`.
  `hack/admin/deselect-phase2.txt:10`: `enable_versioning`. `test/s3tests/baseline/*.json` and
  `test/admin/baseline/*.json`: no versioning outcome recorded.
- `docs/exclusions.md`: the entries to retire ("PutBucketVersioning answers 501", "Writes to a
  versioned or object-lock bucket answer 501", "The versions of a bucket whose versioning was
  ever enabled are not listed", "Deletes that need MFA are refused", "A purging bucket removal
  refuses…", "The admin object removal refuses…", "CompleteMultipartUpload sends no
  x-amz-version-id", the olh clauses of "Tentacle's x-amz-delete-marker…") and to amend ("A
  shard that answers advance-and-retry fails an ordered listing"; the `bi_*` sentence in the
  rgw-rs section).

## 4. Coexistence

**What both gateways must write and read identically** (every item is state the other reads
on its next request):

1. The head oid and locator of an instance (`_:<instance>_<name>`, §2.1); the instance alphabet
   (no underscore, 32 chars; the class and `parse_raw_oid` split on `_` and `:`).
2. The OLH head's five xattr names and encodings (`RGWOLHInfo` v1, `RGWOLHPendingInfo` v1,
   `olh.ver` as a decimal string, empty at creation; the pending name's time prefix, which
   orders expiry).
3. The six class requests at their struct versions (v5/v3/v1/v1/v1/v1), the `olh_tag` as the
   xattr's bytes, `op_tag` shared between the pending attr and the log entry, epoch 0 in every
   request, `log_op` false in a single zone, `zones_trace` with the zone id and bucket key,
   `bilog_flags` with VERSIONED_OP (+NULL_VERSION) on unlink.
4. The index entries the class writes from those requests: instance, list, olh data and version
   marker keys and their `rgw_bucket_dir_entry`/`rgw_bucket_olh_entry` payloads, the stats the
   complete accounts.
5. The protocol order: prepare → head → complete → OLH head pending → link → replay → trim, and
   the guards (`olh.idtag` EQ, `olh.ver` numeric GTE in radosgw / string EQ-on-read in rgw-go,
   both monotone), so a half-done op from either gateway is finished by the other's reader.
6. Delete markers: no head, marker owner and mtime in the index meta, `info.removed`.
7. Bucket instance flags `BUCKET_VERSIONED`, `BUCKET_VERSIONS_SUSPENDED`, `BUCKET_MFA_ENABLED`
   and the cache notify on their change.
8. MFA: the otp object `user:<uid>` in `otp_pool`, `otp_info_t` v1 as `radosgw-admin mfa create`
   writes it, the two-phase `otp_check`/`otp_get_result` with the check-op buffer, the user's
   `mfa_ids`.

**Cross-gateway gate cases** (`[cluster]`, both releases; radosgw = the Rook store's gateway,
rgw-go beside it on the same zone):

- A. radosgw enables versioning, writes v1, v2, a delete marker, suspends, writes a null
  version, deletes the null marker; rgw-go: GET (current, each version, `null`), HEAD of the
  marker's instance (404/405 per Q2), ListObjectVersions with and without markers, DeleteObject of
  a noncurrent version, PutObjectAcl on a version; then radosgw lists and `radosgw-admin bucket
  check` reports clean.
- B. The mirror: rgw-go writes the same sequence; radosgw GETs, lists, `object stat`,
  `bi list` shows instance/list/olh entries with the expected flags and epochs, `bucket check`
  clean; `radosgw-admin olh get`.
- C. Interleaved writers: N racing PUTs of one key split across both gateways; after quiescence
  both list the same current version and version count, every version readable, `bucket check`
  clean. Same with racing PUT and DELETE, and two concurrent deletes of one instance (one 204
  each).
- D. A pending OLH op left by radosgw (`rgw_debug_inject_set_olh_err` set on the Rook radosgw
  through `ceph config set`), reconciled by rgw-go's next read; and the mirror: rgw-go killed
  between link and replay (an injected failure in the driver under a test flag), reconciled by
  radosgw's read. The 1 h pending expiry is exercised with a clock-skewed fake, not on the
  cluster.
- E. A pre-versioning object written by one gateway, versioned by the other: the first versioned
  PUT converts the plain entry; both list the `null` version as noncurrent.
- F. PutBucketVersioning on one gateway is visible to the other within the notify (a PUT on the
  other immediately creates a version).
- G. Reshard during versioned writes: `radosgw-admin bucket reshard` while rgw-go writes
  versions and deletes; no lost version, `bucket check` clean; the resharded bucket's versions
  list from both.
- H. MFA: `radosgw-admin mfa create --totp-seed …` for a user; rgw-go verifies a code generated
  from the seed (`oathtool`-equivalent in the test), refuses a wrong one and a reused one, and
  the device's attempt log shows the checks (`radosgw-admin mfa check`); PutBucketVersioning
  MfaDelete on rgw-go is seen by radosgw's GetBucketVersioning.
- I. Admin: `DELETE /admin/bucket?object` on rgw-go leaves a marker radosgw lists; a purging
  removal by rgw-go of a bucket radosgw filled with versions leaves nothing (`radosgw-admin bi
  list` empty, pools clean after GC).

## 5. Tests

### s3-tests

At the harness PIN f6f2529dd9efccf7710c8a2b561e5a2ab9d48bd3 (`hack/s3tests/PIN`),
`test_s3.py` has 69 tests carrying `versioning` or `delete_marker`. The gate task removes
both markers from `EXCLUDED_MARKERS` (`hack/s3tests/run.sh`) and deletes the two
`deselect-phase2.txt` lines; the parity set then gains:

- *Versioning proper (33):* `test_versioning_obj_create_read_remove`, `…_head` (the deselected
  pair), `test_bucket_list_return_data_versioning`, `test_versioning_concurrent_multi_object_delete`,
  `test_object_put_acl_mtime`, `test_object_copy_versioned_bucket`,
  `test_object_copy_versioned_url_encoding`, `test_object_copy_versioning_multipart_upload`,
  `test_multipart_copy_versioned`, `test_versioning_bucket_create_suspend`,
  `test_versioning_stack_delete_markers`, `test_versioning_obj_plain_null_version_removal`,
  `test_versioning_obj_plain_null_version_overwrite`,
  `test_versioning_obj_plain_null_version_overwrite_suspended`,
  `test_versioning_obj_suspend_versions`, `test_versioning_obj_suspended_copy`,
  `test_versioning_obj_create_versions_remove_all`,
  `test_versioning_obj_create_versions_remove_special_names`,
  `test_versioning_obj_create_overwrite_multipart`, `test_versioning_obj_list_marker`,
  `test_versioning_copy_obj_version`, `test_versioning_multi_object_delete`,
  `test_versioning_multi_object_delete_with_marker`,
  `test_versioning_multi_object_delete_last_missing`,
  `test_versioning_multi_object_delete_with_marker_create`, `test_versioned_object_acl`,
  `test_versioned_object_acl_no_version_specified`,
  `test_versioned_concurrent_object_create_concurrent_remove`,
  `test_versioned_concurrent_object_create_and_remove`,
  `test_versioning_bucket_atomic_upload_return_version_id`,
  `test_versioning_bucket_multipart_upload_return_version_id`,
  `test_get_versioned_object_attributes`.
- *Delete markers (3):* `test_delete_marker_nonversioned` (the only `delete_marker`-only test),
  `test_delete_marker_versioned`, `test_delete_marker_suspended`.
- *Conditional writes on versions (20, `conditional_write` + `versioning`; `conditional_write` is
  not excluded, owner decision 14):* `test_put_current_object_if_none_match`,
  `test_multipart_put_current_object_if_none_match`, `test_put_current_object_if_match`,
  `test_multipart_put_current_object_if_match`, `test_put_object_current_if_match`,
  `test_put_object_suspended_if_match`, `test_multipart_put_object_suspended_if_none_match`,
  `test_versioning_multi_object_delete_last_precondition_failed`, and the twelve
  `test_delete_object{,s}_{current,version}_if_match{,_last_modified_time,_size}`. These pin §2.3's
  Tentacle evaluation; on Squid radosgw is expected to fail the PUT ones (§6, defect 3), which the
  recording confirms and `known-differences-squid.txt` then lists, each backed by the exclusion.
- *Crossing into lock-lifecycle (1):* `test_object_lock_put_obj_lock_enable_after_create` carries
  only `versioning` but needs PutObjectLockConfiguration; it goes into `deselect-phase2.txt`
  under the object-lock heading unless lock lands first (coordination item).
- *Stay excluded by other markers (13):* the ten `lifecycle*` tests, `test_delete_marker_expiration`
  (lifecycle), the three `cloud_*` tests.

The Squid and Tentacle ledgers (`test/s3tests/baseline/*.json`) hold no outcome for any of
these, so the task that frees them re-records both ("the PR that lands a feature deletes its
lines and records the tests they free … with `make s3tests-record-tests`",
`hack/s3tests/deselect-phase2.txt:5-7`). Expected radosgw failures at either floor
beyond defect 3: none known; `fails_on_dbstore` is irrelevant to RADOS.

### go-ceph rgw/admin suite

`TestRadosGWTestSuite/TestBucket/enable_versioning` (PutBucketVersioning through the S3 client,
then `GetBucketInfo` asserts `versioning == "enabled"`, `rgw/admin/bucket_test.go:65-85` in the
go-ceph checkout) leaves `hack/admin/deselect-phase2.txt`; both admin ledgers are re-recorded
(`make admin-record`). `enable_bucket_object_lock` is lock-lifecycle's.

### Specs the plan must require

*Emulator (`fakerados`):* `bucket_link_olh` (every branch of §2.2: new olh, existing, tag
mismatch → ECANCELED, old epoch, promote/demote, null data/marker pair, unmod_since, plain
conversion), `bucket_unlink_instance` (current with a next version, current without, noncurrent,
delete marker, missing → ENOENT, old epoch), `bucket_read_olh_log` (marker, truncation at 1000,
tag mismatch), `trim`, `clear` (VER_MARKER rule), `bi_get`; prepare/complete/suggest on instance
keys with the flag reset; `bucket_list` with instance and list entries, version markers skipped,
`list_versions` on and off, the marker resolution of `encode_list_index_key`, advance-and-retry
after eight reads; string-mode `cmpxattr` on an empty xattr. Each emulated branch cites its class
lines; the emulator is Squid's class (no class-side guard), as today.

*cls marshalling:* goldens from `ceph-dencoder` for `rgw_cls_link_olh_op`,
`rgw_cls_unlink_instance_op`, `rgw_cls_read_olh_log_op`/`_ret`, `rgw_cls_trim_olh_log_op`,
`rgw_cls_bucket_clear_olh_op`, `rgw_bucket_olh_entry`, `rgw_bucket_olh_log_entry`, `RGWOLHInfo`,
`RGWOLHPendingInfo`, `rgw_cls_bi_get_op`/`_ret`, `otp_info_t`, `otp_check_t`,
`cls_otp_check_otp_op`, `cls_otp_get_result_reply` (all have `generate_test_instances`), at
Squid encoding; the index key encoders against literal byte strings from §2.1.

*Driver (unit, on the emulator):* each §2.9 step with its guard failing (ECANCELED → retry;
EEXIST → ECANCELED; repair_olh on tag mismatch; MAX 100 → EIO); pending expiry (clock);
`follow_olh` finishing another writer's pending op; `apply_olh_log`'s replay rules (epoch,
instance tie-break, UNLINK then LINK, REMOVE_INSTANCE deletes the head through the guarded
unversioned delete); the awaited complete before the link, pinned by a spec that fails if the
link is issued first; the null version colocated with the OLH (reset keeps `olh.*`); the
versioned delete's two paths and `bi_get`; Tentacle's conditions against the current version
on both releases, including the race spec for Q1's guard if adopted; the EQ-on-read `olh.ver`
guard tolerating a concurrent newer apply; listing markers with instances; advance-and-retry.

*Op and S3 (unit, on fakes):* PutBucketVersioning's parse table, flag transitions, the object
lock refusal, the MFA rules; GetBucketVersioning's three documents; version headers on PUT, copy
(both), complete, delete, GET, `null`; DeleteObjects' grouping, MFA (v19 rule on both),
`DeleteMarker`/`DeleteMarkerVersionId`; GET/HEAD of a current delete marker (404 + header on
both releases), of a marker instance (Q2); ListObjectVersions' markers and `NextVersionIdMarker`;
`versionId` on PUT (Q3); the `s3:*Version*` action selection; `x-amz-mfa` parsing and the
request flow with a verifier fake; the admin removals.

*Integration (`[cluster]`):* §4's cases A–I, each release.

## 6. Floor defects

Class per the owner's rule (security, data corruption, S3-correctness, cosmetic); status
"unreproduced" until a running release shows it; rgw-go's choice. R = already in
`docs/ceph-upstream-bugs.md`.

| # | defect | releases | class | status | rgw-go |
|---|---|---|---|---|---|
| 1 | DeleteObjects asks for MFA for keys without a version and not for versions (R, tracker #77869) | v20.2.1–v20.2.4 | security (MFA bypass) | unreproduced | v19.2.6/main's test on both releases (exclusion exists) |
| 2 | `cls_rgw complete_op` writes a stale epoch back on cancel (R, #80894); applies to instance entries too | both | data corruption (index meta) | reproduced | radosgw's `-1:0` cancel; class-side, no client workaround (owner decision 2) |
| 3 | Squid evaluates a versioned PUT's If-Match/If-None-Match against the new, nonexistent instance: If-Match always 412, If-None-Match `*` never fails (`src/rgw/driver/rados/rgw_rados.cc:3171@v19.2.6`, `src/rgw/driver/rados/rgw_rados.cc:6493-6543@v19.2.6`; fixed by `get_current_version_state`, `src/rgw/driver/rados/rgw_rados.cc:7238-7258@v20.2.4`) | v19.2.6 | S3-correctness | unreproduced (expected visible in the Squid recording of the `conditional_write`+`versioning` tests) | Tentacle's current-version evaluation on both (PP-3 rule); exclusion + `known-differences-squid` |
| 4 | Conditional writes on versioned buckets are check-then-act: two `If-None-Match: *` writers both succeed; an `If-Match` write lands after its matched version was replaced (`src/rgw/driver/rados/rgw_rados.cc:3282-3300@v20.2.4`; no guard ties the link to the evaluation) | both | S3-correctness (race) | unreproduced | not reproduced if Q1's OLH-head guard is adopted; else same as radosgw, documented |
| 5 | CopyObject sends neither `x-amz-version-id` nor `x-amz-copy-source-version-id` (`src/rgw/rgw_rest_s3.cc:3566-3603@v19.2.6`, `src/rgw/rgw_rest_s3.cc:3873@v20.2.4`) | both | S3-correctness | unreproduced | send both (Q4); exclusion |
| 6 | GET/HEAD naming a delete marker's version id answers 404 NoSuchKey (v20.2.4 with `x-amz-delete-marker: false`), S3 answers 405 MethodNotAllowed with `true` (§2.5) | both | S3-correctness | unreproduced | 405 + `true` after a `bi_get` of the instance on the 404 path (Q2); exclusion |
| 7 | GET/HEAD whose current version is a delete marker: 404 without `x-amz-delete-marker` (`src/rgw/rgw_rest.cc:1893-1933@v19.2.6`; Tentacle sends it, `src/rgw/rgw_rest.cc:1936-1944@v20.2.4`) | v19.2.6 | S3-correctness | unreproduced | send it on both (Tentacle feature level); exclusion on Squid |
| 8 | `x-amz-version-id` omitted when the version id is `null` (PutObject, CompleteMultipart on a suspended bucket; GET of a null current version), where S3 sends `null` (`dump_header_if_nonempty`, `src/rgw/rgw_rest_s3.cc:461@v19.2.6`, `src/rgw/rgw_rest_s3.cc:2748@v19.2.6`, `src/rgw/rgw_rest_s3.cc:4081@v19.2.6`) | both | S3-correctness (minor) | unreproduced | send `null` when the bucket is versioned and the instance empty (Q5); exclusion |
| 9 | `versionId` on PutObject/CopyObject's destination is honoured as the instance on a non-versioning-enabled bucket (R, "radosgw indexes a PutObject naming versionId=null …") | both | S3-correctness | unreproduced | ignore the parameter on PUT and copy destination (Q3); exclusion; registry entry updated |
| 10 | `cls_otp` divides by a stored `step_size` of 0 (R, #80948) | both | denial of service, privileged | liboath reproducer only | this area never writes otp entries; writepath's admin otp refuses 0 |
| 11 | `cls_otp` replay index from the unsigned window distance (R, #80949) | both | S3-correctness (TOTP replay window) | liboath reproducer | class-side; rgw-go calls the class as radosgw does |
| 12 | `cls_otp_client` sends the check-op buffer to `otp_get_result`, whose server decodes a check op; `cls_otp_get_result_op` is dead (`src/cls/otp/cls_otp_client.cc:78-83@v19.2.6`, `src/cls/otp/cls_otp.cc:487@v19.2.6`) | both | cosmetic (consistent by accident) | n/a | rgw-go sends the check op to `otp_get_result`; a registry note so no one "fixes" it one-sided |
| 13 | `RGWRados::get_olh` decodes `olh.ver` as `RGWOLHInfo` (`src/rgw/driver/rados/rgw_rados.cc:8700-8705@v19.2.6`; reads `olh.info` at `src/rgw/driver/rados/rgw_rados.cc:9640@v20.2.4`); only `radosgw-admin olh get` calls it | v19.2.6 | cosmetic (admin tool) | unreproduced | not met: rgw-go has no such call; recorded as not-met so the gate's `olh get` on Squid is read accordingly |
| 14 | Tentacle removes a head without checking its write tag (R, #80898/#80906); reached by `apply_olh_log`'s instance deletion | v20.2.4 | data corruption (tail leak) | unreproduced | phase 1's guard kept (exclusion exists) |
| 15 | Squid before 19.2.3 refused a marker on a marker (R) | below floor | n/a | n/a | unaffected |

Not a radosgw defect but made reachable by this area: the seam's dropped output on
`RGWBIAdvanceAndRetryError` (phase 1 exclusion "A shard that answers advance-and-retry fails an
ordered listing"), fixed by V0 rather than kept.

## 7. Proposed tasks

Sixteen tasks, one layer each (PP-6), briefs aimed under 250 lines; `[cluster]` marks a task
that needs a disposable cluster. Dependencies name the task that produces a seam. Hot files and
their owners follow the list.

**V0 — seam: a failed class call keeps its output.** Layer: seam (`internal/radosclient`,
`goceph`, `fakerados/steps.go`). `ExecResult` exposes the out buffer with a negative rval (a new
accessor; `Bytes()` keeps its contract); goceph copies the buffer the C API already returns;
the emulator's `failResults` delivers it. Behaviour: `bucket_list`'s `RGWBIAdvanceAndRetryError`
reply with its marker (`src/cls/rgw/cls_rgw.cc:680-691@v19.2.6`). Specs: seam unit on the
emulator; an integration spec against a shard. Deps: none. PR: one. ~120 lines. Retires the
exclusion "A shard that answers advance-and-retry fails an ordered listing" together with V9.

**V1 — `cls/rgw`: the OLH methods, `bi_get`, the index key encoders.** Layer: cls. Files: new
`ops_olh.go`, `types_olh.go`, `ops_bi.go`, `indexkey.go` (+ tests, goldens); `const.go` gains
five names. Behaviour: §2.2's structs at their versions and the builders' field order
(`src/cls/rgw/cls_rgw_client.cc:513-624@v19.2.6`); the OLH entry and log entry (`src/cls/rgw/cls_rgw_types.h:513-593@v19.2.6`);
`encode_obj_versioned_data_key`, `encode_olh_data_key`, `get_list_index_key`, `decreasing_str`,
`cls_rgw_after_versions`, `decode_list_index_key` (`src/cls/rgw/cls_rgw.cc:272-353@v19.2.6`, `src/cls/rgw/cls_rgw.cc:417-462@v19.2.6`).
Seams: `LinkOLH(op Execer, LinkOLHOp, rel)`, `UnlinkInstance`, `ReadOLHLog(op *ReadOp, …)
*OLHLogResult`, `TrimOLHLog`, `ClearOLH`, `BIGet(op *ReadOp, key ObjKey, typ BIIndexType)
*BIGetResult`, and the pure key functions. Specs: ceph-dencoder goldens, byte-literal key specs.
Deps: none. PR: one. ~200 lines.

**V2 — `meta`: OLH head types.** Layer: meta. `AttrOLHPendingPrefix`; `OLHInfo` v1 and
`OLHPendingInfo` v1 encode/decode (`src/rgw/driver/rados/rgw_rados.h:94-138@v19.2.6`); `VersioningEnabled`,
`Versioned` helpers on `BucketInfo` (`src/rgw/rgw_common.h:1074-1079@v19.2.6`), lifting
`driver.versioningEnabled`; the OLH pending op-tag format and the random instance generator
(`src/rgw/driver/rados/rgw_rados.cc:7681-7690@v19.2.6`, `src/rgw/driver/rados/rgw_rados.cc:8672-8681@v19.2.6`) as `meta` functions. Specs: goldens; format
specs (16 hex + 16 alnum; 32 alnum without `_`). Deps: none. PR: one. ~100 lines.

**V3 — emulator: table dispatch, then versioned keys and the OLH methods.** Layer: emulator.
PR 1: `RGWClass`'s `switch` becomes a table of `method → func` built from per-file
registrations, so later tasks add a file, not a case (PP-5). PR 2: new `cls_rgw_olh.go`
(link/unlink/read/trim/clear per §2.2 incl. ECANCELED/ENOENT/EINVAL paths and the Squid class's
lack of a guard), `cls_rgw_bi.go` (`bi_get`), and `cls_rgw.go`'s prepare/complete/suggest/list
accepting instance keys (instance and list entries, version markers skipped, the complete's flag
reset, `encode_list_index_key`'s marker rule, advance-and-retry). `Cluster.Entry` gains the
instance and OLH entry readers specs use. Specs: §5's emulator list. Deps: V1, V2 (PR 2). ~250
lines (two briefs if the table refactor is sized out separately).

**V4 — driver: the OLH head operations.** Layer: driver (`internal/driver/olh_head.go`).
`olhInitModification` (`src/rgw/driver/rados/rgw_rados.cc:7607-7718@v19.2.6`, `src/rgw/driver/rados/rgw_rados.cc:8462@v20.2.4`),
`olhCancelModification` (`src/rgw/driver/rados/rgw_rados.cc:7562-7605@v19.2.6`), `checkPendingOLHEntries` with
`rgw_olh_pending_timeout_sec` from `Env.Conf` (`src/rgw/driver/rados/rgw_rados.cc:8708-8738@v19.2.6`), `removeOLHPendingEntries`
(`src/rgw/driver/rados/rgw_rados.cc:8740-8771@v19.2.6`), `followOLH` (`src/rgw/driver/rados/rgw_rados.cc:8773-8825@v19.2.6`; sets the state's delete-marker flag as v20.2.4
`src/rgw/driver/rados/rgw_rados.cc:9760-9764@v20.2.4`), `applyOLHLog`'s head op and replay rules (`src/rgw/driver/rados/rgw_rados.cc:8213-8370@v20.2.4`) with the decision: the
`olh.ver` guard is string-mode EQ on the bytes read (§2.9; a `docs/cgo-limitations.md` entry
"librados' C API has no u64 cmpxattr"), `clearOLH`'s head op (`src/rgw/driver/rados/rgw_rados.cc:8387-8428@v19.2.6`), `repairOLH`'s head
write (`src/rgw/driver/rados/rgw_rados.cc:8079-8128@v19.2.6`). The shard calls these need are injected as a small interface so V4's specs
run on the emulator with V3's methods. Specs: §5's driver list for the head ops; the EQ guard
under a concurrent newer apply. Deps: V1, V2, V3. PR: one. ~250 lines.

**V5 — driver: the OLH index operations and `setOLH`/`unlinkObjInstance`.** Layer: driver
(`olh_index.go`, `indexop.go`). A guarded shard write for an arbitrary class call
(`indexOp.guardedWrite(ctx, func(w *WriteOp))`, reusing `guardReshard`,
`src/rgw/driver/rados/rgw_rados.cc:7934-7980@v19.2.6`, `src/rgw/driver/rados/rgw_rados.cc:7988-8027@v19.2.6`, `src/rgw/driver/rados/rgw_rados.cc:8130-8199@v19.2.6`); `readOLHLog` as an unguarded
read on the current shard (`src/rgw/driver/rados/rgw_rados.cc:8029-8073@v19.2.6`); `bucketIndexClearOLH`; `updateOLH`'s loop
(`src/rgw/driver/rados/rgw_rados.cc:8443-8471@v19.2.6`); `setOLH` and `unlinkObjInstance` with the 100-try ECANCELED loop,
`repair_olh`, the best-effort update on other errors and the skip flag (`src/rgw/driver/rados/rgw_rados.cc:8473-8670@v19.2.6`); and
`indexOp.completeSync`, the awaited ADD completion (§2.3's decision), with a spec that the
versioned write never issues the link before the completion's reply. Specs: §5's driver list for
the sequence and races. Deps: V4. PR: one. ~250 lines.

**V6 — driver: reads follow the OLH; attrs on a version.** Layer: driver (`stat.go`, `read.go`,
`attrs.go`). `StatObject`/`PrefetchObject` follow when no instance is named
(`get_obj_state_impl`, `src/rgw/driver/rados/rgw_rados.cc:6120-6297@v19.2.6`): the refusal at `internal/driver/stat.go:148-152`
goes; `ObjectState` gains `Instance` (the resolved version) and `DeleteMarker` (the current is
a marker, from `followOLH`); `versionId=null` on an OLH head without a manifest is NoSuchKey
(`src/rgw/driver/rados/rgw_rados.cc:6289-6293@v19.2.6`); a missing current (marker) is NoSuchKey with `DeleteMarker` set.
`SetObjectAttrs` on the resolved instance (`set_attrs`, `src/rgw/driver/rados/rgw_rados.cc:6593-6757@v19.2.6`): the instance key's
prepare/complete, the `null` clearing kept; refusals removed. Specs: follow, pending-entry
reconciliation on read, null version present/absent, attrs on a noncurrent version. Deps: V5.
PR: one. ~200 lines. Owner of `op/object.go`'s `ObjectState` edits.

**V7 — driver: versioned PutObject, CopyObject, multipart completion.** Layer: driver
(`put.go`, `headwrite.go`, `copy.go`, `mp_complete.go`). The key carries the instance the op
chose; `versionedOp` per `_do_write_meta` (`src/rgw/driver/rados/rgw_rados.cc:3280-3287@v19.2.6`, `src/rgw/driver/rados/rgw_rados.cc:3433-3439@v20.2.4`): the
VERSIONED_OP bilog flag, the id-tag guard rule (`src/rgw/driver/rados/rgw_rados.cc:3298-3300@v20.2.4`), conditions evaluated on
the current version's state (`src/rgw/driver/rados/rgw_rados.cc:3282-3297@v20.2.4`) on both releases, the OLH head's attrs kept
on reset (already), the awaited complete, then `setOLH(obj, false, nil, 0, …)`
(`src/rgw/driver/rados/rgw_rados.cc:3337-3343@v19.2.6`); the null version over an OLH head; copy and completion through the same
path; `mp_complete.go`'s `attrMPCompletionInstance` becomes the instance. Refusals at
`internal/driver/mp_complete.go:269-271` removed. Specs: new key, overwrite on an enabled bucket, suspended
null version, copy with a source version, completion, the Squid/Tentacle condition table on
versions, the ordering spec. Deps: V5, V6. PR: one (put) + one (copy, multipart) if the brief
exceeds 250 lines. ~250 lines.

**V8 — driver: versioned DeleteObject; the admin removals' driver half.** Layer: driver
(`delete.go`, `purge.go`, `bucketadmin.go`). `ObjectStore.DeleteObject` returns
`(*DeleteResult{VersionID string; DeleteMarker bool}, error)`; `DeleteParams` gains `Owner`
(marker owner, the requester's `ACLOwner`) and `SkipOLHUpdate`; the versioned branch
(`src/rgw/driver/rados/rgw_rados.cc:5757-5843@v19.2.6`, `src/rgw/driver/rados/rgw_rados.cc:6445-6577@v20.2.4`): marker key random or null, meta,
`setOLH(delete_marker=true)`; instance path: `bi_get` of the instance entry (ENOENT →
NoSuchKey), Tentacle's conditions against the dirent (`src/rgw/driver/rados/rgw_rados.cc:6520-6554@v20.2.4`) and, for the marker
path, against the current version (`src/rgw/driver/rados/rgw_rados.cc:6494-6509@v20.2.4`), `unlinkObjInstance`; `apply_olh_log`'s instance
deletion reuses the unversioned path with the guard; the purge and admin object removal lose
`refuseVersionedPurge`/the bucket-flag refusal and list with versions. Specs: both paths, null
instance, missing instance, marker on marker, conditions, the admin paths. Deps: V5, V1. PR:
one (delete) + one (admin removals). ~250 lines. Owner of the `ObjectStore.DeleteObject`
signature change (memstore, `opfakes`, `driverfakes` regenerated here).

**V9 — driver: ListObjects with versions.** Layer: driver (`list.go`) and `op/bucket.go`.
`ListObjectsParams.MarkerInstance`, `ListObjectsResult.NextMarkerInstance` (the last entry's
instance, `src/rgw/driver/rados/rgw_rados.cc:1998-2001@v19.2.6`); the class resolves the marker; the refusal at
`op/listobjects.go:121-123` goes; advance-and-retry: on `RGWBIAdvanceAndRetryError` the shard is
listed again from the reply's marker (`src/cls/rgw/cls_rgw_client.cc:99-110@v19.2.6`, `src/cls/rgw/cls_rgw_client.cc:161-166@v19.2.6`;
`src/rgw/services/svc_bi_rados.cc:948-965@v20.2.4`). Specs: markers with and without instances, `null`, a stale
instance, eight reads of markers. Deps: V0, V3. PR: one. ~180 lines.

**V10 — op + s3: PutBucketVersioning, GetBucketVersioning.** Layer: op (`bucketversioning.go`)
and s3 (`bucketversioning.go`). The parse table, flags, the object-lock refusal (lock-lifecycle's
interface), MFA rules reading `Request.MFAVerified` (false until V13 lands), `PutBucketInfo`
under `RetryRacedBucketWrite`; the three documents. Behaviour: `src/rgw/rgw_op.cc:2729-2860@v19.2.6`,
`src/rgw/rgw_op.cc:2979-3092@v20.2.4`; `src/rgw/rgw_rest_s3.cc:2152-2261@v19.2.6`. Specs: §5's op list. Deps: none
(V13 for the MFA bit). PR: one. Frees `enable_versioning` in the admin deselect list and
re-records the admin ledgers `[cluster]`. ~200 lines.

**V11 — op + s3: versioned writes.** Layer: op and s3 (`putobject.go`, `copyobject.go`,
`completemultipart.go`, `s3/*`). `op.VersionForWrite(r)`: the random instance when
`VersioningEnabled`, else empty, ignoring a `versionId` on PUT and copy destination (Q3);
POST object (bucketconfig) calls it. Headers: `x-amz-version-id` on PUT/complete, `null` on a
suspended bucket (Q5), CopyObject's two headers (Q4); `newHeadVersioningUnserved` removed. Specs:
§5. Deps: V7. PR: one. ~180 lines.

**V12 — op + s3: versioned deletes and the admin removals.** Layer: op and s3 (`deleteobject.go`,
`deleteobjects.go`, `adminbucket.go`, `adminpurge.go`, `s3/deleteobject.go`,
`s3/deleteobjects.go`). `DeleteObject` consumes `DeleteResult`; the lock hook
(`Env.ObjectLock.Verify(ctx, r, attrs, bypass)`, lock-lifecycle's; called when an instance is
named and the bucket has object lock, with the instance's state) sits between the state read and
the delete; MFA through `Request.MFAVerified` (the v19.2.6 rule for DeleteObjects on both
releases); the per-name grouping with `SkipOLHUpdate`; headers and `Deleted` fields;
`versioningUnserved` removed; `RemoveObjectAdmin` and the purges call the versioned delete.
Specs: §5. Deps: V8, V13. PR: one (S3 deletes) + one (admin). ~220 lines.

**V13 — MFA: `cls/otp`, the verifier, the header.** Layer: cls (`internal/cls/otp`: types
`OTPInfo`, `OTPCheck`, ops `Check` write-exec and `GetResult` read-exec sending the check op,
§2.8), emulator (`fakerados/cls_otp.go`: the device repo, `otp_check` with RFC 6238 TOTP over
the stored seed, the five-attempt window, `otp_get_result` by token), driver
(`VerifyMFA(ctx, user *meta.UserInfo, serial, pin) (bool, error)`: `mfa_ids`, the otp object
`user:<uid>` in `OTPPool`, the two calls, SUCCESS only), op (`Env.MFA` interface,
`Request.MFAVerified`), s3 (`PostAuthInit` reads `x-amz-mfa` after the name checks and never
fails on it, `src/rgw/rgw_rest_s3.cc:4996-5001@v19.2.6`). Specs: parse (two fields), unknown serial,
wrong/reused/rate-limited code, verified flag; emulator TOTP vectors (RFC 6238 Appendix B).
Deps: none. PR: one (cls + emulator) + one (driver + op + s3). ~250 lines. Hot file shared with
writepath's admin otp: V13 owns the package skeleton and the types; writepath adds `otp_set`,
`otp_get`, `otp_remove` in their own files.

**V14 — op + s3: versioned reads and ListObjectVersions.** Layer: op and s3. GET/HEAD/attrs/ACL/
tagging on a version and the current version (`ObjectState.Instance` into `x-amz-version-id`,
`null` for the empty instance of a versioned bucket, Q5); the 404 of a current delete marker
with `x-amz-delete-marker: true` on both releases (defect 7); a marker's own instance → 405
MethodNotAllowed + `true` after `bi_get` (Q2); `deletemarker.go` reworked onto
`ObjectState.DeleteMarker`; ListObjectVersions' `version-id-marker` into `MarkerInstance`,
`NextVersionIdMarker` from `NextMarkerInstance`; `versioningUnserved` removed from the reads.
Specs: §5. Deps: V6, V9. PR: one (reads) + one (listing). ~220 lines.

**V15 — gate `[cluster]`.** Layer: tests and docs. The `versioning` and `delete_marker` markers
out of `EXCLUDED_MARKERS`; the two deselect lines deleted and
`test_object_lock_put_obj_lock_enable_after_create` added under object lock unless lock has
landed; both s3-tests ledgers re-recorded; `known-differences-squid.txt` gains defect 3's tests
as the recording shows; §4's integration specs A–I; exclusions retired/amended (§3's list) and
the new ones added (Q1–Q5, the EQ guard, the awaited complete); registry entries for defects 3,
5, 6, 7, 8, 12, 13 (public by default, PP-4). Deps: all. PR: one per independent change
(harness, specs, docs). ~200 lines.

**Order.** Wave 1: V0, V1, V2, V13 (cls+emulator half), V10 (without MFA). Wave 2: V3, V13
(driver+op half). Wave 3: V4, V9. Wave 4: V5. Wave 5: V6, V7, V8 (V6 first for `ObjectState`,
then V8's signature change; V7 beside). Wave 6: V11, V12, V14. Wave 7: V15. Three implementers at
once as in phase 1.

**Hot files (PP-5).**

| file | tasks | resolution |
|---|---|---|
| `internal/testutil/fakerados/cls_rgw.go` | V3, V13, lock-lifecycle's and writepath's emulators | V3's PR 1 makes dispatch a table of per-file registrations; every later task adds a file |
| `internal/cls/rgw/const.go` | V1 only (five names) | V1 owns; others add constants in their own files |
| `internal/op/object.go` | V6 (`ObjectState`), V8 (`DeleteObject` signature, `DeleteParams`), lock-lifecycle (`PutParams`?) | V6 before V8; lock-lifecycle adds fields in a later wave; fakes regenerated by the task that changes the interface |
| `internal/op/bucket.go` | V9 only | V9 owns |
| `internal/op/request.go`, `env.go` | V13 (`MFAVerified`, `Env.MFA`), lock-lifecycle (`Env.ObjectLock`) | one-line additions; V13 first |
| `internal/cls/otp` | V13, writepath (admin otp) | V13 owns the package and types; writepath adds its ops in new files |
| `hack/s3tests/run.sh` `EXCLUDED_MARKERS`, `deselect-phase2.txt` | V15, lock-lifecycle's gate, bucketconfig's gate | one gate task per area edits its own lines; the plan orders the three gates |
| `docs/exclusions.md`, `docs/ceph-upstream-bugs.md` | every task | the process review's structural fix (per-entry files or append regions); until then each task's PR edits only its own entries |
| `internal/s3/handler.go` routes | none | per-op `Register`; no hot file |

**Interfaces lock-lifecycle and bucketconfig need from these tasks.**

- `op.ObjectStore.StatObject`/`PrefetchObject` follow the OLH and return `ObjectState{Instance,
  DeleteMarker}` (V6): GetObjectRetention/LegalHold and `?layout` read a version through it.
- `op.ObjectStore.DeleteObject(ctx, rec, key, DeleteParams) (*DeleteResult, error)` with
  `key.Instance` selecting a version or marker and `DeleteParams{Owner, Mtime, SkipOLHUpdate,
  …}` (V8): lifecycle's noncurrent expiration and delete-marker expiry.
- `op.ListObjectsParams{ListVersions, MarkerInstance}` and `ObjectEntry{IsLatest,
  DeleteMarker, Mtime, Key.Instance}` (V9): lifecycle's version walks.
- `op.VersionForWrite(r) string` (V11): POST object's instance.
- `Env.ObjectLock.Verify(ctx, r, attrs, bypass) error` is lock-lifecycle's to implement; V12
  names the call site and its inputs.
- PutBucketVersioning's object-lock refusal (V10) and `meta.BucketInfo.VersioningEnabled` (V2):
  PutObjectLockConfiguration's "versioning must be enabled" check.

## 8. Open questions

Each with the recommended answer and its reason; all are the owner's or the spec's to settle.

- **Q1. Make conditional writes on versioned buckets atomic?** radosgw's check-then-act lets two
  `If-None-Match: *` writers both succeed (§2.9). Recommend: yes, guard `olhInitModification`
  with `cmpxattr(olh.info, EQ, bytes read)` and "no pending attrs", re-evaluating on
  `-ECANCELED`; it writes the same state, radosgw's writers are visible to it through their
  pending attrs, and it is the owner's "prevent what radosgw allows" applied to a correctness
  race. Cost: an extra retry under contention; recorded as an exclusion.
- **Q2. GET/HEAD of a delete marker's own version id: 405 + `x-amz-delete-marker: true` (S3)
  or radosgw's 404?** Recommend 405, at the cost of one `bi_get` on that 404 path only; the
  header is true S3 and the status is what AWS SDKs expect.
- **Q3. `versionId` on PutObject and CopyObject's destination: ignore it (S3 has no such
  parameter) or honour it as radosgw does on non-enabled buckets?** Recommend ignore; it removes
  the registry's null-instance anomaly and the current 501; the exclusion replaces the registry
  entry's rgw-go line.
- **Q4. CopyObject version headers: send `x-amz-version-id` and
  `x-amz-copy-source-version-id`?** Recommend yes (S3); radosgw sends neither; exclusion.
- **Q5. `x-amz-version-id: null` where radosgw omits the header (suspended-bucket PUT and
  completion; GET of a null current version)?** Recommend yes when the bucket is versioned and
  the instance is empty, never on a never-versioned bucket (S3's rule); exclusion.
- **Q6. The awaited ADD completion on versioned writes (one more round trip on the latency
  path) versus an ordered per-shard submission queue that keeps the fire-and-forget.** Recommend
  await for phase 2 and measure in the gateway comparison; a queue is a later optimization.
- **Q7. `x-amz-delete-marker` on the 404 of a current delete marker on Squid too?** Recommend
  yes: Tentacle feature level (spec §3), and S3 sends it; exclusion on Squid.
- **Q8. The `list-type=2&versions` document: keep radosgw's malformed rendering (phase 1) or
  render a correct document?** Recommend keep: no S3 client sends the form, the registry entry
  explains it, and a flip costs more than it buys; revisit if a client appears.
- **Q9. `docs/exclusions.md` says the `bi_*` methods are out on both sides; the S3 versioned
  delete needs `bi_get` of one instance entry (§2.4), and Q2 adds a second use.** Recommend amend
  the entry (shared with rgw-rs: tell the rgw-rs session) to "out except `bi_get` of an instance
  entry on the versioned delete"; `bi_put`/`bi_list` and the repair paths stay out.
- **Q10. Upstream the missing u64-mode cmpxattr in librados' C API?** Recommend not now: rgw-go's
  EQ-on-read guard is correct and compatible; file a feature request only if a second consumer
  appears. Record in `docs/cgo-limitations.md`.
- **Q11. Fable or Opus for V4/V5.** The brief chose Fable for this research because a wrong OLH
  fact corrupts shared data; the same holds for the two driver tasks that implement the protocol
  (V4, V5) and for V7's ordering decision. Recommend Fable reviews (not implements) V4, V5 and V7,
  within the one-at-a-time budget; Opus implements.
