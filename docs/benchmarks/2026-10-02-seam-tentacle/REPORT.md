# Seam microbenchmark: tentacle

| env.json | |
|---|---|
| bench_time | 20s |
| ceph_version | 20.2.4 |
| cluster_image | quay.io/ceph/ceph:v20.2.4 |
| conc | 1,16,64,256,512 |
| cpu | AMD Ryzen 9 7950X3D 16-Core Processor |
| date | 2026-10-02T04:29:34Z |
| floor_image | quay.io/ceph/ceph:v19.2.6 |
| floor_image_version | 19.2.6 |
| floor_no_hints | true |
| floor_seconds | 20 |
| git_describe | 35c9247 |
| go_version | go version go1.27.1 linux/amd64 |
| gomaxprocs | unset |
| host_librados | 19.2.6-1.fc43 |
| kernel | 7.2.6-100.fc43.x86_64 |
| large_conc | 1,4,16 |
| librados_path | /usr/lib64/librados.so.2.0.0 |
| librados_version | 19.2.6 |
| load | 1.14 2.18 2.99 |
| mem_total_kib | 65007132 |
| nproc | 32 |
| objecter_inflight_op_bytes | 104857600 |
| objecter_inflight_ops | 1024 |
| over_objects | 640 |
| pool_max_avail | 8450596864 |
| release | tentacle |
| rooket_version | rooket v0.0.0-20260928053658-3d8551957e08 |
| write4m_objects | 1000 |

The judged floor is rados bench from quay.io/ceph/ceph:v19.2.6, Ceph 19.2.6, the release of the librados the benchmark links. The cluster runs quay.io/ceph/ceph:v20.2.4; its own rados bench ran once more at each judged cell, shown beside the judged floor and not judged. The floor's writes pass --no-hints, so rados bench sends no allocation hints, as the Go cells send none.

## Verdict

cgo is not a bottleneck on a release when one of the sync, callback and pipe modes meets both criteria there: throughput and mean latency, which compare read4k and write4k at 64 and 256 in flight, and write4m at 1, 4 and 16, with rados bench at the same size and concurrency, run just before and just after the cell's three modes: a ratio divides by the mean of the two runs, and a cell whose runs differ by more than 10% of that mean is flagged unstable beside the verdict it feeds. read4m is measured and shown with its floor, but not judged; its table says why. The rows below the criteria are measurements, compared with no threshold. CPU per operation is the CPU each operation of read4k at 256 in flight used; among the modes that meet both criteria, the one using the least is preferred, and every cell's CPU is in the tables below. The boundary cost is the part of that cell's CPU profile a pure-Go client would no longer pay: the submit crossing and the delivery, not the C librados runs on the Go thread nor the Go completion handler. Thread growth is each mode's largest, its peak less its idle count, across the cells under the byte budget. The cgo frames row counts every sample whose stack holds a frame of the cgo boundary, cgo's runtime.cgoCheck* pointer checks included; it counts the C librados runs on the Go thread, which a pure-Go client would run too, as a cost of the boundary. For pipe the boundary cost leaves out the C side of each completion, one write(2) to the pipe on a librados thread, which the profile records under runtime._ExternalCode with no frame to tell it by: it is unmeasured, and pipe's boundary cost is low by that much.

| criterion | sync | callback | pipe |
|---|---|---|---|
| throughput>=0.8x floor | PASS: worst 0.94x (write4m at 1), margin +0.14; unstable floor: read4k at 64 (15.1%), read4k at 256 (18.9%), write4k at 256 (10.8%), write4m at 4 (25.2%) | PASS: worst 0.97x (write4m at 1), margin +0.17; unstable floor: read4k at 64 (15.1%), read4k at 256 (18.9%), write4k at 256 (10.8%), write4m at 4 (25.2%) | PASS: worst 0.94x (write4m at 1), margin +0.14; unstable floor: read4k at 64 (15.1%), read4k at 256 (18.9%), write4k at 256 (10.8%), write4m at 4 (25.2%) |
| mean latency<=1.25x floor | PASS: worst 1.06x (write4m at 1), margin +0.19; unstable floor: read4k at 64 (15.2%), read4k at 256 (18.9%), write4k at 256 (10.7%), write4m at 4 (25.2%) | PASS: worst 1.03x (write4m at 1), margin +0.22; unstable floor: read4k at 64 (15.2%), read4k at 256 (18.9%), write4k at 256 (10.7%), write4m at 4 (25.2%) | PASS: worst 1.07x (write4m at 1), margin +0.18; unstable floor: read4k at 64 (15.2%), read4k at 256 (18.9%), write4k at 256 (10.7%), write4m at 4 (25.2%) |
| CPU per operation, read4k at 256 (not judged) | 24 µs | 27 µs | 34 µs |
| boundary cost, read4k at 256 (not judged) | 2.8% of 44.23s sampled, about 0.7 µs of the operation's 24 | 5.1% of 26.3s sampled, about 1.4 µs of the operation's 27 | 8.8% of 28.88s sampled, about 3.0 µs of the operation's 34; leaves out pipe's write(2) per completion, unmeasured |
| thread growth (not judged) | largest +532 (write4k at 512) | largest +35 (read4k at 512) | largest +99 (headwrite4k at 512) |
| cgo frames, share of CPU (not judged) | 37.2% of 44.23s sampled | 24.3% of 26.3s sampled | 18.2% of 28.88s sampled |

