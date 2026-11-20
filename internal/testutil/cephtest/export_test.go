package cephtest

import (
	"context"
	"time"

	"github.com/onsi/gomega"
)

// AwaitWithin is Await under the bounds given, so that the specs can run it
// in milliseconds.
func (d ServiceDeparture) AwaitWithin(ctx context.Context, read func(gomega.Gomega) *ServiceRegistration,
	closeBound, againBound, settle, poll time.Duration,
) *ServiceRegistration {
	return d.await(ctx, read, departureBounds{close: closeBound, again: againBound, settle: settle, poll: poll})
}
