package meta

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"

	"github.com/jhoblitt/rgw-go/internal/denc"
)

// The namespaces of tail objects, RGW_OBJ_NS_SHADOW and RGW_OBJ_NS_MULTIPART.
const (
	NSShadow    = "shadow"
	NSMultipart = "multipart"
)

// MaxStripes bounds the stripes Stripes returns, which it builds in memory,
// a few hundred MB at the bound. It covers less than radosgw stores: radosgw
// caps a multipart object at rgw_max_put_size ×
// rgw_multipart_part_upload_limit, about 48.8 TiB at its defaults, not at
// S3's 5 TiB, and an appendable object at nothing. Stripes therefore refuses
// any object over about 8 TiB in the default 4 MiB stripes, and one appended
// to more than 2^21 times at any stripe size. A caller that walks whole
// objects that large iterates with Seek and Next instead, bounded by
// MaxWalkStripes as PartBounds is.
const MaxStripes = 1 << 21

// ErrTooManyStripes is the refusal of a manifest that lays out more stripes
// than a walk's bound: MaxStripes for Stripes, MaxWalkStripes for PartBounds
// and WalkParts.
var ErrTooManyStripes = errors.New("meta: manifest lays out too many stripes")

// Stripe describes one RADOS object of an object's data as the manifest lays
// it out: Size bytes of the object from Ofs on, stored in Obj from LocOfs on.
type Stripe struct {
	Ofs, Size uint64
	// LocOfs is where the data starts within Obj; only an explicit manifest's
	// pieces start anywhere but 0.
	LocOfs uint64
	Obj    Obj
	// Placement is the placement rule that selects Obj's pool; an explicit
	// manifest leaves it empty, as the C++ iterator does.
	Placement PlacementRule
	// InHead reports that Obj is the head object.
	InHead bool
}

// OID is the RADOS object name of the stripe, as get_obj_bucket_and_oid_loc
// derives it: the bucket marker, "_", then the key's oid.
func (s Stripe) OID() string { return prependMarker(s.Obj.Bucket.Marker, s.Obj.Key.OID()) }

// Locator is the stripe's RADOS locator, as get_obj_bucket_and_oid_loc
// derives it; it is empty for every name radosgw writes today.
func (s Stripe) Locator() string {
	if loc := s.Obj.Key.Locator(); loc != "" {
		return prependMarker(s.Obj.Bucket.Marker, loc)
	}
	return ""
}

// Stripes walks the manifest from offset 0 to ObjSize exactly as a loop from
// RGWObjManifest::obj_begin to obj_end does, returning the stripe at every
// step. It builds every stripe eagerly, and is meant for gc, delete and tests
// rather than for serving reads.
//
// A stripe's offset is where it starts, obj_iterator::get_stripe_ofs. The
// size of a stripe of an explicit manifest is its piece's size, as
// obj_iterator::get_stripe_size reports it; the size of a rule-based stripe
// is the distance to the next stripe's start, as
// RGWObjManifest::convert_to_explicit measures it, which trims the stripe
// size the iterator reports to the end of the object.
//
// A manifest without rules whose head holds the whole object is its head
// alone. operator++ cannot leave the head without a rule:
// RGWRados::iterate_obj never asks it to, as it stops at the end of the
// object, but update_gc_chain, which an overwrite or delete runs unless it
// keeps the tail, asks and never ends, since nothing there checks has_tail.
// radosgw writes no such manifest, as its generator refuses one without a
// rule. Stripes fails with denc.ErrMalformed where the C++ iterator would
// read past the end of a map or never reach ObjSize, and with
// ErrTooManyStripes past MaxStripes stripes.
func (m Manifest) Stripes() ([]Stripe, error) { return m.stripes(MaxStripes) }

