package s3_test

import (
	"net/http"
	"net/url"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/s3"
)

// keptTrailers answers op.Trailers from its values.
type keptTrailers map[string]string

func (k keptTrailers) Trailer(name string) (string, bool) {
	v, ok := k[name]
	return v, ok
}

var _ = Describe("ApplyAuth", func() {
	It("moves a SigV2 presigned request's signed query meta into a copy of its headers and out of its query", func() {
		hdr := http.Header{"X-Amz-Meta-A": {"1"}}
		r := &op.Request{Header: hdr, Query: url.Values{
			"x-amz-meta-b": {"2"}, "X-AMZ-META-B": {"3"}, "x-amz-security-token": {"tok"}, "uploadId": {"u"},
		}}
		s3.ApplyAuth(r, &op.AuthResult{QueryMeta: map[string]string{"x-amz-meta-b": "3,2", "x-amz-security-token": "tok"}})
		Expect(r.Header).To(Equal(http.Header{"X-Amz-Meta-A": {"1"}, "X-Amz-Meta-B": {"3,2"}, "X-Amz-Security-Token": {"tok"}}))
		Expect(r.Query).To(Equal(url.Values{"uploadId": {"u"}}), "every parameter a folded name was read from is gone")
		Expect(hdr).To(Equal(http.Header{"X-Amz-Meta-A": {"1"}}), "the server's request keeps its own headers")
	})

	It("gives a request without headers the folded ones", func() {
		r := &op.Request{Query: url.Values{"x-amz-meta-b": {"2"}}}
		s3.ApplyAuth(r, &op.AuthResult{QueryMeta: map[string]string{"x-amz-meta-b": "2"}})
		Expect(r.Header).To(Equal(http.Header{"X-Amz-Meta-B": {"2"}}))
		Expect(r.Query).To(BeEmpty())
	})

	It("leaves the headers and the query as they are without query meta", func() {
		r := &op.Request{Header: http.Header{"X-Amz-Meta-A": {"1"}}, Query: url.Values{"x-amz-meta-b": {"2"}}}
		s3.ApplyAuth(r, &op.AuthResult{})
		Expect(r.Header).To(Equal(http.Header{"X-Amz-Meta-A": {"1"}}))
		Expect(r.Query).To(Equal(url.Values{"x-amz-meta-b": {"2"}}))
		Expect(r.AuthRoute).To(Equal(op.RouteUnknown), "a result that names no route")
	})

	It("gives the request the trailers its payload's reader keeps, and none for a payload that keeps none", func() {
		kept := keptTrailers{"x-amz-checksum-crc32": "AAAAAA=="}
		r := &op.Request{}
		s3.ApplyAuth(r, &op.AuthResult{Body: http.NoBody, Trailers: kept})
		Expect(r.Trailers).To(Equal(op.Trailers(kept)))
		s3.ApplyAuth(r, &op.AuthResult{Body: http.NoBody})
		Expect(r.Trailers).To(BeNil())
	})

	It("gives the request its route, and leaves a header-signed SigV2 request's query as it is", func() {
		r := &op.Request{Query: url.Values{"x-amz-meta-b": {"2"}}}
		s3.ApplyAuth(r, &op.AuthResult{Route: op.RouteSigV2Header})
		Expect(r.AuthRoute).To(Equal(op.RouteSigV2Header))
		Expect(r.Query).To(Equal(url.Values{"x-amz-meta-b": {"2"}}), "the reads skip it; ApplyAuth drops none of it")
	})
})
