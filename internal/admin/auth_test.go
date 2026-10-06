package admin_test

import (
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/admin"
	"github.com/jhoblitt/rgw-go/internal/auth"
	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

// These run the admin handler behind auth's verifier, the one serve hands
// it, so the identity's Admin and System flags come from auth's mapping,
// not from a fake's.
var _ = Describe("admin handler behind the auth verifier", func() {
	var (
		h   http.Handler
		now time.Time
	)

	BeforeEach(func() {
		now = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
		store := memstore.New(memstore.Config{Params: meta.ZoneParams{Name: "z1"}})
		for _, u := range []struct {
			id            string
			admin, system uint8
		}{{"sys", 0, 1}, {"adm", 1, 0}, {"plain", 0, 0}} {
			info := meta.NewUserInfo()
			info.UserID = meta.UserID{ID: u.id}
			info.Admin, info.System = u.admin, u.system
			info.AccessKeys = map[string]meta.AccessKey{"AK" + u.id: {ID: "AK" + u.id, Secret: "SK" + u.id, Active: true}}
			store.AddUser(info)
		}
		cfg := auth.DefaultConfig()
		cfg.Now = func() time.Time { return now }
		env := &op.Env{Zone: store, Users: store, Usage: store, Metrics: op.NopMetrics{}, ClusterID: fsid, HostID: hostID}
		h = admin.NewHandler(env, auth.New(cfg, store, store), admin.Config{Prefix: "admin"})
	})

	get := func(ctx SpecContext, user string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1/admin/info", nil)
		if user != "" {
			req.Header.Set("X-Amz-Content-Sha256", "UNSIGNED-PAYLOAD")
			creds := aws.Credentials{AccessKeyID: "AK" + user, SecretAccessKey: "SK" + user}
			Expect(v4.NewSigner().SignHTTP(ctx, creds, req, "UNSIGNED-PAYLOAD", "s3", "us-east-1", now)).To(Succeed())
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	It("lets a system user without the info cap through, as is_admin_of takes admin or system", func(ctx SpecContext) {
		rec := get(ctx, "sys")
		Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
		Expect(rec.Body.String()).To(ContainSubstring(fsid))
	})
	It("lets an admin user without the info cap through", func(ctx SpecContext) {
		Expect(get(ctx, "adm").Code).To(Equal(http.StatusOK))
	})
	It("refuses a signed user who is neither, and an anonymous request", func(ctx SpecContext) {
		rec := get(ctx, "plain")
		Expect(rec.Code).To(Equal(http.StatusForbidden))
		Expect(rec.Body.String()).To(HavePrefix(`{"Code":"AccessDenied",`))
		Expect(get(ctx, "").Code).To(Equal(http.StatusForbidden))
	})
})
