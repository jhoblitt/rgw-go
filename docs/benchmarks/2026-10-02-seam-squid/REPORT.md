# Seam microbenchmark: squid

| env.json | |
|---|---|
| bench_time | 20s |
| ceph_version | 19.2.6 |
| cluster_image | quay.io/ceph/ceph:v19.2.6 |
| conc | 1,16,64,256,512 |
| cpu | AMD Ryzen 9 7950X3D 16-Core Processor |
| date | 2026-10-02T03:29:16Z |
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
| load | 0.98 0.96 1.31 |
| mem_total_kib | 65007132 |
| nproc | 32 |
| objecter_inflight_op_bytes | 104857600 |
| objecter_inflight_ops | 1024 |
| over_objects | 640 |
| pool_max_avail | 9286684672 |
| release | squid |
| rooket_version | rooket v0.0.0-20260928053658-3d8551957e08 |
| write4m_objects | 1000 |

The judged floor is rados bench from quay.io/ceph/ceph:v19.2.6, Ceph 19.2.6, the release of the librados the benchmark links. The cluster runs the same image. The floor's writes pass --no-hints, so rados bench sends no allocation hints, as the Go cells send none.

## Verdict

cgo is not a bottleneck on a release when one of the sync, callback and pipe modes meets both criteria there: throughput and mean latency, which compare read4k and write4k at 64 and 256 in flight, and write4m at 1, 4 and 16, with rados bench at the same size and concurrency, run just before and just after the cell's three modes: a ratio divides by the mean of the two runs, and a cell whose runs differ by more than 10% of that mean is flagged unstable beside the verdict it feeds. read4m is measured and shown with its floor, but not judged; its table says why. The rows below the criteria are measurements, compared with no threshold. CPU per operation is the CPU each operation of read4k at 256 in flight used; among the modes that meet both criteria, the one using the least is preferred, and every cell's CPU is in the tables below. The boundary cost is the part of that cell's CPU profile a pure-Go client would no longer pay: the submit crossing and the delivery, not the C librados runs on the Go thread nor the Go completion handler. Thread growth is each mode's largest, its peak less its idle count, across the cells under the byte budget. The cgo frames row counts every sample whose stack holds a frame of the cgo boundary, cgo's runtime.cgoCheck* pointer checks included; it counts the C librados runs on the Go thread, which a pure-Go client would run too, as a cost of the boundary. For pipe the boundary cost leaves out the C side of each completion, one write(2) to the pipe on a librados thread, which the profile records under runtime._ExternalCode with no frame to tell it by: it is unmeasured, and pipe's boundary cost is low by that much.

| criterion | sync | callback | pipe |
|---|---|---|---|
| throughput>=0.8x floor | PASS: worst 0.85x (read4k at 64), margin +0.05; unstable floor: read4k at 256 (21.5%), write4m at 4 (22.9%) | PASS: worst 0.86x (write4k at 64), margin +0.06; unstable floor: read4k at 256 (21.5%), write4m at 4 (22.9%) | PASS: worst 0.88x (write4m at 1), margin +0.08; unstable floor: read4k at 256 (21.5%), write4m at 4 (22.9%) |
| mean latency<=1.25x floor | PASS: worst 1.18x (read4k at 64), margin +0.07; unstable floor: read4k at 256 (21.6%), write4m at 4 (22.9%) | PASS: worst 1.16x (write4k at 64), margin +0.09; unstable floor: read4k at 256 (21.6%), write4m at 4 (22.9%) | PASS: worst 1.13x (write4m at 1), margin +0.12; unstable floor: read4k at 256 (21.6%), write4m at 4 (22.9%) |
| CPU per operation, read4k at 256 (not judged) | 26 µs | 31 µs | 30 µs |
| boundary cost, read4k at 256 (not judged) | 3.1% of 19.92s sampled, about 0.8 µs of the operation's 26 | 5.0% of 26.36s sampled, about 1.6 µs of the operation's 31 | 9.3% of 28.81s sampled, about 2.8 µs of the operation's 30; leaves out pipe's write(2) per completion, unmeasured |
| thread growth (not judged) | largest +532 (read4k at 512) | largest +89 (headwrite4k at 512) | largest +79 (indexrtt at 512) |
| cgo frames, share of CPU (not judged) | 37.3% of 19.92s sampled | 24.9% of 26.36s sampled | 19.2% of 28.81s sampled |

