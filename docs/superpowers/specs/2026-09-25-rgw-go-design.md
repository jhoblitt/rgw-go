# rgw-go design

Status: design approved section by section on 2026-09-25; this document
consolidates it for review before planning. Companion: `docs/exclusions.md`,
the canonical excluded-features list shared with rgw-rs, which also holds
the coexistence obligations and the benchmark parity settings. Claims about
C++ RGW, Rook and go-ceph behavior in both documents were checked against
ceph/ceph main at e234256339f and the v19.2.3 tag, Rook main at d364b1e8a,
and go-ceph v0.39.0.

## 1. Goal

rgw-go is a reimplementation of Ceph's RADOS Gateway in Go 1.27 over
ceph/go-ceph, with go-ceph improvements made in the jhoblitt/go-ceph fork.
Its goal, in the user's words, is to be a drop-in replacement for C++ RGW
in a Rook cluster running Squid or Tentacle.

The acceptance criterion is explicit: rgw-go, installed in a derived Ceph
image as `/usr/bin/radosgw`, passes Rook's object integration suite,
`TestCephObjectSuite`, in both of its passes, with and without TLS, against
current Rook main. That suite's shared store is Rook's zoned shape, a single
zone declared through the realm, zonegroup and zone resources with shared
pools mapped as RADOS namespaces, plus a classic store for the dependents
test. Multi-zone sync tests are excluded and the current suite has none;
the keystone suite is excluded because Keystone is; the helm and upgrade
suites test Rook rather than the gateway.

Beyond the criterion, rgw-go is benchmarked against C++ radosgw and rgw-rs
on the same cluster, and the project evaluates whether go-ceph's cgo
boundary is a performance bottleneck a pure-Go RADOS client would remove.
The evidence accumulates in `docs/cgo-limitations.md`.

## 2. Objectives, in priority order

1. Performance: latency and throughput on the S3 data path.
2. Scalability: more concurrent requests with fewer OS threads per process.
3. A compact, maintainable code base, leaning on existing Go packages
   wherever that does not cost the first objective.

Measurable forms: identical RADOS round-trip counts to radosgw on both hot
paths, so any difference is attributable to the pipeline, the HTTP layer or
the cgo boundary; an OS thread count under load of GOMAXPROCS plus
librados's own threads plus a handful, independent of connection count,
against radosgw's 128-thread request pool; and a measured, not assumed,
cost for the cgo boundary.

## 3. Decisions and reasons

- **Bit-compatible with radosgw's RADOS layout.** Same pools and
  namespaces, same object naming, same object-class index protocol, same
  versioned encodings. Checked against the C++ write and read paths: the
  layout costs little on the hot path, so a new layout would confound the
  benchmark rather than win it; the same layout isolates the variable this
  project studies; and radosgw becomes the correctness oracle. The
  unmodified radosgw-admin administers rgw-go's metadata, so no CLI is
  reimplemented.
- **Ceph floor Squid 19.2.6 or Tentacle 20.2.4, feature level Tentacle
  where gateway-side.** Every daemon of a supported cluster, its OSDs and
  any radosgw sharing the zone included, runs 19.2.6 or later on Squid or
  20.2.4 or later on Tentacle. The floor governs which OSD-side class
  behavior may be assumed, and point releases below it get no special
  handling. It constrains running daemons, not data: metadata, index
  entries and queue state written by any earlier release are still decoded,
  and damage an older release left behind, such as queue accounting drift,
  is still tolerated. Feature level has a precise form through the encoding
  rule in section 8.
- **Own gateway core, borrowed edges.** An RGW-shaped op pipeline on
  net/http, one flat store interface, one RADOS driver, and a narrow RADOS
  client seam over go-ceph. Rejected: a wholesale port of RGW's class
  hierarchy, whose storage handle hierarchy exists for six backends and
  collapses with one; and assembling on an existing Go S3 frontend, which
  would put a framework we do not control between the socket and the hot
  path and would need a patch series to reach radosgw's observable
  behavior.
