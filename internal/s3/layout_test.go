package s3_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
	"github.com/jhoblitt/rgw-go/internal/s3"
)

// layoutDoc is a layout document that writes chunks one at a time, as the
// driver's writes its head and manifest, then each stripe's comma and entry.
func layoutDoc(chunks []string) *opfakes.FakeLayoutDocument {
	d := &opfakes.FakeLayoutDocument{}
	d.LenReturns(int64(len(strings.Join(chunks, ""))))
	d.WriteToCalls(func(out io.Writer) (int64, error) {
		var n int64
		for _, c := range chunks {
			m, err := io.WriteString(out, c)
			n += int64(m)
			if err != nil {
				return n, err
			}
		}
		return n, nil
	})
	return d
}

// layoutChunks is the writes of a document laying out stripes stripes, in the
// shape the driver writes one.
func layoutChunks(stripes int) []string {
	const raw = `{"pool":"default.rgw.buckets.data","oid":"m1_k","loc":""}`
	chunks := []string{`{"head":` + raw + `,"manifest":{"objs":[],"obj_size":4},"data_location":[`}
	for i := range stripes {
		if i > 0 {
			chunks = append(chunks, ",")
		}
		chunks = append(chunks, `{"ofs":`+strconv.Itoa(i<<22)+`,"loc":`+raw+`,"loc_ofs":0,"loc_size":4194304}`)
	}
	return append(chunks, "]}")
}

// writeCounter counts the writes the response gets.
type writeCounter struct {
	*httptest.ResponseRecorder
	writes int
}

func (c *writeCounter) Write(p []byte) (int, error) {
	c.writes++
	return c.ResponseRecorder.Write(p)
}