**Answer:** sync, callback and pipe meet both criteria on squid.

**Least CPU per operation among them**, read4k at 256 in flight: sync, 26 µs.

In-flight limits of the cells: derived. No submission parked on the limiter.

Thread counts per benchmark process, the count before its first cell to its highest:

| mode | benchmark processes | largest growth in one process | highest count |
|---|---|---|---|
| sync | 28 | 27→559 (+532) | 559 |
| callback | 28 | 27→116 (+89) | 116 |
| pipe | 28 | 27→106 (+79) | 106 |

## read4k

| conc | mode | n | ops/s | p50 µs | p99 µs | p999 µs | mean µs | CPU µs/op | threads | errors | floor ops/s | floor mean µs | ops ÷ floor | mean ÷ floor | floor runs |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| 1 | sync | 380592 | 14336 | 67.1 | 107 | 143 | 69.7 | 34.7 | 27→28 (+1) | 0 | 15499 | 63.5 | 0.92x | 1.10x | single run |
| 1 | callback | 307369 | 12918 | 74.7 | 118 | 158 | 77.4 | 45.6 | 27→29 (+2) | 0 | 15499 | 63.5 | 0.83x | 1.22x | single run |
| 1 | pipe | 318049 | 10420 | 91.3 | 147 | 190 | 95.9 | 57.9 | 27→29 (+2) | 0 | 15499 | 63.5 | 0.67x | 1.51x | single run |
| 16 | sync | 1000000 | 49552 | 320 | 447 | 538 | 323 | 24.9 | 27→44 (+17) | 0 | 47103 | 337 | 1.05x | 0.96x | single run |
| 16 | callback | 1205336 | 49984 | 317 | 459 | 555 | 320 | 33.7 | 27→34 (+7) | 0 | 47103 | 337 | 1.06x | 0.95x | single run |
| 16 | pipe | 1000000 | 49612 | 319 | 462 | 551 | 322 | 37.1 | 27→33 (+6) | 0 | 47103 | 337 | 1.05x | 0.96x | single run |
| 64 | sync | 1323369 | 54942 | 1148 | 1562 | 1711 | 1165 | 24.6 | 28→96 (+68) | 0 | 64265 / 65040 | 993 / 981 | 0.85x | 1.18x | ops 1.2%, mean 1.2% |
| 64 | callback | 1373886 | 56975 | 1104 | 1557 | 1734 | 1123 | 32.2 | 27→37 (+10) | 0 | 64265 / 65040 | 993 / 981 | 0.88x | 1.14x | ops 1.2%, mean 1.2% |
| 64 | pipe | 1334328 | 59550 | 1073 | 1388 | 1665 | 1075 | 32.0 | 27→37 (+10) | 0 | 64265 / 65040 | 993 / 981 | 0.92x | 1.09x | ops 1.2%, mean 1.2% |
| 256 | sync | 1341778 | 56192 | 4433 | 5943 | 6314 | 4553 | 25.5 | 27→292 (+265) | 0 | 67077 / 54036 | 3813 / 4734 | 0.93x | 1.07x | unstable: ops 21.5%, mean 21.6% |
| 256 | callback | 1445907 | 62351 | 4082 | 5307 | 6158 | 4105 | 31.3 | 27→51 (+24) | 0 | 67077 / 54036 | 3813 / 4734 | 1.03x | 0.96x | unstable: ops 21.5%, mean 21.6% |
| 256 | pipe | 1488916 | 62044 | 4129 | 4975 | 5317 | 4126 | 30.5 | 27→47 (+20) | 0 | 67077 / 54036 | 3813 / 4734 | 1.02x | 0.97x | unstable: ops 21.5%, mean 21.6% |
| 512 | sync | 1346456 | 63437 | 7957 | 10604 | 12137 | 8069 | 25.3 | 27→559 (+532) | 0 | 67033 | 7633 | 0.95x | 1.06x | single run |
| 512 | callback | 1459918 | 62294 | 8116 | 10240 | 12288 | 8217 | 32.4 | 27→71 (+44) | 0 | 67033 | 7633 | 0.93x | 1.08x | single run |
| 512 | pipe | 1480168 | 61801 | 8265 | 9923 | 10642 | 8283 | 30.8 | 27→54 (+27) | 0 | 67033 | 7633 | 0.92x | 1.09x | single run |

