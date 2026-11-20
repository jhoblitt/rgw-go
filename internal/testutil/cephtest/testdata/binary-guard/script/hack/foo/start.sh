#!/usr/bin/env bash
set -euo pipefail
go build -o x ./cmd/rgw-go
./x serve -- --id rgw.foo
