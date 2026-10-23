package gateway

import (
	"context"

	"github.com/jhoblitt/rgw-go/hack/internal/harness"
)

// ExitStatus is the status the command exits with after a run under ctx
// returned err, as the shell reports one: 128 and the signal's number when a
// signal interrupted the run, 1 for any other failure, 0 for none.
func ExitStatus(ctx context.Context, err error) int {
	if err == nil {
		return 0
	}
	if status, ok := harness.InterruptStatus(ctx); ok {
		return status
	}
	return 1
}
