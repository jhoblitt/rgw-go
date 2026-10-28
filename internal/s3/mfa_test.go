package s3_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
	"github.com/jhoblitt/rgw-go/internal/s3"
)

// mfaCode is the current code of the device mfaSerial in these specs.
const (
	mfaSerial = "SERIAL"
	mfaCode   = "123456"
)

// deviceVerifier is a verifier for which the current code of every device
// a user holds is mfaCode.
func deviceVerifier() *opfakes.FakeMFAVerifier {
	v := &opfakes.FakeMFAVerifier{}
	v.VerifyMFAStub = func(_ context.Context, u *meta.UserInfo, serial, pin string) (bool, error) {
		return u != nil && slices.Contains(u.MFAIDs, serial) && pin == mfaCode, nil
	}
	return v
}

// loggedValues is the JSON of the records captureLog collected, less the
// attributes a digit run can fall into by chance: the time, the elapsed
// nanoseconds and the random request id.
func loggedValues(buf *bytes.Buffer) string {
	GinkgoHelper()
	recs := logRecords(buf)
	for _, rec := range recs {
		delete(rec, "time")
		delete(rec, "elapsed")
		delete(rec, "request_id")
	}
	data, err := json.Marshal(recs)
	Expect(err).NotTo(HaveOccurred())
	return string(data)
}

// mfaParsed is a GET of target as user, parsed, in an env whose verifier is
// v, carrying one x-amz-mfa header per value, in order.
func mfaParsed(ctx context.Context, target string, user *meta.UserInfo, v op.MFAVerifier, values ...string) s3.Parsed {
	GinkgoHelper()
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	for _, val := range values {
		req.Header.Add("X-Amz-Mfa", val)
	}
	p, err := s3.ParseRequest(req, s3.Config{}, time.Unix(1, 0))
	Expect(err).NotTo(HaveOccurred())
	p.Req.Identity = op.Identity{User: user, Owner: meta.UserOwner(user.UserID)}
	p.Req.Env = &op.Env{MFA: v}
	return p
}

