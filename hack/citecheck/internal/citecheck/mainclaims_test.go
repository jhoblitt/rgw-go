package citecheck_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/hack/citecheck/internal/citecheck"
)

// mainCommit is the main commit the specs' registries name, and mainBlob a
// link to a line of ceph main at it.
const (
	mainCommit = "8aa24c51928"
	mainBlob   = "[rgw_op.cc:10](https://github.com/ceph/ceph/blob/8aa24c51928768192775ab5220ce0621f5e60355/src/rgw/rgw_op.cc#L10)"
)

// unnamedReason starts why a claim about ceph main that names no main commit
// fails, and unnamedInItem and unnamedInPara are the whole reason in a list
// item and in a paragraph; unchecked is why one that says main was not
// checked fails, and shortMain why one that calls a742f50 main's does.
const (
	unnamedReason = `a claim about ceph main that names no main commit: write the main commit it was checked at, ` +
		`as "at main <commit>" with 9 hex digits or more or as a link to ceph/ceph/blob/<commit>/..., in `
	unnamedInItem = unnamedReason + `its own list item, not one nested in it or around it`
	unnamedInPara = unnamedReason + `its sentence`
	unchecked     = `says ceph main was not checked: check it, and write the main commit it was checked at, ` +
		`as "at main <commit>" with 9 hex digits or more`
	shortMain = `calls a742f50 main's, but a742f50 has fewer than 9 hex digits: write the main commit it was ` +
		`checked at with 9 or more`
)

// registry writes text as the registry docs/ceph-upstream-bugs.md under a
// fresh directory and returns its path; registryAt writes it as docs/name.
func registry(text string) string { return registryAt("ceph-upstream-bugs.md", text) }

func registryAt(name, text string) string {
	p := filepath.Join(GinkgoT().TempDir(), "docs", name)
	Expect(os.MkdirAll(filepath.Dir(p), 0o750)).To(Succeed())
	Expect(os.WriteFile(p, []byte(text), 0o600)).To(Succeed())
	return p
}

