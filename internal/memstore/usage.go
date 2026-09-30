package memstore

import (
	"context"

	"github.com/jhoblitt/rgw-go/internal/op"
)

// Log implements op.UsageLogger by appending e.
func (s *Store) Log(_ context.Context, e op.UsageEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.usage = append(s.usage, cloneUsageEntry(e))
}

// Usage returns a copy of every logged usage entry.
func (s *Store) Usage() []op.UsageEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]op.UsageEntry, len(s.usage))
	for i := range s.usage {
		out[i] = cloneUsageEntry(s.usage[i])
	}
	return out
}

func cloneUsageEntry(e op.UsageEntry) op.UsageEntry {
	e.Owner = cloneOwner(e.Owner)
	e.Payer = cloneOwner(e.Payer)
	return e
}
