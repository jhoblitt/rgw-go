package fakerados

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// The OSD limits the fake enforces, at their defaults, which are the same at
// v19.2.6 and v20.2.4 (src/common/options/global.yaml.in).
const (
	maxObjectSize         = 128 << 20 // osd_max_object_size
	maxAttrNameLen        = 100       // osd_max_attr_name_len
	maxOmapEntriesPerRead = 1024      // osd_max_omap_entries_per_request
)

// Object is one stored object.
type Object struct {
	Data    []byte
	Xattrs  map[string][]byte
	Omap    map[string][]byte
	OmapHdr []byte
	Version uint64
	Mtime   time.Time
}

func newObject(mtime time.Time) *Object {
	return &Object{Xattrs: map[string][]byte{}, Omap: map[string][]byte{}, Mtime: mtime}
}

// clone returns a deep copy of o, nil for nil.
func (o *Object) clone() *Object {
	if o == nil {
		return nil
	}
	c := *o
	c.Data = slices.Clone(o.Data)
	c.Xattrs = cloneMap(o.Xattrs)
	c.Omap = cloneMap(o.Omap)
	c.OmapHdr = slices.Clone(o.OmapHdr)
	return &c
}

func cloneMap(m map[string][]byte) map[string][]byte {
	c := make(map[string][]byte, len(m))
	for k, v := range m {
		c[k] = slices.Clone(v)
	}
	return c
}

// sameContent reports whether a and b hold the same data, xattrs and omap.
func sameContent(a, b *Object) bool {
	if a == nil || b == nil {
		return a == b
	}
	return bytes.Equal(a.Data, b.Data) && bytes.Equal(a.OmapHdr, b.OmapHdr) &&
		maps.EqualFunc(a.Xattrs, b.Xattrs, bytes.Equal) && maps.EqualFunc(a.Omap, b.Omap, bytes.Equal)
}

// ClassFunc emulates one object class: it runs call and returns the method's
// output and return value. A negative value fails the op with that errno. A
// write op's caller sees only the return value, since librados's C write op
// has no output buffer for a class call. It runs under the cluster's lock
// and must not call the Cluster.
type ClassFunc func(call *ClassCall) (out []byte, rval int32)

// ClassCall is one call of a class method inside an op.
type ClassCall struct {
	Method string
	In     []byte
	// Stored is the object as stored before the op, nil when it did not
	// exist, as the call's own copy. The OSD serves a class's reads
	// (cls_cxx_getxattr, cls_cxx_read, cls_cxx_map_get_val) from the store
	// rather than from the op's earlier steps, so an emulator reads here.
	// After an OmapClear earlier in the op, and no omap write since, Stored
	// holds no omap and no omap header, as the OSD then reads none. It is
	// the object as the call began; StoredOmap follows the call's own map
	// writes.
	Stored *Object
	x      *execution
	// start is the op's object as the call began, against which
	// StoredOmap tells the call's own map writes.
	start *Object
	// omap and hdr are the call's own copy of the stored omap and omap
	// header, made when StoredOmap first serves them.
	omap   map[string][]byte
	hdr    []byte
	copied bool
	// wrote is whether the call sent a write through Create, Update or
	// Remove, which the OSD counts in num_write before the write looks for
	// the object; staged is whether one of those writes went into the op's
	// transaction, which only an existing or created object takes.
	wrote, staged bool
}

// StoredOmap returns the stored omap and omap header as a class's map
// read (cls_cxx_map_get_val, cls_cxx_map_read_header) sees them at this
// point of the call: none for an object that did not exist, and none
// while an OmapClear earlier in the op keeps them out of reach. Any omap
// write since brings them back, the call's own setting of a key or of
// the header included, as each such write sets the object's FLAG_OMAP
// again. They are the call's own copy.
func (c *ClassCall) StoredOmap() (omap map[string][]byte, hdr []byte) {
	if c.x.stored == nil || (c.x.omapCleared && !omapWritten(c.start, c.x.obj)) {
		return nil, nil
	}
	if !c.copied {
		c.omap, c.hdr, c.copied = cloneMap(c.x.stored.Omap), slices.Clone(c.x.stored.OmapHdr), true
	}
	return c.omap, c.hdr
}

// Object returns the object as the op has left it so far, nil when it does
// not exist now. Its existence is what a class's cls_cxx_stat sees. It is
// for reading: an emulator writes through Create, Update or Remove, which
// count a write that leaves the object as it was.
func (c *ClassCall) Object() *Object { return c.x.obj }

