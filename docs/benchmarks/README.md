# Benchmarks

One directory per run, holding what the run measured and the report rendered
from it. A claim about rgw-go's performance links the run directory it rests
on.

| Date | Kind | Release | Gateway build | Run |
|---|---|---|---|---|
| 2026-10-02 | seam | Squid, Ceph 19.2.6 | none: the seam microbenchmark at rgw-go 35c9247 | [2026-10-02-seam-squid](2026-10-02-seam-squid/REPORT.md) |
| 2026-10-02 | seam | Tentacle, Ceph 20.2.4 | none: the seam microbenchmark at rgw-go 35c9247 | [2026-10-02-seam-tentacle](2026-10-02-seam-tentacle/REPORT.md) |

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

A seam run is `make bench-seam RELEASE=<release>` (`hack/bench/seam.sh`,
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
cells and their floor use one client release; `seam.sh` stops when it cannot
find that image. A host's librados need not match the cluster: where the
cluster runs another release, its own image's `rados bench` runs once more at
each cell the criteria compare and is reported beside the judged floor, not
judged. `env.json` records both images and the linked librados's version.
Every floor write passes `--no-hints`, since the Go cells send no allocation
hint, and `env.json` records `floor_no_hints`. read4m is shown against its
floor but not judged, as `docs/cgo-limitations.md` explains.

A sweep that fails partway, including one whose benchmark process or floor
run a watchdog stops, removes what it left in the pool before it exits. The
toolbox removes a benchmark's objects eight `rados rm` at a time; in a
cut-down Squid run that took 56 s for 52082 objects, so the roughly 530000 a
failed large write4k cell can leave take about ten minutes.
