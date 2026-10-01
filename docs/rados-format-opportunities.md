# RADOS format opportunities

This registry collects changes to the RADOS objects radosgw stores that would make an S3 gateway faster or cheaper while it still serves S3 correctly. That covers object heads and tails, head xattrs, bucket index entries and their omap, metadata objects, and the log, GC and usage objects.

rgw-go does not implement these changes. Its design keeps every on-disk structure byte-identical to what radosgw writes at the cluster's release, so the two gateways can share a zone (the design spec's version rule; the coexistence obligations in `docs/exclusions.md`). An entry here is an observation and a proposal. Adopting one needs a decision that relaxes that rule for some deployment: a zone only rgw-go serves, a feature flag both gateways honour, or an upstream change to Ceph.

Add an entry whenever work meets a format cost, and update it when a measurement, an upstream change or a decision bears on it. Recording an entry never blocks a task.

## Entry template

```markdown
## <format element>: <the cost>

- **Format element:** the object, xattr, omap key or encoding, with its
  definition at v19.2.6 and v20.2.4 (`path:line`).
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

None yet.
