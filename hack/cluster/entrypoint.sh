#!/usr/bin/env bash
# Runs one daemon of the disposable cluster inside quay.io/ceph/ceph. up.sh mounts
# it at /rgw-go/entrypoint.sh and passes the role as $1.
#
# The mon listens on 3300 and the daemons bind 7100-7199, away from the rados-rs
# cluster's 6789 and 6000-7000, so both can run on the same host network.
set -euo pipefail

readonly MON_ID=a
readonly MON_ADDR=v2:127.0.0.1:3300
readonly MGR_ID=x
readonly RGW_NAME=client.rgw.a

wait_for() {
	local desc=$1 attempts=$2
	shift 2
	for ((i = 0; i < attempts; i++)); do
		if "$@" >/dev/null 2>&1; then
			return 0
		fi
		echo "waiting for ${desc}..."
		sleep 2
	done
	echo "${desc} did not become ready" >&2
	exit 1
}

mon() {
	if [[ ! -f /etc/ceph/ceph.conf ]]; then
		local fsid
		fsid=$(uuidgen)
		cat >/etc/ceph/ceph.conf.new <<EOF
[global]
fsid = ${fsid}
mon_initial_members = ${MON_ID}
mon_host = ${MON_ADDR}
public_network = 127.0.0.1/32
ms_bind_msgr1 = false
ms_bind_msgr2 = true
ms_bind_port_min = 7100
ms_bind_port_max = 7199
auth_cluster_required = cephx
auth_service_required = cephx
auth_client_required = cephx
osd_pool_default_size = 1
osd_pool_default_min_size = 1
osd_crush_chooseleaf_type = 0
mon_allow_pool_size_one = true
mon_max_pg_per_osd = 1000
mon_warn_on_pool_no_redundancy = false

[mon]
mon_allow_pool_delete = true
# The mon store lives on the host's disk; a nearly full workstation must not stop
# a throwaway mon (the default crit threshold of 5% shuts it down).
mon_data_avail_warn = 2
mon_data_avail_crit = 1
auth_allow_insecure_global_id_reclaim = false

[mgr]
mgr_disabled_modules = rook

[osd]
osd_objectstore = memstore
osd_check_max_object_name_len_on_startup = false
EOF
		ceph-authtool --create-keyring /etc/ceph/ceph.mon.keyring --gen-key -n mon. --cap mon 'allow *'
		ceph-authtool --create-keyring /etc/ceph/ceph.client.admin.keyring --gen-key -n client.admin \
			--cap mon 'allow *' --cap osd 'allow *' --cap mgr 'allow *'
		ceph-authtool /etc/ceph/ceph.mon.keyring --import-keyring /etc/ceph/ceph.client.admin.keyring
		monmaptool --create --clobber --addv "${MON_ID}" "[${MON_ADDR}]" --fsid "${fsid}" /etc/ceph/monmap
		mkdir -p "/var/lib/ceph/mon/ceph-${MON_ID}"
		ceph-mon --conf /etc/ceph/ceph.conf.new --mkfs -i "${MON_ID}" \
			--monmap /etc/ceph/monmap --keyring /etc/ceph/ceph.mon.keyring
		# Host processes read the config and admin keyring once up.sh copies them out.
		chmod 644 /etc/ceph/ceph.conf.new /etc/ceph/ceph.client.admin.keyring
		# Published last: the other daemons poll for a ceph.conf that is complete.
		mv /etc/ceph/ceph.conf.new /etc/ceph/ceph.conf
	fi
	exec ceph-mon -f -i "${MON_ID}"
}

mgr() {
	wait_for monitor 60 ceph --connect-timeout 5 -s
	local dir=/var/lib/ceph/mgr/ceph-${MGR_ID}
	mkdir -p "${dir}"
	ceph auth get-or-create "mgr.${MGR_ID}" mon 'allow profile mgr' osd 'allow *' mds 'allow *' \
		-o "${dir}/keyring"
	exec ceph-mgr -f -i "${MGR_ID}"
}

mgr_available() {
	ceph --connect-timeout 5 mgr stat | jq -e '.available'
}

osd() {
	wait_for mgr 90 mgr_available
	local uuid id dir key
	uuid=$(uuidgen)
	id=$(ceph osd new "${uuid}")
	dir=/var/lib/ceph/osd/ceph-${id}
	mkdir -p "${dir}"
	ceph auth get-or-create "osd.${id}" mon 'allow profile osd' mgr 'allow profile osd' osd 'allow *' \
		-o "${dir}/keyring"
	key=$(ceph-authtool --print-key --name "osd.${id}" "${dir}/keyring")
	ceph-osd -i "${id}" --mkfs --key "${key}" --osd-uuid "${uuid}"
	exec ceph-osd -f -i "${id}" --public-addr 127.0.0.1 --keyring "${dir}/keyring"
}

osd_up() {
	ceph --connect-timeout 5 osd stat -f json | jq -e '.num_up_osds >= 1'
}

rgw() {
	wait_for osd 90 osd_up
	local dir=/var/lib/ceph/radosgw/ceph-rgw.a
	mkdir -p "${dir}"
	ceph auth get-or-create "${RGW_NAME}" mon 'allow rw' osd 'allow rwx' mgr 'allow rw' \
		-o "${dir}/keyring"
	exec radosgw -f --cluster ceph --name "${RGW_NAME}" --keyring "${dir}/keyring" \
		--rgw-frontends="beast endpoint=127.0.0.1:7480"
}

case "${1:-}" in
mon | mgr | osd | rgw) "$1" ;;
*)
	echo "usage: $0 mon|mgr|osd|rgw" >&2
	exit 2
	;;
esac
