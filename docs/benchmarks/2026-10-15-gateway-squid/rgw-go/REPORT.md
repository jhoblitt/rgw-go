# Gateway comparison: squid

- rgw-go: docs/benchmarks/2026-10-15-gateway-squid/rgw-go; frontends `beast endpoint=127.0.0.1:7481`, TCP_NODELAY on

Each row is one measured phase. req/s counts S3 requests: elbencho's IOs, a listing's ListObjectsV2 calls, httpload's GETs. A multipart PUT's row, marked (parts/s), counts as elbencho does: its req/s counts UploadPart requests, leaving out each upload's Create and Complete, and its CPU per request is per part. objects/s counts objects written or read, or keys listed. The latency percentiles are per request, from elbencho's latency histogram as its --latpercent9s prints them, or httpload's. CPU per request is the gateway process's user and system time over the phase, from the samples taken once a second around it, interpolated at its start and end, so a short phase's is coarse; a 2 s gap after every phase and every preparation keeps their CPU out of the next phase's. Peak RSS is the highest of those samples, and thread high-water the most OS threads among them: Go parks an idle OS thread rather than ending it, so for rgw-go it is the most the run has needed by the phase's end, its preparations included, not the phase's own demand.

## Findings

No phase had an error or a retry.

## small-put

### small-put: throughput and latency

| variant | threads | req/s rgw-go | objects/s rgw-go | MiB/s rgw-go | p50 ms rgw-go | p99 ms rgw-go | p999 ms rgw-go |
|---|---|---|---|---|---|---|---|
| PUT 4 KiB | 1 | 109 | 109 | 0.43 | 9.09 | 20.2 | 27.1 |
| PUT 4 KiB | 8 | 474 | 474 | 1.85 | 11.6 | 27.4 | 47.1 |
| PUT 4 KiB | 32 | 962 | 962 | 3.76 | 31.0 | 64.5 | 87.0 |
| PUT 4 KiB | 64 | 962 | 962 | 3.76 | 61.4 | 121 | 180 |
| PUT 4 KiB | 128 | 948 | 948 | 3.71 | 120 | 242 | 336 |
| PUT 4 KiB | 256 | 958 | 958 | 3.76 | 229 | 479 | 573 |

### small-put: cost

| variant | threads | CPU ms/req rgw-go | thread high-water rgw-go | peak RSS MiB rgw-go | errors rgw-go | retries rgw-go |
|---|---|---|---|---|---|---|
| PUT 4 KiB | 1 | 0.69 | 33 | 54.2 | 0 | 0 |
| PUT 4 KiB | 8 | 0.76 | 38 | 63.4 | 0 | 0 |
| PUT 4 KiB | 32 | 0.89 | 50 | 93.0 | 0 | 0 |
| PUT 4 KiB | 64 | 0.82 | 58 | 125 | 0 | 0 |
| PUT 4 KiB | 128 | 0.73 | 68 | 187 | 0 | 0 |
| PUT 4 KiB | 256 | 0.67 | 79 | 315 | 0 | 0 |

GC cycles over the whole profile, its preparation and removals included, from GODEBUG=gctrace=1; each cycle stops the world twice:

- rgw-go, small-put: 13960 cycles, longest stop-the-world pause 5200 µs, 1681255 µs in all

## small-get and noop

### small-get and noop: throughput and latency

| variant | threads | req/s rgw-go | objects/s rgw-go | MiB/s rgw-go | p50 ms rgw-go | p99 ms rgw-go | p999 ms rgw-go |
|---|---|---|---|---|---|---|---|
| GET 4 KiB | 1 | 1730 | 1730 | 6.76 | 0.56 | 1.02 | 1.22 |
| anonymous GET / | 1 | 13571 | — | — | 0.07 | 0.19 | 0.36 |
| GET 4 KiB | 8 | 7022 | 7022 | 27.4 | 1.14 | 1.66 | 2.11 |
| anonymous GET / | 8 | 39730 | — | — | 0.15 | 0.53 | 0.66 |
| GET 4 KiB | 32 | 11669 | 11669 | 45.6 | 2.53 | 5.50 | 7.36 |
| anonymous GET / | 32 | 46666 | — | — | 0.46 | 3.00 | 4.32 |
| GET 4 KiB | 64 | 14747 | 14747 | 57.6 | 3.97 | 9.98 | 22.5 |
| anonymous GET / | 64 | 69373 | — | — | 0.41 | 5.19 | 7.73 |
| GET 4 KiB | 128 | 18698 | 18698 | 73.0 | 6.46 | 14.3 | 42.5 |
| anonymous GET / | 128 | 90763 | — | — | 0.44 | 9.89 | 15.0 |
| GET 4 KiB | 256 | 21173 | 21173 | 82.7 | 11.8 | 24.8 | 65.0 |
| anonymous GET / | 256 | 155547 | — | — | 0.63 | 13.3 | 21.2 |

