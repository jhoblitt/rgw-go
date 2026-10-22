package s3

import (
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

var _ = Describe("htmlErrorPage", func() {
	request := func(bucket, id string) *op.Request {
		return &op.Request{ID: id, Bucket: bucket, Env: &op.Env{HostID: "4155-z-zg"}}
	}

	It("is the page radosgw's pretty HTMLFormatter writes, ending where its Content-Length does", func() {
		got := htmlErrorPage(request("site", "tx-1"), 404, "NoSuchKey", "")
		want := "<html>\n" +
			" <head><title>404 Not Found</title></head>\n" +
			" <body>\n" +
			"  <h1>404 Not Found</h1>\n" +
			"  <ul>\n" +
			"   <li>Code: NoSuchKey</li>\n" +
			"   <li>Message: </li>\n" +
			"   <li>BucketName: site</li>\n" +
			"   <li>RequestId: tx-1</li>\n" +
			"   <li>HostId: 4155-z-zg</li>\n" +
			"  </ul>\n" +
			" </body>\n" +
			"</html>\n"
		Expect(string(got)).To(Equal(want),
			"output_header, HTMLFormatter.cc:69-91, then dump at rgw_common.cc:381-404 and output_footer, indented by the open sections, v19.2.7")
	})

	It("leaves out an empty Code, BucketName and RequestId, and keeps an empty Message and HostId", func() {
		r := request("", "")
		r.Env = nil
		got := htmlErrorPage(r, 301, "", "")
		Expect(string(got)).To(Equal("<html>\n <head><title>301 Moved Permanently</title></head>\n <body>\n  <h1>301 Moved Permanently</h1>\n  <ul>\n" +
			"   <li>Message: </li>\n   <li>HostId: </li>\n  </ul>\n </body>\n</html>\n"))
	})

	DescribeTable("titles the page with the status and radosgw's name for it",
		func(status int, title string) {
			Expect(string(htmlErrorPage(request("b", "t"), status, "C", ""))).To(HavePrefix("<html>\n <head><title>"+title+"</title></head>\n <body>\n  <h1>"+title+"</h1>\n"), "status %d", status)
		},
		Entry("a name Go spells otherwise", 503, "503 Slow Down"),
		Entry("radosgw's own status", 498, "498 Rate Limited"),
		Entry("a status http_codes lacks, named by the number alone", 423, "423"),
	)

	It("escapes each value as xml_stream_escaper does and writes the names as they are", func() {
		got := htmlErrorPage(request("b&<>", "t'\""), 400, "InvalidArgument", "a\rb")
		Expect(string(got)).To(ContainSubstring("   <li>Code: InvalidArgument</li>\n   <li>Message: a&#x0d;b</li>\n" +
			"   <li>BucketName: b&amp;&lt;&gt;</li>\n   <li>RequestId: t&apos;&quot;</li>\n"))
	})

	It("sends no x-amz-request-charged, which end_header adds only to a success", func() {
		r := request("site", "tx-1")
		r.BucketRec = &op.BucketRecord{Info: meta.BucketInfo{Owner: meta.UserOwner(meta.UserID{ID: "alice"}), RequesterPays: true}}
		r.Identity = op.Identity{User: &meta.UserInfo{UserID: meta.UserID{ID: "bob"}}}
		charged := httptest.NewRecorder()
		SetCommonHeaders(charged, r)
		Expect(charged.Header()).To(HaveKeyWithValue("x-amz-request-charged", []string{"requester"}),
			"a requester-pays bucket read by another user, rgw_rest.cc:597-601 at v19.2.7")
		rec := httptest.NewRecorder()
		writeHTMLError(rec, r, 404, "NoSuchKey", "")
		Expect(rec.Code).To(Equal(404))
		Expect(rec.Header()).NotTo(HaveKey("x-amz-request-charged"))
	})
})
