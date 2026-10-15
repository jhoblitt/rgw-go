# Gateway comparison: squid

- radosgw: docs/benchmarks/2026-10-15-gateway-squid/radosgw; frontends `beast port=80 tcp_nodelay=1`, TCP_NODELAY on
- rgw-go: docs/benchmarks/2026-10-15-gateway-squid/rgw-go; frontends `beast endpoint=127.0.0.1:7481`, TCP_NODELAY on

Each row is one measured phase. req/s counts S3 requests: elbencho's IOs, a listing's ListObjectsV2 calls, httpload's GETs. A multipart PUT's row, marked (parts/s), counts as elbencho does: its req/s counts UploadPart requests, leaving out each upload's Create and Complete, and its CPU per request is per part. objects/s counts objects written or read, or keys listed. The latency percentiles are per request, from elbencho's latency histogram as its --latpercent9s prints them, or httpload's. CPU per request is the gateway process's user and system time over the phase, from the samples taken once a second around it, interpolated at its start and end, so a short phase's is coarse; a 2 s gap after every phase and every preparation keeps their CPU out of the next phase's. Peak RSS is the highest of those samples, and thread high-water the most OS threads among them: Go parks an idle OS thread rather than ending it, so for rgw-go it is the most the run has needed by the phase's end, its preparations included, not the phase's own demand. A ratio column divides a run's value by radosgw's.

## Findings

No phase had an error or a retry.

## small-put

### small-put: throughput and latency

| variant | threads | req/s radosgw | req/s rgw-go | rgw-go/radosgw | objects/s radosgw | objects/s rgw-go | rgw-go/radosgw | MiB/s radosgw | MiB/s rgw-go | rgw-go/radosgw | p50 ms radosgw | p50 ms rgw-go | rgw-go/radosgw | p99 ms radosgw | p99 ms rgw-go | rgw-go/radosgw | p999 ms radosgw | p999 ms rgw-go | rgw-go/radosgw |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| PUT 4 KiB | 1 | 109 | 109 | 1.00 | 109 | 109 | 1.00 | 0.42 | 0.43 | 1.00 | 9.09 | 9.09 | 1.00 | 21.2 | 20.2 | 0.95 | 35.3 | 27.1 | 0.77 |
| PUT 4 KiB | 8 | 473 | 474 | 1.00 | 473 | 474 | 1.00 | 1.85 | 1.85 | 1.00 | 11.8 | 11.6 | 0.99 | 28.4 | 27.4 | 0.96 | 46.6 | 47.1 | 1.01 |
| PUT 4 KiB | 32 | 922 | 962 | 1.04 | 922 | 962 | 1.04 | 3.60 | 3.76 | 1.04 | 32.3 | 31.0 | 0.96 | 68.6 | 64.5 | 0.94 | 120 | 87.0 | 0.73 |
| PUT 4 KiB | 64 | 911 | 962 | 1.06 | 911 | 962 | 1.06 | 3.56 | 3.76 | 1.06 | 64.5 | 61.4 | 0.95 | 128 | 121 | 0.94 | 158 | 180 | 1.14 |
| PUT 4 KiB | 128 | 899 | 948 | 1.06 | 899 | 948 | 1.06 | 3.52 | 3.71 | 1.05 | 126 | 120 | 0.95 | 244 | 242 | 0.99 | 352 | 336 | 0.95 |
| PUT 4 KiB | 256 | 903 | 958 | 1.06 | 903 | 958 | 1.06 | 3.54 | 3.76 | 1.06 | 248 | 229 | 0.93 | 496 | 479 | 0.97 | 631 | 573 | 0.91 |

### small-put: cost

| variant | threads | CPU ms/req radosgw | CPU ms/req rgw-go | rgw-go/radosgw | thread high-water radosgw | thread high-water rgw-go | rgw-go/radosgw | peak RSS MiB radosgw | peak RSS MiB rgw-go | rgw-go/radosgw | errors radosgw | errors rgw-go | retries radosgw | retries rgw-go |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| PUT 4 KiB | 1 | 0.88 | 0.69 | 0.78 | 603 | 33 | 0.05 | 176 | 54.2 | 0.31 | 0 | 0 | 0 | 0 |
| PUT 4 KiB | 8 | 0.88 | 0.76 | 0.86 | 603 | 38 | 0.06 | 187 | 63.4 | 0.34 | 0 | 0 | 0 | 0 |
| PUT 4 KiB | 32 | 1.14 | 0.89 | 0.78 | 603 | 50 | 0.08 | 200 | 93.0 | 0.46 | 0 | 0 | 0 | 0 |
| PUT 4 KiB | 64 | 1.17 | 0.82 | 0.70 | 603 | 58 | 0.10 | 208 | 125 | 0.60 | 0 | 0 | 0 | 0 |
| PUT 4 KiB | 128 | 1.19 | 0.73 | 0.61 | 603 | 68 | 0.11 | 222 | 187 | 0.84 | 0 | 0 | 0 | 0 |
| PUT 4 KiB | 256 | 1.21 | 0.67 | 0.56 | 603 | 79 | 0.13 | 250 | 315 | 1.26 | 0 | 0 | 0 | 0 |

