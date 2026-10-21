package auth_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // SigV2 signs with HMAC-SHA1
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/auth"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/memstore"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/op/opfakes"
)

// AWS's SigV4 POST example
// (https://docs.aws.amazon.com/AmazonS3/latest/API/sigv4-post-example.html):
// its key, secret, credential, base64 policy and signature.
const (
	awsPostKey        = "AKIAIOSFODNN7EXAMPLE"
	awsPostSecret     = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
	awsPostCredential = "AKIAIOSFODNN7EXAMPLE/20151229/us-east-1/s3/aws4_request"
	awsPostPolicy     = "eyAiZXhwaXJhdGlvbiI6ICIyMDE1LTEyLTMwVDEyOjAwOjAwLjAwMFoiLA0KICAiY29uZGl0aW9ucyI6IFsNCiAgICB7ImJ1Y2tldCI6ICJzaWd2NGV4YW1wbGVidWNrZXQifSwNCiAgICBbInN0YXJ0cy13aXRoIiwgIiRrZXkiLCAidXNlci91c2VyMS8iXSwNCiAgICB7ImFjbCI6ICJwdWJsaWMtcmVhZCJ9LA0KICAgIHsic3VjY2Vzc19hY3Rpb25fcmVkaXJlY3QiOiAiaHR0cDovL3NpZ3Y0ZXhhbXBsZWJ1Y2tldC5zMy5hbWF6b25hd3MuY29tL3N1Y2Nlc3NmdWxfdXBsb2FkLmh0bWwifSwNCiAgICBbInN0YXJ0cy13aXRoIiwgIiRDb250ZW50LVR5cGUiLCAiaW1hZ2UvIl0sDQogICAgeyJ4LWFtei1tZXRhLXV1aWQiOiAiMTQzNjUxMjM2NTEyNzQifSwNCiAgICB7IngtYW16LXNlcnZlci1zaWRlLWVuY3J5cHRpb24iOiAiQUVTMjU2In0sDQogICAgWyJzdGFydHMtd2l0aCIsICIkeC1hbXotbWV0YS10YWciLCAiIl0sDQoNCiAgICB7IngtYW16LWNyZWRlbnRpYWwiOiAiQUtJQUlPU0ZPRE5ON0VYQU1QTEUvMjAxNTEyMjkvdXMtZWFzdC0xL3MzL2F3czRfcmVxdWVzdCJ9LA0KICAgIHsieC1hbXotYWxnb3JpdGhtIjogIkFXUzQtSE1BQy1TSEEyNTYifSwNCiAgICB7IngtYW16LWRhdGUiOiAiMjAxNTEyMjlUMDAwMDAwWiIgfQ0KICBdDQp9"
	awsPostSignature  = "8afdbf4008c03f22c2cd3cdb72e4afbb1f6a588f3255ac628749a66d7f09699e"
)

// s3testsPolicy and its signatures under alice's and carol's secrets are
// SigV2 form vectors computed outside Go, by Python's hmac, as s3-tests'
// test_s3.py signs a form: base64 of HMAC-SHA1 of the base64 policy.
// s3testsCRLFSignature signs the policy followed by a CRLF.
const (
	s3testsPolicy         = "eyJleHBpcmF0aW9uIjogIjIwMTUtMDgtMzBUMTM6MzY6MDBaIiwgImNvbmRpdGlvbnMiOiBbeyJidWNrZXQiOiAiYiJ9LCBbInN0YXJ0cy13aXRoIiwgIiRrZXkiLCAiZm9vIl0sIHsiYWNsIjogInByaXZhdGUifSwgWyJjb250ZW50LWxlbmd0aC1yYW5nZSIsIDAsIDEwMjRdXX0="
	s3testsAliceSignature = "YLXlDXCBzbGLZaboypMPiYnavrE="
	s3testsCarolSignature = "+UI9DPCVvaK2TLvzcO+RqplEVO4="
	s3testsCRLFSignature  = "JYwngcH09Uptie1jnrM4yqtTFZ0="
)