// stripes is Stripes with limit standing for MaxStripes.
func (m Manifest) stripes(limit int) ([]Stripe, error) {
	if n := m.minStripes(); n > uint64(limit) { //nolint:gosec // limit is positive
		return nil, fmt.Errorf("%w: at least %d, more than %d", ErrTooManyStripes, n, limit)
	}
	it, err := newManifestIter(&m, 0)
	if err != nil {
		return nil, err
	}
	var out []Stripe
	for it.ofs != m.ObjSize {
		if len(out) == limit {
			return nil, fmt.Errorf("%w: more than %d before offset %d of %d", ErrTooManyStripes, limit, it.ofs, m.ObjSize)
		}
		s := Stripe{
			Ofs:       it.stripeStart(),
			Obj:       it.location.obj,
			Placement: it.location.placement,
			InHead:    sameBucket(it.location.obj.Bucket, m.Obj.Bucket) && it.location.obj.Key == m.Obj.Key,
		}
		if m.ExplicitObjs {
			p := m.Objs[it.objKeys[it.explicit]]
			s.Size, s.LocOfs = p.Size, p.LocOfs
		}
		if !m.ExplicitObjs && len(m.Rules) == 0 && m.HeadSize >= m.ObjSize {
			s.Size = m.ObjSize - s.Ofs
			return append(out, s), nil
		}
		prev := it.ofs
		if err := it.next(); err != nil {
			return nil, err
		}
		if it.ofs != m.ObjSize && it.ofs <= prev {
			return nil, fmt.Errorf("%w: manifest iteration does not advance past offset %d of %d", denc.ErrMalformed, prev, m.ObjSize)
		}
		if !m.ExplicitObjs {
			s.Size = it.stripeStart() - s.Ofs
		}
		out = append(out, s)
	}
	return out, nil
}

// minStripes is a lower bound on the stripes the manifest lays out, cheap
// enough to refuse a manifest before walking it: its pieces when explicit,
// and otherwise the tail past the head cut into the largest stripes any rule
// allows. A manifest whose rules allow no stripe at all bounds nothing here;
// the walk fails on it.
func (m Manifest) minStripes() uint64 {
	if m.ExplicitObjs {
		return uint64(len(m.Objs))
	}
	var largest uint64
	for _, r := range m.Rules {
		largest = max(largest, r.StripeMaxSize)
	}
	tail := m.ObjSize - min(m.HeadSize, m.ObjSize)
	if largest == 0 || tail == 0 {
		return 0
	}
	n := tail / largest
	if tail%largest != 0 {
		n++
	}
	return n
}

// objSelect is rgw_obj_select as the iterator fills it: never raw.
type objSelect struct {
	placement PlacementRule
	obj       Obj
}

// manifestIter is RGWObjManifest::obj_iterator. Map iterators are indexes
// into the sorted keys, the key count standing for end(). curPartID and
// curStripe are the C++ ints, with the C++ conversions from the u64
// arithmetic that feeds them.
type manifestIter struct {
	m        *Manifest
	ruleKeys []uint64
	objKeys  []uint64

	partOfs, stripeOfs, ofs, stripeSize uint64
	curPartID, curStripe                int32
	curOverridePrefix                   string
	location                            objSelect

	rule int
	// nextRule is -1 while next_rule_iter is still default-constructed,
	// which the C++ dereferences only by crashing.
	nextRule int
	explicit int
}

func sortedKeys[K cmp.Ordered, V any](m map[K]V) []K { return slices.Sorted(maps.Keys(m)) }

// newManifestIter returns an iterator at offset o, as obj_iterator's
// constructor does.
func newManifestIter(m *Manifest, o uint64) (*manifestIter, error) {
	it := &manifestIter{m: m, ruleKeys: sortedKeys(m.Rules), objKeys: sortedKeys(m.Objs), nextRule: -1}
	if err := it.seek(o); err != nil {
		return nil, err
	}
	return it, nil
}

// stripeStart is obj_iterator::get_stripe_ofs.
func (it *manifestIter) stripeStart() uint64 {
	if it.m.ExplicitObjs {
		return it.objKeys[it.explicit]
	}
	return it.stripeOfs
}

// upperBound is std::map::upper_bound over sorted keys.
func upperBound(keys []uint64, v uint64) int {
	i, found := slices.BinarySearch(keys, v)
	if found {
		i++
	}
	return i
}

func (it *manifestIter) ruleAt(i int) ManifestRule { return it.m.Rules[it.ruleKeys[i]] }

