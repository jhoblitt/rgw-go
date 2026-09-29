package driver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"golang.org/x/sync/errgroup"
)

// worker is one background task Run starts.
type worker struct {
	name string
	fn   func(ctx context.Context) error
}

// AddWorker registers a worker Run starts; it must return when ctx ends.
// Registering a worker once Run has started is a wiring bug, since the
// worker would never run, so AddWorker panics naming it.
func (s *Store) AddWorker(name string, fn func(ctx context.Context) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		panic(fmt.Sprintf("driver: worker %q added after Run started", name))
	}
	s.workers = append(s.workers, worker{name: name, fn: fn})
}

// Run runs the driver's background workers under one errgroup until ctx
// ends; each subsystem registers its worker with AddWorker. It returns the
// first worker error, or nil on ctx cancellation. A worker that returns
// ctx's own error once ctx has ended has stopped cleanly, not failed.
func (s *Store) Run(ctx context.Context) error {
	s.mu.Lock()
	s.started = true
	workers := s.workers
	s.mu.Unlock()

	g, gctx := errgroup.WithContext(ctx)
	for _, w := range workers {
		g.Go(func() error {
			slog.InfoContext(gctx, "worker started", slog.String("worker", w.name))
			err := w.fn(gctx)
			// errgroup keeps only the first error, so a clean stop records
			// none: it would hide a failure another worker returns after it.
			if err == nil || (ctx.Err() != nil && errors.Is(err, ctx.Err())) {
				return nil
			}
			return fmt.Errorf("worker %s: %w", w.name, err)
		})
	}
	<-gctx.Done()
	return g.Wait()
}
