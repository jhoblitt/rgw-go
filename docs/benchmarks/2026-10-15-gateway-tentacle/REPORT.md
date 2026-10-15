# Gateway comparison: tentacle

- radosgw: docs/benchmarks/2026-10-15-gateway-tentacle/radosgw; frontends `beast port=80 tcp_nodelay=1`, TCP_NODELAY on
- rgw-go: docs/benchmarks/2026-10-15-gateway-tentacle/rgw-go; frontends `beast endpoint=127.0.0.1:7482`, TCP_NODELAY on

Each row is one measured phase. req/s counts S3 requests: elbencho's IOs, a listing's ListObjectsV2 calls, httpload's GETs. A multipart PUT's row, marked (parts/s), counts as elbencho does: its req/s counts UploadPart requests, leaving out each upload's Create and Complete, and its CPU per request is per part. objects/s counts objects written or read, or keys listed. The latency percentiles are per request, from elbencho's latency histogram as its --latpercent9s prints them, or httpload's. CPU per request is the gateway process's user and system time over the phase, from the samples taken once a second around it, interpolated at its start and end, so a short phase's is coarse; a 2 s gap after every phase and every preparation keeps their CPU out of the next phase's. Peak RSS is the highest of those samples, and thread high-water the most OS threads among them: Go parks an idle OS thread rather than ending it, so for rgw-go it is the most the run has needed by the phase's end, its preparations included, not the phase's own demand. A ratio column divides a run's value by radosgw's.

## Findings

No phase had an error or a retry.

## small-put

### small-put: throughput and latency

| variant | threads | req/s radosgw | req/s rgw-go | rgw-go/radosgw | objects/s radosgw | objects/s rgw-go | rgw-go/radosgw | MiB/s radosgw | MiB/s rgw-go | rgw-go/radosgw | p50 ms radosgw | p50 ms rgw-go | rgw-go/radosgw | p99 ms radosgw | p99 ms rgw-go | rgw-go/radosgw | p999 ms radosgw | p999 ms rgw-go | rgw-go/radosgw |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| PUT 4 KiB | 1 | 202 | 205 | 1.01 | 202 | 205 | 1.01 | 0.79 | 0.80 | 1.01 | 4.86 | 4.86 | 1.00 | 12.2 | 12.3 | 1.01 | 25.3 | 23.8 | 0.94 |
| PUT 4 KiB | 8 | 850 | 908 | 1.07 | 850 | 908 | 1.07 | 3.32 | 3.55 | 1.07 | 6.40 | 6.02 | 0.94 | 17.7 | 18.7 | 1.06 | 24.8 | 29.7 | 1.20 |
| PUT 4 KiB | 32 | 1532 | 1588 | 1.04 | 1532 | 1588 | 1.04 | 5.99 | 6.20 | 1.04 | 17.9 | 16.6 | 0.93 | 43.5 | 41.5 | 0.95 | 97.3 | 74.8 | 0.77 |
| PUT 4 KiB | 64 | 1550 | 1687 | 1.09 | 1550 | 1687 | 1.09 | 6.06 | 6.59 | 1.09 | 37.4 | 34.8 | 0.93 | 86.0 | 79.9 | 0.93 | 223 | 141 | 0.63 |
| PUT 4 KiB | 128 | 1596 | 1746 | 1.09 | 1596 | 1746 | 1.09 | 6.24 | 6.83 | 1.09 | 70.7 | 66.6 | 0.94 | 152 | 133 | 0.88 | 209 | 168 | 0.80 |
| PUT 4 KiB | 256 | 1664 | 1746 | 1.05 | 1664 | 1746 | 1.05 | 6.52 | 6.84 | 1.05 | 135 | 133 | 0.98 | 270 | 270 | 1.00 | 442 | 377 | 0.85 |

### small-put: cost

| variant | threads | CPU ms/req radosgw | CPU ms/req rgw-go | rgw-go/radosgw | thread high-water radosgw | thread high-water rgw-go | rgw-go/radosgw | peak RSS MiB radosgw | peak RSS MiB rgw-go | rgw-go/radosgw | errors radosgw | errors rgw-go | retries radosgw | retries rgw-go |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| PUT 4 KiB | 1 | 0.82 | 0.58 | 0.70 | 669 | 33 | 0.05 | 249 | 57.5 | 0.23 | 0 | 0 | 0 | 0 |
| PUT 4 KiB | 8 | 0.81 | 0.69 | 0.85 | 669 | 38 | 0.06 | 249 | 66.3 | 0.27 | 0 | 0 | 0 | 0 |
| PUT 4 KiB | 32 | 1.09 | 0.81 | 0.74 | 669 | 49 | 0.07 | 251 | 94.2 | 0.38 | 0 | 0 | 0 | 0 |
| PUT 4 KiB | 64 | 1.21 | 0.77 | 0.63 | 669 | 58 | 0.09 | 252 | 133 | 0.53 | 0 | 0 | 0 | 0 |
| PUT 4 KiB | 128 | 1.23 | 0.70 | 0.57 | 669 | 72 | 0.11 | 254 | 208 | 0.82 | 0 | 0 | 0 | 0 |
| PUT 4 KiB | 256 | 1.25 | 0.62 | 0.49 | 669 | 92 | 0.14 | 264 | 355 | 1.35 | 0 | 0 | 0 | 0 |

