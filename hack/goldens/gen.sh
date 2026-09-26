#!/usr/bin/env bash
# Regenerates golden files from the ceph-object-corpus with ceph-dencoder run inside the
# Ceph image, so `go test` never needs the binary. Usage: hack/goldens/gen.sh [types.txt]
#
# quay.io/ceph/ceph:v19.2.6 ships /usr/bin/ceph-dencoder, so no derived image is needed.
# Each line of types.txt is `<C++ type> <package dir>`; the goldens land in
# <package dir>/testdata/goldens/<type>/<archive>/<object>.{bin,json,reenc}, where .json
# is dump_json of the decoded object and .reenc is its re-encoding at the image's version.
# One container per type and archive runs ceph-dencoder over every object in it. SELinux
# labelling is disabled for it rather than relabelling the mounts, because the corpus
# belongs to another repository.
set -euo pipefail
cd "$(dirname "$0")/../.."
CORPUS=${CORPUS:-/home/jhoblitt/github/ceph/ceph-object-corpus/archive}
IMAGE=${IMAGE:-quay.io/ceph/ceph:v19.2.6}
TYPES=${1:-hack/goldens/types.txt}
while read -r typ pkgdir; do
  case $typ in '' | '#'*) continue ;; esac
  case $pkgdir in
    '' | /* | .. | ../* | */.. | */../*)
      echo "$TYPES: $typ: package dir '$pkgdir' must be a relative path inside the repo" >&2
      exit 1
      ;;
  esac
  if [ ! -d "$pkgdir" ]; then
    echo "$TYPES: $typ: package dir '$pkgdir' does not exist" >&2
    exit 1
  fi
  out="$PWD/$pkgdir/testdata/goldens/$typ"
  rm -rf "$out"
  for archive in "$CORPUS"/*/; do
    a=$(basename "$archive")
    src="${archive}objects/$typ"
    [ -d "$src" ] || continue
    dst="$out/$a"
    mkdir -p "$dst"
    for f in "$src"/*; do cp "$f" "$dst/$(basename "$f").bin"; done
    # shellcheck disable=SC2016 # expanded by the container's shell
    podman run --rm --security-opt label=disable -v "$src:/in:ro" -v "$dst:/out" "$IMAGE" sh -euc '
      for f in /in/*; do
        n=${f##*/}
        ceph-dencoder type "$1" import "$f" decode dump_json > "/out/$n.json"
        ceph-dencoder type "$1" import "$f" decode encode export "/out/$n.reenc"
      done' sh "$typ" < /dev/null
  done
done < "$TYPES"
