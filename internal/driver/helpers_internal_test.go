package driver

import (
	"io"
	"log"
	"log/slog"
)

// captureLog sends slog's default logger to w as JSON until restore runs.
// slog.SetDefault also points the log package's output at the new handler,
// and restoring the old default logger leaves it there, so restore puts log's
// writer and flags back too.
func captureLog(w io.Writer) (restore func()) {
	oldLogger, oldWriter, oldFlags := slog.Default(), log.Writer(), log.Flags()
	slog.SetDefault(slog.New(slog.NewJSONHandler(w, nil)))
	return func() {
		slog.SetDefault(oldLogger)
		log.SetOutput(oldWriter)
		log.SetFlags(oldFlags)
	}
}