var _ = Describe("claims about ceph main", func() {
	var stdout, stderr *bytes.Buffer

	BeforeEach(func() {
		stdout, stderr = &bytes.Buffer{}, &bytes.Buffer{}
	})

	formOnly := func(ctx SpecContext, files ...string) error {
		return citecheck.Run(ctx, append([]string{"--form-only"}, files...), nil, stdout, stderr)
	}

	DescribeTable("pass when they name a main commit",
		func(ctx SpecContext, text string) {
			Expect(formOnly(ctx, registry(text))).To(Succeed(), "%s", stdout)
			Expect(stdout.String()).To(MatchRegexp(`main claims checked: [1-9]\d*, failed: 0\n$`))
		},
		Entry("main and the commit", "- **Evidence:** the code is the same on main "+mainCommit+".\n"),
		Entry("at main and the commit", "- **Impact:** it is unchanged at main "+mainCommit+" (2026-10-30).\n"),
		Entry("main at the commit", "- **Kind:** defect, unfixed through main at "+mainCommit+" (2026-10-30).\n"),
		Entry("ceph main and the commit", "- **Impact:** ceph main "+mainCommit+" makes the check.\n"),
		Entry("main's commit", "- **Impact:** main's c3e57712272 removed the startup-only read.\n"),
		Entry("the commit in parentheses", "- **Impact:** main's radosgw ("+mainCommit+") probes for it.\n"),
		Entry("a fix commit and the commit it was checked at",
			"- **Kind:** defect; fixed on main by 2a9a9b164cf (ceph/ceph#72097), checked at "+mainCommit+".\n"),
		Entry("a pull request on main and the commit it was checked at", "- **Upstream:** fixed by "+
			"[ceph/ceph#68364](https://github.com/ceph/ceph/pull/68364) on main (still fixed at "+mainCommit+").\n"),
		Entry("a full commit id", "- **Impact:** main still sends it at 8aa24c51928768192775ab5220ce0621f5e60355.\n"),
		Entry("a link to a line at the commit", "- **Impact:** main still sends it ("+mainBlob+").\n"),
		Entry("a link to the commit",
			"- **Impact:** main fixed it in [this commit](https://github.com/ceph/ceph/commit/"+mainCommit+").\n"),
		Entry("the commit in another sentence of its list item",
			"- **Impact:** main still sends it. Checked at "+mainCommit+".\n"),
		Entry("the commit on a line its list item's paragraph runs on to",
			"- **Impact:** main still sends it,\nas read at "+mainCommit+".\n"),
		Entry("the commit in a paragraph of its list item", "- **Impact:** main still sends it.\n\n  Read at "+
			mainCommit+".\n"),
		Entry("the commit in its sentence of a paragraph", "Main still sends it at "+mainCommit+". It is unchanged.\n"),
		Entry("the commit in its table row", "| main sends it | "+mainCommit+" |\n"),
	)

	DescribeTable("fail when they name no main commit, naming the file, the line, the claim and what to add",
		func(ctx SpecContext, text string, line int, claim, reason string, claims int) {
			md := registry(text)
			err := formOnly(ctx, md)
			Expect(citecheck.ExitStatus(err)).To(Equal(1))
			Expect(err).To(MatchError(fmt.Sprintf("1 of %d main claims name no main commit", claims)))
			Expect(stdout.String()).To(Equal(fmt.Sprintf("%s:%d: %s: %s\n", md, line, claim, reason) +
				fmt.Sprintf("citations checked: 0, failed: 0\nmain claims checked: %d, failed: 1\n", claims)))
		},
		Entry("on main", "- **Impact:** the same code is on main.\n", 1,
			"- **Impact:** the same code is on main.", unnamedInItem, 1),
		Entry("ceph main", "Its check is the one ceph main makes.\n", 1,
			"Its check is the one ceph main makes.", unnamedInPara, 1),
		Entry("main still", "The reply is the same.\nMain still sends it.\n", 2, "Main still sends it.", unnamedInPara, 1),
		Entry("fixed on main by a pull request", "- **Kind:** defect, fixed on main by ceph/ceph#72097.\n", 1,
			"- **Kind:** defect, fixed on main by ceph/ceph#72097.", unnamedInItem, 1),
		Entry("fixed by a pull request on main", "- **Upstream:** fixed by "+
			"[ceph/ceph#68364](https://github.com/ceph/ceph/pull/68364) on main.\n", 1,
			"- **Upstream:** fixed by [ceph/ceph#68364](https://github.com/ceph/ceph/pull/68364) on main.",
			unnamedInItem, 1),
		Entry("a pull request on main in parentheses", "- **Upstream:** [ceph/ceph#67336]"+
			"(https://github.com/ceph/ceph/pull/67336) (main), with ceph/ceph#67926 (squid).\n", 1,
			"- **Upstream:** [ceph/ceph#67336](https://github.com/ceph/ceph/pull/67336) (main), with "+
				"ceph/ceph#67926 (squid).", unnamedInItem, 1),
		Entry("prior art", "- **Kind:** defect. Prior art: upstream fixed it on main before we met it.\n", 1,
			"Prior art: upstream fixed it on main before we met it.", unnamedInItem, 1),
		Entry("a merge to main", "- **Upstream:** [ceph/ceph#72097](https://github.com/ceph/ceph/pull/72097), "+
			"merged on main on 2026-10-16.\n", 1, "- **Upstream:** [ceph/ceph#72097]"+
			"(https://github.com/ceph/ceph/pull/72097), merged on main on 2026-10-16.", unnamedInItem, 1),
		Entry("unfixed on main", "- **Kind:** defect, unfixed on main.\n", 1,
			"- **Kind:** defect, unfixed on main.", unnamedInItem, 1),
		Entry("main keeps", "- **Impact:** main keeps the cap.\n", 1, "- **Impact:** main keeps the cap.",
			unnamedInItem, 1),
		Entry("main's name for a function", "- **Impact:** main's `match_rule` takes it.\n", 1,
			"- **Impact:** main's `match_rule` takes it.", unnamedInItem, 1),
		Entry("main in a table row", "| main sends it | v20.2.4 |\n", 1, "main sends it",
			unnamedReason+"its table row", 1),
		Entry("main not checked", "- **Releases:** v19.2.6 and v20.2.4; main not checked.\n", 1,
			"- **Releases:** v19.2.6 and v20.2.4; main not checked.", unchecked, 1),
		Entry("main not checked, with a commit in its list item",
			"- **Releases:** v20.2.4; main was not checked, though "+mainCommit+" was read.\n", 1,
			"- **Releases:** v20.2.4; main was not checked, though "+mainCommit+" was read.", unchecked, 1),
		Entry("main: not checked", "- **Releases:** v20.2.4 at "+mainCommit+"; main: not checked.\n", 1,
			"- **Releases:** v20.2.4 at "+mainCommit+"; main: not checked.", unchecked, 1),
		Entry("main has not been checked", "- **Releases:** "+mainCommit+" read; main has not been checked.\n", 1,
			"- **Releases:** "+mainCommit+" read; main has not been checked.", unchecked, 1),
		Entry("main hasn't been checked", "- **Releases:** "+mainCommit+" read; main hasn't been checked.\n", 1,
			"- **Releases:** "+mainCommit+" read; main hasn't been checked.", unchecked, 1),
		Entry("main not yet checked", "- **Releases:** "+mainCommit+" read; main not yet checked.\n", 1,
			"- **Releases:** "+mainCommit+" read; main not yet checked.", unchecked, 1),
		Entry("main was never checked", "- **Releases:** "+mainCommit+" read; main was never checked.\n", 1,
			"- **Releases:** "+mainCommit+" read; main was never checked.", unchecked, 1),
		Entry("main unverified", "- **Releases:** "+mainCommit+" read; main unverified.\n", 1,
			"- **Releases:** "+mainCommit+" read; main unverified.", unchecked, 1),
		Entry("not checked on main", "- **Releases:** "+mainCommit+" read; not checked on main.\n", 1,
			"- **Releases:** "+mainCommit+" read; not checked on main.", unchecked, 1),
		Entry("not yet verified against ceph main", "- **Releases:** "+mainCommit+" read; not yet verified "+
			"against ceph main.\n", 1, "- **Releases:** "+mainCommit+" read; not yet verified against ceph main.",
			unchecked, 1),
		Entry("the main branch was not checked", "- **Releases:** "+mainCommit+" read; the main branch was not "+
			"checked.\n", 1, "- **Releases:** "+mainCommit+" read; the main branch was not checked.", unchecked, 1),
		Entry("main remains unchecked", "- **Releases:** "+mainCommit+" read; main remains unchecked.\n", 1,
			"- **Releases:** "+mainCommit+" read; main remains unchecked.", unchecked, 1),
		Entry("main is unchecked", "- **Releases:** "+mainCommit+" read; main is unchecked.\n", 1,
			"- **Releases:** "+mainCommit+" read; main is unchecked.", unchecked, 1),
		Entry("main is still unverified", "- **Releases:** "+mainCommit+" read; main is still unverified.\n", 1,
			"- **Releases:** "+mainCommit+" read; main is still unverified.", unchecked, 1),
		Entry("main was left unchecked", "- **Releases:** "+mainCommit+" read; main was left unchecked.\n", 1,
			"- **Releases:** "+mainCommit+" read; main was left unchecked.", unchecked, 1),
		Entry("unchecked on main", "- **Releases:** "+mainCommit+" read; unchecked on main.\n", 1,
			"- **Releases:** "+mainCommit+" read; unchecked on main.", unchecked, 1),
		Entry("a commit of fewer than 9 hex digits", "- **Impact:** main sends it as of a742f50.\n", 1,
			"- **Impact:** main sends it as of a742f50.", unnamedInItem+" (a742f50 has fewer than 9 hex digits)", 1),
		Entry("a commit of fewer than 9 hex digits called main's, beside one of 9", "- **Impact:** main a742f50 "+
			"sends it, as checked at "+mainCommit+".\n", 1, "- **Impact:** main a742f50 sends it, as checked at "+
			mainCommit+".", shortMain, 1),
		Entry("a commit of fewer than 9 hex digits called main's in a Found item", "- **Found:** task V1, "+
			"2026-10-15, checked at main a742f50.\n", 1, "- **Found:** task V1, 2026-10-15, checked at main a742f50.",
			shortMain, 1),
		Entry("a commit of digits alone", "- **Impact:** main still sends it after 18446744073 seconds.\n", 1,
			"- **Impact:** main still sends it after 18446744073 seconds.", unnamedInItem, 1),
		Entry("a commit in another list item", "- **Impact:** main still sends it.\n- **rgw-go:** read at "+
			mainCommit+".\n", 1, "- **Impact:** main still sends it.", unnamedInItem, 1),
		Entry("a commit in another list item nested in the same one",
			"- **Impact:**\n  - main still sends it.\n  - read at "+mainCommit+".\n", 2,
			"- main still sends it.", unnamedInItem, 1),
		Entry("a commit in a list item nested in its own",
			"- **Impact:** main still sends it:\n  - read at "+mainCommit+".\n", 1,
			"- **Impact:** main still sends it:", unnamedInItem, 1),
		Entry("a commit in the list item its own is nested in",
			"- **Impact:** read at main "+mainCommit+":\n  - main still sends it.\n", 2,
			"- main still sends it.", unnamedInItem, 2),
		Entry("a commit in another sentence of its paragraph", "Main still sends it. It was read at "+
			mainCommit+".\n", 1, "Main still sends it.", unnamedInPara, 1),
		Entry("a commit on its entry's Kind line",
			"## A\n\n- **Kind:** defect, unfixed through main at "+mainCommit+".\n- **Impact:** on main, every "+
				"request reaches it.\n", 4, "- **Impact:** on main, every request reaches it.", unnamedInItem, 2),
		Entry("a commit in its entry's Evidence",
			"## A\n\n- **Evidence:**\n  - main, still at "+mainCommit+", types it.\n- **rgw-go:** differs: it "+
				"types it as main does.\n", 5, "- **rgw-go:** differs: it types it as main does.", unnamedInItem, 2),
		Entry("a commit on its entry's Releases line",
			"## A\n\n- **Impact:** main no longer defers.\n- **Releases:** v20.2.4 and main "+mainCommit+".\n",
			3, "- **Impact:** main no longer defers.", unnamedInItem, 2),
		Entry("a commit a sentence before it says every main line below is read at",
			"- **Impact:** main ("+mainCommit+", the commit every main line below is read at) has the same "+
				"code.\n- **rgw-go:** a grant answers 400 (`rgw_common.cc:70`, main `:66`).\n", 2,
			"- **rgw-go:** a grant answers 400 (`rgw_common.cc:70`, main `:66`).", unnamedInItem, 3),
		Entry("a commit the document's opening says every claim about main below was checked at",
			"Every claim about C++ behavior below was checked against ceph main at "+mainCommit+".\n\n"+
				"## A section\n\nroughly 2k lines in main.\n", 5, "roughly 2k lines in main.", unnamedInPara, 2),
	)

	DescribeTable("pass when the main they name is no claim about ceph main's state",
		func(ctx SpecContext, text string) {
			Expect(formOnly(ctx, registry(text))).To(Succeed(), "%s", stdout)
			Expect(stdout.String()).To(Equal("citations checked: 0, failed: 0\nmain claims checked: 0, failed: 0\n"))
		},
		Entry("the s3-tests harness's main user", "Reasoning about the s3-tests harness's main user.\n"),
		Entry("rgw-go's main branch", "It merged into rgw-go's main branch.\n"),
		Entry("Rook main", "It passes against the current Rook main.\n"),
		Entry("a file", "Its `cmd/rgw-go` builds from main.go.\n"),
		Entry("a function", "Its main() starts the gateway.\n"),
		Entry("a code span", "It runs `git log origin/main`.\n"),
		Entry("a URL", "See https://github.com/ceph/s3-tests/blob/main/README.rst for more.\n"),
		Entry("a link's destination", "See [the README](https://github.com/ceph/s3-tests/blob/main/README.rst).\n"),
		Entry("an entry's title in a heading", "## Main's radosgw reads an empty account\n"),
		Entry("an entry's title in a link to it",
			"- See \"[Main's radosgw reads an empty account](#mains-radosgw-reads-an-empty-account)\".\n"),
		Entry("an entry's title in a link to it in another registry",
			"Recorded in \"[main sends it](ceph-upstream-bugs.md#main-sends-it)\".\n"),
		Entry("the Found line's history", "- **Found:** phase 2, 2026-10-15, confirmed at v19.2.6, v20.2.4 and "+
			"main.\n  - It read the fix on main.\n"),
		Entry("a fenced code block", "```\ngit checkout main\n```\n"),
	)

	It("count a Found item's main and the id after it as a claim, which names its commit", func(ctx SpecContext) {
		Expect(formOnly(ctx, registry("- **Found:** task V1, 2026-10-15, checked at main "+mainCommit+".\n"))).
			To(Succeed())
		Expect(stdout.String()).To(Equal("citations checked: 0, failed: 0\nmain claims checked: 1, failed: 0\n"))
	})

	Describe("with the clone", func() {
		var (
			ceph *cephFixture
			// side is a commit of a branch that origin/main does not descend
			// from, as a fork's or a stable branch's is.
			side string
		)

		BeforeEach(func() {
			ceph = newCephFixture()
			ceph.git("checkout", "-q", "-b", "side")
			ceph.write("src/rgw/rgw_side.cc", "side\n")
			side = ceph.commit("side")
			ceph.git("checkout", "-q", "main")
		})

		run := func(ctx SpecContext, args ...string) error {
			return citecheck.Run(ctx, args, nil, stdout, stderr)
		}

		DescribeTable("pass when a commit they name is on ceph main",
			func(ctx SpecContext, text func() string) {
				Expect(run(ctx, "--ceph", ceph.dir, registry(text()))).To(Succeed(), "%s", stdout)
				Expect(stdout.String()).To(Equal("citations checked: 0, failed: 0\nmain claims checked: 1, failed: 0\n"))
			},
			Entry("an ancestor of origin/main", func() string {
				return "- **Impact:** main still sends it at " + ceph.v1 + ".\n"
			}),
			Entry("one on main beside one that is not", func() string {
				return "- **Impact:** the side branch's " + side + " sends it, as main still does at " + ceph.v1 + ".\n"
			}),
		)

		DescribeTable("fail when no commit they name is on ceph main, saying why",
			func(ctx SpecContext, text, why func() string) {
				md := registry(text() + "\n")
				err := run(ctx, "--ceph", ceph.dir, md)
				Expect(citecheck.ExitStatus(err)).To(Equal(1))
				Expect(stdout.String()).To(Equal(md + ":1: " + text() + ": a claim about ceph main that " +
					"names no commit on ceph main: " + why() + ": write the main commit it was checked at in its own " +
					"list item, not one nested in it or around it\ncitations checked: 0, failed: 0\n" +
					"main claims checked: 1, failed: 1\n"))
			},
			Entry("a commit of another branch", func() string {
				return "- **Impact:** main still sends it at " + side + "."
			}, func() string {
				return "commit " + side + " is not on ceph main: " + ceph.dir + "'s origin/main does not descend from it"
			}),
			Entry("a commit the clone lacks", func() string {
				return "- **Impact:** main still sends it at " + mainCommit + "."
			}, func() string {
				return "commit id " + mainCommit + " names no commit in " + ceph.dir + ", or abbreviates more than one"
			}),
		)

		DescribeTable("fail when the commit they call main's is not on ceph main, a Found item's included",
			func(ctx SpecContext, text func() string, claims int) {
				md := registry(text() + "\n")
				err := run(ctx, "--ceph", ceph.dir, md)
				Expect(citecheck.ExitStatus(err)).To(Equal(1))
				Expect(stdout.String()).To(Equal(md + ":1: " + text() + ": calls " + side + " main's, but commit " +
					side + " is not on ceph main: " + ceph.dir + "'s origin/main does not descend from it: name the " +
					"main commit it was checked at, or the branch " + side + " is on\ncitations checked: 0, failed: 0\n" +
					fmt.Sprintf("main claims checked: %d, failed: 1\n", claims)))
			},
			Entry("beside a commit on main", func() string {
				return "- **Impact:** main " + side + " sends it, as main does at " + ceph.v1 + "."
			}, 2),
			Entry("in a Found item", func() string {
				return "- **Found:** task V1, 2026-10-15, checked at main " + side + "."
			}, 1),
		)

		It("refuse a clone with neither an origin/main nor a main branch", func(ctx SpecContext) {
			ceph.git("update-ref", "-d", "refs/remotes/origin/main")
			ceph.git("branch", "-m", "main", "trunk")
			err := run(ctx, "--ceph", ceph.dir, registry("- **Impact:** main sends it at "+ceph.v1+".\n"))
			Expect(citecheck.ExitStatus(err)).To(Equal(2))
			Expect(err).To(MatchError(ceph.dir + " has neither an origin/main nor a main branch to check main " +
				"commits against"))
			Expect(stdout.String()).To(BeEmpty())
		})

		It("pass under --form-only whatever commit they name, since nothing without a clone tells", func(ctx SpecContext) {
			Expect(formOnly(ctx, registry("- **Impact:** main still sends it at "+side+".\n"))).To(Succeed())
		})

		DescribeTable("are checked by every run but --content, the file left as it is",
			func(ctx SpecContext, summary string, args ...string) {
				text := "- **Impact:** main still sends it.\n"
				md := registry(text)
				err := run(ctx, append(args, "--ceph", ceph.dir, md)...)
				Expect(citecheck.ExitStatus(err)).To(Equal(1))
				Expect(stdout.String()).To(Equal(md + ":1: - **Impact:** main still sends it.: " + unnamedInItem + "\n" +
					summary + "main claims checked: 1, failed: 1\n"))
				Expect(os.ReadFile(md)).To(BeEquivalentTo(text))
			},
			Entry("by default", "citations checked: 0, failed: 0\n"),
			Entry("under --fix", "citations checked: 0, failed: 0, rewritten: 0\n", "--fix"),
		)

		It("are not checked under --content, which checks the links alone", func(ctx SpecContext) {
			md := registry("- **Impact:** main still sends it.\n")
			Expect(run(ctx, "--content", "--ceph", ceph.dir, md)).To(Succeed())
			Expect(stdout.String()).To(Equal("links checked: 0, held: 0, failed: 0, listed: 0\n"))
		})
	})

	It("reads only the registries", func(ctx SpecContext) {
		Expect(formOnly(ctx, markdown("plan.md", "Main still sends it.\n"))).To(Succeed())
		Expect(stdout.String()).To(Equal("citations checked: 0, failed: 0\n"))
	})

	It("reads docs/exclusions.md as a registry too", func(ctx SpecContext) {
		md := registryAt("exclusions.md", "Main still sends it.\n")
		Expect(citecheck.ExitStatus(formOnly(ctx, md))).To(Equal(1))
		Expect(stdout.String()).To(Equal(md + ":1: Main still sends it.: " + unnamedInPara + "\n" +
			"citations checked: 0, failed: 0\nmain claims checked: 1, failed: 1\n"))
	})

	It("reports a failing citation and a claim that names no commit in line order, and fails on both",
		func(ctx SpecContext) {
			md := registry("- **Impact:** main still sends it.\n- see `src/rgw/rgw_op.cc:10@v20.2.4`.\n")
			err := formOnly(ctx, md)
			Expect(citecheck.ExitStatus(err)).To(Equal(1))
			Expect(err).To(MatchError(And(ContainSubstring("1 of 1 citations failed"),
				ContainSubstring("1 of 1 main claims name no main commit"))))
			Expect(stdout.String()).To(Equal(md + ":1: - **Impact:** main still sends it.: " + unnamedInItem + "\n" +
				md + ":2: src/rgw/rgw_op.cc:10@v20.2.4: not a link\n" +
				"citations checked: 1, failed: 1\nmain claims checked: 1, failed: 1\n"))
		})
})
