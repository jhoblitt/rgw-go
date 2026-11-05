package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// The classes smapsClasses sums Rss into, after the first spike's
// smapsclass.sh.
const (
	classGoHeap  = "Go heap arenas"
	classBrk     = "[heap] (tcmalloc's sbrk heap)"
	classStacks  = "thread stacks"
	classAnon    = "other anon (tcmalloc mmap, runtime)"
	classFile    = "file-backed"
	classProcStk = "[stack] and vdso"
)

var smapsClassOrder = []string{classGoHeap, classBrk, classAnon, classStacks, classFile, classProcStk}

// smapsClasses sums an smaps dump's Rss, in kB, by kind of mapping: Go's
// heap arenas (anonymous, between 1 TiB and 0x700000000000, where the
// randomized arena hints land and nothing else of either gateway maps),
// the sbrk heap (tcmalloc 2.9.1 takes its system memory from sbrk first),
// thread stacks (an 8 MiB rw mapping just after a 4 KiB guard), other
// anonymous memory, file-backed mappings, and the main stack and vdso.
func smapsClasses(r io.Reader) (map[string]int64, error) {
	out := map[string]int64{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	var kind string
	var prevPerms string
	var prevSize uint64
	for sc.Scan() {
		line := sc.Text()
		f := strings.Fields(line)
		if len(f) >= 5 && strings.Contains(f[0], "-") && !strings.HasSuffix(f[0], ":") {
			lo, hi, ok := strings.Cut(f[0], "-")
			if !ok {
				continue
			}
			s, err1 := strconv.ParseUint(lo, 16, 64)
			e, err2 := strconv.ParseUint(hi, 16, 64)
			if err1 != nil || err2 != nil {
				continue
			}
			perms := f[1]
			path := ""
			if len(f) >= 6 {
				path = f[5]
			}
			size := e - s
			switch {
			case path != "" && !strings.HasPrefix(path, "["):
				kind = classFile
			case path == "[heap]":
				kind = classBrk
			case path != "":
				kind = classProcStk
			case s >= 1<<40 && e <= 0x700000000000:
				kind = classGoHeap
			case strings.HasPrefix(perms, "rw") && size == 8<<20 && strings.HasPrefix(prevPerms, "---") && prevSize == 4096:
				kind = classStacks
			default:
				kind = classAnon
			}
			prevPerms, prevSize = perms, size
			continue
		}
		if len(f) >= 2 && f[0] == "Rss:" {
			v, err := strconv.ParseInt(f[1], 10, 64)
			if err == nil {
				out[kind] += v
			}
		}
	}
	return out, sc.Err()
}

func smapsFile(path string) (map[string]int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return smapsClasses(f)
}

// writeBreakdown writes, for every run of a load, the snapshots' smaps
// classes in MiB.
func writeBreakdown(w io.Writer, runs []*Run) {
	for _, load := range []string{"large", "vlarge", "small", "mix"} {
		vs := byVariant(runs, load)
		if len(vs) == 0 {
			continue
		}
		fmt.Fprintf(w, "## %s: smaps by mapping, MiB of Rss\n\n", load)
		fmt.Fprintf(w, "| run | snapshot | %s | total |\n|---|---|%s---|\n", strings.Join(smapsClassOrder, " | "),
			strings.Repeat("---|", len(smapsClassOrder)))
		for _, v := range variantOrder {
			for _, r := range vs[v] {
				snaps, _ := filepath.Glob(filepath.Join(r.Dir, "snaps", "*.smaps"))
				for _, sn := range orderSnaps(snaps) {
					c, err := smapsFile(sn)
					if err != nil || len(c) == 0 {
						continue
					}
					var cs []string
					var total int64
					for _, k := range smapsClassOrder {
						cs = append(cs, fmt.Sprintf("%.0f", float64(c[k])/1024))
						total += c[k]
					}
					fmt.Fprintf(w, "| %s-%d | %s | %s | %.0f |\n", v, r.Rep,
						strings.TrimSuffix(filepath.Base(sn), ".smaps"), strings.Join(cs, " | "), float64(total)/1024)
				}
			}
		}
		fmt.Fprintln(w)
	}
}

// orderSnaps puts a run's snapshots in the order they were taken.
func orderSnaps(paths []string) []string {
	rank := func(p string) int {
		b := strings.TrimSuffix(filepath.Base(p), ".smaps")
		order := []string{
			"start", "after-put64", "after-put4k", "after-put256x1", "mid-get256x1", "after-get256x1",
			"after-put256x2", "mid-get256x2", "after-get256x2", "mid-get64", "after-get64", "mid-get4k", "after-get4k",
			"mid-mix", "after-mix", "idle-30", "idle-120", "idle-240", "idle-330", "idle-600", "after-15s", "end",
		}
		for i, o := range order {
			if b == o {
				return i
			}
		}
		return len(order)
	}
	out := append([]string(nil), paths...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && rank(out[j]) < rank(out[j-1]); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
