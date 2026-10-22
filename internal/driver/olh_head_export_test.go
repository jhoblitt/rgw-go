package driver

import (
	"context"
	"errors"
	"maps"
	"slices"

	"github.com/jhoblitt/rgw-go/internal/cls/rgw"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// OLHIndexCall is one call a FakeOLHIndex took: the method's name and the
// arguments it was given. Writes counts the writes to the OLH head the
// spec's WritesBefore had seen when the call came, so a spec can tell
// whether a call came before or after a head op.
type OLHIndexCall struct {
	Method    string
	Head      *op.ObjectState
	Marker    uint64
	Ver       uint64
	Key       meta.ObjKey
	ExpectTag []byte
	Writes    int
}

// FakeOLHIndex is an olhIndex for the specs, which records each call and
// answers from its fields. olhIndex's methods are unexported, so no
// generated fake can implement it from another package.
type FakeOLHIndex struct {
	Calls []OLHIndexCall
	// WritesBefore, when set, counts the writes to the OLH head so far.
	WritesBefore func() int
	// During, when set, runs as each call is taken, before it answers, so a
	// spec can act as another writer between the replay's steps.
	During func(method string)

	// Log is the pending log readLog answers. A trimLog that succeeds drops
	// every epoch of it at or below its ver, as the class's trim does.
	Log      rgw.ReadOLHLogRet
	LogErr   error
	TrimErr  error
	ClearErr error
	// RemoveErr answers removeInstance by the instance it names.
	RemoveErr map[string]error
	OLH       rgw.OLHEntry
	OLHErr    error
}

var _ olhIndex = (*FakeOLHIndex)(nil)

// ErrRemovalRefused is errRemovalRefused for the external specs.
var ErrRemovalRefused = errRemovalRefused

func (f *FakeOLHIndex) record(c OLHIndexCall) {
	if f.WritesBefore != nil {
		c.Writes = f.WritesBefore()
	}
	f.Calls = append(f.Calls, c)
	if f.During != nil {
		f.During(c.Method)
	}
}

// Methods is the name of each call, in call order.
func (f *FakeOLHIndex) Methods() []string {
	out := make([]string, 0, len(f.Calls))
	for _, c := range f.Calls {
		out = append(out, c.Method)
	}
	return out
}

func (f *FakeOLHIndex) readLog(_ context.Context, head *op.ObjectState, marker uint64) (rgw.ReadOLHLogRet, error) {
	f.record(OLHIndexCall{Method: "readLog", Head: head, Marker: marker})
	return f.Log, f.LogErr
}

func (f *FakeOLHIndex) trimLog(_ context.Context, head *op.ObjectState, ver uint64) error {
	f.record(OLHIndexCall{Method: "trimLog", Head: head, Ver: ver})
	if f.TrimErr != nil {
		return f.TrimErr
	}
	maps.DeleteFunc(f.Log.Log, func(epoch uint64, _ []rgw.OLHLogEntry) bool { return epoch <= ver })
	return nil
}

func (f *FakeOLHIndex) clear(_ context.Context, head *op.ObjectState) error {
	f.record(OLHIndexCall{Method: "clear", Head: head})
	return f.ClearErr
}

func (f *FakeOLHIndex) removeInstance(_ context.Context, key meta.ObjKey, expectTag []byte) error {
	f.record(OLHIndexCall{Method: "removeInstance", Key: key, ExpectTag: slices.Clone(expectTag)})
	return f.RemoveErr[key.Instance]
}

func (f *FakeOLHIndex) getOLH(_ context.Context, head *op.ObjectState) (rgw.OLHEntry, error) {
	f.record(OLHIndexCall{Method: "getOLH", Head: head})
	return f.OLH, f.OLHErr
}

// OLHCondGuard is olhCondGuard with its fields exported for the specs.
type OLHCondGuard struct {
	Info      []byte
	NoPending bool
}

// OLHInitModificationForTest is olhInitModification with no expected write
// tag for the external specs.
func (s *Store) OLHInitModificationForTest(ctx context.Context, head *op.ObjectState, cond *OLHCondGuard) (opTag string, ver []byte, err error) {
	return s.OLHInitModificationExpectingForTest(ctx, head, cond, nil)
}

// OLHInitModificationExpectingForTest is olhInitModification for the
// external specs.
func (s *Store) OLHInitModificationExpectingForTest(ctx context.Context, head *op.ObjectState, cond *OLHCondGuard, expectTag []byte) (opTag string, ver []byte, err error) {
	var g *olhCondGuard
	if cond != nil {
		g = &olhCondGuard{info: cond.Info, noPending: cond.NoPending}
	}
	return s.olhInitModification(ctx, head, g, expectTag)
}

// OLHCancelModificationForTest is olhCancelModification for the external
// specs.
func (s *Store) OLHCancelModificationForTest(ctx context.Context, head *op.ObjectState, opTag string) {
	s.olhCancelModification(ctx, head, opTag)
}

// CheckPendingOLHEntriesForTest is checkPendingOLHEntries for the external
// specs.
func (s *Store) CheckPendingOLHEntriesForTest(ctx context.Context, head *op.ObjectState) (expired, pending []string) {
	return s.checkPendingOLHEntries(ctx, head)
}

// RemoveOLHPendingEntriesForTest is removeOLHPendingEntries for the external
// specs.
func (s *Store) RemoveOLHPendingEntriesForTest(ctx context.Context, head *op.ObjectState, names []string) error {
	return s.removeOLHPendingEntries(ctx, head, names)
}

// FollowOLHForTest is followOLH for the external specs.
func (s *Store) FollowOLHForTest(ctx context.Context, head *op.ObjectState, update func(context.Context, *op.ObjectState) error) (meta.ObjKey, bool, error) {
	return s.followOLH(ctx, head, update)
}

// ApplyOLHLogForTest is applyOLHLog over idx for the external specs.
func (s *Store) ApplyOLHLogForTest(ctx context.Context, idx *FakeOLHIndex, head *op.ObjectState, log map[uint64][]rgw.OLHLogEntry, opTag string, expectTag []byte) (uint64, error) {
	return s.applyOLHLog(ctx, idx, head, log, opTag, expectTag)
}

// OLHTail is olhLogTail with its fields exported for the specs.
type OLHTail struct {
	Last  uint64
	Clear bool
	Keep  bool
	OpTag string
}

// UnfinishedOLHTail is the tail an unfinished replay's error names, and
// whether it names one.
func UnfinishedOLHTail(err error) (OLHTail, bool) {
	var te *olhTailError
	if !errors.As(err, &te) {
		return OLHTail{}, false
	}
	return OLHTail{Last: te.tail.last, Clear: te.tail.clear, Keep: te.tail.keep, OpTag: te.tail.opTag}, true
}

// ErrRefusalUnsettled is errRefusalUnsettled for the external specs.
var ErrRefusalUnsettled = errRefusalUnsettled

// FinishOLHLogForTest is finishOLHLog over idx for the external specs.
func (s *Store) FinishOLHLogForTest(ctx context.Context, idx *FakeOLHIndex, head *op.ObjectState, t OLHTail) error {
	return s.finishOLHLog(ctx, idx, head, olhLogTail{last: t.Last, clear: t.Clear, keep: t.Keep, opTag: t.OpTag})
}

// ClearOLHHeadForTest is clearOLHHead over idx for the external specs.
func (s *Store) ClearOLHHeadForTest(ctx context.Context, idx *FakeOLHIndex, head *op.ObjectState, ver uint64) error {
	return s.clearOLHHead(ctx, idx, head, ver)
}

// RepairOLHForTest is repairOLH over idx for the external specs.
func (s *Store) RepairOLHForTest(ctx context.Context, idx *FakeOLHIndex, head *op.ObjectState) error {
	return s.repairOLH(ctx, idx, head)
}
