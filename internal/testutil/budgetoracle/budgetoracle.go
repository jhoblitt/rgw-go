// Package budgetoracle asks librados what its objecter throttle charges a
// built go-ceph op, for the specs that check goceph's in-flight limiter
// against librados itself rather than against a restatement of its rules.
//
// No public librados API reports an op's budget. libceph-common.so.2 exports
// Objecter::calc_op_budget, though, which reads only the op's steps and never
// its Objecter
// (https://github.com/ceph/ceph/blob/v20.2.4/src/osdc/Objecter.cc#L3502-L3518,
// the same code at v19.2.7), so the package finds it with dlsym and calls it
// with no Objecter and no cluster. Both C op handles point at the
// ::ObjectOperation whose first member is that vector of steps: a read op's
// is one
// (https://github.com/ceph/ceph/blob/v20.2.4/src/librados/librados_c.cc#L4087-L4093),
// and a write op's is a librados::ObjectOperationImpl that starts with one
// (https://github.com/ceph/ceph/blob/v20.2.4/src/librados/librados_c.cc#L3601-L3607,
// https://github.com/ceph/ceph/blob/v20.2.4/src/librados/ObjectOperationImpl.h#L21-L25),
// and ObjectOperation starts with ops
// (https://github.com/ceph/ceph/blob/v20.2.4/src/osdc/Objecter.h#L90-L91).
// The code is the same at v19.2.7. All of this is librados's internals, so
// the package is for specs alone, and Budget reads the vector's header
// through that layout before it lets calc_op_budget walk the vector.
//
// A libceph-common that does not export calc_op_budget fails the specs that
// need it. SkipEnv set to "skip" (RGW_GO_TEST_BUDGET_ORACLE=skip) lets them
// skip instead, for a librados built without the symbol.
package budgetoracle

/*
#cgo LDFLAGS: -ldl
#define _GNU_SOURCE
#include <dlfcn.h>
#include <stddef.h>
#include <stdint.h>

// Objecter::calc_op_budget(const boost::container::small_vector_base<OSDOp>&)
// is a member function, so its this comes first. It never reads it, but it
// is given an object rather than NULL.
typedef int (*budget_fn)(void *self, const void *ops);

static char oracle_objecter;

static budget_fn oracle_fn(void) {
	static budget_fn fn;
	if (!fn)
		fn = (budget_fn)dlsym(RTLD_DEFAULT,
			"_ZN8Objecter14calc_op_budgetERKN5boost9container17small_vector_baseI5OSDOpvvEE");
	return fn;
}

static int oracle_exported(void) { return oracle_fn() != NULL; }

// A boost::container::vector holds its start, its size and its capacity, in
// that order. ops is the start of an ::ObjectOperation, so those three words
// lie inside it whatever its layout. A real start is a pointer: past the
// unmapped first page and word-aligned, which a size or a capacity read in
// its place is not.
static int oracle_layout(void *ops, size_t want) {
	uintptr_t start = ((uintptr_t *)ops)[0];
	size_t *words = (size_t *)ops;
	return start >= 4096 && start % sizeof(void *) == 0 &&
		words[1] == want && words[1] <= words[2];
}

static int oracle_budget(void *ops) { return oracle_fn()(&oracle_objecter, ops); }
*/
import "C"

import (
	"errors"
	"fmt"
	"os"
	"reflect"

	"github.com/ceph/go-ceph/rados"
)

// SkipEnv names the variable that, set to "skip", lets the specs skip when
// the libceph-common the process runs does not export calc_op_budget.
const SkipEnv = "RGW_GO_TEST_BUDGET_ORACLE"

var (
	// ErrNotExported is Budget's error when the libceph-common the process
	// runs does not export calc_op_budget.
	ErrNotExported = fmt.Errorf("budgetoracle: the libceph-common this process runs does not export "+
		"Objecter::calc_op_budget; set %s=skip to skip the specs that need it", SkipEnv)
	// ErrLayout is Budget's error when the op's step vector, read through the
	// layout the package assumes, does not hold the steps the op was built
	// with.
	ErrLayout = errors.New("budgetoracle: the op's steps are not where calc_op_budget would read them")
)

// Budget returns what librados's calc_op_budget charges op, a go-ceph op its
// builders have filled with osdOps OSD ops. It first reads the op's step
// vector through the layout it assumes, and returns ErrLayout without
// calling calc_op_budget unless that vector holds osdOps steps.
func Budget[T rados.ReadOp | rados.WriteOp](op *T, osdOps int) (int, error) {
	// go-ceph keeps the op's C handle in its unexported field op.
	f := reflect.ValueOf(op).Elem().FieldByName("op")
	if !f.IsValid() || f.Kind() != reflect.UnsafePointer {
		panic("budgetoracle: go-ceph's op no longer keeps its C handle in op")
	}
	ops := f.UnsafePointer()
	if osdOps < 0 || C.oracle_layout(ops, C.size_t(osdOps)) == 0 {
		return 0, fmt.Errorf("%w: want %d", ErrLayout, osdOps)
	}
	if C.oracle_exported() == 0 {
		return 0, ErrNotExported
	}
	return int(C.oracle_budget(ops)), nil
}

// Skipped reports whether err is ErrNotExported and SkipEnv lets the specs
// skip.
func Skipped(err error) bool {
	return errors.Is(err, ErrNotExported) && os.Getenv(SkipEnv) == "skip"
}
