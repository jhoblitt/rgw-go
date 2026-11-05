#!/bin/bash
# bininfo.sh: records each variant binary's go version -m, sha256, DT_NEEDED and RPATH.
set -euo pipefail
d=/tmp/claude-1000/spike-memory/realworld
for v in M F; do
	b=$d/bin/$v/rgw-go
	echo "=== $v $b"
	sha256sum "$b"
	go version -m "$b"
	readelf -d "$b" | grep -E 'NEEDED|RPATH|RUNPATH'
done
