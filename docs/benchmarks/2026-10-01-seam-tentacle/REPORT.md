# Seam microbenchmark: tentacle

| env.json | |
|---|---|
| bench_time | 20s |
| ceph_version | 20.2.4 |
| conc | 1,16,64,256,512 |
| cpu | AMD Ryzen 9 7950X3D 16-Core Processor |
| date | 2026-10-01T23:02:38Z |
| floor_seconds | 20 |
| git_describe | fcd09fb |
| go_version | go version go1.27.1 linux/amd64 |
| gomaxprocs | unset |
| host_librados | 19.2.6-1.fc43 |
| image | quay.io/ceph/ceph:v20.2.4 |
| kernel | 7.2.6-100.fc43.x86_64 |
| large_conc | 1,4,16 |
| load | 1.71 3.24 3.68 |
| mem_total_kib | 65007132 |
| nproc | 32 |
| objecter_inflight_op_bytes | 104857600 |
| objecter_inflight_ops | 1024 |
| over_objects | 640 |
| pool_max_avail | 9858138112 |
| release | tentacle |
| rooket_version | rooket v0.0.0-20260928053658-3d8551957e08 |
| write4m_objects | 1000 |

This sweep predates the bracketed floor and the floor image: each floor ran once, after the sweep, from the cluster's own image, quay.io/ceph/ceph:v20.2.4, while the benchmark linked the host's librados, 19.2.6-1.fc43. Its throughput and latency ratios are shown but not decided.

## Verdict

cgo is not a bottleneck on a release when one of the callback and pipe modes passes all four criteria there. Throughput and mean latency compare read4k and write4k at 64 and 256 in flight, and read4m and write4m at 1, 4 and 16, with rados bench at the same size and concurrency, which in this sweep ran once per cell, after the sweep. The threads criterion allows each cell under the byte budget GOMAXPROCS + 8 threads of growth, its peak less its idle count. The cgo criterion is the share of the CPU profile of read4k at 256 in flight whose stacks hold a frame of the cgo boundary. The plan names runtime.cgocall, runtime.cgocallback*, the _Cfunc_ stubs and runtime.(*Pinner); its list is read as examples, so cgo's runtime.cgoCheck* pointer checks count as well. sync parks an OS thread per operation in flight and is the baseline, not judged. The informational row under the cgo criterion reads the same profile for the boundary cost alone, what a pure-Go client would no longer pay; it is not one of the four criteria, and the second answer reads it in the cgo criterion's place.

