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
([rgw_obj_types.h:226-254](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_obj_types.h#L226-L254), same text at v20.2.4). Its locator is the
name when the name starts with `_` and the namespace is empty, whatever the instance
([rgw_obj_types.h:208-220](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_obj_types.h#L208-L220)); rgw-go's `meta.ObjKey.OID` and `Locator`
already implement both (`internal/meta/objkey.go`). The instance is 32 random
alphanumerics without underscore ([rgw_rados.cc:8672-8681](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8672-L8681),
[rgw_rados.cc:9614-9623](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9614-L9623)). The index key of any object is `(get_index_key_name, instance)`
([rgw_obj_types.h:190-206](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_obj_types.h#L190-L206)).

**The OLH.** The object logical head is the plain key's head object. It can be colocated with
the null version's data: a suspended-bucket write and a pre-versioning object both live at the
plain oid. Its xattrs ([rgw_common.h:131-136](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.h#L131-L136), [rgw_common.h:150-155](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.h#L150-L155)):

| xattr | value | written by |
|---|---|---|
| `user.rgw.olh.idtag` | the OLH tag, 32 lower alphanumerics; guards every later OLH op (`cmpxattr EQ`) | `olh_init_modification_impl` when the head has none ([rgw_rados.cc:7650-7670](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L7650-L7670)); `repair_olh` from the index's olh entry ([rgw_rados.cc:8096-8115](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8096-L8115)) |
| `user.rgw.olh.ver` | the last applied log epoch as a decimal string; empty at creation | init writes it empty ([rgw_rados.cc:7672-7673](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L7672-L7673)); `apply_olh_log` sets `to_string(last_ver)` guarded by `cmpxattr(ver, GTE, last_ver)` in u64 mode ([rgw_rados.cc:8238-8244](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8238-L8244)) |
| `user.rgw.olh.info` | `RGWOLHInfo` v1: `rgw_obj target`, `bool removed` ([rgw_rados.h:94-116](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.h#L94-L116), [rgw_rados.h:96-118](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.h#L96-L118)) | `apply_olh_log` when a LINK_OLH entry wins ([rgw_rados.cc:8323-8331](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8323-L8331)); the plain-head-becomes-OLH case sets it to itself ([rgw_rados.cc:7636-7648](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L7636-L7648)) |
| `user.rgw.olh.pending.<op_tag>` | `RGWOLHPendingInfo` v1: `real_time time` ([rgw_rados.h:118-138](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.h#L118-L138), [rgw_rados.h:120-140](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.h#L120-L140)); `op_tag` is `%016llx` of the second then random lower alphanumerics to 32 bytes, so names sort by time | `olh_init_modification_impl` before each index link/unlink ([rgw_rados.cc:7676-7695](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L7676-L7695)); removed by `apply_olh_log` per log entry ([rgw_rados.cc:8309-8311](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8309-L8311)), by `olh_cancel_modification` on a failed link ([rgw_rados.cc:7577-7584](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L7577-L7584)), or by expiry after `rgw_olh_pending_timeout_sec` (default 1 h, [rgw.yaml.in:2383-2393](https://github.com/ceph/ceph/blob/v19.2.6/src/common/options/rgw.yaml.in#L2383-L2393), [rgw.yaml.in:2483](https://github.com/ceph/ceph/blob/v20.2.4/src/common/options/rgw.yaml.in#L2483)) in `check_pending_olh_entries` ([rgw_rados.cc:8708-8738](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8708-L8738)) |
| `user.rgw.idtag` | a fresh 32-char object tag set with the OLH tag when the head is created as an OLH ([rgw_rados.cc:7651-7659](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L7651-L7659)); a colocated null version's own write tag otherwise | |

`is_olh(attrs)` is "has `user.rgw.olh.ver`" ([rgw_rados.cc:6086-6090](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6086-L6090),
[rgw_rados.cc:6840-6844](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6840-L6844)); `has_olh_tag` is "has `user.rgw.olh.idtag`" ([rgw_rados.cc:6092-6096](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6092-L6096)). A write over
the OLH head keeps the `user.rgw.olh.` attrs: `prepare_atomic_modification`'s reset is
`remove_rgw_head_obj`, the `obj_remove` class call that keeps that prefix ([rgw_rados.cc:6546-6553](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6546-L6553),
[rgw_rados.cc:7351-7358](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7351-L7358)), and `_do_write_meta` re-sets the OLH tag on an OLH head
([rgw_rados.cc:3191-3193](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L3191-L3193), [rgw_rados.cc:3321](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L3321)). rgw-go's head write already keeps
the prefix (`internal/driver/headwrite.go:206-216`).

**Delete markers** have no head object: only index entries (an instance entry with
`tag = "delete-marker"` and `FLAG_DELETE_MARKER`, a list entry) and the OLH's `info.removed`.

**Bucket index entries** (one shard's omap; `src/cls/rgw/cls_rgw.cc@v19.2.6`, same shapes at
v20.2.4 with the line numbers in §2.2):

| entry | omap key | value |
|---|---|---|
| plain | `<index name>` | `rgw_bucket_dir_entry`; once the key is versioned it is a `FLAG_VER_MARKER` placeholder (`write_version_marker`, [cls_rgw.cc:1568-1579](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L1568-L1579)), which listings skip as invalid ([cls_rgw.cc:604-608](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L604-L608)) |
| instance | `0x80 "1000_" <name> "\0i" <instance>`, plus `"\0d"` for a null-instance delete marker (`encode_obj_versioned_data_key`, [cls_rgw.cc:325-337](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L325-L337)); `encode_obj_index_key` picks this for any non-empty instance ([cls_rgw.cc:339-346](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L339-L346)) | the version's `rgw_bucket_dir_entry` with `FLAG_VER`, `FLAG_CURRENT` when current, `FLAG_DELETE_MARKER`, `versioned_epoch` |
| list | `<name> "\0v" decreasing_str(versioned_epoch) "\0i" <instance>` (`get_list_index_key`, [cls_rgw.cc:302-315](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L302-L315); `decreasing_str`, [cls_rgw.cc:272-290](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L272-L290), pads so a newer epoch sorts first) | the same `rgw_bucket_dir_entry`; what `bucket_list` returns |
| OLH data | `0x80 "1001_" <name>` (`encode_olh_data_key`, [cls_rgw.cc:348-353](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L348-L353)) | `rgw_bucket_olh_entry` v1: `key` (current instance), `delete_marker`, `epoch`, `pending_log` (`map<u64, vector<rgw_bucket_olh_log_entry>>`), `tag`, `exists`, `pending_removal` ([cls_rgw_types.h:556-593](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw_types.h#L556-L593)); log entries v1: `epoch`, `op` (`OLHLogOp` UNKNOWN 0, LINK_OLH 1, UNLINK_OLH 2, REMOVE_INSTANCE 3), `op_tag`, `key`, `delete_marker` ([cls_rgw_types.h:513-554](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw_types.h#L513-L554)) |

Flags: `FLAG_VER` 0x1, `FLAG_CURRENT` 0x2, `FLAG_DELETE_MARKER` 0x4, `FLAG_VER_MARKER` 0x8,
`FLAG_COMMON_PREFIX` 0x8000; `is_current` = not VER, or VER and CURRENT; `is_visible` =
current and not a delete marker; `is_valid` = not VER_MARKER
([cls_rgw_types.h:369-463](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw_types.h#L369-L463), same lines at v20.2.4). The entry encoding is
v8 compat 3 and unchanged between the tags; rgw-go's `rgw.DirEntry` carries every field
(`internal/cls/rgw/types_index.go`). `cls_rgw_after_versions(name)` = name +
`"\1"`, the key past every list entry of a name ([cls_rgw.cc:319-323](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L319-L323)). Special-entry prefixes: 0x80, then
`"0_"` log, `"1000_"` instance, `"1001_"` olh ([cls_rgw.cc:51-66](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L51-L66)); v20.2.4 adds `"2001_"`, the
reshard log the class fills while a shard is `IN_LOGRECORD` ([cls_rgw.cc:51-70](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L51-L70); `write_entry` and
`remove_entry` mirror every write there, [cls_rgw.cc:752-786](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L752-L786)), which rgw-go never writes itself.

**Epochs.** The class assigns the OLH epoch: a request with `olh_epoch` 0 gets the next one,
starting at 2 because 1 is "reserved to converted plain entries"
(`BIOLHEntry::start_modify`, [cls_rgw.cc:1503-1517](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L1503-L1517), [cls_rgw.cc:1630-1644](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1630-L1644));
a nonzero request epoch below the entry's is refused as old. Every S3 request sends 0
(`get_system_versioning_params` returns at once for a non-system request,
[rgw_op.h:2113-2139](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.h#L2113-L2139), and the callers initialize `olh_epoch` to 0), so the
exclusions note "OLH epochs differ by OSD release" concerns only main's time-based epochs and
reply; neither floor returns an epoch.

**A pre-versioning object** becomes the null version on the first versioned write of its key:
`convert_plain_entry_to_versioned` rewrites the plain entry as an instance (and list) entry with
`versioned_epoch` 1 and `FLAG_VER`, CURRENT cleared when the new version is promoted, then
writes the version marker at the plain name ([cls_rgw.cc:1587-1634](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L1587-L1634), [cls_rgw.cc:1715-1762](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1715-L1762)). Its
head stays at the plain oid, which the OLH then shares.

### 2.2 The cls_rgw methods

Registered in [cls_rgw.cc:4699-4703](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L4699-L4703) and [cls_rgw.cc:5116-5120](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L5116-L5120); names in
[cls_rgw_const.h:24-28](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw_const.h#L24-L28). Request structs in `src/cls/rgw/cls_rgw_ops.h`,
unchanged between the tags (the diff touches none of them; v20.2.4 lines are two lower).

| method | flags | request (encode version) | reply | class behaviour at v19.2.6 | errors |
|---|---|---|---|---|---|
| `bucket_link_olh` | RD\|WR | `rgw_cls_link_olh_op` v5 compat 1: key, olh_tag, delete_marker, op_tag, meta, olh_epoch, log_op, bilog_flags, `u64 time_t(unmod_since)`, unmod_since, high_precision_time, zones_trace ([cls_rgw_ops.h:167-232](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw_ops.h#L167-L232), [cls_rgw_ops.h:165-230](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw_ops.h#L165-L230)) | none | `rgw_bucket_link_olh` [cls_rgw.cc:1652-1865](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L1652-L1865), [cls_rgw.cc:1781-2000](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1781-L2000): reads the instance entry (ENOENT allowed only for a delete marker), honours `unmod_since` by returning 0 unchanged, handles the null-instance data/marker pair ([cls_rgw.cc:1712-1734](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L1712-L1734)), reads the olh entry, assigns the epoch, computes promote (newer epoch, or equal epoch and a greater-or-equal instance, [cls_rgw.cc:1764-1766](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L1764-L1766)), demotes the previous current, converts a plain entry when no olh exists, appends LINK_OLH (+REMOVE_INSTANCE) to the pending log, writes the olh entry then the instance and list entries, logs to the bilog only with `log_op` | -EINVAL decode; -ENOENT instance missing (non-marker); **-ECANCELED** olh tag mismatch unless `pending_removal` ([cls_rgw.cc:1768-1777](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L1768-L1777)); v20.2.4 adds the class-side reshard guard first ([cls_rgw.cc:1797-1808](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1797-L1808)) |
| `bucket_unlink_instance` | RD\|WR | `rgw_cls_unlink_instance_op` v3: key, op_tag, olh_epoch, log_op, bilog_flags, olh_tag, zones_trace ([cls_rgw_ops.h:234-276](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw_ops.h#L234-L276), [cls_rgw_ops.h:232-274](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw_ops.h#L232-L274)) | none | [cls_rgw.cc:1867-2018](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L1867-L2018), [cls_rgw.cc:2002-2159](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2002-L2159): converts a plain entry when no olh; old epoch → unlink the list entry and log REMOVE_INSTANCE (marker: nothing); when the instance is the current head, `find_next_key` promotes the next version (LINK_OLH) or logs UNLINK_OLH with `exists=false, pending_removal=true` ([cls_rgw.cc:1936-1968](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L1936-L1968)); a delete marker's instance entry is removed by the class, a data instance's removal is logged for the gateway ([cls_rgw.cc:1970-1979](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L1970-L1979)); list entry removed; olh written | -ENOENT instance entry missing ([cls_rgw.cc:1888-1894](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L1888-L1894)); no olh tag check |
| `bucket_read_olh_log` | RD | `rgw_cls_read_olh_log_op` v1: olh (instance must be empty), ver_marker, olh_tag ([cls_rgw_ops.h:278-303](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw_ops.h#L278-L303)) | `rgw_cls_read_olh_log_ret` v1: `log` (entries with epoch > marker, at most 1000), `is_truncated` ([cls_rgw_ops.h:306-328](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw_ops.h#L306-L328)) | [cls_rgw.cc:2020-2072](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L2020-L2072), [cls_rgw.cc:2161-2213](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2161-L2213) | -EINVAL non-empty instance; **-ECANCELED** tag mismatch, including a missing olh entry whose tag is "" ([cls_rgw.cc:2047-2050](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L2047-L2050)) |
| `bucket_trim_olh_log` | RD\|WR | `rgw_cls_trim_olh_log_op` v1: olh, ver, olh_tag ([cls_rgw_ops.h:330-355](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw_ops.h#L330-L355)) | none | erases log epochs <= ver, writes the entry ([cls_rgw.cc:2074-2124](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L2074-L2124), [cls_rgw.cc:2215-2277](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2215-L2277)) | -EINVAL; -ECANCELED tag mismatch; v20.2.4 guards reshard ([cls_rgw.cc:2257-2266](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2257-L2266)) |
| `bucket_clear_olh` | RD\|WR | `rgw_cls_bucket_clear_olh_op` v1: key, olh_tag ([cls_rgw_ops.h:357-380](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw_ops.h#L357-L380)) | none | removes the olh data entry, then the plain entry only if it is a VER_MARKER ([cls_rgw.cc:2126-2190](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L2126-L2190), [cls_rgw.cc:2279-2355](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2279-L2355)) | -EINVAL; -ECANCELED tag mismatch; v20.2.4 guards reshard ([cls_rgw.cc:2297-2306](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2297-L2306)) |
| `bi_get` | RD | `rgw_cls_bi_get_op`: key, type (Instance 2, OLH 3) | `rgw_cls_bi_get_ret`: one `rgw_cls_bi_entry` | used by `bi_get_instance` ([rgw_rados.cc:9247-9267](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L9247-L9267), [rgw_rados.cc:10192](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L10192)) on the S3 versioned delete (§2.4) and `bi_get_olh` by `repair_olh` | -ENOENT |

Client builders: [cls_rgw_client.cc:513-534](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw_client.cc#L513-L534) (link; `olh_tag` is the
xattr's bytes as a string), [cls_rgw_client.cc:550-566](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw_client.cc#L550-L566) (unlink), [cls_rgw_client.cc:568-577](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw_client.cc#L568-L577) (log read as a read-op exec with a
decoding completion), [cls_rgw_client.cc:597-606](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw_client.cc#L597-L606) (trim), [cls_rgw_client.cc:616-624](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw_client.cc#L616-L624) (clear); at v20.2.4 [cls_rgw_client.cc:377-397](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw_client.cc#L377-L397), [cls_rgw_client.cc:414-429](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw_client.cc#L414-L429),
[cls_rgw_client.cc:432-440](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw_client.cc#L432-L440), [cls_rgw_client.cc:461-469](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw_client.cc#L461-L469), [cls_rgw_client.cc:480-487](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw_client.cc#L480-L487). Every write among them is preceded on the gateway by
`assert_exists` and `cls_rgw_guard_bucket_resharding(-ERR_BUSY_RESHARDING)` inside
`guard_reshard` (link [rgw_rados.cc:7957-7969](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L7957-L7969), unlink [rgw_rados.cc:8011-8020](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8011-L8020), trim [rgw_rados.cc:8153-8160](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8153-L8160),
clear [rgw_rados.cc:8184-8192](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8184-L8192); v20.2.4 [rgw_rados.cc:8876-9141](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L8876-L9141) same shape), exactly as phase 1's prepare and complete;
the log read is not guarded ([rgw_rados.cc:8041-8060](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8041-L8060)). `bucket_list` and `bucket_complete_op` are phase 1's;
their versioned behaviour: `bucket_list` skips VER_MARKER entries and, without `list_versions`,
skips a non-visible entry or one named like the start key to `cls_rgw_after_versions(name)`
([cls_rgw.cc:604-623](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L604-L623), [cls_rgw.cc:654-673](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L654-L673)), and answers `RGWBIAdvanceAndRetryError` (-EFBIG) when
eight omap reads listed nothing ([cls_rgw.cc:688-691](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L688-L691)); `complete_op` resets the entry's flags to `FLAG_VER`
and writes the one key `read_key_entry` resolved, which for an instance key is the instance
entry only ([cls_rgw.cc:1061-1064](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L1061-L1064), [cls_rgw.cc:1168-1197](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L1168-L1197)).

### 2.3 Versioned PUT (and copy, multipart completion)

**Instance assignment (op layer).** `RGWPutObj::execute`: when `versioning_enabled()` (versioned
and not suspended) the object gets the system `version_id` or a random instance
([rgw_op.cc:4278-4285](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L4278-L4285), [rgw_op.cc:4495](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4495)); an append write to a versioned bucket
is `-ERR_INVALID_BUCKET_STATE` ([rgw_op.cc:4269-4272](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L4269-L4272)). `RGWCopyObj::execute` does the same for the
destination ([rgw_op.cc:5573-5577](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5573-L5577), [rgw_op.cc:6142](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L6142)); the source is the object the request named,
with its `?versionId=`, resolved by the read. `RGWCompleteMultipart::execute` likewise
([rgw_op.cc:6459-6466](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L6459-L6466), [rgw_op.cc:7288](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7288)). On a never-versioned or suspended bucket the key keeps
whatever instance the URL carried (`versionId` is read into the key for every object request,
[rgw_rest_s3.cc:4946-4955](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L4946-L4955), [rgw_rest_s3.cc:5506-5515](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5506-L5515)): that is the registry's
"PutObject naming versionId=null on an unversioned bucket" (§6).

**The write (driver).** `_do_write_meta` ([rgw_rados.cc:3124-3417](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L3124-L3417), [rgw_rados.cc:3234](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L3234)),
with the head at the instance oid:

1. `versioned_target = (olh_epoch set && > 0) || instance non-empty`; `versioned_op =
   versioning_enabled || is_olh || versioned_target`; a versioned op sets
   `RGW_BILOG_FLAG_VERSIONED_OP` on the index op ([rgw_rados.cc:3280-3287](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L3280-L3287), [rgw_rados.cc:3433-3439](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L3433-L3439)).
2. index `prepare` ADD on the instance key, behind the reshard guard ([rgw_rados.cc:3289-3295](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L3289-L3295)); the class
   files a pending entry under `0x80 1000_ name \0i instance` (§2.1).
3. the head write op: on an OLH head, `setxattr(olh.idtag)` again ([rgw_rados.cc:3191-3193](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L3191-L3193)); `mtime2`,
   `write_full`, attrs, manifest, `pg_ver`, `source_zone`, `storage_class`; `prepare_atomic_modification`
   adds the id-tag guard and, for an existing head, `create(false)` + `remove_rgw_head_obj`
   ([rgw_rados.cc:6484-6576](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6484-L6576)).
4. `complete_atomic_modification` (the overwritten head's tails to the GC) and the index
   `complete` ADD, which radosgw sends without waiting ([rgw_rados.cc:3314-3324](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L3314-L3324)); for an instance key the
   class writes the instance entry only (§2.2).
5. `versioned_op && olh_epoch set` → `set_olh(obj, delete_marker=false, meta=NULL, epoch=0,
   unmod_since=0, high_precision=false, ..., add_log)` ([rgw_rados.cc:3337-3343](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L3337-L3343),
   [rgw_rados.cc:3492](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L3492)): §2.9 gives
   `set_olh`'s sequence; the net effect is the OLH head gains a pending attr, the shard links the
   instance as current (new epoch, list entry, previous current demoted, plain entry converted on
   the first version), and `update_olh` replays the log onto the head (OLH_INFO → this instance,
   OLH_VER → the epoch), removes the pending attr and trims the log.
6. a failed head write cancels the index op as in phase 1 ([rgw_rados.cc:3369-3416](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L3369-L3416)); a lost race answers
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
(`get_current_version_state`, [rgw_rados.cc:7238-7258](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7238-L7258); `check_preconditions`
[rgw_rados.cc:7260-7329](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7260-L7329); the call [rgw_rados.cc:3282-3297](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L3282-L3297)), and sets the id-tag guard only for an instance-less write
(`set_attr_id_tag`, [rgw_rados.cc:3298-3300](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L3298-L3300)). v19.2.6 evaluates both against the object being written
(`prepare_atomic_modification`, [rgw_rados.cc:6493-6543](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6493-L6543)), which for a versioning-enabled bucket is
the new random instance, never existing: If-Match (any value) always answers 412 and
If-None-Match `*` never fails (§6, defect 3). rgw-go follows v20.2.4's evaluation on both
releases, as phase 1 already does for the unversioned case (owner decision 1, PP-3), and its
checks are not atomic with the link on either release (§2.9).

**Copy** goes through `copy_obj` with `olh_epoch` and the destination version
([rgw_op.cc:5655-5676](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5655-L5676); [rgw_sal_rados.cc:2686-2716](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L2686-L2716) sets
`obj_op.meta.olh_epoch`), so the driver sequence is the write above with the shared-tail or
streamed data of phase 1's copy; the source read follows the OLH or names an instance (§2.5).
**Multipart completion** sets `obj_op.meta.olh_epoch` on the assembled head's write
([rgw_sal_rados.cc:3632](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L3632)) and otherwise runs P's completion; rgw-go's
`mp_complete.go` already carries an `attrMPCompletionInstance` seam for the instance
(`internal/driver/mp_complete.go:351`, `internal/driver/mp_complete.go:397`).

**Response headers.** PutObject and CompleteMultipartUpload send `x-amz-version-id` when the
version id is non-empty ([rgw_rest_s3.cc:2748](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L2748), [rgw_rest_s3.cc:2754](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L2754), [rgw_rest_s3.cc:4081](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L4081); [rgw_rest_s3.cc:2891](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2891),
[rgw_rest_s3.cc:4604](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4604)): the instance on an enabled bucket, nothing on a suspended one, where S3 sends
`null` (§6, defect 8). CopyObject's responses carry no version header at all
([rgw_rest_s3.cc:3566-3603](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L3566-L3603), [rgw_rest_s3.cc:3873](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L3873)), where S3 sends `x-amz-version-id` and
`x-amz-copy-source-version-id` (§6, defect 5).

### 2.4 Versioned DELETE and delete markers

**DeleteObject, op layer** (`src/rgw/rgw_op.cc@v19.2.6`; v20.2.4 in brackets):

- `verify_permission` ([rgw_op.cc:5138-5166](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5138-L5166) [[rgw_op.cc:5528-5556](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L5528-L5556)]): the action is `s3:DeleteObjectVersion`
  when the key names an instance, else `s3:DeleteObject` ([rgw_op.cc:5144-5151](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5144-L5151)); then the
  bypass-governance permission when the bucket has object lock ([rgw_op.cc:5153-5156](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5153-L5156), lock-lifecycle's);
  then MFA: `mfa_enabled() && instance non-empty && !mfa_verified` → `-ERR_MFA_REQUIRED`, 403
  AccessDenied ([rgw_op.cc:5158-5163](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5158-L5163)). rgw-go's `DeleteObject.VerifyPermission` already has this shape
  with the MFA refusal unconditional (`internal/op/deleteobject.go:106-116`).
- `execute` ([rgw_op.cc:5173-5327](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5173-L5327) [[rgw_op.cc:5563](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L5563)]): `check_obj_lock = have_instance && obj_lock_enabled`
  ([rgw_op.cc:5186](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5186)), `null_verid = instance == "null"` ([rgw_op.cc:5187](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5187)); the state is loaded following the OLH
  ([rgw_op.cc:5189](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5189)); a missing object drops the lock check ([rgw_op.cc:5195-5203](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5195-L5203)); `verify_object_lock` on the
  version's attrs is lock-lifecycle's seam ([rgw_op.cc:5211-5221](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5211-L5221)); the delete op gets
  `versioning_status`, `unmod_since`, `olh_epoch` (0), `marker_version_id` (system only),
  `null_verid` ([rgw_op.cc:5278-5288](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5278-L5288)); `-ECANCELED` → 0 ([rgw_op.cc:5302-5304](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5302-L5304)); the result's `delete_marker` and
  `version_id` feed the response.
- Response ([rgw_rest_s3.cc:3455-3470](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L3455-L3470) [[rgw_rest_s3.cc:3735](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L3735)]): `-ENOENT` → 204; 204;
  `x-amz-version-id` when non-empty; `x-amz-delete-marker: true` when `delete_marker`.

**The driver's versioned branch** (`Object::Delete::delete_obj`, [rgw_rados.cc:5757-5843](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5757-L5843)
[[rgw_rados.cc:6445-6577](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6445-L6577)]), taken when `versioning_status & BUCKET_VERSIONED` (enabled or
suspended) or a system marker id is given ([rgw_rados.cc:5774](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5774)); the `null` instance is cleared first
([rgw_rados.cc:5768-5770](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5768-L5770)):

- *No instance: a delete marker.* The marker key's instance is random unless the bucket is
  suspended, where it stays empty, the null marker ([rgw_rados.cc:5777-5787](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5777-L5787)); `result.version_id` is the
  instance or `"null"`, `result.delete_marker` true ([rgw_rados.cc:5789-5793](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5789-L5793)); the marker's index meta
  carries the requester as owner and the request time ([rgw_rados.cc:5795-5804](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5795-L5804)); `set_olh(marker,
  delete_marker=true, &meta, olh_epoch, unmod_since, high_precision_time, ...)` ([rgw_rados.cc:5806-5808](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5806-L5808)).
  The class creates the marker's instance entry itself (`init_as_delete_marker`, §2.2) and
  promotes it; `update_olh` sets `OLH_INFO.removed = true`. A marker on top of a marker is
  allowed at both floors (registry: fixed before v19.2.6). v20.2.4 first checks the
  conditional-delete headers against the current version ([rgw_rados.cc:6494-6509](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6494-L6509)).
- *An instance: unlink it.* `bi_get_instance` reads the instance entry ([rgw_rados.cc:5815](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5815); `bi_get`,
  [rgw_rados.cc:9247-9267](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L9247-L9267), `-ENOENT` → the request ends in 204 with no headers); `result.delete_marker =
  dirent.is_delete_marker()`; `unlink_obj_instance(obj, olh_epoch, bilog_flags, null_verid,
  ...)` ([rgw_rados.cc:5820-5823](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5820-L5823)); `result.version_id` = the instance ([rgw_rados.cc:5827](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5827)). v20.2.4 checks If-Match,
  size and last-modified against the dirent's meta before unlinking ([rgw_rados.cc:6520-6554](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6520-L6554)).
  `unlink_obj_instance` ([rgw_rados.cc:8568-8670](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8568-L8670) [[rgw_rados.cc:9510](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9510)]) runs §2.9's sequence with
  `bucket_index_unlink_instance`; its `update_olh` → `apply_olh_log` then deletes the unlinked
  data instance's head through `delete_obj(instance, versioning_status 0, …,
  RGW_BILOG_FLAG_VERSIONED_OP, skip_olh_obj_update=true)` ([rgw_rados.cc:8334-8345](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8334-L8345)), that is phase 1's
  unversioned delete on the instance head: prepare DEL on the instance key, `obj_remove`
  keeping the `olh.` prefix, complete_del, tails to the GC. A delete marker has no head and the
  class removed its instance entry already, so nothing is deleted for it.
- Both then add a datalog entry only when the zone logs data ([rgw_rados.cc:5837-5840](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5837-L5840)), which a single
  zone never does.

**Both floors** register the `bi_get` method the S3 delete uses (§2.2); `docs/exclusions.md`'s
"the `bi_*` index-entry methods … are out on both sides unless an index-repair path in scope
turns out to need them" needs amending: the versioned delete needs `bi_get` of one instance
entry (§8, Q9).

**DeleteObjects** ([rgw_op.cc:7007-7106](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L7007-L7106) [[rgw_op.cc:7921](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7921)]): after the parse and the count
check, the MFA check refuses the whole request with `-ERR_MFA_REQUIRED` when the bucket has MFA
delete, any key names a version and the request did not verify ([rgw_op.cc:7055-7068](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L7055-L7068)); v20.2.4
inverts the per-key test ([rgw_op.cc:7969-7983](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7969-L7983); registry, §6 defect 1). On a versioned bucket
`handle_versioned_objects` groups keys by name and deletes all but the last of each group with
`FLAG_SKIP_UPDATE_OLH`, then the last with the OLH update ([rgw_op.cc:6912-6965](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L6912-L6965);
`handle_objects` chooses it by `bucket->versioned()`, [rgw_op.cc:6996-7005](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L6996-L7005)). Each key runs
`handle_individual_object` ([rgw_op.cc:6818-6910](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L6818-L6910) [[rgw_op.cc:7743](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7743)]): the per-key action and permission, the
state read and lock check as DeleteObject, `versioning_status`, an empty `marker_version_id`,
`-ENOENT` → 0, and the partial response with `result.delete_marker` and `result.version_id`.
The `Deleted` element carries `Key`, `VersionId` when the key named one, and `DeleteMarker` +
`DeleteMarkerVersionId` when a marker was made ([rgw_rest_s3.cc:4273-4301](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L4273-L4301)
[[rgw_rest_s3.cc:4821](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4821)]); rgw-go's `deleteobjects.go` renders these fields already. The skip leaves the
intermediate pending attrs on the OLH head for the last delete's `update_olh` to apply; rgw-go
may delete each key with its own update instead, which yields the same final state (one
reconciliation per key rather than one per name); the plan picks radosgw's grouping to keep the
op count comparable.

**The admin object removal** (`RGWBucket::remove_object`, [rgw_bucket.cc:274-289](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_bucket.cc#L274-L289),
[rgw_bucket.cc:275-290](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_bucket.cc#L275-L290)) deletes the plain key with the bucket's versioning status, so on a versioned
bucket it writes a delete marker: rgw-go's `RemoveObjectAdmin` refusal lifts onto the same
driver delete. The purging removals list with `list_versions = true` and delete each entry by
its key with its instance ([rgw_sal_rados.cc:362-364](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L362-L364), [rgw_sal_rados.cc:500](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L500)).

### 2.5 Versioned GET and HEAD

**State resolution** is `get_obj_state_impl` ([rgw_rados.cc:6120-6297](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6120-L6297),
[rgw_rados.cc:6874](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6874), identical text): `need_follow_olh = follow_olh && instance empty` ([rgw_rados.cc:6129](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6129));
the head's `olh.idtag` is kept as `olh_tag` whether or not it is an OLH ([rgw_rados.cc:6278-6280](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6278-L6280)); a head
with `olh.ver` is an OLH: with no instance named the state becomes the target's
(`get_olh_target_state`, [rgw_rados.cc:6098-6118](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6098-L6118), `follow_olh` §2.9); with `versionId=null` named and no
manifest on the head, the null version does not exist: `exists=false`, `-ENOENT`
([rgw_rados.cc:6289-6293](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6289-L6293)). A named instance reads `_:<instance>_<name>` directly; a delete marker's
instance has no head, so it is `-ENOENT`.

`Object::Read::prepare` ([rgw_rados.cc:6847](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6847), [rgw_rados.cc:7686](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7686))
loads with `follow_olh=true`, answers `-ENOENT` for a missing object ([rgw_rados.cc:6866-6872](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6866-L6872)), and leaves
the resolved key in `state.obj`; `RadosReadOp::prepare` copies the instance back to the request's
object ([rgw_sal_rados.cc:2863](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L2863), [rgw_sal_rados.cc:3678](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L3678)), and
`RGWGetObj::execute` takes `version_id = s->object->get_instance()` from it
([rgw_op.cc:2259-2260](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L2259-L2260)). So a plain GET of a versioned key answers the current
version with its `x-amz-version-id` ([rgw_rest_s3.cc:461](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L461)), and a GET of the
null version answers `x-amz-version-id: null` because the request named it; a plain GET whose
current version is the null version sends no header (§6, defect 8). HEAD is the same op.

**Permissions first.** `read_obj_policy` reads the object's ACL before `execute`
([rgw_op.cc:423-447](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L423-L447), [rgw_op.cc:453-477](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L453-L477)): a missing object, which includes a
current delete marker (the OLH follow returns `-ENOENT`) and a delete marker's instance, answers
`-ENOENT` to an admin or a requester allowed `s3:ListBucket` on the bucket, else `-EACCES`.
v20.2.4's `read_permissions` then adds `x-amz-delete-marker` to that 404 of a GET or HEAD,
`true` when the key's current version is a delete marker: it reloads the state following the
OLH and reads `is_delete_marker()` ([rgw_rest.cc:1936-1944](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L1936-L1944)), which `follow_olh`
sets from `OLH_INFO.removed` ([rgw_rados.cc:9760-9764](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9760-L9764), copied at
[rgw_sal_rados.cc:2942](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L2942)). v19.2.6 sends no such header. A GET or HEAD that names a
delete marker's own instance is a plain 404 NoSuchKey at both tags, with the header `false` at
v20.2.4 (the instance is named, so nothing is followed), where S3 answers 405 MethodNotAllowed
with `x-amz-delete-marker: true` (§6, defect 6).

**Conditions** (`If-Match`, ranges) apply to the resolved state as in phase 1.
GetObjectAttributes, GetObjectAcl and GetObjectTagging resolve the same way (their actions are
the `Version` ones when an instance is named, [rgw_op.cc:1039](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L1039), [rgw_op.cc:1078](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L1078), [rgw_op.cc:1120](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L1120),
[rgw_op.cc:5700](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5700)); the `?versionId=` of a UploadPartCopy and CopyObject source resolves through
the same read.

**Writes of attrs on a version** (PutObjectAcl, Put/DeleteObjectTagging, lock-lifecycle's
retention and legal hold): `modify_obj_attrs` resolves the target through a read that follows
the OLH and writes the instance ([rgw_sal_rados.cc:2401-2419](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L2401-L2419), [rgw_sal_rados.cc:2994](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L2994));
`RGWRados::set_attrs` ([rgw_rados.cc:6593-6757](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6593-L6757), [rgw_rados.cc:7393](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7393)) clears a `null`
instance ([rgw_rados.cc:6601-6603](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6601-L6603)), answers `-ENOENT` for a null version without a manifest ([rgw_rados.cc:6620-6622](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6620-L6622)),
guards on the version head's write tag, draws a fresh write tag, prepares ADD on the instance
key, writes the attrs and the new `idtag` with the kept mtime (plus 1 ns when the zone logs,
[rgw_sal_rados.cc:2384](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L2384)), and completes ADD on the instance key with the state's size
and the attrs' etag, content type and storage class ([rgw_rados.cc:6665-6725](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6665-L6725)). The class's complete resets
the instance entry's flags to `FLAG_VER`, which is radosgw's state too (the list entry keeps
`CURRENT`); rgw-go's `SetObjectAttrs` takes the resolved state and needs only the instance key
and the removal of its refusals.

### 2.6 Enabling and suspending; null versions

**PutBucketVersioning** (`RGWSetBucketVersioning`, [rgw_op.cc:2759-2860](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L2759-L2860),
[rgw_op.cc:2991-3092](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L2991-L3092) by the grep at [rgw_op.cc:2991](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L2991) and [rgw_op.cc:3009](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L3009)):

1. `verify_permission`: bucket tags into the environment when a policy asks, then
   `s3:PutBucketVersioning` ([rgw_op.cc:2759-2770](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L2759-L2770)).
2. `get_params` ([rgw_rest_s3.cc:2204-2261](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L2204-L2261), [rgw_rest_s3.cc:2307](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2307)): the body up to
   `rgw_max_put_param_size`; an unparsable document or no `VersioningConfiguration` →
   `-EINVAL` (400 InvalidArgument); `Status` `Enabled` → enabled, `Suspended` → suspended,
   anything else → invalid → `-EINVAL`; `MfaDelete` `Enabled`/`Disabled` sets `mfa_set_status`
   and `mfa_status`, any other value `-EINVAL` (`ver_config_status::decode_xml`,
   [rgw_rest_s3.cc:2171-2202](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L2171-L2202)).
3. `execute` ([rgw_op.cc:2777-2860](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L2777-L2860)): NoSuchBucket; a bucket with object lock refuses any status
   but Enabled with `-ERR_INVALID_BUCKET_STATE` and the message "bucket versioning cannot be
   disabled on buckets with object lock enabled" ([rgw_op.cc:2788-2793](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L2788-L2793)), lock-lifecycle's interface;
   MFA: `mfa_set_status` is dropped when it equals the current flag ([rgw_op.cc:2797](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L2797)); changing it
   requires a verified request ([rgw_op.cc:2799-2803](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L2799-L2803)); when MFA is already enabled, flipping between
   Enabled and Suspended requires a verified request ([rgw_op.cc:2805-2817](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L2805-L2817)); the forward to the master
   zone is a no-op in a single zone ([rgw_op.cc:2819-2824](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L2819-L2824)); under `retry_raced_bucket_write`, the MFA
   flag is set or cleared, Enabled sets `BUCKET_VERSIONED` and clears
   `BUCKET_VERSIONS_SUSPENDED`, Suspended sets both, and the instance is written with
   `put_info` ([rgw_op.cc:2828-2849](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L2828-L2849)). Nothing in the index changes: versioning is a flag on the bucket
   instance, which phase 1's `PutBucketInfo` writes under the version tracker with the cache
   notify.

**GetBucketVersioning** ([rgw_op.cc:2729-2757](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L2729-L2757), [rgw_op.cc:2979](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L2979)): `s3:GetBucketVersioning`;
NoSuchBucket; the document holds `Status` (Enabled/Suspended) and `MfaDelete`
(Enabled/Disabled) only when the bucket has ever been versioned, else an empty
`VersioningConfiguration` ([rgw_rest_s3.cc:2152-2169](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L2152-L2169), [rgw_rest_s3.cc:2255](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2255)).

**Flags** ([rgw_common.h:1022-1026](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.h#L1022-L1026), [rgw_common.h:1074-1079](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.h#L1074-L1079); [rgw_common.h:1057-1061](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.h#L1057-L1061),
[rgw_common.h:1116-1121](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.h#L1116-L1121)): `versioned()` is `BUCKET_VERSIONED`; `versioning_enabled()` is
versioned and not suspended; suspending keeps `BUCKET_VERSIONED`, so "ever versioned" is what
rgw-go's `neverVersioned` tests today (`internal/op/putobject.go:277-279`).

**Null versions.** Three things carry the `null` version id: a pre-versioning object (converted
on the key's first versioned write, §2.1), a suspended-bucket write (the instance stays empty,
the head is the plain oid, and the OLH links the empty instance, §2.3), and a suspended-bucket
delete marker (the empty instance with the `"\0d"` instance key, §2.1). The class keeps the data
null version and the null delete marker as two instance entries so the two can coexist
(`rgw_bucket_link_olh`, [cls_rgw.cc:1705-1734](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L1705-L1734)). A request's
`versionId=null` is cleared before the OLH ops and the head read ([rgw_rados.cc:5768-5770](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5768-L5770),
[rgw_rados.cc:6601-6603](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6601-L6603); `get_oid` never encodes it), and `null_verid` only adds
`RGW_BILOG_NULL_VERSION` to the bilog flags ([rgw_rados.cc:8618-8622](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8618-L8622)), which the single zone never writes.
rgw-go's `meta.NullInstance` and the attr-path clearing exist (`internal/driver/attrs.go:30-31`,
`internal/driver/delete.go:38-39`).

**Suspending while versions exist** changes no index entry: later writes become null versions
over the OLH head, listings keep showing the old versions, and a delete without an instance
writes the null marker (replacing a null data version's list entry, [cls_rgw.cc:1712-1731](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L1712-L1731)).
**Re-enabling** likewise only flips the flag.

### 2.7 ListObjectVersions

**Parameters** (`RGWListBucket_ObjStore_S3::get_common_params` and `get_params`,
[rgw_rest_s3.cc:1710-1754](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L1710-L1754), [rgw_rest_s3.cc:1821](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L1821), [rgw_rest_s3.cc:1852](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L1852)): `versions` present
selects the versions listing ([rgw_rest_s3.cc:1712](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L1712)); `prefix`, `allow-unordered`, `delimiter`, `max-keys`
through `parse_max_keys`, `encoding-type` are the v1 listing's; the marker is
`(key-marker, version-id-marker)` as an `rgw_obj_key` ([rgw_rest_s3.cc:1750-1751](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L1750-L1751)). The op is `RGWListBucket`
with `list_versions` ([rgw_op.cc:3078-3121](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L3078-L3121), [rgw_op.cc:3312](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L3312)); its action is
`s3:ListBucketVersions`; v20.2.4 refuses an indexless bucket first ([rgw_op.cc:3319-3324](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L3319-L3324)),
which rgw-go already does for both listings. The same `RadosBucket::list` serves it with
`params.list_versions` ([rgw_sal_rados.cc:877-904](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L877-L904),
[rgw_sal_rados.cc:897](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L897)).

**What the class returns.** With `list_versions` the shard returns every valid list entry in
list-key order: current and noncurrent versions, delete markers, and unversioned objects (their
plain entries), newest version first within a name (§2.1's `decreasing_str`), skipping only the
`FLAG_VER_MARKER` placeholders (`rgw_bucket_list`, [cls_rgw.cc:604-608](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L604-L608)).
Without it, non-visible entries are skipped to the name's end ([cls_rgw.cc:611-623](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L611-L623)); the gateway drops
non-visible entries again in `list_objects_ordered` ([rgw_rados.cc:1920-1924](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L1920-L1924),
[rgw_rados.cc:1905](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L1905)) and rgw-go's `visible` filter mirrors it (`internal/driver/list.go:125-127`,
`internal/driver/list.go:225-234`).

**The marker.** The class resolves `(name, instance)` to a list key through
`encode_list_index_key` ([cls_rgw.cc:358-390](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L358-L390), [cls_rgw.cc:408](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L408)): no instance → the name (so the
listing resumes after every version of that name only if the name's plain key sorts before
them; in practice the versions of `name` sort after `name` itself, so a key-marker without a
version-id-marker lists the key's versions again — the same as S3); `null` → the empty
instance; an instance whose instance entry exists → that version's exact list key; one that does
not → `name + "\x01"`, past every version of the name (a stale or wrong `version-id-marker`
skips the rest of that key). The next marker is the last entry visited, name and instance
([rgw_rados.cc:1998-2001](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L1998-L2001); `next_marker = index_key`), which the response renders as
`NextKeyMarker` and `NextVersionIdMarker`, `null` for an empty instance
([rgw_rest_s3.cc:1809-1817](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L1809-L1817)).

**Reconciliation.** `cls_bucket_list_ordered` checks an entry against its head when the entry
does not exist and is neither a delete marker nor a common prefix, or has pending ops
([rgw_rados.cc:9823-9827](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L9823-L9827)); `check_disk_state` reads the key with its instance and never suggests
removing a delete marker ([rgw_rados.cc:10361](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L10361)). rgw-go's `needsDiskCheck` and `checkDiskState`
already carry the instance and the marker test (`internal/driver/list.go:399-405`, `internal/driver/list.go:465`,
`internal/driver/list.go:475`).

**The document** (`send_versioned_response`, [rgw_rest_s3.cc:1799-1869](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L1799-L1869),
[rgw_rest_s3.cc:1910](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L1910)): `ListVersionsResult` with `EncodingType` first when `url`, the common fields,
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
return value ([rgw_rest_s3.cc:4996-5001](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L4996-L5001), [rgw_rest_s3.cc:5556](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5556)): a malformed or
failed MFA never fails the request by itself; the op that needs MFA then answers
`-ERR_MFA_REQUIRED` (2044 → 403 AccessDenied, [rgw_common.cc:96](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L96)). The check
runs for every S3 request carrying the header, whether or not the op needs MFA. rgw-go's
`PostAuthInit` notes the gap (`internal/s3/request.go:163-179`).

**verify_mfa** ([rgw_sal_rados.cc:284-312](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L284-L312), [rgw_sal_rados.cc:301](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L301)): the
header splits as `get_str_vec(mfa_str, " ", params)` does
([str_list.cc:43-49](https://github.com/ceph/ceph/blob/v19.2.6/src/common/str_list.cc#L43-L49), the same at v20.2.4). That keeps what `for_each_substr` finds at
spaces alone, skipping leading spaces and runs of them so that no field is empty, with a tab
part of a field ([str_list.h:15-24](https://github.com/ceph/ceph/blob/v19.2.6/src/include/str_list.h#L15-L24), [str_list.h:14-23](https://github.com/ceph/ceph/blob/v20.2.4/src/include/str_list.h#L14-L23)). It must give exactly two
fields, `serial` and `pin`, else `-EINVAL`; the
serial must be in the user's `mfa_ids` (`RGWUserInfo`, [rgw_common.h:598](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.h#L598),
[rgw_common.h:622](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.h#L622); rgw-go's `meta.UserInfo.MFAIDs`), else `-EACCES`; then `check_mfa`, any failure
`-EACCES`; success sets `verified`.

**check_mfa** ([svc_cls.cc:46-63](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_cls.cc#L46-L63), same at v20.2.4): the otp object is
`user:<uid>` in the zone's `otp_pool` ([svc_cls.h:56-58](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_cls.h#L56-L58); rgw-go's `ZoneParams.OTPPool`,
default `<zone>.rgw.otp`); `OTP::check` is two class calls on it
([cls_otp_client.cc:60-96](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/otp/cls_otp_client.cc#L60-L96)):

1. `otp_check` (RD|WR) with `cls_otp_check_otp_op` v1 `{id = serial, val = pin, token}` where
   `token` is 16 characters from `CryptoRandom` over a 64-character table, `A-Za-z0-9-_`
   ([random_string.cc:33-51](https://github.com/ceph/ceph/blob/v19.2.6/src/common/random_string.cc#L33-L51), the same at v20.2.4); v19.2.6 sends it through `IoCtx::exec`, v20.2.4 through
   an `ObjectWriteOperation` (the only cls_otp change between the tags). The class
   ([cls_otp.cc:447-481](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/otp/cls_otp.cc#L447-L481)) loads the device, records the attempt with the
   result of a TOTP validation against the seed, step, offset and window, unless five attempts
   already fell in the current step window (`ATTEMPTS_PER_WINDOW`, [cls_otp.cc:43](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/otp/cls_otp.cc#L43), [cls_otp.cc:108-127](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/otp/cls_otp.cc#L108-L127)), and
   writes the instance back. The two registry defects live in `verify` ([cls_otp.cc:129-153](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/otp/cls_otp.cc#L129-L153)): a stored
   `step_size` of 0 faults the OSD, and the replay index takes the window distance unsigned.
2. `otp_get_result` (RD) with **the same `cls_otp_check_otp_op` buffer**: the client passes `in`
   rather than the `cls_otp_get_result_op` it encoded ([cls_otp_client.cc:78-83](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/otp/cls_otp_client.cc#L78-L83)), and
   the class decodes a `cls_otp_check_otp_op` and finds the attempt by `op.token`
   ([cls_otp.cc:483-511](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/otp/cls_otp.cc#L483-L511)), answering `cls_otp_get_result_reply{otp_check_t{token, timestamp,
   result}}`, `OTP_CHECK_UNKNOWN` when no attempt carries the token (rate-limited or evicted).
   `cls_otp_get_result_op` is dead on both sides; a reimplementation that sent it would get
   `-EINVAL`. The two-phase design exists because a write method cannot return data
   ([cls_otp.cc:543-553](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/otp/cls_otp.cc#L543-L553)).

`check_mfa` is 0 only for `OTP_CHECK_SUCCESS`, else `-EACCES`. A successful check consumes the
code: `last_success` is the window index, and the same code is refused afterwards. Types:
`otp_info_t` v1 (type u8, id, seed, seed_type u8, seed_bin, time_ofs i32, step_size u32,
window u32), `otp_check_t` v1 (token, timestamp, result u8)
(`src/cls/otp/cls_otp_types.h@v19.2.6`); writepath's admin otp shares them.

**Where MFA is required** (all `rgw_op.cc`): DeleteObject naming a version on a bucket with
`BUCKET_MFA_ENABLED` ([rgw_op.cc:5158-5163](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5158-L5163), [rgw_op.cc:5548-5553](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L5548-L5553)); DeleteObjects when any key
names a version ([rgw_op.cc:7055-7068](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L7055-L7068); v20.2.4's inverted test, [rgw_op.cc:7969-7983](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7969-L7983), is the registry's
security defect, §6); PutBucketVersioning when it changes `MfaDelete`, or flips Enabled/Suspended
while MFA is on ([rgw_op.cc:2795-2817](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L2795-L2817), [rgw_op.cc:3032-3046](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L3032-L3046)). An admin's request is not exempt
at the op level, but `rgw_process` lets an admin through a refused permission before the MFA
test is reached (the exclusions entry "Deletes that need MFA are refused" covers the order).

**rgw-go today** verifies no token and refuses every MFA-requiring delete (exclusions); phase 2
replaces the refusal with the real check. The parity settings keep MFA off, so no s3-test
exercises it; the go-ceph admin suite has no MFA case; the specs are rgw-go's own (§5).

### 2.9 Racing writers: the pending/complete protocol and OLH log

**The sequence** (`set_olh`, [rgw_rados.cc:8473-8566](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8473-L8566), [rgw_rados.cc:9415](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9415);
`unlink_obj_instance`, [rgw_rados.cc:8568-8670](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8568-L8670), [rgw_rados.cc:9510](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9510)), per attempt, up to `MAX_ECANCELED_RETRY`
100 then `-EIO`:

1. Read the OLH head's state without following (`get_obj_state`, [rgw_rados.cc:8498](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8498)), after invalidating
   it when the previous attempt was cancelled.
2. `olh_init_modification` ([rgw_rados.cc:7607-7718](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L7607-L7718), [rgw_rados.cc:8462](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L8462)), one write op on the OLH
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
4. On the shard op's failure: `olh_cancel_modification` ([rgw_rados.cc:7562-7605](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L7562-L7605)) removes the pending attr
   under the tag guard and, when the head has no `olh.info`, removes the head under
   `cmpxattr(olh.info, EQ, "")` + `obj_check_attrs_prefix(olh.pending., fail if exists)`; then
   `-ECANCELED` (the shard's tag mismatch) → `repair_olh` rewrites the head's `olh.idtag` and
   `olh.info` from the index's olh entry via `bi_get_olh` under the old tag's guard
   ([rgw_rados.cc:8079-8128](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8079-L8128)) and retries; any other error → a best-effort `update_olh` and return.
5. `update_olh` ([rgw_rados.cc:8443-8471](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8443-L8471)): loop `bucket_index_read_olh_log(ver_marker)` (tag-guarded by
   the class) + `apply_olh_log` until not truncated. `apply_olh_log` ([rgw_rados.cc:8213-8370](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8213-L8370)): one write op
   on the head guarded by `cmpxattr(olh.idtag, EQ, tag)` and `cmpxattr(olh.ver, GTE, last_ver)`
   in u64 mode, setting `olh.ver = last_ver` and the kept mtime; replaying the entries in epoch
   order: LINK_OLH applies when its epoch is above the head's current epoch, or the head has no
   target instance, or the entry's instance sorts above the current one ([rgw_rados.cc:8286-8300](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8286-L8300));
   UNLINK_OLH marks removal; REMOVE_INSTANCE collects instances to delete; each entry's pending
   attr is removed; `olh.info` is set when a link won; the collected instance heads are deleted
   through the unversioned delete before the head op ([rgw_rados.cc:8334-8345](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8334-L8345)); then `clear_olh` (the head
   removed under `cmpxattr(olh.idtag, EQ, tag)` + `cmpxattr(olh.ver, EQ, ver)` + no pending
   attrs, then `bucket_clear_olh`, [rgw_rados.cc:8387-8438](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8387-L8438)) or `bucket_index_trim_olh_log(last_ver)`.
   `-ECANCELED` anywhere in `update_olh` after a successful link is success: "raced with another
   user" ([rgw_rados.cc:8556-8559](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8556-L8559)).
6. A later reader's `follow_olh` ([rgw_rados.cc:8773-8825](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8773-L8825)) expires pending attrs older than
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
`rgw_debug_inject_set_olh_err` scenario, [rgw.yaml.in:2886](https://github.com/ceph/ceph/blob/v20.2.4/src/common/options/rgw.yaml.in#L2886)), which
`bucket check` territory handles; both gateways share this. Concurrent deletes of one instance
end with the second's `unlink_instance` answering `-ENOENT` → 204. A write racing a delete: the
marker and the version are both linked, in class order. These are the same for a radosgw and an
rgw-go writer because every decision is made by the class or by a guarded head op; rgw-go
interoperates by sending the same ops with the same guards and tags.

**Three places rgw-go must differ from radosgw's own code to stay safe or possible:**

- *Completion ordering* (§2.3): await the ADD complete before `set_olh`; radosgw relies on
  librados ordering its own asynchronous complete ahead of the link.
- *The u64 cmpxattr.* The librados C API's `rados_write_op_cmpxattr` and `rados_read_op_cmpxattr`
  compare in string mode only ([librados_c.cc:3667-3683](https://github.com/ceph/ceph/blob/v19.2.6/src/librados/librados_c.cc#L3667-L3683), [librados_c.cc:4148-4164](https://github.com/ceph/ceph/blob/v19.2.6/src/librados/librados_c.cc#L4148-L4164));
  the C++ u64 mode parses the stored `olh.ver` as a number, an empty one as 0
  ([PrimaryLogPG.cc:4959-4974](https://github.com/ceph/ceph/blob/v19.2.6/src/osd/PrimaryLogPG.cc#L4959-L4974)). A string GTE would compare "9" above "10".
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
  every release (§2.2; `RGWRados::guard_reshard`, [rgw_rados.cc:7720-7770](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L7720-L7770),
  [rgw_rados.cc:8575](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L8575), re-initializes the shard from the bucket on each try). v20.2.4's class guards
  them itself as well, with the IN_LOGRECORD threshold ([cls_rgw.cc:906-922](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L906-L922)).
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
  ([rgw_op.cc:990](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L990), [rgw_op.cc:1039](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L1039), [rgw_op.cc:1078](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L1078), [rgw_op.cc:1120](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L1120), [rgw_op.cc:5145](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5145), [rgw_op.cc:5746](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5746), [rgw_op.cc:6829](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L6829), [rgw_op.cc:7767](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L7767)),
  exist in `internal/policy/action.go` and the ops already select them. The object
  ACL evaluated is the resolved version's (the state read follows the OLH). A delete marker
  carries the requester as its index owner ([rgw_rados.cc:5797-5798](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5797-L5798)), which the listing renders.
- **Quota and stats.** Every version is an index entry the complete accounts
  (`complete_op` ADD, §2.2) and the unlink's delete unaccounts; delete markers are instance
  entries created by the class without stats; `check_quota` on a PUT counts the bucket's totals
  as radosgw does, so nothing changes in M's enforcement.
- **Multipart.** `CompleteMultipartUpload` is the versioned write (§2.3); parts live in the
  `multipart` namespace and are never versioned (`set_versioning_disabled`,
  [rgw_putobj_processor.cc:372](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_putobj_processor.cc#L372)); UploadPartCopy's source
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
| 3 | Squid evaluates a versioned PUT's If-Match/If-None-Match against the new, nonexistent instance: If-Match always 412, If-None-Match `*` never fails ([rgw_rados.cc:3171](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L3171), [rgw_rados.cc:6493-6543](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6493-L6543); fixed by `get_current_version_state`, [rgw_rados.cc:7238-7258](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7238-L7258)) | v19.2.6 | S3-correctness | unreproduced (expected visible in the Squid recording of the `conditional_write`+`versioning` tests) | Tentacle's current-version evaluation on both (PP-3 rule); exclusion + `known-differences-squid` |
| 4 | Conditional writes on versioned buckets are check-then-act: two `If-None-Match: *` writers both succeed; an `If-Match` write lands after its matched version was replaced ([rgw_rados.cc:3282-3300](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L3282-L3300); no guard ties the link to the evaluation) | both | S3-correctness (race) | unreproduced | not reproduced if Q1's OLH-head guard is adopted; else same as radosgw, documented |
| 5 | CopyObject sends neither `x-amz-version-id` nor `x-amz-copy-source-version-id` ([rgw_rest_s3.cc:3566-3603](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L3566-L3603), [rgw_rest_s3.cc:3873](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L3873)) | both | S3-correctness | unreproduced | send both (Q4); exclusion |
| 6 | GET/HEAD naming a delete marker's version id answers 404 NoSuchKey (v20.2.4 with `x-amz-delete-marker: false`), S3 answers 405 MethodNotAllowed with `true` (§2.5) | both | S3-correctness | unreproduced | 405 + `true` after a `bi_get` of the instance on the 404 path (Q2); exclusion |
| 7 | GET/HEAD whose current version is a delete marker: 404 without `x-amz-delete-marker` ([rgw_rest.cc:1893-1933](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L1893-L1933); Tentacle sends it, [rgw_rest.cc:1936-1944](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L1936-L1944)) | v19.2.6 | S3-correctness | unreproduced | send it on both (Tentacle feature level); exclusion on Squid |
| 8 | `x-amz-version-id` omitted when the version id is `null` (PutObject, CompleteMultipart on a suspended bucket; GET of a null current version), where S3 sends `null` (`dump_header_if_nonempty`, [rgw_rest_s3.cc:461](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L461), [rgw_rest_s3.cc:2748](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L2748), [rgw_rest_s3.cc:4081](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L4081)) | both | S3-correctness (minor) | unreproduced | send `null` when the bucket is versioned and the instance empty (Q5); exclusion |
| 9 | `versionId` on PutObject/CopyObject's destination is honoured as the instance on a non-versioning-enabled bucket (R, "radosgw indexes a PutObject naming versionId=null …") | both | S3-correctness | unreproduced | ignore the parameter on PUT and copy destination (Q3); exclusion; registry entry updated |
| 10 | `cls_otp` divides by a stored `step_size` of 0 (R, #80948) | both | denial of service, privileged | liboath reproducer only | this area never writes otp entries; writepath's admin otp refuses 0 |
| 11 | `cls_otp` replay index from the unsigned window distance (R, #80949) | both | S3-correctness (TOTP replay window) | liboath reproducer | class-side; rgw-go calls the class as radosgw does |
| 12 | `cls_otp_client` sends the check-op buffer to `otp_get_result`, whose server decodes a check op; `cls_otp_get_result_op` is dead ([cls_otp_client.cc:78-83](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/otp/cls_otp_client.cc#L78-L83), [cls_otp.cc:487](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/otp/cls_otp.cc#L487)) | both | cosmetic (consistent by accident) | n/a | rgw-go sends the check op to `otp_get_result`; a registry note so no one "fixes" it one-sided |
| 13 | `RGWRados::get_olh` decodes `olh.ver` as `RGWOLHInfo` ([rgw_rados.cc:8700-8705](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8700-L8705); reads `olh.info` at [rgw_rados.cc:9640](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9640)); only `radosgw-admin olh get` calls it | v19.2.6 | cosmetic (admin tool) | unreproduced | not met: rgw-go has no such call; recorded as not-met so the gate's `olh get` on Squid is read accordingly |
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
reply with its marker ([cls_rgw.cc:680-691](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L680-L691)). Specs: seam unit on the
emulator; an integration spec against a shard. Deps: none. PR: one. ~120 lines. Retires the
exclusion "A shard that answers advance-and-retry fails an ordered listing" together with V9.

**V1 — `cls/rgw`: the OLH methods, `bi_get`, the index key encoders.** Layer: cls. Files: new
`ops_olh.go`, `types_olh.go`, `ops_bi.go`, `indexkey.go` (+ tests, goldens); `const.go` gains
five names. Behaviour: §2.2's structs at their versions and the builders' field order
([cls_rgw_client.cc:513-624](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw_client.cc#L513-L624)); the OLH entry and log entry ([cls_rgw_types.h:513-593](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw_types.h#L513-L593));
`encode_obj_versioned_data_key`, `encode_olh_data_key`, `get_list_index_key`, `decreasing_str`,
`cls_rgw_after_versions`, `decode_list_index_key` ([cls_rgw.cc:272-353](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L272-L353), [cls_rgw.cc:417-462](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L417-L462)).
Seams: `LinkOLH(op Execer, LinkOLHOp, rel)`, `UnlinkInstance`, `ReadOLHLog(op *ReadOp, …)
*OLHLogResult`, `TrimOLHLog`, `ClearOLH`, `BIGet(op *ReadOp, key ObjKey, typ BIIndexType)
*BIGetResult`, and the pure key functions. Specs: ceph-dencoder goldens, byte-literal key specs.
Deps: none. PR: one. ~200 lines.

**V2 — `meta`: OLH head types.** Layer: meta. `AttrOLHPendingPrefix`; `OLHInfo` v1 and
`OLHPendingInfo` v1 encode/decode ([rgw_rados.h:94-138](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.h#L94-L138)); `VersioningEnabled`,
`Versioned` helpers on `BucketInfo` ([rgw_common.h:1074-1079](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.h#L1074-L1079)), lifting
`driver.versioningEnabled`; the OLH pending op-tag format and the random instance generator
([rgw_rados.cc:7681-7690](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L7681-L7690), [rgw_rados.cc:8672-8681](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8672-L8681)) as `meta` functions. Specs: goldens; format
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
`olhInitModification` ([rgw_rados.cc:7607-7718](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L7607-L7718), [rgw_rados.cc:8462](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L8462)),
`olhCancelModification` ([rgw_rados.cc:7562-7605](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L7562-L7605)), `checkPendingOLHEntries` with
`rgw_olh_pending_timeout_sec` from `Env.Conf` ([rgw_rados.cc:8708-8738](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8708-L8738)), `removeOLHPendingEntries`
([rgw_rados.cc:8740-8771](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8740-L8771)), `followOLH` ([rgw_rados.cc:8773-8825](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8773-L8825); sets the state's delete-marker flag as v20.2.4
[rgw_rados.cc:9760-9764](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9760-L9764)), `applyOLHLog`'s head op and replay rules ([rgw_rados.cc:8213-8370](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L8213-L8370)) with the decision: the
`olh.ver` guard is string-mode EQ on the bytes read (§2.9; a `docs/cgo-limitations.md` entry
"librados' C API has no u64 cmpxattr"), `clearOLH`'s head op ([rgw_rados.cc:8387-8428](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8387-L8428)), `repairOLH`'s head
write ([rgw_rados.cc:8079-8128](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8079-L8128)). The shard calls these need are injected as a small interface so V4's specs
run on the emulator with V3's methods. Specs: §5's driver list for the head ops; the EQ guard
under a concurrent newer apply. Deps: V1, V2, V3. PR: one. ~250 lines.

**V5 — driver: the OLH index operations and `setOLH`/`unlinkObjInstance`.** Layer: driver
(`olh_index.go`, `indexop.go`). A guarded shard write for an arbitrary class call
(`indexOp.guardedWrite(ctx, func(w *WriteOp))`, reusing `guardReshard`,
[rgw_rados.cc:7934-7980](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L7934-L7980), [rgw_rados.cc:7988-8027](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L7988-L8027), [rgw_rados.cc:8130-8199](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8130-L8199)); `readOLHLog` as an unguarded
read on the current shard ([rgw_rados.cc:8029-8073](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8029-L8073)); `bucketIndexClearOLH`; `updateOLH`'s loop
([rgw_rados.cc:8443-8471](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8443-L8471)); `setOLH` and `unlinkObjInstance` with the 100-try ECANCELED loop,
`repair_olh`, the best-effort update on other errors and the skip flag ([rgw_rados.cc:8473-8670](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8473-L8670)); and
`indexOp.completeSync`, the awaited ADD completion (§2.3's decision), with a spec that the
versioned write never issues the link before the completion's reply. Specs: §5's driver list for
the sequence and races. Deps: V4. PR: one. ~250 lines.

**V6 — driver: reads follow the OLH; attrs on a version.** Layer: driver (`stat.go`, `read.go`,
`attrs.go`). `StatObject`/`PrefetchObject` follow when no instance is named
(`get_obj_state_impl`, [rgw_rados.cc:6120-6297](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6120-L6297)): the refusal at `internal/driver/stat.go:148-152`
goes; `ObjectState` gains `Instance` (the resolved version) and `DeleteMarker` (the current is
a marker, from `followOLH`); `versionId=null` on an OLH head without a manifest is NoSuchKey
([rgw_rados.cc:6289-6293](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6289-L6293)); a missing current (marker) is NoSuchKey with `DeleteMarker` set.
`SetObjectAttrs` on the resolved instance (`set_attrs`, [rgw_rados.cc:6593-6757](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6593-L6757)): the instance key's
prepare/complete, the `null` clearing kept; refusals removed. Specs: follow, pending-entry
reconciliation on read, null version present/absent, attrs on a noncurrent version. Deps: V5.
PR: one. ~200 lines. Owner of `op/object.go`'s `ObjectState` edits.

**V7 — driver: versioned PutObject, CopyObject, multipart completion.** Layer: driver
(`put.go`, `headwrite.go`, `copy.go`, `mp_complete.go`). The key carries the instance the op
chose; `versionedOp` per `_do_write_meta` ([rgw_rados.cc:3280-3287](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L3280-L3287), [rgw_rados.cc:3433-3439](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L3433-L3439)): the
VERSIONED_OP bilog flag, the id-tag guard rule ([rgw_rados.cc:3298-3300](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L3298-L3300)), conditions evaluated on
the current version's state ([rgw_rados.cc:3282-3297](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L3282-L3297)) on both releases, the OLH head's attrs kept
on reset (already), the awaited complete, then `setOLH(obj, false, nil, 0, …)`
([rgw_rados.cc:3337-3343](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L3337-L3343)); the null version over an OLH head; copy and completion through the same
path; `mp_complete.go`'s `attrMPCompletionInstance` becomes the instance. Refusals at
`internal/driver/mp_complete.go:269-271` removed. Specs: new key, overwrite on an enabled bucket, suspended
null version, copy with a source version, completion, the Squid/Tentacle condition table on
versions, the ordering spec. Deps: V5, V6. PR: one (put) + one (copy, multipart) if the brief
exceeds 250 lines. ~250 lines.

**V8 — driver: versioned DeleteObject; the admin removals' driver half.** Layer: driver
(`delete.go`, `purge.go`, `bucketadmin.go`). `ObjectStore.DeleteObject` returns
`(*DeleteResult{VersionID string; DeleteMarker bool}, error)`; `DeleteParams` gains `Owner`
(marker owner, the requester's `ACLOwner`) and `SkipOLHUpdate`; the versioned branch
([rgw_rados.cc:5757-5843](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5757-L5843), [rgw_rados.cc:6445-6577](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6445-L6577)): marker key random or null, meta,
`setOLH(delete_marker=true)`; instance path: `bi_get` of the instance entry (ENOENT →
NoSuchKey), Tentacle's conditions against the dirent ([rgw_rados.cc:6520-6554](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6520-L6554)) and, for the marker
path, against the current version ([rgw_rados.cc:6494-6509](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6494-L6509)), `unlinkObjInstance`; `apply_olh_log`'s instance
deletion reuses the unversioned path with the guard; the purge and admin object removal lose
`refuseVersionedPurge`/the bucket-flag refusal and list with versions. Specs: both paths, null
instance, missing instance, marker on marker, conditions, the admin paths. Deps: V5, V1. PR:
one (delete) + one (admin removals). ~250 lines. Owner of the `ObjectStore.DeleteObject`
signature change (memstore, `opfakes`, `driverfakes` regenerated here).

**V9 — driver: ListObjects with versions.** Layer: driver (`list.go`) and `op/bucket.go`.
`ListObjectsParams.MarkerInstance`, `ListObjectsResult.NextMarkerInstance` (the last entry's
instance, [rgw_rados.cc:1998-2001](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L1998-L2001)); the class resolves the marker; the refusal at
`op/listobjects.go:121-123` goes; advance-and-retry: on `RGWBIAdvanceAndRetryError` the shard is
listed again from the reply's marker ([cls_rgw_client.cc:99-110](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw_client.cc#L99-L110), [cls_rgw_client.cc:161-166](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw_client.cc#L161-L166);
[svc_bi_rados.cc:948-965](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/services/svc_bi_rados.cc#L948-L965)). Specs: markers with and without instances, `null`, a stale
instance, eight reads of markers. Deps: V0, V3. PR: one. ~180 lines.

**V10 — op + s3: PutBucketVersioning, GetBucketVersioning.** Layer: op (`bucketversioning.go`)
and s3 (`bucketversioning.go`). The parse table, flags, the object-lock refusal (lock-lifecycle's
interface), MFA rules reading `Request.MFAVerified` (false until V13 lands), `PutBucketInfo`
under `RetryRacedBucketWrite`; the three documents. Behaviour: [rgw_op.cc:2729-2860](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L2729-L2860),
[rgw_op.cc:2979-3092](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L2979-L3092); [rgw_rest_s3.cc:2152-2261](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L2152-L2261). Specs: §5's op list. Deps: none
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
fails on it, [rgw_rest_s3.cc:4996-5001](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L4996-L5001)). Specs: parse (two fields), unknown serial,
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