- **Ceph encoding primitives and object-class clients live in rgw-go, not
  go-ceph.** Ceph itself packages every class client as an RGW-internal
  static library with no installed header; only cls_lock is generalized,
  and that by librados embedding it. Only librados bindings go to the fork.
- **The class packages and the encoding primitives depend only on each
  other and on the seam's exec-step abstraction, never on RGW types or
  the driver.** The one leak in the C++ headers, the user identity type in
  usage entries, is handled by the class package owning a small copy.
- **The binary reaches the pod through a derived Ceph image** that
  replaces `radosgw`, because Rook has no per-daemon image override. A
  Rook-side override is a later step.
- **Exclusions** are decided by the three tests in `docs/exclusions.md`
  and recorded there with their coexistence obligations.

## 4. Architecture

One binary, one module, `internal/` throughout. Dependencies point downward.

| Package | Provides |
|---|---|
| `cmd/rgw-go` | `main`, signal context, nothing else |
| `internal/cli` | cobra tree with `serve` and `version`; the radosgw-argv mode |
| `internal/cephconf` | early-argument parsing, the librados config bridge, typed access to `rgw_*` options |
| `internal/frontend` | `http.Server` instances from the beast frontend spec, TLS, per-request deadlines, drain on shutdown |
| `internal/s3`, `internal/admin`, `internal/iam` | protocol layers: dispatch, parsing, response and error documents |
| `internal/op` | the ops, one type per operation, with the store interfaces they consume |
| `internal/auth`, `internal/policy`, `internal/acl` | signature verification with chunked and trailer readers; the IAM policy language; ACL semantics and encoding |
| `internal/driver` | the RADOS store: zone and placement resolution, object naming, manifests and striping, atomic head writes, the index protocol, GC enqueue, metadata cache with watch and notify, quotas, usage |
| `internal/cls/<class>` | one package per object class: request and response marshalling over the exec step |
| `internal/meta` | RGW's stored types with their encodings: identity primitives, metadata objects, zone configuration, manifest and compression info |
| `internal/denc` | Ceph encoding primitives |
| `internal/radosclient`, `internal/radosclient/goceph` | the seam, and its implementation over go-ceph, the only package importing it |
| `internal/metrics`, `internal/asok`, `internal/opslog`, `internal/version` | Prometheus counters; the admin-socket protocol with RGW-shaped counters; the ops log; build info |

Rules inside the table:

- The seam is a leaf package, an accepted departure from declaring
  interfaces at the consumer: it has two consumers, the driver and every
  class package, and one implementer.
- `op` owns the store interfaces, split by concern so each stays small,
  with counterfeiter fakes beside them.
- Types that carry semantics live with their semantics: ACL policy with
  grant evaluation, lifecycle, CORS, website, object lock, tags, public
  access block and encryption configuration each beside their evaluation
  and XML. `meta` holds only what is stored and looked up.
- The op layer is protocol-neutral: S3, the admin API, IAM and STS share
  the user, bucket and account operations, lifted above the driver as
  rgw-rs found they should be.
- S3 dispatch is a table matched on method, scope and subresource in
  radosgw's precedence, after Host, path and query are parsed once. No
  path router: none can express subresource dispatch or wildcard hosts,
  and the one that can match query keys scans regex routes linearly.

Departures from the Go house canon, each with its reason: cgo in the
unit-test suite and CI, because go-ceph is cgo; a derived Ceph image
instead of ko on distroless, because the binary needs librados; zero
`ReadTimeout` and `WriteTimeout` with per-request deadlines through
`http.ResponseController`, because objects are large; and no path router.

Third-party dependencies, complete: go-ceph, cobra, viper,
klauspost/compress, pierrec/lz4/v4, the Prometheus client,
cespare/xxhash/v2, x/sync, Ginkgo and Gomega, counterfeiter, and
aws-sdk-go-v2 plus go-ceph's rgw/admin as test clients. pierrec/lz4/v4
decodes LZ4 in Ceph's per-chunk framing in phase 1 and encodes it in
phase 2, because klauspost/compress has no LZ4 codec; cespare/xxhash/v2
computes the XXH64 that radosgw uses to pick a GC shard, and the
Prometheus client already requires it. Everything else is standard
library.

