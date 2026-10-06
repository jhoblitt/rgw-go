package driver_test

import (
	"bytes"
	"context"
	"net/http"
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/driver"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
	"github.com/jhoblitt/rgw-go/internal/radosclient/radosclientfakes"
)

// bodySink collects the body a GetObject sends.
type bodySink struct{ body bytes.Buffer }

func (*bodySink) WriteHeader(int, http.Header)  {}
func (s *bodySink) Write(p []byte) (int, error) { return s.body.Write(p) }
func (*bodySink) Flush() error                  { return nil }

var _ = Describe("GetObject of one part over the driver", func() {
	It("reads the part head once, its first stripe with its stat, as radosgw's prefetch does", func(ctx SpecContext) {
		const (
			marker = "m1"
			prefix = "multipart.bin.2~vXcCS0UgSATwQOecOwRkZrWyEyux7jO"
		)
		bucket := meta.BucketID{Name: "plain", Marker: marker, ID: marker}
		m := decodeGolden("squid-multipart", bucket)
		head, partHead, partTail := marker+"_multipart.bin", marker+"__multipart_"+prefix+".2", marker+"__shadow_"+prefix+".2_1"

		var (
			mu    sync.Mutex
			reads []string
		)
		pool := &radosclientfakes.FakePool{}
		pool.WithLocatorReturns(pool)
		pool.ReadStub = func(_ context.Context, oid string, rop *radosclient.ReadOp, _ radosclient.OpFlags) (uint64, error) {
			mu.Lock()
			reads = append(reads, oid)
			mu.Unlock()
			switch oid {
			case head:
				return fillHead(rop, nil, map[string][]byte{meta.AttrETag: []byte("mp-3"), meta.AttrManifest: encode(m)}), nil
			case partHead:
				return fillHead(rop, make([]byte, 4<<20), map[string][]byte{meta.AttrETag: []byte("part")}), nil
			}
			for _, step := range rop.Steps() {
				if st, ok := step.(*radosclient.ReadStep); ok {
					fillReadStep(st, make([]byte, st.Offset+st.Length))
				}
			}
			return 1, nil
		}
		s := driver.NewStoreForTest(fakeClusterWith(pool), testZone(), testReadConfig())

		rec := &op.BucketRecord{Info: meta.BucketInfo{Bucket: bucket, PlacementRule: meta.ParsePlacementRule("default-placement")}}
		buckets := &opfakes.FakeBucketStore{}
		buckets.GetBucketReturns(rec, nil)
		zone := &opfakes.FakeZoneInfo{}
		zone.ReleaseReturns(denc.Squid)
		env := &op.Env{Zone: zone, Buckets: buckets, Objects: s, Authz: &opfakes.FakeAuthorizer{}, Metrics: op.NopMetrics{}}
		sink := &bodySink{}
		part := 2
		o := &op.GetObject{GetData: true, PartNumber: &part, Sink: sink}
		r := &op.Request{
			Method: http.MethodGet, Bucket: "plain", Object: meta.ObjKey{Name: "multipart.bin"},
			Env: env, Header: http.Header{}, Identity: op.Identity{OpMask: op.OpTypeAll},
		}
		Expect(op.Run(ctx, o, r)).To(Succeed())

		Expect(o.Status).To(Equal(http.StatusOK))
		Expect(sink.body.Len()).To(Equal(8<<20), "part 2 is 8 MiB")
		Expect(*o.PartsCount).To(Equal(3))
		Expect(reads).To(ConsistOf(head, partHead, partTail),
			"the multipart head's stat, the part head's stat with its first 4 MiB, and the part's second stripe")
	})
})
