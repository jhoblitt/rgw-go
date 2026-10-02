# go-ceph and cgo limitations

This registry records every limitation rgw-go hits in its RADOS client that
comes from go-ceph, librados's C API, or the cgo boundary. It is the input to
the phase 1 question the design spec poses: whether a pure-Go RADOS client
would remove a real bottleneck or cost.

Each entry names its class:

- **Inherent** means the limit comes from librados, its C API, or cgo itself.
  Only a pure-Go client removes it.
- **Binding** means go-ceph's Go wrapper is missing something or gets it wrong.
  The jhoblitt/go-ceph fork can fix it, so it is not an argument for a rewrite.

Each entry also records how it was found, its status, and what the phase 1
benchmark should measure. Add an entry whenever a task, review or benchmark
finds a new limit, and update the status when one is fixed or measured.

## Phase 1 measurement

**Preliminary, measured on a busy host.** A re-run of the same
`make bench-seam` on a quiet host follows and replaces these numbers.
Preliminary: the gateway-level profile of Task 15 completes this.

The seam microbenchmark and its `rados bench` floor ran on 2026-10-01 with
`make bench-seam` at rgw-go fcd09fb, Squid from 22:00:41 to 23:01:35 UTC and
Tentacle from 23:02:38 to 00:00:15 UTC; each release's sync CPU profile was
taken after its sweep, at 00:01:38 and 00:00:54 UTC. The run directories are
[2026-10-01-seam-squid](benchmarks/2026-10-01-seam-squid/REPORT.md) and
[2026-10-01-seam-tentacle](benchmarks/2026-10-01-seam-tentacle/REPORT.md).

