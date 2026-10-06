# Phase 1 Unit W: Object Write Path Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fill the write half of `internal/driver` and the ops and S3 handlers above it: PutObject with radosgw's round trips (guarded index prepare, tails streamed under the put window, the guarded head write carrying data, manifest and attributes, a fire-and-forget index complete), DeleteObject and DeleteObjects with GC enqueue in whichever format each GC shard is in and `pool_full_try`, server-side CopyObject sharing tails through the refcount class, SetObjectAttrs for the object ACL and tagging subresources, the index completion manager with its retry worker, and the GC worker under the `gc_process` lock.

**Architecture:** `internal/driver` gains one write-path struct hung off `Store` and a file per concern: the index protocol (`index.go`) and its completion manager (`completion.go`), the stripe writer that streams tails while the body arrives (`stripe.go`), the atomic head write that transcribes `RGWRados::Object::Write::write_meta` (`headwrite.go`), and `put.go`, `delete.go`, `copy.go`, `attrs.go`, `gc_enqueue.go`, `gc.go` on top. Every RADOS op is built through the seam's `radosclient.WriteOp` and the phase-0 class packages (`cls/rgw`, `cls/gc`, `cls/refcount`, `cls/version`), so the op shapes are inspectable in unit specs against counterfeiter fakes of `radosclient.Pool`. `internal/op` gains the five write ops as `op.Op` implementations over G's frozen `ObjectStore`, and `internal/s3` registers their handlers through `objectHandlers()`. Nothing here compresses, encrypts or versions: those are phase 2, and this plan writes only what unit R has already proved it reads.

**Tech Stack:** Go 1.27, cgo through ceph/go-ceph replaced by the jhoblitt/go-ceph fork at cbf97f85fcf5 (branch `rgw-go`), golang.org/x/sync (`semaphore`, `errgroup`), `crypto/md5`, `github.com/cespare/xxhash/v2` (decision W-D4 below), Ginkgo v2 and Gomega, counterfeiter, rooket disposable clusters (`make cluster-up RELEASE=squid|tentacle`).

**Spec:** `docs/superpowers/specs/2026-09-25-rgw-go-design.md` (§6 data path, §7 concurrency, §8 encoding, §2 priorities, §9 phase 1), read with the index's background in `docs/superpowers/plans/2026-09-29-phase-1/00-index.md` (unit W in "The nine units", "Shared interfaces and seam gaps"), G's frozen contract in `docs/superpowers/plans/2026-09-29-phase-1/G-gateway-core.md` (appendix "Frozen interface contract", which this plan fills and extends additively), `docs/exclusions.md` (the reshard guard, GC shard formats, refcount tags, lock names), `docs/ceph-upstream-bugs.md` (the zero-shard-count fault, the stale-epoch complete) and `docs/cgo-limitations.md`. Every claim below about radosgw was checked against the ceph checkout at `/home/jhoblitt/github/ceph` with `git show v19.2.6:<path>` and `git show v20.2.4:<path>`; a bare `file:line` is v19.2.6, and a v20.2.4 line is marked. The verified references are collected in "The write path radosgw runs" below; tasks cite them by section.

## Global Constraints

- Module `github.com/jhoblitt/rgw-go`; `go 1.27` in `go.mod`, minor only; every `go` command carries `-tags=ceph_preview` (the Makefile's `GO_TAGS`).
- Layout per the Go canon: everything under `internal/`; package names one lowercase word; no `util`, `common` or `helpers`.
- Dependencies point downward: `cls/*` import `denc`, `radosclient` and each other only; `meta` imports `denc`; `op` imports `meta`, `acl`, `policy`, `cephconf`, `radosclient`; `driver` imports `op`, `meta`, `cls/*`, `radosclient`; `s3` imports `op`. `radosclient/goceph` stays the only package importing go-ceph.
- G's frozen contract is filled, never renamed: W implements `ObjectStore.PutObject`, `DeleteObject`, `CopyObject`, `SetObjectAttrs` on `driver.Store` and registers `put_obj`, `delete_obj`, `copy_obj`, `multi_object_delete`, `put_acls` (object scope), `put_obj_tags`, `delete_obj_tags` in `internal/s3/object.go`'s `objectHandlers()`. Additive extensions are listed in "Contract extensions" and regenerate the fakes (`make generate`); `make generate-check` fails a stale fake.
- Byte-compatibility with radosgw is the acceptance test: head object names, tail names, xattr names and values (including NUL termination), manifest fields, index entry fields, GC entry fields and refcount tags are transcribed from the C++ cited in "The write path radosgw runs", not designed. Class requests encode at the cluster's `denc.Release`.
- The reshard guard `cls_rgw_guard_bucket_resharding` with `ret_err` = -2300 (`rgw.GuardBucketResharding`) precedes every `bucket_prepare_op` and every `bucket_complete_op`, the completion manager's retries included, on both releases ([`rgw_rados.cc:9472`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L9472), [`:9510`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L9510), [`:917`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L917); v20.2.4 [`:10406`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L10406), [`:10443`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L10443), [`:955`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L955)). A positive `ret_err` is a no-op, never send one. On `radosclient.ErrBusyResharding` the bucket instance is re-read and the op retried ("Reshard" below).
- `rgw_gc_max_objs` is validated at startup: a value below 1 refuses to start with a configuration error, a value above 65521 is clamped to 65521 (`rgw_shards_max`), and no code path divides by the shard count without that validation having run (`docs/ceph-upstream-bugs.md`, "radosgw faults on a zero rgw_gc_max_objs").
- `OpFlagFullTry` (`LIBRADOS_OPERATION_FULL_TRY`, 64) is set on the head remove of a delete, on every refcount put the GC worker or the inline fallback issues, and on copy's refcount rollback, exactly where radosgw calls `set_pool_full_try` ([`rgw_rados.cc:5943`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5943), [`:5448`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5448), [`:5009`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5009), [`rgw_gc.cc:677`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_gc.cc#L677)).
- Performance first (§2): a PUT within the head costs the same round trips as radosgw's (table below), tails stream while the body arrives under a 16 MiB per-request window (`rgw_put_obj_min_window_size`) on top of goceph's transport limiter (G Task 2), buffers are pooled and recycled after their completion fires, and nothing on the request path waits for the index complete.
- Every goroutine has a stop condition; fan-out is `errgroup.WithContext`; fire-and-forget work runs under the driver's lifetime context, never the request's, so a client disconnect cannot abandon an index complete.
- Tests are Ginkgo v2 and Gomega; integration specs carry `//go:build integration` and `Label("integration")`, read the cluster from `RGW_GO_TEST_CEPH_CONF` through `internal/testutil/cephtest`, and are marked `[cluster]` in the task index. Never touch the ambient Kubernetes or Ceph cluster.
- Logging is `log/slog`, JSON to stderr, static lowercase messages, snake_case keys, the `…Context` variants where a `ctx` is in scope.
- Errors wrap with `%w`; RADOS failures reach the op layer as `op.FromRADOS(err, op.ScopeObject)` unless a section below names a different mapping.
- The librados headers on this machine are absent: cgo builds use `CGO_CFLAGS=-I/home/jhoblitt/github/ceph/src/include` and `CGO_LDFLAGS=-L$HOME/.local/lib/rgw-go`; `go mod download` needs the sandbox disabled.
- Commits are Conventional Commits with the two repository trailers; each task ends in a draft PR assigned to the author, CI watched, merged on green with a merge commit under the standing authorization. Any material change to `docs/exclusions.md` is reported so the rgw-rs session can be told.

## Review Focus

1. **A PUT that overwrites an object another gateway replaced between our stat and our head write.** radosgw's guarded write fails with ECANCELED, cancels its index entry, deletes the tails it wrote, and still answers 200 with its ETag ([`rgw_rados.cc:3373-3391`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L3373-L3391), [`rgw_putobj_processor.cc:184-229`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_putobj_processor.cc#L184-L229), [`:403-406`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_putobj_processor.cc#L403-L406)); a write path that surfaces the ECANCELED as 500, or leaks its tails, diverges from radosgw and from S3's last-writer-wins. The cancel carries radosgw's `-1:0` (W-D8), which the class copies onto the entry, so a stale completion that arrives after it overtakes the winner's (tracker #80894); rgw-go meets that defect exactly as radosgw does until a release carries the class fix (ceph/ceph#72097), and a spec or a review that expects the pre-write version there is wrong. Pinned in Tasks 4 and 5.
2. **A DELETE of a key whose head is gone but whose index entry remains** (a crashed gateway between head remove and complete). radosgw answers 204 either way, but heals the index only when the head vanished between its stat and its remove: that ENOENT still gets `complete_del` ([`rgw_rados.cc:5954-5966`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5954-L5966), [`rgw_rest_s3.cc:3457-3461`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L3457-L3461)); a head already gone at the stat is a plain ENOENT with the entry left to M's listing reconciliation ([`:5900-5904`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L5900-L5904)). An implementation that turns the remove's ENOENT into a cancel, or into an error, leaves the entry for ever or answers 500. Pinned in Task 6.
3. **A bucket that starts resharding while a PUT is in flight.** The prepare or the complete fails with -2300; the prepare must re-read the instance and retry against the new shard, and the complete must retry from the completion manager, never be dropped ([`rgw_rados.cc:7024-7077`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L7024-L7077), [`:875-938`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L875-L938), [`:994-1023`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L994-L1023)). Pinned in Task 4.
4. **A GC shard in the omap format under a Tentacle gateway, or a queue shard under a Squid one.** Both gateways decide per shard through the version class and fall back to `gc_set_entry`; an enqueue that assumes the queue format fails with ECANCELED and leaks the tails, and a processor that lists only one format never frees the other's ([`rgw_gc.cc:120-139`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_gc.cc#L120-L139), [`:590-626`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_gc.cc#L590-L626)). Pinned in Tasks 6 and 9.
5. **A COPY onto itself, or across storage classes, or of an object held within its head.** Each takes a different branch: self-copy rewrites only the head and touches no refcount, a placement or pool change streams the data, an object without tails streams too, and only a tailed same-pool copy shares tails ([`rgw_rados.cc:4830-4849`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L4830-L4849), [`:4874-4879`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L4874-L4879), [`:4952-4981`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L4952-L4981)). A copy that shares tails across pools corrupts the destination's data on read, and a streamed copy of a compressed source that decodes its bytes, or a head copied from decoded bytes, stores plaintext under the source's compression info (W-D13). Pinned in Task 8.

## Decisions this plan owns

- **W-D1, the ECANCELED sentence in §6 follows the code, not the spec.** §6 says "An `ECANCELED` on the guarded head write retries the sequence as radosgw's atomic write does." radosgw retries once on EEXIST (the exclusive create of the assume-no-entry first pass, [`rgw_rados.cc:3419-3437`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L3419-L3437)) and treats ECANCELED, ENOENT and EEXIST on the guarded pass as success after cancelling the index op, when the request carried no precondition ([`:3373-3391`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L3373-L3391)). The MAX_ECANCELED_RETRY loops ([`:8492`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8492), [`:8592`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8592)) belong to the OLH code of phase 2. This plan implements what the code does and asks that §6's sentence be corrected to "a lost race on the guarded head write cancels the index entry and answers success, as radosgw's does".
- **W-D2, the put window is fixed at 16 MiB.** §6 says "16 MiB rising to 64 MiB"; `rgw_put_obj_max_window_size` is defined ([`rgw.yaml.in:114-120`](https://github.com/ceph/ceph/blob/v19.2.6/src/common/options/rgw.yaml.in#L114-L120)) but referenced nowhere in `src/rgw` at v19.2.6 or v20.2.4, and `rgw::make_throttle` takes `rgw_put_obj_min_window_size` alone ([`rgw_sal_rados.cc:2284`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L2284), [`rgw_rados.cc:5053`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5053)). rgw-go reads the min option only; §6's "rising to 64 MiB" should be struck.
- **W-D3, conditional PUT (`If-Match`, `If-None-Match`) is in scope.** radosgw evaluates both on PUT at Squid (`prepare_atomic_modification`, [`rgw_rados.cc:6493-6545`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6493-L6545)) and Tentacle (`check_preconditions`, v20.2.4 [`:7260-7329`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7260-L7329)), §9 lists Put unqualified, and T's parity set keeps the `conditional_write` marker: 8 of its 25 tests run in phase 1, the other 17 enable bucket versioning and are on T's phase 2 deselect list (`hack/s3tests/deselect-phase2.txt`). Task 10 implements them; it is the one scope item the user may cut, and it is isolated so cutting it removes one task step and one s3-tests marker.
- **W-D4, XXH64 comes from `github.com/cespare/xxhash/v2` v2.3.0.** The GC shard of a tag is `rgw_shards_mod(XXH64(tag, seed 8675309), rgw_gc_max_objs)`: the 64-bit hash, truncated to the 32-bit `unsigned` that `rgw_shards_mod` takes, then `% 7877`, or `% 65521` above 7877 shards, then `% rgw_gc_max_objs` ([`rgw_gc.cc:63-66`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_gc.cc#L63-L66), [`rgw_gc.h:27`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_gc.h#L27), [`rgw_tools.h:46-55`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_tools.h#L46-L55); v20.2.4 [`rgw_tools.h:63-72`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_tools.h#L63-L72)). The standard library has no XXH64. The module is on spec §4's third-party dependency list, amended on main to add it (decision 13); it is already in the module graph through the Prometheus client G adds (`prometheus/client_golang` v1.24.1 requires `cespare/xxhash/v2` v2.3.0), and v2.3.0 has the seeded digest the shard needs (`NewWithSeed`, `xxhash.go:45`; there is no seeded one-shot function).
- **W-D5, the tombstone cache is not built.** `rgw_obj_tombstone_cache_size` feeds `RGWObjState.mtime`, `pg_ver` and `zone_short_id` for a missing object ([`rgw_rados.cc:6157-6165`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6157-L6165), [`:5956-5960`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5956-L5960)), whose only consumers are multisite's `fetch_remote_obj` and the Swift object expirer, both excluded, and radosgw builds the cache only when other zones sync from its zone ([`:1343-1346`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L1343-L1346)), so on a single zone the two gateways already agree. Task 6 records it under `docs/exclusions.md`'s "Multisite".
- **W-D6, `PutParams.Tag` and `CopyParams.Tag` are additive contract fields.** radosgw's PUT and COPY use the request's transaction id as the write tag (`rgw_putobj_processor.cc:377`, [`rgw_op.cc:5674`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5674)), so `user.rgw.idtag` on a radosgw-written head is `tx…` plus NUL. The S3 handlers pass `r.ID`; an empty `Tag` falls back to `append_rand_alpha`'s form, `"_"` plus 31 characters of the url-safe base64 alphabet ([`rgw_common.h:1621-1627`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.h#L1621-L1627) appends `"_"` then a 32-byte buffer holding 31 characters and a NUL), which is what `set_attrs`, `copy_obj_data` and `delete` use (`rgw_rados.cc:6667`, `:5051`, `:7091`).
- **W-D8, the cancel sends radosgw's `-1:0`.** `UpdateIndex::cancel` completes the op through `cls_obj_complete_cancel`, which sends `bucket_complete_op` CANCEL with pool -1 and epoch 0 whatever the object's state ([`rgw_rados.cc:9553-9563`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L9553-L9563), v20.2.4 [`:10485-10495`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L10485-L10495), behind the reshard guard, `:7190-7204`, v20.2.4 [`:8041-8055`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L8041-L8055)), from all three of its callers: a lost or failed head write (`:3371-3374`), a failed delete (`:5968-5972`) and a failed `set_attrs` ([`:6726-6731`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6726-L6731)). rgw-go's `indexOp.cancel` takes no version and sends the same two integers from the same three places (Tasks 5, 6, 7). The class copies the op's version onto the entry before the CANCEL write-back ([`cls_rgw.cc:1094`](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L1094), v20.2.4 [`:1229`](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1229)), so after a cancel on an existing entry the next completion fails the pool comparison and applies at any epoch ([`:1082-1083`](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1082-L1083), v20.2.4 [`:1217-1218`](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1217-L1218)): tracker #80894 (`docs/ceph-upstream-bugs.md`, "cls_rgw complete_op writes a stale epoch back when it cancels"). rgw-go meets that defect exactly as radosgw does until a release carries the class fix (ceph/ceph#72097); the request is radosgw's byte for byte, so this is not a difference from radosgw and `docs/exclusions.md` has no entry for it. Flagged in Review Focus 1.
- **W-D9, `PutParams.ContentMD5` is an additive contract field.** radosgw compares the body's MD5 with `Content-MD5` before the head is written ([`rgw_op.cc:4459-4462`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L4459-L4462)), after the tails went out; only the driver stands between EOF and the head write, and it already hashes the body once for the ETag, so it takes the decoded digest and answers `ErrBadDigest` there rather than hashing twice in the op.
- **W-D10, the index entry's owner is the object ACL's owner.** G's `PutParams` carries no owner; `set_attrs` derives the entry's owner from the ACL attr ([`rgw_rados.cc:6700-6706`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6700-L6706)), and PUT and COPY hand the driver an ACL built for the requester (`create_s3_policy(s, driver, policy, s->owner)`), so decoding `user.rgw.acl` gives radosgw's `s->owner` without a contract change.
- **W-D11, the driver runs the second quota check.** radosgw checks the quota against `Content-Length` before the body and against the bytes received after it ([`rgw_op.cc:4200-4207`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L4200-L4207), [`:4418-4422`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L4418-L4422)); the op can only do the first, so `PutObject` calls `CheckQuota` with the received size before the head write, which is also the only check a chunked upload without a length gets.
- **W-D12, `DeleteParams` gains `UnmodifiedSince`, `IfMatchSize` and `IfMatchLastModified`.** `x-amz-delete-if-unmodified-since` is read by the S3 handler on both releases ([`rgw_rest_s3.cc:3427-3444`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L3427-L3444)); `If-Match`, `x-amz-if-match-size` and `x-amz-if-match-last-modified-time` only on Tentacle (v20.2.4 [`:3685-3733`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L3685-L3733), `check_preconditions` [`:7260-7329`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7260-L7329)). G's `DeleteParams.IfMatch` covers the first Tentacle header; the other three are additive, and the op sets the Tentacle ones only when the release is Tentacle, since a Squid radosgw ignores those headers.
- **W-D13, a copy that streams its data copies the stored bytes.** Where `copy_obj` cannot share the source's tails (no manifest, another tail placement rule or pool, a source without tails, a head above the chunk size; [`rgw_rados.cc:4849-4866`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L4849-L4866)), radosgw runs `copy_obj_data` ([`:4875-4879`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L4875-L4879), [`:5034-5114`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5034-L5114)): it reads the source's stored bytes with `Read::read` ([`:5067`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5067)), which does not decompress, writes them through a fresh `AtomicObjectProcessor` for the destination placement under a random tag ([`:5051`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5051)), keeps `user.rgw.compression` (copied into the destination attrs at `:4791-4793`) and completes with `accounted_size` = that info's `orig_size` ([`:5098-5108`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5098-L5108)); the destination placement's compression setting is never consulted. rgw-go's `copyData` does the same through R's raw stored-byte read, `Store.readStored` (R Task 4, driver-internal, so G's store interfaces stay as they are): a compressed source's copy keeps the source's codec, block map and ETag, and `putStream` takes the accounted size from the compression info as `copy_obj_data` does. This is a Squid radosgw's behaviour exactly. A Tentacle radosgw differs, twice, and Task 8 records both in `docs/exclusions.md`'s coexistence section: (1) it routes `copy_obj_data` through the copy's data-processor factory (`RGWCopyObjDPF`, v20.2.4 [rgw_op.cc:5728-5891](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L5728-L5891), passed by every `RGWCopyObj::execute`, [`:6222`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L6222)), which decodes a compressed source, recompresses it with the destination placement's compression type and writes a new `user.rgw.compression` only if that compressor compressed (`:5776-5790`, [`:5821-5840`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L5821-L5840), [`:5871-5890`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L5871-L5890); [rgw_rados.cc:5324-5355](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5324-L5355)); rgw-go cannot compress in phase 1 (spec §9 puts compression on the write path in phase 2), and the stored-byte copy reads back identically through every gateway; by code reading, Tentacle's path also completes an uncompressed result with the source's stored size as its accounted size (`ofs = end + 1`, v20.2.4 [rgw_rados.cc:5346](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L5346), [:5378](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L5378)), which the stored-byte copy never does. (2) An encrypted source: a Squid radosgw answers 501 ([`:4755-4763`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L4755-L4763)); a Tentacle one drops the crypt attrs (v20.2.4 rgw_rados.cc:5034-5041), forces the streamed path (`:5124-5126`) and decrypts the source, re-encrypting as the request asks ([rgw_op.cc:5793-5805](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5793-L5805), [:5846-5866](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5846-L5866)); rgw-go has no SSE-C decryption in phase 1 and no key server, so it answers 501 on both releases and never copies ciphertext under a head without its crypt attrs.
- **W-D7, `StatsStore.AdjustStats` is an additive contract method.** radosgw updates its quota cache after every write and delete ([`rgw_rados.cc:3358-3365`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L3358-L3365), [`:5984`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5984)); G's `StatsStore` has `CheckQuota` only. Task 10 adds `AdjustStats(ctx, rec, owner, objs, addBytes, removedBytes int64)`; the driver's implementation is M's cache when M has landed and a no-op until then, memstore implements it for the specs.

## The write path radosgw runs

This section is the verified reference every task transcribes. Line numbers are v19.2.6 unless marked; where v20.2.4 differs the difference is stated.

### Round trips on the latency path

| Request | Synchronous RADOS ops, in order | Fire and forget |
|---|---|---|
| PUT of a new key within the head (≤ 4 MiB) | 1. guarded `bucket_prepare_op` on the index shard ([`rgw_rados.cc:9456-9479`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L9456-L9479)); 2. head write with exclusive create, idtag, tail_tag, data, manifest, attrs, pg_ver, source_zone ([`:3124-3300`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L3124-L3300)) | `bucket_complete_op` ADD ([`:9481-9523`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L9481-L9523)) |
| PUT beyond the head | tails written as the body streams, one op per 4 MiB stripe ([`rgw_putobj_processor.cc:140-159`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_putobj_processor.cc#L140-L159)), window 16 MiB; then 1 and 2 above ([`:343-411`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_putobj_processor.cc#L343-L411)) | complete ADD |
| PUT over an existing key | 1. prepare; 2. head write with exclusive create fails EEXIST ([`:3302-3306`](https://github.com/ceph/ceph/blob/v19.2.6/src/common/options/rgw.yaml.in#L3302-L3306)); 3. `raw_obj_stat` of the head ([`:6150`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6150)); 4. head write with `cmpxattr(idtag)`, `create(false)`, `obj_remove`, then the rest ([`:6506-6555`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6506-L6555)); 5. GC enqueue of the old tails if the old manifest had any ([`:5382-5407`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5382-L5407)) | complete ADD |
| PUT with `If-Match` or `If-None-Match` | 1. stat (no assume-no-entry pass, `:3423`); 2. prepare; 3. guarded head write; 4. GC enqueue of old tails | complete ADD |
| DELETE (unversioned bucket) | 1. stat (`:5854`); 2. guarded prepare DEL (`:5931`); 3. head `obj_remove` with `pool_full_try`, `cmpxattr(idtag)` on Squid only (`:5914`, `:5936-5945`; v20.2.4 `:6663-6699` has no cmpxattr); 4. GC enqueue of tails (`:5963`) | `bucket_complete_op` DEL (`:5960`) |
| DELETE of a missing key | 1. stat → ENOENT (`:5900-5904`) | none |
| DeleteObjects | per key the DELETE sequence, up to `rgw_multi_obj_del_max_aio` (16) keys at once ([`rgw_op.cc:7011`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L7011), [`:6966-6987`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L6966-L6987)) | per key |
| COPY of a tailed object, same placement and pool | 1. source stat with xattrs (`:4740-4745`); 2. `refcount get` on every tail, 10 at once ([`:4906-4945`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L4906-L4945)); 3. read of the source head data ([`:4959-4965`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L4959-L4965)); 4. prepare; 5. head write (exclusive, EEXIST → stat → guarded) ; 6. GC of the destination's old tails | complete ADD |
| COPY of an object within its head, or across placement, storage class or pool | 1. source stat; then `copy_obj_data`: the PUT sequence with the source's stored bytes, read undecoded, streamed through the stripe writer ([`:5034-5114`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5034-L5114); v20.2.4 through the copy's data-processor factory, W-D13) | complete ADD |
| COPY onto itself (REPLACE metadata) | 1. source stat; 2. prepare; 3. guarded head write rewriting attrs, `keep_tail`, no refcount change ([`:4952-4981`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L4952-L4981)) | complete ADD |
| PutObjectAcl, PutObjectTagging, DeleteObjectTagging | 1. stat ([`rgw_sal_rados.cc:2401-2408`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L2401-L2408)); 2. guarded prepare ADD ([`rgw_rados.cc:6667-6672`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6667-L6672)); 3. head op: `cmpxattr(idtag)`, rmxattrs, setxattrs, new idtag, mtime ([`:6612`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6612), [`:6636-6688`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6636-L6688)) | complete ADD ([`:6720-6725`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6720-L6725)) |

`bucket_complete_op` is issued with `aio_operate` and a completion callback; the request never waits for it ([`:9514-9518`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L9514-L9518)). On -2300 the callback hands the completion to the retry thread, which re-initialises the shard from a fresh bucket instance and reissues `assert_exists`, the guard and the complete under `guard_reshard` ([`:875-938`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L875-L938), [`:994-1023`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L994-L1023)); any other error is logged and dropped ([`:1012-1016`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L1012-L1016)).

### Names, tags and attributes a PUT writes

- **Head object:** oid `<bucket marker>_<key oid>` in the data pool of the bucket's placement rule (`get_obj_bucket_and_oid_loc`; `meta.Stripe.OID`, `meta.ObjKey.OID`), locator `<marker>_<name>` only for a name starting with `_` (`meta.ObjKey.Locator`). Under Rook the pool is a namespace of a shared pool; `meta.Pool{Name, NS}` from `op.ZoneInfo.Placement`.
- **Tail objects:** manifest prefix `"." + 31 characters + "_"` (`rgw_obj_manifest.cc:239-248`: `char buf[33]; gen_rand_alphanumeric(cct, buf, sizeof(buf) - 1)` writes 31 characters and a NUL from the url-safe base64 alphabet `A-Za-z0-9-_`, [`src/common/random_string.cc:45-51`](https://github.com/ceph/ceph/blob/v19.2.6/src/common/random_string.cc#L45-L51); the corpus prefix `.AkTbQPoXT3s8QcthZt-DpS5NPRLqS5u_` has 31); stripe *n* of part 0 is key `<prefix><n>` in namespace `shadow` (`RGWObjManifest::get_implicit_location`), so the oid is `<marker>__shadow_.<rand>_<n>`; stripes count from 1 when `max_head_size > 0` and from 0 otherwise ([`:26-34`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_obj_manifest.cc#L26-L34)). The tail pool is the data pool of the tail placement rule.
- **Manifest** (`meta.Manifest`): `ExplicitObjs` false; `Obj` the head; `HeadSize = min(size, MaxHeadSize)`; `MaxHeadSize` = the head chunk size (4 MiB when the placement has `inline_data` and head and tail pools agree, else 0, [`rgw_putobj_processor.cc:285-312`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_putobj_processor.cc#L285-L312)); `Prefix` as above; `Rules = {0: {StartPartNum 0, StartOfs = MaxHeadSize, PartSize 0, StripeMaxSize = stripe size}}` (`set_trivial_rule`, [rgw_obj_manifest.h:259-263](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_obj_manifest.h#L259-L263): `RGWObjManifestRule rule(0, tail_ofs, 0, stripe_max_size)`); `TailPlacement = {head bucket, tail rule}`; `TailInstance = key.Instance`; `ObjSize = size`; `HeadPlacementRule` = the bucket's rule. Stripe size is `rgw_obj_stripe_size` (4 MiB) rounded down to the pool's required alignment when it has one; the chunk size is `rgw_max_chunk_size` (4 MiB) likewise (`RGWRados::get_max_chunk_size`, `get_max_aligned_size`; [`:280`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_putobj_processor.cc#L280), [`:317`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_putobj_processor.cc#L317)).
- **Tail placement rule:** the request's `x-amz-storage-class` (empty when absent) inheriting the bucket's rule ([`rgw_op.cc:576-582`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L576-L582)); an invalid pair is 400 InvalidArgument (`-EINVAL`, [`:580-582`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L580-L582)). The storage class string is what `user.rgw.storage_class` and the index entry carry: absent header, empty string, no xattr ([`rgw_rados.cc:3225-3226`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L3225-L3226), [`:3253-3257`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L3253-L3257)); `x-amz-storage-class: STANDARD` writes the xattr `STANDARD`.
- **Head xattrs, in the op's order** ([`rgw_rados.cc:3171-3257`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L3171-L3257)): `user.rgw.idtag` = tag + NUL and `user.rgw.tail_tag` = tag + NUL ([`:6562-6574`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6562-L6574)); `user.rgw.manifest` (encoded at the release); then every non-empty request attr in `std::map` (byte) order: `user.rgw.acl` (the policy, owner = the requester), `user.rgw.etag` = 32 hex, **no NUL** ([`rgw_op.cc:4524-4525`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L4524-L4525)), `user.rgw.content_type` and the other generic headers (`Content-Language`, `Expires`, `Cache-Control`, `Content-Disposition`, `Content-Encoding`, `X-Robots-Tag`; each value + NUL, [`rgw_rest.cc:123-131`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L123-L131), `rgw_op.cc populate_with_generic_attrs`), `user.rgw.x-amz-meta-<lowercase name>` = value + NUL after `format_xattr` (quoted-printable when not UTF-8 or containing control characters, [`rgw_op.h:2141-2160`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.h#L2141-L2160); repeated headers joined with `,`), `user.rgw.x-amz-tagging` (encoded RGWObjTags) when `x-amz-tagging` was sent; then `obj_store_pg_ver` for `user.rgw.pg_ver` ([`:3236-3238`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L3236-L3238)), `user.rgw.source_zone` = the zone's short id as 4 LE bytes ([`:3240-3244`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L3240-L3244); the id is the period map's `short_zone_ids` entry, [`svc_zone.cc:297`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_zone.cc#L297), `meta.Period.PeriodMap.ShortZoneIDs`); `user.rgw.storage_class` when non-empty ([`:3253-3257`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L3253-L3257)). `x-amz-meta-*` limits: `rgw_max_attr_name_len` → ENAMETOOLONG, `rgw_max_attr_size` → EFBIG, `rgw_max_attrs_num_in_req` → E2BIG, all 0 (unlimited) by default ([`rgw_op.h:2202-2221`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.h#L2202-L2221)); the blocklist skips `x-amz-storage-class` and the SSE-C headers ([`:2177-2182`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.h#L2177-L2182)).
- **Mtime:** `PutParams.Mtime`, zero means now, stamped with `WriteOp.SetMtime` (`op.mtime2`, [`:3193-3194`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L3193-L3194)).
- **Index prepare** (`rgw.PrepareOp`): `Op` ADD (DEL for a delete); `Key` = index key name and instance; `Tag` = the write tag without NUL; `Locator` = the key's locator; on Squid `LogOp` = the zone's `log_data` and `ZonesTrace = {"<zone id>:<tenant/name:bucket_id>"}` ([`:9456-9479`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9456-L9479)); on Tentacle `cls_rgw_bucket_prepare_op` takes only op, tag, key and locator (v20.2.4 [`cls_rgw_client.cc:164-175`](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw_client.cc#L164-L175)), so `LogOp` false, `BILogFlags` 0 and an empty `ZonesTrace` go out in the same (7, 5) struct. Every prepare is preceded by `AssertExists` and the guard.
- **Index entry** (`rgw.CompleteOp`): `Op` ADD; `Key` = index key name and instance; `Locator` = the key's locator; `Ver = {Pool: head pool id, Epoch: the head write's version}` ([`:9511-9513`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L9511-L9513)); `Meta = {Category Main, Size = bytes written, AccountedSize = bytes written (no compression), Mtime, ETag, Owner = owner id string, OwnerDisplayName, ContentType, StorageClass, UserData "", Appendable false}` (`:7124-7137`); `Tag` = the write tag without NUL; `LogOp` false and `BILogFlags` 0 in an unversioned bucket of a single zone (`need_to_log_data` is the zone's `log_data`, false under Rook); `ZonesTrace = {"<zone id>:<tenant/name:bucket_id>"}` ([`:9466-9468`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L9466-L9468), `rgw_zone_set::insert`).
- **Guard on the head write:** `cmpxattr(user.rgw.idtag, EQ, <bytes as read, NUL included>)` when the object has a manifest or an idtag and the tag is not a fake one, and when `If-None-Match` is not `*` ([`:6493-6512`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L6493-L6512); v20.2.4 [`:7295-7299`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7295-L7299), [`:7345-7349`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7345-L7349)). A head with a manifest but no idtag gets a fake tag and no guard ([`:6241-6247`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L6241-L6247), `generate_fake_tag` [`:6046-6068`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6046-L6068)).
- **Preconditions** (`:6515-6545`; v20.2.4 `check_preconditions` [`:7260-7329`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7260-L7329)): `If-Match: *` fails with 412 when the object is missing at v19.2.6, and v20.2.4 returns ENOENT, 404 NoSuchKey, which rgw-go answers on both releases (owner ruling, option B: v20.2.4's `check_preconditions` on both, `docs/exclusions.md` "Write conditions are Tentacle's on Squid too", which lists every difference from v19.2.6); `If-Match: <etag>` compares the unquoted value to the stored etag by prefix; `If-None-Match: *` fails with 412 when the object exists; `If-None-Match: <etag>` fails when equal. After the head write, a race is resolved as [`:3392-3416`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L3392-L3416) says: with `If-Match: *`, ENOENT → 412 and ECANCELED → success; with `If-None-Match: *`, EEXIST → 412 and ENOENT → success; with an etag condition any race error propagates.

### Delete

`Delete::delete_obj` unversioned ([`:5757-5987`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5757-L5987)): stat; `If-Unmodified-Since` (`x-amz-delete-if-unmodified-since`, Swift-era) adds `obj_check_mtime LE` ([`:5861-5876`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5861-L5876)); a missing head is ENOENT ([`:5900-5904`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5900-L5904)) which the S3 layer answers 204 ([`rgw_rest_s3.cc:3455-3470`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L3455-L3470)); Squid adds `cmpxattr(idtag)` through `prepare_atomic_modification(removal_op = true)` ([`:5914`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5914), [`:6506-6512`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6506-L6512), [`:6557-6560`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6557-L6560)), Tentacle does not (v20.2.4 [`:6663-6670`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6663-L6670)); guarded prepare DEL with an optag of `"_"` plus 31 random characters, since the removal never sets `write_tag` ([`:5931`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5931), [`:7091`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L7091)); head op `obj_remove` keeping `user.rgw.olh.*` ([`:5936`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5936), `remove_rgw_head_obj` [`:5743-5748`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5743-L5748)) with `pool_full_try` ([`:5943`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5943)); ETIMEDOUT leaves the pending entry ([`:5951-5953`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5951-L5953)); success or ENOENT → `complete_del` with the state's mtime, category None, pool id and epoch ([`:5954-5961`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5954-L5961), [`:9536-9551`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L9536-L9551)) then GC of the manifest's tails ([`:5963`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5963)); any other error → cancel ([`:5968-5971`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5968-L5971)); ECANCELED is answered 204 ([`rgw_op.cc:5302-5304`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5302-L5304)). At v20.2.4 the S3 layer also reads `If-Match`, `x-amz-if-match-size` and `x-amz-if-match-last-modified-time` (`rgw_rest_s3.cc` v20.2.4 `RGWDeleteObj_ObjStore_S3::get_params`) and `check_preconditions` enforces them ([`:6663-6670`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L6663-L6670)); G's `DeleteParams.IfMatch` carries the first, Task 10 keys it on the release.

### GC enqueue

`complete_atomic_modification` ([`:5382-5407`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5382-L5407)): tag = `user.rgw.tail_tag` bytes if present else `user.rgw.idtag` bytes, **NUL included** (`bufferlist::to_str()` keeps it); chain = every manifest stripe except the head, each `{pool = mobj.pool.to_str() ("<pool>" or "<pool>:<ns>"), key.name = raw oid, loc}` (`update_gc_chain` [`:5409-5421`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5409-L5421)); `send_split_chain` splits so one `cls_rgw_gc_set_entry_op` encodes under `rgw_max_chunk_size` ([`rgw_gc.cc:68-118`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_gc.cc#L68-L118)); `send_chain` writes `version check(ver 1, EQ) + rgw_gc_queue_enqueue(rgw_gc_obj_min_wait, info)` to shard `gc.<XXH64(tag, 8675309) mod>` ([`:120-132`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_gc.cc#L120-L132), [`rgw_gc_log.cc:29-36`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_gc_log.cc#L29-L36)) and on ECANCELED or EPERM falls back to `gc_set_entry` ([`:133-138`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_gc.cc#L133-L138)); when the enqueue fails otherwise the tails are deleted inline with `refcount put(tag, implicit)` under `pool_full_try` ([`:5399-5403`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5399-L5403), [`:5432-5465`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5432-L5465); v20.2.4 [`:6155-6195`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6155-L6195) issues them `rgw_multi_obj_del_max_aio` at a time). The shard index is `rgw_shards_mod(hash, max_objs)`: `hash % 7877 % max_objs` when `max_objs <= 7877`, `hash % 65521 % max_objs` above ([`rgw_tools.h:46-55`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_tools.h#L46-L55)).

### GC worker

`RGWGC::initialize` ([`rgw_gc.cc:31-56`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_gc.cc#L31-L56)): `max_objs = min(rgw_gc_max_objs, 65521)`; shard oids `gc.0` … `gc.<n-1>` in the zone's `gc_pool`; each gets `create(false) + version check(0, EQ) + rgw_gc_queue_init(rgw_gc_max_queue_size, rgw_gc_max_deferred) + version set(1)` ([`rgw_gc_log.cc:11-19`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_gc_log.cc#L11-L19)); ECANCELED means already queue-era. `GCWorker::entry` ([`:782-809`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_gc.cc#L782-L809)): `process(expired_only = true)` then sleep `rgw_gc_processor_period` (1 h) less the run time. `process` ([`:729-748`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_gc.cc#L729-L748)): shards from a random start; each `process(index)` ([`:550-727`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_gc.cc#L550-L727)): exclusive lock `gc_process` on the shard for `rgw_gc_processor_max_time` (1 h), EBUSY → skip ([`:557-578`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_gc.cc#L557-L578)); pages of 100 (`:585`); format detection ([`:590-614`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_gc.cc#L590-L614)): not yet transitioned → `gc_list` (omap), `cls_version_read`; version 1 with no entries → list one non-expired, none → transitioned; version 0 with ENOENT or none → done. Transitioned → `rgw_gc_queue_list_entries` ([`:616-626`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_gc.cc#L616-L626)). Per entry: stop at the time budget ([`:645-648`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_gc.cc#L645-L648)); each chain object: `refcount put(info.tag, implicit)` with `pool_full_try` and locator, up to `rgw_gc_max_concurrent_io` (10) in flight ([`:656-700`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_gc.cc#L656-L700), [`:382-404`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_gc.cc#L382-L404)), ENOENT counts as success (`:413-415`); omap-era: a tag is removed with `gc_remove` once its objects are gone, flushed `rgw_gc_max_trim_chunk` (16) at a time ([`:446-465`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_gc.cc#L446-L465), [`:493-523`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_gc.cc#L493-L523)); queue-era: after draining a page's ios, `rgw_gc_queue_remove_entries(entries.size())` ([`:703-716`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_gc.cc#L703-L716)); unlock (`:723`). The processor never drains at shutdown (`:720-722`).

### Copy

`RGWRados::copy_obj` ([`:4667-5032`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L4667-L5032)): source read with the copy conditionals (`x-amz-copy-source-if-*`, [`:4732-4745`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L4732-L4745)); an encrypted source is 501 at v19.2.6 ([`:4755-4763`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L4755-L4763)), while v20.2.4 drops the crypt attrs ([`:5034-5041`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5034-L5041)), forces the streamed path ([`:5124-5126`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5124-L5126)) and decrypts the source through the copy's data-processor factory ([rgw_op.cc:5793-5805](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5793-L5805), [:5846-5866](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5846-L5866); W-D13); attrs: the destination ACL replaces the source's, `delete_at`, retention and legal hold are dropped and re-added from the request, OLH attrs dropped in an unversioned bucket, replication attrs dropped (v20.2.4 also `storage_class`, [`:5028`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L5028)), then `set_copy_attrs` (`COPY`: attrs = source attrs entirely; `REPLACE`: the request's attrs plus the source's etag and tail_tag, [`:4583-4609`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4583-L4609)), then idtag, pg_ver and source_zone dropped and compression kept ([`:4787-4794`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4787-L4794)). `copy_data` when there is no manifest, the tail placement rule or pool differs, the source has no tail, or its head is larger than the chunk size ([`:4830-4849`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4830-L4849)); otherwise `copy_first` reads the head data separately ([`:4959-4965`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L4959-L4965)) and every tail gets `refcount get(tag + NUL, implicit)` in parallel, `rgw_max_copy_obj_concurrent_io` (10) at a time ([`:4906-4945`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L4906-L4945)), the manifest is copied with `set_head(dest rule, dest obj, head length)` and `tail_placement.bucket` filled from the source bucket when empty ([`:4913-4917`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L4913-L4917), [`:4965-4967`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L4965-L4967)), and `write_meta` runs with `ptag = tag`, `modify_tail = !copy_itself`, `keep_tail = copy_itself` ([`:4970-4983`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L4970-L4983)); a failure rolls the references back with `refcount put(tag + NUL, implicit)` under `pool_full_try` ([`:4990-5031`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L4990-L5031)). The S3 layer: `x-amz-copy-source` `[/]bucket/key[?versionId=v]` decoded once ([`rgw_op.cc:5329-5374`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5329-L5374)), `x-amz-metadata-directive` `COPY` or `REPLACE` ([`rgw_rest_s3.cc:3525-3537`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L3525-L3537)), self-copy without REPLACE and with the same storage class is 400 InvalidRequest ([`:3540-3548`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L3540-L3548), [`:3553-3564`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L3553-L3564)), `rgw_max_put_size` on the source size → 400 EntityTooLarge, quota against the destination ([`rgw_op.cc:5627-5636`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5627-L5636)), the source needs `s3:GetObject` and the destination `s3:PutObject` ([`:5451-5491`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5451-L5491)); the response is a chunked `<CopyObjectResult>` opened by `send_partial_response(0)` and closed with `<LastModified>` and the quoted `<ETag>` ([`:3566-3603`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L3566-L3603)); the `<Progress>` elements `rgw_copy_obj_progress` enables are emitted only by `fetch_remote_obj`, which the local branch never calls ([`:4726-4736`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L4726-L4736), `copy_obj_data` takes no callback [`:5034-5048`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5034-L5048)), so a same-zone copy carries none.

### The subresource writes

`set_attrs` ([`:6593-6760`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6593-L6760)): `append_atomic_test` adds `cmpxattr(idtag)` when the state has an idtag ([`:6612`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6612)); rmxattrs then setxattrs ([`:6626-6650`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6626-L6650)); guarded prepare ADD with a random tag and the new idtag + NUL ([`:6664-6676`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6664-L6676)); `mtime2` with the state's mtime, which the SAL layer has already nudged by one nanosecond ([`rgw_sal_rados.cc:2378-2390`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L2378-L2390)); complete ADD with the entry rebuilt from the state ([`:6697-6725`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6697-L6725): owner from the new or existing ACL, etag, content_type, storage_class, size, accounted size); failure → cancel ([`:6727-6731`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6727-L6731)). PutObjectTagging maps ECANCELED to 409 TagConflict ([`rgw_op.cc:1104-1106`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L1104-L1106)); DeleteObjectTagging answers 204 ([`rgw_rest_s3.cc:827-836`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L827-L836)); PutObjectAcl refuses a canned ACL together with a body ([`rgw_op.cc:5843-5846`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5843-L5846)), an owner change ([`:5859-5863`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5859-L5863), 403 AccessDenied through EPERM) and more than `rgw_acl_grants_max_num` grants ([`:5865-5880`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5865-L5880)).

### Reshard

`UpdateIndex::guard_reshard` ([`:7024-7077`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L7024-L7077)): up to 10 attempts; on -2300 `block_while_resharding` polls the shard's reshard status and waits, re-reading the bucket instance when resharding has finished ([`:7773-7932`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L7773-L7932)); a finished reshard resets the attempt counter and the shard is recomputed from the new layout. The completion manager's retry runs the same guard ([`:905-931`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L905-L931)). From v20.2.0 the class enforces the guard itself on `bucket_prepare_op` and `bucket_complete_op` (v20.2.4 `cls_rgw.cc` `guard_bucket_resharding`, in `rgw_bucket_prepare_op` and `rgw_bucket_complete_op`) and can answer -2300 during the log-record phase past `rgw_reshardlog_threshold`; rgw-go still sends the guard op and handles -2300 the same way on both releases.

## File structure

```
internal/radosclient/flags.go                    modify: OpFlagFullTry
internal/radosclient/cluster.go                  modify: Pool gains RequiredAlignment
internal/radosclient/goceph/translate.go         modify: OpFlagFullTry -> rados.OperationFullTry
internal/radosclient/goceph/pool.go              modify: RequiredAlignment over IOContext.RequiresAlignment/Alignment
internal/radosclient/radosclientfakes/           regenerate: FakeCluster, add FakePool
internal/cls/rgw/ops_gc.go, types_gc.go          modify: GCList, GCRemove, GCDeferEntry and their types
internal/meta/attrs.go                           modify: the attr names W writes or strips
internal/meta/bucket_id.go                       modify: Key()
internal/meta/manifest_write.go                  NewTrivialManifest, SetObjSize, TailStripe, TailObj, SetHead: the writer-side generator (Tasks 3, 8)
internal/op/object.go                            modify: PutParams.Tag, PutParams.ContentMD5, DeleteParams.UnmodifiedSince/IfMatchSize/IfMatchLastModified, CopyParams.Tag (StatsStore.AdjustStats is M's, W-D7)
internal/op/{putobject,deleteobject,deleteobjects,copyobject,objectattrs}.go   the write ops
internal/memstore/object.go, stats.go            modify: Tag honoured, AdjustStats
internal/s3/object.go                            modify: objectHandlers() gains the seven write routes
internal/s3/{putobject,deleteobject,deleteobjects,copyobject,objectattrs}.go   handlers and XML
internal/driver/store.go                         modify: the `w *writer` field and its initialisation in Open
internal/driver/writer.go                        writer: options, pools, tags, zone trace, short zone id
internal/driver/indexop.go                       index shard selection (over M's index.go helpers), prepare, complete, cancel, the reshard guard, block_while_resharding
internal/driver/completion.go                    completion manager and retry worker
internal/cls/rgw/ops_index.go, const.go          modify: GetBucketResharding (cls_rgw_get_bucket_resharding)
internal/testutil/fakerados/cls_rgw.go           modify: bucket_prepare_op, bucket_complete_op, get_bucket_resharding emulation (M's RGWClass grows)
internal/driver/stripe.go                        stripe writer: window, chunking, tail naming, the written set
internal/driver/headwrite.go                     write_meta: assume-no-entry pass, EEXIST retry, cancel semantics
internal/driver/put.go, delete.go, attrs.go, copy.go   (delete.go also holds deleteObjIndex, Task 6 Step 6)
internal/driver/list.go                          modify (M's): checkDiskState gains the multipart-part delete_obj_index sweep (Task 6)
internal/driver/gc_enqueue.go                    chain, split, enqueue with fallback, inline delete (Task 5)
internal/driver/gc.go                            initialize, the worker, per-shard processing
internal/driver/{writer,indexop,completion,stripe,headwrite,put,delete,attrs,copy,gc_enqueue,gc}_test.go
internal/driver/shape_test.go                    the op-composition parity spec against T's seam shapes (Task 13)
internal/testutil/fakerados/cls_gc.go, cls_refcount.go   new: GCQueueClass, RefcountClass emulators (Tasks 5, 9)
internal/op/readconds.go, errors.go              modify: CheckReadConditions exported, ErrMetadataNameTooLong (Task 10)
internal/s3/requestattrs.go                      requestAttrs, formatXattr: the header-to-xattr rules (Task 11)
test/integration/write_test.go                   [cluster] the reverse oracle (Task 12)
test/s3tests/w-groups.txt                        the s3-tests patterns the W gate is judged by (Task 13)
docs/exclusions.md                               modify: the reshard-lock recovery (Task 4), the tombstone cache (Task 6), W-D13's copy differences (Task 8)
docs/ceph-upstream-bugs.md                       modify: the one-nanosecond mtime quirk, the unused window option
docs/cgo-limitations.md                          modify: pool alignment binding, aio cancel note
hack/rooket/populate.sh                          modify: nothing (T owns it); Task 12 reads its manifest
```

## Task index

| # | Task | Depends on | Cluster |
|---|---|---|---|
| 1 | Seam: `OpFlagFullTry`, `Pool.RequiredAlignment`, `FakePool` | G Task 2 | integration spec `[cluster]` |
| 2 | `cls/rgw` omap-era GC methods: `GCList`, `GCRemove`, `GCDeferEntry` | none | integration spec `[cluster]` |
| 3 | `meta` additions: attr names, `BucketID.Key`, the writer-side manifest | none | no |
| 4 | `driver`: writer state, shard selection, index prepare/complete/cancel, the reshard guard, the completion manager and retry worker; `rgw.GetBucketResharding` | 1, 3, G Task 8, M Tasks 1, 5, 6, 12 (zone, `GetBucketInstance`, `indexPool`, `floorShards`, fakerados `RGWClass`) | no |
| 5 | `driver`: stripe writer, the atomic head write, GC enqueue, `PutObject`; `PutParams.Tag`, `PutParams.ContentMD5` | 3, 4, R Task 3 (`readHead`, `dataPool`, `rawRef`) | no |
| 6 | `driver`: `DeleteObject`; `deleteObjIndex` and the multipart-part sweep in M's `checkDiskState` | 4, 5, M Task 8 (`checkDiskState`), R Task 1 (`Manifest.Seek`) | no |
| 7 | `driver`: `SetObjectAttrs` | 4 | no |
| 8 | `driver`: `CopyObject` | 5, 6, R (`StatObject`, `ReadObject`, `readStored`) | no |
| 9 | `driver`: the GC worker | 2, 6 | no |
| 10 | `op`: `PutObject`, `DeleteObject`, `DeleteObjects`, `CopyObject`, `PutObjectACL`, `PutObjectTagging`, `DeleteObjectTagging`, conditional PUT | G Task 1, Z (`acl` XML, `tags`) | no |
| 11 | `s3`: handlers, XML documents, registration | 10, G Tasks 3-4 | no |
| 12 | Integration: the reverse oracle with `radosgw-admin object stat`, `bucket check`, `gc list` and a radosgw read-back | 5-9, M, R | `[cluster]` |
| 13 | Gate: s3-tests PUT, DELETE, COPY, multi-object-delete and `conditional_write` groups; the write microbench | 11, 12, T Tasks 4-5, 12 | `[cluster]` |

Tasks 1, 2 and 3 are independent and start together. Tasks 4 to 9 are the driver in dependency order; 5 and 7 can run in parallel once 4 merges, 6 after 5, 8 and 9 after 5 and 6. Task 10 needs only G's contract and Z's parsers and can start with Task 4. Task 11 follows 10. Tasks 12 and 13 close the unit.

---

### Task 1: Seam: `OpFlagFullTry`, `Pool.RequiredAlignment`, `FakePool`

**Files:**
- Modify: `internal/radosclient/flags.go`, `internal/radosclient/cluster.go`, `internal/radosclient/ops_test.go`, `internal/radosclient/goceph/translate.go`, `internal/radosclient/goceph/translate_test.go`, `internal/radosclient/goceph/pool.go`, `internal/radosclient/goceph/goceph_integration_test.go`
- Regenerate: `internal/radosclient/radosclientfakes/` (G Task 8 created it for `Cluster`; this task adds `Pool`)

**Interfaces:**
- Consumes: G Task 2's goceph (limiter, `acquire`/`finish` handle discipline in `pool.go`); go-ceph's `IOContext.RequiresAlignment` and `IOContext.Alignment` (upstream, [`rados/ioctx_pool_requires_alignment.go:16`](https://github.com/jhoblitt/go-ceph/blob/cbf97f85fcf5000ca4beca4a7fbf2ee746fa965d/rados/ioctx_pool_requires_alignment.go#L16), [`rados/ioctx_pool_alignment.go:16`](https://github.com/jhoblitt/go-ceph/blob/cbf97f85fcf5000ca4beca4a7fbf2ee746fa965d/rados/ioctx_pool_alignment.go#L16)) and `rados.OperationFullTry` (upstream, [`rados/operation_flags.go:30`](https://github.com/jhoblitt/go-ceph/blob/cbf97f85fcf5000ca4beca4a7fbf2ee746fa965d/rados/operation_flags.go#L30)). **No fork PR**: every binding exists on the pinned fork commit, so the pin stays. If a later step finds one missing after all, the flow is G Task 2's: branch from `jhoblitt/rgw-go` in `/home/jhoblitt/github/go-ceph`, one `//go:build ceph_preview` file per C function with a testify test, `make api-update`, PR into the fork's `rgw-go` branch, then `go mod edit -replace github.com/ceph/go-ceph=github.com/jhoblitt/go-ceph@<new head>` and `go mod tidy` with the sandbox disabled.
- Produces:

```go
package radosclient

// OpFlagFullTry is LIBRADOS_OPERATION_FULL_TRY (librados.h:130): the op may
// run against a full pool or one past its quota, as radosgw sets through
// set_pool_full_try on every deletion and refcount put.
const OpFlagFullTry OpFlags = 1 << 6

// Pool gains:
//	// RequiredAlignment is the pool's required write alignment in bytes, 0
//	// when it has none: rados_ioctx_pool_required_alignment2 when
//	// rados_ioctx_pool_requires_alignment2 says so. radosgw rounds its chunk
//	// and stripe sizes down to it (RGWRados::get_max_chunk_size).
//	RequiredAlignment(ctx context.Context) (uint64, error)
```

```go
package radosclientfakes // FakeCluster and FakePool, counterfeiter-generated
```

- [ ] **Step 1: Write the failing specs**

`internal/radosclient/ops_test.go` gains:

```go
It("defines OpFlagFullTry as librados's bit", func() {
	Expect(uint32(radosclient.OpFlagFullTry)).To(Equal(uint32(64)))
})
```

`internal/radosclient/goceph/translate_test.go` gains, in the flags table:

```go
Entry("full try", radosclient.OpFlagFullTry, rados.OperationFullTry),
Entry("full try with return vec", radosclient.OpFlagFullTry|radosclient.OpFlagReturnVec, rados.OperationFullTry|rados.OperationReturnVec),
```

`goceph_integration_test.go` gains, in the per-mode `Context`:

```go
It("reports no required alignment on the replicated test pool", func(ctx SpecContext) {
	a, err := pool.RequiredAlignment(ctx)
	Expect(err).NotTo(HaveOccurred())
	Expect(a).To(BeZero())
})
It("runs a remove with OpFlagFullTry", func(ctx SpecContext) {
	w := radosclient.NewWriteOp()
	w.WriteFull([]byte("x"))
	_, err := pool.Write(ctx, "fulltry-"+uniq, w, 0)
	Expect(err).NotTo(HaveOccurred())
	w = radosclient.NewWriteOp()
	w.Remove()
	_, err = pool.Write(ctx, "fulltry-"+uniq, w, radosclient.OpFlagFullTry)
	Expect(err).NotTo(HaveOccurred())
})
```

- [ ] **Step 2: Run to fail, implement**

`flags.go`: add the constant with the doc comment above, between `OpFlagIgnoreCache` and `OpFlagReturnVec`. `translate.go`: add `{radosclient.OpFlagFullTry, rados.OperationFullTry}` to the flag table so `translateFlags` accepts it. `cluster.go`: add `RequiredAlignment` to `Pool` and a `//counterfeiter:generate . Pool` directive beside G's `Cluster` one. `pool.go`:

```go
// RequiredAlignment asks librados for the pool's alignment; a pool that
// reports it needs none is 0.
func (p *pool) RequiredAlignment(ctx context.Context) (uint64, error) {
	h, err := p.state.acquire("required alignment", "")
	if err != nil {
		return 0, err
	}
	defer h.finish()
	need, err := h.ioctx.RequiresAlignment()
	if err != nil {
		return 0, fmt.Errorf("goceph: requires alignment: %w", translateErr(err))
	}
	if !need {
		return 0, nil
	}
	a, err := h.ioctx.Alignment()
	if err != nil {
		return 0, fmt.Errorf("goceph: alignment: %w", translateErr(err))
	}
	return a, nil
}
```

(`acquire`/`finish` and `translateErr` are the names phase 0's `pool.go` uses; follow the file.) Then `make generate` to produce `radosclientfakes/fake_pool.go` and refresh `fake_cluster.go`.

Run `go test -tags=ceph_preview ./internal/radosclient/...`; expected PASS.

- [ ] **Step 3: Integration `[cluster]`**

```sh
make cluster-up RELEASE=squid
make integration RELEASE=squid
```

Expected: the two new specs pass in all three completion modes. Reuse a running Squid cluster if `podman ps --filter label=rgw-go.release` shows one (task-14-carries).

- [ ] **Step 4: Commit**

`make check`, then `feat(radosclient): add OpFlagFullTry and Pool.RequiredAlignment to the seam`.

---

### Task 2: `cls/rgw` omap-era GC methods: `GCList`, `GCRemove`, `GCDeferEntry`

**Files:**
- Modify: `internal/cls/rgw/ops_gc.go`, `internal/cls/rgw/types_gc.go`, `internal/cls/rgw/const.go`, `internal/cls/rgw/ops_test.go`, `internal/cls/rgw/fixtures_test.go`, `internal/cls/rgw/goldens_test.go`, `internal/cls/rgw/rgw_integration_test.go`, `internal/cls/gc/types.go` (aliases), `hack/goldens/types.txt`

**Interfaces:**
- Consumes: `clsutil.Encode`, `clsutil.DecodeReply`, `denc`, `radosclient.Execer`.
- Produces:

```go
package rgw

// The omap-era gc methods, cls_rgw_const.h: RGW_GC_LIST, RGW_GC_REMOVE, RGW_GC_DEFER_ENTRY.
const (
	methodGCList       = "gc_list"
	methodGCRemove     = "gc_remove"
	methodGCDeferEntry = "gc_defer_entry"
)

// GCListOp is cls_rgw_gc_list_op, ENCODE_START(2, 1): marker, max, expired_only (v2).
type GCListOp struct {
	Marker      string
	Max         uint32
	ExpiredOnly bool
}
// GCListRet is cls_rgw_gc_list_ret, ENCODE_START(2, 1): entries, next_marker (v2), truncated.
type GCListRet struct {
	Entries    []GCObjInfo
	NextMarker string
	Truncated  bool
}
// GCRemoveOp is cls_rgw_gc_remove_op, ENCODE_START(1, 1): tags as a vector<string>.
type GCRemoveOp struct{ Tags []string }
// GCDeferEntryOp is cls_rgw_gc_defer_entry_op, ENCODE_START(1, 1).
type GCDeferEntryOp struct {
	ExpirationSecs uint32
	Tag            string
}

// GCListResult is the pending reply of GCList.
type GCListResult struct{ res *radosclient.ExecResult }
func (l *GCListResult) Result() (GCListRet, error)

// GCList mirrors cls_rgw_gc_list on a read op: up to max entries after marker,
// only expired ones when expiredOnly. Unexpired entries still count toward
// max, so page on Truncated and NextMarker, never on the reply's length.
func GCList(op *radosclient.ReadOp, marker string, max uint32, expiredOnly bool, r denc.Release) *GCListResult
// GCRemove mirrors cls_rgw_gc_remove: it removes the entries named by tags.
func GCRemove(op radosclient.Execer, tags []string, r denc.Release)
// GCDeferEntry mirrors cls_rgw_gc_defer_entry: it moves tag's entry to now plus expirationSecs.
func GCDeferEntry(op radosclient.Execer, expirationSecs uint32, tag string, r denc.Release)
```

`internal/cls/gc/types.go`: `type ListOp = rgw.GCListOp` and `type ListRet = rgw.GCListRet` (the rgw_gc class reuses cls_rgw's request and reply structs, `cls_rgw_gc_client.cc`), with their `Encode`/`Decode` moved to `rgw` and `gc.DecodeListRet` kept as a thin alias so the gc goldens keep compiling.

- [ ] **Step 1: Write the failing marshalling specs**

`ops_test.go`:

```go
It("encodes GCList as cls_rgw_gc_list_op and decodes the reply", func() {
	op := radosclient.NewReadOp()
	res := rgw.GCList(op, "m", 128, true, denc.Squid)
	step := op.Steps()[0].(*radosclient.ExecStep)
	Expect(step.Class).To(Equal("rgw"))
	Expect(step.Method).To(Equal("gc_list"))
	Expect(rgw.DecodeGCListOp(denc.NewDecoder(step.In))).To(Equal(rgw.GCListOp{Marker: "m", Max: 128, ExpiredOnly: true}))
	want := rgw.GCListRet{Entries: []rgw.GCObjInfo{{Tag: "t\x00"}}, NextMarker: "n", Truncated: true}
	step.Result.Set(clsutilEncode(want), 0)
	got, err := res.Result()
	Expect(err).NotTo(HaveOccurred())
	Expect(got).To(Equal(want))
})
It("encodes GCRemove and GCDeferEntry", func() {
	op := radosclient.NewWriteOp()
	rgw.GCRemove(op, []string{"a\x00", "b\x00"}, denc.Squid)
	rgw.GCDeferEntry(op, 7200, "a\x00", denc.Squid)
	steps := op.Steps()
	Expect(steps[0].(*radosclient.ExecStep).Method).To(Equal("gc_remove"))
	Expect(rgw.DecodeGCRemoveOp(denc.NewDecoder(steps[0].(*radosclient.ExecStep).In)).Tags).To(Equal([]string{"a\x00", "b\x00"}))
	Expect(steps[1].(*radosclient.ExecStep).Method).To(Equal("gc_defer_entry"))
	Expect(rgw.DecodeGCDeferEntryOp(denc.NewDecoder(steps[1].(*radosclient.ExecStep).In))).To(Equal(rgw.GCDeferEntryOp{ExpirationSecs: 7200, Tag: "a\x00"}))
})
```

`fixtures_test.go` gains hand-built byte fixtures for `cls_rgw_gc_remove_op` with two tags and `cls_rgw_gc_defer_entry_op`, built field by field (struct header `1, 1`, length; u32 count then length-prefixed strings; u32 then string), plus a mutation check on `ExpirationSecs`.

- [ ] **Step 2: Run to fail, implement the four types and three methods**

Encoders follow phase 0's pattern (`e.BeginStruct(v, compat)` … `e.EndStruct(f)`, `denc.EncodeSlice(e, tags, (*denc.Encoder).String)`); decoders read `h.Version >= 2` fields conditionally. Move `ListOp`/`ListRet` from `gc/types.go` into `rgw/types_gc.go` as `GCListOp`/`GCListRet` and leave aliases behind.

Run `go test -tags=ceph_preview ./internal/cls/...`; expected PASS.

- [ ] **Step 3: Goldens**

Append to `hack/goldens/types.txt`: `cls_rgw_gc_list_op`, `cls_rgw_gc_list_ret`, `cls_rgw_gc_remove_op`, `cls_rgw_gc_defer_entry_op` with destination `internal/cls/rgw` (ceph-dencoder lists them among the rgw types; if `make goldens` reports one unknown, keep the hand-built fixture for it and say so in the PR). `make goldens` with the sandbox disabled; existing goldens must regenerate byte-identically; `goldens_test.go` gains the four entries.

- [ ] **Step 4: Integration `[cluster]`**

`rgw_integration_test.go` gains, on a fresh oid `gcw-<uniq>` in the test pool:

```go
It("lists, defers and removes an omap-era gc entry", func(ctx SpecContext) {
	info := rgw.GCObjInfo{Tag: "tag-" + uniq + "\x00", Chain: []rgw.GCObj{{Pool: cephtest.TestPool, Key: rgw.ObjKey{Name: "tail-" + uniq}}}}
	w := radosclient.NewWriteOp()
	rgw.GCSetEntry(w, 0, info, rel)
	_, err := pool.Write(ctx, oid, w, 0)
	Expect(err).NotTo(HaveOccurred())

	r := radosclient.NewReadOp()
	res := rgw.GCList(r, "", 100, false, rel)
	_, err = pool.Read(ctx, oid, r, 0)
	Expect(err).NotTo(HaveOccurred())
	ret, err := res.Result()
	Expect(err).NotTo(HaveOccurred())
	Expect(ret.Entries).To(HaveLen(1))
	Expect(ret.Entries[0].Tag).To(Equal(info.Tag))

	w = radosclient.NewWriteOp()
	rgw.GCDeferEntry(w, 3600, info.Tag, rel)
	_, err = pool.Write(ctx, oid, w, 0)
	Expect(err).NotTo(HaveOccurred())
	r = radosclient.NewReadOp()
	res = rgw.GCList(r, "", 100, true, rel) // expired only: the deferred entry is not due
	_, err = pool.Read(ctx, oid, r, 0)
	Expect(err).NotTo(HaveOccurred())
	ret, _ = res.Result()
	Expect(ret.Entries).To(BeEmpty())

	w = radosclient.NewWriteOp()
	rgw.GCRemove(w, []string{info.Tag}, rel)
	_, err = pool.Write(ctx, oid, w, 0)
	Expect(err).NotTo(HaveOccurred())
	r = radosclient.NewReadOp()
	res = rgw.GCList(r, "", 100, false, rel)
	_, err = pool.Read(ctx, oid, r, 0)
	Expect(err).NotTo(HaveOccurred())
	ret, _ = res.Result()
	Expect(ret.Entries).To(BeEmpty())
})
```

`make integration RELEASE=squid` and `RELEASE=tentacle` (the omap methods exist on both).

- [ ] **Step 5: Commit**

`make check`, then `feat(cls/rgw): add the omap-era gc_list, gc_remove and gc_defer_entry methods`.

---

### Task 3: `meta` additions: attr names, `BucketID.Key`, the writer-side manifest

**Files:**
- Modify: `internal/meta/attrs.go`, `internal/meta/bucket_id.go`, `internal/meta/placement.go`, `internal/meta/bucket_test.go`
- Create: `internal/meta/manifest_write.go`, `internal/meta/manifest_write_test.go`

**Interfaces:**
- Consumes: `meta.Manifest`, `meta.Manifest.Stripes`, `meta.Obj`, `meta.PlacementRule`, `meta.BucketID.key`.
- Produces (R may have added some of these names already; keep whichever landed first, once):

```go
package meta

// Attr names the write path writes or strips, from rgw_common.h:80-179.
const (
	AttrTags              = AttrPrefix + "x-amz-tagging"
	AttrDeleteAt          = AttrPrefix + "delete_at"
	AttrShadowObj         = AttrPrefix + "shadow_name"
	AttrOLHPrefix         = AttrPrefix + "olh."
	AttrOLHInfo           = AttrOLHPrefix + "info"
	AttrOLHVer            = AttrOLHPrefix + "ver"
	AttrOLHIDTag          = AttrOLHPrefix + "idtag"
	AttrCryptPrefix       = AttrPrefix + "crypt."
	AttrCryptMode         = AttrCryptPrefix + "mode"
	AttrObjectRetention   = AttrPrefix + "object-retention"
	AttrObjectLegalHold   = AttrPrefix + "object-legal-hold"
	AttrReplicationStatus = AttrPrefix + "amz-replication-status"
	AttrReplicationTrace  = AttrPrefix + "replication-trace"
	AttrReplicatedAt      = AttrPrefix + "replicated-at"
	AttrAppendPartNum     = AttrPrefix + "append_part_num"
	AttrCloudTierType     = AttrPrefix + "cloud_tier_type"
	AttrCloudTierConfig   = AttrPrefix + "cloud_tier_config"
	AttrCacheControl      = AttrPrefix + "cache_control"
	AttrContentDisp       = AttrPrefix + "content_disposition"
	AttrContentEnc        = AttrPrefix + "content_encoding"
	AttrContentLang       = AttrPrefix + "content_language"
	AttrExpires           = AttrPrefix + "expires"
	AttrXRobotsTag        = AttrPrefix + "x-robots-tag"
	AttrUserManifest      = AttrPrefix + "user_manifest"
	AttrSLOManifest       = AttrPrefix + "slo_manifest"
	AttrWebsiteRedirect   = AttrPrefix + "x-amz-website-redirect-location"
	AttrTorrent           = AttrPrefix + "torrent"
)

// Key is rgw_bucket::get_key with its default delimiters: "tenant/name:id",
// the tenant part only when set and the id only when set. It is the location
// key radosgw puts in every prepare and complete's zones_trace.
func (b BucketID) Key() string

// InheritFrom is rgw_placement_rule::inherit_from: an empty Name or
// StorageClass takes r's.
func (p PlacementRule) InheritFrom(r PlacementRule) PlacementRule

// NewTrivialManifest is the manifest AtomicObjectProcessor::prepare leaves
// after set_trivial_rule and generator::create_begin, before any data: head at
// size 0, one rule starting at offset maxHeadSize (set_trivial_rule builds
// RGWObjManifestRule(0, tail_ofs, 0, stripe_max_size), rgw_obj_manifest.h:259-263;
// the corpus manifest squid-large.json carries start_ofs 4194304) with no
// part size and stripeSize stripes, the tail placement tailRule in head's
// bucket, the tail instance head's, and prefix as the tail name prefix ("." +
// 31 characters of gen_rand_alphanumeric's url-safe base64 alphabet + "_" for
// a plain PUT: rgw_obj_manifest.cc:239-248 fills a 33-byte buffer as 31
// characters and a NUL). maxHeadSize is the head chunk size, 0 when the tail pool
// differs or the placement has inline_data off.
func NewTrivialManifest(head Obj, headRule, tailRule PlacementRule, prefix string, maxHeadSize, stripeSize uint64) Manifest

// SetObjSize is generator::create_next at the final offset: ObjSize = size and
// HeadSize = min(size, MaxHeadSize).
func (m *Manifest) SetObjSize(size uint64)

// TailStripe is the stripe index generator::create_next assigns to offset
// ofs, which must be at least MaxHeadSize: (ofs-MaxHeadSize)/StripeMaxSize,
// plus one when the head holds data.
func (m Manifest) TailStripe(ofs uint64) uint64

// TailObj is get_implicit_location for part 0 and stripe n: key
// "<prefix><n>" in namespace shadow, instance TailInstance, in the tail
// placement's bucket.
func (m Manifest) TailObj(n uint64) Obj
```

- [ ] **Step 1: Write the failing specs**

`manifest_write_test.go`:

```go
var _ = Describe("NewTrivialManifest", func() {
	bucket := meta.BucketID{Name: "plain", Marker: "m1", ID: "m1"}
	head := meta.Obj{Bucket: bucket, Key: meta.ObjKey{Name: "k"}}
	rule := meta.PlacementRule{Name: "default-placement"}
	build := func(size uint64, maxHead uint64) meta.Manifest {
		m := meta.NewTrivialManifest(head, rule, rule, ".RANDOMRANDOMRANDOMRANDOMRANDOM12_", maxHead, 4<<20)
		m.SetObjSize(size)
		return m
	}
	It("lays a 10 MiB object out as head plus two tails, as Stripes reads it back", func() {
		m := build(10<<20, 4<<20)
		Expect(m.HeadSize).To(BeEquivalentTo(4 << 20))
		Expect(m.MaxHeadSize).To(BeEquivalentTo(4 << 20))
		Expect(m.Rules).To(Equal(map[uint64]meta.ManifestRule{0: {StartOfs: 4 << 20, StripeMaxSize: 4 << 20}}), "set_trivial_rule: start_ofs is the max head size")
		st, err := m.Stripes()
		Expect(err).NotTo(HaveOccurred())
		Expect(st).To(HaveLen(3))
		Expect(st[0].InHead).To(BeTrue())
		Expect(st[1].Obj).To(Equal(m.TailObj(1)))
		Expect(st[1].OID()).To(Equal("m1__shadow_.RANDOMRANDOMRANDOMRANDOMRANDOM12_1"))
		Expect(st[2].OID()).To(Equal("m1__shadow_.RANDOMRANDOMRANDOMRANDOMRANDOM12_2"))
		Expect(st[2].Size).To(BeEquivalentTo(2 << 20))
		Expect(m.TailStripe(4 << 20)).To(BeEquivalentTo(1))
		Expect(m.TailStripe(8<<20 + 1)).To(BeEquivalentTo(2))
	})
	It("keeps an object of exactly the head size in its head", func() {
		st, err := build(4<<20, 4<<20).Stripes()
		Expect(err).NotTo(HaveOccurred())
		Expect(st).To(HaveLen(1))
	})
	It("keeps an empty object in an empty head", func() {
		m := build(0, 4<<20)
		Expect(m.HeadSize).To(BeZero())
		st, err := m.Stripes()
		Expect(err).NotTo(HaveOccurred())
		Expect(st).To(HaveLen(1))
	})
	It("numbers stripes from 0 when the head holds no data", func() {
		m := build(5<<20, 0)
		Expect(m.HeadSize).To(BeZero())
		Expect(m.Rules[0].StartOfs).To(BeZero())
		Expect(m.TailStripe(0)).To(BeZero())
		Expect(m.TailObj(0).Key.Name).To(Equal(".RANDOMRANDOMRANDOMRANDOMRANDOM12_0"))
		st, err := m.Stripes()
		Expect(err).NotTo(HaveOccurred())
		Expect(st[0].OID()).To(HaveSuffix("_0"))
	})
	It("inherits the tail rule from the head rule", func() {
		m := meta.NewTrivialManifest(head, meta.PlacementRule{Name: "p", StorageClass: "STANDARD"}, meta.PlacementRule{StorageClass: "COLD"}, ".x_", 0, 4<<20)
		Expect(m.TailPlacement.PlacementRule).To(Equal(meta.PlacementRule{Name: "p", StorageClass: "COLD"}))
		Expect(m.TailPlacement.Bucket).To(Equal(bucket))
		Expect(m.HeadPlacementRule).To(Equal(meta.PlacementRule{Name: "p", StorageClass: "STANDARD"}))
	})
	It("round-trips through the encoder without a tail bucket or tail instance", func() {
		m := build(10<<20, 4<<20)
		e := denc.NewEncoder()
		m.Encode(e, denc.Squid)
		got := meta.DecodeManifest(denc.NewDecoder(e.Bytes()))
		Expect(got).To(Equal(m))
	})
	It("rebuilds a radosgw-written manifest byte for byte", func() {
		// The plain-PUT manifest golden from the corpus: decode it, read
		// its prefix, size and rule, build the same shape here, and compare bytes.
		raw := goldentest.Bytes("RGWObjManifest", "plain-put-12M")
		want := meta.DecodeManifest(denc.NewDecoder(raw))
		m := meta.NewTrivialManifest(want.Obj, want.HeadPlacementRule, want.TailPlacement.PlacementRule, want.Prefix, want.MaxHeadSize, want.Rules[0].StripeMaxSize)
		m.SetObjSize(want.ObjSize)
		e := denc.NewEncoder()
		m.Encode(e, denc.Squid)
		Expect(e.Bytes()).To(Equal(raw))
	})
})
```

`bucket_test.go` gains `Key()` entries: `{Name: "b"}` → `"b"`, `{Tenant: "t", Name: "b", ID: "id"}` → `"t/b:id"`, `{Name: "b", ID: "id"}` → `"b:id"`.

- [ ] **Step 2: Run to fail, implement**

`manifest_write.go`:

```go
func NewTrivialManifest(head Obj, headRule, tailRule PlacementRule, prefix string, maxHeadSize, stripeSize uint64) Manifest {
	m := NewManifest()
	m.Obj = head
	m.HeadPlacementRule = headRule
	m.MaxHeadSize = maxHeadSize
	m.Prefix = prefix
	m.Rules = map[uint64]ManifestRule{0: {StartOfs: maxHeadSize, StripeMaxSize: stripeSize}}
	m.TailPlacement = BucketPlacement{Bucket: head.Bucket, PlacementRule: tailRule.InheritFrom(headRule)}
	m.TailInstance = head.Key.Instance
	return m
}

func (m *Manifest) SetObjSize(size uint64) {
	m.ObjSize = size
	m.HeadSize = min(size, m.MaxHeadSize)
}

func (m Manifest) TailStripe(ofs uint64) uint64 {
	rule := m.Rules[0]
	n := (ofs - m.MaxHeadSize) / rule.StripeMaxSize
	if m.MaxHeadSize > 0 {
		n++
	}
	return n
}

func (m Manifest) TailObj(n uint64) Obj {
	return Obj{
		Bucket: m.TailPlacement.Bucket,
		Key:    ObjKey{Name: m.Prefix + strconv.FormatUint(n, 10), NS: NSShadow, Instance: m.TailInstance},
	}
}
```

`SetObjSize` refuses nothing: `create_next` only checks that offsets go forward, and the writer calls it once at the end. Add `InheritFrom` to `placement.go` and `Key` to `bucket_id.go` (`Key()` is `b.key('/', ':')`, the two delimiters `rgw_bucket::get_key` defaults to). The manifest golden `internal/meta/testdata/manifests/squid-large.json` is the plain-PUT manifest Step 3 asks for (obj_size 10485760, max_head_size 4194304, rules[0].start_ofs 4194304, stripe_max_size 4194304): the byte-for-byte spec reads `squid-large.bin` through `goldentest` and need not stay Pending.

- [ ] **Step 3: The corpus fixture**

Find a plain-PUT manifest among the phase 0 goldens under `internal/meta/testdata` (a `RGWObjManifest` from an object of the populated `plain` bucket larger than 4 MiB). If every corpus manifest is multipart or explicit, the byte-for-byte spec stays `Pending` with the reason in its text and Task 12 Step 3 records a fixture from `radosgw-admin object stat` on the Squid cluster (`.manifest` is JSON there, so it takes the raw `user.rgw.manifest` bytes from `rados getxattr`); then un-pend it. Run `go test -tags=ceph_preview ./internal/meta/`; expected PASS.

- [ ] **Step 4: Commit**

`make check`, then `feat(meta): add the writer-side manifest, BucketID.Key and the write-path attr names`.

---
### Task 4: `driver`: writer state, index prepare/complete/cancel, the reshard guard, the completion manager and retry worker; `rgw.GetBucketResharding`

**Files:**
- Create: `internal/driver/writer.go`, `internal/driver/indexop.go`, `internal/driver/completion.go`, `internal/driver/writer_test.go`, `internal/driver/indexop_test.go`, `internal/driver/completion_test.go`
- Modify: `internal/driver/store.go` (the `w *writer` field; `Open` builds it after M's zone resolution and pool cache and registers the `index-completions` worker), `internal/driver/export_test.go` (test exports), `internal/cls/rgw/const.go` and `internal/cls/rgw/ops_index.go` (`GetBucketResharding`), `internal/cls/rgw/ops_test.go`, `internal/testutil/fakerados/cls_rgw.go` (M's `RGWClass` gains `bucket_prepare_op`, `bucket_complete_op`, `get_bucket_resharding`), `internal/testutil/fakerados/cls_rgw_test.go`, `docs/exclusions.md` (the reshard-lock recovery block, Step 7)

**Interfaces:**
- Consumes: G's `driver.Store`, `Store.AddWorker`, `Store.Zone()`, `Store.Period()`, `op.BucketRecord`, `op.FromRADOS`, `op.ErrNotImplemented`; M's `Store.pools`, `Store.indexPool(ctx, info)` (Task 6), `Store.GetBucketInstance(ctx, id)` (Task 5), `floorShards` (Task 12), `fakerados.Cluster` (`RegisterClass`, `Object`, `Put`, `Writes`, `LastWrite`), `fakerados.RGWClass`, the `seedRookZone`, `conf` and `seedShardHeader` spec helpers; `meta.IndexShard`, `BucketInfo.IndexShardOID`, `meta.HashMod`, `BucketID.Key()` (Task 3), `ObjKey.IndexKeyName`, `ObjKey.Locator`, `meta.Period.PeriodMap.ShortZoneIDs`, `meta.Zone.LogData`; `rgw.PrepareOp`, `CompleteOp`, `EntryVer`, `DirEntryMeta`, `InstanceEntry`, `GuardBucketResharding`, `BucketPrepareOp`, `BucketCompleteOp`, `OpAdd`, `OpDel`, `OpCancel`, `CategoryMain`, `CategoryNone`; `radosclient.Pool.Read/Write`, `ErrBusyResharding`, `ErrNotFound`; `cephconf.Options`; `denc.Release` (`Squid`, `Tentacle`).
- Produces:

```go
package rgw // internal/cls/rgw

const methodGetBucketResharding = "get_bucket_resharding" // RGW_GET_BUCKET_RESHARDING, cls_rgw_const.h:80

// GetBucketReshardingResult is the pending reply of GetBucketResharding.
type GetBucketReshardingResult struct{ res *radosclient.ExecResult }

// Result decodes cls_rgw_get_bucket_resharding_ret, ENCODE_START(1, 1)
// around a cls_rgw_bucket_instance_entry (cls_rgw_ops.h:1692-1706).
func (r *GetBucketReshardingResult) Result() (InstanceEntry, error)

// GetBucketResharding mirrors cls_rgw_get_bucket_resharding on a read op
// (cls_rgw_client.cc:1176-1197): the shard header's reshard entry. The
// request is an empty ENCODE_START(1, 1) struct (cls_rgw_ops.h:1675-1689).
func GetBucketResharding(op *radosclient.ReadOp, r denc.Release) *GetBucketReshardingResult
```

```go
package driver

// writeOptions are the rgw_* tunables the write path and its workers read
// once at Open; the defaults are rgw.yaml.in's, identical at v19.2.6 and
// v20.2.4.
type writeOptions struct {
	putWindow          uint64        // rgw_put_obj_min_window_size, 16 MiB; radosgw never reads rgw_put_obj_max_window_size
	stripeSize         uint64        // rgw_obj_stripe_size, 4 MiB
	chunkSize          uint64        // rgw_max_chunk_size, 4 MiB
	maxPutSize         uint64        // rgw_max_put_size, 5 GiB: a streamed body past it is ErrEntityTooLarge (RGWPutObj_ObjStore::get_data, rgw_rest.cc:1096-1098)
	gcMaxObjs          uint32        // rgw_gc_max_objs, 32: floorShards, then min(n, 65521) as RGWGC::initialize's rgw_shards_max()
	gcObjMinWait       uint32        // rgw_gc_obj_min_wait, 7200 s
	gcProcessorMaxTime time.Duration // rgw_gc_processor_max_time, 1 h
	gcProcessorPeriod  time.Duration // rgw_gc_processor_period, 1 h
	gcMaxConcurrentIO  int           // rgw_gc_max_concurrent_io, 10
	gcMaxTrimChunk     int           // rgw_gc_max_trim_chunk, 16
	gcMaxQueueSize     uint64        // rgw_gc_max_queue_size, 131068 KiB
	gcMaxDeferred      uint64        // rgw_gc_max_deferred, 50
	gcThreads          bool          // rgw_enable_gc_threads, true
	multiObjDelMaxAIO  int           // rgw_multi_obj_del_max_aio, 16, floored to 1 as RGWDeleteMultiObj::execute does
	copyConcurrentIO   int           // rgw_max_copy_obj_concurrent_io, 10
}

func readWriteOptions(conf *cephconf.Options) (writeOptions, error)

// writer is the write path's state on Store.
type writer struct {
	opts        writeOptions
	zoneID      string        // svc.zone->get_zone().id: the zone half of every zones_trace entry
	shortZoneID uint32        // RGWPeriodMap::get_zone_short_id(zone id): the period map's entry, 0 when absent (rgw_zone.cc:432-439)
	logData     bool          // need_to_log_data(): the zone's log_data; the default sync module exports data (svc_zone.cc:718-721)
	gcShards    []string      // "gc.0" … "gc.<n-1>" (RGWGC::initialize, rgw_gc.cc:38-42)
	reshardWait time.Duration // RGWReshardWait's duration, 5 s (rgw_reshard.h:266); specs shorten it
	completions *completionManager
	rand        func(n int) string // randAlnum; specs swap it for a fixed sequence
}

func newWriter(s *Store, opts writeOptions) *writer

// randAlnum is gen_rand_alphanumeric (src/common/random_string.cc:45-51): n
// characters from "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_".
func randAlnum(n int) string

// randTag is append_rand_alpha(cct, "", tag, 32) (rgw_common.h:1621-1627):
// "_" and 31 random characters, the tag set_attrs, delete and copy_obj_data
// generate when the request brings none.
func (w *writer) randTag() string

// indexShard is RGWRados::BucketShard: one index shard object with its pool
// handle and shard id, -1 for an unsharded index.
type indexShard struct {
	pool radosclient.Pool
	oid  string
	id   int32
}

// indexShardFor is BucketShard::init + RGWSI_BucketIndex_RADOS::open_bucket_index_shard:
// the shard of info's current index layout that holds key. The hash object
// is the key's name (rgw_obj::get_hash_object; multipart parts hash
// their upload key instead).
func (s *Store) indexShardFor(ctx context.Context, info *meta.BucketInfo, key meta.ObjKey) (indexShard, error)

// indexOp is RGWRados::Bucket::UpdateIndex for one object write: the pending
// index entry under one tag, prepared before the head write and completed or
// cancelled after it, every class call behind the reshard guard.
type indexOp struct {
	s        *Store
	rec      *op.BucketRecord // the instance the shard was resolved from; refreshed after a reshard
	obj      meta.Obj
	tag      string
	blind    bool // an indexless layout: every method is a no-op
	shard    *indexShard
	prepared bool
}

func (s *Store) newIndexOp(rec *op.BucketRecord, key meta.ObjKey, tag string) *indexOp
func (x *indexOp) prepare(ctx context.Context, mod rgw.ModifyOp) error   // UpdateIndex::prepare, synchronous
func (x *indexOp) complete(ver rgw.EntryVer, m rgw.DirEntryMeta)         // UpdateIndex::complete: ADD, not awaited
func (x *indexOp) completeDel(ver rgw.EntryVer, removedMtime time.Time)   // UpdateIndex::complete_del: DEL, not awaited
func (x *indexOp) cancel()                                               // UpdateIndex::cancel: CANCEL at pool -1, epoch 0, not awaited
func (x *indexOp) guardReshard(ctx context.Context, call func(sh indexShard) error) error
func (s *Store) blockWhileResharding(ctx context.Context, x *indexOp, sh indexShard) error

// completionManager is RGWIndexCompletionManager: bucket_complete_op writes
// issued off the request path, and a retry worker for the ones a reshard refused.
type completionManager struct { /* s, ctx, cancel, mu, queue []completion, wake chan struct{}, wg, inflight atomic.Int64 */ }

func newCompletionManager(s *Store) *completionManager
func (m *completionManager) submit(x *indexOp, c rgw.CompleteOp)
func (m *completionManager) run(ctx context.Context) error // the AddWorker body
func (m *completionManager) pending() int                   // in flight plus queued, for specs and the integration tests
```

`export_test.go` adds `type IndexOp = indexOp`, `func (s *Store) NewIndexOpForTest(rec *op.BucketRecord, key meta.ObjKey, tag string) *IndexOp`, `func (x *IndexOp) Prepare(ctx context.Context, mod rgw.ModifyOp) error`, `func (x *IndexOp) Complete(ver rgw.EntryVer, m rgw.DirEntryMeta)`, `CompleteDel`, `Cancel()`, `func (s *Store) PendingCompletionsForTest() int`, `func (s *Store) SetReshardWaitForTest(d time.Duration)`, `func (s *Store) SetRandForTest(f func(int) string)`, `func (s *Store) WriteOptionsForTest() WriteOptions` (`type WriteOptions = writeOptions`), `func RandAlnumForTest(n int) string`.

The request shapes this task fixes, transcribed from [`rgw_rados.cc:9456-9523`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L9456-L9523) (v20.2.4 [`:10395-10455`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L10395-L10455)):

- **prepare**: `AssertExists`; `GuardBucketResharding` (-2300); `bucket_prepare_op{Op, Key{IndexKeyName, Instance}, Tag, Locator}`; on Squid also `LogOp = w.logData` and `ZonesTrace = [zoneID + ":" + bucket.Key()]`; on Tentacle neither (its `cls_rgw_bucket_prepare_op` takes no log arguments, [`cls_rgw_client.cc:164-175`](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw_client.cc#L164-L175)).
- **complete** (ADD, DEL and CANCEL alike): `AssertExists`; guard; `bucket_complete_op{Op, Key, Locator, Ver, Meta, Tag, LogOp = w.logData, BILogFlags 0, RemoveObjs nil, ZonesTrace = [zoneID:bucketKey]}` on both releases. ADD carries the entry's `DirEntryMeta` with `Category` Main; DEL carries `{Category: None, Mtime: removed mtime}`; CANCEL carries `{Category: None}` and `Ver{Pool: -1, Epoch: 0}` whatever the object's state (`cls_obj_complete_cancel`, [`:9553-9563`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L9553-L9563); v20.2.4 [`:10485-10495`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L10485-L10495)).
- **reshard**: `guardReshard` is `UpdateIndex::guard_reshard` ([`:7024-7077`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L7024-L7077)): ten attempts, `ErrBusyResharding` → `blockWhileResharding`, which is `RGWRados::block_while_resharding` ([`:7773-7935`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L7773-L7935)) without its reshard-lock recovery block (`:7855-7919`, v20.2.4 `:8807-8861`: under the bucket's reshard lock it clears a flag an interrupted reshard left; an operator runs `radosgw-admin reshard cancel` instead, a difference Step 7 records): up to ten `get_bucket_resharding` reads of the shard header, `reshardWait` apart; a status that is not busy (Squid: `!= IN_PROGRESS`, `resharding_in_progress()`; Tentacle: `== NOT_RESHARDING`, `resharding()`), or a vanished shard object, re-reads the bucket instance (`GetBucketInstance`; the instance id survives a reshard, only its layout generation moves) into `x.rec`, drops the cached shard and returns nil, after which `guardReshard` resets its counter and recomputes the shard; the tenth busy poll returns `ErrBusyResharding`, which the op layer maps through `FromRADOS` to `ErrUnknown` (500), radosgw's answer for an unmapped `ERR_BUSY_RESHARDING` ([`rgw_common.h:331`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.h#L331); no row in `rgw_common.cc`'s S3 table, [`:51-144`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.h#L51-L144)).
- **completion manager**: `submit` runs the guarded complete in its own goroutine under the manager's lifetime context and never blocks the request; `ErrBusyResharding` queues the completion for `run`, which is `RGWIndexCompletionManager::process` ([`:875-938`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L875-L938)): a fresh `GetBucketInstance`, a recomputed shard, the complete reissued under `guardReshard`; any other error, on either path, is logged and the completion dropped (`handle_completion` [`:994-1023`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L994-L1023), `process` [`:929-933`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L929-L933)), leaving the pending entry for M's listing reconciliation. When `run`'s context ends it cancels the in-flight writes' context and waits for their goroutines; nothing is drained (radosgw's `finalize` abandons them too).

- [ ] **Step 1: `rgw.GetBucketResharding` spec and implementation**

`internal/cls/rgw/ops_test.go` gains:

```go
It("encodes GetBucketResharding as an empty struct and decodes the entry", func() {
	op := radosclient.NewReadOp()
	res := rgw.GetBucketResharding(op, denc.Squid)
	step := op.Steps()[0].(*radosclient.ExecStep)
	Expect(step.Class).To(Equal("rgw"))
	Expect(step.Method).To(Equal("get_bucket_resharding"))
	Expect(step.In).To(Equal([]byte{1, 1, 0, 0, 0, 0}), "ENCODE_START(1, 1) with an empty body")
	e := denc.NewEncoder()
	f := e.BeginStruct(1, 1)
	rgw.InstanceEntry{ReshardStatus: 1}.Encode(e, denc.Squid)
	e.EndStruct(f)
	step.Result.Set(e.Bytes(), 0)
	got, err := res.Result()
	Expect(err).NotTo(HaveOccurred())
	Expect(got.ReshardStatus).To(BeEquivalentTo(1))
})
```

`const.go`: `methodGetBucketResharding = "get_bucket_resharding"`. `ops_index.go`:

```go
// getBucketReshardingOp is cls_rgw_get_bucket_resharding_op: ENCODE_START(1, 1), no fields.
type getBucketReshardingOp struct{}

func (getBucketReshardingOp) Encode(e *denc.Encoder, _ denc.Release) { e.EndStruct(e.BeginStruct(1, 1)) }

// GetBucketReshardingResult is the pending reply of GetBucketResharding.
type GetBucketReshardingResult struct{ res *radosclient.ExecResult }

// Result decodes cls_rgw_get_bucket_resharding_ret (cls_rgw_ops.h:1692-1706).
func (r *GetBucketReshardingResult) Result() (InstanceEntry, error) {
	return clsutil.DecodeReply(r.res, Class, methodGetBucketResharding, func(d *denc.Decoder) InstanceEntry {
		h := d.BeginStruct(1)
		i := DecodeInstanceEntry(d)
		d.EndStruct(h)
		return i
	})
}

// GetBucketResharding mirrors cls_rgw_get_bucket_resharding on a read op
// (cls_rgw_client.cc:1176-1197): the shard header's reshard entry.
func GetBucketResharding(op *radosclient.ReadOp, r denc.Release) *GetBucketReshardingResult {
	return &GetBucketReshardingResult{res: op.Exec(Class, methodGetBucketResharding, clsutil.Encode(getBucketReshardingOp{}, r))}
}
```

Run `go test -tags=ceph_preview ./internal/cls/rgw/`; expected PASS. (`hack/goldens/types.txt` gains `cls_rgw_get_bucket_resharding_op` and `cls_rgw_get_bucket_resharding_ret` if ceph-dencoder lists them; otherwise the hand fixture above stands.)

- [ ] **Step 2: Extend `fakerados.RGWClass` with the index write methods**

`internal/testutil/fakerados/cls_rgw.go`: the `RGWClass` method switch gains three cases. The entry's omap key is `encode_obj_index_key`: the key name when the instance is empty; an instance is `-EOPNOTSUPP` here (versioning is phase 2). The header lives in `Object.OmapHdr`, entries in `Object.Omap`, both encoded at `denc.Squid` (the fake is not release-aware; the replies decode at either).

```go
case "bucket_prepare_op":
	return rgwPrepareOp(obj, in)
case "bucket_complete_op":
	return rgwCompleteOp(obj, in)
case "get_bucket_resharding":
	h, rc := dirHeader(obj)
	if rc < 0 {
		return nil, rc, false
	}
	e := denc.NewEncoder()
	f := e.BeginStruct(1, 1)
	h.NewInstance.Encode(e, denc.Squid)
	e.EndStruct(f)
	return e.Bytes(), 0, false
```

```go
func classErrno(n syscall.Errno) int32 { return -int32(n) }

// dirHeader is read_bucket_header: a missing or undecodable header is -EINVAL,
// as rgw_bucket_complete_op reports it (cls_rgw.cc:1063-1068).
func dirHeader(obj *Object) (rgwcls.DirHeader, int32) {
	if obj == nil || len(obj.OmapHdr) == 0 {
		return rgwcls.DirHeader{}, classErrno(syscall.EINVAL)
	}
	d := denc.NewDecoder(obj.OmapHdr)
	h := rgwcls.DecodeDirHeader(d)
	if d.Err() != nil {
		return rgwcls.DirHeader{}, classErrno(syscall.EINVAL)
	}
	return h, 0
}

func putDirHeader(obj *Object, h rgwcls.DirHeader) {
	e := denc.NewEncoder()
	h.Encode(e, denc.Squid)
	obj.OmapHdr = e.Bytes()
}

func readEntry(obj *Object, idx string) (rgwcls.DirEntry, bool) {
	b, ok := obj.Omap[idx]
	if !ok {
		return rgwcls.DirEntry{}, false
	}
	return rgwcls.DecodeDirEntry(denc.NewDecoder(b)), true
}

func writeEntry(obj *Object, idx string, en rgwcls.DirEntry) {
	e := denc.NewEncoder()
	en.Encode(e, denc.Squid)
	if obj.Omap == nil {
		obj.Omap = map[string][]byte{}
	}
	obj.Omap[idx] = e.Bytes()
}

// roundedSize is cls_rgw_get_rounded_size: up to the next 4 KiB.
func roundedSize(n uint64) uint64 { return (n + 4095) &^ 4095 }

// unaccount is unaccount_entry (cls_rgw.cc:887-898).
func unaccount(h *rgwcls.DirHeader, en rgwcls.DirEntry) {
	if !en.Exists {
		return
	}
	st := h.Stats[en.Meta.Category]
	st.NumEntries--
	st.TotalSize -= en.Meta.AccountedSize
	st.TotalSizeRounded -= roundedSize(en.Meta.AccountedSize)
	st.ActualSize -= en.Meta.Size
	h.Stats[en.Meta.Category] = st
}

// rgwPrepareOp is rgw_bucket_prepare_op (cls_rgw.cc:923-1000 at v20.2.4, the
// same body without the class-side guard at v19.2.6): an empty tag is
// -EINVAL; a missing entry is created unset; the tag joins pending_map with
// state PENDING_MODIFY and the op.
func rgwPrepareOp(obj *Object, in []byte) ([]byte, int32, bool) {
	d := denc.NewDecoder(in)
	p := rgwcls.DecodePrepareOp(d)
	if d.Err() != nil || p.Tag == "" {
		return nil, classErrno(syscall.EINVAL), false
	}
	if _, rc := dirHeader(obj); rc < 0 {
		return nil, rc, false
	}
	if p.Key.Instance != "" {
		return nil, classErrno(syscall.EOPNOTSUPP), false
	}
	idx := p.Key.Name
	en, ok := readEntry(obj, idx)
	if !ok {
		en = rgwcls.NewDirEntry()
		en.Key = p.Key
		en.Locator = p.Locator
	}
	en.PendingMap = append(en.PendingMap, rgwcls.PendingEntry{Tag: p.Tag, Info: rgwcls.PendingInfo{State: rgwcls.PendingModify, Timestamp: time.Now(), Op: uint8(p.Op)}})
	writeEntry(obj, idx, en)
	return nil, 0, false
}

// rgwCompleteOp is rgw_bucket_complete_op (cls_rgw.cc:1040-1195 at v19.2.6):
// a tag that is not pending is -EINVAL; an epoch not above the entry's on the
// same pool turns the op into a cancel; the op's ver lands on the entry before
// any write-back (the defect of tracker #80894, reproduced on purpose).
func rgwCompleteOp(obj *Object, in []byte) ([]byte, int32, bool) {
	d := denc.NewDecoder(in)
	c := rgwcls.DecodeCompleteOp(d)
	if d.Err() != nil {
		return nil, classErrno(syscall.EINVAL), false
	}
	h, rc := dirHeader(obj)
	if rc < 0 {
		return nil, rc, false
	}
	if c.Key.Instance != "" {
		return nil, classErrno(syscall.EOPNOTSUPP), false
	}
	idx := c.Key.Name
	en, ondisk := readEntry(obj, idx)
	if !ondisk {
		en = rgwcls.NewDirEntry()
		en.Key, en.Ver, en.Meta, en.Locator = c.Key, c.Ver, c.Meta, c.Locator
	}
	en.IndexVer = h.Ver
	if c.Tag != "" {
		i := slices.IndexFunc(en.PendingMap, func(p rgwcls.PendingEntry) bool { return p.Tag == c.Tag })
		if i < 0 {
			return nil, classErrno(syscall.EINVAL), false
		}
		en.PendingMap = slices.Delete(en.PendingMap, i, i+1)
	}
	mod := c.Op
	if !(c.Tag != "" && mod == rgwcls.OpCancel) && c.Ver.Pool == en.Ver.Pool && c.Ver.Epoch != 0 && c.Ver.Epoch <= en.Ver.Epoch {
		mod = rgwcls.OpCancel
	}
	en.Ver = c.Ver
	switch mod {
	case rgwcls.OpCancel:
		if c.Tag != "" {
			if !en.Exists && len(en.PendingMap) == 0 {
				delete(obj.Omap, idx)
			} else {
				writeEntry(obj, idx, en)
			}
		}
	case rgwcls.OpDel:
		unaccount(&h, en)
		en.Meta = c.Meta
		switch {
		case !ondisk:
		case len(en.PendingMap) == 0:
			delete(obj.Omap, idx)
		default:
			en.Exists = false
			writeEntry(obj, idx, en)
		}
	case rgwcls.OpAdd:
		unaccount(&h, en)
		en.Meta, en.Key, en.Exists, en.Tag = c.Meta, c.Key, true, c.Tag
		if h.Stats == nil {
			h.Stats = map[uint8]rgwcls.CategoryStats{}
		}
		st := h.Stats[c.Meta.Category]
		st.NumEntries++
		st.TotalSize += c.Meta.AccountedSize
		st.TotalSizeRounded += roundedSize(c.Meta.AccountedSize)
		st.ActualSize += c.Meta.Size
		h.Stats[c.Meta.Category] = st
		writeEntry(obj, idx, en)
	default:
		return nil, classErrno(syscall.EINVAL), false
	}
	putDirHeader(obj, h)
	return nil, 0, false
}
```

`Cluster` gains `Entry(pool, ns, oid, key string) (rgwcls.DirEntry, bool)` (decodes `Omap[key]`) and `Header(pool, ns, oid string) rgwcls.DirHeader` beside M's `Suggestions`. `cls_rgw_test.go` pins: prepare then complete ADD leaves an existing entry with the meta, tag and `Stats[Main]` of one object; a complete with an unknown tag is `EINVAL`; a complete at epoch 5 after one at 10 (same pool) is turned into a cancel and leaves `Ver.Epoch` 5 with 10's meta (tracker #80894's defect); a CANCEL with `-1:0` on an existing entry leaves `Ver{-1, 0}` and a following complete at epoch 3 applies (the same defect reached through radosgw's own cancel); a CANCEL on a never-completed key removes the map entry; DEL on an entry with another tag pending keeps it with `Exists false`.

Run `go test -tags=ceph_preview ./internal/testutil/fakerados/`; expected PASS.

- [ ] **Step 3: Write the failing `writer` specs**

`internal/driver/writer_test.go`, package `driver_test`, on `driver.Open` over a `fakerados.Cluster` seeded with `seedRookZone(c, "ceph-objectstore", true)` (`captureLog` is M Task 12's helper):

```go
var _ = Describe("write options", func() {
	It("reads radosgw's defaults", func(ctx SpecContext) {
		s, err := driver.Open(ctx, c, conf(nil), driver.Options{})
		Expect(err).NotTo(HaveOccurred())
		o := s.WriteOptionsForTest()
		Expect(o.PutWindow).To(BeEquivalentTo(16 << 20))
		Expect(o.StripeSize).To(BeEquivalentTo(4 << 20))
		Expect(o.ChunkSize).To(BeEquivalentTo(4 << 20))
		Expect(o.MaxPutSize).To(BeEquivalentTo(5 << 30))
		Expect(o.GCMaxObjs).To(BeEquivalentTo(32))
		Expect(o.GCObjMinWait).To(BeEquivalentTo(7200))
		Expect(o.GCProcessorMaxTime).To(Equal(time.Hour))
		Expect(o.GCProcessorPeriod).To(Equal(time.Hour))
		Expect(o.GCMaxConcurrentIO).To(Equal(10))
		Expect(o.GCMaxTrimChunk).To(Equal(16))
		Expect(o.GCMaxQueueSize).To(BeEquivalentTo(131068 << 10))
		Expect(o.GCMaxDeferred).To(BeEquivalentTo(50))
		Expect(o.GCThreads).To(BeTrue())
		Expect(o.MultiObjDelMaxAIO).To(Equal(16))
		Expect(o.CopyConcurrentIO).To(Equal(10))
	})
	It("floors a zero rgw_gc_max_objs to one and clamps above rgw_shards_max", func(ctx SpecContext) {
		var buf bytes.Buffer
		restore := captureLog(&buf)
		defer restore()
		s, err := driver.Open(ctx, c, conf(map[string]string{"rgw_gc_max_objs": "0"}), driver.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(s.WriteOptionsForTest().GCMaxObjs).To(BeEquivalentTo(1))
		Expect(buf.String()).To(ContainSubstring("rgw_gc_max_objs"))
		Expect(buf.String()).To(ContainSubstring("80991"))
		s, err = driver.Open(ctx, c, conf(map[string]string{"rgw_gc_max_objs": "70000"}), driver.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(s.WriteOptionsForTest().GCMaxObjs).To(BeEquivalentTo(65521), "rgw_gc.cc:35 min(rgw_gc_max_objs, rgw_shards_max())")
	})
	It("floors rgw_multi_obj_del_max_aio to one", func(ctx SpecContext) {
		s, err := driver.Open(ctx, c, conf(map[string]string{"rgw_multi_obj_del_max_aio": "0"}), driver.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(s.WriteOptionsForTest().MultiObjDelMaxAIO).To(Equal(1))
	})
})

var _ = Describe("randAlnum", func() {
	It("draws from gen_rand_alphanumeric's url-safe alphabet", func() {
		for range 64 {
			s := driver.RandAlnumForTest(31)
			Expect(s).To(HaveLen(31))
			Expect(s).To(MatchRegexp(`^[A-Za-z0-9_-]+$`))
		}
	})
})
```

`conf(nil)`'s fake config must carry the fifteen options above with their defaults (the `cephconf.MapGetter` M's `conf` helper fills; extend its map).

- [ ] **Step 4: Write the failing `indexOp` specs**

`internal/driver/indexop_test.go`, package `driver_test`. Fixture: `c := fakerados.New()` with `VersionClass`, `UserClass` and `RGWClass` registered, `seedRookZone(c, "ceph-objectstore", true)`, and a bucket record whose eleven index shards exist with empty headers:

```go
const indexPool = "ceph-objectstore.rgw.buckets.index"

// testBucket is a Rook-shaped bucket record: eleven Mod shards at generation 0.
func testBucket(id string, shards uint32) *op.BucketRecord {
	info := meta.NewBucketInfo()
	info.Bucket = meta.BucketID{Name: "plain", Marker: id, ID: id}
	info.Owner = meta.UserOwner(meta.UserID{ID: "alice"})
	info.PlacementRule = meta.PlacementRule{Name: "default-placement"}
	info.Layout = meta.NewBucketLayout()
	info.Layout.Current.Layout.Normal.NumShards = shards
	return &op.BucketRecord{Info: info}
}

func seedShards(c *fakerados.Cluster, rec *op.BucketRecord) {
	gen := rec.Info.Layout.Current
	n := max(gen.Layout.Normal.NumShards, 1)
	for i := range n {
		seedShardHeader(c, indexPool, rec.Info.IndexShardOID(gen, i), rgwcls.DirHeader{Stats: map[uint8]rgwcls.CategoryStats{}})
	}
}

func shardOf(rec *op.BucketRecord, name string) string {
	sh, _ := meta.IndexShard(name, rec.Info.Layout.Current.Layout.Normal.NumShards)
	return rec.Info.IndexShardOID(rec.Info.Layout.Current, sh)
}

var _ = Describe("indexOp", func() {
	var (
		c   *fakerados.Cluster
		s   *driver.Store
		rec *op.BucketRecord
		key = meta.ObjKey{Name: "k"}
	)
	BeforeEach(func(ctx SpecContext) {
		c = fakerados.New()
		c.RegisterClass("version", fakerados.VersionClass())
		c.RegisterClass("user", fakerados.UserClass())
		c.RegisterClass("rgw", fakerados.RGWClass())
		seedRookZone(c, "ceph-objectstore", true)
		var err error
		s, err = driver.Open(ctx, c, conf(nil), driver.Options{})
		Expect(err).NotTo(HaveOccurred())
		s.SetReshardWaitForTest(time.Millisecond)
		rec = testBucket("zone-ceph-objectstore.4156.1", 11)
		seedShards(c, rec)
	})

	It("prepares on the shard meta.IndexShard names, guarded, with Squid's zones_trace", func(ctx SpecContext) {
		x := s.NewIndexOpForTest(rec, key, "tx0000000000000000000001-0068d7a1b2-4155-ceph-objectstore")
		Expect(x.Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
		oid := shardOf(rec, "k")
		Expect(c.Writes(indexPool, "", oid)).To(Equal(1))
		steps := c.LastWrite(indexPool, "", oid).Steps()
		Expect(steps).To(HaveLen(3))
		Expect(steps[0]).To(BeAssignableToTypeOf(&radosclient.AssertExistsStep{}))
		guard := steps[1].(*radosclient.ExecStep)
		Expect(guard.Method).To(Equal("guard_bucket_resharding"))
		Expect(rgwcls.DecodeGuardOp(denc.NewDecoder(guard.In)).RetErr).To(BeEquivalentTo(-2300))
		prep := steps[2].(*radosclient.ExecStep)
		Expect(prep.Method).To(Equal("bucket_prepare_op"))
		p := rgwcls.DecodePrepareOp(denc.NewDecoder(prep.In))
		Expect(p).To(Equal(rgwcls.PrepareOp{Op: rgwcls.OpAdd, Key: rgwcls.ObjKey{Name: "k"}, Tag: "tx0000000000000000000001-0068d7a1b2-4155-ceph-objectstore",
			ZonesTrace: []string{s.Zone().ID + ":plain:zone-ceph-objectstore.4156.1"}}), "rgw_rados.cc:9456-9479; log_op false in a single zone")
		en, ok := c.Entry(indexPool, "", oid, "k")
		Expect(ok).To(BeTrue())
		Expect(en.PendingMap).To(HaveLen(1))
		Expect(en.Exists).To(BeFalse())
	})
	It("sends no zones_trace in a prepare on Tentacle", func(ctx SpecContext) {
		t := denc.Tentacle
		s2, err := driver.Open(ctx, c, conf(nil), driver.Options{Release: &t})
		Expect(err).NotTo(HaveOccurred())
		x := s2.NewIndexOpForTest(rec, key, "tag")
		Expect(x.Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
		prep := c.LastWrite(indexPool, "", shardOf(rec, "k")).Steps()[2].(*radosclient.ExecStep)
		p := rgwcls.DecodePrepareOp(denc.NewDecoder(prep.In))
		Expect(p.ZonesTrace).To(BeEmpty(), "v20.2.4 cls_rgw_client.cc:164-175")
		Expect(p.LogOp).To(BeFalse())
	})
	It("escapes and locates a key that starts with an underscore", func(ctx SpecContext) {
		x := s.NewIndexOpForTest(rec, meta.ObjKey{Name: "_u"}, "tag")
		Expect(x.Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
		prep := c.LastWrite(indexPool, "", shardOf(rec, "_u")).Steps()[2].(*radosclient.ExecStep)
		p := rgwcls.DecodePrepareOp(denc.NewDecoder(prep.In))
		Expect(p.Key.Name).To(Equal("__u"), "get_index_key_name escapes the leading underscore")
		Expect(p.Locator).To(Equal("_u"), "get_loc keeps the name as the locator")
	})
	It("uses the bare .dir.<id> object for an unsharded layout and nothing for an indexless one", func(ctx SpecContext) {
		flat := testBucket("zone-ceph-objectstore.4156.2", 0)
		seedShards(c, flat)
		Expect(s.NewIndexOpForTest(flat, key, "tag").Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
		Expect(c.Writes(indexPool, "", ".dir.zone-ceph-objectstore.4156.2")).To(Equal(1))

		blind := testBucket("zone-ceph-objectstore.4156.3", 0)
		blind.Info.Layout.Current.Layout.Type = meta.IndexIndexless
		Expect(s.NewIndexOpForTest(blind, key, "tag").Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
		Expect(c.Writes(indexPool, "", ".dir.zone-ceph-objectstore.4156.3")).To(BeZero(), "UpdateIndex::blind")
	})
	It("completes an ADD off the request path with the entry radosgw writes", func(ctx SpecContext) {
		x := s.NewIndexOpForTest(rec, key, "tag")
		Expect(x.Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
		mtime := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
		x.Complete(rgwcls.EntryVer{Pool: 7, Epoch: 42}, rgwcls.DirEntryMeta{Category: rgwcls.CategoryMain, Size: 5, AccountedSize: 5, Mtime: mtime, ETag: "5d41402abc4b2a76b9719d911017c592", Owner: "alice", OwnerDisplayName: "Alice", ContentType: "text/plain"})
		oid := shardOf(rec, "k")
		Eventually(func() int { return c.Writes(indexPool, "", oid) }).Should(Equal(2))
		steps := c.LastWrite(indexPool, "", oid).Steps()
		Expect(steps[0]).To(BeAssignableToTypeOf(&radosclient.AssertExistsStep{}))
		Expect(steps[1].(*radosclient.ExecStep).Method).To(Equal("guard_bucket_resharding"))
		comp := rgwcls.DecodeCompleteOp(denc.NewDecoder(steps[2].(*radosclient.ExecStep).In))
		Expect(comp.Op).To(Equal(rgwcls.OpAdd))
		Expect(comp.Tag).To(Equal("tag"))
		Expect(comp.Ver).To(Equal(rgwcls.EntryVer{Pool: 7, Epoch: 42}))
		Expect(comp.ZonesTrace).To(Equal([]string{s.Zone().ID + ":plain:zone-ceph-objectstore.4156.1"}))
		Expect(comp.LogOp).To(BeFalse())
		en, _ := c.Entry(indexPool, "", oid, "k")
		Expect(en.Exists).To(BeTrue())
		Expect(en.Meta.ETag).To(Equal("5d41402abc4b2a76b9719d911017c592"))
		Expect(en.PendingMap).To(BeEmpty())
		Expect(c.Header(indexPool, "", oid).Stats[rgwcls.CategoryMain].NumEntries).To(BeEquivalentTo(1))
		Eventually(s.PendingCompletionsForTest).Should(BeZero())
	})
	It("completes a DEL with the removed mtime under category None", func(ctx SpecContext) {
		x := s.NewIndexOpForTest(rec, key, "tag")
		Expect(x.Prepare(ctx, rgwcls.OpDel)).To(Succeed())
		mtime := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
		x.CompleteDel(rgwcls.EntryVer{Pool: 7, Epoch: 43}, mtime)
		oid := shardOf(rec, "k")
		Eventually(func() int { return c.Writes(indexPool, "", oid) }).Should(Equal(2))
		comp := rgwcls.DecodeCompleteOp(denc.NewDecoder(c.LastWrite(indexPool, "", oid).Steps()[2].(*radosclient.ExecStep).In))
		Expect(comp.Op).To(Equal(rgwcls.OpDel))
		Expect(comp.Meta).To(Equal(rgwcls.DirEntryMeta{Category: rgwcls.CategoryNone, Mtime: mtime}), "rgw_rados.cc:9536-9551")
		_, ok := c.Entry(indexPool, "", oid, "k")
		Expect(ok).To(BeFalse(), "a never-completed key is removed by the DEL")
	})
	It("cancels with pool -1 and epoch 0, as cls_obj_complete_cancel does", func(ctx SpecContext) {
		x := s.NewIndexOpForTest(rec, key, "tag")
		Expect(x.Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
		x.Cancel()
		oid := shardOf(rec, "k")
		Eventually(func() int { return c.Writes(indexPool, "", oid) }).Should(Equal(2))
		comp := rgwcls.DecodeCompleteOp(denc.NewDecoder(c.LastWrite(indexPool, "", oid).Steps()[2].(*radosclient.ExecStep).In))
		Expect(comp.Op).To(Equal(rgwcls.OpCancel))
		Expect(comp.Ver).To(Equal(rgwcls.EntryVer{Pool: -1, Epoch: 0}), "rgw_rados.cc:9553-9563")
		Expect(comp.Meta.Category).To(Equal(rgwcls.CategoryNone))
		_, ok := c.Entry(indexPool, "", oid, "k")
		Expect(ok).To(BeFalse(), "cls_rgw.cc:1097-1108: nothing existed and nothing else is pending")
	})
	It("resets an existing entry's version with its cancel, so a stale completion applies after it (tracker #80894)", func(ctx SpecContext) {
		first := s.NewIndexOpForTest(rec, key, "tag-1")
		Expect(first.Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
		lost := s.NewIndexOpForTest(rec, key, "tag-2")
		Expect(lost.Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
		stale := s.NewIndexOpForTest(rec, key, "tag-3")
		Expect(stale.Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
		oid := shardOf(rec, "k")
		first.Complete(rgwcls.EntryVer{Pool: 7, Epoch: 10}, rgwcls.DirEntryMeta{Category: rgwcls.CategoryMain, Size: 10, AccountedSize: 10, ETag: "ten"})
		Eventually(func() int { return c.Writes(indexPool, "", oid) }).Should(Equal(4))
		lost.Cancel()
		Eventually(func() int { return c.Writes(indexPool, "", oid) }).Should(Equal(5))
		en, _ := c.Entry(indexPool, "", oid, "k")
		Expect(en.Ver).To(Equal(rgwcls.EntryVer{Pool: -1, Epoch: 0}), "cls_rgw.cc:1094 copies the cancel's version onto the entry")
		Expect(en.Meta.ETag).To(Equal("ten"))
		stale.Complete(rgwcls.EntryVer{Pool: 7, Epoch: 5}, rgwcls.DirEntryMeta{Category: rgwcls.CategoryMain, Size: 5, AccountedSize: 5, ETag: "five"})
		Eventually(func() string { en, _ := c.Entry(indexPool, "", oid, "k"); return en.Meta.ETag }).Should(Equal("five"), "the pool comparison fails (cls_rgw.cc:1082-1083), so the older completion applies, as it does behind radosgw")
	})
	It("waits out a reshard on prepare and retries against the shard the fresh instance names", func(ctx SpecContext) {
		oid := shardOf(rec, "k")
		hdr := c.Header(indexPool, "", oid)
		hdr.NewInstance.ReshardStatus = 1 // IN_PROGRESS
		seedShardHeader(c, indexPool, oid, hdr)
		go func() {
			time.Sleep(20 * time.Millisecond)
			hdr.NewInstance.ReshardStatus = 0
			seedShardHeader(c, indexPool, oid, hdr)
		}()
		x := s.NewIndexOpForTest(rec, key, "tag")
		Expect(x.Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
		Expect(c.Writes(indexPool, "", oid)).To(Equal(2), "one refused prepare, one that landed")
		Expect(c.Reads(indexPool, "", oid)).To(BeNumerically(">=", 1), "get_bucket_resharding polls")
	})
	It("gives up after ten busy polls with ErrBusyResharding, which the op layer answers as radosgw's 500", func(ctx SpecContext) {
		oid := shardOf(rec, "k")
		hdr := c.Header(indexPool, "", oid)
		hdr.NewInstance.ReshardStatus = 1
		seedShardHeader(c, indexPool, oid, hdr)
		x := s.NewIndexOpForTest(rec, key, "tag")
		err := x.Prepare(ctx, rgwcls.OpAdd)
		Expect(err).To(MatchError(radosclient.ErrBusyResharding))
		Expect(op.FromRADOS(err, op.ScopeObject)).To(MatchError(op.ErrUnknown), "ERR_BUSY_RESHARDING has no S3 row (rgw_common.cc)")
		Expect(c.Reads(indexPool, "", oid)).To(Equal(10*10), "10 guard attempts × 10 polls")
	})
})
```

(`c.Reads` is M Task 4's per-object read counter.) On Tentacle the seventh spec also treats status 3 (IN_LOGRECORD) as busy: add a `DescribeTable("busy statuses per release", …)` with entries `(Squid, 1, busy)`, `(Squid, 2, free)`, `(Squid, 3, free)`, `(Tentacle, 1, busy)`, `(Tentacle, 2, busy)`, `(Tentacle, 3, busy)` that seeds the header status, prepares once, and expects `Writes` 2 (free: refused then landed after one poll) or `ErrBusyResharding` (busy) with `SetReshardWaitForTest(0)`.

- [ ] **Step 5: Write the failing `completionManager` specs**

`internal/driver/completion_test.go`, same fixture:

```go
var _ = Describe("completionManager", func() {
	It("retries a busy-resharding complete from the worker once the shard is free again", func(ctx SpecContext) {
		x := s.NewIndexOpForTest(rec, key, "tag")
		Expect(x.Prepare(ctx, rgwcls.OpAdd)).To(Succeed())
		oid := shardOf(rec, "k")
		hdr := c.Header(indexPool, "", oid)
		hdr.NewInstance.ReshardStatus = 1
		seedShardHeader(c, indexPool, oid, hdr)
		runCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- s.Run(runCtx) }()

		x.Complete(rgwcls.EntryVer{Pool: 7, Epoch: 1}, rgwcls.DirEntryMeta{Category: rgwcls.CategoryMain})
		Eventually(s.PendingCompletionsForTest).Should(Equal(1), "queued for the retry worker")
		Consistently(func() bool { en, ok := c.Entry(indexPool, "", oid, "k"); return ok && en.Exists }).WithTimeout(30 * time.Millisecond).Should(BeFalse())

		hdr.NewInstance.ReshardStatus = 0
		seedShardHeader(c, indexPool, oid, hdr)
		Eventually(func() bool { en, ok := c.Entry(indexPool, "", oid, "k"); return ok && en.Exists }).Should(BeTrue())
		Eventually(s.PendingCompletionsForTest).Should(BeZero())
		cancel()
		Eventually(done).Should(Receive(Succeed()))
	})
	It("drops a complete the class refuses for any other reason and logs it", func(ctx SpecContext) {
		var buf bytes.Buffer
		restore := captureLog(&buf)
		defer restore()
		x := s.NewIndexOpForTest(rec, key, "never-prepared")
		x.Complete(rgwcls.EntryVer{Pool: 7, Epoch: 1}, rgwcls.DirEntryMeta{Category: rgwcls.CategoryMain})
		Eventually(s.PendingCompletionsForTest).Should(BeZero())
		Expect(buf.String()).To(ContainSubstring("bucket index completion failed"))
		_, ok := c.Entry(indexPool, "", shardOf(rec, "k"), "k")
		Expect(ok).To(BeFalse(), "EINVAL: the tag was never pending (cls_rgw.cc:1069-1078)")
	})
	It("stops with Run and abandons what is in flight", func(ctx SpecContext) {
		runCtx, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- s.Run(runCtx) }()
		cancel()
		Eventually(done).Should(Receive(Succeed()))
	})
})
```

- [ ] **Step 6: Run to fail, then write `writer.go`, `indexop.go`, `completion.go`**

`writer.go`:

```go
package driver

// rgwShardsMax is rgw_shards_max(), RGW_SHARDS_PRIME_1 (rgw_tools.h:40-43).
const rgwShardsMax = 65521

func readWriteOptions(conf *cephconf.Options) (writeOptions, error) {
	var o writeOptions
	var err error
	read := func(name string, f func() error) {
		if err == nil {
			if e := f(); e != nil {
				err = fmt.Errorf("reading %s: %w", name, e)
			}
		}
	}
	var n int64
	read("rgw_put_obj_min_window_size", func() (e error) { o.putWindow, e = conf.Size("rgw_put_obj_min_window_size"); return })
	read("rgw_obj_stripe_size", func() (e error) { o.stripeSize, e = conf.Size("rgw_obj_stripe_size"); return })
	read("rgw_max_chunk_size", func() (e error) { o.chunkSize, e = conf.Size("rgw_max_chunk_size"); return })
	read("rgw_max_put_size", func() (e error) { o.maxPutSize, e = conf.Size("rgw_max_put_size"); return })
	read("rgw_gc_max_objs", func() (e error) { n, e = conf.Int64("rgw_gc_max_objs"); return })
	o.gcMaxObjs = min(floorShards("rgw_gc_max_objs", n), rgwShardsMax)
	read("rgw_gc_obj_min_wait", func() (e error) { n, e = conf.Int64("rgw_gc_obj_min_wait"); return })
	o.gcObjMinWait = uint32(max(n, 0)) //nolint:gosec // bounded by the option's range
	read("rgw_gc_processor_max_time", func() (e error) { n, e = conf.Int64("rgw_gc_processor_max_time"); return })
	o.gcProcessorMaxTime = time.Duration(n) * time.Second
	read("rgw_gc_processor_period", func() (e error) { n, e = conf.Int64("rgw_gc_processor_period"); return })
	o.gcProcessorPeriod = time.Duration(n) * time.Second
	read("rgw_gc_max_concurrent_io", func() (e error) { n, e = conf.Int64("rgw_gc_max_concurrent_io"); return })
	o.gcMaxConcurrentIO = int(max(n, 1))
	read("rgw_gc_max_trim_chunk", func() (e error) { n, e = conf.Int64("rgw_gc_max_trim_chunk"); return })
	o.gcMaxTrimChunk = int(max(n, 1))
	read("rgw_gc_max_queue_size", func() (e error) { o.gcMaxQueueSize, e = conf.Uint64("rgw_gc_max_queue_size"); return })
	read("rgw_gc_max_deferred", func() (e error) { o.gcMaxDeferred, e = conf.Uint64("rgw_gc_max_deferred"); return })
	read("rgw_enable_gc_threads", func() (e error) { o.gcThreads, e = conf.Bool("rgw_enable_gc_threads"); return })
	var u uint64
	read("rgw_multi_obj_del_max_aio", func() (e error) { u, e = conf.Uint64("rgw_multi_obj_del_max_aio"); return })
	o.multiObjDelMaxAIO = int(max(u, 1)) //nolint:gosec // bounded
	read("rgw_max_copy_obj_concurrent_io", func() (e error) { n, e = conf.Int64("rgw_max_copy_obj_concurrent_io"); return })
	o.copyConcurrentIO = int(max(n, 1))
	if err != nil {
		return writeOptions{}, err
	}
	if o.chunkSize == 0 || o.stripeSize == 0 || o.putWindow < o.chunkSize {
		return writeOptions{}, fmt.Errorf("driver: rgw_max_chunk_size %d, rgw_obj_stripe_size %d, rgw_put_obj_min_window_size %d: a zero size or a window below one chunk cannot write", o.chunkSize, o.stripeSize, o.putWindow)
	}
	return o, nil
}

const randAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"

func randAlnum(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand does not fail on Linux
	}
	for i := range b {
		b[i] = randAlphabet[int(b[i])%len(randAlphabet)]
	}
	return string(b)
}

func (w *writer) randTag() string { return "_" + w.rand(31) }

func newWriter(s *Store, opts writeOptions) *writer {
	zone := s.Zone()
	w := &writer{
		opts:        opts,
		zoneID:      zone.ID,
		shortZoneID: s.Period().PeriodMap.ShortZoneIDs[zone.ID],
		logData:     zone.LogData,
		reshardWait: 5 * time.Second,
		rand:        randAlnum,
	}
	for i := range opts.gcMaxObjs {
		w.gcShards = append(w.gcShards, gc.ShardOID(int(i)))
	}
	w.completions = newCompletionManager(s)
	return w
}
```

(`rand` is `crypto/rand`; the modulo over a 64-character alphabet is unbiased. `gc.ShardOID` is phase 0's `internal/cls/gc`.)

`indexop.go`:

```go
package driver

// reshardRetries is NUM_RESHARD_RETRIES (rgw_rados.cc:7031) and
// block_while_resharding's num_retries (:7819).
const reshardRetries = 10

func (s *Store) indexShardFor(ctx context.Context, info *meta.BucketInfo, key meta.ObjKey) (indexShard, error) {
	gen := info.Layout.Current
	normal := gen.Layout.Normal
	if normal.HashType != meta.HashMod {
		return indexShard{}, fmt.Errorf("%w: bucket %s index hash type %d", op.ErrNotImplemented, info.Bucket.Name, normal.HashType) // -ENOTSUP, svc_bi_rados.cc:270
	}
	pool, err := s.indexPool(ctx, info)
	if err != nil {
		return indexShard{}, err
	}
	sh := indexShard{pool: pool, id: -1}
	shard, ok := meta.IndexShard(key.Name, normal.NumShards)
	if ok {
		sh.id = int32(shard) //nolint:gosec // at most 65521
	}
	sh.oid = info.IndexShardOID(gen, shard)
	return sh, nil
}

func (s *Store) newIndexOp(rec *op.BucketRecord, key meta.ObjKey, tag string) *indexOp {
	if tag == "" {
		tag = s.w.randTag() // UpdateIndex::prepare: append_rand_alpha when no write tag
	}
	return &indexOp{
		s:     s,
		rec:   rec,
		obj:   meta.Obj{Bucket: rec.Info.Bucket, Key: key},
		tag:   tag,
		blind: rec.Info.Layout.Current.Layout.Type == meta.IndexIndexless,
	}
}

func (x *indexOp) currentShard(ctx context.Context) (indexShard, error) {
	if x.shard == nil {
		sh, err := x.s.indexShardFor(ctx, &x.rec.Info, x.obj.Key)
		if err != nil {
			return indexShard{}, err
		}
		x.shard = &sh
	}
	return *x.shard, nil
}

func (x *indexOp) clsKey() rgw.ObjKey {
	return rgw.ObjKey{Name: x.obj.Key.IndexKeyName(), Instance: x.obj.Key.Instance}
}

// zonesTrace is the rgw_zone_set every prepare and complete carries:
// zones_trace.insert(zone id, bucket.get_key()) (rgw_rados.cc:9469, :9500).
func (x *indexOp) zonesTrace() []string {
	return []string{x.s.w.zoneID + ":" + x.obj.Bucket.Key()}
}

// prepareOp is cls_obj_prepare_op's write op (rgw_rados.cc:9456-9479; v20.2.4 :10395-10411).
func (x *indexOp) prepareOp(mod rgw.ModifyOp) *radosclient.WriteOp {
	w := radosclient.NewWriteOp()
	w.AssertExists()
	rgw.GuardBucketResharding(w, x.s.release)
	p := rgw.PrepareOp{Op: mod, Key: x.clsKey(), Tag: x.tag, Locator: x.obj.Key.Locator()}
	if x.s.release == denc.Squid { // Tentacle's cls_rgw_bucket_prepare_op has no log arguments
		p.LogOp = x.s.w.logData
		p.ZonesTrace = x.zonesTrace()
	}
	rgw.BucketPrepareOp(w, p, x.s.release)
	return w
}

// completeOp is cls_obj_complete_op's write op (rgw_rados.cc:9481-9523).
func (x *indexOp) completeOp(c rgw.CompleteOp) *radosclient.WriteOp {
	w := radosclient.NewWriteOp()
	w.AssertExists()
	rgw.GuardBucketResharding(w, x.s.release)
	rgw.BucketCompleteOp(w, c, x.s.release)
	return w
}

func (x *indexOp) entry(mod rgw.ModifyOp, ver rgw.EntryVer, m rgw.DirEntryMeta) rgw.CompleteOp {
	return rgw.CompleteOp{Op: mod, Key: x.clsKey(), Locator: x.obj.Key.Locator(), Ver: ver, Meta: m, Tag: x.tag, LogOp: x.s.w.logData, ZonesTrace: x.zonesTrace()}
}

func (x *indexOp) prepare(ctx context.Context, mod rgw.ModifyOp) error {
	if x.blind {
		return nil
	}
	err := x.guardReshard(ctx, func(sh indexShard) error {
		_, err := sh.pool.Write(ctx, sh.oid, x.prepareOp(mod), 0)
		return err
	})
	if err != nil {
		return err
	}
	x.prepared = true
	return nil
}

func (x *indexOp) complete(ver rgw.EntryVer, m rgw.DirEntryMeta) {
	if x.blind {
		return
	}
	m.Category = rgw.CategoryMain
	x.s.w.completions.submit(x, x.entry(rgw.OpAdd, ver, m))
}

func (x *indexOp) completeDel(ver rgw.EntryVer, removedMtime time.Time) {
	if x.blind {
		return
	}
	x.s.w.completions.submit(x, x.entry(rgw.OpDel, ver, rgw.DirEntryMeta{Category: rgw.CategoryNone, Mtime: removedMtime}))
}

// cancel is UpdateIndex::cancel through cls_obj_complete_cancel, which sends
// pool -1 and epoch 0 whatever the object's state (rgw_rados.cc:9553-9563 at
// v19.2.6, :10485-10495 at v20.2.4). The class copies that version onto the
// entry before it writes the cancel back (cls_rgw.cc:1094, v20.2.4 :1229), so
// the next completion applies at any epoch (tracker #80894), as it does
// behind radosgw.
func (x *indexOp) cancel() {
	if x.blind {
		return
	}
	x.s.w.completions.submit(x, x.entry(rgw.OpCancel, rgw.EntryVer{Pool: -1, Epoch: 0}, rgw.DirEntryMeta{Category: rgw.CategoryNone}))
}

// guardReshard is UpdateIndex::guard_reshard (rgw_rados.cc:7024-7077).
func (x *indexOp) guardReshard(ctx context.Context, call func(sh indexShard) error) error {
	var err error
	for i := 0; i < reshardRetries; i++ {
		sh, serr := x.currentShard(ctx)
		if serr != nil {
			return serr
		}
		err = call(sh)
		if !errors.Is(err, radosclient.ErrBusyResharding) {
			return err
		}
		slog.InfoContext(ctx, "bucket index shard is resharding; blocking", slog.String("bucket", x.obj.Bucket.Name), slog.String("shard", sh.oid))
		berr := x.s.blockWhileResharding(ctx, x, sh)
		switch {
		case errors.Is(berr, radosclient.ErrBusyResharding):
			continue
		case berr != nil:
			return berr
		}
		i = 0 // the reshard finished: radosgw resets its attempt counter (:7063)
	}
	return err
}

// reshardBusy is the release's reading of a shard's reshard status:
// resharding_in_progress() on Squid (rgw_rados.cc:7841), resharding() on
// Tentacle (v20.2.4 :8783; cls_rgw_types.h:800-810).
func (s *Store) reshardBusy(e rgw.InstanceEntry) bool {
	if s.release == denc.Squid {
		return e.ReshardStatus == uint8(meta.ReshardStatusInProgress)
	}
	return e.ReshardStatus != uint8(meta.ReshardStatusNotResharding)
}

func (s *Store) blockWhileResharding(ctx context.Context, x *indexOp, sh indexShard) error {
	refresh := func() error {
		rec, err := s.GetBucketInstance(ctx, x.rec.Info.Bucket)
		if err != nil {
			return fmt.Errorf("refreshing bucket instance after reshard: %w", err)
		}
		x.rec, x.shard = rec, nil
		return nil
	}
	for i := 1; i <= reshardRetries; i++ {
		rop := radosclient.NewReadOp()
		res := rgw.GetBucketResharding(rop, s.release)
		_, err := sh.pool.Read(ctx, sh.oid, rop, 0)
		switch {
		case errors.Is(err, radosclient.ErrNotFound):
			return refresh() // the old shard is gone: the layout moved on
		case err != nil:
			return err
		}
		entry, err := res.Result()
		if err != nil {
			return err
		}
		if !s.reshardBusy(entry) {
			return refresh()
		}
		if i == reshardRetries {
			break
		}
		slog.InfoContext(ctx, "bucket index still resharding; waiting", slog.String("shard", sh.oid), slog.Int("attempt", i))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(s.w.reshardWait):
		}
	}
	return radosclient.ErrBusyResharding
}
```

`completion.go`:

```go
package driver

type completion struct {
	x *indexOp
	c rgw.CompleteOp
}

type completionManager struct {
	s        *Store
	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	queue    []completion
	wake     chan struct{}
	wg       sync.WaitGroup
	inflight atomic.Int64
}

func newCompletionManager(s *Store) *completionManager {
	ctx, cancel := context.WithCancel(context.Background())
	return &completionManager{s: s, ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1)}
}

// submit is cls_obj_complete_op's aio_operate together with handle_completion
// (rgw_rados.cc:9514-9518, :994-1023): the write runs in its own goroutine
// under the manager's lifetime, a busy-resharding answer queues a retry, any
// other failure is logged and dropped.
func (m *completionManager) submit(x *indexOp, c rgw.CompleteOp) {
	m.wg.Add(1)
	m.inflight.Add(1)
	go func() {
		defer m.wg.Done()
		defer m.inflight.Add(-1)
		sh, err := x.currentShard(m.ctx)
		if err == nil {
			_, err = sh.pool.Write(m.ctx, sh.oid, x.completeOp(c), 0)
		}
		switch {
		case err == nil:
		case errors.Is(err, radosclient.ErrBusyResharding):
			m.enqueue(completion{x: x, c: c})
		default:
			slog.WarnContext(m.ctx, "bucket index completion failed; the pending entry is left for listing to reconcile",
				slog.String("bucket", x.obj.Bucket.Name), slog.String("key", c.Key.Name), slog.Int("op", int(c.Op)), slog.Any("error", err))
		}
	}()
}

func (m *completionManager) enqueue(c completion) {
	m.mu.Lock()
	m.queue = append(m.queue, c)
	m.mu.Unlock()
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *completionManager) pending() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return int(m.inflight.Load()) + len(m.queue)
}

// run is RGWIndexCompletionManager::process (rgw_rados.cc:875-938): each
// queued completion is reissued against the shard a fresh bucket instance
// names, under the reshard guard; a failure is logged and the completion
// dropped. When ctx ends the in-flight submits are cancelled and awaited.
func (m *completionManager) run(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			m.cancel()
			m.wg.Wait()
			return nil
		case <-m.wake:
			m.mu.Lock()
			batch := m.queue
			m.queue = nil
			m.mu.Unlock()
			for _, c := range batch {
				m.retry(c)
			}
		}
	}
}

func (m *completionManager) retry(c completion) {
	m.wg.Add(1)
	defer m.wg.Done()
	rec, err := m.s.GetBucketInstance(m.ctx, c.x.rec.Info.Bucket)
	if err != nil {
		slog.ErrorContext(m.ctx, "bucket index completion dropped: cannot re-read the bucket instance", slog.String("bucket", c.x.obj.Bucket.Name), slog.Any("error", err))
		return
	}
	c.x.rec, c.x.shard = rec, nil
	err = c.x.guardReshard(m.ctx, func(sh indexShard) error {
		_, err := sh.pool.Write(m.ctx, sh.oid, c.x.completeOp(c.c), 0)
		return err
	})
	if err != nil {
		slog.ErrorContext(m.ctx, "bucket index completion failed after resharding; the pending entry is left for listing to reconcile",
			slog.String("bucket", c.x.obj.Bucket.Name), slog.String("key", c.c.Key.Name), slog.Any("error", err))
	}
}
```

`pending()` counts a retry as in flight while `retry` runs, since `wg.Add` precedes the queue pop: pop under the lock, then `inflight.Add(1)` per item before releasing it, and `Add(-1)` at the end of `retry`, so the count never dips to zero between the queue and the reissue. `store.go`: `Store` gains `w *writer`; `Open`, after M's `resolveZone` and `newPoolCache`, runs `wopts, err := readWriteOptions(conf)`, `s.w = newWriter(s, wopts)`, `s.AddWorker("index-completions", s.w.completions.run)`.

- [ ] **Step 7: Record the recovery block's absence, run the suites, lint, commit**

`docs/exclusions.md`, under "Dynamic resharding worker", after its "Still required" paragraph:

> When an interrupted reshard has left a bucket index flagged as resharding, radosgw's write path takes the bucket's reshard lock and, finding no reshard running, clears the flag itself. rgw-go does not take that lock: its writes wait and retry, and answer 500 once their retries run out, until an operator runs `radosgw-admin reshard cancel`.

Run: `make generate-check && go test -tags=ceph_preview -race ./internal/cls/rgw/ ./internal/testutil/fakerados/ ./internal/driver/ && golangci-lint run ./internal/driver/ ./internal/cls/rgw/ ./internal/testutil/fakerados/`
Expected: PASS, lint clean. The completion specs run under `-race`, which is what the manager's locking is for.

`make check`, then `feat(driver): index prepare, complete and cancel behind the reshard guard with radosgw's completion manager`. Draft PR `feat(driver): bucket index protocol and completion manager (unit W task 4)`; its description names the additive class method and the fakerados growth, W-D8's `-1:0` cancel and the `docs/exclusions.md` entry (to be announced to the rgw-rs session), and states that a reshard that moves the layout generation mid-write is proved only by Task 12's cluster oracle.

---
### Task 5: `driver`: stripe writer, the atomic head write, GC enqueue, `PutObject`; `PutParams.Tag`, `PutParams.ContentMD5`

**Files:**
- Create: `internal/driver/stripe.go`, `internal/driver/headwrite.go`, `internal/driver/gc_enqueue.go`, `internal/driver/put.go`, `internal/driver/stripe_test.go`, `internal/driver/headwrite_test.go`, `internal/driver/gc_enqueue_test.go`, `internal/driver/put_test.go`
- Modify: `internal/op/object.go` (`PutParams.Tag`, `PutParams.ContentMD5`), `internal/op/opfakes/` (regenerate), `internal/memstore/object.go` (`Tag` becomes the stored `WriteTag`; `ContentMD5` mismatch is `ErrBadDigest`), `internal/driver/store.go` (the `PutObject` stub goes), `internal/driver/export_test.go`, `internal/testutil/fakerados/cls_rgw.go` (`obj_remove`, `obj_store_pg_ver`), `internal/testutil/fakerados/cls_gc.go` (new: `GCQueueClass`, `Cluster.GCEntries`), `internal/testutil/fakerados/pool.go` (`WriteOp.Mtime()` sets the object's mtime; `Cluster.BeforeWrite(pool, ns, oid, fn)`), `go.mod` (`github.com/cespare/xxhash/v2`, W-D4)

**Interfaces:**
- Consumes: Task 3's `meta.NewTrivialManifest`, `Manifest.SetObjSize`, `TailStripe`, `TailObj`, `PlacementRule.InheritFrom`, `BucketID.Key`; Task 4's `indexOp`, `writer`, `completionManager`; R Task 3's `Store.readHead(ctx, rec, key, false)`, `dataPool`, `rawRef`, `objRef`, `Manifest.Stripes`/`Seek`; G's `op.PutParams`, `PutResult`, `ObjectState`, `op.FromRADOS`, the sentinels `ErrBadDigest`, `ErrRequestTimeout`, `ErrPreconditionFailed`, `ErrNoSuchKey`, `ErrQuotaExceeded`; M's `Store.CheckQuota`, `Store.AdjustStats` (W-D7; a no-op until M Task 9 lands), `Store.Placement` (`InlineData`), `Store.pools`; `radosclient.WriteOp` (`Create`, `CmpXattr`, `SetMtime`, `WriteFull`, `Write`, `SetXattr`, `RmXattr`, `SetAllocHint`, `Remove`), `Pool.Write`, `Pool.ID`, `Pool.RequiredAlignment` (Task 1), `OpFlagFullTry`; `rgw.ObjRemove`, `rgw.ObjStorePGVer`, `rgw.GCSetEntry`, `rgw.GCObjInfo`, `rgw.GCObj`, `gc.QueueEnqueue`, `version.Check`, `version.ObjVersion`, `refcount.Put`; `acl.DecodePolicy`; `github.com/cespare/xxhash/v2`; `golang.org/x/sync/semaphore`.
- Produces:

```go
package op

// PutParams gains two additive fields.
//	// Tag is the write tag: radosgw uses the request id (rgw_op.cc:4290,
//	// rgw_putobj_processor.cc:377), so user.rgw.idtag and user.rgw.tail_tag
//	// hold it NUL-terminated and the index entry holds it bare. Empty selects
//	// append_rand_alpha's form, "_" and 31 random characters.
//	Tag string
//	// ContentMD5 is the decoded Content-MD5 header, 16 bytes, nil when the
//	// request carried none; a body whose MD5 differs is ErrBadDigest before
//	// the head is written (RGWPutObj::execute, rgw_op.cc:4459-4462).
//	ContentMD5 []byte
```

```go
package driver

// layout is AtomicObjectProcessor::prepare's arithmetic for one PUT.
type layout struct {
	headPool, tailPool radosclient.Pool
	headRule, tailRule meta.PlacementRule
	chunk, stripe      uint64 // aligned rgw_max_chunk_size and rgw_obj_stripe_size
	maxHead            uint64 // the head chunk size, or 0 when the tail pool differs or inline_data is off
}

// planPut resolves the pools and sizes for a PUT of key under storageClass
// (rgw_putobj_processor.cc:296-343; the alignments through
// Pool.RequiredAlignment, cached per pool).
func (s *Store) planPut(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, storageClass string) (layout, error)

// tailWriter is RadosWriter with the StripeProcessor and ChunkProcessor above
// it and the put window around it: it streams a body into tail objects while
// the caller keeps the head's bytes, remembers every tail it issued and can
// delete them all.
type tailWriter struct { /* s, m *meta.Manifest, pool radosclient.Pool, chunk, window; sem *semaphore.Weighted; mu; issued []meta.Obj; firstErr error; wg */ }

func (s *Store) newTailWriter(m *meta.Manifest, l layout) *tailWriter

// consume reads body to EOF, hashing every byte into h: the first
// m.MaxHeadSize bytes come back as the head's data, the rest go to tail
// objects in stripe order. A body error is returned as is (auth's
// verification errors are *op.Error already); io.ErrUnexpectedEOF becomes
// ErrRequestTimeout.
func (t *tailWriter) consume(ctx context.Context, body io.Reader, h hash.Hash) (head []byte, size uint64, err error)

// drain is RadosWriter::drain: it waits for every tail write and returns the first failure.
func (t *tailWriter) drain() error

// discard is ~RadosWriter's cleanup: after drain, every issued tail is removed
// (delete_raw_obj; ENOENT ignored, a failure logged as leaked). It runs
// under the driver's lifetime context, not the request's.
func (t *tailWriter) discard()

// headWrite is RGWRados::Object::Write::meta for one write_meta call.
type headWrite struct {
	rec                  *op.BucketRecord
	key                  meta.ObjKey
	tag                  string
	data                 []byte            // the head's bytes; written with write_full even when empty
	manifest             *meta.Manifest    // nil for set_attrs-style writes
	attrs                map[string][]byte // set in byte order of the names; empty values skipped
	rmAttrs              []string
	mtime                time.Time // zero means now
	ifMatch, ifNoneMatch string
	create               bool // PUT_OBJ_CREATE: reset the object
	modifyTail, keepTail bool
	size, accountedSize  uint64
}

// headResult is what write_meta leaves behind.
type headResult struct {
	epoch    uint64
	poolID   int64
	mtime    time.Time
	canceled bool // the write lost a race radosgw answers as success
}

// writeMeta is RGWRados::Object::Write::write_meta (rgw_rados.cc:3419-3437
// over _do_write_meta :3124-3417; v20.2.4 :3572, :3234): the assume-no-entry
// pass, the EEXIST re-stat, the guarded pass, the index prepare and complete,
// the GC of an overwritten object's tails, the quota adjustment, and the
// cancel semantics. x is the index op the caller made with hw.tag.
func (s *Store) writeMeta(ctx context.Context, hw *headWrite, x *indexOp) (headResult, error)

// checkPreconditions is the release's precondition rule for a write:
// prepare_atomic_modification's on Squid (rgw_rados.cc:6502-6527),
// check_preconditions on Tentacle (v20.2.4 :7260-7329).
func (s *Store) checkPreconditions(st *op.ObjectState, ifMatch, ifNoneMatch string) error

// entryOwner is the index entry's owner: the object's ACL owner, as set_attrs
// derives it (rgw_rados.cc:6700-6706); PUT and COPY hand the driver an ACL
// whose owner is the requester, so this equals radosgw's s->owner.
func entryOwner(attrs map[string][]byte) (id, displayName string)

// gcChain is update_gc_chain (rgw_rados.cc:5409-5421): every stripe but the head.
func (s *Store) gcChain(m *meta.Manifest, headRule meta.PlacementRule) ([]rgw.GCObj, error)

// gcShard is RGWGC::tag_index (rgw_gc.cc:63-66): rgw_shards_mod(XXH64(tag, 8675309), max_objs),
// the hash truncated to rgw_shards_mod's unsigned int (rgw_tools.h:46-55).
func (w *writer) gcShard(tag string) int

// enqueueGC is complete_atomic_modification's GC half (rgw_rados.cc:5382-5407
// with send_split_chain rgw_gc.cc:68-118 and send_chain :120-138): the chain
// is queued under tag, the raw tail_tag or idtag bytes NUL included, in
// batches whose encoding stays under rgw_max_chunk_size; a shard that has not
// transitioned (ECANCELED) or refuses (EPERM) takes gc_set_entry instead;
// anything still refused is deleted inline with refcount put under
// pool_full_try. It never fails the caller.
func (s *Store) enqueueGC(ctx context.Context, chain []rgw.GCObj, tag string)

func (s *Store) PutObject(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, body io.Reader, p op.PutParams) (*op.PutResult, error)
```

`export_test.go` adds `func GCShardForTest(s *Store, tag string) int`, `type Layout = layout`, `func (s *Store) PlanPutForTest(ctx, rec, key, sc) (Layout, error)`.

The PUT this task builds, in RADOS order: the tails while the body streams (each `[SetAllocHint(0,0,0), WriteFull]` or `Write(ofs)` for a later piece of a stripe, ≤ `putWindow` bytes in flight), then `drain`, then the index prepare, then the head write, then the fire-and-forget complete — two synchronous round trips within the head, as §6 requires. Over an existing key: prepare, the exclusive head write fails EEXIST, one head stat (R's `readHead`), the guarded head write, the GC enqueue of the old tails, complete. The head op, in step order, is `_do_write_meta`'s ([`:3141-3259`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L3141-L3259)): `[CmpXattr(idtag) when guarded] [Create(false) + obj_remove(keep "user.rgw.olh.") when it existed, else Create(true)] SetXattr(idtag) SetXattr(tail_tag) | SetMtime | WriteFull(data) RmXattr… SetXattr(manifest) SetXattr(attrs in name order, empty skipped) Exec(obj_store_pg_ver, "user.rgw.pg_ver") SetXattr(source_zone, 4 LE bytes) [SetXattr(storage_class)]`.

- [ ] **Step 1: The contract fields, fakes and memstore**

`internal/op/object.go`: add `Tag string` and `ContentMD5 []byte` to `PutParams` after `ETag`, with the comments above. `make generate`. `internal/memstore/object.go`: `PutObject` stores `WriteTag = p.Tag` (or a memstore-generated `"_" + 31` when empty) and, when `p.ContentMD5 != nil`, compares it to the body's MD5 and returns `op.ErrBadDigest` without storing. `internal/memstore/object_test.go` gains `It("refuses a body whose MD5 is not the supplied Content-MD5")` and `It("stores the write tag it was given")`.

- [ ] **Step 2: fakerados growth**

`cls_rgw.go`: `obj_remove` decodes `rgwcls.DecodeObjRemoveOp`; with no prefixes the object is removed (return `created=false` and signal removal by clearing `obj.Data`, `obj.Xattrs`, `obj.Omap` and setting a `removed` marker the pool applies as a delete); with prefixes the xattrs whose names start with one are kept and the rest of the object cleared (cls_rgw.cc:2410-2474: remove, then create with the kept attrs; in one op the following steps recreate it). `obj_store_pg_ver` decodes `DecodeStorePGVerOp` and sets `Xattrs[attr]` to the object's `Version` as 8 LE bytes. `pool.go`: a write op with `Mtime()` set stamps `obj.mtime` after its steps (M's `newObject()` used the cluster clock); `Cluster.BeforeWrite(pool, ns, oid string, fn func(o *Object))` registers a hook run under the pool lock with the stored object before each write op to that oid (nil when absent), the twin of M's `AfterWrite`. `cls_gc.go`: `GCQueueClass() ClassFunc` emulates `rgw_gc_queue_enqueue` by appending the decoded `rgwcls.GCSetEntryOp.Info`, stamped `Time = now + ExpirationSecs`, to a per-object list (`Cluster.GCEntries(pool, ns, oid) []rgwcls.GCObjInfo`); every other method is `-EOPNOTSUPP` until Task 9. `cls_refcount.go`: `RefcountClass() ClassFunc` emulates the refcount class over the `refcount` xattr with phase 0's `refcount.Refcount` codec: `get` on a missing object is `-ENOENT`, otherwise with `ImplicitRef` and no xattr the wildcard `""` is added first, then the tag; `put` on a missing object is `-ENOENT`, drops the tag (or the wildcard when `ImplicitRef` and the tag is not held), records it retired, and removes the object when no ref is left; `set` replaces; `read` returns the sorted tags. `cls_rgw_test.go`, `cls_gc_test.go` and `cls_refcount_test.go` pin each.

- [ ] **Step 3: Write the failing `tailWriter` and `planPut` specs**

`internal/driver/stripe_test.go`, package `driver_test`, on the Task 4 fixture plus R's zone shape (default placement `default-placement` on pool `ceph-objectstore.rgw.buckets.data`, inline_data true; a `COLD` class on pool `cold.data`):

```go
var _ = Describe("planPut", func() {
	It("puts the head chunk in the head when head and tail share a pool", func(ctx SpecContext) {
		l, err := s.PlanPutForTest(ctx, rec, key, "")
		Expect(err).NotTo(HaveOccurred())
		Expect(l.MaxHead).To(BeEquivalentTo(4 << 20))
		Expect(l.Chunk).To(BeEquivalentTo(4 << 20))
		Expect(l.Stripe).To(BeEquivalentTo(4 << 20))
		Expect(l.TailRule).To(Equal(meta.PlacementRule{Name: "default-placement"}), "inherit_from the bucket rule")
	})
	It("keeps the head empty when the storage class lives in another pool", func(ctx SpecContext) {
		l, err := s.PlanPutForTest(ctx, rec, key, "COLD")
		Expect(err).NotTo(HaveOccurred())
		Expect(l.MaxHead).To(BeZero(), "rgw_putobj_processor.cc:313-323")
		Expect(l.TailPool.Name()).To(Equal("cold.data"))
		Expect(l.TailRule).To(Equal(meta.PlacementRule{Name: "default-placement", StorageClass: "COLD"}))
	})
	It("rounds the chunk and stripe down to the pool's alignment", func(ctx SpecContext) {
		c.SetAlignment("ceph-objectstore.rgw.buckets.data", 3<<20) // fakerados gains SetAlignment(pool, bytes) for RequiredAlignment
		l, err := s.PlanPutForTest(ctx, rec, key, "")
		Expect(err).NotTo(HaveOccurred())
		Expect(l.Chunk).To(BeEquivalentTo(3 << 20), "get_max_aligned_size: size - size % alignment")
		Expect(l.Stripe).To(BeEquivalentTo(3 << 20))
	})
})

var _ = Describe("tailWriter", func() {
	It("streams a 10 MiB body as a 4 MiB head and two tails, hashing every byte", func(ctx SpecContext) {
		body := bytes.Repeat([]byte("x"), 10<<20)
		head, size, tails := s.ConsumeForTest(ctx, rec, key, bytes.NewReader(body)) // export_test: planPut + NewTrivialManifest + newTailWriter + consume + drain, returning the tail oids in issue order
		Expect(size).To(BeEquivalentTo(10 << 20))
		Expect(head).To(HaveLen(4 << 20))
		Expect(tails).To(HaveLen(2))
		Expect(tails[0]).To(HaveSuffix("_1"))
		Expect(tails[1]).To(HaveSuffix("_2"))
		Expect(c.Object(dataPool, "", tails[0]).Data).To(HaveLen(4 << 20))
		Expect(c.Object(dataPool, "", tails[1]).Data).To(HaveLen(2 << 20))
		steps := c.LastWrite(dataPool, "", tails[1]).Steps()
		Expect(steps[0]).To(Equal(&radosclient.SetAllocHintStep{}), "add_write_hint: set_alloc_hint2(0, 0, 0)")
		Expect(steps[1]).To(BeAssignableToTypeOf(&radosclient.WriteFullStep{}))
	})
	It("writes a stripe in pieces when the chunk is smaller than the stripe", func(ctx SpecContext) {
		// rgw_max_chunk_size 1 MiB, rgw_obj_stripe_size 4 MiB: stripe _1 gets [WriteFull][Write 1M][Write 2M][Write 3M]
	})
	It("keeps at most the put window in flight", func(ctx SpecContext) {
		// conf rgw_put_obj_min_window_size 8 MiB; a fakerados write hook that blocks until released counts concurrent tail writes; expect a peak of 2
	})
	It("issues nothing for a body within the head, and no empty writes", func(ctx SpecContext) {
		// 4 MiB body: head 4 MiB, no tails; 0-byte body: head empty, no tails
	})
	It("returns the body's error, deletes what it issued and leaves the pool clean", func(ctx SpecContext) {
		// a reader that yields 6 MiB then io.ErrUnexpectedEOF: consume returns op.ErrRequestTimeout; discard removes tail _1; a reader that yields 5 MiB then op.ErrSignatureDoesNotMatch: the same error comes back verbatim
	})
	It("survives a client disconnect without cancelling the writes in flight", func(ctx SpecContext) {
		// a reader that blocks until the request ctx is cancelled after 6 MiB: consume returns ctx.Err(); the tail write that was in flight still lands (fakerados sees it) and discard then removes it
	})
})
```

Write the four elided bodies in full against the same fixture (the hook is `c.BeforeWrite` with a channel; the disconnect spec cancels a child context).

- [ ] **Step 4: Write the failing `writeMeta` and `PutObject` specs**

`internal/driver/put_test.go` (until Task 8 fills the `refcount` emulator, it answers only `put` on a missing object, with ENOENT):

```go
var _ = Describe("PutObject", func() {
	var gcPool = "ceph-objectstore.rgw.log" // the zone fixture's gc_pool (namespace "gc" under Rook; use the fixture's meta.Pool)
	BeforeEach(func() {
		c.RegisterClass("rgw_gc", fakerados.GCQueueClass())
		c.RegisterClass("refcount", fakerados.RefcountClass()) // only `put` on a missing object → ENOENT matters here
		seedGCShards(c, s) // 32 objects gc.0..gc.31 in the zone's gc pool with cls version {Ver: 1}
		s.SetRandForTest(fixedRand("AkTbQPoXT3s8QcthZt-DpS5NPRLqS5u"))
	})
	It("writes a small object with radosgw's two round trips and attributes", func(ctx SpecContext) {
		attrs := map[string][]byte{meta.AttrACL: encode(acl.DefaultPolicy(alice, "Alice")), meta.AttrContentType: []byte("text/plain\x00"), meta.AttrMetaPrefix + "k": []byte("v\x00")}
		res, err := s.PutObject(ctx, rec, key, strings.NewReader("hello"), op.PutParams{Attrs: attrs, Size: 5, Tag: "tx000001-a", Mtime: mtime})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.ETag).To(Equal("5d41402abc4b2a76b9719d911017c592"))
		Expect(res.Size).To(BeEquivalentTo(5))
		Expect(res.Mtime).To(Equal(mtime))
		head := c.Object(dataPool, "", rec.Info.Bucket.Marker+"_k")
		Expect(head.Data).To(Equal([]byte("hello")))
		Expect(head.Xattrs).To(HaveKeyWithValue("user.rgw.idtag", []byte("tx000001-a\x00")))
		Expect(head.Xattrs).To(HaveKeyWithValue("user.rgw.tail_tag", []byte("tx000001-a\x00")))
		Expect(head.Xattrs).To(HaveKeyWithValue("user.rgw.etag", []byte("5d41402abc4b2a76b9719d911017c592")), "no NUL, rgw_op.cc:4504-4506")
		Expect(head.Xattrs).To(HaveKeyWithValue("user.rgw.content_type", []byte("text/plain\x00")))
		Expect(head.Xattrs).To(HaveKeyWithValue("user.rgw.x-amz-meta-k", []byte("v\x00")))
		Expect(head.Xattrs).To(HaveKeyWithValue("user.rgw.source_zone", []byte{0x39, 0x30, 0, 0}), "the fixture's short id 12345 as LE u32")
		Expect(head.Xattrs).To(HaveKey("user.rgw.pg_ver"))
		Expect(head.Xattrs).NotTo(HaveKey("user.rgw.storage_class"), "empty class writes no attr")
		m := meta.DecodeManifest(denc.NewDecoder(head.Xattrs["user.rgw.manifest"]))
		Expect(m.ObjSize).To(BeEquivalentTo(5))
		Expect(m.HeadSize).To(BeEquivalentTo(5))
		Expect(m.MaxHeadSize).To(BeEquivalentTo(4 << 20))
		Expect(m.Prefix).To(Equal(".AkTbQPoXT3s8QcthZt-DpS5NPRLqS5u_"))
		Expect(m.Rules[0]).To(Equal(meta.ManifestRule{StartOfs: 4 << 20, StripeMaxSize: 4 << 20}))
		Expect(m.TailPlacement).To(Equal(meta.BucketPlacement{Bucket: rec.Info.Bucket, PlacementRule: meta.PlacementRule{Name: "default-placement"}}))
		Expect(head.Xattrs).NotTo(HaveKey("user.rgw.x-amz-tagging"))

		steps := c.LastWrite(dataPool, "", rec.Info.Bucket.Marker+"_k").Steps()
		Expect(steps[0]).To(Equal(&radosclient.CreateStep{Exclusive: true}), "assume-no-entry pass")
		Expect(steps[1]).To(Equal(&radosclient.SetXattrStep{Name: "user.rgw.idtag", Value: []byte("tx000001-a\x00")}))
		Expect(steps[2]).To(Equal(&radosclient.SetXattrStep{Name: "user.rgw.tail_tag", Value: []byte("tx000001-a\x00")}))
		Expect(steps[3]).To(BeAssignableToTypeOf(&radosclient.WriteFullStep{}))
		Expect(steps[4].(*radosclient.SetXattrStep).Name).To(Equal("user.rgw.manifest"))
		Expect(names(steps[5:8])).To(Equal([]string{"user.rgw.acl", "user.rgw.content_type", "user.rgw.etag"}), "std::map order")
		Expect(steps[8].(*radosclient.SetXattrStep).Name).To(Equal("user.rgw.x-amz-meta-k"))
		Expect(steps[9].(*radosclient.ExecStep).Method).To(Equal("obj_store_pg_ver"))
		Expect(steps[10].(*radosclient.SetXattrStep).Name).To(Equal("user.rgw.source_zone"))
		Expect(steps).To(HaveLen(11))
		mt, ok := c.LastWrite(dataPool, "", rec.Info.Bucket.Marker+"_k").Mtime()
		Expect(ok).To(BeTrue())
		Expect(mt).To(Equal(mtime))

		shard := shardOf(rec, "k")
		Expect(c.Writes(indexPool, "", shard)).To(Equal(1), "the prepare; the complete is not awaited")
		Eventually(func() int { return c.Writes(indexPool, "", shard) }).Should(Equal(2))
		en, _ := c.Entry(indexPool, "", shard, "k")
		Expect(en.Exists).To(BeTrue())
		Expect(en.Tag).To(Equal("tx000001-a"))
		Expect(en.Meta).To(Equal(rgwcls.DirEntryMeta{Category: rgwcls.CategoryMain, Size: 5, AccountedSize: 5, Mtime: mtime, ETag: "5d41402abc4b2a76b9719d911017c592", Owner: "alice", OwnerDisplayName: "Alice", ContentType: "text/plain"}))
		Expect(en.Ver).To(Equal(rgwcls.EntryVer{Pool: c.PoolID(dataPool), Epoch: head.Version}))
		Expect(c.Writes(dataPool, "", rec.Info.Bucket.Marker+"_k")).To(Equal(1), "one head write: two RADOS round trips in all")
	})
	It("writes a 10 MiB object as head plus two tails and a manifest Stripes reads back", func(ctx SpecContext) {
		// tails ".AkTb…_1" (4 MiB) and "_2" (2 MiB) in the shadow namespace oid form "<marker>__shadow_.AkTb…_1"; manifest ObjSize 10 MiB, HeadSize 4 MiB; Stripes() has 3; the index entry Size 10 MiB
	})
	It("overwrites with the EEXIST retry, guards on the old tag, removes the old head and queues its tails", func(ctx SpecContext) {
		_, err := s.PutObject(ctx, rec, key, bytes.NewReader(bytes.Repeat([]byte("a"), 6<<20)), op.PutParams{Attrs: attrs, Size: 6 << 20, Tag: "tx-old"})
		Expect(err).NotTo(HaveOccurred())
		oldTail := rec.Info.Bucket.Marker + "__shadow_.AkTbQPoXT3s8QcthZt-DpS5NPRLqS5u_1"
		Expect(c.Object(dataPool, "", oldTail)).NotTo(BeNil())
		s.SetRandForTest(fixedRand("SECONDSECONDSECONDSECONDSECOND1"))
		headOID := rec.Info.Bucket.Marker + "_k"
		c.ResetCounters()
		res, err := s.PutObject(ctx, rec, key, strings.NewReader("new"), op.PutParams{Attrs: attrs, Size: 3, Tag: "tx-new"})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.ETag).To(Equal(md5hex("new")))
		Expect(c.Writes(dataPool, "", headOID)).To(Equal(2), "exclusive create fails EEXIST, then the guarded write")
		Expect(c.Reads(dataPool, "", headOID)).To(Equal(1), "one raw_obj_stat between them")
		steps := c.LastWrite(dataPool, "", headOID).Steps()
		Expect(steps[0]).To(Equal(&radosclient.CmpXattrStep{Name: "user.rgw.idtag", Op: radosclient.CmpEQ, Value: []byte("tx-old\x00")}))
		Expect(steps[1]).To(Equal(&radosclient.CreateStep{Exclusive: false}))
		rm := steps[2].(*radosclient.ExecStep)
		Expect(rm.Method).To(Equal("obj_remove"))
		Expect(rgwcls.DecodeObjRemoveOp(denc.NewDecoder(rm.In)).KeepAttrPrefixes).To(Equal([]string{"user.rgw.olh."}))
		Expect(c.Writes(indexPool, "", shardOf(rec, "k"))).To(Equal(1), "prepared once for both passes (UpdateIndex::is_prepared)")
		entries := c.GCEntries(gcPoolName, gcNS, fmt.Sprintf("gc.%d", driver.GCShardForTest(s, "tx-old\x00")))
		Expect(entries).To(HaveLen(1))
		Expect(entries[0].Tag).To(Equal("tx-old\x00"), "tail_tag bytes, NUL included")
		Expect(entries[0].Chain).To(Equal([]rgwcls.GCObj{{Pool: dataPool, Key: rgwcls.ObjKey{Name: oldTail}}}))
		gcOp := c.LastWrite(gcPoolName, gcNS, fmt.Sprintf("gc.%d", driver.GCShardForTest(s, "tx-old\x00"))).Steps()
		Expect(gcOp[0].(*radosclient.ExecStep).Method).To(Equal("check_conds"), "cls_version_check(ver 1)")
		Expect(gcOp[1].(*radosclient.ExecStep).Method).To(Equal("rgw_gc_queue_enqueue"))
		Expect(c.Object(dataPool, "", oldTail)).NotTo(BeNil(), "the GC worker, not the PUT, removes it")
	})
	It("falls back to gc_set_entry on a shard that has not transitioned and deletes inline when both refuse", func(ctx SpecContext) {
		// shard version 0 → the enqueue fails ECANCELED → the same shard receives [gc_set_entry]; a shard whose class returns EIO → refcount put(tag, implicit) with OpFlagFullTry on the old tail (c.LastWrite(...).Flags() shows OpFlagFullTry) and the tail is gone
	})
	It("answers success and cancels with radosgw's -1:0 when another writer replaced the head", func(ctx SpecContext) {
		_, err := s.PutObject(ctx, rec, key, strings.NewReader("first"), op.PutParams{Attrs: attrs, Size: 5, Tag: "tx-1"})
		Expect(err).NotTo(HaveOccurred())
		headOID := rec.Info.Bucket.Marker + "_k"
		s.SetRandForTest(fixedRand("SECONDSECONDSECONDSECONDSECOND1"))
		writes := 0
		c.BeforeWrite(dataPool, "", headOID, func(o *fakerados.Object) {
			writes++
			if writes == 2 && o != nil { // the guarded pass: a racer swapped the tag between our stat and our write
				o.Xattrs["user.rgw.idtag"] = []byte("tx-racer\x00")
			}
		})
		res, err := s.PutObject(ctx, rec, key, bytes.NewReader(bytes.Repeat([]byte("b"), 5<<20)), op.PutParams{Attrs: attrs, Size: 5 << 20, Tag: "tx-2"})
		Expect(err).NotTo(HaveOccurred(), "rgw_rados.cc:3392-3396: ECANCELED without preconditions is success")
		Expect(res.ETag).To(Equal(md5hex(bytes.Repeat([]byte("b"), 5<<20))))
		Expect(c.Object(dataPool, "", headOID).Xattrs["user.rgw.idtag"]).To(Equal([]byte("tx-racer\x00")), "the racer's head stands")
		Expect(c.Object(dataPool, "", rec.Info.Bucket.Marker+"__shadow_.SECONDSECONDSECONDSECONDSECOND1_1")).To(BeNil(), "our tail was deleted")
		shard := shardOf(rec, "k")
		Eventually(func() int { return c.Writes(indexPool, "", shard) }).Should(Equal(4), "prepare+complete of the first PUT, prepare+cancel of the second")
		cancel := rgwcls.DecodeCompleteOp(denc.NewDecoder(c.LastWrite(indexPool, "", shard).Steps()[2].(*radosclient.ExecStep).In))
		Expect(cancel.Op).To(Equal(rgwcls.OpCancel))
		Expect(cancel.Ver).To(Equal(rgwcls.EntryVer{Pool: -1, Epoch: 0}), "cls_obj_complete_cancel, rgw_rados.cc:9553-9563")
		en, _ := c.Entry(indexPool, "", shard, "k")
		Expect(en.Tag).To(Equal("tx-1"), "the first PUT's entry stands")
		Expect(en.Ver).To(Equal(rgwcls.EntryVer{Pool: -1, Epoch: 0}), "the class copies the cancel's version onto the entry (cls_rgw.cc:1094), tracker #80894")
	})
	It("refuses If-None-Match: * on an existing key before touching the index", func(ctx SpecContext) {
		_, err := s.PutObject(ctx, rec, key, strings.NewReader("x"), op.PutParams{Attrs: attrs, Size: 1, Tag: "t1"})
		Expect(err).NotTo(HaveOccurred())
		c.ResetCounters()
		_, err = s.PutObject(ctx, rec, key, strings.NewReader("y"), op.PutParams{Attrs: attrs, Size: 1, Tag: "t2", IfNoneMatch: "*"})
		Expect(err).To(MatchError(op.ErrPreconditionFailed))
		Expect(c.Writes(indexPool, "", shardOf(rec, "k"))).To(BeZero(), "prepare_atomic_modification fails before UpdateIndex::prepare")
		Expect(c.Reads(dataPool, "", rec.Info.Bucket.Marker+"_k")).To(Equal(1), "a precondition skips the assume-no-entry pass")
	})
	DescribeTable("conditional PUT per release",
		func(rel denc.Release, exists bool, ifMatch, ifNoneMatch string, want error) { /* open a store at rel; seed or not; PutObject; MatchError(want) or Succeed for nil */ },
		Entry("If-Match * on a missing key: 404 on Squid too", denc.Squid, false, "*", "", op.ErrNoSuchKey), // v19.2.6 answers 412; option B
		Entry("If-Match * on a missing key: Tentacle 404", denc.Tentacle, false, "*", "", op.ErrNoSuchKey),
		Entry("If-Match * on an existing key succeeds", denc.Squid, true, "*", "", nil),
		Entry("If-Match quoted etag on Squid is unquoted too and succeeds", denc.Squid, true, `"`+md5hex("x")+`"`, "", nil), // v19.2.6 compares it raw and fails; option B
		Entry("If-Match quoted etag on Tentacle is unquoted and succeeds", denc.Tentacle, true, `"`+md5hex("x")+`"`, "", nil),
		Entry("If-Match bare etag succeeds on Squid", denc.Squid, true, md5hex("x"), "", nil),
		Entry("If-None-Match * on a missing key succeeds", denc.Squid, false, "", "*", nil),
		Entry("If-None-Match matching etag fails", denc.Tentacle, true, "", md5hex("x"), op.ErrPreconditionFailed),
		Entry("If-None-Match other etag succeeds on Tentacle", denc.Tentacle, true, "", md5hex("other"), nil),
		Entry("If-None-Match other etag on a missing key succeeds on Squid too", denc.Squid, false, "", md5hex("other"), nil), // v19.2.6 answers 412, not ENOENT; option B
	)
	It("refuses a body whose MD5 is not the Content-MD5 and leaves no object behind", func(ctx SpecContext) {
		sum := md5.Sum([]byte("other"))
		_, err := s.PutObject(ctx, rec, key, bytes.NewReader(bytes.Repeat([]byte("c"), 5<<20)), op.PutParams{Attrs: attrs, Size: 5 << 20, Tag: "t", ContentMD5: sum[:]})
		Expect(err).To(MatchError(op.ErrBadDigest))
		Expect(c.Objects(dataPool, "")).To(BeEmpty(), "the streamed tail was deleted; no head was written")
		Expect(c.Writes(indexPool, "", shardOf(rec, "k"))).To(BeZero())
	})
	It("answers RequestTimeout when the body is shorter than its length", func(ctx SpecContext) {
		_, err := s.PutObject(ctx, rec, key, strings.NewReader("abc"), op.PutParams{Attrs: attrs, Size: 10, Tag: "t"})
		Expect(err).To(MatchError(op.ErrRequestTimeout), "rgw_op.cc:4405-4408")
	})
	It("stores an empty object as an empty head", func(ctx SpecContext) {
		res, err := s.PutObject(ctx, rec, key, strings.NewReader(""), op.PutParams{Attrs: attrs, Size: 0, Tag: "t"})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.ETag).To(Equal("d41d8cd98f00b204e9800998ecf8427e"))
		Expect(c.Object(dataPool, "", rec.Info.Bucket.Marker+"_k").Data).To(BeEmpty())
	})
	It("places a COLD object entirely in tails numbered from zero and records the class", func(ctx SpecContext) {
		res, err := s.PutObject(ctx, rec, key, bytes.NewReader(bytes.Repeat([]byte("d"), 5<<20)), op.PutParams{Attrs: attrs, Size: 5 << 20, Tag: "t", StorageClass: "COLD"})
		Expect(err).NotTo(HaveOccurred())
		head := c.Object(dataPool, "", rec.Info.Bucket.Marker+"_k")
		Expect(head.Data).To(BeEmpty())
		Expect(head.Xattrs).To(HaveKeyWithValue("user.rgw.storage_class", []byte("COLD")))
		Expect(c.Object("cold.data", "", rec.Info.Bucket.Marker+"__shadow_.AkTbQPoXT3s8QcthZt-DpS5NPRLqS5u_0")).NotTo(BeNil())
		Expect(c.Object("cold.data", "", rec.Info.Bucket.Marker+"__shadow_.AkTbQPoXT3s8QcthZt-DpS5NPRLqS5u_1")).NotTo(BeNil())
		m := meta.DecodeManifest(denc.NewDecoder(head.Xattrs["user.rgw.manifest"]))
		Expect(m.MaxHeadSize).To(BeZero())
		Expect(m.TailPlacement.PlacementRule).To(Equal(meta.PlacementRule{Name: "default-placement", StorageClass: "COLD"}))
		Eventually(func() string { en, _ := c.Entry(indexPool, "", shardOf(rec, "k"), "k"); return en.Meta.StorageClass }).Should(Equal("COLD"))
		_ = res
	})
	It("adjusts the quota cache with the new object and the size it replaced", func(ctx SpecContext) {
		// fakes the quota cache through s.StatsForTest(): after a 5-byte PUT objs +1 bytes +5; after overwriting it with 3 bytes objs +0, added 3, removed 5 (rgw_rados.cc:3358-3365)
	})
	It("checks the quota after the body arrives when the length was unknown", func(ctx SpecContext) {
		// a bucket quota of 4 bytes; Size -1 with a 5-byte body → ErrQuotaExceeded, no head, no index writes
	})
})
```

Write the four elided bodies in full. `md5hex`, `names`, `fixedRand` (returns the same string for every call, truncated to `n`), `seedGCShards`, `gcPoolName`/`gcNS` (from `s.ZoneParams().GCPool`) are spec helpers; `c.ResetCounters()`, `c.PoolID(name)`, `c.SetAlignment(pool, n)` are fakerados additions of this task (`RequiredAlignment` answers `SetAlignment`'s value, 0 by default). `ConsumeForTest` and `StatsForTest` are export_test additions; the latter wraps M's quota cache when it exists and a counting stub otherwise.

`internal/driver/gc_enqueue_test.go` pins `gcShard` against values computed outside Go: XXH64 of each tag with seed 8675309 from the xxHash C implementation ceph builds (`src/xxHash`, `XXH64`, 0.8.2 in the checkout used; its `XXH64("", 0)` is the published `0xef46db3751d8e999`), reduced with `rgw_shards_mod`'s C arithmetic. A wrong seed, a hash of the tag without its NUL, or a modulo over the whole 64-bit hash fails every row (the 64-bit modulo gives shards 24, 27, 0 and 19 for the four 32-shard rows):

```go
var _ = Describe("gcShard", func() {
	DescribeTable("is RGWGC::tag_index",
		func(ctx SpecContext, maxObjs, tag string, want int) {
			s, err := driver.Open(ctx, c, conf(map[string]string{"rgw_gc_max_objs": maxObjs}), driver.Options{})
			Expect(err).NotTo(HaveOccurred())
			Expect(driver.GCShardForTest(s, tag)).To(Equal(want))
		},
		// XXH64 with seed 8675309 (rgw_gc.h:27), from ceph's src/xxHash: "" 0xef287162ccb95a3a,
		// "tx-old\x00" 0xcfbf586c12d844fb, "tx-put\x00" 0xf69706c59eddf022, the 57-byte
		// transaction tag 0x5c08d368a6c0f208; rgw_shards_mod keeps the low 32 bits.
		Entry("an empty tag", "32", "", 13),
		Entry("a write tag with its NUL", "32", "tx-old\x00", 14),
		Entry("another write tag with its NUL", "32", "tx-put\x00", 24),
		Entry("a radosgw transaction id, longer than one 32-byte stripe", "32", "tx00000a1b2c3d4e5f6a7b8-0068d7a1b2-4155-ceph-objectstore\x00", 8),
		Entry("above 7877 shards the second prime reduces", "7878", "tx-old\x00", 864),
		Entry("at rgw_shards_max the low 32 bits modulo 65521", "65521", "tx00000a1b2c3d4e5f6a7b8-0068d7a1b2-4155-ceph-objectstore\x00", 47070),
	)
})
```

- [ ] **Step 5: Run to fail, then write `stripe.go`, `headwrite.go`, `gc_enqueue.go`, `put.go`**

`stripe.go`:

```go
package driver

// alignedSize is RGWRados::get_max_aligned_size (rgw_rados.cc:700-708).
func alignedSize(size, alignment uint64) uint64 {
	if alignment == 0 {
		return size
	}
	if size <= alignment {
		return alignment
	}
	return size - size%alignment
}

// poolAlignment caches Pool.RequiredAlignment per pool for the store's life,
// as radosgw caches it in get_required_alignment.
func (s *Store) poolAlignment(ctx context.Context, p radosclient.Pool) (uint64, error) {
	k := p.Name() + ":" + p.Namespace()
	s.w.alignMu.Lock()
	defer s.w.alignMu.Unlock()
	if a, ok := s.w.alignments[k]; ok {
		return a, nil
	}
	a, err := p.RequiredAlignment(ctx)
	if err != nil {
		return 0, op.FromRADOS(err, op.ScopeObject)
	}
	s.w.alignments[k] = a
	return a, nil
}

func (s *Store) planPut(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, storageClass string) (layout, error) {
	head := meta.Obj{Bucket: rec.Info.Bucket, Key: key}
	l := layout{headRule: rec.Info.PlacementRule}
	l.tailRule = meta.PlacementRule{StorageClass: storageClass}.InheritFrom(l.headRule)
	headRef, err := s.rawRef(ctx, l.headRule, head)
	if err != nil {
		return layout{}, err
	}
	l.headPool = headRef.pool
	align, err := s.poolAlignment(ctx, l.headPool)
	if err != nil {
		return layout{}, err
	}
	headChunk := alignedSize(s.w.opts.chunkSize, align)
	l.chunk = headChunk
	tailRef, err := s.rawRef(ctx, l.tailRule, head)
	if err != nil {
		return layout{}, err
	}
	l.tailPool = tailRef.pool
	samePool := l.tailPool.Name() == l.headPool.Name() && l.tailPool.Namespace() == l.headPool.Namespace()
	if !samePool {
		talign, err := s.poolAlignment(ctx, l.tailPool)
		if err != nil {
			return layout{}, err
		}
		l.chunk = alignedSize(s.w.opts.chunkSize, talign)
		l.maxHead = 0
	} else {
		pl, err := s.Placement(l.headRule) // missing placement → inline, as get_placement's failure does
		if err != nil || pl.InlineData {
			l.maxHead = headChunk
		}
	}
	l.stripe = alignedSize(s.w.opts.stripeSize, align)
	return l, nil
}
```

The `writer` gains `alignMu sync.Mutex; alignments map[string]uint64; bufs sync.Pool` (of `chunk`-sized `[]byte`, `chunk` fixed at Open since the pools' alignments are 0 under Rook; a pool with an alignment gets its own allocation). `tailWriter`:

```go
type tailWriter struct {
	s      *Store
	m      *meta.Manifest
	pool   radosclient.Pool
	chunk  uint64
	sem    *semaphore.Weighted // the put window in bytes
	wg     sync.WaitGroup
	mu     sync.Mutex
	issued []meta.Obj
	err    error
}

func (s *Store) newTailWriter(m *meta.Manifest, l layout) *tailWriter {
	return &tailWriter{s: s, m: m, pool: l.tailPool, chunk: l.chunk, sem: semaphore.NewWeighted(int64(s.w.opts.putWindow))} //nolint:gosec // 16 MiB
}

// write issues one piece of stripe n at offset ofs within it, under the
// window; the write runs under the driver's lifetime so a client disconnect
// cannot abandon it half-applied.
func (t *tailWriter) write(ctx context.Context, n uint64, ofs uint64, buf []byte) error {
	if len(buf) == 0 {
		return nil // RadosWriter::process: no empty writes
	}
	if err := t.sem.Acquire(ctx, int64(len(buf))); err != nil {
		return err
	}
	obj := t.m.TailObj(n)
	oid := meta.Stripe{Obj: obj}.OID()
	t.mu.Lock()
	if ofs == 0 {
		t.issued = append(t.issued, obj)
	}
	t.mu.Unlock()
	t.wg.Add(1)
	go func() {
		defer t.wg.Done()
		defer t.sem.Release(int64(len(buf)))
		w := radosclient.NewWriteOp()
		w.SetAllocHint(0, 0, 0)
		if ofs == 0 {
			w.WriteFull(buf)
		} else {
			w.Write(buf, ofs)
		}
		_, err := t.pool.Write(t.s.w.completions.ctx, oid, w, 0)
		if err == nil || !errors.Is(err, context.Canceled) {
			t.s.w.bufs.Put(buf[:cap(buf)]) // the completion fired: the buffer is ours again
		}
		if err != nil {
			t.mu.Lock()
			if t.err == nil {
				t.err = err
			}
			t.mu.Unlock()
		}
	}()
	return nil
}

// readPiece fills buf from r. done reports that r returned io.EOF, a complete
// body; io.ErrUnexpectedEOF from r itself (auth's truncated stream) is an
// error, while a short final piece is not.
func readPiece(r io.Reader, buf []byte) (n int, done bool, err error) {
	for n < len(buf) {
		k, rerr := r.Read(buf[n:])
		n += k
		switch {
		case rerr == nil:
		case errors.Is(rerr, io.EOF):
			return n, true, nil
		default:
			return n, false, rerr
		}
	}
	return n, false, nil
}

// bodyErr maps the reader's verdicts: io.ErrUnexpectedEOF is
// ErrRequestTimeout (rgw_op.cc:4405-4408, the body ended short); an *op.Error
// (auth's verification failures) and a context error pass through.
func bodyErr(err error) error {
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return op.ErrRequestTimeout
	}
	return err
}

func (t *tailWriter) consume(ctx context.Context, body io.Reader, h hash.Hash) ([]byte, uint64, error) {
	r := io.TeeReader(body, h)
	head := []byte{}
	done := false
	if t.m.MaxHeadSize > 0 {
		buf := make([]byte, t.m.MaxHeadSize)
		n, d, err := readPiece(r, buf)
		if err != nil {
			return nil, 0, bodyErr(err)
		}
		head, done = buf[:n], d
	}
	size := uint64(len(head))
	stripeMax := t.m.Rules[0].StripeMaxSize
	for !done {
		n := t.m.TailStripe(size)
		for off := uint64(0); off < stripeMax && !done; {
			want := min(t.chunk, stripeMax-off)
			buf := t.s.w.buf(want)
			got, d, err := readPiece(r, buf)
			if err != nil {
				t.s.w.bufs.Put(buf[:cap(buf)])
				return nil, 0, bodyErr(err)
			}
			done = d
			if got == 0 {
				t.s.w.bufs.Put(buf[:cap(buf)])
				continue
			}
			if size+uint64(got) > t.s.w.opts.maxPutSize {
				t.s.w.bufs.Put(buf[:cap(buf)])
				return nil, 0, op.ErrEntityTooLarge // rgw_rest.cc:1096-1098: ofs + len > rgw_max_put_size
			}
			if err := t.write(ctx, n, off, buf[:got]); err != nil {
				return nil, 0, err
			}
			off += uint64(got)
			size += uint64(got)
		}
	}
	return head, size, nil
}
```

`readPiece` reads until the buffer is full or the reader ends, so a stripe is written in `chunk`-sized pieces and the last piece is short; `w.buf(n)` takes a slice of length `n` from the `sync.Pool` (allocating when the pooled capacity is smaller). `drain` waits on `wg` and returns `t.err` wrapped by `op.FromRADOS`; `discard` waits, then removes every `issued` object with `radosclient.NewWriteOp()` + `Remove()` under `completions.ctx`, ENOENT ignored, a failure logged at warning `"tail object leaked after a failed write"` with the oid.

`headwrite.go`:

```go
package driver

// errRetryGuarded is _do_write_meta's -EEXIST on the assume-no-entry pass:
// the caller re-reads the head and runs the guarded pass.
var errRetryGuarded = errors.New("driver: object exists; retry guarded")

func entryOwner(attrs map[string][]byte) (string, string) {
	b, ok := attrs[meta.AttrACL]
	if !ok {
		return "", ""
	}
	d := denc.NewDecoder(b)
	p := acl.DecodePolicy(d)
	if d.Err() != nil {
		return "", ""
	}
	return p.Owner.ID, p.Owner.DisplayName
}

// rawTag is the stored user.rgw.idtag or tail_tag bytes, NUL included, as
// bufferlist::to_str keeps it; "" when absent.
func rawTag(st *op.ObjectState, name string) string { return string(st.Attrs[name]) }

func (s *Store) checkPreconditions(st *op.ObjectState, ifMatch, ifNoneMatch string) error {
	etag, hasETag := st.Attrs[meta.AttrETag]
	if s.release == denc.Squid { // prepare_atomic_modification, rgw_rados.cc:6502-6527
		if ifMatch != "" {
			if ifMatch == "*" {
				if !st.Exists {
					return op.ErrPreconditionFailed
				}
			} else if !hasETag || !strings.HasPrefix(ifMatch, string(etag)) { // strncmp(if_match, etag, etag.length()): the RAW header
				return op.ErrPreconditionFailed
			}
		}
		if ifNoneMatch != "" {
			if ifNoneMatch == "*" {
				if st.Exists {
					return op.ErrPreconditionFailed
				}
			} else if !hasETag || strings.HasPrefix(ifNoneMatch, string(etag)) {
				return op.ErrPreconditionFailed
			}
		}
		return nil
	}
	// check_preconditions, v20.2.4 rgw_rados.cc:7260-7329
	if ifMatch != "" {
		if ifMatch == "*" {
			if !st.Exists {
				return op.ErrNoSuchKey
			}
		} else if !hasETag {
			if !st.Exists {
				return op.ErrNoSuchKey
			}
			return op.ErrPreconditionFailed
		} else if !strings.HasPrefix(op.Unquote(ifMatch), string(etag)) {
			return op.ErrPreconditionFailed
		}
	}
	if ifNoneMatch != "" {
		if ifNoneMatch == "*" {
			if st.Exists {
				return op.ErrPreconditionFailed
			}
		} else if hasETag && strings.HasPrefix(op.Unquote(ifNoneMatch), string(etag)) {
			return op.ErrPreconditionFailed
		}
	}
	return nil
}
```

(`op.Unquote` is R Task 5's `rgw_string_unquote`; the Squid branch's `!hasETag` cases follow `!state->get_attr(RGW_ATTR_ETAG, bl) || …` → 412.) A note the spec table pins: Squid's `need_guard` is also true when a precondition is given on an object without a tag, so the cmpxattr goes out with an empty value and a missing object fails the op with ENOENT (`:6497-6500`), which `done_cancel` leaves as the error for an etag condition (`:3407-3415`).

```go
func (s *Store) writeMeta(ctx context.Context, hw *headWrite, x *indexOp) (headResult, error) {
	assumeNoent := hw.ifMatch == "" && hw.ifNoneMatch == ""
	var st *op.ObjectState
	if assumeNoent {
		st = &op.ObjectState{Key: hw.key}
	} else {
		var err error
		if st, err = s.readHead(ctx, hw.rec, hw.key, false); err != nil {
			return headResult{}, err
		}
	}
	res, err := s.doWriteMeta(ctx, hw, x, st, assumeNoent)
	if errors.Is(err, errRetryGuarded) {
		if st, err = s.readHead(ctx, hw.rec, hw.key, false); err != nil {
			return headResult{}, err
		}
		res, err = s.doWriteMeta(ctx, hw, x, st, false)
	}
	return res, err
}

func (s *Store) doWriteMeta(ctx context.Context, hw *headWrite, x *indexOp, st *op.ObjectState, assumeNoent bool) (headResult, error) {
	ref, err := s.headRef(ctx, hw.rec, hw.key)
	if err != nil {
		return headResult{}, err
	}
	w := radosclient.NewWriteOp()
	// prepare_atomic_modification (rgw_rados.cc:6484-6580; v20.2.4 :7331-7380 after check_preconditions)
	if s.release != denc.Squid {
		if err := s.checkPreconditions(st, hw.ifMatch, hw.ifNoneMatch); err != nil {
			return headResult{}, err
		}
	}
	fake := st.Manifest != nil && st.WriteTag == "" // generate_fake_tag: no guard
	var guard bool
	if s.release == denc.Squid {
		guard = (st.Manifest != nil || st.WriteTag != "" || hw.ifMatch != "" || hw.ifNoneMatch != "") && !fake
	} else {
		guard = (st.Manifest != nil || st.WriteTag != "") && !fake && hw.key.Instance == ""
	}
	if guard && hw.ifNoneMatch != "*" {
		w.CmpXattr(meta.AttrIDTag, radosclient.CmpEQ, st.Attrs[meta.AttrIDTag])
	}
	if s.release == denc.Squid {
		if err := s.checkPreconditions(st, hw.ifMatch, hw.ifNoneMatch); err != nil {
			return headResult{}, err
		}
	}
	if hw.create {
		if st.Exists {
			w.Create(false)
			rgw.ObjRemove(w, []string{meta.AttrOLHPrefix}, s.release) // remove_rgw_head_obj
		} else {
			w.Create(true)
		}
	}
	tagNUL := []byte(hw.tag + "\x00")
	w.SetXattr(meta.AttrIDTag, tagNUL)
	if hw.modifyTail {
		w.SetXattr(meta.AttrTailTag, tagNUL)
	}
	// _do_write_meta :3172-3259
	mtime := hw.mtime
	if mtime.IsZero() {
		mtime = s.now()
	}
	w.SetMtime(mtime)
	if hw.data != nil {
		// radosgw adds an incompressible alloc hint here only when this request
		// compressed the data: state->compressed is set by set_compressed alone
		// (rgw_rados.cc:217-221, :3202-3205; v20.2.4 :252-255, :3331), never from
		// the stored object's attrs, and nothing on this path compresses.
		w.WriteFull(hw.data)
	}
	for _, name := range hw.rmAttrs {
		w.RmXattr(name)
	}
	attrs := maps.Clone(hw.attrs)
	var storageClass string
	if hw.manifest != nil {
		storageClass = hw.manifest.TailPlacement.PlacementRule.StorageClass
		delete(attrs, meta.AttrManifest)
		w.SetXattr(meta.AttrManifest, encodeAt(hw.manifest, s.release))
	}
	var etag, contentType string
	for _, name := range slices.Sorted(maps.Keys(attrs)) {
		v := attrs[name]
		if len(v) == 0 {
			continue
		}
		w.SetXattr(name, v)
		switch name {
		case meta.AttrETag:
			etag = rgwBlStr(v)
		case meta.AttrContentType:
			contentType = rgwBlStr(v)
		}
	}
	if _, ok := attrs[meta.AttrPGVer]; !ok {
		rgw.ObjStorePGVer(w, meta.AttrPGVer, s.release)
	}
	if _, ok := attrs[meta.AttrSourceZone]; !ok {
		w.SetXattr(meta.AttrSourceZone, binary.LittleEndian.AppendUint32(nil, s.w.shortZoneID))
	}
	if storageClass != "" {
		w.SetXattr(meta.AttrStorageClass, []byte(storageClass))
	}
	origExists, origSize := st.Exists, st.Size
	if st.Compression != nil {
		origSize = st.Compression.OrigSize
	}
	if !hw.create { // multipart's immutable head
		origExists, origSize = false, 0
	}
	if !x.prepared {
		if err := x.prepare(ctx, rgw.OpAdd); err != nil {
			return headResult{}, op.FromRADOS(err, op.ScopeObject)
		}
	}
	epoch, err := ref.pool.Write(ctx, ref.oid, w, 0)
	if err != nil {
		if errors.Is(err, radosclient.ErrExists) && assumeNoent {
			return headResult{}, errRetryGuarded // :3300-3304
		}
		return s.cancelWrite(hw, x, err)
	}
	if st.Manifest != nil && !hw.keepTail { // complete_atomic_modification
		if chain, cerr := s.gcChain(st.Manifest, hw.rec.Info.PlacementRule); cerr == nil && len(chain) > 0 {
			tag := rawTag(st, meta.AttrTailTag)
			if tag == "" {
				tag = rawTag(st, meta.AttrIDTag)
			}
			s.enqueueGC(ctx, chain, tag)
		}
	}
	owner, display := entryOwner(attrs)
	// Category Main explicitly: cls_obj_complete_op copies ent.meta and sets
	// dir_meta.category = category (rgw_rados.cc:9497-9499 at v19.2.6), Main
	// for every head write.
	x.complete(rgw.EntryVer{Pool: ref.pool.ID(), Epoch: epoch}, rgw.DirEntryMeta{Category: rgw.CategoryMain, Size: hw.size, AccountedSize: hw.accountedSize, Mtime: mtime, ETag: etag, Owner: owner, OwnerDisplayName: display, ContentType: contentType, StorageClass: storageClass})
	added := int64(1)
	if origExists {
		added = 0
	}
	if err := s.AdjustStats(ctx, hw.rec, hw.rec.Info.Owner, added, int64(hw.accountedSize), int64(origSize)); err != nil { //nolint:gosec // sizes fit
		slog.WarnContext(ctx, "quota cache adjustment failed", slog.Any("error", err))
	}
	return headResult{epoch: epoch, poolID: ref.pool.ID(), mtime: mtime}, nil
}

// cancelWrite is done_cancel (rgw_rados.cc:3369-3416): the index entry is
// cancelled unless the write timed out, then the race is judged.
func (s *Store) cancelWrite(hw *headWrite, x *indexOp, err error) (headResult, error) {
	canceled := false
	if !errors.Is(err, radosclient.ErrTimedOut) {
		x.cancel() // index_op->cancel (:3371-3374)
		canceled = true
	}
	race := errors.Is(err, radosclient.ErrCanceled) || errors.Is(err, radosclient.ErrNotFound) || errors.Is(err, radosclient.ErrExists)
	switch {
	case hw.ifMatch == "" && hw.ifNoneMatch == "":
		if race {
			return headResult{canceled: canceled}, nil
		}
	default:
		if hw.ifMatch == "*" {
			if errors.Is(err, radosclient.ErrNotFound) {
				return headResult{canceled: canceled}, op.ErrPreconditionFailed
			}
			if errors.Is(err, radosclient.ErrCanceled) {
				return headResult{canceled: canceled}, nil
			}
		}
		if hw.ifNoneMatch == "*" {
			if errors.Is(err, radosclient.ErrExists) {
				return headResult{canceled: canceled}, op.ErrPreconditionFailed
			}
			if errors.Is(err, radosclient.ErrNotFound) {
				return headResult{canceled: canceled}, nil
			}
		}
	}
	return headResult{canceled: canceled}, op.FromRADOS(err, op.ScopeObject)
}
```

`rgwBlStr` strips every trailing NUL (`rgw_bl_str`); `encodeAt(v, r)` is M's encoder helper; the head write never adds radosgw's incompressible hint, which radosgw sends only for data this request compressed. The cancel's `x.cancel` is not awaited (Task 4), so `PutObject`'s discard of the tails runs concurrently with it; both are independent objects.

`gc_enqueue.go`:

```go
package driver

const gcSeed = 8675309 // RGWGC::seed, rgw_gc.h:27

// shardsMod is rgw_shards_mod (rgw_tools.h:46-55 at v19.2.6, :63-72 at
// v20.2.4) for a positive shard count. Its hval parameter is an unsigned int,
// so the 64-bit XXH64 that RGWGC::tag_index passes reaches the modulo as its
// low 32 bits.
func shardsMod(h uint64, shards uint32) int {
	hv := uint32(h) //nolint:gosec // rgw_shards_mod(unsigned hval, ...) keeps the low 32 bits
	if shards <= 7877 {
		return int(hv % 7877 % shards)
	}
	return int(hv % 65521 % shards)
}

// gcShard is RGWGC::tag_index (rgw_gc.cc:63-66): XXH64 of the tag's bytes,
// NUL included, under RGWGC::seed, reduced by rgw_shards_mod. xxhash/v2 has
// no seeded one-shot function, so the digest is built with NewWithSeed.
func (w *writer) gcShard(tag string) int {
	d := xxhash.NewWithSeed(gcSeed)
	_, _ = d.WriteString(tag)
	return shardsMod(d.Sum64(), w.opts.gcMaxObjs)
}

func (s *Store) gcChain(m *meta.Manifest, headRule meta.PlacementRule) ([]rgw.GCObj, error) {
	stripes, err := m.Stripes()
	if err != nil {
		return nil, err
	}
	var chain []rgw.GCObj
	for _, st := range stripes {
		if st.InHead {
			continue
		}
		pool, ok := s.dataPool(st.Placement, st.Obj.Bucket)
		if !ok {
			return nil, fmt.Errorf("%w: no data pool for stripe %s", op.ErrInternalError, st.OID())
		}
		chain = append(chain, rgw.GCObj{Pool: pool.String(), Key: rgw.ObjKey{Name: st.OID()}, Loc: st.Locator()})
	}
	return chain, nil
}

// encodedSize is the cls_rgw estimate_encoded_size family (cls_rgw_types.h:1149-1157, :1203-1211, :1254-1260; cls_rgw_ops.h:995-999).
func gcObjSize(o rgw.GCObj) int { return 6 + 4 + len(o.Pool) + 4 + len(o.Key.Name) + 4 + len(o.Loc) + 6 + 4 + len(o.Key.Name) + 4 + len(o.Key.Instance) }
func gcBaseSize(tag string) int  { return 6 + 4 + 6 + 4 + len(tag) + 8 + 6 + 4 }

func (s *Store) enqueueGC(ctx context.Context, chain []rgw.GCObj, tag string) {
	limit := int(s.w.opts.chunkSize) // rgw_max_chunk_size; a zero limit sends one chain (rgw_gc.cc:74, :109)
	var batch []rgw.GCObj
	size := gcBaseSize(tag)
	send := func(objs []rgw.GCObj) error {
		return s.sendGCChain(ctx, objs, tag)
	}
	flushLeft := func(from int) { s.deleteInline(ctx, chain[from:], tag) }
	for i, o := range chain {
		if limit > 0 && len(batch) > 0 && size+gcObjSize(o) > limit {
			if err := send(batch); err != nil {
				slog.WarnContext(ctx, "gc enqueue failed; deleting tails inline", slog.Any("error", err))
				flushLeft(i - len(batch))
				return
			}
			batch, size = batch[:0], gcBaseSize(tag)
		}
		batch = append(batch, o)
		size += gcObjSize(o)
	}
	if len(batch) > 0 {
		if err := send(batch); err != nil {
			slog.WarnContext(ctx, "gc enqueue failed; deleting tails inline", slog.Any("error", err))
			flushLeft(len(chain) - len(batch))
		}
	}
}

// sendGCChain is RGWGC::send_chain (rgw_gc.cc:120-138).
func (s *Store) sendGCChain(ctx context.Context, objs []rgw.GCObj, tag string) error {
	pool, err := s.pools.get(ctx, s.ZoneParams().GCPool)
	if err != nil {
		return err
	}
	oid := s.w.gcShards[s.w.gcShard(tag)]
	info := rgw.GCObjInfo{Tag: tag, Chain: objs}
	w := radosclient.NewWriteOp()
	version.Check(w, version.ObjVersion{Ver: 1}, version.CondEQ, s.release) // gc_log_enqueue2
	gc.QueueEnqueue(w, s.w.opts.gcObjMinWait, info, s.release)
	_, err = pool.Write(ctx, oid, w, 0)
	if err == nil || !(errors.Is(err, radosclient.ErrCanceled) || errors.Is(err, radosclient.ErrPermission)) {
		return err
	}
	w = radosclient.NewWriteOp()
	rgw.GCSetEntry(w, s.w.opts.gcObjMinWait, info, s.release) // the omap-era shard
	_, err = pool.Write(ctx, oid, w, 0)
	return err
}

// deleteInline is delete_objs_inline (rgw_rados.cc:5432-5465; v20.2.4
// :6155-6195): refcount put(tag, implicit) under pool_full_try on every
// object, rgw_multi_obj_del_max_aio at a time, errors logged.
func (s *Store) deleteInline(ctx context.Context, objs []rgw.GCObj, tag string) {
	sem := semaphore.NewWeighted(int64(s.w.opts.multiObjDelMaxAIO))
	var wg sync.WaitGroup
	for _, o := range objs {
		if err := sem.Acquire(ctx, 1); err != nil {
			break
		}
		wg.Add(1)
		go func(o rgw.GCObj) {
			defer wg.Done()
			defer sem.Release(1)
			pool, err := s.pools.get(ctx, meta.ParsePool(o.Pool))
			if err != nil {
				slog.WarnContext(ctx, "inline tail delete: no pool", slog.String("pool", o.Pool), slog.Any("error", err))
				return
			}
			if o.Loc != "" {
				pool = pool.WithLocator(o.Loc)
			}
			w := radosclient.NewWriteOp()
			refcount.Put(w, tag, true, s.release)
			if _, err := pool.Write(ctx, o.Key.Name, w, radosclient.OpFlagFullTry); err != nil && !errors.Is(err, radosclient.ErrNotFound) {
				slog.WarnContext(ctx, "inline tail delete failed", slog.String("oid", o.Key.Name), slog.Any("error", err))
			}
		}(o)
	}
	wg.Wait()
}
```

`put.go`:

```go
package driver

func (s *Store) PutObject(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, body io.Reader, p op.PutParams) (*op.PutResult, error) {
	if key.Name == "" {
		return nil, op.ErrInvalidArgument // _do_write_meta: "cannot write object with empty name" (-EIO); the op refuses it earlier
	}
	tag := p.Tag
	if tag == "" {
		tag = s.w.randTag()
	}
	l, err := s.planPut(ctx, rec, key, p.StorageClass)
	if err != nil {
		return nil, err
	}
	head := meta.Obj{Bucket: rec.Info.Bucket, Key: key}
	m := meta.NewTrivialManifest(head, l.headRule, l.tailRule, "."+s.w.rand(31)+"_", l.maxHead, l.stripe)
	tw := s.newTailWriter(&m, l)
	h := md5.New() //nolint:gosec // S3's ETag is MD5
	data, size, err := tw.consume(ctx, body, h)
	if err == nil && p.Size >= 0 && uint64(p.Size) != size {
		err = op.ErrRequestTimeout // rgw_op.cc:4405-4408: the body ended short of its Content-Length
	}
	if err == nil {
		err = tw.drain()
	}
	if err == nil {
		if qerr := s.CheckQuota(ctx, rec, rec.Info.Owner, int64(size), 1); qerr != nil { // the second check_quota, on the bytes received (rgw_op.cc:4418-4422)
			err = qerr
		}
	}
	sum := h.Sum(nil)
	if err == nil && p.ContentMD5 != nil && !bytes.Equal(sum, p.ContentMD5) {
		err = op.ErrBadDigest
	}
	if err != nil {
		tw.discard()
		return nil, err
	}
	etag := p.ETag
	if etag == "" {
		etag = hex.EncodeToString(sum)
	}
	m.SetObjSize(size)
	attrs := maps.Clone(p.Attrs)
	if attrs == nil {
		attrs = map[string][]byte{}
	}
	attrs[meta.AttrETag] = []byte(etag)
	hw := &headWrite{rec: rec, key: key, tag: tag, data: data, manifest: &m, attrs: attrs, mtime: p.Mtime, ifMatch: p.IfMatch, ifNoneMatch: p.IfNoneMatch, create: true, modifyTail: true, size: size, accountedSize: size}
	x := s.newIndexOp(rec, key, tag)
	res, err := s.writeMeta(ctx, hw, x)
	if err != nil {
		if !errors.Is(err, radosclient.ErrTimedOut) { // ETIMEDOUT: the head may still land; the tails stay, as writer.clear_written leaves them
			tw.discard()
		}
		return nil, err
	}
	if res.canceled {
		tw.discard() // the race was lost: ~RadosWriter removes what this request wrote
	}
	return &op.PutResult{ETag: etag, Size: size, Mtime: res.mtime, Epoch: res.epoch}, nil
}
```

`data` for a zero-length body is `[]byte{}` (non-nil) so the head still gets `WriteFull`. When `tw.consume` returned a body verification error (an `*op.Error` from A), it comes back verbatim. `store.go` loses the `PutObject` stub; `export_test.go` gains the exports above.

- [ ] **Step 6: Run the suites, lint, commit**

Run: `make generate-check && go test -tags=ceph_preview -race ./internal/op/... ./internal/memstore/... ./internal/testutil/fakerados/... ./internal/driver/... && golangci-lint run ./internal/...`
Expected: PASS.

`make check`, then `feat(driver): PutObject with radosgw's streamed tails, guarded head write and GC enqueue`. Draft PR `feat(driver): PutObject (unit W task 5)`; its description names W-D1, W-D8, W-D9, W-D10, W-D11 and the two additive `PutParams` fields.

---
### Task 6: `driver`: `DeleteObject`; `DeleteParams.UnmodifiedSince`, `IfMatchSize`, `IfMatchLastModified`

**Files:**
- Create: `internal/driver/delete.go`, `internal/driver/delete_test.go`
- Modify: `internal/op/object.go` (`DeleteParams` fields, W-D12), `internal/op/opfakes/` (regenerate), `internal/memstore/object.go` (honour the four conditions), `internal/driver/store.go` (the `DeleteObject` stub goes), `internal/testutil/fakerados/pool.go` (`Cluster.FailNextWrite(pool, ns, oid string, errno syscall.Errno)`; write records expose `Flags()`), `internal/testutil/fakerados/cls_rgw.go` (`obj_check_mtime`); from Step 5 on: `internal/driver/list.go` (M Task 8's `checkDiskState` gains the multipart-part sweep at the comment it leaves); `docs/exclusions.md` (W-D5, Step 4)

**Interfaces:**
- Consumes: Task 4's `indexOp` (`prepare`, `completeDel`, `cancel`), Task 5's `enqueueGC`, `gcChain`, `rawTag`, `checkPreconditions`; R's `readHead`, `headRef`; `rgw.ObjRemove`, `rgw.ObjCheckMtime`, `rgw.MtimeCheck` constants (`MtimeLE`, `MtimeEQ`, phase 0's names, [`const.go:99-103`](https://github.com/jhoblitt/rgw-go/blob/50fc45c4d59953661d5c57c27ca7016bc55edeeb/internal/cls/rgw/const.go#L99-L103)); `radosclient.OpFlagFullTry`, `ErrTimedOut`, `ErrNotFound`, `ErrCanceled`; `op.ErrNoSuchKey`, `op.ErrPreconditionFailed`, `op.ErrConcurrentModification`; for Steps 5-7: M Task 8's `checkDiskState` and its `list_test.go` helpers (`seedIndexEntry`, `entry`, `shardOf`), R Task 1's `meta.Manifest.Seek`/`StripeIter.Location`, `meta.NSMultipart`, `rgw.EntryVer`.
- Produces:

```go
package op

// DeleteParams gains:
//	// UnmodifiedSince is x-amz-delete-if-unmodified-since: the object must not
//	// have changed after it; zero means unset (Delete::delete_obj,
//	// rgw_rados.cc:5869-5886, second precision).
//	UnmodifiedSince time.Time
//	// IfMatchSize is x-amz-if-match-size (Tentacle); nil means unset.
//	IfMatchSize *uint64
//	// IfMatchLastModified is x-amz-if-match-last-modified-time (Tentacle),
//	// compared at second precision; zero means unset.
//	IfMatchLastModified time.Time
```

```go
package driver

// DeleteObject is RGWRados::Object::Delete::delete_obj's unversioned path
// (rgw_rados.cc:5862-5987; v20.2.4 :6600-6725): one head stat, the guarded
// index prepare DEL under a fresh "_" tag, the head obj_remove under
// pool_full_try, complete_del off the request path, the GC enqueue of the
// tails, the quota adjustment. A missing key is ErrNoSuchKey; a lost race is
// ErrConcurrentModification, which the op answers 204 as radosgw does.
func (s *Store) DeleteObject(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, p op.DeleteParams) error
```

The RADOS sequence: `readHead` (1); `prepare DEL` (2); the head op (3): on Squid `[CmpXattr(idtag) when the state has a real tag] [obj_check_mtime LE unmod] Exec(obj_remove, keep "user.rgw.olh.")` with `OpFlagFullTry`; on Tentacle no CmpXattr (`delete_obj` calls `check_preconditions`, not `prepare_atomic_modification`), `[obj_check_mtime LE unmod] [obj_check_mtime EQ last-modified-match]` then the remove. Then `completeDel(ver{pool, epoch}, st.Mtime)` (not awaited) and the GC enqueue (a fourth synchronous op when the object had tails).

- [ ] **Step 1: Contract fields, memstore, fakerados**

`internal/op/object.go`: the three fields with the comments above; `make generate`. `memstore.DeleteObject`: `UnmodifiedSince` non-zero and the object's mtime (truncated to seconds) after it → `ErrPreconditionFailed`; `IfMatch` non-empty and `Unquote(IfMatch)` not a prefix of the etag → `ErrPreconditionFailed`; `IfMatchSize` set and unequal → `ErrPreconditionFailed`; `IfMatchLastModified` set and unequal at second precision → `ErrPreconditionFailed`. fakerados: `FailNextWrite` makes the next write op to that oid fail with the errno without applying it; `Cluster.LastWrite(...)` records gain `Flags() radosclient.OpFlags`; `RGWClass` gains `obj_check_mtime` (decode `CheckMtimeOp`; compare the object's mtime under the `MtimeCheck` type at second precision unless `HighPrecisionTime`; failure is `-ECANCELED`, as cls_rgw returns).

- [ ] **Step 2: Write the failing specs**

`internal/driver/delete_test.go`, on the Task 5 fixture with a 6 MiB object `k` written by `PutObject` under tag `tx-put`:

```go
var _ = Describe("DeleteObject", func() {
	It("deletes with radosgw's stat, prepare, guarded remove, complete_del and GC enqueue", func(ctx SpecContext) {
		headOID := rec.Info.Bucket.Marker + "_k"
		tail := rec.Info.Bucket.Marker + "__shadow_.AkTbQPoXT3s8QcthZt-DpS5NPRLqS5u_1"
		epoch := c.Object(dataPool, "", headOID).Version
		c.ResetCounters()
		Expect(s.DeleteObject(ctx, rec, key, op.DeleteParams{})).To(Succeed())
		Expect(c.Reads(dataPool, "", headOID)).To(Equal(1))
		Expect(c.Object(dataPool, "", headOID)).To(BeNil())
		w := c.LastWrite(dataPool, "", headOID)
		Expect(w.Flags()).To(Equal(radosclient.OpFlagFullTry), "ioctx.set_pool_full_try, rgw_rados.cc:5943")
		steps := w.Steps()
		Expect(steps[0]).To(Equal(&radosclient.CmpXattrStep{Name: "user.rgw.idtag", Op: radosclient.CmpEQ, Value: []byte("tx-put\x00")}), "Squid guards the delete")
		rm := steps[1].(*radosclient.ExecStep)
		Expect(rm.Method).To(Equal("obj_remove"))
		Expect(rgwcls.DecodeObjRemoveOp(denc.NewDecoder(rm.In)).KeepAttrPrefixes).To(Equal([]string{"user.rgw.olh."}))
		Expect(steps).To(HaveLen(2))
		shard := shardOf(rec, "k")
		prep := rgwcls.DecodePrepareOp(denc.NewDecoder(c.LastWrite(indexPool, "", shard).Steps()[2].(*radosclient.ExecStep).In))
		Expect(prep.Op).To(Equal(rgwcls.OpDel))
		Expect(prep.Tag).To(HavePrefix("_"))
		Expect(prep.Tag).To(HaveLen(32), "append_rand_alpha: the removal sets no write tag")
		Eventually(func() int { return c.Writes(indexPool, "", shard) }).Should(Equal(4), "put prepare+complete, delete prepare+complete_del")
		comp := rgwcls.DecodeCompleteOp(denc.NewDecoder(c.LastWrite(indexPool, "", shard).Steps()[2].(*radosclient.ExecStep).In))
		Expect(comp.Op).To(Equal(rgwcls.OpDel))
		Expect(comp.Ver).To(Equal(rgwcls.EntryVer{Pool: c.PoolID(dataPool), Epoch: epoch + 1}), "the remove's version")
		Expect(comp.Meta.Category).To(Equal(rgwcls.CategoryNone))
		Expect(comp.Meta.Mtime).To(Equal(putMtime), "the removed object's mtime")
		_, ok := c.Entry(indexPool, "", shard, "k")
		Expect(ok).To(BeFalse())
		Expect(c.Header(indexPool, "", shard).Stats[rgwcls.CategoryMain].NumEntries).To(BeZero())
		entries := c.GCEntries(gcPoolName, gcNS, fmt.Sprintf("gc.%d", driver.GCShardForTest(s, "tx-put\x00")))
		Expect(entries).To(HaveLen(1))
		Expect(entries[0].Chain[0].Key.Name).To(Equal(tail))
		Expect(c.Object(dataPool, "", tail)).NotTo(BeNil(), "tails wait for the GC worker")
	})
	It("does not guard on Tentacle and honours the three match headers", func(ctx SpecContext) {
		t := denc.Tentacle
		s2, _ := driver.Open(ctx, c, conf(nil), driver.Options{Release: &t})
		headOID := rec.Info.Bucket.Marker + "_k"
		st := c.Object(dataPool, "", headOID)
		wrong := uint64(1)
		Expect(s2.DeleteObject(ctx, rec, key, op.DeleteParams{IfMatchSize: &wrong})).To(MatchError(op.ErrPreconditionFailed))
		Expect(s2.DeleteObject(ctx, rec, key, op.DeleteParams{IfMatch: `"nope"`})).To(MatchError(op.ErrPreconditionFailed))
		Expect(s2.DeleteObject(ctx, rec, key, op.DeleteParams{IfMatchLastModified: putMtime.Add(time.Hour)})).To(MatchError(op.ErrPreconditionFailed))
		Expect(c.Writes(indexPool, "", shardOf(rec, "k"))).To(Equal(1), "no delete prepare went out (only the put's)")
		size := uint64(6 << 20)
		Expect(s2.DeleteObject(ctx, rec, key, op.DeleteParams{IfMatch: `"` + md5hex6MiB + `"`, IfMatchSize: &size, IfMatchLastModified: putMtime})).To(Succeed())
		steps := c.LastWrite(dataPool, "", headOID).Steps()
		Expect(steps[0].(*radosclient.ExecStep).Method).To(Equal("obj_check_mtime"), "cls_obj_check_mtime EQ for last-modified-match, v20.2.4 :6259-6262")
		Expect(steps[1].(*radosclient.ExecStep).Method).To(Equal("obj_remove"))
		Expect(steps).To(HaveLen(2), "no cmpxattr on Tentacle")
		_ = st
	})
	It("answers NoSuchKey for a missing head without touching the index", func(ctx SpecContext) {
		err := s.DeleteObject(ctx, rec, meta.ObjKey{Name: "missing"}, op.DeleteParams{})
		Expect(err).To(MatchError(op.ErrNoSuchKey))
		Expect(c.Writes(indexPool, "", shardOf(rec, "missing"))).To(BeZero(), "rgw_rados.cc:5900-5904: ENOENT before the prepare")
	})
	It("leaves a stale index entry whose head is gone to the listing", func(ctx SpecContext) {
		// seed an index entry `stale` (Exists true) with no head object: DeleteObject → ErrNoSuchKey, the entry untouched; check_disk_state removes it on the next listing
	})
	It("completes the delete when the head vanished between the stat and the remove", func(ctx SpecContext) {
		headOID := rec.Info.Bucket.Marker + "_k"
		c.BeforeWrite(dataPool, "", headOID, func(*fakerados.Object) { c.Delete(dataPool, "", headOID) })
		Expect(s.DeleteObject(ctx, rec, key, op.DeleteParams{})).To(Succeed(), "ENOENT from the remove is success, rgw_rados.cc:5954-5966")
		shard := shardOf(rec, "k")
		Eventually(func() int { return c.Writes(indexPool, "", shard) }).Should(Equal(4))
		comp := rgwcls.DecodeCompleteOp(denc.NewDecoder(c.LastWrite(indexPool, "", shard).Steps()[2].(*radosclient.ExecStep).In))
		Expect(comp.Op).To(Equal(rgwcls.OpDel))
		Expect(comp.Ver.Epoch).To(BeZero(), "the failed remove returned no version")
	})
	It("cancels with radosgw's -1:0 and reports the race when the tag changed underneath", func(ctx SpecContext) {
		headOID := rec.Info.Bucket.Marker + "_k"
		c.BeforeWrite(dataPool, "", headOID, func(o *fakerados.Object) { o.Xattrs["user.rgw.idtag"] = []byte("tx-racer\x00") })
		err := s.DeleteObject(ctx, rec, key, op.DeleteParams{})
		Expect(err).To(MatchError(op.ErrConcurrentModification), "ECANCELED; the op answers 204 (rgw_op.cc:5302-5304)")
		shard := shardOf(rec, "k")
		Eventually(func() int { return c.Writes(indexPool, "", shard) }).Should(Equal(4))
		cancel := rgwcls.DecodeCompleteOp(denc.NewDecoder(c.LastWrite(indexPool, "", shard).Steps()[2].(*radosclient.ExecStep).In))
		Expect(cancel.Op).To(Equal(rgwcls.OpCancel))
		Expect(cancel.Ver).To(Equal(rgwcls.EntryVer{Pool: -1, Epoch: 0}), "index_op.cancel, rgw_rados.cc:5968-5972")
		Expect(c.Object(dataPool, "", headOID)).NotTo(BeNil(), "the racer's object stands")
	})
	It("leaves the pending entry alone when the remove times out", func(ctx SpecContext) {
		headOID := rec.Info.Bucket.Marker + "_k"
		c.FailNextWrite(dataPool, "", headOID, syscall.ETIMEDOUT)
		err := s.DeleteObject(ctx, rec, key, op.DeleteParams{})
		Expect(err).To(HaveOccurred())
		shard := shardOf(rec, "k")
		Consistently(func() int { return c.Writes(indexPool, "", shard) }).WithTimeout(50 * time.Millisecond).Should(Equal(3), "prepare DEL only: neither complete_del nor cancel, rgw_rados.cc:5951-5953")
		en, _ := c.Entry(indexPool, "", shard, "k")
		Expect(en.PendingMap).To(HaveLen(1), "the listing's check_disk_state reconciles it")
	})
	It("refuses a delete of an object modified after x-amz-delete-if-unmodified-since, and checks it in the op otherwise", func(ctx SpecContext) {
		Expect(s.DeleteObject(ctx, rec, key, op.DeleteParams{UnmodifiedSince: putMtime.Add(-time.Hour)})).To(MatchError(op.ErrPreconditionFailed))
		Expect(s.DeleteObject(ctx, rec, key, op.DeleteParams{UnmodifiedSince: putMtime.Add(time.Hour)})).To(Succeed())
		steps := c.LastWrite(dataPool, "", rec.Info.Bucket.Marker+"_k").Steps()
		chk := steps[1].(*radosclient.ExecStep)
		Expect(chk.Method).To(Equal("obj_check_mtime"), "after the cmpxattr, before the remove")
		Expect(rgwcls.DecodeCheckMtimeOp(denc.NewDecoder(chk.In)).HighPrecisionTime).To(BeFalse(), "S3 requests compare at second precision")
	})
	It("adjusts the quota cache by one object and the accounted size", func(ctx SpecContext) {
		// s.StatsForTest(): after the delete objs -1, removed 6 MiB (rgw_rados.cc:5984)
	})
})
```

Write the two elided bodies in full.

- [ ] **Step 3: Run to fail, write `delete.go`**

```go
package driver

func (s *Store) DeleteObject(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, p op.DeleteParams) error {
	st, err := s.readHead(ctx, rec, key, false)
	if err != nil {
		return err
	}
	if !st.Exists {
		return op.ErrNoSuchKey // :5900-5904
	}
	ref, err := s.headRef(ctx, rec, key)
	if err != nil {
		return err
	}
	w := radosclient.NewWriteOp()
	if s.release == denc.Squid && st.WriteTag != "" { // prepare_atomic_modification(removal_op): append the guard only
		w.CmpXattr(meta.AttrIDTag, radosclient.CmpEQ, st.Attrs[meta.AttrIDTag])
	}
	if !p.UnmodifiedSince.IsZero() { // :5869-5886
		if st.Mtime.Truncate(time.Second).After(p.UnmodifiedSince.Truncate(time.Second)) {
			return op.ErrPreconditionFailed
		}
		rgw.ObjCheckMtime(w, p.UnmodifiedSince, rgw.MtimeLE, false, s.release)
	}
	if s.release != denc.Squid { // check_preconditions, v20.2.4 :6254-6262
		if p.IfMatchSize != nil && *p.IfMatchSize != st.Size {
			return op.ErrPreconditionFailed
		}
		if !p.IfMatchLastModified.IsZero() {
			if !st.Mtime.Truncate(time.Second).Equal(p.IfMatchLastModified.Truncate(time.Second)) {
				return op.ErrPreconditionFailed
			}
			rgw.ObjCheckMtime(w, p.IfMatchLastModified, rgw.MtimeEQ, false, s.release)
		}
		if p.IfMatch != "" {
			if err := s.checkPreconditions(st, p.IfMatch, ""); err != nil {
				return err
			}
		}
	}
	accounted := st.Size
	if st.Compression != nil {
		accounted = st.Compression.OrigSize
	}
	x := s.newIndexOp(rec, key, "") // "_" + 31 random: the removal never sets write_tag
	if err := x.prepare(ctx, rgw.OpDel); err != nil {
		return op.FromRADOS(err, op.ScopeObject)
	}
	rgw.ObjRemove(w, []string{meta.AttrOLHPrefix}, s.release) // remove_rgw_head_obj
	epoch, err := ref.pool.Write(ctx, ref.oid, w, radosclient.OpFlagFullTry)
	switch {
	case errors.Is(err, radosclient.ErrTimedOut):
		// :5951-5953: neither complete_del nor cancel; the listing reconciles the pending entry
		return op.FromRADOS(err, op.ScopeObject)
	case err == nil || errors.Is(err, radosclient.ErrNotFound):
		x.completeDel(rgw.EntryVer{Pool: ref.pool.ID(), Epoch: epoch}, st.Mtime)
		if st.Manifest != nil {
			if chain, cerr := s.gcChain(st.Manifest, rec.Info.PlacementRule); cerr == nil && len(chain) > 0 {
				tag := rawTag(st, meta.AttrTailTag)
				if tag == "" {
					tag = rawTag(st, meta.AttrIDTag)
				}
				s.enqueueGC(ctx, chain, tag)
			}
		}
	default:
		x.cancel() // index_op.cancel (:5968-5972)
		return op.FromRADOS(err, op.ScopeObject)
	}
	if err := s.AdjustStats(ctx, rec, rec.Info.Owner, -1, 0, int64(accounted)); err != nil { //nolint:gosec // sizes fit
		slog.WarnContext(ctx, "quota cache adjustment failed", slog.Any("error", err))
	}
	return nil
}
```

`op.FromRADOS` maps `ErrCanceled` to `ErrConcurrentModification` (G Task 1's table, as R-D18 relies on); the DeleteObject op turns that into success. `store.go` loses the `DeleteObject` stub. W-D5: the tombstone cache is not built; Step 4 records it in `docs/exclusions.md`.

- [ ] **Step 4: Record W-D5, run, lint, commit**

`docs/exclusions.md`, under "Multisite", after its "Still required" paragraph:

> radosgw also keeps an in-memory tombstone cache of deleted objects' mtimes and versions (`rgw_obj_tombstone_cache_size`) for multisite's remote fetch and the Swift object expirer, and builds it only when another zone syncs from its zone. rgw-go never builds it, which is what radosgw does on every single-zone deployment.

Run: `make generate-check && go test -tags=ceph_preview -race ./internal/op/... ./internal/memstore/... ./internal/testutil/fakerados/... ./internal/driver/...`
Expected: PASS. `make check`, then `feat(driver): DeleteObject with radosgw's guarded remove, complete_del and GC enqueue`, with `docs/exclusions.md` in the commit. Draft PR `feat(driver): DeleteObject (unit W task 6)`; its description states that the `docs/exclusions.md` change must be announced to the rgw-rs session.

- [ ] **Step 5: Write the failing `deleteObjIndex` sweep specs**

`check_disk_state` ([rgw_rados.cc:10323-10475](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L10323-L10475) at v19.2.6; v20.2.4 [:11247-11399](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L11247-L11399)), which M Task 8's `checkDiskState` transcribes, returns a `CEPH_RGW_REMOVE` suggestion and `-ENOENT` as soon as the head does not exist ([:10362-10378](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L10362-L10378)); for a head that does exist it walks the manifest and calls `delete_obj_index` on every stripe in the `multipart` namespace ([:10413-10429](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L10413-L10429); v20.2.4 [:11337-11353](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L11337-L11353)). So when a listing reconciles a multipart-completed object whose index entry never left the pending state — the completion's `complete` with its `remove_objs` did not land — the part entries the manifest names are retired with it. M's `checkDiskState` leaves a comment where this step inserts the sweep.

`delete_obj_index` ([:6033-6050](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6033-L6050); v20.2.4 [:6787-6804](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6787-L6804)) is `UpdateIndex::complete_del(dpp, -1 /* pool */, 0, mtime, nullptr, y)` on an `UpdateIndex` that was never prepared: a `bucket_complete_op` DEL with an EMPTY tag, `ver {pool: -1, epoch: 0}`, the head's mtime as `meta.mtime` (`cls_obj_complete_del`, [:9536-9551](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L9536-L9551)) and no `remove_objs`. The class accepts the empty tag (the pending-map lookup at [cls_rgw.cc:1066-1072](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L1066-L1072) runs only for a non-empty one) and, for an entry that is not on disk, logs "not on disk, no action" and erases nothing ([:1127-1140](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L1127-L1140)). The shard is the one the PART's own name hashes to: the stripe's `rgw_obj` comes from `raw_obj_to_obj` ([svc_tier_rados.h:131-143](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_tier_rados.h#L131-L143)), which parses the raw oid and leaves `index_hash_source` empty, so `get_hash_object()` falls back to `key.name` (`<key>.<upload>.<n>`); the part's entry, though, was written under the head's name as hash source (`head_obj.index_hash_source = target_obj.key.name`, [rgw_putobj_processor.cc:467](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_putobj_processor.cc#L467)), so on a bucket with more than one shard radosgw's DEL usually lands on a shard that has no such entry and the entry stays for `bucket check` to report as `invalid_multipart_entries`. rgw-go reproduces exactly that, wrong shard included, because the bytes on the wire are the parity target; the radosgw defect is recorded in `docs/ceph-upstream-bugs.md` ("check_disk_state removes a multipart part's index entry from the wrong shard", tracker #81121) rather than corrected here. `newIndexOp` substitutes a random tag for `""` (`UpdateIndex::prepare`'s behaviour), and a tag the pending map does not hold is refused with `-EINVAL` ([cls_rgw.cc:1066-1072](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L1066-L1072)), so the op is built by hand.

`internal/driver/delete_test.go` gains, on the Task 5 fixture (`rec` has 11 shards; `seedIndexEntry`, `entry`, `shardOf` and the `PendingMap` seeding are M Task 8's `list_test.go` helpers in the same `driver_test` package; `partNameOnShard(rec, head, same)` walks `fmt.Sprintf("%s.2~u.%d", head, i)` for `i` from 1 until `shardOf(rec, name) == shardOf(rec, head)` equals `same`, which 11 shards settle within a few iterations):

```go
var _ = Describe("checkDiskState's multipart-part sweep", func() {
	// An explicit manifest is the shortest way to name a part head:
	// one piece in the head, one in the multipart namespace (get_implicit_location
	// puts a part's first stripe there, rgw_obj_manifest.cc:236-239).
	seedMultipartHead := func(ctx context.Context, head, part string) time.Time {
		_, err := s.PutObject(ctx, rec, meta.ObjKey{Name: head}, bytes.NewReader(bytes.Repeat([]byte("h"), 1<<20)), op.PutParams{Attrs: attrsWithACL(alice), Size: 1 << 20, ETag: etagOf("h", 1<<20)})
		Expect(err).NotTo(HaveOccurred())
		m := &meta.Manifest{ExplicitObjs: true, ObjSize: 2 << 20, Obj: meta.Obj{Bucket: rec.Info.Bucket, Key: meta.ObjKey{Name: head}}}
		m.Objs = map[uint64]meta.ManifestPart{
			0:       {Loc: meta.Obj{Bucket: rec.Info.Bucket, Key: meta.ObjKey{Name: head}}, Size: 1 << 20},
			1 << 20: {Loc: meta.Obj{Bucket: rec.Info.Bucket, Key: meta.ObjKey{Name: part, NS: meta.NSMultipart}}, Size: 1 << 20},
		}
		obj := c.Object(dataPool, "", rec.Info.Bucket.Marker+"_"+head)
		obj.Xattrs[meta.AttrManifest] = encode(m)
		st, err := s.StatObject(ctx, rec, meta.ObjKey{Name: head})
		Expect(err).NotTo(HaveOccurred())
		// the head's entry never left pending: the complete did not land
		pending := entry(head, 1<<20)
		pending.Exists = false
		pending.PendingMap = []rgwcls.PendingEntry{{Tag: st.WriteTag, Info: rgwcls.PendingInfo{State: rgwcls.PendingModify, Timestamp: st.Mtime, Op: uint8(rgwcls.OpAdd)}}}
		headShard := fmt.Sprintf(".dir.%s.%d", rec.Info.Bucket.ID, shardOf(rec, head))
		c.Object(indexPool, "", headShard).Omap[head] = encode(pending)
		// the part's entry sits where the part writer put it: hashed on the HEAD's name
		seedIndexEntry(c, indexPool, headShard, entry("_multipart_"+part, 1<<20))
		return st.Mtime
	}
	It("sends delete_obj_index's complete_del for every multipart stripe to the shard the part's own name hashes to", func(ctx SpecContext) {
		part := partNameOnShard(rec, "mp", false)
		mtime := seedMultipartHead(ctx, "mp", part)
		partShard := fmt.Sprintf(".dir.%s.%d", rec.Info.Bucket.ID, shardOf(rec, part))
		headShard := fmt.Sprintf(".dir.%s.%d", rec.Info.Bucket.ID, shardOf(rec, "mp"))
		c.ResetCounters()
		res, err := s.ListObjects(ctx, rec, op.ListObjectsParams{MaxKeys: 10})
		Expect(err).NotTo(HaveOccurred())
		Expect(keyNames(res.Entries)).To(Equal([]string{"mp"}), "the head is reconciled and kept")
		var del rgwcls.CompleteOp
		Eventually(func() bool {
			w := lastExecIn(c.WritesTo(indexPool, "", partShard), "bucket_complete_op", rgwcls.OpDel)
			if w == nil {
				return false
			}
			del = rgwcls.DecodeCompleteOp(denc.NewDecoder(w))
			return true
		}).Should(BeTrue(), "delete_obj_index, rgw_rados.cc:6033-6050")
		Expect(del.Tag).To(BeEmpty(), "UpdateIndex never prepared: optag empty")
		Expect(del.Ver).To(Equal(rgwcls.EntryVer{Pool: -1, Epoch: 0}))
		Expect(del.Key.Name).To(Equal("_multipart_" + part))
		Expect(del.Meta.Mtime).To(BeTemporally("==", mtime), "cls_obj_complete_del: ent.meta.mtime = astate->mtime")
		Expect(del.RemoveObjs).To(BeEmpty())
		Expect(lastExecIn(c.WritesTo(indexPool, "", headShard), "bucket_complete_op", rgwcls.OpDel)).To(BeNil(), "nothing is sent to the shard that holds the entry")
		_, ok := c.Entry(indexPool, "", headShard, "_multipart_"+part)
		Expect(ok).To(BeTrue(), "radosgw's sweep misses the entry on a multi-shard bucket: 'not on disk, no action' on the wrong shard (cls_rgw.cc:1127-1140); bucket check reports it")
	})
	It("retires the entry when the part's name hashes to the shard that holds it", func(ctx SpecContext) {
		part := partNameOnShard(rec, "mp", true)
		seedMultipartHead(ctx, "mp", part)
		shard := fmt.Sprintf(".dir.%s.%d", rec.Info.Bucket.ID, shardOf(rec, "mp"))
		_, err := s.ListObjects(ctx, rec, op.ListObjectsParams{MaxKeys: 10})
		Expect(err).NotTo(HaveOccurred())
		Eventually(func() bool { _, ok := c.Entry(indexPool, "", shard, "_multipart_"+part); return ok }).Should(BeFalse())
		Expect(c.Header(indexPool, "", shard).Stats[rgwcls.CategoryMain].NumEntries).To(BeEquivalentTo(1), "unaccount_entry dropped the part; the head stays")
	})
	It("sends nothing for a head whose manifest has no multipart stripe and nothing for a vanished head", func(ctx SpecContext) {
		_, err := s.PutObject(ctx, rec, meta.ObjKey{Name: "plain"}, bytes.NewReader([]byte("x")), op.PutParams{Attrs: attrsWithACL(alice), Size: 1, ETag: etagOf("x", 1)})
		Expect(err).NotTo(HaveOccurred())
		shard := fmt.Sprintf(".dir.%s.%d", rec.Info.Bucket.ID, shardOf(rec, "plain"))
		pending := entry("plain", 1)
		pending.Exists = false
		pending.PendingMap = []rgwcls.PendingEntry{{Tag: "t", Info: rgwcls.PendingInfo{State: rgwcls.PendingModify}}}
		c.Object(indexPool, "", shard).Omap["plain"] = encode(pending)
		c.ResetCounters()
		_, err = s.ListObjects(ctx, rec, op.ListObjectsParams{MaxKeys: 10})
		Expect(err).NotTo(HaveOccurred())
		Consistently(func() int { return len(c.WritesTo(indexPool, "", shard)) }).Should(BeZero(), "only dir_suggest_changes follows, and that is a suggestion, not a complete_op")
	})
})
```

If the fake records the suggestion as a write, assert instead that no `bucket_complete_op` is among the shard's writes.

Run: `go test -tags=ceph_preview ./internal/driver/ -ginkgo.focus="multipart-part sweep"`. Expected: the first two fail (no DEL is sent; the entry stays in the second), the third passes.

- [ ] **Step 6: Implement `deleteObjIndex` and the sweep**

`internal/driver/delete.go`:

```go
// deleteObjIndex is RGWRados::delete_obj_index (rgw_rados.cc:6033-6050 at
// v19.2.6, :6787-6804 at v20.2.4): a complete_del with no prepare, an empty
// tag, pool -1 and epoch 0, hashed on the key's own name. The op is built by
// hand because newIndexOp substitutes a random tag for "", and a tag the
// entry's pending map does not hold is refused with EINVAL (cls_rgw.cc:1066-1072).
func (s *Store) deleteObjIndex(rec *op.BucketRecord, key meta.ObjKey, mtime time.Time) {
	x := &indexOp{
		s:     s,
		rec:   rec,
		obj:   meta.Obj{Bucket: rec.Info.Bucket, Key: key},
		blind: rec.Info.Layout.Current.Layout.Type == meta.IndexIndexless,
	}
	x.completeDel(rgw.EntryVer{Pool: -1, Epoch: 0}, mtime)
}
```

`internal/driver/list.go` (M Task 8), at the comment in `checkDiskState`, after the `AppendableValue` line:

```go
	if st.Manifest != nil { // rgw_rados.cc:10413-10429 (v20.2.4 :11337-11353): retire the part entries a multipart manifest names
		it, err := st.Manifest.Seek(0)
		for ; err == nil && !it.Done(); err = it.Next() {
			if loc, _, _ := it.Location(); loc.Key.NS == meta.NSMultipart {
				s.deleteObjIndex(rec, loc.Key, st.Mtime)
			}
		}
		if err != nil {
			slog.WarnContext(ctx, "could not walk the manifest for the multipart-part sweep", slog.String("key", key.Name), slog.Any("error", err))
		}
	}
```

`Location()` returns the stripe's `meta.Obj` with the namespace the phase-0 iterator assigns exactly as `get_implicit_location` does ([`internal/meta/manifest_iter.go:387-393`](https://github.com/jhoblitt/rgw-go/blob/50fc45c4d59953661d5c57c27ca7016bc55edeeb/internal/meta/manifest_iter.go#L387-L393); rgw_obj_manifest.cc:223-245 at v19.2.6), and an explicit manifest's pieces carry their own `Loc`, so no raw-oid round trip is needed; the head (part 0's in-head stripe) has an empty namespace and is skipped, as `raw_obj_to_obj` leaves it. The `completeDel` goes through Task 4's completion manager, not awaited, as `cls_obj_complete_op`'s AIO is not.

- [ ] **Step 7: Run, lint, commit**

Run: `go test -tags=ceph_preview -race ./internal/driver/`. Expected: PASS, M Task 8's listing specs included.

```bash
make check
git add internal/driver
git commit -m "feat(driver): retire multipart part entries when a listing reconciles their head

check_disk_state walks an existing head's manifest and sends
delete_obj_index for every stripe in the multipart namespace: a
complete_del with an empty tag and version -1:0, hashed on the part's
own name as radosgw hashes it. The listing now does the same when it
reconciles a pending entry whose completion never landed.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 7: `driver`: `SetObjectAttrs`

**Files:**
- Create: `internal/driver/attrs.go`, `internal/driver/attrs_test.go`
- Modify: `internal/driver/store.go` (the `SetObjectAttrs` stub goes)

**Interfaces:**
- Consumes: Task 4's `indexOp`, Task 5's `entryOwner`, `rgwBlStr`, `rawTag`; R's `headRef`; G's `op.ObjectState` (`Bucket`, `Key`, `Attrs`, `WriteTag`, `Size`, `Compression`, `Mtime`, `Epoch`, `Exists`).
- Produces:

```go
package driver

// SetObjectAttrs is RGWRados::set_attrs (rgw_rados.cc:6593-6760; v20.2.4
// :7393-7570) as RadosObject::set_obj_attrs calls it for PutObjectAcl,
// PutObjectTagging and DeleteObjectTagging (rgw_sal_rados.cc:2378-2391): the
// head op carries the idtag guard, the removals, the additions and a fresh
// idtag under a fresh "_" tag, at the object's mtime plus one nanosecond; the
// index entry is rebuilt from the new and existing attrs. st is the state the
// op's Init read; a lost race is ErrConcurrentModification.
func (s *Store) SetObjectAttrs(ctx context.Context, st *op.ObjectState, set map[string][]byte, rm []string) error
```

The RADOS sequence, after the op's own stat: prepare ADD (1), the head op (2) `[CmpXattr(idtag) when the state has a real tag] RmXattr… SetXattr… (name order, empty skipped) SetXattr(idtag, "_"+31+NUL)` with `SetMtime(st.Mtime + 1ns)`, then complete ADD off the request path with `{Size: st.Size, AccountedSize, Mtime: the nudged mtime, ETag, ContentType, StorageClass, Owner}` from the new attrs where set and the existing ones otherwise ([`:6697-6725`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6697-L6725)). The nanosecond is `RadosObject::set_obj_attrs`' nudge for multisite's `fetch_remote_obj`, applied whenever the SAL flag `FLAG_LOG_OP` is passed, which `modify_obj_attrs` and `delete_obj_attrs` always pass; it is recorded as a quirk in Task 12.

- [ ] **Step 1: Write the failing specs**

`internal/driver/attrs_test.go`, on the Task 5 fixture with `k` written under tag `tx-put` and `st, _ := s.StatObject(ctx, rec, key)`:

```go
var _ = Describe("SetObjectAttrs", func() {
	It("replaces the ACL behind the tag guard, retags the head, and rebuilds the index entry", func(ctx SpecContext) {
		newACL := encode(acl.DefaultPolicy(alice, "Alice Renamed"))
		headOID := rec.Info.Bucket.Marker + "_k"
		c.ResetCounters()
		Expect(s.SetObjectAttrs(ctx, st, map[string][]byte{meta.AttrACL: newACL}, nil)).To(Succeed())
		Expect(c.Reads(dataPool, "", headOID)).To(BeZero(), "the op's Init did the stat")
		w := c.LastWrite(dataPool, "", headOID)
		steps := w.Steps()
		Expect(steps[0]).To(Equal(&radosclient.CmpXattrStep{Name: "user.rgw.idtag", Op: radosclient.CmpEQ, Value: []byte("tx-put\x00")}))
		Expect(steps[1]).To(Equal(&radosclient.SetXattrStep{Name: "user.rgw.acl", Value: newACL}))
		tag := steps[2].(*radosclient.SetXattrStep)
		Expect(tag.Name).To(Equal("user.rgw.idtag"))
		Expect(string(tag.Value)).To(MatchRegexp(`^_[A-Za-z0-9_-]{31}\x00$`), "append_rand_alpha, NUL-terminated")
		Expect(steps).To(HaveLen(3))
		mt, ok := w.Mtime()
		Expect(ok).To(BeTrue())
		Expect(mt).To(Equal(st.Mtime.Add(time.Nanosecond)), "rgw_sal_rados.cc:2384-2385")
		shard := shardOf(rec, "k")
		prep := rgwcls.DecodePrepareOp(denc.NewDecoder(c.LastWrite(indexPool, "", shard).Steps()[2].(*radosclient.ExecStep).In))
		Expect(prep.Op).To(Equal(rgwcls.OpAdd))
		Expect(prep.Tag + "\x00").To(Equal(string(tag.Value)), "one tag for the prepare and the head")
		Eventually(func() int { return c.Writes(indexPool, "", shard) }).Should(Equal(4))
		en, _ := c.Entry(indexPool, "", shard, "k")
		Expect(en.Meta.OwnerDisplayName).To(Equal("Alice Renamed"), "owner from the NEW acl, :6700-6703")
		Expect(en.Meta.Size).To(BeEquivalentTo(6 << 20))
		Expect(en.Meta.ETag).To(Equal(md5hex6MiB), "etag from the existing attrs, :6708-6714")
		Expect(en.Meta.Mtime).To(Equal(st.Mtime.Add(time.Nanosecond)))
		Expect(en.Tag).To(Equal(prep.Tag))
	})
	It("removes the tagging attr and skips nothing else", func(ctx SpecContext) {
		Expect(s.SetObjectAttrs(ctx, st, nil, []string{meta.AttrTags})).To(Succeed())
		steps := c.LastWrite(dataPool, "", rec.Info.Bucket.Marker+"_k").Steps()
		Expect(steps[1]).To(Equal(&radosclient.RmXattrStep{Name: "user.rgw.x-amz-tagging"}))
		Expect(steps[2].(*radosclient.SetXattrStep).Name).To(Equal("user.rgw.idtag"))
	})
	It("does nothing for an empty change", func(ctx SpecContext) {
		c.ResetCounters()
		Expect(s.SetObjectAttrs(ctx, st, map[string][]byte{"user.rgw.empty": {}}, nil)).To(Succeed())
		Expect(c.Writes(dataPool, "", rec.Info.Bucket.Marker+"_k")).To(BeZero(), ":6652-6654: !op.size()")
		Expect(c.Writes(indexPool, "", shardOf(rec, "k"))).To(Equal(2), "only the put's")
	})
	It("sends no guard for a head without a real tag", func(ctx SpecContext) {
		// a seeded head with a manifest and no idtag (a fake tag): the head op has no CmpXattr
	})
	It("cancels with radosgw's -1:0 and reports the race when the tag moved", func(ctx SpecContext) {
		headOID := rec.Info.Bucket.Marker + "_k"
		c.BeforeWrite(dataPool, "", headOID, func(o *fakerados.Object) { o.Xattrs["user.rgw.idtag"] = []byte("tx-racer\x00") })
		err := s.SetObjectAttrs(ctx, st, map[string][]byte{meta.AttrACL: encode(acl.DefaultPolicy(alice, "x"))}, nil)
		Expect(err).To(MatchError(op.ErrConcurrentModification))
		shard := shardOf(rec, "k")
		Eventually(func() int { return c.Writes(indexPool, "", shard) }).Should(Equal(4))
		cancel := rgwcls.DecodeCompleteOp(denc.NewDecoder(c.LastWrite(indexPool, "", shard).Steps()[2].(*radosclient.ExecStep).In))
		Expect(cancel.Op).To(Equal(rgwcls.OpCancel))
		Expect(cancel.Ver).To(Equal(rgwcls.EntryVer{Pool: -1, Epoch: 0}), "index_op.cancel, rgw_rados.cc:6726-6731")
	})
})
```

Write the elided body in full.

- [ ] **Step 2: Run to fail, write `attrs.go`**

```go
package driver

func (s *Store) SetObjectAttrs(ctx context.Context, st *op.ObjectState, set map[string][]byte, rm []string) error {
	rec := st.Bucket
	ref, err := s.headRef(ctx, rec, st.Key)
	if err != nil {
		return err
	}
	w := radosclient.NewWriteOp()
	if st.WriteTag != "" { // append_atomic_test: a real tag guards the write (:6612, :6457-6472)
		w.CmpXattr(meta.AttrIDTag, radosclient.CmpEQ, st.Attrs[meta.AttrIDTag])
	}
	for _, name := range rm {
		w.RmXattr(name)
	}
	n := 0
	for _, name := range slices.Sorted(maps.Keys(set)) {
		if len(set[name]) == 0 {
			continue
		}
		w.SetXattr(name, set[name])
		n++
	}
	if n == 0 && len(rm) == 0 {
		return nil // :6652-6654
	}
	x := s.newIndexOp(rec, st.Key, "")
	if err := x.prepare(ctx, rgw.OpAdd); err != nil {
		return op.FromRADOS(err, op.ScopeObject)
	}
	w.SetXattr(meta.AttrIDTag, []byte(x.tag+"\x00")) // :6664-6676
	mtime := st.Mtime.Add(time.Nanosecond)             // rgw_sal_rados.cc:2384-2385
	w.SetMtime(mtime)
	epoch, err := ref.pool.Write(ctx, ref.oid, w, 0)
	if err != nil {
		x.cancel() // index_op.cancel (:6726-6731)
		return op.FromRADOS(err, op.ScopeObject)
	}
	pick := func(name string) []byte {
		if v, ok := set[name]; ok && len(v) > 0 {
			return v
		}
		return st.Attrs[name]
	}
	owner, display := entryOwner(map[string][]byte{meta.AttrACL: pick(meta.AttrACL)})
	accounted := st.Size
	if st.Compression != nil {
		accounted = st.Compression.OrigSize
	}
	x.complete(rgw.EntryVer{Pool: ref.pool.ID(), Epoch: epoch}, rgw.DirEntryMeta{
		Size: st.Size, AccountedSize: accounted, Mtime: mtime,
		ETag: rgwBlStr(pick(meta.AttrETag)), ContentType: rgwBlStr(pick(meta.AttrContentType)), StorageClass: rgwBlStr(pick(meta.AttrStorageClass)),
		Owner: owner, OwnerDisplayName: display,
	}) // :6697-6725
	return nil
}
```

A removed attr (`rm`) that `pick` would otherwise read back is handled as radosgw does: `set_attrs` reads `state->attrset` unchanged, so a DeleteObjectTagging still rebuilds the entry from the existing etag and content type, and the tagging attr plays no part in the entry.

- [ ] **Step 3: Run, lint, commit**

Run: `go test -tags=ceph_preview -race ./internal/driver/`; expected PASS. `make check`, then `feat(driver): SetObjectAttrs with radosgw's retag and index rebuild`. Draft PR `feat(driver): SetObjectAttrs (unit W task 7)`.

---
### Task 8: `driver`: `CopyObject`

**Files:**
- Create: `internal/driver/copy.go`, `internal/driver/copy_test.go`
- Modify: `internal/op/object.go` (`CopyParams.Tag`, W-D6), `internal/op/opfakes/` (regenerate), `internal/memstore/object.go` (`Tag` stored), `internal/driver/put.go` (`PutObject`'s body becomes `putStream`, shared with `copyData`), `internal/driver/store.go` (the `CopyObject` stub goes), `internal/meta/manifest_write.go` (`Manifest.SetHead` unless R added it), `internal/meta/manifest_write_test.go`, `docs/exclusions.md` (W-D13's two Tentacle differences, Step 4)

**Interfaces:**
- Consumes: Task 5's `planPut`, `layout`, `tailWriter`, `headWrite`, `writeMeta`, `enqueueGC`, `rgwBlStr`; Task 4's `indexOp`; R's `readStored` (Task 4's raw stored-byte read), `ReadObject` (a copy's read-back in the specs), `PrefetchObject` (through `ObjectState.Head`), `dataPool`, `rawRef`, `poolAlignment`; `refcount.Get`, `refcount.Put`; G's `op.CopyParams`, `PutResult`, `ObjectState`; Task 3's attr names (`AttrDeleteAt`, `AttrObjectRetention`, `AttrObjectLegalHold`, `AttrOLHIDTag`, `AttrOLHInfo`, `AttrOLHVer`, `AttrReplicationTrace`, `AttrReplicatedAt`, `AttrReplicationStatus`, `AttrCryptPrefix`, `AttrCryptMode`), `meta.AttrCompression`, `meta.AttrStorageClass`, `meta.AttrTailTag`.
- Produces:

```go
package op

// CopyParams gains:
//	// Tag is the write tag; radosgw passes the request id (rgw_op.cc:5673).
//	// Empty selects "_" plus 31 random characters.
//	Tag string
```

```go
package meta

// SetHead is RGWObjManifest::set_head (rgw_obj_manifest.h:406-415): the head
// placement rule, head object and head size; an explicit manifest with data
// in its head also moves piece 0 there.
func (m *Manifest) SetHead(rule PlacementRule, head Obj, size uint64)
```

```go
package driver

// putStream is PutObject without its argument handling: the tails streamed
// from body, the head written through writeMeta. etag "" means the streamed
// MD5; contentMD5 nil skips the digest check. The accounted size is the
// orig_size of attrs' user.rgw.compression when it is present, as
// copy_obj_data's is (rgw_rados.cc:5098-5108), and the streamed size
// otherwise.
func (s *Store) putStream(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, body io.Reader, l layout, tag, etag string, contentMD5 []byte, attrs map[string][]byte, size int64, mtime time.Time, ifMatch, ifNoneMatch string) (*op.PutResult, error)

// copyAttrs is copy_obj's attribute rewrite (rgw_rados.cc:4759-4794 with
// set_copy_attrs :3673-3700; v20.2.4 :5099-5140): the source's attrs with the
// request's ACL, the object-lock and replication attrs dropped, the OLH attrs
// dropped in an unversioned bucket, then COPY or REPLACE, then idtag, pg_ver
// and source_zone dropped and the compression info kept. An encrypted source
// is ErrNotImplemented on both releases: a Squid radosgw refuses it
// (:4755-4763), a Tentacle one decrypts it (v20.2.4 rgw_op.cc:5793-5805),
// which is not implemented. A Tentacle gateway also drops the source's
// storage class (v20.2.4 :5028).
func (s *Store) copyAttrs(src *op.ObjectState, req map[string][]byte, replace bool) (map[string][]byte, error)

// CopyObject is RGWRados::copy_obj's local branch (rgw_rados.cc:4667-5031;
// v20.2.4 :4916-5292): a tailed source in the destination's pool and
// placement shares its tails through refcount get under the new tag, copies
// the head data and writes a new head; a copy onto itself rewrites the head
// alone and keeps its tails; anything else streams the data (copy_obj_data,
// :5034-5108). src is the state the op read with PrefetchObject, so its Head
// serves copy_first without a second read.
func (s *Store) CopyObject(ctx context.Context, src *op.ObjectState, dst *op.BucketRecord, dstKey meta.ObjKey, p op.CopyParams) (*op.PutResult, error)
```

- [ ] **Step 1: The contract field, memstore, `SetHead`**

`internal/op/object.go`: `Tag string` on `CopyParams` with the comment above; `make generate`; `memstore.CopyObject` stores `WriteTag = p.Tag`. `internal/meta/manifest_write.go` (unless `Manifest.SetHead` already exists from R):

```go
func (m *Manifest) SetHead(rule PlacementRule, head Obj, size uint64) {
	m.HeadPlacementRule = rule
	m.Obj = head
	m.HeadSize = size
	if m.ExplicitObjs && size > 0 {
		p := m.Objs[0]
		p.Loc, p.Size = head, size
		m.Objs[0] = p
	}
}
```

with a spec that a trivial manifest's `Obj`, `HeadPlacementRule` and `HeadSize` move and its `Rules` do not, and that an explicit manifest's piece 0 follows.

- [ ] **Step 2: Write the failing specs**

`internal/driver/copy_test.go`, on the Task 5 fixture (`RefcountClass` registered) with a 10 MiB source `src` written under tag `tx-src` with `x-amz-meta-a: 1` and `Content-Type: text/plain`, and `srcSt, _ := s.PrefetchObject(ctx, rec, srcKey)`:

```go
var _ = Describe("CopyObject", func() {
	dstKey := meta.ObjKey{Name: "dst"}
	reqAttrs := func(extra ...string) map[string][]byte { /* the dest ACL for alice plus pairs of name, value (NUL added) */ }

	It("shares the tails through refcount under the new tag and copies only the head", func(ctx SpecContext) {
		c.ResetCounters()
		res, err := s.CopyObject(ctx, srcSt, rec, dstKey, op.CopyParams{Attrs: reqAttrs(), Tag: "tx-copy"})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.ETag).To(Equal(srcSt.ETag))
		Expect(res.Size).To(BeEquivalentTo(10 << 20))
		tail1 := rec.Info.Bucket.Marker + "__shadow_.AkTbQPoXT3s8QcthZt-DpS5NPRLqS5u_1"
		tail2 := rec.Info.Bucket.Marker + "__shadow_.AkTbQPoXT3s8QcthZt-DpS5NPRLqS5u_2"
		for _, t := range []string{tail1, tail2} {
			w := c.LastWrite(dataPool, "", t)
			get := w.Steps()[0].(*radosclient.ExecStep)
			Expect(get.Method).To(Equal("get"))
			Expect(refcount.DecodeGetOp(denc.NewDecoder(get.In))).To(Equal(refcount.GetOp{Tag: "tx-copy\x00", ImplicitRef: true}), "rgw_rados.cc:4919-4921: the tag NUL-terminated")
			rc := refcount.DecodeRefcount(denc.NewDecoder(c.Object(dataPool, "", t).Xattrs["refcount"]))
			Expect(rc.Refs).To(HaveKey(""), "the wildcard for the original writer")
			Expect(rc.Refs).To(HaveKey("tx-copy\x00"))
		}
		Expect(c.Reads(dataPool, "", rec.Info.Bucket.Marker+"_src")).To(BeZero(), "copy_first served from PrefetchObject's Head")
		dstOID := rec.Info.Bucket.Marker + "_dst"
		head := c.Object(dataPool, "", dstOID)
		Expect(head.Data).To(HaveLen(4 << 20))
		Expect(head.Xattrs).To(HaveKeyWithValue("user.rgw.idtag", []byte("tx-copy\x00")))
		Expect(head.Xattrs).To(HaveKeyWithValue("user.rgw.tail_tag", []byte("tx-copy\x00")), "modify_tail on a copy that is not onto itself")
		Expect(head.Xattrs).To(HaveKeyWithValue("user.rgw.etag", []byte(srcSt.ETag)))
		Expect(head.Xattrs).To(HaveKeyWithValue("user.rgw.x-amz-meta-a", []byte("1\x00")), "COPY keeps the source metadata")
		Expect(head.Xattrs).To(HaveKeyWithValue("user.rgw.content_type", []byte("text/plain\x00")))
		m := meta.DecodeManifest(denc.NewDecoder(head.Xattrs["user.rgw.manifest"]))
		Expect(m.Obj.Key).To(Equal(dstKey))
		Expect(m.HeadSize).To(BeEquivalentTo(4 << 20))
		Expect(m.Prefix).To(Equal(srcSt.Manifest.Prefix), "the tails are the source's")
		Expect(m.TailPlacement.Bucket).To(Equal(rec.Info.Bucket))
		Expect(c.Writes(dataPool, "", dstOID)).To(Equal(1))
		Expect(c.Writes(indexPool, "", shardOf(rec, "dst"))).To(Equal(1), "prepare; the complete is not awaited")
		Eventually(func() int { return c.Writes(indexPool, "", shardOf(rec, "dst")) }).Should(Equal(2))
	})
	It("replaces the metadata when asked and keeps the etag and tail tag", func(ctx SpecContext) {
		_, err := s.CopyObject(ctx, srcSt, rec, dstKey, op.CopyParams{Attrs: reqAttrs("user.rgw.x-amz-meta-b", "2", "user.rgw.content_type", "image/png"), ReplaceAttrs: true, Tag: "tx-copy"})
		Expect(err).NotTo(HaveOccurred())
		head := c.Object(dataPool, "", rec.Info.Bucket.Marker+"_dst")
		Expect(head.Xattrs).NotTo(HaveKey("user.rgw.x-amz-meta-a"))
		Expect(head.Xattrs).To(HaveKeyWithValue("user.rgw.x-amz-meta-b", []byte("2\x00")))
		Expect(head.Xattrs).To(HaveKeyWithValue("user.rgw.content_type", []byte("image/png\x00")))
		Expect(head.Xattrs).To(HaveKeyWithValue("user.rgw.etag", []byte(srcSt.ETag)), "set_copy_attrs REPLACE: the source's etag when the request has none")
	})
	It("rewrites only the head when copying onto itself with REPLACE", func(ctx SpecContext) {
		c.ResetCounters()
		_, err := s.CopyObject(ctx, srcSt, rec, srcKey, op.CopyParams{Attrs: reqAttrs("user.rgw.x-amz-meta-c", "3"), ReplaceAttrs: true, Tag: "tx-self"})
		Expect(err).NotTo(HaveOccurred())
		tail1 := rec.Info.Bucket.Marker + "__shadow_.AkTbQPoXT3s8QcthZt-DpS5NPRLqS5u_1"
		Expect(c.Writes(dataPool, "", tail1)).To(BeZero(), "no refcount change, rgw_rados.cc:4952-4957")
		head := c.Object(dataPool, "", rec.Info.Bucket.Marker+"_src")
		Expect(head.Xattrs).To(HaveKeyWithValue("user.rgw.idtag", []byte("tx-self\x00")))
		Expect(head.Xattrs).To(HaveKeyWithValue("user.rgw.tail_tag", []byte("tx-src\x00")), "keep_tail: the tails stay under the old tag, :4983")
		Expect(head.Xattrs).To(HaveKeyWithValue("user.rgw.x-amz-meta-c", []byte("3\x00")))
		Expect(c.Object(dataPool, "", tail1)).NotTo(BeNil(), "keep_tail: nothing was queued for GC")
		Expect(c.Writes(dataPool, "", rec.Info.Bucket.Marker+"_src")).To(Equal(2), "the exclusive create fails, then the guarded write")
	})
	It("streams the data when the destination lives in another pool", func(ctx SpecContext) {
		res, err := s.CopyObject(ctx, srcSt, rec, dstKey, op.CopyParams{Attrs: reqAttrs(), StorageClass: "COLD", Tag: "tx-copy"})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.ETag).To(Equal(srcSt.ETag), "copy_obj_data keeps the source etag")
		Expect(c.Object("cold.data", "", rec.Info.Bucket.Marker+"__shadow_.SECONDSECONDSECONDSECONDSECOND1_0")).NotTo(BeNil(), "no head data: stripes from 0")
		Expect(c.Object("cold.data", "", rec.Info.Bucket.Marker+"__shadow_.SECONDSECONDSECONDSECONDSECOND1_2")).NotTo(BeNil())
		head := c.Object(dataPool, "", rec.Info.Bucket.Marker+"_dst")
		Expect(head.Xattrs["user.rgw.idtag"]).To(HavePrefix("_"), "copy_obj_data uses a random tag, :5051")
		Expect(head.Xattrs).To(HaveKeyWithValue("user.rgw.storage_class", []byte("COLD")))
		tail1 := rec.Info.Bucket.Marker + "__shadow_.AkTbQPoXT3s8QcthZt-DpS5NPRLqS5u_1"
		Expect(c.Writes(dataPool, "", tail1)).To(BeZero(), "no refcount get across pools")
	})
	It("streams the data for a source held within its head", func(ctx SpecContext) {
		// a 1 KiB source: !has_tail → copy_data; the dest head carries the bytes, no refcount ops anywhere
	})
	It("copies a compressed source's stored bytes and compression info across pools, as copy_obj_data does", func(ctx SpecContext) {
		plain := bytes.Repeat([]byte("compressible "), 400000) // 5.2 MB of plaintext
		comp := snappy.Encode(nil, plain)
		ci := meta.NewCompressionInfo()
		ci.Type, ci.OrigSize = "snappy", uint64(len(plain))
		ci.Blocks = []meta.CompressionBlock{{OldOfs: 0, NewOfs: 0, Len: uint64(len(comp))}}
		csrc := seedCompressedSource(ctx, "csrc", comp, ci)
		res, err := s.CopyObject(ctx, csrc, rec, dstKey, op.CopyParams{Attrs: reqAttrs(), StorageClass: "COLD", Tag: "tx-copy"})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.ETag).To(Equal("e-csrc"), "copy_obj_data keeps the source etag")
		head := c.Object(dataPool, "", rec.Info.Bucket.Marker+"_dst")
		Expect(head.Xattrs).To(HaveKeyWithValue("user.rgw.compression", encode(ci)), "the source's compression info, rgw_rados.cc:4791-4793")
		m := meta.DecodeManifest(denc.NewDecoder(head.Xattrs["user.rgw.manifest"]))
		Expect(m.ObjSize).To(BeEquivalentTo(len(comp)), "the stored size: the bytes were copied undecoded")
		dst, err := s.StatObject(ctx, rec, dstKey)
		Expect(err).NotTo(HaveOccurred())
		var stored bytes.Buffer
		Expect(s.ReadStoredForTest(ctx, dst, 0, dst.Size, &stored)).To(Succeed())
		Expect(stored.Bytes()).To(Equal(comp), "Read::read's bytes, rgw_rados.cc:5067")
		var back bytes.Buffer
		Expect(s.ReadObject(ctx, dst, op.ByteRange{Offset: 0, Length: ci.OrigSize}, &back)).To(Succeed())
		Expect(back.Bytes()).To(Equal(plain))
		Eventually(func() rgwcls.DirEntryMeta {
			en, _ := c.Entry(indexPool, "", shardOf(rec, "dst"), "dst")
			return en.Meta
		}).Should(And(
			HaveField("Size", BeEquivalentTo(len(comp))),
			HaveField("AccountedSize", BeEquivalentTo(len(plain))), // accounted_size = orig_size, rgw_rados.cc:5098-5108
		))
		Expect(c.Writes("cold.data", "", rec.Info.Bucket.Marker+"__shadow_.SECONDSECONDSECONDSECONDSECOND1_0")).To(BeNumerically(">=", 1), "the destination class's pool holds the stored bytes")
	})
	It("refuses an encrypted source with NotImplemented on both releases, and drops the storage class on Tentacle", func(ctx SpecContext) {
		// seed user.rgw.crypt.mode on the source head: a Squid and a Tentacle store both answer op.ErrNotImplemented and write nothing (no dest head, no refcount op, no index op); an unencrypted source carrying user.rgw.storage_class COLD copied by a Tentacle store to the default class: the dest has no user.rgw.storage_class from the source (v20.2.4 rgw_rados.cc:5028), a Squid store keeps it
	})
	It("rolls the references back under pool_full_try when a tail refuses", func(ctx SpecContext) {
		tail2 := rec.Info.Bucket.Marker + "__shadow_.AkTbQPoXT3s8QcthZt-DpS5NPRLqS5u_2"
		c.FailNextWrite(dataPool, "", tail2, syscall.EIO)
		_, err := s.CopyObject(ctx, srcSt, rec, dstKey, op.CopyParams{Attrs: reqAttrs(), Tag: "tx-copy"})
		Expect(err).To(HaveOccurred())
		tail1 := rec.Info.Bucket.Marker + "__shadow_.AkTbQPoXT3s8QcthZt-DpS5NPRLqS5u_1"
		w := c.LastWrite(dataPool, "", tail1)
		Expect(w.Steps()[0].(*radosclient.ExecStep).Method).To(Equal("put"), "done_ret: refcount put on what succeeded, :4990-5031")
		Expect(w.Flags()).To(Equal(radosclient.OpFlagFullTry))
		rc := refcount.DecodeRefcount(denc.NewDecoder(c.Object(dataPool, "", tail1).Xattrs["refcount"]))
		Expect(rc.Refs).NotTo(HaveKey("tx-copy\x00"))
		Expect(c.Object(dataPool, "", rec.Info.Bucket.Marker+"_dst")).To(BeNil())
		Expect(c.Writes(indexPool, "", shardOf(rec, "dst"))).To(BeZero(), "the refcounts precede the prepare")
	})
	It("overwrites an existing destination through the EEXIST retry and queues its old tails", func(ctx SpecContext) {
		// a 6 MiB dest under tag tx-old, then the copy: the dest head sees two writes and one stat, the old dest tail lands in a GC shard under "tx-old\x00", the source tails gain the new ref
	})
})
```

Write the three elided bodies in full. `seedCompressedSource(ctx, name, stored, ci)` writes a head `<marker>_<name>` holding `stored` with `user.rgw.etag` `e-<name>`, the alice ACL, a trivial manifest of `len(stored)` bytes (4 MiB head chunk) and `user.rgw.compression` = `encode(ci)`, and returns `s.PrefetchObject(ctx, rec, {Name: name})`; `snappy` is `github.com/klauspost/compress/snappy`. The spec runs after `s.SetRandForTest(fixedRand("SECONDSECONDSECONDSECONDSECOND1"))`, as the other streaming spec does.

- [ ] **Step 3: Run to fail, write `copy.go` and refactor `put.go`**

`put.go`: `PutObject` becomes argument handling plus `putStream(ctx, rec, key, body, l, tag, p.ETag, p.ContentMD5, p.Attrs, p.Size, p.Mtime, p.IfMatch, p.IfNoneMatch)`; `putStream` holds the body of Task 5's `PutObject` from `NewTrivialManifest` on, hashing only when `etag == ""`, and builds the `headWrite` with `size` = the bytes streamed and `accountedSize` = the `OrigSize` of `meta.DecodeCompressionInfo(attrs[meta.AttrCompression])` when that attr is present (a decode failure is `op.ErrInternalError`, radosgw's -EIO from `rgw_compression_info_from_attrset`), the bytes streamed otherwise; a PUT's attrs never carry the attr, so `PutObject` is unchanged.

`copy.go`:

```go
package driver

func (s *Store) copyAttrs(src *op.ObjectState, req map[string][]byte, replace bool) (map[string][]byte, error) {
	srcAttrs := maps.Clone(src.Attrs)
	if srcAttrs == nil {
		srcAttrs = map[string][]byte{}
	}
	if _, ok := srcAttrs[meta.AttrCryptMode]; ok {
		// A Squid radosgw refuses an encrypted source (:4755-4763). A Tentacle one
		// decrypts it through the copy's data processor and re-encrypts as asked
		// (v20.2.4 rgw_op.cc:5793-5805, :5846-5866), which needs decryption that
		// is not implemented; both releases answer as Squid does, so no copy
		// carries ciphertext under a head without its crypt attrs.
		return nil, op.ErrNotImplemented
	}
	srcAttrs[meta.AttrACL] = req[meta.AttrACL]
	for _, n := range []string{meta.AttrDeleteAt, meta.AttrObjectRetention, meta.AttrObjectLegalHold, meta.AttrOLHIDTag, meta.AttrOLHInfo, meta.AttrOLHVer,
		meta.AttrReplicationTrace, meta.AttrReplicatedAt, meta.AttrReplicationStatus} {
		delete(srcAttrs, n) // OLH attrs: dest_bucket_info.versioning_enabled() is false while versioning is not implemented
	}
	for _, n := range []string{meta.AttrObjectRetention, meta.AttrObjectLegalHold} {
		if v, ok := req[n]; ok {
			srcAttrs[n] = v
		}
	}
	if s.release != denc.Squid {
		delete(srcAttrs, meta.AttrStorageClass) // v20.2.4 :5028
	}
	var attrs map[string][]byte
	if replace { // set_copy_attrs ATTRSMOD_REPLACE
		attrs = maps.Clone(req)
		if len(attrs[meta.AttrETag]) == 0 {
			attrs[meta.AttrETag] = srcAttrs[meta.AttrETag]
		}
		if len(attrs[meta.AttrTailTag]) == 0 {
			if tt, ok := srcAttrs[meta.AttrTailTag]; ok {
				attrs[meta.AttrTailTag] = tt
			}
		}
	} else { // ATTRSMOD_NONE
		attrs = srcAttrs
	}
	delete(attrs, meta.AttrIDTag)
	delete(attrs, meta.AttrPGVer)
	delete(attrs, meta.AttrSourceZone)
	if cmp, ok := srcAttrs[meta.AttrCompression]; ok {
		attrs[meta.AttrCompression] = cmp
	}
	return attrs, nil
}

func (s *Store) CopyObject(ctx context.Context, src *op.ObjectState, dst *op.BucketRecord, dstKey meta.ObjKey, p op.CopyParams) (*op.PutResult, error) {
	if !src.Exists {
		return nil, op.ErrNoSuchKey
	}
	attrs, err := s.copyAttrs(src, p.Attrs, p.ReplaceAttrs)
	if err != nil {
		return nil, err
	}
	destRule := meta.PlacementRule{StorageClass: p.StorageClass}.InheritFrom(dst.Info.PlacementRule)
	dstObj := meta.Obj{Bucket: dst.Info.Bucket, Key: dstKey}
	l, err := s.planPut(ctx, dst, dstKey, p.StorageClass)
	if err != nil {
		return nil, err
	}
	// :4809-4849: the copy_data decision
	srcRule := src.Bucket.Info.PlacementRule
	if src.Manifest != nil && src.Manifest.TailPlacement.PlacementRule != (meta.PlacementRule{}) {
		srcRule = src.Manifest.TailPlacement.PlacementRule
	}
	srcPool, ok := s.dataPool(srcRule, src.Bucket.Info.Bucket)
	if !ok {
		return nil, fmt.Errorf("%w: no data pool for the source of %s", op.ErrInternalError, src.Key.Name)
	}
	dstPool, ok := s.dataPool(destRule, dst.Info.Bucket)
	if !ok {
		return nil, fmt.Errorf("%w: no data pool for %s", op.ErrInternalError, dstKey.Name)
	}
	accounted := src.Size
	if src.Compression != nil {
		accounted = src.Compression.OrigSize
	}
	copyData := src.Manifest == nil || srcRule != destRule || srcPool != dstPool
	copyFirst := false
	if src.Manifest != nil {
		switch hs := src.Manifest.HeadSize; {
		case !src.Manifest.HasTail():
			copyData = true
		case hs > 0 && hs > l.chunk:
			copyData = true
		case hs > 0:
			copyFirst = true
		}
	}
	if copyData {
		delete(attrs, meta.AttrTailTag)
		return s.copyData(ctx, src, dst, dstKey, l, attrs, p.Mtime)
	}
	tag := p.Tag
	if tag == "" {
		tag = s.w.randTag()
	}
	copyItself := src.Bucket.Info.Bucket.ID == dst.Info.Bucket.ID && src.Key == dstKey
	var m *meta.Manifest
	var referenced []meta.Stripe
	if copyItself {
		m = src.Manifest // :4977-4979, keep_tail
	} else {
		mc := cloneManifest(*src.Manifest)
		if mc.TailPlacement.Bucket.Name == "" {
			mc.TailPlacement.Bucket = src.Bucket.Info.Bucket // :4913-4917
		}
		m = &mc
		delete(attrs, meta.AttrTailTag)
		stripes, err := src.Manifest.Stripes()
		if err != nil {
			return nil, err
		}
		referenced, err = s.refTails(ctx, stripes, tag)
		if err != nil {
			s.unrefTails(ctx, referenced, tag)
			return nil, err
		}
	}
	var data []byte
	if copyFirst {
		data, err = s.headData(ctx, src, l.chunk) // src.Head when PrefetchObject filled it, else ReadObject(0, HeadSize)
		if err != nil {
			s.unrefTails(ctx, referenced, tag)
			return nil, err
		}
		m.SetHead(dst.Info.PlacementRule, dstObj, uint64(len(data))) // :4959-4965
	} else {
		data = []byte{}
		m.SetHead(dst.Info.PlacementRule, dstObj, 0)
	}
	hw := &headWrite{rec: dst, key: dstKey, tag: tag, data: data, manifest: m, attrs: attrs, mtime: p.Mtime, create: true, modifyTail: !copyItself, keepTail: copyItself, size: src.Size, accountedSize: accounted}
	x := s.newIndexOp(dst, dstKey, tag)
	res, err := s.writeMeta(ctx, hw, x) // :4970-4985, no preconditions on the destination
	if err != nil {
		s.unrefTails(ctx, referenced, tag)
		return nil, err
	}
	if res.canceled && !copyItself {
		s.unrefTails(ctx, referenced, tag)
	}
	return &op.PutResult{ETag: rgwBlStr(attrs[meta.AttrETag]), Size: src.Size, Mtime: res.mtime, Epoch: res.epoch}, nil
}

// refTails is copy_obj's refcount loop (:4906-4945): get(tag + NUL, implicit)
// on every tail stripe, rgw_max_copy_obj_concurrent_io at a time; it returns
// the stripes whose get succeeded, for a rollback, and the first error.
func (s *Store) refTails(ctx context.Context, stripes []meta.Stripe, tag string) ([]meta.Stripe, error)

// unrefTails is done_ret's rollback (:4990-5031): put(tag + NUL, implicit)
// under pool_full_try on each stripe, errors logged.
func (s *Store) unrefTails(ctx context.Context, stripes []meta.Stripe, tag string)

// copyData is copy_obj_data (:5034-5114): the source's stored bytes, read
// undecoded as Read::read hands them over (:5067), streamed into putStream
// under a random tag (:5051) with the etag kept and no preconditions. A
// compressed source keeps its user.rgw.compression, block map included, and
// putStream takes the copy's accounted size from it (:5098-5108); the
// destination placement's compression setting is not consulted.
func (s *Store) copyData(ctx context.Context, src *op.ObjectState, dst *op.BucketRecord, dstKey meta.ObjKey, l layout, attrs map[string][]byte, mtime time.Time) (*op.PutResult, error) {
	pr, pw := io.Pipe()
	go func() {
		err := s.readStored(ctx, src, 0, src.Size, pw) // Size is the stored size: the manifest's obj_size
		pw.CloseWithError(err)                         // nil closes cleanly; an error surfaces from consume as the body error
	}()
	return s.putStream(ctx, dst, dstKey, pr, l, s.w.randTag(), rgwBlStr(attrs[meta.AttrETag]), nil, attrs, int64(src.Size), mtime, "", "") //nolint:gosec // sizes fit
}
```

`refTails` and `unrefTails` loop over `stripes` skipping `InHead`, resolve each stripe's pool through `rawRef(ctx, st.Placement, st.Obj)`, and run `refcount.Get(w, tag+"\x00", true, s.release)` / `refcount.Put(w, tag+"\x00", true, s.release)` under an `errgroup.WithContext` bounded by `semaphore.NewWeighted(copyConcurrentIO)`; `unrefTails` passes `radosclient.OpFlagFullTry`. `cloneManifest` copies the `Rules` and `Objs` maps. `headData` returns `src.Head[:min(len(src.Head), int(chunk))]` when `src.Head != nil`, else reads the stored `[0, HeadSize)` through `readStored` into a buffer, since `copy_first` copies the head's stored bytes ([`:4959-4965`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L4959-L4965)) and a compressed source's tails stay compressed. When `readStored` fails inside `copyData`, `pw.CloseWithError` makes `consume` return that error, so the tails already written are discarded by `putStream`.

- [ ] **Step 4: Record W-D13's differences in `docs/exclusions.md`**

At the end of "Coexistence obligations independent of any exclusion", one bullet:

> - **A copy that streams its data copies the stored bytes.** Where a copy cannot share the source's tails (another pool, placement or storage class, or a source held in its head), rgw-go reads the source's stored bytes and writes them unchanged, keeping a compressed source's compression attribute and its accounted size, as a Squid radosgw does. A Tentacle radosgw decodes such a source instead and re-encodes it with the destination placement's compression type, so the two gateways' copies can differ in codec and block layout; each reads back identically through either gateway. rgw-go refuses an encrypted copy source with 501 NotImplemented on both releases, as a Squid radosgw does; a Tentacle radosgw decrypts it and re-encrypts as the request asks, which needs SSE-C decryption, not yet implemented, or a key server, which is excluded.

- [ ] **Step 5: Run, lint, commit**

Run: `make generate-check && go test -tags=ceph_preview -race ./internal/... `; expected PASS. `make check`, then `feat(driver): CopyObject sharing tails through the refcount class`, with `docs/exclusions.md` in the commit. Draft PR `feat(driver): CopyObject (unit W task 8)`; the description states W-D13 and that the `docs/exclusions.md` change must be announced to the rgw-rs session.

---

### Task 9: `driver`: the GC worker

**Files:**
- Create: `internal/driver/gc.go`, `internal/driver/gc_test.go`
- Modify: `internal/driver/store.go` (`Open`: `gcInitialize` then `AddWorker("gc", …)` when `gcThreads`), `internal/testutil/fakerados/cls_gc.go` (`rgw_gc_queue_list_entries`, `rgw_gc_queue_remove_entries`), `internal/testutil/fakerados/cls_rgw.go` (`gc_set_entry`, `gc_list`, `gc_remove` over omap), `internal/testutil/fakerados/pool.go` (exclusive locks: `LockExclusive` fails `EBUSY` for another cookie or client while held, `Unlock` releases; `Cluster.Locks(pool, ns, oid) []radosclient.Locker`)

**Interfaces:**
- Consumes: Task 2's `rgw.GCList`, `GCRemove`; phase 0's `gc.QueueInit`, `QueueList`, `QueueRemoveEntries`, `gc.ShardOID`, `version.Check`, `version.Set`, `version.Read`, `refcount.Put`; Task 4's `writer` (`gcShards`, options); `radosclient.Pool.LockExclusive/Unlock`, `OpFlagFullTry`; `golang.org/x/sync/semaphore`.
- Produces:

```go
package driver

// gcProcess is the shard lock's name, gc_index_lock_name (rgw_gc.cc:29).
const gcProcess = "gc_process"

// gcInitialize is RGWGC::initialize (rgw_gc.cc:31-56): every shard gets
// create(false) + version check(0) + rgw_gc_queue_init(queue size, deferred)
// + version set(1) in one op; an already transitioned shard answers
// ECANCELED, which is ignored, as radosgw ignores every result here.
func (s *Store) gcInitialize(ctx context.Context) error

// gcWorker is RGWGC::GCWorker with RGWGCIOManager: the per-shard pass over
// both shard formats, the bounded refcount puts, the omap-era tag removal
// batches and the queue-era entry removal.
type gcWorker struct { /* s; transitioned []bool; now func() time.Time; sleep func(ctx, d) */ }

func newGCWorker(s *Store) *gcWorker
func (g *gcWorker) run(ctx context.Context) error                                        // GCWorker::entry, :782-809
func (g *gcWorker) process(ctx context.Context, expiredOnly bool) error                  // RGWGC::process(bool), :729-748
func (g *gcWorker) processShard(ctx context.Context, idx int, budget time.Duration, expiredOnly bool, io *gcIO) error // RGWGC::process(int, …), :550-727

// gcIO is RGWGCIOManager: at most rgw_gc_max_concurrent_io refcount puts in
// flight, ENOENT a success, per-tag remaining counts for the omap era, tag
// removals flushed rgw_gc_max_trim_chunk at a time.
type gcIO struct { /* … */ }
```

The per-shard pass ([`:550-727`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_gc.cc#L550-L727)), transcribed: `LockExclusive(oid, "gc_process", "", "", maxTime, 0)` — `EBUSY` → return nil (another gateway holds it), `maxTime <= 0` → `EAGAIN`; pages of 100: not yet transitioned → `GCList(marker, 100, expiredOnly)` plus `version.Read`; version 1 with no entries → `GCList(marker, 1, false)`: none → `transitioned[idx] = true`, marker cleared, and the loop goes on in queue form; some → done; version 0 with ENOENT or no entries → done. Transitioned → `gc.QueueList(marker, 100, expiredOnly)`; none → done. `marker = next`; per entry: past the budget → done; omap era: an empty chain schedules the tag's removal, else its object count is recorded; per chain object: the pool from `meta.ParsePool(obj.Pool)`, `WithLocator(loc)` when set, `refcount.Put(tag, true)` with `OpFlagFullTry`, at most `gcMaxConcurrentIO` in flight, `ENOENT` a success, an omap-era success decrementing the tag's count and scheduling its removal at zero, a queue-era failure ending the shard's pass; queue era after the page: drain the puts, then `gc.QueueRemoveEntries(len(entries))` — a failed put means no removal ([:703-716](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_gc.cc#L703-L716)); repeat while truncated; unlock. `process`: a random start shard, every shard in turn, then `io.drain()` (the omap-era removals flushed) unless stopping. `run`: `process(true)`, then sleep `gcProcessorPeriod` minus the pass's duration (no sleep when the pass took longer), until ctx ends. `gc_operate` is a plain write to the zone's `gc_pool`.

- [ ] **Step 1: fakerados growth**

`cls_gc.go`: `rgw_gc_queue_list_entries` decodes `rgw.GCListOp` (Task 2's; the queue class reuses it) and answers `rgw.GCListRet` with the entries after `Marker` (the marker is the entry's index as a decimal string), at most `Max`, only those with `Time <= now` when `ExpiredOnly`, `Truncated`/`NextMarker` when more remain; `rgw_gc_queue_remove_entries` drops the first `NumEntries` entries. `cls_rgw.go`: `gc_set_entry` stores the entry under omap key `info.Tag` (the class keys by tag with an expiration index; the fake keeps `Time = now + expiration` in the value), `gc_list` lists in key order with the same paging rule, `gc_remove` deletes the named keys. `pool.go`: an exclusive lock held by `(client, cookie)` makes `LockExclusive` from another cookie `EBUSY`; `Unlock` clears; `Cluster.Locks` lists holders; expiry is not simulated. The fake's clock is `Cluster.SetClock(func() time.Time)` (M's `newObject()` uses it), so specs move time.

- [ ] **Step 2: Write the failing specs**

`internal/driver/gc_test.go`, on the Task 5 fixture with `GCQueueClass`, `RefcountClass`, `VersionClass`, `RGWClass` registered, `rgw_gc_max_objs` 4 in `conf`, and `g := s.GCWorkerForTest()` (export_test; `SetNow`/`SetSleep` hooks):

```go
var _ = Describe("gc worker", func() {
	It("initialises every shard with create, version check 0, queue init and version set 1", func(ctx SpecContext) {
		Expect(s.GCInitializeForTest(ctx)).To(Succeed())
		for i := range 4 {
			w := c.LastWrite(gcPoolName, gcNS, fmt.Sprintf("gc.%d", i))
			steps := w.Steps()
			Expect(steps[0]).To(Equal(&radosclient.CreateStep{Exclusive: false}))
			Expect(steps[1].(*radosclient.ExecStep).Method).To(Equal("check_conds"))
			Expect(steps[2].(*radosclient.ExecStep).Method).To(Equal("rgw_gc_queue_init"))
			init := gc.DecodeQueueInitOp(denc.NewDecoder(steps[2].(*radosclient.ExecStep).In))
			Expect(init).To(Equal(gc.QueueInitOp{Size: 131068 << 10, NumDeferredEntries: 50}))
			Expect(steps[3].(*radosclient.ExecStep).Method).To(Equal("set"))
		}
		Expect(s.GCInitializeForTest(ctx)).To(Succeed(), "a second run's ECANCELED is ignored")
	})
	It("frees the tails of an expired queue-era entry and removes the entry", func(ctx SpecContext) {
		// PUT then DELETE a 10 MiB object (tag tx-put): gc.<shard> holds one entry expiring in 7200 s; the tails exist.
		// g.process(ctx, true) now → nothing removed (not expired); c.AdvanceClock(2 h); g.process(ctx, true):
		//   both tails receive [put(tag "tx-put\x00", implicit)] with OpFlagFullTry and are gone (RefcountClass: wildcard dropped → object removed);
		//   the shard receives [rgw_gc_queue_remove_entries 1]; c.GCEntries is empty; the lock was taken as "gc_process" and released.
	})
	It("processes an omap-era shard through gc_list and removes the tag once its objects are gone", func(ctx SpecContext) {
		// seed gc.1 with version 0 and one gc_set_entry (two tails, expired); process: gc_list, two puts, then [gc_remove ["tag"]] flushed by drain; the entry is gone
	})
	It("detects a transitioned shard by version 1 with no omap entries and then lists the queue", func(ctx SpecContext) {
		// gc.2 at version 1, no omap entries, one queue entry: the pass reads gc_list (empty), cls version read, gc_list(1, false) (empty), then rgw_gc_queue_list_entries and processes the entry; a second pass reads only the queue
	})
	It("skips a shard another gateway holds", func(ctx SpecContext) {
		Expect(c.LockExclusiveForTest(gcPoolName, gcNS, "gc.3", "gc_process", "other-cookie")).To(Succeed())
		Expect(g.ProcessShardForTest(ctx, 3, time.Hour, true)).To(Succeed())
		Expect(c.Reads(gcPoolName, gcNS, "gc.3")).To(BeZero(), "EBUSY → nothing listed (rgw_gc.cc:573-578)")
	})
	It("keeps rgw_gc_max_concurrent_io puts in flight and stops at the time budget", func(ctx SpecContext) {
		// 25 tails in one entry with rgw_gc_max_concurrent_io 10: a fakerados write hook counts a peak of 10; with a budget already spent (SetNow returns start + max time) the pass lists one page and removes nothing (:645-648)
	})
	It("does not remove a queue page whose puts failed", func(ctx SpecContext) {
		// FailNextWrite(tail, EIO): the entry stays (:703-716); the next pass retries it
	})
	It("runs on the period and stops with the context", func(ctx SpecContext) {
		// SetSleep records the requested durations: after a pass taking 1 s (SetNow) the sleep is period − 1 s; cancel → run returns nil
	})
})
```

Write the six elided bodies in full.

- [ ] **Step 3: Run to fail, write `gc.go`**

```go
package driver

const gcProcess = "gc_process"

func (s *Store) gcPool(ctx context.Context) (radosclient.Pool, error) { return s.pools.get(ctx, s.ZoneParams().GCPool) }

func (s *Store) gcInitialize(ctx context.Context) error {
	pool, err := s.gcPool(ctx)
	if err != nil {
		return err
	}
	for _, oid := range s.w.gcShards {
		w := radosclient.NewWriteOp()
		w.Create(false)
		version.Check(w, version.ObjVersion{}, version.CondEQ, s.release) // gc_log_init2, rgw_gc_log.cc:11-19
		gc.QueueInit(w, s.w.opts.gcMaxQueueSize, s.w.opts.gcMaxDeferred, s.release)
		version.Set(w, version.ObjVersion{Ver: 1}, s.release)
		if _, err := pool.Write(ctx, oid, w, 0); err != nil && !errors.Is(err, radosclient.ErrCanceled) {
			slog.DebugContext(ctx, "gc shard init", slog.String("shard", oid), slog.Any("error", err)) // radosgw ignores the result
		}
	}
	return nil
}

type gcWorker struct {
	s            *Store
	transitioned []bool
	now          func() time.Time
	sleep        func(ctx context.Context, d time.Duration) error
}

func newGCWorker(s *Store) *gcWorker {
	return &gcWorker{s: s, transitioned: make([]bool, len(s.w.gcShards)), now: time.Now, sleep: sleepCtx}
}

func (g *gcWorker) run(ctx context.Context) error {
	for {
		start := g.now()
		slog.InfoContext(ctx, "garbage collection: start")
		if err := g.process(ctx, true); err != nil && ctx.Err() == nil {
			slog.ErrorContext(ctx, "garbage collection process failed", slog.Any("error", err))
		}
		slog.InfoContext(ctx, "garbage collection: stop")
		if ctx.Err() != nil {
			return nil
		}
		if wait := g.s.w.opts.gcProcessorPeriod - g.now().Sub(start); wait > 0 {
			if err := g.sleep(ctx, wait); err != nil {
				return nil
			}
		}
	}
}

func (g *gcWorker) process(ctx context.Context, expiredOnly bool) error {
	n := len(g.s.w.gcShards)
	start := rand.IntN(n) // ceph::util::generate_random_number(0, max_objs - 1)
	io := newGCIO(g)
	for i := range n {
		if err := g.processShard(ctx, (i+start)%n, g.s.w.opts.gcProcessorMaxTime, expiredOnly, io); err != nil {
			return err
		}
	}
	if ctx.Err() == nil {
		io.drain(ctx)
	}
	return nil
}
```

`processShard` transcribes `:550-727` with the paging and format detection in the interface notes above; `gcIO` holds a `semaphore.Weighted(gcMaxConcurrentIO)`, an `errgroup`-free `sync.WaitGroup` with a first-error slot per page, `tagLeft map[int]map[string]int` and `removeTags map[int][]string` flushed at `gcMaxTrimChunk` through `rgw.GCRemove(w, tags, release)` on the shard, and `drain` (wait the puts, flush every shard's tags, wait again). Each put is `refcount.Put(w, info.Tag, true, s.release)` on `pool.WithLocator(loc)` when `loc != ""`, written with `radosclient.OpFlagFullTry`; `ErrNotFound` counts as success (`:413-415`). `store.go`'s `Open` runs `s.gcInitialize(ctx)` (its error only logged) and, when `opts.gcThreads`, `s.AddWorker("gc", newGCWorker(s).run)`.

- [ ] **Step 4: Run, lint, commit**

Run: `go test -tags=ceph_preview -race ./internal/testutil/fakerados/ ./internal/driver/`; expected PASS. `make check`, then `feat(driver): the GC worker over both shard formats under gc_process`. Draft PR `feat(driver): GC worker (unit W task 9)`.

---
### Task 10: `op`: `PutObject`, `DeleteObject`, `DeleteObjects`, `CopyObject`, `PutObjectACL`, `PutObjectTagging`, `DeleteObjectTagging`, conditional PUT

**Files:**
- Create: `internal/op/putobject.go`, `internal/op/deleteobject.go`, `internal/op/deleteobjects.go`, `internal/op/copyobject.go`, `internal/op/objectattrs.go`, `internal/op/putobject_test.go`, `internal/op/deleteobject_test.go`, `internal/op/deleteobjects_test.go`, `internal/op/copyobject_test.go`, `internal/op/objectattrs_test.go`
- Modify: `internal/op/readconds.go` (R Task 5: export `CheckReadConditions` if R kept the four-rule table inside `GetObject`), `internal/op/errors.go` (`ErrMetadataNameTooLong`, an `*Error{Code: "Metadata name too long", Status: 400}` outside G's radosgw table, as [`rgw_common.cc:149`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L149) spells it)

**Interfaces:**
- Consumes: G's `op.Op`, `Run`, `Request`, `Env`, `VerifyBucketPermission`, `VerifyObjectPermission`, `VerifyBucketPermissionIn`, `ObjectStore`, `StatsStore.CheckQuota`, `ZoneInfo.Placement`, `Error.WithMessage`, the sentinels; Z's `acl.PermFor`, `acl.Policy`, `acl.DecodePolicy`, `acl.DecodePublicAccessBlock`, `acl.Policy.IsPublic`, `tags.Set`, `tags.Attr`, `policy.S3PutObject`, `S3DeleteObject`, `S3DeleteObjectVersion`, `S3GetObject`, `S3GetObjectVersion`, `S3PutObjectAcl`, `S3PutObjectVersionAcl`, `S3PutObjectTagging`, `S3PutObjectVersionTagging`, `S3DeleteObjectTagging`, `S3DeleteObjectVersionTagging`; M's `LogUsage`, `op.AttrPublicAccess`; R's `ParseHTTPTime`, `Unquote`, `PrefetchObject`; A's body contract (`r.Body`, `r.ContentLength`); Tasks 5-8's contract fields (`PutParams.Tag`, `ContentMD5`, `DeleteParams.UnmodifiedSince`, `IfMatchSize`, `IfMatchLastModified`, `CopyParams.Tag`); `meta.PlacementRule.InheritFrom`, `meta.TierTypeCloudS3` (R named it), `cephconf.Options` (`rgw_max_put_size`, `rgw_delete_multi_obj_max_num`, `rgw_multi_obj_del_max_aio`).
- Produces:

```go
package op

// PutObject is RGWPutObj for S3 (rgw_op.cc:3920-4560; rgw_rest_s3.cc
// :2597-2704, :2730-2782). The handler fills the inputs; Execute streams the
// body through ObjectStore.PutObject, which reads it to EOF and surfaces
// auth's verification verdict before the head is written.
type PutObject struct {
	Body                 io.Reader         // r.Body: auth's verifying reader
	Size                 int64             // r.ContentLength; -1 for a chunked body of unknown length
	Attrs                map[string][]byte // the handler's requestAttrs: content type, generic headers, every x-amz-* header, NUL-terminated
	ACL                  acl.Policy        // create_s3_policy: canned, grant headers or the default
	CannedACL            string            // x-amz-acl, for the public-access-block refusal (init_processing, :3900-3906)
	Tags                 *tags.Set         // x-amz-tagging, nil when absent
	StorageClass         string            // x-amz-storage-class, "" when absent
	ContentMD5           []byte            // decoded Content-MD5, nil when absent
	IfMatch, IfNoneMatch string            // raw header values

	ETag      string
	Mtime     time.Time
	VersionID string // "" until versioning is implemented
}

// DeleteObject is RGWDeleteObj (rgw_op.cc:5138-5320; rgw_rest_s3.cc:3427-3470; v20.2.4 :3685-3733).
type DeleteObject struct {
	Versioned           bool   // r.Object.Instance != ""
	UnmodifiedSince     string // x-amz-delete-if-unmodified-since, url-decoded by the handler
	IfMatch             string // If-Match (Tentacle only; the handler leaves it empty on Squid)
	IfMatchSize         string // x-amz-if-match-size (Tentacle)
	IfMatchLastModified string // x-amz-if-match-last-modified-time (Tentacle)

	VersionID    string
	DeleteMarker bool
}

// DeleteResult is one key's outcome of a DeleteObjects: what
// send_partial_response renders (rgw_rest_s3.cc:4273-4329).
type DeleteResult struct {
	Key             meta.ObjKey // the requested key; its Instance renders as VersionId
	Err             error       // nil for success
	DeleteMarker    bool        // the delete created a delete marker: del_op->result.delete_marker
	MarkerVersionID string      // that marker's version id: del_op->result.version_id
}

// DeleteObjectsEntry is one <Object> of the request (RGWMultiDelObject).
type DeleteObjectsEntry struct {
	Key                 meta.ObjKey // Key, with VersionId as Instance
	IfMatch             string      // <ETag>, read on Tentacle only
	IfMatchSize         *uint64     // <Size>, read on Tentacle only
	IfMatchLastModified time.Time   // <LastModifiedTime>, read on Tentacle only; zero when absent
}

// DeleteObjects is RGWDeleteMultiObj (rgw_op.cc:6758-7104; v20.2.4
// :7697-8020). Init reads the body, Execute parses it through Parse and runs
// the whole-request checks, then calls Begin once and hands each key's
// outcome to Result as the key completes, the way radosgw streams its
// response; a check that fails before the first key goes to Status instead.
type DeleteObjects struct {
	// Parse is the protocol's parser for the body (RGWMultiDelXMLParser,
	// rgw_multi_del.cc), called where execute parses; its error is sent
	// through Status.
	Parse func(body []byte) ([]DeleteObjectsEntry, error)
	// Status is send_status: called instead of Begin when Execute fails
	// before the first key, to send that error's status line and nothing else.
	Status func(err error)
	// Begin is begin_response: called once, after the whole-request checks and
	// before the first key is deleted, to send the status, the headers and the
	// DeleteResult open tag. An error from it ends Execute.
	Begin func() error
	// Result is send_partial_response: called once per key, in completion
	// order, never concurrently.
	Result func(DeleteResult)

	body []byte
}

// CopyObject is RGWCopyObj (rgw_op.cc:5407-5690; rgw_rest_s3.cc:3478-3603).
type CopyObject struct {
	SrcTenant, SrcBucket string
	SrcKey               meta.ObjKey
	Attrs                map[string][]byte // requestAttrs, used when Replace (set_copy_attrs REPLACE)
	ACL                  acl.Policy        // init_dest_policy
	Replace              bool              // x-amz-metadata-directive: REPLACE
	StorageClass         string            // x-amz-storage-class
	CheckStorageClass    bool              // need_to_check_storage_class: same source and destination, no versionId, not REPLACE
	IfMatch, IfNoneMatch string            // x-amz-copy-source-if-match / -none-match
	IfModifiedSince      string            // x-amz-copy-source-if-modified-since
	IfUnmodifiedSince    string            // x-amz-copy-source-if-unmodified-since

	ETag      string
	Mtime     time.Time
	VersionID string

	srcRec *BucketRecord
	src    *ObjectState
}

// PutObjectACL is RGWPutACLs at object scope (rgw_op.cc:5738-5931). Build is
// the handler's closure over authz.Evaluator.BuildACL: it receives the stored
// policy once Init has read the head, and returns the new one or the authz
// error through authz.ErrorFor.
type PutObjectACL struct {
	Versioned bool
	Build     func(existing acl.Policy) (acl.Policy, error)
}

// PutObjectTagging is RGWPutObjTags (rgw_op.cc:1076-1108).
type PutObjectTagging struct {
	Versioned bool
	Set       tags.Set
}

// DeleteObjectTagging is RGWDeleteObjTags (rgw_op.cc:1117-1138).
type DeleteObjectTagging struct{ Versioned bool }

// CheckReadConditions is RGWRados::Object::Read::prepare's conditionals
// (rgw_rados.cc:6946-6990) as GetObject and CopyObject apply them to a
// source state: ErrInvalidArgument for an unparsable date, ErrNotModified,
// ErrPreconditionFailed.
func CheckReadConditions(st *ObjectState, ifModifiedSince, ifUnmodifiedSince, ifMatch, ifNoneMatch string) error
```

R Task 5 owns the `CheckReadConditions` table; W exports it.

Semantics, transcribed:

- **PutObject.** `Name` `put_obj`; `Action` `S3PutObject`; `OpMask` `OpTypeWrite`. `Init`: `r.BucketRec = Env.Buckets.GetBucket(ctx, r.Tenant, r.Bucket)` (a missing bucket is `ErrNoSuchBucket` from `init_permissions`, `rgw_op.cc:539-541`); the destination placement `meta.PlacementRule{StorageClass: o.StorageClass}.InheritFrom(rec.Info.PlacementRule)` must resolve through `Env.Zone.Placement`, else `ErrInvalidArgument` ([`:576-583`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L576-L583), before any permission check); `Size > rgw_max_put_size` → `ErrEntityTooLarge` (`verify_params`, [rgw_rest.cc:1049-1059](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L1049-L1059), also before permissions). `VerifyPermission`: `VerifyBucketPermission(ctx, r, S3PutObject, acl.PermFor(S3PutObject))` (`:3982-3985`). `Execute` ([`:4142-4560`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L4142-L4560)): `r.Object.Name == ""` → `ErrInvalidArgument`; `CannedACL` in {`public-read`, `public-read-write`, `authenticated-read`} with the bucket's `PublicAccessBlock.BlockPublicACLs` (decoded from `r.BucketRec.Attrs[AttrPublicAccess]`) → `ErrAccessDenied` ([`:3900-3906`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L3900-L3906)); `Size >= 0` and not `Identity.System` → `Env.Stats.CheckQuota(ctx, rec, rec.Info.Owner, Size, 1)` ([`:4200-4207`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L4200-L4207)); attrs = clone(`Attrs`) + `user.rgw.acl` = `ACL.Encode` at the release + `tags.Attr` = `Tags.Encode` when set ([`:4464-4465`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L4464-L4465), `encode_obj_tags_attr`); `res := Env.Objects.PutObject(ctx, rec, r.Object, Body, PutParams{Attrs, Size, StorageClass, IfMatch, IfNoneMatch, Tag: r.ID, ContentMD5})`; `ETag, Mtime = res.ETag, res.Mtime`. `Complete`: `LogUsage(ctx, r, "put_obj")`.
- **DeleteObject.** `Name` `delete_obj`; `Action` `S3DeleteObjectVersion` when `Versioned` else `S3DeleteObject`; `OpMask` `OpTypeDelete`. `Init`: the bucket. `VerifyPermission`: `VerifyBucketPermission(ctx, r, action, acl.PermFor(action))` ([`:5148-5150`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5148-L5150)). `Execute`: parse `UnmodifiedSince` with `utime_t::parse_date`'s grammar (`YYYY-MM-DD[ T]HH:MM:SS[.frac][Z]`, [include/utime.h:397-440](https://github.com/ceph/ceph/blob/v19.2.6/src/include/utime.h#L397-L440); bad → `ErrInvalidArgument`); on Tentacle `IfMatchSize` → `strconv.ParseInt` (bad → `ErrInvalidArgument`), `IfMatchLastModified` → `ParseHTTPTime` (bad → its error); `err := Env.Objects.DeleteObject(ctx, rec, r.Object, DeleteParams{IfMatch, UnmodifiedSince, IfMatchSize, IfMatchLastModified})`; `ErrNoSuchKey` → nil (the 204 for a missing key, [rgw_rest_s3.cc:3457-3461](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L3457-L3461)); `ErrConcurrentModification` → nil ([`:5302-5304`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L5302-L5304)); anything else as is. `Complete`: `LogUsage`.
- **DeleteObjects.** `Name` `multi_object_delete`; `Action` `S3DeleteObject`; `OpMask` `OpTypeDelete`. `Init` is `init_permissions` then `get_params` (`init_processing`, [`:6758-6766`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L6758-L6766); [rgw_rest.cc:1660-1674](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L1660-L1674)): the bucket (`ErrNoSuchBucket` before the body is touched), then the whole body as `read_all_input` reads it with `allow_chunked` false ([rgw_rest.cc:1537-1578](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L1537-L1578)) — no length (a chunked body, whose `ContentLength` is -1, or a request with no `Content-Length` header at all) → `ErrMissingContentLength` (411), a `Content-Length` over `rgw_max_put_param_size` → `ErrInvalidRange` (`-ERANGE`, 416), else the body read to EOF, whose error (a short body, A's payload-hash verdict) is returned; these reach radosgw's `abort_early`, an ordinary error response. `VerifyPermission`: nil (`:6768-6779`, the per-key checks are Execute's). `Execute` (`:7007-7104`; v20.2.4 `:7921-8013`): the whole-request checks, in execute's order, each sent through `Status` and returned, because radosgw's `execute` answers them with `send_status()` alone (`goto error`, [`:7102-7104`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L7102-L7104); at v20.2.4 it returns and `RGWDeleteMultiObj::send_response` calls `send_status`, [`:8016-8020`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L8016-L8020)): an empty body → `ErrInvalidArgument` (`data.c_str()` is NULL, [`:7019-7023`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L7019-L7023); v20.2.4 [`:7923-7927`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7923-L7927)); `Parse`'s error (Squid's `-EINVAL`, `:7025-7039`; Tentacle's `MalformedXML` messages, v20.2.4 [`:7929-7946`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7929-L7946)); on Tentacle an empty entry list → `ErrMalformedXML` "Missing required element Object" (v20.2.4 [`:7948-7952`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7948-L7952)), where a Squid radosgw streams an empty `DeleteResult` ([`:7070-7072`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7070-L7072)); more entries than `rgw_delete_multi_obj_max_num` (negative → 1000) → `ErrMalformedXML` ([`:7040-7049`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L7040-L7049); on Tentacle with radosgw's message "Object count limit <n> exceeded", v20.2.4 [`:7954-7964`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7954-L7964)). Then `Begin()` (`begin_response`, [`:7070`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L7070)), then the keys with at most `max(1, rgw_multi_obj_del_max_aio)` in flight (`semaphore`), each through `deleteOne`, which is `handle_individual_object` ([`:6818-6910`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L6818-L6910); v20.2.4 [`:7743-7855`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7743-L7855)): empty `Name` → `ErrInvalidArgument` (`o.empty()`; the S3 parser already refuses an empty key); `VerifyBucketPermissionIn(ctx, r, S3DeleteObject or S3DeleteObjectVersion, acl.PermFor(action), r.BucketRec, key)` failing → `ErrAccessDenied` (`:6829-6837`, `-EACCES` whatever the evaluator's reason); `Env.Objects.DeleteObject(ctx, rec, key, p)` with, on Tentacle, the entry's `IfMatch`, `IfMatchSize` and `IfMatchLastModified` in `p` (v20.2.4 `:7818-7820`; W-D12's driver checks them) and on Squid none; `ErrNoSuchKey` → nil (`:6891-6893`); any other error, `ErrConcurrentModification` included, is that key's `Err`. The outcome goes to `Result` under a mutex, in completion order. `DeleteMarker` and `MarkerVersionID` carry `del_op->result` ([`:6909`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L6909); v20.2.4 [`:7854`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7854)), and they stay false and empty here: `ObjectStore.DeleteObject` reports no outcome and every delete in this plan is unversioned, as `del_op->result` is for an unversioned bucket; the versioning work fills them from the store. `Complete`: `LogUsage`.
- **CopyObject.** `Name` `copy_obj`; `Action` `S3PutObject`; `OpMask` `OpTypeWrite`. `Init`: the destination bucket and its placement check as PutObject's; `srcRec = Env.Buckets.GetBucket(ctx, SrcTenant, SrcBucket)` (`ErrNoSuchBucket`); `src = Env.Objects.PrefetchObject(ctx, srcRec, SrcKey)` (`read_obj_policy` with `set_prefetch_data`, [`:5413-5420`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5413-L5420); the prefetch is what `copy_first` reads). `VerifyPermission` ([`:5407-5497`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5407-L5497)): `VerifyBucketPermissionIn(ctx, r, S3GetObject or S3GetObjectVersion by SrcKey.Instance, acl.PermFor(action), srcRec, SrcKey)` — the bucket form with the source's ACL, policy and `ARN(src_obj)` while the public-access block and requester-pays stay the destination's, because radosgw calls `verify_bucket_permission(this, s, ARN(src), s->user_acl, src_bucket_acl, src_policy, …)` with `s` the destination request ([`:5454-5457`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5454-L5457); identical at v20.2.4 [`:6020-6023`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L6020-L6023)); a request copy with `BucketRec` swapped would have evaluated both on the source; `!src.Exists` → `ErrNoSuchKey` (`read_obj_policy`'s ENOENT); `CheckStorageClass` → `meta.PlacementRule{StorageClass: rgwBlStr(src.Attrs[AttrStorageClass])}.InheritFrom(srcRec.Info.PlacementRule)` equal to the destination placement → `ErrInvalidRequest.WithMessage("This copy request is illegal because it is trying to copy an object to itself without changing the object's metadata, storage class, website redirect location or encryption attributes.")` ([rgw_rest_s3.cc:3553-3564](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L3553-L3564)); then `VerifyBucketPermission(ctx, r, S3PutObject, acl.PermFor(S3PutObject))`. `Execute` ([`:5558-5690`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L5558-L5690)): `CheckReadConditions(src, IfModifiedSince, IfUnmodifiedSince, IfMatch, IfNoneMatch)` (`init_common` parse failures → `ErrInvalidArgument`, `Read::prepare` → 304/412); `src.Manifest != nil` with tier type `cloud-s3` (Tentacle: or `cloud-s3-glacier`) → `ErrInvalidObjectState.WithMessage("This object was transitioned to cloud-s3")`; not `System`: `accounted > rgw_max_put_size` → `ErrEntityTooLarge`, `Env.Stats.CheckQuota(ctx, rec, rec.Info.Owner, accounted, 1)` ([`:5627-5636`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L5627-L5636)); attrs = clone(`Attrs`) + `user.rgw.acl`; `res := Env.Objects.CopyObject(ctx, src, rec, r.Object, CopyParams{Attrs, ReplaceAttrs: Replace, StorageClass, Tag: r.ID})`; `ETag, Mtime`. `Complete`: `LogUsage`.
- **PutObjectACL.** `Name` `put_acls`; `Action` `S3PutObjectAcl`/`S3PutObjectVersionAcl`; `OpMask` `OpTypeWrite`. `Init`: the bucket and `r.ObjState = Env.Objects.StatObject(ctx, rec, r.Object)` (`is_obj_update_op` → object policies read, [rgw_rest.cc:1910-1913](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L1910-L1913)). `VerifyPermission`: `VerifyObjectPermission(ctx, r, action, acl.PermFor(action))`, whose `ErrNoSuchKey` is the 404 (D-Z5). `Execute` ([`:5821-5931`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5821-L5931)): `existing` = `acl.DecodePolicy` of `r.ObjState.Attrs[AttrACL]` (a zero policy when absent); `newP, err := Build(existing)` (Z's owner check, grant limit, canned/header/body rules, already mapped through `authz.ErrorFor`); the bucket's `PublicAccessBlock.BlockPublicACLs` with `newP.IsPublic()` → `ErrAccessDenied` ([`:5905-5910`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5905-L5910)); `Env.Objects.SetObjectAttrs(ctx, r.ObjState, {AttrACL: encode(newP)}, nil)`; `ErrConcurrentModification` → nil ([`:5928-5930`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5928-L5930), ACLs are immutable in a race). `Complete`: `LogUsage`.
- **PutObjectTagging.** `Name` `put_obj_tags`; `Action` `S3PutObjectTagging`/`S3PutObjectVersionTagging`; `OpMask` `OpTypeWrite`. `Init` and `VerifyPermission` as PutObjectACL's. `Execute`: `Env.Objects.SetObjectAttrs(ctx, r.ObjState, {tags.Attr: Set.Encode}, nil)`; `ErrConcurrentModification` → `ErrTagConflict` (`:1104-1106`, 409 OperationAborted). **DeleteObjectTagging.** `Name` `delete_obj_tags`; `Action` `S3DeleteObjectTagging`/`…Version…`; `OpMask` `OpTypeDelete`; `Execute`: `SetObjectAttrs(ctx, r.ObjState, nil, []string{tags.Attr})`. Both `Complete`: `LogUsage`.

- [ ] **Step 1: Write the failing op specs on `memstore`**

`internal/op/putobject_test.go` (package `op_test`, `memstore` with `alice` owning `plain`, `Authz: op.OwnerOnly{}`, a `req(method, bucket, key)` helper as R's specs use):

```go
var _ = Describe("PutObject", func() {
	It("stores the body with the ACL, tags and metadata attrs and reports the ETag", func(ctx SpecContext) {
		o := &op.PutObject{Body: strings.NewReader("hello"), Size: 5, Attrs: map[string][]byte{meta.AttrContentType: []byte("text/plain\x00"), "user.rgw.x-amz-meta-k": []byte("v\x00")},
			ACL: acl.DefaultPolicy(aliceOwner, "Alice"), Tags: &tags.Set{Tags: []tags.Tag{{Key: "a", Value: "b"}}}}
		r := req("PUT", "plain", "k")
		Expect(op.Run(ctx, o, r)).To(Succeed())
		Expect(o.ETag).To(Equal("5d41402abc4b2a76b9719d911017c592"))
		st, err := store.StatObject(ctx, r.BucketRec, meta.ObjKey{Name: "k"})
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Attrs).To(HaveKey(meta.AttrACL))
		Expect(st.Attrs).To(HaveKey(tags.Attr))
		Expect(st.Attrs).To(HaveKeyWithValue("user.rgw.x-amz-meta-k", []byte("v\x00")))
		Expect(st.WriteTag).To(Equal(r.ID), "the request id is the write tag")
		Expect(store.Usage()).To(ContainElement(HaveField("Category", "put_obj")))
	})
	It("refuses a bucket that does not exist before checking permissions", func(ctx SpecContext) {
		Expect(op.Run(ctx, &op.PutObject{Body: strings.NewReader("x"), Size: 1}, req("PUT", "nope", "k"))).To(MatchError(op.ErrNoSuchBucket))
	})
	It("refuses an unknown storage class as InvalidArgument", func(ctx SpecContext) {
		Expect(op.Run(ctx, &op.PutObject{Body: strings.NewReader("x"), Size: 1, StorageClass: "GLACIAL"}, req("PUT", "plain", "k"))).To(MatchError(op.ErrInvalidArgument))
	})
	It("refuses a Content-Length over rgw_max_put_size before reading", func(ctx SpecContext) {
		// conf rgw_max_put_size 10: Size 11 → op.ErrEntityTooLarge, nothing stored
	})
	It("checks the quota against the announced size", func(ctx SpecContext) {
		// a bucket quota of one object already used: → op.ErrQuotaExceeded
	})
	It("passes a Content-MD5 to the store and reports BadDigest", func(ctx SpecContext) {
		sum := md5.Sum([]byte("other"))
		Expect(op.Run(ctx, &op.PutObject{Body: strings.NewReader("hello"), Size: 5, ContentMD5: sum[:]}, req("PUT", "plain", "k"))).To(MatchError(op.ErrBadDigest))
	})
	It("refuses a public canned ACL under a public access block", func(ctx SpecContext) {
		// bucket attr op.AttrPublicAccess with BlockPublicACLs: CannedACL "public-read" → op.ErrAccessDenied
	})
	It("passes If-Match and If-None-Match to the store", func(ctx SpecContext) {
		// memstore implements the Tentacle rule: If-None-Match "*" on an existing key → op.ErrPreconditionFailed; If-Match "*" on a missing key → op.ErrNoSuchKey
	})
	It("surfaces the body's verification verdict and stores nothing", func(ctx SpecContext) {
		body := io.MultiReader(strings.NewReader("hel"), errReader{op.ErrSignatureDoesNotMatch})
		Expect(op.Run(ctx, &op.PutObject{Body: body, Size: 5}, req("PUT", "plain", "k"))).To(MatchError(op.ErrSignatureDoesNotMatch))
		st, _ := store.StatObject(ctx, plainRec, meta.ObjKey{Name: "k"})
		Expect(st.Exists).To(BeFalse())
	})
})
```

`deleteobject_test.go`: success → 204 semantics (nil), the usage entry; a missing key → nil; `UnmodifiedSince` unparsable → `ErrInvalidArgument`, in the past → `ErrPreconditionFailed`; on a Tentacle env `IfMatch` mismatch → `ErrPreconditionFailed`, `IfMatchSize` "abc" → `ErrInvalidArgument`; `Versioned` selects `S3DeleteObjectVersion` (`Action()` table). `deleteobjects_test.go` (the op with recording `Status`, `Begin` and `Result`, a `Parse` returning fixed entries or an error, and a request carrying a one-byte body `x` with `Content-Length: 1`): three keys of which one missing and one empty → `Begin` once, before the first `Result`, then three `Result` calls carrying nil, nil and `ErrInvalidArgument` in some order, and both existing objects are gone; a `Result` that sleeps while an atomic counter tracks concurrent calls never sees two at once (`rgw_multi_obj_del_max_aio` 4, eight keys); 1001 entries → `ErrMalformedXML` through `Status`, once, and `Begin` never runs; a `Parse` error → that error through `Status`; an empty body with `Content-Length: 0` → `ErrInvalidArgument` through `Status` and `Parse` never called; on a Tentacle env an empty entry list → `ErrMalformedXML` with the message "Missing required element Object" through `Status`, on Squid `Begin` runs and no `Result` follows; no length (`ContentLength` -1, or 0 without the header) → `ErrMissingContentLength` and a `Content-Length` over `rgw_max_put_param_size` → `ErrInvalidRange`, both from `Init` with `Status`, `Parse` and `Begin` never called; a missing bucket → `ErrNoSuchBucket` with the body unread (a reader that fails the spec when read); a key alice may not delete (a second user's request with `OwnerOnly`) → `ErrAccessDenied` in that key's result and the other keys deleted; on a Tentacle env an entry whose `IfMatch` is not the object's etag → `ErrPreconditionFailed` in its result and the object stays, while on Squid the same entry deletes it; every result's `DeleteMarker` is false and `MarkerVersionID` empty. `copyobject_test.go`: a plain copy keeps metadata, `Replace` uses the request's, the ETag is the source's; a missing source → `ErrNoSuchKey`; a missing source bucket → `ErrNoSuchBucket`; `CheckStorageClass` on a same-class self-copy → `ErrInvalidRequest` with the message; `IfMatch` mismatch → `ErrPreconditionFailed`, `IfModifiedSince` in the future → `ErrNotModified`, an unparsable date → `ErrInvalidArgument`; a source over `rgw_max_put_size` → `ErrEntityTooLarge`. `objectattrs_test.go`: `PutObjectACL` with a `Build` returning a policy → the attr changes; a `Build` returning `op.ErrAccessDenied` (Z's owner mismatch) → that error; `PutObjectTagging` stores `tags.Attr`; `DeleteObjectTagging` removes it; each on a missing object → `ErrNoSuchKey` (D-Z5 through `VerifyObjectPermission`).

- [ ] **Step 2: Run to fail, implement the ops**

`putobject.go`:

```go
package op

type PutObject struct { /* as above */ }

func (*PutObject) Name() string          { return "put_obj" }
func (*PutObject) Action() policy.Action { return policy.S3PutObject }
func (*PutObject) OpMask() uint32        { return OpTypeWrite }

func (o *PutObject) Init(ctx context.Context, r *Request) error {
	rec, err := r.Env.Buckets.GetBucket(ctx, r.Tenant, r.Bucket)
	if err != nil {
		return err // ErrNoSuchBucket: init_permissions answers 404 before any permission check
	}
	r.BucketRec = rec
	if _, err := r.Env.Zone.Placement(meta.PlacementRule{StorageClass: o.StorageClass}.InheritFrom(rec.Info.PlacementRule)); err != nil {
		return ErrInvalidArgument // rgw_op.cc:576-583: invalid dest placement
	}
	if maxPut, err := r.Env.Conf.Size("rgw_max_put_size"); err == nil && o.Size > 0 && uint64(o.Size) > maxPut {
		return ErrEntityTooLarge // RGWPutObj_ObjStore::verify_params
	}
	return nil
}

func (o *PutObject) VerifyPermission(ctx context.Context, r *Request) error {
	return VerifyBucketPermission(ctx, r, policy.S3PutObject, acl.PermFor(policy.S3PutObject))
}

func (o *PutObject) Execute(ctx context.Context, r *Request) error {
	if r.Object.Name == "" {
		return ErrInvalidArgument
	}
	if blockPublicACLs(r.BucketRec) && slices.Contains([]string{"public-read", "public-read-write", "authenticated-read"}, o.CannedACL) {
		return ErrAccessDenied // init_processing, :3900-3906
	}
	if o.Size >= 0 && !r.Identity.System {
		if err := r.Env.Stats.CheckQuota(ctx, r.BucketRec, r.BucketRec.Info.Owner, o.Size, 1); err != nil {
			return err
		}
	}
	attrs := maps.Clone(o.Attrs)
	if attrs == nil {
		attrs = map[string][]byte{}
	}
	attrs[meta.AttrACL] = encodeAt(o.ACL, r.Env.Zone.Release())
	if o.Tags != nil {
		attrs[tags.Attr] = encodeAt(*o.Tags, r.Env.Zone.Release())
	}
	res, err := r.Env.Objects.PutObject(ctx, r.BucketRec, r.Object, o.Body, PutParams{
		Attrs: attrs, Size: o.Size, StorageClass: o.StorageClass, IfMatch: o.IfMatch, IfNoneMatch: o.IfNoneMatch, Tag: r.ID, ContentMD5: o.ContentMD5,
	})
	if err != nil {
		return err
	}
	o.ETag, o.Mtime, o.VersionID = res.ETag, res.Mtime, res.Version
	return nil
}

func (o *PutObject) Complete(ctx context.Context, r *Request) { LogUsage(ctx, r, o.Name()) }

// blockPublicACLs reads the bucket's PublicAccessBlock (op.AttrPublicAccess); absent means false.
func blockPublicACLs(rec *BucketRecord) bool
```

`encodeAt` is `op`'s small helper over `denc.NewEncoder` (add it if none exists). The conditional-PUT step is the two `IfMatch`/`IfNoneMatch` pass-throughs above plus the handler's two header reads in Task 11 and the `conditional_write` marker in Task 13; cutting W-D3 removes exactly those lines and the driver ignores empty conditions. `deleteobject.go`, `deleteobjects.go`, `copyobject.go`, `objectattrs.go` follow the semantics above with the same shape (`Init` loading what the op needs, `VerifyPermission` one call, `Execute` transcribing the C++ in order, `Complete` → `LogUsage`); `deleteobjects.go`'s body read, whole-request checks and fan-out:

```go
// Init is init_permissions, then get_params during init_processing
// (rgw_op.cc:6758-6766; rgw_rest.cc:1660-1674): the bucket, then the whole
// body as read_all_input reads it without chunked input (rgw_rest.cc:1537-1578).
// Its failures reach abort_early, an ordinary error response.
func (o *DeleteObjects) Init(ctx context.Context, r *Request) error {
	rec, err := r.Env.Buckets.GetBucket(ctx, r.Tenant, r.Bucket)
	if err != nil {
		return err
	}
	r.BucketRec = rec
	// read_all_input without allow_chunked: no CONTENT_LENGTH is
	// -ERR_LENGTH_REQUIRED, which a chunked body and a request without the
	// header both are; a zero length reads nothing.
	if r.ContentLength < 0 || (r.ContentLength == 0 && r.Header.Get("Content-Length") == "") {
		return ErrMissingContentLength
	}
	maxSize, err := r.Env.Conf.Size("rgw_max_put_param_size")
	if err != nil {
		maxSize = 1 << 20
	}
	if uint64(r.ContentLength) > maxSize { //nolint:gosec // not negative here
		return ErrInvalidRange // -ERANGE
	}
	o.body, err = io.ReadAll(r.Body)
	return err
}

func (o *DeleteObjects) Execute(ctx context.Context, r *Request) error {
	objects, err := o.checkRequest(r)
	if err != nil {
		// execute answers these with send_status alone (goto error,
		// rgw_op.cc:7102-7104; v20.2.4 returns and send_response calls
		// send_status, :8016-8020): no header of end_header's, no document.
		o.Status(err)
		return err
	}
	// begin_response (:7070): the status, the headers and the open tag go out
	// before the first key is deleted; every later failure is that key's.
	if err := o.Begin(); err != nil {
		return err
	}
	aio, err := r.Env.Conf.Uint64("rgw_multi_obj_del_max_aio")
	if err != nil {
		aio = 16
	}
	sem := semaphore.NewWeighted(int64(max(aio, 1))) //nolint:gosec // small
	var (
		mu sync.Mutex // send_partial_response writes one element at a time
		wg sync.WaitGroup
	)
	for _, e := range objects {
		if err := sem.Acquire(ctx, 1); err != nil {
			wg.Wait()
			return err
		}
		wg.Add(1)
		go func(e DeleteObjectsEntry) {
			defer wg.Done()
			defer sem.Release(1)
			res := o.deleteOne(ctx, r, e)
			mu.Lock()
			o.Result(res)
			mu.Unlock()
		}(e)
	}
	wg.Wait()
	return nil
}

// checkRequest is execute's work before begin_response, in its order: the
// body, its parse, Tentacle's empty list, the key count (rgw_op.cc:7019-7050;
// v20.2.4 :7923-7964).
func (o *DeleteObjects) checkRequest(r *Request) ([]DeleteObjectsEntry, error) {
	if len(o.body) == 0 {
		return nil, ErrInvalidArgument // data.c_str() is NULL for an empty body
	}
	objects, err := o.Parse(o.body)
	if err != nil {
		return nil, err
	}
	tentacle := r.Env.Zone.Release() != denc.Squid
	if tentacle && len(objects) == 0 {
		return nil, ErrMalformedXML.WithMessage("Missing required element Object") // v20.2.4 :7948-7952
	}
	maxNum, err := r.Env.Conf.Int64("rgw_delete_multi_obj_max_num")
	if err != nil || maxNum < 0 {
		maxNum = 1000 // DELETE_MULTI_OBJ_MAX_NUM
	}
	if int64(len(objects)) > maxNum {
		if tentacle {
			return nil, ErrMalformedXML.WithMessage(fmt.Sprintf("Object count limit %d exceeded", maxNum)) // v20.2.4 :7954-7964
		}
		return nil, ErrMalformedXML // :7040-7049
	}
	return objects, nil
}

// deleteOne is handle_individual_object (rgw_op.cc:6818-6910; v20.2.4
// :7743-7855): the per-key checks, the delete, and the outcome
// send_partial_response takes (:6909; v20.2.4 :7854).
func (o *DeleteObjects) deleteOne(ctx context.Context, r *Request, e DeleteObjectsEntry) DeleteResult {
	res := DeleteResult{Key: e.Key}
	if e.Key.Name == "" {
		res.Err = ErrInvalidArgument // o.empty(): -EINVAL
		return res
	}
	action := policy.S3DeleteObject
	if e.Key.Instance != "" {
		action = policy.S3DeleteObjectVersion
	}
	if err := VerifyBucketPermissionIn(ctx, r, action, acl.PermFor(action), r.BucketRec, e.Key); err != nil {
		res.Err = ErrAccessDenied // -EACCES whatever the evaluator's reason
		return res
	}
	var p DeleteParams
	if r.Env.Zone.Release() != denc.Squid { // the per-object conditions of v20.2.4 (:7818-7820)
		p.IfMatch, p.IfMatchSize, p.IfMatchLastModified = e.IfMatch, e.IfMatchSize, e.IfMatchLastModified
	}
	// DeleteObject reports no delete marker: the delete is unversioned, and
	// del_op->result carries neither a marker nor a version id then.
	err := r.Env.Objects.DeleteObject(ctx, r.BucketRec, e.Key, p)
	if errors.Is(err, ErrNoSuchKey) {
		err = nil // -ENOENT is success (:6891-6893)
	}
	res.Err = err
	return res
}
```

- [ ] **Step 3: Run, lint, commit**

Run: `go test -tags=ceph_preview -race ./internal/op/...`; expected PASS. `make check`, then `feat(op): the object write ops with radosgw's lifecycle and error mapping`. Draft PR `feat(op): object write ops (unit W task 10)`.

---

### Task 11: `s3`: handlers, XML documents, registration

**Files:**
- Create: `internal/s3/putobject.go`, `internal/s3/deleteobject.go`, `internal/s3/deleteobjects.go`, `internal/s3/copyobject.go`, `internal/s3/objectattrs.go`, `internal/s3/requestattrs.go`, `internal/s3/putobject_test.go`, `internal/s3/deleteobject_test.go`, `internal/s3/deleteobjects_test.go`, `internal/s3/copyobject_test.go`, `internal/s3/objectattrs_test.go`, `internal/s3/requestattrs_test.go`, `internal/s3/streaming_test.go`
- Modify: `internal/s3/object.go` (`objectHandlers()` gains the six routes; `init()` assigns `objectPutACLs = putObjectACL`), `internal/s3/export_test.go` (`DeleteResultElementForTest`)

**Interfaces:**
- Consumes: G's `HandlerFunc`, `WriteXML`, `WriteError`, `SetCommonHeaders`, `SetContentLength` (radosgw's `dump_content_length`: the length with `Accept-Ranges: bytes`), `xmlHeader`, `ISO8601`, `startChunkedXML` (the chunked framing DeleteObjects streams through), `xmltext.Escape` (ceph's XML text escaping, G Task 4), `op.Request` (`Header`, `Query`, `Body`, `ContentLength`, `Identity`, `Object`, `Tenant`, `Env`, `BucketRec`); M's `objectPutACLs`, `writeXMLDeclaration`, the `r.Env.Authz.(*authz.Evaluator)` accessor and `userResolver(r)` (`authz.UserResolver{Users: r.Env.Users, Accounts: r.Env.Accounts}`, M Task 7); G's `SetCommonHeaders` (which carries `x-amz-request-charged`, R-D15) and G's `responseWriter` (`Accept-Ranges: bytes` with every `Content-Length`, R-D14) — W's handlers add neither themselves; R's `parseInt`; Z's `authz.Evaluator.BuildACL`, `BuildDefaultACL`, `authz.ErrorFor`, `tags.ParseHeader`, `tags.ParseXML`, `tags.MaxObjectTags`; Task 10's ops; `cephconf.Options` (`rgw_max_put_param_size`, `rgw_max_attr_name_len`, `rgw_max_attr_size`, `rgw_max_attrs_num_in_req`).
- Produces:

```go
package s3

// requestAttrs is populate_with_generic_attrs (rgw_op.cc:3307-3316) and
// rgw_get_request_metadata (rgw_op.h:2171-2229) over req_info::init_meta_info
// (rgw_common.cc:421-464) and map_qs_metadata (rgw_rest_s3.cc:2581-2595):
// Content-Type and the six generic headers become their attrs; every request
// header whose lowercase name starts with x-amz-, x-goog-, x-dho-, x-rgw-,
// x-object-, x-container- or x-account- becomes user.rgw.x-amz-<rest> (the
// prefix rewritten, repeated headers joined with ","), the x-amz-meta-* query
// parameters likewise, except the four blocklisted names; a value that is not
// UTF-8 or holds a control character is quoted-printable "=?UTF-8?Q?…?=";
// every value gets a trailing NUL. The three rgw_max_attr* limits answer
// ErrMetadataNameTooLong, ErrUnknown (EFBIG) and ErrUnknown (E2BIG).
func requestAttrs(r *op.Request) (map[string][]byte, error)

// formatXattr is format_xattr (rgw_op.h:2138-2160) with mime_encode_as_qp
// (src/common/mime.c:19-50): bytes with the high bit, '=' or a control
// character become "=XX", the rest stay, inside "=?UTF-8?Q?" and "?=".
func formatXattr(v string) string

func putObject(ctx context.Context, w http.ResponseWriter, r *op.Request) error
func deleteObject(ctx context.Context, w http.ResponseWriter, r *op.Request) error
func deleteObjects(ctx context.Context, w http.ResponseWriter, r *op.Request) error
func copyObject(ctx context.Context, w http.ResponseWriter, r *op.Request) error
func putObjectACL(ctx context.Context, w http.ResponseWriter, r *op.Request) error // assigned to objectPutACLs
func putObjectTags(ctx context.Context, w http.ResponseWriter, r *op.Request) error
func deleteObjectTags(ctx context.Context, w http.ResponseWriter, r *op.Request) error

// deleteResultElement renders one key's outcome as send_partial_response
// does (rgw_rest_s3.cc:4273-4329): nothing for a success when quiet. Names
// and values go through xmltext.Escape, ceph's xml_stream_escaper
// (src/common/escape.cc:134-169), which XMLFormatter::dump_string applies
// (Formatter.cc:516-523).
func deleteResultElement(res op.DeleteResult, quiet bool) string

// parseDelete is RGWMultiDelXMLParser at the release (rgw_multi_del.cc): the
// entries of a <Delete> document and its Quiet flag, or the error radosgw's
// execute answers a malformed document with (-EINVAL on Squid, MalformedXML
// with its message on Tentacle). It is DeleteObjects' Parse.
func parseDelete(body []byte, rel denc.Release) (entries []op.DeleteObjectsEntry, quiet bool, err error)

// parseCopySource is RGWCopyObj::parse_copy_location (rgw_op.cc:5329-5374)
// plus postauth_init's tenant split: "[/]tenant:bucket/key[?versionId=v]".
func parseCopySource(v, defaultTenant string) (tenant, bucket string, key meta.ObjKey, ok bool)
```

Handler semantics (`rgw_rest_s3.cc`):

- **put_obj** (`get_params` [`:2597-2704`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L2597-L2704), `send_response` [`:2730-2782`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L2730-L2782)): `r.Query.Has("uploadId")` or `x-amz-copy-source` present → P's UploadPart/UploadPartCopy: `ErrNotImplemented` until P registers its handler (P replaces the branch); `r.ContentLength < 0` without `Transfer-Encoding: chunked` → `ErrMissingContentLength` (411); `?append` → `ErrNotImplemented`; the object-lock headers (`x-amz-object-lock-mode`, `-retain-until-date`, `-legal-hold`) on a bucket without `BucketObjLockEnabled` → `ErrInvalidRequest`, with it → `ErrNotImplemented` (phase 2); `Content-MD5` → base64 decode, not 16 bytes → `ErrInvalidDigest`; `x-amz-tagging` → `tags.ParseHeader(v, tags.MaxObjectTags)`, any error → `ErrInvalidArgument` ([`:2626-2637`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L2626-L2637)); `requestAttrs(r)`; the ACL through `authz.Evaluator.BuildDefaultACL(ctx, r, resolver, owner, bucketOwner)` (canned and grant headers; `OwnerOnly` fallback as M's `createBucket` does); `CannedACL = x-amz-acl`; `StorageClass = x-amz-storage-class`; `IfMatch`/`IfNoneMatch` (the W-D3 step); `op.Run`; response: `SetCommonHeaders`, `ETag: "<etag>"`, `SetContentLength(h, 0)` (`Content-Length: 0` and `Accept-Ranges: bytes`, as `send_response`'s `dump_content_length(s, 0)` sends them, [`:2747`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L2747), [T] [`:2911`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2911); no other success in this task names its length: DeleteObject's 204 ([`:3469`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L3469)) and the tagging and ACL PUTs and the tagging DELETE ([`:822`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L822), [`:3654`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L3654), [`:835`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L835)) end their header with none, so they carry no `Accept-Ranges`, R-D14), `x-amz-version-id` when non-empty, `x-amz-request-charged` (R-D15), status 200 (`rgw_s3_success_create_obj_status` is read through `Env.Conf` and honoured for 201 and 204).
- **delete_obj** ([`:3427-3470`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L3427-L3470)): `x-amz-delete-if-unmodified-since` (url-decoded), and on Tentacle `If-Match`, `x-amz-if-match-size`, `x-amz-if-match-last-modified-time` (url-decoded), into the op; `op.Run`; 204 with `x-amz-version-id` when set and `x-amz-delete-marker: true` when a marker (phase 2 never sets it), `x-amz-request-charged`, no body.
- **multi_object_delete** (`get_params` [rgw_rest.cc:1660-1674](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L1660-L1674) and [`:4231-4245`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L4231-L4245); the response [`:4247-4336`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L4247-L4336); the parser `rgw_multi_del.cc`): the op reads the body in its `Init` and parses it in `Execute` through `Parse`, where radosgw's `get_params` reads it (during `init_processing`, after the bucket is loaded, so its failures are ordinary error responses through `abort_early`) and `RGWDeleteMultiObj::execute` parses it. `Parse` is `parseDelete(body, release)`: `encoding/xml` into `struct{ Quiet *string; Object []struct{ Key *string; VersionId string; ETag *string; LastModifiedTime *string; Size *string } }` under root `Delete`; a parse failure, a missing `Delete` root, an `Object` whose `Key` is absent or empty (`RGWMultiDelObject::xml_end` fails the parse on both releases), and on Tentacle a `LastModifiedTime` that `op.ParseHTTPTime` rejects after url-decoding (as the single-object header is) or a `Size` that is not a base-10 integer (v20.2.4 [rgw_multi_del.cc:17-58](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_multi_del.cc#L17-L58)) fail it: Squid with `ErrInvalidArgument` (`-EINVAL`, [rgw_op.cc:7025-7039](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L7025-L7039)), Tentacle with `ErrMalformedXML` and radosgw's messages, "Failed to parse xml input" and "Missing require element Delete" (v20.2.4 [rgw_op.cc:7929-7946](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7929-L7946)). A Squid parser reads only `Key` and `VersionId`, so Squid ignores the three conditions; on Tentacle they become the entry's `IfMatch` (raw), `IfMatchLastModified` and `IfMatchSize`. `Quiet` is `strings.EqualFold(v, "true")`, kept for the renderer. The op's `Status` is `send_status` ([`:4247-4255`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L4247-L4255)): a failure `Execute` meets before the first key (an empty body, `Parse`'s error, Tentacle's empty list, the key count) makes radosgw's `execute` call `send_status()` alone ([rgw_op.cc:7102-7104](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L7102-L7104); v20.2.4 [`:8016-8020`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L8016-L8020)), so `end_header` never runs: no `x-amz-request-id`, no `Server`, no `Content-Type`, no error document. The frontend then completes the response with the `Content-Length: 0` its buffering filter sends when no length was announced, then `Date` and `Connection` ([rgw_client_io_filters.h:221-253](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_client_io_filters.h#L221-L253); [rgw_asio_client.cc:143-165](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_asio_client.cc#L143-L165), [:183-192](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_asio_client.cc#L183-L192); the filter chain [rgw_asio_frontend.cc:320-325](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_asio_frontend.cc#L320-L325); all identical at both tags). The handler's `Status` is `w.WriteHeader(op.AsError(err).Status)` with no header set and nothing written, which net/http completes the same way, `Content-Length: 0` and `Date`; the `Connection: Keep-Alive` beast adds and net/http does not is G Task 5's recorded frontend difference. The op's `Begin` is `begin_response` ([`:4257-4271`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L4257-L4271)) through G's `startChunkedXML`: `SetCommonHeaders`, `Content-Type: application/xml`, no `Content-Length`, 200, flushed, which makes net/http frame the body with `Transfer-Encoding: chunked`, as radosgw's `end_header(..., CHUNKED_TRANSFER_ENCODING)` does; then `xmlHeader`, flushed as `end_header`'s own flush sends the `dump_start` before it, and the `<DeleteResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">` open tag, flushed as `begin_response` flushes it ([`:4270`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L4270)). The op's `Result` adds `deleteResultElement(res, quiet)` and flushes, so each key's element leaves as it completes: a Squid radosgw flushes each element as it completes ([rgw_op.cc:6804-6816](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L6804-L6816) with [rgw_rest_s3.cc:4321-4327](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L4321-L4327)), a Tentacle one after starting each key and at the end (v20.2.4 [rgw_op.cc:7894-7908](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7894-L7908), [rgw_rest_s3.cc:4873-4877](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L4873-L4877)), the same bytes in the same order either way. When `op.Run` returns nil the handler writes `</DeleteResult>` and flushes (`end_response`, [`:4331-4336`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L4331-L4336)). An error `op.Run` returns before `Status` or `Begin` ran (the bucket, the body) is an ordinary error response through `WriteError`; after `Status` the response is complete; after `Begin` it can only be the request context ending, which is logged, and the response ends short. `deleteResultElement` renders a success, unless `quiet`, as `<Deleted><Key>k</Key>[<VersionId>v</VersionId>][<DeleteMarker>true</DeleteMarker><DeleteMarkerVersionId>m</DeleteMarkerVersionId>]</Deleted>`, `VersionId` when the requested key has an instance and the marker pair when the delete created one (`dump_bool` writes `true`, [`:4291-4300`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L4291-L4300)), and a failure, quiet or not, as `<Error><Key>k</Key><VersionId>v</VersionId><Code>C</Code><Message>C</Message></Error>` with `VersionId` always present and `C = op.AsError(err).Code`, which radosgw repeats as the message ([`:4306-4319`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L4306-L4319)). Every name and value goes through `xmltext.Escape` (G Task 4), which is ceph's `xml_stream_escaper`: `&amp;`, `&lt;`, `&gt;`, `&apos;`, `&quot;`, and `&#xhh;` for a control byte other than tab and newline ([src/common/escape.cc:134-169](https://github.com/ceph/ceph/blob/v19.2.6/src/common/escape.cc#L134-L169)), not `encoding/xml`'s `&#34;` and `&#39;`.
- **copy_obj** ([`:3478-3603`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L3478-L3603)): `parseCopySource(x-amz-copy-source, r.Identity.Tenant)` false → `ErrInvalidArgument` ([`:5036-5040`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L5036-L5040)); `x-amz-metadata-directive` `COPY`/`REPLACE` case-insensitively, anything else → `ErrInvalidArgument.WithMessage("Unknown metadata directive.")`; the four `x-amz-copy-source-if-*` headers; `x-amz-storage-class`; the object-lock headers as PUT's; `requestAttrs(r)`; the destination ACL through `BuildDefaultACL`; `CheckStorageClass = tenant, bucket and key equal the destination's, no `versionId`, directive != REPLACE`; `op.Run`; response 200 `application/xml`: `xmlHeader` + `<CopyObjectResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><LastModified>T</LastModified><ETag>&quot;e&quot;</ETag></CopyObjectResult>` (`dump_format("ETag", "\"%s\"")` escapes the quotes it adds, [`:3598`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L3598), [T] [`:3881`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L3881); both texts go through `xmltext.Escape`) where `T` is `rgw_to_iso8601`'s millisecond form on Squid (`o.Mtime.UTC().Format("2006-01-02T15:04:05.000Z")`) and the same truncated to whole seconds on Tentacle (`dump_time_exact_seconds`). The framing is radosgw's: `send_partial_response(0)` sends the status and headers with chunked encoding, then `xmlHeader` and the `CopyObjectResult` open tag, and flushes ([`:3566-3588`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L3566-L3588)); `send_response` writes `LastModified`, `ETag` and the close tag and flushes ([`:3590-3603`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L3590-L3603)). The handler does the same after `op.Run` succeeds: `SetCommonHeaders`, `Content-Type: application/xml`, no `Content-Length`, `w.WriteHeader(200)`, `xmlHeader` + `<CopyObjectResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`, `http.NewResponseController(w).Flush()`, then `<LastModified>T</LastModified><ETag>&quot;e&quot;</ETag></CopyObjectResult>` and a second flush. There is no `<Progress>` element: `progress_cb` reaches only `fetch_remote_obj`, never a local copy ([`rgw_rados.cc:4726-4736`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L4726-L4736); `copy_obj_data` takes no callback, [`:5034-5048`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5034-L5048)). An error from `op.Run` is an ordinary error response through `WriteError`, as radosgw's `end_header` renders its error document with a `Content-Length` when `op_ret` is set ([rgw_rest.cc:620-624](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L620-L624)).
- **put_acls, object scope** (`:3616-3655`, [`rgw_op.cc:5821-5931`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5821-L5931)): the body under `rgw_max_put_param_size`, over → `ErrMalformedXML.WithMessage("The XML you provided was larger than the maximum <n> bytes allowed.")`; a canned ACL header with a non-empty body → `ErrInvalidArgument`; `op.PutObjectACL{Versioned, Build: func(existing acl.Policy) (acl.Policy, error) { p, err := ev.BuildACL(ctx, r, resolver, existing, body, true); return p, authz.ErrorFor(err) }}`; `op.Run`; 200 with the XML declaration body (`writeXMLDeclaration`).
- **put_obj_tags** (`:776-825`): body under the limit (over → `ErrInvalidRange`), `tags.ParseXML(body, tags.MaxObjectTags)` (errors through `authz.ErrorFor`: `ErrMalformedXML`, `ErrInvalidTag`); `op.Run`; 200 with the declaration. **delete_obj_tags** ([`:827-836`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L827-L836)): `op.Run`; 204 without a body (radosgw's `dump_start` after a 204 is a declaration no HTTP client reads; Go's server refuses a body on 204).
- `objectHandlers()` returns R's three entries plus `put_obj`, `delete_obj`, `multi_object_delete`, `copy_obj`, `put_obj_tags`, `delete_obj_tags`; `object.go`'s `init()` sets `objectPutACLs = putObjectACL` beside R's `objectGetACLs`.

- [ ] **Step 1: Write the failing `requestAttrs` specs**

`internal/s3/requestattrs_test.go`:

```go
var _ = Describe("requestAttrs", func() {
	It("maps the generic headers and every x-amz header, NUL-terminated", func() {
		r := reqWithHeaders(http.Header{"Content-Type": {"text/plain"}, "Content-Language": {"en"}, "Expires": {"Thu, 01 Jan 2026 00:00:00 GMT"}, "Cache-Control": {"no-cache"}, "Content-Disposition": {"inline"}, "Content-Encoding": {"gzip"}, "X-Robots-Tag": {"noindex"},
			"X-Amz-Meta-Foo": {"bar"}, "X-Amz-Date": {"20260928T120000Z"}, "X-Amz-Content-Sha256": {"UNSIGNED-PAYLOAD"}, "X-Goog-Meta-Baz": {"q"}, "X-Amz-Server-Side-Encryption-Customer-Key": {"k"}, "X-Amz-Storage-Class": {"COLD"}})
		attrs, err := requestAttrs(r)
		Expect(err).NotTo(HaveOccurred())
		Expect(attrs).To(Equal(map[string][]byte{
			"user.rgw.content_type": []byte("text/plain\x00"), "user.rgw.content_language": []byte("en\x00"), "user.rgw.expires": []byte("Thu, 01 Jan 2026 00:00:00 GMT\x00"),
			"user.rgw.cache_control": []byte("no-cache\x00"), "user.rgw.content_disposition": []byte("inline\x00"), "user.rgw.content_encoding": []byte("gzip\x00"), "user.rgw.x-robots-tag": []byte("noindex\x00"),
			"user.rgw.x-amz-meta-foo": []byte("bar\x00"), "user.rgw.x-amz-date": []byte("20260928T120000Z\x00"), "user.rgw.x-amz-content-sha256": []byte("UNSIGNED-PAYLOAD\x00"),
			"user.rgw.x-amz-meta-baz": []byte("q\x00"), // x-goog- is rewritten to x-amz-
		}), "rgw_common.cc:421-464 and rgw_op.h:2171-2229: the SSE-C key and the storage class are blocklisted")
	})
	It("joins repeated headers with a comma and merges x-amz-meta query parameters", func() {
		// X-Amz-Meta-A: 1 and X-Amz-Meta-A: 2 → "1,2\x00"; ?x-amz-meta-q=v → user.rgw.x-amz-meta-q "v\x00" (map_qs_metadata)
	})
	It("quoted-printable encodes a value that is not UTF-8 or holds a control character", func() {
		Expect(formatXattr("caf\xe9")).To(Equal("=?UTF-8?Q?caf=E9?="))
		Expect(formatXattr("a\tb")).To(Equal("=?UTF-8?Q?a=09b?="))
		Expect(formatXattr("plain = text")).To(Equal("plain = text"), "valid UTF-8 without control characters stays as is")
		Expect(formatXattr("caf\u00e9")).To(Equal("caf\u00e9"), "valid multibyte UTF-8 is not encoded")
		Expect(formatXattr("\u00e9\n")).To(Equal("=?UTF-8?Q?=C3=A9=0A?="), "a control character forces the encoding of every high-bit byte")
	})
	It("enforces the three configured limits", func() {
		// rgw_max_attr_name_len 20 → a long x-amz-meta name → ErrMetadataNameTooLong (400 "Metadata name too long"); rgw_max_attr_size 3 → "abcd" → op.ErrUnknown; rgw_max_attrs_num_in_req 1 → two metas → op.ErrUnknown
	})
})
```

- [ ] **Step 2: Write the failing handler specs**

`internal/s3/putobject_test.go` on `memstore` through `newHandler` with an authenticator for `alice` (G Task 4's fixture):

```go
var _ = Describe("PUT object", func() {
	It("stores the object and answers radosgw's headers", func() {
		req := httptest.NewRequest("PUT", "/plain/k", strings.NewReader("hello"))
		req.Header.Set("Content-Type", "text/plain")
		req.Header.Set("X-Amz-Meta-Color", "blue")
		req.Header.Set("X-Amz-Tagging", "a=b")
		rec := do(req)
		Expect(rec.Code).To(Equal(200))
		Expect(rec.Header().Get("ETag")).To(Equal(`"5d41402abc4b2a76b9719d911017c592"`))
		Expect(rec.Header().Get("Content-Length")).To(Equal("0"))
		Expect(rec.Header().Get("Accept-Ranges")).To(Equal("bytes"))
		Expect(rec.Header()).NotTo(HaveKey("Content-Type"), "end_header omits it on an empty success")
		Expect(rec.Body.Len()).To(BeZero())
		head := do(httptest.NewRequest("HEAD", "/plain/k", nil))
		Expect(head.Header().Get("X-Amz-Meta-Color")).To(Equal("blue"))
		Expect(head.Header().Get("X-Amz-Tagging-Count")).To(Equal("1"))
	})
	It("requires a length or chunked encoding", func() {
		req := httptest.NewRequest("PUT", "/plain/k", nil)
		req.ContentLength = -1
		Expect(do(req).Code).To(Equal(411))
		Expect(do(req).Body.String()).To(ContainSubstring("<Code>MissingContentLength</Code>"))
	})
	It("rejects a bad Content-MD5 header and a mismatching one", func() {
		// "notbase64!" → 400 InvalidDigest; base64 of another 16 bytes → 400 BadDigest
	})
	It("rejects a malformed x-amz-tagging as InvalidArgument", func() {
		// "a=b=c" → 400 InvalidArgument (radosgw maps INVALID_TAG to EINVAL on PUT)
	})
	It("refuses object-lock headers on a bucket without object lock", func() {
		// x-amz-object-lock-legal-hold: ON → 400 InvalidRequest
	})
	It("passes If-None-Match: * and answers 412 on an existing key", func() {
		// PUT twice with If-None-Match: * → 200 then 412 PreconditionFailed
	})
	It("answers the configured success status", func() {
		// rgw_s3_success_create_obj_status 201 → 201
	})
	It("marks requester-pays uploads by non-owners", func() {
		// bucket RequesterPays, bob uploads → x-amz-request-charged: requester
	})
})
```

The `If-None-Match: *` spec belongs to the W-D3 conditional-PUT step.

`deleteobject_test.go`: 204 with an empty body for an existing and for a missing key; `x-amz-delete-if-unmodified-since: garbage` → 400 InvalidArgument; on Tentacle `If-Match: "wrong"` → 412, on Squid the same header → 204 (ignored). `deleteobjects_test.go`: a two-key body → 200 `application/xml` with two `<Deleted>` inside `<DeleteResult xmlns="…">` and no `Content-Length`; `<Quiet>true</Quiet>` → no `<Deleted>`, an `<Error>` still rendered; a missing or empty `Key` → 400 on both releases (InvalidArgument on Squid, MalformedXML on Tentacle), the status line alone; a body over the limit → 416 InvalidRange with the error document; a body without a length (`req.ContentLength = -1`) → 411 MissingContentLength with the error document; a missing bucket with an unparsable body → 404 NoSuchBucket with the error document, the bucket checked first; 1001 keys → 400, the status line alone, nothing streamed; an empty `<Delete/>` → 200 with `<DeleteResult xmlns="…"></DeleteResult>` on Squid and 400, the status line alone, on Tentacle; on Tentacle an `<Object>` with `<ETag>"wrong"</ETag>` → an `<Error>` with `PreconditionFailed` and the object kept, `<LastModifiedTime>yesterday</LastModifiedTime>` → 400, the status line alone; a key bob may not delete → `<Error><Key>k</Key><VersionId></VersionId><Code>AccessDenied</Code><Message>AccessDenied</Message></Error>`. The streaming and the element renderer are pinned by the two specs below. `copyobject_test.go`: `PUT /plain/dst` with `x-amz-copy-source: /plain/src` → 200 `application/xml` without a `Content-Length`, body `<?xml version="1.0" encoding="UTF-8"?><CopyObjectResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><LastModified>2026-09-28T12:00:00.000Z</LastModified><ETag>&quot;…&quot;</ETag></CopyObjectResult>` (Squid's form with the fixture clock on a whole second); `x-amz-copy-source: plain/src?versionId=v` parses the instance; `x-amz-copy-source: nothing` (no `/`) → 400 InvalidArgument; a self-copy without REPLACE → 400 InvalidRequest with the message; `x-amz-metadata-directive: FOO` → 400 "Unknown metadata directive."; `x-amz-copy-source-if-match: "wrong"` → 412; `tenant:bucket/key` source resolves the tenant; a missing source → 404 NoSuchKey with the error document and a `Content-Length`. `objectattrs_test.go`: `PUT /plain/k?acl` with `x-amz-acl: public-read` → 200 with exactly the declaration body, then `GET /plain/k?acl` shows AllUsers READ; a 2 MiB body → 400 MalformedXML with the message; canned + body → 400 InvalidArgument; `PUT /plain/k?tagging` with a two-tag document → 200 with the declaration, `GET ?tagging` returns them; eleven tags → 400 InvalidTag; `DELETE /plain/k?tagging` → 204 with no body; each on a missing key → 404 NoSuchKey.

`internal/s3/streaming_test.go` runs the handler behind `httptest.NewServer`, so the framing is what a client sees (`deleteResultElement` reaches the specs through `export_test.go` as `DeleteResultElementForTest`):

```go
var _ = Describe("streamed responses", func() {
	It("streams DeleteObjects: chunked, the open tag first, each element as its key completes", func(ctx SpecContext) {
		release := make(chan struct{})
		store := blockingDeleteStore(memstoreWith("plain", "a", "b"), "b", release) // DeleteObject of "b" waits for release
		srv := httptest.NewServer(newHandlerWith(store, conf(map[string]string{"rgw_multi_obj_del_max_aio": "1"})))
		defer srv.Close()
		body := `<Delete><Object><Key>a</Key></Object><Object><Key>b</Key></Object></Delete>`
		resp := doSigned(srv, "POST", "/plain?delete", body)
		defer resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(200))
		Expect(resp.TransferEncoding).To(Equal([]string{"chunked"}), "end_header(..., CHUNKED_TRANSFER_ENCODING), rgw_rest_s3.cc:4267")
		Expect(resp.ContentLength).To(BeEquivalentTo(-1))
		var (
			mu  sync.Mutex
			got bytes.Buffer
		)
		go func() { // the client's reader: whatever the server has flushed shows up in got
			defer GinkgoRecover()
			buf := make([]byte, 512)
			for {
				n, err := resp.Body.Read(buf)
				mu.Lock()
				got.Write(buf[:n])
				mu.Unlock()
				if err != nil {
					return
				}
			}
		}()
		read := func() string { mu.Lock(); defer mu.Unlock(); return got.String() }
		const opened = `<?xml version="1.0" encoding="UTF-8"?><DeleteResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Deleted><Key>a</Key></Deleted>`
		Eventually(read).Should(Equal(opened), "a's element arrives while b's delete is still blocked")
		Consistently(read).WithTimeout(50*time.Millisecond).Should(Equal(opened), "nothing more until b completes")
		close(release)
		Eventually(read).Should(Equal(opened + `<Deleted><Key>b</Key></Deleted></DeleteResult>`))
	})
	It("answers a DeleteObjects that fails before its first key with the status line alone, as send_status does", func(ctx SpecContext) {
		srv := httptest.NewServer(newHandlerWith(memstoreWith("plain"), conf(map[string]string{"rgw_delete_multi_obj_max_num": "1"})))
		defer srv.Close()
		for _, body := range []string{
			`<Delete><Object><Key>a</Key></Object><Object><Key>b</Key></Object></Delete>`, // more keys than rgw_delete_multi_obj_max_num
			`<Delete><Object>`, // a document that does not parse
			``,                 // an empty body
		} {
			resp := doSigned(srv, "POST", "/plain?delete", body)
			b, err := io.ReadAll(resp.Body)
			Expect(resp.Body.Close()).To(Succeed())
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(400), body)
			Expect(b).To(BeEmpty(), "no error document: execute calls send_status alone, rgw_op.cc:7102-7104")
			Expect(resp.ContentLength).To(BeZero(), "the Content-Length: 0 a response without end_header gets, rgw_client_io_filters.h:221-253")
			Expect(resp.TransferEncoding).To(BeEmpty(), body)
			Expect(resp.Header.Get("x-amz-request-id")).To(BeEmpty(), "dump_trans_id runs only in end_header")
			Expect(resp.Header.Get("Server")).To(BeEmpty(), body)
			Expect(resp.Header.Get("Content-Type")).To(BeEmpty(), body)
			Expect(resp.Header.Get("Accept-Ranges")).To(BeEmpty(), body)
		}
	})
	It("frames CopyObject's result as radosgw does: chunked, no Progress for a local copy", func(ctx SpecContext) {
		srv := httptest.NewServer(newHandlerWith(memstoreWith("plain", "src"), conf(nil)))
		defer srv.Close()
		resp := doSigned(srv, "PUT", "/plain/dst", "", "x-amz-copy-source", "/plain/src")
		defer resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(200))
		Expect(resp.TransferEncoding).To(Equal([]string{"chunked"}), "send_partial_response(0), rgw_rest_s3.cc:3566-3588")
		Expect(resp.Header.Get("Content-Type")).To(Equal("application/xml"))
		b, _ := io.ReadAll(resp.Body)
		Expect(string(b)).To(MatchRegexp(`^<\?xml version="1\.0" encoding="UTF-8"\?><CopyObjectResult xmlns="http://s3\.amazonaws\.com/doc/2006-03-01/"><LastModified>[0-9T:.Z-]+</LastModified><ETag>&quot;[0-9a-f]{32}&quot;</ETag></CopyObjectResult>$`), "dump_format escapes the quotes, rgw_rest_s3.cc:3598")
		Expect(string(b)).NotTo(ContainSubstring("<Progress>"))
	})
})

var _ = Describe("deleteResultElement", func() {
	DescribeTable("renders send_partial_response's elements",
		func(res op.DeleteResult, quiet bool, want string) {
			Expect(s3.DeleteResultElementForTest(res, quiet)).To(Equal(want))
		},
		Entry("a plain delete", op.DeleteResult{Key: meta.ObjKey{Name: "k"}}, false, "<Deleted><Key>k</Key></Deleted>"),
		Entry("a versioned delete without a versionId that created a marker",
			op.DeleteResult{Key: meta.ObjKey{Name: "key"}, DeleteMarker: true, MarkerVersionID: "mv1"}, false,
			"<Deleted><Key>key</Key><DeleteMarker>true</DeleteMarker><DeleteMarkerVersionId>mv1</DeleteMarkerVersionId></Deleted>"),
		Entry("a delete of one version names it",
			op.DeleteResult{Key: meta.ObjKey{Name: "key", Instance: "v7"}}, false,
			"<Deleted><Key>key</Key><VersionId>v7</VersionId></Deleted>"),
		Entry("quiet hides a success", op.DeleteResult{Key: meta.ObjKey{Name: "k"}}, true, ""),
		Entry("quiet still reports a failure, with its empty VersionId",
			op.DeleteResult{Key: meta.ObjKey{Name: "k"}, Err: op.ErrAccessDenied}, true,
			"<Error><Key>k</Key><VersionId></VersionId><Code>AccessDenied</Code><Message>AccessDenied</Message></Error>"),
		Entry("names are escaped as xml_stream_escaper escapes them",
			op.DeleteResult{Key: meta.ObjKey{Name: "a&b<c>\"d'e\x01"}}, false,
			"<Deleted><Key>a&amp;b&lt;c&gt;&quot;d&apos;e&#x01;</Key></Deleted>"),
	)
})
```

`blockingDeleteStore(inner, key, release)` wraps an `op.Env` store so `DeleteObject` of `key` waits on `release`; `memstoreWith(bucket, keys...)` seeds alice's bucket; `newHandlerWith(store, conf)` is G's `newHandler` with an authenticator for alice and that configuration; `doSigned(srv, method, path, body, headerPairs...)` sends a request the authenticator accepts and returns the `*http.Response` unread. The reader goroutine sees only what the server flushed, so the first `Eventually` passes only when `a`'s element went out while `b`'s delete was still blocked.

- [ ] **Step 3: Run to fail, implement**

`requestattrs.go`:

```go
package s3

// metaPrefixes are req_info::init_meta_info's, in its order (rgw_common.cc:413-420).
var metaPrefixes = []string{"x-amz-", "x-goog-", "x-dho-", "x-rgw-", "x-object-", "x-container-", "x-account-"}

var metaBlocklist = map[string]bool{ // rgw_get_request_metadata, rgw_op.h:2176-2181
	"x-amz-server-side-encryption-customer-algorithm": true,
	"x-amz-server-side-encryption-customer-key":       true,
	"x-amz-server-side-encryption-customer-key-md5":   true,
	"x-amz-storage-class":                             true,
}

var genericAttrs = []struct{ header, attr string }{ // rgw_rest.cc:122-129
	{"Content-Type", meta.AttrContentType}, {"Content-Language", meta.AttrContentLang}, {"Expires", meta.AttrExpires},
	{"Cache-Control", meta.AttrCacheControl}, {"Content-Disposition", meta.AttrContentDisp}, {"Content-Encoding", meta.AttrContentEnc},
	{"X-Robots-Tag", meta.AttrXRobotsTag},
}

func formatXattr(v string) string {
	if utf8.ValidString(v) && !strings.ContainsFunc(v, unicode.IsControl) {
		return v
	}
	var b strings.Builder
	b.WriteString("=?UTF-8?Q?")
	for i := 0; i < len(v); i++ {
		c := v[i]
		if c&0x80 != 0 || c == '=' || c < 0x20 || c == 0x7f { // mime_encode_as_qp: high bit, '=', is_control_character
			fmt.Fprintf(&b, "=%02X", c)
		} else {
			b.WriteByte(c)
		}
	}
	b.WriteString("?=")
	return b.String()
}

func requestAttrs(r *op.Request) (map[string][]byte, error) {
	attrs := map[string][]byte{}
	for _, g := range genericAttrs {
		if v := r.Header.Get(g.header); v != "" || r.Header.Values(g.header) != nil {
			attrs[g.attr] = append([]byte(v), 0)
		}
	}
	xmeta := map[string]string{} // init_meta_info's x_meta_map
	add := func(name, val string) {
		if old, ok := xmeta[name]; ok {
			xmeta[name] = strings.TrimRight(old, " \t\r\n") + "," + val
		} else {
			xmeta[name] = val
		}
	}
	for name, vals := range r.Header {
		low := strings.ToLower(name)
		for _, p := range metaPrefixes {
			if rest, ok := strings.CutPrefix(low, p); ok {
				for _, v := range vals {
					add("x-amz-"+rest, v)
				}
				break
			}
		}
	}
	for name, vals := range r.Query { // map_qs_metadata
		if low := strings.ToLower(name); strings.HasPrefix(low, "x-amz-meta-") {
			for _, v := range vals {
				add(low, v)
			}
		}
	}
	conf := r.Env.Conf
	maxName, _ := conf.Size("rgw_max_attr_name_len")
	maxSize, _ := conf.Size("rgw_max_attr_size")
	maxNum, _ := conf.Uint64("rgw_max_attrs_num_in_req")
	var count uint64
	for _, name := range slices.Sorted(maps.Keys(xmeta)) {
		if metaBlocklist[name] {
			continue
		}
		v := formatXattr(xmeta[name])
		attr := meta.AttrPrefix + name
		if maxName != 0 && uint64(len(attr)) > maxName {
			return nil, op.ErrMetadataNameTooLong
		}
		if maxSize != 0 && uint64(len(v)) > maxSize {
			return nil, op.ErrUnknown // -EFBIG has no S3 row
		}
		count++
		if maxNum != 0 && count > maxNum {
			return nil, op.ErrUnknown // -E2BIG has no S3 row
		}
		attrs[attr] = append([]byte(v), 0)
	}
	return attrs, nil
}
```

(`Content-Type`'s env entry is `CONTENT_TYPE`, present whenever the header is; `populate_with_generic_attrs` writes it even when empty — hence the `Values != nil` test.) The handlers follow the semantics above; `defaultACL` follows M's accessor pattern; `putObject` in outline:

```go
func putObject(ctx context.Context, w http.ResponseWriter, r *op.Request) error {
	if r.Query.Has("uploadId") || r.Header.Get("X-Amz-Copy-Source") != "" {
		return op.ErrNotImplemented // UploadPart and UploadPartCopy are not implemented yet
	}
	if r.Query.Has("append") {
		return op.ErrNotImplemented
	}
	if r.ContentLength < 0 && !slices.Contains(r.Header.Values("Transfer-Encoding"), "chunked") {
		return op.ErrMissingContentLength // -ERR_LENGTH_REQUIRED, rgw_rest_s3.cc:2599-2604
	}
	o := &op.PutObject{Body: r.Body, Size: r.ContentLength, StorageClass: r.Header.Get("X-Amz-Storage-Class"), CannedACL: r.Header.Get("X-Amz-Acl"),
		IfMatch: r.Header.Get("If-Match"), IfNoneMatch: r.Header.Get("If-None-Match")}
	if err := objectLockHeaders(r); err != nil {
		return err
	}
	if v := r.Header.Get("Content-MD5"); v != "" {
		sum, err := base64.StdEncoding.DecodeString(v)
		if err != nil || len(sum) != md5.Size {
			return op.ErrInvalidDigest // :4185-4198
		}
		o.ContentMD5 = sum
	}
	if v := r.Header.Get("X-Amz-Tagging"); v != "" {
		set, err := tags.ParseHeader(v, tags.MaxObjectTags)
		if err != nil {
			return op.ErrInvalidArgument // :2626-2637: s3 returns only EINVAL for PUT
		}
		o.Tags = &set
	}
	var err error
	if o.Attrs, err = requestAttrs(r); err != nil {
		return err
	}
	if o.ACL, err = defaultACL(ctx, r); err != nil { // BuildDefaultACL through the evaluator
		return err
	}
	if err := op.Run(ctx, o, r); err != nil {
		return err
	}
	h := w.Header()
	SetCommonHeaders(w, r)
	h.Set("ETag", `"`+o.ETag+`"`)
	SetContentLength(h, 0) // send_response's dump_content_length(s, 0), rgw_rest_s3.cc:2747
	if o.VersionID != "" {
		h.Set("x-amz-version-id", o.VersionID)
	}
	addRequestCharged(h, r)
	w.WriteHeader(successStatus(r)) // rgw_s3_success_create_obj_status: 0 → 200, 201, 204
	return nil
}
```

`objectLockHeaders` implements the mode/date/legal-hold validation of `:2640-2676` and answers `ErrInvalidRequest` without bucket object lock, `ErrNotImplemented` with it; `deleteObject`, `copyObject`, `putObjectACL`, `putObjectTags`, `deleteObjectTags` follow the semantics above; `parseCopySource` transcribes [`:5329-5374`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L5329-L5374) and splits `tenant:bucket` on the first `:` (as G's `ParseRequest` does for paths), defaulting to the requester's tenant.

`deleteObjects` wires the op's four hooks to the response:

```go
func deleteObjects(ctx context.Context, w http.ResponseWriter, r *op.Request) error {
	rel := r.Env.Zone.Release()
	var (
		quiet bool
		sent  bool // Status wrote the whole response
		cx    *chunkedXML
	)
	o := &op.DeleteObjects{
		Parse: func(body []byte) ([]op.DeleteObjectsEntry, error) {
			entries, q, err := parseDelete(body, rel)
			quiet = q
			return entries, err
		},
		// send_status alone (rgw_rest_s3.cc:4247-4255): none of end_header's
		// headers and no document; net/http adds Content-Length: 0 and Date,
		// as radosgw's frontend completes such a response.
		Status: func(err error) {
			sent = true
			w.WriteHeader(op.AsError(err).Status)
		},
		// begin_response (:4257-4271): end_header's own flush carries the
		// declaration dump_start put before it; the open tag has its own.
		Begin: func() error {
			var err error
			if cx, err = startChunkedXML(w, r); err != nil {
				return err
			}
			cx.add(xmlHeader)
			if err := cx.flush(); err != nil {
				return err
			}
			cx.add(`<DeleteResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`)
			return cx.flush()
		},
		// send_partial_response: each key's element leaves as the key completes (:4273-4329)
		Result: func(res op.DeleteResult) {
			cx.add(deleteResultElement(res, quiet))
			if err := cx.flush(); err != nil {
				slog.DebugContext(ctx, "delete result not delivered", slog.Any("error", err))
			}
		},
	}
	err := op.Run(ctx, o, r)
	switch {
	case sent:
		return nil
	case cx == nil:
		return err // the bucket or the body: an ordinary error response
	case err != nil:
		slog.WarnContext(ctx, "multi-object delete ended after the response started", slog.Any("error", err))
		return nil
	}
	cx.add("</DeleteResult>") // end_response (:4331-4336)
	if err := cx.flush(); err != nil {
		slog.DebugContext(ctx, "delete result not delivered", slog.Any("error", err))
	}
	return nil
}
```

- [ ] **Step 4: Run, lint, commit**

Run: `go test -tags=ceph_preview -race ./internal/s3/...`; expected PASS. `make check`, then `feat(s3): the object write handlers with radosgw's headers and documents`. Draft PR `feat(s3): object write handlers (unit W task 11)`; the description states that DeleteObjects and CopyObject stream as radosgw does and that a DeleteObjects failing before its first key answers with radosgw's bare status line.

---
### Task 12: Integration: the reverse oracle with `radosgw-admin object stat`, `bucket check`, `gc list` and a radosgw read-back `[cluster]`

**Files:**
- Create: `test/integration/write_test.go` (`//go:build integration`, `Label("integration")`), `test/integration/write_helpers_test.go`
- Modify: `docs/ceph-upstream-bugs.md` (the **rgw-go** line of "cls_rgw complete_op writes a stale epoch back when it cancels" says rgw-go sends radosgw's `-1:0` and meets the defect as radosgw does until a release carries the class fix, and gains the sentence naming where rgw-go sends it (W-D8); new **Quirk** entries: `set_obj_attrs` adds one nanosecond to the mtime; `rgw_put_obj_max_window_size` is defined and never read; Squid's PUT compares a raw `If-Match` against the etag, so a quoted header fails; Squid's precondition PUT of a missing key answers 412, not ENOENT, for `If-None-Match: <etag>`; every `x-amz-*` request header is stored as an xattr), `docs/cgo-limitations.md` (the `rados_ioctx_pool_required_alignment2` binding exists on the fork, so Task 1's `RequiredAlignment` closes no gap; the abandoned-write note: a tail write outliving a client disconnect holds its pinned buffer until the completion fires, which is why `tailWriter` runs under the driver's lifetime and drops the buffer after a context error)

**Interfaces:**
- Consumes: `hack/rooket` (`make cluster-up RELEASE=…`, `make populate`, `make integration`), M's `cephtest.RadosgwAdmin(ctx, conf, args...)`, `cephtest.Conf()`, `cephtest.ReadManifest`, `test/gate.Manifest` (`Pools`, `Users`, `Buckets`), M's `test/integration` suite and its `driver.Open` fixture, `hack/rooket/lib.sh`'s `rgw_endpoint` (T), an aws-sdk-go-v2 S3 client against the coexisting radosgw (M Task 13 added it) and aws-sdk-go-v2's `v4.Signer` for item 12's raw signed requests, `radosclient.Pool.Read` with `GetXattrs` (to read raw xattrs of heads and tails), `refcount.DecodeRefcount`, `rgw.DecodeGCObjInfo`.
- Produces: the oracle specs below and `hack/rooket/README.md`'s one line on running them.

The specs run against `make cluster-up RELEASE=squid && make populate RELEASE=squid`, then again with `RELEASE=tentacle`, on the bucket `plain` and a fresh bucket `rgwgo-w-<rand>` the suite creates through M's `CreateBucket` and removes in `DeferCleanup`. Every object the driver writes is read back through the radosgw the cluster runs (`aws s3api get-object`), and every metadata claim is checked with `radosgw-admin`, never with an assumption.

- [ ] **Step 1: PUT oracle**

1. **Small, head-full, tailed, empty.** `PutObject` of 1 KiB, exactly 4 MiB, 10 MiB and 0 bytes under `x-amz-meta-k: v` and `Content-Type: text/plain` (attrs built through `s3.requestAttrs` on a synthetic request, so the x-amz set matches a real PUT's). For each: `radosgw-admin object stat --bucket <b> --object <k>` decodes; compare `size`, `etag`, `manifest.obj_size`, `head_size`, `max_head_size`, `rules`, `tail_placement`, and the `prefix` shape (`^\.[A-Za-z0-9_-]{31}_$`) with rgw-go's own `StatObject`; the `attrs` key set must equal what a radosgw PUT of the same request leaves (write the same object through the radosgw with `aws s3api put-object --metadata k=v --content-type text/plain` under key `<k>-radosgw` and compare the two `attrs` key sets after dropping `user.rgw.pg_ver` values, `user.rgw.idtag`/`tail_tag` values and the `x-amz-date`/`x-amz-content-sha256` values, which differ per request); the head's raw xattrs through the seam show `user.rgw.etag` without a NUL and `user.rgw.idtag` with one; `aws s3api get-object` through the radosgw returns identical bytes, the ETag and `x-amz-meta-k`; `radosgw-admin bucket check --bucket <b>` prints an empty `invalid_multipart_entries` array and a `check_result` whose `existing_header` equals its `calculated_header` (the only sections it prints: [rgw_bucket.cc:316-321](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_bucket.cc#L316-L321) and [:379](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_bucket.cc#L379) at v19.2.6, [:317-321](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_bucket.cc#L317-L321) and [:507](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_bucket.cc#L507) at v20.2.4); `radosgw-admin bucket stats` shows `num_objects` and `size_actual` matching.
2. **Overwrite.** A 10 MiB object overwritten by 1 KiB: `gc list --include-all` shows one entry whose `tag` is the old `tail_tag` (NUL rendered as `\u0000` in the JSON) and whose chain names the two old tails; `object stat` shows the new manifest; `rados -p <data pool> ls` (toolbox) still lists the old tails until `radosgw-admin gc process --include-all` (the radosgw's GC) removes them — asserting that the coexisting gateway's GC consumes rgw-go's entries.
3. **Storage class.** With the populate script's placement (T's Task 3 adds classes on other pools when it lands; until then a class on the same pool), `PutObject` with `StorageClass: "<class>"`: `object stat` shows `storage_class` in the attrs and `manifest.tail_placement.placement_rule` = `default-placement/<class>`; `aws s3api head-object` shows `StorageClass`.
4. **Race.** Two goroutines `PutObject` the same key 20 times each with distinct bodies; afterwards `bucket check` is clean, `object stat` shows one of the two bodies' etags, `bucket list` has exactly one entry for the key with that etag, and no tail of either writer remains beyond the surviving object's (`rados ls` filtered by the two prefixes, after `gc process --include-all`). This is Review Focus 1 on a real cluster.
5. **Conditional.** `If-None-Match: *` on an existing key → `ErrPreconditionFailed`; `If-Match: *` on a missing key → `ErrNoSuchKey` on both releases — and the same two requests through the radosgw (`aws s3api put-object --if-none-match '*'`) answer the same statuses on Tentacle, which is the parity claim W-D3 rests on; a v19.2.6 radosgw answers the second with 412, a difference `docs/exclusions.md` records ("Write conditions are Tentacle's on Squid too").

- [ ] **Step 2: DELETE and GC oracle**

6. **Delete.** `DeleteObject` of a 10 MiB object: `bucket list` no longer has it; `gc list --include-all` holds its chain under the `tail_tag`; the head is gone (`rados stat` fails); `bucket check` clean; `radosgw-admin gc process --include-all` frees the tails.
7. **rgw-go's GC frees radosgw's garbage.** `aws s3api delete-object` (through the radosgw) of the populate's `large.bin` copy `large-copy.bin` (put through the radosgw first): `gc list` shows radosgw's entry; run rgw-go's worker once with `expiredOnly=false` (`driver` export `GCProcessForTest(ctx, false)` under the integration tag); the tails are gone and `gc list` is empty; `radosgw-admin gc list` on a shard rgw-go transitioned (a fresh cluster has queue-era shards; assert the version through `version.Read`) still works.
8. **Reshard mid-write** (the path Task 4 could not fake). Start 200 sequential `PutObject`s of 1 KiB in a goroutine; after 20 run `radosgw-admin bucket reshard --bucket <b> --num-shards 23 --yes-i-really-mean-it` (a synchronous reshard); wait for the writer; `bucket check --fix`-less `bucket check` is clean, `bucket stats` `num_objects` is 200, `bucket list` has 200 keys, and the driver's log shows at least one "bucket index shard is resharding; blocking" line (capture `slog` into a buffer). The completion manager's retry is what makes the count exact.

- [ ] **Step 3: COPY and subresource oracle**

9. **Copy.** `CopyObject` of a 10 MiB source to `dst` in the same bucket: `object stat dst` shows the source's `prefix` in its manifest and the source's etag; `rados getxattr <tail> refcount` (toolbox, base64) decodes to refs `{"": true, "<tag>\x00": true}`; `aws s3api get-object dst` returns the bytes; `DeleteObject src` then `gc process --include-all`: the tails survive (one ref left) and `get-object dst` still works; `DeleteObject dst`, `gc process`: tails gone.
10. **Copy across classes** streams: the destination's tails are its own (different prefix) in the class's pool. Then the compressed source (W-D13): copy T's corpus object `comp-zlib.bin` (in its compressed class) into `STANDARD` twice, to `cc-rgwgo` through rgw-go's `CopyObject` and to `cc-radosgw` through the radosgw (`aws s3api copy-object --copy-source <b>/comp-zlib.bin --storage-class STANDARD`), and compare `radosgw-admin object stat` of the two. On Squid they match: the attrs key sets are equal (after dropping the per-request values of `user.rgw.idtag`, `user.rgw.tail_tag` and `user.rgw.pg_ver`), `user.rgw.compression` decodes on both to the source's info, both manifests' `obj_size` equal the source's stored size, and `bucket list` shows the same `size` and `accounted_size` for both. On Tentacle only rgw-go's copy keeps `user.rgw.compression`, the difference W-D13 records: assert rgw-go's copy as on Squid, and record radosgw's `size` and `accounted_size` for `cc-radosgw` in the PR description; code reading predicts the source's stored size as its accounted size (v20.2.4 [rgw_rados.cc:5346](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L5346), [:5378](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L5378)), and if the run shows it, that defect gets a new `docs/ceph-upstream-bugs.md` entry with the run as evidence. Both copies read back through the radosgw byte-exact.
11. **SetObjectAttrs.** `PutObjectACL`-style `SetObjectAttrs(st, {acl: public-read policy}, nil)`: `object stat` shows a new `idtag` and the mtime advanced by exactly one nanosecond over the previous `object stat`; `aws s3api get-object-acl` through the radosgw shows the AllUsers grant; `bucket list` shows the owner.
12. **DeleteObjects' early failure, as the radosgw answers it.** Send the radosgw three SigV4-signed `POST /<b>?delete` requests over `net/http` (signed with aws-sdk-go-v2's `v4.Signer` under alice's keys, `x-amz-content-sha256` the body's hash): 1001 `<Object>` entries (`rgw_delete_multi_obj_max_num` is 1000), a body that does not parse (`<Delete><Object>`), and an empty body with `Content-Length: 0`. Code reading predicts the same answer on both releases for all three: status 400, `Content-Length: 0`, an empty body, and neither `x-amz-request-id` nor `Server` nor `Content-Type`, because `end_header` never runs (W Task 11's `multi_object_delete` bullet). Assert the status, the empty body and the zero length, and record the full header set of each response in the PR description. The absence of `x-amz-request-id` and `Server` is the part only this run settles: if the radosgw sends either, W Task 11's `Status` sets that header as radosgw does and its streaming spec asserts it. `Date` and `Connection` are each frontend's own (G Task 5).

- [ ] **Step 4: Both releases, then the registries**

```sh
make cluster-up RELEASE=squid && make populate RELEASE=squid && make integration RELEASE=squid
make cluster-up RELEASE=tentacle && make populate RELEASE=tentacle && make integration RELEASE=tentacle
```

Then the registry edits named in Files. The complete_op entry ("cls_rgw complete_op writes a stale epoch back when it cancels") is not created or rewritten here: its **rgw-go** line reads "rgw-go sends radosgw's -1:0 cancel version and meets the defect as radosgw does until a release carries the class fix (ceph/ceph#72097)." (main carries that wording before execution; set it only if it does not), and this task appends: "The cancel is `indexOp.cancel` (`internal/driver/indexop.go`), which the head write, the delete and `set_attrs` call on failure; `indexop_test.go` pins the request and the entry version it leaves." `docs/ceph-upstream-bugs.md`'s new quirk entries carry the evidence lines: [`rgw_sal_rados.cc:2384-2385`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L2384-L2385) (the nanosecond); [`rgw.yaml.in:114-120`](https://github.com/ceph/ceph/blob/v19.2.6/src/common/options/rgw.yaml.in#L114-L120) and `git grep rgw_put_obj_max_window_size src/rgw` empty at both tags (the unused option); [`rgw_rados.cc:6511-6514`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6511-L6514) and v20.2.4 [`:7296-7302`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7296-L7302) (raw vs unquoted `If-Match`; the Squid outcome confirmed in Step 1.5); [`rgw_rados.cc:6497-6500`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6497-L6500), [`:3407-3415`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L3407-L3415) (the ENOENT); [`rgw_common.cc:421-464`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L421-L464), [`rgw_op.h:2171-2229`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.h#L2171-L2229) and the Step 1.1 key-set comparison (the x-amz-* storage). Each states rgw-go's handling: reproduce. This task writes nothing to `docs/exclusions.md`: each of W's differences is recorded by the task that introduces it (Tasks 4, 6 and 8).

- [ ] **Step 5: Commit**

`test(integration): prove the write path against radosgw-admin and a coexisting radosgw` and `docs: record the write path's quirks in the registries`. Draft PR `test: write-path oracle on Squid and Tentacle (unit W task 12)`; its description lists what each release proved, records the header sets Step 3.12 captured from the radosgw's early DeleteObjects failures, and notes that Step 2.8's reshard is the only cluster-only behaviour of the unit.

---

### Task 13: Gate: s3-tests PUT, DELETE, COPY, multi-object-delete and `conditional_write` groups; the write microbench `[cluster]`

**Files:**
- Create: `test/s3tests/w-groups.txt` (the test-name patterns W is judged by), `internal/driver/shape_test.go` (the op-composition parity spec)
- Modify: `hack/rooket/README.md` (how to run the W gate), `test/bench/seam/shapes.go` (T Task 1: nothing new; the spec reads its `headwrite4k`/`indexrtt` compositions)

**Interfaces:**
- Consumes: T's Tasks 4, 5, 11, 12 (`make s3tests`, `hack/parity`, `test/s3tests/baseline/{squid,tentacle}.json`, `make rgw-go-up`), T's Task 1 `seam.Shapes()` and Task 2 `make bench-seam`; Task 12's cluster.
- Produces: `test/s3tests/w-groups.txt`:

```
test_object_write_*
test_object_read_not_exist
test_object_copy_*
test_object_set_get_metadata_*
test_object_set_get_unicode_metadata
test_object_set_get_non_utf8_metadata
test_object_metadata_replaced_on_put
test_object_write_file
test_object_delete_key_bucket_gone
test_multi_object_delete*
test_multi_objectv2_delete*
test_object_acl_*
test_put_obj_tagging*
test_delete_tagging*
test_object_raw_put_*
test_object_write_check_etag
test_object_write_cache_control
test_object_write_expires
test_put_object_ifmatch_*
test_put_object_ifnonmatch_*
test_put_object_if_match
test_multipart_put_object_if_match
test_delete_object_if_match
test_delete_objects_if_match
```

The last six lines are the conditional-write block (W-D3, W-D12): the eight unmarked `test_put_object_ifmatch_*` and `test_put_object_ifnonmatch_*` tests, and the eight `conditional_write` tests the parity set runs (`test_put_object_if_match`, `test_multipart_put_object_if_match`, and `test_delete_object_if_match` and `test_delete_objects_if_match` with their `_last_modified_time` and `_size` variants, which the patterns match as substrings). The marker's other 17 tests enable bucket versioning, so T's phase 2 deselect list removes them on both gateways, as it does the three `test_object_copy_*` tests that copy from a version; none of them reaches this filter.

- [ ] **Step 1: The op-composition parity spec**

`internal/driver/shape_test.go` (unit spec, no cluster): the steps `PutObject` composes for a 4 KiB object on fakerados (Task 5's fixture) equal, step type by step type, the steps T's `headwrite4k` shape issues (`seam.ByName([]string{"headwrite4k"})` run against a `radosclientfakes.FakePool` recording ops), and the prepare/complete pair equals `indexrtt`'s — so the microbenchmark measures the composition the gateway actually sends. A difference is a finding for T or W, resolved before the gate runs.

- [ ] **Step 2: s3-tests parity**

```sh
make cluster-up RELEASE=squid && make populate RELEASE=squid
make rgw-go-up RELEASE=squid
make s3tests-parity RELEASE=squid
go run ./hack/parity diff -baseline test/s3tests/baseline/squid.json -candidate hack/rooket/out/squid/s3tests-rgw-go.json | grep -E -f test/s3tests/w-groups.txt
```

`make rgw-go-up` is T Task 11's (rgw-go on the rooket cluster); `make s3tests-parity` is T Task 12's (`RUN=parity`, records `hack/rooket/out/squid/s3tests-rgw-go.json` with T's meta and diffs; it exits 1 while other units' differences remain). (T's `s3tests` target takes `RUN=`, not `OUT=`, and writes under `hack/s3tests/out/`; `parity record` requires `-out FILE`; `parity diff` has no `-only` — the grep over the pattern file is the filter, P Task 11's form.) Expected: the grep prints nothing on Squid and on Tentacle. Every difference is triaged: a radosgw quirk rgw-go mis-transcribed is fixed in the owning task; a test whose baseline outcome differs between releases (the `conditional_write` group is expected to be the one, since Squid's raw `If-Match` compare fails quoted etags) is confirmed to differ the same way for rgw-go.

- [ ] **Step 3: The write microbenchmark**

```sh
make bench-seam RELEASE=squid SHAPES=write4k,write4m,headwrite4k,indexrtt
```

on the same host as the phase-0 baseline; the numbers go into the PR description beside T's `docs/benchmarks/` entry (T owns that directory). The gate is parity, not a threshold: the composition spec above is what ties the seam numbers to the gateway.

- [ ] **Step 4: Commit**

`test(s3tests): name the write-path groups the W gate is judged by` and the `hack/parity`/filter change. Draft PR `test: write-path s3-tests gate (unit W task 13)`; the description carries the two parity results and the microbenchmark table.

---

## Self-review

- **Spec coverage.** §6's PUT rows: two round trips within the head (Task 5: prepare, guarded head with data, manifest and attrs; the complete not awaited through Task 4's manager) and tails streamed under the 16 MiB window before them (Task 5's `tailWriter`, W-D2 fixing the window); "a failed head write cancels the prepare" (Task 5's `cancelWrite`); the ECANCELED sentence corrected by W-D1; copy sharing tails through refcount under the NUL-terminated tag, rewriting only the head, streaming only where placement, storage class or head geometry force it (Task 8); the completion manager with retries and the listing reconciliation left to M (Task 4). §7's workers: the GC processor on its period under `gc_process` (Task 9), the completion retry worker (Task 4); fire-and-forget under the driver's lifetime (Task 4's manager context, Task 5's tail writes); every goroutine has a stop (each worker returns on ctx; `tailWriter.drain`, `gcIO.drain`). §8: every class request at the cluster release (`s.release` on every `rgw.`/`gc.`/`refcount.`/`version.` call), persistent types encoded at the release (`encodeAt`), radosgw's placement of heads, tails and GC entries (Tasks 3, 5, 9), the coexistence obligations (the reshard guard on every prepare and complete, both GC formats, refcount tags with NUL, the `gc_process` lock name). §9's items for W: Put, Delete, Copy, DeleteObjects (Tasks 5-8, 10, 11), GC enqueue and worker (Tasks 5, 9), the reshard protocol's write half (Task 4), the object ACL and tagging subresources (Tasks 7, 10, 11); the gate (Tasks 12, 13). §2: the round trips per request are the table's, and Task 13's composition spec ties them to T's benchmark. Unit W's goal in the index (`00-index.md`, "The nine units") maps clause by clause: PUT within and beyond the head (5), the write tag (W-D6) and the ECANCELED handling (W-D1), busy-resharding re-read and retry (4), DELETE and DeleteObjects with both GC formats and `pool_full_try` (5, 6, 10), Copy (8), the completion manager (4), the GC worker (9), object ACL and tagging (7, 10, 11). The brief's baked-in handling: the guard's -2300 on every prepare, complete and retry (4); the zero GC shard count floored and never divided by unguarded (4's `readWriteOptions`, 5's `shardsMod` behind `gcMaxObjs >= 1`); the cancel version matched to radosgw's `-1:0` (W-D8) and flagged (Review Focus 1); `OpFlagFullTry` on deletes, refcount puts and the copy rollback (1, 5, 6, 8, 9); `conditional_write` isolated (W-D3, Task 10's pass-throughs, Task 13's two pattern lines).
- **Placeholder scan.** No TBD/TODO; every "write the elided bodies in full" names the exact behaviour and expected values in its comment; Task 12's step 1.1 leaves the `bucket check` field names to be recorded from the release's output rather than guessed, and says so.
- **Type consistency.** `indexOp{prepare(ctx, mod), complete(ver, meta), completeDel(ver, mtime), cancel()}` is what Tasks 5-8 call; `s.newIndexOp(rec, key, tag)` with `""` meaning `randTag`; `headWrite`/`writeMeta`/`headResult` (5) are what 7 and 8 use — Task 7 builds its own op rather than `writeMeta` because `set_attrs` has no reset, no manifest and no preconditions; `enqueueGC(ctx, chain, tag)`, `gcChain(m, rule)`, `rawTag(st, name)`, `rgwBlStr`, `entryOwner`, `checkPreconditions` (5) are what 6 and 7 use; `planPut`/`layout`/`tailWriter`/`putStream` (5, 8); `writeOptions` fields (4) are what 5, 6, 9 read (`putWindow`, `stripeSize`, `chunkSize`, `maxPutSize`, `gcMaxObjs`, `gcObjMinWait`, `gcProcessorMaxTime`, `gcProcessorPeriod`, `gcMaxConcurrentIO`, `gcMaxTrimChunk`, `gcMaxQueueSize`, `gcMaxDeferred`, `gcThreads`, `multiObjDelMaxAIO`, `copyConcurrentIO`); `writer` fields (`zoneID`, `shortZoneID`, `logData`, `gcShards`, `reshardWait`, `completions`, `rand`, `alignments`, `bufs`); the contract additions `PutParams.Tag`/`ContentMD5` (5), `DeleteParams.UnmodifiedSince`/`IfMatchSize`/`IfMatchLastModified` (6), `CopyParams.Tag` (8), `StatsStore.AdjustStats` (W-D7, M implements), `rgw.GetBucketResharding` (4), `meta.Manifest.SetHead` (8), `op.ErrMetadataNameTooLong` and `CheckReadConditions` (10); the ops' field names in Task 10 are what Task 11's handlers fill (`Body`, `Size`, `Attrs`, `ACL`, `CannedACL`, `Tags`, `StorageClass`, `ContentMD5`, `IfMatch`, `IfNoneMatch`; `UnmodifiedSince`, `IfMatchSize`, `IfMatchLastModified`; `Parse` (returning `DeleteObjectsEntry`s), `Status`, `Begin`, `Result`, which Task 11's `parseDelete` and `deleteObjects` supply; `SrcTenant`, `SrcBucket`, `SrcKey`, `Replace`, `CheckStorageClass`, the four copy conditions; `Build`; `Set`). fakerados growth is one surface: `Entry`, `Header`, `GCEntries`, `BeforeWrite`, `FailNextWrite`, `ResetCounters`, `PoolID`, `SetAlignment`, `SetClock`/`AdvanceClock`, `Locks`, `LockExclusiveForTest`, write records' `Flags()` and `Mtime()`, emulators `RGWClass` (grown), `GCQueueClass`, `RefcountClass`. `rgw.MtimeLE`/`rgw.MtimeEQ` are phase 0's `MtimeCheck` constants ([`const.go:99-103`](https://github.com/jhoblitt/rgw-go/blob/50fc45c4d59953661d5c57c27ca7016bc55edeeb/internal/cls/rgw/const.go#L99-L103)).
- **Review Focus.** 1 → Task 5's race spec and Task 12's step 1.4 (plus W-D8's `-1:0` in Task 4's two cancel specs); 2 → Task 6's vanished-head and stale-entry specs; 3 → Task 4's reshard specs and the completion manager's retry, Task 12's step 2.8; 4 → Task 5's fallback spec (`gc_set_entry` on an omap-era shard) and Task 9's omap-era and transition specs; 5 → Task 8's self-copy, cross-pool, head-only and rollback specs.
- **Cluster tasks.** Tasks 1 and 2 have one integration spec each; Task 12 and Task 13 are the cluster gate; everything else runs on fakerados, `memstore` and the counterfeiter fakes. Behaviour verified only on the cluster, stated in Task 12's PR: a reshard that moves the index generation while writes are in flight.
- **The listing's multipart-part sweep.** `check_disk_state` retires, for a head that exists, every `multipart`-namespace index entry its manifest names through `delete_obj_index` ([rgw_rados.cc:10413-10429](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L10413-L10429) at v19.2.6, [:11337-11353](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L11337-L11353) at v20.2.4); Task 6 Steps 5-7 own it as `deleteObjIndex` (an unprepared `complete_del`: empty tag, version -1:0, hashed on the part's own name) inserted into M Task 8's `checkDiskState` at the comment M leaves. radosgw's own sweep hashes the part's name where the part writer hashed the head's, so on a multi-shard bucket it usually hits a shard without the entry; W reproduces the bytes and reports the defect for the registry rather than fixing it.
- **Corrections to the earlier sections, made in place:** Task 3's trivial rule starts at `MaxHeadSize` (the corpus golden proved it); random tags are 31 characters plus radosgw's `"_"` prefix, not 32; Tentacle's prepare carries no zones_trace; the copy response carries no `<Progress>` for a local copy; Review Focus 2's healing case is the remove's ENOENT, not the stat's.
- **Contract findings for the caller** (nothing in G's or Z's frozen contract changed; everything below is additive or a report): (a) `PutParams` lacked the write tag and the Content-MD5 digest (W-D6, W-D9); (b) `DeleteParams` lacked radosgw's `x-amz-delete-if-unmodified-since` and Tentacle's two match headers (W-D12); (c) `StatsStore` had no post-write adjustment (W-D7, M implements); (d) `op.Error` has no code for `ENAMETOOLONG`'s "Metadata name too long", added as a W sentinel; (e) R's driver exposes `readStored`, the raw stored-byte read (R Task 4, driver-internal, same name and signature in both plans), which `copyData` streams so a compressed source's copy keeps its compression info as `copy_obj_data` does (W-D13); G's store interfaces are unchanged; (f) `op.Authorizer` carries `VerifyBucketIn`/`VerifyObjectIn`, so DeleteObjects' per-key check and CopyObject's source check call `VerifyBucketPermissionIn` and the request-copy idiom is gone — it would have evaluated the public-access block and requester-pays on the source bucket where radosgw evaluates them on the destination; (g) M's `shardMod` is a plain modulo (right for the usage log), while GC and the index use `rgw_shards_mod`'s prime reduction over its 32-bit `unsigned` argument — W keeps its own `shardsMod`, which truncates the GC's 64-bit XXH64 as the C++ conversion does; (h) `fakerados` grows a class emulator per unit; the `ClassFunc` signature has no release, so the fake never enforces Tentacle's class-side guard, which the explicit guard step covers.
