#!/usr/bin/env bash
set -euo pipefail
GOBIN=$PWD/out go install -tags=ceph_preview ./...
out/rgw-go serve -- --id rgw.foo
