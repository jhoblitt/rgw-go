# Gateway comparison: tentacle

- radosgw: docs/benchmarks/2026-10-15-gateway-tentacle/radosgw; frontends `beast port=80 tcp_nodelay=1`, TCP_NODELAY on

Each row is one measured phase. req/s counts S3 requests: elbencho's IOs, a listing's ListObjectsV2 calls, httpload's GETs. A multipart PUT's row, marked (parts/s), counts as elbencho does: its req/s counts UploadPart requests, leaving out each upload's Create and Complete, and its CPU per request is per part. objects/s counts objects written or read, or keys listed. The latency percentiles are per request, from elbencho's latency histogram as its --latpercent9s prints them, or httpload's. CPU per request is the gateway process's user and system time over the phase, from the samples taken once a second around it, interpolated at its start and end, so a short phase's is coarse; a 2 s gap after every phase and every preparation keeps their CPU out of the next phase's. Peak RSS is the highest of those samples, and thread high-water the most OS threads among them: Go parks an idle OS thread rather than ending it, so for rgw-go it is the most the run has needed by the phase's end, its preparations included, not the phase's own demand. A ratio column divides a run's value by radosgw's.

## Findings

No phase had an error or a retry.

## small-put

### small-put: throughput and latency

| variant | threads | req/s radosgw | objects/s radosgw | MiB/s radosgw | p50 ms radosgw | p99 ms radosgw | p999 ms radosgw |
|---|---|---|---|---|---|---|---|
| PUT 4 KiB | 1 | 202 | 202 | 0.79 | 4.86 | 12.2 | 25.3 |
| PUT 4 KiB | 8 | 850 | 850 | 3.32 | 6.40 | 17.7 | 24.8 |
| PUT 4 KiB | 32 | 1532 | 1532 | 5.99 | 17.9 | 43.5 | 97.3 |
| PUT 4 KiB | 64 | 1550 | 1550 | 6.06 | 37.4 | 86.0 | 223 |
| PUT 4 KiB | 128 | 1596 | 1596 | 6.24 | 70.7 | 152 | 209 |
| PUT 4 KiB | 256 | 1664 | 1664 | 6.52 | 135 | 270 | 442 |

### small-put: cost

| variant | threads | CPU ms/req radosgw | thread high-water radosgw | peak RSS MiB radosgw | errors radosgw | retries radosgw |
|---|---|---|---|---|---|---|
| PUT 4 KiB | 1 | 0.82 | 669 | 249 | 0 | 0 |
| PUT 4 KiB | 8 | 0.81 | 669 | 249 | 0 | 0 |
| PUT 4 KiB | 32 | 1.09 | 669 | 251 | 0 | 0 |
| PUT 4 KiB | 64 | 1.21 | 669 | 252 | 0 | 0 |
| PUT 4 KiB | 128 | 1.23 | 669 | 254 | 0 | 0 |
| PUT 4 KiB | 256 | 1.25 | 669 | 264 | 0 | 0 |

## small-get and noop

### small-get and noop: throughput and latency

| variant | threads | req/s radosgw | objects/s radosgw | MiB/s radosgw | p50 ms radosgw | p99 ms radosgw | p999 ms radosgw |
|---|---|---|---|---|---|---|---|
| GET 4 KiB | 1 | 1480 | 1480 | 5.78 | 0.66 | 0.89 | 1.20 |
| anonymous GET / | 1 | 4451 | — | — | 0.22 | 0.31 | 0.41 |
| GET 4 KiB | 8 | 8541 | 8541 | 33.4 | 0.93 | 1.26 | 1.79 |
| anonymous GET / | 8 | 17248 | — | — | 0.45 | 0.70 | 0.96 |
| GET 4 KiB | 32 | 12986 | 12986 | 50.7 | 2.43 | 3.78 | 5.44 |
| anonymous GET / | 32 | 18437 | — | — | 1.64 | 3.77 | 4.90 |
| GET 4 KiB | 64 | 12976 | 12976 | 50.7 | 4.86 | 7.81 | 23.6 |
| anonymous GET / | 64 | 18394 | — | — | 3.20 | 9.03 | 11.9 |
| GET 4 KiB | 128 | 12487 | 12487 | 48.8 | 9.60 | 30.0 | 52.7 |
| anonymous GET / | 128 | 18336 | — | — | 6.14 | 20.7 | 28.3 |
| GET 4 KiB | 256 | 10962 | 10962 | 42.8 | 18.4 | 94.2 | 120 |
| anonymous GET / | 256 | 18127 | — | — | 12.2 | 43.3 | 61.0 |

### small-get and noop: cost

| variant | threads | CPU ms/req radosgw | thread high-water radosgw | peak RSS MiB radosgw | errors radosgw | retries radosgw |
|---|---|---|---|---|---|---|
| GET 4 KiB | 1 | 0.59 | 669 | 255 | 0 | 0 |
| anonymous GET / | 1 | 0.36 | 669 | 246 | 0 | — |
| GET 4 KiB | 8 | 0.74 | 669 | 255 | 0 | 0 |
| anonymous GET / | 8 | 0.72 | 669 | 247 | 0 | — |
| GET 4 KiB | 32 | 0.92 | 669 | 256 | 0 | 0 |
| anonymous GET / | 32 | 0.99 | 669 | 247 | 0 | — |
| GET 4 KiB | 64 | 0.95 | 669 | 257 | 0 | 0 |
| anonymous GET / | 64 | 0.98 | 669 | 248 | 0 | — |
| GET 4 KiB | 128 | 0.96 | 669 | 259 | 0 | 0 |
| anonymous GET / | 128 | 0.95 | 669 | 249 | 0 | — |
| GET 4 KiB | 256 | 0.89 | 669 | 263 | 0 | 0 |
| anonymous GET / | 256 | 0.95 | 669 | 252 | 0 | — |