// Update returns the op's object for a write that does not create it, as
// cls_cxx_map_remove_key's and cls_cxx_map_clear's, nil when it does not
// exist now. Either way the call has sent a write: CEPH_OSD_OP_OMAPRMKEYS
// and OMAPCLEAR count it in num_write before they answer ENOENT for a
// missing object, and only an existing one takes it into the op's
// transaction. Update stages the write on any existing object, but
// OMAPCLEAR takes it only from one whose FLAG_OMAP is set, so a map clear's
// emulator calls Update only for such an object, as rgwUsageLogClear does
// (https://github.com/ceph/ceph/blob/v19.2.7/src/osd/PrimaryLogPG.cc#L7936-L7983 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/osd/PrimaryLogPG.cc#L8013-L8060 at v20.2.4).
func (c *ClassCall) Update() *Object {
	c.wrote = true
	if c.x.obj != nil {
		c.staged = true
	}
	return c.x.obj
}

// Create returns the op's object, creating an empty one when it does not
// exist, as a class's first write to a missing object creates it. The call
// has sent a write the op's transaction takes, whatever it then stores.
func (c *ClassCall) Create() *Object {
	c.wrote, c.staged = true, true
	return c.x.create()
}

// Now returns the cluster's clock, which SetClock sets, as a class reads the
// OSD's with ceph_clock_now.
func (c *ClassCall) Now() time.Time { return c.x.cluster.now() }

// Remove removes the op's object and returns 0, or -ENOENT when it does not
// exist, as cls_cxx_remove does. Either way the call has sent a write:
// CEPH_OSD_OP_DELETE counts it in num_write before _delete_oid finds the
// object missing
// (https://github.com/ceph/ceph/blob/v19.2.7/src/osd/PrimaryLogPG.cc#L6952-L6959 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/osd/PrimaryLogPG.cc#L7021-L7028 at v20.2.4).
func (c *ClassCall) Remove() int32 {
	c.wrote = true
	if c.x.obj == nil {
		return -int32(syscall.ENOENT)
	}
	c.staged = true
	c.x.remove()
	return 0
}

// stepFlagsMask is every step flag the seam defines.
const stepFlagsMask = radosclient.StepFlagExcl | radosclient.StepFlagFailOK |
	radosclient.StepFlagFAdviseRandom | radosclient.StepFlagFAdviseSequential |
	radosclient.StepFlagFAdviseWillNeed | radosclient.StepFlagFAdviseDontNeed |
	radosclient.StepFlagFAdviseNoCache

// opFlagsMask is every op flag the seam defines.
const opFlagsMask = radosclient.OpFlagBalanceReads | radosclient.OpFlagLocalizeReads |
	radosclient.OpFlagIgnoreCache | radosclient.OpFlagFullTry | radosclient.OpFlagReturnVec

// validate refuses what goceph refuses before it submits an op: a flag or a
// comparison librados does not define, a flags step with no step before it,
// and a step the kind of op cannot carry.
func validate(steps []radosclient.Step, write bool, flags radosclient.OpFlags) error {
	kind := "read"
	if write {
		kind = "write"
	}
	if flags&^opFlagsMask != 0 {
		return fmt.Errorf("fakerados: op flags %#x: %w", uint32(flags), radosclient.ErrBadOp)
	}
	for i, st := range steps {
		var ok bool
		switch s := st.(type) {
		case *radosclient.AssertExistsStep, *radosclient.AssertVersionStep, *radosclient.ExecStep:
			ok = true
		case *radosclient.CmpXattrStep:
			ok = validCmp(s.Op)
		case *radosclient.ReadStep, *radosclient.StatStep, *radosclient.GetXattrsStep,
			*radosclient.OmapGetValsStep, *radosclient.OmapGetValsByKeysStep, *radosclient.OmapGetKeysStep:
			ok = !write
		case *radosclient.StepFlagsStep:
			ok = write && i > 0 && s.Flags&^stepFlagsMask == 0
		case *radosclient.OmapCmpStep:
			ok = write && validCmp(s.Op)
		case *radosclient.CreateStep, *radosclient.RemoveStep, *radosclient.WriteFullStep,
			*radosclient.WriteStep, *radosclient.AppendStep, *radosclient.ZeroStep, *radosclient.TruncateStep,
			*radosclient.SetXattrStep, *radosclient.RmXattrStep, *radosclient.OmapSetStep,
			*radosclient.OmapRmKeysStep, *radosclient.OmapClearStep, *radosclient.SetAllocHintStep:
			ok = write
		}
		if !ok {
			return fmt.Errorf("fakerados: step %d, %T, in a %s op: %w", i, st, kind, radosclient.ErrBadOp)
		}
	}
	return nil
}

