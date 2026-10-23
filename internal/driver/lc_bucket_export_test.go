package driver

import (
	"context"
	"time"

	rgwcls "github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/lifecycle"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

// LCStep is a point in a lifecycle action at which a harness's hook runs.
type LCStep = lcStep

// The points of an action.
const (
	LCStepDecided = lcStepDecided
	LCStepReread  = lcStepReread
	LCStepUpload  = lcStepUpload
	LCStepListed  = lcStepListed
)

// LCHarness runs the bucket pass Open gives a Store's lifecycle workers in
// worker 0, on a spec's clock, waits, notifier and hook.
type LCHarness struct {
	ws   *lcWorkers
	pass *lcPass
}

// LCHarnessForTest is a harness over s at now in UTC, the shards in order
// and its waits returning at once.
func (s *Store) LCHarnessForTest(now func() time.Time) *LCHarness {
	p := newLCPass(s)
	ws := newLCWorkers(s, p)
	ws.now, ws.loc, ws.perm = now, time.UTC, identityPerm
	ws.sleep = func(context.Context, time.Duration) error { return nil }
	return &LCHarness{ws: ws, pass: p}
}

// SetSleep makes sleep the workers' waits.
func (h *LCHarness) SetSleep(sleep func(ctx context.Context, d time.Duration) error) {
	h.ws.sleep = sleep
}

// SetNotifier makes n the pass's notifier.
func (h *LCHarness) SetNotifier(n lifecycle.Notifier) { h.pass.notifier = n }

// SetHook makes hook run at each point of each action.
func (h *LCHarness) SetHook(hook func(ctx context.Context, at LCStep, key meta.ObjKey)) {
	h.pass.hook = hook
}

// Pass runs worker 0's pass over every shard.
func (h *LCHarness) Pass(ctx context.Context) { h.ws.worker(0).process(ctx) }

// RunForTest runs the bucket pass on the bucket of entry key in worker 0,
// as after a claim, though none is made.
func (h *LCHarness) RunForTest(ctx context.Context, key string) error {
	w := h.ws.worker(0)
	return h.pass.run(ctx, w, lcClaim{entry: rgwcls.LCEntry{Bucket: key}}, w.stopAt())
}

// RadosgwShardPassForTest runs shard idx of s as radosgw's RGWLC::process
// runs it, unchanged, in worker ix at now, with bucket as its
// bucket_lc_process.
func (s *Store) RadosgwShardPassForTest(ctx context.Context, now func() time.Time, ix int, idx uint32, bucket func(key string) error) {
	ws := newLCWorkers(s, nil)
	ws.now, ws.loc = now, time.UTC
	ws.sleep = func(context.Context, time.Duration) error { return nil }
	radosgwShardPass(ctx, ws.worker(ix), idx, bucket)
}

// LCClientForTest is c as another RADOS client named name, whose cls_lock
// cookies carry its name.
func LCClientForTest(c *fakerados.Cluster, name string) radosclient.Cluster {
	return lcClient{Cluster: c, name: name}
}

// EnableCacheForTest turns the metadata cache of s on, as its control
// watches registering would once s runs.
func EnableCacheForTest(s *Store) { s.sysobj.cache.setEnabled(true) }

// LCStatesForTest is lcStates of a rule's first page for the external specs:
// the states of kept, page's entries at at, over a new Lister.
func LCStatesForTest(page, kept []op.ObjectEntry, at []int, truncated bool) []lifecycle.State {
	var l lifecycle.Lister
	_, states := lcStates(&l, page, kept, at, truncated)
	return states
}
