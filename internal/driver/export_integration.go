//go:build integration

package driver

import (
	"context"
	"sync"
)

// GCProcessForTest runs one pass of the GC worker over every shard, as
// RGWGC::process(expired_only) does, for test/integration's specs, which
// hand rgw-go's collector the garbage radosgw leaves; expiredOnly false frees
// entries not yet due, as radosgw-admin gc process --include-all does.
func (s *Store) GCProcessForTest(ctx context.Context, expiredOnly bool) error {
	return newGCWorker(s).process(ctx, expiredOnly)
}

// stoppedAfterLink holds the stores StopAfterOLHLinkForTest stopped.
var stoppedAfterLink sync.Map

func init() {
	olhStopAfterLink = func(s *Store) bool {
		_, ok := stoppedAfterLink.Load(s)
		return ok
	}
}

// StopAfterOLHLinkForTest makes s's versioned writes and deletes, while stop
// holds, answer success once their link or unlink has landed, before their
// replay, leaving the name as a gateway that dies there leaves it, for
// test/integration's specs of a writer interrupted between the two: the
// pending attr and the log entry wait for the next reader's replay, which
// radosgw's reads run too.
func (s *Store) StopAfterOLHLinkForTest(stop bool) {
	if stop {
		stoppedAfterLink.Store(s, struct{}{})
		return
	}
	stoppedAfterLink.Delete(s)
}
