# Memory comparison: tentacle

Whether rgw-go uses more memory than radosgw, and why. Measured on 2026-11-02 on the rooket
cluster `rgw-go-tentacle`, Ceph v20.2.4, with rgw-go 5700129; rgw-go's code is cited at that
commit. Squid was not measured. The files each table is built from are in this directory
(section 9).

## 1. Answer

Yes for large objects under sustained or concurrent load; no at idle, for small objects or for
isolated large requests. The only recorded comparison, the 2026-10-15 gateway benchmark (most
likely where the observation was made), read both gateways' VmRSS the same way and put rgw-go at
3.3-5.1x radosgw's (1982-3068 MiB against 600-616) for 32 concurrent 64 MiB requests; that is
real. Measured comparably on `rgw-go-tentacle`, rgw-go used less than radosgw at idle (VmRSS 50
against 114 MiB, RssAnon 16 against 76) and under small objects (62 against 191), about the same
for isolated 256 MiB requests one or two at a time (0.88-1.31x at the peaks), but 2.5x once those
requests ran back to back (490 against 198 MiB, both fresh). Threads, stacks, libraries and RADOS
connections are not the cause: rgw-go has 29-46 threads to radosgw's 605-669, and one RADOS
client to its four. The top cause is the **Go heap** (282 of the 490 MiB). rgw-go's pools of
4 MiB buffers hold far more than the bytes in flight: every multipart part reads its first stripe
into a fresh buffer and then pools it, so the pool grows with the traffic, to ~140 MB live with
two requests in flight; and GOGC 100 lets the heap reach twice the live heap. In the benchmark's
32 concurrent GETs the live heap was ~0.96 GB and the heap ~2.4 GB of the 3.2 GB (its gctrace).
The second is **glibc's C heap** (176 MiB): librados copies every write into a buffer of its own,
and glibc keeps those freed 4 MiB buffers in per-thread arenas, 98% of it free. Measured fixes:
`MALLOC_ARENA_MAX=2` with `GOMEMLIMIT=96MiB` held the back-to-back run to 163 MiB at the
defaults' CPU, and a fixed glibc mmap threshold with the same limit to 140 MiB at 29% more CPU,
both below radosgw's 198. A deployment also needs the limit sized from the pod (Rook's radosgw
pod allows 2 GiB with no swap, which the benchmark's 3 GiB would exceed) and the first-stripe
buffer taken from the pool, not allocated; those two are estimated, not run.

## 2. How it was measured

