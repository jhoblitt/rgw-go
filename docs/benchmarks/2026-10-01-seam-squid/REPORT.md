# Seam microbenchmark: squid

| env.json | |
|---|---|
| bench_time | 20s |
| ceph_version | 19.2.6 |
| conc | 1,16,64,256,512 |
| cpu | AMD Ryzen 9 7950X3D 16-Core Processor |
| date | 2026-10-01T22:00:41Z |
| floor_seconds | 20 |
| git_describe | fcd09fb |
| go_version | go version go1.27.1 linux/amd64 |
| gomaxprocs | unset |
| host_librados | 19.2.6-1.fc43 |
| image | quay.io/ceph/ceph:v19.2.6 |
| kernel | 7.2.6-100.fc43.x86_64 |
| large_conc | 1,4,16 |
| load | 1.63 2.32 1.95 |
| mem_total_kib | 65007132 |
| nproc | 32 |
| objecter_inflight_op_bytes | 104857600 |
| objecter_inflight_ops | 1024 |
| over_objects | 640 |
| pool_max_avail | 9197248512 |
| release | squid |
| rooket_version | rooket v0.0.0-20260928053658-3d8551957e08 |
| write4m_objects | 1000 |

This sweep predates the bracketed floor and the floor image: each floor ran once, after the sweep, from the cluster's own image, quay.io/ceph/ceph:v19.2.6, while the benchmark linked the host's librados, 19.2.6-1.fc43. Its throughput and latency ratios are shown but not decided. The floor's writes carried rados bench's allocation hints, which the Go cells do not send.

## Verdict

cgo is not a bottleneck on a release when one of the callback and pipe modes passes all four criteria there. Throughput and mean latency compare read4k and write4k at 64 and 256 in flight, and write4m at 1, 4 and 16, with rados bench at the same size and concurrency, which in this sweep ran once per cell, after the sweep. read4m is measured and shown with its floor, but not judged; its table says why. The threads criterion allows each cell under the byte budget GOMAXPROCS + 8 threads of growth, its peak less its idle count. The cgo criterion is the share of the CPU profile of read4k at 256 in flight whose stacks hold a frame of the cgo boundary. The plan names runtime.cgocall, runtime.cgocallback*, the _Cfunc_ stubs and runtime.(*Pinner); its list is read as examples, so cgo's runtime.cgoCheck* pointer checks count as well. sync parks an OS thread per operation in flight and is the baseline, not judged. The informational row under the cgo criterion reads the same profile for the boundary cost alone, what a pure-Go client would no longer pay; it is not one of the four criteria, and the second answer reads it in the cgo criterion's place. For pipe it leaves out the C side of each completion, one write(2) to the pipe on a librados thread, which the profile records under runtime._ExternalCode with no frame to tell it by: it is unmeasured, and pipe's boundary cost is low by that much.

