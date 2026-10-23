package driver

import (
	"context"
	"time"

	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// OLHLink is olhLink for the external specs.
type OLHLink = olhLink

// OLHPass is olhPass for the external specs.
type OLHPass = olhPass

// OLHUnlink is olhUnlink for the external specs.
type OLHUnlink = olhUnlink

// SetOLHForTest is setOLH for the external specs.
func (s *Store) SetOLHForTest(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, p OLHLink) error {
	return s.setOLH(ctx, rec, key, p)
}

// UnlinkObjInstanceForTest is unlinkObjInstance for the external specs.
func (s *Store) UnlinkObjInstanceForTest(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, p OLHUnlink) error {
	return s.unlinkObjInstance(ctx, rec, key, p)
}

// UpdateOLHForTest is updateOLH for the external specs.
func (s *Store) UpdateOLHForTest(ctx context.Context, rec *op.BucketRecord, head *op.ObjectState) error {
	return s.updateOLH(ctx, rec, head)
}

// ReplayOLHForTest is replayOLH for a writer of opTag whose own removals
// must find expectTag, for the external specs.
func (s *Store) ReplayOLHForTest(ctx context.Context, rec *op.BucketRecord, head *op.ObjectState, opTag string, expectTag []byte) (refused, err error) {
	return s.replayOLH(ctx, s.newOLHIndex(rec, head.Key), head, &olhReplay{opTag: opTag, expectTag: expectTag})
}

// ReadOLHHeadForTest is readOLHHead for the external specs.
func (s *Store) ReadOLHHeadForTest(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey) (*op.ObjectState, error) {
	return s.readOLHHead(ctx, rec, key)
}

// RemoveInstance is removeInstance for the external specs.
func (b *OLHIndex) RemoveInstance(ctx context.Context, key meta.ObjKey, expectTag []byte) error {
	return b.removeInstance(ctx, key, expectTag)
}

// RemoveFailedVersionForTest is removeFailedVersion for the external specs.
func (s *Store) RemoveFailedVersionForTest(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey, cause error) {
	s.removeFailedVersion(ctx, rec, key, cause)
}

// NewOLHTailErrorForTest is an olhTailError naming t and wrapping err, for
// the external specs.
func NewOLHTailErrorForTest(t OLHTail, err error) error {
	return &olhTailError{tail: olhLogTail{last: t.Last, clear: t.Clear, keep: t.Keep, keepHead: t.KeepHead, opTag: t.OpTag}, err: err}
}

// SplitOLHTailForTest is splitOLHTail for the external specs: the tail err
// names, whether it names one, and the rest.
func SplitOLHTailForTest(err error) (OLHTail, bool, error) {
	tail, rest := splitOLHTail(err)
	if tail == nil {
		return OLHTail{}, false, rest
	}
	return OLHTail{Last: tail.tail.last, Clear: tail.tail.clear, Keep: tail.tail.keep, KeepHead: tail.tail.keepHead, OpTag: tail.tail.opTag}, true, rest
}

// OLHAttempts, OLHReplayRetries, OLHCondBackoffs and OLHCondListPage are
// olhAttempts, olhReplayRetries, olhCondBackoffs and olhCondListPage for the
// external specs.
const (
	OLHAttempts      = olhAttempts
	OLHReplayRetries = olhReplayRetries
	OLHCondBackoffs  = olhCondBackoffs
	OLHCondListPage  = olhCondListPage
)

// OLHCondBackoffForTest is olhCondBackoff for the external specs.
func OLHCondBackoffForTest(n int) time.Duration { return olhCondBackoff(n) }

// OLHCondStaleForTest is olhCondStale for the external specs.
func OLHCondStaleForTest() time.Duration { return olhCondStale() }

// SetCondSleepForTest makes sleep how s's conditional links wait out a
// backoff.
func SetCondSleepForTest(s *Store, sleep func(ctx context.Context, d time.Duration) error) {
	s.condSleep = sleep
}
