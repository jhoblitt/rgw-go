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
  instance, "1001_" olh data, "9999_" last. `src/cls/rgw/cls_rgw.cc:51-66@v19.2.6`. v20.2.4 adds
  "2001_" reshard log (`BI_BUCKET_RESHARD_LOG_INDEX` 4, LAST 5): diff hunk @@ -48,30 +48,32 @@,
  so `src/cls/rgw/cls_rgw.cc:51-70@v20.2.4` (to pin exactly).
- C2. List index key of a versioned instance: `name + "\0v" + decreasing_str(versioned_epoch) +
  "\0i" + instance` (`get_list_index_key`, `src/cls/rgw/cls_rgw.cc:302-315@v19.2.6`). `cls_rgw_after_versions` = name +
  "\1" (`src/cls/rgw/cls_rgw.cc:319-323@v19.2.6`). Instance entry key: 0x80 "1000_" name "\0i" instance, plus "\0d" for a
  null-instance delete marker (`encode_obj_versioned_data_key`, `src/cls/rgw/cls_rgw.cc:325-337@v19.2.6`). Plain entry key = name
  when instance empty, else the instance key (`encode_obj_index_key`, `src/cls/rgw/cls_rgw.cc:339-346@v19.2.6`). OLH data key:
  0x80 "1001_" name (`encode_olh_data_key`, `src/cls/rgw/cls_rgw.cc:348-353@v19.2.6`).
- C3. `encode_list_index_key` (`src/cls/rgw/cls_rgw.cc:358-390@v19.2.6`): instance "null" is cleared before lookup; a
  missing instance entry yields `name + "\x01"` as the start-after key.
- C4. `decode_list_index_key` (`src/cls/rgw/cls_rgw.cc:417-462@v19.2.6`): splits on NUL; "i<instance>", "v<ver>".
- C5. `rgw_bucket_dir_entry` flags FLAG_VER 0x1, FLAG_CURRENT 0x2, FLAG_DELETE_MARKER 0x4,
  FLAG_VER_MARKER 0x8, FLAG_COMMON_PREFIX 0x8000; encode v8 compat 3 (…, tag, key.instance, flags,
  versioned_epoch); `is_current` = !FLAG_VER || (VER|CURRENT); `is_visible` = current && !DM;
  `is_valid` = !VER_MARKER. `src/cls/rgw/cls_rgw_types.h:369-463@v19.2.6`. rgw-go already has these
  flags (`internal/cls/rgw/const.go`, FlagVer..FlagCommonPrefix).
- C6. `OLHLogOp` UNKNOWN 0, LINK_OLH 1, UNLINK_OLH 2, REMOVE_INSTANCE 3; `rgw_bucket_olh_log_entry`
  v1 (epoch u64, op u8, op_tag, key, delete_marker); `rgw_bucket_olh_entry` v1 (key,
  delete_marker, epoch, pending_log map<u64, vector<log_entry>>, tag, exists, pending_removal).
  `src/cls/rgw/cls_rgw_types.h:513-593@v19.2.6`.
- C7. Request structs `src/cls/rgw/cls_rgw_ops.h@v19.2.6`: `rgw_cls_link_olh_op` v5 compat 1 (key,
  olh_tag, delete_marker, op_tag, meta, olh_epoch, log_op, bilog_flags, u64 time_t(unmod_since),
  unmod_since, high_precision_time, zones_trace) `src/cls/rgw/cls_rgw_ops.h:167-232@v19.2.6`; `rgw_cls_unlink_instance_op` v3 (key,
  op_tag, olh_epoch, log_op, bilog_flags, olh_tag, zones_trace) `src/cls/rgw/cls_rgw_ops.h:234-276@v19.2.6`;
  `rgw_cls_read_olh_log_op` v1 (olh, ver_marker, olh_tag) `src/cls/rgw/cls_rgw_ops.h:278-303@v19.2.6`; `rgw_cls_read_olh_log_ret` v1
  (log, is_truncated) `src/cls/rgw/cls_rgw_ops.h:306-328@v19.2.6`; `rgw_cls_trim_olh_log_op` v1 (olh, ver, olh_tag) `src/cls/rgw/cls_rgw_ops.h:330-355@v19.2.6`;
  `rgw_cls_bucket_clear_olh_op` v1 (key, olh_tag) `src/cls/rgw/cls_rgw_ops.h:357-380@v19.2.6`. The v19.2.6→v20.2.4 diff of
  cls_rgw_ops.h touches none of these six structs (it changes tag_timeout_op, prepare_op defaults,
  update_stats v2, bi_put_entries, bi_list v2, reshard_add v2). cls_rgw_types.h diff: dir_entry
  and olh types unchanged; adds BIIndexType::ReshardDeleted, rgw_bucket_deleted_entry,
  reshardlog_entries in the dir header (v8), IN_LOGRECORD status.
- C8. Class-side helpers `src/cls/rgw/cls_rgw.cc@v19.2.6`: `read_olh` `src/cls/rgw/cls_rgw.cc:1251-1266@v19.2.6`;
  `update_olh_log` `src/cls/rgw/cls_rgw.cc:1268-1279@v19.2.6` (appends to pending_log[olh epoch]); `BIVerObjEntry` `src/cls/rgw/cls_rgw.cc:1318-1478@v19.2.6`:
  `init(check_delete_marker)` reads the instance entry (for a null instance the "\0d" DM entry is
  separate), `init_as_delete_marker` sets FLAG_DELETE_MARKER and tag "delete-marker" (`src/cls/rgw/cls_rgw.cc:1350-1358@v19.2.6`),
  `write(epoch, current)` removes the old list entry when versioned_epoch>0 then writes FLAG_VER
  (+CURRENT) (`src/cls/rgw/cls_rgw.cc:1410-1427@v19.2.6`), `demote_current` clears CURRENT (`src/cls/rgw/cls_rgw.cc:1429-1431@v19.2.6`), `find_next_key` lists
  one key after this instance's list key filtered by name (`src/cls/rgw/cls_rgw.cc:1437-1473@v19.2.6`). `BIOLHEntry` `src/cls/rgw/cls_rgw.cc:1481-1566@v19.2.6`:
  `start_modify(candidate)`: nonzero candidate < olh.epoch → false, else epoch=candidate; zero →
  epoch 0 becomes 2 ("1 is reserved to converted plain entries"), else ++ (`src/cls/rgw/cls_rgw.cc:1503-1517@v19.2.6`).
  `write_version_marker` writes a FLAG_VER_MARKER entry at the plain name (`src/cls/rgw/cls_rgw.cc:1568-1579@v19.2.6`).
  `convert_plain_entry_to_versioned` (`src/cls/rgw/cls_rgw.cc:1587-1634@v19.2.6`): the plain entry gets versioned_epoch 1 and
  FLAG_VER, CURRENT cleared if demote, written at the instance key (and list key unless
  instance_only), then the version marker.
