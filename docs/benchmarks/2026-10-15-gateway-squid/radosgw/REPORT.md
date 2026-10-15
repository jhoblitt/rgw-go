# Gateway comparison: squid

- radosgw: docs/benchmarks/2026-10-15-gateway-squid/radosgw; frontends `beast port=80 tcp_nodelay=1`, TCP_NODELAY on

Each row is one measured phase. req/s counts S3 requests: elbencho's IOs, a listing's ListObjectsV2 calls, httpload's GETs. A multipart PUT's row, marked (parts/s), counts as elbencho does: its req/s counts UploadPart requests, leaving out each upload's Create and Complete, and its CPU per request is per part. objects/s counts objects written or read, or keys listed. The latency percentiles are per request, from elbencho's latency histogram as its --latpercent9s prints them, or httpload's. CPU per request is the gateway process's user and system time over the phase, from the samples taken once a second around it, interpolated at its start and end, so a short phase's is coarse; a 2 s gap after every phase and every preparation keeps their CPU out of the next phase's. Peak RSS is the highest of those samples, and thread high-water the most OS threads among them: Go parks an idle OS thread rather than ending it, so for rgw-go it is the most the run has needed by the phase's end, its preparations included, not the phase's own demand. A ratio column divides a run's value by radosgw's.

## Findings

No phase had an error or a retry.

## small-put

### small-put: throughput and latency

| variant | threads | req/s radosgw | objects/s radosgw | MiB/s radosgw | p50 ms radosgw | p99 ms radosgw | p999 ms radosgw |
|---|---|---|---|---|---|---|---|
| PUT 4 KiB | 1 | 109 | 109 | 0.42 | 9.09 | 21.2 | 35.3 |
| PUT 4 KiB | 8 | 473 | 473 | 1.85 | 11.8 | 28.4 | 46.6 |
| PUT 4 KiB | 32 | 922 | 922 | 3.60 | 32.3 | 68.6 | 120 |
| PUT 4 KiB | 64 | 911 | 911 | 3.56 | 64.5 | 128 | 158 |
| PUT 4 KiB | 128 | 899 | 899 | 3.52 | 126 | 244 | 352 |
| PUT 4 KiB | 256 | 903 | 903 | 3.54 | 248 | 496 | 631 |

### small-put: cost

| variant | threads | CPU ms/req radosgw | thread high-water radosgw | peak RSS MiB radosgw | errors radosgw | retries radosgw |
|---|---|---|---|---|---|---|
| PUT 4 KiB | 1 | 0.88 | 603 | 176 | 0 | 0 |
| PUT 4 KiB | 8 | 0.88 | 603 | 187 | 0 | 0 |
| PUT 4 KiB | 32 | 1.14 | 603 | 200 | 0 | 0 |
| PUT 4 KiB | 64 | 1.17 | 603 | 208 | 0 | 0 |
| PUT 4 KiB | 128 | 1.19 | 603 | 222 | 0 | 0 |
| PUT 4 KiB | 256 | 1.21 | 603 | 250 | 0 | 0 |

## small-get and noop

### small-get and noop: throughput and latency

| variant | threads | req/s radosgw | objects/s radosgw | MiB/s radosgw | p50 ms radosgw | p99 ms radosgw | p999 ms radosgw |
|---|---|---|---|---|---|---|---|
| GET 4 KiB | 1 | 1442 | 1442 | 5.63 | 0.69 | 0.91 | 1.25 |
| anonymous GET / | 1 | 4384 | — | — | 0.22 | 0.32 | 0.43 |
| GET 4 KiB | 8 | 8576 | 8576 | 33.5 | 0.92 | 1.26 | 1.86 |
| anonymous GET / | 8 | 17364 | — | — | 0.45 | 0.72 | 0.98 |
| GET 4 KiB | 32 | 13426 | 13426 | 52.4 | 2.34 | 3.65 | 4.61 |
| anonymous GET / | 32 | 19112 | — | — | 1.56 | 3.81 | 5.03 |
| GET 4 KiB | 64 | 13650 | 13650 | 53.3 | 4.67 | 7.36 | 8.70 |
| anonymous GET / | 64 | 18279 | — | — | 3.17 | 8.07 | 10.2 |
| GET 4 KiB | 128 | 13396 | 13396 | 52.3 | 9.34 | 15.0 | 42.5 |
| anonymous GET / | 128 | 18094 | — | — | 6.64 | 16.4 | 21.1 |
| GET 4 KiB | 256 | 13193 | 13193 | 51.5 | 18.7 | 44.5 | 79.9 |
| anonymous GET / | 256 | 17311 | — | — | 14.3 | 30.7 | 37.5 |

### small-get and noop: cost

| variant | threads | CPU ms/req radosgw | thread high-water radosgw | peak RSS MiB radosgw | errors radosgw | retries radosgw |
|---|---|---|---|---|---|---|
| GET 4 KiB | 1 | 0.62 | 603 | 258 | 0 | 0 |
| anonymous GET / | 1 | 0.38 | 603 | 248 | 0 | — |
| GET 4 KiB | 8 | 0.74 | 603 | 258 | 0 | 0 |
| anonymous GET / | 8 | 0.72 | 603 | 248 | 0 | — |
| GET 4 KiB | 32 | 0.89 | 603 | 259 | 0 | 0 |
| anonymous GET / | 32 | 0.93 | 603 | 249 | 0 | — |
| GET 4 KiB | 64 | 0.93 | 603 | 259 | 0 | 0 |
| anonymous GET / | 64 | 0.98 | 603 | 250 | 0 | — |
| GET 4 KiB | 128 | 0.93 | 603 | 261 | 0 | 0 |
| anonymous GET / | 128 | 0.93 | 603 | 251 | 0 | — |
| GET 4 KiB | 256 | 0.92 | 603 | 264 | 0 | 0 |
| anonymous GET / | 256 | 1.05 | 603 | 253 | 0 | — |