**Answer:** sync, callback and pipe meet both criteria on tentacle.

**Least CPU per operation among them**, read4k at 256 in flight: sync, 24 µs.

In-flight limits of the cells: derived. No submission parked on the limiter.

Thread counts per benchmark process, the count before its first cell to its highest:

| mode | benchmark processes | largest growth in one process | highest count |
|---|---|---|---|
| sync | 28 | 27→559 (+532) | 559 |
| callback | 28 | 27→62 (+35) | 62 |
| pipe | 28 | 27→126 (+99) | 126 |

## read4k

| conc | mode | n | ops/s | p50 µs | p99 µs | p999 µs | mean µs | CPU µs/op | threads | errors | floor ops/s | floor mean µs | ops ÷ floor | mean ÷ floor | floor runs | cluster image floor ops/s, mean µs |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| 1 | sync | 389580 | 14041 | 68.5 | 107 | 154 | 71.2 | 36.1 | 27→29 (+2) | 0 | 14197 | 69.3 | 0.99x | 1.03x | single run | — |
| 1 | callback | 283254 | 12707 | 75.2 | 124 | 163 | 78.6 | 45.9 | 27→28 (+1) | 0 | 14197 | 69.3 | 0.90x | 1.13x | single run | — |
| 1 | pipe | 277476 | 12063 | 79.6 | 132 | 167 | 82.8 | 53.3 | 27→29 (+2) | 0 | 14197 | 69.3 | 0.85x | 1.20x | single run | — |
| 16 | sync | 1000000 | 49064 | 323 | 448 | 542 | 326 | 24.9 | 27→44 (+17) | 0 | 59121 | 268 | 0.83x | 1.22x | single run | — |
| 16 | callback | 1365740 | 57171 | 279 | 393 | 472 | 280 | 29.3 | 26→32 (+6) | 0 | 59121 | 268 | 0.97x | 1.04x | single run | — |
| 16 | pipe | 1000000 | 48462 | 325 | 486 | 579 | 330 | 37.6 | 27→33 (+6) | 0 | 59121 | 268 | 0.82x | 1.23x | single run | — |
| 64 | sync | 1514265 | 63074 | 1006 | 1275 | 1428 | 1014 | 20.3 | 27→94 (+67) | 0 | 55369 / 64440 | 1153 / 990 | 1.05x | 0.95x | unstable: ops 15.1%, mean 15.2% | 69504, 919 |
| 64 | callback | 1444105 | 60459 | 1060 | 1311 | 1488 | 1058 | 27.3 | 27→33 (+6) | 0 | 55369 / 64440 | 1153 / 990 | 1.01x | 0.99x | unstable: ops 15.1%, mean 15.2% | 69504, 919 |
| 64 | pipe | 1439982 | 59927 | 1069 | 1324 | 1506 | 1068 | 31.1 | 27→32 (+5) | 0 | 55369 / 64440 | 1153 / 990 | 1.00x | 1.00x | unstable: ops 15.1%, mean 15.2% | 69504, 919 |
| 256 | sync | 1450137 | 63708 | 3947 | 5171 | 6226 | 4015 | 23.7 | 27→301 (+274) | 0 | 55710 / 67316 | 4592 / 3799 | 1.04x | 0.96x | unstable: ops 18.9%, mean 18.9% | 63067, 4057 |
| 256 | callback | 1467393 | 61107 | 4182 | 5042 | 5503 | 4189 | 27.1 | 26→41 (+15) | 0 | 55710 / 67316 | 4592 / 3799 | 0.99x | 1.00x | unstable: ops 18.9%, mean 18.9% | 63067, 4057 |
| 256 | pipe | 1377711 | 59443 | 4261 | 5660 | 6178 | 4306 | 34.2 | 27→58 (+31) | 0 | 55710 / 67316 | 4592 / 3799 | 0.97x | 1.03x | unstable: ops 18.9%, mean 18.9% | 63067, 4057 |
| 512 | sync | 1466559 | 63091 | 7996 | 10822 | 12152 | 8113 | 25.4 | 28→559 (+531) | 0 | 67324 | 7600 | 0.94x | 1.07x | single run | — |
| 512 | callback | 1465443 | 61134 | 8351 | 10027 | 10642 | 8374 | 27.5 | 27→62 (+35) | 0 | 67324 | 7600 | 0.91x | 1.10x | single run | — |
| 512 | pipe | 1462422 | 60803 | 8370 | 10343 | 12017 | 8418 | 33.8 | 27→82 (+55) | 0 | 67324 | 7600 | 0.90x | 1.11x | single run | — |