GC cycles over the whole profile, its preparation and removals included, from GODEBUG=gctrace=1; each cycle stops the world twice:

- rgw-go, small-put: 13960 cycles, longest stop-the-world pause 5200 µs, 1681255 µs in all

## small-get and noop

### small-get and noop: throughput and latency

| variant | threads | req/s radosgw | req/s rgw-go | rgw-go/radosgw | objects/s radosgw | objects/s rgw-go | rgw-go/radosgw | MiB/s radosgw | MiB/s rgw-go | rgw-go/radosgw | p50 ms radosgw | p50 ms rgw-go | rgw-go/radosgw | p99 ms radosgw | p99 ms rgw-go | rgw-go/radosgw | p999 ms radosgw | p999 ms rgw-go | rgw-go/radosgw |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| GET 4 KiB | 1 | 1442 | 1730 | 1.20 | 1442 | 1730 | 1.20 | 5.63 | 6.76 | 1.20 | 0.69 | 0.56 | 0.81 | 0.91 | 1.02 | 1.12 | 1.25 | 1.22 | 0.97 |
| anonymous GET / | 1 | 4384 | 13571 | 3.10 | — | — | — | — | — | — | 0.22 | 0.07 | 0.30 | 0.32 | 0.19 | 0.58 | 0.43 | 0.36 | 0.85 |
| GET 4 KiB | 8 | 8576 | 7022 | 0.82 | 8576 | 7022 | 0.82 | 33.5 | 27.4 | 0.82 | 0.92 | 1.14 | 1.23 | 1.26 | 1.66 | 1.32 | 1.86 | 2.11 | 1.14 |
| anonymous GET / | 8 | 17364 | 39730 | 2.29 | — | — | — | — | — | — | 0.45 | 0.15 | 0.33 | 0.72 | 0.53 | 0.74 | 0.98 | 0.66 | 0.67 |
| GET 4 KiB | 32 | 13426 | 11669 | 0.87 | 13426 | 11669 | 0.87 | 52.4 | 45.6 | 0.87 | 2.34 | 2.53 | 1.08 | 3.65 | 5.50 | 1.51 | 4.61 | 7.36 | 1.60 |
| anonymous GET / | 32 | 19112 | 46666 | 2.44 | — | — | — | — | — | — | 1.56 | 0.46 | 0.29 | 3.81 | 3.00 | 0.79 | 5.03 | 4.32 | 0.86 |
| GET 4 KiB | 64 | 13650 | 14747 | 1.08 | 13650 | 14747 | 1.08 | 53.3 | 57.6 | 1.08 | 4.67 | 3.97 | 0.85 | 7.36 | 9.98 | 1.36 | 8.70 | 22.5 | 2.59 |
| anonymous GET / | 64 | 18279 | 69373 | 3.80 | — | — | — | — | — | — | 3.17 | 0.41 | 0.13 | 8.07 | 5.19 | 0.64 | 10.2 | 7.73 | 0.76 |
| GET 4 KiB | 128 | 13396 | 18698 | 1.40 | 13396 | 18698 | 1.40 | 52.3 | 73.0 | 1.40 | 9.34 | 6.46 | 0.69 | 15.0 | 14.3 | 0.96 | 42.5 | 42.5 | 1.00 |
| anonymous GET / | 128 | 18094 | 90763 | 5.02 | — | — | — | — | — | — | 6.64 | 0.44 | 0.07 | 16.4 | 9.89 | 0.60 | 21.1 | 15.0 | 0.71 |
| GET 4 KiB | 256 | 13193 | 21173 | 1.60 | 13193 | 21173 | 1.60 | 51.5 | 82.7 | 1.60 | 18.7 | 11.8 | 0.63 | 44.5 | 24.8 | 0.56 | 79.9 | 65.0 | 0.81 |
| anonymous GET / | 256 | 17311 | 155547 | 8.99 | — | — | — | — | — | — | 14.3 | 0.63 | 0.04 | 30.7 | 13.3 | 0.43 | 37.5 | 21.2 | 0.57 |

