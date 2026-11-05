package op_test

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"

	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// lender is a memstore whose PrefetchObject lends: every state it returns
// carries a HeadRelease and a HeadForget that share one guard, as the
// driver's do for a buffer of its free list, and count the one that acts. A
// read notes how many releases had run when it began, which is what the op
// must still hold while the store reads the head.
type lender struct {
	*memstore.Store

	mu         sync.Mutex
	prefetched []*op.ObjectState
	released   atomic.Int32
	forgotten  atomic.Int32
	reads      atomic.Int32
	// releasedAtRead is the releases that had run when the last read began.
	releasedAtRead atomic.Int32
}

func newLender(s *memstore.Store) *lender { return &lender{Store: s} }

func (l *lender) PrefetchObject(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey) (*op.ObjectState, error) {
	st, err := l.Store.PrefetchObject(ctx, rec, key)
	if st != nil {
		var once sync.Once
		st.HeadRelease = func() { once.Do(func() { l.released.Add(1) }) }
		st.HeadForget = func() { once.Do(func() { l.forgotten.Add(1) }) }
		l.mu.Lock()
		l.prefetched = append(l.prefetched, st)
		l.mu.Unlock()
	}
	return st, err
}

func (l *lender) ReadObject(ctx context.Context, st *op.ObjectState, rng op.ByteRange, sink io.Writer) error {
	l.reads.Add(1)
	l.releasedAtRead.Store(l.released.Load())
	return l.Store.ReadObject(ctx, st, rng, sink)
}

// lent is the states PrefetchObject has returned.
func (l *lender) lent() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.prefetched)
}

var errClientGone = errors.New("the client's connection reset")

// failingSink fails every write, as a connection that dropped does.
type failingSink struct{ captureSink }

func (*failingSink) Write([]byte) (int, error) { return 0, errClientGone }

// sinkBug is what panicSink panics with.
const sinkBug = "a bug in the sink"

// panicSink panics on its first write, as a sink with a bug does.
type panicSink struct{ captureSink }

func (*panicSink) Write([]byte) (int, error) { panic(sinkBug) }
