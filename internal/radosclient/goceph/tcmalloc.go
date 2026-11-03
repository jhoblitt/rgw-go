package goceph

// Every binary and test that links librados links gperftools's full
// tcmalloc, as radosgw does, so librados's and go-ceph's C allocations never
// go through glibc's malloc. The directive puts libtcmalloc.so.4 in the
// executable's own DT_NEEDED, and gcc's driver puts libc after every library
// the link names, so in the loader's global scope tcmalloc's malloc and free
// come before libc's and every object binds to them; checkTcmalloc pins the
// binding. --no-as-needed keeps the entry without relying on the link
// resolving a reference to it, as an --as-needed default would.

/*
#cgo LDFLAGS: -Wl,--push-state -Wl,--no-as-needed -l:libtcmalloc.so.4 -Wl,--pop-state
#define _GNU_SOURCE
#include <dlfcn.h>
#include <stdlib.h>

// gperftools's C API is looked up rather than linked, so that a binary built
// without tcmalloc still links and the check reports what it found instead.
enum {
	RGW_GO_TCMALLOC = 0,
	RGW_GO_NO_TCMALLOC,
	RGW_GO_TCMALLOC_MINIMAL,
	RGW_GO_MALLOC_FAILED,
	RGW_GO_MALLOC_NOT_TCMALLOC,
};

// rgw_go_tcmalloc_check returns RGW_GO_TCMALLOC when the malloc this process
// calls is the full tcmalloc's, and otherwise the first check that failed:
// the heap profiler, which only the full library carries, must be present,
// and MallocExtension_GetOwnership must report a block malloc returns as
// tcmalloc's own (1, MallocExtension_kOwned).
static int rgw_go_tcmalloc_check(void) {
	int (*owner)(const void *) = (int (*)(const void *))dlsym(RTLD_DEFAULT, "MallocExtension_GetOwnership");
	if (owner == NULL) return RGW_GO_NO_TCMALLOC;
	if (dlsym(RTLD_DEFAULT, "HeapProfilerStart") == NULL) return RGW_GO_TCMALLOC_MINIMAL;
	void *p = malloc(64);
	if (p == NULL) return RGW_GO_MALLOC_FAILED;
	int owned = owner(p);
	free(p);
	return owned == 1 ? RGW_GO_TCMALLOC : RGW_GO_MALLOC_NOT_TCMALLOC;
}
*/
import "C"

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// checkTcmalloc returns nil when the process allocates C memory through the
// full tcmalloc radosgw links, loaded with its libunwind from one directory,
// and otherwise says what it found instead.
func checkTcmalloc() error {
	err := mallocIsTcmalloc()
	if err != nil {
		return err
	}
	maps, err := os.ReadFile("/proc/self/maps")
	if err != nil {
		return fmt.Errorf("goceph: reading the process's mappings: %w", err)
	}
	return tcmallocDepsBeside(string(maps))
}

// mallocIsTcmalloc returns nil when the malloc the process calls is the full
// tcmalloc's.
func mallocIsTcmalloc() error {
	switch C.rgw_go_tcmalloc_check() {
	case C.RGW_GO_TCMALLOC:
		return nil
	case C.RGW_GO_NO_TCMALLOC:
		return errors.New("goceph: no tcmalloc in the process: MallocExtension_GetOwnership is not defined")
	case C.RGW_GO_TCMALLOC_MINIMAL:
		return errors.New("goceph: the process has the minimal tcmalloc, not the full libtcmalloc.so.4 radosgw links")
	case C.RGW_GO_MALLOC_FAILED:
		return errors.New("goceph: malloc failed")
	default:
		return errors.New("goceph: tcmalloc does not own a block malloc returned: malloc is another allocator's")
	}
}

// tcmallocDepsBeside returns nil when maps, the text of /proc/self/maps,
// maps libtcmalloc.so.4 and, if any, libunwind.so.8 from one directory. A
// private copy of tcmalloc carries no RUNPATH, so only the executable's
// DT_RPATH finds its libunwind beside it: linked with a DT_RUNPATH, the
// process maps the copy and the host's libunwind, whatever its malloc
// binds to. A tcmalloc built without libunwind maps none.
func tcmallocDepsBeside(maps string) error {
	var tcmalloc, unwind string
	for line := range strings.Lines(maps) {
		i := strings.IndexByte(line, '/')
		if i < 0 {
			continue
		}
		path := strings.TrimSpace(line[i:])
		switch base := filepath.Base(path); {
		case strings.HasPrefix(base, "libtcmalloc.so.4"):
			tcmalloc = filepath.Dir(path)
		case strings.HasPrefix(base, "libunwind.so.8"):
			unwind = filepath.Dir(path)
		}
	}
	switch {
	case tcmalloc == "":
		return errors.New("goceph: the process maps no libtcmalloc.so.4")
	case unwind != "" && unwind != tcmalloc:
		return fmt.Errorf("goceph: libtcmalloc.so.4 is mapped from %s but its libunwind.so.8 from %s: "+
			"the executable's rpath must be a DT_RPATH (-Wl,--disable-new-dtags) to load tcmalloc's dependencies beside it",
			tcmalloc, unwind)
	}
	return nil
}