### small-get and noop: cost

| variant | threads | CPU ms/req radosgw | CPU ms/req rgw-go | rgw-go/radosgw | thread high-water radosgw | thread high-water rgw-go | rgw-go/radosgw | peak RSS MiB radosgw | peak RSS MiB rgw-go | rgw-go/radosgw | errors radosgw | errors rgw-go | retries radosgw | retries rgw-go |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| GET 4 KiB | 1 | 0.62 | 0.42 | 0.67 | 603 | 88 | 0.15 | 258 | 370 | 1.43 | 0 | 0 | 0 | 0 |
| anonymous GET / | 1 | 0.38 | 0.07 | 0.19 | 603 | 90 | 0.15 | 248 | 370 | 1.49 | 0 | 0 | — | — |
| GET 4 KiB | 8 | 0.74 | 0.73 | 0.98 | 603 | 88 | 0.15 | 258 | 346 | 1.34 | 0 | 0 | 0 | 0 |
| anonymous GET / | 8 | 0.72 | 0.16 | 0.22 | 603 | 90 | 0.15 | 248 | 351 | 1.41 | 0 | 0 | — | — |
| GET 4 KiB | 32 | 0.89 | 0.74 | 0.83 | 603 | 88 | 0.15 | 259 | 351 | 1.36 | 0 | 0 | 0 | 0 |
| anonymous GET / | 32 | 0.93 | 0.20 | 0.21 | 603 | 90 | 0.15 | 249 | 353 | 1.42 | 0 | 0 | — | — |
| GET 4 KiB | 64 | 0.93 | 0.64 | 0.69 | 603 | 88 | 0.15 | 259 | 354 | 1.37 | 0 | 0 | 0 | 0 |
| anonymous GET / | 64 | 0.98 | 0.16 | 0.16 | 603 | 90 | 0.15 | 250 | 357 | 1.43 | 0 | 0 | — | — |
| GET 4 KiB | 128 | 0.93 | 0.52 | 0.56 | 603 | 88 | 0.15 | 261 | 359 | 1.38 | 0 | 0 | 0 | 0 |
| anonymous GET / | 128 | 0.93 | 0.13 | 0.14 | 603 | 90 | 0.15 | 251 | 365 | 1.45 | 0 | 0 | — | — |
| GET 4 KiB | 256 | 0.92 | 0.38 | 0.41 | 603 | 90 | 0.15 | 264 | 381 | 1.44 | 0 | 0 | 0 | 0 |
| anonymous GET / | 256 | 1.05 | 0.09 | 0.08 | 603 | 90 | 0.15 | 253 | 393 | 1.55 | 0 | 0 | — | — |

GC cycles over the whole profile, its preparation and removals included, from GODEBUG=gctrace=1; each cycle stops the world twice. A torn line is a cycle's line cut short, by a record of rgw-go's JSON log, which shares its stderr, or by the end of the log the run read; one torn too early to be kept is known from the gap it leaves in the cycles' numbers. A torn line's cycle counts, and its pauses are in neither the longest nor the total:

- rgw-go, small-get: 151063 cycles, longest stop-the-world pause 755000 µs, 30628838 µs in all
- rgw-go, noop: 156698 cycles, longest stop-the-world pause 3600 µs, 31892038 µs in all; 1 torn line, its cycle's pauses unknown

## large-put

### large-put: throughput and latency

| variant | threads | req/s radosgw | req/s rgw-go | rgw-go/radosgw | objects/s radosgw | objects/s rgw-go | rgw-go/radosgw | MiB/s radosgw | MiB/s rgw-go | rgw-go/radosgw | p50 ms radosgw | p50 ms rgw-go | rgw-go/radosgw | p99 ms radosgw | p99 ms rgw-go | rgw-go/radosgw | p999 ms radosgw | p999 ms rgw-go | rgw-go/radosgw |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| PUT 64 MiB | 32 | 6.62 | 5.46 | 0.82 | 6.62 | 5.46 | 0.82 | 424 | 349 | 0.82 | 4784 | 5767 | 1.21 | 4850 | 5898 | 1.22 | 4850 | 5898 | 1.22 |
| PUT 64 MiB in 8 MiB parts (parts/s) | 32 | 49.8 | 53.8 | 1.08 | 6.23 | 6.72 | 1.08 | 399 | 430 | 1.08 | 606 | 573 | 0.95 | 860 | 647 | 0.75 | 983 | 860 | 0.88 |