## write4k

| conc | mode | n | ops/s | p50 µs | p99 µs | p999 µs | mean µs | CPU µs/op | threads | errors | floor ops/s | floor mean µs | ops ÷ floor | mean ÷ floor | floor runs |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| 1 | sync | 6933 | 258 | 3445 | 10721 | 15036 | 3877 | 97.4 | 27→29 (+2) | 0 | 327 | 3054 | 0.79x | 1.27x | single run |
| 1 | callback | 7962 | 325 | 3284 | 7424 | 10859 | 3082 | 114.6 | 27→28 (+1) | 0 | 327 | 3054 | 0.99x | 1.01x | single run |
| 1 | pipe | 7800 | 325 | 3274 | 7893 | 12763 | 3075 | 102.9 | 27→28 (+1) | 0 | 327 | 3054 | 0.99x | 1.01x | single run |
| 16 | sync | 58423 | 2197 | 6014 | 18529 | 30258 | 7283 | 48.1 | 27→45 (+18) | 0 | 2155 | 7424 | 1.02x | 0.98x | single run |
| 16 | callback | 57351 | 2369 | 5878 | 16627 | 23338 | 6754 | 46.8 | 27→32 (+5) | 0 | 2155 | 7424 | 1.10x | 0.91x | single run |
| 16 | pipe | 45294 | 2244 | 6008 | 17568 | 23059 | 7129 | 50.6 | 27→31 (+4) | 0 | 2155 | 7424 | 1.04x | 0.96x | single run |
| 64 | sync | 165955 | 6345 | 8764 | 23903 | 50404 | 10087 | 33.3 | 27→95 (+68) | 0 | 6604 / 6874 | 9690 / 9306 | 0.94x | 1.06x | ops 4.0%, mean 4.0% |
| 64 | callback | 165459 | 5799 | 9739 | 27759 | 65101 | 11034 | 34.3 | 27→39 (+12) | 0 | 6604 / 6874 | 9690 / 9306 | 0.86x | 1.16x | ops 4.0%, mean 4.0% |
| 64 | pipe | 164898 | 6851 | 8715 | 20645 | 57326 | 9341 | 37.4 | 27→39 (+12) | 0 | 6604 / 6874 | 9690 / 9306 | 1.02x | 0.98x | ops 4.0%, mean 4.0% |
| 256 | sync | 182121 | 5043 | 53561 | 155228 | 389645 | 50734 | 32.4 | 27→290 (+263) | 0 | 5109 / 5514 | 50098 / 46411 | 0.95x | 1.05x | ops 7.6%, mean 7.6% |
| 256 | callback | 125984 | 5042 | 53940 | 137234 | 260314 | 50759 | 35.4 | 27→45 (+18) | 0 | 5109 / 5514 | 50098 / 46411 | 0.95x | 1.05x | ops 7.6%, mean 7.6% |
| 256 | pipe | 175761 | 5106 | 54014 | 121857 | 231271 | 50078 | 41.3 | 27→48 (+21) | 0 | 5109 / 5514 | 50098 / 46411 | 0.96x | 1.04x | ops 7.6%, mean 7.6% |
| 512 | sync | 235882 | 4810 | 105628 | 283527 | 354674 | 106373 | 32.7 | 27→548 (+521) | 0 | 5772 | 88522 | 0.83x | 1.20x | single run |
| 512 | callback | 138124 | 5353 | 97260 | 206121 | 340268 | 95547 | 31.6 | 27→60 (+33) | 0 | 5772 | 88522 | 0.93x | 1.08x | single run |
| 512 | pipe | 123265 | 5794 | 93832 | 162721 | 201054 | 88200 | 36.9 | 27→65 (+38) | 0 | 5772 | 88522 | 1.00x | 1.00x | single run |

