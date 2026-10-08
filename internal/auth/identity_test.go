package auth_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/auth"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
)

func fixedNow() time.Time { return time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC) }

// anyPayload is put_obj's forms, the only route radosgw lets carry both, so
// a spec about identities never trips the payload whitelist.
const anyPayload = op.PayloadSigned | op.PayloadChunked

// accessKeyOf and secretOf are the credentials user gives id.
func accessKeyOf(id string) string { return "AK" + strings.ToUpper(meta.ParseUserID(id).ID) }
func secretOf(id string) string    { return "secret-" + meta.ParseUserID(id).ID }

// user is a user with one active access key, accessKeyOf(id), whose secret
// is secretOf(id).
func user(id string, mutate func(*meta.UserInfo)) meta.UserInfo {
	info := meta.NewUserInfo()
	info.UserID = meta.ParseUserID(id)
	info.DisplayName = id
	info.AccessKeys = map[string]meta.AccessKey{accessKeyOf(id): {ID: accessKeyOf(id), Secret: secretOf(id), Active: true}}
	if mutate != nil {
		mutate(&info)
	}
	return info
}

// sdkSigner signs as aws-sdk-go-v2's S3 client does: the path escaped once.
func sdkSigner() *v4.Signer {
	return v4.NewSigner(func(o *v4.SignerOptions) { o.DisableURIPathEscaping = true })
}

// signWith signs req with SigV4 header auth at t, the way the SDK's S3 client
// does. Its ContentSHA256Header middleware sends x-amz-content-sha256 before
// SignHTTP runs, which does not add the header itself, so it is set here
// unless the caller set one.
func signWith(ctx context.Context, req *http.Request, accessKey, secret string, t time.Time) *http.Request {
	hash := req.Header.Get("X-Amz-Content-Sha256")
	if hash == "" {
		hash = "UNSIGNED-PAYLOAD"
		req.Header.Set("X-Amz-Content-Sha256", hash)
	}
	creds := aws.Credentials{AccessKeyID: accessKey, SecretAccessKey: secret}
	Expect(sdkSigner().SignHTTP(ctx, creds, req, hash, "s3", "us-east-1", t)).To(Succeed())
	return req
}

// signedAt signs req as the user id at t.
func signedAt(ctx context.Context, req *http.Request, id string, t time.Time) *http.Request {
	return signWith(ctx, req, accessKeyOf(id), secretOf(id), t)
}

// signedAs signs req as the user id at fixedNow.
func signedAs(ctx context.Context, req *http.Request, id string) *http.Request {
	return signedAt(ctx, req, id, fixedNow())
}

func newGet(ctx context.Context, target string) *http.Request {
	return httptest.NewRequestWithContext(ctx, http.MethodGet, target, nil)
}

// captureLog sends slog's default logger to a buffer as JSON until the spec
// ends, through the request-id handler the binary installs. slog.SetDefault
// also points the log package at the new handler, and restoring the old
// default leaves it there, so log's writer and flags are restored too.
func captureLog() *bytes.Buffer {
	var buf bytes.Buffer
	oldLogger, oldWriter, oldFlags := slog.Default(), log.Writer(), log.Flags()
	slog.SetDefault(slog.New(op.NewLogHandler(slog.NewJSONHandler(&buf, nil))))
	DeferCleanup(func() {
		slog.SetDefault(oldLogger)
		log.SetOutput(oldWriter)
		log.SetFlags(oldFlags)
	})
	return &buf
}

// logRecords decodes the JSON lines captureLog collected.
func logRecords(buf *bytes.Buffer) []map[string]any {
	var recs []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		Expect(json.Unmarshal([]byte(line), &rec)).To(Succeed())
		recs = append(recs, rec)
	}
	return recs
}