## large-put

### large-put: throughput and latency

| variant | threads | req/s radosgw | objects/s radosgw | MiB/s radosgw | p50 ms radosgw | p99 ms radosgw | p999 ms radosgw |
|---|---|---|---|---|---|---|---|
| PUT 64 MiB | 32 | 8.61 | 8.61 | 551 | 3637 | 3703 | 3703 |
| PUT 64 MiB in 8 MiB parts (parts/s) | 32 | 66.5 | 8.32 | 532 | 471 | 496 | 516 |

### large-put: cost

| variant | threads | CPU ms/req radosgw | thread high-water radosgw | peak RSS MiB radosgw | errors radosgw | retries radosgw |
|---|---|---|---|---|---|---|
| PUT 64 MiB | 32 | 196 | 669 | 602 | 0 | 0 |
| PUT 64 MiB in 8 MiB parts (parts/s) | 32 | 30.9 | 669 | 602 | 0 | 0 |

## large-get

### large-get: throughput and latency

| variant | threads | req/s radosgw | objects/s radosgw | MiB/s radosgw | p50 ms radosgw | p99 ms radosgw | p999 ms radosgw |
|---|---|---|---|---|---|---|---|
| GET 64 MiB in 4 MiB ranges | 32 | 107 | 6.42 | 428 | 291 | 418 | 1786 |
| GET 64 MiB | 32 | 9.63 | 9.63 | 625 | 3244 | 3736 | 3801 |

### large-get: cost

| variant | threads | CPU ms/req radosgw | thread high-water radosgw | peak RSS MiB radosgw | errors radosgw | retries radosgw |
|---|---|---|---|---|---|---|
| GET 64 MiB in 4 MiB ranges | 32 | 2.45 | 669 | 600 | 0 | 0 |
| GET 64 MiB | 32 | 29.2 | 669 | 616 | 0 | 0 |

## list

### list: throughput and latency

| variant | threads | req/s radosgw | objects/s radosgw | MiB/s radosgw | p50 ms radosgw | p99 ms radosgw | p999 ms radosgw |
|---|---|---|---|---|---|---|---|
| ListObjectsV2 | 1 | 120 | 119788 | — | 8.32 | 10.1 | 11.1 |
| ListObjectsV2, a prefix per thread | 64 | 1017 | 814104 | — | 62.5 | 74.8 | 98.3 |

### list: cost

| variant | threads | CPU ms/req radosgw | thread high-water radosgw | peak RSS MiB radosgw | errors radosgw | retries radosgw |
|---|---|---|---|---|---|---|
| ListObjectsV2 | 1 | 4.03 | 669 | 635 | 0 | 0 |
| ListObjectsV2, a prefix per thread | 64 | 4.05 | 669 | 632 | 0 | 0 |

## Parity options

| option | radosgw: client.rgw.ceph.objectstore.a (config show) | radosgw: client.rgw.rgw-go (config get) |
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

| env.json | radosgw |
|---|---|
| api_name | ceph-objectstore |
| ceph_version | 20.2.4 |
| clk_tck | 100 |
| cluster_image | quay.io/ceph/ceph:v20.2.4 |
| conc | 1 8 32 64 128 256 |
| cpu | AMD Ryzen 9 7950X3D 16-Core Processor |
| datasets | {"large-get":2147483648,"large-put":2147483648,"list":0,"noop":0,"small-get":419430400,"small-put":3145728000} |
| date | 2026-10-15T19:52:17Z |
| elbencho | 3.1-12 |
| elbencho_commit | 0637a922cbb71d56625f553750dbfbf12e5f0167 |
| elbencho_image | docker.io/library/alpine:3.24.2@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6 |
| elbencho_packages | boost-dev-1.84.0-r6 brotli-static-1.2.0-r1 c-ares-dev-1.34.8-r0 curl-static-8.22.0-r0 g++-15.2.0-r5 gcc-15.2.0-r5 libidn2-static-2.3.8-r0 libpsl-static-0.21.5-r3 musl-1.2.6-r2 nghttp2-static-1.70.0-r0 nghttp3-static-1.15.0-r0 openssl-libs-static-3.5.9-r0 zlib-static-1.3.2-r1 zstd-static-1.5.7-r2 |
| endpoint | http://10.89.0.34:80 |
| frontends | beast port=80 tcp_nodelay=1 |
| gateway | radosgw |
| get_objects | 400 |
| git_describe | 49c333f |
| go_tags | ceph_preview |
| go_version | go version go1.27.1 linux/amd64 |
| godebug |  |
| gomaxprocs | unset |
| kernel | 7.2.8-100.fc43.x86_64 |
| large_objects | 1 |
| large_threads | 32 |
| list_objects | 1600 |
| list_threads | 64 |
| load | 1.53 5.24 5.79 |
| mem_total_kib | 65007268 |
| nproc | 32 |
| pool_max_avail | 8951574528 |
| put_objects | 3000 |
| radosgw_entity | client.rgw.ceph.objectstore.a |
| release | tentacle |
| repo | /home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run |
| rgw_go_flags |  |
| rooket | rooket |
| rooket_version | rooket v0.0.0-20260928053658-3d8551957e08 |
| sampled | pod:rook-ceph-rgw-ceph-objectstore-a-5f8c8b4db6-qvv9v |
| seconds | 60 |
| smoke | 0 |
| tcp_nodelay | on |

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