var _ = Describe("PostAuthInit's x-amz-mfa", func() {
	var (
		alice    *meta.UserInfo
		verifier *opfakes.FakeMFAVerifier
	)
	BeforeEach(func() {
		alice = &meta.UserInfo{UserID: meta.UserID{ID: "alice"}, MFAIDs: []string{mfaSerial}}
		verifier = deviceVerifier()
	})
	// initWith runs PostAuthInit on a GET of /plain/k as alice carrying
	// values, and returns the request.
	initWith := func(ctx context.Context, values ...string) *op.Request {
		GinkgoHelper()
		p := mfaParsed(ctx, "/plain/k", alice, verifier, values...)
		Expect(s3.PostAuthInit(ctx, p)).To(Succeed(), "nothing about MFA fails the request")
		return p.Req
	}
	expectChecked := func(serial, pin string) {
		GinkgoHelper()
		Expect(verifier.VerifyMFACallCount()).To(Equal(1), "one check a request")
		_, user, gotSerial, gotPin := verifier.VerifyMFAArgsForCall(0)
		Expect(user).To(BeIdenticalTo(alice), "the requester's user record")
		Expect([]string{gotSerial, gotPin}).To(Equal([]string{serial, pin}))
	}

	It("checks no code for a request without the header", func(ctx SpecContext) {
		r := initWith(ctx)
		Expect(r.MFAVerified).To(BeFalse())
		Expect(r.MFAPresent).To(BeFalse())
		Expect(verifier.VerifyMFACallCount()).To(BeZero())
	})

	DescribeTable("notes a header that is present, whether or not its code verifies",
		func(ctx SpecContext, value string, verified bool) {
			r := initWith(ctx, value)
			Expect(r.MFAPresent).To(BeTrue(), "%q", value)
			Expect(r.MFAVerified).To(Equal(verified), "%q", value)
		},
		Entry("the device's current code", "SERIAL 123456", true),
		Entry("another code", "SERIAL 654321", false),
		Entry("a value that does not split in two", "SERIAL", false),
		Entry("an empty value", "", false),
	)
	It("notes the header for an identity granted on its key's id alone, whose code it checks not", func(ctx SpecContext) {
		p := mfaParsed(ctx, "/plain/k", alice, verifier, "SERIAL 123456")
		p.Req.Identity.Unverified = true
		Expect(s3.PostAuthInit(ctx, p)).To(Succeed())
		Expect(p.Req.MFAPresent).To(BeTrue())
		Expect(p.Req.MFAVerified).To(BeFalse())
	})
	It("notes the header in an env without a verifier", func(ctx SpecContext) {
		p := mfaParsed(ctx, "/plain/k", alice, nil, "SERIAL 123456")
		Expect(s3.PostAuthInit(ctx, p)).To(Succeed())
		Expect(p.Req.MFAPresent).To(BeTrue())
		Expect(p.Req.MFAVerified).To(BeFalse())
	})

	DescribeTable("hands the verifier the two fields a split at spaces gives, as get_str_vec splits it, and takes its verdict",
		func(ctx SpecContext, value, serial, pin string, want bool) {
			Expect(initWith(ctx, value).MFAVerified).To(Equal(want))
			expectChecked(serial, pin)
		},
		Entry("the device's current code", "SERIAL 123456", mfaSerial, mfaCode, true),
		Entry("doubled spaces between them, which for_each_substr skips as a run", "SERIAL  123456", mfaSerial, mfaCode, true),
		Entry("leading and trailing spaces, which it skips too", " SERIAL 123456 ", mfaSerial, mfaCode, true),
		Entry("another code", "SERIAL 654321", mfaSerial, "654321", false),
		Entry("a serial the user has no device for", "OTHER 123456", "OTHER", mfaCode, false),
		Entry("a tab after the code, which is part of the code", "SERIAL 123456\t", mfaSerial, "123456\t", false),
		Entry("a NUL in the code, handed on whole for the verifier to refuse where radosgw's C string would end at it",
			"SERIAL 123456\x00junk", mfaSerial, "123456\x00junk", false),
	)

	DescribeTable("checks no code for a value that does not split in two, as verify_mfa answers EINVAL before mfa_ids and check_mfa",
		func(ctx SpecContext, value string) {
			Expect(initWith(ctx, value).MFAVerified).To(BeFalse())
			Expect(verifier.VerifyMFACallCount()).To(BeZero(), "a code handed on as a pin would count toward the device's checks")
		},
		Entry("one field", "SERIAL123456"),
		Entry("three fields", "SERIAL 123456 x"),
		Entry("a code a space splits", "SERIAL 123 456"),
		Entry("a tab between serial and code, which only a space separates", "SERIAL\t123456"),
		Entry("a no-break space between them, which only a space separates", "SERIAL\u00a0123456"),
		Entry("an empty value", ""),
		Entry("spaces alone", "   "),
	)

	DescribeTable("reads the last of a repeated header, the one radosgw's env keeps",
		func(ctx SpecContext, values []string, checks int, want bool) {
			Expect(initWith(ctx, values...).MFAVerified).To(Equal(want))
			Expect(verifier.VerifyMFACallCount()).To(Equal(checks))
		},
		Entry("a good value last", []string{"SERIAL", "SERIAL 123456"}, 1, true),
		Entry("a malformed value last", []string{"SERIAL 123456", "SERIAL"}, 0, false),
	)

	DescribeTable("logs a check that could not run and carries on unverified",
		func(ctx SpecContext, verdict bool) {
			buf := captureLogAt(slog.LevelDebug)
			verifier.VerifyMFAStub = nil
			verifier.VerifyMFAReturns(verdict, fmt.Errorf("otp check of device %q in user:alice: %w", mfaSerial, errors.New("rados: ret=-5")))
			Expect(initWith(ctx, "SERIAL 123456").MFAVerified).To(BeFalse())
			recs := logRecords(buf)
			Expect(recs).To(ContainElement(SatisfyAll(
				HaveKeyWithValue("level", "WARN"),
				HaveKeyWithValue("error", ContainSubstring("rados: ret=-5")),
			)), buf.String())
			Expect(loggedValues(buf)).NotTo(ContainSubstring(mfaCode))
		},
		Entry("a false verdict", false),
		Entry("even a true verdict beside the error", true),
	)

	It("logs a check that ended with its request at debug, not warn: a client that goes away ends it", func(ctx SpecContext) {
		buf := captureLogAt(slog.LevelDebug)
		gone, cancel := context.WithCancel(ctx)
		verifier.VerifyMFAStub = func(context.Context, *meta.UserInfo, string, string) (bool, error) {
			cancel()
			return false, fmt.Errorf("otp check of device %q in user:alice: %w", mfaSerial, context.Canceled)
		}
		p := mfaParsed(gone, "/plain/k", alice, verifier, "SERIAL 123456")
		Expect(s3.PostAuthInit(gone, p)).To(Succeed())
		Expect(p.Req.MFAVerified).To(BeFalse())
		Expect(logRecords(buf)).To(ConsistOf(SatisfyAll(
			HaveKeyWithValue("level", "DEBUG"),
			HaveKeyWithValue("error", ContainSubstring("context canceled")),
		)), buf.String())
	})

	It("checks no code for an identity granted on its key's id alone, which proves nothing of the key's user", func(ctx SpecContext) {
		p := mfaParsed(ctx, "/plain/k", alice, verifier, "SERIAL 123456")
		p.Req.Identity.Unverified = true
		Expect(s3.PostAuthInit(ctx, p)).To(Succeed())
		Expect(p.Req.MFAVerified).To(BeFalse())
		Expect(verifier.VerifyMFACallCount()).To(BeZero(), "the check would record a try on the user's device")
	})

	It("checks no code in an env without a verifier", func(ctx SpecContext) {
		p := mfaParsed(ctx, "/plain/k", alice, nil, "SERIAL 123456")
		Expect(s3.PostAuthInit(ctx, p)).To(Succeed())
		Expect(p.Req.MFAVerified).To(BeFalse())
	})

	It("checks no code for a request whose names it refuses, as postauth_init returns before the header", func(ctx SpecContext) {
		p := mfaParsed(ctx, "/t-1:plain/k", alice, verifier, "SERIAL 123456")
		Expect(s3.PostAuthInit(ctx, p)).To(MatchError(op.ErrInvalidTenantName))
		Expect(verifier.VerifyMFACallCount()).To(BeZero())
	})
})