## write4k

| conc | mode | n | ops/s | p50 µs | p99 µs | p999 µs | mean µs | CPU µs/op | threads | errors | floor ops/s | floor mean µs | ops ÷ floor | mean ÷ floor | floor runs | cluster image floor ops/s, mean µs |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| 1 | sync | 14193 | 542 | 1373 | 5575 | 7966 | 1844 | 64.5 | 27→29 (+2) | 0 | 524 | 1908 | 1.04x | 0.97x | single run | — |
| 1 | callback | 14211 | 590 | 1359 | 5020 | 6777 | 1696 | 85.3 | 27→28 (+1) | 0 | 524 | 1908 | 1.13x | 0.89x | single run | — |
| 1 | pipe | 12399 | 557 | 1371 | 5715 | 7524 | 1796 | 85.0 | 27→28 (+1) | 0 | 524 | 1908 | 1.06x | 0.94x | single run | — |
| 16 | sync | 94729 | 3725 | 3746 | 11790 | 19256 | 4295 | 41.5 | 27→45 (+18) | 0 | 4118 | 3885 | 0.90x | 1.11x | single run | — |
| 16 | callback | 77934 | 3726 | 3744 | 11269 | 25214 | 4292 | 43.8 | 27→34 (+7) | 0 | 4118 | 3885 | 0.90x | 1.10x | single run | — |
| 16 | pipe | 100354 | 4097 | 3450 | 9623 | 17947 | 3905 | 43.3 | 27→31 (+4) | 0 | 4118 | 3885 | 1.00x | 1.01x | single run | — |
| 64 | sync | 212935 | 9526 | 6301 | 19820 | 62389 | 6718 | 32.6 | 27→94 (+67) | 0 | 9342 / 9826 | 6850 / 6512 | 0.99x | 1.01x | ops 5.1%, mean 5.1% | 9531, 6713 |
| 64 | callback | 242763 | 9969 | 6290 | 12602 | 46405 | 6419 | 34.4 | 27→37 (+10) | 0 | 9342 / 9826 | 6850 / 6512 | 1.04x | 0.96x | ops 5.1%, mean 5.1% | 9531, 6713 |
| 64 | pipe | 273080 | 9592 | 6393 | 14036 | 52674 | 6672 | 38.0 | 27→36 (+9) | 0 | 9342 / 9826 | 6850 / 6512 | 1.00x | 1.00x | ops 5.1%, mean 5.1% | 9531, 6713 |
| 256 | sync | 101128 | 4683 | 60073 | 139677 | 305561 | 54643 | 31.7 | 27→290 (+263) | 0 | 3859 / 4301 | 66244 / 59504 | 1.15x | 0.87x | unstable: ops 10.8%, mean 10.7% | 4393, 58256 |
| 256 | callback | 112290 | 4754 | 54179 | 243559 | 663280 | 53823 | 37.3 | 27→58 (+31) | 0 | 3859 / 4301 | 66244 / 59504 | 1.17x | 0.86x | unstable: ops 10.8%, mean 10.7% | 4393, 58256 |
| 256 | pipe | 173997 | 4692 | 54522 | 422671 | 680510 | 54553 | 36.7 | 27→61 (+34) | 0 | 3859 / 4301 | 66244 / 59504 | 1.15x | 0.87x | unstable: ops 10.8%, mean 10.7% | 4393, 58256 |
| 512 | sync | 181441 | 5238 | 93863 | 314534 | 1362066 | 97645 | 35.2 | 27→559 (+532) | 0 | 5221 | 97826 | 1.00x | 1.00x | single run | — |
| 512 | callback | 114979 | 4602 | 109594 | 355905 | 703035 | 111100 | 38.0 | 27→51 (+24) | 0 | 5221 | 97826 | 0.88x | 1.14x | single run | — |
| 512 | pipe | 169941 | 5241 | 97621 | 414596 | 884643 | 97576 | 33.2 | 28→72 (+44) | 0 | 5221 | 97826 | 1.00x | 1.00x | single run | — |

