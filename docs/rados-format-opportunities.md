# RADOS format opportunities

This registry collects changes to the RADOS objects radosgw stores that would make an S3 gateway faster or cheaper while it still serves S3 correctly. That covers object heads and tails, head xattrs, bucket index entries and their omap, metadata objects, and the log, GC and usage objects.

rgw-go does not implement these changes. Its design keeps every on-disk structure byte-identical to what radosgw writes at the cluster's release, so the two gateways can share a zone (the design spec's version rule; the coexistence obligations in `docs/differences.md`). An entry here is an observation and a proposal. Adopting one needs a decision that relaxes that rule for some deployment: a zone only rgw-go serves, a feature flag both gateways honour, or an upstream change to Ceph. An object of rgw-go's own, which radosgw neither reads nor writes, leaves radosgw's formats as they are; it is recorded here too, adopted, with what a radosgw sharing the zone leaves of it.

Add an entry whenever work meets a format cost, and update it when a measurement, an upstream change or a decision bears on it. Recording an entry never blocks a task.

## Entry template

```markdown
## <format element>: <the cost>

- **Format element:** the object, xattr, omap key or encoding, with its
  definition at v19.2.7 and v20.2.4 (a link to its lines at each tag).
- **Cost:** what it costs today, and how that was seen: a measurement (a
  `docs/benchmarks/` run, or the method) or reasoning from the code.
- **Proposal:** the changed format.
- **S3 compliance:** why S3 clients see no difference, or which S3 behaviour
  constrains the change.
- **Coexistence:** what the change breaks for a radosgw sharing the zone, and
  the path that would allow it: an rgw-go-only zone, a zone or cluster
  feature flag, or an upstream proposal to Ceph (linked once filed).
- **Status:** idea, measured, proposed upstream, adopted, or rejected (with
  the reason).
- **Found:** the work that met it, and the date.
```

## Entries

## The OLH log's removal entry: no record of a GOVERNANCE bypass

