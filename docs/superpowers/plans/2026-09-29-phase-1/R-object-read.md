# Unit R — Object Read Path: Phase 1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. Follow the tasks in order; do not begin a task until the previous one's verification passes.

**Goal:** Fill `ObjectStore.StatObject` and `ObjectStore.ReadObject` on the RADOS driver and register the object read routes (`get_obj` for GET and HEAD, `get_obj_attrs`, `get_obj_tags`, the object half of `get_acls`) so that every object a radosgw v19.2.6 or v20.2.4 wrote — both manifest forms, every placement and storage class, all four compression codecs, `_`-prefixed names — reads back byte-exact through rgw-go with radosgw's headers, errors and RADOS round trips.

**Architecture:** `internal/meta` gains the read-side attribute names and a ranged manifest iterator (RGWObjManifest::obj_iterator, seek and advance) so the driver walks only the stripes a range touches. `internal/compression` (new) decodes the four codecs in Ceph's per-block framing and maps a decompressed byte range onto the compressed blocks the way `RGWGetObj_Decompress::fixup_range` does. `internal/driver` composes radosgw's one head op (getxattrs + stat2 + first 4 MiB) in `StatObject`/`PrefetchObject`, and `ReadObject` schedules 4 MiB tail reads under a 16 MiB window, writing to the sink in offset order from reused buffers. `internal/op` gains `GetObject` (GET and HEAD, ranges, conditionals, partNumber, the refusals), `GetObjectAttributes`, `GetObjectTagging` and `GetObjectACL`; `internal/s3/object.go` registers the handlers and renders radosgw's headers and XML. Everything radosgw-specific is transcribed from the cited C++ at both tags and gated on `denc.Release` where the tags differ.

