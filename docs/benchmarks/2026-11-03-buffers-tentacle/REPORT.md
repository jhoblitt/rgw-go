# Buffer strategies: tentacle

Three strategies for rgw-go's 4 MiB I/O buffers: none, the `sync.Pool`s rgw-go has, and one capped
free list, at 64 MiB and at 640 MiB. The builds and their specs ran on 2026-11-02; the measurement
ran on 2026-11-03 on the rooket cluster `rgw-go-tentacle`, Ceph v20.2.4. Squid was not measured.
"The first spike" below is the 2026-11-02 memory measurement,
[2026-11-02-memory-tentacle](../2026-11-02-memory-tentacle/REPORT.md), and "the allocator
measurement" is [2026-11-03-allocator-tentacle](../2026-11-03-allocator-tentacle/REPORT.md).
rgw-go's code is cited at fa179fa, the commit
[#546](https://github.com/jhoblitt/rgw-go/pull/546) merged (section 2). The files each table is
built from are in this directory (section 9).

## 1. Answer

Reuse earns its memory, and the way to do it is one capped free list shared by the head, read and
tail buffers. Allocating every buffer (`nopool`) served 82% fewer 4 KiB GETs at 64 in flight (5 945
against syncpool's 32 477 a second) at 11.4x the CPU per GET, 62% of it clearing a 4 MiB buffer to
read 4 KiB into it; it cost 24% more CPU per GiB of 64 MiB GETs and 570 MiB more memory at 32
concurrent 64 MiB PUTs. The free list capped at 640 MiB (`freelist-large`, 160 blocks, what 32 large
requests hold in flight) showed no measurable difference from the `sync.Pool`s in CPU, throughput or
latency on any load, and used less memory on every load that uses the buffers: VmRSS 129 against 209
MiB at 1-2 large requests in flight, 1 299 against 1 437 MiB at 32 concurrent 64 MiB GETs, and 369
against 792 MiB at 64 concurrent 4 KiB GETs. In that last cell, the allocator smoke run's 700 MiB,
the head-prefetch buffers are 95-96% of the live Go heap in every variant (316 MiB under syncpool):
syncpool's are all resident because every buffer its pool drops at a GC is allocated again and
zeroed, while a free list's are never zeroed and a 4 KiB prefetch touches one page of each. A 64 MiB
cap (`freelist-small`) is too small: it missed on 1 small GET in 19 and cost 32% more CPU per GET.
Size the cap to the concurrency a deployment expects, give it a way to shrink, and add a
gateway-wide read budget: 32 concurrent 64 MiB GETs already reach 1.3-1.44 GiB, and only
`rgw_max_concurrent_requests` bounds them. All 36 runs ran (tcmalloc preloaded,
`GOMEMLIMIT=1434MiB`); nothing was cut.

## 2. Method

### Variants

Four throwaway builds of rgw-go 5700129, each carrying the first-stripe fix: a part's first-stripe
buffer drawn from the tail pool, which [#546](https://github.com/jhoblitt/rgw-go/pull/546) has since
merged as fa179fa. The builds carry the fix as it stood before that PR's review; the merged commit
differs from it only in its specs and in what they use, a counter the merge dropped and an interface
the tail pool now sits behind. `syncpool` is the fix alone; the other three change the buffers on top
of it, on branches that were never merged. Each binary was built by `gobuild.sh go build
-tags=ceph_preview ./cmd/rgw-go`, RPATH `/home/jhoblitt/.local/lib/rgw-go-20.2.4` (librados 20.2.4
from the pinned image, not the system's); the binaries were not kept.

| variant | what it does |
|---|---|
| `nopool` | The three `sync.Pool` fields become a stand-in whose Get finds nothing and whose Put drops: every head, read and tail buffer is a fresh `make`. |
| `syncpool` | As it is: `headBufs`, `readBufs`, `tailBufs`. |
| `freelist-small` | One free list per buffer size (one list of 4 MiB blocks at the defaults), shared by all three uses, LIFO, capped at 64 MiB idle (16 blocks). |
| `freelist-large` | The same, capped at 640 MiB idle (160 blocks). |

Every variant keeps the pools' rules: a buffer a cancelled or abandoned op may still write into is
never handed back (statHead's and readMeta's context check, `recycle`, `release(..., false)`); only
exact-size buffers are reused (the lists are keyed by size, and `release` still pools only a buffer
of one chunk); the body budget's accounting is untouched. The freelist variants' `readBuf` no longer
decrements the spec counter `pooledReadBufs` when it pops a buffer, because the shared list can hand
it a head buffer; without that, `read_test.go:605` failed 2 of 2686 specs on the first freelist
build. The counter is read only by specs.

**The caps.** At the defaults, from the code:

- a whole 64 MiB GET holds a 4 MiB head buffer during its stat, which goes back before the data
  reads start, then at most 4 read buffers in its 16 MiB window;
- a whole 64 MiB PUT holds at most 5 tail buffers: 4 in its 16 MiB put window and the one being read
  from the body (its 4 MiB head is a plain `make`, unpooled in every variant);
- a 4 KiB GET without a Range holds one 4 MiB head buffer for its stat op; a 4 KiB PUT, a ranged GET
  and a HEAD hold none.

So 32 large requests in flight hold up to 32 x (16 MiB window + 4 MiB head) = 640 MiB of list
buffers, 160 blocks: `freelist-large` keeps that much idle. `freelist-small` keeps 64 MiB, 16
blocks: more than the 1-2 large requests in flight of the sustained load hold (2 x 20 MiB = 40 MiB),
a tenth of the 32-in-flight figure, and a quarter of the 64 head buffers (256 MiB) the 4 KiB GET
load holds. Inferred, and borne out by the runs (section 5): a list refills only to its cap, so
what decides how often a capped list allocates is how far the number of buffers in use swings, not
its peak.

### Driver specs

Each variant passed `gobuild.sh go test -tags=ceph_preview -race -count=1 ./internal/driver/...`
(the Makefile's `test` flags, `CCACHE_DISABLE=1`), 2686 specs; each run's `go test` lines are in
[specs.txt](specs.txt):

| variant | specs | binary sha256 |
|---|---|---|
| `nopool` | ok, 401.3 s | `539e8331...` |
| `syncpool` | ok, 391.8 s | `9485bfbd...` |
| `freelist-small` | ok, 393.8 s | `17f7d1f9...` |
| `freelist-large` | ok, 395.0 s | `0c8b55b8...` |

### Runs

Every run starts each phase in a fresh rgw-go, from a start script that is the first spike's with the
variant's binary: its own user systemd scope, `GOMAXPROCS`, `GOFLAGS` and the CGO flags unset,
`GODEBUG=gctrace=1,scavtrace=1` (scavtrace only prints the scavenger's releases once per GC cycle),
and the two settings the allocator measurement settled, given to the rgw-go process alone through
`env` inside its scope, as the allocator measurement's start script does:

- **Allocator: tcmalloc**, gperftools 2.9.1 with its defaults: `LD_PRELOAD` of
  `/home/jhoblitt/.local/lib/rgw-go-tcmalloc-2.9.1/libtcmalloc.so.4` (the image's file, sha256
  `f6af4a3a...132c`), every `TCMALLOC_*` unset; the start script refuses a process carrying one, and
  records the variables and the libraries the process has mapped. The allocator report found that the
  preload binds as linking does.
- **`GOMEMLIMIT=1434MiB`**, 0.7 of a 2 GiB pod, the ratio the allocator report derives for tcmalloc
  at 32 concurrent large PUTs. It replaces the plan's 1638MiB, 0.8 of the pod.

Each run runs under the host's build lock (the one gobuild.sh takes), one run at a time, and records
`/proc/loadavg` first.

**The load driver.** No elbencho build exists on this host, so the allocator measurement wrote a
small driver that reproduces the harness's elbencho request shapes, `s3load`, kept in that
measurement's directory as
[_s3load/main.go](../2026-11-03-allocator-tentacle/_s3load/main.go). This spike ran a frozen copy of
the same binary with the allocator measurement's own invocations, so the two measurements' loads are
the same. Throughputs compare within these two campaigns, not with the 2026-10-15 bench's elbencho
runs.

| load | phases, each in a fresh rgw-go |
|---|---|
| 1. sustained large, 1-2 in flight | the first spike's sustained load unchanged: 3 rounds of a 256 MiB PUT, an 8 MiB-part multipart upload of it 2 parts at a time, a full GET of each and a 128 MiB ranged GET |
| 2. 64 MiB at 32 threads | PUT: 32 threads write one whole 64 MiB object each (the harness's large-put whole-object phase); GET: 32 threads read their object whole for 60 s (large-get's whole-object phase); then the objects are removed and `gc process` runs |
| 3. 4 KiB at 64 threads | PUT: 64 threads write up to 3000 new 4 KiB objects each for 30 s (the allocator campaign's length), removed after; GET: 64 threads loop for 60 s over 64 x 400 objects written once at setup |

The harness's large-put and large-get each have a second phase (8 MiB-part uploads, 4 MiB ranged
GETs). Only the whole-object phases run here: they are the "64 MiB PUT" and "64 MiB GET" the plan
names, and a ranged GET takes no head buffer. No variant takes a pooled buffer for a 4 KiB PUT, so
that phase is a control.

Per phase: 5-10 s of idle after start, then the first spike's 0.2 s sampler (VmRSS, RssAnon, the
cgroup's `memory.current` and `memory.stat`) and 0.5 s `/metrics` poller (Go MemStats), a snapshot
at the phase's start and end and 15 s after (the cgroup's `cpu.stat` and `memory.stat`,
`/proc/PID/stat` and `status`, `/metrics`, the heap profile's MemStats), and in each GET phase a
30 s CPU profile from 15 s in (`/debug/pprof/profile`) and a heap profile 2 s after it: the live heap
by allocation site, from which the head buffers' share is the in-use bytes allocated under
`(*Store).headBuf`, in every variant. The GET phases are profiled in every run, not once per
variant, so that every run of a phase carries the same profiling overhead.

**Cluster state.** The loads signed as the bench harness's fixed-key user `bench`
(hack/bench/gateway/internal/gateway/run.go:27-29), created at setup; each run removed the objects
it wrote, and since the parity options turn the garbage collector off,
`radosgw-admin gc process --include-all` ran between runs; at the end the user was removed with its
buckets, and its usage log trimmed (`radosgw-admin usage trim --uid=bench`).

**What ran.** Setup at 00:26 on 2026-11-03 (the options the mons hold for `client.rgw.rgw-go` read
back: every buffer size 4 MiB, both windows 16 MiB, `rgw_max_concurrent_requests` 1024), a smoke
pass of each load with syncpool, checked by hand against the raw files (requests, bytes, removals),
then the campaign: 36 runs, 00:36 to 01:54, 3 repetitions x 3 loads x 4 variants, the variants' order
rotated each repetition, every run exit 0, nothing cut. The 1-minute load average before a run was
1.8-19.1; the high ones follow the previous run's own small-GET phase (8-16 cores) and other
sessions. One run waited ~8 minutes for the build lock behind an interim analysis run during the
campaign; no measured run overlapped anything holding the lock.

What each figure is, as the campaign's analyzer, a throwaway Go program with specs, computed it:

- **CPU**: the cgroup's `cpu.stat` user and system across the phase; per GiB moved and per
  request with rgw-go's own `rgw_go_request_bytes_total` (in plus out) and
  `rgw_go_request_duration_seconds_count`; throughput and the exact p50, p99 and p99.9 of the
  driver's request latencies (its JSON result).
- **Memory**: the highest 0.2 s sample from the phase's start to 1 s past its end, and the snapshot
  15 s after: VmRSS, RssAnon, working set (`memory.current - inactive_file`); the Go heap's
  HeapInuse and HeapSys - HeapReleased from `/metrics`.
- **GC and kernel**: GC cycles (`go_gc_duration_seconds_count`, which is NumGC); GC CPU, the sum of
  the five cpu fields of each gctrace line in the phase, which is that cycle's share of
  `/cpu/classes/gc/total:cpu-seconds`; allocated bytes (`go_memstats_alloc_bytes_total`,
  TotalAlloc); scavenger releases (scavtrace's background and eager KiB); minor faults
  (`/proc/PID/stat` minflt) and the cgroup's `pgfault`.
- **Profiles**: each sample in one class, by its stack: `memclrNoHeapPointers*` as the leaf (and the
  part of it under `mallocgc`), the GC, the scavenger, the rest of the allocator, librados (cgo and
  non-Go threads), syscalls. `perf_event_paranoid` is 2 here, so no kernel samples: pprof charges a
  page fault's kernel time to whichever user frame touched the page first.

Each step above ran from a throwaway script, shell or Go: the setup, start, phase, snapshot, run,
campaign and cleanup scripts and the analyzer. None was kept.

## 3. CPU

Medians of 3 runs, the lowest and highest in brackets. CPU is the rgw-go cgroup's user + system
across the phase. "No measurable difference" means the ranges overlap. Generated tables with every
column: [tables.md](tables.md), one line per run in [summary.tsv](summary.tsv). Each table below
names the tables.md heading it comes from.

**Load 1, sustained large at 1-2 in flight** (3.38 GiB, 118 requests per run), from "sustained sus:
CPU":

| variant | CPU s | of which system | CPU s/GiB | CPU ms/request | wall s |
|---|---|---|---|---|---|
| nopool | 5.07 (5.06-5.15) | 1.37 | 1.502 (1.498-1.525) | 43.0 | 14.5 (14.1-15.7) |
| syncpool | 4.30 (4.27-4.35) | 1.16 | 1.275 (1.265-1.288) | 36.5 | 15.1 (13.8-16.9) |
| freelist-small | 4.29 (4.25-4.30) | 1.15 | 1.272 (1.258-1.274) | 36.4 | 15.7 (15.2-18.1) |
| freelist-large | 4.23 (4.22-4.26) | 1.12 | 1.252 (1.251-1.263) | 35.8 | 15.1 (14.3-16.7) |

nopool costs 18% more CPU. freelist-small and syncpool: no measurable difference. freelist-large
is 1.8% below syncpool, just outside the ranges (1.263 against 1.265): small, but every run.

**Load 2, 64 MiB at 32 threads.** PUT phase (32 PUTs, 2 GiB), from "large put: CPU":

| variant | CPU s | of which system | CPU s/GiB | MiB/s | p50 ms | p99 ms |
|---|---|---|---|---|---|---|
| nopool | 8.23 (7.91-8.39) | 3.18 | 4.117 (3.953-4.194) | 593 (491-600) | 3414 | 3453 |
| syncpool | 7.85 (7.71-7.91) | 2.91 | 3.923 (3.857-3.953) | 606 (563-610) | 3335 | 3379 |
| freelist-small | 8.11 (7.80-8.18) | 3.08 | 4.056 (3.901-4.088) | 572 (569-609) | 3531 | 3579 |
| freelist-large | 7.90 (7.88-8.19) | 3.04 | 3.952 (3.942-4.097) | 555 (499-601) | 3638 | 3687 |

No measurable difference among the three reusing variants. nopool is 5% above syncpool, its range
touching syncpool's at 3.95: at the edge of measurable. One 3.5 s wave of 32 uploads is a short
phase; the large-object CPU difference shows better in the GET phase.

GET phase (60 s of whole 64 MiB GETs, 34 GiB, 544 requests), from "large get: CPU":

| variant | CPU s | of which system | CPU s/GiB | MiB/s | p50 ms | p99 ms | p99.9 ms |
|---|---|---|---|---|---|---|---|
| nopool | 18.84 (18.71-19.60) | 12.42 | 0.554 (0.550-0.576) | 565 (551-568) | 3656 | 3941 | 4039 |
| syncpool | 15.20 (14.76-15.29) | 10.99 | 0.447 (0.434-0.450) | 556 (554-567) | 3636 | 3956 | 4108 |
| freelist-small | 15.54 (12.04-15.64) | 10.95 | 0.460 (0.457-0.488) | 564 (407-567) | 3608 | 4681 | 4821 |
| freelist-large | 15.16 (14.92-15.28) | 10.90 | 0.437 (0.432-0.446) | 570 (555-581) | 3576 | 4044 | 4127 |

nopool costs 24% more CPU per GiB at the same throughput, 1.4 s of it system time. syncpool and
freelist-large: no measurable difference. freelist-small is 3% above both, outside their ranges; one of its runs
moved only 395 requests (407 MiB/s, p99 6.3 s), the others matched.

**Load 3, 4 KiB at 64 threads.** PUT phase (30 s; no variant takes a pooled buffer for it), from
"small put: CPU":

| variant | CPU ms/request | requests/s | p50 ms | p99 ms |
|---|---|---|---|---|
| nopool | 0.765 (0.741-0.777) | 1630 (1608-1808) | 38.0 | 59.5 |
| syncpool | 0.745 (0.737-0.773) | 1600 (1495-1779) | 38.7 | 63.0 |
| freelist-small | 0.757 (0.730-0.764) | 1648 (1623-1774) | 37.8 | 62.3 |
| freelist-large | 0.740 (0.737-0.756) | 1775 (1670-1790) | 34.9 | 61.0 |

No measurable difference, as the code predicts: the control behaves as one.

GET phase (60 s, the decisive cell), from "small get: CPU":

| variant | CPU s | of which system | CPU ms/request | requests/s | p50 ms | p99 ms | p99.9 ms |
|---|---|---|---|---|---|---|---|
| nopool | 982 (975-994) | 107 | 2.754 (2.744-2.777) | 5945 (5924-5965) | 10.4 | 20.8 | 26.5 |
| syncpool | 469 (466-469) | 95 | 0.241 (0.239-0.243) | 32477 (31967-32728) | 1.9 | 2.8 | 3.6 |
| freelist-small | 525 (524-527) | 99 | 0.318 (0.308-0.325) | 27639 (26921-28301) | 2.1 | 5.5 | 8.5 |
| freelist-large | 472 (470-472) | 95 | 0.240 (0.239-0.240) | 32778 (32562-32928) | 1.9 | 2.7 | 3.4 |

nopool serves 82% fewer GETs, at 11.4x the CPU per GET, with 16 cores busy. freelist-small serves
15% fewer at 32% more CPU per GET. syncpool and freelist-large: no measurable difference in CPU,
throughput or latency.

## 4. Memory

MiB. Peak: the highest 0.2 s sample from the phase's start to 1 s past its end. +15 s: the
snapshot 15 s after the phase. Heap resident is HeapSys - HeapReleased; HeapInuse and it are the
highest 0.5 s `/metrics` sample. Working set (WS) is `memory.current - inactive_file`. Every table
here is from [tables.md](tables.md), under the heading it names.

**Load 1, sustained large at 1-2 in flight**, from "sustained sus: memory, MiB":

| variant | VmRSS peak | RssAnon peak | WS peak | VmRSS +15 s | RssAnon +15 s | HeapInuse peak | heap resident peak | heap resident +15 s |
|---|---|---|---|---|---|---|---|---|
| nopool | 121 (121-125) | 85 | 91 | 90 (78-117) | 54 | 35 | 49 | 13 (5-44) |
| syncpool | 209 (207-223) | 173 | 178 | 213 (211-225) | 177 | 142 | 150 | 152 (148-154) |
| freelist-small | 130 (129-131) | 95 | 101 | 131 (129-132) | 95 | 49 | 55 | 55 (54-57) |
| freelist-large | 129 (126-131) | 93 | 104 | 130 (123-131) | 94 | 48 | 54 | 53 (50-54) |

Both free lists hold 80 MiB less than syncpool, at the peak and after; nopool holds least, and
gives most of it back within 15 s. The whole sustained load allocates five 4 MiB buffers in either
free list (section 5), because one list serves the PUT's tail buffers, the GETs' read buffers and
the heads alike; syncpool's three pools allocate 22.

**Load 2, 64 MiB at 32 threads.** PUT phase, from "large put: memory, MiB":

| variant | VmRSS peak | RssAnon peak | WS peak | VmRSS +15 s | HeapInuse peak | heap resident peak | heap resident +15 s |
|---|---|---|---|---|---|---|---|
| nopool | 1919 (1916-1919) | 1884 | 2082 | 1919 | 1243 | 1349 | 1348 |
| syncpool | 1353 (1350-1354) | 1317 | 1522 | 1357 | 785 | 786 | 790 |
| freelist-small | 1350 (1349-1350) | 1314 | 1513 | 1355 | 782 | 784 | 786 |
| freelist-large | 1350 (1349-1350) | 1314 | 1508 | 1354 | 782 | 784 | 786 |

GET phase, from "large get: memory, MiB":

| variant | VmRSS peak | RssAnon peak | WS peak | VmRSS +15 s | HeapInuse peak | heap resident peak | heap resident +15 s |
|---|---|---|---|---|---|---|---|
| nopool | 1371 (1367-1375) | 1336 | 1344 | 1340 (1294-1348) | 1284 | 1296 | 1261 |
| syncpool | 1437 (1433-1438) | 1402 | 1409 | 1437 (1433-1442) | 1345 | 1362 | 1357 |
| freelist-small | 1346 (1296-1369) | 1310 | 1318 | 1123 (928-1194) | 1109 | 1274 | 1043 |
| freelist-large | 1299 (1194-1319) | 1263 | 1271 | 1299 (1194-1319) | 1179 | 1223 | 1223 |

- At 32 concurrent 64 MiB PUTs nopool uses 570 MiB more than any reusing variant; the three reusing
  variants do not differ. The PUT's C heap (tcmalloc holding librados's copies of the writes) is
  RssAnon less the heap, ~530 MiB in every variant.
- At 32 concurrent 64 MiB GETs freelist-large holds 138 MiB less than syncpool (outside the ranges),
  and syncpool's VmRSS, 1437 MiB, is the highest of the four: its heap is at the 1434 MiB limit.
  freelist-small's peak overlaps the others'; it is lowest 15 s after, by dropping what its 64 MiB
  cap will not keep.

**Load 3, 4 KiB at 64 threads.** PUT phase ("small put: memory, MiB"): every variant 95 (95-97) MiB
VmRSS at the peak, 85-87 after, HeapInuse 13: no measurable difference. GET phase, from "small get:
memory, MiB":

| variant | VmRSS peak | RssAnon peak | WS peak | VmRSS +15 s | RssAnon +15 s | HeapInuse peak | heap resident peak | heap resident +15 s |
|---|---|---|---|---|---|---|---|---|
| nopool | 753 (737-887) | 717 | 727 | 609 (592-616) | 572 | 653 | 803 | 567 |
| syncpool | 792 (790-809) | 755 | 764 | 776 (771-776) | 740 | 660 | 736 | 700 |
| freelist-small | 649 (648-667) | 613 | 622 | 611 (584-612) | 574 | 520 | 601 | 549 |
| freelist-large | 369 (369-375) | 333 | 342 | 369 (369-375) | 333 | 525 | 544 | 544 |

**The head buffers' share of the Go heap in this cell** (the lead from the allocator measurement's
smoke run, 697 MiB VmRSS and a ~680 MB heap). From the heap profile taken 2 s after the CPU profile,
47 s into the phase, which gives the live heap as of the GC before it by allocation site; "head
buffers" are the in-use bytes allocated under `(*Store).headBuf`. From "small get: heap profiles:
in-use MiB and the head buffers' part", with the GETs from "small get: CPU":

| variant | HeapInuse then | live heap | head buffers | share of the live heap | share of HeapInuse | head buffers allocated in the run | per GET |
|---|---|---|---|---|---|---|---|
| nopool | 424 (253-441) | 264 | 252 (244-304) | 95% | 59% | 356 615 | 1 |
| syncpool | 589 (442-599) | 328 | 316 (312-316) | 96% | 54% | 2 931 | 1 in 665 |
| freelist-small | 436 (355-456) | 255 | 248 (180-248) | 96% | 57% | 86 181 | 1 in 19 |
| freelist-large | 418 (309-469) | 267 | 256 (256-256) | 96% | 61% | 64 | 64 in all |

- **The lead holds.** In every variant the head buffers are 95-96% of the live heap of a 64-way
  small-GET load: 62-79 buffers of 4 MiB, one per GET in flight and, under syncpool, ~15 more
  pooled. The rest of HeapInuse is garbage between collections (GOGC 100 lets the heap reach twice
  the live heap), almost all of it the GETs' other allocations, ~50 KiB per GET (section 5).
- **What makes syncpool's heap resident is reuse through the GC, not its size.** freelist-large
  holds the same 256 MiB of head buffers in a heap the same size (HeapInuse 525 against 660, heap
  resident 544 against 736), yet its RssAnon is 333 MiB against syncpool's 755: at least 211 MiB of
  its committed Go heap is not resident. A 4 KiB object's prefetch writes one page of its 4 MiB
  buffer (librados copies only the bytes read,
  [librados_c.cc:4221-4261](https://github.com/ceph/ceph/blob/v20.2.4/src/librados/librados_c.cc#L4221-L4261)),
  and a free-list buffer, taken fresh from the OS once and never freed, is never zeroed, so 1023 of
  its 1024 pages stay untouched. syncpool's pool loses its buffers at each GC's cleanup, so 2 931
  are allocated again from freed heap, which `mallocgcLarge` zeroes whole, making all 4 MiB
  resident. This is inferred from those mechanisms (verified in source) and the RssAnon-against-heap
  gap and fault counts measured; no page-level map of a buffer was taken.

## 5. GC, allocation and page faults

Per phase, medians (ranges in [tables.md](tables.md)). GC cycles are `go_gc_duration_seconds_count`
across the phase. GC CPU is the sum of the cycles' gctrace cpu fields, each cycle's share of
`/cpu/classes/gc/total:cpu-seconds`; with GOMAXPROCS 32 it counts each stop-the-world phase 32
times over (mgc.go:1610, 1614 at go1.27.3), so where cycles are frequent it overstates the CPU
burnt, and the profiles' GC share (section 6) is the better measure. Allocated is
`go_memstats_alloc_bytes_total` across the phase. Scavenged is scavtrace's background and eager
releases. Faults are the process's minor faults (`/proc/PID/stat`); the cgroup's `pgfault` agrees
to within 0.1% in every run. From each load and phase's "GC, allocation, faults" in tables.md:

| load, phase | variant | GC cycles | GC CPU s (of which STW x P) | allocated GiB | allocated / moved | scavenged MiB | minor faults | faults / MiB moved |
|---|---|---|---|---|---|---|---|---|
| sustained | nopool | 252 | 1.85 (1.52) | 3.8 | 1.13 | 454 | 120 400 | 34.8 |
| sustained | syncpool | 8 | 0.02 | 0.2 | 0.06 | 0 | 40 400 | 11.7 |
| sustained | freelist-small | 5 | 0.03 | 0.1 | 0.02 | 0 | 19 400 | 5.6 |
| sustained | freelist-large | 5 | 0.01 | 0.1 | 0.02 | 0 | 19 300 | 5.6 |
| large PUT | nopool | 11 | 0.04 | 2.0 | 1.00 | 1 | 477 200 | 233 |
| large PUT | syncpool | 10 | 0.00 | 0.8 | 0.38 | 0 | 332 800 | 163 |
| large PUT | freelist-small | 9 | 0.00 | 0.8 | 0.38 | 0 | 332 000 | 162 |
| large PUT | freelist-large | 9 | 0.00 | 0.8 | 0.38 | 0 | 332 100 | 162 |
| large GET | nopool | 77 | 0.46 (0.18) | 36.2 | 1.06 | 3 421 | 1 200 900 | 34.5 |
| large GET | syncpool | 10 | 0.03 | 2.9 | 0.09 | 0 | 365 000 | 10.5 |
| large GET | freelist-small | 22 | 0.09 | 6.4 | 0.20 | 789 | 483 400 | 13.9 |
| large GET | freelist-large | 12 | 0.03 | 2.7 | 0.08 | 0 | 326 400 | 9.0 |
| small PUT | nopool | 2 255 | 21.6 (12.7) | 3.8 | 20.4 | 435 | 131 300 | 697 |
| small PUT | syncpool | 2 175 | 21.0 (12.1) | 3.7 | 20.4 | 397 | 120 300 | 667 |
| small PUT | freelist-small | 2 257 | 21.4 (12.8) | 3.8 | 20.4 | 458 | 136 600 | 707 |
| small PUT | freelist-large | 2 405 | 21.3 (12.3) | 4.1 | 20.4 | 572 | 167 600 | 798 |
| small GET | nopool | 5 726 | 178.7 (101.7) | 1 410.6 | 1 037 | 60 427 | 5 382 000 | 3 849 |
| small GET | syncpool | 350 | 5.1 (2.4) | 103.9 | 14.0 | 1 122 | 412 700 | 54.2 |
| small GET | freelist-small | 1 780 | 25.7 (13.6) | 417.6 | 65.6 | 17 331 | 3 615 800 | 558 |
| small GET | freelist-large | 388 | 5.4 (2.7) | 93.5 | 12.5 | 0 | 84 000 | 10.9 |

How many 4 MiB buffers each process allocated from its start to 15 s after the phase (the after
heap profile's `alloc_space`, by allocation site; a free list's buffer counts where it was first
allocated, whatever it served after), from the "head bufs allocated", "read bufs allocated" and
"tail bufs allocated" columns of each load and phase's "heap profiles" table in tables.md:

| load, phase | nopool (head, read, tail) | syncpool | freelist-small | freelist-large |
|---|---|---|---|---|
| sustained | 108, 477, 381 | 7, 5, 10 | 0, 0, 5 | 0, 0, 5 |
| large PUT | 0, 0, 324 | 0, 0, 37 | 0, 0, 33 | 0, 0, 36 |
| large GET | 544, 7 902, 0 | 47, 131, 0 | 32, 931, 0 | 32, 97, 0 |
| small GET | 356 615, 0, 0 | 2 931, 0, 0 | 86 181, 0, 0 | 64, 0, 0 |

What these show:

- **nopool's three costs are all there, and scale with buffers per byte.** Per 64 MiB GET it
  allocates 15.5 buffers; per 4 KiB GET one whole 4 MiB buffer, so the small GETs allocate 1 037
  bytes for every byte they serve: 1.4 TiB in a minute, 5 726 GC cycles, 59 GiB released by the
  scavenger and 5.4 million faults (21 GiB of pages) taking memory back.
- **sync.Pool reuses well under steady load, and loses buffers at every GC.** It misses on 1 small
  GET in 665, the buffers its cleanup dropped at the GCs (inferred from sync.Pool's poolCleanup,
  sync/pool.go:258-277 at go1.27.3); its large-GET heap is the highest of the four (162 pool
  buffers at the mid-phase profile against at most 128 read buffers in flight; section 4).
- **The free lists allocate least,** because one list feeds every use: five buffers carry the whole
  sustained load. freelist-large's small-GET phase allocates its 64 buffers once and never again: no
  scavenging at all, and a fifth of syncpool's faults.
- **A capped list allocates whenever demand swings past its cap.** freelist-small (16 buffers)
  misses on 1 small GET in 19 and frees as fast as it allocates: 17 GiB scavenged and 3.6 million
  faults, more than syncpool. Section 2's inference holds: what decides is how far the number of
  buffers in use swings, and 64 GETs completing and starting in bursts swing it by more than 16
  (inferred from the miss rate; the number in use was not sampled).
- **The 4 KiB PUT is a control, and its GC is a separate matter.** All four allocate 20 bytes per
  byte served and run ~2 200 GC cycles in 30 s on a 13 MiB heap; that is the request path's own
  garbage (~80 KiB per PUT), not buffers.
- **Page faults cost about 2 µs each** (inferred from the differences, not measured directly):
  nopool against syncpool adds 1.4 s of system time for 836 000 more faults on the large GETs, 12.3 s
  for 4.97 million on the small GETs, and 0.21 s for 80 000 on the sustained load: 1.7, 2.5 and
  2.6 µs a fault, so about 2 ms to fault in one 4 MiB buffer. pprof cannot see kernel time here
  (`perf_event_paranoid` is 2).

## 6. Where the CPU went, from the profiles

One 30 s CPU profile in every GET run (from 15 s in), so three per variant per GET load; medians
of the three, percent of the profile's sampled time. Each sample is in one class, by its stack:
`memclrNoHeapPointers*` as the leaf (and the part of it under `mallocgc`, the zeroing of a new
allocation), the GC (mark workers, assists, sweeping, its stop-the-world phases), the scavenger, the
rest of `mallocgc` (the allocator), librados (cgo calls and threads the runtime does not run), and
syscalls. The rest is the request path itself. `perf_event_paranoid` is 2, so pprof has no kernel
samples: a page fault's time is charged to the user frame that touched the page. Both tables are
from [tables.md](tables.md), "large get: CPU profile shares" and "small get: CPU profile shares".

**64 MiB GET at 32 threads** (~0.25 cores sampled):

| variant | memclr % (in mallocgc) | GC % | scavenger % | allocator % | librados % | syscalls % | sampled s |
|---|---|---|---|---|---|---|---|
| nopool | 8.5 (8.5) | 1.4 | 2.1 | 0.8 | 51.7 | 0.8 | 8.7 |
| syncpool | 0.0 | 0.0 | 0.0 | 0.3 | 56.1 | 0.6 | 6.9 |
| freelist-small | 1.9 (1.9) | 0.0 | 0.4 | 0.4 | 54.5 | 0.7 | 7.2 |
| freelist-large | 0.0 | 0.1 | 0.0 | 0.4 | 56.1 | 0.3 | 7.1 |

**4 KiB GET at 64 threads** (8-17 cores sampled):

| variant | memclr % (in mallocgc) | GC % | scavenger % | allocator % | librados % | syscalls % | sampled s |
|---|---|---|---|---|---|---|---|
| nopool | 62.4 (62.4) | 8.9 | 0.8 | 5.1 | 5.3 | 1.0 | 499.5 |
| syncpool | 1.2 (1.1) | 2.2 | 0.1 | 8.7 | 20.9 | 2.6 | 231.4 |
| freelist-small | 14.8 (14.7) | 4.5 | 0.7 | 8.0 | 17.1 | 2.3 | 261.1 |
| freelist-large | 0.9 (0.8) | 2.1 | 0.0 | 8.6 | 21.4 | 2.6 | 232.1 |

- **Zeroing is the cost of not reusing.** nopool's small GETs spend 62% of their CPU, 10.4 cores
  for the 30 s, in `memclrNoHeapPointers` under `mallocgcLarge`: clearing 4 MiB to read 4 KiB into
  it. That is what cuts its throughput to 5 945 GETs/s. On 64 MiB GETs the same clearing is 8.5%,
  plus 2.1% in the scavenger, which returns the freed spans to the kernel only for the next
  allocation to fault them back.
- **The GC's own share is second.** 8.9% for nopool's small GETs, 1.4% for its large ones; 2.1-2.2%
  for the pools on small GETs, about what the request path's own garbage costs.
- **The allocator proper is not the cost:** 5-9% on small GETs in every variant, the request path's
  ~50 KiB per GET of small objects; under 1% on large GETs.
- **freelist-small spends 14.8% zeroing**, a quarter of nopool's share, from its misses.
- **syncpool and freelist-large profile alike.** Their small-GET memclr, GC and allocator shares
  agree to within half a point; librados is a fifth of the CPU, the rest the S3 path.
- Page faults do not appear as such. Where a fresh or scavenged buffer is first written by librados,
  the fault time is in librados's samples or outside the Go threads; the fault counts and system time
  (section 5) are their measure.

## 7. Recommendation

**Reuse earns its memory, and the way to do it is one capped free list.** Replace the three
`sync.Pool`s with `freelist-large`'s design: one free list per buffer size (one list of 4 MiB
blocks at the defaults), shared by the head, read and tail buffers, LIFO, capped.

- **Against nopool:** on 4 KiB GETs at 64 in flight nopool serves 82% fewer GETs (5 945 against
  32 778 a second) at 11.4x the CPU per GET, 62% of it clearing buffers; on 64 MiB GETs it costs 24%
  more CPU per GiB; on 64 MiB PUTs it uses 570 MiB more memory. Only at 1-2 large requests in flight
  is it the smallest (121 against 129 MiB peak), for 18% more CPU. Allocating is not an option.
- **Against syncpool:** no measurable difference in CPU, throughput or latency on any load (1.8%
  less CPU per GiB on the sustained load, at the edge of the ranges), and less memory on every load
  that touches the pools: 129 against 209 MiB VmRSS at 1-2 in flight, 1299 against 1437 MiB at 32
  concurrent 64 MiB GETs, 369 against 792 MiB at 64 concurrent 4 KiB GETs; the same at 32 concurrent
  64 MiB PUTs. The lists win on memory three ways: one list serves all three uses, so buffers move
  between them (five carry the whole sustained load); it keeps no per-P surplus and no victim
  generation; and a buffer that never goes back to the heap is never zeroed, so a 4 KiB prefetch
  leaves 1023 of its buffer's 1024 pages untouched and not resident.

**The cap.** It must cover how far the buffers in use swing at the concurrency the deployment
expects, which under bursty traffic is most of them: a list refills only to its cap, and every swing
past it allocates. 64 MiB (16 blocks) did
not cover 64 concurrent small GETs, which hold 64 head buffers: it missed on 1 GET in 19 and cost
32% more CPU per GET than syncpool. 640 MiB (160 blocks) covered every load here; the most
freelist-large's buffers came to, in use and idle together, was 129 blocks (516 MiB, the mid-phase
heap profile), at 32 concurrent 64 MiB GETs. So:

- size it as the in-flight bound of the read and write paths: requests expected in flight x (16 MiB
  window + 4 MiB head), 640 MiB for 32 at the defaults;
- the price is that the list never gives memory back: its buffers are live heap, inside
  `GOMEMLIMIT` and the GC goal, which the GC cannot reclaim under pressure, where `sync.Pool` drains
  after two GCs. 15 s after the 32-way GETs freelist-large still held 1223 MiB of heap resident
  (syncpool 1357, which would fall at its next two GCs; not measured past 15 s). With the 2 GiB pod's
  `GOMEMLIMIT` of 1434 MiB, 640 MiB of idle buffers is 45% of the limit. Bound it below the limit
  with room for the live heap, and trim it when idle (inferred; not built or measured here).

**A gateway-wide read budget: yes, the measurements say GETs need one.** At the defaults each
whole-object GET holds up to 16 MiB of read buffers plus a 4 MiB unpooled copy of its head
(`stat.go:284`), and only `rgw_max_concurrent_requests` (1024) bounds how many run. 32 concurrent
64 MiB GETs already reached 1.3-1.44 GiB of VmRSS, syncpool's heap at the 1434 MiB limit; at 64 the
read buffers alone would be ~1 GiB (inferred), past what a 2 GiB pod holds beside the C heap. The
PUT side's `bodyBudget` does not bound a 2 GiB pod either: at the defaults it is max(100 MiB,
1024 x 4 MiB) = 4 GiB (`bodybudget.go`, `docs/exclusions.md`). The free list's cap and a read budget
should be the same number: the list then never needs more than the budget lets be in flight, and
GETs past it wait instead of growing the heap.

**What the change would touch** (from the two free-list builds):

- `internal/driver/buflist.go`, new: the lists; `internal/driver/store.go`: the three pool fields
  become one, and Open sizes its cap;
- `internal/driver/stat.go` (`headBuf`, `statHead`'s hand-back), `mp_layout.go` (`readMeta`'s),
  `read.go` (`readBuf`, `recycle`), `stripe.go` (`tailBuf`, `release`): each Get and Put, with their
  return rules, the exact-size rule and the body budget unchanged;
- the spec counter `pooledReadBufs` becomes a count of hand-backs (`read_test.go:605` assumed the
  head buffers had a pool of their own), and a spec of the list itself (cap, LIFO, sizes);
- for the read budget: a semaphore beside each GET's window in `read.go`'s `issue`, sized at Open
  as `bodyBudgetSize` is, and a `docs/exclusions.md` entry, since radosgw has none (GETs can wait
  where radosgw's would allocate). The free list alone needs no entry: no client can see it;
- a follow-up worth measuring: serve a GET's head from its head buffer for the request's life
  instead of cloning it (36-128 MiB of head copies in the 32-way GET heap profiles), and draw a
  PUT's 4 MiB head (`stripe.go:420`) from the list (128 MiB at 32 concurrent PUTs).

## 8. Open questions

- **The list never shrinks.** Nothing here ran longer than 15 s after a load, so freelist-large's
  idle retention (up to its 640 MiB cap, in the heap and under `GOMEMLIMIT`) was not set against
  syncpool's drain after two GCs over minutes. A trim on idle, or a cap tied to the memory limit, is
  unbuilt and unmeasured.
- **The read budget is argued, not run.** No load went past 32 concurrent large GETs; the case for
  a gateway-wide budget extrapolates their ~20 MiB per GET in flight. Its waits under load, and its
  exclusion entry, are unmeasured and unwritten.
- **Untouched pages are inferred, not mapped.** freelist-large's 64-way small-GET RssAnon (333 MiB)
  sits 211 MiB below its committed heap; the explanation (a never-freed buffer is never zeroed, and a
  4 KiB prefetch writes one page) follows from the source and the counts, but no page map of a
  buffer was taken. A deployment's derived image could behave differently if anything there touches
  the whole buffer.
- **The free list was not raced against sync.Pool under -race beyond the driver specs**, and its
  mutex was not profiled at higher concurrency than 64; it showed in no profile here.
- **The load driver is not elbencho.** It is the allocator measurement's `s3load`, which reproduces
  the harness's request shapes but not elbencho's libcurl client; throughputs compare within this
  campaign and with the allocator measurement, not with the 2026-10-15 bench.
- **The plan's "every GET and HEAD" takes a head buffer:** the code says every GET without a Range
  (`getobject.go:413-414`); HEAD and ranged GETs take none.
- **gctrace's GC CPU overstates** where cycles are frequent: it counts each stop-the-world phase
  once per P (32 here). The 4 KiB PUT's ~2 200 cycles in 30 s on a 13 MiB heap, the same in every
  variant, are the request path's own garbage (~80 KiB a PUT); a separate question for rgw-go.
- **One host, three runs each.** Load averages before runs were 1.8-19.1, the high ones after the
  previous run's own 8-16 cores of small GETs and other sessions' builds; ranges were tight except
  freelist-small's one slow large-GET run. Squid was not measured.
- **Process.** An interim analysis during repetition 2 took the build lock once per profile and
  made one run wait ~8 minutes (no measured run overlapped it). A `docs/porting-process.md`
  line, drafted here only: "Running analysis that takes the build lock per command during a
  measurement campaign starves the campaign's runs; analyse after, or under one lock hold."

## 9. Raw data

The files are the small summaries the tables are built from; the CPU and heap profiles, gctrace
and scavtrace logs, sampler and `/metrics` series, the driver's JSON results, the binaries and the
scripts stayed out.

- **[tables.md](tables.md)**, per load, phase and variant, the median and range over the variant's
  runs of every figure: CPU and the driver's throughput and latency; memory; GC, allocation and
  faults; the heap profiles' in-use MiB by buffer kind, mid-phase and 15 s after, and the 4 MiB
  buffers allocated since start; and for the GET phases the CPU profile's shares.
- **[summary.tsv](summary.tsv)**, one line per run and phase, 60 in all, with every column tables.md
  summarizes, in its units; -1 marks a figure the phase has none of, such as a profile share outside
  the GET phases, and the sustained load's driver columns (MiB/s, req/s, latencies) read 0, since
  aws-cli, not the driver, ran it.
- **[specs.txt](specs.txt)**, each variant's `go test` lines from its driver spec run.
