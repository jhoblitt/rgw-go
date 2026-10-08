#!/usr/bin/env bash
# Fails when a release's chart values start the radosgw under other options
# than lib.sh's parity_options, which rgw-go-up.sh sets for both gateways: the
# rgwConfig of the values' object store must hold exactly those options, with
# those values.
#
# Usage: parity-options-check.sh
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)
readonly here
readonly script=parity-options-check.sh
# shellcheck source=hack/rooket/lib.sh
source "${here}/lib.sh"
(($# == 0)) || die "usage: ${script}"

failed=0

fail() {
	echo "${script}: $*" >&2
	failed=1
}

# rgw_config prints, as sorted option=value lines, the rgwConfig map at
# cephObjectStores[name=ceph-objectstore].spec.gateway in the values file $1,
# the one place the chart passes it to the CephObjectStore's gateway. It
# tracks the key at each indentation, a list item's keys sitting past its
# "- ". Every entry must be a plain key and a double-quoted value, as the
# CRD's map of strings takes it. An rgwConfig anywhere else, a second one,
# none, or anything else in the block prints a line starting with "!", so the
# comparison fails rather than reading the file wrong.
rgw_config() {
	awk '
		function bad(why) { print "!" FILENAME ":" FNR ": " why }
		/^ *(#.*)?$/ { next }
		{
			match($0, /^ */)
			indent = RLENGTH
			line = substr($0, indent + 1)
			item = 0
			if (substr(line, 1, 2) == "- ") {
				item = 1
				line = substr(line, 3)
				match(line, /^ */)
				indent += 2 + RLENGTH
				line = substr(line, RLENGTH + 1)
			}
			while (depth > 0 && indents[depth] >= indent) depth--
			if (depth > 0 && keys[depth] == "rgwConfig" && block_depth == depth) {
				if (!item && line ~ /^[a-z0-9_]+: "[^"]*"$/) {
					key = line; sub(/:.*/, "", key)
					value = line; sub(/^[^"]*"/, "", value); sub(/"$/, "", value)
					print key "=" value
				} else {
					bad("not an option: \"value\" line: " $0)
					key = ""
				}
				depth++
				indents[depth] = indent
				keys[depth] = key
				next
			}
			if (block_depth && depth > block_depth) { bad("nested under an rgwConfig option: " $0); next }
			if (line !~ /^[A-Za-z0-9_.-]+:( |$)/) next
			key = line; sub(/:.*/, "", key)
			rest = substr(line, length(key) + 2); sub(/^ */, "", rest); sub(/ +#.*$/, "", rest)
			if (depth == 1 && keys[1] == "cephObjectStores" && indents[1] == 0) {
				if (item) store = ""
				if (key == "name") { store = rest; sub(/^"/, "", store); sub(/"$/, "", store) }
			}
			block_depth = 0
			if (key == "rgwConfig") {
				if (!(depth == 3 && keys[1] == "cephObjectStores" && indents[1] == 0 &&
					keys[2] == "spec" && keys[3] == "gateway" && store == "ceph-objectstore"))
					bad("an rgwConfig outside cephObjectStores[name=ceph-objectstore].spec.gateway")
				else if (rest != "")
					bad("an rgwConfig that is not a block map: " $0)
				else if (blocks++)
					bad("a second rgwConfig")
				else
					block_depth = depth + 1
			}
			depth++
			indents[depth] = indent
			keys[depth] = key
		}
		END { if (!blocks) print "!" FILENAME ": no rgwConfig at cephObjectStores[name=ceph-objectstore].spec.gateway" }
	' "$1" | LC_ALL=C sort
}

want=$(printf '%s\n' "${parity_options[@]}" | LC_ALL=C sort)
for r in squid tentacle; do
	use_release "${r}"
	values=${here}/${release}/values/rook-ceph-cluster.yaml
	got=$(rgw_config "${values}")
	[[ "${got}" == "${want}" ]] ||
		fail "the rgwConfig in ${values} differs from lib.sh's parity_options (- lib.sh, + values):
$(diff <(echo "${want}") <(echo "${got}") | grep '^[<>]' | sed 's/^</-/; s/^>/+/')"
done

exit "${failed}"
