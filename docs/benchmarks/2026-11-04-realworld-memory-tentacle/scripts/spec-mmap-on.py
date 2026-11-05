#!/usr/bin/env python3
"""spec-mmap-on.py: for the FMM/FMMNC spec runs only, makes the specs' Stores (NewStoreForTest,
SetMemoryBudgetForTest in internal/driver/export_test.go) map their chunk-sized blocks as Open's do,
so that the buffer specs exercise the mmap'd blocks. THROWAWAY; apply.sh restore undoes it."""
import sys

p = '/home/jhoblitt/github/rgw-go/.claude/worktrees/agent-ab289e9e0d1899338/internal/driver/export_test.go'
s = open(p).read()
pairs = [
    ('\ts.heads = unfollowedHeads{s}\n\ts.bgAIO = semaphore.NewWeighted(1)\n\treturn s\n}\n',
     '\ts.heads = unfollowedHeads{s}\n\ts.bgAIO = semaphore.NewWeighted(1)\n\ts.bufs.mmapSize = s.readCfg.chunk\n\treturn s\n}\n'),
    ('\ts.mem = newMemBudget(n, wait)\n\ts.bufs = newBufLists(n)\n}\n',
     '\ts.mem = newMemBudget(n, wait)\n\ts.bufs = newBufLists(n)\n\ts.bufs.mmapSize = s.readCfg.chunk\n}\n'),
]
for old, new in pairs:
    if s.count(old) != 1:
        sys.exit(f"anchor found {s.count(old)} times: {old[:60]!r}")
    s = s.replace(old, new)
open(p, 'w').write(s)
