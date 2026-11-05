#!/bin/bash
# osdwait.sh SECONDS: polls osd.0's pod every 5 s for up to SECONDS and prints its phase, the osd
# container's state and restart count; stops early when the container has restarted, or has run
# for 60 s with the OSD up in the osdmap.
set -uo pipefail
. /tmp/claude-1000/spike-memory/realworld/env.sh
end=$((SECONDS + $1))
while ((SECONDS < end)); do
	s=$(timeout 30 rooket k -n rook-ceph get pods -l app=rook-ceph-osd -o jsonpath='{.items[0].metadata.name} {.items[0].status.phase} {.items[0].status.containerStatuses[?(@.name=="osd")].restartCount} {.items[0].status.containerStatuses[?(@.name=="osd")].state}')
	echo "$(date +%T) $s"
	case $s in *'"running"'*)
		up=$(timeout 30 rooket k -n rook-ceph exec deploy/rook-ceph-tools -- ceph --connect-timeout=10 osd dump --format json 2>/dev/null | jq -r '.osds[0].up')
		echo "  osdmap up=$up"
		started=$(sed -nE 's/.*"startedAt":"([^"]+)".*/\1/p' <<<"$s")
		if [ "$up" = 1 ] && [ $(($(date +%s) - $(date -d "$started" +%s))) -gt 60 ]; then exit 0; fi
		;;
	esac
	case $s in *' '[1-9]*' '*) [ "${s#* * }" != "$s" ] ;; esac
	r=$(awk '{print $3}' <<<"$s")
	[ -n "$r" ] && [ "$r" -gt 0 ] && { echo "osd container restarted"; exit 1; }
	sleep 5
done
exit 2