func validCmp(op radosclient.CmpOp) bool { return op >= radosclient.CmpEQ && op <= radosclient.CmpLTE }

// execution is one op running against one object.
type execution struct {
	cluster *Cluster
	name    string
	// stored is the object as stored before the op, nil when it did not
	// exist; nothing changes it. obj is the op's own object, nil when it
	// does not exist now.
	stored *Object
	obj    *Object
	// writeOp is the op's kind, mayWrite whether the OSD serves it as a
	// write.
	writeOp  bool
	mayWrite bool
	modified bool
	removed  bool
	// omapCleared is whether an OmapClear has cleared the object's
	// FLAG_OMAP with no omap write setting it again since. The OSD reads
	// the stored omap for an omap comparison and a class's map reads only
	// while that flag is set, which a remove does not change, and
	// CEPH_OSD_OP_OMAPCLEAR clears it
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/osd/PrimaryLogPG.cc#L7936-L7957 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/osd/PrimaryLogPG.cc#L8013-L8034 at v20.2.4).
	// An OmapSet sets it again, and so does a class call setting a key or
	// the omap header, which cls_cxx_map_set_val, cls_cxx_map_set_vals and
	// cls_cxx_map_write_header send as CEPH_OSD_OP_OMAPSETVALS and
	// CEPH_OSD_OP_OMAPSETHEADER
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/osd/objclass.cc#L505-L556 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/osd/objclass.cc#L505-L556 at v20.2.4).
	omapCleared bool
	// maxReply is the longest output a step put in the OSD's reply.
	maxReply int
}

// run runs steps against oid, fills their results and returns the object's
// version after the op; the cluster's lock is held. writeOp is the op's
// kind. mtime stamps what the op changes; a read op carries none.
func (p *Pool) run(name, oid string, steps []radosclient.Step, writeOp bool, flags radosclient.OpFlags, mtime time.Time) (uint64, error) {
	x := &execution{cluster: p.cluster, name: name, stored: p.store.objects[oid], writeOp: writeOp}
	x.obj = x.stored.clone()
	fills, err := x.runSteps(steps)
	// The OSD bounds or drops the outputs of a successful op it serves as a
	// write that changed the object (PrimaryLogPG::execute_ctx).
	wrote := err == nil && x.mayWrite && x.modified
	returnVec := flags&radosclient.OpFlagReturnVec != 0
	if wrote && returnVec && x.maxReply > p.cluster.maxReply {
		// It fails the op before applying it.
		err = x.errno(syscall.EOVERFLOW)
	}
	if err != nil {
		failResults(steps, err)
		return 0, err
	}
	for _, fill := range fills {
		fill()
	}
	if wrote && !returnVec {
		x.dropOutputs(steps)
	}
	return p.commit(oid, x, mtime), nil
}

// runSteps runs the steps in order, stopping at the first failure no FailOK
// flag covers, and returns what fills the results of a successful op.
func (x *execution) runSteps(steps []radosclient.Step) ([]func(), error) {
	// Before running any step, the OSD loads every class an op calls and
	// takes whether the op writes from each step's mode and each method's
	// WR flag (OpInfo::set_from_op). Whatever a call's position or FailOK
	// flag, it refuses the whole op with EOPNOTSUPP when get_method_flags
	// finds no method of the call's name in its class
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/osd/osd_op_util.cc#L189-L196 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/osd/osd_op_util.cc#L189-L196 at v20.2.4).
	// An op that does not write finds no object context for a missing object
	// (PrimaryLogPG::do_op).
	for _, st := range steps {
		s, ok := st.(*radosclient.ExecStep)
		if !ok {
			x.mayWrite = x.mayWrite || writes(st)
			continue
		}
		cls, ok := x.cluster.classes[s.Class]
		if !ok || !cls.registers(s.Method) {
			return nil, x.errno(syscall.EOPNOTSUPP)
		}
		x.mayWrite = x.mayWrite || cls.writes[s.Method]
	}
	if !x.mayWrite && x.stored == nil {
		return nil, x.errno(syscall.ENOENT)
	}
	var fills []func()
	for i, st := range steps {
		if _, ok := st.(*radosclient.StepFlagsStep); ok {
			continue
		}
		flags := stepFlags(steps, i)
		fill, err := x.step(st, flags)
		if fill != nil {
			fills = append(fills, fill)
		}
		if err != nil && flags&radosclient.StepFlagFailOK == 0 {
			return nil, err
		}
	}
	return fills, nil
}

