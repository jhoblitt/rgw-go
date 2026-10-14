# Phase 2 unit L: object lock

Threat model: a client adversary at the spec's authorization and client-facing parser
boundaries (the retention and legal-hold checks on deletes and retention changes, the
s3:BypassGovernanceRetention evaluation, and the XML and ISO 8601 parsers), which get
adversarial review (PP-14); the encodings, the default-retention write and the headers defend
against honest mistakes.

## Scope

Spec §9, phase 2: "object lock with retention and legal hold". In this unit that is:
- **Bucket configuration:** PutObjectLockConfiguration and GetObjectLockConfiguration
  (`?object-lock`), and CreateBucket with `x-amz-bucket-object-lock-enabled: true`, which
  phase 1 answers 501.
- **Object retention:** PutObjectRetention and GetObjectRetention (`?retention[&versionId]`),
  in GOVERNANCE and COMPLIANCE modes, with `x-amz-bypass-governance-retention` and
  s3:BypassGovernanceRetention.
- **Legal hold:** PutObjectLegalHold and GetObjectLegalHold (`?legal-hold[&versionId]`).
- **Default retention:** the bucket rule's retention on every create write that names none.
- **Lock headers:** `x-amz-object-lock-mode`, `-retain-until-date` and `-legal-hold` stored
  by PutObject, CopyObject and CreateMultipartUpload, and sent by GET and HEAD.
- **Enforcement:** DeleteObject and DeleteObjects naming a version are refused while the
  version is retained or held.

Not in this unit:
- PutBucketVersioning's refusal of anything but Enabled on a lock bucket is V10's op.
- Lifecycle's lock checks are the C unit's (C2, C10a, C10b).
- Admin removals and purges ignore the lock (P2, L-D4); versioning's V8b and V12b serve them
  and V12b writes that exclusion.

Exclusions this unit retires:
- "CreateBucket with object lock answers 501 until phase 2" (L2);
- "No object-lock headers" (L5b);
- "A multipart upload with a retention or a legal hold on a bucket with object lock answers
  501" (L5b);
- the object-lock 501 that V11 and V12a narrow phase 1's write and delete refusals to (VD-6):
  L5b lifts it for writes, L4 for deletes.

"CopyObject refuses an object lock on a bucket without object lock" stays.

## Decisions

The index's rulings this unit carries are D12 and D13, and the pending-owner default P2.

| Id | Decision | Reason |
|---|---|---|
| L-D1 (D12) | A date past real_time's range, 2554-07-21T23:34:33.709551615Z (u64 nanoseconds), is refused, and a computed default retention saturates there. A PutObjectLockConfiguration rule whose period from now passes it is InvalidRetentionPeriod. A `RetainUntilDate` past it is the parser's MalformedXML in a body and InvalidArgument in a header. A default retention computed at a write is clamped to the limit. | radosgw wraps the sum in u64 nanoseconds, often to a past date, which leaves the object unprotected at once (defect LL-4). A clamped date reads in radosgw as a lock to 2554, so the RADOS state stays compatible and the failure is closed. |
| L-D2 (D13) | PutObjectRetention treats an existing retention whose retain-until second is not after now as absent: no shortening or mode check applies to it. | It only ever lets a client place new protection, and it is what a lapsed lock means in S3 (defect LL-8). |
| L-D3 | DeleteObjects evaluates s3:BypassGovernanceRetention per key on the object's ARN, as DeleteObject does. | radosgw checks it once on the bucket ARN, so a Deny scoped to `arn:aws:s3:::b/*` does not bind it (defect LL-5, security). |
| L-D4 (P2, the owner, 2026-10-13) | Admin object removal and bucket purge ignore object lock, as radosgw does; V12b writes the exclusion. | The admin caps are the operator's tool, and Rook's bucket deletion relies on the purge. |
| L-D5 | The typed RGWObjectLock lives in `internal/meta/objectlock.go`, as methods on `BucketInfo`; the `ObjLock` field stays a RawStruct. Every write re-encodes the rule it read. | `bucket_info.go` is a hot file, and the registry's "radosgw's bucket instance metadata put drops the bucket's object lock rule" (#71157) is what rgw-go must not repeat. |
| L-D6 | Retention and legal-hold writes go through V6's `SetObjectAttrs` on the resolved version, which is `modify_obj_attrs`, 1 ns mtime nudge included. | It is radosgw's RADOS write, and its fresh write tag is what V8c's `Expect` detects. |
| L-D7 | Enforcement fails closed: deletes on a lock bucket stay 501 after V12a (VD-6) until L4 adds its check to V12a's ops and lifts it, and an attr that does not decode refuses the delete. | A radosgw-created lock bucket is reachable through rgw-go whatever L2 serves, so no merge may open a delete path that skips the check. |
| L-D8 | GET and HEAD send each lock header only when the requester may s3:GetObjectRetention or s3:GetObjectLegalHold, and skip an attr that does not decode. v20.2.4's refusal of a system request lacking either permission is not reproduced. | radosgw's behaviour; a system request is multisite sync, which is excluded. |
| L-D9 | The response documents keep radosgw's bytes: no xmlns on `ObjectLockConfiguration`, `Retention` and `LegalHold`, and the 9-digit nanosecond date. | D16: only document bytes SDKs ignore differ from AWS, and the cross-gateway oracle stays byte-comparable. |
| L-D10 | Writes to a bucket with object lock stay refused (501) until L5a and L5b land: V11 narrows phase 1's write refusal to object lock (VD-6), and L5b lifts it. | A write that skipped the default retention, or dropped a header's retention, would leave an object unprotected that radosgw would have locked: a fail-open window between merges. |
| L-D11 | A delete whose removal V8c's `Expect` refuses is checked again. The refusal leaves the version as before the delete: head, instance entry and list entry, listed in ListObjectVersions, with the write that raced it, and current again if it was (V8c B4). L4 then re-reads the version and re-runs its check, up to a bound named as a constant: locked now is 403, otherwise the delete retries on the new state. Past the bound the version stays listed and V12a's lost-race answer stands. The one window V8c leaves that touches the lock is accepted with no exclusion of its own: another reader replays the unlink's log entry before rgw-go's own replay, and removes the head whatever was written after the check (V8c B5). | Re-checking keeps the answer honest: a delete that lost to a retention answers 403, not V12a's 204 over a version still there. The replay window is radosgw's own behaviour, since every radosgw replay removes what its log says. The delete had passed its lock check, so the window grants no capability the requester lacked a moment earlier. V8c's exclusion records the refusal and re-link; L4 adds the re-check to the L-D3 exclusion. |

