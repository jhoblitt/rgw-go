package policy_test

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/policy"
)

// The radosgw cases are PolicyTest's and IPPolicyTest's Parse cases
// (src/test/rgw/test_rgw_iam_policy.cc:227-755 and :1143-1200 at v19.2.6),
// which parse for arbitraryTenant and reject invalid principals. The error
// cases follow ParseState (src/rgw/rgw_iam_policy.cc:224-810 at v19.2.6) and
// rapidjson's Reader (src/s3select/rapidjson/include/rapidjson/reader.h at
// rapidjson fcb23c2d, which both tags build).

func parseOpts(r denc.Release) policy.ParseOptions {
	return policy.ParseOptions{Tenant: new(arbitraryTenant), RejectInvalidPrincipals: true, Release: r}
}

func mustParse(text string, opts policy.ParseOptions) *policy.Policy {
	p, err := policy.Parse(text, opts)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	return p
}

// annotationOf is a parse failure's annotation, or a description of an error
// that is not a *policy.ParseError.
func annotationOf(err error) string {
	if pe, ok := errors.AsType[*policy.ParseError](err); ok {
		return pe.Annotation
	}
	return fmt.Sprintf("not a *policy.ParseError: %v", err)
}

// captureLogs sends slog's default logger to a buffer until the spec ends,
// restoring the log package's writer and flags too, which slog.SetDefault
// redirects.
func captureLogs() *bytes.Buffer {
	var buf bytes.Buffer
	oldLogger, oldWriter, oldFlags := slog.Default(), log.Writer(), log.Flags()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	DeferCleanup(func() {
		slog.SetDefault(oldLogger)
		log.SetOutput(oldWriter)
		log.SetFlags(oldFlags)
	})
	return &buf
}

// stmt is a policy whose one statement holds members.
func stmt(members string) string {
	return `{"Version": "2012-10-17", "Statement": {` + members + `}}`
}

// parsedAll is what the parser makes of "<service>:*" on r: every action of
// value that r knows, and the service's All, which the parser sets once all of
// them are set.
func parsedAll(r denc.Release, value policy.ActionSet, all policy.Action) policy.ActionSet {
	var s policy.ActionSet
	for a := range policy.ActionCount {
		if value.Has(a) && policy.Known(a, r) {
			s.Set(a)
		}
	}
	s.Set(all)
	return s
}

// expectPolicy holds got to want, with the action sets compared by name
// first so that a failure names the actions that differ.
func expectPolicy(got, want *policy.Policy) {
	ExpectWithOffset(1, got.Statements).To(HaveLen(len(want.Statements)))
	for i := range want.Statements {
		ExpectWithOffset(1, namesIn(got.Statements[i].Actions)).
			To(ConsistOf(namesIn(want.Statements[i].Actions)), "statement %d's Action", i)
		ExpectWithOffset(1, namesIn(got.Statements[i].NotActions)).
			To(ConsistOf(namesIn(want.Statements[i].NotActions)), "statement %d's NotAction", i)
	}
	ExpectWithOffset(1, got).To(Equal(want))
}

