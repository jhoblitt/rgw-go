//go:build integration

package driver

import "context"

// GCProcessForTest runs one pass of the GC worker over every shard, as
// RGWGC::process(expired_only) does, for test/integration's specs, which
// hand rgw-go's collector the garbage radosgw leaves; expiredOnly false frees
// entries not yet due, as radosgw-admin gc process --include-all does.
func (s *Store) GCProcessForTest(ctx context.Context, expiredOnly bool) error {
	return newGCWorker(s).process(ctx, expiredOnly)
}
