#!/usr/bin/env python3
"""mkpatch-mmap.py: writes the THROWAWAY variant FMM's files from 296ad44's (the *.orig beside
this script) as *.mmap: the free list allocates every block of rgw_max_chunk_size bytes with
syscall.Mmap, off the Go heap, and munmaps a block where it drops it (over the cap, when a get sheds
older blocks, at the trim), without FreeOSMemory for those; a block it forgets (an abandoned op's,
one a panic left) is never unmapped, a leak, since its op may still write into it; blocks of other
sizes stay on the Go heap. The store tells the list the chunk size, and two counters, the bytes
mapped and unmapped, go out as rgw_go_memory_budget_free_list_{mmap,munmap}_bytes_total. Every edit
is anchored and must match exactly once."""
import sys

d = '/tmp/claude-1000/spike-memory/realworld/fu/'


def edit(name, pairs):
    s = open(d + name + '.orig').read()
    for old, new in pairs:
        if s.count(old) != 1:
            sys.exit(f"{name}: anchor found {s.count(old)} times: {old[:70]!r}")
        s = s.replace(old, new)
    open(d + name + '.mmap', 'w').write(s)


edit('buflist.go', [
    ('import (\n\t"runtime/debug"\n\t"slices"\n\t"sync"\n\t"time"\n\t"weak"\n)\n',
     'import (\n\t"runtime/debug"\n\t"slices"\n\t"sync"\n\t"sync/atomic"\n\t"syscall"\n\t"time"\n\t"unsafe"\n\t"weak"\n)\n'),
    ('\tarmed, used, closed bool\n\tstop                func() bool\n}\n',
     '\tarmed, used, closed bool\n\tstop                func() bool\n\n'
     '\t// THROWAWAY (realworld spike, variant FMM): blocks of mmapSize bytes are mmap\'d, off the\n'
     '\t// Go heap; mapped holds them by their first byte, and the counters the bytes mapped and\n'
     '\t// unmapped.\n'
     '\tmmapSize               uint64\n'
     '\tmapped                 map[unsafe.Pointer]struct{}\n'
     '\tmmapBytes, munmapBytes atomic.Int64\n'
     '}\n\n'
     '// alloc is a new block of n bytes: mmap\'d when n is mmapSize, from the Go heap otherwise or when\n'
     '// the mmap fails. THROWAWAY (realworld spike, variant FMM).\n'
     'func (l *bufLists) alloc(n uint64) *[]byte {\n'
     '\tif n == l.mmapSize && n > 0 {\n'
     '\t\tb, err := syscall.Mmap(-1, 0, int(n), syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_PRIVATE|syscall.MAP_ANONYMOUS) //nolint:gosec // n is a chunk size\n'
     '\t\tif err == nil {\n'
     '\t\t\tl.mu.Lock()\n'
     '\t\t\tif l.mapped == nil {\n'
     '\t\t\t\tl.mapped = make(map[unsafe.Pointer]struct{})\n'
     '\t\t\t}\n'
     '\t\t\tl.mapped[unsafe.Pointer(unsafe.SliceData(b))] = struct{}{}\n'
     '\t\t\tl.mu.Unlock()\n'
     '\t\t\tl.mmapBytes.Add(int64(n)) //nolint:gosec // n is a chunk size\n'
     '\t\t\treturn &b\n'
     '\t\t}\n'
     '\t}\n'
     '\treturn new(make([]byte, n))\n'
     '}\n\n'
     '// dropLocked lets p go: munmap\'d if the list mapped it, left to the GC otherwise, which it\n'
     '// reports. THROWAWAY (realworld spike, variant FMM).\n'
     'func (l *bufLists) dropLocked(p *[]byte) (heap bool) {\n'
     '\tb := (*p)[:cap(*p)]\n'
     '\tkey := unsafe.Pointer(unsafe.SliceData(b))\n'
     '\tif _, ok := l.mapped[key]; !ok {\n'
     '\t\treturn true\n'
     '\t}\n'
     '\tdelete(l.mapped, key)\n'
     '\tif err := syscall.Munmap(b); err == nil {\n'
     '\t\tl.munmapBytes.Add(int64(len(b)))\n'
     '\t}\n'
     '\treturn false\n'
     '}\n'),
    ('\tl.shedLocked(n)\n\tl.mu.Unlock()\n\treturn new(make([]byte, n))\n}\n',
     '\tl.shedLocked(n)\n\tl.mu.Unlock()\n\treturn l.alloc(n)\n}\n'),
    ('\t\toldest.blocks = slices.Delete(oldest.blocks, 0, 1)\n',
     '\t\tl.dropLocked(oldest.blocks[0].p)\n\t\toldest.blocks = slices.Delete(oldest.blocks, 0, 1)\n'),
    ('\tif w > l.limit-l.idle-max(l.lent, 0) {\n\t\treturn\n\t}\n',
     '\tif w > l.limit-l.idle-max(l.lent, 0) {\n\t\tl.dropLocked(p)\n\t\treturn\n\t}\n'),
    ('\tl.mu.Lock()\n\tdefer l.mu.Unlock()\n\tl.lent -= toInt64(uint64(cap(*p)))\n}\n',
     '\tl.mu.Lock()\n\tdefer l.mu.Unlock()\n\tl.lent -= toInt64(uint64(cap(*p)))\n'
     '\t// THROWAWAY (realworld spike, variant FMM): a forgotten block may still be written by its\n'
     '\t// abandoned op, so it is never unmapped: it leaks.\n'
     '\tdelete(l.mapped, unsafe.Pointer(unsafe.SliceData((*p)[:cap(*p)])))\n}\n'),
    ('\t\tif k := bl.lowWater - bufTrimFloor; k > 0 {\n'
     '\t\t\tbl.blocks = slices.Delete(bl.blocks, 0, k)\n'
     '\t\t\tl.idle -= int64(k) * toInt64(n)\n'
     '\t\t\tdropped = true\n'
     '\t\t}\n',
     '\t\tif k := bl.lowWater - bufTrimFloor; k > 0 {\n'
     '\t\t\t// THROWAWAY (realworld spike, variant FMM): only heap blocks call for FreeOSMemory.\n'
     '\t\t\tfor _, b := range bl.blocks[:k] {\n'
     '\t\t\t\tif l.dropLocked(b.p) {\n'
     '\t\t\t\t\tdropped = true\n'
     '\t\t\t\t}\n'
     '\t\t\t}\n'
     '\t\t\tbl.blocks = slices.Delete(bl.blocks, 0, k)\n'
     '\t\t\tl.idle -= int64(k) * toInt64(n)\n'
     '\t\t}\n'),
])

