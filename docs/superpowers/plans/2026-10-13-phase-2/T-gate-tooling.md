# Phase 2 unit T: gate tooling

Threat model: honest mistakes only. This unit is test and benchmark tooling: a selection that
silently drops or adds tests, a ledger recorded under the wrong selection or cluster mode, a
gate that skips a check. None of it is a security boundary (PP-14). Reviews do not treat a pull
request's author as an adversary, and no task hardens a check against one.

Sources: `research-writepath.md` §5.1 and §7.7 (WP-22, WP-23), `research-bucketconfig.md` §5.1
and its open question 7, `research-lock-lifecycle.md` §5 and §7's C12, all in
`research/`. rgw-go paths are at origin/main 0239625; s3-tests facts
are at the pin f6f2529dd9ef. Where this file departs from the reports, the drafting notes
say why.

## Scope

Spec §9: "Every phase gate includes writing with one gateway and reading with the other on a
shared zone, s3-tests parity with radosgw for the phase's ops, and a benchmark run." And: "Phase
1's s3-tests parity is held on every pull request, not only at the gate".

| Item | Tasks |
|---|---|
| The s3-tests selection moved from `hack/s3tests/run.sh` into Go | WP-22 |
| The include list for marker-excluded tests phase 2 serves, `hack/s3tests/include-phase2.txt` (D20; bucketconfig's open question 7) | WP-23 |
| P1's split: the time-driven lifecycle tests at the gate and on demand, the rest on every pull request | T-1 |
| C12's harness parts: `rgw_lc_debug_interval`, the LC worker per gateway under test, two plain storage classes, `storage_classes` in the conf | T-2 |
| The storage-class copy tests the include list can run (the writepath report's optional row) | T-3 |
| The benchmark run's phase 2 paths | T-4 |
| The phase 2 gate, as the index states it | T-gate |

Not in this unit: each area's recording task, which deletes its own deselect and marker lines,
adds its own include and gate-only lines and records the ledgers (V's, C12's, BC-8, BC-22,
BC-27, BC-28f, BC-34, WP-14). C12 keeps its marker retirements, its lines, its recordings, the
admin subtest and the Rook patch. This unit gives them the machinery.

## Decisions

The index's rulings this unit carries are D20 and the pending-owner default P1. The unit's own
are TD-1 to TD-12.

| Id | Decision | Reason | Tasks |
|---|---|---|---|
| D20 | An include list, `hack/s3tests/include-phase2.txt`, runs the marker-excluded tests phase 2 serves; this unit owns its machinery, and each line lands with its area's recording task. The eight ownership tests need no line: each carries `object_ownership` and no other excluded marker ([test_s3.py:21555-21653](https://github.com/ceph/ceph/blob/f6f2529dd9efccf7710c8a2b561e5a2ab9d48bd3/src/test/rgw/s3-tests/s3tests/functional/test_s3.py#L21555-L21653)), so BC-28f lifts the marker (B6). | bucketconfig's open question 7; the six encryption-configuration tests ([test_s3.py:15342-15423](https://github.com/ceph/ceph/blob/f6f2529dd9efccf7710c8a2b561e5a2ab9d48bd3/src/test/rgw/s3-tests/s3tests/functional/test_s3.py#L15342-L15423)) share `sse_s3` and `encryption` with SSE write tests that stay out. | WP-23 |
| P1 | (the owner, 2026-10-13) The time-driven lifecycle s3-tests, about 1,100 s of sleeps, run at the gate and on demand only; the lifecycle configuration and header tests run on every pull request. | lock-lifecycle Q6: the per-PR set takes about 130 s (spec §9). | T-1 |
| TD-1 | The selection, the conf's new keys and the cluster modes live in a Go command, `hack/s3tests`, beside the lists it reads. `hack/parity` stays the ledger tool and gains only `diff --selection`. | run.sh's selection is already program logic (PP-15); the reports offered hack/parity or a new command, and these are run concerns, not ledger ones. | WP-22, T-1, T-2 |
| TD-2 | run.sh keeps its usage, `--print-commit`, `--print-deselect`, its outputs, and the lines hack/parity's pin proof reads: `S3TESTS_DIR`, `TEST_FILES`, `S3TESTS_REPO` and the pin line. Its selection lines go. | `hack/parity/internal/parity/repin.go:52-54` and `hack/parity/internal/parity/pin.go:48-54` parse them, and `runsh_test.go` runs `--print-commit`. | WP-22 |
| TD-3 | The excluded markers move to `hack/s3tests/excluded-markers.txt`, one marker a line under a comment naming its feature's phase or exclusion. | Each area then deletes its own line, as with the deselect lists; data, not shell. | WP-22 |
| TD-4 | pytest runs the selected node ids explicitly, in collection order, without `-m` or `--deselect`, from WP-22 on. | Go computes the set; the include list then needs no second pytest run, so one junit report remains; collection order keeps today's run order. | WP-22 |
| TD-5 | The selection hash covers the deselect lines, as today, and the include lines, each marked `+` before sorting. With an empty include list it is today's value, `999e22cc41a0` at 0239625. Markers and gate-only lines stay out, as markers are out today. | A recorded outcome names the selection that produced it; an empty addition keeps every existing entry's value. | WP-22, WP-23 |
| TD-6 | P1's split is a list of node ids, `hack/s3tests/gate-only.txt`, not a marker. | `lifecycle_expiration` also marks the four `test_lifecycle_expiration_header_*` tests, and `lifecycle_transition` marks `test_lifecycle_set_noncurrent_transition`, a configuration test ([test_s3.py:9519-9605](https://github.com/ceph/ceph/blob/f6f2529dd9efccf7710c8a2b561e5a2ab9d48bd3/src/test/rgw/s3-tests/s3tests/functional/test_s3.py#L9519-L9605), [test_s3.py:9962](https://github.com/ceph/ceph/blob/f6f2529dd9efccf7710c8a2b561e5a2ab9d48bd3/src/test/rgw/s3-tests/s3tests/functional/test_s3.py#L9962)); P1 keeps both kinds per PR. | T-1 |
| TD-7 | A recording always runs the gate set. The per-PR comparison runs the PR set and compares only it, through `parity diff --selection`. | A full record retires every ledger test it did not run, and diff reports a baseline test the candidate lacks as missing (`hack/parity/main.go`'s package doc). | T-1 |
| TD-8 | `rgw_lc_debug_interval=10` joins the parity options for both gateways, always. | One configuration for every recorded and compared run. With the LC threads off it changes nothing outside the LC mode; s3-tests treats a day as 10 s (`lc_debug_interval`'s default, [\_\_init\_\_.py:287-290](https://github.com/ceph/ceph/blob/f6f2529dd9efccf7710c8a2b561e5a2ab9d48bd3/src/test/rgw/s3-tests/s3tests/functional/__init__.py#L287-L290)). | T-2 |
| TD-9 | The LC worker runs on the gateway under test only (or on both, for C13). radosgw's option changes through the CephObjectStore's `rgwConfig`, and radosgw restarts on each change. | Rook v1.20.7 rewrites the radosgw entity's options from `rgwConfig` in the mon store at every reconcile ([`config.go:236-250`](https://github.com/rook/rook/blob/v1.20.7/pkg/operator/ceph/object/config.go#L236-L250)), so a plain `ceph config set` would not hold; radosgw reads its LC options only at startup; with both workers on, radosgw's would do rgw-go's expirations. | T-2 |
| TD-10 | Two plain storage classes join the default placement, on STANDARD's data pool, created by the Go command. | The transition tests skip below three classes and judge classes by listing ([test_s3.py:9875-9915](https://github.com/ceph/ceph/blob/f6f2529dd9efccf7710c8a2b561e5a2ab9d48bd3/src/test/rgw/s3-tests/s3tests/functional/test_s3.py#L9875-L9915)), and `configured_storage_classes` adds STANDARD itself ([\_\_init\_\_.py:203-209](https://github.com/ceph/ceph/blob/f6f2529dd9efccf7710c8a2b561e5a2ab9d48bd3/src/test/rgw/s3-tests/s3tests/functional/__init__.py#L203-L209)); populate.sh's compression classes share that pool; populate.sh is 311 lines and is not grown (PP-15). | T-2 |
| TD-11 | The `unencrypted-unencrypted` parametrizations of test_copy_enc and test_copy_part_enc join the include list. | Phase 1's cross-class copy has no other s3-test, and the include list runs them for free (writepath §5.1's optional row). | T-3 |
| TD-12 | The phase 2 gate also re-runs phase 0's and phase 1's gates. | Phase 2 changes what both gateways write and read; an earlier gate that stops passing is a phase 2 regression. | T-gate |

## Seams

**Consumed**

- Each unit's recording task edits only its own lines of the lists below, and the controller
  lands recordings one at a time, since the ledgers are append-only: V's recording, C12, BC-8,
  BC-22, BC-27, BC-28f, BC-34, WP-14.
- C12 fills `gate-only.txt` with the time-driven lifecycle tests and records them in the
  `radosgw` LC mode.
- hack/parity's ledger semantics (record, diff, check), and hack/bench's gateway command (T-4).

**Produced**

| Seam | Task | Consumers |
|---|---|---|
| `hack/s3tests/excluded-markers.txt`, the deselect lists in the command's list of files | WP-22 | every recording task |
| `hack/s3tests/include-phase2.txt` | WP-23 | BC-27, T-3 |
| `hack/s3tests/gate-only.txt`; `make s3tests* SET=pr\|gate`; `parity diff --selection FILE` | T-1 | C12, WP-14, T-gate, `.github/workflows/s3tests.yml` through the Makefile |
| `make lc-mode LC=off\|radosgw\|rgw-go\|both`, and `LC=` on the s3-tests targets; the two plain classes; `storage_classes` in the conf | T-2 | C12, C13 (`both`), T-gate |
| `hack/internal/harness` (hack/bench's toolbox and kubectl helpers, moved) | T-2 (a) | T-2 (b), hack/bench |
| the phase 2 benchmark profiles | T-4 | T-gate |

The selection library's shape, a sketch (names are the implementer's; the behaviour binds):

```go
type ListLine struct{ ID, File string; Line int }
func ReadList(path string) ([]ListLine, error)
func MarkerExpr(markers []ListLine) string
func Select(in Input) ([]string, error) // ids in collection order; every refusal joined
func Hash(deselect, include []ListLine) string
```

`Input` holds the collected ids with and without the marker expression, the deselect, include
and gate-only lines, an optional TESTS subset, and the set, `pr` or `gate`.

## Hot files

| File | Tasks | Other units | Rule |
|---|---|---|---|
| `hack/s3tests/run.sh` | WP-22, WP-23, T-1, T-2 (b) | none | in that order; each leaves it glue |
| the `Makefile`'s s3-tests targets | WP-22, T-1, T-2 (b) | none | in that order |
| `hack/s3tests/{excluded-markers,deselect-phase2,deselect-phase3,include-phase2,gate-only}.txt` | WP-22, WP-23, T-1 create or move them; T-3 adds lines | every recording task | each task edits only its own lines; recordings land one at a time through the controller |
| `test/s3tests/baseline/*.json`, `test/s3tests/known-differences-*.txt` | T-3 | every recording task | appended through the controller, one recording at a time |
| `hack/parity/internal/parity/` (diff), `hack/parity/main.go` (its doc) | T-1 | none | single owner |
| `hack/rooket/lib.sh`, `hack/rooket/{squid,tentacle}/values/rook-ceph-cluster.yaml`, `hack/rooket/README.md` | T-2 (b) | none | single owner |
| `hack/bench/internal/harness` | T-2 (a) moves it | T-4 | T-2 (a) before T-4 |
| `hack/admin/deselect-phase2.txt`, `hack/admin/run.sh` | T-gate (a) | V's and C12's recordings delete the lines | T-gate (a) last, once the file is empty |

## Tasks

### Task WP-22: the s3-tests selection in Go

- **Layer:** test tooling. **Cluster:** no.
- **Files:** create `hack/s3tests/main.go` and its library package under `hack/s3tests/internal/`,
  with their suite; create `hack/s3tests/excluded-markers.txt`; modify `hack/s3tests/run.sh`, the
  `Makefile` (the command is built before run.sh calls it), the header comments of
  `deselect-phase2.txt` and `deselect-phase3.txt`, and the harness section of
  `hack/rooket/README.md`.
- **Consumes:** run.sh's selection at 0239625. **Produces:** the command, its library (Seams),
  and `excluded-markers.txt`.
- **Behaviour:**
  1. **Moved without change:** everything run.sh decides at `hack/s3tests/run.sh:65-97` and
     `hack/s3tests/run.sh:306-344`:
     - the marker expression, `not <m1> and not <m2> ...` in the list's order;
     - the deselect lists' parsing: `#` starts a comment, trailing blanks and blank lines drop;
     - the prefix refusal: a line that would also deselect a collected test it does not name,
       since pytest's `--deselect` matches node-id prefixes;
     - the stale refusal: a line naming no test the markers leave;
     - the TESTS-file subset: each line must be in the set, and a file naming none is refused;
     - the hash `--print-deselect` prints: the sorted lines' sha256, its first 12 hex digits.
     Each refusal keeps run.sh's message. The command lists every refusal of a run, then exits 1.
  2. **The collected ids** are the `pytest --collect-only -q` lines run.sh hands over, those
     matching `^s3tests/.+\.py::`.
  3. **pytest runs the selected ids** explicitly, in collection order (TD-4).
  4. **run.sh** keeps TD-2's interface and lines and only sequences: build the expression,
     collect, call the command, run pytest on its ids.
  5. **`excluded-markers.txt`** holds run.sh's markers in run.sh's order, each under its comment
     (TD-3).
- **Specs:**
  - each refusal as a table row, with its message;
  - the marker expression for today's list equals run.sh's;
  - the hash of the lists at 0239625 is `999e22cc41a0`, the value every ledger entry records;
  - the pin's collection, captured once into testdata (it needs the venv and a conf, no
    gateway), selects the 416 ids run.sh selects today;
  - hack/parity's `runsh_test.go` passes against the new run.sh;
  - the pull request's own s3tests check, which its paths trigger, passes on Squid with no
    difference.
- **Exclusions and registry:** none.
- **Depends on:** nothing. **PR:** one. **Brief:** about 150 lines.

### Task WP-23: the include list

- **Layer:** test tooling. **Cluster:** no.
- **Files:** the command's selection; create `hack/s3tests/include-phase2.txt` with its header
  alone; `hack/s3tests/run.sh` (a second collection).
- **Consumes:** WP-22. **Produces:** the include list's machinery (D20).
- **Behaviour:**
  1. **Two collections:** with the marker expression and without it. Both need the conf, since
     test_s3.py parses it at import to parametrize its encryption tests.
  2. **The set** is (marker-selected ∪ include) − deselect, in collection order.
  3. **An include line** names a collected test by its id, or a base id for all its
     parametrizations, and it must be a test the markers exclude. Any other line is stale and
     refused: removed, renamed, or already selected by the markers.
  4. **An include line and a deselect line** naming the same test are refused.
  5. **The hash** covers the include lines (TD-5).
  6. **The file's header** gives the format the deselect lists use (a node id, then `#` and
     why), and the rule that each line sits under its area's comment and lands with that area's
     recording task. BC-27's six encryption-configuration tests are the first lines.
- **Specs:** the union; each stale kind; the overlap; a base id against a parametrization id;
  the hash with an empty list equal to WP-22's; the pull request's s3tests check unchanged with
  the empty list.
- **Exclusions and registry:** none.
- **Depends on:** WP-22. **PR:** one. **Brief:** about 100 lines.

### Task T-1: the per-PR and gate sets

- **Layer:** test tooling. **Cluster:** no.
- **Files:** the command's sets; create `hack/s3tests/gate-only.txt` with its header alone;
  modify `hack/parity/internal/parity/` (`diff --selection`), `hack/parity/main.go`'s package
  doc, and the `Makefile`'s s3-tests targets.
- **Consumes:** WP-23. **Produces:** `SET=pr|gate`, `gate-only.txt`, `parity diff --selection`
  (Seams).
- **Behaviour:**
  1. **The sets.** The gate set is WP-23's. The PR set is the gate set less the gate-only lines.
     A gate-only line names a test of the gate set, by id or base id, or it is stale and
     refused.
  2. **Recordings** (`make s3tests-record`, `s3tests-record-tests`) always run the gate set
     (TD-7). A TESTS file there may name gate-only tests.
  3. **`make s3tests-parity`** runs the PR set unless `SET=gate`. Each run writes its selected
     ids beside its report, `hack/s3tests/out/<release>-<gateway>-<run>.ids`.
     `.github/workflows/s3tests.yml` keeps calling the target, so the per-PR check runs the PR
     set.
  4. **`parity diff --selection FILE`** reads FILE's node ids and maps each to the ledger's id as
     junit names it, `classname::name` (`hack/parity/internal/parity/runfile.go:80`). A ledger
     test outside the selection is not compared and prints nothing. Every other rule holds: a
     selected test the candidate lacks is "missing", one the ledger lacks "no baseline entry".
  5. **Known differences under `--selection`.** An entry that matches no compared test but
     matches a ledger test outside the selection is not stale. One that matches nothing in
     either still fails the run.
- **Specs:** both sets; a stale gate-only line; diff with `--selection`: an unselected baseline
  test not compared, a selected test still compared both ways, a selected missing test still
  failing; the known-differences rule; a full record of the gate set retiring no gate-only
  test; the hash unchanged by gate-only lines.
- **Exclusions and registry:** none.
- **Depends on:** WP-23. **PR:** one. **Brief:** about 140 lines.

### Task T-2: the lifecycle runs' cluster configuration

- **Layer:** cluster tooling. **Cluster:** yes.
- **Files:**
  - (a) move `hack/bench/internal/harness` to `hack/internal/harness`, updating its importers,
    with no behaviour change;
  - (b) `hack/rooket/lib.sh` (`parity_options`), both releases' `values/rook-ceph-cluster.yaml`
    (`rgwConfig`), the command's `lc-mode` and `classes` subcommands, the `Makefile` (`LC=`,
    `make lc-mode`), `hack/s3tests/run.sh` (the conf's one new line), `hack/rooket/README.md`.
- **Consumes:** WP-22; T-1's sets. **Produces:** the modes, the classes and the conf key
  (Seams).
- **Behaviour:**
  1. **The debug interval (TD-8).** `rgw_lc_debug_interval=10` joins `parity_options`
     (`hack/rooket/lib.sh:17-18`) and both chart values' `rgwConfig`, which `make
     parity-options-check` holds equal. s3-tests' `lc_debug_interval` stays at its default.
  2. **The LC modes (TD-9):** `off` (the parity mode), `radosgw`, `rgw-go`, and `both` for C13.
     - radosgw's `rgw_enable_lc_threads` changes through the CephObjectStore's
       `spec.gateway.rgwConfig`. The switch then waits until the service map shows a radosgw
       started after the change, as `hack/rooket/rgw-go-up.sh:119-153` waits for the parity
       options.
     - rgw-go's own entity takes its option, and rgw-go restarts through `make rgw-go-up`.
     - `make lc-mode LC=off` restores the parity mode. Each s3-tests target sets the mode it
       names first, and each record and parity run carries it as `--meta lc=<mode>`.
  3. **The classes (TD-10).** Two plain classes join the zonegroup's and the zone's default
     placement on STANDARD's data pool, with no compression, idempotently, with a period commit
     only when something changed, as populate.sh does for its classes.
  4. **The conf.** `storage_classes` in `[s3 main]` names the two classes; s3-tests adds STANDARD
     itself. run.sh gains only the call that prints the names and the conf line that writes
     them.
  5. **Proof, on both releases:**
     - each mode leaves the LC threads on exactly where it says, read from the running daemons;
     - the phase 1 set recorded against radosgw in the `radosgw` mode equals the committed
       ledger, which TD-7's recordings in that mode rely on;
     - a transition test run through a TESTS file is collected with three classes and no longer
       skips.
- **Specs:** the mode decisions on the harness package's fake execer; the proof runs above,
  written into the task's notes with their run ids.
- **Exclusions and registry:** none; the README records the modes as harness configuration.
- **Depends on:** WP-22 for (a) and (b); T-1 for (b). **PR split:** (a) the harness move; (b)
  the options, modes, classes and conf. **Brief:** about 200 lines.

### Task T-3: storage-class copies on the include list

- **Layer:** recording. **Cluster:** yes.
- **Files:** `hack/s3tests/include-phase2.txt` (this unit's lines); both s3-tests ledgers;
  known-differences files where a recorded difference has an exclusion.
- **Consumes:** WP-23, T-2's classes. **Produces:** 45 recorded tests (TD-11).
- **Behaviour:**
  1. Add the `unencrypted-unencrypted-<source class>-<destination class>-<size>`
     parametrizations of test_copy_enc and test_copy_part_enc
     ([test_s3.py:21943-22027](https://github.com/ceph/ceph/blob/f6f2529dd9efccf7710c8a2b561e5a2ab9d48bd3/src/test/rgw/s3-tests/s3tests/functional/test_s3.py#L21943-L22027)) over T-2's three classes: 36 ids and 9.
  2. Record both ledgers with `make s3tests-record-tests`, then run rgw-go's parity on both
     releases.
  3. A difference with an exclusion gets its known-differences line. One without is a finding
     against phase 1's copy, fixed in this PR or turned into a numbered task before it merges
     (PP-12).
- **Specs:** the ledger checks; `make s3tests-parity` clean on both releases.
- **Exclusions and registry:** none expected.
- **Depends on:** WP-23, T-2. **PR:** one, landed by the controller with the other recordings.
  **Brief:** about 60 lines.

### Task T-4: benchmark profiles for the phase 2 paths

- **Layer:** benchmark tooling. **Cluster:** yes, for its smoke runs.
- **Files:** `hack/bench/gateway` (profiles and their plan goldens); a small Go driver in
  `hack/bench` for any path elbencho cannot drive.
- **Consumes:** phase 1's gateway comparison; `hack/internal/harness`. **Produces:** profiles
  T-gate runs.
- **Behaviour:**
  1. **The paths,** against both gateways under the parity options: PUT and GET of small and
     large objects through a compressing class; PUT carrying a CRC32 checksum in the form the
     AWS SDKs send by default; PUT and GET on a versioned bucket.
  2. **What drives them.** The brief establishes, from the pinned elbencho fork commit
     (`0637a922cbb7`), which of these elbencho can send. A path it cannot drive gets a Go driver
     shaped like `hack/bench/httpload/main.go`. No option is assumed from memory.
  3. **Setup** (the versioned bucket, the class) runs as unmeasured prep phases, as phase 1's
     profiles prepare theirs.
- **Specs:** the profile plans as goldens, as phase 1's plan testdata are; a smoke run per
  gateway on Squid.
- **Exclusions and registry:** none.
- **Depends on:** V's versioned writes (V7a, V11), WP-7, WP-8, WP-10 (b); T-2 (a). **PR:** one.
  **Brief:** about 150 lines.

### Task T-gate: the phase 2 gate

- **Layer:** gate. **Cluster:** yes.
- **Files:**
  - (a) delete `hack/s3tests/deselect-phase2.txt` and `hack/admin/deselect-phase2.txt`, with
    their entries in the command's list of files and in `hack/admin/run.sh`'s `DESELECT_LISTS`,
    once both are empty ("A phase deletes its lines as it lands its features, and its file and
    entry here with the last of them", `hack/s3tests/run.sh:74-76`);
  - (b) the gate record, `gate.md` in the phase 2 plan's directory;
  - (c) the benchmark run's directories under `docs/benchmarks/` and their index lines.
- **Consumes:** every unit's merged tasks. **Produces:** phase 2's close.
- **Behaviour,** each check on Squid and then Tentacle, as phase 1's gates run:
  1. **Preconditions.**
     - Every task of every unit has merged, and every recording task's ledger appends have
       landed.
     - Neither phase 2 deselect list holds a line.
     - `excluded-markers.txt` holds no marker of a phase 2 feature: checksum, object_ownership,
       lifecycle, lifecycle_expiration, lifecycle_transition, object_lock and versioning's.
       Each remaining line names its exclusion or later phase.
     - Every material change to `docs/exclusions.md` in phase 2 was announced to the rgw-rs
       session.
  2. **s3-tests parity:** `make s3tests-parity SET=gate LC=rgw-go` on each release: phase 1's
     set, the tests phase 2 freed, the include list and P1's time-driven tests. It is compared
     with the committed ledgers and each release's known-differences file, with no unexpected
     difference.
  3. **The go-ceph rgw/admin suite:** `make admin-parity` on each release, with no phase 2
     deselect left.
  4. **The Rook suite:** `rook.yml`, on demand, green on each release at the gate's commit, with
     `bucket/lifecycle` back in it (C12 drops it from the patch).
  5. **The cross-gateway cases:** `make integration` with the `phase2` label on each release (V15b,
     C13 in the `both` mode, BC-8, BC-16, BC-22, BC-27, BC-28f, BC-34, WP-15, WP-24); and phase 0's
     and phase 1's gates, `make gate`, `make read-gate` and `make admin-gate` (TD-12).
  6. **Zero open findings:** the controller's ledger lists none, and every finding a review raised
     is fixed in its PR or is a merged numbered task (PP-12).
  7. **The benchmark run:** T-4's profiles and phase 1's, against both gateways on each release,
     on a quiet host. Each run directory goes under `docs/benchmarks/` with its commands, the
     cluster description and the gateway's build stamp, phase 1's documentation rule.
  8. **The registry:** every phase 2 entry in `docs/ceph-upstream-bugs.md` carries its
     reproduction status, and each one left unreproduced says why.
  9. **The record:** `gate.md` lists each check with its command, release, commit and result (a
     workflow run or a log path).
- **Specs:** the checks above.
- **Exclusions and registry:** none of its own.
- **Depends on:** every task of every unit. **PR split:** (a) the empty lists' removal; (b) the
  gate record; (c) the benchmark results. **Brief:** about 120 lines.

### Task T-5: s3-tests, `_has_target_object_key_format` reports what it probes

Found by F5's task review (Minor 2), and numbered here before F5 merged (PP-12).

- **Layer:** s3-tests, on jhoblitt/ceph's `rgw-go-s3tests` branch. **Cluster:** no.
- **Files:** src/test/rgw/s3-tests/s3tests/functional/test_s3.py (`_has_target_object_key_format`),
  merged into `rgw-go-s3tests` by PR; docs/ceph-upstream-bugs.md (a new entry).
- **Behaviour:**
  1. The helper puts a bucket logging configuration with `TargetObjectKeyFormat` on a bucket that
     does not exist, and is meant to answer False when botocore refuses the parameter. Its
     `finally: return True` overrides the `except` branch's `return False` and swallows every other
     exception, so it always answers True ([test_s3.py:16439-16453](https://github.com/ceph/ceph/blob/d8552606423c86efe4df2d39c0b7e4b20ba69c73/src/test/rgw/s3-tests/s3tests/functional/test_s3.py#L16439-L16453), at ceph main
     d8552606423). Its callers ([test_s3.py:16563](https://github.com/ceph/ceph/blob/d8552606423c86efe4df2d39c0b7e4b20ba69c73/src/test/rgw/s3-tests/s3tests/functional/test_s3.py#L16563), [test_s3.py:17603](https://github.com/ceph/ceph/blob/d8552606423c86efe4df2d39c0b7e4b20ba69c73/src/test/rgw/s3-tests/s3tests/functional/test_s3.py#L17603)) never
     take the feature-absent path.
  2. The fix answers False on `ParamValidationError` and True once the request reaches the server,
     whatever the server answers, with nothing returned from `finally`.
- **Specs:** none in rgw-go. The changed module compiles and collects (`py_compile`, pytest
  `--collect-only`).
- **Exclusions and registry:** a new registry entry for the helper. It is not security-relevant, and
  its Upstream line is pending. rgw-go's pin does not move here.
- **Depends on:** nothing. **PR:** the s3-tests PR into `rgw-go-s3tests`, and the registry entry
  in its own rgw-go PR. **Brief:** about 25 lines.

## Order

The unit starts at once (index, Order 2).

| Wave | Tasks | Waits for |
|---|---|---|
| 1, in parallel | WP-22, T-2 (a) | nothing |
| 2 | WP-23 | WP-22 |
| 3 | T-1 | WP-23 |
| cluster, one at a time | T-2 (b), then T-3 | T-1; T-2 (b) and WP-23 |
| any time after its inputs | T-4 (its smoke run is a cluster slot) | V7a, V11, WP-7, WP-8, WP-10 (b), T-2 (a) |
| any time | T-5 | nothing |
| last | T-gate (a), (b), (c) | every task of every unit |

The cluster tasks share the plan's one-at-a-time slot with every unit's.

Cross-unit order this unit imposes:
- Every recording task lands after WP-22, so that it edits the lists in their new home: V's
  recording, C12, BC-8, BC-22, BC-28f, BC-34 and WP-14.
- BC-27 (b) and T-3 land after WP-23.
- WP-14 and C12's lifecycle lines land after T-1; C12's worker recording after T-2 (b).
- C13's mixed-worker cases use T-2 (b)'s `both` mode.

## Defects

None. This unit's tooling meets no radosgw defect; T-gate checks that every other unit's
registry entries carry their reproduction status. Two harness facts, recorded so no task
re-derives them:
- The lifecycle worker markers do not separate the time-driven tests from the configuration and
  header tests (TD-6).
- A plain `ceph config set` on radosgw's entity does not survive the Rook operator's next
  reconcile of `rgwConfig` (TD-9).