- C9. `rgw_bucket_link_olh` `src/cls/rgw/cls_rgw.cc:1652-1865@v19.2.6`. Order: decode; `obj.init(op.delete_marker)`;
  ENOENT tolerated only for a delete marker (`src/cls/rgw/cls_rgw.cc:1682-1688@v19.2.6`); unmod_since check returns 0 with no
  change when mtime >= unmod (`src/cls/rgw/cls_rgw.cc:1690-1701@v19.2.6`); null-instance special case unlinks the other null entry
  (`src/cls/rgw/cls_rgw.cc:1712-1734@v19.2.6`); read olh; `start_modify(op.olh_epoch)` false → write instance non-current (+log
  REMOVE_INSTANCE if removing) and return 0 (`src/cls/rgw/cls_rgw.cc:1751-1760@v19.2.6`); promote = newer epoch, or same epoch and
  olh.key.instance >= op.key.instance (`src/cls/rgw/cls_rgw.cc:1764-1766@v19.2.6`); olh found and `op.olh_tag != olh.tag` and not
  pending_removal → **-ECANCELED** (`src/cls/rgw/cls_rgw.cc:1768-1777@v19.2.6`); promote demotes the previous current
  (`src/cls/rgw/cls_rgw.cc:1778-1790@v19.2.6`); olh not found → convert plain entry, set tag, null instance gets epoch 1
  (`src/cls/rgw/cls_rgw.cc:1792-1804@v19.2.6`); log LINK_OLH (+REMOVE_INSTANCE) (`src/cls/rgw/cls_rgw.cc:1806-1810@v19.2.6`); update olh key/dm if promote; write
  olh; write instance (current = promote) (`src/cls/rgw/cls_rgw.cc:1812-1827@v19.2.6`); bilog only if `op.log_op` and not
  syncstopped, with `op.bilog_flags | RGW_BILOG_FLAG_VERSIONED_OP` (`src/cls/rgw/cls_rgw.cc:1829-1864@v19.2.6`).
- C10. `rgw_bucket_unlink_instance` `src/cls/rgw/cls_rgw.cc:1867-2018@v19.2.6`: instance entry missing → **-ENOENT**
  (`src/cls/rgw/cls_rgw.cc:1888-1894@v19.2.6`); no olh entry → convert plain entry (demote), olh.update(dest_key,false), tag,
  epoch 1 (`src/cls/rgw/cls_rgw.cc:1903-1915@v19.2.6`); old epoch → unlink list entry; delete marker → return 0, else log
  REMOVE_INSTANCE and write (`src/cls/rgw/cls_rgw.cc:1917-1929@v19.2.6`); unlinking the current head: `find_next_key`; found →
  next written current and LINK_OLH logged; none → UNLINK_OLH logged, exists=false,
  pending_removal=true, key name kept for hash placement (`src/cls/rgw/cls_rgw.cc:1936-1968@v19.2.6`); a delete marker's instance
  entry is removed by the class, a data instance logs REMOVE_INSTANCE (`src/cls/rgw/cls_rgw.cc:1970-1979@v19.2.6`); list entry
  removed; olh written; bilog as link (`src/cls/rgw/cls_rgw.cc:1991-2017@v19.2.6`). Note: no olh-tag check in unlink_instance.
- C11. `rgw_bucket_read_olh_log` `src/cls/rgw/cls_rgw.cc:2020-2072@v19.2.6`: -EINVAL for a non-empty instance;
  -ECANCELED when `olh_data_entry.tag != op.olh_tag` (a missing entry has tag "" so any nonempty
  tag is ECANCELED); MAX_OLH_LOG_ENTRIES 1000; returns entries with epoch > ver_marker,
  is_truncated when more. Registered CLS_METHOD_RD (`src/cls/rgw/cls_rgw.cc:4701@v19.2.6`).
- C12. `rgw_bucket_trim_olh_log` `src/cls/rgw/cls_rgw.cc:2074-2124@v19.2.6`: -EINVAL non-empty instance; -ECANCELED tag
  mismatch; erases pending_log keys <= op.ver; writes the olh entry.
- C13. `rgw_bucket_clear_olh` `src/cls/rgw/cls_rgw.cc:2126-2190@v19.2.6`: -EINVAL non-empty instance; -ECANCELED tag
  mismatch; removes the olh data key; removes the plain entry only if FLAG_VER_MARKER.
- C14. `rgw_bucket_list` `src/cls/rgw/cls_rgw.cc:487-694@v19.2.6`: VER_MARKER entries skipped as invalid (`src/cls/rgw/cls_rgw.cc:604-608@v19.2.6`);
  with `!list_versions`, a non-visible entry or one named like start_obj skips to
  `cls_rgw_after_versions(name)` (`src/cls/rgw/cls_rgw.cc:611-623@v19.2.6`); returns `RGWBIAdvanceAndRetryError` (-EFBIG) when
  truncated with nothing listed (`src/cls/rgw/cls_rgw.cc:688-691@v19.2.6`); `max_attempts` 8 (`src/cls/rgw/cls_rgw.cc:492@v19.2.6`).
- C15. `rgw_bucket_complete_op` resets entry flags to FLAG_VER only (`src/cls/rgw/cls_rgw.cc:1061-1064@v19.2.6`).
- C16. Method registration: LINK_OLH, UNLINK_INSTANCE RD|WR; READ_OLH_LOG RD; TRIM_OLH_LOG,
  CLEAR_OLH RD|WR: `src/cls/rgw/cls_rgw.cc:4699-4703@v19.2.6`, `src/cls/rgw/cls_rgw.cc:5116-5120@v20.2.4`. Names
  `src/cls/rgw/cls_rgw_const.h:24-28@v19.2.6` (bucket_link_olh, bucket_unlink_instance,
  bucket_read_olh_log, bucket_trim_olh_log, bucket_clear_olh).
- C17. Client builders `src/cls/rgw/cls_rgw_client.cc@v19.2.6`: link `src/cls/rgw/cls_rgw_client.cc:513-534@v19.2.6` (olh_tag is the
  xattr bufferlist as a string, `olh_tag.to_str()`); unlink `src/cls/rgw/cls_rgw_client.cc:550-566@v19.2.6`; read log as a read-op exec
  with a decoding completion `src/cls/rgw/cls_rgw_client.cc:568-577@v19.2.6`; trim `src/cls/rgw/cls_rgw_client.cc:597-606@v19.2.6`; clear `src/cls/rgw/cls_rgw_client.cc:616-624@v19.2.6`.

### radosgw: the gateway side (src/rgw/driver/rados/rgw_rados.cc@v19.2.6)

- R1. `_do_write_meta` `src/rgw/driver/rados/rgw_rados.cc:3124-3417@v19.2.6`: olh head keeps its OLH_ID_TAG (`src/rgw/driver/rados/rgw_rados.cc:3191-3193@v19.2.6`);
  `versioned_target = (olh_epoch && *olh_epoch > 0) || !instance.empty()`; `versioned_op =
  versioning_enabled() || is_olh || versioned_target` sets bilog VERSIONED_OP (`src/rgw/driver/rados/rgw_rados.cc:3280-3287@v19.2.6`);
  after the index complete, `versioned_op && meta.olh_epoch` → `set_olh(..., obj, false, NULL,
  *meta.olh_epoch, real_time(), false, ..., add_log)` (`src/rgw/driver/rados/rgw_rados.cc:3337-3343@v19.2.6`).