// writes reports whether st is of the OSD's write mode, CEPH_OSD_OP_MODE_WR
// in include/rados.h, which makes its op a write.
func writes(st radosclient.Step) bool {
	switch st.(type) {
	case *radosclient.CreateStep, *radosclient.RemoveStep, *radosclient.WriteFullStep,
		*radosclient.WriteStep, *radosclient.AppendStep, *radosclient.ZeroStep, *radosclient.TruncateStep,
		*radosclient.SetXattrStep, *radosclient.RmXattrStep, *radosclient.OmapSetStep,
		*radosclient.OmapRmKeysStep, *radosclient.OmapClearStep, *radosclient.SetAllocHintStep:
		return true
	}
	return false
}

// dropOutputs empties the outputs of a successful op that wrote and lacks
// ReturnVec, as the OSD's reply then carries none (ignore_out_data in
// MOSDOpReply): a read and a class call come back empty, and librados fails
// each step whose output it decodes with EIO. A failed call keeps its error.
func (x *execution) dropOutputs(steps []radosclient.Step) {
	eio := x.errno(syscall.EIO)
	for _, st := range steps {
		switch s := st.(type) {
		case *radosclient.ReadStep:
			*s.Result = radosclient.ReadResult{Data: s.Buf[:0]}
		case *radosclient.ExecStep:
			if _, err := s.Result.Bytes(); err == nil {
				s.Result.Set(nil, 0)
			}
		case *radosclient.StatStep:
			*s.Result = radosclient.StatResult{Err: eio}
		case *radosclient.GetXattrsStep:
			*s.Result = radosclient.XattrsResult{Err: eio}
		case *radosclient.OmapGetValsStep:
			*s.Result = radosclient.OmapResult{Err: eio}
		case *radosclient.OmapGetValsByKeysStep:
			*s.Result = radosclient.OmapResult{Err: eio}
		case *radosclient.OmapGetKeysStep:
			*s.Result = radosclient.OmapKeysResult{Err: eio}
		}
	}
}

// statReplyLen is the length of a STAT's output, the size and the mtime.
const statReplyLen = 16

// reply records n bytes of a step's output in the OSD's reply.
func (x *execution) reply(n int) { x.maxReply = max(x.maxReply, n) }

// mapReplyLen is the length of m as the OSD encodes a map in a reply: a
// count, then each key and value after its length.
func mapReplyLen(m map[string][]byte) int {
	n := 4
	for k, v := range m {
		n += 8 + len(k) + len(v)
	}
	return n
}

// keysReplyLen is the length of keys as the OSD encodes a set of keys.
func keysReplyLen(keys []string) int {
	n := 4
	for _, k := range keys {
		n += 4 + len(k)
	}
	return n
}

// stepFlags returns the flags steps[i] runs with: those of the last flags
// step following it, which replace its own as set_last_op_flags does, or
// its own, which only an exclusive create has.
func stepFlags(steps []radosclient.Step, i int) radosclient.StepFlags {
	var flags radosclient.StepFlags
	if c, ok := steps[i].(*radosclient.CreateStep); ok && c.Exclusive {
		flags = radosclient.StepFlagExcl
	}
	for _, st := range steps[i+1:] {
		f, ok := st.(*radosclient.StepFlagsStep)
		if !ok {
			break
		}
		flags = f.Flags
	}
	return flags
}

// failResults gives every step that carries a result the op's error, as
// goceph fills each result from the op's error when the op fails. A read
// op's class call that failed the op keeps the result exec gave it, its
// output and return value; an op runs once, so no other result is set yet.
func failResults(steps []radosclient.Step, err error) {
	rval := -errnoOf(err)
	for _, st := range steps {
		switch s := st.(type) {
		case *radosclient.ReadStep:
			s.Result.Err = err
		case *radosclient.StatStep:
			s.Result.Err = err
		case *radosclient.GetXattrsStep:
			s.Result.Err = err
		case *radosclient.OmapGetValsStep:
			s.Result.Err = err
		case *radosclient.OmapGetValsByKeysStep:
			s.Result.Err = err
		case *radosclient.OmapGetKeysStep:
			s.Result.Err = err
		case *radosclient.ExecStep:
			if _, _, rerr := s.Result.Reply(); !errors.Is(rerr, radosclient.ErrIncomplete) {
				continue
			}
			s.Result.Set(nil, rval)
		}
	}
}