- **Format element:** `rgw_bucket_olh_log_entry`, the entry a link or unlink
  adds to a versioned key's pending log, which holds the epoch, the op, the
  op tag, the key and the delete-marker flag and nothing of the request that
  logged it
  ([cls_rgw_types.h:520-549](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw_types.h#L520-L549),
  [cls_rgw_types.h:519-548](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw_types.h#L519-L548)).
  The class writes it from the unlink's op tag and key
  ([cls_rgw.cc:1971](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1971),
  [cls_rgw.cc:2118](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2118)),
  and the op tag is 16 hex digits of the second and 16 random characters
  ([rgw_rados.cc:7873-7882](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L7873-L7882),
  [rgw_rados.cc:8536-8545](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L8536-L8545)).
- **Cost:** a replay that applies another request's logged removal cannot
  tell whether that delete was authorized to bypass a GOVERNANCE retention,
  so it cannot keep a version a GOVERNANCE retention set after the delete
  was logged protects without stranding every delete that did bypass one.
  rgw-go's replay therefore keeps a version only under a legal hold or an
  unexpired COMPLIANCE retention, and removes one a GOVERNANCE retention set
  in that window protects, as radosgw's replay removes any
  (`docs/differences.md`, "A replay keeps another writer's version whose
  object lock holds"). Reasoning from the code; no measurement.
- **Proposal:** a flag in the removal entry saying the delete was
  authorized with the governance bypass, carried in the unlink's class op
  and appended to the entry under encoding version 2 with compat 1, or the
  same bit carried in the op tag's random characters, which radosgw does
  not read. A replay would then keep a GOVERNANCE-retained version unless
  the entry says the delete bypassed the retention.
- **S3 compliance:** S3 lets a delete pass a GOVERNANCE retention only with
  `x-amz-bypass-governance-retention` and s3:BypassGovernanceRetention; the
  flag would let every replay honour a retention set after the delete's
  check, as the delete's own check honours one set before it.
- **Coexistence:** the entry field needs the class to change, so every OSD
  serving the bucket index pool runs it before a gateway sends the flag. A
  decoder of version 1 skips the appended field, so a radosgw that does not
  know it reads the log as before, and its replay removes the version
  whatever the flag says. The op tag form needs no class change but puts
  meaning into characters radosgw draws at random and every gateway treats
  as opaque. Either is an upstream proposal to Ceph; none is filed.
- **Status:** idea.
- **Found:** phase 2 task V19's fix round, from a security review of the
  replay's keep rule, 2026-11-14.

## An OLH log entry carries no guard on its removal: only the delete's own replay can honor its condition

- **Format element:** `rgw_bucket_olh_log_entry`, the entries of a key's
  pending OLH log in its bucket index shard: an epoch, an op, an op tag, a
  key and a delete-marker flag
  ([cls_rgw_types.h:520-554](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw_types.h#L520-L554),
  [cls_rgw_types.h:519-553](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw_types.h#L519-L553)).
  `apply_olh_log` collects the key of every removal entry and removes that
  instance, the null version's data being the key's OLH head, whichever
  request reads the log
  ([rgw_rados.cc:8479-8480](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8479-L8480) and
  [rgw_rados.cc:8529-8541](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8529-L8541) at v19.2.7,
  [rgw_rados.cc:9225-9226](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9225-L9226) and
  [rgw_rados.cc:9275-9287](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9275-L9287) at v20.2.4).
- **Cost:** reasoning from the code, in correctness and in requests. A
  delete that removes the null version's data on the strength of a
  condition, an If-Match marker or lifecycle's, guards the removal on the
  head it judged, and the guard can live only in the replay of that
  delete, because the log cannot carry it. So the replay of any other
  request between the delete's link and its own replay removes whatever
  data the head holds then, a write's that landed after the delete judged
  the head included. And such a delete must run its own replay even when
  told to skip it, which costs a replay per conditional marker that
  DeleteObjects' grouping of a name's deletes would have saved
  (`docs/differences.md`, "A versioned write whose version a delete
  removes before its link succeeds").
- **Proposal:** carry the guard the removing delete judged, the head's
  write tag or its ETag, in the removal's log entry, so that `apply_olh_log`
  removes the instance's head only while the head still carries it,
  whichever request replays the entry. An unconditional delete carries no
  guard and replays as today. The field would be appended beside the
  governance-bypass flag that entry "The OLH log's removal entry: no record
  of a GOVERNANCE bypass" proposes.
- **S3 compliance:** the guard withholds only a removal whose condition no
  longer holds, a write having replaced the data the condition judged; an
  unconditional delete is unchanged.
- **Coexistence:** the entries are written by cls_rgw on the OSDs and
  applied by whichever gateway replays them. A field added to the entry
  under a newer struct version with compat 1 is skipped by a gateway that
  does not know it
  ([encoding.h:1635-1642](https://github.com/ceph/ceph/blob/v19.2.7/src/include/encoding.h#L1635-L1642),
  [encoding.h:1651-1658](https://github.com/ceph/ceph/blob/v20.2.4/src/include/encoding.h#L1651-L1658)),
  but its replay would not honor the guard, so the guard binds only where
  every gateway that replays the key honors it, and rgw-go cannot write the
  field without the class carrying it. The path is an upstream change to
  cls_rgw and `apply_olh_log` together.
- **Status:** idea.
- **Found:** phase 2 task V18's re-review, 2026-11-14, in an emulator run
  of a third request's replay between a conditional marker's link and its
  own replay.

## An rgw-go object naming the versions a replay left unlisted

- **Format element:** a new object, `rgw-go.unlisted.<bucket id>`, in the
  bucket's index pool beside its shards. Its omap holds one key per version
  an rgw-go replay left stored and unlisted: the version's index key name and
  its instance, each with `%`, `/` and NUL percent-encoded, joined by `/`.
  Each value is a JSON record of when it was written, of the OLH tag the
  replay read the log under, and of the claim of a reader relisting the
  version, while one does. An xattr, `rgw-go.prune-cursor`, holds the last
  key a record write's prune examined. No radosgw format holds
  this: the OLH log carries only the class's link, unlink and
  instance-removal ops
  ([cls_rgw_types.h:512-517](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw_types.h#L512-L517),
  [cls_rgw_types.h:511-516](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw_types.h#L511-L516)),
  and its removal entry goes with the replay's trim, while a pending attr
  sits on the OLH head, which no listing reaches, and expires after
  `rgw_olh_pending_timeout_sec`
  ([rgw_rados.cc:8904-8934](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8904-L8934),
  [rgw_rados.cc:9650-9680](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9650-L9680)).
- **Cost:** without it, a version whose head and instance entry stay while
  its list entry is gone is found by no listing, lifecycle pass or bucket
  delete, since each reads the listing alone
  ([rgw_lc.cc:365-380](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L365-L380),
  [rgw_lc.cc:365-380](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L365-L380)),
  so no rule expires it, no S3 request names it unless its id is held, and
  a bucket holding one is either deleted with its objects left, radosgw's
  way, or refused, rgw-go's. The other way to find such versions, reading
  every shard's instance entries with `bi_list` on each lifecycle pass,
  costs at least a second read of the whole index each pass. The object
  costs one omap write in a replay that refuses a removal or keeps one for
  its lock, both rare, before its head op, and after its head op and trim a
  prune of two records for each of the k it recorded: at most three reads
  of the object, 6k index or head reads and 2k+1 writes; one omap read per
  lifecycle pass of a versioned bucket, per bucket delete whose listing
  reads empty and per purge; and per version relisted, a claim and a
  removal of its record. The prune keeps the object to about twice the
  records of versions still unlisted. Reasoning from the code; no
  measurement.
- **Proposal:** adopted in rgw-go as described
  (`recordUnlisted` and `relistUnlisted`, `internal/driver/unlisted.go`;
  `docs/differences.md`, "Lifecycle and a bucket delete list again a
  version a replay left unlisted"). A form both gateways would read is a
  record the class keeps itself, in each shard's own omap under a reserved
  key range, written in the same call as the unlink that takes the list
  entry and removed with the instance entry, which a listing option could
  return.
- **S3 compliance:** nothing in the object is served. A version left
  unlisted is listed again, where it was listed before the removal that a
  replay refused or kept for its lock.
- **Coexistence:** radosgw neither writes, reads nor removes the object.
  Its replays record nothing, so a version radosgw's own paths leave
  unlisted is not found, and its bucket delete and purge leave the object,
  a few omap keys in the index pool, beside the versions' heads and tails
  they also leave (`docs/ceph-upstream-bugs.md`, "[radosgw's bucket delete
  leaves the objects of a version no listing
  shows](ceph-upstream-bugs.md#radosgws-bucket-delete-leaves-the-objects-of-a-version-no-listing-shows)").
  A class-side record would need every OSD serving the index pool to run
  it, and is an upstream proposal to Ceph; none is filed.
- **Status:** adopted in rgw-go, as an object of its own; radosgw's formats
  are unchanged.
- **Found:** phase 2 task V24, 2026-11-21, choosing between this record and
  `bi_list` scans for lifecycle and the listing.
