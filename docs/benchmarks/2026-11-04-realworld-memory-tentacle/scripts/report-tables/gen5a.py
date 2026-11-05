#!/usr/bin/env python3
"""Builds section 5a's tables from out/summary-followup.tsv (reps side by side)."""
import csv
import statistics as st
import sys

rows = list(csv.DictReader(open("/tmp/claude-1000/spike-memory/realworld/out/summary-followup.tsv"), delimiter="\t"))
V = ["R", "F", "F1G", "FG50", "FNC", "FMM", "FMMNC"]


def get(v, load, phase):
    rs = sorted((r for r in rows if r["variant"] == v and r["load"] == load and r["phase"] == phase), key=lambda r: int(r["rep"]))
    assert len(rs) == 2, (v, load, phase, len(rs))
    return rs


def f0(x):
    return "-" if x in ("", None) else f"{float(x):.0f}"


def f1(x):
    return "-" if x in ("", None) else f"{float(x):.1f}"


def pair(rs, col, fmt=f0):
    return " / ".join(fmt(r[col]) for r in rs)


def mean(rs, col):
    xs = [float(r[col]) for r in rs if r[col] not in ("", None)]
    return st.mean(xs) if xs else None


def ratio(v, base, load, phase, col):
    a, b = mean(get(v, load, phase), col), mean(get(base, load, phase), col)
    if a is None or b in (None, 0):
        return "-"
    return f"{a / b:.2f}"


def table(load, phase, unit_col, unit_name, unit_fmt):
    print(f"| variant | VmRSS peak | HeapInuse peak | live median, MB | goal max, MB | GC cycles | GC CPU s | phase CPU s | {unit_name} | VmRSS / F | {unit_name} / F |")
    print("|---|---|---|---|---|---|---|---|---|---|---|")
    for v in V:
        rs = get(v, load, phase)
        if v == "R":
            cells = [pair(rs, "peak VmRSS"), "-", "-", "-", "-", "-", "-", pair(rs, unit_col, unit_fmt)]
        else:
            cells = [pair(rs, "peak VmRSS"), pair(rs, "peak HeapInuse"), pair(rs, "live_median_MB"), pair(rs, "goal_max_MB"),
                     pair(rs, "gc_cycles"), pair(rs, "gc_cpu_s", f1), pair(rs, "phase_cpu_s", f1), pair(rs, unit_col, unit_fmt)]
        cells += [ratio(v, "F", load, phase, "peak VmRSS"), ratio(v, "F", load, phase, unit_col)]
        print(f"| {v} | " + " | ".join(cells) + " |")


which = sys.argv[1]
if which == "large-get":
    table("large", "get64", "MiB_per_s", "MiB/s", f0)
elif which == "small-get":
    table("small", "get4k", "req_per_s", "GETs/s", f0)
elif which == "small-put":
    table("small", "put4k", "req_per_s", "PUTs/s", f0)
elif which == "large-put":
    table("large", "put64", "MiB_per_s", "MiB/s", f0)