// errnoOf is the positive errno err carries, EIO for an error without one.
func errnoOf(err error) int32 {
	e, ok := errors.AsType[*radosclient.Error](err)
	if !ok {
		return int32(syscall.EIO)
	}
	if e.Errno < 0 {
		return -e.Errno
	}
	return e.Errno
}

// errno is the seam's error for errno n on the op, as goceph builds it.
func (x *execution) errno(n syscall.Errno) error {
	return &radosclient.Error{Errno: int32(n), Op: x.name} //nolint:gosec // an errno is a small positive number
}

// create returns the op's object, creating an empty one when it does not
// exist.
func (x *execution) create() *Object {
	if x.obj == nil {
		x.obj = newObject(time.Time{})
		x.modified = true
	}
	return x.obj
}

func (x *execution) remove() {
	x.obj = nil
	x.modified = true
	x.removed = true
}

// step runs one step with the flags it carries and returns what fills its
// result, if it has one.
func (x *execution) step(st radosclient.Step, flags radosclient.StepFlags) (func(), error) {
	switch s := st.(type) {
	case *radosclient.AssertExistsStep:
		// librados asserts existence with a STAT, which sees the op's object.
		if x.obj == nil {
			return nil, x.errno(syscall.ENOENT)
		}
		x.reply(statReplyLen)
	case *radosclient.AssertVersionStep:
		return nil, x.assertVersion(s.Version)
	case *radosclient.CmpXattrStep:
		return nil, x.cmpXattr(s)
	case *radosclient.OmapCmpStep:
		return nil, x.omapCmp(s)
	case *radosclient.CreateStep:
		if x.obj != nil && flags&radosclient.StepFlagExcl != 0 {
			return nil, x.errno(syscall.EEXIST)
		}
		x.create()
		x.modified = true
	case *radosclient.RemoveStep:
		if x.obj == nil {
			return nil, x.errno(syscall.ENOENT)
		}
		x.remove()
	case *radosclient.WriteFullStep:
		if len(s.Data) > maxObjectSize {
			return nil, x.errno(syscall.EFBIG)
		}
		x.create().Data = slices.Clone(s.Data)
		x.modified = true
	case *radosclient.WriteStep:
		return nil, x.writeAt(s.Offset, s.Data)
	case *radosclient.AppendStep:
		var size int
		if x.obj != nil {
			size = len(x.obj.Data)
		}
		return nil, x.writeAt(uint64(size), s.Data)
	case *radosclient.ZeroStep:
		return nil, x.zero(s.Offset, s.Length)
	case *radosclient.TruncateStep:
		return nil, x.truncate(s.Offset)
	case *radosclient.SetXattrStep:
		if len(s.Name) > maxAttrNameLen {
			return nil, x.errno(syscall.ENAMETOOLONG)
		}
		x.create().Xattrs[s.Name] = slices.Clone(s.Value)
		x.modified = true
	case *radosclient.RmXattrStep:
		if x.obj == nil {
			return nil, x.errno(syscall.ENOENT)
		}
		delete(x.obj.Xattrs, s.Name)
		x.modified = true
	case *radosclient.OmapSetStep:
		o := x.create()
		for k, v := range s.Values {
			o.Omap[k] = slices.Clone(v)
		}
		x.modified = true
		x.omapCleared = false
	case *radosclient.OmapRmKeysStep:
		if x.obj == nil {
			return nil, x.errno(syscall.ENOENT)
		}
		for _, k := range s.Keys {
			delete(x.obj.Omap, k)
		}
		x.modified = true
	case *radosclient.OmapClearStep:
		if x.obj == nil {
			return nil, x.errno(syscall.ENOENT)
		}
		if len(x.obj.Omap) > 0 || len(x.obj.OmapHdr) > 0 {
			clear(x.obj.Omap)
			x.obj.OmapHdr = nil
			x.modified = true
		}
		x.omapCleared = true
	case *radosclient.SetAllocHintStep:
		x.create()
		x.modified = true
	case *radosclient.ExecStep:
		return x.exec(s)
	default:
		return x.readStep(st)
	}
	return nil, nil
}

