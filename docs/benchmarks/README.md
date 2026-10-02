# Benchmarks

One directory per run, holding what the run measured and the report rendered
from it. A claim about rgw-go's performance links the run directory it rests
on.

| Date | Kind | Release | Gateway build | Run |
|---|---|---|---|---|
| 2026-10-01 | seam, preliminary | Squid, Ceph 19.2.6 | none: the seam microbenchmark at rgw-go fcd09fb | [2026-10-01-seam-squid](2026-10-01-seam-squid/REPORT.md) |
| 2026-10-01 | seam, preliminary | Tentacle, Ceph 20.2.4 | none: the seam microbenchmark at rgw-go fcd09fb | [2026-10-01-seam-tentacle](2026-10-01-seam-tentacle/REPORT.md) |

Both 2026-10-01 runs are preliminary: the host was busy throughout, as
`docs/cgo-limitations.md`, "Phase 1 measurement", records, and a re-run on a
quiet host replaces them. In each, the sync mode's CPU profile was taken
after the rest of the sweep.

A seam run is `make bench-seam RELEASE=<release>` (`hack/bench/seam.sh`,
described in `hack/rooket/README.md`). Its directory holds `env.json`, the
host, cluster and sweep settings; `seam-<mode>.jsonl`, every call of every
cell `BenchmarkSeam` ran in that completion mode; `overbudget-<mode>.jsonl`,
the cells run past the byte budget; `floor.jsonl`, the `rados bench` runs;
`cpu-<mode>-read4k-256.pprof`, the CPU profiles; and `REPORT.md`, which
`go run ./hack/bench/report seam --dir <directory>` renders from them.
