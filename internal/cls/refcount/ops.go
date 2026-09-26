package refcount

import (
	"fmt"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// The class and the method names its CLS_INIT registers.
const (
	class      = "refcount"
	methodGet  = "get"
	methodPut  = "put"
	methodSet  = "set"
	methodRead = "read"
)

func encode(v interface {
	Encode(*denc.Encoder, denc.Release)
}, r denc.Release,
) []byte {
	e := denc.NewEncoder()
	v.Encode(e, r)
	return e.Bytes()
}

// Get mirrors cls_refcount_get: it adds tag to the object's refs. With
// implicitRef, an object with no refcount xattr first gains the wildcard ref.
// The object must already exist: a get in the op that creates it fails the op
// with ErrNotFound.
func Get(op radosclient.Execer, tag string, implicitRef bool, r denc.Release) {
	op.Exec(class, methodGet, encode(GetOp{Tag: tag, ImplicitRef: implicitRef}, r))
}

// Put mirrors cls_refcount_put: it drops tag, or with implicitRef the
// wildcard ref when tag is not held, and removes the object when no ref is
// left. A tag already retired or not held is a no-op; an object with no refs
// at all fails the op with ErrInvalid.
func Put(op radosclient.Execer, tag string, implicitRef bool, r denc.Release) {
	op.Exec(class, methodPut, encode(PutOp{Tag: tag, ImplicitRef: implicitRef}, r))
}

// Set mirrors cls_refcount_set: it replaces the refs with refs and forgets
// the retired ones; an empty refs removes the object.
func Set(op radosclient.Execer, refs []string, r denc.Release) {
	op.Exec(class, methodSet, encode(SetOp{Refs: refs}, r))
}

// ReadResult is a pending "read" reply.
type ReadResult struct {
	res *radosclient.ExecResult
}

// Read mirrors cls_refcount_read on a read op. With implicitRef, an object
// with no refcount xattr reads as held by the wildcard tag "".
func Read(op *radosclient.ReadOp, implicitRef bool, r denc.Release) *ReadResult {
	return &ReadResult{res: op.Exec(class, methodRead, encode(ReadOp{ImplicitRef: implicitRef}, r))}
}

// Refs decodes the held tags, sorted, once the op has run.
func (res *ReadResult) Refs() ([]string, error) {
	b, err := res.res.Bytes()
	if err != nil {
		return nil, err
	}
	d := denc.NewDecoder(b)
	ret := DecodeReadRet(d)
	if err := d.Err(); err != nil {
		return nil, fmt.Errorf("refcount: decoding read reply: %w", err)
	}
	return ret.Refs, nil
}