| criterion | sync (baseline) | callback | pipe |
|---|---|---|---|
| throughput>=0.8x floor | INCONCLUSIVE: unbracketed floor, one run after the sweep; worst 0.81x (write4m at 1), margin +0.01 | INCONCLUSIVE: unbracketed floor, one run after the sweep; worst 0.58x (write4m at 16), margin -0.22 | INCONCLUSIVE: unbracketed floor, one run after the sweep; worst 0.34x (write4k at 256), margin -0.46 |
| mean latency<=1.25x floor | INCONCLUSIVE: unbracketed floor, one run after the sweep; worst 1.23x (write4m at 1), margin +0.02 | INCONCLUSIVE: unbracketed floor, one run after the sweep; worst 1.71x (write4m at 16), margin -0.46 | INCONCLUSIVE: unbracketed floor, one run after the sweep; worst 2.93x (write4k at 256), margin -1.68 |
| threads<=gomaxprocs+8 | FAIL: against each cell's own idle count, worst +260 of 40 allowed (read4k at 512), margin -220; against its process's first, worst +533 of 40 allowed (read4k at 512), margin -493 | INCONCLUSIVE: against each cell's own idle count, worst +21 of 40 allowed (headwrite4k at 512), margin +19; against its process's first, worst +62 of 40 allowed (headwrite4k at 512), margin -22 | INCONCLUSIVE: against each cell's own idle count, worst +36 of 40 allowed (indexrtt at 512), margin +4; against its process's first, worst +118 of 40 allowed (indexrtt at 512), margin -78 |
| cgo<=10% of CPU | FAIL: 37.0% of 20.53s sampled, margin -27.0 points | FAIL: 25.6% of 1m9.02s sampled, margin -15.6 points | FAIL: 18.8% of 1m8.21s sampled, margin -8.8 points |
| informational: boundary cost<=10% of CPU (librados's C and the Go completion handler excluded) | PASS: 2.7% of 20.53s sampled, margin +7.3 points | PASS: 4.8% of 1m9.02s sampled, margin +5.2 points | PASS: 9.2% of 1m8.21s sampled, margin +0.8 points; leaves out pipe's write(2) per completion, unmeasured |

**Answer:** no judged mode passes all four criteria on squid.

**Answer with the boundary cost in place of the cgo criterion:** no judged mode passes all four criteria on squid.

In-flight limits of the cells: derived. No submission parked on the limiter.

Thread counts per benchmark process, the count before its first cell to its highest:

| mode | benchmark processes | largest growth in one process | highest count |
|---|---|---|---|
| sync | 5 | 26→559 (+533) | 559 |
| callback | 5 | 27→89 (+62) | 89 |
| pipe | 5 | 27→145 (+118) | 145 |

These cells carry no process id, so a mode's cells may have shared a benchmark process; the processes above are inferred, a cell that starts below its predecessor's process's peak starting a new one. Each cell's growth is judged against its own idle count, which leaves out what earlier cells of its process grew, and against its process's first idle count, which does not.

## read4k

| conc | mode | n | ops/s | p50 µs | p99 µs | p999 µs | mean µs | CPU µs/op | threads | errors | floor ops/s | floor mean µs | ops ÷ floor | mean ÷ floor | floor runs |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| 1 | sync | 370755 | 14314 | 67.2 | 105 | 151 | 69.8 | 34.2 | 26→29 (+3) | 0 | 14630 | 67.2 | 0.98x | 1.04x | single run after the sweep |
| 1 | callback | 307160 | 12405 | 77.8 | 123 | 170 | 80.6 | 47.7 | 27→29 (+2) | 0 | 14630 | 67.2 | 0.85x | 1.20x | single run after the sweep |
| 1 | pipe | 287098 | 11345 | 83.6 | 146 | 205 | 88.1 | 56.8 | 27→29 (+2) | 0 | 14630 | 67.2 | 0.78x | 1.31x | single run after the sweep |
| 16 | sync | 1337246 | 56127 | 283 | 404 | 500 | 285 | 22.1 | 29→45 (+16) | 0 | 54631 | 291 | 1.03x | 0.98x | single run after the sweep |
| 16 | callback | 1000000 | 48140 | 329 | 490 | 590 | 332 | 34.4 | 29→33 (+4) | 0 | 54631 | 291 | 0.88x | 1.14x | single run after the sweep |
| 16 | pipe | 1000000 | 47610 | 332 | 497 | 615 | 336 | 38.2 | 29→34 (+5) | 0 | 54631 | 291 | 0.87x | 1.16x | single run after the sweep |
| 64 | sync | 1394095 | 53239 | 1165 | 1962 | 4224 | 1202 | 24.6 | 45→97 (+52) | 0 | 59380 | 1075 | 0.90x | 1.12x | single run after the sweep |
| 64 | callback | 1301738 | 54305 | 1158 | 1675 | 1907 | 1178 | 33.7 | 33→37 (+4) | 0 | 59380 | 1075 | 0.91x | 1.10x | single run after the sweep |
| 64 | pipe | 1289745 | 55483 | 1151 | 1461 | 1757 | 1153 | 33.3 | 34→39 (+5) | 0 | 59380 | 1075 | 0.93x | 1.07x | single run after the sweep |
| 256 | sync | 1343133 | 57773 | 4369 | 5983 | 6671 | 4430 | 26.9 | 97→299 (+202) | 0 | 60837 | 4204 | 0.95x | 1.05x | single run after the sweep |
| 256 | callback | 1379072 | 57791 | 4408 | 5653 | 6685 | 4429 | 34.2 | 40→51 (+11) | 0 | 60837 | 4204 | 0.95x | 1.05x | single run after the sweep |
| 256 | pipe | 1349804 | 56088 | 4551 | 5550 | 6218 | 4564 | 33.4 | 39→46 (+7) | 0 | 60837 | 4204 | 0.92x | 1.09x | single run after the sweep |
| 512 | sync | 1396958 | 58468 | 8655 | 11202 | 12999 | 8754 | 28.0 | 299→559 (+260) | 0 | 60489 | 8459 | 0.97x | 1.03x | single run after the sweep |
| 512 | callback | 1394967 | 58036 | 8780 | 10741 | 12462 | 8820 | 35.0 | 51→61 (+10) | 0 | 60489 | 8459 | 0.96x | 1.04x | single run after the sweep |
| 512 | pipe | 1332310 | 56648 | 9023 | 10796 | 12176 | 9036 | 32.7 | 46→52 (+6) | 0 | 60489 | 8459 | 0.94x | 1.07x | single run after the sweep |

## write4k

| conc | mode | n | ops/s | p50 µs | p99 µs | p999 µs | mean µs | CPU µs/op | threads | errors | floor ops/s | floor mean µs | ops ÷ floor | mean ÷ floor | floor runs |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| 1 | sync | 7696 | 317 | 3344 | 7691 | 12334 | 3157 | 89.6 | 559→559 (+0) | 0 | 317 | 3157 | 1.00x | 1.00x | single run after the sweep |
| 1 | callback | 6580 | 315 | 3345 | 10065 | 12376 | 3179 | 122.4 | 61→61 (+0) | 0 | 317 | 3157 | 0.99x | 1.01x | single run after the sweep |
| 1 | pipe | 7549 | 300 | 3379 | 10487 | 14401 | 3336 | 107.0 | 52→52 (+0) | 0 | 317 | 3157 | 0.95x | 1.06x | single run after the sweep |
| 16 | sync | 58154 | 2298 | 6025 | 16928 | 27158 | 6961 | 53.3 | 559→559 (+0) | 0 | 2650 | 6037 | 0.87x | 1.15x | single run after the sweep |
| 16 | callback | 63158 | 2225 | 5734 | 26764 | 31239 | 7191 | 50.3 | 61→61 (+0) | 0 | 2650 | 6037 | 0.84x | 1.19x | single run after the sweep |
| 16 | pipe | 56293 | 2394 | 5911 | 15571 | 21892 | 6683 | 51.4 | 52→52 (+0) | 0 | 2650 | 6037 | 0.90x | 1.11x | single run after the sweep |
| 64 | sync | 150998 | 6380 | 9266 | 22084 | 51743 | 10014 | 39.1 | 559→559 (+0) | 0 | 7805 | 8199 | 0.82x | 1.22x | single run after the sweep |
| 64 | callback | 154315 | 6631 | 7075 | 27686 | 39588 | 9651 | 40.5 | 61→61 (+0) | 0 | 7805 | 8199 | 0.85x | 1.18x | single run after the sweep |
| 64 | pipe | 159956 | 6961 | 8676 | 20026 | 43363 | 9194 | 40.3 | 52→52 (+0) | 0 | 7805 | 8199 | 0.89x | 1.12x | single run after the sweep |
| 256 | sync | 473952 | 16011 | 14227 | 41867 | 105157 | 15984 | 40.0 | 559→559 (+0) | 0 | 16275 | 15724 | 0.98x | 1.02x | single run after the sweep |
| 256 | callback | 395570 | 18998 | 13123 | 31103 | 63145 | 13472 | 36.2 | 61→61 (+0) | 0 | 16275 | 15724 | 1.17x | 0.86x | single run after the sweep |
| 256 | pipe | 238249 | 5548 | 36843 | 178261 | 299628 | 46107 | 41.6 | 52→68 (+16) | 0 | 16275 | 15724 | 0.34x | 2.93x | single run after the sweep |
| 512 | sync | 370395 | 11538 | 33881 | 115503 | 372759 | 44353 | 37.6 | 559→559 (+0) | 0 | 16306 | 31378 | 0.71x | 1.41x | single run after the sweep |
| 512 | callback | 438552 | 18732 | 25487 | 57277 | 95399 | 27320 | 34.9 | 61→68 (+7) | 0 | 16306 | 31378 | 1.15x | 0.87x | single run after the sweep |
| 512 | pipe | 139792 | 3756 | 137113 | 296137 | 412168 | 136126 | 44.1 | 68→85 (+17) | 0 | 16306 | 31378 | 0.23x | 4.34x | single run after the sweep |

## read4m

Not judged: rados bench's 4 MiB rand floor ran at about a quarter of the Go cells' rate, flat across concurrency, and differs from them where it matters: rand picks its objects at random with replacement, its fixed object names land on fixed placement groups, its objects carried allocation hints before the floor passed --no-hints, and its client library is another build. Its ratios are shown, but the throughput and latency criteria leave read4m out until the floor reads as the Go cell does.

| conc | mode | n | ops/s | p50 µs | p99 µs | p999 µs | mean µs | CPU µs/op | threads | errors | floor ops/s | floor mean µs | ops ÷ floor | mean ÷ floor | floor runs |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| 1 | sync | 12148 | 511 | 1900 | 2919 | 3185 | 1957 | 752.5 | 27→29 (+2) | 0 | 333 | 2997 | 1.53x | 0.65x | single run after the sweep |
| 1 | callback | 24457 | 1031 | 944 | 1601 | 1858 | 970 | 738.6 | 27→29 (+2) | 0 | 333 | 2997 | 3.09x | 0.32x | single run after the sweep |
| 1 | pipe | 5124 | 232 | 4249 | 5306 | 5536 | 4311 | 860.2 | 27→28 (+1) | 0 | 333 | 2997 | 0.70x | 1.44x | single run after the sweep |
| 4 | sync | 25682 | 1046 | 3579 | 9454 | 12722 | 3824 | 685.3 | 29→31 (+2) | 0 | 336 | 11905 | 3.11x | 0.32x | single run after the sweep |
| 4 | callback | 33678 | 1214 | 1845 | 14526 | 16141 | 3295 | 727.3 | 29→30 (+1) | 0 | 336 | 11905 | 3.61x | 0.28x | single run after the sweep |
| 4 | pipe | 8862 | 360 | 11052 | 19519 | 20084 | 11101 | 942.8 | 28→29 (+1) | 0 | 336 | 11905 | 1.07x | 0.93x | single run after the sweep |
| 16 | sync | 8004 | 301 | 51221 | 97758 | 260715 | 53173 | 899.1 | 31→42 (+11) | 0 | 441 | 36259 | 0.68x | 1.47x | single run after the sweep |
| 16 | callback | 25864 | 1084 | 13074 | 28547 | 37130 | 14748 | 879.1 | 30→40 (+10) | 0 | 441 | 36259 | 2.46x | 0.41x | single run after the sweep |
| 16 | pipe | 4237 | 193 | 81550 | 112793 | 125019 | 82765 | 978.7 | 29→42 (+13) | 0 | 441 | 36259 | 0.44x | 2.28x | single run after the sweep |

## write4m

| conc | mode | n | ops/s | p50 µs | p99 µs | p999 µs | mean µs | CPU µs/op | threads | errors | floor ops/s | floor mean µs | ops ÷ floor | mean ÷ floor | floor runs |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| 1 | sync | 1000 | 84.8 | 11186 | 23513 | 38298 | 11788 | 1074.6 | 26→28 (+2) | 0 | 104 | 9585 | 0.81x | 1.23x | single run after the sweep |
| 1 | callback | 1000 | 78.9 | 13023 | 24546 | 35686 | 12668 | 1145.8 | 27→28 (+1) | 0 | 104 | 9585 | 0.76x | 1.32x | single run after the sweep |
| 1 | pipe | 1000 | 105 | 9082 | 18732 | 27490 | 9500 | 926.4 | 27→28 (+1) | 0 | 104 | 9585 | 1.01x | 0.99x | single run after the sweep |
| 4 | sync | 1000 | 117 | 33756 | 61561 | 88841 | 34057 | 1151.0 | 27→29 (+2) | 0 | 137 | 29211 | 0.86x | 1.17x | single run after the sweep |
| 4 | callback | 1000 | 80.1 | 48284 | 80122 | 98123 | 49869 | 1288.6 | 27→28 (+1) | 0 | 137 | 29211 | 0.59x | 1.71x | single run after the sweep |
| 4 | pipe | 1000 | 137 | 28295 | 51924 | 76611 | 29233 | 1162.8 | 27→28 (+1) | 0 | 137 | 29211 | 1.00x | 1.00x | single run after the sweep |
| 16 | sync | 1000 | 138 | 113223 | 189205 | 206731 | 115097 | 2463.2 | 27→34 (+7) | 0 | 137 | 116192 | 1.01x | 0.99x | single run after the sweep |
| 16 | callback | 1000 | 80.1 | 196312 | 314769 | 375496 | 198880 | 2520.7 | 27→32 (+5) | 0 | 137 | 116192 | 0.58x | 1.71x | single run after the sweep |
| 16 | pipe | 1000 | 137 | 113446 | 193446 | 226840 | 116925 | 2612.0 | 27→31 (+4) | 0 | 137 | 116192 | 0.99x | 1.01x | single run after the sweep |

## headread4k

| conc | mode | n | ops/s | p50 µs | p99 µs | p999 µs | mean µs | CPU µs/op | threads | errors |
|---|---|---|---|---|---|---|---|---|---|---|
| 1 | sync | 257938 | 11336 | 85.2 | 133 | 191 | 88.1 | 46.0 | 559→559 (+0) | 0 |
| 1 | callback | 212778 | 9525 | 100 | 175 | 270 | 105 | 63.6 | 68→68 (+0) | 0 |
| 1 | pipe | 224970 | 9240 | 104 | 173 | 242 | 108 | 71.8 | 85→85 (+0) | 0 |
| 16 | sync | 1000000 | 43949 | 352 | 663 | 1314 | 364 | 39.2 | 559→559 (+0) | 0 |
| 16 | callback | 1000000 | 40919 | 389 | 588 | 758 | 391 | 53.6 | 68→68 (+0) | 0 |
| 16 | pipe | 1000000 | 42224 | 373 | 582 | 755 | 379 | 53.5 | 85→85 (+0) | 0 |

## headwrite4k

| conc | mode | n | ops/s | p50 µs | p99 µs | p999 µs | mean µs | CPU µs/op | threads | errors |
|---|---|---|---|---|---|---|---|---|---|---|
| 1 | sync | 7353 | 313 | 3362 | 10416 | 12459 | 3190 | 104.9 | 559→559 (+0) | 0 |
| 1 | callback | 7801 | 309 | 3393 | 10507 | 13566 | 3234 | 143.9 | 68→68 (+0) | 0 |
| 1 | pipe | 4011 | 173 | 5665 | 15101 | 18703 | 5781 | 145.7 | 85→85 (+0) | 0 |
| 16 | sync | 60474 | 2458 | 5874 | 23354 | 27932 | 6509 | 71.4 | 559→559 (+0) | 0 |
| 16 | callback | 61916 | 2543 | 5781 | 16470 | 43331 | 6290 | 76.8 | 68→68 (+0) | 0 |
| 16 | pipe | 31866 | 1350 | 10678 | 27594 | 48587 | 11847 | 86.8 | 85→85 (+0) | 0 |
| 64 | sync | 184881 | 7758 | 7493 | 23365 | 34973 | 8248 | 61.5 | 559→559 (+0) | 0 |
| 64 | callback | 195478 | 7425 | 7414 | 25340 | 75597 | 8619 | 65.6 | 68→68 (+0) | 0 |
| 64 | pipe | 154540 | 4685 | 13928 | 31688 | 65412 | 13659 | 69.6 | 85→85 (+0) | 0 |
| 256 | sync | 354318 | 9467 | 14959 | 36346 | 175911 | 27026 | 65.4 | 559→559 (+0) | 0 |
| 256 | callback | 435534 | 17369 | 14016 | 36217 | 67852 | 14735 | 64.5 | 68→68 (+0) | 0 |
| 256 | pipe | 133839 | 2917 | 90956 | 296931 | 409415 | 87703 | 75.2 | 85→85 (+0) | 0 |
| 512 | sync | 350935 | 16078 | 28547 | 79713 | 549250 | 31819 | 64.1 | 559→559 (+0) | 0 |
| 512 | callback | 144733 | 5485 | 95071 | 180151 | 262378 | 93233 | 63.2 | 68→89 (+21) | 0 |
| 512 | pipe | 79006 | 3117 | 162065 | 424365 | 829363 | 163790 | 76.5 | 85→109 (+24) | 0 |

## indexrtt

| conc | mode | n | ops/s | p50 µs | p99 µs | p999 µs | mean µs | CPU µs/op | threads | errors |
|---|---|---|---|---|---|---|---|---|---|---|
| 1 | sync | 4033 | 324 | 5847 | 14101 | 23579 | 6174 | 96.1 | 559→559 (+0) | 0 |
| 1 | callback | 3550 | 310 | 5952 | 14372 | 23963 | 6442 | 139.3 | 89→89 (+0) | 0 |
| 1 | pipe | 2014 | 171 | 11234 | 26524 | 31521 | 11694 | 141.2 | 109→109 (+0) | 0 |
| 16 | sync | 30607 | 2733 | 11234 | 23024 | 32653 | 11709 | 55.4 | 559→559 (+0) | 0 |
| 16 | callback | 32486 | 2457 | 11294 | 26922 | 34848 | 13020 | 58.7 | 89→89 (+0) | 0 |
| 16 | pipe | 17475 | 1527 | 20215 | 40937 | 49269 | 20944 | 65.8 | 109→109 (+0) | 0 |
| 64 | sync | 102618 | 10025 | 12110 | 27327 | 52786 | 12762 | 43.0 | 559→559 (+0) | 0 |
| 64 | callback | 122235 | 5905 | 22215 | 42283 | 62394 | 21676 | 47.7 | 89→89 (+0) | 0 |
| 64 | pipe | 68334 | 5588 | 21707 | 45272 | 55823 | 22903 | 50.4 | 109→109 (+0) | 0 |
| 256 | sync | 314420 | 24885 | 18981 | 42748 | 141815 | 20567 | 44.4 | 559→559 (+0) | 0 |
| 256 | callback | 180380 | 15399 | 31233 | 57402 | 108202 | 33226 | 44.4 | 89→89 (+0) | 0 |
| 256 | pipe | 197370 | 14950 | 32615 | 59986 | 74177 | 34227 | 50.9 | 109→109 (+0) | 0 |
| 512 | sync | 298411 | 25572 | 37982 | 66875 | 86446 | 40021 | 40.6 | 559→559 (+0) | 0 |
| 512 | callback | 172227 | 14861 | 66464 | 117725 | 165220 | 68809 | 43.4 | 89→89 (+0) | 0 |
| 512 | pipe | 181495 | 15200 | 63790 | 129200 | 264330 | 67308 | 52.2 | 109→145 (+36) | 0 |

## rados bench floor

| shape | op | size | conc | run | image | seconds | max objects | objects read | ops | elapsed s | ops/s | mean µs | MB/s |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| read4k | rand | 4096 | 1 | single | quay.io/ceph/ceph:v19.2.6 | 20 | — | 1 | 292610 | 20.00 | 14630 | 67.2 | 57.15 |
| read4k | rand | 4096 | 16 | single | quay.io/ceph/ceph:v19.2.6 | 20 | — | 16 | 1092645 | 20.00 | 54631 | 291 | 213.40 |
| read4k | rand | 4096 | 64 | single | quay.io/ceph/ceph:v19.2.6 | 20 | — | 64 | 1187651 | 20.00 | 59380 | 1075 | 231.95 |
| read4k | rand | 4096 | 256 | single | quay.io/ceph/ceph:v19.2.6 | 20 | — | 256 | 1217002 | 20.00 | 60837 | 4204 | 237.65 |
| read4k | rand | 4096 | 512 | single | quay.io/ceph/ceph:v19.2.6 | 20 | — | 512 | 1210291 | 20.01 | 60489 | 8459 | 236.29 |
| write4k | write | 4096 | 1 | single | quay.io/ceph/ceph:v19.2.6 | 20 | — | — | 6332 | 20.00 | 317 | 3157 | 1.24 |
| write4k | write | 4096 | 16 | single | quay.io/ceph/ceph:v19.2.6 | 20 | — | — | 53008 | 20.00 | 2650 | 6037 | 10.35 |
| write4k | write | 4096 | 64 | single | quay.io/ceph/ceph:v19.2.6 | 20 | — | — | 156137 | 20.01 | 7805 | 8199 | 30.49 |
| write4k | write | 4096 | 256 | single | quay.io/ceph/ceph:v19.2.6 | 20 | — | — | 325757 | 20.02 | 16275 | 15724 | 63.58 |
| write4k | write | 4096 | 512 | single | quay.io/ceph/ceph:v19.2.6 | 20 | — | — | 326591 | 20.03 | 16306 | 31378 | 63.69 |
| read4m | rand | 4194304 | 1 | single | quay.io/ceph/ceph:v19.2.6 | 20 | — | 1 | 6671 | 20.01 | 333 | 2997 | 1333.77 |
| read4m | rand | 4194304 | 4 | single | quay.io/ceph/ceph:v19.2.6 | 20 | — | 4 | 6719 | 20.00 | 336 | 11905 | 1343.52 |
| read4m | rand | 4194304 | 16 | single | quay.io/ceph/ceph:v19.2.6 | 20 | — | 16 | 8835 | 20.05 | 441 | 36259 | 1762.51 |
| write4m | write | 4194304 | 1 | single | quay.io/ceph/ceph:v19.2.6 | 600 | 1000 | — | 1000 | 9.59 | 104 | 9585 | 417.16 |
| write4m | write | 4194304 | 4 | single | quay.io/ceph/ceph:v19.2.6 | 600 | 1000 | — | 1000 | 7.31 | 137 | 29211 | 547.09 |
| write4m | write | 4194304 | 16 | single | quay.io/ceph/ceph:v19.2.6 | 600 | 1000 | — | 1000 | 7.27 | 137 | 116192 | 549.88 |

## Over the byte budget

Cells run past the byte budget on purpose, each in a process of its own. With the limiter's bounds above what the cell can reach, librados's objecter throttle blocks submitting threads inside C; with the bounds derived from that throttle, the limiter parks the excess submissions in Go. The threads criterion does not cover these cells.

| mode | shape | conc | limiter | n | ops/s | mean µs | threads | growth | allowance | limiter waits |
|---|---|---|---|---|---|---|---|---|---|---|
| callback | write4m | 64 | ops 1048576, bytes 1099511627776 | 640 | 136 | 451896 | 27→62 | +35 | 40 | 0 |
| callback | write4m | 64 | derived | 640 | 132 | 467525 | 27→46 | +19 | 40 | 617 |
| pipe | write4m | 64 | ops 1048576, bytes 1099511627776 | 640 | 119 | 519466 | 27→58 | +31 | 40 | 0 |
| pipe | write4m | 64 | derived | 640 | 136 | 451896 | 27→47 | +20 | 40 | 617 |

## CPU profiles

The profile samples every thread of the process. A sample taken while a Go thread runs C is recorded under the Go stack that called it, ending in runtime.cgocall, so the cgo share holds the C that Go calls ran, librados's submission included, as well as the crossing itself; that C is the second share. A sample of a thread the Go runtime does not run, librados's messenger and finisher threads among them, is recorded under runtime._ExternalCode; that is the last share, and outside the cgo share.

The columns between divide the profile by the frame nearest each sample's leaf, so no sample counts twice. (i), the submit crossing, is the cgo machinery around the C: runtime.cgocall's Go side, the _Cfunc_ stubs, the Pinner and the pointer checks, the completion handler's own calls into C included. (ii), the delivery, is how a completion reaches Go: the runtime.cgocallback* frames for callback, (*aioPipe).drain and the pipe read it waits in for pipe, and nothing for sync, whose wake happens in C. Neither counts the Go completion handler, rados.aioComplete, shown for reference: a pure-Go client runs one too. The boundary cost is (i) plus (ii).

| mode | profile | sampled | cgo frames | C under runtime.cgocall | (i) submit crossing | (ii) delivery | boundary cost | Go completion handler | threads outside Go | largest cgo frames, cumulative |
|---|---|---|---|---|---|---|---|---|---|---|
| sync | cpu-sync-read4k-256.pprof | 20.53s | 37.0% | 34.2% | 2.7% | 0.0% | 2.7% | 0.0% | 51.6% | runtime.cgocall 7.31s; github.com/ceph/go-ceph/rados._Cfunc_rados_read_op_operate 6.41s; github.com/ceph/go-ceph/rados._Cfunc_rados_read_op_read 370ms; github.com/ceph/go-ceph/rados._Cfunc_free 170ms; github.com/ceph/go-ceph/rados._Cfunc_CString 130ms |
| callback | cpu-callback-read4k-256.pprof | 1m9.02s | 25.6% | 16.3% | 4.7% | 0.1% | 4.8% | 4.4% | 43.0% | runtime.cgocall 12.02s; github.com/ceph/go-ceph/rados._Cfunc_rados_aio_read_op_operate 8.98s; runtime.cgocallback 3.71s; runtime.cgocallbackg 3.64s; runtime.cgocallbackg1 3.61s |
| pipe | cpu-pipe-read4k-256.pprof | 1m8.21s | 18.8% | 14.3% | 4.5% | 4.7% | 9.2% | 3.1% | 40.9% | runtime.cgocall 10.35s; github.com/ceph/go-ceph/rados._Cfunc_rados_aio_read_op_operate 7.56s; runtime.(*Pinner).Pin 1.24s; github.com/ceph/go-ceph/rados._Cfunc_rados_read_op_read 860ms; runtime.(*Pinner).Unpin 540ms |
