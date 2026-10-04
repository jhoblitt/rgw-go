# Phase 1 Unit T: Gates, Image and Benchmarks Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the machinery that judges phase 1's other eight units: the seam microbenchmark that answers the cgo question, the s3-tests and go-ceph rgw/admin harnesses with radosgw's own baseline recorded green before rgw-go is compared, the derived Ceph image with rgw-go as `/usr/bin/radosgw` and its goreleaser release, the nightly Rook workflow running the phase-1 subset of the object suite, the gateway comparison through the elbencho fork, and `docs/benchmarks/`.

**Architecture:** Every harness runs against the disposable rooket clusters (`hack/rooket/`) and first against the radosgw those clusters already run, so a later rgw-go failure is unambiguous. The microbenchmark is `testing.B` code over the phase-0 seam in `test/bench/seam`, swept by a shell driver one process per completion mode with `rados bench` from the cluster's own image as the librados-native floor. The s3-tests and admin harnesses are shell around pinned upstream checkouts, with one Go tool, `hack/parity`, turning their junit and `go test -json` output into committed baselines and pass-or-fail comparisons. The image is built as cgo inside a builder stage derived from the target Ceph image and assembled onto that image; goreleaser drives the same build through a `tool:` wrapper. The Rook workflow checks Rook out at a pinned commit, patches the suite down to the phase-1 subset and rewrites the installer's image constant.

**Tech Stack:** Go 1.27 and cgo, Ginkgo v2 and Gomega for specs, `testing.B` for the benchmark, bash, python3 venv for s3-tests, podman or docker, goreleaser v2, GitHub Actions, kind through rooket, the jhoblitt/elbencho fork, `rados bench` from `quay.io/ceph/ceph`.

**Spec:** `docs/superpowers/specs/2026-09-25-rgw-go-design.md` (§2 priorities, §6 op shapes, §9 gates, §11 benchmarking, §12 image and workflows), with `docs/exclusions.md` for the parity settings and `docs/superpowers/plans/2026-09-29-phase-1/00-index.md` for unit T's scope ("The nine units") and decisions D1, D2, D10, D11 ("Spec ambiguities and decisions D1-D11").

## Global Constraints

