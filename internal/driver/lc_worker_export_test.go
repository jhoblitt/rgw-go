package driver

import (
	"context"
	"time"
)

// LCPassForTest runs one lifecycle pass over every shard with the bucket
// pass Open gives its workers, at now in loc, its waits returning at once.
func (s *Store) LCPassForTest(ctx context.Context, now time.Time, loc *time.Location) {
	ws := newLCWorkers(s, newLCPass(s))
	ws.now = func() time.Time { return now }
	ws.sleep = func(context.Context, time.Duration) error { return nil }
	ws.loc = loc
	ws.worker(0).process(ctx)
}
