package goceph

/*
#cgo LDFLAGS: -ldl
#define _GNU_SOURCE
#include <dlfcn.h>

// rgw_go_ceph_version calls libceph-common's ceph_version_to_str, which
// librados loads with it. It is a C++ function, so it is looked up by its
// mangled name; NULL means the symbol is not there.
static const char *rgw_go_ceph_version(void) {
	const char *(*fn)(void) = (const char *(*)(void))dlsym(RTLD_DEFAULT, "_Z19ceph_version_to_strv");
	return fn == NULL ? NULL : fn();
}
*/
import "C"

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// ErrLibradosTooOld marks a librados rgw-go does not support, or one whose
// failure to connect is explained by its age.
var ErrLibradosTooOld = errors.New("goceph: librados too old")

// libradosVersion returns the Ceph version of the librados the process
// runs, such as "19.2.6", or "" when it cannot tell. rados.Version cannot
// serve: it reports the librados API version, 3.0.0 on every release since
// Luminous. ceph_version_to_str returns the ceph_debug_version_for_testing
// environment variable instead when it is set (v19.2.6 common/version.cc
// 26-34), so the environment can spoof the version; the version only
// explains a failure, so that is harmless. Specs replace this function.
var libradosVersion = func() string {
	v := C.rgw_go_ceph_version()
	if v == nil {
		return ""
	}
	return C.GoString(v)
}

// libradosFloors are the first release of each major line whose librados
// parses AES256KRB5 cephx keys, which Squid 19.2.6 and Tentacle 20.2.4
// clusters create by default; with an older one, connecting to such a
// cluster fails with a bare EIO. Majors above the last line parse them.
var libradosFloors = map[int][3]int{
	19: {19, 2, 6},
	20: {20, 2, 4},
}

// checkLibradosLine refuses a librados line older than Squid. That is a
// support policy: rgw-go is built and tested against Squid and Tentacle
// only. A version it cannot parse is not refused.
func checkLibradosLine(v string) error {
	got, ok := parseVersion(v)
	if !ok || got[0] >= 19 {
		return nil
	}
	return fmt.Errorf("%w: librados %s is older than Squid, the oldest release rgw-go supports", ErrLibradosTooOld, v)
}

// belowKeyFloor reports whether librados v is older than the first release
// of its own major line that parses AES256KRB5 keys. The floor is the
// client's line, not the cluster's: 20.2.3 is below it whatever the cluster
// runs, and 19.2.6 is not. An unparsable version is not below it.
func belowKeyFloor(v string) bool {
	got, ok := parseVersion(v)
	if !ok {
		return false
	}
	floor, known := libradosFloors[got[0]]
	return known && slices.Compare(got[:], floor[:]) < 0
}

// explainConnect adds the likely cause to a failed connect when librados v
// is below its line's AES256KRB5 floor, and returns err unchanged otherwise.
// A client that old still connects to a cluster whose keys are AES, as an
// upgraded one's are, or through a downstream build that backports the key
// type, so the age alone is never refused.
func explainConnect(v string, err error) error {
	if err == nil || !belowKeyFloor(v) {
		return err
	}
	return fmt.Errorf("%w: librados %s cannot parse the AES256KRB5 cephx keys Squid 19.2.6 and Tentacle 20.2.4 "+
		"clusters create by default; link librados 19.2.6 or newer on the 19.x line, 20.2.4 or newer on 20.x: %w",
		ErrLibradosTooOld, v, err)
}

// parseVersion reads the leading "major.minor.patch" of a Ceph version
// string such as "19.2.6" or "20.3.0-1234-gabcdef".
func parseVersion(v string) ([3]int, bool) {
	var out [3]int
	fields := strings.SplitN(v, ".", 3)
	if len(fields) != 3 {
		return out, false
	}
	fields[2], _, _ = strings.Cut(fields[2], "-")
	for i, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}
