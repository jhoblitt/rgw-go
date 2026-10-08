package op_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/op"
)

var _ = Describe("LogHandler", func() {
	const id = "tx000000000000000000001-0068d7a1b2-4155-z"
	var (
		buf    *bytes.Buffer
		logger *slog.Logger
	)
	BeforeEach(func() {
		buf = &bytes.Buffer{}
		logger = slog.New(op.NewLogHandler(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	})
	records := func() []map[string]any {
		GinkgoHelper()
		var recs []map[string]any
		for line := range strings.SplitSeq(strings.TrimSpace(buf.String()), "\n") {
			var rec map[string]any
			Expect(json.Unmarshal([]byte(line), &rec)).To(Succeed(), line)
			recs = append(recs, rec)
		}
		return recs
	}

	It("adds the request id to every record logged under the request's context, at every level", func(ctx SpecContext) {
		rctx := op.WithRequestID(ctx, id)
		logger.DebugContext(rctx, "one")
		logger.InfoContext(rctx, "two", slog.String("k", "v"))
		logger.With(slog.String("op", "get_obj")).ErrorContext(rctx, "three")
		recs := records()
		Expect(recs).To(HaveLen(3))
		for _, rec := range recs {
			Expect(rec).To(HaveKeyWithValue("request_id", id), "record %v", rec["msg"])
		}
		Expect(recs[1]).To(HaveKeyWithValue("k", "v"))
		Expect(recs[2]).To(HaveKeyWithValue("op", "get_obj"))
		Expect(strings.Count(buf.String(), `"request_id"`)).To(Equal(3), "one request_id per record")
	})
	It("carries the id through a context derived from the request's", func(ctx SpecContext) {
		rctx, cancel := context.WithCancel(op.WithRequestID(ctx, id))
		defer cancel()
		logger.WarnContext(context.WithoutCancel(rctx), "detached")
		Expect(records()).To(ConsistOf(HaveKeyWithValue("request_id", id)))
	})
	It("adds nothing to a line logged outside a request, and does not fail it", func(ctx SpecContext) {
		h := op.NewLogHandler(slog.NewJSONHandler(buf, nil))
		Expect(h.Handle(ctx, slog.NewRecord(time.Now(), slog.LevelInfo, "outside", 0))).To(Succeed())
		slog.New(h).Info("no context")
		recs := records()
		Expect(recs).To(HaveLen(2))
		for _, rec := range recs {
			Expect(rec).NotTo(HaveKey("request_id"), "record %v", rec["msg"])
		}
	})
	It("leaves the caller's record free to grow after it adds the id", func(ctx SpecContext) {
		// Past five attributes a record keeps the rest in a slice its copies
		// share, so adding to a copy without cloning it writes into the
		// caller's spare capacity.
		r := slog.NewRecord(time.Now(), slog.LevelInfo, "shared", 0)
		for i := range 8 {
			r.AddAttrs(slog.Int("a"+strconv.Itoa(i), i))
		}
		Expect(op.NewLogHandler(slog.NewJSONHandler(buf, nil)).Handle(op.WithRequestID(ctx, id), r)).To(Succeed())
		r.AddAttrs(slog.String("later", "x"))
		var keys []string
		r.Attrs(func(a slog.Attr) bool {
			keys = append(keys, a.Key)
			return true
		})
		Expect(keys).To(Equal([]string{"a0", "a1", "a2", "a3", "a4", "a5", "a6", "a7", "later"}))
	})
	It("adds the id inside a group a logger opened", func(ctx SpecContext) {
		logger.WithGroup("g").InfoContext(op.WithRequestID(ctx, id), "grouped", slog.String("k", "v"))
		Expect(records()).To(ConsistOf(HaveKeyWithValue("g", SatisfyAll(
			HaveKeyWithValue("request_id", id),
			HaveKeyWithValue("k", "v"),
		))))
	})
	It("reads back the id a context carries, and none from another", func(ctx SpecContext) {
		Expect(op.RequestID(op.WithRequestID(ctx, id))).To(Equal(id))
		Expect(op.RequestID(ctx)).To(BeEmpty())
	})
})