edit('store.go', [
    ('\ts.heads = unfollowedHeads{s}\n',
     '\ts.heads = unfollowedHeads{s}\n'
     '\t// THROWAWAY (realworld spike, variant FMM): the free list maps its chunk-sized blocks.\n'
     '\ts.bufs.mmapSize = readCfg.chunk\n'),
])

edit('membudget.go', [
    ('\tFreeListIdle int64\n}\n',
     '\tFreeListIdle int64\n'
     '\t// FreeListMmap and FreeListMunmap are the bytes the free list has mmap\'d and munmap\'d.\n'
     '\t// THROWAWAY (realworld spike, variant FMM).\n'
     '\tFreeListMmap, FreeListMunmap int64\n'
     '}\n'),
    ('\tst.FreeListIdle = s.bufs.idleBytes()\n',
     '\tst.FreeListIdle = s.bufs.idleBytes()\n'
     '\tst.FreeListMmap, st.FreeListMunmap = s.bufs.mmapBytes.Load(), s.bufs.munmapBytes.Load()\n'),
])

edit('metrics.go', [
    ('\tbudgetFreeList = prometheus.NewDesc("rgw_go_memory_budget_free_list_bytes",',
     '\t// THROWAWAY (realworld spike, variant FMM).\n'
     '\tbudgetFreeListMmap = prometheus.NewDesc("rgw_go_memory_budget_free_list_mmap_bytes_total",\n'
     '\t\t"Bytes the free list has mmap\'d for blocks off the Go heap.", nil, nil)\n'
     '\tbudgetFreeListMunmap = prometheus.NewDesc("rgw_go_memory_budget_free_list_munmap_bytes_total",\n'
     '\t\t"Bytes the free list has munmap\'d.", nil, nil)\n'
     '\tbudgetFreeList = prometheus.NewDesc("rgw_go_memory_budget_free_list_bytes",'),
    ('budgetSlowdowns, budgetPrefetchSkipped, budgetFreeList} {',
     'budgetSlowdowns, budgetPrefetchSkipped, budgetFreeList, budgetFreeListMmap, budgetFreeListMunmap} {'),
    ('\tch <- prometheus.MustNewConstMetric(budgetFreeList, prometheus.GaugeValue, float64(st.FreeListIdle))\n',
     '\tch <- prometheus.MustNewConstMetric(budgetFreeList, prometheus.GaugeValue, float64(st.FreeListIdle))\n'
     '\tch <- prometheus.MustNewConstMetric(budgetFreeListMmap, prometheus.CounterValue, float64(st.FreeListMmap))\n'
     '\tch <- prometheus.MustNewConstMetric(budgetFreeListMunmap, prometheus.CounterValue, float64(st.FreeListMunmap))\n'),
])
