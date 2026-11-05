#!/bin/bash
# benchuser.sh RELEASE create|remove: the bench harness's fixed-key user "bench"
# (hack/bench/gateway/internal/gateway/run.go:27-29, site.go:184-185). remove purges its buckets
# and collects the garbage, as hack/admin/remove-user.sh does.
set -euo pipefail
export ROOKET_NAME=rgw-go-$1
site=(--rgw-realm=ceph-objectstore --rgw-zonegroup=ceph-objectstore --rgw-zone=ceph-objectstore)
admin() { timeout 600 rooket k -n rook-ceph exec deploy/rook-ceph-tools -- radosgw-admin "$@" "${site[@]}"; }
case $2 in
create)
	if admin user info --uid bench >/dev/null 2>&1; then
		echo "bench exists"
		exit 0
	fi
	admin user create --uid bench --display-name bench --access-key BENCH0000000000000AK \
		--secret-key bench-secret-0123456789abcdefghijklmnopqrstuv >/dev/null
	echo "bench created"
	;;
remove)
	if admin user info --uid bench >/dev/null 2>&1; then
		admin user rm --uid bench --purge-data
	fi
	admin gc process --include-all
	echo "bench removed, gc processed"
	;;
esac