## Seams

Versioning's ids are V-versioning.md's.

**Consumed from other units:**
- **S2:** the object-lock route-set file it stubs. This unit needs no `op.Env` field: L4's check
  is op code in V12a's ops.
- **V2:** `meta.BucketInfo.VersioningEnabled()` (PutObjectLockConfiguration's check, and
  CreateBucket's flags).
- **V6:** `StatObject` and `PrefetchObject` follow the OLH; after a follow `ObjectState.Key` is
  the resolved version's key (`Key.Instance` the version id), and `ObjectState.DeleteMarker`
  marks a current delete marker, whose state does not exist. `SetObjectAttrs` writes the
  resolved version.
- **V7a and V7b:** the attrs-finalizing call, one per head write, given the bucket record,
  the mtime the write stamps and the create flag, an identity until L5a fills it; V7b puts CopyObject and
  CompleteMultipartUpload on V7a's path.
- **V8a and V8c:** `DeleteObject(ctx, rec *BucketRecord, key meta.ObjKey, p DeleteParams) (*DeleteResult, error)`,
  with `DeleteParams.Expect *ObjectState`, which V8a defines and V8c gives its behaviour. The
  version head's write tag must still be `Expect`'s before the unlink, and the own replay
  removes the head only under it. A removal refused after the unlink is re-linked at the
  version's own epoch, or at 0 when it was current, and answers `op.ErrConcurrentModification`.
- **V10:** PutBucketVersioning's refusal on a lock bucket.
- **V11:** the object-lock gate it narrows phase 1's write refusal to (VD-6), which L5b lifts.
- **V12a:** the versioned DeleteObject and DeleteObjects ops, whose files L4 edits after it,
  with their lock-bucket 501 (VD-6), which L4 lifts.
- **V14a:** the versioned reads in `internal/s3/getobject.go`, before L5b (b).
- **WP-5:** owns `internal/op/multipart.go` first; L5a adds two line-local fields after it.
- **WP-13:** writepath's last header task on the S3 handlers (checksum rendering on reads,
  after WP-7's on writes), before L5b.
- **Existing:** `op.ReadParamBody` (`internal/op/parambody.go`), `op.RetryRacedBucketWrite`
  and `retryBucketWrite` (`internal/op/bucketsubres.go:40-87`), `op.VerifyBucketPermissionIn`
  (`internal/op/deleteobjects.go:237`), `meta.ObjectRetention` and `meta.ObjectLegalHold`
  (`internal/meta/objectlock.go`), `meta.MultipartUploadInfo`'s lock fields
  (`internal/meta/multipart.go:197-253`), the completion's attrs
  (`internal/driver/mp_complete.go:353-357`), and the copy's lock-attr rule
  (`internal/driver/copy.go:142-144`).

**Produced:**
- `meta.MaxRealTime`, the limit of L-D1.
- `func (b *BucketInfo) ObjectLock() (ObjectLock, error)` and
  `func (b *BucketInfo) SetObjectLock(ObjectLock)`, over
  `type ObjectLock struct { Enabled bool; Rule *ObjectLockRule }`.
- Package `internal/iso8601`: `Parse(s string) (time.Time, bool)`, which is `from_iso_8601`
  with `ws_terminates` and refuses past `meta.MaxRealTime`, and `Format(time.Time) string`,
  `to_iso_8601`'s YMDhmsn.
- Package `internal/objectlock` (pure, no `op` import; its own error values, which `op` maps):
  - `ParseConfiguration`, `ParseRetention` and `ParseLegalHold` from `[]byte`, and `Marshal*`;
  - `RetentionPeriodValid(DefaultRetention) bool`;
  - `LockUntil(DefaultRetention, mtime time.Time) time.Time`, saturating;
  - `VerifyDelete(attrs map[string][]byte, now time.Time, bypassHeader, bypassAllowed bool) error`;
  - `RetentionChange(old *meta.ObjectRetention, proposed meta.ObjectRetention, now time.Time, bypass bool) error`.
- `op.BypassGovernanceHeader(v string) bool` (L3), the header url-decoded and "true" in any
  case, for L3's and L4's handlers.
- **For the C unit:** `objectlock.VerifyDelete` with both bypass flags false, which is
  `pass_object_lock_check`'s rule (C2's lock predicate).
- **For C11:** L5a's default retention on the transition's rewrite, a create write.

## Hot files

