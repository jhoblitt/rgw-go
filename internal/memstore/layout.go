package memstore

import (
	"context"
	"fmt"

	"github.com/jhoblitt/rgw-go/internal/op"
)

// ObjectLayout implements op.LayoutStore. It answers NotImplemented, as the
// store holds objects in maps rather than RADOS objects.
func (s *Store) ObjectLayout(_ context.Context, st *op.ObjectState) (op.LayoutDocument, error) {
	return nil, fmt.Errorf("%w: laying out %s, which memstore keeps in no RADOS object", op.ErrNotImplemented, st.Key.Name)
}