var _ = Describe("the ?layout route", func() {
	var (
		w      *subresWorld
		layout *opfakes.FakeLayoutStore
		// carol holds admin=read and neither owns plain nor holds a grant on it.
		carol op.Identity
	)
	as := func(id op.Identity) s3.Authenticator {
		return s3.AuthenticatorFunc(func(context.Context, *http.Request, op.PayloadForms) (*op.AuthResult, error) {
			return &op.AuthResult{Identity: id}, nil
		})
	}
	identityOf := func(rec *op.UserRecord) op.Identity {
		return op.Identity{User: &rec.Info, Owner: meta.UserOwner(rec.Info.UserID), OpMask: rec.Info.OpMask, Caps: rec.Info.Caps}
	}
	world := func(ctx context.Context, rel denc.Release) {
		GinkgoHelper()
		w = newSubresWorld(ctx, rel)
		carol = identityOf(w.store.AddUser(meta.UserInfo{
			UserID: meta.UserID{ID: "carol"}, DisplayName: "Carol", OpMask: op.OpTypeAll, Caps: meta.Caps{"admin": meta.CapRead},
		}))
		layout = &opfakes.FakeLayoutStore{}
		layout.ObjectLayoutReturns(layoutDoc(layoutChunks(1)), nil)
		w.env.Layout = layout
		owner := meta.UserOwner(w.alice.Info.UserID)
		for _, key := range []meta.ObjKey{{Name: "k"}, {Name: "k", Instance: "v1"}} {
			_, err := w.store.PutObject(ctx, w.bucket(ctx), key, strings.NewReader("data"), op.PutParams{
				Size: 4, Attrs: map[string][]byte{meta.AttrACL: encodeACL(acl.DefaultPolicy(owner, "Alice"))},
			})
			Expect(err).NotTo(HaveOccurred())
		}
	}
	BeforeEach(func(ctx SpecContext) { world(ctx, denc.Squid) })
	handler := func(id op.Identity) *s3.Handler { return s3.NewHandler(w.env, as(id), testConfig(s3.Config{})) }
	send := func(id op.Identity, method, target string) *httptest.ResponseRecorder {
		return serveReq(handler(id), method, target, nil)
	}
	expectRouteError := func(rec *httptest.ResponseRecorder, status int, code string) {
		GinkgoHelper()
		Expect(rec.Code).To(Equal(status), rec.Body.String())
		Expect(rec.Header().Get("Content-Type")).To(Equal("application/xml"))
		Expect(rec.Body.String()).To(ContainSubstring("<Code>" + code + "</Code>"))
	}

	It("binds get_obj_layout in every handler", func() {
		Expect(s3.BoundRoutesForTest(newHandler(w.store, s3.AnonymousOnly{}, s3.Config{}))).To(ContainElement("object/get_obj_layout"))
	})

	DescribeTable("sends the document as it is, as application/json with its length, no Accept-Ranges, and its writes buffered",
		func(ctx SpecContext, stripes int) {
			chunks := layoutChunks(stripes)
			want := strings.Join(chunks, "")
			layout.ObjectLayoutReturns(layoutDoc(chunks), nil)
			req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/plain/k?layout", nil)
			rec := &writeCounter{ResponseRecorder: httptest.NewRecorder()}
			handler(carol).ServeHTTP(rec, req)
			Expect(rec.Code).To(Equal(http.StatusOK), rec.Body.String())
			Expect(rec.Header().Get("Content-Type")).To(Equal("application/json"))
			Expect(rec.Header().Get("Content-Length")).To(Equal(strconv.Itoa(len(want))))
			Expect(rec.Header()).NotTo(HaveKey("Accept-Ranges"), "the buffering filter's length, not dump_content_length's")
			Expect(exactHeader(rec.Header(), "x-amz-request-id")).NotTo(BeEmpty())
			Expect(rec.Body.String()).To(Equal(want))
			Expect(rec.writes).To(BeNumerically("<=", len(want)/4096+1), "%d chunks", len(chunks))
		},
		Entry("an object in its head, one stripe", 1),
		Entry("an empty object, no stripes", 0),
		Entry("a striped object whose entries run past the response's buffer", 4096),
	)

	It("observes the request as get_obj_layout with the document's bytes out", func(ctx SpecContext) {
		m := &opfakes.FakeMetrics{}
		w.env.Metrics = m
		rec := send(carol, http.MethodGet, "/plain/k?layout")
		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(m.ObserveCallCount()).To(Equal(1))
		name, status, _, _, out := m.ObserveArgsForCall(0)
		Expect(name).To(Equal("get_obj_layout"))
		Expect(status).To(Equal(http.StatusOK))
		Expect(out).To(Equal(int64(len(strings.Join(layoutChunks(1), "")))))
	})

	It("refuses the bucket's owner without admin=read with 403 AccessDenied, laying nothing out", func() {
		expectRouteError(send(identityOf(w.alice), http.MethodGet, "/plain/k?layout"), http.StatusForbidden, "AccessDenied")
		Expect(layout.ObjectLayoutCallCount()).To(BeZero())
	})

	DescribeTable("answers a missing object before the cap check, by who may list the bucket",
		func(ctx SpecContext, rel denc.Release, marker string) {
			world(ctx, rel)
			rec := send(identityOf(w.alice), http.MethodGet, "/plain/nope?layout")
			expectRouteError(rec, http.StatusNotFound, "NoSuchKey")
			Expect(exactHeader(rec.Header(), "x-amz-delete-marker")).To(Equal(marker))
			denied := send(carol, http.MethodGet, "/plain/nope?layout")
			expectRouteError(denied, http.StatusForbidden, "AccessDenied")
			Expect(denied.Header()).NotTo(haveHeaderAnyCase("x-amz-delete-marker"))
			Expect(layout.ObjectLayoutCallCount()).To(BeZero())
		},
		Entry("on Squid, without x-amz-delete-marker", denc.Squid, ""),
		Entry("on Tentacle, with x-amz-delete-marker: false, as read_permissions adds it", denc.Tentacle, "false"),
	)

	It("lays out the version versionId names", func() {
		Expect(send(carol, http.MethodGet, "/plain/k?layout&versionId=v1").Code).To(Equal(http.StatusOK))
		Expect(layout.ObjectLayoutCallCount()).To(Equal(1))
		_, st := layout.ObjectLayoutArgsForCall(0)
		Expect(st.Key).To(Equal(meta.ObjKey{Name: "k", Instance: "v1"}))
	})

	It("serves HEAD ?layout as a HEAD of the object, which op_head has no ?layout branch to divert", func() {
		rec := send(identityOf(w.alice), http.MethodHead, "/plain/k?layout")
		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(rec.Header().Get("Content-Length")).To(Equal("4"), "the object's length")
		Expect(exactHeader(rec.Header(), "ETag")).NotTo(BeEmpty())
		expectRouteError(send(carol, http.MethodHead, "/plain/k?layout"), http.StatusForbidden, "AccessDenied")
		Expect(layout.ObjectLayoutCallCount()).To(BeZero())
	})

	DescribeTable("answers a layout that fails as its error, sending nothing of the document",
		func(err error, status int, code string) {
			layout.ObjectLayoutReturns(nil, err)
			rec := send(carol, http.MethodGet, "/plain/k?layout")
			expectRouteError(rec, status, code)
			Expect(layout.ObjectLayoutCallCount()).To(Equal(1), "the layout's own error")
			Expect(rec.Body.String()).NotTo(ContainSubstring(`"head"`))
		},
		Entry("an object without a manifest", fmt.Errorf("%w: plain/k has no manifest to lay out", op.ErrInternalError),
			http.StatusInternalServerError, "InternalError"),
		Entry("a manifest walk that stops moving", fmt.Errorf("%w: walking the manifest of plain/k: %w", op.ErrUnknown, denc.ErrMalformed),
			http.StatusInternalServerError, "UnknownError"),
		Entry("a store that keeps no RADOS objects", fmt.Errorf("%w: laying out k", op.ErrNotImplemented),
			http.StatusNotImplemented, "NotImplemented"),
	)

	It("ends the connection when the document fails after its header, never ending a 200 as if whole", func(ctx SpecContext) {
		logs := captureLog()
		chunks := layoutChunks(1)
		d := layoutDoc(chunks)
		d.WriteToCalls(func(out io.Writer) (int64, error) {
			n, err := io.WriteString(out, chunks[0])
			Expect(err).NotTo(HaveOccurred())
			return int64(n), fmt.Errorf("%w: walking the manifest of plain/k", op.ErrUnknown)
		})
		layout.ObjectLayoutReturns(d, nil)
		req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/plain/k?layout", nil)
		rec := httptest.NewRecorder()
		Expect(func() { handler(carol).ServeHTTP(rec, req) }).To(PanicWith(http.ErrAbortHandler))
		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(rec.Header().Get("Content-Length")).To(Equal(strconv.Itoa(len(strings.Join(chunks, "")))))
		Expect(rec.Body.String()).NotTo(ContainSubstring("<Error>"), "no error document after the header")
		Expect(logs.String()).To(ContainSubstring(`"level":"WARN"`))
		Expect(logs.String()).To(ContainSubstring("failed after the response started"))
	})

	It("ends the connection without a warning when the request's context ends mid-document", func(ctx SpecContext) {
		logs := captureLogAt(slog.LevelDebug)
		reqCtx, cancel := context.WithCancel(ctx)
		DeferCleanup(cancel)
		d := layoutDoc(layoutChunks(1))
		d.WriteToCalls(func(io.Writer) (int64, error) {
			cancel()
			return 0, fmt.Errorf("laying out plain/k: %w", reqCtx.Err())
		})
		layout.ObjectLayoutReturns(d, nil)
		req := httptest.NewRequestWithContext(reqCtx, http.MethodGet, "/plain/k?layout", nil)
		rec := httptest.NewRecorder()
		Expect(func() { handler(carol).ServeHTTP(rec, req) }).To(PanicWith(http.ErrAbortHandler))
		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(logs.String()).To(ContainSubstring("client went away"))
		Expect(logs.String()).NotTo(ContainSubstring("failed after the response started"))
	})
})
