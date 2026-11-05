#!/bin/bash
# smapsclass.sh SMAPS: sums Rss and Pss (kB) of an smaps dump by kind of mapping: each file by
# path, [heap] (glibc's main arena), [stack], Go's heap arena range (0xc000000000-0xc0ffffffffff),
# glibc non-main arena heaps (an rw mapping at a 64 MiB boundary whose rw and following ---p parts
# make 64 MiB), thread stacks (an 8 MiB rw mapping just after a 4 KiB ---p guard), and other anon.
gawk '
function flush() {
	if (start == "") return
	k = kind
	rss[k] += r; pss[k] += p; cnt[k]++
}
/^[0-9a-f]+-[0-9a-f]+ / {
	flush()
	split($1, a, "-"); s = strtonum("0x" a[1]); e = strtonum("0x" a[2]); sz = e - s
	perms = $2; path = (NF >= 6) ? $6 : ""
	start = a[1]; r = p = 0
	if (path != "" && path !~ /^\[/) kind = "file " path
	else if (path == "[heap]") kind = "[heap] glibc main arena"
	else if (path ~ /^\[stack/) kind = "[stack]"
	else if (path != "") kind = path
	else if (s >= strtonum("0x10000000000") && e <= strtonum("0x700000000000")) kind = "anon Go heap arenas (randomized base)"
	else if (s < strtonum("0x10000000000")) kind = "anon low (binary bss)"
	else if (perms ~ /^rw/ && s % 67108864 == 0 && sz < 67108864) { kind = "anon glibc arena"; pend = s + 67108864 }
	else if (perms ~ /^---/ && e == pend) kind = "anon glibc arena reserve"
	else if (perms ~ /^rw/ && sz == 8388608 && prevperm ~ /^---/ && prevsz == 4096) kind = "anon thread stack (8 MiB)"
	else kind = "anon other"
	prevperm = perms; prevsz = sz
	next
}
$1 == "Rss:" { r = $2 }
$1 == "Pss:" { p = $2 }
END {
	flush()
	for (k in rss) { tr += rss[k]; tp += pss[k] }
	for (k in rss) printf "%10d %10d %5d  %s\n", rss[k], pss[k], cnt[k], k | "sort -k1,1nr"
	close("sort -k1,1nr")
	printf "%10d %10d        total (kB Rss, kB Pss)\n", tr, tp
}' "$1"
