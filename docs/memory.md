# Memory

How rgw-go sizes its memory, and the settings that trade memory for CPU or
throughput. The numbers come from the [real-world memory
report](benchmarks/2026-11-04-realworld-memory-tentacle/REPORT.md): rgw-go
with the free list ([#559](https://github.com/jhoblitt/rgw-go/pull/559)) in a
2 GiB limit, against Tentacle radosgw, on one 10 GiB OSD: the direction and
size of each effect there, not a promise for your cluster.

## How rgw-go sizes its memory

1. **The Go memory limit.** At start, before it connects to RADOS
   (`internal/cli/serve.go:259`), rgw-go sets the Go runtime's soft memory
   limit to `--memory-limit-ratio` times the cgroup v2 memory limit. The ratio
   is `RGW_GO_MEMORY_LIMIT_RATIO`, 0.8 by default
   (`internal/memlimit/memlimit.go:24`). The limit is the smallest finite
   `memory.max` from the process's cgroup up to the mount's root
   (`internal/memlimit/cgroup.go:42-80`): in a container, the pod's memory
   limit or the unit's `MemoryMax`.
2. **`GOMEMLIMIT` wins.** When the environment holds a non-empty `GOMEMLIMIT`,
   the runtime has applied it already. rgw-go sets none of its own, reads the
   standing one back and sizes the budget from it
   (`internal/memlimit/memlimit.go:100-111`). It also sets none at a ratio of
   0, on cgroup v1, or when no `memory.max` is finite; the budget is then its
   ratio of 80% of the host's physical memory
   (`internal/driver/membudget.go:448-455`), which on a large node is a lot.
   The start-up log says which case applied.
3. **The buffer budget.** The buffers of object data, such as a GET's reads
   and a PUT's pieces and head, draw on one budget of `--memory-budget-ratio`
   (`RGW_GO_MEMORY_BUDGET_RATIO`, above 0 and at most 1, default 0.5) of the
   Go limit (`internal/driver/membudget.go:24`, `:457`). It never falls below
   what one request needs, 68 MiB at the defaults
   (`internal/driver/membudget.go:492-496`); rgw-go warns when it raises the
   budget to that floor. A 2 GiB pod has a 1638 MiB Go limit and an 819 MiB
   budget.
4. **The free list.** A finished request hands its 4 MiB blocks
   (`rgw_max_chunk_size` and `rgw_get_obj_max_req_size` at their defaults) to
   a free list for the next request (`internal/driver/buflist.go:21-36`),
   which the budget caps (`internal/driver/store.go:162`). Idle blocks are
   live Go heap the collector cannot reclaim, which is why the budget is half
   the limit and not all of it (`internal/driver/membudget.go:20-24`). Every
   120 s (`internal/driver/buflist.go:15`) a trim drops the idle blocks no
   request took in that interval, keeping four of each size, and returns the
   heap to the OS (`internal/driver/buflist.go:197-223`). After a burst it
   keeps up to the budget idle until then: about three minutes in the report.
5. **Outside the Go limit.** The C heap, which holds librados's buffers in
   tcmalloc (`internal/radosclient/goceph/tcmalloc.go:13`) and was 46 to
   107 MiB in the report's loads; the socket buffers the cgroup charges; and
   the binary and its libraries. Neither the limit nor the budget counts them.

## The knobs

Peak VmRSS and rates in the report's loads, rep 1 / rep 2, from
[section 5a](benchmarks/2026-11-04-realworld-memory-tentacle/REPORT.md#5a-follow-up-where-the-go-heap-goes):

| setting | 32 x 64 MiB GETs, VmRSS MiB | 4 KiB GETs, VmRSS MiB | 4 KiB GETs/s | 4 KiB PUTs/s | 4 KiB PUT GC cycles |
|---|---|---|---|---|---|
| defaults, 2 GiB | 1513 / 1563 | 337 / 337 | 29166 / 28460 | 1607 / 1532 | 1205 / 1176 |
| 1 GiB limit | 925 / 917 | 227 / 231 | 26598 / 27160 | 1564 / 1694 | 1164 / 1163 |
| `GOGC=50`, 2 GiB | 1263 / 1265 | 226 / 225 | 28744 / 30340 | 1444 / 1489 | 3556 / 3517 |

**The pod's memory limit** (or `MemoryMax`). A smaller limit shrinks the Go
limit and the budget together. At 1 GiB the peak VmRSS fell 40% in the large
GETs and 32% in the 4 KiB GETs, and large-GET throughput held. But the budget
halved to 410 MiB, the 4 KiB GETs ran 7% slower (the smaller budget is the
likelier cause, but the runs do not separate it from the collector's work),
and the cgroup peaked at 906 / 926 of 1024 MiB. It is a smaller pod, not a
free saving. Size the limit for the peak: the 2 GiB pod peaked near 1.5 GiB.

**`--memory-limit-ratio`.** The default leaves a fifth of the limit for what
the runtime does not count: the C heap, which holds librados's buffers, and
the binary and its libraries (`DefaultRatio`'s comment,
`internal/memlimit/memlimit.go:20-23`). The measurement behind
[#554](https://github.com/jhoblitt/rgw-go/pull/554) put the fit for a 2 GiB
pod with tcmalloc at 0.89 to 0.90, and the default stays below it for the
socket buffers the cgroup also charges, up to 217 MiB at 32 concurrent
64 MiB PUTs. A higher ratio gives the heap and the budget more room and
risks the cgroup's OOM kill. A lower one leaves more headroom, shrinks the
budget and, near the limit, makes the collector run more often
(`docs/differences.md`, "The Go runtime's memory limit is sized from the
cgroup's"). A ratio of 0 sets no limit.

**`GOGC`.** rgw-go does not set it, so the runtime's 100 lets the heap grow to
twice the live heap before a collection. `GOGC=50` cut peak VmRSS 18% in the
large GETs and 33% in the 4 KiB GETs, with no GET throughput change beyond
the spread. But it tripled the GC cycles of the 4 KiB PUTs, added 70% to
their CPU (34.1 / 34.6 s against 20.9 / 19.6 s) and lowered their rate 7%
(two reps; the defaults' own two differed by 5%). It leaves the budget alone.
It is a knob for a smaller footprint at that price, not a default.

**`--memory-budget-ratio`.** It bounds the buffers of object data in use, not
the heap: in the large GETs the buffers peaked at 640 MiB and the Go heap in
use at 1329 to 1351 MiB. A request whose first buffer finds the budget spent
waits at most 5 s and is then answered 503 SlowDown, before any byte of its
response and, for a PUT, before its body is read
(`internal/driver/membudget.go:35`); a transfer already under way waits as
long as its client stays and is never failed
(`internal/driver/membudget.go:67-71`). A budget too small for the
concurrency shows as rising `rgw_go_memory_budget_waits_total` and
`rgw_go_memory_budget_slowdowns_total` on the `--metrics-addr` endpoint. Slow
clients cause the same SlowDown, since they hold their shares as long as they
take (`docs/differences.md`, "Object data buffers share one memory budget,
sized from the Go memory limit"). A larger ratio admits more transfers and
keeps more idle blocks, at the collector's expense; a smaller one waits more.

## What to expect compared with radosgw

Against Tentacle radosgw in a 2 GiB pod, rgw-go peaks at about twice its VmRSS
under 32 concurrent 64 MiB transfers (1513 / 1563 MiB against 709 / 654 in
GETs, 2.3x; PUTs 1.9x; the mixed load 2.1x), and at or below it for 256 MiB
objects (0.53 to 0.82x). With 4 KiB objects it is 1.6x under GETs (337 / 337
MiB against 214 / 211) while serving 1.7 to 1.9 times radosgw's GETs a second,
and below half for PUTs (0.47x). At idle it is well below: ten minutes after
the large load rgw-go held 212 / 210 MiB against radosgw's 698 / 644, and
after the mix 243 / 249 against 587 / 595, because radosgw's tcmalloc keeps
what it freed and rgw-go gives memory back.

radosgw from Ceph main has `rgw_heap_release_interval`
([ceph/ceph#72492](https://github.com/ceph/ceph/pull/72492)), which no Squid
or Tentacle release has. Set to 1 in the report, it took radosgw's idle from
826 to 932 MiB down to 408 to 444, where rgw-go idles at 0.50 to 0.56 of that
and peaks at 1.56 to 1.77 times its VmRSS under load.
