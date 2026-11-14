# RADOS format opportunities

This registry collects changes to the RADOS objects radosgw stores that would make an S3 gateway faster or cheaper while it still serves S3 correctly. That covers object heads and tails, head xattrs, bucket index entries and their omap, metadata objects, and the log, GC and usage objects.

rgw-go does not implement these changes. Its design keeps every on-disk structure byte-identical to what radosgw writes at the cluster's release, so the two gateways can share a zone (the design spec's version rule; the coexistence obligations in `docs/differences.md`). An entry here is an observation and a proposal. Adopting one needs a decision that relaxes that rule for some deployment: a zone only rgw-go serves, a feature flag both gateways honour, or an upstream change to Ceph.

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