// readStep runs one of the steps only a read op carries. Existence and a stat
// follow the op's object; the content read is the stored object's, as the
// OSD reads data, xattrs and omap from the store.
func (x *execution) readStep(st radosclient.Step) (func(), error) {
	if x.obj == nil {
		return nil, x.errno(syscall.ENOENT)
	}
	o := x.stored
	if o == nil {
		o = x.obj
	}
	switch s := st.(type) {
	case *radosclient.ReadStep:
		return x.read(o, s), nil
	case *radosclient.StatStep:
		size, mtime := uint64(len(x.obj.Data)), x.obj.Mtime
		x.reply(statReplyLen)
		return func() { s.Result.Size, s.Result.ModTime = size, mtime }, nil
	case *radosclient.GetXattrsStep:
		xattrs := cloneMap(o.Xattrs)
		x.reply(mapReplyLen(xattrs))
		return func() { s.Result.Xattrs = xattrs }, nil
	case *radosclient.OmapGetValsStep:
		vals, more := omapPage(o.Omap, s.StartAfter, s.FilterPrefix, s.Max)
		x.reply(mapReplyLen(vals) + 1) // and the truncated flag
		return func() { s.Result.Values, s.Result.More = vals, more }, nil
	case *radosclient.OmapGetValsByKeysStep:
		vals := map[string][]byte{}
		for _, k := range s.Keys {
			if v, ok := o.Omap[k]; ok {
				vals[k] = slices.Clone(v)
			}
		}
		x.reply(mapReplyLen(vals))
		return func() { s.Result.Values = vals }, nil
	case *radosclient.OmapGetKeysStep:
		keys, more := omapKeys(o.Omap, s.StartAfter, s.Max)
		x.reply(keysReplyLen(keys) + 1) // and the truncated flag
		return func() { s.Result.Keys, s.Result.More = keys, more }, nil
	}
	return nil, nil
}

// assertVersion is ASSERT_VER, against the version the object had before
// the op, 0 when it did not exist.
func (x *execution) assertVersion(v uint64) error {
	var cur uint64
	if x.stored != nil {
		cur = x.stored.Version
	}
	switch {
	case v == 0:
		return x.errno(syscall.EINVAL)
	case v < cur:
		return x.errno(syscall.ERANGE)
	case v > cur:
		return x.errno(syscall.EOVERFLOW)
	}
	return nil
}

// cmpXattr is CMPXATTR in string mode: the op's value, on the left, against
// the stored xattr, a missing one reading as empty.
func (x *execution) cmpXattr(s *radosclient.CmpXattrStep) error {
	if x.stored == nil {
		return x.errno(syscall.ENOENT)
	}
	if !compare(s.Op, s.Value, x.stored.Xattrs[s.Name]) {
		return x.errno(syscall.ECANCELED)
	}
	return nil
}

// omapCmp is OMAP_CMP: the op's object must exist, and the stored value, on
// the left, is compared with the op's, a missing key reading as empty. The
// OSD compares only EQ, LT and GT.
func (x *execution) omapCmp(s *radosclient.OmapCmpStep) error {
	if x.obj == nil {
		return x.errno(syscall.ENOENT)
	}
	switch s.Op {
	case radosclient.CmpEQ, radosclient.CmpLT, radosclient.CmpGT:
	default:
		return x.errno(syscall.EINVAL)
	}
	var stored []byte
	if x.stored != nil && !x.omapCleared {
		stored = x.stored.Omap[s.Key]
	}
	if !compare(s.Op, stored, s.Value) {
		return x.errno(syscall.ECANCELED)
	}
	return nil
}

func compare(op radosclient.CmpOp, l, r []byte) bool {
	c := bytes.Compare(l, r)
	switch op {
	case radosclient.CmpEQ:
		return c == 0
	case radosclient.CmpNE:
		return c != 0
	case radosclient.CmpGT:
		return c > 0
	case radosclient.CmpGTE:
		return c >= 0
	case radosclient.CmpLT:
		return c < 0
	case radosclient.CmpLTE:
		return c <= 0
	}
	return false
}

// tooBig is check_offset_and_length: an extent reaching past the largest
// object the OSD accepts.
func tooBig(off, length uint64) bool {
	return off >= maxObjectSize || length > maxObjectSize || off+length > maxObjectSize
}

