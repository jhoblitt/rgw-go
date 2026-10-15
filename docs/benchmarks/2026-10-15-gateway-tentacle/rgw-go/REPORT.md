# Gateway comparison: tentacle

- rgw-go: docs/benchmarks/2026-10-15-gateway-tentacle/rgw-go; frontends `beast endpoint=127.0.0.1:7482`, TCP_NODELAY on

Each row is one measured phase. req/s counts S3 requests: elbencho's IOs, a listing's ListObjectsV2 calls, httpload's GETs. A multipart PUT's row, marked (parts/s), counts as elbencho does: its req/s counts UploadPart requests, leaving out each upload's Create and Complete, and its CPU per request is per part. objects/s counts objects written or read, or keys listed. The latency percentiles are per request, from elbencho's latency histogram as its --latpercent9s prints them, or httpload's. CPU per request is the gateway process's user and system time over the phase, from the samples taken once a second around it, interpolated at its start and end, so a short phase's is coarse; a 2 s gap after every phase and every preparation keeps their CPU out of the next phase's. Peak RSS is the highest of those samples, and thread high-water the most OS threads among them: Go parks an idle OS thread rather than ending it, so for rgw-go it is the most the run has needed by the phase's end, its preparations included, not the phase's own demand.

## Findings

No phase had an error or a retry.

## small-put

### small-put: throughput and latency

| variant | threads | req/s rgw-go | objects/s rgw-go | MiB/s rgw-go | p50 ms rgw-go | p99 ms rgw-go | p999 ms rgw-go |
|---|---|---|---|---|---|---|---|
| PUT 4 KiB | 1 | 205 | 205 | 0.80 | 4.86 | 12.3 | 23.8 |
| PUT 4 KiB | 8 | 908 | 908 | 3.55 | 6.02 | 18.7 | 29.7 |
| PUT 4 KiB | 32 | 1588 | 1588 | 6.20 | 16.6 | 41.5 | 74.8 |
| PUT 4 KiB | 64 | 1687 | 1687 | 6.59 | 34.8 | 79.9 | 141 |
| PUT 4 KiB | 128 | 1746 | 1746 | 6.83 | 66.6 | 133 | 168 |
| PUT 4 KiB | 256 | 1746 | 1746 | 6.84 | 133 | 270 | 377 |

### small-put: cost

| variant | threads | CPU ms/req rgw-go | thread high-water rgw-go | peak RSS MiB rgw-go | errors rgw-go | retries rgw-go |
|---|---|---|---|---|---|---|
| PUT 4 KiB | 1 | 0.58 | 33 | 57.5 | 0 | 0 |
| PUT 4 KiB | 8 | 0.69 | 38 | 66.3 | 0 | 0 |
| PUT 4 KiB | 32 | 0.81 | 49 | 94.2 | 0 | 0 |
| PUT 4 KiB | 64 | 0.77 | 58 | 133 | 0 | 0 |
| PUT 4 KiB | 128 | 0.70 | 72 | 208 | 0 | 0 |
| PUT 4 KiB | 256 | 0.62 | 92 | 355 | 0 | 0 |

GC cycles over the whole profile, its preparation and removals included, from GODEBUG=gctrace=1; each cycle stops the world twice:

- rgw-go, small-put: 20488 cycles, longest stop-the-world pause 3500 µs, 2492931 µs in all

## small-get and noop

### small-get and noop: throughput and latency

| variant | threads | req/s rgw-go | objects/s rgw-go | MiB/s rgw-go | p50 ms rgw-go | p99 ms rgw-go | p999 ms rgw-go |
|---|---|---|---|---|---|---|---|
| GET 4 KiB | 1 | 1755 | 1755 | 6.86 | 0.55 | 0.99 | 1.17 |
| anonymous GET / | 1 | 13746 | — | — | 0.07 | 0.17 | 0.33 |
| GET 4 KiB | 8 | 7277 | 7277 | 28.4 | 1.12 | 1.65 | 1.87 |
| anonymous GET / | 8 | 40356 | — | — | 0.14 | 0.53 | 0.65 |
| GET 4 KiB | 32 | 11673 | 11673 | 45.6 | 2.56 | 5.63 | 17.4 |
| anonymous GET / | 32 | 47994 | — | — | 0.44 | 2.89 | 4.17 |
| GET 4 KiB | 64 | 14862 | 14862 | 58.1 | 3.94 | 9.86 | 22.3 |
| anonymous GET / | 64 | 64548 | — | — | 0.44 | 5.60 | 8.25 |
| GET 4 KiB | 128 | 19173 | 19173 | 74.9 | 6.40 | 12.8 | 25.9 |
| anonymous GET / | 128 | 89044 | — | — | 0.45 | 9.88 | 14.8 |
| GET 4 KiB | 256 | 17879 | 17879 | 69.8 | 11.9 | 71.7 | 114 |
| anonymous GET / | 256 | 157233 | — | — | 0.64 | 12.9 | 20.6 |

