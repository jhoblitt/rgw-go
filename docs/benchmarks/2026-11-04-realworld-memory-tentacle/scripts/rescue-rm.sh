#!/bin/bash
# rescue-rm.sh [MAX]: frees space on the full OSD by removing, with rados --force-full, up to MAX
# (default all) tail objects (__shadow_) of the mix smoke's bucket rw-mix from the data pool, in one
# toolbox exec. Only the smoke's bucket is touched; its index and gc entries are cleaned after.
set -euo pipefail
. /tmp/claude-1000/spike-memory/realworld/env.sh
max=${1:-1000000}
# shellcheck disable=SC2016
tb sh -c '
set -e
m=$(radosgw-admin bucket stats --bucket rw-mix --rgw-realm=ceph-objectstore --rgw-zonegroup=ceph-objectstore --rgw-zone=ceph-objectstore | sed -nE "s/^ *\"marker\": \"([^\"]+)\",/\1/p" | head -1)
[ -n "$m" ] || { echo "no marker for rw-mix"; exit 1; }
echo "rw-mix marker $m"
p=ceph-objectstore.rgw.buckets.data
rados -p $p ls > /tmp/rescue-ls.txt
echo "objects in $p: $(wc -l < /tmp/rescue-ls.txt)"
grep -F "${m}__shadow_" /tmp/rescue-ls.txt | head -n '"$max"' > /tmp/rescue-rm.txt || true
echo "removing $(wc -l < /tmp/rescue-rm.txt) shadow objects of rw-mix"
xargs -a /tmp/rescue-rm.txt -n 64 rados -p $p --force-full rm
echo "removed"
rm -f /tmp/rescue-ls.txt /tmp/rescue-rm.txt
ceph df | grep -E "TOTAL|buckets.data"'
