package admin_test

import (
	"context"
	"net/http"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/admin"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

var _ = Describe("a NUL in an admin argument that names a RADOS object", func() {
	var fx *fixture
	BeforeEach(func() { fx = newFixture(admin.Config{}) })

	DescribeTable("is refused with 400 InvalidArgument before authentication, in the request's format",
		func(method, path string) {
			res := fx.request(method, path, "admin")
			Expect(res.StatusCode).To(Equal(http.StatusBadRequest))
			Expect(body(res)).To(HavePrefix(`{"Code":"InvalidArgument",`))
			res = fx.request(method, path+"&format=xml", "admin")
			Expect(res.StatusCode).To(Equal(http.StatusBadRequest))
			Expect(body(res)).To(HavePrefix(`<?xml version="1.0" encoding="UTF-8"?><Error><Code>InvalidArgument</Code>`))
			Expect(fx.authenticated()).To(BeZero(), "authentication reads the access key's index object")
		},
		Entry("uid", http.MethodGet, "/admin/user?uid=a%00b"),
		Entry("uid escaped twice, which get_string decodes again", http.MethodGet, "/admin/user?uid=a%2500b"),
		Entry("tenant", http.MethodPut, "/admin/user?uid=u&display-name=d&tenant=t%00x"),
		Entry("email", http.MethodPut, "/admin/user?uid=u&display-name=d&email=a%00b"),
		Entry("access-key", http.MethodPut, "/admin/user?key&uid=admin&access-key=a%00b"),
		Entry("subuser", http.MethodPut, "/admin/user?subuser&uid=admin&subuser=admin:a%00b"),
		Entry("account-id", http.MethodPut, "/admin/user?uid=u&display-name=d&account-id=a%00b"),
		Entry("bucket", http.MethodGet, "/admin/bucket?bucket=a%00b"),
		Entry("bucket-id", http.MethodPut, "/admin/bucket?uid=admin&bucket=b&bucket-id=a%00b"),
		Entry("new-bucket-name", http.MethodPut, "/admin/bucket?uid=admin&bucket=b&new-bucket-name=a%00b"),
		Entry("object", http.MethodDelete, "/admin/bucket?object&bucket=b&object=a%00b"),
		Entry("an account's id", http.MethodGet, "/admin/account?id=a%00b"),
		Entry("an account's name", http.MethodPost, "/admin/account?name=a%00b"),
	)

	It("creates no user under the name before the NUL", func(ctx SpecContext) {
		res := fx.request(http.MethodPut, "/admin/user?uid=al%00ice&display-name=d", "admin")
		Expect(res.StatusCode).To(Equal(http.StatusBadRequest))
		for _, id := range []string{"al", "al\x00ice"} {
			_, err := fx.store.GetUser(ctx, meta.UserID{ID: id})
			Expect(err).To(MatchError(op.ErrNoSuchUser), "user %q", id)
		}
	})

	DescribeTable("is left to the op where the argument names nothing in RADOS",
		func(method, path string) {
			fx.handler.Register("modify_user", func(context.Context, http.ResponseWriter, *op.Request, admin.Request) error {
				return op.ErrNotImplemented
			})
			res := fx.request(method, path, "admin")
			Expect(res.StatusCode).NotTo(Equal(http.StatusBadRequest), body(res))
			Expect(fx.authenticated()).To(Equal(1))
		},
		Entry("display-name", http.MethodPost, "/admin/user?uid=admin&display-name=a%00b"),
		Entry("a user's bucket listing marker, which reaches cls_user only encoded", http.MethodGet, "/admin/bucket?uid=admin&marker=a%00b"),
	)

	It("answers a request no route takes as it answers any other", func() {
		res := fx.get("/admin/nosuch?uid=a%00b", "admin")
		Expect(res.StatusCode).To(Equal(http.StatusMethodNotAllowed))
	})
})
