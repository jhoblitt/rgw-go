package memstore_test

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"

	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/conformance"
)

// usageLog is memstore's conformance.UsageLog: what Usage shows is what
// was logged, so Logged needs no flush. It folds the entries of user and
// bucket into one record per epoch as the usage log merges them.
type usageLog struct{ *memstore.Store }

func (u usageLog) Logged(_ context.Context, user, bucket string) ([]op.UsageRecord, error) {
	byEpoch := map[uint64]int{}
	var out []op.UsageRecord
	for _, e := range u.Usage() {
		filed := e.Owner
		if e.Payer.String() != "" {
			filed = e.Payer
		}
		if filed.String() != user || e.Bucket != bucket {
			continue
		}
		epoch := uint64(e.Time.Truncate(time.Hour).Unix()) //nolint:gosec // a request's hour, after 1970
		i, ok := byEpoch[epoch]
		if !ok {
			i = len(out)
			byEpoch[epoch] = i
			out = append(out, op.UsageRecord{
				User: user, Owner: e.Owner.String(), Payer: e.Payer.String(), Bucket: bucket, Epoch: epoch,
				Categories: map[string]op.UsageData{},
			})
		}
		d := out[i].Categories[e.Category]
		d.BytesSent += e.BytesSent
		d.BytesReceived += e.BytesReceived
		d.Ops += e.Ops
		d.SuccessfulOps += e.SuccessfulOps
		out[i].Categories[e.Category] = d
	}
	return out, nil
}

var _ = Describe("memstore", func() {
	conformance.Run(func(context.Context) (*op.Env, func()) {
		s := memstore.New(memstore.Config{})
		env := &op.Env{
			Zone: s, Users: s, Accounts: s, UsageReader: s, BucketAdmin: s, Realms: s, Buckets: s,
			Objects: s, Multipart: s, Stats: s, Usage: usageLog{s}, Metadata: s,
		}
		return env, func() {}
	})
})
