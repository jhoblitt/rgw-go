# Phase 2 unit V: versioning

Threat model: a client adversary at the spec's security boundaries this unit touches, the
client-facing parsers (the versioning document, `versionId`, the version markers, `x-amz-mfa`,
DeleteObjects' keys) and MFA delete, which is authorization; honest failures everywhere else,
above all the OLH protocol's racing writers from both gateways, crashes mid-protocol and
reshards. Nothing here defends against a malicious RADOS client.

Source: `research/research-versioning.md` (the report), researched at v19.2.6 and v20.2.4. This
file's citations are retargeted to v19.2.7 (unit F). Section numbers (§2.3 and so on) are the report's. Where
this file departs from the report, the drafting notes says why.

## Scope

Spec §9, phase 2: "Versioning with the OLH protocol, ListObjectVersions, … MFA." In the
report's §1 terms:

- `PUT` and `GET /{bucket}?versioning`: PutBucketVersioning (`Status`, `MfaDelete`) and
  GetBucketVersioning. Both routes exist and answer 501 today.
- `GET /{bucket}?versions`: ListObjectVersions with `key-marker`, `version-id-marker`, `prefix`,
  `delimiter`, `max-keys`, `encoding-type` and `allow-unordered`, plus the `list-type=2&versions`
  form phase 1 renders.
- Object ops on a versioned bucket and any op naming `?versionId=`: PutObject, CopyObject (source
  version and a new destination version), CompleteMultipartUpload, DeleteObject (a delete marker
  or one version), DeleteObjects, GET and HEAD, GetObjectAttributes, the object ACL and tagging
  ops on a version, the `null` version and suspended-bucket writes. UploadPartCopy's source
  `?versionId=` resolves through the versioned read.
- MFA delete: `x-amz-mfa` verified through cls_otp, `MfaDelete` on PutBucketVersioning, and the
  refusals of DeleteObject and DeleteObjects.
- Admin: `DELETE /admin/bucket?object` and the purging bucket and user removals on a bucket whose
  versioning was ever enabled.

Not this unit: object lock and lifecycle (L, C), which consume the seams below; POST object (B),
which writes through V11's seam; checksums and the otp metadata section (WP); the s3-tests
selection port and include list (T); multisite, Swift versioning and the `bi_*` repair tooling
(excluded).

## Decisions

The index's rulings (D-ids) and this unit's own (VD-ids). Pending-owner items are written to
their default.

| Id | Decision | Reason | Tasks |
|---|---|---|---|
| D1 | A conditional write to a versioned bucket (PutObject, CopyObject, CompleteMultipartUpload, a conditional delete marker) commits only if its condition held when its link landed. Each attempt evaluates the condition on the OLH head it reads and guards its init op with `cmpxattr(olh.info, EQ, <bytes read, or empty>)`. While other writers' pending attrs remain it also guards init with `obj_check_attrs_prefix(user.rgw.olh.pending., fail if exists)`, which serializes rgw-go's conditional writers, and backs off up to VD-4's bound; past the bound it proceeds without that check. After its link the writer lists the key's versions and voids the write (its instance unlinked, 412) when another version or marker took an epoch between the `olh.ver` it read and its own, or when the version an If-Match matched is gone. That post-link check carries the guarantee; two racing conditional writers may both answer 412. Unconditional writes keep radosgw's guards exactly. | radosgw's check-then-act lets two `If-None-Match: *` writers both win (§2.9, defect 4). The init guard alone misses a writer that skips it (review F1), and a pending attr no writer will remove, which radosgw's own noncurrent-version transition leaves (review N2), must not refuse conditional writes. The state written is radosgw's. | V4, V5c, V7a, V7b, V8a |
| D2 | GET or HEAD naming a delete marker's own version id answers 405 MethodNotAllowed with `x-amz-delete-marker: true`. | S3's answer; one `bi_get` on that 404 path only (defect 6). | V6, V14a |
| D3 | `versionId` on PutObject, on CopyObject's destination and on CompleteMultipartUpload is ignored. | S3 has no such parameter; removes the registry's null-instance anomaly (defect 9). | V11 |
| D4 | CopyObject sends `x-amz-version-id` and `x-amz-copy-source-version-id`. `x-amz-version-id: null` is sent where the bucket is versioned and the instance empty, never on a never-versioned bucket. | S3's headers (defects 5, 8). | V11, V14a |
| D5 | A versioned write awaits its index ADD completion before its link. Unversioned writes keep the unawaited completion. | rgw-go's completions run unordered (`docs/cgo-limitations.md:320-345`); a link that reaches the shard first corrupts the version (§2.3). The round trip is measured in the gateway comparison before any per-shard queue. | V5a, V7a |
| D6 | The 404 of a GET or HEAD whose current version is a delete marker carries `x-amz-delete-marker: true` on Squid too. | Tentacle feature level, and S3 sends it (defect 7). | V14a |
| D7 | The `list-type=2&versions` document stays as phase 1 renders it. | No S3 client sends it; the registry explains it. | V14b |
| D8 | `docs/differences.md`'s two `bi_*` sentences are amended: `bi_get` is in scope for one version's index entry, on the versioned delete and on D2's 404: one read for a named instance, and up to three for `null` (the plain key, then the null delete marker's key, then the null data key, in the class's `read_key_entry` order). `bi_put`, `bi_list` and the repair paths stay out. rgw-rs is told. | The S3 versioned delete needs it at both floors (§2.4), and a converted key's `null` lies behind its version marker (review F4). | V5a, V8a |
| D9 | No upstream u64-mode cmpxattr request now; the C API's missing mode is a `docs/cgo-limitations.md` entry. | VD-1's guard is correct and compatible. | V4 |
| P2 | (the owner, 2026-10-13) The admin object removal and the purges ignore object lock, as radosgw does, with an exclusion. | Rook's bucket deletion relies on the purge; refusing strands lock buckets. | V8b, V12b |
| P3 | (the owner, 2026-10-13) V5b gets a Fable review after its task review, and V5c, split from it for size and carrying D1's races, shares that review; the other OLH tasks get the standard Opus review. | A wrong OLH step corrupts state both gateways share, and V5b and V5c hold the races. | V5b, V5c |
| VD-1 | `apply_olh_log`'s head op is guarded by `cmpxattr(olh.ver, EQ, <bytes read>)` in string mode, not radosgw's numeric GTE. An apply that fails it re-reads the head and the log and applies again, up to a bound; past the bound ECANCELED is success by the protocol, with the pending attrs left for the next reader, the writer's own included (V5b B2). | The librados C API compares xattrs in string mode only ([librados_c.cc:3667-3683](https://github.com/ceph/ceph/blob/v19.2.7/src/librados/librados_c.cc#L3667-L3683)); a string GTE puts "9" above "10". The state written is radosgw's (§2.9) but in one window: when another writer has applied a link at an epoch two links share, a retry from the fresh `olh.ver` keeps the version the class made current, where radosgw's pass writes the link logged last (`docs/ceph-upstream-bugs.md`, "radosgw's apply_olh_log applies a same-epoch link the class did not make current", which V4 adds). The retry keeps the progress GTE keeps (review F5). | V4, V5b |
| VD-2 | Tentacle's condition evaluation, against the current version followed through the OLH, on both releases, for versioned writes and deletes. | Owner decision 1 and PP-3; v19.2.7 already evaluates this way (ce98cebb815), so only v19.2.6, below the floor, differed (defect 3). | V7a, V8a |
| VD-3 | A conditional write whose link-time evaluation fails answers 412 and removes the instance it wrote, through the unversioned delete on the instance key. | Otherwise the instance entry and head stay as an invisible version holding quota. | V5c, V7a |
| VD-4 | A conditional attempt that meets other writers' pending attrs runs `updateOLH` and backs off with jitter, up to a bound the task names as a constant; past it the attempt proceeds without the pending-attr check (D1). Every attempt, conditional or not, keeps radosgw's 100 tries and EIO. | D1's pending-attr guard waits on every in-flight writer, which radosgw's tight loop would exhaust in milliseconds. A pending attr with no log entry, left by a writer that died between its init and its link or by radosgw's own link at an old epoch (review N2), stays until `rgw_olh_pending_timeout_sec` expires it, so the wait must end on its own. | V5c |
| VD-5 | A link or unlink that definitely failed (a class or guard answer) removes the instance the write made, as VD-3 does. An unknown outcome, such as a timeout, never removes anything. | No orphaned instance entry (Review Focus 2); removing after a link that did land would leave a list entry with no head. | V5b |
| VD-6 | S3 writes and deletes on an object-lock bucket stay 501 until L5b and L4 lift them. | Between this unit and L, a version under retention could be deleted, or written without its default retention. The driver serves every bucket, since lifecycle and the admin paths reach it with their own checks; the S3 ops hold the refusal. | V11, V12a |
| VD-7 | `x-amz-mfa` is verified once per request in PostAuthInit, whether or not the op needs it, and never fails the request itself. | radosgw's order (§2.8): the code is consumed the same way from either gateway. | V13d |
| VD-8 | DeleteObjects groups keys by name and skips the OLH update for all but the last of each name. When that last delete fails or finds its version gone, the skipped updates still run (V17). | radosgw's op count, and the same final state (§2.4); the floors leave the updates pending, which Ceph main fixed. | V8a, V12a, V17 |
| VD-9 | The emulator stays Squid's class (no class-side reshard guard, no reshard log), as today. | The guard is the gateway's on both floors (§2.10). | V3a, V3b |
| VD-10 | The OLH and `bi_get` method names live in V1's own files; `internal/cls/rgw/const.go` is not edited. | `ops_mp.go`'s pattern; no hot file. | V1 |
| VD-11 | A read whose follow runs the update for a pending modification resolves the current version again from the head as the update left it, where radosgw's `follow_olh` answers the version current before the update; V6 records it as an exclusion. | The correct S3 answer; the second read writes nothing. radosgw's stale answer is a 404 NoSuchKey while the previous version is current, when the update replays the deletion of the current version (`docs/ceph-upstream-bugs.md`, "radosgw's follow_olh answers the version that was current before the replay it runs", which V4 adds). | V6 |
| VD-12 | `setOLH` retries init's ENOENT on a head its read found, the OLH head removed between that read and its init, from a fresh read within its 100 attempts, where radosgw's `set_olh` returns it ([rgw_rados.cc:8699-8706](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8699-L8706), [rgw_rados.cc:9445-9452](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9445-L9452)); V5b records it as an exclusion. | The correct S3 answer: the version's own write went right, and a fresh read finds no head and creates it (`docs/ceph-upstream-bugs.md`, "radosgw's versioned write fails when the key's OLH head goes between its read and its init", which V4 adds). | V5b |
| VD-13 | `updateOLH` reads the pending log from the head's `olh.ver`, where radosgw's `update_olh` starts at marker 0 ([rgw_rados.cc:8651](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8651), [rgw_rados.cc:9397](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9397)); V5b records it as an exclusion. | Recorded, not aligned. The entries at or below `olh.ver` are already applied to the head, and the class returns only epochs above the `ver_marker` ([cls_rgw.cc:2057-2066](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L2057-L2066), [cls_rgw.cc:2198-2207](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2198-L2207)), so rgw-go's replays skip a refused removal whose trim failed, where radosgw's remove the version: the safer side. A mixed fleet still meets radosgw's behaviour there, V8c's behaviour 5. | V5b |

## Seams

**Consumed**

- S1: the rgw method table. V3a edits handlers already in `cls_rgw.go`; V3b fills
  `cls_rgw_olh.go`, the empty table S1 creates, with the write flag per method.
- S2: the versioning route-set file and the `op.Env` slot `MFA MFAVerifier`, with the interface
  in `internal/op/mfa.go`. V10 binds its two routes there; V13c implements the interface.

  ```go
  type MFAVerifier interface {
      VerifyMFA(ctx context.Context, user *meta.UserInfo, serial, pin string) (bool, error)
  }
  ```

- WP-16: `internal/cls/otp` with `OTPInfo`, `OTPCheck`, the result constants and
  `OTPObject(uid meta.UserID) string`. WP-17: `OTPClassAt(now func() time.Time) ClassFunc`,
  with `OTPClass()` as `OTPClassAt(time.Now)`, assembled with
  `otpCheckMethods(now func() time.Time) methodTable`, an empty table in `cls_otp_check.go`, and
  per-device `last_checks` and `last_success`.
- Phase 1: `indexOp` (`prepare`, `complete`, `completeDel`, `cancel`, `guardReshard`),
  `blockWhileResharding`, `readHead`, `removeHead`, `guardHead`, `completionManager`,
  `checkDiskState`, `PutBucketInfo` under `RetryRacedBucketWrite`.

**Produced for other units**

| Seam | Task | Consumer |
|---|---|---|
| after a follow, `ObjectState.Key` is the resolved version's key (the version id is `Key.Instance`), plus `ObjectState.DeleteMarker bool`; `StatObject`, `PrefetchObject` and `SetObjectAttrs` follow the OLH when no instance is named | V6 | L3 (retention, legal hold), WP `?layout` (D25), C |
| `DeleteObject(ctx, rec *BucketRecord, key meta.ObjKey, p DeleteParams) (*DeleteResult, error)` with `type DeleteResult struct{ VersionID string; DeleteMarker bool }`; a `versionId`, `null` included, always removes that version and never writes a marker | V8a | L4, C10b |
| `DeleteParams.MarkerOwner meta.Owner` (the requester for S3, the entry's owner for lifecycle), `SkipOLHUpdate bool`, `Expect *ObjectState`. `Expect` guards two removals on the head's write tag (`user.rgw.idtag` as `Expect` read it, absent read as empty) and fails with `op.ErrConcurrentModification`: a named version's unlink and head removal, and, on a suspended bucket's delete without a `versionId`, the null version the null delete marker replaces, checked in the marker's init op on the OLH head. A removal the guard refuses after the unlink re-links the version, so it stays listed, unless a newer null write already owns the null key | V8a (fields), V8c (`Expect`'s behaviour) | L4, C10b |
| `ListObjectsParams.MarkerInstance string`, `ListObjectsResult.NextMarkerInstance string`, `ObjectEntry.VersionedEpoch uint64`, `ObjectEntry.Category uint8`; versions on the ordered and the unordered listing | V9 | C10b, C11 |
| the head write's OLH epoch (0: the class assigns it; nonzero: links at that epoch, non-current when older) | V7a | C11's transition |
| one attrs-finalizing call per head write, given the bucket record and the mtime the write stamps; an identity in this unit | V7a | L5a's default retention |
| `func VersionForWrite(r *Request) string` in `internal/op` | V11 | BC-32 (POST object) |
| PutBucketVersioning's object-lock refusal; `meta.BucketInfo.VersioningEnabled()`, `Versioned()`, `MFAEnabled()` | V10, V2 | L2 (PutObjectLockConfiguration) |
| `Request.MFAVerified bool` (the field V10 adds, set by V13d); `(*Store).VerifyMFA` as `op.MFAVerifier` (V13c) | V10, V13c, V13d | V12a, and any later op that needs MFA |

## Hot files

| File | Tasks | Resolution |
|---|---|---|
| `internal/testutil/fakerados/cls_rgw.go` | S1, V3a; C's emulator | S1 first. V3a alone edits the existing handlers; V3b and C add table files only. |
| `internal/testutil/fakerados/cls_otp*.go` | WP-17, V13b | WP-17 owns `cls_otp.go`; V13b writes only `cls_otp_check.go`. |
| `internal/cls/otp/` | WP-16, WP-19, V13a | WP-16 owns the package and types; WP-19 adds `otp_header`'s encoding to `types.go`; V13a adds `ops_check.go`. |
| `internal/driver/indexop.go` | V5a | V5a alone. |
| `internal/driver/olh_head.go` | V4, V5c | V4 creates it; V5c, after V4 by its own dependency, adds `ExpectTag`'s cmpxattr beside D1's guard. |
| `internal/driver/stat.go` | V5b, V6 | V5b adds the unfollowed head read and leaves `StatObject` and `PrefetchObject` refusing an OLH head; V6, after V5b by its own dependency, lifts that refusal. |
| `internal/driver/headwrite.go`, `put.go` | V7a; L5a, WP-10, WP-8 | V7a before L5a (L5a adds one call at V7a's attrs-finalizing point). WP-10 and WP-8 work in `put.go`'s stream stages, not the head write; the controller merges them apart from V7a. |
| `internal/driver/copy.go`, `mp_complete.go` | V7b; WP-8, WP-9, WP-10 | V7b changes only the instance and the link call; the checksum work lives in `mp_cksum.go`. Merged one after the other. |
| `internal/op/object.go`, `internal/driver/delete.go`, `internal/memstore/object.go` | V8a (the DeleteObject signature); V6, WP-5, C10a | No fixed order (the index's hot-file rule): each adds line-local fields, and whichever lands later rebases onto the others. V8a owns the DeleteObject signature change and keeps every field already there (`AssertEpoch` included); V6 lands before V8a by their own dependency. The task changing the interface regenerates `opfakes` and `driverfakes` and updates memstore. |
| `internal/op/bucket.go` | V9 | V9 alone. |
| `internal/op/request.go` | V10; BC-13, BC-14 | line-local fields; V10 adds `MFAVerified`. |
| `internal/s3/request.go` | V13d | V13d alone. |
| `internal/op/deleteobject.go`, `deleteobjects.go`, `internal/s3/deleteobject.go`, `deleteobjects.go` | V12a, then L4 | L4 after V12a. |
| `internal/s3/putobject.go`, `copyobject.go`, `multipart.go`, `getobject.go`, `getobjattrs.go` | V11, V14a; WP-7, WP-13, L5b, C8 | No fixed order: each adds its headers in its own helper, one call line per site, and the later one rebases. |
| `internal/op/putobject.go`, `copyobject.go`, `completemultipart.go` | V11; WP-6, BC-26 | the controller merges them one after the other, never in parallel on one op. |
| `docs/differences.md`, `docs/ceph-upstream-bugs.md`, `docs/cgo-limitations.md` | most tasks | append-only per PP-5; each PR edits only its own entries. |
| `hack/s3tests/deselect-phase2.txt`, `hack/admin/deselect-phase2.txt`, the ledgers | V10, V15a; L, C, B, WP | each area edits its own lines; recordings land one at a time through the controller. |

## Tasks

### Task V0: a failed class call keeps its output

- **Layer:** seam (`internal/radosclient` and its two implementations). **Files:** in the
  jhoblitt/go-ceph fork, `rados/read_op_exec.go` and its spec; in rgw-go,
  `internal/radosclient/results.go`, `internal/radosclient/goceph/translate.go`,
  `internal/testutil/fakerados/steps.go` and `go.mod` (the go-ceph replace). **Cluster:** no
  (the fork's own CI runs its spec against that CI's cluster).
- **Consumes:** nothing. **Produces:**
  - `func (r *ExecResult) Reply() (out []byte, rval int32, err error)`: the method's output and
    return value whatever the sign; `err` is ErrIncomplete before the op ran. `Bytes()` keeps
    its contract.
  - in the fork, a `ceph_preview` accessor on `ReadOpExecStep` returning the out buffer whatever
    `prval`, ErrOperationIncomplete before completion.
- **Behaviour:**
  1. librados hands an exec's out buffer to its completion whatever the method returned
     (`C_out_buffer::finish` ignores `r`, [librados_c.cc:4291-4323](https://github.com/ceph/ceph/blob/v19.2.7/src/librados/librados_c.cc#L4291-L4323),
     [librados_c.cc:4291-4323](https://github.com/ceph/ceph/blob/v20.2.4/src/librados/librados_c.cc#L4291-L4323)). The fork's step drops it on a negative
     `prval` (`canReadOutput = err == nil`); the new accessor does not. The bytes are copied
     before the C buffer is freed, as `Bytes()` copies them.
  2. goceph's read-exec finisher fills `ExecResult` with the output and the negative rval.
  3. fakerados's `exec` keeps a failing method's output in its result, and `failResults` no
     longer overwrites it; steps after the failing one stay without output.
  4. The case this exists for: `bucket_list` encodes its reply, with `marker` set, before
     answering `RGWBIAdvanceAndRetryError` (-EFBIG) when it is truncated with nothing listed
     ([cls_rgw.cc:680-691](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L680-L691), [cls_rgw.cc:730-741](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L730-L741)).
- **Specs:**
  - in the fork (PR a): a read-op exec of a class method that encodes output and returns a
    negative value yields both through the new accessor, while `Bytes()` still refuses; the
    accessor before completion is ErrOperationIncomplete. The implementer picks a method the fork
    CI's cluster loads and records the choice in the PR; cls_rgw's `bucket_list`
    advance-and-retry is the case this exists for;
  - in rgw-go (PR b): `Reply` before the op, after a success, after a negative rval with output;
    the emulator: a probe class returning output with -EFBIG delivers both;
  - a spec on a disposable cluster's index shard is V15b's, not this task's.
- **Exclusions and limitations:** a `docs/cgo-limitations.md` entry: go-ceph's read-op exec step
  dropped a failed call's output, and the fork's accessor fixes it. V9 retires "A shard that
  answers advance-and-retry fails an ordered listing".
- **Depends on:** nothing. **PR split** (the project CLAUDE.md's go-ceph rule): (a) a PR into
  the jhoblitt/go-ceph fork's `rgw-go` branch that keeps the output when `prval` is negative,
  with its spec there, merged on green fork CI; (b) after (a) merges, the rgw-go PR: the pin bump
  of go.mod's go-ceph `replace` to (a)'s merge commit on `rgw-go`, with the seam change and the
  emulator change. **Brief:** about 100 lines.

### Task V1: cls/rgw OLH methods, `bi_get` and the index key encoders

- **Layer:** cls (`internal/cls/rgw`). **Files:** create `ops_olh.go`, `types_olh.go`,
  `ops_bi.go`, `indexkey.go` and their specs and goldens. `const.go` is not edited (VD-10).
  **Cluster:** no.
- **Consumes:** phase 1's `DirEntry`, `DirEntryMeta`, `ObjKey`, the denc encoder.
- **Produces:**

  ```go
  func LinkOLH(op radosclient.Execer, l LinkOLHOp, r denc.Release)
  func UnlinkInstance(op radosclient.Execer, u UnlinkInstanceOp, r denc.Release)
  func ReadOLHLog(op *radosclient.ReadOp, q ReadOLHLogOp, r denc.Release) *ReadOLHLogResult
  func TrimOLHLog(op radosclient.Execer, t TrimOLHLogOp, r denc.Release)
  func ClearOLH(op radosclient.Execer, c ClearOLHOp, r denc.Release)
  func BIGet(op *radosclient.ReadOp, key ObjKey, typ BIIndexType, r denc.Release) *BIGetResult
  ```

  plus `OLHEntry`, `OLHLogEntry`, `OLHLogOp`, and the pure key functions
  `InstanceIndexKey`, `OLHDataKey`, `ListIndexKey`, `DecreasingStr`, `AfterVersions`,
  `DecodeListIndexKey`.
- **Behaviour:**
  1. The five request structs at their versions and field order: link v5 compat 1, unlink v3,
     read-log v1 with its v1 reply, trim v1, clear v1 ([cls_rgw_ops.h:167-380](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw_ops.h#L167-L380),
     [cls_rgw_ops.h:165-378](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw_ops.h#L165-L378), unchanged between the tags). Link's `olh_tag` is
     the xattr's bytes as a string; its `unmod_since` goes twice, as `u64 time_t` then `real_time`
     ([cls_rgw_client.cc:513-624](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw_client.cc#L513-L624),
     [cls_rgw_client.cc:363-487](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw_client.cc#L363-L487)).
  2. `rgw_bucket_olh_log_entry` v1 and `rgw_bucket_olh_entry` v1, `OLHLogOp` UNKNOWN 0, LINK_OLH 1,
     UNLINK_OLH 2, REMOVE_INSTANCE 3 ([cls_rgw_types.h:513-593](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw_types.h#L513-L593); the v20.2.4
     types end at [cls_rgw_types.h:592](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw_types.h#L592), unchanged).
  3. `bi_get` with `BIIndexType` Plain 1, Instance 2 and OLH 3
     ([cls_rgw_types.h:471-476](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw_types.h#L471-L476), [cls_rgw_types.h:471-477](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw_types.h#L471-L477)).
     The class reads Plain's name verbatim, Instance through `encode_obj_index_key`, which is the
     plain key for an empty instance, and OLH as the OLH data key. The reply's entry carries the
     stored bytes, which `BIGetResult` decodes with phase 1's dir-entry decoder or `OLHEntry`'s,
     each of which must read every version the class may have stored
     ([rgw_rados.cc:9443-9485](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L9443-L9485); callers at
     [rgw_rados.cc:10192](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L10192) and
     [rgw_rados.cc:10214](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L10214)). ENOENT is the result's error.
  4. Keys ([cls_rgw.cc:272-353](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L272-L353), [cls_rgw.cc:417-462](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L417-L462);
     v20.2.4 from [cls_rgw.cc:322](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L322) to [cls_rgw.cc:467](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L467)):
     instance `0x80 "1000_" name "\0i" instance`, plus `"\0d"` for a null-instance delete
     marker; OLH data `0x80 "1001_" name`; list `name "\0v" decreasing_str(epoch) "\0i"
     instance`; after-versions `name "\1"`; the decoder splits on NUL.
- **Specs:**
  - goldens from `ceph-dencoder` at Squid encoding for each request, `rgw_cls_read_olh_log_ret`,
    `rgw_bucket_olh_entry`, `rgw_bucket_olh_log_entry`, `rgw_cls_bi_get_op` and `_ret` (all
    have `generate_test_instances`);
  - byte-literal key specs, including `decreasing_str` across its magnitude bands and a null
    marker's `"\0d"`;
  - each builder's exec class and method name, and RD or WR shape (read-op or write-op).
- **Exclusions:** none.
- **Depends on:** nothing. **PR split:** one. **Brief:** about 200 lines.

### Task V2: meta OLH head types and bucket helpers

- **Layer:** meta (`internal/meta`). **Files:** modify `attrs.go`, `bucket_info.go`; create
  `olh.go` and specs. Modify `internal/driver/copy.go` only to call the lifted helper.
  **Cluster:** no.
- **Produces:** `AttrOLHPendingPrefix`; `type OLHInfo struct{ Target Obj; Removed bool }` and
  `type OLHPendingInfo struct{ Time time.Time }` with encode and decode;
  `(*BucketInfo).Versioned()`, `VersioningEnabled()`, `MFAEnabled()`; `NewInstance() string`;
  `NewOLHOpTag(now time.Time) string`; `NewOLHTag() string`.
- **Behaviour:**
  1. `user.rgw.olh.pending.` ([rgw_common.h:131-136](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.h#L131-L136),
     [rgw_common.h:150-155](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.h#L150-L155)).
  2. `RGWOLHInfo` v1 (`rgw_obj target`, `bool removed`) and `RGWOLHPendingInfo` v1
     (`real_time`) ([rgw_rados.h:94-138](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.h#L94-L138),
     [rgw_rados.h:96-140](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.h#L96-L140)).
  3. `versioned()` is `BUCKET_VERSIONED`; `versioning_enabled()` is versioned and not
     suspended; `mfa_enabled()` is `BUCKET_MFA_ENABLED` ([rgw_common.h:1074-1079](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.h#L1074-L1079),
     [rgw_common.h:1116-1121](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.h#L1116-L1121)). `driver.versioningEnabled`
     (`internal/driver/copy.go:247-250`) is replaced by the meta helper.
  4. The instance: 32 random alphanumerics without `_`
     ([rgw_rados.cc:8868-8877](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8868-L8877),
     [rgw_rados.cc:9614-9623](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9614-L9623)). The op tag: `%016llx` of the
     second, then random lower alphanumerics to 32 bytes, so pending names sort by time; the
     OLH tag and the fresh `idtag`: 32 lower alphanumerics
     ([rgw_rados.cc:7842-7887](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L7842-L7887)).
- **Specs:** goldens for both types; the formats (16 hex then 16 lower alnum; 32 alnum without
  `_`); pending names from two seconds sort in time order; the flag table for the three helpers.
- **Exclusions:** none.
- **Depends on:** nothing. **PR split:** one. **Brief:** about 100 lines.

### Task V3a: emulator, versioned keys in the index methods

- **Layer:** emulator. **Files:** modify `internal/testutil/fakerados/cls_rgw.go` (the existing
  prepare, complete, suggest and list handlers) and its specs. **Cluster:** no.
- **Consumes:** S1's tables; V1's key functions. **Produces:** `Cluster` readers specs use for
  instance, list and OLH-data entries of a shard.
- **Behaviour** (Squid's class, VD-9):
  1. prepare, complete and suggest accept an instance key: the entry lives at the instance key
     (`encode_obj_index_key`, [cls_rgw.cc:339-346](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L339-L346)). The EOPNOTSUPP
     refusals go (`internal/testutil/fakerados/cls_rgw.go:440-441`,
     `internal/testutil/fakerados/cls_rgw.go:480-481`,
     `internal/testutil/fakerados/cls_rgw.go:998-1003`).
  2. complete resets the entry's flags to `FLAG_VER` and writes only the key it resolved: an
     instance key writes the instance entry, never a list entry
     ([cls_rgw.cc:1061-1064](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1061-L1064), [cls_rgw.cc:1168-1197](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1168-L1197)).
  3. `bucket_list` skips `FLAG_VER_MARKER` entries; without `list_versions` it skips a
     non-visible entry, or one named like the start key, to `cls_rgw_after_versions(name)`
     ([cls_rgw.cc:604-623](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L604-L623), [cls_rgw.cc:654-673](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L654-L673)).
  4. The start marker resolves through `encode_list_index_key`: no instance, the name; `null`,
     the empty instance; an instance whose instance entry exists, its exact list key; one that
     does not, `name "\x01"` ([cls_rgw.cc:358-390](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L358-L390)). The refusal at
     `internal/testutil/fakerados/cls_rgw.go:1164-1165` goes.
  5. Advance-and-retry after eight omap reads that listed nothing, with the reply encoded
     ([cls_rgw.cc:492](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L492), [cls_rgw.cc:680-691](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L680-L691)).
- **Specs:** each branch, citing its class lines; list with versions on and off over instance, list
  and marker entries; the four marker cases; eight reads of non-visible entries yield -EFBIG with
  the marker (through V0's `Reply`); a complete on an instance key leaves no list entry.
- **Exclusions:** none.
- **Depends on:** S1, V0 (the advance-and-retry spec), V1. **PR split:** one. **Brief:** about
  180 lines.

### Task V3b: emulator, the OLH methods, `bi_get` and `obj_check_attrs_prefix`

- **Layer:** emulator. **Files:** fill `internal/testutil/fakerados/cls_rgw_olh.go` (S1's empty
  table); create `cls_rgw_bi.go`, `cls_rgw_attrs.go` and specs. **Cluster:** no.
- **Consumes:** S1, V1. **Produces:** table rows: `bucket_link_olh` RD|WR,
  `bucket_unlink_instance` RD|WR, `bucket_read_olh_log` RD, `bucket_trim_olh_log` RD|WR,
  `bucket_clear_olh` RD|WR, `bi_get` RD, `obj_check_attrs_prefix` RD
  ([cls_rgw.cc:4699-4707](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L4699-L4707), [cls_rgw.cc:5116-5120](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L5116-L5120)).
- **Behaviour** (Squid's class, no class-side reshard guard, VD-9):
  1. Link ([cls_rgw.cc:1652-1865](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1652-L1865)): the instance entry, ENOENT allowed only
     for a delete marker, which the class creates with tag `delete-marker`; `unmod_since` returns
     0 unchanged; the null data and null marker pair; epoch 0 becomes the next epoch, starting at
     2; a nonzero epoch below the entry's writes the instance non-current and returns
     ([cls_rgw.cc:1503-1517](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1503-L1517), [cls_rgw.cc:1751-1760](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1751-L1760));
     promote on a newer epoch, or an equal epoch and a greater-or-equal instance
     ([cls_rgw.cc:1764-1766](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1764-L1766)); ECANCELED on a tag mismatch unless
     `pending_removal` ([cls_rgw.cc:1768-1777](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1768-L1777)); demote; convert a plain
     entry when no OLH entry exists ([cls_rgw.cc:1587-1634](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1587-L1634)); log LINK_OLH and
     REMOVE_INSTANCE; write the OLH entry, then the instance and list entries.
  2. Unlink ([cls_rgw.cc:1867-2018](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1867-L2018)): ENOENT for a missing instance entry;
     plain conversion; old epoch unlinks the list entry and logs REMOVE_INSTANCE (a marker
     logs nothing); the current head promotes `find_next_key`'s next version or logs UNLINK_OLH
     with `exists=false, pending_removal=true`; a marker's instance entry is removed by the
     class, a data instance's removal is logged; no tag check.
  3. Read-log ([cls_rgw.cc:2020-2072](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L2020-L2072)): EINVAL on an instance; ECANCELED
     on a tag mismatch, a missing entry's tag being ""; entries above the marker, at most 1000,
     with `is_truncated`. Trim ([cls_rgw.cc:2074-2124](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L2074-L2124)) erases epochs at or
     below `ver`. Clear ([cls_rgw.cc:2126-2190](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L2126-L2190)) removes the OLH entry, then
     the plain entry only when it is a `FLAG_VER_MARKER`.
  4. `obj_check_attrs_prefix`: EINVAL on an empty prefix; ECANCELED when "an xattr has the prefix"
     equals `fail_if_exist` ([cls_rgw.cc:2509-2551](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L2509-L2551),
     [cls_rgw.cc:2677-2719](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2677-L2719)).
  5. `bi_get` exactly as the class resolves its key
     ([cls_rgw.cc:2617-2664](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L2617-L2664),
     [cls_rgw.cc:2785-2832](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2785-L2832)): Plain reads the name verbatim;
     Instance goes through `encode_obj_index_key`, so an empty instance reads the plain key, which
     is the version marker once the key is converted; OLH reads the OLH data key; ENOENT when
     absent.
- **Specs:** every branch above by name (new OLH, existing, tag mismatch, old epoch,
  promote/demote, null pair, `unmod_since`, plain conversion; unlink current with and without a
  next version, noncurrent, marker, missing, old epoch; read-log marker, truncation at 1000,
  mismatch; trim; clear's VER_MARKER rule; `bi_get` of each type, Instance of an empty instance on
  a converted key returning the version marker; the attrs-prefix truth table); string-mode
  `cmpxattr` `EQ ""` matches an absent xattr. Each spec cites its class lines.
- **Exclusions:** none.
- **Depends on:** S1, V1. **PR split:** one. **Brief:** about 245 lines.

### Task V4: driver, the OLH head operations

- **Layer:** driver. **Files:** create `internal/driver/olh_head.go` and specs; append to
  `docs/cgo-limitations.md` and `docs/differences.md`. **Cluster:** no.
- **Review:** task review.
- **Consumes:** V1 (requests), V2 (`OLHInfo`, `OLHPendingInfo`, tags), V3b (the emulated methods),
  phase 1's `readHead` and write ops. **Produces** (driver-internal): `olhInitModification`,
  `olhCancelModification`, `checkPendingOLHEntries`, `removeOLHPendingEntries`, `followOLH`,
  `applyOLHLog`, `clearOLHHead`, `repairOLH`, and the interface V5a implements:

  ```go
  type olhIndex interface {
      readLog(ctx context.Context, head *op.ObjectState, marker uint64) (rgw.ReadOLHLogRet, error)
      trimLog(ctx context.Context, head *op.ObjectState, ver uint64) error
      clear(ctx context.Context, head *op.ObjectState) error
      removeInstance(ctx context.Context, key meta.ObjKey, expectTag []byte) error
  }
  ```

  with a fifth method, `getOLH`, the `bi_get` of the OLH entry that `repairOLH` reads.
  `expectTag`, when non-nil, replaces the write tag the removal reads as its guard (V8c's
  `Expect`); `applyOLHLog` passes it for the REMOVE_INSTANCE entries carrying
  the replaying writer's own op tag, and nil otherwise.
- **Behaviour** (v19.2.7 ranges; each function's v20.2.4 first line in N13 of the notes):
  1. `olhInitModification` ([rgw_rados.cc:7799-7910](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L7799-L7910),
     [rgw_rados.cc:8462](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L8462)): one
     write op on the OLH head, the key without an instance. A missing head is `create(exclusive)`;
     an existing one `assert_exists` and `mtime2` of the state's mtime, which the OLH ops keep.
     With an OLH tag, `cmpxattr(olh.idtag, EQ, tag)`. Existing without one (a plain object
     becoming versioned): `cmpxattr(olh.idtag, EQ, "")` and `olh.info` set to itself, not
     removed. Without a tag: a fresh `idtag` and `olh.idtag` and an empty `olh.ver`. Always
     `olh.pending.<op_tag>` = `OLHPendingInfo{now}`. EEXIST becomes ECANCELED. It returns the op
     tag and the `olh.ver` bytes of the head it was built on, for V5c.
  2. D1's guard, when the caller passes the `olh.info` bytes it evaluated a condition on (empty
     when absent): `cmpxattr(olh.info, EQ, bytes)` and `obj_check_attrs_prefix(olh.pending.,
     fail if exists)`, both ahead of every set in the op, so they see other writers only. It
     serializes rgw-go's conditional writers; a writer that checks only `olh.idtag` passes it, and
     V5c's post-link check covers that writer (D1, review F1). Past VD-4's bound the caller drops
     the `obj_check_attrs_prefix` half and keeps the `olh.info` half (review N2).
  3. `olhCancelModification` ([rgw_rados.cc:7754-7797](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L7754-L7797),
     [rgw_rados.cc:8417](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L8417)): remove
     the pending attr under the tag guard; when the head has no `olh.info`, remove the head under
     `cmpxattr(olh.info, EQ, "")` and `obj_check_attrs_prefix(olh.pending., fail if exists)`.
  4. Pending expiry ([rgw_rados.cc:8904-8967](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8904-L8967)): attrs older than
     `rgw_olh_pending_timeout_sec` from `Env.Conf` (default 1 h,
     [rgw.yaml.in:2389-2399](https://github.com/ceph/ceph/blob/v19.2.7/src/common/options/rgw.yaml.in#L2389-L2399),
     [rgw.yaml.in:2483](https://github.com/ceph/ceph/blob/v20.2.4/src/common/options/rgw.yaml.in#L2483)), stopping at the first unexpired (names sort by
     time); removed under the tag guard in batches of 1000, ENOENT and ECANCELED being success.
  5. `followOLH` ([rgw_rados.cc:8969-9021](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8969-L9021),
     [rgw_rados.cc:9715](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9715)): expire; any pending
     attr left runs the injected update (V5b's `updateOLH`), whose ECANCELED is ENOENT; no
     `olh.ver` is EINVAL; no `olh.info` ENOENT; `removed` ENOENT with the delete-marker flag set,
     as v20.2.4 sets `is_dm` ([rgw_rados.cc:9760-9764](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9760-L9764)); else the
     target key.
  6. `applyOLHLog` ([rgw_rados.cc:8409-8566](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8409-L8566),
     [rgw_rados.cc:9155](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9155)): one head op guarded
     by `cmpxattr(olh.idtag, EQ, tag)` and VD-1's `cmpxattr(olh.ver, EQ, <bytes read>)`, setting
     `olh.ver` to the last epoch in decimal and the kept mtime. Replay in epoch order: LINK_OLH
     applies when its epoch is above the head's, the head has no target instance, or the entry's
     instance sorts before the current one ([rgw_rados.cc:8482-8496](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8482-L8496),
     [rgw_rados.cc:9228-9242](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9228-L9242));
     UNLINK_OLH marks removal; REMOVE_INSTANCE collects keys; every entry's pending attr is
     removed; `olh.info` is set when a link won. The collected instances go through
     `removeInstance` before the head op ([rgw_rados.cc:8530-8541](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8530-L8541)).
     Then `clearOLHHead` when removal is marked, else `trimLog(last)`; ECANCELED tolerated. A head
     op that fails VD-1's guard writes nothing and returns ECANCELED to `updateOLH`, which retries
     from a fresh read (V5b).
  7. `clearOLHHead` ([rgw_rados.cc:8583-8634](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8583-L8634)): expire pending
     attrs; remove the head under `olh.idtag EQ`, `olh.ver EQ to_string(ver)` (the same bytes in
     both modes) and no pending attrs; then the index `clear`.
  8. `repairOLH` ([rgw_rados.cc:8275-8324](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8275-L8324),
     [rgw_rados.cc:9021](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9021)): rewrite `olh.idtag`
     and `olh.info` from the index's OLH entry under the old tag's guard.
- **Specs:** each op with its guard failing (tag mismatch, exclusive create lost → ECANCELED); D1's
  guard failing on a changed `olh.info` and on another writer's pending attr, and passing on its
  own; the replay rules (epoch, the instance tie-break, UNLINK then LINK, REMOVE_INSTANCE calling
  `removeInstance`); the EQ guard under a concurrent newer apply: the head op fails ECANCELED and
  writes nothing; expiry with `Cluster.SetClock`; follow's five outcomes; clear's guards; repair
  from a fake OLH entry. The index calls are a fake `olhIndex` here; V5b runs them on the emulator.
- **Exclusions and registry:**
  - `docs/cgo-limitations.md`, "librados' C API has no u64-mode cmpxattr" (D9, a limitation,
    Q10's not-now ruling).
  - `docs/differences.md`, "apply_olh_log's olh.ver guard is EQ on the bytes read, retried from a
    fresh read" (VD-1; the state written is radosgw's but in the same-epoch window, where a retry
    from a fresh `olh.ver` keeps the class's current version and radosgw writes the link logged
    last).
  - `docs/differences.md`, "A replay passes over a version removal that the writer's expected
    write tag refuses": the replay lands, trims the log when another writer refused its clear,
    and reports the refusal; only a refusal returned without an error is final.
  - `docs/ceph-upstream-bugs.md`, five entries: "radosgw's apply_olh_log applies a same-epoch
    link the class did not make current", "cls_rgw's OLH log trim drops an unapplied link at the
    trimmed epoch", "radosgw's clear_olh clears the index's OLH entry after a failed head
    removal", "radosgw's versioned write fails when the key's OLH head goes between its read and
    its init" and "radosgw's follow_olh answers the version that was current before the replay it
    runs".
- **Depends on:** V1, V2, V3b. **PR split:** one. **Brief:** about 235 lines.

### Task V5a: driver, the OLH index operations and the awaited completion

- **Layer:** driver. **Files:** modify `internal/driver/indexop.go`; create
  `internal/driver/olh_index.go` and specs; edit `docs/differences.md`. **Cluster:** no.
- **Review:** task review.
- **Consumes:** V1, V3a, V3b, V4's `olhIndex`. **Produces:**
  - `func (x *indexOp) guardedWrite(ctx context.Context, call func(w *radosclient.WriteOp)) error`
  - `func (x *indexOp) completeSync(ctx context.Context, ver rgw.EntryVer, m rgw.DirEntryMeta)
    error`
  - `linkOLH` and `unlinkInstance` calls taking the head state, the key, the op tag and the
    request fields; `olhIndex`'s implementation; `getInstanceEntry` for V6, V8a and V9
    (behaviour 5).
- **Behaviour:**
  1. Link, unlink, trim and clear each run `assert_exists`, the reshard guard
     (`-ERR_BUSY_RESHARDING`) and the class call in one write op inside `guardReshard`, which
     re-finds the shard from the bucket each try and, from F7, also retries a shard that is gone
     (ENOENT), as v19.2.7's guard does ([rgw_rados.cc:7929-7933](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L7929-L7933))
     and v20.2.4's does not (F7's Tentacle exclusion) (link
     [rgw_rados.cc:8153-8165](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8153-L8165), unlink
     [rgw_rados.cc:8207-8216](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8207-L8216), trim
     [rgw_rados.cc:8349-8356](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8349-L8356), clear
     [rgw_rados.cc:8380-8388](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8380-L8388); v20.2.4
     [rgw_rados.cc:8876-9141](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L8876-L9141), the same shape; `guard_reshard`
     [rgw_rados.cc:7912-7966](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L7912-L7966),
     [rgw_rados.cc:8575](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L8575)). The shard is
     the key's hash shard by index name; the instance is in the key but not in the hash.
  2. The read-log and `bi_get` are unguarded reads on the current shard, the log read with the
     empty-instance key and the head's tag ([rgw_rados.cc:8225-8269](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8225-L8269),
     [rgw_rados.cc:8971](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L8971)).
     `getOLH`, the `bi_get` OLH of the name, maps to `radosclient.ErrNotFound` the class's
     ENOENT for a name with no OLH entry, which `cls_cxx_map_get_val` answers
     ([cls_rgw.cc:2655-2659](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L2655-L2659),
     [cls_rgw.cc:2823-2827](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2823-L2827)):
     V4's settlement of a refused removal reads "absent" from it, and takes the refusal as final
     then. A shard object a reshard removed also answers ENOENT at the OSD, so on ENOENT `getOLH`
     re-finds the shard from the bucket, as `guardReshard` does for the guarded writes
     (behaviour 1), and reads again; only an ENOENT from the shard the bucket now names is not
     found. A stale shard reference would otherwise read as "absent" and settle a refusal as
     final while its entries sit on the new shard.
  3. Request fields: `olh_tag` is the head's `olh.idtag` bytes; epoch 0 unless the caller gives
     one; `log_op` is phase 1's `logOp()`, false in a single zone; `zones_trace` is phase 1's
     `zonesTrace()`; unlink's bilog flags are VERSIONED_OP, plus NULL_VERSION for a request that
     named `null` ([rgw_rados.cc:8814-8818](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8814-L8818)).
  4. `completeSync` (D5) sends phase 1's ADD completion through the same guarded path the
     completion manager uses and returns its answer instead of retrying in the background.
  5. `getInstanceEntry` resolves a version's entry as the class's `read_key_entry` does
     ([cls_rgw.cc:946-978](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L946-L978),
     [cls_rgw.cc:1076-1108](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1076-L1108)), because `bi_get` of an empty
     instance reads the plain key ([cls_rgw.cc:2617-2664](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L2617-L2664),
     [cls_rgw.cc:2785-2832](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2785-L2832); review F4). A named instance is one
     `bi_get` Instance. The empty instance is `bi_get` Instance of the plain key; when that entry is
     `FLAG_VER_MARKER`, `bi_get` Plain of the null delete marker's raw key (V1's
     `InstanceIndexKey` with the marker suffix), and when that is absent, of the null data key.
     Plain reads the name verbatim, so it reaches both. ENOENT from the last read is the caller's.
- **Specs:** the request bytes of each call match V1's goldens; a busy shard waits and retries on
  the refreshed shard; ten busy answers are ErrBusyResharding; the read-log carries no guard;
  `getOLH` of a name with no OLH entry answers `radosclient.ErrNotFound` (`errors.Is`), and of a
  re-tagged entry its tag and kept pending log; `getOLH` on a shard a reshard removed re-finds
  the shard and answers the entry from the new one, not not-found; `completeSync` returns the
  class's error; `getInstanceEntry` on a named instance, a never-converted plain entry, a
  converted key with null data, one with a null delete marker, and one with neither (ENOENT).
- **Exclusions and registry:** a new exclusion, "A versioned write awaits its index completion"
  (D5); "OLH epochs differ by OSD release" rewritten to what phase 2 does: epoch 0 in every
  request and no epoch reply decoded on either floor; the registry entry for tracker #80894
  (defect 2; data corruption, both releases, reproduced) gains that it applies to instance
  entries too, with no client workaround.
- **Depends on:** V3a, V3b, V4. **PR split:** one. **Brief:** about 170 lines.

### Task V5b: driver, `setOLH`, `unlinkObjInstance` and the races

- **Layer:** driver. **Files:** create `internal/driver/olh.go` and specs; modify
  `internal/driver/stat.go` (the unfollowed head read, behaviour 8),
  `internal/driver/export_integration.go` (a fault point) and `docs/differences.md`.
  **Cluster:** no.
- **Review:** task review, then a Fable review (P3, the owner, 2026-10-13).
- **Consumes:** V4, V5a, phase 1's `readHead` and `removeHead`. **Produces:**

  ```go
  func (s *Store) setOLH(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, p olhLink) error
  func (s *Store) unlinkObjInstance(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, p olhUnlink) error
  func (s *Store) updateOLH(ctx context.Context, rec *op.BucketRecord, head *op.ObjectState) error
  ```

  `olhLink` carries the delete-marker flag and meta, the epoch, `unmod_since` and its
  precision and `SkipUpdate`, plus two hooks V5c fills: an optional condition (D1) and an
  optional `ExpectTag []byte`. `olhUnlink` carries the epoch, the null-version flag,
  `SkipUpdate` and an optional `ExpectTag`. A removal refused by its `ExpectTag` is returned,
  after the protocol completes, as `errRemovalRefused` naming the key. Only a refusal returned
  without an error is final, and only a final one is V8c's to re-link (this task's behaviour 5).
- **Behaviour:**
  1. `setOLH` ([rgw_rados.cc:8669-8762](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8669-L8762),
     [rgw_rados.cc:9415](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9415)), each attempt: read the
     OLH head without following (behaviour 8), afresh after an ECANCELED or a retried ENOENT;
     init; link; on a link failure cancel, then ECANCELED → `repairOLH` and retry, any other
     error → a best-effort `updateOLH` unless skipped, and return. Init's ENOENT on a head the
     read found, the head removed since, as the unlink of the key's last version removes it, is
     retried like its ECANCELED (VD-12), where `set_olh` returns it
     ([rgw_rados.cc:8699-8706](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8699-L8706),
     [rgw_rados.cc:9445-9452](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9445-L9452)).
     After the link, `updateOLH` unless skipped; ECANCELED there is success
     ([rgw_rados.cc:8752-8755](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8752-L8755)). 100 attempts, then EIO. A condition
     or an `ExpectTag` adds V5c's rules to each attempt.
  2. A link at an epoch below the OLH entry's (C11's transition, V8c's re-link) writes the
     instance non-current and logs nothing ([cls_rgw.cc:1751-1760](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1751-L1760),
     [cls_rgw.cc:1892-1901](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1892-L1901)), so no replay removes its pending attr. The link's
     reply carries no epoch, so the trigger is the replay (review N3): after its own replay,
     `setOLH` removes its own pending attr under the tag guard, as `olhCancelModification` does,
     only when the log that replay read carried no entry with its op tag. An old-epoch link wrote
     none; a logged entry another gateway applied and trimmed had its attr removed by that apply,
     so the removal finds nothing. A logged entry the replay could not apply (VD-1's bound
     exhausted) keeps its attr, which makes the next `followOLH` apply it. With `SkipUpdate` there
     is no replay, and the attr stays for the next update. radosgw's own `set_olh` never removes
     it, since `apply_olh_log` returns at once on an empty log
     ([rgw_rados.cc:8423-8425](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8423-L8425),
     [rgw_rados.cc:9169-9171](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9169-L9171)): its old-epoch links, such as its
     NoncurrentVersionTransition, leave their pending attr until it expires (VD-4).
  3. `unlinkObjInstance` ([rgw_rados.cc:8764-8866](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8764-L8866),
     [rgw_rados.cc:9510](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9510)): the same
     loop around the unlink; the class's ENOENT is the caller's NoSuchKey.
  4. `updateOLH` ([rgw_rados.cc:8639-8667](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8639-L8667),
     [rgw_rados.cc:9385](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9385)): read the log from
     the head's `olh.ver` (VD-13) and apply it, a page at a time while truncated: the class
     returns up to 1000 epochs above the `ver_marker` a page, setting `is_truncated`
     ([cls_rgw.cc:2057-2066](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L2057-L2066),
     [cls_rgw.cc:2198-2207](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2198-L2207)).
     - Each page's landed head op wrote its last epoch to `olh.ver`, and `updateOLH` carries that
       epoch forward, as the next page's marker and as the `olh.ver` its apply expects, so every
       later page meets VD-1's EQ guard and a log longer than the bound times 1000 epochs lands.
       The later pages' epochs all lie above both that epoch and the `olh.ver` first read, so the
       replay takes the links radosgw's does, whose `update_olh` hands every page the state it
       first read.
     - An apply that fails its guards with ECANCELED is retried from a fresh head read (`olh.ver`
       and the log read again, then applied), up to a bound the task names as a constant; only
       past the bound is ECANCELED success by the protocol, with the pending attrs left for the
       next reader. This restores the progress radosgw's numeric GTE keeps
       ([rgw_rados.cc:8435](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8435),
       [rgw_rados.cc:9181](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9181)) under VD-1's EQ guard (review F5).
     - An apply whose head op landed and whose tail did not finish is not applied again. V4's
       error names the tail left, the clear or the trim, and `updateOLH` runs that tail again
       through V4's function that clears or trims the log after a replay's head op, within the
       same bound (behaviour 5).

     `updateOLH` leaves `*head` as its caller read it, carrying the expected `olh.ver` in a
     copy of its own, as `update_olh` leaves the caller's state:
     `apply_olh_log` only reads it
     ([rgw_rados.cc:8409-8566](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8409-L8566),
     [rgw_rados.cc:9155-9312](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9155-L9312)).
     Each retry works on its own fresh read, and a caller that needs the head as the update left
     it reads the head again (V6, VD-11). The carried copy's `olh.info` is the first read's, and
     nothing compares against it across pages.
  5. `removeInstance` (V4's call): phase 1's unversioned removal on the instance key: read the
     instance head, guard on its write tag, or on `expectTag` when given, then `removeHead`
     (prepare DEL, obj_remove keeping `user.rgw.olh.`, complete_del, tails to the GC); ENOENT is
     success. A failed `expectTag` guard cancels the index DEL, so the head and the instance entry
     stay while the unlink has already removed the list entry, and the removal answers the refusal
     V4's `olhIndex` seam defines. The replay passes over that version, lands its head op (pending
     attrs, `olh.ver`) and its trim or clear, trimming to its last epoch when another writer's
     pending attr refused the clear, and only then returns the refusal, which reaches the writer
     as the Produces paragraph gives it.
     - Only a refusal returned without an error is final. One joined with a failed trim, index
       clear or clear expiry leaves the entry in the log. V4's error names the tail left, and
       `updateOLH` runs it again through V4's tail function, within its bound (behaviour 4); the
       trim that follows a clear another writer refuses lives in that function alone. It returns
       the refusal alone once the tail finishes. Past the bound it returns the error with the
       refusal, which is not final.
     - V4's third outcome, the refusal joined with its not-final, not-retryable sentinel, means
       the writer's entries are still in the log of an OLH entry another writer's link re-tagged
       (V4's `olhIndex` seam). `updateOLH` does not retry it, since no retry restores the old
       tag, and returns it as it is; that writer's replay may remove the version.
     - Reading from `olh.ver` (VD-13), rgw-go's own later replays skip a refused removal whose
       trim failed, at or below the `olh.ver` the head op wrote, where a radosgw replay, reading
       from 0, meets it and removes the version. A mixed fleet keeps that: it is the second
       window of V8c's behaviour 5.
     - Once the writer's own replay has landed with its trim or clear, no later replay meets the
       entry. Until then the version can still go as radosgw's replay removes it: a replay by
       another reader running alongside the writer's own, or one after the writer's head op
       failed and its retry never landed, meets the entry without `expectTag`. That is the second
       window of V8c's behaviour 5.

     Any other removal failure stops the replay before its head op, as `apply_olh_log`'s does
     ([rgw_rados.cc:8537-8540](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8537-L8540),
     [rgw_rados.cc:9283-9286](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9283-L9286)).
     Phase 1's guard stays where Tentacle removes unguarded (defect 14).
  6. VD-5: a link or unlink that failed with a class or guard answer, and a `completeSync`
     that did, remove the instance the write made through `removeInstance`; a timeout or a lost
     connection removes nothing.
  7. A fault point, compiled only into the integration build, stops a writer after its link
     and before its replay, for V15b's case D.
  8. The unfollowed head read. `readHead` refuses a head that carries `olh.ver` until V6 (the
     refusal is in `decodeState`, which it calls). `setOLH`, `unlinkObjInstance` and `updateOLH`'s
     retries read the head through a variant that decodes it without that refusal and never
     follows, as `set_olh` and `unlink_obj_instance` read theirs
     ([rgw_rados.cc:8694](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8694),
     [rgw_rados.cc:8793](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8793);
     [rgw_rados.cc:9440](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9440),
     [rgw_rados.cc:9539](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9539)).
     `StatObject` and `PrefetchObject` keep refusing until V6.
- **Specs** (on the emulator with V3a and V3b):
  - each §2.9 failure: init EEXIST → retry; link ECANCELED → repair, then success; 100
    ECANCELEDs → EIO; another link error → cancel, best-effort update, VD-5's removal; a
    timeout → nothing removed;
  - **"radosgw and rgw-go write versions of one key at once"** (Review Focus 1): two writers,
    rgw-go's driver and a radosgw-shaped writer built from V1's requests in radosgw's order (its
    ADD completion unawaited but sent before its link on the same shard; its apply taken only
    when its last epoch is at or above the stored `olh.ver`, as the OSD's numeric GTE takes it,
    with nothing scheduled between its read and its write). A schedule table, driven by
    fakerados's `AfterWrite` and `BeforeRead` hooks, interleaves them at every boundary of
    prepare, head write, complete, init, link and apply. Expected once both have returned: both
    versions present (heads, instance and list entries), listed newest epoch first, `olh.info`
    on the version the class linked last, `olh.ver` its epoch, no pending attr, the log trimmed.
    V5c reuses this writer;
  - **"an apply that loses VD-1's guard retries"** (F5): rgw-go's writer reads `olh.ver` empty,
    the other applies epoch 2, rgw-go's apply of epoch 3 fails EQ, re-reads and lands: `olh.info`
    on epoch 3's version, no pending attr;
  - **"a log of many pages lands in one update"** (re-review 2, Minor 2): a pending log of more
    than the bound times 1000 epochs replays in one `updateOLH`, each page's apply meeting the EQ
    guard on the epoch the page before wrote, with no retry spent;
  - **"an unfinished tail runs again"** (re-review 2, Important 1): the head op lands and the trim
    fails once; `updateOLH` runs V4's tail again without a second head op, and the log ends
    trimmed. The same for an index clear that fails once;
  - **"a versioned write meets a reshard in progress"** (Review Focus 2): the shard set
    resharding (a) between the completion and the link, (b) between the link and the trim, the
    reshard then finishing by moving the bucket to a new index generation with its instance,
    list and OLH entries copied: the write completes on the new shard, with no orphaned instance
    entry, no pending attr and no log entry left; (c) a reshard that outlasts the guard's ten
    waits: the write answers ErrBusyResharding, its pending attr is removed and its instance with
    it when the shard allows, or else is left as radosgw leaves it, which (c) asserts and the
    exclusion records;
  - a link at an old epoch: the instance non-current, no pending attr left; the same link by the
    radosgw-shaped writer leaves its pending attr, with no log entry, as radosgw does;
  - **"an apply that exhausts VD-1's bound keeps the pending attr"** (review N3): every retry of
    rgw-go's apply loses the EQ guard; `setOLH` returns, its LINK entry unapplied, its pending
    attr present and `olh.info` on the previous version; the next `followOLH` applies the entry,
    and the stored `olh.info` then names the version the listing shows current. The spec asserts
    the stored `olh.info`, not the follow's answer, which is the version current before its
    update (V4; V6 re-resolves, VD-11);
  - `expectTag` refused: `errRemovalRefused`, the head and the instance entry present, the list
    entry absent (V8c's re-link starts from this state). With another writer's init between the
    head op and the clear, the log is trimmed and that writer's replay leaves the version. With a
    trim that fails once, `updateOLH` retries it and returns the refusal alone; with one that
    keeps failing, the error comes with the refusal and the entry stays in the log;
  - init's ENOENT (VD-12): the key's last version is unlinked and its OLH cleared between
    `setOLH`'s read and its init; the next attempt finds no head, creates it and links, and the
    write succeeds with its version current;
  - a writer stopped after its link: the next writer's update applies both; a pending attr past
    the timeout is expired by the next follow (`Cluster.SetClock`);
  - `SkipUpdate` leaves the pending attrs for the next update; two concurrent unlinks of one
    instance: one success, one NoSuchKey.
- **Exclusions and registry:** a new exclusion, "rgw-go removes the instance a failed versioned
  write made" (VD-3, VD-5; radosgw leaves it for `bucket check`); a new exclusion, "A versioned
  write retries when the key's OLH head goes between its read and its init" (VD-12), citing the
  registry's "radosgw's versioned write fails when the key's OLH head goes between its read and
  its init"; a new exclusion, "A replay reads the pending log above the head's olh.ver"
  (VD-13): the entries at or below it are already applied to the head, so rgw-go's replays
  skip a refused removal whose trim failed, where radosgw's remove the version, the safer side,
  and a mixed fleet still meets radosgw's behaviour there (V8c's behaviour 5); the registry
  entry for defect 14 (data corruption, v20.2.4, unreproduced) gains that `apply_olh_log`
  reaches it.
- **Depends on:** V5a. **PR split:** one. **Brief:** about 245 lines.

### Task V5c: driver, conditional links (D1) and `ExpectTag`

- **Layer:** driver. **Files:** create `internal/driver/olh_cond.go` and specs; modify
  `internal/driver/olh_head.go` (`ExpectTag`'s cmpxattr in V4's init). **Cluster:** no.
- **Review:** task review, then V5b's Fable review, which V5c shares (P3).
- **Consumes:** V4's D1 init guard, V5b's `setOLH`, `unlinkObjInstance` and `updateOLH` and its
  radosgw-shaped writer, phase 1's `bucket_list` call on a shard. **Produces:** the condition and
  `ExpectTag` rules inside `setOLH` and `unlinkObjInstance`, consumed by V7a, V7b, V8a and V8c.
- **Behaviour:**
  1. With a condition (D1), each attempt resolves the current version from the head it read
     (`olh.info`'s target, or none, or a delete marker counting as none), evaluates the
     condition, and passes the `olh.info` bytes to V4's init guard. It keeps `V0`, the `olh.ver`
     it read, or 1 when the head had none: epoch 1 is the class's converted plain entry
     ([cls_rgw.cc:1503-1517](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1503-L1517), [cls_rgw.cc:1630-1644](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1630-L1644)),
     which the condition already saw. A failed condition answers 412 and removes the instance
     (VD-3). An init that fails the guard because pending attrs remain runs `updateOLH`, backs
     off and retries; past VD-4's bound the attempt drops the `obj_check_attrs_prefix` half of
     V4's guard and keeps the `olh.info` half. The post-link check (B2) then carries D1's
     guarantee, so a pending attr no writer will remove delays the write by the bound and never
     refuses it (review N2). An attempt the `olh.info` guard cancels re-evaluates the condition;
     radosgw's 100 attempts, then EIO, bound the whole loop.
  2. The post-link check (review F1). The init guard serializes rgw-go's conditional writers
     only: an unconditional rgw-go writer or a radosgw writer checks just `olh.idtag`
     ([rgw_rados.cc:7799-7910](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L7799-L7910)), so it can init and link between a
     conditional write's init and its link. After its link and before its own replay, the
     conditional writer lists the key's versions on its shard (`bucket_list` with versions, the
     name as prefix and start, that name's entries only), newest first, down to the first entry
     at or below `V0`; `E` is its own instance's epoch there. The write is void when another
     version or marker holds an epoch in (`V0`, `E`), or when the condition matched a version
     (If-Match, a size or a last-modified match) that is no longer listed: an unlink of a key's
     only version leaves no list entry at its epoch. A void write is unlinked through
     `unlinkObjInstance`, which promotes the interloper when the void version was current, and
     answers 412. The listing, not the OLH log, is read because another gateway may have applied
     and trimmed the log. Two conditional writers racing may both answer 412.
  3. With an `ExpectTag` (V8c), each attempt compares the head's `user.rgw.idtag` with it, and a
     mismatch is `op.ErrConcurrentModification` with nothing written; init adds
     `cmpxattr(user.rgw.idtag, EQ, ExpectTag)`, whose ECANCELED makes the next attempt re-read
     and re-check. The cmpxattr goes with D1's guard, ahead of every set in the op, so it sees
     other writers only: init itself sets `user.rgw.idtag` on a head without an OLH tag. A missing
     head reads its tag as empty, so a non-empty `ExpectTag` is a mismatch before init, and an
     empty one matches. Init then sends no cmpxattr: on a missing object `getattr_maybe_cache`
     answers ENOENT, which the cmpxattr returns instead of reading the xattr as empty
     ([PrimaryLogPG.cc:6403-6408](https://github.com/ceph/ceph/blob/v19.2.7/src/osd/PrimaryLogPG.cc#L6403-L6408),
     [PrimaryLogPG.cc:6472-6477](https://github.com/ceph/ceph/blob/v20.2.4/src/osd/PrimaryLogPG.cc#L6472-L6477)).
     The exclusive create stands in for it: a head another writer made first fails the create,
     ECANCELED, and the next attempt's compare refuses. The writer's own replay passes it to
     `removeInstance` for the REMOVE_INSTANCE entries carrying its op tag. On `unlinkObjInstance`
     it guards the removal only; the check before the unlink is V8c's.
- **Specs** (on the emulator, with V5b's radosgw-shaped writer):
  - **"a radosgw writer inits between a conditional write's init and its link"**, both
    interleavings: (i) it links first: the conditional write answers 412, its instance is gone,
    the radosgw-shaped version is current, no pending attr; (ii) it links second: the
    conditional write succeeds, both versions are listed, and the radosgw-shaped one is current;
  - the same two with an unconditional rgw-go writer;
  - If-Match on a key's only version, which another writer deletes by version id between the
    init and the link: 412, and the key has no version;
  - two `If-None-Match: *` rgw-go writers of a fresh key: one succeeds, the other answers 412
    with its instance gone; the same two past the bound, both inited without the pending check:
    at most one commits, and every 412 leaves no instance;
  - **"radosgw's noncurrent transition leaves a pending attr"** (review N2, a mixed fleet): the
    radosgw-shaped writer links a noncurrent version at its old epoch and leaves its pending attr
    with no log entry. A correct conditional write right after (If-Match on the current ETag)
    succeeds once the bound passes; an incorrect one (`If-None-Match: *` on the existing key)
    answers 412; neither answers 409;
  - If-Match on a pre-versioning object: the converted entry at epoch 1 does not void the write;
  - `ExpectTag`: a mismatch before init writes nothing; a write landing before init fails the
    init's guard and the retry refuses; the own replay's removal uses it; on a missing head an
    empty `ExpectTag` inits through the exclusive create, a head created first makes the retry
    refuse, and a non-empty one refuses with nothing written.
- **Exclusions:** none of its own: V7a writes D1's, which states this task's guarantee.
- **Depends on:** V5b. **PR split:** one. **Brief:** about 205 lines.

### Task V6: driver, reads follow the OLH; attrs on a version

- **Layer:** driver. **Files:** modify `internal/driver/stat.go`, `read.go`, `attrs.go` and
  `internal/op/object.go` (`ObjectState`); specs; edit `docs/differences.md`. **Cluster:** no.
- **Consumes:** V4's `followOLH`, V5a's `getInstanceEntry`, V5b's `updateOLH`. **Produces:**
  `ObjectState.DeleteMarker bool`; after a follow `ObjectState.Key` is the resolved version's
  key, so `Key.Instance` is the version id (the report's `ObjectState.Instance`, N20).
- **Behaviour:**
  1. `StatObject` and `PrefetchObject` are `get_obj_state_impl`
     ([rgw_rados.cc:6260-6437](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L6260-L6437),
     [rgw_rados.cc:6874](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6874)): the head's
     `olh.idtag` is kept as the OLH tag; a head with `olh.ver` and no instance named follows to
     the target and returns its state ([rgw_rados.cc:6238-6258](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L6238-L6258),
     [rgw_rados.cc:6852](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6852)). When the
     follow ran the update for a pending attr, the head is read once more and the target resolved
     from that read, without a second update (VD-11): radosgw resolves from the head as read
     before the update. The refusal of a head carrying `olh.ver`, in `decodeState`, which
     `readHead` calls, goes.
  2. A current delete marker: `Exists` false, `DeleteMarker` true. `versionId=null` on an OLH
     head without a manifest: not found ([rgw_rados.cc:6429-6433](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L6429-L6433)).
     A named instance reads `_:<instance>_<name>` without following.
  3. D2's lookup: a named version whose head is missing, `null` included, is looked up through
     `getInstanceEntry`, so a null delete marker is found behind the version marker (review F4);
     a delete marker's entry sets `DeleteMarker`. Only this path pays for it.
  4. `SetObjectAttrs` on the resolved version is `set_attrs`
     ([rgw_rados.cc:6779-6943](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L6779-L6943),
     [rgw_rados.cc:7393](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7393)): `null` cleared, a
     null version without a manifest not found, the version head's write-tag guard, a fresh tag,
     prepare and complete ADD on the instance key with the state's size and the attrs' ETag,
     content type and storage class, the mtime kept. The class's complete resets the instance
     entry to `FLAG_VER` while the list entry keeps CURRENT, which is radosgw's state too. The
     resolution follows the OLH as `modify_obj_attrs` does
     ([rgw_sal_rados.cc:2402-2420](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_sal_rados.cc#L2402-L2420),
     [rgw_sal_rados.cc:2994](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L2994)).
- **Specs** (on the emulator, V3a and V3b): follow to the current version; a current marker; a named
  marker (D2) and a named missing version; null present and absent; a pending log entry a stopped
  writer left is applied by `StatObject`; a read racing the deletion of the current version, whose
  writer stopped between its unlink and its replay, answers the next version, not not-found
  (VD-11); attrs on a noncurrent version leave the current untouched;
  the prefetch's head bytes are the target's; `versionId=null` naming a null delete marker sets
  `DeleteMarker` (D2's 405 for it is V14a's).
- **Exclusions:** a new exclusion, "A read that replays a pending modification answers the version
  current after it" (VD-11), citing the registry's "radosgw's follow_olh answers the version that
  was current before the replay it runs".
- **Depends on:** V5b. **PR split:** one. **Brief:** about 180 lines.

### Task V7a: driver, versioned PutObject

- **Layer:** driver. **Files:** modify `internal/driver/put.go`, `headwrite.go`; create
  `internal/driver/put_versioned.go` and specs; edit `docs/differences.md` and
  `docs/ceph-upstream-bugs.md`. **Cluster:** no.
- **Review:** task review.
- **Consumes:** V2, V5a's `completeSync`, V5b's `setOLH`, V5c's condition rules, V6's follow (the
  current version's state). **Produces:** the head write's OLH epoch (0: the class assigns; nonzero:
  C11's transition) and the attrs-finalizing call, one per head write, given the bucket record, the
  mtime the write stamps and whether the head write creates the object (radosgw's `PUT_OBJ_CREATE`),
  an identity until L5a fills it.
- **Behaviour:**
  1. The key carries the instance the op chose (V11): non-empty on an enabled bucket, empty
     otherwise.
  2. `versioned_op` ([rgw_rados.cc:3312-3319](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L3312-L3319),
     [rgw_rados.cc:3433-3439](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L3433-L3439)): versioning enabled, the head at the
     key's oid an OLH, an instance, or a nonzero epoch. A versioned op sets VERSIONED_OP on the
     index op.
  3. Conditions (VD-2): on a versioned op `If-Match` and `If-None-Match` are evaluated against the
     current version, followed through the OLH, a current delete marker counting as no object
     ([rgw_rados.cc:7238-7258](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7238-L7258),
     [rgw_rados.cc:7260-7329](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7260-L7329), called at
     [rgw_rados.cc:3282-3297](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L3282-L3297)), on both releases. The id-tag guard
     applies only to an instance-less write ([rgw_rados.cc:3298-3300](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L3298-L3300)),
     which phase 1's `guardHead` already carries (`internal/driver/headwrite.go:315-349`), now given
     the current version's state. v19.2.7 evaluates and guards the same way
     ([rgw_rados.cc:3185-3205](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L3185-L3205), through
     [rgw_rados.cc:6624-6644](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L6624-L6644)
     and
     [rgw_rados.cc:6646-6715](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L6646-L6715));
     only v19.2.6, below the floor, evaluated against the new instance (defect 3, fixed by
     ce98cebb815).
  4. Prepare ADD on the instance key; the head at the instance oid. On an OLH head the write sets
     `olh.idtag` again ([rgw_rados.cc:3223-3225](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L3223-L3225),
     [rgw_rados.cc:3321](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L3321)); the reset keeps `user.rgw.olh.*`
     (`internal/driver/headwrite.go:206-216`).
  5. `completeSync` (D5), then `setOLH` with no marker, no meta, the epoch, and the write's
     condition when it has one, under V5c's rules, the post-link check included (D1)
     ([rgw_rados.cc:3369-3375](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L3369-L3375),
     [rgw_rados.cc:3492](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L3492)). Every S3 write sends epoch 0
     ([rgw_op.h:2117-2143](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.h#L2117-L2143)).
  6. A suspended bucket's key with an OLH becomes the null version: instance empty, the link's
     key the null instance, the head the OLH head. One without an OLH is a plain write with no
     link. A failed head write cancels the index op as in phase 1
     ([rgw_rados.cc:3401-3448](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L3401-L3448)).
  7. The driver serves every bucket; object-lock buckets stay refused at the op (VD-6).
- **Specs** (on the emulator, V3a and V3b):
  - a new key on an enabled bucket: instance, list and OLH-data entries, the OLH head's xattrs,
    `olh.ver` "2"; an overwrite: epochs 2 and 3, the first demoted;
  - the first versioned write over a pre-versioning object: the plain entry becomes the null
    version at epoch 1 with a version marker at the plain name, its head keeping its data and
    gaining the OLH xattrs;
  - a suspended null version over an OLH head (the old null data's tails to the GC) and a
    suspended write without an OLH;
  - the condition table on versions under both releases' semantics, and D1's PutObject races:
    two `If-None-Match: *` puts interleaved, one 412 with its instance removed; an
    `If-None-Match: *` put with a radosgw-shaped PUT initing between its init and its link, under
    both link orders (412 when the radosgw-shaped one links first, success when it links second);
  - **"a versioned write never links before its completion's reply"**: a hook holds the
    completion's reply; no link reaches the shard until it is released;
  - an explicit epoch below the current links the version non-current;
  - the attrs-finalizing call runs once per head write with the record, the mtime and the create
    flag.
- **Exclusions and registry:** a new exclusion, "A conditional write to a versioned bucket commits
  only if its condition held when it linked", stating D1 exactly: the condition is evaluated on the
  OLH head; rgw-go's conditional writers serialize on the head's pending attrs, up to VD-4's bounded
  backoff, after which they proceed without that wait; after its link the writer lists the key's
  versions and, when another version or marker took an epoch in between or the version an If-Match
  matched is gone, unlinks its own version and answers 412. A reader can see the voided version
  between its link and its removal; two racing conditional writers may both answer 412; a pending
  attr no writer will remove delays the key's conditional writes by the bound. radosgw evaluates
  once and links unconditionally (defect 4); none for VD-2 on Squid, since v19.2.7 evaluates
  conditions against the current version as v20.2.4 does (ce98cebb815); registry entries for defect
  3 (S3-correctness, v19.2.6 only, below the floor, fixed in v19.2.7) and defect 4 (S3-correctness
  race, both, unreproduced, not reproduced by rgw-go under D1).
- **Depends on:** V5c, V6. **PR split:** one. **Brief:** about 245 lines.

### Task V7b: driver, versioned CopyObject and CompleteMultipartUpload

- **Layer:** driver. **Files:** modify `internal/driver/copy.go`, `mp_complete.go` and specs.
  **Cluster:** no.
- **Review:** task review.
- **Consumes:** V7a's versioned head write and its V5c condition rules, V6's resolved source state.
- **Behaviour:**
  1. CopyObject's destination takes V7a's path with the instance the op chose, for the shared-tail
     and the streamed copy alike ([rgw_op.cc:5658-5679](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L5658-L5679);
     [rgw_sal_rados.cc:2687-2717](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_sal_rados.cc#L2687-L2717) sets the epoch on the write;
     the destination instance at [rgw_op.cc:6142](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L6142)). The source is the state the op
     read through V6, a named version or the current one. The OLH xattrs stay stripped from a copy
     into a bucket that is not versioning-enabled (`internal/driver/copy.go:149-152`).
  2. CompleteMultipartUpload's assembled head takes V7a's path
     ([rgw_sal_rados.cc:3636](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_sal_rados.cc#L3636); the instance at
     [rgw_op.cc:6462-6469](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L6462-L6469), [rgw_op.cc:7288](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7288)), with its
     condition under D1. `attrMPCompletionInstance` carries the instance
     (`internal/driver/mp_complete.go:351`, `internal/driver/mp_complete.go:397`). The
     versioning refusal at `internal/driver/mp_complete.go:269-271` goes. Parts are never
     versioned ([rgw_putobj_processor.cc:372](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_putobj_processor.cc#L372)).
- **Specs** (on the emulator): a copy with a source version id; a copy into an enabled bucket makes
  a new version; a copy whose source's current is a delete marker is NoSuchKey; a shared-tail copy
  of a version takes references under the new instance's tag; a completion on an enabled bucket
  makes a version and records its instance; a completion with `If-None-Match: *` against a current
  version answers 412 and leaves no instance.
- **Exclusions:** none of its own (V11 retires "CompleteMultipartUpload sends no
  x-amz-version-id").
- **Depends on:** V7a. **PR split:** one. **Brief:** about 150 lines.

### Task V8a: driver, versioned DeleteObject

- **Layer:** driver. **Files:** modify `internal/driver/delete.go`, `internal/op/object.go`
  (the `DeleteObject` signature, `DeleteParams`, `DeleteResult`), memstore's delete, and the
  regenerated `opfakes` and `driverfakes`; create `internal/driver/delete_versioned.go` and specs;
  edit `docs/differences.md` and `docs/ceph-upstream-bugs.md`. **Cluster:** no.
- **Consumes:** V2, V5a's `getInstanceEntry`, V5b's `setOLH` and `unlinkObjInstance`, V5c's
  condition rules, V6. **Produces:** the Seams table's `DeleteObject`, `DeleteResult` and
  `DeleteParams` fields (`MarkerOwner`, `SkipOLHUpdate`, and the `Expect` field, whose behaviour
  is V8c's).
- **Behaviour:**
  1. The versioned branch runs when the bucket is versioned, enabled or suspended
     ([rgw_rados.cc:5833-5968](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L5833-L5968),
     [rgw_rados.cc:6445-6577](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6445-L6577)). A never-versioned bucket keeps
     phase 1's path. The refusals at the top of `DeleteObject` go.
  2. The branch is chosen on the request's instance as it arrived, before any clearing, as radosgw
     tests the original key ([rgw_rados.cc:5841](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L5841), tested at
     [rgw_rados.cc:5853](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L5853);
     [rgw_rados.cc:6453](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6453),
     [rgw_rados.cc:6465](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6465)). A request without a `versionId` writes a
     delete marker. A request with one, `null` included, removes that version and never writes a
     marker; `null` becomes the empty instance only for the index ops and the head read that follow
     ([rgw_rados.cc:5844-5846](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L5844-L5846),
     [rgw_rados.cc:6456-6458](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6456-L6458)).
  3. Without a `versionId`: the marker's instance is random unless the bucket is suspended, where
     it is the null marker ([rgw_rados.cc:5853-5869](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L5853-L5869)); the result is
     the instance or `null` with `DeleteMarker` true; the marker's meta carries `MarkerOwner` and
     the delete time ([rgw_rados.cc:5871-5880](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L5871-L5880)). The conditions are
     checked against the current version ([rgw_rados.cc:6494-6509](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6494-L6509),
     VD-2) and go into `setOLH` as its condition, so V5c's rules, the post-link check included,
     make a conditional marker atomic; then `setOLH` as a marker with `UnmodifiedSince`
     ([rgw_rados.cc:5899-5901](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L5899-L5901)). A marker on a marker is allowed.
  4. With a `versionId`: the version's entry through `getInstanceEntry`
     ([rgw_rados.cc:5908](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L5908),
     [rgw_rados.cc:10192](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L10192)): one `bi_get` for
     a named instance, and for `null` V5a's three reads in `read_key_entry`'s order, so a
     converted key yields its null version, data or marker, never the plain version-marker entry
     (review F4). ENOENT is NoSuchKey. The conditions are checked against that entry's meta
     ([rgw_rados.cc:5913-5942](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L5913-L5942),
     [rgw_rados.cc:6520-6554](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6520-L6554)). The result is the version id, `null`
     for the null version, with `DeleteMarker` the entry's. Then `unlinkObjInstance`
     ([rgw_rados.cc:5945-5952](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L5945-L5952)), whose replay removes a data head.
     radosgw reads `null` through one `bi_get` of the plain key, so on a converted key it compares
     the conditions with the version marker's empty meta and reports `DeleteMarker` false for a
     null delete marker (defect 16).
  5. `SkipOLHUpdate` passes `SkipUpdate` (VD-8).
- **Specs** (on the emulator):
  - a marker on an enabled and on a suspended bucket (random and `null` ids); a marker on a
    marker; deleting a noncurrent version, the current one (the next promoted), the last one (the
    OLH removed), a marker's instance (the marker gone, `DeleteMarker` true in the result), the
    null version; a missing instance; the conditions on both paths under both releases'
    semantics; `SkipOLHUpdate` leaving the pending attrs;
  - **"`versionId=null` never writes a marker"**: on an enabled and on a suspended bucket, with a
    null version and without one (NoSuchKey);
  - **"`versionId=null` on a converted key"**: If-Match with the pre-versioning object's ETag
    succeeds and a wrong one answers 412; If-Match, size and last-modified of a listed null version
    all pass, the case C10b's noncurrent expiration needs; a null delete marker's removal reports
    `DeleteMarker` true;
  - a conditional marker with a radosgw-shaped writer linking between its init and its link
    (V5c): 412 and no marker.
- **Exclusions and registry:** D8's amendment of both `bi_*` sentences in `docs/differences.md`
  (N11 of the notes), with D8's widened wording; the rgw-rs session is told, as
  `docs/differences.md` requires. A registry entry for defect 16 (S3-correctness, both floors,
  unreproduced) and an exclusion, "A versioned delete of versionId=null checks the null version
  itself".
- **Depends on:** V5c, V6. **PR split:** one. **Brief:** about 220 lines.

### Task V8c: driver, `DeleteParams.Expect`

- **Layer:** driver. **Files:** modify `internal/driver/delete_versioned.go` and specs; edit
  `docs/differences.md`. **Cluster:** no.
- **Consumes:** V8a, V5b's `errRemovalRefused` and its old-epoch link, V5c's `ExpectTag`.
  **Produces:** `Expect`'s behaviour as the Seams table gives it, consumed by L4 and C10b.
- **Behaviour:**
  1. `Expect` is one check, the head's write tag: `user.rgw.idtag` must still hold the bytes
     `Expect` read, an absent xattr reading as empty (string-mode `cmpxattr` EQ matches an absent
     xattr to ""). A mismatch is `op.ErrConcurrentModification`.
  2. With a `versionId`: the version's head is checked before the unlink, and a mismatch writes
     nothing. The unlink then passes `Expect`'s bytes as `olhUnlink.ExpectTag`, so the own
     replay removes the head only under them.
  3. Without a `versionId` on a suspended bucket, `Expect` names the null version the null marker
     replaces. Its data is colocated with the OLH head (§2.1), and the class unlinks its list entry
     and logs its removal when the null marker links ([cls_rgw.cc:1705-1734](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1705-L1734),
     [cls_rgw.cc:1808-1810](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1808-L1810); [cls_rgw.cc:1846-1875](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1846-L1875),
     [cls_rgw.cc:1949-1951](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1949-L1951)). `Expect`'s bytes go in as `setOLH`'s `ExpectTag`
     (V5c): each attempt checks the head it read, and the attempt's init op, the head op of the
     marker's write that carries its pending attr, compares them too. A null write that landed first
     is refused, not retried over.
  4. A refused removal (review F3). The own replay's guarded removal finds the head rewritten (a
     retention, an ACL, a new null write) after the unlink or the marker's link. The class has
     then removed the version's list entry, while its instance entry and head stay
     ([cls_rgw.cc:1867-2018](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1867-L2018), [cls_rgw.cc:2002-2159](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2002-L2159);
     V5b's `errRemovalRefused`). Left so, the version is readable and deletable by its id but
     absent from ListObjectVersions and from lifecycle's listings. V8c acts only on a final
     refusal, `errRemovalRefused` returned without an error (V5b B5). A refusal returned with an
     error leaves the removal in the log, where a later replay can still remove the version, so
     V8c re-links nothing and returns that error: a re-link then would list a version whose head
     that replay removes. V4's not-final, not-retryable outcome, the writer's entries kept in the
     log of an OLH entry another writer's link re-tagged, is the same: V8c re-links nothing, and
     that writer's replay removes the version consistently, which is behaviour 5's second window.

     A final refusal does not prove the version survived either (re-review 2, Important 2).
     Another reader's replay, or a radosgw gateway's, that read the log after the unlink removes
     the version guarded only on the write tag it reads itself, and that removal can land between
     the own refusal and the own head op. So before any re-link, on either path, V8c confirms
     through `getInstanceEntry` that the version's instance entry is still there; when it is
     gone, V8c re-links nothing. Otherwise it re-links the version with `setOLH`, not a marker:
     - when it was noncurrent, at its own `versioned_epoch`: the class writes it back non-current
       with its list entry (V5b B2);
     - when it was current, at epoch 0, so it is current again at a fresh epoch, and the version
       the unlink promoted is demoted. V4's init guard holds the `olh.info` bytes the unlink's
       replay left and no pending attrs. If another writer has moved the head since, the re-link
       falls back to the version's own epoch, non-current, behind that writer's version, after
       first resetting the version's instance record (review N1). The unlink left that record
       flagged CURRENT, and the old-epoch link's `write(epoch, false)` adds `FLAG_VER` without
       clearing anything ([cls_rgw.cc:1410-1427](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1410-L1427),
       [cls_rgw.cc:1537-1554](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1537-L1554)), so it would write a second list entry of the
       name flagged current. The reset is the index ADD V6's `SetObjectAttrs` performs on an
       instance key, prepare then complete with the entry's own meta as `getInstanceEntry`
       re-reads it: the class's complete keeps only `FLAG_VER` and writes the instance key alone
       ([cls_rgw.cc:1062-1064](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1062-L1064), [cls_rgw.cc:1197-1199](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1197-L1199)).
       The invariant either way: exactly one entry of the name is flagged current.
     - on the null-marker path, the null data entry re-links the same way, the reset included,
       since the marker's link left the null data record flagged current too, and the class's
       null pair then removes the marker's list and instance entries. One record, `(name, "")`,
       serves every null write, so this path has a guard step the named path does not (review
       N4): before the reset and the fallback, V8c re-reads the null data record through
       `getInstanceEntry`. When its `versioned_epoch` is no longer the one V8c read when the
       removal was refused, a newer null write owns the key and is already listed and current;
       V8c resets and re-links nothing. A versioned write of a new instance leaves that epoch
       unchanged, so it still takes the reset and the fallback. A refused named version is
       re-listed only by V8c, so the named path needs no epoch check; the existence check above
       covers both paths.
     The delete then answers `op.ErrConcurrentModification`.
  5. Two windows stay, both radosgw's own: a writer that inits after the epoch-0 re-link's init
     and links before it ends below the restored version; and a replay by another reader, which
     does not know `Expect`, removes what it reads, as radosgw's does. The second lasts until the
     writer's own replay has landed with its trim or clear (V4's `olhIndex` seam): it covers a
     replay by another reader running alongside the own one, and one after the own head op failed
     and its retry never landed. The own replay runs straight after its link. A race stays too,
     rgw-go's own since radosgw never re-links. A replay running alongside the own one, which read
     the own entries before the own tail dropped them, can land its removal at any time after the
     existence check, before the re-link's link or after it, and leave a list entry for a version
     with no head. The race lasts until that replay's removal lands, which V8c cannot bound.

     A replay of a re-tagged OLH entry's kept pending log (V4's `olhIndex` seam) adds no case. A
     re-tag needs `pending_removal`, which only the unlink of the last version sets
     ([cls_rgw.cc:1966](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1966),
     [cls_rgw.cc:2113](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2113)),
     and an init with fresh tags, which needs the head gone. The first replay to land on that
     fresh head reads from marker 0, rgw-go's for want of an `olh.ver` (VD-13) and radosgw's always,
     and runs its removals before its head op and its trim. So a replay that later reads from
     above the own entries, or trims them, comes after one that removed the version, and the
     existence check finds it gone.

     L4 and C10b judge these against their threat models.
- **Specs** (on the emulator):
  - a mismatch before the unlink: nothing written;
  - **"a refused removal leaves the version listed"**: an attr write lands between the unlink
    and the removal. The delete answers `op.ErrConcurrentModification`; the head (with its new
    write tag), the instance entry and the list entry all exist; no pending attr; the log
    trimmed; ListObjectVersions lists it. A noncurrent version is back at its original epoch with
    `olh.info` unchanged. A current one is current again at a fresh epoch, with the version the
    unlink promoted demoted and `olh.info` on the restored one;
  - a refusal returned with an error (the same, with a trim that keeps failing): no re-link, the
    delete returns the error, and the removal stays in the log;
  - **"another replay removes the refused version before the re-link"** (re-review 2, Important
    2): a replay alongside, by a reader that read the log after the unlink, removes the version
    between the own refusal and the own head op. The own replay returns a final refusal;
    `getInstanceEntry` finds no instance entry, so V8c re-links nothing, no list entry is
    written for a version with no head, and the delete answers `op.ErrConcurrentModification`;
  - **"exactly one current entry after a fallback re-link"** (review N1): the same with a version
    another writer links and applies between the unlink and the re-link. That version stays
    current; the refused one is listed non-current at its own epoch; exactly one entry of the name
    carries `FLAG_CURRENT`; the refused version's instance record carries `FLAG_VER` only; a
    listing without versions shows the key once, as the other writer's version. The same on the
    null-marker path;
  - **"a second null write during the null-marker fallback keeps the key current"** (review N4):
    on the null-marker path, null PutObject P1 lands between the marker's init and its link, the
    removal is refused, and null PutObject P2 links and applies before the epoch-0 re-link. The
    delete answers `op.ErrConcurrentModification` without resetting or re-linking; P2's list
    entry stays at its epoch, flagged current; exactly one entry of the name carries
    `FLAG_CURRENT`; `olh.info` names P2; a listing without versions shows the key once, as P2;
  - **"a null write between the expectation and the null marker"**: on a suspended bucket whose
    current version is the null version, the caller reads it as `Expect`; a suspended PutObject
    of the same key lands (its head written, completed and linked) before the delete's init; the
    delete answers `op.ErrConcurrentModification`, no marker is linked, and the new null version
    survives with its head, instance and list entries. The same with an attr-only write
    (PutObjectAcl on the null version, which draws a fresh write tag);
  - the null write landing after the marker's init and before its link, under both link orders:
    the delete answers `op.ErrConcurrentModification`, and the new null version ends current with
    its head, instance and list entries, and no marker.
- **Exclusions:** "A null delete marker can be conditional on the null version it replaces" and
  "A refused versioned removal re-links the version, unless a newer null write owns the null
  key": radosgw has no such check; the exclusion
  names the two windows of behaviour 5.
- **Depends on:** V8a, V5c. **PR split:** one. **Brief:** about 225 lines.

### Task V8b: driver, the admin removals on versioned buckets

- **Layer:** driver. **Files:** modify `internal/driver/purge.go`, `bucketadmin.go` and specs.
  **Cluster:** no.
- **Consumes:** V8a's `DeleteObject`, V9's marker instance.
- **Behaviour:**
  1. The object removal deletes the plain key with the bucket's versioning status, so a versioned
     bucket gets a delete marker, with an empty owner: `RadosObject::delete_object` sets none
     ([rgw_bucket.cc:148-161](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_bucket.cc#L148-L161),
     [rgw_bucket.cc:149-162](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_bucket.cc#L149-L162);
     [rgw_sal_rados.cc:2928-2949](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_sal_rados.cc#L2928-L2949),
     [rgw_sal_rados.cc:3766-3787](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L3766-L3787)).
  2. The purges list with versions, unordered, and remove each entry by its exact version
     ([rgw_sal_rados.cc:362-364](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_sal_rados.cc#L362-L364),
     [rgw_sal_rados.cc:379-381](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L379-L381)): a version or a marker by its
     instance, and the empty instance of a versioned entry as `null`, so a purge never writes a
     marker and ends with an empty index. The task reads how radosgw's listing reports a null
     version's instance and records any difference as an exclusion.
  3. Object lock is not checked on either path (P2, the owner, 2026-10-13).
- **Specs** (on the emulator): the object removal on an enabled, a suspended and a lock bucket; a
  purge of a bucket holding versions, markers, a null version and a pre-versioning object leaves no
  index entry and no head, paging a key whose versions span pages.
- **Exclusions:** none here; V12b, which serves the routes, writes them.
- **Depends on:** V8a, V9. **PR split:** one. **Brief:** about 120 lines.

### Task V9: driver, listings with versions

- **Layer:** driver. **Files:** modify `internal/driver/list.go`, `internal/op/bucket.go` and
  specs; edit `docs/differences.md`. **Cluster:** no.
- **Consumes:** V0's `Reply`, V3a's emulated list, V5a's `getInstanceEntry`. **Produces:**
  `ListObjectsParams.MarkerInstance`, `ListObjectsResult.NextMarkerInstance`,
  `ObjectEntry.VersionedEpoch`, `ObjectEntry.Category`.
- **Behaviour:**
  1. The marker's name and instance reach the class's start key on the ordered and the
     unordered listing; `null` is the empty instance. The class resolves it
     ([cls_rgw.cc:358-390](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L358-L390), [cls_rgw.cc:408](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L408)): a key
     marker without a version lists that key's versions again, as S3 does; a stale version id
     skips the rest of its key.
  2. The next marker is the last entry visited, name and instance
     ([rgw_rados.cc:2011-2014](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L2011-L2014)); the unordered listing's likewise
     ([rgw_rados.cc:2194](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L2194),
     [rgw_rados.cc:2284](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L2284)).
  3. Advance-and-retry: on -EFBIG the shard is listed again from the marker in V0's reply
     ([cls_rgw_client.cc:99-110](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw_client.cc#L99-L110),
     [cls_rgw_client.cc:161-166](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw_client.cc#L161-L166);
     [svc_bi_rados.cc:948-965](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/services/svc_bi_rados.cc#L948-L965)).
  4. `IsLatest`, `DeleteMarker` and `Key.Instance` are filled already
     (`internal/driver/list.go:241-262`); `VersionedEpoch` and `Category` are copied from the
     entry for lifecycle.
  5. A marker instance of `null` whose null version is a delete marker: the class resolves
     `null` to the null data key only and, finding none, starts past every version of the key
     ([cls_rgw.cc:358-390](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L358-L390), [cls_rgw.cc:408](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L408)), so radosgw
     resumes a page that ended on a null marker after the key (defect 17). rgw-go asks
     `getInstanceEntry` whether `null` is a marker; if it is, it lists from the key without an
     instance and skips the entries up to and including that marker.
- **Specs** (on the emulator, V3a's list): markers without an instance, with `null`, with an
  existing and with a stale instance; an unordered listing with versions paging inside one key; a
  non-versions listing over more than eight reads of markers and noncurrent versions completes;
  `NextMarkerInstance` on a marker entry; a page ending on a null delete marker resumes at that
  key's next version.
- **Exclusions and registry:** retires "A shard that answers advance-and-retry fails an ordered
  listing" (with V0); a new exclusion and a registry entry for defect 17 (S3-correctness, both
  floors, unreproduced).
- **Leads V9 settles** (V3a's review, not yet traced past the class): V9's brief traces both at
  v19.2.7 and v20.2.4, registers what is a defect, and decides what rgw-go's `checkDiskState`
  suggests for a version's list entry. V5a's D5, a versioned write awaiting its completion
  before its link, is why rgw-go's own writes leave neither.
  1. A listing calls `check_disk_state` for any entry it lists with a pending map
     ([rgw_rados.cc:10019-10031](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L10019-L10031),
     [rgw_rados.cc:10279-10287](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L10279-L10287);
     [rgw_rados.cc:10743-10755](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L10743-L10755),
     [rgw_rados.cc:11005-11013](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L11005-L11013)),
     and `check_disk_state` suggests it under the listed entry's own key, with no case for a
     version
     ([rgw_rados.cc:10519-10671](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L10519-L10671),
     [rgw_rados.cc:11247-11399](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L11247-L11399)).
     `rgw_dir_suggest_changes` applies the change under `encode_obj_index_key`
     ([cls_rgw.cc:2249-2250](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L2249-L2250),
     [cls_rgw.cc:2419-2420](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2419-L2420)),
     which for a version is its instance key
     ([cls_rgw.cc:339-346](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L339-L346),
     [cls_rgw.cc:389-396](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L389-L396)).
     So a suggestion for a version reaches only its instance entry, never the list entry radosgw
     listed, and a list entry carrying a pending map may never be repaired.
  2. A null version's list entry has an empty instance, so the same `encode_obj_index_key`
     keys its suggestion to the plain key, which on a converted name is the version marker.
     Whether the class then updates or removes the marker, given its index version check and
     the marker's state
     ([cls_rgw.cc:2309-2316](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L2309-L2316),
     [cls_rgw.cc:2479-2486](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2479-L2486)),
     is untraced.
- **Depends on:** V0, V3a, V5a. **PR split:** one. **Brief:** about 175 lines.

### Task V10: op and s3, PutBucketVersioning and GetBucketVersioning

- **Layer:** op and handler. **Files:** create `internal/op/bucketversioning.go`,
  `internal/s3/bucketversioning.go` and specs; bind the two routes in S2's versioning route-set
  file; add `Request.MFAVerified bool` to `internal/op/request.go` (false until V13d sets it);
  edit `docs/differences.md`; then `hack/admin/deselect-phase2.txt` and the admin ledgers.
  **Cluster:** yes, for the recording PR only.
- **Security boundary:** the document parser and the MFA rules face clients; adversarial review
  applies to them (PP-14).
- **Consumes:** S2, V2's helpers, phase 1's `RetryRacedBucketWrite`, `verifyBucketAction` and
  `PutBucketInfo`. **Produces:** the object-lock refusal L2 relies on.
- **Behaviour:**
  1. Permission: bucket tags into the environment when a policy asks, then
     `s3:PutBucketVersioning` ([rgw_op.cc:2759-2770](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L2759-L2770),
     [rgw_op.cc:2991](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L2991)).
  2. The document: the body up to `rgw_max_put_param_size`. An unparsable body, or one without
     `VersioningConfiguration`, is 400 MalformedXML, AWS's answer under D16 (global: where the
     status or error code a client sees differs, rgw-go follows AWS). radosgw answers -EINVAL,
     400 InvalidArgument, on both paths ([rgw_rest_s3.cc:2228-2239](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2228-L2239),
     [rgw_rest_s3.cc:2324-2335](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2324-L2335)). `Status` must be Enabled or Suspended and
     `MfaDelete` Enabled or Disabled; radosgw answers InvalidArgument for any other value
     ([rgw_rest_s3.cc:2179-2269](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2179-L2269), [rgw_rest_s3.cc:2307](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2307)). The
     implementer confirms AWS's code for an invalid value from AWS's PutBucketVersioning
     documentation and follows it under D16, quoting it in the task notes (PP-2).
  3. Execute ([rgw_op.cc:2777-2860](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L2777-L2860), [rgw_op.cc:3009](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L3009)):
     NoSuchBucket; on an object-lock bucket any status but Enabled is 409 InvalidBucketState,
     "bucket versioning cannot be disabled on buckets with object lock enabled"
     ([rgw_op.cc:2788-2793](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L2788-L2793), [rgw_op.cc:3020-3025](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L3020-L3025)). MFA
     ([rgw_op.cc:2795-2817](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L2795-L2817), [rgw_op.cc:3032-3046](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L3032-L3046)): an
     `MfaDelete` equal to the bucket's flag is dropped; changing it needs `MFAVerified`; with MFA
     on, flipping Enabled and Suspended needs it too; the refusal is 403 AccessDenied
     (`-ERR_MFA_REQUIRED`, [rgw_common.cc:96](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L96)).
  4. Under `RetryRacedBucketWrite`: the MFA flag set or cleared; Enabled sets `BUCKET_VERSIONED`
     and clears `BUCKET_VERSIONS_SUSPENDED`; Suspended sets both; `PutBucketInfo` writes the
     instance with its cache notify ([rgw_op.cc:2828-2849](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L2828-L2849)). No index entry
     changes.
  5. GetBucketVersioning ([rgw_op.cc:2729-2757](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L2729-L2757), [rgw_op.cc:2979](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L2979)):
     `s3:GetBucketVersioning`; NoSuchBucket; `Status` and `MfaDelete` only when the bucket was
     ever versioned, else an empty `VersioningConfiguration`
     ([rgw_rest_s3.cc:2160-2177](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2160-L2177), [rgw_rest_s3.cc:2255](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2255)).
  6. Routes: the existing `get_bucket_versioning` and `set_bucket_versioning` rows
     (`internal/s3/dispatch.go:131`, `internal/s3/dispatch.go:161`, the signed-payload form at
     `internal/s3/dispatch.go:319`).
- **Specs:** the parse table (MalformedXML for an unparsable body and for a missing
  `VersioningConfiguration`); the flag transitions from each state; the lock refusal; the MFA
  table (unchanged value dropped, change unverified refused, flip with MFA on unverified refused,
  verified accepted); the three GET documents; the cache notify sent; the actions through the
  policy evaluator.
- **Exclusions:** retires "PutBucketVersioning answers 501 until a later phase"; a new one,
  "PutBucketVersioning answers MalformedXML for a document that does not parse": radosgw answers 400
  InvalidArgument from `get_params`' -EINVAL ([rgw_rest_s3.cc:2228-2239](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2228-L2239),
  [rgw_rest_s3.cc:2324-2335](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2324-L2335)), and rgw-go answers AWS's 400 MalformedXML (D16). The
  same entry covers invalid `Status` and `MfaDelete` values if AWS's code for them differs.
- **Depends on:** S2, V2. **PR split:** (1) op and s3; (2) the recording: the `enable_versioning`
  line out of `hack/admin/deselect-phase2.txt`, both admin ledgers re-recorded with
  `make admin-record`, landed through the controller. **Brief:** about 190 lines.

### Task V11: op and s3, versioned writes

- **Layer:** op and handler. **Files:** modify `internal/op/putobject.go`, `copyobject.go`,
  `completemultipart.go`, the append op; create `internal/op/version.go`; modify
  `internal/s3/putobject.go`, `copyobject.go`, `multipart.go` and specs; edit `docs/differences.md`
  and `docs/ceph-upstream-bugs.md`. **Cluster:** no.
- **Consumes:** V7a, V7b, V2. **Produces:** `func VersionForWrite(r *Request) string`.
- **Behaviour:**
  1. `VersionForWrite`: a new instance when the bucket's versioning is enabled, else empty; the
     request's `versionId` is ignored on PutObject, on CopyObject's destination and on
     CompleteMultipartUpload (D3)
     ([rgw_op.cc:4278-4285](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L4278-L4285), [rgw_op.cc:4495](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4495); copy
     [rgw_op.cc:5576-5580](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L5576-L5580), [rgw_op.cc:6142](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L6142); completion
     [rgw_op.cc:6462-6469](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L6462-L6469), [rgw_op.cc:7288](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7288)). POST object calls
     it (BC-32).
  2. An append to a bucket that was ever versioned is 409 InvalidBucketState
     ([rgw_op.cc:4268-4272](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L4268-L4272), [rgw_op.cc:4481-4485](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4481-L4485)).
  3. Headers (D4): PutObject and CompleteMultipartUpload send `x-amz-version-id`, `null` when the
     bucket was ever versioned and the instance is empty, nothing on a never-versioned bucket;
     radosgw sends only a non-empty id ([rgw_rest_s3.cc:2756-2762](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2756-L2762),
     [rgw_rest_s3.cc:4111](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4111); [rgw_rest_s3.cc:2912-2920](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2912-L2920),
     [rgw_rest_s3.cc:4609](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4609)). CopyObject sends `x-amz-version-id` for the
     destination and `x-amz-copy-source-version-id` for the source read, by the same `null`
     rule; radosgw sends neither ([rgw_rest_s3.cc:3596-3633](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3596-L3633),
     [rgw_rest_s3.cc:3873](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L3873)).
  4. The 501 gates (N21) narrow to object lock (VD-6) on these ops; this task adds the narrowed
     gate, and later op tasks switch their own call sites to it. Whichever of V11 and BC-32 lands
     second switches `internal/op/postobject.go` to it.
- **Specs:** `VersionForWrite` on never-versioned, enabled and suspended buckets, with and
  without a `versionId`; the append refusal; the header table for each op and state; a copy from
  a named source version; a lock bucket still 501.
- **s3-tests:** tests for D3 and D4 (the copy headers, `null` on a suspended PUT) go into
  jhoblitt/ceph's `rgw-go-s3tests` branch; the pin bump is its own change.
- **Exclusions and registry:** new exclusions for D3 and D4's write side; "Writes to a versioned
  or object-lock bucket answer 501" narrowed to object lock; "CompleteMultipartUpload sends no
  x-amz-version-id" retired; registry entries for defect 5 and defect 8's write side
  (S3-correctness, both, unreproduced) and defect 9's rgw-go line updated.
- **Depends on:** V7a, V7b. **PR split:** (1) op and s3; (2) the s3-tests PR; (3) the pin bump.
  **Brief:** about 180 lines.

### Task V12a: op and s3, versioned DeleteObject and DeleteObjects

- **Layer:** op and handler. **Files:** modify `internal/op/deleteobject.go`, `deleteobjects.go`,
  `internal/s3/deleteobject.go`, `deleteobjects.go` and specs; edit `docs/differences.md`.
  **Cluster:** no.
- **Security boundary:** the MFA checks are authorization; adversarial review applies (PP-14).
- **Consumes:** V8a, V10's `Request.MFAVerified`, V13d (which sets it), V11's narrowed gate.
- **Behaviour:**
  1. DeleteObject's permission ([rgw_op.cc:5138-5166](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L5138-L5166),
     [rgw_op.cc:5528-5556](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L5528-L5556)): `s3:DeleteObjectVersion` when an instance is named
     ([rgw_op.cc:5144-5151](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L5144-L5151)); on an MFA bucket a named instance without
     `MFAVerified` is 403 AccessDenied ([rgw_op.cc:5158-5163](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L5158-L5163),
     [rgw_op.cc:5548-5553](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L5548-L5553)), replacing the unconditional refusal
     (`internal/op/deleteobject.go:106-116`).
  2. Execute ([rgw_op.cc:5173-5330](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L5173-L5330), [rgw_op.cc:5563](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L5563)): the state
     read follows the OLH; `MarkerOwner` is the requester; a lost race is 204
     ([rgw_op.cc:5305-5307](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L5305-L5307)). L4 later adds its lock check between the read and the
     delete; until then a lock bucket is 501 (VD-6).
  3. The response ([rgw_rest_s3.cc:3485-3500](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3485-L3500),
     [rgw_rest_s3.cc:3745](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L3745)): NoSuchKey is 204; `x-amz-version-id` when the result
     has one; `x-amz-delete-marker: true` when it is a marker.
  4. DeleteObjects ([rgw_op.cc:7019-7119](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L7019-L7119), [rgw_op.cc:7921](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7921)): on
     an MFA bucket, any key naming a version without `MFAVerified` refuses the whole request,
     main's test, which v19.2.6 had, on both releases; both floors invert it
     ([rgw_op.cc:7067-7081](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L7067-L7081), from ce98cebb815, and
     [rgw_op.cc:7969-7983](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7969-L7983); defect 1); keys grouped by name, `SkipOLHUpdate` on
     all but the last of each (VD-8, [rgw_op.cc:6924-6977](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L6924-L6977), chosen by
     `bucket->versioned()` at [rgw_op.cc:7008-7017](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L7008-L7017)); each key as
     `handle_individual_object` ([rgw_op.cc:6824-6922](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L6824-L6922),
     [rgw_op.cc:7743](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7743)); `Deleted` with `VersionId` when named and `DeleteMarker`
     with `DeleteMarkerVersionId` when one was made ([rgw_rest_s3.cc:4303-4331](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4303-L4331),
     [rgw_rest_s3.cc:4821](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4821)), which phase 1 renders already.
  5. `op.mfaEnabled` is deleted and its caller reads `rec.Info.MFAEnabled()`, V2's helper, which is the same expression (the review of V2, N4).
- **Specs:** the action per request; the MFA tables for both ops; the grouping's skip flags, and
  its final state equal to one update per key; headers for a marker (enabled and suspended), a
  version, a marker's instance, a null delete marker's removal (`x-amz-delete-marker: true`) and a
  missing version; the `Deleted` fields; a lock bucket 501.
- **Exclusions:** retires "Deletes that need MFA are refused"; the existing DeleteObjects MFA
  exclusion (defect 1) cites this task's rule.
- **Depends on:** V8a, V10, V11, V13d. **PR split:** one. **Brief:** about 200 lines.

### Task V12b: admin, the object removal and the purges on versioned buckets

- **Layer:** admin. **Files:** modify `internal/op/adminbucket.go` (`RemoveObjectAdmin`),
  `adminpurge.go` (`refuseVersionedPurge`) and specs; edit `docs/differences.md`. **Cluster:** no.
- **Consumes:** V8b.
- **Behaviour:** `DELETE /admin/bucket?object`, `DELETE /admin/bucket` with `purge-objects` or
  `bypass-gc` and `DELETE /admin/user` with `purge-data` reach V8b's driver halves on every bucket;
  the versioned and object-lock refusals go; no lock is checked (P2, the owner, 2026-10-13;
  [rgw_bucket.cc:274-289](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_bucket.cc#L274-L289),
  [rgw_bucket.cc:275-290](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_bucket.cc#L275-L290)).
- **Specs:** each route on enabled, suspended and lock buckets through op fakes; the purge's error
  when a removal fails part way.
- **Exclusions:** retires "A purging bucket removal refuses a bucket whose versioning was ever
  enabled" and "The admin object removal refuses a bucket whose versioning was ever enabled";
  a new one, "Admin removals ignore object lock" (P2).
- **Depends on:** V8b. **PR split:** one. **Brief:** about 100 lines.

### Task V13a: cls/otp, the check and result calls

- **Layer:** cls. **Files:** create `internal/cls/otp/ops_check.go` and specs in WP-16's package;
  update one entry's rgw-go line in `docs/ceph-upstream-bugs.md`. **Cluster:** no.
- **Security boundary:** MFA (PP-14).
- **Consumes:** WP-16's package, `OTPCheck` and the result constants. **Produces** these names,
  since WP-16 already holds `GetResult` (`GetAll`'s pending reply) and `CheckResult` (the result
  enum):

  ```go
  type CheckOp struct{ ID, Val, Token string }  // cls_otp_check_otp_op v1
  type GetResultReply struct{ Result OTPCheck } // cls_otp_get_result_reply v1
  const (
      MethodCheck     = "otp_check"      // RD|WR
      MethodGetResult = "otp_get_result" // RD
  )
  var ErrCheckRefused error // a check whose result is not OTP_CHECK_SUCCESS
  func Check(op radosclient.Execer, req CheckOp, r denc.Release)
  func LookupCheck(op *radosclient.ReadOp, req CheckOp, r denc.Release) *CheckLookup
  func (*CheckLookup) Record() (OTPCheck, error)
  ```

  Each wire type has `Encode` and a `Decode` function, as WP-16's do, for V13b. Both calls take
  the same `CheckOp`, so radosgw's quirk (behaviour 2) is built into the signatures. No
  `GetResultOp` is defined: radosgw never sends `cls_otp_get_result_op`.
- **Behaviour:**
  1. `OTP::check` sends `otp_check` (RD|WR) with the check op, whose token is 16 random
     characters of the 64-character table `A-Za-z0-9-_` (V13c's behaviour 2); the caller, V13c,
     draws it. Squid sends it through `IoCtx::exec` and Tentacle
     on a write op ([cls_otp_client.cc:60-74](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp_client.cc#L60-L74), [cls_otp_client.cc:60-76](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/otp/cls_otp_client.cc#L60-L76)). The call step's
     bytes are the same, and the OSD takes a call to an RD|WR method for a write either way
     (`may_write`, `set_class_write`, [osd_op_util.cc:23-25](https://github.com/ceph/ceph/blob/v19.2.7/src/osd/osd_op_util.cc#L23-L25), [osd_op_util.cc:23-25](https://github.com/ceph/ceph/blob/v20.2.4/src/osd/osd_op_util.cc#L23-L25); [osd_op_util.cc:197-204](https://github.com/ceph/ceph/blob/v19.2.7/src/osd/osd_op_util.cc#L197-L204), [osd_op_util.cc:197-204](https://github.com/ceph/ceph/blob/v20.2.4/src/osd/osd_op_util.cc#L197-L204)), so V13a sends
     it on a write op at both releases, in Tentacle's form.
  2. It then sends `otp_get_result` (RD) on a separate read op carrying the same encoded
     `CheckOp`. radosgw builds a `cls_otp_get_result_op` but passes `in`, the check op
     ([cls_otp_client.cc:76-84](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp_client.cc#L76-L84), [cls_otp_client.cc:78-86](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/otp/cls_otp_client.cc#L78-L86)), and the class's
     `otp_get_result` decodes a check op ([cls_otp.cc:483-511](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L483-L511), [cls_otp.cc:483-511](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/otp/cls_otp.cc#L483-L511)). The reply is
     `cls_otp_get_result_reply{otp_check_t{token, timestamp, result}}`.
  3. The answers are `check_mfa`'s ([svc_cls.cc:46-63](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/services/svc_cls.cc#L46-L63), [svc_cls.cc:46-63](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/services/svc_cls.cc#L46-L63)). ENOENT from either call means no device
     has the id, as `get_otp_instance` finds no `otp/<id>` key ([cls_otp.cc:171-193](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L171-L193), [cls_otp.cc:171-193](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/otp/cls_otp.cc#L171-L193)); it stays
     `radosclient.ErrNotFound`, which V13c reads as a missing device. A decoded result other than
     `OTP_CHECK_SUCCESS` is `check_mfa`'s EACCES: `Record` returns the check with
     `ErrCheckRefused`.
  4. WP's WD-16, the formatting rule, binds this task's code.
- **Specs:** hand-built goldens for `cls_otp_check_otp_op` and `cls_otp_get_result_reply`, as
  WP-16's are, because neither `ceph-object-corpus` nor `ceph-dencoder` has an otp type at either
  tag; `LookupCheck` sends exactly the bytes `Check` sends; each result value decodes; ENOENT and
  each result but SUCCESS answer as behaviour 3 says.
- **Registry:** none new. WP-16 recorded defect 12, "radosgw's otp check sends the otp_check
  request to otp_get_result". This task updates its rgw-go line to say rgw-go sends the check op
  to both methods, so no one "fixes" one side to send `cls_otp_get_result_op`, which the class
  answers EINVAL.
- **Depends on:** WP-16. **PR split:** one. **Brief:** about 90 lines.

### Task V13b: emulator, the otp check methods

- **Layer:** emulator. **Files:** fill `internal/testutil/fakerados/cls_otp_check.go`, the empty
  `otpCheckMethods(now func() time.Time) methodTable` WP-17 created, and specs. **Cluster:** no.
- **Security boundary:** MFA (PP-14); the emulator pins what the verifier trusts.
- **Consumes:** WP-17's device store with `last_checks` and `last_success`; V13a.
- **Behaviour:**
  1. `otp_check` RD|WR ([cls_otp.cc:447-481](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L447-L481),
     [cls_otp.cc:447](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/otp/cls_otp.cc#L447)): load the device by id, record the attempt with the TOTP
     result and write the device back, unless five attempts already fell in the last `step_size`
     seconds. That window slides with the check's own time, as `trim_expired` drops the attempts
     older than `now - step_size`; it is not an aligned TOTP step, so an emulator keyed on
     `floor(now / step)` would admit a sixth attempt across a step boundary
     ([cls_otp.cc:98-106](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L98-L106), [cls_otp.cc:98-106](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/otp/cls_otp.cc#L98-L106);
     `ATTEMPTS_PER_WINDOW`, [cls_otp.cc:43](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L43); `otp_instance::check`, [cls_otp.cc:108-127](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L108-L127),
     [cls_otp.cc:108-127](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/otp/cls_otp.cc#L108-L127)). A declined check answers 0 and writes nothing: `check`
     returns with `*update = false`
     ([cls_otp.cc:113-117](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L113-L117), [cls_otp.cc:113-117](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/otp/cls_otp.cc#L113-L117)), and the method calls `write_otp_instance`
     only on `update`
     ([cls_otp.cc:471-480](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L471-L480), [cls_otp.cc:471-480](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/otp/cls_otp.cc#L471-L480)).
  2. The validation ([cls_otp.cc:129-153](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L129-L153),
     [cls_otp.cc:129](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/otp/cls_otp.cc#L129)): RFC 6238 TOTP over the stored seed, step, time offset
     and window, at the class clock `now` that `OTPClassAt` passes to `otpCheckMethods`, which
     stands for the OSD's `real_clock::now()`; every time the check methods read is that clock,
     never `ClassCall.Now`. A success stores its window index as `last_success`, so the
     same code fails afterwards. The class validates through liboath; the implementer reads
     liboath's `oath_totp_validate2` for the digit count and the window walk and quotes it in the
     task notes (PP-2). The unsigned window distance of defect 11 is reproduced as the class
     computes it; a stored step of 0, which faults the OSD (defect 10), answers EIO here. The
     class hands liboath `val.c_str()`, so the emulator validates `Val` only up to its first NUL,
     and a good code followed by a NUL and junk verifies; counting digits with Go's `len` would
     fail it ([cls_otp.cc:133-136](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L133-L136), [cls_otp.cc:133-136](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/otp/cls_otp.cc#L133-L136)). The id and the token stay
     whole strings.
  3. `otp_get_result` RD ([cls_otp.cc:483-511](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L483-L511), [cls_otp.cc:483-511](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/otp/cls_otp.cc#L483-L511)) decodes a `CheckOp`, so a
     `cls_otp_get_result_op` frame, whose token reads as the id and whose code runs past the
     frame, fails with EINVAL. It finds the device by the id, answering ENOENT when there is none
     (`get_otp_instance`, [cls_otp.cc:171-193](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L171-L193), [cls_otp.cc:171-193](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/otp/cls_otp.cc#L171-L193)). It answers the newest check carrying the
     token, after `trim_expired` drops the checks older than one step, or
     `{token, OTP_CHECK_UNKNOWN, now}` when none does (`otp_instance::find`,
     [cls_otp.cc:155-169](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L155-L169), [cls_otp.cc:155-169](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/otp/cls_otp.cc#L155-L169); `trim_expired`, [cls_otp.cc:98-106](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L98-L106), [cls_otp.cc:98-106](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/otp/cls_otp.cc#L98-L106)). The trim is not written back,
     because the method is RD. So the lookup after a declined check answers
     `OTP_CHECK_UNKNOWN` under a fresh token. Under a reused one it answers the newest earlier
     entry of `last_checks` carrying it, a success included
     ([cls_otp.cc:160-165](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L160-L165), [cls_otp.cc:160-165](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/otp/cls_otp.cc#L160-L165)): the hazard V13c's fresh token defends
     against, which the emulator reproduces so that V13c's spec bites.
  4. WP's WD-16, the formatting rule, binds this task's code.
- **Specs:** RFC 6238 Appendix B's SHA-1 vectors at their times; a wrong code; a reused code; a
  good code followed by a NUL and junk, verified; the sixth attempt in one window, answered 0
  with nothing written; its lookup, `OTP_CHECK_UNKNOWN` under a fresh token and the fifth check's
  result under the fifth's token; an unknown token; a missing device; a `cls_otp_get_result_op`
  frame answered EINVAL; a lookup that trims an expired check and leaves it stored.
- **Registry:** none new; defects 10 and 11 are in the registry already.
- **Depends on:** WP-17, V13a. **PR split:** one. **Brief:** about 155 lines.

### Task V13c: driver, VerifyMFA

- **Layer:** driver. **Files:** create `internal/driver/mfa.go` and specs; register the otp class
  in the driver spec helpers. **Cluster:** no.
- **Security boundary:** MFA (PP-14).
- **Consumes:** V13a, V13b, WP-16's `OTPObject`, `meta.UserInfo.MFAIDs`, `ZoneParams.OTPPool`.
  **Produces:** `(*Store).VerifyMFA`, satisfying S2's `op.MFAVerifier`.
- **Behaviour:**
  1. `verify_mfa` ([rgw_sal_rados.cc:284-312](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_sal_rados.cc#L284-L312),
     [rgw_sal_rados.cc:301](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L301)): a serial not in the user's `mfa_ids`
     is not verified; then `check_mfa`.
  2. `check_mfa` ([svc_cls.cc:46-63](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/services/svc_cls.cc#L46-L63),
     [svc_cls.cc:46](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/services/svc_cls.cc#L46)): the object `user:<uid>` in the zone's `otp_pool`
     ([svc_cls.h:56-58](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/services/svc_cls.h#L56-L58)), V13a's two calls with one token, verified
     only on `OTP_CHECK_SUCCESS`. The token is 16 characters, as `OTP::check` draws it with
     `gen_rand_alphanumeric` and `TOKEN_LEN` ([cls_otp_client.cc:65-66](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp_client.cc#L65-L66),
     [cls_otp_client.cc:65-66](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/otp/cls_otp_client.cc#L65-L66)), drawn once per check and sent in both calls. It
     comes from crypto/rand, as radosgw's comes from `CryptoRandom` over letters, digits, `-` and
     `_` ([random_string.cc:33-51](https://github.com/ceph/ceph/blob/v19.2.7/src/common/random_string.cc#L33-L51), [random_string.cc:33-51](https://github.com/ceph/ceph/blob/v20.2.4/src/common/random_string.cc#L33-L51)). What it must be is unique among every
     gateway's checks on the device within a step, rgw-go's and radosgw's alike: under a shared
     token a declined check answers another check's result (V13b's behaviour 3). So no counter,
     no derivation from the request and no math/rand stream, any of which can repeat across
     processes.
  3. Any other result, which `Record` returns as V13a's `ErrCheckRefused`, or a missing device,
     `radosclient.ErrNotFound` from either call, is `(false, nil)`; V13c matches both with
     `errors.Is`. Any other RADOS failure is `(false, err)`. `Check` returns nothing, so its
     ENOENT arrives as `Pool.Write`'s error, and a failed `otp_check` ends the check before the
     lookup, as `OTP::check` returns
     ([cls_otp_client.cc:72-74](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp_client.cc#L72-L74), [cls_otp_client.cc:74-76](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/otp/cls_otp_client.cc#L74-L76)).
  4. Nothing is logged but the otp id and the result, as `check_mfa` logs them at level 20
     ([svc_cls.cc:60](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/services/svc_cls.cc#L60), [svc_cls.cc:60](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/services/svc_cls.cc#L60)): never the `CheckOp`, its `Val`, or the header, whose
     code still verifies until a check consumes it or its window passes.
  5. A pin holding a NUL is not verified, and the class is not called. The class hands liboath
     `val.c_str()`, so it would verify a good code followed by a NUL and junk
     ([cls_otp.cc:133-136](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L133-L136), [cls_otp.cc:133-136](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/otp/cls_otp.cc#L133-L136)), and `VerifyMFA` serves every later op. No
     exclusion: the difference cannot be observed, because net/http answers 400 to a header
     holding a NUL (V13d's behaviour 4), and radosgw cuts the header at its first NUL itself,
     reading `HTTP_X_AMZ_MFA` as a C string
     ([rgw_rest_s3.cc:5026-5028](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L5026-L5028), [rgw_rest_s3.cc:5556-5558](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5556-L5558)). The task report says so.
- **Specs** (on the emulator, V13b): an unknown serial; a serial in `mfa_ids` whose device is gone
  from the otp object, and a user with no otp object at all, each `(false, nil)`; a good code; a
  wrong one; a reused one; a rate-limited one, five checks in one step whose fifth is a good code,
  then a sixth with a wrong code, which is refused (with a fail as the fifth, a reused token
  reads FAIL too and the spec passes on the bug); two checks drawing different tokens; the token
  shared by the two calls; a failed `otp_check` with no lookup sent; a pin holding a NUL, refused
  with no call; nothing logged but the id and the result; a pool error.
- **Exclusions:** none.
- **Depends on:** V13a, V13b. **PR split:** one. **Brief:** about 130 lines.

### Task V13d: op and s3, the `x-amz-mfa` header

- **Layer:** op and handler. **Files:** modify `internal/s3/request.go` (`PostAuthInit`) and
  specs; wire `Env.MFA` where the gateway builds its env. **Cluster:** no.
- **Security boundary:** a client-facing header that feeds authorization (PP-14).
- **Consumes:** S2's `Env.MFA`, V13c, V10's `Request.MFAVerified`.
- **Behaviour** (VD-7):
  1. `PostAuthInit` reads `x-amz-mfa` after the tenant, bucket and object-name checks
     ([rgw_rest_s3.cc:5026-5031](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L5026-L5031), [rgw_rest_s3.cc:5556](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5556)), where
     phase 1 notes the gap (`internal/s3/request.go:163-179`).
  2. The value splits as `verify_mfa`'s `get_str_vec(mfa_str, " ", params)` does, and exactly
     two fields, serial and pin, are verified. Anything else is not, and never reaches the
     verifier, as `verify_mfa` answers EINVAL before `mfa_ids` and `check_mfa`: a split that
     handed a third field on as the pin would record a FAIL toward the five-attempt limit
     ([rgw_sal_rados.cc:287-293](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_sal_rados.cc#L287-L293), [rgw_sal_rados.cc:304-310](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L304-L310)). `get_str_vec` keeps what
     `for_each_substr` finds between spaces, and that skips leading spaces and runs of them, so no
     field is empty: `SERIAL  123456`, doubled, splits in two as `SERIAL 123456` does, while a
     tab is part of a field
     ([str_list.cc:43-49](https://github.com/ceph/ceph/blob/v19.2.7/src/common/str_list.cc#L43-L49), [str_list.cc:43-49](https://github.com/ceph/ceph/blob/v20.2.4/src/common/str_list.cc#L43-L49); [str_list.h:15-24](https://github.com/ceph/ceph/blob/v19.2.7/src/include/str_list.h#L15-L24), [str_list.h:14-23](https://github.com/ceph/ceph/blob/v20.2.4/src/include/str_list.h#L14-L23)). A
     verified pair sets `Request.MFAVerified`.
  3. Nothing about MFA fails the request here; an op that needs MFA refuses it with 403
     AccessDenied (`-ERR_MFA_REQUIRED` 2044, [rgw_common.cc:96](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L96)).
  4. A NUL never reaches `PostAuthInit`: net/http answers 400 "invalid header value" to a header
     value holding a control character other than a space or a tab, before any handler runs
     (`net/http/server.go:1082-1083` and `httpguts.ValidHeaderFieldValue` at go1.27.1; the
     frontend is net/http over HTTP/1, `internal/frontend/server.go:221-231`). V13d needs no NUL
     rule of its own; a spec pins the 400 at the handler, and a `PostAuthInit` called directly with
     one answers not verified, through V13c's behaviour 5.
  5. The header value is never logged, nor is anything holding it. radosgw's own level-5 log of a
     value that does not split carries its code (`docs/ceph-upstream-bugs.md`, "radosgw's
     verify_mfa logs an x-amz-mfa value that does not split in two, its code included").
- **Specs:** absent; malformed: one field, three, a tab between serial and pin, an empty value,
  each not verified with the fake verifier never called; doubled spaces between the fields,
  verified as radosgw verifies them; unknown serial; good and
  bad codes through a fake verifier; a verifier error logged and the request carried on
  unverified; a NUL in the header answered 400 before the handler, and a NUL passed to
  `PostAuthInit` directly not verified; no log line holding the header value, for a malformed one
  included.
- **Exclusions:** none: the split is radosgw's. V12a retires "Deletes that need MFA are refused".
- **Depends on:** S2, V10, V13c. **PR split:** one. **Brief:** about 110 lines.

### Task V14a: op and s3, versioned reads

- **Layer:** op and handler. **Files:** modify `internal/op/getobject.go`, `objectread.go`,
  `objectattrs.go`, the object ACL and tagging ops, UploadPartCopy's source check,
  `internal/s3/getobject.go`, `getobjattrs.go`, `deletemarker.go` and specs; edit
  `docs/differences.md` and `docs/ceph-upstream-bugs.md`. **Cluster:** no.
- **Consumes:** V6, V11's narrowed gate.
- **Behaviour:**
  1. GET and HEAD send `x-amz-version-id` from the resolved key, `null` for an empty instance on
     a bucket that was ever versioned (D4); radosgw sends only a non-empty id
     ([rgw_rest_s3.cc:469](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L469), [rgw_rest_s3.cc:497](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L497)).
     GetObjectAttributes sends it on both releases, at Tentacle's feature level
     ([rgw_rest_s3.cc:4010](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4010); Squid sends none).
  2. The permission check comes first ([rgw_op.cc:423-447](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L423-L447),
     [rgw_op.cc:453-477](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L453-L477)): a missing object, which includes a current delete
     marker, is 404 to an admin or a requester allowed `s3:ListBucket`, else 403.
  3. D6: that 404, when the current version is a delete marker, carries
     `x-amz-delete-marker: true` on both releases ([rgw_rest.cc:1936-1944](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L1936-L1944); none
     at [rgw_rest.cc:1898-1938](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L1898-L1938)). `deletemarker.go` reads
     `ObjectState.DeleteMarker`; phase 1's Tentacle-only `false` stays for other misses.
  4. D2: a GET or HEAD naming a delete marker's own version answers 405 MethodNotAllowed with
     `x-amz-delete-marker: true`, where radosgw answers 404 NoSuchKey (defect 6).
  5. GetObjectAttributes, the object ACL and tagging reads and writes, and UploadPartCopy's and
     CopyObject's sources resolve a named or current version through V6; their versioning gates
     go. The `s3:*Version*` actions are already chosen when an instance is named.
- **Specs:** the header table (enabled current, a named version, a null current on a suspended
  bucket, a never-versioned bucket); a current marker's 404 with the header on both releases;
  the 403 without it; the 405, including a GET naming `versionId=null` whose null version is a
  delete marker; ACL and tagging on a noncurrent version leave the current alone;
  UploadPartCopy from a named version.
- **s3-tests:** tests for D2, D6 and the GET side of D4 go into jhoblitt/ceph's
  `rgw-go-s3tests` branch; the pin bump is its own change.
- **Exclusions and registry:** new exclusions for D2, D6 and D4's read side; the OLH clauses of
  "Tentacle's x-amz-delete-marker is false, or absent, on a missing …" amended; registry entries
  for defects 6, 7 and 8's read side (S3-correctness, unreproduced).
- **Depends on:** V6, V11. **PR split:** (1) op and s3; (2) the s3-tests PR; (3) the pin bump.
  **Brief:** about 190 lines.

### Task V14b: op and s3, ListObjectVersions

- **Layer:** op and handler. **Files:** modify `internal/op/listobjects.go`,
  `internal/s3/listobjects.go` and specs; edit `docs/differences.md`. **Cluster:** no.
- **Consumes:** V9.
- **Behaviour:**
  1. The parameters ([rgw_rest_s3.cc:1718-1762](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L1718-L1762),
     [rgw_rest_s3.cc:1852](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L1852)): `versions` selects the listing; `key-marker` and
     `version-id-marker` become `Marker` and `MarkerInstance`, `null` the empty instance; the rest
     are the v1 listing's; `s3:ListBucketVersions`. The refusal at
     `internal/op/listobjects.go:121-123` goes.
  2. The document ([rgw_rest_s3.cc:1807-1877](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L1807-L1877),
     [rgw_rest_s3.cc:1910](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L1910)): `NextKeyMarker` and `NextVersionIdMarker` from V9's
     next marker, `null` for an empty instance ([rgw_rest_s3.cc:1817-1825](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L1817-L1825)); the
     fixed `null` at `internal/s3/listobjects.go:400` goes. Entries render as phase 1 renders them.
  3. The `list-type=2&versions` form is unchanged (D7).
- **Specs:** paging a key with three versions and a marker at `max-keys` 1 and 2; a null version's
  markers; a stale `version-id-marker`; a delimiter with versions; `encoding-type=url` on the
  markers; the v2 form unchanged.
- **Exclusions:** retires "The versions of a bucket whose versioning was ever enabled are not
  listed".
- **Depends on:** V9. **PR split:** one. **Brief:** about 120 lines.

### Task V16: concurrent DeleteObjects and writes of one key

- **Layer:** driver and op, wherever the cause lies. **Files:** the symptoms surface in
  `internal/op/deleteobjects.go` and `internal/driver/olh_head.go`; the fix and its specs go
  where the cause is. **Cluster:** yes, to reproduce.
- **Security boundary:** DeleteObjects and the versioned write are on the request path;
  adversarial review applies (PP-14).
- **Found by:** V15a's recording. Both releases' parity runs fail
  `test_versioning_concurrent_multi_object_delete`, which radosgw passes.
- **Behaviour:**
  1. Five concurrent DeleteObjects naming the same 15 versions, three of each of five keys, on
     an Enabled bucket: every key of every request answers `Deleted`, as on radosgw, where a
     delete that finds its object gone succeeds
     ([rgw_op.cc:6908-6910](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L6908-L6910),
     [rgw_op.cc:7836-7838](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7836-L7838)).
     rgw-go answers a per-key InternalError in every round on both releases, while five
     concurrent DeleteObject calls of one version do not fail.
  2. A per-key error that DeleteObjects reports is logged with the key and its cause;
     `deleteOne` logs none (`internal/op/deleteobjects.go:320-348`).
  3. The concurrent versioned writes and removes of
     `test_versioned_concurrent_object_create_concurrent_remove` and `_and_remove` answer no
     500. Each release's parity run logged twelve "writing the olh head of <bucket>/myobj: ...
     no such file or directory" (`internal/driver/olh_head.go:687`), which botocore's retries
     hid. Defect 20's retry (VD-12) is meant to absorb a head that goes before its init; find
     why it did not. Fix it here if it shares item 1's cause, or number it as its own task.
- **Specs:** item 1's race against the emulator, failing before the fix; item 3's race likewise
  once its cause is known; the per-key log line.
- **s3-tests:** `test_versioning_concurrent_multi_object_delete` covers item 1. Item 3 takes an
  s3-test with retries off if its cause is separate and a test can reproduce it.
- **Depends on:** V12a. **PR split:** one. **Brief:** about 50 lines.

### Task V15a: recording, the versioning s3-tests

- **Layer:** tests (recording). **Files:** the area's lines in the s3-tests selection (after T's
  port, WP-22), `hack/s3tests/deselect-phase2.txt`, `test/s3tests/baseline/*.json`,
  `known-differences-squid.txt`. **Cluster:** yes.
- **Consumes:** T's selection port and recording command; the served ops of V10 to V14b.
- **Behaviour:** the `versioning` and `delete_marker` markers leave the excluded set; the two
  deselected tests (`hack/s3tests/deselect-phase2.txt:11-12`) are deleted;
  `test_object_lock_put_obj_lock_enable_after_create` goes under the object-lock heading unless L
  has landed; both ledgers are re-recorded with `make s3tests-record-tests`, landed one at a time
  through the controller. At the harness pin that frees §5's 33 versioning, 3 delete-marker and
  20 conditional-write tests; the 13 lifecycle and cloud tests stay excluded by their markers.
  The s3-tests V11 and V14a added, which radosgw fails on both releases, go into
  `known-differences-squid.txt`; defect 3 needs no line at v19.2.7.
- **Specs:** the recording itself; rgw-go matches the ledgers except the listed differences.
- **Exclusions:** none new; each known difference cites the exclusion its task wrote.
- **Depends on:** V10, V11, V12a, V14a, V14b, V16, WP-22. **PR split:** one. **Brief:** about 80
  lines.

### Task V17: DeleteObjects runs a name's skipped OLH updates after its last delete fails

- **Layer:** op and driver. **Files:** `internal/op/deleteobjects.go`, the driver's OLH update
  seam, their specs; `docs/differences.md`, `docs/ceph-upstream-bugs.md`, both
  known-differences files. **Cluster:** yes, for the s3-tests.
- **Security boundary:** DeleteObjects is on the request path; adversarial review applies
  (PP-14).
- **Found by:** V15a's task review (I3).
- **Behaviour:**
  1. When DeleteObjects deletes several versions of one name, every delete but the name's last
     skips the OLH update (VD-8). When that last delete fails, or finds its version gone, the
     name's skipped updates still run, so no version the request reported deleted stays
     readable. Only names whose earlier deletes skipped the update are touched. This is Ceph
     main's fix, [7ce6a3740c2](https://github.com/ceph/ceph/commit/7ce6a3740c2) with
     [d1193f5341c](https://github.com/ceph/ceph/commit/d1193f5341c), merged as
     [ceph/ceph#72130](https://github.com/ceph/ceph/pull/72130), whose first tag is v22.0.0.
  2. Both floors leave those updates pending
     ([rgw_op.cc:6924-6979](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L6924-L6979),
     [rgw_op.cc:7857-7891](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7857-L7891)),
     and rgw-go reproduces that today. The difference is recorded in `docs/differences.md`,
     replacing the entry's "as radosgw leaves them", and the registry entry V15a adds gets its
     rgw-go line.
- **Specs:** a name with three versions whose last delete names a missing version, and one whose
  last delete fails its precondition: after the request, the deleted versions are gone from a
  read by id and from the versions listing, and the OLH names the version that should be
  current. A name whose last delete succeeds makes the same calls as before. Concurrent
  DeleteObjects of every version of one name leave no OLH head behind, whether the head would
  have kept a pending attr or none: V16's review found heads of both kinds, the second where a
  refused clear left the log untrimmed, which the update re-applies from its first entry.
- **s3-tests:** `test_versioning_multi_object_delete_last_missing` and
  `_last_precondition_failed` cover it. They then pass on rgw-go and fail on radosgw at both
  floors, so each gets a line in both known-differences files.
- **Depends on:** V16, V15a. **PR split:** one. **Brief:** about 50 lines.

### Task V18: a versioned write whose version is deleted before it links succeeds

- **Layer:** driver. **Files:** the driver's versioned link (`setOLH` and its callers), their
  specs; `docs/differences.md`, `docs/ceph-upstream-bugs.md`. **Cluster:** no.
- **Security boundary:** the versioned write is on the request path; adversarial review applies
  (PP-14).
- **Found by:** V16's task review (I2), from V16's exploration.
- **Behaviour:**
  1. A versioned PutObject, CopyObject or CompleteMultipartUpload whose version a DeleteObject or
     DeleteObjects naming its id removes between the write's index completion and its OLH link
     answers success with its version id, and the OLH does not name the removed version. The
     write first and the delete second accounts for what is stored, so the write took effect and
     its 404 is wrong; this is V16's ruling for the same shape one step later. A delete reaches
     the window only by naming an id it knows before the link: V16 found a gateway-generated id
     is not listed until then, which leaves the null version on a Suspended bucket. V18 first
     reproduces the window through S3; if no S3 request reaches it, V18 says so in the registry
     entry and changes nothing else.
  2. radosgw answers 404 NoSuchKey: the class's link_olh refuses a link whose instance entry is
     gone
     ([cls_rgw.cc:1669-1688](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1669-L1688),
     [cls_rgw.cc:1810-1829](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1810-L1829)),
     and set_olh returns that ENOENT
     ([rgw_rados.cc:8715-8736](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8715-L8736),
     [rgw_rados.cc:9461-9482](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9461-L9482)).
     rgw-go answers the same today. The difference goes in `docs/differences.md`, and the
     registry entry V16 adds gets its rgw-go line.
  3. Only that refusal, with the instance's entry gone, becomes success; every other link failure
     keeps its answer. The write's tails belong to the delete that removed its version: none is
     left without a head that names it, and none is removed twice.
- **Specs:** the delete injected between index completion and link against the emulator, for
  PutObject and for one of CopyObject or CompleteMultipartUpload: the write answers its version
  id; the version is gone from a read by id and from the versions listing; the OLH names the
  version that should be current; no tail of the write is left. A link refused for any other
  reason answers as before.
- **s3-tests:** none. The window lies between two RADOS operations inside one request, which a
  client cannot hit reliably; the driver specs inject it.
- **Depends on:** V16. **PR split:** one. **Brief:** about 45 lines.

### Task V19: another request's replay keeps a versioned delete's object-lock guard

- **Layer:** driver. **Files:** the OLH replay's removal of an instance, the object-lock guard
  of a versioned delete, their specs; `docs/differences.md`, `docs/ceph-upstream-bugs.md`.
  **Cluster:** no.
- **Security boundary:** the versioned delete and object lock are on the request path;
  adversarial review applies (PP-14). Triaged not security-relevant (an integrity defect: the
  deleter was authorized when checked, and cannot time the hold).
- **Found by:** V16's adversarial review (P1). Derived from the code, unreproduced.
- **Behaviour:**
  1. A versioned DeleteObject under object lock holds its removal to the version it checked:
     its own replay refuses to remove a version whose write tag changed after the check, and the
     delete re-checks and answers AccessDenied. Retention and legal-hold changes write a fresh
     write tag, so this catches a hold set after the check. Another request's replay, such as a
     GET's, applies the same delete's log entry with no guard, and removes the version with the
     new hold in place; the delete answers 204 and the hold request has answered 200. After
     this task, no replay removes a version the delete's guard would refuse, and the delete
     answers as when its own replay refuses it.
  2. The OLH log keeps radosgw's format byte for byte, so the guard cannot ride in the log
     entry. If no byte-identical mechanism closes the window, the task narrows it as far as one
     does, records what remains in `docs/differences.md`, and says why.
  3. radosgw has no guard at either floor. The registry records that: extend "radosgw's
     DeleteObject removes the head without checking its write tag" if it is the same mechanism,
     or add an entry.
- **Specs:** a hold set between a versioned delete's lock check and its replay, with another
  request's replay applying the delete's entry first: the version and its hold remain, and the
  delete answers AccessDenied. The same delete with no hold set still removes the version
  whichever replay applies it.
- **s3-tests:** none. The window lies inside one request's replay; the driver specs inject it.
- **Depends on:** V16. **PR split:** one. **Brief:** about 45 lines.

### Task V20: a key left with an OLH entry pending removal stays listable

Found by V18's task review (Important 1), and numbered here before V18 merged (PP-12).

- **Layer:** driver. **Files:** the driver's unversioned and null-version write and its index
  completion, their specs; `docs/differences.md`; the registry entry V18 adds. **Cluster:** no.
- **Security boundary:** the versioned write is on the request path; adversarial review applies
  (PP-14).
- **Behaviour:**
  1. A write that creates a key's OLH head, whose version a delete removes before its link,
     leaves the key's OLH index entry marked for removal and a version marker; radosgw leaves the
     same state, which it reaches through S3 (`PUT k?versionId=X` racing `DELETE k?versionId=X`).
     A later unversioned PUT of the key on a Suspended bucket then completes its index entry
     behind the version marker, where no listing reads: the object reads back and is counted,
     and neither ListObjects nor ListObjectVersions shows it (V18's review: `read_key_entry`,
     `complete_op`'s ADD and `get_obj_vals` in cls_rgw.cc; verify each at both tags at
     implementation).
  2. rgw-go's write that meets that state, its own or radosgw's, leaves the key listable: it
     clears or replaces the leftover entry and marker through the class operations radosgw's own
     clear uses, so the bucket index stays byte-identical to a state radosgw writes. If no such
     sequence exists, the task records why in the registry entry and changes nothing.
- **Specs:** the leftover state built in the emulator, then an unversioned PUT on a Suspended
  bucket: the object is listed by ListObjects and ListObjectVersions and reads back; the same
  with the state radosgw leaves; a key without the leftover behaves as before.
- **Exclusions and registry:** a `docs/differences.md` entry where rgw-go lists what radosgw does
  not; the registry entry's rgw-go line.
- **Depends on:** V18. **PR split:** one. **Brief:** about 40 lines.

### Task V15b: the cross-gateway cases

- **Layer:** integration tests. **Files:** the shared-zone suite beside phase 1's cases.
  **Cluster:** yes, both releases, one at a time.
- **Consumes:** every served op of this unit; V5b's fault point; `rooket k` with
  `ROOKET_NAME=rgw-go-<release>` for `radosgw-admin` and `ceph config set`.
- **Behaviour:** §4's cases, radosgw the Rook store's gateway and rgw-go beside it on the zone:
  - A and B: each gateway writes versions, a marker, a suspended null version and deletes the
    null marker; the other reads every version, lists with and without markers, deletes a
    noncurrent version, sets an ACL on one; `radosgw-admin bucket check` is clean and `bi list`
    shows the expected flags and epochs.
  - **C, mixed writers** (Review Focus 1): N racing PUTs of one key split across both gateways,
    then racing PUT and DELETE, then two DELETEs of one instance. After quiescence no version is
    lost, both gateways list the same versions in the same order, the OLH targets the newest,
    every version reads back, `bucket check` is clean, and the two DELETEs answer 204 each.
  - D, interrupted writers. On radosgw, set with `ceph config set`: `rgw_debug_inject_set_olh_err`
    ([rgw.yaml.in:2754](https://github.com/ceph/ceph/blob/v19.2.7/src/common/options/rgw.yaml.in#L2754), [rgw.yaml.in:2886](https://github.com/ceph/ceph/blob/v20.2.4/src/common/options/rgw.yaml.in#L2886))
    fails its link after its init and completion
    ([rgw_rados.cc:8707-8714](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8707-L8714),
    [rgw_rados.cc:9453-9455](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9453-L9455)), leaving an unlinked instance; with
    `rgw_debug_inject_olh_cancel_modification_err` too, its pending attr stays
    ([rgw_rados.cc:7758-7761](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L7758-L7761),
    [rgw_rados.cc:8421](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L8421)). rgw-go's reads and listings still show the
    previous current version, its next unconditional write links normally, and `bucket check`
    reports the orphan as radosgw's own run does. The pending attr's expiry is the emulator's spec
    (V5b), not the cluster's. On rgw-go, the fault point stops a writer after its link; radosgw's
    next read finishes the replay.
  - E: a pre-versioning object from one gateway versioned by the other.
  - F: PutBucketVersioning on one gateway seen by the other's next PUT.
  - G: `radosgw-admin bucket reshard` while rgw-go writes and deletes versions.
  - H: `radosgw-admin mfa create` for a user; rgw-go verifies a code the spec computes from the
    seed and refuses a wrong and a reused one; `radosgw-admin mfa check` shows the attempts;
    `MfaDelete` set on rgw-go is seen by radosgw. The Tentacle run also reproduces defect 1 on
    radosgw.
  - I: rgw-go's admin object removal leaves a marker radosgw lists; rgw-go's purge of a bucket
    radosgw filled with versions leaves `bi list` empty and the pools clean after the GC.
  - V0 and V9 against a real shard: a non-versions listing of a key with more than eight
    reads' worth of markers and noncurrent versions completes.
  - Defect 13: case B's `radosgw-admin olh get` on Squid is read knowing it decodes `olh.ver`.
- **Exclusions:** none new; a case that finds a difference adds it with its task's owner.
- **Depends on:** every task above. **PR split:** (1) cases A to G and the real-shard listing;
  (2) cases H and I.
  **Brief:** about 200 lines.

### Task V15c: the registry after the gate

- **Layer:** docs. **Files:** `docs/ceph-upstream-bugs.md`. **Cluster:** no.
- **Consumes:** V15a's recordings and V15b's runs.
- **Behaviour:** each defect V15a's recordings or V15b's cases reproduced moves from unreproduced
  to reproduced, with the run named. Defect 1, a security defect, goes to the
  rgw-bug-reproduction session through the controller (PP-4). A serious defect reproduced here is
  reported upstream (spec §8).
- **Depends on:** V15a, V15b. **PR split:** one. **Brief:** about 40 lines.

### Task V21: a replay keeps a version a later link of the same log names

Found by V20's implementer and confirmed by its task review, and numbered here before V20 merged
(PP-12).

- **Layer:** driver. **Files:** `applyOLHLog` in `internal/driver/olh_head.go` and its specs; the
  registry entry V20 adds; `docs/differences.md`. **Cluster:** no.
- **Security boundary:** the versioned write's replay; adversarial review applies (PP-14).
- **Behaviour:**
  1. A replay that meets a REMOVE_INSTANCE of a version and, later in the same log, a LINK_OLH of
     that version removes nothing for it, and links it. Today rgw-go's `applyOLHLog` drops such a
     removal only for the null version; a named version is removed though the log links it after.
     radosgw's `apply_olh_log` removes every logged removal before its head op at v19.2.7 and
     v20.2.4. Upstream main's 75c7b8ece79 (ceph/ceph#67661) cancels the removal when the same key
     is relinked after an UNLINK_OLH in the batch; verify how much of this it covers at
     implementation.
  2. The RADOS state the replay writes stays one radosgw writes for that log, except for the version
     it keeps; record the difference.
  3. Trace the lifecycle transition, the one in-process writer that names an existing version,
     against a concurrent delete. Say whether it reaches this shape.
- **Specs:** an emulator log of REMOVE_INSTANCE then LINK_OLH for one named version keeps that
  version listed and readable; the null-version case as today; a removal with no later link of its
  version removes it as today.
- **Depends on:** V20. **PR:** one. **Brief:** about 35 lines.

### Task V22: a replay's removal keeps a version a link lands on after the replay read the log

Found by V21's adversarial review and confirmed by its task re-review, and numbered here before
V21 merged (PP-12). Pre-existing on main: V21 keeps a version a later entry of the same log links,
but a link that lands after a replay has read the log, and before that replay removes the version,
still leaves the version's list entry naming a version whose head and instance entry are gone.
V21's adversarial re-run found four cases of it:

1. The CompleteMultipartUpload retry's link (`linkRecorded`) lands after a replay has read the
   removal: the OLH names a gone version and the key refuses every later write. Reproduced at
   V21's head and at main.
2. A refused removal's relist links the version after another request's replay has read that
   removal: a list entry naming nothing, 4 of 2000 runs at V21's head and 5 at main.
3. The lifecycle transition's link and `keepCurrent`'s land below the epoch a delete's unlink
   moved the OLH entry to, ahead of that delete's replay: a list entry naming nothing. By reading
   only.
4. radosgw is the linker or the replay on a mixed fleet; rgw-go cannot fix radosgw's side.

- **Layer:** driver. **Files:** the replay's removal of a version (`removeInstance` in
  `internal/driver/olh.go`, called from `applyOLHLog` in `internal/driver/olh_head.go`) and its
  specs; the lifecycle transition's link (`linkVersion` in `internal/driver/put_versioned.go`, for
  a transition's write) and `keepCurrent` in `internal/driver/lc_transition.go`, and their specs;
  specs only for the multipart completion's retry link (`linkRecorded` in
  `internal/driver/mp_complete.go`) and the relist (`relistVersion` and `relistBelow` in
  `internal/driver/delete_versioned.go`); the registry entry V20 adds and V21 extends;
  `docs/differences.md`. **Cluster:** no.
- **Security boundary:** the versioned write's replay; adversarial review applies (PP-14).
- **Behaviour:**
  1. The replay removes a version only while the version's list entry is still absent: the
     removal's first index op compares the list entry it read, so a link that has landed since the
     replay read the log refuses the removal, and the replay keeps the version as V21 keeps one a
     later entry of its log links. The delete whose removal it was answers as when V21's drop
     keeps its version. The list entry the compare holds must be the one as of the replay's read of
     the log page that holds the removal. Read before that, it may predate the unlink that logged
     the removal. Read after, it lets a link between the two reads pass.
  2. The compare closes cases 1 and 2, and does not close case 3:
     - Case 1: the retry's link writes the version's list entry, current, and logs its LINK_OLH
       after the replay's read, so the removal's compare is refused. The version stays and the next
       replay links it, the outcome the differences entry already records for a link that lands
       before the read.
     - Case 2: the relist's link writes the version's list entry, below a newer version at the
       entry's old epoch or current at a new one, before the other replay's removal, so the compare
       is refused. The version stays listed, which is right, since the delete answered
       ConcurrentModification and no delete was acknowledged.
     - Case 3: the transition's link takes the class's old-epoch path, which writes the list entry
       and logs nothing, so the delete's replay is refused as well. The compare alone keeps a
       version whose delete was acknowledged and that lifecycle, not a client write, relisted. That
       trades a list entry naming nothing for a lost delete.
  3. For case 3, the transition's link and `keepCurrent`'s relink get the guards V21 gives the
     relist. Each reads the OLH entry, gives way when the entry's pending log holds a removal of the
     version, and sends its link compared with the entry as read. An unlink that lands between the
     read and the link refuses the link, and the next round gives way. A transition that gives way
     answers as one whose link a delete refused: no `keepCurrent`, no notification. Its rewritten
     head and tails go to the delete's replay, so no tail is left without a head that names it and
     none is removed twice. These guards land with the compare in the same PR, never after it.
  4. The window between the removal's first index op and its index completion stays. A link that
     lands there finds the instance entry still present. Record that window, and the state it
     leaves, in `docs/differences.md` and the registry entry.
  5. Case 4 is recorded, not fixed. radosgw's replay removes a version whenever it has read the
     removal, whoever links after. radosgw's links carry neither the compare nor the guards: its
     PutObject naming a version id is case 1's link, which rgw-go's replay keeps, and its
     transition's old-epoch link after an unlink is case 3's, which rgw-go's replay then keeps
     though a delete was acknowledged. The states a mixed fleet reaches go in `docs/differences.md`
     and the registry entry V20 adds.
  6. The RADOS state the replay writes otherwise stays the one radosgw writes for that log.
  7. Trace, before implementing: a delete's removal that completes between a transition's head
     write and its index completion. Say whether that index completion re-creates an instance
     entry with no head; if it does, its fix belongs here, with a spec.
- **Specs:** on both releases, against the emulator:
  - an interleaving where a link of the version lands after the replay's read and before its
    removal keeps the version listed and readable;
  - case 1: a CompleteMultipartUpload records itself and writes version X; a delete of X unlinks
    it; a replay reads the log; the retry's link lands; then the replay removes. X stays stored,
    listed current and readable, the OLH names it once the next replay runs, and a later PutObject
    of the key links;
  - case 2: a guarded delete unlinks X after an attr write moved X's write tag; a PutObject's
    replay reads the log; the delete's own replay refuses its removal and trims it; its relist links
    X; then the PutObject's replay removes. X stays stored and listed and the delete answers
    ConcurrentModification. Run it once with a newer version listed, the relist's old-epoch link,
    and once without, its link at a new epoch;
  - case 3: the transition reads X; a delete unlinks X; the transition's link, and in a second run
    `keepCurrent`'s relink, gives way; then the delete's replay removes. X's head, instance entry and
    list entry are gone and no tail is left. The same with the unlink landing between the
    transition's read of the OLH entry and its link;
  - the interleavings V21's specs and its adversarial harness pin stay as they are.
- **Depends on:** V21. **PR:** one. **Brief:** about 70 lines.

### Task V23: two refused deletes keep the newest version current

Found by V21's adversarial review and numbered here before V21 merged (PP-12). Its re-run 2 reports
it as R2-P1, older than V21 and outside V22's text: 74 of 2000 random schedules with named versions
and 110 of 2000 with the null version at V21's head, 72 and 106 at main. Two guarded deletes of one
key run at once, each refused because an attr write moved its version's write tag after the unlink.
With v1, v2 and v3 written and v3 current, the delete of v3 unlinks it and the delete of v2 unlinks
v2; both own replays refuse their removals and trim; v2's relist finds nothing newer listed, since
v3 is still out of the listing, and links v2 at epoch 0, current at a fresh epoch; v3's relist then
finds v2 listed newer and links v3 below it at its own epoch. Both deletes answer
ConcurrentModification and remove nothing, yet a GET of the key reads v2 until the key's next write.
The null form is the same with null data older than v1 on a suspended bucket: the refused null
delete marker's relist links the null data current while v1's refused delete holds v1 out of the
listing, and v1 is relisted below it.

- **Layer:** driver. **Files:** the refused-removal path in `internal/driver/delete_versioned.go`
  (`refusedRemoval`; the relists V21 changes, `relistVersion` and `relistNull`; `relistBelow`,
  `newestListed` and `listsBefore`) and their specs; the entry "A refused versioned removal re-links
  the version, unless a newer null write owns the null key" in `docs/differences.md`, which states
  the rule (a version no listed version is newer than goes current) and not this consequence.
  **Cluster:** no.
- **Security boundary:** the versioned write's replay; adversarial review applies (PP-14).
- **Behaviour:**
  1. After two refused deletes, the current version is the newest one still listed, newest in the
     order the versions held before the deletes: v3 over v2, and v1 over the older null data. No
     relist links its version current over a newer version another refused delete holds out of the
     listing. radosgw never reaches this state: its replay removes every version its log names
     through `delete_obj`, with no write-tag guard
     ([rgw_rados.cc:8530-8541](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8530-L8541),
     [rgw_rados.cc:9276-9287](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9276-L9287);
     registry entry "radosgw's DeleteObject removes the head without checking its write tag"), so in
     the same interleaving both deletes answer 204 and both versions are removed, leaving v1 current
     in the named form and the null delete marker in the null form. The state to reach is the key's
     state before the two deletes, as V8c restores it after one refusal.
  2. The RADOS state otherwise stays radosgw's: every link goes through the class's epoch-0 or
     old-epoch path, and the index and OLH head bytes are ones radosgw writes. A version a writer
     links after the unlinks stays current over both, a version another replay removed is not
     listed, and no version V21 lists is left unlisted. When the other refused delete's relist never
     runs (its gateway stops, or its request ends), say what the first relist leaves. Record that
     state, and any other difference, in `docs/differences.md`.
- **Specs:** on both releases, against the emulator:
  - named versions: the interleaving above, with v2's relist linking before v3's reads the listing.
    Both deletes answer ConcurrentModification; v1, v2 and v3 are stored and listed, v3 current and
    read by a GET of the key, exactly one entry flagged current;
  - the null version: the null delete marker's link and v1's unlink land before either relist, and
    the null relist links first. Both answer ConcurrentModification; the null data and v1 are stored
    and listed, v1 current;
  - each again with the newer version's relist first, whose outcome stays as today;
  - V21's adversarial harness, its random families H10 (null) and H12 (named), 2000 schedules each:
    0 schedules leave the older version current, the other families' tallies as V21 leaves them.
- **Depends on:** V21. **PR:** one. **Brief:** about 30 lines.

### Task V24: a version left unlisted is still expired and blocks DeleteBucket

Found by V21's task re-review and numbered here before V21 merged (PP-12). Its re-review 3 reports
it as N7, pre-existing on main. A version whose head (the OLH head, for the null version's data)
and instance entry stay while its list entry is gone is in no listing, and each reader below finds
versions only through the listing:

- lifecycle's expiration walks `ListObjects` with `ListVersions` (`lcBucket.rule` and
  `lcBucket.walk`, `internal/driver/lc_bucket.go:280` and `internal/driver/lc_bucket.go:360`), so
  no rule expires it;
- DeleteBucket's emptiness check reads the same unordered listing, versions included
  (`checkBucketEmpty`, `internal/driver/bucketops.go:366-383`), so the bucket reads as empty.
  DeleteBucket then removes the index shards whole (`cleanIndex`,
  `internal/driver/bucketops.go:141-165`), the instance entry with them, and the version's head and
  tails stay in the data pool with nothing naming them. The purging removals end in the same check;
- the bucket index header keeps counting it, since no DEL completed for it, so `BucketStats`
  (`internal/driver/stats.go:408-417`) and the owner's stats synced from it count it until the
  bucket is deleted, and nothing counts its objects after.

V21's adversarial review adds one more path in its re-run 4, as R4-M3, older than V21 and the same
on main: a null version that was its key's only one, where a delete leaves the data's index record
behind with the data gone (Behaviour 1's last path, and Behaviour 7).

- **Layer:** driver. **Files:** the lifecycle walk (`lcBucket.rule`, `lcBucket.walk` and
  `lcVersionPager` in `internal/driver/lc_bucket.go`); DeleteBucket's emptiness check and removal
  (`checkBucketEmpty`, `DeleteBucket` and `cleanIndex` in `internal/driver/bucketops.go`, and
  `claimRemoval` in `internal/driver/bucketadmin.go`) and the admin bucket removal
  (`DeleteBucketWithChildren` in `internal/op/adminpurge.go`); the
  relist paths (`refusedRemoval`, `relistVersion`, `relistNull` and `relistBelow` in
  `internal/driver/delete_versioned.go`), and, where design (A) puts its marker, the replay's keep
  and the writer's check (`applyOLHLog` in `internal/driver/olh_head.go`, `removeInstance` and
  `checkRemoval` in `internal/driver/olh.go`); for Behaviour 7, the delete naming `null` in
  `internal/driver/delete_versioned.go` and the replay's clear of the OLH (`applyOLHLog`,
  `finishOLHLog` and `clearOLHHead` in `internal/driver/olh_head.go`); their
  specs; `docs/differences.md`; `docs/ceph-upstream-bugs.md`; `docs/rados-format-opportunities.md`
  under (A). **Cluster:** no for the emulator specs. A cluster spec is needed only if (B) is chosen:
  `bi_list` is a class call rgw-go has not made, and the emulator's must be pinned against the
  class on both releases.
- **Security boundary:** quota accounting, the bucket-deletion path and, for Behaviour 7, the
  versioned delete's replay; adversarial review applies (PP-14). Every text on the quota side,
  here, in `docs/differences.md` and in the registry, states the mechanism only.
- **Behaviour:**
  1. The paths that leave a version stored and unlisted, as `docs/differences.md` records them
     (verify each at implementation, and add any found since):
     - a refused versioned removal whose relist does not link the version ("A refused versioned
       removal re-links the version, unless a newer null write owns the null key"): on main, the
       null relist's five refused rounds; after V21, the four cases its entry lists: an unbroken
       stream of other writers' changes up to the relist's bound, a relist that gave way to a removal
       whose delete never replays and whose replay keeps the version for its lock, a gateway stop
       between the refused replay's trim and the relist's link, and a failed read or write of the
       relist;
     - a replay's keep of a locked version ("A replay keeps another writer's version whose object
       lock holds"): radosgw's removal of a locked version that an rgw-go replay applies first; a
       writer whose gateway stops between its unlink and its replay, or whose request ends while
       its unlink is in flight; a writer whose own replay runs out of passes with its entries still
       logged; a writer with no `Expect` whose version another replay kept and whose lock ended
       before its check;
     - a failed versioned write ("rgw-go removes the instance a failed versioned write made"): a
       link whose outcome is unknown, or a removal that fails, leaves the head and an instance
       entry never linked, as radosgw's `set_olh` does;
     - a CompleteMultipartUpload version whose link never landed ("AbortMultipartUpload leaves a
       completed upload's parts alone"), until an abort removes it; DeleteBucket aborts the
       bucket's uploads after its emptiness check, so confirm that the abort takes it;
     - a write that goes on behind a stranded version marker as radosgw's does ("An unversioned
       write of a key whose OLH a delete left pending removal clears it first, and is listed"): an
       index entry that does not decode, or a write that meets a reshard;
     - a delete naming `null` after a refused null delete whose relist ended without linking,
       when the null data was the key's only version (R4-M3; V21's adversarial harness, J5, the
       same at V21's head, at main and at the head of its re-run 3). The one path here that leaves
       nothing stored. The refused delete's null delete marker stays current after its 409: the
       key reads 404, ListObjectVersions shows that marker alone, latest, and a read naming `null`
       finds the data. One delete naming `null` takes the marker first, and the data with it: the
       key's head object, which holds the null data, is gone and ListObjectVersions is empty. The
       data's instance record stays in the index, `exists=true`, and the bucket index header still
       counts it, so `BucketStats` and the owner's stats do; no listing shows it and no request
       reaches it, a second delete naming `null` answering NoSuchKey. J5 observed that state and
       did not trace the delete. By reading, the step is the delete's replay, which finds no
       version left and clears the key's OLH: `clearOLHHead` (`internal/driver/olh_head.go:688-709`)
       removes the head with a plain remove and sends no index op for the record. So, by reading
       and not run, any tails of the data stay in the data pool with nothing naming them.
       DeleteBucket, finding the listing empty, removes the record with the index shards.
  2. A version in that state is either listed again, or found and handled by lifecycle and by
     DeleteBucket: lifecycle's rules act on it as on a listed version, and DeleteBucket answers 409
     BucketNotEmpty while it exists, or removes its head, tails and index entries with the bucket.
     DeleteBucket never orphans its objects, and the bucket's stats count exactly the versions
     stored.
  3. The two candidate designs; the implementer costs both before choosing:
     - (A) Each path that leaves the state also leaves a durable marker naming the version, which
       a later operation consumes: lifecycle's pass and DeleteBucket's check read the markers and
       list each version again, or expire or remove it. No record in radosgw's formats serves.
       The OLH log carries only the class's link, unlink and instance-removal ops
       ([cls_rgw_types.h:514-517](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw_types.h#L514-L517),
       [cls_rgw_types.h:513-516](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw_types.h#L513-L516)).
       A pending attr sits on the OLH head, which no listing reaches, and expires after
       `rgw_olh_pending_timeout_sec`
       ([rgw_rados.cc:8904-8934](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8904-L8934),
       [rgw_rados.cc:9650-9680](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9650-L9680)).
       So the marker is expected to be a new rgw-go object, such as an omap object per bucket that
       names the versions, which radosgw neither reads nor removes. That is a format addition:
       record it in `docs/rados-format-opportunities.md`, with what a mixed fleet's radosgw
       DeleteBucket then leaves. Cost: a write on each path rgw-go observes, which are rare. A
       gateway stop or an unknown outcome needs the marker written before the step that can leave
       the state, a write on every versioned write or delete; otherwise those cases stay uncovered,
       and the task says which.
     - (B) Lifecycle and DeleteBucket's emptiness check read the instance-entry namespace through
       `bi_list` and take an instance entry with no list entry for a version in this state. That
       conflicts with D8, which keeps `bi_list` out of scope, and needs the owner's ruling before it
       is chosen. Cost: for DeleteBucket, a bucket whose listing reads empty holds only such
       entries, so one `bi_list` read per shard; for lifecycle, a scan of every instance entry on
       each pass, joined against the listing, at least doubling the pass's index reads.
     - DeleteBucket's detector needs neither design: it is each shard's recount, which still
       counts such a version, and not the stored header, which drifts (Behaviour 6). A count that
       drifts from the index for other reasons must not make a bucket undeletable. (A) or (B)
       stays the choice for lifecycle and the listing.
  4. radosgw reaches the state too, through its failed links and its own stranded writes, and
     handles it as rgw-go does today. Its DeleteBucket checks through the listing, never `bi_list`:
     `RadosBucket::remove` lists the bucket's versions unordered and refuses on any entry
     ([rgw_sal_rados.cc:362-383](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_sal_rados.cc#L362-L383),
     [rgw_sal_rados.cc:379-400](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L379-L400)),
     then `delete_bucket`'s `check_bucket_empty` reads `cls_bucket_list_unordered` again
     ([rgw_rados.cc:5259-5304](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L5259-L5304)
     and [rgw_rados.cc:5320-5325](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L5320-L5325),
     [rgw_rados.cc:5723-5768](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L5723-L5768)
     and [rgw_rados.cc:5945-5950](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L5945-L5950)),
     whose class method drops every key in the 0x80 namespace
     ([cls_rgw.cc:194-244](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L194-L244),
     [cls_rgw.cc:244-294](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L244-L294)).
     Its lifecycle lists through `bucket->list` with `list_versions`
     ([rgw_lc.cc:365-380](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L365-L380),
     [rgw_lc.cc:365-380](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L365-L380)).
     The class accounts a version on its instance entry's ADD
     ([cls_rgw.cc:1168-1185](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1168-L1185),
     [cls_rgw.cc:1301-1318](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1301-L1318)).
     Its only reader of instance entries, `radosgw-admin bucket check unlinked`, reads `bi_list`
     and takes only never-linked entries, at `versioned_epoch` 0
     ([rgw_bucket.cc:690-762](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_bucket.cc#L690-L762),
     [rgw_bucket.cc:841-913](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_bucket.cc#L841-L913)),
     so it reaches the failed-write case alone.
  5. Record the differences in `docs/differences.md`: DeleteBucket's refusal or removal where
     radosgw deletes the bucket and leaves the objects; lifecycle acting on, or the listing
     showing, a version radosgw leaves unlisted; what a mixed fleet's radosgw still leaves; and
     the marker under (A) or the `bi_list` reads under (B). Add or update the registry entry for
     radosgw's DeleteBucket leaving such a version's objects, mechanism only, and update the
     differences entries in Behaviour 1 that say nothing finds the version.
  6. Three PRs, in this order:
     - Behaviour 7's PR first. It changes the delete path and needs neither design. It lands
       before the DeleteBucket PR because the DeleteBucket check refuses a bucket that a counted
       record with no data occupies, and an S3 user reaches that record through Behaviour 7's
       path, a null delete under contention on a suspended bucket (Behaviour 1's last path). With
       the check in place and Behaviour 7 not, that bucket refuses DeleteBucket for good, through
       S3 and through the admin index fix, whose recount keeps the record (V24's adversarial
       review, V24-2, its probe P4 on both releases).
     - The DeleteBucket PR: DeleteBucket never deletes a bucket while such a version exists. A
       deleted bucket turns a repairable state into a permanent one, so this goes before lifecycle
       and the listing. Its design, chosen in its review:
       - DeleteBucket decides on each shard's recount, the calculated header the class's
         read-only check-index op returns, summed over every category, never on the stored
         header
         ([cls_rgw.cc:3035-3114](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L3035-L3114)
         and [cls_rgw.cc:3128-3140](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L3128-L3140),
         [cls_rgw.cc:3361-3434](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L3361-L3434)
         and [cls_rgw.cc:3461-3478](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L3461-L3478)).
         The stored header drifts above the index through ordinary traffic. The class's link of
         a null delete marker over null data writes the marker's instance entry with `exists`
         kept from the data it read, since `init_as_delete_marker` resets the key, flags, meta
         and tag and leaves `exists`
         ([cls_rgw.cc:1350-1358](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1350-L1358),
         [cls_rgw.cc:1476-1484](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1476-L1484)).
         The header never counted that entry, but the recount and a reshard do, and the
         marker's later removal takes the entry without uncounting it
         ([cls_rgw.cc:1970-1978](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1970-L1978),
         [cls_rgw.cc:2117-2126](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2117-L2126)).
         So a suspended bucket's DELETE, then a reshard or the admin index fix, then the
         marker's removal, leave the header one above an empty index, and a check on the stored
         header would refuse that empty bucket until another admin fix (V24's task review, F1,
         reproduced in the emulator on both releases). The recount counts only entries that
         exist.
       - The check-index op is the one rgw-go's admin index check already sends (`CheckIndex`,
         `internal/driver/bucketadmin.go:139`). It writes nothing and returns counts, not entries,
         so D8's exclusion of `bi_list`, `bi_put` and the repair paths does not reach it. The
         rebuild op is never sent. A bucket whose listing reads empty holds only unlisted
         entries, so the recount's scan is small.
       - It counts the generation the removal removes, read after the removal's claim has read
         the bucket instance again (`claimRemoval`, `internal/driver/bucketadmin.go:903`). A
         reshard that commits between DeleteBucket's first read and its claim moves the index to
         a new generation. A count of the old one reads its removed shards, and the removal then
         takes the new one with the version's entry and leaves its head (V24's adversarial
         review, V24-1, its probe P1 on both releases). When the generation differs from the one
         the listing read, DeleteBucket lists and counts again on the new one, or refuses; the PR
         records which. A shard that cannot be read refuses, and never counts as 0.
       - An admin bucket removal with `purge-objects` (`DeleteBucketWithChildren`,
         `internal/op/adminpurge.go:119`) skips the check and keeps today's behaviour. That is the
         operator's way out. A counted record with no data that no S3 request reaches stays
         possible after Behaviour 7: a gateway stop between a version's head removal and its
         awaited completion, a failed awaited completion, or a radosgw that drops a failed
         completion. The admin index fix's recount keeps such a record, and without the way out
         rgw-go would offer none, since its admin removal goes through the same DeleteBucket
         (`internal/op/adminpurge.go:31`, `internal/op/adminpurge.go:126`). What the purge leaves
         is radosgw's behaviour, already in the registry. S3 DeleteBucket and an admin removal
         without `purge-objects` refuse. `docs/differences.md` says so.
     - The lifecycle-and-listing PR last, with its design, (A) or (B). Once the DeleteBucket check
       has landed, a bucket holding an unlisted version refuses DeleteBucket, and its owner cannot
       always name that version through S3: no one holds the id of a version whose lifecycle
       expiration was refused and whose relist ended (V24's adversarial review, V24-3). So this
       PR must make every unlisted version reachable through S3, by the listing or by lifecycle,
       or the bucket stays undeletable through S3 for its owner. Until it lands, the admin
       removal with `purge-objects` is the only way out, and `docs/differences.md` says so.
  7. A delete naming `null` leaves no counted, unreachable index record. Behaviour 1's last path
     leaves no version stored, so Behaviour 2 does not reach it; this does. When the delete takes
     a null delete marker over null data an earlier refusal kept, it does one of two things,
     chosen by the implementer:
     - leaves the data whole: head, tails and instance record kept and counted, the data
       readable and deletable by `null`, the next delete naming `null` taking it, as the same
       state with an older version below already behaves. The key's replay keeps the head where
       the delete's own refusal or lock kept the null data (`keepHead`,
       `internal/driver/olh_head.go:609-617`); here the refusal was an earlier delete's and is no
       longer in the log, so a replay about to clear the OLH cannot tell from its log alone that
       the data is there. What the key reads after the marker goes, the data neither listed nor
       current, is Behaviour 2's state and is handled there;
     - or takes the data in that same delete: its head, its tails, and its instance record with
       the header's count, so that nothing of the key stays.

     Either way no step leaves the record counted with its head gone, and no tail without a
     record naming it. radosgw never reaches the state. The class's link of a null delete marker
     over null data logs the data's removal in the same call
     ([cls_rgw.cc:1705-1734](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1705-L1734)
     and [cls_rgw.cc:1806-1810](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1806-L1810),
     [cls_rgw.cc:1846-1875](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1846-L1875)
     and [cls_rgw.cc:1947-1951](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1947-L1951)),
     and the replay removes it through `delete_obj` with no write-tag guard
     ([rgw_rados.cc:8530-8541](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8530-L8541),
     [rgw_rados.cc:9276-9287](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9276-L9287)),
     so the data, its record and its count are gone once the marker is written, and a delete
     naming `null` takes the marker alone and leaves nothing of the key. The first choice
     differs from radosgw in keeping data radosgw removed, as the refusal already does; the
     second ends where radosgw does, one delete later. Record the choice, what the key reads in
     between, and the state Behaviour 1 records today in `docs/differences.md`, in the entry "A
     refused versioned removal re-links the version, unless a newer null write owns the null
     key".
- **Specs:** against the emulator, on both releases, a version left unlisted by each path in
  Behaviour 1 that the emulator can reach:
  - lifecycle expires it under a rule that matches it, or it is listed again;
  - DeleteBucket answers 409 BucketNotEmpty, or deletes the bucket with no head, tail or index
    object of the version left;
  - the bucket's stats match the versions stored, before and after;
  - a bucket with no such version deletes, and a pass expires, as today;
  - the DeleteBucket PR's design (Behaviour 6): a null delete marker over null data, counted by
    a reshard or by the admin index fix and then removed, leaves a bucket that deletes; a reshard
    that commits between DeleteBucket's first read and its count leaves no head behind; an admin
    removal with `purge-objects` deletes a bucket a counted record with no data occupies, and S3
    DeleteBucket and an admin removal without it refuse that bucket;
  - Behaviour 7, on both releases against the emulator: on a suspended bucket, null data as the
    key's only version; a null delete refused (an attr write moves the head's write tag after
    the marker's link) and its relist ended before it links (stopped after the refused replay's
    trim, as J5 stops it); then deletes naming `null` until nothing of the key is left. After
    each delete the header's count and `BucketStats` equal the versions stored, every instance
    record of the key belongs to a version a read or a delete naming it reaches, and no tail is
    left that no record names; at the end no head, tail or index record of the key remains.
    Under the first choice a read naming `null` returns the data until the delete that takes
    it. The same with an older version below the null data, whose outcome stays as today; and
    V21's adversarial harness's J5 leaves no record on its null-only row.
- **Depends on:** V21. **PR split:** three (Behaviour 6 and 7). **Brief:** about 50 lines.

### Task V25: a link refuses to land on a version a replay's removal has prepared

Found by V22's task review (its central question 3) and its adversarial review (M3), and numbered
here before V22 merged (PP-12). V22's compare refuses a replay's removal of a version a link has
listed since the replay read the log, up to the removal's first index op, its prepare. V22's
Behaviour 4 leaves the window after it open: between the removal's prepare and its index
completion, a link of the version finds the instance entry still present, and the completion then
removes that entry from under the list entry the link wrote. The class's link refuses only a
missing instance entry and never reads its `exists`
([cls_rgw.cc:1668-1688](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1668-L1688),
[cls_rgw.cc:1809-1829](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1809-L1829)),
so it also lands on the placeholder a removal's prepare writes, `exists=false`, once another removal
has taken the version
([cls_rgw.cc:851-867](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L851-L867),
[cls_rgw.cc:983-999](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L983-L999)).
rgw-go's own linkers give way only to a removal still in the OLH log, and any replay that read the
removal trims it on its own schedule: the refused delete's own replay before its relist, or a third
request's. The linkers that land there:

- the relist (`relistVersion`, `relistBelow` and `relistNull`,
  `internal/driver/delete_versioned.go:698`, `internal/driver/delete_versioned.go:1001` and
  `internal/driver/delete_versioned.go:818`): its epoch-0 link at a new epoch, and its link at the
  version's own epoch, below a newer one;
- the CompleteMultipartUpload retry (`linkRecorded`, `internal/driver/mp_complete.go:628`), which
  gives way to nothing: a link at a new epoch, and once the OLH names the gone version the key
  refuses every later write (V22's case 1);
- the lifecycle transition (`linkVersion`, `internal/driver/put_versioned.go:337`, which V22 routes
  through its `linkTransition`): at the OLH entry's epoch when the entry names the version, and at
  the version's own epoch, below the entry's, otherwise;
- `keepCurrent` (`internal/driver/lc_transition.go:230`): at the OLH entry's epoch as read;
- radosgw, on a mixed fleet: its PutObject naming a version's id, and its transition.

Each leaves a list entry naming a version whose head and instance entry are gone. A link at a new
epoch or at the entry's own logs a LINK_OLH, and only one below the entry's takes the class's
old-epoch path
([cls_rgw.cc:1503-1517](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1503-L1517),
[cls_rgw.cc:1630-1644](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1630-L1644)),
so after a logged link the next replay also leaves the OLH naming the gone version. The figures,
from V21's adversarial harness, 2000 schedules a random family:
- V22's gate, main to V22's head: the retry's link landing after a removal's prepare (B6,
  scripted) 2 and 2; the swept write after a disconnect (I7) 24 and 8; the random families H1 2
  and 4, H3 2 and 1, H4 6 and 18, G8 5 and 6, H5 0 and 1, H10 0 and 3. Every dangling schedule
  V22's review and implementer traced lies in this window.
- V22's adversarial review: a link of the version at each op of a replay's removal (P1) is kept up
  to the prepare and dangles after it, on both releases, with the OLH naming the version for a link
  at a new epoch. A transition racing a plain delete and two readers (T1) dangles in 12 runs at
  V22's head, 776 at main, with the transition and the delete both answered success. H5's seed
  1877 is the placeholder case: the relist linked a version whose head had been gone for 31 steps.
- Part of H4's and H10's rise is V22's own retry (its adversarial I2), which V22's fix round removes.

- **Layer:** driver. **Files:** the relists (`refusedRemoval`, `relistVersion`, `relinkOLHEntry`,
  `relistNull`, `relinkOrMove` and `relistBelow` in `internal/driver/delete_versioned.go`); the
  CompleteMultipartUpload retry (`linkRecorded` and `recordedLinkState` in
  `internal/driver/mp_complete.go`); the link's compare (`olhLinkFields` and `linkOLH` in
  `internal/driver/olh_index.go`, and `setOLH` in `internal/driver/olh.go`, which fills it); the
  transition's link (`linkVersion` and V22's `linkTransition` in `internal/driver/put_versioned.go`)
  and `keepCurrent` in `internal/driver/lc_transition.go`; for Behaviour 8, the record and its
  consumer V24's lifecycle-and-listing PR adds (`recordUnlisted` and `settleUnlisted`) and the
  stale-op rule the consumer shares with the abort (`expireStaleOps` in
  `internal/driver/mp_abort.go`); their specs, the consumer's in
  `internal/driver/delete_expect_test.go` and `internal/driver/lc_bucket_test.go`;
  `docs/differences.md`, the window's paragraph in the entry V22 adds ("A replay keeps a version a
  link lists again after it read the log") and the entry V24's lifecycle-and-listing PR adds
  ("Lifecycle and a bucket delete list again a version a replay left unlisted"); the registry entry
  V20 adds. **Cluster:** no.
- **Security boundary:** the versioned write's replay; adversarial review applies (PP-14).
- **Behaviour:**
  1. Each rgw-go linker reads the version's instance entry before its link, after its own last
     write to that entry: the relist's reset (`resetCurrent`), the retry's completion
     (`completeRecordedEntry`), the transition's index completion. For the null version that is
     the null data's entry (`nullDataEntry`). An entry with `exists=false` names no data, a
     removal's placeholder or a completed DEL with another op still pending, and the linker gives
     way to it at once. A pending DEL in the entry is a removal between its prepare and its
     completion, and the linker waits it out. A pending op of any other kind is no reason to wait.
  2. The wait's bound is the one a conditional link spends on another writer's pending
     modification: `olhCondBackoffs` backoffs, 318-635 ms in all
     (`internal/driver/olh_cond.go:34-47`), the entry read again after each. A DEL still pending
     after them is given way to, whatever its age. No linker cancels it or links over it. The
     reasons:
     - A live removal sends its head removal and its awaited completion straight after its
       prepare (`removeInstance`, `internal/driver/olh.go:1012`), so a DEL that outlives the wait
       is a removal stalled in RADOS or one whose gateway stopped.
     - The two cannot be told apart before the shard's tag timeout, 120 s by default. That is the
       age at which the class drops a pending op
       ([cls_rgw.cc:2215-2226](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L2215-L2226)
       and [cls_rgw.cc:2283-2299](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L2283-L2299),
       [cls_rgw.cc:2385-2396](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2385-L2396)
       and [cls_rgw.cc:2453-2469](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2453-L2469)),
       and at which rgw-go's abort cancels one (`expireStaleOps`,
       `internal/driver/mp_abort.go:387-435`). Waiting that long would hold a client's
       CompleteMultipartUpload, and a lifecycle pass, for two minutes a version.
     - A link over a removal that then completes leaves the state this task closes. Giving way
       leaves the version as its removal leaves it: removed, or, where the remover stopped, stored
       and unlisted. V24's lifecycle-and-listing PR finds that state only through its record, its
       design (A), so the give-way records the version, and the record's consumer clears the stale
       DEL before it lists the version again (Behaviour 8). The cost is then a version unlisted
       until lifecycle's next pass or a bucket delete lists it again, as a relist that ends before
       its link leaves one. Add the path to V24's Behaviour 1 and to the differences entry it
       updates.
  3. A link that gives way answers:
     - the relist: as its give-way to a logged removal does today, nothing linked, and the delete
       whose refusal it serves answers ConcurrentModification
       (`internal/driver/delete_versioned.go:241`);
     - the CompleteMultipartUpload retry: InternalError with the upload's meta object kept, as
       `linkRecorded` answers today for a version it cannot link safely, its instance entry gone or
       another write's (`internal/driver/mp_complete.go:622-627`). Success would answer for a
       version a delete is removing, and would retire the meta object, the record of the upload's
       parts, while a stopped remover may have left those parts stored. Such a version is then on
       V24's path of a CompleteMultipartUpload version whose link never landed;
     - the transition and `keepCurrent`: as V22's give-way, NoSuchKey, with no `keepCurrent` and no
       notification, the rewritten head and its tails going to the removal.
  4. Each link compares the instance entry as read in Behaviour 1, beside the OLH entry the relist's,
     the transition's and `keepCurrent`'s links already compare (`olhRelink.olh`). A removal's
     prepare that lands after the read adds its DEL to the entry, so the link's op is refused with
     nothing linked, and the next round reads the DEL. `olhLinkFields.cmp` holds one entry today
     (`internal/driver/olh_index.go:63-70`), which `linkOLH` sends as one OMAP_CMP
     (`internal/driver/olh_index.go:120-125`) and `setOLH` fills from `olhRelink.olh` alone
     (`internal/driver/olh.go:280-283`). It becomes a list, each entry an OMAP_CMP in the link's op
     before the class's call. The CompleteMultipartUpload retry, which compares nothing today
     (`internal/driver/mp_complete.go:654-655`), sends the instance entry alone. A refused compare is
     the linker's lost round, under its existing bound; the retry reads again within Behaviour 2's
     wait and answers as a give-way once it is spent.
  5. The relist refuses a version whose head is gone. `relistVersion` reads the head at a round's
     start only (`internal/driver/delete_versioned.go:710-713`), and `relistBelow` not at all
     (`internal/driver/delete_versioned.go:1001-1057`). Each relist reads the head after its
     instance-entry read and before its link, the OLH head's data for the null version as
     `relistNull` reads it (`internal/driver/delete_versioned.go:839-841`), and links nothing when
     it is gone.
  6. radosgw's links are recorded, not fixed. They carry neither the wait nor the compare, and
     radosgw's replay compares nothing, so a mixed fleet keeps the window. The differences entry's
     window paragraph and the registry entry V20 adds then name radosgw's links and replay alone as
     reaching it, and the registry entry's rgw-go line says what rgw-go's linkers do.
  7. The RADOS state stays radosgw's. The compares are OMAP_CMP steps in the link's own op, as V21's
     and V22's are, and the waits only read. Nothing is written that radosgw does not write. The
     records Behaviour 8 adds go to V24's rgw-go object, which radosgw neither reads nor removes.
  8. A give-way to a DEL still pending after the wait records the version, and the record's
     consumer clears that DEL first. Under V24's design (A) lifecycle and DeleteBucket find only a
     recorded version, and they list it again through the same relist, which would give way to the
     same DEL:
     - The relist, the transition and `keepCurrent`, each when it gives way to such a DEL, record
       the version in V24's unlisted-version object, `rgw-go.unlisted.<bucket id>` (`recordUnlisted`
       at that PR's head), under the OLH tag the linker read, before it answers. A version already
       recorded, as a refused removal's is by its replay, takes the newer record. A record that
       fails is logged, the linker answers as Behaviour 3 says, and the version is left as the
       removal left it. The CompleteMultipartUpload retry records nothing: the upload's meta object
       names its version, and the abort removes that version once it has canceled the ops pending
       past the tag timeout (`expireStaleOps`), V24's Behaviour 1 path 4.
     - The consumer (`settleUnlisted` at that PR's head), before it lists a recorded version again,
       reads the version's instance entry and cancels each DEL pending there past the shard's tag
       timeout, as `expireStaleOps` does for the abort (`internal/driver/mp_abort.go:387-435`), the
       rule the class's `dir_suggest_changes` applies
       ([cls_rgw.cc:2283-2299](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L2283-L2299),
       [cls_rgw.cc:2453-2469](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2453-L2469)).
       A DEL younger than that keeps the record for the next pass, and no relist runs. V24's settle
       age, 512 s, is past the default tag timeout, 120 s, so the DEL a give-way met is stale once
       its record settles, unless a later removal sent another. V26 then holds this cancel to the
       entry's own version, as it holds the abort's.
     - Trace, before implementing: a version still stored under a stale DEL is one no delete was
       answered success for, since a delete answers once its own replay has carried out its
       removal. A path on which a delete was answered and its version stays has the consumer finish
       that removal instead of listing the version again, and the specs pin it.
- **Specs:** on both releases, against the emulator:
  - each linker landing in the window: a replay's removal of X parked after its prepare, once
    before its head removal and once before its index completion; then the relist, its epoch-0
    link and its link below a newer version, the CompleteMultipartUpload retry, the transition and
    `keepCurrent`; then the removal completes. Each waits, then gives way and answers as Behaviour 3
    says. X's head, instance entry and list entry are gone, the OLH does not name X, and a later
    PutObject of the key links;
  - the same with the removal held past the wait: each gives way, linking nothing, and X stays as
    the removal left it, recorded but for the CompleteMultipartUpload retry;
  - Behaviour 8, with the remover stopped after its prepare, for the relist, the transition and
    `keepCurrent`, and for the null version through the relist and the transition: once the record
    has settled, lifecycle's pass, and DeleteBucket on a bucket without lifecycle, cancel the stale
    DEL and list X again, DeleteBucket answering 409 as V24's consumer does. X is then removed by
    its id and the bucket deletes, with no head, tail, index entry or record of X left;
  - a DEL younger than the tag timeout when the consumer runs: the record is kept and nothing is
    listed, and the next pass, once the DEL is stale, lists X;
  - the remover's DEL completing after the give-way: the consumer finds X gone and drops the
    record; a record that fails: the linker answers as Behaviour 3 says, logged;
  - the CompleteMultipartUpload retry's give-way records nothing, and the abort, past the tag
    timeout, removes X;
  - H5's shape: another removal takes X, a third request's replay trims, a later removal's prepare
    writes the placeholder, and the relist gives way;
  - a prepare landing between a linker's instance-entry read and its link refuses that link, for
    each linker; an attr write's pending op on the entry does not make it wait;
  - the relist of a version whose head is gone while its instance entry stays links nothing;
  - the null version, through the relist and the transition;
  - V21's adversarial harness with V22's adversarial additions, 2000 schedules a family: the
    dangling outcomes from Behaviour 4's window go to zero in every family rgw-go runs alone (B6,
    G8, H1, H3, H4, H5, H10, I7, T1, T4, T5, and P1's positions after the prepare), and any left is
    traced and shown to lie outside the window. The radosgw families (D1 to D3) stay as V22 leaves
    them. No new acknowledged-and-kept outcome and no new lost delete;
  - V22's specs, and the interleavings V21's specs pin, stay as they are.
- **Depends on:** V22 and V23. V23 also reworks the relists in `internal/driver/delete_versioned.go`,
  so V25 goes after it. V24's lifecycle-and-listing PR too, whose record and consumer Behaviour 8
  extends, so V25 goes after V24, as the Order has it. **PR:** one. **Brief:** about 70 lines.

### Task V26: a cancel never lowers an index entry below a landed write

Found by V22's adversarial review in its re-run 2 (R2-M1), pre-existing on main, and numbered here
before V22 merged (PP-12). An attribute write whose head write finds the version's head gone cancels
its index op (`internal/driver/attrs.go:92-98`), and `indexOp.cancel` sends pool -1 and epoch 0
whatever the entry holds (`internal/driver/indexop.go:383-388`). The class copies the op's version
onto the entry on every completion and cancel
([cls_rgw.cc:1080-1094](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1080-L1094),
[cls_rgw.cc:1215-1229](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1215-L1229)).
A removal that completed first left the entry not existing, at the removal's version, because
another write's op was still pending on it
([cls_rgw.cc:1141-1166](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1141-L1166),
[cls_rgw.cc:1275-1299](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1275-L1299)).
The cancel lowers that version, so the other write's late ADD completion is no longer stale and runs
as a fresh ADD
([cls_rgw.cc:1168-1198](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1168-L1198),
[cls_rgw.cc:1301-1329](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1301-L1329)):
the instance entry is made again with no head, unlisted and counted in the bucket's stats. V22's
adversarial schedule Q3 (writer AW) leaves it at main and at each V22 head the review ran. radosgw's
`set_attrs` cancels at the same version
([rgw_rados.cc:6912-6917](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L6912-L6917)
and [rgw_rados.cc:9749-9759](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L9749-L9759),
[rgw_rados.cc:7562-7567](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7562-L7567)
and [rgw_rados.cc:10485-10495](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L10485-L10495)).
The copy is the registry's "cls_rgw complete_op writes a stale epoch back when it cancels", whose
rgw-go line says no client can work around it. By reading, two more forms reach the same state:

- a completion the class skips as stale copies its own, older version the same way, so two late
  completions of in-place rewrites after a removal, the older landing first, make the entry again
  with no cancel at all (that registry entry's completions at 10, 5 and 7);
- a cancel at a version read before a later write landed: `resetCurrent` cancels at the version its
  caller read before its prepare (`internal/driver/delete_versioned.go:1086-1103`, read at
  `internal/driver/delete_versioned.go:1007`) and compares nothing.

V22 restores the transition's head check, which covers the transition's own completion (its R2-I1).
The paths on which rgw-go cancels an index op or completes one late, each opened at origin/main:

- `indexOp.cancel`'s callers: an attribute write's failed head write (`SetObjectAttrs`,
  `internal/driver/attrs.go:97`); a failed head write through `writeMeta`, which PutObject,
  CopyObject, the lifecycle transition's raw copy, CompleteMultipartUpload's head write, the
  multipart meta object's create and a part's head all take (`cancelWrite`,
  `internal/driver/headwrite.go:595-600`, from `internal/driver/headwrite.go:408`);
  `writeNullVersion`'s failure (`internal/driver/put_versioned.go:234-239`); `linkVersion` after a
  completion that failed (`internal/driver/put_versioned.go:343-345`); a refused head removal, which
  a plain delete, a part's removal and a replay's removal of a version reach (`removeHeadFlagged`,
  `internal/driver/delete.go:169-170`); CompleteMultipartUpload's cleanup and the abort's removal of
  the meta object (`deleteMeta`, `internal/driver/mp_cleanup.go:692-696`); the abort's refused
  removal of a recorded version (`removeUnlinkedRecorded`, `internal/driver/mp_abort.go:341-343`);
  the bypass purge's failed head removal (`bypassPurge.head`, `internal/driver/purge.go:299-300`);
- the cancels sent around it: the abort's cancel of an op past the tag timeout, at pool -1
  (`expireStaleOps`, `internal/driver/mp_abort.go:425-428`), and `resetCurrent`'s;
- the completions of a write that rewrites an existing entry in place, which can land after a
  removal: `SetObjectAttrs`'s (`internal/driver/attrs.go:100`), unawaited but for a plain or null
  entry on a versioned bucket (`completePlain`, `internal/driver/headwrite.go:575-584`); a write
  with `headWrite.expect`, a copy onto itself or the transition's raw copy
  (`internal/driver/copy.go:309`, `internal/driver/copy.go:575`), completed by `linkVersion` when
  versioned (`internal/driver/put_versioned.go:338-339`) and by `completePlain` otherwise; and
  `completeRecordedEntry`, at the head's epoch as read, from CompleteMultipartUpload's retry and from
  the abort (`internal/driver/mp_complete.go:669-677`, called at
  `internal/driver/mp_complete.go:649-653` and `internal/driver/mp_abort.go:327-331`).

Two need no change. The abort's compared DEL (`completeDelAsRead`,
`internal/driver/mp_abort.go:479-500`) runs only once the abort's prepare is the one op pending
(`removableEntry`, `internal/driver/mp_abort.go:508-518`), so it removes the entry. `deleteObjIndex`'s
untagged DEL at pool -1 (`internal/driver/delete.go:221-230`) is radosgw's `delete_obj_index`
([rgw_rados.cc:6173-6190](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L6173-L6190),
[rgw_rados.cc:6787-6804](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6787-L6804)).
The bypass purge sends it ahead of the bucket's removal, which takes the shards whole. The listing's
sweep sends it for a completed upload's part entries (`internal/driver/list.go:795-812`), whose
heads are that object's tails and stay, so an entry a late completion makes again names a stored
object.

- **Layer:** driver. **Files:** `indexOp.cancel` in `internal/driver/indexop.go` and its callers
  above; `expireStaleOps` in `internal/driver/mp_abort.go`; `resetCurrent` in
  `internal/driver/delete_versioned.go`; `SetObjectAttrs` in `internal/driver/attrs.go` and
  `completePlain` in `internal/driver/headwrite.go`; `linkVersion` in
  `internal/driver/put_versioned.go`; `completeRecordedEntry` in `internal/driver/mp_complete.go`;
  their specs, `indexop_test.go`'s pin of the cancel's request and of the version it leaves among
  them; `docs/differences.md`, the text V22 marks "tracked by V26" and the entry "A versioned write
  awaits its index completion"; in `docs/ceph-upstream-bugs.md`, the rgw-go lines of "cls_rgw
  complete_op writes a stale epoch back when it cancels" and of the entry V22 adds for R2-M1.
  **Cluster:** no.
- **Security boundary:** the versioned write's replay and quota accounting; adversarial review
  applies (PP-14). Every text on the quota side, here, in `docs/differences.md` and in the registry,
  states the mechanism only.
- **Behaviour:**
  1. No rgw-go cancel changes an index entry's version. Each reads the entry the cancel changes, as
     `bi_get` answers it (`getInstanceEntryBI`, `internal/driver/olh_index.go:301-312`), and sends
     its cancel at the entry's own version, with an OMAP_CMP of the bytes read ahead of the class
     call in the same op, as the abort's DEL compares (`completeDelAsRead`). A write that lands
     between the read and the cancel changes the bytes: the compare refuses the cancel, nothing is
     written, and the next round reads again.
  2. No completion of an in-place rewrite, the third group above, lands on an entry a removal has
     completed on, or below the version the entry holds. After its head write the writer reads the
     entry the same way and sends:
     - nothing when its op's tag is no longer pending, a completion the class would refuse
       ([cls_rgw.cc:1066-1073](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1066-L1073),
       [cls_rgw.cc:1201-1208](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1201-L1208));
     - Behaviour 1's cancel when the entry does not exist, a removal having completed since the
       prepare, or holds a version at or above the completion's on its pool;
     - otherwise its ADD, under the same compare.

     A removal completing between the read and the ADD refuses the ADD, and the next round cancels.
     Each writer answers as today, except that a rewrite with `headWrite.expect` whose completion
     cancels links nothing and answers as its head write answers a removed head, NoSuchKey
     (`internal/driver/headwrite.go:602-605`), and `completeRecordedEntry`'s callers answer as for a
     completion that fails (`internal/driver/mp_complete.go:650-652`,
     `internal/driver/mp_abort.go:328-330`). `SetObjectAttrs` then awaits its completion of every
     entry, as it already awaits one of a plain or null entry on a versioned bucket.
  3. The mechanism, and why. Each candidate leaves a path:
     - a cancel at the version of the head the op read: another write of that head between the read
       and the removal carries a higher version, and its late completion still passes;
     - a cancel at the entry's version read earlier and not compared, `resetCurrent`'s form: a
       removal completing between the read and the cancel is lowered just the same;
     - a head check before the completion: the head is in a data pool and the entry in an index
       shard, so no op covers both, and a removal between the check and the completion leaves the
       state; the cancels still lower the version;
     - the cancels alone: a stale completion lowers it too, the first form above.

     Reading the entry and comparing it in the op that changes it closes each, and is the shape the
     abort's DEL has and V25's link compares take. Its cost is one index read for each cancel, a
     failure path, and for each in-place completion, and `SetObjectAttrs` waits for its completion
     of a version. The completions of PutObject's, CopyObject's and CompleteMultipartUpload's other
     writes of a plain or null key keep their form: a read before each would add an index read to
     every such write. The registry entry records that they meet the defect as radosgw's do, and
     Behaviour 8 traces the null version.
  4. Each read and its op run within `olhAttempts` rounds (`internal/driver/olh.go:33`). Once those
     are spent the op is left pending and logged, as a head write that times out leaves its prepare
     (`internal/driver/headwrite.go:596-600`), and the request answers as it does today. A cancel
     after a prepare whose outcome is unknown must still reach the shard after that prepare, as
     `resetCurrent` requires today (`internal/driver/delete_versioned.go:1076-1085`), with the read
     in between. The read and the op run past the request's end, as the unawaited cancel does today.
  5. The RADOS state stays one radosgw writes. A cancel at the entry's own version writes the bytes
     the class writes for a completion it skips as stale at the entry's own epoch, an ADD is
     radosgw's ADD, and the compare writes nothing. Record in `docs/differences.md` the cancel's
     version, the entry's where radosgw's is pool -1 and epoch 0, and the awaited attribute write;
     replace the text V22 marks "tracked by V26".
  6. radosgw is recorded, not fixed. Its `set_attrs` cancel, every `cls_obj_complete_cancel` it
     sends and its stale completions still lower an entry's version on a mixed fleet. An rgw-go
     completion that meets an entry radosgw lowered still reads it as not existing and cancels, so
     the state then needs radosgw's own late completion. The two registry entries' rgw-go lines say
     what rgw-go does now and what still meets the defect (Behaviour 3). The counted entry with no
     head that radosgw can still leave goes among the counted records with no data that no S3
     request reaches, which V24's DeleteBucket PR names.
  7. V22's transition head check stays.
  8. Trace, before implementing: the null version. Its data's head is the OLH head, which a removal
     leaves, so a second removal of its data is refused by its guard and cancels (V22's adversarial
     R1.4, not run). Say whether a late completion then meets the null data's entry; the specs pin
     the answer.
- **Specs:** on both releases, against the emulator:
  - Q3: an attribute write AW reads version X's head, prepares and writes it, its completion held; a
    second attribute write AW2 reads X's head and prepares; a delete unlinks X, and its replay removes
    the head and completes; AW2's head write finds the head gone and cancels; then AW's completion
    lands. X's head, instance entry and list entry are gone, and the bucket's stats count the
    versions stored. The same with the transition as the late writer (V22's R2-I1);
  - two late completions: AW and AW3 both write X's head before the removal, and their completions
    land after it, AW's first. X is gone as in Q3;
  - each cancel of the first two groups, landing on an entry a removal completed on while another op
    stayed pending, leaves the entry at the removal's version; the cancel of a failed write's fresh
    entry still removes it;
  - a prepare, a completion or a removal landing between a cancel's or a completion's read and its op
    refuses the op, and the next round acts on the entry as it then is; the spent bound leaves the op
    pending and the answer as today;
  - CompleteMultipartUpload's retry and the abort, `completeRecordedEntry` after a removal of the
    recorded version completed: no entry is made again, and each answers as Behaviour 2 says;
  - an attribute write that meets no race completes as today, its entry's meta and the stats as
    today;
  - the null version, as Behaviour 8 finds;
  - Q3 with AW2's cancel sent as radosgw's, at pool -1 and epoch 0: with AW's completion rgw-go's,
    X is gone; with a late completion sent as radosgw's, the state stays, pinned as recorded;
  - V21's adversarial harness with V22's adversarial additions, Q3 and RS1 among them, 2000
    schedules a family, its radosgw model completing an ENOENT removal as radosgw does (V22's
    adversarial R1.4): no instance entry without its head in any family rgw-go runs alone, Q/P3 and
    Q3 gone for both late writers, the radosgw families as V25 leaves them, and no new dangling,
    lost-delete or acknowledged-and-kept outcome;
  - V22's and V25's specs, and the interleavings V21's specs pin, stay as they are.
- **Depends on:** V22 and V25. V25 changes `linkVersion`, the CompleteMultipartUpload retry around
  `completeRecordedEntry` and the relist around `resetCurrent`, and its linkers read the instance
  entry these completions and cancels write, so V26 goes after it. V26 does not gate V24: V24's
  DeleteBucket check refuses a bucket that a counted record with no data occupies (its Behaviour 6),
  the entry this task closes is one, and the admin bucket removal with purge-objects is the
  operator's way out until V26 lands. **PR:** one. **Brief:** about 45 lines.

### Task V27: a delete naming `null` takes the null marker over kept data on an object-lock bucket

Found by V24 Behaviour 7's adversarial review (B7A-5), pre-existing on main, and numbered here
before that PR merged (PP-12). That PR's fix round closed B7A-1 and B7A-4 in the delete naming
`null` and left this one, whose fix lies in the op layer's read and in lifecycle's, outside that
PR's change. On an object-lock bucket no delete naming `null` can take a null delete marker that
sits above null data an earlier refusal kept (V24's Behaviour 1, the refused null delete whose
relist ended). The state arises on a suspended bucket whose versioning, and then object lock, are
enabled later: PutObjectLockConfiguration gives a versioning-enabled bucket the flag
(`internal/op/bucketobjectlock.go:155-159`).

The op layer's lock check reads the version through `StatObject` (`deleteUnderLock`,
`internal/op/objectlock.go:61`). `null` names the key's OLH head, and `headState` answers a head
with OLH attrs and a manifest as the null data (`internal/driver/stat.go:61-71`); the index lookup
that finds a marker runs only for a state that does not exist (`namedVersion`, reached at
`internal/driver/stat.go:50-52`). The store's delete resolves `null` in the class's
`read_key_entry` order, the marker first (`getInstanceEntryBI`,
`internal/driver/delete_versioned.go:334`). So the check reads the data and the delete meets the
marker:

- With a legal hold or a COMPLIANCE retention on the data, `verifyDeleteLock` answers 403
  AccessDenied before any unlink (`internal/op/objectlock.go:65`), though the marker holds no lock.
- Without one, the data's state becomes the removal's `Expect` (`guardRemoval`,
  `internal/op/objectlock.go:156-158`), which `expectRemoval` refuses against the marker's entry,
  "one a delete marker and the other not" (`internal/driver/delete_versioned.go:413-414`). Each of
  `deleteUnderLock`'s retries reads the data again (`internal/op/objectlock.go:60-77`), and the
  lost race is the answer: 204 with nothing removed for DeleteObject
  (`internal/op/deleteobject.go:172-173`), the key's ConcurrentModification for DeleteObjects
  (`internal/op/deleteobjects.go:435-437`).
- Lifecycle's removal of the marker reads it again through `StatObject` and skips it unless that
  state is a marker (`lcBucket.removeVersion`, `internal/driver/lc_actions.go:393-408`). The skip
  comes before the bucket's object-lock check (`internal/driver/lc_actions.go:418`), so lifecycle
  skips the marker on any versioned bucket, with object lock or without. By reading, not run.

The marker, and the key's 404 above the data, stay through S3 and lifecycle. V24 Behaviour 7's
adversarial spec OL, which emulates the op layer, reproduces the case without a lock on both
releases, at main and at that PR's head; the hold's 403 is by reading.

- **Layer:** op and driver. **Files:** the op layer's object-lock check (`deleteUnderLock`,
  `verifyDeleteLock` and `guardRemoval` in `internal/op/objectlock.go`) and the store read it makes
  (`op.ObjectStore` in `internal/op/object.go`, with the fakes and memstore its hot-file row names;
  `objectState`, `headState` and `namedVersion` in `internal/driver/stat.go`); lifecycle's removal
  (`lcBucket.removeVersion` in `internal/driver/lc_actions.go`); their specs, the op layer's over
  the RADOS store in `internal/driver/delete_lock_op_test.go`; `docs/differences.md`, the entry "A
  refused versioned removal re-links the version, unless a newer null write owns the null key",
  where V24 Behaviour 7 records the kept state, and "Lifecycle removes an object only as it was
  listed". **Cluster:** no.
- **Security boundary:** object lock and the versioned delete path; adversarial review applies
  (PP-14). No change may let a delete remove data its lock holds.
- **Behaviour:**
  1. The read that decides a removal naming `null` resolves the null version in the index's order:
     the null delete marker first, then the null data. `deleteUnderLock` makes it for the lock
     check of a delete whose request names `null`, and lifecycle's removal for its re-read of a
     version it lists as `null`. A marker there is answered as `namedVersion`
     answers one: a state that does not exist, `DeleteMarker` set when it is listed, with its
     entry's mtime and versioned_epoch. Otherwise the answer is the head's, the null data with its
     attrs, as today. The marker holds no lock, so the check passes it
     (`internal/op/objectlock.go:107-109`), and its state becomes the removal's `Expect`, which
     `expectRemoval` takes as a marker (`internal/driver/delete_versioned.go:411-418`), the unlink
     comparing the marker's entry as read (`olhMarkerCmp`). V24 Behaviour 7's keep then leaves the
     data whole for the next delete naming `null`, as on a bucket without object lock.
  2. The data's lock still holds when a delete reaches it. With the marker gone the read finds the
     data, the check reads its lock, and a hold or a COMPLIANCE retention answers 403 AccessDenied
     with nothing removed. A removal stays held to what its check read:
     - a delete whose check read the marker, and whose removal finds the data because another
       delete took the marker in between, is refused by `expectRemoval` and checks again on the
       data, within `lockedDeleteRetries`;
     - a delete whose check read the data, and whose removal finds a marker linked since, is
       refused and takes the marker in a later round, as today.

     No order of reads and writes lets an unlink of the data follow a check that read the marker.
     That includes a head with no write tag: a marker read as no version, an unlisted one, carries
     no tag, `expectTag` reads an absent tag as empty
     (`internal/driver/delete_versioned.go:173-175`), and a head without one would pass the
     compare at `internal/driver/delete_versioned.go:420-427`. The implementer shows that path
     refused, or refuses it.
  3. The read adds up to three index reads, D8's `read_key_entry` reads, made only where they can
     change the answer: `null` named on a key whose head carries OLH attrs and holds null data. A
     head without OLH attrs has no version marker, so no null delete marker, and a head with OLH
     attrs and no manifest already goes to `namedVersion`. Every other delete reads as today.
  4. Only the reads that decide a removal of a version named `null` change. A delete naming no
     version on a suspended object-lock bucket, which `lockTarget` also checks as `null`
     (`internal/op/objectlock.go:97-99`), keeps today's read: its null delete marker replaces the
     marker's entry and logs the null data's removal
     ([cls_rgw.cc:1712-1731](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1712-L1731),
     [cls_rgw.cc:1853-1872](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1853-L1872)),
     so the version it removes for good is the data, whose lock it must check, and the index's
     order would hand it the marker's state, which `nullMarkerExpect` then refuses on a head
     holding data (`internal/driver/delete_versioned.go:147-159`).

     `StatObject` stays as it is, so GET and HEAD naming `null`, the attr and lock writes and
     lifecycle's other reads keep reading the head first, as radosgw's `get_obj_state` reads
     `null`: it follows the OLH only for an empty instance and answers -ENOENT only for a head
     without a manifest
     ([rgw_rados.cc:6269](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L6269)
     and [rgw_rados.cc:6427-6433](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L6427-L6433),
     [rgw_rados.cc:6883](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6883)
     and [rgw_rados.cc:7041-7047](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7041-L7047)).
     The reasons:
     - Behaviour 3's index reads would fall on every GET naming `null` of such a key, a read path,
       where only a removal needs them.
     - `StatObject` is the seam V6 produces for L3, WP and C (Seams).
     - What a read naming `null` answers in the kept state, the data, stays as V24 Behaviour 7
       records it.

     So `op.ObjectStore` gains the read beside `StatObject`, and `deleteUnderLock` makes it in
     `StatObject`'s place when the request names a version. For every key but `null` on such a
     head it answers what `StatObject` answers.
  5. Lifecycle's removal makes the same read (`internal/driver/lc_actions.go:393`). A null marker
     above kept data then passes the marker check at `internal/driver/lc_actions.go:398` and is
     removed with that marker as `Expect`, on every versioned bucket, and the data stays whole.
     Lifecycle reaches the data, unlisted once the marker goes, only as V24's lifecycle-and-listing
     PR makes such a version reachable. A listed null data version whose index entry has become a
     marker since the listing reads as no data, and `listedAs` skips it
     (`internal/driver/lc_actions.go:331-332`), where today the delete's `Expect` refuses it later.
     The outcome, a skip, is the same.
  6. radosgw never reaches the state. The class's link of a null delete marker over null data logs
     the data's removal in the same call
     ([cls_rgw.cc:1705-1734](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1705-L1734)
     and [cls_rgw.cc:1806-1810](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1806-L1810),
     [cls_rgw.cc:1846-1875](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1846-L1875)
     and [cls_rgw.cc:1947-1951](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1947-L1951)),
     and the replay removes it, so the head keeps only OLH attrs, `get_obj_state` of `null` answers
     -ENOENT, and RGWDeleteObj takes that as a delete marker and skips the check
     ([rgw_op.cc:5186-5221](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L5186-L5221),
     [rgw_op.cc:5576-5611](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L5576-L5611)).
     The read answers as radosgw's check does in every state radosgw reaches, and differs only where
     the head holds data an rgw-go refusal kept. Record in the entry V24 Behaviour 7 updates that a
     delete naming `null` on an object-lock bucket takes the marker and leaves the data, whose lock
     then holds, and in "Lifecycle removes an object only as it was listed" that a null marker is
     read in the index's order.
  7. Trace, before implementing: radosgw's lifecycle removal of a null delete marker at v19.2.7.
     `remove_expired_obj` names the empty instance `null`, reads its state with `get_obj_state` and
     returns any failure
     ([rgw_lc.cc:617-628](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L617-L628)),
     and a null marker's state is Behaviour 4's -ENOENT. v20.2.4 accepts -ENOENT for a marker
     ([rgw_lc.cc:624-643](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L624-L643)).
     Say whether v19.2.7's lifecycle removes a null delete marker at all. If it does not, add the
     registry entry (PP-4) and record rgw-go's removal of it in the lifecycle entry.
- **Specs:** on both releases, against the emulator:
  - V24 Behaviour 7's precondition, with the null data the key's only version and over an older
    version: on a suspended bucket, null data with tails, a null delete refused by an attr write
    after the marker's link, and its relist ended; then versioning enabled and object lock
    configured. Then DeleteObject and DeleteObjects naming `null`, through the op layer over the
    RADOS store:
    - a legal hold on the data, written by a write naming `null` while the marker is current: the
      first delete answers 204 with `x-amz-delete-marker: true`, a delete marker for DeleteObjects,
      and takes the marker alone. The data stays whole, its head, tails and counted record kept and
      its hold on. The next delete answers 403 AccessDenied, "forbidden by object lock", with
      nothing removed, and once the hold is off the next takes the data;
    - a COMPLIANCE retention: the same, the data taken once the clock passes its retain-until date;
    - no lock: the first delete takes the marker, the second the data;
    - after each delete the header's count and `BucketStats` equal the versions stored, and no tail
      is left that no record names;
  - Behaviour 2's races, through `statHook` (`internal/driver/delete_lock_op_test.go`): another
    delete takes the marker between the check and the removal, with a hold on the data, and the
    delete answers 403 with the data kept; a marker linked over the data between the check and the
    removal, and the delete takes the marker;
  - lifecycle, with object lock and without, with a hold on the data and without: in the null-only
    shape the marker expires by an expired-delete-marker rule, and over an older version, once a
    newer version is written, by the noncurrent expiration. Each removes the marker and leaves the
    data whole, the counts and stats as stored;
  - a bucket without object lock, a key whose head holds no null data, and a plain key: as today;
  - a delete naming no version on an object-lock bucket stored as suspended, over the marker and
    the kept data with a hold: 403 AccessDenied as today, the data's lock checked;
  - V24 Behaviour 7's specs, and its adversarial spec OL, which fails with the marker over the data
    at that PR's head, pass.

  No s3-tests test: no S3 client can make the precondition, as for V24 Behaviour 7.
- **Depends on:** V24, its Behaviour 7 PR. Without that PR's keep, a delete that takes the marker
  of a null-only key ends in the replay's clear, which removes the OLH head, and the null data in
  it, with a plain remove (`clearOLHHead`, `internal/driver/olh_head.go:732-753`; V24's Behaviour
  1, last path). V27 alone would then let a delete naming `null` destroy data a hold or a retention
  locks, which today's misread check refuses. V27 shares no code file with V25 or V26, whose
  changes lie in the replay, the links, the relists and the index ops, and it edits other entries
  of `docs/differences.md`, so it is not ordered against them. **PR:** one. **Brief:** about 50
  lines.

### Task V28: a delete decides object lock on current bucket state

Found by V24 Behaviour 7's adversarial review in its re-run 1 (R1-3), pre-existing on main, and
numbered here before that PR merged (PP-12). A delete decides whether to check object lock on the
request's bucket record, loaded once (`loadBucket`, `internal/op/bucketops.go:32-39`) through the
metadata cache (`GetBucket` and `readInstance`, `internal/driver/bucket.go:106-121` and
`internal/driver/bucket.go:164-175`; `sysobjs.read`, `internal/driver/sysobj.go:106-150`).
`lockTarget` checks nothing on a record without object lock (`internal/op/objectlock.go:93-94`), and
`deleteUnderLock` then sends the delete with no `Expect` (`internal/op/objectlock.go:55-58`). Two
records predate a lock written since:

- a request in flight across PutObjectLockConfiguration, which sets the flag on a
  versioning-enabled bucket (`internal/op/bucketobjectlock.go:155-159`), on any gateway and with no
  failure;
- another gateway's cached record when the enable's notify is lost, which the writer only logs
  (`sysobjs.distribute`, `internal/driver/sysobj.go:419-424`), until `rgw_cache_expiry_interval`,
  900 s by default (`internal/driver/options.go:28`), passes.

A legal hold or a retention written meanwhile through a fresh record is then removed. The store's
delete without `Expect` is an unguarded writer, which removes the version whatever it carries
(`olhRemoval`, `internal/driver/olh.go:1339-1345`): its own replay removes it, and when another
replay applies its unlink first and keeps the version for its lock, `checkRemoval` removes it after
(`internal/driver/olh.go:1432-1435`). The adversarial row R1L removes a version under a legal hold
both ways, at main and at V24 Behaviour 7's head. Lifecycle's actions decide the lock on a record
read again before each action, but through the same cache (`lcBucket.current`,
`internal/driver/lc_bucket.go:232-247`; `cur.Info.ObjLockEnabled()` at
`internal/driver/lc_actions.go:132`, `internal/driver/lc_actions.go:288` and
`internal/driver/lc_actions.go:418`), so a gateway that missed the notify expires a locked version
until its cache entry expires. That lifecycle case is by reading, not run.

radosgw has the same gap (`docs/ceph-upstream-bugs.md`, "radosgw decides a delete's object-lock
check on bucket info loaded before object lock was enabled", which V24 Behaviour 7's PR adds). Its
request loads `s->bucket` once
([rgw_op.cc:525-530](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L525-L530),
[rgw_op.cc:555-560](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L555-L560)),
through the instance cache
([svc_bucket_sobj.cc:284-298](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/services/svc_bucket_sobj.cc#L284-L298),
[svc_bucket_sobj.cc:235-249](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/services/svc_bucket_sobj.cc#L235-L249)),
whose entries a lost notify leaves for `rgw_cache_expiry_interval`
([svc_sys_obj_cache.cc:345-348](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/services/svc_sys_obj_cache.cc#L345-L348),
[svc_sys_obj_cache.cc:345-348](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/services/svc_sys_obj_cache.cc#L345-L348);
[rgw.yaml.in:3342](https://github.com/ceph/ceph/blob/v19.2.7/src/common/options/rgw.yaml.in#L3342),
[rgw.yaml.in:3521](https://github.com/ceph/ceph/blob/v20.2.4/src/common/options/rgw.yaml.in#L3521)).
DeleteObject and DeleteObjects take `check_obj_lock` from that info
([rgw_op.cc:5186-5221](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L5186-L5221) and
[rgw_op.cc:6854-6879](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L6854-L6879),
[rgw_op.cc:5576-5611](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L5576-L5611) and
[rgw_op.cc:7782-7807](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7782-L7807)),
while PutObjectLegalHold checks the flag on its own request's info
([rgw_op.cc:8503-8509](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8503-L8509),
[rgw_op.cc:9451-9457](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9451-L9457)).
Lifecycle's `pass_object_lock_check` passes whatever its bucket's info lacks object lock
([rgw_lc.cc:304-307](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L304-L307),
[rgw_lc.cc:304-307](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L304-L307)), on
info loaded once per pass
([rgw_lc.cc:1697-1703](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L1697-L1703),
[rgw_lc.cc:1753-1759](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L1753-L1759)).
By reading, not reproduced on radosgw.

- **Layer:** op and driver. **Files:** a `DeleteParams` field in `internal/op/object.go`, and an
  error the refusal wraps, with opfakes, driverfakes and memstore as the hot-file rule for that
  file says; the three store paths that refuse (Behaviour 2), in `internal/driver/delete.go`,
  `internal/driver/delete_versioned.go` and `internal/driver/olh.go`; the op layer's lock check
  (`deleteUnderLock` in `internal/op/objectlock.go`); the admin removals that set the field (the
  object removal in `internal/op/adminbucket.go` and `PurgeObjects` in `internal/op/adminpurge.go`);
  a read of a bucket instance past the metadata cache on `op.BucketStore` (`internal/op/bucket.go`),
  the driver's beside `GetBucketInstance` in `internal/driver/bucket.go`; lifecycle's removals
  (`expireCurrent`, `markCurrent` and `removeVersion` in `internal/driver/lc_actions.go`, and
  `lcBucket.current` in `internal/driver/lc_bucket.go`); their specs, the op layer's over the RADOS
  store in `internal/driver/delete_lock_op_test.go` and lifecycle's in
  `internal/driver/lc_bucket_test.go`; `docs/differences.md`, the entries "A delete that removes a
  version for good on an object-lock bucket checks its lock", "Every permanent removal by lifecycle
  runs the object-lock check" and "Admin removals ignore object lock"; the rgw-go line of the
  registry entry above. **Cluster:** no.
- **Security boundary:** object lock; adversarial review applies (PP-14). No change may let a delete
  remove a version whose lock a check on current bucket state would keep.
- **Behaviour:**
  1. A delete and a lifecycle action decide the lock check on bucket state at least as new as the
     target's lock attrs as the store read them. Every lock attr rgw-go writes is written through a
     record with object lock: PutObjectRetention and PutObjectLegalHold refuse a bucket without it
     (`internal/op/objectretention.go:196-198`, `internal/op/objectretention.go:351-353`), a
     write's object-lock headers are refused there (`objectLock`,
     `internal/s3/putobject.go:495-500`), and a default retention needs it
     (`internal/objectlock/default.go:24-26`). No S3 request clears the flag:
     PutObjectLockConfiguration only sets it (`internal/op/bucketobjectlock.go:155-159`), and
     PutBucketVersioning keeps it (`internal/op/bucketversioning.go:171-183`). So a read of the
     bucket instance past the metadata cache, made after the store's read of the target found a
     lock attr, finds object lock. Only an admin metadata put that cleared the flag, or a lock
     radosgw's CopyObject stored on a bucket without it (`docs/ceph-upstream-bugs.md`, "radosgw's
     CopyObject stores an object lock on a bucket without object lock"), leaves it off, and then no
     check runs, as radosgw's would run none.
  2. The store refuses. When the record a delete is given has no object lock, the store's delete
     refuses a removal for good whose target, as the store reads it, carries a retention or
     legal-hold attr: it writes nothing and answers an error wrapping a new op error, which says
     the record predates the target's lock. The field is zero by default, so a caller that sets
     nothing is refused: the refusal fails closed. A caller sets it when it has decided the lock
     itself: the admin object removal and `PurgeObjects`, which ignore object lock
     (`internal/op/adminbucket.go:926`, `internal/op/adminpurge.go:87`; P2), and a second round on
     a record read past the cache that still has no object lock (Behaviours 3 and 4). A record with
     object lock is never refused: its caller has checked the lock. The three paths, each on a head
     it reads and with the removal held to that read, so that a lock written after the read refuses
     the removal:
     - the unversioned path: `DeleteObject`'s own head read (`readHead`,
       `internal/driver/delete.go:62`), whose write tag already guards the removal
       (`internal/driver/delete.go:79-81`). A head the guard does not cover, one without a write
       tag, must not let a lock written after the read through; the implementer shows it held, or
       holds it. No read is added;
     - a suspended bucket's null delete marker, which removes the null data: `writeDeleteMarker`
       (`internal/driver/delete_versioned.go:92`) through `setOLH`, whose every attempt reads the
       key's OLH head (`internal/driver/olh.go:322`), the head that holds the null data. The
       attempt refuses before its init, and otherwise sends the head's write tag to its init and
       replay, as `ExpectHead` does for a conditional marker (`internal/driver/olh.go:337-339`). No
       read is added;
     - a versioned delete naming a version (`removeVersion`,
       `internal/driver/delete_versioned.go:328`, through `unlinkVersion`,
       `internal/driver/olh.go:492`). For `null`, the attempt's read of the key's OLH head
       (`internal/driver/olh.go:502`) is the null data's head, held as the `Expect` path holds it
       (`internal/driver/olh.go:507-512`), and no read is added. For any other version no head of
       the version is read before its unlink, so this path reads it once, on a record without
       object lock, and its write tag guards the removal as `expectRemoval`'s does
       (`internal/driver/delete_versioned.go:420-427`). The refusal cannot wait for the replay,
       which reads that head anyway: another request's replay that applies the unlink first keeps
       only a lock no delete passes (`lockHolds`, `internal/driver/olh.go:1323-1337`), since the OLH
       log carries nothing of the writer's, so a GOVERNANCE retention would go, and the version
       would be out of the listing before any refusal.

     A delete given `Expect` on a record without object lock, as lifecycle's are, refuses the same
     way. A delete marker on an enabled bucket removes nothing and is never refused.
  3. The op layer. `deleteUnderLock` sends a delete on a record without object lock as today, the
     field unset (`internal/op/objectlock.go:55-58`). On the refusal it reads the bucket instance
     past the metadata cache, which caches what it read, as `readFresh` reads an account
     (`internal/driver/account.go:424-433`), and decides again on that record: `lockTarget`, the
     check and the store's delete all take it, so a target the stale record names and the current
     one does not, such as a suspended record's null version once the bucket is enabled, is never
     removed unchecked. With object lock it runs the check and the guarded removal; without, it
     sends the delete again with the field set. The permission checks, the governance bypass's
     among them, stay on the request's record. A delete is refused at most once.
  4. Lifecycle. Each removal (`internal/driver/lc_actions.go:138`,
     `internal/driver/lc_actions.go:303` and `internal/driver/lc_actions.go:430`) is a store delete
     on the record `recheck` returned, and meets the refusal when that record is stale. The action
     then reads the instance past the cache, runs `current`'s checks on it, so a bucket whose
     versioning changed since the pass began skips the action and ends the pass as `recheck` does,
     and decides the lock again on it: `lcLockCheck` with object lock, and the removal with the
     field set without. Decide's gate (`removable`, `internal/lifecycle/decide.go:478-484`) reads
     the page's record and only chooses the effect; a removal the action then keeps is decided
     again at the next pass.
  5. The mechanism, and why:
     - No read is added to the common path. An unversioned bucket's DeleteObject, a suspended
       bucket's null delete marker and a delete naming `null` refuse on a head the store already
       reads, and a delete marker on an enabled bucket removes nothing. The read past the cache
       runs only on a refusal.
     - It fails closed at the store, so every caller is covered, lifecycle included, and a caller
       added later is refused until it decides the lock.
     - The cost: the `DeleteParams` field, set by the admin removals and by each second round, and
       the refusal on the three store paths above. A delete naming a version other than `null`, on
       a bucket without object lock, reads that version's head once before its unlink, and its
       removal and the null marker's are then guarded on the head as read. A write to the target
       between that read and the removal is the lost race, where today the delete removes what it
       finds.
     - Rejected: the op layer reading every target on a record without object lock, which adds a
       head read to every delete that removes a version for good there, every unversioned
       DeleteObject included, and covers only the callers that pass through `deleteUnderLock`.
     - Rejected: reading the instance past the cache on every delete. It costs a read per delete
       and holds the removal to nothing, so a lock landing after it is still removed.
  6. The RADOS state stays radosgw's: the reads write nothing, a refusal writes nothing, and a
     delete the check passes removes what radosgw's removes. Record in `docs/differences.md` that
     rgw-go decides on current bucket state where radosgw decides on the request's or the pass's,
     the guard on a bucket without object lock, and, in "Admin removals ignore object lock", that
     the admin removals still do. The registry entry's rgw-go line says what rgw-go does.
- **Specs:** on both releases, against the emulator:
  - R1L's shape: a versioning-enabled bucket with v1; a request's record read before
    PutObjectLockConfiguration; object lock enabled and a legal hold written on v1 through a fresh
    record; then DeleteObject and DeleteObjects naming v1 with the old record. Each answers 403
    AccessDenied, "forbidden by object lock", and v1 stays, with the delete's own replay and with
    another request's replay first. The same with a COMPLIANCE retention, and with a GOVERNANCE one,
    which refuses without the bypass header and passes with it for a requester allowed the bypass;
  - the lost notify: two stores on one emulated cluster, the enable's notify failed (`FailNotify`,
    `internal/testutil/fakerados/cluster.go:371`), the second store's DeleteObject refused as above,
    and its next request reading the current record from the cache;
  - a lock written between the store's read and its removal, on each of the three paths (a write
    hook): the removal refused, then 403 on the next round;
  - an old suspended record over a bucket since enabled and locked, a hold on the null version: a
    delete naming no version writes a delete marker and the null version stays;
  - a bucket that never had object lock: a delete naming a version and an unversioned DeleteObject
    answer as today, with no read past the cache, and the unversioned DeleteObject with no read
    added; an attr write between the store's read and the removal of a named version answers the
    lost race, the version kept and listed;
  - the store's delete given a record without object lock and a locked target, the field unset:
    refused with nothing written; the admin object removal and `PurgeObjects` remove it as today;
  - lifecycle, a pass on a gateway that missed the notify: a noncurrent version under a hold, and
    under COMPLIANCE, is kept; an expiration of a current object on a record still unversioned in
    the cache, over a bucket since enabled and locked, skips and ends the pass; a version with no
    lock attr expires as today.

  No s3-tests test: a request in flight across PutObjectLockConfiguration, or a lost notify, is
  not something an S3 client can arrange.
- **Depends on:** V24, its Behaviour 7 PR, which adds the registry entry, and V27. V27 changes
  `deleteUnderLock` and lifecycle's `removeVersion`, the code this task changes, and this task's
  read of a target named `null` is V27's. The store paths are shared with V25, whose links go
  through `setOLH` in `internal/driver/olh.go`, and with V26, which changes the cancels in
  `internal/driver/delete.go` and `internal/driver/delete_versioned.go`, so V28 goes after both,
  as the Order has it. **PR:** one. **Brief:** about 50 lines.

### Task V29: a bucket delete leaves no OLH head behind

Found by V24's lifecycle-and-listing PR, in its run of V21's adversarial harness, and numbered here
before that PR merged (PP-12). That PR adds its registry entry, "radosgw's bucket delete leaves the
OLH head of a key whose last delete could not clear it". The delete of a key's last version clears
the key's OLH: `clearOLHHead` removes the pending attrs older than `rgw_olh_pending_timeout_sec`, an
hour by default (`internal/driver/olh_head.go:28-32`), then removes the head guarded on its OLH tag,
its `olh.ver` and no pending attr, and a removal those guards refuse leaves the index alone
(`internal/driver/olh_head.go:772-793`), as `clear_olh` does
([rgw_rados.cc:8616-8624](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8616-L8624),
[rgw_rados.cc:9362-9370](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9362-L9370)).
`finishOLHLog` takes that refusal for success and trims nothing when it keeps no refused removal
(`internal/driver/olh_head.go:702-711`), as `apply_olh_log` does
([rgw_rados.cc:8550-8556](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8550-L8556),
[rgw_rados.cc:9296-9302](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9296-L9302)),
and so does `checkRemoval`'s clear (`clearVersionlessOLH`, `internal/driver/olh.go:1504-1528`). The
head stays, and the key's version marker and OLH entry stay in the index. Nothing comes back for
them: a read of the key replays its log only while a pending attr remains (`followOLH`,
`internal/driver/olh_head.go:373-387`), so once the attr expires no read clears the OLH, and only
the key's next write takes the head over. DeleteBucket finds none of it:

- its listing skips a version marker
  ([cls_rgw.cc:604-608](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L604-L608),
  [cls_rgw.cc:654-658](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L654-L658))
  and the OLH entry's namespace (V24's Behaviour 4);
- its recount counts a plain entry that exists with no flags and an instance entry that exists, so
  neither (`checkIndexCount`, `internal/driver/bucketops.go:396-400`;
  [cls_rgw.cc:3035-3114](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L3035-L3114),
  [cls_rgw.cc:3361-3434](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L3361-L3434));
- its removal takes the index shards alone (`cleanIndex`, `internal/driver/bucketops.go:143-167`),
  as `delete_bucket`'s does
  ([rgw_rados.cc:5368-5371](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L5368-L5371),
  [rgw_rados.cc:6008-6009](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6008-L6009)).

So the head stays in the data pool, named by nothing. The pending attr that refuses the clear is one
no log entry names:

- a relist's init that the backstop or a stalled call cut in flight, its link never sent (`setOLH`
  with `olhRelink.finish`, `internal/driver/olh.go:344-347` and `internal/driver/olh.go:374-380`;
  `docs/differences.md`, "A replay keeps a named version that a later link in the same log names");
- a link or unlink whose outcome is unknown and that never landed ("A versioned link or unlink whose
  outcome is unknown keeps its pending attr");
- a gateway stop between an init and its link, rgw-go's or radosgw's.

V24's harness parts K1 (the relist's backstop passing before each of its ops) and K2 (one of its
calls stalled at each op) leave 126 versions unlisted, all listed again by that PR. In 10 of them
the key, emptied through S3 inside the hour after the relist's init was cut and its bucket then
deleted, leaves only its OLH head, no data and one pending attr. With the drain run past
`rgw_olh_pending_timeout_sec`, so that the attr has expired before the last delete, all 126 leave
nothing.

- **Layer:** driver. **Files:** the replay's clear (`finishOLHLog` and `clearOLHHead` in
  `internal/driver/olh_head.go`, and `clearVersionlessOLH`, which `checkRemoval` calls, in
  `internal/driver/olh.go`) and the OLH index seam it records through (`olhIndex` in
  `internal/driver/olh_head.go`, `bucketOLHIndex` in `internal/driver/olh_index.go`); DeleteBucket
  (`deleteBucket` in `internal/driver/bucketops.go`, on both its paths, `DeleteBucket` and
  `DeleteBucketPurged`); the record's object, beside the unlisted-version object V24's
  lifecycle-and-listing PR adds; their specs (`internal/driver/olh_head_test.go`,
  `internal/driver/delete_expect_test.go`, whose relist specs drive the backstop, and the exports
  beside them); `docs/differences.md`; in `docs/ceph-upstream-bugs.md`, the rgw-go line of the entry
  above; `docs/rados-format-opportunities.md`, beside the entry V24's lifecycle-and-listing PR adds
  for its object. **Cluster:** no.
- **Security boundary:** quota accounting and the bucket-deletion path; adversarial review applies
  (PP-14). The head holds no data and no stats count it; nothing here changes what the index counts
  or what the recount decides. Every text on the quota side, here, in `docs/differences.md` and in
  the registry, states the mechanism only.
- **Behaviour:**
  1. After a bucket delete, no OLH head of the bucket's keys is left in the data pool, on every path
     rgw-go observes (Behaviour 5 names the rest). A key's last delete inside
     `rgw_olh_pending_timeout_sec` of a cut init, then DeleteBucket, leave what the same two leave
     once the attr has expired: nothing of the key.
  2. The mechanism: the clear's refusal is recorded, and DeleteBucket removes the recorded heads.
     - A clear that a pending attr refuses, `finishOLHLog`'s or `clearVersionlessOLH`'s, records the
       key in a per-bucket rgw-go object in the bucket's index pool, through the OLH index seam, as
       V24's lifecycle-and-listing PR records an unlisted version. A clear refused because another
       writer moved the OLH, its OLH tag or its `olh.ver`, is not recorded: that writer's replay owns
       the key. The record follows the refusal, the one fact the replay observes, and the delete
       answers as today. A record that fails is logged, and the head stays as it does today.
     - DeleteBucket reads the record and each recorded head after the removal's claim and before
       its recount. Once the recount has read zero, and before the index's removal, it removes each
       recorded head that holds no null data (`holdsNullData`, `internal/driver/olh.go:1411`),
       guarded on the head as read: its OLH tag, its write tag, its `olh.ver`, and the pending attrs
       it read and no other. A head gone or holding data is passed over. A head another writer has
       moved since the read, a new pending attr included, refuses the bucket: BucketNotEmpty with the
       claim dropped, as the recount's refusal does, and a retry reads it again. The admin removal
       with `purge-objects` (`DeleteBucketPurged`) skips the recount and removes the recorded heads
       the same way. The record's object goes with the bucket, as V24's does.
     - The record lives in an object of its own, not in V24's unlisted-version object. That object's
       consumer reads a record with an empty instance as the null version's and removes one that
       does not parse (`parseUnlistedKey` and `settleUnlisted` at that PR's head), so it would relist
       or drop this record, as would a gateway running V24 without V29 during a rolling upgrade.
       Lifecycle does not read it.
  3. Why, against the candidates, each checked at origin/main:
     - DeleteBucket waiting out, or clearing, a stale pending attr before it removes the index,
       bounded by `rgw_olh_pending_timeout_sec`. DeleteBucket has no read that names the key, since
       its listing and its recount pass over a key with no version (above), so it cannot find the
       attr. The bound is an hour by default, which a request cannot hold, and a 409 until then would
       keep a bucket its owner has emptied for that hour, where radosgw deletes it. The clear is kept,
       as Behaviour 2's removal, made where the recount shows the attr's writer can lose nothing.
     - The relist's paired write clearing its own init on every path. The init's op tag is made
       before its write (`internal/driver/olh_head.go:182-186`), so an init cut in flight could be
       canceled after it, the cancel ordered behind it as `resetCurrent`'s is
       (`internal/driver/delete_versioned.go:1108-1117`). That covers one source alone. A link whose
       outcome is unknown keeps its attr on purpose, so that a follow replays it if it lands late
       (`internal/driver/olh.go:396-398`), and a cancel would undo that. A gateway stop, any other
       writer's unknown outcome, and radosgw's writers leave the same attr. Not chosen, and not needed
       once the refusal is recorded.
     - DeleteBucket removing the OLH heads with no data that its emptiness check finds. Chosen, but
       the check finds none today. Finding them needs a read that names the key: `bi_list` or a read
       of the shards' OLH namespace, which D8 keeps out and which would need the owner's ruling, as
       V24's design (B) did; or a record written where rgw-go observes the refusal. The record is
       V24's design (A) applied to this state, and costs a write on that rare path alone.
  4. Why removing over a pending attr younger than the timeout is safe there, which radosgw's clear
     never does. Every writer whose init stands on a head with no version either holds an index
     entry the recount counts once its init has landed, or links only entries the index's removal
     takes. A write of a named version completes its index entry, awaited, before its link and its
     init (`linkVersion`, `internal/driver/put_versioned.go:339-363`; D5). A null write inits with
     its head write, which puts its data in the head, so the removal passes that head over. An
     unlink, a relist and a transition act on a version the recount counts. Reading the heads before
     the recount and removing after it holds that order: an attr read was inited before the
     recount, and one inited after the read refuses the removal. Trace, before implementing, every
     writer whose init can stand on such a head, the delete marker's and the null delete marker's
     included, whose links write their entries after the recount, and record the result; a writer
     that breaks the argument refuses the removal instead.
  5. Not covered, and recorded in `docs/differences.md`: radosgw's last delete on a mixed fleet,
     which records nothing, and radosgw's bucket delete, which reads no record and leaves the head
     and the record's object; a gateway stop between the refused clear and its record; and, by
     reading, a gateway stop between a last delete's head op and its clear, which leaves the head
     with no pending attr and no refusal to record. A head left so stays, as radosgw leaves it.
  6. The RADOS format stays byte-identical. The head's removal removes an object radosgw's
     `clear_olh` removes, writing nothing in its place; the index is removed whole as today; the
     record is a new rgw-go object radosgw neither reads nor removes. Record that object in
     `docs/rados-format-opportunities.md`, with what a mixed fleet's radosgw bucket delete leaves;
     record in `docs/differences.md` the removal over a young pending attr, where radosgw's bucket
     delete leaves the head; and make the registry entry's rgw-go line say what rgw-go does.
- **Specs:** on both releases, against the emulator:
  - K1's and K2's shape: a refused delete of v1 whose relist's init lands while its call ends at
    the backstop, and once with that init stalled until the backstop passes, leaving the init's
    pending attr and v1 unlisted. Past V24's settle age DeleteBucket lists v1 again and answers 409;
    v1 is deleted by its id inside `rgw_olh_pending_timeout_sec`, its clear refused and the key
    recorded. DeleteBucket then deletes the bucket, and no OLH head, head or tail of v1, index
    shard, or object of either record is left;
  - the same with the attr expired before the delete: the clear removes the head as today, and
    nothing is recorded;
  - `checkRemoval`'s clear refused by a pending attr records the key the same way; a clear refused
    by another writer's move records nothing;
  - a live writer's init standing at the clear, whose link lands after: the key holds its version
    and DeleteBucket answers 409 from its listing as today. Once that version is deleted and its
    clear lands, DeleteBucket passes over the record of a head that is gone and deletes;
  - an init landing between DeleteBucket's read of a recorded head and its removal: BucketNotEmpty,
    the claim dropped, the head and its attrs kept; the same for a null write's head write;
  - the admin removal with `purge-objects` removes a recorded head;
  - a record that fails: the delete answers as today, logged, and DeleteBucket leaves the head,
    pinned as recorded;
  - radosgw's last delete, its replay taking the refused clear for success and writing no record:
    DeleteBucket leaves the head, pinned as recorded;
  - V21's adversarial harness with V24's lifecycle-and-listing part: K1's and K2's 126 schedules
    leave nothing without the drain past the pending timeout, and J5's and K5's stay as that PR
    leaves them;
  - V24's specs pass.

  No s3-tests test: a backstop that parts a relist's writes, or a stalled call, is not something an
  S3 client can arrange, and the head is seen only through RADOS.
- **Depends on:** V24, its lifecycle-and-listing PR. That PR adds the relists lifecycle's pass and
  DeleteBucket run (`relistUnlisted`), each under the backstop a refused delete's relist has, and
  lists again the version a cut relist leaves, so a bucket whose owner then empties it through S3
  inside the hour reaches its delete, which #675's recount refused while that version stayed
  unlisted. Its record object and consumer are the shape this task's record follows. V29 shares
  `internal/driver/olh.go` with V25, whose links go through `setOLH` there, and with V28, whose
  store paths change it, and edits other entries of `docs/differences.md`, so it goes after V28, as
  the Order has it. V26 and V27 share no code file with it. **PR:** one. **Brief:** about 45 lines.

### Task V30: a failed versioned write whose removal fails leaves its version reachable

Found by the task review of V24's lifecycle-and-listing PR (its Important-2), and numbered here
before that PR merged (PP-12). A versioned write that gives up on an answer proving no link of its
version landed removes that version (`removeFailedVersion` and `removeUnlinkedVersion`,
`internal/driver/olh.go:1337-1366`): after its awaited index completion fails
(`internal/driver/put_versioned.go:341-352`), and after its link fails, through `setOLH`
(`internal/driver/olh.go:316-322` and `internal/driver/olh.go:329-338`). A removal that fails in
turn is logged, and leaves the head and an instance entry that no link listed
(`internal/driver/olh.go:1360-1365`; `docs/differences.md`, "rgw-go removes the instance a failed
versioned write made"). That is V24's Behaviour 1 path 3. The version is in no listing, the index
counts it, and #675's recount refuses the bucket's delete while it stays. Its owner was answered
the write's failure and holds no version id to delete it by, so only the admin removal with
`purge-objects` removes the bucket.

V24's lifecycle-and-listing PR records the versions a replay leaves unlisted and lists them again,
and leaves this path unrecorded. Its ruling (that PR's report, "Behaviour 1 paths covered"):
`removeFailedVersion` also serves CompleteMultipartUpload's first completion, whose head write goes
through the same `linkVersion` and whose head names the upload's parts. A retried completion may
still link that version (`linkRecorded`, `internal/driver/mp_complete.go:628-663`), so a consumer
that removed it would queue the parts for the GC under that retry. A consumer that relisted any
failed write's version would list a write answered as failed. Telling the two callers apart needs a
seam in the write path, which this task adds.

- **Layer:** driver. **Files:** `linkVersion` in `internal/driver/put_versioned.go`; `olhLink`,
  `setOLH`, `removeFailedVersion` and `removeUnlinkedVersion` in `internal/driver/olh.go`; the record
  and its consumer V24's lifecycle-and-listing PR adds (its unlisted-version object,
  `recordUnlisted` and `settleUnlisted`); their specs (`internal/driver/put_versioned_test.go`,
  `internal/driver/copy_versioned_test.go`, `internal/driver/mp_complete_versioned_test.go`,
  `internal/driver/delete_expect_test.go` for DeleteBucket and `internal/driver/lc_bucket_test.go`
  for lifecycle); `docs/differences.md`, the entry above and the entry V24's lifecycle-and-listing
  PR adds, "Lifecycle and a bucket delete list again a version a replay left unlisted", whose
  uncovered paths name this one; `docs/rados-format-opportunities.md`, the entry that PR adds for its
  object. **Cluster:** no.
- **Security boundary:** quota accounting; adversarial review applies (PP-14). A version left so is
  counted in the index's stats and in what the write added to the quota cache
  (`internal/driver/headwrite.go:435-441`), and the removal that takes it takes both back. Every text
  on the quota side, here, in `docs/differences.md` and in the registry, states the mechanism only.
- **Behaviour:**
  1. `linkVersion` tells the callers apart by the head write it ends. `headWrite.completeMultipart`
     marks CompleteMultipartUpload's head write and no other
     (`internal/driver/headwrite.go:66-71`, set at `internal/driver/mp_complete.go:441-445`), and a
     write with `headWrite.expect` already removes nothing (`internal/driver/put_versioned.go:340`
     and `internal/driver/put_versioned.go:353`). A plain write is one with neither: PutObject's,
     and CopyObject's onto another key. `linkVersion` passes that to both of its removals: its own
     after a failed completion, and `setOLH`'s, through a field of `olhLink`, since `setOLH` decides
     its removal where the link fails. The CompleteMultipartUpload retry already keeps its version on
     every path (`KeepVersion`, `internal/driver/mp_complete.go:655`).
  2. A plain write's removal runs past the end of the request's context, as `setOLH`'s removal
     before an init already does (`internal/driver/olh.go:335`), so a client that leaves does not
     fail it. A removal that still fails records the version in V24's unlisted-version object, as a
     version to remove, never to list: the record carries that kind and the write tag the write's
     head carries. It is written past the end of the request's context too; a record that fails is
     logged, and the version stays as it does today.
  3. The consumers, lifecycle's pass and DeleteBucket, remove such a version, and act on its record
     at once, with no settle age. The write recorded it only after giving up on an answer that
     proves no link of it landed, and `setOLH` makes no attempt after a link whose outcome is unknown
     (`internal/driver/olh.go:396-417`), so no link of it is outstanding. The removal is
     `removeUnlinkedVersion`'s, guarded on the recorded write tag. A version gone, listed, or whose
     head carries another tag is left, and its record removed; a removal that fails keeps the record
     for the next pass. DeleteBucket removes it before its recount and refuses nothing for it, so a
     bucket whose only other entry was such a version deletes; lifecycle removes it before its rules.
  4. A gateway that runs V24 without V30, as on a rolling upgrade, must never list such a record's
     version: V24's consumer reads the record's value into its note and relists any version that
     parses (`settleUnlisted` at that PR's head). The record goes where that consumer drops it or
     does not read it, and the PR says which.
  5. CompleteMultipartUpload's first completion records nothing and keeps today's removal. A
     version its failed removal leaves is named by the upload's meta object, whose record the
     completion wrote before its head write (`internal/driver/mp_complete.go:419-421`). The upload
     stays listed, a retry links the version (`linkRecorded`), and an abort removes it once no link
     can land (`removeUnlinkedRecorded` and `linkSettled`, `internal/driver/mp_abort.go:325` and
     `internal/driver/mp_abort.go:603`). DeleteBucket aborts the bucket's uploads after its listing
     check, V24's Behaviour 1 path 4.
  6. Not covered, as V24 leaves them: a write whose link's outcome is unknown, which removes nothing
     and records nothing; a gateway stop between the failed removal and its record; the null version,
     whose head is the key's OLH head and which a delete naming `null` reaches; radosgw's failed
     writes on a mixed fleet, which record nothing. `docs/differences.md` says so, in the entry above
     and in V24's.
  7. The RADOS format stays byte-identical: the removal is the one the write itself makes, and the
     record is an rgw-go object radosgw neither reads nor removes. Record the record's new kind in the
     entry V24's lifecycle-and-listing PR adds to `docs/rados-format-opportunities.md`.
- **Specs:** on both releases, against the emulator:
  - a versioned PutObject whose link the class refuses, its removal then failing, once at each of
    the removal's ops: the version is recorded as one to remove. Lifecycle's next pass removes it,
    and nothing else changes; DeleteBucket, on a bucket with nothing else, removes it and deletes the
    bucket. No head, tail, instance entry or record of it is left, and the index's stats and the
    quota cache count the versions stored before and after;
  - the same after a failed awaited completion, and for CopyObject onto another key;
  - a removal whose request's context ends during it completes;
  - a record that fails: the write answers as today, logged, and the version stays;
  - a recorded version deleted by its id, or whose head another write replaced, before the
    consumer runs: the consumer removes nothing and drops the record;
  - CompleteMultipartUpload's first completion with the same failures: nothing recorded, the upload
    listed; a retry links the version and an abort removes it, as today, and #675's spec of a bucket
    holding a completion whose link never landed passes;
  - a copy onto itself and a lifecycle transition remove nothing and record nothing, as today; a
    link whose outcome is unknown records nothing;
  - V24's specs pass.

  No s3-tests test: a link the class refuses followed by a removal that fails is not something an
  S3 client can arrange.
- **Depends on:** V24, its lifecycle-and-listing PR, whose record object and consumers this task
  extends. V30 shares `internal/driver/put_versioned.go` with V25 and V26, which change
  `linkVersion`, and `internal/driver/olh.go` with V25 and V28, whose changes reach `setOLH`, so it
  goes after V28, as the Order has it. V27 shares no code file with it. **PR:** one. **Brief:**
  about 40 lines.

## Order

At most three implementers at once. A wave starts when the tasks it depends on have merged;
tasks in one wave run in parallel. Cluster tasks run one at a time across all units, and
recordings land one at a time through the controller.

| Wave | Tasks | Waits on |
|---|---|---|
| 0 | the index's S1, S2, S3; WP-16 and WP-17 in WP's waves | the deferred benchmark run |
| 1 | V0, V1, V2 | nothing |
| 2 | V3a, V3b, V10 | S1, S2, V0, V1, V2 |
| 3 | V4, V13a | V3a, V3b, WP-16 |
| 4 | V5a, V13b | V4, WP-17 |
| 5 | V5b (Fable review), V9, V13c | V5a, V13b |
| 6 | V5c (Fable review, to confirm), V6, V13d, V14b | V5b, V13c, V9 |
| 7 | V7a, V8a | V5c, V6 |
| 8 | V7b, V8b, V8c | V7a, V8a, V9 |
| 9 | V11, V12b | V7b, V8b |
| 10 | V12a, V14a | V11, V13d |
| 11 | V16, then V15a, then V17, then V18, then V19, then V20, then V15b, then V15c, then V21, then V22, then V23, then V24, then V25, then V26, then V27, then V28, then V29, then V30 | everything above, WP-22 |

- V10's recording PR is a cluster task the controller schedules after its first PR merges.
- The critical path is V1, V3b, V4, V5a, V5b, V5c, V7a, V7b, V11, V14a, V15a. L and C start
  their versioned work from V6, V8a, V8c and V9 (index, Order 2); L4 and C10b need V8c for
  `Expect`.
- The Fable reviews of V5b and V5c (P3) run one after the other, within the one-at-a-time budget.

## Defects

The report's §6 table, then from row 18 the defects V4 and its review found, with the task that
carries each choice. Status is unreproduced until a running release shows the defect (PP-11);
"registry" marks an entry already in `docs/ceph-upstream-bugs.md`. Rows 18 to 22 cite theirs by
title; those entries land with V4's PR.

| # | Defect | Releases | Class | Status | rgw-go's choice | Task |
|---|---|---|---|---|---|---|
| 1 | DeleteObjects asks for MFA for keys without a version and not for versions (registry, tracker #77869) | v19.2.7, v20.2.1 to v20.2.4 | security (MFA bypass) | unreproduced | main's test on both releases; the exclusion exists | V12a; V15b reproduces |
| 2 | `complete_op` writes a stale epoch back on cancel (registry, #80894); instance entries too | both | data corruption (index meta) | reproduced | radosgw's cancel version; class-side, no client workaround | V5a |
| 3 | v19.2.6 evaluates a versioned PUT's conditions against the new, nonexistent instance; fixed in v19.2.7 by ce98cebb815, which reads the current version first ([rgw_rados.cc:3185-3205](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L3185-L3205)), as [rgw_rados.cc:7238-7258](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7238-L7258) does | v19.2.6 only, below the floor | S3-correctness | unreproduced; not met at either floor | both floors' evaluation (VD-2); no exclusion or known difference at v19.2.7 | V7a |
| 4 | Conditional writes on versioned buckets are check-then-act ([rgw_rados.cc:3282-3300](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L3282-L3300)) | both | S3-correctness (race) | unreproduced | not reproduced: D1 | V5c, V7a |
| 5 | CopyObject sends neither version header ([rgw_rest_s3.cc:3596-3633](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3596-L3633), [rgw_rest_s3.cc:3873](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L3873)) | both | S3-correctness | unreproduced | send both (D4); exclusion | V11 |
| 6 | GET or HEAD naming a delete marker's version answers 404 (Tentacle with `x-amz-delete-marker: false`); S3 answers 405 with `true` | both | S3-correctness | unreproduced | 405 and `true` (D2); exclusion | V6, V14a |
| 7 | The 404 of a current delete marker lacks `x-amz-delete-marker` ([rgw_rest.cc:1898-1938](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L1898-L1938); Tentacle sends it, [rgw_rest.cc:1936-1944](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L1936-L1944)) | v19.2.6 | S3-correctness | unreproduced | send it on both (D6); exclusion on Squid | V14a |
| 8 | `x-amz-version-id` omitted when the id is `null` ([rgw_rest_s3.cc:469](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L469), [rgw_rest_s3.cc:2756](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2756), [rgw_rest_s3.cc:4111](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4111)) | both | S3-correctness (minor) | unreproduced | send `null` on ever-versioned buckets (D4); exclusion | V11, V14a |
| 9 | `versionId` on PutObject, CopyObject's destination and CompleteMultipartUpload honoured as the instance on a bucket not versioning-enabled (registry) | both | S3-correctness | unreproduced | ignored (D3); exclusion; the registry's rgw-go line updated | V11 |
| 10 | `cls_otp` divides by a stored `step_size` of 0 (registry, #80948) | both | denial of service, privileged | liboath reproducer only | this unit writes no otp entry; WP's admin otp refuses 0; the emulator answers EIO | V13b |
| 11 | `cls_otp`'s replay index from the unsigned window distance (registry, #80949) | both | S3-correctness (TOTP replay window) | liboath reproducer | class-side; rgw-go calls the class as radosgw does, and the emulator reproduces it | V13b |
| 12 | `cls_otp_client` sends the check-op buffer to `otp_get_result`; `cls_otp_get_result_op` is dead ([cls_otp_client.cc:78-83](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp_client.cc#L78-L83), [cls_otp.cc:487](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L487)) | both | cosmetic (consistent by accident) | n/a | send the check op to both methods, as radosgw does; WP-16 recorded it (registry, "radosgw's otp check sends the otp_check request to otp_get_result"), and V13a updates its rgw-go line | V13a, V13b |
| 13 | `RGWRados::get_olh` decodes `olh.ver` as `RGWOLHInfo` ([rgw_rados.cc:8896-8901](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8896-L8901); Tentacle reads `olh.info`, [rgw_rados.cc:9640](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9640)); only `radosgw-admin olh get` calls it | v19.2.6 | cosmetic (admin tool) | unreproduced | not met; read into the gate's `olh get` on Squid | V15b |
| 14 | Tentacle removes a head without checking its write tag (registry, #80898 and #80906), reached by `apply_olh_log`'s instance removal | v20.2.4 | data corruption (tail leak) | unreproduced | phase 1's guard kept; the exclusion exists | V5b |
| 15 | Squid before 19.2.3 refused a marker on a marker (registry) | below the floor | n/a | n/a | unaffected | none |
| 16 | The versioned delete of `versionId=null` reads the null version with one `bi_get` of the plain key, which on a converted key is the version marker: its conditions meet the marker's empty meta (a spurious 412 for any If-Match) and `x-amz-delete-marker` is false for a null delete marker ([rgw_rados.cc:5906-5944](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L5906-L5944), [rgw_rados.cc:6518-6556](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6518-L6556); [cls_rgw.cc:2617-2664](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L2617-L2664); review F4) | both | S3-correctness | unreproduced | resolve `null` in `read_key_entry`'s order, up to three `bi_get` reads (D8); exclusion | V5a, V8a |
| 17 | A `version-id-marker` of `null` whose null version is a delete marker resumes past the key's remaining versions ([cls_rgw.cc:358-390](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L358-L390), [cls_rgw.cc:408](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L408)) | both | S3-correctness | unreproduced | resolved in the driver, skipping through the marker; exclusion | V9 |
| 18 | Two links at one epoch, replayed together onto a head behind it, leave `olh.info` on the one logged last while the index makes the smaller instance current (registry, "radosgw's apply_olh_log applies a same-epoch link the class did not make current") | both | S3-correctness (multisite only) | unreproduced | radosgw's replay on a first pass; a retry from a fresh `olh.ver` keeps the class's current version (VD-1); exclusion | V4 |
| 19 | A head removal that fails with anything but ECANCELED still clears the key's OLH index entry (registry, "radosgw's clear_olh clears the index's OLH entry after a failed head removal") | both | data corruption (honest failure) | unreproduced | radosgw's clear, a failure other than not found logged | V4 |
| 20 | A versioned write whose key's OLH head goes between `set_olh`'s read and its init fails with ENOENT after its version is written (registry, "radosgw's versioned write fails when the key's OLH head goes between its read and its init") | both | S3-correctness (race) | unreproduced | V4's init answers ENOENT as radosgw's does; `setOLH` retries it from a fresh read within its 100 attempts (VD-12); exclusion | V4, V5b |
| 21 | A read that replays a pending modification serves the version current before it, a 404 NoSuchKey while the previous version is current when the replay is the deletion of the current version (registry, "radosgw's follow_olh answers the version that was current before the replay it runs") | both | S3-correctness (race) | unreproduced | V4's `followOLH` answers as radosgw's does; V6 resolves again after the update (VD-11); exclusion | V4, V6 |
| 22 | A link at the OLH entry's own epoch, logged after a replay read the log, is trimmed by that replay unapplied, so when the class made it current `olh.info` keeps the earlier link while the index makes it current; the opposite of row 18 (registry, "cls_rgw's OLH log trim drops an unapplied link at the trimmed epoch") | both | S3-correctness (multisite only) | unreproduced | reproduced through the class: V4's replay trims to its last epoch as radosgw's does | V4 |

Not a radosgw defect, but made reachable by this unit: the seam dropped a failed class call's
output, which made `RGWBIAdvanceAndRetryError` fatal (phase 1's exclusion "A shard that answers
advance-and-retry fails an ordered listing"). V0 and V9 fix it rather than keep it.

A candidate found while drafting V8a, not in the report and not yet verified: a null delete
marker racing a suspended PutObject of the same key, whose head write lands between the marker's
init and its link, may have its head removed by the marker's replay on radosgw (notes, N26).
Class: data corruption if confirmed. Status: unreproduced. V8c reads it at both tags and
records it in the registry if it holds; rgw-go's own replay never removes that head, and V8c's
re-link restores the null version's listing, or leaves a newer null write current.
