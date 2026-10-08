package s3_test

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/types"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
)

// wireHeaders sends method for target to h over a plain TCP connection and
// returns the response's header lines as the server wrote them, without
// net/http's client, which canonicalizes every name it reads.
func wireHeaders(ctx context.Context, h http.Handler, method, target string) []string {
	GinkgoHelper()
	srv := httptest.NewServer(h)
	DeferCleanup(srv.Close)
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", srv.Listener.Addr().String())
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(conn.Close)
	_, err = fmt.Fprintf(conn, "%s %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", method, target, srv.Listener.Addr())
	Expect(err).NotTo(HaveOccurred())
	tr := textproto.NewReader(bufio.NewReader(conn))
	status, err := tr.ReadLine()
	Expect(err).NotTo(HaveOccurred())
	Expect(status).To(HavePrefix("HTTP/1.1 200 "))
	var lines []string
	for {
		line, err := tr.ReadLine()
		Expect(err).NotTo(HaveOccurred())
		if line == "" {
			return lines
		}
		lines = append(lines, line)
	}
}

// exactHeader is the first value the handler's header map h holds under
// exactly name, or "" when there is none. net/http writes a key as the
// handler spelled it; http.Header.Get would look up the canonical spelling
// instead.
func exactHeader(h http.Header, name string) string {
	if v := h[name]; len(v) > 0 {
		return v[0]
	}
	return ""
}

// haveHeaderAnyCase matches a header map holding name in any spelling, so a
// negated check also catches a stray write under the canonical one. It
// fails with the spellings found.
func haveHeaderAnyCase(name string) types.GomegaMatcher {
	return WithTransform(func(h http.Header) []string {
		var found []string
		for k := range h {
			if strings.EqualFold(k, name) {
				found = append(found, k)
			}
		}
		return found
	}, Not(BeEmpty()))
}

// headerNames is the name of each header line.
func headerNames(lines []string) []string {
	names := make([]string, len(lines))
	for i, l := range lines {
		names[i], _, _ = strings.Cut(l, ":")
	}
	return names
}

var _ = Describe("response header names", func() {
	// radosgw sends each name as dump_header is handed it: a user metadata
	// name as its attr is stored after user.rgw. (rgw_rest_s3.cc:581-585 at
	// v19.2.6, :698-702 at v20.2.4), and every other name as its source spells
	// it. SDKs take the bytes after x-amz-meta- for the metadata key.
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		It(method+" sends user metadata, and radosgw's other names, in radosgw's case", func(ctx SpecContext) {
			w := newReadWorld(ctx, denc.Squid)
			w.put(ctx, "mixed", []byte("data"), map[string][]byte{
				meta.AttrMetaPrefix + "lower":     []byte("one\x00"),
				meta.AttrMetaPrefix + "MixedCase": []byte("two\x00"),
			})
			lines := wireHeaders(ctx, w.handler(w.env(nil), w.alice), method, "/plain/mixed")
			Expect(lines).To(ContainElements("x-amz-meta-lower: one", "x-amz-meta-MixedCase: two"))
			Expect(headerNames(lines)).To(ContainElements("x-amz-request-id", "x-rgw-object-type", "ETag", "Last-Modified", "Content-Type", "Server"))
			for _, canonical := range []string{"X-Amz-Meta-Lower", "X-Amz-Meta-Mixedcase", "X-Amz-Request-Id", "X-Rgw-Object-Type", "Etag"} {
				Expect(headerNames(lines)).NotTo(ContainElement(canonical))
			}
		})
	}
})