## read4m

Not judged: rados bench's 4 MiB rand does not read as the Go cell does: rand picks each object at random with replacement among the objects written for it, where each Go worker reads its own, and its fixed object names land on fixed placement groups. Its ratios are shown, but the throughput and latency criteria leave read4m out until the floor reads as the Go cell does.

| conc | mode | n | ops/s | p50 µs | p99 µs | p999 µs | mean µs | CPU µs/op | threads | errors | floor ops/s | floor mean µs | ops ÷ floor | mean ÷ floor | floor runs | cluster image floor ops/s, mean µs |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| 1 | sync | 4492 | 203 | 4941 | 6027 | 6873 | 4936 | 803.0 | 27→29 (+2) | 0 | 193 / 227 | 5166 / 4405 | 0.96x | 1.03x | unstable: ops 15.9%, mean 15.9% | 235, 4244 |
| 1 | callback | 4770 | 205 | 4741 | 6061 | 6724 | 4868 | 907.9 | 26→28 (+2) | 0 | 193 / 227 | 5166 / 4405 | 0.98x | 1.02x | unstable: ops 15.9%, mean 15.9% | 235, 4244 |
| 1 | pipe | 5557 | 232 | 4157 | 5154 | 5424 | 4309 | 882.9 | 27→29 (+2) | 0 | 193 / 227 | 5166 / 4405 | 1.10x | 0.90x | unstable: ops 15.9%, mean 15.9% | 235, 4244 |
| 4 | sync | 9304 | 381 | 10470 | 16337 | 31995 | 10485 | 815.8 | 27→30 (+3) | 0 | 255 / 255 | 15651 / 15651 | 1.49x | 0.67x | ops 0.0%, mean 0.0% | 256, 15589 |
| 4 | callback | 8310 | 370 | 10795 | 16453 | 25911 | 10808 | 873.7 | 27→29 (+2) | 0 | 255 / 255 | 15651 / 15651 | 1.45x | 0.69x | ops 0.0%, mean 0.0% | 256, 15589 |
| 4 | pipe | 8382 | 384 | 10391 | 10982 | 17853 | 10402 | 749.3 | 27→29 (+2) | 0 | 255 / 255 | 15651 / 15651 | 1.50x | 0.66x | ops 0.0%, mean 0.0% | 256, 15589 |
| 16 | sync | 6117 | 294 | 54703 | 80247 | 248858 | 54362 | 915.2 | 27→42 (+15) | 0 | 317 / 381 | 50352 / 41878 | 0.84x | 1.18x | unstable: ops 18.3%, mean 18.4% | 355, 45088 |
| 16 | callback | 5838 | 260 | 60556 | 86796 | 267204 | 61352 | 918.4 | 26→40 (+14) | 0 | 317 / 381 | 50352 / 41878 | 0.75x | 1.33x | unstable: ops 18.3%, mean 18.4% | 355, 45088 |
| 16 | pipe | 11978 | 535 | 28585 | 70533 | 239136 | 29913 | 754.3 | 27→39 (+12) | 0 | 317 / 381 | 50352 / 41878 | 1.53x | 0.65x | unstable: ops 18.3%, mean 18.4% | 355, 45088 |

## write4m

