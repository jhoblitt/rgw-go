package op_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

var _ = Describe("Request scope", func() {
	DescribeTable("is decided by bucket and object",
		func(bucket, object string, want op.Scope) {
			r := &op.Request{Bucket: bucket, Object: meta.ObjKey{Name: object}}
			Expect(r.Scope()).To(Equal(want))
		},
		Entry("service", "", "", op.ScopeService),
		Entry("bucket", "b", "", op.ScopeBucket),
		Entry("object", "b", "k", op.ScopeObject),
	)
})

var _ = Describe("Anonymous", func() {
	It("is radosgw's anonymous user with the full op mask", func() {
		id := op.Anonymous()
		Expect(id.Anonymous).To(BeTrue())
		Expect(id.Owner.String()).To(Equal("anonymous"), "rgw_owner string")
		Expect(id.OpMask).To(Equal(op.OpTypeAll), "RGWUserInfo's default op_mask")
		Expect(id.User).NotTo(BeNil())
		Expect(id.User.UserID.ID).To(Equal(op.AnonymousUserID))
	})
	It("builds the user info as a default-constructed RGWUserInfo, as rgw_get_anon_user does", func() {
		want := meta.NewUserInfo()
		want.UserID = meta.UserID{ID: op.AnonymousUserID}
		Expect(*op.Anonymous().User).To(Equal(want))
	})
	It("hands out a fresh user info each time", func() {
		op.Anonymous().User.DisplayName = "changed"
		Expect(op.Anonymous().User.DisplayName).To(BeEmpty())
	})
})
