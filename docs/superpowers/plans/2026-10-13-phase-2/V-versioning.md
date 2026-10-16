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
| D8 | `docs/exclusions.md`'s two `bi_*` sentences are amended: `bi_get` is in scope for one version's index entry, on the versioned delete and on D2's 404: one read for a named instance, and up to three for `null` (the plain key, then the null delete marker's key, then the null data key, in the class's `read_key_entry` order). `bi_put`, `bi_list` and the repair paths stay out. rgw-rs is told. | The S3 versioned delete needs it at both floors (§2.4), and a converted key's `null` lies behind its version marker (review F4). | V5a, V8a |
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
| VD-8 | DeleteObjects groups keys by name and skips the OLH update for all but the last of each name. | radosgw's op count, and the same final state (§2.4). | V8a, V12a |
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
  with `OTPClass()` as `OTPClassAt(time.Now)`, assembled with an empty `otpCheckMethods`
  table, and per-device `last_checks` and `last_success`.
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
| `internal/cls/otp/` | WP-16, V13a | WP-16 owns the package and types; V13a adds `ops_check.go`. |
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
| `docs/exclusions.md`, `docs/ceph-upstream-bugs.md`, `docs/cgo-limitations.md` | most tasks | append-only per PP-5; each PR edits only its own entries. |
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
  `docs/cgo-limitations.md` and `docs/exclusions.md`. **Cluster:** no.
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
  - `docs/exclusions.md`, "apply_olh_log's olh.ver guard is EQ on the bytes read, retried from a
    fresh read" (VD-1; the state written is radosgw's but in the same-epoch window, where a retry
    from a fresh `olh.ver` keeps the class's current version and radosgw writes the link logged
    last).
  - `docs/exclusions.md`, "A replay passes over a version removal that the writer's expected
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
  `internal/driver/olh_index.go` and specs; edit `docs/exclusions.md`. **Cluster:** no.
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
  `internal/driver/export_integration.go` (a fault point) and `docs/exclusions.md`.
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
  `internal/op/object.go` (`ObjectState`); specs; edit `docs/exclusions.md`. **Cluster:** no.
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
  `internal/driver/put_versioned.go` and specs; edit `docs/exclusions.md` and
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
  edit `docs/exclusions.md` and `docs/ceph-upstream-bugs.md`. **Cluster:** no.
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
- **Exclusions and registry:** D8's amendment of both `bi_*` sentences in `docs/exclusions.md`
  (N11 of the notes), with D8's widened wording; the rgw-rs session is told, as
  `docs/exclusions.md` requires. A registry entry for defect 16 (S3-correctness, both floors,
  unreproduced) and an exclusion, "A versioned delete of versionId=null checks the null version
  itself".
- **Depends on:** V5c, V6. **PR split:** one. **Brief:** about 220 lines.

### Task V8c: driver, `DeleteParams.Expect`

- **Layer:** driver. **Files:** modify `internal/driver/delete_versioned.go` and specs; edit
  `docs/exclusions.md`. **Cluster:** no.
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
  specs; edit `docs/exclusions.md`. **Cluster:** no.
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
- **Depends on:** V0, V3a, V5a. **PR split:** one. **Brief:** about 175 lines.

### Task V10: op and s3, PutBucketVersioning and GetBucketVersioning

- **Layer:** op and handler. **Files:** create `internal/op/bucketversioning.go`,
  `internal/s3/bucketversioning.go` and specs; bind the two routes in S2's versioning route-set
  file; add `Request.MFAVerified bool` to `internal/op/request.go` (false until V13d sets it);
  edit `docs/exclusions.md`; then `hack/admin/deselect-phase2.txt` and the admin ledgers.
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
  `internal/s3/putobject.go`, `copyobject.go`, `multipart.go` and specs; edit `docs/exclusions.md`
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
  `internal/s3/deleteobject.go`, `deleteobjects.go` and specs; edit `docs/exclusions.md`.
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
  `adminpurge.go` (`refuseVersionedPurge`) and specs; edit `docs/exclusions.md`. **Cluster:** no.
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
  append to `docs/ceph-upstream-bugs.md`. **Cluster:** no.
- **Security boundary:** MFA (PP-14).
- **Consumes:** WP-16's package, `OTPCheck` and the result constants. **Produces:**

  ```go
  func Check(w *radosclient.WriteOp, id, val, token string)
  func GetResult(r *radosclient.ReadOp, id, val, token string) *GetResultResult
  ```