## read4m

Not judged: rados bench's 4 MiB rand does not read as the Go cell does: rand picks each object at random with replacement among the objects written for it, where each Go worker reads its own, and its fixed object names land on fixed placement groups. Its ratios are shown, but the throughput and latency criteria leave read4m out until the floor reads as the Go cell does.

| conc | mode | n | ops/s | p50 µs | p99 µs | p999 µs | mean µs | CPU µs/op | threads | errors | floor ops/s | floor mean µs | ops ÷ floor | mean ÷ floor | floor runs |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| 1 | sync | 4454 | 194 | 5141 | 5998 | 6791 | 5146 | 819.7 | 27→29 (+2) | 0 | 193 / 243 | 5169 / 4117 | 0.89x | 1.11x | unstable: ops 22.6%, mean 22.7% |
| 1 | callback | 4546 | 196 | 5179 | 5934 | 6947 | 5112 | 807.8 | 26→28 (+2) | 0 | 193 / 243 | 5169 / 4117 | 0.90x | 1.10x | unstable: ops 22.6%, mean 22.7% |
| 1 | pipe | 5264 | 240 | 4089 | 5184 | 5480 | 4175 | 812.2 | 27→28 (+1) | 0 | 193 / 243 | 5169 / 4117 | 1.10x | 0.90x | unstable: ops 22.6%, mean 22.7% |
| 4 | sync | 9307 | 380 | 10372 | 18730 | 20089 | 10523 | 815.7 | 26→30 (+4) | 0 | 258 / 260 | 15525 / 15401 | 1.47x | 0.68x | ops 0.8%, mean 0.8% |
| 4 | callback | 6237 | 254 | 12015 | 25528 | 29336 | 15724 | 930.0 | 27→29 (+2) | 0 | 258 / 260 | 15525 / 15401 | 0.98x | 1.02x | ops 0.8%, mean 0.8% |
| 4 | pipe | 9205 | 388 | 10196 | 18105 | 20650 | 10297 | 751.9 | 27→29 (+2) | 0 | 258 / 260 | 15525 / 15401 | 1.50x | 0.67x | ops 0.8%, mean 0.8% |
| 16 | sync | 5872 | 265 | 60059 | 81039 | 259689 | 60320 | 918.8 | 27→41 (+14) | 0 | 312 / 343 | 51189 / 46582 | 0.81x | 1.23x | ops 9.4%, mean 9.4% |
| 16 | callback | 8334 | 330 | 47205 | 77066 | 246179 | 48418 | 831.1 | 27→41 (+14) | 0 | 312 / 343 | 51189 / 46582 | 1.01x | 0.99x | ops 9.4%, mean 9.4% |
| 16 | pipe | 5847 | 277 | 58027 | 84380 | 101544 | 57693 | 848.3 | 27→39 (+12) | 0 | 312 / 343 | 51189 / 46582 | 0.85x | 1.18x | ops 9.4%, mean 9.4% |

## write4m