// seek mirrors RGWObjManifest::obj_iterator::seek.
func (it *manifestIter) seek(o uint64) error {
	m := it.m
	it.ofs = o
	if m.ExplicitObjs {
		it.explicit = upperBound(it.objKeys, it.ofs)
		if it.explicit != 0 {
			it.explicit--
		}
		if it.ofs < m.ObjSize {
			if it.explicit == len(it.objKeys) {
				return fmt.Errorf("%w: explicit manifest of %d bytes has no pieces", denc.ErrMalformed, m.ObjSize)
			}
			it.updateExplicitPos()
		} else {
			it.ofs = m.ObjSize
		}
		it.updateLocation()
		return nil
	}
	if o < m.HeadSize {
		it.rule = 0
		it.stripeOfs = 0
		it.stripeSize = m.HeadSize
		if it.rule != len(it.ruleKeys) {
			r := it.ruleAt(it.rule)
			it.curPartID = int32(r.StartPartNum) //nolint:gosec // the C++ converts the u32 to int
			it.curOverridePrefix = r.OverridePrefix
		}
		it.updateLocation()
		return nil
	}

	it.rule = upperBound(it.ruleKeys, it.ofs)
	it.nextRule = it.rule
	if it.rule != 0 {
		it.rule--
	}
	if it.rule == len(it.ruleKeys) {
		it.updateLocation()
		return nil
	}
	r := it.ruleAt(it.rule)
	if r.PartSize > 0 {
		it.curPartID = int32(uint64(r.StartPartNum) + (it.ofs-r.StartOfs)/r.PartSize) //nolint:gosec // the C++ truncates the u64 to int
	} else {
		it.curPartID = int32(r.StartPartNum) //nolint:gosec // the C++ converts the u32 to int
	}
	it.partOfs = r.StartOfs + uint64(uint32(it.curPartID)-r.StartPartNum)*r.PartSize //nolint:gosec // int - u32 is u32 arithmetic in C++
	if r.StripeMaxSize > 0 {
		it.curStripe = int32((it.ofs - it.partOfs) / r.StripeMaxSize)           //nolint:gosec // the C++ truncates the u64 to int
		it.stripeOfs = it.partOfs + uint64(int64(it.curStripe))*r.StripeMaxSize //nolint:gosec // the C++ sign-extends the int to u64
		if it.curPartID == 0 && m.HeadSize > 0 {
			it.curStripe++
		}
	} else {
		it.curStripe = 0
		it.stripeOfs = it.partOfs
	}
	if r.PartSize == 0 {
		it.stripeSize = min(m.ObjSize-it.stripeOfs, r.StripeMaxSize)
	} else {
		next := min(it.stripeOfs+r.StripeMaxSize, it.partOfs+r.PartSize)
		it.stripeSize = next - it.stripeOfs
	}
	it.curOverridePrefix = r.OverridePrefix
	it.updateLocation()
	return nil
}

// next mirrors RGWObjManifest::obj_iterator::operator++.
func (it *manifestIter) next() error {
	m := it.m
	if m.ExplicitObjs {
		it.explicit++
		if it.explicit == len(it.objKeys) {
			it.ofs = m.ObjSize
			it.stripeSize = 0
			return nil
		}
		it.updateExplicitPos()
		it.updateLocation()
		return nil
	}
	if it.ofs == m.ObjSize || len(it.ruleKeys) == 0 {
		return nil
	}
	if it.ofs < m.HeadSize {
		it.rule = 0
		r := it.ruleAt(it.rule)
		it.ofs = min(m.HeadSize, m.ObjSize)
		it.stripeOfs = it.ofs
		it.curStripe = 1
		it.stripeSize = min(m.ObjSize-it.ofs, r.StripeMaxSize)
		if r.PartSize > 0 {
			it.stripeSize = min(it.stripeSize, r.PartSize)
		}
		it.updateLocation()
		return nil
	}

	r := it.ruleAt(it.rule)
	it.stripeOfs += r.StripeMaxSize
	it.curStripe++
	if r.PartSize > 0 {
		if it.stripeOfs >= it.partOfs+r.PartSize {
			it.curStripe = 0
			it.partOfs += r.PartSize
			it.stripeOfs = it.partOfs
			if it.nextRule < 0 {
				return fmt.Errorf("%w: manifest part boundary at offset %d follows the head, where the C++ iterator has no next rule", denc.ErrMalformed, it.partOfs)
			}
			if it.nextRule != len(it.ruleKeys) && it.stripeOfs >= it.ruleAt(it.nextRule).StartOfs {
				it.rule = it.nextRule
				it.nextRule++
				it.curPartID = int32(it.ruleAt(it.rule).StartPartNum) //nolint:gosec // the C++ converts the u32 to int
			} else {
				it.curPartID++
			}
			r = it.ruleAt(it.rule)
		}
		it.stripeSize = min(r.PartSize-(it.stripeOfs-it.partOfs), r.StripeMaxSize)
	}
	it.curOverridePrefix = r.OverridePrefix
	it.ofs = it.stripeOfs
	if it.ofs > m.ObjSize {
		it.ofs = m.ObjSize
		it.stripeOfs = it.ofs
		it.stripeSize = 0
	}
	it.updateLocation()
	return nil
}

