#!/bin/bash
# final-cleanup-main.sh: puts the host back after RM and RMH: records rgw-go-main's end state, then removes
# the cluster (rooket down --delete-disks: kind nodes, registry, iSCSI target, disk image, state directory)
# and the images and files made for it on the host, and checks that nothing of the run is left running.
# The cluster is disposable, so its bench user and data go with it. rgw-go-tentacle is not touched here.
set -uo pipefail
here=/tmp/claude-1000/spike-memory/realworld
export ROOKET_NAME=rgw-go-main
s=$here/setup-main
echo "== state before teardown"; RW_CLUSTER=main "$here/state.sh" >"$s/state-end.txt" 2>&1; tail -5 "$s/state-end.txt"
echo "== rooket down --delete-disks rgw-go-main"
rooket down --delete-disks 2>&1 | tail -15
echo "== rooket list"; rooket list
echo "== state directory"; ls -d /home/jhoblitt/.local/share/rooket/rgw-go-main 2>&1
echo "== iSCSI target and backstore left for rgw-go-main"; sudo -n /usr/bin/targetcli ls 2>&1 | grep -c rgw-go-main
echo "== kind clusters"; kind get clusters 2>&1
echo "== containers named rgw-go-main"; podman ps -a --format '{{.Names}}' | grep -c rgw-go-main
echo "== images made for the run"
podman rmi -f localhost:5003/ceph-main:c928697 2>&1 | tail -2
podman images --digests 2>&1 | grep -E 'ceph-main|c928697' || echo "(none left)"
echo "== host processes of the run"
pgrep -a -x rgw-go || echo "no rgw-go"
pgrep -af 'rwtool|gcloop|guard.sh|podpss' | grep -v pgrep || echo "no sampler, guard or gc loop"
systemctl --user list-units --type=scope --no-legend 'rw-*' || true