| conc | mode | n | ops/s | p50 µs | p99 µs | p999 µs | mean µs | CPU µs/op | threads | errors | floor ops/s | floor mean µs | ops ÷ floor | mean ÷ floor | floor runs |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| 1 | sync | 1000 | 106 | 9203 | 17813 | 26349 | 9426 | 920.0 | 28→28 (+0) | 0 | 105 / 112 | 9533 / 8958 | 0.98x | 1.02x | ops 6.2%, mean 6.2% |
| 1 | callback | 1000 | 104 | 9101 | 17371 | 28240 | 9643 | 1013.8 | 27→28 (+1) | 0 | 105 / 112 | 9533 / 8958 | 0.96x | 1.04x | ops 6.2%, mean 6.2% |
| 1 | pipe | 1000 | 95.5 | 10087 | 21422 | 23418 | 10468 | 956.2 | 27→28 (+1) | 0 | 105 / 112 | 9533 / 8958 | 0.88x | 1.13x | ops 6.2%, mean 6.2% |
| 4 | sync | 1000 | 138 | 28115 | 50223 | 64856 | 28968 | 1188.1 | 27→29 (+2) | 0 | 137 / 109 | 29061 / 36583 | 1.12x | 0.88x | unstable: ops 22.9%, mean 22.9% |
| 4 | callback | 1000 | 138 | 28289 | 48483 | 57439 | 28947 | 1248.4 | 27→27 (+0) | 0 | 137 / 109 | 29061 / 36583 | 1.12x | 0.88x | unstable: ops 22.9%, mean 22.9% |
| 4 | pipe | 1000 | 135 | 28550 | 48532 | 62765 | 29518 | 1247.8 | 27→28 (+1) | 0 | 137 / 109 | 29061 / 36583 | 1.10x | 0.90x | unstable: ops 22.9%, mean 22.9% |
| 16 | sync | 1000 | 118 | 127089 | 242349 | 282269 | 134658 | 2491.3 | 27→36 (+9) | 0 | 110 / 109 | 145525 / 146808 | 1.08x | 0.92x | ops 1.0%, mean 0.9% |
| 16 | callback | 1000 | 137 | 112953 | 194253 | 207682 | 115947 | 2513.7 | 27→31 (+4) | 0 | 110 / 109 | 145525 / 146808 | 1.26x | 0.79x | ops 1.0%, mean 0.9% |
| 16 | pipe | 1000 | 122 | 126905 | 224668 | 242446 | 131017 | 2513.6 | 27→30 (+3) | 0 | 110 / 109 | 145525 / 146808 | 1.11x | 0.90x | ops 1.0%, mean 0.9% |

## headread4k

| conc | mode | n | ops/s | p50 µs | p99 µs | p999 µs | mean µs | CPU µs/op | threads | errors |
|---|---|---|---|---|---|---|---|---|---|---|
| 1 | sync | 296774 | 11270 | 86.3 | 131 | 198 | 88.7 | 47.2 | 27→29 (+2) | 0 |
| 1 | callback | 232814 | 9797 | 97.2 | 162 | 227 | 102 | 59.2 | 27→29 (+2) | 0 |
| 1 | pipe | 250363 | 9921 | 98.2 | 145 | 201 | 101 | 66.4 | 27→29 (+2) | 0 |
| 16 | sync | 1219728 | 48905 | 323 | 483 | 631 | 327 | 35.2 | 27→56 (+29) | 0 |
| 16 | callback | 1000000 | 49296 | 322 | 459 | 620 | 324 | 42.3 | 27→38 (+11) | 0 |
| 16 | pipe | 897609 | 41768 | 387 | 569 | 715 | 383 | 54.1 | 27→45 (+18) | 0 |

## headwrite4k

| conc | mode | n | ops/s | p50 µs | p99 µs | p999 µs | mean µs | CPU µs/op | threads | errors |
|---|---|---|---|---|---|---|---|---|---|---|
| 1 | sync | 7087 | 322 | 3304 | 7288 | 10745 | 3107 | 105.1 | 26→28 (+2) | 0 |
| 1 | callback | 7501 | 317 | 3336 | 9630 | 12360 | 3154 | 140.0 | 27→28 (+1) | 0 |
| 1 | pipe | 7162 | 316 | 3344 | 8368 | 10874 | 3169 | 126.0 | 27→29 (+2) | 0 |
| 16 | sync | 49335 | 2163 | 6123 | 20845 | 48246 | 7395 | 74.0 | 27→45 (+18) | 0 |
| 16 | callback | 54872 | 2316 | 6062 | 17488 | 27590 | 6905 | 82.4 | 26→35 (+9) | 0 |
| 16 | pipe | 51553 | 2210 | 6176 | 16498 | 29396 | 7239 | 82.4 | 27→33 (+6) | 0 |
| 64 | sync | 154363 | 5863 | 9051 | 35526 | 88491 | 10915 | 60.9 | 27→98 (+71) | 0 |
| 64 | callback | 156075 | 6264 | 8997 | 27737 | 72320 | 10215 | 71.5 | 27→47 (+20) | 0 |
| 64 | pipe | 160525 | 6674 | 8967 | 22056 | 49292 | 9587 | 71.5 | 27→46 (+19) | 0 |
| 256 | sync | 117344 | 5754 | 45127 | 135225 | 318715 | 44477 | 64.5 | 27→300 (+273) | 0 |
| 256 | callback | 154686 | 5502 | 50409 | 122822 | 161672 | 46514 | 70.9 | 26→68 (+42) | 0 |
| 256 | pipe | 140106 | 5845 | 44947 | 116647 | 178753 | 43783 | 76.2 | 26→54 (+28) | 0 |
| 512 | sync | 349422 | 4575 | 110263 | 241238 | 420344 | 111821 | 56.3 | 27→559 (+532) | 0 |
| 512 | callback | 117835 | 4001 | 118938 | 425462 | 692666 | 127721 | 74.8 | 27→116 (+89) | 0 |
| 512 | pipe | 140798 | 5192 | 98727 | 323696 | 583484 | 98507 | 76.9 | 27→83 (+56) | 0 |

