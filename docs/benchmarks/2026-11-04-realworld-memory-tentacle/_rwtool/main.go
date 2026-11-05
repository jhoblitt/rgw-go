// Command realworld-spike is the real-world memory spike's tooling, never
// committed: a once-a-second memory sampler for one gateway process and its
// cgroup, a mixed S3 load whose request sequence is fixed by a seed, and the
// analysis of the samples a campaign leaves.
//
//	realworld-spike sample -pid PID -cgroup DIR -out FILE [-metrics HOST:PORT]
//	realworld-spike mix -op prep|run -endpoint URL -bucket B [-requests N] [-threads 32] [-out FILE]
//	realworld-spike analyze -runs DIR -out FILE
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:])
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: realworld-spike sample|mix|analyze [flags]")
		return 2
	}
	var err error
	switch args[0] {
	case "sample":
		err = sampleCmd(ctx, args[1:])
	case "mix":
		err = mixCmd(ctx, args[1:])
	case "analyze":
		err = analyzeCmd(args[1:])
	case "verify":
		err = verifyCmd(ctx, args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", args[0])
		return 2
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "realworld-spike:", err)
		return 1
	}
	return 0
}