GC cycles over the whole profile, its preparation and removals included, from GODEBUG=gctrace=1; each cycle stops the world twice:

- rgw-go, small-put: 20488 cycles, longest stop-the-world pause 3500 µs, 2492931 µs in all

## small-get and noop

### small-get and noop: throughput and latency

| variant | threads | req/s radosgw | req/s rgw-go | rgw-go/radosgw | objects/s radosgw | objects/s rgw-go | rgw-go/radosgw | MiB/s radosgw | MiB/s rgw-go | rgw-go/radosgw | p50 ms radosgw | p50 ms rgw-go | rgw-go/radosgw | p99 ms radosgw | p99 ms rgw-go | rgw-go/radosgw | p999 ms radosgw | p999 ms rgw-go | rgw-go/radosgw |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| GET 4 KiB | 1 | 1480 | 1755 | 1.19 | 1480 | 1755 | 1.19 | 5.78 | 6.86 | 1.19 | 0.66 | 0.55 | 0.83 | 0.89 | 0.99 | 1.12 | 1.20 | 1.17 | 0.97 |
| anonymous GET / | 1 | 4451 | 13746 | 3.09 | — | — | — | — | — | — | 0.22 | 0.07 | 0.31 | 0.31 | 0.17 | 0.56 | 0.41 | 0.33 | 0.81 |
| GET 4 KiB | 8 | 8541 | 7277 | 0.85 | 8541 | 7277 | 0.85 | 33.4 | 28.4 | 0.85 | 0.93 | 1.12 | 1.21 | 1.26 | 1.65 | 1.30 | 1.79 | 1.87 | 1.04 |
| anonymous GET / | 8 | 17248 | 40356 | 2.34 | — | — | — | — | — | — | 0.45 | 0.14 | 0.32 | 0.70 | 0.53 | 0.75 | 0.96 | 0.65 | 0.68 |
| GET 4 KiB | 32 | 12986 | 11673 | 0.90 | 12986 | 11673 | 0.90 | 50.7 | 45.6 | 0.90 | 2.43 | 2.56 | 1.05 | 3.78 | 5.63 | 1.49 | 5.44 | 17.4 | 3.20 |
| anonymous GET / | 32 | 18437 | 47994 | 2.60 | — | — | — | — | — | — | 1.64 | 0.44 | 0.27 | 3.77 | 2.89 | 0.77 | 4.90 | 4.17 | 0.85 |
| GET 4 KiB | 64 | 12976 | 14862 | 1.15 | 12976 | 14862 | 1.15 | 50.7 | 58.1 | 1.15 | 4.86 | 3.94 | 0.81 | 7.81 | 9.86 | 1.26 | 23.6 | 22.3 | 0.95 |
| anonymous GET / | 64 | 18394 | 64548 | 3.51 | — | — | — | — | — | — | 3.20 | 0.44 | 0.14 | 9.03 | 5.60 | 0.62 | 11.9 | 8.25 | 0.69 |
| GET 4 KiB | 128 | 12487 | 19173 | 1.54 | 12487 | 19173 | 1.54 | 48.8 | 74.9 | 1.54 | 9.60 | 6.40 | 0.67 | 30.0 | 12.8 | 0.43 | 52.7 | 25.9 | 0.49 |
| anonymous GET / | 128 | 18336 | 89044 | 4.86 | — | — | — | — | — | — | 6.14 | 0.45 | 0.07 | 20.7 | 9.88 | 0.48 | 28.3 | 14.8 | 0.52 |
| GET 4 KiB | 256 | 10962 | 17879 | 1.63 | 10962 | 17879 | 1.63 | 42.8 | 69.8 | 1.63 | 18.4 | 11.9 | 0.65 | 94.2 | 71.7 | 0.76 | 120 | 114 | 0.95 |
| anonymous GET / | 256 | 18127 | 157233 | 8.67 | — | — | — | — | — | — | 12.2 | 0.64 | 0.05 | 43.3 | 12.9 | 0.30 | 61.0 | 20.6 | 0.34 |