// writeAt is WRITE: it creates the object, zero-fills a gap past its end,
// and extends it to off for an empty write past the end.
func (x *execution) writeAt(off uint64, data []byte) error {
	n := uint64(len(data))
	if tooBig(off, n) {
		return x.errno(syscall.EFBIG)
	}
	o := x.create()
	if end := off + n; end > uint64(len(o.Data)) {
		o.Data = append(o.Data, make([]byte, end-uint64(len(o.Data)))...)
	}
	copy(o.Data[off:], data)
	x.modified = true
	return nil
}

// zero is ZERO: a no-op on a missing object, and within an existing one it
// zeroes only up to the end, which it does not move.
func (x *execution) zero(off, length uint64) error {
	if tooBig(off, length) {
		return x.errno(syscall.EFBIG)
	}
	if length == 0 || x.obj == nil {
		return nil
	}
	if size := uint64(len(x.obj.Data)); off < size {
		clear(x.obj.Data[off:min(off+length, size)])
	}
	x.modified = true
	return nil
}

// truncate is TRUNCATE: a no-op on a missing object, which it does not
// create.
func (x *execution) truncate(size uint64) error {
	if x.obj == nil {
		return nil
	}
	if tooBig(size, 0) {
		return x.errno(syscall.EFBIG)
	}
	cur := uint64(len(x.obj.Data))
	switch {
	case size < cur:
		x.obj.Data = x.obj.Data[:size]
	case size > cur:
		x.obj.Data = append(x.obj.Data, make([]byte, size-cur)...)
	}
	x.modified = true
	return nil
}

// read is READ into the seam's buffer: the extent trimmed at the end of the
// object. A zero length reads to the end into an empty buffer, which
// librados fails with ERANGE when data lies past the offset; that fails the
// step, not the op.
func (x *execution) read(o *Object, s *radosclient.ReadStep) func() {
	size := uint64(len(o.Data))
	start := min(s.Offset, size)
	if s.Length == 0 && s.Offset < size {
		// The OSD's reply carries the rest of the object.
		x.reply(len(o.Data[start:]))
		err := x.errno(syscall.ERANGE)
		return func() { s.Result.Err = err }
	}
	chunk := slices.Clone(o.Data[start : start+min(s.Length, size-start)])
	x.reply(len(chunk))
	return func() {
		if s.Buf != nil {
			n := copy(s.Buf, chunk)
			s.Result.Data, s.Result.N = s.Buf[:n], n
			return
		}
		s.Result.Data, s.Result.N = chunk, len(chunk)
	}
}

// omapPage is OMAPGETVALS: the entries in key order from past startAfter, or
// from prefix when prefix sorts later, while their keys keep prefix; at most
// maxEntries, which the OSD caps at its per-request limit, with more set
// when another entry would follow.
func omapPage(omap map[string][]byte, startAfter, prefix string, maxEntries uint64) (map[string][]byte, bool) {
	maxEntries = min(maxEntries, maxOmapEntriesPerRead)
	vals := map[string][]byte{}
	for _, k := range slices.Sorted(maps.Keys(omap)) {
		if prefix > startAfter {
			if k < prefix {
				continue
			}
		} else if k <= startAfter {
			continue
		}
		if !strings.HasPrefix(k, prefix) {
			break
		}
		if uint64(len(vals)) >= maxEntries {
			return vals, true
		}
		vals[k] = slices.Clone(omap[k])
	}
	return vals, false
}

// omapKeys is OMAPGETKEYS: the keys in order past startAfter, at most
// maxKeys under the OSD's per-request cap, with more set when another would
// follow.
func omapKeys(omap map[string][]byte, startAfter string, maxKeys uint64) ([]string, bool) {
	maxKeys = min(maxKeys, maxOmapEntriesPerRead)
	var keys []string
	for _, k := range slices.Sorted(maps.Keys(omap)) {
		if k <= startAfter {
			continue
		}
		if uint64(len(keys)) >= maxKeys {
			return keys, true
		}
		keys = append(keys, k)
	}
	return keys, false
}

// classStored is the stored object a class call reads, its own copy:
// without its omap and omap header while an OmapClear keeps them out of
// the op's reach.
func (x *execution) classStored() *Object {
	o := x.stored.clone()
	if o != nil && x.omapCleared {
		o.Omap = map[string][]byte{}
		o.OmapHdr = nil
	}
	return o
}

