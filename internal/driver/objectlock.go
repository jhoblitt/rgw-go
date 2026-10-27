package driver

import (
	"fmt"
	"maps"
	"time"

	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/objectlock"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// headAttrsSeen, which only the specs set, sees each finalizeHeadAttrs call.
var headAttrsSeen func(s *Store, rec *op.BucketRecord, mtime time.Time, create bool)

// finalizeHeadAttrs is where a head write's attrs take what its bucket gives
// each object it creates, once per head write: rec is the bucket, mtime the
// time the write stamps and create whether it is PUT_OBJ_CREATE. A create
// takes objectlock.DefaultRetention of rec and mtime, the rule's default
// retention unless attrs carry a retention, which the head write's own op
// then sets. attrs is returned as it is, or as a copy with the retention set.
//
// A lock configuration that does not decode fails a create with
// UnknownError, the -EIO radosgw fails every load of such a bucket with
// (docs/exclusions.md, "Some bucket sub-records are carried opaque in phase
// 0"), before the head write's index op is prepared.
func (s *Store) finalizeHeadAttrs(rec *op.BucketRecord, mtime time.Time, create bool, attrs map[string][]byte) (map[string][]byte, error) {
	if headAttrsSeen != nil {
		headAttrsSeen(s, rec, mtime, create)
	}
	if !create {
		return attrs, nil
	}
	r, err := objectlock.DefaultRetention(&rec.Info, attrs, mtime)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", op.ErrUnknown, err)
	}
	if r == nil {
		return attrs, nil
	}
	out := maps.Clone(attrs)
	if out == nil {
		out = map[string][]byte{}
	}
	out[meta.AttrObjectRetention] = encodeAt(*r, s.release)
	return out, nil
}
