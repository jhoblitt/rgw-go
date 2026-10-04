package meta

import (
	"fmt"

	"github.com/jhoblitt/rgw-go/internal/denc"
)

// StripeIter is RGWObjManifest::obj_iterator: one position in the stripes a
// manifest lays out, moved by Seek and Next. A ranged read seeks to its first
// byte and advances until the range is covered, so it visits only the stripes
// the range touches.
type StripeIter struct{ it *manifestIter }

// Seek is RGWObjManifest::obj_find: the iterator at the stripe holding ofs,
// or at the end when ofs is at or past ObjSize. It fails with
// denc.ErrMalformed where the C++ iterator would read past a map.
func (m Manifest) Seek(ofs uint64) (*StripeIter, error) {
	it, err := newManifestIter(&m, min(ofs, m.ObjSize))
	if err != nil {
		return nil, err
	}
	return &StripeIter{it: it}, nil
}

// Done reports that the iterator is obj_end: its offset is the object's size.
func (it *StripeIter) Done() bool { return it.it.ofs == it.it.m.ObjSize }

// Ofs is get_ofs, the iterator's offset in the object.
func (it *StripeIter) Ofs() uint64 { return it.it.ofs }

// explicitEnd reports that an explicit manifest's iterator is at objs.end(),
// where the C++ accessors would dereference it: Next has stepped past the
// last piece, or there are no pieces. A seek to ObjSize instead rests on the
// last piece, as obj_end does.
func (it *StripeIter) explicitEnd() bool {
	return it.it.m.ExplicitObjs && it.it.explicit == len(it.it.objKeys)
}

// StripeOfs is get_stripe_ofs, where the current stripe starts in the object.
// Past an explicit manifest's last piece it is the end of the object.
func (it *StripeIter) StripeOfs() uint64 {
	if it.explicitEnd() {
		return it.it.ofs
	}
	return it.it.stripeStart()
}

// StripeSize is get_stripe_size: the current piece's size for an explicit
// manifest, 0 past its last piece, and otherwise the rule's stripe size.
// Stepping along a rule without a part size, Next, like operator++, leaves
// the last stripe at its full size where a seek trims it to the object, so a
// reader bounds a stripe by the object size as well.
func (it *StripeIter) StripeSize() uint64 {
	switch {
	case !it.it.m.ExplicitObjs:
		return it.it.stripeSize
	case it.explicitEnd():
		return 0
	default:
		return it.it.m.Objs[it.it.objKeys[it.it.explicit]].Size
	}
}

// LocOfs is location_ofs, where the stripe's data starts within its object;
// only an explicit manifest's pieces start anywhere but 0.
func (it *StripeIter) LocOfs() uint64 {
	if !it.it.m.ExplicitObjs || it.explicitEnd() {
		return 0
	}
	return it.it.m.Objs[it.it.objKeys[it.it.explicit]].LocOfs
}

// PartID is get_cur_part_id, the stripe's multipart part number; it is 0
// throughout a manifest that is not multipart.
func (it *StripeIter) PartID() int32 { return it.it.curPartID }

// Location is get_location: the stripe's object, the placement rule that
// selects its pool (empty for an explicit piece, as the C++ leaves it), and
// whether the object is the head.
func (it *StripeIter) Location() (obj Obj, placement PlacementRule, inHead bool) {
	loc, m := it.it.location, it.it.m
	return loc.obj, loc.placement, sameBucket(loc.obj.Bucket, m.Obj.Bucket) && loc.obj.Key == m.Obj.Key
}

// Next is operator++. Past an explicit manifest's last piece it does nothing,
// where the C++ would step past objs.end(). Like operator++ it stays put at
// the end of a rule-based manifest, on a manifest without rules and on a rule
// whose stripe size is 0, so a loop over Next needs a bound besides Done.
func (it *StripeIter) Next() error {
	if it.explicitEnd() {
		return nil
	}
	return it.it.next()
}

// MaxWalkStripes bounds the stripes a walk that keeps nothing per stripe,
// such as PartBounds', visits. radosgw caps a multipart object at
// rgw_max_put_size × rgw_multipart_part_upload_limit, 10,000 parts of 5 GiB
// at its defaults, which is 12,800,000 stripes of its default 4 MiB; the
// bound leaves five times that. It caps an appendable object at nothing. A
// walk to the bound takes about 6 s, some 90 ns a stripe on a Ryzen 9
// 7950X3D core.
const MaxWalkStripes = 1 << 26

// PartWalk is the walk from obj_begin over a multipart manifest's parts that
// obj_find_part, get_part_obj_state and RadosObject::list_parts take, bounded
// where theirs are not. It stands at a part's first stripe, where Part names
// the part and its head before Skip steps through the part's stripes, so a
// caller can read the head first, as get_part_obj_state does.
type PartWalk struct {
	it *StripeIter
	// stripes counts the stripes visited, the current one included.
	stripes, limit int
}

// WalkParts starts a PartWalk at the manifest's first stripe. Before any step
// it fails with ErrTooManyStripes where the object's tail needs more than
// MaxWalkStripes stripes; it fails with denc.ErrMalformed where the C++
// iterator would read past a map.
func (m Manifest) WalkParts() (*PartWalk, error) { return m.walkParts(MaxWalkStripes) }