// updateExplicitPos mirrors obj_iterator::update_explicit_pos.
func (it *manifestIter) updateExplicitPos() {
	it.ofs = it.objKeys[it.explicit]
	it.stripeOfs = it.ofs
	if it.explicit+1 < len(it.objKeys) {
		it.stripeSize = it.objKeys[it.explicit+1] - it.ofs
	} else {
		it.stripeSize = it.m.ObjSize - it.ofs
	}
}

// updateLocation mirrors obj_iterator::update_location. Assigning an rgw_obj
// to an rgw_obj_select keeps its placement rule, so an explicit piece's
// location keeps whatever rule the iterator last held, which is none.
func (it *manifestIter) updateLocation() {
	m := it.m
	if m.ExplicitObjs {
		if len(m.Objs) == 0 {
			it.location = objSelect{}
			return
		}
		it.location.obj = m.Objs[it.objKeys[it.explicit]].Loc
		return
	}
	if it.ofs < m.HeadSize {
		it.location = objSelect{placement: m.HeadPlacementRule, obj: m.Obj}
		return
	}
	it.location = implicitLocation(it.m, it.curPartID, it.curStripe, it.ofs, it.curOverridePrefix)
}

// implicitLocation mirrors RGWObjManifest::get_implicit_location: the head
// below MaxHeadSize in part 0, and otherwise the tail object named
// <prefix><stripe> in the shadow namespace for part 0, <prefix>.<part> in the
// multipart namespace for a part's first stripe, and <prefix>.<part>_<stripe>
// in the shadow namespace for the rest. The C++ prints the part and stripe
// as (int), so they arrive here as int32.
func implicitLocation(m *Manifest, partID, stripe int32, ofs uint64, overridePrefix string) objSelect {
	oid := m.Prefix
	if overridePrefix != "" {
		oid = overridePrefix
	}
	var ns string
	switch {
	case partID == 0 && ofs < m.MaxHeadSize:
		return objSelect{placement: m.HeadPlacementRule, obj: m.Obj}
	case partID == 0:
		oid += strconv.Itoa(int(stripe))
		ns = NSShadow
	case stripe == 0:
		oid += "." + strconv.Itoa(int(partID))
		ns = NSMultipart
	default:
		oid += "." + strconv.Itoa(int(partID)) + "_" + strconv.Itoa(int(stripe))
		ns = NSShadow
	}
	loc := Obj{Bucket: m.Obj.Bucket, Key: ObjKey{Name: oid, Instance: m.TailInstance, NS: ns}}
	if m.TailPlacement.Bucket.Name != "" {
		loc.Bucket = m.TailPlacement.Bucket
	}
	return objSelect{placement: m.TailPlacement.PlacementRule, obj: loc}
}

// iterDump is obj_iterator::dump.
type iterDump struct {
	PartOfs           uint64     `json:"part_ofs"`
	StripeOfs         uint64     `json:"stripe_ofs"`
	Ofs               uint64     `json:"ofs"`
	StripeSize        uint64     `json:"stripe_size"`
	CurPartID         int32      `json:"cur_part_id"`
	CurStripe         int32      `json:"cur_stripe"`
	CurOverridePrefix string     `json:"cur_override_prefix"`
	Location          selectDump `json:"location"`
}

// selectDump is rgw_obj_select::dump; the iterator never sets the raw object.
type selectDump struct {
	PlacementRule PlacementRule `json:"placement_rule"`
	Obj           Obj           `json:"obj"`
	RawObj        RawObj        `json:"raw_obj"`
	IsRaw         bool          `json:"is_raw"`
}

func (it *manifestIter) dump() iterDump {
	return iterDump{
		PartOfs:           it.partOfs,
		StripeOfs:         it.stripeOfs,
		Ofs:               it.ofs,
		StripeSize:        it.stripeSize,
		CurPartID:         it.curPartID,
		CurStripe:         it.curStripe,
		CurOverridePrefix: it.curOverridePrefix,
		Location:          selectDump{PlacementRule: it.location.placement, Obj: it.location.obj},
	}
}
