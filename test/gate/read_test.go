//go:build integration

package gate_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/test/gate"
)

// The variables hack/rooket/read-gate.sh exports: the radosgw's S3 URL, the
// rgw-go's, and rgw-go's metrics URL.
const (
	radosgwEndpointEnv = "RGW_GO_TEST_RGW_ENDPOINT"
	rgwGoEndpointEnv   = "RGW_GO_TEST_RGW_GO_ENDPOINT"
	rgwGoMetricsEnv    = "RGW_GO_TEST_RGW_GO_METRICS"
)

// mustEnv returns the variable name holds, failing the spec when it is unset.
func mustEnv(name string) string {
	GinkgoHelper()
	v := os.Getenv(name)
	Expect(v).NotTo(BeEmpty(), "%s is not set; run make read-gate RELEASE=squid|tentacle", name)
	return v
}

// response is one answer to a raw request, its body read whole.
type response struct {
	status int
	header http.Header
	body   []byte
}

// rawClient signs a request with SigV4 over UNSIGNED-PAYLOAD and sends it as
// given, so that both gateways see the same bytes: a HEAD with a Range,
// conditional headers in any combination, and subresources without the
// parameters and headers an SDK adds to its operations.
type rawClient struct {
	http   *http.Client
	signer *v4.Signer
	creds  aws.Credentials
}

func newRawClient(u gate.User) *rawClient {
	return &rawClient{
		// Without DisableCompression the transport asks for gzip and
		// decodes it, so the body compared would not be the one sent.
		http:   &http.Client{Timeout: 2 * time.Minute, Transport: &http.Transport{DisableCompression: true}},
		signer: v4.NewSigner(func(o *v4.SignerOptions) { o.DisableURIPathEscaping = true }),
		creds:  aws.Credentials{AccessKeyID: u.AccessKey, SecretAccessKey: u.SecretKey},
	}
}

// do sends method on path, which may carry a query, to the gateway at
// endpoint with the headers hdr, signed, and returns the answer.
func (c *rawClient) do(ctx context.Context, endpoint, method, path string, hdr map[string]string) response {
	GinkgoHelper()
	req, err := http.NewRequestWithContext(ctx, method, endpoint+path, nil)
	Expect(err).NotTo(HaveOccurred(), "%s %s", method, path)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	req.Header.Set("X-Amz-Content-Sha256", "UNSIGNED-PAYLOAD")
	Expect(c.signer.SignHTTP(ctx, c.creds, req, "UNSIGNED-PAYLOAD", "s3", "us-east-1", time.Now())).To(Succeed())
	resp, err := c.http.Do(req)
	Expect(err).NotTo(HaveOccurred(), "%s %s%s", method, endpoint, path)
	body, err := io.ReadAll(resp.Body)
	Expect(errors.Join(err, resp.Body.Close())).NotTo(HaveOccurred(), "reading the body of %s %s%s", method, endpoint, path)
	return response{status: resp.StatusCode, header: resp.Header, body: body}
}

// sdkClient is an aws-sdk-go-v2 S3 client of u's against endpoint that sends
// and checks a checksum only where an operation requires one, and never
// retries, so each call is one request.
func sdkClient(endpoint string, u gate.User) *awss3.Client {
	return awss3.New(awss3.Options{
		Region:                     "us-east-1",
		BaseEndpoint:               aws.String(endpoint),
		UsePathStyle:               true,
		Credentials:                credentials.NewStaticCredentialsProvider(u.AccessKey, u.SecretKey, ""),
		RetryMaxAttempts:           1,
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	})
}

// radosStats is what one scrape of rgw-go's metrics says of its RADOS
// traffic: read operations submitted since it started, and operations and
// requests not yet finished, each summed over completion modes.
type radosStats struct {
	readOps, opsInFlight, requestsInFlight float64
}