Cluster: rooket `rgw-go-tentacle` (Ceph v20.2.4, one OSD, Rook's chart radosgw). Both gateways
served the same zone, `ceph-objectstore`, under the parity options both config entities already
held (`ceph config dump`). Squid was not measured; see section 8.

- **radosgw**: the chart's pod, image `quay.io/ceph/ceph:v20.2.4`, tcmalloc, host-networked in
  the kind node, a container under a 2 GiB memory limit and no CPU limit. Restarted by deleting
  its pod so that it started fresh, as rgw-go did; the ReplicaSet recreated it from the unchanged
  deployment.
- **rgw-go**: 5700129 built with `make rgw-go-up RELEASE=tentacle`, which also created its
  keyring and checked the parity options, run under the host's build wrapper, `gobuild.sh`. The
  wrapper exports `GOMAXPROCS=4` and `GOFLAGS=-p=4`, which that run's rgw-go inherited, so it was
  stopped and restarted with rgw-go-up.sh's exact command line, those two variables removed
  (GOMAXPROCS then defaults to the host's 32, as in the 2026-10-15 bench), `GODEBUG=gctrace=1` as
  the bench ran it, and in a user systemd scope of its own so that it has a cgroup. It runs on the
  host, links glibc malloc, and has no memory limit.
- **Sampling**, the same for both, from the host, every 0.2 s: `/proc/<pid>/status` (VmRSS,
  RssAnon, RssFile, VmHWM, Threads) and the process's own cgroup (`memory.current`,
  `memory.stat`). Working set is `memory.current - inactive_file`, what cAdvisor and so
  `kubectl top` report; these clusters run no metrics-server, so `kubectl top` itself answers
  "Metrics API not available". PSS comes from `smaps_rollup`, read for radosgw inside its
  container as uid 167 and for rgw-go directly, at idle and when settled.
- **What each number means.** VmRSS counts every resident page mapped, a shared library's in
  full in each process that maps it. PSS divides shared pages among the processes sharing them.
  `memory.current` is everything charged to the cgroup: anonymous memory, page cache the process
  touched, kernel memory (socket buffers, page tables, slab). Working set leaves out only inactive
  page cache. For these two processes, which do almost no file I/O, the four differ little
  (section 3); the large differences are between conditions, not between tools.
- **Load**, the same for both, one generator on the host, aws-cli 2.37.0 and s5cmd: 30 s idle;
  4000 PUTs of 4 KiB then two passes of 4000 GETs, 8 workers (s5cmd); a single PUT of a 256 MiB
  object (`s3api put-object`); a multipart upload of it in 8 MiB parts, 2 at a time (`s3 cp`); a
  full GET of the single-PUT object; a 128 MiB ranged GET from its middle; a full GET of the
  multipart object; two single PUTs of 256 MiB at once; two full GETs at once; 60 s settled. 15 s
  of quiet follows each phase. Request checksums are off (`when_required`) so both gateways get
  the same plain signed bodies.
- **Per-phase figures** (each run's `peaks.txt`): "before" is the last sample before a phase
  starts, "peak" the highest sample from its start to 1 s after its end, "after" the last sample
  before the next phase starts, 15 s later.
- **Breakdown**: rgw-go's `/debug/pprof/heap?debug=1` MemStats and allocation sites, `/metrics`
  every 0.5 s, gctrace, its `smaps` grouped by mapping, and glibc's own `malloc_info`, written by
  a gdb call into the process; radosgw's `smaps` the same way, and its `dump_mempools` over the
  admin socket.
- **Sustained large objects** (after the main runs): a freshly started gateway (radosgw by a
  second pod deletion), sampled the same way, under three rounds, back to back, of a single
  256 MiB PUT, a multipart upload of it (8 MiB parts, 2 at a time), a full GET of each and a
  128 MiB ranged GET, 15 requests in ~16 s; then a snapshot 15 s later.
- **CPU**: the gateway cgroup's `cpu.stat` across the same back-to-back load, two runs per
  setting, alternating; radosgw measured as it ran, rgw-go started fresh each time.
- **Cluster state**: the load signed as the bench harness's fixed-key user `bench`
  (hack/bench/gateway/internal/gateway/run.go:27-29), in buckets removed afterwards with the
  user, and since the parity options turn both gateways' garbage collector off,
  `radosgw-admin gc process --include-all` ran between runs to keep the 10 GiB OSD from filling.
- Each step above ran from a throwaway shell script; the scripts were not kept.

## 3. Measurements

### 3.1 This spike: tentacle, one generator, concurrency 1 or 2 for 256 MiB objects

Run `radosgw-1` against run `go-base` (rgw-go with Go's and glibc's defaults), MiB. Each figure is
the highest 0.2 s sample from a phase's start to 1 s past its end; "after" is 15 s later, just
before the next phase. Ratios are rgw-go/radosgw. From [radosgw-1/peaks.txt](radosgw-1/peaks.txt)
and [go-base/peaks.txt](go-base/peaks.txt).

| phase | VmRSS radosgw | VmRSS rgw-go | ratio | RssAnon radosgw | RssAnon rgw-go | ratio | working set radosgw | working set rgw-go | ratio | VmRSS after, radosgw | VmRSS after, rgw-go | threads radosgw | threads rgw-go |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| idle | 114 | 50 | 0.44 | 76 | 16 | 0.21 | 101 | 20 | 0.19 | 115 | 50 | 605 | 32 |
| small | 191 | 62 | 0.32 | 152 | 27 | 0.18 | 183 | 38 | 0.21 | 191 | 61 | 605 | 37 |
| put-single | 215 | 189 | 0.88 | 176 | 154 | 0.88 | 206 | 162 | 0.79 | 215 | 192 | 605 | 37 |
| put-mpu | 215 | 277 | 1.29 | 176 | 242 | 1.38 | 208 | 252 | 1.21 | 215 | 281 | 605 | 37 |
| get-full | 237 | 285 | 1.20 | 197 | 250 | 1.27 | 226 | 260 | 1.15 | 236 | 286 | 605 | 37 |
| get-range | 274 | 286 | 1.04 | 235 | 251 | 1.07 | 264 | 258 | 0.98 | 274 | 286 | 669 | 37 |
| get-mpu | 275 | 291 | 1.06 | 235 | 256 | 1.09 | 266 | 264 | 1.00 | 274 | 295 | 669 | 37 |
| put-single-x2 | 275 | 359 | 1.31 | 235 | 324 | 1.38 | 268 | 341 | 1.27 | 274 | 336 | 669 | 37 |
| get-full-x2 | 334 | 360 | 1.08 | 295 | 325 | 1.10 | 326 | 334 | 1.02 | 334 | 360 | 669 | 37 |
| end of run (60 s settle, then cleanup) | 335 | 238 | 0.71 | 296 | 203 | 0.69 | 324 | 214 | 0.66 |  |  | 669 | 46 |

Per-tool readings at idle and settled, kB. radosgw's from
[radosgw-1/samples-idle-settled.csv](radosgw-1/samples-idle-settled.csv),
[radosgw-1/idle.smaps_rollup](radosgw-1/idle.smaps_rollup) and
[radosgw-1/settled.smaps_rollup](radosgw-1/settled.smaps_rollup); rgw-go's from
[go-base/idle.status](go-base/idle.status), [go-base/settled.status](go-base/settled.status),
[go-base/idle.smaps_rollup](go-base/idle.smaps_rollup),
[go-base/settled.smaps_rollup](go-base/settled.smaps_rollup) and, for the cgroup,
[go-base/samples-idle-peak-settled.csv](go-base/samples-idle-peak-settled.csv) (bytes):

| reading | radosgw idle | rgw-go idle | radosgw settled | rgw-go settled |
|---|---|---|---|---|
| VmRSS (`/proc/<pid>/status`) | 115536 | 54972 | 343452 | 239916 |
| PSS (`smaps_rollup`) | 108902 | 46074 | 336381 | 231493 |
| RssFile (binary and libraries, counted whole) | 38544 | 33848 | 40536 | 35864 |
| RssAnon | 76992 | 21124 | 302916 | 204052 |
| cgroup `memory.current` | 108060 | 26712 | 344160 | 221176 |
| working set (`memory.current - inactive_file`) | 100864 | 22572 | 331736 | 219236 |
| OS threads | 605 | 29 | 669 | 46 |

Idle is 20 s after each first answered; settled is the end of each run, after the 60 s settle and
the cleanup deletes. rgw-go's settled figure comes after the GC that the cleanup's allocations
triggered (section 4); at the end of the 60 s settle itself its VmRSS was 375 MiB, grown from 360
by the spike's own /metrics poller filling the heap toward its GC goal.

What each tool gets wrong here:

- **VmRSS vs PSS** differ by only 2-8%: neither process shares much. radosgw's libraries are
  shared with the other Ceph daemons in the same image (libceph-common Pss 5354 of Rss 8456 kB);
  rgw-go's copied librados set in `~/.local/lib/rgw-go-20.2.4` is its alone.
- **memory.current and working set** leave out file pages the cgroup did not fault in first. The
  page cache of radosgw's binary and libraries stayed charged to the deleted pod's cgroup (cgroup
  `file` 7.4 MB against RssFile 38.5 MB), and rgw-go's to the earlier rgw-go run outside a scope
  (idle working set 20 MiB against VmRSS 50). So the working set understates both by 25-35 MiB at
  start, and rgw-go's more. `kubectl top` is not available on these clusters at all (no
  metrics-server).
- **RssAnon** is the fairest single comparison of the two: it is the memory each process itself
  allocated.

### 3.2 Sustained large objects: back-to-back 256 MiB requests at 1-2 in flight

The phases above leave 15 s of quiet after each request. Run back to back (a fresh gateway, then
three rounds of a single 256 MiB PUT, a multipart upload of it with two 8 MiB parts at a time, a
full GET of each and a 128 MiB ranged GET, 15 requests in ~16 s), rgw-go keeps more. MiB; Go and
glibc figures from rgw-go's snapshot 15 s after the load. Each run links its directory: the VmRSS
and RssAnon columns are its `peaks.txt`, the Go columns its `after.memstats.txt`, the glibc column
its `after.malloc_info.txt`.

| run | VmRSS peak | RssAnon peak | VmRSS 15 s after | Go heap resident after | Go HeapInuse after | glibc arenas, system, free after |
|---|---|---|---|---|---|---|
| [sus-radosgw](sus-radosgw/) | 198 | 158 | 198 | - | - | - |
| [sus-base](sus-base/) | 486 | 452 | 490 | 284 | 282 | 28, 176, 173 |
| [sus-arena2](sus-arena2/) | 312 | 277 | 312 | 238 | 168 | 2, 38, 34 |
| [sus-mmapthr](sus-mmapthr/) | 289 | 255 | 289 | 243 | 242 | 28, 8, 4 |
| [sus-arena2-memlimit](sus-arena2-memlimit/) | 163 | 128 | 148 | 70 | 35 | 2, 38, 34 |
| [sus-combo](sus-combo/) | 140 | 105 | 131 | 84 | 67 | 29, 8, 4 |
| [sus-gmp4](sus-gmp4/) | 376 | 342 | 381 | 279 | 157 | 18, 68, 64 |
| [sus-gmp4-mmapthr](sus-gmp4-mmapthr/) | 312 | 278 | 306 | 260 | 259 | 17, 6, 2 |

Settings, as in section 6: `sus-arena2` `MALLOC_ARENA_MAX=2`; `sus-mmapthr` the 1 MiB mmap
threshold; `sus-arena2-memlimit` and `sus-combo` each of those with `GOMEMLIMIT=96MiB`; `sus-gmp4`
`GOMAXPROCS=4`, with and without the threshold. rgw-go's Go figures are MemStats (HeapSys -
HeapReleased, and HeapInuse); radosgw's 198 MiB was 131 MiB of tcmalloc heap and 22 MiB of 604
thread stacks (smaps, [sus-radosgw/after.smaps-by-mapping.txt](sus-radosgw/after.smaps-by-mapping.txt)).
The CPU runs (section 6.1) repeated this load twice per setting and ended at the same levels:
rgw-go with its defaults at 492-508 MiB, radosgw (not restarted, so starting from its 334 MiB
high-water) at 331-334.