var _ = Describe("Parse on radosgw's cases", func() {
	for _, r := range []denc.Release{denc.Squid, denc.Tentacle} {
		Context("on "+r.String(), func() {
			var (
				opts policy.ParseOptions
				sem  policy.Semantics
			)

			BeforeEach(func() {
				opts = parseOpts(r)
				sem = policy.SemanticsFor(r)
			})

			It("parses one action on one bucket, giving the bucket the tenant (Parse1)", func() {
				expectPolicy(mustParse(testdata("example1.json"), opts), example1())
			})

			It("parses an account principal, s3:* and two resources (Parse2)", func() {
				want := example2()
				want.Statements[0].Actions = parsedAll(r, policy.S3AllValue(), policy.S3All)
				expectPolicy(mustParse(testdata("example2.json"), opts), want)
			})

			It("parses three statements, the release's List and Get actions and a Bool condition (Parse3)", func() {
				expectPolicy(mustParse(testdata("example3.json"), opts), example3(r))
			})

			It("parses one iam action on any resource (Parse4)", func() {
				expectPolicy(mustParse(testdata("example4.json"), opts), example4())
			})

			It("parses iam:* as the iam actions the release knows and iam's All (Parse5)", func() {
				want := example5()
				want.Statements[0].Actions = parsedAll(r, policy.IAMAllValue(), policy.IAMAll)
				expectPolicy(mustParse(testdata("example5.json"), opts), want)
			})

			It("parses * as every action, the service wildcards included (Parse6)", func() {
				expectPolicy(mustParse(testdata("example6.json"), opts), example6())
			})

			It("parses a subuser principal, leaving its account empty (Parse7)", func() {
				expectPolicy(mustParse(testdata("example7.json"), opts), example7())
			})

			It("parses IpAddress and NotIpAddress with their value lists (ParseIPAddress)", func() {
				p := mustParse(testdata("ip_full.json"), opts)
				expectPolicy(p, ipFull())
				conds := p.Statements[0].Conditions
				Expect(conds).To(HaveLen(2))
				Expect(conds[0].Op).To(Equal(policy.OpIPAddress))
				Expect(conds[0].Values).To(Equal([]string{"192.168.1.0/24", "::1"}))
				Expect(conds[1].Op).To(Equal(policy.OpNotIPAddress))
				Expect(conds[1].Values).To(Equal([]string{"192.168.1.1/32", "2001:0db8:85a3:0000:0000:8a2e:0370:7334"}))
			})

			It("keeps the text it parsed byte for byte", func() {
				text := testdata("ip_allow.json")
				Expect(mustParse(text, opts).Text).To(Equal(text))
			})

			Describe("evaluating the parsed documents as the hand-built ones", func() {
				It("Eval1", func() { expectEval1(mustParse(testdata("example1.json"), opts), sem) })
				It("Eval3", func() { expectEval3(mustParse(testdata("example3.json"), opts), r) })
				It("Eval4", func() { expectEval4(mustParse(testdata("example4.json"), opts), sem) })
				It("Eval5", func() { expectEval5(mustParse(testdata("example5.json"), opts), sem) })
				It("Eval6", func() { expectEval6(mustParse(testdata("example6.json"), opts), sem) })
				It("Eval7", func() { expectEval7(mustParse(testdata("example7.json"), opts), sem) })
				It("EvalIPAddress", func() {
					expectEvalIPAddress(
						mustParse(testdata("ip_allow.json"), opts),
						mustParse(testdata("ip_deny.json"), opts),
						mustParse(testdata("ip_full.json"), opts),
						sem)
				})
			})
		})
	}
})