- Module `github.com/jhoblitt/rgw-go`, `go 1.27` minor only; one binary, `cmd/rgw-go`, with `serve` and `version`; helper programs under `hack/` are `main` packages run with `go run`, as `.github/tools/breaking-footer/main.go` is.
- Tests are Ginkgo v2 and Gomega, one `<pkg>_suite_test.go` per package with `RandomizeAllSpecs` and `FailOnPending`; cluster-backed specs carry `//go:build integration` and `Label("integration")`. Benchmarks are `testing.B` functions in `_test.go` files behind the same build tag; they are not tests and carry no Gomega.
- Every build, test and lint passes the `ceph_preview` tag (Makefile `GO_TAGS`); `make check` is green before any task is called done.
- Never the ambient kubectl or Ceph cluster. Every cluster-backed step runs against `make cluster-up RELEASE=squid|tentacle` (rooket, `hack/rooket/`), reaching it only through `ROOKET_NAME=rgw-go-<release> rooket kubectl` and `hack/rooket/out/<release>/ceph.conf`.
- Releases: Squid is `quay.io/ceph/ceph:v19.2.6`, Tentacle `quay.io/ceph/ceph:v20.2.4` (`hack/rooket/<release>/values/rook-ceph-cluster.yaml`, the one home of a release's Ceph version; `pinned_tag` in `hack/rooket/lib.sh` reads it). Squid first, then Tentacle in the same gate (D11).
- radosgw's own baseline is recorded green, twice, before anything is compared with rgw-go; a comparison refuses to run against a baseline recorded for a different s3-tests commit, Ceph version, release or set of deselect lists.
- The phase-0 gate (`test/gate/phase0_test.go:845,982`) asserts the zone holds exactly the manifest's users and buckets, so every harness that creates users runs after `make gate`, never before; the integration workflow keeps that order.
- Parity settings for every timed gateway run, from `docs/exclusions.md`: notifications off, MFA off, SSE off, `rgw_d3n_l1_local_datacache_enabled=false`, `rgw_dynamic_resharding=false`, the same cache setting, the same shard count and stripe and chunk sizes, and the sync, GC and LC workers off (`rgw_run_sync_thread=false`, `rgw_enable_gc_threads=false`, `rgw_enable_lc_threads=false`). Both gateways read them from the mon config store under their own entity names.
- Load generator: the jhoblitt/elbencho fork, branch `s3-error-counts`, pinned at commit `0637a922cbb71d56625f553750dbfbf12e5f0167` (2026-09-22); one client build drives every gateway.
- Every GitHub Action `uses:` is pinned to a 40-hex SHA with a `# vX.Y.Z` comment (`GITHUB_TOKEN=$(gh auth token) pinact run` after editing); `actionlint` with shellcheck on `PATH` before committing a workflow; top-level `permissions: contents: read`; `persist-credentials: false` on every checkout; no `${{ }}` inside `run:`, event and matrix values pass through `env:`.
- Workflows in unit T do not gate pull requests: `integration.yml` and `rook.yml` run nightly and on `workflow_dispatch`.
- The derived image keeps the original radosgw beside rgw-go: `/usr/bin/radosgw.ceph` is Ceph's, `/usr/bin/rgw-go` is ours, `/usr/bin/radosgw` is a symlink to `rgw-go` so `argv[0]` is `radosgw` under Rook. Phase 1 publishes `linux/amd64` only.
- Documentation of a benchmark result carries the exact commands, the cluster description (release, Ceph version, rooket commit, host CPU and memory, kernel), the gateway's build stamp and the date; a claim about performance without its run directory under `docs/benchmarks/` is not made.
- Commits are Conventional Commits ending with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>` and no `Claude-Session` trailer; every PR opens as a draft assigned to the author and merges on green with a merge commit under the standing authorization. The machine facts in the repository `CLAUDE.md` apply: local cgo builds need `CGO_CFLAGS=-I/home/jhoblitt/github/ceph/src/include` and `CGO_LDFLAGS=-L$HOME/.local/lib/rgw-go`; module downloads need the sandbox disabled.

## Review Focus

1. **A flaky s3-tests outcome read as a parity failure.** A test that passes on one radosgw run and fails on the next would make rgw-go red for nothing. Pinned in Task 5: the baseline is two radosgw runs, tests whose outcome differs land in `unstable` and the comparison ignores them; `hack/parity` has a spec for it.
2. **A microbenchmark cell that never finishes.** Sync mode at 512 in flight parks 512 OS threads, and a large shape past librados's objecter byte throttle blocks submitters inside C in every mode. Pinned in Task 1: a per-cell deadline fails the cell instead of hanging the sweep, and a cell whose `conc*size` exceeds the byte budget is skipped by default and run once on purpose to record the thread growth the in-flight limiter (decision D8) exists to prevent.
3. **A Rook run that passed against stock Ceph.** If the installer constant's spelling changes, a rewrite that matches nothing leaves the suite pulling `quay.io/ceph/ceph:v19` and passing vacuously. Pinned in Task 10: `set-image.sh` fails when the constant is not found in the expected form, and the workflow asserts after the run that the CephCluster's image is the derived one.
4. **A baseline from another world.** Comparing rgw-go against a baseline recorded on a different s3-tests commit, Ceph version, release or set of deselect lists would report differences that are drift, not bugs, or, after a list shrinks, silently skip the tests it gave back. Pinned in Task 5: `parity diff` refuses when the metadata differs, with a spec; Task 4's `run.sh` stops on a list line that names no collected test or would deselect one it does not name.
5. **A derived image that breaks the tools sharing it.** Under Rook the derived image is every daemon's and the toolbox's image, so a rename that clobbers `radosgw-admin`, `ceph` or `rados` breaks the cluster before rgw-go is ever reached. Pinned in Task 8: the image test runs `ceph --version`, `radosgw-admin --version`, `rados --version` and `radosgw.ceph --version` and requires the base image's version string from each.

---

## Decisions this plan owns

The index's decisions D1, D2, D10 and D11 (`00-index.md`, "Spec ambiguities and decisions D1-D11") fall to unit T; this plan adopts them and adds the choices below. Nothing here reopens the design.

- **D1, the phase-1 Rook gate** is the subset `zonepools`, `bucket/{owner,policy,quota,rw}`, `user/{caps,keys,opmask,placement,storageclass}`, `cosi` (its non-TLS pass; it skips itself under TLS) and `dependents`, run from a Rook checkout at a pinned commit with `bucket/lifecycle` (phase 2), `topic/kafka` and `notification` (phase 3) removed by `hack/rook/phase1.patch`. Verified at rook `dc7829268`: `runObjectE2ETest` ([`tests/integration/ceph_object_test.go:103-138`](https://github.com/rook/rook/blob/v1.20.7/tests/integration/ceph_object_test.go#L103-L138)) calls them unconditionally. The full suite is the phase-3 gate.
- **D2, image injection:** `hack/rook/set-image.sh` rewrites `squidTestImage` or `tentacleTestImage` in `tests/framework/installer/ceph_installer.go` (`"quay.io/ceph/ceph:v19"` and `"quay.io/ceph/ceph:v20"` at `dc7829268`, lines 46-47), which `ReturnCephVersion` selects by `CEPH_SUITE_VERSION=squid|tentacle` ([`ceph_installer.go:96-115`](https://github.com/rook/rook/blob/v1.20.7/tests/framework/installer/ceph_installer.go#L96-L115)). The Rook-side override stays deferred.
- **D10, the s3-tests parity set:** files `s3tests/functional/test_s3.py` and `s3tests/functional/test_headers.py` (the auth groups unit A's gate names live in the latter), at commit `5522d1c351f75bc00ae0f64f742f3f095f5939d9` (master, 2026-05-27; the boto3 tests moved from `s3tests_boto3/` to `s3tests/functional/`, the README lags). Deselected markers, one `-m` expression: `lifecycle lifecycle_expiration lifecycle_transition cloud_transition cloud_restore target_by_bucket versioning delete_marker object_lock object_ownership encryption bucket_encryption sse_s3 s3select s3website s3website_routing_rules s3website_redirect_location bucket_logging bucket_logging_cleanup fails_without_logging_rollover checksum sns appendobject iam_account iam_cross_account iam_tenant iam_user iam_role user_policy role_policy group group_policy session_policy abac_test test_of_sts webidentity_test token_claims_trust_policy_test token_principal_tag_role_policy_test token_request_tag_trust_policy_test token_resource_tags_test token_role_tags_test token_tag_keys_test s3control`. Deselected too, by pytest node id and on both gateways: the tests `hack/s3tests/deselect-phase2.txt` and `hack/s3tests/deselect-phase3.txt` list (Task 4), which call a feature §9 places in phase 2 or 3 and carry none of those markers. The parity files carry no `versioning` or `object_lock` mark at the pinned commit, so those two markers remove nothing there; the lists name the tests one by one, each with the calls that need the feature, every one checked against its body at the pinned commit: bucket versioning 48, object lock 39, POST object 35, CORS with the OPTIONS preflight 16, public access block 10, policy status 6 and Tentacle-level client checksums 1 (phase 2, 155 lines), and the `?usage` extension 1 (phase 3). A list shrinks as its phase lands: the PR that lands a feature deletes its lines and re-records the radosgw baselines, which carry the lists' digest so that `parity diff` refuses a stale one (Task 5). The known-difference lists (Task 12) hold decided differences only; a test out of the phase's reach is deselected, never listed as a difference. At the pinned commit the two files hold 808 tests: the markers remove 259, the lists 156, and 393 run. Kept: `fails_on_rgw` (both gateways must fail alike; Ceph's own QA excludes it, [`qa/tasks/s3tests.py:531`](https://github.com/ceph/ceph/blob/v19.2.6/qa/tasks/s3tests.py#L531)), `fails_on_aws`, `fails_on_dbstore`, `fails_on_dho`, `fails_with_subdomain`, `auth_aws2`, `auth_aws4`, `auth_common`, `bucket_policy`, `copy`, `tagging`, `list_objects_v2`, `storage_class`, and `conditional_write`. `conditional_write` (25 tests: `If-Match` and `If-None-Match` on PUT, which radosgw evaluates in `_do_write_meta`'s `check_preconditions`, and the conditional DELETE and DeleteObjects forms of W-D12) is kept because §9 lists Put without qualification; 17 of its tests enable bucket versioning and are on the phase 2 list, so 8 run, and unit W is told in the report that the parity set holds them.
- **D11:** Squid first on every harness, Tentacle in the same gate.
- **The cgo question's measurement and threshold.** The microbenchmark reports, per completion mode, shape and concurrency: throughput, submit-to-wake latency p50, p99 and p999, CPU time per operation, and OS threads idle and peak. Its floor is `rados bench` from the cluster's own image, on the same host and pool, at the same object size and concurrency, for the four shapes that mirror it exactly. cgo is **not** a phase-1 bottleneck when, on each release, one of the sync, callback and pipe modes meets both criteria, at 64 and 256 in flight: (a) throughput of `read4k`, `write4k`, `read4m` and `write4m` is at least 0.8 times `rados bench`'s at the same size and concurrency; and (b) their mean latency is at most 1.25 times `rados bench`'s mean. The aims are the lowest latency, the highest throughput and the lowest CPU use, so beside the criteria the report measures, for every mode and against no threshold: CPU per operation, at `read4k` at 256 in flight (among the modes that meet both criteria, the one using the least is preferred; `rados bench` records no CPU, so a floor that records it is the next benchmark's to add); the boundary cost, the part of that CPU a pure-Go client would no longer spend: the submit crossing (`runtime.cgocall`'s Go side, the `_Cfunc_*` stubs, the pinner and cgo's pointer checks) plus the delivery (the `runtime.cgocallback*` frames a callback arrives through, or the pipe goroutine and the read it waits in), counting neither the C those calls run on the Go thread nor the Go completion handler, which a pure-Go client runs too; thread growth, peak minus idle OS threads, which matters only where it costs throughput, latency or CPU; and the share of samples with any cgo frame on the stack, which counts librados's own C work as a cost of the boundary and rises as the Go side gets more efficient. Failing a criterion is the finding, recorded with its margin. (Amended 2026-10-02: every mode is judged, and thread growth and the boundary cost are measured rather than gates.)
- **Benchmark form:** `testing.B` in `test/bench/seam`, one process per mode from the shell driver, because the Go runtime never destroys an OS thread and the fork's completion notifier is process-wide (`docs/cgo-limitations.md`). Results are one JSON line per cell, rendered by `hack/bench/report`.
- **The image build** runs `go build` inside `hack/image/Containerfile.builder`, a stage from the target Ceph image (`quay.io/centos/centos:stream9` base at v19.2.6, `container/Containerfile` in ceph.git) with `librados-devel` at the image's exact Ceph version from `download.ceph.com/rpm-<version>/el9`, `git` for the version stamp and the Go toolchain copied from the official `golang` image; the final image is `hack/image/Containerfile` over the same base. goreleaser drives the identical build through `builds[].tool: hack/image/cgo-build.sh` (a field goreleaser v2 has; verified in `goreleaser jsonschema`) and assembles with `dockers[]`, replacing the phase-0 `kos` entry.
- **Rook workflow layout:** Rook is checked out at the workspace root and rgw-go under `rgw-go/`, because Rook's composite action `.github/workflows/integration-test-setup-cluster-resources` runs `tests/scripts/github-action-helper.sh` relative to the workspace, and reusing it verbatim at a pinned commit beats reimplementing its ten steps.
- **go-ceph rgw/admin suite** at tag `v0.39.0` (the version `go.mod` requires), with `hack/admin/endpoint.patch` replacing `SetupConnection`'s hard-coded `http://test_ceph_a` ([`rgw/admin/radosgw_test.go:134-143`](https://github.com/jhoblitt/go-ceph/blob/cbf97f85fcf5000ca4beca4a7fbf2ee746fa965d/rgw/admin/radosgw_test.go#L134-L143)) by an environment variable, and the fixture user `admin` with keys `AKIAIOSFODNN7EXAMPLE` / `wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY` and caps `buckets=*;users=*;usage=read;metadata=read;info=read;accounts=*` (go-ceph `testing/containers/micro-osd.sh:169,173,177`), plus the three usage options its `usage_test.go` needs (`rgw_enable_usage_log=true`, `rgw_usage_log_tick_interval=1`, `rgw_usage_log_flush_threshold=1`, [`micro-osd.sh:94-96`](https://github.com/jhoblitt/go-ceph/blob/cbf97f85fcf5000ca4beca4a7fbf2ee746fa965d/testing/containers/micro-osd.sh#L94-L96)).

## File structure

```
test/bench/seam/doc.go                      package doc
test/bench/seam/shapes.go                   the seven op shapes over the seam (radosgw's compositions)
test/bench/seam/metrics.go                  OS threads, CPU time, latency percentiles, the peak sampler
test/bench/seam/seam_suite_test.go
test/bench/seam/shapes_test.go              hermetic: each shape builds the steps radosgw builds
test/bench/seam/metrics_test.go             hermetic: percentiles and the sampler
test/bench/seam/seam_bench_test.go          //go:build integration: BenchmarkSeam, flags, JSON results
hack/bench/seam.sh                          the sweep: one process per mode, rados bench floor, results dir
hack/bench/report/main.go                   renders seam and gateway results into markdown tables
hack/bench/report/report_suite_test.go, main_test.go
hack/bench/elbencho-build.sh                static elbencho from the pinned fork commit
hack/bench/sample.sh                        CPU, threads and RSS sampler for a pid or the rgw pod
hack/bench/gateway.sh                       the gateway comparison profiles
hack/parity/main.go                         record and diff: junit and go test -json into baseline JSON
hack/parity/parity_suite_test.go, main_test.go
hack/s3tests/run.sh                         pinned s3-tests, venv, users, conf, marker set, deselect lists, junit
hack/s3tests/deselect-phase2.txt, deselect-phase3.txt   out-of-phase tests no marker removes, by node id
hack/admin/run.sh                           pinned go-ceph, endpoint patch, fixture user, go test -json
hack/admin/endpoint.patch
test/s3tests/baseline/squid.json, tentacle.json
test/admin/baseline/squid.json, tentacle.json
hack/image/Containerfile.builder            Ceph image + librados-devel + git + Go
hack/image/Containerfile                    Ceph image + rgw-go as radosgw
hack/image/cgo-build.sh                     go build inside the builder; goreleaser's tool
hack/image/build.sh                         builder, binary, image for one release
.dockerignore
hack/rook/phase1.patch                      removes lifecycle, kafka topic and notification from the suite
hack/rook/set-image.sh                      rewrites the installer's image constant (D2)
hack/rooket/rgw-go-up.sh, rgw-go-down.sh    rgw-go on the host against a rooket cluster
hack/rooket/populate.sh                     modify: compressed objects on four storage classes
.github/workflows/rook.yml                  nightly phase-1 subset of TestCephObjectSuite on kind
.github/workflows/integration.yml           modify: baselines and the quick microbenchmark after the gate
.github/workflows/release.yml               modify: librados for the stamp check
.github/dependabot.yml                      modify: docker ecosystem for hack/image
.goreleaser.yaml                            modify: cgo builds through the tool wrapper, dockers per release
Makefile                                    modify: bench-seam, s3tests, admin-suite, parity-check, image, rgw-go-up, rgw-go-down, bench-gateway
.gitignore                                  modify: the harnesses' out/ directories
docs/cgo-limitations.md                     modify: the measured answer
docs/benchmarks/README.md                   index; one directory per run
README.md                                   modify: development section
```

## Task index

| # | Task | Wave | Depends on |
|---|---|---|---|
| 1 | Seam microbenchmark package | 0 | phase 0 |
| 2 | Microbenchmark sweep, `rados bench` floor, preliminary cgo answer | 0 | 1 (cluster) |
| 3 | Population: compressed objects on four storage classes | 0 | phase 0 (cluster) |
| 4 | s3-tests harness | 0 | phase 0 (cluster) |
| 5 | `hack/parity` and the s3-tests radosgw baselines | 0 | 4 (cluster) |
| 6 | go-ceph rgw/admin suite runner and baselines | 0 | 5 (cluster) |
| 7 | Integration workflow: baselines and the quick microbenchmark nightly | 0 | 2, 5, 6 |
| 8 | Derived image build | 0 for the mechanics, 2 to serve | none; G's binary to serve a store |
| 9 | goreleaser and the release workflow | 2 | 8 |
| 10 | Rook nightly workflow on the phase-1 subset | 2 | 8; green needs G, A, Z, M, R, W, N |
| 11 | rgw-go on the rooket cluster | 5 | G, M, R, W |
| 12 | s3-tests parity against rgw-go | 5 | 5, 11, A, Z, P |
| 13 | Admin suite against rgw-go | 5 | 6, 11, N |
| 14 | elbencho build and the gateway comparison | 5 | 11 |
| 15 | The cgo answer and `docs/benchmarks/` | 5 | 2, 14 |

Tasks 1 to 8 need no gateway and run in wave 0 in parallel with unit G; Tasks 3, 4 and 6 are independent of each other. Tasks 9 and 10 are authored in wave 2 as soon as G's binary starts under Rook's argv; Task 10's first green run is the wave-5 completion of the Rook gate. Tasks 11 to 15 need the data path.

---

### Task 1: Seam microbenchmark package

**Files:**
- Create: `test/bench/seam/doc.go`, `test/bench/seam/shapes.go`, `test/bench/seam/metrics.go`, `test/bench/seam/seam_suite_test.go`, `test/bench/seam/shapes_test.go`, `test/bench/seam/metrics_test.go`, `test/bench/seam/seam_bench_test.go`

**Interfaces:**
- Consumes: `radosclient.Pool`, `radosclient.NewReadOp`, `NewWriteOp`, `ReadOp.Stat/GetXattrs/ReadInto`, `WriteOp.Create/SetMtime/WriteFull/SetAllocHint/SetXattr/AssertExists`, `Pool.ListObjects`, `Pool.ID` (phase 0 seam); `goceph.Connect`, `goceph.Config{ConfigFile, Mode}`, `goceph.Mode{Sync,Callback,Pipe}`, `goceph.Release(ctx, cluster)`; `rgw.BucketInitIndex`, `rgw.GuardBucketResharding(op, r)`, `rgw.BucketPrepareOp(op, rgw.PrepareOp{Op, Key, Tag, Locator}, r)`, `rgw.BucketCompleteOp(op, rgw.CompleteOp{Op, Key, Locator, Ver, Meta, Tag}, r)`, `rgw.ObjStorePGVer(op, attr, r)`, `rgw.OpAdd`, `rgw.CategoryMain`, `rgw.ObjKey{Name}`, `rgw.EntryVer{Pool, Epoch}`, `rgw.DirEntryMeta{Category, Size, Mtime, ETag, Owner, OwnerDisplayName, ContentType, AccountedSize}` (`internal/cls/rgw`); `cephtest.ConfEnv`, `cephtest.TestPool`.
- Produces:

```go
package seam

// Shape is one operation composition the benchmark issues through the seam.
// Prepare runs once per cell and may be called again for a recalibrated cell,
// so it is idempotent; Run issues iteration i for worker w; Cleanup removes
// what Run created and is idempotent too.
type Shape interface {
	Name() string
	// Size is the bytes one Run moves, for the byte budget and the floor lookup.
	Size() int
	// Ops is how many RADOS operations one Run issues (1, or 2 for the index shape).
	Ops() int
	Prepare(ctx context.Context, p radosclient.Pool, r denc.Release, workers int) error
	Run(ctx context.Context, p radosclient.Pool, w, i int) error
	Cleanup(ctx context.Context, p radosclient.Pool) error
}

// Shapes returns every shape in sweep order: read4k, write4k, read4m,
// write4m (the four rados bench mirrors), headread4k, headwrite4k, indexrtt.
func Shapes() []Shape

// ByName returns the named shapes, or an error naming the unknown one.
func ByName(names []string) ([]Shape, error)

// OSThreads reads this process's thread count from /proc/self/status.
func OSThreads() (int, error)

// CPUTime is the process's user plus system CPU time from getrusage.
func CPUTime() (time.Duration, error)

// Percentiles are latency quantiles of one cell.
type Percentiles struct{ P50, P99, P999, Mean time.Duration }

// Summarize sorts a copy of d and returns its percentiles; it panics on an empty slice.
func Summarize(d []time.Duration) Percentiles

// Sampler polls OSThreads on an interval and keeps the peak.
type Sampler struct{ /* stop chan; peak atomic.Int64; done chan */ }

func NewSampler(interval time.Duration) *Sampler
func (s *Sampler) Stop() (peak int)
```

The shapes are radosgw's compositions, verified in `src/rgw/driver/rados/rgw_rados.cc` at ceph `7ed73efc1be`:

| Shape | Composition | Source |
|---|---|---|
| `read4k`, `read4m` | `ReadOp{ReadInto(0, buf)}` on `src-<w>` of that size, buffer reused per worker | the `rados bench rand` mirror |
| `write4k`, `write4m` | `WriteOp{WriteFull(payload)}` on a unique oid `w-<run>-<w>-<i>` | the `rados bench write` mirror |
| `headread4k` | `ReadOp{version.Read(op), GetXattrs(), Stat(), ReadInto(0, 4 MiB buffer)}` on `head-<w>`, a 4 KiB object with seven xattrs | `raw_obj_stat` with `first_chunk` (v19.2.6 [`rgw_rados.cc:8843-8853`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8843-L8853); v20.2.4 [`:9788-9797`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9788-L9797)): `objv_tracker->prepare_op_for_read` composes `cls_version_read` first ([`:158-166`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L158-L166)), then `getxattrs`, `stat2`, `read`; R Task 3's `readHead` sends the same four steps; GET within the head |
| `headwrite4k` | `WriteOp{Create(true), SetXattr(user.rgw.idtag, tag), SetXattr(user.rgw.tail_tag, tag), SetMtime(now), WriteFull(4 KiB), SetXattr(user.rgw.manifest, 180 bytes), SetXattr(user.rgw.acl, 200 bytes), SetXattr(user.rgw.content_type, "application/octet-stream\x00"), SetXattr(user.rgw.etag, 32 hex), rgw.ObjStorePGVer(op, "user.rgw.pg_ver", r), SetXattr(user.rgw.source_zone, 4 bytes)}` on a unique oid, `tag` being `"_"` + 31 alphanumerics + NUL (`append_rand_alpha(…, 32)`, [rgw_common.h:1621-1627](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.h#L1621-L1627) at v19.2.6; the write tag W Task 4's `randTag` makes) | `prepare_atomic_modification` first (v19.2.6 [`rgw_rados.cc:6484-6575`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6484-L6575); v20.2.4 `:7331-`): `create(true)` for a new object, then `setxattr(idtag)` and `setxattr(tail_tag)`; then `_do_write_meta` ([`:3124-3260`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L3124-L3260); v20.2.4 `:3234-`): `mtime2`, `write_full`, `set_alloc_hint2` ONLY when the old state was compressed ([`:3198-3203`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L3198-L3203)), so a fresh object sends none, the `rmxattr`s (none here), `setxattr(manifest)`, the attrs in `std::map` name order (acl, content_type, etag), `cls_rgw_obj_store_pg_ver`, `setxattr(source_zone)`. W Task 5 transcribes this order and W Task 13's op-composition spec asserts `PutObject` equals it step type by step type; PUT within the head for a new object |
| `indexrtt` | two ops on the worker's shard `.dir.bench-<run>.<w>`: `WriteOp{AssertExists(), GuardBucketResharding, BucketPrepareOp{Op: OpAdd, Key: {Name: key}, Tag: tag}}` then `WriteOp{AssertExists(), GuardBucketResharding, BucketCompleteOp{Op: OpAdd, Key, Tag, Ver: {Pool: p.ID(), Epoch: uint64(i)+1}, Meta: {Category: CategoryMain, Size: 4096, Mtime: now, ETag: 32 hex, Owner: "bench", OwnerDisplayName: "bench", ContentType: "application/octet-stream", AccountedSize: 4096}}}` with `key = "key-<run>-<w>-<i>"` | `cls_obj_prepare_op` (`:11199-11202`) and the completion manager's op ([`:1018-1019`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L1018-L1019)); the index half of a PUT |

`indexrtt.Prepare` creates each shard only when a `ReadOp{Stat()}` on it returns `radosclient.ErrNotFound`: `WriteOp{Create(true), rgw.BucketInitIndex(op)}`. Every shape works in the RADOS namespace the benchmark opens (`bench-<pid>` in `rgw-go-test`), so `Cleanup` is `ListObjects` on that pool handle followed by `WriteOp{Remove()}` per oid with 64 in flight. `run` is a package-level counter the benchmark increments per cell invocation, so recalibration rounds never collide on `Create(true)`.

- [ ] **Step 1: Write the failing hermetic specs**

`shapes_test.go`, package `seam_test`: build each shape's ops through a fake `radosclient.Pool` (counterfeiter is for interfaces declared at a consumer; here a twenty-line hand-written recorder in the test file implementing `radosclient.Pool` that records the `*ReadOp`/`*WriteOp` handed to `Read` and `Write` and returns `Stat` size 0 is enough, since the seam is a leaf whose fakes would live nowhere sensible) and assert the step composition:

```go
var _ = Describe("shapes", func() {
	DescribeTable("compose radosgw's steps",
		func(name string, want []string) {
			shapes, err := seam.ByName([]string{name})
			Expect(err).NotTo(HaveOccurred())
			rec := &recorder{}
			Expect(shapes[0].Prepare(context.Background(), rec, denc.Squid, 1)).To(Succeed())
			rec.reset()
			Expect(shapes[0].Run(context.Background(), rec, 0, 0)).To(Succeed())
			Expect(rec.stepNames()).To(Equal(want), "shape %s", name)
		},
		Entry("headread4k is the version read, xattrs, stat and the first chunk in one op", "headread4k",
			[]string{"*radosclient.ExecStep", "*radosclient.GetXattrsStep", "*radosclient.StatStep", "*radosclient.ReadStep"}),
		// prepare_atomic_modification's create and two tags, then _do_write_meta's data,
		// manifest, the three attrs in name order, pg_ver and source_zone; no alloc hint for
		// an uncompressed object; SetMtime is an op attribute, not a step.
		Entry("headwrite4k is create, two tags, data, four xattrs, one exec and the source zone", "headwrite4k",
			[]string{"*radosclient.CreateStep", "*radosclient.SetXattrStep", "*radosclient.SetXattrStep", "*radosclient.WriteFullStep",
				"*radosclient.SetXattrStep", "*radosclient.SetXattrStep", "*radosclient.SetXattrStep", "*radosclient.SetXattrStep",
				"*radosclient.ExecStep", "*radosclient.SetXattrStep"}),
		Entry("indexrtt is guard+prepare then guard+complete", "indexrtt",
			[]string{"*radosclient.AssertExistsStep", "*radosclient.ExecStep", "*radosclient.ExecStep",
				"*radosclient.AssertExistsStep", "*radosclient.ExecStep", "*radosclient.ExecStep"}),
		Entry("read4k is one read", "read4k", []string{"*radosclient.ReadStep"}),
	)
	It("rejects an unknown shape by name", func() {
		_, err := seam.ByName([]string{"read4k", "bogus"})
		Expect(err).To(MatchError(ContainSubstring("bogus")))
	})
	It("names the exec methods radosgw calls", func() {
		shapes, _ := seam.ByName([]string{"indexrtt"})
		rec := &recorder{}
		Expect(shapes[0].Prepare(context.Background(), rec, denc.Squid, 1)).To(Succeed())
		rec.reset()
		Expect(shapes[0].Run(context.Background(), rec, 0, 0)).To(Succeed())
		Expect(rec.execMethods()).To(Equal([]string{"guard_bucket_resharding", "bucket_prepare_op", "guard_bucket_resharding", "bucket_complete_op"}))
	})
})
```

`metrics_test.go`:

```go
var _ = Describe("Summarize", func() {
	It("returns the percentiles of the sorted durations without sorting the input", func() {
		d := []time.Duration{9, 1, 5, 3, 7}
		p := seam.Summarize(d)
		Expect(p.P50).To(Equal(time.Duration(5)))
		Expect(p.P99).To(Equal(time.Duration(9)))
		Expect(p.Mean).To(Equal(time.Duration(5)))
		Expect(d).To(Equal([]time.Duration{9, 1, 5, 3, 7}), "input left in place")
	})
})

var _ = Describe("OSThreads", func() {
	It("counts at least this goroutine's thread", func() {
		n, err := seam.OSThreads()
		Expect(err).NotTo(HaveOccurred())
		Expect(n).To(BeNumerically(">=", 1))
	})
})

var _ = Describe("Sampler", func() {
	It("reports a peak no lower than the count at start", func() {
		before, _ := seam.OSThreads()
		s := seam.NewSampler(time.Millisecond)
		time.Sleep(5 * time.Millisecond)
		Expect(s.Stop()).To(BeNumerically(">=", before))
	})
})
```

- [ ] **Step 2: Run to verify the suite fails to compile**

```sh
go test "-tags=$(make -s print-go-tags)" ./test/bench/seam/
```
Expected: FAIL, undefined `seam.ByName` and friends.

- [ ] **Step 3: Implement `shapes.go` and `metrics.go`**

`Summarize` copies, `slices.Sort`s, and indexes `n*q/100` clamped to `n-1`; `Mean` is the sum divided by `n`. `OSThreads` scans `/proc/self/status` for the `Threads:` line, as `goceph_integration_test.go` does. `CPUTime` is `syscall.Getrusage(syscall.RUSAGE_SELF, &ru)` and `time.Duration(ru.Utime.Nano() + ru.Stime.Nano())`. `NewSampler` starts one goroutine that calls `OSThreads` every interval and stores the maximum; `Stop` closes the channel, waits, and returns the peak. Payloads are deterministic (`rand.New(rand.NewPCG(1, uint64(w)))` from `math/rand/v2`). The tag is 32 alphanumerics plus `"\x00"`, as `prepare_atomic_modification` appends.

- [ ] **Step 4: Run the hermetic specs to pass; `make check`**

- [ ] **Step 5: Write the benchmark**

`seam_bench_test.go`, `//go:build integration`, package `seam_test`:

```go
var (
	modeFlag    = flag.String("mode", string(goceph.ModeCallback), "completion mode: sync, callback or pipe")
	concFlag    = flag.String("conc", "1,16,64,256,512", "operations in flight per cell, comma separated")
	shapesFlag  = flag.String("shapes", "", "shapes to run, comma separated; every shape when empty")
	resultsFlag = flag.String("results", "", "append one JSON line per cell to this file")
	budgetFlag  = flag.Int64("inflight-bytes", 96<<20, "skip a cell whose conc*size exceeds this; librados's objecter byte throttle is 100 MiB, and past it submission blocks an OS thread inside C in every mode. 0 disables the cap.")
	cellTimeout = flag.Duration("cell-timeout", 5*time.Minute, "fail a cell that has not finished by then")
)

// Cell is one JSON result line.
type Cell struct {
	Time        string  `json:"time"`
	Release     string  `json:"release"`
	Mode        string  `json:"mode"`
	Shape       string  `json:"shape"`
	Size        int     `json:"size"`
	Ops         int     `json:"ops_per_iter"`
	Conc        int     `json:"conc"`
	N           int     `json:"n"`
	NsPerOp     float64 `json:"ns_per_iter"`
	OpsPerSec   float64 `json:"ops_per_sec"`
	P50us       float64 `json:"p50_us"`
	P99us       float64 `json:"p99_us"`
	P999us      float64 `json:"p999_us"`
	MeanUs      float64 `json:"mean_us"`
	CPUusPerOp  float64 `json:"cpu_us_per_op"`
	ThreadsIdle int     `json:"threads_idle"`
	ThreadsPeak int     `json:"threads_peak"`
	GOMAXPROCS  int     `json:"gomaxprocs"`
	Errors      int     `json:"errors"`
}

func BenchmarkSeam(b *testing.B) {
	conf := os.Getenv(cephtest.ConfEnv)
	if conf == "" {
		b.Fatalf("%s is not set; point it at hack/rooket/out/<release>/ceph.conf", cephtest.ConfEnv)
	}
	ctx := context.Background()
	cluster, err := goceph.Connect(ctx, goceph.Config{ConfigFile: conf, Mode: goceph.Mode(*modeFlag)})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = cluster.Close() })
	release, err := goceph.Release(ctx, cluster)
	if err != nil {
		b.Fatal(err)
	}
	pool, err := cluster.Pool(ctx, cephtest.TestPool, fmt.Sprintf("bench-%d", os.Getpid()))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = pool.Close() })
	shapes := seam.Shapes()
	if *shapesFlag != "" {
		if shapes, err = seam.ByName(strings.Split(*shapesFlag, ",")); err != nil {
			b.Fatal(err)
		}
	}
	for _, sh := range shapes {
		for _, c := range parseConc(b, *concFlag) {
			if *budgetFlag > 0 && int64(c)*int64(sh.Size()) > *budgetFlag {
				b.Logf("skipping %s at %d: %d bytes in flight exceeds -inflight-bytes", sh.Name(), c, c*sh.Size())
				continue
			}
			b.Run(fmt.Sprintf("shape=%s/conc=%d", sh.Name(), c), func(b *testing.B) {
				runCell(b, ctx, pool, release, sh, c, filepath.Base(filepath.Dir(conf)))
			})
		}
	}
}
```

`runCell`: `Prepare`; register `Cleanup` once per invocation (`b.Cleanup`); read idle threads; `cpu0 := seam.CPUTime()`; start `seam.NewSampler(10*time.Millisecond)`; `lat := make([]time.Duration, b.N)`; `b.ResetTimer()`; `c` goroutines through `sync.WaitGroup.Go`, each pulling `i := next.Add(1)-1` until `i >= b.N`, timing `sh.Run(cctx, pool, w, i)` into `lat[i]` and counting errors, where `cctx` is `context.WithTimeout(ctx, *cellTimeout)` so a stuck cell fails instead of hanging the sweep (Review Focus 2); `wg.Wait()`; `b.StopTimer()`; `peak := sampler.Stop()`; `cpu1`; `p := seam.Summarize(lat[:b.N])`. Report `b.ReportMetric(float64(p.P99)/1e3, "p99-us")`, `"p50-us"`, `"cpu-us/op"` (`(cpu1-cpu0)/(b.N*sh.Ops())`), `"threads"` (peak), `"ops/s"` (`b.N*sh.Ops()` over `b.Elapsed()`), and append the `Cell` as one JSON line to `*resultsFlag` when set. A cell with any error fails: `b.Fatalf("%d of %d iterations failed; first: %v", ...)`.

- [ ] **Step 6: Run against the Squid cluster in every mode** (cluster-backed)

```sh
make cluster-up RELEASE=squid
for m in sync callback pipe; do
  RGW_GO_TEST_CEPH_CONF=$PWD/hack/rooket/out/squid/ceph.conf \
    go test "-tags=$(make -s print-go-tags),integration" -run '^$' -bench BenchmarkSeam -benchtime 3s \
    ./test/bench/seam/ -args -mode=$m -conc=1,64 -shapes=read4k,indexrtt -results=$TMPDIR/seam-$m.jsonl
done
jq -c '{mode,shape,conc,ops_per_sec,p99_us,threads_idle,threads_peak,errors}' $TMPDIR/seam-*.jsonl
```
Expected: every cell reports 0 errors; sync mode's `threads_peak` at conc 64 exceeds callback's and pipe's; the index shape succeeds on both ops (a `bucket_complete_op` failure here means the prepare's tag or key did not match, which the hermetic spec cannot catch).

- [ ] **Step 7: `make check`, commit**

`feat(bench): add the seam microbenchmark with radosgw's op shapes`

---

### Task 2: Microbenchmark sweep, `rados bench` floor, preliminary cgo answer

**Files:**
- Create: `hack/bench/seam.sh`, `hack/bench/report/main.go`, `hack/bench/report/report_suite_test.go`, `hack/bench/report/main_test.go`, `hack/bench/report/testdata/seam.jsonl`, `hack/bench/report/testdata/floor.jsonl`
- Modify: `Makefile` (target `bench-seam`), `.gitignore` (`/hack/bench/out/`), `docs/cgo-limitations.md` (the measured section), `hack/rooket/README.md` (one paragraph on `make bench-seam`)

**Interfaces:**
- Consumes: Task 1's benchmark and its `Cell` JSON; `hack/rooket/lib.sh` (`use_release`, `out`, `ceph_cmd`); `hack/rooket/out/<release>/image`.
- Produces: `hack/bench/out/<release>/seam-<date>/` holding `seam-<mode>.jsonl`, `floor.jsonl`, `cpu-<mode>-read4k-256.pprof`, `env.json` and `REPORT.md`; `go run ./hack/bench/report seam -dir DIR` writing `REPORT.md`; the floor line format:

```json
{"kind":"floor","tool":"rados bench","op":"write","size":4096,"conc":64,"seconds":20,"ops_per_sec":31250.5,"mean_us":2044.1,"bandwidth_mbps":122.07}
```

`hack/bench/seam.sh squid|tentacle` does, in order, sourcing `hack/rooket/lib.sh` for the cluster: write `env.json` (release, `ceph_version` from `pinned_version`, `ROOKET_VERSION` if set, `nproc`, `MemTotal`, `uname -r`, `go version`, `git describe --always --dirty`); check `ceph df` reports at least 16 GiB available before allowing the 4 MiB shapes, else pass `-shapes` without them and say so; for each mode in `sync callback pipe` run the benchmark in a fresh process (`go test ... -run '^$' -bench BenchmarkSeam -benchtime "$BENCH_TIME" -args -mode=$mode -conc=$CONC -results=$dir/seam-$mode.jsonl`), then one deliberate over-budget cell per async mode (`-shapes=write4m -conc=64 -inflight-bytes=0 -benchtime=5s`) whose thread growth is the evidence decision D8's limiter exists for; a CPU profile of `read4k` at 256 in the callback mode (`-cpuprofile "$dir/cpu-callback-read4k-256.pprof" -shapes=read4k -conc=256`); the floor with `rados bench` from the cluster's image on the host, so it sits where the Go process sits:

```sh
engine=${ROOKET_ENGINE:-podman}
image=$(cat "${out}/image")
rados_bench() { # op size conc
	local op=$1 size=$2 conc=$3 args=()
	[[ ${op} == write ]] && args=(-b "${size}" --no-cleanup)
	"${engine}" run --rm --network host -v "${out}:/etc/ceph:ro" "${image}" \
		rados -c /etc/ceph/ceph.conf -k /etc/ceph/ceph.client.admin.keyring \
		-p rgw-go-test -N "bench-floor-${size}" bench "${BENCH_FLOOR_SECONDS}" "${op}" -t "${conc}" "${args[@]}" --run-name "floor-${size}-${conc}"
}
```
parsing `Average IOPS:`, `Average Latency(s):` and `Bandwidth (MB/sec):` from the text summary with awk into floor lines, running `write` before `rand` for each size (`rand` reads what `write --no-cleanup` left) and `rados -p rgw-go-test -N bench-floor-<size> cleanup` afterwards, for sizes 4096 and 4194304 at concurrencies 1, 16, 64, 256 and 512 (the 4 MiB size only at 1, 4 and 16, under the same byte budget as the benchmark); finally `go run ./hack/bench/report seam -dir "$dir"`. `BENCH_TIME` defaults to 20s, `BENCH_FLOOR_SECONDS` to 20, `CONC` to `1,16,64,256,512`; `BENCH_QUICK=1` sets 5s, 5 and `16,256` for CI.

`hack/bench/report seam -dir DIR` reads every `seam-*.jsonl` and `floor.jsonl` and writes `REPORT.md`: one table per shape with rows per concurrency and columns per mode (ops/s, p50, p99, p999, mean, CPU µs/op, threads idle→peak) plus, for the four mirror shapes, the floor's ops/s and mean and the ratios; then a verdict block applying the two criteria of "Decisions this plan owns" to every mode and printing PASS or FAIL per criterion per release with the margin, followed by the measurements (CPU per operation, the boundary cost, thread growth and the cgo-frame share), reported against no threshold. Thread growth per cell is `threads_peak - threads_idle`.

- [ ] **Step 1: Write the report tool's failing specs**

`main_test.go`, package `main_test`, against `testdata/seam.jsonl` (twelve hand-written cells: two modes, two shapes, three concurrencies) and `testdata/floor.jsonl` (four lines):

```go
var _ = Describe("seam report", func() {
	It("renders one table per shape with the floor ratios", func() {
		md, verdict, err := report.Seam("testdata")
		Expect(err).NotTo(HaveOccurred())
		Expect(md).To(ContainSubstring("## read4k"))
		Expect(md).To(MatchRegexp(`\| 64 \| .*0\.9[0-9]x`), "throughput ratio against the floor at conc 64")
		Expect(verdict).To(HaveKeyWithValue("throughput>=0.8x floor", true))
		Expect(verdict).To(HaveKeyWithValue("threads<=gomaxprocs+8", false), "the sync cell in testdata grows 70 threads")
	})
	It("fails when a mode's file is missing a cell another mode has", func() {
		_, _, err := report.Seam("testdata/uneven")
		Expect(err).To(MatchError(ContainSubstring("conc=256 missing for mode pipe")))
	})
})
```

Keep the rendering in a `report` package under `hack/bench/report/internal/report` only if the file grows past a screen; otherwise export `Seam` from `main.go`'s package through a `report.go` file in the same `main` package and test it with `package main`. Either way the spec above is what passes.

- [ ] **Step 2: Implement `report`, run the specs to pass**

- [ ] **Step 3: Write `hack/bench/seam.sh` and the Makefile target**

```make
.PHONY: bench-seam
bench-seam: need-release ## Sweep the seam microbenchmark and the rados bench floor against the RELEASE cluster
	ROOKET=$(ROOKET_BIN) RGW_GO_TEST_CEPH_CONF=$(CLUSTER_OUT)/ceph.conf hack/bench/seam.sh $(RELEASE)
```

`shellcheck hack/bench/seam.sh` clean.

- [ ] **Step 4: Run the full sweep on Squid, then Tentacle** (cluster-backed)

```sh
make bench-seam RELEASE=squid
cat hack/bench/out/squid/seam-*/REPORT.md
make cluster-up RELEASE=tentacle && make bench-seam RELEASE=tentacle
```
Expected: a report per release with every cell present; the verdict block prints each criterion. Note the CPU profile's cgo share: `go tool pprof -top -nodecount=40 hack/bench/out/squid/seam-*/cpu-callback-read4k-256.pprof | grep -E 'cgocall|cgocallback|_Cfunc_|Pinner'`.

- [ ] **Step 5: Write the preliminary answer into `docs/cgo-limitations.md`**

Add a section `## Phase 1 measurement` after the registry's preamble with: the date, both releases' `env.json` facts, the two criteria with PASS or FAIL and margin per mode and release, the measurements beside them, the thread growth of the over-budget cell as the limiter's evidence, and a line "preliminary: the gateway-level profile of Task 15 completes this". Update each Inherent entry's `**Measure:**` line with a `**Measured:**` line pointing at the run directory. Copy each run directory into `docs/benchmarks/<date>-seam-<release>/` (raw `.jsonl`, `.pprof`, `env.json`, `REPORT.md`) and start `docs/benchmarks/README.md` as the index (one line per run: date, kind, release, gateway build, link).

- [ ] **Step 6: `make check`, commit as a series**

`feat(bench): sweep the seam microbenchmark per mode against a rados bench floor`; `docs(bench): record the seam microbenchmark on Squid and Tentacle`; `docs(cgo): write the preliminary phase 1 measurement`.

---

### Task 3: Population: compressed objects on four storage classes

**Files:**
- Modify: `hack/rooket/populate.sh`, `hack/rooket/README.md`, `test/gate/manifest.go` (two optional fields on `Object`)

**Interfaces:**
- Produces: in `manifest.json`, four more objects in `plain`, each `{"bucket":"plain","key":"comp-<codec>.bin","size":1048576,"storage_class":"COMP_<CODEC>","compression":"<codec>"}` for `zlib`, `snappy`, `zstd` and `lz4`, and a `storage_classes` map `{"COMP_ZLIB":"zlib",...}` at the top level; `gate.Object` gains `StorageClass string \`json:"storage_class,omitempty"\`` and `Compression string \`json:"compression,omitempty"\``. Unit R's oracle reads these.

radosgw compresses per storage class of a zone placement (`radosgw-admin zone placement add --placement-id <p> --storage-class <sc> --data-pool <pool> --compression <codec>`), and a storage class must also exist in the zonegroup's placement target before a client may name it. The script adds, idempotently (skip when `zone get` already lists the class), to the default placement: `COMP_ZLIB`, `COMP_SNAPPY`, `COMP_ZSTD`, `COMP_LZ4`, each on the STANDARD class's data pool with its codec; adds each to the zonegroup placement target; `period update --commit`; then verifies, as the tier code already does, that the current period's zonegroup lists them and that `zone get` shows each class's `compression_type`. The radosgw reloads its zone from the committed period; the existing `as_user` retries ride out the reload. Payloads must compress: `payload_compressible` writes 1 MiB of `key` repeated (seeded, so a rerun writes identical bytes), and each object is `put comp-<codec>.bin` with `--storage-class COMP_<CODEC>`. After writing, `radosgw-admin object stat --bucket plain --object comp-<codec>.bin` must show `compression` with the codec in its manifest (`.manifest.compression` or the `RGW_ATTR_COMPRESSION` attr in `attrs`); the script dies otherwise, because an uncompressed object would silently defeat R's oracle.

- [ ] **Step 1: Edit `populate.sh`; run it against Squid; check the manifest and the stat** (cluster-backed)

```sh
make populate RELEASE=squid
jq '.storage_classes, [.objects[] | select(.compression) | {key, compression}]' hack/rooket/out/squid/manifest.json
```
Expected: four classes and four objects; `object stat` shows each codec.

- [ ] **Step 2: Run the phase 0 gate on Squid; it must stay green**

```sh
make gate RELEASE=squid
```
The gate round-trips every head's xattrs including `user.rgw.compression` (`meta.CompressionInfo`, phase 0) and counts heads against the manifest, so the four new objects are proven as they land. A failure here is a finding about the compression-info encoder, not a reason to drop the objects.

- [ ] **Step 3: Tentacle: `make populate RELEASE=tentacle && make gate RELEASE=tentacle`**

- [ ] **Step 4: Commit**

`feat(rooket): populate compressed objects on four storage classes for the read oracle`

---

### Task 4: s3-tests harness

**Files:**
- Create: `hack/s3tests/run.sh`, `hack/s3tests/deselect-phase2.txt`, `hack/s3tests/deselect-phase3.txt`
- Modify: `Makefile` (target `s3tests`), `.gitignore` (`/hack/s3tests/out/`), `hack/rooket/README.md`

**Interfaces:**
- Consumes: `hack/rooket/lib.sh` (`use_release`, `rgw_daemon`, `rgw_endpoint`, `toolbox`, `admin` pattern from `populate.sh`); for GATEWAY `rgw-go`, the endpoint file Task 11 writes, `hack/rooket/out/<release>/rgw-go.endpoint`.
- Produces: `hack/s3tests/run.sh RELEASE GATEWAY RUN` writing `hack/s3tests/out/<release>-<gateway>-<run>.xml` (junit), `.log`, and `.conf`; the fixed users below; the marker expression and the deselect lists in one place; `hack/s3tests/run.sh --print-commit` (the pinned s3-tests commit, the one home Tasks 5, 7 and 12 read) and `hack/s3tests/run.sh --print-deselect` (a 12-hex digest of the node ids the lists name, the `deselect` meta key every s3-tests result records).

```sh
#!/usr/bin/env bash
# Runs ceph/s3-tests at a pinned commit against one gateway of a rooket
# cluster and leaves a junit report for hack/parity. The parity set is
# test_s3.py and test_headers.py minus the markers of features not
# implemented yet and of docs/exclusions.md, and minus the tests the
# deselect-phase<N>.txt lists name: tests that call a later phase's feature
# without carrying such a marker. Both gateways run exactly this set.
# fails_on_rgw stays in, since both gateways must fail those alike. Users
# are created through radosgw-admin, so run this after make gate: the gate
# counts the zone's users.
#
# Usage: run.sh squid|tentacle radosgw|rgw-go RUN-ID
#        run.sh --print-commit | --print-deselect
set -euo pipefail
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
readonly here
readonly script=run.sh
# shellcheck source=hack/rooket/lib.sh
source "${here}/../rooket/lib.sh"

readonly S3TESTS_REPO=https://github.com/ceph/s3-tests
readonly S3TESTS_COMMIT=5522d1c351f75bc00ae0f64f742f3f095f5939d9 # master, 2026-05-27; bump by hand and re-record the baselines
readonly PYTEST_TIMEOUT_VERSION=2.4.0

# Features not implemented yet and docs/exclusions.md's, by s3-tests marker.
readonly EXCLUDED_MARKERS=(lifecycle lifecycle_expiration lifecycle_transition cloud_transition cloud_restore
	target_by_bucket versioning delete_marker object_lock object_ownership encryption bucket_encryption sse_s3
	s3select s3website s3website_routing_rules s3website_redirect_location bucket_logging bucket_logging_cleanup
	fails_without_logging_rollover checksum sns appendobject iam_account iam_cross_account iam_tenant iam_user
	iam_role user_policy role_policy group group_policy session_policy abac_test test_of_sts webidentity_test
	token_claims_trust_policy_test token_principal_tag_role_policy_test token_request_tag_trust_policy_test
	token_resource_tags_test token_role_tags_test token_tag_keys_test s3control)
readonly TEST_FILES=(s3tests/functional/test_s3.py s3tests/functional/test_headers.py)
# The tests of later phases' features that no marker above removes, by pytest
# node id, one file per phase. A phase deletes its lines as it lands its
# features, and its file and entry here with the last of them.
readonly DESELECT_LISTS=("${here}/deselect-phase2.txt" "${here}/deselect-phase3.txt")

# deselected prints the node ids the lists name, without comments and blanks.
deselected() { sed -e 's/#.*//' -e 's/[[:space:]]*$//' -e '/^$/d' "${DESELECT_LISTS[@]}"; }

case "${1:-}" in
--print-commit)
	echo "${S3TESTS_COMMIT}"
	exit 0
	;;
--print-deselect)
	deselected | LC_ALL=C sort | sha256sum | cut -c1-12
	exit 0
	;;
esac

use_release "${1:-}"
gateway=${2:-}; run_id=${3:-}
[[ "${gateway}" == radosgw || "${gateway}" == rgw-go ]] || die "usage: ${script} squid|tentacle radosgw|rgw-go RUN-ID"
[[ -n "${run_id}" ]] || die "usage: ${script} squid|tentacle radosgw|rgw-go RUN-ID"
src=${here}/out/src; venv=${here}/out/venv; mkdir -p "${here}/out"
```

The script then: fetches the pinned commit (`git init -q "$src"` once, `git -C "$src" fetch -q --depth 1 "$S3TESTS_REPO" "$S3TESTS_COMMIT"`, `checkout -q FETCH_HEAD`; skip when `rev-parse HEAD` already equals it); creates the venv when absent and `pip install -q -r requirements.txt "pytest-timeout==$PYTEST_TIMEOUT_VERSION"` (requirements.txt is unpinned upstream; the venv is rebuilt when `S3TESTS_COMMIT` changes, and the installed versions are written to `out/<...>.freeze` beside each report so a baseline records what ran); resolves the endpoint (`rgw_endpoint "$(rgw_daemon)"` for radosgw, `cat "${out}/rgw-go.endpoint"` for rgw-go, dying when the file is absent); resolves `api_name` with `admin zonegroup get | jq -r .api_name`; ensures the fixture users through `admin` with fixed keys so every conf is deterministic (create when `user info` fails):

| Section | uid | tenant | caps | keys |
|---|---|---|---|---|
| `s3 main` | `s3tests-main` | | | `S3MAIN000000000000AK` / `s3main-secret-0123456789abcdefghijklmnopqrstuv` |
| `s3 alt` | `s3tests-alt` | | | `S3ALT0000000000000AK` / `s3alt-secret-...` |
| `s3 tenant` | `s3tests-tenant` | `s3tt` | | `S3TENANT0000000000AK` / `...` |
| `iam` | `s3tests-iam` | | `user-policy=*;roles=*;oidc-provider=*` | `S3IAM0000000000000AK` / `...` |
| `iam root` | `s3tests-root1` in account `RGW11111111111111111` (`admin account create --account-id RGW11111111111111111 --account-name s3tests-acct1`, then `user create --account-id ... --account-root`) | | | `S3ROOT100000000000AK` / `...` |
| `iam alt root` | `s3tests-root2` in `RGW22222222222222222` | | | `S3ROOT200000000000AK` / `...` |

(the accounts are cheap on Squid and keep a test that reads `[iam root]` unmarked from erroring on configuration rather than on behavior); writes the conf from a heredoc mirroring `s3tests.conf.SAMPLE`'s sections with `host`, `port`, `is_secure = False`, `bucket prefix = s3t-{random}-`, `api_name`, the users' ids, display names, emails (`<uid>@rgw-go.test`) and keys, `tenant = s3tt`, and the two accounts' `account_id`/`user_id`; builds the marker expression `expr="not ${EXCLUDED_MARKERS[0]}"; for m in "${EXCLUDED_MARKERS[@]:1}"; do expr+=" and not ${m}"; done`; turns the lists into `--deselect` arguments and checks them against pytest's own collection (collection reads no conf: s3-tests reads `S3TEST_CONF` in its package fixtures, [`s3tests/functional/__init__.py:340-360`](https://github.com/ceph/ceph/blob/v20.2.4/src/test/rgw/s3-tests/s3tests/functional/__init__.py#L340-L360)):

```sh
ids=$(cd "${src}" && S3TEST_CONF="${conf}" "${venv}/bin/pytest" --collect-only -q -p no:cacheprovider \
	-m "${expr}" "${TEST_FILES[@]}")
mapfile -t deselect < <(deselected)
declare -A listed=()
deselect_args=()
for id in "${deselect[@]}"; do
	listed[${id}]=1
	deselect_args+=("--deselect=${id}")
done
# pytest's --deselect removes every node id that starts with the argument, so
# a line must remove exactly the test it names, and a line that names nothing
# the markers leave is stale.
mapfile -t collected < <(grep -E '^s3tests/.+\.py::' <<<"${ids}")
declare -A seen=()
for c in "${collected[@]}"; do
	if [[ -n "${listed[${c%%\[*}]:-}" ]]; then
		seen[${c%%\[*}]=1
		continue
	fi
	for id in "${deselect[@]}"; do
		[[ "${c}" != "${id}"* ]] || die "${id} would also deselect ${c} (pytest matches --deselect as a node-id prefix)"
	done
done
for id in "${deselect[@]}"; do
	[[ -n "${seen[${id}]:-}" ]] || die "${id} names no test the markers leave: renamed, removed or marker-excluded at this commit"
done
```

then runs

```sh
(cd "${src}" && S3TEST_CONF="${conf}" "${venv}/bin/pytest" -v -rA -p no:cacheprovider \
	--timeout=300 --junitxml="${report}" -m "${expr}" "${deselect_args[@]}" "${TEST_FILES[@]}") >"${log}" 2>&1 || status=$?
```

and exits 0 whether pytest returned 0 or 1 (tests failed; `hack/parity` judges) but nonzero for any other pytest status (2 interrupted, 3 internal, 4 usage, 5 no tests collected), printing the summary line and the report path. Nothing gates the gateway choice: both gateways run the same markers and the same lists, and the baseline discipline is Task 5's and Task 12's.

```make
GATEWAY ?= radosgw
RUN ?= local
.PHONY: s3tests
s3tests: need-release ## Run the phase 1 s3-tests set against GATEWAY (radosgw|rgw-go) on the RELEASE cluster; junit under hack/s3tests/out/
	ROOKET=$(ROOKET_BIN) hack/s3tests/run.sh $(RELEASE) $(GATEWAY) $(RUN)
```

`hack/s3tests/deselect-phase2.txt` and `hack/s3tests/deselect-phase3.txt` in full at the pinned commit. Every line was checked against the test's body there, the reason naming the calls that need the feature; a test is listed only when it calls such a feature itself or through an s3-tests helper, which is why `test_object_raw_get_x_amz_expires_not_expired` and its `_tenant` twin are CORS lines: their first request is an OPTIONS preflight on the presigned URL ([`s3tests/functional/test_s3.py:3495-3505`](https://github.com/ceph/ceph/blob/v20.2.4/src/test/rgw/s3-tests/s3tests/functional/test_s3.py#L3495-L3505)), an op G registers no handler for in phase 1. Every listed id that is a prefix of another test's id is a prefix only of listed ones, so the check above passes at this commit.

`hack/s3tests/deselect-phase2.txt`:

```
# Tests of the phase 1 s3-tests parity set that call a phase 2 feature and
# carry no marker run.sh's EXCLUDED_MARKERS removes, at run.sh's
# S3TESTS_COMMIT. run.sh deselects every line on both gateways, and stops when
# a line names no collected test or, since pytest's --deselect matches node-id
# prefixes, would deselect a test the lists do not name. The PR that lands a
# feature deletes its lines and re-records the radosgw baselines.
# Format: a pytest node id, then "#" and the calls that need the feature.

# bucket versioning: each enables it (PutBucketVersioning)
s3tests/functional/test_s3.py::test_bucket_list_return_data_versioning # PutBucketVersioning, ListObjectVersions
s3tests/functional/test_s3.py::test_versioning_concurrent_multi_object_delete # PutBucketVersioning, ListObjectVersions
s3tests/functional/test_s3.py::test_object_put_acl_mtime # PutBucketVersioning, ListObjectVersions
s3tests/functional/test_s3.py::test_object_copy_versioned_bucket # PutBucketVersioning
s3tests/functional/test_s3.py::test_object_copy_versioned_url_encoding # PutBucketVersioning
s3tests/functional/test_s3.py::test_object_copy_versioning_multipart_upload # PutBucketVersioning
s3tests/functional/test_s3.py::test_multipart_copy_versioned # PutBucketVersioning, ListObjectVersions
s3tests/functional/test_s3.py::test_versioning_bucket_create_suspend # PutBucketVersioning
s3tests/functional/test_s3.py::test_versioning_obj_create_read_remove # PutBucketVersioning, ListObjectVersions
s3tests/functional/test_s3.py::test_versioning_obj_create_read_remove_head # PutBucketVersioning, ListObjectVersions
s3tests/functional/test_s3.py::test_versioning_stack_delete_merkers # PutBucketVersioning, ListObjectVersions
s3tests/functional/test_s3.py::test_versioning_obj_plain_null_version_removal # PutBucketVersioning, ListObjectVersions
s3tests/functional/test_s3.py::test_versioning_obj_plain_null_version_overwrite # PutBucketVersioning, ListObjectVersions
s3tests/functional/test_s3.py::test_versioning_obj_plain_null_version_overwrite_suspended # PutBucketVersioning, ListObjectVersions
s3tests/functional/test_s3.py::test_versioning_obj_suspend_versions # PutBucketVersioning, ListObjectVersions
s3tests/functional/test_s3.py::test_versioning_obj_suspended_copy # PutBucketVersioning
s3tests/functional/test_s3.py::test_versioning_obj_create_versions_remove_all # PutBucketVersioning, ListObjectVersions
s3tests/functional/test_s3.py::test_versioning_obj_create_versions_remove_special_names # PutBucketVersioning, ListObjectVersions
s3tests/functional/test_s3.py::test_versioning_obj_create_overwrite_multipart # PutBucketVersioning, ListObjectVersions
s3tests/functional/test_s3.py::test_versioning_obj_list_marker # PutBucketVersioning, ListObjectVersions
s3tests/functional/test_s3.py::test_versioning_copy_obj_version # PutBucketVersioning
s3tests/functional/test_s3.py::test_versioning_multi_object_delete # PutBucketVersioning, ListObjectVersions
s3tests/functional/test_s3.py::test_versioning_multi_object_delete_with_marker # PutBucketVersioning, ListObjectVersions
s3tests/functional/test_s3.py::test_versioning_multi_object_delete_with_marker_create # PutBucketVersioning, ListObjectVersions
s3tests/functional/test_s3.py::test_versioned_object_acl # PutBucketVersioning
s3tests/functional/test_s3.py::test_versioned_object_acl_no_version_specified # PutBucketVersioning
s3tests/functional/test_s3.py::test_versioned_concurrent_object_create_concurrent_remove # PutBucketVersioning, ListObjectVersions
s3tests/functional/test_s3.py::test_versioned_concurrent_object_create_and_remove # PutBucketVersioning, ListObjectVersions
s3tests/functional/test_s3.py::test_versioning_bucket_atomic_upload_return_version_id # PutBucketVersioning, ListObjectVersions
s3tests/functional/test_s3.py::test_versioning_bucket_multipart_upload_return_version_id # PutBucketVersioning, ListObjectVersions
s3tests/functional/test_s3.py::test_get_versioned_object_attributes # PutBucketVersioning
s3tests/functional/test_s3.py::test_put_current_object_if_none_match # PutBucketVersioning
s3tests/functional/test_s3.py::test_multipart_put_current_object_if_none_match # PutBucketVersioning
s3tests/functional/test_s3.py::test_put_current_object_if_match # PutBucketVersioning
s3tests/functional/test_s3.py::test_multipart_put_current_object_if_match # PutBucketVersioning
s3tests/functional/test_s3.py::test_put_object_current_if_match # PutBucketVersioning
s3tests/functional/test_s3.py::test_delete_object_current_if_match # PutBucketVersioning
s3tests/functional/test_s3.py::test_delete_object_version_if_match # PutBucketVersioning
s3tests/functional/test_s3.py::test_delete_object_current_if_match_last_modified_time # PutBucketVersioning
s3tests/functional/test_s3.py::test_delete_object_version_if_match_last_modified_time # PutBucketVersioning
s3tests/functional/test_s3.py::test_delete_object_current_if_match_size # PutBucketVersioning
s3tests/functional/test_s3.py::test_delete_object_version_if_match_size # PutBucketVersioning
s3tests/functional/test_s3.py::test_delete_objects_current_if_match # PutBucketVersioning
s3tests/functional/test_s3.py::test_delete_objects_version_if_match # PutBucketVersioning
s3tests/functional/test_s3.py::test_delete_objects_current_if_match_last_modified_time # PutBucketVersioning
s3tests/functional/test_s3.py::test_delete_objects_version_if_match_last_modified_time # PutBucketVersioning
s3tests/functional/test_s3.py::test_delete_objects_current_if_match_size # PutBucketVersioning
s3tests/functional/test_s3.py::test_delete_objects_version_if_match_size # PutBucketVersioning

# object lock
s3tests/functional/test_s3.py::test_object_lock_put_obj_lock # CreateBucket with object lock, PutObjectLockConfiguration
s3tests/functional/test_s3.py::test_object_lock_put_obj_lock_invalid_bucket # PutObjectLockConfiguration
s3tests/functional/test_s3.py::test_object_lock_put_obj_lock_enable_after_create # PutBucketVersioning, PutObjectLockConfiguration
s3tests/functional/test_s3.py::test_object_lock_put_obj_lock_with_days_and_years # CreateBucket with object lock, PutObjectLockConfiguration
s3tests/functional/test_s3.py::test_object_lock_put_obj_lock_invalid_days # CreateBucket with object lock, PutObjectLockConfiguration
s3tests/functional/test_s3.py::test_object_lock_put_obj_lock_invalid_years # CreateBucket with object lock, PutObjectLockConfiguration
s3tests/functional/test_s3.py::test_object_lock_put_obj_lock_invalid_mode # CreateBucket with object lock, PutObjectLockConfiguration
s3tests/functional/test_s3.py::test_object_lock_put_obj_lock_invalid_status # CreateBucket with object lock, PutObjectLockConfiguration
s3tests/functional/test_s3.py::test_object_lock_suspend_versioning # CreateBucket with object lock, PutBucketVersioning
s3tests/functional/test_s3.py::test_object_lock_get_obj_lock # CreateBucket with object lock, PutObjectLockConfiguration, GetObjectLockConfiguration
s3tests/functional/test_s3.py::test_object_lock_get_obj_lock_invalid_bucket # GetObjectLockConfiguration
s3tests/functional/test_s3.py::test_object_lock_put_obj_retention # CreateBucket with object lock, PutObjectRetention, GetObjectRetention, x-amz-bypass-governance-retention
s3tests/functional/test_s3.py::test_object_lock_put_obj_retention_invalid_bucket # PutObjectRetention
s3tests/functional/test_s3.py::test_object_lock_put_obj_retention_invalid_mode # CreateBucket with object lock, PutObjectRetention
s3tests/functional/test_s3.py::test_object_lock_get_obj_retention # CreateBucket with object lock, PutObjectRetention, GetObjectRetention, x-amz-bypass-governance-retention
s3tests/functional/test_s3.py::test_object_lock_get_obj_retention_iso8601 # CreateBucket with object lock, PutObjectRetention, x-amz-bypass-governance-retention
s3tests/functional/test_s3.py::test_object_lock_get_obj_retention_invalid_bucket # GetObjectRetention
s3tests/functional/test_s3.py::test_object_lock_put_obj_retention_versionid # CreateBucket with object lock, PutObjectRetention, GetObjectRetention, x-amz-bypass-governance-retention
s3tests/functional/test_s3.py::test_object_lock_put_obj_retention_override_default_retention # CreateBucket with object lock, PutObjectLockConfiguration, PutObjectRetention, GetObjectRetention, x-amz-bypass-governance-retention
s3tests/functional/test_s3.py::test_object_lock_put_obj_retention_increase_period # CreateBucket with object lock, PutObjectRetention, GetObjectRetention, x-amz-bypass-governance-retention
s3tests/functional/test_s3.py::test_object_lock_put_obj_retention_shorten_period # CreateBucket with object lock, PutObjectRetention, x-amz-bypass-governance-retention
s3tests/functional/test_s3.py::test_object_lock_put_obj_retention_shorten_period_bypass # CreateBucket with object lock, PutObjectRetention, GetObjectRetention, x-amz-bypass-governance-retention
s3tests/functional/test_s3.py::test_object_lock_delete_object_with_retention # CreateBucket with object lock, PutObjectRetention, x-amz-bypass-governance-retention
s3tests/functional/test_s3.py::test_object_lock_delete_multipart_object_with_retention # CreateBucket with object lock, object lock headers on CreateMultipartUpload, x-amz-bypass-governance-retention
s3tests/functional/test_s3.py::test_object_lock_delete_object_with_retention_and_marker # CreateBucket with object lock, PutObjectRetention, x-amz-bypass-governance-retention
s3tests/functional/test_s3.py::test_object_lock_multi_delete_object_with_retention # CreateBucket with object lock, PutObjectRetention, x-amz-bypass-governance-retention
s3tests/functional/test_s3.py::test_object_lock_put_legal_hold # CreateBucket with object lock, PutObjectLegalHold
s3tests/functional/test_s3.py::test_object_lock_put_legal_hold_invalid_bucket # PutObjectLegalHold
s3tests/functional/test_s3.py::test_object_lock_put_legal_hold_invalid_status # CreateBucket with object lock, PutObjectLegalHold
s3tests/functional/test_s3.py::test_object_lock_get_legal_hold # CreateBucket with object lock, PutObjectLegalHold, GetObjectLegalHold
s3tests/functional/test_s3.py::test_object_lock_get_legal_hold_invalid_bucket # GetObjectLegalHold
s3tests/functional/test_s3.py::test_object_lock_delete_object_with_legal_hold_on # CreateBucket with object lock, PutObjectLegalHold
s3tests/functional/test_s3.py::test_object_lock_delete_multipart_object_with_legal_hold_on # CreateBucket with object lock, PutObjectLegalHold, object lock headers on CreateMultipartUpload
s3tests/functional/test_s3.py::test_object_lock_delete_object_with_legal_hold_off # CreateBucket with object lock, PutObjectLegalHold
s3tests/functional/test_s3.py::test_object_lock_get_obj_metadata # CreateBucket with object lock, PutObjectRetention, PutObjectLegalHold, x-amz-bypass-governance-retention
s3tests/functional/test_s3.py::test_object_lock_uploading_obj # CreateBucket with object lock, PutObjectLegalHold, object lock headers on PutObject, x-amz-bypass-governance-retention
s3tests/functional/test_s3.py::test_object_lock_changing_mode_from_governance_with_bypass # CreateBucket with object lock, PutObjectRetention, object lock headers on PutObject, x-amz-bypass-governance-retention
s3tests/functional/test_s3.py::test_object_lock_changing_mode_from_governance_without_bypass # CreateBucket with object lock, PutObjectRetention, object lock headers on PutObject
s3tests/functional/test_s3.py::test_object_lock_changing_mode_from_compliance # CreateBucket with object lock, PutObjectRetention, object lock headers on PutObject

# POST object (browser-form uploads)
s3tests/functional/test_s3.py::test_post_object_anonymous_request # POST object
s3tests/functional/test_s3.py::test_post_object_authenticated_request # POST object
s3tests/functional/test_s3.py::test_post_object_authenticated_no_content_type # POST object
s3tests/functional/test_s3.py::test_post_object_authenticated_request_bad_access_key # POST object
s3tests/functional/test_s3.py::test_post_object_set_success_code # POST object
s3tests/functional/test_s3.py::test_post_object_set_invalid_success_code # POST object
s3tests/functional/test_s3.py::test_post_object_upload_larger_than_chunk # POST object
s3tests/functional/test_s3.py::test_post_object_set_key_from_filename # POST object
s3tests/functional/test_s3.py::test_post_object_ignored_header # POST object
s3tests/functional/test_s3.py::test_post_object_case_insensitive_condition_fields # POST object
s3tests/functional/test_s3.py::test_post_object_escaped_field_values # POST object
s3tests/functional/test_s3.py::test_post_object_success_redirect_action # POST object
s3tests/functional/test_s3.py::test_post_object_invalid_signature # POST object
s3tests/functional/test_s3.py::test_post_object_invalid_access_key # POST object
s3tests/functional/test_s3.py::test_post_object_invalid_date_format # POST object
s3tests/functional/test_s3.py::test_post_object_no_key_specified # POST object
s3tests/functional/test_s3.py::test_post_object_missing_signature # POST object
s3tests/functional/test_s3.py::test_post_object_missing_policy_condition # POST object
s3tests/functional/test_s3.py::test_post_object_user_specified_header # POST object
s3tests/functional/test_s3.py::test_post_object_request_missing_policy_specified_field # POST object
s3tests/functional/test_s3.py::test_post_object_condition_is_case_sensitive # POST object
s3tests/functional/test_s3.py::test_post_object_expires_is_case_sensitive # POST object
s3tests/functional/test_s3.py::test_post_object_expired_policy # POST object
s3tests/functional/test_s3.py::test_post_object_wrong_bucket # POST object
s3tests/functional/test_s3.py::test_post_object_invalid_request_field_value # POST object
s3tests/functional/test_s3.py::test_post_object_missing_expires_condition # POST object
s3tests/functional/test_s3.py::test_post_object_missing_conditions_list # POST object
s3tests/functional/test_s3.py::test_post_object_upload_size_limit_exceeded # POST object
s3tests/functional/test_s3.py::test_post_object_missing_content_length_argument # POST object
s3tests/functional/test_s3.py::test_post_object_invalid_content_length_argument # POST object
s3tests/functional/test_s3.py::test_post_object_upload_size_below_minimum # POST object
s3tests/functional/test_s3.py::test_post_object_upload_size_rgw_chunk_size_bug # POST object
s3tests/functional/test_s3.py::test_post_object_empty_conditions # POST object
s3tests/functional/test_s3.py::test_post_object_tags_anonymous_request # POST object
s3tests/functional/test_s3.py::test_post_object_tags_authenticated_request # POST object

# CORS, including the OPTIONS preflight
s3tests/functional/test_s3.py::test_object_raw_get_x_amz_expires_not_expired # OPTIONS preflight on the presigned URL before its GET
s3tests/functional/test_s3.py::test_object_raw_get_x_amz_expires_not_expired_tenant # OPTIONS preflight on the presigned URL before its GET
s3tests/functional/test_s3.py::test_set_cors # PutBucketCors, GetBucketCors, DeleteBucketCors
s3tests/functional/test_s3.py::test_cors_origin_response # PutBucketCors, GetBucketCors, OPTIONS preflight
s3tests/functional/test_s3.py::test_cors_origin_wildcard # PutBucketCors, GetBucketCors
s3tests/functional/test_s3.py::test_cors_header_option # PutBucketCors, GetBucketCors, OPTIONS preflight
s3tests/functional/test_s3.py::test_cors_presigned_get_object # PutBucketCors, OPTIONS preflight
s3tests/functional/test_s3.py::test_cors_presigned_get_object_tenant # PutBucketCors, OPTIONS preflight
s3tests/functional/test_s3.py::test_cors_presigned_get_object_v2 # PutBucketCors, OPTIONS preflight
s3tests/functional/test_s3.py::test_cors_presigned_get_object_tenant_v2 # PutBucketCors, OPTIONS preflight
s3tests/functional/test_s3.py::test_cors_presigned_put_object # PutBucketCors, OPTIONS preflight
s3tests/functional/test_s3.py::test_cors_presigned_put_object_with_acl # PutBucketCors, OPTIONS preflight
s3tests/functional/test_s3.py::test_cors_presigned_put_object_v2 # PutBucketCors, OPTIONS preflight
s3tests/functional/test_s3.py::test_cors_presigned_put_object_tenant_v2 # PutBucketCors, OPTIONS preflight
s3tests/functional/test_s3.py::test_cors_presigned_put_object_tenant # PutBucketCors, OPTIONS preflight
s3tests/functional/test_s3.py::test_cors_presigned_put_object_tenant_with_acl # PutBucketCors, OPTIONS preflight

# public access block
s3tests/functional/test_s3.py::test_get_undefined_public_block # GetPublicAccessBlock, DeletePublicAccessBlock
s3tests/functional/test_s3.py::test_get_public_block_deny_bucket_policy # PutPublicAccessBlock, GetPublicAccessBlock
s3tests/functional/test_s3.py::test_put_public_block # PutPublicAccessBlock, GetPublicAccessBlock
s3tests/functional/test_s3.py::test_block_public_put_bucket_acls # PutPublicAccessBlock, GetPublicAccessBlock
s3tests/functional/test_s3.py::test_block_public_object_canned_acls # PutPublicAccessBlock
s3tests/functional/test_s3.py::test_block_public_policy # PutPublicAccessBlock
s3tests/functional/test_s3.py::test_block_public_policy_with_principal # PutPublicAccessBlock
s3tests/functional/test_s3.py::test_block_public_restrict_public_buckets # PutPublicAccessBlock, DeletePublicAccessBlock
s3tests/functional/test_s3.py::test_ignore_public_acls # PutPublicAccessBlock
s3tests/functional/test_s3.py::test_put_get_delete_public_block # PutPublicAccessBlock, GetPublicAccessBlock, DeletePublicAccessBlock

# policy status
s3tests/functional/test_s3.py::test_get_bucket_policy_status # GetBucketPolicyStatus
s3tests/functional/test_s3.py::test_get_public_acl_bucket_policy_status # GetBucketPolicyStatus
s3tests/functional/test_s3.py::test_get_authpublic_acl_bucket_policy_status # GetBucketPolicyStatus
s3tests/functional/test_s3.py::test_get_publicpolicy_acl_bucket_policy_status # GetBucketPolicyStatus
s3tests/functional/test_s3.py::test_get_nonpublicpolicy_acl_bucket_policy_status # GetBucketPolicyStatus
s3tests/functional/test_s3.py::test_get_nonpublicpolicy_principal_bucket_policy_status # GetBucketPolicyStatus

# client checksums at the Tentacle level
s3tests/functional/test_s3.py::test_get_checksum_object_attributes # x-amz-checksum-sha256 on PutObject
```

`hack/s3tests/deselect-phase3.txt`:

```
# Tests of the phase 1 s3-tests parity set that call a phase 3 feature and
# carry no marker run.sh's EXCLUDED_MARKERS removes, at run.sh's
# S3TESTS_COMMIT. run.sh deselects every line on both gateways, and stops when
# a line names no collected test or, since pytest's --deselect matches node-id
# prefixes, would deselect a test the lists do not name. The PR that lands a
# feature deletes its lines and re-records the radosgw baselines.
# Format: a pytest node id, then "#" and the calls that need the feature.

# the ?usage extension
s3tests/functional/test_s3.py::test_account_usage # ListBuckets with ?usage
```

- [ ] **Step 1: Write `run.sh`; `shellcheck` clean; run it against the Squid radosgw** (cluster-backed, after `make gate`)

```sh
make gate RELEASE=squid          # first: the gate counts users
make s3tests RELEASE=squid GATEWAY=radosgw RUN=try1
tail -3 hack/s3tests/out/squid-radosgw-try1.log
grep -o '<testsuite [^>]*>' hack/s3tests/out/squid-radosgw-try1.xml | head -1   # tests= failures= errors= skipped=
```
Expected: 393 tests run and 415 deselected (at the pinned commit the two files hold 808 tests, 760 of them in `test_s3.py`; the markers remove 259 and the lists 156), a summary with far more passes than failures, failures dominated by `fails_on_rgw`-marked tests, no errors from configuration (`KeyError` on a conf section would be one), and no test hitting the 300 s timeout. A collection count of 0 means the marker expression or the file paths are wrong; a `NoSectionError` means a section the users table lacks. Then prove the list check, restoring the file after each: a line `s3tests/functional/test_s3.py::test_bucket_listv2_continuationtoken` appended to `deselect-phase3.txt` stops `run.sh` before any test runs, naming `test_bucket_listv2_continuationtoken_empty`, which pytest would also have removed; a line for `test_lifecycle_set` (marked `lifecycle`, so never collected) stops it as naming no test. `hack/s3tests/run.sh --print-deselect` prints `136c2f295daa` for the lists as written above, and the same after a reason is reworded.

- [ ] **Step 2: Same against Tentacle**

- [ ] **Step 3: Document in `hack/rooket/README.md` (order after the gate, the users, where reports land, the deselect lists and that the PR landing a feature deletes its lines and re-records the baselines) and commit**

`feat(s3tests): add the pinned comparative s3-tests harness`

---

### Task 5: `hack/parity` and the s3-tests radosgw baselines

**Files:**
- Create: `hack/parity/main.go`, `hack/parity/parity_suite_test.go`, `hack/parity/main_test.go`, `hack/parity/testdata/run1.xml`, `hack/parity/testdata/run2.xml`, `hack/parity/testdata/gotest.jsonl`, `test/s3tests/baseline/squid.json`, `test/s3tests/baseline/tentacle.json`
- Modify: `Makefile` (targets `parity-record`, `parity-check`), `hack/rooket/README.md`

**Interfaces:**
- Produces:

```
go run ./hack/parity record -format junit|gotest -suite NAME -out FILE [-meta k=v]... RUN-FILE [RUN-FILE...]
    Outcomes come from the last run file; a test whose outcome differs between the
    given runs is listed under "unstable". Every test must appear in every run, or
    record fails naming the missing test.
go run ./hack/parity diff -baseline FILE -candidate FILE [-known FILE]
    -known FILE lists regular expressions over test ids (one per line, "#" comments)
    whose outcomes are EXPECTED to differ (the known-difference list):
    a matching test that differs is skipped; one that AGREES is printed as
    "<test>: known difference no longer differs" and fails the run, so a stale
    entry is noticed the day rgw-go or the baseline changes.
    Exit 0 when every test outside the baseline's unstable set and outside -known
    has the same outcome in both; else prints "<test>: baseline <o1>, candidate <o2>" per difference and
    exits 1. Exits 2 without comparing when the two files' meta differ in any of
    the keys s3tests_commit, go_ceph_tag, release, ceph_version and deselect
    that is present in both, unless -allow-meta-drift is given.
```

Result file schema, `hack/parity` package doc is its one home:

```json
{
  "suite": "s3tests",
  "meta": {"s3tests_commit": "5522d1c3...", "deselect": "136c2f295daa", "release": "squid", "ceph_version": "19.2.6", "gateway": "radosgw", "recorded": "2026-09-28T03:11:00Z", "runs": 2},
  "outcomes": {"s3tests.functional.test_s3::test_bucket_list_empty": "passed", "...": "failed"},
  "unstable": ["s3tests.functional.test_s3::test_something_racy"]
}
```

An s3-tests result's `deselect` is `hack/s3tests/run.sh --print-deselect` for the lists the run applied (Task 4), so a baseline recorded before a list changed is refused like one from another s3-tests commit. Outcomes are `passed`, `failed`, `error` or `skipped`. A junit test id is `classname + "::" + name`; a `go test -json` id is the `Test` field of its terminal `pass`, `fail` or `skip` event (`TestRadosGWTestSuite/TestUser`), package-level events (empty `Test`) ignored. Only `encoding/xml`, `encoding/json`, `flag`, `os`, `sort`, `fmt`, `time` are imported.

- [ ] **Step 1: Write the failing specs**

`testdata/run1.xml` and `run2.xml`: pytest-shaped junit (`<testsuites><testsuite ...><testcase classname="s3tests.functional.test_s3" name="test_a"/>...`) with six cases: `test_a` passes in both; `test_b` fails in both (`<failure message="..."/>`); `test_c` passes in run1 and fails in run2; `test_d` skipped in both (`<skipped/>`); `test_e` errors in both (`<error/>`); `test_f` passes in both. `testdata/gotest.jsonl`: events for `TestRadosGWTestSuite`, `TestRadosGWTestSuite/TestUser` pass, `TestRadosGWTestSuite/TestUsage` fail, `TestRadosGWTestSuite/TestAccount` skip.

```go
var _ = Describe("record", func() {
	It("takes outcomes from the last run and marks disagreements unstable", func() {
		res, err := parity.Record(parity.FormatJunit, "s3tests", map[string]string{"release": "squid"},
			[]string{"testdata/run1.xml", "testdata/run2.xml"})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Outcomes).To(HaveKeyWithValue("s3tests.functional.test_s3::test_a", "passed"))
		Expect(res.Outcomes).To(HaveKeyWithValue("s3tests.functional.test_s3::test_b", "failed"))
		Expect(res.Outcomes).To(HaveKeyWithValue("s3tests.functional.test_s3::test_d", "skipped"))
		Expect(res.Outcomes).To(HaveKeyWithValue("s3tests.functional.test_s3::test_e", "error"))
		Expect(res.Unstable).To(ConsistOf("s3tests.functional.test_s3::test_c"))
		Expect(res.Meta).To(HaveKeyWithValue("runs", "2"))
	})
	It("reads go test -json terminal events", func() {
		res, err := parity.Record(parity.FormatGoTest, "admin", nil, []string{"testdata/gotest.jsonl"})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Outcomes).To(Equal(map[string]string{
			"TestRadosGWTestSuite/TestUser": "passed", "TestRadosGWTestSuite/TestUsage": "failed", "TestRadosGWTestSuite/TestAccount": "skipped",
		}))
	})
	It("fails when a run lacks a test another run has", func() {
		_, err := parity.Record(parity.FormatJunit, "s3tests", nil, []string{"testdata/run1.xml", "testdata/run-short.xml"})
		Expect(err).To(MatchError(ContainSubstring("test_f")))
	})
})

var _ = Describe("diff", func() {
	base := parity.Result{Suite: "s3tests", Meta: map[string]string{"s3tests_commit": "aaa", "release": "squid"},
		Outcomes: map[string]string{"t1": "passed", "t2": "failed", "t3": "passed"}, Unstable: []string{"t3"}}
	It("is equal when only unstable tests differ", func() {
		cand := parity.Result{Meta: map[string]string{"s3tests_commit": "aaa", "release": "squid", "gateway": "rgw-go"},
			Outcomes: map[string]string{"t1": "passed", "t2": "failed", "t3": "failed"}}
		diffs, err := parity.Diff(base, cand, false)
		Expect(err).NotTo(HaveOccurred())
		Expect(diffs).To(BeEmpty())
	})
	It("names every stable test whose outcome changed, and a test missing from the candidate", func() {
		cand := parity.Result{Meta: map[string]string{"s3tests_commit": "aaa", "release": "squid"},
			Outcomes: map[string]string{"t1": "failed", "t3": "passed"}}
		diffs, err := parity.Diff(base, cand, false)
		Expect(err).NotTo(HaveOccurred())
		Expect(diffs).To(ConsistOf("t1: baseline passed, candidate failed", "t2: baseline failed, candidate missing"))
	})
	It("refuses to compare across a different s3-tests commit unless told to", func() {
		cand := parity.Result{Meta: map[string]string{"s3tests_commit": "bbb", "release": "squid"}, Outcomes: base.Outcomes}
		_, err := parity.Diff(base, cand, false)
		Expect(err).To(MatchError(ContainSubstring("s3tests_commit")))
		_, err = parity.Diff(base, cand, true)
		Expect(err).NotTo(HaveOccurred())
	})
	It("refuses to compare results run under different deselect lists", func() {
		listed := parity.Result{Meta: map[string]string{"s3tests_commit": "aaa", "release": "squid", "deselect": "111111111111"},
			Outcomes: base.Outcomes}
		cand := parity.Result{Meta: map[string]string{"s3tests_commit": "aaa", "release": "squid", "deselect": "222222222222"},
			Outcomes: base.Outcomes}
		_, err := parity.Diff(listed, cand, false)
		Expect(err).To(MatchError(ContainSubstring("deselect")))
	})
	It("ignores keys outside the compared set, such as the gateway", func() {
		cand := parity.Result{Meta: map[string]string{"s3tests_commit": "aaa", "release": "squid", "gateway": "rgw-go"},
			Outcomes: base.Outcomes}
		withGateway := base
		withGateway.Meta = map[string]string{"s3tests_commit": "aaa", "release": "squid", "gateway": "radosgw"}
		diffs, err := parity.Diff(withGateway, cand, false)
		Expect(err).NotTo(HaveOccurred())
		Expect(diffs).To(BeEmpty())
	})
})
```

`Record`, `Diff`, `Result` and the `Format` constants live in `hack/parity/parity.go` (package `main`) and the specs are `package main` with the import alias dropped, or the logic sits in `hack/parity/internal/parity` imported by `main.go`; pick the latter, since `main_test.go` then stays `package parity_test` as the canon prefers.

- [ ] **Step 2: Run to fail, implement, run to pass, `make check`**

- [ ] **Step 3: Makefile targets**

```make
.PHONY: parity-record
parity-record: ## Record a baseline from RUN-FILES: make parity-record SUITE=s3tests FORMAT=junit OUT=test/s3tests/baseline/squid.json META="release=squid ceph_version=19.2.6 s3tests_commit=..." FILES="a.xml b.xml"
	go run ./hack/parity record -format $(FORMAT) -suite $(SUITE) -out $(OUT) $(foreach kv,$(META),-meta $(kv)) $(FILES)

.PHONY: parity-check
parity-check: ## Compare a candidate result with a baseline: make parity-check BASELINE=... CANDIDATE=...
	go run ./hack/parity diff -baseline $(BASELINE) -candidate $(CANDIDATE)
```

- [ ] **Step 4: Record the Squid baseline from two radosgw runs** (cluster-backed, after `make gate`)

```sh
make s3tests RELEASE=squid GATEWAY=radosgw RUN=base1
make s3tests RELEASE=squid GATEWAY=radosgw RUN=base2
ceph_version=$(jq -r .ceph_version hack/rooket/out/squid/manifest.json)
make parity-record SUITE=s3tests FORMAT=junit OUT=test/s3tests/baseline/squid.json \
  META="release=squid ceph_version=$ceph_version gateway=radosgw s3tests_commit=$(hack/s3tests/run.sh --print-commit) deselect=$(hack/s3tests/run.sh --print-deselect) pytest_freeze=$(sha256sum hack/s3tests/out/squid-radosgw-base2.freeze | cut -c1-12)" \
  FILES="hack/s3tests/out/squid-radosgw-base1.xml hack/s3tests/out/squid-radosgw-base2.xml"
jq '{n: (.outcomes|length), passed: [.outcomes[]|select(.=="passed")]|length, failed: [.outcomes[]|select(.=="failed")]|length, unstable: .unstable}' test/s3tests/baseline/squid.json
```
Expected: a baseline with every collected test (393 outcomes at the pinned commit, none of them a listed test), an `unstable` list that is empty or short (each entry is worth a look: a radosgw flake is a finding for `docs/ceph-upstream-bugs.md` when reproducible), and `parity-check` of `base2` recorded alone against the baseline exits 0.

- [ ] **Step 5: Tentacle baseline the same way**

- [ ] **Step 6: Commit as a series**

`feat(parity): record and compare test outcomes from junit and go test -json`; `test(s3tests): record radosgw's phase 1 baseline on Squid and Tentacle`.

---

### Task 6: go-ceph rgw/admin suite runner and baselines

**Files:**
- Create: `hack/admin/run.sh`, `hack/admin/endpoint.patch`, `test/admin/baseline/squid.json`, `test/admin/baseline/tentacle.json`
- Modify: `Makefile` (target `admin-suite`), `.gitignore` (`/hack/admin/out/`), `hack/rooket/README.md`

**Interfaces:**
- Consumes: `hack/rooket/lib.sh`; `hack/parity`; for GATEWAY `rgw-go`, `hack/rooket/out/<release>/rgw-go.endpoint`.
- Produces: `hack/admin/run.sh RELEASE GATEWAY RUN` writing `hack/admin/out/<release>-<gateway>-<run>.jsonl` (`go test -json`) and `.log`; the fixture user `admin`; the rgw usage options set on the radosgw's entity.

The go-ceph suite's endpoint is hard-coded ([`rgw/admin/radosgw_test.go:134-143`](https://github.com/jhoblitt/go-ceph/blob/cbf97f85fcf5000ca4beca4a7fbf2ee746fa965d/rgw/admin/radosgw_test.go#L134-L143): `http://test_ceph_a` unless `HOSTNAME=test_ceph_aio`), so `endpoint.patch` replaces `SetupConnection`'s body with

```go
func (suite *RadosGWTestSuite) SetupConnection() {
	suite.accessKey = "AKIAIOSFODNN7EXAMPLE"
	suite.secretKey = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
	endpoint := os.Getenv("RGW_GO_ADMIN_ENDPOINT")
	if endpoint == "" {
		suite.T().Fatal("RGW_GO_ADMIN_ENDPOINT is not set")
	}
	suite.endpoint = endpoint
	suite.bucketTestName = "test"
}
```

generated with `git diff` in the checkout so it applies cleanly at the tag. `run.sh`: `GO_CEPH_TAG=v0.39.0`, fetch into `hack/admin/out/go-ceph` (`git init`, `fetch --depth 1 https://github.com/ceph/go-ceph refs/tags/$GO_CEPH_TAG`, `checkout FETCH_HEAD`, `git apply "$here/endpoint.patch"`; skip when already at the tag with the patch applied, detected by `grep -q RGW_GO_ADMIN_ENDPOINT rgw/admin/radosgw_test.go`); ensure the user `admin` with `--access-key AKIAIOSFODNN7EXAMPLE --secret-key wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY --display-name "Admin User" --caps "buckets=*;users=*;usage=read;metadata=read;info=read;accounts=*"` through `admin user create` (and `admin caps add --uid admin --caps ...` when it already exists, so a rerun after a caps change converges); for GATEWAY `radosgw`, set the usage options the suite needs on the radosgw's own entity and restart it once:

```sh
rgw_id=$(jq -r '.metadata.id // empty' <<<"${rgw}")   # the daemon id in the service map, already in cephx form, e.g. ceph.objectstore.a
[[ -n "${rgw_id}" ]] || die "the radosgw reports no id: ${rgw}"
# Rook's generateCephXUser maps the store name's dashes to DOTS (rook
# pkg/operator/ceph/object/config.go:161-164) and runs radosgw under that
# name, so the service map's id needs no rewriting; the `config show
# rgw_frontends` check below guards the result either way.
entity="client.rgw.${rgw_id}"
for kv in rgw_enable_usage_log=true rgw_usage_log_tick_interval=1 rgw_usage_log_flush_threshold=1; do
	ceph_cmd config set "${entity}" "${kv%%=*}" "${kv#*=}"
done
k -n rook-ceph rollout restart deploy -l app=rook-ceph-rgw
k -n rook-ceph rollout status deploy -l app=rook-ceph-rgw --timeout=5m
curl -fsS --retry 30 --retry-delay 2 --retry-connrefused -o /dev/null "${endpoint}/"
```

The entity name is checked before it is set: `ceph_cmd config show "${entity}" rgw_frontends` must print the frontend spec, which proves the section name is the one the running daemon reads (Rook names the daemon `client.rgw.<store>.<letter>` with dashes mapped to underscores; the check catches a mapping mistake instead of setting options nothing reads). For GATEWAY `rgw-go`, the options are Task 11's to set before starting. Then

```sh
(cd "${src}" && RGW_GO_ADMIN_ENDPOINT="${endpoint}" go test -tags ceph_preview -count=1 -v -json -run TestRadosGWTestSuite ./rgw/admin/) >"${report}" 2>"${log}" || status=$?
```

exiting 0 for status 0 or 1 and nonzero otherwise, as Task 4 does. `-tags ceph_preview` is what Rook's own CI sets for the account APIs (`GOFLAGS: -tags=ceph_preview`, rook [`.github/workflows/ceph-suite-integration-test.yml:33-35`](https://github.com/rook/rook/blob/v1.20.7/.github/workflows/ceph-suite-integration-test.yml#L33-L35)). Module downloads for the go-ceph checkout need network (and locally, the sandbox disabled).

```make
.PHONY: admin-suite
admin-suite: need-release ## Run go-ceph's rgw/admin suite at its pinned tag against GATEWAY on the RELEASE cluster
	ROOKET=$(ROOKET_BIN) hack/admin/run.sh $(RELEASE) $(GATEWAY) $(RUN)
```

- [ ] **Step 1: Write `endpoint.patch` from a scratch clone of go-ceph at `v0.39.0`; write `run.sh`; `shellcheck`**

- [ ] **Step 2: Run twice against the Squid radosgw and record** (cluster-backed, after `make gate`)

```sh
make admin-suite RELEASE=squid GATEWAY=radosgw RUN=base1
make admin-suite RELEASE=squid GATEWAY=radosgw RUN=base2
make parity-record SUITE=admin FORMAT=gotest OUT=test/admin/baseline/squid.json \
  META="release=squid ceph_version=$(jq -r .ceph_version hack/rooket/out/squid/manifest.json) gateway=radosgw go_ceph_tag=v0.39.0" \
  FILES="hack/admin/out/squid-radosgw-base1.jsonl hack/admin/out/squid-radosgw-base2.jsonl"
jq '.outcomes' test/admin/baseline/squid.json
```
Expected: every `TestRadosGWTestSuite/*` present; `TestUsage` passes (it is what the three options are for; a failure there means the entity name or the restart did not take); `unstable` empty. A test that fails on radosgw is recorded as `failed` and stays in the baseline: rgw-go must fail it the same way.

- [ ] **Step 3: Tentacle baseline**

- [ ] **Step 4: Commit as a series**

`feat(admin): run go-ceph's rgw/admin suite against a rooket cluster`; `test(admin): record radosgw's rgw/admin baseline on Squid and Tentacle`.

---

### Task 7: Integration workflow: baselines and the quick microbenchmark nightly

**Files:**
- Modify: `.github/workflows/integration.yml`, `README.md`, `hack/admin/run.sh` (`--print-tag`)

**Interfaces:**
- Consumes: Tasks 2, 5, 6's Makefile targets and committed baselines.
- Produces: nightly evidence that radosgw's baselines still hold on the pinned images and pins, and a quick seam sweep as an artifact.

After the `Phase 0 gate` step, in this order (the gate counts users):

```yaml
      # radosgw's own baselines, recorded in test/*/baseline/. A drift here is
      # a change in the Ceph image, the s3-tests pin or the go-ceph tag, never
      # an rgw-go bug: re-record on purpose (hack/rooket/README.md).
      - name: s3-tests against radosgw
        run: make s3tests "RELEASE=${RELEASE}" GATEWAY=radosgw RUN=nightly

      - name: Compare with the recorded s3-tests baseline
        run: |
          go run ./hack/parity record -format junit -suite s3tests -out "${RUNNER_TEMP}/s3tests-nightly.json" \
            -meta "release=${RELEASE}" -meta "ceph_version=$(jq -r .ceph_version "hack/rooket/out/${RELEASE}/manifest.json")" \
            -meta "s3tests_commit=$(hack/s3tests/run.sh --print-commit)" -meta "deselect=$(hack/s3tests/run.sh --print-deselect)" \
            "hack/s3tests/out/${RELEASE}-radosgw-nightly.xml"
          go run ./hack/parity diff -baseline "test/s3tests/baseline/${RELEASE}.json" -candidate "${RUNNER_TEMP}/s3tests-nightly.json"

      - name: go-ceph rgw/admin suite against radosgw
        run: make admin-suite "RELEASE=${RELEASE}" GATEWAY=radosgw RUN=nightly

      - name: Compare with the recorded rgw/admin baseline
        run: |
          go run ./hack/parity record -format gotest -suite admin -out "${RUNNER_TEMP}/admin-nightly.json" \
            -meta "release=${RELEASE}" -meta "ceph_version=$(jq -r .ceph_version "hack/rooket/out/${RELEASE}/manifest.json")" \
            -meta "go_ceph_tag=$(hack/admin/run.sh --print-tag)" "hack/admin/out/${RELEASE}-radosgw-nightly.jsonl"
          go run ./hack/parity diff -baseline "test/admin/baseline/${RELEASE}.json" -candidate "${RUNNER_TEMP}/admin-nightly.json"

      - name: Seam microbenchmark (quick)
        run: make bench-seam "RELEASE=${RELEASE}" BENCH_QUICK=1

      - name: Upload harness reports
        if: always()
        uses: actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a # v7.0.1
        with:
          name: harness-${{ matrix.release }}
          path: |
            hack/s3tests/out/*.xml
            hack/s3tests/out/*.log
            hack/admin/out/*.jsonl
            hack/admin/out/*.log
            hack/bench/out/
          retention-days: 14
          if-no-files-found: ignore
```

`timeout-minutes` rises from 90 to 150. The workflow reads each pin from its one home: `hack/s3tests/run.sh --print-commit` and `--print-deselect` (Task 4), and `hack/admin/run.sh --print-tag`, which this task adds to Task 6's script (it prints `GO_CEPH_TAG` and exits before the release argument is read). A nightly run under lists that changed since the baseline was recorded exits 2 at the comparison, which is the prompt to re-record. python3 with `venv` is on the runner image; nothing to install.

- [ ] **Step 1: Edit; `actionlint .github/workflows/integration.yml`; `GITHUB_TOKEN=$(gh auth token) pinact run --check .github/workflows/integration.yml`**

- [ ] **Step 2: Push the branch, dispatch once, watch**

```sh
gh workflow run integration.yml --ref "$(git branch --show-current)"
```
Watch per the CI-watching canon. Expected: both matrix jobs green with the two compare steps exiting 0. A `diff` failure on a fresh baseline means the runner's radosgw differs from the local one (different Ceph patch release in the image, or a flaky test not caught by two local runs): add the test to `unstable` by re-recording with the nightly run as a third input, and say so in the commit.

- [ ] **Step 3: README development section: the three targets and the order; commit**

`ci: check radosgw's s3-tests and rgw/admin baselines nightly and sweep the seam benchmark`

---

### Task 8: Derived image build

**Files:**
- Create: `hack/image/Containerfile.builder`, `hack/image/Containerfile`, `hack/image/cgo-build.sh`, `hack/image/build.sh`, `.dockerignore`
- Modify: `Makefile` (target `image`), `.gitignore` (`/hack/image/out/`), `.github/dependabot.yml` (docker ecosystem for `/hack/image`), `hack/rooket/README.md`

**Interfaces:**
- Consumes: `hack/rooket/lib.sh` `pinned_tag` and `pinned_version` (the release's Ceph image is set in one place); `cmd/rgw-go` (phase 0's binary builds and answers `version`; G's binary serves).
- Produces: `hack/image/build.sh RELEASE [TAG]` printing the image reference it built, `ghcr.io/jhoblitt/rgw-go:local-<ceph-tag>` by default (a fully qualified name that is never pushed, so a kind node with it loaded never tries to pull); `hack/image/cgo-build.sh <go args...>`, which runs `go "$@"` inside the builder with the repository mounted at its own path, honoring `GOOS`, `GOARCH`, `CGO_ENABLED`, `GOFLAGS` and `RGW_GO_CEPH_TAG` from the caller's environment (goreleaser's `tool` contract, Task 9).

`Containerfile.builder`:

```dockerfile
# The build stage for rgw-go: the target Ceph image itself, so the binary
# links against the librados and glibc it will run with, plus the headers,
# git for the version stamp and the Go toolchain.
ARG CEPH_IMAGE=quay.io/ceph/ceph:v19.2.6
FROM docker.io/library/golang:1.27.1-bookworm@sha256:<digest> AS go
FROM ${CEPH_IMAGE}
ARG CEPH_VERSION=19.2.6
# The Ceph image deletes its ceph repo file after installing (container/Containerfile
# in ceph.git, "CLEAN UP!"), so the matching -devel package comes from the
# release repo for the image's exact version.
RUN rpm --import https://download.ceph.com/keys/release.asc && \
    printf '[ceph]\nname=ceph\nbaseurl=https://download.ceph.com/rpm-%s/el9/$basearch\nenabled=1\ngpgcheck=1\ngpgkey=https://download.ceph.com/keys/release.asc\n' "${CEPH_VERSION}" >/etc/yum.repos.d/ceph.repo && \
    dnf install -y --setopt=install_weak_deps=False "librados-devel-${CEPH_VERSION}" git && \
    dnf clean all
COPY --from=go /usr/local/go /usr/local/go
ENV PATH=/usr/local/go/bin:${PATH} GOTOOLCHAIN=local GOFLAGS=-mod=mod
```

`GOTOOLCHAIN=local` makes a `go.mod` that moves past the image's Go fail loudly at build time rather than download a toolchain; the `golang` `FROM` line carries a digest Dependabot's docker ecosystem bumps. `gcc` is already in the Ceph image (its `packages.txt`); the builder's `dnf install` is checked on first build for what it pulls in, and the header comment records it.

`Containerfile`:

```dockerfile
# rgw-go in the target Ceph image as radosgw. Ceph's radosgw stays beside it,
# and every other Ceph binary is untouched: under Rook this image is every
# daemon's and the toolbox's.
ARG CEPH_IMAGE=quay.io/ceph/ceph:v19.2.6
FROM ${CEPH_IMAGE}
COPY rgw-go /usr/bin/rgw-go
RUN mv /usr/bin/radosgw /usr/bin/radosgw.ceph && ln -s rgw-go /usr/bin/radosgw
LABEL org.opencontainers.image.title="rgw-go" \
      org.opencontainers.image.source="https://github.com/jhoblitt/rgw-go" \
      org.opencontainers.image.licenses="LGPL-2.1-or-later" \
      org.opencontainers.image.base.name="${CEPH_IMAGE}"
```

The build context of `Containerfile` is a directory holding only the binary, so it is the same file goreleaser's `dockers` uses with the binary it copies in. `cgo-build.sh`:

```sh
#!/usr/bin/env bash
# Runs `go "$@"` inside the rgw-go builder image for the Ceph tag in
# RGW_GO_CEPH_TAG (hack/rooket/<release>/values/... is where a release's tag
# lives; build.sh passes it), with the repository mounted at its own path so
# relative output paths resolve, and the caller's GOOS, GOARCH, CGO_ENABLED
# and GOFLAGS passed through. goreleaser invokes it as builds[].tool.
set -euo pipefail
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repo=$(cd "${here}/../.." && pwd)
tag=${RGW_GO_CEPH_TAG:?set RGW_GO_CEPH_TAG to the Ceph image tag, e.g. v19.2.6}
[[ "${tag}" =~ ^v([0-9]+\.[0-9]+\.[0-9]+)$ ]] || { echo "cgo-build.sh: ${tag} is not a vX.Y.Z tag" >&2; exit 1; }
version=${BASH_REMATCH[1]}
engine=${RGW_GO_ENGINE:-$(command -v podman || command -v docker)}
builder=localhost/rgw-go-builder:${tag}
if ! "${engine}" image exists "${builder}" 2>/dev/null && ! "${engine}" image inspect "${builder}" >/dev/null 2>&1; then
	"${engine}" build --platform linux/amd64 -f "${here}/Containerfile.builder" \
		--build-arg "CEPH_IMAGE=quay.io/ceph/ceph:${tag}" --build-arg "CEPH_VERSION=${version}" -t "${builder}" "${here}"
fi
modcache=$(go env GOMODCACHE 2>/dev/null || echo "${HOME}/go/pkg/mod")
mkdir -p "${modcache}" "${repo}/hack/image/out/gocache"
user_flags=(--user "$(id -u):$(id -g)")
[[ "${engine}" == *podman ]] && user_flags=(--userns=keep-id)
exec "${engine}" run --rm "${user_flags[@]}" --platform linux/amd64 \
	-v "${repo}:${repo}" -w "${repo}" -v "${modcache}:/go/pkg/mod" \
	-e GOMODCACHE=/go/pkg/mod -e GOCACHE="${repo}/hack/image/out/gocache" -e HOME=/tmp \
	-e GOOS="${GOOS:-linux}" -e GOARCH="${GOARCH:-amd64}" -e CGO_ENABLED="${CGO_ENABLED:-1}" -e GOFLAGS="${GOFLAGS:--mod=mod}" \
	"${builder}" go "$@"
```

`build.sh RELEASE [TAG]`: sources `lib.sh`, `use_release`, `tag=$(pinned_tag)`, builds the binary with `RGW_GO_CEPH_TAG=$tag hack/image/cgo-build.sh build -trimpath -tags ceph_preview -ldflags '-s -w' -o hack/image/out/$release/rgw-go ./cmd/rgw-go`, assembles with `"$engine" build --platform linux/amd64 -f hack/image/Containerfile --build-arg CEPH_IMAGE=quay.io/ceph/ceph:$tag -t "$image" hack/image/out/$release/` (the context directory holds the binary alone), and prints `$image`. `.dockerignore` at the root excludes `hack/*/out/`, `dist/`, `bin/`, `coverage.out` and nothing under `.git`, because the builder mounts the checkout and the stamp needs the tags.

```make
.PHONY: image
image: need-release ## Build the derived Ceph image for RELEASE with rgw-go as radosgw (prints the reference)
	ROOKET=$(ROOKET_BIN) hack/image/build.sh $(RELEASE)
```

Dependabot, appended under `updates:`:

```yaml
  # The Go toolchain image the builder stage copies from (hack/image/Containerfile.builder).
  - package-ecosystem: docker
    directory: /hack/image
    schedule:
      interval: weekly
    commit-message:
      prefix: "chore(deps)"
```

- [ ] **Step 1: Inspect the base image once and record what the header comments claim; pin the Go image**

```sh
podman run --rm quay.io/ceph/ceph:v19.2.6 sh -c 'cat /etc/os-release | head -2; rpm -q librados2 gcc git; ls /etc/yum.repos.d/; readlink -f /usr/bin/radosgw'
podman run --rm quay.io/ceph/ceph:v20.2.4 sh -c 'cat /etc/os-release | head -2; rpm -q librados2 gcc git; ls /etc/yum.repos.d/'
skopeo inspect --format '{{.Digest}}' docker://docker.io/library/golang:1.27.1-bookworm
```
Expected: CentOS Stream 9 on both, `librados2-19.2.6`/`-20.2.4`, `gcc` present, `git` absent, no `ceph.repo`. If Tentacle's image is on a different base, the `el9` in the repo URL becomes a build argument derived from `/etc/os-release` and the header says so. The digest skopeo prints replaces `<digest>` in `Containerfile.builder`'s `FROM` line (the registry is not reachable from the planning sandbox, which is why it is a step here); Dependabot's docker entry bumps it from then on.

- [ ] **Step 2: Write the files; build Squid; test the image**

```sh
img=$(make -s image RELEASE=squid)
podman run --rm "$img" rgw-go version
podman run --rm "$img" readlink -f /usr/bin/radosgw
podman run --rm "$img" sh -c 'ldd /usr/bin/rgw-go | grep -E "librados|not found"'
podman run --rm "$img" sh -c 'radosgw.ceph --version && ceph --version && radosgw-admin --version && rados --version'
```
Expected: `rgw-go version` prints the stamp `go version -m` reports for a host build of the same commit (a pseudo-version, not `(devel)`); `/usr/bin/rgw-go`; `librados.so.2 => /lib64/librados.so.2` and no `not found`; the four Ceph commands print `ceph version 19.2.6 ...` (Review Focus 5). Add these five checks to `build.sh` behind `RGW_GO_IMAGE_TEST=1` so `make image` verifies what it built.

- [ ] **Step 3: Tentacle: `make image RELEASE=tentacle` with the same checks**

- [ ] **Step 4: `make check`; `actionlint` is not involved; commit**

`feat(image): build the derived Ceph image with rgw-go as radosgw`

---

### Task 9: goreleaser and the release workflow

**Files:**
- Modify: `.goreleaser.yaml`, `.github/workflows/release.yml`, `README.md`

**Interfaces:**
- Consumes: Task 8's `cgo-build.sh` and `Containerfile`.
- Produces: on a `v*` tag, `ghcr.io/jhoblitt/rgw-go:<version>-ceph-v19.2.6`, `:<version>-squid`, `:squid`, and the Tentacle three; archives per release; checksums, SBOM, keyless signatures over checksums and images.

`.goreleaser.yaml` replaces the phase-0 single build and the `kos` entry:

```yaml
version: 2

# Every build is cgo inside a builder stage from the target Ceph image
# (hack/image/cgo-build.sh), so a binary links against that release's
# librados and glibc; hence one build per release and linux/amd64 only.
builds:
  - id: rgw-go-squid
    main: ./cmd/rgw-go
    binary: rgw-go
    tool: hack/image/cgo-build.sh
    env:
      - CGO_ENABLED=1
      - RGW_GO_CEPH_TAG=v19.2.6
    flags:
      - -trimpath
      - -tags=ceph_preview   # keep in step with GO_TAGS in the Makefile
    ldflags:
      - -s -w
    goos: [linux]
    goarch: [amd64]
  - id: rgw-go-tentacle
    main: ./cmd/rgw-go
    binary: rgw-go
    tool: hack/image/cgo-build.sh
    env:
      - CGO_ENABLED=1
      - RGW_GO_CEPH_TAG=v20.2.4
    flags: [-trimpath, -tags=ceph_preview]
    ldflags: [-s -w]
    goos: [linux]
    goarch: [amd64]

archives:
  - id: squid
    ids: [rgw-go-squid]
    formats: [tar.gz]
    name_template: "{{ .ProjectName }}_{{ .Version }}_ceph-v19.2.6_{{ .Os }}_{{ .Arch }}"
  - id: tentacle
    ids: [rgw-go-tentacle]
    formats: [tar.gz]
    name_template: "{{ .ProjectName }}_{{ .Version }}_ceph-v20.2.4_{{ .Os }}_{{ .Arch }}"

checksum:
  name_template: checksums.txt

dockers:
  - id: squid
    ids: [rgw-go-squid]
    dockerfile: hack/image/Containerfile
    use: docker
    image_templates:
      - "ghcr.io/jhoblitt/rgw-go:{{ .Version }}-ceph-v19.2.6"
      - "ghcr.io/jhoblitt/rgw-go:{{ .Version }}-squid"
      - "ghcr.io/jhoblitt/rgw-go:squid"
    build_flag_templates:
      - --platform=linux/amd64
      - --build-arg=CEPH_IMAGE=quay.io/ceph/ceph:v19.2.6
      - --label=org.opencontainers.image.version={{ .Version }}
      - --label=org.opencontainers.image.revision={{ .FullCommit }}
      - --label=org.opencontainers.image.created={{ .Date }}
  - id: tentacle
    ids: [rgw-go-tentacle]
    dockerfile: hack/image/Containerfile
    use: docker
    image_templates:
      - "ghcr.io/jhoblitt/rgw-go:{{ .Version }}-ceph-v20.2.4"
      - "ghcr.io/jhoblitt/rgw-go:{{ .Version }}-tentacle"
      - "ghcr.io/jhoblitt/rgw-go:tentacle"
    build_flag_templates:
      - --platform=linux/amd64
      - --build-arg=CEPH_IMAGE=quay.io/ceph/ceph:v20.2.4
      - --label=org.opencontainers.image.version={{ .Version }}
      - --label=org.opencontainers.image.revision={{ .FullCommit }}
      - --label=org.opencontainers.image.created={{ .Date }}

sboms:
  - artifacts: archive

signs:
  - cmd: cosign
    signature: "${artifact}.sigstore.json"
    args: [sign-blob, "--bundle=${signature}", "${artifact}", "--yes"]
    artifacts: checksum

docker_signs:
  - cmd: cosign
    args: [sign, "${artifact}", "--yes"]
    artifacts: all

changelog:
  use: github
```

The two Ceph tags appear here and in `hack/rooket/<release>/values/rook-ceph-cluster.yaml`; a `make release-pins-check` target asserts they agree (`grep` the goreleaser file for `pinned_tag` of each release) and joins `check`, so a bump in one place fails the gate until the other follows. `release.yml` gains, before the stamp assertion, the librados install noble carries (`sudo apt-get install -y --no-install-recommends librados-dev`, the same step `ci.yml` uses: headers and the `.so` to link against are all the assertion's `go build` needs, since it never connects) and the assertion keeps `CGO_ENABLED=1`. Docker is on the runner; the `goreleaser` step needs no new permissions.

- [ ] **Step 1: Edit; `goreleaser check`; snapshot build without publishing**

```sh
goreleaser check
goreleaser release --snapshot --clean --skip=publish,sign,sbom
ls dist/
podman run --rm ghcr.io/jhoblitt/rgw-go:squid rgw-go version
```
Expected: two binaries, two archives, two images tagged locally; the binary in the image reports the snapshot version goreleaser stamps (`0.0.0-SNAPSHOT-<sha>` style pseudo-version) and not `(devel)`; `dist/` holds no darwin or windows artifacts. If goreleaser's `docker` step cannot find the image because it used podman's storage, set `RGW_GO_ENGINE=docker` for the snapshot run and say so in the README.

- [ ] **Step 2: `actionlint` and `pinact run --check` on `release.yml`; `make check`; commit**

`build(release): publish the derived Ceph image per release from a cgo build in its own base`

- [ ] **Step 3: Tag a pre-release once G's binary serves (`v0.1.0-rc.1`) and watch the release workflow; the images are what Task 10 will eventually pull for a published run, but Task 10 builds locally and does not wait for this.**

---

### Task 10: Rook nightly workflow on the phase-1 subset

**Files:**
- Create: `.github/workflows/rook.yml`, `hack/rook/phase1.patch`, `hack/rook/set-image.sh`, `hack/rook/README.md`
- Modify: `README.md`

**Interfaces:**
- Consumes: Task 8's `build.sh`; Rook at `ROOK_COMMIT` (`dc7829268` today, `tests/integration/ceph_object_test.go`, `tests/framework/installer/ceph_installer.go`, `.github/workflows/integration-test-setup-cluster-resources/action.yaml`, `tests/scripts/github-action-helper.sh`, `tests/scripts/collect-logs.sh`); G's binary answering Rook's argv and readiness probe.
- Produces: a nightly run per release and TLS pass of `CephObjectSuite/TestWithoutTLS` and `TestWithTLS` on kind against the derived image, with Rook's logs as an artifact on failure.

`set-image.sh` (decision D2):

```sh
#!/usr/bin/env bash
# Points Rook's e2e installer at IMAGE for RELEASE by rewriting the test-image
# constant in the Rook checkout at DIR: the installer picks the Ceph image only
# from those constants (tests/framework/installer/ceph_installer.go,
# ReturnCephVersion). It fails when the constant is not found in the expected
# form, so a renamed constant cannot leave the suite on stock Ceph.
# Usage: set-image.sh squid|tentacle IMAGE [DIR]
set -euo pipefail
release=${1:?release}; image=${2:?image}; dir=${3:-.}
case "${release}" in squid) const=squidTestImage ;; tentacle) const=tentacleTestImage ;; *) echo "set-image.sh: release ${release}" >&2; exit 1 ;; esac
f=${dir}/tests/framework/installer/ceph_installer.go
grep -qE "^[[:space:]]*${const}[[:space:]]*=[[:space:]]*\"quay.io/ceph/ceph:v[0-9]+\"" "${f}" ||
	{ echo "set-image.sh: ${const} = \"quay.io/ceph/ceph:vNN\" not found in ${f}; Rook changed, update this script" >&2; exit 1; }
sed -i -E "s|^([[:space:]]*${const}[[:space:]]*=[[:space:]]*)\"[^\"]+\"|\1\"${image}\"|" "${f}"
grep -qF "${const} = \"${image}\"" "${f}" || grep -qF "= \"${image}\"" "${f}"
(cd "${dir}" && go vet ./tests/framework/installer/)
echo "${f}: ${const} = ${image}"
```

`phase1.patch` is `git diff` of the Rook checkout after deleting, in `tests/integration/ceph_object_test.go`, the imports `bucketlifecycle`, `notification` and `topickafka` and the calls `bucketlifecycle.TestObjectBucketClaimLifecycle(...)`, `topickafka.TestBucketTopicKafka(...)` and `notification.TestBucketNotification(...)`, leaving the nine calls of the subset (decision D1); `hack/rook/README.md` states the subset, why, that the full suite is the phase-3 gate, and how to regenerate the patch when `ROOK_COMMIT` moves (`git apply --check` fails loudly on drift).

`rook.yml`:

```yaml
# The Rook gate: Rook's object suite, minus the lifecycle, Kafka topic and
# notification tests of features not implemented yet
# (hack/rook/phase1.patch), on kind against the derived image with rgw-go
# as radosgw. Rook is checked out at the workspace root because its setup
# action runs its helper scripts from there; rgw-go sits under rgw-go/.
# Nightly and on demand; it does not gate pull requests.
name: rook

on:
  schedule:
    - cron: "41 6 * * *"
  workflow_dispatch:

permissions:
  contents: read

concurrency:
  group: ${{ github.workflow }}-${{ github.ref }}-${{ github.event_name }}
  cancel-in-progress: true

env:
  # rook/rook main, bumped by hand with hack/rook/phase1.patch regenerated.
  ROOK_COMMIT: dc782926879ad88872d2f2f808143be0d6ca052b
  # The kind node version Rook's own callers pass to its setup action.
  KUBERNETES_VERSION: v1.37.0
  # go-ceph's rgw/admin account APIs, as Rook's CI sets it.
  GOFLAGS: "-tags=ceph_preview"

jobs:
  object-suite:
    name: ${{ matrix.release }} ${{ matrix.pass }}
    runs-on: ubuntu-latest
    timeout-minutes: 150
    strategy:
      fail-fast: false
      matrix:
        release: [squid, tentacle]
        pass: [TestWithoutTLS, TestWithTLS]
    env:
      RELEASE: ${{ matrix.release }}
      PASS: ${{ matrix.pass }}
    steps:
      - name: Check out Rook
        uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with:
          repository: rook/rook
          ref: ${{ env.ROOK_COMMIT }}
          fetch-depth: 0
          persist-credentials: false

      - name: Check out rgw-go
        uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with:
          path: rgw-go
          fetch-depth: 0
          persist-credentials: false

      # Rook's own setup: disk space, Go, the kind cluster, node prep, host
      # routes, the OSD disk, and the Rook build loaded into the cluster.
      - name: Set up the cluster the way Rook's CI does
        uses: ./.github/workflows/integration-test-setup-cluster-resources
        with:
          kubernetes-version: ${{ env.KUBERNETES_VERSION }}

      - name: Build the derived image
        working-directory: rgw-go
        run: |
          image=$(RGW_GO_ENGINE=docker hack/image/build.sh "${RELEASE}")
          echo "IMAGE=${image}" >>"$GITHUB_ENV"

      - name: Load the derived image into the kind node
        run: tests/scripts/github-action-helper.sh load_image_into_cluster "${IMAGE}"

      - name: Reduce the suite to the phase 1 subset and point it at the image
        run: |
          git apply --check rgw-go/hack/rook/phase1.patch
          git apply rgw-go/hack/rook/phase1.patch
          rgw-go/hack/rook/set-image.sh "${RELEASE}" "${IMAGE}" .

      - name: TestCephObjectSuite
        run: |
          DEVICE_FILTER=$(tests/scripts/github-action-helper.sh find_extra_block_dev)
          export DEVICE_FILTER
          SKIP_CLEANUP_POLICY=false CEPH_SUITE_VERSION="${RELEASE}" go test -v -timeout 2400s -failfast \
            -run "CephObjectSuite/${PASS}" github.com/rook/rook/tests/integration

      # A pass against stock Ceph would be no gate at all.
      - name: Assert the cluster ran the derived image
        if: always()
        run: |
          got=$(kubectl -n object-ns get cephcluster -o jsonpath='{.items[0].spec.cephVersion.image}' 2>/dev/null || true)
          echo "cephVersion.image=${got}"
          [ "${got}" = "${IMAGE}" ] || { echo "the suite did not run the derived image"; exit 1; }

      - name: Collect Rook's logs
        if: always()
        run: |
          export LOG_DIR="${GITHUB_WORKSPACE}/tests/integration/_output/tests/"
          export CLUSTER_NAMESPACE="object-ns"
          export OPERATOR_NAMESPACE="object-ns-system"
          tests/scripts/collect-logs.sh

      - name: Upload Rook's logs
        if: failure() || cancelled()
        uses: actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a # v7.0.1
        with:
          name: rook-${{ matrix.release }}-${{ matrix.pass }}
          path: tests/integration/_output/tests/
          retention-days: 14
          if-no-files-found: ignore
```

The `TestWithoutTLS` and `TestWithTLS` passes, the `object-ns` namespaces, `SKIP_CLEANUP_POLICY=false`, `-failfast` and the timeout are Rook's own invocation ([`.github/workflows/ceph-suite-integration-test.yml:133-138`](https://github.com/rook/rook/blob/v1.20.7/.github/workflows/ceph-suite-integration-test.yml#L133-L138)). The suite deletes its cluster on the way out (`SKIP_CLEANUP_POLICY=false`), so the image assertion runs `if: always()` and tolerates an absent CephCluster only by failing, which is the point: a run whose cluster is gone before the check is a run that could not be checked, and the assertion is moved before `TearDownSuite` if that turns out to be the ordinary case (Rook's `AfterTest` collects the operator log, so a `kubectl get cephcluster` inside a `-run` filter's last subtest is not available; the fallback is `grep -F "${IMAGE}" tests/integration/_output/tests/*operator*.log`, which `collect-logs.sh` gathers). The first dispatch settles which.

- [ ] **Step 1: Write `set-image.sh`, generate `phase1.patch` from a Rook scratch checkout at `ROOK_COMMIT`, prove both** (no cluster)

```sh
git -C /home/jhoblitt/github/rook worktree add "$TMPDIR/rook-p1" dc7829268
cd "$TMPDIR/rook-p1" && git apply --check hack/rook/phase1.patch && git apply hack/rook/phase1.patch
grep -c "sharedObjectStore)" tests/integration/ceph_object_test.go     # 12 calls before, 9 after
hack/rook/set-image.sh squid ghcr.io/jhoblitt/rgw-go:local-v19.2.6 .
go vet ./tests/integration/ ./tests/framework/installer/
git -C /home/jhoblitt/github/rook worktree remove --force "$TMPDIR/rook-p1"
```
Expected: 9 calls; `set-image.sh` prints the rewritten line; `go vet` clean. Also `hack/rook/set-image.sh squid x .` against a copy whose constant was renamed exits 1 with the "Rook changed" message (Review Focus 3).

- [ ] **Step 2: Write `rook.yml`; `actionlint`; `pinact run --check`; commit**

`ci: add the nightly Rook object-suite gate on the phase 1 subset against the derived image`

- [ ] **Step 3 (wave 2, once G's binary starts under Rook's argv): dispatch once**

```sh
gh workflow run rook.yml --ref "$(git branch --show-current)"
```
Expected at this stage: the derived image builds and loads, the CephCluster comes up (mons, mgr, OSD are Ceph's own binaries), the RGW deployment reaches readiness through G's probe answer, and the sub-tests fail where M, R, W and N have not landed. The run proves the harness end to end; the failures are the other units' burndown list. Record the run URL in `hack/rook/README.md`.

- [ ] **Step 4 (wave 5, after N): the first green run on both releases and both passes is the phase-1 Rook gate; record it in `docs/benchmarks/README.md`'s gate table and the PR that closes phase 1.**

---

### Task 11: rgw-go on the rooket cluster

**Files:**
- Create: `hack/rooket/rgw-go-up.sh`, `hack/rooket/rgw-go-down.sh`
- Modify: `Makefile` (targets `rgw-go-up`, `rgw-go-down`), `hack/rooket/README.md`, `hack/rooket/diag.sh` (collect `rgw-go.log`)

**Interfaces:**
- Consumes: `cmd/rgw-go` from G (`rgw-go serve -- <radosgw argv>`, `--rados-completions`, `--metrics-addr`); M, R, W for a store that serves objects.
- Produces: `hack/rooket/out/<release>/rgw-go.endpoint` (`http://127.0.0.1:7481`), `rgw-go.pid`, `rgw-go.log`, `rgw-go.metrics` (`127.0.0.1:9481`), `ceph.client.rgw.rgw-go.keyring`; the parity options set for both gateways' entities.

`rgw-go-up.sh RELEASE [rgw-go flags...]`: builds `bin/rgw-go` (`go build "-tags=$GO_TAGS" -o bin/rgw-go ./cmd/rgw-go`, cgo, with the machine's `CGO_*` facts from the environment); creates the entity once, `ceph_cmd auth get-or-create client.rgw.rgw-go mon 'allow rw' osd 'allow rwx'` (radosgw's caps under Rook) into `${out}/ceph.client.rgw.rgw-go.keyring`; sets, for `client.rgw.rgw-go` and for the radosgw pod's entity (Task 6's `entity` derivation, its `config show` check reused from a shared function in `lib.sh`), the parity options: `rgw_dynamic_resharding=false`, `rgw_enable_gc_threads=false`, `rgw_enable_lc_threads=false`, `rgw_run_sync_thread=false`, `rgw_d3n_l1_local_datacache_enabled=false`, and Task 6's three usage options, restarting the radosgw deployment when any value changed (compare `config get` before and after); starts

```sh
RGW_GO_LOG_FORMAT=json nohup bin/rgw-go serve "$@" -- \
	--id rgw.rgw-go --keyring "${out}/ceph.client.rgw.rgw-go.keyring" -c "${out}/ceph.conf" \
	--rgw-realm="${realm}" --rgw-zonegroup="${zonegroup}" --rgw-zone="${zone}" \
	--rgw-frontends="beast port=${RGW_GO_PORT:-7481}" \
	>"${out}/rgw-go.log" 2>&1 &
echo $! >"${out}/rgw-go.pid"
```

with `--metrics-addr=127.0.0.1:${RGW_GO_METRICS_PORT:-9481}` among the rgw-go flags by default, waits `curl -fsS --retry 30 --retry-delay 1 --retry-connrefused "http://127.0.0.1:${port}/"` (radosgw answers an anonymous GET on the root with 200, which is why Rook's probe works and why `up.sh` curls it), and writes `rgw-go.endpoint`. `rgw-go-down.sh` kills the pid with `TERM`, waits up to 30 s for the drain, and removes the pid and endpoint files. `diag.sh` copies `rgw-go.log` when present.

- [ ] **Step 1: Write both scripts; `shellcheck`; run against Squid** (cluster-backed)

```sh
make rgw-go-up RELEASE=squid
curl -sS -o /dev/null -w '%{http_code}\n' "$(cat hack/rooket/out/squid/rgw-go.endpoint)/"
AWS_ACCESS_KEY_ID=$(jq -r '.users[0].access_key' hack/rooket/out/squid/manifest.json) \
AWS_SECRET_ACCESS_KEY=$(jq -r '.users[0].secret_key' hack/rooket/out/squid/manifest.json) \
  aws --endpoint-url "$(cat hack/rooket/out/squid/rgw-go.endpoint)" s3api head-object --bucket plain --key small.bin
make rgw-go-down RELEASE=squid
```
Expected: 200; the head of an object radosgw wrote, served by rgw-go (R's oracle in one line); a clean stop.

- [ ] **Step 2: Commit**

`feat(rooket): run rgw-go on the host against a rooket cluster under the parity settings`

---

### Task 12: s3-tests parity against rgw-go

**Files:**
- Create: `test/s3tests/known-differences-squid.txt` (its header and one entry, below) and `test/s3tests/known-differences-tentacle.txt` (its header alone), the known-difference lists `parity diff -known` reads
- Modify: `Makefile` (target `s3tests-parity`), `hack/rooket/README.md`

**Interfaces:**
- Consumes: Tasks 4, 5, 11; A, Z, M, R, W, P.
- Produces: `make s3tests-parity RELEASE=<r>`: runs the set against rgw-go, records, diffs against `test/s3tests/baseline/<r>.json`, exit 0 on parity.

```make
.PHONY: s3tests-parity
s3tests-parity: need-release ## Run the phase 1 s3-tests set against rgw-go and compare with radosgw's recorded baseline
	$(MAKE) s3tests RELEASE=$(RELEASE) GATEWAY=rgw-go RUN=parity
	go run ./hack/parity record -format junit -suite s3tests -out $(CLUSTER_OUT)/s3tests-rgw-go.json \
	  -meta release=$(RELEASE) -meta ceph_version=$$(jq -r .ceph_version $(CLUSTER_OUT)/manifest.json) -meta gateway=rgw-go \
	  -meta s3tests_commit=$$(hack/s3tests/run.sh --print-commit) -meta deselect=$$(hack/s3tests/run.sh --print-deselect) \
	  hack/s3tests/out/$(RELEASE)-rgw-go-parity.xml
	go run ./hack/parity diff -baseline test/s3tests/baseline/$(RELEASE).json -candidate $(CLUSTER_OUT)/s3tests-rgw-go.json -known test/s3tests/known-differences-$(RELEASE).txt
```

`test/s3tests/known-differences-<release>.txt` is the list of tests whose outcome is expected to differ from radosgw's by a difference the plan set decides and `docs/exclusions.md` records, one regular expression per line over the test ids `hack/parity` records (`classname::name`, so `s3tests.functional.test_s3::<test>`), each under a `#` comment line giving its reason; `parity diff -known` (Task 5) skips them and fails on a stale entry. A test the phase boundary puts out of reach is not a difference: it is on Task 4's deselect list for its phase and runs on neither gateway. `test_versioning_multi_object_delete_with_marker_create` ([s3tests/functional/test_s3.py:8212-8232](https://github.com/ceph/ceph/blob/v20.2.4/src/test/rgw/s3-tests/s3tests/functional/test_s3.py#L8212-L8232) at the pinned commit), which needs bucket versioning and carries only `fails_on_dbstore`, is one of them, on the phase 2 list. Both files open with the same header:

```
# Tests whose outcome is expected to differ from radosgw's on this release,
# each by a decided difference that docs/exclusions.md records: one regular
# expression per line over hack/parity's test ids, under a comment giving
# the reason. A test that needs a later phase's feature is not listed here;
# hack/s3tests/deselect-phase<N>.txt removes it on both gateways.
```

The Tentacle list is that header alone. The Squid list adds owner decision 11's entry (R-D1 kept), whose difference R Task 7 records in `docs/exclusions.md`:

```
# a Squid radosgw has no GetObjectAttributes and answers ?attributes with the object body; rgw-go serves GetObjectAttributes on both releases
^s3tests\.functional\.test_s3::test_get_object_attributes$
```

A difference that is not decided is a bug for the owning unit, never a new line here; a test that needs a later phase's feature goes on Task 4's list for that phase, never here.

- [ ] **Step 1: Run on Squid with rgw-go up** (cluster-backed)

```sh
make gate RELEASE=squid && make rgw-go-up RELEASE=squid
make s3tests-parity RELEASE=squid
```
Expected on the first run: a list of differences, each a test rgw-go passes or fails unlike radosgw; each is a bug report to A, Z, M, R, W or P by test name, filed against the unit (a `test_headers.py` difference is A's, `test_bucket_policy*` Z's, listing M's, `test_object_*` R's or W's, `test_multipart_*` P's). The gate passes when the list is empty on both releases. The unstable list stays what radosgw's runs made it; rgw-go never adds to it.

- [ ] **Step 2: Tentacle**

- [ ] **Step 3: Commit** `test(s3tests): compare rgw-go's phase 1 set with radosgw's baseline`, and, when green, add the s3-tests parity line to `docs/benchmarks/README.md`'s gate table with the run's report paths.

---

### Task 13: Admin suite against rgw-go

**Files:**
- Modify: `Makefile` (target `admin-parity`), `hack/rooket/README.md`

**Interfaces:**
- Consumes: Tasks 6, 11; N.
- Produces: `make admin-parity RELEASE=<r>` mirroring Task 12 with `-format gotest`, `-suite admin`, `-meta go_ceph_tag=$(hack/admin/run.sh --print-tag)` and `test/admin/baseline/<r>.json`.

- [ ] **Step 1: Run on Squid; differences go to N by test name; then Tentacle** (cluster-backed)

- [ ] **Step 2: Commit** `test(admin): compare rgw-go's rgw/admin surface with radosgw's baseline`

---

### Task 14: elbencho build and the gateway comparison

**Files:**
- Create: `hack/bench/elbencho-build.sh`, `hack/bench/sample.sh`, `hack/bench/gateway.sh`, `hack/bench/httpload/main.go`, `hack/bench/httpload/main_test.go` (the no-op pipeline-baseline load)
- Modify: `hack/bench/report/main.go` (subcommand `gateway`), `hack/bench/report/main_test.go`, `hack/bench/report/testdata/gateway/` (one elbencho JSON, one `httpload` JSON, one samples CSV, one gctrace log), `Makefile` (targets `elbencho`, `bench-gateway`), `hack/rooket/README.md`

**Interfaces:**
- Consumes: Task 11 (`rgw-go-up.sh` with flags passed through; `rgw-go.pid`, `rgw-go.metrics`); G's `--rados-completions` and its metrics address serving `net/http/pprof` under `/debug/pprof/` (decision D9's minimal surface; if G's address serves only `/metrics`, this task adds the `net/http/pprof` handlers to `internal/metrics` in a one-file change, and says so in its PR); the parity settings Task 11 applies.
- Produces: `hack/bench/out/<release>/gateway-<gateway>-<date>/` with `env.json`, `config.json` (the two entities' `ceph config show` for every parity option), one `<profile>.json` and `<profile>.csv` from elbencho per profile (for `noop`, one `noop-<t>.json` per concurrency from `httpload`), `samples-<profile>.csv` (time, utime, stime, threads, rss), `cpu-<profile>.pprof` and `gctrace-<profile>.log` for rgw-go, and `REPORT.md`; `go run ./hack/bench/report gateway -dir DIR [-dir DIR...]` rendering one comparison table per profile across the given runs (radosgw, rgw-go, and rgw-rs when its session provides a run directory in the same layout).

`elbencho-build.sh`: fetches `https://github.com/jhoblitt/elbencho` at `ELBENCHO_COMMIT=0637a922cbb71d56625f553750dbfbf12e5f0167` into `hack/bench/out/elbencho-src`, runs the fork's Alpine static build (`build_helpers/docker/build_static_local.sh`'s recipe with the engine from `RGW_GO_ENGINE`, `S3_SUPPORT=1`, `BUILD_STATIC=1`; the aws-sdk-cpp download is over a gigabyte and the build takes minutes, so the source directory is kept between runs), and installs `hack/bench/out/elbencho`, verifying `elbencho --version` prints the fork's version and `elbencho --help | grep -q s3reqtimeout` proves the fork's flag is present.

`sample.sh TARGET OUT`, where TARGET is `pid:<pid>` or `pod:<name>`, appends one CSV line per second: for a pid from `/proc/<pid>/stat` (fields 14 and 15, utime and stime in ticks, converted with `getconf CLK_TCK`) and `/proc/<pid>/status` (`Threads`, `VmRSS`); for a pod through `k -n rook-ceph exec POD -- cat /proc/1/stat /proc/1/status` (Rook's rgw container runs radosgw as PID 1; the script asserts `/proc/1/comm` is `radosgw` on its first sample and dies otherwise). It stops on `TERM`.

`gateway.sh RELEASE GATEWAY [PROFILE...]`: ensures elbencho; creates the user `bench` with fixed keys through `admin`; resolves the endpoint and `api_name` as Task 4 does; writes `env.json` and `config.json`; for `rgw-go` restarts it through `rgw-go-up.sh` with `GODEBUG=gctrace=1` exported so the GC pauses land in `rgw-go.log`, which the run slices per profile by timestamps; starts the sampler; runs the profiles; stops the sampler; renders the report. Profiles, each writing `--csvfile` and `--jsonfile` into the run directory and driving elbencho with `--s3endpoints "$endpoint" --s3key "$ak" --s3secret "$sk" --s3region "$api_name" --s3nocompress --s3reqtimeout 30 --lat --latpercent --latpercent9s`:

| Profile | Command core | Answers |
|---|---|---|
| `small-put` | `-d` once to create `bench-small`, then for `t` in `1 8 32 64 128 256`: `-w -t $t -n 0 -N 4000 -s 4k -b 4k --timelimit 60 bench-small` | small-object PUT latency percentiles at rising concurrency (§11) |
| `small-get` | for the same `t`: `-r -t $t -n 0 -N 4000 -s 4k -b 4k --timelimit 60 bench-small` | small-object GET at rising concurrency; also spec §11's one-round-trip pipeline baseline (G appendix: a signed GET of an object held within its head is one composed RADOS read) |
| `noop` | for the same `t`: `go run ./hack/bench/httpload -url "$endpoint/" -threads $t -duration 60s -out noop-$t.json` — an anonymous `GET /` with no `Authorization` header | spec §11's no-op pipeline baseline under the same concurrency ladder and duration as `small-get`: G's appendix fixes the no-op handler as anonymous ListBuckets, zero RADOS operations, radosgw's own short-circuit ([`rgw_op.cc:2562`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L2562) at v19.2.6, "skipping list_buckets() for anonymous user") |
| `large-put` | `-w -t 64 -n 0 -N 8 -s 64m -b 64m --timelimit 60 bench-large` (one PUT per object, radosgw's streaming path) and `-w -t 64 -n 0 -N 8 -s 64m -b 8m --timelimit 60 bench-large-mpu` (elbencho uploads in parts when the block is smaller than the object) | large-object throughput, plain and multipart |
| `large-get` | `-r -t 64 -n 0 -N 8 -s 64m -b 4m --timelimit 60 bench-large` (4 MiB ranged reads, the tail window) and `-b 64m` (whole objects) | large-object read throughput |
| `list` | prepare `bench-list` with `-w -t 64 -n 0 -N 1600 -s 0` (about 100 000 keys), then the listing phase with elbencho's object-listing option (`--s3listobj`, with `--s3listobjpar` for the parallel variant), the exact form confirmed against `elbencho --help` of the pinned build and recorded in the run's `REPORT.md` as §11 requires of every command | listing at scale |

`hack/bench/httpload` is a small Go load generator (standard library only; elbencho issues object operations and cannot send a bare anonymous `GET /`): flags `-url`, `-threads`, `-duration`, `-out`; `threads` goroutines each loop `GET url` with no `Authorization` header over one `http.Transport` (`MaxIdleConnsPerHost = threads`, keep-alive on), read and discard the body, and record the latency; every response must be 200 with a body containing `<ListAllMyBucketsResult` (radosgw and rgw-go both answer the anonymous root GET that way, which is what Rook's probe relies on), any other status or a transport error counts as an error, and the run exits 1 when errors are non-zero — the same rule the elbencho profiles apply. It writes `{"threads", "duration_s", "requests", "errors", "ops_per_s", "lat_us": {"p50", "p99", "p999"}}`, which `report gateway` renders as the `noop` rows beside `small-get` at the same `t`. `main_test.go` runs it for one second against an `httptest.Server` that answers the empty `ListAllMyBucketsResult` and asserts the counts and percentiles are consistent, and that a 403 server makes it exit non-zero.

For `rgw-go` each profile also captures `curl -fsS -o cpu-<profile>.pprof "http://$metrics/debug/pprof/profile?seconds=60"` in the background while the 60 s phase runs. `report gateway` reads elbencho's JSON for throughput, IOPS, p50/p99/p999 (elbencho's `latpercent9s` output), error and retry counts (the fork's per-phase counters), the samples CSV for CPU seconds per request (`(utime+stime) delta / requests`), peak threads and peak RSS, and the gctrace log for GC pause count and max pause; the cgo share of `cpu-small-get.pprof` is computed with `go tool pprof -top` and folded into Task 15.

```make
.PHONY: elbencho
elbencho: ## Build the pinned jhoblitt/elbencho fork statically into hack/bench/out/elbencho
	hack/bench/elbencho-build.sh

.PHONY: bench-gateway
bench-gateway: need-release ## Run the gateway comparison profiles against GATEWAY (radosgw|rgw-go) on the RELEASE cluster
	ROOKET=$(ROOKET_BIN) hack/bench/gateway.sh $(RELEASE) $(GATEWAY) $(PROFILES)
```

- [ ] **Step 1: Write the report's `gateway` subcommand test-first** against `testdata/gateway/` (an elbencho JSON captured from a two-second local run of the pinned build against the Squid radosgw, a ten-line samples CSV, a five-line gctrace log): the table has one row per profile and concurrency with throughput, p50/p99/p999, CPU ms per request, peak threads, RSS, errors and retries, and two runs given as `-dir` render side by side with the ratio column `rgw-go/radosgw`.

- [ ] **Step 2: Build elbencho; write `sample.sh` and `gateway.sh`; `shellcheck`; run the small profiles against radosgw on Squid** (cluster-backed)

```sh
make elbencho
make bench-gateway RELEASE=squid GATEWAY=radosgw PROFILES="small-put small-get noop"
cat hack/bench/out/squid/gateway-radosgw-*/REPORT.md
```
Expected: zero errors and retries in every phase (an error count is a finding about the harness or the gateway, never noise: it is why the fork exists), latency rising with concurrency, and radosgw's thread count flat at its pool size.

- [ ] **Step 3: The same against rgw-go, all profiles, then both gateways on Tentacle**

```sh
make rgw-go-up RELEASE=squid && make bench-gateway RELEASE=squid GATEWAY=rgw-go
go run ./hack/bench/report gateway -dir hack/bench/out/squid/gateway-radosgw-<date> -dir hack/bench/out/squid/gateway-rgw-go-<date>
```
Expected: a side-by-side report; rgw-go's peak threads within `GOMAXPROCS + librados's idle count + 8` at every concurrency (spec §2); the profile's cgo share noted.

- [ ] **Step 4: Commit as a series**

`feat(bench): build the pinned elbencho fork and run the gateway comparison profiles`; `feat(bench): render gateway comparisons side by side`.

---

### Task 15: The cgo answer and `docs/benchmarks/`

**Files:**
- Create: `docs/benchmarks/<date>-gateway-<release>/` per release (the run directories of Task 14, both gateways, copied whole)
- Modify: `docs/cgo-limitations.md`, `docs/benchmarks/README.md`, `README.md`

**Interfaces:**
- Consumes: Tasks 2 and 14's run directories and reports.
- Produces: the phase-1 answer to the cgo question, with its evidence in the repository.

- [ ] **Step 1: Re-run the seam sweep once with G's in-flight limiter configured** (cluster-backed)

The limiter (decision D8, `goceph.Config.MaxInflightOps` and `MaxInflightBytes`) lands with G before W; the microbenchmark passes it through a flag (`-inflight-ops`, `-max-inflight-bytes`, added to `seam_bench_test.go` in this step, defaulting to off so Task 2's results stay reproducible) and the over-budget cell is re-run with the limiter at 100 MiB: its thread growth must now be zero in the async modes. `make bench-seam RELEASE=squid BENCH_LIMITER=1` and Tentacle; copy into `docs/benchmarks/<date>-seam-<release>-limited/`.

- [ ] **Step 2: Fold the gateway profile in**

From `cpu-small-get.pprof` of the rgw-go run on each release: the boundary cost, the submit crossing plus the delivery as `hack/bench/report` divides a profile, and the share with any cgo frame on the stack beside it, measured, not judged; from the report, rgw-go's CPU ms per request against radosgw's at 64 and 256 threads. Write `docs/cgo-limitations.md`'s `## Phase 1 measurement` section in full: the two seam criteria per mode and release with PASS or FAIL and margin and the measurements beside them, the limiter re-run, the gateway-level cgo share, the CPU-per-request comparison, the attribution "ours versus RADOS versus cgo" the spec §11 asks for — from Task 14's run directories, not from any flag (G builds no `--baseline-handler`; its appendix fixes the two baseline handlers as request shapes): the `noop` profile (anonymous `GET /`, zero RADOS operations) is the HTTP layer's cost, `small-get` (a signed GET within the head, one composed RADOS read) minus `noop` at the same `t` is RADOS plus cgo, and the boundary cost splits that remainder — and the answer in one sentence: whether the cgo boundary is a phase-1 bottleneck under the thresholds, and what a pure-Go client would and would not change (the inherent entries the registry already lists: threads, pinning, copies, the AES256KRB5 coupling). Every Inherent entry's `**Measured:**` line points at a run directory.

- [ ] **Step 3: `docs/benchmarks/README.md`**

The index table (date, kind, release, gateway build stamp, cluster, link) for every run directory; a `Method` section stating once what every run directory contains and how it is produced (`make bench-seam`, `make bench-gateway`), the parity settings and how `config.json` proves them, and the cluster description fields; a `Gates` table with the phase-1 gate results and their evidence (s3-tests parity report paths, the admin parity report, the Rook run URL from Task 10, the seam and gateway runs).

- [ ] **Step 4: Commit as a series**

`docs(bench): record the phase 1 seam and gateway benchmarks on Squid and Tentacle`; `docs(cgo): answer the phase 1 cgo question from the measurements`.

---

## Self-review

- **Spec coverage.** §9's phase-1 gates: s3-tests parity (Tasks 4, 5, 12; §10's "equal pass and fail set for the phase's groups" is the marker set plus Task 4's per-phase deselect lists, run identically on both gateways), the go-ceph rgw/admin suite (6, 13), the seam microbenchmark answering the cgo question (1, 2, 15), the pipeline baseline's load and attribution (14, 15 over G's handlers), the first gateway comparison (14), the Rook suite on the derived image (8, 10). §11: three benchmarks, the elbencho fork pinned, the recorded quantities (p50/p99/p999, throughput, CPU per request, threads, RSS, errors and retries, GC pauses and a cgo-attributed CPU profile for rgw-go), `docs/benchmarks/` with commands and cluster description (14, 15). §12: the derived image per release with the original beside it, the cgo build in a container stage on the target base, goreleaser kept for tags, archives, checksums, SBOM and signing, the stamp asserted equal to the tag (8, 9); the two non-gating workflows (7, 10). §6's op shapes are Task 1's table with their `rgw_rados.cc` sources. Unit T's population change for R's oracle (`00-index.md`, "The nine units") is Task 3. Decisions D1, D2, D10, D11 are adopted and stated with their evidence.
- **Placeholder scan.** The one deliberately open element is the exact `--s3listobj` form in Task 14, confirmed against the pinned build's `--help` and recorded in the run's report, which §11 requires of every command anyway; G declined `--baseline-handler` and pinned the baselines as request shapes instead (G appendix: no-op = anonymous `GET /`, one-round-trip = a signed within-head `GET /<bucket>/<key>`); Task 14's `small-get` is the one-round-trip shape and its `noop` profile (`hack/bench/httpload`, an anonymous `GET /` under the same ladder) is the no-op run; Task 15 reads both from the run directory.
- **Type consistency.** `seam.Shape`, `seam.Shapes`, `seam.ByName`, `seam.OSThreads`, `seam.CPUTime`, `seam.Summarize`, `seam.NewSampler`, the `Cell` JSON keys, `parity.Record`, `parity.Diff`, `parity.Result`, `parity.FormatJunit`, `parity.FormatGoTest`, the result schema's `meta` keys (`s3tests_commit`, `go_ceph_tag`, `release`, `ceph_version`, `deselect`, `gateway`), `hack/s3tests/run.sh --print-commit`, `hack/s3tests/run.sh --print-deselect`, `hack/s3tests/deselect-phase2.txt`, `hack/s3tests/deselect-phase3.txt`, `hack/admin/run.sh --print-tag`, `RGW_GO_ADMIN_ENDPOINT`, `RGW_GO_CEPH_TAG`, `RGW_GO_ENGINE`, `hack/rooket/out/<release>/rgw-go.endpoint` are spelled the same wherever they appear.
- **Review Focus.** Each of the five lines names the task whose steps pin it: 1 and 4 in Task 5's specs (4 also in Task 4's list check), 2 in Task 1's flags and Task 2's over-budget cell, 3 in Task 10's `set-image.sh` failure path and post-run assertion, 5 in Task 8's image test.