var _ = Describe("the handler's x-amz-mfa", func() {
	var (
		store    *memstore.Store
		alice    *op.UserRecord
		verifier *opfakes.FakeMFAVerifier
		h        *s3.Handler
		seen     *op.Request
	)
	BeforeEach(func() {
		store = memstore.New(memstore.Config{})
		alice = store.AddUser(meta.UserInfo{UserID: meta.UserID{ID: "alice"}, OpMask: op.OpTypeAll, MFAIDs: []string{mfaSerial}})
		verifier = deviceVerifier()
		env := testEnv(store)
		env.MFA = verifier
		h = s3.NewHandler(env, authAs(alice), testConfig(s3.Config{}))
		seen = nil
		h.Register("get_obj", ok(&seen))
	})

	It("checks the code once for a request whose op needs no MFA, and gives the op the verdict", func() {
		rec := serveReq(h, http.MethodGet, "/plain/k", nil, "x-amz-mfa", "SERIAL 123456")
		Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
		Expect(seen.MFAVerified).To(BeTrue())
		Expect(verifier.VerifyMFACallCount()).To(Equal(1))
	})

	DescribeTable("logs nothing of the header value",
		func(value string, fail bool) {
			buf := captureLogAt(slog.LevelDebug)
			if fail {
				verifier.VerifyMFAStub = nil
				verifier.VerifyMFAReturns(false, fmt.Errorf("otp check of device %q in user:alice: %w", mfaSerial, errors.New("rados: ret=-5")))
			}
			Expect(serveReq(h, http.MethodGet, "/plain/k", nil, "x-amz-mfa", value).Code).To(Equal(http.StatusOK))
			Expect(logRecords(buf)).NotTo(BeEmpty(), "the request was logged")
			Expect(loggedValues(buf)).NotTo(ContainSubstring(mfaCode))
		},
		Entry("a value that does not split in two", "SERIAL 123456 x", false),
		Entry("a tab between serial and code", "SERIAL\t123456", false),
		Entry("a verified code", "SERIAL 123456", false),
		Entry("a code another device's serial names", "OTHER 123456", false),
		Entry("a check that could not run", "SERIAL 123456", true),
	)

	// net/textproto refuses the header line, and net/http answers the bare
	// 400 it gives a request it cannot read.
	It("never sees a NUL: net/http answers 400 to a header value holding one before any handler runs", func(ctx SpecContext) {
		srv := httptest.NewServer(h)
		DeferCleanup(srv.Close)
		var d net.Dialer
		conn, err := d.DialContext(ctx, "tcp", srv.Listener.Addr().String())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(conn.Close)
		_, err = fmt.Fprintf(conn, "GET /plain/k HTTP/1.1\r\nHost: %s\r\nX-Amz-Mfa: SERIAL 123456\x00\r\nConnection: close\r\n\r\n", srv.Listener.Addr())
		Expect(err).NotTo(HaveOccurred())
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(resp.Body.Close)
		Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
		Expect(resp.Header.Get("x-amz-request-id")).To(BeEmpty(), "the handler never ran")
		Expect(seen).To(BeNil())
		Expect(verifier.VerifyMFACallCount()).To(BeZero())
	})
})