- **Behaviour:**
  1. `otp_check` (RD|WR) in a write op with `cls_otp_check_otp_op` v1 `{id, val, token}`, where the
     token is 16 random alphanumerics; Squid sends it through `IoCtx::exec` and Tentacle through a
     write op, the only cls_otp change between the tags
     ([cls_otp_client.cc:60-96](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp_client.cc#L60-L96),
     [cls_otp_client.cc:60](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/otp/cls_otp_client.cc#L60)).
  2. `otp_get_result` (RD) with the same check-op buffer, not a `cls_otp_get_result_op`: the
     client passes `in` ([cls_otp_client.cc:78-83](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp_client.cc#L78-L83)) and the class decodes a
     check op ([cls_otp.cc:483-511](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L483-L511), [cls_otp.cc:483](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/otp/cls_otp.cc#L483)).
     The reply is `cls_otp_get_result_reply{otp_check_t{token, timestamp, result}}`.
- **Specs:** goldens from `ceph-dencoder` for `cls_otp_check_otp_op` and
  `cls_otp_get_result_reply`; `GetResult` sends the check op's bytes; each result value decodes.
- **Registry:** defect 12 (cosmetic, both, consistent by accident): a note so no one "fixes" one
  side to send `cls_otp_get_result_op`, which the class would answer EINVAL.
- **Depends on:** WP-16. **PR split:** one. **Brief:** about 80 lines.

### Task V13b: emulator, the otp check methods

- **Layer:** emulator. **Files:** create `internal/testutil/fakerados/cls_otp_check.go` (WP-17's
  `otpCheckMethods` table) and specs. **Cluster:** no.
- **Security boundary:** MFA (PP-14); the emulator pins what the verifier trusts.
- **Consumes:** WP-17's device store with `last_checks` and `last_success`; V13a.
- **Behaviour:**
  1. `otp_check` RD|WR ([cls_otp.cc:447-481](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L447-L481),
     [cls_otp.cc:447](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/otp/cls_otp.cc#L447)): load the device by id, record the attempt with the TOTP
     result unless five attempts already fell in the current step window (`ATTEMPTS_PER_WINDOW`,
     [cls_otp.cc:43](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L43); [cls_otp.cc:108-127](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L108-L127),
     [cls_otp.cc:108](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/otp/cls_otp.cc#L108)), and write the device back.
  2. The validation ([cls_otp.cc:129-153](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L129-L153),
     [cls_otp.cc:129](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/otp/cls_otp.cc#L129)): RFC 6238 TOTP over the stored seed, step, time offset
     and window, at the cluster clock; a success stores its window index as `last_success`, so the
     same code fails afterwards. The class validates through liboath; the implementer reads
     liboath's `oath_totp_validate2` for the digit count and the window walk and quotes it in the
     task notes (PP-2). The unsigned window distance of defect 11 is reproduced as the class
     computes it; a stored step of 0, which faults the OSD (defect 10), answers EIO here.
  3. `otp_get_result` RD decodes a check op and answers the attempt with its token, or
     `OTP_CHECK_UNKNOWN` when none carries it.
- **Specs:** RFC 6238 Appendix B's SHA-1 vectors at their times; a wrong code; a reused code; the
  sixth attempt in one window; an unknown token; a missing device.
- **Registry:** none new; defects 10 and 11 are in the registry already.
- **Depends on:** WP-17, V13a. **PR split:** one. **Brief:** about 140 lines.

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
     ([svc_cls.h:56-58](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/services/svc_cls.h#L56-L58)), V13a's two calls with one fresh token, verified
     only on `OTP_CHECK_SUCCESS`.
  3. Any other result, or a missing device, is `(false, nil)`; a RADOS failure is `(false, err)`.
- **Specs** (on the emulator, V13b): an unknown serial; a good code; a wrong one; a reused one; a
  rate-limited one; the token shared by the two calls; a pool error.
- **Exclusions:** none.
- **Depends on:** V13a, V13b. **PR split:** one. **Brief:** about 100 lines.

### Task V13d: op and s3, the `x-amz-mfa` header

- **Layer:** op and handler. **Files:** modify `internal/s3/request.go` (`PostAuthInit`) and
  specs; wire `Env.MFA` where the gateway builds its env. **Cluster:** no.
- **Security boundary:** a client-facing header that feeds authorization (PP-14).
- **Consumes:** S2's `Env.MFA`, V13c, V10's `Request.MFAVerified`.
- **Behaviour** (VD-7):
  1. `PostAuthInit` reads `x-amz-mfa` after the tenant, bucket and object-name checks
     ([rgw_rest_s3.cc:5026-5031](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L5026-L5031), [rgw_rest_s3.cc:5556](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5556)), where
     phase 1 notes the gap (`internal/s3/request.go:163-179`).
  2. The value splits on single spaces into exactly two fields, serial and pin; anything else is
     not verified. A verified pair sets `Request.MFAVerified`.
  3. Nothing about MFA fails the request here; an op that needs MFA refuses it with 403
     AccessDenied (`-ERR_MFA_REQUIRED` 2044, [rgw_common.cc:96](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L96)).
- **Specs:** absent, malformed (one field, three, double space), unknown serial, good and bad
  codes through a fake verifier; a verifier error logged and the request carried on unverified.
- **Exclusions:** none; V12a retires "Deletes that need MFA are refused".
- **Depends on:** S2, V10, V13c. **PR split:** one. **Brief:** about 90 lines.

### Task V14a: op and s3, versioned reads

- **Layer:** op and handler. **Files:** modify `internal/op/getobject.go`, `objectread.go`,
  `objectattrs.go`, the object ACL and tagging ops, UploadPartCopy's source check,
  `internal/s3/getobject.go`, `getobjattrs.go`, `deletemarker.go` and specs; edit
  `docs/exclusions.md` and `docs/ceph-upstream-bugs.md`. **Cluster:** no.
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
  `internal/s3/listobjects.go` and specs; edit `docs/exclusions.md`. **Cluster:** no.
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
- **Depends on:** V10, V11, V12a, V14a, V14b, WP-22. **PR split:** one. **Brief:** about 80 lines.

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
| 11 | V15a, then V15b, then V15c | everything above, WP-22 |

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
| 12 | `cls_otp_client` sends the check-op buffer to `otp_get_result`; `cls_otp_get_result_op` is dead ([cls_otp_client.cc:78-83](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp_client.cc#L78-L83), [cls_otp.cc:487](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L487)) | both | cosmetic (consistent by accident) | n/a | send the check op, as radosgw does; a registry note | V13a |
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