| conc | mode | n | ops/s | p50 µs | p99 µs | p999 µs | mean µs | CPU µs/op | threads | errors | floor ops/s | floor mean µs | ops ÷ floor | mean ÷ floor | floor runs | cluster image floor ops/s, mean µs |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| 1 | sync | 1000 | 112 | 8612 | 16412 | 25186 | 8910 | 1004.2 | 27→28 (+1) | 0 | 118 / 121 | 8493 / 8276 | 0.94x | 1.06x | ops 2.6%, mean 2.6% | 120, 8333 |
| 1 | callback | 1000 | 116 | 8187 | 18259 | 22654 | 8637 | 964.0 | 26→27 (+1) | 0 | 118 / 121 | 8493 / 8276 | 0.97x | 1.03x | ops 2.6%, mean 2.6% | 120, 8333 |
| 1 | pipe | 1000 | 112 | 8773 | 16556 | 21095 | 8967 | 884.8 | 27→27 (+0) | 0 | 118 / 121 | 8493 / 8276 | 0.94x | 1.07x | ops 2.6%, mean 2.6% | 120, 8333 |
| 4 | sync | 1000 | 159 | 24100 | 45615 | 62060 | 25089 | 1038.4 | 27→29 (+2) | 0 | 128 / 166 | 31093 / 24126 | 1.08x | 0.91x | unstable: ops 25.2%, mean 25.2% | 162, 24630 |
| 4 | callback | 1000 | 162 | 23817 | 46280 | 65256 | 24708 | 1242.2 | 27→28 (+1) | 0 | 128 / 166 | 31093 / 24126 | 1.10x | 0.89x | unstable: ops 25.2%, mean 25.2% | 162, 24630 |
| 4 | pipe | 1000 | 163 | 23855 | 43949 | 54018 | 24550 | 1071.4 | 27→27 (+0) | 0 | 128 / 166 | 31093 / 24126 | 1.11x | 0.89x | unstable: ops 25.2%, mean 25.2% | 162, 24630 |
| 16 | sync | 1000 | 160 | 101394 | 169914 | 181947 | 99988 | 2457.5 | 27→36 (+9) | 0 | 162 / 162 | 98517 / 98386 | 0.98x | 1.02x | ops 0.0%, mean 0.1% | 135, 118318 |
| 16 | callback | 1000 | 159 | 100502 | 163101 | 187878 | 100388 | 2573.6 | 27→31 (+4) | 0 | 162 / 162 | 98517 / 98386 | 0.98x | 1.02x | ops 0.0%, mean 0.1% | 135, 118318 |
| 16 | pipe | 1000 | 161 | 95758 | 156484 | 174351 | 98586 | 2504.4 | 27→31 (+4) | 0 | 162 / 162 | 98517 / 98386 | 1.00x | 1.00x | ops 0.0%, mean 0.1% | 135, 118318 |

## headread4k

| conc | mode | n | ops/s | p50 µs | p99 µs | p999 µs | mean µs | CPU µs/op | threads | errors |
|---|---|---|---|---|---|---|---|---|---|---|
| 1 | sync | 234660 | 11077 | 86.5 | 147 | 199 | 90.2 | 46.5 | 27→30 (+3) | 0 |
| 1 | callback | 252494 | 9840 | 97.1 | 161 | 220 | 102 | 59.6 | 27→29 (+2) | 0 |
| 1 | pipe | 193590 | 9405 | 101 | 166 | 220 | 106 | 67.9 | 27→29 (+2) | 0 |
| 16 | sync | 956564 | 35921 | 441 | 597 | 707 | 445 | 46.4 | 27→50 (+23) | 0 |
| 16 | callback | 890895 | 39986 | 405 | 587 | 720 | 400 | 52.9 | 27→44 (+17) | 0 |
| 16 | pipe | 1000000 | 47998 | 330 | 475 | 621 | 333 | 46.7 | 27→36 (+9) | 0 |

## headwrite4k

| conc | mode | n | ops/s | p50 µs | p99 µs | p999 µs | mean µs | CPU µs/op | threads | errors |
|---|---|---|---|---|---|---|---|---|---|---|
| 1 | sync | 12579 | 545 | 1388 | 5758 | 12109 | 1834 | 80.6 | 26→29 (+3) | 0 |
| 1 | callback | 6340 | 288 | 3404 | 9434 | 13553 | 3466 | 144.2 | 27→27 (+0) | 0 |
| 1 | pipe | 8940 | 309 | 3260 | 9274 | 13490 | 3241 | 129.8 | 27→28 (+1) | 0 |
| 16 | sync | 67663 | 2583 | 5290 | 17079 | 29663 | 6195 | 72.0 | 27→47 (+20) | 0 |
| 16 | callback | 76467 | 2756 | 4987 | 15606 | 28893 | 5805 | 80.7 | 27→35 (+8) | 0 |
| 16 | pipe | 96303 | 4017 | 3456 | 9552 | 16593 | 3983 | 80.7 | 27→34 (+7) | 0 |
| 64 | sync | 225534 | 9029 | 6641 | 14594 | 37998 | 7087 | 60.5 | 28→98 (+70) | 0 |
| 64 | callback | 244942 | 9877 | 6361 | 12383 | 40857 | 6479 | 65.1 | 27→44 (+17) | 0 |
| 64 | pipe | 243847 | 9056 | 6495 | 16121 | 151873 | 7067 | 68.2 | 27→49 (+22) | 0 |
| 256 | sync | 416007 | 4871 | 51017 | 168723 | 603905 | 52544 | 61.1 | 27→297 (+270) | 0 |
| 256 | callback | 184322 | 3921 | 62724 | 266293 | 547332 | 65279 | 67.2 | 27→59 (+32) | 0 |
| 256 | pipe | 113005 | 4363 | 61893 | 145019 | 341540 | 58677 | 74.8 | 27→57 (+30) | 0 |
| 512 | sync | 126560 | 4808 | 108321 | 247049 | 530208 | 106258 | 62.8 | 28→556 (+528) | 0 |
| 512 | callback | 236984 | 4444 | 109399 | 593397 | 1100674 | 115122 | 71.3 | 27→60 (+33) | 0 |
| 512 | pipe | 147195 | 4694 | 110291 | 272123 | 550335 | 108901 | 77.1 | 27→126 (+99) | 0 |

