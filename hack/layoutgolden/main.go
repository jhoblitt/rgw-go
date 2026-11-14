// Command layoutgolden turns a run's capture of ?layout answers into the
// goldens internal/driver's specs lay objects out against.
//
//	go run ./hack/layoutgolden --release squid|tentacle [--in FILE] [--out DIR]
//
// The capture is what the ?layout and otp gate prints, one line for each
// object, as test/integration/phase2_layout_otp_test.go says. It is read
// from --in, internal/driver/testdata/layout/<release>.jsonl by default, and
// the goldens are written under --out, internal/driver/testdata/layout by
// default, as <release>/<shape>.bin and <release>/<shape>.json. The package
// holds the rest of the contract.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/jhoblitt/rgw-go/hack/layoutgolden/internal/layoutgolden"
)

func main() {
	os.Exit(run())
}

func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := layoutgolden.Run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "layoutgolden:", err)
		return 1
	}
	return 0
}