- R2. `Object::Delete::delete_obj` `src/rgw/driver/rados/rgw_rados.cc:5757-5987@v19.2.6`: "null" instance cleared (`src/rgw/driver/rados/rgw_rados.cc:5768-5770@v19.2.6`); versioned
  branch when `versioning_status & BUCKET_VERSIONED || marker_version_id set` (`src/rgw/driver/rados/rgw_rados.cc:5774@v19.2.6`); no
  instance → marker key: random instance unless `BUCKET_VERSIONS_SUSPENDED` (`src/rgw/driver/rados/rgw_rados.cc:5785-5787@v19.2.6`);
  result.version_id "null" when empty, delete_marker true (`src/rgw/driver/rados/rgw_rados.cc:5789-5793@v19.2.6`); meta owner/display/mtime;
  `set_olh(..., marker, true, &meta, olh_epoch, unmod_since, high_precision_time, ...)`
  (`src/rgw/driver/rados/rgw_rados.cc:5806-5808@v19.2.6`); with an instance → `bi_get_instance` then `unlink_obj_instance`,
  result.delete_marker = dirent.is_delete_marker(), version_id = instance (`src/rgw/driver/rados/rgw_rados.cc:5813-5828@v19.2.6`); datalog
  entry when add_log (`src/rgw/driver/rados/rgw_rados.cc:5837-5840@v19.2.6`). Unversioned path `src/rgw/driver/rados/rgw_rados.cc:5845-5987@v19.2.6`.
- R3. `is_olh(attrs)` = RGW_ATTR_OLH_VER present (`src/rgw/driver/rados/rgw_rados.cc:6086-6090@v19.2.6`); `has_olh_tag` = OLH_ID_TAG
  present (`src/rgw/driver/rados/rgw_rados.cc:6092-6096@v19.2.6`); `get_olh_target_state` follows and loads the target (`src/rgw/driver/rados/rgw_rados.cc:6098-6118@v19.2.6`).
- R4. `get_obj_state_impl` `src/rgw/driver/rados/rgw_rados.cc:6120-6297@v19.2.6`: `need_follow_olh = follow_olh && instance.empty()`
  (`src/rgw/driver/rados/rgw_rados.cc:6129@v19.2.6`); olh_tag from OLH_ID_TAG whether or not olh (`src/rgw/driver/rados/rgw_rados.cc:6278-6280@v19.2.6`); is_olh → follow when
  needed; "null" instance on an olh head without a manifest → exists=false, -ENOENT
  (`src/rgw/driver/rados/rgw_rados.cc:6289-6293@v19.2.6`). `get_obj_state` retries on -EAGAIN (`src/rgw/driver/rados/rgw_rados.cc:6299-6312@v19.2.6`).
- R5. `olh_cancel_modification` `src/rgw/driver/rados/rgw_rados.cc:7562-7605@v19.2.6`: guarded rmxattr of `user.rgw.olh.pending.<op_tag>`;
  then, if the state has no OLH_INFO, a guarded remove with `cmpxattr(OLH_INFO, EQ, "")`,
  `cls_obj_check_prefix_exist(pending prefix, fail_if_exist)`, `remove()`.
- R6. `olh_init_modification_impl` `src/rgw/driver/rados/rgw_rados.cc:7607-7706@v19.2.6`: olh key must have no instance; `!exists` →
  `create(exclusive)`, else `assert_exists` + `mtime2(state.mtime)`; has olh tag → guard
  `cmpxattr(OLH_ID_TAG, EQ, tag)`; exists without tag (a plain head becoming an olh) →
  `cmpxattr(OLH_ID_TAG, EQ, "")` and `setxattr(OLH_INFO, {target=olh_obj, removed=false})`
  (`src/rgw/driver/rados/rgw_rados.cc:7636-7648@v19.2.6`); no tag → new idtag (32 lower alnum) and olh idtag (32), empty OLH_VER
  (`src/rgw/driver/rados/rgw_rados.cc:7650-7674@v19.2.6`); pending attr `user.rgw.olh.pending.<op_tag>` with op_tag = `%016llx` of the
  second + random lower alnum to 32 chars, value `RGWOLHPendingInfo{time}` (`src/rgw/driver/rados/rgw_rados.cc:7676-7695@v19.2.6`). The
  wrapper maps -EEXIST to -ECANCELED (`src/rgw/driver/rados/rgw_rados.cc:7708-7718@v19.2.6`).
- R7. `bucket_index_link_olh` `src/rgw/driver/rados/rgw_rados.cc:7934-7980@v19.2.6`: `guard_reshard` around a write op of `assert_exists`,
  `cls_rgw_guard_bucket_resharding(-ERR_BUSY_RESHARDING)`, `cls_rgw_bucket_link_olh(key =
  (get_index_key_name, instance), olh_state.olh_tag, ...)`; datalog when log_data_change.
  `bucket_index_guard_olh_op` = `cmpxattr(OLH_ID_TAG, EQ, olh_tag)` (`src/rgw/driver/rados/rgw_rados.cc:7982-7986@v19.2.6`).
  `bucket_index_unlink_instance` `src/rgw/driver/rados/rgw_rados.cc:7988-8027@v19.2.6` same guard shape.
- R8. `bucket_index_read_olh_log` `src/rgw/driver/rados/rgw_rados.cc:8029-8073@v19.2.6`: a read op, `bs.init(bucket, obj, nullptr)`, no
  reshard guard; key has an empty instance; olh_tag from state. `repair_olh` `src/rgw/driver/rados/rgw_rados.cc:8079-8128@v19.2.6` rewrites
  OLH_ID_TAG and OLH_INFO from `bi_get_olh` under the old tag's guard (tracker #37792).
  `bucket_index_trim_olh_log` `src/rgw/driver/rados/rgw_rados.cc:8130-8167@v19.2.6` and `bucket_index_clear_olh` `src/rgw/driver/rados/rgw_rados.cc:8169-8199@v19.2.6` are guarded
  writes.
- R9. `apply_olh_log` `src/rgw/driver/rados/rgw_rados.cc:8213-8370@v19.2.6`: write op on the olh head: `cmpxattr(OLH_ID_TAG, EQ, tag)`,
  `cmpxattr(OLH_VER, GTE, last_ver)` (u64 mode), `setxattr(OLH_VER, decimal string)`,
  `mtime2(state.mtime)` (`src/rgw/driver/rados/rgw_rados.cc:8238-8247@v19.2.6`); current epoch and target decoded from OLH_VER / OLH_INFO
  (`src/rgw/driver/rados/rgw_rados.cc:8256-8272@v19.2.6`); replay: REMOVE_INSTANCE collects keys; LINK_OLH applies when `link_epoch <
  epoch || key.instance.empty() || key.instance > entry.key.instance` (`src/rgw/driver/rados/rgw_rados.cc:8286-8300@v19.2.6`); UNLINK_OLH
  sets need_to_remove (`src/rgw/driver/rados/rgw_rados.cc:8301-8304@v19.2.6`); every entry's pending attr is rmxattr'd (`src/rgw/driver/rados/rgw_rados.cc:8309-8311@v19.2.6`);
  OLH_INFO set when need_to_link (`src/rgw/driver/rados/rgw_rados.cc:8323-8331@v19.2.6`); removed instances deleted through `delete_obj(...,
  versioning_status 0, null_verid, RGW_BILOG_FLAG_VERSIONED_OP, ..., skip_olh_obj_update=true)`,
  ENOENT ignored (`src/rgw/driver/rados/rgw_rados.cc:8334-8345@v19.2.6`); then the head op; then `clear_olh` if need_to_remove else
  `bucket_index_trim_olh_log(last_ver)`, -ECANCELED tolerated (`src/rgw/driver/rados/rgw_rados.cc:8354-8367@v19.2.6`).