## 5. Configuration and invocation

Rook launches the `radosgw` binary with `--foreground`, `--id`, `--host`,
`--setuser`, `--setgroup`, mon and keyring flags, `--rgw-enable-apis`, the
beast frontend spec, and optional ops-log flags, and sets `rgw_zone`,
`rgw_zonegroup`, usage-log and logging options through the mon config
store. rgw-go therefore accepts radosgw's argv: when invoked as `radosgw`,
`cli` rewrites argv into `serve -- <args>` so cobra stays the single entry
point, and `cephconf` does what ceph's `global_init` does. It consumes the
early arguments ceph handles before config, `-c`, `--cluster`, `-i`, `-n`,
`--no-config-file` and `-v`, creates the connection under that name, reads
the config file and `CEPH_ARGS`, and hands everything else to librados,
which already handles `-f`, `-d`, `--keyring`, `--mon-host`,
`--no-mon-config`, `--setuser` and every `rgw_*` option. Privileges are
dropped after the frontend binds, not before connecting: like radosgw,
`cephconf` defers the drop, connects to RADOS as the launching user, and
the frontend drops to `--setuser`/`--setgroup` only after binding, so it
can bind privileged ports. librados applies the mon config store during
connect, so options Rook set centrally are readable afterwards.

Ceph config is the single source for everything radosgw configures.
rgw-go's own flags cover only what has no ceph option: `--log-level`,
`--log-format`, `--metrics-addr`, `--rados-completions`, and viper's
`--config`. The frontend parses the beast spec keys `port`, `ssl_port`,
`endpoint`, `ssl_endpoint`, `ssl_certificate`, `ssl_private_key`,
`ssl_options`, `ssl_ciphers`, `tcp_nodelay`, `request_timeout_ms`,
`max_connection_backlog` and `max_header_size`; unknown keys log and are
ignored. `rgw_enable_apis` is honored; swift and swift_auth are ignored
with one log line. Logging is slog, JSON to stderr, as radosgw in Rook logs
to stderr.

## 6. Data path

One goroutine per request. The frontend stamps radosgw's transaction-id
format; the dispatcher selects the op; auth resolves the credential through
the metadata cache and, for chunked uploads, wraps the body in a reader
that verifies each chunk signature as bytes stream; the op runs radosgw's
lifecycle in order: load metadata, verify permission through op mask, ACL,
policy and quota, execute, complete, respond.

| Path | Round trips on the latency path | Parallelism |
|---|---|---|
| PUT within the 4 MiB head | index prepare with the reshard guard, then the guarded head write with data, manifest and attributes; the index complete is issued and not awaited | none |
| PUT beyond the head | 4 MiB tail stripes written while the body streams, then prepare, then head | tails in flight up to the put window, 16 MiB |
| GET within the head | one read op composing stat, xattrs and the first 4 MiB | none |
| GET beyond the head, or a range | the first op, then tail reads | up to 16 MiB in flight in 4 MiB requests, streamed in order |

These are radosgw's round trips and radosgw's default windows. The
fire-and-forget index complete follows radosgw's completion manager, with
retries by a worker and reconciliation of a leftover pending entry at the
next listing. A failed head write cancels the prepare. A lost race on the
guarded head write, an `ECANCELED`, `ENOENT` or `EEXIST`, cancels the index
entry and answers success when the request carried no precondition, as
radosgw's does. Copy shares tails through the refcount class under the new
tag with the NUL-terminated form radosgw writes, rewriting only the head,
and streams data only where placement, storage class, encryption or head
geometry force it. CompleteMultipart takes radosgw's named lock on the
multipart meta object. Listing performs radosgw's pending-entry
reconciliation.