## indexrtt

| conc | mode | n | ops/s | p50 µs | p99 µs | p999 µs | mean µs | CPU µs/op | threads | errors |
|---|---|---|---|---|---|---|---|---|---|---|
| 1 | sync | 7228 | 605 | 3420 | 7599 | 13010 | 3307 | 72.6 | 27→29 (+2) | 0 |
| 1 | callback | 6201 | 531 | 3533 | 10520 | 14775 | 3769 | 97.0 | 27→28 (+1) | 0 |
| 1 | pipe | 7640 | 580 | 3480 | 9094 | 13160 | 3448 | 94.6 | 27→29 (+2) | 0 |
| 16 | sync | 64578 | 5281 | 5730 | 16378 | 23580 | 6057 | 47.1 | 26→45 (+19) | 0 |
| 16 | callback | 65090 | 5318 | 5741 | 13989 | 21079 | 6016 | 50.9 | 27→31 (+4) | 0 |
| 16 | pipe | 61086 | 5130 | 5774 | 15003 | 21284 | 6237 | 54.9 | 27→33 (+6) | 0 |
| 64 | sync | 226844 | 18053 | 6408 | 17617 | 29077 | 7089 | 36.7 | 27→96 (+69) | 0 |
| 64 | callback | 225837 | 18457 | 6440 | 16246 | 22909 | 6934 | 41.8 | 27→41 (+14) | 0 |
| 64 | pipe | 201633 | 17607 | 6547 | 17521 | 44366 | 7268 | 48.4 | 27→40 (+13) | 0 |
| 256 | sync | 469706 | 40998 | 11943 | 24298 | 48717 | 12486 | 30.6 | 27→290 (+263) | 0 |
| 256 | callback | 508468 | 40277 | 12124 | 24399 | 76198 | 12710 | 37.5 | 27→52 (+25) | 0 |
| 256 | pipe | 487465 | 37204 | 13041 | 29342 | 68868 | 13760 | 42.8 | 27→59 (+32) | 0 |
| 512 | sync | 446113 | 41689 | 23203 | 46374 | 76568 | 24550 | 31.5 | 27→554 (+527) | 0 |
| 512 | callback | 442797 | 38952 | 24716 | 47626 | 117940 | 26280 | 35.7 | 27→50 (+23) | 0 |
| 512 | pipe | 487236 | 42249 | 22980 | 47289 | 79251 | 24230 | 38.2 | 27→81 (+54) | 0 |

## rados bench floor