- R10. `clear_olh` (ref form) `src/rgw/driver/rados/rgw_rados.cc:8387-8438@v19.2.6`: reads state without following; expires pending attrs
  (`check_pending_olh_entries`, `remove_olh_pending_entries`); rm op: `cmpxattr(OLH_ID_TAG, EQ,
  tag)`, `cmpxattr(OLH_VER, EQ, ver)`, `cls_obj_check_prefix_exist(pending, fail_if_exist)`,
  `remove()`; -ECANCELED returned as is; then `bucket_index_clear_olh`.
- R11. `update_olh` `src/rgw/driver/rados/rgw_rados.cc:8443-8471@v19.2.6`: read log from ver_marker, apply, loop while truncated.
- R12. `set_olh` `src/rgw/driver/rados/rgw_rados.cc:8473-8566@v19.2.6`: MAX_ECANCELED_RETRY 100; per try: invalidate on ECANCELED,
  `get_obj_state(olh_obj, follow=false)`, `olh_init_modification`, `bucket_index_link_olh`; on link
  failure `olh_cancel_modification`, on -ECANCELED `repair_olh` then retry, other errors a
  best-effort `update_olh` unless skip (`src/rgw/driver/rados/rgw_rados.cc:8519-8541@v19.2.6`); after the link `update_olh`, -ECANCELED → 0
  (`src/rgw/driver/rados/rgw_rados.cc:8556-8559@v19.2.6`). `unlink_obj_instance` `src/rgw/driver/rados/rgw_rados.cc:8568-8670@v19.2.6` mirrors it with bilog flags VERSIONED_OP (+
  NULL_VERSION when null_verid) (`src/rgw/driver/rados/rgw_rados.cc:8618-8622@v19.2.6`).
- R13. `gen_rand_obj_instance_name`: 32 random alphanumerics, no underscore (`src/rgw/driver/rados/rgw_rados.cc:8672-8681@v19.2.6`).
- R14. `get_olh` `src/rgw/driver/rados/rgw_rados.cc:8688-8706@v19.2.6` decodes RGW_ATTR_OLH_VER as an RGWOLHInfo (looks wrong; check
  callers before recording). `check_pending_olh_entries` `src/rgw/driver/rados/rgw_rados.cc:8708-8738@v19.2.6`: expire by
  `rgw_olh_pending_timeout_sec`, stop at the first unexpired (names sort by time).
  `remove_olh_pending_entries` `src/rgw/driver/rados/rgw_rados.cc:8740-8771@v19.2.6`: guarded rmxattr in batches of 1000; ENOENT/ECANCELED
  → 0.
- R15. `follow_olh` `src/rgw/driver/rados/rgw_rados.cc:8773-8825@v19.2.6`: expire pending; if any pending remain → `update_olh` (-ECANCELED
  → -ENOENT) (`src/rgw/driver/rados/rgw_rados.cc:8788-8801@v19.2.6`); no OLH_VER → -EINVAL; no OLH_INFO → -ENOENT; `removed` → -ENOENT;
  else target.

### radosgw: attrs, flags, MFA (v19.2.6)

- A1. `RGW_ATTR_OLH_PREFIX` "user.rgw.olh." `src/rgw/rgw_common.h:131@v19.2.6`; OLH_INFO `src/rgw/rgw_common.h:133@v19.2.6`;
  OLH_VER `src/rgw/rgw_common.h:134@v19.2.6`; OLH_ID_TAG `src/rgw/rgw_common.h:135@v19.2.6`; OLH_PENDING_PREFIX "user.rgw.olh.pending." `src/rgw/rgw_common.h:136@v19.2.6`. rgw-go has
  the first four (`internal/meta/attrs.go:27-30`) and lacks the pending prefix.
- A2. Flags BUCKET_VERSIONED 0x2 `src/rgw/rgw_common.h:1022@v19.2.6`, BUCKET_VERSIONS_SUSPENDED 0x4 `src/rgw/rgw_common.h:1023@v19.2.6`, BUCKET_MFA_ENABLED
  0x10 `src/rgw/rgw_common.h:1025@v19.2.6`, BUCKET_OBJ_LOCK_ENABLED 0x20 `src/rgw/rgw_common.h:1026@v19.2.6`; `versioned()` `src/rgw/rgw_common.h:1074@v19.2.6`; `versioning_status()`
  masks VERSIONED|SUSPENDED|MFA `src/rgw/rgw_common.h:1075@v19.2.6`; `versioning_enabled()` `src/rgw/rgw_common.h:1076@v19.2.6`; `mfa_enabled()` `src/rgw/rgw_common.h:1077@v19.2.6`;
  `obj_lock_enabled()` `src/rgw/rgw_common.h:1079@v19.2.6`. rgw-go: `internal/meta/bucket_info.go:16-23`.
- A3. `RGWOLHInfo` `src/rgw/driver/rados/rgw_rados.h:94@v19.2.6`, `RGWOLHPendingInfo` `src/rgw/driver/rados/rgw_rados.h:118@v19.2.6`
  (encodings to read). `RGWObjState::is_olh`, `olh_tag` `src/rgw/rgw_sal.h:104-105@v19.2.6`.
- A4. MFA: `RadosUser::verify_mfa` `src/rgw/driver/rados/rgw_sal_rados.cc:284-310@v19.2.6`: the
  header is split on " " into serial and pin (invalid string → NOTICE, not an error), the user's
  `mfa_ids` must hold the serial, then `svc.cls->mfa.check_mfa(user_id, serial, pin)`. Header read
  at `src/rgw/rgw_rest_s3.cc:4996-4998@v19.2.6` (`HTTP_X_AMZ_MFA`, into `s->mfa_verified`).
  `ERR_MFA_REQUIRED` 2044 → 403 AccessDenied (`src/rgw/rgw_common.cc:96@v19.2.6`,
  `src/rgw/rgw_common.h:296@v19.2.6`). Delete checks `src/rgw/rgw_op.cc:5158-5162@v19.2.6` and `src/rgw/rgw_op.cc:7055-7065@v19.2.6`; SetBucketVersioning
  MFA logic `src/rgw/rgw_op.cc:2795-2835@v19.2.6`.

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

- C18. `decreasing_str` `src/cls/rgw/cls_rgw.cc:272-290@v19.2.6`, `src/cls/rgw/cls_rgw.cc:322@v20.2.4`: a leading digit
  9..4 by magnitude band, then the zero-padded complement, so larger epochs sort first.
