#!/bin/bash
# podpss.sh OUT SECONDS: radosgw's smaps_rollup once a second for SECONDS, read from inside the pod's
# log-collector container (it shares the pod's pid namespace; its cgroup is not the rgw
# container's, so the loop is not charged to radosgw) as uid 167, appended to OUT as
# "EPOCH Rss Pss Pss_Anon Pss_File" in kB. The loop ends at SECONDS or when its output pipe closes.
set -uo pipefail
. /tmp/claude-1000/spike-memory/realworld/env.sh
o=$1 secs=$2
pod=$(rgwpod)
# shellcheck disable=SC2016
exec timeout $((secs + 60)) rooket k -n rook-ceph exec "$pod" -c log-collector -- sh -c '
for c in /proc/[0-9]*/comm; do [ "$(cat "$c" 2>/dev/null)" = radosgw ] && P=$(basename "$(dirname "$c")"); done
end=$(( $(date +%s) + '"$secs"' ))
while [ "$(date +%s)" -lt "$end" ]; do
	t=$(date +%s.%N)
	v=$(setpriv --reuid=167 --regid=167 --clear-groups cat /proc/$P/smaps_rollup | awk "\$1==\"Rss:\"{r=\$2} \$1==\"Pss:\"{p=\$2} \$1==\"Pss_Anon:\"{a=\$2} \$1==\"Pss_File:\"{f=\$2} END{print r, p, a, f}")
	echo "$t $v" || exit 0
	sleep 1
done' >>"$o"
