package op

import (
	"context"
	"log/slog"
)

// requestIDKey is the context key WithRequestID stores a request's
// transaction id under.
type requestIDKey struct{}

// WithRequestID returns ctx carrying id, the request's transaction id, which
// LogHandler adds to every record logged under the returned context or one
// derived from it.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// RequestID returns the transaction id ctx carries, or "" for a context
// outside a request.
func RequestID(ctx context.Context) string {
	if id, ok := ctx.Value(requestIDKey{}).(string); ok {
		return id
	}
	return ""
}

// LogHandler adds the attribute request_id to every record logged under a
// context WithRequestID returned and passes every other record through
// unchanged, so a line logged outside a request carries no request id. Under
// a WithGroup logger the attribute lands in the group, as every record
// attribute does.
type LogHandler struct {
	next slog.Handler
}

// NewLogHandler wraps next.
func NewLogHandler(next slog.Handler) *LogHandler { return &LogHandler{next: next} }

// Enabled reports next's answer.
func (h *LogHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

// Handle hands next a clone of r with the request id added: copies of a
// record share their attributes, so r itself is left as it came.
func (h *LogHandler) Handle(ctx context.Context, r slog.Record) error {
	if id := RequestID(ctx); id != "" {
		r = r.Clone()
		r.AddAttrs(slog.String("request_id", id))
	}
	return h.next.Handle(ctx, r)
}

// WithAttrs wraps next's handler with attrs.
func (h *LogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &LogHandler{next: h.next.WithAttrs(attrs)}
}

// WithGroup wraps next's handler with the group.
func (h *LogHandler) WithGroup(name string) slog.Handler {
	return &LogHandler{next: h.next.WithGroup(name)}
}
