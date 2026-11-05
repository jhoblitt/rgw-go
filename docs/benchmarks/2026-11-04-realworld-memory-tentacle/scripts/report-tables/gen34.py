#!/usr/bin/env python3
"""Renders sections 3 and 4's tables, with RM and RMH beside R, M and F, from out/tables.md.

usage: gen34.py TABLES_MD KEY   (KEY: 3.1 | 3.2 | 3.3 | 3.4 | 4.1 | 4.2)
"""
import re
import statistics as st
import sys

V = ["R", "M", "F", "RM", "RMH"]
RATIOS = [("M", "R"), ("F", "R"), ("F", "M"), ("RM", "R"), ("RMH", "RM"), ("RMH", "R"), ("F", "RMH")]


def parse(path):
    """sections[(load, title)] = list of (header, rows)."""
    out = {}
    load = None
    title = None
    header = None
    rows = None
    cur = []

    def flush():
        nonlocal header, rows
        if header is not None:
            out.setdefault((load, title), []).append((header, rows))
        header, rows = None, None

    for line in open(path):
        line = line.rstrip("\n")
        if line.startswith("## "):
            flush()
            load = line[3:].strip()
        elif line.startswith("### "):
            flush()
            title = re.sub(r",? ?\(?runs in rep order\)?", "", line[4:]).strip()
            title = title.split(",")[0].strip()
        elif line.startswith("|"):
            cells = [c.strip() for c in line.strip().strip("|").split("|")]
            if all(set(c) <= set("-") for c in cells):
                continue
            if header is None:
                header, rows = cells, []
            else:
                rows.append(cells)
        else:
            flush()
    flush()
    return out


def nums(cell):
    xs = []
    for p in cell.split("/"):
        p = p.strip()
        try:
            xs.append(float(p))
        except ValueError:
            return None
    return xs


def mean(cell):
    xs = nums(cell) if cell else None
    return st.mean(xs) if xs else None


def table_for(secs, load, title, idx=0):
    return secs[(load, title)][idx]


def cell(secs, load, title, variant, col, idx=0, key="variant"):
    hdr, rows = table_for(secs, load, title, idx)
    for r in rows:
        if r[hdr.index(key)] == variant:
            return r[hdr.index(col)]
    return "-"


def cell2(secs, load, title, variant, phase, col):
    """a throughput-style table with a phase column."""
    hdr, rows = table_for(secs, load, title)
    for r in rows:
        if r[hdr.index("variant")] == variant and r[hdr.index("phase")] == phase:
            return r[hdr.index(col)]
    return "-"


def cell_idle(secs, load, title, variant, s, col):
    hdr, rows = table_for(secs, load, title)
    for r in rows:
        if r[hdr.index("variant")] == variant and r[hdr.index("s")] == str(s):
            return r[hdr.index(col)]
    return "-"


def ratio_cells(vals):
    out = []
    for a, b in RATIOS:
        x, y = mean(vals.get(a)), mean(vals.get(b))
        out.append("-" if x is None or y in (None, 0) else f"{x / y:.2f}")
    return out


HEAD = "| " + " | ".join(V) + " | " + " | ".join(f"{a}/{b}" for a, b in RATIOS) + " |"
SEP = "|" + "---|" * (len(V) + len(RATIOS) + 1)


def render(first_cols, rows):
    """rows: list of (labels list, vals dict variant->cell, ratios bool)."""
    print("| " + " | ".join(first_cols) + " | " + HEAD[2:])
    print("|" + "---|" * len(first_cols) + SEP[1:])
    for labels, vals, with_ratios in rows:
        rc = ratio_cells(vals) if with_ratios else [""] * len(RATIOS)
        print("| " + " | ".join(labels) + " | " + " | ".join(vals.get(v, "-") or "-" for v in V) + " | " + " | ".join(rc) + " |")


def render_plain(first_cols, rows):
    """like render, without the ratio columns (for throughputs, which the ratios do not describe)."""
    print("| " + " | ".join(first_cols) + " | " + " | ".join(V) + " |")
    print("|" + "---|" * (len(first_cols) + len(V)))
    for labels, vals, _ in rows:
        print("| " + " | ".join(labels) + " | " + " | ".join(vals.get(v, "-") or "-" for v in V) + " |")


def peak_rows(secs, load, spec):
    rows = []
    for label_phase, label_metric, title, col in spec:
        vals = {v: cell(secs, load, title, v, col) for v in V}
        rows.append(([label_phase, label_metric], vals, label_metric != "threads"))
    return rows


