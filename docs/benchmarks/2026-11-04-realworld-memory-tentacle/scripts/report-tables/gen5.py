#!/usr/bin/env python3
"""gen5.py: section 5's table (rep 1 smaps by mapping) from out/breakdown.md, with RM / RMH rows."""
import re

path = '/tmp/claude-1000/spike-memory/realworld/out/breakdown.md'
data = {}  # (load, run, snapshot) -> cells
load = None
for line in open(path):
    line = line.rstrip('\n')
    if line.startswith('## '):
        load = line[3:].split(':')[0]
    elif line.startswith('| ') and not line.startswith('| run') and not line.startswith('|---'):
        c = [x.strip() for x in line.strip().strip('|').split('|')]
        data[(load, c[0], c[1])] = c[2:]
pts = [('fresh, idle 15 s', 'large', 'start'), ('after 32 x 64 MiB PUT', 'large', 'after-put64'),
       ('during 32 x 64 MiB GETs', 'large', 'mid-get64'), ('600 s after them', 'large', 'idle-600'),
       ('during 2 x 256 MiB GETs', 'vlarge', 'mid-get256x2'), ('during 64 x 4 KiB GETs', 'small', 'mid-get4k')]
# columns of a breakdown row: Go heap, [heap], other anon, stacks, files, [stack], total
use = [0, 1, 2, 3, 4, 6]


def row(load, runs, snap):
    cols = [data[(load, r, snap)] for r in runs]
    return ' | '.join(' / '.join(c[i] for c in cols) for i in use)


print('| snapshot | gateway | Go heap | tcmalloc heap | other anon | thread stacks | files | total |')
print('|---|---|---|---|---|---|---|---|')
for label, load, snap in pts:
    print(f'| {label} | R | {row(load, ["R-1"], snap)} |')
    print(f'| | M / F | {row(load, ["M-1", "F-1"], snap)} |')
    print(f'| | RM / RMH | {row(load, ["RM-1", "RMH-1"], snap)} |')
