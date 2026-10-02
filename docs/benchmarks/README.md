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
quiet host replaces them. They predate the bracketed floor, the floor image
and the per-cell benchmark processes: each floor ran once, after the sweep,
from the cluster's own image, and each mode ran its cells in a few shared
processes. Their REPORT.md files are re-rendered from the unchanged data by
the current `hack/bench/report`, which reads that layout and leaves the
throughput and latency criteria undecided for it.

In each, the sync mode's CPU profile was taken after the rest of the sweep,
from the run directory `<dir>`, with the benchmark binary that sweep built,
`GOMAXPROCS` unset and `ROOKET_NAME=rgw-go-<release>`:

```sh
RGW_GO_TEST_CEPH_CONF=hack/rooket/out/<release>/ceph.conf \
	hack/bench/out/<release>/seam.test -test.run '^$' -test.bench BenchmarkSeam \
	-test.timeout 2h -test.benchtime 20s -mode=sync -shapes=read4k -conc=256 \
	-test.cpuprofile=<dir>/cpu-sync-read4k-256.pprof
```

`seam.sh` built that binary at the start of the sweep with
`go test -c -tags=ceph_preview,integration -o hack/bench/out/<release>/seam.test ./test/bench/seam`,
at the commit `env.json` names under `git_describe`.

A seam run is `make bench-seam RELEASE=<release>` (`hack/bench/seam.sh`,
described in `hack/rooket/README.md`). Its directory holds `env.json`, the
host, cluster and sweep settings; `seam-<mode>.jsonl`, every call of every
cell `BenchmarkSeam` ran in that completion mode; `overbudget-<mode>.jsonl`,
the cells run past the byte budget; `floor.jsonl`, the `rados bench` runs;
`cpu-<mode>-read4k-256.pprof`, the CPU profiles; and `REPORT.md`, which
`go run ./hack/bench/report seam --dir <directory>` renders from them.

The floor's `rados bench` runs from the floor image, the Ceph container image
of the release of the librados the benchmark binary links, so that the Go
cells and their floor use one client release; `seam.sh` stops when it cannot
find that image. A host's librados need not match the cluster: where the
cluster runs another release, its own image's `rados bench` runs once more at
each cell the criteria compare and is reported beside the judged floor, not
judged. `env.json` records both images and the linked librados's version.
Every floor write passes `--no-hints`, since the Go cells send no allocation
hint, and `env.json` records `floor_no_hints`; the 2026-10-01 floors were
written with rados bench's hints. read4m is shown against its floor but not
judged, as `docs/cgo-limitations.md` explains.

A sweep that fails partway, including one whose benchmark process or floor
run a watchdog stops, removes what it left in the pool before it exits. The
toolbox removes a benchmark's objects eight `rados rm` at a time; in a
cut-down Squid run that took 56 s for 52082 objects, so the roughly 530000 a
failed large write4k cell can leave take about ten minutes.