- C19. v20.2.4 class-side reshard guard: `guard_bucket_resharding` `src/cls/rgw/cls_rgw.cc:906-922@v20.2.4` (busy when
  IN_PROGRESS, or IN_LOGRECORD with `reshardlog_entries >= rgw_reshardlog_threshold`, default
  30000, `src/common/options/rgw.yaml.in:2875-2879@v20.2.4`); called first in link_olh
  (`src/cls/rgw/cls_rgw.cc:1797-1808@v20.2.4`), unlink_instance (`src/cls/rgw/cls_rgw.cc:2020-2029@v20.2.4`), trim (`src/cls/rgw/cls_rgw.cc:2257-2266@v20.2.4`), clear (`src/cls/rgw/cls_rgw.cc:2297-2306@v20.2.4`),
  prepare, complete, suggest, rebuild, bi_put_entries; not in read_olh_log. Every entry write goes
  through `write_entry(..., header)`/`remove_entry` which mirror into the reshard log while
  IN_LOGRECORD (`src/cls/rgw/cls_rgw.cc:752-786@v20.2.4`).
- C20. v20.2.4 line numbers in src/cls/rgw/cls_rgw.cc: prefixes `src/cls/rgw/cls_rgw.cc:51-70@v20.2.4`; get_list_index_key `src/cls/rgw/cls_rgw.cc:352@v20.2.4`; after_versions `src/cls/rgw/cls_rgw.cc:369@v20.2.4`;
  versioned data key `src/cls/rgw/cls_rgw.cc:375@v20.2.4`; obj_index_key `src/cls/rgw/cls_rgw.cc:389@v20.2.4`; olh_data_key `src/cls/rgw/cls_rgw.cc:398@v20.2.4`; encode_list_index_key
  `src/cls/rgw/cls_rgw.cc:408@v20.2.4`; decode_list_index_key `src/cls/rgw/cls_rgw.cc:467@v20.2.4`; bucket_list `src/cls/rgw/cls_rgw.cc:537@v20.2.4`; read_olh `src/cls/rgw/cls_rgw.cc:1374@v20.2.4`; BIVerObjEntry `src/cls/rgw/cls_rgw.cc:1444@v20.2.4`;
  BIOLHEntry `src/cls/rgw/cls_rgw.cc:1608@v20.2.4` (epoch 2 at `src/cls/rgw/cls_rgw.cc:1638@v20.2.4`); write_version_marker `src/cls/rgw/cls_rgw.cc:1695@v20.2.4`; convert `src/cls/rgw/cls_rgw.cc:1715@v20.2.4`; link
  `src/cls/rgw/cls_rgw.cc:1781@v20.2.4` (ECANCELED `src/cls/rgw/cls_rgw.cc:1914@v20.2.4`); unlink `src/cls/rgw/cls_rgw.cc:2002@v20.2.4`; read log `src/cls/rgw/cls_rgw.cc:2161@v20.2.4` (ECANCELED `src/cls/rgw/cls_rgw.cc:2190@v20.2.4`, MAX 1000
  `src/cls/rgw/cls_rgw.cc:2195@v20.2.4`); trim `src/cls/rgw/cls_rgw.cc:2215@v20.2.4` (ECANCELED `src/cls/rgw/cls_rgw.cc:2245@v20.2.4`); clear `src/cls/rgw/cls_rgw.cc:2279@v20.2.4` (ECANCELED `src/cls/rgw/cls_rgw.cc:2321@v20.2.4`). cls_rgw_ops.h
  olh structs `src/cls/rgw/cls_rgw_ops.h:165-378@v20.2.4`; cls_rgw_types.h dir_entry `src/cls/rgw/cls_rgw_types.h:369-469@v20.2.4`, OLHLogOp `src/cls/rgw/cls_rgw_types.h:512@v20.2.4`,
  olh_entry ends `src/cls/rgw/cls_rgw_types.h:592@v20.2.4`. Client builders `src/cls/rgw/cls_rgw_client.cc:363-487@v20.2.4`.
- C21. `complete_op` ADD branch writes only `idx` (the key `read_key_entry` resolved), sets
  exists/tag/meta and accounts stats (`src/cls/rgw/cls_rgw.cc:1168-1197@v19.2.6`); DEL branch `src/cls/rgw/cls_rgw.cc:1127-1167@v19.2.6`. The list
  entry of a version is written only by link_olh/unlink_instance.
- C22. `bi_get_instance` `src/rgw/driver/rados/rgw_rados.cc:9247-9267@v19.2.6` (`src/rgw/driver/rados/rgw_rados.cc:10192@v20.2.4`) → `bi_get(...,
  BIIndexType::Instance)` `src/rgw/driver/rados/rgw_rados.cc:9291@v19.2.6`; `bi_get_olh` `src/rgw/driver/rados/rgw_rados.cc:9269-9289@v19.2.6` (`src/rgw/driver/rados/rgw_rados.cc:10214@v20.2.4`).
- R16. `prepare_atomic_modification` `src/rgw/driver/rados/rgw_rados.cc:6484-6576@v19.2.6`: Squid's guard condition
  includes `if_match || if_nomatch` and the conditions are evaluated against `state` = the
  object being written (`src/rgw/driver/rados/rgw_rados.cc:6493-6543@v19.2.6`); for a versioned write that is the new, nonexistent
  instance. reset_obj with an existing head: `create(false)` + `remove_rgw_head_obj` (keeps
  olh.* attrs) `src/rgw/driver/rados/rgw_rados.cc:6546-6553@v19.2.6`. v20.2.4 `src/rgw/driver/rados/rgw_rados.cc:7331-7360@v20.2.4`: no conditions here; `set_attr_id_tag`
  decides the guard.
- R17. v20.2.4 `_do_write_meta` `src/rgw/driver/rados/rgw_rados.cc:3282-3300@v20.2.4`: for a key with an instance,
  `get_current_version_state` (follows the OLH, `src/rgw/driver/rados/rgw_rados.cc:7238-7258@v20.2.4`) supplies the state
  `check_preconditions` (`src/rgw/driver/rados/rgw_rados.cc:7260-7329@v20.2.4`) evaluates; `set_attr_id_tag = guard && instance.empty()
  && if_nomatch != "*"` so instance writes carry no id-tag cmpxattr.
- R18. v20.2.4 versioned delete preconditions: marker path checks against the current version
  (`src/rgw/driver/rados/rgw_rados.cc:6494-6509@v20.2.4`), instance path against the dirent from `bi_get_instance` (`src/rgw/driver/rados/rgw_rados.cc:6520-6554@v20.2.4`); then
  `unlink_obj_instance` (`src/rgw/driver/rados/rgw_rados.cc:6556-6560@v20.2.4`).
- R19. `get_system_versioning_params` `src/rgw/rgw_op.h:2113-2139@v19.2.6`: non-system → return
  0 leaving olh_epoch 0 and version_id empty. `AtomicObjectProcessor::complete` passes the
  engaged optional into `obj_op.meta.olh_epoch`
  (`src/rgw/driver/rados/rgw_putobj_processor.cc:385@v19.2.6`; `olh_epoch` is
  `std::optional<uint64_t>`, `src/rgw/driver/rados/rgw_putobj_processor.h:171@v19.2.6`), and sets
  `set_versioning_disabled(!versioning_enabled())` (`src/rgw/driver/rados/rgw_putobj_processor.cc:372@v19.2.6`).
