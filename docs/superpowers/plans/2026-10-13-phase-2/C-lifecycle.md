# Phase 2 unit C: lifecycle

Threat model: a client adversary at the configuration ops' parser boundary (the
LifecycleConfiguration grammar, its limits and Content-MD5), which gets adversarial review
(PP-14); the worker acts only on stored configurations and defends against honest mistakes and
races (radosgw's worker, client writes during a pass, a worker that dies or stalls), its lock
check judged as L's enforcement is.

## Scope

Spec §9, phase 2: "lifecycle with the worker for expiration, noncurrent expiration, multipart
abort and in-cluster storage-class transition"; spec §7: "phase 2 adds lifecycle" to the
workers. In this unit that is:
- **Configuration ops:** PutBucketLifecycleConfiguration, GetBucketLifecycleConfiguration and
  DeleteBucketLifecycle (`?lifecycle`), with the rule grammar of ID, Status, Filter (Prefix,
  Tag, And, ObjectSizeGreaterThan and LessThan, ArchiveZone) or the legacy Prefix,
  Expiration, NoncurrentVersionExpiration, AbortIncompleteMultipartUpload, Transition and
  NoncurrentVersionTransition.
- **The lc index:** the `lc.<i>` shards, their entries and heads, and the side effects of
  bucket deletion and `bucket.instance` metadata puts.
- **Headers:** `x-amz-expiration` on GET, HEAD and PutObject; `x-amz-abort-date` and
  `x-amz-abort-rule-id` on CreateMultipartUpload.
- **The worker:** current and delete-marker expiration, noncurrent expiration, abort of
  incomplete multipart uploads, and transition to a storage class of the bucket's placement,
  under radosgw's shard lock and claim protocol, beside radosgw's own worker.
- **The parity set:** the lifecycle and object-lock s3-tests, the admin subtest and the Rook
  sub-test, recorded on both releases (C12), and the cross-gateway gates (C13).

Not in this unit: cloud-s3 transition, restore, and v20.2.4's restore expirer, which
`docs/exclusions.md` keeps out ("Storage-class transitions inside the cluster are not
excluded"); the s3-tests harness and selection, which the T unit owns; no admin REST route
reads or writes lifecycle at either tag, so no admin op is added.

Exclusions this unit retires:
- "A stored lifecycle configuration answers 501 until phase 2" (C6);
- "No x-amz-expiration" and "No x-amz-expiration on a write" (C8);
- the lifecycle half of "The instance's lifecycle configuration and topic mappings, which
  radosgw updates after the write, are not kept by rgw-go yet" (C7).

## Decisions

The index's rulings this unit carries are D10, D11 and D14, the pending-owner default P1, and
the controller's ruling that splits C10 (C-D16).

| Id | Decision | Reason |
|---|---|---|
| C-D1 (D10) | rgw-go honours `rgw_enable_lc_threads`, default true, and runs radosgw's shard-lock and claim protocol, so either or both workers may run on a zone. It departs from the protocol in two places only: the head rule for an entry it leaves unclaimed (C-D4, C-D16), and the claim re-read, which before a bucket, at every listing page and before the post-state stops a pass whose claim is no longer its own. | radosgw documents several LC threads per zone as supported ([rgw.yaml.in:244-260](https://github.com/ceph/ceph/blob/v19.2.7/src/common/options/rgw.yaml.in#L244-L260), [rgw.yaml.in:252-268](https://github.com/ceph/ceph/blob/v20.2.4/src/common/options/rgw.yaml.in#L252-L268)), its lock and claim arbitrate, and deferring to it would leave lifecycle undone in an rgw-go-only zone. |
| C-D2 (D11) | Current expiration on a versioning-suspended bucket writes the null delete marker, as S3 does: a null current version is replaced and so removed, and a non-null current version is kept as noncurrent. | radosgw deletes the current version permanently there, and an older version resurfaces (defect LL-2). The marker is the one radosgw's own DeleteObject writes on such a bucket, so the RADOS state stays compatible. AWS's table "Lifecycle actions and bucket versioning state" (S3 User Guide, "Lifecycle configuration elements") states the S3 rule. |
| C-D3 | Every permanent removal the worker makes runs the lock predicate noncurrent expiration has: a retention date after now, a legal hold ON, or a lock attr that does not decode keeps the object. A delete marker over a locked version is allowed. | radosgw's current expiration never checks the lock, so a permanent removal there deletes a retained object (defect LL-3). |
| C-D4 (D14) | A bucket whose rules name a storage class with a cloud-s3 tier is left unclaimed, logged once per pass. rgw-go never moves the shard's head past an entry it leaves unclaimed. From there it walks the shard with a private marker, claiming as usual and writing no head: to the shard's end, then from the shard's first entry back to the head's entry, so every claimable entry is reached on every pass (review F2). | A claim, or a head moved past the entry, would hide the bucket from radosgw's worker for the day, and cloud transitions would stall on a mixed fleet. Leaving the head in place lets radosgw's worker reach it. radosgw's own walk wraps only when its head reaches the shard's end ([rgw_lc.cc:2252-2465](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2252-L2465)), so the private walk must wrap itself, or the entries sorted before a refused one would never be processed again in an rgw-go-only zone. An rgw-go-only zone never processes the refused bucket itself, and the exclusion says so. |
| C-D5 (P1, the owner, 2026-10-13) | The time-driven worker s3-tests (markers `lifecycle_expiration` and `lifecycle_transition`, about 1,100 s of sleeps) run at the phase gate and on demand. The configuration and header tests join the per-PR set. | The per-PR set runs in about 130 s (spec §9). This amends spec §9's per-PR rule. |
| C-D6 | The worker uses v20.2.4's page-boundary rule on both releases: a current delete marker at the end of a truncated page is kept. | v19.2.6 removes it, and an older version becomes current again (defect LL-1). |
| C-D7 | `x-amz-expiration` takes the fixed form on both releases: the current Expiration rule only, and no header for a request that names a version. | radosgw reports a NoncurrentVersionExpiration rule for a current object (defect LL-6). |
| C-D8 | PutBucketLifecycleConfiguration on an indexless bucket is 405 MethodNotAllowed on both releases. | v19.2.6 stores a configuration the worker cannot list (defect LL-7). |
| C-D9 | The lifecycle attr's write and erase go through `op.RetryRacedBucketWrite`, re-authorized on each try. | radosgw makes one objv-guarded attempt, so a racing bucket write fails the request (defect LL-9). |
| C-D10 | A Content-MD5 whose decoded length is not 16 is BadDigest. | radosgw compares 16 bytes whatever the length (defect LL-10). The answer is BadDigest either way, so there is no exclusion. |
| C-D11 | "Today", the work window and an object's age are in the gateway's local time zone, `time.Local`, as radosgw's `localtime_r` and `round_to_day` make them. The location is a parameter, so specs run in a non-UTC zone without touching `time.Local`. | radosgw's behaviour ([utime.h:218-227](https://github.com/ceph/ceph/blob/v19.2.7/src/include/utime.h#L218-L227)). Gateways in different zones each run a bucket once per their own day: duplicate passes, not corruption. The exclusion states the deployment obligation; under Rook every daemon runs with TZ unset. |
| C-D12 | The shard lock is held only around shard bookkeeping, never across a bucket's processing. | A longer hold would starve radosgw's PutBucketLifecycle, whose `guard_lc_modify` gives up after 500 tries 100 ms apart. |
| C-D13 | Before each action the worker re-reads the target's state, and acts only when it is still the version it listed (instance, mtime and ETag). It runs the lock predicate on that state, and removes with the listed entry's conditions (`UnmodifiedSince`, `IfMatch`, `IfMatchLastModified`), plus V8a's `Expect` = the re-read state on a version's removal. On the unversioned path (C10a) the gap phase 1's delete would leave, an attr-only write between the re-read and the removal, is closed: the removal's write op asserts the head's RADOS version as the re-read returned it (librados assert_version, through `DeleteParams.AssertEpoch`), so any write since fails the removal, and the object is re-evaluated on the next pass. On the versioned path V8c's `Expect` refuses a removal of a version written since the re-read, and re-links it, so the version stays listed as before and the next pass decides it afresh. The one window V8c leaves, another reader replaying the unlink's log entry first, is radosgw's own behaviour (L-D11). | radosgw's marker write ignores `unmod_since` for a new marker, and its version removal takes no condition, so a stale pass can hide a newer version or remove a rewritten null version (defects LL-11, LL-12). The re-read and the conditions keep each action to the object it was decided for (Review Focus 3). |
| C-D14 | The per-entry workpool is goroutines bounded by `rgw_lc_max_wp_worker`; every entry of a rule finishes before the next rule starts. | radosgw's WorkPool drains at the rule's end ([rgw_lc.cc:734-871](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L734-L871), [rgw_lc.cc:755-892](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L755-L892)). |
| C-D15 | The worker calls a no-op `lifecycle.Notifier` where radosgw publishes a notification. | Phase 3 fills it. |
| C-D16 (controller) | Until C10b lands, the worker claims only a bucket whose every rule it applies, and leaves the rest unclaimed under C-D4's head rule, whose wrapping walk still reaches every other bucket of the shard on every pass. Before C10b that is a bucket that was never versioned; before C11, also one whose rules have no transition. Each task that serves an action widens the claim predicate in the same PR. | Lifecycle ships early (the owner) without a claimed bucket half processed: a claim marks the bucket done for the day, so radosgw's worker would skip the rules rgw-go cannot yet apply. |

## Seams

**Consumed from other units** (versioning's ids are V-versioning.md's):
- **S1:** the empty method-table file `internal/testutil/fakerados/cls_rgw_lc.go`.
- **S2:** the lifecycle route-set file it stubs, and the `op.Env.Lifecycle` field, of the
  `op.LifecycleStore` interface S2 declares with C5's signature.
- **L1 (c):** `objectlock.VerifyDelete` with both bypass flags false, the lock predicate;
  `meta.BucketInfo.ObjectLock`.
- **L5a:** the default retention, which C11's rewrite receives as a create write.
- **V2:** `meta.BucketInfo.VersioningEnabled()` and `Versioned()`.
- **V6:** `StatObject` follows the OLH; after a follow `ObjectState.Key` is the resolved
  version's key (`Key.Instance` the version id), with `ObjectState.DeleteMarker`; a named
  instance reads that version.
- **V7a:** the head write's OLH epoch: 0 lets the class assign it, a nonzero one links at that
  epoch, non-current when older (C11).
- **V8a:** `DeleteObject(ctx, rec *BucketRecord, key meta.ObjKey, p DeleteParams) (*DeleteResult, error)`
  with `DeleteParams.MarkerOwner`, `Expect` and `SkipOLHUpdate`, beside phase 1's
  `UnmodifiedSince`, `IfMatch` and `IfMatchLastModified`. On the marker path the conditions are
  checked against the current version and go into `setOLH`'s condition, atomic under D1; on the
  instance path they are checked against the entry's meta, and `Expect` guards the unlink and
  the head's removal (C10b).
- **V9:** `ListObjectsParams.MarkerInstance`, versions on the unordered listing, and
  `ObjectEntry.VersionedEpoch` and `ObjectEntry.Category` beside `IsLatest`, `DeleteMarker`,
  `Mtime` and `Key.Instance` (C10b, C11).
- **V15a:** lifts the `versioning` marker, which 12 lifecycle tests and one lock test also
  carry (C12).
- **WP-11:** `copyData`'s `raw bool` parameter (`internal/driver/copy.go:431`), true for the
  transition.
- **WP-22 and the T unit:** the s3-tests selection in Go, the include list, and the lifecycle
  harness task that carries C12's former harness parts.
- **Phase 1, for C10a:** the unordered listing without versions, `DeleteObject` on a bucket
  that was never versioned, with `UnmodifiedSince`, `IfMatch` and `IfMatchLastModified`
  (`internal/op/object.go:89-115`), and `MultipartStore.GetUpload` and `Abort`
  (`internal/op/multipart.go:196-207`). Also the version assert on a write op,
  `func (o *WriteOp) AssertVersion(ver uint64)` (`internal/radosclient/ops.go:126`), which
  goceph translates to go-ceph's `WriteOp.AssertVersion`
  (`internal/radosclient/goceph/translate.go:356-357`) and fakerados emulates as ASSERT_VER,
  ERANGE when the object is newer and EOVERFLOW when older
  (`internal/testutil/fakerados/steps.go:521-537`). The version to assert is
  `ObjectState.Epoch`, the version the head read returns (`internal/driver/stat.go:71`). No
  go-ceph change is needed.
- **Existing:** `meta.StrHashLinux` (`internal/meta/bucket_layout.go:450`), `lcMaxObjs` and
  `lcHashPrime` (`internal/driver/options.go:17,35,99`), `ZoneParams.LCPool`
  (`internal/meta/zone_params.go:22,150`), `Pool.LockExclusive` and `Unlock`
  (`internal/radosclient/cluster.go:112-119`), `Store.AddWorker`
  (`internal/driver/run.go:21`), `ZoneInfo.Placement` (`internal/op/zone.go:39`),
  `op.ReadParamBody`, `op.RetryRacedBucketWrite`, `op.AttrLC`, `xmltext.Parse`.

**Produced:**
- Package `internal/lifecycle` (pure): the encodings, `Parse(body []byte, maxRules int)` and
  `Marshal`, the prefix map, `Expired`, the per-entry decision, `ExpirationHeader` and
  `AbortHeader`, and `type Notifier interface`.
- Package `internal/cls/rgw` (`ops_lc.go`, `types_lc.go`): `LCEntry`, `LCHead`, and the seven
  calls over `radosclient.Execer`, such as `LCSetEntry(op radosclient.Execer, e LCEntry)` and
  `LCGetEntry(op *radosclient.ReadOp, marker string) *LCEntryResult`.
- `op.LifecycleStore`, in `internal/op/lifecyclestore.go`:
  `SetEntry(ctx context.Context, rec *BucketRecord) error` and
  `RemoveEntry(ctx context.Context, rec *BucketRecord) error`, each under `guard_lc_modify`.
- `op.DeleteParams.AssertEpoch uint64` (C10a): when nonzero, the unversioned removal's write
  op asserts the head is at that RADOS version.
- Driver-internal, for C10a, C10b and C11: the bucket-pass interface C9 calls, with its claim
  predicate (C-D4, C-D16), and C9's claim re-read.

## Hot files

| File | Tasks | Owner and rule |
|---|---|---|
| `internal/testutil/fakerados/cls_rgw_lc.go` | S1, C4 | S1 creates it empty; C4 fills it |
| `internal/cls/rgw/const.go` | none here | not edited: the method names live in `ops_lc.go` |
| `internal/op/bucketsubres.go` (holds `GetBucketLifecycle`, `AttrLC`) | BC tasks, C6 (b) | C6 (b) moves the lifecycle op out and edits nothing else |
| `internal/s3/bucket.go` (the `get_lifecycle` binding) | S2, BC tasks, C6 (b) | C6 (b) removes that one binding when its route set takes it |
| `internal/driver/options.go` | C5, writepath | line-local additions |
| `internal/driver/store.go` (`Env`) | C5 | one binding line |
| `internal/driver/bucket.go`, `internal/driver/metadata_instance.go` | C7, V8b | C7's additions are line-local calls |
| `internal/driver/lc_bucket.go`, `lc_actions.go` (the claim predicate) | C10a, C10b, C11 | C10a creates them; C10b and C11 each widen the predicate in their own PR |
| `internal/op/object.go` (`DeleteParams`), `internal/driver/delete.go`, `internal/memstore/object.go` | V6, V8a; C10a | C10a adds one line-local field and its use in the unversioned path; V8a changes the signature around it. Whichever lands first, the later rebases (index Order, hot-file rule) |
| `internal/s3/getobject.go`, `putobject.go`, `multipart.go` | V, WP-7, WP-13, L5b, C8 | each task adds its headers in its own helper, one call line per site; whichever lands first, the later rebases |
| `internal/driver/run.go` or the worker setup | C9 | one `AddWorker` call |
| the s3-tests selection, `hack/s3tests/deselect-phase2.txt`, `hack/admin/deselect-phase2.txt`, the ledgers | WP-22, every area's recording task | each area edits only its own lines; the controller lands recordings one at a time |
| `docs/exclusions.md`, `docs/ceph-upstream-bugs.md` | every task | each PR edits only its own entries |

## Tasks

### Task C1: lifecycle types, encodings and XML

- **Layer:** pure library (`internal/lifecycle`). **Cluster:** no.
- **Files:** create `internal/lifecycle/{doc.go,types.go,encode.go,xml.go,prefixmap.go}` and
  the suite.
- **Consumes:** `internal/tags`' RGWObjTags encoding, `xmltext.Parse`, `denc`. **Produces:**
  the configuration types, their encoders and decoders, `Parse(body []byte, maxRules int)`,
  `Marshal`, and the prefix map.
- **Behaviour:**
  1. **Encodings,** each at the same lines at both tags, decoders taking every version the C++
     accepts:
     - `LCExpiration` v4, compat 2, legacy compat length: days, date and newer_noncurrent,
       all strings, date from v3 and newer_noncurrent from v4
       ([rgw_lc.h:41-104](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.h#L41-L104), [rgw_lc.h:41-104](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.h#L41-L104));
     - `LCTransition` v1: days, date and storage_class, all strings
       ([rgw_lc.h:107-169](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.h#L107-L169), [rgw_lc.h:107-169](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.h#L107-L169));
     - `LCFilter` v4, compat 1: prefix, RGWObjTags, flags u32 (ArchiveZone is bit 0), size_gt
       and size_lt as strings. The decoder is `DECODE_START(3)` at v19.2.6 and `(4)` at
       v20.2.4; with compat 1 both read v4, so v4 is written on both
       ([rgw_lc.h:187-313](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.h#L187-L313), [rgw_lc.h:187-313](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.h#L187-L313));
     - `LCRule` v6, legacy compat: id, prefix, status, expiration, noncur_expiration,
       mp_expiration, dm_expiration, filter, transitions and noncur_transitions, each map keyed
       by storage class ([rgw_lc.h:316-464](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.h#L316-L464), [rgw_lc.h:316-464](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.h#L316-L464));
     - `RGWLifecycleConfiguration` v1: the multimap of id to rule, in key order
       ([rgw_lc.h:510-562](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.h#L510-L562),
       [rgw_lc.h:510-562](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.h#L510-L562)).
  2. **Grammar** ([rgw_lc_s3.cc:19-308](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc_s3.cc#L19-L308), the same blob as
     [rgw_lc_s3.cc:19-308](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc_s3.cc#L19-L308)); every error is the package's malformed-XML error:
     - the root `LifecycleConfiguration` is not mandatory, so a wrong root is an empty
       configuration; the `Rule` array is mandatory;
     - a rule needs `Status` Enabled or Disabled, and a `Filter` or else a top-level `Prefix`,
       and at least one action; an empty `ID` gets 48 random lowercase alphanumerics;
     - `Expiration` takes exactly one of `Days`, `Date` and `ExpiredObjectDeleteMarker`; a
       `Date` is ISO 8601 at exact midnight UTC ([rgw_lc_s3.cc:19-30](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc_s3.cc#L19-L30));
     - `NoncurrentDays` and `DaysAfterInitiation` are mandatory; a `Transition` takes exactly
       one of `Days` and `Date`, and a `StorageClass`; a repeated storage class is an error;
     - `ObjectSizeLessThan` at or below `ObjectSizeGreaterThan` is an error;
     - more rules than `maxRules` (`rgw_lc_max_rules`) is an error;
     - integers are read as `decode_xml_obj` reads them ([rgw_xml.cc:282-377](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_xml.cc#L282-L377),
       [rgw_xml.cc:282-377](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_xml.cc#L282-L377)).
  3. **Validation,** `rebuild` and `check_and_add_rule`
     ([rgw_lc_s3.cc:346-360](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc_s3.cc#L346-L360);
     [rgw_lc.cc:173-192](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L173-L192),
     [rgw_lc.cc:173-192](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L173-L192)), in
     rule order:
     - a rule `LCRule::valid` refuses is the invalid-argument error: an ID over 255, no
       action, an expiration with both or Days of 0 or less, a negative transition day, or
       Days and Date mixed across the expiration and the current transitions
       ([rgw_lc.cc:60-95](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L60-L95), [rgw_lc.cc:60-95](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L60-L95));
     - a duplicate ID is the invalid-argument error;
     - tags with `ExpiredObjectDeleteMarker` or `AbortIncompleteMultipartUpload` is the
       invalid-request error.
  4. **Dump** ([rgw_lc_s3.cc:310-344](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc_s3.cc#L310-L344),
     [rgw_lc_s3.cc:310-344](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc_s3.cc#L310-L344)): rules in rule-map order;
     each with ID, then `Filter` when not empty or else `Prefix`, Status, Expiration,
     NoncurrentVersionExpiration, AbortIncompleteMultipartUpload, the Transitions and the
     NoncurrentVersionTransitions; `And` when the filter has more than one condition.
  5. **Prefix map** ([rgw_lc.cc:114-171](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L114-L171),
     [rgw_lc.cc:114-171](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L114-L171)): one op per
     rule, holding the expiration days or date, the noncurrent days and newer count, the abort
     days, the delete-marker flag, the transitions keyed by canonical storage class, the
     filter's prefix or else the rule's, the tags, the sizes and the flags; ordered by prefix.
- **Specs:**
  - corpus goldens for `RGWLifecycleConfiguration`, `LCRule`, `LCExpiration` and `LCFilter`
    (from 15.0.0) in the ceph checkout's `ceph-object-corpus`; generated vectors for
    `LCTransition`, which the corpus lacks; decoding an `LCExpiration` v2 and v3;
  - grammar and validation tables, error by error in radosgw's order; a wrong root; 1001
    rules under the default limit; a 48-character ID from the right alphabet;
  - dump tables byte for byte, with Filter or Prefix and with And;
  - the prefix map's order and its canonical class (`""` is STANDARD);
  - Rook's `bucket/lifecycle` documents (Prefix `""` outside Filter, Abort, Expiration Days)
    parsed and dumped as Rook's comparison expects.
- **Exclusions and registry:** none.
- **Depends on:** nothing. **PR split:** (a) the encodings; (b) the XML and validation.
  **Brief:** about 230 lines.

### Task C2: lifecycle evaluation

- **Layer:** pure library (`internal/lifecycle`). **Cluster:** no.
- **Files:** create `internal/lifecycle/{expire.go,decide.go,headers.go}` and their specs.
- **Consumes:** C1; L1 (c)'s `objectlock.VerifyDelete`. **Produces:**
  `Expired(mtime time.Time, days int, now time.Time, debug time.Duration, loc *time.Location) bool`;
  the per-entry decision, `Decide`, over a listed entry and the lister's state, with the
  size, tags and lock predicate injected; `ExpirationHeader` and `AbortHeader`.
- **Behaviour:**
  1. **Age** ([rgw_lc.cc:272-302](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L272-L302),
     [rgw_lc.cc:272-302](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L272-L302)): due when local midnight of now
     minus the mtime's seconds is at least days × 86400 s (`round_to_day`,
     [utime.h:218-227](https://github.com/ceph/ceph/blob/v19.2.7/src/include/utime.h#L218-L227), [utime.h:218-227](https://github.com/ceph/ceph/blob/v20.2.4/src/include/utime.h#L218-L227);
     C-D11). With a positive debug interval, due when now minus the mtime is at least days ×
     the interval. A date is due once now reaches it.
  2. **The rule's actions,** built in `LCOpRule::build`'s order
     ([rgw_lc.cc:1582-1608](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L1582-L1608), [rgw_lc.cc:1638-1664](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L1638-L1664)):
     current expiration when Days is positive or a Date is set, delete-marker expiration,
     noncurrent expiration, a current transition per class, a noncurrent transition per class.
     A rule that is disabled, has no object action, or fails the ArchiveZone check is skipped
     ([rgw_lc.cc:1009-1028](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L1009-L1028), [rgw_lc.cc:1029-1048](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L1029-L1048)).
  3. **Selection** ([rgw_lc.cc:1617-1677](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L1617-L1677),
     [rgw_lc.cc:1673-1733](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L1673-L1733)): of the actions whose check
     holds, the one whose due time is strictly latest, so an earlier action wins a tie; a
     transition to the entry's own canonical class is dropped; then the tag filter, which
     reads the object's tags (none is a skip, an undecodable set an error) and which a delete
     marker passes ([rgw_lc.cc:1030-1114](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L1030-L1114), [rgw_lc.cc:1050-1134](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L1050-L1134)).
  4. **The lister's state** ([rgw_lc.cc:1610-1615](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L1610-L1615),
     [rgw_lc.cc:1666-1671](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L1666-L1671)): the next entry's name, by
     v20.2.4's rule on both releases, so the last entry of a truncated page sees its own name
     ([rgw_lc.cc:434-451](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L434-L451); C-D6); the count of earlier entries
     of the same name; the previous entry's mtime, the effective mtime of a noncurrent entry;
     an empty-instance noncurrent entry named `null`
     ([rgw_lc.cc:467-513](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L467-L513), [rgw_lc.cc:475-521](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L475-L521)).
  5. **The checks.** The size filter is size > gt and size < lt from the head stat
     ([rgw_lc.cc:516-545](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L516-L545),
     [rgw_lc.cc:524-552](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L524-L552)). Then:
     - **Current expiration** ([rgw_lc.cc:1116-1207](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L1116-L1207),
       [rgw_lc.cc:1136-1227](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L1136-L1227)): current entries
       only; a current delete marker followed by an entry of its name is skipped, otherwise
       due like an object and removed. Otherwise the decision follows the bucket: Enabled
       writes a marker; Suspended writes the null marker (C-D2), which removes a null current
       version; unversioned removes. Each removal goes through the lock predicate (C-D3).
     - **Delete-marker expiration** ([rgw_lc.cc:1263-1308](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L1263-L1308),
       [rgw_lc.cc:1284-1330](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L1284-L1330)): a delete marker with
       no following entry of its name, due at once; removed.
     - **Noncurrent expiration** ([rgw_lc.cc:1209-1261](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L1209-L1261),
       [rgw_lc.cc:1229-1282](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L1229-L1282)): due by the
       effective mtime; more earlier entries of the name than NewerNoncurrentVersions; the
       lock predicate ([rgw_lc.cc:304-349](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L304-L349),
       [rgw_lc.cc:304-349](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L304-L349)); the version removed.
     - **Transition** ([rgw_lc.cc:1310-1580](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L1310-L1580), [rgw_lc.cc:1332-1636](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L1332-L1636)):
       never a delete marker; current or noncurrent as the action says, a noncurrent one due
       by the effective mtime; the target class.
  6. **`ExpirationHeader`** in C-D7's form, which is v19.2.7's
     ([rgw_lc.cc:2750-2888](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2750-L2888); v20.2.4's still takes the
     noncurrent rule, [rgw_lc.cc:2817-2952](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2817-L2952), so the
     exclusion is Tentacle's only, P5): over enabled rules matching
     the key's prefix and the object's tags, the current Expiration only; Days count from
     midnight UTC after the mtime; the earliest wins;
     `expiry-date="<RFC 1123 in GMT>", rule-id="<id>"`; nothing when the request names a
     version.
  7. **`AbortHeader`** ([rgw_lc.cc:2890-2954](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2890-L2954),
     [rgw_lc.cc:2954-3018](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2954-L3018)): enabled rules matching
     the prefix only, with DaysAfterInitiation, by the same midnight rule; the earliest date and
     its rule ID.
- **Specs** (table-driven over entry sequences):
  - a current delete marker at the end of a truncated page, older versions on the next: kept
    under both releases' `Semantics`;
  - a suspended bucket with a null current version and with a non-null one;
  - a lock bucket whose flags lack `BUCKET_VERSIONED`: a retained current object is kept;
  - NewerNoncurrentVersions 0, 1 and 2; size and tag filters; a tie of due times; a
    same-class transition dropped;
  - `Expired` around local midnight in a non-UTC location, and in debug mode;
  - the header around midnight UTC; a noncurrent-only rule gives no header for a current
    object; a versioned request gives none; the abort header ignores tags.
- **Exclusions and registry:** the exclusions for C-D2, C-D3, C-D6 and C-D7, and the registry
  entries for defects LL-1 (prior art upstream, nothing to file), LL-2, LL-3 and LL-6
  (fixed in v19.2.7, so its exclusion is Tentacle's only), each unreproduced; reproductions in C13 (c).
- **Depends on:** C1, L1 (c). **PR:** one. **Brief:** about 230 lines.

### Task C3: the cls_rgw lc client

- **Layer:** class client (`internal/cls/rgw`). **Cluster:** no.
- **Files:** create `internal/cls/rgw/ops_lc.go`, `types_lc.go` and their specs, with the
  method names as constants in `ops_lc.go`.
- **Produces:** `LCEntry{Bucket string; StartTime uint64; Status uint32}`,
  `LCHead{StartDate uint64; Marker string; ShardRolloverDate uint64}`, the status constants,
  and the seven calls (Seams).
- **Behaviour:**
  1. The methods `lc_get_entry`, `lc_set_entry`, `lc_rm_entry`, `lc_get_next_entry`,
     `lc_put_head`, `lc_get_head` and `lc_list_entries`
     ([cls_rgw_const.h:59-65](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw_const.h#L59-L65), [cls_rgw_const.h:62-68](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw_const.h#L62-L68)).
  2. `cls_rgw_lc_entry` v1 ([cls_rgw_types.h:1301-1333](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw_types.h#L1301-L1333),
     [cls_rgw_types.h:1338-1370](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw_types.h#L1338-L1370)), with statuses 0
     uninitial, 1 processing, 2 failed, 3 complete ([rgw_lc.h:34-39](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.h#L34-L39),
     [rgw_lc.h:34-39](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.h#L34-L39)); `cls_rgw_lc_obj_head` v2, compat 2, each time 8 bytes
     ([cls_rgw_types.h:1264-1299](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw_types.h#L1264-L1299),
     [cls_rgw_types.h:1301-1336](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw_types.h#L1301-L1336)).
  3. The op and reply structs ([cls_rgw_ops.h:1183-1456](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw_ops.h#L1183-L1456),
     [cls_rgw_ops.h:1221-1494](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw_ops.h#L1221-L1494)): v2 for every entry carrier, v1 for the
     rest; `list_entries` sent at struct_v 3, its reply a `map<string,int>` at struct_v 2 or
     below and a vector of entries above, then `is_truncated`.
  4. Reads go in a `ReadOp`, writes in a `WriteOp`, one exec each, as the client builds them
     ([cls_rgw_client.cc:943-1067](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw_client.cc#L943-L1067), [cls_rgw_client.cc:755-880](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw_client.cc#L755-L880));
     a list reply's entries come sorted by bucket.
- **Specs:** corpus goldens for `cls_rgw_lc_entry` (from 16.2.0), `cls_rgw_lc_get_entry_ret`,
  `cls_rgw_lc_set_entry_op` and `cls_rgw_lc_rm_entry_op`; generated vectors for the head, the
  get_next_entry, list_entries, put_head and get_head structs; a legacy list reply decoded;
  every call's bytes against a recording `Execer`.
- **Exclusions and registry:** none.
- **Depends on:** nothing. **PR:** one. **Brief:** about 150 lines.

### Task C4: the fakerados lc methods

- **Layer:** emulator. **Cluster:** no.
- **Files:** fill `internal/testutil/fakerados/cls_rgw_lc.go` (S1's table file) and add its
  spec.
- **Consumes:** S1's table type; C3's types. **Produces:** the seven methods in the emulator.
- **Behaviour** ([cls_rgw.cc:4175-4359](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L4175-L4359), [cls_rgw.cc:4577-4761](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L4577-L4761);
  registered at [cls_rgw.cc:4734-4740](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L4734-L4740) and
  [cls_rgw.cc:5153-5159](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L5153-L5159)):
  1. `lc_get_entry` returns the omap value under the marker, ENOENT when absent, and EIO when
     the value does not decode as `cls_rgw_lc_entry`, the legacy pair included:
     `read_omap_entry` reads no pair ([cls_rgw.cc:915-932](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L915-L932),
     [cls_rgw.cc:1045-1062](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1045-L1062)).
  2. `lc_set_entry` sets the omap value under the entry's bucket, creating the object;
     `lc_rm_entry` removes it.
  3. `lc_get_next_entry` returns the first key after the marker, or an empty entry at the end;
     a value that does not decode, the legacy pair included, is EIO
     ([cls_rgw.cc:4255-4265](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L4255-L4265),
     [cls_rgw.cc:4657-4667](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L4657-L4667)).
  4. `lc_list_entries` returns the keys strictly after the marker, at most the maximum,
     truncated while a key remains, so a maximum of 0 answers no entries, truncated
     ([PrimaryLogPG.cc:7702-7763](https://github.com/ceph/ceph/blob/v19.2.7/src/osd/PrimaryLogPG.cc#L7702-L7763),
     [PrimaryLogPG.cc:7779-7840](https://github.com/ceph/ceph/blob/v20.2.4/src/osd/PrimaryLogPG.cc#L7779-L7840));
     each value read as an entry, else as the legacy pair, else EIO; replies at the request's
     struct_v.
  5. `lc_put_head` writes the omap header; `lc_get_head` of an empty header is a zero head,
     and one that does not decode is EINVAL.
  6. A read of a missing shard object is ENOENT. The write flag is set for `lc_set_entry`,
     `lc_rm_entry` and `lc_put_head` only.
- **Specs:** each method through C3's client on the emulator; ENOENT on a missing shard;
  `lc_set_entry` creating it; `lc_get_next_entry` order and end; list paging, truncation, a
  legacy value and a struct_v 2 reply; the write flags in the derived write list; a value
  stored in the legacy pair form: EIO from `lc_get_entry` and `lc_get_next_entry`, and the
  pair's bucket and status with start 0 from `lc_list_entries` (`docs/ceph-upstream-bugs.md`,
  "cls_rgw's lc_get_entry and lc_get_next_entry refuse an lc entry stored in its old pair
  form, which stalls the shard"); a maximum of 0.
- **Exclusions and registry:** none.
- **Depends on:** S1, C3. **PR:** one. **Brief:** about 120 lines.

### Task C5: the lc shard store and the options

- **Layer:** driver. **Cluster:** no.
- **Files:** create `internal/driver/lc_shards.go`, `internal/op/lifecyclestore.go` and their
  specs; modify `internal/driver/options.go` (line-local), `driver.Store.Env` and memstore's
  Env (one binding each).
- **Consumes:** C3, C4; S2's `Env.Lifecycle` field. **Produces:** the implementation of S2's `op.LifecycleStore`; the
  shard naming, key and index; driver-internal head and entry helpers for C9.
- **Behaviour:**
  1. **Shards:** `lc.0` to `lc.<n-1>`, n being phase 1's `lcMaxObjs`, in
     `ZoneParams.LCPool`, by default the log pool in namespace `lc`
     ([rgw_lc.cc:233-254](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L233-L254),
     [rgw_lc.cc:233-254](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L233-L254); [rgw_zone.cc:582](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_zone.cc#L582),
     [rgw_zone.cc:590](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_zone.cc#L590)).
  2. **Key and index:** `tenant:name:marker`, and `StrHashLinux(key) % 7877 % n`
     ([rgw_lc.cc:1966-1992](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L1966-L1992), [rgw_lc.cc:2023-2049](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2023-L2049)).
  3. **`SetEntry` and `RemoveEntry`** are `guard_lc_modify`
     ([rgw_lc.cc:2584-2634](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2584-L2634),
     [rgw_lc.cc:2651-2701](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2651-L2701)):
     - the `lc_process` exclusive lock on the bucket's shard, for `rgw_lc_lock_max_time`,
       under one random 16-character cookie per process;
     - EBUSY and EEXIST retried every 100 ms, up to 500 times, then the error;
     - then the entry {key, 0, uninitial} written, or the key's entry removed; then unlock.
  4. **Options** ([rgw.yaml.in:374-459](https://github.com/ceph/ceph/blob/v19.2.7/src/common/options/rgw.yaml.in#L374-L459),
     [rgw.yaml.in:400-485](https://github.com/ceph/ceph/blob/v20.2.4/src/common/options/rgw.yaml.in#L400-L485);
     [rgw.yaml.in:3886-3901](https://github.com/ceph/ceph/blob/v19.2.7/src/common/options/rgw.yaml.in#L3886-L3901),
     [rgw.yaml.in:4172-4187](https://github.com/ceph/ceph/blob/v20.2.4/src/common/options/rgw.yaml.in#L4172-L4187)), read as phase 1's options are:
     - `rgw_enable_lc_threads` true; `rgw_lc_max_worker` 3; `rgw_lc_max_wp_worker` 3;
     - `rgw_lc_lock_max_time` 90 s; `rgw_lc_thread_delay` 0 ms; `rgw_lc_debug_interval` -1;
     - `rgw_lifecycle_work_time` "00:00-06:00", read as `%d:%d-%d:%d`;
     - `rgw_lc_max_rules` 1000; `rgwlc_auto_session_clear` true; `rgwlc_skip_bucket_step`
       false.
- **Specs**, on fakerados:
  - the shard of known keys, the vectors computed through `meta.StrHashLinux`;
  - `SetEntry` and `RemoveEntry` bytes; EBUSY and EEXIST retried under a fake sleeper, and
    the error after the 500th;
  - every option at its default, set, and set to garbage; the work-time forms.
- **Exclusions and registry:** none.
- **Depends on:** C3, C4, S2. **PR:** one. **Brief:** about 180 lines.

### Task C6: the lifecycle configuration ops

- **Layer:** op and S3 handler. **Cluster:** no.
- **Files:** create `internal/op/lifecycle.go`, `internal/s3/lifecycle.go` and their specs;
  modify the lifecycle route-set file; move `GetBucketLifecycle` out of
  `internal/op/bucketsubres.go:536-576` and its binding out of `internal/s3/bucket.go`.
- **Consumes:** C1, C5; `ReadParamBody`; `RetryRacedBucketWrite`; phase 1's Content-MD5
  decoder. **Produces:** the three ops.
- **Behaviour:**
  1. **PUT** ([rgw_op.cc:5933-6028](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L5933-L6028),
     [rgw_op.cc:6592-6695](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L6592-L6695)), after
     s3:PutLifecycleConfiguration ([rgw_op.cc:5762-5802](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L5762-L5802),
     [rgw_op.cc:6405-6445](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L6405-L6445)), in order:
     1. an indexless bucket is 405 MethodNotAllowed, "Indexless buckets do not support
        lifecycle policy", on both releases (C-D8);
     2. a `Content-MD5` that does not decode, or decodes to other than 16 bytes, is 400
        BadDigest (C-D10);
     3. the body through `ReadParamBody`;
     4. a body whose MD5 differs is BadDigest;
     5. a parse failure or a grammar error is 400 MalformedXML; C1's invalid-argument and
        invalid-request errors are 400 InvalidArgument and 400 InvalidRequest;
     6. radosgw's forward is a no-op here;
     7. `user.rgw.lc` written through `RetryRacedBucketWrite`, re-authorized (C-D9;
        [rgw_lc.cc:2636-2666](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2636-L2666),
        [rgw_lc.cc:2703-2733](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2703-L2733));
     8. `Env.Lifecycle.SetEntry`. When it fails the attr stays, as in radosgw, and the
        request fails with the lock's error.
     - The answer is send_response's status with dump_start
       ([rgw_rest_s3.cc:3728-3735](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3728-L3735),
       [rgw_rest_s3.cc:4179-4186](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4179-L4186)).
  2. **GET:** s3:GetLifecycleConfiguration; no attr is 404 NoSuchLifecycleConfiguration; an
     attr that does not decode is 500; otherwise C1's dump under
     `<LifecycleConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`
     ([rgw_rest_s3.cc:3688-3726](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3688-L3726), [rgw_rest_s3.cc:4139-4177](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4139-L4177)).
     Phase 1's 501 goes.
  3. **DELETE** ([rgw_op.cc:6030-6047](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L6030-L6047),
     [rgw_op.cc:6697-6714](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L6697-L6714);
     [rgw_lc.cc:2668-2693](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2668-L2693),
     [rgw_lc.cc:2735-2760](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2735-L2760)): checked as
     s3:PutLifecycleConfiguration, as radosgw checks it; the attr erased when present,
     through `RetryRacedBucketWrite`; then `RemoveEntry`; 204 whether or not there was one
     ([rgw_rest_s3.cc:3737-3747](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3737-L3747),
     [rgw_rest_s3.cc:4188-4198](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4188-L4198)).
- **Specs:**
  - on counterfeiter fakes, each error in order, the 405 under both releases' `Semantics`;
  - Content-MD5 absent, right, wrong, decoding short, and all `=` (registry, "radosgw's
    from_base64 reads before an all-'=' input");
  - a wrong root stores an empty configuration, which GET renders;
  - the attr written before the entry, and kept when the entry's lock fails;
  - a racing bucket write retried, and a permission revoked between attempts refused;
  - DELETE's 204 without a configuration; a body over `rgw_max_put_param_size`;
  - Rook's `bucket/lifecycle` sequence: put, read back, update, delete.
- **Exclusions and registry:** retires "A stored lifecycle configuration answers 501 until
  phase 2". The exclusions for C-D8 and C-D9, and the registry entries for defects LL-7 and
  LL-9, unreproduced (reproductions in C13 (b)); LL-10's entry with no exclusion.
- **Depends on:** C1, C5, S2. **PR split:** (a) PUT and DELETE; (b) GET and the move.
  **Brief:** about 200 lines.

### Task C7: the lc entry's side effects

- **Layer:** driver. **Cluster:** no.
- **Files:** modify `internal/driver/bucket.go` (bucket removal) and
  `internal/driver/metadata_instance.go`, which holds the bucket instance section since WP-18
  (the instance put, `putInstance`: its doc and debug log,
  `internal/driver/metadata_instance.go:181-182,294`).
- **Consumes:** C5's `SetEntry` and `RemoveEntry`. **Produces:** no new seam.
- **Behaviour:**
  1. **Bucket removal** drops the entry when the bucket carried `user.rgw.lc`, after the
     multipart abort, ignoring the result ([rgw_sal_rados.cc:402-407](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_sal_rados.cc#L402-L407),
     [rgw_sal_rados.cc:419-424](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L419-L424)).
  2. **A `bucket.instance` metadata put,** after the index init, sets the entry when the new
     attrs carry `user.rgw.lc`, and drops it when only the old instance's did. That is
     v20.2.4's condition, used on both releases: v19.2.6 also drops it when neither did,
     which removes only an entry the old record never implied
     ([rgw_bucket.cc:2900-2928](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_bucket.cc#L2900-L2928),
     [rgw_bucket.cc:3010-3040](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_bucket.cc#L3010-L3040)).
- **Specs** on fakerados: a removed bucket with a configuration leaves no entry, one without
  makes no lc call, and a failed removal of the entry does not fail the bucket removal; an
  instance put with the attr on neither, old, new and both, against the entry; the topic half
  of the exclusion untouched.
- **Exclusions and registry:** retires the lifecycle half of "The instance's lifecycle
  configuration and topic mappings ... are not kept by rgw-go yet".
- **Depends on:** C5. **PR:** one. **Brief:** about 100 lines.

### Task C8: the lifecycle response headers

- **Layer:** S3 handler. **Cluster:** no.
- **Files:** create `internal/s3/lifecycleheaders.go` and its spec; one call line each in
  `internal/s3/getobject.go`, `putobject.go` and `multipart.go`.
- **Consumes:** C2's `ExpirationHeader` and `AbortHeader`; C1's decoder.
- **Behaviour:**
  1. `x-amz-expiration` on GET, HEAD and PutObject, from the bucket's configuration, the key,
     the object's stored tags as `rgw_cond_decode_objtags` takes them
     ([rgw_op.cc:2198-2212](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L2198-L2212), [rgw_op.cc:2433-2447](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L2433-L2447)) and its
     mtime; none when the request names a version, or the configuration does not decode
     (C-D7). The call sites are radosgw's
     ([rgw_rest_s3.cc:397](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L397), [rgw_rest_s3.cc:470](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L470),
     [rgw_rest_s3.cc:2750](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2750); [rgw_rest_s3.cc:400](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L400),
     [rgw_rest_s3.cc:498](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L498), [rgw_rest_s3.cc:2903](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2903)).
  2. `x-amz-abort-date`, in the time-header format, and `x-amz-abort-rule-id` on
     CreateMultipartUpload ([rgw_rest_s3.cc:4068-4071](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4068-L4071),
     [rgw_rest_s3.cc:4535-4538](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4535-L4538)).
- **Specs:** s3-tests' `test_lifecycle_expiration_header_put`, `_head`, `_tags_head` and
  `_and_tags_head` as unit cases; a versioned GET with no header; a noncurrent-only rule with
  no header on a current object; the abort headers by prefix, tags ignored.
- **Exclusions and registry:** retires "No x-amz-expiration" and "No x-amz-expiration on a
  write"; C-D7's exclusion is C2's.
- **Depends on:** C2, C6; after L5b and writepath's header tasks (hot files). **PR:** one.
  **Brief:** about 140 lines.

### Task C9: the worker, the shard protocol

- **Layer:** driver. **Cluster:** no.
- **Files:** create `internal/driver/lc_worker.go` and its spec; one `AddWorker` call.
- **Consumes:** C5; the bucket pass C10a implements, behind an interface C9 declares, with a
  stub until C10a lands. **Produces:** the bucket-pass interface, which has a claim predicate
  (may this worker claim the bucket under this key) and the pass itself; a claim value
  {shard, entry}; and the claim re-read for C10a and C10b.
- **Behaviour:**
  1. **Startup:** with `rgw_enable_lc_threads`, `rgw_lc_max_worker` workers, worker ix's
     cookie `lc_thrd: <ix>` ([rgw_appmain.cc:231-233](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_appmain.cc#L231-L233),
     [rgw_appmain.cc:233-235](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_appmain.cc#L233-L235); [rgw_lc.cc:2467-2477](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2467-L2477),
     [rgw_lc.cc:2534-2544](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2534-L2544);
     [rgw_lc.h:601-603](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.h#L601-L603), [rgw_lc.h:602-604](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.h#L602-L604)). The root
     context stops them.
  2. **Schedule** ([rgw_lc.cc:201-231](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L201-L231),
     [rgw_lc.cc:201-231](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L201-L231)): when the local work window
     holds, or always in debug mode ([rgw_lc.cc:2510-2539](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2510-L2539),
     [rgw_lc.cc:2577-2606](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2577-L2606)), one pass over every
     shard in a fresh random order ([rgw_lc.cc:1953-2023](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L1953-L2023),
     [rgw_lc.cc:2010-2086](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2010-L2086)); then sleep to the next local window start or one
     debug interval ([rgw_lc.cc:2541-2570](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2541-L2570),
     [rgw_lc.cc:2608-2637](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2608-L2637)).
     v20.2.4's restore expirer after the pass is excluded ([rgw_lc.cc:2079-2083](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2079-L2083)).
  3. **The shard pass** ([rgw_lc.cc:2252-2465](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2252-L2465),
     [rgw_lc.cc:2317-2532](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2317-L2532)), in order:
     1. Lock `lc_process` on `lc.<i>` for `rgw_lc_lock_max_time`, five tries sleeping 0, 100,
        200, 300 and 400 ms ([rgw_lc.cc:1832-1863](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L1832-L1863),
        [rgw_lc.cc:1888-1919](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L1888-L1919)); a shard still held is skipped.
     2. Read the head; a shard that does not exist is skipped.
     3. An empty marker with rollover allowed takes the first entry, sets the marker, stamps
        the start date and clears the rollover date ([rgw_lc.cc:2134-2147](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2134-L2147),
        [rgw_lc.cc:2198-2211](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2198-L2211)); otherwise the marker's entry, where ENOENT
        advances the head and loops ([rgw_lc.cc:2226-2250](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2226-L2250),
        [rgw_lc.cc:2291-2315](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2291-L2315)).
     4. An entry processing in a live session is skipped, and so is one completed today
        ([rgw_lc.cc:2025-2043](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2025-L2043),
        [rgw_lc.cc:2088-2106](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2088-L2106);
        [rgw_lc.cc:2149-2171](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2149-L2171), [rgw_lc.cc:2213-2235](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2213-L2235)).
     5. **The head rule (C-D4, C-D16):** an entry the claim predicate refuses is not claimed,
        and the head is not moved past it. The pass then walks the shard with a private
        marker, skipping as item 4 does and claiming as item 6 does but without its head
        advance, so it never writes the head. It runs from the refused entry to the shard's
        end, then from the shard's first entry back to the head's entry, so every claimable
        entry is reached on every pass. No rollover date is stamped on such a pass, since the
        head never reaches the end.
     6. **The claim:** the entry set processing with start time now, then the head advanced
        to the next entry ([rgw_lc.cc:2173-2201](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2173-L2201),
        [rgw_lc.cc:2237-2266](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2237-L2266)). rgw-go re-reads the entry and finds its own
        claim before unlocking (C-D1).
     7. Unlock, then the bucket pass with `thread_stop_at`, now plus the interval
        ([rgw_lc.cc:2045-2051](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2045-L2051),
        [rgw_lc.cc:2108-2114](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2108-L2114); C-D12).
     8. Lock again with the backoff; a failure ends the pass, leaving the entry processing.
        rgw-go re-reads the entry and writes no post-state over a claim not its own.
     9. Post-state: ENOENT removes the entry, an error marks it failed, success marks it
        complete.
     10. An empty marker stamps the rollover date and ends the pass
         ([rgw_lc.cc:2203-2224](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2203-L2224), [rgw_lc.cc:2268-2289](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2268-L2289));
         otherwise it loops.
     11. **A pair-form entry** (`docs/ceph-upstream-bugs.md`, "cls_rgw's lc_get_entry and
         lc_get_next_entry refuse an lc entry stored in its old pair form, which stalls the
         shard"): rgw-go rewrites the entry rather than stalling the shard as radosgw does
         (spec §8). Where `lc_get_entry` or `lc_get_next_entry` answers EIO (a
         `*radosclient.Error`, Errno EIO), the pass reads the entry as `lc_list_entries` does,
         entry else pair: the next entry through `lc_list_entries` from the current key with a
         maximum of 1, and the head's own entry through an omap read of its key. It rewrites
         the entry with `lc_set_entry` as {bucket, 0, status} under the shard lock, and goes
         on. A value that is neither ends this shard's pass with the head unmoved, and the pass
         moves to the next shard. The rewrite is the form radosgw itself stores from v15.2.5,
         so it stays RADOS-compatible and radosgw's worker reads the shard afterwards.
     12. Every `lc_list_entries` call carries a maximum of at least 1, and a page loop also
         stops on an empty page: a maximum of 0 answers no entries with is_truncated true while
         a key remains.
- **Specs**, on fakerados with a fake clock and the injected location:
  - the shard order random per pass; a held shard skipped after the five tries;
  - the claim, then the head, under the lock, and the unlock before the bucket pass (a
    second client locks the shard during it);
  - each post-state; rollover only after 24 h or in debug mode; already-run-today in a
    non-UTC location; an expired session reclaimed, and not with auto-clear off;
  - the work window, the next start and the debug interval in a non-UTC location;
  - two workers on two RADOS clients over many buckets and shards: every bucket processed
    once per day;
  - the head rule, with a stub predicate refusing one bucket:
    - **a refused entry in the middle of a shard** holding A < E < Z, E refused, over three
      debug days: each day A and Z are each processed once, E is never claimed, and the head
      stays at E with no head write after the first day;
    - **an rgw-go-only zone:** many buckets over many shards, one refused entry on several
      shards. Every other bucket is processed on every pass, once per pass, for several
      passes;
    - the mixed case: a second client running radosgw's protocol unchanged claims E,
      advances the head to the end and wraps, and rgw-go's next pass follows the head;
  - **a pair-form entry:** a shard holding A < P < Z with P stored as a pair: one pass
    processes A, P and Z, P ends in the entry form, and a radosgw-protocol client's next pass
    does not stall; a head left at a pair key: the pass rewrites it and goes on;
  - **Review Focus 3, a worker that dies holding a claim:** worker A claims a bucket and is
    cancelled before its post-state. Worker B skips the bucket until A's session expires,
    then claims and processes it, and it ends complete under B's start time. A worker
    restarted before the expiry does not process it.
  - **Review Focus 3, a stalled worker:** A stalls past its session and B reclaims. When A
    resumes, its claim re-read fails, so it stops and writes no post-state.
- **Exclusions and registry:** C-D11's exclusion (the deployment's time zone); the pair-form
  rewrite's exclusion in `docs/exclusions.md`, and the registry entry's rgw-go line updated to
  it. The reproduced-alike behaviours stay without entries: an entry left processing for two
  days after a failed re-lock, and the local day boundary.
- **Depends on:** C5. **PR:** one. **Brief:** about 250 lines.

### Task C10a: the worker on unversioned buckets, and the multipart abort

- **Layer:** driver. **Cluster:** no.
- **Files:** create `internal/driver/lc_bucket.go`, `lc_actions.go` and their specs; modify
  `internal/op/object.go` (one `DeleteParams` field), `internal/driver/delete.go` (its use in
  the unversioned removal) and `internal/memstore/object.go` (the same, and the epoch an attr
  write advances).
- **Consumes:** C9's interface and claim re-read; C2's `Decide`; L1 (c)'s lock predicate;
  phase 1's unordered listing, `StatObject`, `DeleteObject`, `MultipartStore.GetUpload` and
  `Abort`; `radosclient.WriteOp.AssertVersion`. **Produces:** the bucket pass and its claim
  predicate; `DeleteParams.AssertEpoch`; the action executor C10b extends; the transition
  interface C11 fills.
- **Behaviour:**
  1. **The bucket** ([rgw_lc.cc:1679-1830](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L1679-L1830),
     [rgw_lc.cc:1735-1886](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L1735-L1886)):
     - `rgwlc_skip_bucket_step` succeeds before anything is read
       ([rgw_lc.cc:1693](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L1693), [rgw_lc.cc:1749](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L1749));
     - the key splits into tenant, name and marker; a marker that is not the bucket's is
       ENOENT;
     - no `user.rgw.lc` is success, and a configuration that does not decode is a failure.
  2. **The claim predicate** refuses, logged once per pass:
     - a bucket whose rules name a class with a cloud-s3 tier in the zonegroup's placement,
       always (C-D4; the tier test is the transition's,
       [rgw_lc.cc:1310-1532](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L1310-L1532),
       [rgw_lc.cc:1332-1588](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L1332-L1588));
     - a bucket that was ever versioned, until C10b;
     - a bucket with a transition rule, until C11 (C-D16).
     It reads one bucket record and its attr, under C9's shard lock.
  3. **Per rule** of the prefix map, in order ([rgw_lc.cc:351-446](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L351-L446),
     [rgw_lc.cc:351-454](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L351-L454)):
     - an unordered listing under the rule's prefix, in pages of 1000, sleeping
       `rgw_lc_thread_delay` between pages;
     - each entry handed to the workpool with the lister's state (C-D14);
     - at each page, the claim re-read, which stops the bucket on a lost claim, and
       `thread_stop_at`, which stops it when due ([rgw_lc.cc:881-884](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L881-L884),
       [rgw_lc.cc:902-905](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L902-L905)).
  4. **Current expiration on an unversioned bucket** removes the object, once C2 decides it
     (C-D13):
     1. Re-read the key with `StatObject`. Skip, not counted as an error, unless it still
        exists with the listed mtime and ETag.
     2. Run the lock predicate on that state (C-D3): a bucket whose flags carry lock without
        `BUCKET_VERSIONED` is this path (LL-3).
     3. Delete as `remove_expired_obj` does ([rgw_lc.cc:601-658](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L601-L658),
        [rgw_lc.cc:608-679](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L608-L679)), with `UnmodifiedSince`,
        `IfMatch` and `IfMatchLastModified` from the listed entry, and `AssertEpoch` = the
        re-read state's `Epoch`; a failed condition, a failed assert, or ENOENT is a skip.
     4. **The version assert** closes the window between the re-read and the removal (C-D13):
        - the driver's unversioned `DeleteObject` puts `AssertVersion(AssertEpoch)` first on
          the write op that carries its conditions, its write-tag guard and `obj_remove`, so
          the OSD checks it in the same op as the removal;
        - a mismatch, ERANGE or EOVERFLOW, means the head was written since the re-read. The
          driver cancels the index prepare, as for any failed removal, and returns
          `op.ErrConcurrentModification`, not `FromRADOS`'s InvalidRange for ERANGE;
        - the worker drops the action for this pass, and the object is decided afresh on the
          next;
        - zero leaves the op unchanged, so S3 deletes are untouched;
        - memstore checks its own epoch, and its `SetObjectAttrs` advances it, as every RADOS
          write advances the user version.
     5. Call the Notifier on success (C-D15).
  5. **Multipart abort,** after the rules ([rgw_lc.cc:886-1000](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L886-L1000),
     [rgw_lc.cc:907-1020](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L907-L1020)):
     - namespace `multipart`, the meta-object filter, unordered and without versions, under
       each enabled rule's prefix that has DaysAfterInitiation;
     - an entry whose meta mtime has expired is aborted through `GetUpload` and `Abort`,
       AbortMultipartUpload's path; NoSuchUpload is logged;
     - tags and size are not filtered. Abort-only rules apply to any bucket the predicate
       admits.
- **Specs**, on fakerados and memstore:
  - an expired object removed; one rewritten after the listing kept;
  - kept: a retained or held object on a lock bucket without `BUCKET_VERSIONED`; one whose
    retention was placed after the listing and before the re-read;
  - **an attr-only write between the re-read and the removal:** a hook writes a retention, a
    legal hold, and then a tag set alone through `SetObjectAttrs` after the re-read. Each time
    the removal fails its version assert, the object survives the pass with its index entry
    and quota intact, and the next pass decides it again (kept while retained or held,
    removed after the tag write);
  - `AssertEpoch` zero on an S3 delete changes no op step (a recording pool);
  - an upload aborted; an upload with a completion record leaves its parts (#80896);
  - pages of 1000 with the delay; the workpool bound; the skip option;
  - a pass whose claim was reclaimed stops at its next page;
  - **the interim claim rule (C-D16):** on two workers on two RADOS clients, one running
    radosgw's protocol unchanged:
    - a versioned bucket and a bucket with a transition rule are left unclaimed by rgw-go and
      claimed by the other worker, whose bucket pass is a recording stub;
    - an rgw-go-only zone leaves them unprocessed, and processes every other bucket of their
      shards on every pass (C9's wrapping walk);
    - a cloud-tier bucket is likewise left (C-D4).
- **Exclusions and registry:**
  - C-D4's exclusion: an rgw-go-only zone never processes a bucket with cloud-tier rules;
  - C-D16's interim exclusion, rewritten by C10b and C11 and retired when C11 lands.
  The unversioned path's window needs none: the version assert closes it.
- **Depends on:** C9, C2, L1 (c). **PR split:** (a) the bucket pass, the predicate and current
  expiration; (b) the multipart abort. **Brief:** about 210 lines.

### Task C10b: the worker on versioned buckets

- **Layer:** driver. **Cluster:** no.
- **Files:** modify `internal/driver/lc_bucket.go` (the listing with versions, and the
  predicate) and `lc_actions.go`, with their specs.
- **Consumes:** C10a; C2's decisions for versioned entries; V6's `StatObject`; V8a's
  `DeleteObject` with `MarkerOwner`, and V8c's `Expect` with its re-link; V9's versioned
  listing.
- **Produces:** the claim predicate widened to versioned buckets.
- **Behaviour:**
  1. **The listing** of a versioned bucket lists versions, unordered, through V9, and feeds C2
     the lister's state from the entries in the order V9 returns them.
  2. **The actions,** each after C-D13's re-read of its target: the current version for a
     marker, the listed instance (`null` for an empty one) for a removal. The delete follows
     `remove_expired_obj` ([rgw_lc.cc:601-658](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L601-L658),
     [rgw_lc.cc:608-679](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L608-L679)), with `MarkerOwner` set to the
     entry's owner:
     - **current expiration, Enabled:** a marker, with no instance and the listed entry's
       `UnmodifiedSince`, `IfMatch` and `IfMatchLastModified`, which V8a checks against the
       current version under D1, closing LL-11;
     - **current expiration, Suspended:** the null marker (C-D2). The marker removes the null
       version wherever it sits, current or not ([cls_rgw.cc:1705-1734](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1705-L1734),
       [cls_rgw.cc:1846-1875](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1846-L1875)), so whenever a null version exists the
       worker re-reads it and runs the lock predicate on it first (C-D3, review F8): a locked
       null version skips the action. The delete passes `Expect` = the re-read null version,
       which V8c checks in the marker's init op;
     - **a current delete marker** with no following entry, by current or delete-marker
       expiration: its instance removed, with the conditions but `IfMatch`;
     - **noncurrent expiration:** the version removed by its instance, with the lock
       predicate, the conditions and `Expect` = the re-read state, so a null version rewritten
       since is refused, closing LL-12.
     - A failed condition, a failed `Expect`, or ENOENT is a skip. After a refused `Expect`,
       V8c has re-linked the version as it was, so the next pass decides it afresh.
  3. **The predicate** admits a bucket that was ever versioned (C-D16), in this PR.
- **Specs**, on fakerados:
  - a marker on an Enabled bucket; the null marker on a Suspended one, replacing a null
    current version and keeping a non-null one noncurrent; a delete marker's removal;
    noncurrent removal under NewerNoncurrentVersions 0, 1 and 2;
  - kept: a held noncurrent version; a retained null version under Suspended;
  - **Review Focus 3, a stale pass:**
    - a PUT after the listing leaves no marker over the newer version (LL-11);
    - a suspended bucket's null version rewritten after the listing is kept (LL-12);
    - a retention placed after the listing keeps the version;
    - an attr-only write between the re-read and the null marker that replaces a null
      current version: the null version survives the pass;
    - **after a refused removal** (a hook between the unlink and the removal, as V8c's specs
      use): a retention written there leaves the noncurrent version with its head, instance
      entry and list entry, at its own epoch, in the next pass's listing. The next pass keeps
      it while it is retained and removes it once the retention lapses;
  - **a retained noncurrent null version** under a non-null current, on a bucket whose flags
    carry lock and suspension (an admin metadata put): no null marker is written, and the null
    version is kept (review F8);
    - two passes over one bucket write one marker per expired key;
  - the interim rule's spec, now claiming versioned buckets.
- **Exclusions and registry:** C-D13's exclusion for the versioned path; the registry entries
  for defects LL-11 and LL-12, data corruption, unreproduced, with prior art searched before
  any filing (reproductions in C13 (c)); C-D16's interim exclusion rewritten.
- **Depends on:** C10a, V6, V8a, V8c, V9. **PR split:** (a) current and delete-marker expiration on
  versioned and suspended buckets; (b) noncurrent expiration. **Brief:** about 200 lines.

### Task C11: in-cluster transition

- **Layer:** driver. **Cluster:** no.
- **Files:** create `internal/driver/lc_transition.go` and its spec.
- **Consumes:** C10a's transition interface and claim predicate; `ZoneInfo.Placement`;
  WP-11's raw `copyData`; V7a's head-write OLH epoch; V9's `ObjectEntry.VersionedEpoch` and
  `Category`; L5a's default retention. **Produces:** the claim predicate widened to buckets
  with transition rules, which retires C-D16's interim exclusion.
- **Behaviour** ([rgw_lc.cc:1310-1532](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L1310-L1532),
  [rgw_lc.cc:1332-1588](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L1332-L1588);
  [rgw_rados.cc:5184-5257](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L5184-L5257),
  [rgw_rados.cc:5417-5490](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L5417-L5490)):
  1. A CloudTiered entry (`ObjectEntry.Category`) is skipped. The target is the bucket's placement with the target
     class; a cloud-s3 tier is never transitioned (C-D4); otherwise `Placement` must accept
     it, or the action fails.
  2. The head, attrs and size are read; an mtime other than the listed one is a skip
     (ECANCELED).
  3. `null` is cleared as an instance; `user.rgw.idtag` and `user.rgw.tail_tag` are dropped;
     the owner comes from the ACL.
  4. The stored bytes are rewritten into the target class through `copyData` with `raw` set
     ([rgw_rados.cc:5066-5153](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L5066-L5153),
     [rgw_rados.cc:5294-5386](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L5294-L5386)):
     - a new 32-character tag, the same mtime and attrs, compression kept and not redone;
     - an encrypted source (`user.rgw.crypt.mode`) without `user.rgw.crypt.part-lengths` first
       gets that attr from its manifest, as both floors do before the rewrite
       ([rgw_rados.cc:5155-5182](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L5155-L5182),
       called at [rgw_rados.cc:5211](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L5211) and
       [rgw_rados.cc:5444](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L5444)): one length per part, a part starting
       at each stripe 0, encoded as a `vector<size_t>`; a manifest that does not decode fails
       the action. Without it the rewritten object cannot be decrypted (b9b65194f64);
     - for a version, the entry's versioned epoch as V7a's head-write epoch. A newer version
       makes it a link below the OLH entry's epoch, which writes it non-current and logs
       nothing, so V5b's `setOLH` removes the writer's own pending attr itself; C11 relies on
       that, and on the current version linking at its own epoch and staying current
       ([rgw_rados.cc:3369-3375](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L3369-L3375),
       [rgw_rados.cc:3433-3439](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L3433-L3439));
     - the head write guarded on the idtag read, so a racing overwrite or attr write fails
       it, which is a skip;
     - old tails to the GC through the atomic write; L5a's default retention when the
       object had none.
  5. The Notifier on success.
  6. The claim predicate admits a bucket with transition rules, in this PR; on a versioned
     bucket that needs C10b's widening too.
- **Specs** on fakerados: the head and manifest in the new class's pools, and the index
  entry's class; an encrypted multipart source keeps its `user.rgw.crypt.part-lengths`
  through the rewrite (b9b65194f64); compression attrs kept and the bytes unchanged; a noncurrent version
  transitioned stays noncurrent, with `olh.info` unchanged and no `user.rgw.olh.pending.*`
  attr left on the OLH head, so an `If-None-Match: *` PUT straight after does not wait; the
  current version transitioned stays current; a racing overwrite and a racing retention write each leave
  the object untouched; a class the placement lacks fails the action; the default retention
  added when absent.
- **Exclusions and registry:** none of its own; cloud transition stays under "Cloud transition
  and restore".
- **Depends on:** C10a, V5b, V7a, V9, WP-11, L5a; versioned transitions also C10b. **PR:**
  one.
  **Brief:** about 190 lines.

### Task C12: the lifecycle and object-lock parity set

- **Layer:** tests (parity ledgers). **Cluster:** yes.
- **Files:** this unit's lines in the s3-tests selection that WP-22 moves into Go, in
  `hack/s3tests/deselect-phase2.txt` and `hack/admin/deselect-phase2.txt`; the s3-tests and
  admin ledgers; `test/s3tests/known-differences-*.txt`; the Rook workflow's patch.
- **Consumes:** WP-22's selection, T-2 (the debug interval,
  LC threads on for the gateway under test with radosgw restarted between modes, two plain
  storage classes, and `storage_classes` in the conf), and C-D5's gate-only class.
- **Behaviour:**
  1. The markers `lifecycle`, `lifecycle_expiration`, `lifecycle_transition` and
     `object_lock` leave the excluded set. `cloud_transition`, `cloud_restore` and
     `target_by_bucket` stay, under "Cloud transition and restore", 21 tests.
  2. The 38 lines under "# object lock" leave `deselect-phase2.txt`, with
     `test_object_lock_put_obj_lock_enable_after_create`, wherever V15a left it.
  3. The worker-marker tests go to the gate-only class (C-D5); the configuration and header
     tests join the per-PR set.
  4. radosgw's outcome for every freed test is recorded on both releases with
     `make s3tests-record-tests`, a time-driven test as unstable where two runs disagree. The
     controller lands each recording alone.
  5. `TestRadosGWTestSuite/TestBucket/enable_bucket_object_lock` leaves the admin deselect
     list, and the admin ledgers are re-recorded.
  6. Rook's `bucket/lifecycle` sub-test leaves the workflow's patch.
  7. A known difference is added only where a recorded test touches an exclusion of L or C,
     each line naming it.
- **Specs:** the ledger checks pass on both releases; the per-PR run's set and time unchanged
  but for the configuration and header tests.
- **Exclusions and registry:** none new; each known difference cites its exclusion.
- **Depends on:** WP-22, T-2, V15a for the 12 lifecycle tests
  and the lock test that also carry `versioning`. **PR split:** (a) object lock, after L2 to
  L5b; (b) the lifecycle configuration and header tests, after C6 and C8; (c) the worker
  tests, after C10a, C10b and C11. **Brief:** about 120 lines.

### Task C13: the cross-gateway and mixed-worker gates

- **Layer:** integration (`//go:build integration`). **Cluster:** yes.
- **Files:** create `test/integration/objectlock_test.go` and `test/integration/lc_test.go`.
- **Consumes:** the disposable rooket cluster per release, with both gateways' LC threads on
  and `rgw_lc_debug_interval` set, a configuration separate from the parity options (T-2).
- **Behaviour:** each case runs one way and then the other, on one zone, on both releases:
  1. a lifecycle configuration with Filter, And of tags, sizes, Noncurrent and Newer, Abort
     and Transition, PUT through one gateway: GET through the other is byte-identical XML,
     `user.rgw.lc` is byte-identical, and `radosgw-admin lc list` shows the entry on the
     same shard; DELETE through the other removes both;
  2. PutObjectLockConfiguration through one and GetObjectLockConfiguration through the other,
     with identical `obj_lock` bytes; a CreateBucket with lock through either reads as
     versioned with lock through the other;
  3. a version locked through one (GOVERNANCE, COMPLIANCE, legal hold) is refused by
     DeleteObject and DeleteObjects through the other; a bypass with the permission
     succeeds; a mode change and a shortening are refused alike;
  4. a default retention set through one and a PUT, copy or completion through the other
     decode to the same {mode, mtime + period}, Years as 365.2425 days;
  5. one gateway's worker over the other's objects: expiration, noncurrent expiration with
     NewerNoncurrentVersions, delete-marker expiration, an upload's abort, and transition,
     read back from the new class's pool with `radosgw-admin bucket check` clean;
  6. **mixed workers:** both on over buckets across shards for several debug days. Each entry
     completes once per day; one marker per expired key; no locked version removed; the head
     cycles;
  7. **a worker crash:** one gateway killed mid-bucket; the other skips the entry until the
     session expires, then reclaims it;
  8. **side effects:** a bucket with lifecycle deleted leaves no entry; an instance put through
     rgw-go's admin API with and without `user.rgw.lc` sets and drops the entry.
  9. **Review Focus 3, both workers across a crash mid-bucket:**
     - The population mixes versioned keys, retained and held versions, and a client
       writing new versions while the passes run.
     - rgw-go is killed mid-bucket once and stalled past its session once (stopped, then
       resumed), and radosgw is then killed mid-bucket.
     - After several debug days:
       - no key has two markers from one expiry;
       - no retained or held version is gone;
       - no version written after rgw-go's listing sits under a marker rgw-go wrote.
     - The same staging against radosgw's stalled worker is the reproduction attempt for
       LL-11 and LL-12. It is recorded, not asserted.
  - Each new defect of this unit and of L is reproduced on the floor it names where a case
    reaches it, and its registry entry updated to reproduced or not.
- **Specs:** the cases above, labelled `integration, rgw-go, phase2, lifecycle` and
  `objectlock`.
- **Exclusions and registry:** the reproduction status of LL-1 to LL-12; a security entry that
  reproduces goes to the private channel (code-conventions `bug-reports.md`).
- **Depends on:** L2 to L5b, C6, C7, C10a, C10b and C11, and T-2.
  **PR split:** (a) the lock cases 2 to 4; (b) the configuration and side-effect cases 1 and
  8; (c) the worker cases 5 to 7 and 9. **Brief:** about 200 lines.

## Order

The owner asked for lifecycle early, so C6, C9 and C10a sit in the earliest waves their
dependencies allow; none of them waits on versioning.

| Wave | Tasks, in parallel | Waits for |
|---|---|---|
| 1 | C1 (a), C3 | nothing |
| 2 | C1 (b), C4 | C1 (a); S1 and C3 for C4 |
| 3 | C2, C5 | C1 and L1 (c) for C2; C3, C4 and S2 for C5 |
| 4 | C6 (a), C6 (b), C7, C9 | C1 and C5 |
| 5 | C10a (a), then C10a (b) | C9, C2, L1 (c) |
| 6 | C10b (a), then (b) | C10a, V6, V8a, V8c and V9 (V's wave 8; V9 is in V's wave 5) |
| 7 | C11; C8 | C10a, V7a, V9, WP-11 and L5a for C11, and C10b for versioned transitions; C2, C6, L5b and WP-13 for C8 (L's wave 6) |
| 8, one at a time | C12 (a), (b), (c); C13 (a), (b), (c) | each PR's own dependencies; T-2 |

- C12 and C13 are this unit's cluster tasks. They run one at a time with every other unit's
  cluster tasks, and C12's recordings land one at a time through the controller. C12 (b) can
  run as soon as C6 and C8 are in.
- From wave 5 an rgw-go-only zone runs lifecycle on unversioned buckets, and a mixed zone
  leaves the rest to radosgw's worker (C-D16).
- The critical path is C3, C4, C5, C9, C10a, then C10b once V8c is in, C11, and C12
  (c) and C13 (c).

## Defects

The research report's defect table, the rows this unit owns, carried with class and status,
and two defects found while drafting (LL-11, LL-12). Every new entry starts unreproduced,
derived from the source at both tags; C13 budgets the reproductions (PP-11).

| Id | Defect | Releases | Class | Status | rgw-go's choice | Task |
|---|---|---|---|---|---|---|
| LL-1 | A current delete marker at the end of a 1000-entry listing page is removed although older versions follow on the next page, and the newest of them becomes current again ([rgw_lc.cc:434-443](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L434-L443)). | v19.2.6; fixed in v20.2.4 (tracker #73539, commit 874d2403f5e), no squid backport | data corruption | unreproduced; prior art upstream, nothing to file | C-D6: v20.2.4's rule on both. Exclusion for Squid. | C2 |
| LL-2 | Current expiration on a versioning-suspended bucket deletes the current version permanently, and an older version may resurface; S3 writes a null delete marker ([rgw_lc.cc:1116-1207](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L1116-L1207), [rgw_lc.cc:1136-1227](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L1136-L1227)). | both | data corruption | unreproduced; filed once reproduced | C-D2: the null marker. Exclusion. | C2 |
| LL-3 | Current expiration never checks object lock, so a permanent removal deletes a retained or held object: on a bucket whose flags carry lock without `BUCKET_VERSIONED`, which a `bucket.instance` metadata put can write, and under LL-2. | both | data corruption | unreproduced | C-D3: the lock predicate on every permanent removal. Exclusion. | C2, C10a, C10b |
| LL-6 | `x-amz-expiration` reports a NoncurrentVersionExpiration rule for a current object, with that rule's date read from the current Expiration ([rgw_lc.cc:2913-2928](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2913-L2928), in [rgw_lc.cc:2817-2952](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2817-L2952); v19.2.7 takes the current Expiration only, [rgw_lc.cc:2850-2864](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2850-L2864)). | v20.2.4 (and v19.2.6); fixed in v19.2.7 (squid 4dac1850993, tracker #77564); tentacle 30daa120099, in no tag | S3-correctness | unreproduced | C-D7: the fixed form, v19.2.7's. Exclusion for Tentacle (P5). | C2 |
| LL-7 | v19.2.6 stores a lifecycle configuration on an indexless bucket, which the worker cannot list; v20.2.4 answers 405. | v19.2.6 | S3-correctness | unreproduced | C-D8: the 405 on both. Exclusion for Squid. | C6 |
| LL-9 | PutBucketLifecycle's attr write is one objv-guarded attempt, so a racing bucket write fails it with 409 ([rgw_lc.cc:2636-2666](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2636-L2666), [rgw_lc.cc:2703-2733](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2703-L2733)). | both | cosmetic | unreproduced | C-D9: retried, re-authorized. Exclusion. | C6 |
| LL-10 | PutBucketLifecycle compares 16 bytes of the decoded Content-MD5 whatever its length, reading past a short value ([rgw_op.cc:5933-6028](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L5933-L6028), [rgw_op.cc:6592-6695](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L6592-L6695)). | both | cosmetic: BadDigest either way | unreproduced | C-D10. No exclusion. | C6 |
| LL-11 | Current expiration on a versioning-enabled bucket writes a delete marker over a version written after its listing: `unmod_since` reaches `bucket_link_olh`, which applies it only to an instance entry that already exists, and a new marker's does not ([rgw_rados.cc:5900](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L5900), [rgw_rados.cc:6512](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6512); [cls_rgw.cc:1690-1701](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1690-L1701), [cls_rgw.cc:1831-1842](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1831-L1842)). A noncurrent rule may then delete the newer version. | both | data corruption | unreproduced; prior art not yet searched | C-D13: re-read, then the listed entry's conditions. Exclusion. | C10b |
| LL-12 | Noncurrent expiration removes a null version that a PUT on a suspended bucket rewrote as current after the listing: the worker passes only `unmod_since`, which the instance path never checks; it checks only If-Match, size and last-modified conditions, which the worker does not set ([rgw_rados.cc:5833-5968](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L5833-L5968), [rgw_lc.cc:608-679](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L608-L679)). | both | data corruption | unreproduced; prior art not yet searched | C-D13: re-read, then `Expect`. Exclusion. | C10b |

Reproduced as radosgw does, with no difference and no entry: the attr kept when
`guard_lc_modify` then fails to lock; an lc entry left processing for two days after a failed
re-lock; the day boundary in local time.

Existing registry entries this unit meets:
- "radosgw faults on a zero rgw_gc_max_objs, rgw_lc_max_objs or rgw_usage_max_shards"
  (#80991): phase 1's `floorShards` handles it.
- "radosgw's from_base64 reads before an all-'=' input" (security): PutBucketLifecycle's
  Content-MD5 is a caller; rgw-go answers BadDigest through its own decoder.
- "radosgw ignores a payload-hash mismatch on bodies read by read_all_input" (security): the
  body goes through `ReadParamBody`.
- "Squid's DeleteObjectTagging stamps the object with the epoch plus one nanosecond": a Squid
  radosgw's stamped object expires as decades old under rgw-go's worker too, since it is the
  stored mtime.
- "Squid labels a copy to another storage class with its source's class": the worker reads
  the index entry's class; rgw-go's copies are labelled right.
- "A multipart completion whose meta object outlives its head write..." (#80896): the abort
  goes through rgw-go's `Abort`, which leaves a completed upload's parts alone.
