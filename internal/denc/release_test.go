package denc_test

import (
	"bytes"
	"log"
	"log/slog"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
)

var _ = DescribeTable("ParseRelease",
	func(name string, want denc.Release, wantOK bool) {
		got, ok := denc.ParseRelease(name)
		Expect(ok).To(Equal(wantOK), "name %q", name)
		Expect(got).To(Equal(want), "name %q", name)
	},
	Entry("squid", "squid", denc.Squid, true),
	Entry("tentacle", "tentacle", denc.Tentacle, true),
	Entry("newer than known maps to the newest known", "umbrella", denc.Tentacle, true),
	Entry("uppercase", "Squid", denc.Squid, true),
	Entry("older than the floor", "reef", denc.Squid, false),
	Entry("unknown", "banana", denc.Squid, false),
)

var _ = DescribeTable("Release.String",
	func(r denc.Release, want string) {
		Expect(r.String()).To(Equal(want))
	},
	Entry("squid", denc.Squid, "squid"),
	Entry("tentacle", denc.Tentacle, "tentacle"),
	Entry("a value no release has", denc.Release(7), "Release(7)"),
)

// captureLogs sends slog's default logger to a buffer until the spec ends.
// slog.SetDefault also points the log package's output at the new handler,
// and restoring the old default logger leaves it there, so the cleanup puts
// log's writer and flags back too.
func captureLogs() *bytes.Buffer {
	var buf bytes.Buffer
	oldLogger, oldWriter, oldFlags := slog.Default(), log.Writer(), log.Flags()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	DeferCleanup(func() {
		slog.SetDefault(oldLogger)
		log.SetOutput(oldWriter)
		log.SetFlags(oldFlags)
	})
	return &buf
}

var _ = Describe("ClusterRelease", func() {
	DescribeTable("maps a supported release to itself without a warning",
		func(ctx SpecContext, name string, want denc.Release) {
			logs := captureLogs()
			got, ok := denc.ClusterRelease(ctx, name)
			Expect([]any{got, ok}).To(Equal([]any{want, true}), "name %q", name)
			Expect(logs.String()).To(BeEmpty())
		},
		Entry("squid", "squid", denc.Squid),
		Entry("tentacle", "tentacle", denc.Tentacle),
		Entry("in capitals", "TENTACLE", denc.Tentacle),
	)

	DescribeTable("maps a newer release to the newest known one with a warning",
		func(ctx SpecContext, name string) {
			logs := captureLogs()
			got, ok := denc.ClusterRelease(ctx, name)
			Expect([]any{got, ok}).To(Equal([]any{denc.Tentacle, true}), "name %q", name)
			Expect(logs.String()).To(And(
				ContainSubstring(`"level":"WARN"`),
				ContainSubstring(`"require_osd_release":"`+name+`"`),
				ContainSubstring(`"encoding_for":"tentacle"`),
			))
		},
		Entry("a known newer name", "umbrella"),
		Entry("a name newer than every known one", "vampire"),
	)

	DescribeTable("refuses a release below the squid floor",
		func(ctx SpecContext, name string) {
			_, ok := denc.ClusterRelease(ctx, name)
			Expect(ok).To(BeFalse(), "name %q", name)
		},
		Entry("the release before squid", "reef"),
		Entry("an early release", "argonaut"),
		Entry("in capitals", "LUMINOUS"),
	)
})
