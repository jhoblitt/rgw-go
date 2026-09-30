package s3_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cephconf"
	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
	"github.com/jhoblitt/rgw-go/internal/s3"
)

// bucketsCreated is every listed bucket's creation time.
var bucketsCreated = meta.Time{Time: time.Date(2026, 9, 27, 1, 2, 3, 456_000_000, time.UTC)}

var _ = Describe("list_buckets", func() {
	var (
		store *memstore.Store
		alice *op.UserRecord
		as    s3.Authenticator
	)
	BeforeEach(func(ctx SpecContext) {
		store = memstore.New(memstore.Config{Now: func() time.Time { return bucketsCreated.Time }})
		alice = store.AddUser(meta.UserInfo{UserID: meta.UserID{ID: "alice"}, DisplayName: "Alice", OpMask: op.OpTypeAll})
		for _, name := range []string{"plain", "second"} {
			_, err := store.CreateBucket(ctx, op.CreateBucketParams{Name: name, Owner: meta.UserOwner(alice.Info.UserID)})
			Expect(err).NotTo(HaveOccurred())
		}
		as = authAs(alice)
	})
	const (
		opened = `<?xml version="1.0" encoding="UTF-8"?>` +
			`<ListAllMyBucketsResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">` +
			`<Owner><ID>alice</ID><DisplayName>Alice</DisplayName></Owner><Buckets>`
		plain  = `<Bucket><Name>plain</Name><CreationDate>2026-09-27T01:02:03.456Z</CreationDate></Bucket>`
		second = `<Bucket><Name>second</Name><CreationDate>2026-09-27T01:02:03.456Z</CreationDate></Bucket>`
		closed = `</Buckets></ListAllMyBucketsResult>`
	)
	It("renders the owner and every bucket in send_response_begin, _data and _end's order", func() {
		rec := serveReq(newHandler(store, as, s3.Config{}), http.MethodGet, "/", nil)
		Expect(rec.Code).To(Equal(200))
		Expect(rec.Header().Get("Content-Type")).To(Equal("application/xml"))
		Expect(rec.Body.String()).To(Equal(opened + plain + second + closed))
	})
	It("escapes the owner's display name as dump_owner's dump_string does", func() {
		s := memstore.New(memstore.Config{Now: func() time.Time { return bucketsCreated.Time }})
		ann := s.AddUser(meta.UserInfo{UserID: meta.UserID{ID: "ann"}, DisplayName: `A&B "C" <D>`, OpMask: op.OpTypeAll})
		rec := serveReq(newHandler(s, authAs(ann), s3.Config{}), http.MethodGet, "/", nil)
		Expect(rec.Body.String()).To(ContainSubstring(`<Owner><ID>ann</ID><DisplayName>A&amp;B &quot;C&quot; &lt;D&gt;</DisplayName></Owner>`))
	})
	It("names the requesting user as the owner, and lists what its account owns", func() {
		users := &opfakes.FakeUserStore{}
		env := testEnv(store)
		env.Users = users
		account := meta.AccountOwner("RGW11111111111111111")
		acct := s3.AuthenticatorFunc(func(context.Context, *http.Request, op.PayloadForms) (*op.AuthResult, error) {
			return &op.AuthResult{Identity: op.Identity{User: &alice.Info, Owner: account, OpMask: op.OpTypeAll}}, nil
		})
		rec := serveReq(s3.NewHandler(env, acct, testConfig(s3.Config{})), http.MethodGet, "/", nil)
		Expect(rec.Body.String()).To(ContainSubstring(`<Owner><ID>alice</ID><DisplayName>Alice</DisplayName></Owner>`),
			"dump_owner writes s->user, rgw_rest_s3.cc:1523 at v19.2.6")
		_, owner, _, _ := users.ListUserBucketsArgsForCall(0)
		Expect(owner).To(Equal(account))
	})
	It("writes a tenanted user's id as rgw_user::to_str does", func() {
		s := memstore.New(memstore.Config{})
		tom := s.AddUser(meta.UserInfo{UserID: meta.UserID{Tenant: "t1", ID: "tom"}, OpMask: op.OpTypeAll})
		rec := serveReq(newHandler(s, authAs(tom), s3.Config{}), http.MethodGet, "/", nil)
		Expect(rec.Body.String()).To(ContainSubstring(`<Owner><ID>t1$tom</ID></Owner>`), "DisplayName is left out when empty")
	})
	It("ignores max-buckets and continuation-token, which radosgw's S3 get_params never reads", func() {
		h := newHandler(store, as, s3.Config{})
		for _, target := range []string{"/?max-buckets=1", "/?max-buckets=0", "/?max-buckets=x", "/?continuation-token=plain", "/?prefix=s"} {
			rec := serveReq(h, http.MethodGet, target, nil)
			Expect(rec.Code).To(Equal(200), target)
			Expect(rec.Body.String()).To(Equal(opened+plain+second+closed), target)
		}
	})
	It("answers HEAD / with the headers alone: Content-Length 0 and neither Accept-Ranges nor Transfer-Encoding", func() {
		rec := serveReq(newHandler(store, as, s3.Config{}), http.MethodHead, "/", nil)
		Expect(rec.Code).To(Equal(200))
		Expect(rec.Header().Get("Content-Type")).To(Equal("application/xml"))
		Expect(rec.Header().Get("Content-Length")).To(Equal("0"), "BufferingFilter::complete_request, rgw_client_io_filters.h:221-253")
		Expect(rec.Header().Get("Accept-Ranges")).To(BeEmpty())
		Expect(rec.Header().Get("Transfer-Encoding")).To(BeEmpty(), "dump_chunked_encoding skips a HEAD, rgw_rest.cc:399-411")
		Expect(rec.Body.Len()).To(BeZero())
	})
	Describe("over a connection", func() {
		var (
			users   *opfakes.FakeUserStore
			release chan struct{}
			srv     *httptest.Server
		)
		page := func(name string) []meta.BucketEnt {
			return []meta.BucketEnt{{Bucket: meta.BucketID{Name: name}, CreationTime: bucketsCreated}}
		}
		fetch := func(ctx context.Context) *http.Response {
			GinkgoHelper()
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/", nil)
			Expect(err).NotTo(HaveOccurred())
			resp, err := http.DefaultClient.Do(req)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(resp.Body.Close)
			return resp
		}
		BeforeEach(func() {
			users = &opfakes.FakeUserStore{}
			release = make(chan struct{})
			users.ListUserBucketsCalls(func(_ context.Context, _ meta.Owner, marker string, _ int) ([]meta.BucketEnt, string, bool, error) {
				if marker == "" {
					return page("plain"), "plain", true, nil
				}
				<-release
				return page("second"), "", false, nil
			})
			env := testEnv(store)
			env.Users = users
			env.Conf = cephconf.NewOptions(cephconf.MapGetter{"rgw_list_buckets_max_chunk": "1"})
			srv = httptest.NewServer(s3.NewHandler(env, as, testConfig(s3.Config{})))
			DeferCleanup(srv.Close)
			DeferCleanup(func() { // runs first: a blocked page must not hold Close
				select {
				case <-release:
				default:
					close(release)
				}
			})
		})
		It("frames the listing chunked and sends each page as it is read", func(ctx SpecContext) {
			resp := fetch(ctx)
			Expect(resp.StatusCode).To(Equal(200))
			Expect(resp.TransferEncoding).To(Equal([]string{"chunked"}), "end_header(..., CHUNKED_TRANSFER_ENCODING), rgw_rest_s3.cc:1519")
			Expect(resp.ContentLength).To(BeEquivalentTo(-1))
			Expect(resp.Header.Get("Accept-Ranges")).To(BeEmpty(), "no Content-Length, so no dump_content_length")
			Expect(resp.Header.Get("Content-Type")).To(Equal("application/xml"))
			var (
				mu  sync.Mutex
				got bytes.Buffer
			)
			go func() { // the client's reader: whatever the server has flushed shows up in got; it stops at the body's end or close
				defer GinkgoRecover()
				buf := make([]byte, 512)
				for {
					n, err := resp.Body.Read(buf)
					mu.Lock()
					got.Write(buf[:n])
					mu.Unlock()
					if err != nil {
						return
					}
				}
			}()
			read := func() string {
				mu.Lock()
				defer mu.Unlock()
				return got.String()
			}
			Eventually(read).WithTimeout(2*time.Second).WithPolling(5*time.Millisecond).Should(Equal(opened+plain),
				"send_response_data flushes the first page while the second is read, rgw_rest_s3.cc:1529-1538")
			Consistently(read).WithTimeout(50 * time.Millisecond).WithPolling(5 * time.Millisecond).Should(Equal(opened + plain))
			close(release)
			Eventually(read).WithTimeout(2 * time.Second).WithPolling(5 * time.Millisecond).Should(Equal(opened + plain + second + closed))
		})
		It("closes the document when a later page fails, as the scope guard's send_response_end does", func(ctx SpecContext) {
			users.ListUserBucketsCalls(func(_ context.Context, _ meta.Owner, marker string, _ int) ([]meta.BucketEnt, string, bool, error) {
				if marker == "" {
					return page("plain"), "plain", true, nil
				}
				return nil, "", false, op.ErrInternalError
			})
			resp := fetch(ctx)
			Expect(resp.StatusCode).To(Equal(200), "the status went out with the first page")
			b, err := io.ReadAll(resp.Body)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(b)).To(Equal(opened + plain + closed))
		})
		It("answers a failed first page with an ordinary error document", func(ctx SpecContext) {
			users.ListUserBucketsReturns(nil, "", false, op.ErrInternalError)
			resp := fetch(ctx)
			Expect(resp.StatusCode).To(Equal(500), "send_response_begin with op_ret set: end_header's error branch, rgw_rest.cc:620-624")
			Expect(resp.TransferEncoding).To(BeEmpty())
			Expect(resp.Header.Get("Accept-Ranges")).To(Equal("bytes"))
			b, err := io.ReadAll(resp.Body)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(b)).To(ContainSubstring("<Code>InternalError</Code>"))
		})
	})
})
