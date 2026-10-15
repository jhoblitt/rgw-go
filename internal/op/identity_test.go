package op_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/op"
)

// routeReads is what each route may take from the query: its x-amz-meta-*
// and its x-amz-server-side-encryption*.
var routeReads = []struct {
	name             string
	route            op.AuthRoute
	meta, encryption bool
}{
	{"a result that names no route, which fails closed", op.RouteUnknown, false, false},
	{"an anonymous request, which may send the same names as headers", op.RouteAnonymous, true, true},
	{"SigV2's header route, which signs no query parameter but the sub-resources", op.RouteSigV2Header, false, false},
	{"SigV2's presigned route, which signs the query's meta and no encryption setting", op.RouteSigV2Query, true, false},
	{"SigV4 in the header, whose canonical query names every parameter", op.RouteSigV4Header, true, true},
	{"presigned SigV4", op.RouteSigV4Query, true, true},
}

var _ = Describe("AuthRoute", func() {
	It("reads neither the query's meta nor its encryption on the zero value", func() {
		var rt op.AuthRoute
		Expect(rt.ReadsQueryMeta()).To(BeFalse(), "x-amz-meta-*")
		Expect(rt.ReadsQueryEncryption()).To(BeFalse(), "x-amz-server-side-encryption*")
	})

	It("has an entry in the route table for every route", func() {
		listed := map[op.AuthRoute]bool{}
		for _, r := range routeReads {
			listed[r.route] = true
		}
		for rt := range op.NumAuthRoutesForTest {
			Expect(listed).To(HaveKey(rt), "route %d", rt)
		}
		Expect(listed).To(HaveLen(int(op.NumAuthRoutesForTest)))
	})

	It("lets a read take from the query only what the route's signature covers, or an anonymous request sends", func() {
		for _, r := range routeReads {
			Expect(r.route.ReadsQueryMeta()).To(Equal(r.meta), "x-amz-meta-* on %s", r.name)
			Expect(r.route.ReadsQueryEncryption()).To(Equal(r.encryption), "x-amz-server-side-encryption* on %s", r.name)
		}
	})
})
