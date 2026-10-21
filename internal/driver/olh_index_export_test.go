package driver

import (
	"context"
	"time"

	"github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// olhIndexCheck holds bucketOLHIndex's methods to olhIndex's signatures. A
// method bucketOLHIndex lacks comes from noRemoval, two embeddings down, so a
// method of bucketOLHIndex's own, one down, takes its place: a
// removeInstance added to bucketOLHIndex is checked with the other four, and
// the check needs no change. The fallback is named in the checks below, so
// that once the stub is shadowed the unused linter still sees it used.
type olhIndexCheck struct {
	*bucketOLHIndex
	olhIndexFallback
}

// olhIndexFallback holds noRemoval one embedding below olhIndexCheck's
// fields.
type olhIndexFallback struct{ noRemoval }

// noRemoval stands in for the removeInstance bucketOLHIndex does not have.
type noRemoval struct{}

func (noRemoval) removeInstance(context.Context, meta.ObjKey, []byte) error { return nil }

var (
	_ olhIndex = olhIndexCheck{olhIndexFallback: olhIndexFallback{}}
	_          = olhIndexFallback.removeInstance
)

// OLHIndex is bucketOLHIndex for the external specs.
type OLHIndex = bucketOLHIndex

// NewOLHIndexForTest is newOLHIndex for the external specs.
func (s *Store) NewOLHIndexForTest(rec *op.BucketRecord, key meta.ObjKey) *OLHIndex {
	return s.newOLHIndex(rec, key)
}

// OLHLinkFields is olhLinkFields with its fields exported for the specs.
type OLHLinkFields struct {
	DeleteMarker  bool
	Meta          rgw.DirEntryMeta
	Epoch         uint64
	UnmodSince    time.Time
	HighPrecision bool
}

// LinkOLH is linkOLH for the external specs.
func (b *OLHIndex) LinkOLH(ctx context.Context, head *op.ObjectState, key meta.ObjKey, opTag string, f OLHLinkFields) error {
	return b.linkOLH(ctx, head, key, opTag, olhLinkFields{
		deleteMarker: f.DeleteMarker, meta: f.Meta, epoch: f.Epoch, unmodSince: f.UnmodSince, highPrecision: f.HighPrecision,
	})
}

// UnlinkInstance is unlinkInstance for the external specs.
func (b *OLHIndex) UnlinkInstance(ctx context.Context, head *op.ObjectState, key meta.ObjKey, opTag string, epoch uint64, nullVersion bool) error {
	return b.unlinkInstance(ctx, head, key, opTag, olhUnlinkFields{epoch: epoch, nullVersion: nullVersion})
}

// ReadLog is readLog for the external specs.
func (b *OLHIndex) ReadLog(ctx context.Context, head *op.ObjectState, marker uint64) (rgw.ReadOLHLogRet, error) {
	return b.readLog(ctx, head, marker)
}

// TrimLog is trimLog for the external specs.
func (b *OLHIndex) TrimLog(ctx context.Context, head *op.ObjectState, ver uint64) error {
	return b.trimLog(ctx, head, ver)
}

// Clear is clear for the external specs.
func (b *OLHIndex) Clear(ctx context.Context, head *op.ObjectState) error { return b.clear(ctx, head) }

// GetOLH is getOLH for the external specs.
func (b *OLHIndex) GetOLH(ctx context.Context, head *op.ObjectState) (rgw.OLHEntry, error) {
	return b.getOLH(ctx, head)
}

// GetInstanceEntryForTest is getInstanceEntry for the external specs.
func (s *Store) GetInstanceEntryForTest(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey) (rgw.DirEntry, error) {
	return s.getInstanceEntry(ctx, rec, key)
}

// CompleteSync is completeSync for the external specs.
func (x *IndexOp) CompleteSync(ctx context.Context, ver rgw.EntryVer, m rgw.DirEntryMeta) error {
	return x.completeSync(ctx, ver, m)
}

// SetBILogFlagsForTest sets the RGWBILogFlags x's requests carry.
func (x *IndexOp) SetBILogFlagsForTest(flags uint16) { x.bilogFlags = flags }
