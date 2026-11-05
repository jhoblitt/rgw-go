#!/bin/bash
# state.sh: tentacle's state for the put-back check, as alloc/state.sh and gtcm/state.sh, with this
# campaign's processes and scopes.
set -uo pipefail
. /tmp/claude-1000/spike-memory/realworld/env.sh
echo "== date"; date -Is
echo "== users"; tb radosgw-admin user list "${site[@]}"
echo "== buckets"; tb radosgw-admin bucket list "${site[@]}"
echo "== gc entries (include-all)"; tb radosgw-admin gc list --include-all "${site[@]}" | grep -c '"tag"'
echo "== usage show --uid=bench --start-date=2026-11-01"; tb radosgw-admin usage show --uid=bench --start-date=2026-11-01 "${site[@]}"
echo "== ceph df"; tb ceph --connect-timeout=20 df
echo "== rgw pods and deployment"
timeout 60 rooket k -n rook-ceph get pods -l app=rook-ceph-rgw -o wide
timeout 60 rooket k -n rook-ceph get deploy rook-ceph-rgw-ceph-objectstore-a -o jsonpath='{.metadata.annotations.deployment\.kubernetes\.io/revision} {.spec.template.spec.containers[0].image}{"\n"}'
echo "== config dump"; tb ceph --connect-timeout=20 config dump
echo "== rgw-go processes and scopes on the host"
pgrep -a -x rgw-go || echo "no rgw-go"
systemctl --user list-units --type=scope --no-legend 'rw-*' 'gtcm-*' 'alloc-*' 'spike-*' 'pools-*' || true
