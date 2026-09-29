# Phase 1 Unit P: Multipart Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

> Written 2026-09-28 against ceph v19.2.6 (`[S]`) and v20.2.4 (`[T]`); every line reference is to those tags unless marked.

**Goal:** Fill `op.MultipartStore` in the rados driver (`CreateUpload`, `GetUpload`, `PutPart`, `CopyPart`, `ListParts`, `ListUploads`, `Complete`, `Abort`) and register the seven S3 multipart handlers through `s3.multipartHandlers()` so that an upload rgw-go writes is byte-for-byte the layout radosgw writes — the `.meta` object in the placement's data-extra pool, the part heads and their shadow stripes, the bucket-index entries in the `multipart` namespace, the ETag-of-ETags — and an upload begun by either gateway can be completed by the other.

**Architecture:** A new class package `internal/cls/lock` marshals the `lock` class's requests over `radosclient.Execer`, so CompleteMultipart's `assert_exists` + `lock_exclusive` go out in one write op (radosgw's `MPRadosSerializer::try_lock`). The driver's multipart code is a thin layer over W's write machinery — W's stripe writer, `writeMeta`, index prepare/complete/cancel behind the reshard guard, the completion manager, GC enqueue — extended additively where multipart differs (non-atomic heads, an extra-pool head, a hash-source override, `remove_objs` on index completions, an exclusive first stripe). Listing reuses M's `ListObjects` in the `multipart` namespace with one additive name filter, reading through R's pool resolution and `ReadObject` for part copies.

**Tech Stack:** Go 1.27; the jhoblitt/go-ceph fork through `internal/radosclient` (no seam change); `internal/denc`, `internal/meta`, `internal/cls/{rgw,version,lock}`; Ginkgo v2 + Gomega; counterfeiter fakes in `internal/op/opfakes`; M's `internal/testutil/fakerados` and `internal/memstore`; the rooket Squid and Tentacle clusters for the two `[cluster]` tasks.

**Spec:** `docs/superpowers/specs/2026-09-25-rgw-go-design.md` §2, §6 ("CompleteMultipart takes radosgw's named lock on the multipart meta object"), §8 (encoding rule, placement of "head, tail and multipart names"), §9 ("the seven multipart ops"); unit P's scope in `docs/superpowers/plans/2026-09-29-phase-1/00-index.md` ("The nine units"); G's frozen contract (`G-gateway-core.md`, "Frozen interface contract"); Z's additions; M, A, R, N and W's contract additions as the brief lists them.

## Global Constraints

- Ceph floor: Squid 19.2.6+ and Tentacle 20.2.4+ daemons (PR #48). No special handling below the floor. Data written by older releases still decodes: `RGWUploadPartInfo` v2-v6, `multipart_upload_info` v1-v4, legacy `2/` upload ids ([rgw_multi.h:15-16](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_multi.h#L15-L16); the decoders at [rgw_basic_types.h:253-300](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_basic_types.h#L253-L300) and [rgw_common.h:1508-1540](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.h#L1508-L1540) at v19.2.6, [:270-325](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.h#L270-L325) and [:1552-1600](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.h#L1552-L1600) at v20.2.4).
- Encode persistent types at the cluster release, decode every version (spec §8): `RGWUploadPartInfo` v5 on Squid / v6 on Tentacle, `multipart_upload_info` v2 / v4 (`ENCODE_START(5, 2)` and `ENCODE_START(2, 1)` at [rgw_basic_types.h:253](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_basic_types.h#L253) and [rgw_common.h:1508](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.h#L1508) at v19.2.6, `(6, 2)` and `(4, 1)` at [:270](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.h#L270) and [:1552](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.h#L1552) at v20.2.4); class requests at the release; every `cls/*` call passes `s.release`.
- G's frozen contract is built against verbatim; every change here is additive: `op.ListObjectsParams.NameFilter`, W's `headWrite`/`indexOp`/`tailWriter` fields, and nothing renamed (P-D6, P-D7).
- Class packages call only methods Squid's classes register: `lock.lock`, `lock.unlock`, `lock.break_lock`, `lock.get_info`, `lock.assert_locked` ([cls_lock.cc:620-648](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/lock/cls_lock.cc#L620-L648)), `rgw.mp_upload_part_info_update` (registered at both floors: [cls_rgw.cc:4743](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L4743) at v19.2.6, [:5162](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L5162) at v20.2.4). No pool is created; no ephemeral lock is taken (P-D1; `docs/ceph-upstream-bugs.md`, "cls_lock get_info and assert_locked fail with EIO on an expired ephemeral lock").
- Lock names, durations and composition are radosgw's: `RGWCompleteMultipart` on the meta object in the DATA-EXTRA pool, `rgw_mp_lock_max_time` (600 s), cookie `""`, `assert_exists` in the same op (docs/exclusions.md:424-451; `MPRadosSerializer`, [rgw_sal_rados.cc:3737-3760](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L3737-L3760) at v19.2.6, [:4600-4623](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L4600-L4623) at v20.2.4).
- Bodies are read to EOF and A's verdict checked before any part is registered (D-A2); an aws-chunked body's decoded length is `AuthResult.ContentLength`.
- Never verify against the ambient cluster: the two `[cluster]` tasks run on `make cluster-up RELEASE=squid|tentacle` clusters through `rooket k`, `ROOKET_NAME=rgw-go-<release>`.
- No `docs/` file is created; `docs/ceph-upstream-bugs.md` and `docs/exclusions.md` are UPDATED where Tasks 3 and 11 say (Task 3: the client-checksum difference of P-D5, the one behaviour of this unit that differs from radosgw; Task 11: the cls_lock entry's rgw-go line, the dead `PUT_OBJ_EXCL` quirk, the shared-uploads bullet). Every behaviour that differs from radosgw is recorded in `docs/exclusions.md` by the task that introduces it, in the same PR, and reported for the rgw-rs session.
- `make check` green before a task is done; cgo builds need the `CGO_CFLAGS`/`CGO_LDFLAGS` from `CLAUDE.md`; every `go test` passes `-tags=ceph_preview`.

## Review Focus

1. **A part re-uploaded under the same number after the first landed.** radosgw's second write of `<key>.<id>.<n>` fails its exclusive create, re-prefixes to `<key>.<rand32>.<n>`, and records the old prefix in `past_prefixes` (`process_first_chunk`, [rgw_putobj_processor.cc:414-440](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_putobj_processor.cc#L414-L440); the class merges `past_prefixes`, [cls_rgw.cc:4360-4400](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L4360-L4400); v19.2.6); Complete and Abort must GC the OLD part's head and stripes and remove its index entry, the ETag list must use the NEW part, and `bucket check` must stay clean throughout. Pinned in Task 4 (the re-prefix spec), Task 6 and Task 7 (past-prefix GC and `remove_objs` specs), Task 10 (the cluster `bucket check` after a re-upload).
2. **A part landing while Complete runs.** The part's `cls_version_inc` on the meta object makes Complete's `cls_version_check` delete fail with ECANCELED; the parts that raced in must be GC'd and their index entries removed (`cleanup_orphaned_parts`, [rgw_sal_rados.cc:3038](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L3038) at v19.2.6), and the completed object must not reference them. Pinned in Task 6 ("retries the meta delete after a racing part" spec) and Task 10 step 4.
3. **Complete of an upload id that no longer exists.** radosgw answers 200 with an empty quoted ETag, `<ETag>&quot;&quot;</ETag>` on the wire, when the target object's ETag equals the recomputed ETag-of-ETags (a retried completion), and 500 InternalError "This multipart completion is already in progress" otherwise — never 404 ([rgw_op.cc:6436-6446](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L6436-L6446) at v19.2.6). Pinned in Task 6's `checkPreviouslyCompleted` table and Task 8's op spec.
4. **A part body that is short, long, or fails its digest.** `Content-Length` longer than the body → 400 RequestTimeout; a body past `rgw_max_put_size` → EntityTooLarge; a Content-MD5 mismatch → BadDigest; in every case no part is registered, no index entry is left pending, and the written stripes are removed. Pinned in Task 4's failure specs.
5. **ListMultipartUploads paging.** The marker is the META object's name (`<key>.<id>.meta`), part heads share the namespace and must not count toward `max-uploads`, `key-marker` without `upload-id-marker` yields `<key>..meta`, and a delimiter is searched in the whole meta name including its `.<id>.meta` suffix (radosgw's behaviour, not S3's; P-D6). Pinned in Task 5's listing specs and the `NameFilter` placement spec in M's listing.

## Decisions this plan fixes

- **P-D1 `internal/cls/lock` binds the write-op forms.** `Lock`, `Unlock`, `BreakLock`, `AssertLocked` over `radosclient.Execer` and `GetInfo` on a `*radosclient.ReadOp`, encoding `cls_lock_lock_op`, `cls_lock_unlock_op`, `cls_lock_break_op`, `cls_lock_assert_op`, `cls_lock_get_info_op` and decoding `cls_lock_get_info_reply` exactly as `cls_lock_ops.h` frames them (each `ENCODE_START(1, 1)`, with the lock types and flags of `cls_lock_types.h`; `src/cls/lock/` is identical at v19.2.6 and v20.2.4). The composition CompleteMultipart needs is written at the call site: `op.AssertExists(); lock.Lock(op, ...)` in one `WriteOp`, as `MPRadosSerializer::try_lock` does ([rgw_sal_rados.cc:3750-3760](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L3750-L3760) at v19.2.6, [:4613](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L4613) at v20.2.4); the package adds a `LockExisting` helper that spells that pair so no caller forgets the assertion. The seam's ioctx `LockExclusive`/`Unlock` stay for GC (`gc_process`). The package documents, and its specs pin, that `GetInfo` and `AssertLocked` must never be sent for an ephemeral lock (tracker #80993; `docs/ceph-upstream-bugs.md`, "cls_lock get_info and assert_locked fail with EIO on an expired ephemeral lock"); rgw-go phase 1 takes none.
- **P-D2 Lock parameters are radosgw's.** Name `RGWCompleteMultipart`, type exclusive, cookie `""` (radosgw's `MPRadosSerializer` never sets one, so its holder is `client.<gid>` + `""`), description `""`, duration `rgw_mp_lock_max_time` seconds, flags 0 ([rgw_sal_rados.cc:3737-3760](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L3737-L3760) at v19.2.6, [:4600-4623](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L4600-L4623) at v20.2.4). Recovery is by expiry, as radosgw's is.
- **P-D3 The meta object and the part heads are written NON-atomically, as radosgw writes them.** `RadosMultipartUpload::init` and `MultipartObjectProcessor::complete` never call `set_atomic`, so `prepare_atomic_modification` takes its early branch: no `cmpxattr` guard, no exclusive create, no `user.rgw.idtag`/`tail_tag`; `PUT_OBJ_EXCL` is consulted nowhere (`is_atomic` defaults to false, [rgw_sal.h:89](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_sal.h#L89), and only `RGWObjectCtx::set_atomic`, [rgw_rados.cc:223-227](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L223-L227), sets it; the early branch is in [rgw_rados.cc:6484-6575](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6484-L6575); `PUT_OBJ_EXCL`, [rgw_rados.h:67-69](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.h#L67-L69), appears nowhere in rgw_rados.cc; all at v19.2.6), confirmed by the phase-0 gate on a real cluster (non-head objects carry no idtag). W's `headWrite` gains `atomic bool`; P sets it false for the meta object and part heads, true for the completed target (which radosgw does `set_atomic(true)` on).
- **P-D4 Parts are sorted and de-duplicated by number before Complete; `ErrInvalidPartOrder` is never produced.** radosgw parses the completion document into `std::map<int, string>` (last duplicate wins, ascending order; [rgw_multi.cc:24-71](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_multi.cc#L24-L71)), so an out-of-order client list completes fine. The op sorts; the driver validates lockstep against the stored parts and answers `ErrInvalidPart` for a count or number or ETag mismatch. `memstore`'s "strictly increasing → ErrInvalidPartOrder" rule is relaxed to match (reported to G as a contract note).
- **P-D5 Tentacle checksums are phase 2; conditional Complete is not.** `x-amz-checksum-*` and `x-amz-checksum-algorithm` are ignored on both releases (T's `checksum` marker excludes those tests; owner decision 7 kept the spec's phasing); the Tentacle `multipart_upload_info` carries `cksum_type 0, cksum_flags 0`, the Tentacle `RGWUploadPartInfo` an absent optional ([rgw_common.h:1552-1600](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.h#L1552-L1600) and rgw_basic_types.h:270-325; `FLAG_CKSUM_NONE` is 0, [rgw_cksum.h:104-108](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum.h#L104-L108); radosgw's own checksum path is `try_sum_part_cksums`, [rgw_op.cc:7044-7161](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L7044-L7161); all at v20.2.4). A Squid radosgw ignores them too, but a Tentacle radosgw validates a supplied checksum (400 BadDigest on a mismatch) and stores `user.rgw.cksum` on PutObject, UploadPart and CompleteMultipartUpload (`RGWPutObj::execute`, [rgw_op.cc:4757-4789](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L4757-L4789); `RGWCompleteMultipart::execute`, [:7330-7340](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L7330-L7340); at v20.2.4), which GetObjectAttributes and a checksum-mode GET render (rgw_rest_s3.cc:4029-4031, :551-552). On Tentacle that is a difference from radosgw, for single PUT (W's PutObject) as for multipart; Task 3, whose upload info is the first record written without the checksum type, records it for both in `docs/exclusions.md`'s coexistence section. `If-Match`/`If-None-Match` on CompleteMultipart are honoured on Tentacle only (a Squid radosgw ignores them; Tentacle's `get_params` reads them, [rgw_rest_s3.cc:4572-4573](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4572-L4573) at v20.2.4, and its `complete` passes them to the head write) through W's precondition path on the final head write.
- **P-D6 ListMultipartUploads reuses M's `ListObjects` with an additive `NameFilter`.** radosgw's `list_multiparts` is the ordered listing in namespace `multipart` with `access_list_filter = MultipartMetaFilter`, applied after the marker advance and BEFORE prefix, delimiter and counting (`list_multiparts`, [rgw_sal_rados.cc:915-956](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L915-L956), with `MultipartMetaFilter`, [svc_tier_rados.cc:8-30](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_tier_rados.cc#L8-L30); the order is the loop's at [rgw_rados.cc:1802-2160](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L1802-L2160): `count < max → marker = key`, then the filter, then prefix, then delimiter). `op.ListObjectsParams` gains `NameFilter func(name string) bool`; M's `listObjectsOrdered`/`listObjectsUnordered` and `memstore` apply it at that point. P passes `MultipartMetaFilter` and the marker `<key-marker>.<upload-id-marker>.meta`.
- **P-D7 The driver builds on W, additively.** `headWrite` gains `atomic`, `pool`/`oid` (an extra-pool head), `category`, `removeObjs`, `hashName`, `logOp`; `indexOp` gains the hash source and `remove_objs` on complete/completeDel/cancel; `tailWriter` gains a stripe namer and an awaited exclusive first stripe with the first chunk retained for a retry (radosgw's `RadosWriter::write_exclusive` issues `create(true)`, the alloc hint and `write_full` and drains them before anything else, [rgw_putobj_processor.cc:118-183](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_putobj_processor.cc#L118-L183) at v19.2.6). The meta object's delete is P's own sequence over W's `indexOp` (prepare DEL, `obj_remove` + `cls_version_check`, `complete_del`/cancel with `remove_objs`, as `Delete::delete_obj` does, [rgw_rados.cc:5757-5987](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5757-L5987) at v19.2.6) because W's `DeleteObject` targets data-pool heads and has no version check or `remove_objs`.
- **P-D8 UploadPartCopy reads through R.** The source is `PrefetchObject`ed for its state, permission-checked with `op.VerifyObjectPermissionIn(ctx, r, action, perm, srcRec, src)` (Z's rule for RGWPutObj, [rgw_op.cc:3955-3963](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L3955-L3963); the explicit-resource form of `op.Authorizer` — a request copy with `BucketRec` swapped would evaluate the public-access block and requester-pays on the source bucket where radosgw keeps them on the request's), refused when cloud-tiered or missing, and streamed with `ReadObject` over `[fst, lst]` in `rgw_max_chunk_size` pieces into the part writer. No `x-amz-copy-source-if-*` conditionals: `RGWPutObj` evaluates none ([rgw_op.cc:3806-4530](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L3806-L4530)). Range grammar `bytes=<digits>-<digits>`, first > last → ERANGE (416 InvalidRange), first ≥ size → ERANGE, last clamped to size-1 (`range_to_ofs`), no range → `[0, accounted_size-1]` ([rgw_op.cc:3806-3918](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L3806-L3918), [:4018-4075](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L4018-L4075)).
- **P-D9 Error mapping follows radosgw's table, oddities included** (the errno table, [rgw_common.cc:51-144](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L51-L144) at v19.2.6): Complete's lock failure of ANY kind, a missing upload included, → 500 InternalError "This multipart completion is already in progress" unless `checkPreviouslyCompleted` holds; Abort on a missing upload → 404 NoSuchUpload, on a locked one → 503 ServiceUnavailable (EBUSY); `rgw_multipart_part_upload_limit` exceeded → 416 InvalidRange (ERANGE); an empty `uploadId=` on Complete or ListParts → 500 UnknownError (ENOTSUP has no row); UploadPart/ListParts on a missing upload → 404 NoSuchUpload (`get_info`'s mapping).
- **P-D10 A ranged copy-source PUT without `uploadId` stays NotImplemented.** RGWPutObj accepts `x-amz-copy-source` + `x-amz-copy-source-range` on a plain PUT (an rgw extension, no S3 counterpart, no s3-tests coverage). W's `put_obj` handler hands every request with `uploadId` to P's `uploadPart`; the uploadId-less ranged copy keeps W's `ErrNotImplemented`.
- **P-D11 Part registration uses `rgw.mp_upload_part_info_update` only.** The class method exists at both floors ([cls_rgw.cc:4743](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L4743) at v19.2.6, [:5162](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L5162) at v20.2.4), so the EOPNOTSUPP omap fallback radosgw keeps for old OSDs is not implemented; what it would have written is byte-identical and decodes the same. The class's EEXIST (a random prefix colliding with a past one) is passed through `FromRADOS` as radosgw passes the raw errno (the class, [cls_rgw.cc:4360-4400](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L4360-L4400); its caller, [rgw_putobj_processor.cc:560-604](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_putobj_processor.cc#L560-L604); P-D9).
- **P-D12 Quota accounting mirrors radosgw's cache updates** (`_do_write_meta`, [rgw_rados.cc:3124-3417](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L3124-L3417), and `Delete::delete_obj`, [:5757-5987](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5757-L5987), at v19.2.6): each part write `AdjustStats(+1 object, +accounted bytes, 0)` (`!reset_obj` → `orig_exists=false` even for a re-upload); the meta object write `AdjustStats(+1, 0, 0)`; Complete's head write `AdjustStats(+1 or 0 if the target existed, 0, origSize)` (`completeMultipart`); Complete's meta delete `AdjustStats(-1, 0, metaSize)`; Abort's meta delete `AdjustStats(-1, 0, partsAccountedSize)`. The index header itself is corrected by the class through `remove_objs` (`rgw_bucket_complete_op`'s loop, [cls_rgw.cc:1215-1227](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L1215-L1227) at v19.2.6, un-accounts each entry through `complete_remove_obj`, [:981-1004](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L981-L1004)).
- **P-D13 ListParts and ListMultipartUploads render what radosgw renders** (`send_response` at [rgw_rest_s3.cc:4121-4171](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L4121-L4171) and [:4173-4230](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L4173-L4230), `dump_time` and `dump_owner` at [rgw_rest.cc:498-516](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L498-L516), all at v19.2.6; Tentacle's ListParts calls `dump_time_exact_seconds`, [rgw_rest_s3.cc:4704](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4704) at v20.2.4): ListParts' `Owner` is the meta object's ACL owner, no `Initiator`, `NextPartNumberMarker` = the highest part returned (0 for none), `LastModified` in milliseconds on Squid and whole seconds on Tentacle; ListMultipartUploads' `Initiator` and `Owner` are the index entry's owner, `StorageClass` the literal `STANDARD`, `Initiated` in milliseconds, `Key`/`Prefix` url-encoded with '/' kept when `encoding-type=url`.
- **P-D14 `Location` in CompleteMultipartUploadResult** is `<scheme>://<host>/<bucket>/<key>` (or `/<tenant>:<bucket>/<key>` plus a `<Tenant>` element), scheme `https` when the request was TLS, host = the raw `Host` header (`compute_domain_uri`, [rgw_rest.h:805-819](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.h#L805-L819) at v19.2.6); the exact host form is pinned by the cluster gate against radosgw, not by a unit spec.
- **P-D15 `x-amz-abort-date`/`x-amz-abort-rule-id`** (lifecycle) and SSE response headers are phase 2: never emitted.

## File structure

```
internal/cls/lock/doc.go                     package doc: the lock class, its methods, the ephemeral caveat
internal/cls/lock/types.go                   Type, Flags, EntityName, LockerID, LockerInfo, Info + encoders/decoders
internal/cls/lock/ops.go                     Lock, LockExisting, Unlock, BreakLock, AssertLocked, GetInfo
internal/cls/lock/{types,ops,goldens}_test.go, lock_suite_test.go, lock_integration_test.go
internal/cls/lock/testdata/goldens/          generated by hack/goldens/gen.sh
internal/cls/rgw/ops_mp.go                   MPUploadPartInfoUpdate (raw-framed info), MPUploadPartInfoUpdateOp
internal/cls/rgw/const.go                    modify: methodMPUploadPartInfoUpdate
internal/cls/rgw/ops_test.go, goldens_test.go  modify
internal/meta/multipart.go                   MultipartUploadInfo, UploadPartInfo, ObjectRetention, ObjectLegalHold; meta-name helpers
internal/meta/manifest_multipart.go          NewPartManifest, StripeObj, Manifest.Append
internal/meta/{multipart,manifest_multipart,goldens}_test.go  modify/create
hack/goldens/types.txt                       modify: the nine new corpus types
internal/op/bucket.go                        modify: ListObjectsParams.NameFilter (P-D6)
internal/op/multipart.go                     unchanged types; doc corrections
internal/op/opfakes/                         regenerate
internal/op/{initmultipart,uploadpart,completemultipart,abortmultipart,listparts,listuploads}.go   the ops
internal/op/*_test.go                        the op specs on memstore
internal/memstore/multipart.go, bucket.go    modify: P-D4 relaxation, NameFilter, PutParams.Tag/ContentMD5 on parts
internal/driver/mp_layout.go                 metaRef, partRef, extraPool, readMeta, metaState
internal/driver/mp_upload.go                 CreateUpload, GetUpload
internal/driver/mp_part.go                   PutPart, CopyPart, the exclusive-first stripe retry, part registration
internal/driver/mp_list.go                   ListParts, ListUploads
internal/driver/mp_complete.go               Complete: lock, validate, assemble, head write, meta delete loop
internal/driver/mp_abort.go                  Abort, deleteMeta, partsToGC, cleanupPartHistory (shared with Complete); abortMultiparts (the DeleteBucket hook)
internal/driver/bucketops.go                 modify (M's): DeleteBucket calls abortMultiparts inside its own-bucket branch (Task 7)
internal/driver/mp_*_test.go                 on fakerados
internal/driver/headwrite.go                 modify (W's): atomic, pool/oid override, category, removeObjs, hashName, logOp
internal/driver/indexop.go                   modify (W's): hashName, removeObjs
internal/driver/stripe.go                    modify (W's): namer, exclusiveFirst, first chunk
internal/driver/listing.go                   modify (M's): NameFilter placement
internal/driver/store.go                     modify: the seven MultipartStore stubs go
internal/testutil/fakerados/cls_lock.go      LockClass emulator; cls_rgw.go gains mp_upload_part_info_update
internal/s3/multipart.go                     multipartHandlers(), the seven handlers, XML documents, uploadPart entry for W's put_obj
internal/s3/multipart_test.go
internal/s3/putobject.go                     modify (W's): the uploadId branch calls uploadPart
test/integration/multipart_test.go           [cluster] the cross-gateway oracle and bucket check
test/s3tests/p-groups.txt                    the s3-tests patterns the P gate is judged by
docs/ceph-upstream-bugs.md, docs/exclusions.md   modify (Task 11; docs/exclusions.md also Task 3, the client-checksum difference)
```

## Task index

| # | Task | Depends on | Cluster |
|---|---|---|---|
| 1 | `internal/cls/lock`: types, ops, goldens, fake, integration spec | G Task 2 (seam) | one spec `[cluster]` |
| 2 | `meta` multipart types and name helpers; `cls/rgw` part-info update; goldens | none | no |
| 3 | `driver`: layout helpers, `readMeta`, W's additive extensions, `CreateUpload`, `GetUpload` | 2; W Tasks 4-5; R Task 3; M Tasks 1, 5, 6 | no |
| 4 | `driver`: `PutPart`, `CopyPart` | 3; W Task 5 (`tailWriter`, `planPut`); R Task 4 (`ReadObject`) | no |
| 5 | `driver`: `ListParts`, `ListUploads`; `ListObjectsParams.NameFilter` | 3; M Task 8 (listing) | no |
| 6 | `driver`: `Complete` | 1, 4, 5; W Task 5 (`writeMeta`, `enqueueGC`) | no |
| 7 | `driver`: `Abort`, `deleteMeta`, the shared GC/cleanup helpers; `abortMultiparts` in M's `DeleteBucket` | 6; M Task 7 (`DeleteBucket`), M Task 8 (`ListObjects`) | no |
| 8 | `op`: the six multipart ops (UploadPart covers the copy) | G Task 1; Z; M's `LogUsage`; A's body contract | no |
| 9 | `s3`: handlers, XML, registration, W's `put_obj` branch | 8; G Tasks 3-4; W Task 11 | no |
| 10 | Integration: cross-gateway oracle, `radosgw-admin bucket check`, `object stat` parity | 3-9; T's harness | `[cluster]` |
| 11 | Gate: s3-tests multipart groups; registry updates | 9, 10; T Tasks 4, 5, 12 | `[cluster]` |

Tasks 1 and 2 are independent and start together. Task 3 needs W's `writeMeta`/`indexOp` and R's `readHead`/`dataPool` to exist; Tasks 4 and 5 can run in parallel after 3; 6 needs 4 and 5; 7 follows 6; Task 8 needs only G's contract and can start with Task 3; 9 follows 8. Tasks 10 and 11 close the unit.

---

### Task 1: `internal/cls/lock`: types, ops, goldens, fake, integration spec

**Files:**
- Create: `internal/cls/lock/doc.go`, `internal/cls/lock/types.go`, `internal/cls/lock/ops.go`, `internal/cls/lock/types_test.go`, `internal/cls/lock/ops_test.go`, `internal/cls/lock/goldens_test.go`, `internal/cls/lock/lock_suite_test.go`, `internal/cls/lock/lock_integration_test.go`, `internal/testutil/fakerados/cls_lock.go`, `internal/testutil/fakerados/cls_lock_test.go`
- Modify: `internal/cls/internal/clsutil/clsutil.go` (`ReadFramed`), `internal/cls/internal/clsutil/clsutil_test.go`, `hack/goldens/types.txt` (six lines), `.golangci.yml` only if depguard lists class packages explicitly (check; phase 0's `cls/gc` needed no change)
- Generated: `internal/cls/lock/testdata/goldens/{cls_lock_lock_op,cls_lock_unlock_op,cls_lock_break_op,cls_lock_assert_op,cls_lock_get_info_reply,entity_name_t}/19.2.0-404-g78ddc7f9027/*.{bin,json,reenc}` by `hack/goldens/gen.sh`

**Interfaces:**
- Consumes: `radosclient.Execer`, `*radosclient.ReadOp`, `*radosclient.WriteOp` (`AssertExists`, `Exec`), `radosclient.ExecResult`; `denc.Encoder`/`Decoder` (`BeginStruct`, `BeginStructLegacy`, `EndStruct`, `String`, `U8`, `U32`, `I64`, `Time`, `Raw`, `EncodeMap`/`DecodeMap`); `clsutil.Encode`, `clsutil.DecodeReply`; `fakerados.Object` (`Xattrs map[string][]byte`), `fakerados.ClassFunc`; `cephtest.Conf`, `cephtest.ReadManifest`, `goceph.Connect` (the pattern of `internal/cls/version/version_integration_test.go`).
- Produces:

```go
package lock // internal/cls/lock

// The class and the methods its CLS_INIT registers (cls_lock.cc:620-648 at
// v19.2.6; the file is identical at v20.2.4). set_cookie and list_locks are
// not bound: radosgw never calls them.
const (
	Class              = "lock"
	methodLock         = "lock"
	methodUnlock       = "unlock"
	methodBreakLock    = "break_lock"
	methodGetInfo      = "get_info"
	methodAssertLocked = "assert_locked"
)

// Type is ClsLockType (cls_lock_types.h). TypeExclusiveEphemeral is decoded
// but never requested by rgw-go: the class removes an ephemeral lock's object
// when the last holder expires, and get_info and assert_locked, registered
// read-only, then fail with EIO instead of reporting it (tracker #80993).
type Type uint8

const (
	TypeNone               Type = 0
	TypeExclusive          Type = 1
	TypeShared             Type = 2
	TypeExclusiveEphemeral Type = 3
)

// Flags are the LOCK_FLAG_* bits: MayRenew makes a lock the caller already
// holds idempotent; MustRenew fails with ENOENT unless the caller holds it.
// Both set is EINVAL (lock_obj, cls_lock.cc:153-160).
type Flags uint8

const (
	FlagMayRenew  Flags = 0x1
	FlagMustRenew Flags = 0x2
)

// EntityName is entity_name_t: the holder's type and number, DENC'd as u8
// then i64 (msg_types.h:45-51). Client is CEPH_ENTITY_TYPE_CLIENT (msgr.h:94).
type EntityName struct {
	Type uint8
	Num  int64
}

const EntityTypeClient uint8 = 0x08

func (n EntityName) Encode(e *denc.Encoder, _ denc.Release)
func DecodeEntityName(d *denc.Decoder) EntityName
// String is entity_name_t's operator<<: "<type>.<num>", e.g. "client.4123";
// ParseEntityName inverts it, which is how a Locker's Client string from the
// seam's ListLockers becomes a break_lock argument.
func (n EntityName) String() string
func ParseEntityName(s string) (EntityName, bool)

// LockOp is cls_lock_lock_op, ENCODE_START(1, 1): name, type as u8, cookie,
// tag, description, duration as utime_t (u32 seconds, u32 nanoseconds), flags
// as u8. Duration zero means the lock never expires.
type LockOp struct {
	Name        string
	Type        Type
	Cookie      string
	Tag         string
	Description string
	Duration    time.Duration
	Flags       Flags
}

// UnlockOp is cls_lock_unlock_op: name, cookie.
type UnlockOp struct{ Name, Cookie string }

// BreakOp is cls_lock_break_op: name, locker, cookie (in that order).
type BreakOp struct {
	Name   string
	Locker EntityName
	Cookie string
}

// AssertOp is cls_lock_assert_op: name, type as u8, cookie, tag.
type AssertOp struct {
	Name   string
	Type   Type
	Cookie string
	Tag    string
}

// GetInfoOp is cls_lock_get_info_op: name.
type GetInfoOp struct{ Name string }

// LockerID is locker_id_t: the holder's entity name and cookie.
type LockerID struct {
	Locker EntityName
	Cookie string
}

// LockerInfo is locker_info_t. Addr is the holder's entity_addr_t as the
// class encoded it (a framed struct under the client's features); rgw-go
// carries it opaquely and re-encodes it byte for byte.
type LockerInfo struct {
	Expiration  time.Time
	Addr        []byte
	Description string
}

// Info is lock_info_t / cls_lock_get_info_reply: the holders, the type, the tag.
type Info struct {
	Lockers map[LockerID]LockerInfo
	Type    Type
	Tag     string
}

// Every type above has Encode(e *denc.Encoder, r denc.Release) and a
// Decode<Name>(d *denc.Decoder) counterpart; the decoders accept the
// DECODE_START_LEGACY_COMPAT_LEN(1, 1, 1) framing every cls_lock struct uses.

// Lock adds lock.lock: it takes name for the request's client identity under
// cookie, failing with EEXIST when that identity already holds it without
// MayRenew, EBUSY when another holder or tag stands in the way, ENOENT with
// MustRenew when it does not hold it (lock_obj, cls_lock.cc:135-249). A lock on
// a missing object CREATES the object; callers that need the object to exist
// use LockExisting.
func Lock(op radosclient.Execer, p LockOp, r denc.Release)

// LockExisting is MPRadosSerializer::try_lock's composition (rgw_sal_rados.cc
// :3750-3760 at v19.2.6, :4613 at v20.2.4): assert_exists, then lock.lock for
// an exclusive lock, in the one write op, so a vanished object answers ENOENT
// rather than being recreated as a bare lock holder.
func LockExisting(op *radosclient.WriteOp, name, cookie, description string, duration time.Duration, r denc.Release)

// Unlock adds lock.unlock for this client's hold under cookie; ENOENT when it
// holds none (remove_lock, :274-305).
func Unlock(op radosclient.Execer, name, cookie string, r denc.Release)

// BreakLock adds lock.break_lock, releasing locker's hold under cookie.
func BreakLock(op radosclient.Execer, name string, locker EntityName, cookie string, r denc.Release)

// AssertLocked adds lock.assert_locked: the op fails with EBUSY unless this
// client holds name under cookie and tag with type typ. Never send it for an
// ephemeral lock (see Type).
func AssertLocked(op radosclient.Execer, name string, typ Type, cookie, tag string, r denc.Release)

// InfoResult is a pending get_info reply.
type InfoResult struct{ res *radosclient.ExecResult }

// GetInfo adds lock.get_info on a read op. Never send it for an ephemeral lock
// (see Type).
func GetInfo(op *radosclient.ReadOp, name string, r denc.Release) *InfoResult
func (res *InfoResult) Info() (Info, error)
```

```go
package clsutil

// ReadFramed returns the bytes of one ENCODE_START-framed value at the
// decoder's position, header included, and advances past it: u8 version, u8
// compat, u32 length, then length bytes. It lets a class package carry a
// nested struct it does not decode.
func ReadFramed(d *denc.Decoder) []byte
```

```go
package fakerados

// LockClass emulates the lock class over the object's "lock.<name>" xattr,
// holding an encoded lock.Info: lock (with lock_obj's EEXIST/EBUSY/ENOENT/
// EINVAL rules and expiry), unlock, break_lock, get_info, assert_locked.
// The requester is always ClientName; a lock on an absent object creates it.
func LockClass() ClassFunc

// ClientName is the entity every fake request originates from.
var ClientName = lock.EntityName{Type: lock.EntityTypeClient, Num: 4242}
```

- [ ] **Step 1: Write the failing encoding specs**

`internal/cls/lock/lock_suite_test.go` is the standard Ginkgo bootstrap (`package lock_test`, `TestLock`, `RegisterFailHandler(Fail)`, `RunSpecs(t, "cls/lock")`). `internal/cls/lock/types_test.go`:

```go
package lock_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cls/lock"
	"github.com/jhoblitt/rgw-go/internal/denc"
)

func encode(v interface{ Encode(*denc.Encoder, denc.Release) }) []byte {
	e := denc.NewEncoder()
	v.Encode(e, denc.Squid)
	return e.Bytes()
}

var _ = Describe("cls_lock wire types", func() {
	It("encodes cls_lock_unlock_op as ENCODE_START(1, 1) with two strings", func() {
		Expect(encode(lock.UnlockOp{Name: "n"})).To(Equal([]byte{
			1, 1, 9, 0, 0, 0, // version 1, compat 1, body length 9
			1, 0, 0, 0, 'n', // name
			0, 0, 0, 0, // cookie ""
		}))
	})
	It("encodes cls_lock_lock_op in cls_lock_ops.h's field order with a utime_t duration", func() {
		b := encode(lock.LockOp{Name: "RGWCompleteMultipart", Type: lock.TypeExclusive, Duration: 600 * time.Second})
		Expect(b[:6]).To(Equal([]byte{1, 1, 46, 0, 0, 0}), "4+20 name, 1 type, 4+4+4 empty strings, 8 utime, 1 flags")
		Expect(b[6:30]).To(Equal(append([]byte{20, 0, 0, 0}, []byte("RGWCompleteMultipart")...)))
		Expect(b[30]).To(Equal(byte(1)), "EXCLUSIVE")
		Expect(b[31:43]).To(Equal(make([]byte, 12)), "cookie, tag, description empty")
		Expect(b[43:51]).To(Equal([]byte{0x58, 0x02, 0, 0, 0, 0, 0, 0}), "600 s, 0 ns")
		Expect(b[51]).To(Equal(byte(0)), "flags")
		Expect(b).To(HaveLen(52))
	})
	It("round-trips every op through its decoder", func() {
		ops := []struct {
			enc []byte
			dec func(*denc.Decoder) any
		}{
			{encode(lock.LockOp{Name: "a", Type: lock.TypeShared, Cookie: "c", Tag: "t", Description: "d", Duration: 90 * time.Second, Flags: lock.FlagMayRenew}), func(d *denc.Decoder) any { return lock.DecodeLockOp(d) }},
			{encode(lock.UnlockOp{Name: "a", Cookie: "c"}), func(d *denc.Decoder) any { return lock.DecodeUnlockOp(d) }},
			{encode(lock.BreakOp{Name: "a", Locker: lock.EntityName{Type: lock.EntityTypeClient, Num: 4123}, Cookie: "c"}), func(d *denc.Decoder) any { return lock.DecodeBreakOp(d) }},
			{encode(lock.AssertOp{Name: "a", Type: lock.TypeExclusive, Cookie: "c", Tag: "t"}), func(d *denc.Decoder) any { return lock.DecodeAssertOp(d) }},
			{encode(lock.GetInfoOp{Name: "a"}), func(d *denc.Decoder) any { return lock.DecodeGetInfoOp(d) }},
		}
		for i, o := range ops {
			d := denc.NewDecoder(o.enc)
			v := o.dec(d)
			Expect(d.Err()).NotTo(HaveOccurred(), "op %d", i)
			Expect(d.Remaining()).To(BeZero(), "op %d", i)
			Expect(encode(v.(interface{ Encode(*denc.Encoder, denc.Release) }))).To(Equal(o.enc), "op %d", i)
		}
	})
	It("encodes entity_name_t as u8 type then i64 num and prints it as client.<num>", func() {
		n := lock.EntityName{Type: lock.EntityTypeClient, Num: 4123}
		Expect(encode(n)).To(Equal([]byte{8, 0x1b, 0x10, 0, 0, 0, 0, 0, 0}))
		Expect(n.String()).To(Equal("client.4123"))
		p, ok := lock.ParseEntityName("client.4123")
		Expect(ok).To(BeTrue())
		Expect(p).To(Equal(n))
		_, ok = lock.ParseEntityName("nonsense")
		Expect(ok).To(BeFalse())
	})
	It("decodes a get_info reply and re-encodes the holder's address untouched", func() {
		// lock_info_t{lockers: {client.7/"" → {expiration 1700000000.5, addr <framed blob>, "desc"}}, EXCLUSIVE, tag ""}
		addr := []byte{1, 1, 4, 0, 0, 0, 0xde, 0xad, 0xbe, 0xef} // any framed struct: version 1, compat 1, len 4
		e := denc.NewEncoder()
		f := e.BeginStruct(1, 1)
		e.U32(1) // one locker
		lf := e.BeginStruct(1, 1)
		lock.EntityName{Type: 8, Num: 7}.Encode(e, denc.Squid)
		e.String("")
		e.EndStruct(lf)
		inf := e.BeginStruct(1, 1)
		e.U32(1700000000)
		e.U32(500000000)
		e.Raw(addr)
		e.String("desc")
		e.EndStruct(inf)
		e.U8(1)
		e.String("")
		e.EndStruct(f)
		d := denc.NewDecoder(e.Bytes())
		info := lock.DecodeInfo(d)
		Expect(d.Err()).NotTo(HaveOccurred())
		Expect(info.Type).To(Equal(lock.TypeExclusive))
		Expect(info.Lockers).To(HaveLen(1))
		li := info.Lockers[lock.LockerID{Locker: lock.EntityName{Type: 8, Num: 7}}]
		Expect(li.Expiration).To(Equal(time.Unix(1700000000, 500000000).UTC()))
		Expect(li.Addr).To(Equal(addr))
		Expect(li.Description).To(Equal("desc"))
		Expect(encode(info)).To(Equal(e.Bytes()))
	})
})
```

`internal/cls/lock/ops_test.go`:

```go
var _ = Describe("lock ops", func() {
	It("composes LockExisting as assert_exists then lock.lock in one write op", func() {
		op := radosclient.NewWriteOp()
		lock.LockExisting(op, "RGWCompleteMultipart", "", "", 600*time.Second, denc.Squid)
		steps := op.Steps()
		Expect(steps).To(HaveLen(2))
		Expect(steps[0]).To(BeAssignableToTypeOf(&radosclient.AssertExistsStep{}))
		ex := steps[1].(*radosclient.ExecStep)
		Expect(ex.Class).To(Equal("lock"))
		Expect(ex.Method).To(Equal("lock"))
		d := denc.NewDecoder(ex.In)
		got := lock.DecodeLockOp(d)
		Expect(d.Err()).NotTo(HaveOccurred())
		Expect(got).To(Equal(lock.LockOp{Name: "RGWCompleteMultipart", Type: lock.TypeExclusive, Duration: 600 * time.Second}))
	})
	It("names the class methods Squid's class registers", func() {
		op := radosclient.NewWriteOp()
		lock.Unlock(op, "n", "c", denc.Squid)
		lock.BreakLock(op, "n", lock.EntityName{Type: 8, Num: 1}, "c", denc.Squid)
		lock.AssertLocked(op, "n", lock.TypeExclusive, "c", "", denc.Squid)
		var methods []string
		for _, s := range op.Steps() {
			methods = append(methods, s.(*radosclient.ExecStep).Method)
		}
		Expect(methods).To(Equal([]string{"unlock", "break_lock", "assert_locked"}))
		rop := radosclient.NewReadOp()
		res := lock.GetInfo(rop, "n", denc.Squid)
		Expect(rop.Steps()[0].(*radosclient.ExecStep).Method).To(Equal("get_info"))
		rop.Steps()[0].(*radosclient.ExecStep).Result.Set([]byte{1, 1, 9, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}, 0) // no lockers, type NONE, tag ""
		info, err := res.Info()
		Expect(err).NotTo(HaveOccurred())
		Expect(info).To(Equal(lock.Info{Lockers: map[lock.LockerID]lock.LockerInfo{}, Type: lock.TypeNone}))
	})
	It("surfaces the op's error from Info", func() {
		rop := radosclient.NewReadOp()
		res := lock.GetInfo(rop, "n", denc.Squid)
		rop.Steps()[0].(*radosclient.ExecStep).Result.Set(nil, -int32(syscall.ENOENT))
		_, err := res.Info()
		Expect(err).To(MatchError(radosclient.ErrNotFound))
	})
})
```

(`ExecResult.Set(out, rval)` is [results.go:27](https://github.com/jhoblitt/rgw-go/blob/50fc45c4d59953661d5c57c27ca7016bc55edeeb/internal/radosclient/results.go#L27); `ExecResult.Bytes()` maps a negative rval through the seam's sentinels, which is what `clsutil.DecodeReply` relies on. If `Bytes()` does not map errnos to sentinels, adapt the last assertion to the error `Bytes()` returns — check [results.go:16-25](https://github.com/jhoblitt/rgw-go/blob/50fc45c4d59953661d5c57c27ca7016bc55edeeb/internal/radosclient/results.go#L16-L25) first.)

- [ ] **Step 2: Run the specs to see them fail**

Run: `go test -tags=ceph_preview ./internal/cls/lock/`
Expected: compile failure, `package lock` undefined.

- [ ] **Step 3: Implement `clsutil.ReadFramed`, `types.go`, `ops.go`, `doc.go`**

`clsutil.go` gains:

```go
// ReadFramed returns one ENCODE_START-framed value, header included, and
// advances past it.
func ReadFramed(d *denc.Decoder) []byte {
	start := d.Offset()
	_ = d.U8() // version
	_ = d.U8() // compat
	n := d.U32()
	body := d.Raw(int(n))
	if d.Err() != nil {
		return nil
	}
	out := make([]byte, 0, 6+len(body))
	out = append(out, d.Bytes()[start:start+6]...) // if Decoder exposes no underlying slice, rebuild: version, compat, then n as 4 LE bytes
	return append(out, body...)
}
```

If `denc.Decoder` has no accessor for its buffer, rebuild the header: `out = append(out, v, c); out = binary.LittleEndian.AppendUint32(out, n)` — the two forms are byte-identical. Add a spec in `clsutil_test.go`: a framed value followed by a trailing byte reads back the framed bytes and leaves the trailing byte.

`types.go` (excerpt; every type follows the same shape):

```go
package lock

func encodeUtime(e *denc.Encoder, d time.Duration) {
	e.U32(uint32(d / time.Second))       //nolint:gosec // utime_t seconds
	e.U32(uint32(d % time.Second))       //nolint:gosec // < 1e9
}

func decodeUtime(d *denc.Decoder) time.Duration {
	s := d.U32()
	ns := d.U32()
	return time.Duration(s)*time.Second + time.Duration(ns)
}

func (n EntityName) Encode(e *denc.Encoder, _ denc.Release) { e.U8(n.Type); e.I64(n.Num) }

func DecodeEntityName(d *denc.Decoder) EntityName { return EntityName{Type: d.U8(), Num: d.I64()} }

var entityTypeNames = map[uint8]string{0x01: "mon", 0x02: "mds", 0x04: "osd", 0x08: "client", 0x10: "mgr"}

func (n EntityName) String() string {
	name, ok := entityTypeNames[n.Type]
	if !ok {
		name = "unknown"
	}
	return name + "." + strconv.FormatInt(n.Num, 10)
}

func ParseEntityName(s string) (EntityName, bool) {
	typ, num, ok := strings.Cut(s, ".")
	if !ok {
		return EntityName{}, false
	}
	n, err := strconv.ParseInt(num, 10, 64)
	if err != nil {
		return EntityName{}, false
	}
	for t, name := range entityTypeNames {
		if name == typ {
			return EntityName{Type: t, Num: n}, true
		}
	}
	return EntityName{}, false
}

// Encode mirrors cls_lock_lock_op::encode, ENCODE_START(1, 1).
func (o LockOp) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	e.String(o.Name)
	e.U8(uint8(o.Type))
	e.String(o.Cookie)
	e.String(o.Tag)
	e.String(o.Description)
	encodeUtime(e, o.Duration)
	e.U8(uint8(o.Flags))
	e.EndStruct(f)
}

// DecodeLockOp mirrors cls_lock_lock_op::decode, DECODE_START_LEGACY_COMPAT_LEN(1, 1, 1).
func DecodeLockOp(d *denc.Decoder) LockOp {
	h := d.BeginStructLegacy(1, 1, 1, 0)
	o := LockOp{Name: d.String(), Type: Type(d.U8()), Cookie: d.String(), Tag: d.String(), Description: d.String()}
	o.Duration = decodeUtime(d)
	o.Flags = Flags(d.U8())
	d.EndStruct(h)
	return o
}
```

`UnlockOp`, `BreakOp` (name, `Locker.Encode`, cookie), `AssertOp`, `GetInfoOp` follow. `LockerID`: `BeginStruct(1,1)`, locker, cookie. `LockerInfo`: `BeginStruct(1,1)`, `e.Time(Expiration)`, `e.Raw(Addr)`, `e.String(Description)`; decode with `d.Time()`, `clsutil.ReadFramed(d)`, `d.String()`. `Info`: `BeginStruct(1,1)`, `denc.EncodeMap(e, Lockers, encLockerID, encLockerInfo)` — `LockerID` is not `cmp.Ordered`, so sort the keys with a comparator matching `locker_id_t::operator<` (entity name by type then num, then cookie) and encode `u32 count` + pairs by hand; decode with `d.U32()` count and a loop; then `U8` type, `String` tag. Map order matters for byte-identical re-encoding of the goldens: `entity_name_t::operator<` compares `_type` then `_num` (msg_types.h); pin it in the goldens step.

`ops.go`:

```go
package lock

func Lock(op radosclient.Execer, p LockOp, r denc.Release) {
	op.Exec(Class, methodLock, clsutil.Encode(p, r))
}

func LockExisting(op *radosclient.WriteOp, name, cookie, description string, duration time.Duration, r denc.Release) {
	op.AssertExists()
	Lock(op, LockOp{Name: name, Type: TypeExclusive, Cookie: cookie, Description: description, Duration: duration}, r)
}

func Unlock(op radosclient.Execer, name, cookie string, r denc.Release) {
	op.Exec(Class, methodUnlock, clsutil.Encode(UnlockOp{Name: name, Cookie: cookie}, r))
}

func BreakLock(op radosclient.Execer, name string, locker EntityName, cookie string, r denc.Release) {
	op.Exec(Class, methodBreakLock, clsutil.Encode(BreakOp{Name: name, Locker: locker, Cookie: cookie}, r))
}

func AssertLocked(op radosclient.Execer, name string, typ Type, cookie, tag string, r denc.Release) {
	op.Exec(Class, methodAssertLocked, clsutil.Encode(AssertOp{Name: name, Type: typ, Cookie: cookie, Tag: tag}, r))
}

type InfoResult struct{ res *radosclient.ExecResult }

func GetInfo(op *radosclient.ReadOp, name string, r denc.Release) *InfoResult {
	return &InfoResult{res: op.Exec(Class, methodGetInfo, clsutil.Encode(GetInfoOp{Name: name}, r))}
}

func (res *InfoResult) Info() (Info, error) {
	return clsutil.DecodeReply(res.res, Class, methodGetInfo, DecodeInfo)
}
```

`doc.go` states the package's scope, the write-op composition (`LockExisting`), the cookie semantics (unique per client; radosgw's multipart cookie is ""), and the ephemeral caveat with the tracker link.

- [ ] **Step 4: Run the specs to see them pass**

Run: `go test -tags=ceph_preview ./internal/cls/lock/ ./internal/cls/internal/clsutil/`
Expected: PASS.

- [ ] **Step 5: Goldens**

Append to `hack/goldens/types.txt`:

```
cls_lock_lock_op internal/cls/lock
cls_lock_unlock_op internal/cls/lock
cls_lock_break_op internal/cls/lock
cls_lock_assert_op internal/cls/lock
cls_lock_get_info_reply internal/cls/lock
entity_name_t internal/cls/lock
```

Run `hack/goldens/gen.sh` (podman, quay.io/ceph/ceph:v19.2.6; the corpus at `/home/jhoblitt/github/ceph/ceph-object-corpus/archive/19.2.0-404-g78ddc7f9027/objects/` holds 10, 10, 7, 6, 10 and 10 objects for these types). `internal/cls/lock/goldens_test.go`, the `internal/cls/refcount/goldens_test.go` pattern:

```go
var squid = goldentest.Options{Release: denc.Squid, SkipJSON: true}

var _ = Describe("corpus goldens", func() {
	const dir = "testdata"
	It("cls_lock_lock_op", func() {
		goldentest.RoundTrip(dir, "cls_lock_lock_op", squid, lock.DecodeLockOp, func(e *denc.Encoder, v lock.LockOp, r denc.Release) { v.Encode(e, r) })
	})
	It("cls_lock_unlock_op", func() { goldentest.RoundTrip(dir, "cls_lock_unlock_op", squid, lock.DecodeUnlockOp, func(e *denc.Encoder, v lock.UnlockOp, r denc.Release) { v.Encode(e, r) }) })
	It("cls_lock_break_op", func() { goldentest.RoundTrip(dir, "cls_lock_break_op", squid, lock.DecodeBreakOp, func(e *denc.Encoder, v lock.BreakOp, r denc.Release) { v.Encode(e, r) }) })
	It("cls_lock_assert_op", func() { goldentest.RoundTrip(dir, "cls_lock_assert_op", squid, lock.DecodeAssertOp, func(e *denc.Encoder, v lock.AssertOp, r denc.Release) { v.Encode(e, r) }) })
	It("cls_lock_get_info_reply", func() { goldentest.RoundTrip(dir, "cls_lock_get_info_reply", squid, lock.DecodeInfo, func(e *denc.Encoder, v lock.Info, r denc.Release) { v.Encode(e, r) }) })
	It("entity_name_t", func() { goldentest.RoundTrip(dir, "entity_name_t", squid, lock.DecodeEntityName, func(e *denc.Encoder, v lock.EntityName, r denc.Release) { v.Encode(e, r) }) })
})
```

Run: `go test -tags=ceph_preview ./internal/cls/lock/`. Expected: PASS. If `cls_lock_get_info_reply` fails on locker ORDER, the map comparator is wrong: fix it to `entity_name_t::operator<` (type, then num) then cookie, and re-run; if it fails on the address bytes, `ReadFramed` mis-sized the addr — the corpus addr is `entity_addr_t` under full features, a framed struct (`ENCODE_START(1, 1)`), so the failure would be a `ReadFramed` bug. `SkipJSON` because ceph-dencoder's `dump_json` of these types is not compared anywhere else in rgw-go.

- [ ] **Step 6: The fakerados emulator**

`internal/testutil/fakerados/cls_lock.go`:

```go
package fakerados

// ClientName is the entity every fake request originates from
// (cls_get_request_origin's inst.name for the one fake client).
var ClientName = lock.EntityName{Type: lock.EntityTypeClient, Num: 4242}

const lockXattrPrefix = "lock." // LOCK_PREFIX, cls_lock.cc

// LockClass emulates the lock class (cls_lock.cc:135-420 at v19.2.6).
func LockClass() ClassFunc {
	return func(obj *Object, method string, in []byte) ([]byte, int32, bool) {
		switch method {
		case "lock":
			return lockLock(obj, in)
		case "unlock":
			d := denc.NewDecoder(in)
			op := lock.DecodeUnlockOp(d)
			if d.Err() != nil {
				return nil, errno(syscall.EINVAL), false
			}
			return nil, lockRemove(obj, op.Name, ClientName, op.Cookie), false
		case "break_lock":
			d := denc.NewDecoder(in)
			op := lock.DecodeBreakOp(d)
			if d.Err() != nil {
				return nil, errno(syscall.EINVAL), false
			}
			return nil, lockRemove(obj, op.Name, op.Locker, op.Cookie), false
		case "get_info":
			d := denc.NewDecoder(in)
			op := lock.DecodeGetInfoOp(d)
			if d.Err() != nil {
				return nil, errno(syscall.EINVAL), false
			}
			info, rc := lockRead(obj, op.Name)
			if rc < 0 {
				return nil, rc, false
			}
			e := denc.NewEncoder()
			info.Encode(e, denc.Squid)
			return e.Bytes(), 0, false
		case "assert_locked":
			d := denc.NewDecoder(in)
			op := lock.DecodeAssertOp(d)
			if d.Err() != nil {
				return nil, errno(syscall.EINVAL), false
			}
			info, rc := lockRead(obj, op.Name)
			if rc < 0 {
				return nil, rc, false
			}
			if info.Type != op.Type || info.Tag != op.Tag {
				return nil, errno(syscall.EBUSY), false
			}
			if _, ok := info.Lockers[lock.LockerID{Locker: ClientName, Cookie: op.Cookie}]; !ok {
				return nil, errno(syscall.EBUSY), false
			}
			return nil, 0, false
		}
		return nil, errno(syscall.EOPNOTSUPP), false
	}
}

// lockRead is read_lock: a missing object is ENOENT, a missing xattr an empty
// info, expired holders are trimmed. (The ephemeral-removal branch is not
// emulated: rgw-go never takes ephemeral locks.)
func lockRead(obj *Object, name string) (lock.Info, int32) {
	if obj == nil {
		return lock.Info{}, errno(syscall.ENOENT)
	}
	b, ok := obj.Xattrs[lockXattrPrefix+name]
	if !ok {
		return lock.Info{Lockers: map[lock.LockerID]lock.LockerInfo{}}, 0
	}
	d := denc.NewDecoder(b)
	info := lock.DecodeInfo(d)
	if d.Err() != nil {
		return lock.Info{}, errno(syscall.EIO)
	}
	now := time.Now()
	for id, li := range info.Lockers {
		if !li.Expiration.IsZero() && li.Expiration.Before(now) {
			delete(info.Lockers, id)
		}
	}
	return info, 0
}

func lockWrite(obj *Object, name string, info lock.Info) {
	e := denc.NewEncoder()
	info.Encode(e, denc.Squid)
	if obj.Xattrs == nil {
		obj.Xattrs = map[string][]byte{}
	}
	obj.Xattrs[lockXattrPrefix+name] = e.Bytes()
}

// lockLock is lock_obj (cls_lock.cc:135-249). A lock on an absent object
// creates it (created = true).
func lockLock(obj *Object, in []byte) ([]byte, int32, bool) {
	d := denc.NewDecoder(in)
	op := lock.DecodeLockOp(d)
	if d.Err() != nil {
		return nil, errno(syscall.EINVAL), false
	}
	failIfExists := op.Flags&lock.FlagMayRenew == 0
	failIfAbsent := op.Flags&lock.FlagMustRenew != 0
	if op.Type != lock.TypeExclusive && op.Type != lock.TypeShared && op.Type != lock.TypeExclusiveEphemeral {
		return nil, errno(syscall.EINVAL), false
	}
	if op.Name == "" || (!failIfExists && failIfAbsent) {
		return nil, errno(syscall.EINVAL), false
	}
	created := false
	if obj == nil {
		obj = &Object{} // the pool stores it when created is true; adapt to the pool's creation hook
		created = true
	}
	info, rc := lockRead(obj, op.Name)
	if rc < 0 && rc != errno(syscall.ENOENT) {
		return nil, rc, false
	}
	if info.Lockers == nil {
		info.Lockers = map[lock.LockerID]lock.LockerInfo{}
	}
	id := lock.LockerID{Locker: ClientName, Cookie: op.Cookie}
	if len(info.Lockers) > 0 && op.Tag != info.Tag {
		return nil, errno(syscall.EBUSY), false
	}
	if _, held := info.Lockers[id]; held {
		if failIfExists && !failIfAbsent {
			return nil, errno(syscall.EEXIST), false
		}
		delete(info.Lockers, id)
	} else if failIfAbsent {
		return nil, errno(syscall.ENOENT), false
	}
	if len(info.Lockers) > 0 {
		exclusive := op.Type == lock.TypeExclusive || op.Type == lock.TypeExclusiveEphemeral
		if exclusive || info.Type != op.Type {
			return nil, errno(syscall.EBUSY), false
		}
	}
	info.Type, info.Tag = op.Type, op.Tag
	var exp time.Time
	if op.Duration != 0 {
		exp = time.Now().Add(op.Duration)
	}
	info.Lockers[id] = lock.LockerInfo{Expiration: exp, Addr: []byte{1, 1, 0, 0, 0, 0}, Description: op.Description}
	lockWrite(obj, op.Name, info)
	return nil, 0, created
}

// lockRemove is remove_lock: ENOENT for an unknown holder.
func lockRemove(obj *Object, name string, locker lock.EntityName, cookie string) int32 {
	info, rc := lockRead(obj, name)
	if rc < 0 {
		return rc
	}
	id := lock.LockerID{Locker: locker, Cookie: cookie}
	if _, ok := info.Lockers[id]; !ok {
		return errno(syscall.ENOENT)
	}
	delete(info.Lockers, id)
	lockWrite(obj, name, info)
	return 0
}
```

Check how M's `fakerados` hands a nil `*Object` to a class and applies `created` (M plan :227: `ClassFunc func(obj *Object, method string, in []byte) (out []byte, rval int32, created bool)`); if the pool passes a fresh non-nil `Object` for a missing oid and uses `created` to decide whether to store it, drop the `obj == nil` branch and set `created = true` when the object had no prior existence (the pool tells the class through a field or the caller; read `pool.go`'s exec dispatch and follow it). `cls_lock_test.go` pins: lock on a missing object creates it and holds `client.4242/""`; a second lock with the same cookie is EEXIST, with MayRenew succeeds and refreshes the expiration; another cookie is EBUSY; a different tag is EBUSY; MustRenew without a hold is ENOENT; MayRenew|MustRenew is EINVAL; unlock then unlock is 0 then ENOENT; break_lock with the holder's name releases it; get_info lists the holder; an expired holder (duration 1 ns, sleep) is gone on the next read; assert_locked EBUSY for the wrong cookie.

Run: `go test -tags=ceph_preview ./internal/testutil/fakerados/`. Expected: PASS.

- [ ] **Step 7: The integration spec `[cluster]`**

`internal/cls/lock/lock_integration_test.go` (`//go:build integration`, `Label("integration")`, the `version_integration_test.go` fixture: `cephtest.Conf()`, `cephtest.ReadManifest(conf, &m)` with `m.Pools.Data`, `goceph.Connect`). Add `radosclient.ErrBusy` if the seam lacks the sentinel; otherwise match `&radosclient.Error{Errno: 16}` in the EBUSY assertion:

```go
It("takes, refuses, releases and breaks the RGWCompleteMultipart lock as the class does", func(ctx SpecContext) {
	pool, err := cluster.Pool(ctx, m.Pools.Data, "")
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(func() { Expect(pool.Close()).To(Succeed()) })
	oid := "rgw-go-lock-spec-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	DeferCleanup(func() {
		w := radosclient.NewWriteOp()
		w.Remove()
		_, _ = pool.Write(ctx, oid, w, 0)
	})
	const name = "RGWCompleteMultipart"
	rel := denc.Squid

	// assert_exists + lock on a missing object: ENOENT, and nothing is created.
	w := radosclient.NewWriteOp()
	lock.LockExisting(w, name, "", "", time.Minute, rel)
	_, err = pool.Write(ctx, oid, w, 0)
	Expect(err).To(MatchError(radosclient.ErrNotFound))
	r := radosclient.NewReadOp()
	r.Stat()
	_, err = pool.Read(ctx, oid, r, 0)
	Expect(err).To(MatchError(radosclient.ErrNotFound), "the assertion kept the lock from creating the object")

	w = radosclient.NewWriteOp()
	w.WriteFull([]byte("x"))
	Expect(pool.Write(ctx, oid, w, 0)).Error().To(Succeed())

	w = radosclient.NewWriteOp()
	lock.LockExisting(w, name, "", "", time.Minute, rel)
	Expect(pool.Write(ctx, oid, w, 0)).Error().To(Succeed())

	w = radosclient.NewWriteOp()
	lock.LockExisting(w, name, "", "", time.Minute, rel)
	_, err = pool.Write(ctx, oid, w, 0)
	Expect(err).To(MatchError(radosclient.ErrExists), "the same client and cookie again: EEXIST, lock_obj :193-199")

	w = radosclient.NewWriteOp()
	lock.LockExisting(w, name, "other", "", time.Minute, rel)
	_, err = pool.Write(ctx, oid, w, 0)
	Expect(err).To(MatchError(radosclient.ErrBusy), "another cookie: EBUSY")

	lockers, err := pool.ListLockers(ctx, oid, name)
	Expect(err).NotTo(HaveOccurred())
	Expect(lockers).To(HaveLen(1))
	Expect(lockers[0].Cookie).To(BeEmpty())
	holder, ok := lock.ParseEntityName(lockers[0].Client)
	Expect(ok).To(BeTrue(), lockers[0].Client)

	rop := radosclient.NewReadOp()
	res := lock.GetInfo(rop, name, rel)
	Expect(pool.Read(ctx, oid, rop, 0)).Error().To(Succeed())
	info, err := res.Info()
	Expect(err).NotTo(HaveOccurred())
	Expect(info.Type).To(Equal(lock.TypeExclusive))
	Expect(info.Lockers).To(HaveKey(lock.LockerID{Locker: holder}))

	w = radosclient.NewWriteOp()
	lock.BreakLock(w, name, holder, "", rel)
	Expect(pool.Write(ctx, oid, w, 0)).Error().To(Succeed())

	w = radosclient.NewWriteOp()
	lock.Unlock(w, name, "", rel)
	_, err = pool.Write(ctx, oid, w, 0)
	Expect(err).To(MatchError(radosclient.ErrNotFound), "nothing left to unlock")
})
```

Run on the Squid cluster: `make cluster-up RELEASE=squid` (once), then `ROOKET_NAME=rgw-go-squid go test -tags='ceph_preview integration' ./internal/cls/lock/ -run TestLock -ginkgo.label-filter=integration` (use the Makefile's integration target if phase 0 defined one: `grep -n integration Makefile`). Expected: PASS. Repeat on Tentacle.

- [ ] **Step 8: Lint, check, commit**

Run: `make check`. Expected: green (the new package compiles under `ceph_preview`; depguard allows `internal/cls/*` → `denc`, `radosclient`, `clsutil`).

```bash
git add internal/cls/lock internal/cls/internal/clsutil internal/testutil/fakerados/cls_lock.go internal/testutil/fakerados/cls_lock_test.go hack/goldens/types.txt
git commit -m "feat(cls/lock): bind the lock class over the exec seam

CompleteMultipart takes radosgw's RGWCompleteMultipart lock with
assert_exists and lock_exclusive in one write op, which the seam's
ioctx LockExclusive cannot compose. Lock, Unlock, BreakLock,
AssertLocked and GetInfo marshal cls_lock's requests as
cls_lock_ops.h frames them, proven against the v19 corpus goldens;
LockExisting spells the assert_exists composition so a vanished meta
object answers ENOENT instead of being recreated as a lock holder.
get_info and assert_locked are documented as unsafe on an ephemeral
lock (tracker #80993); rgw-go takes none.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: `meta` multipart types and name helpers; `cls/rgw` part-info update; goldens

**Files:**
- Create: `internal/meta/multipart.go`, `internal/meta/multipart_test.go`, `internal/meta/objectlock.go`, `internal/meta/objectlock_test.go`, `internal/meta/manifest_multipart.go`, `internal/meta/manifest_multipart_test.go`, `internal/cls/rgw/ops_mp.go`
- Modify: `internal/denc/frame.go` and `internal/denc/frame_test.go` (`Decoder.RestOfStruct`), `internal/meta/goldens_test.go` (four `It`s), `internal/cls/rgw/goldens_test.go` (one `It`), `internal/cls/rgw/const.go` (`methodMPUploadPartInfoUpdate`), `internal/cls/rgw/ops_test.go`, `internal/testutil/fakerados/cls_rgw.go` (`mp_upload_part_info_update`), `internal/testutil/fakerados/cls_rgw_test.go`, `hack/goldens/types.txt` (five lines)
- Generated: `internal/meta/testdata/goldens/{RGWUploadPartInfo,multipart_upload_info}/…`, `internal/cls/rgw/testdata/goldens/cls_rgw_mp_upload_part_info_update_op/…`

**Interfaces:**
- Consumes: `meta.Manifest`, `ManifestRule`, `PlacementRule` (`Encode`/`DecodePlacementRule`, W's `InheritFrom`), `CompressionInfo` (`Encode`/`DecodeCompressionInfo`, `NewCompressionInfo`), `Obj`, `ObjKey`, `NSMultipart`, `NSShadow`, `BucketPlacement`; `denc` (`BeginStruct`, `BeginStructLegacy`, `Header.End` or the equivalent end offset — read [frame.go:31-40](https://github.com/jhoblitt/rgw-go/blob/50fc45c4d59953661d5c57c27ca7016bc55edeeb/internal/denc/frame.go#L31-L40) for the field name), `denc.EncodeSlice`/`DecodeSlice`, `Time`, `I64`, `U16`, `Bool`; `clsutil.ReadFramed` (Task 1); the manifest goldens `internal/meta/testdata/manifests/squid-multipart.{bin,json}` (R Task 1 / phase 0).
- Produces:

```go
package meta

// Multipart object names, RGWMPObj (svc_tier_rados.h) and MP_META_SUFFIX
// (svc_tier_rados.cc:8), identical at v19.2.6 and v20.2.4.
const (
	MultipartMetaSuffix           = ".meta"
	MultipartUploadIDPrefix       = "2~" // MULTIPART_UPLOAD_ID_PREFIX, rgw_multi.h:16
	MultipartUploadIDPrefixLegacy = "2/" // rgw_multi.h:15
)

// MultipartMetaName is RGWMPObj::get_meta: "<key>.<uploadID>.meta".
func MultipartMetaName(key, uploadID string) string

// MultipartPrefix is the manifest prefix MultipartObjectProcessor::prepare sets
// (rgw_putobj_processor.cc:484-488): "<key>.<unique>", where unique is the upload
// id, or the 32-character random string a re-upload of the same part number
// falls back to on EEXIST (:414-439).
func MultipartPrefix(key, unique string) string

// MultipartPartName is RGWMPObj::get_part: "<prefix>.<n>", the part head's
// name in the multipart namespace.
func MultipartPartName(prefix string, n uint32) string

// MultipartPartKey is the meta object's omap key for part n of a v2 upload:
// "part.%08d" (rgw_putobj_processor.cc:547-551, rgw_sal_rados.cc:3354-3357).
func MultipartPartKey(n uint32) string

// ParseMultipartMeta is RGWMPObj::from_meta: "<key>.<uploadID>.meta" split at
// its last two dots; false when the name has fewer than two dots.
func ParseMultipartMeta(name string) (key, uploadID string, ok bool)

// IsMultipartMeta is MultipartMetaFilter (svc_tier_rados.cc:10-30): the name
// ends in ".meta" with room for a key, and has a dot before the suffix.
func IsMultipartMeta(name string) bool

// IsV2UploadID is is_v2_upload_id (rgw_multi.cc:76-82): both prefixes count.
func IsV2UploadID(id string) bool

// UploadPartInfo is RGWUploadPartInfo (rgw_basic_types.h:253 at v19.2.6,
// :270 at v20.2.4): one registered part, stored under MultipartPartKey in the
// meta object's omap. Cksum is Tentacle's optional rgw::cksum::Cksum carried
// as the raw bytes from the optional's presence byte to the struct's end
// (checksums are not implemented yet); nil encodes an absent optional.
type UploadPartInfo struct {
	Num           uint32
	Size          uint64
	ETag          string
	Modified      time.Time
	Manifest      Manifest
	Compression   CompressionInfo
	AccountedSize uint64
	PastPrefixes  []string // std::set<std::string>: kept sorted and unique
	Cksum         []byte
}

// Encode writes ENCODE_START(5, 2) on Squid and ENCODE_START(6, 2) on
// Tentacle: num, size, etag, modified, manifest, cs_info, accounted_size,
// past_prefixes, and on Tentacle the optional cksum.
func (p UploadPartInfo) Encode(e *denc.Encoder, r denc.Release)
// DecodeUploadPartInfo is DECODE_START_LEGACY_COMPAT_LEN(6, 2, 2): manifest
// from v3, cs_info and accounted_size from v4 (else accounted = size),
// past_prefixes from v5, cksum from v6.
func DecodeUploadPartInfo(d *denc.Decoder) UploadPartInfo

// MultipartUploadInfo is multipart_upload_info (rgw_common.h:1508 at
// v19.2.6, :1552 at v20.2.4), the meta object's data.
type MultipartUploadInfo struct {
	DestPlacement PlacementRule
	Retention     *ObjectRetention // nil when obj_retention_exist is false
	LegalHold     *ObjectLegalHold
	CksumType     uint16 // Tentacle: rgw::cksum::Type, 0 none
	CksumFlags    uint16 // Tentacle: FLAG_CKSUM_NONE 0
}

// Encode writes ENCODE_START(2, 1) on Squid and ENCODE_START(4, 1) on
// Tentacle: dest_placement, the two existence bools, a (default when nil)
// retention and legal hold, and on Tentacle the u16 type and u16 flags.
func (u MultipartUploadInfo) Encode(e *denc.Encoder, r denc.Release)
// DecodeMultipartUploadInfo accepts v1 (placement only) to v4.
func DecodeMultipartUploadInfo(d *denc.Decoder) MultipartUploadInfo

// ObjectRetention is RGWObjectRetention (rgw_object_lock.h): ENCODE_START(2, 1)
// with the mode, the date as utime_t and the date again as round_trip_encode's
// i64 nanoseconds (encoding.h:394-412), which decode prefers from v2.
type ObjectRetention struct {
	Mode        string
	RetainUntil time.Time
}

// ObjectLegalHold is RGWObjectLegalHold: ENCODE_START(1, 1) with the status.
type ObjectLegalHold struct{ Status string }

// NewPartManifest is what MultipartObjectProcessor::prepare + prepare_head +
// generator::create_begin leave for part n (rgw_putobj_processor.cc:441-489,
// rgw_obj_manifest.h:265-270, rgw_obj_manifest.cc:221-275): prefix
// "<key>.<unique>", Rules {0: {StartPartNum n, StartOfs 0, PartSize 0,
// StripeMaxSize stripe}}, MaxHeadSize 0 (no data in any head), Obj = target
// (the UPLOAD's key, not the part head) at HeadSize 0, HeadPlacementRule =
// headRule, TailPlacement = tailRule.InheritFrom(headRule) in target's bucket,
// TailInstance = target.Key.Instance.
func NewPartManifest(target Obj, headRule, tailRule PlacementRule, prefix string, partNum uint32, stripeSize uint64) Manifest

// StripeObj is get_implicit_location for rule 0's part and stripe s: the
// object TailObj names when part is 0; "<prefix>.<part>" in the multipart
// namespace for stripe 0 of a later part (the part head); "<prefix>.<part>_<s>"
// in the shadow namespace after it. A rule OverridePrefix replaces the prefix.
func (m Manifest) StripeObj(part uint32, stripe uint64) Obj

// Append is RGWObjManifest::append for rule-based manifests
// (rgw_obj_manifest.cc:44-127): the first part is adopted whole; a later
// part's rules are absorbed into the running rule when part size, stripe size,
// prefix and the expected next part number all match, else appended at
// ObjSize with OverridePrefix set when the part's prefix differs; ObjSize
// grows by the part's. An explicit manifest on either side is
// ErrExplicitManifest (radosgw-written parts are never explicit).
func (m *Manifest) Append(part Manifest) error

var ErrExplicitManifest = errors.New("meta: cannot append an explicit manifest")
```

```go
package rgw // internal/cls/rgw

const methodMPUploadPartInfoUpdate = "mp_upload_part_info_update" // RGW_MP_UPLOAD_PART_INFO_UPDATE, cls_rgw_const.h:68 [S], :71 [T]

// MPUploadPartInfoUpdateOp is cls_rgw_mp_upload_part_info_update_op
// (cls_rgw_ops.h:1457-1477 [S], :1495-1515 [T]): ENCODE_START(1, 1), the
// omap key and the RGWUploadPartInfo. Info is the part info's own encoding,
// carried raw because class packages do not import meta.
type MPUploadPartInfoUpdateOp struct {
	PartKey string
	Info    []byte
}

func (o MPUploadPartInfoUpdateOp) Encode(e *denc.Encoder, _ denc.Release)
func DecodeMPUploadPartInfoUpdateOp(d *denc.Decoder) MPUploadPartInfoUpdateOp

// MPUploadPartInfoUpdate adds mp_upload_part_info_update (cls_rgw.cc:4360-4400
// at v19.2.6, :4762 at v20.2.4): the class reads the part stored under
// partKey, folds its manifest prefix and past_prefixes into info's
// past_prefixes, fails with EEXIST when info's own prefix is among them, and
// writes the merged info. Registered RD|WR at both floors.
func MPUploadPartInfoUpdate(op radosclient.Execer, partKey string, info []byte, r denc.Release)
```

- [ ] **Step 1: Write the failing name-helper and type specs**

`internal/meta/multipart_test.go`:

```go
var _ = Describe("multipart names", func() {
	It("builds and parses radosgw's meta and part names", func() {
		Expect(meta.MultipartMetaName("multipart.bin", "2~AkTb")).To(Equal("multipart.bin.2~AkTb.meta"))
		Expect(meta.MultipartPrefix("multipart.bin", "2~AkTb")).To(Equal("multipart.bin.2~AkTb"))
		Expect(meta.MultipartPartName("multipart.bin.2~AkTb", 3)).To(Equal("multipart.bin.2~AkTb.3"))
		Expect(meta.MultipartPartKey(3)).To(Equal("part.00000003"))
		k, id, ok := meta.ParseMultipartMeta("a.b.c.2~AkTb.meta")
		Expect(ok).To(BeTrue())
		Expect(k).To(Equal("a.b.c"), "from_meta splits at the last two dots")
		Expect(id).To(Equal("2~AkTb"))
		k, id, ok = meta.ParseMultipartMeta("key..meta")
		Expect(ok).To(BeTrue())
		Expect([]string{k, id}).To(Equal([]string{"key", ""}), "an empty upload-id-marker: RGWMPObj(key, \"\")")
		_, _, ok = meta.ParseMultipartMeta("nodots")
		Expect(ok).To(BeFalse())
	})
	DescribeTable("IsMultipartMeta is MultipartMetaFilter",
		func(name string, want bool) { Expect(meta.IsMultipartMeta(name)).To(Equal(want)) },
		Entry("a meta object", "k.2~id.meta", true),
		Entry("a part head", "k.2~id.3", false),
		Entry("the bare suffix", ".meta", false),
		Entry("no dot before the suffix", "kmeta.meta", true), // svc_tier_rados.cc:24-27: rfind('.') before the suffix finds the suffix's own dot? No: pos-1 excludes it; "kmeta.meta" has no earlier dot → false
		Entry("suffix only after a key", "k.meta", false),
	)
	It("recognises both upload id prefixes", func() {
		Expect(meta.IsV2UploadID("2~x")).To(BeTrue())
		Expect(meta.IsV2UploadID("2/x")).To(BeTrue())
		Expect(meta.IsV2UploadID("x")).To(BeFalse())
	})
})
```

Fix the fourth table entry's expectation after reading `MultipartMetaFilter` once more: `pos = name.find(".meta", len-5)` finds the suffix; `pos = name.rfind('.', pos - 1)` searches from the character BEFORE the suffix's dot; `"kmeta.meta"` has no other dot → false. The entry's `want` is `false`; the comment above records the reasoning.

```go
var _ = Describe("UploadPartInfo", func() {
	part := meta.UploadPartInfo{Num: 2, Size: 8 << 20, ETag: "0123456789abcdef0123456789abcdef", Modified: time.Unix(1700000000, 0).UTC(),
		Manifest: meta.NewPartManifest(target, rule, rule, "k.2~id", 2, 4<<20), Compression: meta.NewCompressionInfo(), AccountedSize: 8 << 20, PastPrefixes: []string{"k.oldprefix"}}
	It("encodes v5 on Squid and v6 with an absent cksum on Tentacle, decoding both", func() {
		s := encodeAt(part, denc.Squid)
		t := encodeAt(part, denc.Tentacle)
		Expect(s[0]).To(Equal(byte(5)))
		Expect(t[0]).To(Equal(byte(6)))
		Expect(t).To(HaveLen(len(s)+1), "one presence byte")
		Expect(t[len(t)-1]).To(Equal(byte(0)))
		for _, b := range [][]byte{s, t} {
			d := denc.NewDecoder(b)
			got := meta.DecodeUploadPartInfo(d)
			Expect(d.Err()).NotTo(HaveOccurred())
			Expect(got).To(Equal(part))
		}
	})
	It("decodes v2 with accounted_size = size and no manifest", func() {
		e := denc.NewEncoder()
		f := e.BeginStruct(2, 2)
		e.U32(1); e.U64(10); e.String("e"); e.Time(time.Unix(1, 0))
		e.EndStruct(f)
		got := meta.DecodeUploadPartInfo(denc.NewDecoder(e.Bytes()))
		Expect(got.AccountedSize).To(BeEquivalentTo(10))
		Expect(got.Manifest.Rules).To(BeEmpty())
	})
	It("keeps a Tentacle checksum's bytes through a round trip", func() {
		// encode v6 by hand with presence 1 and a fake framed cksum {1,1,2,0,0,0,7,7}; decode; re-encode at Tentacle; identical
	})
})

var _ = Describe("MultipartUploadInfo", func() {
	It("encodes Squid's v2 and Tentacle's v4 and decodes v1..v4", func() {
		u := meta.MultipartUploadInfo{DestPlacement: meta.PlacementRule{Name: "default-placement", StorageClass: "COLD"}}
		s := encodeAt(u, denc.Squid)
		Expect(s).To(Equal(append(append([]byte{2, 1, 0x4c, 0, 0, 0}, // body: 4+22 placement, 1+1 bools, 26 retention, 10 legal hold = 64? recompute: 26 + 2 + 26 + 10 = 64 = 0x40
			[]byte{22, 0, 0, 0}...), append([]byte("default-placement/COLD"), 0, 0, 2, 1, 20, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 1, 4, 0, 0, 0, 0, 0, 0, 0)...)))
		t := encodeAt(u, denc.Tentacle)
		Expect(t[0]).To(Equal(byte(4)))
		Expect(t[len(t)-4:]).To(Equal([]byte{0, 0, 0, 0}), "cksum type none, flags 0")
		for _, b := range [][]byte{s, t} {
			d := denc.NewDecoder(b)
			Expect(meta.DecodeMultipartUploadInfo(d)).To(Equal(u))
			Expect(d.Err()).NotTo(HaveOccurred())
		}
		v1 := denc.NewEncoder()
		f := v1.BeginStruct(1, 1)
		u.DestPlacement.Encode(v1, denc.Squid)
		v1.EndStruct(f)
		Expect(meta.DecodeMultipartUploadInfo(denc.NewDecoder(v1.Bytes()))).To(Equal(u))
	})
	It("carries retention and legal hold when set", func() {
		until := time.Date(2030, 1, 2, 3, 4, 5, 6, time.UTC)
		u := meta.MultipartUploadInfo{DestPlacement: meta.PlacementRule{Name: "p"}, Retention: &meta.ObjectRetention{Mode: "GOVERNANCE", RetainUntil: until}, LegalHold: &meta.ObjectLegalHold{Status: "ON"}}
		b := encodeAt(u, denc.Squid)
		d := denc.NewDecoder(b)
		got := meta.DecodeMultipartUploadInfo(d)
		Expect(d.Err()).NotTo(HaveOccurred())
		Expect(got.Retention.RetainUntil).To(Equal(until), "round_trip_decode restores the nanoseconds the utime dropped")
		Expect(got.LegalHold.Status).To(Equal("ON"))
	})
})
```

Compute the v2 byte vector in the first spec by hand when writing the test (the expected value above is illustrative: body = placement string (4 + 22) + 2 bools + a default retention (6-byte header + 4 + 8 + 8 = 26) + a default legal hold (6 + 4 = 10) = 64 = 0x40, so the header's length byte is 0x40, not 0x4c; fix the literal) — the point of the literal is that a DEFAULT retention and legal hold occupy exactly 26 and 10 bytes, which the corpus goldens confirm in Step 5.

`internal/meta/manifest_multipart_test.go` (`decodeManifest` is R Task 1's helper; without it, read the file and call `meta.DecodeManifest`):

```go
var _ = Describe("part manifests", func() {
	bucket := meta.BucketID{Name: "plain", Marker: "m1", ID: "m1"}
	target := meta.Obj{Bucket: bucket, Key: meta.ObjKey{Name: "multipart.bin"}}
	rule := meta.PlacementRule{Name: "default-placement"}
	It("lays a part out as radosgw's MultipartObjectProcessor does", func() {
		m := meta.NewPartManifest(target, rule, meta.PlacementRule{}, "multipart.bin.2~id", 2, 4<<20)
		Expect(m.Obj).To(Equal(target))
		Expect(m.HeadSize).To(BeZero())
		Expect(m.MaxHeadSize).To(BeZero())
		Expect(m.Prefix).To(Equal("multipart.bin.2~id"))
		Expect(m.Rules).To(Equal(map[uint64]meta.ManifestRule{0: {StartPartNum: 2, StripeMaxSize: 4 << 20}}))
		Expect(m.TailPlacement).To(Equal(meta.BucketPlacement{Bucket: bucket, PlacementRule: rule}), "inherit_from the head rule")
		Expect(m.StripeObj(2, 0)).To(Equal(meta.Obj{Bucket: bucket, Key: meta.ObjKey{Name: "multipart.bin.2~id.2", NS: meta.NSMultipart}}), "the part head")
		Expect(m.StripeObj(2, 1)).To(Equal(meta.Obj{Bucket: bucket, Key: meta.ObjKey{Name: "multipart.bin.2~id.2_1", NS: meta.NSShadow}}))
		Expect(m.StripeObj(0, 1)).To(Equal(m.TailObj(1)), "W's TailObj is part 0")
		m.SetObjSize(6 << 20)
		stripes, err := m.Stripes()
		Expect(err).NotTo(HaveOccurred())
		Expect(stripes).To(HaveLen(2))
		Expect(stripes[0].OID()).To(Equal("m1__multipart_multipart.bin.2~id.2"))
		Expect(stripes[1].OID()).To(Equal("m1__shadow_multipart.bin.2~id.2_1"))
		Expect(stripes[1].Size).To(BeEquivalentTo(2 << 20))
	})
	It("appends parts into the rules the corpus multipart manifest carries", func() {
		// squid-multipart.bin: 20 MiB in parts of 8, 8 and 4 MiB, stripe 4 MiB
		golden := decodeManifest("testdata/manifests/squid-multipart.bin")
		var m meta.Manifest
		for i, size := range []uint64{8 << 20, 8 << 20, 4 << 20} {
			p := meta.NewPartManifest(target, rule, meta.PlacementRule{}, golden.Prefix, uint32(i+1), 4<<20)
			p.SetObjSize(size)
			Expect(m.Append(p)).To(Succeed())
		}
		Expect(m.ObjSize).To(BeEquivalentTo(20 << 20))
		Expect(m.Rules).To(Equal(golden.Rules), "rgw_obj_manifest.cc:44-118")
		Expect(m.Prefix).To(Equal(golden.Prefix))
		Expect(m.Obj).To(Equal(golden.Obj))
	})
	It("absorbs equal-sized parts, splits a shorter last part, and overrides a re-uploaded part's prefix", func() {
		var m meta.Manifest
		p1 := meta.NewPartManifest(target, rule, meta.PlacementRule{}, "k.2~id", 1, 4<<20); p1.SetObjSize(5 << 20)
		p2 := meta.NewPartManifest(target, rule, meta.PlacementRule{}, "k.RANDOMRANDOMRANDOMRANDOMRANDOM12", 2, 4<<20); p2.SetObjSize(5 << 20)
		p3 := meta.NewPartManifest(target, rule, meta.PlacementRule{}, "k.2~id", 3, 4<<20); p3.SetObjSize(1 << 20)
		Expect(m.Append(p1)).To(Succeed())
		Expect(m.Append(p2)).To(Succeed())
		Expect(m.Append(p3)).To(Succeed())
		Expect(m.Rules).To(Equal(map[uint64]meta.ManifestRule{
			0:       {StartPartNum: 1, StartOfs: 0, PartSize: 5 << 20, StripeMaxSize: 4 << 20},
			5 << 20: {StartPartNum: 2, StartOfs: 5 << 20, PartSize: 5 << 20, StripeMaxSize: 4 << 20, OverridePrefix: "k.RANDOMRANDOMRANDOMRANDOMRANDOM12"},
			10 << 20: {StartPartNum: 3, StartOfs: 10 << 20, PartSize: 1 << 20, StripeMaxSize: 4 << 20},
		}))
		Expect(m.ObjSize).To(BeEquivalentTo(11 << 20))
		stripes, err := m.Stripes()
		Expect(err).NotTo(HaveOccurred())
		Expect(stripes[2].OID()).To(Equal("m1__multipart_k.RANDOMRANDOMRANDOMRANDOMRANDOM12.2"), "the override prefix names part 2's objects")
	})
	It("refuses an explicit manifest", func() {
		var m meta.Manifest
		Expect(m.Append(meta.Manifest{ExplicitObjs: true})).To(MatchError(meta.ErrExplicitManifest))
	})
})
```

(`encodeAt` is the meta test helper the goldens use — `func encodeAt(v interface{ Encode(*denc.Encoder, denc.Release) }, r denc.Release) []byte`; add it to `export_test.go` or the suite if absent. In the third spec, the second rule's `OverridePrefix` and the absorption of nothing follow `RGWObjManifest::append` (rgw_obj_manifest.cc:44-118, identical at v19.2.6 and v20.2.4): p2's prefix differs → `append_rules` with the override; p3's part size 1 MiB differs from p2's 5 MiB → appended without override because its prefix equals `m.Prefix`.)

- [ ] **Step 2: Run the specs to see them fail**

Run: `go test -tags=ceph_preview ./internal/meta/`
Expected: compile failures on the new identifiers.

- [ ] **Step 3: Implement `multipart.go`, `objectlock.go`, `manifest_multipart.go`**

`multipart.go` (the type bodies):

```go
func (p UploadPartInfo) Encode(e *denc.Encoder, r denc.Release) {
	v := uint8(5)
	if r >= denc.Tentacle {
		v = 6
	}
	f := e.BeginStruct(v, 2)
	e.U32(p.Num)
	e.U64(p.Size)
	e.String(p.ETag)
	e.Time(p.Modified)
	p.Manifest.Encode(e, r)
	p.Compression.Encode(e, r)
	e.U64(p.AccountedSize)
	denc.EncodeSlice(e, slices.Sorted(slices.Values(p.PastPrefixes)), (*denc.Encoder).String)
	if v >= 6 {
		if p.Cksum == nil {
			e.U8(0)
		} else {
			e.Raw(p.Cksum) // the presence byte and the value, as decoded
		}
	}
	e.EndStruct(f)
}

func DecodeUploadPartInfo(d *denc.Decoder) UploadPartInfo {
	h := d.BeginStructLegacy(6, 2, 2, 0)
	p := UploadPartInfo{Num: d.U32(), Size: d.U64(), ETag: d.String(), Modified: d.Time(), Compression: NewCompressionInfo()}
	if h.Version >= 3 {
		p.Manifest = DecodeManifest(d)
	}
	if h.Version >= 4 {
		p.Compression = DecodeCompressionInfo(d)
		p.AccountedSize = d.U64()
	} else {
		p.AccountedSize = p.Size
	}
	if h.Version >= 5 {
		p.PastPrefixes = denc.DecodeSlice(d, (*denc.Decoder).String)
	}
	if h.Version >= 6 {
		p.Cksum = d.RestOfStruct(h) // the optional, presence byte included, to the struct's end
		if len(p.Cksum) == 1 && p.Cksum[0] == 0 {
			p.Cksum = nil
		}
	}
	d.EndStruct(h)
	return p
}
```

(`denc.Header` keeps its end offset unexported (`end int`, [frame.go:31-37](https://github.com/jhoblitt/rgw-go/blob/50fc45c4d59953661d5c57c27ca7016bc55edeeb/internal/denc/frame.go#L31-L37)), so this task adds `func (d *Decoder) RestOfStruct(h Header) []byte` to `internal/denc/frame.go`: the bytes from the current offset to `h.end`, nil when the header carried no length or nothing remains, with a spec in `frame_test.go` (a v2 struct with one trailing field read back after its known fields; an exhausted struct yields nil). `DecodeSlice` of strings yields the sorted set as stored; `Encode` sorts defensively.) `MultipartUploadInfo.Encode`: `v = 2` or `4`; `BeginStruct(v, 1)`; `DestPlacement.Encode`; `e.Bool(Retention != nil)`; `e.Bool(LegalHold != nil)`; `deref(Retention).Encode`; `deref(LegalHold).Encode`; `if v >= 4 { e.U16(CksumType); e.U16(CksumFlags) }`. Decode: `h := d.BeginStructLegacy(4, 1, 1, 0)`; placement; `if h.Version >= 2 { re, le := d.Bool(), d.Bool(); ret := DecodeObjectRetention(d); lh := DecodeObjectLegalHold(d); if re { u.Retention = &ret }; if le { u.LegalHold = &lh }; if h.Version >= 3 { u.CksumType = d.U16(); if h.Version >= 4 { u.CksumFlags = d.U16() } } }`. Note Squid's decoder is `DECODE_START(2)` (compat check against 2) and Tentacle's the legacy-compat form; both accept what the other writes (compat 1), which is what the cross-release goldens in Step 5 prove for the v2 form.

`objectlock.go`:

```go
func (o ObjectRetention) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(2, 1)
	e.String(o.Mode)
	e.Time(o.RetainUntil)
	var ns int64
	if !o.RetainUntil.IsZero() {
		ns = o.RetainUntil.UnixNano()
	}
	e.I64(ns) // ceph::round_trip_encode of a real_time: its nanosecond count
	e.EndStruct(f)
}

func DecodeObjectRetention(d *denc.Decoder) ObjectRetention {
	h := d.BeginStruct(2)
	o := ObjectRetention{Mode: d.String(), RetainUntil: d.Time()}
	if h.Version >= 2 {
		if ns := d.I64(); ns != 0 {
			o.RetainUntil = time.Unix(0, ns).UTC()
		}
	}
	d.EndStruct(h)
	return o
}

func (l ObjectLegalHold) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	e.String(l.Status)
	e.EndStruct(f)
}

func DecodeObjectLegalHold(d *denc.Decoder) ObjectLegalHold {
	h := d.BeginStruct(1)
	l := ObjectLegalHold{Status: d.String()}
	d.EndStruct(h)
	return l
}
```

`manifest_multipart.go`:

```go
func NewPartManifest(target Obj, headRule, tailRule PlacementRule, prefix string, partNum uint32, stripeSize uint64) Manifest {
	m := NewManifest()
	m.Obj = target
	m.HeadPlacementRule = headRule
	m.Prefix = prefix
	m.Rules = map[uint64]ManifestRule{0: {StartPartNum: partNum, StripeMaxSize: stripeSize}}
	m.TailPlacement = BucketPlacement{Bucket: target.Bucket, PlacementRule: tailRule.InheritFrom(headRule)}
	m.TailInstance = target.Key.Instance
	return m
}

func (m Manifest) StripeObj(part uint32, stripe uint64) Obj {
	prefix := m.Prefix
	if r, ok := m.Rules[0]; ok && r.OverridePrefix != "" {
		prefix = r.OverridePrefix
	}
	var name, ns string
	switch {
	case part == 0:
		name, ns = prefix+strconv.FormatUint(stripe, 10), NSShadow
	case stripe == 0:
		name, ns = prefix+"."+strconv.FormatUint(uint64(part), 10), NSMultipart
	default:
		name, ns = prefix+"."+strconv.FormatUint(uint64(part), 10)+"_"+strconv.FormatUint(stripe, 10), NSShadow
	}
	bucket := m.Obj.Bucket
	if m.TailPlacement.Bucket.Name != "" {
		bucket = m.TailPlacement.Bucket
	}
	return Obj{Bucket: bucket, Key: ObjKey{Name: name, Instance: m.TailInstance, NS: ns}}
}

func (m *Manifest) Append(part Manifest) error {
	if m.ExplicitObjs || part.ExplicitObjs {
		return ErrExplicitManifest
	}
	if len(m.Rules) == 0 {
		*m = part
		m.Rules = maps.Clone(part.Rules)
		return nil
	}
	if m.Prefix == "" {
		m.Prefix = part.Prefix
	}
	keys := sortedKeys(part.Rules)
	if len(keys) == 0 {
		return ErrExplicitManifest
	}
	for i, k := range keys {
		lastKey := slices.Max(sortedKeys(m.Rules))
		rule := m.Rules[lastKey]
		if rule.PartSize == 0 {
			rule.PartSize = m.ObjSize - rule.StartOfs
			m.Rules[lastKey] = rule
		}
		next := part.Rules[k]
		if next.PartSize == 0 {
			next.PartSize = part.ObjSize - next.StartOfs
		}
		rulePrefix := cmp.Or(rule.OverridePrefix, m.Prefix)
		nextPrefix := cmp.Or(next.OverridePrefix, part.Prefix)
		if rule.PartSize != next.PartSize || rule.StripeMaxSize != next.StripeMaxSize || rulePrefix != nextPrefix {
			var override string
			if nextPrefix != m.Prefix {
				override = nextPrefix
			}
			m.appendRules(part, keys[i:], override)
			break
		}
		expected := rule.StartPartNum + 1
		if rule.PartSize > 0 {
			expected = rule.StartPartNum + uint32((m.ObjSize+next.StartOfs-rule.StartOfs)/rule.PartSize) //nolint:gosec // part counts are small
		}
		if expected != next.StartPartNum {
			m.appendRules(part, keys[i:], "")
			break
		}
	}
	m.ObjSize += part.ObjSize
	return nil
}

// appendRules is RGWObjManifest::append_rules: the part's remaining rules,
// shifted by the current size, with the override prefix when given.
func (m *Manifest) appendRules(part Manifest, keys []uint64, override string) {
	for _, k := range keys {
		r := part.Rules[k]
		if r.PartSize == 0 {
			r.PartSize = part.ObjSize - r.StartOfs
		}
		r.StartOfs += m.ObjSize
		if override != "" {
			r.OverridePrefix = override
		}
		m.Rules[r.StartOfs] = r
	}
}
```

The C++ `append_rules` copies the rule as it stands in `m.rules` — whose `part_size` the loop above patched in place (`next_rule.part_size = m.obj_size - next_rule.start_ofs` mutates `m.rules`' entry through the iterator) — so the Go `appendRules` recomputes a zero `PartSize` the same way; the two agree because the incoming manifest has exactly one rule in radosgw's part case, and the spec's corpus comparison proves the outcome.

- [ ] **Step 4: Run the meta specs**

Run: `go test -tags=ceph_preview ./internal/meta/`. Expected: PASS.

- [ ] **Step 5: Goldens for the three types**

`hack/goldens/types.txt` gains `RGWUploadPartInfo internal/meta`, `multipart_upload_info internal/meta`, `RGWObjectRetention internal/meta`, `RGWObjectLegalHold internal/meta` and `cls_rgw_mp_upload_part_info_update_op internal/cls/rgw` (the corpus at 19.2.0-404-g78ddc7f9027 holds 10, 3, 1, 1 and 10 objects; the two object-lock types get their own `It`s with `meta.DecodeObjectRetention`/`DecodeObjectLegalHold`). Run `hack/goldens/gen.sh`. `internal/meta/goldens_test.go` gains, in the existing `Describe`, with `SkipJSON: true` (neither type's `dump` is compared elsewhere; `RGWUploadPartInfo::dump` prints only num/size/etag/modified/past_prefixes):

```go
It("RGWUploadPartInfo", func() {
	goldentest.RoundTrip(dir, "RGWUploadPartInfo", goldentest.Options{Release: denc.Squid, SkipJSON: true}, meta.DecodeUploadPartInfo,
		func(e *denc.Encoder, v meta.UploadPartInfo, r denc.Release) { v.Encode(e, r) })
})
It("multipart_upload_info", func() {
	goldentest.RoundTrip(dir, "multipart_upload_info", goldentest.Options{Release: denc.Squid, SkipJSON: true}, meta.DecodeMultipartUploadInfo,
		func(e *denc.Encoder, v meta.MultipartUploadInfo, r denc.Release) { v.Encode(e, r) })
})
```

The corpus objects were written by a v19 dencoder, so their re-encoding at `denc.Squid` (v5 / v2) must be byte-identical (`.reenc`). `internal/cls/rgw/goldens_test.go` gains `cls_rgw_mp_upload_part_info_update_op` with `rgw.DecodeMPUploadPartInfoUpdateOp` — after Step 6 defines it. Run the two packages' tests; expected PASS. A failure on `RGWUploadPartInfo` means the manifest or compression encoders drifted (they are phase 0's, proven separately) or `past_prefixes` ordering; a failure on `multipart_upload_info` pins the default retention/legal-hold byte layout (a default `RGWObjectRetention` is 26 bytes: header, empty mode, zero `utime_t`, then `round_trip_encode`'s int64 nanoseconds, ceph_time.h:105-112; a default `RGWObjectLegalHold` is 10).

- [ ] **Step 6: `cls/rgw` part-info update and its fake**

`internal/cls/rgw/ops_mp.go`:

```go
package rgw

type MPUploadPartInfoUpdateOp struct {
	PartKey string
	Info    []byte
}

// Encode mirrors cls_rgw_mp_upload_part_info_update_op::encode, ENCODE_START(1, 1).
func (o MPUploadPartInfoUpdateOp) Encode(e *denc.Encoder, _ denc.Release) {
	f := e.BeginStruct(1, 1)
	e.String(o.PartKey)
	e.Raw(o.Info)
	e.EndStruct(f)
}

// DecodeMPUploadPartInfoUpdateOp mirrors its decode, DECODE_START(1); the
// RGWUploadPartInfo is carried as its framed bytes.
func DecodeMPUploadPartInfoUpdateOp(d *denc.Decoder) MPUploadPartInfoUpdateOp {
	h := d.BeginStruct(1)
	o := MPUploadPartInfoUpdateOp{PartKey: d.String(), Info: clsutil.ReadFramed(d)}
	d.EndStruct(h)
	return o
}

func MPUploadPartInfoUpdate(op radosclient.Execer, partKey string, info []byte, r denc.Release) {
	op.Exec(Class, methodMPUploadPartInfoUpdate, clsutil.Encode(MPUploadPartInfoUpdateOp{PartKey: partKey, Info: info}, r))
}
```

`ops_test.go` gains `It("encodes mp_upload_part_info_update with the raw part info")`: build an op with `PartKey: "part.00000001"` and `Info: []byte{5, 2, 4, 0, 0, 0, 1, 0, 0, 0}` (a pretend v5 frame), expect the exec step's `In` to be `[1 1 <len> 0 0 0] + [13 0 0 0 "part.00000001"] + info` and the decoder to return the same struct. `fakerados/cls_rgw.go`'s `RGWClass` gains:

```go
case "mp_upload_part_info_update":
	d := denc.NewDecoder(in)
	op := rgwcls.DecodeMPUploadPartInfoUpdateOp(d)
	if d.Err() != nil {
		return nil, errno(syscall.EINVAL), false
	}
	if obj == nil {
		return nil, errno(syscall.ENOENT), false
	}
	nd := denc.NewDecoder(op.Info)
	info := meta.DecodeUploadPartInfo(nd)
	if nd.Err() != nil {
		return nil, errno(syscall.EINVAL), false
	}
	if stored, ok := obj.Omap[op.PartKey]; ok {
		sd := denc.NewDecoder(stored)
		old := meta.DecodeUploadPartInfo(sd)
		if sd.Err() != nil {
			return nil, errno(syscall.EIO), false
		}
		if old.Manifest.Prefix != "" {
			info.PastPrefixes = append(info.PastPrefixes, old.Manifest.Prefix)
		}
		info.PastPrefixes = append(info.PastPrefixes, old.PastPrefixes...)
		slices.Sort(info.PastPrefixes)
		info.PastPrefixes = slices.Compact(info.PastPrefixes)
	}
	if slices.Contains(info.PastPrefixes, info.Manifest.Prefix) {
		return nil, errno(syscall.EEXIST), false // cls_rgw.cc:4383-4392
	}
	e := denc.NewEncoder()
	info.Encode(e, denc.Squid)
	if obj.Omap == nil {
		obj.Omap = map[string][]byte{}
	}
	obj.Omap[op.PartKey] = e.Bytes()
	return nil, 0, false
```

(`fakerados` may import `meta`; the class packages may not.) `cls_rgw_test.go` pins: a first update stores the info; a second under the same key with a new prefix carries the old prefix in `PastPrefixes`; a third whose prefix equals a past one is EEXIST.

- [ ] **Step 7: Check and commit**

Run: `make check`. Expected: green.

```bash
git add internal/meta internal/cls/rgw internal/cls/internal/clsutil internal/testutil/fakerados/cls_rgw.go internal/testutil/fakerados/cls_rgw_test.go hack/goldens/types.txt
git commit -m "feat(meta): multipart upload info, part info and part manifests

The meta object's data is multipart_upload_info and each registered
part is an RGWUploadPartInfo in its omap; both are encoded at the
cluster release (v2/v5 on Squid, v4/v6 on Tentacle with no checksum)
and decoded at every version, proven against the corpus goldens. Part
manifests are laid out as MultipartObjectProcessor lays them out and
assembled with RGWObjManifest::append's rule arithmetic, reproducing
the corpus multipart manifest's rules. cls/rgw gains
mp_upload_part_info_update, which both floors register.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3: `driver`: layout helpers, `readMeta`, W's additive extensions, `CreateUpload`, `GetUpload`

**Files:**
- Create: `internal/driver/mp_layout.go`, `internal/driver/mp_upload.go`, `internal/driver/mp_layout_test.go`, `internal/driver/mp_upload_test.go`
- Modify: `internal/driver/headwrite.go` (W Task 5: `headWrite` fields `atomic`, `pool`, `oid`, `loc`, `category`, `completeMultipart`; `doWriteMeta`'s non-atomic branch and the pool override), `internal/driver/indexop.go` (W Task 4: `indexOp.hashName`, `indexOp.removeObjs`), `internal/driver/store.go` (the `CreateUpload`/`GetUpload` NotImplemented stubs go), `internal/driver/export_test.go`, `internal/driver/headwrite_test.go` (two specs for the new branches), `internal/testutil/fakerados/pool.go` only if `Cluster.Object` cannot address the extra pool's namespace (Rook's `<store>.rgw.buckets.non-ec` is a namespace of a shared pool: `meta.Pool{Name, NS}`; M's `poolCache.get` already opens by both), `docs/exclusions.md` (coexistence section: the client-checksum difference of P-D5, Step 7)

**Interfaces:**
- Consumes: G's `op.BucketRecord`, `op.Upload`, `op.UploadParams`, `op.ErrNoSuchUpload`, `op.ErrInternalError`, `op.FromRADOS`, `op.ScopeUpload`; M's `Store.pools.get(ctx, meta.Pool)`, `Store.Placement`, `Store.zone` (`ZoneGroup.DefaultPlacement`, `Params.PlacementPools`); R's `dataPool`-style fallback (R Task 3), `readHead`'s attr filtering rules (`user.rgw.` prefix; `RadosMultipartUpload::get_info` takes the upload's attrs from the meta head, [rgw_sal_rados.cc:3642-3717](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L3642-L3717) at v19.2.6); W's `writer` (`randAlnum`, `randTag`, `shortZoneID`), `headWrite`, `writeMeta`, `indexOp`, `newIndexOp`, `entryOwner`, `encodeAt`; `version.Read` ([`internal/cls/version/ops.go:53`](https://github.com/jhoblitt/rgw-go/blob/50fc45c4d59953661d5c57c27ca7016bc55edeeb/internal/cls/version/ops.go#L53)), `version.ObjVersion`; `meta.MultipartMetaName`, `MultipartUploadInfo`, `DecodeMultipartUploadInfo`, `NSMultipart`, `Stripe{Obj}.OID()`, `ObjKey.Locator`; `rgw.CategoryMultiMeta`, `rgw.ObjKey`; `radosclient.ReadOp` (`GetXattrs`, `Stat`, `ReadInto`), `radosclient.Pool.Read`, `ErrNotFound`.
- Produces:

```go
package driver

// mpRef locates one object of an upload in RADOS: the meta object in the
// placement's data-extra pool, or a part head or stripe in the tail rule's
// data pool. A namespaced key has no locator (rgw_obj_key::get_loc).
type mpRef struct {
	pool radosclient.Pool
	oid  string
	loc  string
	key  meta.ObjKey // Name "<key>.<id>.meta" or "<prefix>.<n>", NS multipart
}

// extraPool is rgw_get_obj_data_pool for an object with in_extra_data set
// (rgw_obj_manifest.cc:412-428, rgw_zone.h get_head_data_pool, rgw_zone_types.h
// :274-280): the bucket rule's data_extra_pool, or STANDARD's data pool when
// the placement names none (Placement already applies that fallback), or the
// zonegroup default placement's extra pool when the rule is unknown to the
// zone; ok false when nothing resolves (radosgw's -EIO). The storage class
// plays no part: a COLD upload's meta object still sits in the extra pool.
func (s *Store) extraPool(rule meta.PlacementRule) (meta.Pool, bool)

// metaRef is the meta object of upload id under key in rec: oid
// "<marker>_" + ObjKey{Name: MultipartMetaName(key, id), NS: multipart}.OID().
func (s *Store) metaRef(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, uploadID string) (mpRef, error)

// metaState is one read of the meta object.
type metaState struct {
	ref     mpRef
	size    uint64
	mtime   time.Time
	attrs   map[string][]byte  // the user.rgw.* xattrs (rgw_filter_attrset)
	version version.ObjVersion // ceph.objclass.version; Ver 0 when the object has none
	info    meta.MultipartUploadInfo
}

// readMeta is RadosMultipartUpload::get_info + get_obj_attrs in ONE op, as
// raw_obj_stat with a version tracker and a first chunk composes it
// (rgw_rados.cc:8827-8870; get_obj_state_impl :6150): [cls_version_read,
// getxattrs, stat2, read(0, rgw_max_chunk_size)]. ENOENT and an empty head are
// op.ErrNoSuchUpload (rgw_sal_rados.cc:3680-3703); an undecodable
// multipart_upload_info is op.ErrInternalError (-EIO, :3706-3711).
func (s *Store) readMeta(ctx context.Context, ref mpRef) (*metaState, error)

// uploadFromMeta builds the op.Upload from a meta read: the owner is
// the meta object's ACL owner (what ListParts renders), Initiated its mtime,
// Placement the stored dest_placement, Attrs its xattrs.
func uploadFromMeta(rec *op.BucketRecord, key meta.ObjKey, id string, st *metaState) *op.Upload

func (s *Store) CreateUpload(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, p op.UploadParams) (*op.Upload, error)
func (s *Store) GetUpload(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, uploadID string) (*op.Upload, error)
```

Additive fields on W's types:

```go
package driver

// headWrite gains:
//	// atomic false takes prepare_atomic_modification's non-atomic branch
//	// (rgw_rados.cc:6497-6505): no guard, no preconditions, no idtag or
//	// tail_tag, and create means create(false) + obj_remove regardless of
//	// the object's existence. The multipart meta object and part heads are
//	// written this way, as radosgw never calls set_atomic on them; every
//	// other head write is atomic.
//	atomic bool
//	// pool, oid and loc place the head somewhere other than the bucket's data
//	// pool under the key's oid — the meta object in the data-extra pool, a
//	// part head in the tail rule's pool. nil pool keeps headRef's resolution.
//	pool radosclient.Pool
//	oid, loc string
//	// category labels the index entry; 0 means Main (MultiMeta for the meta object).
//	category uint8
//	// completeMultipart adds no bytes to the quota cache (the parts already
//	// did): update_stats(owner, bucket, added, 0, orig_size) (rgw_rados.cc:3359-3365).
//	completeMultipart bool

// indexOp gains:
//	// hashName replaces key.Name as the shard hash source (rgw_obj::index_hash_source,
//	// rgw_obj_types.h:540-541): a part head and the meta object hash the upload's key.
//	hashName string
//	// removeObjs ride on the complete, complete_del and cancel that closes this op
//	// (rgw_cls_obj_complete_op::remove_objs): the part entries CompleteMultipart
//	// and Abort retire in the same class call.
//	removeObjs []rgw.ObjKey
```

`W-D6`'s tag rule is unchanged: the callers here pass `""` and `newIndexOp` picks `randTag()`, which is what `UpdateIndex::prepare` does for a non-atomic write whose `write_tag` is empty ([rgw_rados.cc:7087-7093](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L7087-L7093) at v19.2.6). W's `headWrite.tag` is then unused for a non-atomic write (no idtag is set); the index op's tag is the one that matters.

- [ ] **Step 1: Extend W's types and `doWriteMeta`**

In `headwrite.go`, add the fields above. In `doWriteMeta`, replace the head-ref line and the guard/create block with the following; the atomic branch is W's existing code, unchanged:

```go
	var ref objRef
	if hw.pool != nil {
		ref = objRef{pool: hw.pool, oid: hw.oid, loc: hw.loc}
	} else {
		var err error
		if ref, err = s.headRef(ctx, hw.rec, hw.key); err != nil {
			return headResult{}, err
		}
	}
	w := radosclient.NewWriteOp()
	if hw.atomic {
		// prepare_atomic_modification's atomic branch: release-ordered
		// checkPreconditions, the guard cmpxattr, create(true) /
		// create(false)+obj_remove by existence, idtag and tail_tag.
	} else if hw.create {
		// rgw_rados.cc:6500-6503: a non-atomic reset removes and recreates without
		// a guard; nothing about the object's state is consulted.
		w.Create(false)
		rgw.ObjRemove(w, []string{meta.AttrOLHPrefix}, s.release)
	}
```

and, where the index entry is built, `Category: cmp.Or(hw.category, rgw.CategoryMain)`; where the quota is adjusted, `addBytes := int64(hw.accountedSize); if hw.completeMultipart { addBytes = 0 }`. The `errRetryGuarded` return stays behind `hw.atomic && assumeNoent && errors.Is(err, radosclient.ErrExists)` (a non-atomic `create(false)` never answers EEXIST). `writeMeta` itself: when `!hw.atomic`, skip the `readHead` that preconditions would need (there are none) and call `doWriteMeta(ctx, hw, x, &op.ObjectState{Key: hw.key}, true)` directly.

In `indexop.go`: `indexShardFor` is called with a key whose `Name` is `cmp.Or(x.hashName, x.obj.Key.Name)` (the NS-less name: `rgw_obj::get_hash_object()` returns `index_hash_source` or `key.name`; `BucketShard::init` hashes exactly that string with `rgw_bucket_shard_index` — M's `meta.IndexShard(name, shards)`); `complete`, `completeDel` and `cancel` set `RemoveObjs: x.removeObjs` on the `rgw.CompleteOp` they build. `export_test.go` exports `SetHashNameForTest`/`SetRemoveObjsForTest` on `IndexOp`, or the test constructs through the new driver entry points below.

`headwrite_test.go` gains:

```go
It("writes a non-atomic head with create(false), obj_remove and no idtag", func(ctx SpecContext) {
	hw := s.NewHeadWriteForTest(rec, meta.ObjKey{Name: "m.2~id.meta", NS: meta.NSMultipart}) // returns *headWrite with rec/key set
	hw.SetNonAtomicCreateForTest(extraPool, rec.Info.Bucket.Marker+"__multipart_m.2~id.meta", []byte("data"), rgwcls.CategoryMultiMeta, "m")
	x := s.NewIndexOpForTest(rec, hw.Key(), "")
	res, err := s.WriteMetaForTest(ctx, hw, x)
	Expect(err).NotTo(HaveOccurred())
	steps := c.LastWrite(extraPoolName, extraNS, hw.OID()).Steps()
	Expect(steps[0]).To(Equal(&radosclient.CreateStep{Exclusive: false}))
	Expect(steps[1].(*radosclient.ExecStep).Method).To(Equal("obj_remove"))
	Expect(steps[2]).To(BeAssignableToTypeOf(&radosclient.WriteFullStep{}))
	names := stepNames(steps)
	Expect(names).NotTo(ContainElement("user.rgw.idtag"))
	Expect(names).NotTo(ContainElement("user.rgw.tail_tag"))
	Expect(names).NotTo(ContainElement("user.rgw.manifest"))
	Expect(c.Writes(indexPool, "", shardOf(rec, "m"))).To(Equal(1), "hashed by the upload key, not the meta name")
	Eventually(func() uint8 { en, _ := c.Entry(indexPool, "", shardOf(rec, "m"), "_multipart_m.2~id.meta"); return en.Meta.Category }).Should(Equal(rgwcls.CategoryMultiMeta))
	_ = res
})
It("removes the listed entries in the same complete", func(ctx SpecContext) {
	// seed two Main entries "p1" and "p2" in the shard of "k"; a headWrite of "k" with removeObjs {p1, p2}; after the complete both are gone and Stats[Main].NumEntries dropped by two (complete_remove_obj, cls_rgw.cc)
})
```

(The fake's `bucket_complete_op` must honour `RemoveObjs`: extend `rgwCompleteOp` in `fakerados/cls_rgw.go` — for each key, read the entry, `unaccount`, delete the omap key; a missing key is skipped, as `complete_remove_obj` logs and moves on.)

- [ ] **Step 2: Write the failing layout and upload specs**

`internal/driver/mp_layout_test.go`, on W's Task 5 fixture (`seedRookZone`, `driver.Open`, `RGWClass`, `VersionClass`; the zone seed's default placement has `data_extra_pool` `ceph-objectstore.rgw.buckets.non-ec`, per M's zone spec [:281](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_zone_types.h#L281)):

```go
var _ = Describe("multipart layout", func() {
	It("puts the meta object in the placement's data-extra pool whatever the storage class", func(ctx SpecContext) {
		ref, err := s.MetaRefForTest(ctx, rec, meta.ObjKey{Name: "multipart.bin"}, "2~AkTb")
		Expect(err).NotTo(HaveOccurred())
		Expect(ref.Pool.Name()).To(Equal("ceph-objectstore.rgw.buckets.non-ec"))
		Expect(ref.OID).To(Equal(rec.Info.Bucket.Marker + "__multipart_multipart.bin.2~AkTb.meta"))
		Expect(ref.Loc).To(BeEmpty())
		cold := *rec
		cold.Info.PlacementRule = meta.PlacementRule{Name: "default-placement", StorageClass: "COLD"}
		ref2, err := s.MetaRefForTest(ctx, &cold, meta.ObjKey{Name: "multipart.bin"}, "2~AkTb")
		Expect(err).NotTo(HaveOccurred())
		Expect(ref2.Pool.Name()).To(Equal("ceph-objectstore.rgw.buckets.non-ec"), "get_data_extra_pool ignores the class")
	})
	It("falls back to STANDARD's data pool when the placement has no extra pool, and to the default placement for an unknown rule", func(ctx SpecContext) {
		// a zone seed variant without data_extra_pool → the data pool; a bucket rule "gone" → the zonegroup default placement's extra pool (rgw_obj_manifest.cc:412-428)
	})
	It("reads the meta object in one op composing version, xattrs, stat and the first chunk", func(ctx SpecContext) {
		seedMeta(c, s, rec, "multipart.bin", "2~AkTb", meta.PlacementRule{Name: "default-placement", StorageClass: "COLD"}, attrsWithACL(alice), version.ObjVersion{Ver: 3, Tag: "t"})
		c.ResetCounters()
		st, err := s.ReadMetaForTest(ctx, rec, meta.ObjKey{Name: "multipart.bin"}, "2~AkTb")
		Expect(err).NotTo(HaveOccurred())
		Expect(c.Reads(extraPoolName, extraNS, st.Ref.OID)).To(Equal(1))
		steps := c.LastRead(extraPoolName, extraNS, st.Ref.OID).Steps()
		Expect(steps[0].(*radosclient.ExecStep).Method).To(Equal("read"), "cls_version_read first: RGWObjVersionTracker::prepare_op_for_read")
		Expect(steps[0].(*radosclient.ExecStep).Class).To(Equal("version"))
		Expect(steps[1]).To(BeAssignableToTypeOf(&radosclient.GetXattrsStep{}))
		Expect(steps[2]).To(BeAssignableToTypeOf(&radosclient.StatStep{}))
		Expect(steps[3].(*radosclient.ReadStep).Length).To(BeEquivalentTo(4 << 20))
		Expect(st.Info.DestPlacement).To(Equal(meta.PlacementRule{Name: "default-placement", StorageClass: "COLD"}))
		Expect(st.Version).To(Equal(version.ObjVersion{Ver: 3, Tag: "t"}))
		Expect(st.Attrs).To(HaveKey(meta.AttrACL))
	})
	DescribeTable("readMeta's refusals",
		func(seed func(), want error) { seed(); _, err := s.ReadMetaForTest(ctx, rec, meta.ObjKey{Name: "k"}, "2~x"); Expect(err).To(MatchError(want)) },
		Entry("a missing object is NoSuchUpload", func() {}, op.ErrNoSuchUpload),
		Entry("an empty head is NoSuchUpload", func() { c.Put(extraPoolName, extraNS, metaOID(rec, "k", "2~x"), nil) }, op.ErrNoSuchUpload),
		Entry("garbage is InternalError", func() { c.Put(extraPoolName, extraNS, metaOID(rec, "k", "2~x"), []byte{0xff, 0xff, 0xff}) }, op.ErrInternalError),
	)
})
```

(`c.LastRead` is a read-op twin of W's `LastWrite`; add it to fakerados if M/W did not. `seedMeta` writes the object radosgw would: data = `encodeAt(MultipartUploadInfo{DestPlacement}, denc.Squid)`, the attrs, `ceph.objclass.version` = the encoded version when given, in the extra pool's namespace, plus an index entry `_multipart_<key>.<id>.meta` of category MultiMeta in the shard of `<key>` — the helper P's later specs reuse.)

`internal/driver/mp_upload_test.go`:

```go
var _ = Describe("CreateUpload and GetUpload", func() {
	BeforeEach(func() { s.SetRandForTest(fixedRand("AkTbQPoXT3s8QcthZt-DpS5NPRLqS5uQ9")) }) // 32 characters for the id, 31 for tags: fixedRand returns n characters of the seed
	It("writes radosgw's meta object and index entry", func(ctx SpecContext) {
		attrs := map[string][]byte{meta.AttrACL: encode(acl.DefaultPolicy(alice, "Alice")), meta.AttrContentType: []byte("text/plain\x00"), meta.AttrMetaPrefix + "k": []byte("v\x00")}
		up, err := s.CreateUpload(ctx, rec, key, op.UploadParams{Owner: alice, OwnerName: "Alice", Placement: meta.PlacementRule{Name: "default-placement", StorageClass: "COLD"}, Attrs: attrs})
		Expect(err).NotTo(HaveOccurred())
		Expect(up.ID).To(Equal("2~AkTbQPoXT3s8QcthZt-DpS5NPRLqS5uQ9"))
		Expect(up.Key).To(Equal(key))
		Expect(up.Placement.StorageClass).To(Equal("COLD"))
		oid := rec.Info.Bucket.Marker + "__multipart_k.2~AkTbQPoXT3s8QcthZt-DpS5NPRLqS5uQ9.meta"
		obj := c.Object(extraPoolName, extraNS, oid)
		Expect(obj).NotTo(BeNil(), "the meta object lives in the data-extra pool")
		info := meta.DecodeMultipartUploadInfo(denc.NewDecoder(obj.Data))
		Expect(info).To(Equal(meta.MultipartUploadInfo{DestPlacement: meta.PlacementRule{Name: "default-placement", StorageClass: "COLD"}}))
		Expect(obj.Xattrs).To(HaveKey(meta.AttrACL))
		Expect(obj.Xattrs).To(HaveKeyWithValue(meta.AttrContentType, []byte("text/plain\x00")))
		Expect(obj.Xattrs).To(HaveKey(meta.AttrPGVer))
		Expect(obj.Xattrs).To(HaveKey(meta.AttrSourceZone))
		Expect(obj.Xattrs).NotTo(HaveKey(meta.AttrIDTag), "RadosMultipartUpload::init never sets the object atomic")
		Expect(obj.Xattrs).NotTo(HaveKey(meta.AttrManifest))
		Expect(obj.Xattrs).NotTo(HaveKey(meta.AttrStorageClass))
		Expect(obj.Xattrs).NotTo(HaveKey(meta.AttrETag))
		steps := c.LastWrite(extraPoolName, extraNS, oid).Steps()
		Expect(steps[0]).To(Equal(&radosclient.CreateStep{Exclusive: false}), "PUT_OBJ_EXCL is consulted nowhere: create(false)")
		Expect(steps[1].(*radosclient.ExecStep).Method).To(Equal("obj_remove"))
		Expect(steps[2]).To(BeAssignableToTypeOf(&radosclient.WriteFullStep{}))
		mt, ok := c.LastWrite(extraPoolName, extraNS, oid).Mtime()
		Expect(ok).To(BeTrue())
		Expect(mt).To(Equal(up.Initiated))

		shard := shardOf(rec, "k")
		Eventually(func() int { return c.Writes(indexPool, "", shard) }).Should(Equal(2), "prepare and complete on the shard of the UPLOAD key")
		en, ok := c.Entry(indexPool, "", shard, "_multipart_k.2~AkTbQPoXT3s8QcthZt-DpS5NPRLqS5uQ9.meta")
		Expect(ok).To(BeTrue())
		Expect(en.Meta.Category).To(Equal(rgwcls.CategoryMultiMeta))
		Expect(en.Meta.Size).To(BeEquivalentTo(len(obj.Data)))
		Expect(en.Meta.AccountedSize).To(BeZero())
		Expect(en.Meta.Owner).To(Equal("alice"))
		Expect(en.Meta.OwnerDisplayName).To(Equal("Alice"))
		Expect(en.Meta.ContentType).To(Equal("text/plain"))
		Expect(en.Meta.ETag).To(BeEmpty())
		Expect(en.Tag).To(HavePrefix("_"), "a random index tag: the write is non-atomic")
		Expect(en.Tag).To(HaveLen(32))
		Expect(c.Header(indexPool, "", shard).Stats[rgwcls.CategoryMultiMeta].NumEntries).To(BeEquivalentTo(1))
		Expect(memStatsAdjustments()).To(ContainElement(adjustment{objs: 1, bytes: 0, removed: 0}), "update_stats(owner, bucket, 1, 0, 0)")
	})
	It("reads the upload back with the ACL owner, the mtime and the placement", func(ctx SpecContext) {
		created, _ := s.CreateUpload(ctx, rec, key, op.UploadParams{Owner: alice, OwnerName: "Alice", Placement: meta.PlacementRule{Name: "default-placement"}, Attrs: attrsWithACL(alice)})
		up, err := s.GetUpload(ctx, rec, key, created.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(up.Owner).To(Equal(alice))
		Expect(up.OwnerName).To(Equal("Alice"))
		Expect(up.Initiated).To(Equal(created.Initiated))
		Expect(up.Placement).To(Equal(meta.PlacementRule{Name: "default-placement"}))
		Expect(up.Attrs).To(HaveKey(meta.AttrACL))
	})
	It("answers NoSuchUpload for an unknown id", func(ctx SpecContext) {
		_, err := s.GetUpload(ctx, rec, key, "2~nope")
		Expect(err).To(MatchError(op.ErrNoSuchUpload))
	})
	It("encodes Tentacle's v4 upload info with no checksum", func(ctx SpecContext) {
		t := denc.Tentacle
		s2, _ := driver.Open(ctx, c, conf(nil), driver.Options{Release: &t})
		up, err := s2.CreateUpload(ctx, rec, key, op.UploadParams{Owner: alice, Placement: meta.PlacementRule{Name: "default-placement"}, Attrs: attrsWithACL(alice)})
		Expect(err).NotTo(HaveOccurred())
		data := c.Object(extraPoolName, extraNS, metaOID(rec, "k", up.ID)).Data
		Expect(data[0]).To(Equal(byte(4)))
		Expect(data[len(data)-4:]).To(Equal([]byte{0, 0, 0, 0}))
	})
})
```

(`memStatsAdjustments` reads the M `AdjustStats` calls the fixture records — W Task 5's fixture has an `adjustment` recorder or asserts through M's quota cache; follow W's fixture.)

- [ ] **Step 3: Run the specs to see them fail**

Run: `go test -tags=ceph_preview ./internal/driver/ -ginkgo.focus="multipart layout|CreateUpload"`
Expected: compile failures on the new identifiers.

- [ ] **Step 4: Implement `mp_layout.go` and `mp_upload.go`**

```go
package driver

func (s *Store) extraPool(rule meta.PlacementRule) (meta.Pool, bool) {
	if p, err := s.Placement(rule); err == nil {
		return p.DataExtraPool, true
	}
	def := s.zone.ZoneGroup.DefaultPlacement
	if p, err := s.Placement(meta.PlacementRule{Name: def.Name}); err == nil {
		return p.DataExtraPool, true
	}
	return meta.Pool{}, false
}

func (s *Store) metaRef(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, uploadID string) (mpRef, error) {
	pool, ok := s.extraPool(rec.Info.PlacementRule)
	if !ok {
		return mpRef{}, fmt.Errorf("%w: no data-extra pool resolves for placement %q", op.ErrInternalError, rec.Info.PlacementRule)
	}
	p, err := s.pools.get(ctx, pool)
	if err != nil {
		return mpRef{}, op.FromRADOS(err, op.ScopeUpload)
	}
	mk := meta.ObjKey{Name: meta.MultipartMetaName(key.Name, uploadID), NS: meta.NSMultipart}
	return mpRef{pool: p, oid: meta.Stripe{Obj: meta.Obj{Bucket: rec.Info.Bucket, Key: mk}}.OID(), loc: mk.Locator(), key: mk}, nil
}

func (s *Store) readMeta(ctx context.Context, ref mpRef) (*metaState, error) {
	r := radosclient.NewReadOp()
	ver := version.Read(r, s.release)
	xa := r.GetXattrs()
	st := r.Stat()
	buf := s.w.buf(s.rc.chunk) // rgw_max_chunk_size bytes from the put buffer pool
	defer s.w.bufs.Put(buf[:cap(buf)])
	rd := r.ReadInto(0, buf)
	pool := ref.pool
	if ref.loc != "" {
		pool = pool.WithLocator(ref.loc)
	}
	_, err := pool.Read(ctx, ref.oid, r, 0)
	if err != nil {
		return nil, op.FromRADOS(err, op.ScopeUpload) // ENOENT → ErrNoSuchUpload
	}
	ms := &metaState{ref: ref, size: st.Size, mtime: st.ModTime, attrs: filterRGWAttrs(xa.Attrs)}
	if v, verr := ver.Version(); verr == nil {
		ms.version = v
	} // a missing version xattr reads as the zero version; radosgw's read_version treats ENODATA the same
	if rd.N == 0 {
		return nil, op.ErrNoSuchUpload // rgw_sal_rados.cc:3700-3702
	}
	d := denc.NewDecoder(rd.Data[:rd.N])
	ms.info = meta.DecodeMultipartUploadInfo(d)
	if d.Err() != nil {
		return nil, fmt.Errorf("%w: decoding multipart upload info: %w", op.ErrInternalError, d.Err())
	}
	return ms, nil
}
```

(`filterRGWAttrs` is R Task 3's `user.rgw.`-prefix filter; reuse its name. `version.Read`'s result on an object without the xattr: check `internal/cls/version` — the class returns ENODATA? No: `cls_version_read` → `read_version(hctx, &objv, false)` → ENODATA → `objv.ver = 0`, returns 0 → an encoded zero version comes back; so `ver.Version()` succeeds with `Ver 0`.)

```go
func uploadFromMeta(rec *op.BucketRecord, key meta.ObjKey, id string, st *metaState) *op.Upload {
	ownerID, display := entryOwner(st.attrs)
	return &op.Upload{ID: id, Bucket: rec, Key: key, Owner: meta.ParseOwner(ownerID), OwnerName: display, Initiated: st.mtime, Placement: st.info.DestPlacement, Attrs: st.attrs}
}

func (s *Store) CreateUpload(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, p op.UploadParams) (*op.Upload, error) {
	id := meta.MultipartUploadIDPrefix + s.w.rand(32) // gen_rand_alphanumeric(buf, 32): 32 characters (rgw_sal_rados.cc:3284-3287)
	ref, err := s.metaRef(ctx, rec, key, id)
	if err != nil {
		return nil, err
	}
	info := meta.MultipartUploadInfo{DestPlacement: p.Placement}
	data := encodeAt(info, s.release)
	now := s.now()
	hw := &headWrite{
		rec: rec, key: ref.key, data: data, attrs: p.Attrs, mtime: now,
		create: true, atomic: false, pool: ref.pool, oid: ref.oid, loc: ref.loc,
		category: rgw.CategoryMultiMeta, size: uint64(len(data)), accountedSize: 0,
	}
	x := s.newIndexOp(rec, ref.key, "")
	x.hashName = key.Name
	if _, err := s.writeMeta(ctx, hw, x); err != nil {
		return nil, err
	}
	return &op.Upload{ID: id, Bucket: rec, Key: key, Owner: p.Owner, OwnerName: p.OwnerName, Initiated: now, Placement: p.Placement, Attrs: p.Attrs}, nil
}

func (s *Store) GetUpload(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, uploadID string) (*op.Upload, error) {
	ref, err := s.metaRef(ctx, rec, key, uploadID)
	if err != nil {
		return nil, err
	}
	st, err := s.readMeta(ctx, ref)
	if err != nil {
		return nil, err
	}
	return uploadFromMeta(rec, key, uploadID, st), nil
}
```

`Upload.Placement` for CreateUpload is the op's `dest_placement` (`x-amz-storage-class` inherited from the bucket rule, Task 8), stored verbatim as radosgw stores `s->dest_placement`. Remove the two stubs from `store.go`. `export_test.go`: `MetaRefForTest`, `ReadMetaForTest`, `NewHeadWriteForTest`, `SetNonAtomicCreateForTest`, `WriteMetaForTest`, and `type MetaState = metaState` with exported field accessors as the specs use them.

- [ ] **Step 5: Run the specs to see them pass**

Run: `go test -tags=ceph_preview ./internal/driver/`. Expected: PASS, W's existing specs included (the atomic path is untouched; W's `PutObject` specs prove it).

- [ ] **Step 6: Check and commit**

Run: `make check`. Expected: green.

```bash
git add internal/driver internal/testutil/fakerados
git commit -m "feat(driver): multipart meta object layout, CreateUpload and GetUpload

The meta object is a head in the placement's data-extra pool, indexed
in the multipart namespace on the shard of the upload's key under the
MultiMeta category, holding the encoded multipart_upload_info as its
data. radosgw writes it and the part heads without set_atomic, so
they carry no idtag and no guard; writeMeta gains that non-atomic
branch, a pool override and the entry category, and the index op a
hash source and remove_objs, all additively. One composed read
(version, xattrs, stat, first chunk) serves GetUpload as get_info and
get_obj_attrs do.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

- [ ] **Step 7: Record the client-checksum difference in `docs/exclusions.md`; commit**

CreateUpload writes Tentacle's `multipart_upload_info` with no checksum type whatever `x-amz-checksum-algorithm` said (P-D5), the first record this unit writes that a Tentacle radosgw would write differently, so this task records the difference, for single PUT as for multipart (owner decision 7). In `docs/exclusions.md`, "Coexistence obligations independent of any exclusion", append:

```markdown
- **Client checksums are ignored until phase 2.** rgw-go ignores the
  `x-amz-checksum-*` headers and aws-chunked trailers of PutObject,
  UploadPart and CompleteMultipartUpload, and `x-amz-checksum-algorithm` on
  CreateMultipartUpload, on both releases, and stores no checksum. A Squid
  radosgw ignores them too, so on Squid nothing differs. A Tentacle radosgw
  validates a supplied checksum, answering 400 BadDigest on a mismatch, and
  stores the object's checksum in `user.rgw.cksum` (`rgw_op.cc:4757-4789`
  for PutObject and UploadPart, `:7330-7340` for CompleteMultipartUpload,
  at v20.2.4). On a Tentacle cluster an object rgw-go wrote therefore
  carries no `user.rgw.cksum`: GetObjectAttributes and a checksum-mode GET
  through radosgw show no checksum for it, and rgw-go accepts a checksum
  that does not match the data. Phase 2 adds Tentacle-level checksums.
```

```bash
git add docs/exclusions.md
git commit -m "docs(exclusions): record that client checksums are ignored until phase 2

A Tentacle radosgw validates x-amz-checksum-* values and stores
user.rgw.cksum on single and multipart uploads; rgw-go ignores them on
both releases until phase 2, so objects it writes on Tentacle carry no
checksum.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

The PR description names the entry, which is reported for the rgw-rs session.

---

### Task 4: `driver`: `PutPart`, `CopyPart`

**Files:**
- Create: `internal/driver/mp_part.go`, `internal/driver/mp_part_test.go`
- Modify: `internal/driver/stripe.go` (W Task 5: `tailWriter.namer`, `exclusiveFirst`, `first`; `newPartWriter`), `internal/driver/stripe_test.go` (three specs), `internal/driver/store.go` (the `PutPart`/`CopyPart` stubs go), `internal/driver/export_test.go`, `internal/memstore/multipart.go` (`CopyPart` stores `up.Attrs` as the part's attrs; `PutPart` honours `PutParams.ContentMD5` → `ErrBadDigest`), `internal/memstore/multipart_test.go`

**Interfaces:**
- Consumes: Task 3's `metaRef`, `readMeta`, `mpRef`, the extended `headWrite`/`indexOp`; W Task 5's `planPut`, `layout`, `tailWriter` (`consume`, `drain`, `discard`, `write`, `readPiece`, `bodyErr`), `writeMeta`, `encodeAt`, `w.rand`, `w.buf`/`bufs`, `s.CheckQuota`; R Task 4's `Store.ReadObject(ctx, st, rng, sink)`; Task 2's `meta.NewPartManifest`, `MultipartPrefix`, `MultipartPartKey`, `UploadPartInfo`, `NewCompressionInfo`; `rgw.MPUploadPartInfoUpdate`; `version.Inc`; G's `op.PutParams` (`Attrs`, `Size`, `ContentMD5`, `Mtime`), `op.PartResult`, `op.ByteRange`, `op.ObjectState`, the sentinels `ErrBadDigest`, `ErrRequestTimeout`, `ErrEntityTooLarge`, `ErrNoSuchUpload`, `ErrRequestTimedOut`; `radosclient.ErrExists`, `ErrTimedOut`, `ErrNotFound`.
- Produces:

```go
package driver

// tailWriter gains:
//	// namer names stripe n; nil is m.TailObj (part 0). A part writer names
//	// stripe 0 the part head "<prefix>.<n>" and the rest "<prefix>.<n>_<s>".
//	namer func(n uint64) meta.Obj
//	// exclusiveFirst makes the first piece of stripe 0 RadosWriter::write_exclusive
//	// (rgw_putobj_processor.cc:161-178): create(true), the alloc hint, write_full,
//	// issued and AWAITED before anything else; an empty part still issues it. On
//	// EEXIST consume returns radosclient.ErrExists with that piece kept in first,
//	// so the caller can retry under another prefix without re-reading the body.
//	exclusiveFirst bool
//	first          []byte

// newPartWriter is newTailWriter for part n of m: namer m.StripeObj(n, ·) in
// l.tailPool, exclusiveFirst on.
func (s *Store) newPartWriter(m *meta.Manifest, l layout, n uint32) *tailWriter

// partResult is what one part write leaves: the part head's stripe 0 ref,
// the manifest as written, the MD5 and size.
type partResult struct {
	m     meta.Manifest
	etag  string
	size  uint64
	mtime time.Time
}

// streamPart is MultipartObjectProcessor's data path plus RGWPutObj::execute's
// digest and length checks for one part: the body streams into the part
// head and its shadow stripes under the put window, the first stripe created
// exclusively; EEXIST re-prefixes to "<key>.<32 random>" and streams again
// once (rgw_putobj_processor.cc:414-439). It returns with every stripe
// drained, or with every issued stripe removed on failure.
func (s *Store) streamPart(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, uploadID string, n uint32, body io.Reader, l layout, size int64, contentMD5 []byte) (partResult, error)

// registerPart is MultipartObjectProcessor::complete's tail: the part head's
// non-atomic head write on the tail rule's pool with the index entry hashed by
// the upload key (rgw_putobj_processor.cc:504-536), then one op on the meta
// object: assert_exists, mp_upload_part_info_update("part.%08d", info),
// cls_version_inc (:560-575). ENOENT there is ErrNoSuchUpload with the
// stripes removed (the upload is gone: RadosWriter's destructor deletes what
// it wrote); ETIMEDOUT keeps them (the write may yet land, :596-602).
func (s *Store) registerPart(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, ref mpRef, n uint32, pr partResult, attrs map[string][]byte, l layout, tw *tailWriter) error

func (s *Store) PutPart(ctx context.Context, up *op.Upload, n int, body io.Reader, p op.PutParams) (*op.PartResult, error)
func (s *Store) CopyPart(ctx context.Context, up *op.Upload, n int, src *op.ObjectState, rng op.ByteRange) (*op.PartResult, error)
```

`PutPart` and `CopyPart` need only `up.ID`, `up.Bucket` and `up.Key`: they read the meta object themselves for the destination placement, as `RGWPutObj::execute` calls `get_info` ([rgw_op.cc:4249-4262](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L4249-L4262)), so the op does not `GetUpload` first (one meta read per part, radosgw's count). `CopyPart` takes the part head's request attrs (ACL, generic and x-amz-meta attrs) from `up.Attrs`, because G's `CopyPart` carries no `PutParams`; the op builds that `Upload` per request (reported as a contract gap in the final reply; `PutPart` takes them from `p.Attrs`).

The RADOS sequence for a 9 MiB part 2 of `k` under id `2~ID` with 4 MiB chunk and stripe: (1) meta read (`readMeta`); (2) `[Create(true), SetAllocHint(0,0,0), WriteFull(4 MiB)]` on `<marker>__multipart_k.2~ID.2`, awaited; (3) `[SetAllocHint, WriteFull(4 MiB)]` on `<marker>__shadow_k.2~ID.2_1` and (4) `[SetAllocHint, WriteFull(1 MiB)]` on `…2_2`, under the window; drain; (5) the second quota check; (6) index prepare ADD on `k`'s shard; (7) the part head write `[SetMtime, SetXattr(acl), SetXattr(content_type), SetXattr(etag), SetXattr(x-amz-meta-*), Exec(obj_store_pg_ver), SetXattr(source_zone)]` — no create, no data, no idtag, no manifest, no storage_class (`MultipartObjectProcessor::complete` never sets `meta.manifest`, [rgw_putobj_processor.cc:491-560](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_putobj_processor.cc#L491-L560) at v19.2.6, and the phase-0 gate sees no manifest on part heads, [test/gate/phase0_test.go:1215](https://github.com/jhoblitt/rgw-go/blob/50fc45c4d59953661d5c57c27ca7016bc55edeeb/test/gate/phase0_test.go#L1215)); complete ADD off the request path; (8) `[AssertExists, Exec(rgw.mp_upload_part_info_update), Exec(version.inc)]` on the meta object. A part within one stripe skips (3)-(4); a part of 0 bytes still issues (2) with an empty `WriteFull`.

- [ ] **Step 1: Write the failing `tailWriter` specs**

`internal/driver/stripe_test.go` gains:

```go
var _ = Describe("part writer", func() {
	It("creates the part head exclusively, names later stripes as shadows, and awaits the first write", func(ctx SpecContext) {
		m := meta.NewPartManifest(target, rule, meta.PlacementRule{}, "k.2~id", 2, 4<<20)
		l, _ := s.PlanPutForTest(ctx, rec, key, "")
		tw := s.NewPartWriterForTest(&m, l, 2)
		size, err := tw.Consume(ctx, bytes.NewReader(bytes.Repeat([]byte("p"), 9<<20)), md5.New())
		Expect(err).NotTo(HaveOccurred())
		Expect(tw.Drain()).To(Succeed())
		Expect(size).To(BeEquivalentTo(9 << 20))
		head := rec.Info.Bucket.Marker + "__multipart_k.2~id.2"
		steps := c.LastWrite(dataPool, "", head).Steps()
		Expect(steps[0]).To(Equal(&radosclient.CreateStep{Exclusive: true}), "write_exclusive")
		Expect(steps[1]).To(BeAssignableToTypeOf(&radosclient.SetAllocHintStep{}))
		Expect(steps[2]).To(BeAssignableToTypeOf(&radosclient.WriteFullStep{}))
		Expect(c.Object(dataPool, "", head).Data).To(HaveLen(4 << 20))
		Expect(c.Object(dataPool, "", rec.Info.Bucket.Marker+"__shadow_k.2~id.2_1").Data).To(HaveLen(4 << 20))
		Expect(c.Object(dataPool, "", rec.Info.Bucket.Marker+"__shadow_k.2~id.2_2").Data).To(HaveLen(1 << 20))
		Expect(c.WriteOrder(dataPool, "")[0]).To(Equal(head), "the exclusive create completed before any shadow was issued")
	})
	It("returns ErrExists with the first chunk kept when the part head exists", func(ctx SpecContext) {
		c.Put(dataPool, "", rec.Info.Bucket.Marker+"__multipart_k.2~id.2", []byte("old"))
		m := meta.NewPartManifest(target, rule, meta.PlacementRule{}, "k.2~id", 2, 4<<20)
		l, _ := s.PlanPutForTest(ctx, rec, key, "")
		tw := s.NewPartWriterForTest(&m, l, 2)
		_, err := tw.Consume(ctx, bytes.NewReader(bytes.Repeat([]byte("n"), 5<<20)), md5.New())
		Expect(err).To(MatchError(radosclient.ErrExists))
		Expect(tw.First()).To(HaveLen(4 << 20))
		Expect(c.Object(dataPool, "", rec.Info.Bucket.Marker+"__multipart_k.2~id.2").Data).To(Equal([]byte("old")), "untouched")
		Expect(c.Object(dataPool, "", rec.Info.Bucket.Marker+"__shadow_k.2~id.2_1")).To(BeNil(), "nothing else was issued")
	})
	It("writes an empty part as an exclusive empty head", func(ctx SpecContext) {
		m := meta.NewPartManifest(target, rule, meta.PlacementRule{}, "k.2~id", 1, 4<<20)
		l, _ := s.PlanPutForTest(ctx, rec, key, "")
		tw := s.NewPartWriterForTest(&m, l, 1)
		size, err := tw.Consume(ctx, strings.NewReader(""), md5.New())
		Expect(err).NotTo(HaveOccurred())
		Expect(tw.Drain()).To(Succeed())
		Expect(size).To(BeZero())
		obj := c.Object(dataPool, "", rec.Info.Bucket.Marker+"__multipart_k.2~id.1")
		Expect(obj).NotTo(BeNil())
		Expect(obj.Data).To(BeEmpty())
	})
})
```

(`c.WriteOrder(pool, ns)` lists oids in the order their write ops completed; add it to fakerados if absent.)

- [ ] **Step 2: Write the failing `PutPart`/`CopyPart` specs**

`internal/driver/mp_part_test.go`, on Task 3's fixture with an upload created by `CreateUpload` (`up`), `seedGCShards` and `RefcountClass` as W's Task 5 fixture has them. If the fake records `SetMtime` as the op's `Mtime()` rather than a step, assert `Mtime()` in place of the `hs[0]` assertion and shift the indexes:

```go
var _ = Describe("PutPart", func() {
	partAttrs := func() map[string][]byte {
		return map[string][]byte{meta.AttrACL: encode(acl.DefaultPolicy(alice, "Alice")), meta.AttrContentType: []byte("application/octet-stream\x00"), meta.AttrMetaPrefix + "p": []byte("1\x00")}
	}
	It("writes a 9 MiB part as radosgw does and registers it on the meta object", func(ctx SpecContext) {
		body := bytes.Repeat([]byte("p"), 9<<20)
		c.ResetCounters()
		res, err := s.PutPart(ctx, up, 2, bytes.NewReader(body), op.PutParams{Attrs: partAttrs(), Size: int64(len(body)), Mtime: mtime})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.ETag).To(Equal(md5hex(body)))
		Expect(res.Size).To(BeEquivalentTo(9 << 20))
		metaOID := metaOID(rec, "k", up.ID)
		Expect(c.Reads(extraPoolName, extraNS, metaOID)).To(Equal(1), "get_info")
		head := rec.Info.Bucket.Marker + "__multipart_k." + up.ID + ".2"
		Expect(c.Writes(dataPool, "", head)).To(Equal(2), "write_exclusive, then write_meta")
		hs := c.LastWrite(dataPool, "", head).Steps()
		Expect(hs[0]).To(BeAssignableToTypeOf(&radosclient.SetMtimeStep{}))
		Expect(names(hs)).To(Equal([]string{"user.rgw.acl", "user.rgw.content_type", "user.rgw.etag", "user.rgw.x-amz-meta-p", "obj_store_pg_ver", "user.rgw.source_zone"}), "no create, no data, no idtag, no manifest, no storage_class")
		x := c.Object(dataPool, "", head).Xattrs
		Expect(x).NotTo(HaveKey(meta.AttrIDTag))
		Expect(x).NotTo(HaveKey(meta.AttrManifest))
		Expect(x).To(HaveKeyWithValue(meta.AttrETag, []byte(md5hex(body))))
		Expect(c.Object(dataPool, "", rec.Info.Bucket.Marker+"__shadow_k."+up.ID+".2_2").Data).To(HaveLen(1 << 20))

		shard := shardOf(rec, "k")
		Eventually(func() int { return c.Writes(indexPool, "", shard) }).Should(Equal(4), "the meta object's prepare+complete, then the part's")
		en, ok := c.Entry(indexPool, "", shard, "_multipart_k."+up.ID+".2")
		Expect(ok).To(BeTrue(), "the part head is indexed on the UPLOAD key's shard")
		Expect(en.Meta.Category).To(Equal(rgwcls.CategoryMain))
		Expect(en.Meta.Size).To(BeEquivalentTo(9 << 20))
		Expect(en.Meta.AccountedSize).To(BeEquivalentTo(9 << 20))
		Expect(en.Meta.ETag).To(Equal(md5hex(body)))
		Expect(en.Meta.StorageClass).To(BeEmpty())
		Expect(en.Tag).To(HavePrefix("_"))

		reg := c.LastWrite(extraPoolName, extraNS, metaOID).Steps()
		Expect(reg[0]).To(BeAssignableToTypeOf(&radosclient.AssertExistsStep{}))
		Expect(reg[1].(*radosclient.ExecStep).Method).To(Equal("mp_upload_part_info_update"))
		Expect(reg[2].(*radosclient.ExecStep).Class).To(Equal("version"))
		Expect(reg[2].(*radosclient.ExecStep).Method).To(Equal("inc"))
		stored := meta.DecodeUploadPartInfo(denc.NewDecoder(c.Object(extraPoolName, extraNS, metaOID).Omap["part.00000002"]))
		Expect(stored.Num).To(BeEquivalentTo(2))
		Expect(stored.ETag).To(Equal(md5hex(body)))
		Expect(stored.Size).To(BeEquivalentTo(9 << 20))
		Expect(stored.AccountedSize).To(BeEquivalentTo(9 << 20))
		Expect(stored.Manifest.Prefix).To(Equal("k." + up.ID))
		Expect(stored.Manifest.Rules).To(Equal(map[uint64]meta.ManifestRule{0: {StartPartNum: 2, StripeMaxSize: 4 << 20}}))
		Expect(stored.Manifest.ObjSize).To(BeEquivalentTo(9 << 20))
		Expect(stored.Manifest.Obj.Key.Name).To(Equal("k"), "the manifest's head is the UPLOAD key")
		Expect(stored.Compression.Type).To(Equal("none"))
		Expect(stored.PastPrefixes).To(BeEmpty())
		Expect(memStatsAdjustments()).To(ContainElement(adjustment{objs: 1, bytes: 9 << 20, removed: 0}))
	})
	It("re-prefixes a re-uploaded part number and the class records the old prefix", func(ctx SpecContext) {
		first := bytes.Repeat([]byte("a"), 5<<20)
		_, err := s.PutPart(ctx, up, 1, bytes.NewReader(first), op.PutParams{Attrs: partAttrs(), Size: int64(len(first))})
		Expect(err).NotTo(HaveOccurred())
		s.SetRandForTest(fixedRand("RANDOMRANDOMRANDOMRANDOMRANDOM12XYZ"))
		second := bytes.Repeat([]byte("b"), 5<<20)
		h := md5hex(second)
		res, err := s.PutPart(ctx, up, 1, bytes.NewReader(second), op.PutParams{Attrs: partAttrs(), Size: int64(len(second))})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.ETag).To(Equal(h), "the digest covers the re-streamed first chunk exactly once")
		newHead := rec.Info.Bucket.Marker + "__multipart_k.RANDOMRANDOMRANDOMRANDOMRANDOM12.1"
		Expect(c.Object(dataPool, "", newHead)).NotTo(BeNil())
		Expect(c.Object(dataPool, "", rec.Info.Bucket.Marker+"__multipart_k."+up.ID+".1").Data).To(Equal(first[:4<<20]), "the first part's head stands until Complete or Abort GCs it")
		stored := meta.DecodeUploadPartInfo(denc.NewDecoder(c.Object(extraPoolName, extraNS, metaOID(rec, "k", up.ID)).Omap["part.00000001"]))
		Expect(stored.Manifest.Prefix).To(Equal("k.RANDOMRANDOMRANDOMRANDOMRANDOM12"))
		Expect(stored.PastPrefixes).To(Equal([]string{"k." + up.ID}), "cls_rgw.cc:4375-4382")
		Expect(stored.ETag).To(Equal(h))
		shard := shardOf(rec, "k")
		_, ok := c.Entry(indexPool, "", shard, "_multipart_k.RANDOMRANDOMRANDOMRANDOMRANDOM12.1")
		Expect(ok).To(BeTrue(), "the re-uploaded part has its own index entry")
		_, ok = c.Entry(indexPool, "", shard, "_multipart_k."+up.ID+".1")
		Expect(ok).To(BeTrue(), "and the old one stays until Complete/Abort remove it")
	})
	It("answers NoSuchUpload and removes what it wrote when the upload vanished during the body", func(ctx SpecContext) {
		metaOID := metaOID(rec, "k", up.ID)
		c.BeforeWrite(extraPoolName, extraNS, metaOID, func(o *fakerados.Object) { c.Remove(extraPoolName, extraNS, metaOID) }) // the abort landed between the head write and the registration
		_, err := s.PutPart(ctx, up, 3, bytes.NewReader(bytes.Repeat([]byte("c"), 6<<20)), op.PutParams{Attrs: partAttrs(), Size: 6 << 20})
		Expect(err).To(MatchError(op.ErrNoSuchUpload))
		Expect(c.Object(dataPool, "", rec.Info.Bucket.Marker+"__multipart_k."+up.ID+".3")).To(BeNil(), "~RadosWriter removes the head and stripes")
		Expect(c.Object(dataPool, "", rec.Info.Bucket.Marker+"__shadow_k."+up.ID+".3_1")).To(BeNil())
		_, ok := c.Entry(indexPool, "", shardOf(rec, "k"), "_multipart_k."+up.ID+".3")
		Expect(ok).To(BeTrue(), "radosgw leaves the index entry; bucket check --fix reaps it")
	})
	It("keeps the stripes when the registration times out", func(ctx SpecContext) {
		c.FailNextWrite(extraPoolName, extraNS, metaOID(rec, "k", up.ID), syscall.ETIMEDOUT)
		_, err := s.PutPart(ctx, up, 4, bytes.NewReader(bytes.Repeat([]byte("d"), 5<<20)), op.PutParams{Attrs: partAttrs(), Size: 5 << 20})
		Expect(err).To(MatchError(op.ErrRequestTimedOut))
		Expect(c.Object(dataPool, "", rec.Info.Bucket.Marker+"__multipart_k."+up.ID+".4")).NotTo(BeNil(), "writer.clear_written on ETIMEDOUT")
	})
	DescribeTable("refuses a bad body before registering anything",
		func(body io.Reader, size int64, md5sum []byte, want error) {
			_, err := s.PutPart(ctx, up, 5, body, op.PutParams{Attrs: partAttrs(), Size: size, ContentMD5: md5sum})
			Expect(err).To(MatchError(want))
			Expect(c.Object(extraPoolName, extraNS, metaOID(rec, "k", up.ID)).Omap).NotTo(HaveKey("part.00000005"))
			Expect(c.Object(dataPool, "", rec.Info.Bucket.Marker+"__multipart_k."+up.ID+".5")).To(BeNil(), "the exclusive head was removed")
			Expect(c.Writes(indexPool, "", shardOf(rec, "k"))).To(Equal(2), "only the upload's own prepare+complete: no part prepare went out")
		},
		Entry("a short body is RequestTimeout", strings.NewReader("abc"), int64(10), nil, op.ErrRequestTimeout),
		Entry("a wrong Content-MD5 is BadDigest", bytes.NewReader(bytes.Repeat([]byte("e"), 5<<20)), int64(5<<20), md5sumOf("other"), op.ErrBadDigest),
		Entry("A's verification verdict passes through", &failingReader{n: 4 << 20, err: op.ErrSignatureDoesNotMatch}, int64(-1), nil, op.ErrSignatureDoesNotMatch),
		Entry("past rgw_max_put_size is EntityTooLarge", &hugeReader{}, int64(-1), nil, op.ErrEntityTooLarge), // the fixture's conf sets rgw_max_put_size to 8 MiB
	)
	It("answers NoSuchUpload for an unknown upload before reading the body", func(ctx SpecContext) {
		r := &countingReader{Reader: strings.NewReader("x")}
		_, err := s.PutPart(ctx, &op.Upload{ID: "2~nope", Bucket: rec, Key: key}, 1, r, op.PutParams{Attrs: partAttrs(), Size: 1})
		Expect(err).To(MatchError(op.ErrNoSuchUpload))
		Expect(r.n).To(BeZero())
	})
	It("places a COLD upload's parts in the class's pool and records the class on no attr", func(ctx SpecContext) {
		cold, _ := s.CreateUpload(ctx, rec, meta.ObjKey{Name: "c"}, op.UploadParams{Owner: alice, Placement: meta.PlacementRule{Name: "default-placement", StorageClass: "COLD"}, Attrs: attrsWithACL(alice)})
		_, err := s.PutPart(ctx, cold, 1, bytes.NewReader(bytes.Repeat([]byte("z"), 5<<20)), op.PutParams{Attrs: partAttrs(), Size: 5 << 20})
		Expect(err).NotTo(HaveOccurred())
		Expect(c.Object("cold.data", "", rec.Info.Bucket.Marker+"__multipart_c."+cold.ID+".1")).NotTo(BeNil(), "set_meta_placement_rule(&tail_placement_rule)")
		Expect(c.Object("cold.data", "", rec.Info.Bucket.Marker+"__shadow_c."+cold.ID+".1_1")).NotTo(BeNil())
		Expect(c.Object("cold.data", "", rec.Info.Bucket.Marker+"__multipart_c."+cold.ID+".1").Xattrs).NotTo(HaveKey(meta.AttrStorageClass), "no manifest attr on a part head → no storage_class attr")
		stored := meta.DecodeUploadPartInfo(denc.NewDecoder(c.Object(extraPoolName, extraNS, metaOID(rec, "c", cold.ID)).Omap["part.00000001"]))
		Expect(stored.Manifest.TailPlacement.PlacementRule).To(Equal(meta.PlacementRule{Name: "default-placement", StorageClass: "COLD"}))
	})
})

var _ = Describe("CopyPart", func() {
	It("streams the source range through ReadObject into a part", func(ctx SpecContext) {
		src := bytes.Repeat([]byte("s"), 10<<20)
		_, err := s.PutObject(ctx, rec, meta.ObjKey{Name: "src"}, bytes.NewReader(src), op.PutParams{Attrs: attrsWithACL(alice), Size: int64(len(src)), Tag: "tx-src"})
		Expect(err).NotTo(HaveOccurred())
		st, _ := s.PrefetchObject(ctx, rec, meta.ObjKey{Name: "src"})
		upc := *up
		upc.Attrs = partAttrs()
		res, err := s.CopyPart(ctx, &upc, 1, st, op.ByteRange{Offset: 1 << 20, Length: 6 << 20})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Size).To(BeEquivalentTo(6 << 20))
		Expect(res.ETag).To(Equal(md5hex(src[1<<20 : 7<<20])))
		head := c.Object(dataPool, "", rec.Info.Bucket.Marker+"__multipart_k."+up.ID+".1")
		Expect(head.Data).To(Equal(src[1<<20 : 5<<20]))
		Expect(head.Xattrs).To(HaveKey(meta.AttrACL), "the request's attrs, not the source's")
		Expect(head.Xattrs).NotTo(HaveKey(meta.AttrMetaPrefix+"src-only"))
		stored := meta.DecodeUploadPartInfo(denc.NewDecoder(c.Object(extraPoolName, extraNS, metaOID(rec, "k", up.ID)).Omap["part.00000001"]))
		Expect(stored.Size).To(BeEquivalentTo(6 << 20))
	})
	It("removes the part when the source read fails midway", func(ctx SpecContext) {
		// FailNextRead on the source's tail _1 (EIO): CopyPart returns op.ErrInternalError (FromRADOS), no part registered, the part head is gone
	})
})
```

- [ ] **Step 3: Run the specs to see them fail**

Run: `go test -tags=ceph_preview ./internal/driver/ -ginkgo.focus="part writer|PutPart|CopyPart"`. Expected: compile failures.

- [ ] **Step 4: Extend `tailWriter` and implement `mp_part.go`**

`stripe.go`: `write` resolves `obj := t.namer(n)` when `t.namer != nil` (else `t.m.TailObj(n)`); when `t.exclusiveFirst && n == 0 && ofs == 0 && !t.firstDone`:

```go
	w := radosclient.NewWriteOp()
	w.Create(true)
	w.SetAllocHint(0, 0, 0)
	w.WriteFull(buf) // an empty part writes an empty head (HeadObjectProcessor::process's flush of an empty first chunk)
	_, err := t.pool.Write(ctx, oid, w, 0) // awaited: write_exclusive drains before returning
	t.firstDone = true
	if errors.Is(err, radosclient.ErrExists) {
		t.first = buf // kept for the caller's retry; not returned to the buffer pool
		return err
	}
	if err != nil {
		t.s.w.bufs.Put(buf[:cap(buf)])
		return err
	}
	t.s.w.bufs.Put(buf[:cap(buf)])
	t.mu.Lock()
	t.issued = append(t.issued, obj)
	t.mu.Unlock()
	return nil
```

`consume`'s `if len(buf)==0 → return nil` shortcut in `write` must not swallow the exclusive first piece: `consume` calls `write(ctx, 0, 0, buf[:0])` once when the body is empty and `exclusiveFirst` is set (after `readPiece` returns 0 bytes and done on the very first read). `consume` returns `radosclient.ErrExists` unchanged (not through `bodyErr`). `newPartWriter`:

```go
func (s *Store) newPartWriter(m *meta.Manifest, l layout, n uint32) *tailWriter {
	t := s.newTailWriter(m, l)
	t.namer = func(stripe uint64) meta.Obj { return m.StripeObj(n, stripe) }
	t.exclusiveFirst = true
	return t
}
```

`mp_part.go`:

```go
package driver

func (s *Store) streamPart(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, uploadID string, n uint32, body io.Reader, l layout, size int64, contentMD5 []byte) (partResult, error) {
	target := meta.Obj{Bucket: rec.Info.Bucket, Key: key}
	h := md5.New()
	prefix := meta.MultipartPrefix(key.Name, uploadID)
	m := meta.NewPartManifest(target, rec.Info.PlacementRule, l.tailRule, prefix, n, l.stripe)
	tw := s.newPartWriter(&m, l, n)
	_, got, err := tw.consume(ctx, body, h)
	if errors.Is(err, radosclient.ErrExists) {
		// MultipartObjectProcessor::process_first_chunk: a new random prefix, once.
		prefix = meta.MultipartPrefix(key.Name, s.w.rand(32))
		m = meta.NewPartManifest(target, rec.Info.PlacementRule, l.tailRule, prefix, n, l.stripe)
		retry := s.newPartWriter(&m, l, n)
		h.Reset()
		_, got, err = retry.consume(ctx, io.MultiReader(bytes.NewReader(tw.first), body), h)
		s.w.bufs.Put(tw.first[:cap(tw.first)])
		tw = retry
	}
	if err == nil {
		err = tw.drain()
	}
	if err == nil && size >= 0 && got != uint64(size) {
		err = op.ErrRequestTimeout // rgw_op.cc:4405-4408
	}
	if err == nil && contentMD5 != nil && !bytes.Equal(h.Sum(nil), contentMD5) {
		err = op.ErrBadDigest // :4459-4462
	}
	if err != nil {
		tw.discard()
		if errors.Is(err, radosclient.ErrExists) {
			return partResult{}, op.FromRADOS(err, op.ScopeObject)
		}
		return partResult{}, err
	}
	m.SetObjSize(got)
	return partResult{m: m, etag: hex.EncodeToString(h.Sum(nil)), size: got, mtime: s.now()}, nil
}
```

(`tw.discard` after a successful drain removes nothing but is harmless; on the error paths it removes every issued stripe, the part head included, as `~RadosWriter` does. The `tw` whose exclusive create failed issued nothing, so its `discard` is a no-op; the retry writer owns the rest.)

```go
func (s *Store) registerPart(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, ref mpRef, n uint32, pr partResult, attrs map[string][]byte, l layout, tw *tailWriter) error {
	headObj := pr.m.StripeObj(n, 0)
	a := maps.Clone(attrs)
	a[meta.AttrETag] = []byte(pr.etag)
	hw := &headWrite{
		rec: rec, key: headObj.Key, attrs: a, mtime: pr.mtime,
		atomic: false, create: false, modifyTail: true,
		pool: l.tailPool, oid: meta.Stripe{Obj: headObj}.OID(), loc: headObj.Key.Locator(),
		size: pr.size, accountedSize: pr.size,
	}
	x := s.newIndexOp(rec, headObj.Key, "")
	x.hashName = key.Name // head_obj.index_hash_source = target_obj.key.name
	if _, err := s.writeMeta(ctx, hw, x); err != nil {
		tw.discard()
		return err
	}
	info := meta.UploadPartInfo{Num: n, Size: pr.size, AccountedSize: pr.size, ETag: pr.etag, Modified: pr.mtime, Manifest: pr.m, Compression: meta.NewCompressionInfo()}
	w := radosclient.NewWriteOp()
	w.AssertExists()
	rgw.MPUploadPartInfoUpdate(w, meta.MultipartPartKey(n), encodeAt(info, s.release), s.release)
	version.Inc(w, s.release)
	pool := ref.pool
	if ref.loc != "" {
		pool = pool.WithLocator(ref.loc)
	}
	if _, err := pool.Write(ctx, ref.oid, w, 0); err != nil {
		if !errors.Is(err, radosclient.ErrTimedOut) {
			tw.discard() // ~RadosWriter: clear_written ran only on ETIMEDOUT
		}
		return op.FromRADOS(err, op.ScopeUpload) // ENOENT → ErrNoSuchUpload; ETIMEDOUT → ErrRequestTimedOut; EEXIST → the raw errno's row
	}
	return nil
}

func (s *Store) PutPart(ctx context.Context, up *op.Upload, n int, body io.Reader, p op.PutParams) (*op.PartResult, error) {
	rec, key := up.Bucket, up.Key
	ref, err := s.metaRef(ctx, rec, key, up.ID)
	if err != nil {
		return nil, err
	}
	ms, err := s.readMeta(ctx, ref) // get_info: the destination placement
	if err != nil {
		return nil, err
	}
	l, err := s.planPut(ctx, rec, key, ms.info.DestPlacement.StorageClass)
	if err != nil {
		return nil, err
	}
	pr, err := s.streamPart(ctx, rec, key, up.ID, uint32(n), body, l, p.Size, p.ContentMD5) //nolint:gosec // the op parsed a non-negative int
	if err != nil {
		return nil, err
	}
	if err := s.CheckQuota(ctx, rec, rec.Info.Owner, int64(pr.size), 1); err != nil { //nolint:gosec // fits
		return nil, err // the second check_quota, on the bytes received (rgw_op.cc:4418-4422)
	}
	if err := s.registerPart(ctx, rec, key, ref, uint32(n), pr, p.Attrs, l, tw(pr)); err != nil { //nolint:gosec
		return nil, err
	}
	return &op.PartResult{ETag: pr.etag, Size: pr.size, Mtime: pr.mtime}, nil
}
```

(`streamPart` returns the writer inside `partResult` — add a `tw *tailWriter` field — so `registerPart` can `discard`; the sketch's `tw(pr)` is that field. The quota failure after streaming also discards: fold `CheckQuota` into the error path with `pr.tw.discard()`. `planPut`'s chunk and stripe: `MultipartObjectProcessor::prepare_head` sizes both for the TAIL rule (`get_max_chunk_size(tail_placement_rule, …)`); W's `planPut` aligns them by the pools it resolves — if it aligns the chunk by the head pool, add `partLayout` that aligns both by `l.tailPool` and use it here.)

```go
func (s *Store) CopyPart(ctx context.Context, up *op.Upload, n int, src *op.ObjectState, rng op.ByteRange) (*op.PartResult, error) {
	pr, pw := io.Pipe()
	go func() {
		err := s.ReadObject(ctx, src, rng, pw) // the range in rgw_max_chunk_size pieces, decompressed
		pw.CloseWithError(err)
	}()
	res, err := s.PutPart(ctx, up, n, pr, op.PutParams{Attrs: up.Attrs, Size: int64(rng.Length)}) //nolint:gosec // fits
	if err != nil {
		_ = pr.CloseWithError(err) // stop the reader
	}
	return res, err
}
```

A `ReadObject` failure surfaces from the pipe as the body's error: `bodyErr` passes an `*op.Error` through and maps other errors — `ReadObject` already returns `op.FromRADOS` errors, so the part write fails with that error and `discard` runs. `p.Size = rng.Length` makes a short source read (a truncated stream) `ErrRequestTimeout` as radosgw's length check would; the op computed `rng` from the source's size, so a mismatch is a read failure, not a client error.

Remove the two stubs from `store.go`; `export_test.go` exports `NewPartWriterForTest`, `Consume`, `Drain`, `First`, `StreamPartForTest`.

- [ ] **Step 5: Run the driver specs**

Run: `go test -tags=ceph_preview ./internal/driver/`. Expected: PASS.

- [ ] **Step 6: memstore parity**

`internal/memstore/multipart.go`: `PutPart` compares `p.ContentMD5` when set (`op.ErrBadDigest`), enforces `p.Size` (`op.ErrRequestTimeout` when the body is shorter, `op.ErrEntityTooLarge` when longer than `rgw_max_put_size`'s 5 GiB is moot — skip), stores `p.Attrs` plus the etag as the part's attrs; `CopyPart` stores `up.Attrs`. Specs in `multipart_test.go` for the digest and the attrs. Run `go test -tags=ceph_preview ./internal/memstore/`; expected PASS.

- [ ] **Step 7: Check and commit**

```bash
make check
git add internal/driver internal/memstore internal/testutil/fakerados
git commit -m "feat(driver): UploadPart and UploadPartCopy over the stripe writer

A part streams into its head and shadow stripes under the put window,
the head created exclusively as RadosWriter::write_exclusive does and
re-prefixed once on EEXIST; the head is then written non-atomically
on the tail rule's pool with its index entry hashed by the upload
key, and the part is registered on the meta object with
mp_upload_part_info_update and cls_version_inc in one op. A copy
source streams through ReadObject. Failures remove every stripe,
except a timed-out registration, which radosgw leaves in place.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: `driver`: `ListParts`, `ListUploads`; `ListObjectsParams.NameFilter`

**Files:**
- Create: `internal/driver/mp_list.go`, `internal/driver/mp_list_test.go`
- Modify: `internal/op/bucket.go` (`ListObjectsParams.NameFilter`), `internal/op/opfakes/` (regenerate), `internal/driver/listing.go` (M Task 8: the filter in `listObjectsOrdered` and `listObjectsUnordered`), `internal/driver/listing_test.go` (one spec), `internal/memstore/bucket.go` (the filter), `internal/memstore/multipart.go` (`ListUploads` through the filtered listing, `ListParts` marker semantics), `internal/memstore/bucket_test.go`, `internal/driver/store.go` (the `ListParts`/`ListUploads` stubs go)

**Interfaces:**
- Consumes: Task 3's `metaRef`; M's `Store.ListObjects` and its `listObjectsOrdered` loop (M plan [:3618-3700](https://github.com/ceph/ceph/blob/v20.2.4/src/common/options/rgw.yaml.in#L3618-L3700)), `op.ListObjectsParams`, `op.ObjectEntry`; `radosclient.ReadOp.OmapGetVals(startAfter, filterPrefix string, maxEntries uint64)`, `OmapResult`; `meta.IsV2UploadID`, `MultipartPartKey`, `MultipartMetaName`, `ParseMultipartMeta`, `IsMultipartMeta`, `DecodeUploadPartInfo`; G's `op.ListPartsResult`, `op.Part`, `op.ListUploadsParams`, `op.ListUploadsResult`, `op.Upload`.
- Produces:

```go
package op

// ListObjectsParams gains:
//	// NameFilter, when set, is RGWRados::Bucket::ListParams::access_list_filter
//	// (rgw_rados.cc:2004-2009): an entry whose namespace-local name it rejects is
//	// skipped after the marker has advanced past it and before the prefix,
//	// delimiter and count are considered. ListMultipartUploads passes
//	// meta.IsMultipartMeta to see meta objects only.
//	NameFilter func(name string) bool
```

```go
package driver

// listPartsPage is RadosMultipartUpload::list_parts (rgw_sal_rados.cc:3329-3431):
// for a v2 upload id, omap get_vals after "part.%08d" of marker for max+1
// values, expecting consecutive numbers from marker+1 and falling back to the
// unsorted path on a gap (a legacy-id upload, or a key a mixed-version gateway
// wrote); for a legacy id, every value, sorted by number, those above marker.
// truncated is whether more parts follow; next is the last number returned.
func (s *Store) listPartsPage(ctx context.Context, ref mpRef, uploadID string, marker, max int) (parts []meta.UploadPartInfo, next int, truncated bool, err error)

func (s *Store) ListParts(ctx context.Context, up *op.Upload, marker, max int) (op.ListPartsResult, error)
func (s *Store) ListUploads(ctx context.Context, rec *op.BucketRecord, p op.ListUploadsParams) (op.ListUploadsResult, error)
```

`ListParts` reads the omap only: the op's `Init` already read the meta object through `GetUpload` for the ACL and the placement, so the driver does not repeat radosgw's `get_info` read (radosgw's ListParts reads the meta object twice, once for `read_obj_policy` and once in `execute`; rgw-go does it once — an op fewer, not a byte difference). A missing meta object makes the omap read ENOENT → `ErrNoSuchUpload`. `Part.Size` is `RadosMultipartPart::get_size()`, which is `info.accounted_size` (rgw_sal_rados.h; equal to `size` for the uncompressed parts phase 1 writes, the ORIGINAL size for compressed parts a radosgw with a compressing placement wrote), `Part.Mtime` is `info.modified`.

`ListUploads` is `RadosBucket::list_multiparts` ([rgw_sal_rados.cc:915-956](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L915-L956)): M's `ListObjects` with `NS: meta.NSMultipart`, `NameFilter: meta.IsMultipartMeta`, `Marker: meta.MultipartMetaName(p.KeyMarker, p.UploadIDMarker)` when `p.KeyMarker != ""` (radosgw builds `RGWMPObj(key_marker, upload_id_marker).get_meta()`, so an empty id marker yields `<key>..meta`), `Prefix`, `Delimiter`, `MaxKeys: p.MaxUploads`; each entry parses with `ParseMultipartMeta` into an `Upload{ID, Bucket: rec, Key: {Name: key}, Owner: entry.Owner, OwnerName: entry.OwnerDisplayName, Initiated: entry.Mtime}`; `CommonPrefixes` and `Truncated` pass through; `NextKeyMarker`/`NextUploadIDMarker` are the LAST upload's key and id, empty when the page holds no upload (RGWListBucketMultiparts::execute, [rgw_op.cc:6740-6743](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L6740-L6743) — not the listing's own next marker).

- [ ] **Step 1: Write the failing `NameFilter` spec in M's listing**

`internal/driver/listing_test.go` gains, on M's listing fixture (a bucket with entries seeded straight into the shard omap; adjust the expected `NextMarker` to M's form, index name or name):

```go
It("applies NameFilter after the marker advances and before counting, as access_list_filter does", func(ctx SpecContext) {
	// namespace multipart: "a.2~1.1" (a part head), "a.2~1.meta", "a.2~2.1", "a.2~2.meta", "b.2~3.meta"
	seedEntries(c, rec, meta.NSMultipart, "a.2~1.1", "a.2~1.meta", "a.2~2.1", "a.2~2.meta", "b.2~3.meta")
	res, err := s.ListObjects(ctx, rec, op.ListObjectsParams{NS: meta.NSMultipart, MaxKeys: 2, NameFilter: meta.IsMultipartMeta})
	Expect(err).NotTo(HaveOccurred())
	Expect(keyNames(res.Entries)).To(Equal([]string{"a.2~1.meta", "a.2~2.meta"}), "part heads neither count nor appear")
	Expect(res.Truncated).To(BeTrue())
	Expect(res.NextMarker).To(Equal("_multipart_a.2~2.meta"), "the marker is the last COUNTED entry's index name")
	res, err = s.ListObjects(ctx, rec, op.ListObjectsParams{NS: meta.NSMultipart, Marker: "a.2~2.meta", MaxKeys: 2, NameFilter: meta.IsMultipartMeta})
	Expect(err).NotTo(HaveOccurred())
	Expect(keyNames(res.Entries)).To(Equal([]string{"b.2~3.meta"}))
	Expect(res.Truncated).To(BeFalse())
})
```

Then, in `listObjectsOrdered`'s loop, right after the `if count < maxN { res.NextMarker = … }` block and before the prefix check (M plan :3686-3690; [rgw_rados.cc:1997-2009](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L1997-L2009)): `if p.NameFilter != nil && !p.NameFilter(key.Name) { continue }`; the same line in `listObjectsUnordered` at its equivalent point ([rgw_rados.cc:2160-2330](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L2160-L2330) `list_objects_unordered` applies the filter in the same relative position — confirm the line when editing). `memstore.ListObjects`: after the marker skip, before the prefix test. Regenerate the fakes (`make generate`).

- [ ] **Step 2: Write the failing `ListParts` and `ListUploads` specs**

`internal/driver/mp_list_test.go`, on Task 4's fixture with an upload `up` of key `k` holding parts 1, 2, 3, 5 (5 MiB each, part 4 never uploaded) written by `PutPart`:

```go
var _ = Describe("ListParts", func() {
	It("pages the omap after part.%08d of the marker and reports the last number", func(ctx SpecContext) {
		c.ResetCounters()
		res, err := s.ListParts(ctx, up, 0, 2)
		Expect(err).NotTo(HaveOccurred())
		Expect(c.Reads(extraPoolName, extraNS, metaOID(rec, "k", up.ID))).To(Equal(1))
		step := c.LastRead(extraPoolName, extraNS, metaOID(rec, "k", up.ID)).Steps()[0].(*radosclient.OmapGetValsStep)
		Expect(step.StartAfter).To(Equal("part.00000000"))
		Expect(step.MaxEntries).To(BeEquivalentTo(3), "num_parts + 1 detects truncation")
		Expect(numbers(res.Parts)).To(Equal([]int{1, 2}))
		Expect(res.NextMarker).To(Equal(2))
		Expect(res.Truncated).To(BeTrue())
		Expect(res.Parts[0].ETag).To(HaveLen(32))
		Expect(res.Parts[0].Size).To(BeEquivalentTo(5 << 20))
		Expect(res.Parts[0].Mtime).NotTo(BeZero())
	})
	It("falls back to the unsorted path on a gap and returns what is above the marker", func(ctx SpecContext) {
		res, err := s.ListParts(ctx, up, 2, 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(numbers(res.Parts)).To(Equal([]int{3, 5}), "part 4 is missing: the sorted walk sees 5 where it expected 4 and re-lists everything")
		Expect(res.NextMarker).To(Equal(5))
		Expect(res.Truncated).To(BeFalse())
		Expect(c.Reads(extraPoolName, extraNS, metaOID(rec, "k", up.ID))).To(Equal(2), "one sorted attempt, one get_all")
	})
	It("lists a legacy 2/ upload with get_all", func(ctx SpecContext) {
		// seed a meta object under id "2/legacy" with omap keys "part.1", "part.10", "part.2" (legacy keys are the part number string): ListParts(marker 0, max 10) returns 1, 2, 10 in numeric order; the read has StartAfter "" and MaxEntries 0 (or a page loop)
	})
	It("is NoSuchUpload for a vanished meta object", func(ctx SpecContext) {
		_, err := s.ListParts(ctx, &op.Upload{ID: "2~gone", Bucket: rec, Key: key}, 0, 10)
		Expect(err).To(MatchError(op.ErrNoSuchUpload))
	})
})

var _ = Describe("ListUploads", func() {
	BeforeEach(func(ctx SpecContext) {
		// uploads: "a/x" id A1, "a/x" id A2 (same key twice), "a/y" id B1, "b" id C1; parts on A1 so part heads share the namespace
	})
	It("lists meta objects only, in index order, with radosgw's markers", func(ctx SpecContext) {
		res, err := s.ListUploads(ctx, rec, op.ListUploadsParams{MaxUploads: 3})
		Expect(err).NotTo(HaveOccurred())
		Expect(uploadKeysAndIDs(res.Uploads)).To(Equal([][2]string{{"a/x", idA1}, {"a/x", idA2}, {"a/y", idB1}}), "sorted as index names _multipart_<key>.<id>.meta sort")
		Expect(res.Truncated).To(BeTrue())
		Expect(res.NextKeyMarker).To(Equal("a/y"))
		Expect(res.NextUploadIDMarker).To(Equal(idB1))
		Expect(res.Uploads[0].Owner).To(Equal(alice))
		Expect(res.Uploads[0].OwnerName).To(Equal("Alice"))
		Expect(res.Uploads[0].Initiated).NotTo(BeZero())
		res, err = s.ListUploads(ctx, rec, op.ListUploadsParams{KeyMarker: "a/y", UploadIDMarker: idB1, MaxUploads: 3})
		Expect(err).NotTo(HaveOccurred())
		Expect(uploadKeysAndIDs(res.Uploads)).To(Equal([][2]string{{"b", idC1}}))
		Expect(res.Truncated).To(BeFalse())
		Expect(res.NextKeyMarker).To(Equal("b"))
		Expect(res.NextUploadIDMarker).To(Equal(idC1))
	})
	It("passes prefix and delimiter through the listing, so common prefixes come from the meta names", func(ctx SpecContext) {
		res, err := s.ListUploads(ctx, rec, op.ListUploadsParams{Prefix: "a/", Delimiter: "/", MaxUploads: 10})
		Expect(err).NotTo(HaveOccurred())
		Expect(len(res.Uploads)).To(Equal(3))
		Expect(res.CommonPrefixes).To(BeEmpty())
		res, err = s.ListUploads(ctx, rec, op.ListUploadsParams{Delimiter: "/", MaxUploads: 10})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.CommonPrefixes).To(Equal([]string{"a/"}))
		Expect(uploadKeysAndIDs(res.Uploads)).To(Equal([][2]string{{"b", idC1}}))
		res, err = s.ListUploads(ctx, rec, op.ListUploadsParams{Delimiter: ".", MaxUploads: 10})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Uploads).To(BeEmpty(), "radosgw searches the delimiter in the whole meta name, '.<id>.meta' included")
		Expect(res.CommonPrefixes).To(Equal([]string{"a/x.", "a/y.", "b."}))
	})
	It("uses <key>..meta for a key marker without an upload id marker", func(ctx SpecContext) {
		res, err := s.ListUploads(ctx, rec, op.ListUploadsParams{KeyMarker: "a/x", MaxUploads: 10})
		Expect(err).NotTo(HaveOccurred())
		Expect(uploadKeysAndIDs(res.Uploads)[0]).To(Equal([2]string{"a/x", idA1}), "\"a/x..meta\" sorts before \"a/x.2~….meta\"")
	})
	It("returns empty next markers when the page holds no upload", func(ctx SpecContext) {
		res, err := s.ListUploads(ctx, rec, op.ListUploadsParams{Delimiter: ".", MaxUploads: 2})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Uploads).To(BeEmpty())
		Expect(res.NextKeyMarker).To(BeEmpty())
		Expect(res.NextUploadIDMarker).To(BeEmpty())
		Expect(res.Truncated).To(BeTrue())
	})
})
```

- [ ] **Step 3: Run the specs to see them fail**

Run: `go test -tags=ceph_preview ./internal/driver/ -ginkgo.focus="ListParts|ListUploads|NameFilter"`. Expected: failures/compile errors.

- [ ] **Step 4: Implement `mp_list.go`**

```go
package driver

func (s *Store) listPartsPage(ctx context.Context, ref mpRef, uploadID string, marker, max int) ([]meta.UploadPartInfo, int, bool, error) {
	pool := ref.pool
	if ref.loc != "" {
		pool = pool.WithLocator(ref.loc)
	}
	sorted := meta.IsV2UploadID(uploadID)
	read := func(startAfter string, n uint64) (map[string][]byte, error) {
		r := radosclient.NewReadOp()
		res := r.OmapGetVals(startAfter, "", n)
		if _, err := pool.Read(ctx, ref.oid, r, 0); err != nil {
			return nil, op.FromRADOS(err, op.ScopeUpload)
		}
		return res.Values, nil // OmapResult's map; see results.go:54-59 for the field name
	}
	decode := func(vals map[string][]byte) ([]meta.UploadPartInfo, error) {
		out := make([]meta.UploadPartInfo, 0, len(vals))
		for _, k := range slices.Sorted(maps.Keys(vals)) {
			d := denc.NewDecoder(vals[k])
			info := meta.DecodeUploadPartInfo(d)
			if d.Err() != nil {
				return nil, fmt.Errorf("%w: could not decode part info %q: %w", op.ErrInternalError, k, d.Err()) // -EIO
			}
			out = append(out, info)
		}
		return out, nil
	}
	if sorted {
		vals, err := read(meta.MultipartPartKey(uint32(marker)), uint64(max)+1) //nolint:gosec // the op bounds both
		if err != nil {
			return nil, 0, false, err
		}
		infos, err := decode(vals)
		if err != nil {
			return nil, 0, false, err
		}
		expected := uint32(marker) + 1 //nolint:gosec
		ok := true
		for i, info := range infos {
			if i >= max {
				break
			}
			if info.Num != expected {
				ok = false // a gap or a mixed-version key order: assume_unsorted
				break
			}
			expected++
		}
		if ok {
			trunc := len(infos) > max
			page := infos[:min(max, len(infos))]
			next := marker
			if len(page) > 0 {
				next = int(page[len(page)-1].Num)
			}
			return page, next, trunc, nil
		}
	}
	// get_all: every value, sorted by number, above the marker, max of them.
	var all map[string][]byte
	for after := ""; ; {
		vals, err := read(after, 1000)
		if err != nil {
			return nil, 0, false, err
		}
		if all == nil {
			all = vals
		} else {
			maps.Copy(all, vals)
		}
		if len(vals) < 1000 {
			break
		}
		after = slices.Max(slices.Collect(maps.Keys(vals)))
	}
	infos, err := decode(all)
	if err != nil {
		return nil, 0, false, err
	}
	slices.SortFunc(infos, func(a, b meta.UploadPartInfo) int { return cmp.Compare(a.Num, b.Num) })
	infos = slices.DeleteFunc(infos, func(p meta.UploadPartInfo) bool { return int(p.Num) <= marker })
	trunc := len(infos) > max
	page := infos[:min(max, len(infos))]
	next := marker
	if len(page) > 0 {
		next = int(page[len(page)-1].Num)
	}
	return page, next, trunc, nil
}

func (s *Store) ListParts(ctx context.Context, up *op.Upload, marker, max int) (op.ListPartsResult, error) {
	ref, err := s.metaRef(ctx, up.Bucket, up.Key, up.ID)
	if err != nil {
		return op.ListPartsResult{}, err
	}
	infos, next, trunc, err := s.listPartsPage(ctx, ref, up.ID, marker, max)
	if err != nil {
		return op.ListPartsResult{}, err
	}
	res := op.ListPartsResult{NextMarker: next, Truncated: trunc, Parts: make([]op.Part, 0, len(infos))}
	for _, p := range infos {
		res.Parts = append(res.Parts, op.Part{Number: int(p.Num), ETag: p.ETag, Size: p.AccountedSize, Mtime: p.Modified})
	}
	return res, nil
}

func (s *Store) ListUploads(ctx context.Context, rec *op.BucketRecord, p op.ListUploadsParams) (op.ListUploadsResult, error) {
	lp := op.ListObjectsParams{Prefix: p.Prefix, Delimiter: p.Delimiter, MaxKeys: p.MaxUploads, NS: meta.NSMultipart, NameFilter: meta.IsMultipartMeta}
	if p.KeyMarker != "" {
		lp.Marker = meta.MultipartMetaName(p.KeyMarker, p.UploadIDMarker)
	}
	lr, err := s.ListObjects(ctx, rec, lp)
	if err != nil {
		return op.ListUploadsResult{}, err
	}
	res := op.ListUploadsResult{CommonPrefixes: lr.CommonPrefixes, Truncated: lr.Truncated}
	for _, e := range lr.Entries {
		key, id, ok := meta.ParseMultipartMeta(e.Key.Name)
		if !ok {
			continue // the filter admitted only meta names; unreachable
		}
		res.Uploads = append(res.Uploads, op.Upload{ID: id, Bucket: rec, Key: meta.ObjKey{Name: key}, Owner: e.Owner, OwnerName: e.OwnerDisplayName, Initiated: e.Mtime})
	}
	if n := len(res.Uploads); n > 0 {
		res.NextKeyMarker = res.Uploads[n-1].Key.Name
		res.NextUploadIDMarker = res.Uploads[n-1].ID
	}
	return res, nil
}
```

(`OmapGetVals(startAfter, "", max+1)` is `sysobj.omap().get_vals(p, num_parts + 1, …)` with `p = "part." + %08d(marker)`: the class's `omap_get_vals` returns keys strictly AFTER `startAfter`, which is what radosgw relies on. M's `ListObjects` returns `Entries[i].Key` already namespace-stripped (`ParseIndexKeyName`), so `e.Key.Name` is `<key>.<id>.meta`; if M returns the index-key form instead, parse it with `meta.ParseIndexKeyName` first. `e.Mtime`, `e.Owner`, `e.OwnerDisplayName` are `ObjectEntry`'s.)

`memstore`: `ListUploads` filters its uploads through the same marker/prefix/delimiter logic applied to the synthetic name `<key>.<id>.meta` (so the `.`-delimiter oddity reproduces), `ListParts` keeps its sort-after-marker with `NextMarker` = the last number. Remove the two stubs from `store.go`.

- [ ] **Step 5: Run the specs**

Run: `go test -tags=ceph_preview ./internal/driver/ ./internal/memstore/ ./internal/op/`. Expected: PASS (M's listing specs included).

- [ ] **Step 6: Check and commit**

```bash
make check
git add internal/op internal/driver internal/memstore
git commit -m "feat(driver): ListParts and ListMultipartUploads

ListParts pages the meta object's omap after part.%08d of the marker
as list_parts does, falling back to a full read when the numbers are
not consecutive, and reports the last number returned.
ListMultipartUploads is the bucket listing in the multipart namespace
with list_multiparts' MultipartMetaFilter; ListObjectsParams gains
NameFilter, applied where access_list_filter runs, after the marker
advances and before prefix, delimiter and count, so part heads never
count toward max-uploads.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 6: `driver`: `Complete`

**Files:**
- Create: `internal/driver/mp_complete.go`, `internal/driver/mp_complete_test.go`, `internal/driver/mp_cleanup.go` (the GC/cleanup helpers Tasks 6 and 7 share), `internal/driver/mp_cleanup_test.go`
- Modify: `internal/op/multipart.go` (`Upload.WriteTag`, `Upload.IfMatch`, `Upload.IfNoneMatch`, doc corrections on `MultipartStore.Complete`), `internal/op/opfakes/` (regenerate), `internal/driver/store.go` (`mp mpOptions`, read at `Open`; the `Complete` stub goes), `internal/driver/export_test.go`, `internal/memstore/multipart.go` (P-D4: no `ErrInvalidPartOrder`; parts sorted and de-duplicated; the ETag-of-ETags stays), `internal/memstore/multipart_test.go`

**Interfaces:**
- Consumes: Task 1's `lock.LockExisting`, `lock.Unlock`; Task 3's `metaRef`, `readMeta`, `metaState`, the extended `headWrite`/`indexOp`; Task 5's `listPartsPage`; Task 2's `Manifest.Append`, `Manifest.StripeObj`, `UploadPartInfo`, `MultipartPartName`, `ObjectRetention`/`ObjectLegalHold`; W's `writeMeta`, `headResult`, `enqueueGC`, `gcChain`, `randTag`, `encodeAt`, `rgwBlStr`; R's `readHead` (for `checkPreviouslyCompleted`); `version.Check`, `version.ObjVersion`, `version.CondEQ` (the `Cond` constant's name in [`internal/cls/version/types.go:41-50`](https://github.com/jhoblitt/rgw-go/blob/50fc45c4d59953661d5c57c27ca7016bc55edeeb/internal/cls/version/types.go#L41-L50)); `rgw.ObjRemove`, `rgw.OpDel`, `rgw.EntryVer`, `rgw.ObjKey`; `op.Unquote` (R); `radosclient.OpFlagFullTry` (W Task 1), `ErrCanceled`, `ErrNotFound`, `ErrTimedOut`, `ErrExists`, `ErrBusy` (or the raw `&radosclient.Error{Errno: 16}`); `cephconf.Options.Seconds("rgw_mp_lock_max_time")` (type int → `Int64`), `Size("rgw_multipart_min_part_size")`.
- Produces:

```go
package op

// Upload gains request-scoped inputs for Complete, whose signature carries
// no params:
//	// WriteTag is the completed head's idtag and tail_tag; radosgw passes the
//	// request id (rgw_op.cc:6467, s->req_id). Empty selects a random tag.
//	WriteTag string
//	// IfMatch and IfNoneMatch are CompleteMultipartUpload's conditional headers,
//	// honoured by Tentacle only (v20.2.4 rgw_rest_s3.cc:4572-4573); the op leaves
//	// them empty on Squid.
//	IfMatch, IfNoneMatch string
```

```go
package driver

// mpOptions are the multipart tunables, read once at Open (rgw.yaml.in at v19.2.6).
type mpOptions struct {
	lockMaxTime time.Duration // rgw_mp_lock_max_time, 10 min (:459-466), type int seconds
	minPartSize uint64        // rgw_multipart_min_part_size, 5 MiB (:2357-2363)
}

func readMPOptions(conf *cephconf.Options) (mpOptions, error)

const completeLockName = "RGWCompleteMultipart"

// lockMeta is MPRadosSerializer::try_lock (rgw_sal_rados.cc:3750-3760): one
// write op, assert_exists then lock_exclusive under cookie "" for
// rgw_mp_lock_max_time. The raw seam error comes back for the caller's mapping.
func (s *Store) lockMeta(ctx context.Context, ref mpRef) error

// unlockMeta is the serializer's unlock (lock.unlock, cookie ""), sent when a
// completion or abort leaves the meta object in place; failures are logged
// (RGWCompleteMultipart::complete, rgw_op.cc:6582-6590).
func (s *Store) unlockMeta(ctx context.Context, ref mpRef)

// listAllParts drains listPartsPage in pages of 1000 (RadosMultipartUpload::
// complete and abort both page at 1000).
func (s *Store) listAllParts(ctx context.Context, ref mpRef, uploadID string) ([]meta.UploadPartInfo, error)

// partCleanup accumulates what a completion or abort retires: the index
// entries to remove in the closing class call and the GC chain of stripes.
type partCleanup struct {
	removeObjs []rgw.ObjKey
	chain      []rgw.GCObj
	prefixes   map[uint32]map[string]bool // processed_prefixes per part number
}

// retirePart is cleanup_part_history (rgw_sal_rados.cc:3103-3149) for one
// part: each past prefix's head "<pp>.<n>" leaves the index and every stripe
// of the part's manifest under that prefix joins the chain.
func (s *Store) retirePart(p meta.UploadPartInfo, c *partCleanup) error

// retireCurrentPart adds the part's CURRENT objects too (abort's and
// cleanup_orphaned_parts' update_gc_chain with the meta object as head: every
// stripe, the part head included, rgw_sal_rados.cc:3196-3207, :3061-3078) and
// its head's index entry.
func (s *Store) retireCurrentPart(p meta.UploadPartInfo, c *partCleanup) error

// deleteMeta is Delete::delete_obj on the meta object (rgw_rados.cc:5862-5987)
// with the extras the multipart callers pass: the index prepare DEL under a
// random tag on the upload key's shard, obj_remove keeping user.rgw.olh.*,
// cls_version_check(EQ) when the read version is non-zero, pool_full_try;
// success or ENOENT → complete_del with the removeObjs and the quota
// adjustment (-1 object, accounted bytes); any other error → cancel with the
// removeObjs. ECANCELED (a part landed since the read) comes back as
// radosclient.ErrCanceled for the caller's retry; ETIMEDOUT leaves the pending
// entry and returns ErrRequestTimedOut.
type metaDelete struct {
	rec        *op.BucketRecord
	key        meta.ObjKey // the upload's key: the shard hash source
	ref        mpRef
	version    version.ObjVersion
	mtime      time.Time
	removeObjs []rgw.ObjKey
	accounted  uint64 // bytes the quota cache drops: the parts' total on abort, the meta object's own on complete
}

func (s *Store) deleteMeta(ctx context.Context, d metaDelete) error

// checkPreviouslyCompleted is RGWCompleteMultipart::check_previously_completed
// (rgw_op.cc:6543-6580): the target's stored etag equals the ETag-of-ETags of
// the request's parts.
func (s *Store) checkPreviouslyCompleted(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, parts []op.CompletePart) (*op.ObjectState, bool)

// multipartETag is hex(MD5 of the concatenated 16-byte part digests) + "-" + count.
func multipartETag(etags []string) (string, error)

func (s *Store) Complete(ctx context.Context, up *op.Upload, parts []op.CompletePart) (*op.PutResult, error)
```

The sequence, as `RGWCompleteMultipart::execute` ([rgw_op.cc:6367-6540](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L6367-L6540)) over `RadosMultipartUpload::complete` ([rgw_sal_rados.cc:3433-3640](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L3433-L3640)) runs it — an N-part upload of `k`, ids and tags as in Task 4's fixture:

1. **Lock**: `[AssertExists, Exec(lock.lock{RGWCompleteMultipart, EXCLUSIVE, "", "", "", 600 s, 0})]` on the meta object in the extra pool (`MPRadosSerializer::try_lock`, [rgw_sal_rados.cc:3750-3760](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L3750-L3760) at v19.2.6). ENOENT → `checkPreviouslyCompleted`: one `readHead` of `k`; equal etag → return `{ETag: "", Size: st.Size, Mtime: st.Mtime}` (radosgw's early return, [rgw_op.cc:6438-6441](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L6438-L6441) at v19.2.6, leaves its `etag` member empty; it is assigned only at [:6533](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L6533)); else, and for EBUSY, EEXIST (this gateway's own earlier attempt still holds it) or anything else → `op.ErrInternalError.WithMessage("This multipart completion is already in progress")` wrapping the seam error (P-D9).
2. **Read**: `readMeta` — attrs, mtime, size, cls version (radosgw's head stat reads the version in the same op, `raw_obj_stat`, [rgw_rados.cc:8827-8870](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8827-L8870) at v19.2.6). A failure unlocks and returns `FromRADOS(err, ScopeObject)` (radosgw's raw errno: 404 NoSuchKey for the impossible vanish).
3. **Validate and assemble** (`RadosMultipartUpload::complete`, [rgw_sal_rados.cc:3433-3640](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L3433-L3640) at v19.2.6): `listAllParts`; sort `parts` by number and keep the last of duplicates (P-D4 — the op already did; the driver repeats it so a direct caller cannot bypass it); `len(stored) != len(parts)` → `ErrInvalidPart`; lockstep over `i`: `stored[i].Num != parts[i].Number` → `ErrInvalidPart`; `op.Unquote(parts[i].ETag) != stored[i].ETag` → `ErrInvalidPart`; `i < len(parts)-1 && stored[i].AccountedSize < minPartSize` → `ErrEntityTooSmall` (checked before the number, as radosgw orders it); an empty manifest (`len(Rules) == 0 && len(Objs) == 0`) → `ErrInvalidPart`; `manifest.Append(stored[i].Manifest)`; `removeObjs += ObjKey{Name: MultipartPartName(stored[i].Manifest.Prefix, num), NS: multipart}.IndexKeyName()` (radosgw's `src_obj` under the manifest's own prefix); `retirePart(stored[i], &cleanup)`; compression: `mergeCompression` per [:3542-3574](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L3542-L3574) — a part whose `Compression.Type != "none"` when an earlier one was uncompressed, or a different type or compressor message, → `ErrInvalidPart`; a compressed part shifts its blocks by the running `OrigSize`/`NewOfs` and marks the object compressed; `ofs += Size; accounted += AccountedSize`; MD5 accumulates `hex.DecodeString(stored[i].ETag)` (16 bytes; a non-hex stored etag is `ErrInvalidPart`). Any failure unlocks and returns.
4. **GC the history**: `enqueueGC(ctx, cleanup.chain, up.ID)` when the chain is non-empty — the tag is the UPLOAD ID, as `send_chain_to_gc(chain, mp_obj.get_upload_id())` sends it; synchronous, never fails the caller (W Task 5's semantics).
5. **The head write**: attrs = the META object's attrs (`ms.attrs`, `user.rgw.pg_ver` and `user.rgw.source_zone` included, since the op copies the meta object's attrs, [rgw_op.cc:6467](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L6467) at v19.2.6) with `user.rgw.etag` = `multipartETag`, `user.rgw.compression` when compressed, `user.rgw.object-retention`/`object-legal-hold` when `ms.info` carries them; `headWrite{rec, key: up.Key, tag: cmp.Or(up.WriteTag, s.w.randTag()), manifest: &manifest, attrs, mtime: now, create: true, atomic: true, modifyTail: true, completeMultipart: true, size: ofs, accountedSize: accounted, ifMatch: up.IfMatch, ifNoneMatch: up.IfNoneMatch}`; `x := s.newIndexOp(rec, up.Key, tag); x.removeObjs = cleanup.removeObjs`; `res, err := s.writeMeta(ctx, hw, x)` — W's machinery: prepare, the exclusive or guarded head write over an existing `k` (whose old tails W GCs under its tail_tag), the complete ADD that also retires every part entry (`remove_objs`, [cls_rgw.cc:1215-1227](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L1215-L1227) at v19.2.6), the quota `AdjustStats(+1|0, 0, origSize)`. A precondition failure or a lost race with conditions unlocks and returns W's error; a lost race without conditions is success (`res.canceled`), and the completion continues.
6. **Delete the meta object**, up to 15 attempts ([rgw_op.cc:6488-6528](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L6488-L6528)): `deleteMeta{rec, key: up.Key, ref, version: ms.version, mtime: ms.mtime, removeObjs: nil, accounted: ms.size}` (the meta object's own accounted size; `abortmp` false); on `ErrCanceled` and attempts left: `ms2, err := readMeta(ref)` (ENOENT → stop: someone removed it; another error → log and stop); `cleanup_orphaned_parts`: `listAllParts`, each part `retireCurrentPart` + `retirePart` (its head and stripes to the chain under the upload-id tag, its entry to `removeObjs`; parts that raced in after the completion are not part of the object), `enqueueGC(chain, up.ID)`, `version = ms2.version`, retry with the new `removeObjs`. Success → the lock died with the object: no unlock. Fifteen failures → log at error and unlock (radosgw's `complete()` unlocks whatever is still locked); the completion is still a success.
7. Return `{ETag: etag, Size: ofs, Mtime: res.mtime, Epoch: res.epoch}`.

- [ ] **Step 1: The contract fields, options, memstore**

`internal/op/multipart.go`: the three `Upload` fields with the comments above; correct `MultipartStore.Complete`'s doc: "a part that does not match is ErrInvalidPart; parts are sorted by number before validation, so no order error exists (radosgw's std::map)". `make generate`. `store.go`: `mp mpOptions` read in `Open` after W's `readWriteOptions` (`Int64("rgw_mp_lock_max_time")` seconds; `Size("rgw_multipart_min_part_size")`); the fixture's `conf(nil)` map gains `rgw_mp_lock_max_time: "600"`, `rgw_multipart_min_part_size: "5242880"`, `rgw_multipart_part_upload_limit: "10000"`. `memstore.Complete`: sort and de-duplicate by number, drop the `ErrInvalidPartOrder` return, keep the ETag/size/attrs behaviour; store `up.WriteTag` as the object's `WriteTag`; `multipart_test.go` adjusts the order spec to expect success with parts given as 3, 1, 2.

- [ ] **Step 2: Write the failing specs**

`internal/driver/mp_complete_test.go`, on Task 4's fixture, with `LockClass` registered and a helper `uploadWithParts(ctx, key, sizes ...int) (up *op.Upload, etags []string)` that creates the upload and `PutPart`s each size:

```go
var _ = Describe("Complete", func() {
	completeParts := func(etags []string) []op.CompletePart {
		out := make([]op.CompletePart, len(etags))
		for i, e := range etags {
			out[i] = op.CompletePart{Number: i + 1, ETag: `"` + e + `"`}
		}
		return out
	}
	It("assembles three parts into the object radosgw would write", func(ctx SpecContext) {
		up, etags := uploadWithParts(ctx, key, 5<<20, 5<<20, 3<<20)
		up.WriteTag = "tx-complete"
		metaOID := metaOID(rec, "k", up.ID)
		metaObj := c.Object(extraPoolName, extraNS, metaOID)
		metaPGVer := slices.Clone(metaObj.Xattrs[meta.AttrPGVer])
		metaSize := uint64(len(metaObj.Data))
		c.ResetCounters()
		res, err := s.Complete(ctx, up, completeParts(etags))
		Expect(err).NotTo(HaveOccurred())
		want, _ := driver.MultipartETagForTest(etags)
		Expect(res.ETag).To(Equal(want))
		Expect(res.ETag).To(HaveSuffix("-3"))
		Expect(res.Size).To(BeEquivalentTo(13 << 20))

		// the lock, in one op, before anything else
		lockOp := c.WritesTo(extraPoolName, extraNS, metaOID)[0].Steps()
		Expect(lockOp[0]).To(BeAssignableToTypeOf(&radosclient.AssertExistsStep{}))
		l := lock.DecodeLockOp(denc.NewDecoder(lockOp[1].(*radosclient.ExecStep).In))
		Expect(l).To(Equal(lock.LockOp{Name: "RGWCompleteMultipart", Type: lock.TypeExclusive, Duration: 600 * time.Second}))
		Expect(c.Reads(extraPoolName, extraNS, metaOID)).To(Equal(2), "get_obj_attrs, then one omap page")

		head := c.Object(dataPool, "", rec.Info.Bucket.Marker+"_k")
		Expect(head).NotTo(BeNil())
		Expect(head.Data).To(BeEmpty(), "no data step: every byte is in the part stripes")
		Expect(head.Xattrs).To(HaveKeyWithValue(meta.AttrIDTag, []byte("tx-complete\x00")))
		Expect(head.Xattrs).To(HaveKeyWithValue(meta.AttrTailTag, []byte("tx-complete\x00")))
		Expect(head.Xattrs).To(HaveKeyWithValue(meta.AttrETag, []byte(want)))
		Expect(head.Xattrs).To(HaveKey(meta.AttrACL), "the meta object's attrs become the object's")
		Expect(head.Xattrs).To(HaveKeyWithValue(meta.AttrPGVer, metaPGVer), "pg_ver is COPIED from the meta object, not recomputed (rgw_rados.cc:3236)")
		Expect(head.Xattrs).NotTo(HaveKey(meta.AttrCompression))
		m := meta.DecodeManifest(denc.NewDecoder(head.Xattrs[meta.AttrManifest]))
		Expect(m.ObjSize).To(BeEquivalentTo(13 << 20))
		Expect(m.HeadSize).To(BeZero())
		Expect(m.Prefix).To(Equal("k." + up.ID))
		Expect(m.Rules).To(Equal(map[uint64]meta.ManifestRule{
			0:        {StartPartNum: 1, StartOfs: 0, PartSize: 5 << 20, StripeMaxSize: 4 << 20},
			10 << 20: {StartPartNum: 3, StartOfs: 10 << 20, PartSize: 3 << 20, StripeMaxSize: 4 << 20},
		}))
		stripes, _ := m.Stripes()
		Expect(stripes).To(HaveLen(5), "5+5+3 MiB in 4 MiB stripes: 2+2+1")
		for _, st := range stripes {
			Expect(c.Object(dataPool, "", st.OID())).NotTo(BeNil(), st.OID())
		}
		steps := c.LastWrite(dataPool, "", rec.Info.Bucket.Marker+"_k").Steps()
		Expect(steps[0]).To(Equal(&radosclient.CreateStep{Exclusive: true}))
		Expect(names(steps)).NotTo(ContainElement("obj_store_pg_ver"), "attrs carried pg_ver")
		Expect(names(steps)).To(ContainElement("user.rgw.storage_class"), "from the manifest's tail rule") // only when the fixture bucket's class is non-empty; with the default "" class expect NotTo
		Expect(names(steps)).To(ContainElements("user.rgw.idtag", "user.rgw.tail_tag", "user.rgw.manifest", "user.rgw.etag"))

		shard := shardOf(rec, "k")
		Eventually(func() bool { en, ok := c.Entry(indexPool, "", shard, "k"); return ok && en.Exists }).Should(BeTrue())
		en, _ := c.Entry(indexPool, "", shard, "k")
		Expect(en.Meta).To(Equal(rgwcls.DirEntryMeta{Category: rgwcls.CategoryMain, Size: 13 << 20, AccountedSize: 13 << 20, Mtime: res.Mtime, ETag: want, Owner: "alice", OwnerDisplayName: "Alice", ContentType: "text/plain"}))
		for n := 1; n <= 3; n++ {
			_, ok := c.Entry(indexPool, "", shard, fmt.Sprintf("_multipart_k.%s.%d", up.ID, n))
			Expect(ok).To(BeFalse(), "remove_objs retired part %d in the complete", n)
		}
		comp := rgwcls.DecodeCompleteOp(denc.NewDecoder(lastExecIn(c.WritesTo(indexPool, "", shard), "bucket_complete_op", rgwcls.OpAdd)))
		Expect(comp.RemoveObjs).To(HaveLen(3))
		Eventually(func() bool { _, ok := c.Entry(indexPool, "", shard, "_multipart_k."+up.ID+".meta"); return ok }).Should(BeFalse(), "the meta entry went with complete_del")
		hdr := c.Header(indexPool, "", shard)
		Expect(hdr.Stats[rgwcls.CategoryMain].NumEntries).To(BeEquivalentTo(1))
		Expect(hdr.Stats[rgwcls.CategoryMain].TotalSize).To(BeEquivalentTo(13 << 20))
		Expect(hdr.Stats[rgwcls.CategoryMultiMeta].NumEntries).To(BeZero())

		Expect(c.Object(extraPoolName, extraNS, metaOID)).To(BeNil(), "the meta object is gone")
		del := c.LastWrite(extraPoolName, extraNS, metaOID)
		Expect(del.Flags()).To(Equal(radosclient.OpFlagFullTry))
		ds := del.Steps()
		Expect(ds[0].(*radosclient.ExecStep).Method).To(Equal("obj_remove"))
		Expect(ds[1].(*radosclient.ExecStep).Class).To(Equal("version"))
		Expect(ds[1].(*radosclient.ExecStep).Method).To(Equal("check"))
		chk := version.DecodeCheckOp(denc.NewDecoder(ds[1].(*radosclient.ExecStep).In))
		Expect(chk.Objv.Ver).To(BeEquivalentTo(3), "three part registrations incremented it")
		Expect(execMethods(c.WritesTo(extraPoolName, extraNS, metaOID))).NotTo(ContainElement("unlock"), "the lock died with the object")
		Expect(memStatsAdjustments()).To(ContainElements(adjustment{objs: 1, bytes: 0, removed: 0}, adjustment{objs: -1, bytes: 0, removed: metaSize}))
	})
	It("accepts the parts in any order and ignores a duplicated number's earlier entry", func(ctx SpecContext) {
		up, etags := uploadWithParts(ctx, key, 5<<20, 5<<20)
		parts := []op.CompletePart{{Number: 2, ETag: etags[1]}, {Number: 1, ETag: "\"bogus\""}, {Number: 1, ETag: etags[0]}}
		_, err := s.Complete(ctx, up, parts)
		Expect(err).NotTo(HaveOccurred(), "std::map<int, string>: sorted, last duplicate wins (rgw_multi.cc:44-53)")
	})
	DescribeTable("refuses what radosgw refuses",
		func(mutate func(up *op.Upload, parts []op.CompletePart) []op.CompletePart, want error) {
			up, etags := uploadWithParts(ctx, key, 5<<20, 5<<20, 1<<20)
			parts := mutate(up, completeParts(etags))
			_, err := s.Complete(ctx, up, parts)
			Expect(err).To(MatchError(want))
			Expect(c.Object(dataPool, "", rec.Info.Bucket.Marker+"_k")).To(BeNil(), "nothing written")
			Expect(c.Object(extraPoolName, extraNS, metaOID(rec, "k", up.ID))).NotTo(BeNil(), "the meta object stays")
			Expect(execMethods(c.WritesTo(extraPoolName, extraNS, metaOID(rec, "k", up.ID)))).To(ContainElement("unlock"), "RGWCompleteMultipart::complete releases the lock")
			Expect(c.Writes(indexPool, "", shardOf(rec, "k"))).To(Equal(8), "the upload's and three parts' prepare+complete only")
		},
		Entry("a wrong etag", func(_ *op.Upload, p []op.CompletePart) []op.CompletePart { p[1].ETag = `"` + md5hex("x") + `"`; return p }, op.ErrInvalidPart),
		Entry("a missing part", func(_ *op.Upload, p []op.CompletePart) []op.CompletePart { return p[:2] }, op.ErrInvalidPart),
		Entry("a part that was never uploaded", func(_ *op.Upload, p []op.CompletePart) []op.CompletePart { p[2].Number = 7; return p }, op.ErrInvalidPart),
		Entry("a small part before the last", func(ctx SpecContext) func(*op.Upload, []op.CompletePart) []op.CompletePart {
			return func(up *op.Upload, p []op.CompletePart) []op.CompletePart {
				r, _ := s.PutPart(ctx, up, 4, strings.NewReader("tiny"), op.PutParams{Attrs: partAttrs(), Size: 4})
				return append(p, op.CompletePart{Number: 4, ETag: r.ETag}) // part 3 (1 MiB) is now not last
			}
		}(ctx), op.ErrEntityTooSmall),
	)
	It("GCs a re-uploaded part's earlier objects under the upload id and retires both entries", func(ctx SpecContext) {
		up, etags := uploadWithParts(ctx, key, 5<<20, 5<<20)
		s.SetRandForTest(fixedRand("RANDOMRANDOMRANDOMRANDOMRANDOM12XYZ"))
		second := bytes.Repeat([]byte("b"), 5<<20)
		r, err := s.PutPart(ctx, up, 1, bytes.NewReader(second), op.PutParams{Attrs: partAttrs(), Size: int64(len(second))})
		Expect(err).NotTo(HaveOccurred())
		etags[0] = r.ETag
		_, err = s.Complete(ctx, up, completeParts(etags))
		Expect(err).NotTo(HaveOccurred())
		shard := shardOf(rec, "k")
		for _, name := range []string{"_multipart_k." + up.ID + ".1", "_multipart_k.RANDOMRANDOMRANDOMRANDOMRANDOM12.1", "_multipart_k." + up.ID + ".2"} {
			_, ok := c.Entry(indexPool, "", shard, name)
			Expect(ok).To(BeFalse(), name)
		}
		entries := c.GCEntries(gcPoolName, gcNS, fmt.Sprintf("gc.%d", driver.GCShardForTest(s, up.ID)))
		Expect(entries).To(HaveLen(1))
		Expect(entries[0].Tag).To(Equal(up.ID), "cleanup_part_history tags the chain with the upload id")
		Expect(chainOIDs(entries[0].Chain)).To(ConsistOf(rec.Info.Bucket.Marker+"__multipart_k."+up.ID+".1", rec.Info.Bucket.Marker+"__shadow_k."+up.ID+".1_1"), "the old part's head and stripe")
		head := c.Object(dataPool, "", rec.Info.Bucket.Marker+"_k")
		m := meta.DecodeManifest(denc.NewDecoder(head.Xattrs[meta.AttrManifest]))
		Expect(m.Rules[0].OverridePrefix).To(Equal("k.RANDOMRANDOMRANDOMRANDOMRANDOM12"), "part 1's rule names the new prefix")
		Expect(m.Rules[5<<20].OverridePrefix).To(BeEmpty())
	})
	It("answers 500 'already in progress' when the lock is held, and touches nothing", func(ctx SpecContext) {
		up, etags := uploadWithParts(ctx, key, 5<<20)
		holdLock(c, metaOID(rec, "k", up.ID), lock.EntityName{Type: 8, Num: 999}) // seeds the lock xattr for another client
		_, err := s.Complete(ctx, up, completeParts(etags))
		Expect(err).To(MatchError(op.ErrInternalError))
		Expect(op.AsError(err).Message).To(Equal("This multipart completion is already in progress"))
		Expect(c.Object(dataPool, "", rec.Info.Bucket.Marker+"_k")).To(BeNil())
	})
	It("answers 200 with an empty ETag for a re-sent completion of a finished upload, and 500 for an unknown id", func(ctx SpecContext) {
		up, etags := uploadWithParts(ctx, key, 5<<20, 5<<20)
		first, err := s.Complete(ctx, up, completeParts(etags))
		Expect(err).NotTo(HaveOccurred())
		again, err := s.Complete(ctx, up, completeParts(etags))
		Expect(err).NotTo(HaveOccurred(), "check_previously_completed: the etags match")
		Expect(again.ETag).To(BeEmpty(), "rgw_op.cc:6428-6435 returns before etag is assigned")
		Expect(again.Size).To(Equal(first.Size))
		_, err = s.Complete(ctx, &op.Upload{ID: "2~nope", Bucket: rec, Key: key}, completeParts(etags[:1]))
		Expect(err).To(MatchError(op.ErrInternalError), "a lock on a missing object is ENOENT, and the etag does not match")
	})
	It("retries the meta delete after a racing part and GCs the orphan", func(ctx SpecContext) {
		up, etags := uploadWithParts(ctx, key, 5<<20, 5<<20)
		metaOID := metaOID(rec, "k", up.ID)
		raced := false
		c.BeforeWrite(extraPoolName, extraNS, metaOID, func(o *fakerados.Object) {
			if o != nil && !raced && hasStep(o, "obj_remove") { // the first delete attempt: a part 3 lands first
				raced = true
				r, err := s.PutPart(ctx, up, 3, bytes.NewReader(bytes.Repeat([]byte("z"), 5<<20)), op.PutParams{Attrs: partAttrs(), Size: 5 << 20})
				Expect(err).NotTo(HaveOccurred())
				_ = r
			}
		})
		res, err := s.Complete(ctx, up, completeParts(etags))
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Size).To(BeEquivalentTo(10 << 20), "part 3 is not part of the object")
		Expect(c.Object(extraPoolName, extraNS, metaOID)).To(BeNil(), "the second delete succeeded")
		shard := shardOf(rec, "k")
		_, ok := c.Entry(indexPool, "", shard, "_multipart_k."+up.ID+".3")
		Expect(ok).To(BeFalse(), "cleanup_orphaned_parts retired it with the retried delete")
		entries := c.GCEntries(gcPoolName, gcNS, fmt.Sprintf("gc.%d", driver.GCShardForTest(s, up.ID)))
		Expect(chainOIDs(flatten(entries))).To(ContainElements(rec.Info.Bucket.Marker+"__multipart_k."+up.ID+".3", rec.Info.Bucket.Marker+"__shadow_k."+up.ID+".3_1"))
	})
	It("honours If-None-Match: * on Tentacle by refusing an existing key before writing", func(ctx SpecContext) {
		t := denc.Tentacle
		s2, _ := driver.Open(ctx, c, conf(nil), driver.Options{Release: &t})
		_, err := s2.PutObject(ctx, rec, key, strings.NewReader("old"), op.PutParams{Attrs: attrsWithACL(alice), Size: 3, Tag: "t"})
		Expect(err).NotTo(HaveOccurred())
		up, etags := uploadWithParts2(ctx, s2, key, 5<<20)
		up.IfNoneMatch = "*"
		_, err = s2.Complete(ctx, up, completeParts(etags))
		Expect(err).To(MatchError(op.ErrPreconditionFailed))
		Expect(c.Object(dataPool, "", rec.Info.Bucket.Marker+"_k").Data).To(Equal([]byte("old")))
		Expect(c.Object(extraPoolName, extraNS, metaOID(rec, "k", up.ID))).NotTo(BeNil(), "the upload survives a failed conditional completion")
	})
	It("merges radosgw-written compressed parts' block maps and refuses a codec change", func(ctx SpecContext) {
		// seed two UploadPartInfo entries by hand with Compression{Type: "zlib", OrigSize: 5 MiB, Blocks: [{0, 0, 3 MiB}]} each, Size 3 MiB, AccountedSize 5 MiB, manifests from NewPartManifest; Complete → user.rgw.compression decodes to OrigSize 10 MiB, two blocks, the second at OldOfs 5 MiB / NewOfs 3 MiB (rgw_sal_rados.cc:3542-3574); a third part with Type "snappy" → ErrInvalidPart
	})
})
```

(`c.WritesTo(pool, ns, oid)` is the list of write records in order, `LastWrite` its last; `lastExecIn`, `execMethods`, `chainOIDs`, `flatten`, `hasStep`, `holdLock` are small spec helpers to write beside the specs; `holdLock` encodes a `lock.Info` with the given holder and 10 minutes of expiry into `Xattrs["lock.RGWCompleteMultipart"]`.)

- [ ] **Step 3: Run the specs to see them fail**

Run: `go test -tags=ceph_preview ./internal/driver/ -ginkgo.focus=Complete`. Expected: compile failures.

- [ ] **Step 4: Implement `mp_cleanup.go` and `mp_complete.go`**

`mp_cleanup.go`:

```go
package driver

const completeLockName = "RGWCompleteMultipart"

func (s *Store) lockMeta(ctx context.Context, ref mpRef) error {
	w := radosclient.NewWriteOp()
	lock.LockExisting(w, completeLockName, "", "", s.mp.lockMaxTime, s.release)
	pool := ref.pool
	if ref.loc != "" {
		pool = pool.WithLocator(ref.loc)
	}
	_, err := pool.Write(ctx, ref.oid, w, 0)
	return err
}

func (s *Store) unlockMeta(ctx context.Context, ref mpRef) {
	w := radosclient.NewWriteOp()
	lock.Unlock(w, completeLockName, "", s.release)
	if _, err := ref.pool.Write(s.w.completions.ctx, ref.oid, w, 0); err != nil && !errors.Is(err, radosclient.ErrNotFound) {
		slog.WarnContext(ctx, "failed to unlock multipart meta object", slog.String("oid", ref.oid), slog.Any("error", err))
	}
}

func (s *Store) listAllParts(ctx context.Context, ref mpRef, uploadID string) ([]meta.UploadPartInfo, error) {
	var all []meta.UploadPartInfo
	for marker, more := 0, true; more; {
		page, next, trunc, err := s.listPartsPage(ctx, ref, uploadID, marker, 1000)
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		marker, more = next, trunc
	}
	return all, nil
}

func (c *partCleanup) seen(part uint32, prefix string) bool {
	if c.prefixes == nil {
		c.prefixes = map[uint32]map[string]bool{}
	}
	if c.prefixes[part] == nil {
		c.prefixes[part] = map[string]bool{}
	}
	if c.prefixes[part][prefix] {
		return true
	}
	c.prefixes[part][prefix] = true
	return false
}

// stripesOf lists every stripe of m under prefix as GC chain entries: pool
// "<pool>" or "<pool>:<ns>" as rgw_pool::to_str renders it, the raw oid, the locator.
func (s *Store) stripesOf(m meta.Manifest, prefix string) ([]rgw.GCObj, meta.Obj, error) {
	mm := m
	mm.Prefix = prefix
	for k, r := range mm.Rules { // an override prefix from Append is not what a part's own manifest carries; clear defensively
		r.OverridePrefix = ""
		mm.Rules[k] = r
	}
	stripes, err := mm.Stripes()
	if err != nil {
		return nil, meta.Obj{}, err
	}
	pool, ok := s.dataPool(mm.TailPlacement.PlacementRule, mm.TailPlacement.Bucket) // with radosgw's fallbacks
	if !ok {
		return nil, meta.Obj{}, fmt.Errorf("%w: no data pool for %v", op.ErrInternalError, mm.TailPlacement.PlacementRule)
	}
	var out []rgw.GCObj
	var head meta.Obj
	for i, st := range stripes {
		if i == 0 {
			head = st.Obj
		}
		out = append(out, rgw.GCObj{Pool: pool.String(), Key: rgw.ObjKey{Name: st.OID()}, Locator: st.Locator()})
	}
	return out, head, nil
}

func (s *Store) retirePart(p meta.UploadPartInfo, c *partCleanup) error {
	for _, pp := range p.PastPrefixes {
		if c.seen(p.Num, pp) {
			continue
		}
		c.removeObjs = append(c.removeObjs, rgw.ObjKey{Name: meta.ObjKey{Name: meta.MultipartPartName(pp, p.Num), NS: meta.NSMultipart}.IndexKeyName()})
		chain, _, err := s.stripesOf(p.Manifest, pp)
		if err != nil {
			return err
		}
		c.chain = append(c.chain, chain...)
	}
	return nil
}

func (s *Store) retireCurrentPart(p meta.UploadPartInfo, c *partCleanup) error {
	prefix := p.Manifest.Prefix
	if prefix == "" || c.seen(p.Num, prefix) {
		return nil
	}
	chain, head, err := s.stripesOf(p.Manifest, prefix)
	if err != nil {
		return err
	}
	c.chain = append(c.chain, chain...)
	c.removeObjs = append(c.removeObjs, rgw.ObjKey{Name: head.Key.IndexKeyName()})
	return nil
}

func (s *Store) deleteMeta(ctx context.Context, d metaDelete) error {
	x := s.newIndexOp(d.rec, d.ref.key, "")
	x.hashName = d.key.Name
	x.removeObjs = d.removeObjs
	if err := x.prepare(ctx, rgw.OpDel); err != nil {
		return op.FromRADOS(err, op.ScopeUpload)
	}
	w := radosclient.NewWriteOp()
	rgw.ObjRemove(w, []string{meta.AttrOLHPrefix}, s.release) // remove_rgw_head_obj
	if d.version.Ver != 0 {
		version.Check(w, d.version, version.CondEQ, s.release) // params.check_objv
	}
	pool := d.ref.pool
	if d.ref.loc != "" {
		pool = pool.WithLocator(d.ref.loc)
	}
	epoch, err := pool.Write(ctx, d.ref.oid, w, radosclient.OpFlagFullTry)
	switch {
	case errors.Is(err, radosclient.ErrTimedOut):
		return op.ErrRequestTimedOut // the pending entry stays for the listing to reconcile
	case err == nil || errors.Is(err, radosclient.ErrNotFound):
		x.completeDel(rgw.EntryVer{Pool: pool.ID(), Epoch: epoch}, d.mtime)
		if aerr := s.AdjustStats(ctx, d.rec, d.rec.Info.Owner, -1, 0, int64(d.accounted)); aerr != nil { //nolint:gosec
			slog.WarnContext(ctx, "quota cache adjustment failed", slog.Any("error", aerr))
		}
		return nil
	default:
		x.cancel() // index_op.cancel with the remove_objs (rgw_rados.cc:5969): a CANCEL at pool -1, epoch 0 (:9559-9560)
		if errors.Is(err, radosclient.ErrCanceled) {
			return err
		}
		return op.FromRADOS(err, op.ScopeUpload)
	}
}
```

(`rgw.GCObj`'s field names are W Task 2's/phase 0's — `Pool`, `Key`, `Locator`; `pool.String()` is `meta.Pool.String()`, `"<name>"` or `"<name>:<ns>"`, the `rgw_pool::to_str` form W's `gcChain` uses. `x.completeDel(ver, mtime)` and `x.cancel()` include `x.removeObjs` after Task 3. The cancel takes no version: W's `indexOp.cancel` always sends radosgw's `-1:0` (W-D8), which is what `Delete::delete_obj`'s `index_op.cancel` sends for the meta object too, through `UpdateIndex::cancel` and `cls_obj_complete_cancel` ([rgw_rados.cc:5969](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5969), [:7190-7205](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L7190-L7205), [:9553-9563](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L9553-L9563) at v19.2.6; [:6723](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6723), :8041-8056, [:10485-10495](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L10485-L10495) at v20.2.4).)

`mp_complete.go`:

```go
package driver

func multipartETag(etags []string) (string, error) {
	h := md5.New() //nolint:gosec // S3's ETag algorithm
	for _, e := range etags {
		b, err := hex.DecodeString(e)
		if err != nil || len(b) != md5.Size {
			return "", op.ErrInvalidPart
		}
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil)) + "-" + strconv.Itoa(len(etags)), nil
}

func sortParts(parts []op.CompletePart) []op.CompletePart {
	byNum := map[int]string{}
	for _, p := range parts {
		byNum[p.Number] = p.ETag // std::map: last duplicate wins
	}
	out := make([]op.CompletePart, 0, len(byNum))
	for _, n := range slices.Sorted(maps.Keys(byNum)) {
		out = append(out, op.CompletePart{Number: n, ETag: byNum[n]})
	}
	return out
}

// mergeCompression is RadosMultipartUpload::complete's cs_info handling
// (rgw_sal_rados.cc:3542-3574).
func mergeCompression(cs *meta.CompressionInfo, compressed *bool, part meta.CompressionInfo, handled int) error {
	partCompressed := part.Type != "none"
	if handled > 0 && (partCompressed != *compressed || cs.Type != part.Type || (cs.CompressorMessage != nil && !equalMessage(cs.CompressorMessage, part.CompressorMessage))) {
		return op.ErrInvalidPart
	}
	if !partCompressed {
		return nil
	}
	newOfs := uint64(0)
	if n := len(cs.Blocks); n > 0 {
		newOfs = cs.Blocks[n-1].NewOfs + cs.Blocks[n-1].Len
	}
	for _, b := range part.Blocks {
		cb := meta.CompressionBlock{OldOfs: b.OldOfs + cs.OrigSize, NewOfs: newOfs, Len: b.Len}
		cs.Blocks = append(cs.Blocks, cb)
		newOfs = cb.NewOfs + cb.Len
	}
	if !*compressed {
		cs.Type = part.Type
		if part.CompressorMessage != nil {
			cs.CompressorMessage = part.CompressorMessage
		}
	}
	cs.OrigSize += part.OrigSize
	*compressed = true
	return nil
}

func (s *Store) checkPreviouslyCompleted(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, parts []op.CompletePart) (*op.ObjectState, bool) {
	st, err := s.readHead(ctx, rec, key, false)
	if err != nil || !st.Exists {
		return nil, false
	}
	etags := make([]string, len(parts))
	for i, p := range parts {
		etags[i] = op.Unquote(p.ETag)
	}
	want, err := multipartETag(etags)
	return st, err == nil && st.ETag == want
}

func (s *Store) Complete(ctx context.Context, up *op.Upload, parts []op.CompletePart) (*op.PutResult, error) {
	rec, key := up.Bucket, up.Key
	parts = sortParts(parts)
	ref, err := s.metaRef(ctx, rec, key, up.ID)
	if err != nil {
		return nil, err
	}
	if err := s.lockMeta(ctx, ref); err != nil {
		if errors.Is(err, radosclient.ErrNotFound) {
			if st, ok := s.checkPreviouslyCompleted(ctx, rec, key, parts); ok {
				slog.InfoContext(ctx, "multipart completion already completed", slog.String("upload", up.ID))
				return &op.PutResult{Size: st.Size, Mtime: st.Mtime, Epoch: st.Epoch}, nil // ETag "" as radosgw renders it (rgw_op.cc:6438-6441 at v19.2.6)
			}
		}
		return nil, fmt.Errorf("%w: %w", op.ErrInternalError.WithMessage("This multipart completion is already in progress"), err)
	}
	locked := true
	defer func() {
		if locked {
			s.unlockMeta(ctx, ref)
		}
	}()
	ms, err := s.readMeta(ctx, ref)
	if err != nil {
		return nil, err
	}
	stored, err := s.listAllParts(ctx, ref, up.ID)
	if err != nil {
		return nil, err
	}
	if len(stored) != len(parts) {
		return nil, op.ErrInvalidPart
	}
	var (
		manifest   meta.Manifest
		cleanup    partCleanup
		cs         = meta.NewCompressionInfo()
		compressed bool
		etags      []string
		ofs, acc   uint64
	)
	for i, p := range parts {
		st := stored[i]
		if i < len(parts)-1 && st.AccountedSize < s.mp.minPartSize {
			return nil, op.ErrEntityTooSmall
		}
		if int(st.Num) != p.Number || op.Unquote(p.ETag) != st.ETag {
			return nil, op.ErrInvalidPart
		}
		if len(st.Manifest.Rules) == 0 && len(st.Manifest.Objs) == 0 {
			return nil, op.ErrInvalidPart
		}
		if err := manifest.Append(st.Manifest); err != nil {
			return nil, fmt.Errorf("%w: %w", op.ErrInvalidPart, err)
		}
		cleanup.removeObjs = append(cleanup.removeObjs, rgw.ObjKey{Name: meta.ObjKey{Name: meta.MultipartPartName(st.Manifest.Prefix, st.Num), NS: meta.NSMultipart}.IndexKeyName()})
		cleanup.seen(st.Num, st.Manifest.Prefix)
		if err := s.retirePart(st, &cleanup); err != nil {
			return nil, err
		}
		if err := mergeCompression(&cs, &compressed, st.Compression, i); err != nil {
			return nil, err
		}
		etags = append(etags, st.ETag)
		ofs += st.Size
		acc += st.AccountedSize
	}
	etag, err := multipartETag(etags)
	if err != nil {
		return nil, err
	}
	if len(cleanup.chain) > 0 {
		s.enqueueGC(ctx, cleanup.chain, up.ID)
	}
	attrs := maps.Clone(ms.attrs)
	attrs[meta.AttrETag] = []byte(etag)
	if compressed {
		attrs[meta.AttrCompression] = encodeAt(cs, s.release)
	}
	if ms.info.Retention != nil {
		attrs[meta.AttrObjectRetention] = encodeAt(*ms.info.Retention, s.release)
	}
	if ms.info.LegalHold != nil {
		attrs[meta.AttrObjectLegalHold] = encodeAt(*ms.info.LegalHold, s.release)
	}
	tag := cmp.Or(up.WriteTag, s.w.randTag())
	hw := &headWrite{rec: rec, key: key, tag: tag, manifest: &manifest, attrs: attrs, mtime: s.now(), create: true, atomic: true, modifyTail: true, completeMultipart: true, size: ofs, accountedSize: acc, ifMatch: up.IfMatch, ifNoneMatch: up.IfNoneMatch}
	x := s.newIndexOp(rec, key, tag)
	x.removeObjs = cleanup.removeObjs
	res, err := s.writeMeta(ctx, hw, x)
	if err != nil {
		return nil, err
	}
	// remove the meta object; cls_version_check catches parts that raced with the assembly
	d := metaDelete{rec: rec, key: key, ref: ref, version: ms.version, mtime: ms.mtime, accounted: ms.size}
	for attempt := 0; attempt < 15; attempt++ {
		err := s.deleteMeta(ctx, d)
		if err == nil {
			locked = false // the lock went with the object
			break
		}
		if !errors.Is(err, radosclient.ErrCanceled) || attempt == 14 {
			slog.ErrorContext(ctx, "failed to remove multipart meta object", slog.String("oid", ref.oid), slog.Any("error", err))
			break
		}
		ms2, rerr := s.readMeta(ctx, ref)
		if rerr != nil {
			if errors.Is(rerr, op.ErrNoSuchUpload) {
				locked = false
			}
			break
		}
		orphans, lerr := s.listAllParts(ctx, ref, up.ID)
		if lerr != nil {
			slog.ErrorContext(ctx, "failed to list orphaned parts", slog.Any("error", lerr))
			break
		}
		var oc partCleanup
		for _, p := range orphans {
			if err := s.retireCurrentPart(p, &oc); err != nil {
				return nil, err
			}
			if err := s.retirePart(p, &oc); err != nil {
				return nil, err
			}
		}
		if len(oc.chain) > 0 {
			s.enqueueGC(ctx, oc.chain, up.ID)
		}
		d.version, d.mtime, d.accounted, d.removeObjs = ms2.version, ms2.mtime, ms2.size, oc.removeObjs
	}
	return &op.PutResult{ETag: etag, Size: ofs, Mtime: res.mtime, Epoch: res.epoch}, nil
}
```

`readHead`'s returned `ObjectState.ETag` already has its one trailing NUL stripped (R Task 3), so the comparison with `multipartETag` needs no `rgwBlStr`. The orphan retirement covers parts that raced in during the assembly (`cleanup_orphaned_parts` on ALL parts still listed: after the completion's `remove_objs` retired the assembled parts' entries, only orphans remain in the omap — the omap itself is deleted with the meta object).

- [ ] **Step 5: Run the specs**

Run: `go test -tags=ceph_preview ./internal/driver/ ./internal/memstore/`. Expected: PASS.

- [ ] **Step 6: Check and commit**

```bash
make check
git add internal/op internal/driver internal/memstore
git commit -m "feat(driver): CompleteMultipartUpload under the RGWCompleteMultipart lock

Complete takes radosgw's exclusive lock on the meta object with
assert_exists in the same op, validates the parts in lockstep with
the stored ones, appends their manifests with RGWObjManifest::append's
arithmetic, writes the ETag-of-ETags head atomically under the
request's tag with the meta object's attrs, retires every part entry
in the same index complete, GCs re-uploaded parts' history under the
upload id, and removes the meta object behind cls_version_check,
cleaning up parts that raced in. A lock failure answers radosgw's 500
unless the object already carries the recomputed ETag.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 7: `driver`: `Abort`

**Files:**
- Create: `internal/driver/mp_abort.go`, `internal/driver/mp_abort_test.go`
- Modify: `internal/driver/store.go` (the `Abort` stub goes), `internal/driver/export_test.go`; from Step 6 on: `internal/driver/bucketops.go` (M Task 7's `DeleteBucket` calls `abortMultiparts` at the comment it leaves), `internal/memstore/bucket.go` and `internal/memstore/bucket_test.go` (`DeleteBucket` drops the bucket's uploads)

**Interfaces:**
- Consumes: Task 6's `lockMeta`, `unlockMeta`, `listAllParts`, `partCleanup`, `retireCurrentPart`, `retirePart`, `deleteMeta`, `metaDelete`; Task 3's `metaRef`, `readMeta`; W's `enqueueGC`; `op.ErrNoSuchUpload`, `op.FromRADOS`, `op.ScopeUpload`; `radosclient.ErrNotFound`, `ErrCanceled`; for Steps 6-8: M Task 8's `Store.ListObjects` with Task 5's `NameFilter`, M Task 7's `Store.DeleteBucket`, `meta.NSMultipart`, Task 2's `meta.IsMultipartMeta` and `meta.ParseMultipartMeta`, M's `meta.ParseIndexKeyName`, `op.ObjectEntry`.
- Produces:

```go
package driver

// Abort is RGWAbortMultipart::execute (rgw_op.cc:6614-6650) over
// RadosMultipartUpload::abort (rgw_sal_rados.cc:3151-3263): the
// RGWCompleteMultipart lock (ENOENT → ErrNoSuchUpload; EBUSY → 503 through
// FromRADOS), then up to 15 rounds of: read the meta object (its version),
// list every part, queue every part's stripes — the head included — and its
// history under the upload-id tag, collect their index entries, and delete
// the meta object with cls_version_check and abortmp's accounting (the parts'
// total accounted size); ECANCELED (a part landed) restarts the round. The
// unlock radosgw sends afterwards hits a removed object and is ignored.
func (s *Store) Abort(ctx context.Context, up *op.Upload) error

// abortMultiparts is RadosBucket::abort_multiparts (rgw_sal_rados.cc:958-1013
// at v19.2.6, :978-1033 at v20.2.4), which RadosBucket::remove runs under
// own_bucket after the emptiness check (:395-400; v20.2.4 :413-418): the
// multipart-namespace listing filtered to meta objects, in pages of 1000 with
// no prefix or delimiter, each upload aborted through Abort. ENOENT and
// NoSuchUpload are logged and skipped; any other error is returned and the
// bucket delete fails with it. aborted is radosgw's num_deleted, which counts
// the skipped uploads too. DeleteBucket calls it.
func (s *Store) abortMultiparts(ctx context.Context, rec *op.BucketRecord) (aborted int, err error)
```

**P-D16** A part without a manifest (a pre-Hammer upload; radosgw's own branch deletes an empty-named object, [rgw_sal_rados.cc:3186-3192](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L3186-L3192) at v19.2.6, because `list_parts` never fills `RadosMultipartPart::oid`) is logged at warning and skipped; its index entry is still retired with the meta object.

- [ ] **Step 1: Write the failing specs**

`internal/driver/mp_abort_test.go`, on Task 6's fixture (in "aborts an upload with no parts", adjust the `LastWrite` index if the fake records the failed unlock):

```go
var _ = Describe("Abort", func() {
	It("queues every part object under the upload id, retires their entries and removes the meta object", func(ctx SpecContext) {
		up, _ := uploadWithParts(ctx, key, 5<<20, 3<<20)
		s.SetRandForTest(fixedRand("RANDOMRANDOMRANDOMRANDOMRANDOM12XYZ"))
		_, err := s.PutPart(ctx, up, 1, bytes.NewReader(bytes.Repeat([]byte("b"), 5<<20)), op.PutParams{Attrs: partAttrs(), Size: 5 << 20}) // re-upload → past prefix
		Expect(err).NotTo(HaveOccurred())
		metaOID := metaOID(rec, "k", up.ID)
		metaSize := uint64(len(c.Object(extraPoolName, extraNS, metaOID).Data))
		c.ResetCounters()
		Expect(s.Abort(ctx, up)).To(Succeed())

		Expect(c.Object(extraPoolName, extraNS, metaOID)).To(BeNil())
		writes := c.WritesTo(extraPoolName, extraNS, metaOID)
		Expect(writes[0].Steps()[0]).To(BeAssignableToTypeOf(&radosclient.AssertExistsStep{}), "the lock first")
		Expect(writes[0].Steps()[1].(*radosclient.ExecStep).Method).To(Equal("lock"))
		del := writes[1]
		Expect(del.Flags()).To(Equal(radosclient.OpFlagFullTry))
		Expect(del.Steps()[0].(*radosclient.ExecStep).Method).To(Equal("obj_remove"))
		Expect(del.Steps()[1].(*radosclient.ExecStep).Method).To(Equal("check"))
		Expect(execMethods(writes[2:])).To(Equal([]string{"unlock"}), "RGWAbortMultipart::execute unlocks afterwards; ENOENT is ignored")

		shard := shardOf(rec, "k")
		for _, name := range []string{"_multipart_k." + up.ID + ".1", "_multipart_k.RANDOMRANDOMRANDOMRANDOMRANDOM12.1", "_multipart_k." + up.ID + ".2", "_multipart_k." + up.ID + ".meta"} {
			Eventually(func() bool { _, ok := c.Entry(indexPool, "", shard, name); return ok }).Should(BeFalse(), name)
		}
		comp := rgwcls.DecodeCompleteOp(denc.NewDecoder(lastExecIn(c.WritesTo(indexPool, "", shard), "bucket_complete_op", rgwcls.OpDel)))
		Expect(comp.RemoveObjs).To(HaveLen(3), "the current two parts' heads and the old part 1's")
		hdr := c.Header(indexPool, "", shard)
		Expect(hdr.Stats[rgwcls.CategoryMain].NumEntries).To(BeZero())
		Expect(hdr.Stats[rgwcls.CategoryMultiMeta].NumEntries).To(BeZero())

		entries := c.GCEntries(gcPoolName, gcNS, fmt.Sprintf("gc.%d", driver.GCShardForTest(s, up.ID)))
		Expect(entries).To(HaveLen(1))
		Expect(entries[0].Tag).To(Equal(up.ID))
		Expect(chainOIDs(entries[0].Chain)).To(ConsistOf(
			rec.Info.Bucket.Marker+"__multipart_k.RANDOMRANDOMRANDOMRANDOMRANDOM12.1", rec.Info.Bucket.Marker+"__shadow_k.RANDOMRANDOMRANDOMRANDOMRANDOM12.1_1",
			rec.Info.Bucket.Marker+"__multipart_k."+up.ID+".2",
			rec.Info.Bucket.Marker+"__multipart_k."+up.ID+".1", rec.Info.Bucket.Marker+"__shadow_k."+up.ID+".1_1",
		), "current parts' heads and stripes plus the past prefix's")
		for _, o := range chainOIDs(entries[0].Chain) {
			Expect(c.Object(dataPool, "", o)).NotTo(BeNil(), "%s waits for the GC worker", o)
		}
		Expect(memStatsAdjustments()).To(ContainElement(adjustment{objs: -1, bytes: 0, removed: 8 << 20}), "abortmp: parts_accounted_size, not the meta object's size")
		_ = metaSize
	})
	It("answers NoSuchUpload for an unknown id and 503 while a completion holds the lock", func(ctx SpecContext) {
		Expect(s.Abort(ctx, &op.Upload{ID: "2~nope", Bucket: rec, Key: key})).To(MatchError(op.ErrNoSuchUpload))
		up, _ := uploadWithParts(ctx, key, 5<<20)
		holdLock(c, metaOID(rec, "k", up.ID), lock.EntityName{Type: 8, Num: 999})
		Expect(s.Abort(ctx, up)).To(MatchError(op.ErrServiceUnavailable), "EBUSY, rgw_common.cc:134")
		Expect(c.Object(extraPoolName, extraNS, metaOID(rec, "k", up.ID))).NotTo(BeNil())
	})
	It("restarts the round when a part lands during the abort", func(ctx SpecContext) {
		up, _ := uploadWithParts(ctx, key, 5<<20)
		metaOID := metaOID(rec, "k", up.ID)
		raced := false
		c.BeforeWrite(extraPoolName, extraNS, metaOID, func(o *fakerados.Object) {
			if o != nil && !raced && hasStep(o, "obj_remove") {
				raced = true
				_, err := s.PutPart(ctx, up, 2, bytes.NewReader(bytes.Repeat([]byte("z"), 5<<20)), op.PutParams{Attrs: partAttrs(), Size: 5 << 20})
				Expect(err).NotTo(HaveOccurred())
			}
		})
		Expect(s.Abort(ctx, up)).To(Succeed())
		Expect(c.Object(extraPoolName, extraNS, metaOID)).To(BeNil())
		entries := c.GCEntries(gcPoolName, gcNS, fmt.Sprintf("gc.%d", driver.GCShardForTest(s, up.ID)))
		Expect(chainOIDs(flatten(entries))).To(ContainElement(rec.Info.Bucket.Marker + "__multipart_k." + up.ID + ".2"), "the second round saw part 2")
		_, ok := c.Entry(indexPool, "", shardOf(rec, "k"), "_multipart_k."+up.ID+".2")
		Expect(ok).To(BeFalse())
	})
	It("aborts an upload with no parts", func(ctx SpecContext) {
		up, _ := s.CreateUpload(ctx, rec, key, op.UploadParams{Owner: alice, Placement: meta.PlacementRule{Name: "default-placement"}, Attrs: attrsWithACL(alice)})
		Expect(s.Abort(ctx, up)).To(Succeed())
		Expect(c.Object(extraPoolName, extraNS, metaOID(rec, "k", up.ID))).To(BeNil())
		Expect(c.GCEntries(gcPoolName, gcNS, fmt.Sprintf("gc.%d", driver.GCShardForTest(s, up.ID)))).To(BeEmpty())
		del := c.LastWrite(extraPoolName, extraNS, metaOID(rec, "k", up.ID)) // the delete is the last write before the ignored unlock
		Expect(names(del.Steps())).NotTo(ContainElement("check"), "no version xattr yet: version_for_check is null")
	})
})
```

- [ ] **Step 2: Run the specs to see them fail**

Run: `go test -tags=ceph_preview ./internal/driver/ -ginkgo.focus=Abort`. Expected: compile failure.

- [ ] **Step 3: Implement `mp_abort.go`**

```go
package driver

func (s *Store) Abort(ctx context.Context, up *op.Upload) error {
	rec, key := up.Bucket, up.Key
	ref, err := s.metaRef(ctx, rec, key, up.ID)
	if err != nil {
		return err
	}
	if err := s.lockMeta(ctx, ref); err != nil {
		return op.FromRADOS(err, op.ScopeUpload) // ENOENT → ErrNoSuchUpload (rgw_op.cc:6641-6644); EBUSY → ErrServiceUnavailable
	}
	defer s.unlockMeta(ctx, ref) // serializer->unlock() after abort: ENOENT on the removed object is ignored
	for attempt := 0; attempt < 15; attempt++ {
		ms, err := s.readMeta(ctx, ref)
		if err != nil {
			return err // ErrNoSuchUpload for a vanished object
		}
		parts, err := s.listAllParts(ctx, ref, up.ID)
		if err != nil {
			return err
		}
		var cleanup partCleanup
		var accounted uint64
		for _, p := range parts {
			if len(p.Manifest.Rules) == 0 && len(p.Manifest.Objs) == 0 {
				slog.WarnContext(ctx, "multipart part without a manifest skipped on abort", slog.String("upload", up.ID), slog.Uint64("part", uint64(p.Num)))
			} else {
				if err := s.retireCurrentPart(p, &cleanup); err != nil {
					return err
				}
				if err := s.retirePart(p, &cleanup); err != nil {
					return err
				}
			}
			accounted += p.AccountedSize
		}
		if len(cleanup.chain) > 0 {
			s.enqueueGC(ctx, cleanup.chain, up.ID) // send_chain_to_gc(chain, upload_id), synchronous; failures delete inline
		}
		err = s.deleteMeta(ctx, metaDelete{rec: rec, key: key, ref: ref, version: ms.version, mtime: ms.mtime, removeObjs: cleanup.removeObjs, accounted: accounted})
		if errors.Is(err, radosclient.ErrCanceled) && attempt < 14 {
			slog.DebugContext(ctx, "multipart meta delete cancelled by a racing part; retrying", slog.String("upload", up.ID))
			continue
		}
		return err
	}
	return nil
}
```

(`deleteMeta` returns `ErrRequestTimedOut` for ETIMEDOUT and `FromRADOS` errors otherwise; the fifteenth ECANCELED is returned as `op.FromRADOS(ErrCanceled)` → `op.ErrConcurrentModification`, 409, radosgw's table row for ECANCELED ([rgw_common.cc:141](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L141)); the abort spec above does not exercise it.) Remove the stub from `store.go`.

- [ ] **Step 4: Run the specs**

Run: `go test -tags=ceph_preview ./internal/driver/`. Expected: PASS.

- [ ] **Step 5: Check and commit**

```bash
make check
git add internal/driver
git commit -m "feat(driver): AbortMultipartUpload

Abort takes the completion lock, queues every part's head, stripes and
re-upload history for GC under the upload id, retires their index
entries in the meta object's complete_del, and removes the meta
object behind cls_version_check, restarting when a part lands
meanwhile; the quota cache drops the parts' accounted total as
abortmp does.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

- [ ] **Step 6: Write the failing `abortMultiparts` specs**

`RadosBucket::remove` ([rgw_sal_rados.cc:350-468](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L350-L468) at v19.2.6; [:367-489](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L367-L489) at v20.2.4) lists the empty namespace to refuse a non-empty bucket, then, when the zone owns the bucket, calls `abort_multiparts` ([:395-400](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L395-L400); v20.2.4 [:413-418](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L413-L418)): every in-flight upload of a bucket being deleted is aborted, so a bucket whose only contents are in-flight uploads is deleted cleanly. Without it rgw-go's `DeleteBucket` (M Task 7; its `checkBucketEmpty` counts the empty namespace only, as `list` with `params.ns` empty does) would drop the index shards and orphan the `.meta` objects and part heads in the extra and data pools with no GC entry. `abort_multiparts` ([:958-1013](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L958-L1013); v20.2.4 [:978-1033](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L978-L1033)) pages `list_multiparts` ([:915-956](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L915-L956)) — the ordered listing in `RGW_OBJ_NS_MULTIPART` with `MultipartMetaFilter`, empty prefix and delimiter, `max = 1000`, the marker advanced to `params.marker.name` (the namespace-local name of the last entry the listing considered) — and calls `abort` on each; `-ENOENT` and `-ERR_NO_SUCH_UPLOAD` are logged and skipped, any other error is returned and `remove` fails with it before touching the entry point; `num_deleted` counts the skipped ones too and is logged at level 0 when non-zero. M's `DeleteBucket` leaves a comment inside its `own` branch, after `checkBucketEmpty`, where Step 7 inserts the call. No rgw-go-created upload can exist before this task lands; the pre-P gap is a radosgw-created upload on a shared zone, which M's plan states in Task 7.

M Task 7's spec "does not count a multipart-namespace entry as content" seeds `_multipart_k.2~abc.meta` with no meta object; it stays green after Step 7 because `Abort` answers `ErrNoSuchUpload` for the missing object and `abortMultiparts` skips it, as `abort_multiparts` skips `-ERR_NO_SUCH_UPLOAD`.

`internal/driver/mp_abort_test.go` gains, on Task 6's fixture (`rec` is owned by this zonegroup; `seedIndexEntry`, `entry` and `shardOf` are M Task 8's `list_test.go` helpers in the same `driver_test` package; `metaPool` is the root pool's name):

```go
var _ = Describe("DeleteBucket aborts in-flight uploads", func() {
	It("aborts every upload in pages of 1000 before removing the bucket, as RadosBucket::remove does", func(ctx SpecContext) {
		up1, _ := uploadWithParts(ctx, key, 5<<20)
		up2, err := s.CreateUpload(ctx, rec, meta.ObjKey{Name: "other"}, op.UploadParams{Owner: alice, Placement: meta.PlacementRule{Name: "default-placement"}, Attrs: attrsWithACL(alice)})
		Expect(err).NotTo(HaveOccurred())
		for i := range 1001 { // meta entries without objects: each abort is a tolerated NoSuchUpload; 1001 > 1000 forces a second page
			name := fmt.Sprintf("_multipart_ghost%04d.2~x.meta", i)
			seedIndexEntry(c, indexPool, fmt.Sprintf(".dir.%s.%d", rec.Info.Bucket.ID, shardOf(rec, "ghost"+fmt.Sprintf("%04d", i))), entry(name, 0))
		}
		n, err := driver.AbortMultipartsForTest(s, ctx, rec)
		Expect(err).NotTo(HaveOccurred())
		Expect(n).To(Equal(1003), "num_deleted counts the two real uploads and the 1001 skipped ones")
		Expect(c.Object(extraPoolName, extraNS, metaOID(rec, "k", up1.ID))).To(BeNil())
		Expect(c.Object(extraPoolName, extraNS, metaOID(rec, "other", up2.ID))).To(BeNil())
		Expect(c.GCEntries(gcPoolName, gcNS, fmt.Sprintf("gc.%d", driver.GCShardForTest(s, up1.ID)))).To(HaveLen(1), "the part's stripes wait for the GC worker")

		Expect(s.DeleteBucket(ctx, rec)).To(Succeed(), "the second call finds nothing to abort")
		Expect(c.Objects(indexPool, "")).To(BeEmpty(), "clean_index ran after the aborts")
		Expect(c.Object(metaPool, "root", ".bucket.meta."+rec.Info.Bucket.Name+":"+rec.Info.Bucket.ID)).To(BeNil())
	})
	It("aborts through DeleteBucket, skips a meta entry whose object is gone, and fails the delete on any other abort error", func(ctx SpecContext) {
		seedIndexEntry(c, indexPool, fmt.Sprintf(".dir.%s.%d", rec.Info.Bucket.ID, shardOf(rec, "ghost")), entry("_multipart_ghost.2~x.meta", 0))
		up, _ := uploadWithParts(ctx, key, 5<<20)
		holdLock(c, metaOID(rec, "k", up.ID), lock.EntityName{Type: 8, Num: 999})
		Expect(s.DeleteBucket(ctx, rec)).To(MatchError(op.ErrServiceUnavailable), "abort_multiparts returns the abort's error and remove stops there")
		Expect(c.Object(metaPool, "root", rec.Info.Bucket.Name)).NotTo(BeNil(), "the entry point survives")
		Expect(c.Object(extraPoolName, extraNS, metaOID(rec, "k", up.ID))).NotTo(BeNil())
		releaseLock(c, metaOID(rec, "k", up.ID))
		Expect(s.DeleteBucket(ctx, rec)).To(Succeed(), "the ghost entry is skipped, the real upload aborted")
		Expect(c.Object(extraPoolName, extraNS, metaOID(rec, "k", up.ID))).To(BeNil())
		Expect(c.Object(metaPool, "root", rec.Info.Bucket.Name)).To(BeNil())
	})
	It("does not list or abort anything for a bucket another zonegroup owns", func(ctx SpecContext) {
		up, _ := uploadWithParts(ctx, key, 5<<20)
		foreign := *rec
		foreign.Info.Zonegroup = "elsewhere"
		c.ResetCounters()
		Expect(s.DeleteBucket(ctx, &foreign)).To(Succeed())
		for i := range 11 {
			Expect(c.Reads(indexPool, "", fmt.Sprintf(".dir.%s.%d", rec.Info.Bucket.ID, i))).To(BeZero(), "results.is_truncated = own_bucket: no listing at all")
		}
		Expect(c.Object(extraPoolName, extraNS, metaOID(rec, "k", up.ID))).NotTo(BeNil(), "abort_multiparts runs only under own_bucket")
	})
})
```

`releaseLock(c, oid)` is the inverse of Task 6's `holdLock`; `fakerados` gains it if Task 6 did not. `export_test.go` gains `func AbortMultipartsForTest(s *Store, ctx context.Context, rec *op.BucketRecord) (int, error)`.

`internal/memstore/bucket_test.go` gains one `It`: after `CreateUpload` on a bucket, `DeleteBucket` succeeds and `ListUploads` on a bucket re-created under the same name is empty (`memstore` has no shared pools, so dropping the uploads is the whole of the radosgw behaviour it can model).

Run: `go test -tags=ceph_preview ./internal/driver/ -ginkgo.focus="DeleteBucket aborts"`. Expected: compile failure (`abortMultiparts`, `AbortMultipartsForTest` undefined).

- [ ] **Step 7: Implement `abortMultiparts`, the `DeleteBucket` call and the memstore drop**

`internal/driver/mp_abort.go`:

```go
func (s *Store) abortMultiparts(ctx context.Context, rec *op.BucketRecord) (aborted int, err error) {
	var marker string
	for {
		res, err := s.ListObjects(ctx, rec, op.ListObjectsParams{NS: meta.NSMultipart, NameFilter: meta.IsMultipartMeta, Marker: marker, MaxKeys: 1000})
		if err != nil {
			return aborted, err // list_bucket_multiparts failed: ret returned, remove fails
		}
		for _, e := range res.Entries {
			key, id, ok := meta.ParseMultipartMeta(e.Key.Name)
			if !ok {
				continue
			}
			up := &op.Upload{ID: id, Bucket: rec, Key: meta.ObjKey{Name: key}, Owner: e.Owner, OwnerName: e.OwnerDisplayName, Initiated: e.Mtime} // get_multipart_upload(key.name, nullopt, owner, mtime)
			switch err := s.Abort(ctx, up); {
			case err == nil:
			case errors.Is(err, op.ErrNoSuchUpload): // -ENOENT and -ERR_NO_SUCH_UPLOAD: "unable to find part(s) of aborted multipart upload", keep going
				slog.InfoContext(ctx, "multipart upload vanished before the bucket delete aborted it", slog.String("bucket", rec.Info.Bucket.Name), slog.String("meta", e.Key.Name))
			default:
				return aborted, err
			}
			aborted++ // num_deleted++ runs on the skipped ones too
		}
		if !res.Truncated {
			return aborted, nil
		}
		// marker = params.marker.name: NextMarker is the index key name of the
		// last entry considered (listObjectsOrdered); Marker takes the
		// namespace-local name, so strip the namespace here.
		k, _ := meta.ParseIndexKeyName(res.NextMarker)
		marker = k.Name
	}
}
```

The listing's `Marker` is the namespace-local name because M's `ListObjects` re-applies `p.NS` to it (`meta.ObjKey{Name, NS}.IndexKeyName()`); feeding `NextMarker` back unparsed would double the `_multipart_` prefix. If M's `NextMarker` turns out to be the namespace-local name already when this task is implemented, drop the parse and say so in the commit.

`internal/driver/bucketops.go` (M Task 7), at the comment inside the `own` branch:

```go
		if err := s.checkBucketEmpty(ctx, rec); err != nil {
			return err
		}
		n, err := s.abortMultiparts(ctx, rec) // abort_multiparts, rgw_sal_rados.cc:395-400 (v20.2.4 :413-418)
		if err != nil {
			return err
		}
		if n > 0 {
			slog.WarnContext(ctx, "aborted incomplete multipart uploads on bucket delete", slog.String("bucket", rec.Info.Bucket.Name), slog.Int("count", n)) // ldpp_dout(dpp, 0) WARNING
		}
```

`internal/memstore/bucket.go`: `DeleteBucket` deletes every upload keyed under the bucket after its emptiness check. `export_test.go`: `AbortMultipartsForTest` calls `s.abortMultiparts`.

- [ ] **Step 8: Run the specs, check, commit**

Run: `go test -tags=ceph_preview ./internal/driver/ ./internal/memstore/`. Expected: PASS, M Task 7's `DeleteBucket` specs included.

```bash
make check
git add internal/driver internal/memstore
git commit -m "feat(driver): abort in-flight multipart uploads on bucket delete

RadosBucket::remove aborts every upload of a bucket it owns after the
emptiness check; DeleteBucket now pages the multipart meta listing by
1000 and aborts each through Abort, skipping a vanished upload and
failing the delete on any other error, so the meta objects and part
heads are queued for GC instead of being orphaned when the index goes.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 8: `op`: the six multipart ops (UploadPart covers the copy)

**Files:**
- Create: `internal/op/initmultipart.go`, `internal/op/uploadpart.go`, `internal/op/completemultipart.go`, `internal/op/abortmultipart.go`, `internal/op/listparts.go`, `internal/op/listuploads.go`, `internal/op/multipartxml.go` (the completion document parser), and `_test.go` for each on `memstore`
- Modify: `internal/op/errors.go` only if `ErrServiceUnavailable`'s message needs no change (it does not); nothing in G's frozen types

**Interfaces:**
- Consumes: G's `op.Op`, `Run`, `Request`, `Env`, `VerifyBucketPermission`, `VerifyObjectPermission`, `VerifyObjectPermissionIn`, `MultipartStore`, `ObjectStore.PrefetchObject` (R), `StatsStore.CheckQuota`, `ZoneInfo.Placement`, `Error.WithMessage`, `AsError`, the sentinels; Z's `acl.PermFor`, `acl.Policy`, `acl.DecodePolicy`, `tags.Set`, `tags.Attr`, `policy.S3PutObject`, `S3GetObject`, `S3GetObjectVersion`, `S3AbortMultipartUpload`, `S3ListMultipartUploadParts`, `S3ListBucketMultipartUploads`; M's `LogUsage`; R's `Unquote`; W's `PutObject` shape (Task 10) as the pattern, `meta.PlacementRule.InheritFrom`, `meta.TierTypeCloudS3` (R named it); A's body contract (`r.Body`, `r.ContentLength`); `cephconf.Options` (`rgw_max_put_size`, `rgw_multipart_part_upload_limit`, `rgw_max_listing_results`).
- Produces:

```go
package op

// InitMultipart is RGWInitMultipart (rgw_op.cc:6271-6343; rgw_rest_s3.cc:3969-4062).
type InitMultipart struct {
	Attrs        map[string][]byte // the handler's requestAttrs, NUL-terminated
	ACL          acl.Policy        // create_s3_policy
	Tags         *tags.Set         // x-amz-tagging, nil when absent
	StorageClass string            // x-amz-storage-class, "" when absent

	UploadID string
	Upload   *Upload
}

// UploadPart is RGWPutObj with uploadId (rgw_op.cc:3806-3918, :4142-4560;
// rgw_rest_s3.cc:2597-2704), UploadPartCopy included: a CopySource request
// streams [Range] of the source through CopyPart.
type UploadPart struct {
	UploadID   string
	PartNumber int
	Body       io.Reader         // r.Body: auth's verifying reader; nil for a copy
	Size       int64             // r.ContentLength; -1 for chunked
	Attrs      map[string][]byte // requestAttrs
	ACL        acl.Policy
	ContentMD5 []byte // decoded Content-MD5, nil when absent (not read for a copy)

	CopySource           bool
	SrcTenant, SrcBucket string
	SrcKey               meta.ObjKey
	Range                string // raw x-amz-copy-source-range, "" when absent

	ETag  string
	Mtime time.Time

	srcRec *BucketRecord
	src    *ObjectState
	rng    ByteRange
}

// CompleteMultipart is RGWCompleteMultipart (rgw_op.cc:6345-6593;
// rgw_rest.cc:1580-1595; rgw_rest_s3.cc:4064-4108).
type CompleteMultipart struct {
	UploadID             string
	Body                 []byte // the document, at most rgw_max_put_param_size bytes (the handler enforced it)
	IfMatch, IfNoneMatch string // Tentacle only; the handler leaves them empty on Squid, whose radosgw ignores them

	Parts []CompletePart // parsed by Execute: sorted by number, last duplicate wins, as radosgw's std::map<int, string>
	ETag  string
	Size  uint64
	Mtime time.Time
}

// AbortMultipart is RGWAbortMultipart (rgw_op.cc:6595-6650).
type AbortMultipart struct{ UploadID string }

// ListParts is RGWListMultipart (rgw_op.cc:6652-6694; rgw_rest.cc:1597-1622).
type ListParts struct {
	UploadID string
	Marker   int // part-number-marker, 0 when absent
	MaxParts int // max-parts, 1000 when absent, clamped to [0, rgw_max_listing_results]

	Upload       *Upload    // Init: the meta object's owner, placement and attrs
	Owner        acl.Owner  // the meta object's ACL owner (ListPartsResult's Owner element)
	StorageClass string     // the upload's storage class, "STANDARD" when empty
	Result       ListPartsResult
}

// ListMultipartUploads is RGWListBucketMultiparts (rgw_op.cc:6696-6750; rgw_rest.cc:1624-1660).
type ListMultipartUploads struct {
	Prefix, Delimiter         string
	KeyMarker, UploadIDMarker string
	MaxUploads                int  // max-uploads, 1000 when absent, clamped to [0, rgw_max_listing_results]
	EncodingURL               bool // encoding-type=url

	Result ListUploadsResult
}

// ParseCompleteMultipart is RGWMultiXMLParser + RGWMultiCompleteUpload::xml_end
// (rgw_multi.cc:24-71): the root may be CompleteMultipartUpload,
// CompletedMultipartUpload or MultipartUpload; each Part needs PartNumber
// (atoi: a leading integer, 0 when none) and ETag (raw text, quotes kept); a
// Part missing either, an unparsable document, a wrong root or no Part at
// all is ErrMalformedXML. Parts come back sorted by number with the last
// duplicate kept, as std::map<int, std::string> holds them.
func ParseCompleteMultipart(doc []byte) ([]CompletePart, error)

// ParseCopySourceRange is RGWPutObj::init_processing's x-amz-copy-source-range
// grammar (rgw_op.cc:3866-3893): "bytes=<digits>-<digits>", both present and
// numeric, else ErrInvalidArgument; first > last is ErrInvalidRange (-ERANGE).
func ParseCopySourceRange(v string) (first, last uint64, err error)
```

Semantics, transcribed:

- **InitMultipart.** `Name` `init_multipart`; `Action` `S3PutObject`; `OpMask` `OpTypeWrite`. `Init`: `r.BucketRec = Env.Buckets.GetBucket(ctx, r.Tenant, r.Bucket)`; the destination placement `meta.PlacementRule{StorageClass: o.StorageClass}.InheritFrom(rec.Info.PlacementRule)` must resolve through `Env.Zone.Placement`, else `ErrInvalidArgument` (RGWOp::init_processing's dest_placement check, [rgw_op.cc:576-583](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L576-L583), shared with PUT). `VerifyPermission`: `VerifyBucketPermission(ctx, r, S3PutObject, acl.PermFor(S3PutObject))` ([:6280-6283](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L6280-L6283)). `Execute` ([:6293-6343](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L6293-L6343)): `r.Object.Name == ""` → `ErrInvalidArgument`; attrs = clone(`Attrs`) + `user.rgw.acl` = `ACL.Encode` at the release + `tags.Attr` = `Tags.Encode` when set; `up, err := Env.Multipart.CreateUpload(ctx, rec, r.Object, UploadParams{Owner: r.Identity.Owner, OwnerName: r.Identity.User.DisplayName, Placement: dest, Attrs: attrs})`; `UploadID, Upload = up.ID, up`. `Complete`: `LogUsage(ctx, r, "init_multipart")`.
- **UploadPart.** `Name` `put_obj` (radosgw's op name and usage category for both forms); `Action` `S3PutObject`; `OpMask` `OpTypeWrite`. `Init` (:3806-3918): the bucket; `!CopySource && Size > rgw_max_put_size` → `ErrEntityTooLarge` (`verify_params`, [rgw_rest.cc:1049-1059](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L1049-L1059)); when `CopySource`: `srcRec = Env.Buckets.GetBucket(ctx, SrcTenant, SrcBucket)` (`ErrNoSuchBucket`), `first, last, err := ParseCopySourceRange(Range)` when `Range != ""` (its errors as they are), `src = Env.Objects.PrefetchObject(ctx, srcRec, SrcKey)` (`cs_object->set_prefetch_data()`, [:3929](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L3929)). `VerifyPermission` ([:3920-3989](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L3920-L3989)): when `CopySource`, `VerifyObjectPermissionIn(ctx, r, S3GetObject or S3GetObjectVersion by SrcKey.Instance, acl.PermFor(action), srcRec, src)` (P-D8) — its `ErrNoSuchKey` for a missing source is the 404 (D-Z5; radosgw's `read_obj_policy` ENOENT); then `VerifyBucketPermission(ctx, r, S3PutObject, acl.PermFor(S3PutObject))`. `Execute` ([:4142-4560](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L4142-L4560)): `r.Object.Name == ""` → `ErrInvalidArgument`; `!CopySource && Size >= 0 && !r.Identity.System` → `Env.Stats.CheckQuota(ctx, rec, rec.Info.Owner, Size, 1)` ([:4200-4207](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L4200-L4207); the driver runs the second check); attrs = clone(`Attrs`) + `user.rgw.acl`; `up := &Upload{ID: UploadID, Bucket: rec, Key: r.Object, Attrs: attrs}`; for a copy: `src.Manifest != nil && src.Manifest.TierType == meta.TierTypeCloudS3` → `ErrInvalidObjectState.WithMessage("This object was transitioned to cloud-s3")` ([:4297-4303](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L4297-L4303)); `!src.Exists` → `ErrNoSuchKey` ([:4315-4318](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L4315-L4318)); `size := src.Size` or `src.Compression.OrigSize` when compressed (`get_obj_size` is the accounted size); no `Range` → `rng = {0, size}`; with a range: `first >= size && size > 0` → `ErrInvalidRange` (`range_to_ofs`, [rgw_rados.cc:7002-7022](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L7002-L7022)), `last = min(last, size-1)`, `rng = {first, last-first+1}`; an empty source with any range copies nothing (`fst > lst` ends the loop); `res, err := Env.Multipart.CopyPart(ctx, up, PartNumber, src, rng)`; otherwise `res, err := Env.Multipart.PutPart(ctx, up, PartNumber, Body, PutParams{Attrs: attrs, Size: Size, ContentMD5: ContentMD5, Tag: r.ID})`; `ETag, Mtime = res.ETag, res.Mtime`. `Complete`: `LogUsage(ctx, r, "put_obj")`.
- **CompleteMultipart.** `Name` `complete_multipart`; `Action` `S3PutObject`; `OpMask` `OpTypeWrite`. `Init`: the bucket; `UploadID == ""` → `ErrUnknown` (`-ENOTSUP`, no S3 row in [rgw_common.cc:51-144](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L51-L144): 500 UnknownError). `VerifyPermission`: `VerifyBucketPermission(ctx, r, S3PutObject, acl.PermFor(S3PutObject))` (:6354-6357). `Execute` ([:6367-6540](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6367-L6540)): `len(Body) == 0` → `ErrMalformedXML`; `Parts, err = ParseCompleteMultipart(Body)`; `len(Parts) > rgw_multipart_part_upload_limit` → `ErrInvalidRange` (`-ERANGE`, [:6408-6412](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L6408-L6412); counted AFTER the map collapsed duplicates); `res, err := Env.Multipart.Complete(ctx, &Upload{ID: UploadID, Bucket: rec, Key: r.Object, WriteTag: r.ID, IfMatch: IfMatch, IfNoneMatch: IfNoneMatch}, Parts)`; `ETag, Size, Mtime = res.ETag, res.Size, res.Mtime`. `Complete`: `LogUsage(ctx, r, "complete_multipart")`.
- **AbortMultipart.** `Name` `abort_multipart`; `Action` `S3AbortMultipartUpload`; `OpMask` `OpTypeDelete`. `Init`: the bucket. `VerifyPermission`: `VerifyBucketPermission(ctx, r, S3AbortMultipartUpload, acl.PermFor(S3AbortMultipartUpload))` (:6601-6604). `Execute` ([:6614-6650](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L6614-L6650)): `UploadID == "" || r.Object.Name == ""` → `ErrInvalidArgument`; `Env.Multipart.Abort(ctx, &Upload{ID: UploadID, Bucket: rec, Key: r.Object})`. `Complete`: `LogUsage(ctx, r, "abort_multipart")`.
- **ListParts.** `Name` `list_multipart`; `Action` `S3ListMultipartUploadParts`; `OpMask` `OpTypeRead`. `Init`: the bucket; `UploadID == ""` → `ErrUnknown` (`-ENOTSUP`); `Upload, err = Env.Multipart.GetUpload(ctx, rec, r.Object, UploadID)` (`ErrNoSuchUpload`); `r.ObjState = &ObjectState{Bucket: rec, Key: r.Object, Exists: true, Attrs: Upload.Attrs}` — the meta object's attrs are the object policy input (`read_obj_policy` with `uploadId`, [rgw_op.cc:398-418](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L398-L418); Z's rule). `VerifyPermission`: `VerifyObjectPermission(ctx, r, S3ListMultipartUploadParts, acl.PermFor(S3ListMultipartUploadParts))` ([:6658-6659](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L6658-L6659)). `Execute`: `Owner` = `acl.DecodePolicy(Upload.Attrs[meta.AttrACL]).Owner` (an undecodable ACL is `ErrInternalError`, [:6683-6688](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L6683-L6688) `-EIO`); `StorageClass = Upload.Placement.CanonicalStorageClass()` (`rgw_placement_rule::get_storage_class` returns STANDARD for an empty class); `Result, err = Env.Multipart.ListParts(ctx, Upload, Marker, MaxParts)`. `Complete`: `LogUsage(ctx, r, "list_multipart")`.
- **ListMultipartUploads.** `Name` `list_bucket_multiparts`; `Action` `S3ListBucketMultipartUploads`; `OpMask` `OpTypeRead`. `Init`: the bucket. `VerifyPermission`: `VerifyBucketPermission(ctx, r, S3ListBucketMultipartUploads, acl.PermFor(...))` (:6702-6707). `Execute` (:6715-6750): `Result, err = Env.Multipart.ListUploads(ctx, rec, ListUploadsParams{Prefix, Delimiter, KeyMarker, UploadIDMarker, MaxUploads})`. `Complete`: `LogUsage(ctx, r, "list_bucket_multiparts")`.

- [ ] **Step 1: Write the failing parser specs**

`internal/op/multipartxml_test.go`:

```go
var _ = Describe("ParseCompleteMultipart", func() {
	It("reads the parts of any of the three roots, sorted, last duplicate winning", func() {
		doc := `<CompleteMultipartUpload xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Part><PartNumber>2</PartNumber><ETag>"b"</ETag></Part><Part><ETag>"a"</ETag><PartNumber>1</PartNumber></Part><Part><PartNumber>1</PartNumber><ETag>"a2"</ETag></Part></CompleteMultipartUpload>`
		parts, err := op.ParseCompleteMultipart([]byte(doc))
		Expect(err).NotTo(HaveOccurred())
		Expect(parts).To(Equal([]op.CompletePart{{Number: 1, ETag: `"a2"`}, {Number: 2, ETag: `"b"`}}))
		for _, root := range []string{"CompletedMultipartUpload", "MultipartUpload"} {
			_, err := op.ParseCompleteMultipart([]byte("<" + root + "><Part><PartNumber>1</PartNumber><ETag>x</ETag></Part></" + root + ">"))
			Expect(err).NotTo(HaveOccurred(), root)
		}
	})
	DescribeTable("is MalformedXML for",
		func(doc string) {
			_, err := op.ParseCompleteMultipart([]byte(doc))
			Expect(err).To(MatchError(op.ErrMalformedXML))
		},
		Entry("not XML", "hello"),
		Entry("another root", "<Delete><Part><PartNumber>1</PartNumber><ETag>x</ETag></Part></Delete>"),
		Entry("no parts", "<CompleteMultipartUpload></CompleteMultipartUpload>"),
		Entry("a part without an ETag", "<CompleteMultipartUpload><Part><PartNumber>1</PartNumber></Part></CompleteMultipartUpload>"),
		Entry("a part without a number", "<CompleteMultipartUpload><Part><ETag>x</ETag></Part></CompleteMultipartUpload>"),
		Entry("an empty number", "<CompleteMultipartUpload><Part><PartNumber></PartNumber><ETag>x</ETag></Part></CompleteMultipartUpload>"),
	)
	It("parses a part number as atoi does", func() {
		parts, err := op.ParseCompleteMultipart([]byte("<CompleteMultipartUpload><Part><PartNumber>12abc</PartNumber><ETag>x</ETag></Part><Part><PartNumber>zzz</PartNumber><ETag>y</ETag></Part></CompleteMultipartUpload>"))
		Expect(err).NotTo(HaveOccurred())
		Expect(parts).To(Equal([]op.CompletePart{{Number: 0, ETag: "y"}, {Number: 12, ETag: "x"}}))
	})
})

var _ = Describe("ParseCopySourceRange", func() {
	It("accepts bytes=first-last and refuses the rest", func() {
		f, l, err := op.ParseCopySourceRange("bytes=5-10")
		Expect(err).NotTo(HaveOccurred())
		Expect([]uint64{f, l}).To(Equal([]uint64{5, 10}))
		for _, bad := range []string{"5-10", "bytes=5", "bytes=-10", "bytes=5-", "bytes=a-b", " bytes=5-10"} {
			_, _, err := op.ParseCopySourceRange(bad)
			Expect(err).To(MatchError(op.ErrInvalidArgument), bad)
		}
		_, _, err = op.ParseCopySourceRange("bytes=10-5")
		Expect(err).To(MatchError(op.ErrInvalidRange), "-ERANGE")
	})
})
```

- [ ] **Step 2: Write the failing op specs on `memstore`**

`internal/op/uploadpart_test.go` and siblings, on G Task 4's memstore fixture (`env`, `alice`, a bucket `plain`, `request(...)` building an `op.Request` with `Identity`), following W Task 10's pattern:

```go
var _ = Describe("the multipart ops", func() {
	It("runs a whole upload through the six ops", func(ctx SpecContext) {
		init := &op.InitMultipart{Attrs: map[string][]byte{meta.AttrContentType: []byte("text/plain\x00")}, ACL: acl.DefaultPolicy(alice, "Alice")}
		r := request("POST", "plain", "k")
		Expect(op.Run(ctx, init, r)).To(Succeed())
		Expect(init.UploadID).To(HavePrefix("2~"))
		Expect(init.UploadID).To(HaveLen(34))

		p1 := bytes.Repeat([]byte("a"), 5<<20)
		up1 := &op.UploadPart{UploadID: init.UploadID, PartNumber: 1, Body: bytes.NewReader(p1), Size: int64(len(p1)), Attrs: map[string][]byte{}, ACL: acl.DefaultPolicy(alice, "Alice")}
		Expect(op.Run(ctx, up1, request("PUT", "plain", "k"))).To(Succeed())
		Expect(up1.ETag).To(Equal(md5hex(p1)))
		Expect(memStore.Usage()).To(ContainElement(HaveField("Category", "put_obj")))

		Expect(env.Objects.PutObject(ctx, rec, meta.ObjKey{Name: "src"}, bytes.NewReader(bytes.Repeat([]byte("s"), 8<<20)), op.PutParams{Attrs: attrsWithACL(alice), Size: 8 << 20})).Error().NotTo(HaveOccurred())
		up2 := &op.UploadPart{UploadID: init.UploadID, PartNumber: 2, CopySource: true, SrcTenant: "", SrcBucket: "plain", SrcKey: meta.ObjKey{Name: "src"}, Range: "bytes=1048576-3145727", Attrs: map[string][]byte{}, ACL: acl.DefaultPolicy(alice, "Alice"), Size: 0}
		Expect(op.Run(ctx, up2, request("PUT", "plain", "k"))).To(Succeed())
		Expect(up2.ETag).To(Equal(md5hex(bytes.Repeat([]byte("s"), 2<<20))))

		lp := &op.ListParts{UploadID: init.UploadID, MaxParts: 1000}
		Expect(op.Run(ctx, lp, request("GET", "plain", "k"))).To(Succeed())
		Expect(lp.Result.Parts).To(HaveLen(2))
		Expect(lp.Result.NextMarker).To(Equal(2))
		Expect(lp.Owner.ID).To(Equal("alice"))
		Expect(lp.StorageClass).To(Equal("STANDARD"))

		lu := &op.ListMultipartUploads{MaxUploads: 1000}
		Expect(op.Run(ctx, lu, request("GET", "plain", ""))).To(Succeed())
		Expect(lu.Result.Uploads).To(HaveLen(1))
		Expect(lu.Result.Uploads[0].ID).To(Equal(init.UploadID))

		doc := fmt.Sprintf(`<CompleteMultipartUpload><Part><PartNumber>2</PartNumber><ETag>"%s"</ETag></Part><Part><PartNumber>1</PartNumber><ETag>%s</ETag></Part></CompleteMultipartUpload>`, up2.ETag, up1.ETag)
		cm := &op.CompleteMultipart{UploadID: init.UploadID, Body: []byte(doc)}
		Expect(op.Run(ctx, cm, request("POST", "plain", "k"))).To(Succeed())
		Expect(cm.ETag).To(HaveSuffix("-2"))
		Expect(cm.Size).To(BeEquivalentTo(7 << 20))
		st, err := env.Objects.StatObject(ctx, rec, meta.ObjKey{Name: "k"})
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Exists).To(BeTrue())
		Expect(st.ETag).To(Equal(cm.ETag))
		Expect(st.Attrs).To(HaveKeyWithValue(meta.AttrContentType, []byte("text/plain\x00")), "the upload's attrs")

		lu = &op.ListMultipartUploads{MaxUploads: 1000}
		Expect(op.Run(ctx, lu, request("GET", "plain", ""))).To(Succeed())
		Expect(lu.Result.Uploads).To(BeEmpty())
	})
	It("aborts", func(ctx SpecContext) {
		init := &op.InitMultipart{ACL: acl.DefaultPolicy(alice, "Alice")}
		Expect(op.Run(ctx, init, request("POST", "plain", "k"))).To(Succeed())
		Expect(op.Run(ctx, &op.AbortMultipart{UploadID: init.UploadID}, request("DELETE", "plain", "k"))).To(Succeed())
		Expect(op.Run(ctx, &op.AbortMultipart{UploadID: init.UploadID}, request("DELETE", "plain", "k"))).To(MatchError(op.ErrNoSuchUpload))
		Expect(op.Run(ctx, &op.AbortMultipart{}, request("DELETE", "plain", "k"))).To(MatchError(op.ErrInvalidArgument))
	})
	DescribeTable("CompleteMultipart refusals",
		func(body string, want error) {
			init := &op.InitMultipart{ACL: acl.DefaultPolicy(alice, "Alice")}
			Expect(op.Run(ctx, init, request("POST", "plain", "k"))).To(Succeed())
			err := op.Run(ctx, &op.CompleteMultipart{UploadID: init.UploadID, Body: []byte(body)}, request("POST", "plain", "k"))
			Expect(err).To(MatchError(want))
		},
		Entry("an empty body", "", op.ErrMalformedXML),
		Entry("no parts", "<CompleteMultipartUpload/>", op.ErrMalformedXML),
		Entry("too many parts", tooManyParts(10001), op.ErrInvalidRange),
		Entry("a part that was not uploaded", `<CompleteMultipartUpload><Part><PartNumber>1</PartNumber><ETag>"x"</ETag></Part></CompleteMultipartUpload>`, op.ErrInvalidPart),
	)
	It("answers 500 UnknownError for an empty uploadId on Complete and ListParts, as radosgw's ENOTSUP does", func(ctx SpecContext) {
		Expect(op.Run(ctx, &op.CompleteMultipart{Body: []byte("x")}, request("POST", "plain", "k"))).To(MatchError(op.ErrUnknown))
		Expect(op.Run(ctx, &op.ListParts{MaxParts: 10}, request("GET", "plain", "k"))).To(MatchError(op.ErrUnknown))
	})
	It("refuses a part larger than rgw_max_put_size before touching the store, and a copy range past the source", func(ctx SpecContext) {
		// conf rgw_max_put_size 8 MiB in the fixture's Env.Conf: Size 9 MiB → ErrEntityTooLarge from Init (no CreateUpload needed);
		// a 4 MiB source and Range "bytes=4194304-5000000" → ErrInvalidRange; "bytes=0-99999999" → succeeds with 4 MiB copied (last clamped)
	})
	It("checks the source's permission with the source as the object and the bucket for the write", func(ctx SpecContext) {
		// an FakeAuthorizer recording calls: UploadPart with CopySource → VerifyObject called with r.Bucket == "srcbucket" and action S3GetObject, then VerifyBucket with the destination and S3PutObject
	})
	It("passes the meta object's attrs as the object policy for ListParts", func(ctx SpecContext) {
		// FakeAuthorizer: the request's ObjState.Attrs at VerifyObject time equal the upload's attrs
	})
})
```

(`tooManyParts(n)` builds a document with n distinct part numbers.) `memstore.Complete` must produce `ErrInvalidPart` for the fourth table entry (it does: the ETag mismatch / missing part).

- [ ] **Step 3: Run the specs to see them fail; implement**

`multipartxml.go`:

```go
package op

type completeXML struct {
	XMLName xml.Name
	Parts   []struct {
		PartNumber *string `xml:"PartNumber"`
		ETag       *string `xml:"ETag"`
	} `xml:"Part"`
}

// atoi is C atoi: optional sign and leading digits, 0 when none.
func atoi(s string) int {
	s = strings.TrimLeft(s, " \t\n\v\f\r")
	end := 0
	if end < len(s) && (s[end] == '+' || s[end] == '-') {
		end++
	}
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	n, err := strconv.Atoi(s[:end])
	if err != nil {
		return 0
	}
	return n
}

func ParseCompleteMultipart(doc []byte) ([]CompletePart, error) {
	var x completeXML
	if err := xml.Unmarshal(doc, &x); err != nil {
		return nil, ErrMalformedXML
	}
	switch x.XMLName.Local {
	case "CompleteMultipartUpload", "CompletedMultipartUpload", "MultipartUpload":
	default:
		return nil, ErrMalformedXML
	}
	byNum := map[int]string{}
	for _, p := range x.Parts {
		if p.PartNumber == nil || p.ETag == nil || *p.PartNumber == "" {
			return nil, ErrMalformedXML // RGWMultiPart::xml_end returns false
		}
		byNum[atoi(*p.PartNumber)] = *p.ETag
	}
	if len(byNum) == 0 {
		return nil, ErrMalformedXML
	}
	out := make([]CompletePart, 0, len(byNum))
	for _, n := range slices.Sorted(maps.Keys(byNum)) {
		out = append(out, CompletePart{Number: n, ETag: byNum[n]})
	}
	return out, nil
}

func ParseCopySourceRange(v string) (uint64, uint64, error) {
	rest, ok := strings.CutPrefix(v, "bytes=")
	if !ok {
		return 0, 0, ErrInvalidArgument
	}
	a, b, ok := strings.Cut(rest, "-")
	if !ok || a == "" || b == "" || strings.Trim(a, "0123456789") != "" || strings.Trim(b, "0123456789") != "" {
		return 0, 0, ErrInvalidArgument
	}
	first, err1 := strconv.ParseUint(a, 10, 64)
	last, err2 := strconv.ParseUint(b, 10, 64)
	if err1 != nil || err2 != nil {
		return 0, 0, ErrInvalidArgument // strtoull overflow: radosgw wraps; a value past 2^64 is nonsense either way
	}
	if first > last {
		return 0, 0, ErrInvalidRange
	}
	return first, last, nil
}
```

(radosgw's `xml_end` for a Part with an EMPTY PartNumber string returns false — `s.empty() → return false` — hence the `*p.PartNumber == ""` check; an empty ETag string is accepted. `find("Part")` yields direct children of the root only; `encoding/xml`'s `xml:"Part"` matches direct children too.)

Each op file follows W Task 10's shape (`Name`, `Action`, `OpMask`, `Init`, `VerifyPermission`, `Execute`, `Complete`), with the semantics above transcribed literally. `UploadPart.rangeFor(src)`:

```go
func (o *UploadPart) rangeFor(src *ObjectState) (ByteRange, error) {
	size := src.Size
	if src.Compression != nil {
		size = src.Compression.OrigSize
	}
	if o.Range == "" {
		return ByteRange{Offset: 0, Length: size}, nil // lst = accounted_size - 1
	}
	first, last, err := ParseCopySourceRange(o.Range) // already validated in Init; kept for a direct caller
	if err != nil {
		return ByteRange{}, err
	}
	if size == 0 {
		return ByteRange{}, nil // range_to_ofs skips its checks; the copy loop reads nothing
	}
	if first >= size {
		return ByteRange{}, ErrInvalidRange // -ERANGE
	}
	last = min(last, size-1)
	return ByteRange{Offset: first, Length: last - first + 1}, nil
}
```

Run: `go test -tags=ceph_preview ./internal/op/`. Expected: PASS.

- [ ] **Step 4: Check and commit**

```bash
make check
git add internal/op
git commit -m "feat(op): the multipart ops

InitiateMultipartUpload, UploadPart (with UploadPartCopy), Complete,
Abort, ListParts and ListMultipartUploads run radosgw's lifecycle:
bucket permission for the writes and the abort, the meta object's ACL
as the object policy for ListParts, the source's object permission
for a copy. The completion document is parsed as RGWMultiXMLParser
parses it, with the parts sorted and de-duplicated as its std::map
holds them, and radosgw's error rows are kept, ENOTSUP's 500 included.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 9: `s3`: handlers, XML, registration, W's `put_obj` branch

**Files:**
- Create: `internal/s3/multipart.go`, `internal/s3/multipart_test.go`
- Modify: `internal/s3/putobject.go` (W Task 11: the `uploadId` branch calls `uploadPart`; the ranged copy-source PUT without `uploadId` keeps `ErrNotImplemented`, P-D10), `internal/s3/handler.go` only if G's `NewHandler` does not already merge `multipartHandlers()` (G's contract says it does)

**Interfaces:**
- Consumes: G's `HandlerFunc`, `WriteXML`, `writeChunkedXML` (the chunked framing radosgw gives both listings), `WriteError`, `SetCommonHeaders`, `xmlHeader`, `ISO8601`, `op.Request` (`Method`, `Header`, `Query`, `Body`, `ContentLength`, `TLS`, `Identity`, `Object`, `Tenant`, `Bucket`, `Env`, `BucketRec`), `Handler.Register`; W's `requestAttrs`, `parseCopySource`, `putObject`; M's `writeXMLDeclaration`, the `r.Env.Authz.(*authz.Evaluator)` accessor and `userResolver(r)` (`authz.UserResolver{Users: r.Env.Users, Accounts: r.Env.Accounts}`, M Task 7); R's `parseInt`; G's `SetCommonHeaders` (which carries `x-amz-request-charged`, R-D15) and G's `SetContentLength` (radosgw's `dump_content_length`: the length with `Accept-Ranges: bytes`, R-D14), which only `uploadPart` calls, for a part without a copy source, since no other P success names its length to `end_header`; G's `xmltext.Text` and `xmltext.Escape` (ceph's XML text escaping); Z's `authz.Evaluator.BuildDefaultACL`, `authz.ErrorFor`, `tags.ParseHeader`, `tags.MaxObjectTags`; Task 8's ops; `cephconf.Options` (`rgw_max_put_param_size`, `rgw_max_listing_results`); `denc.Release` via `r.Env.Zone.Release()`.
- Produces:

```go
package s3

func multipartHandlers() map[string]HandlerFunc // init_multipart, complete_multipart, abort_multipart, list_multipart, list_bucket_multiparts

func initMultipart(ctx context.Context, w http.ResponseWriter, r *op.Request) error
// uploadPart serves put_obj requests carrying uploadId: UploadPart and
// UploadPartCopy. putObject calls it before anything else.
func uploadPart(ctx context.Context, w http.ResponseWriter, r *op.Request) error
func completeMultipart(ctx context.Context, w http.ResponseWriter, r *op.Request) error
func abortMultipart(ctx context.Context, w http.ResponseWriter, r *op.Request) error
func listParts(ctx context.Context, w http.ResponseWriter, r *op.Request) error
func listMultipartUploads(ctx context.Context, w http.ResponseWriter, r *op.Request) error

// urlEncodeKey is op.URLEncode(s, false): url_encode(val, encode_slash=false),
// dump_urlsafe's form for ListMultipartUploads' Key and Prefix
// (rgw_rest_s3.cc:4210, :4222 at v19.2.6 pass encode_slash=false explicitly).
// op.URLEncode is rgw-go's one encoder (char_needs_url_encoding
// transcribed); ListObjects uses it with true for Key and NextMarker and
// false for Prefix, Delimiter and CommonPrefixes.
func urlEncodeKey(s string) string { return op.URLEncode(s, false) }

// completeLocation is compute_domain_uri + RGWCompleteMultipart_ObjStore_S3::send_response's
// Location (rgw_rest.h:805-819, rgw_rest_s3.cc:4081-4098): "<scheme>://<host>/<bucket>/<key>",
// or "/<tenant>:<bucket>/<key>" for a tenanted bucket.
func completeLocation(r *op.Request) string
```

Handler semantics (`rgw_rest_s3.cc` at v19.2.6; `[T]` marks Tentacle-only lines):

- **init_multipart** (`get_params` [:3969-4027](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L3969-L4027), `send_response` [:4029-4062](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L4029-L4062)): the object-lock headers (`x-amz-object-lock-mode`, `-retain-until-date`, `-legal-hold`) as W's PUT handles them (bucket without object lock → `ErrInvalidRequest`; with → `ErrNotImplemented`, phase 2); `x-amz-tagging` → `tags.ParseHeader(v, tags.MaxObjectTags)`, any error → `ErrInvalidArgument` ([:3983-3991](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L3983-L3991)); `requestAttrs(r)`; the ACL through `BuildDefaultACL` (canned and grant headers, `OwnerOnly` fallback as M's `createBucket`); `StorageClass = x-amz-storage-class`; SSE headers → the R-D9 refusals (`prepare_encryption`: `x-amz-server-side-encryption` present → 501 as W's PUT does); `op.Run`; response: `SetCommonHeaders`, `x-amz-request-charged` (R-D15), 200 `application/xml` through `WriteXML`, with the `Content-Length` radosgw's frontend adds and no `Accept-Ranges` (`send_response` ends the header with `end_header(s, this, to_mime_type(s->format))`, [:4043](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L4043), [\[T\]:4544](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4544), which names no length; R-D14): `xmlHeader + <InitiateMultipartUploadResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">[<Tenant>t</Tenant>]<Bucket>b</Bucket><Key>k</Key><UploadId>id</UploadId></InitiateMultipartUploadResult>`. No `x-amz-abort-date` (lifecycle is phase 2, P-D15).
- **put_obj with uploadId → uploadPart** (`get_params` [:2597-2704](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L2597-L2704)): `r.ContentLength < 0` without `Transfer-Encoding: chunked` → `ErrMissingContentLength` (411; a copy PUT carries `Content-Length: 0`); `partNumber` absent → `ErrInvalidArgument` ([:2660](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L2660)-2670: `uploadId` without `partNumber`); `partNumber` → `strconv.Atoi` (bad → `ErrInvalidArgument`; radosgw sets no range on the number); `Content-MD5` → base64 decode, not 16 bytes → `ErrInvalidDigest`; `x-amz-copy-source` → `parseCopySource(v, r.Identity.Tenant)` (false → `ErrInvalidArgument`) and `x-amz-copy-source-range` raw into `Range`; `x-amz-tagging` and the object-lock headers as PUT (tags on a part are stored on the part head, as radosgw stores them); `requestAttrs(r)`; ACL via `BuildDefaultACL`; `op.Run`; response (`send_response` [:2730-2782](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L2730-L2782)): without a copy source `ETag: "<etag>"`, `SetContentLength(h, 0)` (`Content-Length: 0` and `Accept-Ranges: bytes`, as `dump_content_length(s, 0)` at [:2747](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L2747) sends them, [\[T\]:2911](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2911)), `x-amz-request-charged`, status 200 (`rgw_s3_success_create_obj_status` honoured as W's PUT does); with a copy source 200 `application/xml`: `xmlHeader + <CopyPartResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><LastModified>%Y-%m-%dT%H:%M:%S.000Z</LastModified><ETag>&quot;etag&quot;</ETag></CopyPartResult>` (`dump_format("ETag", "\"%s\"")` escapes the quotes it adds, [:2768](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L2768), [\[T\]:2938](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2938)), no `Accept-Ranges` (the copy branch's `end_header` names no length, [:2756](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L2756)) — whole seconds with a literal `.000Z` (strftime `%Y-%m-%dT%T.000Z`, [:2764-2770](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L2764-L2770)), no `ETag` header.
- **complete_multipart** (`get_params` [rgw_rest.cc:1580-1595](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L1580-L1595), [:4064-4074](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L4064-L4074); `send_response` [:4076-4108](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L4076-L4108)): the body through `io.LimitReader(r.Body, rgw_max_put_param_size+1)`, over the limit → `ErrInvalidRange` (`read_all_input`'s `-ERANGE`); `[T]` `If-Match`/`If-None-Match` into the op, left empty on Squid; the op reads `uploadId` from `r.Query`; `op.Run`; response 200 `application/xml` through `WriteXML`, with the frontend's `Content-Length` and no `Accept-Ranges` (`end_header(s, this, to_mime_type(s->format))`, [:4082](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L4082), [\[T\]:4612](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4612), names no length; R-D14): `xmlHeader + <CompleteMultipartUploadResult xmlns="…"><Location>L</Location>[<Tenant>t</Tenant>]<Bucket>b</Bucket><Key>k</Key><ETag>&quot;e&quot;</ETag></CompleteMultipartUploadResult>` (the quotes `dump_format` adds are escaped, [:4104](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L4104), [\[T\]:4634](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4634)) (`Location` first, as `dump_format` order has it; the key is NOT url-encoded). `x-amz-version-id` only when set (never in phase 1). A re-sent completion renders `<ETag>&quot;&quot;</ETag>` (the op's empty ETag, [rgw_op.cc:6438-6441](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L6438-L6441) at v19.2.6).
- **abort_multipart** ([:4110-4119](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L4110-L4119)): `op.Run`; 204, no body, `x-amz-request-charged`.
- **list_multipart** (`get_params` [rgw_rest.cc:1597-1622](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L1597-L1622); `send_response` [:4121-4171](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L4121-L4171)): `part-number-marker` → `strconv.Atoi` (bad → `ErrInvalidArgument`; negative accepted); `max-parts` → `parseValueAndBound(v, 0, rgw_max_listing_results, 1000)` (empty → default; non-numeric or trailing junk → `ErrInvalidArgument`; clamped); `op.Run`; the document through G's `writeChunkedXML`, framed as radosgw's `send_response` frames it: `end_header(..., CHUNKED_TRANSFER_ENCODING)` sends the headers first ([:4128](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L4128); [\[T\]:4664](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4664)) and the declaration and the document leave in its one closing flush ([:4169](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4169); [\[T\]:4717](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4717)), so the 200 `application/xml` response carries `Transfer-Encoding: chunked` and neither `Content-Length` nor `Accept-Ranges`: `<ListPartsResult xmlns="…">[<Tenant/>]<Bucket/><Key/><UploadId/><StorageClass>SC</StorageClass><PartNumberMarker>m</PartNumberMarker><NextPartNumberMarker>n</NextPartNumberMarker><MaxParts>x</MaxParts><IsTruncated>true|false</IsTruncated><Owner><ID>id</ID>[<DisplayName>n</DisplayName>]</Owner>` then per part `<Part><LastModified>T</LastModified><PartNumber>n</PartNumber><ETag>&quot;e&quot;</ETag><Size>s</Size></Part>` ([:4164](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L4164), [\[T\]:4707](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4707)); `T` is `ISO8601(mtime)` on Squid and `ISO8601(mtime.Truncate(time.Second))` on Tentacle (`dump_time_exact_seconds`, [rgw_rest_s3.cc:4704](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4704) at v20.2.4); `NextPartNumberMarker` is the highest part number returned, 0 when none; `DisplayName` omitted when empty (`dump_owner`); no `Initiator`. HEAD reaches the same op (`op_head` with `uploadId`, [:4823-4831](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L4823-L4831)) and `writeChunkedXML`'s HEAD form: the op runs, then the headers with `Content-Type: application/xml` and the `Content-Length: 0` radosgw's buffering filter completes a HEAD with, no `Accept-Ranges`, no `Transfer-Encoding`, no body (`dump_chunked_encoding` skips a HEAD, [rgw_rest.cc:399-411](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L399-L411); [rgw_client_io_filters.h:221-253](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_client_io_filters.h#L221-L253)). An error from `op.Run` is an ordinary error response on either method, as `end_header`'s error branch renders one ([rgw_rest.cc:620-624](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L620-L624)).
- **list_bucket_multiparts** (`get_params` [rgw_rest.cc:1624-1660](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L1624-L1660); `send_response` [:4173-4230](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L4173-L4230)): `delimiter`, `prefix`, `max-uploads` → `parseValueAndBound(v, 0, rgw_max_listing_results, 1000)`; `encoding-type` present and not `url` case-insensitively → `ErrInvalidArgument.WithMessage("Invalid Encoding Method specified in Request")`; `key-marker`, `upload-id-marker`; `op.Run`; the document through `writeChunkedXML`, framed as `send_response` frames it (`end_header(..., CHUNKED_TRANSFER_ENCODING)` [:4181](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L4181), [\[T\]:4729](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4729); one closing flush [:4228](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4228), [\[T\]:4776](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4776)), a 200 `application/xml` with `Transfer-Encoding: chunked` and neither `Content-Length` nor `Accept-Ranges`: `<ListMultipartUploadsResult xmlns="…">[<Tenant/>]<Bucket/>[<Prefix/>][<KeyMarker/>][<UploadIdMarker/>][<NextKeyMarker/>][<NextUploadIdMarker/>]<MaxUploads/>[<Delimiter/>]<IsTruncated/>` then per upload `<Upload><Key>k</Key><UploadId>id</UploadId><Initiator><ID/>[<DisplayName/>]</Initiator><Owner><ID/>[<DisplayName/>]</Owner><StorageClass>STANDARD</StorageClass><Initiated>T</Initiated></Upload>` (`Key` through `urlEncodeKey` when `encoding-type=url`), then `<CommonPrefixes><Prefix>p</Prefix>…</CommonPrefixes>` when any (one element holding every prefix, `open_array_section` once); the bracketed elements appear only when non-empty. `Initiated` is `ISO8601` (milliseconds) on both releases. HEAD (`op_head` with `uploads`, [:4685-4693](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L4685-L4693)) runs the op and answers as ListParts' HEAD does.
- `multipartHandlers()` returns the five routes; `putobject.go`'s `putObject` begins with `if r.Query.Has("uploadId") { return uploadPart(ctx, w, r) }` in place of W's `ErrNotImplemented` for that case (the `x-amz-copy-source-range` without `uploadId` case keeps W's refusal, P-D10).

- [ ] **Step 1: Write the failing handler specs**

`internal/s3/multipart_test.go`, on G Task 4's `newHandler(memstore, auth, cfg)` fixture with `do(req)` and the `alice` authenticator, and `srv`, an `httptest.Server` over the same handler, for the framing a client sees:

```go
var _ = Describe("multipart handlers", func() {
	It("runs an upload end to end over HTTP with radosgw's documents and headers", func() {
		rec := do(httptest.NewRequest("POST", "/plain/k?uploads", nil))
		Expect(rec.Code).To(Equal(200))
		Expect(rec.Header().Get("Content-Type")).To(Equal("application/xml"))
		Expect(rec.Header()).NotTo(HaveKey("Accept-Ranges"), "send_response's end_header names no length, rgw_rest_s3.cc:4043")
		Expect(rec.Body.String()).To(MatchRegexp(`^<\?xml version="1.0" encoding="UTF-8"\?><InitiateMultipartUploadResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Bucket>plain</Bucket><Key>k</Key><UploadId>2~[A-Za-z0-9_-]{32}</UploadId></InitiateMultipartUploadResult>$`))
		id := regexp.MustCompile(`<UploadId>([^<]+)</UploadId>`).FindStringSubmatch(rec.Body.String())[1]

		p1 := bytes.Repeat([]byte("a"), 5<<20)
		req := httptest.NewRequest("PUT", "/plain/k?uploadId="+id+"&partNumber=1", bytes.NewReader(p1))
		rec = do(req)
		Expect(rec.Code).To(Equal(200))
		Expect(rec.Header().Get("ETag")).To(Equal(`"` + md5hex(p1) + `"`))
		Expect(rec.Header().Get("Content-Length")).To(Equal("0"))
		Expect(rec.Header().Get("Accept-Ranges")).To(Equal("bytes"))
		Expect(rec.Body.Len()).To(BeZero())

		put := httptest.NewRequest("PUT", "/plain/src", bytes.NewReader(bytes.Repeat([]byte("s"), 8<<20)))
		Expect(do(put).Code).To(Equal(200))
		cp := httptest.NewRequest("PUT", "/plain/k?uploadId="+id+"&partNumber=2", nil)
		cp.ContentLength = 0
		cp.Header.Set("X-Amz-Copy-Source", "/plain/src")
		cp.Header.Set("X-Amz-Copy-Source-Range", "bytes=0-2097151")
		rec = do(cp)
		Expect(rec.Code).To(Equal(200))
		Expect(rec.Header().Get("ETag")).To(BeEmpty(), "CopyPartResult carries the etag in the body")
		Expect(rec.Body.String()).To(MatchRegexp(`^<\?xml version="1.0" encoding="UTF-8"\?><CopyPartResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><LastModified>\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.000Z</LastModified><ETag>&quot;` + md5hex(bytes.Repeat([]byte("s"), 2<<20)) + `&quot;</ETag></CopyPartResult>$`))

		rec = do(httptest.NewRequest("GET", "/plain/k?uploadId="+id+"&max-parts=1", nil))
		Expect(rec.Code).To(Equal(200))
		Expect(rec.Body.String()).To(ContainSubstring(`<ListPartsResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Bucket>plain</Bucket><Key>k</Key><UploadId>` + id + `</UploadId><StorageClass>STANDARD</StorageClass><PartNumberMarker>0</PartNumberMarker><NextPartNumberMarker>1</NextPartNumberMarker><MaxParts>1</MaxParts><IsTruncated>true</IsTruncated><Owner><ID>alice</ID><DisplayName>Alice</DisplayName></Owner><Part><LastModified>`))
		Expect(rec.Body.String()).To(MatchRegexp(`<Part><LastModified>\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z</LastModified><PartNumber>1</PartNumber><ETag>&quot;[0-9a-f]{32}&quot;</ETag><Size>5242880</Size></Part></ListPartsResult>$`))
		Expect(rec.Header().Get("Content-Length")).To(BeEmpty(), "RGWListMultipart_ObjStore_S3::send_response ends its header with CHUNKED_TRANSFER_ENCODING, rgw_rest_s3.cc:4128")
		Expect(rec.Header().Get("Accept-Ranges")).To(BeEmpty(), "no Content-Length, so no dump_content_length")

		rec = do(httptest.NewRequest("GET", "/plain?uploads&encoding-type=url&prefix=k", nil))
		Expect(rec.Code).To(Equal(200))
		Expect(rec.Body.String()).To(ContainSubstring(`<ListMultipartUploadsResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Bucket>plain</Bucket><Prefix>k</Prefix><NextKeyMarker>k</NextKeyMarker><NextUploadIdMarker>` + id + `</NextUploadIdMarker><MaxUploads>1000</MaxUploads><IsTruncated>false</IsTruncated><Upload><Key>k</Key><UploadId>` + id + `</UploadId><Initiator><ID>alice</ID><DisplayName>Alice</DisplayName></Initiator><Owner><ID>alice</ID><DisplayName>Alice</DisplayName></Owner><StorageClass>STANDARD</StorageClass><Initiated>`))

		doc := `<CompleteMultipartUpload><Part><PartNumber>1</PartNumber><ETag>"` + md5hex(p1) + `"</ETag></Part><Part><PartNumber>2</PartNumber><ETag>"` + md5hex(bytes.Repeat([]byte("s"), 2<<20)) + `"</ETag></Part></CompleteMultipartUpload>`
		req = httptest.NewRequest("POST", "/plain/k?uploadId="+id, strings.NewReader(doc))
		req.Host = "s3.example.com:7480"
		rec = do(req)
		Expect(rec.Code).To(Equal(200))
		Expect(rec.Header()).NotTo(HaveKey("Accept-Ranges"), "send_response's end_header names no length, rgw_rest_s3.cc:4082")
		Expect(rec.Body.String()).To(MatchRegexp(`^<\?xml version="1.0" encoding="UTF-8"\?><CompleteMultipartUploadResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Location>http://s3.example.com:7480/plain/k</Location><Bucket>plain</Bucket><Key>k</Key><ETag>&quot;[0-9a-f]{32}-2&quot;</ETag></CompleteMultipartUploadResult>$`))

		head := do(httptest.NewRequest("HEAD", "/plain/k", nil))
		Expect(head.Code).To(Equal(200))
		Expect(head.Header().Get("Content-Length")).To(Equal(strconv.Itoa(7 << 20)))
	})
	It("aborts with 204 and answers NoSuchUpload afterwards", func() {
		id := initUpload("/plain/k")
		rec := do(httptest.NewRequest("DELETE", "/plain/k?uploadId="+id, nil))
		Expect(rec.Code).To(Equal(204))
		Expect(rec.Body.Len()).To(BeZero())
		rec = do(httptest.NewRequest("DELETE", "/plain/k?uploadId="+id, nil))
		Expect(rec.Code).To(Equal(404))
		Expect(rec.Body.String()).To(ContainSubstring("<Code>NoSuchUpload</Code>"))
	})
	DescribeTable("request refusals",
		func(build func(id string) *http.Request, status int, code string) {
			id := initUpload("/plain/k")
			rec := do(build(id))
			Expect(rec.Code).To(Equal(status))
			Expect(rec.Body.String()).To(ContainSubstring("<Code>" + code + "</Code>"))
		},
		Entry("a part without a number", func(id string) *http.Request { return httptest.NewRequest("PUT", "/plain/k?uploadId="+id, strings.NewReader("x")) }, 400, "InvalidArgument"),
		Entry("a non-numeric part number", func(id string) *http.Request { return httptest.NewRequest("PUT", "/plain/k?uploadId="+id+"&partNumber=x", strings.NewReader("x")) }, 400, "InvalidArgument"),
		Entry("a part without a length", func(id string) *http.Request { r := httptest.NewRequest("PUT", "/plain/k?uploadId="+id+"&partNumber=1", nil); r.ContentLength = -1; return r }, 411, "MissingContentLength"),
		Entry("a bad Content-MD5", func(id string) *http.Request { r := httptest.NewRequest("PUT", "/plain/k?uploadId="+id+"&partNumber=1", strings.NewReader("x")); r.Header.Set("Content-MD5", "nope"); return r }, 400, "InvalidDigest"),
		Entry("a malformed copy range", func(id string) *http.Request { r := httptest.NewRequest("PUT", "/plain/k?uploadId="+id+"&partNumber=1", nil); r.ContentLength = 0; r.Header.Set("X-Amz-Copy-Source", "/plain/src"); r.Header.Set("X-Amz-Copy-Source-Range", "5-10"); return r }, 400, "InvalidArgument"),
		Entry("an inverted copy range", func(id string) *http.Request { r := httptest.NewRequest("PUT", "/plain/k?uploadId="+id+"&partNumber=1", nil); r.ContentLength = 0; r.Header.Set("X-Amz-Copy-Source", "/plain/src"); r.Header.Set("X-Amz-Copy-Source-Range", "bytes=10-5"); return r }, 416, "InvalidRange"),
		Entry("a completion with no body", func(id string) *http.Request { return httptest.NewRequest("POST", "/plain/k?uploadId="+id, nil) }, 400, "MalformedXML"),
		Entry("a completion body over rgw_max_put_param_size", func(id string) *http.Request { return httptest.NewRequest("POST", "/plain/k?uploadId="+id, bytes.NewReader(make([]byte, 1<<20+1))) }, 416, "InvalidRange"),
		Entry("an unknown upload on ListParts", func(string) *http.Request { return httptest.NewRequest("GET", "/plain/k?uploadId=2~nope", nil) }, 404, "NoSuchUpload"),
		Entry("an empty uploadId on ListParts", func(string) *http.Request { return httptest.NewRequest("GET", "/plain/k?uploadId=", nil) }, 500, "UnknownError"),
		Entry("a bad max-parts", func(id string) *http.Request { return httptest.NewRequest("GET", "/plain/k?uploadId="+id+"&max-parts=ten", nil) }, 400, "InvalidArgument"),
		Entry("a bad encoding type", func(string) *http.Request { return httptest.NewRequest("GET", "/plain?uploads&encoding-type=base64", nil) }, 400, "InvalidArgument"),
		Entry("a completion of a bogus upload", func(string) *http.Request { return httptest.NewRequest("POST", "/plain/k?uploadId=2~nope", strings.NewReader(`<CompleteMultipartUpload><Part><PartNumber>1</PartNumber><ETag>"x"</ETag></Part></CompleteMultipartUpload>`)) }, 500, "InternalError"),
	)
	It("renders the tenanted Location and the Tenant element", func() {
		// a request as the tenanted user "t1$bob" to /t1:tb/k: <Location>http://host/t1:tb/k</Location><Tenant>t1</Tenant><Bucket>tb</Bucket>
	})
	It("clamps max-uploads and max-parts to rgw_max_listing_results and defaults them to 1000", func() {
		// ?uploads&max-uploads=5000 → <MaxUploads>1000</MaxUploads>; ?uploadId=…&max-parts= → <MaxParts>1000</MaxParts>; max-parts=0 → 0 parts, IsTruncated true when any exist
	})
	It("url-encodes keys and prefixes without the slash under encoding-type=url", func() {
		// upload of "a b/c+d": <Key>a%20b/c%2Bd</Key>; with delimiter "/": <CommonPrefixes><Prefix>a%20b/</Prefix></CommonPrefixes>
	})
	It("writes Tentacle's ListParts LastModified in whole seconds", func() {
		// a memstore Env with Release Tentacle: <LastModified>…\.000Z</LastModified>
	})
	It("frames both listings chunked as radosgw does: no Content-Length and no Accept-Ranges", func() {
		id := initUpload("/plain/k")
		for _, target := range []string{"/plain/k?uploadId=" + id, "/plain?uploads"} {
			resp, err := http.Get(srv.URL + target)
			Expect(err).NotTo(HaveOccurred())
			b, err := io.ReadAll(resp.Body)
			Expect(resp.Body.Close()).To(Succeed())
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(200), target)
			Expect(resp.TransferEncoding).To(Equal([]string{"chunked"}), "end_header(..., CHUNKED_TRANSFER_ENCODING), rgw_rest_s3.cc:4128 and :4181")
			Expect(resp.ContentLength).To(BeEquivalentTo(-1), target)
			Expect(resp.Header.Get("Accept-Ranges")).To(BeEmpty(), target)
			Expect(resp.Header.Get("Content-Type")).To(Equal("application/xml"), target)
			Expect(string(b)).To(HavePrefix(`<?xml version="1.0" encoding="UTF-8"?>`), target)
		}
	})
	It("serves HEAD for both listings with the headers alone: Content-Length 0, no Accept-Ranges, no body", func() {
		id := initUpload("/plain/k")
		for _, target := range []string{"/plain/k?uploadId=" + id, "/plain?uploads"} {
			rec := do(httptest.NewRequest("HEAD", target, nil))
			Expect(rec.Code).To(Equal(200), target)
			Expect(rec.Header().Get("Content-Type")).To(Equal("application/xml"), target)
			Expect(rec.Header().Get("Content-Length")).To(Equal("0"), "the buffering filter completes a HEAD, rgw_client_io_filters.h:221-253")
			Expect(rec.Header().Get("Accept-Ranges")).To(BeEmpty(), target)
			Expect(rec.Header().Get("Transfer-Encoding")).To(BeEmpty(), "dump_chunked_encoding skips a HEAD, rgw_rest.cc:399-411")
			Expect(rec.Body.Len()).To(BeZero(), target)
		}
	})
})
```

- [ ] **Step 2: Run the specs to see them fail; implement `multipart.go`**

`completeLocation`:

```go
func completeLocation(r *op.Request) string {
	scheme := "http"
	if r.TLS {
		scheme = "https"
	}
	host := r.Header.Get("Host")
	if host == "" {
		host = r.Host
	}
	if r.Tenant != "" {
		return fmt.Sprintf("%s://%s/%s:%s/%s", scheme, host, r.Tenant, r.Bucket, r.Object.Name)
	}
	return fmt.Sprintf("%s://%s/%s/%s", scheme, host, r.Bucket, r.Object.Name)
}
```

(`rgw_transport_is_secure` also honours `rgw_trust_forwarded_https` with `X-Forwarded-Proto: https`; Z's `authz.Config.TrustForwardedHTTPS` reads the option — reuse the same predicate if Z exposed it, else read `rgw_trust_forwarded_https` through `r.Env.Conf` here.) `parseValueAndBound(v string, lo, hi, def int) (int, error)` transcribes [rgw_op.h:2633-2662](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.h#L2633-L2662): empty → def; `strconv.Atoi` after trimming trailing whitespace only (leading junk or trailing non-space → `ErrInvalidArgument`); clamp. ListParts and ListMultipartUploads render through G's `writeChunkedXML`, every other document through `WriteXML`. The XML documents are built with `encoding/xml` structs whose every string field is an `xmltext.Text` (G Task 4), so each text is escaped as radosgw's `XMLFormatter` escapes it (`xml_stream_escaper`, [src/common/escape.cc:134-169](https://github.com/ceph/ceph/blob/v19.2.6/src/common/escape.cc#L134-L169)) and the quotes around each ETag come out as `&quot;`, and whose optional elements use `omitempty` where radosgw omits them (Prefix, KeyMarker, UploadIdMarker, NextKeyMarker, NextUploadIdMarker, Delimiter, DisplayName, Tenant) and plain fields where radosgw always writes them (Bucket, Key, UploadId, StorageClass, markers of ListParts, MaxParts, MaxUploads, IsTruncated as the strings `true`/`false`). `uploadPart` reuses W's header parsing helpers (`requestAttrs`, `parseCopySource`, the Content-MD5 decode, the object-lock check); the ranged copy without `uploadId` never reaches it.

Run: `go test -tags=ceph_preview ./internal/s3/`. Expected: PASS.

- [ ] **Step 3: Check and commit**

```bash
make check
git add internal/s3
git commit -m "feat(s3): the seven multipart routes

InitiateMultipartUpload, UploadPart and UploadPartCopy (through the
put_obj route), CompleteMultipartUpload, AbortMultipartUpload,
ListParts and ListMultipartUploads parse the query and headers as
radosgw's get_params do and render its documents element for element:
Location before Bucket, optional markers and DisplayName omitted when
empty, STANDARD for every listed upload, url-encoded keys keeping the
slash, whole-second part timestamps on Tentacle. ListParts and
ListMultipartUploads are framed chunked, as radosgw frames them.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 10: Integration: cross-gateway oracle, `radosgw-admin bucket check`, `object stat` parity `[cluster]`

**Files:**
- Create: `test/integration/multipart_test.go` (`//go:build integration`, `Label("integration")`)
- Modify: `hack/rooket/README.md` (one paragraph under the integration section: what the multipart oracle exercises and how to run it)

**Interfaces:**
- Consumes: W Task 12's integration fixture and helpers (`cephtest.Conf()`, `cephtest.ReadManifest`, `goceph.Connect`, `driver.Open` against the rooket cluster, the `radosgwAdmin(args...) json` and `awsCLI(user, args...)` shell-outs through `rooket k`/the toolbox and the radosgw endpoint populate's manifest records; define them beside this file if W placed them in a `_test.go` this package cannot import); T Task 11's `make rgw-go-up RELEASE=<r>` (rgw-go serving the same zone) and its endpoint file; Tasks 3-9; `internal/cls/lock`.
- Produces: the oracle described below, runnable with `make cluster-up RELEASE=squid` (or `tentacle`) then `make integration RELEASE=squid` (or the phase-0 integration target: `grep -n integration Makefile`), never against the ambient cluster.

The oracle, per release, on a fresh bucket `mp-oracle` created through radosgw (populate's `alice`):

1. **rgw-go writes, radosgw reads.** Through rgw-go's HTTP endpoint (`rgw-go-up`) with alice's keys: InitiateMultipartUpload of `k1`, three parts of 8, 8 and 4 MiB (the CLI's `aws s3api upload-part`), CompleteMultipartUpload. Then through RADOSGW's endpoint: `head-object` ETag equals rgw-go's `<md5>-3` and `Content-Length` 20 MiB; `get-object` bytes equal the uploaded ones; `radosgw-admin object stat --bucket mp-oracle --object k1` JSON: `manifest.prefix` is `k1.<id>`, `manifest.rules` equals `meta.Manifest.MarshalJSON` of rgw-go's own decoding of the head's `user.rgw.manifest` (read back with the seam), `attrs` hold `user.rgw.idtag == user.rgw.tail_tag`, `user.rgw.etag`, no `user.rgw.storage_class` (default placement); `radosgw-admin bucket list --bucket mp-oracle` shows exactly one entry `k1` with that ETag and size, no `_multipart_` entries; `radosgw-admin bucket check --bucket mp-oracle` prints `"invalid_multipart_entries": []`.
2. **radosgw starts, rgw-go completes.** Through radosgw: `create-multipart-upload k2`, two parts of 6 MiB; through rgw-go: `list-parts` shows both with radosgw's ETags and `<StorageClass>STANDARD</StorageClass>`, `list-multipart-uploads` shows `k2` with alice as Initiator and Owner, `complete-multipart-upload`; through radosgw: `head-object k2` ETag `<md5>-2`, `get-object` equals; `object stat` manifest rules `{0: {1, 0, 6 MiB, 4 MiB}}`; `bucket check` clean; `rados -p <non-ec pool> ls` (the extra pool from populate's manifest, Rook's namespace) no longer lists `…__multipart_k2.<id>.meta`.
3. **rgw-go starts, radosgw completes.** The mirror image of 2 with `k3`; before completing, `rados -p <non-ec> ls` lists the meta object and `rados -p <non-ec> listomapkeys <oid>` shows `part.00000001`, `part.00000002`; `radosgw-admin bucket list` shows the two `_multipart_k3.<id>.<n>` entries and the `.meta` entry; after radosgw's completion everything reads back through rgw-go's GET.
4. **A re-uploaded part and a racing part.** Through rgw-go: upload `k4` part 1 twice (6 MiB then 5 MiB of other bytes) and part 2 once; `bucket check` clean while live (both part-1 heads have the meta entry); complete with the second part-1 ETag; `object stat` rules show `override_prefix` `k4.<rand>` on rule 0 (or the prefix itself when Append adopted it — the FIRST part's manifest is adopted whole, so `manifest.prefix` IS the random one and rule 0 has no override; assert what the driver's own `MarshalJSON` says and that radosgw's stat agrees); `radosgw-admin gc list --include-all` holds a chain tagged `<upload id>` whose objects are the first part-1's head and stripe; `radosgw-admin gc process --include-all` then removes them (`rados stat` fails); `bucket check` clean.
5. **Abort.** Through rgw-go: `k5` with three parts, abort; `gc list --include-all` holds the chain under `<upload id>` with every part head and stripe; `bucket list` has no `k5` entries; `bucket check` clean; the meta object is gone from the extra pool.
6. **Lock exclusion, both ways.** Through rgw-go: `k6` with two parts; hold `RGWCompleteMultipart` on its meta object from the test through `internal/cls/lock` (`LockExisting`, 60 s); radosgw's `complete-multipart-upload k6` fails with 500 `InternalError` and the message `This multipart completion is already in progress`; unlock; radosgw completes. Then `k7` through radosgw with parts; hold the lock the same way; rgw-go's complete fails the same; unlock; rgw-go completes. `bucket check` clean after each.
7. **Round-trip counts.** With the seam's `Stats` (G's `StatsReporter`) or the fakerados-free metric G exposes, a 5 MiB UploadPart through rgw-go costs: 1 meta read, 1 exclusive head write, 1 shadow write, 1 index prepare, 1 head write, 1 index complete (asynchronous), 1 registration — 7 RADOS ops; record the observed count in the PR description beside radosgw's (which this task cannot measure) rather than asserting parity.

- [ ] **Step 1: Write the spec skeleton with the seven `It`s** (each `It` runs one numbered stage; the fixture creates `mp-oracle` in `BeforeAll` and empties and removes it in `AfterAll` with `radosgw-admin bucket rm --purge-objects`).

- [ ] **Step 2: Run on Squid**

```sh
make cluster-up RELEASE=squid
make rgw-go-up RELEASE=squid
ROOKET_NAME=rgw-go-squid make integration RELEASE=squid GINKGO_FLAGS='--label-filter=integration --focus=multipart'
```

Expected: PASS. Every difference is a bug in Tasks 3-9 (or a wrong claim in this plan — fix the plan's sentence too). Record `bucket check`'s exact field names from the release's output in the spec the first time (the plan does not guess them).

- [ ] **Step 3: Run on Tentacle** — `RELEASE=tentacle`; additionally assert through rgw-go's ListParts that `LastModified` has `.000Z` (whole seconds) and, for a radosgw-started upload with `--checksum-algorithm CRC32` given to `create-multipart-upload`, that rgw-go's `list-parts` and `complete-multipart-upload` succeed without checksum elements (P-D5: rgw-go ignores checksums; the meta object radosgw wrote carries `cksum_type != none`, which rgw-go decodes and ignores — the completed object then lacks `user.rgw.cksum`, a documented phase-1 gap the PR description states).

- [ ] **Step 4: README and commit**

```bash
git add test/integration/multipart_test.go hack/rooket/README.md
git commit -m "test(integration): the cross-gateway multipart oracle

Uploads rgw-go writes are read back by radosgw and vice versa; an
upload begun by either gateway is completed by the other; a
re-uploaded part's history and an aborted upload's parts reach GC
under the upload-id tag; radosgw-admin bucket check stays clean at
every stage; and the RGWCompleteMultipart lock held by one gateway
excludes the other's completion with radosgw's 500.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 11: Gate: s3-tests multipart groups; registry updates `[cluster]`

**Files:**
- Create: `test/s3tests/p-groups.txt`
- Modify: `docs/ceph-upstream-bugs.md` (three entries), `docs/exclusions.md` (one bullet), `hack/rooket/README.md` (the gate line)

**Interfaces:**
- Consumes: T Task 4's `make s3tests RELEASE=<r> GATEWAY=rgw-go RUN=<n>`, T Task 5's `hack/parity` and `test/s3tests/baseline/<r>.json`, T Task 12's `make s3tests-parity RELEASE=<r>`; Tasks 1-10.
- Produces: a green parity diff for P's patterns on both releases; the registry entries below.

- [ ] **Step 1: The pattern file**

`test/s3tests/p-groups.txt` lists the `s3tests/functional/test_s3.py` names P is judged by, as regular expressions over the pytest node id (the exact names come from T's baseline JSON — this file is checked against it in Step 2, and every name in the baseline matching a pattern is P's):

```
test_multipart_
test_list_multipart
test_abort_multipart
test_atomic_multipart
test_multipart_copy_
test_multipart_upload_
test_multipart_get_part
test_object_copy_.*part
test_get_object_.*part
```

Not P's, because the parity set never runs them: the tests T's markers remove (`checksum`, `encryption`, `sse_s3`, `lifecycle`, `appendobject` among them), and the four that T's phase 2 deselect list (`hack/s3tests/deselect-phase2.txt`) removes although these patterns match them, because each enables bucket versioning: `test_multipart_copy_versioned`, `test_object_copy_versioning_multipart_upload`, `test_multipart_put_current_object_if_none_match` and `test_multipart_put_current_object_if_match`.

- [ ] **Step 2: Run the parity comparison on Squid**

```sh
make cluster-up RELEASE=squid && make rgw-go-up RELEASE=squid
make s3tests-parity RELEASE=squid
go run ./hack/parity diff -baseline test/s3tests/baseline/squid.json -candidate hack/rooket/out/squid/s3tests-rgw-go.json | grep -E -f test/s3tests/p-groups.txt   # CLUSTER_OUT is the Makefile's hack/rooket/out/$(RELEASE)
```

`make s3tests-parity` exits 1 while other units' differences remain; the grep is P's verdict. Expected: no lines. Each line is a P bug: the test name says which op; reproduce it against `memstore`/`fakerados` first (a handler or op spec), fix, re-run. Known candidates to check first, from the code read in this plan: `test_multipart_upload_size_too_small` (EntityTooSmall on a non-last part, Task 6), `test_multipart_upload_incorrect_etag` and `test_multipart_upload_missing_part` (InvalidPart), `test_multipart_upload_empty` (MalformedXML for no parts), `test_abort_multipart_upload_not_found` (404 NoSuchUpload), `test_multipart_copy_invalid_range` (400 InvalidArgument or 416 InvalidRange — the test accepts either; rgw-go answers 416 for an inverted range and 416 for a start past the end, 400 for a malformed header, Task 8), `test_multipart_resend_first_finishes_last` (a part re-uploaded while the first upload is in flight: the second write's EEXIST re-prefix, Task 4), `test_list_multipart_upload*` (paging and markers, Task 5), `test_multipart_get_part` (R-D17 arbitration: R's Task 8 owns the partNumber GET; P supplies the parts).

- [ ] **Step 3: Tentacle** — the same with `RELEASE=tentacle`.

- [ ] **Step 4: Registry entries**

Before writing any entry, run the prior-art check (`docs/ceph-upstream-bugs.md`'s rule and the global one): search tracker.ceph.com full text for `PUT_OBJ_EXCL`, `check_previously_completed`, `RadosMultipartUpload::init` and `MPRadosSerializer`; search ceph/ceph open and recently closed PRs for the same identifiers; record what was found in each entry's **Upstream** line, and file nothing without an explicit instruction (the entries name the tracker issues they would be filed as; the caller decides).

`docs/ceph-upstream-bugs.md`:

1. Update "cls_lock get_info and assert_locked fail with EIO on an expired ephemeral lock" → **rgw-go:** "binds the lock class in `internal/cls/lock` (lock, unlock, break_lock, get_info, assert_locked); it never takes an ephemeral lock, and the package documents that get_info and assert_locked must not be sent for one. The one data-path lock, `RGWCompleteMultipart`, is a plain exclusive lock."
2. New: **"PUT_OBJ_EXCL is defined but never consulted, so the multipart meta object is not created exclusively."** Kind: defect (documented intent not implemented), unfixed through main. Evidence: [`rgw_rados.h:67-69`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.h#L67-L69) defines `PUT_OBJ_EXCL` and `PUT_OBJ_CREATE_EXCL`; `RadosMultipartUpload::init` writes the meta object with `PUT_OBJ_CREATE_EXCL` and loops `while (ret == -EEXIST)` ([`rgw_sal_rados.cc:3270-3327`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L3270-L3327) at v19.2.6, [`:4114-4174`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L4114-L4174) at v20.2.4); `_do_write_meta` and `prepare_atomic_modification` test only `PUT_OBJ_CREATE` ([`rgw_rados.cc:3160`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L3160), [`:6484-6575`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6484-L6575)), and the meta object's state is never set atomic, so the write is `create(false)` + `obj_remove` — an upload id collision would silently overwrite the other upload's meta object, and the retry loop is dead. Releases: every release with the SAL multipart code; checked at v19.2.6, v20.2.4 and main (state the main SHA read). rgw-go: writes the same non-exclusive op (the layout is what coexistence needs) and relies on the 32-character random id as radosgw does. Upstream: the search results; the tracker issue to file if none.
3. New: **"A re-sent CompleteMultipartUpload of a finished upload answers 200 with an empty ETag."** Kind: intended behaviour that surprises (a retry after a lost response gets an empty quoted ETag, `<ETag>&quot;&quot;</ETag>` on the wire). Evidence: `RGWCompleteMultipart::execute` returns at [`rgw_op.cc:6428-6435`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L6428-L6435) (v19.2.6; [`:7260-7267`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7260-L7267) at v20.2.4) before `etag` is assigned at `:6531`; `send_response` dumps the member ([`rgw_rest_s3.cc:4104`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L4104); [`:4634`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4634) at v20.2.4). rgw-go: matches (Task 6). Upstream: the search results.

`docs/exclusions.md`, "Coexistence obligations": a new bullet **"Multipart uploads in flight are shared."** Either gateway may add parts to, list, complete or abort an upload the other began: the meta object's name and pool (`<key>.<id>.meta` in the placement's data-extra pool), its `multipart_upload_info` data and omap `part.%08d` entries, the part names, the `past_prefixes` history, the index entries in the `multipart` namespace and the `RGWCompleteMultipart` lock are radosgw's, and rgw-go writes the meta object and part heads without an idtag exactly as radosgw's non-atomic writes do. Two costs accepted: a part uploaded before Hammer (no manifest) is skipped by rgw-go's abort where radosgw's own branch is dead (P-D16); Tentacle checksums on multipart are decoded and ignored until phase 2.

- [ ] **Step 5: Commit**

```bash
git add test/s3tests/p-groups.txt docs/ceph-upstream-bugs.md docs/exclusions.md hack/rooket/README.md
git commit -m "test(s3tests): the multipart parity gate; registry entries

The s3-tests multipart groups pass against rgw-go with radosgw's
baseline on Squid and Tentacle. The registry records that PUT_OBJ_EXCL
is never consulted, so radosgw's meta object write is not exclusive
and its EEXIST loop is dead, and that a re-sent completion answers 200
with an empty ETag; the coexistence list gains the shared in-flight
upload.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

## Self-review

- **Spec coverage.** §6 "CompleteMultipart takes radosgw's named lock on the multipart meta object": Task 1 (the class package, `LockExisting`), Task 6 (the lock, its duration, the unlock rule), Task 10 stage 6 (exclusion both ways). §8 "head, tail and multipart names with radosgw's escaping" and the encoding rule: Task 2 (`MultipartMetaName`, `MultipartPartName`, `NewPartManifest`/`StripeObj`, `UploadPartInfo` and `MultipartUploadInfo` encoded per release and decoded at every version, proven by corpus goldens), Task 3 (the extra pool, the meta object's index entry), Task 4 (part heads and shadows). §9 "the seven multipart ops": Tasks 3-9 (Create/Init, Put/Copy part, List parts, List uploads, Complete, Abort — seven S3 routes over eight store methods). Unit P's gates in the index (`00-index.md`, "The nine units"): the cross-gateway oracle (Task 10 stages 1-3), `radosgw-admin bucket check` (Task 10 every stage), the s3-tests multipart groups (Task 11). D-A2 (read to EOF before completing): Task 4's `consume` reads to EOF and surfaces A's verdict before the head write and the registration; a copy's body is R's `ReadObject`. Z's rules: Task 8 (bucket permission for Init/Put/Complete, `S3AbortMultipartUpload`, the meta attrs as ListParts' object policy, the source check for a copy). W's contract: every write goes through `writeMeta`, `indexOp`, `tailWriter`, `enqueueGC` (Tasks 3-7) with additive fields only. R's: `PrefetchObject`/`ReadObject` for the copy source (Tasks 4, 8), `readHead` for `checkPreviouslyCompleted`. M's: `ListObjects` in the multipart namespace (Task 5), `LogUsage` (Task 8), `AdjustStats` (Tasks 3, 4, 6, 7), `Placement.DataExtraPool` (Task 3). N's: nothing consumed. Differences from radosgw: the one this unit keeps, client checksums ignored on Tentacle for single and multipart uploads (P-D5, owner decision 7), is recorded in `docs/exclusions.md` by Task 3 Step 7.
- **Placeholder scan.** No TBD/TODO. Elided spec bodies name the exact behaviour and expected values in their comment (Task 2's Tentacle checksum round trip; Task 3's extra-pool fallback; Task 4's failing source read; Task 5's legacy `2/` listing; Task 6's compression merge; Task 9's tenant, clamp, url-encoding, Tentacle timestamp and HEAD specs; Task 10's stages are the spec). Two implementation-time checks are stated as such rather than guessed: `radosclient.ExecResult.Bytes()`'s errno mapping (Task 1) and `planPut`'s alignment pool (Task 4). `bucket check`'s JSON field names are recorded from the release's output (Task 10), as W did.
- **Type consistency.** `mpRef{pool, oid, loc, key}`, `metaRef`, `readMeta`, `metaState{ref, size, mtime, attrs, version, info}` (Task 3) are what Tasks 4-7 call; `headWrite.atomic/pool/oid/loc/category/completeMultipart` and `indexOp.hashName/removeObjs` (Task 3) are what Tasks 3, 4, 6, 7 set; `tailWriter.namer/exclusiveFirst/first`, `newPartWriter`, `streamPart`, `registerPart`, `partResult` (Task 4); `listPartsPage`, `ListObjectsParams.NameFilter` (Task 5) are what Tasks 6, 7 (`listAllParts`) and the driver's `ListUploads` use; `lockMeta`, `unlockMeta`, `listAllParts`, `partCleanup{removeObjs, chain, prefixes}`, `retirePart`, `retireCurrentPart`, `deleteMeta`/`metaDelete`, `checkPreviouslyCompleted`, `multipartETag`, `sortParts`, `mergeCompression` (Task 6) are what Task 7 calls; `Upload.WriteTag/IfMatch/IfNoneMatch` (Task 6) are what Task 8's `CompleteMultipart` sets; `op.InitMultipart/UploadPart/CompleteMultipart/AbortMultipart/ListParts/ListMultipartUploads`, `ParseCompleteMultipart`, `ParseCopySourceRange` (Task 8) are what Task 9's handlers build; `meta.MultipartMetaName/MultipartPrefix/MultipartPartName/MultipartPartKey/ParseMultipartMeta/IsMultipartMeta/IsV2UploadID`, `UploadPartInfo`, `MultipartUploadInfo`, `ObjectRetention`, `ObjectLegalHold`, `NewPartManifest`, `StripeObj`, `Append` (Task 2) are used under those names throughout; `lock.LockOp/UnlockOp/BreakOp/AssertOp/GetInfoOp/Info/LockerID/LockerInfo/EntityName`, `Lock/LockExisting/Unlock/BreakLock/AssertLocked/GetInfo` (Task 1) are what Task 6 and the fake use; `rgw.MPUploadPartInfoUpdate` (Task 2) is what Task 4 calls. `op.Part.Size` is the accounted size everywhere (`RadosMultipartPart::get_size` returns `accounted_size`, [rgw_sal_rados.h:777](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.h#L777) at v19.2.6).
- **Review Focus.** 1 → Task 4 ("re-prefixes a re-uploaded part number"), Task 6 ("GCs a re-uploaded part's earlier objects"), Task 7 (the abort spec's past prefix), Task 10 stage 4. 2 → Task 6 ("retries the meta delete after a racing part"), Task 7 ("restarts the round"), Task 10 stage 4's racing part. 3 → Task 6 ("answers 200 with an empty ETag … and 500 for an unknown id"), Task 8's refusal table, Task 9's "completion of a bogus upload" entry. 4 → Task 4's refusal table (short body, bad digest, A's verdict, too large) and the vanished-upload and timeout specs. 5 → Task 5's `NameFilter` placement spec and the `ListUploads` specs (part heads uncounted, `<key>..meta`, the `.` delimiter, empty next markers), Task 9's clamp and url-encoding specs.
- **Cluster tasks.** Task 1 has one integration spec; Tasks 10 and 11 are the cluster gate; everything else runs on the corpus goldens, `fakerados`, `memstore` and the counterfeiter fakes. Behaviour verified only on the cluster, to be stated in Task 10's PR description: the exact `Location` host form radosgw renders for a virtual-hosted request (P-D14), radosgw's handling of an rgw-go-written meta object under a Tentacle checksum upload (stage 3 on Tentacle), and the GC worker actually removing the upload-id-tagged chains (stage 4-5 through `gc process`).
- **The bucket-delete abort.** `RadosBucket::remove` aborts every in-flight upload of a bucket the zone owns ([rgw_sal_rados.cc:395-400](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L395-L400) at v19.2.6, [:413-418](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L413-L418) at v20.2.4); Task 7 Steps 6-8 own it as `abortMultiparts`, called from M Task 7's `DeleteBucket` at the comment M leaves, paging the meta listing by 1000 and skipping `ErrNoSuchUpload` as `abort_multiparts` skips `-ERR_NO_SUCH_UPLOAD`. Before this task lands the only exposure is a radosgw-created upload on a shared zone (no rgw-go upload can exist before P); M's Task 7 states that gap.
- **Contract findings for the caller** (nothing in G's or Z's frozen contract renamed or removed; everything additive or a report): (a) `MultipartStore.Complete` and `CopyPart` carry no request-scoped parameters — the write tag, the Tentacle conditional headers and a copy's part-head attrs travel on additive `op.Upload` fields (`WriteTag`, `IfMatch`, `IfNoneMatch`) and on `up.Attrs` respectively (Tasks 4, 6); a `CompleteParams`/`PutParams` argument would be cleaner. (b) `ErrInvalidPartOrder` can never be produced against radosgw's semantics (its std::map sorts the parts); memstore's rule is relaxed (P-D4). (c) `op.ListObjectsParams` gains `NameFilter` (P-D6), the second additive listing parameter after M's `AllowUnordered`. (d) radosgw's head stat includes `cls_version_read` in the same op (`raw_obj_stat`, [rgw_rados.cc:8827-8870](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8827-L8870) at v19.2.6); R Task 3's `readHead` composes it first and stores it as `ObjectState.Version`. (e) G's ECANCELED mapping is settled: `FromRADOS` maps it to 409 ConcurrentModification ([rgw_common.cc:141](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L141) at v19.2.6), as G Task 1's table has it, and Task 6 matches — nothing outstanding. (f) G's `op.URLEncode(s, encodeSlash)` is the one encoder (`char_needs_url_encoding` transcribed); `urlEncodeKey` is `op.URLEncode(s, false)`. (g) W's `writeMeta` literal sets `Category: rgw.CategoryMain` explicitly; Task 3's `hw.category` override stays for the meta object and part heads.
