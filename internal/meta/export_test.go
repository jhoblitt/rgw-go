package meta

// StripesUpTo is Stripes with limit standing for MaxStripes, so a spec can
// reach the bound without building millions of stripes.
func (m Manifest) StripesUpTo(limit int) ([]Stripe, error) { return m.stripes(limit) }

// PartBoundsUpTo is PartBounds with limit standing for MaxWalkStripes, so a
// spec can reach the bound without walking tens of millions of stripes.
func (m Manifest) PartBoundsUpTo(n, limit int) (ofs, size uint64, head Obj, ok bool, err error) {
	return m.partBounds(n, limit)
}

// WalkPartsUpTo is WalkParts with limit standing for MaxWalkStripes.
func (m Manifest) WalkPartsUpTo(limit int) (*PartWalk, error) { return m.walkParts(limit) }