func scrapeRADOS(ctx context.Context, url string) radosStats {
	GinkgoHelper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	Expect(err).NotTo(HaveOccurred())
	resp, err := http.DefaultClient.Do(req)
	Expect(err).NotTo(HaveOccurred(), "scraping %s", url)
	Expect(resp.StatusCode).To(Equal(http.StatusOK), "scraping %s", url)
	parser := expfmt.NewTextParser(model.UTF8Validation)
	families, err := parser.TextToMetricFamilies(resp.Body)
	Expect(errors.Join(err, resp.Body.Close())).NotTo(HaveOccurred(), "parsing %s", url)
	var s radosStats
	for _, f := range []string{"rgw_go_rados_ops_total", "rgw_go_rados_ops_in_flight", "rgw_go_requests_in_flight"} {
		Expect(families).To(HaveKey(f), "%s serves no %s", url, f)
	}
	for _, mt := range families["rgw_go_rados_ops_total"].GetMetric() {
		for _, l := range mt.GetLabel() {
			if l.GetName() == "kind" && l.GetValue() == "read" {
				s.readOps += mt.GetCounter().GetValue()
			}
		}
	}
	for _, mt := range families["rgw_go_rados_ops_in_flight"].GetMetric() {
		s.opsInFlight += mt.GetGauge().GetValue()
	}
	for _, mt := range families["rgw_go_requests_in_flight"].GetMetric() {
		s.requestsInFlight += mt.GetGauge().GetValue()
	}
	return s
}

// quietWindow is how long the RADOS read counter must hold still on each
// side of a measured request. The counter is the whole process's: the parity
// options keep the gc worker off, but the quota syncs and the index-completion
// retries read on their own, the quota user sync at startup among them. A
// read in either window marks the measurement as disturbed rather than
// miscounted. The usage flush writes each request's usage about a second
// later; a write leaves the read counter alone, so the window ignores it.
const (
	quietWindow  = time.Second
	quietPolling = 50 * time.Millisecond
	quietTries   = 6
)

// quietReads waits for rgw-go to have no request and no RADOS operation in
// flight, so that every op of the request before it has completed, then
// reports its read-op count and whether that count held for quietWindow. A
// false is background work, not a failure.
func quietReads(ctx context.Context, url string) (float64, bool) {
	GinkgoHelper()
	Eventually(func(g Gomega) {
		s := scrapeRADOS(ctx, url)
		g.Expect(s.opsInFlight).To(BeZero(), "RADOS operations in flight")
		g.Expect(s.requestsInFlight).To(BeZero(), "requests in flight")
	}).WithContext(ctx).WithTimeout(time.Minute).WithPolling(quietPolling).Should(Succeed())
	start := scrapeRADOS(ctx, url).readOps
	failures := InterceptGomegaFailures(func() {
		Consistently(func() float64 { return scrapeRADOS(ctx, url).readOps }).
			WithContext(ctx).WithTimeout(quietWindow).WithPolling(quietPolling).
			Should(Equal(start))
	})
	return start, len(failures) == 0
}

// readOpsOf returns how many RADOS read ops send costs rgw-go, from the
// counter's change across one call measured between two quiet windows. It
// calls send once first, unmeasured, so that the bucket, the user and the
// access key are in rgw-go's metadata cache and the count is the request's
// own. A background read can still land in the moment between the windows,
// so the count is the first that two undisturbed measurements agree on, out
// of quietTries.
func readOpsOf(ctx context.Context, url string, send func()) float64 {
	GinkgoHelper()
	send()
	var counts []float64
	for try := range quietTries {
		before, quiet := quietReads(ctx, url)
		if !quiet {
			note("try %d: background RADOS reads before the request; measuring again", try+1)
			continue
		}
		send()
		after, quiet := quietReads(ctx, url)
		if !quiet {
			note("try %d: background RADOS reads after the request; measuring again", try+1)
			continue
		}
		note("try %d: %v RADOS read ops", try+1, after-before)
		if slices.Contains(counts, after-before) {
			return after - before
		}
		counts = append(counts, after-before)
	}
	Fail(fmt.Sprintf("no two undisturbed measurements of %d tries agreed: %v", quietTries, counts))
	return 0
}

