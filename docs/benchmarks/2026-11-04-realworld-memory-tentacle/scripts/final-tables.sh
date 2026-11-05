#!/bin/bash
# final-tables.sh: the report's generated tables with RM and RMH beside R, M and F, after the RM/RMH campaign.
# runs-all/ links the Tentacle campaign's runs (R, M, F) and runs-main/'s (RM, RMH); the follow-up's runs-fu/ gets the
# RM and RMH runs of loads 1 and 3 too. rwtool-analyze2 is built from _rwtool (the change: RM and RMH are radosgws).
set -euo pipefail
here=/tmp/claude-1000/spike-memory/realworld
cd "$here"
rm -rf runs-all
for load in large vlarge small mix; do
	mkdir -p "runs-all/$load"
	for d in runs/$load/*-* runs-main/$load/*-*; do
		[ -e "$d/done" ] && ln -s "$here/$d" "runs-all/$load/$(basename "$d")"
	done
done
for load in large small; do
	for d in runs-main/$load/*-*; do
		[ -e "$d/done" ] && [ ! -e "runs-fu/$load/$(basename "$d")" ] && ln -s "$here/$d" "runs-fu/$load/$(basename "$d")"
	done
done
t=$here/bin/rwtool-analyze2
$t analyze -runs runs-all -out out/tables.md -breakdown out/breakdown.md -gc out/gc.md \
	-summary out/summary.tsv -samples out/samples.tsv.gz -variants R,M,F,RM,RMH \
	-ratios M/R,F/R,F/M,RM/R,RMH/R,RMH/RM,F/RM,F/RMH
$t analyze -runs runs-fu -out out/tables-followup.md -breakdown out/breakdown-followup.md -followup out/followup.md \
	-gc out/gc-followup.md -summary out/summary-followup.tsv -samples out/samples-followup.tsv.gz \
	-variants R,RM,RMH,F,F1G,FG50,FNC,FMM,FMMNC -ratios F1G/F,FG50/F,FNC/F,FMM/F,FMMNC/F,FMMNC/R,RMH/RM,RMH/R,F/RMH
python3 /tmp/claude-1000/rwfu-scratch/heapstats.py runs-main out/heapstats-main.tsv
python3 /tmp/claude-1000/rwfu-scratch/cpu_by_phase.py out/cpu.tsv runs runs-main
ls -la out