### small-get and noop: cost

| variant | threads | CPU ms/req radosgw | CPU ms/req rgw-go | rgw-go/radosgw | thread high-water radosgw | thread high-water rgw-go | rgw-go/radosgw | peak RSS MiB radosgw | peak RSS MiB rgw-go | rgw-go/radosgw | errors radosgw | errors rgw-go | retries radosgw | retries rgw-go |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| GET 4 KiB | 1 | 0.59 | 0.41 | 0.70 | 669 | 92 | 0.14 | 255 | 365 | 1.43 | 0 | 0 | 0 | 0 |
| anonymous GET / | 1 | 0.36 | 0.07 | 0.19 | 669 | 92 | 0.14 | 246 | 369 | 1.50 | 0 | 0 | — | — |
| GET 4 KiB | 8 | 0.74 | 0.69 | 0.93 | 669 | 92 | 0.14 | 255 | 346 | 1.35 | 0 | 0 | 0 | 0 |
| anonymous GET / | 8 | 0.72 | 0.16 | 0.22 | 669 | 92 | 0.14 | 247 | 348 | 1.41 | 0 | 0 | — | — |
| GET 4 KiB | 32 | 0.92 | 0.75 | 0.81 | 669 | 92 | 0.14 | 256 | 350 | 1.37 | 0 | 0 | 0 | 0 |
| anonymous GET / | 32 | 0.99 | 0.19 | 0.20 | 669 | 92 | 0.14 | 247 | 352 | 1.42 | 0 | 0 | — | — |
| GET 4 KiB | 64 | 0.95 | 0.64 | 0.67 | 669 | 92 | 0.14 | 257 | 354 | 1.38 | 0 | 0 | 0 | 0 |
| anonymous GET / | 64 | 0.98 | 0.16 | 0.17 | 669 | 92 | 0.14 | 248 | 354 | 1.43 | 0 | 0 | — | — |
| GET 4 KiB | 128 | 0.96 | 0.51 | 0.53 | 669 | 92 | 0.14 | 259 | 358 | 1.38 | 0 | 0 | 0 | 0 |
| anonymous GET / | 128 | 0.95 | 0.14 | 0.14 | 669 | 92 | 0.14 | 249 | 363 | 1.46 | 0 | 0 | — | — |
| GET 4 KiB | 256 | 0.89 | 0.39 | 0.43 | 669 | 92 | 0.14 | 263 | 378 | 1.44 | 0 | 0 | 0 | 0 |
| anonymous GET / | 256 | 0.95 | 0.09 | 0.09 | 669 | 92 | 0.14 | 252 | 392 | 1.56 | 0 | 0 | — | — |

GC cycles over the whole profile, its preparation and removals included, from GODEBUG=gctrace=1; each cycle stops the world twice. A torn line is a cycle's line cut short, by a record of rgw-go's JSON log, which shares its stderr, or by the end of the log the run read; one torn too early to be kept is known from the gap it leaves in the cycles' numbers. A torn line's cycle counts, and its pauses are in neither the longest nor the total:

- rgw-go, small-get: 151452 cycles, longest stop-the-world pause 6500 µs, 28801899 µs in all
- rgw-go, noop: 159205 cycles, longest stop-the-world pause 4400 µs, 31560127 µs in all; 2 torn lines, their cycles' pauses unknown

## large-put

### large-put: throughput and latency

| variant | threads | req/s radosgw | req/s rgw-go | rgw-go/radosgw | objects/s radosgw | objects/s rgw-go | rgw-go/radosgw | MiB/s radosgw | MiB/s rgw-go | rgw-go/radosgw | p50 ms radosgw | p50 ms rgw-go | rgw-go/radosgw | p99 ms radosgw | p99 ms rgw-go | rgw-go/radosgw | p999 ms radosgw | p999 ms rgw-go | rgw-go/radosgw |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| PUT 64 MiB | 32 | 8.61 | 8.95 | 1.04 | 8.61 | 8.95 | 1.04 | 551 | 573 | 1.04 | 3637 | 3473 | 0.95 | 3703 | 3572 | 0.96 | 3703 | 3572 | 0.96 |
| PUT 64 MiB in 8 MiB parts (parts/s) | 32 | 66.5 | 66.4 | 1.00 | 8.32 | 8.31 | 1.00 | 532 | 532 | 1.00 | 471 | 463 | 0.98 | 496 | 532 | 1.07 | 516 | 541 | 1.05 |

