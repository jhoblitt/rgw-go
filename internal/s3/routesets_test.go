package s3_test

import (
	"context"
	"net/http"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/s3"
)

// alwaysBoundRoutes is every route NewHandler must always bind, as
// scope/name, whichever unit map or feature set binds it.
var alwaysBoundRoutes = []string{
	"service/list_buckets",

	"bucket/list_bucket", "bucket/list_bucket_v2", "bucket/create_bucket", "bucket/delete_bucket",
	"bucket/stat_bucket", "bucket/get_bucket_location", "bucket/get_acls", "bucket/put_acls",
	"bucket/get_bucket_policy", "bucket/put_bucket_policy", "bucket/delete_bucket_policy",
	"bucket/get_bucket_tags", "bucket/put_bucket_tags", "bucket/delete_bucket_tags",
	"bucket/get_lifecycle", "bucket/put_lifecycle", "bucket/delete_lifecycle",
	"bucket/multi_object_delete", "bucket/list_bucket_multiparts",

	"object/get_obj", "object/get_obj_tags", "object/get_obj_attrs", "object/get_acls", "object/put_obj",
	"object/copy_obj", "object/delete_obj", "object/put_acls", "object/put_obj_tags", "object/delete_obj_tags",
	"object/init_multipart", "object/complete_multipart", "object/abort_multipart", "object/list_multipart",
}

var _ = Describe("route sets", func() {
	var (
		store *memstore.Store
		calls []string
	)
	BeforeEach(func() {
		store = memstore.New(memstore.Config{})
		calls = nil
	})
	route := func(name string) s3.HandlerFunc {
		return func(_ context.Context, w http.ResponseWriter, _ *op.Request) error {
			calls = append(calls, name)
			w.WriteHeader(http.StatusOK)
			return nil
		}
	}
	build := func(u s3.UnitHandlersForTest) *s3.Handler {
		return s3.NewHandlerWithForTest(testEnv(store), s3.AnonymousOnly{}, testConfig(s3.Config{}), u)
	}
	get := func(h *s3.Handler, target string) int {
		return serveReq(h, http.MethodGet, target, nil).Code
	}

	It("binds nothing for an empty set", func() {
		h := build(s3.UnitHandlersForTest{Sets: []s3.RouteSetForTest{
			{Scope: op.ScopeBucket},
			{Scope: op.ScopeObject, Handlers: map[string]s3.HandlerFunc{}},
		}})
		Expect(s3.BoundRoutesForTest(h)).To(BeEmpty())
	})
	It("binds each set's entries to that set's scope, so bucket and object get_acls reach different handlers", func() {
		h := build(s3.UnitHandlersForTest{Sets: []s3.RouteSetForTest{
			{Scope: op.ScopeBucket, Handlers: map[string]s3.HandlerFunc{
				"get_bucket_versioning": route("versioning"),
				"get_acls":              route("bucket acl"),
			}},
			{Scope: op.ScopeObject, Handlers: map[string]s3.HandlerFunc{"get_acls": route("object acl")}},
		}})
		Expect(get(h, "/plain?versioning")).To(Equal(http.StatusOK))
		Expect(get(h, "/plain?acl")).To(Equal(http.StatusOK))
		Expect(get(h, "/plain/k?acl")).To(Equal(http.StatusOK))
		Expect(calls).To(Equal([]string{"versioning", "bucket acl", "object acl"}))
		Expect(s3.BoundRoutesForTest(h)).To(Equal([]string{"bucket/get_acls", "bucket/get_bucket_versioning", "object/get_acls"}))
	})
	It("binds a set beside the units' maps, each route reaching its own handler", func() {
		h := build(s3.UnitHandlersForTest{
			Bucket: map[string]s3.HandlerFunc{"get_bucket_tags": route("unit")},
			Sets: []s3.RouteSetForTest{
				{Scope: op.ScopeBucket, Handlers: map[string]s3.HandlerFunc{"get_cors": route("set")}},
			},
		})
		Expect(get(h, "/plain?tagging")).To(Equal(http.StatusOK))
		Expect(get(h, "/plain?cors")).To(Equal(http.StatusOK))
		Expect(calls).To(Equal([]string{"unit", "set"}))
	})
	DescribeTable("refuses",
		func(want string, u s3.UnitHandlersForTest) {
			Expect(func() { build(u) }).To(PanicWith(ContainSubstring(want)))
		},
		Entry("a route two sets bind in one scope", `bucket route "get_bucket_versioning" is bound twice`,
			s3.UnitHandlersForTest{Sets: []s3.RouteSetForTest{
				{Scope: op.ScopeBucket, Handlers: map[string]s3.HandlerFunc{"get_bucket_versioning": route("a")}},
				{Scope: op.ScopeBucket, Handlers: map[string]s3.HandlerFunc{"get_bucket_versioning": route("b")}},
			}}),
		Entry("a route a set binds that a unit's map binds too", `bucket route "get_lifecycle" is bound twice`,
			s3.UnitHandlersForTest{
				Bucket: map[string]s3.HandlerFunc{"get_lifecycle": route("unit")},
				Sets: []s3.RouteSetForTest{
					{Scope: op.ScopeBucket, Handlers: map[string]s3.HandlerFunc{"get_lifecycle": route("set")}},
				},
			}),
		Entry("a set naming a route its scope never dispatches", `no object route is named "get_bucket_versioning"`,
			s3.UnitHandlersForTest{Sets: []s3.RouteSetForTest{
				{Scope: op.ScopeObject, Handlers: map[string]s3.HandlerFunc{"get_bucket_versioning": route("x")}},
			}}),
		Entry("a set in a scope Dispatch never routes", `route "get_acls" names scope 3, which Dispatch never routes`,
			s3.UnitHandlersForTest{Sets: []s3.RouteSetForTest{
				{Scope: op.ScopeUser, Handlers: map[string]s3.HandlerFunc{"get_acls": route("x")}},
			}}),
	)
	It("keeps NewHandler binding every route it must always bind, beside the features' sets", func() {
		h := newHandler(store, s3.AnonymousOnly{}, s3.Config{})
		Expect(s3.BoundRoutesForTest(h)).To(ContainElements(alwaysBoundRoutes))
	})
})