## indexrtt

| conc | mode | n | ops/s | p50 µs | p99 µs | p999 µs | mean µs | CPU µs/op | threads | errors |
|---|---|---|---|---|---|---|---|---|---|---|
| 1 | sync | 3542 | 330 | 5785 | 13912 | 21237 | 6055 | 90.8 | 27→28 (+1) | 0 |
| 1 | callback | 3986 | 319 | 5907 | 14149 | 29259 | 6273 | 131.9 | 27→28 (+1) | 0 |
| 1 | pipe | 3554 | 325 | 5853 | 15314 | 23827 | 6158 | 112.2 | 27→28 (+1) | 0 |
| 16 | sync | 32527 | 2465 | 11240 | 27859 | 39657 | 12983 | 54.5 | 27→45 (+18) | 0 |
| 16 | callback | 27961 | 2659 | 11222 | 26845 | 46634 | 12033 | 55.2 | 27→32 (+5) | 0 |
| 16 | pipe | 28225 | 2164 | 12612 | 34152 | 48411 | 14783 | 59.8 | 27→32 (+5) | 0 |
| 64 | sync | 102675 | 10223 | 11805 | 26934 | 51822 | 12516 | 38.9 | 26→95 (+69) | 0 |
| 64 | callback | 114548 | 10287 | 11819 | 27140 | 34035 | 12441 | 46.3 | 26→42 (+16) | 0 |
| 64 | pipe | 123804 | 10177 | 11905 | 27108 | 56652 | 12574 | 51.1 | 27→40 (+13) | 0 |
| 256 | sync | 336175 | 26866 | 18015 | 36645 | 70019 | 19051 | 32.7 | 27→288 (+261) | 0 |
| 256 | callback | 324196 | 25332 | 18543 | 41217 | 65520 | 20206 | 42.5 | 27→47 (+20) | 0 |
| 256 | pipe | 329542 | 25894 | 18368 | 37143 | 133294 | 19767 | 47.8 | 27→55 (+28) | 0 |
| 512 | sync | 322447 | 25501 | 37403 | 70346 | 106916 | 40128 | 34.4 | 27→553 (+526) | 0 |
| 512 | callback | 323930 | 26293 | 36838 | 75759 | 263666 | 38931 | 43.2 | 27→52 (+25) | 0 |
| 512 | pipe | 329612 | 26100 | 37085 | 64778 | 107911 | 39214 | 44.8 | 27→106 (+79) | 0 |

## rados bench floor