### small-get and noop: cost

| variant | threads | CPU ms/req rgw-go | thread high-water rgw-go | peak RSS MiB rgw-go | errors rgw-go | retries rgw-go |
|---|---|---|---|---|---|---|
| GET 4 KiB | 1 | 0.41 | 92 | 365 | 0 | 0 |
| anonymous GET / | 1 | 0.07 | 92 | 369 | 0 | — |
| GET 4 KiB | 8 | 0.69 | 92 | 346 | 0 | 0 |
| anonymous GET / | 8 | 0.16 | 92 | 348 | 0 | — |
| GET 4 KiB | 32 | 0.75 | 92 | 350 | 0 | 0 |
| anonymous GET / | 32 | 0.19 | 92 | 352 | 0 | — |
| GET 4 KiB | 64 | 0.64 | 92 | 354 | 0 | 0 |
| anonymous GET / | 64 | 0.16 | 92 | 354 | 0 | — |
| GET 4 KiB | 128 | 0.51 | 92 | 358 | 0 | 0 |
| anonymous GET / | 128 | 0.14 | 92 | 363 | 0 | — |
| GET 4 KiB | 256 | 0.39 | 92 | 378 | 0 | 0 |
| anonymous GET / | 256 | 0.09 | 92 | 392 | 0 | — |

GC cycles over the whole profile, its preparation and removals included, from GODEBUG=gctrace=1; each cycle stops the world twice. A torn line is a cycle's line cut short, by a record of rgw-go's JSON log, which shares its stderr, or by the end of the log the run read; one torn too early to be kept is known from the gap it leaves in the cycles' numbers. A torn line's cycle counts, and its pauses are in neither the longest nor the total:

- rgw-go, small-get: 151452 cycles, longest stop-the-world pause 6500 µs, 28801899 µs in all
- rgw-go, noop: 159205 cycles, longest stop-the-world pause 4400 µs, 31560127 µs in all; 2 torn lines, their cycles' pauses unknown

## large-put

### large-put: throughput and latency

| variant | threads | req/s rgw-go | objects/s rgw-go | MiB/s rgw-go | p50 ms rgw-go | p99 ms rgw-go | p999 ms rgw-go |
|---|---|---|---|---|---|---|---|
| PUT 64 MiB | 32 | 8.95 | 8.95 | 573 | 3473 | 3572 | 3572 |
| PUT 64 MiB in 8 MiB parts (parts/s) | 32 | 66.4 | 8.31 | 532 | 463 | 532 | 541 |

### large-put: cost

| variant | threads | CPU ms/req rgw-go | thread high-water rgw-go | peak RSS MiB rgw-go | errors rgw-go | retries rgw-go |
|---|---|---|---|---|---|---|
| PUT 64 MiB | 32 | 206 | 92 | 1982 | 0 | 0 |
| PUT 64 MiB in 8 MiB parts (parts/s) | 32 | 26.3 | 92 | 2565 | 0 | 0 |

GC cycles over the whole profile, its preparation and removals included, from GODEBUG=gctrace=1; each cycle stops the world twice:

- rgw-go, large-put: 7 cycles, longest stop-the-world pause 3300 µs, 17171 µs in all

## large-get

### large-get: throughput and latency

| variant | threads | req/s rgw-go | objects/s rgw-go | MiB/s rgw-go | p50 ms rgw-go | p99 ms rgw-go | p999 ms rgw-go |
|---|---|---|---|---|---|---|---|
| GET 64 MiB in 4 MiB ranges | 32 | 108 | 6.38 | 431 | 291 | 389 | 418 |
| GET 64 MiB | 32 | 9.95 | 9.95 | 647 | 3113 | 3539 | 3801 |

