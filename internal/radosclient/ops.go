package radosclient

import (
	"slices"
	"time"
)

// ReadOp is a compound read operation, built then run once through Pool.Read.
// It retains the slices and maps passed to its builders; do not modify them
// until the op has run.
type ReadOp struct {
	steps []Step
}

// NewReadOp returns an empty read op.
func NewReadOp() *ReadOp { return &ReadOp{} }

// Steps returns the op's steps in call order.
func (o *ReadOp) Steps() []Step { return slices.Clone(o.steps) }

// AssertExists fails the op with ENOENT unless the object exists.
func (o *ReadOp) AssertExists() { o.steps = append(o.steps, &AssertExistsStep{}) }

// AssertVersion fails the op unless the object is at version ver.
func (o *ReadOp) AssertVersion(ver uint64) {
	o.steps = append(o.steps, &AssertVersionStep{Version: ver})
}

// CmpXattr fails the op unless the named xattr compares to value under op.
func (o *ReadOp) CmpXattr(name string, op CmpOp, value []byte) {
	o.steps = append(o.steps, &CmpXattrStep{Name: name, Op: op, Value: value})
}

// Read reads length bytes at offset.
func (o *ReadOp) Read(offset, length uint64) *ReadResult {
	r := &ReadResult{}
	o.steps = append(o.steps, &ReadStep{Offset: offset, Length: length, Result: r})
	return r
}

// Stat reads the object's size and modification time.
func (o *ReadOp) Stat() *StatResult {
	r := &StatResult{}
	o.steps = append(o.steps, &StatStep{Result: r})
	return r
}

// GetXattrs reads every extended attribute.
func (o *ReadOp) GetXattrs() *XattrsResult {
	r := &XattrsResult{}
	o.steps = append(o.steps, &GetXattrsStep{Result: r})
	return r
}

// OmapGetVals reads up to maxEntries omap entries after startAfter whose keys begin with filterPrefix.
func (o *ReadOp) OmapGetVals(startAfter, filterPrefix string, maxEntries uint64) *OmapResult {
	r := &OmapResult{}
	o.steps = append(o.steps, &OmapGetValsStep{StartAfter: startAfter, FilterPrefix: filterPrefix, Max: maxEntries, Result: r})
	return r
}

// OmapGetValsByKeys reads the omap entries for keys.
func (o *ReadOp) OmapGetValsByKeys(keys []string) *OmapResult {
	r := &OmapResult{}
	o.steps = append(o.steps, &OmapGetValsByKeysStep{Keys: keys, Result: r})
	return r
}

// OmapGetKeys reads up to maxKeys omap keys after startAfter.
func (o *ReadOp) OmapGetKeys(startAfter string, maxKeys uint64) *OmapKeysResult {
	r := &OmapKeysResult{}
	o.steps = append(o.steps, &OmapGetKeysStep{StartAfter: startAfter, Max: maxKeys, Result: r})
	return r
}

// Exec calls the object-class method class.method with input in.
func (o *ReadOp) Exec(class, method string, in []byte) *ExecResult {
	var r *ExecResult
	o.steps, r = appendExec(o.steps, class, method, in)
	return r
}

// WriteOp is a compound write operation, built then run once through Pool.Write.
// It retains the slices and maps passed to its builders; do not modify them
// until the op has run.
type WriteOp struct {
	steps []Step
	mtime *time.Time
}

// NewWriteOp returns an empty write op.
func NewWriteOp() *WriteOp { return &WriteOp{} }

// Steps returns the op's steps in call order.
func (o *WriteOp) Steps() []Step { return slices.Clone(o.steps) }

// SetMtime sets the modification time the op stamps on the object. It
// applies to the whole op, so it is not a step.
func (o *WriteOp) SetMtime(t time.Time) { o.mtime = &t }

// Mtime returns the time SetMtime recorded, and whether it was called.
func (o *WriteOp) Mtime() (time.Time, bool) {
	if o.mtime == nil {
		return time.Time{}, false
	}
	return *o.mtime, true
}