| shape | op | size | conc | run | image | seconds | max objects | objects read | ops | elapsed s | ops/s | mean µs | MB/s |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| read4k | rand | 4096 | 1 | single | quay.io/ceph/ceph:v19.2.6 | 20 | — | 1 | 283945 | 20.00 | 14197 | 69.3 | 55.46 |
| read4k | rand | 4096 | 16 | single | quay.io/ceph/ceph:v19.2.6 | 20 | — | 16 | 1182426 | 20.00 | 59121 | 268 | 230.94 |
| read4k | rand | 4096 | 64 | before | quay.io/ceph/ceph:v19.2.6 | 20 | — | 64 | 1107431 | 20.00 | 55369 | 1153 | 216.29 |
| read4k | rand | 4096 | 64 | after | quay.io/ceph/ceph:v19.2.6 | 20 | — | 64 | 1288866 | 20.00 | 64440 | 990 | 251.72 |
| read4k | rand | 4096 | 64 | single | quay.io/ceph/ceph:v20.2.4 | 20 | — | 64 | 1390165 | 20.00 | 69504 | 919 | 271.50 |
| read4k | rand | 4096 | 256 | before | quay.io/ceph/ceph:v19.2.6 | 20 | — | 256 | 1114406 | 20.00 | 55710 | 4592 | 217.62 |
| read4k | rand | 4096 | 256 | after | quay.io/ceph/ceph:v19.2.6 | 20 | — | 256 | 1346577 | 20.00 | 67316 | 3799 | 262.95 |
| read4k | rand | 4096 | 256 | single | quay.io/ceph/ceph:v20.2.4 | 20 | — | 256 | 1261565 | 20.00 | 63067 | 4057 | 246.36 |
| read4k | rand | 4096 | 512 | single | quay.io/ceph/ceph:v19.2.6 | 20 | — | 512 | 1347054 | 20.01 | 67324 | 7600 | 262.98 |
| write4k | write | 4096 | 1 | single | quay.io/ceph/ceph:v19.2.6 | 20 | — | — | 10474 | 20.00 | 524 | 1908 | 2.05 |
| write4k | write | 4096 | 16 | single | quay.io/ceph/ceph:v19.2.6 | 20 | — | — | 82365 | 20.00 | 4118 | 3885 | 16.08 |
| write4k | write | 4096 | 64 | before | quay.io/ceph/ceph:v19.2.6 | 20 | — | — | 186859 | 20.00 | 9342 | 6850 | 36.49 |
| write4k | write | 4096 | 64 | after | quay.io/ceph/ceph:v19.2.6 | 20 | — | — | 196560 | 20.00 | 9826 | 6512 | 38.38 |
| write4k | write | 4096 | 64 | single | quay.io/ceph/ceph:v20.2.4 | 20 | — | — | 190683 | 20.01 | 9531 | 6713 | 37.23 |
| write4k | write | 4096 | 256 | before | quay.io/ceph/ceph:v19.2.6 | 20 | — | — | 77568 | 20.10 | 3859 | 66244 | 15.08 |
| write4k | write | 4096 | 256 | after | quay.io/ceph/ceph:v19.2.6 | 20 | — | — | 86232 | 20.05 | 4301 | 59504 | 16.80 |
| write4k | write | 4096 | 256 | single | quay.io/ceph/ceph:v20.2.4 | 20 | — | — | 88071 | 20.05 | 4393 | 58256 | 17.16 |
| write4k | write | 4096 | 512 | single | quay.io/ceph/ceph:v19.2.6 | 20 | — | — | 104893 | 20.09 | 5221 | 97826 | 20.39 |
| read4m | rand | 4194304 | 1 | before | quay.io/ceph/ceph:v19.2.6 | 20 | — | 1 | 3870 | 20.00 | 193 | 5166 | 773.84 |
| read4m | rand | 4194304 | 1 | after | quay.io/ceph/ceph:v19.2.6 | 20 | — | 1 | 4538 | 20.00 | 227 | 4405 | 907.57 |
| read4m | rand | 4194304 | 1 | single | quay.io/ceph/ceph:v20.2.4 | 20 | — | 1 | 4710 | 20.00 | 235 | 4244 | 941.87 |
| read4m | rand | 4194304 | 4 | before | quay.io/ceph/ceph:v19.2.6 | 20 | — | 4 | 5112 | 20.01 | 255 | 15651 | 1021.88 |
| read4m | rand | 4194304 | 4 | after | quay.io/ceph/ceph:v19.2.6 | 20 | — | 4 | 5112 | 20.01 | 255 | 15651 | 1021.92 |
| read4m | rand | 4194304 | 4 | single | quay.io/ceph/ceph:v20.2.4 | 20 | — | 4 | 5133 | 20.01 | 256 | 15589 | 1025.90 |
| read4m | rand | 4194304 | 16 | before | quay.io/ceph/ceph:v19.2.6 | 20 | — | 16 | 6362 | 20.04 | 317 | 50352 | 1269.69 |
| read4m | rand | 4194304 | 16 | after | quay.io/ceph/ceph:v19.2.6 | 20 | — | 16 | 7653 | 20.08 | 381 | 41878 | 1524.73 |
| read4m | rand | 4194304 | 16 | single | quay.io/ceph/ceph:v20.2.4 | 20 | — | 16 | 7102 | 20.03 | 355 | 45088 | 1418.35 |
| write4m | write | 4194304 | 1 | before | quay.io/ceph/ceph:v19.2.6 | 600 | 1000 | — | 1000 | 8.49 | 118 | 8493 | 470.87 |
| write4m | write | 4194304 | 1 | after | quay.io/ceph/ceph:v19.2.6 | 600 | 1000 | — | 1000 | 8.28 | 121 | 8276 | 483.19 |
| write4m | write | 4194304 | 1 | single | quay.io/ceph/ceph:v20.2.4 | 600 | 1000 | — | 1000 | 8.34 | 120 | 8333 | 479.89 |
| write4m | write | 4194304 | 4 | before | quay.io/ceph/ceph:v19.2.6 | 600 | 1000 | — | 1000 | 7.78 | 128 | 31093 | 513.84 |
| write4m | write | 4194304 | 4 | after | quay.io/ceph/ceph:v19.2.6 | 600 | 1000 | — | 1000 | 6.04 | 166 | 24126 | 662.26 |
| write4m | write | 4194304 | 4 | single | quay.io/ceph/ceph:v20.2.4 | 600 | 1000 | — | 1000 | 6.16 | 162 | 24630 | 648.98 |
| write4m | write | 4194304 | 16 | before | quay.io/ceph/ceph:v19.2.6 | 600 | 1000 | — | 1000 | 6.17 | 162 | 98517 | 648.77 |
| write4m | write | 4194304 | 16 | after | quay.io/ceph/ceph:v19.2.6 | 600 | 1000 | — | 1000 | 6.17 | 162 | 98386 | 648.49 |
| write4m | write | 4194304 | 16 | single | quay.io/ceph/ceph:v20.2.4 | 600 | 1000 | — | 1000 | 7.41 | 135 | 118318 | 539.92 |