| criterion | sync (baseline) | callback | pipe |
|---|---|---|---|
| throughput>=0.8x floor | INCONCLUSIVE: unbracketed floor, one run after the sweep; worst 0.75x (read4k at 64), margin -0.05 | INCONCLUSIVE: unbracketed floor, one run after the sweep; worst 0.66x (read4k at 64), margin -0.14 | INCONCLUSIVE: unbracketed floor, one run after the sweep; worst 0.85x (read4k at 256), margin +0.05 |
| mean latency<=1.25x floor | INCONCLUSIVE: unbracketed floor, one run after the sweep; worst 1.34x (read4k at 64), margin -0.09 | INCONCLUSIVE: unbracketed floor, one run after the sweep; worst 1.53x (read4k at 64), margin -0.28 | INCONCLUSIVE: unbracketed floor, one run after the sweep; worst 1.18x (read4k at 256), margin +0.07 |
| threads<=gomaxprocs+8 | FAIL: against each cell's own idle count, worst +266 of 40 allowed (read4k at 512), margin -226; against its process's first, worst +532 of 40 allowed (read4k at 512), margin -492 | INCONCLUSIVE: against each cell's own idle count, worst +38 of 40 allowed (headwrite4k at 512), margin +2; against its process's first, worst +81 of 40 allowed (headwrite4k at 512), margin -41 | INCONCLUSIVE: against each cell's own idle count, worst +29 of 40 allowed (read4k at 512), margin +11; against its process's first, worst +116 of 40 allowed (indexrtt at 512), margin -76 |
| cgo<=10% of CPU | FAIL: 41.1% of 53.74s sampled, margin -31.1 points | FAIL: 26.5% of 1m7.19s sampled, margin -16.5 points | FAIL: 19.5% of 1m13.99s sampled, margin -9.5 points |
| informational: boundary cost<=10% of CPU (librados's C and the Go completion handler excluded) | PASS: 3.9% of 53.74s sampled, margin +6.1 points | PASS: 5.2% of 1m7.19s sampled, margin +4.8 points | PASS: 8.3% of 1m13.99s sampled, margin +1.7 points |

**Answer:** no judged mode passes all four criteria on tentacle.

**Answer with the boundary cost in place of the cgo criterion:** no judged mode passes all four criteria on tentacle.

In-flight limits of the cells: derived. No submission parked on the limiter.

Thread counts per benchmark process, the count before its first cell to its highest:

| mode | benchmark processes | largest growth in one process | highest count |
|---|---|---|---|
| sync | 4 | 27→559 (+532) | 559 |
| callback | 5 | 27→108 (+81) | 108 |
| pipe | 4 | 27→143 (+116) | 143 |

These cells carry no process id, so a mode's cells may have shared a benchmark process; the processes above are inferred, a cell that starts below its predecessor's process's peak starting a new one. Each cell's growth is judged against its own idle count, which leaves out what earlier cells of its process grew, and against its process's first idle count, which does not.

## read4k

| conc | mode | n | ops/s | p50 µs | p99 µs | p999 µs | mean µs | CPU µs/op | threads | errors | floor ops/s | floor mean µs | ops ÷ floor | mean ÷ floor | floor runs |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| 1 | sync | 322144 | 13525 | 70.9 | 116 | 169 | 73.9 | 37.2 | 27→29 (+2) | 0 | 15145 | 64.9 | 0.89x | 1.14x | single run after the sweep |
| 1 | callback | 306651 | 12108 | 79.4 | 128 | 177 | 82.5 | 48.3 | 27→29 (+2) | 0 | 15145 | 64.9 | 0.80x | 1.27x | single run after the sweep |
| 1 | pipe | 301219 | 12442 | 78.4 | 115 | 154 | 80.3 | 52.0 | 27→28 (+1) | 0 | 15145 | 64.9 | 0.82x | 1.24x | single run after the sweep |
| 16 | sync | 1258783 | 49266 | 323 | 473 | 576 | 325 | 24.8 | 29→46 (+17) | 0 | 62092 | 256 | 0.79x | 1.27x | single run after the sweep |
| 16 | callback | 1000000 | 47491 | 333 | 503 | 609 | 337 | 32.6 | 29→34 (+5) | 0 | 62092 | 256 | 0.76x | 1.32x | single run after the sweep |
| 16 | pipe | 1371463 | 56062 | 284 | 414 | 511 | 285 | 33.5 | 28→32 (+4) | 0 | 62092 | 256 | 0.90x | 1.12x | single run after the sweep |
| 64 | sync | 1207641 | 50666 | 1247 | 1676 | 1832 | 1263 | 26.6 | 46→95 (+49) | 0 | 68007 | 939 | 0.75x | 1.34x | single run after the sweep |
| 64 | callback | 1000000 | 44659 | 1443 | 1804 | 2013 | 1433 | 30.9 | 34→37 (+3) | 0 | 68007 | 939 | 0.66x | 1.53x | single run after the sweep |
| 64 | pipe | 1412826 | 58931 | 1080 | 1498 | 1821 | 1086 | 33.6 | 32→42 (+10) | 0 | 68007 | 939 | 0.87x | 1.16x | single run after the sweep |
| 256 | sync | 1249440 | 52278 | 4810 | 6324 | 6806 | 4896 | 27.5 | 95→293 (+198) | 0 | 69440 | 3685 | 0.75x | 1.33x | single run after the sweep |
| 256 | callback | 1319026 | 55578 | 4597 | 5585 | 5967 | 4606 | 29.0 | 38→45 (+7) | 0 | 69440 | 3685 | 0.80x | 1.25x | single run after the sweep |
| 256 | pipe | 1426580 | 58763 | 4233 | 7858 | 13022 | 4356 | 34.2 | 42→56 (+14) | 0 | 69440 | 3685 | 0.85x | 1.18x | single run after the sweep |
| 512 | sync | 1366884 | 57268 | 8819 | 11418 | 13325 | 8938 | 28.0 | 293→559 (+266) | 0 | 68491 | 7473 | 0.84x | 1.20x | single run after the sweep |
| 512 | callback | 1329850 | 55895 | 9103 | 10977 | 11603 | 9158 | 29.1 | 45→47 (+2) | 0 | 68491 | 7473 | 0.82x | 1.23x | single run after the sweep |
| 512 | pipe | 1455397 | 60875 | 8364 | 10802 | 12311 | 8409 | 33.9 | 56→85 (+29) | 0 | 68491 | 7473 | 0.89x | 1.13x | single run after the sweep |

## write4k

| conc | mode | n | ops/s | p50 µs | p99 µs | p999 µs | mean µs | CPU µs/op | threads | errors | floor ops/s | floor mean µs | ops ÷ floor | mean ÷ floor | floor runs |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| 1 | sync | 14332 | 603 | 1363 | 4777 | 5852 | 1657 | 67.6 | 559→559 (+0) | 0 | 594 | 1681 | 1.02x | 0.99x | single run after the sweep |
| 1 | callback | 13908 | 600 | 1360 | 4837 | 6625 | 1665 | 85.0 | 47→47 (+0) | 0 | 594 | 1681 | 1.01x | 0.99x | single run after the sweep |
| 1 | pipe | 13357 | 584 | 1383 | 5706 | 9887 | 1712 | 85.6 | 85→85 (+0) | 0 | 594 | 1681 | 0.98x | 1.02x | single run after the sweep |
| 16 | sync | 97340 | 4405 | 3479 | 10028 | 15535 | 3632 | 45.4 | 559→559 (+0) | 0 | 3970 | 4029 | 1.11x | 0.90x | single run after the sweep |
| 16 | callback | 102337 | 4151 | 3390 | 10329 | 23335 | 3854 | 43.2 | 47→47 (+0) | 0 | 3970 | 4029 | 1.05x | 0.96x | single run after the sweep |
| 16 | pipe | 97260 | 3676 | 3616 | 14094 | 35057 | 4352 | 45.4 | 85→85 (+0) | 0 | 3970 | 4029 | 0.93x | 1.08x | single run after the sweep |
| 64 | sync | 338497 | 12607 | 4810 | 10666 | 39119 | 5076 | 35.1 | 559→559 (+0) | 0 | 9885 | 6473 | 1.28x | 0.78x | single run after the sweep |
| 64 | callback | 248458 | 9803 | 6205 | 12470 | 42207 | 6528 | 36.6 | 47→47 (+0) | 0 | 9885 | 6473 | 0.99x | 1.01x | single run after the sweep |
| 64 | pipe | 218790 | 9837 | 6099 | 14477 | 47830 | 6505 | 37.7 | 85→85 (+0) | 0 | 9885 | 6473 | 1.00x | 1.00x | single run after the sweep |
| 256 | sync | 528303 | 24172 | 10134 | 22891 | 57001 | 10583 | 37.8 | 559→559 (+0) | 0 | 4886 | 52358 | 4.95x | 0.20x | single run after the sweep |
| 256 | callback | 90553 | 4216 | 61716 | 222427 | 442886 | 60650 | 39.4 | 47→48 (+1) | 0 | 4886 | 52358 | 0.86x | 1.16x | single run after the sweep |
| 256 | pipe | 215799 | 4695 | 57136 | 159103 | 574153 | 54491 | 40.2 | 85→86 (+1) | 0 | 4886 | 52358 | 0.96x | 1.04x | single run after the sweep |
| 512 | sync | 510842 | 24413 | 19739 | 51409 | 122457 | 20944 | 36.5 | 559→559 (+0) | 0 | 4228 | 120773 | 5.77x | 0.17x | single run after the sweep |
| 512 | callback | 114085 | 3579 | 125833 | 670260 | 1136290 | 142993 | 40.1 | 48→70 (+22) | 0 | 4228 | 120773 | 0.85x | 1.18x | single run after the sweep |
| 512 | pipe | 103020 | 4946 | 108656 | 220585 | 393761 | 103376 | 41.4 | 86→112 (+26) | 0 | 4228 | 120773 | 1.17x | 0.86x | single run after the sweep |

## read4m

| conc | mode | n | ops/s | p50 µs | p99 µs | p999 µs | mean µs | CPU µs/op | threads | errors | floor ops/s | floor mean µs | ops ÷ floor | mean ÷ floor | floor runs |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| 1 | sync | 8329 | 368 | 2625 | 3687 | 9813 | 2717 | 766.8 | 27→28 (+1) | 0 | 223 | 4485 | 1.65x | 0.61x | single run after the sweep |
| 1 | callback | 4972 | 197 | 4803 | 6869 | 16921 | 5086 | 825.8 | 27→28 (+1) | 0 | 223 | 4485 | 0.88x | 1.13x | single run after the sweep |
| 1 | pipe | 5799 | 241 | 4076 | 5096 | 6295 | 4142 | 794.7 | 27→29 (+2) | 0 | 223 | 4485 | 1.08x | 0.92x | single run after the sweep |
| 4 | sync | 13460 | 559 | 6997 | 14704 | 26385 | 7153 | 756.3 | 28→29 (+1) | 0 | 234 | 17078 | 2.39x | 0.42x | single run after the sweep |
| 4 | callback | 5665 | 248 | 15850 | 23356 | 92742 | 16106 | 930.9 | 28→29 (+1) | 0 | 234 | 17078 | 1.06x | 0.94x | single run after the sweep |
| 4 | pipe | 8222 | 345 | 11475 | 16571 | 63244 | 11608 | 775.7 | 29→30 (+1) | 0 | 234 | 17078 | 1.47x | 0.68x | single run after the sweep |
| 16 | sync | 9416 | 422 | 25855 | 161469 | 397507 | 37820 | 806.0 | 29→41 (+12) | 0 | 267 | 59892 | 1.58x | 0.63x | single run after the sweep |
| 16 | callback | 5431 | 260 | 46314 | 159627 | 209575 | 61548 | 896.4 | 29→41 (+12) | 0 | 267 | 59892 | 0.97x | 1.03x | single run after the sweep |
| 16 | pipe | 6022 | 286 | 41684 | 147356 | 169809 | 55797 | 792.2 | 30→42 (+12) | 0 | 267 | 59892 | 1.07x | 0.93x | single run after the sweep |

## write4m

| conc | mode | n | ops/s | p50 µs | p99 µs | p999 µs | mean µs | CPU µs/op | threads | errors | floor ops/s | floor mean µs | ops ÷ floor | mean ÷ floor | floor runs |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| 1 | sync | 1000 | 121 | 8008 | 16402 | 21427 | 8294 | 923.9 | 27→27 (+0) | 0 | 119 | 8373 | 1.01x | 0.99x | single run after the sweep |
| 1 | callback | 1000 | 106 | 9193 | 16977 | 27215 | 9396 | 1086.3 | 27→28 (+1) | 0 | 119 | 8373 | 0.89x | 1.12x | single run after the sweep |
| 1 | pipe | 1000 | 113 | 8632 | 14752 | 17980 | 8871 | 1120.0 | 27→28 (+1) | 0 | 119 | 8373 | 0.94x | 1.06x | single run after the sweep |
| 4 | sync | 1000 | 169 | 23246 | 39087 | 48984 | 23551 | 1204.1 | 27→29 (+2) | 0 | 134 | 29770 | 1.26x | 0.79x | single run after the sweep |
| 4 | callback | 1000 | 156 | 25046 | 45056 | 55215 | 25650 | 1247.9 | 26→28 (+2) | 0 | 134 | 29770 | 1.16x | 0.86x | single run after the sweep |
| 4 | pipe | 1000 | 166 | 23406 | 41762 | 53637 | 24069 | 1301.6 | 27→27 (+0) | 0 | 134 | 29770 | 1.24x | 0.81x | single run after the sweep |
| 16 | sync | 1000 | 166 | 93303 | 155960 | 185486 | 95685 | 2591.6 | 27→36 (+9) | 0 | 163 | 97910 | 1.02x | 0.98x | single run after the sweep |
| 16 | callback | 1000 | 159 | 96277 | 175470 | 203936 | 100330 | 2674.4 | 27→32 (+5) | 0 | 163 | 97910 | 0.97x | 1.02x | single run after the sweep |
| 16 | pipe | 1000 | 150 | 104460 | 177007 | 214916 | 106370 | 2729.3 | 28→31 (+3) | 0 | 163 | 97910 | 0.92x | 1.09x | single run after the sweep |

## headread4k

| conc | mode | n | ops/s | p50 µs | p99 µs | p999 µs | mean µs | CPU µs/op | threads | errors |
|---|---|---|---|---|---|---|---|---|---|---|
| 1 | sync | 216487 | 9922 | 96.0 | 164 | 248 | 101 | 56.8 | 559→559 (+0) | 0 |
| 1 | callback | 205130 | 8783 | 105 | 242 | 812 | 114 | 67.1 | 70→70 (+0) | 0 |
| 1 | pipe | 233943 | 9394 | 103 | 169 | 239 | 106 | 70.1 | 112→112 (+0) | 0 |
| 16 | sync | 832106 | 34449 | 461 | 655 | 783 | 464 | 43.5 | 559→559 (+0) | 0 |
| 16 | callback | 944449 | 42064 | 376 | 592 | 763 | 380 | 52.7 | 70→70 (+0) | 0 |
| 16 | pipe | 1000000 | 43825 | 357 | 577 | 732 | 365 | 52.5 | 112→112 (+0) | 0 |

## headwrite4k

| conc | mode | n | ops/s | p50 µs | p99 µs | p999 µs | mean µs | CPU µs/op | threads | errors |
|---|---|---|---|---|---|---|---|---|---|---|
| 1 | sync | 13760 | 584 | 1399 | 5105 | 6867 | 1711 | 86.8 | 559→559 (+0) | 0 |
| 1 | callback | 12410 | 531 | 1442 | 5729 | 7946 | 1882 | 110.5 | 70→70 (+0) | 0 |
| 1 | pipe | 13353 | 564 | 1421 | 5157 | 6880 | 1772 | 114.1 | 112→112 (+0) | 0 |
| 16 | sync | 99123 | 4185 | 3446 | 11329 | 15342 | 3823 | 67.6 | 559→559 (+0) | 0 |
| 16 | callback | 99662 | 4010 | 3493 | 10308 | 20333 | 3990 | 71.6 | 70→70 (+0) | 0 |
| 16 | pipe | 99386 | 3991 | 3471 | 10166 | 18968 | 4008 | 72.1 | 112→112 (+0) | 0 |
| 64 | sync | 257384 | 11101 | 5431 | 13124 | 42387 | 5764 | 57.5 | 559→559 (+0) | 0 |
| 64 | callback | 268428 | 9933 | 6159 | 11672 | 40076 | 6443 | 61.9 | 70→70 (+0) | 0 |
| 64 | pipe | 210610 | 9304 | 6587 | 14800 | 40211 | 6878 | 64.6 | 112→112 (+0) | 0 |
| 256 | sync | 267225 | 4165 | 55490 | 286255 | 475202 | 61440 | 73.6 | 559→559 (+0) | 0 |
| 256 | callback | 158088 | 4506 | 61759 | 154020 | 212109 | 56791 | 64.3 | 70→70 (+0) | 0 |
| 256 | pipe | 494319 | 4403 | 59420 | 199959 | 512970 | 58133 | 72.7 | 112→114 (+2) | 0 |
| 512 | sync | 171412 | 4897 | 108389 | 289896 | 465077 | 104335 | 62.3 | 559→559 (+0) | 0 |
| 512 | callback | 140492 | 4859 | 111674 | 226793 | 286345 | 105179 | 68.8 | 70→108 (+38) | 0 |
| 512 | pipe | 220788 | 4381 | 117738 | 258806 | 412187 | 116802 | 71.4 | 114→114 (+0) | 0 |

## indexrtt

| conc | mode | n | ops/s | p50 µs | p99 µs | p999 µs | mean µs | CPU µs/op | threads | errors |
|---|---|---|---|---|---|---|---|---|---|---|
| 1 | sync | 7377 | 453 | 3678 | 11472 | 25535 | 4419 | 137.5 | 559→559 (+0) | 0 |
| 1 | callback | 6596 | 604 | 3479 | 10430 | 12685 | 3310 | 99.6 | 108→108 (+0) | 0 |
| 1 | pipe | 6367 | 610 | 3449 | 10413 | 12951 | 3278 | 95.5 | 114→114 (+0) | 0 |
| 16 | sync | 45297 | 3911 | 7135 | 18133 | 50237 | 8180 | 74.4 | 559→559 (+0) | 0 |
| 16 | callback | 62044 | 5326 | 5733 | 13971 | 21075 | 6008 | 52.1 | 108→108 (+0) | 0 |
| 16 | pipe | 62971 | 4855 | 5818 | 16742 | 25092 | 6590 | 57.2 | 114→114 (+0) | 0 |
| 64 | sync | 225238 | 15922 | 6761 | 20310 | 43181 | 8037 | 46.4 | 559→559 (+0) | 0 |
| 64 | callback | 232958 | 16430 | 6632 | 18306 | 42426 | 7790 | 44.5 | 108→108 (+0) | 0 |
| 64 | pipe | 210675 | 17810 | 6586 | 17619 | 31190 | 7186 | 45.3 | 114→114 (+0) | 0 |
| 256 | sync | 420468 | 33371 | 14810 | 29287 | 72277 | 15339 | 43.2 | 559→559 (+0) | 0 |
| 256 | callback | 392828 | 38731 | 12569 | 28914 | 66863 | 13215 | 38.3 | 108→108 (+0) | 0 |
| 256 | pipe | 428158 | 37352 | 13150 | 25493 | 52484 | 13704 | 47.6 | 114→114 (+0) | 0 |
| 512 | sync | 433519 | 33512 | 29216 | 55227 | 129750 | 30542 | 43.3 | 559→559 (+0) | 0 |
| 512 | callback | 421797 | 38140 | 25367 | 51277 | 89335 | 26840 | 38.8 | 108→108 (+0) | 0 |
| 512 | pipe | 444914 | 35034 | 28107 | 52145 | 84576 | 29218 | 49.7 | 114→143 (+29) | 0 |

## rados bench floor

| shape | op | size | conc | run | image | seconds | max objects | objects read | ops | elapsed s | ops/s | mean µs | MB/s |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| read4k | rand | 4096 | 1 | single | quay.io/ceph/ceph:v20.2.4 | 20 | — | 1 | 302903 | 20.00 | 15145 | 64.9 | 59.16 |
| read4k | rand | 4096 | 16 | single | quay.io/ceph/ceph:v20.2.4 | 20 | — | 16 | 1241854 | 20.00 | 62092 | 256 | 242.55 |
| read4k | rand | 4096 | 64 | single | quay.io/ceph/ceph:v20.2.4 | 20 | — | 64 | 1360196 | 20.00 | 68007 | 939 | 265.65 |
| read4k | rand | 4096 | 256 | single | quay.io/ceph/ceph:v20.2.4 | 20 | — | 256 | 1389008 | 20.00 | 69440 | 3685 | 271.25 |
| read4k | rand | 4096 | 512 | single | quay.io/ceph/ceph:v20.2.4 | 20 | — | 512 | 1370234 | 20.01 | 68491 | 7473 | 267.54 |
| write4k | write | 4096 | 1 | single | quay.io/ceph/ceph:v20.2.4 | 20 | — | — | 11888 | 20.00 | 594 | 1681 | 2.32 |
| write4k | write | 4096 | 16 | single | quay.io/ceph/ceph:v20.2.4 | 20 | — | — | 79411 | 20.00 | 3970 | 4029 | 15.51 |
| write4k | write | 4096 | 64 | single | quay.io/ceph/ceph:v20.2.4 | 20 | — | — | 197776 | 20.01 | 9885 | 6473 | 38.62 |
| write4k | write | 4096 | 256 | single | quay.io/ceph/ceph:v20.2.4 | 20 | — | — | 97947 | 20.05 | 4886 | 52358 | 19.09 |
| write4k | write | 4096 | 512 | single | quay.io/ceph/ceph:v20.2.4 | 20 | — | — | 85016 | 20.11 | 4228 | 120773 | 16.52 |
| read4m | rand | 4194304 | 1 | single | quay.io/ceph/ceph:v20.2.4 | 20 | — | 1 | 4457 | 20.00 | 223 | 4485 | 891.37 |
| read4m | rand | 4194304 | 4 | single | quay.io/ceph/ceph:v20.2.4 | 20 | — | 4 | 4685 | 20.01 | 234 | 17078 | 936.37 |
| read4m | rand | 4194304 | 16 | single | quay.io/ceph/ceph:v20.2.4 | 20 | — | 16 | 5349 | 20.05 | 267 | 59892 | 1066.97 |
| write4m | write | 4194304 | 1 | single | quay.io/ceph/ceph:v20.2.4 | 600 | 1000 | — | 1000 | 8.38 | 119 | 8373 | 477.58 |
| write4m | write | 4194304 | 4 | single | quay.io/ceph/ceph:v20.2.4 | 600 | 1000 | — | 1000 | 7.45 | 134 | 29770 | 536.78 |
| write4m | write | 4194304 | 16 | single | quay.io/ceph/ceph:v20.2.4 | 600 | 1000 | — | 1000 | 6.13 | 163 | 97910 | 652.42 |

## Over the byte budget

Cells run past the byte budget on purpose, each in a process of its own. With the limiter's bounds above what the cell can reach, librados's objecter throttle blocks submitting threads inside C; with the bounds derived from that throttle, the limiter parks the excess submissions in Go. The threads criterion does not cover these cells.

| mode | shape | conc | limiter | n | ops/s | mean µs | threads | growth | allowance | limiter waits |
|---|---|---|---|---|---|---|---|---|---|---|
| callback | write4m | 64 | ops 1048576, bytes 1099511627776 | 640 | 165 | 375745 | 27→63 | +36 | 40 | 0 |
| callback | write4m | 64 | derived | 640 | 171 | 359927 | 27→50 | +23 | 40 | 617 |
| pipe | write4m | 64 | ops 1048576, bytes 1099511627776 | 640 | 166 | 373163 | 27→60 | +33 | 40 | 0 |
| pipe | write4m | 64 | derived | 640 | 166 | 371973 | 26→46 | +20 | 40 | 617 |

## CPU profiles

The profile samples every thread of the process. A sample taken while a Go thread runs C is recorded under the Go stack that called it, ending in runtime.cgocall, so the cgo share holds the C that Go calls ran, librados's submission included, as well as the crossing itself; that C is the second share. A sample of a thread the Go runtime does not run, librados's messenger and finisher threads among them, is recorded under runtime._ExternalCode; that is the last share, and outside the cgo share.

The columns between divide the profile by the frame nearest each sample's leaf, so no sample counts twice. (i), the submit crossing, is the cgo machinery around the C: runtime.cgocall's Go side, the _Cfunc_ stubs, the Pinner and the pointer checks, the completion handler's own calls into C included. (ii), the delivery, is how a completion reaches Go: the runtime.cgocallback* frames for callback, (*aioPipe).drain and the pipe read it waits in for pipe, and nothing for sync, whose wake happens in C. Neither counts the Go completion handler, rados.aioComplete, shown for reference: a pure-Go client runs one too. The boundary cost is (i) plus (ii).

| mode | profile | sampled | cgo frames | C under runtime.cgocall | (i) submit crossing | (ii) delivery | boundary cost | Go completion handler | threads outside Go | largest cgo frames, cumulative |
|---|---|---|---|---|---|---|---|---|---|---|
| sync | cpu-sync-read4k-256.pprof | 53.74s | 41.1% | 37.1% | 3.9% | 0.0% | 3.9% | 0.0% | 45.0% | runtime.cgocall 21.16s; github.com/ceph/go-ceph/rados._Cfunc_rados_read_op_operate 18.31s; github.com/ceph/go-ceph/rados._Cfunc_rados_read_op_read 1.53s; github.com/ceph/go-ceph/rados._Cfunc_CString 330ms; runtime.(*Pinner).Unpin 320ms |
| callback | cpu-callback-read4k-256.pprof | 1m7.19s | 26.5% | 17.1% | 5.1% | 0.1% | 5.2% | 4.1% | 41.5% | runtime.cgocall 12.16s; github.com/ceph/go-ceph/rados._Cfunc_rados_aio_read_op_operate 8.65s; runtime.cgocallback 3.36s; runtime.cgocallbackg 3.32s; runtime.cgocallbackg1 3.28s |
| pipe | cpu-pipe-read4k-256.pprof | 1m13.99s | 19.5% | 15.6% | 3.9% | 4.4% | 8.3% | 3.3% | 39.7% | runtime.cgocall 12.26s; github.com/ceph/go-ceph/rados._Cfunc_rados_aio_read_op_operate 8.97s; runtime.(*Pinner).Pin 1.17s; github.com/ceph/go-ceph/rados._Cfunc_rados_read_op_read 980ms; github.com/ceph/go-ceph/rados._Cfunc_free 520ms |