var _ = Describe("Verifier identities", func() {
	var (
		store *memstore.Store
		cfg   auth.Config
		v     *auth.Verifier
	)

	BeforeEach(func() {
		store = memstore.New(memstore.Config{Release: denc.Squid, Now: fixedNow})
		store.AddUser(user("alice", func(u *meta.UserInfo) {
			u.OpMask = op.OpTypeRead
			u.Caps = meta.Caps{"users": 1}
		}))
		store.AddUser(user("t1$bob", nil))
		store.AddUser(user("carol", func(u *meta.UserInfo) { u.System = 1 }))
		store.AddUser(user("dave", func(u *meta.UserInfo) { u.Admin = 1 }))
		store.AddUser(user("erin", func(u *meta.UserInfo) {
			u.AccessKeys["AKERINAPP"] = meta.AccessKey{ID: "AKERINAPP", Secret: "secret-erin-app", Subuser: "erin:app", Active: true}
			u.SubUsers = map[string]meta.SubUser{"erin:app": {Name: "erin:app", Perm: 0x1}}
		}))
		store.AddUser(user("frank", func(u *meta.UserInfo) { u.AccountID = "RGW00000000000000001" }))
		store.AddAccount(meta.AccountInfo{ID: "RGW00000000000000001", Tenant: "acct-tenant", Name: "acme", MaxBuckets: 7})
		store.AddUser(user("grace", func(u *meta.UserInfo) {
			k := u.AccessKeys["AKGRACE"]
			k.Active = false
			u.AccessKeys["AKGRACE"] = k
		}))
		store.AddUser(user("henry", func(u *meta.UserInfo) { u.AccountID = "RGW00000000000000009" })) // no such account
		cfg = auth.DefaultConfig()
		cfg.Now = fixedNow
		v = auth.New(cfg, store, store)
	})

	authenticate := func(ctx context.Context, req *http.Request) (*op.AuthResult, error) {
		return v.Authenticate(ctx, req, anyPayload)
	}
	get := func(ctx context.Context, id string) *op.AuthResult {
		res, err := authenticate(ctx, signedAs(ctx, newGet(ctx, "http://s3.example.com/"), id))
		Expect(err).NotTo(HaveOccurred(), "user %q", id)
		return res
	}

	It("fills the identity the way LocalApplier does", func(ctx SpecContext) {
		res := get(ctx, "alice")
		rec, err := store.GetUser(ctx, meta.UserID{ID: "alice"})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Identity.Anonymous).To(BeFalse())
		Expect(res.Identity.User).NotTo(BeNil())
		Expect(*res.Identity.User).To(Equal(rec.Info))
		Expect(res.Identity.Owner).To(Equal(meta.UserOwner(meta.UserID{ID: "alice"})))
		Expect(res.Identity.Account).To(BeNil())
		Expect(res.Identity.SubUser).To(BeEmpty())
		Expect(res.Identity.Tenant).To(BeEmpty())
		Expect(res.Identity.AccessKey).To(Equal("AKALICE"))
		Expect(res.Identity.OpMask).To(Equal(op.OpTypeRead))
		Expect(res.Identity.Caps).To(Equal(meta.Caps{"users": 1}))
		Expect(res.Identity.Admin).To(BeFalse())
		Expect(res.Identity.System).To(BeFalse())
		Expect(res.Identity.Attrs).To(Equal(rec.Attrs))
		Expect(res.PayloadSHA256).To(Equal("UNSIGNED-PAYLOAD"))
		Expect(res.Body).To(BeNil())
		Expect(res.Presigned).To(BeFalse())
	})

	It("carries the tenant, the subuser, and admin as admin-or-system", func(ctx SpecContext) {
		Expect(get(ctx, "t1$bob").Identity.Tenant).To(Equal("t1"))
		Expect(get(ctx, "erin").Identity.SubUser).To(BeEmpty(), "the primary key names no subuser")
		res, err := authenticate(ctx, signWith(ctx, newGet(ctx, "http://s3.example.com/"), "AKERINAPP", "secret-erin-app", fixedNow()))
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Identity.SubUser).To(Equal("erin:app"))
		Expect(res.Identity.AccessKey).To(Equal("AKERINAPP"))
		Expect(res.Identity.User.UserID).To(Equal(meta.UserID{ID: "erin"}))
		carol, dave := get(ctx, "carol"), get(ctx, "dave")
		Expect(carol.Identity.System).To(BeTrue())
		Expect(carol.Identity.Admin).To(BeTrue(), "is_admin_of is admin || system")
		Expect(dave.Identity.Admin).To(BeTrue())
		Expect(dave.Identity.System).To(BeFalse())
	})

	It("loads the account of an account user and owns as the account", func(ctx SpecContext) {
		res := get(ctx, "frank")
		Expect(res.Identity.Account).NotTo(BeNil())
		Expect(res.Identity.Account.Name).To(Equal("acme"))
		Expect(res.Identity.Account.MaxBuckets).To(BeEquivalentTo(7))
		Expect(res.Identity.Owner).To(Equal(meta.AccountOwner("RGW00000000000000001")))
		Expect(res.Identity.Tenant).To(BeEmpty(), "get_tenant is the user's, not the account's")
	})

	It("denies an account user whose account cannot be loaded, or when there is no account store", func(ctx SpecContext) {
		_, err := authenticate(ctx, signedAs(ctx, newGet(ctx, "http://s3.example.com/"), "henry"))
		Expect(err).To(MatchError(op.ErrAccessDenied))
		noAccounts := auth.New(cfg, store, nil)
		_, err = noAccounts.Authenticate(ctx, signedAs(ctx, newGet(ctx, "http://s3.example.com/"), "frank"), anyPayload)
		Expect(err).To(MatchError(op.ErrAccessDenied))
		_, err = noAccounts.Authenticate(ctx, signedAs(ctx, newGet(ctx, "http://s3.example.com/"), "alice"), anyPayload)
		Expect(err).NotTo(HaveOccurred(), "a user outside an account needs no account store")
	})

	It("loads the account before it compares the signature, as LocalEngine does", func(ctx SpecContext) {
		_, err := authenticate(ctx, signWith(ctx, newGet(ctx, "http://s3.example.com/"), "AKHENRY", "not-henrys-secret", fixedNow()))
		Expect(err).To(MatchError(op.ErrAccessDenied))
	})

	It("answers InvalidAccessKeyId for an unknown or inactive key and AccessDenied for a key the user record lacks, naming no key", func(ctx SpecContext) {
		_, err := authenticate(ctx, signWith(ctx, newGet(ctx, "http://s3.example.com/"), "AKNOBODY", "x", fixedNow()))
		Expect(err).To(MatchError(op.ErrInvalidAccessKeyID))
		Expect(err.Error()).NotTo(ContainSubstring("AKNOBODY"), "an unknown key")
		_, err = authenticate(ctx, signedAs(ctx, newGet(ctx, "http://s3.example.com/"), "grace"))
		Expect(err).To(MatchError(op.ErrInvalidAccessKeyID), "memstore does not index an inactive key")

		rec, err := store.GetUser(ctx, meta.UserID{ID: "grace"})
		Expect(err).NotTo(HaveOccurred())
		users := &opfakes.FakeUserStore{}
		users.GetUserByAccessKeyReturns(rec, nil)
		_, err = auth.New(cfg, users, nil).Authenticate(ctx, signedAs(ctx, newGet(ctx, "http://s3.example.com/"), "grace"), anyPayload)
		Expect(err).To(MatchError(op.ErrInvalidAccessKeyID), "a store that returns the inactive key's user")
		Expect(err.Error()).NotTo(ContainSubstring("AKGRACE"), "an inactive key")

		rec, err = store.GetUser(ctx, meta.UserID{ID: "alice"})
		Expect(err).NotTo(HaveOccurred())
		rec.Info.AccessKeys = nil
		users.GetUserByAccessKeyReturns(rec, nil)
		_, err = auth.New(cfg, users, nil).Authenticate(ctx, signedAs(ctx, newGet(ctx, "http://s3.example.com/"), "alice"), anyPayload)
		Expect(err).To(MatchError(op.ErrAccessDenied), "a key the index knows but the record lost")
		Expect(err.Error()).NotTo(ContainSubstring("AKALICE"), "a key the record lost")
	})

	It("takes the access key id from the lookup, not from the stored key", func(ctx SpecContext) {
		rec, err := store.GetUser(ctx, meta.UserID{ID: "alice"})
		Expect(err).NotTo(HaveOccurred())
		k := rec.Info.AccessKeys["AKALICE"]
		k.ID = ""
		rec.Info.AccessKeys["AKALICE"] = k
		users := &opfakes.FakeUserStore{}
		users.GetUserByAccessKeyReturns(rec, nil)
		res, err := auth.New(cfg, users, nil).Authenticate(ctx, signedAs(ctx, newGet(ctx, "http://s3.example.com/"), "alice"), anyPayload)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Identity.AccessKey).To(Equal("AKALICE"))
		Expect(users.GetUserByAccessKeyCallCount()).To(Equal(1))
		_, key := users.GetUserByAccessKeyArgsForCall(0)
		Expect(key).To(Equal("AKALICE"))
	})

	It("logs a failed key lookup other than a missing user with its code, and none of the request's credentials", func(ctx SpecContext) {
		buf := captureLog()
		users := &opfakes.FakeUserStore{}
		// A store's error names the index object it read, the key itself.
		users.GetUserByAccessKeyReturns(nil, fmt.Errorf("%w: reading users.keys/AKALICE: rados: timed out", op.ErrServiceUnavailable))
		req := signedAs(ctx, newGet(ctx, "http://s3.example.com/"), "alice")
		_, err := auth.New(cfg, users, nil).Authenticate(ctx, req, anyPayload)
		Expect(err).To(MatchError(op.ErrInvalidAccessKeyID))
		Expect(err.Error()).NotTo(ContainSubstring("AKALICE"))
		recs := logRecords(buf)
		Expect(recs).To(HaveLen(1))
		Expect(recs[0]).To(HaveKeyWithValue("level", "ERROR"))
		Expect(recs[0]).To(HaveKeyWithValue("code", "ServiceUnavailable"))
		_, sig, _ := strings.Cut(req.Header.Get("Authorization"), "Signature=")
		Expect(buf.String()).NotTo(ContainSubstring("AKALICE"))
		Expect(buf.String()).NotTo(ContainSubstring(secretOf("alice")))
		Expect(buf.String()).NotTo(ContainSubstring(sig))

		buf.Reset()
		_, err = authenticate(ctx, signWith(ctx, newGet(ctx, "http://s3.example.com/"), "AKNOBODY", "x", fixedNow()))
		Expect(err).To(MatchError(op.ErrInvalidAccessKeyID))
		Expect(logRecords(buf)).To(BeEmpty(), "an unknown key is not an error to log")
	})

	DescribeTable("logs a key lookup's store failure once, under the request's id, with its code and none of the request's credentials",
		func(ctx SpecContext, storeErr error, code string) {
			const id = "tx000000000000000000001-0068d7a1b2-4155-z"
			buf := captureLog()
			users := &opfakes.FakeUserStore{}
			users.GetUserByAccessKeyReturns(nil, storeErr)
			req := signedAs(ctx, newGet(ctx, "http://s3.example.com/"), "alice")
			_, err := auth.New(cfg, users, nil).Authenticate(op.WithRequestID(ctx, id), req, anyPayload)
			Expect(err).To(MatchError(op.ErrInvalidAccessKeyID), "LocalEngine::authenticate denies any lookup failure")
			recs := logRecords(buf)
			Expect(recs).To(ConsistOf(SatisfyAll(
				HaveKeyWithValue("level", "ERROR"),
				HaveKeyWithValue("request_id", id),
				HaveKeyWithValue("code", code),
			)))
			_, sig, _ := strings.Cut(req.Header.Get("Authorization"), "Signature=")
			Expect(sig).NotTo(BeEmpty())
			Expect(buf.String()).NotTo(ContainSubstring("AKALICE"), "the access key")
			Expect(buf.String()).NotTo(ContainSubstring(secretOf("alice")), "the secret")
			Expect(buf.String()).NotTo(ContainSubstring(sig), "the signature")
		},
		Entry("a store error naming the key's index object",
			fmt.Errorf("%w: reading users.keys/AKALICE: rados: timed out", op.ErrServiceUnavailable), "ServiceUnavailable"),
		Entry("a bare expired deadline, radosgw's RequestTimeout", context.DeadlineExceeded, "RequestTimeout"),
	)

	It("refuses a key lookup its client's departure cut short as a gone client's, logging no error", func(ctx SpecContext) {
		buf := captureLog()
		rctx, cancel := context.WithCancel(ctx)
		defer cancel()
		users := &opfakes.FakeUserStore{}
		users.GetUserByAccessKeyStub = func(ctx context.Context, _ string) (*op.UserRecord, error) {
			cancel() // net/http cancels a request's context once its connection is gone
			return nil, fmt.Errorf("reading users.keys/AKALICE: %w", ctx.Err())
		}
		_, err := auth.New(cfg, users, nil).Authenticate(rctx, signedAs(ctx, newGet(ctx, "http://s3.example.com/"), "alice"), anyPayload)
		Expect(err).To(MatchError(op.ErrInvalidAccessKeyID))
		Expect(err).To(MatchError(context.Canceled), "the protocol handler ends the request instead of answering it")
		Expect(err.Error()).NotTo(ContainSubstring("AKALICE"))
		Expect(logRecords(buf)).To(BeEmpty(), "logged at debug: the store did nothing wrong")
	})

	It("denies an rgwx-uid user that does not load, and logs a failure other than a missing user without the rgwx-uid", func(ctx SpecContext) {
		buf := captureLog()
		rec, err := store.GetUser(ctx, meta.UserID{ID: "carol"})
		Expect(err).NotTo(HaveOccurred())
		users := &opfakes.FakeUserStore{}
		users.GetUserByAccessKeyReturns(rec, nil)
		users.GetUserReturns(nil, errors.New("decoding user t1$bob: rados: timed out"))
		verifier := auth.New(cfg, users, nil)
		_, err = verifier.Authenticate(ctx, signedAs(ctx, newGet(ctx, "http://s3.example.com/?rgwx-uid=t1%24bob"), "carol"), anyPayload)
		Expect(err).To(MatchError(op.ErrAccessDenied))
		Expect(err.Error()).NotTo(ContainSubstring("bob"))
		Expect(users.GetUserCallCount()).To(Equal(1))
		_, id := users.GetUserArgsForCall(0)
		Expect(id).To(Equal(meta.UserID{Tenant: "t1", ID: "bob"}))
		recs := logRecords(buf)
		Expect(recs).To(HaveLen(1))
		Expect(recs[0]).To(HaveKeyWithValue("level", "ERROR"))
		Expect(recs[0]).To(HaveKeyWithValue("code", "InternalError"))
		Expect(buf.String()).NotTo(ContainSubstring("bob"))

		buf.Reset()
		users.GetUserReturns(nil, fmt.Errorf("user t1$bob: %w", op.ErrNoSuchUser))
		_, err = verifier.Authenticate(ctx, signedAs(ctx, newGet(ctx, "http://s3.example.com/?rgwx-uid=t1%24bob"), "carol"), anyPayload)
		Expect(err).To(MatchError(op.ErrAccessDenied))
		Expect(err).NotTo(MatchError(op.ErrNoSuchUser), "the store's error stays out of the answer")
		Expect(logRecords(buf)).To(BeEmpty(), "a missing user is not an error to log")
	})

	It("denies an rgwx-uid account that does not load, and logs it without the rgwx-uid", func(ctx SpecContext) {
		buf := captureLog()
		const id = "RGW00000000000000009"
		_, err := authenticate(ctx, signedAs(ctx, newGet(ctx, "http://s3.example.com/?rgwx-uid="+id), "carol"))
		Expect(err).To(MatchError(op.ErrAccessDenied))
		Expect(err.Error()).NotTo(ContainSubstring(id))
		recs := logRecords(buf)
		Expect(recs).To(HaveLen(1))
		Expect(recs[0]).To(HaveKeyWithValue("level", "ERROR"))
		Expect(recs[0]).To(HaveKeyWithValue("code", "NoSuchEntity"))
		Expect(buf.String()).NotTo(ContainSubstring(id))

		buf.Reset()
		_, err = auth.New(cfg, store, nil).Authenticate(ctx, signedAs(ctx, newGet(ctx, "http://s3.example.com/?rgwx-uid="+id), "carol"), anyPayload)
		Expect(err).To(MatchError(op.ErrAccessDenied), "no account store")
		Expect(err.Error()).NotTo(ContainSubstring(id))
		Expect(logRecords(buf)).To(HaveLen(1))
		Expect(buf.String()).NotTo(ContainSubstring(id))
	})

	It("logs the stored account id of an account user whose account does not load", func(ctx SpecContext) {
		buf := captureLog()
		_, err := authenticate(ctx, signedAs(ctx, newGet(ctx, "http://s3.example.com/"), "henry"))
		Expect(err).To(MatchError(op.ErrAccessDenied))
		recs := logRecords(buf)
		Expect(recs).To(HaveLen(1))
		Expect(recs[0]).To(HaveKeyWithValue("level", "ERROR"))
		Expect(recs[0]).To(HaveKeyWithValue("account_id", "RGW00000000000000009"))
	})

	It("honors rgwx-uid for a system user only", func(ctx SpecContext) {
		res, err := authenticate(ctx, signedAs(ctx, newGet(ctx, "http://s3.example.com/?rgwx-uid=t1%24bob"), "carol"))
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Identity.Owner).To(Equal(meta.UserOwner(meta.UserID{Tenant: "t1", ID: "bob"})))
		Expect(res.Identity.Tenant).To(Equal("t1"))
		Expect(res.Identity.User.UserID).To(Equal(meta.UserID{ID: "carol"}), "the identity is still carol's")
		Expect(res.Identity.AccessKey).To(Equal("AKCAROL"))

		res, err = authenticate(ctx, signedAs(ctx, newGet(ctx, "http://s3.example.com/?rgwx-uid=RGW00000000000000001"), "carol"))
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Identity.Owner).To(Equal(meta.AccountOwner("RGW00000000000000001")))
		Expect(res.Identity.Tenant).To(Equal("acct-tenant"))
		Expect(res.Identity.Account).To(BeNil(), "carol belongs to no account")

		_, err = authenticate(ctx, signedAs(ctx, newGet(ctx, "http://s3.example.com/?rgwx-uid=nobody"), "carol"))
		Expect(err).To(MatchError(op.ErrAccessDenied), "a user that does not load")
		_, err = authenticate(ctx, signedAs(ctx, newGet(ctx, "http://s3.example.com/?rgwx-uid=RGW00000000000000009"), "carol"))
		Expect(err).To(MatchError(op.ErrAccessDenied), "an account that does not load")

		res, err = authenticate(ctx, signedAs(ctx, newGet(ctx, "http://s3.example.com/?rgwx-uid="), "carol"))
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Identity.Owner).To(Equal(meta.UserOwner(meta.UserID{ID: "carol"})), "an empty rgwx-uid names no one")

		res, err = authenticate(ctx, signedAs(ctx, newGet(ctx, "http://s3.example.com/?rgwx-uid=t1%24bob"), "alice"))
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Identity.Owner).To(Equal(meta.UserOwner(meta.UserID{ID: "alice"})), "ignored for a non-system user")
		Expect(res.Identity.Tenant).To(BeEmpty())
		_, err = authenticate(ctx, signedAs(ctx, newGet(ctx, "http://s3.example.com/?rgwx-uid=nobody"), "alice"))
		Expect(err).NotTo(HaveOccurred(), "a non-system user's rgwx-uid is never looked up")
	})
})
