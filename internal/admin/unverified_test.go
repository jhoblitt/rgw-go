package admin_test

import (
	"bytes"
	"context"
	"log"
	"log/slog"
	"net/http"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/admin"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// No admin route is a CORS preflight, so an identity granted without a
// signature compare is refused whatever route it reaches, before the route
// runs and whatever the identity's caps.
var _ = Describe("admin handler with an unverified identity", func() {
	var (
		fx  *fixture
		ran []string
	)
	BeforeEach(func() {
		fx = newFixture(admin.Config{})
		ran = nil
		for _, name := range admin.RouteNamesForTest {
			fx.handler.Register(name, func(_ context.Context, w http.ResponseWriter, _ *op.Request, _ admin.Request) error {
				ran = append(ran, name)
				w.WriteHeader(http.StatusOK)
				return nil
			})
		}
	})

	DescribeTable("refuses it with 500 InternalError, logged with the route and no credentials",
		func(method, path, route string) {
			var buf bytes.Buffer
			oldLogger, oldWriter, oldFlags := slog.Default(), log.Writer(), log.Flags()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
			DeferCleanup(func() {
				slog.SetDefault(oldLogger)
				log.SetOutput(oldWriter)
				log.SetFlags(oldFlags)
			})
			res := fx.request(method, path, "admin", "X-Test-Unverified", "1", "Authorization", "AWS AKADMIN:forged")
			b := body(res)
			Expect(res.StatusCode).To(Equal(http.StatusInternalServerError), b)
			Expect(b).To(ContainSubstring("InternalError"))
			Expect(ran).To(BeEmpty(), "no route runs")
			Expect(buf.String()).To(ContainSubstring(`"level":"ERROR"`))
			Expect(buf.String()).To(ContainSubstring(route))
			Expect(buf.String()).NotTo(ContainSubstring("AKADMIN"))
			Expect(buf.String()).NotTo(ContainSubstring("forged"))

			res = fx.request(method, path, "admin")
			Expect(res.StatusCode).To(Equal(http.StatusOK), "the same request, verified, runs %s", route)
			Expect(ran).To(Equal([]string{route}))
		},
		Entry("GET info", http.MethodGet, "/admin/info", "get_info"),
		Entry("GET user", http.MethodGet, "/admin/user?uid=nocaps", "get_user_info"),
		Entry("PUT user", http.MethodPut, "/admin/user?uid=newuser&display-name=n", "create_user"),
		Entry("DELETE user", http.MethodDelete, "/admin/user?uid=nocaps", "remove_user"),
		Entry("PUT a user's key", http.MethodPut, "/admin/user?key&uid=nocaps", "create_access_key"),
		Entry("POST bucket", http.MethodPost, "/admin/bucket?bucket=b&uid=nocaps", "unlink_bucket"),
		Entry("GET metadata", http.MethodGet, "/admin/metadata/user", "list_metadata"),
		Entry("DELETE usage", http.MethodDelete, "/admin/usage", "trim_usage"),
		Entry("POST account", http.MethodPost, "/admin/account?name=a", "create_account"),
		Entry("POST ratelimit", http.MethodPost, "/admin/ratelimit?scope=user&uid=nocaps", "put_ratelimit_info"),
		Entry("GET realm", http.MethodGet, "/admin/realm", "get_realm"),
	)
})

var _ = Describe("admin handler's real routes with an unverified identity", func() {
	It("removes no user, though the identity holds every cap", func() {
		fx := newFixture(admin.Config{})
		res := fx.request(http.MethodDelete, "/admin/user?uid=nocaps", "admin", "X-Test-Unverified", "1")
		Expect(res.StatusCode).To(Equal(http.StatusInternalServerError), body(res))
		_, err := fx.store.GetUser(context.Background(), meta.UserID{ID: "nocaps"})
		Expect(err).NotTo(HaveOccurred())
	})
})