### large-put: cost

| variant | threads | CPU ms/req radosgw | CPU ms/req rgw-go | rgw-go/radosgw | thread high-water radosgw | thread high-water rgw-go | rgw-go/radosgw | peak RSS MiB radosgw | peak RSS MiB rgw-go | rgw-go/radosgw | errors radosgw | errors rgw-go | retries radosgw | retries rgw-go |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| PUT 64 MiB | 32 | 196 | 206 | 1.05 | 669 | 92 | 0.14 | 602 | 1982 | 3.29 | 0 | 0 | 0 | 0 |
| PUT 64 MiB in 8 MiB parts (parts/s) | 32 | 30.9 | 26.3 | 0.85 | 669 | 92 | 0.14 | 602 | 2565 | 4.26 | 0 | 0 | 0 | 0 |

GC cycles over the whole profile, its preparation and removals included, from GODEBUG=gctrace=1; each cycle stops the world twice:

- rgw-go, large-put: 7 cycles, longest stop-the-world pause 3300 µs, 17171 µs in all

## large-get

### large-get: throughput and latency

| variant | threads | req/s radosgw | req/s rgw-go | rgw-go/radosgw | objects/s radosgw | objects/s rgw-go | rgw-go/radosgw | MiB/s radosgw | MiB/s rgw-go | rgw-go/radosgw | p50 ms radosgw | p50 ms rgw-go | rgw-go/radosgw | p99 ms radosgw | p99 ms rgw-go | rgw-go/radosgw | p999 ms radosgw | p999 ms rgw-go | rgw-go/radosgw |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| GET 64 MiB in 4 MiB ranges | 32 | 107 | 108 | 1.01 | 6.42 | 6.38 | 0.99 | 428 | 431 | 1.01 | 291 | 291 | 1.00 | 418 | 389 | 0.93 | 1786 | 418 | 0.23 |
| GET 64 MiB | 32 | 9.63 | 9.95 | 1.03 | 9.63 | 9.95 | 1.03 | 625 | 647 | 1.04 | 3244 | 3113 | 0.96 | 3736 | 3539 | 0.95 | 3801 | 3801 | 1.00 |

### large-get: cost

| variant | threads | CPU ms/req radosgw | CPU ms/req rgw-go | rgw-go/radosgw | thread high-water radosgw | thread high-water rgw-go | rgw-go/radosgw | peak RSS MiB radosgw | peak RSS MiB rgw-go | rgw-go/radosgw | errors radosgw | errors rgw-go | retries radosgw | retries rgw-go |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| GET 64 MiB in 4 MiB ranges | 32 | 2.45 | 1.95 | 0.80 | 669 | 92 | 0.14 | 600 | 3068 | 5.11 | 0 | 0 | 0 | 0 |
| GET 64 MiB | 32 | 29.2 | 26.9 | 0.92 | 669 | 92 | 0.14 | 616 | 3068 | 4.98 | 0 | 0 | 0 | 0 |

GC cycles over the whole profile, its preparation and removals included, from GODEBUG=gctrace=1; each cycle stops the world twice:

- rgw-go, large-get: 1 cycles, longest stop-the-world pause 53.0 µs, 61.0 µs in all

## list

### list: throughput and latency

| variant | threads | req/s radosgw | req/s rgw-go | rgw-go/radosgw | objects/s radosgw | objects/s rgw-go | rgw-go/radosgw | MiB/s radosgw | MiB/s rgw-go | rgw-go/radosgw | p50 ms radosgw | p50 ms rgw-go | rgw-go/radosgw | p99 ms radosgw | p99 ms rgw-go | rgw-go/radosgw | p999 ms radosgw | p999 ms rgw-go | rgw-go/radosgw |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| ListObjectsV2 | 1 | 120 | 113 | 0.93 | 119788 | 111891 | 0.93 | — | — | — | 8.32 | 8.96 | 1.08 | 10.1 | 10.4 | 1.03 | 11.1 | 11.8 | 1.06 |
| ListObjectsV2, a prefix per thread | 64 | 1017 | 1469 | 1.44 | 814104 | 1175119 | 1.44 | — | — | — | 62.5 | 42.0 | 0.67 | 74.8 | 76.8 | 1.03 | 98.3 | 92.2 | 0.94 |

### list: cost

