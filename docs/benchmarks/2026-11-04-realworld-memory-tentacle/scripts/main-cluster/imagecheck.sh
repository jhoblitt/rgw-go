#!/bin/bash
# imagecheck.sh: what the pinned Ceph main image is: its digest in the cluster's registry, ceph --version,
# the OS release, the tcmalloc radosgw links, and whether radosgw knows rgw_heap_release_interval.
set -uo pipefail
img=localhost:5003/ceph-main:c928697
echo "== manifest digest in the registry (pinned: sha256:c928697602b7feffdaef0b59d95a7e94a864e4dd89d33a867e0e48bac0fe7e06)"
skopeo inspect --tls-verify=false --raw docker://$img | sha256sum
podman pull --tls-verify=false $img 2>&1 | tail -1
podman images --digests localhost:5003/ceph-main
cat >/tmp/claude-1000/rwfu-scratch/inimage.sh <<'EOS'
echo "== os-release"; grep -E '^(PRETTY_NAME|VERSION_ID)=' /etc/os-release
echo "== ceph --version"; ceph --version
echo "== radosgw --version"; radosgw --version
echo "== rpm"; rpm -q gperftools-libs ceph-radosgw librados2 libunwind 2>&1
echo "== ldd radosgw (allocator, libc)"; ldd /usr/bin/radosgw | grep -i -E 'tcmalloc|jemalloc|libc\.so|libunwind'
echo "== tcmalloc files"; ls -la /usr/lib64/libtcmalloc* 2>&1
echo "== option in radosgw's defaults"; radosgw --show-config 2>/dev/null | grep -E '^rgw_heap_release' || ceph-conf -D 2>/dev/null | grep -E '^rgw_heap_release'
EOS
podman run --rm -v /tmp/claude-1000/rwfu-scratch/inimage.sh:/inimage.sh:ro,Z --entrypoint bash $img /inimage.sh 2>&1
