package fakerados

import (
	"fmt"
	"maps"
	"slices"
	"syscall"
)

// tableClasses names the classes fakerados emulates from method tables. Each
// has its method set, which its registration must give, so RegisterClass
// refuses them.
var tableClasses = map[string]bool{}

// classMethod is one method of an emulated class: its emulator, and whether
// the class registers it with CLS_METHOD_WR, the flag that makes the OSD
// serve an op calling it as a write.
type classMethod struct {
	fn    ClassFunc
	write bool
}

// methodTable is an emulated class's methods by name. Each area of a class
// keeps its methods in a table of its own, in the file that emulates them.
type methodTable map[string]classMethod

// mergeMethodTables is the class whose methods are those of every table. A
// name two tables hold panics, since a class registers each method once and
// only one of the two emulators could answer it.
func mergeMethodTables(tables ...methodTable) methodTable {
	all := methodTable{}
	for _, t := range tables {
		for name, m := range t {
			if _, ok := all[name]; ok {
				panic(fmt.Sprintf("fakerados: class method %q is in two method tables", name))
			}
			all[name] = m
		}
	}
	return all
}

// assembleClass is the class the tables make: their methods merged, which
// panics on a name two tables hold, and the method and write lists those
// derive.
func assembleClass(tables ...methodTable) (methods methodTable, names, writes []string) {
	methods = mergeMethodTables(tables...)
	return methods, methods.names(), methods.writeMethods()
}

// dispatch returns the ClassFunc that runs each call through the emulator of
// its method, and answers EOPNOTSUPP for a method the table lacks. The OSD
// refuses such a call at op setup, where OpInfo::set_from_op's
// get_method_flags finds no method of its name
// (https://github.com/ceph/ceph/blob/v19.2.7/src/osd/osd_op_util.cc#L189-L196 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/osd/osd_op_util.cc#L189-L196 at v20.2.4),
// and so does the fake's op setup, given the table's names through
// RegisterClassMethods, which alone registers a table's class. Answering
// when the call's step runs is do_osd_ops's own check, where get_method
// finds none
// (https://github.com/ceph/ceph/blob/v19.2.7/src/osd/PrimaryLogPG.cc#L6159-L6164 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/osd/PrimaryLogPG.cc#L6228-L6233 at v20.2.4),
// which the fake reaches only for a name a registration lists and the table
// lacks.
func (t methodTable) dispatch() ClassFunc {
	return func(call *ClassCall) ([]byte, int32) {
		m, ok := t[call.Method]
		if !ok {
			return nil, -int32(syscall.EOPNOTSUPP)
		}
		return m.fn(call)
	}
}

// names is the table's methods in name order, the method set
// RegisterClassMethods takes with the table's ClassFunc. The list has no
// spare capacity, so a caller's append copies it rather than writing into it.
func (t methodTable) names() []string { return slices.Clip(slices.Sorted(maps.Keys(t))) }

// writeMethods is the table's write methods in name order, the writes
// RegisterClassMethods takes with the table's ClassFunc and names. The list
// has no spare capacity either.
func (t methodTable) writeMethods() []string {
	var names []string
	for name, m := range t {
		if m.write {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return slices.Clip(names)
}

// noOutput is the ClassFunc of a method that replies with no output.
func noOutput(fn func(call *ClassCall) int32) ClassFunc {
	return func(call *ClassCall) ([]byte, int32) { return nil, fn(call) }
}