// AssertExists fails the op with ENOENT unless the object exists.
func (o *WriteOp) AssertExists() { o.steps = append(o.steps, &AssertExistsStep{}) }

// AssertVersion fails the op unless the object is at version ver.
func (o *WriteOp) AssertVersion(ver uint64) {
	o.steps = append(o.steps, &AssertVersionStep{Version: ver})
}

// CmpXattr fails the op unless the named xattr compares to value under op.
func (o *WriteOp) CmpXattr(name string, op CmpOp, value []byte) {
	o.steps = append(o.steps, &CmpXattrStep{Name: name, Op: op, Value: value})
}

// Create creates the object, failing with EEXIST if exclusive and it exists.
func (o *WriteOp) Create(exclusive bool) {
	o.steps = append(o.steps, &CreateStep{Exclusive: exclusive})
}

// Remove deletes the object.
func (o *WriteOp) Remove() { o.steps = append(o.steps, &RemoveStep{}) }

// WriteFull replaces the object's data with data.
func (o *WriteOp) WriteFull(data []byte) { o.steps = append(o.steps, &WriteFullStep{Data: data}) }

// Write writes data at offset.
func (o *WriteOp) Write(data []byte, offset uint64) {
	o.steps = append(o.steps, &WriteStep{Data: data, Offset: offset})
}

// Append appends data to the object.
func (o *WriteOp) Append(data []byte) { o.steps = append(o.steps, &AppendStep{Data: data}) }

// Zero zeroes length bytes at offset.
func (o *WriteOp) Zero(offset, length uint64) {
	o.steps = append(o.steps, &ZeroStep{Offset: offset, Length: length})
}

// Truncate truncates the object to offset bytes.
func (o *WriteOp) Truncate(offset uint64) { o.steps = append(o.steps, &TruncateStep{Offset: offset}) }

// SetXattr sets the named extended attribute to value.
func (o *WriteOp) SetXattr(name string, value []byte) {
	o.steps = append(o.steps, &SetXattrStep{Name: name, Value: value})
}

// RmXattr removes the named extended attribute.
func (o *WriteOp) RmXattr(name string) { o.steps = append(o.steps, &RmXattrStep{Name: name}) }

// OmapSet sets the omap entries in kv.
func (o *WriteOp) OmapSet(kv map[string][]byte) { o.steps = append(o.steps, &OmapSetStep{Values: kv}) }

// OmapRmKeys removes the given omap keys.
func (o *WriteOp) OmapRmKeys(keys []string) { o.steps = append(o.steps, &OmapRmKeysStep{Keys: keys}) }

// OmapClear removes every omap entry.
func (o *WriteOp) OmapClear() { o.steps = append(o.steps, &OmapClearStep{}) }

// OmapCmp fails the op unless the omap value at key compares to value under op.
func (o *WriteOp) OmapCmp(key string, op CmpOp, value []byte) {
	o.steps = append(o.steps, &OmapCmpStep{Key: key, Op: op, Value: value})
}

// SetAllocHint advises the OSD of the object's expected size and write size.
func (o *WriteOp) SetAllocHint(expectedObjectSize, expectedWriteSize uint64, flags AllocHintFlags) {
	o.steps = append(o.steps, &SetAllocHintStep{
		ExpectedObjectSize: expectedObjectSize,
		ExpectedWriteSize:  expectedWriteSize,
		Flags:              flags,
	})
}

// Exec calls the object-class method class.method with input in.
func (o *WriteOp) Exec(class, method string, in []byte) *ExecResult {
	var r *ExecResult
	o.steps, r = appendExec(o.steps, class, method, in)
	return r
}

func appendExec(steps []Step, class, method string, in []byte) ([]Step, *ExecResult) {
	r := &ExecResult{op: "exec " + class + "." + method}
	return append(steps, &ExecStep{Class: class, Method: method, In: in, Result: r}), r
}