## 7. Concurrency

- **Completions wake goroutines; three mechanisms are built and
  selectable** by `--rados-completions`: `sync`, stock go-ceph's blocking
  operate, the thread-per-operation baseline; `callback`, a C callback
  into Go that signals the waiter's channel, the default; and `pipe`, a C
  callback writing an 8-byte id to a pipe that one goroutine drains through
  the netpoller. librados runs callbacks on its single finisher thread, so
  they are serialized regardless. A seam-level microbenchmark measures
  submit-to-wake latency, completions per second and OS threads per mode.
- **Buffers are pinned, not copied**, with `runtime.Pinner` for the life
  of the operation. librados copies once in each direction inside the C
  API, which radosgw also pays.
- **Submission never blocks an OS thread.** librados's objecter throttle,
  1024 operations or 100 MiB, blocks the submitting thread inside C.
  rgw-go keeps its own in-flight limiter under those limits so waiting
  happens parked in Go, honors `rgw_max_concurrent_requests`, default
  1024, as a hard cap, and bounds body memory with a byte budget.
- **Cancellation.** A client disconnect cancels the request context and
  every wait selects on it. librados can only abandon an in-flight
  operation on the client side (`rados_aio_cancel`, not yet bound in the
  fork), and a write already sent to the OSD may still apply, so the
  completion owns the pinned buffers and never touches the response.
- **Metadata cache.** Decoded users, bucket entry points, bucket instances
  with attributes, and zone configuration, 25000 entries with radosgw's
  900 second expiry, watching the eight control-pool notify objects and
  sending the same notify on every metadata write rgw-go performs.
- **Workers**, each an errgroup goroutine stopped by the root context:
  phase 1 has the GC processor on its one-hour period with the `gc_process`
  locks, the usage log flush every 30 seconds or 1024 entries, the index
  completion retry worker, watch re-registration, and quota statistics;
  phase 2 adds lifecycle; phase 3 adds the notification deliverer and the
  persistent-queue expiry pass.

## 8. Compatibility and encoding

`denc` implements the wire encoding RGW and its classes use: little-endian
integers, length-prefixed strings and bufferlists, the standard containers,
time, and both struct framings, the standard one and the legacy
compat-length one that sixty decoders in scope use. No feature-dependent
encoding exists in this scope. On decode a struct whose compat byte is
above our version is refused and trailing unknown fields are skipped; on
encode the length is patched when the frame closes.

The version rule, by layer:

- Driver-level persistent types, the ones the gateway reads and writes as
  object data, xattrs or omap values: decode every struct version the C++
  decoders accept, since never-rewritten metadata sits at its original
  encoding; encode at the version the cluster's own radosgw release
  writes, detected once from the required OSD release and overridable by
  flag. Coexistence is byte-exact by construction.
- Class requests: encode at the same cluster-release version, so request
  shapes added in Tentacle are reachable on Tentacle OSDs.
- Class replies: decode from the Squid version upward, because the OSD's
  class re-encodes stored records into replies at its own version. The
  `bi_*` entries that return raw stored bytes are out of scope.

Consequence: a Tentacle gateway-side feature is available on a Squid
cluster when its persistent form is a separate attribute, as checksums
are, and unavailable when it is a field of a struct the cluster's radosgw
would re-encode without it, as bucket logging is.

Class packages call only methods Squid's classes register; the four index
methods that exist only in main are never used. Methods that modify and
return, above all the 2pc queue reserve, run through a read op with the
return-vector flag, a fork item that gates persistent notifications. The
driver reproduces radosgw's placement of everything, pools and namespaces
from the zone parameters in the root pool including Rook's shared-pool
namespaces, metadata names in every namespace, index shard names across
layout generations, head, tail and multipart names with radosgw's escaping,
attribute names, and the control, log, GC, usage, lifecycle and
notification objects. The spec states these as constraints proven by the
oracle, not as string formats. The coexistence obligations that hold
regardless of any exclusion, cache invalidation, GC formats, the reshard
guard, OLH epochs, compression codecs, user statistics, lock names and
radosgw's signature quirks, are in `docs/exclusions.md` and are
requirements of this design.

