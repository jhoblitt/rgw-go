package meta

// StripesUpTo is Stripes with limit standing for MaxStripes, so a spec can
// reach the bound without building millions of stripes.
func (m Manifest) StripesUpTo(limit int) ([]Stripe, error) { return m.stripes(limit) }
