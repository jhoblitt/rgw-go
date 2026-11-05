#!/bin/bash
# rgwhost.sh RELEASE: prints "PID CGROUPDIR" of the release's radosgw as the host sees it, found by
# the fsid on its command line.
set -euo pipefail
case $1 in
squid) fsid=f0b9de3b-6995-4455-97fa-61a60f6cd552 ;;
tentacle) fsid=8bcd55ea-aeb4-4e94-9e4c-53fba0e163ab ;;
main) fsid=$(cat /tmp/claude-1000/spike-memory/realworld/setup-main/fsid) ;;
esac
for p in $(pgrep -x radosgw); do
	if tr '\0' ' ' <"/proc/$p/cmdline" | grep -q -- "--fsid=$fsid"; then
		cg=$(sed -n 's/^0:://p' "/proc/$p/cgroup")
		echo "$p /sys/fs/cgroup$cg"
	fi
done