Coexistence extends to radosgw's defects. When upstream Ceph behaves
wrongly, or surprisingly, in a way a correct client meets, rgw-go matches
it on the releases that carry it, works around it, or states why it is
unaffected. The choice is recorded per defect in `docs/ceph-upstream-bugs.md`,
together with the affected releases, evidence at a named release tag, and
the defect's upstream status. The registry is maintained for the life of
the project: an entry is added whenever work meets a defect and updated when
upstream fixes it. It distinguishes defects from intended behaviour that
surprises a reimplementation, and leaves go-ceph's defects to
`docs/cgo-limitations.md`.

## 9. Phases and gates

Every phase gate includes writing with one gateway and reading with the
other on a shared zone, s3-tests parity with radosgw for the phase's ops,
and a benchmark run.

**Phase 0, foundations.** Repository and CI. `denc`. `meta` decoding every
version and encoding per the rule, against corpus goldens. Class packages
for rgw, user, version, refcount and rgw_gc. The seam, its go-ceph
implementation in all three completion modes, and the fork bindings.
Disposable Squid and Tentacle clusters with a radosgw and a population
script. Gate: every metadata object a populated radosgw wrote decodes and
re-encodes byte-identically.

**Phase 1, data path, auth and core admin; first benchmark.** SigV4 header,
query and presigned with chunked and trailer payloads, SigV2, anonymous;
ACL, policy, quota and tenant authorization; ListBuckets, Create, Delete,
Head and GetLocation for buckets, ListObjects v1 and v2; Put, Get with
ranges and conditionals, Head, Delete, Copy, DeleteObjects,
GetObjectAttributes; the seven multipart ops; ACL, policy and tagging
subresources; read compatibility for every placement, storage class,
compression codec and manifest layout; the coexistence machinery, cache
invalidation both ways, GC enqueue and worker, the reshard protocol, usage
log writes; TLS through the beast spec. Admin: user with keys, subusers,
caps and quota; bucket except Sync_Bucket; usage; metadata over user,
bucket and bucket.instance; info; account; config; ratelimit settings
stored; the read-only realm and period getters. Gates: s3-tests parity, the
go-ceph rgw/admin suite, the seam microbenchmark answering the cgo
question, the pipeline baseline, the first gateway comparison, and the
Rook suite as soon as the derived image serves a store.

**Phase 2, versioning, lifecycle and bucket configuration.** Versioning
with the OLH protocol, ListObjectVersions, object lock with retention and
legal hold, lifecycle with the worker for expiration, noncurrent
expiration, multipart abort and in-cluster storage-class transition, CORS
with preflight, static website, request payment, SSE-C and the encryption
configuration ops behaving as an unconfigured radosgw, ownership controls,
public access block, policy status, POST object, checksums at Tentacle
level, compression and placement on the write path, MFA, the `?layout`
extension. Admin adds the otp metadata section.

**Phase 3, remaining services and workers.** Bucket notifications in full
except Kafka and AMQP delivery: topic actions, the notification
subresource with its filter grammar, event generation, synchronous HTTP
and HTTPS delivery, persistent topics over the 2pc queue class with an
HTTP deliverer and the expiry pass, roughly four thousand lines, gated by
Rook's notification and Kafka tests. IAM with its full action set; STS
AssumeRole, AssumeRoleWithWebIdentity and GetSessionToken; S3 Control;
bucket logging, on Tentacle clusters per the encoding rule; the `?usage`
extension; rate-limit enforcement; ops log to a file in radosgw's format;
admin-socket counters in the form ceph-exporter scrapes, with librados's
own client socket kept out of the shared socket directory. Admin adds the
roles, user_policy, group and topic metadata sections.

## 10. Testing

