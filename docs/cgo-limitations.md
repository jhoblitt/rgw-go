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

**Measured on a quiet host.** The gateway-level profile of Task 15 completes
this.

The seam microbenchmark and its `rados bench` floor ran on 2026-10-02 with
`make bench-seam` at rgw-go 35c9247, Squid from 03:29:16 to 04:27:43 UTC and
Tentacle from 04:29:34 to 05:29:26 UTC; each sweep took its three CPU
profiles itself, as its last cells. The run directories are
[2026-10-02-seam-squid](benchmarks/2026-10-02-seam-squid/REPORT.md) and
[2026-10-02-seam-tentacle](benchmarks/2026-10-02-seam-tentacle/REPORT.md).
They replace the preliminary runs of 2026-10-01, measured on a busy host,
which stay in git history ([benchmarks/README.md](benchmarks/README.md)).

Nothing else built, tested or benchmarked on the host during the sweeps.
The 1-minute load before each sweep's first cell was 0.98 on Squid and 1.14
on Tentacle (`env.json`). `noise.log`, which samples every 15 s the load and
up to 12 processes that used 5% of a CPU or more, and named at most 7 in any
sample, shows no process outside the sweep and the cluster above 58% of one
CPU in any sample; the 1-minute load there, the benchmark's and the
cluster's own included, had a median of 3.67 and a maximum of 7.12 during
Squid's sweep and a median of 3.59 and a maximum of 7.93 during Tentacle's.

| Conditions | Squid | Tentacle |
|---|---|---|
| Ceph, the cluster's image | 19.2.6, `quay.io/ceph/ceph:v19.2.6` | 20.2.4, `quay.io/ceph/ceph:v20.2.4` |
| The judged floor's `rados bench` | `quay.io/ceph/ceph:v19.2.6`, the cluster's own | `quay.io/ceph/ceph:v19.2.6`, the linked librados's release; the cluster's own image ran once more beside each bracketed floor, not judged |
| librados the benchmark links | the host's 19.2.6-1.fc43 | the host's 19.2.6-1.fc43 |

Common to both: one rooket kind worker (rooket 3d8551957e08) with one 10 GiB
BlueStore OSD, which the kernel reports as rotational, on a file the host
serves over iSCSI, and the pool `rgw-go-test` (32 PGs, one replica); an AMD
Ryzen 9 7950X3D, 32 threads, 62 GiB, kernel 7.2.6-100.fc43; go1.27.1 with
the default GOMAXPROCS, 32;
`objecter_inflight_ops` 1024 and `objecter_inflight_op_bytes` 100 MiB, so the
in-flight limiter's derived bounds were 1008 operations and 98304000 bytes.
Each cell ran in each mode in a benchmark process of its own. A 4 MiB write
cell or floor run wrote 1000 objects and an over-budget cell 640; every
other cell ran for testing.B's 20 s, and every other floor run for 20 s.
Each cell the throughput and latency criteria compare, and read4m, ran
between a floor run just before its three modes and one just after.

The criteria are judged in the callback and pipe modes; sync is the
baseline. The numbers below are `hack/bench/report`'s reading of the
committed data. (a) and (b) divide each compared cell by the mean of its
two floor runs, and the report names a cell whose two runs differ by more
than 10% of their mean as an unstable floor beside the verdict, which it
does not change:

| Criterion | Squid callback | Squid pipe | Tentacle callback | Tentacle pipe |
|---|---|---|---|---|
| (a) throughput >= 0.8x floor | PASS 0.86x (write4k at 64), +0.06 | PASS 0.88x (write4m at 1), +0.08 | PASS 0.97x (write4m at 1), +0.17 | PASS 0.94x (write4m at 1), +0.14 |
| (b) mean latency <= 1.25x floor | PASS 1.16x (write4k at 64), +0.09 | PASS 1.13x (write4m at 1), +0.12 | PASS 1.03x (write4m at 1), +0.22 | PASS 1.07x (write4m at 1), +0.18 |
| (c) thread growth <= GOMAXPROCS + 8 = 40 | FAIL +89 (headwrite4k at 512), -49 | FAIL +79 (indexrtt at 512), -39 | PASS +35 (read4k at 512), +5 | FAIL +99 (headwrite4k at 512), -59 |
| (d) cgo frames <= 10% of CPU | FAIL 24.9%, -14.9 points | FAIL 19.2%, -9.2 points | FAIL 24.3%, -14.3 points | FAIL 18.2%, -8.2 points |
| informational: boundary cost <= 10% of CPU | PASS 5.0%, +5.0 points | PASS 9.3%, +0.7 points | PASS 5.1%, +4.9 points | PASS 8.8%, +1.2 points |

