#!/bin/bash
# gdbheap.sh PID PREFIX: attaches gdb to PID for a moment and records the C heap from the
# allocator's own view. Always glibc's malloc_info XML (PREFIX.malloc_info.xml), which under a
# preloaded tcmalloc shows whether glibc's own heap was ever used. When libtcmalloc is mapped, also
# tcmalloc's MallocExtension_GetStats text (PREFIX.tcmalloc_stats.txt) and its numeric properties
# (PREFIX.tcmalloc_props.txt), through the C API in malloc_extension_c.h. The library has no
# debug info, so each call casts the function to its C signature.
set -euo pipefail
pid=$1 p=$2
gdbx() { timeout 90 gdb -p "$pid" -batch -nx -q -ex "set debuginfod enabled off" -ex "set language c" \
	-ex "set print elements 0" -ex "set pagination off" "$@" -ex detach; }
gdbx -ex "set \$f = (void *) fopen(\"$p.malloc_info.xml\", \"w\")" \
	-ex 'call (int) malloc_info(0, $f)' -ex 'call (int) fclose($f)' >/dev/null 2>&1 || echo "malloc_info via gdb failed" >&2
if grep -q libtcmalloc "/proc/$pid/maps"; then
	props=(generic.current_allocated_bytes generic.heap_size generic.total_physical_bytes
		tcmalloc.pageheap_free_bytes tcmalloc.pageheap_unmapped_bytes tcmalloc.pageheap_committed_bytes
		tcmalloc.current_total_thread_cache_bytes tcmalloc.max_total_thread_cache_bytes
		tcmalloc.thread_cache_free_bytes tcmalloc.central_cache_free_bytes tcmalloc.transfer_cache_free_bytes
		tcmalloc.slack_bytes tcmalloc.aggressive_memory_decommit tcmalloc.pageheap_scavenge_count
		tcmalloc.pageheap_decommit_count tcmalloc.pageheap_total_decommit_bytes
		tcmalloc.pageheap_commit_count tcmalloc.pageheap_total_commit_bytes
		tcmalloc.pageheap_reserve_count tcmalloc.pageheap_total_reserve_bytes)
	args=(-ex 'set $v = (unsigned long *) malloc(8)')
	for k in "${props[@]}"; do
		args+=(-ex "set \$ok = ((int (*)(const char *, unsigned long *)) MallocExtension_GetNumericProperty)(\"$k\", \$v)"
			-ex "printf \"$k %lu ok=%d\\n\", *\$v, \$ok")
	done
	args+=(-ex 'call (void) free($v)')
	gdbx "${args[@]}" 2>/dev/null | grep -E '^(generic|tcmalloc)\.' >"$p.tcmalloc_props.txt" || echo "tcmalloc properties via gdb failed" >&2
	gdbx -ex 'set $b = (char *) malloc(262144)' \
		-ex 'call ((void (*)(char *, int)) MallocExtension_GetStats)($b, 262144)' \
		-ex 'printf "%s", $b' -ex 'call (void) free($b)' 2>/dev/null | sed -n '/^-----/,$p' >"$p.tcmalloc_stats.txt" || echo "tcmalloc stats via gdb failed" >&2
fi
ls "$p".* >/dev/null
