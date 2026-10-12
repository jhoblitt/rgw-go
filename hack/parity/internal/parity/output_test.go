package parity_test

import (
	"strings"
	"unicode"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/hack/parity/internal/parity"
)

// workflowCommands returns the lines of out that the GitHub Actions runner
// would read as a workflow command, as actions/runner's ActionCommand
// parses them: "::name::" at the start after any blanks, or the older
// "##[name]" anywhere in the line.
func workflowCommands(out string) []string {
	var commands []string
	for line := range strings.SplitSeq(out, "\n") {
		if t := strings.TrimLeftFunc(line, unicode.IsSpace); strings.HasPrefix(t, "::") || strings.Contains(line, "##[") {
			commands = append(commands, line)
		}
	}
	return commands
}

// commandNames are workflow commands the Actions runner reads.
var commandNames = []string{"error", "warning", "notice", "group", "add-mask", "add-matcher"}

var _ = Describe("runner commands anywhere in a line", func() {
	for _, name := range commandNames {
		It("leaves no ##["+name+"] mid-line for the runner to read", func() {
			Expect(parity.SafeLine("zz##[" + name + "]forged")).To(Equal(`zz#\x23[` + name + `]forged`))
			Expect(parity.SafeText("parity: zz##[" + name + "]forged")).NotTo(ContainSubstring("##["))
		})

		It("prints, in check --base, a test id carrying ##["+name+"] without the command", func(ctx SpecContext) {
			f := newBaseFixture()
			id := "zz##[" + name + "]forged"
			f.appendEntry(id, entry(f.pin, "passed"))
			f.appendEntry(id, entry(f.pin, "failed"))
			status, stdout, _, _ := f.check(ctx)
			Expect(status).To(Equal(1))
			Expect(workflowCommands(stdout)).To(BeEmpty())
			Expect(stdout).To(ContainSubstring(`zz#\x23[` + name + `]forged entry 2: passed -> failed without a reason`))
		})
	}

	It("keeps SafeLine injective", func() {
		Expect(parity.SafeLine("##[x")).NotTo(Equal(parity.SafeLine(`#\x23[x`)))
		Expect(parity.SafeLine("###[x")).NotTo(ContainSubstring("##["))
	})
})

var _ = Describe("SafeLine", func() {
	DescribeTable("escapes what could forge a line or a command",
		func(in, want string) {
			Expect(parity.SafeLine(in)).To(Equal(want))
		},
		Entry("a newline", "a\nb", `a\u000ab`),
		Entry("a carriage return", "a\rb", `a\u000db`),
		Entry("an escape sequence", "a\x1b[31mb", `a\u001b[31mb`),
		Entry("a bidi override", "a\u202eb", `a\u202eb`),
		Entry("a line separator", "a\u2028b", `a\u2028b`),
		Entry("a workflow command", "::error::x", `\::error::x`),
		Entry("a workflow command after blanks", "  ::add-mask::x", `\  ::add-mask::x`),
		Entry("an old-style command", "##[error]x", `#\x23[error]x`),
		Entry("plain text", "s3tests.functional.test_s3::test_x: passed", "s3tests.functional.test_s3::test_x: passed"),
	)
})
