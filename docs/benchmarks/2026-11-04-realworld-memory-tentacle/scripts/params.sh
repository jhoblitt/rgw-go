# shellcheck shell=bash
# params.sh: the campaign's load sizes, set from the smoke runs (notes, "Calibration").
# Waves of the count-bound GET phases: each wave is one s3load invocation in which every thread
# reads each of its objects once.
large_get_waves=${large_get_waves:-16}
vl_get_waves=${vl_get_waves:-4}
small_get_waves=${small_get_waves:-40}
# The mix's requests: the sequence's first N, the same for every variant.
mix_requests=${mix_requests:-30000}
mix_seed=20261104
# Idle curve points, seconds after the last phase ends.
read -r -a idle_points <<<"${idle_points_list:-30 120 240 330 600}"