// omapWritten reports whether now holds an omap key, or an omap header,
// that start lacks: what a write of a key or of the header leaves, and a
// removal never does. It is asked only while an OmapClear keeps the stored
// omap out of reach, when the op's omap holds nothing but what such writes
// added since.
func omapWritten(start, now *Object) bool {
	if now == nil {
		return false
	}
	var omap map[string][]byte
	var hdr []byte
	if start != nil {
		omap, hdr = start.Omap, start.OmapHdr
	}
	if len(now.OmapHdr) > 0 && !bytes.Equal(now.OmapHdr, hdr) {
		return true
	}
	for k := range now.Omap {
		if _, ok := omap[k]; !ok {
			return true
		}
	}
	return false
}

// exec runs a class call. A write the call put in the op's transaction, or a
// change it made to the object, modifies the object, so the op bumps its
// version even when the write left it as it was: the OSD treats an op as a
// no-op only while its transaction is empty, and otherwise finish_ctx gives
// the object the next user_version, a CLS_METHOD_WR call having marked the
// op a user modification
// (https://github.com/ceph/ceph/blob/v19.2.7/src/osd/PrimaryLogPG.cc#L8873-L8971 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/osd/PrimaryLogPG.cc#L8979-L9075 at v20.2.4).
func (x *execution) exec(s *radosclient.ExecStep) (func(), error) {
	cls := x.cluster.classes[s.Class]
	call := &ClassCall{Method: s.Method, In: slices.Clone(s.In), Stored: x.classStored(), x: x}
	before := x.obj.clone()
	call.start = before
	out, rval := cls.fn(call)
	if x.omapCleared && omapWritten(before, x.obj) {
		x.omapCleared = false
	}
	changed := !sameContent(before, x.obj)
	if call.staged || changed {
		x.modified = true
	}
	if (call.wrote || changed) && !cls.writes[s.Method] {
		// The OSD fails a method without the WR flag that sent a write,
		// before taking its output, once num_write has grown past prev_wr
		// (https://github.com/ceph/ceph/blob/v19.2.7/src/osd/PrimaryLogPG.cc#L6170-L6185 at v19.2.7,
		// https://github.com/ceph/ceph/blob/v20.2.4/src/osd/PrimaryLogPG.cc#L6239-L6254 at v20.2.4).
		return func() { s.Result.Set(nil, -int32(syscall.EIO)) }, x.errno(syscall.EIO)
	}
	x.reply(len(out))
	if rval < 0 {
		err := &radosclient.Error{Errno: -rval, Op: x.name}
		if x.writeOp {
			return func() { s.Result.Set(nil, rval) }, err
		}
		// librados hands a read op's exec its output whatever the method
		// returned (C_out_buffer::finish), and the OSD keeps a failed op's
		// output in its reply (PrimaryLogPG::execute_ctx). A read op carries
		// no FailOK, so this call fails the op, whose fills never run: the
		// result is set here.
		s.Result.Set(slices.Clone(out), rval)
		return nil, err
	}
	if x.writeOp {
		out = nil
	}
	out = slices.Clone(out)
	return func() { s.Result.Set(out, 0) }, nil
}

// commit stores what a successful op left and returns the object's version
// after it; the cluster's lock is held. An op that changed nothing leaves the
// object and its version, as the OSD does an op with an empty transaction.
// A zero mtime, or the Unix epoch, leaves the object's own: librados sends
// both as the zero timespec, for which the OSD keeps the stored mtime.
func (p *Pool) commit(oid string, x *execution, mtime time.Time) uint64 {
	s := p.store
	var prior uint64
	if x.stored != nil {
		prior = x.stored.Version
	}
	if !x.modified {
		return prior
	}
	ver := max(s.lastVer[oid], prior) + 1
	s.lastVer[oid] = ver
	if x.removed && x.stored != nil {
		// The OSD disconnects an object's watchers when an op deletes it,
		// even one that creates it again (_delete_oid).
		s.breakWatches(oid, &radosclient.Error{Errno: int32(syscall.ENOTCONN), Op: opName("watch", oid)})
	}
	if x.obj == nil {
		delete(s.objects, oid)
		return ver
	}
	x.obj.Version = ver
	if !mtime.IsZero() && !mtime.Equal(time.Unix(0, 0)) {
		x.obj.Mtime = mtime
	}
	s.objects[oid] = x.obj
	return ver
}