| variant | threads | CPU ms/req radosgw | CPU ms/req rgw-go | rgw-go/radosgw | thread high-water radosgw | thread high-water rgw-go | rgw-go/radosgw | peak RSS MiB radosgw | peak RSS MiB rgw-go | rgw-go/radosgw | errors radosgw | errors rgw-go | retries radosgw | retries rgw-go |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| ListObjectsV2 | 1 | 4.03 | 12.8 | 3.18 | 669 | 92 | 0.14 | 635 | 833 | 1.31 | 0 | 0 | 0 | 0 |
| ListObjectsV2, a prefix per thread | 64 | 4.05 | 7.45 | 1.84 | 669 | 100 | 0.15 | 632 | 923 | 1.46 | 0 | 0 | 0 | 0 |

GC cycles over the whole profile, its preparation and removals included, from GODEBUG=gctrace=1; each cycle stops the world twice:

- rgw-go, list: 27004 cycles, longest stop-the-world pause 4700 µs, 8026555 µs in all

## CPU profiles

Each profile covers its profile's last phase, taken from the gateway's /debug/pprof/profile for the phase's time limit; read them with go tool pprof.

- rgw-go: cpu-large-get.pprof, cpu-large-put.pprof, cpu-list.pprof, cpu-noop.pprof, cpu-small-get.pprof, cpu-small-put.pprof

## Parity options

| option | radosgw: client.rgw.ceph.objectstore.a (config show) | radosgw: client.rgw.rgw-go (config get) | rgw-go: client.rgw.ceph.objectstore.a (config show) | rgw-go: client.rgw.rgw-go (config get) |
|---|---|---|---|---|
| rgw_d3n_l1_local_datacache_enabled | false | false | false | false |
| rgw_dynamic_resharding | false | false | false | false |
| rgw_enable_gc_threads | false | false | false | false |
| rgw_enable_lc_threads | false | false | false | false |
| rgw_enable_usage_log | true | true | true | true |
| rgw_run_sync_thread | false | false | false | false |
| rgw_usage_log_flush_threshold | 1 | 1 | 1 | 1 |
| rgw_usage_log_tick_interval | 1 | 1 | 1 | 1 |

## Environment

| env.json | radosgw | rgw-go |
|---|---|---|
| api_name | ceph-objectstore | ceph-objectstore |
| ceph_version | 20.2.4 | 20.2.4 |
| clk_tck | 100 | 100 |
| cluster_image | quay.io/ceph/ceph:v20.2.4 | quay.io/ceph/ceph:v20.2.4 |
| conc | 1 8 32 64 128 256 | 1 8 32 64 128 256 |
| cpu | AMD Ryzen 9 7950X3D 16-Core Processor | AMD Ryzen 9 7950X3D 16-Core Processor |
| datasets | {"large-get":2147483648,"large-put":2147483648,"list":0,"noop":0,"small-get":419430400,"small-put":3145728000} | {"large-get":2147483648,"large-put":2147483648,"list":0,"noop":0,"small-get":419430400,"small-put":3145728000} |
| date | 2026-10-15T19:52:17Z | 2026-10-15T20:23:22Z |
| elbencho | 3.1-12 | 3.1-12 |
| elbencho_commit | 0637a922cbb71d56625f553750dbfbf12e5f0167 | 0637a922cbb71d56625f553750dbfbf12e5f0167 |
| elbencho_image | docker.io/library/alpine:3.24.2@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6 | docker.io/library/alpine:3.24.2@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6 |
| elbencho_packages | boost-dev-1.84.0-r6 brotli-static-1.2.0-r1 c-ares-dev-1.34.8-r0 curl-static-8.22.0-r0 g++-15.2.0-r5 gcc-15.2.0-r5 libidn2-static-2.3.8-r0 libpsl-static-0.21.5-r3 musl-1.2.6-r2 nghttp2-static-1.70.0-r0 nghttp3-static-1.15.0-r0 openssl-libs-static-3.5.9-r0 zlib-static-1.3.2-r1 zstd-static-1.5.7-r2 | boost-dev-1.84.0-r6 brotli-static-1.2.0-r1 c-ares-dev-1.34.8-r0 curl-static-8.22.0-r0 g++-15.2.0-r5 gcc-15.2.0-r5 libidn2-static-2.3.8-r0 libpsl-static-0.21.5-r3 musl-1.2.6-r2 nghttp2-static-1.70.0-r0 nghttp3-static-1.15.0-r0 openssl-libs-static-3.5.9-r0 zlib-static-1.3.2-r1 zstd-static-1.5.7-r2 |
| endpoint | http://10.89.0.34:80 | http://127.0.0.1:7482 |
| frontends | beast port=80 tcp_nodelay=1 | beast endpoint=127.0.0.1:7482 |
| gateway | radosgw | rgw-go |
| get_objects | 400 | 400 |
| git_describe | 49c333f | 49c333f |
| go_tags | ceph_preview | ceph_preview |
| go_version | go version go1.27.1 linux/amd64 | go version go1.27.1 linux/amd64 |
| godebug |  | gctrace=1 |
| gomaxprocs | unset | unset |
| kernel | 7.2.8-100.fc43.x86_64 | 7.2.8-100.fc43.x86_64 |
| large_objects | 1 | 1 |
| large_threads | 32 | 32 |
| list_objects | 1600 | 1600 |
| list_threads | 64 | 64 |
| load | 1.53 5.24 5.79 | 2.57 17.19 24.39 |
| mem_total_kib | 65007268 | 65007268 |
| nproc | 32 | 32 |
| pool_max_avail | 8951574528 | 10049380352 |
| put_objects | 3000 | 3000 |
| radosgw_entity | client.rgw.ceph.objectstore.a | client.rgw.ceph.objectstore.a |
| release | tentacle | tentacle |
| repo | /home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run | /home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run |
| rgw_go_flags |  |  |
| rooket | rooket | rooket |
| rooket_version | rooket v0.0.0-20260928053658-3d8551957e08 | rooket v0.0.0-20260928053658-3d8551957e08 |
| sampled | pod:rook-ceph-rgw-ceph-objectstore-a-5f8c8b4db6-qvv9v | pid:107221 |
| seconds | 60 | 60 |
| smoke | 0 | 0 |
| tcp_nodelay | on | on |