**Tech Stack:** Go 1.27; go-ceph (jhoblitt fork, `ceph_preview` tag) through the `radosclient` seam; `internal/denc`, `internal/meta`; `github.com/klauspost/compress` (zstd, snappy), `github.com/pierrec/lz4/v4` (lz4 block API, decision D3; on spec §4's dependency list, amended on main), standard `compress/flate|zlib|gzip` (zlib); counterfeiter fakes (`opfakes`, `radosclientfakes`), `memstore`, M's `internal/testutil/fakerados`; Ginkgo v2 + Gomega; the rooket clusters for the gate.

**Spec:** docs/superpowers/specs/2026-09-25-rgw-go-design.md §2 (performance first), §6 (data path: one op for stat+xattrs+first 4 MiB, the 16 MiB tail window), §8 (compatibility), §9 (phase 1 gates). Unit scope: docs/superpowers/plans/2026-09-29-phase-1/00-index.md, "The nine units", unit R. Contracts built against verbatim: G's frozen interface contract (docs/superpowers/plans/2026-09-29-phase-1/G-gateway-core.md, "Frozen interface contract"), Z's additions (Z-authorization.md, "Frozen interface contract additions"), M's decisions M-D6, M-D8, M-D9 and `op.LogUsage`, A's `AuthResult.ContentLength`. Every citation below was verified at v19.2.6 (`[S]`) and v20.2.4 (`[T]`).

---

## Global Constraints

- Module `github.com/jhoblitt/rgw-go`; `go 1.27`, minor only; every `go` command carries `-tags=ceph_preview` (the Makefile's `GO_TAGS`); cgo builds on this machine need `CGO_CFLAGS=-I/home/jhoblitt/github/ceph/src/include CGO_LDFLAGS=-L$HOME/.local/lib/rgw-go`; `go get`/`go mod download` run with the sandbox disabled.
- Ceph floor: every daemon runs 19.2.6+ (Squid) or 20.2.4+ (Tentacle); no special handling for point releases below the floor. Squid-vs-Tentacle differences are gated on `denc.Release` (`Env.Zone.Release()`): the `x-amz-mp-parts-count` header (R-D10), GetObjectAttributes' existence in radosgw (R-D1), the Tentacle cloud-tier restore states (R-D6), SSE-S3's `rgw_crypt_require_ssl` check (R-D9).
- G's frozen contract is filled, never renamed: `StatObject`, `ReadObject`, `ObjectState`, `ByteRange`, `op.Sink`, `op.Request`, `op.Run`, route names, `objectHandlers()`. Additive changes (`ObjectStore.PrefetchObject`, `ObjectState.Head`) regenerate the fakes (`make generate`) and extend `memstore`; `make generate-check` fails a stale fake. `s3.objectGetACLs` (M-D8) is assigned, not redeclared. `acl.PermFor(action)` is the perm passed to `op.VerifyObjectPermission` (D-Z3); `VerifyObjectPermission` runs even for a missing object and its `op.ErrNoSuchKey` is the 404 (D-Z5).
- Performance first (spec §2): RADOS round trips identical to radosgw's (R-D3), tail reads of `rgw_get_obj_max_req_size` (4 MiB) under a `rgw_get_obj_window_size` (16 MiB) window, data streamed through `op.Sink` in offset order, read buffers reused across requests (`radosclient.ReadOp.ReadInto`), no whole-object buffering, decompression per block into a reused output buffer.
- Byte-compatibility is the acceptance test: header names, values, error codes, messages and the XML bodies are transcribed from the C++ cited in each task at both tags, not designed.
- Dependencies point downward: `meta` and `compression` import nothing above `denc`; `driver` imports `op`, `meta`, `compression`, `radosclient`; `op` imports `meta`, `acl`, `policy`, `tags`; `s3` imports `op`. `compression` is the only package importing the codec libraries. `github.com/pierrec/lz4/v4` is on spec §4's third-party dependency list, amended on main to add it (decision D3, accepted by decision 13), and `github.com/klauspost/compress` (already on the list) joins `go.mod`.
- Tests are Ginkgo v2 and Gomega only; one `<pkg>_suite_test.go` per package (`compression` gets a new one; `meta`, `driver`, `op`, `s3`, `gate` have theirs); integration specs carry `//go:build integration` and `Label("integration")`. Cluster tasks use the rooket clusters (`make cluster-up RELEASE=squid|tentacle`, `make populate`, T's `make rgw-go-up`) and are marked `[cluster]`; never the ambient cluster.
- Logging is `log/slog` with the `…Context` variants, static lowercase messages, snake_case keys; never a key or header value that could be a secret (SSE-C keys are never logged). Errors wrap with `%w`; every RADOS error reaches the op as `op.FromRADOS(err, op.ScopeObject)` or a specific sentinel.
- Commits are Conventional Commits with the repository trailers; each task ends in a draft PR assigned to the author, CI watched, merged on green. Material changes to `docs/exclusions.md` (Task 7) are reported so the rgw-rs session can be told. Nothing new is created under `docs/`; Tasks 7 and 8 modify existing registry files only.

## Review Focus

1. **A range that starts in the tail of a compressed multipart object.** The decompressed range maps onto compressed blocks of several parts; the RADOS reads must be widened to block boundaries, decoded block by block, trimmed by `q_ofs`/`q_len`, and written in order, or the client gets misaligned bytes with a 206. Pinned in Task 2 (`Window` on a multi-block map) and Task 4 (a ranged read across two compressed stripes on fakerados).
2. **A tail read that fails or a client that disconnects mid-stream after the header went out.** With four 4 MiB reads in flight, one failure must stop the stream without writing the later pieces, drain the in-flight completions, and never reuse a buffer an abandoned RADOS op may still fill. Pinned in Task 4 (an injected ENOENT on the third stripe; a cancelled context).
3. **`If-None-Match` with an ETag list, weak validators or `*`, and `If-Modified-Since` in ISO 8601 or an unparsable date.** radosgw compares literally after unquoting one pair of quotes, treats a date it cannot parse as 400, and gives `If-None-Match` precedence over `If-Modified-Since` — a "clever" implementation diverges. Pinned in Task 5 (a DescribeTable of the sixteen combinations).
4. **`partNumber` on a single-part object, on a part beyond the count, on an object whose part head is missing, and with `Range`.** Each has a distinct answer (whole object for part 1; 400 InvalidPart; 400 InvalidPart; range applied to the part). Pinned in Task 5.
5. **An object whose head is exactly 4 MiB (`head-full.bin`) and one whose head is empty but has a manifest (multipart).** The first must be served from the prefetch alone (one op); the second must not read the head's data at all. Pinned in Task 4 and the gate (Task 8).

## Decisions this plan fixes

Each is repeated where it applies, with its source.

- **R-D1** GetObjectAttributes is served on both releases (spec §3: Tentacle feature level for gateway-side features). A Squid radosgw has no `RGWGetObjAttrs` (absent at [S]; [T] [rgw_op.h:1740](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.h#L1740), dispatched from `op_get` at [T] [rgw_rest_s3.cc:5370-5371](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5370-L5371)) and answers `?attributes` as GetObject with the object's body ([S] [rgw_rest_s3.cc:4805-4821](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L4805-L4821) has no attributes branch). That is a difference on Squid: Task 7, which registers the route, records it in `docs/exclusions.md`'s coexistence section. Its parity cost is one s3-tests case, `test_get_object_attributes`, carried in T's `test/s3tests/known-differences-squid.txt` (T Task 12); Task 8 asserts it is the only difference.
- **R-D2** Prefetch exactly as radosgw's `RGWGetObj::prefetch_data` ([S] [rgw_op.cc:2176-2191](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L2176-L2191)): a GET without `Range` reads the first `rgw_max_chunk_size` bytes in the head op; HEAD, ranged GET and GetObjectAttributes stat without data. Contract addition: `ObjectStore.PrefetchObject` and `ObjectState.Head`.
- **R-D3** Round trips mirror radosgw: HEAD 1; GET within the head 1; GET with `Range` 1 + one read per 4 MiB of the range; GET past the head 1 + one read per 4 MiB past the prefetched data; `partNumber` 2 head ops + the part's reads.
- **R-D4** `If-Match: *` is compared literally and fails 412 on an existing object ([S] [rgw_rados.cc:6975-6981](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6975-L6981)); recorded as a quirk in `docs/ceph-upstream-bugs.md` (Task 8).
- **R-D5** Compression decoders live in `internal/compression`; a ranged read of a compressed object is widened to block boundaries and trimmed after decoding, as `RGWGetObj_Decompress::fixup_range`/`handle_data` do ([rgw_compression.cc:107-218](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_compression.cc#L107-L218), identical at both tags).
- **R-D6** Cloud-tier objects are refused as radosgw refuses them: Squid 403 InvalidObjectState "This object was transitioned to cloud-s3" on GET, headers on HEAD ([S] [rgw_op.cc:2361-2371](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L2361-L2371), [:938-967](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L938-L967)); Tentacle by restore status ([T] [:989-1122](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L989-L1122)): InProgress → 408 RequestTimeout "restore is still in progress", CloudRestored → served, otherwise 403 InvalidObjectState with "Read through is not enabled for this config" when the tier forbids read-through and "This object was transitioned to cloud-s3" when it allows it (cloud restore is excluded; logged at error). That last case is a difference: radosgw starts a restore there and answers 408 RequestTimeout "restore is still in progress" ([T] [:1083-1111](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L1083-L1111)); Task 7 records it under `docs/exclusions.md`'s "Cloud transition and restore". Tentacle's `x-amz-restore` headers are emitted from the restore attrs.
- **R-D7** Swift DLO/SLO objects (`user.rgw.user_manifest`, `user.rgw.slo_manifest`) answer 501 NotImplemented on GET and HEAD (Swift is excluded). radosgw composes them even on an S3 GET or HEAD (`RGWGetObj::execute`, [S] [rgw_op.cc:2373-2394](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L2373-L2394), [T] [:2606-2627](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L2606-L2627); the S3 handler skips the manifest only for the multisite `sync-manifest` system parameter, [S] [rgw_rest_s3.cc:295](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L295), [T] [:300](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L300)), so this is a difference: Task 7 records it under `docs/exclusions.md`'s "Swift API and Swift authentication". Such objects exist only where a client wrote them through Swift.
- **R-D8** `?torrent` answers 404 NoSuchKey when the object carries no `user.rgw.torrent`, as radosgw answers for every object written while `rgw_torrent_flag` was false (the default at both tags, [src/common/options/rgw.yaml.in \[S\]:3205-3210](https://github.com/ceph/ceph/blob/v19.2.6/src/common/options/rgw.yaml.in#L3205-L3210), [\[T\]:3343-3348](https://github.com/ceph/ceph/blob/v20.2.4/src/common/options/rgw.yaml.in#L3343-L3348); Rook never sets it), and 501 NotImplemented when it does, where radosgw serves the stored torrent ([S] [rgw_op.cc:2275-2298](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L2275-L2298), [T] [:2511-2535](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L2511-L2535); [rgw_torrent.cc:62-72](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_torrent.cc#L62-L72)). radosgw also falls back to a torrent an older release kept in the head object's omap under `rgw.torrent` (`RadosObject::get_torrent_info`, [S] [rgw_sal_rados.cc:2465-2499](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L2465-L2499), [T] [:3058-3092](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L3058-L3092)); rgw-go reads the attribute only and answers 404 there. Both are differences: Task 7, which registers `?torrent` on `get_obj`, records them in `docs/exclusions.md`'s coexistence section.
- **R-D9** SSE on read (`rgw_s3_prepare_decrypt`, [S] [rgw_crypt.cc:1303-1505](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_crypt.cc#L1303-L1505)): an `x-amz-server-side-encryption` request header on GET/HEAD → 400 InvalidRequest; SSE-C objects get radosgw's header validation and messages verbatim, then 501 NotImplemented once the headers are valid (decryption is phase 2); SSE-KMS → 400 InvalidArgument "Failed to retrieve the actual key, kms-keyid: <id>"; AES256 → 400 InvalidArgument "Failed to retrieve the actual key"; RGW-AUTO → 500 InternalError; `rgw_crypt_require_ssl` over plain HTTP → 400 InvalidRequest (SSE-C and SSE-KMS both tags, AES256 on Squid only).
- **R-D10** `x-amz-mp-parts-count`: Squid only with `partNumber` ([S] [rgw_rados.cc:6907-6909](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6907-L6909)); Tentacle on every multipart object ([T] [rgw_rados.cc:7690-7697](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7690-L7697), rgw_rest_s3.cc:531).
- **R-D11** GetObjectAttributes' `ObjectParts` sizes come from the manifest (no RADOS ops) for uncompressed objects and from one part-head read per listed part for compressed objects (radosgw's `accounted_size`, [T] [rgw_sal_rados.cc:2893-2910](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L2893-L2910)).
- **R-D12** Header names are written in net/http's canonical form; radosgw's spellings differ only in case.
- **R-D13** `x-rgw-object-type: Normal` on every GET/HEAD, or `Appendable` plus `x-rgw-next-append-position` when `user.rgw.append_part_num` exists ([S] [rgw_rest_s3.cc:464-469](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L464-L469)).
- **R-D14** `Accept-Ranges: bytes` goes out exactly where radosgw's `dump_content_length` runs ([rgw_rest.cc:388-397](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L388-L397) at [S] and [T]): on a GET or HEAD of an object that succeeds, which names its length ([S] [rgw_rest_s3.cc:459](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L459), [T] [:495](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L495); a 206 too), and on every error document (`end_header`'s error branch, [rgw_rest.cc:620-624](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L620-L624), [T] [:625-629](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L625-L629)). Not on a 304: `rgw_err::is_err` is false for 200-399 ([rgw_common.cc:203-207](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L203-L207)), and the `done:` path calls `end_header(s, this)` with no length ([S] [rgw_rest_s3.cc:617-638](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L617-L638)). Not on the XML successes R serves either: GetObjectTagging ([S] [:751](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L751)) and GetObjectAttributes ([T] [:4014](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4014)) pass `end_header` no length, and their `Content-Length` is the one radosgw's frontend adds when the response completes ([rgw_client_io_filters.h:221-253](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_client_io_filters.h#L221-L253)). So `getObjectSink` sets the length through G's `SetContentLength` (G Task 4), `WriteError` adds the pair to errors, and the XML routes set a `Content-Length` alone; G's response writer adds nothing.
- **R-D15** `x-amz-request-charged: requester` when the bucket is requester-pays, the identity is not its owner and the status is not an error ([rgw_rest.cc:597-601](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L597-L601)). G's `SetCommonHeaders` emits it (G Task 4), so R's handlers rely on G; the per-route step 13 below is satisfied by calling `SetCommonHeaders`, not by a route-local helper.
- **R-D16** A ranged manifest iterator (`Manifest.Seek`) is added to `meta`; eager `Stripes()` stays for gc and tests.
- **R-D17** GET and HEAD with `partNumber` answer with radosgw's ETag, as Task 8's cluster comparison measures it; radosgw's behaviour wins over AWS's and over s3-tests'. By code reading it is the part head's own ETag: `Read::prepare` redirects the state to the part head through `get_part_obj_state` and returns that head's attrs, Content-Type and metadata included ([S] [rgw_rados.cc:6880-6933](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6880-L6933), [T] [:7728-7781](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7728-L7781)). The plan isolates the choice in one function (`GetObject.Init`), so the measurement can move it. s3-tests' `test_multipart_get_part` expects the multipart ETag ([s3tests/functional/test_s3.py:6650](https://github.com/ceph/ceph/blob/v20.2.4/src/test/rgw/s3-tests/s3tests/functional/test_s3.py#L6650), :6654 at the pinned commit); if radosgw fails it, rgw-go failing it too is parity, not a known difference, and it gets no line in T's known-differences lists.
- **R-D18** Head data reads that reach RADOS carry `CmpXattr(user.rgw.idtag, EQ, WriteTag)` when the tag is real (`append_atomic_test`, [S] [rgw_rados.cc:6457-6472](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6457-L6472)); ECANCELED → 409 ConcurrentModification.
- **R-D19** Data pools on the read path resolve with radosgw's fallbacks (explicit placement → the rule's class → STANDARD's pool → the zonegroup default placement; [rgw_zone.h:285-303](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_zone.h#L285-L303), [rgw_zone_types.h:281-290](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_zone_types.h#L281-L290), rgw_obj_manifest.cc:412-449), not M's strict `Placement`, so an object of a since-removed storage class still reads.

## The read path radosgw runs

The verified reference every task transcribes; `[S]` is v19.2.6, `[T]` v20.2.4, identical unless stated.

| Request | Synchronous RADOS ops, in order |
|---|---|
| HEAD | 1: head object `getxattrs` + `stat2` (`raw_obj_stat`, [S] [rgw_rados.cc:8827-8870](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8827-L8870), no data since `prefetch_data()` is false for `!get_data`, [rgw_op.cc:2176-2191](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L2176-L2191)) |
| GET, no Range, object within its head | 1: `getxattrs` + `stat2` + `read(0, rgw_max_chunk_size)`; the body comes from the prefetch (`get_obj_iterate_cb` :7408-7422) |
| GET, no Range, object with tails | 1 as above; then one `read(read_ofs, len)` per ≤ 4 MiB piece of every stripe past the prefetched bytes (`iterate_obj` [:7466-7536](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L7466-L7536)), ≤ 16 MiB in flight (`Read::iterate` [:7444-7464](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L7444-L7464)), delivered in offset order (`get_obj_data::flush` [:7345-7379](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L7345-L7379)) |
| GET with Range | 1: `getxattrs` + `stat2` (no prefetch); then the pieces of `[ofs, end]` as above, head pieces guarded by `cmpxattr(idtag)` (:7400-7403) |
| GET/HEAD with partNumber | 1: multipart head `getxattrs` + `stat2`; 2: part head `getxattrs` + `stat2` (+ `read` when the request prefetches) (`Read::prepare` [:6857-6917](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6857-L6917), `get_part_obj_state` [:6759-6845](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6759-L6845)); then the part's pieces |
| GET of a compressed object | as above with the range widened to compression blocks (`fixup_range` [rgw_compression.cc:183-218](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_compression.cc#L183-L218)); each block decoded independently (`handle_data` [:107-181](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_compression.cc#L107-L181)) |
| GetObjectAttributes [T] | 1: head `getxattrs` + `stat2`; with `ObjectParts` on a multipart object, one `getxattrs` + `stat2` per listed part ([T] [rgw_sal_rados.cc:2834-2933](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L2834-L2933)) |
| GetObjectTagging, GetObjectAcl | 1: head `getxattrs` + `stat2` ([S] [rgw_op.cc:1057-1074](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L1057-L1074), [:5695-5734](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5695-L5734); the ACL comes from the same attrs) |

Conditionals ([S] [rgw_rados.cc:6946-6990](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6946-L6990), S3 uses second precision since `high_precision_time = system_request`): `If-Modified-Since` only when no `If-None-Match`, `!(since < mtime)` → 304; `If-Unmodified-Since` only when no `If-Match`, `since < mtime` → 412; `If-Match` unquoted and compared as a prefix of the stored etag → 412 on mismatch; `If-None-Match` unquoted, equal → 304. An unparsable date is 400 InvalidArgument (`init_common` [rgw_op.cc:2464-2487](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L2464-L2487); `parse_time` [rgw_common.cc:702-715](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L702-L715) accepts RFC 850, asctime, RFC 1123 with GMT/UTC or `%z`, and ISO 8601). Range (`parse_range` [rgw_op.cc:160-224](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L160-L224)): `bytes=` anywhere in the value or a case-insensitive `bytes` then `=`; `atoll` on both ends; suffix `-N`; `end < ofs` or no `-` → 416 unless `rgw_ignore_get_invalid_range`; `range_to_ofs` ([rgw_rados.cc:7002-7022](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L7002-L7022)): suffix clamps to 0, `end` clamps to size−1, `ofs >= size` → 416; a Range on an empty object → 416 ([rgw_op.cc:2397-2401](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L2397-L2401)). Status 206 with `Content-Range: bytes ofs-end/total` whenever a Range header was present, HEAD included ([rgw_rest_s3.cc:398-407](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L398-L407)).

Response headers (`send_response_data` [S] [rgw_rest_s3.cc:380-659](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L380-L659); `end_header` [rgw_rest.cc:589-655](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L589-L655)): `Content-Length` + `Accept-Ranges: bytes`; `Last-Modified` (RFC 1123, GMT); `x-amz-version-id` when the key has an instance; `x-rgw-object-type`; `x-amz-mp-parts-count` (R-D10); `ETag` quoted; the six `response-*` query overrides (anonymous → 400 InvalidRequest, control characters → 400 InvalidRequest); the `rgw_to_http_attrs` table (Content-Language, Expires, Cache-Control, Content-Disposition, Content-Encoding without `aws-chunked`, X-Object-Manifest, X-Robots-Tag, X-Amz-Storage-Class, x-amz-website-redirect-location; [rgw_rest.cc:98-112](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L98-L112)) with trailing NULs stripped; `Content-Type` from `user.rgw.content_type` (trailing NULs stripped) or `binary/octet-stream`; `x-amz-meta-*` from `user.rgw.x-amz-meta-*` (one trailing NUL stripped); `x-amz-tagging-count`; `x-amz-request-charged`; `Server`, `x-amz-request-id` (G). On 304: Last-Modified, ETag, Cache-Control, Expires and no body (`rgw_err::is_err` is false for 304, [rgw_common.cc:203-207](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L203-L207)); other errors carry the XML error document.

## File structure

```
go.mod, go.sum                                   modify: + github.com/klauspost/compress, + github.com/pierrec/lz4/v4 (Task 2)
internal/meta/attrs_read.go                      the read-side attr names (Task 1)
internal/meta/manifest_read.go                   StripeIter (Seek/Next), HasTail, PartsCount, PartBounds (Task 1)
internal/meta/manifest_read_test.go              specs against the phase-0 manifest goldens (Task 1)
internal/compression/{doc,codec,zlib,snappy,zstd,lz4,blocks}.go   the four decoders in Ceph framing; the block map (Task 2)
internal/compression/{compression_suite,codec,blocks}_test.go
internal/op/object.go                            modify: ObjectStore.PrefetchObject, ObjectState.Head (Task 3)
internal/op/opfakes/fake_object_store.go         regenerated (Task 3)
internal/memstore/object.go                      modify: PrefetchObject, ReadObject honours Head (Task 3)
internal/testutil/fakerados/pool.go              modify: ReadStep copies into Buf (Task 3)
internal/driver/readpool.go                      data-pool resolution with radosgw's fallbacks (Task 3)
internal/driver/stat.go                          the head op, ObjectState decode, StatObject, PrefetchObject (Task 3)
internal/driver/read.go                          ReadObject: stripe scheduler, window, ordered sink, buffers, decompression; readStored, the raw stored-byte read W's CopyObject uses (Task 4)
internal/driver/{stat,read}_test.go              specs on fakerados (Tasks 3, 4)
internal/op/getobject.go                         GetObject op (GET and HEAD): range, conditionals, partNumber, refusals (Task 5)
internal/op/readconds.go                         parse_range, parse_time, unquote, the condition table (Task 5)
internal/op/getobject_test.go, readconds_test.go
internal/op/objectread.go                        GetObjectTagging, GetObjectACL, GetObjectAttributes ops (Task 6)
internal/op/objectread_test.go
internal/s3/object.go                            modify: objectHandlers() gains get_obj, get_obj_attrs, get_obj_tags; objectGetACLs assigned (Task 7)
internal/s3/getobject.go                         header parsing, response rendering, the attrs table (Task 7)
internal/s3/getobjattrs.go                       GetObjectAttributes XML (Task 7)
internal/s3/getobject_test.go, getobjattrs_test.go
test/gate/read_test.go                           [cluster] the read-back oracle against radosgw, the one-round-trip metric (Task 8)
hack/rooket/read-gate.sh                         resolves both endpoints and runs the read gate (Task 8)
Makefile                                         modify: read-gate target (Task 8)
docs/exclusions.md                               modify: GetObjectAttributes on Squid, Swift DLO/SLO, ?torrent, cloud read-through (Task 7)
docs/ceph-upstream-bugs.md                       modify: the If-Match `*` quirk (Task 8)
```

## Task index

| # | Task | Depends on | Cluster |
|---|---|---|---|
| 1 | `meta`: read-side attr names, the ranged manifest iterator, parts helpers | none | no |
| 2 | `compression`: the four decoders in Ceph framing and the block map | none | no |
| 3 | `driver`: pool resolution, the composed head op, `StatObject`, `PrefetchObject`; contract additions | 1, G Task 8, M Task 1 (fakerados, poolCache) | no |
| 4 | `driver`: `ReadObject` — stripes, window, ordered sink, buffers, decompression | 2, 3 | no |
| 5 | `op`: `GetObject` (GET and HEAD): range, conditionals, partNumber, refusals, usage | 1, G Task 1, Z (`acl.PermFor`), M (`op.LogUsage`) | no |
| 6 | `op`: `GetObjectTagging`, `GetObjectACL`, `GetObjectAttributes` | 5, Z (`tags`, `acl` XML) | no |
| 7 | `s3`: handlers and rendering for the four routes | 5, 6, G Tasks 3-4, M Task 11 (`objectGetACLs`) | no |
| 8 | Gate: corpus read-back through rgw-go against radosgw, the one-round-trip metric, s3-tests GET groups; registry entries | 4, 7, T Tasks 3, 11, 12 | `[cluster]` |

Tasks 1 and 2 are independent and start together. Task 3 needs M's Task 1 (`fakerados`, `poolCache`) and G's driver skeleton; 4 follows 3. Task 5 needs only G's contract, Z's `acl.PermFor` and M's `op.LogUsage`; 6 follows 5; 7 follows 6. Task 8 closes the unit.

---

### Task 1: `meta`: read-side attr names, the ranged manifest iterator, parts helpers

**Files:**
- Create: `internal/meta/attrs_read.go`, `internal/meta/manifest_read.go`, `internal/meta/manifest_read_test.go`
- Read (unchanged): `internal/meta/manifest_iter.go` (the unexported `manifestIter`, `newManifestIter`, `next`, `stripeStart`, `implicitLocation`, `sameBucket`), `internal/meta/attrs.go`

**Interfaces:**
- Consumes: `meta.Manifest`, `meta.Stripe`, `meta.Obj`, `meta.ObjKey`, `meta.PlacementRule`, the unexported iterator in `manifest_iter.go`, the goldens `internal/meta/testdata/manifests/squid-{large,multipart}.{bin,json}`.
- Produces (W's Task 3 adds the names it writes in `attrs.go`; a name present in both is defined once, so drop the duplicate here when W lands first):

```go
package meta

// Attr names the read path meets (src/rgw/rgw_common.h [S]:80-169, [T]:85-170).
const (
	AttrCryptPrefix           = AttrPrefix + "crypt."
	AttrCryptMode             = AttrCryptPrefix + "mode"   // "SSE-C-AES256", "SSE-KMS", "AES256", "RGW-AUTO"
	AttrCryptKeyMD5           = AttrCryptPrefix + "keymd5"
	AttrCryptKeyID            = AttrCryptPrefix + "keyid"
	AttrUserManifest          = AttrPrefix + "user_manifest"
	AttrSLOManifest           = AttrPrefix + "slo_manifest"
	AttrTorrent               = AttrPrefix + "torrent"
	AttrAppendPartNum         = AttrPrefix + "append_part_num"
	AttrReplicationStatus     = AttrPrefix + "amz-replication-status"
	AttrRestoreStatus         = AttrPrefix + "restore-status"       // [T] only
	AttrRestoreType           = AttrPrefix + "restore-type"         // [T] only
	AttrRestoreExpiryDate     = AttrPrefix + "restore-expiry-date"  // [T] only
	AttrCloudTierStorageClass = AttrPrefix + "cloudtier_storage_class"
	AttrCacheControl          = AttrPrefix + "cache_control"
	AttrContentDisp           = AttrPrefix + "content_disposition"
	AttrContentEnc            = AttrPrefix + "content_encoding"
	AttrContentLang           = AttrPrefix + "content_language"
	AttrExpires               = AttrPrefix + "expires"
	AttrXRobotsTag            = AttrPrefix + "x-robots-tag"
	AttrWebsiteRedirect       = AttrPrefix + "x-amz-website-redirect-location"
	AttrCksum                 = AttrPrefix + "cksum" // [T] only; unread until checksums are implemented
)

// RestoreStatus is rgw::sal::RGWRestoreStatus ([T] rgw_sal.h:169-174), stored as one byte.
type RestoreStatus uint8
const (
	RestoreNone              RestoreStatus = 0
	RestoreAlreadyInProgress RestoreStatus = 1
	CloudRestored            RestoreStatus = 2
	RestoreFailed            RestoreStatus = 3
)

// StripeIter is RGWObjManifest::obj_iterator positioned by Seek and advanced by Next.
type StripeIter struct{ /* it *manifestIter, m *Manifest */ }
// Seek is obj_find: the iterator at the stripe holding ofs, clamped to ObjSize.
func (m *Manifest) Seek(ofs uint64) (*StripeIter, error)
func (it *StripeIter) Done() bool           // ofs == ObjSize (obj_end)
func (it *StripeIter) Ofs() uint64          // get_ofs
func (it *StripeIter) StripeOfs() uint64    // get_stripe_ofs
func (it *StripeIter) StripeSize() uint64   // get_stripe_size, untrimmed
func (it *StripeIter) LocOfs() uint64       // location_ofs: an explicit piece's loc_ofs, else 0
func (it *StripeIter) PartID() int32        // get_cur_part_id
func (it *StripeIter) Location() (obj Obj, placement PlacementRule, inHead bool)
func (it *StripeIter) Next() error          // operator++

func (m *Manifest) HasTail() bool                                   // RGWObjManifest::has_tail
func (m *Manifest) PartsCount() (int, error)                        // 0 unless multipart; else max(1, endPartID-1)
func (m *Manifest) PartBounds(n int) (ofs, size uint64, head Obj, ok bool, err error) // obj_find_part
```

`Seek` mirrors `obj_iterator{dpp, this, std::min(ofs, obj_size)}` ([rgw_obj_manifest.h:595-597](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_obj_manifest.h#L595-L597)); `PartsCount` mirrors `get_part_obj_state`'s `last_part_id = obj_end().get_cur_part_id(); parts_count = max(1, last_part_id - 1)` with `0` for a non-multipart manifest ([S] [rgw_rados.cc:6769-6779](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6769-L6779)); `PartBounds` mirrors `obj_find_part` ([rgw_obj_manifest.cc:200-219](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_obj_manifest.cc#L200-L219): a linear walk from `obj_begin`, `end` when the part id passes `n`) and the part's extent as `get_part_obj_state` measures it (the offset of the part's first stripe to the offset where the part id changes, [:6794-6838](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6794-L6838)); `HasTail` mirrors [rgw_obj_manifest.h:394-404](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_obj_manifest.h#L394-L404) (explicit: one piece → its loc differs from the head; ≥ 2 pieces → true; rules: `obj_size > head_size`). Every accessor is the C++ member of the same name on the unexported `manifestIter` (`ofs`, `stripeOfs`, `stripeSize`, `curPartID`, `location`).

- [ ] **Step 1: Write the failing iterator specs against the goldens**

`internal/meta/manifest_read_test.go` (package `meta_test`, reusing `manifest_test.go`'s `decodeWhole` and `load` pattern):

```go
var _ = Describe("Manifest.Seek", func() {
	const marker = "e7bceed5-d2a6-4b0b-b484-cac20e0beb53.4156.1"
	load := func(name string) meta.Manifest {
		GinkgoHelper()
		b, err := os.ReadFile(filepath.Join("testdata", "manifests", name+".bin"))
		Expect(err).NotTo(HaveOccurred())
		return decodeWhole(b, meta.DecodeManifest)
	}
	oid := func(it *meta.StripeIter) string {
		obj, _, _ := it.Location()
		return meta.Stripe{Obj: obj}.OID()
	}

	Describe("large.bin: a 4 MiB head and two shadow stripes", func() {
		const prefix = ".AkTbQPoXT3s8QcthZt-DpS5NPRLqS5u_"
		It("seeks into the head and walks to the end in stripe order", func() {
			m := load("squid-large")
			it, err := m.Seek(0)
			Expect(err).NotTo(HaveOccurred())
			_, _, inHead := it.Location()
			Expect(inHead).To(BeTrue())
			Expect(it.StripeOfs()).To(BeZero())
			Expect(it.StripeSize()).To(BeEquivalentTo(4 << 20))
			Expect(it.LocOfs()).To(BeZero())
			Expect(it.PartID()).To(BeZero())
			Expect(it.Next()).To(Succeed())
			Expect(oid(it)).To(Equal(marker + "__shadow_" + prefix + "1"))
			Expect(it.StripeOfs()).To(BeEquivalentTo(4 << 20))
			Expect(it.Next()).To(Succeed())
			Expect(oid(it)).To(Equal(marker + "__shadow_" + prefix + "2"))
			Expect(it.StripeSize()).To(BeEquivalentTo(2 << 20), "the iterator trims the last stripe to the object")
			Expect(it.Next()).To(Succeed())
			Expect(it.Done()).To(BeTrue())
			Expect(it.Ofs()).To(BeEquivalentTo(10 << 20))
		})
		It("seeks into the middle of a tail stripe", func() {
			m := load("squid-large")
			it, err := m.Seek(5 << 20)
			Expect(err).NotTo(HaveOccurred())
			Expect(oid(it)).To(Equal(marker + "__shadow_" + prefix + "1"))
			Expect(it.Ofs()).To(BeEquivalentTo(5 << 20))
			Expect(it.StripeOfs()).To(BeEquivalentTo(4 << 20))
			Expect(it.StripeSize()).To(BeEquivalentTo(4 << 20))
		})
		It("clamps a seek past the end to obj_end", func() {
			m := load("squid-large")
			it, err := m.Seek(11 << 20)
			Expect(err).NotTo(HaveOccurred())
			Expect(it.Done()).To(BeTrue())
		})
		It("has a tail and no parts", func() {
			m := load("squid-large")
			Expect(m.HasTail()).To(BeTrue())
			Expect(m.PartsCount()).To(BeZero())
			_, _, _, ok, err := m.PartBounds(1)
			Expect(err).NotTo(HaveOccurred())
			Expect(ok).To(BeFalse())
		})
	})

	Describe("multipart.bin: three parts of 8, 8 and 4 MiB across two rules", func() {
		const prefix = "multipart.bin.2~vXcCS0UgSATwQOecOwRkZrWyEyux7jO"
		It("counts the parts as radosgw's end iterator does (cur_part_id 4 in the golden dump)", func() {
			m := load("squid-multipart")
			Expect(m.PartsCount()).To(Equal(3))
			Expect(m.HasTail()).To(BeTrue())
		})
		DescribeTable("finds each part's bounds and head object",
			func(n int, ofs, size uint64, head string) {
				m := load("squid-multipart")
				gotOfs, gotSize, obj, ok, err := m.PartBounds(n)
				Expect(err).NotTo(HaveOccurred())
				Expect(ok).To(BeTrue())
				Expect(gotOfs).To(Equal(ofs))
				Expect(gotSize).To(Equal(size))
				Expect(meta.Stripe{Obj: obj}.OID()).To(Equal(marker + "__multipart_" + head))
				Expect(obj.Key.NS).To(Equal(meta.NSMultipart))
			},
			Entry("part 1", 1, uint64(0), uint64(8<<20), prefix+".1"),
			Entry("part 2", 2, uint64(8<<20), uint64(8<<20), prefix+".2"),
			Entry("part 3", 3, uint64(16<<20), uint64(4<<20), prefix+".3"),
		)
		It("reports no part 4 and no part 0", func() {
			m := load("squid-multipart")
			for _, n := range []int{0, 4} {
				_, _, _, ok, err := m.PartBounds(n)
				Expect(err).NotTo(HaveOccurred())
				Expect(ok).To(BeFalse(), "part %d", n)
			}
		})
		It("seeks into part 2's second stripe and carries the part id", func() {
			m := load("squid-multipart")
			it, err := m.Seek(13 << 20)
			Expect(err).NotTo(HaveOccurred())
			Expect(oid(it)).To(Equal(marker + "__shadow_" + prefix + ".2_1"))
			Expect(it.PartID()).To(BeEquivalentTo(2))
			Expect(it.StripeOfs()).To(BeEquivalentTo(12 << 20))
			Expect(it.Next()).To(Succeed())
			Expect(it.PartID()).To(BeEquivalentTo(3))
			Expect(oid(it)).To(Equal(marker + "__multipart_" + prefix + ".3"))
		})
	})

	Describe("an explicit manifest", func() {
		// Two pieces: the head holds 1 MiB, a shadow piece holds the next 1 MiB from loc_ofs 512.
		build := func() meta.Manifest {
			head := meta.Obj{Bucket: meta.BucketID{Name: "plain", Marker: "m1"}, Key: meta.ObjKey{Name: "k"}}
			shadow := meta.Obj{Bucket: head.Bucket, Key: meta.ObjKey{Name: "s1", NS: meta.NSShadow}}
			m := meta.NewManifest()
			m.ExplicitObjs = true
			m.Obj = head
			m.ObjSize = 2 << 20
			m.HeadSize = 1 << 20
			m.MaxHeadSize = 1 << 20
			m.Objs = map[uint64]meta.ManifestPart{
				0:       {Loc: head, Size: 1 << 20},
				1 << 20: {Loc: shadow, LocOfs: 512, Size: 1 << 20},
			}
			return m
		}
		It("reports the piece's loc_ofs and size and has a tail", func() {
			m := build()
			Expect(m.HasTail()).To(BeTrue())
			it, err := m.Seek(1<<20 + 100)
			Expect(err).NotTo(HaveOccurred())
			Expect(it.StripeOfs()).To(BeEquivalentTo(1 << 20))
			Expect(it.StripeSize()).To(BeEquivalentTo(1 << 20))
			Expect(it.LocOfs()).To(BeEquivalentTo(512))
			obj, _, inHead := it.Location()
			Expect(inHead).To(BeFalse())
			Expect(obj.Key.Name).To(Equal("s1"))
			Expect(it.Next()).To(Succeed())
			Expect(it.Done()).To(BeTrue())
		})
		It("a single explicit piece that is the head has no tail", func() {
			m := build()
			delete(m.Objs, 1<<20)
			m.ObjSize = 1 << 20
			Expect(m.HasTail()).To(BeFalse())
		})
	})

	It("a head-only rule manifest has no tail and one stripe", func() {
		m := meta.NewManifest()
		m.Obj = meta.Obj{Bucket: meta.BucketID{Name: "plain", Marker: "m1"}, Key: meta.ObjKey{Name: "k"}}
		m.ObjSize, m.HeadSize, m.MaxHeadSize = 1024, 1024, 4<<20
		Expect(m.HasTail()).To(BeFalse())
		it, err := m.Seek(0)
		Expect(err).NotTo(HaveOccurred())
		_, _, inHead := it.Location()
		Expect(inHead).To(BeTrue())
		Expect(it.StripeSize()).To(BeEquivalentTo(1024))
	})
})
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test -tags=ceph_preview ./internal/meta/ 2>&1 | head -20`
Expected: compile errors, `m.Seek undefined`, `meta.StripeIter undefined`.

- [ ] **Step 3: Write `attrs_read.go` and `manifest_read.go`**

`attrs_read.go` holds the constant block and `RestoreStatus` exactly as in Interfaces, with the comment naming rgw_common.h. `manifest_read.go`:

```go
package meta

// StripeIter is RGWObjManifest::obj_iterator: one position in the stripes a
// manifest lays out, moved by Seek and Next. The driver reads a byte range by
// seeking to its first stripe and advancing until the range is covered, so
// only the stripes the range touches are visited.
type StripeIter struct {
	it *manifestIter
	m  *Manifest
}

// Seek is obj_find: an iterator at the stripe holding ofs, or at the end when
// ofs is at or past ObjSize.
func (m *Manifest) Seek(ofs uint64) (*StripeIter, error) {
	it, err := newManifestIter(m, min(ofs, m.ObjSize))
	if err != nil {
		return nil, err
	}
	return &StripeIter{it: it, m: m}, nil
}

// Done reports obj_end: the iterator's offset is the object's size.
func (it *StripeIter) Done() bool { return it.it.ofs == it.m.ObjSize }

// Ofs is get_ofs, the current offset within the object.
func (it *StripeIter) Ofs() uint64 { return it.it.ofs }

// StripeOfs is get_stripe_ofs, where the current stripe starts.
func (it *StripeIter) StripeOfs() uint64 { return it.it.stripeStart() }

// StripeSize is get_stripe_size: the current piece's size for an explicit
// manifest, else the rule-derived stripe size, which the seek already trims
// to the object for the last stripe.
func (it *StripeIter) StripeSize() uint64 {
	if it.m.ExplicitObjs {
		return it.m.Objs[it.it.objKeys[it.it.explicit]].Size
	}
	return it.it.stripeSize
}

// LocOfs is location_ofs: where the data starts within the stripe's object,
// non-zero only for an explicit manifest's pieces.
func (it *StripeIter) LocOfs() uint64 {
	if it.m.ExplicitObjs {
		return it.m.Objs[it.it.objKeys[it.it.explicit]].LocOfs
	}
	return 0
}

// PartID is get_cur_part_id: 0 outside a multipart manifest.
func (it *StripeIter) PartID() int32 { return it.it.curPartID }

// Location is get_location: the stripe's object, the placement rule that
// selects its pool (empty for an explicit piece, as the C++ leaves it), and
// whether the object is the head.
func (it *StripeIter) Location() (obj Obj, placement PlacementRule, inHead bool) {
	loc := it.it.location
	return loc.obj, loc.placement, sameBucket(loc.obj.Bucket, it.m.Obj.Bucket) && loc.obj.Key == it.m.Obj.Key
}

// Next is operator++. Past the end it is a no-op, as the C++ is.
func (it *StripeIter) Next() error { return it.it.next() }

// HasTail is RGWObjManifest::has_tail (rgw_obj_manifest.h:394-404).
func (m *Manifest) HasTail() bool {
	if m.ExplicitObjs {
		if len(m.Objs) == 1 {
			for _, p := range m.Objs {
				return !(sameBucket(p.Loc.Bucket, m.Obj.Bucket) && p.Loc.Key == m.Obj.Key)
			}
		}
		return len(m.Objs) >= 2
	}
	return m.ObjSize > m.HeadSize
}

// PartsCount is the parts count radosgw derives from the end iterator
// (rgw_rados.cc get_part_obj_state): 0 for a manifest that is not multipart,
// 1 for a single-part upload whose last part id is off by one, else
// endPartID-1.
func (m *Manifest) PartsCount() (int, error) {
	end, err := m.Seek(m.ObjSize)
	if err != nil {
		return 0, err
	}
	last := int(end.PartID())
	if last == 0 {
		return 0, nil
	}
	return max(1, last-1), nil
}

// PartBounds is obj_find_part plus the extent get_part_obj_state measures: the
// object offset and size of part n and the object holding its first stripe,
// which is the part's head. ok is false when the manifest is not multipart or
// has no part n.
func (m *Manifest) PartBounds(n int) (ofs, size uint64, head Obj, ok bool, err error) {
	if n <= 0 {
		return 0, 0, Obj{}, false, nil
	}
	it, err := m.Seek(0)
	if err != nil {
		return 0, 0, Obj{}, false, err
	}
	if it.Done() {
		return 0, 0, Obj{}, false, nil
	}
	end, err := m.Seek(m.ObjSize)
	if err != nil {
		return 0, 0, Obj{}, false, err
	}
	if end.PartID() == 0 { // not multipart
		return 0, 0, Obj{}, false, nil
	}
	for !it.Done() && int(it.PartID()) < n {
		if err := it.Next(); err != nil {
			return 0, 0, Obj{}, false, err
		}
	}
	if it.Done() || int(it.PartID()) != n {
		return 0, 0, Obj{}, false, nil
	}
	ofs = it.Ofs()
	head, _, _ = it.Location()
	for !it.Done() && int(it.PartID()) == n {
		if err := it.Next(); err != nil {
			return 0, 0, Obj{}, false, err
		}
	}
	return ofs, it.Ofs() - ofs, head, true, nil
}
```

`newManifestIter` guards its map indexes already (`denc.ErrMalformed` where the C++ would read past a map); `StripeSize`/`LocOfs` index `objKeys[explicit]` only when `!Done()` for an explicit manifest — add `if it.Done() { return 0 }` at the top of both, since `explicit == len(objKeys)` at the end.

- [ ] **Step 4: Run the specs; expected PASS. Run the whole meta suite (the goldens still round-trip) and the lint**

Run: `go test -tags=ceph_preview ./internal/meta/ && golangci-lint run ./internal/meta/`
Expected: PASS, no findings.

- [ ] **Step 5: Commit**

```sh
git add internal/meta/attrs_read.go internal/meta/manifest_read.go internal/meta/manifest_read_test.go
git commit -m "feat(meta): add the ranged manifest iterator, part helpers and read-side attr names"
```

Open the draft PR `feat(meta): ranged manifest iterator for the read path (unit R task 1)`.

---

### Task 2: `compression`: the four decoders in Ceph framing and the block map

**Files:**
- Create: `internal/compression/doc.go`, `internal/compression/codec.go`, `internal/compression/zlib.go`, `internal/compression/snappy.go`, `internal/compression/zstd.go`, `internal/compression/lz4.go`, `internal/compression/blocks.go`, `internal/compression/compression_suite_test.go`, `internal/compression/codec_test.go`, `internal/compression/blocks_test.go`
- Modify: `go.mod`, `go.sum` (`go get github.com/klauspost/compress@v1.19.1 github.com/pierrec/lz4/v4@v4.1.18`, sandbox disabled), `docs/cgo-limitations.md` is NOT touched (no cgo here)

**Interfaces:**
- Consumes: `meta.CompressionBlock{OldOfs, NewOfs, Len}` (the block list of `meta.CompressionInfo`), `meta.CompressionInfo.CompressorMessage`.
- Produces:

```go
package compression

// Codec names as Compressor::get_comp_alg_name spells them (src/compressor/Compressor.h:50-58);
// they are the compression_type strings RGWCompressionInfo stores.
const (
	None   = "none"
	Snappy = "snappy"
	Zlib   = "zlib"
	Zstd   = "zstd"
	LZ4    = "lz4"
)

var (
	ErrUnknownCodec = errors.New("compression: unknown codec") // radosgw: "Cannot load compressor of type", -EIO
	ErrCorrupt      = errors.New("compression: corrupt block")  // a decoder failure, radosgw's -1/-2 → the GET fails
)

// Decoder decodes one stored block — one compression_block of an object's
// RGWCompressionInfo, an independent unit of compression — into dst's backing
// array (grown when it does not fit) and returns the decoded bytes. message is
// the object's compressor_message, which only zlib reads (its window bits).
type Decoder interface {
	Decode(dst, block []byte, message *int32) ([]byte, error)
}

// NewDecoder returns the decoder for codec, or ErrUnknownCodec ("none" included:
// the caller never decodes an uncompressed object).
func NewDecoder(codec string) (Decoder, error)

// Window is what RGWGetObj_Decompress::fixup_range derives for a decompressed
// range: which blocks to read, the compressed byte range that holds them, and
// how many decoded bytes to skip and deliver.
type Window struct {
	First, Last      int    // block indexes, inclusive
	QOfs, QLen       uint64 // decoded bytes to skip at the start of First; decoded bytes to deliver
	CompOfs, CompEnd uint64 // compressed range to read, inclusive end as the C++ keeps it
}

// Range is fixup_range (rgw_compression.cc:183-218): with partial set the
// blocks holding [ofs, end] are chosen by old_ofs, else every block.
func Range(blocks []meta.CompressionBlock, partial bool, ofs, end uint64) (Window, error)

// Stream is RGWGetObj_Decompress::handle_data: compressed bytes arrive in
// order through Write, complete blocks are decoded as they become whole, and
// the decoded bytes of the window are written to w. Buffers are reused across
// blocks. Close reports a short stream.
type Stream struct{ /* dec, blocks, win, message, w, cur, pending []byte, out []byte, next int, skipped, delivered uint64 */ }
func NewStream(dec Decoder, blocks []meta.CompressionBlock, win Window, message *int32, w io.Writer) *Stream
func (s *Stream) Write(p []byte) (int, error)
func (s *Stream) Close() error
```

Framing, transcribed from `src/compressor` at v19.2.6 (the v20.2.4 diff touches only QAT/UADK plumbing and an lz4 copy path, never the bytes):

- **zlib** ([`ZlibCompressor.cc:44-96`](https://github.com/ceph/ceph/blob/v19.2.6/src/compressor/zlib/ZlibCompressor.cc#L44-L96), `:214-290`): the stored block is one prefix byte (`0` for zlib, `1` for isal, written at [`:75`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_compression.cc#L75)/[`:123`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_compression.cc#L123)) followed by a deflate stream; the decoder skips the byte (`avail_in = len - begin`) and calls `inflateInit2(windowBits)` with `windowBits = compressor_message` or `-15` when absent (`ZLIB_DEFAULT_WIN_SIZE`). `compressor_zlib_winsize` is `-15` by default, min -15, max 32 ([global.yaml.in:775-781](https://github.com/ceph/ceph/blob/v19.2.6/src/common/options/global.yaml.in#L775-L781)). Window bits select the wrapper exactly as zlib does: `< 0` raw deflate, `8..15` zlib wrapper, `16..31` gzip wrapper, `32+` automatic zlib/gzip detection. Concatenated members are decoded to the end of the block (the C++ loops `inflate` until the input is consumed, tolerating `Z_BUF_ERROR`).
- **snappy** ([`SnappyCompressor.h:41-71`](https://github.com/ceph/ceph/blob/v19.2.6/src/compressor/snappy/SnappyCompressor.h#L41-L71)): the raw snappy block format, `GetUncompressedLength` + `RawUncompress`, no extra framing.
- **zstd** ([`ZstdCompressor.h:11-75`](https://github.com/ceph/ceph/blob/v19.2.6/src/compressor/zstd/ZstdCompressor.h#L11-L75)): a little-endian `uint32` of the uncompressed length, then one zstd frame (`ZSTD_compressStream2 … ZSTD_e_end`); the decoder checks `compressed_len >= 4`, reads the length, streams the rest.
- **lz4** ([`LZ4Compressor.cc:19-58`](https://github.com/ceph/ceph/blob/v19.2.6/src/compressor/lz4/LZ4Compressor.cc#L19-L58), [`:70-115`](https://github.com/ceph/ceph/blob/v19.2.6/src/compressor/lz4/LZ4Compressor.cc#L70-L115)): `uint32 count`, then `count` pairs of (`uint32 origin_len`, `uint32 compressed_len`), then the concatenated LZ4 blocks; radosgw always rebuilds the input contiguous first, so `count` is 1 for every block it writes, but the blocks of a `count > 1` header were compressed on one `LZ4_stream_t` and are decoded with `LZ4_decompress_safe_continue`, i.e. each block may reference the previous blocks' output — `lz4.UncompressBlockWithDict(src, dst, dict)` with `dict` = the output decoded so far.

- [ ] **Step 1: Add the modules**

```sh
go get github.com/klauspost/compress@v1.19.1 github.com/pierrec/lz4/v4@v4.1.18   # sandbox disabled: ~/go/pkg/mod is read-only inside it
go mod tidy
```
Expected: both appear in `go.mod`'s first `require` block once a package imports them (after Step 4); until then `go mod tidy` drops them — run it again after Step 4.

- [ ] **Step 2: Write the failing codec specs**

`compression_suite_test.go` is the canonical bootstrap (`RandomizeAllSpecs`, `FailOnPending`). `codec_test.go` encodes fixtures with Go encoders in Ceph's framing — the layout assertions pin the framing, the round trips pin the decoders:

```go
package compression_test

import (
	"bytes"
	"encoding/binary"
	"math/rand/v2"

	"github.com/klauspost/compress/flate"
	"github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/snappy"
	"github.com/klauspost/compress/zlib"
	"github.com/klauspost/compress/zstd"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/pierrec/lz4/v4"

	"github.com/jhoblitt/rgw-go/internal/compression"
)

// plaintext is compressible and seeded, like populate.sh's payload_compressible.
func plaintext(n int) []byte {
	r := rand.New(rand.NewPCG(1, 2))
	out := make([]byte, n)
	words := []string{"alpha ", "beta ", "gamma ", "delta "}
	for i := 0; i < n; {
		i += copy(out[i:], words[r.IntN(len(words))])
	}
	return out
}

// Encoders in Ceph's framing (src/compressor at v19.2.6).
func cephZlib(src []byte, winBits int) []byte {
	var b bytes.Buffer
	b.WriteByte(0) // the prefix byte ZlibCompressor::zlib_compress writes (:75)
	switch {
	case winBits < 0:
		w, err := flate.NewWriter(&b, 5)
		Expect(err).NotTo(HaveOccurred())
		_, _ = w.Write(src)
		Expect(w.Close()).To(Succeed())
	case winBits <= 15:
		w := zlib.NewWriter(&b)
		_, _ = w.Write(src)
		Expect(w.Close()).To(Succeed())
	default:
		w := gzip.NewWriter(&b)
		_, _ = w.Write(src)
		Expect(w.Close()).To(Succeed())
	}
	return b.Bytes()
}
func cephSnappy(src []byte) []byte { return snappy.Encode(nil, src) }
func cephZstd(src []byte) []byte {
	enc, err := zstd.NewWriter(nil)
	Expect(err).NotTo(HaveOccurred())
	defer enc.Close()
	out := binary.LittleEndian.AppendUint32(nil, uint32(len(src)))
	return enc.EncodeAll(src, out)
}
func cephLZ4(chunks ...[]byte) []byte {
	out := binary.LittleEndian.AppendUint32(nil, uint32(len(chunks)))
	var body []byte
	var c lz4.Compressor
	for _, ch := range chunks {
		dst := make([]byte, lz4.CompressBlockBound(len(ch)))
		n, err := c.CompressBlock(ch, dst)
		Expect(err).NotTo(HaveOccurred())
		Expect(n).NotTo(BeZero(), "incompressible fixture")
		out = binary.LittleEndian.AppendUint32(out, uint32(len(ch)))
		out = binary.LittleEndian.AppendUint32(out, uint32(n))
		body = append(body, dst[:n]...)
	}
	return append(out, body...)
}

var _ = Describe("Decoder", func() {
	src := plaintext(1 << 20)

	DescribeTable("decodes a block written in Ceph's framing",
		func(codec string, block []byte, message *int32) {
			dec, err := compression.NewDecoder(codec)
			Expect(err).NotTo(HaveOccurred())
			got, err := dec.Decode(nil, block, message)
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(src))
		},
		Entry("zlib, raw deflate, no message (the default -15)", compression.Zlib, cephZlib(src, -15), nil),
		Entry("zlib, raw deflate, message -15", compression.Zlib, cephZlib(src, -15), ptr(int32(-15))),
		Entry("zlib, zlib wrapper, message 15", compression.Zlib, cephZlib(src, 15), ptr(int32(15))),
		Entry("zlib, gzip wrapper, message 31", compression.Zlib, cephZlib(src, 31), ptr(int32(31))),
		Entry("zlib, automatic detection, message 47 finds gzip", compression.Zlib, cephZlib(src, 31), ptr(int32(47))),
		Entry("snappy", compression.Snappy, cephSnappy(src), nil),
		Entry("zstd", compression.Zstd, cephZstd(src), nil),
		Entry("lz4, one block", compression.LZ4, cephLZ4(src), nil),
		Entry("lz4, two blocks", compression.LZ4, cephLZ4(src[:600000], src[600000:]), nil),
	)

	It("reuses dst when it is large enough and grows it otherwise", func() {
		dec, _ := compression.NewDecoder(compression.Snappy)
		buf := make([]byte, 0, 2<<20)
		got, err := dec.Decode(buf, cephSnappy(src), nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(cap(got)).To(Equal(cap(buf)), "decoded into the caller's buffer")
		got, err = dec.Decode(make([]byte, 0, 16), cephSnappy(src), nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(src))
	})

	DescribeTable("refuses what radosgw's decompress refuses",
		func(codec string, block []byte) {
			dec, err := compression.NewDecoder(codec)
			Expect(err).NotTo(HaveOccurred())
			_, err = dec.Decode(nil, block, nil)
			Expect(err).To(MatchError(compression.ErrCorrupt))
		},
		Entry("zstd shorter than its length header", compression.Zstd, []byte{1, 2, 3}),
		Entry("zstd whose frame decodes to a different length", compression.Zstd, append(binary.LittleEndian.AppendUint32(nil, 5), cephZstd(src)[4:]...)),
		Entry("lz4 with a truncated pair table", compression.LZ4, []byte{2, 0, 0, 0, 1, 0, 0, 0}),
		Entry("lz4 whose block decodes short", compression.LZ4, func() []byte {
			b := cephLZ4(src)
			binary.LittleEndian.PutUint32(b[4:], uint32(len(src)+1)) // origin_len lies
			return b
		}()),
		Entry("snappy garbage", compression.Snappy, []byte{0xff, 0xff, 0xff}),
		Entry("zlib garbage after the prefix", compression.Zlib, []byte{0, 0xff, 0xff, 0xff, 0xff}),
	)

	It("knows no other codec", func() {
		_, err := compression.NewDecoder("none")
		Expect(err).To(MatchError(compression.ErrUnknownCodec))
		_, err = compression.NewDecoder("brotli")
		Expect(err).To(MatchError(compression.ErrUnknownCodec))
	})

	Describe("the framing the fixtures pin", func() {
		It("zlib starts with the prefix byte", func() { Expect(cephZlib([]byte("x"), -15)[0]).To(BeZero()) })
		It("zstd starts with the little-endian length", func() {
			Expect(binary.LittleEndian.Uint32(cephZstd(src)[:4])).To(BeEquivalentTo(len(src)))
		})
		It("lz4 starts with count and one (origin, compressed) pair", func() {
			b := cephLZ4(src)
			Expect(binary.LittleEndian.Uint32(b[0:4])).To(BeEquivalentTo(1))
			Expect(binary.LittleEndian.Uint32(b[4:8])).To(BeEquivalentTo(len(src)))
			Expect(binary.LittleEndian.Uint32(b[8:12])).To(BeEquivalentTo(len(b) - 12))
		})
	})
})

func ptr[T any](v T) *T { return &v }
```

- [ ] **Step 3: Write the failing block-map and stream specs**

`blocks_test.go`:

```go
var _ = Describe("Range", func() {
	// three blocks of a 10 MiB object compressed 4 MiB at a time, as RGWPutObj_Compress lays them out
	blocks := []meta.CompressionBlock{
		{OldOfs: 0, NewOfs: 0, Len: 100},
		{OldOfs: 4 << 20, NewOfs: 100, Len: 90},
		{OldOfs: 8 << 20, NewOfs: 190, Len: 80},
	}
	DescribeTable("selects the blocks fixup_range selects",
		func(partial bool, ofs, end uint64, want compression.Window) {
			got, err := compression.Range(blocks, partial, ofs, end)
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(want))
		},
		Entry("the whole object, not partial", false, uint64(0), uint64(10<<20-1),
			compression.Window{First: 0, Last: 2, QOfs: 0, QLen: 10 << 20, CompOfs: 0, CompEnd: 269}),
		Entry("a range inside the first block", true, uint64(0), uint64(10),
			compression.Window{First: 0, Last: 0, QOfs: 0, QLen: 11, CompOfs: 0, CompEnd: 99}),
		Entry("a range across the last two blocks", true, uint64(5<<20), uint64(9<<20),
			compression.Window{First: 1, Last: 2, QOfs: 1 << 20, QLen: 4<<20 + 1, CompOfs: 100, CompEnd: 269}),
		Entry("a range that ends on a block boundary", true, uint64(4<<20-1), uint64(4<<20),
			compression.Window{First: 0, Last: 1, QOfs: 4<<20 - 1, QLen: 2, CompOfs: 0, CompEnd: 189}),
		Entry("a suffix range in the last block", true, uint64(10<<20-7), uint64(10<<20-1),
			compression.Window{First: 2, Last: 2, QOfs: 2<<20 - 7, QLen: 7, CompOfs: 190, CompEnd: 269}),
	)
	It("refuses an empty block list, as rgw_compression_info_from_attr does (-EIO)", func() {
		_, err := compression.Range(nil, false, 0, 0)
		Expect(err).To(MatchError(compression.ErrCorrupt))
	})
})

var _ = Describe("Stream", func() {
	src := plaintext(10 << 20)
	// compress in 4 MiB chunks, building the block list as RGWPutObj_Compress::process does (:68-73)
	var blocks []meta.CompressionBlock
	var comp []byte
	for ofs := 0; ofs < len(src); ofs += 4 << 20 {
		chunk := src[ofs:min(ofs+4<<20, len(src))]
		b := cephSnappy(chunk)
		blocks = append(blocks, meta.CompressionBlock{OldOfs: uint64(ofs), NewOfs: uint64(len(comp)), Len: uint64(len(b))})
		comp = append(comp, b...)
	}
	dec, _ := compression.NewDecoder(compression.Snappy)

	feed := func(s *compression.Stream, data []byte, sizes ...int) {
		GinkgoHelper()
		for i, k := 0, 0; i < len(data); k++ {
			n := sizes[k%len(sizes)]
			n = min(n, len(data)-i)
			wrote, err := s.Write(data[i : i+n])
			Expect(err).NotTo(HaveOccurred())
			Expect(wrote).To(Equal(n))
			i += n
		}
		Expect(s.Close()).To(Succeed())
	}
	It("delivers the whole object from arbitrarily split compressed pieces", func() {
		win, err := compression.Range(blocks, false, 0, uint64(len(src)-1))
		Expect(err).NotTo(HaveOccurred())
		var out bytes.Buffer
		feed(compression.NewStream(dec, blocks, win, nil, &out), comp, 1, 7, 4097, 1<<20)
		Expect(out.Bytes()).To(Equal(src))
	})
	It("delivers exactly a range spanning two blocks, trimmed by QOfs and QLen", func() {
		ofs, end := uint64(5<<20-3), uint64(8<<20+5)
		win, err := compression.Range(blocks, true, ofs, end)
		Expect(err).NotTo(HaveOccurred())
		var out bytes.Buffer
		feed(compression.NewStream(dec, blocks, win, nil, &out), comp[win.CompOfs:win.CompEnd+1], 4096)
		Expect(out.Bytes()).To(Equal(src[ofs : end+1]))
	})
	It("reports a stream that ends before the last block is whole", func() {
		win, _ := compression.Range(blocks, false, 0, uint64(len(src)-1))
		s := compression.NewStream(dec, blocks, win, nil, io.Discard)
		_, err := s.Write(comp[:len(comp)-10])
		Expect(err).NotTo(HaveOccurred())
		Expect(s.Close()).To(MatchError(io.ErrUnexpectedEOF))
	})
	It("fails on the first corrupt block and writes nothing after it", func() {
		bad := slices.Clone(comp)
		bad[blocks[1].NewOfs+3] ^= 0xff
		win, _ := compression.Range(blocks, false, 0, uint64(len(src)-1))
		var out bytes.Buffer
		s := compression.NewStream(dec, blocks, win, nil, &out)
		_, err := s.Write(bad)
		Expect(err).To(MatchError(compression.ErrCorrupt))
		Expect(out.Len()).To(Equal(4 << 20), "the first block was delivered before the second failed")
	})
})
```

- [ ] **Step 4: Run to verify they fail; then implement**

Run: `go test -tags=ceph_preview ./internal/compression/ 2>&1 | head`; expected compile errors on the package.

`codec.go`:

```go
package compression

const (
	None   = "none"
	Snappy = "snappy"
	Zlib   = "zlib"
	Zstd   = "zstd"
	LZ4    = "lz4"
)

var (
	ErrUnknownCodec = errors.New("compression: unknown codec")
	ErrCorrupt      = errors.New("compression: corrupt block")
)

type Decoder interface {
	Decode(dst, block []byte, message *int32) ([]byte, error)
}

func NewDecoder(codec string) (Decoder, error) {
	switch codec {
	case Zlib:
		return zlibDecoder{}, nil
	case Snappy:
		return snappyDecoder{}, nil
	case Zstd:
		return newZstdDecoder()
	case LZ4:
		return lz4Decoder{}, nil
	}
	return nil, fmt.Errorf("%w: %q", ErrUnknownCodec, codec)
}

// grow returns dst[:0] with at least n bytes of capacity.
func grow(dst []byte, n int) []byte {
	if cap(dst) >= n {
		return dst[:0]
	}
	return make([]byte, 0, n)
}
```

`zlib.go`:

```go
// zlibDecoder is ZlibCompressor::decompress (ZlibCompressor.cc:214-290): skip
// the prefix byte, inflate with the stored window bits (-15 when absent), to the
// end of the block, member after member.
type zlibDecoder struct{}

const zlibDefaultWinSize = -15 // ZLIB_DEFAULT_WIN_SIZE

func (zlibDecoder) Decode(dst, block []byte, message *int32) ([]byte, error) {
	if len(block) < 1 {
		return nil, fmt.Errorf("%w: zlib block shorter than its prefix byte", ErrCorrupt)
	}
	wb := int32(zlibDefaultWinSize)
	if message != nil {
		wb = *message
	}
	in := block[1:]
	buf := bytes.NewBuffer(grow(dst, 4*len(in)))
	for len(in) > 0 {
		var rc io.ReadCloser
		var err error
		r := bytes.NewReader(in)
		switch {
		case wb < 0:
			rc = flate.NewReader(r)
		case wb <= 15:
			rc, err = zlib.NewReader(r)
		case wb <= 31:
			var gr *gzip.Reader
			gr, err = gzip.NewReader(r)
			if err == nil {
				gr.Multistream(false)
				rc = gr
			}
		default: // 32+: automatic header detection, as inflateInit2 does
			if len(in) >= 2 && in[0] == 0x1f && in[1] == 0x8b {
				var gr *gzip.Reader
				gr, err = gzip.NewReader(r)
				if err == nil {
					gr.Multistream(false)
					rc = gr
				}
			} else {
				rc, err = zlib.NewReader(r)
			}
		}
		if err != nil {
			return nil, fmt.Errorf("%w: zlib: %v", ErrCorrupt, err)
		}
		if _, err := buf.ReadFrom(rc); err != nil {
			return nil, fmt.Errorf("%w: zlib: %v", ErrCorrupt, err)
		}
		in = in[len(in)-r.Len():] // whatever the member left unread starts the next one
		if r.Len() == len(in) {   // no progress: trailing garbage is what zlib calls Z_DATA_ERROR
			return nil, fmt.Errorf("%w: zlib: %d trailing bytes", ErrCorrupt, r.Len())
		}
	}
	return buf.Bytes(), nil
}
```

(`bytes.Reader.Len` after the member's `Close`-less read reports the unread remainder because klauspost's readers consume exactly one member from the underlying `io.Reader` when it is a `flate.Reader`-compatible `io.ByteReader`; `bytes.Reader` is one.)

`snappy.go`:

```go
type snappyDecoder struct{}

func (snappyDecoder) Decode(dst, block []byte, _ *int32) ([]byte, error) {
	n, err := snappy.DecodedLen(block)
	if err != nil {
		return nil, fmt.Errorf("%w: snappy: %v", ErrCorrupt, err)
	}
	out, err := snappy.Decode(grow(dst, n)[:n], block)
	if err != nil {
		return nil, fmt.Errorf("%w: snappy: %v", ErrCorrupt, err)
	}
	return out, nil
}
```

`zstd.go`:

```go
// zstdDecoder is ZstdCompressor::decompress (ZstdCompressor.h:44-75): a
// little-endian uint32 of the decoded length, then one frame. One
// zstd.Decoder (no concurrency, DecodeAll only) is shared; it is goroutine-safe
// for DecodeAll.
type zstdDecoder struct{ d *zstd.Decoder }

func newZstdDecoder() (Decoder, error) {
	d, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(0), zstd.WithDecoderMaxMemory(64<<20))
	if err != nil {
		return nil, err
	}
	return zstdDecoder{d: d}, nil
}

func (z zstdDecoder) Decode(dst, block []byte, _ *int32) ([]byte, error) {
	if len(block) < 4 {
		return nil, fmt.Errorf("%w: zstd block shorter than its length header", ErrCorrupt)
	}
	want := binary.LittleEndian.Uint32(block[:4])
	out, err := z.d.DecodeAll(block[4:], grow(dst, int(want)))
	if err != nil {
		return nil, fmt.Errorf("%w: zstd: %v", ErrCorrupt, err)
	}
	if uint32(len(out)) != want {
		return nil, fmt.Errorf("%w: zstd decoded %d bytes, header says %d", ErrCorrupt, len(out), want)
	}
	return out, nil
}
```

`lz4.go`:

```go
// lz4Decoder is LZ4Compressor::decompress (LZ4Compressor.cc:70-115): a uint32
// count, count (origin_len, compressed_len) pairs, then the blocks, each
// decoded with the previous output as its dictionary (LZ4_decompress_safe_continue).
type lz4Decoder struct{}

func (lz4Decoder) Decode(dst, block []byte, _ *int32) ([]byte, error) {
	if len(block) < 4 {
		return nil, fmt.Errorf("%w: lz4 block shorter than its count", ErrCorrupt)
	}
	count := binary.LittleEndian.Uint32(block[:4])
	hdr := 4 + 8*uint64(count)
	if uint64(len(block)) < hdr {
		return nil, fmt.Errorf("%w: lz4 pair table truncated", ErrCorrupt)
	}
	var total uint64
	pairs := make([][2]uint32, count) // (origin, compressed)
	for i := range pairs {
		pairs[i][0] = binary.LittleEndian.Uint32(block[4+8*i:])
		pairs[i][1] = binary.LittleEndian.Uint32(block[8+8*i:])
		total += uint64(pairs[i][0])
	}
	if total > uint64(math.MaxInt32) {
		return nil, fmt.Errorf("%w: lz4 origin length %d", ErrCorrupt, total)
	}
	out := grow(dst, int(total))[:total]
	in := block[hdr:]
	var written int
	for _, p := range pairs {
		if uint64(len(in)) < uint64(p[1]) {
			return nil, fmt.Errorf("%w: lz4 compressed data truncated", ErrCorrupt)
		}
		n, err := lz4.UncompressBlockWithDict(in[:p[1]], out[written:written+int(p[0])], out[:written])
		if err != nil || n != int(p[0]) {
			return nil, fmt.Errorf("%w: lz4: decoded %d of %d bytes: %v", ErrCorrupt, n, p[0], err)
		}
		written += n
		in = in[p[1]:]
	}
	return out, nil
}
```

`blocks.go`:

```go
type Window struct {
	First, Last      int
	QOfs, QLen       uint64
	CompOfs, CompEnd uint64
}

// Range is RGWGetObj_Decompress::fixup_range (rgw_compression.cc:183-218).
func Range(blocks []meta.CompressionBlock, partial bool, ofs, end uint64) (Window, error) {
	if len(blocks) == 0 {
		return Window{}, fmt.Errorf("%w: no compression blocks", ErrCorrupt) // rgw_compression_info_from_attr: -EIO
	}
	w := Window{First: 0, Last: len(blocks) - 1}
	if partial {
		w.First, w.Last = 0, 0
		if len(blocks) > 1 {
			// upper_bound over blocks[1:] by old_ofs, then one back
			fb := 1 + sort.Search(len(blocks)-1, func(i int) bool { return ofs < blocks[1+i].OldOfs })
			w.First = fb - 1
			// lower_bound from fb with "old_ofs <= end" as the ordering predicate, then one back
			lb := fb + sort.Search(len(blocks)-fb, func(i int) bool { return !(blocks[fb+i].OldOfs <= end) })
			w.Last = lb - 1
		}
	}
	w.QOfs = ofs - blocks[w.First].OldOfs
	w.QLen = end + 1 - ofs
	w.CompOfs = blocks[w.First].NewOfs
	w.CompEnd = blocks[w.Last].NewOfs + blocks[w.Last].Len - 1
	return w, nil
}

// Stream is RGWGetObj_Decompress::handle_data (rgw_compression.cc:107-181).
type Stream struct {
	dec     Decoder
	blocks  []meta.CompressionBlock
	win     Window
	message *int32
	w       io.Writer
	next    int    // index of the block being assembled
	cur     uint64 // compressed offset of pending[0]
	pending []byte // bytes of the current block received so far (reused)
	out     []byte // decoded block (reused)
	skip    uint64 // QOfs still to skip
	left    uint64 // QLen still to deliver
}

func NewStream(dec Decoder, blocks []meta.CompressionBlock, win Window, message *int32, w io.Writer) *Stream {
	return &Stream{dec: dec, blocks: blocks, win: win, message: message, w: w, next: win.First, cur: win.CompOfs, skip: win.QOfs, left: win.QLen}
}

func (s *Stream) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 && s.next <= s.win.Last {
		b := s.blocks[s.next]
		need := b.NewOfs + b.Len - (s.cur + uint64(len(s.pending)))
		take := min(uint64(len(p)), need)
		if len(s.pending) == 0 && take == b.Len {
			// a whole block in one piece: decode without copying it aside
			if err := s.emit(p[:take]); err != nil {
				return 0, err
			}
		} else {
			s.pending = append(s.pending, p[:take]...)
			if uint64(len(s.pending)) == b.Len {
				if err := s.emit(s.pending); err != nil {
					return 0, err
				}
				s.pending = s.pending[:0]
			}
		}
		p = p[take:]
		s.cur += take
		if s.cur == b.NewOfs+b.Len {
			s.next++
		}
	}
	if len(p) > 0 {
		return n - len(p), fmt.Errorf("%w: %d bytes past the last block", ErrCorrupt, len(p))
	}
	return n, nil
}

// emit decodes one whole block and writes the part of it inside the window.
func (s *Stream) emit(block []byte) error {
	out, err := s.dec.Decode(s.out, block, s.message)
	if err != nil {
		return err
	}
	s.out = out[:0]
	if s.skip >= uint64(len(out)) {
		s.skip -= uint64(len(out))
		return nil
	}
	out = out[s.skip:]
	s.skip = 0
	if uint64(len(out)) > s.left {
		out = out[:s.left]
	}
	if len(out) == 0 {
		return nil
	}
	if _, err := s.w.Write(out); err != nil {
		return err
	}
	s.left -= uint64(len(out))
	return nil
}

// Close reports a stream that ended inside a block or before the window was delivered.
func (s *Stream) Close() error {
	if s.next <= s.win.Last || len(s.pending) > 0 || s.left > 0 {
		return io.ErrUnexpectedEOF
	}
	return nil
}
```

`doc.go` states the package's scope in three sentences: the decoders for the four codecs radosgw may have used, in the per-block framing of `src/compressor`, and the block map of `RGWGetObj_Decompress`; nothing here compresses (phase 2 adds the writers).

- [ ] **Step 5: Run the suite, tidy, lint**

Run: `go mod tidy && go test -tags=ceph_preview -race ./internal/compression/ && golangci-lint run ./internal/compression/`
Expected: PASS; `go.mod` requires `github.com/klauspost/compress v1.19.1` and `github.com/pierrec/lz4/v4 v4.1.18`.

- [ ] **Step 6: Commit**

```sh
git add go.mod go.sum internal/compression
git commit -m "feat(compression): decode zlib, snappy, zstd and lz4 blocks in Ceph's framing"
```

Open the draft PR `feat(compression): the four read-side codecs (unit R task 2)`; its description names decision D3 (`pierrec/lz4/v4`, on spec §4's dependency list as amended on main).

---

### Task 3: `driver`: pool resolution, the composed head op, `StatObject`, `PrefetchObject`; contract additions

**Files:**
- Modify: `internal/op/object.go` (`ObjectState.Head`, `ObjectStore.PrefetchObject`), `internal/memstore/object.go` (`PrefetchObject`), `internal/testutil/fakerados/pool.go` (`ReadStep` honours `Buf`), `internal/radosclient/cluster.go` (`//counterfeiter:generate . Pool` beside G's `Cluster` directive, unless W's Task 1 already added it)
- Generated: `internal/op/opfakes/fake_object_store.go`, `internal/radosclient/radosclientfakes/fake_pool.go`
- Create: `internal/driver/readpool.go`, `internal/driver/stat.go`, `internal/driver/stat_test.go`
- Modify: `internal/driver/store.go` (drop the `StatObject` NotImplemented stub; nothing else)

**Interfaces:**
- Consumes: G's `op.ObjectState`, `op.BucketRecord`, `op.FromRADOS`, `op.ErrInternalError`, `op.ErrNotImplemented`; M's `Store.zone *zoneConfig` (`s.zone.Params meta.ZoneParams`, `s.zone.ZoneGroup meta.ZoneGroup`), `Store.pools *poolCache` (`s.pools.get(ctx, meta.Pool) (radosclient.Pool, error)`), `internal/testutil/fakerados`; `radosclient.ReadOp` (`GetXattrs`, `Stat`, `ReadInto`, `Exec` through phase 0's `version.Read`/`ReadResult.Version`, `internal/cls/version/ops.go:53-66`), `radosclient.Pool.Read`, `Pool.WithLocator`, `radosclient.ErrNotFound`; `meta.DecodeManifest`, `meta.DecodeCompressionInfo`, `meta.Stripe{Obj}.OID()/Locator()`, Task 1's attr names; `cephconf.Options.Size("rgw_max_chunk_size")`.
- Produces:

```go
package op

// ObjectState gains:
//	// Head is the first bytes of the head object — rgw_max_chunk_size or the
//	// whole head when shorter — filled by PrefetchObject; nil after StatObject.
//	Head []byte
//	// Version is the head's cls_version, read in the same op as the stat
//	// (RGWObjVersionTracker::prepare_op_for_read composes cls_version_read into
//	// every raw_obj_stat, rgw_rados.cc:158-166, :8843-8853 at v19.2.6; :9788-9797
//	// at v20.2.4); zero for an object that carries none.
//	Version meta.ObjVersion

// ObjectStore gains:
//	// PrefetchObject is StatObject plus the head's first rgw_max_chunk_size
//	// bytes in the same RADOS op (RGWRados::raw_obj_stat with first_chunk,
//	// what radosgw's prefetch_data asks for). Exists false, no error, for a
//	// missing key.
//	PrefetchObject(ctx context.Context, rec *BucketRecord, key meta.ObjKey) (*ObjectState, error)
```

```go
package driver

// readConfig is read once at Open, like the driver's options:
// rgw_max_chunk_size, rgw_get_obj_max_req_size, rgw_get_obj_window_size,
// rgw_ignore_get_invalid_range.
type readConfig struct{ chunk, maxReq, window uint64 }

// dataPool is rgw_get_obj_data_pool with radosgw's fallbacks, so an object of
// a since-removed storage class still reads; ok is false when no pool
// resolves, radosgw's -EIO "probably misconfiguration".
func (s *Store) dataPool(rule meta.PlacementRule, bucket meta.BucketID) (pool meta.Pool, ok bool)

// objRef is obj_to_raw's result: a pool handle (with the locator set when the
// key has one), oid and locator. rawRef resolves any manifest object through
// its placement rule; headRef is rawRef for a head object.
type objRef struct{ pool radosclient.Pool; oid, loc string }
func (s *Store) rawRef(ctx context.Context, rule meta.PlacementRule, obj meta.Obj) (objRef, error)
func (s *Store) headRef(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey) (objRef, error)

// readHead is RGWRados::raw_obj_stat + get_obj_state_impl: the one head op —
// cls_version_read (prepare_op_for_read), getxattrs, stat2 — and the
// ObjectState it decodes. prefetch adds read(0, rgw_max_chunk_size).
func (s *Store) readHead(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, prefetch bool) (*op.ObjectState, error)

func (s *Store) StatObject(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey) (*op.ObjectState, error)     // readHead(false)
func (s *Store) PrefetchObject(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey) (*op.ObjectState, error) // readHead(true)
```

`objRef`, `rawRef` and `headRef` are the names Task 4 and W Task 5 consume, matching the code above.

Semantics (get_obj_state_impl [S] [rgw_rados.cc:6120-6297](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6120-L6297), identical at [\[T\]:6874](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6874)): ENOENT → `Exists: false`; `Epoch` = the version `Pool.Read` returns; attrs filtered to the `user.rgw.` prefix (`rgw_filter_attrset`, [:8866](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8866)); ETag = `user.rgw.etag` with ONE trailing NUL removed ([:6175-6184](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6175-L6184)); `WriteTag` = the raw `user.rgw.idtag` bytes; `user.rgw.compression` decoded (`Compression`), a decode failure is `op.ErrInternalError` (radosgw -EIO, [:6195-6198](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6195-L6198)); `user.rgw.manifest` decoded and patched with `set_head(rec.Info.PlacementRule, obj, statSize)` ([rgw_obj_manifest.h:406-415](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_obj_manifest.h#L406-L415): `HeadPlacementRule`, `Obj`, `HeadSize`, and for an explicit manifest with `HeadSize > 0` piece 0's `Loc`/`Size`), then `Size = Manifest.ObjSize` ([:6214-6228](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6214-L6228); a decode failure is `op.ErrInternalError`); `StorageClass` = `user.rgw.storage_class` verbatim; `ContentType` = `user.rgw.content_type` with all trailing NULs removed (`rgw_bl_str`); `Head` = the prefetched bytes. A head carrying `user.rgw.olh.ver` is an OLH of a versioned bucket (`is_olh`, [:6086-6090](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6086-L6090)); following it is phase 2, so it answers `op.ErrNotImplemented` (R-D20, logged once per request at info). The fake tag radosgw generates for a manifest without an idtag ([:6238-6245](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6238-L6245)) exists only to skip the atomic guard, so `WriteTag` simply stays empty.

- [ ] **Step 1: The contract additions and their fakes**

`internal/op/object.go`: add `Head []byte` to `ObjectState` (after `ContentType`, with the comment above) and `PrefetchObject` to `ObjectStore` (after `StatObject`). `internal/radosclient/cluster.go`: add `//counterfeiter:generate . Pool` under G's `Cluster` directive if W has not. Run `make generate`; expected: `opfakes/fake_object_store.go` gains `PrefetchObject*`, `radosclientfakes/fake_pool.go` appears (or is unchanged if W added it).

`internal/memstore/object.go` (`s.objects` and `objectKey` stand for G's memstore map and key helper; use their names):

```go
// PrefetchObject implements op.ObjectStore: StatObject plus the head's first
// chunk. memstore holds the whole object, so the chunk is a copy of its first
// prefetchLen bytes.
const prefetchLen = 4 << 20 // rgw_max_chunk_size's default

func (s *Store) PrefetchObject(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey) (*op.ObjectState, error) {
	st, err := s.StatObject(ctx, rec, key)
	if err != nil || !st.Exists {
		return st, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	data := s.objects[objectKey(rec, key)].data
	st.Head = slices.Clone(data[:min(len(data), prefetchLen)])
	return st, nil
}
```

`internal/testutil/fakerados/pool.go`, the `*radosclient.ReadStep` case: after computing the byte range, replace the clone with

```go
		data := obj.Data[min(s.Offset, uint64(len(obj.Data))):end]
		if s.Buf != nil {
			n := copy(s.Buf, data)
			s.Result.Data = s.Buf[:n]
		} else {
			s.Result.Data = slices.Clone(data)
		}
		s.Result.N = len(s.Result.Data)
```

Add to `internal/testutil/fakerados/pool_test.go`: `It("reads into the caller's buffer through ReadInto")` — seed 10 bytes, `ReadInto(2, make([]byte, 4))`, expect `Data` to be the 4 bytes at offset 2 sharing the buffer's backing array (`&res.Data[0] == &buf[0]`).

Add to `internal/memstore/object_test.go`: `It("prefetches the first 4 MiB of the head")` — put a 5 MiB object, `PrefetchObject`, expect `Head` to equal the first 4 MiB and `StatObject`'s `Head` to be nil.

- [ ] **Step 2: Write the failing driver specs**

`internal/driver/stat_test.go`, package `driver_test`, on `fakerados` seeded with M's `seedRookZone` (zone `ceph-objectstore`, default placement `default-placement`, data pool `ceph-objectstore.rgw.buckets.data`, plus a `COMP_ZLIB` storage class on the same pool and a `COLD` class on pool `cold.data`) and a bucket record for `plain` (marker `m1`, placement `default-placement`):

```go
var _ = Describe("StatObject and PrefetchObject", func() {
	const (
		dataPool = "ceph-objectstore.rgw.buckets.data"
		marker   = "m1"
	)
	var (
		c     *fakerados.Cluster
		s     *driver.Store
		plain *op.BucketRecord
	)
	// encode renders a meta type as radosgw stores it (Squid); zone_test.go defines it.
	head := func(oid string, data []byte, attrs map[string][]byte) {
		c.Put(dataPool, "", oid, data)
		obj := c.Object(dataPool, "", oid)
		maps.Copy(obj.Xattrs, attrs)
	}
	BeforeEach(func(ctx SpecContext) {
		c = fakerados.New()
		seedRookZone(c, "ceph-objectstore", true) // extended: adds storage classes COMP_ZLIB (compression zlib, same pool) and COLD (pool cold.data)
		var err error
		s, err = driver.Open(ctx, c, conf(map[string]string{"rgw_zone": "ceph-objectstore", "rgw_zonegroup": "ceph-objectstore", "rgw_realm": "ceph-objectstore", "rgw_max_chunk_size": "4194304", "rgw_get_obj_max_req_size": "4194304", "rgw_get_obj_window_size": "16777216", "rgw_ignore_get_invalid_range": "false"}), driver.Options{})
		Expect(err).NotTo(HaveOccurred())
		plain = &op.BucketRecord{Info: meta.BucketInfo{Bucket: meta.BucketID{Name: "plain", Marker: marker, ID: marker}, PlacementRule: meta.ParsePlacementRule("default-placement")}}
	})

	It("answers Exists false and no error for a missing key", func(ctx SpecContext) {
		st, err := s.StatObject(ctx, plain, meta.ObjKey{Name: "nope"})
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Exists).To(BeFalse())
		Expect(st.Key.Name).To(Equal("nope"))
		Expect(st.Bucket).To(Equal(plain))
	})

	It("decodes a head-only object: size, mtime, epoch, attrs, etag without its NUL, content type, no manifest, no data", func(ctx SpecContext) {
		head(marker+"_small.bin", bytes.Repeat([]byte{7}, 1024), map[string][]byte{
			"user.rgw.etag":         []byte("0123456789abcdef0123456789abcdef\x00"),
			"user.rgw.idtag":        []byte("tx000...\x00"),
			"user.rgw.content_type": []byte("text/plain\x00"),
			"user.rgw.acl":          {1, 2, 3},
			"ceph.objclass.version": {9},
		})
		st, err := s.StatObject(ctx, plain, meta.ObjKey{Name: "small.bin"})
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Exists).To(BeTrue())
		Expect(st.Size).To(BeEquivalentTo(1024))
		Expect(st.Epoch).To(BeEquivalentTo(1))
		Expect(st.ETag).To(Equal("0123456789abcdef0123456789abcdef"))
		Expect(st.WriteTag).To(Equal("tx000...\x00"), "the idtag keeps its NUL: it is compared byte for byte")
		Expect(st.ContentType).To(Equal("text/plain"))
		Expect(st.Attrs).To(HaveKey("user.rgw.acl"))
		Expect(st.Attrs).NotTo(HaveKey("ceph.objclass.version"), "rgw_filter_attrset keeps user.rgw. only")
		Expect(st.Manifest).To(BeNil())
		Expect(st.Compression).To(BeNil())
		Expect(st.Head).To(BeNil())
	})

	It("prefetches the whole head when it is shorter than the chunk, and exactly the chunk otherwise", func(ctx SpecContext) {
		small := bytes.Repeat([]byte{1}, 1024)
		head(marker+"_small.bin", small, map[string][]byte{"user.rgw.etag": []byte("e")})
		st, err := s.PrefetchObject(ctx, plain, meta.ObjKey{Name: "small.bin"})
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Head).To(Equal(small))
		big := bytes.Repeat([]byte{2}, 5<<20)
		head(marker+"_big.bin", big[:4<<20], map[string][]byte{"user.rgw.etag": []byte("e")})
		st, err = s.PrefetchObject(ctx, plain, meta.ObjKey{Name: "big.bin"})
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Head).To(HaveLen(4 << 20))
	})

	It("issues one op: getxattrs, stat and, for a prefetch, one read of rgw_max_chunk_size at 0", func(ctx SpecContext) {
		// A counterfeiter FakePool records the op; the cluster fake hands it out for the data pool.
		cluster := &radosclientfakes.FakeCluster{}
		pool := &radosclientfakes.FakePool{}
		cluster.PoolReturns(pool, nil)
		cluster.RequiredOSDReleaseReturns("squid", nil)
		pool.WithLocatorReturns(pool)
		pool.ReadStub = func(_ context.Context, oid string, rop *radosclient.ReadOp, _ radosclient.OpFlags) (uint64, error) {
			for _, step := range rop.Steps() {
				switch st := step.(type) {
				case *radosclient.GetXattrsStep:
					st.Result.Xattrs = map[string][]byte{"user.rgw.etag": []byte("e")}
				case *radosclient.StatStep:
					st.Result.Size, st.Result.ModTime = 3, time.Unix(1700000000, 0)
				case *radosclient.ReadStep:
					st.Result.N = copy(st.Buf, "abc")
					st.Result.Data = st.Buf[:st.Result.N]
				}
			}
			return 42, nil
		}
		s2 := openWithZone(ctx, cluster) // driver.Open over the fake cluster with the zone seeded through the resolver stubs
		st, err := s2.StatObject(ctx, plain, meta.ObjKey{Name: "k"})
		Expect(err).NotTo(HaveOccurred())
		_, oid, rop, _ := pool.ReadArgsForCall(0)
		Expect(oid).To(Equal(marker + "_k"))
		Expect(stepTypes(rop.Steps())).To(Equal([]string{"*radosclient.ExecStep", "*radosclient.GetXattrsStep", "*radosclient.StatStep"}), "cls_version_read first (prepare_op_for_read), then getxattrs and stat2: raw_obj_stat, rgw_rados.cc:8843-8850")
		Expect(rop.Steps()[0].(*radosclient.ExecStep).Method).To(Equal("read"))
		Expect(st.Epoch).To(BeEquivalentTo(42))

		_, err = s2.PrefetchObject(ctx, plain, meta.ObjKey{Name: "k"})
		Expect(err).NotTo(HaveOccurred())
		_, _, rop, _ = pool.ReadArgsForCall(1)
		steps := rop.Steps()
		Expect(stepTypes(steps)).To(Equal([]string{"*radosclient.ExecStep", "*radosclient.GetXattrsStep", "*radosclient.StatStep", "*radosclient.ReadStep"}))
		rd := steps[2].(*radosclient.ReadStep)
		Expect(rd.Offset).To(BeZero())
		Expect(rd.Length).To(BeEquivalentTo(4 << 20))
		Expect(rd.Buf).To(HaveLen(4 << 20))
	})

	It("names and locates an underscore key as get_obj_bucket_and_oid_loc does", func(ctx SpecContext) {
		pool := &radosclientfakes.FakePool{}
		cluster := &radosclientfakes.FakeCluster{}
		cluster.PoolReturns(pool, nil)
		cluster.RequiredOSDReleaseReturns("squid", nil)
		located := &radosclientfakes.FakePool{}
		pool.WithLocatorReturns(located)
		located.ReadReturns(0, radosclient.ErrNotFound)
		s2 := openWithZone(ctx, cluster)
		_, err := s2.StatObject(ctx, plain, meta.ObjKey{Name: "_underscore.bin"})
		Expect(err).NotTo(HaveOccurred())
		Expect(pool.WithLocatorArgsForCall(0)).To(Equal(marker + "__underscore.bin"))
		_, oid, _, _ := located.ReadArgsForCall(0)
		Expect(oid).To(Equal(marker + "___underscore.bin"))
		Expect(pool.ReadCallCount()).To(BeZero(), "the read went through the located handle")
	})

	It("decodes a manifest, patches its head and takes the size from it; a manifest without an idtag keeps an empty write tag", func(ctx SpecContext) {
		m := meta.NewManifest()
		m.Obj = meta.Obj{Bucket: plain.Info.Bucket, Key: meta.ObjKey{Name: "large.bin"}}
		m.ObjSize, m.HeadSize, m.MaxHeadSize = 10<<20, 4<<20, 4<<20
		m.Prefix = ".abc_"
		m.Rules = map[uint64]meta.ManifestRule{0: {StartOfs: 4 << 20, StripeMaxSize: 4 << 20}}
		m.TailPlacement = meta.BucketPlacement{Bucket: plain.Info.Bucket, PlacementRule: plain.Info.PlacementRule}
		// stored with a stale head placement and size, as a manifest "broken due to old bugs" would be
		m.HeadSize = 1
		m.HeadPlacementRule = meta.ParsePlacementRule("stale")
		head(marker+"_large.bin", make([]byte, 4<<20), map[string][]byte{"user.rgw.etag": []byte("e"), "user.rgw.manifest": encode(m)})
		st, err := s.StatObject(ctx, plain, meta.ObjKey{Name: "large.bin"})
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Size).To(BeEquivalentTo(10<<20), "the manifest's obj_size, not the head's stat size")
		Expect(st.Manifest).NotTo(BeNil())
		Expect(st.Manifest.HeadSize).To(BeEquivalentTo(4<<20), "set_head patches head_size to the stat size")
		Expect(st.Manifest.HeadPlacementRule).To(Equal(plain.Info.PlacementRule), "and the head placement to the bucket's")
		Expect(st.Manifest.Obj.Key.Name).To(Equal("large.bin"))
		Expect(st.WriteTag).To(BeEmpty())
	})

	It("decodes compression info and refuses a corrupt one with InternalError", func(ctx SpecContext) {
		ci := meta.NewCompressionInfo()
		ci.Type, ci.OrigSize = "zlib", 1 << 20
		ci.Blocks = []meta.CompressionBlock{{OldOfs: 0, NewOfs: 0, Len: 100}}
		head(marker+"_c.bin", make([]byte, 100), map[string][]byte{"user.rgw.etag": []byte("e"), "user.rgw.compression": encode(ci)})
		st, err := s.StatObject(ctx, plain, meta.ObjKey{Name: "c.bin"})
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Compression).NotTo(BeNil())
		Expect(st.Compression.OrigSize).To(BeEquivalentTo(1 << 20))
		Expect(st.Size).To(BeEquivalentTo(100), "Size stays the stored size; the op reports orig_size")
		head(marker+"_bad.bin", make([]byte, 100), map[string][]byte{"user.rgw.etag": []byte("e"), "user.rgw.compression": {0xff, 0xff}})
		_, err = s.StatObject(ctx, plain, meta.ObjKey{Name: "bad.bin"})
		Expect(err).To(MatchError(op.ErrInternalError))
	})

	It("refuses an OLH head with NotImplemented", func(ctx SpecContext) {
		head(marker+"_v.bin", nil, map[string][]byte{"user.rgw.olh.ver": {1, 0, 0, 0, 0, 0, 0, 0}, "user.rgw.olh.idtag": []byte("t")})
		_, err := s.StatObject(ctx, plain, meta.ObjKey{Name: "v.bin"})
		Expect(err).To(MatchError(op.ErrNotImplemented))
	})

	Describe("dataPool", func() {
		DescribeTable("resolves the head pool with radosgw's fallbacks",
			func(rule string, explicit string, want string, ok bool) {
				b := plain.Info.Bucket
				b.ExplicitPlacement.DataPool = meta.ParsePool(explicit)
				pool, got := s.DataPoolForTest(meta.ParsePlacementRule(rule), b) // exported through export_test.go
				Expect(got).To(Equal(ok))
				if ok {
					Expect(pool).To(Equal(meta.ParsePool(want)))
				}
			},
			Entry("the STANDARD class of the rule", "default-placement", "", dataPool, true),
			Entry("an explicit placement wins", "default-placement", "legacy.pool", "legacy.pool", true),
			Entry("a class with its own pool", "default-placement/COLD", "", "cold.data", true),
			Entry("a class the zone no longer has falls back to STANDARD's pool", "default-placement/GONE", "", dataPool, true),
			Entry("an empty rule falls back to the zonegroup default placement", "", "", dataPool, true),
			Entry("an unknown placement with a known default", "nope", "", dataPool, true),
		)
		It("fails when even the default placement is unknown to the zone", func() {
			// a zone whose params lack the zonegroup's default placement
			Expect(s.DataPoolForTest(meta.ParsePlacementRule("nope"), plain.Info.Bucket)).Error() // adapt: build a second store whose zone params have no placement_pools
		})
	})
})

func stepTypes(steps []radosclient.Step) []string {
	out := make([]string, 0, len(steps))
	for _, s := range steps {
		out = append(out, fmt.Sprintf("%T", s))
	}
	return out
}
```

`openWithZone(ctx, cluster)` is a spec helper that builds a `driver.Store` over a counterfeiter `FakeCluster`: it stubs `ConfigGet` with the options map and `Pool` to return the given `FakePool`, and gives M's `resolveZone` what it reads by pre-canning `Read` results for the root-pool objects — if that is unwieldy, add `internal/driver/export_test.go` with `func NewStoreForTest(cluster radosclient.Cluster, zone ZoneForTest, cfg readConfig) *Store` that sets `s.zone`, `s.pools = newPoolCache(cluster)`, `s.readCfg` directly (package `driver`, `_test.go` only). Prefer the export helper: it keeps the step-shape specs independent of M's resolver.

- [ ] **Step 3: Run to fail, then write `readpool.go` and `stat.go`**

`readpool.go`:

```go
package driver

// readConfig is the read path's options, read once at Open (rgw.yaml.in:
// rgw_max_chunk_size 4 MiB, rgw_get_obj_max_req_size 4 MiB,
// rgw_get_obj_window_size 16 MiB).
type readConfig struct {
	chunk, maxReq, window uint64
}

func loadReadConfig(o *cephconf.Options) (readConfig, error) {
	var c readConfig
	var err error
	if c.chunk, err = o.Size("rgw_max_chunk_size"); err != nil {
		return c, err
	}
	if c.maxReq, err = o.Size("rgw_get_obj_max_req_size"); err != nil {
		return c, err
	}
	if c.window, err = o.Size("rgw_get_obj_window_size"); err != nil {
		return c, err
	}
	if c.chunk == 0 || c.maxReq == 0 || c.window < c.maxReq {
		return c, fmt.Errorf("driver: rgw_max_chunk_size %d, rgw_get_obj_max_req_size %d, rgw_get_obj_window_size %d: a zero chunk or a window below one request cannot read", c.chunk, c.maxReq, c.window)
	}
	return c, nil
}

// dataPool is rgw_get_obj_data_pool (rgw_obj_manifest.cc:412-430): the
// bucket's explicit data pool when it has one, else the placement rule's pool
// for its storage class, STANDARD's pool when the zone has no such class
// (RGWZonePlacementInfo::get_data_pool, rgw_zone_types.h:281-290), and the
// zonegroup default placement's pool when the rule names no placement of this
// zone (RGWZoneParams::get_head_data_pool, rgw_zone.h:285-303). ok is false
// when nothing resolves: radosgw's -EIO "probably misconfiguration".
func (s *Store) dataPool(rule meta.PlacementRule, bucket meta.BucketID) (meta.Pool, bool) {
	if bucket.ExplicitPlacement.DataPool.Name != "" {
		return bucket.ExplicitPlacement.DataPool, true
	}
	if rule.Name != "" {
		if pi, ok := s.zone.Params.PlacementPools[rule.Name]; ok {
			return classPool(pi, rule.StorageClass), true
		}
	}
	def := s.zone.ZoneGroup.DefaultPlacement
	pi, ok := s.zone.Params.PlacementPools[def.Name]
	if !ok {
		return meta.Pool{}, false
	}
	return classPool(pi, def.StorageClass), true
}

// classPool is RGWZonePlacementInfo::get_data_pool.
func classPool(pi meta.ZonePlacementInfo, sc string) meta.Pool {
	if c, ok := pi.StorageClasses[sc]; ok && c.DataPool != nil {
		return *c.DataPool
	}
	if std, ok := pi.StorageClasses[meta.StorageClassStandard]; ok && std.DataPool != nil {
		return *std.DataPool
	}
	return meta.Pool{}
}

// objRef is a RADOS object of a bucket as get_obj_bucket_and_oid_loc names it,
// with its pool handle.
type objRef struct {
	pool radosclient.Pool
	oid  string
	loc  string
}

// rawRef resolves obj through rule as rgw_obj_to_raw does and opens its pool;
// the handle carries the locator when the key has one.
func (s *Store) rawRef(ctx context.Context, rule meta.PlacementRule, obj meta.Obj) (objRef, error) {
	pool, ok := s.dataPool(rule, obj.Bucket)
	if !ok || pool.Name == "" {
		return objRef{}, fmt.Errorf("%w: no data pool for placement %q of bucket %s", op.ErrInternalError, rule, obj.Bucket.Name)
	}
	h, err := s.pools.get(ctx, pool)
	if err != nil {
		return objRef{}, op.FromRADOS(err, op.ScopeObject)
	}
	stripe := meta.Stripe{Obj: obj}
	ref := objRef{pool: h, oid: stripe.OID(), loc: stripe.Locator()}
	if ref.loc != "" {
		ref.pool = h.WithLocator(ref.loc)
	}
	return ref, nil
}

func (s *Store) headRef(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey) (objRef, error) {
	return s.rawRef(ctx, rec.Info.PlacementRule, meta.Obj{Bucket: rec.Info.Bucket, Key: key})
}
```

`stat.go`:

```go
package driver

// readHead is RGWRados::raw_obj_stat (rgw_rados.cc:8827-8870) and the decode
// half of get_obj_state_impl (:6120-6297): one RADOS op reading every xattr,
// the stat and, when prefetch is set, the first rgw_max_chunk_size bytes.
func (s *Store) readHead(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, prefetch bool) (*op.ObjectState, error) {
	ref, err := s.headRef(ctx, rec, key)
	if err != nil {
		return nil, err
	}
	rop := radosclient.NewReadOp()
	objv := version.Read(rop, s.release) // prepare_op_for_read: cls_version_read first, rgw_rados.cc:158-166, :8843-8844
	xattrs := rop.GetXattrs()
	stat := rop.Stat()
	var rd *radosclient.ReadResult
	if prefetch {
		rd = rop.ReadInto(0, make([]byte, s.readCfg.chunk))
	}
	ver, err := ref.pool.Read(ctx, ref.oid, rop, radosclient.OpFlagNone)
	st := &op.ObjectState{Bucket: rec, Key: key}
	if errors.Is(err, radosclient.ErrNotFound) {
		return st, nil
	}
	if err != nil {
		return nil, op.FromRADOS(err, op.ScopeObject)
	}
	st.Exists = true
	st.Epoch = ver
	if v, verr := objv.Version(); verr == nil { // an object without a version reads as zero
		st.Version = meta.ObjVersion{Ver: v.Ver, Tag: v.Tag}
	}
	st.Size = stat.Size
	st.Mtime = stat.ModTime
	st.Attrs = filterAttrs(xattrs.Xattrs)
	if rd != nil {
		st.Head = rd.Data[:rd.N]
	}
	return st, decodeState(ctx, st, rec, key)
}

// filterAttrs is rgw_filter_attrset: only the user.rgw. names.
func filterAttrs(in map[string][]byte) map[string][]byte {
	out := make(map[string][]byte, len(in))
	for k, v := range in {
		if strings.HasPrefix(k, meta.AttrPrefix) {
			out[k] = v
		}
	}
	return out
}

// decodeState fills the typed fields of st from its attrs as get_obj_state_impl does.
func decodeState(ctx context.Context, st *op.ObjectState, rec *op.BucketRecord, key meta.ObjKey) error {
	if _, olh := st.Attrs[meta.AttrOLHVer]; olh { // "user.rgw.olh.ver": versioning is not implemented yet
		slog.InfoContext(ctx, "refusing an olh head: versioning is not implemented", slog.String("bucket", rec.Info.Bucket.Name), slog.String("key", key.Name))
		return op.ErrNotImplemented
	}
	if etag, ok := st.Attrs[meta.AttrETag]; ok {
		if n := len(etag); n > 0 && etag[n-1] == 0 {
			etag = etag[:n-1] // "get rid of extra null character at the end of the etag" (:6177-6183)
		}
		st.ETag = string(etag)
	}
	st.WriteTag = string(st.Attrs[meta.AttrIDTag])
	st.StorageClass = string(st.Attrs[meta.AttrStorageClass])
	st.ContentType = strings.TrimRight(string(st.Attrs[meta.AttrContentType]), "\x00")
	if b, ok := st.Attrs[meta.AttrCompression]; ok {
		ci, err := decodeWhole(b, meta.DecodeCompressionInfo)
		if err != nil {
			return fmt.Errorf("%w: could not decode compression info for %s/%s: %v", op.ErrInternalError, rec.Info.Bucket.Name, key.Name, err)
		}
		st.Compression = &ci
	}
	if b, ok := st.Attrs[meta.AttrManifest]; ok {
		m, err := decodeWhole(b, meta.DecodeManifest)
		if err != nil {
			return fmt.Errorf("%w: couldn't decode manifest of %s/%s: %v", op.ErrInternalError, rec.Info.Bucket.Name, key.Name, err)
		}
		setHead(&m, rec.Info.PlacementRule, meta.Obj{Bucket: rec.Info.Bucket, Key: key}, st.Size)
		st.Size = m.ObjSize
		st.Manifest = &m
	}
	return nil
}

// setHead is RGWObjManifest::set_head (rgw_obj_manifest.h:406-415), which
// get_obj_state_impl applies to "patch manifest to reflect the head we just
// read, some manifests might be broken due to old bugs".
func setHead(m *meta.Manifest, rule meta.PlacementRule, obj meta.Obj, size uint64) {
	m.HeadPlacementRule = rule
	m.Obj = obj
	m.HeadSize = size
	if m.ExplicitObjs && size > 0 {
		p := m.Objs[0]
		p.Loc, p.Size = obj, size
		if m.Objs == nil {
			m.Objs = map[uint64]meta.ManifestPart{}
		}
		m.Objs[0] = p
	}
}

// decodeWhole decodes b with dec and fails when bytes remain or the decoder failed.
func decodeWhole[T any](b []byte, dec func(*denc.Decoder) T) (T, error) {
	d := denc.NewDecoder(b)
	v := dec(d)
	if err := d.Err(); err != nil { // adapt to denc's error accessor
		return v, err
	}
	if d.Remaining() != 0 {
		return v, fmt.Errorf("%d trailing bytes", d.Remaining())
	}
	return v, nil
}

func (s *Store) StatObject(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey) (*op.ObjectState, error) {
	return s.readHead(ctx, rec, key, false)
}

func (s *Store) PrefetchObject(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey) (*op.ObjectState, error) {
	return s.readHead(ctx, rec, key, true)
}
```

`meta.AttrOLHVer` (`user.rgw.olh.ver`) joins `attrs_read.go` if Task 1 did not add it. `Open` gains `s.readCfg, err = loadReadConfig(conf)` next to M's option reads; `store.go` loses the `StatObject` stub. `export_test.go` exports `DataPoolForTest` and `NewStoreForTest`.

- [ ] **Step 4: Run the suites and the generate check**

Run: `make generate-check && go test -tags=ceph_preview -race ./internal/op/... ./internal/memstore/... ./internal/testutil/fakerados/... ./internal/driver/... && golangci-lint run ./internal/driver/ ./internal/memstore/ ./internal/op/`
Expected: PASS; no stale fake.

- [ ] **Step 5: Commit**

```sh
git add internal/op internal/memstore internal/testutil/fakerados internal/radosclient internal/driver
git commit -m "feat(driver): stat and prefetch the object head in radosgw's one RADOS op"
```

Open the draft PR `feat(driver): StatObject and PrefetchObject (unit R task 3)`; its description states the two additive contract changes.

---

### Task 4: `driver`: `ReadObject` — stripes, window, ordered sink, buffers, decompression

**Files:**
- Create: `internal/driver/read.go`, `internal/driver/read_test.go`
- Modify: `internal/driver/store.go` (drop the `ReadObject` NotImplemented stub; `Open` builds the buffer pool), `go.mod` (`golang.org/x/sync` becomes a direct requirement)

**Interfaces:**
- Consumes: Task 3's `objRef`, `rawRef`, `readConfig`, `op.ObjectState` (`Manifest`, `Compression`, `Head`, `WriteTag`, `Size`, `Bucket`, `Key`); Task 1's `Manifest.Seek`, `StripeIter`; Task 2's `compression.NewDecoder`, `compression.Range`, `compression.NewStream`; `radosclient.ReadOp.CmpXattr`, `ReadInto`; `golang.org/x/sync/semaphore`, `errgroup`.
- Produces:

```go
package driver

// ReadObject implements op.ObjectStore: it streams rng of st to sink in offset
// order. rng is in the object's logical bytes — decompressed bytes for a
// compressed object — and must lie within the object (the op ran range_to_ofs).
// It is RGWRados::Object::Read::iterate (rgw_rados.cc:7444-7464): pieces of at
// most rgw_get_obj_max_req_size per stripe (iterate_obj :7466-7536), up to
// rgw_get_obj_window_size bytes in flight (BlockingAioThrottle), head bytes
// already in st.Head served without a RADOS op and head reads guarded by the
// idtag (get_obj_iterate_cb :7391-7442), completions delivered in offset order
// (get_obj_data::flush :7345-7379). A compressed object's range is widened to
// its compression blocks and decoded through compression.Stream
// (RGWGetObj_Decompress).
func (s *Store) ReadObject(ctx context.Context, st *op.ObjectState, rng op.ByteRange, sink io.Writer) error

// readStored streams the stored bytes [ofs, ofs+n) of st to w in offset
// order and does not decompress them: the bytes Read::read
// (rgw_rados.cc:7224-7343) and Read::iterate (:7444-7536) take from the
// object's stripes, under ReadObject's piece walk, window and buffers. ofs
// and n are stored offsets, which for a compressed object are not the
// logical ones; n == 0 writes nothing. ReadObject serves an uncompressed
// object, and a compressed one's widened block range, through it;
// CopyObject's data copy streams a source's stored stripes with it, as
// copy_obj_data's Read::read does (:5067).
func (s *Store) readStored(ctx context.Context, st *op.ObjectState, ofs, n uint64, w io.Writer) error

// piece is one RADOS read: iterate_obj's (read_obj, obj_ofs, read_ofs, len, is_head_obj).
type piece struct {
	obj    meta.Obj
	rule   meta.PlacementRule
	inHead bool
	objOfs uint64 // logical (stored) offset, the ordering key
	ofs    uint64 // offset within obj
	n      uint64
}
```

Error mapping: a RADOS error on a piece → `op.FromRADOS(err, op.ScopeObject)`, except ECANCELED from the idtag guard → `op.ErrConcurrentModification` (radosgw's 409, [rgw_common.cc:141](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L141)); a read shorter than asked → `io.ErrUnexpectedEOF` wrapped in `op.ErrInternalError`; a pool that cannot be resolved → `op.ErrInternalError`; `ctx.Err()` when the context ends. After an error every in-flight read is drained before ReadObject returns (`get_obj_data::cancel`), and a buffer whose read ended with a context error is dropped, never pooled (the seam's rule for `ReadInto`).

- [ ] **Step 1: Write the failing specs**

`internal/driver/read_test.go`, package `driver_test`, on `fakerados` (data) and `radosclientfakes.FakePool` (op shapes, ordering, concurrency), using Task 3's `NewStoreForTest`, `seedRookZone`, `encode` and a `bucket` fixture (`plain`, marker `m1`, `default-placement`):

```go
var _ = Describe("ReadObject", func() {
	const (
		dataPool = "ceph-objectstore.rgw.buckets.data"
		marker   = "m1"
		prefix   = ".AkTbQPoXT3s8QcthZt-DpS5NPRLqS5u_"
	)
	var (
		c     *fakerados.Cluster
		s     *driver.Store
		plain *op.BucketRecord
	)
	payload := func(n int, seed byte) []byte {
		b := make([]byte, n)
		for i := range b {
			b[i] = seed + byte(i%251)
		}
		return b
	}
	put := func(oid string, data []byte, attrs map[string][]byte) {
		c.Put(dataPool, "", oid, data)
		maps.Copy(c.Object(dataPool, "", oid).Xattrs, attrs)
	}
	// largeManifest is large.bin's layout: 4 MiB head, 4 MiB stripes, 10 MiB.
	largeManifest := func() meta.Manifest {
		m := meta.NewManifest()
		m.Obj = meta.Obj{Bucket: plain.Info.Bucket, Key: meta.ObjKey{Name: "large.bin"}}
		m.ObjSize, m.HeadSize, m.MaxHeadSize = 10<<20, 4<<20, 4<<20
		m.Prefix = prefix
		m.Rules = map[uint64]meta.ManifestRule{0: {StartOfs: 4 << 20, StripeMaxSize: 4 << 20}}
		m.TailPlacement = meta.BucketPlacement{Bucket: plain.Info.Bucket, PlacementRule: plain.Info.PlacementRule}
		m.HeadPlacementRule = plain.Info.PlacementRule
		return m
	}
	seedLarge := func(data []byte, tag string) {
		attrs := map[string][]byte{"user.rgw.etag": []byte("e"), "user.rgw.manifest": encode(largeManifest())}
		if tag != "" {
			attrs["user.rgw.idtag"] = []byte(tag)
		}
		put(marker+"_large.bin", data[:4<<20], attrs)
		put(marker+"__shadow_"+prefix+"1", data[4<<20:8<<20], nil)
		put(marker+"__shadow_"+prefix+"2", data[8<<20:], nil)
	}
	read := func(ctx context.Context, st *op.ObjectState, ofs, n uint64) ([]byte, error) {
		var out bytes.Buffer
		err := s.ReadObject(ctx, st, op.ByteRange{Offset: ofs, Length: n}, &out)
		return out.Bytes(), err
	}

	BeforeEach(func(ctx SpecContext) {
		c = fakerados.New()
		seedRookZone(c, "ceph-objectstore", true)
		s = openFake(ctx, c) // driver.Open over the fake cluster with the read options
		plain = &op.BucketRecord{Info: meta.BucketInfo{Bucket: meta.BucketID{Name: "plain", Marker: marker, ID: marker}, PlacementRule: meta.ParsePlacementRule("default-placement")}}
	})

	It("reads a head-only object whole and by range", func(ctx SpecContext) {
		data := payload(1024, 1)
		put(marker+"_small.bin", data, map[string][]byte{"user.rgw.etag": []byte("e")})
		st, err := s.StatObject(ctx, plain, meta.ObjKey{Name: "small.bin"})
		Expect(err).NotTo(HaveOccurred())
		Expect(read(ctx, st, 0, 1024)).To(Equal(data))
		Expect(read(ctx, st, 100, 50)).To(Equal(data[100:150]))
		Expect(read(ctx, st, 0, 0)).To(BeEmpty())
	})

	It("serves a prefetched head without a RADOS read, and an unprefetched one with a guarded read", func(ctx SpecContext) {
		data := payload(3<<20, 2)
		pool := &radosclientfakes.FakePool{}
		pool.WithLocatorReturns(pool)
		pool.ReadStub = fakeHeadOnly(data, "tag\x00") // fills getxattrs/stat/read steps from data and the idtag
		s2 := driver.NewStoreForTest(fakeClusterWith(pool), testZone(), testReadConfig())
		st, err := s2.PrefetchObject(ctx, plain, meta.ObjKey{Name: "k"})
		Expect(err).NotTo(HaveOccurred())
		var out bytes.Buffer
		Expect(s2.ReadObject(ctx, st, op.ByteRange{Offset: 0, Length: 3 << 20}, &out)).To(Succeed())
		Expect(out.Bytes()).To(Equal(data))
		Expect(pool.ReadCallCount()).To(Equal(1), "the prefetch op was the only RADOS op")

		st, err = s2.StatObject(ctx, plain, meta.ObjKey{Name: "k"})
		Expect(err).NotTo(HaveOccurred())
		out.Reset()
		Expect(s2.ReadObject(ctx, st, op.ByteRange{Offset: 10, Length: 100}, &out)).To(Succeed())
		Expect(out.Bytes()).To(Equal(data[10:110]))
		_, oid, rop, _ := pool.ReadArgsForCall(pool.ReadCallCount() - 1)
		Expect(oid).To(Equal(marker + "_k"))
		steps := rop.Steps()
		Expect(steps).To(HaveLen(2))
		cmp := steps[0].(*radosclient.CmpXattrStep)
		Expect(cmp.Name).To(Equal("user.rgw.idtag"))
		Expect(cmp.Op).To(Equal(radosclient.CmpEQ))
		Expect(cmp.Value).To(Equal([]byte("tag\x00")))
		rd := steps[1].(*radosclient.ReadStep)
		Expect(rd.Offset).To(BeEquivalentTo(10))
		Expect(rd.Length).To(BeEquivalentTo(100))
	})

	It("reads a tailed object whole from the prefetch plus the tails, in order", func(ctx SpecContext) {
		data := payload(10<<20, 3)
		seedLarge(data, "tag\x00")
		st, err := s.PrefetchObject(ctx, plain, meta.ObjKey{Name: "large.bin"})
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Size).To(BeEquivalentTo(10 << 20))
		Expect(read(ctx, st, 0, 10<<20)).To(Equal(data))
	})

	DescribeTable("reads ranges across the head/tail boundary and inside tails",
		func(ofs, n uint64) {
			ctx := context.Background()
			data := payload(10<<20, 4)
			seedLarge(data, "")
			st, err := s.StatObject(ctx, plain, meta.ObjKey{Name: "large.bin"})
			Expect(err).NotTo(HaveOccurred())
			Expect(read(ctx, st, ofs, n)).To(Equal(data[ofs : ofs+n]))
		},
		Entry("the last head byte and the first tail byte", uint64(4<<20-1), uint64(2)),
		Entry("inside the first tail", uint64(5<<20), uint64(1<<20)),
		Entry("the last byte", uint64(10<<20-1), uint64(1)),
		Entry("head through both tails", uint64(4<<20-3), uint64(6<<20+3)),
		Entry("whole object without a prefetch", uint64(0), uint64(10<<20)),
	)

	It("issues iterate_obj's pieces: per stripe, at most rgw_get_obj_max_req_size, with location_ofs", func(ctx SpecContext) {
		// An explicit manifest: head 1 MiB, then a 6 MiB piece stored at loc_ofs 512 in one shadow object.
		m := meta.NewManifest()
		m.ExplicitObjs = true
		m.Obj = meta.Obj{Bucket: plain.Info.Bucket, Key: meta.ObjKey{Name: "x"}}
		shadow := meta.Obj{Bucket: plain.Info.Bucket, Key: meta.ObjKey{Name: "sh", NS: meta.NSShadow}}
		m.ObjSize, m.HeadSize, m.MaxHeadSize = 7<<20, 1<<20, 1<<20
		m.Objs = map[uint64]meta.ManifestPart{0: {Loc: m.Obj, Size: 1 << 20}, 1 << 20: {Loc: shadow, LocOfs: 512, Size: 6 << 20}}
		pool := &radosclientfakes.FakePool{}
		pool.WithLocatorReturns(pool)
		var reads []readCall // (oid, ofs, len) in issue order, recorded by the stub, which answers zeros
		pool.ReadStub = recordReads(&reads, map[string][]byte{"user.rgw.etag": []byte("e"), "user.rgw.manifest": encode(m)}, 1<<20)
		s2 := driver.NewStoreForTest(fakeClusterWith(pool), testZone(), testReadConfig())
		st, err := s2.StatObject(ctx, plain, meta.ObjKey{Name: "x"})
		Expect(err).NotTo(HaveOccurred())
		Expect(s2.ReadObject(ctx, st, op.ByteRange{Offset: 1<<20 - 10, Length: 6<<20 + 10}, io.Discard)).To(Succeed())
		Expect(reads[1:]).To(Equal([]readCall{ // reads[0] is the stat
			{marker + "_x", 1<<20 - 10, 10},
			{marker + "__shadow_sh", 512, 4 << 20},
			{marker + "__shadow_sh", 512 + 4<<20, 2 << 20},
		}))
	})

	It("reads a multipart object's range across a part boundary from the right stripes", func(ctx SpecContext) {
		m := decodeGolden("squid-multipart") // the squid-multipart golden, marker rewritten to m1 by the helper
		pool := &radosclientfakes.FakePool{}
		pool.WithLocatorReturns(pool)
		var reads []readCall
		pool.ReadStub = recordReads(&reads, map[string][]byte{"user.rgw.etag": []byte("e"), "user.rgw.manifest": encode(m)}, 0)
		s2 := driver.NewStoreForTest(fakeClusterWith(pool), testZone(), testReadConfig())
		st, err := s2.StatObject(ctx, plain, meta.ObjKey{Name: "multipart.bin"})
		Expect(err).NotTo(HaveOccurred())
		Expect(s2.ReadObject(ctx, st, op.ByteRange{Offset: 13 << 20, Length: 5 << 20}, io.Discard)).To(Succeed())
		const p = "multipart.bin.2~vXcCS0UgSATwQOecOwRkZrWyEyux7jO"
		Expect(reads[1:]).To(Equal([]readCall{
			{marker + "__shadow_" + p + ".2_1", 1 << 20, 3 << 20},
			{marker + "__multipart_" + p + ".3", 0, 2 << 20},
		}))
	})

	It("keeps at most rgw_get_obj_window_size in flight and still writes in offset order", func(ctx SpecContext) {
		// 20 MiB in a head + 4 tails of 4 MiB; the stub completes reads in reverse order and counts concurrency.
		var inflight, peak atomic.Int32
		var mu sync.Mutex
		release := map[string]chan struct{}{}
		pool := &radosclientfakes.FakePool{}
		pool.WithLocatorReturns(pool)
		m := largeManifestOfSize(20 << 20) // helper: like largeManifest with ObjSize 20 MiB
		data := payload(20<<20, 5)
		pool.ReadStub = func(ctx context.Context, oid string, rop *radosclient.ReadOp, _ radosclient.OpFlags) (uint64, error) {
			if isStat(rop) {
				return fillHead(rop, data[:4<<20], map[string][]byte{"user.rgw.etag": []byte("e"), "user.rgw.manifest": encode(m)}), nil
			}
			n := inflight.Add(1)
			for {
				old := peak.Load()
				if n <= old || peak.CompareAndSwap(old, n) {
					break
				}
			}
			defer inflight.Add(-1)
			mu.Lock()
			ch := make(chan struct{})
			release[oid] = ch
			mu.Unlock()
			<-ch // released by the spec in reverse order
			return 1, fillRead(rop, data, oid, prefix, marker)
		}
		s2 := driver.NewStoreForTest(fakeClusterWith(pool), testZone(), testReadConfig())
		st, err := s2.PrefetchObject(ctx, plain, meta.ObjKey{Name: "large.bin"})
		Expect(err).NotTo(HaveOccurred())
		var out bytes.Buffer
		done := make(chan error, 1)
		go func() { done <- s2.ReadObject(ctx, st, op.ByteRange{Offset: 0, Length: 20 << 20}, &out) }()
		Eventually(func() int32 { return inflight.Load() }).Should(BeEquivalentTo(4), "16 MiB window / 4 MiB requests")
		Consistently(func() int32 { return inflight.Load() }).Should(BeNumerically("<=", 4))
		mu.Lock()
		for _, n := range []string{"4", "3", "2", "1"} { // complete in reverse
			close(release[marker+"__shadow_"+prefix+n])
		}
		mu.Unlock()
		Eventually(done).Should(Receive(Succeed()))
		Expect(out.Bytes()).To(Equal(data))
		Expect(peak.Load()).To(BeEquivalentTo(4))
	})

	It("stops at the first failed tail, drains the rest, and maps the error", func(ctx SpecContext) {
		data := payload(10<<20, 6)
		seedLarge(data, "")
		// remove the second tail: ENOENT on the third stripe
		delete(c.Object(dataPool, "", marker+"__shadow_"+prefix+"2").Xattrs, "x") // ensure it exists...
		c.Remove(dataPool, "", marker+"__shadow_"+prefix+"2")                        // deletes the object
		st, err := s.StatObject(ctx, plain, meta.ObjKey{Name: "large.bin"})
		Expect(err).NotTo(HaveOccurred())
		out, err := read(ctx, st, 0, 10<<20)
		Expect(err).To(MatchError(op.ErrNoSuchKey))
		Expect(out).To(HaveLen(8<<20), "the first two stripes were written, nothing after")
	})

	It("answers ConcurrentModification when the head's idtag changed under it", func(ctx SpecContext) {
		data := payload(2<<20, 7)
		put(marker+"_k", data, map[string][]byte{"user.rgw.etag": []byte("e"), "user.rgw.idtag": []byte("old\x00")})
		st, err := s.StatObject(ctx, plain, meta.ObjKey{Name: "k"})
		Expect(err).NotTo(HaveOccurred())
		c.Object(dataPool, "", marker+"_k").Xattrs["user.rgw.idtag"] = []byte("new\x00")
		_, err = read(ctx, st, 0, 2<<20)
		Expect(err).To(MatchError(op.ErrConcurrentModification))
	})

	It("returns the context error and drops the buffers of abandoned reads", func(ctx SpecContext) {
		pool := &radosclientfakes.FakePool{}
		pool.WithLocatorReturns(pool)
		m := largeManifestOfSize(20 << 20)
		block := make(chan struct{})
		pool.ReadStub = func(ctx context.Context, oid string, rop *radosclient.ReadOp, _ radosclient.OpFlags) (uint64, error) {
			if isStat(rop) {
				return fillHead(rop, make([]byte, 4<<20), map[string][]byte{"user.rgw.etag": []byte("e"), "user.rgw.manifest": encode(m)}), nil
			}
			select {
			case <-block:
				return 1, nil
			case <-ctx.Done():
				return 0, ctx.Err()
			}
		}
		s2 := driver.NewStoreForTest(fakeClusterWith(pool), testZone(), testReadConfig())
		st, err := s2.StatObject(ctx, plain, meta.ObjKey{Name: "large.bin"})
		Expect(err).NotTo(HaveOccurred())
		rctx, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- s2.ReadObject(rctx, st, op.ByteRange{Offset: 4 << 20, Length: 16 << 20}, io.Discard) }()
		Eventually(pool.ReadCallCount).Should(BeNumerically(">=", 2))
		cancel()
		Eventually(done).Should(Receive(MatchError(context.Canceled)))
		Expect(s2.PooledBuffersForTest()).To(BeZero(), "no buffer of an abandoned read went back to the pool")
		close(block)
	})

	Describe("a compressed object", func() {
		// 10 MiB plaintext, snappy per 4 MiB block (RGWPutObj_Compress), the compressed bytes laid out as
		// a 4 MiB head plus one tail so the last block straddles the head/tail boundary.
		var plain10 []byte
		var comp []byte
		var ci meta.CompressionInfo
		BeforeEach(func() {
			plain10 = plaintextCompressible(10 << 20)
			ci = meta.NewCompressionInfo()
			ci.Type, ci.OrigSize = "snappy", 10<<20
			comp = comp[:0]
			for ofs := 0; ofs < len(plain10); ofs += 4 << 20 {
				b := snappy.Encode(nil, plain10[ofs:min(ofs+4<<20, len(plain10))])
				ci.Blocks = append(ci.Blocks, meta.CompressionBlock{OldOfs: uint64(ofs), NewOfs: uint64(len(comp)), Len: uint64(len(b))})
				comp = append(comp, b...)
			}
		})
		seedCompressed := func(headLen int) *op.ObjectState {
			GinkgoHelper()
			m := largeManifestOfSize(uint64(len(comp)))
			m.HeadSize, m.MaxHeadSize = uint64(headLen), uint64(headLen)
			attrs := map[string][]byte{"user.rgw.etag": []byte("e"), "user.rgw.manifest": encode(m), "user.rgw.compression": encode(ci)}
			put(marker+"_large.bin", comp[:headLen], attrs)
			for i, ofs := 1, headLen; ofs < len(comp); i, ofs = i+1, ofs+4<<20 {
				put(marker+"__shadow_"+prefix+strconv.Itoa(i), comp[ofs:min(ofs+4<<20, len(comp))], nil)
			}
			st, err := s.StatObject(context.Background(), plain, meta.ObjKey{Name: "large.bin"})
			Expect(err).NotTo(HaveOccurred())
			Expect(st.Compression).NotTo(BeNil())
			return st
		}
		It("decodes the whole object", func(ctx SpecContext) {
			st := seedCompressed(len(comp) / 2)
			Expect(read(ctx, st, 0, 10<<20)).To(Equal(plain10))
		})
		DescribeTable("decodes a range, reading only the blocks it needs",
			func(ofs, n uint64) {
				st := seedCompressed(len(comp) / 2)
				got, err := read(context.Background(), st, ofs, n)
				Expect(err).NotTo(HaveOccurred())
				Expect(got).To(Equal(plain10[ofs : ofs+n]))
			},
			Entry("inside the first block", uint64(10), uint64(100)),
			Entry("across two blocks", uint64(4<<20-5), uint64(10)),
			Entry("the last bytes", uint64(10<<20-7), uint64(7)),
			Entry("all of the second block exactly", uint64(4<<20), uint64(4<<20)),
		)
		It("reads exactly the compressed range fixup_range selects", func(ctx SpecContext) {
			pool := &radosclientfakes.FakePool{}
			pool.WithLocatorReturns(pool)
			var reads []readCall
			m := largeManifestOfSize(uint64(len(comp)))
			m.HeadSize, m.MaxHeadSize = 4<<20, 4<<20 // comp is far smaller than 4 MiB: everything is in the head
			pool.ReadStub = recordReadsFrom(&reads, comp, map[string][]byte{"user.rgw.etag": []byte("e"), "user.rgw.manifest": encode(m), "user.rgw.compression": encode(ci)})
			s2 := driver.NewStoreForTest(fakeClusterWith(pool), testZone(), testReadConfig())
			st, err := s2.StatObject(ctx, plain, meta.ObjKey{Name: "large.bin"})
			Expect(err).NotTo(HaveOccurred())
			var out bytes.Buffer
			Expect(s2.ReadObject(ctx, st, op.ByteRange{Offset: 5 << 20, Length: 1 << 20}, &out)).To(Succeed())
			Expect(out.Bytes()).To(Equal(plain10[5<<20 : 6<<20]))
			b := ci.Blocks[1]
			Expect(reads[1:]).To(Equal([]readCall{{marker + "_large.bin", b.NewOfs, b.Len}}))
		})
		It("fails on a corrupt block after delivering the blocks before it", func(ctx SpecContext) {
			st := seedCompressed(len(comp) / 2)
			obj := c.Object(dataPool, "", marker+"_large.bin")
			obj.Data[ci.Blocks[1].NewOfs+2] ^= 0xff
			out, err := read(ctx, st, 0, 10<<20)
			Expect(err).To(MatchError(compression.ErrCorrupt))
			Expect(out).To(HaveLen(4 << 20))
		})
		It("hands a raw reader the stored bytes, undecoded, across the head and the tail", func(ctx SpecContext) {
			st := seedCompressed(len(comp) / 2)
			var out bytes.Buffer
			Expect(s.ReadStoredForTest(ctx, st, 0, st.Size, &out)).To(Succeed())
			Expect(out.Bytes()).To(Equal(comp), "Read::read returns what the stripes hold (rgw_rados.cc:7224-7343)")
			out.Reset()
			Expect(s.ReadStoredForTest(ctx, st, 0, 0, &out)).To(Succeed())
			Expect(out.Len()).To(BeZero())
		})
	})
})
```

`c.Remove` is a fakerados helper that deletes an object; add it if absent.

Helpers to write in `read_test.go`: `readCall{oid string; ofs, n uint64}`; `isStat(rop)` (the op has a `StatStep`); `fillHead(rop, data, attrs) uint64` (answers the head op from data/attrs — the leading `version.Read` exec with an encoded `cls_version_read_ret` holding `ObjVersion{Ver: 1, Tag: "t"}`, so `ObjectState.Version` reads back as that — and returns version 1); `fillRead(rop, data, oid, prefix, marker) error` (maps a tail oid to its slice of data and copies into `Buf`); `recordReads(&reads, attrs, headLen)` and `recordReadsFrom(&reads, headData, attrs)` (record every op's read step as a `readCall`, answer the head op from attrs, answer data reads with zeros or from `headData`); `largeManifestOfSize(n)`; `plaintextCompressible(n)` (Task 2's `plaintext`); `decodeGolden(name)`; `fakeClusterWith(pool)` (a `FakeCluster` whose `Pool` returns `pool`); `testZone()` (a `ZoneForTest` with `default-placement` on `dataPool`); `testReadConfig()` (`{4 MiB, 4 MiB, 16 MiB}`). `Store.PooledBuffersForTest()`, `Store.ReadStoredForTest(ctx, st, ofs, n, w)` (calls `readStored`) and `fakerados.Cluster.Remove` are small additions (`export_test.go`; a `Remove` method on the fake cluster, mirroring `Put`).

- [ ] **Step 2: Run to fail; write `read.go`**

```go
package driver

// ReadObject: see the interface comment above.
func (s *Store) ReadObject(ctx context.Context, st *op.ObjectState, rng op.ByteRange, sink io.Writer) error {
	if rng.Length == 0 {
		return nil
	}
	if st.Compression == nil || st.Compression.Type == compression.None {
		return s.readStored(ctx, st, rng.Offset, rng.Length, sink)
	}
	// RGWGetObj::execute: obj_size = orig_size, fixup_range widens the request
	// to compression blocks and the filter trims the decoded output
	// (rgw_compression.cc:107-218).
	dec, err := compression.NewDecoder(st.Compression.Type)
	if err != nil {
		return fmt.Errorf("%w: %v", op.ErrInternalError, err) // "Cannot load compressor of type", -EIO
	}
	end := rng.Offset + rng.Length - 1
	partial := rng.Offset != 0 || end != st.Compression.OrigSize-1
	win, err := compression.Range(st.Compression.Blocks, partial, rng.Offset, end)
	if err != nil {
		return fmt.Errorf("%w: %v", op.ErrInternalError, err)
	}
	stream := compression.NewStream(dec, st.Compression.Blocks, win, st.Compression.CompressorMessage, sink)
	if err := s.readStored(ctx, st, win.CompOfs, win.CompEnd-win.CompOfs+1, stream); err != nil {
		return err
	}
	return stream.Close()
}

// readStored streams stored bytes [ofs, ofs+n) of st to w, undecoded:
// iterate_obj's piece walk fed through the throttled, ordered reader.
func (s *Store) readStored(ctx context.Context, st *op.ObjectState, ofs, n uint64, w io.Writer) error {
	if n == 0 {
		return nil
	}
	pieces, err := s.pieces(st, ofs, n)
	if err != nil {
		return err
	}
	return s.stream(ctx, st, pieces, w)
}

// pieces is RGWRados::iterate_obj (rgw_rados.cc:7466-7536): the reads of
// [ofs, ofs+n), stripe by stripe, each at most rgw_get_obj_max_req_size.
func (s *Store) pieces(st *op.ObjectState, ofs, n uint64) ([]piece, error) {
	end := ofs + n - 1
	head := meta.Obj{Bucket: st.Bucket.Info.Bucket, Key: st.Key}
	var out []piece
	if st.Manifest == nil {
		for ofs <= end {
			rd := min(n, s.readCfg.maxReq)
			out = append(out, piece{obj: head, rule: st.Bucket.Info.PlacementRule, inHead: true, objOfs: ofs, ofs: ofs, n: rd})
			n -= rd
			ofs += rd
		}
		return out, nil
	}
	it, err := st.Manifest.Seek(ofs)
	if err != nil {
		return nil, fmt.Errorf("%w: manifest of %s/%s: %v", op.ErrInternalError, st.Bucket.Info.Bucket.Name, st.Key.Name, err)
	}
	for !it.Done() && ofs <= end {
		stripeOfs := it.StripeOfs()
		next := stripeOfs + it.StripeSize()
		for ofs < next && ofs <= end {
			obj, rule, inHead := it.Location()
			rd := min(n, it.StripeSize()-(ofs-stripeOfs), s.readCfg.maxReq)
			out = append(out, piece{obj: obj, rule: rule, inHead: inHead, objOfs: ofs, ofs: it.LocOfs() + (ofs - stripeOfs), n: rd})
			n -= rd
			ofs += rd
		}
		if err := it.Next(); err != nil {
			return nil, fmt.Errorf("%w: manifest of %s/%s: %v", op.ErrInternalError, st.Bucket.Info.Bucket.Name, st.Key.Name, err)
		}
	}
	if ofs <= end {
		return nil, fmt.Errorf("%w: manifest of %s/%s ends at %d, %d bytes short of the object", op.ErrInternalError, st.Bucket.Info.Bucket.Name, st.Key.Name, ofs, end+1-ofs)
	}
	return out, nil
}

// result is one completed piece, delivered in objOfs order.
type result struct {
	p    piece
	buf  []byte // from the pool when the read reached RADOS; nil for a prefetched piece
	data []byte
	err  error
	done chan struct{}
}

// stream is Read::iterate + get_obj_iterate_cb + get_obj_data::flush: pieces
// already in st.Head are written without an op; the rest are read under the
// byte window, each in its own goroutine, and written in issue order as they
// complete. On any error the remaining reads are drained before returning.
func (s *Store) stream(ctx context.Context, st *op.ObjectState, pieces []piece, w io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	sem := semaphore.NewWeighted(int64(s.readCfg.window)) // BlockingAioThrottle's window, cost = bytes
	results := make(chan *result, len(pieces))
	var wg sync.WaitGroup
	defer wg.Wait() // drain: every read goroutine has returned before we do

	issue := func(p piece) {
		r := &result{p: p, done: make(chan struct{})}
		results <- r
		if p.inHead && p.objOfs < uint64(len(st.Head)) { // served from the prefetch, no op
			r.data = st.Head[p.objOfs:min(uint64(len(st.Head)), p.objOfs+p.n)]
			close(r.done)
			return
		}
		if err := sem.Acquire(ctx, int64(p.n)); err != nil {
			r.err = err
			close(r.done)
			return
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer close(r.done)
			defer sem.Release(int64(p.n))
			r.buf = s.bufs.Get().([]byte)[:p.n]
			r.data, r.err = s.readPiece(ctx, st, p, r.buf)
		}()
	}

	// The producer issues ahead of the consumer, blocking on the window.
	prodErr := make(chan error, 1)
	go func() {
		defer close(results)
		for _, p := range pieces {
			if ctx.Err() != nil {
				prodErr <- ctx.Err()
				return
			}
			// split a piece the prefetch covers only partly
			if p.inHead && p.objOfs < uint64(len(st.Head)) && p.objOfs+p.n > uint64(len(st.Head)) {
				have := uint64(len(st.Head)) - p.objOfs
				issue(piece{obj: p.obj, rule: p.rule, inHead: true, objOfs: p.objOfs, ofs: p.ofs, n: have})
				p = piece{obj: p.obj, rule: p.rule, inHead: true, objOfs: p.objOfs + have, ofs: p.ofs + have, n: p.n - have}
			}
			issue(p)
		}
		prodErr <- nil
	}()

	var failed error
	for r := range results {
		<-r.done
		if failed == nil {
			switch {
			case r.err != nil:
				failed = r.err
				cancel()
			case uint64(len(r.data)) != r.p.n:
				failed = fmt.Errorf("%w: short read of %s at %d: %d of %d bytes: %w", op.ErrInternalError, r.p.obj.Key.Name, r.p.ofs, len(r.data), r.p.n, io.ErrUnexpectedEOF)
				cancel()
			default:
				if _, err := w.Write(r.data); err != nil {
					failed = err
					cancel()
				}
			}
		}
		if r.buf != nil && !errors.Is(r.err, context.Canceled) && !errors.Is(r.err, context.DeadlineExceeded) {
			s.bufs.Put(r.buf[:cap(r.buf)]) // a buffer an abandoned op may still fill is never pooled
		}
	}
	if failed != nil {
		return failed
	}
	return <-prodErr
}

// readPiece is one op of get_obj_iterate_cb (:7396-7441): the idtag guard on a
// head read, then read(read_ofs, len) into buf.
func (s *Store) readPiece(ctx context.Context, st *op.ObjectState, p piece, buf []byte) ([]byte, error) {
	ref, err := s.rawRef(ctx, p.rule, p.obj)
	if err != nil {
		return nil, err
	}
	rop := radosclient.NewReadOp()
	if p.inHead && st.WriteTag != "" {
		rop.CmpXattr(meta.AttrIDTag, radosclient.CmpEQ, []byte(st.WriteTag))
	}
	rd := rop.ReadInto(p.ofs, buf)
	if _, err := ref.pool.Read(ctx, ref.oid, rop, radosclient.OpFlagNone); err != nil {
		switch {
		case ctx.Err() != nil:
			return nil, ctx.Err()
		case errors.Is(err, radosclient.ErrCanceled):
			return nil, fmt.Errorf("%w: %s changed while being read", op.ErrConcurrentModification, p.obj.Key.Name)
		}
		return nil, op.FromRADOS(err, op.ScopeObject)
	}
	return rd.Data[:rd.N], nil
}
```

`Store` gains `bufs sync.Pool` (`New: func() any { return make([]byte, s.readCfg.maxReq) }`, set in `Open` after `loadReadConfig`) and `readCfg readConfig`. The `rawRef` per piece opens the pool handle through M's cache (a map lookup after the first call). For the head piece the rule is `st.Manifest.HeadPlacementRule` (patched to the bucket's) — `it.Location()` returns it; without a manifest the bucket's rule.

- [ ] **Step 3: Run the specs under the race detector**

Run: `go test -tags=ceph_preview -race -count=3 ./internal/driver/ && golangci-lint run ./internal/driver/`
Expected: PASS three times (the ordering and window specs are concurrency-sensitive); no findings.

- [ ] **Step 4: Commit**

```sh
git add internal/driver go.mod go.sum
git commit -m "feat(driver): stream object reads in 4 MiB pieces under a 16 MiB window, in order"
```

Open the draft PR `feat(driver): ReadObject (unit R task 4)`.

---

### Task 5: `op`: `GetObject` (GET and HEAD): range, conditionals, partNumber, refusals, usage

**Files:**
- Create: `internal/op/getobject.go`, `internal/op/readconds.go`, `internal/op/getobject_test.go`, `internal/op/readconds_test.go`

**Interfaces:**
- Consumes: G's `op.Request`, `op.Run`, `VerifyObjectPermission`, `Env.Buckets.GetBucket`, `Env.Objects` (`StatObject`, `PrefetchObject`, `ReadObject`), `Env.Zone` (`Release`, `ZoneGroup`), `Env.Conf` (`rgw_ignore_get_invalid_range`, `rgw_crypt_require_ssl`), the error sentinels, `Error.WithMessage`; Z's `acl.PermFor`, `policy.S3GetObject`, `S3GetObjectVersion`, `S3GetObjectTorrent`, `S3GetObjectVersionTorrent`; M's `op.LogUsage`; Task 1's attr names, `Manifest.PartsCount`, `PartBounds`, `RestoreStatus`; `meta.TierTypeCloudS3`, `meta.TierTypeCloudS3Glacier`, `meta.ZoneGroupPlacementTier.AllowReadThrough`.
- Produces:

```go
package op

// GetObject is RGWGetObj for S3 (rgw_op.cc RGWGetObj::*, rgw_rest_s3.cc
// RGWGetObj_ObjStore_S3::get_params): GET and HEAD of an object. The handler
// fills the inputs from the request, runs the op, and renders the results.
type GetObject struct {
	// Inputs.
	GetData           bool              // false for HEAD (get_obj_op(false)); Name() is "get_obj" for both
	Range             string            // the Range header, "" when absent
	IfMatch           string            // raw header values, "" when absent
	IfNoneMatch       string
	IfModifiedSince   string
	IfUnmodifiedSince string
	PartNumber        *int              // ?partNumber, parsed by the handler; nil when absent
	Torrent           bool              // ?torrent
	ResponseOverrides map[string]string // response header name → value from the six response-* params
	SSEHeader         bool              // x-amz-server-side-encryption present on the request
	SSECAlgorithm     string            // x-amz-server-side-encryption-customer-algorithm, "" when absent
	SSECKey           string            // …-customer-key (base64)
	SSECKeyMD5        string            // …-customer-key-MD5 (base64)
	Secure            bool              // rgw_transport_is_secure: TLS, or a trusted forwarded proto
	Sink              Sink              // where Execute writes the headers (through the handler's wrapper) and the body

	// Results.
	State      *ObjectState // the state served: the object's, or the part head's with PartNumber
	CondState  *ObjectState // the multipart head when PartNumber redirected, else State (If-Match compares its ETag)
	Offset     uint64       // the served range after range_to_ofs
	Length     uint64       // Content-Length (total_len); 0 when ofs > end
	Partial    bool         // 206 with Content-Range
	ObjSize    uint64       // s->obj_size: the logical size, orig_size when compressed
	PartsCount *int         // x-amz-mp-parts-count
	VersionID  string       // x-amz-version-id: the key's instance
	Status     int          // 200 or 206
	partRange  *ByteRange   // set when a legacy part head has no manifest: the part as a range of the multipart head
}

func (o *GetObject) Name() string          // "get_obj"
func (o *GetObject) Action() policy.Action // S3GetObject / S3GetObjectVersion; the Torrent variants with ?torrent
func (o *GetObject) OpMask() uint32        // OpTypeRead
func (o *GetObject) Init(ctx context.Context, r *Request) error
func (o *GetObject) VerifyPermission(ctx context.Context, r *Request) error
func (o *GetObject) Execute(ctx context.Context, r *Request) error
func (o *GetObject) Complete(ctx context.Context, r *Request)

// ParseRange is RGWGetObj::parse_range (rgw_op.cc:160-224) on a Range header
// value: ofs and end as radosgw holds them (end -1 for open, ofs negative for
// a suffix range), partial (a bytes= range was present), and ErrInvalidRange
// for what radosgw refuses; a unit other than bytes is ignored (whole object).
func ParseRange(value string) (ofs, end int64, partial bool, err error)

// RangeToOfs is RGWRados::Object::Read::range_to_ofs (rgw_rados.cc:7002-7022).
func RangeToOfs(size uint64, ofs, end int64) (uint64, uint64, error) // ErrInvalidRange for ofs >= size

// ParseHTTPTime is parse_time (rgw_common.cc:702-715): RFC 850, asctime,
// RFC 1123 (GMT/UTC or a numeric zone) or ISO 8601; ErrInvalidArgument otherwise.
func ParseHTTPTime(s string) (time.Time, error)

// Unquote is rgw_string_unquote (rgw_common.cc:518-533).
func Unquote(s string) string

// atoll is glibc atoll: leading whitespace, an optional sign, digits; 0 for none.
func atoll(s string) int64
```

Lifecycle, transcribed:

- **Init** (`rgw_build_bucket_policies`/`rgw_build_object_policies`, [rgw_op.cc:494](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L494), [:631-649](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L631-L649); `Read::prepare` [:6857-6917](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6857-L6917)): `r.BucketRec` from `Env.Buckets.GetBucket(ctx, r.Tenant, r.Bucket)`, `ErrNoSuchBucket` returned as is (radosgw: `-ERR_NO_SUCH_BUCKET` before any object read). Prefetch = `GetData && Range == ""` (`prefetch_data()`, [:2176-2191](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L2176-L2191)). Without `PartNumber`: `r.ObjState = State = CondState = Objects.PrefetchObject|StatObject(rec, r.Object)`. With `PartNumber`: the multipart head is read without prefetch (`std::exchange(prefetch_data, false)`, [:6861](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6861)); `r.ObjState = CondState = head`; when the head does not exist the op continues to `VerifyPermission` (D-Z5 answers the 404); otherwise `count := head.Manifest.PartsCount()` (0 without a manifest): `count == 0 && *PartNumber == 1` → `State = head` (whole object, no parts count: [:6893-6898](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6893-L6898)); `count == 0` → `ErrInvalidPart`; else `PartBounds(*PartNumber)` — not found → `ErrInvalidPart` ([:6783-6787](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6783-L6787)); the part head is read with the request's prefetch (`Objects.PrefetchObject|StatObject(rec, partHead.Key)`), a missing one → `ErrInvalidPart` ([:6903-6906](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6903-L6906)); the multipart head's `user.rgw.crypt.*` attrs are copied into the part's attrs where absent ([:6881-6887](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6881-L6887), [:6911-6916](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6911-L6916)); `State = partState`; when the part head has no manifest (a legacy part; radosgw synthesises one, [:6815-6844](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6815-L6844)) `State = head` and `partRange = {ofs, size}`; `PartsCount = &count`. On Tentacle, `PartsCount` is also set when `head.Manifest.PartsCount() > 0` without `PartNumber` ([T] [rgw_rados.cc:7690-7697](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7690-L7697); R-D10). `VersionID = r.Object.Instance`.
- **VerifyPermission**: `VerifyObjectPermission(ctx, r, o.Action(), acl.PermFor(o.Action()))` — Z's evaluator answers `ErrNoSuchKey`/`ErrAccessDenied` for a missing object (D-Z5). `Action()`: `S3GetObject`, `S3GetObjectVersion` when `r.Object.Instance != ""`, the `Torrent` variants when `Torrent` ([rgw_op.cc:989-1001](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L989-L1001)).
- **Execute** ([rgw_op.cc:2214-2462](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L2214-L2462) in this order): (1) `init_common`: `ParseRange` when `Range != ""` — `ErrInvalidRange` unless `rgw_ignore_get_invalid_range` (then whole object, `Partial = false`); `ParseHTTPTime` on `IfModifiedSince`/`IfUnmodifiedSince` → `ErrInvalidArgument` on failure ([:2464-2487](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L2464-L2487)). (2) `State == nil || !State.Exists` → `ErrNoSuchKey` (prepare's ENOENT; unreachable after D-Z5 but kept). (3) Conditionals ([rgw_rados.cc:6946-6990](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6946-L6990)) with `mtime = State.Mtime` (the part's after a redirect) truncated to seconds and `etag = CondState.ETag`: `IfModifiedSince != "" && IfNoneMatch == ""` and `!(since.Before(mtime))` at second precision → `ErrNotModified`; `IfUnmodifiedSince != "" && IfMatch == ""` and `since.Before(mtime)` → `ErrPreconditionFailed`; `IfMatch != ""` and `!strings.HasPrefix(Unquote(IfMatch), etag)` → `ErrPreconditionFailed` (`if_match_str.compare(0, etag.length(), etag) != 0`: the unquoted value must START with the stored etag; `*` is not special, R-D4); `IfNoneMatch != ""` and `strings.HasPrefix(Unquote(IfNoneMatch), etag)` → `ErrNotModified`. (4) `Torrent`: `State.Attrs[AttrCryptMode] == "SSE-C-AES256"` → `ErrInvalidArgument` ([:2277-2283](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L2277-L2283)); no `AttrTorrent` → `ErrNoSuchKey` (`rgw_read_torrent_file` ENOENT); else `ErrNotImplemented` (R-D8). (5) Compression: `State.Compression != nil`: `Type == "none"` or no blocks → radosgw's `rgw_compression_info_from_attr` returns -EIO for zero blocks ([rgw_compression.cc:20-22](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_compression.cc#L20-L22)) → `ErrInternalError`; else `ObjSize = Compression.OrigSize` (:2334-2339); without compression `ObjSize = State.Size`. (6) Cloud tier, only when `GetData` ([:2361-2371](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L2361-L2371)): `State.Manifest != nil` and its `TierType` is `cloud-s3` (Squid), or `cloud-s3`/`cloud-s3-glacier` on Tentacle (`is_tier_type_s3`): Squid → `ErrInvalidObjectState.WithMessage("This object was transitioned to cloud-s3")`; Tentacle ([\[T\]:989-1122](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L989-L1122)) → `RestoreStatus(State.Attrs[AttrRestoreStatus][0])` when present: `RestoreAlreadyInProgress` → `ErrRequestTimeout.WithMessage("restore is still in progress")`; `CloudRestored` → continue; otherwise look the tier up: `Env.Zone.ZoneGroup().PlacementTargets[r.BucketRec.Info.PlacementRule.Name].TierTargets[sc]` with `sc = State.StorageClass` or the bucket rule's class when empty — a tier that does not `AllowReadThrough` → `ErrInvalidObjectState.WithMessage("Read through is not enabled for this config")`; one that does → `ErrInvalidObjectState.WithMessage("This object was transitioned to cloud-s3")` plus `slog.ErrorContext(ctx, "cloud read-through restore is not implemented", …)` (R-D6). (7) `AttrUserManifest` or `AttrSLOManifest` present → `ErrNotImplemented` (R-D7; radosgw would compose the Swift large object, [:2373-2394](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L2373-L2394)), logged at info. (8) `Range != "" && ObjSize == 0` → `ErrInvalidRange` (:2397-2401). (9) `RangeToOfs(ObjSize, ofs, end)`; `Offset, Length = ofs, end+1-ofs` (or 0 when `ofs > end`); `Status = 206` when `Partial` else 200. With `partRange`, the range is applied within the part: `Offset += partRange.Offset` and `ObjSize`/`Length` bounded by `partRange.Length` (radosgw's synthesised part manifest has the part's size). (10) SSE (`rgw_s3_prepare_decrypt`, [rgw_crypt.cc:1303-1505](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_crypt.cc#L1303-L1505), R-D9), for GET and HEAD alike: `SSEHeader` → `ErrInvalidRequest`; by `State.Attrs[AttrCryptMode]`: `SSE-C-AES256`: `requireSSL && !Secure` → `ErrInvalidRequest`; `SSECAlgorithm == ""` → `ErrInvalidArgument.WithMessage("Requests specifying Server Side Encryption with Customer provided keys must provide a valid encryption algorithm.")`; `!= "AES256"` → `ErrInvalidEncryptionAlgorithm.WithMessage("The requested encryption algorithm is not valid, must be AES256.")`; key not base64 or not 32 bytes → `ErrInvalidArgument.WithMessage("Requests specifying Server Side Encryption with Customer provided keys must provide an appropriate secret key.")`; md5 not base64 or not 16 bytes → `…"must provide an appropriate secret key md5."`; `md5(key) != md5 header || md5 header bytes != State.Attrs[AttrCryptKeyMD5]` → `ErrInvalidArgument.WithMessage("The calculated MD5 hash of the key did not match the hash that was provided.")`; all valid → `ErrNotImplemented` (decryption is phase 2); `SSE-KMS`: `requireSSL && !Secure` → `ErrInvalidRequest`; else `ErrInvalidArgument.WithMessage("Failed to retrieve the actual key, kms-keyid: " + string(Attrs[AttrCryptKeyID]))`; `RGW-AUTO` → `ErrInternalError`; `AES256`: Squid `requireSSL && !Secure` → `ErrInvalidRequest`; else `ErrInvalidArgument.WithMessage("Failed to retrieve the actual key")`. `requireSSL` is `Env.Conf.Bool("rgw_crypt_require_ssl")`, true when unreadable. (11) `ResponseOverrides`: non-empty and `r.Identity.Anonymous` → `ErrInvalidRequest`; any value with a control character (`unicode.IsControl`, C's `iscntrl`) → `ErrInvalidRequest` ([rgw_rest_s3.cc:513-533](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L513-L533)). (12) `!GetData || Length == 0` → `o.Sink.WriteHeader(o.Status, nil)`; return. (13) `Env.Objects.ReadObject(ctx, State, ByteRange{Offset, Length}, o.Sink)`; on success `o.Sink.Flush()`. The handler's `Sink` wrapper renders the headers on the first `Write` or `WriteHeader`, so an error before the first byte reaches `WriteError` as radosgw's `send_response_data_error` does.
- **Complete**: `LogUsage(ctx, r, o.Name())` (rgw_log.cc; M's helper).

- [ ] **Step 1: Write the failing parsing specs**

`internal/op/readconds_test.go`:

```go
var _ = Describe("ParseRange", func() {
	DescribeTable("is RGWGetObj::parse_range",
		func(value string, ofs, end int64, partial bool, wantErr error) {
			o, e, p, err := op.ParseRange(value)
			if wantErr != nil {
				Expect(err).To(MatchError(wantErr))
				return
			}
			Expect(err).NotTo(HaveOccurred())
			Expect([]any{o, e, p}).To(Equal([]any{ofs, end, partial}))
		},
		Entry("a closed range", "bytes=0-10", int64(0), int64(10), true, nil),
		Entry("an open range", "bytes=4-", int64(4), int64(-1), true, nil),
		Entry("a suffix range", "bytes=-7", int64(-7), int64(-1), true, nil),
		Entry("spaces around the unit and equals", "  bytes = 0-1", int64(0), int64(1), true, nil),
		Entry("a unit that is not bytes is ignored", "items=0-1", int64(0), int64(-1), false, nil),
		Entry("bytes= anywhere in the value", "x-bytes=2-3", int64(2), int64(3), true, nil),
		Entry("a second range is dropped by atoll", "bytes=0-1,5-6", int64(0), int64(1), true, nil),
		Entry("atoll reads a non-number as 0", "bytes=abc-5", int64(0), int64(5), true, nil),
		Entry("end before start", "bytes=10-5", 0, 0, false, op.ErrInvalidRange),
		Entry("no dash", "bytes=5", 0, 0, false, op.ErrInvalidRange),
		Entry("negative end", "bytes=0--1", 0, 0, false, op.ErrInvalidRange),
	)
})

var _ = Describe("RangeToOfs", func() {
	DescribeTable("is RGWRados::Object::Read::range_to_ofs",
		func(size uint64, ofs, end int64, wantOfs, wantEnd uint64, wantErr error) {
			o, e, err := op.RangeToOfs(size, ofs, end)
			if wantErr != nil {
				Expect(err).To(MatchError(wantErr))
				return
			}
			Expect(err).NotTo(HaveOccurred())
			Expect([]uint64{o, e}).To(Equal([]uint64{wantOfs, wantEnd}))
		},
		Entry("open range", uint64(11), int64(4), int64(-1), uint64(4), uint64(10), nil),
		Entry("suffix", uint64(11), int64(-7), int64(-1), uint64(4), uint64(10), nil),
		Entry("suffix longer than the object", uint64(11), int64(-70), int64(-1), uint64(0), uint64(10), nil),
		Entry("end clamped", uint64(11), int64(0), int64(50), uint64(0), uint64(10), nil),
		Entry("start past the end", uint64(11), int64(40), int64(50), 0, 0, op.ErrInvalidRange),
		Entry("empty object, whole", uint64(0), int64(0), int64(-1), uint64(0), uint64(0), nil), // end wraps to size-1 only when size > 0: radosgw leaves end = -1 → total_len 0
	)
})

var _ = Describe("ParseHTTPTime", func() {
	utc := func(y int, mo time.Month, d, h, mi, s int) time.Time { return time.Date(y, mo, d, h, mi, s, 0, time.UTC) }
	DescribeTable("accepts what parse_time accepts",
		func(s string, want time.Time) {
			got, err := op.ParseHTTPTime(s)
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(BeTemporally("==", want))
		},
		Entry("RFC 1123 GMT", "Sat, 29 Oct 2100 19:43:31 GMT", utc(2100, 10, 29, 19, 43, 31)),
		Entry("RFC 1123 UTC", "Sat, 29 Oct 2100 19:43:31 UTC", utc(2100, 10, 29, 19, 43, 31)),
		Entry("RFC 1123 numeric zone", "Sat, 29 Oct 2100 19:43:31 +0100", utc(2100, 10, 29, 18, 43, 31)),
		Entry("RFC 850", "Saturday, 29-Oct-94 19:43:31 GMT", utc(1994, 10, 29, 19, 43, 31)),
		Entry("asctime", "Sat Oct 29 19:43:31 1994", utc(1994, 10, 29, 19, 43, 31)),
		Entry("ISO 8601 with Z", "2026-09-28T01:02:03Z", utc(2026, 9, 28, 1, 2, 3)),
		Entry("ISO 8601 with fraction", "2026-09-28T01:02:03.250Z", utc(2026, 9, 28, 1, 2, 3).Add(250*time.Millisecond)),
		Entry("ISO 8601 with a space", "2026-09-28 01:02:03", utc(2026, 9, 28, 1, 2, 3)),
	)
	DescribeTable("rejects the rest with InvalidArgument",
		func(s string) {
			_, err := op.ParseHTTPTime(s)
			Expect(err).To(MatchError(op.ErrInvalidArgument))
		},
		Entry("garbage", "yesterday"),
		Entry("RFC 1123 without a zone", "Sat, 29 Oct 2100 19:43:31"),
		Entry("epoch seconds", "1700000000"),
	)
})

var _ = Describe("Unquote", func() {
	DescribeTable("is rgw_string_unquote",
		func(in, want string) { Expect(op.Unquote(in)).To(Equal(want)) },
		Entry("quoted", `"abc"`, "abc"),
		Entry("unquoted", `abc`, "abc"),
		Entry("trailing spaces inside", `"abc"   `, "abc"),
		Entry("one quote", `"abc`, `"abc`),
		Entry("a lone quote", `"`, `"`),
		Entry("star", `*`, `*`),
	)
})
```

- [ ] **Step 2: Write the failing op specs**

`internal/op/getobject_test.go`, package `op_test`, with a `memstore` seeded by `alice` and bucket `plain` holding `small` (1024 bytes, etag known, content type `text/plain`, mtime `2026-09-27T01:02:03.456Z`) and `empty` (0 bytes), plus an `opfakes.FakeObjectStore` for manifest, compression, cloud-tier, SSE and partNumber cases. A `captureSink` records `WriteHeader(status)`, the bytes and `Flush`. `run(o, r)` is `op.Run`.

```go
var _ = Describe("GetObject", func() {
	var (
		store *memstore.Store
		env   *op.Env
		alice op.Identity
		sink  *captureSink
	)
	BeforeEach(func(ctx SpecContext) {
		store = memstore.New(memstore.Config{Release: denc.Squid, Now: func() time.Time { return time.Date(2026, 9, 27, 1, 2, 3, 456_000_000, time.UTC) }})
		env = &op.Env{Zone: store, Users: store, Buckets: store, Objects: store, Usage: store, Authz: op.OwnerOnly{}, Metrics: op.NopMetrics{}, Conf: cephconf.NewOptions(cephconf.MapGetter{"rgw_ignore_get_invalid_range": "false", "rgw_crypt_require_ssl": "true"})}
		a := store.AddUser(meta.UserInfo{UserID: meta.UserID{ID: "alice"}, OpMask: op.OpTypeAll})
		alice = op.Identity{User: &a.Info, Owner: meta.UserOwner(a.Info.UserID), OpMask: op.OpTypeAll}
		rec, err := store.CreateBucket(ctx, op.CreateBucketParams{Name: "plain", Owner: alice.Owner})
		Expect(err).NotTo(HaveOccurred())
		_, err = store.PutObject(ctx, rec, meta.ObjKey{Name: "small"}, bytes.NewReader(payload(1024)), op.PutParams{Size: 1024, Attrs: map[string][]byte{"user.rgw.content_type": []byte("text/plain")}})
		Expect(err).NotTo(HaveOccurred())
		_, err = store.PutObject(ctx, rec, meta.ObjKey{Name: "empty"}, bytes.NewReader(nil), op.PutParams{Size: 0})
		Expect(err).NotTo(HaveOccurred())
		sink = &captureSink{}
	})
	req := func(key string) *op.Request {
		return &op.Request{Method: "GET", Bucket: "plain", Object: meta.ObjKey{Name: key}, Identity: alice, Env: env, Header: http.Header{}, Query: url.Values{}}
	}
	get := func(key string, mut func(o *op.GetObject)) (*op.GetObject, *op.Request, error) {
		o := &op.GetObject{GetData: true, Sink: sink}
		if mut != nil {
			mut(o)
		}
		r := req(key)
		return o, r, op.Run(context.Background(), o, r)
	}

	It("serves a whole object: 200, the full length, the body, the state, prefetch used", func() {
		o, r, err := get("small", nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(o.Status).To(Equal(200))
		Expect(o.Partial).To(BeFalse())
		Expect(o.Offset).To(BeZero())
		Expect(o.Length).To(BeEquivalentTo(1024))
		Expect(o.ObjSize).To(BeEquivalentTo(1024))
		Expect(sink.status).To(Equal(200))
		Expect(sink.body.Bytes()).To(Equal(payload(1024)))
		Expect(sink.flushed).To(BeTrue())
		Expect(r.ObjState).To(Equal(o.State))
		Expect(o.State.Head).NotTo(BeNil(), "a GET without Range prefetches")
		Expect(store.Usage()).To(HaveLen(1))
		Expect(store.Usage()[0].Category).To(Equal("get_obj"))
	})
	It("HEAD stats without a prefetch, writes the header and no body", func() {
		o, _, err := get("small", func(o *op.GetObject) { o.GetData = false })
		Expect(err).NotTo(HaveOccurred())
		Expect(o.State.Head).To(BeNil())
		Expect(sink.status).To(Equal(200))
		Expect(sink.body.Len()).To(BeZero())
		Expect(o.Length).To(BeEquivalentTo(1024))
	})
	It("answers NoSuchKey for a missing key and NoSuchBucket for a missing bucket", func() {
		_, _, err := get("nope", nil)
		Expect(err).To(MatchError(op.ErrNoSuchKey))
		o := &op.GetObject{GetData: true, Sink: sink}
		r := req("small")
		r.Bucket = "nobucket"
		Expect(op.Run(context.Background(), o, r)).To(MatchError(op.ErrNoSuchBucket))
	})

	Describe("Range", func() {
		It("serves a closed range as 206 without a prefetch", func() {
			o, _, err := get("small", func(o *op.GetObject) { o.Range = "bytes=10-19" })
			Expect(err).NotTo(HaveOccurred())
			Expect(o.State.Head).To(BeNil(), "a ranged GET does not prefetch")
			Expect(o.Status).To(Equal(206))
			Expect(o.Partial).To(BeTrue())
			Expect(o.Offset).To(BeEquivalentTo(10))
			Expect(o.Length).To(BeEquivalentTo(10))
			Expect(sink.body.Bytes()).To(Equal(payload(1024)[10:20]))
		})
		It("serves a suffix range and clamps an end past the object", func() {
			o, _, err := get("small", func(o *op.GetObject) { o.Range = "bytes=-7" })
			Expect(err).NotTo(HaveOccurred())
			Expect([]uint64{o.Offset, o.Length}).To(Equal([]uint64{1017, 7}))
			o, _, err = get("small", func(o *op.GetObject) { o.Range = "bytes=1000-5000" })
			Expect(err).NotTo(HaveOccurred())
			Expect([]uint64{o.Offset, o.Length}).To(Equal([]uint64{1000, 24}))
		})
		It("answers InvalidRange past the end, for an inverted range and on an empty object", func() {
			_, _, err := get("small", func(o *op.GetObject) { o.Range = "bytes=2000-3000" })
			Expect(err).To(MatchError(op.ErrInvalidRange))
			_, _, err = get("small", func(o *op.GetObject) { o.Range = "bytes=10-5" })
			Expect(err).To(MatchError(op.ErrInvalidRange))
			_, _, err = get("empty", func(o *op.GetObject) { o.Range = "bytes=0-1" })
			Expect(err).To(MatchError(op.ErrInvalidRange))
		})
		It("serves the whole object for an invalid range when rgw_ignore_get_invalid_range is set", func() {
			env.Conf = cephconf.NewOptions(cephconf.MapGetter{"rgw_ignore_get_invalid_range": "true", "rgw_crypt_require_ssl": "true"})
			o, _, err := get("small", func(o *op.GetObject) { o.Range = "bytes=10-5" })
			Expect(err).NotTo(HaveOccurred())
			Expect(o.Status).To(Equal(200))
			Expect(o.Length).To(BeEquivalentTo(1024))
		})
		It("HEAD with a Range is 206 with the range's length and no body", func() {
			o, _, err := get("small", func(o *op.GetObject) { o.GetData, o.Range = false, "bytes=0-9" })
			Expect(err).NotTo(HaveOccurred())
			Expect(o.Status).To(Equal(206))
			Expect(o.Length).To(BeEquivalentTo(10))
			Expect(sink.body.Len()).To(BeZero())
		})
	})

	Describe("conditionals", func() {
		const etag = "<the memstore etag of payload(1024): md5 hex>"
		mtime := "Sun, 27 Sep 2026 01:02:03 GMT" // the object's mtime at second precision
		before := "Sun, 27 Sep 2026 01:02:02 GMT"
		after := "Sun, 27 Sep 2026 01:02:04 GMT"
		DescribeTable("evaluate as Read::prepare does",
			func(im, inm, ims, ius string, want error) {
				_, _, err := get("small", func(o *op.GetObject) { o.IfMatch, o.IfNoneMatch, o.IfModifiedSince, o.IfUnmodifiedSince = im, inm, ims, ius })
				if want == nil {
					Expect(err).NotTo(HaveOccurred())
				} else {
					Expect(err).To(MatchError(want))
				}
			},
			Entry("If-Match equal, quoted", `"`+etag+`"`, "", "", "", nil),
			Entry("If-Match equal, unquoted", etag, "", "", "", nil),
			Entry("If-Match with the etag as a prefix matches (compare(0, etag.length()))", `"`+etag+`-extra"`, "", "", "", nil),
			Entry("If-Match different", `"ABCORZ"`, "", "", "", op.ErrPreconditionFailed),
			Entry("If-Match star is literal (rgw_rados.cc:6975-6981)", "*", "", "", "", op.ErrPreconditionFailed),
			Entry("If-None-Match equal", "", `"`+etag+`"`, "", "", op.ErrNotModified),
			Entry("If-None-Match different", "", `"ABCORZ"`, "", "", nil),
			Entry("If-None-Match star never matches", "", "*", "", "", nil),
			Entry("If-Modified-Since before the mtime", "", "", before, "", nil),
			Entry("If-Modified-Since equal to the mtime's second", "", "", mtime, "", op.ErrNotModified),
			Entry("If-Modified-Since after", "", "", after, "", op.ErrNotModified),
			Entry("If-Modified-Since ignored when If-None-Match is present", "", `"ABCORZ"`, after, "", nil),
			Entry("If-Unmodified-Since after the mtime", "", "", "", after, nil),
			Entry("If-Unmodified-Since equal", "", "", "", mtime, nil),
			Entry("If-Unmodified-Since before", "", "", "", before, op.ErrPreconditionFailed),
			Entry("If-Unmodified-Since ignored when If-Match is present", `"`+etag+`"`, "", "", before, nil),
			Entry("If-Match wins over If-Modified-Since's 304? no: both evaluated, 304 first", `"`+etag+`"`, "", after, "", op.ErrNotModified),
			Entry("an unparsable date is InvalidArgument", "", "", "yesterday", "", op.ErrInvalidArgument),
		)
	})

	Describe("on a fake store", func() {
		var objects *opfakes.FakeObjectStore
		state := func(mut func(st *op.ObjectState)) *op.ObjectState {
			st := &op.ObjectState{Exists: true, Size: 10 << 20, Mtime: time.Unix(1700000000, 0), ETag: "e", Attrs: map[string][]byte{}, Key: meta.ObjKey{Name: "k"}}
			if mut != nil {
				mut(st)
			}
			return st
		}
		BeforeEach(func() {
			objects = &opfakes.FakeObjectStore{}
			env.Objects = objects
		})
		serve := func(st *op.ObjectState, mut func(o *op.GetObject)) (*op.GetObject, error) {
			objects.StatObjectReturns(st, nil)
			objects.PrefetchObjectReturns(st, nil)
			o, _, err := get("k", mut)
			return o, err
		}

		It("reports orig_size for a compressed object and asks ReadObject for the decompressed range", func() {
			st := state(func(st *op.ObjectState) {
				st.Size = 5000
				st.Compression = &meta.CompressionInfo{Type: "zlib", OrigSize: 10 << 20, Blocks: []meta.CompressionBlock{{Len: 5000}}}
			})
			o, err := serve(st, func(o *op.GetObject) { o.Range = "bytes=100-199" })
			Expect(err).NotTo(HaveOccurred())
			Expect(o.ObjSize).To(BeEquivalentTo(10 << 20))
			_, _, rng, _ := objects.ReadObjectArgsForCall(0)
			Expect(rng).To(Equal(op.ByteRange{Offset: 100, Length: 100}))
		})
		It("refuses compression info without blocks as radosgw's -EIO", func() {
			_, err := serve(state(func(st *op.ObjectState) { st.Compression = &meta.CompressionInfo{Type: "zlib", OrigSize: 1} }), nil)
			Expect(err).To(MatchError(op.ErrInternalError))
		})

		Describe("cloud tier", func() {
			tiered := func(sc string, restore *op.RestoreStatusForTest) *op.ObjectState {
				return state(func(st *op.ObjectState) {
					m := meta.NewManifest()
					m.TierType = meta.TierTypeCloudS3
					st.Manifest = &m
					st.StorageClass = sc
					if restore != nil {
						st.Attrs[meta.AttrRestoreStatus] = []byte{byte(*restore)}
					}
				})
			}
			It("Squid: 403 InvalidObjectState on GET, headers on HEAD", func() {
				_, err := serve(tiered("CLOUDTIER", nil), nil)
				Expect(err).To(MatchError(op.ErrInvalidObjectState))
				Expect(op.AsError(err).Message).To(Equal("This object was transitioned to cloud-s3"))
				_, err = serve(tiered("CLOUDTIER", nil), func(o *op.GetObject) { o.GetData = false })
				Expect(err).NotTo(HaveOccurred())
			})
			Context("Tentacle", func() {
				BeforeEach(func() {
					zone := &opfakes.FakeZoneInfo{}
					zone.ReleaseReturns(denc.Tentacle)
					zg := meta.ZoneGroup{PlacementTargets: map[string]meta.ZoneGroupPlacementTarget{"default-placement": {TierTargets: map[string]meta.ZoneGroupPlacementTier{
						"CLOUDTIER": {TierType: meta.TierTypeCloudS3, StorageClass: "CLOUDTIER"},
						"READTHRU":  {TierType: meta.TierTypeCloudS3, StorageClass: "READTHRU", AllowReadThrough: true},
					}}}}
					zone.ZoneGroupReturns(zg)
					env.Zone = zone
				})
				It("a tier without read-through: 403 with radosgw's message", func() {
					_, err := serve(tiered("CLOUDTIER", nil), nil)
					Expect(op.AsError(err).Message).To(Equal("Read through is not enabled for this config"))
				})
				It("a tier with read-through: 403, restore not implemented", func() {
					_, err := serve(tiered("READTHRU", nil), nil)
					Expect(err).To(MatchError(op.ErrInvalidObjectState))
					Expect(op.AsError(err).Message).To(Equal("This object was transitioned to cloud-s3"))
				})
				It("a restore in progress: 408", func() {
					rs := op.RestoreStatusForTest(meta.RestoreAlreadyInProgress)
					_, err := serve(tiered("CLOUDTIER", &rs), nil)
					Expect(err).To(MatchError(op.ErrRequestTimeout))
					Expect(op.AsError(err).Message).To(Equal("restore is still in progress"))
				})
				It("a restored object is served", func() {
					rs := op.RestoreStatusForTest(meta.CloudRestored)
					_, err := serve(tiered("CLOUDTIER", &rs), nil)
					Expect(err).NotTo(HaveOccurred())
				})
			})
		})

		It("refuses Swift large objects with NotImplemented", func() {
			for _, a := range []string{meta.AttrUserManifest, meta.AttrSLOManifest} {
				_, err := serve(state(func(st *op.ObjectState) { st.Attrs[a] = []byte("x") }), nil)
				Expect(err).To(MatchError(op.ErrNotImplemented), a)
			}
		})
		It("?torrent: NoSuchKey without the attr, NotImplemented with it, InvalidArgument for SSE-C", func() {
			_, err := serve(state(nil), func(o *op.GetObject) { o.Torrent = true })
			Expect(err).To(MatchError(op.ErrNoSuchKey))
			_, err = serve(state(func(st *op.ObjectState) { st.Attrs[meta.AttrTorrent] = []byte("d") }), func(o *op.GetObject) { o.Torrent = true })
			Expect(err).To(MatchError(op.ErrNotImplemented))
			_, err = serve(state(func(st *op.ObjectState) { st.Attrs[meta.AttrCryptMode] = []byte("SSE-C-AES256") }), func(o *op.GetObject) { o.Torrent = true })
			Expect(err).To(MatchError(op.ErrInvalidArgument))
		})

		Describe("SSE", func() {
			key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
			sum := md5.Sum(bytes.Repeat([]byte{1}, 32))
			keyMD5 := base64.StdEncoding.EncodeToString(sum[:])
			encrypted := func(mode string) *op.ObjectState {
				return state(func(st *op.ObjectState) {
					st.Attrs[meta.AttrCryptMode] = []byte(mode)
					st.Attrs[meta.AttrCryptKeyMD5] = sum[:]
					st.Attrs[meta.AttrCryptKeyID] = []byte("kid")
				})
			}
			DescribeTable("answers as rgw_s3_prepare_decrypt does",
				func(mode string, mut func(o *op.GetObject), want error, msg string) {
					_, err := serve(encrypted(mode), func(o *op.GetObject) { o.Secure = true; if mut != nil { mut(o) } })
					Expect(err).To(MatchError(want))
					if msg != "" {
						Expect(op.AsError(err).Message).To(Equal(msg))
					}
				},
				Entry("an x-amz-server-side-encryption header on GET", "SSE-C-AES256", func(o *op.GetObject) { o.SSEHeader = true }, op.ErrInvalidRequest, ""),
				Entry("SSE-C without the algorithm", "SSE-C-AES256", nil, op.ErrInvalidArgument, "Requests specifying Server Side Encryption with Customer provided keys must provide a valid encryption algorithm."),
				Entry("SSE-C with the wrong algorithm", "SSE-C-AES256", func(o *op.GetObject) { o.SSECAlgorithm = "AES128" }, op.ErrInvalidEncryptionAlgorithm, "The requested encryption algorithm is not valid, must be AES256."),
				Entry("SSE-C with a short key", "SSE-C-AES256", func(o *op.GetObject) { o.SSECAlgorithm, o.SSECKey = "AES256", "c2hvcnQ=" }, op.ErrInvalidArgument, "Requests specifying Server Side Encryption with Customer provided keys must provide an appropriate secret key."),
				Entry("SSE-C with a bad md5", "SSE-C-AES256", func(o *op.GetObject) { o.SSECAlgorithm, o.SSECKey, o.SSECKeyMD5 = "AES256", key, "!!" }, op.ErrInvalidArgument, "Requests specifying Server Side Encryption with Customer provided keys must provide an appropriate secret key md5."),
				Entry("SSE-C with a mismatching md5", "SSE-C-AES256", func(o *op.GetObject) { o.SSECAlgorithm, o.SSECKey, o.SSECKeyMD5 = "AES256", key, base64.StdEncoding.EncodeToString(make([]byte, 16)) }, op.ErrInvalidArgument, "The calculated MD5 hash of the key did not match the hash that was provided."),
				Entry("SSE-C with valid headers: decryption is not implemented", "SSE-C-AES256", func(o *op.GetObject) { o.SSECAlgorithm, o.SSECKey, o.SSECKeyMD5 = "AES256", key, keyMD5 }, op.ErrNotImplemented, ""),
				Entry("SSE-KMS: no key server", "SSE-KMS", nil, op.ErrInvalidArgument, "Failed to retrieve the actual key, kms-keyid: kid"),
				Entry("SSE-S3: no vault", "AES256", nil, op.ErrInvalidArgument, "Failed to retrieve the actual key"),
				Entry("RGW-AUTO: no default key", "RGW-AUTO", nil, op.ErrInternalError, ""),
			)
			It("refuses SSE-C and SSE-KMS over plain HTTP when rgw_crypt_require_ssl is set", func() {
				for _, mode := range []string{"SSE-C-AES256", "SSE-KMS", "AES256"} {
					_, err := serve(encrypted(mode), func(o *op.GetObject) { o.Secure = false })
					Expect(err).To(MatchError(op.ErrInvalidRequest), mode)
				}
			})
			It("HEAD runs the same checks", func() {
				_, err := serve(encrypted("SSE-KMS"), func(o *op.GetObject) { o.GetData, o.Secure = false, true })
				Expect(err).To(MatchError(op.ErrInvalidArgument))
			})
		})

		Describe("partNumber", func() {
			multipart := func() *op.ObjectState {
				m := decodeGoldenManifest("squid-multipart")
				return state(func(st *op.ObjectState) { st.Manifest = &m; st.Size = m.ObjSize; st.ETag = "mp-3"; st.Attrs[meta.AttrCryptKeyID] = []byte("k") })
			}
			partHead := func(exists bool) *op.ObjectState {
				pm := meta.NewManifest()
				pm.ObjSize = 8 << 20
				return &op.ObjectState{Exists: exists, Size: 8 << 20, ETag: "part2", Manifest: &pm, Attrs: map[string][]byte{}, Mtime: time.Unix(1700000100, 0)}
			}
			It("redirects to the part head, serves the part with the count, and copies the crypt attrs", func() {
				objects.StatObjectReturns(multipart(), nil)
				objects.PrefetchObjectReturns(partHead(true), nil)
				n := 2
				o, _, err := get("k", func(o *op.GetObject) { o.PartNumber = &n })
				Expect(err).NotTo(HaveOccurred())
				Expect(objects.StatObjectCallCount()).To(Equal(1), "the multipart head, without prefetch")
				Expect(objects.PrefetchObjectCallCount()).To(Equal(1), "the part head, with the request's prefetch")
				_, _, key := objects.PrefetchObjectArgsForCall(0)
				Expect(key).To(Equal(meta.ObjKey{Name: "multipart.bin.2~vXcCS0UgSATwQOecOwRkZrWyEyux7jO.2", NS: meta.NSMultipart}))
				Expect(*o.PartsCount).To(Equal(3))
				Expect(o.State.ETag).To(Equal("part2"), "the part head's attrs (Read::prepare, rgw_rados.cc:6880-6933)")
				Expect(o.CondState.ETag).To(Equal("mp-3"))
				Expect(o.State.Attrs).To(HaveKeyWithValue(meta.AttrCryptKeyID, []byte("k")))
				Expect(o.Length).To(BeEquivalentTo(8 << 20))
				_, st, rng, _ := objects.ReadObjectArgsForCall(0)
				Expect(st.ETag).To(Equal("part2"))
				Expect(rng).To(Equal(op.ByteRange{Offset: 0, Length: 8 << 20}))
			})
			It("a part past the count, a part on a single-part object, and a missing part head are InvalidPart", func() {
				objects.StatObjectReturns(multipart(), nil)
				n := 5
				_, _, err := get("k", func(o *op.GetObject) { o.PartNumber = &n })
				Expect(err).To(MatchError(op.ErrInvalidPart))
				objects.StatObjectReturns(state(nil), nil)
				n = 2
				_, _, err = get("k", func(o *op.GetObject) { o.PartNumber = &n })
				Expect(err).To(MatchError(op.ErrInvalidPart))
				objects.StatObjectReturns(multipart(), nil)
				objects.PrefetchObjectReturns(partHead(false), nil)
				_, _, err = get("k", func(o *op.GetObject) { o.PartNumber = &n })
				Expect(err).To(MatchError(op.ErrInvalidPart))
			})
			It("part 1 of a single-part object is the whole object without a parts count", func() {
				objects.StatObjectReturns(state(nil), nil)
				n := 1
				o, _, err := get("k", func(o *op.GetObject) { o.PartNumber = &n })
				Expect(err).NotTo(HaveOccurred())
				Expect(o.PartsCount).To(BeNil())
				Expect(o.Length).To(BeEquivalentTo(10 << 20))
			})
			It("Tentacle reports the parts count on a plain GET of a multipart object; Squid does not", func() {
				objects.PrefetchObjectReturns(multipart(), nil)
				o, _, err := get("k", nil)
				Expect(err).NotTo(HaveOccurred())
				Expect(o.PartsCount).To(BeNil())
				zone := &opfakes.FakeZoneInfo{}
				zone.ReleaseReturns(denc.Tentacle)
				env.Zone = zone
				o, _, err = get("k", nil)
				Expect(err).NotTo(HaveOccurred())
				Expect(*o.PartsCount).To(Equal(3))
			})
		})

		It("refuses response-* overrides from an anonymous request and with control characters", func() {
			_, err := serve(state(nil), func(o *op.GetObject) { o.ResponseOverrides = map[string]string{"Content-Type": "a\r\nb"} })
			Expect(err).To(MatchError(op.ErrInvalidRequest))
			o := &op.GetObject{GetData: true, Sink: sink, ResponseOverrides: map[string]string{"Content-Type": "x"}}
			r := req("k")
			r.Identity = op.Anonymous()
			objects.PrefetchObjectReturns(state(nil), nil)
			// OwnerOnly denies anonymous; use a permissive authorizer
			env.Authz = allowAll{}
			Expect(op.Run(context.Background(), o, r)).To(MatchError(op.ErrInvalidRequest))
		})

		It("returns a read error that happens before the first byte, and the sink saw no header", func() {
			objects.PrefetchObjectReturns(state(nil), nil)
			objects.ReadObjectReturns(op.ErrNoSuchKey)
			_, err := get("k", nil)
			Expect(err).To(MatchError(op.ErrNoSuchKey))
			Expect(sink.status).To(BeZero())
		})
		It("uses the torrent actions and the version action", func() {
			objects.PrefetchObjectReturns(state(nil), nil)
			o := &op.GetObject{GetData: true, Torrent: true}
			Expect(o.Action()).To(Equal(policy.S3GetObjectTorrent))
			r := req("k")
			r.Object.Instance = "v1"
			// Action reads the request; expose it as Action() after Init sets o.versioned, or give Action the instance through a field the handler sets.
		})
	})
})
```

(`Action()` takes no request: `Init` records `o.versioned = r.Object.Instance != ""`; the handler may also set `Versioned` before `Run` — make it an exported input `Versioned bool` the handler sets from `r.Object.Instance`, so `Action()` is right before `Init` too. `allowAll` is a three-method `op.Authorizer` returning nil. `op.RestoreStatusForTest` is an alias for `meta.RestoreStatus` — or use `meta.RestoreStatus` directly and drop the alias.)

- [ ] **Step 3: Run to fail; write `readconds.go` and `getobject.go`**

`readconds.go`:

```go
package op

// ParseRange is RGWGetObj::parse_range (rgw_op.cc:160-224).
func ParseRange(value string) (ofs, end int64, partial bool, err error) {
	rs := value
	if i := strings.Index(rs, "bytes="); i >= 0 {
		rs = rs[i+len("bytes="):]
	} else {
		p := 0
		for p < len(rs) && isSpace(rs[p]) {
			p++
		}
		e := p
		for e < len(rs) && isAlpha(rs[e]) {
			e++
		}
		if !strings.EqualFold(rs[p:e], "bytes"[:min(e-p, 5)]) || e-p == 0 { // strncasecmp(rs, "bytes", end-pos): a prefix of "bytes" passes, as the C++ does
			return 0, -1, false, nil
		}
		for e < len(rs) && isSpace(rs[e]) {
			e++
		}
		if e >= len(rs) || rs[e] != '=' {
			return 0, -1, false, nil
		}
		rs = rs[e+1:]
	}
	dash := strings.IndexByte(rs, '-')
	if dash < 0 {
		return 0, 0, false, ErrInvalidRange
	}
	partial = true
	ofsStr, endStr := rs[:dash], rs[dash+1:]
	end = -1
	if endStr != "" {
		end = atoll(endStr)
		if end < 0 {
			return 0, 0, false, ErrInvalidRange
		}
	}
	if ofsStr != "" {
		ofs = atoll(ofsStr)
	} else { // RFC 2616 suffix-byte-range-spec
		ofs, end = -end, -1
	}
	if end >= 0 && end < ofs {
		return 0, 0, false, ErrInvalidRange
	}
	return ofs, end, true, nil
}
```

(`strncasecmp(rs.c_str(), "bytes", end - pos)` compares only `end-pos` characters, so the word "byt" passes; keep that. `isSpace`/`isAlpha` are C's `isspace`/`isalpha` on ASCII.)

```go
// RangeToOfs is RGWRados::Object::Read::range_to_ofs (rgw_rados.cc:7002-7022).
func RangeToOfs(size uint64, ofs, end int64) (uint64, uint64, error) {
	if ofs < 0 {
		ofs += int64(size)
		if ofs < 0 {
			ofs = 0
		}
		end = int64(size) - 1
	} else if end < 0 {
		end = int64(size) - 1
	}
	if size > 0 {
		if ofs >= int64(size) {
			return 0, 0, ErrInvalidRange
		}
		if end >= int64(size) {
			end = int64(size) - 1
		}
	}
	if end < ofs { // size 0: total_len is 0
		return uint64(ofs), uint64(ofs), nil // callers compute Length as 0 when end < ofs; see GetObject
	}
	return uint64(ofs), uint64(end), nil
}

// atoll is glibc's: optional whitespace, sign, digits; 0 when none; saturates.
func atoll(s string) int64 { /* strconv on the longest numeric prefix; overflow → math.MaxInt64/MinInt64 */ }

// Unquote is rgw_string_unquote (rgw_common.cc:518-533).
func Unquote(s string) string {
	if len(s) < 2 || s[0] != '"' {
		return s
	}
	n := len(s)
	for n > 2 && s[n-1] == ' ' {
		n--
	}
	if s[n-1] != '"' {
		return s
	}
	return s[1 : n-1]
}

// httpTimeLayouts are parse_time's strptime formats (rgw_common.cc:566-597, :599-659).
var httpTimeLayouts = []string{
	"Monday, 02-Jan-06 15:04:05 MST", "Monday, 2-Jan-06 15:04:05 MST", // RFC 850, GMT/UTC checked below
	"Mon Jan _2 15:04:05 2006", "Mon Jan 2 15:04:05 2006",             // asctime
	"Mon, 02 Jan 2006 15:04:05 MST", "Mon, 2 Jan 2006 15:04:05 MST",   // RFC 1123
	"Mon, 02 Jan 2006 15:04:05 -0700", "Mon, 2 Jan 2006 15:04:05 -0700", // RFC 1123 with %z
	"2006-01-02T15:04:05Z07:00", "2006-01-02T15:04:05.999999999Z07:00", "2006-01-02T15:04:05", "2006-01-02T15:04:05.999999999",
	"2006-01-02 15:04:05", "2006-01-02 15:04:05.999999999",
}

// ParseHTTPTime is parse_time. Layouts with MST accept only GMT and UTC, as
// check_gmt_end does; the result is UTC at nanosecond precision, the
// comparison truncates to seconds (obj_time_weight::compare_low_precision).
func ParseHTTPTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	for _, layout := range httpTimeLayouts {
		t, err := time.ParseInLocation(layout, s, time.UTC)
		if err != nil {
			continue
		}
		if strings.Contains(layout, "MST") {
			zone, _ := t.Zone()
			if zone != "GMT" && zone != "UTC" {
				continue
			}
		}
		return t.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("%w: cannot parse time %q", ErrInvalidArgument, s)
}
```

`getobject.go` implements the lifecycle exactly as listed under Interfaces; the conditionals in one function:

```go
// checkConditions is Read::prepare's conditional block (rgw_rados.cc:6946-6990)
// at second precision (S3 requests are never high_precision_time).
func (o *GetObject) checkConditions(since, unmodSince *time.Time) error {
	mtime := o.State.Mtime.Truncate(time.Second)
	if since != nil && o.IfNoneMatch == "" && !since.Truncate(time.Second).Before(mtime) {
		return ErrNotModified
	}
	if unmodSince != nil && o.IfMatch == "" && unmodSince.Truncate(time.Second).Before(mtime) {
		return ErrPreconditionFailed
	}
	etag := o.CondState.ETag
	if o.IfMatch != "" && !strings.HasPrefix(Unquote(o.IfMatch), etag) {
		return ErrPreconditionFailed
	}
	if o.IfNoneMatch != "" && strings.HasPrefix(Unquote(o.IfNoneMatch), etag) {
		return ErrNotModified
	}
	return nil
}
```

`Execute` ends with:

```go
	if !o.GetData || o.Length == 0 {
		o.Sink.WriteHeader(o.Status, nil)
		return nil
	}
	if err := r.Env.Objects.ReadObject(ctx, o.State, ByteRange{Offset: o.Offset, Length: o.Length}, o.Sink); err != nil {
		return err
	}
	return o.Sink.Flush()
```

The handler's `Sink` (Task 7) writes the headers on the first `Write` or on `WriteHeader`, so the op never renders protocol headers.

- [ ] **Step 4: Run the suite and the lint**

Run: `go test -tags=ceph_preview -race ./internal/op/ && golangci-lint run ./internal/op/`
Expected: PASS.

- [ ] **Step 5: Commit**

```sh
git add internal/op/getobject.go internal/op/readconds.go internal/op/getobject_test.go internal/op/readconds_test.go
git commit -m "feat(op): add GetObject with radosgw's ranges, conditionals, partNumber and refusals"
```

Open the draft PR `feat(op): GetObject (unit R task 5)`; the description names R-D4 (`If-Match: *`) and R-D17 (the part head's attrs).

---

### Task 6: `op`: `GetObjectTagging`, `GetObjectACL`, `GetObjectAttributes`

**Files:**
- Create: `internal/op/objectread.go`, `internal/op/objectread_test.go`

**Interfaces:**
- Consumes: Task 5's `GetObject` (GetObjectAttributes embeds its Init/refusals), `ParseHTTPTime` is not needed here; Z's `tags.Decode`, `tags.Set`, `acl.DecodePolicy`, `acl.Policy.MarshalS3XML`, `acl.DefaultPolicy(owner meta.Owner, displayName string)`, `policy.S3GetObjectTagging`, `S3GetObjectVersionTagging`, `S3GetObjectAcl`, `S3GetObjectVersionAcl`, `S3GetObject`, `S3GetObjectVersion`; M's `op.BucketACLFor(rec)` (the bucket owner's display name comes from the bucket ACL's owner, as radosgw's `s->bucket_owner` does) and `op.LogUsage`; Task 1's `Manifest.PartsCount`, `PartBounds`, `Seek`.
- Produces:

```go
package op

// GetObjectTagging is RGWGetObjTags (rgw_op.cc:1037-1074).
type GetObjectTagging struct {
	Versioned bool
	// Results.
	HasTags bool
	Tags    tags.Set
}

// GetObjectACL is RGWGetACLs at object scope (rgw_op.cc:5695-5734 with
// get_obj_policy_from_attr :288-325): the stored user.rgw.acl, or the bucket
// owner's default policy when the head has none ("couldn't find acl header
// for object, generating default").
type GetObjectACL struct {
	Versioned bool
	// Results.
	Policy acl.Policy
}

// GetObjectAttributes is RGWGetObjAttrs ([T] rgw_op.cc:6337-6403,
// rgw_rest_s3.cc:3941-4137), served on both releases although a Squid
// radosgw answers ?attributes as GetObject. It is a GetObject
// without data: Init, the SSE checks and the cloud-tier rule are GetObject's.
type GetObjectAttributes struct {
	GetObject                  // GetData false; the handler sets Versioned, SSE*, Secure
	Requested  ObjectAttrs     // x-amz-object-attributes
	MaxParts   *int            // x-amz-max-parts, capped at 1000; nil when absent
	PartMarker *int            // x-amz-part-number-marker; nil when absent
	// Results.
	Parts              []ObjectPart // when Requested has ObjectParts and the object is multipart
	PartsTruncated     bool
	NextPartMarker     int
}

// ObjectAttrs is RGWGetObjAttrs::ReqAttributes as flags.
type ObjectAttrs uint16
const (
	AttrETag ObjectAttrs = 1 << iota
	AttrChecksum
	AttrObjectParts
	AttrStorageClass
	AttrObjectSize
)
// ParseObjectAttrs is recognize_attrs ([T]:6337-6359): comma-separated, case-insensitive names; unknown names are ignored.
func ParseObjectAttrs(header string) ObjectAttrs

// ObjectPart is one <Part> of ObjectParts: number and size ([T] rgw_rest_s3.cc:4087-4096; checksums are not implemented yet).
type ObjectPart struct {
	Number int
	Size   uint64
}
```

Semantics:

- **GetObjectTagging**: `Init` loads the bucket (`ErrNoSuchBucket`) and `r.ObjState = Objects.StatObject(...)`; `VerifyPermission` = `VerifyObjectPermission(S3GetObjectTagging|S3GetObjectVersionTagging, acl.PermFor(...))`; `Execute`: a missing object is `ErrNoSuchKey` (`get_obj_attrs` ENOENT); `tags.Attr` present → `HasTags = true`, `Tags = tags.Decode(...)` with a decode failure → `ErrInternalError` (radosgw -EIO, [rgw_rest_s3.cc:760-766](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L760-L766)). `Name()` `"get_obj_tags"`.
- **GetObjectACL**: `Init` as above; `VerifyPermission` = `S3GetObjectAcl|S3GetObjectVersionAcl`; `Execute`: missing → `ErrNoSuchKey`; `user.rgw.acl` present → `acl.DecodePolicy` (failure → `ErrInternalError`); absent → `acl.DefaultPolicy(bucketACL.Owner.ID, bucketACL.Owner.DisplayName)` from `BucketACLFor(r.BucketRec)`, logged at warn "couldn't find acl header for object, generating default". `Name()` `"get_acls"`.
- **GetObjectAttributes**: `Name()` `"get_obj_attrs"`; `Action()` `S3GetObject|S3GetObjectVersion` (Z: `s3GetObjectAttributes` is or-ed at [T] and never decides alone); `Init` = `GetObject.Init` with `GetData = false` (no prefetch, radosgw's `RGWGetObj::get_data = false`); `VerifyPermission` = `VerifyObjectPermission(S3GetObject…)`; `Execute` runs `GetObject.Execute`'s steps (1)-(10) with `Range == ""` (no `Torrent`, no `ResponseOverrides`), which yields `ObjSize`, `State`, `PartsCount` (set on both releases here: the count feeds `ObjectParts` and radosgw's Tentacle code computes it for every multipart manifest) and the SSE refusals; then, when `Requested&AttrObjectParts != 0` and `PartsCount != nil && *PartsCount > 0`: `list_parts` ([T] [rgw_sal_rados.cc:2834-2933](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L2834-L2933)): `max := 1000` or `*MaxParts` (capped at 1000 by the handler), `marker := 0` or `*PartMarker`; `marker > count-1` → no parts; for part `n := marker+1 … count`: stop with `PartsTruncated = true` when `max` is exhausted; else `ofs, size, head, ok := Manifest.PartBounds(n)`; for an uncompressed object `Size = size`; for a compressed object read the part head (`Objects.StatObject(rec, head.Key)`) and use its `Compression.OrigSize` (radosgw's `accounted_size`; R-D11), a missing head ends the list as radosgw's `get_part_obj_state` failure does (logged, `ret < 0` breaks the loop, the parts so far are returned); `NextPartMarker = n`. The op never calls `ReadObject` and writes nothing to a sink (the handler renders the XML). `Complete` → `LogUsage`.

- [ ] **Step 1: Write the failing specs**

`internal/op/objectread_test.go` (package `op_test`, the memstore fixture of Task 5 plus `opfakes.FakeObjectStore` cases):

```go
var _ = Describe("GetObjectTagging", func() {
	It("returns an empty set for an object without tags and the decoded set with them", func(ctx SpecContext) {
		o := &op.GetObjectTagging{}
		Expect(op.Run(ctx, o, req("small"))).To(Succeed())
		Expect(o.HasTags).To(BeFalse())
		set := tags.Set{}
		Expect(set.Add("k", "v", tags.MaxObjectTags)).To(Succeed())
		Expect(store.SetObjectAttrs(ctx, stateOf("small"), map[string][]byte{tags.Attr: encodeTags(set)}, nil)).To(Succeed())
		o = &op.GetObjectTagging{}
		Expect(op.Run(ctx, o, req("small"))).To(Succeed())
		Expect(o.HasTags).To(BeTrue())
		Expect(o.Tags.Tags).To(Equal([]tags.Tag{{Key: "k", Value: "v"}}))
		Expect(store.Usage()).To(HaveLen(2))
		Expect(store.Usage()[1].Category).To(Equal("get_obj_tags"))
	})
	It("is NoSuchKey for a missing object and uses the tagging action", func(ctx SpecContext) {
		o := &op.GetObjectTagging{}
		Expect(op.Run(ctx, o, req("nope"))).To(MatchError(op.ErrNoSuchKey))
		Expect(o.Action()).To(Equal(policy.S3GetObjectTagging))
		Expect((&op.GetObjectTagging{Versioned: true}).Action()).To(Equal(policy.S3GetObjectVersionTagging))
	})
})

var _ = Describe("GetObjectACL", func() {
	It("returns the stored policy", func(ctx SpecContext) {
		o := &op.GetObjectACL{}
		Expect(op.Run(ctx, o, req("small"))).To(Succeed())
		Expect(o.Policy.Owner.ID).To(Equal("alice"))
		Expect(o.Policy.MarshalS3XML()).To(ContainSubstring("<Permission>FULL_CONTROL</Permission>"))
	})
	It("generates the bucket owner's default policy when the head has no acl attr", func(ctx SpecContext) {
		Expect(store.SetObjectAttrs(ctx, stateOf("small"), nil, []string{meta.AttrACL})).To(Succeed())
		o := &op.GetObjectACL{}
		Expect(op.Run(ctx, o, req("small"))).To(Succeed())
		Expect(o.Policy).To(Equal(acl.DefaultPolicy(alice.Owner, "Alice")))
	})
	It("is NoSuchKey for a missing object and InternalError for a corrupt acl", func(ctx SpecContext) {
		Expect(op.Run(ctx, &op.GetObjectACL{}, req("nope"))).To(MatchError(op.ErrNoSuchKey))
		Expect(store.SetObjectAttrs(ctx, stateOf("small"), map[string][]byte{meta.AttrACL: {0xff}}, nil)).To(Succeed())
		Expect(op.Run(ctx, &op.GetObjectACL{}, req("small"))).To(MatchError(op.ErrInternalError))
	})
})

var _ = Describe("GetObjectAttributes", func() {
	DescribeTable("ParseObjectAttrs is recognize_attrs",
		func(h string, want op.ObjectAttrs) { Expect(op.ParseObjectAttrs(h)).To(Equal(want)) },
		Entry("all five, mixed case", "ETag,Checksum,objectparts,StorageClass,ObjectSize", op.AttrETag|op.AttrChecksum|op.AttrObjectParts|op.AttrStorageClass|op.AttrObjectSize),
		Entry("unknown names are ignored", "ETag,Bogus", op.AttrETag),
		Entry("empty", "", op.ObjectAttrs(0)),
	)
	It("stats without prefetch, reports the size, etag and no parts for a single-part object", func(ctx SpecContext) {
		o := &op.GetObjectAttributes{Requested: op.AttrETag | op.AttrObjectSize | op.AttrObjectParts | op.AttrStorageClass}
		Expect(op.Run(ctx, o, req("small"))).To(Succeed())
		Expect(o.State.Head).To(BeNil())
		Expect(o.ObjSize).To(BeEquivalentTo(1024))
		Expect(o.PartsCount).To(BeNil())
		Expect(o.Parts).To(BeEmpty())
		Expect(store.Usage()[0].Category).To(Equal("get_obj_attrs"))
	})
	It("lists a multipart object's parts from the manifest, paged by max-parts and marker", func(ctx SpecContext) {
		objects := &opfakes.FakeObjectStore{}
		env.Objects = objects
		m := decodeGoldenManifest("squid-multipart")
		objects.StatObjectReturns(&op.ObjectState{Exists: true, Size: m.ObjSize, Manifest: &m, ETag: "mp-3", Attrs: map[string][]byte{}}, nil)
		o := &op.GetObjectAttributes{Requested: op.AttrObjectParts}
		Expect(op.Run(ctx, o, req("k"))).To(Succeed())
		Expect(*o.PartsCount).To(Equal(3))
		Expect(o.Parts).To(Equal([]op.ObjectPart{{1, 8 << 20}, {2, 8 << 20}, {3, 4 << 20}}))
		Expect(o.PartsTruncated).To(BeFalse())
		Expect(objects.StatObjectCallCount()).To(Equal(1), "no part head is read for an uncompressed object: its sizes come from the manifest")

		two, one := 2, 1
		o = &op.GetObjectAttributes{Requested: op.AttrObjectParts, MaxParts: &two}
		Expect(op.Run(ctx, o, req("k"))).To(Succeed())
		Expect(o.Parts).To(HaveLen(2))
		Expect(o.PartsTruncated).To(BeTrue())
		Expect(o.NextPartMarker).To(Equal(2))
		o = &op.GetObjectAttributes{Requested: op.AttrObjectParts, PartMarker: &one}
		Expect(op.Run(ctx, o, req("k"))).To(Succeed())
		Expect(o.Parts).To(Equal([]op.ObjectPart{{2, 8 << 20}, {3, 4 << 20}}))
		three := 3
		o = &op.GetObjectAttributes{Requested: op.AttrObjectParts, PartMarker: &three}
		Expect(op.Run(ctx, o, req("k"))).To(Succeed())
		Expect(o.Parts).To(BeEmpty())
	})
	It("reads each part head for a compressed multipart object and reports the decoded sizes", func(ctx SpecContext) {
		objects := &opfakes.FakeObjectStore{}
		env.Objects = objects
		m := decodeGoldenManifest("squid-multipart")
		head := &op.ObjectState{Exists: true, Size: m.ObjSize, Manifest: &m, Compression: &meta.CompressionInfo{Type: "zlib", OrigSize: 60 << 20, Blocks: []meta.CompressionBlock{{Len: 1}}}, Attrs: map[string][]byte{}}
		objects.StatObjectReturnsOnCall(0, head, nil)
		for i := 1; i <= 3; i++ {
			objects.StatObjectReturnsOnCall(i, &op.ObjectState{Exists: true, Compression: &meta.CompressionInfo{Type: "zlib", OrigSize: uint64(i) * 10 << 20, Blocks: []meta.CompressionBlock{{Len: 1}}}, Attrs: map[string][]byte{}}, nil)
		}
		o := &op.GetObjectAttributes{Requested: op.AttrObjectParts | op.AttrObjectSize}
		Expect(op.Run(ctx, o, req("k"))).To(Succeed())
		Expect(o.ObjSize).To(BeEquivalentTo(60 << 20))
		Expect(o.Parts).To(Equal([]op.ObjectPart{{1, 10 << 20}, {2, 20 << 20}, {3, 30 << 20}}))
		Expect(objects.StatObjectCallCount()).To(Equal(4))
		_, _, key := objects.StatObjectArgsForCall(1)
		Expect(key.NS).To(Equal(meta.NSMultipart))
	})
	It("applies the SSE refusals like GetObject", func(ctx SpecContext) {
		objects := &opfakes.FakeObjectStore{}
		env.Objects = objects
		objects.StatObjectReturns(&op.ObjectState{Exists: true, Size: 3, Attrs: map[string][]byte{meta.AttrCryptMode: []byte("SSE-KMS"), meta.AttrCryptKeyID: []byte("kid")}}, nil)
		o := &op.GetObjectAttributes{}
		o.Secure = true
		Expect(op.Run(ctx, o, req("k"))).To(MatchError(op.ErrInvalidArgument))
	})
})
```

- [ ] **Step 2: Run to fail; implement `objectread.go`**

The three ops follow the lifecycle above. `GetObjectAttributes` embeds `GetObject` and overrides `Name`, `Init` (`o.GetData = false; o.Range = ""; return o.GetObject.Init(ctx, r)`), `Execute`:

```go
func (o *GetObjectAttributes) Execute(ctx context.Context, r *Request) error {
	o.GetObject.Sink = discardSink{} // Execute's header write goes nowhere; the handler renders
	if err := o.GetObject.Execute(ctx, r); err != nil {
		return err
	}
	if o.PartsCount == nil { // GetObject sets it only on Tentacle without partNumber; radosgw's RGWGetObjAttrs needs it on both
		if o.State.Manifest != nil {
			n, err := o.State.Manifest.PartsCount()
			if err != nil {
				return fmt.Errorf("%w: %v", ErrInternalError, err)
			}
			if n > 0 {
				o.PartsCount = &n
			}
		}
	}
	if o.Requested&AttrObjectParts == 0 || o.PartsCount == nil {
		return nil
	}
	return o.listParts(ctx, r)
}
```

with `listParts` as described (max default 1000, marker default 0, `PartBounds` per part, part-head stats for a compressed object through `r.Env.Objects.StatObject(ctx, r.BucketRec, head.Key)`). `discardSink` implements `Sink` with no-ops.

- [ ] **Step 3: Run the suite and lint**

Run: `go test -tags=ceph_preview -race ./internal/op/ && golangci-lint run ./internal/op/`
Expected: PASS.

- [ ] **Step 4: Commit**

```sh
git add internal/op/objectread.go internal/op/objectread_test.go
git commit -m "feat(op): add GetObjectTagging, GetObjectACL and GetObjectAttributes"
```

Open the draft PR `feat(op): object tagging, ACL and attributes reads (unit R task 6)`.

---

### Task 7: `s3`: handlers and rendering for the four routes

**Files:**
- Modify: `internal/s3/object.go` (`objectHandlers()` gains `get_obj`, `get_obj_attrs`, `get_obj_tags`; `init()` or `newObjectHandlers` assigns `objectGetACLs = getObjectACL`), `docs/exclusions.md` (the four differences these routes make reachable, R-D1, R-D6, R-D7 and R-D8; Step 4)
- Create: `internal/s3/getobject.go`, `internal/s3/getobjattrs.go`, `internal/s3/getobject_test.go`, `internal/s3/getobjattrs_test.go`

**Interfaces:**
- Consumes: G's `HandlerFunc`, `WriteError`, `WriteXML`, `SetCommonHeaders`, the `xmlHeader` constant, `sinkOf(w)` (G Task 4: the `op.Sink` adapter over the route's writer, which cannot be a sink itself; its `WriteHeader(status, h)` merges `h` into `w.Header()` before the status), `SetContentLength(h, n)` (G Task 4: radosgw's `dump_content_length`, the length with `Accept-Ranges: bytes`), `xmltext.Text` and `xmltext.Escape` (G Task 4: ceph's XML text escaping), `op.Request` (`Method`, `Header`, `Query`, `TLS`, `Object.Instance`, `Identity`, `BucketRec`, `Env`); M's `objectGetACLs` variable and `writeXMLDeclaration`; Z's `tags.Set.MarshalXML`, `acl.Policy.MarshalS3XML`; Tasks 5-6's ops.
- Produces:

```go
package s3

// getObject serves get_obj for GET and HEAD (RGWGetObj_ObjStore_S3).
func getObject(ctx context.Context, w http.ResponseWriter, r *op.Request) error
// getObjectTags serves get_obj_tags (RGWGetObjTags_ObjStore_S3::send_response_data, rgw_rest_s3.cc:746-773).
func getObjectTags(ctx context.Context, w http.ResponseWriter, r *op.Request) error
// getObjectACL is the object half of get_acls (RGWGetACLs_ObjStore_S3::send_response :3605-3614); assigned to objectGetACLs.
func getObjectACL(ctx context.Context, w http.ResponseWriter, r *op.Request) error
// getObjectAttrs serves get_obj_attrs ([T] RGWGetObjAttrs_ObjStore_S3).
func getObjectAttrs(ctx context.Context, w http.ResponseWriter, r *op.Request) error

// getObjectSink is the op.Sink getObject hands the op: it renders radosgw's
// response headers from the op's results on the first Write or WriteHeader
// (send_response_data's "sent_header" path), then streams bytes.
type getObjectSink struct{ w op.Sink; r *op.Request; o *op.GetObject; wrote bool }

// parseInt is strict_strtol (src/common/strtol.cc:42-73) for the three integer
// parameters radosgw reads with it; err is radosgw's message.
func parseInt(s string) (int, error)

// responseAttrParams is rgw_rest_s3.cc:132-141: query parameter → header.
var responseAttrParams = [...]struct{ param, header string }{
	{"response-content-type", "Content-Type"}, {"response-content-language", "Content-Language"},
	{"response-expires", "Expires"}, {"response-cache-control", "Cache-Control"},
	{"response-content-disposition", "Content-Disposition"}, {"response-content-encoding", "Content-Encoding"},
}

// attrHeaders is rgw_to_http_attrs (rgw_rest.cc:98-112): attr → header, plus
// rgw_extended_http_attrs entries read from Env.Conf at handler construction.
var attrHeaders = map[string]string{
	meta.AttrContentLang: "Content-Language", meta.AttrExpires: "Expires", meta.AttrCacheControl: "Cache-Control",
	meta.AttrContentDisp: "Content-Disposition", meta.AttrContentEnc: "Content-Encoding",
	meta.AttrUserManifest: "X-Object-Manifest", meta.AttrXRobotsTag: "X-Robots-Tag",
	meta.AttrStorageClass: "X-Amz-Storage-Class", meta.AttrWebsiteRedirect: "x-amz-website-redirect-location",
}
```

`getObject` (`RGWGetObj_ObjStore_S3::get_params` [:291-322](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L291-L322), `RGWGetObj_ObjStore::get_params` [rgw_rest.cc:836-852](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L836-L852)): builds `op.GetObject{GetData: r.Method != "HEAD", Range: Header.Get("Range"), IfMatch: Header.Get("If-Match"), IfNoneMatch: Header.Get("If-None-Match"), IfModifiedSince: Header.Get("If-Modified-Since"), IfUnmodifiedSince: Header.Get("If-Unmodified-Since"), Torrent: Query.Has("torrent"), Versioned: r.Object.Instance != "", SSEHeader: Header has "x-amz-server-side-encryption", SSECAlgorithm/Key/KeyMD5: the three `x-amz-server-side-encryption-customer-*` headers, Secure: secure(r)}`; `partNumber` present → `parseInt` → failure → `op.ErrInvalidPart.WithMessage("Invalid partNumber: " + err)`; the six `response-*` params present in `Query` → `ResponseOverrides[header] = value` (the op refuses them for anonymous callers and control characters); `o.Sink = &getObjectSink{w: sinkOf(w), r: r, o: o}`; `op.Run`; on an error returned before the sink wrote the header → return it (G's `WriteError`); on an error after the header went out → `slog.WarnContext(ctx, "object read failed after the response started", …)` and return nil (the connection ends short, as radosgw's does). `secure(r)` is `rgw_transport_is_secure` ([rgw_common.cc:1071-1093](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L1071-L1093)): `r.TLS`, or when `Env.Conf.Bool("rgw_trust_forwarded_https")`, a `Forwarded` header containing `proto=https` or `X-Forwarded-Proto: https`.

`getObjectSink.header()` renders, in `send_response_data`'s order ([rgw_rest_s3.cc:380-644](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L380-L644); `end_header` [rgw_rest.cc:589-655](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L589-L655)), into `h := http.Header{}` and calls `w.WriteHeader(o.Status, h)`:
1. `Content-Range: bytes <Offset>-<Offset+Length-1>/<ObjSize>` when `o.Range != ""` (`bytes */0` when `ObjSize == 0`; dump_range [:757-778](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L757-L778)) — on 206 and, since radosgw keys it on `range_str`, also on a HEAD with Range.
2. Tentacle: `x-amz-restore` from `AttrRestoreStatus`/`AttrRestoreType`/`AttrRestoreExpiryDate` when present ([\[T\]:589-626](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L589-L626)): `ongoing-request="true"` for `RestoreAlreadyInProgress`; for restore type Temporary (`uint8 1`? — decode `RGWRestoreType` as one byte: `None 0, Temporary 1, Permanent 2`, [T] rgw_sal.h beside `RGWRestoreStatus`) `ongoing-request="false", expiry-date="<RFC 1123>"` and the storage-class header taken from `AttrCloudTierStorageClass`.
3. `SetContentLength(h, o.Length)`: `Content-Length: <Length>` and `Accept-Ranges: bytes`, as `dump_content_length` sends them ([S] [rgw_rest_s3.cc:459](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L459), [T] [:495](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L495); R-D14).
4. `Last-Modified` = `State.Mtime.UTC().Format("Mon, 02 Jan 2006 15:04:05 GMT")` (dump_time_header_impl `%a, %d %b %Y %H:%M:%S %Z` under gmtime).
5. `x-amz-version-id` when `o.VersionID != ""`.
6. `x-rgw-object-type: Normal`, or `Appendable` and `x-rgw-next-append-position: <ObjSize>` when `AttrAppendPartNum` is present (R-D13).
7. `x-amz-replication-status` from `AttrReplicationStatus` when present (verbatim bytes).
8. `x-amz-mp-parts-count` when `o.PartsCount != nil`.
9. `ETag: "<State.ETag>"` when non-empty (dump_etag quotes it).
10. The `response-*` overrides (already validated by the op): `response-content-type` sets the content type; the others set their header.
11. Attribute headers over `State.Attrs` in map-key order (radosgw iterates a `std::map`, byte order — sort the keys): a name in `attrHeaders` and not already set by an override → its value with all trailing NULs removed, `Content-Encoding` with the `aws-chunked` tokens dropped (`content_encoding_without_aws_chunked` [:361](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L361)-378: split on `", "`, drop `aws-chunked`, re-join with `", "`, omit when empty); `user.rgw.content_type` → the content type unless an override set it (value via `rgw_bl_str`: all trailing NULs removed); `user.rgw.x-amz-meta-*` → header `x-amz-meta-<rest>` with ONE trailing NUL removed (`rgw_sanitized_hdrval`); `user.rgw.x-amz-tagging` → `x-amz-tagging-count: <n>` (decode failure → count 0, logged); object-lock retention/legal-hold headers are phase 2 (not emitted).
12. `Content-Type`: the chosen one or `binary/octet-stream`.
13. `x-amz-request-charged: requester` when `r.BucketRec.Info.RequesterPays` and `!r.Identity.Owner.Equal(r.BucketRec.Info.Owner)` (end_header [:597-601](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L597-L601); R-D15) — emitted by G's `SetCommonHeaders`; the route calls it and adds nothing of its own.
14. `SetCommonHeaders` (x-amz-request-id, Server) — through `h` so G's writer sees them once.

On 304 (`op.ErrNotModified` returned by `Run`, before any header): radosgw's `done:` branch emits Last-Modified, ETag, Cache-Control and Expires and no body ([:623-638](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L623-L638); `rgw_err::is_err` is false for 304). `getObject` handles it itself: `SetCommonHeaders`, the four headers from `o.State`, `w.WriteHeader(304)`, return nil — G's `WriteError` would attach an XML body a 304 must not carry. Every other error goes to `WriteError` as usual (radosgw's error document).

`Write(p)` renders the header once, then forwards; `WriteHeader(status, _)` (the op's headers-only path) renders with `status`; `Flush` forwards.

`getObjectTags`: `op.GetObjectTagging{Versioned}`; `op.Run`; `WriteXML`-style body `xmlHeader + o.Tags.MarshalXML()` (an empty set renders `<Tagging xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><TagSet></TagSet></Tagging>`), status 200, `Content-Type: application/xml`, a `Content-Length` and no `Accept-Ranges` (`send_response_data` ends the header with `end_header(s, this, to_mime_type(s->format))`, [S] [:751](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L751), which names no length; R-D14), common headers.

`getObjectACL`: `op.GetObjectACL{Versioned}`; body `xmlHeader + o.Policy.MarshalS3XML()` (radosgw's `dump_start` then the policy XML, [:3605-3614](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L3605-L3614)), 200, `application/xml`.

`getObjectAttrs` ([T] [rgw_rest_s3.cc:3941-4137](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L3941-L4137)): `x-amz-max-parts` → `parseInt` → failure `op.ErrInvalidPart.WithMessage("Invalid value for MaxParts: " + err)`, `min(v, 1000)`; `x-amz-part-number-marker` → failure `…"Invalid value for PartNumberMarker: " + err`; `x-amz-object-attributes` → `op.ParseObjectAttrs`; `x-amz-expected-bucket-owner` read and ignored as radosgw does; SSE headers and `Secure` as for GET. Response: `Last-Modified`, `x-amz-version-id` when non-empty, then `xmlHeader` + `<GetObjectAttributes>` (no xmlns) with, in order and only when requested: `<ETag>` unquoted; `<Checksum></Checksum>` (empty: checksums are phase 2); `<ObjectParts>` only when `o.PartsCount != nil`: one `<Part><PartNumber>n</PartNumber><Size>s</Size></Part>` per part, then `<PartsCount>`, `<TotalPartsCount>`, `<IsTruncated>true|false</IsTruncated>`, `<MaxParts>` when given, `<NextPartNumberMarker>` when truncated, `<PartNumberMarker>` when given; `<ObjectSize>`; `<StorageClass>` = `State.StorageClass` or `STANDARD`. `Content-Type: application/xml`, a `Content-Length` and no `Accept-Ranges` ([T] [:4014](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4014) names no length; R-D14). The formatter writes booleans as `true`/`false` and integers bare, and every string (the ETag, the storage class) through `xml_stream_escaper` (`dump_string`, [Formatter.cc:536-543](https://github.com/ceph/ceph/blob/v20.2.4/src/common/Formatter.cc#L536-L543) at [T]); the handler writes each through `xmltext.Escape`.

`objectHandlers()` returns `{"get_obj": getObject, "get_obj_tags": getObjectTags, "get_obj_attrs": getObjectAttrs}` merged with W's entries; `objectGetACLs = getObjectACL` is assigned in `object.go`'s `init()` (M declared the variable in `acl.go` with `notImplemented` as its default; an `init` assignment in the same package replaces it before `NewHandler` runs).

- [ ] **Step 1: Write the failing handler specs**

`internal/s3/getobject_test.go`, package `s3_test`, on `memstore` through G's `newHandler(store, auth, cfg)` with an authenticator for `alice` (G's Task 4 fixture) and the objects of Task 5's fixture (`small`: 1024 bytes, `text/plain`, `x-amz-meta-color: blue` stored as `user.rgw.x-amz-meta-color = "blue\x00"`, cache-control `no-store\x00`; `empty`), plus a Tentacle handler variant (`memstore.Config{Release: denc.Tentacle}`):

```go
var _ = Describe("get_obj", func() {
	It("GET: 200 with radosgw's headers and the body", func() {
		rec := do("GET", "/plain/small", nil)
		Expect(rec.Code).To(Equal(200))
		h := rec.Header()
		Expect(h.Get("Content-Length")).To(Equal("1024"))
		Expect(h.Get("Accept-Ranges")).To(Equal("bytes"))
		Expect(h.Get("Last-Modified")).To(Equal("Sun, 27 Sep 2026 01:02:03 GMT"))
		Expect(h.Get("ETag")).To(Equal(`"` + etagOf(payload(1024)) + `"`))
		Expect(h.Get("Content-Type")).To(Equal("text/plain"))
		Expect(h.Get("x-amz-meta-color")).To(Equal("blue"), "one trailing NUL stripped")
		Expect(h.Get("Cache-Control")).To(Equal("no-store"))
		Expect(h.Get("x-rgw-object-type")).To(Equal("Normal"))
		Expect(h.Get("x-amz-request-id")).To(HavePrefix("tx"))
		Expect(h.Get("Server")).NotTo(BeEmpty())
		Expect(h).NotTo(HaveKey("Content-Range"))
		Expect(h).NotTo(HaveKey("X-Amz-Version-Id"))
		Expect(h).NotTo(HaveKey("X-Amz-Mp-Parts-Count"))
		Expect(rec.Body.Bytes()).To(Equal(payload(1024)))
	})
	It("HEAD: the same headers, no body; a zero-byte object has Content-Length 0", func() {
		rec := do("HEAD", "/plain/small", nil)
		Expect(rec.Code).To(Equal(200))
		Expect(rec.Header().Get("Content-Length")).To(Equal("1024"))
		Expect(rec.Header().Get("Accept-Ranges")).To(Equal("bytes"), "send_response_data's dump_content_length, rgw_rest_s3.cc:459")
		Expect(rec.Body.Len()).To(BeZero())
		Expect(do("HEAD", "/plain/empty", nil).Header().Get("Content-Length")).To(Equal("0"))
	})
	It("defaults the content type to binary/octet-stream", func() {
		Expect(do("GET", "/plain/empty", nil).Header().Get("Content-Type")).To(Equal("binary/octet-stream"))
	})
	It("Range: 206, Content-Range and the slice; a suffix range; HEAD with Range", func() {
		rec := do("GET", "/plain/small", map[string]string{"Range": "bytes=10-19"})
		Expect(rec.Code).To(Equal(206))
		Expect(rec.Header().Get("Content-Range")).To(Equal("bytes 10-19/1024"))
		Expect(rec.Header().Get("Content-Length")).To(Equal("10"))
		Expect(rec.Body.Bytes()).To(Equal(payload(1024)[10:20]))
		rec = do("GET", "/plain/small", map[string]string{"Range": "bytes=-7"})
		Expect(rec.Header().Get("Content-Range")).To(Equal("bytes 1017-1023/1024"))
		rec = do("HEAD", "/plain/small", map[string]string{"Range": "bytes=0-9"})
		Expect(rec.Code).To(Equal(206))
		Expect(rec.Header().Get("Content-Range")).To(Equal("bytes 0-9/1024"))
		Expect(rec.Body.Len()).To(BeZero())
	})
	It("an invalid range is 416 InvalidRange with the error document", func() {
		rec := do("GET", "/plain/small", map[string]string{"Range": "bytes=2000-3000"})
		Expect(rec.Code).To(Equal(416))
		Expect(rec.Body.String()).To(ContainSubstring("<Code>InvalidRange</Code>"))
		Expect(rec.Header().Get("Content-Type")).To(Equal("application/xml"))
		Expect(rec.Header().Get("Accept-Ranges")).To(Equal("bytes"), "end_header's error branch, rgw_rest.cc:620-624")
		rec = do("GET", "/plain/empty", map[string]string{"Range": "bytes=40-50"})
		Expect(rec.Code).To(Equal(416))
	})
	It("304 carries ETag and Last-Modified and no body", func() {
		etag := do("GET", "/plain/small", nil).Header().Get("ETag")
		rec := do("GET", "/plain/small", map[string]string{"If-None-Match": etag})
		Expect(rec.Code).To(Equal(304))
		Expect(rec.Header().Get("ETag")).To(Equal(etag))
		Expect(rec.Header().Get("Last-Modified")).To(Equal("Sun, 27 Sep 2026 01:02:03 GMT"))
		Expect(rec.Header().Get("Cache-Control")).To(Equal("no-store"))
		Expect(rec.Body.Len()).To(BeZero())
		Expect(rec.Header()).NotTo(HaveKey("Content-Type"))
		Expect(rec.Header()).NotTo(HaveKey("Accept-Ranges"), "is_err is false for 304 and end_header gets no length, rgw_rest_s3.cc:623-638")
	})
	It("412 and 400 for the other conditionals", func() {
		Expect(do("GET", "/plain/small", map[string]string{"If-Match": `"ABCORZ"`}).Code).To(Equal(412))
		rec := do("GET", "/plain/small", map[string]string{"If-Modified-Since": "yesterday"})
		Expect(rec.Code).To(Equal(400))
		Expect(rec.Body.String()).To(ContainSubstring("<Code>InvalidArgument</Code>"))
	})
	It("404 NoSuchKey with the error document; 404 NoSuchBucket", func() {
		rec := do("GET", "/plain/nope", nil)
		Expect(rec.Code).To(Equal(404))
		Expect(rec.Body.String()).To(ContainSubstring("<Code>NoSuchKey</Code>"))
		Expect(do("GET", "/nobucket/x", nil).Body.String()).To(ContainSubstring("<Code>NoSuchBucket</Code>"))
	})
	It("applies the six response-* overrides for a signed request and refuses them for an anonymous one", func() {
		q := "?response-content-type=foo/bar&response-content-disposition=bla&response-content-encoding=aaa&response-content-language=esperanto&response-cache-control=no-cache&response-expires=123"
		rec := do("GET", "/plain/small"+q, nil)
		Expect(rec.Code).To(Equal(200))
		h := rec.Header()
		Expect([]string{h.Get("Content-Type"), h.Get("Content-Disposition"), h.Get("Content-Encoding"), h.Get("Content-Language"), h.Get("Cache-Control"), h.Get("Expires")}).To(Equal([]string{"foo/bar", "bla", "aaa", "esperanto", "no-cache", "123"}))
		anon := doAs(anonymousHandler(), "GET", "/plain/small?response-content-type=x", nil) // a public-read object fixture
		Expect(anon.Code).To(Equal(400))
		Expect(anon.Body.String()).To(ContainSubstring("<Code>InvalidRequest</Code>"))
	})
	It("partNumber: a bad value is 400 InvalidPart with strict_strtol's message", func() {
		rec := do("GET", "/plain/small?partNumber=x", nil)
		Expect(rec.Code).To(Equal(400))
		Expect(rec.Body.String()).To(ContainSubstring("<Code>InvalidPart</Code>"))
		Expect(rec.Body.String()).To(ContainSubstring("<Message>Invalid partNumber: Expected option value to be integer, got &apos;x&apos;</Message>"))
		rec = do("GET", "/plain/small?partNumber=1", nil)
		Expect(rec.Code).To(Equal(200), "part 1 of a single-part object is the object")
		Expect(rec.Header()).NotTo(HaveKey("X-Amz-Mp-Parts-Count"))
		Expect(do("GET", "/plain/small?partNumber=2", nil).Code).To(Equal(400))
	})
	It("x-amz-tagging-count on GET and HEAD, x-amz-version-id for a versionId request", func(ctx SpecContext) {
		set := tags.Set{}
		Expect(set.Add("a", "1", 10)).To(Succeed())
		Expect(set.Add("b", "2", 10)).To(Succeed())
		Expect(store.SetObjectAttrs(ctx, stateOf("small"), map[string][]byte{tags.Attr: encodeTags(set)}, nil)).To(Succeed())
		Expect(do("HEAD", "/plain/small", nil).Header().Get("x-amz-tagging-count")).To(Equal("2"))
		Expect(do("GET", "/plain/small?versionId=null", nil).Header().Get("x-amz-version-id")).To(Equal("null"))
	})
	It("x-amz-request-charged on a requester-pays bucket for a non-owner", func(ctx SpecContext) {
		// bob reads alice's requester-pays bucket through a public-read object ACL
		Expect(do("GET", "/plain/small", nil).Header()).NotTo(HaveKey("X-Amz-Request-Charged"), "the owner is not charged")
		rec := doAs(bobHandler(requesterPays(ctx)), "GET", "/plain/small", nil)
		Expect(rec.Header().Get("x-amz-request-charged")).To(Equal("requester"))
	})
	It("an error after the first byte closes the response short without an error document", func() {
		// a FakeObjectStore whose ReadObject writes 10 bytes then fails
		rec := doWith(failingReader(10), "GET", "/plain/small", nil)
		Expect(rec.Code).To(Equal(200))
		Expect(rec.Body.Len()).To(Equal(10))
	})
	It("Tentacle emits x-amz-mp-parts-count on a plain GET of a multipart object; Squid does not", func() {
		// a FakeObjectStore returning the squid-multipart golden's state
		Expect(doWith(multipartStore(), "HEAD", "/plain/k", nil).Header()).NotTo(HaveKey("X-Amz-Mp-Parts-Count"))
		Expect(doWithRelease(denc.Tentacle, multipartStore(), "HEAD", "/plain/k", nil).Header().Get("x-amz-mp-parts-count")).To(Equal("3"))
	})
})

var _ = Describe("get_obj_tags and object get_acls", func() {
	It("renders an empty TagSet and a populated one", func(ctx SpecContext) {
		rec := do("GET", "/plain/small?tagging", nil)
		Expect(rec.Code).To(Equal(200))
		Expect(rec.Header().Get("Content-Type")).To(Equal("application/xml"))
		Expect(rec.Header()).NotTo(HaveKey("Accept-Ranges"), "send_response_data's end_header names no length, rgw_rest_s3.cc:751")
		Expect(rec.Body.String()).To(Equal(`<?xml version="1.0" encoding="UTF-8"?><Tagging xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><TagSet></TagSet></Tagging>`))
		set := tags.Set{}
		Expect(set.Add("k", "v", 10)).To(Succeed())
		Expect(store.SetObjectAttrs(ctx, stateOf("small"), map[string][]byte{tags.Attr: encodeTags(set)}, nil)).To(Succeed())
		Expect(do("GET", "/plain/small?tagging", nil).Body.String()).To(ContainSubstring("<Tag><Key>k</Key><Value>v</Value></Tag>"))
	})
	It("renders the object ACL through the shared get_acls route", func() {
		rec := do("GET", "/plain/small?acl", nil)
		Expect(rec.Code).To(Equal(200))
		Expect(rec.Body.String()).To(HavePrefix(`<?xml version="1.0" encoding="UTF-8"?><AccessControlPolicy xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Owner><ID>alice</ID>`))
	})
})
```

`internal/s3/getobjattrs_test.go`:

```go
var _ = Describe("get_obj_attrs", func() {
	It("renders the requested attributes of a single-part object, on Squid too", func() {
		rec := do("GET", "/plain/small?attributes", map[string]string{"x-amz-object-attributes": "ETag,Checksum,ObjectParts,StorageClass,ObjectSize"})
		Expect(rec.Code).To(Equal(200))
		Expect(rec.Header().Get("Last-Modified")).To(Equal("Sun, 27 Sep 2026 01:02:03 GMT"))
		Expect(rec.Header().Get("Content-Type")).To(Equal("application/xml"))
		Expect(rec.Header()).NotTo(HaveKey("Accept-Ranges"), "end_header names no length, rgw_rest_s3.cc:4014 at v20.2.4")
		Expect(rec.Body.String()).To(Equal(`<?xml version="1.0" encoding="UTF-8"?><GetObjectAttributes><ETag>` + etagOf(payload(1024)) + `</ETag><Checksum></Checksum><ObjectSize>1024</ObjectSize><StorageClass>STANDARD</StorageClass></GetObjectAttributes>`))
	})
	It("renders ObjectParts for a multipart object with paging", func() {
		rec := doWith(multipartStore(), "GET", "/plain/k?attributes", map[string]string{"x-amz-object-attributes": "ObjectParts", "x-amz-max-parts": "2"})
		Expect(rec.Body.String()).To(Equal(`<?xml version="1.0" encoding="UTF-8"?><GetObjectAttributes><ObjectParts><Part><PartNumber>1</PartNumber><Size>8388608</Size></Part><Part><PartNumber>2</PartNumber><Size>8388608</Size></Part><PartsCount>3</PartsCount><TotalPartsCount>3</TotalPartsCount><IsTruncated>true</IsTruncated><MaxParts>2</MaxParts><NextPartNumberMarker>2</NextPartNumberMarker></ObjectParts></GetObjectAttributes>`))
	})
	It("rejects bad MaxParts and PartNumberMarker as InvalidPart with radosgw's messages", func() {
		rec := do("GET", "/plain/small?attributes", map[string]string{"x-amz-max-parts": "x"})
		Expect(rec.Code).To(Equal(400))
		Expect(rec.Body.String()).To(ContainSubstring("Invalid value for MaxParts: Expected option value to be integer, got &apos;x&apos;"))
		rec = do("GET", "/plain/small?attributes", map[string]string{"x-amz-part-number-marker": "99999999999"})
		Expect(rec.Body.String()).To(ContainSubstring("Invalid value for PartNumberMarker: The option value &apos;99999999999&apos; seems to be invalid"))
	})
	It("HEAD ?attributes is a HEAD of the object (op_head has no attributes op)", func() {
		Expect(do("HEAD", "/plain/small?attributes", nil).Header().Get("Content-Length")).To(Equal("1024"))
	})
})
```

(`do`, `doAs`, `doWith`, `doWithRelease`, `anonymousHandler`, `bobHandler`, `requesterPays`, `failingReader`, `multipartStore`, `stateOf`, `etagOf`, `encodeTags` are spec helpers over G's `newHandler`; `multipartStore` is a `FakeObjectStore` returning the `squid-multipart` golden's state for `StatObject`/`PrefetchObject` and a zero-filled `ReadObject`.) The HEAD `?attributes` case relies on G's dispatch table: `HEAD` with `attributes` is not a subresource row for HEAD, so it dispatches to `get_obj` — assert that Dispatch does so; if G's table routes it to `get_obj_attrs`, `getObjectAttrs` treats `HEAD` as `getObject` with `GetData = false`.

- [ ] **Step 2: Run to fail; implement `getobject.go`, `getobjattrs.go`, `object.go`**

`parseInt`:

```go
// parseInt is strict_strtol: the whole string must be a base-10 integer that
// fits an int; the messages are strtol.cc:47-55 and :66-69.
func parseInt(s string) (int, error) {
	v, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64) // strtoll skips leading whitespace and takes a sign
	switch {
	case err == nil && (v < math.MinInt32 || v > math.MaxInt32):
		return 0, fmt.Errorf("The option value '%s' seems to be invalid", s)
	case errors.Is(err, strconv.ErrRange):
		return 0, fmt.Errorf("The option value '%s' seems to be invalid", s)
	case err != nil:
		return 0, fmt.Errorf("Expected option value to be integer, got '%s'", s)
	}
	return int(v), nil
}
```

(`strtoll` accepts a leading `+`/`-` and whitespace, which `strings.TrimSpace` plus `ParseInt` cover; `0x` prefixes are rejected by both since the base is 10. The messages start with a capital and end without a period because radosgw's do; the linter's `ST1005` is silenced on these two lines with a `//nolint:staticcheck // radosgw's message` comment.)

`getObjectSink.header()` follows the numbered list; the attrs loop sorts `maps.Keys(State.Attrs)`; header values are set with `h.Set` (canonical names, R-D12) except `x-amz-meta-*`, which are set with `h[textproto.CanonicalMIMEHeaderKey(name)]` too — canonical form everywhere. `Content-Length` is set through `SetContentLength(h, o.Length)`, so net/http neither chunks nor guesses and `Accept-Ranges` goes with it as radosgw's `dump_content_length` sends it (R-D14).

- [ ] **Step 3: Run the suite and lint**

Run: `go test -tags=ceph_preview -race ./internal/s3/ && golangci-lint run ./internal/s3/`
Expected: PASS.

- [ ] **Step 4: Record the read path's differences in `docs/exclusions.md`**

These routes are where the four differences reach a client, so they are recorded in this PR (the task that introduces a difference records it), in `docs/exclusions.md`'s own style:

1. Under "Swift API and Swift authentication", after the delete-at sentences (R-D7):

   > Objects written through Swift as dynamic or static large objects carry `user.rgw.user_manifest` or `user.rgw.slo_manifest`, and radosgw composes them from their segments even on an S3 GET or HEAD. rgw-go does not: it answers 501 NotImplemented on GET and HEAD rather than serve the manifest object itself as the data. Such objects exist only where a client wrote them through Swift.

2. Under "Cloud transition and restore", after its "Still required" sentence (R-D6):

   > rgw-go does so as radosgw does: a cloud-tiered object answers 403 InvalidObjectState on GET, with the headers alone on HEAD, and on Tentacle a restored copy is served and a restore in progress answers 408 RequestTimeout. One case differs: where a Tentacle tier allows read-through, radosgw starts a restore from the cloud and answers 408 RequestTimeout ("restore is still in progress"), while rgw-go, which runs no restore, answers the 403.

3. At the end of "Coexistence obligations independent of any exclusion", two bullets (R-D1, R-D8):

   > - **GetObjectAttributes is served on Squid too.** A Squid radosgw has no GetObjectAttributes operation and answers `GET ?attributes` as a GetObject, with the object's body; rgw-go serves the operation on both releases, as the Tentacle feature level for gateway-side features asks, so on a Squid zone the same request returns the attributes document from rgw-go and the object from radosgw. The parity cost is one s3-tests case, `test_get_object_attributes`, listed in `test/s3tests/known-differences-squid.txt`.
   > - **BitTorrent files are not served.** `?torrent` answers 404 NoSuchKey for an object that has no torrent, as radosgw does, and 501 NotImplemented for one that carries `user.rgw.torrent`, where radosgw returns the bencoded file. Ceph's S3 compliance page lists GET Object torrent as unsupported (doc/dev/radosgw/s3_compliance.rst, table "Operations on Objects", published at https://docs.ceph.com/en/tentacle/dev/radosgw/s3_compliance/ with no version stated), but the code serves a stored torrent: a PUT generates one only while `rgw_torrent_flag` is on, and the option defaults to false (`RGWPutObj::get_torrent_filter`, [rgw_op.cc:4110-4125](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L4110-L4125) at v19.2.6 and [:4319-4334](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4319-L4334) at v20.2.4; [rgw.yaml.in:3205-3210](https://github.com/ceph/ceph/blob/v19.2.6/src/common/options/rgw.yaml.in#L3205-L3210) and :3343-3348), and `GET ?torrent`, authorized as `s3:GetObjectTorrent` or `s3:GetObjectVersionTorrent` ([rgw_op.cc:989-994](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L989-L994) and [:1195-1196](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L1195-L1196)), returns the stored file ([:2276-2292](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L2276-L2292) and :2512-2528). On a zone with the default configuration the two gateways therefore agree, 404 NoSuchKey; they differ only for objects written while an operator had the flag on, and Rook never sets it. radosgw also reads a torrent that an older release kept in the head object's omap under the key `rgw.torrent`; rgw-go looks only at the attribute, so such an object answers 404.

- [ ] **Step 5: Commit**

```sh
git add internal/s3/object.go internal/s3/getobject.go internal/s3/getobjattrs.go internal/s3/getobject_test.go internal/s3/getobjattrs_test.go docs/exclusions.md
git commit -m "feat(s3): serve GET, HEAD, GetObjectAttributes, object tagging and ACL reads with radosgw's headers"
```

Open the draft PR `feat(s3): object read routes (unit R task 7)`; the description names R-D1, R-D6, R-D7, R-D8, R-D12, R-D14 and R-D15, and states that the `docs/exclusions.md` change must be announced to the rgw-rs session.

---

### Task 8: Gate: corpus read-back through rgw-go against radosgw, the one-round-trip metric, s3-tests GET groups; registry entries `[cluster]`

**Files:**
- Create: `test/gate/read_test.go`, `hack/rooket/read-gate.sh`
- Modify: `Makefile` (`read-gate` target), `hack/rooket/README.md` (one paragraph), `docs/ceph-upstream-bugs.md` (one entry), `go.mod` (`github.com/aws/aws-sdk-go-v2/service/s3` and `config`/`credentials` as test dependencies, if A's Task 9 has not added them)

**Interfaces:**
- Consumes: T Task 3's manifest fields (`Object.StorageClass`, `Object.Compression`, the four `comp-<codec>.bin` objects), T Task 11's `make rgw-go-up RELEASE=<r>` (`hack/rooket/out/<r>/rgw-go.endpoint`, the metrics listener `127.0.0.1:9481`), T Task 12's `make s3tests-parity RELEASE=<r>` (its diff lines `<test>: baseline <o1>, candidate <o2>`), `hack/rooket/lib.sh` (`use_release`, `rgw_daemon`, `rgw_endpoint`), `test/gate` (`gate.LoadManifest`, `cephtest.Conf`, `RGW_GO_TEST_MANIFEST`), G's metrics (`rgw_go_rados_ops_total{kind="read",mode=…}`).
- Produces: `make read-gate RELEASE=squid|tentacle` — starts rgw-go against the populated cluster, exports `RGW_GO_TEST_RGW_ENDPOINT` (radosgw) and `RGW_GO_TEST_RGW_GO_ENDPOINT`, `RGW_GO_TEST_RGW_GO_METRICS`, runs `go test ./test/gate/... -ginkgo.label-filter=read`, then stops rgw-go.

The gate treats radosgw as the oracle (spec §3): every object populate.sh wrote is fetched through both gateways with the same client and the same request, and the two responses must agree byte for byte in body and in the header set below. It runs on Squid and on Tentacle; where the releases legitimately differ (R-D1, R-D10) the spec says which side is asserted.

`hack/rooket/read-gate.sh RELEASE`: `use_release`; `make rgw-go-up RELEASE=$release` (T's script; idempotent); `export RGW_GO_TEST_RGW_ENDPOINT=$(rgw_endpoint "$(rgw_daemon)") RGW_GO_TEST_RGW_GO_ENDPOINT=$(cat "$out/rgw-go.endpoint") RGW_GO_TEST_RGW_GO_METRICS=http://127.0.0.1:${RGW_GO_METRICS_PORT:-9481}/metrics RGW_GO_TEST_CEPH_CONF=$out/ceph.conf RGW_GO_TEST_MANIFEST=$out/manifest.json`; `go test "-tags=$GO_TAGS,integration" -race -count=1 -v ./test/gate/... -args -ginkgo.v -ginkgo.label-filter=read`; `trap 'make rgw-go-down RELEASE=$release' EXIT`. Makefile: `read-gate: need-release ## Read the populated corpus through rgw-go and radosgw and compare` → `ROOKET=$(ROOKET_BIN) hack/rooket/read-gate.sh $(RELEASE)`.

- [ ] **Step 1: Write the gate spec**

`test/gate/read_test.go`, `//go:build integration`, package `gate_test`, `Label("integration", "read")`. Two S3 clients from `aws-sdk-go-v2` (path style, region `us-east-1`, the manifest's `users[0]` keys, `RequestChecksumCalculation: WhenRequired`, `ResponseChecksumValidation: WhenRequired`, no retries), one per endpoint; a raw `net/http` client for the requests boto shapes but the SDK will not send (a HEAD with Range, a bad Range, `?attributes` without headers).

```go
var _ = Describe("object reads through rgw-go against radosgw", Label("integration", "read"), Ordered, func() {
	var (
		m        gate.Manifest
		rgw, rgo *s3.Client        // aws-sdk-go-v2/service/s3
		raw      *rawClient        // signs and sends one request to either endpoint; returns status, headers, body
		release  denc.Release
	)
	BeforeAll(func() {
		conf := cephtest.Conf()
		cephtest.ReadManifest(conf, &m)
		rgw = newClient(os.Getenv("RGW_GO_TEST_RGW_ENDPOINT"), m.Users[0])
		rgo = newClient(os.Getenv("RGW_GO_TEST_RGW_GO_ENDPOINT"), m.Users[0])
		raw = newRawClient(m.Users[0])
		release, _ = denc.ParseRelease(m.Release)
	})

	// compare fetches the same request from both gateways and asserts status, body and the compared headers agree.
	compared := []string{"Content-Length", "Content-Type", "ETag", "Last-Modified", "Accept-Ranges", "Content-Range", "X-Amz-Storage-Class", "X-Amz-Tagging-Count", "X-Rgw-Object-Type", "X-Amz-Mp-Parts-Count", "X-Amz-Version-Id", "Cache-Control", "Content-Disposition", "Content-Encoding", "Content-Language", "Expires"}
	compare := func(method, path string, hdr map[string]string) (oracle, got response) {
		GinkgoHelper()
		oracle = raw.do(os.Getenv("RGW_GO_TEST_RGW_ENDPOINT"), method, path, hdr)
		got = raw.do(os.Getenv("RGW_GO_TEST_RGW_GO_ENDPOINT"), method, path, hdr)
		Expect(got.status).To(Equal(oracle.status), "%s %s: body %s", method, path, got.body)
		for _, h := range compared {
			Expect(got.header.Get(h)).To(Equal(oracle.header.Get(h)), "%s %s: header %s", method, path, h)
		}
		for name := range oracle.header {
			if strings.HasPrefix(name, "X-Amz-Meta-") {
				Expect(got.header.Get(name)).To(Equal(oracle.header.Get(name)), name)
			}
		}
		Expect(got.body).To(Equal(oracle.body), "%s %s: body differs", method, path)
		return
	}

	It("reads every populated object whole and by HEAD, byte-exact and header-exact", func() {
		for _, o := range m.Objects {
			path := "/" + o.Bucket + "/" + o.Key
			oracle, _ := compare("GET", path, nil)
			Expect(oracle.body).To(HaveLen(int(o.Size)), path)
			if o.Compression != "" {
				Expect(oracle.header.Get("X-Amz-Storage-Class")).To(Equal(o.StorageClass), "the compressed objects carry their class")
			}
			compare("HEAD", path, nil)
		}
	})

	DescribeTable("reads ranges across every layout boundary",
		func(key string, rng string) {
			path := "/plain/" + key
			oracle, _ := compare("GET", path, map[string]string{"Range": rng})
			Expect(oracle.status).To(BeElementOf(206, 416))
			compare("HEAD", path, map[string]string{"Range": rng})
		},
		Entry("head-full.bin: the last byte of a head that fills its chunk", "head-full.bin", "bytes=4194303-4194303"),
		Entry("large.bin: across the head/tail boundary", "large.bin", "bytes=4194300-4194310"),
		Entry("large.bin: inside the second tail", "large.bin", "bytes=9000000-9000100"),
		Entry("large.bin: a suffix", "large.bin", "bytes=-1000"),
		Entry("large.bin: an open range from the tail", "large.bin", "bytes=8388608-"),
		Entry("large.bin: past the end is 416", "large.bin", "bytes=20000000-"),
		Entry("large.bin: inverted is 416", "large.bin", "bytes=10-5"),
		Entry("multipart.bin: across a part boundary", "multipart.bin", "bytes=8388600-8388620"),
		Entry("multipart.bin: the last part", "multipart.bin", "bytes=16777216-"),
		Entry("empty.bin: any range is 416", "empty.bin", "bytes=0-1"),
		Entry("_underscore.bin: a locator object by range", "_underscore.bin", "bytes=4-9"),
		Entry("comp-zlib.bin: inside the first compression block", "comp-zlib.bin", "bytes=1000-2000"),
		Entry("comp-snappy.bin: across the 4 MiB block boundary? the object is 1 MiB: one block, a middle range", "comp-snappy.bin", "bytes=500000-600000"),
		Entry("comp-zstd.bin: a suffix", "comp-zstd.bin", "bytes=-100"),
		Entry("comp-lz4.bin: the last byte", "comp-lz4.bin", "bytes=1048575-1048575"),
	)

	It("answers the conditionals as radosgw does", func() {
		head := raw.do(os.Getenv("RGW_GO_TEST_RGW_ENDPOINT"), "HEAD", "/plain/small.bin", nil)
		etag, lm := head.header.Get("ETag"), head.header.Get("Last-Modified")
		for _, h := range []map[string]string{
			{"If-Match": etag}, {"If-Match": `"ABCORZ"`}, {"If-Match": "*"},
			{"If-None-Match": etag}, {"If-None-Match": `"ABCORZ"`}, {"If-None-Match": "*"},
			{"If-Modified-Since": lm}, {"If-Modified-Since": "Sat, 29 Oct 1994 19:43:31 GMT"}, {"If-Modified-Since": "yesterday"},
			{"If-Unmodified-Since": lm}, {"If-Unmodified-Since": "Sat, 29 Oct 1994 19:43:31 GMT"}, {"If-Unmodified-Since": "Sat, 29 Oct 2100 19:43:31 GMT"},
			{"If-None-Match": `"ABCORZ"`, "If-Modified-Since": "Sat, 29 Oct 2100 19:43:31 GMT"},
			{"If-Match": etag, "If-Unmodified-Since": "Sat, 29 Oct 1994 19:43:31 GMT"},
		} {
			compare("GET", "/plain/small.bin", h)
		}
	})

	It("serves multipart parts by partNumber and refuses the rest as radosgw does", func() {
		for _, n := range []string{"1", "2", "3", "4", "x", "0"} {
			compare("GET", "/plain/multipart.bin?partNumber="+n, nil)
			compare("HEAD", "/plain/multipart.bin?partNumber="+n, nil)
		}
		compare("GET", "/plain/small.bin?partNumber=1", nil)
		compare("GET", "/plain/small.bin?partNumber=2", nil)
	})

	It("answers the response-* overrides and the metadata object alike", func() {
		compare("GET", "/plain/meta.bin?response-content-type=foo/bar&response-cache-control=no-cache&response-content-disposition=bla&response-content-encoding=aaa&response-content-language=esperanto&response-expires=123", nil)
		compare("GET", "/plain/meta.bin", nil)
	})

	It("reads tags and the object ACL alike", func() {
		compare("GET", "/plain/small.bin?tagging", nil)
		compare("GET", "/plain/small.bin?acl", nil)
	})

	It("GetObjectAttributes: Tentacle agrees with radosgw; Squid serves what a Tentacle radosgw would", func() {
		path := "/plain/multipart.bin?attributes"
		hdr := map[string]string{"x-amz-object-attributes": "ETag,Checksum,ObjectParts,StorageClass,ObjectSize", "x-amz-max-parts": "2"}
		got := raw.do(os.Getenv("RGW_GO_TEST_RGW_GO_ENDPOINT"), "GET", path, hdr)
		Expect(got.status).To(Equal(200))
		Expect(string(got.body)).To(ContainSubstring("<PartsCount>3</PartsCount>"))
		Expect(string(got.body)).To(ContainSubstring("<ObjectSize>20971520</ObjectSize>"))
		if release == denc.Tentacle {
			oracle := raw.do(os.Getenv("RGW_GO_TEST_RGW_ENDPOINT"), "GET", path, hdr)
			Expect(got.status).To(Equal(oracle.status))
			Expect(got.body).To(Equal(oracle.body))
		}
	})

	It("a signed GET of an object within its head costs exactly one RADOS read op (the one-round-trip baseline)", func() {
		before := radosReadOps(os.Getenv("RGW_GO_TEST_RGW_GO_METRICS"))
		got := raw.do(os.Getenv("RGW_GO_TEST_RGW_GO_ENDPOINT"), "GET", "/plain/head-full.bin", nil)
		Expect(got.status).To(Equal(200))
		Expect(got.body).To(HaveLen(4 << 20))
		Expect(radosReadOps(os.Getenv("RGW_GO_TEST_RGW_GO_METRICS")) - before).To(BeEquivalentTo(1), "getxattrs+stat+read(4 MiB) in one op; the bucket and user come from the cache")
		before = radosReadOps(os.Getenv("RGW_GO_TEST_RGW_GO_METRICS"))
		raw.do(os.Getenv("RGW_GO_TEST_RGW_GO_ENDPOINT"), "HEAD", "/plain/large.bin", nil)
		Expect(radosReadOps(os.Getenv("RGW_GO_TEST_RGW_GO_METRICS")) - before).To(BeEquivalentTo(1), "HEAD is one op")
		before = radosReadOps(os.Getenv("RGW_GO_TEST_RGW_GO_METRICS"))
		raw.do(os.Getenv("RGW_GO_TEST_RGW_GO_ENDPOINT"), "GET", "/plain/large.bin", nil)
		Expect(radosReadOps(os.Getenv("RGW_GO_TEST_RGW_GO_METRICS")) - before).To(BeEquivalentTo(3), "head op plus two 4 MiB tail reads (10 MiB object)")
		before = radosReadOps(os.Getenv("RGW_GO_TEST_RGW_GO_METRICS"))
		raw.do(os.Getenv("RGW_GO_TEST_RGW_GO_ENDPOINT"), "GET", "/plain/large.bin", map[string]string{"Range": "bytes=0-9"})
		Expect(radosReadOps(os.Getenv("RGW_GO_TEST_RGW_GO_METRICS")) - before).To(BeEquivalentTo(2), "a ranged GET stats without a prefetch, then reads")
	})
})
```

`radosReadOps(url)` scrapes `/metrics` and sums `rgw_go_rados_ops_total{kind="read",…}` over modes (`expfmt.TextParser`). The metric counts every RADOS op of the process, so the spec runs these four requests with nothing else in flight and repeats each measurement once first to warm the bucket and user caches (M's cache: the first request of a session reads the bucket entry point, instance and user). `rawClient.do` signs with SigV4 (`aws-sdk-go-v2/aws/signer/v4`, `UNSIGNED-PAYLOAD`) and returns `response{status int; header http.Header; body []byte}`.

- [ ] **Step 2: Run on Squid, then Tentacle** (cluster-backed; T Task 3's populate adds the compressed objects)

```sh
make cluster-up RELEASE=squid && make populate RELEASE=squid
make read-gate RELEASE=squid
make cluster-up RELEASE=tentacle && make populate RELEASE=tentacle
make read-gate RELEASE=tentacle
```
Expected: every spec green on both. A header or body difference is a finding against R (fix it here), unless the oracle itself is at fault — then it is a registry entry. The partNumber ETag (R-D17): rgw-go's must equal radosgw's, whichever it is, and `compare` asserts exactly that. If `compare("GET", "/plain/multipart.bin?partNumber=2")` shows radosgw returning the multipart ETag, change `GetObject.Init` to keep `State.ETag = CondState.ETag` for a part (the one place the choice lives); record the observed header in the PR description either way.

- [ ] **Step 3: s3-tests GET groups against rgw-go** (cluster-backed, after T Task 12)

```sh
make s3tests-parity RELEASE=squid 2>&1 | tee $TMPDIR/parity-squid.txt
```
Expected: exit 0. On Squid the one known difference among the GET-shaped tests, `test_get_object_attributes` (R-D1), is on T's `test/s3tests/known-differences-squid.txt`; any other difference among them (`test_ranged_*`, `test_get_object_if*`, `test_object_raw_*`, `test_object_head_zero_bytes`, `test_object_read_not_exist`, `test_multipart_get_part`, `test_non_multipart_get_part`, `test_get_obj_head_tagging`, `test_get_obj_tagging`, `test_get_object_torrent`, `test_atomic_read_*`, `test_object_set_get_metadata_*`, `test_object_acl*`) is a finding against R. `test_object_raw_get_x_amz_expires_not_expired` and its `_tenant` twin are not among them: their first request is an OPTIONS preflight, so T's phase 2 deselect list removes them on both gateways, as it does `test_get_versioned_object_attributes` and `test_get_checksum_object_attributes`. `test_multipart_get_part` is parity whichever way radosgw answers it (R-D17): rgw-go returns radosgw's partNumber ETag, so the test passes or fails exactly as it does against radosgw, and it never goes on a known-differences list. `make s3tests-parity RELEASE=tentacle` must exit 0 (GetObjectAttributes exists there). Record both report paths in the PR description.

- [ ] **Step 4: Registry entry**

(The read path's `docs/exclusions.md` entries are Task 7's.)

`docs/ceph-upstream-bugs.md`, one new entry:

```
## GetObject compares If-Match and If-None-Match as prefixes and treats `*` literally

- **Kind:** quirk (mirrored).
- **Evidence:** `RGWRados::Object::Read::prepare` unquotes the header and runs
  `if_match_str.compare(0, etag.length(), etag.c_str(), etag.length())`
  (v19.2.6 `rgw_rados.cc:6975-6989`, v20.2.4 `:7814-7828`): the value must start
  with the stored ETag, so `If-Match: "<etag>garbage"` passes and
  `If-Match: *` fails with 412 PreconditionFailed on an existing object,
  where RFC 7232 and AWS treat `*` as matching any current representation;
  `If-None-Match: *` never matches and serves 200.
- **Releases:** every release checked, v19.2.6 through v20.2.4.
- **rgw-go:** mirrors it (`op.GetObject.checkConditions`) so a client sees
  the same answer from either gateway; recorded here because a correct
  client meets it.
- **Upstream:** not filed yet.
- **Found:** unit R planning, 2026-09-28, verified at both tags.
```

(Verify the [T] line numbers with `grep -n 'if_match_str.compare' $TMPDIR/ceph/rgw_rados.cc.20` before committing; only the [S] lines were verified when this plan was written.)

- [ ] **Step 5: Commit**

```sh
git add test/gate/read_test.go hack/rooket/read-gate.sh hack/rooket/README.md Makefile docs/ceph-upstream-bugs.md go.mod go.sum
git commit -m "test(gate): read the populated corpus through rgw-go against radosgw, byte for byte"
```

Open the draft PR `test(gate): object read gate (unit R task 8)`; the description records the s3-tests parity outcome per release, the one-round-trip result, and the partNumber ETag observation.

---

## Self-review

- **Spec coverage.** §6 "GET within the head: one read op composing stat, xattrs and the first 4 MiB": Task 3's `PrefetchObject` (one op, `getxattrs`+`stat2`+`read(0, rgw_max_chunk_size)`) and Task 8's metric assertion. §6 "GET beyond the head, or a range: the first op, then tail reads, up to 16 MiB in flight in 4 MiB requests, streamed in order": Task 4 (`pieces`, the byte-weighted semaphore, ordered delivery, the window spec). §9 "Get with ranges and conditionals, Head, GetObjectAttributes": Tasks 5-7. §9 "read compatibility for every placement, storage class, compression codec and manifest layout": Task 3 (`dataPool` with radosgw's fallbacks, both manifest forms through `set_head`), Task 1 (both iterator forms), Task 2 (four codecs), Task 8 (the corpus incl. T's four compressed classes). §8 encoding rule: everything decoded is a persistent type decoded at every version (`meta`), nothing is encoded on this path. §2 performance: R-D2/R-D3 round trips, reused buffers, no whole-object buffering, decompression per block. Unit R's goal in the index (`00-index.md`, "The nine units"): "`_`-prefixed names and their locators" (Task 3's locator spec, Task 8's `_underscore.bin` range), "the object ACL and tagging GETs" (Task 6-7), "cloud-tier and key-server-encrypted objects refused as radosgw refuses them" (R-D6, R-D9 in Task 5). The brief's gate items: the corpus read back byte-exact (Task 8 Step 1), s3-tests GET groups (Step 3), the one-round-trip baseline (Step 1's metric spec).
- **Placeholder scan.** No TBD/TODO; the two "adapt to G's accessor name" notes (`s.objectData` in Task 3's memstore snippet, `stat.Result()` in `readHead`) are replaced below: `readHead` reads `stat.Size` and `stat.ModTime` (`radosclient.StatResult`'s fields, [results.go:41-45](https://github.com/jhoblitt/rgw-go/blob/50fc45c4d59953661d5c57c27ca7016bc55edeeb/internal/radosclient/results.go#L41-L45)) — fixed in the text. Spec helpers (`openWithZone`, `openFake`, `NewStoreForTest`, `fillHead`, `recordReads`, `do`, `doWith`, …) are named, given signatures or one-line contracts, and belong to the task that first uses them.
- **Type consistency.** `op.ObjectState.Head []byte` (Task 3) is what Task 4 serves and Task 5's specs assert; `Store.readStored(ctx, st, ofs, n, w)` (Task 4, driver-internal) is the raw stored-byte read W's `copyData` calls, with the same name and signature in W's plan, so G's store interfaces stay unchanged; `ObjectStore.PrefetchObject(ctx, rec, key)` is the name in Tasks 3, 4, 5, 6; `compression.Range/Window/Stream/NewDecoder` (Task 2) are what Task 4 calls; `meta.Manifest.Seek/StripeIter/PartsCount/PartBounds` (Task 1) are what Tasks 4-6 call; `op.GetObject` fields (`GetData`, `Range`, `IfMatch`…, `PartNumber *int`, `Torrent`, `ResponseOverrides`, `SSE*`, `Secure`, `Sink`, `State`, `CondState`, `Offset`, `Length`, `Partial`, `ObjSize`, `PartsCount *int`, `VersionID`, `Status`, `Versioned`) are the names Task 7's handler fills and renders; `op.ParseObjectAttrs`/`ObjectAttrs`/`ObjectPart` (Task 6) are what `getObjectAttrs` uses; `parseInt` (Task 7) produces radosgw's two message forms.
- **Review Focus.** Line 1 (compressed multipart range) → Task 2's `Stream` two-block spec and Task 4's compressed range table; line 2 (failed tail, disconnect) → Task 4's ENOENT-on-stripe-3 and cancelled-context specs; line 3 (conditional combinations) → Task 5's eighteen-row table and Task 8's cluster comparison; line 4 (partNumber cases) → Task 5's partNumber specs and Task 8; line 5 (a full head, an empty head with a manifest) → Task 4's prefetch spec and Task 8's `head-full.bin` one-op assertion and `multipart.bin` reads.
- **Cluster tasks.** Only Task 8 needs a cluster (the rooket Squid and Tentacle clusters, never the ambient one); Tasks 1-7 run on the goldens, fakes, `fakerados` and `memstore`. Untestable without a cluster and stated as such in Task 8's PR description: radosgw's actual partNumber ETag (R-D17), the Tentacle restore headers on a really restored object (the corpus has none), and whether `rgw_ignore_get_invalid_range` is honoured by the deployed radosgw (populate does not set it).
- **Contract gaps reported, not fixed here:** `x-amz-request-charged` in G's common headers (R-D15; `Accept-Ranges` is G's `SetContentLength` and `WriteError`, R-D14); `StatObject`'s missing prefetch (closed additively by `PrefetchObject`); M's `fakerados.ReadStep` ignoring `Buf` (fixed additively in Task 3); W's Task 3 attr names may overlap Task 1's (drop the duplicate where the second lands).
