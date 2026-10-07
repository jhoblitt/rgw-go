package s3_test

import (
	"net/http"
	"net/url"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cephconf"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/s3"
)

var _ = Describe("requestAttrs", func() {
	var conf cephconf.MapGetter
	BeforeEach(func() { conf = cephconf.MapGetter{} })
	req := func(h http.Header, q url.Values) *op.Request {
		if q == nil {
			q = url.Values{}
		}
		return &op.Request{Header: h, Query: q, Env: &op.Env{Conf: cephconf.NewOptions(conf)}}
	}

	It("maps the generic headers and every header under a meta prefix, NUL-terminated", func() {
		attrs, err := s3.RequestAttrsForTest(req(http.Header{
			"Content-Type": {"text/plain"}, "Content-Language": {"en"}, "Expires": {"Thu, 01 Jan 2026 00:00:00 GMT"},
			"Cache-Control": {"no-cache"}, "Content-Disposition": {"inline"}, "Content-Encoding": {"gzip"}, "X-Robots-Tag": {"noindex"},
			"X-Amz-Meta-Foo": {"bar"}, "X-Amz-Acl": {"private"}, "X-Goog-Meta-Baz": {"q"}, "X-Rgw-Thing": {"r"},
			"X-Object-Meta-O": {"o"}, "X-Container-Meta-C": {"c"}, "X-Account-Meta-A": {"a"}, "X-Dho-D": {"d"},
			"Authorization": {"AWS4-HMAC-SHA256 secret"}, "Host": {"h"},
		}, nil), true)
		Expect(err).NotTo(HaveOccurred())
		Expect(attrs).To(Equal(map[string][]byte{
			meta.AttrContentType: []byte("text/plain\x00"), meta.AttrContentLang: []byte("en\x00"),
			meta.AttrExpires: []byte("Thu, 01 Jan 2026 00:00:00 GMT\x00"), meta.AttrCacheControl: []byte("no-cache\x00"),
			meta.AttrContentDisp: []byte("inline\x00"), meta.AttrContentEnc: []byte("gzip\x00"), meta.AttrXRobotsTag: []byte("noindex\x00"),
			"user.rgw.x-amz-meta-foo": []byte("bar\x00"), "user.rgw.x-amz-acl": []byte("private\x00"),
			"user.rgw.x-amz-meta-baz": []byte("q\x00"), "user.rgw.x-amz-thing": []byte("r\x00"),
			"user.rgw.x-amz-meta-o": []byte("o\x00"), "user.rgw.x-amz-meta-c": []byte("c\x00"), "user.rgw.x-amz-meta-a": []byte("a\x00"),
			"user.rgw.x-amz-d": []byte("d\x00"),
		}), "rgw_common.cc:413-464 and rgw_op.h:2171-2233 at v19.2.6: each prefix becomes x-amz-")
	})
	It("stores none of the credentials, digests, dates and storage class v20.2.4 lists, nor the session token, on either release", func() {
		attrs, err := s3.RequestAttrsForTest(req(http.Header{
			"X-Amz-Server-Side-Encryption-Customer-Algorithm": {"AES256"}, "X-Amz-Server-Side-Encryption-Customer-Key": {"k"},
			"X-Amz-Server-Side-Encryption-Customer-Key-Md5":               {"m"},
			"X-Amz-Copy-Source-Server-Side-Encryption-Customer-Algorithm": {"AES256"}, "X-Amz-Copy-Source-Server-Side-Encryption-Customer-Key": {"k"},
			"X-Amz-Copy-Source-Server-Side-Encryption-Customer-Key-Md5": {"m"},
			"X-Amz-Storage-Class": {"COLD"}, "X-Amz-Content-Sha256": {"UNSIGNED-PAYLOAD"}, "X-Amz-Checksum-Algorithm": {"CRC32"},
			"X-Amz-Date": {"20260928T120000Z"}, "X-Amz-Security-Token": {"token"},
		}, nil), true)
		Expect(err).NotTo(HaveOccurred())
		Expect(attrs).To(BeEmpty(), "rgw_op.h:2344-2357 at v20.2.4; v19.2.6 lists only the first three and the storage class")
	})
	It("stores no name that asks for encryption, under any meta prefix", func() {
		attrs, err := s3.RequestAttrsForTest(req(http.Header{
			"X-Rgw-Server-Side-Encryption": {"AES256"}, "X-Goog-Server-Side-Encryption-Customer-Key": {"k"},
			"X-Amz-Server-Side-Encryption-Bucket-Key-Enabled": {"true"}, "X-Object-Server-Side-Encryptionx": {"v"},
			"X-Account-Copy-Source-Server-Side-Encryption-Customer-Key": {"k"},
		}, nil), true)
		Expect(err).NotTo(HaveOccurred())
		Expect(attrs).To(BeEmpty())
	})
	It("takes a repeated header's last value, as RGWEnv keeps it", func() {
		attrs, err := s3.RequestAttrsForTest(req(http.Header{"X-Amz-Meta-A": {"1", "2"}}, nil), true)
		Expect(err).NotTo(HaveOccurred())
		Expect(attrs).To(Equal(map[string][]byte{"user.rgw.x-amz-meta-a": []byte("2\x00")}))
	})
	It("joins the values of names that meet, header before query, trimming the earlier value's trailing white space", func() {
		attrs, err := s3.RequestAttrsForTest(req(
			http.Header{"X-Amz-Meta-Foo": {"a "}, "X-Goog-Meta-Foo": {"b\t"}},
			url.Values{"x-amz-meta-foo": {"c"}, "X-AMZ-META-Q": {"v"}, "other": {"x"}},
		), true)
		Expect(err).NotTo(HaveOccurred())
		Expect(attrs).To(Equal(map[string][]byte{
			"user.rgw.x-amz-meta-foo": []byte("a,b,c\x00"), "user.rgw.x-amz-meta-q": []byte("v\x00"),
		}), "init_meta_info's join in the environment's order, then map_qs_metadata, rgw_rest_s3.cc:2581-2595")
	})
	It("leaves the query string out for a copy, which reads no map_qs_metadata", func() {
		attrs, err := s3.RequestAttrsForTest(req(http.Header{}, url.Values{"x-amz-meta-q": {"v"}}), false)
		Expect(err).NotTo(HaveOccurred())
		Expect(attrs).To(BeEmpty())
	})
	It("keeps a name's underscores and stores a generic header sent empty", func() {
		attrs, err := s3.RequestAttrsForTest(req(http.Header{"X-Amz-Meta-A_b": {"v"}, "Content-Type": {""}}, nil), true)
		Expect(err).NotTo(HaveOccurred())
		Expect(attrs).To(Equal(map[string][]byte{"user.rgw.x-amz-meta-a_b": []byte("v\x00"), meta.AttrContentType: []byte("\x00")}))
	})
	It("stores each rgw_extended_http_attrs header as its attr", func() {
		conf["rgw_extended_http_attrs"] = "x-foo, Bar_Baz"
		attrs, err := s3.RequestAttrsForTest(req(http.Header{"X-Foo": {"1"}, "Bar-Baz": {"2"}}, nil), true)
		Expect(err).NotTo(HaveOccurred())
		Expect(attrs).To(Equal(map[string][]byte{"user.rgw.x_foo": []byte("1\x00"), "user.rgw.bar_baz": []byte("2\x00")}),
			"rgw_rest.cc:195-209 at v19.2.6")
	})
	It("encodes a value format_xattr encodes", func() {
		attrs, err := s3.RequestAttrsForTest(req(http.Header{"X-Amz-Meta-K": {"caf\xe9"}}, nil), true)
		Expect(err).NotTo(HaveOccurred())
		Expect(attrs).To(Equal(map[string][]byte{"user.rgw.x-amz-meta-k": []byte("=?UTF-8?Q?caf=E9?=\x00")}))
	})
	DescribeTable("formatXattr is format_xattr over mime_encode_as_qp",
		func(in, want string) { Expect(s3.FormatXattrForTest(in)).To(Equal(want)) },
		Entry("invalid UTF-8", "caf\xe9", "=?UTF-8?Q?caf=E9?="),
		Entry("a tab, a control character", "a\tb", "=?UTF-8?Q?a=09b?="),
		Entry("DEL", "a\x7fb", "=?UTF-8?Q?a=7Fb?="),
		Entry("valid UTF-8 with an equals sign stays", "plain = text", "plain = text"),
		Entry("valid multibyte UTF-8 stays", "café", "café"),
		Entry("a control character encodes every high-bit byte and equals sign", "é=\n", "=?UTF-8?Q?=C3=A9=3D=0A?="),
		Entry("a NUL alone is no control character", "a\x00b", "a\x00b"),
		Entry("the encoding stops at a NUL", "\n\x00tail", "=?UTF-8?Q?=0A?="),
		Entry("a surrogate is not UTF-8", "\xed\xa0\x80", "=?UTF-8?Q?=ED=A0=80?="),
	)
	Describe("the configured limits", func() {
		It("refuses an attr name over rgw_max_attr_name_len with radosgw's answer to ENAMETOOLONG", func() {
			conf["rgw_max_attr_name_len"] = "30"
			_, err := s3.RequestAttrsForTest(req(http.Header{"X-Amz-Meta-Rather-Long-Name": {"v"}}, nil), true)
			Expect(err).To(MatchError(op.ErrMetadataNameTooLong))
			_, err = s3.RequestAttrsForTest(req(http.Header{"X-Amz-Meta-Short": {"v"}}, nil), true)
			Expect(err).NotTo(HaveOccurred(), "user.rgw.x-amz-meta-short is 26 bytes")
		})
		It("refuses a value over rgw_max_attr_size, measured once encoded, as UnknownError", func() {
			conf["rgw_max_attr_size"] = "3"
			_, err := s3.RequestAttrsForTest(req(http.Header{"X-Amz-Meta-K": {"abcd"}}, nil), true)
			Expect(err).To(MatchError(op.ErrUnknown))
			_, err = s3.RequestAttrsForTest(req(http.Header{"X-Amz-Meta-K": {"\x01"}}, nil), true)
			Expect(err).To(MatchError(op.ErrUnknown), "=?UTF-8?Q?=01?= is 15 bytes")
			_, err = s3.RequestAttrsForTest(req(http.Header{"X-Amz-Meta-K": {"abc"}, "Content-Type": {"long/type"}}, nil), true)
			Expect(err).NotTo(HaveOccurred(), "a generic attr is not limited")
		})
		It("refuses more attrs than rgw_max_attrs_num_in_req as UnknownError, the blocklisted not counted", func() {
			conf["rgw_max_attrs_num_in_req"] = "1"
			_, err := s3.RequestAttrsForTest(req(http.Header{"X-Amz-Meta-A": {"1"}, "X-Amz-Meta-B": {"2"}}, nil), true)
			Expect(err).To(MatchError(op.ErrUnknown))
			_, err = s3.RequestAttrsForTest(req(http.Header{"X-Amz-Meta-A": {"1"}, "X-Amz-Date": {"d"}, "X-Amz-Storage-Class": {"c"}}, nil), true)
			Expect(err).NotTo(HaveOccurred())
		})
		It("puts no header value in its error", func() {
			conf["rgw_max_attr_size"] = "3"
			_, err := s3.RequestAttrsForTest(req(http.Header{"X-Amz-Meta-Secretname": {"secretvalue"}}, nil), true)
			Expect(err.Error()).NotTo(ContainSubstring("secret"))
		})
	})
})
