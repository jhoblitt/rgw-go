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
	Stored *Object
	x      *execution
}

// Object returns the object as the op has left it so far, nil when it does
// not exist now. Its existence is what a class's cls_cxx_stat sees, and an
// emulator writes through it.
func (c *ClassCall) Object() *Object { return c.x.obj }

// Create returns the op's object, creating an empty one when it does not
// exist, as a class's first write to a missing object creates it.
func (c *ClassCall) Create() *Object { return c.x.create() }

// Remove removes the op's object and returns 0, or -ENOENT when it does not
// exist, as cls_cxx_remove does.
func (c *ClassCall) Remove() int32 {
	if c.x.obj == nil {
		return -int32(syscall.ENOENT)
	}
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
	radosclient.OpFlagIgnoreCache | radosclient.OpFlagReturnVec

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
	// WR flag (OpInfo::set_from_op). An op that does not write finds no
	// object context for a missing object (PrimaryLogPG::do_op).
	for _, st := range steps {
		s, ok := st.(*radosclient.ExecStep)
		if !ok {
			x.mayWrite = x.mayWrite || writes(st)
			continue
		}
		cls, ok := x.cluster.classes[s.Class]
		if !ok {
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
// goceph fills each result from the op's error when the op fails.
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
	if x.stored != nil {
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

// exec runs a class call; what it changes in the object is a change to it.
func (x *execution) exec(s *radosclient.ExecStep) (func(), error) {
	cls := x.cluster.classes[s.Class]
	call := &ClassCall{Method: s.Method, In: slices.Clone(s.In), Stored: x.stored.clone(), x: x}
	before := x.obj.clone()
	out, rval := cls.fn(call)
	if !sameContent(before, x.obj) {
		x.modified = true
		if !cls.writes[s.Method] {
			// The OSD fails a method that updates the object without the
			// WR flag, before taking its output (PrimaryLogPG::do_osd_ops).
			return func() { s.Result.Set(nil, -int32(syscall.EIO)) }, x.errno(syscall.EIO)
		}
	}
	x.reply(len(out))
	if rval < 0 {
		return func() { s.Result.Set(nil, rval) }, &radosclient.Error{Errno: -rval, Op: x.name}
	}
	if x.writeOp {
		out = nil
	}
	out = slices.Clone(out)
	return func() { s.Result.Set(out, 0) }, nil
}

// commit stores what a successful op left and returns the object's version
// after it; the cluster's lock is held. An op that changed nothing leaves the
// object and its version, as the OSD does an op with an empty transaction,
// and a zero mtime leaves the object's own, as the OSD does for an op
// without one.
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
	if !mtime.IsZero() {
		x.obj.Mtime = mtime
	}
	s.objects[oid] = x.obj
	return ver
}
