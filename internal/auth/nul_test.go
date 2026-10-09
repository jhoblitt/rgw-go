package auth_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/auth"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
)

var _ = Describe("a NUL in a credential that names a RADOS object", func() {
	var (
		users    *opfakes.FakeUserStore
		accounts *opfakes.FakeAccountStore
		v        *auth.Verifier
	)
	BeforeEach(func(ctx SpecContext) {
		store := memstore.New(memstore.Config{Release: denc.Squid, Now: fixedNow})
		store.AddUser(user("carol", func(u *meta.UserInfo) { u.System = 1 }))
		carol, err := store.GetUser(ctx, meta.UserID{ID: "carol"})
		Expect(err).NotTo(HaveOccurred())
		users, accounts = &opfakes.FakeUserStore{}, &opfakes.FakeAccountStore{}
		users.GetUserByAccessKeyCalls(func(_ context.Context, key string) (*op.UserRecord, error) {
			if key == "AKCAROL" {
				return carol, nil
			}
			return nil, op.ErrNoSuchUser
		})
		cfg := auth.DefaultConfig()
		cfg.Now = fixedNow
		v = auth.New(cfg, users, accounts)
	})

	DescribeTable("answers an access key holding one InvalidAccessKeyId without looking it up",
		func(ctx SpecContext, target string) {
			_, err := v.Authenticate(ctx, newGet(ctx, target), anyPayload)
			Expect(err).To(MatchError(op.ErrInvalidAccessKeyID))
			Expect(users.GetUserByAccessKeyCallCount()).To(BeZero(), "the lookup reads the key's index object")
		},
		Entry("a SigV2 query's AWSAccessKeyId",
			"http://s3.example.com/?AWSAccessKeyId=AKCAROL%00x&Expires=1441000000&Signature=x"),
		Entry("a SigV4 query's X-Amz-Credential",
			"http://s3.example.com/?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=AKCAROL%00x%2F20150830%2Fus-east-1%2Fs3%2Faws4_request"+
				"&X-Amz-Date=20150830T123600Z&X-Amz-Expires=60&X-Amz-SignedHeaders=host&X-Amz-Signature=00"),
	)

	DescribeTable("answers a system user's rgwx-uid holding one AccessDenied without loading it",
		func(ctx SpecContext, uid string) {
			_, err := v.Authenticate(ctx, signedAs(ctx, newGet(ctx, "http://s3.example.com/?rgwx-uid="+uid), "carol"), anyPayload)
			Expect(err).To(MatchError(op.ErrAccessDenied))
			Expect(users.GetUserCallCount()).To(BeZero(), "the user's object")
			Expect(accounts.GetAccountCallCount()).To(BeZero(), "the account's object")
		},
		Entry("a user", "t1%24bob%00x"),
		Entry("an account", "RGW00000000000000001%00"),
	)

	It("looks up a key without one", func(ctx SpecContext) {
		_, err := v.Authenticate(ctx, newGet(ctx, "http://s3.example.com/?AWSAccessKeyId=AKCAROL&Expires=1441000000&Signature=x"), anyPayload)
		Expect(err).To(MatchError(op.ErrSignatureDoesNotMatch))
		Expect(users.GetUserByAccessKeyCallCount()).To(Equal(1))
	})
})