### large-put: cost

| variant | threads | CPU ms/req radosgw | CPU ms/req rgw-go | rgw-go/radosgw | thread high-water radosgw | thread high-water rgw-go | rgw-go/radosgw | peak RSS MiB radosgw | peak RSS MiB rgw-go | rgw-go/radosgw | errors radosgw | errors rgw-go | retries radosgw | retries rgw-go |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| PUT 64 MiB | 32 | 188 | 275 | 1.46 | 603 | 90 | 0.15 | 565 | 1952 | 3.45 | 0 | 0 | 0 | 0 |
| PUT 64 MiB in 8 MiB parts (parts/s) | 32 | 28.9 | 27.3 | 0.94 | 603 | 90 | 0.15 | 579 | 2508 | 4.33 | 0 | 0 | 0 | 0 |

GC cycles over the whole profile, its preparation and removals included, from GODEBUG=gctrace=1; each cycle stops the world twice:

- rgw-go, large-put: 8 cycles, longest stop-the-world pause 5500 µs, 21346 µs in all

## large-get

### large-get: throughput and latency

| variant | threads | req/s radosgw | req/s rgw-go | rgw-go/radosgw | objects/s radosgw | objects/s rgw-go | rgw-go/radosgw | MiB/s radosgw | MiB/s rgw-go | rgw-go/radosgw | p50 ms radosgw | p50 ms rgw-go | rgw-go/radosgw | p99 ms radosgw | p99 ms rgw-go | rgw-go/radosgw | p999 ms radosgw | p999 ms rgw-go | rgw-go/radosgw |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| GET 64 MiB in 4 MiB ranges | 32 | 105 | 144 | 1.36 | 6.38 | 8.67 | 1.36 | 421 | 574 | 1.36 | 299 | 215 | 0.72 | 496 | 381 | 0.77 | 762 | 434 | 0.57 |
| GET 64 MiB | 32 | 12.4 | 12.9 | 1.04 | 12.4 | 12.9 | 1.04 | 807 | 834 | 1.03 | 2556 | 2359 | 0.92 | 3703 | 3015 | 0.81 | 4325 | 3375 | 0.78 |

### large-get: cost

| variant | threads | CPU ms/req radosgw | CPU ms/req rgw-go | rgw-go/radosgw | thread high-water radosgw | thread high-water rgw-go | rgw-go/radosgw | peak RSS MiB radosgw | peak RSS MiB rgw-go | rgw-go/radosgw | errors radosgw | errors rgw-go | retries radosgw | retries rgw-go |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| GET 64 MiB in 4 MiB ranges | 32 | 2.49 | 1.86 | 0.75 | 603 | 90 | 0.15 | 583 | 2658 | 4.56 | 0 | 0 | 0 | 0 |
| GET 64 MiB | 32 | 29.6 | 26.1 | 0.88 | 603 | 90 | 0.15 | 584 | 2849 | 4.88 | 0 | 0 | 0 | 0 |

GC cycles over the whole profile, its preparation and removals included, from GODEBUG=gctrace=1; each cycle stops the world twice:

- rgw-go, large-get: 2 cycles, longest stop-the-world pause 2900 µs, 5825 µs in all

## list

### list: throughput and latency

| variant | threads | req/s radosgw | req/s rgw-go | rgw-go/radosgw | objects/s radosgw | objects/s rgw-go | rgw-go/radosgw | MiB/s radosgw | MiB/s rgw-go | rgw-go/radosgw | p50 ms radosgw | p50 ms rgw-go | rgw-go/radosgw | p99 ms radosgw | p99 ms rgw-go | rgw-go/radosgw | p999 ms radosgw | p999 ms rgw-go | rgw-go/radosgw |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| ListObjectsV2 | 1 | 113 | 107 | 0.95 | 112067 | 106256 | 0.95 | — | — | — | 8.96 | 9.34 | 1.04 | 10.6 | 10.9 | 1.02 | 11.6 | 11.9 | 1.02 |
| ListObjectsV2, a prefix per thread | 64 | 1045 | 1001 | 0.96 | 836208 | 800802 | 0.96 | — | — | — | 60.4 | 63.5 | 1.05 | 72.7 | 74.8 | 1.03 | 104 | 81.9 | 0.78 |

