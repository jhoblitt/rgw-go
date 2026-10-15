# Benchmarks

One directory per run, holding what the run measured and the report rendered
from it. A claim about rgw-go's performance links the run directory it rests
on.

| Date | Kind | Release | Gateway build | Run |
|---|---|---|---|---|
| 2026-10-02 | seam | Squid, Ceph 19.2.6 | none: the seam microbenchmark at rgw-go 35c9247 | [2026-10-02-seam-squid](2026-10-02-seam-squid/REPORT.md) |
| 2026-10-02 | seam | Tentacle, Ceph 20.2.4 | none: the seam microbenchmark at rgw-go 35c9247 | [2026-10-02-seam-tentacle](2026-10-02-seam-tentacle/REPORT.md) |
| 2026-10-15 | gateway | Squid, Ceph 19.2.7 | rgw-go 252972a against radosgw 19.2.7 | [2026-10-15-gateway-squid](2026-10-15-gateway-squid/REPORT.md) |
| 2026-10-15 | gateway | Tentacle, Ceph 20.2.4 | rgw-go 49c333f against radosgw 20.2.4 | [2026-10-15-gateway-tentacle](2026-10-15-gateway-tentacle/REPORT.md) |
| 2026-10-15 | seam limiter | Squid, Ceph 19.2.7 | none: the seam microbenchmark at rgw-go 49c333f | [2026-10-15-seam-limiter-squid](2026-10-15-seam-limiter-squid/) |
| 2026-10-15 | seam limiter | Tentacle, Ceph 20.2.4 | none: the seam microbenchmark at rgw-go 49c333f | [2026-10-15-seam-limiter-tentacle](2026-10-15-seam-limiter-tentacle/) |

The 2026-10-02 runs, measured on a quiet host, replace the preliminary
2026-10-01 runs, measured on a busy one, which also predate the bracketed
floor, the floor image, `--no-hints` and the per-cell benchmark processes.
They stay in git history: the commit that added the 2026-10-02 runs removed
them (`git log --diff-filter=D -- 'docs/benchmarks/2026-10-01-*'`). The
1-minute load before each sweep's first cell was 0.98 on Squid and 1.14 on
Tentacle, on 32 CPUs (`env.json`'s `load`), and `noise.log` shows no other
build, test or benchmark during either sweep: outside the sweep's own
processes and the cluster's, no process took more than 58% of one CPU in
any sample. Each sweep took its three CPU profiles itself, as its last
cells. Their REPORT.md files are rendered from the committed data by the
current `hack/bench/report`.

The 2026-10-15 runs are the benchmark run the design's §9 deferred until
the floor moved to v19.2.7: per release, the first gateway comparison, and
the seam sweep's over-budget cell re-run under the in-flight limiter. Each
gateway directory holds the radosgw run and the rgw-go run, each with the
REPORT.md `go run ./hack/bench/report gateway --dir <run>` renders, and the
side-by-side REPORT.md that
`go run ./hack/bench/report gateway --dir <radosgw run> --dir <rgw-go run> --out <directory>/REPORT.md`
renders. rgw-go's server code is the same at 252972a and 49c333f, which
differ only in tooling and the s3-tests ledgers. No phase of any run had an
error or a retry. The host was not equally quiet for every run:

- Squid's two gateway runs (09:57 to 11:08 UTC) ran with Firefox and Chrome
  open; a browser content process used 46% of a CPU in the sample before the
  radosgw run. The rgw-go run's own report step failed on a gctrace line
  torn by rgw-go's log; its measurements are complete, and its REPORT.md is
  rendered by the report tool that
  [#326](https://github.com/jhoblitt/rgw-go/pull/326) fixed. Squid's limiter
  cell and all of Tentacle ran from 19:50 UTC, with both browsers closed;
  Zoom, open and idle, closed at 19:50:45, during Tentacle's cluster
  bring-up, before any measured run. The WARP and tailscale clients ran
  throughout.
- The 1-minute load before each run, from its `env.json`: 1.83 and 2.68
  (Squid's radosgw and rgw-go runs), 1.53 and 2.57 (Tentacle's), 2.19 and
  6.99 (the Squid and Tentacle limiter cells). The higher loads before each
  rgw-go run (5- and 15-minute: 20.77 and 33.71 on Squid, 17.19 and 24.39 on
  Tentacle) and before Tentacle's limiter cell follow the run that ended
  minutes before it.
- One pause on Squid's rgw-go small-get, 755 ms, is a single GC
  stop-the-world pause at 10:46:20 UTC; that profile's next-longest is under
  7 ms on either release. Squid's noop gctrace log has 1 torn line and
  Tentacle's 2; the report counts their cycles and leaves their pauses out.
- radosgw runs in the cluster and is reached on the cluster's network,
  port 80 (`env.json`'s `endpoint`); rgw-go runs on the host and is reached
  on 127.0.0.1.
- An earlier attempt, on a busy host, is not kept.

The limiter cell's directories hold `overbudget-<mode>.jsonl`, `env.json`,
`sweep.log`, `noise.log` and `bench-<mode>.log`, and no REPORT.md, since
`hack/bench/report` renders only a whole sweep. With the limiter's byte
bound at 100 MiB, 615 of the 640 writes parked on it in every mode on both
releases. The process grew +35 and +37 threads in sync, +19 and +19 in
callback and +22 and +20 in pipe (Squid, Tentacle), with no errors.

A seam run is `make bench-seam RELEASE=<release>` (`hack/bench/seam`,
described in `hack/rooket/README.md`). Its directory holds `env.json`, the
host, cluster and sweep settings; `seam-<mode>.jsonl`, every call of every
cell `BenchmarkSeam` ran in that completion mode; `overbudget-<mode>.jsonl`,
the cells run past the byte budget; `floor.jsonl`, the `rados bench` runs;
`cpu-<mode>-read4k-256.pprof`, the CPU profiles; `noise.log`, the load and
up to 12 processes that used 5% of a CPU or more over one second, every
15 s; and `REPORT.md`, which
`go run ./hack/bench/report seam --dir <directory>` renders from all of
them but `noise.log`.

The floor's `rados bench` runs from the floor image, the Ceph container image
of the release of the librados the benchmark binary links, so that the Go
cells and their floor use one client release; the sweep stops when it cannot
find that image. A host's librados need not match the cluster: where the
cluster runs another release, its own image's `rados bench` runs once more at
each cell the criteria compare and is reported beside the judged floor, not
judged. `env.json` records both images and the linked librados's version.
Every floor write passes `--no-hints`, since the Go cells send no allocation
hint, and `env.json` records `floor_no_hints`. read4m is shown against its
floor but not judged, as `docs/cgo-limitations.md` explains.

A sweep that fails partway, including one whose benchmark process or floor
run a watchdog stops, removes what it left in the pool before it exits. It
hands the names of a benchmark's objects to one toolbox session, whose xargs
removes them eight `rados rm` at a time, 1024 names each, as
`hack/bench/seam.sh`, the shell sweep the command replaced, did. A cut-down
Squid run of `seam.sh` removed 52082 objects that way in 56 s, so the roughly
530000 a failed large write4k cell can leave should take about ten minutes;
no run of the Go command has measured it yet.
