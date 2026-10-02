package radosclient

import "context"

// ListSource is a listing of one namespace in RADOS's order, hobject order,
// which ListPage pages.
type ListSource interface {
	// Seek repositions the listing for a resume at placement hash h. It may
	// land anywhere at or before the first object hashing to h: librados
	// lands at the start of h's placement group on a fresh listing.
	Seek(h uint32)
	// Next returns the next object and its locator, and false once the
	// listing ends or fails.
	Next() (oid, locator string, ok bool)
	// Err is what ended the listing, nil at its end.
	Err() error
}

// ListPage is Pool.ListObjectsFrom over src, a listing of namespace ns. For
// a token it seeks src to the placement hash of the object the token names
// and skips what hobject order puts at or before that object. It delivers
// at most limit objects when limit > 0, and reads one more to tell whether
// anything follows.
func ListPage(ctx context.Context, src ListSource, ns, token string, limit int, fn func(oid, locator string) error) (next string, more bool, err error) {
	var lastOID string
	var lastHash uint32
	resuming := token != ""
	if resuming {
		if lastOID, err = DecodeListToken(token); err != nil {
			return "", false, err
		}
		lastHash = PlacementHash(ns, lastOID)
		src.Seek(lastHash)
	}
	n := 0
	var delivered string
	for {
		oid, locator, ok := src.Next()
		if !ok {
			return "", false, src.Err()
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", false, ctxErr
		}
		if resuming {
			if !ListAfter(PlacementHash(ns, oid), oid, lastHash, lastOID) {
				continue
			}
			// The listing is in hobject order, so nothing after this entry
			// comes before the resume point.
			resuming = false
		}
		if limit > 0 && n == limit {
			return EncodeListToken(delivered), true, nil
		}
		if fnErr := fn(oid, locator); fnErr != nil {
			return "", false, fnErr
		}
		delivered = oid
		n++
	}
}
