package meta

// NewTrivialManifest is the manifest AtomicObjectProcessor::prepare holds after
// set_trivial_rule and generator::create_begin, before any data: an empty head,
// one rule keyed at 0 that starts at maxHeadSize with no part size and stripes
// of stripeSize, tails in head's bucket under head's instance, and tailRule
// with what it leaves empty taken from headRule. maxHeadSize is the head chunk
// size, or 0 when the tail pool differs from the head's or the placement keeps
// no data inline. prefix names the tails; for a plain PUT radosgw makes it "."
// plus 31 characters of gen_rand_alphanumeric's url-safe base64 alphabet plus
// "_", as create_begin asks for a 32-byte string and the NUL takes one.
func NewTrivialManifest(head Obj, headRule, tailRule PlacementRule, prefix string, maxHeadSize, stripeSize uint64) Manifest {
	m := NewManifest()
	m.Obj = head
	m.HeadPlacementRule = headRule
	m.MaxHeadSize = maxHeadSize
	m.Prefix = prefix
	m.Rules = map[uint64]ManifestRule{0: {StartOfs: maxHeadSize, StripeMaxSize: stripeSize}}
	m.TailPlacement = BucketPlacement{Bucket: head.Bucket, PlacementRule: tailRule.InheritFrom(headRule)}
	m.TailInstance = head.Key.Instance
	return m
}

// SetObjSize is generator::create_next at the object's final offset: the head
// holds the object up to MaxHeadSize. create_next's refusal of an offset
// behind the last is not kept, as the writer sets the size once.
func (m *Manifest) SetObjSize(size uint64) {
	m.ObjSize = size
	m.HeadSize = min(size, m.MaxHeadSize)
}

// TailStripe is the stripe generator::create_next assigns to offset ofs,
// which must be at least MaxHeadSize: the stripes of the rule at offset 0
// counted from MaxHeadSize, from 1 when MaxHeadSize is non-zero, as stripe 0
// is then the head. Like create_next it divides by the rule's stripe size, so
// a rule without one panics.
func (m Manifest) TailStripe(ofs uint64) uint64 {
	n := (ofs - m.MaxHeadSize) / m.Rules[0].StripeMaxSize
	if m.MaxHeadSize > 0 {
		n++
	}
	return n
}

// TailObj is get_implicit_location for part 0 and stripe n past the head, as
// the reader names it: "<prefix><n>" in the shadow namespace under
// TailInstance, in the tail placement's bucket, or the head's when that is
// unset. Like radosgw it names n by its low 32 bits as an int, so the names
// go negative at 2^31 and repeat at 2^32.
func (m Manifest) TailObj(n uint64) Obj {
	return implicitLocation(&m, 0, int32(n), m.MaxHeadSize, "").obj //nolint:gosec // get_implicit_location prints (int)cur_stripe
}