### list: cost

| variant | threads | CPU ms/req radosgw | CPU ms/req rgw-go | rgw-go/radosgw | thread high-water radosgw | thread high-water rgw-go | rgw-go/radosgw | peak RSS MiB radosgw | peak RSS MiB rgw-go | rgw-go/radosgw | errors radosgw | errors rgw-go | retries radosgw | retries rgw-go |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| ListObjectsV2 | 1 | 4.51 | 13.3 | 2.94 | 603 | 90 | 0.15 | 612 | 814 | 1.33 | 0 | 0 | 0 | 0 |
| ListObjectsV2, a prefix per thread | 64 | 4.48 | 8.38 | 1.87 | 603 | 90 | 0.15 | 578 | 843 | 1.46 | 0 | 0 | 0 | 0 |

GC cycles over the whole profile, its preparation and removals included, from GODEBUG=gctrace=1; each cycle stops the world twice:

- rgw-go, list: 33153 cycles, longest stop-the-world pause 4500 µs, 3909227 µs in all

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
| ceph_version | 19.2.7 | 19.2.7 |
| clk_tck | 100 | 100 |
| cluster_image | quay.io/ceph/ceph:v19.2.7 | quay.io/ceph/ceph:v19.2.7 |
| conc | 1 8 32 64 128 256 | 1 8 32 64 128 256 |
| cpu | AMD Ryzen 9 7950X3D 16-Core Processor | AMD Ryzen 9 7950X3D 16-Core Processor |
| datasets | {"large-get":2147483648,"large-put":2147483648,"list":0,"noop":0,"small-get":419430400,"small-put":3145728000} | {"large-get":2147483648,"large-put":2147483648,"list":0,"noop":0,"small-get":419430400,"small-put":3145728000} |
| date | 2026-10-15T09:57:46Z | 2026-10-15T10:33:29Z |
| elbencho | 3.1-12 | 3.1-12 |
| elbencho_commit | 0637a922cbb71d56625f553750dbfbf12e5f0167 | 0637a922cbb71d56625f553750dbfbf12e5f0167 |
| elbencho_image | docker.io/library/alpine:3.24.2@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6 | docker.io/library/alpine:3.24.2@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6 |
| elbencho_packages | boost-dev-1.84.0-r6 brotli-static-1.2.0-r1 c-ares-dev-1.34.8-r0 curl-static-8.22.0-r0 g++-15.2.0-r5 gcc-15.2.0-r5 libidn2-static-2.3.8-r0 libpsl-static-0.21.5-r3 musl-1.2.6-r2 nghttp2-static-1.70.0-r0 nghttp3-static-1.15.0-r0 openssl-libs-static-3.5.9-r0 zlib-static-1.3.2-r1 zstd-static-1.5.7-r2 | boost-dev-1.84.0-r6 brotli-static-1.2.0-r1 c-ares-dev-1.34.8-r0 curl-static-8.22.0-r0 g++-15.2.0-r5 gcc-15.2.0-r5 libidn2-static-2.3.8-r0 libpsl-static-0.21.5-r3 musl-1.2.6-r2 nghttp2-static-1.70.0-r0 nghttp3-static-1.15.0-r0 openssl-libs-static-3.5.9-r0 zlib-static-1.3.2-r1 zstd-static-1.5.7-r2 |
| endpoint | http://10.89.0.40:80 | http://127.0.0.1:7481 |
| frontends | beast port=80 tcp_nodelay=1 | beast endpoint=127.0.0.1:7481 |
| gateway | radosgw | rgw-go |
| get_objects | 400 | 400 |
| git_describe | 252972a | 252972a |
| go_tags | ceph_preview | ceph_preview |
| go_version | go version go1.27.1 linux/amd64 | go version go1.27.1 linux/amd64 |
| godebug |  | gctrace=1 |
| gomaxprocs | unset | unset |
| kernel | 7.2.8-100.fc43.x86_64 | 7.2.8-100.fc43.x86_64 |
| large_objects | 1 | 1 |
| large_threads | 32 | 32 |
| list_objects | 1600 | 1600 |
| list_threads | 64 | 64 |
| load | 1.83 2.29 2.15 | 2.68 20.77 33.71 |
| mem_total_kib | 65007268 | 65007268 |
| nproc | 32 | 32 |
| pool_max_avail | 9595482112 | 10114347008 |
| put_objects | 3000 | 3000 |
| radosgw_entity | client.rgw.ceph.objectstore.a | client.rgw.ceph.objectstore.a |
| release | squid | squid |
| repo | /home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run | /home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run |
| rgw_go_flags |  |  |
| rooket | rooket | rooket |
| rooket_version | rooket v0.0.0-20260928053658-3d8551957e08 | rooket v0.0.0-20260928053658-3d8551957e08 |
| sampled | pod:rook-ceph-rgw-ceph-objectstore-a-78f948894c-dd2mb | pid:812973 |
| seconds | 60 | 60 |
| smoke | 0 | 0 |
| tcp_nodelay | on | on |

