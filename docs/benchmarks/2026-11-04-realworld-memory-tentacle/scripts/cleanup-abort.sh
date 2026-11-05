#!/bin/bash
# cleanup-abort.sh BUCKET...: after an aborted run, removes the buckets' objects and the buckets
# through the rgw-go still running (or, with none, through radosgw), stops rgw-go, collects the
# garbage and shows the OSD's use.
set -uo pipefail
. /tmp/claude-1000/spike-memory/realworld/env.sh
endpoint=$rgwgo_endpoint
pgrep -x rgw-go >/dev/null || endpoint=$(radosgw_endpoint)
for b in "$@"; do
	"$s3load" -endpoint "$endpoint" -op purge -bucket "$b"
	"$s3load" -endpoint "$endpoint" -op rmbucket -bucket "$b"
done
"$here/gw-stop.sh"
tb radosgw-admin gc process --include-all "${site[@]}"
echo "gc entries: $(tb radosgw-admin gc list --include-all "${site[@]}" | grep -c '"tag"')"
echo "osd used: $(tb ceph --connect-timeout=10 tell osd.0 perf dump | jq -r '(.bluestore.allocated + .bluefs.db_used_bytes)')"
