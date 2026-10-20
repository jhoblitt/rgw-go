package postpolicy_test

import (
	"slices"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/postpolicy"
)

var _ = Describe("Check", func() {
	// Each entry is one of s3-tests' POST policies (test_s3.py at the pinned
	// s3-tests, named in the entry) with its form, and the refusal radosgw
	// answers, "" for none. The tests that a refusal at Parse or at the
	// upload's length ends are in the Parse specs.
	DescribeTable("answers s3-tests' policies as radosgw does",
		func(conds, fields []string, bucket, want string) {
			p := mustParse(policyDoc(future, conds...))
			Expect(checkRefusal(p, v2Env(bucket, fields...), now)).To(Equal(want))
		},
		Entry("test_post_object_authenticated_request", baseConds, baseFields, "bkt", ""),
		Entry("test_post_object_authenticated_no_content_type",
			[]string{`{"bucket": "bkt"}`, `["starts-with", "$key", "foo"]`, `{"acl": "private"}`, `["content-length-range", 0, 1024]`},
			[]string{"key", "foo.txt", "AWSAccessKeyId", "AKID", "acl", "private", "signature", "SIG", "policy", "POLICY"}, "bkt", ""),
		Entry("test_post_object_ignored_header", baseConds, slices.Concat(baseFields, []string{"x-ignore-foo", "bar"}), "bkt", ""),
		Entry("test_post_object_case_insensitive_condition_fields",
			[]string{
				`{"bUcKeT": "bkt"}`, `["StArTs-WiTh", "$KeY", "foo"]`, `{"AcL": "private"}`,
				`["StArTs-WiTh", "$CoNtEnT-TyPe", "text/plain"]`, `["content-length-range", 0, 1024]`,
			},
			[]string{"kEy", "foo.txt", "AWSAccessKeyId", "AKID", "aCl", "private", "signature", "SIG", "pOLICy", "POLICY", "Content-Type", "text/plain"},
			"bkt", ""),
		Entry("test_post_object_escaped_field_values",
			[]string{
				`{"bucket": "bkt"}`, `["starts-with", "$key", "\\$foo"]`, `{"acl": "private"}`,
				`["starts-with", "$Content-Type", "text/plain"]`, `["content-length-range", 0, 1024]`,
			},
			slices.Concat([]string{"key", `\$foo.txt`}, baseFields[2:]), "bkt", ""),
		Entry("test_post_object_success_redirect_action",
			withBase(`["eq", "$success_action_redirect", "http://h/bkt"]`),
			slices.Concat(baseFields, []string{"success_action_redirect", "http://h/bkt"}), "bkt", ""),
		Entry("test_post_object_missing_policy_condition, which fails first on its key's prefix",
			[]string{
				`["starts-with", "$key", "\\$foo"]`, `{"acl": "private"}`,
				`["starts-with", "$Content-Type", "text/plain"]`, `["content-length-range", 0, 1024]`,
			},
			baseFields, "bkt", `Policy condition failed: starts-with: $key, \$foo`),
		Entry("its policy with the key's prefix met", baseConds[1:], baseFields, "bkt", "Policy missing condition: bucket"),
		Entry("test_post_object_user_specified_header",
			withBase(`["starts-with", "$x-amz-meta-foo",  "bar"]`),
			slices.Concat(baseFields, []string{"x-amz-meta-foo", "barclamp"}), "bkt", ""),
		Entry("test_post_object_request_missing_policy_specified_field",
			withBase(`["starts-with", "$x-amz-meta-foo",  "bar"]`), baseFields, "bkt",
			"Policy condition failed: starts-with: $x-amz-meta-foo, bar"),
		Entry("test_post_object_wrong_bucket, whose bucket field the URL's bucket replaces",
			baseConds, slices.Concat([]string{"bucket", "bkt"}, baseFields), "other",
			"Policy check failed, variable not met condition: bucket"),
		Entry("test_post_object_invalid_request_field_value",
			withBase(`["eq", "$x-amz-meta-foo",  ""]`),
			slices.Concat(baseFields, []string{"x-amz-meta-foo", "barclamp"}), "bkt",
			"Policy condition failed: eq: $x-amz-meta-foo, "),
		Entry("test_post_object_tags_authenticated_request",
			withBase(`["starts-with", "$tagging", ""]`),
			slices.Concat(baseFields, []string{"tagging", "<Tagging><TagSet></TagSet></Tagging>"}), "bkt", ""),
		Entry("test_post_object_upload_checksum, whose checksum field no condition names",
			[]string{
				`{"bucket": "bkt"}`, `["starts-with", "$key", "foo_cksum_test"]`, `{"acl": "private"}`,
				`["starts-with", "$Content-Type", "text/plain"]`, `["content-length-range", 0, 5242880]`,
			},
			slices.Concat([]string{"key", "foo_cksum_test.txt"}, baseFields[2:],
				[]string{"x-amz-checksum-sha256", "aTL9MeXa9HObn6eP93eygxsJlcwdCwCTysgGAZAgE7w="}), "bkt", ""),
		Entry("test_encryption_sse_c_post_object_authenticated_request",
			withBase(`["starts-with", "$x-amz-server-side-encryption-customer-algorithm", ""]`,
				`["starts-with", "$x-amz-server-side-encryption-customer-key", ""]`,
				`["starts-with", "$x-amz-server-side-encryption-customer-key-md5", ""]`),
			slices.Concat(baseFields, []string{
				"x-amz-server-side-encryption-customer-algorithm", "AES256",
				"x-amz-server-side-encryption-customer-key", "pO3upElrwuEXSoFwCfnZPdSsmt/xWeFa0N9KgDijwVs=",
				"x-amz-server-side-encryption-customer-key-md5", "DWygnHRtgiJ77HCm+1rvHw==",
			}), "bkt", ""),
		Entry("test_sse_kms_post_object_authenticated_request",
			withBase(`["starts-with", "$x-amz-server-side-encryption", ""]`,
				`["starts-with", "$x-amz-server-side-encryption-aws-kms-key-id", ""]`),
			slices.Concat(baseFields, []string{
				"x-amz-server-side-encryption", "aws:kms",
				"x-amz-server-side-encryption-aws-kms-key-id", "testkey-1",
			}), "bkt", ""),
		Entry("test_sse_s3_default_post_object_authenticated_request, a condition on a field the form leaves out",
			withBase(`["starts-with", "$x-amz-server-side-encryption", ""]`), baseFields, "bkt", ""),
	)

	Context("at the expiration", func() {
		var p *postpolicy.Policy

		BeforeEach(func() { p = mustParse(policyDoc(future)) })

		DescribeTable("refuses a policy whose expiration is at or before now, in whole seconds",
			func(at time.Time, want string) {
				Expect(checkRefusal(p, &postpolicy.Env{}, at)).To(Equal(want))
			},
			Entry("a second before", futureTime.Add(-time.Second), ""),
			Entry("a nanosecond before, the same second as ceph_clock_now's", futureTime.Add(-time.Nanosecond), ""),
			Entry("at it", futureTime, "Policy expired"),
			Entry("most of a second after, the same whole second", futureTime.Add(999*time.Millisecond), "Policy expired"),
			Entry("an hour after", futureTime.Add(time.Hour), "Policy expired"),
			Entry("s3-tests' 6000 s after (test_post_object_expired_policy)", futureTime.Add(6000*time.Second), "Policy expired"),
		)

		It("refuses an expiration before 1970, which radosgw's unsigned arithmetic never reaches", func() {
			p := mustParse(policyDoc("1969-12-31T23:59:59Z"))
			Expect(checkRefusal(p, &postpolicy.Env{}, now)).To(Equal("Policy expired"))
		})

		It("checks the expiration before any condition", func() {
			p := mustParse(policyDoc(past, `{"bucket": "nope"}`, `["eq", "$key", "nope"]`))
			Expect(checkRefusal(p, v2Env("bkt"), now)).To(Equal("Policy expired"))
		})
	})

	Context("an object condition", func() {
		It("refuses a field the form lacks", func() {
			p := mustParse(policyDoc(future, `{"acl": "private"}`))
			Expect(checkRefusal(p, v2Env("bkt"), now)).To(Equal("Policy check failed, variable not found: acl"))
		})

		It("refuses a value that differs, even in case", func() {
			p := mustParse(policyDoc(future, `{"acl": "private"}`))
			Expect(checkRefusal(p, v2Env("bkt", "acl", "Private"), now)).To(Equal("Policy check failed, variable not met condition: acl"))
		})

		It("names the field as the policy spells it", func() {
			p := mustParse(policyDoc(future, `{"ACL": "private"}`))
			Expect(checkRefusal(p, v2Env("bkt", "acl", "x"), now)).To(Equal("Policy check failed, variable not met condition: ACL"))
		})

		It("reads a name starting with $ as that name", func() {
			p := mustParse(policyDoc(future, `{"$acl": "private"}`))
			Expect(checkRefusal(p, v2Env("bkt", "acl", "private"), now)).To(Equal("Policy check failed, variable not found: $acl"))
		})

		It("is checked before every array condition, wherever it stands", func() {
			p := mustParse(policyDoc(future, `["eq", "$key", "nope"]`, `{"acl": "private"}`))
			Expect(checkRefusal(p, v2Env("bkt", "key", "k", "acl", "public-read"), now)).To(Equal("Policy check failed, variable not met condition: acl"))
		})
	})

	Context("an array condition", func() {
		DescribeTable("compares its two operands",
			func(cond string, fields []string, want string) {
				p := mustParse(policyDoc(future, cond, `{"bucket": "bkt"}`))
				Expect(checkRefusal(p, v2Env("bkt", fields...), now)).To(Equal(want))
			},
			Entry("eq, equal", `["eq", "$key", "a/b"]`, []string{"key", "a/b"}, ""),
			Entry("eq, differing in case", `["eq", "$key", "a/b"]`, []string{"key", "A/b"}, "Policy condition failed: eq: $key, a/b"),
			Entry("eq, a prefix only", `["eq", "$key", "a/b"]`, []string{"key", "a/bc"}, "Policy condition failed: eq: $key, a/b"),
			Entry("starts-with, a prefix", `["starts-with", "$key", "a/"]`, []string{"key", "a/b"}, ""),
			Entry("starts-with, the whole value", `["starts-with", "$key", "a/b"]`, []string{"key", "a/b"}, ""),
			Entry("starts-with, a value shorter than the prefix", `["starts-with", "$key", "a/b"]`, []string{"key", "a/"},
				"Policy condition failed: starts-with: $key, a/b"),
			Entry("starts-with, differing in case", `["starts-with", "$key", "a/"]`, []string{"key", "A/b"},
				"Policy condition failed: starts-with: $key, a/"),
			Entry("starts-with an empty prefix, any value", `["starts-with", "$key", ""]`, []string{"key", "x"}, ""),
			Entry("any case of the operator", `["Starts-With", "$key", "a"]`, []string{"key", "ab"}, ""),
			Entry("a variable on the right", `["eq", "abc", "$key"]`, []string{"key", "abc"}, ""),
			Entry("two variables", `["eq", "$key", "$acl"]`, []string{"key", "v", "acl", "v"}, ""),
			Entry("two literals, which check no field", `["eq", "x", "x"]`, []string{"key", "v"}, "Policy missing condition: key"),
			Entry("two literals that differ", `["eq", "x", "y"]`, nil, "Policy condition failed: eq: x, y"),
			Entry("a missing field, read as empty", `["eq", "$x-amz-meta-a", ""]`, nil, ""),
			Entry("$ alone, the variable named empty", `["eq", "$", ""]`, nil, ""),
			Entry("a $ inside the operand, a literal", `["eq", "a$key", "a$key"]`, nil, ""),
		)

		It("reads a variable whatever the case of its name", func() {
			p := mustParse(policyDoc(future, `["eq", "$X-AMZ-META-Foo", "v"]`, `{"bucket": "bkt"}`))
			Expect(checkRefusal(p, v2Env("bkt", "x-amz-meta-fOO", "v"), now)).To(BeEmpty())
		})

		It("refuses on the first condition in the document that fails", func() {
			p := mustParse(policyDoc(future, `["eq", "$key", "a"]`, `["eq", "$acl", "b"]`))
			Expect(checkRefusal(p, v2Env("bkt", "key", "x", "acl", "y"), now)).To(Equal("Policy condition failed: eq: $key, a"))
		})
	})

	Context("a field no condition checks", func() {
		It("is refused, the smallest name named as the form first spells it", func() {
			p := mustParse(policyDoc(future, `{"bucket": "bkt"}`))
			env := v2Env("bkt", "Zed", "1", "X-Amz-Meta-A", "2", "x-amz-meta-a", "3")
			Expect(checkRefusal(p, env, now)).To(Equal("Policy missing condition: X-Amz-Meta-A"))
		})

		It("compares names with ASCII case folding alone", func() {
			p := mustParse(policyDoc(future, `{"bucket": "bkt"}`, `["eq", "$caf`+"\xc3\x89"+`", "v"]`))
			Expect(checkRefusal(p, v2Env("bkt", "caf\xc3\xa9", "v"), now)).To(Equal("Policy condition failed: eq: $caf\xc3\x89, v"))
		})

		It("is the field with which Tentacle sets a canned ACL a policy is silent on", func() {
			smuggled := "acl\x00bbbbbbbbbbbcrc32\x00x-amz-checksum-"
			p := mustParse(policyDoc(future, `{"bucket": "bkt"}`, `["starts-with", "$key", "foo"]`,
				`["starts-with", "$Content-Type", "text/plain"]`, `["content-length-range", 0, 1024]`))
			env := v2Env("bkt", "key", "foo.txt", "AWSAccessKeyId", "AKID", "signature", "SIG", "policy", "POLICY",
				"Content-Type", "text/plain", smuggled, "public-read", "x-amz-checksum-crc32", "NhCmhg==")
			Expect(checkRefusal(p, env, now)).To(Equal("Policy missing condition: " + smuggled))
		})

		It("is any field the caller did not mark", func() {
			p := mustParse(policyDoc(future, baseConds...))
			env := v2Env("bkt", baseFields...)
			env.Add("x-amz-signature", "sig")
			Expect(checkRefusal(p, env, now)).To(Equal("Policy missing condition: x-amz-signature"))
			env.MarkChecked("X-Amz-Signature")
			Expect(checkRefusal(p, env, now)).To(BeEmpty())
		})

		DescribeTable("passes a field radosgw excepts",
			func(name string) {
				p := mustParse(policyDoc(future, `{"bucket": "bkt"}`))
				Expect(checkRefusal(p, v2Env("bkt", name, "v"), now)).To(BeEmpty())
			},
			Entry("x-ignore-", "x-ignore-foo"),
			Entry("x-ignore- in any case", "X-Ignore-Foo"),
			Entry("x-ignore- alone", "x-ignore-"),
			// Tentacle's exception, on both releases: Squid's radosgw refuses
			// these (docs/exclusions.md, "A POST policy needs no condition for a
			// checksum field").
			Entry("x-amz-checksum-algorithm", "x-amz-checksum-algorithm"),
			Entry("CRC32", "x-amz-checksum-crc32"),
			Entry("CRC32C", "x-amz-checksum-crc32c"),
			Entry("CRC64NVME", "x-amz-checksum-crc64nvme"),
			Entry("SHA1", "x-amz-checksum-sha1"),
			Entry("SHA256", "x-amz-checksum-sha256"),
			Entry("SHA256 in capitals", "x-amz-checksum-SHA256"),
			Entry("SHA512", "x-amz-checksum-sha512"),
			Entry("BLAKE3", "x-amz-checksum-blake3"),
			Entry("XXH3, Tentacle's name", "x-amz-checksum-xxh3"),
			Entry("XXHASH3, main's name", "x-amz-checksum-xxhash3"),
		)

		DescribeTable("refuses a field outside the exceptions",
			func(name string) {
				p := mustParse(policyDoc(future, `{"bucket": "bkt"}`))
				Expect(checkRefusal(p, v2Env("bkt", name, "v"), now)).To(Equal("Policy missing condition: " + name))
			},
			Entry("x-ignore without its dash", "x-ignorefoo"),
			Entry("x-amz-checksum-none, which names no algorithm", "x-amz-checksum-none"),
			Entry("x-amz-checksum-type", "x-amz-checksum-type"),
			Entry("the checksum prefix alone", "x-amz-checksum-"),
			Entry("an algorithm with more after it", "x-amz-checksum-crc32x"),
			Entry("an algorithm with a NUL after it", "x-amz-checksum-crc32\x00x-amz-checksum-"),
			// The two shapes Tentacle excepts, the prefix found past a NUL and an
			// algorithm at byte 15 (docs/ceph-upstream-bugs.md, "Tentacle's POST
			// policy excepts a field whose name holds a NUL after a checksum
			// algorithm's").
			Entry("an x-amz-meta- name with an algorithm at byte 15 and the prefix past a NUL",
				"x-amz-meta-aaaacrc32\x00x-amz-checksum-"),
			Entry("acl, a NUL, eleven bytes, an algorithm and the prefix past a NUL",
				"acl\x00bbbbbbbbbbbcrc32\x00x-amz-checksum-"),
			Entry("MD5, which main names and Tentacle has no id for", "x-amz-checksum-md5"),
			Entry("XXHASH64, which main names", "x-amz-checksum-xxhash64"),
			Entry("XXHASH128, which main names", "x-amz-checksum-xxhash128"),
			Entry("the checksum prefix in capitals, which Tentacle matches exactly", "X-Amz-Checksum-Sha256"),
			Entry("x-amz-checksum-algorithm in capitals", "X-Amz-Checksum-Algorithm"),
		)
	})
})
