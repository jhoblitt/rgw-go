# Allocator comparison: tentacle

glibc malloc against tcmalloc in rgw-go, for CPU and memory. Measured from 2026-11-02 21:57 to
2026-11-03 00:10 (-07:00) on the rooket cluster `rgw-go-tentacle`, Ceph v20.2.4, with rgw-go
5700129; rgw-go's code is cited at that commit. Squid was not measured. "The spike" below is the
2026-11-02 memory measurement, [2026-11-02-memory-tentacle](../2026-11-02-memory-tentacle/REPORT.md).
The files each table is built from are in this directory (section 9).

## 1. Answer

Link tcmalloc, as radosgw does. Against glibc's defaults it cost no measurable CPU on any load
(4.29 against 4.39 s for the sustained 256 MiB load, 0.760 against 0.751 ms per 4 KiB PUT at 64 in
flight, 0.48 against 0.46 CPU-s per GiB of 64 MiB GETs at 32 in flight), and about 4% less on 64 MiB
PUTs at 32 in flight (14.39 against 14.95 s). It held the C heap to what librados had in flight:
28 MiB after the sustained load against glibc's 181, and 521 MiB after 32 concurrent 64 MiB PUTs
against glibc's 869-895 MiB (glibc's peak 1278-1444). Under the sustained load that made VmRSS 35%
lower, 295 against 455 MiB (radosgw 227). `MALLOC_ARENA_MAX=2` kept as little at 1-2 in flight, and
less after a 32-way load (318-411 MiB, though its peak was higher, 595-626), but cost 7% more CPU per
small PUT at 64 in flight. tcmalloc's faster release (`TCMALLOC_AGGRESSIVE_DECOMMIT`) gave the C heap
back (17 MiB after) for 28% more CPU on the sustained load and 70% more per GiB of large GETs; keep
its defaults. No allocator closes the gap to radosgw at 32 in flight, 3.4-4.7x in VmRSS: there the Go
heap is 1.2-2.2 GB of rgw-go's 1.5-3.0 GiB. With tcmalloc, a 2 GiB pod at 32 concurrent large PUTs
leaves room for a `GOMEMLIMIT` of 0.7 of the limit, not 0.8. `go test -race` passes with tcmalloc
preloaded. Cut to fit the time: `tcm-radosgw` (radosgw's environment sets no `TCMALLOC_*`), the
optional release-on-a-timer build, and small-object GET.

## 2. Method

**Cluster and binary.** rooket `rgw-go-tentacle` (Ceph v20.2.4, one 10 GiB OSD, Rook's chart
radosgw), both gateways on the same zone under the parity options, which both entities already held
(read with `ceph config get`; nothing set). One rgw-go binary for every variant: origin/main
5700129 (`57001292892821f877fd8bcc596fbb7a99c20637`, `vcs.modified=false`), built once with
`CCACHE_DISABLE=1 gobuild.sh go build -tags=ceph_preview`, sha256 `1b871259...0ec`, go1.27.3,
linking the librados 20.2.4 set in `~/.local/lib/rgw-go-20.2.4`. The host's glibc is 2.42.

**Variants.** Each run starts rgw-go fresh in a user systemd scope of its own (so it has a cgroup),
with rgw-go-up.sh's command line, `GOMAXPROCS` and `GOFLAGS` unset (so `GOMAXPROCS` is the host's
32), `GODEBUG=gctrace=1`, and the variant's environment given to the rgw-go process alone (`env`
inside the scope); the start script records the environment and libraries the process actually has.

| name | allocator | settings |
|---|---|---|
| `glibc` | glibc 2.42 | defaults |
| `glibc-arena2` | glibc 2.42 | `MALLOC_ARENA_MAX=2` |
| `tcm` | gperftools 2.9.1 tcmalloc, `LD_PRELOAD` | defaults |
| `tcm-release` | the same | `TCMALLOC_AGGRESSIVE_DECOMMIT=true` |
| `radosgw` | gperftools 2.9.1 tcmalloc, linked | the pod's (no `TCMALLOC_*`) |

