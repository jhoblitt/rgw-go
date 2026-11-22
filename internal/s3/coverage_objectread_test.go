package s3_test

import (
	"context"
	"net/http"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/policy"
	"github.com/jhoblitt/rgw-go/internal/s3"
)

// The specs of this file pin entries of docs/differences.md, "Object read
// differences", that no other spec pinned.
var _ = Describe("GET and HEAD, as docs/differences.md records them", func() {
	var w *writeWorld
	BeforeEach(func(ctx SpecContext) {
		w = newWriteWorld(ctx, denc.Squid)
		w.put(ctx, "empty", "", nil)
	})

	// "An empty header reads as no header": radosgw takes each of these
	// sent empty as present.
	DescribeTable("take Range and the four conditionals sent empty as absent, and serve the object",
		func(target string, hdr ...string) {
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				rec := w.send(w.alice, method, target, "", hdr...)
				Expect(rec.Code).To(Equal(http.StatusOK), "%s %v: %s", method, hdr, rec.Body.String())
			}
		},
		Entry("an empty If-Match, which no ETag starts on radosgw: 412", "/plain/src", "If-Match", ""),
		Entry("an empty If-Unmodified-Since, which radosgw's parse_time refuses: 400", "/plain/src", "If-Unmodified-Since", ""),
		Entry("an empty If-Modified-Since, which radosgw's parse_time refuses: 400", "/plain/src", "If-Modified-Since", ""),
		Entry("an empty Range on an empty object, which radosgw answers 416", "/plain/empty", "Range", ""),
	)
	It("keeps If-Modified-Since in force beside an empty If-None-Match, which turns it off on radosgw", func() {
		rec := w.send(w.alice, http.MethodGet, "/plain/src", "", "If-None-Match", "", "If-Modified-Since", "Fri, 01 Jan 2100 00:00:00 GMT")
		Expect(rec.Code).To(Equal(http.StatusNotModified))
	})

	// "An empty Range leaves checksum mode on".
	It("show the stored checksum under checksum mode beside an empty Range, which turns it off on radosgw", func() {
		Expect(w.send(w.alice, http.MethodPut, "/plain/k", "hello", "X-Amz-Checksum-Algorithm", "CRC32").Code).To(Equal(http.StatusOK))
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			rec := w.send(w.alice, method, "/plain/k", "", "X-Amz-Checksum-Mode", "ENABLED", "Range", "")
			Expect(rec.Code).To(Equal(http.StatusOK), method)
			Expect(checksumHeaders(rec.Header())).To(Equal(map[string]string{"x-amz-checksum-crc32": helloCRC32, "x-amz-checksum-type": "FULL_OBJECT"}), method)
		}
	})

	// "X-Rgw-Auth": radosgw answers a GET carrying it with the status and
	// headers alone and a Content-Length of 0.
	It("serve a GET carrying X-Rgw-Auth its body, ignoring the header", func() {
		rec := w.send(w.alice, http.MethodGet, "/plain/src", "", "X-Rgw-Auth", "yes")
		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(rec.Body.String()).To(Equal("hello"))
		Expect(rec.Header().Get("Content-Length")).To(Equal("5"))
	})
})

// "A user metadata name that is not a valid field name is not sent": net/http
// drops such a header, where radosgw sends the name as stored.
var _ = Describe("user metadata whose name is no valid field name, as docs/differences.md records it", func() {
	It("is left out of a GET and a HEAD on the wire, the rest of the metadata sent", func(ctx SpecContext) {
		w := newReadWorld(ctx, denc.Squid)
		w.setAttrs(ctx, "small", map[string][]byte{meta.AttrMetaPrefix + "a b": []byte("v")})
		h := w.handler(w.env(nil), w.alice)
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			var sent []string
			for _, line := range wireHeaders(ctx, h, method, "/plain/small") {
				if l := strings.ToLower(line); strings.HasPrefix(l, "x-amz-meta-") {
					sent = append(sent, l)
				}
			}
			Expect(sent).To(Equal([]string{"x-amz-meta-color: blue"}), method)
		}
	})
})

// "GET and HEAD serve a system request without the object-lock
// permissions": Tentacle refuses a system request lacking either permission
// with 403 AccessDenied; rgw-go serves it, each header left to its
// permission.
var _ = Describe("a system request's read of a locked object, as docs/differences.md records it", func() {
	It("is served on Tentacle without the lock headers its permissions refuse", func(ctx SpecContext) {
		w := newWriteWorld(ctx, denc.Tentacle)
		w.lockPlain(ctx, nil)
		asked := 0
		w.env.Authz = lockReadAuthz{
			Authorizer: w.env.Authz, asked: &asked,
			deny: map[policy.Action]bool{policy.S3GetObjectRetention: true, policy.S3GetObjectLegalHold: true},
		}
		w.put(ctx, "k", "data", map[string][]byte{meta.AttrObjectRetention: retentionAttr, meta.AttrObjectLegalHold: holdAttr})
		system := s3.AuthenticatorFunc(func(context.Context, *http.Request, op.PayloadForms) (*op.AuthResult, error) {
			return &op.AuthResult{Identity: op.Identity{
				User: &w.alice.Info, Owner: meta.UserOwner(w.alice.Info.UserID), OpMask: op.OpTypeAll, System: true,
			}}, nil
		})
		h := s3.NewHandler(w.env, system, testConfig(s3.Config{}))
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			rec := serveReq(h, method, "/plain/k", nil)
			Expect(rec.Code).To(Equal(http.StatusOK), method)
			for _, name := range []string{hdrLockMode, hdrLockUntil, hdrLockHold} {
				Expect(exactHeader(rec.Header(), name)).To(BeEmpty(), "%s %s", method, name)
			}
		}
		Expect(asked).To(BeNumerically(">", 0), "the permissions were asked")
	})
})
