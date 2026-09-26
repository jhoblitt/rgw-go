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