## Commands

Every measured command, as elbencho and httpload recorded it; elbencho leaves out the secret key.

### radosgw

```
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://10.89.0.40:80" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-radosgw-20261015T095711Z/small-put.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-radosgw-20261015T095711Z/small-put.json" "-w" "-t" "1" "-n" "0" "-N" "3000" "-s" "4k" "-b" "4k" "--timelimit" "60" "bench-small"
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://10.89.0.40:80" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-radosgw-20261015T095711Z/small-put.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-radosgw-20261015T095711Z/small-put.json" "-w" "-t" "8" "-n" "0" "-N" "3000" "-s" "4k" "-b" "4k" "--timelimit" "60" "bench-small"
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://10.89.0.40:80" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-radosgw-20261015T095711Z/small-put.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-radosgw-20261015T095711Z/small-put.json" "-w" "-t" "32" "-n" "0" "-N" "3000" "-s" "4k" "-b" "4k" "--timelimit" "60" "bench-small"
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://10.89.0.40:80" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-radosgw-20261015T095711Z/small-put.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-radosgw-20261015T095711Z/small-put.json" "-w" "-t" "64" "-n" "0" "-N" "3000" "-s" "4k" "-b" "4k" "--timelimit" "60" "bench-small"
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://10.89.0.40:80" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-radosgw-20261015T095711Z/small-put.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-radosgw-20261015T095711Z/small-put.json" "-w" "-t" "128" "-n" "0" "-N" "3000" "-s" "4k" "-b" "4k" "--timelimit" "60" "bench-small"
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://10.89.0.40:80" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-radosgw-20261015T095711Z/small-put.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-radosgw-20261015T095711Z/small-put.json" "-w" "-t" "256" "-n" "0" "-N" "3000" "-s" "4k" "-b" "4k" "--timelimit" "60" "bench-small"
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://10.89.0.40:80" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-radosgw-20261015T095711Z/small-get.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-radosgw-20261015T095711Z/small-get.json" "-r" "-t" "1" "-n" "0" "-N" "400" "-s" "4k" "-b" "4k" "--timelimit" "60" "--infloop" "bench-small"
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://10.89.0.40:80" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-radosgw-20261015T095711Z/small-get.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-radosgw-20261015T095711Z/small-get.json" "-r" "-t" "8" "-n" "0" "-N" "400" "-s" "4k" "-b" "4k" "--timelimit" "60" "--infloop" "bench-small"
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://10.89.0.40:80" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-radosgw-20261015T095711Z/small-get.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-radosgw-20261015T095711Z/small-get.json" "-r" "-t" "32" "-n" "0" "-N" "400" "-s" "4k" "-b" "4k" "--timelimit" "60" "--infloop" "bench-small"
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://10.89.0.40:80" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-radosgw-20261015T095711Z/small-get.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-radosgw-20261015T095711Z/small-get.json" "-r" "-t" "64" "-n" "0" "-N" "400" "-s" "4k" "-b" "4k" "--timelimit" "60" "--infloop" "bench-small"
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://10.89.0.40:80" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-radosgw-20261015T095711Z/small-get.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-radosgw-20261015T095711Z/small-get.json" "-r" "-t" "128" "-n" "0" "-N" "400" "-s" "4k" "-b" "4k" "--timelimit" "60" "--infloop" "bench-small"
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://10.89.0.40:80" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-radosgw-20261015T095711Z/small-get.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-radosgw-20261015T095711Z/small-get.json" "-r" "-t" "256" "-n" "0" "-N" "400" "-s" "4k" "-b" "4k" "--timelimit" "60" "--infloop" "bench-small"
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://10.89.0.40:80" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-radosgw-20261015T095711Z/large-put.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-radosgw-20261015T095711Z/large-put.json" "-w" "-t" "32" "-n" "0" "-N" "1" "-s" "64m" "-b" "64m" "--timelimit" "60" "bench-large"
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://10.89.0.40:80" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-radosgw-20261015T095711Z/large-put.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-radosgw-20261015T095711Z/large-put.json" "-w" "-t" "32" "-n" "0" "-N" "1" "-s" "64m" "-b" "8m" "--timelimit" "60" "bench-large-mpu"
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://10.89.0.40:80" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-radosgw-20261015T095711Z/large-get.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-radosgw-20261015T095711Z/large-get.json" "-r" "-t" "32" "-n" "0" "-N" "1" "-s" "64m" "-b" "4m" "--timelimit" "60" "--infloop" "bench-large"
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://10.89.0.40:80" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-radosgw-20261015T095711Z/large-get.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-radosgw-20261015T095711Z/large-get.json" "-r" "-t" "32" "-n" "0" "-N" "1" "-s" "64m" "-b" "64m" "--timelimit" "60" "--infloop" "bench-large"
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://10.89.0.40:80" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-radosgw-20261015T095711Z/list.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-radosgw-20261015T095711Z/list.json" "--s3listobj" "102400" "-t" "1" "--timelimit" "60" "--infloop" "bench-list"
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://10.89.0.40:80" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-radosgw-20261015T095711Z/list.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-radosgw-20261015T095711Z/list.json" "--s3listobjpar" "-t" "64" "-n" "0" "-N" "1600" "--timelimit" "60" "--infloop" "bench-list"
httpload -url http://10.89.0.40:80/ -threads 1 -duration 60s -out /home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-radosgw-20261015T095711Z/noop-1.json
httpload -url http://10.89.0.40:80/ -threads 128 -duration 60s -out /home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-radosgw-20261015T095711Z/noop-128.json
httpload -url http://10.89.0.40:80/ -threads 256 -duration 60s -out /home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-radosgw-20261015T095711Z/noop-256.json
httpload -url http://10.89.0.40:80/ -threads 32 -duration 60s -out /home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-radosgw-20261015T095711Z/noop-32.json
httpload -url http://10.89.0.40:80/ -threads 64 -duration 60s -out /home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-radosgw-20261015T095711Z/noop-64.json
httpload -url http://10.89.0.40:80/ -threads 8 -duration 60s -out /home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-radosgw-20261015T095711Z/noop-8.json
```