- R20. `set_attrs` `src/rgw/driver/rados/rgw_rados.cc:6593-6757@v19.2.6` (`src/rgw/driver/rados/rgw_rados.cc:7393@v20.2.4`): "null" instance cleared
  (`src/rgw/driver/rados/rgw_rados.cc:6601-6603@v19.2.6`); `append_atomic_test` guard; null version without manifest → -ENOENT
  (`src/rgw/driver/rados/rgw_rados.cc:6620-6622@v19.2.6`); a fresh 32-char write tag, prepare ADD, setxattr idtag (`src/rgw/driver/rados/rgw_rados.cc:6665-6676@v19.2.6`); mtime
  kept unless set (`src/rgw/driver/rados/rgw_rados.cc:6683-6688@v19.2.6`); complete ADD with the state's size and the attrs' etag/ct/sc
  (`src/rgw/driver/rados/rgw_rados.cc:6723-6725@v19.2.6`) or cancel. `RadosObject::set_obj_attrs` adds 1 ns when log_op
  (`src/rgw/driver/rados/rgw_sal_rados.cc:2384@v19.2.6`); `modify_obj_attrs` resolves the target through
  `get_obj_attrs` (follows the OLH) and writes the instance (`src/rgw/driver/rados/rgw_sal_rados.cc:2401-2419@v19.2.6`).
- R21. `Object::Read::prepare` `src/rgw/driver/rados/rgw_rados.cc:6847@v19.2.6` (`src/rgw/driver/rados/rgw_rados.cc:7686@v20.2.4`): `get_state(...,
  follow_olh=true)`; `!exists` → -ENOENT; `state.obj = astate->obj` (the resolved instance);
  `RadosReadOp::prepare` copies the instance back to the SAL object
  (`src/rgw/driver/rados/rgw_sal_rados.cc:2863@v19.2.6`, `src/rgw/driver/rados/rgw_sal_rados.cc:3678@v20.2.4`), and `RGWGetObj::execute` reads
  `version_id = s->object->get_instance()` (`src/rgw/rgw_op.cc:2259-2260@v19.2.6`).
- R22. Listing: `list_objects_ordered` passes `list_versions` and drops `!is_visible` entries
  itself (`src/rgw/driver/rados/rgw_rados.cc:1886@v19.2.6`, `src/rgw/driver/rados/rgw_rados.cc:1920-1924@v19.2.6`; `src/rgw/driver/rados/rgw_rados.cc:1905@v20.2.4`); `cls_bucket_list_ordered`
  `src/rgw/driver/rados/rgw_rados.cc:9614@v19.2.6` (`src/rgw/driver/rados/rgw_rados.cc:10535@v20.2.4`) calls `check_disk_state` for an entry that is neither
  existing, a delete marker nor a common prefix, or has pending ops (`src/rgw/driver/rados/rgw_rados.cc:9823-9827@v19.2.6`); unordered
  `src/rgw/driver/rados/rgw_rados.cc:10083-10085@v19.2.6`. `check_disk_state` `src/rgw/driver/rados/rgw_rados.cc:10323-10475@v19.2.6` (`src/rgw/driver/rados/rgw_rados.cc:11247@v20.2.4`) reads the entry's
  key with its instance and treats a delete marker as present (`src/rgw/driver/rados/rgw_rados.cc:10361@v19.2.6`).
- R23. Purge lists with `list_versions = true` (`src/rgw/driver/rados/rgw_sal_rados.cc:363@v19.2.6`, `src/rgw/driver/rados/rgw_sal_rados.cc:500@v19.2.6`).
- O1. rgw_op.cc@v19.2.6: `RGWGetBucketVersioning::execute` `src/rgw/rgw_op.cc:2747-2757@v19.2.6` (NoSuchBucket; versioned,
  versioning_enabled, mfa_enabled); `RGWSetBucketVersioning::verify_permission` `src/rgw/rgw_op.cc:2759-2770@v19.2.6`
  (s3PutBucketVersioning); `::execute` `src/rgw/rgw_op.cc:2777-2860@v19.2.6`: get_params, NoSuchBucket, object lock
  refusal `-ERR_INVALID_BUCKET_STATE` "bucket versioning cannot be disabled on buckets with object
  lock enabled" (`src/rgw/rgw_op.cc:2788-2793@v19.2.6`), MFA rules (`src/rgw/rgw_op.cc:2795-2817@v19.2.6`), forward to master, retry_raced_bucket_write
  flag update (`src/rgw/rgw_op.cc:2828-2849@v19.2.6`). v20.2.4: `src/rgw/rgw_op.cc:2979@v20.2.4`, `src/rgw/rgw_op.cc:2991@v20.2.4`, `src/rgw/rgw_op.cc:3009@v20.2.4` (MFA `src/rgw/rgw_op.cc:3032-3046@v20.2.4`).
- O2. rgw_rest_s3.cc@v19.2.6: GetBucketVersioning doc `src/rgw/rgw_rest_s3.cc:2152-2169@v19.2.6` (Status, MfaDelete only when
  versioned); `ver_config_status::decode_xml` `src/rgw/rgw_rest_s3.cc:2171-2202@v19.2.6` (Status Enabled|Suspended else
  invalid; MfaDelete Enabled|Disabled else -EINVAL); `SetBucketVersioning::get_params`
  `src/rgw/rgw_rest_s3.cc:2204-2261@v19.2.6` (read_all_input rgw_max_put_param_size; parse failure -EINVAL). v20.2.4 `src/rgw/rgw_rest_s3.cc:2255@v20.2.4`,
  `src/rgw/rgw_rest_s3.cc:2307@v20.2.4`.
- O3. `RGWPutObj::execute` instance: `versioning_enabled` → `set_instance(version_id)` if given
  else `gen_rand_obj_instance_name` (`src/rgw/rgw_op.cc:4278-4285@v19.2.6`, `src/rgw/rgw_op.cc:4495@v20.2.4`); append on a
  versioned bucket → -ERR_INVALID_BUCKET_STATE (`src/rgw/rgw_op.cc:4269-4272@v19.2.6`). `RGWCopyObj::execute`
  `src/rgw/rgw_op.cc:5573-5577@v19.2.6` (`src/rgw/rgw_op.cc:6124@v20.2.4`, `src/rgw/rgw_op.cc:6142@v20.2.4`): version_id → set_instance, else
  versioning_enabled → gen_rand; copy_obj gets olh_epoch and `&version_id` (`src/rgw/rgw_op.cc:5669-5671@v19.2.6`).
  `RGWCompleteMultipart::execute` `src/rgw/rgw_op.cc:6459-6466@v19.2.6` (`src/rgw/rgw_op.cc:7288@v20.2.4`); `upload->complete(...,
  olh_epoch, ...)` `src/rgw/rgw_op.cc:6482-6485@v19.2.6`.