| Layer | Needs | Covers | Runs |
|---|---|---|---|
| Unit specs, Ginkgo and Gomega | librados-dev, no cluster | `denc`, `meta`, class marshalling, auth, policy, ACL, the dispatcher, every op against counterfeiter fakes, the frontend in-process | `make test`, every PR |
| Corpus and vector goldens | nothing at run time | every encoding in scope: each corpus object decodes to the JSON ceph-dencoder dumps and re-encodes to the bytes ceph-dencoder produces; generated vectors for types the corpus lacks and for Squid-version encodings; nondeterministic types compare decoded values | `make test`, every PR |
| Integration specs, `//go:build integration` | a disposable cluster | the seam in all three modes, the driver's layout and protocols, the live oracle matrix against radosgw, the admin API through go-ceph's rgw/admin client, ceph/s3-tests | on demand and at gates |
| Rook end to end | kind, Rook, the derived image | the object suite, both TLS passes | nightly and at gates |

The corpus archives in the ceph checkout span 15.2 through 19.2. Goldens
are regenerated by a make target that runs ceph-dencoder from the Ceph
image; `go test` never needs it. s3-tests is comparative: the same suite
against radosgw and rgw-go on the same cluster with the same configuration,
and the gate is an equal pass and fail set for the phase's groups. The
disposable cluster is a one-worker Rook cluster on kind per release, which
rooket stands up from a released Rook with the release's Ceph image pinned,
each under its own name so that it collides with no other cluster on the
host. It is host-networked from its first bring-up, because the specs run
librados and S3 clients on the host and Rook cannot move a running cluster
onto host networking. Rook names the object store's realm, zonegroup, zone
and pools after the store, so the population derives them from the running
radosgw, and radosgw-admin always names the site, since without it
radosgw-admin creates and works in a zone the radosgw never serves. Rook's
operator writes the realm, zonegroup, zone and periods with the Ceph of its
own image, which can be a newer release than the cluster's, so the gate
holds such an object to the writing release's encoding and to what the
cluster's own ceph-dencoder writes back. The ambient cluster is never used.

## 11. Benchmarking

Three benchmarks answer three questions. The seam microbenchmark answers
the cgo question. A pipeline baseline, a no-op handler and a
one-round-trip handler under the real load, answers how much of a request
is ours. The gateway comparison, radosgw, rgw-go and rgw-rs one at a time
on the same disposable Rook cluster through the derived image under the
parity settings in `docs/exclusions.md`, answers the project's question:
small-object PUT and GET at rising concurrency for latency percentiles,
large-object throughput, and listing at scale.

The load generator is the jhoblitt/elbencho fork at a pinned commit of its
`s3-error-counts` branch, chosen because it is a native, distributed
storage benchmark whose fork adds per-phase S3 error and retry counts to
its CSV and JSON results, a request timeout option, and ready profiles for
S3 read bandwidth, read IOPS, read latency and write bandwidth. Every
gateway is driven by the same client build. Recorded per run: p50, p99 and
p999 latency, throughput, CPU time per request, OS threads, resident
memory, error and retry counts, and for rgw-go the GC pause profile and a
CPU profile with the cgo boundary attributed. Results and their exact
commands live under `docs/benchmarks/` with the cluster description.

## 12. Repository, CI and release

Public repository `jhoblitt/rgw-go`, license LGPL-2.1-or-later as rgw-rs,
created per the GitHub canon: `gh repo create --public`, the default-branch
ruleset, an empty root commit on `main`, and every file through a draft
`init` pull request. Module `github.com/jhoblitt/rgw-go`, `go 1.27`, the
Go canon's Makefile, CI, lint config, cobra root, version package and
CLAUDE.md block. go-ceph is consumed through a `replace` to the fork at a
pinned commit until the bindings are upstream, bumped by hand.