### 3.3 The observation: the 2026-10-15 gateway benchmark, tentacle, re-read

The only rgw-go/radosgw memory comparison recorded is the bench's "peak RSS MiB" column
([2026-10-15-gateway-tentacle](../2026-10-15-gateway-tentacle/REPORT.md)), from which this table
is taken. Both columns are VmRSS read the same way, from `/proc/<pid>/status`
(hack/bench/gateway/internal/gateway/sample.go:18-31, 73-77), so the comparison itself is like for
like; what the column does not show is why.

| profile (threads) | peak RSS radosgw | peak RSS rgw-go | rgw-go/radosgw |
|---|---|---|---|
| PUT 4 KiB (1-128) | 249-254 | 57-208 | 0.23-0.82 |
| PUT 4 KiB (256) | 264 | 355 | 1.35 |
| GET 4 KiB, anonymous GET / (1-256) | 246-263 | 346-392 | 1.35-1.56 |
| PUT 64 MiB (32) | 602 | 1982 | 3.29 |
| PUT 64 MiB in 8 MiB parts (32) | 602 | 2565 | 4.26 |
| GET 64 MiB in 4 MiB ranges (32) | 600 | 3068 | 5.11 |
| GET 64 MiB (32) | 616 | 3068 | 4.98 |
| ListObjectsV2 (1, 64) | 632-635 | 833-923 | 1.31-1.46 |

Three things the column hides:

1. **It is one process across profiles.** rgw-go is started once per run, so each profile starts
   from the last one's high-water: its large-get samples begin at 2568 MiB, carried over from
   large-put ([rgw-go/samples-large-get.csv](../2026-10-15-gateway-tentacle/rgw-go/samples-large-get.csv)).
   The small-get rows (346-392) start from small-put's: its samples begin at 324 MiB.
2. **The ratio is a large-object, high-concurrency effect.** At 1-128 small PUTs rgw-go is below
   radosgw. The 3-5x rows are 32 concurrent 64 MiB requests.
3. **It is mostly Go heap there.** The large-get profile's one GC line reads
   `2426->2426->956 MB, 2601 MB goal`
   ([rgw-go/gctrace-large-get.log](../2026-10-15-gateway-tentacle/rgw-go/gctrace-large-get.log)):
   ~0.96 GB live, so a heap goal of ~2.6 GB, of a 3068 MiB peak.

### 3.4 Verdict on the observation

Measured comparably, at this spike's modest load:

- **Idle and small objects: rgw-go is lower**, VmRSS 0.32-0.44x and RssAnon 0.18-0.21x of
  radosgw's, with 32-37 threads to radosgw's 605.
- **Isolated 256 MiB requests at 1-2 in flight: about the same**, 0.88-1.31x VmRSS at the peaks,
  highest after the multipart upload (1.29x) and two concurrent single PUTs (1.31x).
- **Back-to-back 256 MiB requests at 1-2 in flight: rgw-go is 2.5x higher**, 490 against 198 MiB
  from a fresh start (3.2).
- **After the load: lower once Go collects**, 0.71x after the isolated requests. radosgw's
  tcmalloc keeps its high-water; rgw-go's glibc heap keeps most of its own (section 4).

So the observation is right for large objects under sustained or concurrent load: 2.5x at two in
flight back to back, 3-5x at 32 in flight in the bench. It is wrong for idle, small objects and
isolated large requests.

## 4. Breakdown of rgw-go's memory

rgw-go `go-base` (Go and glibc defaults, GOMAXPROCS 32), three moments: idle 20 s after start;
the peak, during the two concurrent 256 MiB GETs right after two concurrent 256 MiB PUTs
(VmRSS 360 MiB); and the end of the run. MiB. Sources, all in [go-base](go-base/): `smaps`
grouped by mapping ([idle](go-base/idle.smaps-by-mapping.txt),
[end](go-base/settled.smaps-by-mapping.txt)), Go's MemStats
([idle](go-base/idle.memstats.txt), [end](go-base/settled.memstats.txt)) and `/metrics` at the
peak ([gometrics-peak.csv](go-base/gometrics-peak.csv)), the sampler at the peak
([samples-idle-peak-settled.csv](go-base/samples-idle-peak-settled.csv)), gctrace
([gcphases.txt](go-base/gcphases.txt), [gctrace-large.log](go-base/gctrace-large.log)), glibc's
own `malloc_info` ([settled.malloc_info.txt](go-base/settled.malloc_info.txt), taken through
gdb), `/proc/<pid>/status`.

