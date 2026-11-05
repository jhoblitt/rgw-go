# Real-world memory: rgw-go against Tentacle radosgw

Measured on 2026-11-04 on the rooket cluster `rgw-go-tentacle` (Ceph v20.2.4) and, for RM and RMH, on a
disposable second cluster, `rgw-go-main` (Ceph main, section 2). Squid was not measured. The question is
the owner's: how does rgw-go's real-world memory use compare with Tentacle radosgw's. The gateways are
R, radosgw v20.2.4; M, rgw-go 01ebd5a (the memory budget and `sync.Pool`s); F, rgw-go 296ad44 (the memory
budget and one free list, [#559](https://github.com/jhoblitt/rgw-go/pull/559)); and RM and RMH, radosgw
from Ceph main without and with `rgw_heap_release_interval = 1`. rgw-go's own code is cited as
`path:line` at 01ebd5a (M) or 296ad44 (F); Ceph's by GitHub link pinned to v20.2.4 or, for the option
only main has, to the main commit its change landed in. It follows the memory measurement
[2026-11-02-memory-tentacle](../2026-11-02-memory-tentacle/REPORT.md), the
[allocator](../2026-11-03-allocator-tentacle/REPORT.md) and [buffer](../2026-11-03-buffers-tentacle/REPORT.md)
measurements, which used an in-process burst over fakerados or one load at a time. The files are listed in
section 9.

## 1. Answer

How rgw-go's real-world memory compares with Tentacle radosgw's depends on the load. Peak VmRSS, in
MiB, rep 1 / rep 2; M is rgw-go 01ebd5a (`sync.Pool`s), F is rgw-go 296ad44 (the free list of
[#559](https://github.com/jhoblitt/rgw-go/pull/559)), R is radosgw 20.2.4 in its pod, all in a 2 GiB limit; ratios are of the two
reps' means.

- **Large objects, 32 concurrent 64 MiB.** rgw-go peaks at about twice radosgw. PUTs: M 950 / 947 and F
  931 / 930 against R's 489 / 486 (1.95x and 1.91x). GETs: M 1 696 / 1 663 and F 1 513 / 1 563 against 709 / 654
  (2.46x and 2.26x). The three moved the same bytes (GETs 543-582 MiB/s). Afterwards radosgw stays at its
  high-water mark (696 / 642 at +30 s and 698 / 644 at +600 s: tcmalloc keeps what it freed), while rgw-go
  gives memory back: by +330 s M is at 174 / 166 and F at 204 / 210, 0.25 and 0.31 of R's.
- **Very large objects, 256 MiB at 1 and 2 in flight.** rgw-go is at or below radosgw: M 0.79-1.09x and F
  0.53-0.82x of R's 144-254, at the same rates within the spread.
- **Small objects, 64 concurrent 4 KiB.** PUTs: M 95 / 93 and F 94 / 94 against R's 200 / 196 (0.47x). GETs: M
  732 / 726 (3.43x R's 214 / 211) and F 337 / 337 (1.59x), while rgw-go serves 1.7-1.9x radosgw's GETs a
  second (27 776-30 592 against 16 350-16 658).
- **A sustained mix, 30 000 requests, 70% GET, 4 KiB to 256 MiB, 32 in flight.** Peak M 1 583 / 1 472 (2.57x R's
  590 / 599) and F 1 255 / 1 206 (2.07x); ten idle minutes later R is at 587 / 595, M at 207 / 204 (0.35) and F at
  243 / 249 (0.42). The OSD bound the request rates, which were the same for all.
- **Radosgw from Ceph main, with and without `rgw_heap_release_interval = 1` (RM, RMH).** RM is larger than R
  in every load: 1.4-1.5x at the large and mix peaks, 2.0-2.8x with 256 MiB and 4 KiB objects, from a 381 MiB
  start (R: 114); the build also differs in OS, tcmalloc (2.15 against 2.9.1) and thread pool, and the data cannot
  separate those. The option returns the free memory within 30 s of a burst: RMH idles at 434 / 408 after the
  large load (RM 932 / 897) and 437 / 444 after the mix (RM 826 / 830), and lowers the 32-way GET and mix peaks
  by 6-7%, at CPU within -1% and +7% of RM's; with small objects there is nothing to release and RM and RMH
  are equal. That leaves radosgw at about twice rgw-go's idle (F is 0.50-0.56 of RMH, M 0.40-0.47) and, at the
  loaded peaks, 0.56-0.64 of F's (F is 1.77x RMH's in the large GETs and 1.56x in the mix).

**Measured and inferred.** Everything above is measured, on one 10 GiB OSD with the client on the host, two
reps per cell, and holds for that cluster, those buffer sizes and the radosgw builds named. Inferred: that
the Go heap above the memory budget is GC headroom and the head copy's garbage (tested with variants on the
32 x 64 MiB and 4 KiB loads only, section 5a); that radosgw's idle footprint is tcmalloc keeping what it
freed (tcmalloc's own statistics agree for R at the end of the runs and for RM and RMH throughout); that
the free list's blocks act as a GC ballast (read from the numbers, not tested). What holds the 308 MiB a fresh
main radosgw has was not found.

## 2. Method

Date 2026-11-04, cluster rooket `rgw-go-tentacle` (Ceph v20.2.4, one OSD of 10 GiB, Rook's
radosgw), and for RM and RMH a second, disposable cluster, `rgw-go-main`, below. The scripts are
[scripts/](scripts/), the tool (sampler, mix driver, analysis) is [_rwtool/](_rwtool/) (section 9),
and each run's raw data (`runs/<load>/<variant>-<rep>/`: samples, smaps snapshots, gctrace logs)
stayed in the run's scratch directory.

### Variants

| id | what | binary sha256 |
|---|---|---|
| R | radosgw v20.2.4, the cluster's own pod, tcmalloc (image's), container limit 2Gi, memory.swap.max 0, no CPU limit | (the image's) |
| M | rgw-go 01ebd5a (origin/main): memory budget + `sync.Pool`s | `0f7eb90c...b325` |
| F | rgw-go 296ad44 (`driver-free-list`, #559): memory budget + one free list | `831dd2d3...9973` |
| RM | radosgw from Ceph main, the image `quay.ceph.io/ceph-ci/ceph@sha256:c928697602b7...e7e06` (ceph 22.0.0-442-gd7075594), in the Rook radosgw pod of the disposable cluster `rgw-go-main`: container limit 2Gi, memory.swap.max 0, no CPU limit, `rgw_heap_release_interval` 0 (the default) | (the image's) |
| RMH | RM with `rgw_heap_release_interval` 1 | (the image's) |

- M and F were built one after the other from detached checkouts, `gobuild.sh go build
  -tags=ceph_preview ./cmd/rgw-go`: go1.27.3, `vcs.modified=false`, DT_NEEDED libtcmalloc.so.4 and
  librados.so.2, RPATH the host's copies of the Tentacle image's librados 20.2.4 and gperftools
  2.9.1 tcmalloc ([logs/bininfo.txt](logs/bininfo.txt), [out/builds.txt](out/builds.txt)). tcmalloc is linked, not preloaded.
- rgw-go ran on the host with rgw-go-up.sh's command line, from an on-disk copy of the binary, in
  a user systemd scope with `MemoryMax=2147483648` and `MemorySwapMax=0`, the radosgw container's
  limits as found (`memory.max` 2147483648, `memory.swap.max` 0). From it `internal/memlimit` set
  the Go memory limit to 1717986918 B (1638 MiB, ratio 0.8) and the memory budget to 858993459 B
  (819 MiB, ratio 0.5), as each run's log records. GOMAXPROCS, GOFLAGS, GOMEMLIMIT, GOGC and every
  allocator variable unset; `GODEBUG=gctrace=1`.
- radosgw was restarted before each run by deleting its pod; the ReplicaSet recreated it from the
  unchanged deployment. While M or F ran, radosgw sat idle in its pod; one gateway carried load at
  a time.
- Same options: both config entities hold the parity options, and every buffer, window and
  concurrency option compared is equal ([setup/options.txt](setup/options.txt)): rgw_max_chunk_size,
  rgw_obj_stripe_size and rgw_get_obj_max_req_size 4 MiB, the get and put windows 16 MiB,
  rgw_max_concurrent_requests 1024, objecter_inflight_op_bytes 100 MiB, objecter_inflight_ops
  24576 in both (radosgw's module default,
  [rgw_main.cc:80-86](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_main.cc#L80-L86),
  which rgw-go applies too). Same zone, data pool and bucket names; plain HTTP, path-style, the
  bench harness's fixed-key user; radosgw at `http://10.89.0.124:80` (host-networked in the kind
  node), rgw-go at `http://127.0.0.1:7482`.

### RM and RMH: radosgw from Ceph main, with and without heap release

The owner asked for radosgw from Ceph main with `rgw_heap_release_interval = 1` in the comparison.
The option came with [ceph/ceph#72492](https://github.com/ceph/ceph/pull/72492), merged on 2026-10-21 as
151439b6213391e814da8ac48b031a871b7a0c21; no Squid or Tentacle release has it. When radosgw links
tcmalloc, a thread wakes every `rgw_heap_release_interval` seconds (0, the default, is off; the value
is read again each pass) and, unless tcmalloc's page heap holds less free memory than
`rgw_heap_release_bytes` (default 64 MiB), asks tcmalloc to hand about that many bytes of free spans
back to the OS ([rgw.yaml.in:1388-1411](https://github.com/ceph/ceph/blob/151439b6213391e814da8ac48b031a871b7a0c21/src/common/options/rgw.yaml.in#L1388-L1411),
[rgw_appmain.cc:380-419](https://github.com/ceph/ceph/blob/151439b6213391e814da8ac48b031a871b7a0c21/src/rgw/rgw_appmain.cc#L380-L419)). It returns only memory tcmalloc
holds as free; memory the application holds is untouched.

- **The image.** `quay.ceph.io/ceph-ci/ceph@sha256:c928697602b7fe...` (built 2026-11-05 00:10 UTC).
  `ceph --version` inside it: `ceph version 22.0.0-442-gd7075594 (d70755948de6884af4140268e388ccf95ec3657f)
  vampire (dev)`; that commit is on main and has 151439b6213 as an ancestor (`git merge-base
  --is-ancestor`, 776 commits later). `radosgw --show-config` lists `rgw_heap_release_interval` (0) and
  `rgw_heap_release_bytes` (64 MiB). It is Rocky Linux 10.2 and radosgw links gperftools-libs
  2.15-4.el10_0 (`libtcmalloc.so.4.5.16`); R's image is CentOS Stream 9 with gperftools-libs 2.9.1-3.el9
  ([logs/imagecheck.log](logs/imagecheck.log)). The tcmalloc differs, so RM, not R, is RMH's control.
- **How it was fetched.** A plain `podman pull` ran at 110 KB/s on this host, so the manifest and the
  blobs were fetched over 24 ranged connections (`scripts/main-cluster/fastpull.py`): the manifest's
  sha256 is the pinned digest, each blob's is the manifest's, and skopeo copied the layout unchanged into
  the new cluster's own registry, where the manifest again has the pinned digest.
- **Where they ran.** Not against `rgw-go-tentacle`: a main radosgw may call class methods a Tentacle
  OSD lacks, and the owner ruled the combination out. RM and RMH ran in
  radosgw's own Rook pod of `rgw-go-main`, a disposable rooket cluster of the same shape as the
  Tentacle one (Rook v1.20.7 as R's cluster, one worker, one 10 GiB OSD, host networking, the Tentacle
  cluster's chart values with the image replaced; `allowUnsupported: true`, because that Rook does not
  know Ceph main) in [scripts/main-cluster/](scripts/main-cluster/). Its mon, mgr, OSD and radosgw are
  the one main build, where R's cluster runs 20.2.4 throughout, so what is compared is the gateway
  process's memory, not the clusters. Its radosgw was at `http://10.89.0.214:80`, host-networked in the new kind node.
- **Run as R was.** The pod has R's resources (rgw container limit 2Gi, requests 500m and 1Gi,
  log-collector 1Gi, [setup-main/](setup-main/)), the parity options, and Rook's `log_to_file true`,
  which was left on so that RM and RMH have R's page-cache charge in `memory.current` (shown with and
  without it, as the working set). The pod was deleted before each run, as R's was. The option was set with
  `ceph config set client.rgw.ceph.objectstore.a rgw_heap_release_interval 1` (`config rm` for RM)
  before the restart and confirmed on the running radosgw's admin socket (`config get`, in each run's
  `start.txt`). Sampling is R's: the process from the host, the cgroup, the in-pod PSS loop,
  smaps snapshots, `dump_mempools`; and, since main's admin socket has it, tcmalloc's `heap stats` at
  every snapshot (Tentacle's has no such command).
- **Options.** Every sizing option compared matches R's but `rgw_thread_pool_size`: 128 on main
  (`rgw_thread_pool_size`, [rgw.yaml.in:1375-1383](https://github.com/ceph/ceph/blob/d70755948de6884af4140268e388ccf95ec3657f/src/common/options/rgw.yaml.in#L1375-L1383), lowered by d22252b6b5a on
  2026-06-24) against 512 on 20.2.4
  (`rgw_thread_pool_size`, [rgw.yaml.in:1161-1171](https://github.com/ceph/ceph/blob/v20.2.4/src/common/options/rgw.yaml.in#L1161-L1171)). Main's default was kept,
  so RM and RMH have fewer request threads than R ([setup-main/options.txt](setup-main/options.txt)).
- **What RM bundles.** Against R, RM differs by the cluster's daemons (22.0.0 against 20.2.4), the OS
  and glibc, tcmalloc 2.15 against 2.9.1, the thread pool, and main's radosgw code. A fresh main
  radosgw is also larger: 390 MiB RSS 30 s after a start, of which tcmalloc reports 308 MiB in use by
  the application and 0.05 MiB free, and Ceph's mempools hold 0.25 MiB; R's is 114 MiB. What holds the
  308 MiB was not found. RMH against RM isolates the option.
- **Smoke test, before any measurement.** Create bucket, PUT, multipart upload (25 MB, 3 parts) and an
  aborted one, GET with equal bytes, ranged GET, listing of objects, buckets, uploads and parts, delete:
  all passed through the cluster's own radosgw, and its logs (434 lines) held no error, no
  `-EOPNOTSUPP` and no `-95` ([setup-main/smoke.log](setup-main/smoke.log)).
- **One cluster busy at a time.** The Tentacle cluster carried no load during the 16 RM/RMH runs, which started
  after the main cluster was up and ran one after another from `main-plan.txt` (all four loads, two
  reps each, RM and RMH alternating).

### Sampling

Once a second for the whole run (`rwtool sample`, from the host): `/proc/<pid>/status` (VmRSS,
RssAnon, RssFile, VmHWM, Threads), `smaps_rollup` (PSS), the gateway's cgroup (`memory.current`,
`memory.peak`, `memory.stat`), and for rgw-go its `/metrics` (Go heap, Go memory limit, budget,
F's `rgw_go_memory_budget_free_list_bytes`). radosgw's PSS cannot be read from the host (other
uid), so a loop in its pod's log-collector container, which shares the pod's pid namespace but not
the rgw container's cgroup, read it once a second (`podpss.sh`). Working set is `memory.current -
inactive_file`. smaps snapshots, grouped by mapping, after each phase, during the middle of the
long phases, and at each idle point; at the very end, after sampling stopped, tcmalloc's own
statistics for rgw-go (gdb) and `dump_mempools` for radosgw.

### Loads

One client per load for every variant, from the host: the earlier spikes' `s3load` (alloc spike
binary, sha256 `0ab74154...cc84`, the pools spike's frozen copy) for loads 1-3, and for the mix a
driver of the same client settings (`rwtool mix`: aws-sdk-go-v2, path-style, the bench keys,
checksums only when required, no retries, an idle connection per thread). Every phase is
count-bound, so each variant does the same work; the throughput is recorded beside it. Each run
starts its gateway fresh, idles 15 s, then:

| load | phases |
|---|---|
| 1 large | PUT: 32 threads x one 64 MiB object (2 GiB, one wave); GET: 16 waves of the 32 whole objects at 32 in flight (512 GETs, 32 GiB); then 600 s idle |
| 2 very large | 6 back-to-back whole 256 MiB PUTs at 1 in flight, then 4 waves of 6 GETs of them; the same at 2 in flight (2 threads x 3) |
| 3 small | PUT: 64 threads x 400 objects of 4 KiB (25 600); GET: 40 waves of those 25 600 at 64 in flight (1 024 000 GETs) |
| 4 mix | prep writes a get set and a put set (each 1000 x 4 KiB, 100 x 1 MiB, 4 x 64 MiB, 2 x 256 MiB) and the gateway is restarted; then the first 30 000 requests of a sequence fixed by its seed, 70% GET / 30% PUT, sizes 4 KiB 50%, 1 MiB 30%, 64 MiB 15%, 256 MiB 5% by request count, 32 in flight; GETs read the get set, PUTs overwrite the put set; then 600 s idle |

A GET wave is one s3load invocation in which every thread reads each of its objects once; between
waves, in-flight requests dip for the last stragglers and a new process's connections.

**The mix and the 10 GiB OSD.** The parity options turn both gateways' garbage collector off, and
the mix overwrites ~200 MiB/s. The first mix smoke filled the OSD in 45 s and crashed it (section
7). The campaign's mix therefore runs beside a back-to-back `radosgw-admin gc process --include-all`
(64 deletes at a time) and a guard that reads the OSD's own counters every 2 s: it holds new
requests back once BlueStore's allocated bytes are 2.5 GiB above their value at the mix's start
(the data set), lets them go within 1 GiB of it (in-flight ones finish), and kills the driver at
4.5 GiB above it or at 8 GiB of BlueStore and BlueFS together. The seconds paused are counted per run; the requests are the same 30 000 either way. Runs
of 19 000 and 24 000 requests came in under 10 minutes (503 and 578 s for R; the rate rises with
the run's length) and were set aside (not kept here); every mix run in the tables did 30 000.

## 3. Results per load

Generated tables with every column, one cell per run in rep order:
[out/tables.md](out/tables.md) (`rwtool analyze`); smaps by mapping: [out/breakdown.md](out/breakdown.md). Each
table here adds RM and RMH, the Ceph main radosgw without and with `rgw_heap_release_interval = 1`, and the
ratios RM/R, RMH/RM, RMH/R and F/RMH.

### 3.1 Large objects: 32 x 64 MiB PUT, then 16 waves of 32 GETs

MiB; each cell is rep 1 / rep 2. Peak: the highest 1 s sample from a phase's start to 1 s past its
end. Steady: the median of the phase's second half. Ratios are of the two reps' means as printed here
(they can differ by 0.01 from `out/tables.md`'s, which uses the unrounded means). RM and RMH are radosgw
from Ceph main without and with heap release (section 2).

| phase | metric | R | M | F | RM | RMH | M/R | F/R | F/M | RM/R | RMH/RM | RMH/R | F/RMH |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| PUT | VmRSS peak | 489 / 486 | 950 / 947 | 931 / 930 | 751 / 747 | 747 / 747 | 1.95 | 1.91 | 0.98 | 1.54 | 1.00 | 1.53 | 1.25 |
| PUT | RssAnon peak | 450 / 448 | 914 / 912 | 895 / 894 | 691 / 690 | 688 / 690 | 2.03 | 1.99 | 0.98 | 1.54 | 1.00 | 1.53 | 1.30 |
| PUT | PSS peak | 479 / 476 | 943 / 940 | 924 / 923 | 737 / 733 | 733 / 735 | 1.97 | 1.93 | 0.98 | 1.54 | 1.00 | 1.54 | 1.26 |
| PUT | working set peak | 492 / 484 | 1098 / 1110 | 1097 / 1056 | 721 / 719 | 717 / 715 | 2.26 | 2.21 | 0.98 | 1.48 | 0.99 | 1.47 | 1.50 |
| GET | VmRSS peak | 709 / 654 | 1696 / 1663 | 1513 / 1563 | 950 / 917 | 880 / 862 | 2.46 | 2.26 | 0.92 | 1.37 | 0.93 | 1.28 | 1.77 |
| GET | VmRSS steady | 702 / 648 | 1694 / 1661 | 1510 / 1540 | 940 / 905 | 807 / 753 | 2.49 | 2.26 | 0.91 | 1.37 | 0.85 | 1.16 | 1.96 |
| GET | RssAnon peak | 670 / 616 | 1659 / 1627 | 1477 / 1527 | 893 / 860 | 821 / 805 | 2.56 | 2.34 | 0.91 | 1.36 | 0.93 | 1.26 | 1.85 |
| GET | PSS peak | 698 / 643 | 1689 / 1656 | 1506 / 1556 | 937 / 903 | 910 / 849 | 2.49 | 2.28 | 0.92 | 1.37 | 0.96 | 1.31 | 1.74 |
| GET | working set peak | 696 / 641 | 1671 / 1652 | 1495 / 1535 | 907 / 873 | 834 / 818 | 2.49 | 2.27 | 0.91 | 1.33 | 0.93 | 1.24 | 1.83 |
| GET | cgroup memory.current peak | 697 / 642 | 1671 / 1654 | 1495 / 1535 | 908 / 874 | 835 / 818 | 2.48 | 2.26 | 0.91 | 1.33 | 0.93 | 1.23 | 1.83 |
| GET | cgroup memory.peak | 699 / 650 | 1684 / 1662 | 1500 / 1543 | 1177 / 1145 | 1060 / 1066 | 2.48 | 2.26 | 0.91 | 1.72 | 0.92 | 1.58 | 1.43 |
| GET | threads | 605 / 605 | 60 / 64 | 68 / 55 | 215 / 215 | 215 / 215 |  |  |  |  |  |  |  |

rgw-go's Go runtime in the GETs: HeapInuse peak M 1452 / 1453, F 1351 / 1329; heap resident (HeapSys
- HeapReleased) M 1537 / 1510, F 1361 / 1409; budget held 640 for both (768 in the PUT); F's free
list 640 at the phase's end. Go memory limit 1638 MiB in every rgw-go run.

| phase | throughput | R | M | F | RM | RMH |
|---|---|---|---|---|---|---|
| PUT (2 GiB) | MiB/s | 568 / 433 | 573 / 477 | 532 / 538 | 427 / 407 | 494 / 552 |
| GET (32 GiB) | MiB/s | 582 / 562 | 478 / 565 | 577 / 543 | 609 / 553 | 624 / 555 |

No request failed. M's first-rep GET (478) is the one outlier; its second matched the others. The GETs ran
553-624 MiB/s on RM and RMH, R's 562-582: the same within the spread.

Against R, RM's peak is 1.37x in the GETs and 1.54x in the PUTs; RMH's is 0.93x of RM's in the GETs
(880 / 862 against 950 / 917) and equal in the PUTs. RM's and RMH's `memory.peak` (1 145-1 177 and 1 060-1 066
MiB) is 225-270 MiB above the highest `memory.current` the sampler saw (908 / 874 and 835 / 818); the
excess is not located here, and the working set and RSS are the like-for-like figures.

### 3.2 Very large objects: back-to-back 256 MiB at 1, then 2, in flight

VmRSS peak, MiB:

| phase | metric | R | M | F | RM | RMH | M/R | F/R | F/M | RM/R | RMH/RM | RMH/R | F/RMH |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| 6 PUTs at 1 | VmRSS peak | 144 / 146 | 149 / 143 | 112 / 119 | 412 / 411 | 410 / 406 | 1.01 | 0.80 | 0.79 | 2.84 | 0.99 | 2.81 | 0.28 |
| 24 GETs at 1 | VmRSS peak | 253 / 238 | 204 / 186 | 132 / 130 | 516 / 483 | 482 / 481 | 0.79 | 0.53 | 0.67 | 2.03 | 0.96 | 1.96 | 0.27 |
| 6 PUTs at 2 | VmRSS peak | 254 / 239 | 225 / 229 | 171 / 171 | 513 / 484 | 447 / 439 | 0.92 | 0.69 | 0.75 | 2.02 | 0.89 | 1.80 | 0.39 |
| 24 GETs at 2 | VmRSS peak | 254 / 240 | 277 / 260 | 202 / 202 | 515 / 483 | 636 / 503 | 1.09 | 0.82 | 0.75 | 2.02 | 1.14 | 2.31 | 0.35 |

Throughput, MiB/s:

| phase | throughput | R | M | F | RM | RMH |
|---|---|---|---|---|---|---|
| 6 PUTs at 1 | MiB/s | 324 / 327 | 376 / 362 | 314 / 372 | 328 / 302 | 325 / 325 |
| 24 GETs at 1 | MiB/s | 507 / 548 | 387 / 545 | 545 / 528 | 574 / 504 | 455 / 344 |
| 6 PUTs at 2 | MiB/s | 398 / 424 | 370 / 381 | 356 / 362 | 442 / 382 | 394 / 395 |
| 24 GETs at 2 | MiB/s | 428 / 582 | 409 / 559 | 573 / 443 | 606 / 384 | 489 / 410 |

PSS and RssAnon give the same ratios within 0.06. HeapInuse peaks: M 70-168, F 42-103 MiB.

RM and RMH hold 406-636 MiB in these loads where R holds 144-254, 2.0-2.8x R's: the 381 MiB a fresh main
radosgw already has (section 5) is most of it. RMH's 24 GETs at 2 reached 636 MiB in rep 1 and 503 in rep 2,
RM's 515 and 483; the release does not lower these peaks, and one rep of it is higher. The 24 GETs at 1 in flight
ran at 455 / 344 MiB/s on RMH, the lowest of the five in both reps, against 574 / 504 on RM; but a gateway's two
reps differ by up to 32% in these 10-18 s phases (RMH 455 / 344; R 428 / 582 at 2 in flight), so the data do not
show that as the release's cost.

### 3.3 Small objects: 25 600 PUTs of 4 KiB, then 40 waves of 25 600 GETs, at 64 in flight

| phase | metric | R | M | F | RM | RMH | M/R | F/R | F/M | RM/R | RMH/RM | RMH/R | F/RMH |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| PUT | VmRSS peak | 200 / 196 | 95 / 93 | 94 / 94 | 436 / 435 | 436 / 433 | 0.47 | 0.47 | 1.00 | 2.20 | 1.00 | 2.19 | 0.22 |
| GET | VmRSS peak | 214 / 211 | 732 / 726 | 337 / 337 | 438 / 437 | 438 / 435 | 3.43 | 1.59 | 0.46 | 2.06 | 1.00 | 2.05 | 0.77 |
| GET | VmRSS steady | 214 / 211 | 708 / 711 | 337 / 337 | 438 / 437 | 438 / 435 | 3.34 | 1.59 | 0.47 | 2.06 | 1.00 | 2.05 | 0.77 |
| GET | PSS peak | 203 / 201 | 725 / 720 | 330 / 331 | 424 / 425 | 424 / 423 | 3.58 | 1.64 | 0.46 | 2.10 | 1.00 | 2.10 | 0.78 |
| GET | working set peak | 222 / 220 | 704 / 699 | 309 / 309 | 672 / 417 | 414 / 415 | 3.17 | 1.40 | 0.44 | 2.46 | 0.76 | 1.88 | 0.75 |
| GET | cgroup memory.current peak | 922 / 931 | 704 / 699 | 310 / 309 | 1118 / 1118 | 1115 / 1121 | 0.76 | 0.33 | 0.44 | 1.21 | 1.00 | 1.21 | 0.28 |

| phase | throughput | R | M | F | RM | RMH |
|---|---|---|---|---|---|---|
| PUT | requests/s | 1605.1 / 1720.6 | 1599.4 / 1788.0 | 1607.2 / 1532.4 | 1506.1 / 1508.5 | 1692.7 / 1547.8 |
| GET | requests/s | 16658.0 / 16349.9 | 30592.4 / 27775.5 | 29165.7 / 28459.6 | 21318.3 / 21025.3 | 21229.5 / 21157.7 |

radosgw's cgroup `memory.current` reached 922-931 MiB in the GETs against a VmRSS of 211-214: about
700 MiB was page cache of radosgw's own log file, which it writes on every request because the mon
config holds `log_to_file true` (Rook's log collector), charged to its container. rgw-go writes no
per-request log file, so for radosgw `memory.current` overstates; RSS, PSS and the working set
(which leaves inactive page cache out, as the kubelet's eviction does) are the like-for-like
figures. rgw-go serves the 4 KiB GETs 1.7-1.9x as fast as radosgw.

RM and RMH hold 433-438 MiB in the small loads, 2.05-2.2x R's, in the PUTs and the GETs alike: that is
their floor (section 5), and the release does nothing here because the page heap holds only 16-17 MiB free,
under its 64 MiB threshold (section 5b). They serve the 4 KiB GETs at 21.0-21.3 thousand a second, 1.3x R's and
0.73x F's. Their `memory.current` peak is 1 115-1 121 MiB, some 680 above their VmRSS; for R the like excess was the
page cache of the radosgw's log file, which was not checked for these.

### 3.4 Sustained mix: 30 000 requests at 32 in flight

70% GET / 30% PUT; 4 KiB 50%, 1 MiB 30%, 64 MiB 15%, 256 MiB 5% of the requests. Rep 1 / rep 2;
ratios of the means. Steady: the median of the mix's second half.

| phase | metric | R | M | F | RM | RMH | M/R | F/R | F/M | RM/R | RMH/RM | RMH/R | F/RMH |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| mix | VmRSS peak | 590 / 599 | 1583 / 1472 | 1255 / 1206 | 833 / 844 | 779 / 797 | 2.57 | 2.07 | 0.81 | 1.41 | 0.94 | 1.33 | 1.56 |
| mix | VmRSS steady | 578 / 584 | 1368 / 1336 | 1234 / 1181 | 824 / 833 | 703 / 706 | 2.33 | 2.08 | 0.89 | 1.43 | 0.85 | 1.21 | 1.71 |
| mix | RssAnon peak | 552 / 560 | 1547 / 1436 | 1219 / 1170 | 775 / 790 | 721 / 743 | 2.68 | 2.15 | 0.80 | 1.41 | 0.94 | 1.32 | 1.63 |
| mix | PSS peak | 579 / 587 | 1576 / 1466 | 1249 / 1200 | 817 / 830 | 754 / 774 | 2.61 | 2.10 | 0.81 | 1.41 | 0.93 | 1.31 | 1.60 |
| mix | working set peak | 686 / 674 | 1668 / 1653 | 1471 / 1445 | 879 / 886 | 792 / 795 | 2.44 | 2.14 | 0.88 | 1.30 | 0.90 | 1.17 | 1.84 |
| mix | cgroup memory.current peak | 705 / 694 | 1668 / 1653 | 1472 / 1445 | 900 / 913 | 812 / 814 | 2.37 | 2.09 | 0.88 | 1.30 | 0.90 | 1.16 | 1.79 |
| mix | cgroup memory.peak | 720 / 724 | 1685 / 1710 | 1531 / 1470 | 1076 / 1101 | 923 / 944 | 2.35 | 2.08 | 0.88 | 1.51 | 0.86 | 1.29 | 1.61 |
| mix | threads | 669 / 669 | 46 / 46 | 45 / 46 | 279 / 279 | 279 / 279 |  |  |  |  |  |  |  |

| run | seconds | MiB/s | requests/s | paused, s | errors |
|---|---|---|---|---|---|
| R | 618.3 / 701.7 | 1117 / 984 | 48.5 / 42.8 | 150 / 164 | 0 / 0 |
| M | 700.0 / 614.0 | 986 / 1125 | 42.9 / 48.9 | 148 / 142 | 0 / 0 |
| F | 643.6 / 701.3 | 1073 / 984 | 46.6 / 42.8 | 144 / 151 | 0 / 0 |
| RM | 616.4 / 673.0 | 1120 / 1026 | 48.7 / 44.6 | 150 / 149 | 0 / 0 |
| RMH | 616.5 / 642.3 | 1120 / 1075 | 48.7 / 46.7 | 147 / 146 | 0 / 0 |

rgw-go's Go heap: HeapInuse peak M 1325 / 1277, F 1030 / 997 (F/M 0.78); F's free list peak 428 / 412.

The five gateways moved the same requests at the same rates within the run-to-run spread
(614-702 s): the one OSD, and the pauses the garbage collector forces on it, bound the mix, not
the gateway. Both rgw-go builds stayed inside their 2 GiB scope: at its cgroup peak M had 338-363
MiB to spare, F 517-578.

RM peaks at 833 / 844 MiB (1.41x R) and RMH at 779 / 797 (1.33x R, 0.94x of RM); in steady state 824 / 833
and 703 / 706 (0.85x of RM). Against RMH, F's peak is 1.56x and M's 1.9-2.0x; the cgroup `memory.peak` of RM
and RMH, 1 076-1 101 and 923-944 MiB, is 110-190 MiB above their sampled `memory.current` peaks, as in the large
GETs.

### 3.5 CPU by phase

The gateway cgroup's CPU over each phase (`out/cpu.tsv`; R, RM and RMH are the rgw container's, M and F the
user scope's), seconds, rep 1 / rep 2:

| phase | R | M | F | RM | RMH | RMH/RM | F/RMH |
|---|---|---|---|---|---|---|---|
| 32 x 64 MiB PUTs | 8.4 / 8.6 | 7.2 / 7.2 | 8.2 / 7.2 | 7.9 / 7.8 | 7.8 / 7.7 | 0.99 | 0.99 |
| 32 x 64 MiB GETs | 14.3 / 15.1 | 14.0 / 13.6 | 15.0 / 13.7 | 14.4 / 14.0 | 15.2 / 15.2 | 1.07 | 0.94 |
| 256 MiB GETs at 2 | 2.7 / 2.5 | 2.7 / 2.5 | 2.4 / 2.6 | 2.7 / 2.8 | 2.9 / 2.8 | 1.04 | 0.88 |
| 64 x 4 KiB PUTs | 27.5 / 27.4 | 19.9 / 19.3 | 20.9 / 19.6 | 19.3 / 19.4 | 19.7 / 19.9 | 1.02 | 1.02 |
| 64 x 4 KiB GETs | 856.8 / 874.7 | 257.0 / 256.3 | 260.6 / 254.2 | 648.4 / 652.9 | 653.1 / 652.8 | 1.00 | 0.39 |
| the mix | 1070.7 / 931.0 | 751.9 / 762.9 | 757.7 / 754.0 | 871.0 / 879.9 | 905.5 / 892.8 | 1.03 | 0.84 |
| idle after the large load (600 s) | - | - | - | 2.5 | 2.6 | 1.04 | - |
| idle after the mix (600 s) | - | - | - | 2.6 / 2.6 | 2.6 / 2.6 | 1.00 | - |

The idle rows (RM and RMH only, from 21:20, so the first large run of each has none) are the CPU over
the 600 s of idle, with the 5 snapshots in it: 2.5-2.6 s, about 0.4% of one core, with or without the
release. Under load RMH's CPU was within -1% and +7% of RM's (the 32-way GETs +7%, the mix +3%). The 4 KiB GETs cost
radosgw 650-875 s of CPU against rgw-go's 254-261 s (F is 0.39 of RMH).

## 4. Idle curves

### 4.1 After the large load

VmRSS, MiB, at seconds after the last GET, rep 1 / rep 2. Ratios of the reps' means.

| s | R | M | F | RM | RMH | M/R | F/R | F/M | RM/R | RMH/RM | RMH/R | F/RMH |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| 30 | 696 / 642 | 1696 / 1662 | 1513 / 1563 | 932 / 897 | 434 / 408 | 2.51 | 2.30 | 0.92 | 1.37 | 0.46 | 0.63 | 3.65 |
| 120 | 698 / 644 | 1001 / 1032 | 1227 / 978 | 934 / 899 | 436 / 410 | 1.51 | 1.64 | 1.08 | 1.37 | 0.46 | 0.63 | 2.61 |
| 240 | 698 / 644 | 627 / 643 | 200 / 202 | 934 / 899 | 436 / 410 | 0.95 | 0.30 | 0.32 | 1.37 | 0.46 | 0.63 | 0.48 |
| 330 | 698 / 644 | 174 / 166 | 204 / 210 | 934 / 899 | 436 / 410 | 0.25 | 0.31 | 1.22 | 1.37 | 0.46 | 0.63 | 0.49 |
| 600 | 698 / 644 | 172 / 168 | 212 / 210 | 934 / 899 | 436 / 410 | 0.25 | 0.31 | 1.24 | 1.37 | 0.46 | 0.63 | 0.50 |

rgw-go's Go heap in rep 1, HeapInuse / HeapReleased / free list, MiB:

| s | M | F |
|---|---|---|
| 30 | 867 / 22 / - | 1069 / 9 / 640 |
| 120 | 555 / 717 / - | 666 / 299 / 640 |
| 240 | 16 / 1092 / - | 32 / 1321 / 16 |
| 330 | 13 / 1543 / - | 39 / 1320 / 16 |
| 600 | 11 / 1544 / - | 49 / 1315 / 16 |

- **radosgw keeps its high-water mark.** tcmalloc holds what was freed: its sbrk heap stays at 629
  MiB for the whole 600 s while Ceph's mempools hold 0.3 MiB (`dump_mempools` at the end).
- **M** drains over the runtime's forced GCs, which come ~120 s apart when nothing else triggers
  one: the first takes the heap in use from ~870 to ~560 MiB, the second (by 240 s) to 16, and the
  scavenger returns the freed heap over the following minute; from 330 s on M is at 166-174.
- **F** keeps its 640 MiB of idle blocks (160) until the list's trim, which fired at +176 s in rep
  1 (178 s after the last GET); the trim leaves the 4-block floor (16 MiB) and calls FreeOSMemory, so
  F falls from ~1230 to ~190 at once. A forced GC before the trim (+110 s) already returns the
  heap's garbage, so at 120 s F is 1.08x M on the mean (1.22x in rep 1). From the trim until M's
  second forced GC and scavenger F is a third of M (240 s); after that F sits 30-45 MiB above M: the
  16 MiB floor and a live heap of ~25 MB (gctrace "49->49->25 MB") that the sampler's /metrics
  scrapes keep filling.
- **RM keeps its high-water mark, as R does:** 932 / 897 MiB at +30 s, 934 / 899 at +600 s. tcmalloc's
  `heap stats` shows why: its page heap holds 521 / 489 MiB free from the end of the GETs, and it has returned
  20 / 21 MiB to the OS; the 309 MiB the application holds are in use throughout (section 5b).
- **RMH gives the free memory back within 30 s of the end of the load:** 434 / 408 MiB at +30 s and 436 / 410 at
  +600 s, 0.46 of RM's; the page heap holds 24 / 0 MiB free and 524 / 543 MiB have been released. The floor it
  stops at is the 309 MiB in use by the application and the binary, libraries and stacks. That is 0.63 of R's
  high-water mark, and still twice F's 212 / 210.

### 4.2 After the mix

VmRSS, MiB, rep 1 / rep 2. Each rep is shown: the idle points fall before or after the forced GCs
and F's trim depending on when the last requests finished, so a mean blurs two different curves.

| s | R | M | F | RM | RMH | M/R | F/R | F/M | RM/R | RMH/RM | RMH/R | F/RMH |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| 30 | 586 / 594 | 1326 / 1329 | 1218 / 584 | 826 / 830 | 437 / 444 | 2.25 | 1.53 | 0.68 | 1.40 | 0.53 | 0.75 | 2.05 |
| 120 | 586 / 594 | 763 / 1264 | 604 / 604 | 826 / 830 | 437 / 444 | 1.72 | 1.02 | 0.60 | 1.40 | 0.53 | 0.75 | 1.37 |
| 240 | 586 / 594 | 343 / 446 | 225 / 242 | 826 / 830 | 437 / 444 | 0.67 | 0.40 | 0.59 | 1.40 | 0.53 | 0.75 | 0.53 |
| 330 | 587 / 595 | 204 / 202 | 237 / 245 | 827 / 830 | 437 / 444 | 0.34 | 0.41 | 1.19 | 1.40 | 0.53 | 0.75 | 0.55 |
| 600 | 587 / 595 | 207 / 204 | 243 / 249 | 827 / 830 | 437 / 444 | 0.35 | 0.42 | 1.20 | 1.40 | 0.53 | 0.75 | 0.56 |

- radosgw stays at its high-water mark again (586-595).
- F's free list held 368-412 MiB at the mix's end; the first trim at ~+75 s dropped 32 MiB of it in
  rep 1 and the full trim at ~+195 s took it to the 16 MiB floor. F's 584 at 30 s in rep 2 is a GC
  that fell just before that point (HeapReleased 639 MiB there, 43 in rep 1).
- From 240 s both rgw-go builds are a third to two fifths of radosgw; F sits 35-45 MiB above M from
  330 s on, as after the large load.
- RM is flat at 826 / 830 MiB for the 600 s (page heap 405 / 416 MiB free); RMH is at 437 / 444 from the first
  point, 0.53 of RM's and 0.75 of R's 587 / 595. F, at 243 / 249 from 330 s, is 0.56 of RMH.

## 5. Breakdown

Rss by mapping from the smaps snapshots, MiB ([out/breakdown.md](out/breakdown.md), `rwtool analyze
-breakdown`): Go's heap arenas; the sbrk heap, which is tcmalloc's in both gateways (gperftools
2.9.1 takes system memory from sbrk first); other anonymous memory; thread stacks; file-backed
mappings (binary and libraries).

**Rep 1:**

| snapshot | gateway | Go heap | tcmalloc heap | other anon | thread stacks | files | total |
|---|---|---|---|---|---|---|---|
| fresh, idle 15 s | R | 0 | 49 | 2 | 21 | 41 | 114 |
| | M / F | 11 / 11 | 7 / 7 | 4 / 4 | 0 / 0 | 36 / 36 | 60 / 59 |
| | RM / RMH | 0 / 0 | 302 / 302 | 2 / 2 | 8 / 8 | 69 / 68 | 381 / 381 |
| after 32 x 64 MiB PUT | R | 0 | 423 | 2 | 21 | 42 | 489 |
| | M / F | 803 / 784 | 103 / 103 | 7 / 7 | 0 / 0 | 37 / 37 | 951 / 932 |
| | RM / RMH | 0 / 0 | 670 / 468 | 2 / 2 | 8 / 8 | 68 / 69 | 748 / 548 |
| during 32 x 64 MiB GETs | R | 0 | 639 | 3 | 22 | 42 | 707 |
| | M / F | 1540 / 1355 | 106 / 102 | 9 / 9 | 0 / 0 | 38 / 37 | 1693 / 1505 |
| | RM / RMH | 0 / 0 | 866 / 747 | 3 / 3 | 8 / 8 | 68 / 69 | 946 / 828 |
| 600 s after them | R | 0 | 629 | 2 | 25 | 42 | 698 |
| | M / F | 17 / 59 | 107 / 103 | 10 / 12 | 0 / 0 | 38 / 37 | 172 / 212 |
| | RM / RMH | 0 / 0 | 853 / 354 | 2 / 2 | 10 / 10 | 68 / 69 | 934 / 436 |
| during 2 x 256 MiB GETs | R | 0 | 187 | 2 | 22 | 41 | 252 |
| | M / F | 176 / 88 | 49 / 49 | 6 / 6 | 0 / 0 | 38 / 38 | 269 / 181 |
| | RM / RMH | 0 / 0 | 436 / 354 | 2 / 2 | 8 / 8 | 68 / 68 | 514 / 433 |
| during 64 x 4 KiB GETs | R | 0 | 144 | 4 | 23 | 41 | 213 |
| | M / F | 599 / 233 | 46 / 48 | 20 / 17 | 0 / 0 | 38 / 38 | 704 / 337 |
| | RM / RMH | 0 / 0 | 357 / 356 | 4 / 4 | 8 / 8 | 68 / 68 | 437 / 437 |

- **rgw-go's excess is all Go heap.** Its C heap (librados's buffers in tcmalloc) is 46-107 MiB,
  a sixth of radosgw's 144-639 MiB, and it has 33-68 threads to radosgw's 605-669, whose stacks
  hold 21-25 MiB. What makes rgw-go larger under load is the Go heap: up to 1.5 GiB during 32
  concurrent 64 MiB GETs, against a memory budget of 819 MiB.
- tcmalloc's own statistics at the end of the large runs (gdb, `snaps/end.tcmalloc_stats.txt` in each run's scratch directory):
  rgw-go's 105-109 MiB C heap is 5 MiB in use and ~90 MiB in tcmalloc's page-heap freelist, in M
  and F alike. radosgw's 629 MiB sbrk heap at the same point holds 0.3 MiB of Ceph mempool
  bytes (`dump_mempools`): almost all of it is freed memory tcmalloc keeps.
- **Where the Go heap goes above the budget.** The budget (819 MiB) bounds the buffers a request
  holds (`budget held` peaked at 640-768 MiB). HeapInuse reached 1452 (M) and 1351 MiB (F) during
  the large GETs: the rest is garbage between collections and, for M, buffers the `sync.Pool`s hold
  beyond what is in flight. GOGC 100 lets the heap reach twice the live heap until the 1638 MiB Go
  limit binds.
- **A fresh radosgw from main starts at 381 MiB, not 114.** Its sbrk heap is 302-304 MiB at the start of every
  run, and its files 63-69 MiB (21 of them `librgw_lancedb.so.1`, which main's radosgw maps); tcmalloc's
  `heap stats` puts 308-311 MiB "in use by the application" at every snapshot of every run, before and
  after the load, while Ceph's mempools hold 0.25 MiB. What holds the 308 MiB was not found. Under load the
  sbrk heap grows with tcmalloc's page-heap freelist (853 MiB after the large GETs on RM, 521 of them free);
  with the release on (RMH) it ends at 354 MiB. Its 215 to 279 threads hold 8-10 MiB of stacks, against
  R's 21-25 for 605-669.

## 5a. Follow-up: where the Go heap goes

Asked after the campaign started: why does rgw-go's Go heap reach 1.33-1.45 GiB in the 32 x 64 MiB
GETs, against a live buffer set of ~640 MiB (32 x a 16 MiB window plus a 4 MiB head)? Theories:
(a) GC headroom near 2x live, which a lower limit or GOGC shrinks; (b) garbage from allocations
that bypass the list, which removing the head copy shrinks; (c) neither.

**From the main campaign's data** ([out/gc.md](out/gc.md)). The live heap was not sampled per second
(`/gc/heap/live:bytes` is not among the Go collector's /metrics series); gctrace's figures stand in
for it: each cycle's heap at its start, the live heap it marked, and the goal it set. Cycles are
assigned to a phase by the GC count in the samples before and after the phase. GC CPU is the sum of
gctrace's five CPU figures per cycle (its stop-the-world parts are charged at all 32 Ps, so it
overstates where cycles are frequent).

| phase | run | GC cycles | GC CPU s | phase CPU s | live after GC, median / max MB | heap at GC start, max MB | goal, max MB | HeapInuse peak MiB |
|---|---|---|---|---|---|---|---|---|
| 32 x 64 MiB GET | M-1 / M-2 | 5 / 5 | 0.1 / 0.0 | 14.0 / 13.6 | 831 / 1337, 780 / 974 | 1534, 1507 | 1565, 1563 | 1452, 1453 |
| 32 x 64 MiB GET | F-1 / F-2 | 3 / 4 | 0.1 / 0.0 | 15.0 / 13.7 | 688 / 779, 710 / 719 | 1350, 1395 | 1376, 1421 | 1351, 1329 |
| 64 x 4 KiB GET | M-1 / M-2 | 221 / 220 | 3.1 / 2.9 | 257 / 256 | 285 / 306, 285 / 307 | 602, 611 | 614, 615 | 574, 556 |
| 64 x 4 KiB GET | F-1 / F-2 | 260 / 260 | 3.5 / 3.3 | 261 / 254 | 209 / 214, 209 / 213 | 425, 423 | 429, 427 | 414, 410 |
| 64 x 4 KiB PUT | F-1 / F-2 | 1205 / 1176 | 12.7 / 11.0 | 20.9 / 19.6 | 4 / 6 | 11 | 13 | 12, 13 |

- F's goal is twice its live heap in both GET loads (1376-1421 MB over 688-719 live; 427-429 over
  209-213): GOGC 100, the Go limit (1638 MiB) not binding. M's large-GET goal (1563-1565 MB) is
  below twice its live heap (780-831): the limit set it.
- So the heap in use is the live heap plus the garbage GOGC lets accumulate until the next cycle.
  F's live heap in the large GETs, ~700 MB, is the free list's blocks (in flight and idle) plus 32
  head copies of 4 MiB (`slices.Clone` in statHead) that live as long as their requests. The
  garbage rate is low: 3-4 cycles in ~57 s, ~40 MB/s, about what 512 head copies of 4 MiB in 57 s
  make (38 MiB/s).
- GC CPU is negligible in the large GETs (0.1 s of 14-15 s) and ~1.3% of the 4 KiB GETs' CPU.

**The follow-up runs.** Five variants of F, loads 1 and 3 only, two reps each, run after the main
campaign in the order of `fu-plan.txt` (rotated), with the same sampler, client and rules. Their
Go limit and budget come from the scope as before.

| id | what |
|---|---|
| F1G | F in a user scope with `MemoryMax=1073741824` (1 GiB), the radosgw container's limit halved. The Go memory limit follows it to 819 MiB (858 993 459 B) and the memory budget to 410 MiB (429 496 729 B), each run's log. It lowers the budget with the limit, so it is not a pure GC test. |
| FG50 | F at 2 GiB with `GOGC=50` in the process's environment (`start.txt`). Limit and budget as F's. |
| FNC | THROWAWAY, `patches/nocopy.patch`: no head copy. The head of a whole GET is the free-list block the prefetch read into, not `slices.Clone` of it (`internal/driver/stat.go:322` at 296ad44; `:309` at 01ebd5a). The block goes back to the list once, when the request's context ends or its `HeadRelease` runs. |
| FMM | THROWAWAY, `patches/mmap.patch`: the free list takes each `rgw_max_chunk_size` block from `syscall.Mmap` (anonymous, private) instead of the Go heap, and `munmap`s it where the list drops one; no `FreeOSMemory` for those, a forgotten block leaks, blocks of other sizes stay on the heap. It adds the counters `rgw_go_memory_budget_free_list_{mmap,munmap}_bytes_total`. |
| FMMNC | THROWAWAY, `patches/mmap-nocopy.patch`: FMM and FNC together. |

Each patch applies to 296ad44 with `patch -p1` and reproduces the files its binary was built from
(`out/builds.txt` has the binaries' hashes). FNC, FMM and FMMNC were read back after each run: every
object of the load fetched four times and its MD5 compared with its ETag (`verify.json`), 128 reads
per large run and 102 400 per small run, 0 mismatches and 0 failures in all 12 runs. The driver
specs for the files each patch changes (151 of 2 771, `out/specs.txt`): FMM passes, with and without
the specs' own stores mapping their blocks; FNC and FMMNC fail the same 2 (`ReadObject` stopping on a
sink that fails mid-stream, which expects every lent byte back when `ReadObject` returns). That is the
head change's intended lifetime, not a fault in mmap: the head's block is lent until the request's
context ends. The readback found no corruption; the variants were not run under the race detector
against a live cluster.

Every figure below is rep 1 / rep 2 of the phase named, MiB unless marked; GC cycles and CPU are the
phase's from gctrace (its CPU figure charges stop-the-world parts at all 32 Ps, so it overstates where
cycles are frequent; the phase CPU is the cgroup's). The last two columns are the ratio of the reps'
means to F's. Per-run tables with every column are `out/followup.md`, `out/gc-followup.md` and
`out/tables-followup.md`.

**32 x 64 MiB GETs (16 waves, 32 GiB):**

| variant | VmRSS peak | HeapInuse peak | live median, MB | goal max, MB | GC cycles | GC CPU s | phase CPU s | MiB/s | VmRSS / F | MiB/s / F |
|---|---|---|---|---|---|---|---|---|---|---|
| R | 709 / 654 | - | - | - | - | - | - | 582 / 562 | 0.44 | 1.02 |
| F | 1513 / 1563 | 1351 / 1329 | 688 / 710 | 1376 / 1421 | 3 / 4 | 0.1 / 0.0 | 15.0 / 13.7 | 577 / 544 | 1.00 | 1.00 |
| F1G | 925 / 917 | 711 / 689 | 436 / 404 | 769 / 768 | 6 / 5 | 0.1 / 0.1 | 14.5 / 13.3 | 619 / 605 | 0.60 | 1.09 |
| FG50 | 1263 / 1265 | 1058 / 1057 | 694 / 696 | 1097 / 1131 | 7 / 6 | 0.1 / 0.1 | 13.6 / 13.4 | 512 / 564 | 0.82 | 0.96 |
| FNC | 1008 / 1010 | 865 / 867 | 0 / 0 | 0 / 0 | 0 / 0 | 0.0 / 0.0 | 13.1 / 14.4 | 554 / 558 | 0.66 | 0.99 |
| FMM | 1053 / 1034 | 158 / 234 | 62 / 61 | 260 / 246 | 36 / 37 | 0.4 / 0.3 | 14.8 / 15.1 | 554 / 561 | 0.68 | 0.99 |
| FMMNC | 1010 / 1006 | 223 / 223 | 0 / 0 | 0 / 0 | 0 / 0 | 0.0 / 0.0 | 13.0 / 13.2 | 566 / 550 | 0.66 | 1.00 |

**64 x 4 KiB GETs (40 waves, 1 024 000 GETs):**

| variant | VmRSS peak | HeapInuse peak | live median, MB | goal max, MB | GC cycles | GC CPU s | phase CPU s | GETs/s | VmRSS / F | GETs/s / F |
|---|---|---|---|---|---|---|---|---|---|---|
| R | 214 / 211 | - | - | - | - | - | - | 16658 / 16350 | 0.63 | 0.57 |
| F | 337 / 337 | 414 / 410 | 209 / 209 | 429 / 427 | 260 / 260 | 3.5 / 3.3 | 260.6 / 254.2 | 29166 / 28460 | 1.00 | 1.00 |
| F1G | 227 / 231 | 208 / 209 | 105 / 105 | 219 / 221 | 533 / 531 | 6.2 / 6.3 | 269.2 / 267.7 | 26598 / 27160 | 0.68 | 0.93 |
| FG50 | 226 / 225 | 311 / 312 | 208 / 208 | 326 / 321 | 537 / 538 | 6.3 / 6.1 | 254.7 / 257.1 | 28744 / 30340 | 0.67 | 1.03 |
| FNC | 383 / 393 | 493 / 504 | 249 / 249 | 507 / 518 | 204 / 203 | 2.8 / 2.8 | 253.1 / 252.8 | 28772 / 30459 | 1.15 | 1.03 |
| FMM | 115 / 114 | 14 / 15 | 4 / 4 | 15 / 15 | 38503 / 38889 | 413.1 / 411.7 | 587.6 / 587.1 | 15254 / 15672 | 0.34 | 0.54 |
| FMMNC | 115 / 114 | 13 / 12 | 4 / 4 | 15 / 13 | 37002 / 37304 | 421.6 / 399.1 | 575.3 / 574.4 | 16574 / 15983 | 0.34 | 0.56 |

**64 x 4 KiB PUTs (25 600):**

| variant | VmRSS peak | HeapInuse peak | live median, MB | goal max, MB | GC cycles | GC CPU s | phase CPU s | PUTs/s | VmRSS / F | PUTs/s / F |
|---|---|---|---|---|---|---|---|---|---|---|
| R | 200 / 196 | - | - | - | - | - | - | 1605 / 1721 | 2.11 | 1.06 |
| F | 94 / 94 | 12 / 13 | 4 / 4 | 13 / 13 | 1205 / 1176 | 12.7 / 11.0 | 20.9 / 19.6 | 1607 / 1532 | 1.00 | 1.00 |
| F1G | 92 / 92 | 12 / 13 | 4 / 4 | 12 / 12 | 1164 / 1163 | 10.6 / 10.6 | 18.8 / 19.1 | 1564 / 1694 | 0.98 | 1.04 |
| FG50 | 88 / 89 | 9 / 10 | 3 / 3 | 8 / 8 | 3556 / 3517 | 27.1 / 26.7 | 34.1 / 34.6 | 1444 / 1489 | 0.94 | 0.93 |
| FNC | 93 / 94 | 12 / 13 | 4 / 4 | 13 / 12 | 1165 / 1158 | 10.7 / 10.4 | 19.4 / 19.2 | 1534 / 1722 | 0.99 | 1.04 |
| FMM | 94 / 93 | 13 / 13 | 4 / 4 | 13 / 12 | 1146 / 1168 | 10.9 / 10.7 | 19.1 / 19.1 | 1743 / 1578 | 0.99 | 1.06 |
| FMMNC | 94 / 94 | 13 / 13 | 4 / 4 | 13 / 12 | 1163 / 1179 | 10.9 / 11.2 | 19.4 / 19.4 | 1572 / 1726 | 1.00 | 1.05 |

In the 32 x 64 MiB PUT phase VmRSS is 930-934 MiB for every variant but F1G (574); FMM and FMMNC
hold 141-143 MiB of Go heap in it (F: 780); cycles 5-16; throughput 482-553 MiB/s against F's
532-538 (`out/followup.md`).

### Verdicts

**(a) GC headroom: confirmed.** FG50 changes nothing but the GC target, and it moves the heap.
In the large GETs the live heap is F's (694 / 696 MB against 688 / 710) and the goal falls from about
twice the live heap to about 1.5 times (1 097 / 1 131 MB against 1 376 / 1 421), so HeapInuse falls
1 058 / 1 057 against 1 351 / 1 329 (-22%) and VmRSS 1 263 / 1 265 against 1 513 / 1 563 (-18%). In
the 4 KiB GETs VmRSS falls a third (226 / 225 against 337 / 337). The cost is more cycles, not
throughput or visible CPU in the GETs: 6-7 cycles against 3-4 in the large GETs at 0.1 s of GC CPU in
both, and 537 / 538 against 260 in the 4 KiB GETs (6.1-6.3 s of GC CPU against 3.3-3.5 s, a phase
CPU of 255 / 257 s against 261 / 254 s), at 512 / 564 MiB/s against 577 / 544 and 28 744 / 30 340
GETs/s against 29 166 / 28 460. It does cost the 4 KiB PUTs: 3 556 / 3 517 cycles against 1 205 /
1 176, 26.7-27.1 s of GC CPU against 11.0-12.7 s, a phase CPU of 34.1 / 34.6 s against 20.9 / 19.6 s
(+69%) and 1 444 / 1 489 PUTs/s against 1 607 / 1 532 (-7%, the two lowest of the fourteen PUT phases,
but 4.8% apart in F's own two). F1G gets there by the limit: its goal sits at the limit (769 / 768 MB),
VmRSS is 925 / 917 (-40%) and the cgroup's `memory.peak` 906 / 926 MiB of 1 024. But its live heap
falls too (436 / 404 MB against 688 / 710), because the budget halved with the limit and the free
list peaked at 336 MiB instead of 640. By the medians' arithmetic, of the 640 MiB HeapInuse fell,
about 280 is the smaller live heap and about 360 the smaller headroom. Its large-GET throughput is
not lower (619 / 605 MiB/s against 577 / 544); its 4 KiB GETs are 7% slower in both reps (26 598 /
27 160 against 29 166 / 28 460) with the free list capped at 100 MiB instead of 204 and twice F's
cycles (533 / 531) for 2.8 s more GC CPU; the smaller budget, not the GC, is the likelier cause, but
the runs do not separate them.

**(b) Garbage from the head copy: confirmed for the large GETs, with a cost in the small ones.** FNC
ran no GC cycle at all in either large GET phase (F: 3-4): the 4 MiB copies, 512 of them, were what
the collector was collecting. HeapInuse is 865 / 867 against 1 351 / 1 329 (-36%) and VmRSS 1 008 /
1 010 against 1 513 / 1 563 (-34%), at 554 / 558 MiB/s against 577 / 544. With no cycle the heap
holds whatever the PUT and the first waves left, so 865 is an upper bound, not a floor. In the 4 KiB
GETs it goes the other way: VmRSS 383 / 393 against 337 / 337 (+15%), free list peak 244 / 248 MiB
against 204, because the head's block is lent to the request's end instead of being given back at
once, so more blocks are out at once than there were 4 KiB copies; cycles fall to 204 / 203 and GC
CPU to 2.8 s, and GETs/s are F's (28 773 / 30 459). The patch breaks 2 specs by design (above).

**(c) Neither: not supported.** Each theory accounts for a part. Of F's ~1 340 MiB HeapInuse in the
large GETs, GOGC 50 takes ~290, removing the head copy ~480 and the 1 GiB scope ~640 (with a smaller
live set); the follow-up did not combine the first two.

**Off-heap blocks (FMM, FMMNC): the Go heap shrinks to 12-18% of F's, the process does not.** In the
large GETs HeapInuse is 158 / 234 (FMM) and 223 / 223 (FMMNC) against 1 351 / 1 329. The list mapped
640 / 640 (FMM) and 644 / 640 MiB (FMMNC) and unmapped nothing in either run, so the cost of
mapping and unmapping blocks under churn was not exercised. VmRSS is 1 053 / 1 034 (FMM) and 1 010 /
1 006 (FMMNC) against 1 513 / 1 563: 0.66-0.68 of F, and the same as FNC's 1 008 / 1 010. Taking the
blocks off the Go heap moves where the 640 MiB are counted, not whether they are resident: the
requests touch them. Against radosgw's 709 / 654 FMMNC is 1.48x, so off-heap blocks do not bring
rgw-go near radosgw in the 32-way GETs; what is left is the budget's buffers in flight (640 MiB),
the C heap (~100), the Go heap (~220) and the binary (~36). FMM ran 36 / 37 cycles at 0.3-0.4 s of GC
CPU (of 14.8 / 15.1 s); FMMNC ran none. Throughput is F's (554 / 561 and 566 / 550 MiB/s).

In the 4 KiB GETs the picture is the reverse, and this is where the "GC ballast" matters. VmRSS
is 115 / 114 for both, 0.34 of F's and 0.54 of radosgw's (214 / 211), at **15 254 / 15 672 GETs/s
(FMM) and 16 574 / 15 983 (FMMNC), 0.54 and 0.56 of F's 29 166 / 28 460** and about radosgw's
16 658 / 16 350. The phase CPU is 587 / 587 s (FMM) and 575 / 574 s (FMMNC) against F's 261 / 254 s,
2.2-2.3 times, 0.56 ms of CPU per GET against 0.25 ms. The collector ran 38 503 / 38 889 (FMM) and
37 002 / 37 304 (FMMNC) cycles against 260, about 145 times as many, one every 1.7 ms, at 399-422 s
of gctrace CPU. The live heap is 4 MB and the goal 13-15 MB, where F's is 209 MB and 427-429 MB.
Reading that: F's idle blocks sit on the Go heap and count as live heap, so GOGC 100 lets the
request path's small garbage run for ~220 MB between cycles; with the blocks off the heap the same
garbage meets a 15 MB goal. The heap-resident blocks were acting as a GC ballast, and taking them
away costs about half the GET rate and more than double the CPU. That reading is the numbers' fit,
not a test: a run of FMM with a raised GOGC or a heap floor would be the test, and was not made. The
4 KiB PUTs are unchanged by mmap (VmRSS 93-94, 1 146-1 179 cycles, 1 572-1 743 PUTs/s).

## 5b. radosgw from Ceph main, and what heap release does

RM is the control (`rgw_heap_release_interval` 0, main's default), RMH has it at 1 (section 2). Both ran in
radosgw's Rook pod of `rgw-go-main`, 16 runs, two reps of each load. Every figure is rep 1 / rep 2.

**tcmalloc's own view**, MiB, from `heap stats` on the admin socket (`out/heapstats-main.tsv`; the first
column is the bytes in use by the application of RM ; RMH):

| snapshot | in use by the application, RM / RMH | page heap free, RM | page heap free, RMH | released to the OS, RM | released to the OS, RMH | actual memory used, RM | actual memory used, RMH |
|---|---|---|---|---|---|---|---|
| after the 32 x 64 MiB GETs | 309 / 309 ; 309 / 309 | 521 / 489 | 160 / 148 | 20 / 21 | 388 / 395 | 862 / 827 | 498 / 486 |
| large, idle +30 s | 309 / 309 ; 309 / 309 | 521 / 489 | 24 / 0 | 20 / 21 | 524 / 543 | 862 / 827 | 362 / 338 |
| large, idle +120 s | 309 / 309 ; 309 / 309 | 521 / 489 | 24 / 0 | 20 / 21 | 524 / 543 | 862 / 827 | 362 / 338 |
| large, idle +600 s | 309 / 309 ; 309 / 309 | 521 / 489 | 25 / 0 | 20 / 21 | 524 / 543 | 862 / 827 | 362 / 338 |
| 256 MiB loads, +15 s | 308 / 308 ; 308 / 308 | 120 / 88 | 0 / 24 | 2 / 2 | 246 / 94 | 448 / 416 | 326 / 350 |
| after the 64 x 4 KiB GETs | 311 / 311 ; 311 / 311 | 17 / 17 | 16 / 17 | 0 / 0 | 0 / 0 | 363 / 365 | 362 / 363 |
| small, +15 s | 311 / 311 ; 311 / 311 | 17 / 17 | 16 / 17 | 0 / 0 | 0 / 0 | 363 / 365 | 362 / 363 |
| after the mix | 309 / 309 ; 309 / 309 | 405 / 416 | 16 / 24 | 28 / 17 | 536 / 555 | 748 / 760 | 359 / 370 |
| mix, idle +30 s | 309 / 309 ; 309 / 309 | 405 / 416 | 16 / 24 | 28 / 17 | 536 / 555 | 748 / 760 | 360 / 370 |
| mix, idle +120 s | 309 / 309 ; 309 / 309 | 405 / 416 | 16 / 24 | 28 / 17 | 535 / 555 | 748 / 760 | 360 / 370 |
| mix, idle +240 s | 309 / 309 ; 309 / 309 | 405 / 416 | 17 / 25 | 27 / 16 | 534 / 554 | 749 / 760 | 361 / 371 |
| mix, idle +600 s | 309 / 309 ; 309 / 309 | 405 / 416 | 17 / 25 | 26 / 16 | 534 / 554 | 750 / 761 | 361 / 371 |

- **At load, the release lowers the peak a little.** RMH's peak VmRSS is 0.93 of RM's in the 32 x 64 MiB
  GETs (880 / 862 against 950 / 917) and 0.94 in the mix (779 / 797 against 833 / 844); in the PUTs and in
  both small loads the two are equal; in the 256 MiB loads it does not lower the peaks and one rep is higher
  (section 3.2). At the end of the GETs RMH had already returned 388 / 395 MiB to the OS.
- **At idle, it gives the free memory back within the first 30 s and holds there.** After the large load the
  page heap's free memory goes from 521 / 489 MiB (RM) to 24 / 0 (RMH), after the mix from 405 / 416 to
  16 / 24; RSS goes from 932 / 897 to 434 / 408 and from 826 / 830 to 437 / 444 (sections 4.1 and 4.2).
  The pass makes no later difference: the series are flat to +600 s.
- **The release stops at the application's 309 MiB.** The figure "in use by the application" is 308-311 MiB at
  every snapshot of every run, the start included; what the release can return is what tcmalloc holds free
  above it. RMH therefore idles at 408-444 MiB, 0.46-0.53 of RM's and 0.63-0.75 of R's high-water mark, but
  3.6-3.9 times R's fresh 114 MiB.
- **With small objects there is nothing for it to do.** The 64 x 4 KiB loads leave 16-17 MiB free in the page
  heap, under the 64 MiB `rgw_heap_release_bytes`, so no pass runs: RM and RMH hold 433-438 MiB alike.
- **Its cost:** CPU within -1% and +7% of RM's in every loaded phase (+7% in the 32-way GETs, +3% in the mix,
  section 3.5), unchanged at idle (2.5-2.6 s of CPU in 600 s); the throughputs of the large, small and mix
  loads are RM's within the spread, and the 24 GETs of 256 MiB at 1 in flight were slower on RMH in both reps
  (section 3.2), which two reps do not tie to the release. Whether memory returned to the OS and faulted in
  again costs requests in a bursty load was not measured.

**Against R, and against rgw-go.** RM, the control, is larger than R in every load: 1.37-1.54x R's peak at the
large and mix loads, 2.0-2.8x with 256 MiB objects and 2.06-2.2x with 4 KiB, with a 381 MiB start where R's is
114; its 4 KiB GETs run at 1.3x R's rate on 0.75 of the CPU. Several differences are bundled in that (daemon
versions, OS and glibc, tcmalloc 2.15 against 2.9.1, the thread pool, main's code) and the data does not
separate them. The release changes the comparison with rgw-go at idle and barely at load:

| after the load | R | M | F | RM | RMH |
|---|---|---|---|---|---|
| large, +600 s | 698 / 644 | 172 / 168 | 212 / 210 | 934 / 899 | 436 / 410 |
| mix, +600 s | 587 / 595 | 207 / 204 | 243 / 249 | 827 / 830 | 437 / 444 |

At +600 s F is 0.23-0.30 of RM and 0.50-0.56 of RMH. At the loaded peaks it is the other way: F is 1.77x RMH's
in the 32 x 64 MiB GETs and 1.56x in the mix, and with small objects F's 94 MiB (PUTs) and 337 (GETs) are 0.22
and 0.77 of RMH's.

## 6. Recommendation

**PR #559, the free list: merge it as it is.** The in-process measurement that raised the question
found two things against the free list, about 30% more VmRSS at the peak and close to the whole
budget held until the 120 s trim. On the real cluster, with librados, neither makes it the larger
process:

- F's peak VmRSS is below M's in every load that uses the buffers, and equal to it elsewhere: F/M 0.98
  at 32 x 64 MiB PUTs, 0.91-0.92 at 32 x 64 MiB GETs, 0.67-0.79 at 256 MiB, 0.46 at 64 x 4 KiB GETs,
  1.00 at 64 x 4 KiB PUTs, 0.81 (peak) and 0.89 (steady) in the mix. A pod's limit must cover the
  peak, and F's is lower; its cgroup `memory.peak` in the mix was 1 470-1 531 MiB against M's 1 685-1 710.
- F does hold its blocks after a burst: 640 MiB (160 blocks) until the trim at +176 s after the large
  load, 368-412 MiB until +75 to +195 s after the mix. M holds its memory too, until the runtime's
  forced collections come, 120 s apart. After the large load F is 1.08x M at 120 s on the means (1.22
  and 0.95 in the two reps), then a third of M from the trim to M's second collection; after the mix it
  is below M at 120 s (604 / 604 against 763 / 1 264). From 330 s on F sits 30-45 MiB above M (the
  16 MiB floor and a ~25 MB live heap): 212 / 210 against 172 / 168 at 600 s after the large load.
- Throughput is M's within the run-to-run spread in every load (small GETs 28 460-29 166 against
  27 776-30 592 per s; mix 42.8-46.6 against 42.9-48.9 requests/s).

The costs are the 30-45 MiB idle floor over M and the hold window; this data gives no reason to
change the trim, and tested no other setting. What #559 does not do is close the gap to radosgw: F is
still 2.1-2.3x radosgw's peak in the large GETs and the mix.

**Follow-up directions, in the order the data supports:**

1. **Remove the head copy (FNC).** The best-evidenced lever for the 32-way large GETs: no GC cycle in the
   phase, HeapInuse 865 / 867 against 1 351 / 1 329 MiB, VmRSS 1 008 / 1 010 against 1 513 / 1 563
   (-34%), throughput unchanged. Costs: a 4 KiB GET's block is lent until the request ends, so the 64-way
   small GETs grow 15% (383 / 393 against 337 / 337) and the free list peaks 244 / 248 MiB against
   204; the lifetime of the head's block changes, so 2 driver specs that expect a failed read's lent
   bytes back at return fail and the contract needs a design, not a patch; and it touches the read
   path. Worth building as its own PR, with the small-GET growth bounded.
2. **A lower GOGC or limit.** Both are cheap to try and neither needs code. GOGC=50: -18% VmRSS in the
   large GETs, -33% in the 4 KiB GETs, no GET throughput change; it triples the GC cycles of the
   4 KiB PUTs (3 556 / 3 517 against 1 205 / 1 176), adds 70% to their phase CPU (34.1 / 34.6 s against
   20.9 / 19.6) and lowered their rate 7% (1 444 / 1 489 against 1 607 / 1 532 per s, two reps), so it
   is not a default to ship. A 1 GiB scope (F1G): -40% and -32%, but the budget halves with the
   limit (410 MiB), the 4 KiB GETs ran 7% slower (26 598 / 27 160 against 29 166 / 28 460 per s), and
   the cgroup peaked at 906 / 926 of 1 024 MiB: it describes a smaller pod, not a free saving. A knob
   for a deployment that wants a smaller footprint, with those costs documented.
3. **mmap, only with a GC-ballast answer.** On its own it does nothing for the large GETs beyond what
   removing the head copy does (VmRSS 1 006-1 053 against FNC's 1 008 / 1 010; 1.48x radosgw's). In the
   64-way 4 KiB GETs it brings VmRSS to 114-115 MiB (0.34 of F's, 0.54 of radosgw's) and costs 0.54-0.56
   of F's GETs/s, 2.2-2.3x the CPU and about 145x the GC cycles, because the idle blocks stopped
   propping up the GC target. It would be worth building only if a heap floor or a raised GOGC for the
   mapped build restores F's rate, which was not tried. The mapping and unmapping of blocks under churn
   was not exercised (no block was unmapped), and a forgotten block leaks.

**Heap release in radosgw (RMH) does not change the recommendation.** `rgw_heap_release_interval = 1` takes
radosgw's idle footprint from its high-water mark to 408-444 MiB (0.46-0.53 of what it kept) at -1% to +7% CPU
under load and none at idle, and lowers its large-GET and mix peaks by 6-7%. rgw-go is still ahead at idle (F is
0.50-0.56 of RMH, M 0.40-0.47) and behind at the large peaks (F is 1.77x RMH's in the 32 x 64 MiB GETs and 1.56x
in the mix); with small objects F is below RMH (94 against 436 MiB at 4 KiB PUTs, 337 against 438 at GETs). What
#559 earns by returning its blocks at the trim is something radosgw can have with an option, from main only; the
option does not narrow the loaded-peak gap that the follow-up directions above address. The data tests one
interval and the default threshold, and no other trim for F.

## 7. What was changed and put back

Two clusters were touched. `rgw-go-tentacle` carried the Tentacle campaign and the follow-up; it was put back
and is left up. `rgw-go-main` was made for RM and RMH and removed. `rgw-go-squid` was not touched. Tentacle's
start state: [setup/state-start.txt](setup/state-start.txt); its end state after the put-back:
[setup/state-end.txt](setup/state-end.txt); re-checked after the RM/RMH runs, which did not run on it:
[setup/verify-tentacle.log](setup/verify-tentacle.log). The main cluster's:
[setup-main/state-start.txt](setup-main/state-start.txt) and [setup-main/final-cleanup.log](setup-main/final-cleanup.log).

**Incident, 10:23-10:49.** The first mix smoke (M, before the guard existed) wrote ~6.6 GB of
overwrite garbage in 45 s, faster than the one gc pass beside it could collect, and filled the
10 GiB OSD: BlueStore aborted on ENOSPC at 17:23:55Z and osd.0 then crash-looped on BlueFS ENOSPC
during RocksDB recovery (1.1 MiB free). The cluster served nothing for ~25 minutes. It was
recovered in place, without losing data: four RocksDB/BlueFS overrides on osd.0 got its WAL
recovery through but not its MANIFEST write; then, with the Rook operator and the OSD deployment
scaled to 0, a pod of the OSD's spec added a 4 GiB file on the kind node's disk as a BlueFS DB
device (`ceph-bluestore-tool bluefs-bdev-new-db`), ran ceph-osd by hand, removed the smoke's
bucket and its 1539 tail objects, migrated BlueFS back to the main device (`bluefs-bdev-migrate`),
passed `fsck`, and restored the deployment and the operator. Every step is in
[setup/incident-notes.md](setup/incident-notes.md).

| change | why | put back |
|---|---|---|
| `rooket ceph-config --out` into the measuring worktree's gitignored `hack/rooket/out/tentacle`; `ceph auth get-or-create client.rgw.rgw-go` | rgw-go's ceph.conf and keyring | host files in the gitignored out directory, left; get-or-create leaves the existing entity as it is. No option set: the options were read with `ceph config get/show` |
| osd.0 filled and crash-looping (the incident) | the first mix smoke | recovered in place (above); 217 PGs active+clean, osd up, 0 restarts of the restored pod |
| `ceph config set osd.0` bluestore_rocksdb_options_annex, bluefs_max_log_runway, bluefs_min_log_runway | the in-place recovery attempts | `ceph config rm` each; `ceph config dump` identical to the start's ([setup/rescue/](setup/rescue/)) |
| rook-ceph-operator and rook-ceph-osd-0 scaled to 0; pod `rw-rescue-osd-0`; file `/var/lib/rook/rw-rescue-db.img` on the node, attached as BlueFS DB | the rescue | BlueFS migrated back, the DB device removed from the label (`show-label`: main device only), the file removed, the pod deleted, both deployments scaled back to 1 |
| osd.0 pod deleted 4 times; radosgw pod deleted before each R run | restarts | recreated by their ReplicaSets from unchanged deployments |
| the crash reports of osd.0: 22, 17:23:55Z to 17:43:55Z, all the incident's | the incident | `ceph crash archive-all` (no other plan was recorded; `final-cleanup.sh` says so); `crash ls-new` is empty and the "22 daemons have recently crashed" warning is gone, leaving the start's four |
| user `bench` (the bench harness's fixed keys), created by the setup | the S3 client's identity | `user rm --uid bench --purge-data` and a last `gc process --include-all`; `user list` is the start's eight users |
| buckets rw-gctest, rw-large, rw-vl1, rw-vl2, rw-small and rw-mix, and their objects | the loads | each run removed its objects and bucket through the gateway that served it, then ran `gc process --include-all` (`cleanup.log` in each run's directory); the aborted mixes were removed by `cleanup-abort.sh`; `bucket list` is the start's two buckets and the data pool its 18 objects |
| garbage: both gateways run with their collectors off | the mix's overwrites | `gc process --include-all` beside every mix and at the end: 0 entries |
| bench's usage log (`rgw_enable_usage_log` is on) | every request | `usage trim --uid=bench`; `usage show --uid=bench --start-date=2026-11-01` is empty again |
| osd.0's RocksDB, 1.6 GiB used after the loads | the mixes' churn and the incident | `ceph tell osd.0 compact`, which the campaign also ran before each mix: 111 MiB used, and every pool's object count equal to the start's |
| `rgw-go-main`: the kind cluster, its iSCSI target and backstore, a 10 GiB disk image, the registry on port 5003, Rook v1.20.7 with the Ceph main image, the bench user, buckets and data, `rgw_heap_release_interval` set with `ceph config set` | RM and RMH | `rooket down --delete-disks` once the campaign ended: target and backstore deleted, image and state directory removed; `rooket list` shows only squid and tentacle; no container or kind cluster of that name is left |
| the Ceph main image in the engine's storage; a copy of its layout on tmpfs | the image check | `podman rmi` of the image; the layout removed. The slow `podman pull`, killed, left nothing |
| a main radosgw against `rgw-go-tentacle` | the owner ruled it out | nothing was made: no entity, keyring or container |
| the five gateway binaries in `~/.cache/rgw-go-realworld`, and the sampler's host files | the runs | left on the host, outside the clusters and the repository; no rgw-go process, sampler, guard or `rw-*` systemd scope remains |

Tentacle after the put-back and again after the RM/RMH campaign: users, buckets, gc entries, usage and config
dump identical to the start's; every pool's object count as at the start; 217 PGs active+clean; osd.0 up; the
radosgw pod never restarted by the RM/RMH runs; health the start's four warnings.

## 8. Open questions

- **One small cluster.** One node, one 10 GiB OSD, one replica, the client on the same host. The large
  loads ran at 430-620 MiB/s and the mix at 43-49 requests/s whichever gateway served them, so the OSD
  and the garbage collection it needs, not the gateway, bound the throughput; a faster or wider
  backend lets more bytes be in flight and could move every peak, rgw-go's budgeted 640 MiB and
  radosgw's alike. The throughput comparisons say only that neither gateway was the limit here.
- **The in-process finding is not explained.** The fakerados burst found F 30% above the `sync.Pool`
  build at the peak; no run here shows it. This report did not look for why (librados's buffers, the
  pacing of real requests and the long phases are candidates, none tested).
- **The follow-up is two loads, two reps.** Loads 1 and 3 only; no mix, no idle curve beyond 30 s
  (F1G, FG50, FNC, FMM, FMMNC), no combination of the head change with GOGC, and no M. Its throughput
  differences of 7% (F1G and FMM on the 4 KiB GETs, FG50 on the 4 KiB PUTs) are two reps each, against
  a 3-6% spread between F's own.
- **F1G does two things.** It halves the Go limit and, through the ratio, the budget. A run with the
  limit lowered and the budget held (or the reverse) would separate the GC from the smaller number of
  buffers in flight; the throughput effect of the smaller budget on a faster backend is unknown.
- **FMM's own spread.** Its large-GET HeapInuse was 158 and 234 MiB and its steady VmRSS 937 and
  1 016 in the two reps; the cause was not looked for.
- **mmap under churn.** The list unmapped nothing in any run (munmap'd 0), so the cost of mapping,
  faulting in zeroed pages and unmapping blocks as the list sheds and trims was not exercised, nor the
  leak of a forgotten block. Whether a raised GOGC or a heap floor restores the 4 KiB GET rate of the
  mapped build was not tried.
- **FNC and FMMNC fail two specs by design.** Their head block is lent until the request's context
  ends. The readback found no corruption in the loads run, but no abandoned-request or disconnect
  load was run against them.
- **What else allocates.** Section 5a attributes the Go heap to the budget's buffers, the head copy
  and GC headroom; the heap profile was not used to account for the remainder.
- **gctrace's CPU.** It charges stop-the-world phases at all 32 Ps, so GC CPU is high where cycles
  are frequent (the 4 KiB PUTs, the mapped builds); the cgroup's phase CPU is the figure to trust.
- **The host was not quiet.** The 1-minute load at the starts of the main campaign's runs was 0.8-12.6,
  after the previous run's own 8-16 cores of small GETs and other sessions' work. Ranges were tight
  but for M's first large GET (478 MiB/s against 565).
- **Squid** was not measured; neither was a Tentacle radosgw outside its Rook pod, or with other
  tcmalloc settings than the image's.
- **Process.** The campaign's first mix smoke filled the OSD and cost about 25 minutes of recovery
  (section 7) because the guard that holds the mix back did not exist yet and the collector pass
  beside it never finished; the guard was then rewritten three times as the mix lengthened (the log
  is in `scripts/guard.sh`'s header). A `docs/porting-process.md` entry is for the controller to
  judge.
- **The 308 MiB of a fresh main radosgw.** RM and RMH hold 308-311 MiB "in use by the application" at every
  snapshot, from the first 30 s of a run, with 0.25 MiB in Ceph's mempools; `librgw_lancedb.so.1` is mapped
  (the S3 vector option `rgw_s3vector_backend` names LanceDB, [rgw.yaml.in:5297-5298](https://github.com/ceph/ceph/blob/d70755948de6884af4140268e388ccf95ec3657f/src/common/options/rgw.yaml.in#L5297-L5298)), but
  nothing here ties the allocation to it. It sets the floor under every small-object and idle figure of RM and
  RMH, so RMH's and RM's comparison with rgw-go at idle and with small objects is mostly this floor.
- **RM and RMH are one build on one cluster.** Daemons at 22.0.0-442 (a dev build, "vampire"), Rocky Linux
  10.2, gperftools 2.15, `rgw_thread_pool_size` 128: each differs from R's cluster, and R cannot be run on
  the same OS and tcmalloc. A pool of 512 would add threads and stacks (R's 605 threads hold 21-25 MiB, RM's
  215 hold 8-10); not measured. The numbers are for that build, not for a release.
- **One interval and the default threshold.** `rgw_heap_release_interval = 1` with `rgw_heap_release_bytes`
  at 64 MiB only. Other values were not run, nor the cost of memory that is returned to the OS and faulted in
  again by a bursty load (the 24 GETs of 256 MiB at 1 in flight ran slower on RMH in both reps, 455 / 344
  against 574 / 504 MiB/s, which two reps do not tie to the release).
- **`memory.peak` of RM and RMH** is 110-270 MiB above the highest `memory.current` the sampler saw (sections
  3.1 and 3.4); where that excess is was not found.
- **The idle CPU** was recorded only from the second large runs and the mixes (section 3.5).

## 9. Files in this directory

The summaries the tables are built from, the throwaway patches, the scripts and the tool's source.
What stayed in the run's scratch directory is each run's raw data (`runs/`, `runs-fu/` and
`runs-main/`, about 310 MB: the once-a-second `samples.jsonl`, the smaps snapshots, the gctrace
logs, the client's JSON results), the five gateway binaries and the Ceph main image.

- **[out/tables.md](out/tables.md)**, per load, phase and gateway (R, M, F, RM, RMH), the peaks, steady
  states, throughputs and idle curves of every run, in rep order, with the ratios of the means.
  [out/summary.tsv](out/summary.tsv) has one line per run and phase;
  [out/samples.tsv.gz](out/samples.tsv.gz) every run's once-a-second samples (VmRSS, RssAnon, RssFile,
  PSS, the cgroup's figures, the Go heap) with the phase each second falls in.
- **[out/breakdown.md](out/breakdown.md)**, smaps by mapping at each snapshot;
  **[out/gc.md](out/gc.md)**, the GC cycles, live heap and goal of each rgw-go phase from gctrace;
  **[out/cpu.tsv](out/cpu.tsv)**, the gateway cgroup's CPU over each phase;
  **[out/heapstats-main.tsv](out/heapstats-main.tsv)**, tcmalloc's own view of RM and RMH at every snapshot.
- **The follow-up (section 5a):** [out/followup.md](out/followup.md) (per run, every column),
  [out/gc-followup.md](out/gc-followup.md), [out/tables-followup.md](out/tables-followup.md),
  [out/breakdown-followup.md](out/breakdown-followup.md), [out/summary-followup.tsv](out/summary-followup.tsv),
  [out/samples-followup.tsv.gz](out/samples-followup.tsv.gz), [out/specs.txt](out/specs.txt) (the driver specs of
  the throwaway builds) and [out/builds.txt](out/builds.txt) (every binary's hash, `go version -m` and
  libraries).
- **[patches/](patches/)**: `nocopy.patch` (FNC), `mmap.patch` (FMM) and `mmap-nocopy.patch` (FMMNC).
  **Throwaway: never merge.** Each applies to 296ad44 with `patch -p1` and reproduces the files its binary
  was built from.
- **[setup/](setup/)** and **[setup-main/](setup-main/)**: each cluster's start and end state, the options
  compared, the radosgw's configuration; the smoke test and the image check are in [logs/](logs/).
  [setup/incident-notes.md](setup/incident-notes.md) is the record of the osd.0 incident and its recovery.
- **[scripts/](scripts/)**: what drove the runs: `env.sh` (settings every script sources), `gw-start.sh`,
  `run.sh`, `snap.sh`, `campaign.sh` and the plans (`plan.txt`, `fu-plan.txt`, `main-plan.txt`), the mix's
  `guard.sh` and `gcloop.sh`, the put-back scripts, the main cluster's bring-up, image fetch, image check and smoke test under
  `main-cluster/`, and `report-tables/`, which rendered this report's tables from `out/`. They carry the run's absolute paths (`/tmp/claude-1000/spike-memory/realworld`, a
  worktree of this repository) and `env.sh` is where to change them. Scripts changed during the run: the
  Tentacle campaign ran R, M and F with the first versions; `env.sh`, `gw-start.sh`, `snap.sh` and
  `run.sh` were then made to treat RM and RMH like R, and `run.sh` to record the idle's CPU (from 21:20 on 2026-11-04, so
  the first two RM and RMH runs have none); R, M and F behave as before. The committed versions are the last.
- **[_rwtool/](_rwtool/)**: the tool's source (sampler, mixed-load driver, readback check, analysis) with its
  specs. The `_` keeps it out of the module's `./...` builds, vet, lint and tests; it builds when named:
  `go build -o rwtool ./docs/benchmarks/2026-11-04-realworld-memory-tentacle/_rwtool`, with the module's
  aws-sdk-go-v2. It is the source as it ran but for gofumpt's layout, which this repository's format check
  requires, and the change that makes `analyze` treat RM and RMH as radosgws; its package comment's "never
  committed" describes it as it ran. The load client for the first three loads was the binary `s3load` of the allocator
  measurement (sha256 `0ab74154...cc84`); its source worktree is gone, and the source the allocator report keeps,
  [../2026-11-03-allocator-tentacle/_s3load/main.go](../2026-11-03-allocator-tentacle/_s3load/main.go), is the
  binary's by that report's account, which was not re-checked here.
