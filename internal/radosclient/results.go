package radosclient

import "time"

// ExecResult is an exec step's output, filled by the implementation after the op runs.
type ExecResult struct {
	op   string
	out  []byte
	rval int32
	done bool
}

// Bytes returns the class method's output. It returns ErrIncomplete before
// the op ran, and an *Error carrying the errno when the method returned a
// negative value.
func (r *ExecResult) Bytes() ([]byte, error) {
	if !r.done {
		return nil, ErrIncomplete
	}
	if r.rval < 0 {
		return nil, &Error{Errno: -r.rval, Op: r.op}
	}
	return r.out, nil
}

// Reply returns the class method's output and its return value: the value
// when negative, and 0 for a success, a positive return included. A
// negative value keeps its output, since a method may encode a reply before
// it fails, as cls_rgw's bucket_list does before it answers advance-and-retry
// (https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L680-L691 at v19.2.7,
// https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L730-L741 at v20.2.4).
// A write op's call has no output, because librados's C write-op exec has
// no output buffer. When another step failed the op, there is no output and
// the value is the op's negative errno. It returns ErrIncomplete before the
// op ran.
func (r *ExecResult) Reply() (out []byte, rval int32, err error) {
	if !r.done {
		return nil, 0, ErrIncomplete
	}
	return r.out, r.rval, nil
}

// Set records the method's output and return value; implementations and tests call it.
func (r *ExecResult) Set(out []byte, rval int32) {
	r.out = out
	r.rval = rval
	r.done = true
}

// ReadResult is a read step's output.
type ReadResult struct {
	Data []byte
	N    int
	Err  error
}

// StatResult is a stat step's output.
type StatResult struct {
	Size    uint64
	ModTime time.Time
	Err     error
}

// XattrsResult is a get-xattrs step's output.
type XattrsResult struct {
	Xattrs map[string][]byte
	Err    error
}

// OmapResult is an omap-values step's output; More reports that entries remain past Max.
type OmapResult struct {
	Values map[string][]byte
	More   bool
	Err    error
}

// OmapKeysResult is an omap-keys step's output; More reports that keys remain past Max.
type OmapKeysResult struct {
	Keys []string
	More bool
	Err  error
}
