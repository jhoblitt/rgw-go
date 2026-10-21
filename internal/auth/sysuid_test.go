package auth_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // SigV2 signs with HMAC-SHA1
	"encoding/base64"
	"net/http"

	"github.com/aws/aws-sdk-go-v2/aws"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/auth"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
)

// headerV2Get is a GET of target signed by id in its SigV2 Authorization
// header at v2Date, over the string to sign sts.
func headerV2Get(ctx context.Context, target, id, sts string) *http.Request {
	h := hmac.New(sha1.New, []byte(secretOf(id)))
	h.Write([]byte(sts))
	req := newGet(ctx, target)
	req.Header.Set("Date", v2Date)
	req.Header.Set("Authorization", "AWS "+accessKeyOf(id)+":"+base64.StdEncoding.EncodeToString(h.Sum(nil)))
	return req
}

var _ = Describe("A system user's rgwx-uid", func() {
	var (
		store *memstore.Store
		v     *auth.Verifier
	)

	BeforeEach(func() {
		store = memstore.New(memstore.Config{Release: denc.Squid, Now: fixedNow})
		store.AddUser(user("t1$bob", nil))
		store.AddUser(user("carol", func(u *meta.UserInfo) { u.System = 1 }))
		cfg := auth.DefaultConfig()
		cfg.Now = fixedNow
		v = auth.New(cfg, store, store)
	})

	// sigV2 are carol's SigV2 requests of the query rgwx-uid=uid, which
	// neither route signs: the string to sign holds no rgwx- parameter.
	sigV2 := func(ctx context.Context, uid string) map[string]*http.Request {
		return map[string]*http.Request{
			"header-signed": headerV2Get(ctx, "http://s3.example.com/?rgwx-uid="+uid, "carol", "GET\n\n\n"+v2Date+"\n/"),
			"presigned": presignedV2(ctx, http.MethodGet, "/", "rgwx-uid="+uid, nil,
				accessKeyOf("carol"), secretOf("carol"), v2Expires, "GET\n\n\n"+v2Expires+"\n/"),
		}
	}

	It("is not taken from a SigV2 request, which signs none, on either route", func(ctx SpecContext) {
		for route, req := range sigV2(ctx, "t1%24bob") {
			res, err := v.Authenticate(ctx, req, anyPayload)
			Expect(err).NotTo(HaveOccurred(), route)
			Expect(res.Identity.Owner).To(Equal(meta.UserOwner(meta.UserID{ID: "carol"})), route)
			Expect(res.Identity.Tenant).To(BeEmpty(), route)
			Expect(res.Identity.System).To(BeTrue(), route)
		}
	})

	It("is never looked up on a SigV2 request, so one naming no one is no refusal", func(ctx SpecContext) {
		rec, err := store.GetUser(ctx, meta.UserID{ID: "carol"})
		Expect(err).NotTo(HaveOccurred())
		users := &opfakes.FakeUserStore{}
		users.GetUserByAccessKeyReturns(rec, nil)
		users.GetUserReturns(nil, op.ErrNoSuchUser)
		accounts := &opfakes.FakeAccountStore{}
		accounts.GetAccountReturns(nil, op.ErrNoSuchEntity)
		verifier := auth.New(auth.Config{UseRados: true, Now: fixedNow}, users, accounts)
		for _, uid := range []string{"nobody", "RGW99999999999999999"} {
			for route, req := range sigV2(ctx, uid) {
				_, err := verifier.Authenticate(ctx, req, anyPayload)
				Expect(err).NotTo(HaveOccurred(), "%s rgwx-uid=%s", route, uid)
			}
		}
		Expect(users.GetUserCallCount()).To(BeZero(), "the rgwx-uid user")
		Expect(accounts.GetAccountCallCount()).To(BeZero(), "the rgwx-uid account")
	})

	It("is still taken from a SigV4 request, whose canonical query signs it, header-signed or presigned", func(ctx SpecContext) {
		creds := aws.Credentials{AccessKeyID: accessKeyOf("carol"), SecretAccessKey: secretOf("carol")}
		url, _, err := sdkSigner().PresignHTTP(ctx, creds,
			newGet(ctx, "http://s3.example.com/?rgwx-uid=t1%24bob&X-Amz-Expires=300"), "UNSIGNED-PAYLOAD", "s3", "us-east-1", fixedNow())
		Expect(err).NotTo(HaveOccurred())
		for route, req := range map[op.AuthRoute]*http.Request{
			op.RouteSigV4Header: signedAs(ctx, newGet(ctx, "http://s3.example.com/?rgwx-uid=t1%24bob"), "carol"),
			op.RouteSigV4Query:  newGet(ctx, url),
		} {
			res, err := v.Authenticate(ctx, req, anyPayload)
			Expect(err).NotTo(HaveOccurred(), "route %d", route)
			Expect(res.Route).To(Equal(route))
			Expect(res.Identity.Owner).To(Equal(meta.UserOwner(meta.UserID{Tenant: "t1", ID: "bob"})), "route %d", route)
			Expect(res.Identity.Tenant).To(Equal("t1"), "route %d", route)
		}
	})
})