| shape | op | size | conc | run | image | seconds | max objects | objects read | ops | elapsed s | ops/s | mean µs | MB/s |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| read4k | rand | 4096 | 1 | single | quay.io/ceph/ceph:v19.2.6 | 20 | — | 1 | 309985 | 20.00 | 15499 | 63.5 | 60.54 |
| read4k | rand | 4096 | 16 | single | quay.io/ceph/ceph:v19.2.6 | 20 | — | 16 | 942078 | 20.00 | 47103 | 337 | 184.00 |
| read4k | rand | 4096 | 64 | before | quay.io/ceph/ceph:v19.2.6 | 20 | — | 64 | 1285337 | 20.00 | 64265 | 993 | 251.03 |
| read4k | rand | 4096 | 64 | after | quay.io/ceph/ceph:v19.2.6 | 20 | — | 64 | 1300864 | 20.00 | 65040 | 981 | 254.06 |
| read4k | rand | 4096 | 256 | before | quay.io/ceph/ceph:v19.2.6 | 20 | — | 256 | 1341751 | 20.00 | 67077 | 3813 | 262.02 |
| read4k | rand | 4096 | 256 | after | quay.io/ceph/ceph:v19.2.6 | 20 | — | 256 | 1080937 | 20.00 | 54036 | 4734 | 211.08 |
| read4k | rand | 4096 | 512 | single | quay.io/ceph/ceph:v19.2.6 | 20 | — | 512 | 1341203 | 20.01 | 67033 | 7633 | 261.85 |
| write4k | write | 4096 | 1 | single | quay.io/ceph/ceph:v19.2.6 | 20 | — | — | 6547 | 20.00 | 327 | 3054 | 1.28 |
| write4k | write | 4096 | 16 | single | quay.io/ceph/ceph:v19.2.6 | 20 | — | — | 43101 | 20.00 | 2155 | 7424 | 8.42 |
| write4k | write | 4096 | 64 | before | quay.io/ceph/ceph:v19.2.6 | 20 | — | — | 132109 | 20.00 | 6604 | 9690 | 25.80 |
| write4k | write | 4096 | 64 | after | quay.io/ceph/ceph:v19.2.6 | 20 | — | — | 137538 | 20.01 | 6874 | 9306 | 26.85 |
| write4k | write | 4096 | 256 | before | quay.io/ceph/ceph:v19.2.6 | 20 | — | — | 102661 | 20.09 | 5109 | 50098 | 19.96 |
| write4k | write | 4096 | 256 | after | quay.io/ceph/ceph:v19.2.6 | 20 | — | — | 110483 | 20.04 | 5514 | 46411 | 21.54 |
| write4k | write | 4096 | 512 | single | quay.io/ceph/ceph:v19.2.6 | 20 | — | — | 115924 | 20.08 | 5772 | 88522 | 22.55 |
| read4m | rand | 4194304 | 1 | before | quay.io/ceph/ceph:v19.2.6 | 20 | — | 1 | 3868 | 20.00 | 193 | 5169 | 773.47 |
| read4m | rand | 4194304 | 1 | after | quay.io/ceph/ceph:v19.2.6 | 20 | — | 1 | 4856 | 20.00 | 243 | 4117 | 971.01 |
| read4m | rand | 4194304 | 4 | before | quay.io/ceph/ceph:v19.2.6 | 20 | — | 4 | 5154 | 20.01 | 258 | 15525 | 1030.14 |
| read4m | rand | 4194304 | 4 | after | quay.io/ceph/ceph:v19.2.6 | 20 | — | 4 | 5196 | 20.02 | 260 | 15401 | 1038.35 |
| read4m | rand | 4194304 | 16 | before | quay.io/ceph/ceph:v19.2.6 | 20 | — | 16 | 6259 | 20.05 | 312 | 51189 | 1248.84 |
| read4m | rand | 4194304 | 16 | after | quay.io/ceph/ceph:v19.2.6 | 20 | — | 16 | 6879 | 20.06 | 343 | 46582 | 1371.57 |
| write4m | write | 4194304 | 1 | before | quay.io/ceph/ceph:v19.2.6 | 600 | 1000 | — | 1000 | 9.53 | 105 | 9533 | 419.51 |
| write4m | write | 4194304 | 1 | after | quay.io/ceph/ceph:v19.2.6 | 600 | 1000 | — | 1000 | 8.96 | 112 | 8958 | 446.42 |
| write4m | write | 4194304 | 4 | before | quay.io/ceph/ceph:v19.2.6 | 600 | 1000 | — | 1000 | 7.28 | 137 | 29061 | 549.73 |
| write4m | write | 4194304 | 4 | after | quay.io/ceph/ceph:v19.2.6 | 600 | 1000 | — | 1000 | 9.16 | 109 | 36583 | 436.84 |
| write4m | write | 4194304 | 16 | before | quay.io/ceph/ceph:v19.2.6 | 600 | 1000 | — | 1000 | 9.11 | 110 | 145525 | 439.20 |
| write4m | write | 4194304 | 16 | after | quay.io/ceph/ceph:v19.2.6 | 600 | 1000 | — | 1000 | 9.20 | 109 | 146808 | 434.95 |

