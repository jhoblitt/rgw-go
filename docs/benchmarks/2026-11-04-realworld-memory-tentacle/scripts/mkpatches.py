#!/usr/bin/env python3
"""mkpatches.py: writes the three throwaway patches against 296ad44 (#559), each headed by a
comment that labels it throwaway: nocopy.patch (FNC), mmap.patch (FMM), mmap-nocopy.patch (FMMNC),
from the *.orig files and their variants beside this script."""
import difflib

d = '/tmp/claude-1000/spike-memory/realworld/fu/'
files = {
    'stat': ('internal/driver/stat.go', 'stat.go.orig', 'stat.go.nocopy'),
    'buflist': ('internal/driver/buflist.go', 'buflist.go.orig', 'buflist.go.mmap'),
    'store': ('internal/driver/store.go', 'store.go.orig', 'store.go.mmap'),
    'membudget': ('internal/driver/membudget.go', 'membudget.go.orig', 'membudget.go.mmap'),
    'metrics': ('internal/metrics/metrics.go', 'metrics.go.orig', 'metrics.go.mmap'),
}


def diff(key):
    path, a, b = files[key]
    return ''.join(difflib.unified_diff(open(d + a).readlines(), open(d + b).readlines(),
                                        'a/' + path, 'b/' + path))


head = 'THROWAWAY patch from the real-world memory spike of 2026-11-04, variant {}: never merge.\n' \
       'It applies to 296ad44 (#559) with `patch -p1` or `git apply`.\n{}\n\n'
nocopy = ('FNC', 'A GET\'s prefetched head is served from its free-list buffer instead of a copy; the\n'
          'buffer goes back to the list once, when the request\'s context ends or the state is discarded.')
mmap = ('FMM', 'The free list mmaps every rgw_max_chunk_size block off the Go heap and munmaps it where it\n'
        'drops it (over the cap, when shedding, at the trim), with no FreeOSMemory for those; a\n'
        'forgotten block leaks; blocks of other sizes stay on the Go heap. It exports\n'
        'rgw_go_memory_budget_free_list_{mmap,munmap}_bytes_total.')
both = ('FMMNC', 'mmap.patch (FMM) and nocopy.patch (FNC) together.')
mm = ''.join(diff(k) for k in ('buflist', 'store', 'membudget', 'metrics'))
open(d + 'nocopy.patch', 'w').write(head.format(*nocopy) + diff('stat'))
open(d + 'mmap.patch', 'w').write(head.format(*mmap) + mm)
open(d + 'mmap-nocopy.patch', 'w').write(head.format(*both) + mm + diff('stat'))