## Over the byte budget

Cells run past the byte budget on purpose, each in a process of its own. With the limiter's bounds above what the cell can reach, librados's objecter throttle blocks submitting threads inside C; with the bounds derived from that throttle, the limiter parks the excess submissions in Go.

| mode | shape | conc | limiter | n | ops/s | mean µs | threads | growth | limiter waits |
|---|---|---|---|---|---|---|---|---|---|
| callback | write4m | 64 | ops 1048576, bytes 1099511627776 | 640 | 159 | 388227 | 26→61 | +35 | 0 |
| callback | write4m | 64 | derived | 640 | 160 | 384992 | 27→46 | +19 | 617 |
| pipe | write4m | 64 | ops 1048576, bytes 1099511627776 | 640 | 159 | 385459 | 27→62 | +35 | 0 |
| pipe | write4m | 64 | derived | 640 | 165 | 373692 | 27→46 | +19 | 617 |

## CPU profiles

The profile samples every thread of the process. A sample taken while a Go thread runs C is recorded under the Go stack that called it, ending in runtime.cgocall, so the cgo share holds the C that Go calls ran, librados's submission included, as well as the crossing itself; that C is the second share. A sample of a thread the Go runtime does not run, librados's messenger and finisher threads among them, is recorded under runtime._ExternalCode; that is the last share, and outside the cgo share.

The columns between divide the profile by the frame nearest each sample's leaf, so no sample counts twice. (i), the submit crossing, is the cgo machinery around the C: runtime.cgocall's Go side, the _Cfunc_ stubs, the Pinner and the pointer checks, the completion handler's own calls into C included. (ii), the delivery, is how a completion reaches Go: the runtime.cgocallback* frames for callback, (*aioPipe).drain and the pipe read it waits in for pipe, and nothing for sync, whose wake happens in C. Neither counts the Go completion handler, rados.aioComplete, shown for reference: a pure-Go client runs one too. The boundary cost is (i) plus (ii).

| mode | profile | sampled | cgo frames | C under runtime.cgocall | (i) submit crossing | (ii) delivery | boundary cost | Go completion handler | threads outside Go | largest cgo frames, cumulative |
|---|---|---|---|---|---|---|---|---|---|---|
| sync | cpu-sync-read4k-256.pprof | 44.23s | 37.2% | 34.4% | 2.8% | 0.0% | 2.8% | 0.0% | 50.2% | runtime.cgocall 15.89s; github.com/ceph/go-ceph/rados._Cfunc_rados_read_op_operate 13.97s; github.com/ceph/go-ceph/rados._Cfunc_rados_read_op_read 760ms; github.com/ceph/go-ceph/rados._Cfunc_free 280ms; github.com/ceph/go-ceph/rados._Cfunc_rados_release_read_op 280ms |
| callback | cpu-callback-read4k-256.pprof | 26.3s | 24.3% | 15.0% | 5.0% | 0.1% | 5.1% | 4.1% | 43.5% | runtime.cgocall 4.32s; github.com/ceph/go-ceph/rados._Cfunc_rados_aio_read_op_operate 3.09s; runtime.cgocallback 1.24s; runtime.cgocallbackg 1.22s; runtime.cgocallbackg1 1.21s |
| pipe | cpu-pipe-read4k-256.pprof | 28.88s | 18.2% | 13.9% | 4.4% | 4.5% | 8.8% | 3.2% | 41.3% | runtime.cgocall 4.24s; github.com/ceph/go-ceph/rados._Cfunc_rados_aio_read_op_operate 3.04s; runtime.(*Pinner).Pin 510ms; runtime.(*Pinner).Unpin 310ms; github.com/ceph/go-ceph/rados._Cfunc_rados_read_op_read 270ms |