- O4. `RGWDeleteObj::verify_permission` `src/rgw/rgw_op.cc:5138-5166@v19.2.6` (`src/rgw/rgw_op.cc:5528-5556@v20.2.4`):
  action by instance (`src/rgw/rgw_op.cc:5145-5147@v19.2.6`), bypass perm, MFA (`src/rgw/rgw_op.cc:5158-5163@v19.2.6`). `::execute` `src/rgw/rgw_op.cc:5173-5327@v19.2.6`
  (`src/rgw/rgw_op.cc:5563@v20.2.4`): `check_obj_lock = have_instance && obj_lock_enabled` (`src/rgw/rgw_op.cc:5186@v19.2.6`); `null_verid`
  (`src/rgw/rgw_op.cc:5187@v19.2.6`); `get_obj_state(follow=true)` (`src/rgw/rgw_op.cc:5189@v19.2.6`); ENOENT drops the lock check (`src/rgw/rgw_op.cc:5195-5203@v19.2.6`);
  `versioning_status`, `marker_version_id`, `olh_epoch`, `null_verid` into the delete op
  (`src/rgw/rgw_op.cc:5278-5288@v19.2.6`); ECANCELED → 0 (`src/rgw/rgw_op.cc:5302-5304@v19.2.6`); 412 → 0 with `no_precondition_error` (`src/rgw/rgw_op.cc:5305@v19.2.6`).
  Response `src/rgw/rgw_rest_s3.cc:3455-3470@v19.2.6` (`src/rgw/rgw_rest_s3.cc:3735@v20.2.4`): ENOENT → 204; x-amz-version-id
  if nonempty; x-amz-delete-marker true.
- O5. `RGWDeleteMultiObj::handle_individual_object` `src/rgw/rgw_op.cc:6818-6910@v19.2.6`
  (`src/rgw/rgw_op.cc:7743@v20.2.4`): per-key action, `check_obj_lock` as above, `versioning_status`,
  `marker_version_id` empty, FLAG_SKIP_UPDATE_OLH, ENOENT → 0, partial response with
  `result.delete_marker` and `result.version_id`. `handle_objects` `src/rgw/rgw_op.cc:6996-7005@v19.2.6` picks
  `handle_versioned_objects` when `bucket->versioned()`; that groups keys by name and skips
  update_olh for all but the last of each group (`src/rgw/rgw_op.cc:6912-6965@v19.2.6`). `::execute` MFA check
  `src/rgw/rgw_op.cc:7055-7068@v19.2.6`, inverted `src/rgw/rgw_op.cc:7969-7983@v20.2.4` (registry). Partial response
  `src/rgw/rgw_rest_s3.cc:4273-4301@v19.2.6` (`src/rgw/rgw_rest_s3.cc:4821@v20.2.4`): Key, VersionId when named, DeleteMarker +
  DeleteMarkerVersionId when a marker was made.
- O6. `RGWListBucket::execute` `src/rgw/rgw_op.cc:3078-3121@v19.2.6` (`src/rgw/rgw_op.cc:3312@v20.2.4`, which adds the
  indexless refusal `src/rgw/rgw_op.cc:3319-3324@v20.2.4`). `get_common_params` `src/rgw/rgw_rest_s3.cc:1710-1739@v19.2.6`
  (`versions`, `src/rgw/rgw_rest_s3.cc:1712@v19.2.6`); `get_params` `src/rgw/rgw_rest_s3.cc:1741-1754@v19.2.6` (key-marker/version-id-marker `src/rgw/rgw_rest_s3.cc:1750-1751@v19.2.6`);
  `send_versioned_response` `src/rgw/rgw_rest_s3.cc:1799-1869@v19.2.6` (v20.2.4 `src/rgw/rgw_rest_s3.cc:1821@v20.2.4`, `src/rgw/rgw_rest_s3.cc:1852@v20.2.4`, `src/rgw/rgw_rest_s3.cc:1910@v20.2.4`; v2 `src/rgw/rgw_rest_s3.cc:2081@v20.2.4`).
- O7. GET: `RGWGetObj_ObjStore_S3::send_response_data` x-amz-version-id if nonempty
  (`src/rgw/rgw_rest_s3.cc:461@v19.2.6`); PutObj `src/rgw/rgw_rest_s3.cc:2748@v19.2.6`, `src/rgw/rgw_rest_s3.cc:2754@v19.2.6` (`src/rgw/rgw_rest_s3.cc:2891@v20.2.4`); CompleteMultipart
  `src/rgw/rgw_rest_s3.cc:4081@v19.2.6` (`src/rgw/rgw_rest_s3.cc:4604@v20.2.4`); CopyObj's responses send no version header (`src/rgw/rgw_rest_s3.cc:3566-3603@v19.2.6`,
  `src/rgw/rgw_rest_s3.cc:3873@v20.2.4`). `versionId` → object key at `src/rgw/rgw_rest_s3.cc:4946-4955@v19.2.6`, `src/rgw/rgw_rest_s3.cc:5506-5515@v20.2.4`.
- O8. v20.2.4 `read_permissions` adds x-amz-delete-marker to a GET/HEAD refused with -ENOENT:
  `load_obj_state(follow_olh=true)`, `is_delete_marker()` (`src/rgw/rgw_rest.cc:1936-1944@v20.2.4`);
  `follow_olh` sets `state->is_dm` when `olh.removed` (`src/rgw/driver/rados/rgw_rados.cc:9760-9764@v20.2.4`), copied
  at `src/rgw/driver/rados/rgw_sal_rados.cc:2942@v20.2.4`. `read_obj_policy`'s ENOENT → ENOENT for an admin or a
  requester allowed s3:ListBucket, else EACCES (`src/rgw/rgw_op.cc:453-477@v20.2.4`, `src/rgw/rgw_op.cc:423-447@v19.2.6`).
- M1. MFA chain: header read and verified in `RGWHandler_REST_S3::postauth_init`
  (`src/rgw/rgw_rest_s3.cc:4996-4999@v19.2.6`, `src/rgw/rgw_rest_s3.cc:5556@v20.2.4`), return value discarded; `verify_mfa`
  `src/rgw/driver/rados/rgw_sal_rados.cc:284-312@v19.2.6` (`src/rgw/driver/rados/rgw_sal_rados.cc:301@v20.2.4`): split on " " into exactly two → else
  -EINVAL; serial must be in `mfa_ids` → else -EACCES; `check_mfa` <0 → -EACCES; else
  verified=true. `check_mfa` `src/rgw/services/svc_cls.cc:46-63@v19.2.6` (same lines v20.2.4):
  oid `"user:" + uid.to_str()` in `zone_params.otp_pool` (`src/rgw/services/svc_cls.h:56-58@v19.2.6`, `src/rgw/services/svc_cls.cc:32-44@v19.2.6`);
  `OTP::check` → result SUCCESS → 0 else -EACCES.