## large-put

### large-put: throughput and latency

| variant | threads | req/s radosgw | objects/s radosgw | MiB/s radosgw | p50 ms radosgw | p99 ms radosgw | p999 ms radosgw |
|---|---|---|---|---|---|---|---|
| PUT 64 MiB | 32 | 6.62 | 6.62 | 424 | 4784 | 4850 | 4850 |
| PUT 64 MiB in 8 MiB parts (parts/s) | 32 | 49.8 | 6.23 | 399 | 606 | 860 | 983 |

### large-put: cost

| variant | threads | CPU ms/req radosgw | thread high-water radosgw | peak RSS MiB radosgw | errors radosgw | retries radosgw |
|---|---|---|---|---|---|---|
| PUT 64 MiB | 32 | 188 | 603 | 565 | 0 | 0 |
| PUT 64 MiB in 8 MiB parts (parts/s) | 32 | 28.9 | 603 | 579 | 0 | 0 |

## large-get

### large-get: throughput and latency

| variant | threads | req/s radosgw | objects/s radosgw | MiB/s radosgw | p50 ms radosgw | p99 ms radosgw | p999 ms radosgw |
|---|---|---|---|---|---|---|---|
| GET 64 MiB in 4 MiB ranges | 32 | 105 | 6.38 | 421 | 299 | 496 | 762 |
| GET 64 MiB | 32 | 12.4 | 12.4 | 807 | 2556 | 3703 | 4325 |

### large-get: cost

| variant | threads | CPU ms/req radosgw | thread high-water radosgw | peak RSS MiB radosgw | errors radosgw | retries radosgw |
|---|---|---|---|---|---|---|
| GET 64 MiB in 4 MiB ranges | 32 | 2.49 | 603 | 583 | 0 | 0 |
| GET 64 MiB | 32 | 29.6 | 603 | 584 | 0 | 0 |

## list

### list: throughput and latency

| variant | threads | req/s radosgw | objects/s radosgw | MiB/s radosgw | p50 ms radosgw | p99 ms radosgw | p999 ms radosgw |
|---|---|---|---|---|---|---|---|
| ListObjectsV2 | 1 | 113 | 112067 | — | 8.96 | 10.6 | 11.6 |
| ListObjectsV2, a prefix per thread | 64 | 1045 | 836208 | — | 60.4 | 72.7 | 104 |

### list: cost

| variant | threads | CPU ms/req radosgw | thread high-water radosgw | peak RSS MiB radosgw | errors radosgw | retries radosgw |
|---|---|---|---|---|---|---|
| ListObjectsV2 | 1 | 4.51 | 603 | 612 | 0 | 0 |
| ListObjectsV2, a prefix per thread | 64 | 4.48 | 603 | 578 | 0 | 0 |

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
| ceph_version | 19.2.7 |
| clk_tck | 100 |
| cluster_image | quay.io/ceph/ceph:v19.2.7 |
| conc | 1 8 32 64 128 256 |
| cpu | AMD Ryzen 9 7950X3D 16-Core Processor |
| datasets | {"large-get":2147483648,"large-put":2147483648,"list":0,"noop":0,"small-get":419430400,"small-put":3145728000} |
| date | 2026-10-15T09:57:46Z |
| elbencho | 3.1-12 |
| elbencho_commit | 0637a922cbb71d56625f553750dbfbf12e5f0167 |
| elbencho_image | docker.io/library/alpine:3.24.2@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6 |
| elbencho_packages | boost-dev-1.84.0-r6 brotli-static-1.2.0-r1 c-ares-dev-1.34.8-r0 curl-static-8.22.0-r0 g++-15.2.0-r5 gcc-15.2.0-r5 libidn2-static-2.3.8-r0 libpsl-static-0.21.5-r3 musl-1.2.6-r2 nghttp2-static-1.70.0-r0 nghttp3-static-1.15.0-r0 openssl-libs-static-3.5.9-r0 zlib-static-1.3.2-r1 zstd-static-1.5.7-r2 |
| endpoint | http://10.89.0.40:80 |
| frontends | beast port=80 tcp_nodelay=1 |
| gateway | radosgw |
| get_objects | 400 |
| git_describe | 252972a |
| go_tags | ceph_preview |
| go_version | go version go1.27.1 linux/amd64 |
| godebug |  |
| gomaxprocs | unset |
| kernel | 7.2.8-100.fc43.x86_64 |
| large_objects | 1 |
| large_threads | 32 |
| list_objects | 1600 |
| list_threads | 64 |
| load | 1.83 2.29 2.15 |
| mem_total_kib | 65007268 |
| nproc | 32 |
| pool_max_avail | 9595482112 |
| put_objects | 3000 |
| radosgw_entity | client.rgw.ceph.objectstore.a |
| release | squid |
| repo | /home/jhoblitt/github/rgw-go/.claude/worktrees/bench-run |
| rgw_go_flags |  |
| rooket | rooket |
| rooket_version | rooket v0.0.0-20260928053658-3d8551957e08 |
| sampled | pod:rook-ceph-rgw-ceph-objectstore-a-78f948894c-dd2mb |
| seconds | 60 |
| smoke | 0 |
| tcp_nodelay | on |

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