def thr_rows(secs, load, title, spec, col):
    rows = []
    for label, phase in spec:
        vals = {v: cell2(secs, load, title, v, phase, col) for v in V}
        rows.append(([label, f"{col}"], vals, False))
    return rows


def main():
    secs = parse(sys.argv[1])
    key = sys.argv[2]
    if key == "3.1":
        render(["phase", "metric"], peak_rows(secs, "large", [
            ("PUT", "VmRSS peak", "put64 peak", "VmRSS"),
            ("PUT", "RssAnon peak", "put64 peak", "RssAnon"),
            ("PUT", "PSS peak", "put64 peak", "PSS"),
            ("PUT", "working set peak", "put64 peak", "working set"),
            ("GET", "VmRSS peak", "get64 peak", "VmRSS"),
            ("GET", "VmRSS steady", "get64 steady", "VmRSS"),
            ("GET", "RssAnon peak", "get64 peak", "RssAnon"),
            ("GET", "PSS peak", "get64 peak", "PSS"),
            ("GET", "working set peak", "get64 peak", "working set"),
            ("GET", "cgroup memory.current peak", "get64 peak", "cg current"),
            ("GET", "cgroup memory.peak", "get64 peak", "memory.peak"),
            ("GET", "threads", "get64 peak", "threads"),
        ]))
        print()
        render_plain(["phase", "throughput"], thr_rows(secs, "large", "large throughput", [("PUT (2 GiB)", "put64"), ("GET (32 GiB)", "get64")], "MiB/s"))
    elif key == "3.2":
        phases = [("6 PUTs at 1", "put256x1"), ("24 GETs at 1", "get256x1"), ("6 PUTs at 2", "put256x2"), ("24 GETs at 2", "get256x2")]
        render(["phase", "metric"], peak_rows(secs, "vlarge", [(lab, "VmRSS peak", f"{ph} peak", "VmRSS") for lab, ph in phases]))
        print()
        render_plain(["phase", "throughput"], thr_rows(secs, "vlarge", "vlarge throughput", phases, "MiB/s"))
    elif key == "3.3":
        render(["phase", "metric"], peak_rows(secs, "small", [
            ("PUT", "VmRSS peak", "put4k peak", "VmRSS"),
            ("GET", "VmRSS peak", "get4k peak", "VmRSS"),
            ("GET", "VmRSS steady", "get4k steady", "VmRSS"),
            ("GET", "PSS peak", "get4k peak", "PSS"),
            ("GET", "working set peak", "get4k peak", "working set"),
            ("GET", "cgroup memory.current peak", "get4k peak", "cg current"),
        ]))
        print()
        render_plain(["phase", "throughput"], thr_rows(secs, "small", "small throughput", [("PUT", "put4k"), ("GET", "get4k")], "requests/s"))
    elif key == "3.4":
        render(["phase", "metric"], peak_rows(secs, "mix", [
            ("mix", "VmRSS peak", "mix peak", "VmRSS"),
            ("mix", "VmRSS steady", "mix steady", "VmRSS"),
            ("mix", "RssAnon peak", "mix peak", "RssAnon"),
            ("mix", "PSS peak", "mix peak", "PSS"),
            ("mix", "working set peak", "mix peak", "working set"),
            ("mix", "cgroup memory.current peak", "mix peak", "cg current"),
            ("mix", "cgroup memory.peak", "mix peak", "memory.peak"),
            ("mix", "threads", "mix peak", "threads"),
        ]))
        print()
        hdr, rows = table_for(secs, "mix", "mix throughput")
        print("| run | seconds | MiB/s | requests/s | paused, s | errors |")
        print("|---|---|---|---|---|---|")
        for v in V:
            for r in rows:
                if r[hdr.index("variant")] == v:
                    print(f"| {v} | {r[hdr.index('seconds')]} | {r[hdr.index('MiB/s')]} | {r[hdr.index('requests/s')]} | {r[hdr.index('paused s')]} | {r[hdr.index('errors')]} |")
    elif key in ("4.1", "4.2"):
        load = "large" if key == "4.1" else "mix"
        title = f"{load} idle curve"
        pts = [30, 120, 240, 330, 600]
        for col in ("VmRSS",) if key == "4.2" else ("VmRSS",):
            rows = []
            for s in pts:
                vals = {v: cell_idle(secs, load, title, v, s, col) for v in V}
                rows.append(([str(s)], vals, True))
            render(["s"], rows)
    else:
        raise SystemExit("unknown key")


main()
