// Command rgw-go: Ceph RADOS Gateway reimplemented in Go
//
// Managed by go-conventions (references/layout.md owns the main shape).
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/jhoblitt/rgw-go/internal/cli"
)

func main() {
	os.Exit(run())
}

func run() int {
	ctx, stop := cli.NotifyContext(context.Background())
	defer stop()
	// Not os.Args[1:]: installed as radosgw, the whole command line is ceph's.
	if err := cli.Run(ctx, cli.Argv(os.Args), os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "rgw-go:", err)
		return 1
	}
	return 0
}
