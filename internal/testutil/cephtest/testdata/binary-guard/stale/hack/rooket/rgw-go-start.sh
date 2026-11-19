#!/usr/bin/env bash
set -euo pipefail
go build -tags=ceph_preview -o bin/rgw-go ./cmd/rgw-go