// v2Form is a SigV2 form's credentials for key, signed with secret over
// policy.
func v2Form(key, secret, policy string) op.BrowserUploadCredentials {
	h := hmac.New(sha1.New, []byte(secret))
	h.Write([]byte(policy))
	return op.BrowserUploadCredentials{AccessKey: key, Signature: base64.StdEncoding.EncodeToString(h.Sum(nil)), Policy: policy}
}

// v4Form is a SigV4 form's credentials naming credential, signed with
// secret over policy under the signing key of signScope, AWS's derivation:
// HMAC-SHA256 down the scope's date, region and service, then
// aws4_request.
func v4Form(credential, signScope, secret, policy string) op.BrowserUploadCredentials {
	mac := func(key []byte, msg string) []byte {
		h := hmac.New(sha256.New, key)
		h.Write([]byte(msg))
		return h.Sum(nil)
	}
	f := strings.SplitN(signScope, "/", 4)
	for len(f) < 3 {
		f = append(f, "")
	}
	k := mac(mac(mac(mac([]byte("AWS4"+secret), f[0]), f[1]), f[2]), "aws4_request")
	return op.BrowserUploadCredentials{
		Algorithm: "AWS4-HMAC-SHA256", Credential: credential, Signature: hex.EncodeToString(mac(k, policy)), Policy: policy,
	}
}

// aliceV4 is a SigV4 form of alice's over the s3-tests policy.
func aliceV4() op.BrowserUploadCredentials {
	const scope = "20150830/us-east-1/s3/aws4_request"
	return v4Form(accessKeyOf("alice")+"/"+scope, scope, secretOf("alice"), s3testsPolicy)
}

// refusedAsAccessDenied asserts a refused browser upload: get_policy answers
// every failed authentication -EACCES, and no result stands in for one.
func refusedAsAccessDenied(res *op.AuthResult, err error, what string) {
	GinkgoHelper()
	Expect(err).To(HaveOccurred(), what)
	Expect(op.AsError(err)).To(SatisfyAll(
		HaveField("Code", "AccessDenied"), HaveField("Status", http.StatusForbidden), HaveField("Message", ""),
	), what)
	Expect(res).To(BeNil(), what)
}