| File | Tasks | Owner and rule |
|---|---|---|
| `internal/meta/objectlock.go` | L1 | L1; C2 and L4 only call it |
| `internal/meta/bucket_info.go` | none here | not edited (L-D5) |
| `internal/s3/putobject.go` | V11, WP-7, WP-13, L1 (a), L5b | L1 (a) swaps `fromISO8601` for `iso8601.Parse`, one call line, early; L5b after WP-13, adding a helper with one call line per site |
| `internal/s3/getobject.go`, `copyobject.go`, `multipart.go`, `internal/op/getobject.go` | V11, V14a, WP-7, WP-13, L5b, C8 | order: versioning, then writepath's header tasks, then L5b, then C8; each adds its headers in its own helper |
| `internal/driver/headwrite.go` | V7a, L5a | L5a after V7a: it fills V7a's attrs-finalizing call |
| `internal/op/multipart.go` (`UploadParams`), `internal/driver/mp_upload.go` | WP-5, L5a | WP-5 first; L5a adds two fields and copies them into the upload info |
| the object-lock route-set file | S2, L2, L3 | S2 stubs it; L2 then L3 add line-local entries |
| `internal/op/deleteobject.go`, `deleteobjects.go`, `internal/s3/deleteobject.go`, `deleteobjects.go` | V12a, L4 | V12a first; L4 then adds the check between its state read and its delete |
| `internal/op/bucketops.go`, `internal/s3/bucket.go` (CreateBucket) | L2 (b), BC tasks | L2 (b) edits only the object-lock branch |
| `docs/exclusions.md`, `docs/ceph-upstream-bugs.md` | every task | each PR edits only its own entries |

## Tasks

### Task L1: object-lock types, XML, dates and evaluation

- **Layer:** pure library and meta. **Cluster:** no.
- **Files:** create `internal/iso8601/` and `internal/objectlock/` (each with its suite);
  modify `internal/s3/putobject.go` (one call line). `internal/meta/objectlock.go`, the typed
  RGWObjectLock, is F8's (FD-7), built under the names this task uses.
- **Consumes:** `meta.ObjectRetention`, `meta.ObjectLegalHold`, `meta.BucketInfo.ObjLock`
  (`internal/meta/bucket_info.go:77-110`), `xmltext.Parse`.
- **Produces:** `meta.MaxRealTime`, package `iso8601`, package `objectlock` (Seams). It consumes
  `BucketInfo.ObjectLock` and `SetObjectLock` from F8.
