package citecheck_test

import (
	"os"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/hack/citecheck/internal/citecheck"
)

var _ = Describe("Checker", func() {
	var (
		ceph    *cephFixture
		checker *citecheck.Checker
	)

	BeforeEach(func(ctx SpecContext) {
		ceph = newCephFixture()
		var err error
		checker, err = citecheck.NewChecker(ctx, ceph.dir)
		Expect(err).NotTo(HaveOccurred())
	})

	// reason checks the one citation md holds and returns why it fails.
	reason := func(ctx SpecContext, md string) string {
		cites := citecheck.Scan([]byte(md))
		Expect(cites).To(HaveLen(1), "citations in %q", md)
		r, err := checker.Check(ctx, cites[0])
		Expect(err).NotTo(HaveOccurred(), "checking %q", md)
		return r
	}

	DescribeTable("a citation that holds",
		func(ctx SpecContext, md string) {
			Expect(reason(ctx, strings.ReplaceAll(md, "ABBREV", ceph.v1[:10]))).To(BeEmpty())
		},
		Entry("a code span citing a range", "`src/rgw/rgw_fixture.cc:6-13@v1.0.0`"),
		Entry("a code span citing the last line, which ends in a newline", "`src/rgw/rgw_fixture.cc:43@v1.0.0`"),
		Entry("a link to a range", "https://github.com/ceph/ceph/blob/v1.0.0/src/rgw/rgw_fixture.cc#L15-L19"),
		Entry("a link to one line at an annotated tag", "https://github.com/ceph/ceph/blob/v2.0.0/src/rgw/rgw_fixture.cc#L48"),
		Entry("a symbol on the cited line", "`src/rgw/rgw_fixture.cc:6@v1.0.0#Store::Object::Read::prepare`"),
		Entry("a symbol a declaration in a class names", "`src/rgw/rgw_fixture.h:10@v1.0.0#Store::Object::Read::prepare`"),
		Entry("a symbol whose qualified definition encloses the range, past a brace in a comment",
			"`src/rgw/rgw_fixture.cc:9-11@v1.0.0#Store::Object::Read::prepare`"),
		Entry("a symbol qualified by a namespace its definition is written inside",
			"`src/rgw/rgw_fixture.cc:8@v1.0.0#rgw::fixture::Store::Object::Read::prepare`"),
		Entry("a symbol qualified less than its definition", "`src/rgw/rgw_fixture.cc:8@v1.0.0#Read::prepare`"),
		Entry("a symbol whose unqualified definition in its class encloses the range, past a conditional's braces",
			"`src/rgw/rgw_fixture.h:17@v1.0.0#Store::Object::Read::iterate`"),
		Entry("a symbol whose definition's header, on lines of its own, holds the range",
			"`src/rgw/rgw_fixture.h:23@v1.0.0#Store::Object::Read::prepare`"),
		Entry("a symbol whose definition encloses an if whose header calls another class's",
			"`src/rgw/rgw_fixture.h:25@v1.0.0#Store::Object::Read::prepare`"),
		Entry("a constructor whose header holds braces in its member initializers",
			"`src/rgw/rgw_fixture.cc:24@v1.0.0#rgw::fixture::Store::Store`"),
		Entry("a symbol whose in-class definition, in its own class, encloses the range",
			"`src/rgw/rgw_fixture.h:33@v1.0.0#Store::Object::Write::prepare`"),
		Entry("a symbol another class's member initializer calls inside parentheses, which its class does not qualify",
			"`src/rgw/rgw_fixture.h:35@v1.0.0#Store::Object::Read::prepare`"),
		Entry("a base class's constructor a namespaced constructor's member-initializer list calls",
			"`src/rgw/rgw_fixture.h:44-45@v1.0.0#rgw::fixture::Store::Store`"),
		Entry("a base class a nested class's head names", "`src/rgw/rgw_fixture.h:31@v1.0.0#rgw::fixture::Base`"),
		Entry("a type a trailing return type names", "`src/rgw/rgw_fixture.h:48@v1.0.0#rgw::fixture::Base`"),
		Entry("a type a parameter of a declaration names", "`src/rgw/rgw_fixture.h:50@v1.0.0#rgw::fixture::Base`"),
		Entry("a function an initializer calls after a template argument list closes",
			"`src/rgw/rgw_fixture.h:55@v1.0.0#Zz::make`"),
		Entry("a member template with a default template argument, in its own class",
			"`src/rgw/rgw_fixture.h:53@v1.0.0#rgw::fixture::Processor::wait`"),
		Entry("a function a default member initializer calls", "`src/rgw/rgw_fixture.h:49@v1.0.0#Zz::compute`"),
		Entry("a name a comment at class scope mentions, which declares nothing",
			"`src/rgw/rgw_fixture.h:36@v1.0.0#Store::Object::Read::retry`"),
		Entry("a name a directive at class scope mentions, which declares nothing",
			"`src/rgw/rgw_fixture.h:37@v1.0.0#Store::Object::Read::retry`"),
		Entry("a symbol past a brace in a character literal, a line comment, two raw strings, a digit separator's "+
			"neighbor and a continued directive", "`src/rgw/rgw_fixture.cc:37@v1.0.0#Store::lex`"),
		Entry("an abbreviated commit id", "`src/rgw/rgw_fixture.cc:6@ABBREV#Store::Object::Read::prepare`"),
	)

	DescribeTable("a citation that fails",
		func(ctx SpecContext, md, want string) {
			Expect(reason(ctx, md)).To(Equal(strings.ReplaceAll(want, "CEPH", ceph.dir)))
		},
		Entry("a ref that does not exist", "`src/rgw/rgw_fixture.cc:1@v9.9.9`", "ref v9.9.9 names no tag or commit in CEPH"),
		Entry("a branch", "`src/rgw/rgw_fixture.cc:1@main`",
			"ref main is refs/heads/main, a branch or symbolic ref, which moves: cite a tag or a commit id"),
		Entry("a remote-tracking branch", "`src/rgw/rgw_fixture.cc:1@origin/main`",
			"ref origin/main is refs/remotes/origin/main, a branch or symbolic ref, which moves: cite a tag or a commit id"),
		Entry("HEAD", "`src/rgw/rgw_fixture.cc:1@HEAD`",
			"ref HEAD is refs/heads/main, a branch or symbolic ref, which moves: cite a tag or a commit id"),
		Entry("a revision expression in a link", "https://github.com/ceph/ceph/blob/v2.0.0~1/src/rgw/rgw_fixture.cc#L1",
			"ref v2.0.0~1 is neither a tag nor a commit id"),
		Entry("a full commit id the clone lacks", "`src/rgw/rgw_fixture.cc:1@0123456789abcdef0123456789abcdef01234567`",
			"ref 0123456789abcdef0123456789abcdef01234567 names no commit in CEPH"),
		Entry("a commit id the clone lacks", "`src/rgw/rgw_fixture.cc:1@deadbeef`",
			"commit id deadbeef names no commit in CEPH, or abbreviates more than one"),
		Entry("a missing path", "`src/rgw/rgw_nope.cc:1@v1.0.0`", "path src/rgw/rgw_nope.cc does not exist at v1.0.0"),
		Entry("a directory", "`src/rgw:1@v1.0.0`", "path src/rgw is a tree, not a file, at v1.0.0"),
		Entry("a path that is not clean", "`./src/rgw/rgw_fixture.cc:1@v1.0.0`",
			"path ./src/rgw/rgw_fixture.cc is not a clean path from the repository's root"),
		Entry("a range past the end of the file", "`src/rgw/rgw_fixture.cc:42-44@v1.0.0`",
			"line 44 is past the end of src/rgw/rgw_fixture.cc, which has 43 lines at v1.0.0"),
		Entry("a link past the end of the file", "https://github.com/ceph/ceph/blob/v1.0.0/src/rgw/rgw_fixture.cc#L48",
			"line 48 is past the end of src/rgw/rgw_fixture.cc, which has 43 lines at v1.0.0"),
		Entry("a range that runs backwards", "`src/rgw/rgw_fixture.cc:12-10@v1.0.0`", "lines 12-10 run backwards"),
		Entry("line 0", "`src/rgw/rgw_fixture.cc:0@v1.0.0`", "line 0 does not exist: lines count from 1"),
		Entry("a malformed link anchor", "https://github.com/ceph/ceph/blob/v1.0.0/src/rgw/rgw_fixture.cc#L6-13",
			"anchor #L6-13 is not #L<line> or #L<from>-L<to>"),
		Entry("a symbol neither the lines nor a block around them names",
			"`src/rgw/rgw_fixture.cc:8@v1.0.0#Store::Object::Read::iterate`",
			"the cited lines do not hold iterate, and no block enclosing them is headed by it"),
		Entry("a symbol whose enclosing definition another class qualifies, past a brace in a string",
			"`src/rgw/rgw_fixture.cc:17-18@v1.0.0#Store::Object::Read::prepare`",
			"the nearest block enclosing the cited lines that names prepare is "+
				"rgw::fixture::Store::Object::Write::prepare at line 15, not Store::Object::Read::prepare"),
		Entry("a symbol whose definition another class qualifies, cited at its header",
			"`src/rgw/rgw_fixture.cc:15@v1.0.0#Store::Object::Read::prepare`",
			"the nearest block enclosing the cited lines that names prepare is "+
				"rgw::fixture::Store::Object::Write::prepare at line 15, not Store::Object::Read::prepare"),
		Entry("a symbol whose same-named method another class defines in its body around the range",
			"`src/rgw/rgw_fixture.h:33@v1.0.0#Store::Object::Read::prepare`",
			"the nearest block enclosing the cited lines that names prepare is "+
				"rgw::fixture::Store::Object::Write::prepare at line 32, not Store::Object::Read::prepare"),
		Entry("a symbol whose same-named method another class defines in its body, cited at its header",
			"`src/rgw/rgw_fixture.h:32@v1.0.0#Store::Object::Read::prepare`",
			"the nearest block enclosing the cited lines that names prepare is "+
				"rgw::fixture::Store::Object::Write::prepare at line 32, not Store::Object::Read::prepare"),
		Entry("a symbol whose same-named method another class declares on the cited line",
			"`src/rgw/rgw_fixture.h:10@v1.0.0#Store::Object::Write::prepare`",
			"the cited lines name only rgw::fixture::Store::Object::Read::prepare, and no block enclosing them is "+
				"headed by prepare"),
		Entry("a symbol past the end of its definition, which a brace in a construct the lexer leaves out would extend",
			"`src/rgw/rgw_fixture.cc:42@v1.0.0#Store::lex`",
			"the cited lines do not hold lex, and no block enclosing them is headed by it"),
		Entry("a symbol the cited line calls with another qualifier, in a block not headed by it",
			"`src/rgw/rgw_fixture.h:29@v1.0.0#Store::Object::Read::prepare`",
			"the cited lines name only Write::prepare, and no block enclosing them is headed by prepare"),
		Entry("a name that only qualifies the enclosing definition",
			"`src/rgw/rgw_fixture.cc:8@v1.0.0#Object::Object`",
			"the cited lines do not hold Object, and no block enclosing them is headed by it"),
		Entry("a symbol whose block ends before the cited line, though a later conditional branch adds a brace",
			"`src/rgw/rgw_fixture.h:21@v1.0.0#Store::Object::Read::iterate`",
			"the cited lines do not hold iterate, and no block enclosing them is headed by it"),
		Entry("a method whose return type is qualified, cited with another class",
			"`src/rgw/rgw_fixture.h:47@v1.0.0#Store::name`",
			"the nearest block enclosing the cited lines that names name is rgw::fixture::Processor::name at line 47, "+
				"not Store::name"),
		Entry("a member template with a default template argument, cited with another class",
			"`src/rgw/rgw_fixture.h:53@v1.0.0#Store::wait`",
			"the nearest block enclosing the cited lines that names wait is rgw::fixture::Processor::wait at line 52, "+
				"not Store::wait"),
		Entry("a constructor, after an access label, cited with another class",
			"`src/rgw/rgw_fixture.h:46@v1.0.0#Store::Processor`",
			"the nearest block enclosing the cited lines that names Processor is "+
				"rgw::fixture::Processor::Processor at line 44, not Store::Processor"),
	)

	It("holds a citation at one tag and fails it at the next, where its line moved", func(ctx SpecContext) {
		Expect(reason(ctx, "`src/rgw/rgw_fixture.cc:8@v1.0.0#Store::Object::Read::prepare`")).To(BeEmpty())
		Expect(reason(ctx, "`src/rgw/rgw_fixture.cc:8@v2.0.0#Store::Object::Read::prepare`")).
			To(Equal("the cited lines do not hold prepare, and no block enclosing them is headed by it"))
		Expect(reason(ctx, "`src/rgw/rgw_fixture.cc:13@v2.0.0#Store::Object::Read::prepare`")).To(BeEmpty())
	})

	It("resolves a commit id as a ref", func(ctx SpecContext) {
		Expect(reason(ctx, "`src/rgw/rgw_fixture.cc:6@"+ceph.v1+"#Store::Object::Read::prepare`")).To(BeEmpty())
	})

	It("resolves an abbreviated commit id that a blob's id also starts with", func(ctx SpecContext) {
		prefix := ceph.blobSharing(ceph.v1)
		Expect(strings.Fields(ceph.git("rev-parse", "--disambiguate="+prefix))).To(HaveLen(2))
		Expect(reason(ctx, "`src/rgw/rgw_fixture.cc:6@"+prefix+"#Store::Object::Read::prepare`")).To(BeEmpty())
	})

	It("asks git for each ref and file once", func(ctx SpecContext) {
		Expect(reason(ctx, "`src/rgw/rgw_fixture.cc:6@v1.0.0`")).To(BeEmpty())
		Expect(os.RemoveAll(ceph.dir)).To(Succeed())
		Expect(reason(ctx, "`src/rgw/rgw_fixture.cc:8-11@v1.0.0#Store::Object::Read::prepare`")).To(BeEmpty())
		_, err := checker.Check(ctx, citecheck.Scan([]byte("`src/rgw/rgw_fixture.cc:6@v2.0.0`"))[0])
		Expect(err).To(MatchError(ContainSubstring("git rev-parse")))
	})

	It("refuses a directory that is not a git repository", func(ctx SpecContext) {
		dir := GinkgoT().TempDir()
		_, err := citecheck.NewChecker(ctx, dir)
		Expect(err).To(MatchError(HavePrefix(dir + " is not a git repository")))
	})

	DescribeTable("refuses a partial clone, which git cat-file would fetch into",
		func(ctx SpecContext, clone func() string, why string) {
			dir := clone()
			_, err := citecheck.NewChecker(ctx, dir)
			Expect(err).To(MatchError(dir + " is a partial clone (" + why + "), which git would fetch the objects it " +
				"lacks into: check against a full clone"))
		},
		Entry("a clone made with --filter", func() string { return ceph.partialClone() }, "remote.origin.promisor is true"),
		Entry("extensions.partialClone alone", func() string {
			ceph.git("config", "extensions.partialClone", "origin")
			return ceph.dir
		}, "extensions.partialClone is origin"),
		Entry("a promisor remote alone", func() string {
			ceph.git("config", "remote.origin.promisor", "true")
			return ceph.dir
		}, "remote.origin.promisor is true"),
	)
})