// walkParts is WalkParts with limit standing for MaxWalkStripes.
func (m Manifest) walkParts(limit int) (*PartWalk, error) {
	if least := m.minStripes(); least > uint64(limit) { //nolint:gosec // limit is positive
		return nil, fmt.Errorf("%w: at least %d, more than %d", ErrTooManyStripes, least, limit)
	}
	it, err := m.Seek(0)
	if err != nil {
		return nil, err
	}
	return &PartWalk{it: it, stripes: 1, limit: limit}, nil
}

// Done reports that the walk has reached the end of the object.
func (w *PartWalk) Done() bool { return w.it.Done() }

// Part names the part the walk stands at: its number, where it starts in the
// object, and the object holding its first stripe, which is the part's head.
func (w *PartWalk) Part() (n int, ofs uint64, head Obj) {
	head, _, _ = w.it.Location()
	return int(w.it.PartID()), w.it.Ofs(), head
}

// Find is obj_find_part from where the walk stands: it steps to the first
// stripe of part n and reports true, or reports false where it meets a later
// part or the end first.
func (w *PartWalk) Find(n int) (bool, error) {
	for !w.it.Done() && int(w.it.PartID()) != n {
		if int(w.it.PartID()) > n {
			return false, nil
		}
		if err := w.step(); err != nil {
			return false, err
		}
	}
	return !w.it.Done(), nil
}

// Skip steps past the stripes of the part the walk stands at, to the next
// part's first stripe or the end, and returns the part's size: the distance to
// where the part id next changes or the object ends, as get_part_obj_state's
// generator sizes a part.
func (w *PartWalk) Skip() (uint64, error) {
	n, ofs := w.it.PartID(), w.it.Ofs()
	for !w.it.Done() && w.it.PartID() == n {
		if err := w.step(); err != nil {
			return 0, err
		}
	}
	return w.it.Ofs() - ofs, nil
}

// step is Next. It fails with denc.ErrMalformed where Next does not move past
// the previous offset, so on a step that stays put, where the C++ walk never
// ends, and on one that wraps backward, which the C++ follows. It fails with
// ErrTooManyStripes on the stripe past the walk's limit.
func (w *PartWalk) step() error {
	it := w.it
	prev := it.Ofs()
	if err := it.Next(); err != nil {
		return err
	}
	if it.Done() {
		return nil
	}
	if it.Ofs() <= prev {
		return fmt.Errorf("%w: manifest iteration does not advance past offset %d of %d", denc.ErrMalformed, prev, it.it.m.ObjSize)
	}
	w.stripes++
	if w.stripes > w.limit {
		return fmt.Errorf("%w: more than %d before offset %d of %d", ErrTooManyStripes, w.limit, it.Ofs(), it.it.m.ObjSize)
	}
	return nil
}

// HasTail is RGWObjManifest::has_tail: whether any of the object's data lies
// outside its head object. A lone explicit piece is the head when rgw_obj's
// == says so.
func (m Manifest) HasTail() bool {
	switch {
	case !m.ExplicitObjs:
		return m.ObjSize > m.HeadSize
	case len(m.Objs) == 1:
		return !sameObj(m.Objs[sortedKeys(m.Objs)[0]].Loc, m.Obj)
	default:
		return len(m.Objs) >= 2
	}
}

// sameObj is rgw_obj's ==, which compares the bucket as rgw_bucket's == does
// and the key's name and instance, but not its namespace.
func sameObj(a, b Obj) bool {
	return sameBucket(a.Bucket, b.Bucket) && a.Key.Name == b.Key.Name && a.Key.Instance == b.Key.Instance
}

// PartsCount is the parts count get_part_obj_state derives from obj_end's
// part id: 0 for a manifest that is not multipart, and otherwise that id less
// one, as it is one past the last part, but at least 1, as a single-part
// upload keeps its part's rule and ends at part 1.
func (m Manifest) PartsCount() (int, error) {
	end, err := m.Seek(m.ObjSize)
	if err != nil {
		return 0, err
	}
	last := int(end.PartID())
	if last == 0 {
		return 0, nil
	}
	return max(1, last-1), nil
}

// PartBounds is obj_find_part with the extent get_part_obj_state gives the
// part it finds: where part n starts in the object, its size up to where the
// part id next changes or the object ends, and the object holding its first
// stripe, which is the part's head. ok is false when the manifest is not
// multipart or has no part n. Like obj_find_part it walks the stripes from
// the start. It fails with denc.ErrMalformed where that walk does not move
// forward, and with ErrTooManyStripes where the object's tail needs more
// than MaxWalkStripes stripes, or the walk passes that many.
func (m Manifest) PartBounds(n int) (ofs, size uint64, head Obj, ok bool, err error) {
	return m.partBounds(n, MaxWalkStripes)
}

// partBounds is PartBounds with limit standing for MaxWalkStripes.
func (m Manifest) partBounds(n, limit int) (ofs, size uint64, head Obj, ok bool, err error) {
	end, err := m.Seek(m.ObjSize)
	if err != nil {
		return 0, 0, Obj{}, false, err
	}
	if end.PartID() == 0 {
		return 0, 0, Obj{}, false, nil
	}
	w, err := m.walkParts(limit)
	if err != nil {
		return 0, 0, Obj{}, false, err
	}
	if ok, err = w.Find(n); !ok || err != nil {
		return 0, 0, Obj{}, false, err
	}
	_, ofs, head = w.Part()
	if size, err = w.Skip(); err != nil {
		return 0, 0, Obj{}, false, err
	}
	return ofs, size, head, true, nil
}