var _ = Describe("Verifier.AuthenticateBrowserUpload", func() {
	var (
		store *memstore.Store
		cfg   auth.Config
		v     *auth.Verifier
	)

	BeforeEach(func() {
		store = memstore.New(memstore.Config{Release: denc.Squid, Now: fixedNow})
		store.AddUser(user("alice", func(u *meta.UserInfo) {
			u.OpMask = op.OpTypeRead
			u.Caps = meta.Caps{"users": 1}
		}))
		store.AddUser(user("t1$bob", nil))
		store.AddUser(user("carol", func(u *meta.UserInfo) { u.System = 1 }))
		store.AddUser(user("dave", func(u *meta.UserInfo) { u.Admin = 1 }))
		store.AddUser(user("erin", func(u *meta.UserInfo) {
			u.AccessKeys["AKERINAPP"] = meta.AccessKey{ID: "AKERINAPP", Secret: "secret-erin-app", Subuser: "erin:app", Active: true}
			u.SubUsers = map[string]meta.SubUser{"erin:app": {Name: "erin:app", Perm: 0x1}}
		}))
		store.AddUser(user("frank", func(u *meta.UserInfo) { u.AccountID = "RGW00000000000000001" }))
		store.AddAccount(meta.AccountInfo{ID: "RGW00000000000000001", Tenant: "acct-tenant", Name: "acme", MaxBuckets: 7})
		store.AddUser(user("grace", func(u *meta.UserInfo) {
			k := u.AccessKeys["AKGRACE"]
			k.Active = false
			u.AccessKeys["AKGRACE"] = k
		}))
		store.AddUser(user("henry", func(u *meta.UserInfo) { u.AccountID = "RGW00000000000000009" })) // no such account
		store.AddUser(user("example", func(u *meta.UserInfo) {
			u.AccessKeys = map[string]meta.AccessKey{awsPostKey: {ID: awsPostKey, Secret: awsPostSecret, Active: true}}
		}))
		cfg = auth.DefaultConfig()
		cfg.Now = fixedNow
		v = auth.New(cfg, store, store)
	})

	// headerIdentity is the identity the header path gives id's request.
	headerIdentity := func(ctx context.Context, id string) op.Identity {
		res, err := v.Authenticate(ctx, signedAs(ctx, newGet(ctx, "http://s3.example.com/"), id), anyPayload)
		Expect(err).NotTo(HaveOccurred(), "user %q", id)
		return res.Identity
	}

	Context("with SigV2", func() {
		It("authenticates s3-tests' form as the key's user, with the identity the header path gives", func(ctx SpecContext) {
			res, err := v.AuthenticateBrowserUpload(ctx, op.BrowserUploadCredentials{
				AccessKey: accessKeyOf("alice"), Signature: s3testsAliceSignature, Policy: s3testsPolicy,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Identity).To(Equal(headerIdentity(ctx, "alice")))
			Expect(res.Identity.Anonymous).To(BeFalse())
			Expect(res.Identity.Unverified).To(BeFalse())
			Expect(res.Route).To(Equal(op.RouteBrowserUpload))
			Expect(res.PayloadSHA256).To(Equal("UNSIGNED-PAYLOAD"))
			Expect(res.Body).To(BeNil(), "the form's body is the request's own")
			Expect(res.QueryMeta).To(BeEmpty())
			Expect(res.Trailers).To(BeNil())
		})

		It("authenticates a system user's s3-tests form", func(ctx SpecContext) {
			res, err := v.AuthenticateBrowserUpload(ctx, op.BrowserUploadCredentials{
				AccessKey: accessKeyOf("carol"), Signature: s3testsCarolSignature, Policy: s3testsPolicy,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Identity.User.UserID).To(Equal(meta.UserID{ID: "carol"}))
			Expect(res.Identity.System).To(BeTrue())
			Expect(res.Identity.Admin).To(BeTrue(), "is_admin_of is admin || system")
		})

		It("signs the policy field as sent, untrimmed", func(ctx SpecContext) {
			withCRLF := op.BrowserUploadCredentials{AccessKey: accessKeyOf("alice"), Signature: s3testsCRLFSignature, Policy: s3testsPolicy + "\r\n"}
			_, err := v.AuthenticateBrowserUpload(ctx, withCRLF)
			Expect(err).NotTo(HaveOccurred(), "a signature over the policy and its CRLF")
			withCRLF.Signature = s3testsAliceSignature
			res, err := v.AuthenticateBrowserUpload(ctx, withCRLF)
			refusedAsAccessDenied(res, err, "a signature over the policy without its CRLF")
		})

		It("reads AWSAccessKeyId alone, whatever x-amz-credential names", func(ctx SpecContext) {
			creds := v2Form(accessKeyOf("alice"), secretOf("alice"), s3testsPolicy)
			creds.Credential = accessKeyOf("t1$bob") + "/20150830/us-east-1/s3/aws4_request"
			res, err := v.AuthenticateBrowserUpload(ctx, creds)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Identity.AccessKey).To(Equal(accessKeyOf("alice")))
		})

		It("takes every algorithm but AWS4-HMAC-SHA256 for SigV2, AWS4-HMAC-SHA256 in another case included", func(ctx SpecContext) {
			for _, alg := range []string{"", "aws4-hmac-sha256", "AWS4-HMAC-SHA256 ", "AWS4-ECDSA-P256-SHA256"} {
				creds := v2Form(accessKeyOf("alice"), secretOf("alice"), s3testsPolicy)
				creds.Algorithm = alg
				res, err := v.AuthenticateBrowserUpload(ctx, creds)
				Expect(err).NotTo(HaveOccurred(), "algorithm %q", alg)
				Expect(res.Identity.AccessKey).To(Equal(accessKeyOf("alice")), "algorithm %q", alg)
			}
			creds := aliceV4()
			creds.Algorithm = "aws4-hmac-sha256"
			res, err := v.AuthenticateBrowserUpload(ctx, creds)
			refusedAsAccessDenied(res, err, "SigV4 fields under another case of the algorithm read as SigV2, which has no key")
		})

		DescribeTable("refuses a form that does not authenticate as AccessDenied",
			func(ctx SpecContext, creds func() op.BrowserUploadCredentials) {
				res, err := v.AuthenticateBrowserUpload(ctx, creds())
				refusedAsAccessDenied(res, err, "")
			},
			Entry("a signature over another policy", func() op.BrowserUploadCredentials {
				c := v2Form(accessKeyOf("alice"), secretOf("alice"), "e30=")
				c.Policy = s3testsPolicy
				return c
			}),
			Entry("another user's secret", func() op.BrowserUploadCredentials {
				return v2Form(accessKeyOf("alice"), secretOf("t1$bob"), s3testsPolicy)
			}),
			Entry("a signature without its base64 padding", func() op.BrowserUploadCredentials {
				return op.BrowserUploadCredentials{
					AccessKey: accessKeyOf("alice"), Signature: strings.TrimRight(s3testsAliceSignature, "="), Policy: s3testsPolicy,
				}
			}),
			Entry("an empty signature", func() op.BrowserUploadCredentials {
				return op.BrowserUploadCredentials{AccessKey: accessKeyOf("alice"), Policy: s3testsPolicy}
			}),
			Entry("an empty access key", func() op.BrowserUploadCredentials {
				c := v2Form(accessKeyOf("alice"), secretOf("alice"), s3testsPolicy)
				c.AccessKey = ""
				return c
			}),
			Entry("no field at all", func() op.BrowserUploadCredentials { return op.BrowserUploadCredentials{} }),
			Entry("an unknown key", func() op.BrowserUploadCredentials {
				return v2Form("AKNOBODY", "secret-nobody", s3testsPolicy)
			}),
			Entry("an inactive key", func() op.BrowserUploadCredentials {
				return v2Form(accessKeyOf("grace"), secretOf("grace"), s3testsPolicy)
			}),
			Entry("a key holding a NUL", func() op.BrowserUploadCredentials {
				return v2Form(accessKeyOf("alice")+"\x00", secretOf("alice"), s3testsPolicy)
			}),
			Entry("SigV4's fields alone", func() op.BrowserUploadCredentials {
				c := aliceV4()
				c.Algorithm = ""
				return c
			}),
		)
	})

	Context("with SigV4", func() {
		It("authenticates AWS's published SigV4 POST example", func(ctx SpecContext) {
			res, err := v.AuthenticateBrowserUpload(ctx, op.BrowserUploadCredentials{
				Algorithm: "AWS4-HMAC-SHA256", Credential: awsPostCredential, Signature: awsPostSignature, Policy: awsPostPolicy,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Identity.User.UserID).To(Equal(meta.UserID{ID: "example"}))
			Expect(res.Identity.AccessKey).To(Equal(awsPostKey))
			Expect(res.Route).To(Equal(op.RouteBrowserUpload))
		})

		It("signs as these specs do: their signer reproduces AWS's example", func() {
			Expect(v4Form(awsPostCredential, "20151229/us-east-1/s3/aws4_request", awsPostSecret, awsPostPolicy).Signature).
				To(Equal(awsPostSignature))
		})

		It("authenticates a form aws-sdk-go-v2 presigns, as the key's user with the header path's identity", func(ctx SpecContext) {
			client := awss3.NewPresignClient(awss3.NewFromConfig(aws.Config{
				Region:      "us-east-1",
				Credentials: credentials.NewStaticCredentialsProvider(accessKeyOf("alice"), secretOf("alice"), ""),
			}))
			post, err := client.PresignPostObject(ctx, &awss3.PutObjectInput{Bucket: aws.String("b"), Key: aws.String("foo")})
			Expect(err).NotTo(HaveOccurred())
			res, err := v.AuthenticateBrowserUpload(ctx, op.BrowserUploadCredentials{
				Algorithm:  post.Values["X-Amz-Algorithm"],
				Credential: post.Values["X-Amz-Credential"],
				Signature:  post.Values["X-Amz-Signature"],
				Policy:     post.Values["policy"],
			})
			Expect(err).NotTo(HaveOccurred(), "fields %v", post.Values)
			Expect(res.Identity).To(Equal(headerIdentity(ctx, "alice")))
		})

		It("checks neither the scope's date, region nor service, nor what follows the service", func(ctx SpecContext) {
			for _, scope := range []string{
				"19700101/nowhere/sqs/aws4_request",
				"20150830/us-east-1/s3/not_aws4_request",
				"20150830/us-east-1/s3/aws4_request/extra",
			} {
				res, err := v.AuthenticateBrowserUpload(ctx, v4Form(accessKeyOf("alice")+"/"+scope, scope, secretOf("alice"), s3testsPolicy))
				Expect(err).NotTo(HaveOccurred(), "scope %q", scope)
				Expect(res.Identity.AccessKey).To(Equal(accessKeyOf("alice")), "scope %q", scope)
			}
		})

		It("reads a scope of fewer than three fields as parse_cred_scope does, a missing field taking the rest whole", func(ctx SpecContext) {
			for scope, signed := range map[string]string{
				"20150830":           "20150830/20150830/20150830",
				"20150830/us-east-1": "20150830/us-east-1/us-east-1",
				"":                   "//",
			} {
				res, err := v.AuthenticateBrowserUpload(ctx, v4Form(accessKeyOf("alice")+"/"+scope, signed, secretOf("alice"), s3testsPolicy))
				Expect(err).NotTo(HaveOccurred(), "scope %q", scope)
				Expect(res.Identity.AccessKey).To(Equal(accessKeyOf("alice")), "scope %q", scope)
			}
		})

		It("takes the key id up to the credential's first '/', a subuser's key included", func(ctx SpecContext) {
			const scope = "20150830/us-east-1/s3/aws4_request"
			res, err := v.AuthenticateBrowserUpload(ctx, v4Form("AKERINAPP/"+scope, scope, "secret-erin-app", s3testsPolicy))
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Identity.AccessKey).To(Equal("AKERINAPP"))
			Expect(res.Identity.SubUser).To(Equal("erin:app"))
			Expect(res.Identity.User.UserID).To(Equal(meta.UserID{ID: "erin"}))
		})

		It("reads x-amz-credential alone, whatever AWSAccessKeyId names", func(ctx SpecContext) {
			creds := aliceV4()
			creds.AccessKey = accessKeyOf("t1$bob")
			res, err := v.AuthenticateBrowserUpload(ctx, creds)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Identity.AccessKey).To(Equal(accessKeyOf("alice")))
		})

		DescribeTable("refuses a form that does not authenticate as AccessDenied",
			func(ctx SpecContext, creds func() op.BrowserUploadCredentials) {
				res, err := v.AuthenticateBrowserUpload(ctx, creds())
				refusedAsAccessDenied(res, err, "")
			},
			Entry("a signature under another scope than the credential's", func() op.BrowserUploadCredentials {
				return v4Form(accessKeyOf("alice")+"/20150830/us-east-1/s3/aws4_request", "20150831/us-east-1/s3/aws4_request",
					secretOf("alice"), s3testsPolicy)
			}),
			Entry("another user's secret", func() op.BrowserUploadCredentials {
				const scope = "20150830/us-east-1/s3/aws4_request"
				return v4Form(accessKeyOf("alice")+"/"+scope, scope, secretOf("t1$bob"), s3testsPolicy)
			}),
			Entry("a signature in upper-case hex", func() op.BrowserUploadCredentials {
				c := aliceV4()
				c.Signature = strings.ToUpper(c.Signature)
				return c
			}),
			Entry("a signature over another policy", func() op.BrowserUploadCredentials {
				c := aliceV4()
				c.Policy = "e30="
				return c
			}),
			Entry("an empty signature", func() op.BrowserUploadCredentials {
				c := aliceV4()
				c.Signature = ""
				return c
			}),
			Entry("an empty credential", func() op.BrowserUploadCredentials {
				c := aliceV4()
				c.Credential = ""
				return c
			}),
			Entry("a credential with an empty key id", func() op.BrowserUploadCredentials {
				const scope = "20150830/us-east-1/s3/aws4_request"
				return v4Form("/"+scope, scope, secretOf("alice"), s3testsPolicy)
			}),
			Entry("a credential with no '/', signed as radosgw takes it, whole as the key id and the scope", func() op.BrowserUploadCredentials {
				k := accessKeyOf("alice")
				return v4Form(k, k+"/"+k+"/"+k, secretOf("alice"), s3testsPolicy)
			}),
			Entry("an unknown key", func() op.BrowserUploadCredentials {
				const scope = "20150830/us-east-1/s3/aws4_request"
				return v4Form("AKNOBODY/"+scope, scope, "secret-nobody", s3testsPolicy)
			}),
			Entry("an inactive key", func() op.BrowserUploadCredentials {
				const scope = "20150830/us-east-1/s3/aws4_request"
				return v4Form(accessKeyOf("grace")+"/"+scope, scope, secretOf("grace"), s3testsPolicy)
			}),
			Entry("SigV2's fields alone", func() op.BrowserUploadCredentials {
				c := v2Form(accessKeyOf("alice"), secretOf("alice"), s3testsPolicy)
				c.Algorithm = "AWS4-HMAC-SHA256"
				return c
			}),
		)
	})

	Context("the identity", func() {
		DescribeTable("is the one the header path gives the key",
			func(ctx SpecContext, id string) {
				res, err := v.AuthenticateBrowserUpload(ctx, v2Form(accessKeyOf(id), secretOf(id), s3testsPolicy))
				Expect(err).NotTo(HaveOccurred())
				Expect(res.Identity).To(Equal(headerIdentity(ctx, id)))
			},
			Entry("a tenant's user, under the tenant", "t1$bob"),
			Entry("an admin", "dave"),
			Entry("an account's user, owning as the account", "frank"),
		)

		It("takes no rgwx-uid for a system user: the form signs nothing of the query", func(ctx SpecContext) {
			res, err := v.AuthenticateBrowserUpload(ctx, v2Form(accessKeyOf("carol"), secretOf("carol"), s3testsPolicy))
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Identity.Owner).To(Equal(meta.UserOwner(meta.UserID{ID: "carol"})))
			Expect(res.Identity.Tenant).To(BeEmpty())
		})

		It("carries the key's user's stored attrs, where its identity policies live", func(ctx SpecContext) {
			rec, err := store.GetUser(ctx, meta.UserID{ID: "alice"})
			Expect(err).NotTo(HaveOccurred())
			rec.Attrs = map[string][]byte{"user.rgw.user-policy": []byte("policies")}
			users := &opfakes.FakeUserStore{}
			users.GetUserByAccessKeyReturns(rec, nil)
			res, err := auth.New(cfg, users, nil).AuthenticateBrowserUpload(ctx, v2Form(accessKeyOf("alice"), secretOf("alice"), s3testsPolicy))
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Identity.Attrs).To(Equal(rec.Attrs))
			_, key := users.GetUserByAccessKeyArgsForCall(0)
			Expect(key).To(Equal(accessKeyOf("alice")))
		})

		It("is refused for an account user whose account does not load, or with no account store", func(ctx SpecContext) {
			buf := captureLog()
			res, err := v.AuthenticateBrowserUpload(ctx, v2Form(accessKeyOf("henry"), secretOf("henry"), s3testsPolicy))
			Expect(logRecords(buf)).To(ConsistOf(HaveKeyWithValue("account_id", "RGW00000000000000009")))
			refusedAsAccessDenied(res, err, "no such account")
			res, err = auth.New(cfg, store, nil).AuthenticateBrowserUpload(ctx, v2Form(accessKeyOf("frank"), secretOf("frank"), s3testsPolicy))
			refusedAsAccessDenied(res, err, "no account store")
		})

		It("ignores a session token, as the local engine does on the header path", func(ctx SpecContext) {
			creds := v2Form(accessKeyOf("alice"), secretOf("alice"), s3testsPolicy)
			creds.SecurityToken = "not-a-token"
			res, err := v.AuthenticateBrowserUpload(ctx, creds)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Identity).To(Equal(headerIdentity(ctx, "alice")))
			v4 := aliceV4()
			v4.SecurityToken = "not-a-token"
			_, err = v.AuthenticateBrowserUpload(ctx, v4)
			Expect(err).NotTo(HaveOccurred(), "SigV4")
		})
	})

	It("refuses an empty key id or signature before it looks the key up, as AWSEngine::authenticate does", func(ctx SpecContext) {
		users := &opfakes.FakeUserStore{}
		users.GetUserByAccessKeyReturns(nil, op.ErrNoSuchUser)
		verifier := auth.New(cfg, users, nil)
		for what, creds := range map[string]op.BrowserUploadCredentials{
			"SigV2 without a signature":    {AccessKey: accessKeyOf("alice"), Policy: s3testsPolicy},
			"SigV2 without a key":          {Signature: s3testsAliceSignature, Policy: s3testsPolicy},
			"SigV4 without a signature":    {Algorithm: "AWS4-HMAC-SHA256", Credential: accessKeyOf("alice") + "/20150830/us-east-1/s3/aws4_request", Policy: s3testsPolicy},
			"SigV4 with an empty key id":   {Algorithm: "AWS4-HMAC-SHA256", Credential: "/20150830/us-east-1/s3/aws4_request", Signature: "00", Policy: s3testsPolicy},
			"SigV4 with a credential of /": {Algorithm: "AWS4-HMAC-SHA256", Credential: "/", Signature: "00", Policy: s3testsPolicy},
		} {
			res, err := verifier.AuthenticateBrowserUpload(ctx, creds)
			refusedAsAccessDenied(res, err, what)
		}
		Expect(users.GetUserByAccessKeyCallCount()).To(BeZero())
	})

	It("is refused when rgw_s3_auth_use_rados leaves no backend", func(ctx SpecContext) {
		cfg.UseRados = false
		res, err := auth.New(cfg, store, store).AuthenticateBrowserUpload(ctx, v2Form(accessKeyOf("alice"), secretOf("alice"), s3testsPolicy))
		refusedAsAccessDenied(res, err, "SigV2")
		res, err = auth.New(cfg, store, store).AuthenticateBrowserUpload(ctx, aliceV4())
		refusedAsAccessDenied(res, err, "SigV4")
	})

	It("authenticates with rgw_s3_auth_disable_signature_url set, which the browser-upload abstractor does not read", func(ctx SpecContext) {
		cfg.DisablePresignedURLs = true
		res, err := auth.New(cfg, store, store).AuthenticateBrowserUpload(ctx, aliceV4())
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Identity.AccessKey).To(Equal(accessKeyOf("alice")))
	})

	It("names none of the form's credentials in a refusal or in its log", func(ctx SpecContext) {
		buf := captureLog()
		users := &opfakes.FakeUserStore{}
		users.GetUserByAccessKeyReturns(nil, fmt.Errorf("%w: reading users.keys/AKALICE: rados: timed out", op.ErrServiceUnavailable))
		creds := aliceV4()
		res, err := auth.New(cfg, users, nil).AuthenticateBrowserUpload(ctx, creds)
		refusedAsAccessDenied(res, err, "a store failure")
		recs := logRecords(buf)
		Expect(recs).To(ConsistOf(SatisfyAll(HaveKeyWithValue("level", "ERROR"), HaveKeyWithValue("code", "ServiceUnavailable"))))
		for _, secret := range []string{accessKeyOf("alice"), secretOf("alice"), creds.Signature, creds.Credential} {
			Expect(err.Error()).NotTo(ContainSubstring(secret))
			Expect(buf.String()).NotTo(ContainSubstring(secret))
		}
		for _, c := range []op.BrowserUploadCredentials{v2Form("AKNOBODY", "x", s3testsPolicy), v2Form(accessKeyOf("alice"), "x", s3testsPolicy)} {
			_, err := v.AuthenticateBrowserUpload(ctx, c)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).NotTo(ContainSubstring(c.AccessKey))
			Expect(err.Error()).NotTo(ContainSubstring(c.Signature))
		}
	})

	It("ends a key lookup its client's departure cut short as a gone client's", func(ctx SpecContext) {
		rctx, cancel := context.WithCancel(ctx)
		defer cancel()
		users := &opfakes.FakeUserStore{}
		users.GetUserByAccessKeyStub = func(ctx context.Context, _ string) (*op.UserRecord, error) {
			cancel()
			return nil, fmt.Errorf("reading users.keys/AKALICE: %w", ctx.Err())
		}
		res, err := auth.New(cfg, users, nil).AuthenticateBrowserUpload(rctx, aliceV4())
		refusedAsAccessDenied(res, err, "")
		Expect(err).To(MatchError(context.Canceled), "the protocol handler ends the request instead of answering it")
	})
})