| part | idle | peak | end of run | how measured |
|---|---|---|---|---|
| Go heap, resident | 11.5 | ~137 | 8.0 | smaps rows in the Go heap's range at idle and end; at the peak HeapSys - HeapReleased (149.8 - 12.5), an upper bound |
| of which live at the last GC | ~1 | 86 (at put-single-x2's GC) | 2-3 | gctrace |
| of which garbage and unused heap | ~10 | ~50 | ~5 | the difference: GOGC 100 lets the heap reach twice the live heap before the next GC |
| goroutine stacks | 1.1 | 2.2 | 2.3 | MemStats Stack |
| Go runtime metadata (GC, spans, other) | ~7 | ~11 | ~13 | MemStats Sys - HeapSys - Stack; part of it untouched |
| glibc C heap (librados, ceph-common) | 3.6 | ~175 | 181.2 | smaps: 21 arenas plus `[heap]` at idle, 40 arenas plus `[heap]` at the end; at the peak RssAnon less the Go estimate |
| of which free, held by glibc | - | - | 188.1 of 192.4 (98%) | malloc_info at the end: 41 arenas, 4.3 MiB in use |
| thread stacks and other anon | 3.4 | ~4 | 7.8 | smaps "anon other": 29 threads at idle, 37 at the peak, 46 at the end |
| rgw-go binary (file) | 13.9 | 14 | 15.8 | smaps |
| shared libraries (file) | ~19 | ~19 | ~19 | smaps: libceph-common 7.4, libcrypto 3.6, libc 1.7, libstdc++ 1.7, librados 1.0, ~40 more under 0.5 each |
| **VmRSS** | **53.8** | **359.9** | **234.3** | status |

These are the isolated-request run's figures. Under back-to-back requests (3.2) the Go heap is
the larger part: 282 MiB of Go heap and 176 MiB of glibc heap in a 490 MiB VmRSS (`sus-base`),
because the pools grow with multipart parts (section 5).

Per part:

- **Go heap, live and retained.** At idle the heap is nearly empty: HeapInuse 3.4 MB, HeapSys
  19.8 MB, 5.6 MB released. Under 256 MiB objects the live heap at a GC was 49 MB for one PUT, 66
  for the multipart upload, 86 for two concurrent PUTs (gctrace). That is two to three times the
  16 MiB window and 4 MiB head a request may hold, because the three `sync.Pool`s of 4 MiB buffers
  (`readBufs`, `tailBufs`, `headBufs`; internal/driver/read.go:438-458,
  internal/driver/stripe.go:217-227 and 253, internal/driver/stat.go:295-301) keep their buffers
  through one GC and a pool's victim generation through a second: the 46 MB a GC left at the end
  of the run went in the very next GC (`167->167->46 MB`, then `87->89->3 MB`). With GOGC 100 the
  heap then grows to twice that before it is collected. Nothing collects it while rgw-go is idle:
  the runtime forces a GC only every two minutes, and the scavenger returns only what a GC has
  freed. Here a GC came 92 s after the load, and the heap went from 167 MB to 3 MB live, 179 MB
  released.
- **Goroutine stacks**: 1-2 MiB. Not a factor.
- **OS threads**: 29 idle, 37 under load, 46 after, against radosgw's 605-669. Thread stacks are
  mostly untouched; all of "other anon" is 3-8 MiB. Not a factor for memory, except through
  glibc's arenas (next).
- **C heap**: the largest part after large-object traffic, and nearly all of it free. librados
  copies every write's data into a bufferlist of its own (`rados_write_op_write` /
  `_write_full`,
  [librados_c.cc:3768-3793](https://github.com/ceph/ceph/blob/v20.2.4/src/librados/librados_c.cc#L3768-L3793)),
  so 4 MiB C buffers are allocated and freed by whichever of rgw-go's ~40 threads runs the call.
  The C heap grew with the writes and not the reads: RssAnon less the Go estimate went to 83 MiB
  after the single PUT, 118 after the multipart upload, stayed at ~122 through the three GETs, and
  went to 195 with the two concurrent PUTs ([go-base/gopeaks.txt](go-base/gopeaks.txt), the
  `rest` columns). glibc gives a contended thread its own arena (41 here); a freed 4 MiB chunk
  coalesces into its arena's top chunk; and glibc's dynamic threshold, once a chunk above the
  mmap threshold is freed, raises the mmap threshold to that chunk's size and the trim threshold
  to twice it (mallopt(3), man-pages 6.13, "M_MMAP_THRESHOLD"), so later 4 MiB chunks come from
  the arenas and tops under the raised trim threshold are kept. The end-of-run arenas hold
  4.1-20.2 MiB each, multiples of ~4 MiB, almost all in the top chunk. `malloc_trim(0)`, called
  through gdb at the end, gave back 6.4 MiB of the 188, leaving the top chunks in place
  (measured; glibc's source was not read to say why).
- **Mapped libraries**: ~19 MiB file-backed plus a 14-16 MiB binary, against radosgw's ~25 MiB
  of libraries plus a 12.5-14 MiB binary. Not a cause.
- **RADOS connections**: rgw-go opens one RADOS client; radosgw opens four (the mons list 4
  sessions for `client.rgw.ceph.objectstore.a`, 1 for `client.rgw.rgw-go`): its config store's
  librados
  ([store.cc:36](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/config/store.cc#L36)),
  RGWRados's
  ([rgw_rados.cc:1162-1166](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L1162-L1166)),
  the realm watcher's
  ([rgw_realm_watcher.cc:84](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_realm_watcher.cc#L84)),
  and the driver's neorados
  ([rgw_sal.cc:73-85](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_sal.cc#L73-L85)).
  rgw-go's connection count is not a cause.
- **rgw-go's own pools and caches**: the three 4 MiB buffer pools above are the Go heap's
  retention. The metadata cache mirrors radosgw's ObjectCache (internal/driver/cache.go) and held
  nothing measurable here (the end-of-run heap profile's largest in-use record was 0.1 MiB,
  [go-base/settled.heap-inuse-top.txt](go-base/settled.heap-inuse-top.txt), and the live heap
  3 MB); the body budget and the in-flight limiter are semaphores, accounting only, and allocate
  nothing (internal/driver/bodybudget.go, internal/radosclient/goceph/limiter.go).

For comparison, radosgw at the end of its run: VmRSS 343452 kB, of which tcmalloc's `[heap]`
271792 kB and 668 thread stacks 25912 kB resident
([radosgw-1/settled.smaps-by-mapping.txt](radosgw-1/settled.smaps-by-mapping.txt)); its mempools
held 0.3 MB of buffers (`dump_mempools`), so it too keeps its freed buffer memory, in tcmalloc
rather than glibc.

## 5. Causes, ranked by size

Ranked by size under the loads where rgw-go is higher: large objects back to back (this spike,
1-2 in flight) and concurrently (the 2026-10-15 bench, 32 in flight, estimated from its gctrace).

1. **The Go heap: pooled 4 MiB buffers stay live, and GOGC 100 doubles them.**
   - Evidence, sustained load (measured, `sus-*` and the CPU runs): with the defaults the live heap
     after each GC climbed 49 -> 57 -> 69 -> 73 -> 113 -> 121 -> 137 MB during 15 back-to-back
     256 MiB requests with at most two in flight ([cpu-base/gctrace.log](cpu-base/gctrace.log)),
     and the heap reached 242 MB; in the `sus-base` snapshot the Go heap held 282 MiB of a
     490 MiB VmRSS. With the C heap held down by the fixed mmap threshold, the Go heap was 242 of
     289 MiB (`sus-mmapthr`).
   - What is live: the heap profile at the last GC (`sus-base`,
     [after.heap-inuse-top.txt](sus-base/after.heap-inuse-top.txt)) attributes 88 MiB to buffers
     allocated for a part's first stripe (internal/driver/stripe.go:489), sized min(stripe,
     window + 1), 4 MiB here (internal/driver/stripe.go:450-451), which release then pools in
     `tailBufs` because it equals the chunk (internal/driver/stripe.go:247-255), and 24 MiB to
     `headBufs` heads (internal/driver/stat.go:295-301, via the upload's meta read,
     internal/driver/mp_layout.go:124); `sus-mmapthr` adds 20 MiB of `readBufs`
     (internal/driver/read.go:438-458). Two requests hold at most ~40 MiB in flight (a 16 MiB
     window and a 4 MiB head each,
     [rgw.yaml.in:102-110](https://github.com/ceph/ceph/blob/v20.2.4/src/common/options/rgw.yaml.in#L102-L110),
     [:1999-2004](https://github.com/ceph/ceph/blob/v20.2.4/src/common/options/rgw.yaml.in#L1999-L2004)),
     so the rest is pool contents. Why the pool grows: a part's first stripe is read into a fresh
     buffer (internal/driver/stripe.go:489) that, once written, is released into `tailBufs`
     (internal/driver/stripe.go:577), while `tailBufs` is drawn from only for tail chunks, one per
     chunk (internal/driver/stripe.go:221); so each 8 MiB part leaves one more 4 MiB buffer in the
     pool than it took. The surplus lives until two GCs pass, and because it raises the live heap
     it also raises the GC goal and so pushes those GCs out. Measured against the alternative:
     with `GOMAXPROCS=4` (`sus-gmp4`) the live heap still reached 140 MB and the profile held
     100 MiB of first-stripe buffers, so per-P pool slots are not the cause.
   - Why it doubles: GOGC 100 lets the heap reach twice the live heap before the next GC, and with
     no allocation nothing collects it until the runtime's two-minute forced GC
     ([proc.go:6527](https://github.com/golang/go/blob/go1.27.3/src/runtime/proc.go#L6527) at
     go1.27.3); a pool's buffers need two GCs to go (the isolated-request run's last GCs left
     46 MB, then 3 MB).
   - At the bench's 32 in flight (estimated from its gctrace): `2426->2426->956 MB, 2601 MB goal`
     ([rgw-go/gctrace-large-get.log](../2026-10-15-gateway-tentacle/rgw-go/gctrace-large-get.log)):
     ~0.96 GB live, ~30 MB a request, a 2.43 GB heap, about three quarters of the 3068 MiB
     (3.22 GB) peak.
2. **glibc keeps librados's freed write buffers, one arena per busy thread.**
   - Evidence (measured): 176 MiB of glibc heap, 173 of it free, in 28 arenas after the sustained
     run (`sus-base`); 192.4 MiB, 188.1 free, in 41 arenas after the isolated requests, 76% of that
     run's 238 MiB end-of-run VmRSS. It grew with writes, not reads (section 4).
     `MALLOC_ARENA_MAX=2` cut it to 50.0 MiB in 2 arenas, and a fixed 1 MiB mmap threshold to
     8-18.5 MiB (section 6).
   - Why: librados copies every write's bytes into a buffer of its own
     ([librados_c.cc:3768-3793](https://github.com/ceph/ceph/blob/v20.2.4/src/librados/librados_c.cc#L3768-L3793)),
     where radosgw's writer hands it the bufferlist it already holds
     ([rgw_putobj_processor.cc:168-181](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_putobj_processor.cc#L168-L181),
     [librados_cxx.cc:467-481](https://github.com/ceph/ceph/blob/v20.2.4/src/librados/librados_cxx.cc#L467-L481)).
     librados brings no allocator
     ([src/librados/CMakeLists.txt:34-36](https://github.com/ceph/ceph/blob/v20.2.4/src/librados/CMakeLists.txt#L34-L36)),
     so in rgw-go the copies are glibc's, while radosgw's buffers are tcmalloc's
     ([src/rgw/CMakeLists.txt:495-500](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/CMakeLists.txt#L495-L500)).
     glibc's dynamic mmap threshold moves 4 MiB chunks into the arenas and raises the trim
     threshold with it (mallopt(3)), and the freed chunks pile up in each arena's top, which
     `malloc_trim(0)` left in place (6.4 of 188 MiB returned).
   - At the bench's 32 in flight (estimated): its 3.22 GB peak less the 2.43 GB Go heap and
     ~0.05 GB of binary, libraries and runtime leaves ~0.7 GB, which this spike's runs attribute to
     the C heap; the bench recorded no `malloc_info`.
3. **A measurement artefact in the bench: one rgw-go process across profiles.** Each profile
   starts at the last one's high-water (large-get began at 2568 MiB), so the small-object GET rows'
   1.35-1.56x carry small-put's 256-thread peak, not their own.

Not causes, measured:

- **OS threads**: rgw-go 29-46, radosgw 605-669.
- **Goroutine stacks**: 1-2 MiB.
- **Mapped libraries**: ~19 MiB plus the binary, about radosgw's.
- **RADOS connections**: rgw-go opens 1 RADOS client, radosgw 4.
- **rgw-go's metadata cache, body budget and in-flight limiter**: the cache was near empty, and
  the budget and limiter are semaphores that allocate nothing.

One trap for anyone measuring rgw-go: started through `gobuild.sh make rgw-go-up`, it inherits
`GOMAXPROCS=4` and `GOFLAGS=-p=4` from the wrapper (`/proc/<pid>/environ` of the first rgw-go this
spike started), so its threads and glibc arenas are not what the bench or a deployment sees.

## 6. Fixes

### 6.1 Measured: the same load, rgw-go with one setting changed

Each column is a whole run of the load in section 2 against a freshly started rgw-go, as
`go-base` was (section 3), with one setting changed: `go-arena2` `MALLOC_ARENA_MAX=2`;
`go-mmapthr` `GLIBC_TUNABLES=glibc.malloc.mmap_threshold=1048576`; `go-memlimit`
`GOMEMLIMIT=96MiB`; `go-combo` both of the last two; `go-mmap-gogc50` the threshold and `GOGC=50`;
`go-mmap-win4` the threshold and 4 MiB read and write windows
(`CEPH_ARGS=--rgw-get-obj-window-size=4194304 --rgw-put-obj-min-window-size=4194304`). VmRSS in
MiB: each phase's peak, and the end of the run. Each column links the run's `peaks.txt`.

| phase | [radosgw-1](radosgw-1/peaks.txt) | [go-base](go-base/peaks.txt) | [go-arena2](go-arena2/peaks.txt) | [go-mmapthr](go-mmapthr/peaks.txt) | [go-memlimit](go-memlimit/peaks.txt) | [go-combo](go-combo/peaks.txt) | [go-mmap-gogc50](go-mmap-gogc50/peaks.txt) | [go-mmap-win4](go-mmap-win4/peaks.txt) |
|---|---|---|---|---|---|---|---|---|
| idle | 114 | 50 | 50 | 53 | 52 | 50 | 49 | 52 |
| small | 191 | 62 | 56 | 62 | 64 | 63 | 61 | 61 |
| put-single | 215 | 189 | 127 | 121 | 193 | 108 | 110 | 76 |
| put-mpu | 215 | 277 | 205 | 171 | 278 | 140 | 115 | 143 |
| get-full | 237 | 285 | 214 | 198 | 282 | 137 | 93 | 154 |
| get-range | 274 | 286 | 216 | 201 | 281 | 138 | 97 | 154 |
| get-mpu | 275 | 291 | 216 | 201 | 265 | 142 | 98 | 165 |
| put-single-x2 | 275 | 359 | 250 | 238 | 374 | 169 | 186 | 180 |
| get-full-x2 | 334 | 360 | 244 | 230 | 372 | 118 | 159 | 172 |
| **highest peak** | **334** | **360** | **250** | **238** | **374** | **169** | **186** | **180** |
| end of run | 335 | 238 | 107 | 77 | 307 | 74 | 70 | 72 |

glibc's own view at the end of each run (`malloc_info`, through gdb; each run links its
`settled.malloc_info.txt`):

| run | arenas | glibc system MiB | free MiB | in use MiB |
|---|---|---|---|---|
| [go-base](go-base/settled.malloc_info.txt) | 41 | 192.4 | 188.1 | 4.3 |
| [go-arena2](go-arena2/settled.malloc_info.txt) | 2 | 50.0 | 45.5 | 4.5 |
| [go-mmapthr](go-mmapthr/settled.malloc_info.txt) | 44 | 18.5 | 14.1 | 4.4 |
| [go-memlimit](go-memlimit/settled.malloc_info.txt) | 47 | 253.8 | 249.4 | 4.4 |
| [go-combo](go-combo/settled.malloc_info.txt) | 46 | 18.3 | 13.9 | 4.4 |
| [go-mmap-gogc50](go-mmap-gogc50/settled.malloc_info.txt) | 43 | 16.1 | 11.7 | 4.4 |
| [go-mmap-win4](go-mmap-win4/settled.malloc_info.txt) | 46 | 17.9 | 13.5 | 4.4 |

CPU, and VmRSS after sustained large-object traffic (a freshly started rgw-go, or radosgw as it
ran, then three rounds back to back of a single 256 MiB PUT, a multipart upload of it with two
8 MiB parts at a time, a full GET of each and a 128 MiB ranged GET; the gateway cgroup's
`cpu.stat` across them; two runs each, alternating; radosgw was not restarted for these, so its
VmRSS starts from its 334 MiB high-water, against 198 MiB from a fresh start in 3.2). From
[cpu.txt](cpu.txt), one line per run in the order they ran:

| gateway | setting | CPU s (user + system) | of which system | wall s | VmRSS after, MiB |
|---|---|---|---|---|---|
| radosgw | - | 5.13 / 5.37 | 1.70 / 1.85 | 17.1 / 17.9 | 334 / 331 |
| rgw-go | defaults | 4.35 / 4.45 | 1.20 / 1.31 | 15.7 / 18.9 | 508 / 492 |
| rgw-go | MALLOC_ARENA_MAX=2 | 4.29 / 4.29 | 1.16 / 1.20 | 15.6 / 16.0 | 277 / 320 |
| rgw-go | GLIBC_TUNABLES=glibc.malloc.mmap_threshold=1048576 | 5.66 / 5.69 | 2.52 / 2.52 | 15.4 / 16.0 | 328 / 275 |
| rgw-go | GOMEMLIMIT=96MiB | 4.54 / 4.36 | 1.32 / 1.19 | 16.4 / 15.9 | 312 / 312 |
| rgw-go | GOMEMLIMIT=96MiB GLIBC_TUNABLES=glibc.malloc.mmap_threshold=1048576 | 5.80 / 5.64 | 2.61 / 2.47 | 16.6 / 14.2 | 132 / 129 |
| rgw-go | MALLOC_ARENA_MAX=2 GOMEMLIMIT=96MiB | 4.30 / 4.28 | 1.14 / 1.13 | 15.8 / 15.6 | 155 / 154 |

Sustained-load figures for the same settings are in section 3.2.

### 6.2 Each fix, its effect and its cost

Measured means a run in 6.1; estimated means reasoned from the runs and the 2026-10-15 bench,
and is marked so.

1. **Fix glibc's mmap threshold** (`GLIBC_TUNABLES=glibc.malloc.mmap_threshold=1048576` in the
   image or the pod, or `mallopt(M_MMAP_THRESHOLD, 1 << 20)` through cgo before librados starts).
   A fixed threshold turns off the dynamic one (mallopt(3)), so librados's 4 MiB buffers are
   mmapped and unmapped on free.
   - Effect, measured: highest peak 360 -> 238 MiB; end of run 238 -> 77 MiB; glibc at the end
     192.4 -> 18.5 MiB; sustained, 490 -> 289 MiB (`sus-mmapthr`), the rest being Go heap.
     Estimated at the bench's 32 in flight: removes most of the ~0.7 GB that is not Go heap.
   - Cost, measured: an mmap, page faults and an munmap per buffer of 1 MiB or more. For 15 large
     requests rgw-go spent 5.66 and 5.69 s of CPU against 4.35 and 4.45 s with the defaults (+29%),
     all of it system time (2.52 s against 1.20-1.31), about 0.4 CPU-seconds per GiB of the
     3.4 GiB moved; radosgw spent 5.13 and 5.37 s on the same requests. Latency per request:
     within noise at 1-2 in flight. No change clients can see; it would need a
     `docs/exclusions.md` line only if rgw-go set it itself.
2. **Cap glibc's arenas** (`MALLOC_ARENA_MAX=2`).
   - Effect, measured: highest peak 360 -> 250 MiB; end of run 238 -> 107 MiB; glibc at the end
     192.4 MiB in 41 arenas -> 50.0 MiB in 2; sustained, 490 -> 312 MiB (`sus-arena2`, glibc
     38 MiB).
   - Cost, measured: none at 1-2 in flight, 4.29 and 4.29 s of CPU against 4.35 and 4.45 s.
     ~40 threads share 2 arenas' locks, so contention at 32 in flight could cost; unmeasured. It
     keeps more than fix 1 (50 against 18.5 MiB of glibc heap at the end) at no CPU cost here.
3. **`malloc_trim(0)`** after large requests or on a timer: measured useless. It returned 6.4 of
   188 free MiB: the free memory sits in the arenas' top chunks, which it left in place here
   (measured; glibc's source was not read to say why).
4. **A Go memory limit** (`GOMEMLIMIT`, or better, set at start from the pod's limit: Rook passes
   `POD_MEMORY_LIMIT`, and the cgroup's `memory.max` is readable).
   - Effect, measured with `GOMEMLIMIT=96MiB` alone: the Go side stayed under 96 MiB (heap goal
     <= 78 MB, against up to 172 MB), but VmRSS was no lower (highest peak 374, end of run
     307 MiB), because the C heap, untouched by a Go limit, came out larger this run (253.8 MiB in
     47 arenas). Under the back-to-back requests of the CPU runs, where the default heap grows to
     ~240 MB, it cut VmRSS after the load from 492-508 to 312 MiB. It bounds only the Go part.
   - Effect, estimated at the bench's 32 in flight: with ~0.96 GB live, a limit of ~1.5 GiB would
     hold the Go heap near 1.5 GiB instead of its 2.6 GB goal, and under Rook's 2 GiB pod limit
     (where nothing sets one today) it is what stands between that load and an OOM kill.
   - Cost: more GC cycles as the live heap nears the limit. The heap here is mostly pointer-free
     `[]byte` buffers, which the GC does not scan, so a cycle is cheap; measured CPU: 4.54 and
     4.36 s against 4.35 and 4.45 s with the defaults, within noise. A limit below the live heap
     makes the GC run continuously (Go caps that at about half the CPU), so it must leave room
     for the C heap and the live heap at peak load. It is new behaviour (sizing from the pod), so
     it needs a `docs/exclusions.md` entry.
5. **A lower GOGC** (`GOGC=50`, measured together with the 1 MiB mmap threshold): highest peak
   238 -> 186 MiB against the threshold alone, end of run 77 -> 70 MiB; the heap goal stays 1.5x
   the live heap at every size. Cost: 2.5-3x the GC cycles (391 against 134 at idle, 4263 against
   1677 in the small-object phase, [go-mmap-gogc50/gcphases.txt](go-mmap-gogc50/gcphases.txt),
   [go-mmapthr/gcphases.txt](go-mmapthr/gcphases.txt)), paid at every load, where a memory limit
   costs nothing until the heap nears it. The memory limit is the better lever.
6. **A glibc setting and a Go memory limit together**, the two causes at once.
   - Fixed mmap threshold + `GOMEMLIMIT=96MiB`, measured: highest isolated-request peak 360 -> 169
     MiB, below radosgw's 334 in every phase (put-single 108 against 215, two concurrent GETs 118
     against 334), end of run 74 MiB against radosgw's 335; sustained: peak 140, after 131 MiB
     against radosgw's 198 fresh (`sus-combo`); CPU 5.80 and 5.64 s against 4.35 and 4.45 s, the
     threshold's system time.
   - `MALLOC_ARENA_MAX=2` + `GOMEMLIMIT=96MiB`, measured under sustained load only: peak 163,
     after 148 MiB (`sus-arena2-memlimit`); CPU 4.30 and 4.28 s, the defaults' own. The cheaper of
     the two pairs at 1-2 in flight; its cost at 32 in flight (arena lock contention) is
     unmeasured.
   - The 96 MiB suits only this load. A deployment needs the limit sized from the pod, as in fix
     4, with room left for the C heap, which `MALLOC_ARENA_MAX=2` left at 38-50 MiB here.
7. **Stop the pools growing, then bound them** (code, estimated).
   - First, take a part's first-stripe buffer from `tailBufs` instead of allocating it
     (internal/driver/stripe.go:489), or do not pool it on release (stripe.go:577), so that parts
     stop adding a 4 MiB buffer to the pool each. Estimated effect: the sustained live heap from
     ~140 MB toward the ~40 MiB two requests hold, and the heap with it; the multipart-heavy
     profile is where the heap profile put 88-100 MiB of the live heap.
   - Then replace the three `sync.Pool`s (`readBufs`, `tailBufs`, `headBufs`) with one free list
     capped near the bytes the gateway may have in flight, so reuse never retains more than that.
   - Further, allocating the 4 MiB I/O buffers outside the Go heap (an mmap-backed arena; they
     are already pinned for cgo) would keep GOGC from doubling them at all. Estimated effect at
     the bench's 32 in flight: the Go heap from ~2.4 GB toward the ~0.6 GB in flight
     (32 x (16 MiB window + 4 MiB head)).
   - Cost: code, and explicit buffer lifetimes, including the abandoned-read case the pools
     already handle (internal/driver/read.go:450-458).
8. **Smaller windows** (`rgw_get_obj_window_size`, `rgw_put_obj_min_window_size`, 16 MiB each),
   measured at 4 MiB together with the 1 MiB mmap threshold: highest peak 238 -> 180 MiB, a single
   PUT's 121 -> 76, its live heap 53 -> 37 MB. Cost: a full 256 MiB GET took 1.38 s against 0.90
   and the multipart object's 1.63 against 0.80 (single runs), since fewer reads are in flight.
   They are parity options that bound radosgw too, so this trades throughput for memory on both;
   it is not a fix for the gap.
9. **librados options** (`objecter_inflight_op_bytes`, 100 MiB): bounds the bytes in RADOS
   flight, not the Go buffers rgw-go allocates before its limiter admits a read
   (internal/driver/read.go:356-369, internal/radosclient/goceph/pool.go:285-297, 329), so
   lowering it would not shrink the Go heap. Not measured.
10. **Fewer connections or threads**: not causes. rgw-go already has one RADOS client and 29-46
    threads.
11. **An LD_PRELOADed tcmalloc** (the Ceph image ships `libtcmalloc.so.4`): not measured.
    Estimated to make rgw-go's C heap behave like radosgw's, which also keeps its high-water
    (radosgw's `[heap]` held 265 MiB at the end with 0.3 MiB of buffers in use), so no better
    than fix 1.

## 7. Registry entries

The cgo limitations this measurement found are recorded in
[docs/cgo-limitations.md](../../cgo-limitations.md): the new entry "librados's buffers live in
glibc's heap, which keeps them after use", and the memory side of "Payload copies across the
boundary".

## 8. Open questions

- **Where the observation was made.** Assumed to be the 2026-10-15 gateway benchmark, the only
  recorded comparison; not confirmed. If it was elsewhere (htop on the host, a Rook pod running
  the derived image), the verdict in 3.4 still holds for the load, though the tool may differ.
- **Squid was not measured.** Only `rgw-go-tentacle` ran. The causes lie in rgw-go's buffer
  pools, librados's C API, glibc and the Go runtime; whether Squid behaves the same is
  unverified.
- **High concurrency was not measured.** Large objects were kept at 1-2 in flight. The
  32-in-flight figures come from the 2026-10-15 bench, and each fix's effect there is estimated,
  as is `MALLOC_ARENA_MAX=2`'s cost there (arena lock contention among ~90 threads) and a
  `GOMEMLIMIT`'s GC cost near a ~1 GB live heap.
- **rgw-go inside the radosgw pod was not measured.** Under the derived image, rgw-go would run
  under the pod's 2 GiB `memory.max`, with `memory.swap.max` 0 (both read from the radosgw
  container's cgroup), and no `GOMEMLIMIT`. At the bench's 32 x 64 MiB load its 3 GiB VmRSS,
  nearly all anonymous, could be neither reclaimed nor swapped, so it would be OOM-killed. That
  is inferred, not run.
- **The first-stripe fix (section 6, fix 7) was not tried.** That taking the buffer from the
  pool, or not pooling it, ends the live heap's growth is inferred from the code and the heap
  profiles, not from a run.
- **radosgw's tcmalloc retention** (265 MiB `[heap]` after the isolated requests, with 0.3 MiB of
  buffers in its mempools) was not tuned (`TCMALLOC_RELEASE_RATE`), and radosgw registers no
  `heap` admin command to inspect it, so how much of radosgw's own figure is reclaimable is
  unknown.
- **Durations are single runs** on a shared host (another Ceph build held 4 CPUs when the spike
  began; the 1-minute load was 1.5 when it ended), so the latency comparisons in 6.2 rule out
  only large effects.

## 9. Raw data

One directory per run, named as above. The files are the small summaries the tables are built
from; the smaps dumps, heap profiles, whole logs and full sample series stayed out.

- **Every run's `peaks.txt`**, per phase, MiB: VmRSS (`rss`), RssAnon (`anon`), the cgroup's
  `memory.current` (`cg`) and the working set (`ws`), each before (`<`), at its peak (`^`) and
  after (`>`), and threads at the peak and after. The sustained runs' phases are `load` and
  `after`.
- **`radosgw-1/` and `go-*/`**, the runs of the load in section 2. The `go-*` runs add
  `settled.malloc_info.txt`, glibc's `malloc_info` at the end of the run, per arena over 1 MiB and
  in total; `gcphases.txt`, per phase from gctrace: GC cycles, the largest heap at a GC's start,
  the largest live heap a GC left and the largest goal, MB; `gopeaks.txt`, per phase, MiB: Go's
  heap in use (`inuse`), Go's resident estimate, Sys - HeapReleased (`gores`), and RssAnon less
  that estimate (`rest`), at the peak and settled; and `gctrace-large.log`, the gctrace lines
  from the first 256 MiB request to the process's exit.
- **`go-base/` and `radosgw-1/`** add the idle and settled snapshots: `smaps_rollup`, rgw-go's
  `status` and MemStats (`*.memstats.txt`, without the pause arrays), each `smaps` grouped by
  mapping (`*.smaps-by-mapping.txt`: Rss kB, Pss kB, mappings, kind), and the sampler's rows they
  were read from (`samples-*.csv`, the cgroup in bytes). `go-base/gometrics-peak.csv` holds the
  `/metrics` rows at the peak and `go-base/settled.heap-inuse-top.txt` the heap profile's largest
  in-use records at the end.
- **`sus-*/`**, the sustained runs. rgw-go's add `after.memstats.txt`, `after.malloc_info.txt`,
  `after.heap-inuse-top.txt` (the heap profile's largest records in use at the last GC, with their
  first frames) and `gctrace.log`, every gctrace line. `sus-radosgw/` and `sus-base/` hold `smaps`
  grouped by mapping.
- **`cpu-*/gctrace.log`**, every gctrace line of the second of each setting's two CPU runs: each
  start truncated the log, so the first run's is lost. **`cpu.txt`** has one line per CPU run.

The grouping by mapping labels `[heap]` glibc's main arena; in radosgw, which links tcmalloc,
that mapping is tcmalloc's heap.