CI adds an install of librados-dev, Squid or newer, to the template's test,
lint and checks jobs; those jobs build and run unit specs and never connect
to a cluster, so the headers alone matter there. Two added workflows,
neither gating pull requests: an integration workflow, nightly and on
demand, that brings up the disposable Rook clusters on the runner with a
pinned rooket, one job per release, and runs the integration specs, the
oracle matrix and the s3-tests comparison; and a
Rook workflow, nightly and on demand, that builds the derived image and
runs the object suite on kind.

rgw-go supports running only against librados 19.2.6 or later on Squid, or
20.2.4 or later on Tentacle. This is a support policy: technically the
floor binds only when the key rgw-go presents is AES256KRB5, since those
are the first releases able to parse that cephx key type, and an older
librados fails to connect with one. The policy follows from where such keys
come from. Ceph's mkfs default at those releases allows only AES256KRB5, so
a freshly created cluster's keys have that type. Rook allows both AES and
AES256KRB5, but makes AES256KRB5 the preferred type, which new daemon keys,
the RGW's included, take unless the cluster's `cephx.daemon.keyType`
overrides it. The key types alone would not require a cluster floor, since
a cluster upgraded from an older release keeps its AES keys working; the
cluster floor in section 3 is the same releases, for OSD-side behavior.
Under Rook the derived image is every daemon's image, so rgw-go's librados
is always the cluster's own release, and a supported derived image implies
a cluster at the same floor. The integration workflow installs librados at
the floor; the headers used for building are unaffected.

Release keeps goreleaser for tags, archives, checksums, SBOM and keyless
signing, but the build is cgo in a container stage on the same base as the
target Ceph image, and the artifact is a derived Ceph image per supported
release with rgw-go as `/usr/bin/radosgw` and the original kept beside it,
published to ghcr.io under this repository, tagged with the release and the
base. `.git` is present at build time so the version stamp survives, and
the release job asserts the stamp equals the tag.

The repository CLAUDE.md carries: cgo and librados-dev, the go-ceph replace,
never the ambient cluster, Opus workers for implementation, and the
obligation to notify the rgw-rs session when `docs/exclusions.md` changes.

## 13. go-ceph fork work

Thin cgo wrappers, each with a test in go-ceph's style, offered upstream
after they prove out: the async completion object and asynchronous operate
for read and write ops, with the three completion mechanisms behind an
option; read-op steps `getxattrs`, `stat2`, `cmpxattr` and omap key
listing; write-op steps `cmpxattr`, `rmxattr`, `append`, `zero`,
`truncate`, `set_flags` and omap compare; and an exec path that returns
data from a modifying class method through the return-vector flag.

## 14. Risks and items to verify at implementation

- cgo pointer lifetime across asynchronous operations is the class of bug
  that is silent until it corrupts; the seam's tests run under the race
  detector and with cgo checks enabled.
- The return-vector exec binding gates persistent notifications.
- The base OS of each Ceph release image, which fixes the builder stage.
- The librados version the CI distribution ships. Realized: Ubuntu noble's
  librados is below the supported floor in section 12, so the integration
  workflow takes librados from download.ceph.com, while the jobs that never
  connect keep noble's headers.
- The librados floor in section 12 couples rgw-go to the cephx key types
  Ceph issues. Only a pure-Go RADOS client that implements the AES256KRB5
  cipher (RFC 8009 AES256-CTS-HMAC-SHA384-192) removes the coupling. Under
  Rook the coupling costs nothing extra: the derived image is every
  daemon's image, because Rook has no per-daemon override for the RGW, so
  rgw-go's librados always matches the cluster's release and meets the
  floor whenever the cluster can issue such keys. It binds a gateway run
  outside that image, such as a host process against a fresh cluster.
- The fixed 4 KiB reservation radosgw makes per persistent event, with a
  re-reservation at commit for larger events, reported by rgw-rs and to be
  read from the code when phase 3 begins.
- The persistent-queue hazards recorded in `docs/exclusions.md`, which
  rgw-go can avoid triggering but not fix: a Squid radosgw's queue registry
  listing never pages, and reservation drift accrued under a release below
  the floor survives the upgrade.
