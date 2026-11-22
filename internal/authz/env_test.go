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

// aclAndTags are a grant, a canned ACL and tags, a repeated key among them.
func aclAndTags() []string {
	return []string{"X-Amz-Grant-Read", `id="bob"`, "X-Amz-Acl", "public-read", "X-Amz-Tagging", "k=b&k=a&k2=v2"}
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

		It("takes the parameters the listing read, once it has set them", func() {
			r := envRequest(http.MethodGet, "b", "", "prefix=q&delimiter=x&max-keys=7x")
			r.List = &op.ListConditions{Prefix: "a/", Delimiter: "/", MaxKeys: 3}
			Expect(pairsOf(cfg, r, policy.S3ListBucket)).To(Equal(with(baseFor(""),
				"s3:prefix", "a/", "s3:delimiter", "/", "s3:max-keys", "3")))
			r.List = &op.ListConditions{}
			Expect(pairsOf(cfg, r, policy.S3ListBucketVersions)).To(Equal(with(baseFor(""), "s3:max-keys", "0")),
				"an empty prefix and delimiter are left out, max-keys never")
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

		It("adds the encryption keys, from headers and then a SigV4 request's query", func() {
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
			r.AuthRoute = op.RouteSigV4Header
			Expect(pairsOf(cfg, r, policy.S3PutObject)).To(Equal(with(baseFor(""),
				"s3:x-amz-acl", "", "s3:x-amz-server-side-encryption", "aws:kms")), "query")
			r = envRequest(http.MethodPut, "b", "o", "X-AMZ-SERVER-SIDE-ENCRYPTION=aws:kms")
			r.AuthRoute = op.RouteSigV4Header
			Expect(pairsOf(cfg, r, policy.S3PutObject)).To(Equal(with(baseFor(""),
				"s3:x-amz-acl", "", "s3:x-amz-server-side-encryption", "aws:kms")), "query in capitals")
		})

		DescribeTable("takes no encryption key from the query where the route may not have signed it",
			func(route op.AuthRoute) {
				r := envRequest(http.MethodPut, "b", "o", "x-amz-server-side-encryption=aws:kms&x-amz-server-side-encryption-aws-kms-key-id=q",
					"X-Amz-Server-Side-Encryption", "AES256")
				r.AuthRoute = route
				Expect(pairsOf(cfg, r, policy.S3PutObject)).To(Equal(with(baseFor(""),
					"s3:x-amz-acl", "", "s3:x-amz-server-side-encryption", "AES256")),
					"the signed header's, which radosgw's query overwrites (rgw_rest_s3.cc:2599-2601 at v19.2.7)")
				r = envRequest(http.MethodPut, "b", "o", "X-AMZ-SERVER-SIDE-ENCRYPTION=aws:kms")
				r.AuthRoute = route
				Expect(pairsOf(cfg, r, policy.S3PutObject)).To(Equal(with(baseFor(""), "s3:x-amz-acl", "")), "the query alone")
			},
			Entry("SigV2's header route", op.RouteSigV2Header),
			Entry("SigV2's presigned route, whose string to sign takes the query's meta and token alone", op.RouteSigV2Query),
			Entry("a request that names no route", op.RouteUnknown),
		)

		It("takes the encryption keys from a presigned SigV4 request's query, which its canonical query signs", func() {
			r := envRequest(http.MethodPut, "b", "o", "x-amz-server-side-encryption=aws:kms", "X-Amz-Server-Side-Encryption", "AES256")
			r.AuthRoute = op.RouteSigV4Query
			Expect(pairsOf(cfg, r, policy.S3PutObject)).To(Equal(with(baseFor(""),
				"s3:x-amz-acl", "", "s3:x-amz-server-side-encryption", "aws:kms")))
		})

		It("reads the encryption headers from X-Amz- alone, under none of radosgw's other meta prefixes", func() {
			r := envRequest(http.MethodPut, "b", "o", "",
				"X-Rgw-Server-Side-Encryption", "rgw", "X-Amz-Server-Side-Encryption", "amz",
				"X-Account-Server-Side-Encryption", "account")
			Expect(pairsOf(cfg, r, policy.S3PutObject)).To(Equal(with(baseFor(""),
				"s3:x-amz-acl", "", "s3:x-amz-server-side-encryption", "amz")), "beside an X-Amz- one")
			for _, p := range []string{"X-Goog-", "X-Dho-", "X-Rgw-", "X-Object-", "X-Container-", "X-Account-"} {
				r = envRequest(http.MethodPut, "b", "o", "",
					p+"Server-Side-Encryption", "AES256", p+"Server-Side-Encryption-Aws-Kms-Key-Id", "key1",
					p+"Server-Side-Encryption-Customer-Algorithm", "AES256")
				Expect(pairsOf(envConfig(denc.Tentacle), r, policy.S3PutObject)).To(Equal(with(baseFor(""), "s3:x-amz-acl", "")), "%s alone", p)
			}
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

		It("adds the copy source and the metadata directive for CopyObject, after the tags, then the ACL's keys", func() {
			r := envRequest(http.MethodPut, "b", "o", "",
				"X-Amz-Copy-Source", "/src/k%20", "X-Amz-Metadata-Directive", "REPLACE", "X-Amz-Acl", "private")
			pairs, tagsFirst := authz.EnvPairs(cfg, r, policy.S3PutObject)
			Expect(pairs).To(Equal(with(baseFor(""),
				"s3:x-amz-copy-source", "/src/k%20", "s3:x-amz-metadata-directive", "REPLACE", "s3:x-amz-acl", "private")))
			Expect(tagsFirst).To(BeTrue())
			r = envRequest(http.MethodPut, "b", "o", "", "X-Amz-Copy-Source", "/src/k")
			Expect(pairsOf(cfg, r, policy.S3PutObject)).To(Equal(with(baseFor(""),
				"s3:x-amz-copy-source", "/src/k")), "no directive and no canned ACL")
		})

		It("takes a copy source with an empty bucket as RGWPutObj's, as the dispatch does", func() {
			for _, src := range []string{"//k", "/%2Fk", "%2Fk"} {
				r := envRequest(http.MethodPut, "b", "o", "", "X-Amz-Copy-Source", src, "X-Amz-Metadata-Directive", "REPLACE")
				Expect(pairsOf(cfg, r, policy.S3PutObject)).To(Equal(with(baseFor(""), "s3:x-amz-acl", "")), src)
			}
			for _, src := range []string{"/src/k", "src/k", "", "/src", "/src/"} {
				r := envRequest(http.MethodPut, "b", "o", "", "X-Amz-Copy-Source", src)
				Expect(pairsOf(cfg, r, policy.S3PutObject)).To(Equal(with(baseFor(""), "s3:x-amz-copy-source", src)),
					"%q, which the dispatch sends to the copy", src)
			}
		})

		It("adds only the encryption headers for CompleteMultipartUpload, after the tags, on a route that reads the query", func() {
			for _, q := range []string{"uploadId=u", "uploads&uploadId=u"} {
				r := envRequest(http.MethodPost, "b", "o", q+"&x-amz-server-side-encryption=aws:kms",
					"X-Amz-Acl", "public-read", "X-Amz-Tagging", "k=v", "X-Amz-Server-Side-Encryption", "AES256")
				r.AuthRoute = op.RouteSigV4Header
				pairs, tagsFirst := authz.EnvPairs(cfg, r, policy.S3PutObject)
				Expect(pairs).To(Equal(with(baseFor(""), "s3:x-amz-server-side-encryption", "AES256")), q)
				Expect(tagsFirst).To(BeTrue(), q)
				r = envRequest(http.MethodPost, "b", "o", q)
				Expect(pairsOf(cfg, r, policy.S3PutObject)).To(Equal(baseFor("")), q+" without encryption")
			}
		})

		// radosgw adds none of the ACL's and the tags' keys for CopyObject and
		// CreateMultipartUpload; rgw-go adds RGWPutObj's, after radosgw's own.
		Describe("the ACL's and the tags' keys of CopyObject and CreateMultipartUpload", func() {
			It("adds CreateMultipartUpload's grants, canned ACL and tags in RGWPutObj's order, after the encryption headers", func() {
				r := envRequest(http.MethodPost, "b", "o", "uploads", append(aclAndTags(), "X-Amz-Server-Side-Encryption", "AES256")...)
				pairs, tagsFirst := authz.EnvPairs(cfg, r, policy.S3PutObject)
				Expect(pairs).To(Equal(with(baseFor(""),
					"s3:x-amz-server-side-encryption", "AES256",
					"s3:x-amz-grant-read", `id="bob"`,
					"s3:x-amz-acl", "public-read",
					"s3:RequestObjectTag/k", "b", "s3:RequestObjectTag/k", "a", "s3:RequestObjectTag/k2", "v2")))
				Expect(tagsFirst).To(BeTrue())
				r = envRequest(http.MethodPost, "b", "o", "uploads")
				Expect(pairsOf(cfg, r, policy.S3PutObject)).To(Equal(baseFor("")),
					"no canned ACL key when none is sent, where RGWPutObj adds one empty")
				r = envRequest(http.MethodPost, "b", "o", "uploads", "X-Amz-Acl", "")
				Expect(pairsOf(cfg, r, policy.S3PutObject)).To(Equal(baseFor("")),
					"no canned ACL key for an empty one sent, which names none")
				r = envRequest(http.MethodPut, "b", "o", "", "X-Amz-Copy-Source", "/src/k", "X-Amz-Acl", "")
				Expect(pairsOf(cfg, r, policy.S3PutObject)).To(Equal(with(baseFor(""), "s3:x-amz-copy-source", "/src/k")),
					"nor for a copy's")
				r = envRequest(http.MethodPost, "b", "o", "uploads", "X-Amz-Grant-Read", "", "X-Amz-Grant-Write", "w")
				Expect(pairsOf(cfg, r, policy.S3PutObject)).To(Equal(with(baseFor(""), "s3:x-amz-grant-write", "w")),
					"no grant key for an empty grant header, which grants nothing")
				r = envRequest(http.MethodPost, "b", "o", "uploads", "X-Amz-Grant-Read", ",", "X-Amz-Grant-Write", ", ,")
				Expect(pairsOf(cfg, r, policy.S3PutObject)).To(Equal(with(baseFor(""), "s3:x-amz-grant-write", ", ,")),
					"none for one that names no grantee, and one for one whose item does not parse")
				r = envRequest(http.MethodPost, "b", "o", "uploads", "X-Amz-Tagging", "k=v&")
				Expect(pairsOf(cfg, r, policy.S3PutObject)).To(Equal(baseFor("")),
					"no tags from a header the upload refuses once authorized")
			})

			It("adds a copy's tags only when it replaces the source's metadata, the one directive under which it stores them", func() {
				for _, d := range []string{"REPLACE", "replace", "Replace\x00x"} {
					r := envRequest(http.MethodPut, "b", "o", "", append(aclAndTags(),
						"X-Amz-Copy-Source", "/src/k", "X-Amz-Metadata-Directive", d)...)
					Expect(pairsOf(cfg, r, policy.S3PutObject)).To(Equal(with(baseFor(""),
						"s3:x-amz-copy-source", "/src/k", "s3:x-amz-metadata-directive", d,
						"s3:x-amz-grant-read", `id="bob"`,
						"s3:x-amz-acl", "public-read",
						"s3:RequestObjectTag/k", "b", "s3:RequestObjectTag/k", "a", "s3:RequestObjectTag/k2", "v2")), "%q", d)
				}
				r := envRequest(http.MethodPut, "b", "o", "", append(aclAndTags(), "X-Amz-Copy-Source", "/src/k")...)
				Expect(pairsOf(cfg, r, policy.S3PutObject)).To(Equal(with(baseFor(""),
					"s3:x-amz-copy-source", "/src/k", "s3:x-amz-grant-read", `id="bob"`, "s3:x-amz-acl", "public-read")),
					"no directive, which copies the source's tags")
				r = envRequest(http.MethodPut, "b", "o", "", append(aclAndTags(), "X-Amz-Copy-Source", "/src/k",
					"X-Amz-Metadata-Directive", "COPY", "X-Amz-Tagging-Directive", "REPLACE")...)
				Expect(pairsOf(cfg, r, policy.S3PutObject)).To(Equal(with(baseFor(""),
					"s3:x-amz-copy-source", "/src/k", "s3:x-amz-metadata-directive", "COPY",
					"s3:x-amz-grant-read", `id="bob"`, "s3:x-amz-acl", "public-read")),
					"COPY, with a tagging directive the copy does not read")
			})

			It("adds them to the s3:PutObject check alone", func() {
				for _, r := range []*op.Request{
					envRequest(http.MethodPost, "b", "o", "uploads", aclAndTags()...),
					envRequest(http.MethodPut, "b", "o", "", append(aclAndTags(),
						"X-Amz-Copy-Source", "/src/k", "X-Amz-Metadata-Directive", "REPLACE")...),
				} {
					Expect(pairsOf(cfg, r, policy.S3GetObject)).To(Equal(baseFor("")), "%s s3:GetObject", r.Method)
					Expect(pairsOf(cfg, r, policy.S3PutObjectTagging)).To(Equal(with(baseFor(""),
						"s3:RequestObjectTag/k", "b", "s3:RequestObjectTag/k", "a", "s3:RequestObjectTag/k2", "v2")),
						"%s s3:PutObjectTagging, its own keys alone", r.Method)
				}
			})
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

	Describe("PutObjectTagging", func() {
		It("adds the tags a write sets, for the check an object write makes of the action", func() {
			r := envRequest(http.MethodPut, "b", "o", "", "X-Amz-Tagging", "k=b&k=a&k2=v2")
			pairs, tagsFirst := authz.EnvPairs(cfg, r, policy.S3PutObjectTagging)
			Expect(pairs).To(Equal(with(baseFor(""),
				"s3:RequestObjectTag/k", "b", "s3:RequestObjectTag/k", "a", "s3:RequestObjectTag/k2", "v2")))
			Expect(tagsFirst).To(BeFalse())
			r = envRequest(http.MethodPost, "b", "o", "uploads", "X-Amz-Tagging", "k=v")
			Expect(pairsOf(cfg, r, policy.S3PutObjectTagging)).To(Equal(with(baseFor(""), "s3:RequestObjectTag/k", "v")), "an upload")
		})

		It("adds none without tags, for a header the write refuses, or for the op's own request", func() {
			Expect(pairsOf(cfg, envRequest(http.MethodPut, "b", "o", ""), policy.S3PutObjectTagging)).To(Equal(baseFor("")), "no header")
			r := envRequest(http.MethodPut, "b", "o", "", "X-Amz-Tagging", "k=v&")
			Expect(pairsOf(cfg, r, policy.S3PutObjectTagging)).To(Equal(baseFor("")), "tags that do not parse")
			r = envRequest(http.MethodPut, "b", "o", "tagging", "X-Amz-Tagging", "k=v")
			Expect(pairsOf(cfg, r, policy.S3PutObjectTagging)).To(Equal(baseFor("")),
				"RGWPutObjTags adds none, whatever header the request carries")
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

		// Before the tags, the grants, the canned ACL and the request's tags
		// would put these environments past 20 pairs.
		It("is before the ACL's and the tags' keys CopyObject and CreateMultipartUpload add, which radosgw lacks", func() {
			hdr := []string{
				"X-Amz-Tagging", manyTags,
				"X-Amz-Grant-Read", "r", "X-Amz-Grant-Write", "w", "X-Amz-Grant-Read-Acp", "ra",
			}
			for _, r := range []*op.Request{
				envRequest(http.MethodPost, "b", "o", "uploads", hdr...),
				envRequest(http.MethodPut, "b", "o", "", append(hdr,
					"X-Amz-Copy-Source", "/src/k", "X-Amz-Metadata-Directive", "REPLACE")...),
			} {
				env := authz.BuildEnvWithTags(envConfig(denc.Tentacle), r, policy.S3PutObject, bucketTags)
				Expect(env.Lookup("s3:RequestObjectTag/j")).To(Equal([]string{"10"}), r.Method)
				v, ok := env.Find("s3:ResourceTag/k", policy.SemanticsFor(denc.Tentacle))
				Expect(ok).To(BeTrue())
				Expect(v).To(Equal("first"), "%s on tentacle", r.Method)
			}
		})
	})

	Describe("the keys Amazon S3's request context sets, which a Deny reads", func() {
		// amazonValue is key's value for a Deny, or absent.
		const absent = "<absent>"
		amazonValue := func(r *op.Request, a policy.Action, key string) string {
			for _, p := range authz.AmazonPairs(cfg, r, a) {
				if p[0] == key {
					return p[1]
				}
			}
			return absent
		}
		denies := func(env policy.Env, a policy.Action, cond string) policy.Effect {
			p := mustParse(`{"Version": "2012-10-17", "Statement": [{"Effect": "Deny", "Principal": "*",` +
				` "Action": "` + a.String() + `", "Resource": "*", "Condition": ` + cond + `}]}`)
			res := policy.ObjectARN("", "b", "o")
			return p.Eval(env, nil, a, &res, policy.SemanticsFor(denc.Squid))
		}

		It("are the request's time in Amazon S3's two forms and whether it is secure", func() {
			r := envRequest(http.MethodGet, "b", "o", "")
			Expect(authz.AmazonPairs(cfg, r, policy.S3GetObject)).To(Equal([][2]string{
				{"aws:CurrentTime", "2026-09-27T12:00:00Z"},
				{"aws:EpochTime", "1790510400"},
				{"aws:SecureTransport", "false"},
			}), "a plain request on no route")
			r.TLS = true
			Expect(amazonValue(r, policy.S3GetObject, "aws:SecureTransport")).To(Equal("true"), "over TLS")
			cfg.TrustForwardedHTTPS = true
			r = envRequest(http.MethodGet, "b", "o", "", "X-Forwarded-Proto", "https")
			Expect(amazonValue(r, policy.S3GetObject, "aws:SecureTransport")).To(Equal("true"), "from a trusted proxy")
		})

		DescribeTable("name how the request is authenticated, and the payload hash a SigV4 signature covers",
			func(route op.AuthRoute, formSigV4 bool, want [][2]string) {
				r := envRequest(http.MethodGet, "b", "o", "", "X-Amz-Content-Sha256", "e3b0c442")
				r.AuthRoute, r.FormSigV4 = route, formSigV4
				Expect(authz.AmazonPairs(cfg, r, policy.S3GetObject)[3:]).To(Equal(want))
			},
			Entry("SigV4 in the Authorization header", op.RouteSigV4Header, false, [][2]string{
				{"s3:signatureversion", "AWS4-HMAC-SHA256"}, {"s3:authType", "REST-HEADER"}, {"s3:x-amz-content-sha256", "e3b0c442"},
			}),
			Entry("a SigV4 presigned URL", op.RouteSigV4Query, false, [][2]string{
				{"s3:signatureversion", "AWS4-HMAC-SHA256"}, {"s3:authType", "REST-QUERY-STRING"}, {"s3:x-amz-content-sha256", "e3b0c442"},
			}),
			Entry("SigV2 in the Authorization header", op.RouteSigV2Header, false,
				[][2]string{{"s3:signatureversion", "AWS"}, {"s3:authType", "REST-HEADER"}}),
			Entry("a SigV2 presigned URL", op.RouteSigV2Query, false,
				[][2]string{{"s3:signatureversion", "AWS"}, {"s3:authType", "REST-QUERY-STRING"}}),
			Entry("a browser upload whose form SigV2 signs", op.RouteBrowserUpload, false,
				[][2]string{{"s3:signatureversion", "AWS"}, {"s3:authType", "POST"}}),
			Entry("a browser upload whose form SigV4 signs", op.RouteBrowserUpload, true,
				[][2]string{{"s3:signatureversion", "AWS4-HMAC-SHA256"}, {"s3:authType", "POST"}}),
			Entry("an anonymous request", op.RouteAnonymous, false, [][2]string{}),
			Entry("no route", op.RouteUnknown, false, [][2]string{}),
		)

		DescribeTable("take UNSIGNED-PAYLOAD for every payload the SigV4 verifier reads with no signature",
			func(route op.AuthRoute, hdr []string, want string) {
				r := envRequest(http.MethodPut, "b", "o", "", hdr...)
				r.AuthRoute = route
				Expect(amazonValue(r, policy.S3PutObject, "s3:x-amz-content-sha256")).To(Equal(want))
			},
			Entry("no payload hash", op.RouteSigV4Header, nil, "UNSIGNED-PAYLOAD"),
			Entry("a presigned request with no payload hash", op.RouteSigV4Query, nil, "UNSIGNED-PAYLOAD"),
			Entry("UNSIGNED-PAYLOAD", op.RouteSigV4Header, []string{"X-Amz-Content-Sha256", "UNSIGNED-PAYLOAD"}, "UNSIGNED-PAYLOAD"),
			Entry("a prefix before it", op.RouteSigV4Header, []string{"X-Amz-Content-Sha256", "XUNSIGNED-PAYLOAD"}, "UNSIGNED-PAYLOAD"),
			Entry("a suffix after it", op.RouteSigV4Header, []string{"X-Amz-Content-Sha256", "UNSIGNED-PAYLOAD-X"}, "UNSIGNED-PAYLOAD"),
			Entry("it twice in one value", op.RouteSigV4Header, []string{"X-Amz-Content-Sha256", "UNSIGNED-PAYLOAD,UNSIGNED-PAYLOAD"},
				"UNSIGNED-PAYLOAD"),
			Entry("a presigned request's prefixed spelling", op.RouteSigV4Query, []string{"X-Amz-Content-Sha256", "XUNSIGNED-PAYLOAD"},
				"UNSIGNED-PAYLOAD"),
			Entry("the last of a repeated header", op.RouteSigV4Header,
				[]string{"X-Amz-Content-Sha256", "e3b0c442", "X-Amz-Content-Sha256", "XUNSIGNED-PAYLOAD"}, "UNSIGNED-PAYLOAD"),
			Entry("a hash, the last of a repeated header", op.RouteSigV4Header,
				[]string{"X-Amz-Content-Sha256", "UNSIGNED-PAYLOAD", "X-Amz-Content-Sha256", "e3b0c442"}, "e3b0c442"),
			Entry("aws-chunked with unsigned chunks", op.RouteSigV4Header,
				[]string{"X-Amz-Content-Sha256", "STREAMING-UNSIGNED-PAYLOAD-TRAILER"}, "UNSIGNED-PAYLOAD"),
			Entry("an unsigned form ending in TRAILER, whose body the verifier hashes", op.RouteSigV4Header,
				[]string{"X-Amz-Content-Sha256", "UNSIGNED-PAYLOAD-TRAILER"}, "UNSIGNED-PAYLOAD-TRAILER"),
			Entry("an aws-chunked spelling of the unsigned form, whose chunk signatures the verifier checks", op.RouteSigV4Header,
				[]string{"X-Amz-Content-Sha256", "STREAMING-XUNSIGNED-PAYLOAD-TRAILER"}, "STREAMING-XUNSIGNED-PAYLOAD-TRAILER"),
			Entry("aws-chunked's signed form", op.RouteSigV4Header,
				[]string{"X-Amz-Content-Sha256", "STREAMING-AWS4-HMAC-SHA256-PAYLOAD"}, "STREAMING-AWS4-HMAC-SHA256-PAYLOAD"),
			Entry("aws-chunked's signed form with a signed trailer", op.RouteSigV4Header,
				[]string{"X-Amz-Content-Sha256", "STREAMING-AWS4-HMAC-SHA256-PAYLOAD-TRAILER"}, "STREAMING-AWS4-HMAC-SHA256-PAYLOAD-TRAILER"),
			Entry("a lower-case spelling, which the verifier hashes against", op.RouteSigV4Header,
				[]string{"X-Amz-Content-Sha256", "unsigned-payload"}, "unsigned-payload"),
			Entry("a SigV2 request", op.RouteSigV2Header, []string{"X-Amz-Content-Sha256", "UNSIGNED-PAYLOAD"}, absent),
			Entry("a browser upload", op.RouteBrowserUpload, nil, absent),
		)

		DescribeTable("read the payload hash a second way where Amazon's texts leave it open",
			func(route op.AuthRoute, hdr []string, want []authz.AmazonOther) {
				r := envRequest(http.MethodPut, "b", "o", "", hdr...)
				r.AuthRoute = route
				Expect(authz.AmazonOthers(cfg, r, policy.S3PutObject)).To(Equal(want))
			},
			Entry("a presigned request with no payload hash, also without the key", op.RouteSigV4Query, nil,
				[]authz.AmazonOther{{Key: "s3:x-amz-content-sha256", Absent: true}}),
			Entry("a presigned request sending UNSIGNED-PAYLOAD, also without the key", op.RouteSigV4Query,
				[]string{"X-Amz-Content-Sha256", "UNSIGNED-PAYLOAD"}, []authz.AmazonOther{{Key: "s3:x-amz-content-sha256", Absent: true}}),
			Entry("a presigned request sending the body's hash, also without the key", op.RouteSigV4Query,
				[]string{"X-Amz-Content-Sha256", "e3b0c442"}, []authz.AmazonOther{{Key: "s3:x-amz-content-sha256", Absent: true}}),
			Entry("a presigned request sending a prefixed spelling, also as sent and without the key", op.RouteSigV4Query,
				[]string{"X-Amz-Content-Sha256", "XUNSIGNED-PAYLOAD"}, []authz.AmazonOther{
					{Key: "s3:x-amz-content-sha256", Value: "XUNSIGNED-PAYLOAD"}, {Key: "s3:x-amz-content-sha256", Absent: true},
				}),
			Entry("aws-chunked with unsigned chunks, also as sent", op.RouteSigV4Header,
				[]string{"X-Amz-Content-Sha256", "STREAMING-UNSIGNED-PAYLOAD-TRAILER"},
				[]authz.AmazonOther{{Key: "s3:x-amz-content-sha256", Value: "STREAMING-UNSIGNED-PAYLOAD-TRAILER"}}),
			Entry("a prefixed spelling, also as sent", op.RouteSigV4Header, []string{"X-Amz-Content-Sha256", "XUNSIGNED-PAYLOAD"},
				[]authz.AmazonOther{{Key: "s3:x-amz-content-sha256", Value: "XUNSIGNED-PAYLOAD"}}),
			Entry("UNSIGNED-PAYLOAD itself, read one way", op.RouteSigV4Header, []string{"X-Amz-Content-Sha256", "UNSIGNED-PAYLOAD"},
				[]authz.AmazonOther{}),
			Entry("a header-signed request with no payload hash, read one way", op.RouteSigV4Header, nil, []authz.AmazonOther{}),
			Entry("the body's hash, read one way", op.RouteSigV4Header, []string{"X-Amz-Content-Sha256", "e3b0c442"}, []authz.AmazonOther{}),
			Entry("aws-chunked with signed chunks, read one way", op.RouteSigV4Header,
				[]string{"X-Amz-Content-Sha256", "STREAMING-AWS4-HMAC-SHA256-PAYLOAD"}, []authz.AmazonOther{}),
			Entry("an unsigned form ending in TRAILER, whose body the verifier hashes, read one way", op.RouteSigV4Header,
				[]string{"X-Amz-Content-Sha256", "UNSIGNED-PAYLOAD-TRAILER"}, []authz.AmazonOther{}),
			Entry("a SigV2 request, which has no payload hash", op.RouteSigV2Query, nil, []authz.AmazonOther{}),
			Entry("a browser upload, which has no payload hash", op.RouteBrowserUpload, nil, []authz.AmazonOther{}),
		)

		It("read the payload hash one way, or not at all, on the two checks whose keys AWS lists without it", func() {
			r := envRequest(http.MethodPut, "b", "", "object-lock")
			r.AuthRoute = op.RouteSigV4Query
			for _, a := range []policy.Action{policy.S3GetBucketObjectLockConfiguration, policy.S3PutBucketObjectLockConfiguration} {
				Expect(authz.AmazonOthers(cfg, r, a)).To(BeEmpty(), a.String())
			}
			Expect(authz.AmazonOthers(cfg, r, policy.IAMGetUser)).To(BeEmpty(), "an action outside S3")
		})

		It("leave the payload hash out of the two checks whose keys AWS lists without it", func() {
			r := envRequest(http.MethodPut, "b", "", "object-lock")
			r.AuthRoute = op.RouteSigV4Header
			for _, a := range []policy.Action{policy.S3GetBucketObjectLockConfiguration, policy.S3PutBucketObjectLockConfiguration} {
				Expect(amazonValue(r, a, "s3:x-amz-content-sha256")).To(Equal(absent), a.String())
				Expect(amazonValue(r, a, "s3:authType")).To(Equal("REST-HEADER"), a.String())
			}
		})

		DescribeTable("take x-amz-storage-class for the actions AWS lists the key for",
			func(a policy.Action, want string) {
				r := envRequest(http.MethodPut, "b", "o", "", "X-Amz-Storage-Class", "STANDARD_IA")
				Expect(amazonValue(r, a, "s3:x-amz-storage-class")).To(Equal(want))
				Expect(amazonValue(envRequest(http.MethodPut, "b", "o", ""), a, "s3:x-amz-storage-class")).
					To(Equal(absent), "without the header")
			},
			Entry("s3:PutObject", policy.S3PutObject, "STANDARD_IA"),
			Entry("s3:PutObjectAcl", policy.S3PutObjectAcl, "STANDARD_IA"),
			Entry("s3:PutObjectVersionAcl", policy.S3PutObjectVersionAcl, "STANDARD_IA"),
			Entry("s3:BypassGovernanceRetention", policy.S3BypassGovernanceRetention, "STANDARD_IA"),
			Entry("s3:PutObjectTagging", policy.S3PutObjectTagging, absent),
			Entry("s3:GetObject", policy.S3GetObject, absent),
		)

		DescribeTable("take the version a check of a version action is on",
			func(pairs func() [][2]string, want string) {
				v := absent
				for _, p := range pairs() {
					if p[0] == "s3:VersionId" {
						v = p[1]
					}
				}
				Expect(v).To(Equal(want))
			},
			Entry("GetObjectVersion", func() [][2]string {
				return authz.AmazonPairs(cfg, envRequest(http.MethodGet, "b", "o", "versionId=v1"), policy.S3GetObjectVersion)
			}, "v1"),
			Entry("HeadObject of a version", func() [][2]string {
				return authz.AmazonPairs(cfg, envRequest(http.MethodHead, "b", "o", "versionId=v1"), policy.S3GetObjectVersion)
			}, "v1"),
			Entry("DeleteObjectVersion", func() [][2]string {
				return authz.AmazonPairs(cfg, envRequest(http.MethodDelete, "b", "o", "versionId=v1"), policy.S3DeleteObjectVersion)
			}, "v1"),
			Entry("PutObjectVersionAcl", func() [][2]string {
				return authz.AmazonPairs(cfg, envRequest(http.MethodPut, "b", "o", "acl&versionId=v1"), policy.S3PutObjectVersionAcl)
			}, "v1"),
			Entry("PutObjectVersionTagging", func() [][2]string {
				return authz.AmazonPairs(cfg, envRequest(http.MethodPut, "b", "o", "tagging&versionId=v1"), policy.S3PutObjectVersionTagging)
			}, "v1"),
			Entry("DeleteObjectVersionTagging", func() [][2]string {
				return authz.AmazonPairs(cfg, envRequest(http.MethodDelete, "b", "o", "tagging&versionId=v1"),
					policy.S3DeleteObjectVersionTagging)
			}, "v1"),
			Entry("GetObjectVersionAttributes", func() [][2]string {
				return authz.AmazonPairs(cfg, envRequest(http.MethodGet, "b", "o", "attributes&versionId=v1"),
					policy.S3GetObjectVersionAttributes)
			}, "v1"),
			Entry("a GET naming a version beside a copy source header it has no use for", func() [][2]string {
				return authz.AmazonPairs(cfg, envRequest(http.MethodGet, "b", "o", "versionId=v1", "X-Amz-Copy-Source", "/src/k?versionId=v2"),
					policy.S3GetObjectVersion)
			}, "v1"),
			Entry("a tagging PUT naming a version beside a copy source header it has no use for", func() [][2]string {
				return authz.AmazonPairs(cfg, envRequest(http.MethodPut, "b", "o", "tagging&versionId=v1", "X-Amz-Copy-Source", "/src/k"),
					policy.S3PutObjectVersionTagging)
			}, "v1"),
			Entry("an action AWS does not list the key for", func() [][2]string {
				return authz.AmazonPairs(cfg, envRequest(http.MethodGet, "b", "o", "versionId=v1"), policy.S3GetObject)
			}, absent),
			Entry("a version action naming no version", func() [][2]string {
				return authz.AmazonPairs(cfg, envRequest(http.MethodGet, "b", "o", ""), policy.S3GetObjectVersion)
			}, absent),
			Entry("a copy's source, whatever version the query names", func() [][2]string {
				r := envRequest(http.MethodPut, "b", "o", "versionId=v1", "X-Amz-Copy-Source", "/src/k?versionId=v2")
				return authz.AmazonPairsOnKey(cfg, r, policy.S3GetObjectVersion, meta.ObjKey{Name: "k", Instance: "v2"})
			}, "v2"),
			Entry("a DeleteObjects key, whatever version the query names", func() [][2]string {
				r := envRequest(http.MethodPost, "b", "", "delete&versionId=v1")
				return authz.AmazonPairsOnKey(cfg, r, policy.S3DeleteObjectVersion, meta.ObjKey{Name: "k", Instance: "v2"})
			}, "v2"),
			Entry("a DeleteObjects key naming the null version", func() [][2]string {
				r := envRequest(http.MethodPost, "b", "", "delete")
				return authz.AmazonPairsOnKey(cfg, r, policy.S3DeleteObjectVersion, meta.ObjKey{Name: "k", Instance: "null"})
			}, "null"),
			Entry("UploadPartCopy's source state, read at the version it names", func() [][2]string {
				r := envRequest(http.MethodPut, "b", "o", "partNumber=1&uploadId=u&versionId=v1", "X-Amz-Copy-Source", "/src/k?versionId=v2")
				st := &op.ObjectState{Key: meta.ObjKey{Name: "k", Instance: "v2"}, Exists: true}
				return authz.AmazonPairsOnState(cfg, r, policy.S3GetObjectVersion, st)
			}, "v2"),
			Entry("a source state read at the null version, whose id is empty", func() [][2]string {
				r := envRequest(http.MethodPut, "b", "o", "", "X-Amz-Copy-Source", "/src/k?versionId=null")
				st := &op.ObjectState{Key: meta.ObjKey{Name: "k"}, Exists: true}
				return authz.AmazonPairsOnState(cfg, r, policy.S3GetObjectVersion, st)
			}, "null"),
		)

		It("set the S3 keys for S3's actions alone", func() {
			r := envRequest(http.MethodPut, "b", "o", "versionId=v1", "X-Amz-Storage-Class", "STANDARD_IA")
			r.AuthRoute = op.RouteSigV4Header
			Expect(authz.AmazonPairs(cfg, r, policy.IAMGetUser)).To(HaveLen(3))
			Expect(authz.AmazonPairs(cfg, r, policy.S3ObjectLambdaGetObject)).To(HaveLen(3))
		})

		It("reach a Deny, and leave the environment radosgw's", func() {
			r := envRequest(http.MethodGet, "b", "o", "")
			r.AuthRoute = op.RouteSigV4Query
			env := authz.BuildEnv(cfg, r, policy.S3GetObject)
			Expect(denies(env, policy.S3GetObject, `{"Bool": {"aws:SecureTransport": "false"}}`)).To(Equal(policy.Deny))
			Expect(denies(env, policy.S3GetObject, `{"StringEquals": {"s3:authType": "REST-QUERY-STRING"}}`)).To(Equal(policy.Deny))
			Expect(env.Lookup("aws:SecureTransport")).To(BeNil())
			Expect(env.Lookup("s3:authType")).To(BeNil())
			Expect(env.Lookup("aws:CurrentTime")).To(Equal([]string{"1790510400"}))
		})

		It("reach the missing-object check's Deny", func() {
			r := envRequest(http.MethodGet, "b", "", "")
			r.AuthRoute = op.RouteSigV4Header
			env := authz.BaseEnv(cfg, r)
			Expect(denies(env, policy.S3ListBucket, `{"Bool": {"aws:SecureTransport": "false"}}`)).To(Equal(policy.Deny))
			Expect(denies(env, policy.S3ListBucket, `{"StringNotEquals": {"s3:x-amz-content-sha256": "UNSIGNED-PAYLOAD"}}`)).
				To(Equal(policy.Pass))
		})
	})
})
