#!/usr/bin/env python3
"""heapstats.py RUNS_DIR OUT_TSV: tcmalloc's own view of the Ceph main radosgw (RM, RMH) at every snapshot.

Reads snaps/*.heapstats.txt of each run ("ceph daemon ... heap stats") and writes one line per snapshot:
bytes in use by the application, in the page heap freelist, in the other freelists, released to the OS,
and the actual memory used, MiB.
"""
import glob
import os
import re
import sys

runs, out = sys.argv[1], sys.argv[2]
pat = {
    "in_use": r"MALLOC:\s*\+?\s*(\d+) \(.*Bytes in use by application",
    "pageheap_free": r"MALLOC:\s*\+\s*(\d+) \(.*Bytes in page heap freelist",
    "central_free": r"MALLOC:\s*\+\s*(\d+) \(.*Bytes in central cache freelist",
    "transfer_free": r"MALLOC:\s*\+\s*(\d+) \(.*Bytes in transfer cache freelist",
    "thread_free": r"MALLOC:\s*\+\s*(\d+) \(.*Bytes in thread cache freelists",
    "released": r"MALLOC:\s*\+\s*(\d+) \(.*Bytes released to OS",
    "actual": r"MALLOC:\s*=\s*(\d+) \(.*Actual memory used",
}
order = ["start", "after-put64", "mid-get64", "after-get64", "after-put256x1", "mid-get256x1", "after-get256x1", "after-put256x2",
         "mid-get256x2", "after-get256x2", "after-put4k", "mid-get4k", "after-get4k", "mid-mix", "after-mix",
         "idle-30", "idle-120", "idle-240", "idle-330", "idle-600", "after-15s", "end"]
rows = []
for d in sorted(glob.glob(os.path.join(runs, "*", "R*-*"))):
    load = os.path.basename(os.path.dirname(d))
    var, rep = os.path.basename(d).rsplit("-", 1)
    for f in glob.glob(os.path.join(d, "snaps", "*.heapstats.txt")):
        snap = os.path.basename(f).removesuffix(".heapstats.txt")
        t = open(f).read()
        vals = {}
        for k, p in pat.items():
            m = re.search(p, t)
            vals[k] = int(m.group(1)) / 1048576 if m else None
        if vals["in_use"] is None:
            continue
        rows.append((load, var, int(rep), order.index(snap) if snap in order else 99, snap, vals))
rows.sort(key=lambda r: (r[0], r[1], r[2], r[3]))
with open(out, "w") as w:
    w.write("load\tvariant\trep\tsnapshot\tin_use_MiB\tpageheap_free_MiB\tcentral_free_MiB\ttransfer_free_MiB\tthread_free_MiB\treleased_MiB\tactual_MiB\n")
    for load, var, rep, _, snap, v in rows:
        w.write("\t".join([load, var, str(rep), snap] + [f"{v[k]:.1f}" if v[k] is not None else "" for k in
                ("in_use", "pageheap_free", "central_free", "transfer_free", "thread_free", "released", "actual")]) + "\n")
print(len(rows), "snapshots")
