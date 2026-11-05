#!/bin/bash
# final-cleanup.sh: puts tentacle back after the campaign, as alloc/final-cleanup.sh: stops every
# rgw-go, lists bench's buckets, removes the bench user with its buckets and collects the garbage
# (benchuser.sh remove), trims bench's usage log and shows it empty, archives the osd.0 crash reports
# of the incident (all of them are osd.0's, from 17:23Z to 17:42Z), then records the end state and
# compares it with the start's.
set -uo pipefail
. /tmp/claude-1000/spike-memory/realworld/env.sh
s=$here/setup
echo "== stop rgw-go"; "$here/gw-stop.sh"
echo "== rgw-go processes: $(pgrep -c -x rgw-go)"
echo "== leftover buckets of bench"; tb radosgw-admin bucket list --uid=bench "${site[@]}"
echo "== remove bench"; "$spike/benchuser.sh" tentacle remove
echo "== usage trim --uid=bench"; tb radosgw-admin usage trim --uid=bench "${site[@]}"; echo "exit $?"
echo "== usage show --uid=bench --start-date=2026-11-01"; tb radosgw-admin usage show --uid=bench --start-date=2026-11-01 "${site[@]}"
echo "== gc process"; tb radosgw-admin gc process --include-all "${site[@]}"; echo "exit $?"
echo "== crash ls before archiving"; tb ceph --connect-timeout=20 crash ls
echo "== crash archive-all"; tb ceph --connect-timeout=20 crash archive-all; echo "exit $?"
"$here/state.sh" >"$s/state-end.txt" 2>&1
echo "== start vs end (users, buckets, gc, usage, config dump; df, pod names and dates left out)"
filter() { sed -e '/^== date/,+1d' -e '/^== ceph df/,/^== rgw pods/{/^== rgw pods/!d}' -e '/rook-ceph-rgw-ceph-objectstore-a-/d' "$1"; }
diff <(filter "$s/state-start.txt") <(filter "$s/state-end.txt") && echo "identical but for df and pod names"
echo "== ceph df at the end"; sed -n '/^== ceph df/,/^== rgw pods/p' "$s/state-end.txt"
echo "== rgw pod now"; grep rook-ceph-rgw "$s/state-end.txt"
echo "== health"; tb ceph --connect-timeout=20 health detail | head -20
