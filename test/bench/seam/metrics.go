package seam

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"syscall"
	"time"
)

// OSThreads reads this process's thread count from /proc/self/status.
func OSThreads() (int, error) {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0, err
	}
	for line := range bytes.Lines(b) {
		if v, ok := bytes.CutPrefix(line, []byte("Threads:")); ok {
			n, err := strconv.Atoi(string(bytes.TrimSpace(v)))
			if err != nil {
				return 0, fmt.Errorf("parsing the Threads line of /proc/self/status: %w", err)
			}
			return n, nil
		}
	}
	return 0, errors.New("/proc/self/status has no Threads line")
}

// CPUTime is the process's user plus system CPU time from getrusage.
func CPUTime() (time.Duration, error) {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0, fmt.Errorf("getrusage: %w", err)
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano()), nil
}

// Percentiles are latency quantiles of one cell.
type Percentiles struct{ P50, P99, P999, Mean time.Duration }

// Summarize sorts a copy of d and returns its percentiles; it panics on an empty slice.
func Summarize(d []time.Duration) Percentiles {
	if len(d) == 0 {
		panic("seam: Summarize of no durations")
	}
	s := slices.Clone(d)
	slices.Sort(s)
	var sum time.Duration
	for _, v := range s {
		sum += v
	}
	return Percentiles{
		P50:  rank(s, 500),
		P99:  rank(s, 990),
		P999: rank(s, 999),
		Mean: sum / time.Duration(len(s)),
	}
}

// rank returns the element of sorted s at n*perMille/1000, clamped to the last.
func rank(s []time.Duration, perMille int) time.Duration {
	return s[min(len(s)*perMille/1000, len(s)-1)]
}

// Sampler polls OSThreads on an interval and keeps the peak. It also counts
// once when it starts and once when it stops, so the peak covers both ends
// of the window whatever the interval.
type Sampler struct {
	count      func() (int, error)
	stop, done chan struct{}
	// peak is written by NewSampler, then by the polling goroutine, then by
	// Stop once that goroutine has returned, so no two writers overlap.
	peak int
}

// NewSampler counts the threads now and then every interval until Stop.
func NewSampler(interval time.Duration) *Sampler { return newSampler(interval, OSThreads) }

func newSampler(interval time.Duration, count func() (int, error)) *Sampler {
	s := &Sampler{count: count, stop: make(chan struct{}), done: make(chan struct{})}
	s.sample()
	go s.poll(interval)
	return s
}

// poll samples on every tick until Stop closes s.stop.
func (s *Sampler) poll(interval time.Duration) {
	defer close(s.done)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
			s.sample()
		}
	}
}

// sample raises the peak to the current count. A count that cannot be read
// is skipped: the peak is a maximum, and the next sample stands for it.
func (s *Sampler) sample() {
	if n, err := s.count(); err == nil {
		s.peak = max(s.peak, n)
	}
}

// Stop ends the polling, counts once more, and returns the highest count seen.
func (s *Sampler) Stop() (peak int) {
	close(s.stop)
	<-s.done
	s.sample()
	return s.peak
}