## Commands

Every measured command, as elbencho and httpload recorded it; elbencho leaves out the secret key.

### radosgw

```
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://10.89.0.34:80" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-radosgw-20261015T195140Z/small-put.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-radosgw-20261015T195140Z/small-put.json" "-w" "-t" "1" "-n" "0" "-N" "3000" "-s" "4k" "-b" "4k" "--timelimit" "60" "bench-small"
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://10.89.0.34:80" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-radosgw-20261015T195140Z/small-put.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-radosgw-20261015T195140Z/small-put.json" "-w" "-t" "8" "-n" "0" "-N" "3000" "-s" "4k" "-b" "4k" "--timelimit" "60" "bench-small"
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://10.89.0.34:80" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-radosgw-20261015T195140Z/small-put.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-radosgw-20261015T195140Z/small-put.json" "-w" "-t" "32" "-n" "0" "-N" "3000" "-s" "4k" "-b" "4k" "--timelimit" "60" "bench-small"
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://10.89.0.34:80" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-radosgw-20261015T195140Z/small-put.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-radosgw-20261015T195140Z/small-put.json" "-w" "-t" "64" "-n" "0" "-N" "3000" "-s" "4k" "-b" "4k" "--timelimit" "60" "bench-small"
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://10.89.0.34:80" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-radosgw-20261015T195140Z/small-put.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-radosgw-20261015T195140Z/small-put.json" "-w" "-t" "128" "-n" "0" "-N" "3000" "-s" "4k" "-b" "4k" "--timelimit" "60" "bench-small"
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://10.89.0.34:80" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-radosgw-20261015T195140Z/small-put.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-radosgw-20261015T195140Z/small-put.json" "-w" "-t" "256" "-n" "0" "-N" "3000" "-s" "4k" "-b" "4k" "--timelimit" "60" "bench-small"
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://10.89.0.34:80" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-radosgw-20261015T195140Z/small-get.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-radosgw-20261015T195140Z/small-get.json" "-r" "-t" "1" "-n" "0" "-N" "400" "-s" "4k" "-b" "4k" "--timelimit" "60" "--infloop" "bench-small"
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://10.89.0.34:80" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-radosgw-20261015T195140Z/small-get.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-radosgw-20261015T195140Z/small-get.json" "-r" "-t" "8" "-n" "0" "-N" "400" "-s" "4k" "-b" "4k" "--timelimit" "60" "--infloop" "bench-small"
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://10.89.0.34:80" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-radosgw-20261015T195140Z/small-get.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-radosgw-20261015T195140Z/small-get.json" "-r" "-t" "32" "-n" "0" "-N" "400" "-s" "4k" "-b" "4k" "--timelimit" "60" "--infloop" "bench-small"
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://10.89.0.34:80" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-radosgw-20261015T195140Z/small-get.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-radosgw-20261015T195140Z/small-get.json" "-r" "-t" "64" "-n" "0" "-N" "400" "-s" "4k" "-b" "4k" "--timelimit" "60" "--infloop" "bench-small"
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://10.89.0.34:80" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-radosgw-20261015T195140Z/small-get.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-radosgw-20261015T195140Z/small-get.json" "-r" "-t" "128" "-n" "0" "-N" "400" "-s" "4k" "-b" "4k" "--timelimit" "60" "--infloop" "bench-small"
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://10.89.0.34:80" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-radosgw-20261015T195140Z/small-get.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-radosgw-20261015T195140Z/small-get.json" "-r" "-t" "256" "-n" "0" "-N" "400" "-s" "4k" "-b" "4k" "--timelimit" "60" "--infloop" "bench-small"
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://10.89.0.34:80" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-radosgw-20261015T195140Z/large-put.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-radosgw-20261015T195140Z/large-put.json" "-w" "-t" "32" "-n" "0" "-N" "1" "-s" "64m" "-b" "64m" "--timelimit" "60" "bench-large"
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://10.89.0.34:80" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-radosgw-20261015T195140Z/large-put.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-radosgw-20261015T195140Z/large-put.json" "-w" "-t" "32" "-n" "0" "-N" "1" "-s" "64m" "-b" "8m" "--timelimit" "60" "bench-large-mpu"
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://10.89.0.34:80" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-radosgw-20261015T195140Z/large-get.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-radosgw-20261015T195140Z/large-get.json" "-r" "-t" "32" "-n" "0" "-N" "1" "-s" "64m" "-b" "4m" "--timelimit" "60" "--infloop" "bench-large"
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://10.89.0.34:80" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-radosgw-20261015T195140Z/large-get.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-radosgw-20261015T195140Z/large-get.json" "-r" "-t" "32" "-n" "0" "-N" "1" "-s" "64m" "-b" "64m" "--timelimit" "60" "--infloop" "bench-large"
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://10.89.0.34:80" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-radosgw-20261015T195140Z/list.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-radosgw-20261015T195140Z/list.json" "--s3listobj" "102400" "-t" "1" "--timelimit" "60" "--infloop" "bench-list"
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://10.89.0.34:80" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-radosgw-20261015T195140Z/list.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-radosgw-20261015T195140Z/list.json" "--s3listobjpar" "-t" "64" "-n" "0" "-N" "1600" "--timelimit" "60" "--infloop" "bench-list"
httpload -url http://10.89.0.34:80/ -threads 1 -duration 60s -out /home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-radosgw-20261015T195140Z/noop-1.json
httpload -url http://10.89.0.34:80/ -threads 128 -duration 60s -out /home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-radosgw-20261015T195140Z/noop-128.json
httpload -url http://10.89.0.34:80/ -threads 256 -duration 60s -out /home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-radosgw-20261015T195140Z/noop-256.json
httpload -url http://10.89.0.34:80/ -threads 32 -duration 60s -out /home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-radosgw-20261015T195140Z/noop-32.json
httpload -url http://10.89.0.34:80/ -threads 64 -duration 60s -out /home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-radosgw-20261015T195140Z/noop-64.json
httpload -url http://10.89.0.34:80/ -threads 8 -duration 60s -out /home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-radosgw-20261015T195140Z/noop-8.json
```