### large-get: cost

| variant | threads | CPU ms/req rgw-go | thread high-water rgw-go | peak RSS MiB rgw-go | errors rgw-go | retries rgw-go |
|---|---|---|---|---|---|---|
| GET 64 MiB in 4 MiB ranges | 32 | 1.95 | 92 | 3068 | 0 | 0 |
| GET 64 MiB | 32 | 26.9 | 92 | 3068 | 0 | 0 |

GC cycles over the whole profile, its preparation and removals included, from GODEBUG=gctrace=1; each cycle stops the world twice:

- rgw-go, large-get: 1 cycles, longest stop-the-world pause 53.0 µs, 61.0 µs in all

## list

### list: throughput and latency

| variant | threads | req/s rgw-go | objects/s rgw-go | MiB/s rgw-go | p50 ms rgw-go | p99 ms rgw-go | p999 ms rgw-go |
|---|---|---|---|---|---|---|---|
| ListObjectsV2 | 1 | 113 | 111891 | — | 8.96 | 10.4 | 11.8 |
| ListObjectsV2, a prefix per thread | 64 | 1469 | 1175119 | — | 42.0 | 76.8 | 92.2 |

### list: cost

| variant | threads | CPU ms/req rgw-go | thread high-water rgw-go | peak RSS MiB rgw-go | errors rgw-go | retries rgw-go |
|---|---|---|---|---|---|---|
| ListObjectsV2 | 1 | 12.8 | 92 | 833 | 0 | 0 |
| ListObjectsV2, a prefix per thread | 64 | 7.45 | 100 | 923 | 0 | 0 |

GC cycles over the whole profile, its preparation and removals included, from GODEBUG=gctrace=1; each cycle stops the world twice:

- rgw-go, list: 27004 cycles, longest stop-the-world pause 4700 µs, 8026555 µs in all

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
| ceph_version | 20.2.4 |
| clk_tck | 100 |
| cluster_image | quay.io/ceph/ceph:v20.2.4 |
| conc | 1 8 32 64 128 256 |
| cpu | AMD Ryzen 9 7950X3D 16-Core Processor |
| datasets | {"large-get":2147483648,"large-put":2147483648,"list":0,"noop":0,"small-get":419430400,"small-put":3145728000} |
| date | 2026-10-15T20:23:22Z |
| elbencho | 3.1-12 |
| elbencho_commit | 0637a922cbb71d56625f553750dbfbf12e5f0167 |
| elbencho_image | docker.io/library/alpine:3.24.2@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6 |
| elbencho_packages | boost-dev-1.84.0-r6 brotli-static-1.2.0-r1 c-ares-dev-1.34.8-r0 curl-static-8.22.0-r0 g++-15.2.0-r5 gcc-15.2.0-r5 libidn2-static-2.3.8-r0 libpsl-static-0.21.5-r3 musl-1.2.6-r2 nghttp2-static-1.70.0-r0 nghttp3-static-1.15.0-r0 openssl-libs-static-3.5.9-r0 zlib-static-1.3.2-r1 zstd-static-1.5.7-r2 |
| endpoint | http://127.0.0.1:7482 |
| frontends | beast endpoint=127.0.0.1:7482 |
| gateway | rgw-go |
| get_objects | 400 |
| git_describe | 49c333f |
| go_tags | ceph_preview |
| go_version | go version go1.27.1 linux/amd64 |
| godebug | gctrace=1 |
| gomaxprocs | unset |
| kernel | 7.2.8-100.fc43.x86_64 |
| large_objects | 1 |
| large_threads | 32 |
| list_objects | 1600 |
| list_threads | 64 |
| load | 2.57 17.19 24.39 |
| mem_total_kib | 65007268 |
| nproc | 32 |
| pool_max_avail | 10049380352 |
| put_objects | 3000 |
| radosgw_entity | client.rgw.ceph.objectstore.a |
| release | tentacle |
| repo | /home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run |
| rgw_go_flags |  |
| rooket | rooket |
| rooket_version | rooket v0.0.0-20260928053658-3d8551957e08 |
| sampled | pid:107221 |
| seconds | 60 |
| smoke | 0 |
| tcp_nodelay | on |

## Commands

Every measured command, as elbencho and httpload recorded it; elbencho leaves out the secret key.

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