- M2. cls_otp client `src/cls/otp/cls_otp_client.cc:60-96@v19.2.6`: `otp_check` with
  `cls_otp_check_otp_op{id, val, token(16 random alnum)}` (v19 via `IoCtx::exec`, v20 via an
  ObjectWriteOperation, the only diff between tags), then `otp_get_result` **sent with the same
  check-op buffer `in`**, and the server's `otp_get_result` decodes a `cls_otp_check_otp_op`
  too (`src/cls/otp/cls_otp.cc:483-511@v19.2.6`); `cls_otp_get_result_op` is dead on both sides.
  Methods: otp_set RD|WR, otp_get RD, otp_check RD|WR, otp_get_result RD, otp_remove RD|WR,
  get_current_time RD (`src/cls/otp/cls_otp.cc:557-575@v19.2.6`). `otp_instance::check` `src/cls/otp/cls_otp.cc:108-127@v19.2.6`: at most
  ATTEMPTS_PER_WINDOW 5 checks per step window (`src/cls/otp/cls_otp.cc:43@v19.2.6`), else no record → UNKNOWN; `verify`
  `src/cls/otp/cls_otp.cc:129-153@v19.2.6` (liboath totp validate2, replay index). Types `src/cls/otp/cls_otp_types.h@v19.2.6`:
  `otp_info_t` v1 (type u8, id, seed, seed_type u8, seed_bin, time_ofs i32, step_size u32,
  window u32), `otp_check_t` v1 (token, timestamp, result u8), `otp_repo_t`. Ops
  `src/cls/otp/cls_otp_ops.h@v19.2.6` all v1.
- M3. rgw-go: `meta.UserInfo.MFAIDs` exists (`internal/meta/user.go:286`);
  `ZoneParams.OTPPool` exists (`internal/meta/zone_params.go:32`, default `<zone>.rgw.otp`
  `internal/meta/zone_params.go:157`).
- S1. librados C API cmpxattr is string mode only: `rados_write_op_cmpxattr`
  `src/librados/librados_c.cc:3667-3683@v19.2.6` (`CEPH_OSD_CMPXATTR_MODE_STRING` `src/librados/librados_c.cc:3679@v19.2.6`),
  `rados_read_op_cmpxattr` `src/librados/librados_c.cc:4148-4164@v19.2.6` (`src/librados/librados_c.cc:4160@v19.2.6`). The OSD's u64 mode parses the stored xattr with
  `from_chars`, an empty xattr as 0, and compares request-value OP stored
  (`src/osd/PrimaryLogPG.cc:4959-4974@v19.2.6`, dispatch `src/osd/PrimaryLogPG.cc:6413-6444@v19.2.6`). go-ceph's fork binding
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

- P1. v20.2.4 rgw_rados.cc `_do_write_meta`: `setxattr(RGW_ATTR_OLH_ID_TAG, state->olh_tag)` `src/rgw/driver/rados/rgw_rados.cc:3321@v20.2.4`;
  `versioned_target` `src/rgw/driver/rados/rgw_rados.cc:3433@v20.2.4`; `versioned_op` `src/rgw/driver/rados/rgw_rados.cc:3436@v20.2.4`; bilog flag `src/rgw/driver/rados/rgw_rados.cc:3439@v20.2.4`; `set_olh` call `src/rgw/driver/rados/rgw_rados.cc:3492@v20.2.4`.
- P2. v20.2.4 rgw_common.h: OLH attrs `src/rgw/rgw_common.h:150-155@v20.2.4`; flags `src/rgw/rgw_common.h:1057-1061@v20.2.4`; helpers `src/rgw/rgw_common.h:1116-1121@v20.2.4`;
  `ERR_MFA_REQUIRED` `src/rgw/rgw_common.h:318@v20.2.4`; `mfa_ids` `src/rgw/rgw_common.h:622@v20.2.4`; `mfa_verified` `src/rgw/rgw_common.h:1420@v20.2.4`.
- P3. v20.2.4 rgw_sal_rados.cc: `verify_mfa` `src/rgw/driver/rados/rgw_sal_rados.cc:301@v20.2.4`; `RadosBucket::list` `src/rgw/driver/rados/rgw_sal_rados.cc:897@v20.2.4`;
  `load_obj_state` `src/rgw/driver/rados/rgw_sal_rados.cc:2935@v20.2.4` (is_dm copy `src/rgw/driver/rados/rgw_sal_rados.cc:2942@v20.2.4`); `set_obj_attrs` `src/rgw/driver/rados/rgw_sal_rados.cc:2971@v20.2.4`; `modify_obj_attrs`
  `src/rgw/driver/rados/rgw_sal_rados.cc:2994@v20.2.4`; `gen_rand_obj_instance_name` `src/rgw/driver/rados/rgw_sal_rados.cc:3041@v20.2.4`; `RadosReadOp::prepare` `src/rgw/driver/rados/rgw_sal_rados.cc:3678@v20.2.4`;
  `RadosDeleteOp::delete_obj` `src/rgw/driver/rados/rgw_sal_rados.cc:3731@v20.2.4`; `delete_object` `src/rgw/driver/rados/rgw_sal_rados.cc:3766@v20.2.4`.
- P4. v20.2.4 rgw_rados.cc: `delete_obj` versioned branch `src/rgw/driver/rados/rgw_rados.cc:6445-6577@v20.2.4` (marker-path preconditions
  `src/rgw/driver/rados/rgw_rados.cc:6494-6509@v20.2.4`, instance-path `src/rgw/driver/rados/rgw_rados.cc:6520-6554@v20.2.4`, unlink `src/rgw/driver/rados/rgw_rados.cc:6556-6560@v20.2.4`); `RGWRados::delete_obj` `src/rgw/driver/rados/rgw_rados.cc:6743@v20.2.4`;
  `list_objects_ordered` `src/rgw/driver/rados/rgw_rados.cc:1905@v20.2.4`; `cls_bucket_list_ordered` `src/rgw/driver/rados/rgw_rados.cc:10535@v20.2.4`; `check_disk_state` `src/rgw/driver/rados/rgw_rados.cc:11247@v20.2.4`;
  `set_attrs` `src/rgw/driver/rados/rgw_rados.cc:7393@v20.2.4`; `Read::prepare` `src/rgw/driver/rados/rgw_rados.cc:7686@v20.2.4`; `guard_reshard` `src/rgw/driver/rados/rgw_rados.cc:8575@v20.2.4`; OLH functions
  `src/rgw/driver/rados/rgw_rados.cc:8417-9770@v20.2.4` as listed in C20/R-notes; `get_olh` reads OLH_INFO at `src/rgw/driver/rados/rgw_rados.cc:9640@v20.2.4` (the diff hunk
  `@@ -220,12 +220,12 @@` of the set_olh..follow_olh range, i.e. 9415+225).
- P5. `rgw_debug_inject_set_olh_err` option `src/common/options/rgw.yaml.in:2886@v20.2.4`;
  `rgw_olh_pending_timeout_sec` default 1_hr `src/common/options/rgw.yaml.in:2383-2393@v19.2.6`, `src/common/options/rgw.yaml.in:2483@v20.2.4`.
- P6. `svc_cls.cc` MFA lines identical at v20.2.4 (`src/rgw/services/svc_cls.cc:32@v20.2.4`, `src/rgw/services/svc_cls.cc:46@v20.2.4`, `src/rgw/services/svc_cls.cc:56@v20.2.4`).
- P7. s3m.sh (the marker inventory) and the cs/cg/gs/gg/fd helpers lived in `$TMPDIR`
  (`/tmp/claude/rv`), removed at the end of the run.
