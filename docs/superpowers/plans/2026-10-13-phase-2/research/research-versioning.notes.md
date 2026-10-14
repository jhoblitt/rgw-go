# Notes ledger: versioning research

Each entry: the claim; its source (a link to the lines at the tag for radosgw, a repo-relative
`path:line` at origin/main for rgw-go); the release(s) it holds on. Entries are appended as
verified; nothing here is from memory.

## Citation form

Applied 2026-10-13 on the coordinator's rules: every Ceph citation carries its full path from
ceph's root and a tag (v19.2.6 or v20.2.4), never a shorthand span or a branch; rgw-go code is a
plain `internal/<path>:<line>` with no ref, read at origin/main = 0239625 as N1 records. Both this
file and the report were normalized by a reviewed script and pass hack/citecheck at 8f30fb6 with
no failure.

## Documents read

- research-brief.md (this directory): requirements. Read in full.

## Verified facts

### Repository state

- N1. Worktree HEAD is af535e4; origin/main is 0239625. Every rgw-go citation below is read with
  `git show origin/main:<path>` (scripts in $TMPDIR). `git diff --stat af535e4 0239625` touches
  none of internal/meta/attrs.go, objkey.go, bucket_info.go, internal/op/putobject.go,
  deleteobject.go, deleteobjects.go, listobjects.go, internal/s3/listobjects.go,
  internal/testutil/fakerados/cls_rgw.go, internal/cls/rgw/*; it does touch internal/s3/handler.go,
  internal/s3/request.go, internal/driver/mp_complete.go, hack/s3tests/deselect-phase2.txt,
  hack/admin/deselect-phase2.txt, docs/exclusions.md, docs/ceph-upstream-bugs.md.
- N2. PP-12 (fix findings in their PR; zero open findings at the gate) and PP-13 (one PR per
  independent change; a task states its PR split): `docs/porting-process.md:151-177`.

### cls_rgw: the bucket index side of the OLH protocol (OSD class)

- C1. Special-entry prefix byte `BI_PREFIX_CHAR` 0x80; prefixes: "" objs, "0_" log, "1000_" obj
  instance, "1001_" olh data, "9999_" last. [cls_rgw.cc:51-66](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L51-L66). v20.2.4 adds
  "2001_" reshard log (`BI_BUCKET_RESHARD_LOG_INDEX` 4, LAST 5): diff hunk @@ -48,30 +48,32 @@,
  so [cls_rgw.cc:51-70](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L51-L70) (to pin exactly).
- C2. List index key of a versioned instance: `name + "\0v" + decreasing_str(versioned_epoch) +
  "\0i" + instance` (`get_list_index_key`, [cls_rgw.cc:302-315](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L302-L315)). `cls_rgw_after_versions` = name +
  "\1" ([cls_rgw.cc:319-323](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L319-L323)). Instance entry key: 0x80 "1000_" name "\0i" instance, plus "\0d" for a
  null-instance delete marker (`encode_obj_versioned_data_key`, [cls_rgw.cc:325-337](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L325-L337)). Plain entry key = name
  when instance empty, else the instance key (`encode_obj_index_key`, [cls_rgw.cc:339-346](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L339-L346)). OLH data key:
  0x80 "1001_" name (`encode_olh_data_key`, [cls_rgw.cc:348-353](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L348-L353)).
- C3. `encode_list_index_key` ([cls_rgw.cc:358-390](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L358-L390)): instance "null" is cleared before lookup; a
  missing instance entry yields `name + "\x01"` as the start-after key.
- C4. `decode_list_index_key` ([cls_rgw.cc:417-462](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L417-L462)): splits on NUL; "i<instance>", "v<ver>".
- C5. `rgw_bucket_dir_entry` flags FLAG_VER 0x1, FLAG_CURRENT 0x2, FLAG_DELETE_MARKER 0x4,
  FLAG_VER_MARKER 0x8, FLAG_COMMON_PREFIX 0x8000; encode v8 compat 3 (…, tag, key.instance, flags,
  versioned_epoch); `is_current` = !FLAG_VER || (VER|CURRENT); `is_visible` = current && !DM;
  `is_valid` = !VER_MARKER. [cls_rgw_types.h:369-463](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw_types.h#L369-L463). rgw-go already has these
  flags (`internal/cls/rgw/const.go`, FlagVer..FlagCommonPrefix).
- C6. `OLHLogOp` UNKNOWN 0, LINK_OLH 1, UNLINK_OLH 2, REMOVE_INSTANCE 3; `rgw_bucket_olh_log_entry`
  v1 (epoch u64, op u8, op_tag, key, delete_marker); `rgw_bucket_olh_entry` v1 (key,
  delete_marker, epoch, pending_log map<u64, vector<log_entry>>, tag, exists, pending_removal).
  [cls_rgw_types.h:513-593](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw_types.h#L513-L593).
- C7. Request structs `src/cls/rgw/cls_rgw_ops.h@v19.2.6`: `rgw_cls_link_olh_op` v5 compat 1 (key,
  olh_tag, delete_marker, op_tag, meta, olh_epoch, log_op, bilog_flags, u64 time_t(unmod_since),
  unmod_since, high_precision_time, zones_trace) [cls_rgw_ops.h:167-232](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw_ops.h#L167-L232); `rgw_cls_unlink_instance_op` v3 (key,
  op_tag, olh_epoch, log_op, bilog_flags, olh_tag, zones_trace) [cls_rgw_ops.h:234-276](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw_ops.h#L234-L276);
  `rgw_cls_read_olh_log_op` v1 (olh, ver_marker, olh_tag) [cls_rgw_ops.h:278-303](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw_ops.h#L278-L303); `rgw_cls_read_olh_log_ret` v1
  (log, is_truncated) [cls_rgw_ops.h:306-328](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw_ops.h#L306-L328); `rgw_cls_trim_olh_log_op` v1 (olh, ver, olh_tag) [cls_rgw_ops.h:330-355](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw_ops.h#L330-L355);
  `rgw_cls_bucket_clear_olh_op` v1 (key, olh_tag) [cls_rgw_ops.h:357-380](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw_ops.h#L357-L380). The v19.2.6→v20.2.4 diff of
  cls_rgw_ops.h touches none of these six structs (it changes tag_timeout_op, prepare_op defaults,
  update_stats v2, bi_put_entries, bi_list v2, reshard_add v2). cls_rgw_types.h diff: dir_entry
  and olh types unchanged; adds BIIndexType::ReshardDeleted, rgw_bucket_deleted_entry,
  reshardlog_entries in the dir header (v8), IN_LOGRECORD status.
- C8. Class-side helpers `src/cls/rgw/cls_rgw.cc@v19.2.6`: `read_olh` [cls_rgw.cc:1251-1266](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L1251-L1266);
  `update_olh_log` [cls_rgw.cc:1268-1279](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L1268-L1279) (appends to pending_log[olh epoch]); `BIVerObjEntry` [cls_rgw.cc:1318-1478](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L1318-L1478):
  `init(check_delete_marker)` reads the instance entry (for a null instance the "\0d" DM entry is
  separate), `init_as_delete_marker` sets FLAG_DELETE_MARKER and tag "delete-marker" ([cls_rgw.cc:1350-1358](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L1350-L1358)),
  `write(epoch, current)` removes the old list entry when versioned_epoch>0 then writes FLAG_VER
  (+CURRENT) ([cls_rgw.cc:1410-1427](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L1410-L1427)), `demote_current` clears CURRENT ([cls_rgw.cc:1429-1431](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L1429-L1431)), `find_next_key` lists
  one key after this instance's list key filtered by name ([cls_rgw.cc:1437-1473](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L1437-L1473)). `BIOLHEntry` [cls_rgw.cc:1481-1566](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L1481-L1566):
  `start_modify(candidate)`: nonzero candidate < olh.epoch → false, else epoch=candidate; zero →
  epoch 0 becomes 2 ("1 is reserved to converted plain entries"), else ++ ([cls_rgw.cc:1503-1517](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L1503-L1517)).
  `write_version_marker` writes a FLAG_VER_MARKER entry at the plain name ([cls_rgw.cc:1568-1579](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L1568-L1579)).
  `convert_plain_entry_to_versioned` ([cls_rgw.cc:1587-1634](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L1587-L1634)): the plain entry gets versioned_epoch 1 and
  FLAG_VER, CURRENT cleared if demote, written at the instance key (and list key unless
  instance_only), then the version marker.
- C9. `rgw_bucket_link_olh` [cls_rgw.cc:1652-1865](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L1652-L1865). Order: decode; `obj.init(op.delete_marker)`;
  ENOENT tolerated only for a delete marker ([cls_rgw.cc:1682-1688](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L1682-L1688)); unmod_since check returns 0 with no
  change when mtime >= unmod ([cls_rgw.cc:1690-1701](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L1690-L1701)); null-instance special case unlinks the other null entry
  ([cls_rgw.cc:1712-1734](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L1712-L1734)); read olh; `start_modify(op.olh_epoch)` false → write instance non-current (+log
  REMOVE_INSTANCE if removing) and return 0 ([cls_rgw.cc:1751-1760](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L1751-L1760)); promote = newer epoch, or same epoch and
  olh.key.instance >= op.key.instance ([cls_rgw.cc:1764-1766](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L1764-L1766)); olh found and `op.olh_tag != olh.tag` and not
  pending_removal → **-ECANCELED** ([cls_rgw.cc:1768-1777](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L1768-L1777)); promote demotes the previous current
  ([cls_rgw.cc:1778-1790](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L1778-L1790)); olh not found → convert plain entry, set tag, null instance gets epoch 1
  ([cls_rgw.cc:1792-1804](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L1792-L1804)); log LINK_OLH (+REMOVE_INSTANCE) ([cls_rgw.cc:1806-1810](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L1806-L1810)); update olh key/dm if promote; write
  olh; write instance (current = promote) ([cls_rgw.cc:1812-1827](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L1812-L1827)); bilog only if `op.log_op` and not
  syncstopped, with `op.bilog_flags | RGW_BILOG_FLAG_VERSIONED_OP` ([cls_rgw.cc:1829-1864](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L1829-L1864)).
- C10. `rgw_bucket_unlink_instance` [cls_rgw.cc:1867-2018](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L1867-L2018): instance entry missing → **-ENOENT**
  ([cls_rgw.cc:1888-1894](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L1888-L1894)); no olh entry → convert plain entry (demote), olh.update(dest_key,false), tag,
  epoch 1 ([cls_rgw.cc:1903-1915](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L1903-L1915)); old epoch → unlink list entry; delete marker → return 0, else log
  REMOVE_INSTANCE and write ([cls_rgw.cc:1917-1929](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L1917-L1929)); unlinking the current head: `find_next_key`; found →
  next written current and LINK_OLH logged; none → UNLINK_OLH logged, exists=false,
  pending_removal=true, key name kept for hash placement ([cls_rgw.cc:1936-1968](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L1936-L1968)); a delete marker's instance
  entry is removed by the class, a data instance logs REMOVE_INSTANCE ([cls_rgw.cc:1970-1979](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L1970-L1979)); list entry
  removed; olh written; bilog as link ([cls_rgw.cc:1991-2017](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L1991-L2017)). Note: no olh-tag check in unlink_instance.
- C11. `rgw_bucket_read_olh_log` [cls_rgw.cc:2020-2072](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L2020-L2072): -EINVAL for a non-empty instance;
  -ECANCELED when `olh_data_entry.tag != op.olh_tag` (a missing entry has tag "" so any nonempty
  tag is ECANCELED); MAX_OLH_LOG_ENTRIES 1000; returns entries with epoch > ver_marker,
  is_truncated when more. Registered CLS_METHOD_RD ([cls_rgw.cc:4701](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L4701)).
- C12. `rgw_bucket_trim_olh_log` [cls_rgw.cc:2074-2124](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L2074-L2124): -EINVAL non-empty instance; -ECANCELED tag
  mismatch; erases pending_log keys <= op.ver; writes the olh entry.
- C13. `rgw_bucket_clear_olh` [cls_rgw.cc:2126-2190](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L2126-L2190): -EINVAL non-empty instance; -ECANCELED tag
  mismatch; removes the olh data key; removes the plain entry only if FLAG_VER_MARKER.
- C14. `rgw_bucket_list` [cls_rgw.cc:487-694](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L487-L694): VER_MARKER entries skipped as invalid ([cls_rgw.cc:604-608](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L604-L608));
  with `!list_versions`, a non-visible entry or one named like start_obj skips to
  `cls_rgw_after_versions(name)` ([cls_rgw.cc:611-623](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L611-L623)); returns `RGWBIAdvanceAndRetryError` (-EFBIG) when
  truncated with nothing listed ([cls_rgw.cc:688-691](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L688-L691)); `max_attempts` 8 ([cls_rgw.cc:492](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L492)).
- C15. `rgw_bucket_complete_op` resets entry flags to FLAG_VER only ([cls_rgw.cc:1061-1064](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L1061-L1064)).
- C16. Method registration: LINK_OLH, UNLINK_INSTANCE RD|WR; READ_OLH_LOG RD; TRIM_OLH_LOG,
  CLEAR_OLH RD|WR: [cls_rgw.cc:4699-4703](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L4699-L4703), [cls_rgw.cc:5116-5120](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L5116-L5120). Names
  [cls_rgw_const.h:24-28](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw_const.h#L24-L28) (bucket_link_olh, bucket_unlink_instance,
  bucket_read_olh_log, bucket_trim_olh_log, bucket_clear_olh).
- C17. Client builders `src/cls/rgw/cls_rgw_client.cc@v19.2.6`: link [cls_rgw_client.cc:513-534](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw_client.cc#L513-L534) (olh_tag is the
  xattr bufferlist as a string, `olh_tag.to_str()`); unlink [cls_rgw_client.cc:550-566](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw_client.cc#L550-L566); read log as a read-op exec
  with a decoding completion [cls_rgw_client.cc:568-577](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw_client.cc#L568-L577); trim [cls_rgw_client.cc:597-606](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw_client.cc#L597-L606); clear [cls_rgw_client.cc:616-624](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw_client.cc#L616-L624).

### radosgw: the gateway side (src/rgw/driver/rados/rgw_rados.cc@v19.2.6)

- R1. `_do_write_meta` [rgw_rados.cc:3124-3417](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L3124-L3417): olh head keeps its OLH_ID_TAG ([rgw_rados.cc:3191-3193](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L3191-L3193));
  `versioned_target = (olh_epoch && *olh_epoch > 0) || !instance.empty()`; `versioned_op =
  versioning_enabled() || is_olh || versioned_target` sets bilog VERSIONED_OP ([rgw_rados.cc:3280-3287](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L3280-L3287));
  after the index complete, `versioned_op && meta.olh_epoch` → `set_olh(..., obj, false, NULL,
  *meta.olh_epoch, real_time(), false, ..., add_log)` ([rgw_rados.cc:3337-3343](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L3337-L3343)).
- R2. `Object::Delete::delete_obj` [rgw_rados.cc:5757-5987](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5757-L5987): "null" instance cleared ([rgw_rados.cc:5768-5770](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5768-L5770)); versioned
  branch when `versioning_status & BUCKET_VERSIONED || marker_version_id set` ([rgw_rados.cc:5774](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5774)); no
  instance → marker key: random instance unless `BUCKET_VERSIONS_SUSPENDED` ([rgw_rados.cc:5785-5787](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5785-L5787));
  result.version_id "null" when empty, delete_marker true ([rgw_rados.cc:5789-5793](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5789-L5793)); meta owner/display/mtime;
  `set_olh(..., marker, true, &meta, olh_epoch, unmod_since, high_precision_time, ...)`
  ([rgw_rados.cc:5806-5808](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5806-L5808)); with an instance → `bi_get_instance` then `unlink_obj_instance`,
  result.delete_marker = dirent.is_delete_marker(), version_id = instance ([rgw_rados.cc:5813-5828](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5813-L5828)); datalog
  entry when add_log ([rgw_rados.cc:5837-5840](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5837-L5840)). Unversioned path [rgw_rados.cc:5845-5987](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5845-L5987).
- R3. `is_olh(attrs)` = RGW_ATTR_OLH_VER present ([rgw_rados.cc:6086-6090](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6086-L6090)); `has_olh_tag` = OLH_ID_TAG
  present ([rgw_rados.cc:6092-6096](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6092-L6096)); `get_olh_target_state` follows and loads the target ([rgw_rados.cc:6098-6118](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6098-L6118)).
- R4. `get_obj_state_impl` [rgw_rados.cc:6120-6297](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6120-L6297): `need_follow_olh = follow_olh && instance.empty()`
  ([rgw_rados.cc:6129](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6129)); olh_tag from OLH_ID_TAG whether or not olh ([rgw_rados.cc:6278-6280](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6278-L6280)); is_olh → follow when
  needed; "null" instance on an olh head without a manifest → exists=false, -ENOENT
  ([rgw_rados.cc:6289-6293](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6289-L6293)). `get_obj_state` retries on -EAGAIN ([rgw_rados.cc:6299-6312](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6299-L6312)).
- R5. `olh_cancel_modification` [rgw_rados.cc:7562-7605](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L7562-L7605): guarded rmxattr of `user.rgw.olh.pending.<op_tag>`;
  then, if the state has no OLH_INFO, a guarded remove with `cmpxattr(OLH_INFO, EQ, "")`,
  `cls_obj_check_prefix_exist(pending prefix, fail_if_exist)`, `remove()`.
- R6. `olh_init_modification_impl` [rgw_rados.cc:7607-7706](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L7607-L7706): olh key must have no instance; `!exists` →
  `create(exclusive)`, else `assert_exists` + `mtime2(state.mtime)`; has olh tag → guard
  `cmpxattr(OLH_ID_TAG, EQ, tag)`; exists without tag (a plain head becoming an olh) →
  `cmpxattr(OLH_ID_TAG, EQ, "")` and `setxattr(OLH_INFO, {target=olh_obj, removed=false})`
  ([rgw_rados.cc:7636-7648](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L7636-L7648)); no tag → new idtag (32 lower alnum) and olh idtag (32), empty OLH_VER
  ([rgw_rados.cc:7650-7674](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L7650-L7674)); pending attr `user.rgw.olh.pending.<op_tag>` with op_tag = `%016llx` of the
  second + random lower alnum to 32 chars, value `RGWOLHPendingInfo{time}` ([rgw_rados.cc:7676-7695](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L7676-L7695)). The
  wrapper maps -EEXIST to -ECANCELED ([rgw_rados.cc:7708-7718](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L7708-L7718)).
- R7. `bucket_index_link_olh` [rgw_rados.cc:7934-7980](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L7934-L7980): `guard_reshard` around a write op of `assert_exists`,
  `cls_rgw_guard_bucket_resharding(-ERR_BUSY_RESHARDING)`, `cls_rgw_bucket_link_olh(key =
  (get_index_key_name, instance), olh_state.olh_tag, ...)`; datalog when log_data_change.
  `bucket_index_guard_olh_op` = `cmpxattr(OLH_ID_TAG, EQ, olh_tag)` ([rgw_rados.cc:7982-7986](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L7982-L7986)).
  `bucket_index_unlink_instance` [rgw_rados.cc:7988-8027](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L7988-L8027) same guard shape.
- R8. `bucket_index_read_olh_log` [rgw_rados.cc:8029-8073](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8029-L8073): a read op, `bs.init(bucket, obj, nullptr)`, no
  reshard guard; key has an empty instance; olh_tag from state. `repair_olh` [rgw_rados.cc:8079-8128](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8079-L8128) rewrites
  OLH_ID_TAG and OLH_INFO from `bi_get_olh` under the old tag's guard (tracker #37792).
  `bucket_index_trim_olh_log` [rgw_rados.cc:8130-8167](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8130-L8167) and `bucket_index_clear_olh` [rgw_rados.cc:8169-8199](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8169-L8199) are guarded
  writes.
- R9. `apply_olh_log` [rgw_rados.cc:8213-8370](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8213-L8370): write op on the olh head: `cmpxattr(OLH_ID_TAG, EQ, tag)`,
  `cmpxattr(OLH_VER, GTE, last_ver)` (u64 mode), `setxattr(OLH_VER, decimal string)`,
  `mtime2(state.mtime)` ([rgw_rados.cc:8238-8247](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8238-L8247)); current epoch and target decoded from OLH_VER / OLH_INFO
  ([rgw_rados.cc:8256-8272](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8256-L8272)); replay: REMOVE_INSTANCE collects keys; LINK_OLH applies when `link_epoch <
  epoch || key.instance.empty() || key.instance > entry.key.instance` ([rgw_rados.cc:8286-8300](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8286-L8300)); UNLINK_OLH
  sets need_to_remove ([rgw_rados.cc:8301-8304](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8301-L8304)); every entry's pending attr is rmxattr'd ([rgw_rados.cc:8309-8311](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8309-L8311));
  OLH_INFO set when need_to_link ([rgw_rados.cc:8323-8331](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8323-L8331)); removed instances deleted through `delete_obj(...,
  versioning_status 0, null_verid, RGW_BILOG_FLAG_VERSIONED_OP, ..., skip_olh_obj_update=true)`,
  ENOENT ignored ([rgw_rados.cc:8334-8345](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8334-L8345)); then the head op; then `clear_olh` if need_to_remove else
  `bucket_index_trim_olh_log(last_ver)`, -ECANCELED tolerated ([rgw_rados.cc:8354-8367](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8354-L8367)).
- R10. `clear_olh` (ref form) [rgw_rados.cc:8387-8438](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8387-L8438): reads state without following; expires pending attrs
  (`check_pending_olh_entries`, `remove_olh_pending_entries`); rm op: `cmpxattr(OLH_ID_TAG, EQ,
  tag)`, `cmpxattr(OLH_VER, EQ, ver)`, `cls_obj_check_prefix_exist(pending, fail_if_exist)`,
  `remove()`; -ECANCELED returned as is; then `bucket_index_clear_olh`.
- R11. `update_olh` [rgw_rados.cc:8443-8471](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8443-L8471): read log from ver_marker, apply, loop while truncated.
- R12. `set_olh` [rgw_rados.cc:8473-8566](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8473-L8566): MAX_ECANCELED_RETRY 100; per try: invalidate on ECANCELED,
  `get_obj_state(olh_obj, follow=false)`, `olh_init_modification`, `bucket_index_link_olh`; on link
  failure `olh_cancel_modification`, on -ECANCELED `repair_olh` then retry, other errors a
  best-effort `update_olh` unless skip ([rgw_rados.cc:8519-8541](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8519-L8541)); after the link `update_olh`, -ECANCELED → 0
  ([rgw_rados.cc:8556-8559](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8556-L8559)). `unlink_obj_instance` [rgw_rados.cc:8568-8670](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8568-L8670) mirrors it with bilog flags VERSIONED_OP (+
  NULL_VERSION when null_verid) ([rgw_rados.cc:8618-8622](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8618-L8622)).
- R13. `gen_rand_obj_instance_name`: 32 random alphanumerics, no underscore ([rgw_rados.cc:8672-8681](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8672-L8681)).
- R14. `get_olh` [rgw_rados.cc:8688-8706](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8688-L8706) decodes RGW_ATTR_OLH_VER as an RGWOLHInfo (looks wrong; check
  callers before recording). `check_pending_olh_entries` [rgw_rados.cc:8708-8738](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8708-L8738): expire by
  `rgw_olh_pending_timeout_sec`, stop at the first unexpired (names sort by time).
  `remove_olh_pending_entries` [rgw_rados.cc:8740-8771](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8740-L8771): guarded rmxattr in batches of 1000; ENOENT/ECANCELED
  → 0.
- R15. `follow_olh` [rgw_rados.cc:8773-8825](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8773-L8825): expire pending; if any pending remain → `update_olh` (-ECANCELED
  → -ENOENT) ([rgw_rados.cc:8788-8801](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8788-L8801)); no OLH_VER → -EINVAL; no OLH_INFO → -ENOENT; `removed` → -ENOENT;
  else target.

### radosgw: attrs, flags, MFA (v19.2.6)

- A1. `RGW_ATTR_OLH_PREFIX` "user.rgw.olh." [rgw_common.h:131](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.h#L131); OLH_INFO [rgw_common.h:133](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.h#L133);
  OLH_VER [rgw_common.h:134](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.h#L134); OLH_ID_TAG [rgw_common.h:135](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.h#L135); OLH_PENDING_PREFIX "user.rgw.olh.pending." [rgw_common.h:136](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.h#L136). rgw-go has
  the first four (`internal/meta/attrs.go:27-30`) and lacks the pending prefix.
- A2. Flags BUCKET_VERSIONED 0x2 [rgw_common.h:1022](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.h#L1022), BUCKET_VERSIONS_SUSPENDED 0x4 [rgw_common.h:1023](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.h#L1023), BUCKET_MFA_ENABLED
  0x10 [rgw_common.h:1025](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.h#L1025), BUCKET_OBJ_LOCK_ENABLED 0x20 [rgw_common.h:1026](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.h#L1026); `versioned()` [rgw_common.h:1074](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.h#L1074); `versioning_status()`
  masks VERSIONED|SUSPENDED|MFA [rgw_common.h:1075](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.h#L1075); `versioning_enabled()` [rgw_common.h:1076](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.h#L1076); `mfa_enabled()` [rgw_common.h:1077](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.h#L1077);
  `obj_lock_enabled()` [rgw_common.h:1079](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.h#L1079). rgw-go: `internal/meta/bucket_info.go:16-23`.
- A3. `RGWOLHInfo` [rgw_rados.h:94](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.h#L94), `RGWOLHPendingInfo` [rgw_rados.h:118](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.h#L118)
  (encodings to read). `RGWObjState::is_olh`, `olh_tag` [rgw_sal.h:104-105](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_sal.h#L104-L105).
- A4. MFA: `RadosUser::verify_mfa` [rgw_sal_rados.cc:284-310](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L284-L310): the
  header is split on " " into serial and pin (invalid string → NOTICE, not an error), the user's
  `mfa_ids` must hold the serial, then `svc.cls->mfa.check_mfa(user_id, serial, pin)`. Header read
  at [rgw_rest_s3.cc:4996-4998](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L4996-L4998) (`HTTP_X_AMZ_MFA`, into `s->mfa_verified`).
  `ERR_MFA_REQUIRED` 2044 → 403 AccessDenied ([rgw_common.cc:96](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L96),
  [rgw_common.h:296](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.h#L296)). Delete checks [rgw_op.cc:5158-5162](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5158-L5162) and [rgw_op.cc:7055-7065](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L7055-L7065); SetBucketVersioning
  MFA logic [rgw_op.cc:2795-2835](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L2795-L2835).

### rgw-go at origin/main (seams found so far)

- G1. cls/rgw has no OLH method: `internal/cls/rgw/const.go` lists bucket_init_index … 
  guard/set/get_bucket_resharding and mp_upload_part_info_update only; ops_index.go has
  Prepare/Complete/List/Suggest/CheckIndex/RebuildIndex. ModifyOp values incl. OpLinkOLH 4,
  OpLinkOLHDM 5, OpUnlinkInstance 6 exist; BILogFlagVersionedOp 0x1, BILogNullVersion 0x2 exist.
- G2. fakerados `RGWClass` (`internal/testutil/fakerados/cls_rgw.go:25-110`): no OLH
  methods; doc says "a prepare or complete naming an instance is EOPNOTSUPP". Hot file (PP-5).
- G3. Driver refusals: `DeleteObject` refuses VERSIONED|SUSPENDED|OBJ_LOCK buckets
  (`internal/driver/delete.go:34-36`); `internal/driver/stat.go:148-151` refuses an olh head (AttrOLHVer present);
  `internal/driver/mp_complete.go` refuses the same buckets; `internal/driver/copy.go:149-152` strips OLH attrs when the
  destination is not versioning-enabled, `versioningEnabled` at `internal/driver/copy.go:247-250`;
  `internal/driver/headwrite.go:206-216` ObjRemove keeps the olh prefix; `internal/driver/headwrite.go:320-342` has a Tentacle instance guard rule (read).
- G4. Op refusals: `versioningUnserved`/`newHeadVersioningUnserved`/`neverVersioned`
  (`internal/op/putobject.go:213-280`); `DeleteObject` has Versioned/VersionID/DeleteMarker and an
  MFA refusal (`internal/op/deleteobject.go:23-142`); `DeleteObjects` `internal/op/deleteobjects.go:156-190`; `ListObjects` refuses
  ListVersions on a versioned bucket (`internal/op/listobjects.go:108-122`); `CopyObject` `internal/op/copyobject.go:121`, `internal/op/copyobject.go:203`;
  `ListObjectsParams.ListVersions`, `ObjectEntry.IsLatest/DeleteMarker/Exists`
  (`internal/op/bucket.go:45-77`).
- G5. S3: `versionId` → `r.Object.Instance` (`internal/s3/request.go:93`); dispatch rows
  `get_bucket_versioning`/`set_bucket_versioning` exist (`internal/s3/dispatch.go:131`, `internal/s3/dispatch.go:161`) without a
  handler; the versions documents v1 and v2 are already rendered (`internal/s3/listobjects.go:83-430`);
  `internal/s3/deletemarker.go` sends x-amz-delete-marker false; `internal/s3/deleteobject.go:32-36`, `internal/s3/putobject.go:63-64`,
  `internal/s3/getobject.go:281-282` send version headers; `internal/s3/copyobject.go:146` and `internal/s3/multipart.go:275` parse the
  source versionId.
- G6. Seam: `radosclient.ExecResult` does not surface a positive class return
  (`docs/cgo-limitations.md:718`); not needed by the floors (OLH epoch reply is main-only).
- G7. Harness: `hack/s3tests/deselect-phase2.txt` names two versioning tests (worktree copy;
  re-read at origin/main); `hack/admin/deselect-phase2.txt` names
  `TestRadosGWTestSuite/TestBucket/enable_versioning` and `enable_bucket_object_lock`.

### Second batch

- C18. `decreasing_str` [cls_rgw.cc:272-290](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L272-L290), [cls_rgw.cc:322](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L322): a leading digit
  9..4 by magnitude band, then the zero-padded complement, so larger epochs sort first.
- C19. v20.2.4 class-side reshard guard: `guard_bucket_resharding` [cls_rgw.cc:906-922](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L906-L922) (busy when
  IN_PROGRESS, or IN_LOGRECORD with `reshardlog_entries >= rgw_reshardlog_threshold`, default
  30000, [rgw.yaml.in:2875-2879](https://github.com/ceph/ceph/blob/v20.2.4/src/common/options/rgw.yaml.in#L2875-L2879)); called first in link_olh
  ([cls_rgw.cc:1797-1808](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1797-L1808)), unlink_instance ([cls_rgw.cc:2020-2029](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2020-L2029)), trim ([cls_rgw.cc:2257-2266](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2257-L2266)), clear ([cls_rgw.cc:2297-2306](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2297-L2306)),
  prepare, complete, suggest, rebuild, bi_put_entries; not in read_olh_log. Every entry write goes
  through `write_entry(..., header)`/`remove_entry` which mirror into the reshard log while
  IN_LOGRECORD ([cls_rgw.cc:752-786](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L752-L786)).
- C20. v20.2.4 line numbers in src/cls/rgw/cls_rgw.cc: prefixes [cls_rgw.cc:51-70](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L51-L70); get_list_index_key [cls_rgw.cc:352](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L352); after_versions [cls_rgw.cc:369](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L369);
  versioned data key [cls_rgw.cc:375](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L375); obj_index_key [cls_rgw.cc:389](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L389); olh_data_key [cls_rgw.cc:398](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L398); encode_list_index_key
  [cls_rgw.cc:408](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L408); decode_list_index_key [cls_rgw.cc:467](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L467); bucket_list [cls_rgw.cc:537](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L537); read_olh [cls_rgw.cc:1374](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1374); BIVerObjEntry [cls_rgw.cc:1444](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1444);
  BIOLHEntry [cls_rgw.cc:1608](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1608) (epoch 2 at [cls_rgw.cc:1638](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1638)); write_version_marker [cls_rgw.cc:1695](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1695); convert [cls_rgw.cc:1715](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1715); link
  [cls_rgw.cc:1781](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1781) (ECANCELED [cls_rgw.cc:1914](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1914)); unlink [cls_rgw.cc:2002](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2002); read log [cls_rgw.cc:2161](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2161) (ECANCELED [cls_rgw.cc:2190](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2190), MAX 1000
  [cls_rgw.cc:2195](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2195)); trim [cls_rgw.cc:2215](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2215) (ECANCELED [cls_rgw.cc:2245](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2245)); clear [cls_rgw.cc:2279](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2279) (ECANCELED [cls_rgw.cc:2321](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2321)). cls_rgw_ops.h
  olh structs [cls_rgw_ops.h:165-378](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw_ops.h#L165-L378); cls_rgw_types.h dir_entry [cls_rgw_types.h:369-469](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw_types.h#L369-L469), OLHLogOp [cls_rgw_types.h:512](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw_types.h#L512),
  olh_entry ends [cls_rgw_types.h:592](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw_types.h#L592). Client builders [cls_rgw_client.cc:363-487](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw_client.cc#L363-L487).
- C21. `complete_op` ADD branch writes only `idx` (the key `read_key_entry` resolved), sets
  exists/tag/meta and accounts stats ([cls_rgw.cc:1168-1197](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L1168-L1197)); DEL branch [cls_rgw.cc:1127-1167](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L1127-L1167). The list
  entry of a version is written only by link_olh/unlink_instance.
- C22. `bi_get_instance` [rgw_rados.cc:9247-9267](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L9247-L9267) ([rgw_rados.cc:10192](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L10192)) → `bi_get(...,
  BIIndexType::Instance)` [rgw_rados.cc:9291](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L9291); `bi_get_olh` [rgw_rados.cc:9269-9289](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L9269-L9289) ([rgw_rados.cc:10214](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L10214)).
- R16. `prepare_atomic_modification` [rgw_rados.cc:6484-6576](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6484-L6576): Squid's guard condition
  includes `if_match || if_nomatch` and the conditions are evaluated against `state` = the
  object being written ([rgw_rados.cc:6493-6543](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6493-L6543)); for a versioned write that is the new, nonexistent
  instance. reset_obj with an existing head: `create(false)` + `remove_rgw_head_obj` (keeps
  olh.* attrs) [rgw_rados.cc:6546-6553](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6546-L6553). v20.2.4 [rgw_rados.cc:7331-7360](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7331-L7360): no conditions here; `set_attr_id_tag`
  decides the guard.
- R17. v20.2.4 `_do_write_meta` [rgw_rados.cc:3282-3300](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L3282-L3300): for a key with an instance,
  `get_current_version_state` (follows the OLH, [rgw_rados.cc:7238-7258](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7238-L7258)) supplies the state
  `check_preconditions` ([rgw_rados.cc:7260-7329](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7260-L7329)) evaluates; `set_attr_id_tag = guard && instance.empty()
  && if_nomatch != "*"` so instance writes carry no id-tag cmpxattr.
- R18. v20.2.4 versioned delete preconditions: marker path checks against the current version
  ([rgw_rados.cc:6494-6509](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6494-L6509)), instance path against the dirent from `bi_get_instance` ([rgw_rados.cc:6520-6554](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6520-L6554)); then
  `unlink_obj_instance` ([rgw_rados.cc:6556-6560](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6556-L6560)).
- R19. `get_system_versioning_params` [rgw_op.h:2113-2139](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.h#L2113-L2139): non-system → return
  0 leaving olh_epoch 0 and version_id empty. `AtomicObjectProcessor::complete` passes the
  engaged optional into `obj_op.meta.olh_epoch`
  ([rgw_putobj_processor.cc:385](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_putobj_processor.cc#L385); `olh_epoch` is
  `std::optional<uint64_t>`, [rgw_putobj_processor.h:171](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_putobj_processor.h#L171)), and sets
  `set_versioning_disabled(!versioning_enabled())` ([rgw_putobj_processor.cc:372](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_putobj_processor.cc#L372)).
- R20. `set_attrs` [rgw_rados.cc:6593-6757](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6593-L6757) ([rgw_rados.cc:7393](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7393)): "null" instance cleared
  ([rgw_rados.cc:6601-6603](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6601-L6603)); `append_atomic_test` guard; null version without manifest → -ENOENT
  ([rgw_rados.cc:6620-6622](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6620-L6622)); a fresh 32-char write tag, prepare ADD, setxattr idtag ([rgw_rados.cc:6665-6676](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6665-L6676)); mtime
  kept unless set ([rgw_rados.cc:6683-6688](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6683-L6688)); complete ADD with the state's size and the attrs' etag/ct/sc
  ([rgw_rados.cc:6723-6725](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6723-L6725)) or cancel. `RadosObject::set_obj_attrs` adds 1 ns when log_op
  ([rgw_sal_rados.cc:2384](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L2384)); `modify_obj_attrs` resolves the target through
  `get_obj_attrs` (follows the OLH) and writes the instance ([rgw_sal_rados.cc:2401-2419](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L2401-L2419)).
- R21. `Object::Read::prepare` [rgw_rados.cc:6847](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6847) ([rgw_rados.cc:7686](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7686)): `get_state(...,
  follow_olh=true)`; `!exists` → -ENOENT; `state.obj = astate->obj` (the resolved instance);
  `RadosReadOp::prepare` copies the instance back to the SAL object
  ([rgw_sal_rados.cc:2863](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L2863), [rgw_sal_rados.cc:3678](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L3678)), and `RGWGetObj::execute` reads
  `version_id = s->object->get_instance()` ([rgw_op.cc:2259-2260](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L2259-L2260)).
- R22. Listing: `list_objects_ordered` passes `list_versions` and drops `!is_visible` entries
  itself ([rgw_rados.cc:1886](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L1886), [rgw_rados.cc:1920-1924](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L1920-L1924); [rgw_rados.cc:1905](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L1905)); `cls_bucket_list_ordered`
  [rgw_rados.cc:9614](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L9614) ([rgw_rados.cc:10535](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L10535)) calls `check_disk_state` for an entry that is neither
  existing, a delete marker nor a common prefix, or has pending ops ([rgw_rados.cc:9823-9827](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L9823-L9827)); unordered
  [rgw_rados.cc:10083-10085](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L10083-L10085). `check_disk_state` [rgw_rados.cc:10323-10475](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L10323-L10475) ([rgw_rados.cc:11247](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L11247)) reads the entry's
  key with its instance and treats a delete marker as present ([rgw_rados.cc:10361](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L10361)).
- R23. Purge lists with `list_versions = true` ([rgw_sal_rados.cc:363](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L363), [rgw_sal_rados.cc:500](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L500)).
- O1. rgw_op.cc@v19.2.6: `RGWGetBucketVersioning::execute` [rgw_op.cc:2747-2757](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L2747-L2757) (NoSuchBucket; versioned,
  versioning_enabled, mfa_enabled); `RGWSetBucketVersioning::verify_permission` [rgw_op.cc:2759-2770](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L2759-L2770)
  (s3PutBucketVersioning); `::execute` [rgw_op.cc:2777-2860](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L2777-L2860): get_params, NoSuchBucket, object lock
  refusal `-ERR_INVALID_BUCKET_STATE` "bucket versioning cannot be disabled on buckets with object
  lock enabled" ([rgw_op.cc:2788-2793](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L2788-L2793)), MFA rules ([rgw_op.cc:2795-2817](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L2795-L2817)), forward to master, retry_raced_bucket_write
  flag update ([rgw_op.cc:2828-2849](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L2828-L2849)). v20.2.4: [rgw_op.cc:2979](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L2979), [rgw_op.cc:2991](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L2991), [rgw_op.cc:3009](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L3009) (MFA [rgw_op.cc:3032-3046](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L3032-L3046)).
- O2. rgw_rest_s3.cc@v19.2.6: GetBucketVersioning doc [rgw_rest_s3.cc:2152-2169](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L2152-L2169) (Status, MfaDelete only when
  versioned); `ver_config_status::decode_xml` [rgw_rest_s3.cc:2171-2202](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L2171-L2202) (Status Enabled|Suspended else
  invalid; MfaDelete Enabled|Disabled else -EINVAL); `SetBucketVersioning::get_params`
  [rgw_rest_s3.cc:2204-2261](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L2204-L2261) (read_all_input rgw_max_put_param_size; parse failure -EINVAL). v20.2.4 [rgw_rest_s3.cc:2255](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2255),
  [rgw_rest_s3.cc:2307](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2307).
- O3. `RGWPutObj::execute` instance: `versioning_enabled` → `set_instance(version_id)` if given
  else `gen_rand_obj_instance_name` ([rgw_op.cc:4278-4285](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L4278-L4285), [rgw_op.cc:4495](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4495)); append on a
  versioned bucket → -ERR_INVALID_BUCKET_STATE ([rgw_op.cc:4269-4272](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L4269-L4272)). `RGWCopyObj::execute`
  [rgw_op.cc:5573-5577](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5573-L5577) ([rgw_op.cc:6124](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L6124), [rgw_op.cc:6142](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L6142)): version_id → set_instance, else
  versioning_enabled → gen_rand; copy_obj gets olh_epoch and `&version_id` ([rgw_op.cc:5669-5671](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5669-L5671)).
  `RGWCompleteMultipart::execute` [rgw_op.cc:6459-6466](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L6459-L6466) ([rgw_op.cc:7288](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7288)); `upload->complete(...,
  olh_epoch, ...)` [rgw_op.cc:6482-6485](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L6482-L6485).
- O4. `RGWDeleteObj::verify_permission` [rgw_op.cc:5138-5166](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5138-L5166) ([rgw_op.cc:5528-5556](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L5528-L5556)):
  action by instance ([rgw_op.cc:5145-5147](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5145-L5147)), bypass perm, MFA ([rgw_op.cc:5158-5163](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5158-L5163)). `::execute` [rgw_op.cc:5173-5327](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5173-L5327)
  ([rgw_op.cc:5563](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L5563)): `check_obj_lock = have_instance && obj_lock_enabled` ([rgw_op.cc:5186](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5186)); `null_verid`
  ([rgw_op.cc:5187](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5187)); `get_obj_state(follow=true)` ([rgw_op.cc:5189](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5189)); ENOENT drops the lock check ([rgw_op.cc:5195-5203](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5195-L5203));
  `versioning_status`, `marker_version_id`, `olh_epoch`, `null_verid` into the delete op
  ([rgw_op.cc:5278-5288](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5278-L5288)); ECANCELED → 0 ([rgw_op.cc:5302-5304](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5302-L5304)); 412 → 0 with `no_precondition_error` ([rgw_op.cc:5305](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5305)).
  Response [rgw_rest_s3.cc:3455-3470](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L3455-L3470) ([rgw_rest_s3.cc:3735](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L3735)): ENOENT → 204; x-amz-version-id
  if nonempty; x-amz-delete-marker true.
- O5. `RGWDeleteMultiObj::handle_individual_object` [rgw_op.cc:6818-6910](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L6818-L6910)
  ([rgw_op.cc:7743](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7743)): per-key action, `check_obj_lock` as above, `versioning_status`,
  `marker_version_id` empty, FLAG_SKIP_UPDATE_OLH, ENOENT → 0, partial response with
  `result.delete_marker` and `result.version_id`. `handle_objects` [rgw_op.cc:6996-7005](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L6996-L7005) picks
  `handle_versioned_objects` when `bucket->versioned()`; that groups keys by name and skips
  update_olh for all but the last of each group ([rgw_op.cc:6912-6965](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L6912-L6965)). `::execute` MFA check
  [rgw_op.cc:7055-7068](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L7055-L7068), inverted [rgw_op.cc:7969-7983](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7969-L7983) (registry). Partial response
  [rgw_rest_s3.cc:4273-4301](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L4273-L4301) ([rgw_rest_s3.cc:4821](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4821)): Key, VersionId when named, DeleteMarker +
  DeleteMarkerVersionId when a marker was made.
- O6. `RGWListBucket::execute` [rgw_op.cc:3078-3121](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L3078-L3121) ([rgw_op.cc:3312](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L3312), which adds the
  indexless refusal [rgw_op.cc:3319-3324](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L3319-L3324)). `get_common_params` [rgw_rest_s3.cc:1710-1739](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L1710-L1739)
  (`versions`, [rgw_rest_s3.cc:1712](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L1712)); `get_params` [rgw_rest_s3.cc:1741-1754](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L1741-L1754) (key-marker/version-id-marker [rgw_rest_s3.cc:1750-1751](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L1750-L1751));
  `send_versioned_response` [rgw_rest_s3.cc:1799-1869](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L1799-L1869) (v20.2.4 [rgw_rest_s3.cc:1821](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L1821), [rgw_rest_s3.cc:1852](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L1852), [rgw_rest_s3.cc:1910](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L1910); v2 [rgw_rest_s3.cc:2081](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2081)).
- O7. GET: `RGWGetObj_ObjStore_S3::send_response_data` x-amz-version-id if nonempty
  ([rgw_rest_s3.cc:461](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L461)); PutObj [rgw_rest_s3.cc:2748](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L2748), [rgw_rest_s3.cc:2754](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L2754) ([rgw_rest_s3.cc:2891](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2891)); CompleteMultipart
  [rgw_rest_s3.cc:4081](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L4081) ([rgw_rest_s3.cc:4604](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4604)); CopyObj's responses send no version header ([rgw_rest_s3.cc:3566-3603](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L3566-L3603),
  [rgw_rest_s3.cc:3873](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L3873)). `versionId` → object key at [rgw_rest_s3.cc:4946-4955](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L4946-L4955), [rgw_rest_s3.cc:5506-5515](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5506-L5515).
- O8. v20.2.4 `read_permissions` adds x-amz-delete-marker to a GET/HEAD refused with -ENOENT:
  `load_obj_state(follow_olh=true)`, `is_delete_marker()` ([rgw_rest.cc:1936-1944](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L1936-L1944));
  `follow_olh` sets `state->is_dm` when `olh.removed` ([rgw_rados.cc:9760-9764](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9760-L9764)), copied
  at [rgw_sal_rados.cc:2942](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L2942). `read_obj_policy`'s ENOENT → ENOENT for an admin or a
  requester allowed s3:ListBucket, else EACCES ([rgw_op.cc:453-477](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L453-L477), [rgw_op.cc:423-447](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L423-L447)).
- M1. MFA chain: header read and verified in `RGWHandler_REST_S3::postauth_init`
  ([rgw_rest_s3.cc:4996-4999](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L4996-L4999), [rgw_rest_s3.cc:5556](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5556)), return value discarded; `verify_mfa`
  [rgw_sal_rados.cc:284-312](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L284-L312) ([rgw_sal_rados.cc:301](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L301)): split on " " into exactly two → else
  -EINVAL; serial must be in `mfa_ids` → else -EACCES; `check_mfa` <0 → -EACCES; else
  verified=true. `check_mfa` [svc_cls.cc:46-63](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_cls.cc#L46-L63) (same lines v20.2.4):
  oid `"user:" + uid.to_str()` in `zone_params.otp_pool` ([svc_cls.h:56-58](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_cls.h#L56-L58), [svc_cls.cc:32-44](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_cls.cc#L32-L44));
  `OTP::check` → result SUCCESS → 0 else -EACCES.
- M2. cls_otp client [cls_otp_client.cc:60-96](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/otp/cls_otp_client.cc#L60-L96): `otp_check` with
  `cls_otp_check_otp_op{id, val, token(16 random alnum)}` (v19 via `IoCtx::exec`, v20 via an
  ObjectWriteOperation, the only diff between tags), then `otp_get_result` **sent with the same
  check-op buffer `in`**, and the server's `otp_get_result` decodes a `cls_otp_check_otp_op`
  too ([cls_otp.cc:483-511](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/otp/cls_otp.cc#L483-L511)); `cls_otp_get_result_op` is dead on both sides.
  Methods: otp_set RD|WR, otp_get RD, otp_check RD|WR, otp_get_result RD, otp_remove RD|WR,
  get_current_time RD ([cls_otp.cc:557-575](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/otp/cls_otp.cc#L557-L575)). `otp_instance::check` [cls_otp.cc:108-127](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/otp/cls_otp.cc#L108-L127): at most
  ATTEMPTS_PER_WINDOW 5 checks per step window ([cls_otp.cc:43](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/otp/cls_otp.cc#L43)), else no record → UNKNOWN; `verify`
  [cls_otp.cc:129-153](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/otp/cls_otp.cc#L129-L153) (liboath totp validate2, replay index). Types `src/cls/otp/cls_otp_types.h@v19.2.6`:
  `otp_info_t` v1 (type u8, id, seed, seed_type u8, seed_bin, time_ofs i32, step_size u32,
  window u32), `otp_check_t` v1 (token, timestamp, result u8), `otp_repo_t`. Ops
  `src/cls/otp/cls_otp_ops.h@v19.2.6` all v1.
- M3. rgw-go: `meta.UserInfo.MFAIDs` exists (`internal/meta/user.go:286`);
  `ZoneParams.OTPPool` exists (`internal/meta/zone_params.go:32`, default `<zone>.rgw.otp`
  `internal/meta/zone_params.go:157`).
- S1. librados C API cmpxattr is string mode only: `rados_write_op_cmpxattr`
  [librados_c.cc:3667-3683](https://github.com/ceph/ceph/blob/v19.2.6/src/librados/librados_c.cc#L3667-L3683) (`CEPH_OSD_CMPXATTR_MODE_STRING` [librados_c.cc:3679](https://github.com/ceph/ceph/blob/v19.2.6/src/librados/librados_c.cc#L3679)),
  `rados_read_op_cmpxattr` [librados_c.cc:4148-4164](https://github.com/ceph/ceph/blob/v19.2.6/src/librados/librados_c.cc#L4148-L4164) ([librados_c.cc:4160](https://github.com/ceph/ceph/blob/v19.2.6/src/librados/librados_c.cc#L4160)). The OSD's u64 mode parses the stored xattr with
  `from_chars`, an empty xattr as 0, and compares request-value OP stored
  ([PrimaryLogPG.cc:4959-4974](https://github.com/ceph/ceph/blob/v19.2.6/src/osd/PrimaryLogPG.cc#L4959-L4974), dispatch [PrimaryLogPG.cc:6413-6444](https://github.com/ceph/ceph/blob/v19.2.6/src/osd/PrimaryLogPG.cc#L6413-L6444)). go-ceph's fork binding
  (`internal/radosclient/goceph/translate.go:163-171`) is the string-mode C call.
- S2. `radosclient.ExecResult.Bytes()` returns an error and no output when `rval < 0`
  (`internal/radosclient/results.go:16-22`); the Objecter delivers the out buffer
  with the rval, so this is the seam's choice, not librados's.
- G8. S3 routes are registered per op through `Handler.Register(name, fn)` into a map
  (`internal/s3/handler.go:100-118`); an unregistered route answers 501 (`internal/s3/handler.go:377-380`).
- G9. `internal/s3/deletemarker.go` sends x-amz-delete-marker false only on Tentacle
  and only for a BeforeVerify NoSuchKey.
- G10. s3-tests at the PIN f6f2529dd9efccf7710c8a2b561e5a2ab9d48bd3 carrying `versioning` or
  `delete_marker`: 69 tests (listed by `$TMPDIR` s3m.sh; the names are in §5 of the report).
  The Squid and Tentacle ledgers hold no outcome for them (excluded by marker).

### Third batch (pins used by the report)

- P1. v20.2.4 rgw_rados.cc `_do_write_meta`: `setxattr(RGW_ATTR_OLH_ID_TAG, state->olh_tag)` [rgw_rados.cc:3321](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L3321);
  `versioned_target` [rgw_rados.cc:3433](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L3433); `versioned_op` [rgw_rados.cc:3436](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L3436); bilog flag [rgw_rados.cc:3439](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L3439); `set_olh` call [rgw_rados.cc:3492](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L3492).
- P2. v20.2.4 rgw_common.h: OLH attrs [rgw_common.h:150-155](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.h#L150-L155); flags [rgw_common.h:1057-1061](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.h#L1057-L1061); helpers [rgw_common.h:1116-1121](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.h#L1116-L1121);
  `ERR_MFA_REQUIRED` [rgw_common.h:318](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.h#L318); `mfa_ids` [rgw_common.h:622](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.h#L622); `mfa_verified` [rgw_common.h:1420](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.h#L1420).
- P3. v20.2.4 rgw_sal_rados.cc: `verify_mfa` [rgw_sal_rados.cc:301](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L301); `RadosBucket::list` [rgw_sal_rados.cc:897](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L897);
  `load_obj_state` [rgw_sal_rados.cc:2935](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L2935) (is_dm copy [rgw_sal_rados.cc:2942](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L2942)); `set_obj_attrs` [rgw_sal_rados.cc:2971](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L2971); `modify_obj_attrs`
  [rgw_sal_rados.cc:2994](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L2994); `gen_rand_obj_instance_name` [rgw_sal_rados.cc:3041](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L3041); `RadosReadOp::prepare` [rgw_sal_rados.cc:3678](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L3678);
  `RadosDeleteOp::delete_obj` [rgw_sal_rados.cc:3731](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L3731); `delete_object` [rgw_sal_rados.cc:3766](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L3766).
- P4. v20.2.4 rgw_rados.cc: `delete_obj` versioned branch [rgw_rados.cc:6445-6577](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6445-L6577) (marker-path preconditions
  [rgw_rados.cc:6494-6509](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6494-L6509), instance-path [rgw_rados.cc:6520-6554](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6520-L6554), unlink [rgw_rados.cc:6556-6560](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6556-L6560)); `RGWRados::delete_obj` [rgw_rados.cc:6743](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6743);
  `list_objects_ordered` [rgw_rados.cc:1905](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L1905); `cls_bucket_list_ordered` [rgw_rados.cc:10535](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L10535); `check_disk_state` [rgw_rados.cc:11247](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L11247);
  `set_attrs` [rgw_rados.cc:7393](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7393); `Read::prepare` [rgw_rados.cc:7686](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7686); `guard_reshard` [rgw_rados.cc:8575](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L8575); OLH functions
  [rgw_rados.cc:8417-9770](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L8417-L9770) as listed in C20/R-notes; `get_olh` reads OLH_INFO at [rgw_rados.cc:9640](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9640) (the diff hunk
  `@@ -220,12 +220,12 @@` of the set_olh..follow_olh range, i.e. 9415+225).
- P5. `rgw_debug_inject_set_olh_err` option [rgw.yaml.in:2886](https://github.com/ceph/ceph/blob/v20.2.4/src/common/options/rgw.yaml.in#L2886);
  `rgw_olh_pending_timeout_sec` default 1_hr [rgw.yaml.in:2383-2393](https://github.com/ceph/ceph/blob/v19.2.6/src/common/options/rgw.yaml.in#L2383-L2393), [rgw.yaml.in:2483](https://github.com/ceph/ceph/blob/v20.2.4/src/common/options/rgw.yaml.in#L2483).
- P6. `svc_cls.cc` MFA lines identical at v20.2.4 ([svc_cls.cc:32](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/services/svc_cls.cc#L32), [svc_cls.cc:46](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/services/svc_cls.cc#L46), [svc_cls.cc:56](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/services/svc_cls.cc#L56)).
- P7. s3m.sh (the marker inventory) and the cs/cg/gs/gg/fd helpers lived in `$TMPDIR`
  (`/tmp/claude/rv`), removed at the end of the run.
