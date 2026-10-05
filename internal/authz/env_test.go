package authz_test

import (
	"net/http"
	"net/url"
	"strconv"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/authz"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

var envClock = time.Date(2026, 9, 27, 12, 0, 0, 500_000_000, time.UTC)

func envConfig(r denc.Release) authz.Config {
	cfg := authz.DefaultConfig(r)
	cfg.Now = func() time.Time { return envClock }
	return cfg
}

// envRequest is a request from alice at 10.1.2.3:5555; hdr is header name,
// value pairs, added in order, and query a raw query string.
func envRequest(method, bucket, key, query string, hdr ...string) *op.Request {
	q, err := url.ParseQuery(query)
	Expect(err).NotTo(HaveOccurred())
	h := http.Header{}
	for i := 0; i+1 < len(hdr); i += 2 {
		h.Add(hdr[i], hdr[i+1])
	}
	return &op.Request{
		Method:     method,
		Header:     h,
		Query:      q,
		RawQuery:   query,
		RemoteAddr: "10.1.2.3:5555",
		Bucket:     bucket,
		Object:     meta.ObjKey{Name: key},
		Identity:   identityOf(aliceInfo()),
	}
}

// baseFor is the base keys of an alice request with no optional header.
func baseFor(subuser string) [][2]string {
	return [][2]string{
		{"aws:CurrentTime", "1790510400"},
		{"aws:EpochTime", "2026-09-27T12:00:00.500000000Z"},
		{"aws:PrincipalType", "User"},
		{"aws:SourceIp", "10.1.2.3"},
		{"aws:username", "alice"},
		{"rgw:subuser", subuser},
		{"sts:authentication", "false"},
	}
}

func with(base [][2]string, kv ...string) [][2]string {
	out := append([][2]string{}, base...)
	for i := 0; i+1 < len(kv); i += 2 {
		out = append(out, [2]string{kv[i], kv[i+1]})
	}
	return out
}

func pairsOf(cfg authz.Config, r *op.Request, a policy.Action) [][2]string {
	pairs, _ := authz.EnvPairs(cfg, r, a)
	return pairs
}

func mustParse(text string) *policy.Policy {
	GinkgoHelper()
	p, err := policy.Parse(text, policy.ParseOptions{Release: denc.Squid})
	Expect(err).NotTo(HaveOccurred())
	return p
}

func sourceIP(cfg authz.Config, r *op.Request) []string {
	env := authz.BuildEnv(cfg, r, policy.S3GetObject)
	return env.Lookup("aws:SourceIp")
}

var _ = Describe("BuildEnv", func() {
	var cfg authz.Config

	BeforeEach(func() {
		cfg = envConfig(denc.Squid)
	})

	Describe("the base keys", func() {
		It("are rgw_build_iam_environment's, in its order", func() {
			r := envRequest(http.MethodGet, "b", "o", "",
				"Referer", "http://example.com/", "User-Agent", "aws-cli")
			r.TLS = true
			r.Identity.SubUser = "ro"
			Expect(pairsOf(cfg, r, policy.S3GetObject)).To(Equal([][2]string{
				{"aws:CurrentTime", "1790510400"},
				{"aws:EpochTime", "2026-09-27T12:00:00.500000000Z"},
				{"aws:PrincipalType", "User"},
				{"aws:Referer", "http://example.com/"},
				{"aws:SecureTransport", "true"},
				{"aws:SourceIp", "10.1.2.3"},
				{"aws:UserAgent", "aws-cli"},
				{"aws:username", "alice"},
				{"rgw:subuser", "ro"},
				{"sts:authentication", "false"},
			}))
		})

		It("keep an empty subuser and leave out absent headers", func() {
			r := envRequest(http.MethodGet, "b", "o", "")
			Expect(pairsOf(cfg, r, policy.S3GetObject)).To(Equal(baseFor("")))
		})

		It("take a header present but empty, and the last of a repeated one", func() {
			r := envRequest(http.MethodGet, "b", "o", "",
				"Referer", "", "User-Agent", "first", "User-Agent", "last")
			env := authz.BuildEnv(cfg, r, policy.S3GetObject)
			Expect(env.Lookup("aws:Referer")).To(Equal([]string{""}))
			Expect(env.Lookup("aws:UserAgent")).To(Equal([]string{"last"}))
		})

		It("mark a request carrying a security token", func() {
			r := envRequest(http.MethodGet, "b", "o", "", "X-Amz-Security-Token", "tok")
			env := authz.BuildEnv(cfg, r, policy.S3GetObject)
			Expect(env.Lookup("sts:authentication")).To(Equal([]string{"true"}))
		})

		It("name the anonymous user", func() {
			r := envRequest(http.MethodGet, "b", "o", "")
			r.Identity = op.Anonymous()
			env := authz.BuildEnv(cfg, r, policy.S3GetObject)
			Expect(env.Lookup("aws:username")).To(Equal([]string{"anonymous"}))
			Expect(env.Lookup("rgw:subuser")).To(Equal([]string{""}))
		})

		It("take the time from time.Now without a clock", func() {
			cfg.Now = nil
			before := time.Now().Unix()
			env := authz.BuildEnv(cfg, envRequest(http.MethodGet, "b", "o", ""), policy.S3GetObject)
			got := env.Lookup("aws:CurrentTime")
			Expect(got).To(HaveLen(1))
			secs, err := strconv.ParseInt(got[0], 10, 64)
			Expect(err).NotTo(HaveOccurred())
			Expect(secs).To(BeNumerically(">=", before))
		})
	})

	Describe("aws:SourceIp", func() {
		It("is the client address without its port", func() {
			r := envRequest(http.MethodGet, "b", "o", "")
			Expect(sourceIP(cfg, r)).To(Equal([]string{"10.1.2.3"}))
			r.RemoteAddr = "[2001:db8::1]:443"
			Expect(sourceIP(cfg, r)).To(Equal([]string{"2001:db8::1"}), "ipv6")
			cfg.RemoteAddrParam = ""
			Expect(sourceIP(cfg, r)).To(Equal([]string{"2001:db8::1"}), "empty parameter")
		})

		It("takes X-Forwarded-For's first address (IPPolicyTest.IPEnvironment)", func() {
			cfg.RemoteAddrParam = "HTTP_X_FORWARDED_FOR"
			r := envRequest(http.MethodGet, "b", "o", "", "X-Forwarded-For", "192.168.1.4, 4.3.2.1")
			Expect(sourceIP(cfg, r)).To(Equal([]string{"192.168.1.4"}))
			Expect(sourceIP(cfg, envRequest(http.MethodGet, "b", "o", ""))).To(BeNil(), "header absent")
		})

		It("takes another header whole, and finds it whatever the parameter's case", func() {
			cfg.RemoteAddrParam = "HTTP_X_REAL_IP"
			r := envRequest(http.MethodGet, "b", "o", "", "X-Real-Ip", "1.2.3.4, 5.6.7.8")
			Expect(sourceIP(cfg, r)).To(Equal([]string{"1.2.3.4, 5.6.7.8"}))
			cfg.RemoteAddrParam = "http_x_forwarded_for"
			r = envRequest(http.MethodGet, "b", "o", "", "X-Forwarded-For", "192.168.1.4, 4.3.2.1")
			Expect(sourceIP(cfg, r)).To(Equal([]string{"192.168.1.4, 4.3.2.1"}), "only the exact name is cut")
		})

		It("keeps the whole chain for a lowercase X-Forwarded-For parameter, so no IpAddress condition matches it", func() {
			allow := mustParse(`{"Version": "2012-10-17", "Statement": [{"Effect": "Allow", "Principal": "*",
				"Action": "s3:GetObject", "Resource": "*", "Condition": {"IpAddress": {"aws:SourceIp": "192.168.0.0/16"}}}]}`)
			r := envRequest(http.MethodGet, "b", "o", "", "X-Forwarded-For", "192.168.1.4, 4.3.2.1")
			arn := policy.ObjectARN("", "b", "o")
			id := r.Identity
			for _, tc := range []struct {
				param string
				want  policy.Effect
			}{{"HTTP_X_FORWARDED_FOR", policy.Allow}, {"http_x_forwarded_for", policy.Pass}} {
				cfg.RemoteAddrParam = tc.param
				env := authz.BuildEnv(cfg, r, policy.S3GetObject)
				Expect(allow.Eval(env, authz.ViewOf(&id), policy.S3GetObject, &arn, policy.SemanticsFor(denc.Squid))).
					To(Equal(tc.want), tc.param)
			}
		})

		It("reads the content type and length headers under their CGI names", func() {
			r := envRequest(http.MethodPut, "b", "o", "", "Content-Type", "10.0.0.1", "Content-Length", "10.0.0.2")
			cfg.RemoteAddrParam = "CONTENT_TYPE"
			Expect(sourceIP(cfg, r)).To(Equal([]string{"10.0.0.1"}))
			cfg.RemoteAddrParam = "content_length"
			Expect(sourceIP(cfg, r)).To(Equal([]string{"10.0.0.2"}))
			cfg.RemoteAddrParam = "HTTP_CONTENT_TYPE"
			Expect(sourceIP(cfg, r)).To(BeNil(), "beast files Content-Type under CONTENT_TYPE alone")
		})

		It("keeps an IPv6 address's zone", func() {
			r := envRequest(http.MethodGet, "b", "o", "")
			r.RemoteAddr = "[fe80::1%eth0]:443"
			Expect(sourceIP(cfg, r)).To(Equal([]string{"fe80::1%eth0"}))
		})

		It("is absent for a parameter naming no request header", func() {
			cfg.RemoteAddrParam = "REQUEST_METHOD"
			Expect(sourceIP(cfg, envRequest(http.MethodGet, "b", "o", ""))).To(BeNil())
		})
	})

	Describe("aws:SecureTransport", func() {
		DescribeTable("trusts forwarded headers only when configured",
			func(trust bool, hdr, want []string) {
				cfg.TrustForwardedHTTPS = trust
				r := envRequest(http.MethodGet, "b", "o", "", hdr...)
				env := authz.BuildEnv(cfg, r, policy.S3GetObject)
				Expect(env.Lookup("aws:SecureTransport")).To(Equal(want))
			},
			Entry("X-Forwarded-Proto trusted", true, []string{"X-Forwarded-Proto", "https"}, []string{"true"}),
			Entry("X-Forwarded-Proto untrusted", false, []string{"X-Forwarded-Proto", "https"}, nil),
			Entry("X-Forwarded-Proto in capitals", true, []string{"X-Forwarded-Proto", "HTTPS"}, nil),
			Entry("Forwarded trusted", true, []string{"Forwarded", "for=1.2.3.4;proto=https"}, []string{"true"}),
			Entry("Forwarded with http", true, []string{"Forwarded", "proto=http"}, nil),
		)
	})

	Describe("ListBucket", func() {
		It("adds the listing parameters (RGWListBucket::verify_permission)", func() {
			r := envRequest(http.MethodGet, "b", "", "prefix=a/&delimiter=/&max-keys=7")
			Expect(pairsOf(cfg, r, policy.S3ListBucket)).To(Equal(with(baseFor(""),
				"s3:prefix", "a/", "s3:delimiter", "/", "s3:max-keys", "7")))
			Expect(pairsOf(cfg, r, policy.S3ListBucketVersions)).To(Equal(with(baseFor(""),
				"s3:prefix", "a/", "s3:delimiter", "/", "s3:max-keys", "7")), "versions")
		})

		It("always adds max-keys, bounded as radosgw lists", func() {
			r := envRequest(http.MethodGet, "b", "", "")
			Expect(pairsOf(cfg, r, policy.S3ListBucket)).To(Equal(with(baseFor(""), "s3:max-keys", "1000")))
			r = envRequest(http.MethodGet, "b", "", "prefix=&max-keys=5000")
			Expect(pairsOf(cfg, r, policy.S3ListBucket)).To(Equal(with(baseFor(""), "s3:max-keys", "1000")), "bounded")
			cfg.MaxListingResults = 2000
			Expect(pairsOf(cfg, r, policy.S3ListBucket)).To(Equal(with(baseFor(""), "s3:max-keys", "2000")), "configured bound")
			cfg = authz.Config{Release: denc.Tentacle, Now: cfg.Now, RemoteAddrParam: "REMOTE_ADDR"}
			Expect(pairsOf(cfg, r, policy.S3ListBucket)).To(Equal(with(baseFor(""), "s3:max-keys", "5000")), "tentacle's default bound")
		})

		It("leaves out a max-keys that does not parse", func() {
			r := envRequest(http.MethodGet, "b", "", "max-keys=7x")
			Expect(pairsOf(cfg, r, policy.S3ListBucket)).To(Equal(baseFor("")))
		})

		It("adds nothing for HEAD bucket, which checks s3:ListBucket too", func() {
			r := envRequest(http.MethodHead, "b", "", "prefix=a/")
			Expect(pairsOf(cfg, r, policy.S3ListBucket)).To(Equal(baseFor("")))
		})
	})

	Describe("PutObject", func() {
		It("always adds the canned ACL, empty when absent", func() {
			r := envRequest(http.MethodPut, "b", "o", "")
			pairs, tagsFirst := authz.EnvPairs(cfg, r, policy.S3PutObject)
			Expect(pairs).To(Equal(with(baseFor(""), "s3:x-amz-acl", "")))
			Expect(tagsFirst).To(BeFalse())
		})

		It("adds grants, then the canned ACL, then the request's tags (RGWPutObj::verify_permission)", func() {
			r := envRequest(http.MethodPut, "b", "o", "",
				"X-Amz-Acl", "public-read", "X-Amz-Grant-Read", `id="bob"`, "X-Amz-Tagging", "k=v&k2=v2")
			Expect(pairsOf(cfg, r, policy.S3PutObject)).To(Equal(with(baseFor(""),
				"s3:x-amz-grant-read", `id="bob"`,
				"s3:x-amz-acl", "public-read",
				"s3:RequestObjectTag/k", "v",
				"s3:RequestObjectTag/k2", "v2")))
		})

		It("adds every grant header in rgw_add_grant_to_iam_environment's order", func() {
			r := envRequest(http.MethodPut, "b", "o", "",
				"X-Amz-Grant-Full-Control", "f", "X-Amz-Grant-Write-Acp", "wa", "X-Amz-Grant-Read-Acp", "ra",
				"X-Amz-Grant-Write", "w", "X-Amz-Grant-Read", "r")
			Expect(pairsOf(cfg, r, policy.S3PutObject)).To(Equal(with(baseFor(""),
				"s3:x-amz-grant-read", "r", "s3:x-amz-grant-write", "w", "s3:x-amz-grant-read-acp", "ra",
				"s3:x-amz-grant-write-acp", "wa", "s3:x-amz-grant-full-control", "f",
				"s3:x-amz-acl", "")))
		})

		It("adds no grant key for an X-Amz-Grant header that is none of the five", func() {
			r := envRequest(http.MethodPut, "b", "o", "", "X-Amz-Grantee", "x")
			Expect(pairsOf(cfg, r, policy.S3PutObject)).To(Equal(with(baseFor(""), "s3:x-amz-acl", "")))
		})

		It("adds a repeated tag key's values in order and no tags from a header the op refuses", func() {
			r := envRequest(http.MethodPut, "b", "o", "", "X-Amz-Tagging", "k=b&k=a")
			Expect(pairsOf(cfg, r, policy.S3PutObject)).To(Equal(with(baseFor(""),
				"s3:x-amz-acl", "", "s3:RequestObjectTag/k", "b", "s3:RequestObjectTag/k", "a")))
			r = envRequest(http.MethodPut, "b", "o", "", "X-Amz-Tagging", "k=v&")
			Expect(pairsOf(cfg, r, policy.S3PutObject)).To(Equal(with(baseFor(""), "s3:x-amz-acl", "")))
		})

		It("adds the encryption keys, from headers and then the query", func() {
			r := envRequest(http.MethodPut, "b", "o", "",
				"X-Amz-Server-Side-Encryption", "AES256",
				"X-Amz-Server-Side-Encryption-Aws-Kms-Key-Id", "key1",
				"X-Amz-Server-Side-Encryption-Customer-Algorithm", "AES256")
			Expect(pairsOf(cfg, r, policy.S3PutObject)).To(Equal(with(baseFor(""),
				"s3:x-amz-acl", "",
				"s3:x-amz-server-side-encryption", "AES256",
				"s3:x-amz-server-side-encryption-aws-kms-key-id", "key1")), "squid")
			Expect(pairsOf(envConfig(denc.Tentacle), r, policy.S3PutObject)).To(Equal(with(baseFor(""),
				"s3:x-amz-acl", "",
				"s3:x-amz-server-side-encryption", "AES256",
				"s3:x-amz-server-side-encryption-customer-algorithm", "AES256",
				"s3:x-amz-server-side-encryption-aws-kms-key-id", "key1")), "tentacle")

			r = envRequest(http.MethodPut, "b", "o", "x-amz-server-side-encryption=aws:kms",
				"X-Amz-Server-Side-Encryption", "AES256")
			Expect(pairsOf(cfg, r, policy.S3PutObject)).To(Equal(with(baseFor(""),
				"s3:x-amz-acl", "", "s3:x-amz-server-side-encryption", "aws:kms")), "query")
			r = envRequest(http.MethodPut, "b", "o", "X-AMZ-SERVER-SIDE-ENCRYPTION=aws:kms")
			Expect(pairsOf(cfg, r, policy.S3PutObject)).To(Equal(with(baseFor(""),
				"s3:x-amz-acl", "", "s3:x-amz-server-side-encryption", "aws:kms")), "query in capitals")
		})

		It("reads the encryption header under radosgw's other metadata prefixes, the last in its order winning", func() {
			r := envRequest(http.MethodPut, "b", "o", "",
				"X-Rgw-Server-Side-Encryption", "rgw", "X-Amz-Server-Side-Encryption", "amz",
				"X-Account-Server-Side-Encryption", "account")
			Expect(pairsOf(cfg, r, policy.S3PutObject)).To(Equal(with(baseFor(""),
				"s3:x-amz-acl", "", "s3:x-amz-server-side-encryption", "rgw")))
		})

		It("refuses an UploadPart under a Deny on any canned ACL but bucket-owner-full-control", func() {
			deny := mustParse(`{"Version": "2012-10-17", "Statement": [{"Effect": "Deny", "Principal": "*",
				"Action": "s3:PutObject", "Resource": "*",
				"Condition": {"StringNotEquals": {"s3:x-amz-acl": "bucket-owner-full-control"}}}]}`)
			arn := policy.ObjectARN("", "b", "o")
			for _, rel := range []denc.Release{denc.Squid, denc.Tentacle} {
				part := envRequest(http.MethodPut, "b", "o", "partNumber=1&uploadId=u")
				id := part.Identity
				env := authz.BuildEnv(envConfig(rel), part, policy.S3PutObject)
				Expect(deny.Eval(env, authz.ViewOf(&id), policy.S3PutObject, &arn, policy.SemanticsFor(rel))).
					To(Equal(policy.Deny), "UploadPart on %v", rel)
				put := envRequest(http.MethodPut, "b", "o", "", "X-Amz-Acl", "bucket-owner-full-control")
				env = authz.BuildEnv(envConfig(rel), put, policy.S3PutObject)
				Expect(deny.Eval(env, authz.ViewOf(&id), policy.S3PutObject, &arn, policy.SemanticsFor(rel))).
					To(Equal(policy.Pass), "PutObject with the ACL on %v", rel)
			}
		})

		It("adds RGWPutObj's keys for UploadPart and UploadPartCopy", func() {
			r := envRequest(http.MethodPut, "b", "o", "partNumber=1&uploadId=u")
			Expect(pairsOf(cfg, r, policy.S3PutObject)).To(Equal(with(baseFor(""), "s3:x-amz-acl", "")), "UploadPart")
			r = envRequest(http.MethodPut, "b", "o", "partNumber=1&uploadId=u", "X-Amz-Copy-Source", "/src/k")
			Expect(pairsOf(cfg, r, policy.S3PutObject)).To(Equal(with(baseFor(""), "s3:x-amz-acl", "")), "UploadPartCopy")
			r = envRequest(http.MethodPut, "b", "o", "", "X-Amz-Copy-Source", "/src/k", "X-Amz-Copy-Source-Range", "bytes=0-1")
			Expect(pairsOf(cfg, r, policy.S3PutObject)).To(Equal(with(baseFor(""), "s3:x-amz-acl", "")), "a ranged copy source")
		})

		It("adds the copy source and the metadata directive for CopyObject, after the tags", func() {
			r := envRequest(http.MethodPut, "b", "o", "",
				"X-Amz-Copy-Source", "/src/k%20", "X-Amz-Metadata-Directive", "REPLACE", "X-Amz-Acl", "private")
			pairs, tagsFirst := authz.EnvPairs(cfg, r, policy.S3PutObject)
			Expect(pairs).To(Equal(with(baseFor(""),
				"s3:x-amz-copy-source", "/src/k%20", "s3:x-amz-metadata-directive", "REPLACE")))
			Expect(tagsFirst).To(BeTrue())
			r = envRequest(http.MethodPut, "b", "o", "", "X-Amz-Copy-Source", "/src/k")
			Expect(pairsOf(cfg, r, policy.S3PutObject)).To(Equal(with(baseFor(""), "s3:x-amz-copy-source", "/src/k")), "no directive")
		})

		It("adds only the encryption headers for CreateMultipartUpload and CompleteMultipartUpload, after the tags", func() {
			for _, q := range []string{"uploads", "uploadId=u"} {
				r := envRequest(http.MethodPost, "b", "o", q+"&x-amz-server-side-encryption=aws:kms",
					"X-Amz-Acl", "public-read", "X-Amz-Tagging", "k=v", "X-Amz-Server-Side-Encryption", "AES256")
				pairs, tagsFirst := authz.EnvPairs(cfg, r, policy.S3PutObject)
				Expect(pairs).To(Equal(with(baseFor(""), "s3:x-amz-server-side-encryption", "AES256")), q)
				Expect(tagsFirst).To(BeTrue(), q)
				r = envRequest(http.MethodPost, "b", "o", q)
				Expect(pairsOf(cfg, r, policy.S3PutObject)).To(Equal(baseFor("")), q+" without encryption")
			}
		})
	})

	Describe("PutBucketAcl and PutObjectAcl", func() {
		It("add the canned ACL, then the grants (RGWPutACLs::verify_permission)", func() {
			r := envRequest(http.MethodPut, "b", "o", "acl", "X-Amz-Grant-Write", "w", "X-Amz-Acl", "private")
			want := with(baseFor(""), "s3:x-amz-acl", "private", "s3:x-amz-grant-write", "w")
			for _, a := range []policy.Action{policy.S3PutObjectAcl, policy.S3PutObjectVersionAcl, policy.S3PutBucketAcl} {
				pairs, tagsFirst := authz.EnvPairs(cfg, r, a)
				Expect(pairs).To(Equal(want), a.String())
				Expect(tagsFirst).To(BeFalse(), a.String())
			}
			r = envRequest(http.MethodPut, "b", "", "acl")
			Expect(pairsOf(cfg, r, policy.S3PutBucketAcl)).To(Equal(with(baseFor(""), "s3:x-amz-acl", "")), "no headers")
		})
	})

	It("adds only the base keys for an op that adds none", func() {
		r := envRequest(http.MethodGet, "b", "o", "prefix=a", "X-Amz-Acl", "private", "X-Amz-Copy-Source", "/s/k")
		Expect(pairsOf(cfg, r, policy.S3GetObject)).To(Equal(baseFor("")))
		Expect(pairsOf(cfg, r, policy.S3DeleteObject)).To(Equal(baseFor("")), "DeleteObject")
		r = envRequest(http.MethodPost, "b", "o", "", "X-Amz-Server-Side-Encryption", "AES256")
		Expect(pairsOf(cfg, r, policy.S3PutObject)).To(Equal(baseFor("")), "POST object")
	})

	It("builds the base keys alone for the missing-object check", func() {
		r := envRequest(http.MethodGet, "b", "", "prefix=a/")
		env := authz.BaseEnv(cfg, r)
		Expect(env.Lookup("s3:prefix")).To(BeNil())
		Expect(env.Lookup("s3:max-keys")).To(BeNil())
		Expect(env.Lookup("aws:SourceIp")).To(Equal([]string{"10.1.2.3"}))
	})

	Describe("the tag point", func() {
		// Three grants, the canned ACL and ten request tags put a PUT's
		// environment at 21 pairs before its bucket tags, while
		// CreateMultipartUpload adds its tags right after the seven base keys.
		const manyTags = "a=1&b=2&c=3&d=4&e=5&f=6&g=7&h=8&i=9&j=10"
		bucketTags := func(env *policy.Env) {
			env.Add("s3:ResourceTag/k", "first")
			env.Add("s3:ResourceTag/k", "second")
		}

		It("is after RGWPutObj's keys, so Tentacle's find reads the value added past 20 pairs", func() {
			r := envRequest(http.MethodPut, "b", "o", "", "X-Amz-Tagging", manyTags,
				"X-Amz-Grant-Read", "r", "X-Amz-Grant-Write", "w", "X-Amz-Grant-Read-Acp", "ra")
			tentacle := envConfig(denc.Tentacle)
			env := authz.BuildEnvWithTags(tentacle, r, policy.S3PutObject, bucketTags)
			Expect(env.Lookup("s3:RequestObjectTag/j")).To(Equal([]string{"10"}))
			v, ok := env.Find("s3:ResourceTag/k", policy.SemanticsFor(denc.Tentacle))
			Expect(ok).To(BeTrue())
			Expect(v).To(Equal("second"), "tentacle")
			v, _ = env.Find("s3:ResourceTag/k", policy.SemanticsFor(denc.Squid))
			Expect(v).To(Equal("second"), "squid")
		})

		It("is before CreateMultipartUpload's keys, so Tentacle's find keeps the first value", func() {
			r := envRequest(http.MethodPost, "b", "o", "uploads", "X-Amz-Tagging", manyTags)
			env := authz.BuildEnvWithTags(envConfig(denc.Tentacle), r, policy.S3PutObject, bucketTags)
			v, ok := env.Find("s3:ResourceTag/k", policy.SemanticsFor(denc.Tentacle))
			Expect(ok).To(BeTrue())
			Expect(v).To(Equal("first"), "tentacle")
			v, _ = env.Find("s3:ResourceTag/k", policy.SemanticsFor(denc.Squid))
			Expect(v).To(Equal("second"), "squid")
		})
	})
})