### rgw-go

```
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://127.0.0.1:7482" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-rgw-go-20261015T202210Z/small-put.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-rgw-go-20261015T202210Z/small-put.json" "-w" "-t" "1" "-n" "0" "-N" "3000" "-s" "4k" "-b" "4k" "--timelimit" "60" "bench-small"
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://127.0.0.1:7482" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-rgw-go-20261015T202210Z/small-put.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-rgw-go-20261015T202210Z/small-put.json" "-w" "-t" "8" "-n" "0" "-N" "3000" "-s" "4k" "-b" "4k" "--timelimit" "60" "bench-small"
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://127.0.0.1:7482" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-rgw-go-20261015T202210Z/small-put.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-rgw-go-20261015T202210Z/small-put.json" "-w" "-t" "32" "-n" "0" "-N" "3000" "-s" "4k" "-b" "4k" "--timelimit" "60" "bench-small"
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://127.0.0.1:7482" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-rgw-go-20261015T202210Z/small-put.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-rgw-go-20261015T202210Z/small-put.json" "-w" "-t" "64" "-n" "0" "-N" "3000" "-s" "4k" "-b" "4k" "--timelimit" "60" "bench-small"
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://127.0.0.1:7482" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-rgw-go-20261015T202210Z/small-put.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-rgw-go-20261015T202210Z/small-put.json" "-w" "-t" "128" "-n" "0" "-N" "3000" "-s" "4k" "-b" "4k" "--timelimit" "60" "bench-small"
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://127.0.0.1:7482" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-rgw-go-20261015T202210Z/small-put.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-rgw-go-20261015T202210Z/small-put.json" "-w" "-t" "256" "-n" "0" "-N" "3000" "-s" "4k" "-b" "4k" "--timelimit" "60" "bench-small"
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://127.0.0.1:7482" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-rgw-go-20261015T202210Z/small-get.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-rgw-go-20261015T202210Z/small-get.json" "-r" "-t" "1" "-n" "0" "-N" "400" "-s" "4k" "-b" "4k" "--timelimit" "60" "--infloop" "bench-small"
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://127.0.0.1:7482" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-rgw-go-20261015T202210Z/small-get.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-rgw-go-20261015T202210Z/small-get.json" "-r" "-t" "8" "-n" "0" "-N" "400" "-s" "4k" "-b" "4k" "--timelimit" "60" "--infloop" "bench-small"
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://127.0.0.1:7482" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-rgw-go-20261015T202210Z/small-get.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-rgw-go-20261015T202210Z/small-get.json" "-r" "-t" "32" "-n" "0" "-N" "400" "-s" "4k" "-b" "4k" "--timelimit" "60" "--infloop" "bench-small"
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://127.0.0.1:7482" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-rgw-go-20261015T202210Z/small-get.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-rgw-go-20261015T202210Z/small-get.json" "-r" "-t" "64" "-n" "0" "-N" "400" "-s" "4k" "-b" "4k" "--timelimit" "60" "--infloop" "bench-small"
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://127.0.0.1:7482" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-rgw-go-20261015T202210Z/small-get.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-rgw-go-20261015T202210Z/small-get.json" "-r" "-t" "128" "-n" "0" "-N" "400" "-s" "4k" "-b" "4k" "--timelimit" "60" "--infloop" "bench-small"
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://127.0.0.1:7482" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-rgw-go-20261015T202210Z/small-get.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-rgw-go-20261015T202210Z/small-get.json" "-r" "-t" "256" "-n" "0" "-N" "400" "-s" "4k" "-b" "4k" "--timelimit" "60" "--infloop" "bench-small"
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://127.0.0.1:7482" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-rgw-go-20261015T202210Z/large-put.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-rgw-go-20261015T202210Z/large-put.json" "-w" "-t" "32" "-n" "0" "-N" "1" "-s" "64m" "-b" "64m" "--timelimit" "60" "bench-large"
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://127.0.0.1:7482" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-rgw-go-20261015T202210Z/large-put.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-rgw-go-20261015T202210Z/large-put.json" "-w" "-t" "32" "-n" "0" "-N" "1" "-s" "64m" "-b" "8m" "--timelimit" "60" "bench-large-mpu"
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://127.0.0.1:7482" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-rgw-go-20261015T202210Z/large-get.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-rgw-go-20261015T202210Z/large-get.json" "-r" "-t" "32" "-n" "0" "-N" "1" "-s" "64m" "-b" "4m" "--timelimit" "60" "--infloop" "bench-large"
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://127.0.0.1:7482" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-rgw-go-20261015T202210Z/large-get.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-rgw-go-20261015T202210Z/large-get.json" "-r" "-t" "32" "-n" "0" "-N" "1" "-s" "64m" "-b" "64m" "--timelimit" "60" "--infloop" "bench-large"
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://127.0.0.1:7482" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-rgw-go-20261015T202210Z/list.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-rgw-go-20261015T202210Z/list.json" "--s3listobj" "102400" "-t" "1" "--timelimit" "60" "--infloop" "bench-list"
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://127.0.0.1:7482" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-rgw-go-20261015T202210Z/list.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-rgw-go-20261015T202210Z/list.json" "--s3listobjpar" "-t" "64" "-n" "0" "-N" "1600" "--timelimit" "60" "--infloop" "bench-list"
httpload -url http://127.0.0.1:7482/ -threads 1 -duration 60s -out /home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-rgw-go-20261015T202210Z/noop-1.json
httpload -url http://127.0.0.1:7482/ -threads 128 -duration 60s -out /home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-rgw-go-20261015T202210Z/noop-128.json
httpload -url http://127.0.0.1:7482/ -threads 256 -duration 60s -out /home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-rgw-go-20261015T202210Z/noop-256.json
httpload -url http://127.0.0.1:7482/ -threads 32 -duration 60s -out /home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-rgw-go-20261015T202210Z/noop-32.json
httpload -url http://127.0.0.1:7482/ -threads 64 -duration 60s -out /home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-rgw-go-20261015T202210Z/noop-64.json
httpload -url http://127.0.0.1:7482/ -threads 8 -duration 60s -out /home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/tentacle/gateway-rgw-go-20261015T202210Z/noop-8.json
```