## Over the byte budget

Cells run past the byte budget on purpose, each in a process of its own. With the limiter's bounds above what the cell can reach, librados's objecter throttle blocks submitting threads inside C; with the bounds derived from that throttle, the limiter parks the excess submissions in Go.

| mode | shape | conc | limiter | n | ops/s | mean µs | threads | growth | limiter waits |
|---|---|---|---|---|---|---|---|---|---|
| callback | write4m | 64 | ops 1048576, bytes 1099511627776 | 640 | 125 | 494823 | 26→61 | +35 | 0 |
| callback | write4m | 64 | derived | 640 | 138 | 445812 | 27→46 | +19 | 617 |
| pipe | write4m | 64 | ops 1048576, bytes 1099511627776 | 640 | 137 | 449103 | 27→62 | +35 | 0 |
| pipe | write4m | 64 | derived | 640 | 141 | 437025 | 27→46 | +19 | 617 |

## CPU profiles

The profile samples every thread of the process. A sample taken while a Go thread runs C is recorded under the Go stack that called it, ending in runtime.cgocall, so the cgo share holds the C that Go calls ran, librados's submission included, as well as the crossing itself; that C is the second share. A sample of a thread the Go runtime does not run, librados's messenger and finisher threads among them, is recorded under runtime._ExternalCode; that is the last share, and outside the cgo share.

The columns between divide the profile by the frame nearest each sample's leaf, so no sample counts twice. (i), the submit crossing, is the cgo machinery around the C: runtime.cgocall's Go side, the _Cfunc_ stubs, the Pinner and the pointer checks, the completion handler's own calls into C included. (ii), the delivery, is how a completion reaches Go: the runtime.cgocallback* frames for callback, (*aioPipe).drain and the pipe read it waits in for pipe, and nothing for sync, whose wake happens in C. Neither counts the Go completion handler, rados.aioComplete, shown for reference: a pure-Go client runs one too. The boundary cost is (i) plus (ii).

| mode | profile | sampled | cgo frames | C under runtime.cgocall | (i) submit crossing | (ii) delivery | boundary cost | Go completion handler | threads outside Go | largest cgo frames, cumulative |
|---|---|---|---|---|---|---|---|---|---|---|
| sync | cpu-sync-read4k-256.pprof | 19.92s | 37.3% | 34.2% | 3.1% | 0.0% | 3.1% | 0.0% | 51.3% | runtime.cgocall 7.17s; github.com/ceph/go-ceph/rados._Cfunc_rados_read_op_operate 6.37s; github.com/ceph/go-ceph/rados._Cfunc_rados_read_op_read 290ms; github.com/ceph/go-ceph/rados._Cfunc_free 150ms; runtime.(*Pinner).Unpin 90ms |
| callback | cpu-callback-read4k-256.pprof | 26.36s | 24.9% | 15.6% | 4.9% | 0.1% | 5.0% | 4.3% | 42.4% | runtime.cgocall 4.39s; github.com/ceph/go-ceph/rados._Cfunc_rados_aio_read_op_operate 3.16s; runtime.cgocallback 1.37s; runtime.cgocallbackg 1.37s; runtime.cgocallbackg1 1.36s |
| pipe | cpu-pipe-read4k-256.pprof | 28.81s | 19.2% | 14.7% | 4.5% | 4.8% | 9.3% | 3.2% | 41.6% | runtime.cgocall 4.64s; github.com/ceph/go-ceph/rados._Cfunc_rados_aio_read_op_operate 2.98s; runtime.(*Pinner).Pin 480ms; github.com/ceph/go-ceph/rados._Cfunc_rados_read_op_read 350ms; github.com/ceph/go-ceph/rados._Cfunc_CString 230ms |