- **The tcmalloc library** is `libtcmalloc.so.4.5.9` copied out of `quay.io/ceph/ceph:v20.2.4`
  (image `6facee348180`, gperftools-libs-2.9.1-3.el9) with `podman create` and `podman cp` into
  `/home/jhoblitt/.local/lib/rgw-go-tcmalloc-2.9.1/`, byte-identical (sha256 `f6af4a3a...132c`, the
  RPM's own digest for the file). It has no RUNPATH, and the host resolves everything it NEEDs
  (libunwind.so.8, libstdc++, libm, libc, libgcc_s), so nothing else was copied or patched; its
  libunwind is the host's, which matters only for stack capture, and the default
  `TCMALLOC_SAMPLE_PARAMETER` of 0 leaves sampling off
  ([sampler.cc:52-56](https://github.com/gperftools/gperftools/blob/gperftools-2.9.1/src/sampler.cc#L52-L56)).
- **It is the process's malloc from the first allocation.** Under `LD_DEBUG=bindings`, `rgw-go
  --version` with the preload binds every object's `malloc`, `free`, `calloc`, `realloc`,
  `posix_memalign` and `aligned_alloc` to libtcmalloc, rgw-go's own cgo calls, librados,
  libceph-common and libc's internal calls included: 40 `malloc` bindings to libtcmalloc and 0 to
  libc (38 to libc without it). In every tcm run, glibc's own `malloc_info` shows one arena holding
  0 bytes, smaps has no glibc arena mappings, and tcmalloc's statistics are populated (section 5).
- **`tcm-radosgw` was dropped**: the radosgw process carries no `TCMALLOC_*` variable (its
  `/proc/<pid>/environ`, read in its container, and its pod spec), so it runs gperftools' defaults
  and the variant would be `tcm` again. `/etc/sysconfig/ceph`'s
  `TCMALLOC_MAX_TOTAL_THREAD_CACHE_BYTES=134217728` is read only by systemd units. Nor does radosgw's
  own code set a tcmalloc property at v20.2.4: its only `MallocExtension` calls are the `heap`
  admin command's
  ([heap_profiler.cc:50-79](https://github.com/ceph/ceph/blob/v20.2.4/src/perfglue/heap_profiler.cc#L50-L79)),
  which radosgw does not register.
- **`tcm-release`'s setting.** gperftools 2.9.1 honours both candidates. `TCMALLOC_RELEASE_RATE`
  (default 1.0,
  [page_heap.cc:47-53](https://github.com/gperftools/gperftools/blob/gperftools-2.9.1/src/page_heap.cc#L47-L53))
  paces an incremental scavenger: after each release of a span it waits for 1000/rate pages per page
  released to be freed again before the next
  ([page_heap.cc:437-468](https://github.com/gperftools/gperftools/blob/gperftools-2.9.1/src/page_heap.cc#L437-L468)),
  so with 8 KiB pages one 4 MiB span released at rate 10 still waits for 400 MiB of frees.
  `TCMALLOC_AGGRESSIVE_DECOMMIT`
  ([static_vars.cc:94-106](https://github.com/gperftools/gperftools/blob/gperftools-2.9.1/src/static_vars.cc#L94-L106))
  decommits every span freed to the page heap at once
  ([page_heap.cc:341-388](https://github.com/gperftools/gperftools/blob/gperftools-2.9.1/src/page_heap.cc#L341-L388)),
  with `madvise(MADV_DONTNEED)` on Linux
  ([system-alloc.cc:65-78](https://github.com/gperftools/gperftools/blob/gperftools-2.9.1/src/system-alloc.cc#L65-L78),
  [:513-549](https://github.com/gperftools/gperftools/blob/gperftools-2.9.1/src/system-alloc.cc#L513-L549)).
  The second is the one that bounds what a 64 MiB-object load leaves behind, so `tcm-release` uses it.
- **Not run:** the optional throwaway build calling `MallocExtension_ReleaseFreeMemory()` (cut 2).

**Loads.** Each run measures one of four:

1. `sustained`: the spike's sustained load, three rounds back to back of a 256 MiB PUT, a multipart
   upload of it in 8 MiB parts 2 at a time, a whole GET of each and a 128 MiB ranged GET (aws-cli,
   1-2 in flight; 3.375 GiB moved, 118 requests).
2. `large-put` and `large-get`, 32 in flight, and 3. `small-put` and `small-get`, 64 in flight: the
   shapes of `hack/bench/gateway`'s `large-put`, `large-get`, `small-put` and `small-get` profiles
   (hack/bench/gateway/internal/gateway/profiles.go:124-162 and 192-239), driven by a Go driver
   written for this campaign, `s3load`, because the harness cannot run a variant: it always builds
   bin/rgw-go and restarts it through rgw-go-up.sh itself, with no scope and its own environment
   (hack/bench/gateway/internal/gateway/gateways.go:39-93), and its elbencho is not built on this
   host. The driver, kept as [_s3load/main.go](_s3load/main.go) (section 9), uses the module's
   aws-sdk-go-v2, signs each payload's SHA-256 over plain HTTP as elbencho does by default, and sends
   no flexible checksums and no retries. Its throughputs compare within this campaign and the
   2026-11-03 buffer campaign, which ran the same driver
   ([2026-11-03-buffers-tentacle](../2026-11-03-buffers-tentacle/REPORT.md)), not with the
   2026-10-15 bench's elbencho runs.
   - `large-put`: 32 threads each PUT one 64 MiB object whole; removed and collected; then 32 threads
     each upload one 64 MiB object in 8 MiB parts, in sequence.
   - `large-get`: 32 x 64 MiB written (unmeasured), then 32 threads read them for 30 s in 4 MiB
     ranges, then 30 s whole.
   - `small-put`: 64 threads PUT 4 KiB objects for 30 s (up to 3000 a thread).
   - `small-get`: 64 x 400 objects of 4 KiB written (unmeasured), then 64 threads read them for 30 s.
   - The harness's phases run 60 s (`BENCH_SECONDS`); 30 s here, to fit the time budget.

**Each run**, under the host's build lock (the one gobuild.sh takes), one run at a time: the
1-minute load average; the gateway started fresh (radosgw by deleting its pod; the ReplicaSet
recreates it); 15 s idle; then the spike's sampler every 0.2 s (`/proc/<pid>/status`, the gateway's
cgroup `memory.current` and `memory.stat`) through the load; the gateway cgroup's `cpu.stat` read
before and after each measured phase; 2 s of quiet after each phase; 15 s after the last, a
snapshot: status, smaps, Go MemStats from `/debug/pprof/heap?debug=1`, and the C heap from the
allocator's own view, read through gdb: glibc's `malloc_info`, and under tcmalloc
`MallocExtension_GetStats` and `MallocExtension_GetNumericProperty`. Bucket creation, read sets,
removals and garbage collection are outside the measured phases.

**Cluster state.** The loads signed as the bench harness's fixed-key user `bench`
(hack/bench/gateway/internal/gateway/run.go:27-29), created for the campaign, in buckets each run
removed after it; since the parity options turn both gateways' garbage collector off,
`radosgw-admin gc process --include-all` ran between runs, and at the end the user was removed and
its usage log trimmed (`radosgw-admin usage trim --uid=bench`).

**Repetition.** Three runs per gateway per load, the gateways' order rotated from rep to rep and
load to load by the plan, so no gateway runs its reps together. Tables give the median and, in
brackets, the lowest and highest of the three. A difference inside the spread is reported as no
measurable difference.

**Derived figures.** CPU is `usage_usec` (user + system) summed over the measured phases. CPU per
GiB and per request divide it by the bytes and S3 requests the phases moved. Peaks are the highest
0.2 s sample from a measured phase's start to 1 s past its end. "After" is the snapshot 15 s after
the last phase. Working set is `memory.current - inactive_file`. The Go heap peak is the largest
heap size in rgw-go's gctrace.

**What ran.** 60 runs, 2026-11-02 21:57 to 2026-11-03 00:10 -07:00, all completed: 4 loads x 5
gateways x 3, in the plan's rotated order, plus 5 smoke runs, which are not in the tables. The
1-minute load average before a run was 0.8-6.0, median 2.7. The runs waited 47 minutes in all
for the build lock (at most 8 minutes for one) behind other sessions' `go test -race` runs. The
plan allowed three cuts, in this order, should the campaign not fit in about 3 hours of cluster
time: `tcm-radosgw` if it duplicated `tcm`, the optional throwaway build, and small-object GET.
Small-object GET was cut after the first round of runs, when those waits made the plan overrun the
time budget (the plan's third cut). Cluster time, setup to put-back: 21:23 to 00:17, 2 h 54 min.

Each step above ran from a throwaway script, shell or Python: the plan, the run and gateway start
scripts, the sampler, the gdb snapshot, and the analysis that wrote the tables; the race and link
checks below ran the same way. The scripts were not kept.

## 3. CPU

Median over 3 runs, lowest and highest in brackets. CPU is the gateway cgroup's `usage_usec` over the
measured phases. Per-phase figures are in [phases.md](phases.md), the rest in [tables.md](tables.md),
both from the per-run records in [runs.json](runs.json).

**Sustained large, 1-2 in flight** (the spike's sustained load: 3.375 GiB, 118 requests, ~15 s), from
[tables.md](tables.md), "sustained: CPU and throughput":

| gateway | CPU s | of which system | CPU s/GiB | CPU ms/request | MiB/s |
|---|---|---|---|---|---|
| radosgw | 5.07 (4.99-5.08) | 1.67 (1.60-1.71) | 1.50 (1.48-1.51) | 43.0 (42.3-43.0) | 228 (222-232) |
| glibc | 4.39 (4.31-4.42) | 1.24 (1.23-1.27) | 1.30 (1.28-1.31) | 37.2 (36.5-37.5) | 211 (201-225) |
| glibc-arena2 | 4.33 (4.32-4.33) | 1.20 (1.19-1.20) | 1.28 (1.28-1.28) | 36.7 (36.6-36.7) | 237 (234-239) |
| tcm | 4.29 (4.24-4.32) | 1.16 (1.14-1.24) | 1.27 (1.26-1.28) | 36.4 (35.9-36.6) | 249 (224-255) |
| tcm-release | 5.63 (5.58-5.67) | 2.48 (2.42-2.50) | 1.67 (1.65-1.68) | 47.7 (47.3-48.0) | 221 (195-238) |

glibc, glibc-arena2 and tcm: no measurable difference. tcm-release: 28% more CPU than tcm, all of it
system time, as the spike's fixed glibc mmap threshold (+29%). rgw-go used 13-15% less than radosgw.
Throughput: no measurable difference.

**Large, 32 in flight** (`large-put`: 32 x 64 MiB PUT whole, then 32 x 64 MiB in 8 MiB parts; 4 GiB,
384 requests. `large-get`: 30 s of 4 MiB ranged GETs, then 30 s of whole GETs, 32 threads). The
whole-profile columns, large-put CPU s and large-get CPU s/GiB, are from [tables.md](tables.md); the
per-phase columns from [phases.md](phases.md) (`put64`, `mpu8`, `get4m` and `get64m`):

| gateway | large-put CPU s | put whole, CPU s/GiB | put in parts, CPU s/GiB | large-get CPU s/GiB | 4 MiB ranges, CPU s/GiB | whole GETs, CPU s/GiB |
|---|---|---|---|---|---|---|
| radosgw | 16.69 (16.66-16.93) | 4.09 (4.09-4.18) | 4.25 (4.24-4.28) | 0.49 (0.49-0.51) | 0.55 (0.54-0.56) | 0.44 (0.43-0.48) |
| glibc | 14.95 (14.52-15.20) | 4.05 (3.96-4.11) | 3.42 (3.30-3.49) | 0.46 (0.46-0.49) | 0.51 (0.50-0.52) | 0.43 (0.43-0.47) |
| glibc-arena2 | 14.33 (14.06-14.53) | 3.89 (3.88-3.94) | 3.29 (3.14-3.32) | 0.46 (0.45-0.48) | 0.52 (0.49-0.52) | 0.43 (0.42-0.44) |
| tcm | 14.39 (14.20-14.42) | 3.88 (3.78-4.01) | 3.32 (3.19-3.33) | 0.48 (0.48-0.49) | 0.52 (0.49-0.53) | 0.47 (0.44-0.48) |
| tcm-release | 15.42 (15.36-15.95) | 3.90 (3.84-3.99) | 3.87 (3.78-3.98) | 0.81 (0.81-0.82) | 0.85 (0.85-0.88) | 0.78 (0.78-0.79) |

Throughput and latency per phase, from [tables.md](tables.md), "large-put: CPU and throughput" and
"large-get: CPU and throughput":

| gateway | put whole MiB/s | latency p50 / p99 ms | put in parts MiB/s | part p50 / p99 ms | 4 MiB ranges MiB/s | p50 / p99 ms | whole GETs MiB/s | p50 / p99 ms |
|---|---|---|---|---|---|---|---|---|
| radosgw | 589 (577-600) | 3419 / 3477 | 521 (504-542) | 451 / 600 | 557 (540-563) | 223 / 333 | 646 (616-674) | 3171 / 3549 |
| glibc | 546 (460-594) | 3669 / 3751 | 512 (492-544) | 443 / 641 | 535 (409-563) | 227 / 419 | 629 (627-703) | 3242 / 3647 |
| glibc-arena2 | 571 (544-608) | 3530 / 3587 | 552 (536-554) | 447 / 643 | 412 (383-532) | 302 / 423 | 651 (626-705) | 3174 / 3499 |
| tcm | 589 (574-597) | 3434 / 3472 | 548 (543-553) | 448 / 608 | 535 (421-564) | 231 / 377 | 643 (583-672) | 3223 / 3574 |
| tcm-release | 595 (566-602) | 3390 / 3438 | 518 (513-548) | 451 / 590 | 534 (411-558) | 231 / 378 | 617 (606-760) | 3330 / 3637 |

- **PUTs**: tcm and glibc-arena2 used about 4% less CPU than glibc over the whole profile (14.39 and
  14.33 against 14.95 s; the ranges do not overlap). Per phase the ranges just touch, so the gain is at
  the edge of what three runs can show. tcm-release cost 7% more than tcm, in the multipart phase
  (+17%).
- **GETs**: glibc, glibc-arena2 and tcm: no measurable difference in CPU per GiB. tcm-release cost 70%
  more, nearly all system time (24.0 of 29.6 s).
- **Throughput and latency**: no measurable difference. glibc-arena2's 4 MiB-range median is lower
  (412 MiB/s), but every variant had one run near 410, so its range overlaps the others.
- rgw-go used 14% less CPU than radosgw on PUTs and the same per GiB on GETs.

**Small, 64 in flight** (`small-put`: 30 s of 4 KiB PUTs, about 51,000 requests, ~1,700 a second),
from [tables.md](tables.md), "small-put: CPU and throughput", and [phases.md](phases.md), `put4k`:

| gateway | CPU ms/request | CPU s | of which system | requests | latency p50 / p99 ms |
|---|---|---|---|---|---|
| radosgw | 1.125 (1.119-1.180) | 59.48 (57.25-60.96) | 16.21 (15.80-19.23) | 51670 (50912-53136) | 35.9 / 62.7 |
| glibc | 0.751 (0.749-0.751) | 39.75 (38.94-40.10) | 7.29 (7.06-7.36) | 52955 (51848-53524) | 35.2 / 61.7 |
| glibc-arena2 | 0.805 (0.804-0.856) | 44.18 (41.86-45.11) | 8.95 (8.40-9.21) | 54941 (48934-56029) | 34.1 / 56.8 |
| tcm | 0.760 (0.732-0.776) | 38.25 (35.74-38.47) | 6.84 (6.44-6.97) | 50610 (46062-52248) | 36.9 / 63.4 |
| tcm-release | 0.745 (0.741-0.756) | 38.70 (36.23-39.18) | 7.09 (6.61-7.13) | 51963 (47898-52855) | 35.7 / 63.1 |

glibc, tcm and tcm-release: no measurable difference per request. glibc-arena2 cost 7% more per
request (0.804-0.856 against 0.749-0.751): 64 request goroutines' C calls share two arenas' locks
(the cause is inferred). Request rate and latency: no measurable difference. radosgw used 48% more CPU
per request.

## 4. Memory

MiB. Peak: the highest 0.2 s sample during a measured phase. After: 15 s after the last phase. The Go
heap columns are MemStats after (HeapSys - HeapReleased, an upper bound of what is resident) and the
largest heap in gctrace. Every table here is from [tables.md](tables.md), the load's "memory, MiB".

**Sustained large, 1-2 in flight:**

| gateway | VmRSS peak | RssAnon peak | working set peak | VmRSS after | Go heap after | threads |
|---|---|---|---|---|---|---|
| radosgw | 227 (181-317) | 188 (142-278) | 218 (168-304) | 227 (181-317) | - | 605 |
| glibc | 455 (443-458) | 421 (409-424) | 430 (414-430) | 455 (443-458) | 230 (221-257) | 32-33 |
| glibc-arena2 | 357 (294-362) | 323 (260-328) | 329 (265-336) | 357 (294-362) | 294 (225-296) | 33 |
| tcm | 295 (284-296) | 260 (249-262) | 268 (256-268) | 295 (284-296) | 231 (217-233) | 33 |
| tcm-release | 307 (256-332) | 272 (221-297) | 279 (229-306) | 305 (253-330) | 253 (206-283) | 33 |

tcm: 160 MiB (35%) below glibc, the C heap's difference (section 5). Against glibc-arena2,
tcm-release and radosgw: no measurable difference (the ranges overlap).

**Large, 32 in flight:**

| gateway | large-put VmRSS peak | after | Go heap after | Go heap peak | large-get VmRSS peak | after | Go heap after | Go heap peak |
|---|---|---|---|---|---|---|---|---|
| radosgw | 492 (492-493) | 491 (491-492) | - | - | 659 (654-664) | 641 (636-646) | - | - |
| glibc | 2456 (2214-2656) | 2433 (2171-2596) | 1500 (1361-1968) | 1356 (1157-1498) | 2664 (2461-3035) | 2450 (2354-2664) | 1593 (1504-1609) | 1512 (1507-2070) |
| glibc-arena2 | 1944 (1817-2073) | 1946 (1819-2073) | 1515 (1468-1919) | 1465 (1378-1511) | 1900 (1878-2699) | 1900 (1867-2549) | 1556 (1525-2177) | 1543 (1405-2177) |
| tcm | 2289 (2275-2294) | 2289 (2275-2294) | 1899 (1898-1924) | 1327 (1225-1895) | 2263 (2249-2513) | 2162 (2091-2175) | 1584 (1512-1603) | 1572 (1523-1761) |
| tcm-release | 1638 (1541-1914) | 1517 (1420-1787) | 1622 (1464-1963) | 1509 (1462-1616) | 1999 (1714-2403) | 1757 (1587-2218) | 1684 (1517-2143) | 1781 (1520-2140) |

- The Go heap is 1.2-2.2 GB of the 1.5-3.0 GiB, and how much of it is resident at a given moment
  depends on where its GC cycle stands, so it moves VmRSS by more than the allocators differ. glibc
  and tcm overlap at both peaks, and glibc-arena2's lower large-put peak comes with a lower Go heap
  in the same runs, not with a smaller C heap (its C peak is larger than tcm's, section 5).
  Allocator differences at this load are read from section 5, not from VmRSS.
- After the large GETs: tcm 2162 (2091-2175) against glibc 2450 (2354-2664), 290 MiB lower.
- Every rgw-go variant is 3.4-4.7x radosgw here (2289/492 for PUTs, 2263/659 for GETs with tcm),
  as in the 2026-10-15 bench and the spike.
- RssAnon is VmRSS less 34-40 MiB of file pages in every run, and the working set within -29 to
  +109 MiB of VmRSS; both are in [tables.md](tables.md).

**Small, 64 in flight:**

| gateway | VmRSS peak | RssAnon peak | working set peak | VmRSS after | threads peak |
|---|---|---|---|---|---|
| radosgw | 210 (206-211) | 171 (167-172) | 199 (195-199) | 208 (205-209) | 605 |
| glibc | 125 (124-127) | 91 (89-92) | 99 (98-100) | 123 (122-124) | 60 |
| glibc-arena2 | 78 (78-78) | 44 (43-44) | 51 (51-52) | 75 (75-78) | 64 |
| tcm | 94 (93-95) | 58 (58-60) | 66 (66-67) | 93 (93-94) | 57 |
| tcm-release | 93 (93-93) | 58 (57-58) | 65 (65-66) | 90 (90-91) | 58 |

The Go heap is 13-16 MiB here, so these are the C heaps' differences: glibc-arena2 lowest, tcm 31 MiB
below glibc, all below radosgw.

## 5. The C heap, from each allocator's view

After the load, through gdb: glibc's `malloc_info`; under tcmalloc
`MallocExtension_GetNumericProperty` and `MallocExtension_GetStats`. MiB, median (range), from
[tables.md](tables.md), the load's "the C heap after"; each run's figures are its `glibc` and `tcm`
records in [runs.json](runs.json). The `malloc_info` XML and the stats text were not kept.

| load | glibc: arenas; system; peak; free | glibc-arena2: system; peak; free | tcm: heap size (its peak); resident; page heap free | tcm-release: heap size; resident; unmapped |
|---|---|---|---|---|
| sustained | 27; 181 (168-205); 181; 177 | 34 (30-42); 34; 30 | 28.0 (28.0-32.0); 30.2; 16.0 | 32.0 (32.0-36.0); 14.2; 20.3 |
| large-put | 57; 869 (798-1016); 1428 (1412-1444); 865 | 411 (314-515); 595 (559-610); 406 | 520.9 (520.9-520.9); 523.7; 506.4 | 520.9; 17.2; 506.4 |
| large-get | 61; 895 (712-1011); 1278 (1266-1322); 891 | 318 (258-322); 626 (590-654); 314 | 520.9 (520.9-520.9); 521.6; 503.9 | 520.9; 17.7; 506.0 |
| small-put | 56; 73 (71-73); 73; 68 | 18 (18-18); 18; 13 | 27.9 (26.9-27.9); 30.6; 2.4 | 27.9; 28.4; 1.9 |

- **In use after every load, every allocator: 4.1-4.8 MiB.** Everything else is free memory the
  allocator keeps.
- **glibc** kept 93-99% of its heap free in 25-64 arenas, as the spike found. Its "peak" is the sum of
  each arena's own maximum, an upper bound of the heap at any one moment. `MALLOC_ARENA_MAX=2` cut
  what it kept to 19-47% of the defaults' and its peak to 42-49%.
- **tcmalloc** never gives memory back with its defaults (release rate 1, section 2): its heap size is
  its high-water mark. That mark was 520.9 MiB in all twelve 32-way runs, tcm and tcm-release alike,
  which matches 32 PUTs each holding a 16 MiB write window of librados copies (inferred; the copies are
  the spike's section 5), and 28-32 MiB at 1-2 in flight. So tcmalloc kept what was once in flight and
  no more, where glibc's peak was 2.5-2.8x that. radosgw's own VmRSS after large-put, 491 MiB, is of
  the same size (inferred; radosgw has no `heap` command to ask). The thread caches held 1.5-9 MiB,
  under the 32 MiB default cap.
- **tcm-release** returned all but 14-28 MiB after every load (`tcmalloc.pageheap_unmapped_bytes` is
  what aggressive decommit gave back), but reached the same peak.
- **The preload took everything.** In all 24 tcm and tcm-release runs, glibc's own `malloc_info` held
  one arena of 0 bytes, and smaps showed tcmalloc's sbrk heap in `[heap]` (12-534 MiB) and no glibc
  arena mapping. One run's smaps holds one 2 MiB anonymous mapping that the spike's smaps
  classification labels an arena, which glibc's 0 bytes shows is not one (from a check, not kept,
  of every run's smaps for arena mappings).

## 6. The race-detector check

`go test -race` of `./internal/radosclient/goceph`, the package that wraps go-ceph's librados, run
through `gobuild.sh` with `CCACHE_DISABLE=1`, once plainly and once with `-exec` naming a wrapper that
sets `LD_PRELOAD` for the test binary alone, so the Go toolchain never runs under tcmalloc. The
wrapper and the script that ran both were not kept; each log's outcome lines are in
[race.txt](race.txt).

| specs | glibc | tcmalloc preloaded |
|---|---|---|
| unit (no cluster) | pass, 127 of 127 | pass, 127 of 127 |
| integration, RADOS I/O against tentacle | pass, 118 of 118 | pass, 118 of 118 |

**It passes.** No `DATA RACE` report in any log. Each time, the wrapper also ran the same
`goceph.test` under `LD_DEBUG=bindings`: 40 `malloc` bindings to libtcmalloc and 0 to libc,
librados's included, so the race-instrumented binary did run on tcmalloc. The unit specs load
librados but do no RADOS I/O; the integration specs do. Their first run failed the same 5 of 118
under both allocators, for want of `make populate`'s `manifest.json` in the checkout they ran in;
with the same cluster's manifest copied in, both passed. They left 1448 objects in the `rgw-go-test`
pool, removed afterwards. A tcmalloc-linked build puts it into every test binary that links the
package carrying the flag (section 7); this check says the race detector is fine with that.

## 7. How to adopt the winner

**Link it; do not preload it.**

- **Linking works as measured.** A throwaway build of the same commit with
  `-L<dir> -l:libtcmalloc.so.4 -Wl,-rpath,<dir>` added to `CGO_LDFLAGS` (its script and binary were
  not kept) lists `libtcmalloc.so.4` first in its DT_NEEDED, ahead of libresolv, librados and libc.
  Under `LD_DEBUG=bindings`, with no preload, its own, librados's and libc's `malloc` bind to
  tcmalloc: the preload's result.
- **A preload in the image would reach everything.** Under Rook, rgw-go's image is every Ceph
  daemon's and the toolbox's (hack/image/Containerfile:1-3), so an image-level `LD_PRELOAD` would put
  tcmalloc into every process those containers run. Setting it in rgw-go's pod spec alone is a
  deployment setting every installation would have to carry.

**The build and image changes.**

- **Runtime: none.** The image is built FROM the pinned Ceph image (hack/image/Containerfile), which
  already installs gperftools-libs-2.9.1 because radosgw NEEDs `libtcmalloc.so.4` (section 2).
- **The link flag** goes with rgw-go's other cgo link settings, in one of two places (an owner
  decision):
  - a `#cgo LDFLAGS: -l:libtcmalloc.so.4` beside the go-ceph import in internal/radosclient/goceph,
    which links it into every binary and test binary that uses librados. CI's Ubuntu runners, which
    install `librados-dev`, would then need a libtcmalloc as well, Ubuntu's gperftools build rather
    than 2.9.1 (inferred; not tried);
  - or the release builds' `CGO_LDFLAGS` only (Makefile, goreleaser, `Containerfile.builder`): tests
    keep glibc, and the shipped binary runs a different allocator from its tests.
- **The builder** is FROM the same Ceph image (hack/image/Containerfile.builder:12), where
  `/usr/lib64/libtcmalloc.so.4` exists, so `-l:libtcmalloc.so.4` needs no -devel package there
  (inferred; the builder was not run).
- **The build host** links the copy in `/home/jhoblitt/.local/lib/rgw-go-tcmalloc-2.9.1/`,
  byte-identical to the image's, added to gobuild.sh's `CGO_LDFLAGS` with an rpath as the librados
  set is.
- **Settings:** gperftools' defaults, as radosgw runs. Leave `TCMALLOC_AGGRESSIVE_DECOMMIT` off: it
  costs 28-70% more CPU on large objects.

**`docs/exclusions.md`: no entry.** Linking tcmalloc gives rgw-go radosgw's own allocator
([src/rgw/CMakeLists.txt:495-500](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/CMakeLists.txt#L495-L500),
as the spike cites it), and no client sees an allocator.

**`GOMEMLIMIT`'s ratio: 0.7, not 0.8, for a 2 GiB pod.**

- Go's limit covers only what the Go runtime maps; it "does not account for space used by the Go
  binary and memory external to Go" (src/runtime/debug/garbage.go:189-193 at go1.27.3). The pod's
  limit has to hold, beside it, the C heap at its peak, the mapped binary and libraries (34-40 MiB of
  file pages here) and C thread stacks (a few MiB).
- The C heap's peak at 32 concurrent 64 MiB PUTs, measured: tcmalloc 521 MiB; glibc-arena2
  595-654 MiB; glibc's defaults up to 1278-1444 MiB (an upper bound).
- At 0.8, a 2 GiB pod leaves 410 MiB, less than any allocator's peak at that load. With tcmalloc,
  0.7 leaves 614 MiB: 521, ~40 of files and stacks, and ~50 of margin. With glibc's defaults the
  ratio would have to be about 0.3.
- **A ratio holds for one concurrency only.** The peak is the write windows in flight, about 16 MiB
  per concurrent large PUT (`rgw_put_obj_min_window_size`): 28-32 MiB at 1-2 in flight, 521 MiB at
  32 (inferred from those two points). At 64 concurrent large PUTs it would be about 1 GiB, and 0.7
  would not fit. A reserve sized from the window and the concurrency a deployment expects would hold
  where a ratio does not (inferred; not run).

## 8. Open questions

- **Small-object GET was not measured** (cut 3). Its one smoke run (64 threads of 4 KiB GETs for 5 s,
  glibc-arena2) reached 697 MiB of VmRSS with a ~680 MB Go heap, against 78-125 MiB for 4 KiB PUTs:
  a Go-heap effect of concurrent small GETs, not an allocator one, unexplained and worth its own look.
- **At 32 in flight the gap to radosgw is the Go heap's.** No allocator touches its 1.2-2.2 GB; the
  spike's pool and GOGC findings and the memlimit work are the levers.
- **Release tuning between the two extremes was not measured**: the cut timer build calling
  `MallocExtension_ReleaseFreeMemory()`, or a release rate above 1. Either might keep tcm's CPU and
  hand back the high-water mark when idle.
- **Not run in a pod.** The linked binary was built and its bindings checked, but no image was built,
  and rgw-go never ran under the pod's 2 GiB `memory.max`. The builder-side link and the fit of 0.7 are
  inferred.
- **glibc differs between host and image.** rgw-go ran on the host's glibc 2.42; in the derived image
  it would get the image's 2.34. The glibc variants' numbers there are unmeasured.
- **libunwind came from the host** (8.2.0, not the image's 1.8.0-4.el9). It matters only for stack
  capture, which is off by default.
- **The load generator.** The 32- and 64-way loads came from a Go driver with the harness's shapes,
  not from elbencho, in 30 s phases rather than 60. Throughputs compare within this campaign, not with
  the 2026-10-15 bench.
- **Three runs each.** At 32 in flight the spreads were wide (each variant had one slow run of 4 MiB
  ranged GETs), so differences under about 5% there are beyond this campaign. The host was shared:
  load 0.8-6.0 before runs, and 47 minutes of lock waits between them.
- **radosgw's allocator view** was not taken: it registers no `heap` command, and gdb was not attached
  inside its container.
- Squid was not measured.

## 9. Raw data

The files are the small summaries the tables are built from; the smaps dumps, heap and goroutine
profiles, `malloc_info` XML, tcmalloc stats text, gateway logs, per-sample series and binaries
stayed out, as did the scripts.

- **[runs.json](runs.json)**, one record per measured run, 60 in all (the smoke runs are not in it):
  `load`, `gw` (the variant), `rep`, and `dir`, the run's raw directory, not kept; `load1`, the
  1-minute load average before it; `cpu_s`, `user_s`, `sys_s` and `wall_s` over the measured phases;
  `phases`, per phase its CPU s and wall s and the driver's result (`MiBps`, `rps`, request latency
  `p50` and `p99` and object latency `obj_p50` and `obj_p99` in ms, `bytes`, `requests`, `errors`;
  the sustained load's bytes and requests are its fixed 3.375 GiB and 118); `bytes`, `requests`,
  `cpu_per_gib` and `cpu_ms_per_req` over the run; `rss`, `anon`, `ws` and `thr` peaks and afters
  (VmRSS, RssAnon, working set in MiB, threads); `go_heap_resident_after` (HeapSys - HeapReleased),
  `go_heapinuse_after` and `go_sys_after`, MiB; `gc`, from gctrace: cycles, the largest heap and the
  largest live heap, MB; `glibc`, from `malloc_info`: arenas, arenas used, and system, max, free and
  mmapped MiB; `tcm`, tcmalloc's numeric properties, bytes.
- **[tables.md](tables.md)**, per load and gateway, the median and range of runs.json's figures:
  CPU and throughput, memory, and the C heap after.
- **[phases.md](phases.md)**, per load, measured phase and gateway, CPU s, CPU s per GiB, CPU ms per
  request, GiB moved and requests, from runs.json.
- **[race.txt](race.txt)**, the race check's outcome lines from each `go test` log, its count of
  `DATA RACE` reports, and the malloc bindings of the test binary under the preload.
- **[_s3load/main.go](_s3load/main.go)**, the load driver's source, as it ran but for gofumpt's
  layout, which this repository's format check requires (line breaks and trailing commas in five
  composite literals). Its package comment's "never committed" describes it as it ran. The `_`
  keeps it out of the module's `./...` builds, lints and tests; it builds when named,
  `go build ./docs/benchmarks/2026-11-03-allocator-tentacle/_s3load`.
