# Unit N — Admin API Implementation Plan

> **For agentic workers:** REQUIRED: load superpowers:executing-plans (or superpowers:subagent-driven-development) and go-conventions:go-conventions before starting. Steps use checkbox (`- [ ]`) syntax for tracking. Build against G's "Frozen interface contract" verbatim; the additions from M, A, R and Z are listed under Global Constraints and are built on, never redefined.

**Goal:** Serve radosgw's `/admin/*` REST API from `internal/admin` on G's op layer — users (keys, subusers, caps, quota), buckets (every op but `Sync_Bucket`), usage (show, and a trim whose loop is bounded), metadata over `user`, `bucket` and `bucket.instance`, `info`, `account`, `config`, `ratelimit`, and the read-only `realm` and `period` getters — rendered in JSON, XML and HTML exactly as radosgw's admin handlers render them, so radosgw-admin's JSON shapes and go-ceph's `rgw/admin` client work unchanged, ordered so the Rook operator's calls work first.

**Architecture:** `internal/admin` is a second protocol layer beside `internal/s3`: `cli serve` mounts it at `/<rgw_admin_entry>/` (default `admin`) when `rgw_enable_apis` lists `admin`, with the longest-prefix rule `RGWRESTMgr::get_resource_mgr` applies. It reuses A's `s3.Authenticator` for SigV4/SigV2, parses radosgw's `RESTArgs`, dispatches on resource × method × the first admin sub-resource in the query, and drives one `op.Op` per request through `op.Run`, whose `VerifyPermission` is `RGWRESTOp::verify_permission`: a capability check on `Identity.Caps` (the `Identity.Admin` override is G's). The ops live in `internal/op` (shared with S3, as unit N's scope in the index asks) and use the store interfaces; N fills `op.MetadataStore` in the driver and adds four small additive interfaces for what the admin surface needs and no S3 op does (`UsageReader`, `BucketAdminStore`, `RealmStore`, the widened `AccountStore`). Two seams N owns: `Cluster.FSID()` for `/admin/info`, and `Pool.ListObjectsFrom` — an exact resume by the object's placement hash — for the opaque listing markers of decision D5. Every response body is written through `internal/formatter`, a transcription of ceph's `JSONFormatter`, `XMLFormatter` and `HTMLFormatter`, by dump functions that make radosgw's C++ `dump()` calls in radosgw's order, so one function serves every format (N-D2, N-D11); `meta` and `acl` types the admin API renders gain `Dump` methods beside their phase-0 `MarshalJSON`. A bucket purge with `bypass-gc` deletes object data directly through a driver path (N-D9) that consumes W's, R's and P's driver internals and therefore lands in Task 14, after those units.

**Tech Stack:** Go 1.27, Ginkgo v2/Gomega, counterfeiter fakes and `memstore` from G, M's `fakerados`, the phase-0 corpus goldens (ceph-dencoder's `JSONFormatter(true)` dumps) as the byte-exact oracle for the `Dump` methods, go-ceph (jhoblitt fork, `ceph_preview`, pin cbf97f85fcf5 — no fork change needed), ceph v19.2.6 (Squid) and v20.2.4 (Tentacle) as the behavioural references, `hack/admin/run.sh` (T Task 6) for the go-ceph `rgw/admin` suite.

**Spec:** docs/superpowers/specs/2026-09-25-rgw-go-design.md §2, §6, §8, §9 ("Admin:" sentence, phase 1); the index's background in docs/superpowers/plans/2026-09-29-phase-1/00-index.md (unit N in "The nine units", the seam gaps in "Shared interfaces and seam gaps", decision D5 in "Spec ambiguities and decisions D1-D11"). A file:line citation without a tag is v19.2.6.

---

## Global Constraints

- Go 1.27, cobra/viper, `log/slog` JSON to stderr, Ginkgo v2 + Gomega, counterfeiter fakes beside the interface (`internal/op/opfakes`), `make check` green before a task is called done (go-conventions). cgo build settings and the `ceph_preview` tag as in the repository CLAUDE.md.
- Ceph floor (rgw-go PR #48): daemons are 19.2.6+ (Squid) or 20.2.4+ (Tentacle). Squid-vs-Tentacle differences are gated on `Env.Zone.Release()` (`denc.Release`); nothing is done for point releases below the floor. Data written by older releases is still decoded.
- G's frozen interface contract is built against verbatim. The ADDITIVE changes this plan makes (each named in the task that makes it): `op.UsageReader`, `op.BucketAdminStore`, `op.RealmStore`, the widened `op.AccountStore`, `op.Env.{UsageReader,BucketAdmin,Realms}`, `policy.ActionNone`, `meta.{ParseCaps,Caps.AddString,Caps.RemoveString,Caps.Check,ValidCapType,ParseOpTypeList,OpTypeString,ParseSubuserPerm,PermString}`, `meta.{UserInfo,BucketInfo,BucketEntryPoint,Caps}.UnmarshalJSON`, `meta.{AttrsJSON,UserCompleteInfo,BucketCompleteInfo}` (Task 8), `radosclient.Cluster.FSID`, `radosclient.Pool.ListObjectsFrom`, `cls/user` account-resource ops, `memstore.Store.AddUsage` (`AccountName` on `op.AccountStore`, memstore and the driver stub is A Task 1's; Task 7 fills the driver's); the new leaf package `internal/formatter` (Task 3); `meta.Dumper` and the `Dump` methods of the `meta` types the admin API renders (Tasks 3, 4, 6, 7, 8, 12); `acl.{Policy,Owner,List,Grant}.Dump` in a new `internal/acl/dump.go` (Task 6); the field `op.MetadataEntry.Doc` (Task 8). Fakes are regenerated; `make generate-check` fails a stale fake. G's `op.PayloadForms` and `s3.Authenticator.Authenticate(ctx, req, payloads op.PayloadForms)` are consumed unchanged: the admin handler passes each route's forms, `op.PayloadSigned` for the metadata PUT and the zero value for every other admin route (Task 3).
- Contract additions from M (build on, do not redefine): `op.LogUsage`, `op.UsageEntry.Payer`, `op.ListObjectsParams.AllowUnordered`, `meta.ObjVersion` (M-D9), `s3.objectGetACLs`/`objectPutACLs` (M-D8), M owns `AdjustStats` and control-watch re-registration; driver internals N reuses are named in each task's Consumes line (`sysobjs`, `objv`, `userObj`, `epObj`, `instanceObj`, `readEntryPoint`/`writeEntryPoint`/`removeEntryPoint`, `readInstance`/`writeInstance`/`removeInstance`, `linkBucket`/`unlinkBucket`, `readShardHeaders`, `readIndexStats`, `syncOwnerStats`, `indexPool`, `shardOIDs`, `usageOID`, `options`, `shardMod`, `readAccount`, `accountObj`, `ownerBucketsObj`, `emailIndexObj`, `filterRGWAttrs`, `forEachOwner`).
- Contract additions from A: `op.AuthResult.ContentLength`; `op.AccountStore{GetAccount}`, `op.AccountRecord`, `Env.Accounts` (N fills the driver side and widens the interface); payload verification (D-A2): every op that consumes a body reads it to EOF and checks the error before it writes.
- Contract additions from R: `ObjectStore.PrefetchObject`, `ObjectState.Head`; `internal/meta/attrs_read.go` attr names are reused; `Accept-Ranges: bytes` goes out exactly where radosgw's `dump_content_length` runs ([rgw_rest.cc:388-397](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L388-L397)): on every error document, admin ones included (`end_header`'s error branch, [rgw_rest.cc:620-624](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L620-L624), v20.2.4 [:625-629](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L625-L629)), and on the realm, realm-list and period getters' successes, which name their length (`end_header(s, NULL, "application/json", s->formatter->get_len())`, [driver/rados/rgw_rest_realm.cc:49](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rest_realm.cc#L49), [:302](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rest_realm.cc#L302), [:348](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rest_realm.cc#L348), v20.2.4 [:50](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rest_realm.cc#L50), [:323](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rest_realm.cc#L323), [:369](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rest_realm.cc#L369)); no other admin success names one (the flusher's `do_start` and `RGWRESTOp::send_response` call `end_header` without it, [rgw_rest.cc:1035-1042](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L1035-L1042), [:1677-1685](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L1677-L1685)), so none carries `Accept-Ranges`; N sets the pair through G's `s3.SetContentLength`; `x-amz-request-charged` is bucket-scoped and never applies to `/admin` (radosgw's `end_header` guards it on `s->bucket`, which admin requests never set).
- Contract additions from Z: `op.Run`'s admin override on `Identity.Admin` (A sets it to `admin || system`, which is `LocalApplier::is_admin_of` on Squid and `is_admin()` on Tentacle — no gate); `authz.AccountLookup{AccountName}` is satisfied by `op.AccountStore` itself since A Task 1 declared `AccountName`; Task 7 replaces the driver's stub (N-D5).
- From G Task 4, consumed as defined: the leaf package `xmltext` (`Escape`, ceph's `xml_stream_escaper`, [escape.cc:134-169](https://github.com/ceph/ceph/blob/v19.2.6/src/common/escape.cc#L134-L169)), which `internal/formatter`'s XML and HTML writers call instead of an escaper of their own, so every XML document rgw-go writes escapes alike; and `s3.SetContentLength`, radosgw's `dump_content_length`, through which `WriteError` and `WriteTyped` send their length with `Accept-Ranges`.
- Driver internals of later units, consumed read-only by Task 14 only (it waits for them): W's `gcChain`, `rawTag`, `newIndexOp`/`indexOp.prepare`, `deleteObjIndex`, `s.pools`, the writer option `gcMaxConcurrentIO` (W Tasks 4-6, 9); R's `readHead`, `headRef` (R Task 3); P's `abortMultiparts` (P Task 7); phase 0's `refcount.Put` and `rgw.ObjRemove`. No N task edits another unit's file.
- Every radosgw claim is verified at both tags with file:line; the tests use `memstore`, the `opfakes` and `fakerados`; cluster-only tasks are marked `[cluster]` and run on the rooket harness (`make cluster-up RELEASE=squid|tentacle`, `rooket k`), never the ambient cluster.
- Nothing is created under `docs/`; the two existing registries are modified where a task says so. No commits, no cluster use and no subagents by the planner; the implementer commits per task as each task's last step says.

### Decisions this plan fixes

- **N-D1 Mount.** `admin.Handler` is an `http.Handler`; `cli serve` composes `admin.Mount(prefix, adminHandler, s3Handler)`, which routes a path equal to `/<prefix>` or starting with `/<prefix>/` to the admin handler and everything else to S3, when `apis.Admin`; with only `admin` enabled the S3 side is G's 405 handler. Like radosgw (`RGWRESTMgr::get_resource_mgr`, [rgw_rest.cc:1974-1997](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L1974-L1997)) a bucket literally named `admin` is shadowed. `rgw_admin_entry` is read once at startup (default `admin`, [rgw.yaml.in:1014-1021](https://github.com/ceph/ceph/blob/v19.2.6/src/common/options/rgw.yaml.in#L1014-L1021)).
- **N-D2 Formats, rendering and headers.** Every admin response is written through `internal/formatter` (Task 3), a transcription of ceph's `JSONFormatter`, `XMLFormatter` and `HTMLFormatter` ([src/common/Formatter.cc:153-654](https://github.com/ceph/ceph/blob/v19.2.6/src/common/Formatter.cc#L153-L654), [HTMLFormatter.cc:34-168](https://github.com/ceph/ceph/blob/v19.2.6/src/common/HTMLFormatter.cc#L34-L168) at v19.2.6; v20.2.4 differs only in writing `null` for a non-finite double, which no admin dump writes): JSON drops a name at the top level and inside an array; XML makes every section and scalar an element named as the dump call names it, array entries keeping their own names and a space becoming `_`; HTML is XML with scalars as `<li>name: value</li>`; strings are escaped by `json_stream_escaper` and `xml_stream_escaper` ([escape.cc:254-286](https://github.com/ceph/ceph/blob/v19.2.6/src/common/escape.cc#L254-L286), [:134-169](https://github.com/ceph/ceph/blob/v19.2.6/src/common/escape.cc#L134-L169), identical at both tags) and names are written raw. The format is chosen as `RGWHandler_REST::allocate_formatter` chooses it for the admin handlers, whose default is JSON (`RGWHandler_Auth_S3::init`, [rgw_rest_s3.cc:5130-5138](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L5130-L5138); v20.2.4 [:5690-5698](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5690-L5698)): `format=xml|json|html` compared exactly, else the `Accept` value up to its first `;` compared exactly with `text/xml` or `application/xml`, `application/json`, `text/html`, else JSON ([rgw_rest.cc:1732-1764](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L1732-L1764); v20.2.4 [:1736-1768](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L1736-L1768)); the XML formatter lower-cases names when the query carries `bulk-delete` or `multipart-manifest=delete` ([:1791-1797](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L1791-L1797)). A request radosgw finds no handler for — `/admin` itself or a resource it does not register — answers 405 in JSON whatever the format, because `abort_early` allocates a JSONFormatter when no handler allocated one ([rgw_rest.cc:680-683](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L680-L683), [:2290-2305](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L2290-L2305); [rgw_process.cc:316-318](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_process.cc#L316-L318)); every later error renders in the selected format. Bodies follow radosgw's three send paths: a flusher-started body opens with `output_header` — the XML declaration, or the HTML status page, which radosgw closes and flushes before the op's content — then the dump (`RGWRESTFlusher::do_start`, [rgw_rest.cc:1035-1042](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L1035-L1042); `rgw_flush_formatter_and_reset` [:303-314](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L303-L314)); the zone config, metadata get and list dump without `dump_start`, so without a declaration ([rgw_rest_config.cc:35-47](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_config.cc#L35-L47), [rgw_rest_metadata.cc:54-174](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_metadata.cc#L54-L174)); the realm, realm-list and period getters name `Content-Type: application/json` themselves whatever the format ([rgw_rest_realm.cc:48-49](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rest_realm.cc#L48-L49), [:301-302](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rest_realm.cc#L301-L302), [:344-348](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rest_realm.cc#L344-L348); v20.2.4 [:49-50](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rest_realm.cc#L49-L50), [:322-323](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rest_realm.cc#L322-L323), [:365-369](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rest_realm.cc#L365-L369)). Errors are radosgw's `dump(req_state*)` ([rgw_common.cc:381-404](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L381-L404); v20.2.4 [:394-417](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L394-L417)) between `output_header` and `output_footer`: an `Error` section with `Code`, `Message`, `RequestId` and `HostId`, never `BucketName` (admin requests set no bucket name); HTML has no `Error` section. `format=html` is implemented, not refused: radosgw's HTMLFormatter is its XML formatter with `<li>` scalars and a status-page header, so rgw-go reproduces its bodies byte for byte. The `Content-Length` is radosgw's: an admin op other than the three getters hands `end_header` no length, and radosgw's frontend buffers the body and completes the response with its length (`BufferingFilter::complete_request`, [rgw_client_io_filters.h:207-253](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_client_io_filters.h#L207-L253), always in beast's filter chain, [rgw_asio_frontend.cc:320-325](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_asio_frontend.cc#L320-L325), both releases), `Content-Length: 0` on an empty 200, so rgw-go, which sends each body whole with its length, matches it. The `Content-Type` is a difference from radosgw, recorded in docs/exclusions.md's coexistence section by Task 3: every rgw-go body carries one — the format's (`to_mime_type`, [rgw_common.h:189-207](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.h#L189-L207)) or, for the three getters, radosgw's own `application/json` — while radosgw sends none on a flusher-started JSON body or on the zone config body (`end_header` sends one only for an error, a type the op names, or a formatter holding bytes, [rgw_rest.cc:611-631](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L611-L631); v20.2.4 [:616-636](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L616-L636)); an empty success carries none on either gateway. Errors carry both headers on both gateways, and `Accept-Ranges: bytes` with them, which `end_header`'s error branch sets through `dump_content_length` ([rgw_rest.cc:620-624](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L620-L624); v20.2.4 [:625-629](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L625-L629)); the three getters' successes carry it too, since they name their length ([rgw_rest_realm.cc:49](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rest_realm.cc#L49), [:302](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rest_realm.cc#L302), [:348](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rest_realm.cc#L348); v20.2.4 [:50](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rest_realm.cc#L50), [:323](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rest_realm.cc#L323), [:369](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rest_realm.cc#L369)), and no other admin success does, on either gateway. XML and HTML text goes through G's shared `xmltext.Escape`, which is `xml_stream_escaper`. radosgw fixes the 200 when an op starts its flusher (`RGWRESTFlusher::do_start`, [rgw_rest.cc:1035-1042](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L1035-L1042); v20.2.4 [:1040-1047](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L1040-L1047); its frontend sends the buffered response when the request completes), and an op that fails after that point still answers 200, its body cut off where the failure struck (`RGWRESTOp::send_response`, [:1677-1685](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L1677-L1685); v20.2.4 [:1681-1689](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L1681-L1689)): a bucket listing for a uid that does not exist answers 200 with no document, only what the flusher's start wrote (`RGWBucketAdminOp::info`, [rgw_bucket.cc:1649-1665](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_bucket.cc#L1649-L1665); v20.2.4 [:1816-1831](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_bucket.cc#L1816-L1831)), a bucket index check given `check-objects` without `fix` a 200 whose body stops after `invalid_multipart_entries` (`RGWBucketAdminOp::check_index`, [:1256-1267](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_bucket.cc#L1256-L1267); v20.2.4 [:1409-1429](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_bucket.cc#L1409-L1429)), and a usage or user listing whose RADOS read fails midway a partial 200. rgw-go renders every body whole before it answers, so each of these gets the error document with the error's status; that is a difference recorded with the headers by Task 3.
- **N-D3 Opaque listing markers (decision D5 in the index).** `Pool.ListObjectsFrom`'s token is `base64url("1:" + lastOID)`; resume seeks to the object's placement hash — `ceph_str_hash_rjenkins(ns + "\x1f" + oid)` when the namespace is non-empty, `rjenkins(oid)` otherwise (osd_types.cc:1785-1796) — and skips entries with that hash whose oid is `<=` lastOID (hobject order: reversed hash, namespace, oid; [hobject.cc:335-370](https://github.com/ceph/ceph/blob/v19.2.6/src/common/hobject.cc#L335-L370)). go-ceph's `Iter.Token()` after `Next()` is the next-batch cursor ([Objecter.h:2247-2249](https://github.com/ceph/ceph/blob/v19.2.6/src/osdc/Objecter.h#L2247-L2249)) and would skip entries, so it is never used as a marker. No fork change: `Iter.Seek`/`Token`, `Conn.GetFSID` are upstream bindings. The pool's `object_hash` is assumed rjenkins (the default; Rook never changes it) and the integration spec checks the resume end to end. A marker therefore resumes a listing only on the gateway that issued it: radosgw parses every marker as a librados object cursor (`rgw_list_pool`, rgw_tools.cc:354-358; v20.2.4 :370-374) and rgw-go refuses radosgw's with 400 InvalidArgument, a difference recorded in docs/exclusions.md's coexistence section by Task 9.
- **N-D4 Bounded usage trim.** Per usage shard object, `TrimUsage` first counts the matching records with `user_usage_log_read` pages, then repeats `user_usage_log_trim` at most `ceil(count/1000) + 1` times, stopping early on ENODATA. A trim that ends without ENODATA logs one warning naming tracker #72593 (payer-keyed records are never removed on Squid/Tentacle; fix ceph/ceph#65329 is v21-only) and #58136 (a bucket filter stalls behind 1000 other records; fix ceph/ceph#49168 unmerged), and the call still succeeds, as radosgw's would if it ever returned. radosgw's `cls_rgw_usage_log_trim` repeats until ENODATA with no bound ([cls_rgw_client.cc:830-853](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw_client.cc#L830-L853); v20.2.4 [:639-659](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw_client.cc#L639-L659)), so where radosgw's request never returns, rgw-go's answers 200 with the records left. That is a difference from radosgw, recorded in docs/exclusions.md's coexistence section by Task 10.
- **N-D5 One account store, one shape.** The driver implements A's `op.AccountStore`, widened here with the by-name/by-email readers, the writer, the remover and the account-users index. `AccountName(ctx, id)` = `GetAccount(ctx, id).Info.Name` is declared on `op.AccountStore` by A Task 1 with a memstore implementation and a driver stub; Task 7 replaces the driver stub with the real lookup, as it does `GetAccount`'s. `op.AccountStore` therefore satisfies Z's `authz.AccountLookup` from wave 1.
- **N-D6 ENOENT stays NoSuchKey where radosgw passes it through.** `bucket?policy`, link, unlink, `bucket?quota`, `bucket?index`, `bucket?object` (DELETE), `ratelimit`, `metadata` get/remove and `account` get/modify/delete answer 404 `NoSuchKey` for a missing bucket, user or account (raw ENOENT, [rgw_common.cc:97](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L97) row); `bucket` info and `bucket` remove answer `NoSuchBucket` ([rgw_bucket.cc:1643-1644](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_bucket.cc#L1643-L1644), [rgw_rest_bucket.cc:245-247](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rest_bucket.cc#L245-L247)); user ops answer `NoSuchUser`. go-ceph's suite (bucket_test.go "get policy non-existing bucket" → `ErrNoSuchKey`) and Rook (`account.go:89,136` → `ErrNoSuchKey`) depend on this.
- **N-D7 Admin ops carry no IAM action and no op mask.** `policy.ActionNone` is added (additive to Z's package) for `Op.Action()`; `OpMask()` returns 0, as `RGWOp::op_mask()` does for every admin op ([rgw_op.h:299](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.h#L299)), so `verifyOpMask` passes. `VerifyPermission` is the cap check; Authz is never consulted.
- **N-D8 Two admin user documents.** The admin user ops render `dump_user_info` ([rgw_user.cc:132-196](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_user.cc#L132-L196): a `user_info` section, `tenant` first, bare `user_id`, keys without `create_date`, `system`/`admin` always present; v20.2.4 [:133-201](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_user.cc#L133-L201) writes `full_user_id` first, before `tenant`, and `namespace`, when the user has one, between `tenant` and `user_id`), while the `metadata` user section renders `RGWUserCompleteInfo::dump`: `RGWUserInfo::dump` plus `attrs` ([driver/rados/rgw_user.h:731-739](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_user.h#L731-L739); v20.2.4 [driver/rados/rgw_user.cc:2725-2733](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_user.cc#L2725-L2733); `meta.UserCompleteInfo.Dump`, Task 8). Both are implemented; the first in `internal/admin`, the second in `meta`.
- **N-D9 Bucket purge, with and without `bypass-gc`.** Without the flag, `purge-objects` (bucket removal) and `purge-data` (user and account removal) are `RadosBucket::remove(delete_children=true)` ([rgw_sal_rados.cc:350-468](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L350-L468); v20.2.4 [:367-489](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L367-L489)): `Env.Buckets.ListObjects` over every version of the plain namespace in pages of 1000, `Env.Objects.DeleteObject` per entry (a missing key skipped, as `-ENOENT` is), then `Env.Buckets.DeleteBucket`, whose own-bucket branch aborts the in-flight uploads (M Task 7 with P Task 7's `abortMultiparts`); the tails reach GC through W's delete. The multipart namespace is never deleted object by object: radosgw aborts the uploads instead. M's `DeleteBucket` checks that the index is empty where radosgw's purge passes `check_empty = false` ([:445](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L445); v20.2.4 [:462](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L462)), so a PUT landing after the listing makes rgw-go answer 409 BucketNotEmpty where radosgw removes the bucket and orphans that object; that is a difference from radosgw, recorded in docs/exclusions.md's coexistence section by Task 4. With `bypass-gc=true` the REST op takes `RadosBucket::remove_bypass_gc` whatever `purge-objects` says ([rgw_rest_bucket.cc:225-248](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rest_bucket.cc#L225-L248), [rgw_bucket.cc:1282-1327](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_bucket.cc#L1282-L1327); v20.2.4 [:1444-1490](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_bucket.cc#L1444-L1490)), and rgw-go honours it the same way, so `DELETE /admin/bucket?bucket=b&bypass-gc=true` empties and removes a non-empty bucket. The steps ([rgw_sal_rados.cc:470-608](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L470-L608); v20.2.4 [:491-629](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L491-L629), identical): refresh the bucket; read every index shard header as `read_stats` does, a failure returning (an indexless bucket has no shard object, so ENOENT, which the op answers 404 NoSuchBucket); abort the in-flight uploads through the ordinary abort — radosgw's daemon has GC initialised, so their parts go to GC under the upload id ([rgw_sal_rados.cc:3222-3236](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L3222-L3236)); list every version of the plain namespace, 1000 per page, unordered; per entry read the head (a missing head skipped) and, when it has a manifest, release every tail stripe with the GC worker's delete — `refcount put` under the object's `tail_tag`, else its `idtag`, NUL kept, implicit ref (`delete_tail_obj_aio`, [rgw_rados.cc:10659-10685](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L10659-L10685); v20.2.4 [:11596-11622](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L11596-L11622)) — then remove the head: an index prepare DEL under a random tag, `obj_remove` with no kept prefix and no guard, and `delete_obj_index`'s unprepared `complete_del` (`delete_obj_aio`, [:10687-10732](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L10687-L10732), [:6033-6050](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6033-L6050); v20.2.4 [:11624-11669](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L11624-L11669)); an entry without a manifest is left for the last step; finally the ordinary purge above deletes whatever is left and removes the bucket, and its result is the request's. The driver half is `BucketAdminStore.PurgeBypassGC` (Task 14, after W, R and P land; `op.ErrNotImplemented` until then). Three differences from radosgw, recorded in docs/exclusions.md's coexistence section by Task 14: (a) radosgw's REST op never sets `max_aio`, so `concurrent_max` is 0 ([rgw_bucket.h:248](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_bucket.h#L248); v20.2.4 [:240](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_bucket.h#L240)) and the loop's `max_aio--` test fails on the first manifest it walks: that object's tail stripes are neither deleted nor queued for GC, and every later delete is issued with no bound until the final drain ([rgw_sal_rados.cc:542-550](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L542-L550); v20.2.4 [:563-571](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L563-L571)); rgw-go deletes every object's tails and keeps at most `rgw_gc_max_concurrent_io` (10) deletes in flight, the option bounding the GC worker's puts ([rgw_gc.cc:371](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_gc.cc#L371); v20.2.4 [:385](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_gc.cc#L385)); (b) a tail stripe already gone counts as deleted, as the GC worker counts it ([rgw_gc.cc:413-415](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_gc.cc#L413-L415); v20.2.4 [:428](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_gc.cc#L428)), where radosgw's drain returns the ENOENT and stops the removal before the bucket is deleted ([rgw_sal_rados.cc:98-112](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L98-L112), [:585-589](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L585-L589); tracker #24789, a regression: the fix for #40587 was lost in 2019 and no release from v15.1.0 on carries it); through the admin API the zero budget leaves only the final drain, so every head the walk reached is already gone when the op answers 404 NoSuchBucket for a bucket that still exists ([rgw_rest_bucket.cc:245-247](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rest_bucket.cc#L245-L247)), and a retry completes the removal, since the walk skips a missing head ([rgw_sal_rados.cc:522-525](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L522-L525)) and the final `remove` a missing object ([:385-392](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L385-L392)); only radosgw-admin, whose drains come every `--max-concurrent-ios` deletes, can stop partway through an object and fail again on each retry, and it exits 0 with the bucket left ([rgw_admin.cc:8768-8779](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_admin.cc#L8768-L8779), [:11600](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_admin.cc#L11600)); (c) the tail puts carry `pool_full_try`, as the GC worker's do ([rgw_gc.cc:677](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_gc.cc#L677); v20.2.4 [:689](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_gc.cc#L689)), so a purge frees space on a full pool. The defects behind (a) and (b) are in docs/ceph-upstream-bugs.md on main ("radosgw's admin API bypass-gc removal leaks a tail and runs unbounded" and "radosgw's bypass-gc bucket removal fails once a tail stripe is gone"); Task 14 adds rgw-go's realization to their **rgw-go** lines. Until W and P land, either purge on the real driver answers their `op.ErrNotImplemented`.
- **N-D10 Release gates in this unit** (each keyed on `Env.Zone.Release()`): Tentacle-only `default-storage-class` parameter (Squid splits `default-placement` as `name/class`, rgw_rest_user.cc diff); Tentacle-only `full_user_id` (first) and `namespace` (after `tenant`) in the admin user document; account `GET`/`DELETE` caps `account` (Squid, [rgw_rest_account.cc:174](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_account.cc#L174),196) vs `accounts` (Tentacle); Tentacle-only `PUT /admin/account?quota` (Squid routes `?quota` to Modify, which ignores quota parameters); the dump keys Tentacle adds to the zone family, which the zone config and period getters write only on Tentacle — `dedup_pool`, `bucket_logging_pool` and `restore_pool` in `RGWZoneParams::dump` (v20.2.4 [rgw_zone.cc:317](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_zone.cc#L317), [:334](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_zone.cc#L334), [:339](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_zone.cc#L339)), `allow_read_through`, `read_through_restore_days`, `restore_storage_class` and `s3-glacier` in `RGWZoneGroupPlacementTier::dump` ([:934-942](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_zone.cc#L934-L942)), the keys the phase-0 corpus gate already drops for Squid (`postSquidKeys`, internal/meta/goldens_test.go); the three keys Tentacle's `bucket_stats` adds — `reshard_status` (`to_string` of the layout's reshard state) and `judge_reshard_lock_time` after `index_generation`, `read_tracker` (the instance's read version) last (v20.2.4 [driver/rados/rgw_bucket.cc:1566-1567](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_bucket.cc#L1566-L1567), [:1596](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_bucket.cc#L1596)); the order of a bucket index check's `invalid_multipart_entries`, key order on Squid and shard by shard on Tentacle, whose rewrite of `check_bad_index_multipart` walks each shard with `bi_list` (v20.2.4 [:343-482](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_bucket.cc#L343-L482), [:497-557](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_bucket.cc#L497-L557); Task 11). Nothing else in the admin surface differs between v19.2.6 and v20.2.4 (diffs of rgw_rest_{user,bucket,realm,usage,metadata,config,ratelimit,info,account}.cc, rgw_usage.cc, rgw_account.cc, cls_rgw.cc usage methods, and of the dump functions the ops call in rgw_common.cc, rgw_user.cc, rgw_bucket.cc, rgw_acl.cc, rgw_zone.cc, rgw_period.cc and rgw_realm.cc, between the two tags; Tentacle's rewrite of `check_bad_index_multipart` finds the same entries).
- **N-D11 One dump per type; `MarshalJSON` stays.** The admin API renders `meta` and `acl` values only through `Dump(f formatter.Formatter, rel denc.Release)` methods (`meta.Dumper`) that make the C++ `dump()`'s calls, in its order, as release `rel` writes them. Phase 0's `MarshalJSON` methods are left as they are: encoding/json requires valid JSON while `JSONFormattable::encode_json` writes an unquoted value bare ([ceph_json.cc:29-36](https://github.com/ceph/ceph/blob/v19.2.6/src/common/ceph_json.cc#L29-L36), [:922-946](https://github.com/ceph/ceph/blob/v19.2.6/src/common/ceph_json.cc#L922-L946)); `MarshalJSON` follows v20.2.4 on every release while the admin API must answer as the cluster's release does; and nothing merged in phase 0 has to change. The cost is two JSON renderings per type, held together by the corpus: a new spec renders every golden type's `Dump` at Squid through `formatter.NewJSON(true)` inside an `object` section, exactly as ceph-dencoder's `dump_json` does ([ceph_dencoder.cc:189-194](https://github.com/ceph/ceph/blob/v19.2.6/src/tools/ceph-dencoder/ceph_dencoder.cc#L189-L194), [denc_registry.h:79-81](https://github.com/ceph/ceph/blob/v19.2.6/src/tools/ceph-dencoder/denc_registry.h#L79-L81)), and compares the bytes with the golden, which pins key order and the formatting of numbers, strings and empty sections, while phase 0's canonical comparison of `MarshalJSON` keeps running. A field combination the corpus lacks is covered only by the cluster gate's byte comparison with radosgw (Task 13). A value rgw-go carries opaque (a bucket's website configuration or sync policy, a zonegroup's sync groups) makes `Dump` call `f.Fail(meta.ErrOpaqueJSON)` exactly where `MarshalJSON` fails, and the handler answers 501 NotImplemented where radosgw renders the record; that is a difference from radosgw, recorded beside the existing opaque-record bullets of docs/exclusions.md's coexistence section by Task 9 (metadata) and Task 12 (period).

## Review Focus

The seven inputs the spec implies but no task's tests exercise directly, most likely to bite first; each line's test is added to the owning task in its own step style.

1. **A marker from another gateway or a stale one.** An operator pages `metadata list` with a radosgw `ObjectCursor` string, with an arbitrary base64 string, or with an rgw-go token whose last object was deleted. Expected, as radosgw's own handling implies ([rgw_rest_metadata.cc:83-92](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_metadata.cc#L83-L92), [rgw_tools.cc:354-358](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_tools.cc#L354-L358)): a marker that is not base64 restarts from the beginning; one that decodes but is not an rgw-go token is `InvalidArgument` (400); a deleted last object resumes at the next object after it in hash order; never a crash or an endless page. Pinned in Task 2 (goceph and fakerados specs), Task 8 (`List` maps `ErrBadOp`) and Task 9 (the handler).
2. **Query keys in unexpected order.** `PUT /admin/user?key&quota&uid=x`: radosgw honours the FIRST admin sub-resource in the query string (`RGWHTTPArgs::append`, [rgw_common.cc:962-975](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L962-L975)), not the dispatcher's order. Expected: `key` wins. Pinned in Task 3's dispatch specs.
3. **Booleans radosgw tolerates.** `suspended=` (empty), `suspended=1`, `SUSPENDED=True`: `RESTArgs::get_bool` accepts empty, `true`/`1` and `false`/`0` case-insensitively and answers 400 for `yes` only where the op checks the return (`ratelimit` does; user ops ignore it and take the default). Pinned in Task 3 (args specs) and Task 12 (ratelimit rejects `global=yes`).
4. **A usage trim that never reaches ENODATA.** A shard holding payer-keyed records (tracker #72593) or a bucket filter behind other records (#58136). Expected: the call returns 200 within `ceil(n/1000)+1` rounds and logs one warning naming the trackers; rgw-go never spins. Pinned in Task 10 with a fakerados emulation of both defects.
5. **An account user through the user endpoints.** `PUT /admin/user?uid=x&account-id=RGW...` with a mismatched tenant, an `account-root` user without an account, or `POST` moving a user out of its account. Expected: 400 `InvalidArgument` with radosgw's message, and the account-users index kept in step on create and remove. Pinned in Tasks 4 and 7.
6. **An `Accept` header that is a list, a format in capitals, or a format on a path radosgw has no handler for.** `Accept: application/xml, text/html`, `Accept: Application/XML` and `format=XML` all answer JSON, because radosgw compares the whole value (cut at the first `;`) and the `format` argument exactly ([rgw_rest.cc:1739-1760](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L1739-L1760)); `Accept: application/xml;q=0.9` answers XML; `GET /admin/nosuch?format=xml` answers 405 in JSON, since no handler allocated a formatter ([rgw_rest.cc:680-683](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L680-L683)), while `POST /admin/info?format=xml` answers 405 in XML. Pinned in Task 3's handler specs.
7. **`bypass-gc` without `purge-objects`, on a bucket whose first listed object has tail stripes.** radosgw purges regardless of `purge-objects` (rgw_bucket.cc:1303-1304) and, with the REST op's zero `max_aio`, leaves that first object's tail stripes behind ([rgw_sal_rados.cc:542-550](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L542-L550)). Expected of rgw-go: the bucket is emptied and removed, every tail stripe is gone, and no GC entry names the bucket's marker. Pinned in Task 6 (op), Task 14 (driver on fakerados) and checked live in Task 13.

## File structure

```
internal/formatter/doc.go                     package doc: ceph's JSONFormatter, XMLFormatter and HTMLFormatter transcribed (Task 3)
internal/formatter/formatter.go               Formatter interface, EscapeJSON; XML text through G's xmltext.Escape (Task 3)
internal/formatter/json.go, xml.go, html.go   NewJSON, NewXML, NewHTML (Task 3)
internal/formatter/*_test.go                  byte strings derived from Formatter.cc and HTMLFormatter.cc (Task 3)
internal/admin/doc.go                         package doc: the admin protocol layer, what it reuses, N-D1..N-D11
internal/admin/handler.go                     Handler, NewHandler, Mount, ServeHTTP lifecycle (Task 3)
internal/admin/request.go                     ParseArgs, ParseRequest, SelectFormat: resource path, RESTArgs semantics, sub-resource, format (Task 3)
internal/admin/dispatch.go                    the resource × method × sub-resource table → radosgw op names and payload forms (Task 3)
internal/admin/render.go                      WriteBody, WriteDumped, WriteTyped, WriteEmpty, WriteError; radosgw's status names (Task 3)
internal/admin/info.go                        get_info, get_zone_config handlers (Task 3)
internal/admin/user.go                        user create/info/modify/remove/list handlers and dumpUserInfo (Tasks 4, 9)
internal/admin/user_sub.go                    key, subuser, caps and quota handlers and their dumps (Task 5)
internal/admin/bucket.go                      bucket info/list/link/unlink/remove/quota/policy/object handlers, bucketStats (Task 6)
internal/admin/bucket_index.go                check_bucket_index handler (Task 11)
internal/admin/account.go                     account handlers, the Tentacle quota op (Task 7)
internal/admin/metadata.go                    metadata get/put/list/remove handlers (Task 9)
internal/admin/usage.go                       get_usage, trim_usage handlers and the usage dump (Task 10)
internal/admin/realm.go                       get_realm, list_realms, get_period handlers (Task 12)
internal/admin/ratelimit.go                   get/put ratelimit handlers (Task 12)
internal/admin/*_test.go                      handler specs on memstore through httptest, JSON, XML and HTML bodies
internal/op/adminstores.go                    UsageReader, BucketAdminStore, RealmStore, the widened AccountStore, Env fields (Task 1)
internal/op/adminuser.go                      CreateUser, GetUserInfo, ModifyUser, RemoveUser, ListUsers ops (Task 4)
internal/op/adminpurge.go                     PurgeBucket, DeleteBucketWithChildren (Task 4), RemoveBucketBypassGC (Task 6)
internal/op/adminuser_sub.go                  CreateKey, RemoveKey, CreateSubuser, ModifySubuser, RemoveSubuser, AddCaps, RemoveCaps, GetUserQuota, SetUserQuota ops (Task 5)
internal/op/adminbucket.go                    BucketInfo, LinkBucket, UnlinkBucket, RemoveBucketAdmin, SetBucketQuota, GetBucketPolicy, RemoveObjectAdmin (Task 6)
internal/op/adminbucket_index.go              CheckBucketIndex op (Task 11)
internal/op/adminaccount.go                   CreateAccount, GetAccountInfo, ModifyAccount, RemoveAccount ops (Task 7)
internal/op/adminmetadata.go                  GetMetadata, PutMetadata, ListMetadata, RemoveMetadata ops (Task 9)
internal/op/adminusage.go                     ShowUsage, TrimUsage ops (Task 10)
internal/op/admininfo.go                      GetInfo, GetZoneConfig, GetRealm, ListRealms, GetPeriod, GetRateLimit, SetRateLimit ops (Tasks 3, 12)
internal/op/opfakes/                          regenerated (Task 1)
internal/policy/action.go                     modify: ActionNone (Task 1)
internal/acl/dump.go                          Policy, Owner, List and Grant Dump methods (Task 6)
internal/meta/caps.go                         ParseCaps, Caps.AddString/RemoveString/Check, ValidCapType, ParseOpTypeList, OpTypeString, ParseSubuserPerm, PermString (Task 1)
internal/meta/time.go                         modify: Time.Gmtime, which MarshalJSON calls (Task 3)
internal/meta/dump.go                         Dumper, the encode_json helpers, Quota.Dump (Task 3), Caps.DumpAs (Task 4), dumpAttrs, ObjVersion.Dump (Task 8)
internal/meta/dump_zone.go                    ZoneParams, ZonePlacementInfo, ZoneStorageClasses, ZoneStorageClass Dump; JSONFormattable.DumpAs (Task 3)
internal/meta/dump_bucket.go                  BucketID and DataPlacement Dump (Task 6), BucketEntryPoint, BucketInfo and BucketCompleteInfo Dump (Task 8)
internal/meta/dump_account.go                 AccountInfo.Dump (Task 7)
internal/meta/dump_user.go                    UserInfo and UserCompleteInfo Dump with the subuser and key callbacks (Task 8)
internal/meta/dump_period.go                  Realm, Period, PeriodMap, PeriodConfig, ZoneGroup, Zone, placement target and tier Dump; RateLimitInfo.Dump (Task 12)
internal/meta/dump_test.go, dump_goldens_test.go   release keys; the byte-exact corpus gate each Dump task extends (Tasks 3, 4, 6, 7, 8, 12)
internal/meta/user_json.go                    UserInfo.UnmarshalJSON, Caps.UnmarshalJSON, UserCompleteInfo (Task 8)
internal/meta/bucket_json.go                  BucketInfo.UnmarshalJSON, BucketEntryPoint.UnmarshalJSON, AttrsJSON, BucketCompleteInfo (Task 8)
internal/radosclient/cluster.go               modify: Cluster.FSID, Pool.ListObjectsFrom (Task 2)
internal/radosclient/goceph/cluster.go        modify: FSID (Task 2)
internal/radosclient/goceph/pool.go           modify: ListObjectsFrom (Task 2)
internal/radosclient/goceph/rjenkins.go       ceph_str_hash_rjenkins, placementHash, list tokens (Task 2)
internal/testutil/fakerados/cluster.go        modify: FSID, ListObjectsFrom (Task 2)
internal/testutil/fakerados/cls_rgw.go        modify: user_usage_log_read/trim/clear, bucket_check_index/rebuild emulation (Tasks 10, 11)
internal/testutil/fakerados/cls_user.go       modify: account_resource_add/get/rm/list emulation (Task 7)
internal/cls/user/account.go                  account resource ops and types (Task 7)
internal/driver/store.go                      modify: Env fields, AddWorker none; stubs removed as tasks fill them (Tasks 1, 6, 7, 8, 10, 12, 14)
internal/driver/bucketadmin.go                BucketAdminStore: index stats with shard strings, ChangeBucketOwner, UnlinkBucketOwner, RenameBucket, index check/rebuild, RemoveIndexEntries (Tasks 6, 11)
internal/driver/purge.go                      BucketAdminStore.PurgeBypassGC: remove_bypass_gc's data pass (Task 14)
internal/driver/account.go                    AccountStore: by-name/by-email indexes, PutAccount, RemoveAccount, account-users index, AccountName (Task 7)
internal/driver/metadata.go                   MetadataStore over the three sections; key/oid mapping; version rules; typed Doc (Task 8)
internal/driver/usageadmin.go                 ReadUsage, TrimUsage with N-D4's bound (Task 10)
internal/driver/realm.go                      RealmStore: realm by id/name/default, ListRealms, GetPeriod, period config read/write (Task 12)
internal/memstore/admin.go                    the same interfaces in memory; AddUsage for the specs (Tasks 1, 6, 7, 8, 10, 12)
internal/cli/serve.go                         modify: admin.Mount when apis.Admin (Task 3)
test/integration/admin_test.go                [cluster] seams and the admin surface against a rooket cluster, bodies compared with radosgw's in JSON and XML (Task 13)
hack/rooket/README.md                         modify: how to run the admin suite against rgw-go (Task 13)
docs/exclusions.md                            modify: one coexistence bullet per difference, written by the task that introduces it (Tasks 3, 4, 6, 9, 10, 11, 12, 14)
docs/ceph-upstream-bugs.md                    modify: the two usage-trim entries' rgw-go lines and two quirks (Task 13); the rgw-go sentences of the two bypass-gc entries main carries, the unbounded leak and the missing-tail failure (Task 14)
```

## Task index

| # | Task | Depends on | Cluster |
|---|---|---|---|
| 1 | Contract additions: interfaces, Env fields, `policy.ActionNone`, `meta` cap/perm parsers, fakes, memstore, driver stubs | G, A, M, Z merged | no |
| 2 | Seams: `Cluster.FSID`, `Pool.ListObjectsFrom` with the rjenkins token; fakerados | 1 | integration spec in 13 |
| 3 | `internal/formatter`, the zone `Dump` methods and the `internal/admin` core: mount, parse, dispatch, args, rendering in JSON, XML and HTML, caps; `/admin/info`, `/admin/config?type=zone`; `cli serve` wiring | 1, 2 | no |
| 4 | User admin core: create, info, modify, remove, list; the admin user document | 3 | no |
| 5 | Keys, subusers, caps and quota | 4 | no |
| 6 | Bucket admin: driver `BucketAdminStore`; info, list, link, unlink, remove with and without bypass-gc, quota, policy, object remove | 3 | no |
| 7 | Accounts: `cls/user` account resources, driver `AccountStore`, the ops, Tentacle quota, `AccountName` | 4 | no |
| 8 | Metadata driver: `MetadataStore` for user, bucket, bucket.instance; the JSON decoders and the documents' `Dump` methods | 2, 6 | no |
| 9 | Metadata REST ops and `user?list` | 8 | no |
| 10 | Usage: driver read and bounded trim, fakerados emulation of the two defects, REST show and trim | 6 | no |
| 11 | Bucket index check and rebuild | 6 | no |
| 12 | Realm and period getters; ratelimit | 3 | no |
| 13 | Gate: integration spec with JSON and XML bodies compared with radosgw's, bypass-gc, go-ceph suite, Rook-first checklist, registry updates `[cluster]` | all, Task 14 included | yes |
| 14 | Bypass-GC purge in the driver: `PurgeBypassGC` over W's, R's and P's internals | 6; W Tasks 4-6 and 9, R Task 3, P Task 7 | no |

Rook-first order: Tasks 1–7 give the operator every call it makes (`user/*`, `bucket?quota`, `bucket` info/list, link, remove, `bucket?policy`, accounts); Task 9 gives go-ceph's `GetUsers`; Tasks 10–12 complete the go-ceph suite and the dashboard's read-only getters. Task 14 waits for units W, R and P (waves 3 and 4) and runs before the gate; until it lands `DELETE /admin/bucket?bypass-gc=true` on the real driver answers 501.

---

## Task 1: Contract additions: interfaces, `Env` fields, `policy.ActionNone`, `meta` cap/perm parsers, fakes, memstore, driver stubs

**Files:**
- Create: `internal/op/adminstores.go`, `internal/op/adminstores_test.go`, `internal/meta/caps.go`, `internal/meta/caps_test.go`, `internal/memstore/admin.go`, `internal/memstore/admin_test.go`
- Modify: `internal/op/account.go` (A's `AccountStore` widened), `internal/op/env.go` (`Env.UsageReader`, `Env.BucketAdmin`, `Env.Realms`), `internal/op/doc.go` (counterfeiter directives), `internal/policy/action.go` (`ActionNone`), `internal/policy/action_test.go`, `internal/driver/store.go` (`Env()` wires the new fields; every new method answers `op.ErrNotImplemented` until its task), `internal/meta/user.go` (export the two mask tables' string helpers through `caps.go`)
- Generated: `internal/op/opfakes/fake_usage_reader.go`, `fake_bucket_admin_store.go`, `fake_realm_store.go`, `fake_account_store.go` (regenerated)

**Interfaces:**
- Consumes: G's `op.Env`, `op.BucketRecord`, `op.Stats`, `op.Error` sentinels, `memstore.Store` (its mutex and version helper), `driver.Store.Env()`; A's `op.AccountRecord`, `op.AccountStore{GetAccount}`, `memstore.AddAccount`; M's `meta.ObjVersion`; `meta.Caps`, the unexported `rgwPerms`/`opTypeFlags`/`capNames` tables and `maskString`/`permString`/`capPermString` in `internal/meta/user.go`.
- Produces:

```go
package op

// UsageData is rgw_usage_data.
type UsageData struct{ BytesSent, BytesReceived, Ops, SuccessfulOps uint64 }

// S3SelectUsage is rgw_s3select_usage_data.
type S3SelectUsage struct{ BytesProcessed, BytesReturned uint64 }

// UsageRecord is one rgw_usage_log_entry as user_usage_log_read returns it:
// keyed by the payer when the record has one, else the owner (cls_rgw.cc
// usage_log_read_cb, v19.2.6:3706-3720), already aggregated per (User,
// Bucket) within one page, Epoch being the first record's.
type UsageRecord struct {
	User, Owner, Payer, Bucket string
	Epoch                      uint64
	Total                      UsageData
	Categories                 map[string]UsageData
	S3Select                   S3SelectUsage
}

// UsageIter is RGWUsageIter: the shard index and the class read iterator a
// paged ReadUsage continues from.
type UsageIter struct {
	Index    uint32
	ReadIter string
}

//counterfeiter:generate . UsageReader

// UsageReader reads and trims the usage log; the admin API's usage ops.
type UsageReader interface {
	// ReadUsage is RGWRados::read_usage (rgw_rados.cc:1674-1716): every shard
	// of user (all shards when user is ""), records of bucket only when bucket
	// is non-empty, epochs in [start, end). It returns at most max records and
	// advances it; truncated says whether a further call has more.
	ReadUsage(ctx context.Context, user, bucket string, start, end uint64, max uint32, it *UsageIter) (recs []UsageRecord, truncated bool, err error)
	// TrimUsage is RGWRados::trim_usage (:1718-1736) with at most
	// ceil(records/1000)+1 trim rounds per shard, since radosgw's usage
	// trim may never answer ENODATA (trackers #72593, #58136). It succeeds
	// when nothing is left to trim, and also when the bound is reached; the
	// driver logs the second case.
	TrimUsage(ctx context.Context, user, bucket string, start, end uint64) error
}

// CategoryStats is RGWStorageStats for one RGWObjCategory.
type CategoryStats struct{ Size, SizeRounded, SizeUtilized, NumObjects uint64 }

// BucketIndexStats is RGWRados::get_bucket_stats' output (rgw_rados.cc:8872-8914):
// per-category totals over every shard and the three per-shard strings in
// BucketIndexShardsManager::to_string form, "<shard>#<value>" joined by ",".
type BucketIndexStats struct {
	Categories               map[string]CategoryStats // keyed by to_string(RGWObjCategory): "rgw.main", ...
	Ver, MasterVer, MaxMarker string
}

//counterfeiter:generate . BucketAdminStore

// BucketAdminStore is what the admin bucket ops need of the driver and no S3
// op does.
type BucketAdminStore interface {
	IndexStats(ctx context.Context, rec *BucketRecord) (BucketIndexStats, error)
	// ChangeBucketOwner is RGWBucketAdminOp::link's storage steps
	// (driver/rados/rgw_bucket.cc:1093-1184): unlink from the ACL owner, a
	// default ACL for owner with displayName, Info.Owner rewritten, the entry
	// point relinked; with newName the bucket is renamed (a new instance and
	// entry point, the old ones removed). rec is updated in place.
	ChangeBucketOwner(ctx context.Context, rec *BucketRecord, owner meta.Owner, displayName string, newName *meta.BucketID) error
	// UnlinkBucketOwner is RGWBucketCtl::unlink_bucket with update_entrypoint
	// (rgw_bucket.cc:1023): the cls_user entry removed and the entry point
	// written with linked=false.
	UnlinkBucketOwner(ctx context.Context, rec *BucketRecord, owner meta.Owner) error
	// CheckIndex is RGWRados::bucket_check_index (rgw_rados.cc:5481-5514);
	// RebuildIndex is bucket_rebuild_index (:5516-5527).
	CheckIndex(ctx context.Context, rec *BucketRecord) (existing, calculated map[string]CategoryStats, err error)
	RebuildIndex(ctx context.Context, rec *BucketRecord) error
	// RemoveIndexEntries is RGWRados::remove_objs_from_index (:10247).
	RemoveIndexEntries(ctx context.Context, rec *BucketRecord, keys []meta.ObjKey) error
	// ChownBucket is RadosBucket::chown (rgw_sal_rados.cc:699-751), the form
	// account migration uses: unlink the old owner, link the new one, rewrite
	// Info.Owner, and in the ACL replace the old owner's canonical grant by a
	// FULL_CONTROL grant for the new owner and the owner itself; other grants
	// stay. ECANCELED is retried up to 10 times (adopt_user_bucket, rgw_user.cc:1681-1712).
	ChownBucket(ctx context.Context, rec *BucketRecord, owner meta.Owner, displayName string) error
	// SyncOwnerStats is rgw_sync_all_stats (rgw_user.cc:16-55), what
	// user info's sync=true runs: every bucket of owner resynced into the
	// owner's stats, then the sync completed.
	SyncOwnerStats(ctx context.Context, owner meta.Owner) error
	// PurgeBypassGC is RadosBucket::remove_bypass_gc's data pass
	// (rgw_sal_rados.cc:483-590): the index shard headers read as read_stats
	// reads them, the in-flight uploads aborted, then every listed object's
	// tail stripes released directly and its head and index entry removed,
	// instead of queueing the tails for GC. The bucket itself is left for
	// the ordinary purge the caller runs next, as remove_bypass_gc ends in
	// remove(delete_children=true) (:598-605).
	PurgeBypassGC(ctx context.Context, rec *BucketRecord) error
}

//counterfeiter:generate . RealmStore

// RealmStore reads the realm and period objects of the root pool for the
// read-only admin getters and the period config for ratelimit.
type RealmStore interface {
	// GetRealm is RGWRealm::init (rgw_zone.cc:106-145): by id, else by name,
	// else the default realm; a missing object is ErrNoSuchKey.
	GetRealm(ctx context.Context, id, name string) (meta.Realm, error)
	// ListRealms is RGWSI_Zone::list_realms plus read_default_id (svc_zone.cc:421-427,
	// rgw_zone.cc:1092-1105); a missing default is "".
	ListRealms(ctx context.Context) (defaultID string, names []string, err error)
	// GetPeriod is RGWPeriod::init (rgw_period.cc:14-46): an empty periodID is
	// the realm's current period, a zero epoch its latest epoch.
	GetPeriod(ctx context.Context, realmID, periodID string, epoch uint32) (meta.Period, error)
	// GetPeriodConfig reads "period_config.<realm id>" (rgw_zone.cc:616-636,
	// :671-677); a missing object is ErrNotFound.
	GetPeriodConfig(ctx context.Context, realmID string) (meta.PeriodConfig, error)
	PutPeriodConfig(ctx context.Context, realmID string, cfg meta.PeriodConfig) error
}

// PutAccountOptions is store_account's exclusive flag.
type PutAccountOptions struct{ Exclusive bool }

//counterfeiter:generate . AccountStore

// AccountStore is the account reads, the admin API's writes and indexes
// (driver/rados/account.cc), and the AccountName authz.AccountLookup needs.
type AccountStore interface {
	GetAccount(ctx context.Context, id string) (*AccountRecord, error)
	GetAccountByName(ctx context.Context, tenant, name string) (*AccountRecord, error)
	GetAccountByEmail(ctx context.Context, email string) (*AccountRecord, error)
	// PutAccount is rgwrados::account::write (account.cc:242-392): old is the
	// record before the change (nil on create); a name or email another
	// account holds is ErrAccountAlreadyExists; a lost version race is
	// ErrConcurrentModification.
	PutAccount(ctx context.Context, rec *AccountRecord, old *meta.AccountInfo, opts PutAccountOptions) error
	// RemoveAccount is account.cc:394-438.
	RemoveAccount(ctx context.Context, rec *AccountRecord) error
	// The users index "users.<account>" (driver/rados/users.cc): AddAccountUser
	// is add with exclusive=false and no limit, keyed by the display name;
	// RemoveAccountUser removes that key; ListAccountUsers returns user ids.
	AddAccountUser(ctx context.Context, accountID string, info meta.UserInfo) error
	RemoveAccountUser(ctx context.Context, accountID, displayName string) error
	ListAccountUsers(ctx context.Context, accountID, marker string, max uint32) (ids []string, next string, err error)
	// AccountName is GetAccount(id).Info.Name.
	AccountName(ctx context.Context, id string) (string, error)
}

// Env gains, after Accounts:
//	UsageReader UsageReader
//	BucketAdmin BucketAdminStore
//	Realms      RealmStore
```

`AccountName` is declared by A Task 1; it is listed above only because Task 7 replaces the driver's stub.

```go
package policy

// ActionNone is the action of an op radosgw's IAM never sees: the admin
// ops. String() is "", ParseAction never returns it, Known is false.
const ActionNone Action = 0xFFFF
```

```go
package meta

// RGW_CAP_* (rgw_common.h:209-211) and RGW_PERM_INVALID.
const (
	CapRead  uint32 = 0x1
	CapWrite uint32 = 0x2
	CapAll   uint32 = CapRead | CapWrite
	PermInvalid uint32 = 0xFF00
)
var ErrInvalidCap = errors.New("meta: invalid capability")

// ValidCapType is RGWUserCaps::is_valid_cap_type (rgw_common.cc:2083-2110).
func ValidCapType(t string) bool
// ParseCap is RGWUserCaps::get_cap (:1907-1929): "type=perm" with perm a
// comma list of "*", "read", "write" (unknown words ignored, an absent perm
// is 0); an unknown type is ErrInvalidCap. Whitespace around the type is trimmed.
func ParseCap(cap string) (typ string, perm uint32, err error)
// AddString is add_from_string (:1966-1982): ';'-separated caps or-ed in.
func (c Caps) AddString(s string) error
// RemoveString is remove_from_string (:1984-2000): bits cleared, empty types deleted.
func (c Caps) RemoveString(s string) error
// Check is check_cap (:2071-2081).
func (c Caps) Check(typ string, perm uint32) bool
// ParseOpTypeList is rgw_parse_op_type_list (:2162-2165, :1883-1900): "*", read, write, delete; unknown words ignored.
func ParseOpTypeList(s string) uint32
// OpTypeString is op_type_to_str: the form user JSON shows ("read, write, delete").
func OpTypeString(mask uint32) string
// ParseSubuserPerm is rgw_str_to_perm (:2696-2710): "" none, read, write, readwrite, full; else PermInvalid.
func ParseSubuserPerm(s string) uint32
// PermString is perm_to_str ("read-write", "full-control", "<none>").
func PermString(mask uint32) string
```

```go
package memstore

// The Store implements op.UsageReader, op.BucketAdminStore, op.RealmStore and
// the widened op.AccountStore in memory.
func (s *Store) AddUsage(rec op.UsageRecord) // seeds the usage log for the specs
```

Tasks 6, 7, 10 and 12 fill the memstore behaviour; this task adds the data and the account indexes.

- [ ] **Step 1: Write the failing `meta` cap-parser specs**

`internal/meta/caps_test.go`:

```go
var _ = Describe("caps parsing", func() {
	DescribeTable("ParseCap is RGWUserCaps::get_cap",
		func(in, wantType string, wantPerm uint32, wantErr error) {
			typ, perm, err := meta.ParseCap(in)
			if wantErr != nil {
				Expect(err).To(MatchError(wantErr))
				return
			}
			Expect(err).NotTo(HaveOccurred())
			Expect(typ).To(Equal(wantType))
			Expect(perm).To(Equal(wantPerm))
		},
		Entry("read", "users=read", "users", meta.CapRead, nil),
		Entry("star", "buckets=*", "buckets", meta.CapAll, nil),
		Entry("read, write", "usage=read, write", "usage", meta.CapAll, nil),
		Entry("trimmed type", " metadata =write", "metadata", meta.CapWrite, nil),
		Entry("no perm", "info=", "info", uint32(0), nil),
		Entry("unknown word is ignored", "zone=bogus", "zone", uint32(0), nil),
		Entry("unknown type", "kittens=read", "", uint32(0), meta.ErrInvalidCap),
		Entry("no equals", "users", "", uint32(0), meta.ErrInvalidCap),
	)
	It("adds and removes ';'-separated caps as add_from_string and remove_from_string do", func() {
		c := meta.Caps{}
		Expect(c.AddString("users=read;buckets=*")).To(Succeed())
		Expect(c).To(Equal(meta.Caps{"users": meta.CapRead, "buckets": meta.CapAll}))
		Expect(c.AddString("users=write")).To(Succeed())
		Expect(c["users"]).To(Equal(meta.CapAll))
		Expect(c.RemoveString("users=read;buckets=*")).To(Succeed())
		Expect(c).To(Equal(meta.Caps{"users": meta.CapWrite}))
		Expect(c.RemoveString("nosuch=read")).To(MatchError(meta.ErrInvalidCap))
		Expect(c.RemoveString("info=read")).To(Succeed(), "removing an absent type is a no-op")
	})
	It("checks caps as check_cap does", func() {
		c := meta.Caps{"users": meta.CapRead}
		Expect(c.Check("users", meta.CapRead)).To(BeTrue())
		Expect(c.Check("users", meta.CapWrite)).To(BeFalse())
		Expect(c.Check("buckets", meta.CapRead)).To(BeFalse())
	})
	DescribeTable("op types and subuser perms",
		func(in string, want uint32, wantStr string) {
			Expect(meta.ParseOpTypeList(in)).To(Equal(want))
			Expect(meta.OpTypeString(want)).To(Equal(wantStr))
		},
		Entry("all", "read, write, delete", uint32(meta.OpTypeAll), "read, write, delete"),
		Entry("delete only", "delete", uint32(0x4), "delete"),
		Entry("star", "*", uint32(meta.OpTypeAll), "read, write, delete"),
		Entry("bogus is ignored", "bogus", uint32(0), "<none>"),
	)
	DescribeTable("ParseSubuserPerm is rgw_str_to_perm",
		func(in string, want uint32) { Expect(meta.ParseSubuserPerm(in)).To(Equal(want)) },
		Entry("empty", "", uint32(0)),
		Entry("read", "read", uint32(0x1)),
		Entry("readwrite", "readwrite", uint32(0x3)),
		Entry("full", "FULL", uint32(0xf)),
		Entry("invalid", "read-write", meta.PermInvalid),
	)
	It("renders perms as perm_to_str", func() {
		Expect(meta.PermString(0x3)).To(Equal("read-write"))
		Expect(meta.PermString(0xf)).To(Equal("full-control"))
		Expect(meta.PermString(0)).To(Equal("<none>"))
	})
})
```

- [ ] **Step 2: Run to fail, implement `internal/meta/caps.go`**

Run: `GOTOOLCHAIN=local go test -tags ceph_preview ./internal/meta/ -run TestMeta -ginkgo.focus "caps parsing"` — FAIL (undefined symbols). Then:

```go
package meta

import (
	"errors"
	"strings"
)

const (
	CapRead     uint32 = 0x1
	CapWrite    uint32 = 0x2
	CapAll      uint32 = CapRead | CapWrite
	PermInvalid uint32 = 0xFF00
)

// ErrInvalidCap is -ERR_INVALID_CAP: an unknown capability type.
var ErrInvalidCap = errors.New("meta: invalid capability")

// capTypes is RGWUserCaps::is_valid_cap_type's list (rgw_common.cc:2085-2101).
var capTypes = map[string]struct{}{
	"user": {}, "users": {}, "buckets": {}, "metadata": {}, "info": {}, "usage": {}, "zone": {},
	"bilog": {}, "mdlog": {}, "datalog": {}, "roles": {}, "user-policy": {}, "amz-cache": {},
	"oidc-provider": {}, "user-info-without-keys": {}, "ratelimit": {}, "accounts": {},
}

func ValidCapType(t string) bool { _, ok := capTypes[t]; return ok }

// parseFlags is rgw_parse_list_of_flags: a comma list, unknown words ignored.
func parseFlags(table []flagName, s string) uint32 {
	var v uint32
	for _, w := range strings.Split(s, ",") {
		w = strings.TrimSpace(w)
		for _, fl := range table {
			if w == fl.name {
				v |= fl.mask
			}
		}
	}
	return v
}

func ParseCap(cap string) (string, uint32, error) {
	typ, perm, found := strings.Cut(cap, "=")
	if !found {
		return "", 0, ErrInvalidCap // get_cap leaves type empty and is_valid_cap_type fails it
	}
	typ = strings.TrimSpace(typ)
	if !ValidCapType(typ) {
		return "", 0, ErrInvalidCap
	}
	return typ, parseFlags(capNames, perm), nil
}

func (c Caps) AddString(s string) error {
	for _, cap := range strings.Split(s, ";") {
		if cap == "" && len(s) > 0 { // add_from_string stops at the string's end, a trailing ';' adds nothing
			continue
		}
		typ, perm, err := ParseCap(cap)
		if err != nil {
			return err
		}
		c[typ] |= perm
	}
	return nil
}

func (c Caps) RemoveString(s string) error {
	for _, cap := range strings.Split(s, ";") {
		if cap == "" && len(s) > 0 {
			continue
		}
		typ, perm, err := ParseCap(cap)
		if err != nil {
			return err
		}
		if old, ok := c[typ]; ok {
			if old &^= perm; old == 0 {
				delete(c, typ)
			} else {
				c[typ] = old
			}
		}
	}
	return nil
}

func (c Caps) Check(typ string, perm uint32) bool { return c[typ]&perm == perm }

func ParseOpTypeList(s string) uint32 { return parseFlags(opTypeAll, s) }

// opTypeAll is op_type_mapping with its "*" row (rgw_common.cc:2155-2159).
var opTypeAll = append([]flagName{{OpTypeAll, "*"}}, opTypeFlags...)

func OpTypeString(mask uint32) string { return maskString(opTypeFlags, mask) }

func ParseSubuserPerm(s string) uint32 {
	switch strings.ToLower(s) {
	case "":
		return 0
	case "read":
		return 0x1
	case "write":
		return 0x2
	case "readwrite":
		return 0x3
	case "full":
		return 0xf
	}
	return PermInvalid
}

func PermString(mask uint32) string { return permString(mask) }
```

Note `rgw_str_to_perm` uses `strcasecmp`, hence `ToLower`. `add_from_string`'s `do { ... } while (start < size)` loop with `str.find(';')` gives the same split as `strings.Split` except that an empty input string parses one empty cap and fails with ERR_INVALID_CAP — `RGWUserCapPool::add` rejects an empty caps string first ([rgw_user.cc:1279-1282](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_user.cc#L1279-L1282)), so the ops never reach it; `AddString("")` returning `ErrInvalidCap` matches radosgw. Adjust the two `continue` guards so an empty whole string still fails: `if cap == "" && strings.Contains(s, ";")`.

Run the focus again: PASS. `make lint`.

- [ ] **Step 3: Write the failing `policy.ActionNone` spec**

`internal/policy/action_test.go` (add to Z's file):

```go
It("has an ActionNone that no name maps to", func() {
	Expect(policy.ActionNone.String()).To(BeEmpty())
	_, ok := policy.ParseAction("")
	Expect(ok).To(BeFalse())
	Expect(policy.Known(policy.ActionNone, denc.Squid)).To(BeFalse())
	Expect(policy.Known(policy.ActionNone, denc.Tentacle)).To(BeFalse())
})
```

- [ ] **Step 4: Implement `ActionNone`**

In `internal/policy/action.go`: `const ActionNone Action = 0xFFFF`; in `String()`, before the table lookup, `if a == ActionNone { return "" }`; in `Known`, `if a == ActionNone { return false }` before any table index. Run the policy suite: PASS.

- [ ] **Step 5: Add the interfaces, the Env fields and regenerate the fakes**

Create `internal/op/adminstores.go` with the `UsageData`, `S3SelectUsage`, `UsageRecord`, `UsageIter`, `UsageReader`, `CategoryStats`, `BucketIndexStats`, `BucketAdminStore`, `RealmStore` and `PutAccountOptions` declarations above, each with its `//counterfeiter:generate . <Interface>` directive placed as G's `doc.go` places them (move the directives there if G keeps them centrally). Widen `AccountStore` in `internal/op/account.go` to the nine methods above (keep A's doc comment on `GetAccount`). Add `UsageReader UsageReader`, `BucketAdmin BucketAdminStore`, `Realms RealmStore` to `Env` after `Accounts`. Run `make generate` and check the four fakes appear/refresh.

`internal/op/adminstores_test.go`:

```go
var _ = Describe("admin store contract", func() {
	It("has fakes for every admin store", func() {
		var (
			_ op.UsageReader      = (*opfakes.FakeUsageReader)(nil)
			_ op.BucketAdminStore = (*opfakes.FakeBucketAdminStore)(nil)
			_ op.RealmStore       = (*opfakes.FakeRealmStore)(nil)
			_ op.AccountStore     = (*opfakes.FakeAccountStore)(nil)
		)
	})
})
```

- [ ] **Step 6: Write the failing memstore specs**

`internal/memstore/admin_test.go`:

```go
var _ = Describe("memstore admin stores", func() {
	var store *memstore.Store
	BeforeEach(func() { store = memstore.New(memstore.Config{}) })

	It("implements every admin store interface", func() {
		var (
			_ op.UsageReader      = store
			_ op.BucketAdminStore = store
			_ op.RealmStore       = store
			_ op.AccountStore     = store
		)
	})
	It("indexes accounts by name and email, case-insensitively for email", func(ctx SpecContext) {
		rec := store.AddAccount(meta.AccountInfo{ID: "RGW00000000000000001", Tenant: "t", Name: "acme", Email: "Ops@Acme.example"})
		byName, err := store.GetAccountByName(ctx, "t", "acme")
		Expect(err).NotTo(HaveOccurred())
		Expect(byName.Info.ID).To(Equal(rec.Info.ID))
		byEmail, err := store.GetAccountByEmail(ctx, "ops@acme.EXAMPLE")
		Expect(err).NotTo(HaveOccurred())
		Expect(byEmail.Info.ID).To(Equal(rec.Info.ID))
		_, err = store.GetAccountByName(ctx, "other", "acme")
		Expect(err).To(MatchError(op.ErrNoSuchEntity))
		name, err := store.AccountName(ctx, rec.Info.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(name).To(Equal("acme"))
	})
	It("refuses a second account with the same name or email", func(ctx SpecContext) {
		store.AddAccount(meta.AccountInfo{ID: "RGW00000000000000001", Name: "acme", Email: "a@b"})
		dup := &op.AccountRecord{Info: meta.AccountInfo{ID: "RGW00000000000000002", Name: "acme"}}
		Expect(store.PutAccount(ctx, dup, nil, op.PutAccountOptions{Exclusive: true})).To(MatchError(op.ErrAccountAlreadyExists))
		dup.Info.Name = "other"
		dup.Info.Email = "A@B"
		Expect(store.PutAccount(ctx, dup, nil, op.PutAccountOptions{Exclusive: true})).To(MatchError(op.ErrAccountAlreadyExists))
	})
	It("moves the name index on rename and drops it on remove", func(ctx SpecContext) {
		rec := store.AddAccount(meta.AccountInfo{ID: "RGW00000000000000001", Name: "acme"})
		old := rec.Info
		rec.Info.Name = "acme2"
		Expect(store.PutAccount(ctx, rec, &old, op.PutAccountOptions{})).To(Succeed())
		_, err := store.GetAccountByName(ctx, "", "acme")
		Expect(err).To(MatchError(op.ErrNoSuchEntity))
		Expect(store.RemoveAccount(ctx, rec)).To(Succeed())
		_, err = store.GetAccountByName(ctx, "", "acme2")
		Expect(err).To(MatchError(op.ErrNoSuchEntity))
		_, err = store.GetAccount(ctx, rec.Info.ID)
		Expect(err).To(MatchError(op.ErrNoSuchEntity))
	})
	It("keeps the account users index keyed by display name", func(ctx SpecContext) {
		store.AddAccount(meta.AccountInfo{ID: "RGW00000000000000001", Name: "acme"})
		Expect(store.AddAccountUser(ctx, "RGW00000000000000001", meta.UserInfo{UserID: meta.UserID{ID: "u1"}, DisplayName: "Alice"})).To(Succeed())
		Expect(store.AddAccountUser(ctx, "RGW00000000000000001", meta.UserInfo{UserID: meta.UserID{ID: "u2"}, DisplayName: "Bob"})).To(Succeed())
		ids, next, err := store.ListAccountUsers(ctx, "RGW00000000000000001", "", 1)
		Expect(err).NotTo(HaveOccurred())
		Expect(ids).To(Equal([]string{"u1"}))
		Expect(next).NotTo(BeEmpty())
		ids, next, err = store.ListAccountUsers(ctx, "RGW00000000000000001", next, 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(ids).To(Equal([]string{"u2"}))
		Expect(next).To(BeEmpty())
		Expect(store.RemoveAccountUser(ctx, "RGW00000000000000001", "Alice")).To(Succeed())
		ids, _, _ = store.ListAccountUsers(ctx, "RGW00000000000000001", "", 10)
		Expect(ids).To(Equal([]string{"u2"}))
	})
	It("records usage for the specs and reads it back", func(ctx SpecContext) {
		store.AddUsage(op.UsageRecord{User: "alice", Owner: "alice", Bucket: "plain", Epoch: 3600, Categories: map[string]op.UsageData{"put_obj": {Ops: 1, SuccessfulOps: 1}}})
		var it op.UsageIter
		recs, truncated, err := store.ReadUsage(ctx, "alice", "", 0, ^uint64(0), 1000, &it)
		Expect(err).NotTo(HaveOccurred())
		Expect(truncated).To(BeFalse())
		Expect(recs).To(HaveLen(1))
		Expect(recs[0].Bucket).To(Equal("plain"))
	})
})
```

- [ ] **Step 7: Implement `internal/memstore/admin.go`**

Add to `Store`: `accountsByName map[string]string` (key `tenant + "$" + name`), `accountsByEmail map[string]string` (lower-cased), `accountUsers map[string]map[string]string` (account → display name → user id), `usage []op.UsageRecord`, `periodConfigs map[string]meta.PeriodConfig`, `realms map[string]meta.Realm`, `periods map[string]meta.Period` (key `id + "." + epoch`), all initialised in `New` and, for realms/periods, seeded from `Config.Realm`/`Config.Period`. Implement:

- `GetAccountByName`/`GetAccountByEmail`: index lookup then `GetAccount`; missing → `op.ErrNoSuchEntity`.
- `PutAccount`: under the mutex, if another id holds `rec.Info.Name` (same tenant) or `rec.Info.Email` (case-insensitive) → `op.ErrAccountAlreadyExists`; `Exclusive` and the id exists → the same error; a non-exclusive put whose `rec.Version` differs from the stored version → `op.ErrConcurrentModification`; store a copy with a fresh `Version` (G's helper), drop `old`'s name/email index entries that point at this id, add the new ones; update `rec.Version`/`rec.Mtime`.
- `RemoveAccount`: delete the record, its indexes and its users index; missing → `op.ErrNoSuchEntity`.
- `AddAccountUser`/`RemoveAccountUser`/`ListAccountUsers`: the map; listing sorted by display name, `marker` = the display name to start after, `next` = the last returned display name when more remain.
- `AccountName`: `GetAccount` then `.Info.Name`.
- `AddUsage`/`ReadUsage`: filter `usage` by user (`User` when non-empty), bucket, `[start, end)` epoch; sort by (User, Bucket, Epoch); `it.Index` is the offset; return up to `max`, `truncated` when more remain. `TrimUsage`: delete matching records. `Log` (M's) stays as is; `AddUsage` is the spec seed.
- `IndexStats`, `ChangeBucketOwner`, `UnlinkBucketOwner`, `ChownBucket`, `SyncOwnerStats`, `CheckIndex`, `RebuildIndex`, `RemoveIndexEntries`, `PurgeBypassGC`: return `op.ErrNotImplemented` here; Tasks 6 and 11 replace them (Task 6 gives `PurgeBypassGC` its memstore behaviour: every object record, every version, and every upload of the bucket dropped, the memstore keeping no GC).
- `GetRealm`/`ListRealms`/`GetPeriod`/`GetPeriodConfig`/`PutPeriodConfig`: over the seeded maps; `GetRealm` resolves id, else name, else the single seeded realm as default; missing → `op.ErrNoSuchKey`; `GetPeriodConfig` missing → `op.ErrNotFound`.

Run the memstore suite: PASS.

- [ ] **Step 8: Wire the driver stubs**

In `internal/driver/store.go`: `var ( _ op.UsageReader = (*Store)(nil); _ op.BucketAdminStore = (*Store)(nil); _ op.RealmStore = (*Store)(nil) )`; every new method returns `op.ErrNotImplemented` (the `AccountStore` additions too; A's `GetAccount` stub stays; `PurgeBypassGC`'s stub stays until Task 14); `Env()` sets `UsageReader: s`, `BucketAdmin: s`, `Realms: s`. Build: `GOTOOLCHAIN=local go build -tags ceph_preview ./...`.

- [ ] **Step 9: `make check`; commit**

```bash
git add internal/op internal/meta/caps.go internal/meta/caps_test.go internal/memstore internal/policy internal/driver/store.go
git commit -m "feat(op): add the admin store interfaces, cap parsers and ActionNone"
```

The commit body names the additive contract changes (Global Constraints) and that every driver method answers NotImplemented until its task.

## Task 2: Seams: `Cluster.FSID`, `Pool.ListObjectsFrom` with the rjenkins token; fakerados

**Files:**
- Create: `internal/radosclient/listtoken.go`, `internal/radosclient/listtoken_test.go` (the hash and the token, cgo-free so fakerados shares them), `internal/radosclient/goceph/listfrom.go`
- Modify: `internal/radosclient/cluster.go` (`Cluster.FSID`, `Pool.ListObjectsFrom`), `internal/radosclient/goceph/cluster.go` (`FSID`), `internal/radosclient/goceph/goceph_integration_test.go` (resume round trip, `//go:build integration`), `internal/testutil/fakerados/cluster.go` and `pool.go` (`FSID`, `ListObjectsFrom`), `internal/testutil/fakerados/*_test.go`, `docs/cgo-limitations.md` is NOT touched (no cgo limitation found: both bindings are upstream)

**Interfaces:**
- Consumes: go-ceph `rados.Conn.GetFSID` ([rados/conn.go:259](https://github.com/jhoblitt/go-ceph/blob/cbf97f85fcf5000ca4beca4a7fbf2ee746fa965d/rados/conn.go#L259)), `rados.Iter.Seek/Token/Next/Value/Locator/Err/Close` (rados/object_iter.go, object_iter_locator.go on the fork); goceph's `pool.state.acquire`/`release`, `toSeamError`; M's `fakerados` pool object map.
- Produces:

```go
package radosclient

// Cluster gains:
//	// FSID is the cluster fsid in its canonical string form (rados_cluster_fsid).
//	FSID() (string, error)

// Pool gains:
//	// ListObjectsFrom lists the namespace's objects after token ("" starts at
//	// the beginning), at most max of them when max > 0, calling fn for each in
//	// the pool's listing order. next continues the listing and more says whether
//	// anything follows; both are zero at the end. A token this package did not
//	// produce is ErrBadOp. Tokens are opaque to callers and round-trip only
//	// through rgw-go.
//	ListObjectsFrom(ctx context.Context, token string, max int, fn func(oid, locator string) error) (next string, more bool, err error)

// RJenkins is ceph_str_hash_rjenkins (src/common/ceph_hash.cc:22-78).
func RJenkins(b []byte) uint32
// PlacementHash is pg_pool_t::hash_key for the default object_hash
// (osd_types.cc:1785-1796): rjenkins of ns + "\x1f" + oid, or of oid alone
// when ns is empty.
func PlacementHash(ns, oid string) uint32
// EncodeListToken and DecodeListToken are the token form: base64url of
// "1:" + the last object listed.
func EncodeListToken(lastOID string) string
func DecodeListToken(token string) (lastOID string, err error) // ErrBadOp-wrapping
// ListAfter reports whether an entry with hash h and name oid comes after the
// resume point (lastHash, lastOID) in hobject order: a different hash, or the
// same hash and a greater name.
func ListAfter(h uint32, oid string, lastHash uint32, lastOID string) bool
```

```go
package goceph

func (c *cluster) FSID() (string, error)
func (p *pool) ListObjectsFrom(ctx context.Context, token string, max int, fn func(oid, locator string) error) (string, bool, error)
```

```go
package fakerados

// The fake cluster answers FSID with FakeFSID; the fake pool lists in
// (PlacementHash, oid) order and honours the same tokens.
const FakeFSID = "00000000-0000-4000-8000-00000000f51d"
```

Why not go-ceph's `Iter.Token()`: it is `rados_nobjects_list_get_pg_hash_position`, the hash of `NListContext::pos`, which librados advances to the NEXT batch's start when it fetches a batch ([Objecter.h:2247-2249](https://github.com/ceph/ceph/blob/v19.2.6/src/osdc/Objecter.h#L2247-L2249), [IoCtxImpl.cc:518-536](https://github.com/ceph/ceph/blob/v19.2.6/src/librados/IoCtxImpl.cc#L518-L536)); taken after `Next()` it points past the entries of the current batch not yet returned, and `Seek` to it skips them. Seeking to the placement hash of the last object delivered is exact: `Objecter::list_nobjects_seek` builds `hobject_t(hash=pos, name="")` ([Objecter.cc:3761-3773](https://github.com/ceph/ceph/blob/v19.2.6/src/osdc/Objecter.cc#L3761-L3773)), the OSD lists from that position in hobject order — reversed hash, namespace, name ([hobject.cc:335-370](https://github.com/ceph/ceph/blob/v19.2.6/src/common/hobject.cc#L335-L370)) — so the first entries back share the hash and are skipped by name comparison.

- [ ] **Step 1: Write the failing specs (the rjenkins vectors below were generated from Ceph's source)**

The vectors were produced by compiling [`src/common/ceph_hash.cc:22-78`](https://github.com/ceph/ceph/blob/v19.2.6/src/common/ceph_hash.cc#L22-L78) (gcc 15.3.1, `typedef unsigned int __u32;` prepended, the include dropped) with this driver; rerun it to add a vector:

```sh
S=$(mktemp -d)
cat > "$S/vec.c" <<'EOF'
#include <stdio.h>
#include <string.h>
typedef unsigned int __u32;
#include "/home/jhoblitt/github/ceph/src/common/ceph_hash.cc"   /* rjenkins only needs __u32 */
int main(void) {
  const char *in[] = {"", "a", "plain", "alice", "tenant$bob", ".bucket.meta.plain:zone.4155.1", "0123456789ab", "0123456789abcdefghijklmnop", "ns\037plain"};
  for (unsigned i = 0; i < sizeof in / sizeof *in; i++)
    printf("%-40s %u\n", in[i], ceph_str_hash_rjenkins(in[i], strlen(in[i])));
  return 0;
}
EOF
gcc -x c -w -o "$S/vec" "$S/vec.c" && "$S/vec"
```

`internal/radosclient/listtoken_test.go` (the `ns\x1fplain` vector is 3667301547, `users.uid` 3965031260, `admin` 2364429183 for extra entries):

```go
var _ = Describe("list tokens", func() {
	DescribeTable("RJenkins matches ceph_str_hash_rjenkins",
		func(in string, want uint32) { Expect(radosclient.RJenkins([]byte(in))).To(Equal(want)) },
		Entry("empty", "", uint32(3175731469)),
		Entry("a", "a", uint32(703514648)),
		Entry("plain", "plain", uint32(2467236041)),
		Entry("alice", "alice", uint32(1882812382)),
		Entry("tenant$bob", "tenant$bob", uint32(3423182699)),
		Entry("instance oid", ".bucket.meta.plain:zone.4155.1", uint32(3442924146)),
		Entry("12 bytes", "0123456789ab", uint32(2465405648)),
		Entry("26 bytes", "0123456789abcdefghijklmnop", uint32(3493940311)),
	)
	It("hashes a namespaced object as pg_pool_t::hash_key does", func() {
		Expect(radosclient.PlacementHash("ns", "plain")).To(Equal(radosclient.RJenkins([]byte("ns\x1fplain"))))
		Expect(radosclient.PlacementHash("", "plain")).To(Equal(radosclient.RJenkins([]byte("plain"))))
	})
	It("round-trips tokens and rejects foreign ones", func() {
		tok := radosclient.EncodeListToken("tenant/plain")
		Expect(tok).NotTo(ContainSubstring("/"))
		got, err := radosclient.DecodeListToken(tok)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal("tenant/plain"))
		_, err = radosclient.DecodeListToken("3:b55a9110:root::bu_9:head")
		Expect(err).To(MatchError(radosclient.ErrBadOp))
		_, err = radosclient.DecodeListToken(base64.RawURLEncoding.EncodeToString([]byte("2:x")))
		Expect(err).To(MatchError(radosclient.ErrBadOp))
	})
	It("orders entries as hobject_t does within and across hashes", func() {
		Expect(radosclient.ListAfter(5, "b", 5, "a")).To(BeTrue())
		Expect(radosclient.ListAfter(5, "a", 5, "a")).To(BeFalse())
		Expect(radosclient.ListAfter(5, "0", 5, "a")).To(BeFalse())
		Expect(radosclient.ListAfter(6, "0", 5, "a")).To(BeTrue(), "another hash is never skipped")
	})
})
```

- [ ] **Step 2: Implement `internal/radosclient/listtoken.go`**

```go
package radosclient

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"strings"
)

// RJenkins is ceph_str_hash_rjenkins: Bob Jenkins' lookup2 with the golden
// ratio in a and b, zero in c, and the length folded into c (ceph_hash.cc:22-78).
func RJenkins(k []byte) uint32 {
	a, b, c := uint32(0x9e3779b9), uint32(0x9e3779b9), uint32(0)
	length := uint32(len(k))
	for len(k) >= 12 {
		a += binary.LittleEndian.Uint32(k[0:4])
		b += binary.LittleEndian.Uint32(k[4:8])
		c += binary.LittleEndian.Uint32(k[8:12])
		a, b, c = mix(a, b, c)
		k = k[12:]
	}
	c += length
	switch len(k) { // all cases fall through, as the C does
	case 11:
		c += uint32(k[10]) << 24
		fallthrough
	case 10:
		c += uint32(k[9]) << 16
		fallthrough
	case 9:
		c += uint32(k[8]) << 8
		fallthrough
	case 8:
		b += uint32(k[7]) << 24
		fallthrough
	case 7:
		b += uint32(k[6]) << 16
		fallthrough
	case 6:
		b += uint32(k[5]) << 8
		fallthrough
	case 5:
		b += uint32(k[4])
		fallthrough
	case 4:
		a += uint32(k[3]) << 24
		fallthrough
	case 3:
		a += uint32(k[2]) << 16
		fallthrough
	case 2:
		a += uint32(k[1]) << 8
		fallthrough
	case 1:
		a += uint32(k[0])
	}
	_, _, c = mix(a, b, c)
	return c
}

// mix is the lookup2 mix macro.
func mix(a, b, c uint32) (uint32, uint32, uint32) {
	a -= b; a -= c; a ^= c >> 13
	b -= c; b -= a; b ^= a << 8
	c -= a; c -= b; c ^= b >> 13
	a -= b; a -= c; a ^= c >> 12
	b -= c; b -= a; b ^= a << 16
	c -= a; c -= b; c ^= b >> 5
	a -= b; a -= c; a ^= c >> 3
	b -= c; b -= a; b ^= a << 10
	c -= a; c -= b; c ^= b >> 15
	return a, b, c
}

func PlacementHash(ns, oid string) uint32 {
	if ns == "" {
		return RJenkins([]byte(oid))
	}
	return RJenkins([]byte(ns + "\x1f" + oid))
}

const listTokenPrefix = "1:"

func EncodeListToken(lastOID string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(listTokenPrefix + lastOID))
}

func DecodeListToken(token string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || !strings.HasPrefix(string(raw), listTokenPrefix) {
		return "", fmt.Errorf("radosclient: list token %q: %w", token, ErrBadOp)
	}
	return string(raw[len(listTokenPrefix):]), nil
}

func ListAfter(h uint32, oid string, lastHash uint32, lastOID string) bool {
	return h != lastHash || oid > lastOID
}
```

(gofumpt wants one statement per line in `mix`; write it that way.) Run the radosclient suite: PASS against the generated vectors.

- [ ] **Step 3: Add the seam methods and the goceph implementation**

`internal/radosclient/cluster.go`: add `FSID() (string, error)` to `Cluster` and `ListObjectsFrom(...)` to `Pool` with the comments above. `internal/radosclient/goceph/cluster.go`:

```go
// FSID implements radosclient.Cluster.
func (c *cluster) FSID() (string, error) {
	if err := c.begin("fsid"); err != nil {
		return "", err
	}
	defer c.end()
	fsid, err := c.conn.GetFSID()
	return fsid, toSeamError("fsid", err)
}
```

`internal/radosclient/goceph/listfrom.go`:

```go
// ListObjectsFrom implements radosclient.Pool: a token names the last
// object delivered; the listing seeks to that object's placement hash and
// skips what hobject order puts at or before it.
func (p *pool) ListObjectsFrom(ctx context.Context, token string, max int, fn func(oid, locator string) error) (string, bool, error) {
	h, err := p.state.acquire("list objects", "")
	if err != nil {
		return "", false, err
	}
	defer p.state.release(h)
	iter, err := h.ioctx.Iter()
	if err != nil {
		return "", false, toSeamError("list objects", err)
	}
	defer iter.Close()

	var lastOID string
	var lastHash uint32
	resuming := token != ""
	if resuming {
		if lastOID, err = radosclient.DecodeListToken(token); err != nil {
			return "", false, err
		}
		lastHash = radosclient.PlacementHash(p.state.namespace, lastOID)
		iter.Seek(rados.IterToken(lastHash))
	}
	n := 0
	var delivered string
	for iter.Next() {
		if err := ctx.Err(); err != nil {
			return "", false, err
		}
		oid := iter.Value()
		if resuming {
			if !radosclient.ListAfter(radosclient.PlacementHash(p.state.namespace, oid), oid, lastHash, lastOID) {
				continue
			}
			resuming = false
		}
		if max > 0 && n == max {
			return radosclient.EncodeListToken(delivered), true, nil
		}
		if err := fn(oid, iter.Locator()); err != nil {
			return "", false, err
		}
		delivered = oid
		n++
	}
	return "", false, toSeamError("list objects", iter.Err())
}
```

Build with the cgo flags from the repository CLAUDE.md.

- [ ] **Step 4: Write the failing fakerados specs**

In the fakerados suite:

```go
It("answers a fixed fsid", func() {
	fsid, err := cluster.FSID()
	Expect(err).NotTo(HaveOccurred())
	Expect(fsid).To(Equal(fakerados.FakeFSID))
})
It("pages a namespace with opaque tokens in placement-hash order", func(ctx SpecContext) {
	pool := openPool(ctx, cluster, "rgw.meta", "users.uid")
	for _, oid := range []string{"alice", "bob", "carol", "dave", "erin", "alice.buckets"} {
		writeObject(ctx, pool, oid, []byte("x"))
	}
	var all []string
	Expect(pool.ListObjects(ctx, func(oid, _ string) error { all = append(all, oid); return nil })).To(Succeed())
	Expect(all).To(HaveLen(6))
	var paged []string
	token := ""
	for {
		next, more, err := pool.ListObjectsFrom(ctx, token, 2, func(oid, _ string) error { paged = append(paged, oid); return nil })
		Expect(err).NotTo(HaveOccurred())
		if !more {
			Expect(next).To(BeEmpty())
			break
		}
		token = next
	}
	Expect(paged).To(Equal(all), "paging delivers the full listing in listing order")
})
It("resumes after a deleted last object at the next entry", func(ctx SpecContext) {
	pool := openPool(ctx, cluster, "rgw.meta", "users.uid")
	for _, oid := range []string{"alice", "bob", "carol"} {
		writeObject(ctx, pool, oid, []byte("x"))
	}
	var first []string
	next, more, err := pool.ListObjectsFrom(ctx, "", 1, func(oid, _ string) error { first = append(first, oid); return nil })
	Expect(err).NotTo(HaveOccurred())
	Expect(more).To(BeTrue())
	removeObject(ctx, pool, first[0])
	var rest []string
	_, _, err = pool.ListObjectsFrom(ctx, next, 0, func(oid, _ string) error { rest = append(rest, oid); return nil })
	Expect(err).NotTo(HaveOccurred())
	Expect(rest).To(HaveLen(2))
	Expect(rest).NotTo(ContainElement(first[0]))
})
It("rejects a token it did not make", func(ctx SpecContext) {
	pool := openPool(ctx, cluster, "rgw.meta", "users.uid")
	_, _, err := pool.ListObjectsFrom(ctx, "3:b55a9110:root::bu_9:head", 10, func(string, string) error { return nil })
	Expect(err).To(MatchError(radosclient.ErrBadOp))
})
```

(`openPool`, `writeObject`, `removeObject` are the suite's existing helpers from M's fakerados specs; use their real names.)

- [ ] **Step 5: Implement the fakerados side**

`fakerados.Cluster.FSID()` returns `FakeFSID`. The fake pool's `ListObjects` already walks its object map; give both listings one order: sort the oids by `(radosclient.PlacementHash(ns, oid), oid)` and make `ListObjects` use it too. `ListObjectsFrom` decodes the token, computes `lastHash`, and delivers the sorted entries for which `radosclient.ListAfter(...)` holds, with the same `max`/`more`/`next` rules as goceph. Run the fakerados suite: PASS.

- [ ] **Step 6: Integration spec `[cluster]` (runs in Task 13)**

Add to `internal/radosclient/goceph/goceph_integration_test.go`: against the rooket cluster's `users.uid` pool of the populated zone (names from `cephtest.ReadManifest`), list everything with `ListObjects`, then page with `ListObjectsFrom` at `max` 1, 2 and 7 and require the concatenation to equal the full listing each time; then write 50 objects `nlist-%02d` into a scratch namespace of the same pool, page by 3, delete the last delivered object of page one, resume from its token and require the remaining sequence. `FSID()` must equal `ceph fsid` (`rooket k exec ... ceph fsid`, read through `cephtest`'s helper) — this is also what go-ceph's `TestGetInfo` checks against `/admin/info`. Label `integration`.

- [ ] **Step 7: `make check`; commit**

```bash
git add internal/radosclient internal/testutil/fakerados
git commit -m "feat(radosclient): add FSID and ListObjectsFrom with placement-hash tokens"
```

## Task 3: `internal/formatter`, the zone `Dump` methods and the `internal/admin` core: mount, parse, dispatch, args, rendering, caps; `/admin/info`, `/admin/config?type=zone`; `cli serve` wiring

**Files:**
- Create: `internal/formatter/doc.go`, `internal/formatter/formatter.go`, `internal/formatter/json.go`, `internal/formatter/xml.go`, `internal/formatter/html.go`, `internal/formatter/formatter_suite_test.go`, `internal/formatter/formatter_test.go`, `internal/meta/dump.go`, `internal/meta/dump_zone.go`, `internal/meta/dump_test.go`, `internal/meta/dump_goldens_test.go`, `internal/admin/doc.go`, `internal/admin/handler.go`, `internal/admin/request.go`, `internal/admin/dispatch.go`, `internal/admin/render.go`, `internal/admin/info.go`, `internal/admin/admin_suite_test.go`, `internal/admin/handler_test.go`, `internal/admin/request_test.go`, `internal/admin/dispatch_test.go`, `internal/admin/info_test.go`, `internal/op/admininfo.go`, `internal/op/admininfo_test.go`, `internal/op/adminop.go`
- Modify: `internal/meta/time.go` (`Time.Gmtime`, which `MarshalJSON` calls; its output is unchanged), `internal/op/env.go` (`Env.ClusterID string`, additive), `internal/cli/serve.go` (mount), `internal/cli/serve_integration_test.go` (the `swift,admin` case now answers `/admin/info`), `docs/exclusions.md` (the admin headers bullet, Step 10)

**Interfaces:**
- Consumes: G's `s3.Authenticator` (`Authenticate(ctx, req, payloads op.PayloadForms)`), `op.PayloadForms` and `op.PayloadSigned`, `s3.SetCommonHeaders`, `s3.SetContentLength` (radosgw's `dump_content_length`: the length with `Accept-Ranges: bytes`), `xmltext.Escape` (ceph's XML text escaping, which `internal/formatter`'s XML and HTML writers call), `op.TransID`, `op.Request`, `op.Run`, `op.Error`/`op.AsError`, `op.Env`, `cephconf.APIs`, `frontend.Deadlines`; A's `auth.Verifier` (through `s3.Authenticator`); M's `op.LogUsage`; Task 1's `meta.Caps.Check`, `policy.ActionNone`; Task 2's `Cluster.FSID`; phase 0's `meta.ZoneParams`, `ZonePlacementInfo`, `ZoneStorageClasses`, `ZoneStorageClass`, `JSONFormattable`, `Pool`, `Time`, `Quota`, `AccessKey`, `denc.Release`, `goldentest.Load` and the meta suite's `decodeWhole`.
- Produces:

```go
package formatter

// Formatter is the part of ceph::Formatter (src/common/Formatter.h:27-132)
// that radosgw's admin handlers call. Names are written exactly as given,
// unescaped, as the C++ writes them; string values are escaped.
type Formatter interface {
	OpenObjectSection(name string)
	OpenArraySection(name string)
	CloseSection()
	DumpString(name, s string)
	// DumpStream is dump_stream(name) << s, which the C++ uses for times and
	// the index type: a string, except that XMLFormatter writes the element
	// name without get_xml_name (Formatter.cc:536-542).
	DumpStream(name, s string)
	// DumpUnquoted is dump_format_unquoted(name, "%s", s): bare in JSON.
	DumpUnquoted(name, s string)
	DumpInt(name string, v int64)
	DumpUnsigned(name string, v uint64)
	DumpBool(name string, v bool)
	DumpNull(name string)
	// SetStatus, OutputHeader and OutputFooter are the calls radosgw's REST
	// layer makes around a body (rgw_rest.cc:289-314, :571-577).
	SetStatus(code int, name string)
	OutputHeader()
	OutputFooter()
	// Fail records that a value cannot be rendered and Err returns the first
	// such failure; ceph's Formatter has no counterpart.
	Fail(err error)
	Err() error
	// Bytes is what has been written since the last Reset: what flush writes,
	// without the line break a pretty XMLFormatter adds (Formatter.cc:388-401).
	Bytes() []byte
	Reset()
}

// NewJSON is JSONFormatter(pretty) (Formatter.cc:153-375).
func NewJSON(pretty bool) Formatter
// NewXML is XMLFormatter(pretty, lowercased, underscored=true) (:377-654).
func NewXML(pretty, lowercased bool) Formatter
// NewHTML is HTMLFormatter(pretty) (HTMLFormatter.cc:34-168).
func NewHTML(pretty bool) Formatter
// EscapeJSON is json_stream_escaper (escape.cc:254-286). XML and HTML text
// goes through G's xmltext.Escape, xml_stream_escaper, the one escaper every
// XML document rgw-go writes shares.
func EscapeJSON(s string) string
```

```go
package meta

// Dumper is a value with a C++ dump(): Dump writes its fields into the
// section the caller opened, in the C++ order, as release rel writes them.
type Dumper interface {
	Dump(f formatter.Formatter, rel denc.Release)
}

// Gmtime is utime_t::gmtime (src/include/utime.h:247-277), the text
// encode_json gives a time through dump_stream (ceph_json.cc:577-590).
func (t Time) Gmtime() string

// The quota's and the zone family's dumps.
func (q Quota) Dump(f formatter.Formatter, rel denc.Release)             // RGWQuotaInfo::dump
func (z ZoneParams) Dump(f formatter.Formatter, rel denc.Release)        // RGWZoneParams::dump
func (p ZonePlacementInfo) Dump(f formatter.Formatter, rel denc.Release) // RGWZonePlacementInfo::dump
func (c ZoneStorageClasses) Dump(f formatter.Formatter, rel denc.Release)
func (s ZoneStorageClass) Dump(f formatter.Formatter, rel denc.Release)
// DumpAs is encode_json(name, JSONFormattable) (ceph_json.cc:922-946).
func (j JSONFormattable) DumpAs(f formatter.Formatter, name string)
```

```go
package op

// Env gains:
//	ClusterID string // the RADOS fsid, /admin/info's cluster_id; set at startup

// AdminOp is the part every admin op shares: RGWRESTOp (rgw_rest.h:522-537).
// Action is ActionNone, OpMask is 0 (RGWOp::op_mask default, rgw_op.h:299) and
// Init does nothing. Each op writes its own one-line VerifyPermission, which
// is RGWRESTOp::verify_permission (rgw_rest.cc:1687-1690): CheckCaps with the
// op's check_caps pair; Run's admin override then applies (rgw_process.cc:225-231).
type AdminOp struct{}
func (AdminOp) Action() policy.Action
func (AdminOp) OpMask() uint32
func (AdminOp) Init(ctx context.Context, r *Request) error
// CheckCaps is RGWUserCaps::check_cap on the identity's caps: AccessDenied when not held.
func CheckCaps(r *Request, typ string, perm uint32) error

// GetInfo is RGWOp_Info_Get (rgw_rest_info.cc:10-44): cap info=read.
type GetInfo struct {
	AdminOp
	ClusterID string // result
}
// GetZoneConfig is RGWOp_ZoneConfig_Get (rgw_rest_config.cc:35-57): cap zone=read.
type GetZoneConfig struct {
	AdminOp
	Params meta.ZoneParams // result
}
```

```go
package admin

// Config is what the handler needs of the process.
type Config struct {
	Prefix        string // rgw_admin_entry, "admin"
	TransIDSuffix string
	ServerHeader  string
	MaxConcurrent int // rgw_max_concurrent_requests; the admin handler keeps its own count
}

// Args are the query arguments in query order with RESTArgs' readers
// (rgw_rest.cc:854-1032). Values are url-decoded once. A repeated key keeps
// its LAST value: RGWHTTPArgs::append assigns val_map[name] = val
// (rgw_common.cc:918-922 at v19.2.6, :931-937 at v20.2.4), the rule auth's
// query parser follows too.
type Args struct{ /* ordered []kv, idx map[string]int */ }
func ParseArgs(rawQuery string) Args
func (a Args) Has(name string) bool
func (a Args) String(name, def string) (v string, present bool)
// Bool is RESTArgs::get_bool: absent → def; "", "true"/"TRUE"/..., "1" → true;
// "false" (any case), "0" → false; anything else → def and ErrInvalidArgument.
func (a Args) Bool(name string, def bool) (v bool, present bool, err error)
func (a Args) Int64(name string, def int64) (int64, bool, error)   // stringtoll → ErrInvalidArgument
func (a Args) Int32(name string, def int32) (int32, bool, error)
func (a Args) Uint32(name string, def uint32) (uint32, bool, error)
func (a Args) Uint64(name string, def uint64) (uint64, bool, error)
// Epoch is RESTArgs::get_epoch over utime_t::parse_date (utime.h:397-497):
// "YYYY-MM-DD", "YYYY-MM-DD HH:MM:SS", "YYYY-MM-DDTHH:MM:SS[.frac][±zzzz]" or
// "<sec>.<usec>"; anything else is ErrInvalidArgument.
func (a Args) Epoch(name string, def uint64) (uint64, bool, error)
// SubResource is the first admin sub-resource in query order (RGWHTTPArgs::append,
// rgw_common.cc:962-975): subuser, key, caps, index, policy, quota, list, object, sync; "" when none.
func (a Args) SubResource() string

// Format is RGWFormat for the admin API.
type Format uint8

const (
	FormatJSON Format = iota
	FormatXML
	FormatHTML
)

// SelectFormat is RGWHandler_REST::allocate_formatter with the admin
// handlers' default, JSON (rgw_rest.cc:1732-1764, rgw_rest_s3.cc:5133).
func SelectFormat(a Args, accept string) Format

// Request is one parsed admin request.
type Request struct {
	Resource string // first path segment after the prefix: "user", "bucket", ...
	Sub      string // second segment: the metadata section, "period" under realm
	Args     Args
	Format   Format // SelectFormat's answer; JSON for a request no handler takes
}
// ParseRequest strips prefix from the path. A path without a resource, or
// with one radosgw registers no manager for, is ErrMethodNotAllowed and its
// Format stays JSON.
func ParseRequest(path, prefix, rawQuery string) (Request, error)

// Route is one row of the dispatch table.
type Route struct {
	Name     string
	Payloads op.PayloadForms // the signed payload forms the op's type accepts
}
// Dispatch is the per-resource op_get/op_put/op_post/op_delete of
// rgw_rest_{user,bucket,metadata,usage,info,config,realm,ratelimit,account}.cc;
// a method or sub-resource radosgw has no op for is ErrMethodNotAllowed.
// Sync_Bucket (PUT bucket?sync), /admin/log and POST realm/period are
// excluded (docs/exclusions.md) and 405 too. rel gates the one row that
// differs by release: PUT account?quota, which Squid routes to Modify.
func Dispatch(method string, q Request, rel denc.Release) (Route, error)

// HandlerFunc serves one route; it returns an error only before writing.
type HandlerFunc func(ctx context.Context, w http.ResponseWriter, r *op.Request, q Request) error
type Handler struct{ /* env, auth, cfg, routes, seq, inflight */ }
func NewHandler(env *op.Env, auth s3.Authenticator, cfg Config) *Handler // registers every route; the unimplemented answer NotImplemented
func (h *Handler) Register(name string, fn HandlerFunc)
func (h *Handler) ServeHTTP(w http.ResponseWriter, req *http.Request)
// Mount routes /<prefix> and /<prefix>/... to admin and everything else to other,
// as RGWRESTMgr::get_resource_mgr does (rgw_rest.cc:1974-1997).
func Mount(prefix string, admin, other http.Handler) http.Handler

// The four ways a response leaves (render.go). Each sets the common headers,
// Content-Type and Content-Length. A dump that called Fail writes nothing
// and returns the failure.
func WriteBody(w http.ResponseWriter, r *op.Request, q Request, dump func(formatter.Formatter)) error  // a flusher-started body
func WriteDumped(w http.ResponseWriter, r *op.Request, q Request, dump func(formatter.Formatter)) error // a body dumped without dump_start
func WriteTyped(w http.ResponseWriter, r *op.Request, q Request, contentType string, dump func(formatter.Formatter)) error
func WriteEmpty(w http.ResponseWriter, r *op.Request, status int)
// WriteError is end_header's error branch in q's format.
func WriteError(ctx context.Context, w http.ResponseWriter, r *op.Request, q Request, err error)
```

The lifecycle in `ServeHTTP`, `process_request`'s order for an admin request ([rgw_process.cc:310-345](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_process.cc#L310-L345); [rgw_rest.cc:2276-2318](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L2276-L2318)): (1) `r := &op.Request{ID: op.TransID(seq, now, cfg.TransIDSuffix), Time: now, Method, Host, Path, RawQuery, Header, Body, ContentLength, RemoteAddr, TLS, Env: env}`; (2) `ParseRequest` — an error here is radosgw's missing handler, answered in JSON whatever the format, because `abort_early` allocates a JSONFormatter when no handler allocated one ([rgw_rest.cc:680-683](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L680-L683), [:2290-2305](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L2290-L2305)); (3) `q.Format = SelectFormat(q.Args, req.Header.Get("Accept"))`, the handler's `init_from_header` ([rgw_rest_s3.cc:4900-4906](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L4900-L4906)); (4) `Dispatch` — a method or sub-resource with no op is 405 in `q.Format`, radosgw's `get_op` returning null after init ([rgw_process.cc:325-328](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_process.cc#L325-L328)); (5) the in-flight cap → `op.ErrSlowDown`, which radosgw's throttle answers after `get_op` ([rgw_process.cc:330](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_process.cc#L330)); (6) `auth.Authenticate(ctx, req, route.Payloads)` → identity; `r.Tenant = r.Identity.Tenant`; a suspended user → `op.ErrUserSuspended` ([rgw_process.cc:401-404](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_process.cc#L401-L404) runs for every handler); (7) the route's handler, `WriteError(ctx, w, r, q, err)` on error; (8) `env.Metrics.Observe(route.Name, status, elapsed, in, out)` and a debug log line as G's handler does. The response writer is G's `responseWriter` pattern: it records status and bytes, forwards `Flush` and `Unwrap`, and adds no header of its own (`Accept-Ranges` comes only from `render.go`'s named-length path, as it comes only from `SetContentLength` and `WriteError` in G); it is not an `op.Sink`, as G's is not (G's `sinkOf` adapts a writer for a streaming op), and no admin op streams to one. Copy it or export it from `s3` if G left it unexported — prefer a small local copy over widening `s3`.

`Mount` — the longest-prefix rule of `RGWRESTMgr::get_resource_mgr` ([rgw_rest.cc:1974-1997](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L1974-L1997)): the path equals `/<prefix>` or starts with `/<prefix>/` → admin; the resource name comparison is exact and case-sensitive.

- [ ] **Step 1: Write the failing formatter specs**

`internal/formatter/formatter_suite_test.go` boots Ginkgo as the repository's other suites do. `internal/formatter/formatter_test.go` pins each implementation against the C++ algorithm the byte strings below were derived from ([Formatter.cc:172-281](https://github.com/ceph/ceph/blob/v19.2.6/src/common/Formatter.cc#L172-L281) for JSON's commas, names and indentation; :414-654 for XML; [HTMLFormatter.cc:69-168](https://github.com/ceph/ceph/blob/v19.2.6/src/common/HTMLFormatter.cc#L69-L168)):

```go
var _ = Describe("formatter", func() {
	// sample makes the calls dump_user_info's opening makes: an object at the
	// top, scalars of every kind, an array of objects, an empty array and a
	// time through dump_stream.
	sample := func(f formatter.Formatter) {
		f.OpenObjectSection("user_info")
		f.DumpString("tenant", "")
		f.DumpString("user_id", `a"b<c>`)
		f.DumpInt("suspended", -1)
		f.DumpUnsigned("size", 18446744073709551615)
		f.OpenArraySection("keys")
		f.OpenObjectSection("key")
		f.DumpString("access_key", "AK")
		f.DumpBool("active", true)
		f.CloseSection()
		f.CloseSection()
		f.OpenArraySection("caps")
		f.CloseSection()
		f.DumpStream("create_date", "2026-09-29T00:00:00.000000Z")
		f.CloseSection()
	}
	It("writes JSONFormatter's compact form: no name at the top level or inside an array", func() {
		f := formatter.NewJSON(false)
		sample(f)
		Expect(string(f.Bytes())).To(Equal(`{"tenant":"","user_id":"a\"b<c>","suspended":-1,"size":18446744073709551615,"keys":[{"access_key":"AK","active":true}],"caps":[],"create_date":"2026-09-29T00:00:00.000000Z"}`))
	})
	It("writes JSONFormatter's pretty form, the form ceph-dencoder prints", func() {
		f := formatter.NewJSON(true)
		sample(f)
		Expect(string(f.Bytes())).To(Equal("{\n" +
			"    \"tenant\": \"\",\n" +
			"    \"user_id\": \"a\\\"b<c>\",\n" +
			"    \"suspended\": -1,\n" +
			"    \"size\": 18446744073709551615,\n" +
			"    \"keys\": [\n" +
			"        {\n" +
			"            \"access_key\": \"AK\",\n" +
			"            \"active\": true\n" +
			"        }\n" +
			"    ],\n" +
			"    \"caps\": [],\n" +
			"    \"create_date\": \"2026-09-29T00:00:00.000000Z\"\n" +
			"}\n"))
	})
	It("writes XMLFormatter's elements, array entries keeping their names", func() {
		f := formatter.NewXML(false, false)
		sample(f)
		Expect(string(f.Bytes())).To(Equal(`<user_info><tenant></tenant><user_id>a&quot;b&lt;c&gt;</user_id><suspended>-1</suspended><size>18446744073709551615</size><keys><key><access_key>AK</access_key><active>true</active></key></keys><caps></caps><create_date>2026-09-29T00:00:00.000000Z</create_date></user_info>`))
	})
	It("indents a pretty XMLFormatter by one space per open section", func() {
		f := formatter.NewXML(true, false)
		f.OpenObjectSection("a")
		f.DumpString("b", "c")
		f.CloseSection()
		Expect(string(f.Bytes())).To(Equal("<a>\n <b>c</b>\n</a>\n"))
	})
	It("writes HTMLFormatter's scalars as list items inside XML sections", func() {
		f := formatter.NewHTML(false)
		sample(f)
		Expect(string(f.Bytes())).To(Equal(`<user_info><li>tenant: </li><li>user_id: a&quot;b&lt;c&gt;</li><li>suspended: -1</li><li>size: 18446744073709551615</li><keys><key><li>access_key: AK</li><li>active: true</li></key></keys><caps></caps><li>create_date: 2026-09-29T00:00:00.000000Z</li></user_info>`))
	})
	It("writes HTMLFormatter's status page and closes it with output_footer", func() {
		f := formatter.NewHTML(false)
		f.SetStatus(403, "Forbidden")
		f.OutputHeader()
		f.DumpString("Code", "AccessDenied")
		f.OutputFooter()
		Expect(string(f.Bytes())).To(Equal(`<html><head><title>403 Forbidden</title></head><body><h1>403 Forbidden</h1><ul><li>Code: AccessDenied</li></ul></body></html>`))
	})
	It("writes the XML declaration once until a reset", func() {
		f := formatter.NewXML(false, false)
		f.OutputHeader()
		f.OutputHeader()
		Expect(string(f.Bytes())).To(Equal(`<?xml version="1.0" encoding="UTF-8"?>`))
		f.Reset()
		f.OutputHeader()
		Expect(string(f.Bytes())).To(Equal(`<?xml version="1.0" encoding="UTF-8"?>`))
		Expect(string(formatter.NewJSON(false).Bytes())).To(BeEmpty(), "JSONFormatter has no header")
	})
	It("writes names raw, underscores XML element names and honours lowercased", func() {
		j := formatter.NewJSON(false)
		j.OpenObjectSection("tagset")
		j.DumpString(`k"1`, "v")
		j.CloseSection()
		Expect(string(j.Bytes())).To(Equal(`{"k"1":"v"}`), "print_name writes the name unescaped")
		x := formatter.NewXML(false, true)
		x.DumpString("Tag Name", "v")
		x.DumpStream("Tag Name", "v")
		Expect(string(x.Bytes())).To(Equal(`<tag_name>v</tag_name><Tag Name>v</Tag Name>`), "dump_stream skips get_xml_name")
	})
	It("writes a top-level scalar without a name and dump_null in each form", func() {
		j := formatter.NewJSON(false)
		j.DumpString("ignored", "v")
		Expect(string(j.Bytes())).To(Equal(`"v"`))
		j.Reset()
		j.OpenObjectSection("o")
		j.DumpNull("n")
		j.DumpUnquoted("u", "1.5")
		j.CloseSection()
		Expect(string(j.Bytes())).To(Equal(`{"n":null,"u":1.5}`))
		x := formatter.NewXML(false, false)
		x.DumpNull("n")
		Expect(string(x.Bytes())).To(Equal(`<n xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xsi:nil="true" />`))
	})
	It("closes every open section in output_footer", func() {
		f := formatter.NewXML(false, false)
		f.OpenObjectSection("a")
		f.OpenArraySection("b")
		f.OutputFooter()
		Expect(string(f.Bytes())).To(Equal(`<a><b></b></a>`))
	})
	It("keeps the first failure until a reset", func() {
		f := formatter.NewJSON(false)
		f.Fail(errors.New("first"))
		f.Fail(errors.New("second"))
		Expect(f.Err()).To(MatchError("first"))
		f.Reset()
		Expect(f.Err()).NotTo(HaveOccurred())
	})
	DescribeTable("escapes JSON as json_stream_escaper does",
		func(in, want string) {
			Expect(formatter.EscapeJSON(in)).To(Equal(want))
		},
		Entry("quotes", `"'`, `\"'`),
		Entry("markup", "<&>", "<&>"),
		Entry("backslash", `\`, `\\`),
		Entry("tab and newline", "\t\n", `\t\n`),
		Entry("other control bytes", "\r\x01\x7f", `\u000d\u0001\u007f`),
		Entry("UTF-8 is copied", "é", "é"),
	)
	It("writes XML and HTML text through xml_stream_escaper", func() {
		x := formatter.NewXML(false, false)
		x.DumpString("k", "\"'<&>\t\r\x01\x7f\xff")
		Expect(string(x.Bytes())).To(Equal("<k>&quot;&apos;&lt;&amp;&gt;\t&#x0d;&#x01;&#x7f;\xff</k>"))
		h := formatter.NewHTML(false)
		h.DumpString("k", "a<b")
		Expect(string(h.Bytes())).To(Equal("<li>k: a&lt;b</li>"))
	})
})
```

Run: `GOTOOLCHAIN=local go test -tags ceph_preview ./internal/formatter/` — FAIL (package missing).

- [ ] **Step 2: Implement `internal/formatter`**

`doc.go`:

```go
// Package formatter transcribes ceph's JSONFormatter, XMLFormatter and
// HTMLFormatter (src/common/Formatter.cc, HTMLFormatter.cc), the writers
// behind radosgw's admin responses, so a dump written once renders every
// format byte for byte as radosgw's does.
package formatter
```

`formatter.go`: the `Formatter` interface above and the JSON escaper; XML and HTML text goes through G's `xmltext.Escape` (G Task 4), which is `xml_stream_escaper` ([escape.cc:134-169](https://github.com/ceph/ceph/blob/v19.2.6/src/common/escape.cc#L134-L169)):

```go
package formatter

import (
	"fmt"
	"strings"
)

// EscapeJSON is json_stream_escaper (escape.cc:254-286): '"', '\\', tab and
// newline take their short escapes; other bytes below 0x20 and 0x7f become
// \u00xx; every other byte, UTF-8 included, is copied.
func EscapeJSON(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '"':
			b.WriteString(`\"`)
		case c == '\\':
			b.WriteString(`\\`)
		case c == '\t':
			b.WriteString(`\t`)
		case c == '\n':
			b.WriteString(`\n`)
		case c < 0x20 || c == 0x7f:
			fmt.Fprintf(&b, `\u%04x`, c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}
```

`json.go`:

```go
package formatter

import (
	"bytes"
	"strconv"
)

// jsonSection is json_formatter_stack_entry_d.
type jsonSection struct {
	size    int
	isArray bool
}

type jsonFormatter struct {
	pretty bool
	buf    bytes.Buffer
	stack  []jsonSection
	err    error
}

func NewJSON(pretty bool) Formatter { return &jsonFormatter{pretty: pretty} }

// indent writes four spaces for each open section after the first.
func (j *jsonFormatter) indent() {
	for i := 1; i < len(j.stack); i++ {
		j.buf.WriteString("    ")
	}
}

// printComma is print_comma (Formatter.cc:172-190).
func (j *jsonFormatter) printComma(s *jsonSection) {
	switch {
	case s.size > 0 && j.pretty:
		j.buf.WriteString(",\n")
		j.indent()
	case s.size > 0:
		j.buf.WriteByte(',')
	case j.pretty:
		j.buf.WriteByte('\n')
		j.indent()
	}
	if j.pretty && s.isArray {
		j.buf.WriteString("    ")
	}
}

// printName is print_name (:198-217): nothing at the top level, and no name
// inside an array.
func (j *jsonFormatter) printName(name string) {
	if len(j.stack) == 0 {
		return
	}
	s := &j.stack[len(j.stack)-1]
	j.printComma(s)
	if !s.isArray {
		if j.pretty {
			j.buf.WriteString("    ")
		}
		j.buf.WriteByte('"')
		j.buf.WriteString(name)
		j.buf.WriteByte('"')
		if j.pretty {
			j.buf.WriteString(": ")
		} else {
			j.buf.WriteByte(':')
		}
	}
	s.size++
}

// open is open_section (:219-240).
func (j *jsonFormatter) open(name string, isArray bool) {
	j.printName(name)
	if isArray {
		j.buf.WriteByte('[')
	} else {
		j.buf.WriteByte('{')
	}
	j.stack = append(j.stack, jsonSection{isArray: isArray})
}

func (j *jsonFormatter) OpenObjectSection(name string) { j.open(name, false) }
func (j *jsonFormatter) OpenArraySection(name string)  { j.open(name, true) }

// CloseSection is close_section (:262-281).
func (j *jsonFormatter) CloseSection() {
	s := j.stack[len(j.stack)-1]
	if j.pretty && s.size > 0 {
		j.buf.WriteByte('\n')
		j.indent()
	}
	if s.isArray {
		j.buf.WriteByte(']')
	} else {
		j.buf.WriteByte('}')
	}
	j.stack = j.stack[:len(j.stack)-1]
	if j.pretty && len(j.stack) == 0 {
		j.buf.WriteByte('\n')
	}
}

// value is add_value(name, val, quoted=false) (:301-313).
func (j *jsonFormatter) value(name, v string) {
	j.printName(name)
	j.buf.WriteString(v)
}

// quoted is add_value(name, val, quoted=true) with print_quoted_string (:192-196).
func (j *jsonFormatter) quoted(name, v string) {
	j.printName(name)
	j.buf.WriteByte('"')
	j.buf.WriteString(EscapeJSON(v))
	j.buf.WriteByte('"')
}

func (j *jsonFormatter) DumpString(name, s string)          { j.quoted(name, s) }
func (j *jsonFormatter) DumpStream(name, s string)          { j.quoted(name, s) }
func (j *jsonFormatter) DumpUnquoted(name, s string)        { j.value(name, s) }
func (j *jsonFormatter) DumpInt(name string, v int64)       { j.value(name, strconv.FormatInt(v, 10)) }
func (j *jsonFormatter) DumpUnsigned(name string, v uint64) { j.value(name, strconv.FormatUint(v, 10)) }
func (j *jsonFormatter) DumpBool(name string, v bool)       { j.value(name, strconv.FormatBool(v)) }
func (j *jsonFormatter) DumpNull(name string)               { j.value(name, "null") }
func (j *jsonFormatter) SetStatus(int, string)              {}
func (j *jsonFormatter) OutputHeader()                      {}
func (j *jsonFormatter) OutputFooter()                      {}

func (j *jsonFormatter) Fail(err error) {
	if j.err == nil {
		j.err = err
	}
}

func (j *jsonFormatter) Err() error    { return j.err }
func (j *jsonFormatter) Bytes() []byte { return j.buf.Bytes() }

func (j *jsonFormatter) Reset() {
	j.buf.Reset()
	j.stack = j.stack[:0]
	j.err = nil
}
```

`xml.go`:

```go
package formatter

import (
	"bytes"
	"strconv"
	"strings"

	"github.com/jhoblitt/rgw-go/internal/xmltext"
)

// xmlDeclaration is XMLFormatter::XML_1_DTD (Formatter.cc:377-378).
const xmlDeclaration = `<?xml version="1.0" encoding="UTF-8"?>`

type xmlFormatter struct {
	pretty, lowercased bool
	buf                bytes.Buffer
	sections           []string
	headerDone         bool
	err                error
}

// NewXML builds the formatter radosgw builds: underscored is always true
// (rgw_rest.cc:1796).
func NewXML(pretty, lowercased bool) Formatter {
	return &xmlFormatter{pretty: pretty, lowercased: lowercased}
}

// elementName is get_xml_name (:461-467, :646-654): a space becomes '_', and
// with lowercased every A-Z byte is lowered, as tolower in the C locale.
func (x *xmlFormatter) elementName(name string) string {
	b := []byte(name)
	for i, c := range b {
		switch {
		case c == ' ':
			b[i] = '_'
		case x.lowercased && c >= 'A' && c <= 'Z':
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}

// printSpaces is print_spaces (:637-644).
func (x *xmlFormatter) printSpaces() {
	if x.pretty {
		x.buf.WriteString(strings.Repeat(" ", len(x.sections)))
	}
}

func (x *xmlFormatter) newline() {
	if x.pretty {
		x.buf.WriteByte('\n')
	}
}

// open is open_section_in_ns (:603-622); arrays and objects are alike.
func (x *xmlFormatter) open(name string) {
	x.printSpaces()
	x.buf.WriteString("<" + x.elementName(name) + ">")
	x.newline()
	x.sections = append(x.sections, name)
}

func (x *xmlFormatter) OpenObjectSection(name string) { x.open(name) }
func (x *xmlFormatter) OpenArraySection(name string)  { x.open(name) }

// CloseSection is close_section (:469-480).
func (x *xmlFormatter) CloseSection() {
	name := x.elementName(x.sections[len(x.sections)-1])
	x.sections = x.sections[:len(x.sections)-1]
	x.printSpaces()
	x.buf.WriteString("</" + name + ">")
	x.newline()
}

// element is the shape of every XMLFormatter scalar (:482-523, :544-571).
func (x *xmlFormatter) element(name, text string) {
	e := x.elementName(name)
	x.printSpaces()
	x.buf.WriteString("<" + e + ">" + text + "</" + e + ">")
	x.newline()
}

func (x *xmlFormatter) DumpString(name, s string)    { x.element(name, xmltext.Escape(s)) }
func (x *xmlFormatter) DumpUnquoted(name, s string)  { x.element(name, xmltext.Escape(s)) }
func (x *xmlFormatter) DumpInt(name string, v int64) { x.element(name, strconv.FormatInt(v, 10)) }
func (x *xmlFormatter) DumpUnsigned(name string, v uint64) {
	x.element(name, strconv.FormatUint(v, 10))
}
func (x *xmlFormatter) DumpBool(name string, v bool) { x.element(name, strconv.FormatBool(v)) }

// DumpStream is dump_stream and finish_pending_string (:536-542, :624-635):
// the element name is written as given.
func (x *xmlFormatter) DumpStream(name, s string) {
	x.printSpaces()
	x.buf.WriteString("<" + name + ">" + xmltext.Escape(s) + "</" + name + ">")
	x.newline()
}

// DumpNull is dump_null (:493-499).
func (x *xmlFormatter) DumpNull(name string) {
	x.printSpaces()
	x.buf.WriteString("<" + x.elementName(name) + ` xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xsi:nil="true" />`)
	x.newline()
}

func (x *xmlFormatter) SetStatus(int, string) {}

// OutputHeader is output_header (:414-422): the declaration, once.
func (x *xmlFormatter) OutputHeader() {
	if x.headerDone {
		return
	}
	x.headerDone = true
	x.buf.WriteString(xmlDeclaration)
	x.newline()
}

// OutputFooter is output_footer (:424-429): every open section closed.
func (x *xmlFormatter) OutputFooter() {
	for len(x.sections) > 0 {
		x.CloseSection()
	}
}

func (x *xmlFormatter) Fail(err error) {
	if x.err == nil {
		x.err = err
	}
}

func (x *xmlFormatter) Err() error    { return x.err }
func (x *xmlFormatter) Bytes() []byte { return x.buf.Bytes() }

// Reset is reset (:403-412), which also clears the header flag.
func (x *xmlFormatter) Reset() {
	x.buf.Reset()
	x.sections = x.sections[:0]
	x.headerDone = false
	x.err = nil
}
```

`html.go`:

```go
package formatter

import (
	"strconv"

	"github.com/jhoblitt/rgw-go/internal/xmltext"
)

// htmlFormatter is HTMLFormatter: XMLFormatter's sections and dump_null, with
// every other scalar a list item and a status page as its header.
type htmlFormatter struct {
	xmlFormatter
	status     int
	statusName string
}

func NewHTML(pretty bool) Formatter {
	return &htmlFormatter{xmlFormatter: xmlFormatter{pretty: pretty}}
}

// item is dump_template and dump_format_va (HTMLFormatter.cc:93-100,
// :141-168): the name as given, the value escaped when it is a string.
func (h *htmlFormatter) item(name, text string) {
	h.printSpaces()
	h.buf.WriteString("<li>" + name + ": " + text + "</li>")
	h.newline()
}

func (h *htmlFormatter) DumpString(name, s string)          { h.item(name, xmltext.Escape(s)) }
func (h *htmlFormatter) DumpStream(name, s string)          { h.item(name, xmltext.Escape(s)) }
func (h *htmlFormatter) DumpUnquoted(name, s string)        { h.item(name, xmltext.Escape(s)) }
func (h *htmlFormatter) DumpInt(name string, v int64)       { h.item(name, strconv.FormatInt(v, 10)) }
func (h *htmlFormatter) DumpUnsigned(name string, v uint64) { h.item(name, strconv.FormatUint(v, 10)) }
func (h *htmlFormatter) DumpBool(name string, v bool)       { h.item(name, strconv.FormatBool(v)) }

// SetStatus is set_status (:58-67): a nil name keeps the previous one.
func (h *htmlFormatter) SetStatus(code int, name string) {
	h.status = code
	if name != "" {
		h.statusName = name
	}
}

// OutputHeader is output_header (:69-91): html, head, body, h1 and an open ul.
func (h *htmlFormatter) OutputHeader() {
	if h.headerDone {
		return
	}
	h.headerDone = true
	line := strconv.Itoa(h.status)
	if h.statusName != "" {
		line += " " + h.statusName
	}
	h.OpenObjectSection("html")
	h.printSpaces()
	h.buf.WriteString("<head><title>" + line + "</title></head>")
	h.newline()
	h.OpenObjectSection("body")
	h.printSpaces()
	h.buf.WriteString("<h1>" + line + "</h1>")
	h.newline()
	h.OpenObjectSection("ul")
}

// Reset is reset (:47-56).
func (h *htmlFormatter) Reset() {
	h.xmlFormatter.Reset()
	h.status = 0
	h.statusName = ""
}
```

Run the formatter suite: PASS. `make lint`.

- [ ] **Step 3: Write the failing `Dump` specs for the zone family and the quota**

`internal/meta/dump_goldens_test.go` is the byte-exact oracle every later task extends with its types:

```go
// dumpGolden renders v as ceph-dencoder's dump_json does
// (ceph_dencoder.cc:189-194): a pretty JSONFormatter, the value's dump()
// inside an "object" section, then a line break.
func dumpGolden(v meta.Dumper) string {
	f := formatter.NewJSON(true)
	f.OpenObjectSection("object")
	v.Dump(f, denc.Squid)
	f.CloseSection()
	Expect(f.Err()).NotTo(HaveOccurred())
	return string(f.Bytes()) + "\n"
}

// dumpGoldens compares the dump of every corpus object of typ with the
// dencoder's bytes, which carry dump()'s key order and number formatting.
func dumpGoldens[T meta.Dumper](typ string, decode func(*denc.Decoder) T) {
	GinkgoHelper()
	cs, err := goldentest.Load("testdata", typ)
	Expect(err).NotTo(HaveOccurred())
	Expect(cs).NotTo(BeEmpty(), "no goldens for %s", typ)
	for _, c := range cs {
		Expect(dumpGolden(decodeWhole(c.Bin, decode))).To(Equal(string(c.JSON)), c.Type+"/"+c.Archive+"/"+c.Name)
	}
}

var _ = Describe("Dump against the corpus", func() {
	It("RGWQuotaInfo", func() { dumpGoldens("RGWQuotaInfo", meta.DecodeQuota) })
	It("RGWZoneParams", func() { dumpGoldens("RGWZoneParams", meta.DecodeZoneParams) })
	It("RGWZonePlacementInfo", func() { dumpGoldens("RGWZonePlacementInfo", meta.DecodeZonePlacementInfo) })
	It("RGWZoneStorageClasses", func() { dumpGoldens("RGWZoneStorageClasses", meta.DecodeZoneStorageClasses) })
	It("RGWZoneStorageClass", func() { dumpGoldens("RGWZoneStorageClass", meta.DecodeZoneStorageClass) })
})
```

`internal/meta/dump_test.go` pins what the corpus cannot: the Tentacle keys and the embedded tier config.

```go
var _ = Describe("Dump", func() {
	render := func(v meta.Dumper, rel denc.Release) string {
		f := formatter.NewJSON(false)
		f.OpenObjectSection("x")
		v.Dump(f, rel)
		f.CloseSection()
		return string(f.Bytes())
	}
	It("writes the zone params keys Tentacle adds only on Tentacle", func() {
		z := meta.ZoneParams{ID: "zid", Name: "z1", DedupPool: meta.ParsePool("z1.rgw.dedup"), RestorePool: meta.ParsePool("z1.rgw.restore")}
		squid := render(z, denc.Squid)
		Expect(squid).NotTo(ContainSubstring("dedup_pool"))
		Expect(squid).NotTo(ContainSubstring("bucket_logging_pool"))
		Expect(squid).NotTo(ContainSubstring("restore_pool"))
		tentacle := render(z, denc.Tentacle)
		Expect(tentacle).To(ContainSubstring(`"control_pool":"","dedup_pool":"z1.rgw.dedup","gc_pool":""`))
		Expect(tentacle).To(ContainSubstring(`"group_pool":"","bucket_logging_pool":"","system_key"`))
		Expect(tentacle).To(HaveSuffix(`"realm_id":"","restore_pool":"z1.rgw.restore"}`))
	})
	It("writes a tier config value unquoted when it was stored unquoted, as radosgw does", func() {
		var tc meta.JSONFormattable
		tc.Type = meta.FormattableObject
		tc.Object = map[string]meta.JSONFormattable{
			"retain": {Type: meta.FormattableValue, Value: "+5"},
			"target": {Type: meta.FormattableValue, Value: "s3", Quoted: true},
		}
		z := meta.ZoneParams{TierConfig: tc}
		Expect(render(z, denc.Squid)).To(ContainSubstring(`"tier_config":{"retain":+5,"target":"s3"}`))
	})
	It("renders Time.Gmtime as utime_t::gmtime does", func() {
		Expect(meta.Time{}.Gmtime()).To(Equal("0.000000"))
		Expect(meta.Time{Time: time.Unix(1348588800, 123456000)}.Gmtime()).To(Equal("2012-09-25T16:00:00.123456Z"))
	})
})
```

Run: `GOTOOLCHAIN=local go test -tags ceph_preview ./internal/meta/ -run TestMeta -ginkgo.focus "Dump"` — FAIL (undefined).

- [ ] **Step 4: Implement `internal/meta/dump.go` and `dump_zone.go`**

`internal/meta/time.go`: move `MarshalJSON`'s string building into `Gmtime` and make `MarshalJSON` return `json.Marshal(t.Gmtime())`; the phase-0 goldens prove the output unchanged.

`internal/meta/dump.go`:

```go
package meta

import (
	"maps"
	"slices"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/formatter"
)

// Dumper is a value with a C++ dump(): Dump writes its fields into the
// section the caller opened, in the C++ order, as release rel writes them.
type Dumper interface {
	Dump(f formatter.Formatter, rel denc.Release)
}

// dumpTime is encode_json(name, utime_t or real_time) (ceph_json.cc:577-590).
func dumpTime(f formatter.Formatter, name string, t Time) { f.DumpStream(name, t.Gmtime()) }

// dumpPool is encode_json(name, rgw_pool): its string form (rgw_common.cc:2441-2444).
func dumpPool(f formatter.Formatter, name string, p Pool) { f.DumpString(name, p.String()) }

// dumpStrings is encode_json of a list, vector or set of strings: an array
// whose entries are named "obj" (ceph_json.h:534-594).
func dumpStrings(f formatter.Formatter, name string, v []string) {
	f.OpenArraySection(name)
	for _, s := range v {
		f.DumpString("obj", s)
	}
	f.CloseSection()
}

// dumpMap is encode_json of a std::map: an array of "entry" sections, each
// with a "key" and a "val" (ceph_json.h:597-646), in key order.
func dumpMap[K cmp.Ordered, V any](f formatter.Formatter, name string, m map[K]V, key func(formatter.Formatter, K), val func(formatter.Formatter, V)) {
	f.OpenArraySection(name)
	for _, k := range slices.Sorted(maps.Keys(m)) {
		f.OpenObjectSection("entry")
		key(f, k)
		val(f, m[k])
		f.CloseSection()
	}
	f.CloseSection()
}

// dumpSection is encode_json(name, T) for a struct: its dump() inside an
// object section (ceph_json.h:497-502).
func dumpSection(f formatter.Formatter, name string, v Dumper, rel denc.Release) {
	f.OpenObjectSection(name)
	v.Dump(f, rel)
	f.CloseSection()
}

// Dump is RGWQuotaInfo::dump (rgw_quota.cc:1034-1042; v20.2.4 :1032-1040).
func (q Quota) Dump(f formatter.Formatter, _ denc.Release) {
	f.DumpBool("enabled", q.Enabled)
	f.DumpBool("check_on_raw", q.CheckOnRaw)
	f.DumpInt("max_size", q.MaxSize)
	f.DumpInt("max_size_kb", roundedKB(q.MaxSize))
	f.DumpInt("max_objects", q.MaxObjects)
}
```

(`cmp` is imported with `maps` and `slices`.)

`internal/meta/dump_zone.go`:

```go
package meta

import (
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/formatter"
)

// Dump is RGWZoneParams::dump (rgw_zone.cc:309-334; v20.2.4 :312-340, which
// adds dedup_pool, bucket_logging_pool and restore_pool).
func (z ZoneParams) Dump(f formatter.Formatter, rel denc.Release) {
	f.DumpString("id", z.ID) // RGWSystemMetaObj::dump, rgw_zone.cc:812-816
	f.DumpString("name", z.Name)
	dumpPool(f, "domain_root", z.DomainRoot)
	dumpPool(f, "control_pool", z.ControlPool)
	if rel >= denc.Tentacle {
		dumpPool(f, "dedup_pool", z.DedupPool)
	}
	for _, p := range []struct {
		name string
		pool Pool
	}{
		{"gc_pool", z.GCPool}, {"lc_pool", z.LCPool}, {"log_pool", z.LogPool},
		{"intent_log_pool", z.IntentLogPool}, {"usage_log_pool", z.UsageLogPool},
		{"roles_pool", z.RolesPool}, {"reshard_pool", z.ReshardPool},
		{"user_keys_pool", z.UserKeysPool}, {"user_email_pool", z.UserEmailPool},
		{"user_swift_pool", z.UserSwiftPool}, {"user_uid_pool", z.UserUIDPool},
		{"otp_pool", z.OTPPool}, {"notif_pool", z.NotifPool}, {"topics_pool", z.TopicsPool},
		{"account_pool", z.AccountPool}, {"group_pool", z.GroupPool},
	} {
		dumpPool(f, p.name, p.pool)
	}
	if rel >= denc.Tentacle {
		dumpPool(f, "bucket_logging_pool", z.BucketLoggingPool)
	}
	// encode_json_plain: RGWAccessKey::dump_plain in an object section
	// (rgw_common.cc:2968-2972).
	f.OpenObjectSection("system_key")
	f.DumpString("access_key", z.SystemKey.ID)
	f.DumpString("secret_key", z.SystemKey.Secret)
	f.CloseSection()
	dumpMap(f, "placement_pools", z.PlacementPools,
		func(f formatter.Formatter, k string) { f.DumpString("key", k) },
		func(f formatter.Formatter, p ZonePlacementInfo) { dumpSection(f, "val", p, rel) })
	z.TierConfig.DumpAs(f, "tier_config")
	f.DumpString("realm_id", z.RealmID)
	if rel >= denc.Tentacle {
		dumpPool(f, "restore_pool", z.RestorePool)
	}
}

// Dump is RGWZonePlacementInfo::dump (rgw_zone.cc:763-773; v20.2.4 :771-781).
func (p ZonePlacementInfo) Dump(f formatter.Formatter, rel denc.Release) {
	dumpPool(f, "index_pool", p.IndexPool)
	dumpSection(f, "storage_classes", p.StorageClasses, rel)
	dumpPool(f, "data_extra_pool", p.DataExtraPool)
	f.DumpUnsigned("index_type", uint64(p.IndexType))
	f.DumpBool("inline_data", p.InlineData)
}

// Dump is RGWZoneStorageClasses::dump (rgw_zone.cc:869-874; v20.2.4 :884-889):
// one section per class, named by the class, in key order.
func (c ZoneStorageClasses) Dump(f formatter.Formatter, rel denc.Release) {
	for _, name := range slices.Sorted(maps.Keys(c)) {
		dumpSection(f, name, c[name], rel)
	}
}

// Dump is RGWZoneStorageClass::dump (rgw_zone.cc:926-934; v20.2.4 :966-974).
func (s ZoneStorageClass) Dump(f formatter.Formatter, _ denc.Release) {
	if s.DataPool != nil {
		dumpPool(f, "data_pool", *s.DataPool)
	}
	if s.CompressionType != nil {
		f.DumpString("compression_type", *s.CompressionType)
	}
}

// DumpAs is JSONFormattable::encode_json (ceph_json.cc:922-946): a value
// quoted or bare as it was stored (:29-36), an array whose entries are named
// "obj", an object whose members carry their keys; nothing for FMT_NONE.
func (j JSONFormattable) DumpAs(f formatter.Formatter, name string) {
	switch j.Type {
	case FormattableValue:
		if j.Quoted {
			f.DumpString(name, j.Value)
		} else {
			f.DumpUnquoted(name, j.Value)
		}
	case FormattableArray:
		f.OpenArraySection(name)
		for _, e := range j.Array {
			e.DumpAs(f, "obj")
		}
		f.CloseSection()
	case FormattableObject:
		f.OpenObjectSection(name)
		for _, k := range slices.Sorted(maps.Keys(j.Object)) {
			j.Object[k].DumpAs(f, k)
		}
		f.CloseSection()
	}
}
```

(`dump_zone.go` imports `maps` and `slices` too.) Run the meta suite: PASS, the phase-0 goldens included (MarshalJSON is untouched apart from `Time`'s refactor).

- [ ] **Step 5: Write the failing `op` specs for `AdminOp`, `GetInfo`, `GetZoneConfig`**

`internal/op/admininfo_test.go`:

```go
var _ = Describe("admin ops", func() {
	var (
		store *memstore.Store
		env   *op.Env
	)
	BeforeEach(func() {
		store = memstore.New(memstore.Config{Params: meta.ZoneParams{Name: "z1"}})
		env = &op.Env{Zone: store, Metrics: op.NopMetrics{}, ClusterID: "75d1938b-2949-4933-8386-fb2d1449ff03", Usage: store}
	})
	withCaps := func(caps meta.Caps) op.Identity {
		u := meta.NewUserInfo()
		u.UserID = meta.UserID{ID: "admin"}
		u.Caps = caps
		return op.Identity{User: &u, Owner: meta.UserOwner(u.UserID), OpMask: op.OpTypeAll, Caps: caps}
	}
	It("checks the info cap and returns the fsid", func(ctx SpecContext) {
		o := &op.GetInfo{}
		r := &op.Request{Env: env, Identity: withCaps(meta.Caps{"info": meta.CapRead})}
		Expect(op.Run(ctx, o, r)).To(Succeed())
		Expect(o.ClusterID).To(Equal("75d1938b-2949-4933-8386-fb2d1449ff03"))
	})
	It("denies a missing cap and an anonymous identity", func(ctx SpecContext) {
		Expect(op.Run(ctx, &op.GetInfo{}, &op.Request{Env: env, Identity: withCaps(meta.Caps{"info": meta.CapWrite})})).To(MatchError(op.ErrAccessDenied))
		Expect(op.Run(ctx, &op.GetInfo{}, &op.Request{Env: env, Identity: op.Anonymous()})).To(MatchError(op.ErrAccessDenied))
	})
	It("lets an admin identity through without caps, as rgw_process_authenticated does", func(ctx SpecContext) {
		id := withCaps(nil)
		id.Admin = true
		Expect(op.Run(ctx, &op.GetInfo{}, &op.Request{Env: env, Identity: id})).To(Succeed())
	})
	It("passes the op-mask check with a zero mask", func(ctx SpecContext) {
		id := withCaps(meta.Caps{"info": meta.CapRead})
		id.OpMask = 0
		Expect(op.Run(ctx, &op.GetInfo{}, &op.Request{Env: env, Identity: id})).To(Succeed())
	})
	It("reads the zone params under zone=read", func(ctx SpecContext) {
		o := &op.GetZoneConfig{}
		r := &op.Request{Env: env, Identity: withCaps(meta.Caps{"zone": meta.CapAll})}
		Expect(op.Run(ctx, o, r)).To(Succeed())
		Expect(o.Params.Name).To(Equal("z1"))
	})
	It("names itself for metrics and usage", func() {
		Expect((&op.GetInfo{}).Name()).To(Equal("get_info"))
		Expect((&op.GetZoneConfig{}).Name()).To(Equal("get_zone_config"))
		Expect((&op.GetInfo{}).Action()).To(Equal(policy.ActionNone))
		Expect((&op.GetInfo{}).OpMask()).To(BeZero())
	})
})
```

- [ ] **Step 6: Implement `internal/op/adminop.go` and `admininfo.go`**

```go
package op

// AdminOp is RGWRESTOp: the admin ops' shared lifecycle.
type AdminOp struct{}

func (AdminOp) Action() policy.Action                { return policy.ActionNone }
func (AdminOp) OpMask() uint32                       { return 0 }
func (AdminOp) Init(context.Context, *Request) error { return nil }

// CheckCaps is RGWUserCaps::check_cap on the identity: -EPERM is AccessDenied.
func CheckCaps(r *Request, typ string, perm uint32) error {
	if r.Identity.Caps.Check(typ, perm) {
		return nil
	}
	return ErrAccessDenied
}

// GetInfo is RGWOp_Info_Get.
type GetInfo struct {
	AdminOp
	ClusterID string
}

func NewGetInfo() *GetInfo { return &GetInfo{} }
func (o *GetInfo) Name() string { return "get_info" }
func (o *GetInfo) VerifyPermission(_ context.Context, r *Request) error {
	return CheckCaps(r, "info", meta.CapRead) // RGWOp_Info_Get::check_caps
}
func (o *GetInfo) Execute(_ context.Context, r *Request) error {
	o.ClusterID = r.Env.ClusterID
	return nil
}
func (o *GetInfo) Complete(ctx context.Context, r *Request) { LogUsage(ctx, r, o.Name()) }
```

Every admin op follows this shape: a `NewXxx()` constructor (so later tasks can add defaults without changing call sites), `Name()` = radosgw's `name()`, a one-line `VerifyPermission` naming the `check_caps` pair, `Execute`, and `Complete` → `LogUsage`. A zero-value op (the spec's `&op.GetInfo{}`) behaves the same. `GetZoneConfig`: cap `zone`/`CapRead`, `Execute` sets `o.Params = r.Env.Zone.ZoneParams()`, name `get_zone_config`. Run the op suite: PASS.

- [ ] **Step 7: Write the failing admin handler specs**

`internal/admin/admin_suite_test.go` boots Ginkgo; `handler_test.go` builds a `memstore` with two users — `admin` (caps `info=read;zone=read;users=*;buckets=*;usage=read;metadata=*;accounts=*`) and `nocaps` — plus a fake authenticator: a `s3.AuthenticatorFunc` of signature `func(ctx context.Context, req *http.Request, payloads op.PayloadForms) (*op.AuthResult, error)` that records the `payloads` it was handed in `lastPayloads`, reads the `X-Test-User` header, returns that user's identity (`Caps` from `Info.Caps`, `Admin`/`System` from the flags, `Owner` the user), `op.Anonymous()` when the header is absent, and `op.ErrInvalidAccessKeyID` for an unknown name. `newServer()` returns `httptest.NewServer(admin.Mount("admin", admin.NewHandler(env, auth, admin.Config{Prefix: "admin", ServerHeader: "Ceph Object Gateway (squid)"}), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(599) })))` — 599 marks "went to S3". `get(path, user)`, `do(method, path, user)` and `getAccept(path, user, accept)` issue requests; `body(res)` reads the body; `const fsid = "75d1938b-2949-4933-8386-fb2d1449ff03"`; `reqID` is `tx[0-9a-f]{21}-[0-9a-f]{10}` and `hostID` the memstore zone's host id.

```go
var _ = Describe("admin handler", func() {
	It("mounts at the prefix with radosgw's longest-prefix rule", func() {
		for _, p := range []string{"/admin", "/admin/", "/admin/info", "/admin/user?uid=x"} {
			Expect(get(p, "admin").StatusCode).NotTo(Equal(599), p)
		}
		for _, p := range []string{"/adminx", "/plain/admin", "/", "/Admin/info"} {
			Expect(get(p, "admin").StatusCode).To(Equal(599), p)
		}
	})
	It("serves /admin/info in JSON with radosgw's bytes", func() {
		res := get("/admin/info?format=json", "admin")
		Expect(res.StatusCode).To(Equal(200))
		Expect(res.Header.Get("Content-Type")).To(Equal("application/json"))
		Expect(res.Header.Get("Server")).To(Equal("Ceph Object Gateway (squid)"))
		Expect(res.Header.Get("x-amz-request-id")).To(MatchRegexp(`^` + reqID))
		Expect(string(body(res))).To(Equal(`{"info":{"storage_backends":[{"name":"rados","cluster_id":"` + fsid + `"}]}}`))
		Expect(res.Header.Get("Content-Length")).To(Equal(strconv.Itoa(len(body(get("/admin/info", "admin"))))))
		Expect(res.Header).NotTo(HaveKey("Accept-Ranges"), "the flusher's end_header names no length, rgw_rest.cc:1035-1042")
	})
	It("serves /admin/info in XML: the declaration, then the names JSON drops", func() {
		res := get("/admin/info?format=xml", "admin")
		Expect(res.StatusCode).To(Equal(200))
		Expect(res.Header.Get("Content-Type")).To(Equal("application/xml"))
		Expect(string(body(res))).To(Equal(`<?xml version="1.0" encoding="UTF-8"?><dummy><info><storage_backends><dummy><name>rados</name><cluster_id>` + fsid + `</cluster_id></dummy></storage_backends></info></dummy>`))
	})
	It("serves /admin/info in HTML: the closed status page, then the dump", func() {
		res := get("/admin/info?format=html", "admin")
		Expect(res.Header.Get("Content-Type")).To(Equal("text/html"))
		Expect(string(body(res))).To(Equal(`<html><head><title>200 OK</title></head><body><h1>200 OK</h1><ul></ul></body></html><dummy><info><storage_backends><dummy><li>name: rados</li><li>cluster_id: ` + fsid + `</li></dummy></storage_backends></info></dummy>`))
	})
	DescribeTable("selects the format as allocate_formatter does",
		func(query, accept, want string) {
			res := getAccept("/admin/info"+query, "admin", accept)
			Expect(res.Header.Get("Content-Type")).To(Equal(want))
		},
		Entry("default", "", "", "application/json"),
		Entry("format=xml", "?format=xml", "", "application/xml"),
		Entry("format wins over Accept", "?format=json", "application/xml", "application/json"),
		Entry("format is exact", "?format=XML", "", "application/json"),
		Entry("Accept up to ';'", "", "application/xml; q=0.9", "application/xml"),
		Entry("text/xml", "", "text/xml", "application/xml"),
		Entry("text/html", "", "text/html", "text/html"),
		Entry("a list is not parsed", "", "application/xml, text/html", "application/json"),
		Entry("Accept is exact", "", "Application/XML", "application/json"),
		Entry("*/*", "", "*/*", "application/json"),
	)
	It("answers AccessDenied in radosgw's error document in each format", func() {
		res := get("/admin/info", "nocaps")
		Expect(res.StatusCode).To(Equal(403))
		Expect(res.Header.Get("Content-Type")).To(Equal("application/json"))
		Expect(res.Header.Get("Accept-Ranges")).To(Equal("bytes"), "end_header's error branch sets the length through dump_content_length, rgw_rest.cc:620-624")
		Expect(string(body(res))).To(MatchRegexp(`^\{"Code":"AccessDenied","Message":"","RequestId":"` + reqID + `","HostId":"` + regexp.QuoteMeta(hostID) + `"\}$`))
		res = get("/admin/info?format=xml", "nocaps")
		Expect(res.Header.Get("Content-Type")).To(Equal("application/xml"))
		Expect(string(body(res))).To(MatchRegexp(`^<\?xml version="1\.0" encoding="UTF-8"\?><Error><Code>AccessDenied</Code><Message></Message><RequestId>` + reqID + `</RequestId><HostId>` + regexp.QuoteMeta(hostID) + `</HostId></Error>$`))
		res = get("/admin/info?format=html", "nocaps")
		Expect(res.Header.Get("Content-Type")).To(Equal("text/html"))
		Expect(string(body(res))).To(MatchRegexp(`^<html><head><title>403 Forbidden</title></head><body><h1>403 Forbidden</h1><ul><li>Code: AccessDenied</li><li>Message: </li><li>RequestId: ` + reqID + `</li><li>HostId: ` + regexp.QuoteMeta(hostID) + `</li></ul></body></html>$`))
		Expect(res.Header.Get("Content-Length")).NotTo(BeEmpty())
		Expect(get("/admin/info", "").StatusCode).To(Equal(403), "anonymous")
	})
	It("answers 405 in JSON where radosgw has no handler and in the format where it has one", func() {
		res := get("/admin/nosuch?format=xml", "admin")
		Expect(res.StatusCode).To(Equal(405))
		Expect(res.Header.Get("Content-Type")).To(Equal("application/json"), "abort_early allocates a JSONFormatter")
		Expect(get("/admin?format=xml", "admin").Header.Get("Content-Type")).To(Equal("application/json"))
		res = do("POST", "/admin/info?format=xml", "admin")
		Expect(res.StatusCode).To(Equal(405))
		Expect(string(body(res))).To(HavePrefix(`<?xml version="1.0" encoding="UTF-8"?><Error><Code>MethodNotAllowed</Code>`))
		Expect(get("/admin/config?type=zonegroup", "admin").StatusCode).To(Equal(405))
		Expect(do("PUT", "/admin/bucket?sync&bucket=b", "admin").StatusCode).To(Equal(405), "Sync_Bucket is excluded")
		Expect(get("/admin/log", "admin").StatusCode).To(Equal(405))
		Expect(do("POST", "/admin/realm/period", "admin").StatusCode).To(Equal(405))
	})
	It("hands the authenticator the forms the route's op type accepts", func() {
		do("PUT", "/admin/metadata/user?key=admin", "admin")
		Expect(lastPayloads).To(Equal(op.PayloadSigned), "RGW_OP_ADMIN_SET_METADATA is on the single-chunk list")
		do("PUT", "/admin/user?quota&uid=admin&quota-type=user", "admin")
		Expect(lastPayloads).To(Equal(op.PayloadForms(0)))
		get("/admin/info", "admin")
		Expect(lastPayloads).To(Equal(op.PayloadForms(0)))
	})
	It("returns the zone params for config?type=zone without a declaration or dump_start", func() {
		want := func(f formatter.Formatter) string {
			f.OpenObjectSection("zone_params")
			store.ZoneParams().Dump(f, store.Release())
			f.CloseSection()
			return string(f.Bytes())
		}
		res := get("/admin/config?type=zone", "admin")
		Expect(res.StatusCode).To(Equal(200))
		Expect(string(body(res))).To(Equal(want(formatter.NewJSON(false))))
		res = get("/admin/config?type=zone&format=xml", "admin")
		Expect(res.Header.Get("Content-Type")).To(Equal("application/xml"))
		Expect(string(body(res))).To(Equal(want(formatter.NewXML(false, false))))
		Expect(string(body(res))).To(HavePrefix("<zone_params><id>"))
	})
	It("refuses a suspended user", func() {
		suspend("admin")
		Expect(get("/admin/info", "admin").StatusCode).To(Equal(403))
		var doc map[string]string
		_ = json.Unmarshal(body(get("/admin/info", "admin")), &doc)
		Expect(doc["Code"]).To(Equal("UserSuspended"))
	})
	It("answers SlowDown past the concurrency cap, in the request's format", func() {
		// cfg.MaxConcurrent = 1; a route registered for the spec blocks on a channel;
		// with one request parked in it, a second GET /admin/info?format=xml answers
		// 503 with <Code>SlowDown</Code> in the XML error document.
	})
})
```

Write the SlowDown body in full as its comment describes. `request_test.go`:

```go
var _ = Describe("ParseArgs", func() {
	It("keeps query order and the last value of a repeated key", func() {
		a := admin.ParseArgs("uid=a&uid=b&key&quota&x=1%202")
		v, ok := a.String("uid", "")
		Expect(ok).To(BeTrue())
		Expect(v).To(Equal("b"), "RGWHTTPArgs::append assigns: val_map[name] = val, rgw_common.cc:922")
		v, _ = a.String("x", "")
		Expect(v).To(Equal("1 2"))
		Expect(a.SubResource()).To(Equal("key"), "the first admin sub-resource in the query wins")
		Expect(admin.ParseArgs("quota&key").SubResource()).To(Equal("quota"))
		Expect(admin.ParseArgs("uid=a").SubResource()).To(BeEmpty())
	})
	DescribeTable("Bool is RESTArgs::get_bool",
		func(q string, def, want bool, wantPresent bool, wantErr bool) {
			v, present, err := admin.ParseArgs(q).Bool("suspended", def)
			Expect(present).To(Equal(wantPresent))
			if wantErr {
				Expect(err).To(MatchError(op.ErrInvalidArgument))
				Expect(v).To(Equal(def), "the default survives a bad value")
				return
			}
			Expect(err).NotTo(HaveOccurred())
			Expect(v).To(Equal(want))
		},
		Entry("absent", "uid=x", false, false, false, false),
		Entry("empty is true", "suspended", false, true, true, false),
		Entry("true", "suspended=True", false, true, true, false),
		Entry("1", "suspended=1", false, true, true, false),
		Entry("false", "suspended=FALSE", true, false, true, false),
		Entry("0", "suspended=0", true, false, true, false),
		Entry("yes is invalid", "suspended=yes", true, true, true, true),
	)
	DescribeTable("Epoch is utime_t::parse_date",
		func(in string, want uint64, ok bool) {
			v, _, err := admin.ParseArgs("start=" + url.QueryEscape(in)).Epoch("start", 7)
			if !ok {
				Expect(err).To(MatchError(op.ErrInvalidArgument))
				return
			}
			Expect(err).NotTo(HaveOccurred())
			Expect(v).To(Equal(want))
		},
		Entry("date", "2012-09-25", uint64(1348531200), true),
		Entry("date time", "2012-09-25 16:00:00", uint64(1348588800), true),
		Entry("T and fraction", "2012-09-25T16:00:00.123", uint64(1348588800), true),
		Entry("seconds.usec", "1348588800.5", uint64(1348588800), true),
		Entry("garbage", "yesterday", 0, false),
	)
	It("parses the resource path and refuses what radosgw registers no manager for", func() {
		q, err := admin.ParseRequest("/admin/metadata/bucket.instance", "admin", "key=b%3A1")
		Expect(err).NotTo(HaveOccurred())
		Expect(q.Resource).To(Equal("metadata"))
		Expect(q.Sub).To(Equal("bucket.instance"))
		_, err = admin.ParseRequest("/admin", "admin", "")
		Expect(err).To(MatchError(op.ErrMethodNotAllowed))
		q, err = admin.ParseRequest("/admin/nosuch", "admin", "format=xml")
		Expect(err).To(MatchError(op.ErrMethodNotAllowed))
		Expect(q.Format).To(Equal(admin.FormatJSON))
	})
})
```

`dispatch_test.go`: a `DescribeTable` with one `Entry` per row of the table in Step 8 (method, path, query, release → name and payload forms), plus the 405 rows, plus `PUT /admin/account?quota` → `set_account_quota_info` on `denc.Tentacle` and `modify_account` on `denc.Squid`, plus `PUT /admin/metadata/user` → `set_metadata` with `op.PayloadSigned` and every other row with `op.PayloadForms(0)`.

- [ ] **Step 8: Implement `request.go`, `dispatch.go`, `render.go`, `handler.go`, `info.go`**

`request.go`: `ParseArgs` splits `rawQuery` on `&`, each piece on the first `=`, `url.QueryUnescape`s both (radosgw url-decodes the whole `name=value` pair with `in_query=true`, so `+` is a space); it records order and keeps the last value of a repeated key. `SubResource` walks the ordered keys and returns the first in `{subuser, key, caps, index, policy, quota, list, object, sync}`. `Bool`/`Int*`/`Epoch` as specified; `Epoch` implements `parse_date` with `time.Parse` over the layouts `2006-01-02`, `2006-01-02 15:04:05`, `2006-01-02T15:04:05`, both with an optional fractional part (strip digits after `.`) and an optional `-0700` zone (apply `time.Parse`'s zone offset), else `<sec>.<usec>` with `strconv`. `ParseRequest` trims `/<prefix>` then splits the remainder on `/` into `Resource` and `Sub` (a third segment is ignored, as `init_from_header` ignores it); a `Resource` outside radosgw's registered admin managers — `info`, `usage`, `account` ([rgw_appmain.cc:356-358](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_appmain.cc#L356-L358); v20.2.4 [:365-367](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_appmain.cc#L365-L367)) and `user`, `bucket`, `metadata`, `log`, `config`, `realm`, `ratelimit` (`RadosStore::register_admin_apis`, [rgw_sal_rados.cc:2025-2036](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L2025-L2036); v20.2.4 [:2568-2579](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L2568-L2579)) — or an empty one is `op.ErrMethodNotAllowed` with `Format` left JSON. `SelectFormat`:

```go
func SelectFormat(a Args, accept string) Format {
	switch v, _ := a.String("format", ""); v {
	case "xml":
		return FormatXML
	case "json":
		return FormatJSON
	case "html":
		return FormatHTML
	}
	if i := strings.IndexByte(accept, ';'); i >= 0 {
		accept = accept[:i] // trim at first ';' (rgw_rest.cc:1749-1751)
	}
	switch accept {
	case "text/xml", "application/xml":
		return FormatXML
	case "application/json":
		return FormatJSON
	case "text/html":
		return FormatHTML
	}
	return FormatJSON
}
```

`dispatch.go`, the table (radosgw op names; the forms column is what `get_auth_data_v4` accepts for the op's type):

| Resource | Method | Sub-resource | Name | Payload forms |
|---|---|---|---|---|
| info | GET | — | get_info | none |
| config | GET | (`type=zone` else 405) | get_zone_config | none |
| user | GET | quota / list / — | get_quota_info / list_user / get_user_info | none |
| user | PUT | subuser / key / caps / quota / — | create_subuser / create_access_key / add_user_caps / set_quota_info / create_user | none |
| user | POST | subuser / — | modify_subuser / modify_user | none |
| user | DELETE | subuser / key / caps / — | remove_subuser / remove_access_key / remove_user_caps / remove_user | none |
| bucket | GET | policy / index / — | get_policy / check_bucket_index / get_bucket_info | none |
| bucket | PUT | quota / sync / — | set_bucket_quota / 405 / link_bucket | none |
| bucket | POST | — | unlink_bucket | none |
| bucket | DELETE | object / — | remove_object / remove_bucket | none |
| metadata | GET | (`myself` arg) / (`key` arg) / — | get_metadata_myself / get_metadata / list_metadata | none |
| metadata | PUT | — | set_metadata | `op.PayloadSigned` |
| metadata | DELETE | — | remove_metadata | none |
| usage | GET / DELETE | — | get_usage / trim_usage | none |
| account | POST / PUT / GET / DELETE | quota (PUT, Tentacle) / — | create_account / set_account_quota_info or modify_account / get_account / delete_account | none |
| ratelimit | GET / POST | — | get_ratelimit_info / put_ratelimit_info | none |
| realm | GET | list / — | list_realms / get_realm | none |
| realm (Sub `period`) | GET | — | get_period | none |
| log, realm/period POST, everything else | — | — | 405 | — |

`metadata`'s GET uses `Args.Has("myself")`/`Has("key")` ([rgw_rest_metadata.cc:302-309](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_metadata.cc#L302-L309), `args.exists`), not the sub-resource list. `account` PUT `?quota` routes to `set_account_quota_info` only when `rel >= denc.Tentacle`; on Squid it is `modify_account` (rgw_rest_account.cc v19.2.6:228-231 vs v20.2.4:306-311); the handler passes `env.Zone.Release()`. The forms: `RGWOp_Metadata_Put::get_type()` is `RGW_OP_ADMIN_SET_METADATA` ([rgw_rest_metadata.h:67](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_metadata.h#L67), both tags), the one admin op type on `get_auth_data_v4`'s single-chunk list ([rgw_rest_s3.cc:5916](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L5916); v20.2.4 [:6483](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L6483)); every other admin op keeps `RGW_OP_UNKNOWN`, so a signed non-empty body there is 501 NotImplemented and only an unsigned or empty payload passes (go-ceph and Rook sign `UNSIGNED-PAYLOAD`); no admin op takes aws-chunked ([:5962-5969](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5962-L5969); v20.2.4 [:6539](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L6539)).

`render.go`:

```go
package admin

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/jhoblitt/rgw-go/internal/formatter"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/s3"
)

// mimeType is to_mime_type (rgw_common.h:189-207).
func (f Format) mimeType() string {
	switch f {
	case FormatXML:
		return "application/xml"
	case FormatHTML:
		return "text/html"
	default:
		return "application/json"
	}
}

// newFormatter is reallocate_formatter for the admin handlers
// (rgw_rest.cc:1791-1804): XML lower-cases names for a bulk-delete or
// multipart-manifest=delete query; HTML is pretty only for the website
// endpoint, which admin requests never are.
func (q Request) newFormatter() formatter.Formatter {
	switch q.Format {
	case FormatXML:
		mm, _ := q.Args.String("multipart-manifest", "")
		return formatter.NewXML(false, q.Args.Has("bulk-delete") || mm == "delete")
	case FormatHTML:
		return formatter.NewHTML(false)
	default:
		return formatter.NewJSON(false)
	}
}

// statusNames is http_codes (rgw_rest.cc:44-88), the text dump_errno hands
// the formatter and HTMLFormatter prints.
var statusNames = map[int]string{
	100: "Continue", 200: "OK", 201: "Created", 202: "Accepted", 204: "No Content",
	205: "Reset Content", 206: "Partial Content", 207: "Multi Status", 208: "Already Reported",
	300: "Multiple Choices", 301: "Moved Permanently", 302: "Found", 303: "See Other",
	304: "Not Modified", 305: "User Proxy", 306: "Switch Proxy", 307: "Temporary Redirect",
	308: "Permanent Redirect", 400: "Bad Request", 401: "Unauthorized", 402: "Payment Required",
	403: "Forbidden", 404: "Not Found", 405: "Method Not Allowed", 406: "Not Acceptable",
	407: "Proxy Authentication Required", 408: "Request Timeout", 409: "Conflict", 410: "Gone",
	411: "Length Required", 412: "Precondition Failed", 413: "Request Entity Too Large",
	414: "Request-URI Too Long", 415: "Unsupported Media Type",
	416: "Requested Range Not Satisfiable", 417: "Expectation Failed",
	422: "Unprocessable Entity", 498: "Rate Limited", 500: "Internal Server Error",
	501: "Not Implemented", 503: "Slow Down", 507: "Insufficient Storage",
}

// WriteBody writes a flusher-started body: RGWRESTFlusher::do_start's
// dump_start, flushed with the headers by rgw_flush_formatter_and_reset, whose
// output_footer closes HTML's status page, then the op's dump
// (rgw_rest.cc:1035-1047, :303-314).
func WriteBody(w http.ResponseWriter, r *op.Request, q Request, dump func(formatter.Formatter)) error {
	f := q.newFormatter()
	f.SetStatus(http.StatusOK, statusNames[http.StatusOK])
	f.OutputHeader()
	f.OutputFooter()
	head := bytes.Clone(f.Bytes())
	f.Reset()
	dump(f)
	if err := f.Err(); err != nil {
		return err
	}
	write(w, r, http.StatusOK, q.Format.mimeType(), append(head, f.Bytes()...), false)
	return nil
}

// WriteDumped writes a body the op dumped without dump_start, as the zone
// config, metadata get and list do (rgw_rest_config.cc:35-47,
// rgw_rest_metadata.cc:54-174): no declaration and no status page.
func WriteDumped(w http.ResponseWriter, r *op.Request, q Request, dump func(formatter.Formatter)) error {
	return writeDump(w, r, q, q.Format.mimeType(), false, dump)
}

// WriteTyped is WriteDumped under a content type the op names itself, as the
// realm and period getters name application/json whatever the format
// (rgw_rest_realm.cc:48-49, :301-302, :344-348). Those getters name the
// length too, end_header(s, NULL, "application/json",
// s->formatter->get_len()), so it goes out through dump_content_length, with
// Accept-Ranges.
func WriteTyped(w http.ResponseWriter, r *op.Request, q Request, contentType string, dump func(formatter.Formatter)) error {
	return writeDump(w, r, q, contentType, true, dump)
}

func writeDump(w http.ResponseWriter, r *op.Request, q Request, contentType string, named bool, dump func(formatter.Formatter)) error {
	f := q.newFormatter()
	dump(f)
	if err := f.Err(); err != nil {
		return err
	}
	write(w, r, http.StatusOK, contentType, f.Bytes(), named)
	return nil
}

// WriteEmpty writes status with the common headers and no body.
func WriteEmpty(w http.ResponseWriter, r *op.Request, status int) {
	s3.SetCommonHeaders(w, r)
	w.Header().Set("Content-Length", "0")
	w.WriteHeader(status)
}

// WriteError is end_header's error branch (rgw_rest.cc:620-624): dump_start,
// dump(req_state*) (rgw_common.cc:381-404), output_footer, and the length
// through dump_content_length, with Accept-Ranges. Admin requests set no
// bucket name, so BucketName never appears.
func WriteError(ctx context.Context, w http.ResponseWriter, r *op.Request, q Request, err error) {
	e := op.AsError(err)
	if e.Status >= http.StatusInternalServerError {
		slog.ErrorContext(ctx, "admin request failed", slog.String("request_id", r.ID), slog.String("code", e.Code), slog.Any("error", err))
	}
	f := q.newFormatter()
	f.SetStatus(e.Status, statusNames[e.Status])
	f.OutputHeader()
	if q.Format != FormatHTML {
		f.OpenObjectSection("Error")
	}
	if e.Code != "" {
		f.DumpString("Code", e.Code)
	}
	f.DumpString("Message", e.Message)
	if r.ID != "" {
		f.DumpString("RequestId", r.ID)
	}
	f.DumpString("HostId", r.Env.HostID)
	if q.Format != FormatHTML {
		f.CloseSection()
	}
	f.OutputFooter()
	write(w, r, e.Status, q.Format.mimeType(), f.Bytes(), true)
}

// write sends body at status. named says radosgw's end_header was handed the
// length, which it then sets through dump_content_length with Accept-Ranges:
// every error document and the three getters. An admin success that names
// none gets the length radosgw's frontend adds when the response completes
// (rgw_client_io_filters.h:221-253), without Accept-Ranges.
func write(w http.ResponseWriter, r *op.Request, status int, contentType string, body []byte, named bool) {
	s3.SetCommonHeaders(w, r)
	w.Header().Set("Content-Type", contentType)
	if named {
		s3.SetContentLength(w.Header(), uint64(len(body)))
	} else {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	}
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
```

A handler whose dump calls `Fail` gets the error back from `WriteBody`/`WriteDumped`/`WriteTyped` before anything is written and returns it; `ServeHTTP` maps an error wrapping `meta.ErrOpaqueJSON` to `op.ErrNotImplemented` in `WriteError` (the record is carried opaque).

`handler.go`: as the lifecycle above; `NewHandler` registers every route name from the table with a handler answering `op.ErrNotImplemented`, then `info.go`'s two; later tasks `Register` theirs from `newXxxHandlers()` functions `NewHandler` calls, one file per task. `Mount`:

```go
func Mount(prefix string, admin, other http.Handler) http.Handler {
	p := "/" + strings.Trim(prefix, "/")
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == p || strings.HasPrefix(req.URL.Path, p+"/") {
			admin.ServeHTTP(w, req)
			return
		}
		other.ServeHTTP(w, req)
	})
}
```

`info.go`:

```go
func getInfo(ctx context.Context, w http.ResponseWriter, r *op.Request, q Request) error {
	o := op.NewGetInfo()
	if err := op.Run(ctx, o, r); err != nil {
		return err
	}
	// RGWOp_Info_Get::execute (rgw_rest_info.cc:31-41): the two "dummy"
	// sections show only in XML and HTML.
	return WriteBody(w, r, q, func(f formatter.Formatter) {
		f.OpenObjectSection("dummy")
		f.OpenObjectSection("info")
		f.OpenArraySection("storage_backends")
		f.OpenObjectSection("dummy")
		f.DumpString("name", "rados")
		f.DumpString("cluster_id", o.ClusterID)
		f.CloseSection()
		f.CloseSection()
		f.CloseSection()
		f.CloseSection()
	})
}

func getZoneConfig(ctx context.Context, w http.ResponseWriter, r *op.Request, q Request) error {
	o := op.NewGetZoneConfig()
	if err := op.Run(ctx, o, r); err != nil {
		return err
	}
	// RGWOp_ZoneConfig_Get::send_response (rgw_rest_config.cc:35-47): the
	// headers go out before the dump, so no declaration precedes it.
	return WriteDumped(w, r, q, func(f formatter.Formatter) {
		f.OpenObjectSection("zone_params")
		o.Params.Dump(f, r.Env.Zone.Release())
		f.CloseSection()
	})
}
```

(`Dispatch` answers 405 for a `type` other than `zone`, as `RGWHandler_Config::op_get` returns no op, [rgw_rest_config.cc:49-57](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_config.cc#L49-L57).) Run the admin suite: PASS. `make lint`.

- [ ] **Step 9: Wire `cli serve`**

In `internal/cli/serve.go` after G's step 8 (`handler := s3.NewHandler(...)`, with A's verifier as the authenticator): when `apis.Admin`, `fsid, err := cluster.FSID()` (fatal on error), `env.ClusterID = fsid`, `prefix, _ := conf.String("rgw_admin_entry")`, `adm := admin.NewHandler(env, verifier, admin.Config{Prefix: prefix, TransIDSuffix: cfg.TransIDSuffix, ServerHeader: cfg.ServerHeader, MaxConcurrent: cfg.MaxConcurrent})`, `root = admin.Mount(prefix, adm, handler)`; `frontend.New(spec, frontend.Deadlines(root, ...))`. When only `admin` is enabled, `handler` is G's 405 handler and `root` still mounts admin. Update G's integration case: `--rgw-enable-apis=swift,admin` answers 405 on `/` AND 403 `AccessDenied` on an unsigned `GET /admin/info` (JSON body). `slog.InfoContext(ctx, "admin api mounted", slog.String("prefix", prefix))`.

- [ ] **Step 10: Record the header difference**

`docs/exclusions.md`, in "Coexistence obligations independent of any exclusion", a new bullet after the "Signature quirks are radosgw's, not AWS's" bullet:

"- **Admin responses carry a Content-Type on every body.** rgw-go renders the admin API in JSON, XML or HTML exactly as radosgw's formatters do, bodies byte for byte, with radosgw's Content-Length and Accept-Ranges headers, but it sets a Content-Type on every body, the format's own or, for the realm, realm-list and period getters, the `application/json` radosgw names there whatever the format; radosgw sends none on a JSON body its flusher started or on the zone configuration. radosgw also fixes a 200 status as soon as an op starts its body, and an op that fails after that still answers 200 with its body cut short: a bucket listing for a uid that does not exist gets a 200 with no document, a bucket index check given `check-objects` without `fix` a 200 that stops after the multipart entries, and a usage or user listing whose RADOS read fails midway a partial one. rgw-go renders the whole body first and answers each of these with the error document and its status. Clients that read the body, go-ceph and Rook among them, see no difference on success; one that inspects the headers does."

Report the change for the rgw-rs session.

- [ ] **Step 11: `make check`; commit**

```bash
git add internal/formatter internal/meta/dump.go internal/meta/dump_zone.go internal/meta/dump_test.go internal/meta/dump_goldens_test.go internal/meta/time.go internal/admin internal/op/adminop.go internal/op/admininfo.go internal/op/admininfo_test.go internal/op/env.go internal/cli docs/exclusions.md
git commit -m "feat(admin): mount the admin API with radosgw's formatters, dispatch, args and error document"
```

## Task 4: User admin core: create, info, modify, remove, list; the admin user document

**Files:**
- Create: `internal/op/adminuser.go`, `internal/op/adminuser_test.go`, `internal/op/adminpurge.go`, `internal/admin/user.go`, `internal/admin/user_test.go`
- Modify: `internal/meta/owner.go` (export `ValidAccountID`), `internal/meta/user.go` (`IdentityType.DumpName`), `internal/meta/dump.go` and `dump_goldens_test.go` (`Caps.DumpAs`, `Caps.Dump`), `internal/admin/handler.go` (`NewHandler` calls `newUserHandlers()`), `docs/exclusions.md` (the purge bullet, Step 5)

**Interfaces:**
- Consumes: G's `op.UserStore` (`GetUser`, `GetUserByAccessKey`, `GetUserByEmail`, `PutUser{Exclusive, IfVersion}`, `RemoveUser`, `ListUserBuckets`), `op.BucketStore` (`GetBucket`, `PutBucketInfo`, `DeleteBucket` — whose own-bucket branch aborts the uploads, M Task 7 with P Task 7 — `ListObjects`), `op.ObjectStore.DeleteObject`, `op.StatsStore.UserStats`, `op.MetadataStore.List` (G's memstore has it; the driver's comes in Task 8), `op.ErrUserAlreadyExists`, `op.ErrEmailExists`, `op.ErrKeyExists`, `op.ErrNoSuchUser`, `op.ErrInvalidArgument`, `op.ErrInvalidTenantName`, `op.ErrInvalidAccessKeyID`, `op.ErrInvalidSecretKey`, `op.ErrInvalidCapability`, `op.ErrBucketAlreadyExists`; A's `op.AccountStore.GetAccount`; M's `op.LogUsage`, `options.defaultBucketQuota`/`defaultUserQuota` are the driver's — the op reads `rgw_user_default_quota_max_objects`/`_size` and `rgw_bucket_default_quota_max_objects`/`_size`, `rgw_user_max_buckets`, `rgw_user_unique_email`, `rgw_list_buckets_max_chunk` through `Env.Conf`; Task 1's `meta.Caps.AddString`, `meta.ParseOpTypeList`, `meta.OpTypeString`, `meta.PermString`, `op.AccountStore.AddAccountUser/RemoveAccountUser`, `op.BucketAdminStore.ChownBucket/SyncOwnerStats`; Task 3's `admin.Args`, `WriteBody`, `WriteEmpty`, `WriteError`, `Register`, `formatter.Formatter`, `meta.Quota.Dump`, `meta.Time.Gmtime`; phase 0's `capPermString` in `meta`.
- Produces:

```go
package meta

// ValidAccountID is rgw::account::validate_id: "RGW" + 17 digits (rgw_account.cc:47-71).
func ValidAccountID(s string) bool
```

```go
package op

// KeyType is RGWUserAdminOpState's key_type: KEY_TYPE_SWIFT 0, KEY_TYPE_S3 1, KEY_TYPE_UNDEFINED -1.
type KeyType int8
const (
	KeyTypeSwift     KeyType = 0
	KeyTypeS3        KeyType = 1
	KeyTypeUndefined KeyType = -1
)
// ParseKeyType is the rgw_rest_user.cc mapping: "swift", "s3", anything else undefined.
func ParseKeyType(s string) KeyType

// UserKeyParams is the key part of RGWUserAdminOpState (rgw_user.h:212-230,
// :340-356): AccessKey/SecretKey when given; GenerateKey is set_generate_key,
// which generates whichever of the two is absent. Type undefined means "swift
// when a subuser is named, else s3" (RGWAccessKeyPool::check_op, rgw_user.cc:474-483).
type UserKeyParams struct {
	AccessKey, SecretKey string
	Type                 KeyType
	GenerateKey          bool
	Subuser              string
	Active               *bool // Key_Create's "active" when specified
}
func (p UserKeyParams) hasKeyOp() bool // set_access_key/set_secret_key/set_generate_key set key_op

// CreateUser is RGWOp_User_Create + RGWUserAdminOp_User::create (rgw_rest_user.cc:133-285,
// rgw_user.cc:1744-1918, :2410-2442). Cap users=write.
type CreateUser struct {
	AdminOp
	UID              meta.UserID // tenant applied from the "tenant" argument by the handler
	DisplayName      string
	Email            string
	Key              UserKeyParams
	Caps             string // user-caps, ';'-separated
	Suspended        *bool
	MaxBuckets       *int32 // nil: rgw_user_max_buckets (the REST layer only sets it when it differs from that default)
	System           *bool
	AccountRoot      *bool
	OpMask           *uint32
	DefaultPlacement *meta.PlacementRule
	PlacementTags    []string // nil: unspecified
	AccountID        string
	Path             string
	Result           meta.UserInfo
}

// ModifyUser is RGWOp_User_Modify + RGWUserAdminOp_User::modify (:287-438; rgw_user.cc:2017-2248).
type ModifyUser struct {
	AdminOp
	UID              meta.UserID
	DisplayName      string
	Email            *string // nil: unchanged; "" clears (user_email_specified)
	Key              UserKeyParams
	Suspended        *bool
	MaxBuckets       *int32
	System           *bool
	AccountRoot      *bool
	OpMask           *uint32
	DefaultPlacement *meta.PlacementRule
	PlacementTags    []string
	AccountID        string
	Path             string
	UserQuota        *meta.Quota // set by set_quota_info
	BucketQuota      *meta.Quota
	Result           meta.UserInfo
}

// GetUserInfo is RGWOp_User_Info + RGWUserAdminOp_User::info (:72-131; rgw_user.cc:2347-2408).
type GetUserInfo struct {
	AdminOp
	UID        meta.UserID // or
	AccessKey  string
	FetchStats bool
	SyncStats  bool
	Result     meta.UserInfo
	Stats      *Stats // when FetchStats
	DumpKeys   bool   // decided by the handler: users=read held, or Identity.Admin
}

// RemoveUser is RGWOp_User_Remove + RGWUserAdminOp_User::remove (:440-479; rgw_user.cc:1940-2015).
type RemoveUser struct {
	AdminOp
	UID       meta.UserID
	PurgeData bool
}

// ListUsers is RGWOp_User_List + RGWUser::list (:44-70; rgw_user.cc:2276-2329): the
// "user" metadata section, max-entries capped at 1000.
type ListUsers struct {
	AdminOp
	Marker     string
	MaxEntries uint32 // default 1000
	Keys       []string
	Truncated  bool
	Count      uint64
	NextMarker string
}

// PurgeBucket is RadosBucket::remove(delete_children=true)
// (rgw_sal_rados.cc:350-468) over the op stores: every version in the plain
// namespace deleted, then DeleteBucket, whose own-bucket branch aborts the
// in-flight uploads as remove's abort_multiparts does (:395-400). Shared by
// RemoveUser, RemoveBucketAdmin and RemoveAccount.
func PurgeBucket(ctx context.Context, env *Env, rec *BucketRecord) error
// DeleteBucketWithChildren deletes rec, purging first when purge is set.
func DeleteBucketWithChildren(ctx context.Context, env *Env, rec *BucketRecord, purge bool) error

// lookupUser is RGWUser::init (rgw_user.cc:1389-1459): by uid, then by email
// when rgw_user_unique_email, then by access key; it reports which index found it.
type userLookup struct {
	rec                              *UserRecord
	foundByUID, foundByEmail, foundByKey bool
}
func lookupUser(ctx context.Context, env *Env, uid meta.UserID, email, accessKey string, swift bool) (userLookup, error)
```

```go
package admin

// dumpUserInfo is dump_user_info (rgw_user.cc:132-196; v20.2.4 :133-201),
// radosgw's admin user document: a "user_info" section, keys only when
// dumpKeys, stats only when given; the metadata section renders
// RGWUserInfo::dump instead.
func dumpUserInfo(f formatter.Formatter, info meta.UserInfo, dumpKeys bool, stats *op.Stats, rel denc.Release)
// dumpSubusers, dumpAccessKeys and dumpSwiftKeys are dump_subusers_info,
// dump_access_keys_info and dump_swift_keys_info (:74-130): arrays whose
// entries are "user" and "key" sections, in std::map order.
func dumpSubusers(f formatter.Formatter, info meta.UserInfo)
func dumpAccessKeys(f formatter.Formatter, info meta.UserInfo)
func dumpSwiftKeys(f formatter.Formatter, info meta.UserInfo)
// dumpStats is RGWStorageStats::dump with dump_utilized true (rgw_common.cc:3102-3115).
func dumpStats(f formatter.Formatter, s op.Stats)
func newUserHandlers() map[string]HandlerFunc // get_user_info, create_user, modify_user, remove_user
```

```go
package meta

// DumpAs is encode_json(name, RGWUserCaps), which is RGWUserCaps::dump(f,
// name) (rgw_common.cc:2446-2449, :2016-2043): an array of "cap" sections,
// each a type and its permission words, "<none>" when empty.
func (c Caps) DumpAs(f formatter.Formatter, name string)
// Dump is RGWUserCaps::dump(f), which names the array "caps" (:2002-2005).
func (c Caps) Dump(f formatter.Formatter, _ denc.Release)
// DumpName is the string dump_user_info and RGWUserInfo::dump give the
// identity type (rgw_user.cc:164-185).
func (t IdentityType) DumpName() string
```

`meta.UserInfo.Dump` (Task 8) is NOT used for these routes: it is `RGWUserInfo::dump` (metadata form: no `tenant`, `user_id` as `tenant$id`, keys with `create_date`, `system`/`admin` omitted when false). The `type` string is `IdentityType.dumpName()`, exported as `meta.IdentityType.DumpName()` in this task.

Semantics carried into the ops (sources in driver/rados/rgw_user.cc at v19.2.6 unless named):

- **lookupUser** (`RGWUser::init`, [:1389-1459](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_user.cc#L1389-L1459)) — `GetUser(uid)` unless uid empty or anonymous; not found and `rgw_user_unique_email` and email given → `GetUserByEmail`; not found and access key given → `GetUserByAccessKey` (a swift key is looked up through the same index; the driver keeps swift keys in `users.swift`, M Task 4 — call `GetUserByAccessKey` for s3 and skip swift until a `GetUserBySwiftKey` exists: radosgw's `found_by_key` for swift keys only matters for create-time duplicate detection of an explicit swift id, which Task 5's subuser path re-checks through the user's own `SwiftKeys`).
- **CreateUser.Execute** (`check_op` [:1515-1546](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_user.cc#L1515-L1546), `user_add_helper` [:198-234](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_user.cc#L198-L234), `execute_add` [:1744-1894](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_user.cc#L1744-L1894)) — `check_op`: uid empty or anonymous → `ErrInvalidArgument`; tenant chars alnum/`_` → else `ErrInvalidTenantName`; `user_add_helper`: found by uid → `ErrUserAlreadyExists`, by email → `ErrEmailExists`, by key → `ErrKeyExists`; empty display name → `ErrInvalidArgument`; `execute_add`: `meta.ValidAccountID(uid.ID)` or of the tenant → `ErrInvalidArgument`; info = `meta.NewUserInfo()` with uid, display name, `Type = IdentityRGW`, email, `MaxBuckets` (param or `rgw_user_max_buckets`), `Suspended`, `System`, `OpMask` when given, quotas from the `rgw_*_default_quota_max_*` options (`Enabled` when either limit ≥ 0, as `rgw_apply_default_*_quota` does), placement and tags when given; `AccountID`: `ValidAccountID` else `ErrInvalidArgument`; `Env.Accounts.GetAccount` (missing → the error as is: `ErrNoSuchEntity`); tenant must equal the account's → `ErrInvalidArgument.WithMessage("User tenant does not match account tenant")`; `AccountRoot` without an account → `ErrInvalidArgument.WithMessage("account-root user must belong to an account")`, else `Type = IdentityRoot`; an account user's display name passes `validateIAMUserName` (non-empty, ≤ 64, `^[\w+=,.@-]+$`) else `ErrInvalidArgument` with radosgw's message; `Path` default `/`; `CreateDate = now`. Then the key when `Key.hasKeyOp()` (Task 5's `addKey`, called with `deferUpdate`), the caps (`Caps.AddString`, `ErrInvalidCapability` on `meta.ErrInvalidCap`), then `PutUser(rec, {Exclusive: true})` (radosgw writes non-exclusively after its own lookup; exclusive here closes the race and yields the same `UserAlreadyExists`), then `Env.Accounts.AddAccountUser(accountID, info)` when the user has an account ([svc_user_rados.cc:360-375](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_user_rados.cc#L360-L375)). `Result = info`.
- **ModifyUser.Execute** (`execute_modify`, [:2017-2228](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_user.cc#L2017-L2228)) — `lookupUser(uid)`; not found → `ErrNoSuchUser`; anonymous → `ErrAccessDenied`; email: non-empty and changed → `GetUserByEmail`; found for another uid → `ErrEmailExists`; empty with `Email != nil` → cleared; display name when non-empty; `MaxBuckets`, `System`, `OpMask`, quotas, `Suspended` (and every bucket of the user: `ListUserBuckets` in chunks of `rgw_list_buckets_max_chunk`, `GetBucket`, flag `meta.BucketSuspended` set or cleared, `PutBucketInfo`; a failing bucket is logged and the loop goes on, the last error returned — [rgw_rados.cc:5332-5368](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5332-L5368)), placement/tags, `AccountID` (validate; changing a non-empty account → `ErrInvalidArgument.WithMessage("users cannot be moved out of their account")`; joining: tenant check, `GetAccount`, then `Env.BucketAdmin.ChownBucket` for every bucket of the user to `meta.AccountOwner(id)` with the account's name, `ErrNoSuchBucket` skipped — [rgw_user.cc:1714-1742](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_user.cc#L1714-L1742); then `AddAccountUser`), `AccountRoot` (needs an account) → `Type`, IAM name check for account users, `Path`, then the key op when any, then `PutUser(rec, {IfVersion: &rec.Version})`; `ErrConcurrentModification` is returned as is (409).
- **GetUserInfo.Execute** (`RGWUserAdminOp_User::info`, [:2347-2408](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_user.cc#L2347-L2408)) — uid and access key both empty → `ErrInvalidArgument` ([rgw_rest_user.cc:106-109](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rest_user.cc#L106-L109)); `lookupUser`; not found → `ErrNoSuchUser`; owner = the account when `AccountID` set; `SyncStats` → `Env.BucketAdmin.SyncOwnerStats(owner)`; `FetchStats` → `Env.Stats.UserStats(owner)` (a not-found error is zero stats). `VerifyPermission` is the request-dependent pair: `user-info-without-keys=read` OR `users=read` (`CheckCaps` twice); the handler sets `DumpKeys = Caps.Check("users", CapRead) || Identity.Admin` ([rgw_rest_user.cc:124-128](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rest_user.cc#L124-L128); Squid's `is_admin_of` is `admin || system`, [rgw_auth.cc:1048-1051](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth.cc#L1048-L1051), so no release gate).
- **RemoveUser.Execute** (`execute_remove`, [:1940-1995](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_user.cc#L1940-L1995)) — `GetUser`; missing → `ErrNoSuchUser`; buckets: `ListUserBuckets` pages; any bucket and `!PurgeData` → `ErrBucketAlreadyExists` (radosgw's `-EEXIST`, 409 "BucketAlreadyExists"); with purge, `DeleteBucketWithChildren(purge=true)` per bucket; then `RemoveUser(rec)`; then `RemoveAccountUser(accountID, displayName)` when the user had an account ([svc_user_rados.cc:602-608](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_user_rados.cc#L602-L608)). Note M's `RemoveUser` removes the indexes and `<uid>.buckets`.
- **ListUsers.Execute** (`RGWUser::list`, [:2276-2329](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_user.cc#L2276-L2329)) — `MaxEntries` above 1000 → 1000; `Env.Metadata.List("user", Marker, left)` in a loop while truncated and `left > 0`, collecting keys; `NextMarker` = the last `next` when truncated (raw, not base64 — `user?list` differs from `metadata list` here).
- **PurgeBucket** (`RadosBucket::remove` with `delete_children`, [rgw_sal_rados.cc:350-468](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L350-L468); v20.2.4 [:367-489](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L367-L489)) — page `ListObjects{ListVersions: true, AllowUnordered: true, MaxKeys: 1000}` over the plain namespace from an empty marker, `DeleteObject(rec, key, DeleteParams{})` per entry (an `ErrNoSuchKey` is skipped, as `-ENOENT` is, :385-392); then `DeleteBucket` (N-D9: it aborts the uploads, so the multipart namespace is never deleted object by object, and it checks the index is empty where radosgw's `delete_bucket` does not, a difference Step 5 records). An `ErrNotImplemented` from any store is returned unchanged (the real driver until W/P land).

The handler for each route builds the op from `Args` exactly as the `RGWOp_User_*::execute` bodies do (parameter names, defaults, and the "only when the argument exists" rules: `suspended`, `system`, `account-root`, `max-buckets` when it differs from `rgw_user_max_buckets` on create and when present on modify; `email` on modify only when present; `generate-key` default true on create, false on modify; `op-mask` parse failure → `ErrInvalidArgument`; `default-placement`: on Tentacle `Name` = the value and `StorageClass` = `default-storage-class`, on Squid `meta.ParsePlacementRule(value)` (`from_str` splits on `/`); an unknown placement (`Env.Zone.Placement(rule)` fails) → `ErrInvalidArgument`; `placement-tags` split on `,`; a non-system caller setting `system=true` → `ErrInvalidArgument`). Responses, all flusher-started as radosgw's `RGWUserAdminOp_User::create`/`modify`/`info` start theirs ([rgw_user.cc:2400-2405](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_user.cc#L2400-L2405), [:2435-2438](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_user.cc#L2435-L2438), [:2468-2471](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_user.cc#L2468-L2471)): create/modify → 200 with `dumpUserInfo(result, true, nil, rel)`; info → 200 with `dumpUserInfo(result, DumpKeys, stats, rel)`; remove → 200 empty. `list_user` is rendered by Task 9.

- [ ] **Step 1: Write the failing op specs**

`internal/op/adminuser_test.go` on `memstore` (users `admin` with `users=*`, an account `RGW00000000000000001` tenant `t1` name `acme`):

```go
var _ = Describe("admin user ops", func() {
	// env, store, run(o) helpers; run builds the Request with the admin identity
	It("creates a user with radosgw's defaults and reports it", func(ctx SpecContext) {
		o := op.NewCreateUser()
		o.UID = meta.UserID{ID: "leseb"}
		o.DisplayName = "This is leseb"
		o.Email = "leseb@example.com"
		o.Caps = "users=read"
		o.OpMask = ptr(uint32(0x4))
		o.Key.GenerateKey = true
		o.PlacementTags = []string{"fast", "ssd"}
		Expect(run(ctx, o)).To(Succeed())
		Expect(o.Result.MaxBuckets).To(Equal(int32(1000)))
		Expect(o.Result.Caps).To(Equal(meta.Caps{"users": meta.CapRead}))
		Expect(o.Result.OpMask).To(Equal(uint32(0x4)))
		Expect(o.Result.AccessKeys).To(HaveLen(1))
		for id, k := range o.Result.AccessKeys {
			Expect(id).To(HaveLen(20))
			Expect(id).To(MatchRegexp(`^[A-Z0-9]{20}$`))
			Expect(k.Secret).To(HaveLen(40))
			Expect(k.Active).To(BeTrue())
		}
		Expect(o.Result.Type).To(Equal(meta.IdentityRGW))
		Expect(o.Result.Path).To(Equal("/"))
		Expect(o.Result.CreateDate.IsZero()).To(BeFalse())
		rec, err := store.GetUser(ctx, o.UID)
		Expect(err).NotTo(HaveOccurred())
		Expect(rec.Info.PlacementTags).To(Equal([]string{"fast", "ssd"}))
	})
	It("refuses duplicates the way radosgw names them", func(ctx SpecContext) {
		create("leseb", "leseb@example.com", "AKIA1", "s1")
		Expect(run(ctx, newCreate("leseb", "x@y", "", ""))).To(MatchError(op.ErrUserAlreadyExists))
		Expect(run(ctx, newCreate("other", "leseb@example.com", "", ""))).To(MatchError(op.ErrEmailExists))
		Expect(run(ctx, newCreate("other2", "", "AKIA1", "s2"))).To(MatchError(op.ErrKeyExists))
		Expect(run(ctx, newCreate("", "", "", ""))).To(MatchError(op.ErrInvalidArgument), "anonymous/empty uid")
		o := newCreate("nodisplay", "", "", "")
		o.DisplayName = ""
		Expect(run(ctx, o)).To(MatchError(op.ErrInvalidArgument))
		o = newCreate("bad", "", "", "")
		o.UID.Tenant = "te-nant"
		Expect(run(ctx, o)).To(MatchError(op.ErrInvalidTenantName))
		o = newCreate("RGW00000000000000009", "", "", "")
		Expect(run(ctx, o)).To(MatchError(op.ErrInvalidArgument), "uid shaped like an account id")
	})
	It("requires a secret for an explicit key and rejects bad caps", func(ctx SpecContext) {
		o := newCreate("k", "", "AKIA2", "")
		o.Key.GenerateKey = false
		Expect(run(ctx, o)).To(MatchError(op.ErrInvalidSecretKey))
		o = newCreate("c", "", "", "")
		o.Caps = "kittens=read"
		Expect(run(ctx, o)).To(MatchError(op.ErrInvalidCapability))
	})
	It("joins an account with a matching tenant and indexes the user there", func(ctx SpecContext) {
		o := newCreate("root1", "", "", "")
		o.UID.Tenant = "t1"
		o.AccountID = "RGW00000000000000001"
		o.AccountRoot = ptr(true)
		Expect(run(ctx, o)).To(Succeed())
		Expect(o.Result.Type).To(Equal(meta.IdentityRoot))
		ids, _, err := store.ListAccountUsers(ctx, "RGW00000000000000001", "", 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(ids).To(ConsistOf("root1"))
		bad := newCreate("root2", "", "", "")
		bad.AccountID = "RGW00000000000000001" // tenant "" != "t1"
		Expect(run(ctx, bad)).To(MatchError(op.ErrInvalidArgument))
		bad = newCreate("root3", "", "", "")
		bad.AccountRoot = ptr(true)
		Expect(run(ctx, bad)).To(MatchError(op.ErrInvalidArgument), "account-root needs an account")
		bad = newCreate("root4", "", "", "")
		bad.UID.Tenant = "t1"
		bad.AccountID = "RGW00000000000000001"
		bad.DisplayName = "has space"
		Expect(run(ctx, bad)).To(MatchError(op.ErrInvalidArgument), "IAM user name")
	})
	It("modifies only what the request names", func(ctx SpecContext) {
		create("leseb", "leseb@example.com", "", "")
		m := op.NewModifyUser()
		m.UID = meta.UserID{ID: "leseb"}
		m.Email = ptr("leseb@leseb.com")
		Expect(run(ctx, m)).To(Succeed())
		Expect(m.Result.Email).To(Equal("leseb@leseb.com"))
		Expect(m.Result.DisplayName).To(Equal("This is leseb"), "untouched")
		m = op.NewModifyUser()
		m.UID = meta.UserID{ID: "leseb"}
		m.MaxBuckets = ptr(int32(-1))
		Expect(run(ctx, m)).To(Succeed())
		Expect(m.Result.MaxBuckets).To(Equal(int32(-1)))
		m = op.NewModifyUser()
		m.UID = meta.UserID{ID: "nosuch"}
		Expect(run(ctx, m)).To(MatchError(op.ErrNoSuchUser))
		create("other", "other@example.com", "", "")
		m = op.NewModifyUser()
		m.UID = meta.UserID{ID: "leseb"}
		m.Email = ptr("other@example.com")
		Expect(run(ctx, m)).To(MatchError(op.ErrEmailExists))
	})
	It("suspends the user's buckets with the user", func(ctx SpecContext) {
		create("leseb", "", "", "")
		_, err := store.CreateBucket(ctx, op.CreateBucketParams{Name: "b1", Owner: meta.UserOwner(meta.UserID{ID: "leseb"})})
		Expect(err).NotTo(HaveOccurred())
		m := op.NewModifyUser()
		m.UID = meta.UserID{ID: "leseb"}
		m.Suspended = ptr(true)
		Expect(run(ctx, m)).To(Succeed())
		rec, _ := store.GetBucket(ctx, "", "b1")
		Expect(rec.Info.Flags & meta.BucketSuspended).NotTo(BeZero())
		m.Suspended = ptr(false)
		Expect(run(ctx, m)).To(Succeed())
		rec, _ = store.GetBucket(ctx, "", "b1")
		Expect(rec.Info.Flags & meta.BucketSuspended).To(BeZero())
	})
	It("refuses to leave an account and adopts buckets when joining one", func(ctx SpecContext) {
		create("u", "", "", "")
		_, _ = store.CreateBucket(ctx, op.CreateBucketParams{Name: "ub", Owner: meta.UserOwner(meta.UserID{ID: "u"})})
		// joining
		m := op.NewModifyUser()
		m.UID = meta.UserID{ID: "u"}
		m.AccountID = "RGW00000000000000002" // an account in the "" tenant
		Expect(run(ctx, m)).To(Succeed())
		rec, _ := store.GetBucket(ctx, "", "ub")
		Expect(rec.Info.Owner).To(Equal(meta.AccountOwner("RGW00000000000000002")))
		// leaving
		m = op.NewModifyUser()
		m.UID = meta.UserID{ID: "u"}
		m.AccountID = "RGW00000000000000003"
		Expect(run(ctx, m)).To(MatchError(op.ErrInvalidArgument))
	})
	It("reads a user by uid or access key with stats", func(ctx SpecContext) {
		create("leseb", "", "AKIA3", "s3")
		g := op.NewGetUserInfo()
		g.AccessKey = "AKIA3"
		g.FetchStats = true
		Expect(run(ctx, g)).To(Succeed())
		Expect(g.Result.UserID.ID).To(Equal("leseb"))
		Expect(g.Stats).NotTo(BeNil())
		g = op.NewGetUserInfo()
		Expect(run(ctx, g)).To(MatchError(op.ErrInvalidArgument), "neither uid nor access-key")
		g = op.NewGetUserInfo()
		g.UID = meta.UserID{ID: "nosuch"}
		Expect(run(ctx, g)).To(MatchError(op.ErrNoSuchUser))
	})
	It("accepts user-info-without-keys=read for info only", func(ctx SpecContext) {
		create("leseb", "", "", "")
		g := op.NewGetUserInfo()
		g.UID = meta.UserID{ID: "leseb"}
		Expect(runAs(ctx, g, meta.Caps{"user-info-without-keys": meta.CapRead})).To(Succeed())
		Expect(runAs(ctx, op.NewCreateUser(), meta.Caps{"user-info-without-keys": meta.CapRead})).To(MatchError(op.ErrAccessDenied))
	})
	It("removes a user, refusing when buckets remain unless purging", func(ctx SpecContext) {
		create("leseb", "", "", "")
		_, _ = store.CreateBucket(ctx, op.CreateBucketParams{Name: "b1", Owner: meta.UserOwner(meta.UserID{ID: "leseb"})})
		d := op.NewRemoveUser()
		d.UID = meta.UserID{ID: "leseb"}
		Expect(run(ctx, d)).To(MatchError(op.ErrBucketAlreadyExists))
		d.PurgeData = true
		Expect(run(ctx, d)).To(Succeed())
		_, err := store.GetUser(ctx, d.UID)
		Expect(err).To(MatchError(op.ErrNoSuchUser))
		_, err = store.GetBucket(ctx, "", "b1")
		Expect(err).To(MatchError(op.ErrNoSuchBucket))
		Expect(run(ctx, d)).To(MatchError(op.ErrNoSuchUser))
	})
	It("lists users through the metadata section with a 1000 cap", func(ctx SpecContext) {
		create("a", "", "", "")
		create("b", "", "", "")
		l := op.NewListUsers()
		l.MaxEntries = 5000
		Expect(run(ctx, l)).To(Succeed())
		Expect(l.Keys).To(ConsistOf("admin", "a", "b"))
		Expect(l.Truncated).To(BeFalse())
		l = op.NewListUsers()
		l.MaxEntries = 1
		Expect(run(ctx, l)).To(Succeed())
		Expect(l.Keys).To(HaveLen(1))
		Expect(l.Truncated).To(BeTrue())
		Expect(l.NextMarker).NotTo(BeEmpty())
	})
})
```

- [ ] **Step 2: Implement `adminuser.go`, `adminpurge.go`, `meta.ValidAccountID`**

Structure: `lookupUser`, `validateTenant` (`meta.ErrInvalidTenantName` mapping), `validateIAMUserName`, `defaultQuota(conf, prefix)` (reads `rgw_<prefix>_default_quota_max_objects`/`_size` as `Int64`, `Enabled = objs >= 0 || size >= 0`), `applyPlacement`, `suspendBuckets`, `adoptBuckets`, and the five ops as specified. `CreateUser`/`ModifyUser` call Task 5's `addKey(ctx, env, rec, &info, params)` — in this task write `addKey` as a stub in `adminuser_sub.go` that handles only `GenerateKey` with no explicit id (the create path the spec needs) and returns `ErrNotImplemented` otherwise; Task 5 completes it. Key generation: `genAccessKey` = 20 bytes from `crypto/rand` mapped onto `A-Z0-9`, retried while `GetUserByAccessKey` finds it (rgw_user.cc:508-533); `genSecretKey` = 40 bytes onto `A-Za-z0-9` (`gen_rand_alphanumeric_plain`). `PurgeBucket` as specified. Run the op suite: PASS.

- [ ] **Step 3: Write the failing handler specs**

`internal/admin/user_test.go` drives the go-ceph and Rook request shapes through `httptest` (the suite's `admin` identity holds `users=*`):

```go
It("PUT /admin/user creates and returns dump_user_info", func() {
	res := do("PUT", "/admin/user?format=json&uid=leseb&display-name=This+is+leseb&email=leseb%40example.com&user-caps=users%3Dread&op-mask=delete&default-placement=default-placement&placement-tags=fast%2Cssd", "admin")
	Expect(res.StatusCode).To(Equal(200))
	var u map[string]any
	Expect(json.Unmarshal(body(res), &u)).To(Succeed())
	Expect(u).To(HaveKeyWithValue("tenant", ""))
	Expect(u).To(HaveKeyWithValue("user_id", "leseb"))
	Expect(u).To(HaveKeyWithValue("op_mask", "delete"))
	Expect(u).To(HaveKeyWithValue("system", false))
	Expect(u).To(HaveKeyWithValue("admin", false))
	Expect(u["caps"]).To(Equal([]any{map[string]any{"type": "users", "perm": "read"}}))
	Expect(u["placement_tags"]).To(Equal([]any{"fast", "ssd"}))
	Expect(u["keys"]).To(HaveLen(1))
	key := u["keys"].([]any)[0].(map[string]any)
	Expect(key).To(HaveKey("user"))
	Expect(key).To(HaveKey("access_key"))
	Expect(key).To(HaveKey("secret_key"))
	Expect(key).To(HaveKeyWithValue("active", true))
	Expect(key).NotTo(HaveKey("create_date"), "dump_access_keys_info has no create_date")
	Expect(u).NotTo(HaveKey("full_user_id"), "Squid")
	keys := body(res)
	Expect(bytes.Index(keys, []byte(`"tenant"`))).To(BeNumerically("<", bytes.Index(keys, []byte(`"user_id"`))), "radosgw's field order")
})
It("renders the same document in XML with the element names JSON drops", func() {
	res := do("PUT", "/admin/user?format=xml&uid=x1&display-name=X&generate-key=false", "admin")
	Expect(res.StatusCode).To(Equal(200))
	Expect(res.Header.Get("Content-Type")).To(Equal("application/xml"))
	b := string(body(res))
	Expect(b).To(MatchRegexp(`^<\?xml version="1\.0" encoding="UTF-8"\?><user_info><tenant></tenant><user_id>x1</user_id><display_name>X</display_name><email></email><suspended>0</suspended><max_buckets>1000</max_buckets><subusers></subusers><keys></keys><swift_keys></swift_keys><caps></caps><op_mask>read, write, delete</op_mask><system>false</system><admin>false</admin><default_placement></default_placement><default_storage_class></default_storage_class><placement_tags></placement_tags><bucket_quota><enabled>false</enabled><check_on_raw>false</check_on_raw><max_size>-1</max_size><max_size_kb>0</max_size_kb><max_objects>-1</max_objects></bucket_quota><user_quota><enabled>false</enabled><check_on_raw>false</check_on_raw><max_size>-1</max_size><max_size_kb>0</max_size_kb><max_objects>-1</max_objects></user_quota><temp_url_keys></temp_url_keys><type>rgw</type><mfa_ids></mfa_ids><account_id></account_id><path>/</path><create_date>\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{6}Z</create_date><tags></tags><group_ids></group_ids></user_info>$`))
	res = do("PUT", "/admin/user?format=xml&uid=x2&display-name=Y&user-caps=users%3Dread", "admin")
	Expect(string(body(res))).To(ContainSubstring(`<keys><key><user>x2</user><access_key>`))
	Expect(string(body(res))).To(ContainSubstring(`<caps><cap><type>users</type><perm>read</perm></cap></caps>`))
})
It("writes full_user_id first and namespace after tenant on Tentacle", func() {
	useRelease(denc.Tentacle)
	res := do("PUT", "/admin/user?uid=t%24u2&display-name=d", "admin")
	b := body(res)
	var u map[string]any
	Expect(json.Unmarshal(b, &u)).To(Succeed())
	Expect(u).To(HaveKeyWithValue("full_user_id", "t$u2"))
	Expect(u).NotTo(HaveKey("namespace"))
	Expect(string(b)).To(HavePrefix(`{"full_user_id":"t$u2","tenant":"t","user_id":"u2",`), "v20.2.4 rgw_user.cc:136-142")
})
It("GET /admin/user reads by uid or access-key and hides keys without users=read", func() {
	// create leseb, then GET ?uid=leseb → keys present; as a user holding only user-info-without-keys=read → no "keys"/"swift_keys"; ?access-key=<ak> → same user; ?stats=true → "stats" with size, size_actual, size_utilized, size_kb, size_kb_actual, size_kb_utilized, num_objects in that order; no uid → 400 InvalidArgument; unknown → 404 NoSuchUser
})
It("POST /admin/user modifies and honours the Squid/Tentacle placement encodings", func() {
	// Squid: default-placement=default-placement%2FFOO → default_placement "default-placement", default_storage_class "FOO"
	// Tentacle: default-placement=default-placement&default-storage-class=FOO → the same; max-buckets=-1 → -1; email change; placement-tags persisted (GET shows them)
})
It("DELETE /admin/user removes, 409 with buckets unless purge-data", func() {
	// create leseb with bucket b1; DELETE /admin/user?uid=leseb → 409 BucketAlreadyExists; DELETE …&purge-data=true → 200, Content-Length 0, empty body; GET ?uid=leseb → 404 NoSuchUser
})
It("409 UserAlreadyExists for an existing uid", func() {
	res := do("PUT", "/admin/user?uid=admin&display-name=Admin+user", "admin")
	Expect(res.StatusCode).To(Equal(409))
	Expect(errCode(res)).To(Equal("UserAlreadyExists"))
})
It("rejects system=true from a non-system caller", func() {
	Expect(do("PUT", "/admin/user?uid=s&display-name=s&system=true", "admin").StatusCode).To(Equal(400))
})
```

`useRelease` swaps the memstore's `Config.Release`; `errCode` decodes the error document. Write the three commented bodies in full.

- [ ] **Step 4: Implement `internal/admin/user.go` and `Caps.DumpAs`**

`newUserHandlers()` returns the four handlers; each parses its arguments exactly as listed in the task preamble, runs the op and renders through `WriteBody` — create and modify with `dumpUserInfo(f, o.Result, true, nil, rel)`, info with `dumpUserInfo(f, o.Result, o.DumpKeys, o.Stats, rel)` (`rel = r.Env.Zone.Release()`) — or `WriteEmpty(w, r, 200)` for remove.

```go
func dumpUserInfo(f formatter.Formatter, info meta.UserInfo, dumpKeys bool, stats *op.Stats, rel denc.Release) {
	f.OpenObjectSection("user_info")
	if rel >= denc.Tentacle {
		f.DumpString("full_user_id", info.UserID.String()) // v20.2.4 rgw_user.cc:137
	}
	f.DumpString("tenant", info.UserID.Tenant)
	if rel >= denc.Tentacle && info.UserID.NS != "" {
		f.DumpString("namespace", info.UserID.NS)
	}
	f.DumpString("user_id", info.UserID.ID)
	f.DumpString("display_name", info.DisplayName)
	f.DumpString("email", info.Email)
	f.DumpInt("suspended", int64(info.Suspended))
	f.DumpInt("max_buckets", int64(info.MaxBuckets))
	dumpSubusers(f, info)
	if dumpKeys {
		dumpAccessKeys(f, info)
		dumpSwiftKeys(f, info)
	}
	info.Caps.DumpAs(f, "caps")
	f.DumpString("op_mask", meta.OpTypeString(info.OpMask))
	f.DumpBool("system", info.System != 0)
	f.DumpBool("admin", info.Admin != 0)
	f.DumpString("default_placement", info.DefaultPlacement.Name)
	f.DumpString("default_storage_class", info.DefaultPlacement.StorageClass)
	dumpObjs(f, "placement_tags", info.PlacementTags)
	f.OpenObjectSection("bucket_quota")
	info.BucketQuota.Dump(f, rel)
	f.CloseSection()
	f.OpenObjectSection("user_quota")
	info.UserQuota.Dump(f, rel)
	f.CloseSection()
	// std::map<int, std::string>: "entry" sections of "key" and "val" (ceph_json.h:597-608).
	f.OpenArraySection("temp_url_keys")
	for _, k := range slices.Sorted(maps.Keys(info.TempURLKeys)) {
		f.OpenObjectSection("entry")
		f.DumpInt("key", int64(k))
		f.DumpString("val", info.TempURLKeys[k])
		f.CloseSection()
	}
	f.CloseSection()
	f.DumpString("type", info.Type.DumpName())
	dumpObjs(f, "mfa_ids", slices.Compact(slices.Sorted(slices.Values(info.MFAIDs))))
	f.DumpString("account_id", info.AccountID)
	f.DumpString("path", info.Path)
	f.DumpStream("create_date", info.CreateDate.Gmtime())
	// std::multimap<string, string>: key order, equal keys as inserted.
	tags := slices.Clone(info.Tags)
	slices.SortStableFunc(tags, func(a, b meta.UserTag) int { return cmp.Compare(a.Key, b.Key) })
	f.OpenArraySection("tags")
	for _, t := range tags {
		f.OpenObjectSection("entry")
		f.DumpString("key", t.Key)
		f.DumpString("val", t.Value)
		f.CloseSection()
	}
	f.CloseSection()
	dumpObjs(f, "group_ids", slices.Compact(slices.Sorted(slices.Values(info.GroupIDs))))
	if stats != nil {
		f.OpenObjectSection("stats")
		dumpStats(f, *stats)
		f.CloseSection()
	}
	f.CloseSection()
}

// dumpObjs is encode_json of a list, vector or set of strings: entries named
// "obj" (ceph_json.h:534-594).
func dumpObjs(f formatter.Formatter, name string, v []string) {
	f.OpenArraySection(name)
	for _, s := range v {
		f.DumpString("obj", s)
	}
	f.CloseSection()
}

func dumpSubusers(f formatter.Formatter, info meta.UserInfo) {
	f.OpenArraySection("subusers")
	for _, name := range slices.Sorted(maps.Keys(info.SubUsers)) {
		f.OpenObjectSection("user")
		f.DumpString("id", info.UserID.String()+":"+info.SubUsers[name].Name)
		f.DumpString("permissions", meta.PermString(info.SubUsers[name].Perm))
		f.CloseSection()
	}
	f.CloseSection()
}

// keyUser is dump_format("user", "%s%s%s", uid, sep, subuser) (rgw_user.cc:99-104).
func keyUser(info meta.UserInfo, k meta.AccessKey) string {
	if k.Subuser == "" {
		return info.UserID.String()
	}
	return info.UserID.String() + ":" + k.Subuser
}

func dumpAccessKeys(f formatter.Formatter, info meta.UserInfo) {
	f.OpenArraySection("keys")
	for _, id := range slices.Sorted(maps.Keys(info.AccessKeys)) {
		k := info.AccessKeys[id]
		f.OpenObjectSection("key")
		f.DumpString("user", keyUser(info, k))
		f.DumpString("access_key", k.ID)
		f.DumpString("secret_key", k.Secret)
		f.DumpBool("active", k.Active)
		f.CloseSection()
	}
	f.CloseSection()
}

func dumpSwiftKeys(f formatter.Formatter, info meta.UserInfo) {
	f.OpenArraySection("swift_keys")
	for _, id := range slices.Sorted(maps.Keys(info.SwiftKeys)) {
		k := info.SwiftKeys[id]
		f.OpenObjectSection("key")
		f.DumpString("user", keyUser(info, k))
		f.DumpString("secret_key", k.Secret)
		f.DumpBool("active", k.Active)
		f.CloseSection()
	}
	f.CloseSection()
}

// dumpStats writes op.Stats as RGWStorageStats::dump with dump_utilized true;
// user stats carry no utilized size, which radosgw's load_stats leaves 0.
func dumpStats(f formatter.Formatter, s op.Stats) {
	f.DumpUnsigned("size", s.Size)
	f.DumpUnsigned("size_actual", s.SizeRounded)
	f.DumpUnsigned("size_utilized", 0)
	f.DumpUnsigned("size_kb", (s.Size+1023)/1024) // rgw_rounded_kb
	f.DumpUnsigned("size_kb_actual", (s.SizeRounded+1023)/1024)
	f.DumpUnsigned("size_kb_utilized", 0)
	f.DumpUnsigned("num_objects", s.NumObjects)
}
```

`internal/meta/dump.go` gains:

```go
func (c Caps) DumpAs(f formatter.Formatter, name string) {
	f.OpenArraySection(name)
	for _, typ := range slices.Sorted(maps.Keys(c)) {
		f.OpenObjectSection("cap")
		f.DumpString("type", typ)
		f.DumpString("perm", capPermString(c[typ]))
		f.CloseSection()
	}
	f.CloseSection()
}

func (c Caps) Dump(f formatter.Formatter, _ denc.Release) { c.DumpAs(f, "caps") }
```

(`capPermString` is phase 0's rendering of `cap_names`, "<none>" for no bits.) `meta.IdentityType.DumpName` renames `dumpName`, its callers updated. `dump_goldens_test.go` gains `It("RGWUserCaps", func() { dumpGoldens("RGWUserCaps", meta.DecodeCaps) })` — ceph-dencoder prints `RGWUserCaps::dump(f)`, the "caps" array inside the "object" section. Run the admin and meta suites: PASS.

- [ ] **Step 5: Record the purge difference**

`docs/exclusions.md`, "Coexistence obligations independent of any exclusion", a new bullet after the admin headers bullet:

"- **A purging bucket removal checks the index is empty at the end.** Removing a bucket with purge-objects, or a user or account with purge-data, deletes every listed object and then removes the bucket through the same path as S3 DeleteBucket, which refuses a bucket that still holds an object. radosgw removes the bucket at that point without checking, so an object written between its listing and the removal is left without a bucket; rgw-go answers 409 BucketNotEmpty instead, and a retry purges it."

Report the change for the rgw-rs session.

- [ ] **Step 6: `make check`; commit**

```bash
git add internal/op/adminuser.go internal/op/adminuser_test.go internal/op/adminpurge.go internal/admin/user.go internal/admin/user_test.go internal/meta/owner.go internal/meta/user.go internal/meta/dump.go internal/meta/dump_goldens_test.go docs/exclusions.md
git commit -m "feat(admin): user create, info, modify and remove with radosgw's user document"
```

## Task 5: Keys, subusers, caps and quota

**Files:**
- Create: `internal/op/adminuser_sub.go`, `internal/op/adminuser_sub_test.go`, `internal/admin/user_sub.go`, `internal/admin/user_sub_test.go`, `internal/meta/quota_json.go` (`Quota.UnmarshalJSON`), `internal/meta/quota_json_test.go`
- Modify: `internal/admin/handler.go` (`newUserSubHandlers()`), `internal/op/adminuser.go` (the `addKey` stub replaced)

**Interfaces:**
- Consumes: Task 4's ops, `lookupUser`, `genAccessKey`, `genSecretKey`, `UserKeyParams`; G's `op.UserStore.PutUser`; `op.ErrInvalidAccessKeyID`, `op.ErrInvalidSecretKey`, `op.ErrInvalidKeyType`, `op.ErrKeyExists`, `op.ErrNoSuchSubUser`, `op.ErrInvalidCapability`, `op.ErrInvalidArgument`, `op.ErrMissingContentLength`; Task 1's `meta.ParseSubuserPerm`, `meta.PermInvalid`, `meta.Caps.AddString/RemoveString`; Task 3's `WriteBody`, `WriteEmpty`, `meta.Quota.Dump`; Task 4's `dumpAccessKeys`, `dumpSwiftKeys`, `dumpSubusers`, `meta.Caps.DumpAs`.
- Produces:

```go
package meta

// UnmarshalJSON is RGWQuotaInfo::decode_json: max_size, or max_size_kb * 1024
// when max_size is absent; max_objects; enabled; check_on_raw.
func (q *Quota) UnmarshalJSON(b []byte) error
```

```go
package op

// addKey is RGWAccessKeyPool::add (rgw_user.cc:457-777): check_op picks the
// type (swift with a subuser, else s3), demands an access key for s3 unless
// generating one, then generate_key for a new key or modify_key for one the
// user already holds. It mutates info; the caller writes it.
func addKey(ctx context.Context, env *Env, info *meta.UserInfo, p UserKeyParams) error
// removeKey is RGWAccessKeyPool::remove (:779-853): a key the user does not hold is ErrInvalidAccessKeyID.
func removeKey(info *meta.UserInfo, p UserKeyParams) error

// CreateKey is RGWOp_Key_Create + RGWUserAdminOp_Key::create (rgw_rest_user.cc:666-726; rgw_user.cc:2586-2625). Cap users=write.
type CreateKey struct {
	AdminOp
	UID    meta.UserID
	Key    UserKeyParams // GenerateKey default true
	Result meta.UserInfo // the handler renders keys or swift_keys by Key.Type
	Type   KeyType       // the type check_op settled on
}
// RemoveKey is RGWOp_Key_Remove (:728-773; rgw_user.cc:2627-2648).
type RemoveKey struct {
	AdminOp
	UID meta.UserID
	Key UserKeyParams
}
// CreateSubuser, ModifySubuser, RemoveSubuser are RGWOp_Subuser_* (:481-664) over
// RGWSubUserPool (rgw_user.cc:925-1222). Caps users=write.
type CreateSubuser struct {
	AdminOp
	UID     meta.UserID
	Subuser string
	Perm    string // "access": "", read, write, readwrite, full
	Key     UserKeyParams // access-key, secret-key, key-type (default swift), generate-secret, gen-access-key
	Result  meta.UserInfo
}
type ModifySubuser struct {
	AdminOp
	UID     meta.UserID
	Subuser string
	Perm    *string
	Key     UserKeyParams // secret-key (only when non-empty), key-type (default swift), generate-secret
	Result  meta.UserInfo
}
type RemoveSubuser struct {
	AdminOp
	UID       meta.UserID
	Subuser   string
	PurgeKeys bool // default true; radosgw's execute_remove purges regardless
}
// AddCaps and RemoveCaps are RGWOp_Caps_* (:775-849; rgw_user.cc:1257-1340). Caps users=write.
type AddCaps struct {
	AdminOp
	UID    meta.UserID
	Caps   string
	Result meta.Caps
}
type RemoveCaps struct{ AddCaps }
// GetUserQuota is RGWOp_Quota_Info (:871-941). Cap users=read.
type GetUserQuota struct {
	AdminOp
	UID       meta.UserID
	QuotaType string // "", "user", "bucket"
	Result    meta.UserInfo
}
// SetUserQuota is RGWOp_Quota_Set (:943-1127). Cap users=write.
type SetUserQuota struct {
	AdminOp
	UID         meta.UserID
	QuotaType   string
	UserQuota   *meta.Quota
	BucketQuota *meta.Quota
}
```

```go
package admin

// quotaJSON is UserQuotas::decode_json (rgw_rest_user.cc:865-868), the body
// set_quota_info accepts when quota-type is empty.
type quotaJSON struct {
	BucketQuota meta.Quota `json:"bucket_quota"`
	UserQuota   meta.Quota `json:"user_quota"`
}
// dumpQuotaInfo is RGWOp_Quota_Info's body (rgw_rest_user.cc:930-938): a
// "quota" section holding bucket_quota then user_quota for an empty
// quota-type, else the one quota as "user_quota" or "bucket_quota".
func dumpQuotaInfo(f formatter.Formatter, info meta.UserInfo, quotaType string, rel denc.Release)
func newUserSubHandlers() map[string]HandlerFunc // create_access_key, remove_access_key, create_subuser, modify_subuser, remove_subuser, add_user_caps, remove_user_caps, get_quota_info, set_quota_info
```

Semantics (driver/rados/rgw_user.cc at v19.2.6 unless named):

- **check_op** ([rgw_user.cc:457-498](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_user.cc#L457-L498)): type undefined → swift when `Subuser != ""` else s3; s3 with neither an access key nor `GenerateKey` (`will_gen_access`) → `ErrInvalidAccessKeyID`; `check_existing_key` ([:394-455](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_user.cc#L394-L455)): swift → `uid:sub` in `SwiftKeys`; s3 → the id in `AccessKeys`.
- **generate_key** ([:536-639](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_user.cc#L536-L639)): an existing key → `ErrKeyExists`; an explicit id another user holds (`GetUserByAccessKey`) → `ErrKeyExists`; the subuser recorded on the key (`new_key.subuser`) when a subuser is named; secret: empty and not generating → `ErrInvalidSecretKey`; s3 id generated when `gen_access`; swift id is `uid:sub` (empty subuser → `ErrInvalidAccessKeyID`); `CreatedAt = now`; inserted into `AccessKeys` or `SwiftKeys`, `Active = true`.
- **modify_key** ([:642-710](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_user.cc#L642-L710)): s3 needs an id (`ErrInvalidAccessKeyID`), swift needs a subuser (`ErrInvalidArgument`); a key the user does not hold → `ErrInvalidAccessKeyID`; new secret when given or generated; `Active` when specified; `CreatedAt` untouched.
- **set_generate_key** ([rgw_user.h:350-356](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_user.h#L350-L356)): generates the id only when none was given and the secret only when none was given, so `PUT user?key&uid=admin&access-key=X` with `generate-key` defaulting to true yields key `X` with a generated secret — go-ceph's keys_test expects 3 keys after that call.
- **Subuser add** ([:1053-1085](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_user.cc#L1053-L1085) with check_op [:963-1002](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_user.cc#L963-L1002)): empty name → `ErrInvalidArgument`; `ParseSubuserPerm(Perm) == PermInvalid` → `ErrInvalidArgument`; type default swift; an existing key → `ErrKeyExists`; s3 without an access key → generate one; empty secret → generate; `execute_add`: the key when any key op, then `SubUsers[name] = {Name, Perm}`; a second create of the same subuser is not refused by radosgw (it overwrites the perm) — mirror that. Modify ([:1151-1222](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_user.cc#L1151-L1222)): missing → `ErrNoSuchSubUser`; key op when secret given or `generate-secret`; perm when given. Remove ([:1087-1149](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_user.cc#L1087-L1149)): missing → `ErrNoSuchSubUser`; every swift key `uid:sub` and every s3 key with `Subuser == sub` removed; then the subuser.
- **Caps** (:1257-1340): empty string → `ErrInvalidCapability`; `AddString`/`RemoveString` errors → `ErrInvalidCapability`; the response is the caps array.
- **Quota info** ([rgw_rest_user.cc:886-941](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rest_user.cc#L886-L941)): uid required → `ErrInvalidArgument`; `quota-type` other than `""`/`user`/`bucket` → `ErrInvalidArgument`; missing user → `ErrNoSuchUser`; body `dumpQuotaInfo`, flusher-started: JSON drops the top-level name, XML and HTML show it (`quota`, `user_quota` or `bucket_quota`).
- **Quota set** ([:1005-1127](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rest_user.cc#L1005-L1127)): uid required; `quota-type` validated; a body when `Content-Length > 0` or `Transfer-Encoding: chunked` (at most 1024 bytes — more is `ErrInvalidArgument`, `get_json_input`'s `-ERANGE`... radosgw maps ERANGE to 416 `InvalidRange`; use `op.ErrInvalidRange`); with a body and `quota-type ""` the body is `quotaJSON`, else one `meta.Quota`; a chunked request with an EMPTY body falls back to parameters; parameters need a `quota-type` (`""` → `ErrInvalidArgument`) and default to the current values: `max-objects`, `max-size`, `max-size-kb` (`× 1024`, wins over `max-size`), `enabled`; then `ModifyUser` with the quota set. A body that is not JSON → `ErrInvalidArgument`.

- [ ] **Step 1: Write the failing `meta.Quota.UnmarshalJSON` spec**

```go
It("decodes RGWQuotaInfo's JSON, taking max_size_kb when max_size is absent", func() {
	var q meta.Quota
	Expect(json.Unmarshal([]byte(`{"enabled":true,"max_size_kb":4096,"max_objects":-1}`), &q)).To(Succeed())
	Expect(q).To(Equal(meta.Quota{MaxSize: 4096 * 1024, MaxObjects: -1, Enabled: true}))
	Expect(json.Unmarshal([]byte(`{"max_size":10,"max_size_kb":4096,"max_objects":5,"check_on_raw":true}`), &q)).To(Succeed())
	Expect(q.MaxSize).To(Equal(int64(10)))
	Expect(q.CheckOnRaw).To(BeTrue())
})
```

Implement with an auxiliary struct holding `*int64` for `max_size`. PASS.

- [ ] **Step 2: Write the failing op specs**

`internal/op/adminuser_sub_test.go`:

```go
It("generates a key pair, keeps a given access key, and refuses duplicates", func(ctx SpecContext) {
	create("admin2", "", "AKIAIOSFODNN7EXAMPLE", "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY")
	k := op.NewCreateKey()
	k.UID = meta.UserID{ID: "admin2"}
	k.Key.GenerateKey = true
	Expect(run(ctx, k)).To(Succeed())
	Expect(k.Result.AccessKeys).To(HaveLen(2))
	k = op.NewCreateKey()
	k.UID = meta.UserID{ID: "admin2"}
	k.Key.AccessKey = "HDNEZQXZAA6NIWOBOL0U"
	k.Key.GenerateKey = true
	Expect(run(ctx, k)).To(Succeed())
	Expect(k.Result.AccessKeys).To(HaveLen(3))
	Expect(k.Result.AccessKeys["HDNEZQXZAA6NIWOBOL0U"].Secret).To(HaveLen(40))
	k = op.NewCreateKey()
	k.UID = meta.UserID{ID: "admin2"}
	k.Key.AccessKey = "HDNEZQXZAA6NIWOBOL0U"
	k.Key.SecretKey = "new"
	k.Key.GenerateKey = false
	Expect(run(ctx, k)).To(Succeed(), "an existing key is modified, not refused")
	Expect(k.Result.AccessKeys["HDNEZQXZAA6NIWOBOL0U"].Secret).To(Equal("new"))
	create("other", "", "OTHERKEY0000000000AK", "s")
	k = op.NewCreateKey()
	k.UID = meta.UserID{ID: "admin2"}
	k.Key.AccessKey = "OTHERKEY0000000000AK"
	k.Key.SecretKey = "s"
	Expect(run(ctx, k)).To(MatchError(op.ErrKeyExists))
	k = op.NewCreateKey()
	k.UID = meta.UserID{ID: "admin2"}
	k.Key.AccessKey = "X"
	k.Key.GenerateKey = false
	Expect(run(ctx, k)).To(MatchError(op.ErrInvalidSecretKey))
})
It("removes an s3 key and reports a missing one as InvalidAccessKeyId", func(ctx SpecContext) {
	create("u", "", "AK1", "s1")
	d := op.NewRemoveKey()
	d.UID = meta.UserID{ID: "u"}
	d.Key.AccessKey = "AK1"
	Expect(run(ctx, d)).To(Succeed())
	Expect(run(ctx, d)).To(MatchError(op.ErrInvalidAccessKeyID))
})
It("creates, modifies and removes a subuser with its keys", func(ctx SpecContext) {
	create("leseb", "", "", "")
	c := op.NewCreateSubuser()
	c.UID = meta.UserID{ID: "leseb"}
	c.Subuser = "foo"
	c.Perm = "readwrite"
	c.Key = op.UserKeyParams{Type: op.KeyTypeS3, AccessKey: "SUBUSER_ACCESS_KEY", SecretKey: "SUBUSER_SECRET_KEY"}
	Expect(run(ctx, c)).To(Succeed())
	Expect(c.Result.SubUsers["foo"].Perm).To(Equal(uint32(0x3)))
	Expect(c.Result.SwiftKeys).To(BeEmpty())
	Expect(c.Result.AccessKeys["SUBUSER_ACCESS_KEY"].Subuser).To(Equal("foo"))
	m := op.NewModifySubuser()
	m.UID = c.UID
	m.Subuser = "foo"
	m.Perm = ptr("read")
	Expect(run(ctx, m)).To(Succeed())
	Expect(m.Result.SubUsers["foo"].Perm).To(Equal(uint32(0x1)))
	r := op.NewRemoveSubuser()
	r.UID = c.UID
	r.Subuser = "foo"
	Expect(run(ctx, r)).To(Succeed())
	rec, _ := store.GetUser(ctx, c.UID)
	Expect(rec.Info.SubUsers).To(BeEmpty())
	Expect(rec.Info.AccessKeys).NotTo(HaveKey("SUBUSER_ACCESS_KEY"), "subuser keys are purged")
	Expect(run(ctx, r)).To(MatchError(op.ErrNoSuchSubUser))
	c2 := op.NewCreateSubuser()
	c2.UID = c.UID
	c2.Subuser = "swift1"
	c2.Key.GenerateKey = true // swift by default
	Expect(run(ctx, c2)).To(Succeed())
	Expect(c2.Result.SwiftKeys).To(HaveKey("leseb:swift1"))
	bad := op.NewCreateSubuser()
	bad.UID = c.UID
	bad.Subuser = "x"
	bad.Perm = "read-write"
	Expect(run(ctx, bad)).To(MatchError(op.ErrInvalidArgument))
})
It("adds and removes caps, returning the array", func(ctx SpecContext) {
	create("test", "", "", "")
	a := op.NewAddCaps()
	a.UID = meta.UserID{ID: "test"}
	a.Caps = "users=read"
	Expect(run(ctx, a)).To(Succeed())
	Expect(a.Result).To(Equal(meta.Caps{"users": meta.CapRead}))
	r := op.NewRemoveCaps()
	r.UID = a.UID
	r.Caps = "users=read"
	Expect(run(ctx, r)).To(Succeed())
	Expect(r.Result).To(BeEmpty())
	a.Caps = ""
	Expect(run(ctx, a)).To(MatchError(op.ErrInvalidCapability))
})
It("gets and sets the user and bucket quotas", func(ctx SpecContext) {
	create("leseb", "", "", "")
	s := op.NewSetUserQuota()
	s.UID = meta.UserID{ID: "leseb"}
	s.QuotaType = "user"
	s.UserQuota = &meta.Quota{MaxSize: -1, MaxObjects: 100, Enabled: true}
	Expect(run(ctx, s)).To(Succeed())
	g := op.NewGetUserQuota()
	g.UID = s.UID
	Expect(run(ctx, g)).To(Succeed())
	Expect(g.Result.UserQuota.MaxObjects).To(Equal(int64(100)))
	Expect(g.Result.BucketQuota.Enabled).To(BeFalse())
	g.UID = meta.UserID{}
	Expect(run(ctx, g)).To(MatchError(op.ErrInvalidArgument))
	g.UID = meta.UserID{ID: "nosuch"}
	Expect(run(ctx, g)).To(MatchError(op.ErrNoSuchUser))
})
```

- [ ] **Step 3: Implement `internal/op/adminuser_sub.go`**

As specified; `addKey` replaces Task 4's stub; `CreateKey.Execute` records the settled type in `o.Type` for the handler. Every op ends with `PutUser(rec, {IfVersion: &rec.Version})` and sets `Result`. PASS.

- [ ] **Step 4: Write the failing handler specs**

`internal/admin/user_sub_test.go`, the go-ceph and Rook shapes: `PUT /admin/user?key&uid=admin` → 200, body a JSON array of `{"user","access_key","secret_key","active"}` with 2 entries; `PUT /admin/user?key&uid=admin&access-key=HDNEZQXZAA6NIWOBOL0U` → 3 entries; `DELETE /admin/user?key&uid=admin&access-key=…` → 200 empty; `PUT /admin/user?uid=leseb&subuser=foo&access=readwrite&key-type=s3&access-key=SUBUSER_ACCESS_KEY&secret-key=SUBUSER_SECRET_KEY&generate-secret=false&gen-access-key=false` → 200 body `[{"id":"leseb:foo","permissions":"read-write"}]`, and a following `GET /admin/user?uid=leseb` shows `subusers[0].id == "leseb:foo"`, `permissions == "read-write"`, no swift keys, and a key with `user == "leseb:foo"`; `POST /admin/user?uid=leseb&subuser=foo&access=read` → `permissions "read"`; `DELETE /admin/user?uid=leseb&subuser=foo` → 200 and no subusers; `PUT /admin/user?caps&uid=test&user-caps=users%3Dread` → `[{"type":"users","perm":"read"}]`; `DELETE …?caps…` → `[]`; `GET /admin/user?quota&uid=leseb&quota-type=user` → `{"enabled":…,"check_on_raw":…,"max_size":…,"max_size_kb":…,"max_objects":…}`; `PUT /admin/user?quota&uid=leseb&quota-type=user&max-objects=100&enabled=true` (no body) → 200 then GET shows 100; `PUT …?quota&uid=leseb` with body `{"user_quota":{"max_size_kb":4096,"max_objects":-1,"enabled":false},"bucket_quota":{"max_size_kb":1024,"max_objects":-1,"enabled":true}}` → both set; `PUT …?quota&uid=leseb` with no body and no quota-type → 400; `PUT …?key&quota&uid=x` → the key route (first sub-resource wins, Review Focus 2) — assert by the response shape. XML bodies, compared whole: `PUT /admin/user?caps&uid=test&user-caps=users%3Dread&format=xml` → `<?xml version="1.0" encoding="UTF-8"?><caps><cap><type>users</type><perm>read</perm></cap></caps>`; `PUT /admin/user?uid=leseb&subuser=foo&access=readwrite&format=xml` (with the s3 key arguments above) → `<?xml version="1.0" encoding="UTF-8"?><subusers><user><id>leseb:foo</id><permissions>read-write</permissions></user></subusers>`; `GET /admin/user?quota&uid=leseb&quota-type=user&format=xml` after the 100-object set → `<?xml version="1.0" encoding="UTF-8"?><user_quota><enabled>true</enabled><check_on_raw>false</check_on_raw><max_size>-1</max_size><max_size_kb>0</max_size_kb><max_objects>100</max_objects></user_quota>`; `GET /admin/user?quota&uid=leseb&format=xml` → the same document wrapped as `<quota><bucket_quota>…</bucket_quota><user_quota>…</user_quota></quota>`.

- [ ] **Step 5: Implement `internal/admin/user_sub.go`; `make check`; commit**

Handlers per the `RGWOp_*::execute` argument lists (Task preamble). Every body is flusher-started, as `RGWUserAdminOp_*` start theirs, and written with `WriteBody`: key create → `dumpSwiftKeys` for a swift key, `dumpAccessKeys` for s3 (Task 4's; rgw_user.cc:2610-2622); subuser create and modify → `dumpSubusers` ([:2521-2524](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_user.cc#L2521-L2524), [:2554-2557](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_user.cc#L2554-L2557)); caps add and remove → `o.Result.DumpAs(f, "caps")` ([:2675-2678](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_user.cc#L2675-L2678), [:2710-2713](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_user.cc#L2710-L2713)); quota info → `dumpQuotaInfo`; key remove, subuser remove and quota set → `WriteEmpty(w, r, 200)`.

```go
// dumpQuotaInfo is RGWOp_Quota_Info::execute's body (rgw_rest_user.cc:930-938).
func dumpQuotaInfo(f formatter.Formatter, info meta.UserInfo, quotaType string, rel denc.Release) {
	switch quotaType {
	case "":
		f.OpenObjectSection("quota") // UserQuotas::dump, :861-864
		f.OpenObjectSection("bucket_quota")
		info.BucketQuota.Dump(f, rel)
		f.CloseSection()
		f.OpenObjectSection("user_quota")
		info.UserQuota.Dump(f, rel)
		f.CloseSection()
		f.CloseSection()
	case "user":
		f.OpenObjectSection("user_quota")
		info.UserQuota.Dump(f, rel)
		f.CloseSection()
	default:
		f.OpenObjectSection("bucket_quota")
		info.BucketQuota.Dump(f, rel)
		f.CloseSection()
	}
}
```

```bash
git add internal/op/adminuser_sub.go internal/op/adminuser_sub_test.go internal/op/adminuser.go internal/admin/user_sub.go internal/admin/user_sub_test.go internal/meta/quota_json.go internal/meta/quota_json_test.go
git commit -m "feat(admin): user keys, subusers, caps and quota"
```

## Task 6: Bucket admin: driver `BucketAdminStore`; info, list, link, unlink, remove with and without bypass-gc, quota, policy, object remove

**Files:**
- Create: `internal/driver/bucketadmin.go`, `internal/driver/bucketadmin_test.go`, `internal/op/adminbucket.go`, `internal/op/adminbucket_test.go`, `internal/admin/bucket.go`, `internal/admin/bucket_test.go`, `internal/acl/dump.go`, `internal/acl/dump_goldens_test.go`, `internal/meta/dump_bucket.go`
- Modify: `internal/memstore/admin.go` (`IndexStats`, `ChangeBucketOwner`, `UnlinkBucketOwner`, `ChownBucket`, `SyncOwnerStats`, `PurgeBypassGC` implemented), `internal/driver/store.go` (stubs dropped; `PurgeBypassGC`'s stays for Task 14), `internal/op/adminpurge.go` (`RemoveBucketBypassGC`), `internal/meta/dump_goldens_test.go` (`rgw_bucket`), `internal/admin/handler.go` (`newBucketHandlers()`), `docs/exclusions.md` (the tenanted listing bullet, Step 7)

**Interfaces:**
- Consumes: M's driver internals `readShardHeaders`, `readIndexStats`, `linkBucket`, `unlinkBucket`, `readEntryPoint`, `writeEntryPoint`, `removeEntryPoint`, `writeInstance`, `removeInstance`, `PutBucketInfo`, `GetBucket`, `syncAllStats`, `newWriteVersion`, `objv`; `cls/rgw.DirHeader{Ver, MasterVer, Stats map[uint8]CategoryStats, MaxMarker}` and `CategoryStats{TotalSize, TotalSizeRounded, ActualSize, NumEntries}` (confirm the field names in [`internal/cls/rgw/types_index.go:286-300`](https://github.com/jhoblitt/rgw-go/blob/50fc45c4d59953661d5c57c27ca7016bc55edeeb/internal/cls/rgw/types_index.go#L286-L300)), `CategoryNone..CategoryCloudTiered`; Z's `acl.DefaultPolicy`, `acl.Policy.Encode`, `acl.DecodePolicy`, `acl.List.RemoveCanonUserGrant`, `acl.List.AddGrant`, `acl.PermFullControl`, `acl.GranteeCanonUser`, `tags.Attr`, `tags.Decode`; `meta.AttrACL`, `meta.BucketSuspended`/`BucketVersioned`/`BucketVersionsSuspended`/`BucketMFAEnabled`/`BucketObjLockEnabled`, `meta.BucketInfo.Layout.Current` (`IndexLayoutGen{Gen, Layout{Type, Normal{NumShards}}}` — names from `internal/meta/bucket_layout.go`), `meta.IndexNormal`, `meta.ParseOwner`, `meta.Owner.String`; G's `op.BucketStore`, `op.ObjectStore.StatObject`/`DeleteObject`, `op.StatsStore`; Task 1's `op.BucketAdminStore` (`PurgeBypassGC` included), `op.BucketIndexStats`; Task 4's `DeleteBucketWithChildren`, `PurgeBucket`, `lookupUser`; Task 5's quota parsing; Task 3's `WriteBody`, `WriteEmpty`, `formatter.Formatter`, `meta.Quota.Dump`, `meta.Time.Gmtime`, the meta `dumpPool`/`dumpSection` helpers; phase 0's `acl` types with their `kind` and `sortedGrants`, `meta.BucketInfo.Layout` (`Resharding`, `JudgeReshardLockTime`).
- Produces:

```go
package driver

// categoryName is to_string(RGWObjCategory) (cls_rgw_types.cc:133-143).
func categoryName(c uint8) string // rgw.none, rgw.main, rgw.shadow, rgw.multimeta, rgw.cloudtiered, "unknown"
// shardString is BucketIndexShardsManager::to_string (cls_rgw_client.h:190-207): "<shard>#<value>" joined by ",".
func shardString(values []string) string

// The op.BucketAdminStore methods.
func (s *Store) IndexStats(ctx context.Context, rec *op.BucketRecord) (op.BucketIndexStats, error)
func (s *Store) ChangeBucketOwner(ctx context.Context, rec *op.BucketRecord, owner meta.Owner, displayName string, newName *meta.BucketID) error
func (s *Store) UnlinkBucketOwner(ctx context.Context, rec *op.BucketRecord, owner meta.Owner) error
func (s *Store) ChownBucket(ctx context.Context, rec *op.BucketRecord, owner meta.Owner, displayName string) error
func (s *Store) SyncOwnerStats(ctx context.Context, owner meta.Owner) error
```

```go
package op

// BucketStatsData is what bucket_stats (driver/rados/rgw_bucket.cc:1351-1436) renders.
type BucketStatsData struct {
	Rec      *BucketRecord
	HasIndex bool // local zonegroup and a Normal index: the only case with shard stats
	Index    BucketIndexStats
	Tags     map[string]string // user.rgw.x-amz-tagging when present
}

// BucketInfo is RGWOp_Bucket_Info + RGWBucketAdminOp::info (rgw_rest_bucket.cc:19-62,
// rgw_bucket.cc:1631-1724, list_owner_bucket_info :1546-1629). Cap buckets=read.
// Exactly one of Single, Entries or Names is set: a named bucket; a uid's (or
// the account's, when the user has one) buckets with stats; otherwise names.
// With no uid and no bucket every entry point of the zone is listed
// (metadata section "bucket", pages of 1000, no marker).
type BucketInfo struct {
	AdminOp
	Bucket     string      // may be "tenant/name"
	UID        meta.UserID // its tenant is the default tenant
	UIDGiven   bool
	Stats      bool
	MaxEntries uint32
	Marker     string

	Single     *BucketStatsData
	Entries    []BucketStatsData
	Names      []string
	Paged      bool // uid given and MaxEntries > 0: the {"buckets",truncated,count,marker} form
	Truncated  bool
	Count      uint64
	NextMarker string
}

// LinkBucket is RGWOp_Bucket_Link + RGWBucketAdminOp::link (:128-170; rgw_bucket.cc:1026-1187). Cap buckets=write.
type LinkBucket struct {
	AdminOp
	UID           meta.UserID
	AccountID     string
	Bucket        string // may be "tenant/name"
	BucketID      string
	NewBucketName string // may be "tenant/name"
}
// UnlinkBucket is RGWOp_Bucket_Unlink + RGWBucketAdminOp::unlink (:172-209; :999-1024).
type UnlinkBucket struct {
	AdminOp
	UID       meta.UserID
	AccountID string
	Bucket    string
}
// RemoveBucketAdmin is RGWOp_Bucket_Remove + RGWBucketAdminOp::remove_bucket (:211-248; :1282-1327).
type RemoveBucketAdmin struct {
	AdminOp
	Bucket, Tenant string
	PurgeObjects   bool
	// BypassGC takes remove_bypass_gc, which purges whatever PurgeObjects
	// says (rgw_bucket.cc:1303-1306).
	BypassGC bool
}
// SetBucketQuota is RGWOp_Set_Bucket_Quota + RGWBucket::set_quota (:250-328; :261-272).
type SetBucketQuota struct {
	AdminOp
	UID    meta.UserID
	Bucket string
	Quota  meta.Quota // the handler resolves params against the current quota
}
// GetBucketPolicy is RGWOp_Get_Policy + RGWBucket::get_policy (:64-92; :907-983).
type GetBucketPolicy struct {
	AdminOp
	Bucket, Object string
	Result         acl.Policy
}
// RemoveObjectAdmin is RGWOp_Object_Remove (:362-390; :274-289).
type RemoveObjectAdmin struct {
	AdminOp
	Bucket, Object string
}

// loadAdminBucket is RGWBucket::init (rgw_bucket.cc:169-216): "tenant/name"
// split, else the uid's tenant; a bucket that does not exist is ErrNoSuchKey
// unless the caller maps it (BucketInfo and RemoveBucketAdmin: ErrNoSuchBucket).
func loadAdminBucket(ctx context.Context, env *Env, uid meta.UserID, bucket string) (*BucketRecord, error)

// RemoveBucketBypassGC is RadosBucket::remove_bypass_gc
// (rgw_sal_rados.cc:470-608): the driver's data pass, then the ordinary
// purge, remove(delete_children=true), whose result is returned (:598-607).
func RemoveBucketBypassGC(ctx context.Context, env *Env, rec *BucketRecord) error
```

```go
package admin

func newBucketHandlers() map[string]HandlerFunc // get_bucket_info, link_bucket, unlink_bucket, remove_bucket, set_bucket_quota, get_policy, remove_object
// dumpBucketStats is bucket_stats (rgw_bucket.cc:1351-1436; v20.2.4
// :1514-1599, which adds reshard_status, judge_reshard_lock_time and
// read_tracker).
func dumpBucketStats(f formatter.Formatter, d op.BucketStatsData, rel denc.Release)
// dumpBucketUsage is dump_bucket_usage (rgw_bucket.cc:298-310): one section
// per category in RGWObjCategory's enum order.
func dumpBucketUsage(f formatter.Formatter, cats map[string]op.CategoryStats)
// dumpCategoryStats is RGWStorageStats::dump with dump_utilized true (rgw_common.cc:3102-3115).
func dumpCategoryStats(f formatter.Formatter, s op.CategoryStats)
```

```go
package meta

// Dump is rgw_data_placement_target::dump (rgw_basic_types.cc:131-136, both tags).
func (p DataPlacement) Dump(f formatter.Formatter, rel denc.Release)
// Dump is rgw_bucket::dump (rgw_basic_types.cc:144-151, both tags).
func (b BucketID) Dump(f formatter.Formatter, rel denc.Release)
```

```go
package acl

// The acl types' dumps (rgw_acl.cc, identical at v19.2.6 and v20.2.4).
func (p Policy) Dump(f formatter.Formatter, rel denc.Release)      // RGWAccessControlPolicy::dump :417-421
func (o Owner) Dump(f formatter.Formatter, rel denc.Release)       // ACLOwner::dump :404-408
func (l List) Dump(f formatter.Formatter, rel denc.Release)        // RGWAccessControlList::dump :369-402
func (g Grant) Dump(f formatter.Formatter, rel denc.Release)       // ACLGrant::dump :275-302
func (p Permission) Dump(f formatter.Formatter, rel denc.Release)  // ACLPermission::dump :265-268
func (t GranteeType) Dump(f formatter.Formatter, rel denc.Release) // ACLGranteeType::dump :270-273
```

Driver semantics:

- **IndexStats**: `readShardHeaders(ctx, &rec.Info)` (M) in shard order; for every header and every `(category, stats)` in `Stats`: `Size += TotalSize`, `SizeRounded += TotalSizeRounded`, `SizeUtilized += ActualSize`, `NumObjects += NumEntries` (accumulate_raw_stats, [rgw_rados.cc:5464-5479](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5464-L5479)); `Ver`, `MasterVer`, `MaxMarker` = `shardString` over the headers' `Ver` (decimal), `MasterVer`, `MaxMarker`, shard numbers 0..n-1 (an unsharded index has one header at shard 0). An indexless layout returns zero stats and empty strings (the op does not call it then).
- **ChangeBucketOwner** ([rgw_bucket.cc:1093-1184](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_bucket.cc#L1093-L1184), in that order): `rec.Attrs[meta.AttrACL]` absent → `op.ErrInvalidArgument` with radosgw's Hera message; decode the policy (failure → `ErrInternalError`, radosgw's `-EIO`); `unlinkBucket(ctx, meta.ParseOwner(policy.Owner.ID), rec.Info.Bucket)` (update_entrypoint=false: the cls_user entry only); the new ACL: `acl.DefaultPolicy(owner, displayName).Encode(e, release)` into `rec.Attrs[AttrACL]`; `rec.Info.Owner = owner`; when `newName` differs in tenant or name: `old := rec.Info.Bucket`, `rec.Info.Bucket.Tenant/Name = newName's` (marker and id kept), `writeInstance(ctx, &rec.Info, rec.Attrs, exclusive=true, now, &v)` with a fresh `newWriteVersion()`; else `PutBucketInfo(rec)`; then the entry point `{Bucket: rec.Info.Bucket, Owner: owner, CreationTime: rec.Info.CreationTime, Linked: true}` written with `writeEntryPoint(ctx, ep, nil, false, now, &epv)` and `linkBucket(ctx, owner, rec.Info.Bucket, rec.Info.CreationTime)` (link_bucket with update_entrypoint and the ep given); when renamed: `removeEntryPoint(ctx, old.Tenant, old.Name, &rec.EPVersion)` then `removeInstance(ctx, old, nil)`, each failure returned as radosgw returns it. `rec.Version`/`EPVersion` updated.
- **UnlinkBucketOwner** (`RGWBucketCtl::unlink_bucket(update_entrypoint=true)`, rgw_bucket.cc `do_unlink_bucket`): `unlinkBucket(ctx, owner, rec.Info.Bucket)`; then `ep := readEntryPoint(tenant, name)`, `ep.Linked = false`, `writeEntryPoint(..., ep.v)`; an `ENOENT` on the entry point is success.
- **ChownBucket** ([rgw_sal_rados.cc:699-751](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L699-L751)): up to 10 rounds on `ErrConcurrentModification` (adopt_user_bucket): `GetBucket` afresh, `unlinkBucket(old owner)`, `linkBucket(owner, bucket, creation)`, `rec.Info.Owner = owner`, ACL: decode (a decode failure is not fatal — the ACL is left as is), `RemoveCanonUserGrant(oldOwner.ID)`, `AddGrant(Grant{Type: GranteeCanonUser, ID: owner.String(), Name: displayName, Permission: PermFullControl})`, `Owner = {owner.String(), displayName}`, re-encode; `PutBucketInfo(rec)`.
- **SyncOwnerStats**: M's `syncAllStats(ctx, owner)`.

Op semantics:

- **loadAdminBucket**: `RGWBucket::init`: bucket and uid both empty → `ErrInvalidArgument`; `tenant, name = split at the first "/"` else tenant = uid's tenant; `GetBucket(tenant, name)`; `ErrNoSuchBucket` → `ErrNoSuchKey` (raw ENOENT everywhere but info and remove); when a uid is given `GetUser` (missing → `ErrNoSuchKey`, `load_user`'s ENOENT).
- **BucketInfo**: with `Bucket`: `loadAdminBucket` with `ErrNoSuchKey` → `ErrNoSuchBucket` (rgw_bucket.cc:1643-1644); `Single = bucketStats(rec)`. `bucketStats`: `HasIndex = rec.Info.Zonegroup == Env.Zone.ZoneGroup().ID && layout type == meta.IndexNormal`; `Index = Env.BucketAdmin.IndexStats(rec)` when `HasIndex`; `Tags` from `tags.Decode(rec.Attrs[tags.Attr])` when present. With `UIDGiven` and no bucket: `GetUser` (missing → `ErrNoSuchKey`); owner = `AccountOwner(info.AccountID)` when set else the user; `pageSize = rgw_list_buckets_max_chunk`, capped by `MaxEntries` when > 0; loop `ListUserBuckets(owner, marker, min(pageSize, MaxEntries-count))` until not truncated or `count >= MaxEntries`; per entry `Stats` → `GetBucket` + `bucketStats` (a failing bucket is skipped: radosgw ignores `bucket_stats`' return there) else the name; `Paged = MaxEntries > 0`, `Truncated` = the last page's `more`, `NextMarker` its `next`. With neither: `Env.Metadata.List("bucket", marker, 1000)` pages from `""` until not truncated; names as returned (`tenant/name` keys included); with `Stats`, `GetBucket` by the key's tenant/name split — radosgw passes the whole key as the name with the uid's tenant and silently skips the failure; do the split, which returns the stats radosgw's CLI shows and its REST call drops; that is a difference from radosgw, recorded in docs/exclusions.md's coexistence section by Step 7.
- **LinkBucket** (rgw_bucket.cc:1026-1091): owner = `AccountOwner(AccountID)` when set, else `UserOwner(UID)` when set, else `ErrInvalidArgument.WithMessage("requires user or account id")`; `loadAdminBucket(uid, bucket)`; display name: account → `Env.Accounts.GetAccount` (`ErrNoSuchEntity` → `ErrNoSuchKey`) `.Info.Name`; user with `AccountID != ""` → `ErrInvalidArgument.WithMessage("account users cannot own buckets. use --account-id instead")`; else the user's display name; `BucketID != ""` and `!= rec.Info.Bucket.ID` → `ErrInvalidArgument.WithMessage("specified bucket id does not match " + id)`; `NewBucketName` split at `/` into a `meta.BucketID{Tenant, Name}` (tenant otherwise the request's `op_state.get_tenant()`, which is the uid's tenant); `Env.BucketAdmin.ChangeBucketOwner(rec, owner, displayName, newName)`.
- **UnlinkBucket**: owner as above else `ErrInvalidArgument`; `loadAdminBucket`; `UnlinkBucketOwner`.
- **RemoveBucketAdmin**: `GetBucket(Tenant, Bucket)` (`ErrNoSuchBucket` kept); `rec.Info.Zonegroup != ZoneGroup().ID` → `op.ErrPermanentRedirect` (rgw_bucket.cc:1296-1301); `BypassGC` → `RemoveBucketBypassGC(rec)` whatever `PurgeObjects` says, else `DeleteBucketWithChildren(rec, PurgeObjects)` (:1303-1306); an `ErrNoSuchKey` from either path is answered `ErrNoSuchBucket`, the op's mapping of every `-ENOENT` ([rgw_rest_bucket.cc:245-247](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rest_bucket.cc#L245-L247)). **RemoveBucketBypassGC** (N-D9): `Env.BucketAdmin.PurgeBypassGC(ctx, rec)` — its error returns and nothing further runs, as remove_bypass_gc returns before `remove` — then `PurgeBucket(ctx, env, rec)`.
- **SetBucketQuota**: the handler requires the `uid` and `bucket` ARGUMENTS to exist (even empty → `ErrInvalidArgument` only when absent, [rgw_rest_bucket.cc:268-282](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rest_bucket.cc#L268-L282)); quota from the body (as Task 5) or from params with the current bucket quota as defaults (`max-size-kb` default `roundedKB(current max_size)`); `loadAdminBucket(uid, bucket)`; `rec.Info.Quota = Quota`; `PutBucketInfo(rec)`.
- **GetBucketPolicy**: `loadAdminBucket("", bucket)`; `Object != ""` → `Env.Objects.StatObject(rec, ObjKey{Name: Object})`, `!Exists` → `ErrNoSuchKey`, `Attrs[AttrACL]` decoded (absent → `ErrNoSuchKey`); else `rec.Attrs[AttrACL]` (absent → `ErrNoSuchKey`); decode failure → `ErrInternalError`.
- **RemoveObjectAdmin**: `loadAdminBucket("", bucket)`; `Env.Objects.DeleteObject(rec, ObjKey{Name: Object}, DeleteParams{})`.
- Every op's `Complete` → `op.LogUsage(ctx, r, name)`.

Handler rendering, every body flusher-started as `RGWBucketAdminOp::info` and `get_policy` start theirs (rgw_bucket.cc:1649-1650, :972-974), written with `WriteBody`: `Single` → `dumpBucketStats`; `Names` → an array `buckets` of `dump_string("bucket", name)` ([:1701-1718](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_bucket.cc#L1701-L1718)); `Entries` → an array `buckets` of `dumpBucketStats`; `Paged` → a `result` section holding the `buckets` array, `truncated` (bool), `count` (unsigned) and `marker` when truncated (`list_owner_bucket_info`, [:1582-1626](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_bucket.cc#L1582-L1626)); an unpaged uid listing is the `buckets` array alone. Link, unlink, remove, quota and object remove → `WriteEmpty(w, r, 200)`. `bucket?policy` → a `policy` section around `o.Result.Dump(f, rel)` ([:976-978](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_bucket.cc#L976-L978)).

```go
// bucketCategories is RGWObjCategory's enum order under to_string's names
// (cls_rgw_types.cc:133-143), the order std::map iterates the stats map in.
var bucketCategories = []string{"rgw.none", "rgw.main", "rgw.shadow", "rgw.multimeta", "rgw.cloudtiered", "unknown"}

func dumpBucketUsage(f formatter.Formatter, cats map[string]op.CategoryStats) {
	f.OpenObjectSection("usage")
	for _, name := range bucketCategories {
		if s, ok := cats[name]; ok {
			f.OpenObjectSection(name)
			dumpCategoryStats(f, s)
			f.CloseSection()
		}
	}
	f.CloseSection()
}

func dumpCategoryStats(f formatter.Formatter, s op.CategoryStats) {
	f.DumpUnsigned("size", s.Size)
	f.DumpUnsigned("size_actual", s.SizeRounded)
	f.DumpUnsigned("size_utilized", s.SizeUtilized)
	f.DumpUnsigned("size_kb", (s.Size+1023)/1024) // rgw_rounded_kb
	f.DumpUnsigned("size_kb_actual", (s.SizeRounded+1023)/1024)
	f.DumpUnsigned("size_kb_utilized", (s.SizeUtilized+1023)/1024)
	f.DumpUnsigned("num_objects", s.NumObjects)
}

func dumpBucketStats(f formatter.Formatter, d op.BucketStatsData, rel denc.Release) {
	info := d.Rec.Info
	current := info.Layout.Current
	f.OpenObjectSection("stats")
	f.DumpString("bucket", info.Bucket.Name)
	f.DumpString("tenant", info.Bucket.Tenant)
	versioning := "off"
	if info.Flags&meta.BucketVersioned != 0 {
		versioning = "enabled"
		if info.Flags&meta.BucketVersionsSuspended != 0 {
			versioning = "suspended"
		}
	}
	f.DumpString("versioning", versioning)
	f.DumpString("zonegroup", info.Zonegroup)
	f.DumpString("placement_rule", info.PlacementRule.String())
	f.OpenObjectSection("explicit_placement")
	info.Bucket.ExplicitPlacement.Dump(f, rel)
	f.CloseSection()
	f.DumpString("id", info.Bucket.ID)
	f.DumpString("marker", info.Bucket.Marker)
	f.DumpStream("index_type", current.Layout.Type.String())
	f.DumpInt("index_generation", int64(current.Gen)) //nolint:gosec // dump_int of the u64 generation
	if rel >= denc.Tentacle {
		f.DumpString("reshard_status", info.Layout.Resharding.String()) // v20.2.4 rgw_bucket.cc:1566
		f.DumpStream("judge_reshard_lock_time", info.Layout.JudgeReshardLockTime.Gmtime())
	}
	f.DumpBool("object_lock_enabled", info.Flags&meta.BucketObjLockEnabled != 0)
	f.DumpBool("mfa_enabled", info.Flags&meta.BucketMFAEnabled != 0)
	f.DumpString("owner", info.Owner.String())
	if d.HasIndex {
		f.DumpInt("num_shards", int64(current.Layout.Normal.NumShards))
		f.DumpString("ver", d.Index.Ver)
		f.DumpString("master_ver", d.Index.MasterVer)
		f.DumpString("max_marker", d.Index.MaxMarker)
		dumpBucketUsage(f, d.Index.Categories)
	}
	f.DumpStream("mtime", meta.Time{Time: d.Rec.Mtime}.Gmtime())
	f.DumpStream("creation_time", info.CreationTime.Gmtime())
	f.OpenObjectSection("bucket_quota")
	info.Quota.Dump(f, rel)
	f.CloseSection()
	if d.Tags != nil {
		// RGWObjTags::dump (rgw_tag.cc:59-66): each tag's key is its field name.
		f.OpenObjectSection("tagset")
		for _, k := range slices.Sorted(maps.Keys(d.Tags)) {
			f.DumpString(k, d.Tags[k])
		}
		f.CloseSection()
	}
	if rel >= denc.Tentacle {
		f.DumpInt("read_tracker", int64(d.Rec.Version.Ver)) //nolint:gosec // dump_int of objv_tracker.read_version.ver, v20.2.4 rgw_bucket.cc:1596
	}
	f.CloseSection()
}
```

- [ ] **Step 1: Write the failing driver specs**

`internal/driver/bucketadmin_test.go` on fakerados (M's fixtures create a zone with a bucket `plain` owned by `alice` through `CreateBucket`):

```go
It("sums shard headers per category and renders the shard strings", func(ctx SpecContext) {
	rec := createBucket(ctx, "plain", alice)
	bumpShard(ctx, rec, 0, rgwcls.CategoryMain, 3, 4096, 5000) // fakerados helper: sets header stats and ver on shard 0
	bumpShard(ctx, rec, 3, rgwcls.CategoryMain, 1, 1024, 1024)
	st, err := store.IndexStats(ctx, rec)
	Expect(err).NotTo(HaveOccurred())
	Expect(st.Categories["rgw.main"].NumObjects).To(Equal(uint64(4)))
	Expect(st.Categories["rgw.main"].SizeRounded).To(Equal(uint64(6024)))
	Expect(st.Ver).To(HavePrefix("0#"))
	Expect(strings.Count(st.Ver, ",")).To(Equal(int(rec.Info.Layout.Current.Layout.Normal.NumShards) - 1))
	Expect(st.MaxMarker).To(MatchRegexp(`^0#[^,]*,1#`))
})
It("changes the owner: ACL, instance owner, entry point and the two cls_user lists", func(ctx SpecContext) {
	rec := createBucket(ctx, "plain", alice)
	Expect(store.ChangeBucketOwner(ctx, rec, meta.UserOwner(bob), "Bob", nil)).To(Succeed())
	got, _ := store.GetBucket(ctx, "", "plain")
	Expect(got.Info.Owner).To(Equal(meta.UserOwner(bob)))
	pol := decodeACL(got.Attrs[meta.AttrACL])
	Expect(pol.Owner.ID).To(Equal(bob.String()))
	Expect(pol.Owner.DisplayName).To(Equal("Bob"))
	Expect(listOwnerBuckets(ctx, meta.UserOwner(bob))).To(ConsistOf("plain"))
	Expect(listOwnerBuckets(ctx, meta.UserOwner(alice))).To(BeEmpty())
	ep := readEP(ctx, "", "plain")
	Expect(ep.Owner).To(Equal(meta.UserOwner(bob)))
	Expect(ep.Linked).To(BeTrue())
})
It("renames through link with a new name", func(ctx SpecContext) {
	rec := createBucket(ctx, "initial-name", alice)
	id := rec.Info.Bucket.ID
	Expect(store.ChangeBucketOwner(ctx, rec, meta.UserOwner(alice), "Alice", &meta.BucketID{Name: "renamed-name"})).To(Succeed())
	_, err := store.GetBucket(ctx, "", "initial-name")
	Expect(err).To(MatchError(op.ErrNoSuchBucket))
	got, err := store.GetBucket(ctx, "", "renamed-name")
	Expect(err).NotTo(HaveOccurred())
	Expect(got.Info.Bucket.ID).To(Equal(id), "the instance id and marker survive")
	Expect(got.Info.Bucket.Name).To(Equal("renamed-name"))
	Expect(objectExists(ctx, domainRoot, meta.BucketID{Name: "initial-name", ID: id}.InstanceOID())).To(BeFalse(), "old instance removed")
	Expect(listOwnerBuckets(ctx, meta.UserOwner(alice))).To(ConsistOf("renamed-name"))
})
It("unlinks: the cls_user entry goes and the entry point says linked=false", func(ctx SpecContext) {
	rec := createBucket(ctx, "plain", alice)
	Expect(store.UnlinkBucketOwner(ctx, rec, meta.UserOwner(alice))).To(Succeed())
	Expect(listOwnerBuckets(ctx, meta.UserOwner(alice))).To(BeEmpty())
	Expect(readEP(ctx, "", "plain").Linked).To(BeFalse())
})
It("chowns keeping other grants and swapping the owner grant", func(ctx SpecContext) {
	rec := createBucket(ctx, "plain", alice)
	addGrant(ctx, rec, carol, acl.PermRead)
	Expect(store.ChownBucket(ctx, rec, meta.AccountOwner("RGW00000000000000001"), "acme")).To(Succeed())
	got, _ := store.GetBucket(ctx, "", "plain")
	pol := decodeACL(got.Attrs[meta.AttrACL])
	Expect(pol.Owner.ID).To(Equal("RGW00000000000000001"))
	Expect(grantIDs(pol)).To(ConsistOf("RGW00000000000000001", carol.String()))
	Expect(got.Info.Owner).To(Equal(meta.AccountOwner("RGW00000000000000001")))
})
It("refuses to change the owner of a bucket without an ACL", func(ctx SpecContext) {
	rec := createBucket(ctx, "plain", alice)
	delete(rec.Attrs, meta.AttrACL)
	Expect(store.PutBucketAttrs(ctx, rec, nil, []string{meta.AttrACL})).To(Succeed())
	rec, _ = store.GetBucket(ctx, "", "plain")
	Expect(store.ChangeBucketOwner(ctx, rec, meta.UserOwner(bob), "Bob", nil)).To(MatchError(op.ErrInvalidArgument))
})
```

- [ ] **Step 2: Implement `internal/driver/bucketadmin.go`; memstore parity**

As specified. The memstore: `IndexStats` from its object map (Main category: `NumObjects`, `Size`, `SizeRounded = roundedObjSize`, `SizeUtilized = Size`; `Ver` = `"0#" + strconv of a per-bucket write counter`, `MasterVer "0#0"`, `MaxMarker "0#"`, shards 0..NumShards-1 when sharded); `ChangeBucketOwner`, `UnlinkBucketOwner`, `ChownBucket` over its bucket and owner-list maps with the same ACL edits; `SyncOwnerStats` a no-op; `PurgeBypassGC` drops every object record, every version, and every upload of the bucket (the memstore keeps no GC) and leaves the bucket, as the driver's data pass does. The driver's `PurgeBypassGC` stays `op.ErrNotImplemented` until Task 14. Run both suites: PASS.

- [ ] **Step 3: Write the failing op specs**

`internal/op/adminbucket_test.go` on memstore (users `admin` (`buckets=*;users=*`), `test-user1`, `test-user2`, `acctuser` with `AccountID`; bucket `test` owned by admin):

```go
It("returns a single bucket's stats, NoSuchBucket when absent", func(ctx SpecContext) {
	o := op.NewBucketInfo()
	o.Bucket = "test"
	o.Stats = true
	Expect(run(ctx, o)).To(Succeed())
	Expect(o.Single).NotTo(BeNil())
	Expect(o.Single.Rec.Info.Bucket.Name).To(Equal("test"))
	Expect(o.Single.HasIndex).To(BeTrue())
	o = op.NewBucketInfo()
	o.Bucket = "foo"
	Expect(run(ctx, o)).To(MatchError(op.ErrNoSuchBucket))
})
It("lists every bucket, and a user's with or without stats, paged", func(ctx SpecContext) {
	createBucket("b2", "test-user1")
	o := op.NewBucketInfo()
	Expect(run(ctx, o)).To(Succeed())
	Expect(o.Names).To(ConsistOf("test", "b2"))
	o = op.NewBucketInfo()
	o.UID = meta.UserID{ID: "test-user1"}
	o.UIDGiven = true
	o.Stats = true
	Expect(run(ctx, o)).To(Succeed())
	Expect(o.Entries).To(HaveLen(1))
	Expect(o.Entries[0].Rec.Info.Owner).To(Equal(meta.UserOwner(meta.UserID{ID: "test-user1"})))
	createBucket("b3", "test-user1")
	o = op.NewBucketInfo()
	o.UID = meta.UserID{ID: "test-user1"}
	o.UIDGiven = true
	o.MaxEntries = 1
	Expect(run(ctx, o)).To(Succeed())
	Expect(o.Paged).To(BeTrue())
	Expect(o.Names).To(HaveLen(1))
	Expect(o.Truncated).To(BeTrue())
	Expect(o.NextMarker).NotTo(BeEmpty())
	o = op.NewBucketInfo()
	o.UID = meta.UserID{ID: "nosuch"}
	o.UIDGiven = true
	Expect(run(ctx, o)).To(MatchError(op.ErrNoSuchKey))
})
It("links a bucket to another user, checks the id, renames, and unlinks", func(ctx SpecContext) {
	rec, _ := store.GetBucket(ctx, "", "test")
	l := op.NewLinkBucket()
	l.UID = meta.UserID{ID: "test-user2"}
	l.Bucket = "test"
	l.BucketID = rec.Info.Bucket.ID
	Expect(run(ctx, l)).To(Succeed())
	got, _ := store.GetBucket(ctx, "", "test")
	Expect(got.Info.Owner).To(Equal(meta.UserOwner(l.UID)))
	l.BucketID = "wrong"
	Expect(run(ctx, l)).To(MatchError(op.ErrInvalidArgument))
	l = op.NewLinkBucket()
	l.UID = meta.UserID{ID: "test-user2"}
	l.Bucket = "test"
	l.NewBucketName = "renamed"
	Expect(run(ctx, l)).To(Succeed())
	_, err := store.GetBucket(ctx, "", "test")
	Expect(err).To(MatchError(op.ErrNoSuchBucket))
	u := op.NewUnlinkBucket()
	u.UID = meta.UserID{ID: "test-user2"}
	u.Bucket = "renamed"
	Expect(run(ctx, u)).To(Succeed())
	Expect(run(ctx, &op.LinkBucket{Bucket: "renamed"})).To(MatchError(op.ErrInvalidArgument), "needs uid or account-id")
	l = op.NewLinkBucket()
	l.UID = meta.UserID{ID: "acctuser"}
	l.Bucket = "renamed"
	Expect(run(ctx, l)).To(MatchError(op.ErrInvalidArgument), "account users cannot own buckets")
	l = op.NewLinkBucket()
	l.UID = meta.UserID{ID: "test-user2"}
	l.Bucket = "nosuch"
	Expect(run(ctx, l)).To(MatchError(op.ErrNoSuchKey))
})
It("links to an account with the account's name as display name", func(ctx SpecContext) {
	l := op.NewLinkBucket()
	l.AccountID = "RGW00000000000000001"
	l.Bucket = "test"
	Expect(run(ctx, l)).To(Succeed())
	got, _ := store.GetBucket(ctx, "", "test")
	Expect(got.Info.Owner).To(Equal(meta.AccountOwner("RGW00000000000000001")))
	Expect(decodeACL(got.Attrs[meta.AttrACL]).Owner.DisplayName).To(Equal("acme"))
})
It("removes a bucket, purging objects only when asked", func(ctx SpecContext) {
	putObject(ctx, "test", "k1")
	d := op.NewRemoveBucketAdmin()
	d.Bucket = "test"
	Expect(run(ctx, d)).To(MatchError(op.ErrBucketNotEmpty))
	d.PurgeObjects = true
	Expect(run(ctx, d)).To(Succeed())
	Expect(run(ctx, d)).To(MatchError(op.ErrNoSuchBucket))
})
It("purges with bypass-gc whatever purge-objects says", func(ctx SpecContext) {
	putObject(ctx, "test", "k1")
	d := op.NewRemoveBucketAdmin()
	d.Bucket = "test"
	d.BypassGC = true
	Expect(run(ctx, d)).To(Succeed(), "remove_bucket takes remove_bypass_gc without consulting delete_children, rgw_bucket.cc:1303-1304")
	_, err := store.GetBucket(ctx, "", "test")
	Expect(err).To(MatchError(op.ErrNoSuchBucket))
	Expect(run(ctx, d)).To(MatchError(op.ErrNoSuchBucket))
})
It("leaves the bucket when the bypass-gc data pass fails", func(ctx SpecContext) {
	fake := &opfakes.FakeBucketAdminStore{}
	fake.PurgeBypassGCReturns(op.ErrNotImplemented)
	env.BucketAdmin = fake
	d := op.NewRemoveBucketAdmin()
	d.Bucket = "test"
	d.BypassGC = true
	Expect(run(ctx, d)).To(MatchError(op.ErrNotImplemented))
	_, err := store.GetBucket(ctx, "", "test")
	Expect(err).NotTo(HaveOccurred(), "remove_bypass_gc returns before remove, rgw_sal_rados.cc:483-590")
	fake.PurgeBypassGCReturns(op.ErrNoSuchKey)
	Expect(run(ctx, d)).To(MatchError(op.ErrNoSuchBucket), "the op answers every -ENOENT as NoSuchBucket, rgw_rest_bucket.cc:245-247")
})
It("sets the bucket quota and reads the policy", func(ctx SpecContext) {
	q := op.NewSetBucketQuota()
	q.UID = meta.UserID{ID: "admin"}
	q.Bucket = "test"
	q.Quota = meta.Quota{MaxSize: 1000000 * 1024, MaxObjects: -1}
	Expect(run(ctx, q)).To(Succeed())
	got, _ := store.GetBucket(ctx, "", "test")
	Expect(got.Info.Quota.MaxSize).To(Equal(int64(1000000 * 1024)))
	p := op.NewGetBucketPolicy()
	p.Bucket = "test"
	Expect(run(ctx, p)).To(Succeed())
	Expect(p.Result.Owner.ID).To(Equal("admin"))
	p.Bucket = "foo"
	Expect(run(ctx, p)).To(MatchError(op.ErrNoSuchKey))
	p = op.NewGetBucketPolicy()
	p.Bucket = "test"
	p.Object = "missing"
	Expect(run(ctx, p)).To(MatchError(op.ErrNoSuchKey))
})
It("removes an object through the admin route", func(ctx SpecContext) {
	putObject(ctx, "test", "k1")
	r := op.NewRemoveObjectAdmin()
	r.Bucket = "test"
	r.Object = "k1"
	Expect(run(ctx, r)).To(Succeed())
	Expect(run(ctx, r)).To(MatchError(op.ErrNoSuchKey))
})
```

- [ ] **Step 4: Implement `internal/op/adminbucket.go`**

As specified. PASS.

- [ ] **Step 5: Write the failing handler specs**

`internal/admin/bucket_test.go`, the go-ceph and Rook shapes: `GET /admin/bucket` → bare JSON array of names (1 entry); `GET /admin/bucket?bucket=foo` → 404 `NoSuchBucket`; `GET /admin/bucket?bucket=test` → 200 with `versioning "off"`, `object_lock_enabled false`, `id`, `marker`, `owner "admin"`, `creation_time` within a minute of now, `bucket_quota` with `max_size_kb`, `usage` object with `rgw.main` having `size`, `size_actual`, `size_utilized`, `size_kb`, `size_kb_actual`, `size_kb_utilized`, `num_objects`, `ver` like `0#…`, `index_type "Normal"`; field order: `"bucket"` before `"tenant"` before `"versioning"`; `GET /admin/bucket?policy&bucket=foo` → 404 `NoSuchKey`; `GET /admin/bucket?policy&bucket=test` → 200 with `acl.acl_user_map[0].user == "admin"` and `owner.id`; `PUT /admin/bucket?uid=test-user2&bucket-id=<id>&bucket=test` → 200 then `GET` shows `owner "test-user2"`; `PUT /admin/bucket?uid=u&bucket=initial-name&new-bucket-name=renamed-name` → 200, old 404 `NoSuchBucket`, new 200; `POST /admin/bucket?uid=test-user2&bucket=test` → 200; `DELETE /admin/bucket?bucket=test&purge-objects=true` → 200; `DELETE /admin/bucket?bucket=foo` → 404 `NoSuchBucket`; `PUT /admin/bucket?quota&uid=admin&bucket=test&max-size-kb=1000000` → 200 then `GET` `bucket_quota.max_size_kb == 1000000`; `PUT /admin/bucket?quota&bucket=test` (no uid) → 400; `GET /admin/bucket?uid=admin&stats=false` → bare array of names; `GET /admin/bucket?uid=admin&stats=true` → array of objects with `owner "admin"` and `bucket_quota.max_size`; `GET /admin/bucket?uid=admin&max-entries=1` → `{"buckets":[…],"truncated":true,"count":1,"marker":"…"}`; `DELETE /admin/bucket?object&bucket=test&object=k1` → 200; `DELETE /admin/bucket?bucket=test&bypass-gc=true` with an object in the bucket and no `purge-objects` → 200 with `Content-Length: 0`, then `GET /admin/bucket?bucket=test` → 404 `NoSuchBucket` (Review Focus 7). XML, compared whole or by prefix: `GET /admin/bucket?format=xml` → `<?xml version="1.0" encoding="UTF-8"?><buckets><bucket>test</bucket></buckets>`; `GET /admin/bucket?uid=admin&max-entries=1&format=xml` → `<?xml version="1.0" encoding="UTF-8"?><result><buckets><bucket>…</bucket></buckets><truncated>true</truncated><count>1</count><marker>…</marker></result>` (a regex over the two elided values); `GET /admin/bucket?bucket=test&format=xml` → prefix `<?xml version="1.0" encoding="UTF-8"?><stats><bucket>test</bucket><tenant></tenant><versioning>off</versioning><zonegroup>` and the substring `<usage><rgw.main><size>`; `GET /admin/bucket?policy&bucket=test&format=xml` → a regex over `<?xml version="1.0" encoding="UTF-8"?><policy><acl><acl_user_map><entry><user>admin</user><acl>15</acl></entry></acl_user_map><acl_group_map></acl_group_map><grant_map><entry><id>admin</id><grant><type><type>0</type></type><id>admin</id><name>[^<]*</name><permission><flags>15</flags></permission></grant></entry></grant_map></acl><owner><id>admin</id><display_name>[^<]*</display_name></owner></policy>`. On Tentacle (`useRelease(denc.Tentacle)`), `GET /admin/bucket?bucket=test` contains `"index_generation":0,"reshard_status":"None","judge_reshard_lock_time":"0.000000","object_lock_enabled":false` and ends `,"read_tracker":<the instance version>}}`; on Squid it has none of the three keys.

- [ ] **Step 6: Implement `internal/admin/bucket.go`, `internal/acl/dump.go` and `internal/meta/dump_bucket.go`**

`internal/admin/bucket.go`: the handlers per the `RGWOp_Bucket_*::execute` argument lists and the rendering above. `internal/meta/dump_bucket.go`:

```go
package meta

func (p DataPlacement) Dump(f formatter.Formatter, _ denc.Release) {
	dumpPool(f, "data_pool", p.DataPool)
	dumpPool(f, "data_extra_pool", p.DataExtraPool)
	dumpPool(f, "index_pool", p.IndexPool)
}

func (b BucketID) Dump(f formatter.Formatter, rel denc.Release) {
	f.DumpString("name", b.Name)
	f.DumpString("marker", b.Marker)
	f.DumpString("bucket_id", b.ID)
	f.DumpString("tenant", b.Tenant)
	dumpSection(f, "explicit_placement", b.ExplicitPlacement, rel)
}
```

`internal/acl/dump.go`, beside the phase-0 `MarshalJSON` methods it mirrors:

```go
package acl

import (
	"maps"
	"slices"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/formatter"
)

func (p Policy) Dump(f formatter.Formatter, rel denc.Release) {
	f.OpenObjectSection("acl")
	p.ACL.Dump(f, rel)
	f.CloseSection()
	f.OpenObjectSection("owner")
	p.Owner.Dump(f, rel)
	f.CloseSection()
}

func (o Owner) Dump(f formatter.Formatter, _ denc.Release) {
	f.DumpString("id", o.ID)
	f.DumpString("display_name", o.DisplayName)
}

// Dump writes the user and group maps in key order and the grants in the
// multimap's order; the referer list is not dumped.
func (l List) Dump(f formatter.Formatter, rel denc.Release) {
	f.OpenArraySection("acl_user_map")
	for _, k := range slices.Sorted(maps.Keys(l.UserMap)) {
		f.OpenObjectSection("entry")
		f.DumpString("user", k)
		f.DumpInt("acl", int64(l.UserMap[k]))
		f.CloseSection()
	}
	f.CloseSection()
	f.OpenArraySection("acl_group_map")
	for _, k := range slices.Sorted(maps.Keys(l.GroupMap)) {
		f.OpenObjectSection("entry")
		f.DumpUnsigned("group", uint64(k))
		f.DumpInt("acl", int64(l.GroupMap[k]))
		f.CloseSection()
	}
	f.CloseSection()
	f.OpenArraySection("grant_map")
	for _, g := range l.sortedGrants() {
		f.OpenObjectSection("entry")
		f.DumpString("id", g.Key)
		f.OpenObjectSection("grant")
		g.Grant.Dump(f, rel)
		f.CloseSection()
		f.CloseSection()
	}
	f.CloseSection()
}

func (g Grant) Dump(f formatter.Formatter, rel denc.Release) {
	f.OpenObjectSection("type")
	g.kind().Dump(f, rel)
	f.CloseSection()
	switch g.kind() {
	case GranteeCanonUser:
		f.DumpString("id", g.ID)
		f.DumpString("name", g.Name)
	case GranteeEmail:
		f.DumpString("email", g.Email)
	case GranteeGroup:
		f.DumpInt("group", int64(int32(g.Group))) //nolint:gosec // C++ dumps static_cast<int>(group.type)
	case GranteeReferer:
		f.DumpString("url_spec", g.URLSpec)
	}
	f.OpenObjectSection("permission")
	g.Permission.Dump(f, rel)
	f.CloseSection()
}

func (p Permission) Dump(f formatter.Formatter, _ denc.Release)  { f.DumpInt("flags", int64(p)) }
func (t GranteeType) Dump(f formatter.Formatter, _ denc.Release) { f.DumpUnsigned("type", uint64(t)) }
```

`internal/acl/dump_goldens_test.go` runs the byte-exact comparison of Task 3's `dump_goldens_test.go` (the same `dumpGolden` helper, with a local decode-whole helper) over `RGWAccessControlPolicy`, `RGWAccessControlList`, `ACLOwner`, `ACLGrant`, `ACLPermission` and `ACLGranteeType`; `internal/meta/dump_goldens_test.go` gains `rgw_bucket` (`meta.DecodeBucketID`). Run the admin, acl and meta suites: PASS.

- [ ] **Step 7: Record the tenanted listing difference**

`docs/exclusions.md`, "Coexistence obligations independent of any exclusion", a new bullet at the end of the admin bullets that begin with the admin headers bullet:

"- **The admin bucket listing with stats reports tenanted buckets.** `GET /admin/bucket?stats=true` without a uid lists every bucket with its stats. radosgw looks each one up under the requester's tenant with the whole `tenant/name` key as the name, fails for every bucket in another tenant, and leaves it out silently; rgw-go splits the key and reports those buckets too, as radosgw-admin's own listing does."

Report the change for the rgw-rs session.

- [ ] **Step 8: `make check`; commit**

```bash
git add internal/driver/bucketadmin.go internal/driver/bucketadmin_test.go internal/driver/store.go internal/memstore/admin.go internal/op/adminbucket.go internal/op/adminbucket_test.go internal/op/adminpurge.go internal/admin/bucket.go internal/admin/bucket_test.go internal/acl/dump.go internal/acl/dump_goldens_test.go internal/meta/dump_bucket.go internal/meta/dump_goldens_test.go docs/exclusions.md
git commit -m "feat(admin): bucket info, list, link, unlink, remove with bypass-gc, quota, policy and object removal"
```

## Task 7: Accounts: `cls/user` account resources, driver `AccountStore`, the ops, Tentacle quota, `AccountName`

**Files:**
- Create: `internal/cls/user/account.go`, `internal/cls/user/account_test.go`, `internal/driver/account.go`, `internal/driver/account_test.go`, `internal/op/adminaccount.go`, `internal/op/adminaccount_test.go`, `internal/admin/account.go`, `internal/admin/account_test.go`, `internal/meta/dump_account.go`
- Modify: `internal/meta/dump_goldens_test.go` (`RGWAccountInfo`), `internal/testutil/fakerados/cls_user.go` (account_resource_add/get/rm/list emulation), `internal/driver/store.go` (A's `GetAccount` stub and Task 1's stubs replaced), `internal/admin/handler.go` (`newAccountHandlers()`), `internal/admin/dispatch.go` (the Tentacle `?quota` row already present)

**Interfaces:**
- Consumes: M's `sysobjs.read/write/remove`, `objv`, `newWriteVersion`, `accountObj`, `emailIndexObj`, `readAccount`, `ownerBucketsObj`; `meta.UID`/`DecodeUID` (M Task 4's RGWUID), `meta.AccountInfo`, `meta.DecodeAccountInfo`, `meta.NewAccountInfo`, `meta.ValidAccountID`; G's `op.UserStore.ListUserBuckets`; A's `op.AccountRecord`; Task 1's widened `op.AccountStore`, `op.PutAccountOptions`; Task 4's `DeleteBucketWithChildren`, `RemoveUser` op internals; `op.ErrAccountAlreadyExists`, `op.ErrNoSuchEntity`, `op.ErrNoSuchKey`, `op.ErrBucketNotEmpty`, `op.ErrBucketAlreadyExists`, `op.ErrInvalidArgument`, `op.ErrConcurrentModification`; Task 3's `WriteBody`, `WriteEmpty`, `formatter.Formatter`, the meta `dumpSection` helper and `Quota.Dump`.
- Produces:

```go
package user // internal/cls/user

// AccountResource is cls_user_account_resource (cls_user_types.h:239-261).
type AccountResource struct {
	Name, Path string
	Metadata   []byte
}
// AccountHeader is cls_user_account_header (cls_user_types.h:220-235).
type AccountHeader struct{ Count uint32 }
// UserResourceMetadata is rgwrados::users::resource_metadata (driver/rados/users.h:68-84).
type UserResourceMetadata struct{ UserID string }
func (m UserResourceMetadata) Encode(e *denc.Encoder, _ denc.Release)
func DecodeUserResourceMetadata(d *denc.Decoder) UserResourceMetadata

// AccountResourceAdd is account_resource_add (cls_user.cc:520-579): keys are
// the lower-cased name; an existing name with exclusive is EEXIST; a new name
// at the limit is EUSERS.
func AccountResourceAdd(op radosclient.Execer, entry AccountResource, exclusive bool, limit uint32, r denc.Release)
// AccountResourceRm is account_resource_rm (:616-661): a missing name is ENOENT.
func AccountResourceRm(op radosclient.Execer, name string, r denc.Release)
// AccountResourceGet is account_resource_get (:581-614).
func AccountResourceGet(op *radosclient.ReadOp, name string, r denc.Release) *AccountResourceGetResult
func (res *AccountResourceGetResult) Entry() (AccountResource, error)
// AccountResourceList is account_resource_list (:663-720): after marker, at
// most min(max, 1000) raw entries, those whose path starts with pathPrefix
// returned; Marker is the last raw key read.
func AccountResourceList(op *radosclient.ReadOp, marker, pathPrefix string, max uint32, r denc.Release) *AccountResourceListResult
func (res *AccountResourceListResult) Result() (entries []AccountResource, truncated bool, marker string, err error)
```

```go
package driver

func (s *Store) accountNameObj(tenant, name string) sysObj // account pool, "name.<tenant>$<name>" (account.cc:92-100)
func (s *Store) accountUsersObj(id string) sysObj          // account pool, "users.<id>" (:52-58)
// Account emails use emailIndexObj: accounts and users share users.email (:108-114).

// The op.AccountStore methods (rgwrados::account, driver/rados/account.cc).
func (s *Store) GetAccount(ctx context.Context, id string) (*op.AccountRecord, error)               // read :163-198; id mismatch → ErrInternalError
func (s *Store) GetAccountByName(ctx context.Context, tenant, name string) (*op.AccountRecord, error) // read_by_name :200-217
func (s *Store) GetAccountByEmail(ctx context.Context, email string) (*op.AccountRecord, error)      // read_by_email :219-239: a user's email is ErrNoSuchEntity
func (s *Store) PutAccount(ctx context.Context, rec *op.AccountRecord, old *meta.AccountInfo, opts op.PutAccountOptions) error // write :242-392
func (s *Store) RemoveAccount(ctx context.Context, rec *op.AccountRecord) error                     // remove :394-438
func (s *Store) AddAccountUser(ctx context.Context, accountID string, info meta.UserInfo) error   // users.cc:27-51
func (s *Store) RemoveAccountUser(ctx context.Context, accountID, displayName string) error       // :91-106
func (s *Store) ListAccountUsers(ctx context.Context, accountID, marker string, max uint32) ([]string, string, error) // :108-140; ENOENT → none
func (s *Store) AccountName(ctx context.Context, id string) (string, error)
```

```go
package op

// AccountParams is rgw::account::AdminOpState (rgw_account.h:47-62 at v20.2.4;
// the quota fields exist on Squid too but nothing sets them there).
type AccountParams struct {
	ID, Tenant, Name, Email                                  string
	MaxUsers, MaxRoles, MaxGroups, MaxAccessKeys, MaxBuckets *int32
	QuotaScope                                               string // "", "account", "bucket"
	QuotaMaxSize, QuotaMaxObjects                            *int64
	QuotaEnabled                                             *bool
}

// CreateAccount is RGWOp_Account_Create + rgw::account::create (rgw_rest_account.cc:20-102; rgw_account.cc:112-177). Cap accounts=write.
type CreateAccount struct {
	AdminOp
	Params AccountParams
	Result meta.AccountInfo
}
// GetAccountInfo is RGWOp_Account_Get + info (:171-191; :461-496). Cap account=read on Squid, accounts=read on Tentacle.
type GetAccountInfo struct {
	AdminOp
	Params AccountParams // id, tenant, name, email
	Result meta.AccountInfo
}
// ModifyAccount is RGWOp_Account_Modify and RGWOp_Account_Quota_Set + modify (:104-168, v20.2.4:223-299; :179-272). Cap accounts=write.
type ModifyAccount struct {
	AdminOp
	Params AccountParams
	Result meta.AccountInfo
}
// RemoveAccount is RGWOp_Account_Delete + remove (:193-221; :274-459). Cap account/accounts=write per release.
type RemoveAccount struct {
	AdminOp
	Params AccountParams
}

// loadAccount is the id | name (with tenant) | email lookup shared by info, modify and remove
// (rgw_account.cc:190-203): none given → ErrInvalidArgument with radosgw's message; a
// missing account is ErrNoSuchKey (the raw ENOENT the REST layer passes through).
func loadAccount(ctx context.Context, env *Env, p AccountParams) (*AccountRecord, error)
// generateAccountID is rgw::account::generate_id: "RGW" + 17 random decimal digits.
func generateAccountID() string
// validateAccountName is validate_name (rgw_account.cc:73-103).
func validateAccountName(name string) error
```

Semantics:

- **PutAccount** ([account.cc:242-392](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/account.cc#L242-L392)): `old != nil && old.ID != rec.Info.ID` → `ErrInvalidArgument`; `sameName = old != nil && old.Tenant == Tenant && old.Name == Name`, `sameEmail = old != nil && EqualFold(old.Email, Email)`; when the name changed and the old name's redirect points at this id, remember it for removal; likewise the old email; a new name whose redirect exists (read succeeds) → `ErrAccountAlreadyExists`; a new email whose redirect exists → `ErrAccountAlreadyExists`; write `account.<id>` (`sysobjs.write` with `rec.Attrs`, exclusive per `opts`, version: fresh on create, `rec.Version` checked on update; `EEXIST` → `ErrAccountAlreadyExists`, `ECANCELED` → `ErrConcurrentModification`); then remove the old redirects and write the new ones (`meta.UID{ID: id}` encoded, fresh version) — each failure logged at warning and ignored, as radosgw ignores it. `rec.Version`/`Mtime` updated.
- **RemoveAccount** ([:394-438](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/account.cc#L394-L438)): remove `account.<id>` under `rec.Version` (`ECANCELED` → `ErrConcurrentModification`, `ENOENT` → `ErrNoSuchEntity`); then the name redirect, the email redirect and `users.<id>`, each ENOENT/failure logged and ignored.
- **AddAccountUser**: `AccountResourceAdd(users.<id>, {Name: info.DisplayName, Path: info.Path, Metadata: encode(UserResourceMetadata{info.UserID.ID})}, false, math.MaxUint32)` ([svc_user_rados.cc:365-366](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_user_rados.cc#L365-L366)). **RemoveAccountUser**: `AccountResourceRm(name)`; ENOENT is not an error (the index may be missing on an old zone). **ListAccountUsers**: `AccountResourceList(marker, "", max)`; decode each entry's metadata → id; a missing object is an empty list.
- **CreateAccount**: name given → `validateAccountName` (empty, `$`, `:`, invalid UTF-8 → `ErrInvalidArgument` with radosgw's message); info = `meta.NewAccountInfo()` with tenant, name, email, the `Max*` given; quotas from `rgw_account_default_quota_max_*` and `rgw_bucket_default_quota_max_*` (as Task 4's `defaultQuota`); id: empty → `generateAccountID()`, else `ValidAccountID` or `ErrInvalidArgument`; `PutAccount(rec, nil, {Exclusive: true})`; `Result = info`. The REST layer maps `ErrAccountAlreadyExists` as is (radosgw maps EEXIST → ERR_ACCOUNT_EXISTS only here).
- **GetAccountInfo**: `loadAccount`; `Result = rec.Info`.
- **ModifyAccount**: `loadAccount`; `Tenant != "" && != Info.Tenant` → `ErrInvalidArgument.WithMessage("cannot modify account tenant")`; name validated when given; email when given; `Max*` when given; `QuotaScope` `account` → `Info.Quota`, `bucket` → `Info.BucketQuota`: `QuotaMaxSize`/`QuotaMaxObjects`/`QuotaEnabled` applied when given; `PutAccount(rec, &old, {})`; `ErrAccountAlreadyExists` from a taken name → returned as `ErrBucketAlreadyExists` (radosgw's Modify passes EEXIST through to the S3 table's 409 `BucketAlreadyExists` row — a quirk Task 13 records).
- **RemoveAccount**: `loadAccount`; `ListAccountUsers` pages of 100: any → `ErrBucketNotEmpty` (radosgw's `-ENOTEMPTY`, "The account cannot be deleted until all users are removed."); `ListUserBuckets(AccountOwner(id))` pages of 100: any → `ErrBucketAlreadyExists` (`-EEXIST`); roles, groups, OIDC providers and topics have no phase-1 store and read as empty; `RemoveAccount(rec)`. (`purge_data` is never set by the REST layer.)
- Caps: `GetAccountInfo`/`RemoveAccount` check `account` on Squid and `accounts` on Tentacle (`r.Env.Zone.Release()`); create/modify/quota always `accounts`.
- REST (rgw_rest_account.cc): POST → create with `id, tenant, name, email, max-users, max-roles, max-groups, max-access-keys, max-buckets` (`Int32`, applied when present); PUT → modify with the same; PUT `?quota` on Tentacle → `id` and `quota-type` required and `quota-type ∈ {account, bucket}` else `ErrInvalidArgument`; `max-size` and `max-objects` are read with `get_int32` (a value outside int32 is `ErrInvalidArgument`), `enabled` a bool; then modify with the quota scope; on Squid `PUT ?quota` is a plain modify (the quota parameters are ignored). GET → `id, tenant, name`; DELETE → the same. A missing account → 404 `NoSuchKey` (N-D6). Bodies, flusher-started: create/modify/get → an `AccountInfo` section around `meta.AccountInfo.Dump` (`encode_json("AccountInfo", info)`, [rgw_account.cc:173](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_account.cc#L173), [:268](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_account.cc#L268), [:492](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_account.cc#L492); RGWAccountInfo::dump, [rgw_common.cc:3026-3039](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L3026-L3039)); delete → 200 empty.

- [ ] **Step 1: Write the failing `cls/user` specs**

`internal/cls/user/account_test.go`: encode/decode round trips for `AccountResource`, `AccountHeader`, `UserResourceMetadata` and the four ops against hand-built byte fixtures derived from the ENCODE_START(1,1) framing (as the package's existing fixtures do), plus `goldens_test.go` entries if the corpus has `cls_user_account_resource` dumps (check `internal/cls/user/testdata`; add none if the corpus lacks them). Implement `account.go`. PASS.

- [ ] **Step 2: Emulate the four methods in fakerados**

`internal/testutil/fakerados/cls_user.go`: omap entries keyed by the lower-cased name holding the encoded resource, the count in the omap header; `EEXIST`, `EUSERS` (as `radosclient.Error` with that errno), `ENOENT`, the 1000 cap and the raw-key marker exactly as [cls_user.cc:520-720](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/user/cls_user.cc#L520-L720). Specs for each branch.

- [ ] **Step 3: Write the failing driver specs**

`internal/driver/account_test.go` on fakerados (a zone with `account_pool` and `user_email_pool` from M's fixtures):

```go
It("creates an account with its name and email redirects", func(ctx SpecContext) {
	rec := &op.AccountRecord{Info: newAccount("RGW00000000000000001", "t", "acme", "Ops@Acme.example")}
	Expect(store.PutAccount(ctx, rec, nil, op.PutAccountOptions{Exclusive: true})).To(Succeed())
	Expect(rec.Version.Ver).NotTo(BeZero())
	got, err := store.GetAccount(ctx, "RGW00000000000000001")
	Expect(err).NotTo(HaveOccurred())
	Expect(got.Info.Name).To(Equal("acme"))
	Expect(objectExists(ctx, accountPool, "name.t$acme")).To(BeTrue())
	Expect(objectExists(ctx, emailPool, "ops@acme.example")).To(BeTrue(), "lower-cased")
	byName, err := store.GetAccountByName(ctx, "t", "acme")
	Expect(err).NotTo(HaveOccurred())
	Expect(byName.Info.ID).To(Equal("RGW00000000000000001"))
	byEmail, err := store.GetAccountByEmail(ctx, "OPS@acme.example")
	Expect(err).NotTo(HaveOccurred())
	Expect(byEmail.Info.ID).To(Equal("RGW00000000000000001"))
	Expect(store.PutAccount(ctx, rec, nil, op.PutAccountOptions{Exclusive: true})).To(MatchError(op.ErrAccountAlreadyExists))
})
It("refuses a name or email another account holds and moves indexes on change", func(ctx SpecContext) { /* second account with name "acme" → ErrAccountAlreadyExists; rename acme → acme2: "name.t$acme" gone, "name.t$acme2" present; RemoveAccount removes account, name, email and users objects */ })
It("does not read a user's email as an account", func(ctx SpecContext) {
	writeUID(ctx, emailPool, "alice@example.com", "alice") // a user's RGWUID redirect
	_, err := store.GetAccountByEmail(ctx, "alice@example.com")
	Expect(err).To(MatchError(op.ErrNoSuchEntity))
})
It("keeps the account users index", func(ctx SpecContext) {
	createAccount(ctx, "RGW00000000000000001")
	Expect(store.AddAccountUser(ctx, "RGW00000000000000001", meta.UserInfo{UserID: meta.UserID{ID: "u1"}, DisplayName: "Alice", Path: "/"})).To(Succeed())
	Expect(store.AddAccountUser(ctx, "RGW00000000000000001", meta.UserInfo{UserID: meta.UserID{ID: "u1"}, DisplayName: "Alice", Path: "/"})).To(Succeed(), "not exclusive")
	ids, next, err := store.ListAccountUsers(ctx, "RGW00000000000000001", "", 10)
	Expect(err).NotTo(HaveOccurred())
	Expect(ids).To(Equal([]string{"u1"}))
	Expect(next).To(Equal("alice"), "the raw lower-cased key")
	Expect(store.RemoveAccountUser(ctx, "RGW00000000000000001", "Alice")).To(Succeed())
	Expect(store.RemoveAccountUser(ctx, "RGW00000000000000001", "Alice")).To(Succeed(), "ENOENT tolerated")
	ids, _, _ = store.ListAccountUsers(ctx, "RGW00000000000000002", "", 10)
	Expect(ids).To(BeEmpty(), "missing index object")
	name, err := store.AccountName(ctx, "RGW00000000000000001")
	Expect(err).NotTo(HaveOccurred())
	Expect(name).To(Equal("acme"))
})
```

- [ ] **Step 4: Implement `internal/driver/account.go`; drop the stubs**

As specified; `GetAccount` replaces A's `ErrNotImplemented` stub (A's `Verifier` starts resolving account users on the real driver from here). PASS.

- [ ] **Step 5: Write the failing op and handler specs**

`internal/op/adminaccount_test.go`: create with an explicit id and name → `Result.ID`/`Name`; create twice → `ErrAccountAlreadyExists`; create without id → a generated `RGW` + 17 digits (`MatchRegexp(`^RGW[0-9]{17}$`)`); name with `$` → `ErrInvalidArgument`; get by id, by name+tenant, by email; get with nothing → `ErrInvalidArgument`; get missing → `ErrNoSuchKey`; modify name; modify tenant → `ErrInvalidArgument`; modify with `QuotaScope: "account"`, `QuotaMaxSize 1073741824`, `QuotaMaxObjects 1000`, `QuotaEnabled true` → `Result.Quota` equal, then `bucket` scope → `Result.BucketQuota`; modify to a taken name → `ErrBucketAlreadyExists`; remove with a user in the account → `ErrBucketNotEmpty`, with a bucket → `ErrBucketAlreadyExists`, empty → success then `ErrNoSuchKey`; caps per release: `GetAccountInfo` succeeds with `account=read` on Squid and fails on Tentacle, and the reverse with `accounts=read`.

`internal/admin/account_test.go`: `POST /admin/account?id=RGW12345678901234567&name=test-account` → 200 `{"id":"RGW12345678901234567",…,"name":"test-account",…}`, again → 409 `AccountAlreadyExists`; `GET /admin/account?id=…` → 200; `GET /admin/account?id=nosuch` → 404 `NoSuchKey`; `PUT /admin/account?id=…&name=modified-account` → 200 with the new name; `PUT /admin/account?quota&id=…&quota-type=account&max-size=1073741824&max-objects=1000&enabled=true` on Tentacle → 200 and `GET` shows `quota.max_size 1073741824`, `quota.enabled true`; the same on Squid → 200 with the quota unchanged; `PUT …?quota&id=…&quota-type=bogus` on Tentacle → 400; `DELETE /admin/account?id=…` → 200, again → 404 `NoSuchKey`; `POST /admin/account?name=quota-test-account` → 200 with a generated id; `GET /admin/account?id=RGW12345678901234567&format=xml` → 200 whose body starts `<?xml version="1.0" encoding="UTF-8"?><AccountInfo><id>RGW12345678901234567</id><tenant></tenant><name>test-account</name><email></email><quota><enabled>false</enabled>` and ends `<max_users>1000</max_users><max_roles>1000</max_roles><max_groups>1000</max_groups><max_buckets>1000</max_buckets><max_access_keys>4</max_access_keys></AccountInfo>`.

- [ ] **Step 6: Implement `internal/op/adminaccount.go`, `internal/admin/account.go` and `AccountInfo.Dump`; `make check`; commit**

`internal/admin/account.go` renders create, modify, the Tentacle quota op and get through `WriteBody`, as `rgw::account::create`, `modify` and `info` start their flusher ([rgw_account.cc:172-174](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_account.cc#L172-L174), [:267-269](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_account.cc#L267-L269), [:491-493](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_account.cc#L491-L493)): an `AccountInfo` section around `o.Result.Dump(f, rel)`; delete → `WriteEmpty(w, r, 200)`. `internal/meta/dump_account.go`:

```go
package meta

// Dump is RGWAccountInfo::dump (rgw_common.cc:3026-3039; v20.2.4 :3088-3101).
func (a AccountInfo) Dump(f formatter.Formatter, rel denc.Release) {
	f.DumpString("id", a.ID)
	f.DumpString("tenant", a.Tenant)
	f.DumpString("name", a.Name)
	f.DumpString("email", a.Email)
	dumpSection(f, "quota", a.Quota, rel)
	dumpSection(f, "bucket_quota", a.BucketQuota, rel)
	f.DumpInt("max_users", int64(a.MaxUsers))
	f.DumpInt("max_roles", int64(a.MaxRoles))
	f.DumpInt("max_groups", int64(a.MaxGroups))
	f.DumpInt("max_buckets", int64(a.MaxBuckets))
	f.DumpInt("max_access_keys", int64(a.MaxAccessKeys))
}
```

`dump_goldens_test.go` gains `It("RGWAccountInfo", func() { dumpGoldens("RGWAccountInfo", meta.DecodeAccountInfo) })`. The account-users index keeps `AccountResourceList`'s path prefix unused: no admin op filters by it (radosgw's IAM ListUsers does, outside phase 1), which the PR description states.

```bash
git add internal/cls/user internal/testutil/fakerados/cls_user.go internal/driver/account.go internal/driver/account_test.go internal/driver/store.go internal/op/adminaccount.go internal/op/adminaccount_test.go internal/admin/account.go internal/admin/account_test.go internal/meta/dump_account.go internal/meta/dump_goldens_test.go
git commit -m "feat(admin): accounts with name, email and users indexes, and the Tentacle quota op"
```

## Task 8: Metadata driver: `MetadataStore` for user, bucket, bucket.instance; the JSON decoders and the documents' `Dump` methods

**Files:**
- Create: `internal/driver/metadata.go`, `internal/driver/metadata_test.go`, `internal/meta/user_json.go`, `internal/meta/user_json_test.go`, `internal/meta/bucket_json.go`, `internal/meta/bucket_json_test.go`, `internal/meta/dump_user.go`
- Modify: `internal/op/metadata.go` (G's `MetadataEntry` gains `Doc`), `internal/meta/dump.go` (`dumpAttrs`, `ObjVersion.Dump`), `internal/meta/dump_bucket.go` (the `BucketEntryPoint`, `BucketInfo` and `BucketCompleteInfo` dumps), `internal/meta/dump_test.go`, `internal/meta/dump_goldens_test.go` (`RGWUserInfo`, `RGWBucketEntryPoint`, `RGWBucketInfo`), `internal/driver/store.go` (G's `MetadataStore` stubs replaced), `internal/memstore/admin.go` (G's memstore `MetadataStore` made section-faithful: keys, filters, the same `Data` JSON and `Doc`)

**Interfaces:**
- Consumes: Task 2's `Pool.ListObjectsFrom`; M's `sysobjs`, `userObj`, `epObj`, `instanceObj`, `readEntryPoint`/`writeEntryPoint`/`removeEntryPoint`, `readInstance`/`writeInstance`/`removeInstance`, `GetUser`/`PutUser`/`RemoveUser`, `linkBucket`/`unlinkBucket`, `initIndex`, `Placement`, `filterRGWAttrs`, `s.zone.Params.{UserUIDPool,DomainRoot}`, `meta.ObjVersion`; Task 7's `AddAccountUser`/`RemoveAccountUser`; G's frozen `op.MetadataStore`, `op.MetadataEntry{Key, Data json.RawMessage, Version, Mtime}`, `op.PutMetadataOptions{IfVersion}`; `meta.UserInfo.MarshalJSON`, `meta.BucketInfo.MarshalJSON`, `meta.BucketEntryPoint` tags; Task 3's `formatter.Formatter`, `meta.Dumper`, `dumpTime`, `dumpStrings`, `dumpMap`, `dumpSection`, `Quota.Dump`; Task 4's `Caps.DumpAs`, `IdentityType.DumpName`; Task 6's `BucketID.Dump`; phase 0's `permString`, `maskString`, `opTypeFlags`, `stringSet`, `sortedTags`, `syncPolicyEmpty` in `meta`.
- Produces:

```go
package op

// MetadataEntry gains (additive):
//
//	// Doc is the entry's document as RGWMetadataObject::dump writes it,
//	// what the admin getter renders under "data" in every format; Get sets it.
//	Doc meta.Dumper
```

```go
package meta

// UnmarshalJSON is RGWUserInfo::decode_json (rgw_common.cc:2837-2893): user_id
// parsed with ParseUserID; suspended, system and admin as bools; keys, swift_keys
// and subusers from their array forms (a key's subuser from "subuser" or the
// "user" suffix after ':'); op_mask through ParseOpTypeList; type from its name.
func (u *UserInfo) UnmarshalJSON(b []byte) error
// UnmarshalJSON is RGWUserCaps::decode_json (rgw_common.cc:2059-2069): [{"type","perm"}] with perm words.
func (c *Caps) UnmarshalJSON(b []byte) error
// UnmarshalJSON is RGWBucketEntryPoint::decode_json (driver/rados/rgw_bucket.cc:3593-3604).
func (b *BucketEntryPoint) UnmarshalJSON(data []byte) error
// UnmarshalJSON is RGWBucketInfo::decode_json (rgw_common.cc:2544-2585): num_shards,
// bi_shard_hash_type and index_type feed Layout.Current; "region" stands in for an
// absent zonegroup; has_website with a website_conf is refused (ErrOpaqueJSON: the
// configuration is carried opaque, as static websites are not implemented
// yet); a non-empty sync_policy likewise.
func (b *BucketInfo) UnmarshalJSON(data []byte) error
// AttrsJSON is encode_json for std::map<std::string, bufferlist>: [{"key": name, "val": base64}],
// [] when empty.
type AttrsJSON map[string][]byte
func (a AttrsJSON) MarshalJSON() ([]byte, error)
func (a *AttrsJSON) UnmarshalJSON(b []byte) error

// UserCompleteInfo is RGWUserCompleteInfo, the "user" section's document
// (driver/rados/rgw_user.h:731-745; v20.2.4 driver/rados/rgw_user.cc:2725-2739).
// HasAttrs is has_attrs: whether decoded JSON carried "attrs".
type UserCompleteInfo struct {
	Info     UserInfo
	Attrs    AttrsJSON
	HasAttrs bool
}
// MarshalJSON is RGWUserCompleteInfo::dump: the user's fields, then "attrs".
func (c UserCompleteInfo) MarshalJSON() ([]byte, error)
// UnmarshalJSON is RGWUserCompleteInfo::decode_json.
func (c *UserCompleteInfo) UnmarshalJSON(b []byte) error

// BucketCompleteInfo is RGWBucketCompleteInfo, the "bucket.instance"
// section's document (driver/rados/rgw_bucket.h:51-57; v20.2.4 :50-56).
type BucketCompleteInfo struct {
	Info  BucketInfo `json:"bucket_info"`
	Attrs AttrsJSON  `json:"attrs"`
}

// The dumps, each its C++ namesake's; the code is in Step 4.
func (u UserInfo) Dump(f formatter.Formatter, rel denc.Release)
func (c UserCompleteInfo) Dump(f formatter.Formatter, rel denc.Release)
func (b BucketEntryPoint) Dump(f formatter.Formatter, rel denc.Release)
func (b BucketInfo) Dump(f formatter.Formatter, rel denc.Release)
func (c BucketCompleteInfo) Dump(f formatter.Formatter, rel denc.Release)
func (v ObjVersion) Dump(f formatter.Formatter, rel denc.Release)
```

```go
package driver

// Metadata sections (RGWMetadataManager handlers registered by the rados driver).
const (
	sectionUser           = "user"
	sectionBucket         = "bucket"
	sectionBucketInstance = "bucket.instance"
)
const bucketInstancePrefix = ".bucket.meta." // RGW_BUCKET_INSTANCE_MD_PREFIX

// instanceKeyToOID and instanceOIDToKey are RGWSI_BucketInstance_SObj_Module's
// key_to_oid/oid_to_key (svc_bucket_sobj.cc:88-122): "tenant/name:id" ↔ ".bucket.meta.tenant:name:id".
func instanceKeyToOID(key string) string
func instanceOIDToKey(oid string) string
// parseInstanceKey is rgw_bucket_parse_bucket_key (rgw_bucket.cc:32-84) without the shard.
func parseInstanceKey(key string) (meta.BucketID, error)

// The op.MetadataStore methods.
func (s *Store) Get(ctx context.Context, section, key string) (op.MetadataEntry, error)
func (s *Store) Put(ctx context.Context, section, key string, e op.MetadataEntry, opts op.PutMetadataOptions) error
func (s *Store) Remove(ctx context.Context, section, key string) error
func (s *Store) List(ctx context.Context, section, marker string, max int) ([]string, string, bool, error)
```

Semantics (rgw_metadata.cc, svc_meta_be_sobj.cc, svc_user_rados.cc, svc_bucket_sobj.cc, driver/rados/rgw_user.cc and rgw_bucket.cc at v19.2.6):

- **Sections and keys.** An unknown section is `ErrNoSuchKey` (`find_handler` → ENOENT). `user`: pool `users.uid`, oid = key, keys are `rgw_user::to_str()` strings; listing skips oids ending in `.buckets`. `bucket`: pool `domain_root`, oid = key (`tenant/name` or `name`); listing keeps oids that are non-empty and do not start with `.`. `bucket.instance`: pool `domain_root`, oid = `instanceKeyToOID(key)`; listing keeps oids with the `.bucket.meta.` prefix and returns `instanceOIDToKey`.
- **Get.** Each section's document is built once and set twice, as `Data` (its JSON) and as `Doc` (the same value, which the getter dumps in the request's format). `user`: `GetUser(ParseUserID(key))` → `doc = meta.UserCompleteInfo{Info: rec.Info, Attrs: filterRGWAttrs(rec.Attrs)}`, `Version = rec.Version`, `Mtime = rec.Mtime`; `ErrNoSuchUser` → `ErrNoSuchKey`. `bucket`: `readEntryPoint` → `doc = ep`, its version and mtime; `ErrNoSuchBucket` → `ErrNoSuchKey`. `bucket.instance`: `parseInstanceKey` → `readInstance` → `doc = meta.BucketCompleteInfo{Info: info, Attrs: attrs}`, the instance's version and mtime; an instance whose website configuration or sync policy is carried opaque fails `json.Marshal` with `meta.ErrOpaqueJSON`, which Get returns wrapped. `Key` is the caller's key.
- **Put.** `e.Data` decoded per section into the same document types (a decode failure is `ErrInvalidArgument`); `e.Version` is the write version (a zero version gets `newWriteVersion()`); `opts.IfVersion` is the read version the write is conditioned on (nil: unconditional). `user` (RGWMetadataHandlerPut_User::put_checked, [rgw_user.cc:2817-2839](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_user.cc#L2817-L2839)): `PutUser(&UserRecord{Info: doc.Info, Attrs: doc.Attrs when doc.HasAttrs, Version: e.Version, Mtime: e.Mtime}, {IfVersion})` — M's `PutUser` reads the old record itself for index maintenance; then the account users index as Task 4 does (`AddAccountUser` when the account is new or changed, [svc_user_rados.cc:360-375](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_user_rados.cc#L360-L375)). `bucket` ([rgw_bucket.cc:2265-2317](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_bucket.cc#L2265-L2317)): `writeEntryPoint(ep, attrs of the old entry point when it exists, false, e.Mtime, v)`; then `put_post`: old exists and (owner changed or linked→false) → `unlinkBucket(old owner)`; new linked and (no old, old not linked, or owner changed) → `linkBucket(new owner, bucket, creation time)`. `bucket.instance` ([:2803-2946](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_bucket.cc#L2803-L2946)): `put_check`: no old instance or a different `bucket_id` → the bucket key (tenant, name, id) is taken from the KEY (`parseInstanceKey`), `Layout.Current.Layout.Type` from `Placement(info.PlacementRule)`'s index type (a rule the zone lacks is `ErrInvalidArgument`); an existing instance keeps its `ExplicitPlacement` and `PlacementRule`; `writeInstance(info, attrs, false, e.Mtime, v)`; `put_post`: `initIndex(&info)` for a Normal layout (idempotent: shards already present are fine — M's `initIndex` is an exclusive create per shard, so tolerate `EEXIST`); the LC config and topic mappings are phase 2/3 and skipped with a debug log naming the lines. `ECANCELED` → `ErrConcurrentModification`. Sync-type decisions (`check_versions`) are the op's (Task 9); the store only writes.
- **Remove.** `user`: `GetUser` then `RemoveUser(rec)` ([rgw_user.cc:2772-2787](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_user.cc#L2772-L2787)) plus `RemoveAccountUser` for an account user; `bucket` ([rgw_bucket.cc:2185-2213](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_bucket.cc#L2185-L2213)): `readEntryPoint`, `unlinkBucket(ep.Owner, ep.Bucket)` (no ep update; a failure only logged), `removeEntryPoint` (a failure only logged), return nil — radosgw's remove is idempotent; a missing entry point is `ErrNoSuchKey` (the read fails first). `bucket.instance` ([:2707-2725](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_bucket.cc#L2707-L2725)): `readInstance` (missing → `ErrNoSuchKey`), `removeInstance` under its version.
- **List.** `Pool.ListObjectsFrom(pool, marker, max, fn)` on the section's pool and namespace, `fn` applying the section's filter and key mapping; `next`/`more` from the seam. A `radosclient.ErrBadOp` from a foreign token is `ErrInvalidArgument` (Review Focus 1).

- [ ] **Step 1: Write the failing `meta` decoder and document specs**

`internal/meta/user_json_test.go`: a round trip `json.Marshal(u)` → `Unmarshal` → `Equal(u)` for a `UserInfo` with two access keys (one with a subuser), a swift key, two subusers, caps, op mask `read, write`, quotas, temp url keys, tags, group ids, an account id and type root; plus a hand-written `testdata/user_info.json` in `RGWUserInfo::dump`'s layout (keys with `create_date`, `"system": true`) decoding without error and re-marshalling to the same document — Task 13 replaces it with a document captured from `radosgw-admin user info` on the rooket cluster; `Caps` from `[{"type":"users","perm":"read, write"}]` → `{"users": CapAll}` and `"*"` → `CapAll`; a `UserCompleteInfo` with an attr marshals as the user's fields with `"attrs"` last and decodes back with `HasAttrs` true, and a document without `"attrs"` decodes with `HasAttrs` false. `internal/meta/bucket_json_test.go`: `BucketEntryPoint` round trip; `BucketInfo` round trip for a Normal 11-shard bucket (creation_time, owner, flags, zonegroup, placement_rule "default-placement/STANDARD", has_instance_obj, quota, num_shards 11 → `Layout.Current.Layout.Normal.NumShards`, bi_shard_hash_type 0, requester_pays, swift_versioning, index_type 0, reshard_status 0, new_bucket_instance_id); `"region"` accepted for zonegroup; `has_website: true` → `ErrOpaqueJSON`; `AttrsJSON` round trip with base64 values in key order; `BucketCompleteInfo` round trip.

`internal/meta/dump_goldens_test.go` gains the three document types, whose corpus dumps pin every field's order and number form:

```go
	It("RGWUserInfo", func() { dumpGoldens("RGWUserInfo", meta.DecodeUserInfo) })
	It("RGWBucketEntryPoint", func() { dumpGoldens("RGWBucketEntryPoint", meta.DecodeBucketEntryPoint) })
	It("RGWBucketInfo", func() { dumpGoldens("RGWBucketInfo", meta.DecodeBucketInfo) })
```

`internal/meta/dump_test.go` pins what the corpus cannot — the attrs, the complete-info wrappers, the XML entry names and the opaque records:

```go
	It("dumps the metadata documents as their JSON forms", func() {
		u := meta.NewUserInfo()
		u.UserID = meta.UserID{Tenant: "t", ID: "alice"}
		u.AccessKeys = map[string]meta.AccessKey{"AK": {ID: "AK", Secret: "SK", Active: true}}
		u.SubUsers = map[string]meta.SubUser{"sub": {Name: "sub", Perm: 0x1}}
		u.System = 1
		uc := meta.UserCompleteInfo{Info: u, Attrs: meta.AttrsJSON{"user.rgw.x": []byte("v")}}
		data, err := json.Marshal(uc)
		Expect(err).NotTo(HaveOccurred())
		Expect(render(uc, denc.Squid)).To(MatchJSON(data))
		bc := meta.BucketCompleteInfo{Info: meta.NewBucketInfo(), Attrs: meta.AttrsJSON{"user.rgw.acl": {2, 2}}}
		data, err = json.Marshal(bc)
		Expect(err).NotTo(HaveOccurred())
		Expect(render(bc, denc.Tentacle)).To(MatchJSON(data))
	})
	It("writes a user document in XML under radosgw's entry names", func() {
		u := meta.NewUserInfo()
		u.UserID = meta.UserID{Tenant: "t", ID: "alice"}
		u.AccessKeys = map[string]meta.AccessKey{"AK": {ID: "AK", Secret: "SK", Active: true}}
		u.SubUsers = map[string]meta.SubUser{"sub": {Name: "sub", Perm: 0x1}}
		u.System = 1
		f := formatter.NewXML(false, false)
		f.OpenObjectSection("data")
		meta.UserCompleteInfo{Info: u, Attrs: meta.AttrsJSON{"user.rgw.x": []byte("v")}}.Dump(f, denc.Squid)
		f.CloseSection()
		s := string(f.Bytes())
		Expect(s).To(HavePrefix(`<data><user_id>t$alice</user_id><display_name></display_name><email></email><suspended>0</suspended><max_buckets>1000</max_buckets>` +
			`<subusers><subuser><id>t$alice:sub</id><permissions>read</permissions></subuser></subusers>` +
			`<keys><key><user>t$alice</user><access_key>AK</access_key><secret_key>SK</secret_key><active>true</active><create_date>0.000000</create_date></key></keys>` +
			`<swift_keys></swift_keys><caps></caps><op_mask>read, write, delete</op_mask><system>true</system><default_placement></default_placement>`))
		Expect(s).To(HaveSuffix(`<group_ids></group_ids><attrs><entry><key>user.rgw.x</key><val>dg==</val></entry></attrs></data>`))
	})
	It("fails the formatter on a bucket whose website configuration is carried opaque", func() {
		b := meta.NewBucketInfo()
		b.Website = meta.RawStruct{2, 1, 0, 0, 0, 0}
		f := formatter.NewJSON(false)
		f.OpenObjectSection("x")
		meta.BucketCompleteInfo{Info: b}.Dump(f, denc.Squid)
		f.CloseSection()
		Expect(f.Err()).To(MatchError(meta.ErrOpaqueJSON))
	})
	It("dumps an object version as encode_json(obj_version) does", func() {
		Expect(render(meta.ObjVersion{Ver: 3, Tag: "_abc"}, denc.Squid)).To(Equal(`{"tag":"_abc","ver":3}`))
	})
```

- [ ] **Step 2: Implement the decoders and the document types**

Mirror the C++ field lists: `RGWUserInfo::dump` ([rgw_common.cc:2775-2835](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L2775-L2835)) with `RGWUserCompleteInfo`'s `attrs` ([driver/rados/rgw_user.h:731-745](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_user.h#L731-L745)), `RGWBucketEntryPoint::dump` ([driver/rados/rgw_bucket.cc:3580-3591](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_bucket.cc#L3580-L3591)), `RGWBucketInfo::decode_json` ([rgw_common.cc:2544-2585](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L2544-L2585), the dump at [:2515-2542](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L2515-L2542)), bufferlists as base64 ([ceph_json.cc:592-603](https://github.com/ceph/ceph/blob/v19.2.6/src/common/ceph_json.cc#L592-L603), decode [:460-471](https://github.com/ceph/ceph/blob/v19.2.6/src/common/ceph_json.cc#L460-L471)), all at v19.2.6; `BucketInfo.UnmarshalJSON` decodes into the same auxiliary layout `MarshalJSON` writes from ([bucket_info.go:349](https://github.com/jhoblitt/rgw-go/blob/50fc45c4d59953661d5c57c27ca7016bc55edeeb/internal/meta/bucket_info.go#L349)-) and applies the layout fields; the `Layout` is otherwise `meta.NewBucketLayout`-shaped for a Normal index (`Logs` from `LogLayoutFromIndex(0, current)` as `init_default_bucket_layout` does, [rgw_bucket.cc:2781-2801](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_bucket.cc#L2781-L2801)). `UserCompleteInfo` holds the user as a named field: embedding `UserInfo` would promote its `MarshalJSON` and `UnmarshalJSON` to the wrapper, which would then drop `"attrs"` both ways.

```go
// MarshalJSON is RGWUserCompleteInfo::dump: the user's fields, then "attrs"
// at the same level.
func (c UserCompleteInfo) MarshalJSON() ([]byte, error) {
	info, err := json.Marshal(c.Info)
	if err != nil {
		return nil, err
	}
	attrs, err := json.Marshal(c.Attrs)
	if err != nil {
		return nil, err
	}
	out := append(info[:len(info)-1:len(info)-1], `,"attrs":`...)
	return append(append(out, attrs...), '}'), nil
}

// UnmarshalJSON is RGWUserCompleteInfo::decode_json.
func (c *UserCompleteInfo) UnmarshalJSON(b []byte) error {
	if err := json.Unmarshal(b, &c.Info); err != nil {
		return err
	}
	var aux struct {
		Attrs *AttrsJSON `json:"attrs"`
	}
	if err := json.Unmarshal(b, &aux); err != nil {
		return err
	}
	c.HasAttrs = aux.Attrs != nil
	if c.HasAttrs {
		c.Attrs = *aux.Attrs
	}
	return nil
}
```

Run the meta suite: the decoder and JSON specs PASS; the dump specs still FAIL.

- [ ] **Step 3: Write the failing driver specs**

`internal/driver/metadata_test.go` on fakerados with M's fixtures (`alice`, bucket `plain`):

```go
It("gets and lists users, skipping the .buckets objects", func(ctx SpecContext) {
	e, err := store.Get(ctx, "user", "alice")
	Expect(err).NotTo(HaveOccurred())
	Expect(e.Version.Ver).NotTo(BeZero())
	var doc map[string]any
	Expect(json.Unmarshal(e.Data, &doc)).To(Succeed())
	Expect(doc).To(HaveKeyWithValue("user_id", "alice"))
	Expect(doc).To(HaveKey("attrs"))
	Expect(e.Doc).To(BeAssignableToTypeOf(meta.UserCompleteInfo{}))
	keys, next, more, err := store.List(ctx, "user", "", 1000)
	Expect(err).NotTo(HaveOccurred())
	Expect(keys).To(ContainElement("alice"))
	Expect(keys).NotTo(ContainElement(HaveSuffix(".buckets")))
	Expect(more).To(BeFalse())
	Expect(next).To(BeEmpty())
	_, err = store.Get(ctx, "user", "nosuch")
	Expect(err).To(MatchError(op.ErrNoSuchKey))
	_, err = store.Get(ctx, "kittens", "x")
	Expect(err).To(MatchError(op.ErrNoSuchKey))
})
It("puts a user with its indexes and account link", func(ctx SpecContext) {
	e, _ := store.Get(ctx, "user", "alice")
	var u meta.UserCompleteInfo
	Expect(json.Unmarshal(e.Data, &u)).To(Succeed())
	u.Info.DisplayName = "Alice Renamed"
	u.Info.Email = "alice2@example.com"
	data, _ := json.Marshal(u)
	Expect(store.Put(ctx, "user", "alice", op.MetadataEntry{Data: data, Version: meta.ObjVersion{Ver: e.Version.Ver + 1, Tag: e.Version.Tag}}, op.PutMetadataOptions{IfVersion: &e.Version})).To(Succeed())
	rec, _ := store.GetUserByEmail(ctx, "alice2@example.com")
	Expect(rec.Info.DisplayName).To(Equal("Alice Renamed"))
	stale := e.Version
	Expect(store.Put(ctx, "user", "alice", op.MetadataEntry{Data: data}, op.PutMetadataOptions{IfVersion: &stale})).To(MatchError(op.ErrConcurrentModification))
})
It("serves bucket entry points and bucket instances under radosgw's keys", func(ctx SpecContext) {
	rec, _ := store.GetBucket(ctx, "", "plain")
	e, err := store.Get(ctx, "bucket", "plain")
	Expect(err).NotTo(HaveOccurred())
	var ep meta.BucketEntryPoint
	Expect(json.Unmarshal(e.Data, &ep)).To(Succeed())
	Expect(ep.Bucket.ID).To(Equal(rec.Info.Bucket.ID))
	Expect(e.Doc).To(BeAssignableToTypeOf(meta.BucketEntryPoint{}))
	key := "plain:" + rec.Info.Bucket.ID
	e, err = store.Get(ctx, "bucket.instance", key)
	Expect(err).NotTo(HaveOccurred())
	var bi meta.BucketCompleteInfo
	Expect(json.Unmarshal(e.Data, &bi)).To(Succeed())
	Expect(bi.Info.Bucket.Name).To(Equal("plain"))
	Expect(bi.Attrs).To(HaveKey(meta.AttrACL))
	Expect(e.Doc).To(BeAssignableToTypeOf(meta.BucketCompleteInfo{}))
	keys, _, _, _ := store.List(ctx, "bucket", "", 1000)
	Expect(keys).To(ConsistOf("plain"))
	keys, _, _, _ = store.List(ctx, "bucket.instance", "", 1000)
	Expect(keys).To(ConsistOf(key))
	createBucket(ctx, "t1", "tb", alice)
	keys, _, _, _ = store.List(ctx, "bucket", "", 1000)
	Expect(keys).To(ConsistOf("plain", "t1/tb"))
	keys, _, _, _ = store.List(ctx, "bucket.instance", "", 1000)
	Expect(keys).To(ContainElement(HavePrefix("t1/tb:")))
})
It("relinks on an entry point put whose owner changed", func(ctx SpecContext) {
	e, _ := store.Get(ctx, "bucket", "plain")
	var ep meta.BucketEntryPoint
	_ = json.Unmarshal(e.Data, &ep)
	ep.Owner = meta.UserOwner(bob)
	data, _ := json.Marshal(ep)
	Expect(store.Put(ctx, "bucket", "plain", op.MetadataEntry{Data: data}, op.PutMetadataOptions{IfVersion: &e.Version})).To(Succeed())
	Expect(listOwnerBuckets(ctx, meta.UserOwner(bob))).To(ConsistOf("plain"))
	Expect(listOwnerBuckets(ctx, meta.UserOwner(alice))).To(BeEmpty())
})
It("creates a bucket instance from a put and initialises its index", func(ctx SpecContext) {
	info := meta.NewBucketInfo() // with Bucket{Name: "fresh", ID: "z.1.9", Marker: "z.1.9"}, Owner alice, PlacementRule default, Zonegroup id
	data, _ := json.Marshal(meta.BucketCompleteInfo{Info: info})
	Expect(store.Put(ctx, "bucket.instance", "fresh:z.1.9", op.MetadataEntry{Data: data}, op.PutMetadataOptions{})).To(Succeed())
	got, err := store.GetBucketInstance(ctx, meta.BucketID{Name: "fresh", ID: "z.1.9"})
	Expect(err).NotTo(HaveOccurred())
	Expect(got.Info.Layout.Current.Layout.Type).To(Equal(meta.IndexNormal))
	Expect(shardObjectsExist(ctx, &got.Info)).To(BeTrue())
})
It("removes: user with indexes, entry point idempotently, instance under its version", func(ctx SpecContext) { /* ... ErrNoSuchKey on a missing key for each section */ })
It("pages listings with the seam's tokens and rejects a foreign marker", func(ctx SpecContext) {
	for i := range 5 { createUser(ctx, fmt.Sprintf("u%d", i)) }
	var all []string
	marker := ""
	for {
		keys, next, more, err := store.List(ctx, "user", marker, 2)
		Expect(err).NotTo(HaveOccurred())
		all = append(all, keys...)
		if !more { break }
		marker = next
	}
	Expect(all).To(ContainElements("u0", "u1", "u2", "u3", "u4", "alice"))
	_, _, _, err := store.List(ctx, "user", "3:b55a9110:root::u0:head", 2)
	Expect(err).To(MatchError(op.ErrInvalidArgument))
})
```

- [ ] **Step 4: Implement the dumps, `internal/driver/metadata.go` and memstore parity; `make check`; commit**

`internal/meta/dump_user.go`:

```go
package meta

// Dump is RGWUserInfo::dump (rgw_common.cc:2775-2835; v20.2.4 :2837-2897).
func (u UserInfo) Dump(f formatter.Formatter, rel denc.Release) {
	user := u.UserID.String()
	f.DumpString("user_id", user)
	f.DumpString("display_name", u.DisplayName)
	f.DumpString("email", u.Email)
	f.DumpInt("suspended", int64(u.Suspended))
	f.DumpInt("max_buckets", int64(u.MaxBuckets))
	// encode_json_map with an object name and a callback writes one named
	// section per entry (ceph_json.h:659-690): RGWSubUser::dump(f, user),
	// rgw_common.cc:2913-2922.
	f.OpenArraySection("subusers")
	for _, name := range slices.Sorted(maps.Keys(u.SubUsers)) {
		s := u.SubUsers[name]
		f.OpenObjectSection("subuser")
		f.DumpString("id", user+":"+s.Name)
		f.DumpString("permissions", permString(s.Perm))
		f.CloseSection()
	}
	f.CloseSection()
	dumpUserKeys(f, "keys", user, u.AccessKeys, false)
	dumpUserKeys(f, "swift_keys", user, u.SwiftKeys, true)
	u.Caps.DumpAs(f, "caps")
	f.DumpString("op_mask", maskString(opTypeFlags, u.OpMask))
	if u.System != 0 { // "no need to show it for every user" (:2794)
		f.DumpBool("system", true)
	}
	if u.Admin != 0 {
		f.DumpBool("admin", true)
	}
	f.DumpString("default_placement", u.DefaultPlacement.Name)
	f.DumpString("default_storage_class", u.DefaultPlacement.StorageClass)
	dumpStrings(f, "placement_tags", u.PlacementTags)
	dumpSection(f, "bucket_quota", u.BucketQuota, rel)
	dumpSection(f, "user_quota", u.UserQuota, rel)
	dumpMap(f, "temp_url_keys", u.TempURLKeys,
		func(f formatter.Formatter, k int32) { f.DumpInt("key", int64(k)) },
		func(f formatter.Formatter, v string) { f.DumpString("val", v) })
	f.DumpString("type", u.Type.DumpName())
	dumpStrings(f, "mfa_ids", stringSet(u.MFAIDs))
	f.DumpString("account_id", u.AccountID)
	f.DumpString("path", u.Path)
	dumpTime(f, "create_date", u.CreateDate)
	f.OpenArraySection("tags")
	for _, t := range sortedTags(u.Tags) {
		f.OpenObjectSection("entry")
		f.DumpString("key", t.Key)
		f.DumpString("val", t.Value)
		f.CloseSection()
	}
	f.CloseSection()
	dumpStrings(f, "group_ids", stringSet(u.GroupIDs))
}

// dumpUserKeys is encode_json_map over access_keys or swift_keys with
// user_info_dump_key or user_info_dump_swift_key (rgw_common.cc:2620-2630):
// RGWAccessKey::dump(f, user, swift), which names the owner with the subuser
// appended and leaves out a Swift key's id (:2974-2988).
func dumpUserKeys(f formatter.Formatter, name, user string, keys map[string]AccessKey, swift bool) {
	f.OpenArraySection(name)
	for _, id := range slices.Sorted(maps.Keys(keys)) {
		k := keys[id]
		owner := user
		if k.Subuser != "" {
			owner += ":" + k.Subuser
		}
		f.OpenObjectSection("key")
		f.DumpString("user", owner)
		if !swift {
			f.DumpString("access_key", k.ID)
		}
		f.DumpString("secret_key", k.Secret)
		f.DumpBool("active", k.Active)
		dumpTime(f, "create_date", k.CreatedAt)
		f.CloseSection()
	}
	f.CloseSection()
}

// Dump is RGWUserCompleteInfo::dump.
func (c UserCompleteInfo) Dump(f formatter.Formatter, rel denc.Release) {
	c.Info.Dump(f, rel)
	dumpAttrs(f, "attrs", c.Attrs)
}
```

`internal/meta/dump_bucket.go` gains:

```go
// Dump is RGWBucketEntryPoint::dump (driver/rados/rgw_bucket.cc:3580-3591;
// v20.2.4 :3681-3692).
func (b BucketEntryPoint) Dump(f formatter.Formatter, rel denc.Release) {
	dumpSection(f, "bucket", b.Bucket, rel)
	f.DumpString("owner", b.Owner.String())
	dumpTime(f, "creation_time", b.CreationTime)
	f.DumpBool("linked", b.Linked)
	f.DumpBool("has_bucket_info", b.HasBucketInfo)
	if b.HasBucketInfo && b.OldBucketInfo != nil {
		dumpSection(f, "old_bucket_info", *b.OldBucketInfo, rel)
	}
}

// Dump is RGWBucketInfo::dump (rgw_common.cc:2515-2542; v20.2.4 :2577-2604).
// The website configuration and the sync policy are carried opaque, so a
// bucket with either fails the formatter as MarshalJSON fails.
func (b BucketInfo) Dump(f formatter.Formatter, rel denc.Release) {
	if b.HasWebsite() {
		f.Fail(fmt.Errorf("%w: website_conf", ErrOpaqueJSON))
		return
	}
	if b.SyncPolicy != nil && !syncPolicyEmpty(b.SyncPolicy) {
		f.Fail(fmt.Errorf("%w: sync_policy", ErrOpaqueJSON))
		return
	}
	current := b.Layout.Current.Layout
	dumpSection(f, "bucket", b.Bucket, rel)
	dumpTime(f, "creation_time", b.CreationTime)
	f.DumpString("owner", b.Owner.String())
	f.DumpUnsigned("flags", uint64(b.Flags))
	f.DumpString("zonegroup", b.Zonegroup)
	f.DumpString("placement_rule", b.PlacementRule.String())
	f.DumpBool("has_instance_obj", b.HasInstanceObj)
	dumpSection(f, "quota", b.Quota, rel)
	f.DumpUnsigned("num_shards", uint64(current.Normal.NumShards))
	f.DumpUnsigned("bi_shard_hash_type", uint64(current.Normal.HashType))
	f.DumpBool("requester_pays", b.RequesterPays)
	f.DumpBool("has_website", false)
	f.DumpBool("swift_versioning", b.SwiftVersioning)
	f.DumpString("swift_ver_location", b.SwiftVerLocation)
	f.DumpUnsigned("index_type", uint64(current.Type))
	dumpMap(f, "mdsearch_config", b.MDSearchConfig,
		func(f formatter.Formatter, k string) { f.DumpString("key", k) },
		func(f formatter.Formatter, v uint32) { f.DumpUnsigned("val", uint64(v)) })
	f.DumpInt("reshard_status", int64(b.ReshardStatus))
	f.DumpString("new_bucket_instance_id", b.NewBucketInstanceID)
}

// Dump is RGWBucketCompleteInfo::dump (driver/rados/rgw_bucket.cc:2112-2115;
// v20.2.4 :2278-2281).
func (c BucketCompleteInfo) Dump(f formatter.Formatter, rel denc.Release) {
	dumpSection(f, "bucket_info", c.Info, rel)
	dumpAttrs(f, "attrs", c.Attrs)
}
```

`internal/meta/dump.go` gains:

```go
// dumpAttrs is encode_json of a std::map<std::string, bufferlist>: "entry"
// sections of "key" and "val" (ceph_json.h:597-607), each value base64 with
// padding and no line breaks (ceph_json.cc:592-603; ceph_armor, armor.c:94-97).
func dumpAttrs(f formatter.Formatter, name string, attrs map[string][]byte) {
	dumpMap(f, name, attrs,
		func(f formatter.Formatter, k string) { f.DumpString("key", k) },
		func(f formatter.Formatter, v []byte) { f.DumpString("val", base64.StdEncoding.EncodeToString(v)) })
}

// Dump is the body of encode_json(name, obj_version) (rgw_metadata.cc:41-47;
// v20.2.4 :37-43).
func (v ObjVersion) Dump(f formatter.Formatter, _ denc.Release) {
	f.DumpString("tag", v.Tag)
	f.DumpUnsigned("ver", v.Ver)
}
```

Run the meta suite: PASS. `internal/driver/metadata.go` per the semantics above; the memstore's `MetadataStore` (G's) is reshaped to the same sections, keys, filters, `Data` documents and `Doc` values so the Task 9 handler specs run on it. Run the driver and memstore suites: PASS.

```bash
git add internal/driver/metadata.go internal/driver/metadata_test.go internal/driver/store.go internal/meta/user_json.go internal/meta/user_json_test.go internal/meta/bucket_json.go internal/meta/bucket_json_test.go internal/meta/dump.go internal/meta/dump_user.go internal/meta/dump_bucket.go internal/meta/dump_test.go internal/meta/dump_goldens_test.go internal/op/metadata.go internal/memstore/admin.go
git commit -m "feat(driver): metadata sections user, bucket and bucket.instance with radosgw's keys and documents"
```

## Task 9: Metadata REST ops and `user?list`

**Files:**
- Create: `internal/op/adminmetadata.go`, `internal/op/adminmetadata_test.go`, `internal/admin/metadata.go`, `internal/admin/metadata_test.go`
- Modify: `internal/admin/handler.go` (`newMetadataHandlers()`), `internal/admin/user.go` (`list_user` handler; the op exists since Task 4), `docs/exclusions.md`

**Interfaces:**
- Consumes: Task 8's `MetadataStore` semantics and `MetadataEntry.Doc`; G's `op.MetadataStore`, `op.MetadataEntry`; `meta.ObjVersion` and Task 8's `ObjVersion.Dump`, `meta.Time` and Task 3's `Time.Gmtime`; Task 3's `WriteBody`, `WriteDumped`, `WriteEmpty`, `formatter.Formatter`; Task 4's `op.ListUsers`; `op.ErrMissingContentLength`, `op.ErrInvalidArgument`, `op.ErrNoSuchKey`, `op.ErrConcurrentModification`.
- Produces:

```go
package op

// SyncType is RGWMDLogSyncType (rgw_rest_metadata.cc:221-232): update-by-version
// (APPLY_UPDATES), update-by-timestamp (APPLY_NEWER), always (APPLY_ALWAYS).
type SyncType uint8
const (
	SyncAlways SyncType = iota
	SyncByVersion
	SyncByTimestamp
)
func ParseSyncType(s string) (SyncType, bool)

// GetMetadata is RGWOp_Metadata_Get (rgw_rest_metadata.cc:54-69). Cap metadata=read.
type GetMetadata struct {
	AdminOp
	Section, Key string
	Result       MetadataEntry
}
// ListMetadata is RGWOp_Metadata_List (:77-174). Cap metadata=read.
type ListMetadata struct {
	AdminOp
	Section    string
	Marker     string // already base64-decoded by the handler
	MaxEntries uint64 // 0 with !Extended: everything
	Extended   bool   // max-entries was given
	Keys       []string
	Truncated  bool
	Count      uint64
	NextMarker string
}
// PutMetadata is RGWOp_Metadata_Put + RGWMetadataManager::put and
// RGWMetadataHandlerPut_SObj::put_pre (:234-288; rgw_metadata.cc:501-552, :255-278;
// check_versions rgw_metadata.h:196-218). Cap metadata=write.
type PutMetadata struct {
	AdminOp
	Section, Key string
	Body         []byte // the request body
	Sync         SyncType
	Applied      bool           // false: STATUS_NO_APPLY ("skipped")
	OnDisk       meta.ObjVersion // the version read before the write (RGWX_UPDATE_VERSION)
}
// RemoveMetadata is RGWOp_Metadata_Delete (:290-300). Cap metadata=write.
type RemoveMetadata struct {
	AdminOp
	Section, Key string
}
```

```go
package admin

func newMetadataHandlers() map[string]HandlerFunc // get_metadata, get_metadata_myself, list_metadata, set_metadata, remove_metadata

// dumpMetadataInfo is RGWMetadataManager::get's document (rgw_metadata.cc:485-494;
// v20.2.4 :280-289), which the op writes without starting the flusher.
func dumpMetadataInfo(f formatter.Formatter, key string, e op.MetadataEntry, rel denc.Release) {
	f.OpenObjectSection("metadata_info")
	f.DumpString("key", key)
	f.OpenObjectSection("ver")
	e.Version.Dump(f, rel)
	f.CloseSection()
	// real_clock::is_zero: the epoch, which Go's zero time also stands for.
	if !e.Mtime.IsZero() && !e.Mtime.Equal(time.Unix(0, 0)) {
		f.DumpStream("mtime", meta.Time{Time: e.Mtime}.Gmtime())
	}
	f.OpenObjectSection("data")
	e.Doc.Dump(f, rel)
	f.CloseSection()
	f.CloseSection()
}

// dumpMetadataKeys is RGWOp_Metadata_List's document (rgw_rest_metadata.cc:134-170,
// both tags): the bare "keys" array unless max-entries was given, for
// backward compatibility (:98), and the marker base64-wrapped (:164-168).
func dumpMetadataKeys(f formatter.Formatter, o *op.ListMetadata) {
	if o.Extended {
		f.OpenObjectSection("result")
	}
	f.OpenArraySection("keys")
	for _, k := range o.Keys {
		f.DumpString("key", k)
	}
	f.CloseSection()
	if o.Extended {
		f.DumpBool("truncated", o.Truncated)
		f.DumpUnsigned("count", o.Count)
		if o.Truncated {
			f.DumpString("marker", base64.StdEncoding.EncodeToString([]byte(o.NextMarker)))
		}
		f.CloseSection()
	}
}

// dumpUserList is RGWUser::list's document (driver/rados/rgw_user.cc:2295-2323;
// v20.2.4 :2301-2329): count through dump_int and the marker unwrapped.
func dumpUserList(f formatter.Formatter, o *op.ListUsers) {
	f.OpenObjectSection("result")
	f.OpenArraySection("keys")
	for _, k := range o.Keys {
		f.DumpString("key", k)
	}
	f.CloseSection()
	f.DumpBool("truncated", o.Truncated)
	f.DumpInt("count", int64(o.Count)) //nolint:gosec // RGWUser::list caps max-entries at 1000
	if o.Truncated {
		f.DumpString("marker", o.NextMarker)
	}
	f.CloseSection()
}
```

Semantics:

- **frame_metadata_key** ([rgw_rest_metadata.cc:35-52](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_metadata.cc#L35-L52)): section = the path's second segment (`q.Sub`); when it is empty the `key` argument is the section and the key is empty. `?myself` sets the key to the requester's owner id (`r.Identity.Owner.String()`).
- **GetMetadata**: `Env.Metadata.Get(section, key)`; the handler renders `dumpMetadataInfo(f, framed, o.Result, rel)` through `WriteDumped` — `key` carries the framed `section:key` (just `section` for a section-only request), `ver` the `tag`/`ver` object, `mtime` a `gmtime` stream when non-zero, `data` the entry's `Doc`. The get and list ops write into the request's formatter with no flusher start, so their bodies carry no XML declaration or HTML status page (N-D2). A `Doc` that calls `Fail` — a bucket instance with a website configuration or sync policy carried opaque — answers 501 NotImplemented (N-D11); `Get` already fails the same way when it marshals `Data`.
- **ListMetadata**: `marker` base64-decoded by the handler (standard base64; an undecodable marker is `""`, [rgw_rest_metadata.cc:83-92](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_metadata.cc#L83-L92)); `max-entries` present → `Extended`, a non-integer → `ErrInvalidArgument`; loop `Env.Metadata.List(section, marker, left)` with `left = MaxEntries - count` (Extended) or 1000, appending keys, until `!more || left == 0`; `Truncated = more`, `NextMarker = next`. Rendered by `dumpMetadataKeys` through `WriteDumped`: JSON gives the bare array `["a","b"]` or `{"keys":[…],"truncated":b,"count":n,"marker":"…"}` (the marker only when truncated, `rgw::to_base64`, standard alphabet with padding); XML gives `<keys><key>a</key>…</keys>` or `<result><keys>…</keys><truncated>…</truncated><count>…</count>[<marker>…</marker>]</result>`. Unknown section → 404 `NoSuchKey`.
- **PutMetadata**: the handler reads the body: `Content-Length` > 0 or `Transfer-Encoding: chunked`, else `op.ErrMissingContentLength` (411); at most `rgw_max_put_param_size`... radosgw has no cap here; cap at 16 MiB and answer `ErrInvalidArgument` beyond; read to EOF and honour D-A2's verification error — the route's payload forms (Task 3's table) let this op alone take a signed body, as `RGW_OP_ADMIN_SET_METADATA` alone among the admin ops is on radosgw's single-chunk list ([rgw_rest_s3.cc:5916](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L5916); v20.2.4 [:6483](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L6483)). `update-type` unknown → `ErrInvalidArgument`. The op: parse the body as JSON (`ErrInvalidArgument` when not); the body's `"key"`, when present, REPLACES the framed key for the handler lookup ([rgw_metadata.cc:528](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_metadata.cc#L528), `decode_json("key", metadata_key)`), and its section/entry are split at the first `:`; `"ver"` → the incoming version; `"mtime"` → `meta.Time`; `"data"` required else `ErrInvalidArgument`. `put_pre`: `Get(section, key)` → exists/ondisk version/mtime (`ErrNoSuchKey` → not exists); `check_versions(exists, ondisk, ondiskTime, incoming, incomingTime, Sync)`: `SyncByVersion` → apply only when `ondisk.Tag == incoming.Tag && incoming.Ver > ondisk.Ver`; `SyncByTimestamp` → `incomingTime.After(ondiskTime)`; `SyncAlways` → apply; not applied → `Applied = false`, `OnDisk = ondisk`, success. Applied: `Env.Metadata.Put(section, key, MetadataEntry{Key, Data, Version: incoming, Mtime: incomingTime}, {IfVersion: &ondisk when exists})`, `Applied = true`, `OnDisk = ondisk`. Response: `WriteEmpty(w, r, 204)` with headers `RGWX_UPDATE_STATUS: applied|skipped` and `RGWX_UPDATE_VERSION: ver:<OnDisk.Ver>,tag:<OnDisk.Tag>` written with the literal spelling (`w.Header()["RGWX_UPDATE_STATUS"] = …`, not `Set`, whose canonical form would change the case). `send_response` writes `RGWX_UPDATE_VERSION` on every response the op produces, errors included ([rgw_rest_metadata.cc:276-288](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_metadata.cc#L276-L288), both tags), so the handler sets it before it returns any error of its own — the body read, `update-type`, the op — with `ver:0,tag:` when nothing was read; an error from authentication or the cap check precedes the op, as `abort_early` does, and carries neither header.
- **RemoveMetadata**: `Env.Metadata.Remove(section, key)`; `WriteEmpty(w, r, 200)`.
- **list_user** (`GET /admin/user?list`): Task 4's `ListUsers` rendered by `dumpUserList` through `WriteBody`, as `RGWUser::list` starts the flusher ([rgw_user.cc:2293](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_user.cc#L2293); v20.2.4 [:2299](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_user.cc#L2299)): `{"keys":[…],"truncated":b,"count":n[,"marker":raw]}` in JSON, `<?xml version="1.0" encoding="UTF-8"?><result><keys><key>…</key></keys><truncated>…</truncated><count>…</count></result>` in XML.

- [ ] **Step 1: Write the failing op specs**

`internal/op/adminmetadata_test.go` on memstore: get user/bucket/bucket.instance entries (`Result.Version` with its tag, `Result.Mtime` set, `Result.Data` decoding as the section's document and `Result.Doc` holding it: `meta.UserCompleteInfo`, `meta.BucketEntryPoint`, `meta.BucketCompleteInfo`); list `user` extended with `MaxEntries 1` → one key, truncated, marker set; list all → every key; put with `SyncByVersion` and a lower version → `Applied false`; with a higher version and the same tag → applied and `GetUser` shows the change; with a different tag → not applied; `SyncByTimestamp` newer/older; `SyncAlways` applies regardless; a body without `data` → `ErrInvalidArgument`; a body whose `key` names another entry writes THAT entry; remove then get → `ErrNoSuchKey`.

- [ ] **Step 2: Implement `internal/op/adminmetadata.go`**

PASS.

- [ ] **Step 3: Write the failing handler specs**

`internal/admin/metadata_test.go`: `GET /admin/metadata/user` → bare array of user ids (go-ceph's `GetUsers`: `["admin","leseb"]`); `GET /admin/metadata/user?max-entries=1` → `{"keys":[…],"truncated":true,"count":1,"marker":"<base64>"}` and a second request with that marker continues; `GET /admin/metadata/user?key=admin` → `{"key":"user:admin","ver":{"tag":…,"ver":…},"mtime":"…","data":{…"user_id":"admin"…,"attrs":[…]}}` whose `data` keys come in `RGWUserInfo::dump`'s order with `attrs` last; `GET /admin/metadata/user?myself` as `admin` → the same; `GET /admin/metadata/bucket?key=test` → entry point; `GET /admin/metadata/bucket.instance?key=test:<id>` → `{"data":{"bucket_info":…,"attrs":…}}`; a bucket instance given a website configuration in the memstore → 501 `NotImplemented`; `GET /admin/metadata/kittens` → 404 `NoSuchKey`; `PUT /admin/metadata/user?key=admin` with the GET document and `ver.ver` incremented → 204 with `RGWX_UPDATE_STATUS: applied` and `RGWX_UPDATE_VERSION: ver:<old>,tag:<tag>`; the same again with `update-type=update-by-version` → 204 `skipped`; `PUT` without a body → 411 carrying `RGWX_UPDATE_VERSION: ver:0,tag:` and no `RGWX_UPDATE_STATUS`; `update-type=bogus` → 400; `DELETE /admin/metadata/user?key=leseb` → 200 then `GET` → 404; a `GET` with a `marker` that is not base64 → treated as the beginning (200). `GET /admin/user?list&max-entries=1` → `{"keys":[…],"truncated":true,"count":1,"marker":"<raw token>"}` whose marker is NOT base64-wrapped (it round-trips as given). XML, each with `Content-Type: application/xml`:

```go
It("renders metadata in XML without a declaration, and the user list with one", func() {
	body := get(`/admin/metadata/user?key=admin&format=xml`)
	Expect(body).To(HavePrefix(`<metadata_info><key>user:admin</key><ver><tag>`))
	Expect(body).To(ContainSubstring(`</ver><mtime>`))
	Expect(body).To(ContainSubstring(`<data><user_id>admin</user_id><display_name>`))
	Expect(body).To(ContainSubstring(`<keys><key><user>admin</user><access_key>`))
	Expect(body).To(HaveSuffix(`</attrs></data></metadata_info>`))
	Expect(get(`/admin/metadata/user?format=xml`)).To(Equal(`<keys><key>admin</key><key>leseb</key></keys>`))
	Expect(get(`/admin/metadata/user?max-entries=1&format=xml`)).To(MatchRegexp(`^<result><keys><key>admin</key></keys><truncated>true</truncated><count>1</count><marker>[A-Za-z0-9+/=]+</marker></result>$`))
	Expect(get(`/admin/user?list&format=xml`)).To(Equal(`<?xml version="1.0" encoding="UTF-8"?><result><keys><key>admin</key><key>leseb</key></keys><truncated>false</truncated><count>2</count></result>`))
})
```

(`get` issues the request as `admin` and returns the body after asserting 200; the memstore lists keys in order.)

- [ ] **Step 4: Implement `internal/admin/metadata.go` and the `list_user` handler; `make check`; commit**

`docs/exclusions.md`, "Coexistence obligations independent of any exclusion": a new bullet at the end of the admin bullets that begin with the admin headers bullet,

- **Admin listing markers are rgw-go's own.** A truncated `GET /admin/metadata/<section>` or `GET /admin/user?list` returns a marker that resumes the listing only on the gateway that issued it. radosgw's marker is a librados object cursor, and radosgw parses any marker it is given as one (`rgw_list_pool`, [rgw_tools.cc:354-358](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_tools.cc#L354-L358); v20.2.4 [:370-374](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_tools.cc#L370-L374)). rgw-go's marker names the last key it returned and resumes after that key's placement hash, because the cursor go-ceph exposes marks the next batch rather than the last key, and rgw-go answers 400 InvalidArgument for a radosgw marker. go-ceph's rgw/admin package, Rook's client, sends no marker: its `GetUsers` asks for the unpaged list. A client that pages and moves between gateways mid-listing starts the listing again.

and one sentence appended to the "Some bucket sub-records are carried opaque in phase 0" bullet: "The admin API's `GET /admin/metadata/bucket.instance` of a bucket holding a website configuration or a sync policy answers 501 NotImplemented, where radosgw renders the record."

```bash
git add internal/op/adminmetadata.go internal/op/adminmetadata_test.go internal/admin/metadata.go internal/admin/metadata_test.go internal/admin/user.go internal/admin/handler.go docs/exclusions.md
git commit -m "feat(admin): metadata get, list, put and remove over the three sections"
```

## Task 10: Usage: driver read and bounded trim, fakerados emulation of the two defects, REST show and trim

**Files:**
- Create: `internal/driver/usageadmin.go`, `internal/driver/usageadmin_test.go`, `internal/op/adminusage.go`, `internal/op/adminusage_test.go`, `internal/admin/usage.go`, `internal/admin/usage_test.go`
- Modify: `internal/testutil/fakerados/cls_rgw.go` (`user_usage_log_read`, `user_usage_log_trim`, `usage_log_clear` over M's `user_usage_log_add` emulation, with the two defects), `internal/driver/store.go` (stubs dropped), `internal/memstore/admin.go` (`ReadUsage`/`TrimUsage` already; `TrimUsage` honours the bucket filter), `internal/admin/handler.go` (`newUsageHandlers()`), `docs/exclusions.md`

**Interfaces:**
- Consumes: M's `usageOID(user, index)`, `options.usageMaxShards`/`usageMaxUserShards`, the usage log pool handle, `shardMod`; `cls/rgw` `UsageLogRead(op, UsageReadOp{StartEpoch, EndEpoch, Owner, Bucket, Iter, MaxEntries}, r) *UsageReadResult` → `UsageReadRet{Usage map[UserBucket]UsageLogEntry, Truncated, NextIter}` (confirm the field names in [`internal/cls/rgw/ops_usage.go:46-135`](https://github.com/jhoblitt/rgw-go/blob/50fc45c4d59953661d5c57c27ca7016bc55edeeb/internal/cls/rgw/ops_usage.go#L46-L135)), `UsageLogTrim(op, UsageTrimOp{StartEpoch, EndEpoch, User, Bucket}, r)`; `radosclient.ErrNotFound`, `ErrNoData` (ENODATA — add the sentinel to `radosclient/errors.go` if absent); Task 1's `op.UsageReader`, `op.UsageRecord`, `op.UsageIter`; Task 3's `Args.Epoch`, `WriteBody`, `WriteEmpty`, `formatter.Formatter`, `meta.Time.Gmtime`; `op.ErrInvalidArgument`, `op.ErrNoSuchBucket`, `op.ErrNoSuchKey`, `op.ErrNotImplemented`.
- Produces:

```go
package driver

// The op.UsageReader methods.
func (s *Store) ReadUsage(ctx context.Context, user, bucket string, start, end uint64, max uint32, it *op.UsageIter) ([]op.UsageRecord, bool, error)
func (s *Store) TrimUsage(ctx context.Context, user, bucket string, start, end uint64) error

// usageTrimRounds bounds the trim rounds for one shard: ceil(records/1000) + 1,
// since radosgw's usage trim may never answer ENODATA.
func usageTrimRounds(records int) int
// countUsage pages user_usage_log_read over one shard and counts the records
// in range, the bound's input.
func (s *Store) countUsage(ctx context.Context, oid, user, bucket string, start, end uint64) (int, error)
```

```go
package op

// ShowUsage is RGWOp_Usage_Get + RGWUsage::show (rgw_rest_usage.cc:15-70; rgw_usage.cc:33-164). Cap usage=read.
type ShowUsage struct {
	AdminOp
	UID          string // as given (rgw_user string form)
	Bucket       string // requires a loaded bucket: ErrNoSuchBucket... radosgw's load_bucket ENOENT → NoSuchKey
	Tenant       string
	Start, End   uint64 // default 0 and MaxUint64
	ShowEntries  bool   // default true
	ShowSummary  bool   // default true
	Categories   map[string]bool // empty: all
	Entries      []UsageUser // in (user, bucket) order as the pages delivered them
	Summary      []UsageSummary
}
type UsageUser struct {
	User    string
	Buckets []UsageRecord
}
type UsageSummary struct {
	User       string
	Categories map[string]UsageData // filtered
	Total      UsageData            // sum over the filtered categories
	S3Select   S3SelectUsage
}
// TrimUsage is RGWOp_Usage_Delete + RGWUsage::trim (:72-121; :166-177). Cap usage=write.
type TrimUsage struct {
	AdminOp
	UID, Bucket, Tenant string
	Start, End          uint64
	RemoveAll           bool
}
```

```go
package admin

func newUsageHandlers() map[string]HandlerFunc // get_usage, trim_usage

// dumpUsage is RGWUsage::show's document (rgw_usage.cc:50-160, both tags),
// written after the flusher starts (:48).
func dumpUsage(f formatter.Formatter, o *op.ShowUsage) {
	f.OpenObjectSection("usage")
	if o.ShowEntries {
		f.OpenArraySection("entries")
		for _, u := range o.Entries {
			f.OpenObjectSection("user")
			f.DumpString("user", u.User)
			f.OpenArraySection("buckets")
			for _, b := range u.Buckets {
				epoch := int64(b.Epoch) //nolint:gosec // an hour's epoch in seconds; dump_int writes it signed (:101)
				f.OpenObjectSection("bucket")
				f.DumpString("bucket", b.Bucket)
				f.DumpStream("time", meta.Time{Time: time.Unix(epoch, 0)}.Gmtime())
				f.DumpInt("epoch", epoch)
				f.DumpString("owner", b.Owner)
				if b.Payer != "" && b.Payer != b.Owner {
					f.DumpString("payer", b.Payer)
				}
				dumpUsageCategories(f, b.Categories, o.Categories)
				f.OpenObjectSection("s3select")
				if _, ok := o.Categories["s3select"]; len(o.Categories) == 0 || ok {
					f.DumpUnsigned("bytes_processed", b.S3Select.BytesProcessed)
					f.DumpUnsigned("bytes_returned", b.S3Select.BytesReturned)
				}
				f.CloseSection()
				f.CloseSection()
			}
			f.CloseSection()
			f.CloseSection()
		}
		f.CloseSection()
	}
	if o.ShowSummary {
		f.OpenArraySection("summary")
		for _, s := range o.Summary {
			f.OpenObjectSection("user")
			f.DumpString("user", s.User)
			dumpUsageCategories(f, s.Categories, o.Categories)
			f.OpenObjectSection("total")
			f.DumpUnsigned("bytes_sent", s.Total.BytesSent)
			f.DumpUnsigned("bytes_received", s.Total.BytesReceived)
			f.DumpUnsigned("ops", s.Total.Ops)
			f.DumpUnsigned("successful_ops", s.Total.SuccessfulOps)
			f.DumpUnsigned("bytes_processed", s.S3Select.BytesProcessed)
			f.DumpUnsigned("bytes_returned", s.S3Select.BytesReturned)
			f.CloseSection()
			f.CloseSection()
		}
		f.CloseSection()
	}
	f.CloseSection()
}

// dumpUsageCategories is dump_usage_categories_info (rgw_usage.cc:14-31): the
// categories in name order, skipping those a non-empty filter does not name.
func dumpUsageCategories(f formatter.Formatter, usage map[string]op.UsageData, filter map[string]bool) {
	f.OpenArraySection("categories")
	for _, name := range slices.Sorted(maps.Keys(usage)) {
		if _, ok := filter[name]; len(filter) > 0 && !ok {
			continue
		}
		d := usage[name]
		f.OpenObjectSection("entry")
		f.DumpString("category", name)
		f.DumpUnsigned("bytes_sent", d.BytesSent)
		f.DumpUnsigned("bytes_received", d.BytesReceived)
		f.DumpUnsigned("ops", d.Ops)
		f.DumpUnsigned("successful_ops", d.SuccessfulOps)
		f.CloseSection()
	}
	f.CloseSection()
}
```

Semantics:

- **ReadUsage** ([rgw_rados.cc:1674-1716](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L1674-L1716)): `first := usageOID(user, 0)`; `oid := usageOID(user, it.Index)` (`first` when `Index == 0`); `num := max`; loop: `UsageLogRead(oid, {start, end, user, bucket, it.ReadIter, num})`; `ENOENT` → this shard has nothing, go to `next`; error → return; `num -= len(ret)`, merge each `(user, bucket)` record into the page's map with `aggregate` (rgw_usage_log_entry::aggregate, [cls_rgw_types.h:1005-1023](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw_types.h#L1005-L1023): the first record's owner/bucket/epoch/payer, per-category and total sums); `it.ReadIter = ret.NextIter`; `next:` when not truncated: `it.ReadIter = ""`, `it.Index++`, `oid = usageOID(user, it.Index)`; while `num > 0 && !truncated && oid != first`. Return the page's records in `(User, Bucket)` order (`std::map<rgw_user_bucket>`), `truncated` as the last class reply said. When the walk wraps to `first` with nothing truncated the listing is complete: `truncated = false`. An empty `user` walks every shard (`usageMaxShards`), a user its `usageMaxUserShards` hashes.
- **TrimUsage** ([:1718-1736](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L1718-L1736) with N-D4): for `index` from 0 while `usageOID(user, index) != first` again: `n, err := countUsage(oid, …)` (pages of 1000 with the class read; `ENOENT` → 0); `rounds := usageTrimRounds(n)`; loop `rounds` times: `UsageLogTrim(oid, {start, end, user, bucket})`: `ENODATA` → break (done for this shard), `ENOENT` → break (no object), other error → return; after the loop, if the last round was not ENODATA, `slog.WarnContext(ctx, "usage trim stopped at its bound without ENODATA; payer-keyed records (tracker #72593) or a bucket filter behind other records (#58136) may remain", oid, rounds)`. Return nil.
- **fakerados** ([cls_rgw.cc:3522-3803](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L3522-L3803)): `user_usage_log_add` (M) writes both keys per record — by time `"%011d_%s_%s"` (epoch, payer-or-owner? NO: `rgw_user_usage_log_add` at [:3583](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L3583) keys by the PAYER when set: verify in the emulation against v19.2.6 lines 3563-3618 — the by-user key uses `entry.payer` when non-empty else `entry.owner`, the by-time key the same user) — and `user_usage_log_read` iterates the omap from the by-user prefix (`"<user>_%011d_"`) or the by-time prefix, filters by bucket and epoch range, aggregates into a map keyed by (payer-or-owner, bucket), honours `max_entries` (default 1000) and returns `truncated`/`next_iter` ([:3620-3750](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L3620-L3750)); `user_usage_log_trim` ([:3752-3803](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw.cc#L3752-L3803)) iterates the same way but removes the keys built from `entry.owner` (NOT the payer — the defect), returns `ENODATA` only when the 1000-key window had no match and nothing follows, 0 otherwise; `usage_log_clear` empties the omap. With a payer-keyed record the emulated trim removes nothing and returns 0 for ever, as the real class does; with a bucket filter and 1000 non-matching keys ahead it returns 0 without progress.
- **ShowUsage.Execute** ([rgw_rest_usage.cc:28-70](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_usage.cc#L28-L70), [rgw_usage.cc:33-164](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_usage.cc#L33-L164)): `Bucket != ""` → `Env.Buckets.GetBucket(Tenant, Bucket)` (`ErrNoSuchBucket` → `ErrNoSuchKey`); the bucket path is `RadosBucket::read_usage` ([rgw_sal_rados.cc:785-797](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L785-L797)): `user = the bucket owner's string`, `bucket = name`; an account-owned bucket → `ErrUnknown` (radosgw's `-ENOTSUP` has no row in `rgw_http_s3_errors`, so `set_req_state_err` "resorts to 500" UnknownError, [rgw_common.cc:322-354](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L322-L354) at v19.2.6, [:360](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L360) at v20.2.4 — the same mapping P-D9 records); else user = `UID` (may be empty). Page `ReadUsage(user, bucket, Start, End, 1000, &it)` until `!truncated` (`ENOENT` handled inside); for each record in page order: `ShowEntries` → group consecutive records by `User` into `UsageUser`s (a new `user` section whenever the user changes, [rgw_usage.cc:85-96](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_usage.cc#L85-L96), so one user can appear in several groups across pages); `summary[user].aggregate(record, Categories)`. `Summary` in user order (map) with `Categories` filtered, `Total = sum over the filtered categories`, `S3Select` aggregated when the filter is empty or names `s3select`.
- **TrimUsage.Execute** ([:85-121](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_usage.cc#L85-L121)): `Bucket != ""` → `GetBucket(Tenant, Bucket)` (missing → `ErrNoSuchKey`), user = the bucket owner (account owner → `ErrUnknown`, `-ENOTSUP` → 500 UnknownError as in ShowUsage); no uid, no bucket, `Start == 0`, `End == MaxUint64` and `!RemoveAll` → `ErrInvalidArgument`; `Env.UsageReader.TrimUsage(user, bucket, Start, End)`.
- Handler: `uid`, `bucket`, `tenant`, `start`/`end` through `Args.Epoch` (bad → `ErrInvalidArgument`; radosgw ignores `get_epoch`'s return and proceeds with the default — mirror THAT: an unparsable date is the default, [rgw_rest_usage.cc:52-53](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_usage.cc#L52-L53) discard the return), `show-entries`/`show-summary` default true, `categories` comma list, `remove-all`. Response: `dumpUsage` through `WriteBody`, as `RGWUsage::show` starts the flusher before its first read ([rgw_usage.cc:48](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_usage.cc#L48)): the `usage` section holds `entries` when show-entries and `summary` when show-summary, so JSON gives `{}` and XML `<usage></usage>` when neither is shown; a bucket's `time` is `utime_t(epoch, 0)`'s `gmtime` and its `s3select` section is empty when the category filter leaves out `s3select`. Trim → `WriteEmpty(w, r, 200)`.

- [ ] **Step 1: Write the failing fakerados emulation specs**

In the fakerados suite, against a usage object written through M's `user_usage_log_add` emulation: read by user returns the records keyed by payer-or-owner with `truncated` after 1000; read by time (empty user) walks the by-time keys; trim of owner-keyed records returns 0 while records remain and ENODATA once none do (a three-record fixture: exactly two rounds); trim of a payer-keyed record returns 0 on every round and leaves it (`Expect(entriesLeft()).To(Equal(1))` after 5 rounds); trim with `bucket=b` behind 1000 records of other buckets returns 0 without removing anything; `usage_log_clear` empties.

- [ ] **Step 2: Implement the emulation**

`internal/testutil/fakerados/cls_rgw.go`: the omap key builders (`%011d`), `usage_iterate_range`'s walk over the sorted omap with the 1000 window, the read callback's aggregation, the trim callback's owner-built keys.

- [ ] **Step 3: Write the failing driver specs**

`internal/driver/usageadmin_test.go` on fakerados with `usageMaxShards 4`, `usageMaxUserShards 2` (M's options in the fixture):

```go
It("reads a user's usage across its shards and pages", func(ctx SpecContext) {
	logUsage(ctx, "alice", "plain", hour(1), "put_obj", 3)
	logUsage(ctx, "alice", "plain", hour(2), "get_obj", 1)
	logUsage(ctx, "alice", "other", hour(1), "put_obj", 1)
	logUsage(ctx, "bob", "b", hour(1), "put_obj", 1)
	var it op.UsageIter
	recs, truncated, err := store.ReadUsage(ctx, "alice", "", 0, math.MaxUint64, 1000, &it)
	Expect(err).NotTo(HaveOccurred())
	Expect(truncated).To(BeFalse())
	Expect(recs).To(HaveLen(2), "aggregated per (user, bucket)")
	Expect(recs[0].Bucket).To(Equal("other"))
	Expect(recs[1].Categories).To(HaveKeyWithValue("put_obj", op.UsageData{Ops: 3, SuccessfulOps: 3}))
	Expect(recs[1].Total.Ops).To(Equal(uint64(4)))
	recs, _, _ = store.ReadUsage(ctx, "", "", 0, math.MaxUint64, 1000, &op.UsageIter{})
	Expect(recs).To(HaveLen(3), "every shard")
	recs, _, _ = store.ReadUsage(ctx, "alice", "plain", hour(2), hour(3), 1000, &op.UsageIter{})
	Expect(recs).To(HaveLen(1))
	Expect(recs[0].Categories).To(HaveKey("get_obj"))
	for i := range 2500 { logUsage(ctx, "carol", fmt.Sprintf("b%04d", i), hour(1), "put_obj", 1) }
	var all []op.UsageRecord
	it = op.UsageIter{}
	for {
		page, more, err := store.ReadUsage(ctx, "carol", "", 0, math.MaxUint64, 1000, &it)
		Expect(err).NotTo(HaveOccurred())
		all = append(all, page...)
		if !more { break }
	}
	Expect(all).To(HaveLen(2500))
})
It("trims within the bound and stops on ENODATA", func(ctx SpecContext) {
	for i := range 2500 { logUsage(ctx, "carol", fmt.Sprintf("b%04d", i), hour(1), "put_obj", 1) }
	Expect(store.TrimUsage(ctx, "carol", "", 0, math.MaxUint64)).To(Succeed())
	recs, _, _ := store.ReadUsage(ctx, "carol", "", 0, math.MaxUint64, 1000, &op.UsageIter{})
	Expect(recs).To(BeEmpty())
	Expect(cluster.ExecCount("user_usage_log_trim")).To(BeNumerically("<=", 2*usageTrimRoundsFor(2500)), "bounded")
})
It("gives up on payer-keyed records after the bound with one warning (tracker #72593)", func(ctx SpecContext) {
	logUsagePayer(ctx, "alice", "bob", "plain", hour(1), "get_obj", 1)
	logs := captureLogs()
	Expect(store.TrimUsage(ctx, "bob", "", 0, math.MaxUint64)).To(Succeed())
	Expect(cluster.ExecCount("user_usage_log_trim")).To(Equal(driver.UsageTrimRoundsForTest(1)))
	Expect(logs.String()).To(ContainSubstring("72593"))
	recs, _, _ := store.ReadUsage(ctx, "bob", "", 0, math.MaxUint64, 1000, &op.UsageIter{})
	Expect(recs).To(HaveLen(1), "still there, as on Squid and Tentacle")
})
It("gives up on a bucket filter stuck behind other records (tracker #58136)", func(ctx SpecContext) {
	for i := range 1000 { logUsage(ctx, "dan", fmt.Sprintf("a%04d", i), hour(1), "put_obj", 1) }
	logUsage(ctx, "dan", "zzz", hour(1), "put_obj", 1)
	Expect(store.TrimUsage(ctx, "dan", "zzz", 0, math.MaxUint64)).To(Succeed())
	recs, _, _ := store.ReadUsage(ctx, "dan", "zzz", 0, math.MaxUint64, 1000, &op.UsageIter{})
	Expect(recs).To(HaveLen(1), "not trimmed by the class on Squid and Tentacle")
})
```

- [ ] **Step 4: Implement `internal/driver/usageadmin.go`; drop the stubs**

PASS.

- [ ] **Step 5: Write the failing op and handler specs**

`internal/op/adminusage_test.go` on memstore: `ShowUsage{}` over seeded records (two users, three buckets, two hours, categories `put_obj`/`get_obj`) → `Entries` grouped by user in order, each bucket's `Epoch` the first hour, `Summary` per user with `Total.Ops` = the sum; `Categories{"get_obj"}` filters both the entries' category lists and the summary total; `ShowEntries=false` → `Entries` nil; `Bucket: "plain"` → only that bucket's records under the owner's user; a missing bucket → `ErrNoSuchKey`; `TrimUsage{}` with nothing and `RemoveAll=false` → `ErrInvalidArgument`; with `RemoveAll` → every record gone; with `UID` → only that user's.

`internal/admin/usage_test.go`: `GET /admin/usage?show-summary=true` → 200 `{"entries":[{"user":"alice","buckets":[{"bucket":"plain","time":"2026-…T01:00:00.000000Z","epoch":…,"owner":"alice","categories":[{"category":"put_obj","bytes_sent":0,"bytes_received":10,"ops":1,"successful_ops":1}],"s3select":{"bytes_processed":0,"bytes_returned":0}}]}],"summary":[{"user":"alice","categories":[…],"total":{"bytes_sent":0,"bytes_received":10,"ops":1,"successful_ops":1,"bytes_processed":0,"bytes_returned":0}}]}` (go-ceph's `Usage` struct decodes it); `GET /admin/usage?uid=alice&start=2026-09-25+16:00:00&end=2026-09-26` filters; `GET /admin/usage?show-entries=false&show-summary=false` → `{}`; `DELETE /admin/usage` → 400; `DELETE /admin/usage?remove-all=true` → 200 and a following GET has no entries; `DELETE /admin/usage?uid=alice` → 200; an anonymous or `usage=read`-only caller on DELETE → 403. In XML: `GET /admin/usage?show-summary=true&format=xml` → a body starting `<?xml version="1.0" encoding="UTF-8"?><usage><entries><user><user>alice</user><buckets><bucket><bucket>plain</bucket><time>`, containing `<owner>alice</owner><categories><entry><category>put_obj</category><bytes_sent>0</bytes_sent><bytes_received>10</bytes_received><ops>1</ops><successful_ops>1</successful_ops></entry></categories><s3select><bytes_processed>0</bytes_processed><bytes_returned>0</bytes_returned></s3select></bucket></buckets></user></entries><summary><user><user>alice</user>` and ending `<total><bytes_sent>0</bytes_sent><bytes_received>10</bytes_received><ops>1</ops><successful_ops>1</successful_ops><bytes_processed>0</bytes_processed><bytes_returned>0</bytes_returned></total></user></summary></usage>`; `?show-entries=false&show-summary=false&format=xml` → `<?xml version="1.0" encoding="UTF-8"?><usage></usage>`; `?categories=get_obj&format=xml` → alice's bucket with `<categories></categories><s3select></s3select>`.

- [ ] **Step 6: Implement `internal/op/adminusage.go` and `internal/admin/usage.go`; record the bound; `make check`; commit**

`docs/exclusions.md`, "Coexistence obligations independent of any exclusion", a new bullet at the end of the admin bullets that begin with the admin headers bullet:

"- **The admin usage trim is bounded.** `DELETE /admin/usage` repeats `user_usage_log_trim` on each usage shard object at most ceil(n/1000)+1 times, n being the records on that shard its filter matches, then answers 200, logging a warning when the class has still not answered ENODATA. radosgw repeats the call until ENODATA with no bound (`cls_rgw_usage_log_trim`, [cls_rgw_client.cc:830-853](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw_client.cc#L830-L853); v20.2.4 [:639-659](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw_client.cc#L639-L659)), and on Squid and Tentacle that answer may never come: a record whose payer is not its owner is never removed, because the class stores it under the payer and deletes under the owner (tracker #72593, fixed in v21 only), and a trim filtered by bucket makes no progress behind 1000 records of other buckets (#58136). Where radosgw's request would spin without answering, rgw-go's answers 200 with those records left in place."

Report the change for the rgw-rs session.

```bash
git add internal/driver/usageadmin.go internal/driver/usageadmin_test.go internal/driver/store.go internal/testutil/fakerados/cls_rgw.go internal/op/adminusage.go internal/op/adminusage_test.go internal/admin/usage.go internal/admin/usage_test.go internal/memstore/admin.go docs/exclusions.md
git commit -m "feat(admin): usage show and a bounded usage trim"
```

The commit body names N-D4 and both trackers.

## Task 11: Bucket index check and rebuild

**Files:**
- Create: `internal/op/adminbucket_index.go`, `internal/op/adminbucket_index_test.go`, `internal/admin/bucket_index.go`, `internal/admin/bucket_index_test.go`
- Modify: `internal/driver/bucketadmin.go` (`CheckIndex`, `RebuildIndex`, `RemoveIndexEntries`), `internal/driver/bucketadmin_test.go`, `internal/testutil/fakerados/cls_rgw.go` (`bucket_check_index`, `bucket_rebuild_index` emulation), `internal/memstore/admin.go`, `internal/admin/handler.go` (`newBucketIndexHandlers()`), `docs/exclusions.md`

**Interfaces:**
- Consumes: `cls/rgw.BucketCheckIndex(op *ReadOp, r) *CheckIndexResult` → `CheckIndexRet{ExistingHeader, CalculatedHeader DirHeader}`, `cls/rgw.BucketRebuildIndex(op Execer)`; M's `indexPool`, `shardOIDs`, `options.bucketIndexMaxAIO`; `meta.IndexShard(key, numShards)`, `meta.BucketInfo.IndexShardOID`, `meta.ObjKey.IndexKeyName`; `radosclient.WriteOp.OmapRmKeys`; G's `op.BucketStore.ListObjects` (`NS: "multipart"`, `ListVersions`); Task 6's `loadAdminBucket`, `dumpBucketUsage`; Task 3's `WriteBody`, `formatter.Formatter`; Task 1's `op.BucketAdminStore`.
- Produces:

```go
package driver

// CheckIndex is RGWRados::bucket_check_index (rgw_rados.cc:5481-5514): one
// bucket_check_index per shard, rgw_bucket_index_max_aio in flight, the two
// headers accumulated per category as IndexStats does.
func (s *Store) CheckIndex(ctx context.Context, rec *op.BucketRecord) (existing, calculated map[string]op.CategoryStats, err error)
// RebuildIndex is bucket_rebuild_index (:5516-5527): bucket_rebuild_index on every shard.
func (s *Store) RebuildIndex(ctx context.Context, rec *op.BucketRecord) error
// RemoveIndexEntries is remove_objs_from_index (:10247-10321): an indexless
// layout is ErrInvalidArgument; each key's shard is bucket_shard_index of its
// sharding key — for the multipart namespace RGWMPObj::from_meta(name).get_key(),
// which is the OBJECT name: the entry name with its last two "."-components
// (upload id and part number or "meta") removed (svc_tier_rados.h:73-87,
// svc_bi_rados.h:109-122); the name itself otherwise — and the omap key removed
// is the entry's index key name; one omap_rm_keys per shard.
func (s *Store) RemoveIndexEntries(ctx context.Context, rec *op.BucketRecord, keys []meta.ObjKey) error
```

```go
package op

// CheckBucketIndex is RGWOp_Check_Bucket_Index + RGWBucketAdminOp::check_index
// (rgw_rest_bucket.cc:94-126; driver/rados/rgw_bucket.cc:1241-1280). Cap buckets=write.
type CheckBucketIndex struct {
	AdminOp
	Bucket       string
	Fix          bool
	CheckObjects bool

	InvalidMultipart []string // check_bad_index_multipart's entries, raw index key names
	Objects          []string // check_object_index's listing, only with CheckObjects
	Existing         map[string]CategoryStats
	Calculated       map[string]CategoryStats
}
```

```go
package admin

func newBucketIndexHandlers() map[string]HandlerFunc // check_bucket_index

// dumpBucketCheck is RGWBucketAdminOp::check_index's document
// (driver/rados/rgw_bucket.cc:1258-1276; v20.2.4 :1411-1438), written after
// the flusher starts: every entry of both checks is a string named "object"
// (dump_mulipart_index_results :88-94, dump_bucket_index :291-296), so the
// objects section repeats that key, then dump_index_check (:312-324).
func dumpBucketCheck(f formatter.Formatter, o *op.CheckBucketIndex) {
	f.OpenObjectSection("bucket_check")
	f.OpenArraySection("invalid_multipart_entries")
	for _, name := range o.InvalidMultipart {
		f.DumpString("object", name)
	}
	f.CloseSection()
	if o.CheckObjects {
		f.OpenObjectSection("objects")
		for _, name := range o.Objects {
			f.DumpString("object", name)
		}
		f.CloseSection()
	}
	f.OpenObjectSection("check_result")
	f.OpenObjectSection("existing_header")
	dumpBucketUsage(f, o.Existing)
	f.CloseSection()
	f.OpenObjectSection("calculated_header")
	dumpBucketUsage(f, o.Calculated)
	f.CloseSection()
	f.CloseSection()
	f.CloseSection()
}
```

Semantics (rgw_bucket.cc at v19.2.6):

- **check_bad_index_multipart** ([:326-420](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_bucket.cc#L326-L420)): list the `multipart` namespace with `ListVersions` in pages of 1000; for each entry take `oid = key.OID()`... radosgw uses `rgw_obj(bucket, key).get_oid()` and splits at the LAST `.`: a suffix `meta` marks an upload's meta object (`meta_objs[name] = true`), any other entry is a part keyed by the same prefix (`all_objs[key] = name`); after the listing, every part whose prefix has no meta object is invalid → `InvalidMultipart`, each the entry's raw index key name with its namespace prefix (`key.IndexKeyName()`, e.g. `_multipart_orph.2~zzz.1`), because the listing returns entries under their index keys ([rgw_rados.cc:2101](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L2101)) and `dump_mulipart_index_results` writes `o.name` ([:88-94](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_bucket.cc#L88-L94)); with `Fix`, `Env.BucketAdmin.RemoveIndexEntries(rec, keys)` in batches of 1000 (radosgw flushes and removes per 1000, [:387-401](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_bucket.cc#L387-L401), then the remainder). Squid reports them in key order (`all_objs` is a map, [:340](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_bucket.cc#L340), [:381](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_bucket.cc#L381)). Tentacle rewrote the check to walk each shard in turn with `bi_list` (v20.2.4 [:343-482](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_bucket.cc#L343-L482), [:497-557](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_bucket.cc#L497-L557); the REST op leaves `max_aio` 0, so one coroutine takes the shards in order), which finds the same entries but reports them shard by shard; so when `rel >= denc.Tentacle` the op orders them by (`meta.IndexShard(object name, numShards)`, key), the object name being the entry name with its last two `.`-components removed as `RemoveIndexEntries` computes it, since every entry of an upload lives on its object's shard.
- **check_object_index** ([:422-468](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_bucket.cc#L422-L468)): `CheckObjects` without `Fix` → `ErrInvalidArgument` ("check-objects flag requires fix index enabled"); radosgw returns that after its flusher has started, so its client gets a 200 whose body stops after `invalid_multipart_entries` — the difference N-D2 records. With both: list the plain namespace without versions (radosgw's `ListParams` sets no `list_versions`, [:446-448](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_bucket.cc#L446-L448); v20.2.4 [:583-585](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_bucket.cc#L583-L585)), pages of 1000, and record each entry's `key.name` under `Objects`. radosgw lists with `rgw_bucket_object_check_filter` ([:141-146](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_bucket.cc#L141-L146)), which forces `check_disk_state` on every plain entry ([rgw_rados.cc:9820-9864](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L9820-L9864); v20.2.4 :10740ff): an entry whose object is gone is dropped from the listing and removed from the index, and the rest are refreshed from their heads. No phase-1 listing interface carries a "force check" flag, so rgw-go's `check-objects` lists as every listing does (M's listing repairs only entries with pending state) — recorded in docs/exclusions.md (Step 4) and the PR description.
- **check_index** ([:848-871](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_bucket.cc#L848-L871)): `Env.BucketAdmin.CheckIndex(rec)` → `Existing`/`Calculated`; with `Fix`, `RebuildIndex(rec)`.
- Order in `RGWBucketAdminOp::check_index` ([:1241-1280](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_bucket.cc#L1241-L1280)): multipart check, object check, index check; all three inside one `bucket_check` object.
- Response: `dumpBucketCheck` through `WriteBody`, as `check_index` starts the flusher before its checks (:1256; v20.2.4 :1409). The top-level name drops in JSON: `{"invalid_multipart_entries":[…],"objects":{"object":"a","object":"b"},"check_result":{"existing_header":{"usage":{…}},"calculated_header":{"usage":{…}}}}`, `objects` only with `check-objects` and a JSON object that repeats the key `object` once per entry; in XML `<bucket_check><invalid_multipart_entries><object>…</object></invalid_multipart_entries><objects><object>a</object><object>b</object></objects><check_result><existing_header><usage><rgw.main>…</rgw.main></usage></existing_header>…</check_result></bucket_check>`. go-ceph's `CheckBucketIndexResponse` reads `invalid_multipart_entries` and both `check_result` headers' `usage`.
- **Driver**: `CheckIndex` issues `BucketCheckIndex` on every shard oid (`shardOIDs`), `bucketIndexMaxAIO` in flight, accumulating `ExistingHeader.Stats` and `CalculatedHeader.Stats` as `accumulate_raw_stats` ([rgw_rados.cc:5464-5479](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5464-L5479)); `RebuildIndex` issues `BucketRebuildIndex` likewise; `RemoveIndexEntries` groups keys by shard (`meta.IndexShard(shardingKey, numShards)`, `0` for an unsharded index) and runs one `OmapRmKeys` `WriteOp` per shard on `rec.Info.IndexShardOID(current, shard)`.
- **fakerados**: `bucket_check_index` returns the shard's stored header as `existing_header` and a header recomputed from the shard's entries (per category: count, `total_size`, `total_size_rounded`, `actual_size`) as `calculated_header` (cls_rgw.cc `rgw_bucket_check_index`); `bucket_rebuild_index` writes the recomputed header back.

- [ ] **Step 1: Write the failing driver specs**

`internal/driver/bucketadmin_test.go` additions on fakerados: a bucket with three entries whose stored header says 2 objects — `CheckIndex` returns `existing["rgw.main"].NumObjects == 2` and `calculated["rgw.main"].NumObjects == 3`; `RebuildIndex` then `IndexStats` reports 3; `RemoveIndexEntries` with a multipart part key `meta.ObjKey{Name: "obj.2~abc.1", NS: "multipart"}` removes the omap entry from the shard `meta.IndexShard("obj", n)` names — the shard the object `obj` itself lives on (assert the entry is gone and the other shards untouched); on an indexless layout → `ErrInvalidArgument`.

- [ ] **Step 2: Implement the driver methods and the emulation; PASS**

- [ ] **Step 3: Write the failing op and handler specs**

Op (memstore): an upload's meta entry `obj.2~abc.meta` plus parts `obj.2~abc.1`, `obj.2~abc.2` and an orphan part `orph.2~zzz.1` in the multipart namespace → `InvalidMultipart == ["_multipart_orph.2~zzz.1"]`, untouched without `Fix`, removed with `Fix` (a second run finds none); on an 11-shard bucket with orphans of two objects whose shards run against their names' order, Squid reports them in key order and Tentacle shard by shard; `CheckObjects` without `Fix` → `ErrInvalidArgument`; with both → `Objects` lists the plain entries' names, one per object however many versions it has; `Existing`/`Calculated` filled; missing bucket → `ErrNoSuchKey`.

Handler: `GET /admin/bucket?index&bucket=test&check-objects=true&fix=true` → 200 whose raw JSON is `{"invalid_multipart_entries":[],"objects":{"object":"a","object":"b"},"check_result":{"existing_header":{"usage":{"rgw.main":{…}}},"calculated_header":{"usage":{"rgw.main":{…}}}}}` for a bucket holding `a` and `b` (go-ceph's struct decodes it); the same with `format=xml` → a body starting `<?xml version="1.0" encoding="UTF-8"?><bucket_check><invalid_multipart_entries></invalid_multipart_entries><objects><object>a</object><object>b</object></objects><check_result><existing_header><usage><rgw.main><size>`; `GET /admin/bucket?index&bucket=test&check-objects=true` → 400; `GET /admin/bucket?index&bucket=foo` → 404 `NoSuchKey`; a `buckets=read`-only caller → 403.

- [ ] **Step 4: Implement `internal/op/adminbucket_index.go` and `internal/admin/bucket_index.go`; `make check`; commit**

`docs/exclusions.md`, "Coexistence obligations independent of any exclusion", a new bullet at the end of the admin bullets that begin with the admin headers bullet:

"- **The admin index check does not re-read every object's head.** `GET /admin/bucket?index&check-objects=true&fix=true` lists the bucket's objects and reports their names, as radosgw does, but radosgw's listing also reads the head of every object it lists and repairs the index from it: an entry whose object is gone is dropped from the report and removed from the index, and the others are refreshed (`rgw_bucket_object_check_filter`, [rgw_bucket.cc:141-146](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_bucket.cc#L141-L146), forcing `check_disk_state` in the listing, [rgw_rados.cc:9820-9864](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L9820-L9864)). rgw-go's listing repairs only entries with a pending operation, as every listing does, so it reports and keeps such an entry. `radosgw-admin bucket check --check-objects --fix` repairs it."

Report the change for the rgw-rs session.

```bash
git add internal/driver/bucketadmin.go internal/driver/bucketadmin_test.go internal/testutil/fakerados/cls_rgw.go internal/memstore/admin.go internal/op/adminbucket_index.go internal/op/adminbucket_index_test.go internal/admin/bucket_index.go internal/admin/bucket_index_test.go internal/admin/handler.go docs/exclusions.md
git commit -m "feat(admin): bucket index check and rebuild"
```

## Task 12: Realm and period getters; ratelimit

**Files:**
- Create: `internal/driver/realm.go`, `internal/driver/realm_test.go`, `internal/admin/realm.go`, `internal/admin/realm_test.go`, `internal/admin/ratelimit.go`, `internal/admin/ratelimit_test.go`, `internal/meta/dump_period.go`
- Modify: `internal/op/admininfo.go` (`GetRealm`, `ListRealms`, `GetPeriod`, `GetRateLimit`, `SetRateLimit`), `internal/op/admininfo_test.go`, `internal/meta/attrs.go` (`AttrRateLimit`), `internal/meta/sysobj.go` (`RealmNamesPrefix` exported), `internal/meta/dump_test.go`, `internal/meta/dump_goldens_test.go` (`RGWRealm`, `RGWPeriod`, `RGWZoneGroup`, `RGWZone`, `RGWZoneGroupPlacementTarget`), `internal/driver/store.go` (stubs dropped), `internal/memstore/admin.go` (`RealmStore` behaviour from Task 1 completed), `internal/admin/handler.go` (`newRealmHandlers()`, `newRateLimitHandlers()`), `docs/exclusions.md`

**Interfaces:**
- Consumes: M's `rootPools` (driver/zone.go: the realm and period root pools), `sysobjs.read/write`, `meta.RealmOID`, `RealmNameOID`, `DefaultRealmOID`, `PeriodOID`, `PeriodLatestEpochOID`, `PeriodConfigOID`, `meta.DecodeRealm`, `DecodeNameToID`, `DecodeDefaultSystemMetaObjInfo`, `DecodePeriod`, `DecodePeriodLatestEpochInfo`, `DecodePeriodConfig`, `meta.PeriodConfig.Encode`, `meta.RateLimitInfo`/`DecodeRateLimitInfo`/`Encode`; Task 2's `Pool.ListObjects` (prefix filter); G's `op.UserStore.PutUser`, `op.BucketStore.PutBucketAttrs`, `op.ZoneInfo.Realm()`; Task 1's `op.RealmStore`; Task 3's `WriteBody`, `WriteTyped`, `WriteEmpty`, `formatter.Formatter`, `meta.Dumper`, `dumpSection`, `dumpStrings`, `dumpMap`, `Quota.Dump`, `dumpGoldens`; phase 0's `meta.{ZoneGroup,Zone,ZoneGroupPlacementTarget,ZoneGroupPlacementTier,ZoneGroupPlacementTierS3,TierACLMapping,ZoneGroupTierS3Glacier,SyncPolicy}`, `TierTypeCloudS3`, `isS3`, `isGlacier`, `aclTypeEmailUser`, `aclTypeGroup`.
- Produces:

```go
package meta

const AttrRateLimit = AttrPrefix + "ratelimit" // RGW_ATTR_RATELIMIT (rgw_common.h:81)
const RealmNamesPrefix = "realms_names."       // realm_names_oid_prefix

// The period family's dumps, each its C++ namesake's; the code is in Step 4.
func (r Realm) Dump(f formatter.Formatter, rel denc.Release)
func (p Period) Dump(f formatter.Formatter, rel denc.Release)
func (m PeriodMap) Dump(f formatter.Formatter, rel denc.Release)
func (c PeriodConfig) Dump(f formatter.Formatter, rel denc.Release)
func (l RateLimitInfo) Dump(f formatter.Formatter, rel denc.Release)
func (g ZoneGroup) Dump(f formatter.Formatter, rel denc.Release)
func (z Zone) Dump(f formatter.Formatter, rel denc.Release)
func (t ZoneGroupPlacementTarget) Dump(f formatter.Formatter, rel denc.Release)
func (t ZoneGroupPlacementTier) Dump(f formatter.Formatter, rel denc.Release)
func (s ZoneGroupPlacementTierS3) Dump(f formatter.Formatter, rel denc.Release)
func (m TierACLMapping) Dump(f formatter.Formatter, rel denc.Release)
func (g ZoneGroupTierS3Glacier) Dump(f formatter.Formatter, rel denc.Release)
```

```go
package driver

// The op.RealmStore methods over the root pools.
func (s *Store) GetRealm(ctx context.Context, id, name string) (meta.Realm, error)
func (s *Store) ListRealms(ctx context.Context) (string, []string, error)
func (s *Store) GetPeriod(ctx context.Context, realmID, periodID string, epoch uint32) (meta.Period, error)
func (s *Store) GetPeriodConfig(ctx context.Context, realmID string) (meta.PeriodConfig, error)
func (s *Store) PutPeriodConfig(ctx context.Context, realmID string, cfg meta.PeriodConfig) error
```

```go
package op

// GetRealm is RGWOp_Realm_Get (driver/rados/rgw_rest_realm.cc:262-304). Cap zone=read.
type GetRealm struct {
	AdminOp
	ID, Name string
	Result   meta.Realm
}
// ListRealms is RGWOp_Realm_List (:307-350). Cap zone=read.
type ListRealms struct {
	AdminOp
	DefaultID string
	Names     []string
}
// GetPeriod is RGWOp_Period_Get (:54-80). Cap zone=read.
type GetPeriod struct {
	AdminOp
	RealmID, PeriodID string
	Epoch             uint32
	Result            meta.Period
}
// GetRateLimit is RGWOp_Ratelimit_Info (rgw_rest_ratelimit.cc:4-120). Cap ratelimit=read.
type GetRateLimit struct {
	AdminOp
	UID, Scope, Bucket, Tenant string
	Global                     bool
	Result                     meta.RateLimitInfo // user or bucket scope
	Period                     meta.PeriodConfig  // global
}
// SetRateLimit is RGWOp_Ratelimit_Set (:122-343). Cap ratelimit=write.
type SetRateLimit struct {
	AdminOp
	UID, Scope, Bucket, Tenant                          string
	Global                                              bool
	MaxReadOps, MaxWriteOps, MaxReadBytes, MaxWriteBytes *int64
	Enabled                                             *bool
}
```

```go
package admin

func newRealmHandlers() map[string]HandlerFunc     // get_realm, list_realms, get_period
func newRateLimitHandlers() map[string]HandlerFunc // get_ratelimit_info, put_ratelimit_info
```

Semantics:

- **GetRealm** (RGWSystemMetaObj::init, [rgw_zone.cc:106-145](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_zone.cc#L106-L145)): `id == ""` → `name == ""` → the default: read `DefaultRealmOID()` (`DecodeDefaultSystemMetaObjInfo.DefaultID`), else `RealmNameOID(name)` (`DecodeNameToID.ObjID`); then `RealmOID(id)` → `DecodeRealm`. A missing object at any step → `ErrNoSuchKey` (radosgw's ENOENT); a decode failure → `ErrInternalError`. **ListRealms** ([svc_zone.cc:421-427](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_zone.cc#L421-L427)): the realm root pool's objects with the `realms_names.` prefix, names = the suffix, sorted; the default id read as above with ENOENT → `""`. **GetPeriod** (rgw_period.cc:14-46): `periodID == ""` → the realm (`GetRealm(realmID, "")`) gives `CurrentPeriod` and its id; `epoch == 0` → `PeriodLatestEpochOID(id)` → `DecodePeriodLatestEpochInfo.Epoch`; then `PeriodOID(id, epoch)` → `DecodePeriod`; ENOENT → `ErrNoSuchKey`. **GetPeriodConfig**/**PutPeriodConfig** (rgw_zone.cc:616-651): `PeriodConfigOID(realmID)` in the period root pool, non-exclusive, unversioned write; a missing config is `ErrNotFound`.
- **GetRealm/ListRealms/GetPeriod ops**: pass-throughs to `Env.Realms`; `GetPeriod`'s `realm_id` argument defaults to `""` (the default realm, exactly as radosgw's `RGWRealm(realm_id)` with an empty id resolves).
- **GetRateLimit** ([rgw_rest_ratelimit.cc:13-120](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_ratelimit.cc#L13-L120)): `global` must be `true`/`false` (case-insensitive) when present, else `ErrInvalidArgument` — radosgw checks the raw string before `get_bool`; scope `bucket` with a bucket and not global → `GetBucket(Tenant, Bucket)` (missing → `ErrNoSuchKey`), `rec.Attrs[AttrRateLimit]` decoded (absent → zero value; a decode failure → `ErrInternalError`, radosgw's `-EIO`); scope `user` with a uid and not global → `GetUser` (missing → `ErrNoSuchKey`, radosgw's `-ENOENT`), the user's attr likewise; `global` → `GetPeriodConfig(Env.Zone.Realm().ID)` (missing → zero config); anything else → `ErrInvalidArgument`. Responses, each flusher-started and so written with `WriteBody` ([:59-63](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_ratelimit.cc#L59-L63), [:95-99](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_ratelimit.cc#L95-L99), [:109-115](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_ratelimit.cc#L109-L115)): bucket → a `bucket_ratelimit` section holding `encode_json("bucket_ratelimit", info)`, so JSON shows `{"bucket_ratelimit":{…}}` and XML `<bucket_ratelimit><bucket_ratelimit>…</bucket_ratelimit></bucket_ratelimit>`; user → the same under `user_ratelimit`; global → a `period_config` section holding `bucket_ratelimit`, `user_ratelimit` and `anonymous_ratelimit` in that order. A user-scope GET in radosgw falls through to `op_ret = -EINVAL` after flushing ([:100-118](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_ratelimit.cc#L100-L118)) — the body and the 200 are already sent, so the observable answer is 200 with the body; rgw-go answers 200 with the body.
- **SetRateLimit** ([:178-343](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_ratelimit.cc#L178-L343)): `max-read-ops`, `max-write-ops`, `max-read-bytes`, `max-write-bytes` as `Int64` (a parse failure → `ErrInvalidArgument`); `enabled` and `global` must be `true`/`false` when present else `ErrInvalidArgument`; no limit or enabled argument at all → `ErrInvalidArgument` ("No rate limit configuration arguments have been sent", [:169-172](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_ratelimit.cc#L169-L172)); a negative limit is ignored ([:141-164](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_ratelimit.cc#L141-L164)). Scope `user` (uid, not global): load the user, decode the attr, apply the given fields, encode at `Env.Zone.Release()`, `PutUser(rec, {IfVersion})` with `rec.Attrs[AttrRateLimit]` set (`merge_and_store_attrs`); scope `bucket`: the same over `PutBucketAttrs(rec, {AttrRateLimit: bl}, nil)`; `global`: `GetPeriodConfig` (missing → zero), apply to `BucketRateLimit`/`AnonRateLimit`/`UserRateLimit` by scope `bucket`/`anon`/`user`, `PutPeriodConfig`; anything else → `ErrInvalidArgument`. `WriteEmpty(w, r, 200)`. Note M's `RateLimitInfo.Encode` writes version 1 (what Squid and Tentacle write).
- Realm handlers, each through `WriteTyped(w, r, q, "application/json", …)`: radosgw dumps these bodies without starting its flusher, so with no XML declaration, and names `application/json` and the length itself whatever the format ([rgw_rest_realm.cc:48-49](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rest_realm.cc#L48-L49), [:301-302](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rest_realm.cc#L301-L302), [:344-348](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rest_realm.cc#L344-L348); v20.2.4 [:49-50](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rest_realm.cc#L49-L50), [:322-323](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rest_realm.cc#L322-L323), [:365-369](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rest_realm.cc#L365-L369)), so the length goes out through `dump_content_length` with `Accept-Ranges: bytes`, which `WriteTyped` reproduces through G's `s3.SetContentLength`. `GET /admin/realm?id=&name=` → a `realm` section around `Realm.Dump` (`{"id","name","current_period","epoch"}`, RGWRealm::dump [rgw_realm.cc:251-256](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_realm.cc#L251-L256) over RGWSystemMetaObj::dump); `GET /admin/realm?list` → a `realms_list` section holding `default_info` and the `realms` names as `obj` entries ([:344-347](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rest_realm.cc#L344-L347)), so JSON `{"default_info":id,"realms":[names]}`; `GET /admin/realm/period?realm_id=&period_id=&epoch=` → a `period` section around `Period.Dump` ([:48](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_period.cc#L48)); `epoch` through `Uint32`. A period whose zonegroup holds sync policy groups fails the formatter and answers 501 (N-D11).

- [ ] **Step 1: Write the failing driver specs**

`internal/driver/realm_test.go` on fakerados with M's root-pool fixture (realm `r1` id `R`, current period `P` epoch 2, latest epoch 2, `default.realm` → `R`, a second realm name object `realms_names.other` → `O`):

```go
It("resolves the realm by id, name and default", func(ctx SpecContext) {
	for _, tc := range []struct{ id, name string }{{"R", ""}, {"", "r1"}, {"", ""}} {
		r, err := store.GetRealm(ctx, tc.id, tc.name)
		Expect(err).NotTo(HaveOccurred())
		Expect(r.ID).To(Equal("R"))
		Expect(r.CurrentPeriod).To(Equal("P"))
	}
	_, err := store.GetRealm(ctx, "", "nosuch")
	Expect(err).To(MatchError(op.ErrNoSuchKey))
	def, names, err := store.ListRealms(ctx)
	Expect(err).NotTo(HaveOccurred())
	Expect(def).To(Equal("R"))
	Expect(names).To(Equal([]string{"other", "r1"}))
})
It("reads the current period at its latest epoch and a named one", func(ctx SpecContext) {
	p, err := store.GetPeriod(ctx, "", "", 0)
	Expect(err).NotTo(HaveOccurred())
	Expect(p.ID).To(Equal("P"))
	Expect(p.Epoch).To(Equal(uint32(2)))
	p, err = store.GetPeriod(ctx, "R", "P", 1)
	Expect(err).NotTo(HaveOccurred())
	Expect(p.Epoch).To(Equal(uint32(1)))
	_, err = store.GetPeriod(ctx, "R", "P", 9)
	Expect(err).To(MatchError(op.ErrNoSuchKey))
})
It("round-trips the period config", func(ctx SpecContext) {
	_, err := store.GetPeriodConfig(ctx, "R")
	Expect(err).To(MatchError(op.ErrNotFound))
	cfg := meta.PeriodConfig{UserRateLimit: meta.RateLimitInfo{MaxReadOps: 5, Enabled: true}}
	Expect(store.PutPeriodConfig(ctx, "R", cfg)).To(Succeed())
	got, err := store.GetPeriodConfig(ctx, "R")
	Expect(err).NotTo(HaveOccurred())
	Expect(got.UserRateLimit).To(Equal(cfg.UserRateLimit))
	Expect(objectExists(ctx, rootPool, "period_config.R")).To(BeTrue())
})
```

- [ ] **Step 2: Implement `internal/driver/realm.go`; memstore parity; PASS**

- [ ] **Step 3: Write the failing `Dump` specs for the period family**

`internal/meta/dump_goldens_test.go` gains the five corpus types:

```go
	It("RGWRealm", func() { dumpGoldens("RGWRealm", meta.DecodeRealm) })
	It("RGWPeriod", func() { dumpGoldens("RGWPeriod", meta.DecodePeriod) })
	It("RGWZoneGroup", func() { dumpGoldens("RGWZoneGroup", meta.DecodeZoneGroup) })
	It("RGWZone", func() { dumpGoldens("RGWZone", meta.DecodeZone) })
	It("RGWZoneGroupPlacementTarget", func() { dumpGoldens("RGWZoneGroupPlacementTarget", meta.DecodeZoneGroupPlacementTarget) })
```

`internal/meta/dump_test.go` pins the release keys of the placement tier, the rate limits and the opaque sync policy:

```go
	It("writes a cloud tier as each release does", func() {
		t := meta.NewZoneGroupPlacementTier()
		t.TierType = meta.TierTypeCloudS3Glacier
		t.StorageClass = "GLACIER"
		Expect(render(t, denc.Squid)).To(Equal(`{"tier_type":"cloud-s3-glacier","storage_class":"GLACIER","retain_head_object":false}`))
		tentacle := render(t, denc.Tentacle)
		Expect(tentacle).To(HavePrefix(`{"tier_type":"cloud-s3-glacier","storage_class":"GLACIER","retain_head_object":false,"s3":{"endpoint":"","access_key":"","secret":"","region":"","host_style":"path",`))
		Expect(tentacle).To(HaveSuffix(`"multipart_min_part_size":33554432},"allow_read_through":false,"read_through_restore_days":1,"restore_storage_class":"STANDARD","s3-glacier":{"glacier_restore_days":1,"glacier_restore_tier_type":"Standard"}}`))
		t.TierType = meta.TierTypeCloudS3
		Expect(render(t, denc.Squid)).To(ContainSubstring(`"retain_head_object":false,"s3":{"endpoint":""`))
	})
	It("writes rate limits without main's list and delete limits", func() {
		l := meta.RateLimitInfo{MaxReadOps: 1, MaxWriteOps: 2, MaxListOps: 7, MaxReadBytes: 3, MaxWriteBytes: 4, Enabled: true}
		Expect(render(l, denc.Tentacle)).To(Equal(`{"max_read_ops":1,"max_write_ops":2,"max_read_bytes":3,"max_write_bytes":4,"enabled":true}`))
	})
	It("writes an empty zonegroup sync policy and fails on one with groups", func() {
		g := meta.ZoneGroup{ID: "g", Name: "zg"}
		Expect(render(g, denc.Squid)).To(ContainSubstring(`"realm_id":"","sync_policy":{"groups":[]},"enabled_features":[]}`))
		g.SyncPolicy = meta.SyncPolicy{Raw: []byte{1, 1, 8, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0}}
		f := formatter.NewJSON(false)
		f.OpenObjectSection("x")
		g.Dump(f, denc.Squid)
		f.CloseSection()
		Expect(f.Err()).To(MatchError(meta.ErrOpaqueJSON))
	})
```

Run the meta suite: FAIL.

- [ ] **Step 4: Implement `internal/meta/dump_period.go`**

```go
package meta

// Dump is RGWRealm::dump (rgw_realm.cc:251-256, both tags) over
// RGWSystemMetaObj::dump (rgw_zone.cc:812-816; v20.2.4 :822-826).
func (r Realm) Dump(f formatter.Formatter, _ denc.Release) {
	f.DumpString("id", r.ID)
	f.DumpString("name", r.Name)
	f.DumpString("current_period", r.CurrentPeriod)
	f.DumpUnsigned("epoch", uint64(r.Epoch))
}

// Dump is RGWPeriod::dump (rgw_period.cc:234-246, both tags).
func (p Period) Dump(f formatter.Formatter, rel denc.Release) {
	f.DumpString("id", p.ID)
	f.DumpUnsigned("epoch", uint64(p.Epoch))
	f.DumpString("predecessor_uuid", p.PredecessorUUID)
	dumpStrings(f, "sync_status", p.SyncStatus)
	dumpSection(f, "period_map", p.PeriodMap, rel)
	f.DumpString("master_zonegroup", p.MasterZoneGroup)
	f.DumpString("master_zone", p.MasterZone)
	dumpSection(f, "period_config", p.PeriodConfig, rel)
	f.DumpString("realm_id", p.RealmID)
	f.DumpUnsigned("realm_epoch", uint64(p.RealmEpoch))
}

// Dump is RGWPeriodMap::dump (rgw_zone.cc:1000-1005; v20.2.4 :1040-1045).
func (m PeriodMap) Dump(f formatter.Formatter, rel denc.Release) {
	f.DumpString("id", m.ID)
	dumpValues(f, "zonegroups", m.ZoneGroups, rel)
	dumpMap(f, "short_zone_ids", m.ShortZoneIDs,
		func(f formatter.Formatter, k string) { f.DumpString("key", k) },
		func(f formatter.Formatter, v uint32) { f.DumpUnsigned("val", uint64(v)) })
}

// dumpValues is encode_json_map(name, m): the values alone, each an "obj"
// section, in key order (ceph_json.h:649-656).
func dumpValues[V Dumper](f formatter.Formatter, name string, m map[string]V, rel denc.Release) {
	f.OpenArraySection(name)
	for _, k := range slices.Sorted(maps.Keys(m)) {
		dumpSection(f, "obj", m[k], rel)
	}
	f.CloseSection()
}

// Dump is RGWPeriodConfig::dump (rgw_zone.cc:662-669; v20.2.4 :670-677).
func (c PeriodConfig) Dump(f formatter.Formatter, rel denc.Release) {
	dumpSection(f, "bucket_quota", c.BucketQuota, rel)
	dumpSection(f, "user_quota", c.UserQuota, rel)
	dumpSection(f, "user_ratelimit", c.UserRateLimit, rel)
	dumpSection(f, "bucket_ratelimit", c.BucketRateLimit, rel)
	dumpSection(f, "anonymous_ratelimit", c.AnonRateLimit, rel)
}

// Dump is RGWRateLimitInfo::dump (rgw_common.cc:2766-2773; v20.2.4
// :2828-2835); neither release has main's list and delete limits.
func (l RateLimitInfo) Dump(f formatter.Formatter, _ denc.Release) {
	f.DumpInt("max_read_ops", l.MaxReadOps)
	f.DumpInt("max_write_ops", l.MaxWriteOps)
	f.DumpInt("max_read_bytes", l.MaxReadBytes)
	f.DumpInt("max_write_bytes", l.MaxWriteBytes)
	f.DumpBool("enabled", l.Enabled)
}

// Dump is RGWZoneGroup::dump (rgw_zone.cc:735-750; v20.2.4 :743-758). A sync
// policy with groups is carried opaque, so it fails the formatter as
// MarshalJSON fails; an empty one is rgw_sync_policy_info::dump's empty
// "groups" array (rgw_sync_policy.cc:777-783; v20.2.4 :784-790).
func (g ZoneGroup) Dump(f formatter.Formatter, rel denc.Release) {
	if !g.SyncPolicy.Empty() {
		f.Fail(fmt.Errorf("%w: zonegroup sync policy groups", ErrOpaqueJSON))
		return
	}
	f.DumpString("id", g.ID)
	f.DumpString("name", g.Name)
	f.DumpString("api_name", g.APIName)
	f.DumpBool("is_master", g.IsMaster)
	dumpStrings(f, "endpoints", g.Endpoints)
	dumpStrings(f, "hostnames", g.Hostnames)
	dumpStrings(f, "hostnames_s3website", g.HostnamesS3Website)
	f.DumpString("master_zone", g.MasterZone)
	dumpValues(f, "zones", g.Zones, rel)
	dumpValues(f, "placement_targets", g.PlacementTargets, rel)
	f.DumpString("default_placement", g.DefaultPlacement.String())
	f.DumpString("realm_id", g.RealmID)
	f.OpenObjectSection("sync_policy")
	f.OpenArraySection("groups")
	f.CloseSection()
	f.CloseSection()
	dumpStrings(f, "enabled_features", g.EnabledFeatures)
}

// Dump is RGWZone::dump (rgw_zone.cc:71-85, both tags).
func (z Zone) Dump(f formatter.Formatter, _ denc.Release) {
	f.DumpString("id", z.ID)
	f.DumpString("name", z.Name)
	dumpStrings(f, "endpoints", z.Endpoints)
	f.DumpBool("log_meta", z.LogMeta)
	f.DumpBool("log_data", z.LogData)
	f.DumpUnsigned("bucket_index_max_shards", uint64(z.BucketIndexMaxShards))
	f.DumpBool("read_only", z.ReadOnly)
	f.DumpString("tier_type", z.TierType)
	f.DumpBool("sync_from_all", z.SyncFromAll)
	dumpStrings(f, "sync_from", z.SyncFrom)
	f.DumpString("redirect_zone", z.RedirectZone)
	dumpStrings(f, "supported_features", z.SupportedFeatures)
}

// Dump is RGWZoneGroupPlacementTarget::dump (rgw_zone.cc:848-856; v20.2.4
// :858-866), tier_targets only when there are any.
func (t ZoneGroupPlacementTarget) Dump(f formatter.Formatter, rel denc.Release) {
	f.DumpString("name", t.Name)
	dumpStrings(f, "tags", t.Tags)
	dumpStrings(f, "storage_classes", t.StorageClasses)
	if len(t.TierTargets) > 0 {
		dumpMap(f, "tier_targets", t.TierTargets,
			func(f formatter.Formatter, k string) { f.DumpString("key", k) },
			func(f formatter.Formatter, v ZoneGroupPlacementTier) { dumpSection(f, "val", v, rel) })
	}
}

// Dump is RGWZoneGroupPlacementTier::dump. Squid writes the S3 config for
// cloud-s3 alone (rgw_zone.cc:895-904); Tentacle writes it for either cloud
// type, then the read-through fields and, for cloud-s3-glacier, the glacier
// config (v20.2.4 :929-944).
func (t ZoneGroupPlacementTier) Dump(f formatter.Formatter, rel denc.Release) {
	f.DumpString("tier_type", t.TierType)
	f.DumpString("storage_class", t.StorageClass)
	f.DumpBool("retain_head_object", t.RetainHeadObject)
	if rel < denc.Tentacle {
		if t.TierType == TierTypeCloudS3 {
			dumpSection(f, "s3", t.S3, rel)
		}
		return
	}
	if t.isS3() {
		dumpSection(f, "s3", t.S3, rel)
	}
	f.DumpBool("allow_read_through", t.AllowReadThrough)
	f.DumpUnsigned("read_through_restore_days", t.ReadThroughRestoreDays)
	f.DumpString("restore_storage_class", t.RestoreStorageClass)
	if t.isGlacier() {
		dumpSection(f, "s3-glacier", t.S3Glacier, rel)
	}
}

// Dump is RGWZoneGroupPlacementTierS3::dump (rgw_zone.cc:966-979; v20.2.4
// :1006-1019), which writes the tier's secret key.
func (s ZoneGroupPlacementTierS3) Dump(f formatter.Formatter, rel denc.Release) {
	f.DumpString("endpoint", s.Endpoint)
	f.DumpString("access_key", s.Key.ID)
	f.DumpString("secret", s.Key.Secret)
	f.DumpString("region", s.Region)
	style := "path"
	if s.HostStyle != 0 {
		style = "virtual"
	}
	f.DumpString("host_style", style)
	f.DumpString("target_storage_class", s.TargetStorageClass)
	f.DumpString("target_path", s.TargetPath)
	dumpMap(f, "acl_mappings", s.ACLMappings,
		func(f formatter.Formatter, k string) { f.DumpString("key", k) },
		func(f formatter.Formatter, v TierACLMapping) { dumpSection(f, "val", v, rel) })
	f.DumpUnsigned("multipart_sync_threshold", s.MultipartSyncThreshold)
	f.DumpUnsigned("multipart_min_part_size", s.MultipartMinPartSize)
}

// Dump is RGWTierACLMapping::dump (rgw_zone.cc:981-998; v20.2.4 :1021-1038).
func (m TierACLMapping) Dump(f formatter.Formatter, _ denc.Release) {
	typ := "id"
	switch m.Type {
	case aclTypeEmailUser:
		typ = "email"
	case aclTypeGroup:
		typ = "uri"
	}
	f.DumpString("type", typ)
	f.DumpString("source_id", m.SourceID)
	f.DumpString("dest_id", m.DestID)
}

// Dump is RGWZoneGroupTierS3Glacier::dump (v20.2.4 rgw_zone.cc:910-915).
func (g ZoneGroupTierS3Glacier) Dump(f formatter.Formatter, _ denc.Release) {
	f.DumpUnsigned("glacier_restore_days", g.RestoreDays)
	tier := "Standard"
	if g.RestoreTierType != 0 {
		tier = "Expedited"
	}
	f.DumpString("glacier_restore_tier_type", tier)
}
```

Run the meta suite: PASS.

- [ ] **Step 5: Write the failing op and handler specs**

Ops (memstore seeded with a realm and period through `memstore.Config`): the three getters pass through; caps `zone=read`; `GetRateLimit` scope/global validation table; set then get for user, bucket and each global scope; a negative limit ignored; no arguments → `ErrInvalidArgument`; `global=yes` → `ErrInvalidArgument` (Review Focus 3).

Handlers: `GET /admin/realm` → `{"id":…,"name":…,"current_period":…,"epoch":…}` with `Content-Type: application/json`, its `Content-Length` and `Accept-Ranges: bytes` (the getter names the length); the same with `format=xml` → `Content-Type: application/json` and the body `<realm><id>R</id><name>r1</name><current_period>P</current_period><epoch>…</epoch></realm>`, no declaration; `GET /admin/realm?list` → `{"default_info":"R","realms":["r1"]}`, in XML `<realms_list><default_info>R</default_info><realms><obj>r1</obj></realms></realms_list>`; `GET /admin/realm/period` → the period document with `period_map`, `period_config`, `realm_id`, and in XML a body starting `<period><id>P</id><epoch>` whose zonegroups are `<zonegroups><obj><id>`; a period whose zonegroup holds sync policy groups → 501 `NotImplemented`; `GET /admin/realm/period?epoch=9` → 404 `NoSuchKey`; `POST /admin/realm/period` → 405; `POST /admin/ratelimit?ratelimit-scope=bucket&bucket=b&enabled=true&max-read-ops=1&max-write-ops=1&max-read-bytes=1&max-write-bytes=1` → 200 then `GET /admin/ratelimit?ratelimit-scope=bucket&bucket=b` → `{"bucket_ratelimit":{"max_read_ops":1,"max_write_ops":1,"max_read_bytes":1,"max_write_bytes":1,"enabled":true}}` (go-ceph's `TestBucketRateLimit` compares exactly this, with `max_list_ops`/`max_delete_ops` absent), and with `format=xml` → `<?xml version="1.0" encoding="UTF-8"?><bucket_ratelimit><bucket_ratelimit><max_read_ops>1</max_read_ops><max_write_ops>1</max_write_ops><max_read_bytes>1</max_read_bytes><max_write_bytes>1</max_write_bytes><enabled>true</enabled></bucket_ratelimit></bucket_ratelimit>`; `…scope=user&uid=alice` set and get → `{"user_ratelimit":{…}}`; `…global=true&ratelimit-scope=anon&max-read-ops=2` → 200 and `GET …?global=true` → `{"bucket_ratelimit":{…},"user_ratelimit":{…},"anonymous_ratelimit":{…}}` in that order; `GET …?global=yes` → 400; `POST …?ratelimit-scope=bucket&bucket=b` (no limits) → 400; a missing bucket → 404 `NoSuchKey`; a `ratelimit=read`-only caller on POST → 403.

- [ ] **Step 6: Implement the ops and handlers; record the opaque period; `make check`; commit**

`docs/exclusions.md`: one sentence appended to the "Zonegroup and period JSON with sync policy groups is not rendered" bullet: "The admin API's `GET /admin/realm/period` answers 501 NotImplemented for such a period, where radosgw renders it."

```bash
git add internal/driver/realm.go internal/driver/realm_test.go internal/driver/store.go internal/op/admininfo.go internal/op/admininfo_test.go internal/admin/realm.go internal/admin/realm_test.go internal/admin/ratelimit.go internal/admin/ratelimit_test.go internal/meta/attrs.go internal/meta/sysobj.go internal/meta/dump_period.go internal/meta/dump_test.go internal/meta/dump_goldens_test.go internal/memstore/admin.go docs/exclusions.md
git commit -m "feat(admin): realm and period getters, ratelimit settings"
```

## Task 13: Gate: integration spec with JSON and XML bodies compared with radosgw's, bypass-gc, go-ceph suite, Rook-first checklist, registry updates `[cluster]`

**Files:**
- Create: `test/integration/admin_test.go`, `test/integration/admin_suite_test.go`, `internal/meta/testdata/user_info.json` (captured), `internal/meta/testdata/bucket_instance.json` (captured)
- Modify: `internal/radosclient/goceph/goceph_integration_test.go` (Task 2 Step 6's spec lands here if not already), `hack/rooket/README.md`, `docs/ceph-upstream-bugs.md`, `internal/meta/user_json_test.go` and `bucket_json_test.go` (the captured fixtures wired in)

**Interfaces:**
- Consumes: T's `hack/rooket/` (`make cluster-up RELEASE=…`, `make populate`, `make integration`, `make rgw-go-up` writing `hack/rooket/out/<release>/rgw-go.endpoint`, `make admin-suite RELEASE=… GATEWAY=rgw-go RUN=…`, `make admin-parity RELEASE=…`, `hack/rooket/lib.sh`'s `admin`/`rgw_endpoint` helpers), `cephtest.ReadManifest`; go-ceph `rgw/admin` as a test client (G's dependency list); aws-sdk-go-v2 for bucket creation; every task above, Task 14 included.
- Produces: the phase-1 admin gate — green `admin-parity` on Squid and Tentacle, the integration spec with byte comparisons against radosgw, the registry updates.

- [ ] **Step 1: The integration spec `[cluster]`**

`test/integration/admin_test.go` (`//go:build integration`, `Label("integration")`), against a running rgw-go (`RGW_GO_ADMIN_ENDPOINT` from `hack/rooket/out/<release>/rgw-go.endpoint`) and the cluster's radosgw (`rgw_endpoint`) as the oracle, with an admin user created through `radosgw-admin user create --uid rgwgo-admin --caps "accounts=*;buckets=*;users=*;usage=*;metadata=*;zone=read;info=read;ratelimit=*"` (Rook's admin-ops caps plus `info`, `ratelimit` and the write caps the trim, index check and metadata specs need):

- `GetInfo` → `cluster_id` equals `ceph fsid` (`rooket k exec … ceph fsid`) on both gateways.
- The Rook-first sequence on rgw-go, each step asserting the go-ceph struct fields Rook reads: `CreateUser{ID, DisplayName, MaxBuckets, UserCaps: "users=read;buckets=*"}` → `GetUser` (caps, keys, op_mask, default placement); `ModifyUser{MaxBuckets: -1}`; `RemoveUserCap`/`AddUserCap`; `SetUserQuota`/`GetUserQuota`; a bucket created through S3 with the user's key; `GetBucketInfo{Bucket, Stats}` (owner, `usage["rgw.main"]`, `bucket_quota`); `SetIndividualBucketQuota`; `LinkBucket` to a second user and `GetBucketInfo` shows it; `GetBucketPolicy`; `ListBuckets` contains it; `RemoveBucket{PurgeObject: true}` after a `PutObject`; `RemoveUser`; `CreateAccount{Name}` → generated id, `GetAccount`, `ModifyAccount`, `DeleteAccount`; `GetUsers` contains `rgwgo-admin`.
- The body oracle: every read the admin API serves — `info`; `config?type=zone`; user info, create and modify (with and without keys, with `stats=true`); key create; caps add; `user?quota`; `bucket` single, all, all with stats, a uid's paged and unpaged listing; `bucket?policy`; `bucket?index&check-objects=true&fix=true`; account create, get and modify; `metadata` get of a user, a bucket and a bucket instance, `metadata` list plain and with `max-entries`; `user?list`; `usage` with and without the summary; `realm`, `realm?list`, `realm/period`; `ratelimit` for a bucket, a user and `global`; an `AccessDenied`, a `NoSuchKey`, a `NoSuchBucket` and a 405 of each path — is sent to radosgw and to rgw-go, over identically created fixtures, in `format=json` and `format=xml`, and `info` and one error also in `format=html`. Both bodies pass through the same normaliser, which replaces the values that differ between two runs of one gateway (ids, markers, bucket ids, versions and tags, times, access and secret keys, request and host ids, the fsid, the index shard version and marker strings) with placeholders, keyed on the element or key name in XML and JSON alike; the normalised bodies must be EQUAL byte for byte. A difference is a bug in rgw-go unless N-D2, N-D10 or a docs/exclusions.md bullet the tasks above wrote explains it, and the spec names the bullet in its exception list.
- The headers: every rgw-go response carries `Content-Length`, equal to radosgw's (its frontend's buffering filter supplies the length radosgw's `end_header` was not handed); every rgw-go body carries `Content-Type` `application/json`, `application/xml` or `text/html` by its format, except the realm, realm-list and period bodies, which carry `application/json` in every format; `Accept-Ranges: bytes` rides on every error document and on the three getters' bodies, on both gateways, and on no other response; radosgw's headers differ only as the admin headers bullet (Task 3) says — the spec asserts both sides.
- `metadata list user` paged with `max-entries=2` through rgw-go returns the same set of keys radosgw returns unpaged.
- Usage: with `rgw_enable_usage_log=true` on the rgw-go instance (T's harness sets the three usage options), a `PutObject` then `GetUsage{ShowSummary}` shows the category within the flush interval; `TrimUsage{RemoveAll}` returns 200 within 5 seconds (the bound).
- Bucket removal and GC: on each gateway create buckets `purge-<gw>` and `bypass-<gw>` through S3, each with two objects larger than `rgw_max_chunk_size` (4 MiB) so both have tail stripes. `DELETE /admin/bucket?bucket=purge-rgwgo&purge-objects=true` on rgw-go → 200, and `radosgw-admin gc list --include-all` (through `rooket k exec`) lists the two objects' tail stripes under the bucket's marker. `DELETE /admin/bucket?bucket=bypass-rgwgo&bypass-gc=true` on rgw-go → 200 with no `purge-objects`, the bucket is gone, no GC entry names an object with its marker prefix, and `rados -p <data pool> ls` lists no object with that prefix. The same bypass request on radosgw → 200 and the bucket is gone, but the data pool still holds one object's tail stripes, in no GC entry: the leak Task 14 records. If radosgw leaves nothing, the defect is fixed at that release: update Task 14's registry entry rather than the assertion.
- Both releases: run with `RELEASE=squid` and `RELEASE=tentacle`; the Tentacle run also exercises `SetAccountQuota`, `full_user_id` and the Tentacle keys of `bucket_stats` and the zone config, which the body oracle compares like every other field.

- [ ] **Step 2: Capture the fixtures**

From the Squid cluster: `radosgw-admin user info --uid rgwgo-admin` → `internal/meta/testdata/user_info.json`; `radosgw-admin metadata get bucket.instance:<bucket>:<id>` → `bucket_instance.json`; wire both into Task 8's decoder specs as "decodes and re-marshals identically" cases (through `rooket k`, never the ambient cluster).

- [ ] **Step 3: The go-ceph suite**

```sh
make cluster-up RELEASE=squid && make populate RELEASE=squid && make rgw-go-up RELEASE=squid
make admin-suite RELEASE=squid GATEWAY=rgw-go RUN=n1
make admin-parity RELEASE=squid
```

and the same for `tentacle`. `admin-parity` (T Task 13) compares the run with `test/admin/baseline/<release>.json` (radosgw's own outcomes at go-ceph v0.39.0): every test radosgw passes must pass, every test radosgw fails must fail the same way. Expected on Squid: `TestAccountQuota` skipped (go-ceph skips below Tentacle), `TestAccount`'s get/delete skipped by the suite; on Tentacle they run. A mismatch is fixed in the owning task before this task is called done.

- [ ] **Step 4: Rook-first checklist**

Record in the PR description that the operator's admin calls (unit N's gate in the index, tallied against rook f09547c: `CreateUser`, `GetUser`, `ModifyUser`, `RemoveUser`, `AddUserCap`, `RemoveUserCap`, `SetUserQuota`, `GetUserQuota`, `CreateKey`, `RemoveKey`, `SetIndividualBucketQuota`, `GetBucketInfo`, `GetBucketPolicy`, `ListBuckets`, `LinkBucket`, `RemoveBucket`, `CreateAccount`, `GetAccount`, `ModifyAccount`) are exercised by Step 1, and that the Rook integration tests `user/{caps,keys,opmask,placement,storageclass}`, `bucket/{owner,quota}`, `dependents` and `cosi` run in T's Rook workflow against the derived image (T Tasks 8–10). Nothing in this task runs Rook itself.

- [ ] **Step 5: Registry updates; the exclusions check**

`docs/ceph-upstream-bugs.md`: the two usage-trim entries' **rgw-go** lines become "Task 10's `TrimUsage` bounds each shard's trim at `ceil(records/1000)+1` rounds (N-D4) and logs a warning naming this tracker when the bound is reached without ENODATA"; add two **Quirk** entries with evidence at v19.2.6 and v20.2.4: (a) "radosgw's account modify answers `BucketAlreadyExists` for a taken account name" — `rgw::account::modify` returns `-EEXIST` ([rgw_account.cc:262-265](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_account.cc#L262-L265) via [account.cc:296-310](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/account.cc#L296-L310)) and `RGWOp_Account_Modify` does not map it ([rgw_rest_account.cc:166-168](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_account.cc#L166-L168)), so the S3 table's EEXIST row applies ([rgw_common.cc:113](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L113)); rgw-go mirrors it (Task 7); (b) "the admin bucket list with stats silently drops tenanted buckets" — `RGWBucketAdminOp::info` calls `bucket_stats(tenant of the uid, "<tenant>/<name>")` for every metadata key and ignores its return ([rgw_bucket.cc:1708-1714](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_bucket.cc#L1708-L1714)); rgw-go splits the key and reports the stats, a difference recorded in docs/exclusions.md by Task 6. Before adding either, search tracker.ceph.com and ceph/ceph's pull requests for the functions and messages involved, and link what the search finds on the entry's **Upstream** line, or write "not filed".

`docs/exclusions.md` needs no edit here: each task recorded its own difference. Check that the coexistence section holds every bullet and sentence Tasks 3, 4, 6, 9, 10, 11, 12 and 14 name, and report the set for the rgw-rs session.

- [ ] **Step 6: README and commit**

`hack/rooket/README.md` gains "Running the admin gate": the three commands of Step 3 and how to point go-ceph's client at rgw-go (`RGW_GO_ADMIN_ENDPOINT`).

```bash
git add test/integration internal/meta/testdata internal/meta/*_test.go hack/rooket/README.md docs/ceph-upstream-bugs.md
git commit -m "test(admin): cluster gate, go-ceph parity and the registry entries"
```

The PR description states: the gaps this unit leaves (purge and object removal need W and P on the real driver; `check-objects`' forced re-check; the admin handler's own in-flight counter beside S3's; `path_prefix` on account user listing), the release gates (N-D10), and that no Rook run happened here (T's workflow is the Rook gate).

## Task 14: Bypass-GC purge in the driver: `PurgeBypassGC` over W's, R's and P's internals

This task lands after units W (Tasks 4-6 and 9), R (Task 3) and P (Task 7), whose internals it calls read-only; until it lands, `DELETE /admin/bucket?bypass-gc=true` on the real driver answers Task 1's `op.ErrNotImplemented`, while the memstore behaviour of Task 6 serves the op and handler specs.

**Files:**
- Create: `internal/driver/purge.go`, `internal/driver/purge_test.go`
- Modify: `internal/driver/store.go` (the `PurgeBypassGC` stub removed), `docs/exclusions.md`, `docs/ceph-upstream-bugs.md`

**Interfaces:**
- Consumes, read-only: W's `gcChain`, `rawTag`, `newIndexOp`, `indexOp.prepare`, `deleteObjIndex`, `s.pools`, `s.w.opts.gcMaxConcurrentIO`; R's `readHead`, `headRef`; P's `abortMultiparts`; M's `readShardHeaders`, `syncOwnerStats`, the driver's `ListObjects` with `AllowUnordered`; phase 0's `refcount.Put`, `rgw.ObjRemove`, `radosclient.OpFlagFullTry`, `radosclient.OpFlagNone`; G's `op.FromRADOS`; Task 1's `op.BucketAdminStore.PurgeBypassGC`; Task 6's `op.RemoveBucketAdmin` (in the specs).
- Produces: the driver's `PurgeBypassGC`, replacing Task 1's stub; no interface changes.

- [ ] **Step 1: Write the failing specs**

`internal/driver/purge_test.go`, on fakerados through the fixture W's delete specs use (W's `PutObject` with a 4 MiB head and stripe, `rgw_gc_max_concurrent_io` set to 2, bucket `b` owned by `alice`), with the fakerados surface W and R grow (`GCEntries`, `BeforeWrite`, `FailNextWrite`, `Entry`, `Object`, `Remove`); `tailOIDs(rec, key)` lists an object's stripes past the head from its manifest before the purge, `dataObjects(rec)` the data pool's objects under the bucket's marker, `gcOIDs()` every chain object of every GC entry:

```go
It("releases every tail directly, the first object's included, and leaves no GC entry", func(ctx SpecContext) {
	putObject(ctx, rec, "big1", 9<<20)
	putObject(ctx, rec, "big2", 9<<20)
	putObject(ctx, rec, "small", 10)
	putRawHead(ctx, rec, "raw", []byte("x")) // a head without a manifest xattr
	up := initUpload(ctx, rec, "mp")
	putPart(ctx, up, 1, 5<<20)
	Expect(store.PurgeBypassGC(ctx, rec)).To(Succeed())
	Expect(dataObjects(rec)).To(ConsistOf(headOID(rec, "raw")), "an object without a manifest is the ordinary purge's")
	Eventually(func() []string { return indexKeys(rec) }).Should(ConsistOf("raw"))
	Expect(gcOIDs()).NotTo(ContainElement(HavePrefix(rec.Info.Bucket.Marker + "_")), "no tail waits for GC")
	Expect(gcTags()).To(ConsistOf(up.ID), "the aborted upload's parts go to GC under the upload id")
	_, err := store.GetBucket(ctx, "", "b")
	Expect(err).NotTo(HaveOccurred(), "the bucket itself is the ordinary purge's")
})
It("keeps at most rgw_gc_max_concurrent_io deletes in flight", func(ctx SpecContext) {
	for i := range 6 {
		putObject(ctx, rec, fmt.Sprintf("o%d", i), 13<<20)
	}
	var inFlight, peak atomic.Int32
	cluster.BeforeWrite(func(pool, oid string, w *radosclient.WriteOp) {
		if pool != dataPoolName {
			return
		}
		n := inFlight.Add(1)
		for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
		}
		time.Sleep(time.Millisecond)
		inFlight.Add(-1)
	})
	Expect(store.PurgeBypassGC(ctx, rec)).To(Succeed())
	Expect(peak.Load()).To(BeNumerically("<=", 2))
	Expect(dataObjects(rec)).To(BeEmpty())
})
It("counts a tail stripe already gone as deleted", func(ctx SpecContext) {
	putObject(ctx, rec, "big", 9<<20)
	cluster.Remove(dataPoolName, "", tailOIDs(rec, "big")[0])
	Expect(store.PurgeBypassGC(ctx, rec)).To(Succeed())
	Expect(dataObjects(rec)).To(BeEmpty())
})
It("stops at a failed delete and returns it once the deletes in flight end", func(ctx SpecContext) {
	putObject(ctx, rec, "big", 9<<20)
	cluster.FailNextWrite(dataPoolName, tailOIDs(rec, "big")[1], radosclient.ErrPermission)
	Expect(store.PurgeBypassGC(ctx, rec)).To(MatchError(op.ErrAccessDenied))
})
It("fails on a bucket without index objects, as read_stats fails", func(ctx SpecContext) {
	nx := createIndexlessBucket(ctx, "nx", alice)
	Expect(store.PurgeBypassGC(ctx, nx)).To(MatchError(op.ErrNoSuchBucket))
})
It("removes a bucket through the admin op on both paths", func(ctx SpecContext) {
	purged := createBucket(ctx, "", "purged", alice)
	putObject(ctx, purged, "big", 9<<20)
	purgedTails := tailOIDs(purged, "big")
	bypassed := createBucket(ctx, "", "bypassed", alice)
	putObject(ctx, bypassed, "big", 9<<20)

	o := op.NewRemoveBucketAdmin()
	o.Bucket, o.PurgeObjects = "purged", true
	Expect(runAdmin(ctx, o)).To(Succeed())
	Expect(gcOIDs()).To(ContainElements(purgedTails), "the ordinary purge queues the tails for GC")

	o = op.NewRemoveBucketAdmin()
	o.Bucket, o.BypassGC = "bypassed", true // no purge-objects: bypass-gc purges anyway
	Expect(runAdmin(ctx, o)).To(Succeed())
	Expect(dataObjects(bypassed)).To(BeEmpty())
	Expect(gcOIDs()).NotTo(ContainElement(HavePrefix(bypassed.Info.Bucket.Marker + "_")))

	for _, name := range []string{"purged", "bypassed"} {
		_, err := store.GetBucket(ctx, "", name)
		Expect(err).To(MatchError(op.ErrNoSuchBucket))
	}
})
```

(`runAdmin` runs the op with `op.Run` on a request whose `Env` is `store.Env()` and whose identity holds `buckets=*`.) Run: FAIL on the stub's `op.ErrNotImplemented`.

- [ ] **Step 2: Implement `internal/driver/purge.go`**

```go
package driver

// PurgeBypassGC is RadosBucket::remove_bypass_gc's data pass (rgw_sal_rados.cc:470-597;
// v20.2.4 :491-618). The refresh it starts with is the caller's bucket read;
// the bucket removal it ends with is the ordinary purge the caller runs next.
func (s *Store) PurgeBypassGC(ctx context.Context, rec *op.BucketRecord) error {
	// read_stats over the current index (:487-490). radosgw names the shard
	// objects from num_shards whatever the layout (svc_bi_rados.cc:130-160,
	// :191-215), so a bucket without index objects fails here with ENOENT.
	if _, err := s.readShardHeaders(ctx, &rec.Info); err != nil {
		return op.FromRADOS(err, op.ScopeBucket)
	}
	// abort_multiparts (:492-495): the ordinary abort, whose parts the
	// daemon's garbage collector takes under the upload id.
	if _, err := s.abortMultiparts(ctx, rec); err != nil {
		return err
	}
	p := &bypassPurge{s: s, rec: rec, sem: semaphore.NewWeighted(int64(s.w.opts.gcMaxConcurrentIO))}
	// Every version of the plain namespace, unordered, 1000 at a time (:500-509).
	marker := ""
	for {
		res, err := s.ListObjects(ctx, rec, op.ListObjectsParams{ListVersions: true, AllowUnordered: true, Marker: marker, MaxKeys: 1000})
		if err != nil {
			return p.stop(err)
		}
		for _, e := range res.Entries {
			if err := p.object(ctx, e.Key); err != nil {
				return p.stop(err)
			}
		}
		if !res.Truncated {
			break
		}
		marker = res.NextMarker
	}
	if err := p.stop(nil); err != nil {
		return err
	}
	// sync_owner_stats; its result is only logged (:591-594).
	if _, err := s.syncOwnerStats(ctx, rec.Info.Owner, &rec.Info); err != nil {
		slog.WarnContext(ctx, "failed to sync owner stats before the bucket delete", slog.String("bucket", rec.Info.Bucket.Name), slog.Any("error", err))
	}
	return nil
}

// bypassPurge issues remove_bypass_gc's deletes with at most
// rgw_gc_max_concurrent_io in flight across tails and heads, the bound the
// garbage collector puts on its own deletes (rgw_gc.cc:371, :382-392;
// v20.2.4 :385, :399-409); radosgw's admin op sets no bound at all (rgw_rest_bucket.cc:225-248,
// rgw_bucket.h:248). The first failed delete stops new ones, and the purge
// returns it once the rest have ended, as drain_aio returns a failed
// completion's result (rgw_sal_rados.cc:98-112).
type bypassPurge struct {
	s   *Store
	rec *op.BucketRecord
	sem *semaphore.Weighted
	wg  sync.WaitGroup
	mu  sync.Mutex
	err error
}

func (p *bypassPurge) failed() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

// issue runs del in the budget; a failure is mapped as the admin op maps the
// errno radosgw's drain returns (rgw_rest_bucket.cc:245-247).
func (p *bypassPurge) issue(ctx context.Context, del func() error) error {
	if err := p.failed(); err != nil {
		return err
	}
	if err := p.sem.Acquire(ctx, 1); err != nil {
		return err
	}
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		defer p.sem.Release(1)
		if err := del(); err != nil {
			p.mu.Lock()
			if p.err == nil {
				p.err = op.FromRADOS(err, op.ScopeBucket)
			}
			p.mu.Unlock()
		}
	}()
	return nil
}

// stop waits for the deletes in flight, then returns err or else the first
// failed delete.
func (p *bypassPurge) stop(err error) error {
	p.wg.Wait()
	if err != nil {
		return err
	}
	return p.failed()
}

// object is one listed entry (:510-582): its head read as a state that does
// not follow an olh; a missing head is skipped (:522-525); with a manifest,
// every tail stripe is released and then the head removed; without one,
// nothing is done here and the ordinary purge deletes the object.
func (p *bypassPurge) object(ctx context.Context, key meta.ObjKey) error {
	st, err := p.s.readHead(ctx, p.rec, key, false)
	if err != nil {
		return err
	}
	if !st.Exists {
		slog.InfoContext(ctx, "bypass-gc purge: cannot find object state", slog.String("bucket", p.rec.Info.Bucket.Name), slog.String("key", key.Name))
		return nil
	}
	if st.Manifest == nil {
		return nil
	}
	chain, err := p.s.gcChain(st.Manifest, p.rec.Info.PlacementRule)
	if err != nil {
		return err
	}
	// The refcount tag is the tail tag, else the object's own (:539-541).
	tag := rawTag(st, meta.AttrTailTag)
	if tag == "" {
		tag = rawTag(st, meta.AttrIDTag)
	}
	for _, o := range chain {
		if err := p.issue(ctx, func() error { return p.tail(ctx, o, tag) }); err != nil {
			return err
		}
	}
	return p.head(ctx, key, st)
}

// tail is the garbage collector's delete of one chain object (rgw_gc.cc:676-686;
// v20.2.4 :688-698): a refcount put under the tag with the implicit reference,
// the locator set, allowed into a full pool's reserve, and ENOENT counted as
// done (:413-415; v20.2.4 :428).
func (p *bypassPurge) tail(ctx context.Context, o rgw.GCObj, tag string) error {
	pool, err := p.s.pools.get(ctx, meta.ParsePool(o.Pool))
	if err != nil {
		return err
	}
	if o.Loc != "" {
		pool = pool.WithLocator(o.Loc)
	}
	w := radosclient.NewWriteOp()
	refcount.Put(w, tag, true, p.s.release)
	if _, err := pool.Write(ctx, o.Key.Name, w, radosclient.OpFlagFullTry); err != nil && !errors.Is(err, radosclient.ErrNotFound) {
		return err
	}
	return nil
}

// head is delete_obj_aio with keep_index_consistent (rgw_rados.cc:10687-10732;
// v20.2.4 :11624-11669): an index prepare DEL under a fresh tag, since a state
// just read carries no write tag (UpdateIndex::prepare, :7087-7093); obj_remove
// with no kept prefix, no guard and no flags; then delete_obj_index's
// unprepared complete_del (:6033-6050), issued without waiting for the head.
func (p *bypassPurge) head(ctx context.Context, key meta.ObjKey, st *op.ObjectState) error {
	ref, err := p.s.headRef(ctx, p.rec, key)
	if err != nil {
		return err
	}
	x := p.s.newIndexOp(p.rec, key, "")
	if err := x.prepare(ctx, rgw.OpDel); err != nil {
		return op.FromRADOS(err, op.ScopeBucket)
	}
	w := radosclient.NewWriteOp()
	rgw.ObjRemove(w, nil, p.s.release)
	if err := p.issue(ctx, func() error {
		_, err := ref.pool.Write(ctx, ref.oid, w, radosclient.OpFlagNone)
		return err
	}); err != nil {
		return err
	}
	p.s.deleteObjIndex(p.rec, key, st.Mtime)
	return nil
}
```

`store.go` drops the `PurgeBypassGC` stub. A head R's `readHead` refuses as a versioned bucket's OLH answers its `op.ErrNotImplemented` here too: versioned buckets are phase 2. Run the driver suite: PASS.

- [ ] **Step 3: Record the differences and the defects**

`docs/exclusions.md`, "Coexistence obligations independent of any exclusion", a new bullet at the end of the admin bullets that begin with the admin headers bullet:

"- **The admin bucket removal with bypass-gc deletes every tail, and with a bound.** `DELETE /admin/bucket?bypass-gc=true` deletes the bucket's object data directly instead of queueing it for garbage collection, as radosgw's `remove_bypass_gc` does, with three differences. radosgw's admin op never sets that loop's concurrency, so its budget starts at 0: the first object whose manifest it walks keeps its tail stripes, neither deleted nor queued, and every later delete is issued without a bound ([`rgw_sal_rados.cc:505`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L505), [`:542-550`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L542-L550); `rgw_bucket.h:248`). rgw-go deletes every object's tails and keeps at most `rgw_gc_max_concurrent_io` deletes in flight. A tail stripe that is already gone counts as deleted, as the garbage collector counts it, where radosgw's removal stops on it (tracker #24789): the admin op answers 404 NoSuchBucket for a bucket that still exists, after deleting every head it reached, and a retry then removes the bucket. And the tail deletes may use a full pool's reserve, as the garbage collector's do, so the removal can free space on a full cluster."

`docs/ceph-upstream-bugs.md`: main carries both defects, "radosgw's admin API bypass-gc removal leaks a tail and runs unbounded" and "radosgw's bypass-gc bucket removal fails once a tail stripe is gone", whose **rgw-go** lines already describe this purge; this task does not write them again. Append one sentence to each **rgw-go** line naming where rgw-go does it: "rgw-go's `PurgeBypassGC` (`internal/driver/purge.go`) does so." If main lacks either entry when this task runs, stop and report it: the registry owns those entries, not this plan.

Report the exclusions bullet and the two appended sentences for the rgw-rs session.

- [ ] **Step 4: `make check`; commit**

```bash
git add internal/driver/purge.go internal/driver/purge_test.go internal/driver/store.go docs/exclusions.md docs/ceph-upstream-bugs.md
git commit -m "feat(driver): purge a bucket with bypass-gc as radosgw's remove_bypass_gc does, bounded"
```

## Self-review

**1. Spec coverage.** §9's "Admin:" sentence and unit N's goal in the index (`00-index.md`, "The nine units"), item by item: user with keys, subusers, caps and quota → Tasks 4, 5; bucket except Sync_Bucket (info, list, stats, link, unlink, remove with purge and with bypass-gc, check index, policy, object remove, quota) → Tasks 6, 11 and 14 (`PUT bucket?sync` is 405 in Task 3's table; N-D9 for both purges); usage show and a bounded trim → Task 10 (N-D4); metadata over user, bucket and bucket.instance (get, put, list, remove) → Tasks 8, 9, with the opaque markers of D5 from Task 2; info → Task 3; account → Task 7; config → Task 3; ratelimit settings stored (user, bucket and the period config) → Task 12; read-only realm and period getters → Task 12; caps checked from the identity → Task 3's `CheckCaps` on `Identity.Caps` with G's admin override; radosgw's response shapes in JSON, XML and HTML → every handler renders through `internal/formatter` with `Dump` methods that transcribe radosgw's `dump()`s (N-D2, N-D11; N-D8 for the two user forms), the corpus pins those dumps byte for byte (Task 3's gate, extended by Tasks 4, 6, 7, 8 and 12), and Task 13's body oracle compares every admin response with radosgw's byte for byte in JSON and XML. `MetadataStore` filled → Task 8. The seams N owns → Task 2. The go-ceph rgw/admin gate and the Rook-first order → Task 13 and the task index. Not in scope and said so: `/admin/log`, realm/period POST, Sync_Bucket, `/admin/dedup`, `/admin/restore` (docs/exclusions.md); the forced disk re-check of `check-objects` (Task 11); purge on the real driver until W and P land, and bypass-gc until Task 14 lands after W, R and P (N-D9).

**2. Placeholder scan.** Searched for TBD, TODO, "implement later", "fill in", "similar to Task", "add appropriate", "handle edge cases" and pending markers: none remain. The rjenkins vectors in Task 2 are real numbers generated from Ceph's source; the meta fixtures in Task 8 are hand-written until Task 13 captures radosgw's; the byte strings in the formatter and `Dump` specs are derived from Formatter.cc, HTMLFormatter.cc and the corpus goldens.

**3. Type consistency.** `formatter.Formatter` with `NewJSON`, `NewXML` and `NewHTML` (Task 3) is what every rendering task writes to, through `WriteBody`, `WriteDumped`, `WriteTyped`, `WriteEmpty` and `WriteError` (Task 3), the three send paths N-D2 names; `WriteTyped` and `WriteError` alone set the length through G's `s3.SetContentLength`, with `Accept-Ranges`; the XML and HTML writers escape text through G's `xmltext.Escape`, and `formatter` keeps only `EscapeJSON` of its own; `meta.Dumper`, `dumpTime`, `dumpPool`, `dumpStrings`, `dumpMap`, `dumpSection` (Task 3) and `dumpValues` (Task 12) are the helpers the `Dump` methods share. `admin.Route{Name, Payloads}` carries each route's payload forms into `Authenticate(ctx, req, route.Payloads)` (Task 3). `op.AdminOp` is an empty struct with `Action`/`OpMask`/`Init`; every op defines its own `VerifyPermission` through `op.CheckCaps(r, typ, perm)` (Task 3), and Tasks 4–12 use `NewXxx()` constructors and `Result` fields as declared. `op.UserKeyParams`, `addKey`, `removeKey`, `lookupUser`, `PurgeBucket`, `DeleteBucketWithChildren` (Task 4/5) are the names Tasks 6 and 7 call. `op.BucketStatsData`, `loadAdminBucket`, `categoryName`, `shardString`, `dumpBucketUsage` (Task 6) are reused by Task 11. `op.AccountParams`, `loadAccount` (Task 7). `op.SyncType` (Task 9). `op.MetadataEntry.Doc` (Task 8) is what Task 9's getter dumps. `op.UsageRecord`, `UsageIter`, `UsageData`, `S3SelectUsage` (Task 1) carry through Task 10. The Task 1 interfaces — `UsageReader`, `BucketAdminStore` (with `ChownBucket`, `SyncOwnerStats` and `PurgeBypassGC`), `RealmStore`, the widened `AccountStore` — match the driver signatures in Tasks 6, 7, 10, 11, 12 and 14 and the memstore methods in Tasks 1 and 6; Task 6's `RemoveBucketBypassGC` calls `PurgeBypassGC` and then `PurgeBucket`. `admin.Dispatch(method, q, rel)` takes the release everywhere it is named. `Env.ClusterID` is added in Task 3 and set in `cli serve`. `meta` additions: `caps.go` (Task 1), `Time.Gmtime`, the zone family's and `Quota`'s `Dump` (Task 3), `ValidAccountID`, `IdentityType.DumpName`, `Caps.DumpAs` (Task 4), `Quota.UnmarshalJSON` (Task 5), `BucketID` and `DataPlacement` `Dump` (Task 6), `AccountInfo.Dump` (Task 7), `UserInfo`/`Caps`/`BucketEntryPoint`/`BucketInfo.UnmarshalJSON`, `AttrsJSON`, `UserCompleteInfo`, `BucketCompleteInfo`, their `Dump`s and `ObjVersion.Dump` (Task 8), `AttrRateLimit`, `RealmNamesPrefix` and the period family's `Dump`s (Task 12) — each named in Global Constraints. `policy.ActionNone` (Task 1). `radosclient.RJenkins`, `PlacementHash`, `EncodeListToken`, `DecodeListToken`, `ListAfter`, `Cluster.FSID`, `Pool.ListObjectsFrom` (Task 2) are the names Tasks 8 and 13 use. `cls/user` account resource ops (Task 7). Error sentinels are G's names throughout; the two deliberate radosgw quirks (`ErrBucketAlreadyExists` for a taken account name on modify, `ErrNoSuchKey` for raw ENOENT, N-D6) are named where they apply.

**4. Review Focus.** (1) foreign/stale markers: Task 2 specs (deleted last object, foreign token → `ErrBadOp`), Task 8 (`List` maps to `ErrInvalidArgument`), Task 9 (non-base64 marker restarts; raw `user?list` marker). (2) sub-resource order: Task 3 `ParseArgs` spec (`key&quota` → key) and Task 5's `PUT …?key&quota` handler spec. (3) tolerant booleans: Task 3's `Bool` table and Task 12's `global=yes` → 400. (4) trim without ENODATA: Task 10's payer and bucket-filter specs on fakerados, with the round bound asserted. (5) account users through the user endpoints: Task 4's tenant/root/IAM-name specs and index assertions, Task 7's index driver specs. (6) formats: Task 3's `SelectFormat` table (an `Accept` list, capitals, a `;q=` suffix), the 405 answered in JSON for an unknown resource and in the selected format for an unknown method, and the XML spec of every rendering task. (7) bypass-gc: Task 6's op specs (purge without `purge-objects`, a failing data pass), Task 14's fakerados specs (every object's tails released, the bound, a missing tail stripe, both paths through the op) and Task 13's GC gate against radosgw.

**Contract findings for the caller** (nothing in G's or Z's frozen contract was changed; all additions are additive): (a) G's `cli serve` composes one handler and G's dispatch table has no `/admin` — N mounts a second handler (N-D1); a bucket named `admin` is shadowed, as in radosgw. (b) G's contract has no usage READ, no realm/period reads, no bucket-admin primitives and A's `AccountStore` has only `GetAccount` — N adds `UsageReader`, `RealmStore`, `BucketAdminStore` (with `PurgeBypassGC`) and widens `AccountStore` (Task 1) and adds `Env.{UsageReader,BucketAdmin,Realms,ClusterID}`. (c) `Op.Action()` has no value for an op outside IAM — `policy.ActionNone` added (N-D7). (d) Z's `authz.AccountLookup` and A's `op.AccountStore` are satisfied by one implementation (`AccountName` on the store, N-D5); Z could take `op.AccountStore` directly. (e) G's frozen `MetadataStore.Put` carries the write version in `MetadataEntry.Version` and the read check in `PutMetadataOptions.IfVersion`; the sync-type decision lives in the op (Task 9) — no contract change needed; `MetadataEntry` gains the additive `Doc` so the getter can render every format (Task 8). (f) `go-ceph`'s `Iter.Token()` is unusable as a listing marker (it is the next-batch cursor); the seam resumes by placement hash instead (N-D3) — the seam-gap wording "pg-hash token" (`00-index.md`, "Shared interfaces and seam gaps") is honoured in spirit, not by `Token()`. (g) M's listing cannot force `check_disk_state` on every entry; `check-objects` lists without the forced re-check (Task 11) — a candidate `ListObjectsParams.ForceCheck` for M if the dashboard's index check is ever relied on. (h) Task 14 reads W's, R's and P's driver internals (Global Constraints) and so lands after units W, R and P; those units must keep the named functions' signatures, or tell this unit when they change. (i) The admin handler consumes G's `op.PayloadForms` and the three-argument `Authenticate` unchanged: `op.PayloadSigned` for the metadata PUT, the zero value for every other admin route (Task 3). (j) G's exported `s3.SetContentLength` and its leaf `xmltext` package are consumed as G Task 4 defines them: the escaper is shared rather than transcribed twice, and the length helper is how admin errors and the three getters carry radosgw's `Accept-Ranges` (Task 3).
