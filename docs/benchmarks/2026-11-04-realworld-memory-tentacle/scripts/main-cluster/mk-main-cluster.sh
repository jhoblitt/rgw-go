#!/bin/bash
# mk-main-cluster.sh: brings up rgw-go-main, the disposable cluster of the real-world memory run's RM and RMH
# variants: the same shape as rgw-go-tentacle (Rook v1.20.7, one worker, one 10 GiB OSD, host networking, the
# chart's ceph-objectstore with the parity options), with the Ceph image of Ceph main pinned by digest.
# The image is pulled by digest, tagged, pushed to the cluster's own registry (rooket load) and named in
# main/cfg/values/rook-ceph-cluster.yaml; Rook v1.20.7 does not know Ceph main, so allowUnsupported is set.
# `rooket up --skip-deploy` ran first, from the cfg directory, with the same flags (main/up-1.log).
set -euo pipefail
export ROOKET_NAME=rgw-go-main
cfg=/tmp/claude-1000/spike-memory/realworld/main/cfg
digest=sha256:c928697602b7feffdaef0b59d95a7e94a864e4dd89d33a867e0e48bac0fe7e06
image=quay.ceph.io/ceph-ci/ceph@$digest
rooket up --rook-version v1.20.7 --workers 1 --config-dir "$cfg" --registry-port 5003 --skip-deploy
podman pull "$image"
podman tag "$image" localhost/ceph-main:c928697
rooket load localhost/ceph-main:c928697
rooket deploy
rooket wait --timeout 30m