### small-get and noop: cost

| variant | threads | CPU ms/req rgw-go | thread high-water rgw-go | peak RSS MiB rgw-go | errors rgw-go | retries rgw-go |
|---|---|---|---|---|---|---|
| GET 4 KiB | 1 | 0.42 | 88 | 370 | 0 | 0 |
| anonymous GET / | 1 | 0.07 | 90 | 370 | 0 | — |
| GET 4 KiB | 8 | 0.73 | 88 | 346 | 0 | 0 |
| anonymous GET / | 8 | 0.16 | 90 | 351 | 0 | — |
| GET 4 KiB | 32 | 0.74 | 88 | 351 | 0 | 0 |
| anonymous GET / | 32 | 0.20 | 90 | 353 | 0 | — |
| GET 4 KiB | 64 | 0.64 | 88 | 354 | 0 | 0 |
| anonymous GET / | 64 | 0.16 | 90 | 357 | 0 | — |
| GET 4 KiB | 128 | 0.52 | 88 | 359 | 0 | 0 |
| anonymous GET / | 128 | 0.13 | 90 | 365 | 0 | — |
| GET 4 KiB | 256 | 0.38 | 90 | 381 | 0 | 0 |
| anonymous GET / | 256 | 0.09 | 90 | 393 | 0 | — |

GC cycles over the whole profile, its preparation and removals included, from GODEBUG=gctrace=1; each cycle stops the world twice. A torn line is a cycle's line cut short, by a record of rgw-go's JSON log, which shares its stderr, or by the end of the log the run read; one torn too early to be kept is known from the gap it leaves in the cycles' numbers. A torn line's cycle counts, and its pauses are in neither the longest nor the total:

- rgw-go, small-get: 151063 cycles, longest stop-the-world pause 755000 µs, 30628838 µs in all
- rgw-go, noop: 156698 cycles, longest stop-the-world pause 3600 µs, 31892038 µs in all; 1 torn line, its cycle's pauses unknown

## large-put

### large-put: throughput and latency

| variant | threads | req/s rgw-go | objects/s rgw-go | MiB/s rgw-go | p50 ms rgw-go | p99 ms rgw-go | p999 ms rgw-go |
|---|---|---|---|---|---|---|---|
| PUT 64 MiB | 32 | 5.46 | 5.46 | 349 | 5767 | 5898 | 5898 |
| PUT 64 MiB in 8 MiB parts (parts/s) | 32 | 53.8 | 6.72 | 430 | 573 | 647 | 860 |

### large-put: cost

| variant | threads | CPU ms/req rgw-go | thread high-water rgw-go | peak RSS MiB rgw-go | errors rgw-go | retries rgw-go |
|---|---|---|---|---|---|---|
| PUT 64 MiB | 32 | 275 | 90 | 1952 | 0 | 0 |
| PUT 64 MiB in 8 MiB parts (parts/s) | 32 | 27.3 | 90 | 2508 | 0 | 0 |

GC cycles over the whole profile, its preparation and removals included, from GODEBUG=gctrace=1; each cycle stops the world twice:

- rgw-go, large-put: 8 cycles, longest stop-the-world pause 5500 µs, 21346 µs in all

## large-get

### large-get: throughput and latency

| variant | threads | req/s rgw-go | objects/s rgw-go | MiB/s rgw-go | p50 ms rgw-go | p99 ms rgw-go | p999 ms rgw-go |
|---|---|---|---|---|---|---|---|
| GET 64 MiB in 4 MiB ranges | 32 | 144 | 8.67 | 574 | 215 | 381 | 434 |
| GET 64 MiB | 32 | 12.9 | 12.9 | 834 | 2359 | 3015 | 3375 |

### large-get: cost

| variant | threads | CPU ms/req rgw-go | thread high-water rgw-go | peak RSS MiB rgw-go | errors rgw-go | retries rgw-go |
|---|---|---|---|---|---|---|
| GET 64 MiB in 4 MiB ranges | 32 | 1.86 | 90 | 2658 | 0 | 0 |
| GET 64 MiB | 32 | 26.1 | 90 | 2849 | 0 | 0 |

GC cycles over the whole profile, its preparation and removals included, from GODEBUG=gctrace=1; each cycle stops the world twice:

- rgw-go, large-get: 2 cycles, longest stop-the-world pause 2900 µs, 5825 µs in all