var _ = Describe("Parse", func() {
	var squid, tentacle policy.ParseOptions

	BeforeEach(func() {
		squid = parseOpts(denc.Squid)
		tentacle = parseOpts(denc.Tentacle)
	})

	DescribeTable("refuses a document as radosgw does, with its annotation",
		func(doc string, r denc.Release, want string) {
			_, err := policy.Parse(doc, parseOpts(r))
			Expect(annotationOf(err)).To(Equal(want))
		},
		Entry("a version other than the two", `{"Version": "2020-01-01", "Statement": {"Effect": "Allow"}}`, denc.Squid,
			"`2020-01-01` is not a valid version. Valid versions are `2008-10-17` and `2012-10-17`."),
		Entry("an effect other than Allow and Deny", stmt(`"Effect": "Maybe"`), denc.Squid,
			"`Maybe` is not a valid effect."),
		Entry("an action no action matches", stmt(`"Action": "s3:Nope"`), denc.Squid,
			"`s3:Nope` is not a valid action."),
		Entry("a Tentacle action on Squid", stmt(`"Action": "s3:GetObjectAttributes"`), denc.Squid,
			"`s3:GetObjectAttributes` is not a valid action."),
		Entry("a resource of another tenant", stmt(`"Resource": "arn:aws:s3::othertenant:b"`), denc.Squid,
			"Policy owned by tenant `arbitrary_tenant` cannot grant access to resource owned by tenant `othertenant`."),
		Entry("a resource that is no ARN", stmt(`"Resource": "bucket"`), denc.Squid,
			"`bucket` is not a valid ARN. Resource ARNs should have a format like `arn:aws:s3::tenant:resource' or `arn:aws:s3:::resource`."),
		Entry("a Principal array", stmt(`"Principal": ["*"]`), denc.Squid,
			"`Principal` does not take array."),
		Entry("a canonical user", stmt(`"Principal": {"CanonicalUser": "abc"}`), denc.Squid,
			"RGW does not support canonical users."),
		Entry("an AWS ARN naming a group", stmt(`"Principal": {"AWS": "arn:aws:iam::t:group/g"}`), denc.Squid,
			"`arn:aws:iam::t:group/g` is not a supported AWS or Federated ARN. Supported ARNs are forms like: "+
				"`arn:aws:iam::tenant:root` or a bare tenant name for a tenant, "+
				"`arn:aws:iam::tenant:role/role-name` for a role, "+
				"`arn:aws:sts::tenant:assumed-role/role-name/role-session-name` for an assumed role, "+
				"`arn:aws:iam::tenant:user/user-name` for a user, "+
				"`arn:aws:iam::tenant:oidc-provider/idp-url` for OIDC."),
		Entry("a Federated string with a slash that is no ARN", stmt(`"Principal": {"Federated": "a/b"}`), denc.Squid,
			"`a/b` is not a supported AWS or Federated ARN. Supported ARNs are forms like: "+
				"`arn:aws:iam::tenant:root` or a bare tenant name for a tenant, "+
				"`arn:aws:iam::tenant:role/role-name` for a role, "+
				"`arn:aws:sts::tenant:assumed-role/role-name/role-session-name` for an assumed role, "+
				"`arn:aws:iam::tenant:user/user-name` for a user, "+
				"`arn:aws:iam::tenant:oidc-provider/idp-url` for OIDC."),
		Entry("a Service principal on Squid", stmt(`"Principal": {"Service": "s3.amazonaws.com"}`), denc.Squid,
			"RGW does not support principals of type `Service`."),
		Entry("a statement key given twice", stmt(`"Effect": "Allow", "Effect": "Deny"`), denc.Squid,
			"Token `Effect` is not allowed in the context of `Statement`."),
		Entry("a principal type in both Principal and NotPrincipal",
			stmt(`"Effect": "Deny", "Principal": {"AWS": "a"}, "NotPrincipal": {"AWS": "b"}`), denc.Squid,
			"Token `AWS` is not allowed in the context of `NotPrincipal`."),
		Entry("a top-level key given twice", `{"Version": "2012-10-17", "Version": "2012-10-17"}`, denc.Squid,
			"Token `Version` is not allowed in the context of `<Top>`."),
		Entry("a statement key at the top level", `{"Effect": "Allow"}`, denc.Squid,
			"Token `Effect` is not allowed in the context of `<Top>`."),
		Entry("a key no keyword has", `{"Version": "2012-10-17", "Statement": {}, "Extra": 1}`, denc.Squid,
			"Unknown key `Extra`."),
		Entry("an unknown condition operator", stmt(`"Condition": {"Nope": {"k": "v"}}`), denc.Squid,
			"Unknown key `Nope`."),
		Entry("an unknown condition operator with IfExists, named without it",
			stmt(`"Condition": {"NopeIfExists": {"k": "v"}}`), denc.Squid,
			"Unknown key `Nope`."),
		Entry("a keyword that is no operator under Condition", stmt(`"Condition": {"VersionIfExists": {"k": "v"}}`), denc.Squid,
			"Token `Version` is not allowed in the context of `Condition`."),
		Entry("a keyword as a condition key", stmt(`"Condition": {"StringEquals": {"Effect": "x"}}`), denc.Squid,
			"Token `Effect` is not allowed in the context of `StringEquals`."),
		Entry("a value starting ${ that does not close", stmt(`"Condition": {"StringEquals": {"k": "${broken"}}`), denc.Squid,
			"Invalid interpolation `${broken`."),
		Entry("a value starting $ but not ${", stmt(`"Condition": {"StringEquals": {"k": "$x}"}}`), denc.Squid,
			"Invalid interpolation `$x}`."),
		Entry("a true literal", stmt(`"Condition": {"Bool": {"k": true}}`), denc.Squid,
			"No error?"),
		Entry("a null literal", `{"Id": null}`, denc.Squid,
			"No error?"),
		Entry("Allow with NotPrincipal on Tentacle",
			stmt(`"Effect": "Allow", "NotPrincipal": {"AWS": "*"}, "Action": "s3:*", "Resource": "*"`), denc.Tentacle,
			"Allow with NotPrincipal is not allowed."),
		Entry("NotPrincipal, then Allow, on Tentacle", stmt(`"NotPrincipal": "*", "Effect": "Allow"`), denc.Tentacle,
			"Allow with NotPrincipal is not allowed."),
		Entry("a Principal string other than *", stmt(`"Principal": "arn:aws:iam::t:root"`), denc.Squid,
			"`arn:aws:iam::t:root` is not valid in the context of `Principal`."),
		Entry("an empty Principal string", stmt(`"Principal": ""`), denc.Squid,
			"`` is not valid in the context of `Principal`."),
		Entry("a NotPrincipal string other than *", stmt(`"NotPrincipal": "x"`), denc.Squid,
			"`x` is not valid in the context of `NotPrincipal`."),
		Entry("a statement given as a string", `{"Statement": "x"}`, denc.Squid,
			"`x` is not valid in the context of `Statement`."),
		// v20.2.4's do_string asserts that a statement exists first and
		// aborts (docs/exclusions.md); rgw-go refuses it as v19.2.6 does.
		Entry("a statement given as a string on Tentacle", `{"Statement": ["x"]}`, denc.Tentacle,
			"`x` is not valid in the context of `Statement`."),
		Entry("a string under a condition operator", stmt(`"Condition": {"StringEquals": "x"}`), denc.Squid,
			"`x` is not valid in the context of `StringEquals`."),
		Entry("a number outside a condition", `{"Version": 2012}`, denc.Squid,
			"Numbers are not allowed outside condition arguments."),
		Entry("an object under a keyword that takes a string", `{"Version": {}}`, denc.Squid,
			"The Version keyword cannot introduce an object."),
		Entry("an object as an Action", stmt(`"Action": {}`), denc.Squid,
			"The Action keyword cannot introduce an object."),
		Entry("an object as a condition value", stmt(`"Condition": {"StringEquals": {"k": {}}}`), denc.Squid,
			"The <Condition Key> keyword cannot introduce an object."),
		Entry("an array in an array", stmt(`"Action": [["s3:GetObject"]]`), denc.Squid,
			"`Action` does not take array."),
		Entry("an array at the top level", `[]`, denc.Squid, "Array not allowed at top level."),
		Entry("a string at the top level", `"x"`, denc.Squid, "String not allowed at top level."),
		Entry("a number at the top level", `1`, denc.Squid, "Number not allowed at top level."),
	)

	DescribeTable("reports radosgw's message, its offset counting the raw text",
		func(doc, needle string, after bool, want string) {
			off := strings.Index(doc, needle)
			Expect(off).To(BeNumerically(">=", 0), "needle %q", needle)
			if after {
				off += len(needle)
			}
			_, err := policy.Parse(doc, squid)
			Expect(err).To(MatchError(&policy.ParseError{Offset: int64(off), Annotation: want}))
			Expect(err).To(MatchError(fmt.Sprintf("At character offset %d, %s", off, want)))
		},
		Entry("a string the handler refuses ends past its closing quote", `{"Version": "2020"}`, `"2020"`, true,
			"`2020` is not a valid version. Valid versions are `2008-10-17` and `2012-10-17`."),
		Entry("a key the handler refuses ends past its closing quote", `{"Extra": 1}`, `"Extra"`, true,
			"Unknown key `Extra`."),
		Entry("a number the handler refuses starts at its first character", `{"Version": -2012}`, `-2012`, false,
			"Numbers are not allowed outside condition arguments."),
		Entry("a literal ends past its last letter", `{"Id": false}`, `false`, true, "No error?"),
		Entry("an object the handler refuses ends past its {", `{"Version": {}}`, `: {`, true,
			"The Version keyword cannot introduce an object."),
		Entry("an array the handler refuses ends past its [", `[]`, `[`, true, "Array not allowed at top level."),
		Entry("a block comment counts", `/* a comment */ {"Version": "2020"}`, `"2020"`, true,
			"`2020` is not a valid version. Valid versions are `2008-10-17` and `2012-10-17`."),
		Entry("a line comment counts", "// a comment\n{\"Version\": \"2020\"}", `"2020"`, true,
			"`2020` is not a valid version. Valid versions are `2008-10-17` and `2012-10-17`."),
		Entry("an empty document", ``, ``, false, "The document is empty."),
		Entry("a document of whitespace and comments", " /* c */\n", "\n", true, "The document is empty."),
		Entry("text after the root object", `{} x`, `x`, false, "The document root must not be followed by other values."),
		Entry("a member without a name", `{"Version": "2012-10-17",}`, `}`, false, "Missing a name for object member."),
		Entry("a name without a colon", `{"Version" "2012-10-17"}`, `"2012`, false,
			"Missing a colon after a name of object member."),
		Entry("members without a comma", `{"Version": "2012-10-17" "Id": "x"}`, `"Id"`, false,
			"Missing a comma or '}' after an object member."),
		Entry("elements without a comma", stmt(`"Action": ["s3:GetObject" "s3:PutObject"]`), `"s3:PutObject"`, false,
			"Missing a comma or ']' after an array element."),
		Entry("a bare word", `{"Version": x}`, `x`, false, "Invalid value."),
		Entry("a misspelled literal", `{"Id": tru}`, `}`, false, "Invalid value."),
		Entry("a minus sign alone", `{"Id": -}`, `-`, true, "Invalid value."),
		Entry("a string that never closes", `{"Id": "abc`, `"abc`, true, "Missing a closing quotation mark in string."),
		Entry("an unknown escape, at its backslash", `{"Id": "a\qb"}`, `\q`, false, "Invalid escape character in string."),
		Entry("a bad hex digit, at the escape's backslash", `{"Id": "\u12G4"}`, `\u`, false,
			"Incorrect hex digit after \\u escape in string."),
		Entry("a low surrogate alone", `{"Id": "\uDC00"}`, `\u`, false, "The surrogate pair in string is invalid."),
		Entry("a high surrogate without its low one, at the first escape", `{"Id": "x\uD800A"}`, `\uD800`, false,
			"The surrogate pair in string is invalid."),
		Entry("a control character in a string", "{\"Id\": \"a\tb\"}", "\t", false, "Invalid encoding in string."),
		Entry("a fraction without digits", stmt(`"Condition": {"NumericEquals": {"k": 1.}}`), `1.`, true,
			"Miss fraction part in number."),
		Entry("an exponent without digits", stmt(`"Condition": {"NumericEquals": {"k": 1e+}}`), `1e+`, true,
			"Miss exponent in number."),
		Entry("an exponent past 308, at the number's start", stmt(`"Condition": {"NumericEquals": {"k": 1e309}}`), `1e309`, false,
			"Number too big to be stored in double."),
		Entry("a leading zero, at the digit after it", stmt(`"Condition": {"NumericEquals": {"k": 01}}`), `1}`, false,
			"Missing a comma or '}' after an object member."),
		Entry("a block comment that never closes", `{} /* x`, `/* x`, true, "Unspecific syntax error."),
		Entry("a slash that starts no comment", `{} / x`, `/`, true, "Unspecific syntax error."),
	)

	Describe("strings and numbers", func() {
		It("decodes every escape rapidjson knows, a surrogate pair included", func() {
			p := mustParse(`{"Statement": {"Sid": "\"\\\/\b\f\n\r\té😀\u0000"}}`, squid)
			Expect(*p.Statements[0].Sid).To(Equal("\"\\/\b\f\n\r\té\U0001F600\x00"))
		})

		It("keeps bytes that are not UTF-8 as they are", func() {
			p := mustParse("{\"Statement\": {\"Sid\": \"\xff\xfe\"}}", squid)
			Expect(*p.Statements[0].Sid).To(Equal("\xff\xfe"))
		})

		It("ends the document at its first NUL byte, keeping the text whole", func() {
			text := "{\"Version\": \"2012-10-17\"}\x00 not JSON"
			p := mustParse(text, squid)
			Expect(p.Text).To(Equal(text))
			Expect(p.Version).To(Equal(policy.V2012_10_17))
		})

		It("keeps a number's text as the condition value", func() {
			p := mustParse(stmt(`"Condition": {"NumericEquals": {"k": [-1.50E+3, 0, 1e308, 1.5e309, 0.00001e313, 1e-99999]}}`), squid)
			Expect(p.Statements[0].Conditions).To(Equal([]policy.Condition{
				cond(policy.OpNumericEquals, "k", "-1.50E+3", "0", "1e308", "1.5e309", "0.00001e313", "1e-99999"),
			}))
		})

		It("refuses an exponent past 308 less the fraction's digits", func() {
			for _, n := range []string{"1.5e310", "0.00001e314"} {
				_, err := policy.Parse(stmt(`"Condition": {"NumericEquals": {"k": `+n+`}}`), squid)
				Expect(annotationOf(err)).To(Equal("Number too big to be stored in double."), n)
			}
		})

		It("parses a document with line and block comments", func() {
			doc := `// leading comment
{
  /* the version */ "Version": "2012-10-17", // trailing comment
  "Statement": {"Effect": "Allow", /* inline */ "Action": "s3:ListBucket", "Resource": "*"}
} /* after the root */`
			p := mustParse(doc, squid)
			Expect(p.Statements).To(HaveLen(1))
			Expect(namesIn(p.Statements[0].Actions)).To(ConsistOf("s3:ListBucket"))
		})
	})

	Describe("principals", func() {
		principals := func(doc string, opts policy.ParseOptions) []policy.Principal {
			p, err := policy.Parse(doc, opts)
			ExpectWithOffset(1, err).NotTo(HaveOccurred())
			return p.Statements[0].Principals
		}

		DescribeTable("parses an AWS or Federated principal as radosgw does",
			func(typ, s string, want policy.Principal) {
				Expect(principals(stmt(`"Principal": {"`+typ+`": "`+s+`"}`), squid)).To(Equal([]policy.Principal{want}))
			},
			Entry("an account's root", "AWS", "arn:aws:iam::acct:root", policy.AccountPrincipal("acct")),
			Entry("a bare tenant", "AWS", "tenantA", policy.AccountPrincipal("tenantA")),
			Entry("an empty string, as a bare tenant", "AWS", "", policy.AccountPrincipal("")),
			Entry("a string starting * under AWS, as a bare tenant", "AWS", "*x", policy.AccountPrincipal("*x")),
			Entry("* under AWS", "AWS", "*", policy.WildcardPrincipal()),
			Entry("* under Federated, as a bare tenant", "Federated", "*", policy.AccountPrincipal("*")),
			Entry("a user", "AWS", "arn:aws:iam::t:user/u", policy.UserPrincipal("t", "u")),
			Entry("a role", "AWS", "arn:aws:iam::t:role/r", policy.RolePrincipal("t", "r")),
			Entry("an assumed role", "AWS", "arn:aws:sts::t:assumed-role/r/s", policy.AssumedRolePrincipal("t", "r/s")),
			Entry("an OIDC provider", "Federated", "arn:aws:iam::t:oidc-provider/idp.example.com",
				policy.OIDCProviderPrincipal("idp.example.com")),
		)

		It("takes any Principal or NotPrincipal string starting * for every principal", func() {
			p := mustParse(stmt(`"Effect": "Deny", "Principal": "*anyone", "NotPrincipal": "*x"`), squid)
			Expect(p.Statements[0].Principals).To(Equal([]policy.Principal{policy.WildcardPrincipal()}))
			Expect(p.Statements[0].NotPrincipals).To(Equal([]policy.Principal{policy.WildcardPrincipal()}))
		})

		It("parses a Service principal on Tentacle", func() {
			Expect(principals(stmt(`"Principal": {"Service": "s3.amazonaws.com"}`), tentacle)).
				To(Equal([]policy.Principal{policy.ServicePrincipal("s3.amazonaws.com")}))
		})

		It("drops an unsupported principal when invalid principals are not rejected, and logs it", func() {
			logs := captureLogs()
			opts := squid
			opts.RejectInvalidPrincipals = false
			Expect(principals(stmt(`"Principal": {"CanonicalUser": "abc"}`), opts)).To(BeEmpty(), "a canonical user")
			Expect(logs.String()).To(And(
				ContainSubstring(`"level":"WARN"`), ContainSubstring(`"principal":"abc"`),
				ContainSubstring(`"reason":"RGW does not support canonical users."`)))
			Expect(principals(stmt(`"Principal": {"Service": "s3.amazonaws.com"}`), opts)).To(BeEmpty(), "a service on squid")
			Expect(principals(stmt(`"Principal": {"AWS": ["arn:aws:iam::t:group/g", "arn:aws:iam::t:user/u"]}`), opts)).
				To(Equal([]policy.Principal{policy.UserPrincipal("t", "u")}), "a group beside a user")
		})
		It("logs a dropped principal under the parse's context, so a request's line carries its id", func(ctx SpecContext) {
			var buf bytes.Buffer
			oldLogger, oldWriter, oldFlags := slog.Default(), log.Writer(), log.Flags()
			slog.SetDefault(slog.New(op.NewLogHandler(slog.NewJSONHandler(&buf, nil))))
			DeferCleanup(func() {
				slog.SetDefault(oldLogger)
				log.SetOutput(oldWriter)
				log.SetFlags(oldFlags)
			})
			opts := squid
			opts.RejectInvalidPrincipals = false
			opts.Context = op.WithRequestID(ctx, "tx1")
			Expect(principals(stmt(`"Principal": {"CanonicalUser": "abc"}`), opts)).To(BeEmpty())
			Expect(buf.String()).To(ContainSubstring(`"msg":"ignored policy principal"`))
			Expect(buf.String()).To(ContainSubstring(`"request_id":"tx1"`))
		})

		// radosgw's flat_set keeps the first of two principals equal in kind,
		// account and id, so any two OIDC providers, or two services, are one
		// (docs/ceph-upstream-bugs.md, "radosgw drops all but the first OIDC
		// provider or Service principal in a statement").
		It("collapses principals as radosgw's principal set does", func() {
			Expect(principals(stmt(`"Principal": {"Federated": [
				"arn:aws:iam::t:oidc-provider/a.example.com", "arn:aws:iam::t:oidc-provider/b.example.com"]}`), squid)).
				To(Equal([]policy.Principal{policy.OIDCProviderPrincipal("a.example.com")}), "two OIDC providers")
			Expect(principals(stmt(`"Principal": {"Service": ["a.example.com", "b.example.com"]}`), tentacle)).
				To(Equal([]policy.Principal{policy.ServicePrincipal("a.example.com")}), "two services")
			Expect(principals(stmt(`"Principal": {"AWS": [
				"arn:aws:iam::t:user/u", "arn:aws:iam::t:user/v", "arn:aws:iam::t:user/u", "t", "arn:aws:iam::t:root"]}`), squid)).
				To(Equal([]policy.Principal{
					policy.UserPrincipal("t", "u"), policy.UserPrincipal("t", "v"), policy.AccountPrincipal("t"),
				}), "users and an account named twice")
		})
	})

	Describe("statements", func() {
		It("takes any Action or NotAction string starting * for every action", func() {
			p := mustParse(stmt(`"Action": "*Object", "NotAction": ["*"]`), squid)
			Expect(p.Statements[0].Actions).To(Equal(policy.AllValue()), "Action")
			Expect(p.Statements[0].NotActions).To(Equal(policy.AllValue()), "NotAction")
		})

		It("parses a Tentacle action on Tentacle", func() {
			p := mustParse(stmt(`"Action": "s3:GetObjectAttributes"`), tentacle)
			Expect(namesIn(p.Statements[0].Actions)).To(ConsistOf("s3:GetObjectAttributes"))
		})

		It("leaves a statement without an Effect at Deny", func() {
			p := mustParse(stmt(`"Action": "s3:GetObject", "Resource": "*"`), squid)
			Expect(p.Statements[0].Effect).To(Equal(policy.Deny))
		})

		It("keeps a statement naming both Resource and NotResource", func() {
			p := mustParse(stmt(`"Resource": "arn:aws:s3:::a", "NotResource": "arn:aws:s3:::b"`), squid)
			Expect(p.Statements[0].Resources).To(Equal([]policy.ARN{s3ARN("a")}), "Resource")
			Expect(p.Statements[0].NotResources).To(Equal([]policy.ARN{s3ARN("b")}), "NotResource")
		})

		It("keeps resources in document order, once each", func() {
			p := mustParse(stmt(`"Resource": ["arn:aws:s3:::b", "arn:aws:s3:::a", "arn:aws:s3:::b"]`), squid)
			Expect(p.Statements[0].Resources).To(Equal([]policy.ARN{s3ARN("b"), s3ARN("a")}))
		})

		It("accepts Allow with NotPrincipal on Squid", func() {
			p := mustParse(stmt(`"Effect": "Allow", "NotPrincipal": {"AWS": "*"}, "Action": "s3:*", "Resource": "*"`), squid)
			Expect(p.Statements[0].NotPrincipals).To(Equal([]policy.Principal{policy.WildcardPrincipal()}))
		})

		// ParseState::obj_end resets the statement's seen keys whenever an
		// object closes inside an array, not only a statement's.
		It("lets a statement repeat its keys after an object closes inside a Condition array", func() {
			p := mustParse(stmt(`"Effect": "Deny", "Condition": [{"Bool": {"k": "true"}}], "Effect": "Allow"`), squid)
			Expect(p.Statements[0].Effect).To(Equal(policy.Allow))
		})

		It("checks each statement's keys afresh", func() {
			p := mustParse(`{"Statement": [{"Effect": "Allow"}, {"Effect": "Deny"}]}`, squid)
			Expect(p.Statements).To(HaveLen(2))
			Expect(p.Statements[1].Effect).To(Equal(policy.Deny))
		})
	})

	Describe("conditions", func() {
		conditions := func(members string) []policy.Condition {
			p, err := policy.Parse(stmt(`"Condition": `+members), squid)
			ExpectWithOffset(1, err).NotTo(HaveOccurred())
			return p.Statements[0].Conditions
		}

		It("parses an operator's IfExists suffix", func() {
			Expect(conditions(`{"StringEqualsIfExists": {"k": "v"}}`)).
				To(Equal([]policy.Condition{ifExists(cond(policy.OpStringEquals, "k", "v"))}))
		})

		It("marks a condition whose value is an interpolation as runtime", func() {
			Expect(conditions(`{"StringEquals": {"k": ["v", "${aws:username}"]}}`)).
				To(Equal([]policy.Condition{runtime(cond(policy.OpStringEquals, "k", "v", "${aws:username}"))}))
		})

		It("keeps one condition per key, in document order, and an empty value list", func() {
			Expect(conditions(`{"StringLike": {"b": "x*", "a": []}, "Null": {"c": "true"}}`)).To(Equal([]policy.Condition{
				cond(policy.OpStringLike, "b", "x*"),
				{Op: policy.OpStringLike, Key: "a"},
				cond(policy.OpNull, "c", "true"),
			}))
		})
	})

	Describe("the tenant", func() {
		resources := func(resource string, tenant *string) []policy.ARN {
			p, err := policy.Parse(stmt(`"Resource": "`+resource+`"`), policy.ParseOptions{Tenant: tenant, Release: denc.Squid})
			ExpectWithOffset(1, err).NotTo(HaveOccurred())
			return p.Statements[0].Resources
		}

		It("gives the tenant to a resource whose account is empty or *, and keeps its own", func() {
			Expect(resources("arn:aws:s3::*:b", new(arbitraryTenant))).To(Equal([]policy.ARN{s3ARN("b")}), "*")
			Expect(resources("arn:aws:s3::arbitrary_tenant:b", new(arbitraryTenant))).To(Equal([]policy.ARN{s3ARN("b")}), "the tenant")
		})

		It("accepts every account unchanged without a tenant", func() {
			for _, account := range []string{"othertenant", "", "*"} {
				Expect(resources("arn:aws:s3::"+account+":b", nil)).To(Equal([]policy.ARN{
					{Partition: policy.PartitionAWS, Service: policy.ServiceS3, Account: account, Resource: "b"},
				}), "account %q", account)
			}
		})
	})
})