- **Behaviour:**
  1. **Encodings.** `DefaultRetention` v1 {mode string, days int32, years int32},
     `ObjectLockRule` v1 {DefaultRetention}, `RGWObjectLock` v1 {enabled, rule_exist, the rule
     only when rule_exist}; the default is enabled with no rule
     ([rgw_object_lock.h:12-154](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_object_lock.h#L12-L154), the same encodings as
     [rgw_object_lock.h:11-149](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_object_lock.h#L11-L149); v19.2.7 adds only `decode_json` declarations).
     - `ObjectLock()` decodes `ObjLock` only when the flags carry `BUCKET_OBJ_LOCK_ENABLED`,
       and is the disabled zero value otherwise. `SetObjectLock` writes `ObjLock` and leaves
       the flags to the caller.
     - Decoding then encoding is byte-identical, a rule included (L-D5).
  2. **Configuration XML** ([rgw_object_lock.cc:14-91](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_object_lock.cc#L14-L91), whose XML functions
     are the same at [rgw_object_lock.cc:8-75](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_object_lock.cc#L8-L75)). Every failure is the package's
     malformed-XML error:
     - `ObjectLockEnabled` is mandatory and exactly "Enabled"; `Rule` is optional;
     - `DefaultRetention` needs `Mode` GOVERNANCE or COMPLIANCE, and exactly one of `Days`
       and `Years` present, judged by presence, not value;
     - integers are read as `decode_xml_obj(int)` reads them: strtol over the whole string,
       trailing blanks allowed, int range ([rgw_xml.cc:282-377](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_xml.cc#L282-L377),
       [rgw_xml.cc:282-377](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_xml.cc#L282-L377)).
     - The dump has no xmlns, `ObjectLockEnabled` when enabled, and the rule when present,
       with `Days` when positive and `Years` otherwise.
  3. **Retention and legal-hold XML** ([rgw_object_lock.cc:124-149](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_object_lock.cc#L124-L149),
     [rgw_object_lock.cc:108-133](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_object_lock.cc#L108-L133)): `Mode` GOVERNANCE or COMPLIANCE and a
     `RetainUntilDate` that `iso8601.Parse` reads are mandatory; `Status` is ON or OFF. The
     date is dumped through `iso8601.Format`.
  4. **`RetentionPeriodValid`:** exactly one of days and years positive
     ([rgw_object_lock.h:116-121](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_object_lock.h#L116-L121)).
  5. **`LockUntil`:** mtime + days × 86400 s when days is positive, else mtime + years ×
     31,556,952 s (`std::chrono::years`, 365.2425 days), clamped to `meta.MaxRealTime`
     ([rgw_object_lock.cc:103-111](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_object_lock.cc#L103-L111),
     [rgw_object_lock.cc:87-95](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_object_lock.cc#L87-L95); L-D1). A
     companion predicate tells whether the period from a given now stays inside the range,
     for L2's refusal.
  6. **`VerifyDelete`** is `verify_object_lock`
     ([rgw_common.cc:1639-1667](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L1639-L1667),
     [rgw_common.cc:1702-1730](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L1702-L1730)), in its order:
     - a retention attr that does not decode is the undecodable error (EIO);
     - a retain-until whose whole second is after now is locked, unless the mode is
       GOVERNANCE and both bypass flags are set;
     - a legal-hold attr that does not decode is the undecodable error; ON is locked.
  7. **`RetentionChange`** is PutObjectRetention's check of an existing retention
     ([rgw_op.cc:8358-8435](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8358-L8435),
     [rgw_op.cc:9293-9383](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9293-L9383)), compared in whole
     seconds, in this order:
     - no existing retention, or one that has lapsed (L-D2), passes;
     - a proposed date earlier than the old one needs the old mode GOVERNANCE and the bypass,
       else access-denied, "proposed retain-until date shortens an existing retention period
       and governance bypass check failed";
     - the same mode passes;
     - COMPLIANCE to GOVERNANCE is access-denied, "can't change retention mode from
       COMPLIANCE to GOVERNANCE";
     - GOVERNANCE to COMPLIANCE needs the bypass, else access-denied, "can't change retention
       mode from GOVERNANCE without governance bypass".
  8. **`iso8601.Parse`** is `from_iso_8601` with `ws_terminates`
     ([iso_8601.cc:51-151](https://github.com/ceph/ceph/blob/v19.2.7/src/common/iso_8601.cc#L51-L151),
     [iso_8601.cc:51-151](https://github.com/ceph/ceph/blob/v20.2.4/src/common/iso_8601.cc#L51-L151)): phase 1's
     grammar in `s3.fromISO8601`, now keeping the nanoseconds, and refusing a time past
     `meta.MaxRealTime` instead of wrapping (L-D1). **`Format`** is `to_iso_8601`'s YMDhmsn,
     nine fractional digits and a Z ([iso_8601.h:39-43](https://github.com/ceph/ceph/blob/v19.2.7/src/common/iso_8601.h#L39-L43),
     [iso_8601.h:39-43](https://github.com/ceph/ceph/blob/v20.2.4/src/common/iso_8601.h#L39-L43)). `s3.fromISO8601`'s caller switches to it.
- **Specs:**
  - corpus goldens from the ceph checkout's `ceph-object-corpus` for `RGWObjectLock`,
    `ObjectLockRule`, `DefaultRetention`, `RGWObjectRetention` and `RGWObjectLegalHold`;
  - `ObjectLock()` on a record without the flag, and a byte-identical round trip with a rule;
  - XML error tables for the three documents in radosgw's order, and dump bytes (Days or
    Years, no Rule, no xmlns, nine digits);
  - the `VerifyDelete` matrix: mode × bypass header × permission × past or future second ×
    legal hold ON, OFF or absent × an attr that does not decode;
  - the `RetentionChange` matrix, with a lapsed COMPLIANCE retention replaced by a shorter
    GOVERNANCE one (L-D2);
  - `LockUntil` with days, with years as 365.2425 days, and the clamp at
    `meta.MaxRealTime` and one nanosecond past it;
  - `iso8601`: every phase 1 `fromISO8601` case, one to nine fractional digits, a year
    before 1970, and a date one second past the limit refused.
- **Exclusions and registry:** the L-D1 exclusion, covering L2's refusal, the parsers' and
  L5a's clamp. The registry entry for defect LL-4, class security, unreproduced; the
  reproduction is budgeted in C13 (a), and the entry goes to the rgw-bug-reproduction
  session (index, Global Constraints).
- **Depends on:** nothing. **PR split:** (a) `iso8601` and the `s3.fromISO8601` swap;
  (b) the meta encodings and goldens; (c) `objectlock`'s XML and evaluation. **Brief:**
  about 220 lines.

### Task L2: bucket object-lock configuration, and CreateBucket with lock

- **Layer:** op and S3 handler. **Cluster:** no.
- **Files:** create `internal/op/bucketobjectlock.go`, `internal/s3/bucketobjectlock.go` and
  their specs; modify the object-lock route-set file, and the object-lock branch of
  `internal/op/bucketops.go:85-88,210-216` and `internal/s3/bucket.go:116`.
- **Consumes:** L1; S2's route set; V2's `VersioningEnabled`; `op.ReadParamBody`;
  `retryBucketWrite`; `BucketStore.PutBucketInfo`. **Produces:** the two ops.
- **Behaviour:**
  1. **PutObjectLockConfiguration**
     ([rgw_op.cc:8230-8306](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8230-L8306), [rgw_op.cc:9165-9241](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9165-L9241)), in order:
     1. s3:PutBucketObjectLockConfiguration on the bucket.
     2. Versioning not Enabled is 409 InvalidBucketState, "Object lock cannot be enabled
        unless the bucket has versioning enabled", before the body is read.
     3. The body through `ReadParamBody` under `rgw_max_put_param_size`
        ([rgw_rest.cc:1484-1502](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L1484-L1502), [rgw_rest.cc:1484-1502](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L1484-L1502)).
     4. A parse failure or an invalid document is 400 MalformedXML.
     5. A rule that fails `RetentionPeriodValid` is 400 InvalidRetentionPeriod, "retention
        period must be a positive integer value"; so is a rule whose period from now passes
        `meta.MaxRealTime` (L-D1).
     6. radosgw's forward to the master is a no-op here.
     7. Under `retryBucketWrite`, re-authorized: when the flag is missing, versioning is
        checked again (InvalidBucketState) and `BUCKET_OBJ_LOCK_ENABLED` set; the lock is
        replaced with `SetObjectLock`; `PutBucketInfo`.
     - The answer is 200 with no body.
  2. **GetObjectLockConfiguration** ([rgw_op.cc:8313-8332](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8313-L8332),
     [rgw_op.cc:9248-9267](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9248-L9267)): s3:GetBucketObjectLockConfiguration; without
     the flag, 404 ObjectLockConfigurationNotFoundError; otherwise L1's dump
     ([rgw_rest_s3.cc:4504-4518](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4504-L4518),
     [rgw_rest_s3.cc:5015-5029](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5015-L5029)).
  3. **CreateBucket with lock:** a new bucket's record gets
     `BUCKET_VERSIONED | BUCKET_OBJ_LOCK_ENABLED` and the default lock, enabled with no rule
     ([rgw_rados.cc:2404-2406](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L2404-L2406),
     [rgw_rados.cc:2496-2498](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L2496-L2498)). The already-owned path is phase
     1's, unchanged. The phase 1 501 goes.
- **Specs:**
  - on counterfeiter fakes, each error in order: the 409 before a malformed body, MalformedXML
    cases, both InvalidRetentionPeriod cases (Days and Years both set; `Years` 600 from now);
  - a racing bucket write retried, and a permission revoked between attempts refused;
  - the flag set only when missing, and a rule replaced and then removed;
  - GET's 404 and its document bytes;
  - CreateBucket with lock: both flags, and the `ObjLock` bytes equal phase 1's default;
    the header false or absent sets neither; the admin bucket dump's `object_lock_enabled`
    (`internal/admin/bucket.go:152`);
  - memstore and driver round trips;
  - once V10 has landed: PutBucketVersioning Suspended on a bucket L2 just locked is 409.
- **Exclusions and registry:** retires "CreateBucket with object lock answers 501 until phase
  2". L-D1's exclusion is L1's.
- **Depends on:** L1 (b, c), S2, V2; the last spec on V10. **PR split:** (a) the
  configuration ops; (b) CreateBucket with lock. **Brief:** about 170 lines.

### Task L3: object retention and legal hold

- **Layer:** op and S3 handler. **Cluster:** no.
- **Files:** create `internal/op/objectretention.go`, `internal/s3/objectretention.go` and
  their specs; modify the object-lock route-set file (after L2).
- **Consumes:** L1; V6's `StatObject` and `SetObjectAttrs` on the resolved version;
  `ReadParamBody`. **Produces:** the four ops, and `op.BypassGovernanceHeader`.
- **Behaviour:**
  1. **PutObjectRetention.**
     - verify_permission ([rgw_op.cc:8334-8351](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8334-L8351),
       [rgw_op.cc:9269-9286](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9269-L9286)):
       s3:PutObjectRetention on the object; then get_params reads the bypass header and the
       body ([rgw_rest_s3.cc:4521-4532](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4521-L4532),
       [rgw_rest_s3.cc:5032-5043](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5032-L5043));
       only when the header is set is s3:BypassGovernanceRetention evaluated on the object.
     - `BypassGovernanceHeader` is get_params' reading of the header: url-decoded, then "true"
       in any case. DeleteObject's is the same
       ([rgw_rest_s3.cc:3435-3483](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3435-L3483),
       [rgw_rest_s3.cc:3685-3733](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L3685-L3733)).
     - execute ([rgw_op.cc:8358-8435](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8358-L8435),
       [rgw_op.cc:9293-9383](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9293-L9383)), in order:
       1. a bucket without lock is 400 InvalidRequest, "object retention can't be set if
          bucket object lock not configured";
       2. a document L1 refuses is MalformedXML;
       3. a retain-until second before now is 400 InvalidArgument, "the retain-until date
          must be in the future";
       4. the version's state, `versionId` honoured (`null` included); a missing object or
          version, or a delete marker, is phase 1's object 404, with what V14a sends there;
       5. an existing retention that does not decode is 500;
       6. `RetentionChange` with bypass = header and permission, its refusals 403
          AccessDenied with their messages;
       7. `SetObjectAttrs` of the retention, v2 with nanoseconds (L-D6;
          [rgw_sal_rados.cc:2402-2420](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_sal_rados.cc#L2402-L2420),
          [rgw_sal_rados.cc:2994-3012](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L2994-L3012)).
     - 200 with no body. v20.2.4's bucket-logging record is phase 3's.
  2. **GetObjectRetention** ([rgw_op.cc:8437-8484](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8437-L8484),
     [rgw_op.cc:9385-9432](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9385-L9432)): s3:GetObjectRetention; no lock is
     InvalidRequest, "bucket object lock not configured"; the state's 404; no attr is 404
     ObjectLockConfigurationNotFoundError; an attr that does not decode is 500; otherwise
     `<Retention>`
     ([rgw_rest_s3.cc:4543-4557](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4543-L4557),
     [rgw_rest_s3.cc:5054-5068](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5054-L5068)).
  3. **PutObjectLegalHold** ([rgw_op.cc:8503-8539](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8503-L8539),
     [rgw_op.cc:9451-9507](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9451-L9507)): s3:PutObjectLegalHold;
     no lock is InvalidRequest; the body and parse as above; the state's 404 on both releases
     (v19.2.6's `modify_obj_attrs` reads the attrs first); `SetObjectAttrs` of the legal hold.
  4. **GetObjectLegalHold** ([rgw_op.cc:8558-8588](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8558-L8588),
     [rgw_op.cc:9526-9556](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9526-L9556)): Get's shape, with
     `<LegalHold>`
     ([rgw_rest_s3.cc:4568-4582](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4568-L4582),
     [rgw_rest_s3.cc:5079-5093](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5079-L5093)).
- **Specs:**
  - the `RetentionChange` matrix through the op, header and permission each with and without
    the other, and a lapsed retention (L-D2);
  - the bypass permission is not evaluated without the header (a counting authorizer fake);
    the header readings `true`, `TRUE`, `%74rue` and `yes`;
  - InvalidRequest without lock for all four ops; a past date; a date past the limit;
  - a retention attr that does not decode: 500 on Put and Get;
  - no attr: 404 ObjectLockConfigurationNotFoundError; a missing version and a current
    delete marker: 404;
  - a noncurrent version written and read by `versionId`, the current left alone;
  - the write moves the mtime by 1 ns and replaces the idtag (registry quirk);
  - response bytes; a body over `rgw_max_put_param_size`; a payload-hash mismatch refused.
- **Exclusions and registry:** L-D2's exclusion and the registry entry for defect LL-8, class
  S3-correctness, unreproduced (reproduction in C13 (a)).
- **Depends on:** L1, L2 (the route-set file), S2, V6. **PR:** one. **Brief:** about 200
  lines.

### Task L4: delete enforcement

- **Layer:** op and S3 handler. **Cluster:** no.
- **Files:** create `internal/op/objectlock.go` and its spec; modify
  `internal/op/deleteobject.go`, `deleteobjects.go`, `internal/s3/deleteobject.go` and
  `deleteobjects.go`, after V12a (one helper call and one field each).
- **Consumes:** L1 (c)'s `VerifyDelete`; L3's `BypassGovernanceHeader`;
  `op.VerifyBucketPermissionIn`; V12a's ops and their state read; `DeleteParams.Expect`, which
  V8a defines and V8c implements.
- **Produces:** the lock check on both delete ops, and the end of their lock-bucket 501.
- **Behaviour:**
  1. **The header.** Both handlers read `x-amz-bypass-governance-retention` through
     `BypassGovernanceHeader` into a field of their op
     ([rgw_rest_s3.cc:3435-3483](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3435-L3483),
     [rgw_rest_s3.cc:3685-3733](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L3685-L3733);
     [rgw_rest_s3.cc:4261-4275](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4261-L4275),
     [rgw_rest_s3.cc:4779-4793](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4779-L4793)).
  2. **When it runs:** a delete that names a version, `null` included, on a bucket with lock,
     between V12a's state read and its delete. A state that does not exist skips it: a
     delete marker's own version has no head, and radosgw skips the check on ENOENT
     ([rgw_op.cc:5173-5221](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L5173-L5221),
     [rgw_op.cc:5563-5610](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L5563-L5610)). A delete without
     `versionId` is never checked; it writes a marker.
  3. **The bypass permission** is evaluated only when the header is set, as
     s3:BypassGovernanceRetention on the key's object ARN, with the request's identity and
     block settings. A refusal of any kind is "not allowed" and never fails the request.
     - That is DeleteObject's check ([rgw_op.cc:5138-5166](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L5138-L5166),
       [rgw_op.cc:5528-5556](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L5528-L5556)).
     - radosgw's DeleteObjects checks it once on the bucket ARN
       ([rgw_op.cc:6774-6786](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L6774-L6786),
       [rgw_op.cc:7707-7719](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7707-L7719);
       [rgw_common.cc:1469-1476](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L1469-L1476), [rgw_common.cc:1506-1513](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L1506-L1513)).
       rgw-go checks each key's object ARN (L-D3).
  4. **The check** is `VerifyDelete` on the state's attrs at `r.Env.Clock()`. Locked is 403
     AccessDenied, with the message "forbidden by object lock" on DeleteObject; undecodable
     is 500 InternalError. DeleteObjects renders a refused key as phase 1 renders an
     AccessDenied key ([rgw_op.cc:6824-6875](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L6824-L6875),
     [rgw_op.cc:7743-7805](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7743-L7805)).
  5. **The removal** passes `Expect` = the state the check read, so a retention or legal hold
     written after the check fails V8c's write-tag check, before the unlink or at the removal.
  6. **After a refusal** (L-D11): `op.ErrConcurrentModification` from a delete that carried
     `Expect` means V8c left the version listed as before. L4 re-reads it, runs items 3 and 4
     again on the new state, and retries the delete with that state as `Expect`, up to the
     bound. A version locked now is refused as in item 4. Past the bound, V12a's lost-race
     answer stands.
  7. **The 501 goes:** V12a's refusal of a delete on a lock bucket (VD-6) is removed in the
     same PR.
  8. Admin removal and purge never run it (L-D4): `verify_object_lock` has only the two S3
     callers at both tags.
- **Specs**, on op fakes and on memstore through `op.DeleteObject` and `op.DeleteObjects`:
  - L1's matrix through both ops;
  - a Deny of s3:BypassGovernanceRetention on `arn:aws:s3:::b/*` refuses the bypass for a
    DeleteObjects key as for DeleteObject;
  - the permission not evaluated without the header (a counting authorizer fake);
  - a locked version refused and kept; GOVERNANCE removed with header and permission;
  - a delete without `versionId` writes a marker over a locked version;
  - a delete marker's own version removed;
  - **after a refused removal**, on the emulator through the driver, with a hook between the
    unlink and the removal as V8c's specs use:
    - a retention written between the check and the unlink, and one written between the
      unlink and the removal, each answer 403 "forbidden by object lock";
    - the version's head, instance entry and list entry exist, it is in ListObjectVersions,
      and it is current again if it was;
    - an ACL written in the same window lets the retry remove the version, with one 204;
    - a writer that changes the version on every try exhausts the bound, and the version
      stays listed;
  - a bucket without lock is never checked; a lock bucket no longer answers 501.
- **Exclusions and registry:** the L-D3 exclusion and the registry entry for defect LL-5,
  class security, unreproduced (reproduction in C13 (a); to the rgw-bug-reproduction
  session), with a sentence for L-D11's re-check; retires the delete half of VD-6's
  object-lock 501.
- **Depends on:** L1 (c), L3, V8c, V12a. **PR:** one. **Brief:** about 190 lines.

### Task L5a: the lock on the write path, driver

- **Layer:** driver (and memstore). **Cluster:** no.
- **Files:** create `internal/driver/objectlock.go`, which fills V7a's attrs-finalizing call;
  modify `internal/driver/headwrite.go` at that call only, `internal/driver/mp_upload.go`
  (`CreateUpload`'s info literal), `internal/op/multipart.go` (`UploadParams`, two fields), and
  memstore's equivalents.
- **Consumes:** L1's `ObjectLock` and `LockUntil`; V7a's attrs-finalizing call and V7b's
  copy and completion on V7a's path.
  **Produces:** `op.UploadParams.Retention *meta.ObjectRetention` and
  `op.UploadParams.LegalHold *meta.ObjectLegalHold`; the default retention for C11's
  rewrite.
- **Behaviour:**
  1. **Default retention** ([rgw_rados.cc:3211-3221](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L3211-L3221),
     [rgw_rados.cc:3308-3318](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L3308-L3318)). When the bucket's flags carry
     lock, its lock has a rule, the write is a create, and the attrs carry no
     `user.rgw.object-retention`, the head write adds one: {rule mode,
     `LockUntil(rule, set_mtime)`}, in the same RADOS op. It is V7a's attrs-finalizing call,
     which has the record and set_mtime, the mtime `writeMeta` fixes once
     (`internal/driver/headwrite.go:153`). The call must also tell a create write from the
     others (item 2), which V7a's call passes as its create flag.
  2. **The create writes** are radosgw's `PUT_OBJ_CREATE` writes:
     - the atomic PUT and the streamed copy's processor
       ([rgw_putobj_processor.cc:384](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_putobj_processor.cc#L384),
       [rgw_putobj_processor.cc:414](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_putobj_processor.cc#L414)), which also serves the
       transition's rewrite;
     - the shared-tail copy ([rgw_rados.cc:5008](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L5008),
       [rgw_rados.cc:5236](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L5236));
     - the multipart completion ([rgw_sal_rados.cc:3633](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_sal_rados.cc#L3633),
       [rgw_sal_rados.cc:4477](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L4477)).
     - Not part heads, which set no flag, nor the meta object, which is `PUT_OBJ_CREATE_EXCL`
       ([rgw_sal_rados.cc:3308](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_sal_rados.cc#L3308),
       [rgw_sal_rados.cc:4148](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L4148)). In rgw-go those are the
       head writes with `create` set that are not `nonAtomic`.
     - v20.2.4's other create writes (bucket logging, restore, cloud tier) are outside phase 2.
  3. **CreateMultipartUpload's lock** goes into `multipart_upload_info`'s four fields from the
     two new params ([rgw_sal_rados.cc:3314-3321](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_sal_rados.cc#L3314-L3321),
     [rgw_sal_rados.cc:4156-4163](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L4156-L4163)). The completion already
     writes them as attrs (`internal/driver/mp_complete.go:353-357`;
     [rgw_sal_rados.cc:3601-3610](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_sal_rados.cc#L3601-L3610),
     [rgw_sal_rados.cc:4445-4454](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L4445-L4454)), so the default applies at
     completion only when the upload named none.
- **Specs**, on fakerados and memstore alike:
  - the retention attr's bytes for a PUT, a shared-tail copy, a streamed copy and a
    completion, against radosgw's encoding;
  - none on a part head or the meta object; none without a rule, or with a rule on a record
    whose flags lack lock;
  - an attr already present (a header's, or the upload's) wins;
  - the date from the stamped mtime under a fake clock, for days and for years; a rule of
    `Years` 600 clamps to `meta.MaxRealTime`;
  - `multipart_upload_info` bytes with and without the lock fields, against P's encoder.
- **Exclusions and registry:** none of its own; L-D1's clamp is L1's exclusion.
- **Depends on:** L1 (b, c), V7a, V7b, WP-5. **PR:** one. **Brief:** about 150 lines.

### Task L5b: the lock headers, op and S3

- **Layer:** op and S3 handler. **Cluster:** no.
- **Files:** modify `internal/op/initmultipart.go` (the two `UploadParams` fields), `internal/s3/putobject.go`, `copyobject.go`, `multipart.go` and
  `getobject.go` (one helper each in a new `internal/s3/objectlockheaders.go`, one call line
  per site), and `internal/op/getobject.go` (the two permissions, one helper call).
- **Consumes:** L1; L5a's `UploadParams` fields; phase 1's `objectLockHeaders`.
- **Produces:** the headers; the retirement of L-D10's refusals.
- **Behaviour:**
  1. **PutObject and CopyObject** store the header retention, {mode, the date with its
     nanoseconds}, and the legal hold as attrs
     ([rgw_op.cc:4542-4551](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L4542-L4551), [rgw_op.cc:4810-4819](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4810-L4819);
     [rgw_op.cc:5587-5596](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L5587-L5596), [rgw_op.cc:6150-6159](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L6150-L6159)).
     - Phase 1's value checks stay, with L-D1's refusal of a date past the limit as
       InvalidArgument.
     - A copy drops the source's lock attrs and keeps the request's, as phase 1's copy
       already does (`internal/driver/copy.go:142-144`;
       [rgw_rados.cc:4800-4807](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L4800-L4807),
       [rgw_rados.cc:5013-5020](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L5013-L5020)).
  2. **CreateMultipartUpload** sets the two `UploadParams` fields.
  3. **The refusals:** the 501 for a write naming a retention or a legal hold on a lock
     bucket goes (`internal/s3/multipart.go:289-305`), and so does V11's object-lock gate on
     the write ops (VD-6, L-D10). The InvalidRequest on a bucket without lock stays.
  4. **GET and HEAD** (L-D8). On a bucket with lock, the op evaluates s3:GetObjectRetention
     and s3:GetObjectLegalHold on the object ([rgw_op.cc:1007-1010](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L1007-L1010),
     [rgw_op.cc:1148-1164](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L1148-L1164)). The handler then sends, from the resolved
     version's attrs:
     - `x-amz-object-lock-mode` and `x-amz-object-lock-retain-until-date` (`iso8601.Format`)
       when the first is allowed;
     - `x-amz-object-lock-legal-hold` when the second is;
     - nothing for an attr that does not decode
       ([rgw_rest_s3.cc:603-621](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L603-L621), [rgw_rest_s3.cc:712-730](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L712-L730)).
- **Specs:**
  - the attrs a PutObject and a CopyObject store, under COPY and REPLACE, against a source
    that carries a lock of its own;
  - the upload's params from CreateMultipartUpload headers;
  - a fractional-second date kept to the nanosecond; a date past the limit is InvalidArgument;
  - a lock-bucket write with and without lock headers succeeds once L5a is in;
  - GET and HEAD: each permission combination × attr present, absent and undecodable; a
    bucket without lock sends nothing; the date's nine digits.
- **Exclusions and registry:** retires "No object-lock headers", "A multipart upload with a
  retention or a legal hold on a bucket with object lock answers 501", and the write half of
  the object-lock 501 that V11 leaves of "Writes to a versioned or object-lock bucket answer
  501".
- **Depends on:** L5a, V11, V14a, WP-13 (hot-file order). **PR split:** (a) the write
  headers and the retired refusals; (b) the GET and HEAD headers. **Brief:** about 170
  lines.

## Order

No task in this unit needs a cluster. The object-lock s3-tests recording is C12's PR (a), and
the cross-gateway lock cases are C13's PR (a); both follow L5b.

| Wave | Tasks, in parallel | Waits for |
|---|---|---|
| 1 | L1 (a), L1 (b) | nothing |
| 2 | L1 (c) | L1 (a), L1 (b) |
| 3 | L2 (a), L2 (b) | L1, S2, V2 |
| 4 | L3 | L2, V6 (V's wave 6) |
| 5 | L5a | V7a, V7b, WP-5 (V's wave 8) |
| 6 | L4; L5b (a), then L5b (b) | L3, V8c and V12a for L4 (V's waves 8 and 10); L5a, V11, V14a and WP-13 for L5b |

Cross-unit order:
- Versioning keeps a lock bucket's S3 writes and deletes at 501 until L5b and L4 lift them
  (VD-6, L-D7, L-D10), so no V task waits on this unit.
- C10a consumes `objectlock.VerifyDelete` from L1 (c); C11's rewrite gets L5a's default
  retention.

## Defects

The research report's defect table, the rows this unit owns, carried with class and status.
Every new entry starts unreproduced, derived from the source at both tags; C13 (a) budgets the
reproductions on a disposable cluster (PP-11).

| Id | Defect | Releases | Class | Status | rgw-go's choice | Task |
|---|---|---|---|---|---|---|
| LL-4 | The lock-until date wraps: `get_lock_until_date` adds the period in u64 nanoseconds, so a default period ending after 2554-07-21 wraps, often to a past date, and the object is unprotected at once; `from_iso_8601` wraps an explicit date the same way ([rgw_object_lock.cc:103-111](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_object_lock.cc#L103-L111)). Tracker #56993's fix (dd87a0ff845) removed an int overflow; the chrono form wraps again. | both, and main | security (WORM fail-open) | unreproduced | L-D1: refuse what cannot be represented; clamp a computed default. Exclusion. | L1 |
| LL-5 | DeleteObjects checks s3:BypassGovernanceRetention once, on the bucket ARN ([rgw_op.cc:6780-6783](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L6780-L6783), [rgw_op.cc:7713-7716](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7713-L7716)), so a Deny on `arn:aws:s3:::b/*` does not bind it, while DeleteObject honours it. | both | security (authorization bypass) | unreproduced | L-D3: per key on the object ARN. Exclusion. | L4 |
| LL-8 | PutObjectRetention refuses COMPLIANCE to GOVERNANCE, and a shorter date without the bypass, even after the existing retention has lapsed ([rgw_op.cc:8413-8430](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8413-L8430), [rgw_op.cc:9293-9383](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9293-L9383)). | both | S3-correctness (fail-closed) | unreproduced | L-D2: a lapsed retention is absent. Exclusion. | L3 |

Existing registry entries this unit meets:
- "radosgw's bucket instance metadata put drops the bucket's object lock rule" (#71157): L1's
  typed lock re-encodes the rule it read, so rgw-go keeps it (L-D5).
- "radosgw's CopyObject stores an object lock on a bucket without object lock": rgw-go keeps
  refusing (L5b).
- "radosgw's date conditions wrap past 2554": the same u64 range as LL-4; L1's `iso8601`
  refuses rather than wraps.
- "radosgw ignores a payload-hash mismatch on bodies read by read_all_input" (security): every
  body here goes through `ReadParamBody`.
- "radosgw's SigV2 resource lists encryption and object-lock but never signs them": phase 1
  reproduces it for `?object-lock`; nothing changes.
