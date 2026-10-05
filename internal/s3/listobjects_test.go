package s3_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/acl"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/s3"
)

// listedAt is every listed object's mtime.
var listedAt = time.Date(2026, 9, 27, 1, 2, 3, 456_000_000, time.UTC)

const (
	listHead = `<?xml version="1.0" encoding="UTF-8"?>` + `<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`
	aliceTag = `<Owner><ID>alice</ID><DisplayName>Alice</DisplayName></Owner>`
)

var _ = Describe("list_bucket and list_bucket_v2", func() {
	var (
		store *memstore.Store
		alice *op.UserRecord
		h     *s3.Handler
		srv   *httptest.Server
	)
	put := func(ctx context.Context, rec *op.BucketRecord, name string) {
		GinkgoHelper()
		e := denc.NewEncoder()
		acl.Policy{Owner: acl.Owner{ID: "alice", DisplayName: "Alice"}}.Encode(e, denc.Squid)
		_, err := store.PutObject(ctx, rec, meta.ObjKey{Name: name}, strings.NewReader("v"),
			op.PutParams{Size: 1, ETag: "e1", Mtime: listedAt, Attrs: map[string][]byte{meta.AttrACL: e.Bytes()}})
		Expect(err).NotTo(HaveOccurred())
	}
	setup := func(ctx context.Context, rel denc.Release, names ...string) {
		store = memstore.New(memstore.Config{Release: rel})
		alice = store.AddUser(meta.UserInfo{UserID: meta.UserID{ID: "alice"}, DisplayName: "Alice", OpMask: op.OpTypeAll})
		rec, err := store.CreateBucket(ctx, op.CreateBucketParams{Name: "plain", Owner: meta.UserOwner(alice.Info.UserID)})
		Expect(err).NotTo(HaveOccurred())
		for _, n := range names {
			put(ctx, rec, n)
		}
		h = newHandler(store, authAs(alice), s3.Config{})
		srv = httptest.NewServer(h)
		DeferCleanup(srv.Close)
	}
	BeforeEach(func(ctx SpecContext) { setup(ctx, denc.Squid, "a", "dir/x", "_under", "z") })
	get := func(target string) *httptest.ResponseRecorder {
		return serveReq(h, http.MethodGet, target, nil)
	}

	It("renders v1 in radosgw's element order", func() {
		rec := get("/plain?delimiter=/&max-keys=1")
		Expect(rec.Code).To(Equal(200))
		Expect(rec.Body.String()).To(Equal(listHead+
			`<Name>plain</Name><Prefix></Prefix><MaxKeys>1</MaxKeys><Delimiter>/</Delimiter><IsTruncated>true</IsTruncated>`+
			`<Contents><Key>_under</Key><LastModified>2026-09-27T01:02:03.456Z</LastModified><ETag>&quot;e1&quot;</ETag><Size>1</Size><StorageClass>STANDARD</StorageClass>`+
			aliceTag+`<Type>Normal</Type></Contents>`+
			`<Marker></Marker><NextMarker>_under</NextMarker></ListBucketResult>`),
			"NextMarker is the parsed name, rgw_rest_s3.cc:1962-1965")
	})
	It("renders common prefixes before contents and url-encodes when asked", func() {
		body := get("/plain?delimiter=/&encoding-type=url&prefix=").Body.String()
		Expect(body).To(ContainSubstring(`<EncodingType>url</EncodingType><Name>plain</Name>`))
		Expect(body).To(MatchRegexp(`<CommonPrefixes><Prefix>dir/</Prefix></CommonPrefixes><Contents>`), "prefixes come from send_common_response, before Contents")
		body = get("/plain?encoding-type=URL&prefix=dir/&max-keys=1").Body.String()
		Expect(body).To(ContainSubstring(`<Prefix>dir/</Prefix>`), "Prefix is a dump_string, never encoded, :1878")
		Expect(body).To(ContainSubstring(`<Contents><Key>dir%2Fx</Key>`), "a key's slash is encoded, :1940")
		Expect(body).NotTo(ContainSubstring("<NextMarker>"), "the listing ends")
	})
	It("renders v2 with KeyCount, the tokens and Owner only when fetched", func() {
		body := get("/plain?list-type=2&max-keys=2&start-after=_under&fetch-owner=true").Body.String()
		Expect(body).To(ContainSubstring(`<Contents><Key>a</Key>`))
		Expect(body).To(ContainSubstring(`<Owner><ID>alice</ID>`))
		Expect(body).To(MatchRegexp(`<NextContinuationToken>dir/x</NextContinuationToken><KeyCount>2</KeyCount><StartAfter>_under</StartAfter></ListBucketResult>$`))
		body = get("/plain?list-type=2&continuation-token=a").Body.String()
		Expect(body).NotTo(ContainSubstring("<Owner>"))
		Expect(body).To(ContainSubstring(`<ContinuationToken>a</ContinuationToken>`))
		Expect(body).To(ContainSubstring(`<Contents><Key>dir/x</Key>`), "the token beats start-after as the marker")
		Expect(body).NotTo(ContainSubstring("<StartAfter>"))
	})
	It("lists the versions of a never-versioned bucket as the null versions", func() {
		body := get("/plain?versions&max-keys=1&key-marker=_under&version-id-marker=v1").Body.String()
		Expect(body).To(Equal(`<?xml version="1.0" encoding="UTF-8"?>`+
			`<ListVersionsResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`+
			`<Name>plain</Name><Prefix></Prefix><MaxKeys>1</MaxKeys><IsTruncated>true</IsTruncated>`+
			`<KeyMarker>_under</KeyMarker><VersionIdMarker>v1</VersionIdMarker>`+
			`<NextKeyMarker>a</NextKeyMarker><NextVersionIdMarker>null</NextVersionIdMarker>`+
			`<Version><Key>a</Key><VersionId>null</VersionId><IsLatest>true</IsLatest><LastModified>2026-09-27T01:02:03.456Z</LastModified>`+
			`<ETag>&quot;e1&quot;</ETag><Size>1</Size><StorageClass>STANDARD</StorageClass>`+aliceTag+`<Type>Normal</Type></Version>`+
			`</ListVersionsResult>`), "send_versioned_response, rgw_rest_s3.cc:1799-1869")
	})
	It("renders a v2 versions listing as radosgw does", func() {
		body := get("/plain?list-type=2&versions&delimiter=/&start-after=b&encoding-type=url").Body.String()
		Expect(body).To(Equal(`<?xml version="1.0" encoding="UTF-8"?>`+
			`<ListVersionsResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`+
			`<Name>plain</Name><Prefix></Prefix><MaxKeys>1000</MaxKeys><Delimiter>/</Delimiter><IsTruncated>false</IsTruncated>`+
			`<CommonPrefixes><Prefix>dir/</Prefix></CommonPrefixes>`+
			`<KeyContinuationToken>b</KeyContinuationToken><VersionIdContinuationToken></VersionIdContinuationToken>`+
			`<EncodingType>url</EncodingType>`+
			`<Version><Key>z</Key><VersionId>null</VersionId><IsLatest>true</IsLatest><LastModified>2026-09-27T01:02:03.456Z</LastModified>`+
			`<ETag>&quot;e1&quot;</ETag><Size>1</Size><StorageClass>STANDARD</StorageClass></Version>`+
			`<CommonPrefixes><Prefix>dir/</Prefix><KeyCount>1</KeyCount><StartAfter>b</StartAfter></CommonPrefixes>`+
			`</ListVersionsResult>`), "RGWListBucket_ObjStore_S3v2::send_versioned_response, rgw_rest_s3.cc:1970-2051")
	})
	It("cuts LastModified to whole seconds on Tentacle", func(ctx SpecContext) {
		setup(ctx, denc.Tentacle, "a")
		Expect(get("/plain").Body.String()).To(ContainSubstring(`<LastModified>2026-09-27T01:02:03.000Z</LastModified>`),
			"dump_time_exact_seconds, rgw_rest_s3.cc:2052 at v20.2.4")
	})
	It("escapes keys as radosgw's formatter does", func(ctx SpecContext) {
		setup(ctx, denc.Squid, `a&b<c`)
		Expect(get("/plain").Body.String()).To(ContainSubstring(`<Key>a&amp;b&lt;c</Key>`))
	})
	It("answers 400 InvalidArgument for a non-numeric max-keys and for an unordered listing with a delimiter", func() {
		rec := get("/plain?max-keys=x")
		Expect(rec.Code).To(Equal(400))
		Expect(rec.Body.String()).To(ContainSubstring("<Code>InvalidArgument</Code>"))
		Expect(get("/plain?allow-unordered=true&delimiter=/").Code).To(Equal(400), "rgw_op.cc:3085-3090: -EINVAL")
		Expect(get("/plain?allow-unordered=TRUE").Code).To(Equal(200), "get_bool compares case-insensitively")
	})
	It("answers 404 NoSuchBucket for a missing bucket", func() {
		rec := get("/missing?list-type=2")
		Expect(rec.Code).To(Equal(404))
		Expect(rec.Body.String()).To(ContainSubstring("<Code>NoSuchBucket</Code>"))
	})
	It("frames v1 and v2 chunked as radosgw's send_response does: no Content-Length and no Accept-Ranges", func(ctx SpecContext) {
		for _, target := range []string{"/plain", "/plain?list-type=2"} {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+target, nil)
			Expect(err).NotTo(HaveOccurred())
			resp, err := http.DefaultClient.Do(req)
			Expect(err).NotTo(HaveOccurred())
			b, err := io.ReadAll(resp.Body)
			Expect(resp.Body.Close()).To(Succeed())
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(200), target)
			Expect(resp.TransferEncoding).To(Equal([]string{"chunked"}), "end_header(..., CHUNKED_TRANSFER_ENCODING), rgw_rest_s3.cc:1906 and :2062")
			Expect(resp.ContentLength).To(BeEquivalentTo(-1), target)
			Expect(resp.Header.Get("Accept-Ranges")).To(BeEmpty(), "no Content-Length, so no dump_content_length")
			Expect(resp.Header.Get("Content-Type")).To(Equal("application/xml"), target)
			Expect(string(b)).To(HavePrefix(listHead), target)
		}
	})
	It("answers a failed listing with an ordinary error document", func(ctx SpecContext) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/plain?max-keys=x", nil)
		Expect(err).NotTo(HaveOccurred())
		resp, err := http.DefaultClient.Do(req)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.Body.Close()).To(Succeed())
		Expect(resp.StatusCode).To(Equal(400))
		Expect(resp.TransferEncoding).To(BeEmpty(), "end_header's error branch sends the document with a Content-Length, rgw_rest.cc:620-624")
		Expect(resp.Header.Get("Accept-Ranges")).To(Equal("bytes"))
	})
})
