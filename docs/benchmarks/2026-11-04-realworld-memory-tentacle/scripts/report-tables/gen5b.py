#!/usr/bin/env python3
"""gen5b.py KEY: section 5b's tables from out/heapstats-main.tsv and out/cpu.tsv.  KEY: heap | cpu"""
import csv
import sys

out = '/tmp/claude-1000/spike-memory/realworld/out/'
key = sys.argv[1]


def cells(rows, **kw):
    sel = [r for r in rows if all(r[k] == v for k, v in kw.items())]
    sel.sort(key=lambda r: int(r['rep']))
    return sel


if key == 'heap':
    rows = list(csv.DictReader(open(out + 'heapstats-main.tsv'), delimiter='\t'))
    pts = [('large', 'after-get64', 'after the 32 x 64 MiB GETs'), ('large', 'idle-30', 'large, idle +30 s'),
           ('large', 'idle-120', 'large, idle +120 s'), ('large', 'idle-600', 'large, idle +600 s'),
           ('vlarge', 'after-15s', '256 MiB loads, +15 s'),
           ('small', 'after-get4k', 'after the 64 x 4 KiB GETs'), ('small', 'after-15s', 'small, +15 s'),
           ('mix', 'after-mix', 'after the mix'), ('mix', 'idle-30', 'mix, idle +30 s'), ('mix', 'idle-120', 'mix, idle +120 s'),
           ('mix', 'idle-240', 'mix, idle +240 s'), ('mix', 'idle-600', 'mix, idle +600 s')]
    print('| snapshot | in use by the application, RM / RMH | page heap free, RM | page heap free, RMH | released to the OS, RM | released to the OS, RMH | actual memory used, RM | actual memory used, RMH |')
    print('|---|---|---|---|---|---|---|---|')
    for load, snap, label in pts:
        def col(var, c):
            xs = cells(rows, load=load, variant=var, snapshot=snap)
            return ' / '.join(f"{float(r[c]):.0f}" for r in xs) if xs else '-'
        print(f"| {label} | {col('RM', 'in_use_MiB')} ; {col('RMH', 'in_use_MiB')} | {col('RM', 'pageheap_free_MiB')} | {col('RMH', 'pageheap_free_MiB')} | "
              f"{col('RM', 'released_MiB')} | {col('RMH', 'released_MiB')} | {col('RM', 'actual_MiB')} | {col('RMH', 'actual_MiB')} |")
elif key == 'cpu':
    rows = list(csv.DictReader(open(out + 'cpu.tsv'), delimiter='\t'))
    phases = [('large', 'put64', '32 x 64 MiB PUTs'), ('large', 'get64', '32 x 64 MiB GETs'), ('vlarge', 'get256x2', '256 MiB GETs at 2'),
              ('small', 'put4k', '64 x 4 KiB PUTs'), ('small', 'get4k', '64 x 4 KiB GETs'), ('mix', 'mix', 'the mix'),
              ('large', 'idle', 'idle after the large load (600 s)'), ('mix', 'idle', 'idle after the mix (600 s)')]
    V = ['R', 'M', 'F', 'RM', 'RMH']
    print('| phase | ' + ' | '.join(V) + ' | RMH/RM | F/RMH |')
    print('|---|' + '---|' * (len(V) + 2))
    for load, ph, label in phases:
        vals = {}
        for v in V:
            xs = cells(rows, load=load, variant=v, phase=ph)
            vals[v] = [float(r['cpu_s']) for r in xs]
        def m(v):
            return sum(vals[v]) / len(vals[v]) if vals[v] else None
        def r(a, b):
            x, y = m(a), m(b)
            return f"{x / y:.2f}" if x is not None and y not in (None, 0) else '-'
        print(f"| {label} | " + ' | '.join((' / '.join(f"{x:.1f}" for x in vals[v]) if vals[v] else '-') for v in V) + f" | {r('RMH', 'RM')} | {r('F', 'RMH')} |")
