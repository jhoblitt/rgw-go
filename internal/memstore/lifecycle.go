package memstore

import (
	"context"

	"github.com/jhoblitt/rgw-go/internal/op"
)

var _ op.LifecycleStore = (*Store)(nil)

// SetEntry implements op.LifecycleStore. The store keeps no lifecycle index:
// only lifecycle workers read one, no op lists it, and the store runs no
// worker, so the entry is accepted and dropped.
func (s *Store) SetEntry(context.Context, *op.BucketRecord) error { return nil }

// RemoveEntry implements op.LifecycleStore; there is no entry to remove.
func (s *Store) RemoveEntry(context.Context, *op.BucketRecord) error { return nil }
