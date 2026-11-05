#!/bin/bash
# rescue-pod.sh create|delete: a pod with osd.0's deployment's pod spec, its init containers (which
# activate the OSD directory) unchanged and its osd container replaced by `sleep infinity` without
# probes, so that ceph-bluestore-tool and ceph-osd can be run by hand on the OSD's device. Its label
# is app=rw-rescue, so nothing that selects app=rook-ceph-osd sees it. The pod spec is saved in
# realworld/rescue/.
set -euo pipefail
. /tmp/claude-1000/spike-memory/realworld/env.sh
d=$here/rescue
mkdir -p "$d"
case $1 in
create)
	timeout 60 rooket k -n rook-ceph get deploy rook-ceph-osd-0 -o json >"$d/deploy-osd-0.json"
	jq '{apiVersion: "v1", kind: "Pod",
		metadata: {name: "rw-rescue-osd-0", namespace: "rook-ceph", labels: {app: "rw-rescue"}},
		spec: (.spec.template.spec
			| .restartPolicy = "Never"
			| .containers = [.containers[] | select(.name == "osd")
				| .command = ["sleep", "infinity"] | .args = []
				| del(.livenessProbe, .startupProbe, .readinessProbe)])}' "$d/deploy-osd-0.json" >"$d/pod.json"
	timeout 60 rooket k -n rook-ceph apply -f "$d/pod.json"
	timeout 300 rooket k -n rook-ceph wait --for=condition=Ready pod/rw-rescue-osd-0 --timeout=280s
	;;
delete)
	timeout 120 rooket k -n rook-ceph delete pod rw-rescue-osd-0 --wait=true
	;;
esac
