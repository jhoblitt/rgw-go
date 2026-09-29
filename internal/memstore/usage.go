package memstore

import (
	"context"

	"github.com/jhoblitt/rgw-go/internal/op"
)

// Log implements op.UsageLogger by appending e.
func (s *Store) Log(_ context.Context, e op.UsageEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e.Owner = cloneOwner(e.Owner)
	s.usage = append(s.usage, e)
}

// Usage returns a copy of every logged usage entry.
func (s *Store) Usage() []op.UsageEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]op.UsageEntry, len(s.usage))
	for i, e := range s.usage {
		e.Owner = cloneOwner(e.Owner)
		out[i] = e
	}
	return out
}