### rgw-go

```
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://127.0.0.1:7481" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-rgw-go-20261015T103216Z/small-put.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-rgw-go-20261015T103216Z/small-put.json" "-w" "-t" "1" "-n" "0" "-N" "3000" "-s" "4k" "-b" "4k" "--timelimit" "60" "bench-small"
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://127.0.0.1:7481" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-rgw-go-20261015T103216Z/small-put.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-rgw-go-20261015T103216Z/small-put.json" "-w" "-t" "8" "-n" "0" "-N" "3000" "-s" "4k" "-b" "4k" "--timelimit" "60" "bench-small"
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://127.0.0.1:7481" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-rgw-go-20261015T103216Z/small-put.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-rgw-go-20261015T103216Z/small-put.json" "-w" "-t" "32" "-n" "0" "-N" "3000" "-s" "4k" "-b" "4k" "--timelimit" "60" "bench-small"
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://127.0.0.1:7481" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-rgw-go-20261015T103216Z/small-put.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-rgw-go-20261015T103216Z/small-put.json" "-w" "-t" "64" "-n" "0" "-N" "3000" "-s" "4k" "-b" "4k" "--timelimit" "60" "bench-small"
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://127.0.0.1:7481" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-rgw-go-20261015T103216Z/small-put.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-rgw-go-20261015T103216Z/small-put.json" "-w" "-t" "128" "-n" "0" "-N" "3000" "-s" "4k" "-b" "4k" "--timelimit" "60" "bench-small"
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://127.0.0.1:7481" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-rgw-go-20261015T103216Z/small-put.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-rgw-go-20261015T103216Z/small-put.json" "-w" "-t" "256" "-n" "0" "-N" "3000" "-s" "4k" "-b" "4k" "--timelimit" "60" "bench-small"
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://127.0.0.1:7481" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-rgw-go-20261015T103216Z/small-get.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-rgw-go-20261015T103216Z/small-get.json" "-r" "-t" "1" "-n" "0" "-N" "400" "-s" "4k" "-b" "4k" "--timelimit" "60" "--infloop" "bench-small"
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://127.0.0.1:7481" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-rgw-go-20261015T103216Z/small-get.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-rgw-go-20261015T103216Z/small-get.json" "-r" "-t" "8" "-n" "0" "-N" "400" "-s" "4k" "-b" "4k" "--timelimit" "60" "--infloop" "bench-small"
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://127.0.0.1:7481" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-rgw-go-20261015T103216Z/small-get.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-rgw-go-20261015T103216Z/small-get.json" "-r" "-t" "32" "-n" "0" "-N" "400" "-s" "4k" "-b" "4k" "--timelimit" "60" "--infloop" "bench-small"
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://127.0.0.1:7481" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-rgw-go-20261015T103216Z/small-get.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-rgw-go-20261015T103216Z/small-get.json" "-r" "-t" "64" "-n" "0" "-N" "400" "-s" "4k" "-b" "4k" "--timelimit" "60" "--infloop" "bench-small"
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://127.0.0.1:7481" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-rgw-go-20261015T103216Z/small-get.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-rgw-go-20261015T103216Z/small-get.json" "-r" "-t" "128" "-n" "0" "-N" "400" "-s" "4k" "-b" "4k" "--timelimit" "60" "--infloop" "bench-small"
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://127.0.0.1:7481" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-rgw-go-20261015T103216Z/small-get.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-rgw-go-20261015T103216Z/small-get.json" "-r" "-t" "256" "-n" "0" "-N" "400" "-s" "4k" "-b" "4k" "--timelimit" "60" "--infloop" "bench-small"
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://127.0.0.1:7481" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-rgw-go-20261015T103216Z/large-put.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-rgw-go-20261015T103216Z/large-put.json" "-w" "-t" "32" "-n" "0" "-N" "1" "-s" "64m" "-b" "64m" "--timelimit" "60" "bench-large"
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://127.0.0.1:7481" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-rgw-go-20261015T103216Z/large-put.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-rgw-go-20261015T103216Z/large-put.json" "-w" "-t" "32" "-n" "0" "-N" "1" "-s" "64m" "-b" "8m" "--timelimit" "60" "bench-large-mpu"
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://127.0.0.1:7481" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-rgw-go-20261015T103216Z/large-get.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-rgw-go-20261015T103216Z/large-get.json" "-r" "-t" "32" "-n" "0" "-N" "1" "-s" "64m" "-b" "4m" "--timelimit" "60" "--infloop" "bench-large"
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://127.0.0.1:7481" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-rgw-go-20261015T103216Z/large-get.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-rgw-go-20261015T103216Z/large-get.json" "-r" "-t" "32" "-n" "0" "-N" "1" "-s" "64m" "-b" "64m" "--timelimit" "60" "--infloop" "bench-large"
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://127.0.0.1:7481" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-rgw-go-20261015T103216Z/list.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-rgw-go-20261015T103216Z/list.json" "--s3listobj" "102400" "-t" "1" "--timelimit" "60" "--infloop" "bench-list"
"/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/elbencho" "--s3endpoints" "http://127.0.0.1:7481" "--s3key" "BENCH0000000000000AK" "--s3region" "ceph-objectstore" "--s3nocompress" "--s3reqtimeout" "30000" "--nolive" "--lat" "--latpercent" "--latpercent9s" "1" "--lathisto" "--csvfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-rgw-go-20261015T103216Z/list.csv" "--jsonfile" "/home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-rgw-go-20261015T103216Z/list.json" "--s3listobjpar" "-t" "64" "-n" "0" "-N" "1600" "--timelimit" "60" "--infloop" "bench-list"
httpload -url http://127.0.0.1:7481/ -threads 1 -duration 60s -out /home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-rgw-go-20261015T103216Z/noop-1.json
httpload -url http://127.0.0.1:7481/ -threads 128 -duration 60s -out /home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-rgw-go-20261015T103216Z/noop-128.json
httpload -url http://127.0.0.1:7481/ -threads 256 -duration 60s -out /home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-rgw-go-20261015T103216Z/noop-256.json
httpload -url http://127.0.0.1:7481/ -threads 32 -duration 60s -out /home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-rgw-go-20261015T103216Z/noop-32.json
httpload -url http://127.0.0.1:7481/ -threads 64 -duration 60s -out /home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-rgw-go-20261015T103216Z/noop-64.json
httpload -url http://127.0.0.1:7481/ -threads 8 -duration 60s -out /home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run/hack/bench/out/squid/gateway-rgw-go-20261015T103216Z/noop-8.json
```