The unstable floors named beside (a) and (b) are, on Squid, read4k at 256
(21.5% apart in operations a second, 21.6% in mean latency) and write4m at 4
(22.9%); on Tentacle, read4k at 64 (15.1% and 15.2%), read4k at 256
(18.9%), write4k at 256 (10.8% and 10.7%) and write4m at 4 (25.2%). Sync,
the baseline, was at worst 0.85x and 1.18x on Squid (read4k at 64) and
0.94x and 1.06x on Tentacle (write4m at 1), grew +532 threads on both
(read4k at 512 on Squid, write4k at 512 on Tentacle), and held 37.3% and
37.2% of its CPU in cgo frames and 3.1% and 2.8% in boundary cost.

The report gives two answers for each release:

- **With (d) as written, every cgo frame:** no judged mode passes all four
  criteria on Squid or on Tentacle.
- **With the boundary cost in (d)'s place, reported beside it as
  informational:** callback passes all four criteria on Tentacle. On Squid
  callback fails only (c), at +89 threads of the 40 allowed (headwrite4k at
  512 in flight), so no judged mode passes there. Pipe fails (c) on both
  releases.

(a) and (b) leave read4m out, for the reasons below. What the numbers show:

- **The cgo share is steady across releases and above 10% in every mode:**
  24.9% and 24.3% in callback, 19.2% and 18.2% in pipe, 37.3% and 37.2% in
  sync (Squid, Tentacle), in the CPU profile of read4k at 256 in flight. (d)
  counts every sample with a frame of the cgo boundary on its stack, cgo's
  `runtime.cgoCheck*` pointer checks included, since the plan's list of
  frames is read as examples. Most of it is C that the calls into librados
  run on the Go thread: the runtime records a sample taken in C under the Go
  stack of the call, ending in `runtime.cgocall`
  ([proc.go:5844-5860](https://github.com/golang/go/blob/go1.27.1/src/runtime/proc.go#L5844-L5860)),
  and samples whose leaf is `runtime.cgocall` are 15.6% and 15.0% in
  callback, 14.7% and 13.9% in pipe, 34.2% and 34.4% in sync. librados's own
  threads, which the runtime records under `runtime._ExternalCode`
  ([signal_unix.go:544-552](https://github.com/golang/go/blob/go1.27.1/src/runtime/signal_unix.go#L544-L552)),
  took a further 41.3% to 51.3% of the sampled CPU in every mode.
- **The boundary cost, what a pure-Go client would no longer pay, is 5.0%
  and 5.1% in callback and 9.3% and 8.8% in pipe**, under 10% in both modes
  on both releases; the two releases agree to within 0.5 points in each
  mode, and the cgo share to within 1.0. The report divides each profile by
  the frame nearest each sample's leaf, so no sample counts twice, into
  three numbers besides the C:
  - **(i) the submit crossing** is the cgo machinery around the C a call runs:
    `runtime.cgocall`'s Go side, the `_Cfunc_` stubs, the pinner and the
    pointer checks. It counts the completion handler's own calls into C as
    well, which the walk from the leaf meets before the handler; their C
    stays in the C share. It is 4.9% and 5.0% in callback, 4.5% and 4.4% in
    pipe, and 3.1% and 2.8% in sync.
  - **(ii) the delivery** is how a completion reaches Go: the
    `runtime.cgocallback*` frames a callback arrives through, or the pipe's
    `(*aioPipe).drain` goroutine and the read it waits in, short of the
    handler either one calls. Sync has none, because its wake happens in C.
    It is 0.1% in callback on both releases and 4.8% and 4.5% in pipe.
  - **The Go completion handler**, `rados.aioComplete` and the Go it calls,
    is 4.3% and 4.1% in callback and 3.2% on both releases in pipe. It
    counts in neither (i) nor (ii): a pure-Go client runs a handler too.

  The boundary cost is (i) plus (ii). Callback pays almost all of it to
  submit, pipe about half to deliver; the pointer checks alone, which only
  the reading of the plan's list as examples counts, are 0.7% of callback's
  profile on both releases and 0.7% and 0.5% of pipe's.

  (ii) counts only the Go side of delivery. Pipe's C side is one 8-byte
  write(2) to the pipe per completion, on the librados thread that completes
  the operation (the fork's `rados/aio_notifier.go`, `aio_pipe_complete`),
  which the profile samples under `runtime._ExternalCode` with no frame to
  tell it from librados's other work. A pure-Go client would not pay it, so
  pipe's boundary cost is low by an unmeasured amount, against margins of 0.7
  and 1.2 points. Pipe's `runtime._ExternalCode` share is no higher than
  callback's (41.6% and 41.3% against 42.4% and 43.5%), which suggests the
  write is small.
- **Thread growth, (c), now has a verdict.** Each cell ran in a benchmark
  process of its own, so its growth, its peak less its idle count, is its
  own. Callback stayed within the 40 allowed on Tentacle, the closest at +35
  (read4k at 512); on Squid it went over at +42 (headwrite4k at 256), +44
  (read4k at 512) and +89 (headwrite4k at 512). Pipe went over on both
  releases: at +56 (headwrite4k at 512) and +79 (indexrtt at 512) on Squid,
  and at +44 (write4k at 512), +54 (indexrtt at 512), +55 (read4k at 512) and
  +99 (headwrite4k at 512) on Tentacle. Each cell ran once, so each growth is
  a single measurement: callback's worst was +89 on Squid and +35 on
  Tentacle, where headwrite4k at 512 grew +33. Sync grew with the operations
  in flight: read4k +68 and +67 at 64, +265 and +274 at 256, +532 and +531 at
  512, where callback grew +10 and +6, +24 and +15, +44 and +35, and pipe +10
  and +5, +20 and +31, +27 and +55, at about the same throughput (read4k at
  256: 56192 and 63708 ops/s in sync, 62351 and 61107 in callback, 62044 and
  59443 in pipe).
- **The in-flight limiter's evidence:** write4m at 64 in flight, 256 MiB
  budgeted, past the objecter's 100 MiB byte throttle. With the limiter's
  bounds above what the cell reaches, the process grew +35 threads in
  callback and in pipe on both releases; with the bounds derived from the
  throttle, +19, while 617 of the 640 writes parked on the limiter each
  time. No cell under the byte budget parked on it.
- **Throughput and latency held within the criteria against `rados bench`
  in both modes on both releases.** The worst cells, write4k at 64 on Squid
  in callback and write4m at 1 elsewhere, had floor runs within 6.2% of each
  other. Against either of its two floor runs alone, every compared cell with
  an unstable floor would still pass in callback and pipe, at worst 0.88x in
  throughput and 1.13x in latency (pipe, read4k at 256, Tentacle). read4k at
  64 to 512 in flight ran at 0.88x to 1.03x of the floor in callback and pipe
  on Squid and 0.90x to 1.01x on Tentacle; 4 MiB writes at 1, 4 and 16 ran
  at 95.5 to 138 objects a second on Squid and 112 to 163 on Tentacle across
  the modes, against floor runs of 105 to 137 and 118 to 166.
- **read4m is shown but not judged.** rados bench's 4 MiB rand does not
  read as the Go read4m cell does: rand picks each object at random with
  replacement among the objects written for it, where each Go worker reads
  its own, and its fixed object names land on fixed placement groups. Like
  every floor, judged or not, it also runs the upstream container's client
  library, where the benchmark links Fedora's build of the same release, and
  until every floor write passed `--no-hints` its objects carried allocation
  hints, which the Go cells do not send. In a cut-down Tentacle run before
  the 2026-10-02 sweeps it ran at about a quarter of the cell's rate (328 and
  377 objects a second against 1308 to 1318), flat across concurrency, and
  the matched-release image did not close the gap. The 2026-10-02 runs did
  not show that gap: the floor read 193 to 343 objects a second on Squid and
  193 to 381 on Tentacle, and the Go cells 194 to 388 and 203 to 535, 0.75x
  to 1.53x of the floor. Two follow-ups toward judging read4m are open: the
  Go read4m cell picking a random object among its conc each iteration,
  which would read as rand does; and the floor running a `rados` linked to
  the host's own librados, which would remove the client build's difference
  from every floor. Neither changes the floor's fixed placement groups.

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
- **Measured (quiet host):** a connected seam benchmark process with
  GOMAXPROCS 32 counted 26 to 28 threads before its first call in every mode
  on both releases, and librados's own threads took 41.3% to 51.3% of the
  sampled CPU of read4k at 256 in flight
  ([Squid](benchmarks/2026-10-02-seam-squid/REPORT.md),
  [Tentacle](benchmarks/2026-10-02-seam-tentacle/REPORT.md); "Phase 1
  measurement" above).

### A parked OS thread per synchronous operation

- **Evidence:** in sync mode each in-flight operation blocks one OS thread in
  a cgo call. The Task 12 integration run added 11 to 14 threads under its
  small load.
- **Mitigation:** the callback and pipe modes, which cut the growth rather
  than hold the count flat: in the 2026-10-02 runs their largest growth in
  any cell under the byte budget was +89 threads in callback (Squid) and +99
  in pipe (Tentacle), both headwrite4k at 512 in flight, where sync's was
  +532 on both releases
  ([Squid](benchmarks/2026-10-02-seam-squid/REPORT.md),
  [Tentacle](benchmarks/2026-10-02-seam-tentacle/REPORT.md)).
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
- **Measured (quiet host):** sync read4k grew +265 and +274 threads at 256
  in flight and +532 and +531 at 512 on Squid and Tentacle, where callback
  and pipe grew at most +31 at 256 and +55 at 512 at about the same
  throughput, each cell in a benchmark process of its own
  ([Squid](benchmarks/2026-10-02-seam-squid/REPORT.md),
  [Tentacle](benchmarks/2026-10-02-seam-tentacle/REPORT.md)).

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
- **Measured (quiet host):** write4m at 64 in flight, past the byte
  throttle, grew the process by +35 threads in callback and in pipe on both
  releases with the limiter's bounds above what the cell reaches, and by +19
  with the derived bounds, 617 of 640 writes parking on the limiter; no cell
  under the byte budget parked on it
  ([Squid](benchmarks/2026-10-02-seam-squid/REPORT.md),
  [Tentacle](benchmarks/2026-10-02-seam-tentacle/REPORT.md)).

### Completions cross from C back to Go

- **Evidence:** an async completion must reach Go through a C-to-Go callback
  (a cgo callback on a librados finisher thread) or through a pipe the fork
  writes and a Go goroutine reads. The fork's aio mode is process-wide, so
  callback-mode and pipe-mode clusters cannot coexist.
- **Measure:** per-operation latency and CPU for callback and pipe against
  sync; this is the core of the cgo question.
- **Measured (quiet host):** read4k with one operation in flight had a
  median latency of 67.1 and 68.5 µs in sync, 74.7 and 75.2 in callback and
  91.3 and 79.6 in pipe on Squid and Tentacle, at 34.7 and 36.1, 45.6 and
  45.9, and 57.9 and 53.3 µs of CPU per operation; at 256 in flight the CPU
  was 25.5 and 23.7, 31.3 and 27.1, and 30.5 and 34.2 µs.
  `runtime.cgocallback` and the frames under it held 5.2% and 4.7% of
  callback mode's read4k profile at 256, almost all of it the Go completion
  handler, 4.3% and 4.1%, and its own calls into C: the delivery itself was
  0.1%. Pipe mode's delivery, `(*aioPipe).drain` and the pipe read it waits
  in, was 4.8% and 4.5%, besides its handler's 3.2% on both releases
  ([Squid](benchmarks/2026-10-02-seam-squid/REPORT.md),
  [Tentacle](benchmarks/2026-10-02-seam-tentacle/REPORT.md)).

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
  2026-10-02 runs do not isolate it.

### The object version is per I/O context

- **Evidence:** `rados_get_last_version` takes an I/O context and races
  between concurrent synchronous writes on it. Sync mode therefore borrows a
  private I/O context per write. Async completions carry their own version.
- **Measure:** included in sync-mode per-operation cost.
- **Measured (quiet host):** sync write4k at 64 in flight took 33.3 and
  32.6 µs of CPU per operation on Squid and Tentacle, against 34.3 and 34.4
  in callback and 37.4 and 38.0 in pipe
  ([Squid](benchmarks/2026-10-02-seam-squid/REPORT.md),
  [Tentacle](benchmarks/2026-10-02-seam-tentacle/REPORT.md)).

### The C API cannot read back an I/O context's full-try

- **Evidence:** goceph sets full-try on every I/O context it opens, as
  radosgw's `rgw_init_ioctx` does (`rgw_tools.cc:97-98` at v19.2.6, `:98-99`
  at v20.2.4), through `rados_set_pool_full_try`. The C API has only that
  setter and `rados_unset_pool_full_try` (librados.h:3782-3784 at v19.2.6,
  :3783-3785 at v20.2.4), and go-ceph binds no more. Only the C++ API reads
  the flag back, with `IoCtx::get_pool_full_try()` (librados.hpp:1356 at
  v19.2.6, :1377 at v20.2.4). Reaching it from Go takes a C++ cgo shim over
  `IOContext.Pointer()`, and a connected cluster to open the context.
- **Status:** accepted; found in W Task 1's review. No spec asserts that
  goceph's contexts carry full-try. Its only observable effect is how an op
  on a full pool, or one at its quota, is answered.
- **Note:** a pure-Go client would set the flag on each op it encodes, where
  a unit spec can read it.

### Payload copies across the boundary

- **Evidence:** librados copies write payloads into its own buffers when the
  operation is built. Class-method output lands in C memory, and the fork copies
  it into Go. Reads land in pinned Go memory.
- **Measure:** allocation and copy cost on large objects; CPU profile with the
  cgo boundary attributed, as the spec's phase 1 section asks.
- **Measured (quiet host):** CPU per operation was 749 to 930 µs for
  read4m and 885 to 2574 µs for write4m, against 20 to 115 µs for read4k and
  write4k, across modes and concurrencies on both releases; the read4k
  profile at 256 is attributed in "Phase 1 measurement" above. Allocation is
  not measured
  ([Squid](benchmarks/2026-10-02-seam-squid/REPORT.md),
  [Tentacle](benchmarks/2026-10-02-seam-tentacle/REPORT.md)).

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
  The metadata reads (`sysobjs.read`) read into a 4 KiB buffer with a stat
  in the same operation, and read an object the stat shows larger again
  at that size, so only a metadata object above 4 KiB costs a second round
  trip.
- **Measure:** allocation per read against the object's size.
- **Measured:** not yet. The seam's read shapes reuse one buffer per worker,
  so the 2026-10-02 runs measure no allocation.

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
| `Iter.Token()` returns the listing cursor's hash position, which the Objecter moves past each fetched batch while librados hands the batch out one entry at a time, so a token taken mid-batch resumes after entries not yet returned; it is not a per-entry resume marker | Phase 1 planning (admin API) | Worked around: `Pool.ListObjectsFrom` seeks to the last object's placement hash and skips every entry hobject order puts at or before that object, since the seek restarts at the start of the hash's placement group (next row) |
| `Iter.Seek` (`rados_nobjects_list_seek`) is exact only once the listing has fetched. `Objecter::list_nobjects_seek(uint32_t)` sets the position but leaves `NListContext::sort_bitwise` alone, which is false on a fresh listing (osdc/Objecter.h:2223 at v19.2.6, :2190 at v20.2.4). The first fetch then takes the cluster's SORTBITWISE as a change of order and restarts at `hobject_t(hash=current_pg)`, the first position of the hash's placement group (osdc/Objecter.cc:3761-3773 and :3828-3835 at v19.2.6, :3934-3946 and :4001-4008 at v20.2.4). After a fetch the flag is true and `IoCtxImpl::nlist_seek` leaves it so (librados/IoCtxImpl.cc:538-543 at v19.2.6, :545-550 at v20.2.4), so a later seek is exact. go-ceph's `Iter()` opens a fresh listing and rgw-go seeks before the first `Next`, so every resumed page replays the start of the group, and the OSD walks every namespace's objects in that range: PGNLS filters the namespace object by object (osd/PrimaryLogPG.cc:1366-1369 at v19.2.6, :1408-1411 at v20.2.4). The constraint is that the C API cannot rebuild a cursor from a stored value. The exact `rados_nobjects_list_seek_cursor` takes a `rados_object_list_cursor`, which C hands out only from a live listing or enumeration (`rados_nobjects_list_get_cursor`, `rados_object_list_begin` and `_end`, `rados_object_list`'s next, `rados_object_list_slice`; include/rados/librados.h:1110-1272 at v19.2.6, :1111-1273 at v20.2.4), with no counterpart to C++'s `ObjectCursor::from_str` (librados.hpp:100 at both tags). The fork binds neither nobjects cursor call, only `rados_object_list` (rados/ioctx.go:331-358). A seek on an I/O context whose pool was deleted aborts the process: `list_nobjects_seek` calls `OSDMap::raw_pg_to_pg`, which `ceph_assert`s that the pool is in the client's map (osd/OSDMap.h:1432-1436 at v19.2.6, :1464-1468 at v20.2.4). An unseeked listing gets ENOENT instead (osdc/Objecter.cc:3813-3819, :3986-3992), the code `rados_nobjects_list_next2` also returns at a listing's end (librados/librados_c.cc:2473-2481 at both tags), so go-ceph's `Iter.Err` reads it as an empty listing. goceph reuses idle I/O contexts, so a context can predate the deletion by more than one call, and a pre-check would race it | N Task 2 review | Worked around for the replay: `radosclient.ListPage` skips by hobject order everything a seek replays before the resume point, so a resumed page re-reads up to one placement group. The abort is open (docs/ceph-upstream-bugs.md, "librados aborts the process on a listing seek after its pool is deleted"). Suggested lift, inferred from the code and not run: bind `rados_nobjects_list_seek_cursor` in the fork. A cursor seek to `rados_object_list_begin`, a fresh `hobject_t()` (librados/librados_c.cc:2239-2246; osdc/Objecter.cc:5143-5146 at v19.2.6, :5332-5335 at v20.2.4), sets `sort_bitwise` (Objecter.cc:3784, :3957), after which the hash seek is exact and the replay disappears |
| With several failing steps, the op-level errno is picked from a Go map, so it varies between runs | Task 12 review | Fixed on `rgw-go` by ceph/go-ceph#1344: step errors are reported in step order; the rgw-go workaround stays harmless |
| A failed unwatch removes the watcher but never closes its channels, leaking the dispatch goroutine | Task 12 review | Fixed on `rgw-go` by ceph/go-ceph#1341 (jhoblitt/go-ceph#2): `Delete` closes the channels even when the unwatch fails; the rgw-go workaround stays harmless |
| `OmapCmp` bound to `omap_cmp`, which truncates keys at a NUL byte | Task 11 | Fixed: binds `omap_cmp2` |
| Exec-step output parameters lived in Go memory written after the call returned | Task 11 ruling | Fixed on `rgw-go` by ceph/go-ceph#1343 (jhoblitt/go-ceph#8): C-allocated; the `ReadOp.Read` buffer is pinned too |
| After a successful unwatch, a watch callback that looked the watcher up before `Delete` can reach its `select` after `done` and `events` are closed and pick the send on the closed channel, which panics on a librados thread (go-ceph watcher.go:355-358); fix: close(done), rados_watch_flush, then close the channels. `watchErrorCb` has the same window on the errors channel | Task 12 review; final review | Fixed on `rgw-go` by ceph/go-ceph#1341 (jhoblitt/go-ceph#2): close `done`, `rados_watch_flush`, then close `events` and `errors` |
| `IOContext.WatchWithTimeout` and `Watcher.Delete` call librados synchronously (`rados_watch3`; `rados_unwatch2`, then `rados_watch_flush`; go-ceph cbf97f85fcf5 rados/watcher.go:109-123, :179-191), and the fork binds none of `rados_aio_watch2`, `rados_aio_unwatch` and `rados_aio_watch_flush`, which librados has (librados.h:2541, :2601, :2752 at v19.2.6; :2542, :2602, :2753 at v20.2.4). goceph's `Pool.Watch` checks ctx only on entry (internal/radosclient/goceph/pool.go:465-478), and `watch.Close` unwatches and flushes synchronously (:554-570). With `rados_osd_op_timeout` at its default of 0, a watch or unwatch can wait as long as its PG is unavailable, so the driver's control-watch worker outlives its context and a SIGTERM waits for it. radosgw blocks the same way. `RGWWatcher::reinit` re-registers through the synchronous `register_watch` (svc_notify.cc:108, :160-168 at v19.2.6; :106, :125-133 at v20.2.4, which with `null_yield` calls the synchronous `watch2`, rgw_tools.cc:129-131). Shutdown waits for every unwatch and the flush: at v19.2.6 `finalize_watch` unwatches and flushes synchronously (:266-276, :328-341); at v20.2.4 it spawns each unwatch as an awaited `librados::async_unwatch` on a private io_context that `shutdown` runs to completion, and flushes synchronously (:226-240, :293-302, :314-328; rgw_tools.cc:138-142) | M Task 2 review | Open. Lifted by an aio watch, unwatch and flush binding in the fork, with which goceph returns at ctx's end and leaves the op in flight (see "Cancellation stops only the client"), or by a non-zero `rados_osd_op_timeout`, which the Objecter arms on the watch registration and the unwatch as on every other OSD op (`_op_submit_with_budget`, osdc/Objecter.cc:597 and :2317-2324 at v19.2.6, :617 and :2385 at v20.2.4) |
| `WatchWithTimeout` holds the package-wide `watchersMtx` for writing across the synchronous `rados_watch3` (rados/watcher.go:113-123), and the notify and error callbacks and `NotifyEvent.Ack` take it for reading (:370-372, :383-385, :271-273). While one registration blocks, no other watch in the process delivers a notify, reports an error or acks (goceph acks from its dispatch goroutine, internal/radosclient/goceph/pool.go:516-519). The driver re-registers a lost control watch while the others stay registered. So while that registration stays blocked, every notify a gateway, radosgw or rgw-go, sends to a control object whose PG is healthy waits out its notify timeout, `client_notify_timeout`, 10 s by default (mds-client.yaml.in:131-134; IoCtxImpl.cc:1800-1802 at v19.2.6, :1824-1826 at v20.2.4). So does each of the up to `rgw_max_notify_retries` invalidations, 10 by default (rgw.yaml.in:3368 at v19.2.6, :3553 at v20.2.4), that radosgw's `robust_notify` and rgw-go's `distribute` send after a timeout (svc_notify.cc:481-511 at v19.2.6, :468-498 at v20.2.4; internal/driver/notify.go:258-271). Each metadata write in the zone then waits up to (1 + 10) × 10 s = 110 s, after which its notify fails with ETIMEDOUT and is logged, and radosgw's write returns its own result (svc_sys_obj_cache.cc:344-353 at both releases). That lasts as long as the PG blocking the registration stays unavailable, which with `rados_osd_op_timeout` at 0 has no bound. `Delete` takes the same lock (:180-185), so no watch closes meanwhile either. radosgw takes its `watchers_lock` only after the watch call returns (`register_watch`, svc_notify.cc:160-168 at v19.2.6, :125-133 at v20.2.4) | M Task 2 fix round | Open. Lifted in the fork by not holding `watchersMtx` across the C call, for example by handing the callbacks the watcher through `rados_watch3`'s `arg` as a `cgo.Handle` rather than looking it up by cookie |
| (rgw-go seam, not go-ceph) `radosclient.ExecResult` reports a positive class return as success but does not surface the value | Task 12 | Open; add an accessor if a class client needs the value |
