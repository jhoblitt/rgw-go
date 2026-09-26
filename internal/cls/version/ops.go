package version

import (
	"fmt"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// The class and the method names its CLS_INIT registers.
const (
	class            = "version"
	methodSet        = "set"
	methodInc        = "inc"
	methodIncConds   = "inc_conds"
	methodRead       = "read"
	methodCheckConds = "check_conds"
)

func encode(v interface {
	Encode(*denc.Encoder, denc.Release)
}, r denc.Release,
) []byte {
	e := denc.NewEncoder()
	v.Encode(e, r)
	return e.Bytes()
}

// Set mirrors cls_version_set: it stores v as the object's version,
// unconditionally and exactly as given; it never initializes one.
func Set(op radosclient.Execer, v ObjVersion, r denc.Release) {
	op.Exec(class, methodSet, encode(SetOp{Objv: v}, r))
}

// Inc mirrors cls_version_inc without conditions: it increments the stored
// version. On an object with none it first initializes Ver 1 with a random
// tag, so the first Inc leaves Ver 2.
func Inc(op radosclient.Execer, r denc.Release) {
	op.Exec(class, methodInc, encode(IncOp{}, r))
}

// IncConds mirrors cls_version_inc with a condition: it increments the stored
// version only if it satisfies cond against v, and otherwise fails the op
// with ErrCanceled.
func IncConds(op radosclient.Execer, v ObjVersion, cond Cond, r denc.Release) {
	op.Exec(class, methodIncConds, encode(IncOp{Objv: v, Conds: []Condition{{Ver: v, Cond: cond}}}, r))
}

// Check mirrors cls_version_check: it fails the op with ErrCanceled unless
// the stored version satisfies cond against v. An object without a version
// is checked as the zero ObjVersion.
func Check(op radosclient.Execer, v ObjVersion, cond Cond, r denc.Release) {
	op.Exec(class, methodCheckConds, encode(CheckOp{Objv: v, Conds: []Condition{{Ver: v, Cond: cond}}}, r))
}

// ReadResult is a pending "read" reply.
type ReadResult struct {
	res *radosclient.ExecResult
}

// Read mirrors cls_version_read on a read op. The request has no input, so
// the release selects nothing.
func Read(op *radosclient.ReadOp, _ denc.Release) *ReadResult {
	return &ReadResult{res: op.Exec(class, methodRead, nil)}
}

// Version decodes the stored version once the op has run; an object without
// one reads as the zero ObjVersion.
func (res *ReadResult) Version() (ObjVersion, error) {
	b, err := res.res.Bytes()
	if err != nil {
		return ObjVersion{}, err
	}
	d := denc.NewDecoder(b)
	ret := DecodeReadRet(d)
	if err := d.Err(); err != nil {
		return ObjVersion{}, fmt.Errorf("version: decoding read reply: %w", err)
	}
	return ret.Objv, nil
}
