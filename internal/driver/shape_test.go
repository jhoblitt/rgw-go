package driver_test

import (
	"context"
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/radosclient/radosclientfakes"
	"github.com/jhoblitt/rgw-go/test/bench/seam"
)

// composition is what one RADOS write op carries, compared step type by step
// type: an xattr by its name, a class call by its class and method, a create
// by whether it is exclusive.
type composition struct {
	Steps []string
	Mtime bool
	Flags radosclient.OpFlags
}

func composed(w interface {
	Steps() []radosclient.Step
	Mtime() (time.Time, bool)
}, flags radosclient.OpFlags,
) composition {
	var kinds []string
	for _, st := range w.Steps() {
		switch x := st.(type) {
		case *radosclient.SetXattrStep:
			kinds = append(kinds, "SetXattr "+x.Name)
		case *radosclient.ExecStep:
			kinds = append(kinds, "Exec "+x.Class+"."+x.Method)
		case *radosclient.CreateStep:
			kinds = append(kinds, fmt.Sprintf("Create exclusive=%t", x.Exclusive))
		default:
			kinds = append(kinds, strings.TrimPrefix(fmt.Sprintf("%T", st), "*radosclient."))
		}
	}
	_, mtime := w.Mtime()
	return composition{Steps: kinds, Mtime: mtime, Flags: flags}
}

// shapeWrites runs one iteration of the seam benchmark's shape name at rel
// against a pool that answers every op with success, and returns the
// compositions of the writes the iteration issued, Prepare's left out.
func shapeWrites(ctx context.Context, name string, rel denc.Release) []composition {
	GinkgoHelper()
	shapes, err := seam.ByName([]string{name})
	Expect(err).NotTo(HaveOccurred())
	sh := shapes[0]
	pool := &radosclientfakes.FakePool{}
	Expect(sh.Prepare(ctx, pool, rel, 1)).To(Succeed())
	prepared := pool.WriteCallCount()
	Expect(sh.Run(ctx, pool, 0, 0)).To(Succeed())
	out := make([]composition, 0, pool.WriteCallCount()-prepared)
	for i := prepared; i < pool.WriteCallCount(); i++ {
		_, _, w, flags := pool.WriteArgsForCall(i)
		out = append(out, composed(w, flags))
	}
	Expect(out).To(HaveLen(sh.Ops()), "shape %s's writes against its Ops", name)
	return out
}

// The seam benchmark's headwrite4k and indexrtt shapes stand for the RADOS
// ops of a PUT within the head; their numbers say something about the
// gateway only while the driver sends those ops.
var _ = Describe("the seam benchmark's PUT shapes", func() {
	DescribeTable("compose what PutObject sends for a new 4 KiB object",
		func(ctx SpecContext, rel denc.Release) {
			mtime := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
			c := newPutCluster()
			c.SetClock(func() time.Time { return mtime })
			s := openPutStore(ctx, c, rel, nil, mtime)
			rec := testBucket(putBucketID, 11)
			seedShards(c, rec)
			seedRecordInstance(c, rec)
			shard := indexShardOID(rec, "k")
			headOID := putBucketID + "_k"
			c.ResetCounters()

			body := strings.Repeat("x", 4<<10)
			_, err := s.PutObject(ctx, rec, meta.ObjKey{Name: "k"}, strings.NewReader(body), op.PutParams{
				Size: int64(len(body)),
				Attrs: map[string][]byte{
					meta.AttrACL:         encode(acl.DefaultPolicy(alice, "Alice")),
					meta.AttrContentType: []byte("application/octet-stream\x00"),
				},
			})
			Expect(err).NotTo(HaveOccurred())
			settle(s)

			Expect(c.WriteOrder(testDataPool, "")).To(Equal([]string{headOID}), "the data pool's writes: the head alone")
			Expect(c.WriteOrder(rookIndexPool, "")).To(Equal([]string{shard, shard}), "the index pool's writes: the prepare and the complete")
			Expect(c.Reads(testDataPool, "", headOID)).To(BeZero(), "a new object's head is not read")
			heads := c.WritesTo(testDataPool, "", headOID)
			Expect(heads).To(HaveLen(1), "one head write")
			Expect([]composition{composed(heads[0], heads[0].Flags())}).To(Equal(shapeWrites(ctx, "headwrite4k", rel)),
				"the head write against headwrite4k")

			index := c.WritesTo(rookIndexPool, "", shard)
			got := make([]composition, 0, len(index))
			for _, w := range index {
				got = append(got, composed(w, w.Flags()))
			}
			Expect(got).To(Equal(shapeWrites(ctx, "indexrtt", rel)), "the index prepare and complete against indexrtt")
		},
		Entry("on Squid", denc.Squid),
		Entry("on Tentacle", denc.Tentacle),
	)
})