## list

### list: throughput and latency

| variant | threads | req/s rgw-go | objects/s rgw-go | MiB/s rgw-go | p50 ms rgw-go | p99 ms rgw-go | p999 ms rgw-go |
|---|---|---|---|---|---|---|---|
| ListObjectsV2 | 1 | 107 | 106256 | — | 9.34 | 10.9 | 11.9 |
| ListObjectsV2, a prefix per thread | 64 | 1001 | 800802 | — | 63.5 | 74.8 | 81.9 |

### list: cost

| variant | threads | CPU ms/req rgw-go | thread high-water rgw-go | peak RSS MiB rgw-go | errors rgw-go | retries rgw-go |
|---|---|---|---|---|---|---|
| ListObjectsV2 | 1 | 13.3 | 90 | 814 | 0 | 0 |
| ListObjectsV2, a prefix per thread | 64 | 8.38 | 90 | 843 | 0 | 0 |

GC cycles over the whole profile, its preparation and removals included, from GODEBUG=gctrace=1; each cycle stops the world twice:

- rgw-go, list: 33153 cycles, longest stop-the-world pause 4500 µs, 3909227 µs in all

## CPU profiles

Each profile covers its profile's last phase, taken from the gateway's /debug/pprof/profile for the phase's time limit; read them with go tool pprof.

- rgw-go: cpu-large-get.pprof, cpu-large-put.pprof, cpu-list.pprof, cpu-noop.pprof, cpu-small-get.pprof, cpu-small-put.pprof

## Parity options

| option | rgw-go: client.rgw.ceph.objectstore.a (config show) | rgw-go: client.rgw.rgw-go (config get) |
|---|---|---|
| rgw_d3n_l1_local_datacache_enabled | false | false |
| rgw_dynamic_resharding | false | false |
| rgw_enable_gc_threads | false | false |
| rgw_enable_lc_threads | false | false |
| rgw_enable_usage_log | true | true |
| rgw_run_sync_thread | false | false |
| rgw_usage_log_flush_threshold | 1 | 1 |
| rgw_usage_log_tick_interval | 1 | 1 |

## Environment

| env.json | rgw-go |
|---|---|
| api_name | ceph-objectstore |
| ceph_version | 19.2.7 |
| clk_tck | 100 |
| cluster_image | quay.io/ceph/ceph:v19.2.7 |
| conc | 1 8 32 64 128 256 |
| cpu | AMD Ryzen 9 7950X3D 16-Core Processor |
| datasets | {"large-get":2147483648,"large-put":2147483648,"list":0,"noop":0,"small-get":419430400,"small-put":3145728000} |
| date | 2026-10-15T10:33:29Z |
| elbencho | 3.1-12 |
| elbencho_commit | 0637a922cbb71d56625f553750dbfbf12e5f0167 |
| elbencho_image | docker.io/library/alpine:3.24.2@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6 |
| elbencho_packages | boost-dev-1.84.0-r6 brotli-static-1.2.0-r1 c-ares-dev-1.34.8-r0 curl-static-8.22.0-r0 g++-15.2.0-r5 gcc-15.2.0-r5 libidn2-static-2.3.8-r0 libpsl-static-0.21.5-r3 musl-1.2.6-r2 nghttp2-static-1.70.0-r0 nghttp3-static-1.15.0-r0 openssl-libs-static-3.5.9-r0 zlib-static-1.3.2-r1 zstd-static-1.5.7-r2 |
| endpoint | http://127.0.0.1:7481 |
| frontends | beast endpoint=127.0.0.1:7481 |
| gateway | rgw-go |
| get_objects | 400 |
| git_describe | 252972a |
| go_tags | ceph_preview |
| go_version | go version go1.27.1 linux/amd64 |
| godebug | gctrace=1 |
| gomaxprocs | unset |
| kernel | 7.2.8-100.fc43.x86_64 |
| large_objects | 1 |
| large_threads | 32 |
| list_objects | 1600 |
| list_threads | 64 |
| load | 2.68 20.77 33.71 |
| mem_total_kib | 65007268 |
| nproc | 32 |
| pool_max_avail | 10114347008 |
| put_objects | 3000 |
| radosgw_entity | client.rgw.ceph.objectstore.a |
| release | squid |
| repo | /home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run |
| rgw_go_flags |  |
| rooket | rooket |
| rooket_version | rooket v0.0.0-20260928053658-3d8551957e08 |
| sampled | pid:812973 |
| seconds | 60 |
| smoke | 0 |
| tcp_nodelay | on |

## Commands

Every measured command, as elbencho and httpload recorded it; elbencho leaves out the secret key.

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
