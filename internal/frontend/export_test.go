package frontend

import "time"

// SetDrainTimeout shortens s's drain for the specs that let a drain expire.
func SetDrainTimeout(s *Server, d time.Duration) { s.drain = d }

// CloseListener closes s's i-th bound listener, so the spec's Serve fails on it.
func CloseListener(s *Server, i int) error { return s.listeners[i].Close() }