// The headers both gateways must send alike, besides every x-amz-meta-*.
// Date, x-amz-request-id and x-amz-id-2 differ by design.
var comparedHeaders = []string{
	"Content-Length", "Content-Type", "ETag", "Last-Modified", "Accept-Ranges", "Content-Range",
	"X-Amz-Storage-Class", "X-Amz-Tagging-Count", "X-Rgw-Object-Type", "X-Amz-Mp-Parts-Count",
	"X-Amz-Version-Id", "Cache-Control", "Content-Disposition", "Content-Encoding", "Content-Language", "Expires",
}

func metaHeaders(h http.Header) []string {
	var names []string
	for name := range maps.Keys(h) {
		if strings.HasPrefix(name, "X-Amz-Meta-") {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

var _ = Describe("object reads through rgw-go against radosgw", Label("integration", "read"), func() {
	var (
		m             gate.Manifest
		release       denc.Release
		raw           *rawClient
		rgw, rgo, met string
	)

	BeforeEach(func() {
		m = loadManifest()
		var ok bool
		release, ok = denc.ParseRelease(m.Release)
		Expect(ok).To(BeTrue(), "the manifest's release %q", m.Release)
		rgw, rgo, met = mustEnv(radosgwEndpointEnv), mustEnv(rgwGoEndpointEnv), mustEnv(rgwGoMetricsEnv)
		raw = newRawClient(m.Users[0])
	})

	// compare sends one request to both gateways and requires rgw-go's
	// answer to be radosgw's: the status, the compared headers, every
	// x-amz-meta-* header and the body, byte for byte. It returns radosgw's.
	compare := func(ctx context.Context, method, path string, hdr map[string]string) response {
		GinkgoHelper()
		oracle := raw.do(ctx, rgw, method, path, hdr)
		got := raw.do(ctx, rgo, method, path, hdr)
		var b strings.Builder
		b.WriteString(method + " " + path)
		for _, name := range slices.Sorted(maps.Keys(hdr)) {
			fmt.Fprintf(&b, " [%s: %s]", name, hdr[name])
		}
		what := b.String()
		Expect(got.status).To(Equal(oracle.status), "%s: status; rgw-go's body %s, radosgw's %s", what, brief(got.body), brief(oracle.body))
		for _, h := range comparedHeaders {
			if h == "Content-Length" && oracle.status >= 400 {
				// The error document's length follows its request id.
				continue
			}
			Expect(got.header.Values(h)).To(Equal(oracle.header.Values(h)), "%s: header %s", what, h)
		}
		Expect(metaHeaders(got.header)).To(Equal(metaHeaders(oracle.header)), "%s: the x-amz-meta-* headers", what)
		for _, name := range metaHeaders(oracle.header) {
			Expect(got.header.Values(name)).To(Equal(oracle.header.Values(name)), "%s: header %s", what, name)
		}
		if oracle.status >= 400 {
			Expect(string(withoutIDs(got.body))).To(Equal(string(withoutIDs(oracle.body))), "%s: the error document", what)
		} else {
			Expect(bytes.Equal(got.body, oracle.body)).To(BeTrue(),
				"%s: rgw-go's %d-byte body differs from radosgw's %d bytes", what, len(got.body), len(oracle.body))
		}
		return oracle
	}

	It("reads every populated object whole and by HEAD, byte-exact and header-exact", func(ctx SpecContext) {
		Expect(m.ObjectsIn("plain")).To(ContainElement(HaveField("Compression", Not(BeEmpty()))),
			"the manifest lists no compressed object: run make populate RELEASE=%s", m.Release)
		for _, o := range m.Objects {
			path := "/" + o.Bucket + "/" + o.Key
			oracle := compare(ctx, http.MethodGet, path, nil)
			Expect(oracle.status).To(Equal(http.StatusOK), path)
			Expect(oracle.body).To(HaveLen(int(o.Size)), path)
			if o.StorageClass != "" {
				Expect(oracle.header.Get("X-Amz-Storage-Class")).To(Equal(o.StorageClass), "%s carries its class", path)
			}
			compare(ctx, http.MethodHead, path, nil)
		}
	})

	It("reads every populated object through the AWS SDK alike", func(ctx SpecContext) {
		clients := map[string]*awss3.Client{"radosgw": sdkClient(rgw, m.Users[0]), "rgw-go": sdkClient(rgo, m.Users[0])}
		type read struct {
			body                            []byte
			etag, contentType, storageClass string
			contentLength                   int64
			metadata                        map[string]string
			headETag, headStorageClass      string
			headContentLength               int64
			headMetadata                    map[string]string
		}
		for _, o := range m.Objects {
			reads := map[string]read{}
			for name, c := range clients {
				got, err := c.GetObject(ctx, &awss3.GetObjectInput{Bucket: aws.String(o.Bucket), Key: aws.String(o.Key)})
				Expect(err).NotTo(HaveOccurred(), "%s: GetObject %s/%s", name, o.Bucket, o.Key)
				body, err := io.ReadAll(got.Body)
				Expect(err).NotTo(HaveOccurred())
				Expect(got.Body.Close()).To(Succeed())
				head, err := c.HeadObject(ctx, &awss3.HeadObjectInput{Bucket: aws.String(o.Bucket), Key: aws.String(o.Key)})
				Expect(err).NotTo(HaveOccurred(), "%s: HeadObject %s/%s", name, o.Bucket, o.Key)
				reads[name] = read{
					body: body, etag: aws.ToString(got.ETag), contentType: aws.ToString(got.ContentType),
					storageClass: string(got.StorageClass), contentLength: aws.ToInt64(got.ContentLength), metadata: got.Metadata,
					headETag: aws.ToString(head.ETag), headStorageClass: string(head.StorageClass),
					headContentLength: aws.ToInt64(head.ContentLength), headMetadata: head.Metadata,
				}
			}
			Expect(reads["rgw-go"]).To(Equal(reads["radosgw"]), "%s/%s", o.Bucket, o.Key)
			Expect(reads["radosgw"].body).To(HaveLen(int(o.Size)), "%s/%s", o.Bucket, o.Key)
		}
	})

	DescribeTable("reads ranges across every layout boundary",
		func(ctx SpecContext, key, rng string) {
			path := "/plain/" + key
			oracle := compare(ctx, http.MethodGet, path, map[string]string{"Range": rng})
			Expect(oracle.status).To(BeElementOf(http.StatusPartialContent, http.StatusRequestedRangeNotSatisfiable), "%s %s", path, rng)
			compare(ctx, http.MethodHead, path, map[string]string{"Range": rng})
		},
		Entry("head-full.bin: the last byte of a head that fills its chunk", "head-full.bin", "bytes=4194303-4194303"),
		Entry("large.bin: across the head/tail boundary", "large.bin", "bytes=4194300-4194310"),
		Entry("large.bin: inside the second tail stripe", "large.bin", "bytes=9000000-9000100"),
		Entry("large.bin: a suffix", "large.bin", "bytes=-1000"),
		Entry("large.bin: an open range from a tail stripe", "large.bin", "bytes=8388608-"),
		Entry("large.bin: past the end is 416", "large.bin", "bytes=20000000-"),
		Entry("large.bin: an inverted range", "large.bin", "bytes=10-5"),
		Entry("multipart.bin: across a part boundary", "multipart.bin", "bytes=8388600-8388620"),
		Entry("multipart.bin: the last part", "multipart.bin", "bytes=16777216-"),
		Entry("empty.bin: any range", "empty.bin", "bytes=0-1"),
		Entry("_underscore.bin: a locator object by range", "_underscore.bin", "bytes=4-9"),
		Entry("comp-zlib.bin: inside the first compression block", "comp-zlib.bin", "bytes=1000-2000"),
		Entry("comp-snappy.bin: the middle of its one block", "comp-snappy.bin", "bytes=500000-600000"),
		Entry("comp-zstd.bin: a suffix", "comp-zstd.bin", "bytes=-100"),
		Entry("comp-lz4.bin: the last byte", "comp-lz4.bin", "bytes=1048575-1048575"),
	)

	It("answers the conditionals as radosgw does", func(ctx SpecContext) {
		head := raw.do(ctx, rgw, http.MethodHead, "/plain/small.bin", nil)
		Expect(head.status).To(Equal(http.StatusOK))
		etag, lm := head.header.Get("ETag"), head.header.Get("Last-Modified")
		Expect(etag).To(HavePrefix(`"`))
		// radosgw compares If-Match and If-None-Match as prefixes of the
		// unquoted value, so the stored ETag followed by anything matches,
		// and a list matches only through its first entity tag.
		prefixed := strings.TrimSuffix(etag, `"`) + `garbage"`
		listed := `"ABCORZ", ` + etag
		for _, h := range []map[string]string{
			{"If-Match": etag},
			{"If-Match": `"ABCORZ"`},
			{"If-Match": "*"},
			{"If-Match": prefixed},
			{"If-Match": listed},
			{"If-None-Match": etag},
			{"If-None-Match": `"ABCORZ"`},
			{"If-None-Match": "*"},
			{"If-None-Match": prefixed},
			{"If-None-Match": listed},
			{"If-Modified-Since": lm},
			{"If-Modified-Since": "Sat, 29 Oct 1994 19:43:31 GMT"},
			{"If-Modified-Since": "yesterday"},
			{"If-Unmodified-Since": lm},
			{"If-Unmodified-Since": "Sat, 29 Oct 1994 19:43:31 GMT"},
			{"If-Unmodified-Since": "Sat, 29 Oct 2100 19:43:31 GMT"},
			{"If-None-Match": `"ABCORZ"`, "If-Modified-Since": "Sat, 29 Oct 2100 19:43:31 GMT"},
			{"If-Match": etag, "If-Unmodified-Since": "Sat, 29 Oct 1994 19:43:31 GMT"},
		} {
			oracle := compare(ctx, http.MethodGet, "/plain/small.bin", h)
			note("GET /plain/small.bin %v: radosgw answers %d", h, oracle.status)
		}
	})

	It("serves multipart parts by partNumber and refuses the rest as radosgw does", func(ctx SpecContext) {
		for _, n := range []string{"1", "2", "3", "4", "x", "0"} {
			oracle := compare(ctx, http.MethodGet, "/plain/multipart.bin?partNumber="+n, nil)
			note("GET /plain/multipart.bin?partNumber=%s: radosgw answers %d, ETag %s, x-amz-mp-parts-count %q",
				n, oracle.status, oracle.header.Get("ETag"), oracle.header.Get("X-Amz-Mp-Parts-Count"))
			compare(ctx, http.MethodHead, "/plain/multipart.bin?partNumber="+n, nil)
		}
		compare(ctx, http.MethodGet, "/plain/small.bin?partNumber=1", nil)
		compare(ctx, http.MethodGet, "/plain/small.bin?partNumber=2", nil)
	})

	It("answers the response-* overrides and the metadata object alike", func(ctx SpecContext) {
		compare(ctx, http.MethodGet, "/plain/meta.bin?response-content-type=foo/bar&response-cache-control=no-cache"+
			"&response-content-disposition=bla&response-content-encoding=aaa&response-content-language=esperanto&response-expires=123", nil)
		oracle := compare(ctx, http.MethodGet, "/plain/meta.bin", nil)
		Expect(oracle.header.Get("X-Amz-Meta-Color")).To(Equal("blue"))
	})

	It("reads tags and the object ACL alike", func(ctx SpecContext) {
		compare(ctx, http.MethodGet, "/plain/small.bin?tagging", nil)
		compare(ctx, http.MethodGet, "/plain/small.bin?acl", nil)
	})

	It("serves GetObjectAttributes: as radosgw on Tentacle, and on Squid as a Tentacle radosgw would", func(ctx SpecContext) {
		path := "/plain/multipart.bin?attributes"
		hdr := map[string]string{"X-Amz-Object-Attributes": "ETag,Checksum,ObjectParts,StorageClass,ObjectSize", "X-Amz-Max-Parts": "2"}
		got := raw.do(ctx, rgo, http.MethodGet, path, hdr)
		Expect(got.status).To(Equal(http.StatusOK), "rgw-go's body %s", got.body)
		Expect(string(got.body)).To(ContainSubstring("<PartsCount>3</PartsCount>"))
		Expect(string(got.body)).To(ContainSubstring("<ObjectSize>20971520</ObjectSize>"))
		if release == denc.Tentacle {
			oracle := raw.do(ctx, rgw, http.MethodGet, path, hdr)
			Expect(got.status).To(Equal(oracle.status))
			Expect(string(got.body)).To(Equal(string(oracle.body)))
		}
	})

	It("costs one RADOS read op for a signed GET within the head, and counts the ops of the reads beyond it", func(ctx SpecContext) {
		get := func(path string, hdr map[string]string, wantStatus, wantLen int) func() {
			return func() {
				GinkgoHelper()
				got := raw.do(ctx, rgo, http.MethodGet, path, hdr)
				Expect(got.status).To(Equal(wantStatus), "GET %s %v", path, hdr)
				Expect(got.body).To(HaveLen(wantLen), "GET %s %v", path, hdr)
			}
		}
		head := func(path string) func() {
			return func() {
				GinkgoHelper()
				Expect(raw.do(ctx, rgo, http.MethodHead, path, nil).status).To(Equal(http.StatusOK), "HEAD %s", path)
			}
		}
		Expect(readOpsOf(ctx, met, get("/plain/head-full.bin", nil, http.StatusOK, 4<<20))).To(BeEquivalentTo(1),
			"a GET of an object its head holds whole: getxattrs, stat and the 4 MiB read in one op")
		Expect(readOpsOf(ctx, met, head("/plain/large.bin"))).To(BeEquivalentTo(1), "a HEAD: getxattrs and stat in one op")
		Expect(readOpsOf(ctx, met, get("/plain/large.bin", nil, http.StatusOK, 10<<20))).To(BeEquivalentTo(3),
			"a GET of 10 MiB: the head op with the first 4 MiB, then two tail reads")
		Expect(readOpsOf(ctx, met, get("/plain/large.bin", map[string]string{"Range": "bytes=0-9"}, http.StatusPartialContent, 10))).
			To(BeEquivalentTo(2), "a ranged GET: the head op without a prefetch, then the read")
	})
})

// brief returns body for a failure message: an XML document whole, anything
// else as its length.
func brief(body []byte) string {
	if bytes.HasPrefix(body, []byte("<?xml")) {
		return string(body)
	}
	return fmt.Sprintf("<%d bytes>", len(body))
}

// requestIDs matches the RequestId and HostId of an error document. Both end
// in the gateway process's librados instance id, which differs between the
// two gateways, and between runs of either, by design.
var requestIDs = regexp.MustCompile(`<(RequestId|HostId)>[^<]*</(RequestId|HostId)>`)

// withoutIDs returns an error document with its RequestId and HostId emptied.
func withoutIDs(body []byte) []byte {
	return requestIDs.ReplaceAll(body, []byte("<$1></$2>"))
}
