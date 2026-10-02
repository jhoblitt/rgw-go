// Command profilegen writes the CPU profiles the report's specs read: a loop
// that crosses into C and pins memory, in three proportions.
//
//	light    mostly Go: cpu-sync-read4k-256.pprof and cpu-callback-read4k-256.pprof
//	heavy    mostly pinning and C: cpu-pipe-read4k-256.pprof
//	c-heavy  nearly all C under runtime.cgocall: pprof/c-heavy.pprof
//
// Rerun from this directory, for example
// go run . light ../cpu-sync-read4k-256.pprof; a new profile's sample counts
// differ, so the specs' expected numbers change with it.
package main

/*
#include <unistd.h>
static long spin(long n) { long s = 0; for (long i = 0; i < n; i++) { s += i ^ (s >> 3); } return s; }
*/
import "C"

import (
	"fmt"
	"os"
	"runtime"
	"runtime/pprof"
	"time"
)

// variant is how much of each loop pass pins, runs C and runs Go.
type variant struct {
	pins, spin, golang int
	getpid             bool
}

var variants = map[string]variant{
	"light":   {pins: 20, spin: 2000, golang: 400000, getpid: true},
	"heavy":   {pins: 2000, spin: 20000, golang: 20000, getpid: true},
	"c-heavy": {pins: 0, spin: 2000000, golang: 20000},
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: profilegen light|heavy|c-heavy OUT")
		os.Exit(2)
	}
	v, ok := variants[os.Args[1]]
	if !ok {
		fmt.Fprintln(os.Stderr, "profilegen: unknown variant", os.Args[1])
		os.Exit(2)
	}
	if err := run(v, os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, "profilegen:", err)
		os.Exit(1)
	}
}

func run(v variant, out string) error {
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	if err := pprof.StartCPUProfile(f); err != nil {
		return err
	}
	var p runtime.Pinner
	buf := make([]byte, 64)
	deadline := time.Now().Add(1500 * time.Millisecond)
	var sink int64
	for time.Now().Before(deadline) {
		for range v.pins {
			p.Pin(&buf[0])
			p.Unpin()
		}
		if v.pins > 0 {
			p.Pin(&buf[0])
		}
		sink += int64(C.spin(C.long(v.spin)))
		if v.getpid {
			_ = C.getpid()
		}
		p.Unpin()
		for i := range v.golang {
			sink += int64(i) ^ (sink >> 3)
		}
	}
	pprof.StopCPUProfile()
	fmt.Println(sink)
	return f.Close()
}
