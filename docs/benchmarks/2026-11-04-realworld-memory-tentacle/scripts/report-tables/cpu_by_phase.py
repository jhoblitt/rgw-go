#!/usr/bin/env python3
"""cpu_by_phase.py OUT_TSV RUNS_DIR...: the gateway cgroup's CPU over each phase of each run (cpu.csv), seconds."""
import csv
import glob
import os
import sys

out = sys.argv[1]
rows = []
for runs in sys.argv[2:]:
    for d in sorted(glob.glob(os.path.join(runs, "*", "*-*"))):
        f = os.path.join(d, "cpu.csv")
        if not os.path.exists(os.path.join(d, "done")) or not os.path.exists(f):
            continue
        load = os.path.basename(os.path.dirname(d))
        var, rep = os.path.basename(d).rsplit("-", 1)
        for r in csv.DictReader(open(f)):
            rows.append((load, var, int(rep), r["phase"], (float(r["t1"]) - float(r["t0"])),
                         int(r["usage_usec"]) / 1e6, int(r["user_usec"]) / 1e6, int(r["system_usec"]) / 1e6))
rows.sort()
with open(out, "w") as w:
    w.write("load\tvariant\trep\tphase\tseconds\tcpu_s\tuser_s\tsystem_s\n")
    for r in rows:
        w.write("\t".join([r[0], r[1], str(r[2]), r[3]] + [f"{x:.1f}" for x in r[4:]]) + "\n")
print(len(rows), "rows")
