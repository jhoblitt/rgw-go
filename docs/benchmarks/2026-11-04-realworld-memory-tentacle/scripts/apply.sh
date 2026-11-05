#!/bin/bash
# apply.sh VARIANT: copies a throwaway variant's files over the worktree's 296ad44 checkout:
# FNC stat.go; FMM buflist.go, store.go, membudget.go, metrics.go; FMMNC both sets. restore puts
# the checkout's own files back.
set -euo pipefail
fu=/tmp/claude-1000/spike-memory/realworld/fu
wt=/home/jhoblitt/github/rgw-go/.claude/worktrees/agent-ab289e9e0d1899338
mmap() {
	cp "$fu/buflist.go.mmap" "$wt/internal/driver/buflist.go"
	cp "$fu/store.go.mmap" "$wt/internal/driver/store.go"
	cp "$fu/membudget.go.mmap" "$wt/internal/driver/membudget.go"
	cp "$fu/metrics.go.mmap" "$wt/internal/metrics/metrics.go"
}
case $1 in
FNC) cp "$fu/stat.go.nocopy" "$wt/internal/driver/stat.go" ;;
FMM) mmap ;;
FMMNC)
	mmap
	cp "$fu/stat.go.nocopy" "$wt/internal/driver/stat.go"
	;;
restore)
	git -C "$wt" checkout -- internal/driver/stat.go internal/driver/buflist.go internal/driver/store.go \
		internal/driver/membudget.go internal/metrics/metrics.go internal/driver/export_test.go
	;;
*)
	echo "unknown variant $1" >&2
	exit 1
	;;
esac
git -C "$wt" diff --stat