The host was not quiet. Besides the two clusters, a desktop session
(Firefox, Thunderbird, gnome-shell) and other sessions' Go builds, test
binaries and golangci-lint ran during the sweeps. The 1-minute load, sampled
every 15 s, had a median of 4.05 and a maximum of 7.30 during Squid's sweep,
and a median of 5.30 and a maximum of 43.22 during Tentacle's, when a build,
its test binaries and golangci-lint ran from 23:14:36 to 23:17:55 UTC (sync
mode's headwrite4k and indexrtt cells) and golangci-lint took up to 12 CPUs
at 23:25:49 UTC (callback write4k at 512).

| Conditions | Squid | Tentacle |
|---|---|---|
| Ceph, the cluster's and rados bench's image | 19.2.6, `quay.io/ceph/ceph:v19.2.6` | 20.2.4, `quay.io/ceph/ceph:v20.2.4` |
| librados the benchmark links | the host's 19.2.6-1.fc43 | the host's 19.2.6-1.fc43 |

Common to both: one rooket kind worker (rooket 3d8551957e08) with one 10 GiB
BlueStore OSD, which the kernel reports as rotational, on a file the host
serves over iSCSI, and the pool `rgw-go-test` (32 PGs, one replica); an AMD
Ryzen 9 7950X3D, 32 threads, 62 GiB, kernel 7.2.6-100.fc43; go1.27.1 with
the default GOMAXPROCS, 32;
`objecter_inflight_ops` 1024 and `objecter_inflight_op_bytes` 100 MiB, so the
in-flight limiter's derived bounds were 1008 operations and 98304000 bytes.
Each cell ran for testing.B's 20 s, each 4 MiB write cell and floor run wrote
1000 objects, and each other floor run lasted 20 s.

The criteria are judged in the callback and pipe modes; sync is the
baseline:

| Criterion | Squid callback | Squid pipe | Tentacle callback | Tentacle pipe |
|---|---|---|---|---|
| (a) throughput >= 0.8x floor | FAIL 0.58x (write4m at 16), -0.22 | FAIL 0.34x (write4k at 256), -0.46 | FAIL 0.66x (read4k at 64), -0.14 | PASS 0.85x (read4k at 256), +0.05 |
| (b) mean latency <= 1.25x floor | FAIL 1.71x (write4m at 16), -0.46 | FAIL 2.93x (write4k at 256), -1.68 | FAIL 1.53x (read4k at 64), -0.28 | PASS 1.18x (read4k at 256), +0.07 |
| (c) thread growth <= GOMAXPROCS + 8 = 40 | PASS +21 (headwrite4k at 512), +19 | PASS +36 (indexrtt at 512), +4 | PASS +38 (headwrite4k at 512), +2 | PASS +29 (read4k at 512), +11 |
| (d) cgo frames <= 10% of CPU | FAIL 25.2%, -15.2 points | FAIL 18.1%, -8.1 points | FAIL 25.9%, -15.9 points | FAIL 19.1%, -9.1 points |

No judged mode passes all four criteria on either release. What the numbers
show:

- **The cgo share is steady across releases and above 10% in every mode:**
  25.2% and 25.9% in callback, 18.1% and 19.1% in pipe, 36.6% and 40.6% in
  sync (Squid, Tentacle), in the CPU profile of read4k at 256 in flight. Most
  of it is C that the calls into librados run on the Go thread: the runtime
  records a sample taken in C under the Go stack of the call, ending in
  `runtime.cgocall`
  ([proc.go:5844-5860](https://github.com/golang/go/blob/go1.27.1/src/runtime/proc.go#L5844-L5860)),
  and samples whose leaf is `runtime.cgocall` are 16.3% and 17.1% in
  callback, 14.3% and 15.6% in pipe, 34.2% and 37.1% in sync. The rest, Go
  frames under a cgo frame (the crossing, the pinner and the Go code a
  completion callback runs), is 8.9% and 8.8% in callback, 3.8% and 3.5% in
  pipe. librados's own threads, which the runtime records under
  `runtime._ExternalCode`
  ([signal_unix.go:544-552](https://github.com/golang/go/blob/go1.27.1/src/runtime/signal_unix.go#L544-L552)),
  took a further 39.7% to 51.6% of the sampled CPU in every mode.
- **Callback and pipe held thread growth within the allowance on both
  releases**, the closest at +38 (Tentacle, callback, headwrite4k at 512).
  Sync grew with the operations in flight: read4k +52 and +49 at 64, +202 and
  +198 at 256, +260 and +266 at 512, where callback grew +4 and +3, +11 and +7,
  +10 and +2, and pipe +5 and +10, +7 and +14, +6 and +29, at about the same
  throughput (read4k at 256: 57773 and 52278 ops/s in sync, 57791 and 55578 in
  callback, 56088 and 58763 in pipe).
- **The in-flight limiter's evidence:** write4m at 64 in flight, 256 MiB
  budgeted, past the objecter's 100 MiB byte throttle. With the limiter's
  bounds above what the cell reaches, the process grew +35 and +36 threads in
  callback and +31 and +33 in pipe; with the bounds derived from the
  throttle, +19 and +23 in callback and +20 and +20 in pipe, while 617 of the
  640 writes parked on the limiter each time. No cell under the byte budget
  parked on it.
- **Throughput and latency against the floor did not hold still long enough
  to decide on.** 4 KiB writes at 256 and 512 in flight fell to 3600 to 5600
  operations a second in some runs: Squid's pipe cells (5548 and 3756 ops/s
  against a floor of 16275 and 16306), and on Tentacle the callback cells
  (4216 and 3579), the pipe cells (4695 and 4946) and the rados bench floor
  itself (4886 and 4228, against its own 9885 at 64), while sync ran 24172
  and 24413 there. The C++ floor fell too, so the drop is not a property of
  a completion mode. On Squid callback wrote 4 MiB objects at 79 to 80 a
  second at 1, 4 and 16 in flight, against 85 to 138 for sync and pipe and
  104 to 137 for the floor; on Tentacle callback wrote 106 to 159, 0.89x to
  1.16x of the floor. The 4 MiB rand floor read 223 to 441 objects a second,
  and the seam's modes 193 to 1214 on Squid and 197 to 559 on Tentacle, no
  mode ahead on both. read4k at 64 to 512 in flight was 0.91x to 0.96x of the
  floor in callback and pipe on Squid; on Tentacle, against a floor 13 to 15%
  faster than Squid's, callback was 0.66x to 0.82x, pipe 0.85x to 0.89x and
  sync 0.75x to 0.84x.

How exposed each criterion is to the host's noise: the cgo share is a
fraction of the benchmark process's own profile, and it agreed between the
releases to within 1.0 point in callback and pipe. Thread growth is the
process's own count, but contention can raise it: the scheduler hands the P
of a thread that has sat in a cgo call for more than a sysmon tick to
another thread
([proc.go:6743-6766](https://github.com/golang/go/blob/go1.27.1/src/runtime/proc.go#L6743-L6766)),
so a call slowed by contention can add one. Throughput and latency are the
most exposed: each divides a cell by a floor run measured 5 to 55 minutes
after it, so whatever the cluster and the host did in between enters the
ratio; the floor's 4 KiB writes at 256 in flight ran 16275 a second on Squid
and 4886 on Tentacle.

As the numbers stand: by the criterion's measure the cgo boundary holds 18%
to 26% of the benchmark's CPU in the asynchronous modes, about 4% (pipe) to
9% (callback) of it outside the C that librados runs on the Go thread, and
fails (d) everywhere; callback and pipe keep the thread count within
GOMAXPROCS + 8 where sync does not, and the limiter cuts the over-budget
growth by 11 to 16 threads; and this run cannot say whether either
asynchronous mode keeps up with `rados bench`, because the comparison moved
more between runs than the criteria's margins.

## Inherent

### OS threads owned by librados

- **Evidence:** before issuing any operation, the Task 12 integration process
  had about 49 OS threads against Squid and 58 against Tentacle. librados runs
  its own messenger, finisher and timer threads.
- **Status:** accepted for now.
- **Measure:** thread count at idle and under load, per mode, against the
  design target of GOMAXPROCS plus a small constant. Count process-wide: the
  `Threads` field of `/proc/<pid>/status`, which rgw-go exports as
  `rgw_go_process_threads`, not `go_threads`. `go_threads` counts the Go
  runtime's thread records, so it misses every librados thread that never
  calls into Go, the messenger's workers among them
  (`PosixNetworkStack::spawn_worker` starts them as `std::thread`s,
  [PosixStack.h:51](https://github.com/ceph/ceph/blob/v19.2.6/src/msg/async/PosixStack.h#L51)),
  and it counts the spare record cgo keeps for callbacks from C threads,
  which has no thread of its own
  ([`runtime.mstartm0`](https://github.com/golang/go/blob/go1.27.1/src/runtime/proc.go#L1964-L1973)).
  Under Go 1.27.1 a cgo binary with no C threads reports one more
  `go_threads` than the kernel does.
- **Measured (preliminary, busy host):** a connected seam benchmark process
  with GOMAXPROCS 32 counted 26 or 27 threads before its first cell in every
  mode on both releases, and librados's own threads took 39.7% to 51.6% of
  the sampled CPU of read4k at 256 in flight
  ([Squid](benchmarks/2026-10-01-seam-squid/REPORT.md),
  [Tentacle](benchmarks/2026-10-01-seam-tentacle/REPORT.md); "Phase 1
  measurement" above).

### A parked OS thread per synchronous operation

- **Evidence:** in sync mode each in-flight operation blocks one OS thread in
  a cgo call. The Task 12 integration run added 11 to 14 threads under its
  small load.
- **Mitigation:** the callback and pipe modes held the thread count flat.
- **Measure:** threads and throughput versus concurrency, sync against the two
  async modes. Count from a fresh process: an OS thread the Go runtime no
  longer needs is parked on its idle list for reuse rather than exited
  (`stopm` and `mput`,
  [proc.go:3005-3026](https://github.com/golang/go/blob/go1.27.1/src/runtime/proc.go#L3005-L3026)
  and [:7246-7257](https://github.com/golang/go/blob/go1.27.1/src/runtime/proc.go#L7246-L7257)
  at go1.27.1), so after a warm-up burst a later sync burst often shows no
  rise at all. A thread exits only when a goroutine exits while locked to it
  with `runtime.LockOSThread`: `gdestroy` then unwinds the thread to
  `mstart0`, whose `mexit` ends it rather than return a thread the goroutine
  may have left in an unusual state to the pool
  ([proc.go:4569-4583](https://github.com/golang/go/blob/go1.27.1/src/runtime/proc.go#L4569-L4583),
  [:1901-1910](https://github.com/golang/go/blob/go1.27.1/src/runtime/proc.go#L1901-L1910)).
  `mexit` has no other caller, and its doc names that unwind as the way a
  thread exits
  ([:1989-1993](https://github.com/golang/go/blob/go1.27.1/src/runtime/proc.go#L1989-L1993)).
  The thread ends after its goroutine has returned, so a count taken at that
  moment can still include it. Nothing on the seam's paths locks a thread:
  neither the go-ceph fork nor rgw-go's `internal/` calls `LockOSThread`, so
  there the count only rises, and the seam benchmark counts a cell's idle
  threads before its first call. In the final-review measurements, 512
  concurrent reads rose sync mode by 0 to 70 threads, with 62 to 512 reads in
  flight, while callback and pipe rose by 0 with 350 to 512 in flight.
- **Measured (preliminary, busy host):** sync read4k grew +202 and +198
  threads at 256 in flight and +260 and +266 at 512 on Squid and Tentacle,
  where callback and pipe grew at most +14 at 256 and +29 at 512 at about the
  same throughput ([Squid](benchmarks/2026-10-01-seam-squid/REPORT.md),
  [Tentacle](benchmarks/2026-10-01-seam-tentacle/REPORT.md)).

### The objecter throttle blocks the submitting thread

- **Evidence:** librados budgets every operation against
  `objecter_inflight_ops` (default 1024) and `objecter_inflight_op_bytes`
  (default 100 MiB), both in `src/common/options/global.yaml.in`
  ([v19.2.6](https://github.com/ceph/ceph/blob/v19.2.6/src/common/options/global.yaml.in#L2378-L2389)).
  `Objecter::_op_submit_with_budget` takes the budget inside the submit call
  ([Objecter.cc:2295](https://github.com/ceph/ceph/blob/v19.2.6/src/osdc/Objecter.cc#L2295-L2315)),
  and since `RadosClient::connect` turns on the balanced budget
  ([RadosClient.cc:263](https://github.com/ceph/ceph/blob/v19.2.6/src/librados/RadosClient.cc#L263)),
  `_throttle_op` waits in `Throttle::get` until other operations complete
  ([Objecter.cc:3356-3381](https://github.com/ceph/ceph/blob/v19.2.6/src/osdc/Objecter.cc#L3356-L3381)).
  The asynchronous calls submit on the caller's thread too
  (`IoCtxImpl::aio_operate_read` and `aio_operate` call `op_submit`), so
  once the budget is spent a callback- or pipe-mode submission pins an OS
  thread in its cgo call just as a synchronous one does.

  The byte budget charges a read the length it asks for, whatever the
  object holds, and a class call nothing: `Objecter::calc_op_budget` adds a
  read's extent length and a write-mode step's input
  ([Objecter.cc:3338-3354](https://github.com/ceph/ceph/blob/v19.2.6/src/osdc/Objecter.cc#L3338-L3354)
  at v19.2.6, [:3502-3518](https://github.com/ceph/ceph/blob/v20.2.4/src/osdc/Objecter.cc#L3502-L3518)
  at v20.2.4), and `ceph_osd_op_mode_read` excludes `CEPH_OSD_OP_CALL`
  ([rados.h:401-405](https://github.com/ceph/ceph/blob/v19.2.6/src/include/rados.h#L401-L405),
  [:402-406](https://github.com/ceph/ceph/blob/v20.2.4/src/include/rados.h#L402-L406)).
  radosgw's head read asks for `rgw_max_chunk_size`
  ([rgw_rados.cc:8853](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8853)
  at v19.2.6, [:9797](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9797)
  at v20.2.4), 4 MiB by default
  ([rgw.yaml.in:84-95](https://github.com/ceph/ceph/blob/v19.2.6/src/common/options/rgw.yaml.in#L84-L95),
  [:84-98](https://github.com/ceph/ceph/blob/v20.2.4/src/common/options/rgw.yaml.in#L84-L98)).
  Reading the head of a 4 KiB object therefore costs 4 MiB of the 100 MiB
  byte budget, and at most 25 head reads fit in flight per process. That is
  radosgw's ceiling as much as rgw-go's: radosgw raises
  `objecter_inflight_ops` to 24576 and leaves the byte budget at its default
  ([rgw_main.cc:83](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_main.cc#L83),
  [v20.2.4](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_main.cc#L83)).
  rgw-go's limiter, sized a sixteenth below the byte budget, allows at most
  23.
- **Status:** mitigated. goceph parks every Read and Write on the Cluster's
  in-flight limiter in Go, sized 16 operations and one sixteenth of the bytes
  below librados's own limits, which it reads through `rados_conf_get` after
  connect. The operation margin is for the calls that take the objecter's
  budget without passing the limiter: each lock call holds one operation
  while it runs, and an object listing one for the whole listing. Watch and
  notify register linger operations, which take none
  (`Objecter::take_linger_budget`), and mon traffic goes through the
  MonClient. The byte margin absorbs what librados budgets
  (`Objecter::calc_op_budget`) but the limiter's weight leaves out: xattr and
  omap payloads and xattr comparisons. The weight is read lengths, write and
  append data and exec inputs; librados budgets no class call at all. An
  abandoned operation keeps its share until its completion fires, since
  `rados_aio_cancel` is unbound.
- **Measure:** throttle waits and thread count under the seam
  microbenchmark, `BenchmarkSeam` in `test/bench/seam`; the head read's cost
  in its `shape=headread4k` cells, whose `budget_bytes` is the 4 MiB the head
  read asks for, so the default `-budget-bytes`, the limiter's 98304000
  bytes, skips those above 23 in flight.
- **Measured (preliminary, busy host):** write4m at 64 in flight, past the
  byte throttle, grew the process by +35 and +36 threads in callback and +31
  and +33 in pipe with the limiter's bounds above what the cell reaches, and
  by +19 and +23 and +20 and +20 with the derived bounds, 617 of 640 writes
  parking on the limiter; no cell under the byte budget parked on it
  ([Squid](benchmarks/2026-10-01-seam-squid/REPORT.md),
  [Tentacle](benchmarks/2026-10-01-seam-tentacle/REPORT.md)).

### Completions cross from C back to Go

- **Evidence:** an async completion must reach Go through a C-to-Go callback
  (a cgo callback on a librados finisher thread) or through a pipe the fork
  writes and a Go goroutine reads. The fork's aio mode is process-wide, so
  callback-mode and pipe-mode clusters cannot coexist.
- **Measure:** per-operation latency and CPU for callback and pipe against
  sync; this is the core of the cgo question.
- **Measured (preliminary, busy host):** read4k with one operation in flight
  had a median latency of 67.2 and 70.9 µs in sync, 77.8 and 79.4 in
  callback and 83.6 and 78.4 in pipe on Squid and Tentacle, at 34.2 and 37.2,
  47.7 and 48.3, and 56.8 and 52.0 µs of CPU per operation; at 256 in flight
  the CPU was 26.9 and 27.5, 34.2 and 29.0, and 33.4 and 34.2 µs.
  `runtime.cgocallback` and the frames under it held 5.4% and 5.0% of
  callback mode's read4k profile at 256
  ([Squid](benchmarks/2026-10-01-seam-squid/REPORT.md),
  [Tentacle](benchmarks/2026-10-01-seam-tentacle/REPORT.md)).

### A write operation cannot return class-method output

- **Evidence:** `rados_write_op_exec` in librados.h has no output buffer,
  unlike `rados_read_op_exec`, although the OSD wire protocol can return data
  from a write. Modifying class methods that return data (2pc_queue reserve,
  user reset_user_stats2) must run inside a read operation with
  `LIBRADOS_OPERATION_RETURNVEC`.
- **Status:** worked around by the design's read-op route, which depends on
  the OSD accepting a modifying method in a read op. The goceph integration
  spec confirms it: cls hello write_return_data run in a ReturnVec ReadOp
  returns its output and its xattr persists. The route is bounded: the OSD
  serves such an op as a write, so without ReturnVec it returns no step's
  output, and with ReturnVec it fails the op with EOVERFLOW, applying
  nothing, when any step's output passes `osd_max_write_op_reply_len`, 64
  bytes by default (PrimaryLogPG::execute_ctx, `src/osd/PrimaryLogPG.cc:4210-4246`
  at v19.2.6, `:4287-4323` at v20.2.4). Found in the M Task 1 review;
  fakerados models both.

### The locator is per I/O context, not per operation

- **Evidence:** `rados_ioctx_locator_set_key` sets the locator on an I/O
  context. RGW sets a locator on every object whose name starts with `_`, so
  the client juggles I/O contexts per operation. Task 12's first version cached
  one context per locator, which grew without bound under user control.
- **Status:** pooled contexts with the locator set per operation
  (goceph/pool.go).
- **Measure:** cost of the extra locator set and context borrow per operation.
- **Measured:** not yet. No seam shape sets an object locator, so the
  2026-10-01 runs do not isolate it.

### The object version is per I/O context

- **Evidence:** `rados_get_last_version` takes an I/O context and races
  between concurrent synchronous writes on it. Sync mode therefore borrows a
  private I/O context per write. Async completions carry their own version.
- **Measure:** included in sync-mode per-operation cost.
- **Measured (preliminary, busy host):** sync write4k at 64 in flight took
  39.1 and 35.1 µs of CPU per operation on Squid and Tentacle, against 40.5
  and 36.6 in callback and 40.3 and 37.7 in pipe
  ([Squid](benchmarks/2026-10-01-seam-squid/REPORT.md),
  [Tentacle](benchmarks/2026-10-01-seam-tentacle/REPORT.md)).

### Payload copies across the boundary

- **Evidence:** librados copies write payloads into its own buffers when the
  operation is built. Class-method output lands in C memory, and the fork copies
  it into Go. Reads land in pinned Go memory.
- **Measure:** allocation and copy cost on large objects; CPU profile with the
  cgo boundary attributed, as the spec's phase 1 section asks.
- **Measured (preliminary, busy host):** CPU per operation was 685 to 979 µs
  for read4m and 924 to 2729 µs for write4m, against 22 to 122 µs for read4k
  and write4k, across modes and concurrencies on both releases; the read4k
  profile at 256 is attributed in "Phase 1 measurement" above. Allocation is
  not measured
  ([Squid](benchmarks/2026-10-01-seam-squid/REPORT.md),
  [Tentacle](benchmarks/2026-10-01-seam-tentacle/REPORT.md)).

### A read needs its buffer sized up front

- **Evidence:** `rados_read_op_read` reads into a buffer of the length its
  caller gives, and go-ceph passes a Go buffer of that length. librados's
  C++ `read(0, 0)`, with which radosgw reads a whole system object into a
  bufferlist (`driver/rados/config/impl.cc:46-60` at v19.2.6), has no C
  equivalent: a zero length reads into an empty buffer, which librados
  fails with ERANGE when data remains (`radosclient.ReadStep`). A caller
  reading an object of unknown size therefore stats it first or reads into
  a buffer as large as the object can be. The driver reads each realm,
  period, zonegroup and zone object into a 4 MiB buffer (`rootObjectMax`),
  which goceph allocates in full for every such read.
- **Status:** accepted for the few reads at startup; a path that reads
  small objects often needs a size it knows, a stat, or a pooled buffer.
- **Measure:** allocation per read against the object's size.
- **Measured:** not yet. The seam's read shapes reuse one buffer per worker,
  so the 2026-10-01 runs measure no allocation.

### Cancellation stops only the client

- **Evidence:** `rados_aio_cancel` makes the Objecter drop the operation with
  `-ECANCELED` (IoCtxImpl::aio_cancel). A write already sent to the OSD may
  still apply. Until the completion fires, Go buffers stay pinned and the I/O
  context must stay open. A pool closed with an abandoned operation in flight
  segfaulted in Task 12 before in-flight counting was added.
- **Note:** a pure-Go client has the same wire-level limit on sent writes, but
  owns its buffers and needs no pinning or reaper.

### The librados at run time sets which cephx keys authenticate

- **Evidence:** Ceph 19.2.6 and 20.2.4 are the first releases whose cephx
  can parse AES256KRB5 keys, and an older librados fails `rados_connect`
  with such a key (`input/output error`), which is what the first
  integration workflow runs hit with noble's librados 19.2.3. Such keys are
  the default: Ceph's mkfs at those releases allows only AES256KRB5, and
  Rook, which allows both AES and AES256KRB5, makes AES256KRB5 the
  preferred type that new daemon keys, the RGW's included, take unless
  `cephx.daemon.keyType` overrides it. rgw-go therefore supports only
  librados 19.2.6 or later on Squid, or 20.2.4 or later on Tentacle. That is
  a support policy; technically the floor binds only when the key presented
  is AES256KRB5. It is a client floor, not a cluster floor: upgraded
  clusters keep their AES keys working. Under Rook the derived image is
  every daemon's image, so rgw-go's librados is the cluster's own release
  and meets the floor whenever the cluster can issue such keys. The headers
  used for building are unaffected.
- **Note:** a pure-Go client removes the coupling to the installed library
  only by implementing the AES256KRB5 cipher itself, RFC 8009
  AES256-CTS-HMAC-SHA384-192, wherever cephx uses a key: entity keys,
  tickets, authorizers and session keys. It then carries that work, and
  every future cipher change, instead of taking it from Ceph.
- **Detection:** `rados_version` reports the librados API version (3.0.0
  on every release), not the Ceph release. `goceph.Connect` reads the release
  from libceph-common's `ceph_version_to_str` by its mangled C++ symbol. It
  refuses a pre-Squid library up front and otherwise uses the release only to
  explain a failed connect. If the symbol disappears, both are skipped with a
  warning; the connect itself is unaffected.
- **Status:** accepted; the integration workflow installs librados 20.2.4
  from download.ceph.com.

### Build and deployment cost

- **Evidence:** every build needs librados headers and the shared library.
  `CGO_ENABLED=0` and cross-compilation are impossible, so the goreleaser
  config must change once `cmd/rgw-go` imports the client. CI installs
  librados-dev. Local sandboxed builds need `CCACHE_DISABLE=1`. Building the
  derived image adds two more costs:
  - A builder image per Ceph release (`hack/image/Containerfile.builder`),
    because the binary must link against the librados and glibc of the Ceph
    image it runs in: that image plus the `librados-devel` build matching the
    image's librados2, `git-core` and the Go toolchain. Building one needs a
    container engine and download.ceph.com, and adds about 305 MB over the
    Ceph image: 304.8 MB over `quay.io/ceph/ceph:v19.2.6` and 305.1 MB over
    `v20.2.4`, of which the Go toolchain is 257 MB and the package install
    48.1 MB (measured with podman 5.8.4).
  - A Go build cache per librados. Go's build cache "does not detect changes
    to C libraries imported with cgo" (`go help cache`, go1.27.1), and both
    floor images carry the same gcc, 11.5.0-15.el9, so a cache shared by
    builds against Squid's and Tentacle's librados could hand one release
    go-ceph objects compiled against the other's headers.
    `hack/image/cgo-build.sh` keeps one cache per builder.
- **Note:** a pure-Go client removes both: one `CGO_ENABLED=0` binary, built
  on any host with an ordinary build cache, would serve every release. The
  derived image stays one per release, because under Rook it is the Ceph
  image every daemon runs.

## RADOS semantics that shape the client

These come from the OSD, not from cgo, so a pure-Go client has them too.

### A read in the same operation sees the object as it was before the operation

- **Evidence:** a ReadOp can run a modifying class method with RETURNVEC and
  the change persists, but a later read step in the same op returns the
  pre-op value (replicated pools read attrs from the object store;
  PrimaryLogPG getattr_maybe_cache). Class clients therefore rely on a
  method's own output, never on reading back its effect.
- **Status:** found in Task 12, confirmed on Squid and Tentacle.

### Two modifying class methods in one operation both see the pre-op state

- **Evidence:** the later one's write wins, e.g. two rgw_gc queue enqueues in
  one op keep only the second entry.
- **Status:** found in Task 14 (gc), confirmed on Squid.

### A class method run in a read operation leaves the object's mtime unchanged

- **Evidence:** radosgw runs reset_user_stats2 in an ObjectWriteOperation;
  rgw-go runs it in a ReturnVec ReadOp so it can return data, and a read op
  carries no mtime (PrimaryLogPG.cc). Harmless today because nothing reads
  `<user>.buckets`' mtime.
- **Status:** found in Task 14 (user) review.

## Binding

| Limitation | Found | Status |
|---|---|---|
| Empty or nil buffers panicked in `WriteOp.SetXattr` and the steps behind `Write`, `WriteFull`, `WriteSame`, `CmpExt` and `ReadOp.Read` (`&b[0]` with no length check) | Task 12 planning | Fixed on the fork's `rgw-go` branch by ceph/go-ceph#1345 (jhoblitt/go-ceph#5); upstream PR open |
| The same unguarded `&b[0]` in `IOContext.WriteFull`, `Append`, `GetXattr`, `SetXattr`, the ioctx buffer loops, the striper and `Checksum` | Fork follow-up | Fixed by the same ceph/go-ceph#1345, which also covers these sites and the striper |
| `ReadOp.Operate` reported a positive return as an error, breaking CmpXattr guards and discarding later steps' results | Task 11 review | Fixed on `rgw-go` by ceph/go-ceph#1344 (jhoblitt/go-ceph#6); upstream PR open |
| go-ceph's WriteOp reports a positive librados return as an error, although the OSD committed the write | Task 14 (rgw guard sign experiment) | Fixed on `rgw-go` by ceph/go-ceph#1344 (jhoblitt/go-ceph#6), sync and async; the goceph workaround stays harmless |
| `rados_read_op_stat2` needs Reef or later and was not version-gated | Task 11 review | Fixed |
| No async write with an mtime (`rados_aio_write_op_operate2` unbound), so mtime writes fall back to a blocking call | Task 12 review | Fixed on `rgw-go` (jhoblitt/go-ceph#12, fork-only): `WriteOp.OperateAsyncWithMtime`, Reef or later |
| The object-list iterator drops the locator (`rados_nobjects_list_next2` unbound) | Task 12 review | Fixed on `rgw-go` (jhoblitt/go-ceph#13, fork-only): `Iter.Locator()` |
| `rados_aio_cancel` is unbound | Registry audit | Open |
| `Iter.Token()` returns the listing cursor's hash position, which the Objecter moves past each fetched batch while librados hands the batch out one entry at a time, so a token taken mid-batch resumes after entries not yet returned; it is not a per-entry resume marker | Phase 1 planning (admin API) | Worked around: `Pool.ListObjectsFrom` seeks to the last object's placement hash and skips same-hash entries up to it |
| With several failing steps, the op-level errno is picked from a Go map, so it varies between runs | Task 12 review | Fixed on `rgw-go` by ceph/go-ceph#1344: step errors are reported in step order; the rgw-go workaround stays harmless |
| A failed unwatch removes the watcher but never closes its channels, leaking the dispatch goroutine | Task 12 review | Fixed on `rgw-go` by ceph/go-ceph#1341 (jhoblitt/go-ceph#2): `Delete` closes the channels even when the unwatch fails; the rgw-go workaround stays harmless |
| `OmapCmp` bound to `omap_cmp`, which truncates keys at a NUL byte | Task 11 | Fixed: binds `omap_cmp2` |
| Exec-step output parameters lived in Go memory written after the call returned | Task 11 ruling | Fixed on `rgw-go` by ceph/go-ceph#1343 (jhoblitt/go-ceph#8): C-allocated; the `ReadOp.Read` buffer is pinned too |
| After a successful unwatch, a watch callback that looked the watcher up before `Delete` can reach its `select` after `done` and `events` are closed and pick the send on the closed channel, which panics on a librados thread (go-ceph watcher.go:355-358); fix: close(done), rados_watch_flush, then close the channels. `watchErrorCb` has the same window on the errors channel | Task 12 review; final review | Fixed on `rgw-go` by ceph/go-ceph#1341 (jhoblitt/go-ceph#2): close `done`, `rados_watch_flush`, then close `events` and `errors` |
| `IOContext.WatchWithTimeout` and `Watcher.Delete` call librados synchronously (`rados_watch3`; `rados_unwatch2`, then `rados_watch_flush`; go-ceph cbf97f85fcf5 rados/watcher.go:109-123, :179-191), and the fork binds none of `rados_aio_watch2`, `rados_aio_unwatch` and `rados_aio_watch_flush`, which librados has (librados.h:2541, :2601, :2752 at v19.2.6; :2542, :2602, :2753 at v20.2.4). goceph's `Pool.Watch` checks ctx only on entry (internal/radosclient/goceph/pool.go:465-478), and `watch.Close` unwatches and flushes synchronously (:554-570). With `rados_osd_op_timeout` at its default of 0, a watch or unwatch can wait as long as its PG is unavailable, so the driver's control-watch worker outlives its context and a SIGTERM waits for it. radosgw blocks the same way. `RGWWatcher::reinit` re-registers through the synchronous `register_watch` (svc_notify.cc:108, :160-168 at v19.2.6; :106, :125-133 at v20.2.4, which with `null_yield` calls the synchronous `watch2`, rgw_tools.cc:129-131). Shutdown waits for every unwatch and the flush: at v19.2.6 `finalize_watch` unwatches and flushes synchronously (:266-276, :328-341); at v20.2.4 it spawns each unwatch as an awaited `librados::async_unwatch` on a private io_context that `shutdown` runs to completion, and flushes synchronously (:226-240, :293-302, :314-328; rgw_tools.cc:138-142) | M Task 2 review | Open. Lifted by an aio watch, unwatch and flush binding in the fork, with which goceph returns at ctx's end and leaves the op in flight (see "Cancellation stops only the client"), or by a non-zero `rados_osd_op_timeout`, which the Objecter arms on the watch registration and the unwatch as on every other OSD op (`_op_submit_with_budget`, osdc/Objecter.cc:597 and :2317-2324 at v19.2.6, :617 and :2385 at v20.2.4) |
| `WatchWithTimeout` holds the package-wide `watchersMtx` for writing across the synchronous `rados_watch3` (rados/watcher.go:113-123), and the notify and error callbacks and `NotifyEvent.Ack` take it for reading (:370-372, :383-385, :271-273). While one registration blocks, no other watch in the process delivers a notify, reports an error or acks (goceph acks from its dispatch goroutine, internal/radosclient/goceph/pool.go:516-519). The driver re-registers a lost control watch while the others stay registered. So while that registration stays blocked, every notify a gateway, radosgw or rgw-go, sends to a control object whose PG is healthy waits out its notify timeout, `client_notify_timeout`, 10 s by default (mds-client.yaml.in:131-134; IoCtxImpl.cc:1800-1802 at v19.2.6, :1824-1826 at v20.2.4). So does each of the up to `rgw_max_notify_retries` invalidations, 10 by default (rgw.yaml.in:3368 at v19.2.6, :3553 at v20.2.4), that radosgw's `robust_notify` and rgw-go's `distribute` send after a timeout (svc_notify.cc:481-511 at v19.2.6, :468-498 at v20.2.4; internal/driver/notify.go:258-271). Each metadata write in the zone then waits up to (1 + 10) × 10 s = 110 s, after which its notify fails with ETIMEDOUT and is logged, and radosgw's write returns its own result (svc_sys_obj_cache.cc:344-353 at both releases). That lasts as long as the PG blocking the registration stays unavailable, which with `rados_osd_op_timeout` at 0 has no bound. `Delete` takes the same lock (:180-185), so no watch closes meanwhile either. radosgw takes its `watchers_lock` only after the watch call returns (`register_watch`, svc_notify.cc:160-168 at v19.2.6, :125-133 at v20.2.4) | M Task 2 fix round | Open. Lifted in the fork by not holding `watchersMtx` across the C call, for example by handing the callbacks the watcher through `rados_watch3`'s `arg` as a `cgo.Handle` rather than looking it up by cookie |
| (rgw-go seam, not go-ceph) `radosclient.ExecResult` reports a positive class return as success but does not surface the value | Task 12 | Open; add an accessor if a class client needs the value |
