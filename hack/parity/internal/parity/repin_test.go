package parity_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/hack/parity/internal/parity"
)

// suiteDir is where the fixture clone holds the suite, as run.sh's
// S3TESTS_DIR names it.
const suiteDir = "src/test/rgw/s3-tests"

// gitRepo is a git repository a spec commits fixtures to.
type gitRepo struct{ dir string }

func newGitRepo() *gitRepo {
	r := &gitRepo{dir: GinkgoT().TempDir()}
	r.git("init", "-q", "-b", "main")
	return r
}

// git runs git in the repository, isolated from the caller's configuration.
// A fixture outlives the spec node that builds it, so no node's context
// bounds it.
func (r *gitRepo) git(args ...string) string {
	cmd := exec.CommandContext(context.Background(), "git", append([]string{"-C", r.dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=parity", "GIT_AUTHOR_EMAIL=parity@rgw-go.test",
		"GIT_COMMITTER_NAME=parity", "GIT_COMMITTER_EMAIL=parity@rgw-go.test")
	out, err := cmd.CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), "git %s: %s", strings.Join(args, " "), out)
	return strings.TrimSpace(string(out))
}

// serveTrusted makes the repository serve as the s3-tests repository does:
// a fetch may filter and want any commit by id, as GitHub allows, and its
// commits from here on go to rgw-go-s3tests.
func (r *gitRepo) serveTrusted() {
	r.git("config", "uploadpack.allowFilter", "true")
	r.git("config", "uploadpack.allowAnySHA1InWant", "true")
	r.git("checkout", "-q", "-b", parity.TrustedBranch)
}

// trust moves rgw-go-s3tests to commit, as a rewrite merged there would.
func (r *gitRepo) trust(commit string) {
	r.git("update-ref", "refs/heads/"+parity.TrustedBranch, commit)
}

// write sets a file of the suite.
func (r *gitRepo) write(file, text string) {
	full := filepath.Join(r.dir, suiteDir, file)
	Expect(os.MkdirAll(filepath.Dir(full), 0o750)).To(Succeed())
	Expect(os.WriteFile(full, []byte(text), 0o600)).To(Succeed())
}

// insert inserts text before the first line of a suite file that equals
// before.
func (r *gitRepo) insert(file, before, text string) {
	data, err := os.ReadFile(filepath.Join(r.dir, suiteDir, file))
	Expect(err).NotTo(HaveOccurred())
	lines := strings.SplitAfter(string(data), "\n")
	for i, l := range lines {
		if strings.TrimSuffix(l, "\n") == before {
			r.write(file, strings.Join(lines[:i], "")+text+strings.Join(lines[i:], ""))
			return
		}
	}
	Fail("no line " + before + " in " + file)
}

func (r *gitRepo) commit(msg string) string {
	r.git("add", "-A")
	r.git("commit", "-q", "--no-verify", "-m", msg)
	return r.git("rev-parse", "HEAD")
}

const fixtureTestS3 = `import pytest
from . import (
    get_prefix,
    )
from .utils import assert_raises

DOC = """
a
b
"""


def test_bucket_list_empty():
    assert get_prefix() is None


@pytest.mark.list_objects_v2
def test_bucket_list_v2():
    assert True


@pytest.mark.usefixtures("prefixed")
def test_uses_fixture():
    assert True


def test_with_fixture(tmp_path):
    assert tmp_path
`

// seed commits the suite at the pin.
func (r *gitRepo) seed() string {
	r.write("pytest.ini", "[pytest]\nmarkers =\n    fails_on_rgw\n    list_objects_v2\n")
	r.write("requirements.txt", "boto3\n")
	r.write("README.rst", "s3-tests\n")
	r.write("s3tests/functional/__init__.py", "import pytest\n\nprefix = None\n\n\ndef get_prefix():\n    return prefix\n\n\n"+
		"@pytest.fixture(autouse=True)\ndef setup_teardown():\n    yield\n")
	r.write("s3tests/functional/utils.py", "def assert_raises(exc, fn, *args, **kwargs):\n    return fn(*args, **kwargs)\n")
	r.write("s3tests/functional/test_s3.py", fixtureTestS3)
	r.write("s3tests/functional/test_headers.py", "def test_headers_one():\n    assert True\n")
	return r.commit("the suite")
}

// pinFiles writes a run.sh that fetches from clone, a PIN file pinning
// commit and two ledgers whose chains end at it, and returns the options
// that move them.
func pinFiles(dir, clone, commit, next string) parity.RepinOptions {
	runSh, pinFile := filepath.Join(dir, "run.sh"), filepath.Join(dir, "PIN")
	Expect(os.WriteFile(runSh, runShText(suiteDir, "file://"+clone), 0o700)).To(Succeed())
	Expect(os.WriteFile(pinFile, pinText(commit), 0o644)).To(Succeed()) //nolint:gosec // a PIN file is read by all
	opts := parity.RepinOptions{Clone: clone, New: next, RunSh: runSh, PinFile: pinFile}
	for _, release := range []string{"squid", "tentacle"} {
		l := ledgerOf(map[string][]parity.Entry{"s3tests.functional.test_s3::test_bucket_list_empty": {entry(commit, "passed")}})
		l.Release, l.Pins[0].Source = release, commit
		file := filepath.Join(dir, release+".json")
		writeLedgerFile(file, l)
		opts.Ledgers = append(opts.Ledgers, file)
	}
	return opts
}

// snapshot returns the contents of the files a pin move writes.
func snapshot(opts parity.RepinOptions) []string {
	var out []string
	for _, f := range append([]string{opts.RunSh, opts.PinFile}, opts.Ledgers...) {
		data, err := os.ReadFile(f)
		Expect(err).NotTo(HaveOccurred())
		out = append(out, string(data))
	}
	return out
}

var _ = Describe("Repin", func() {
	var (
		repo *gitRepo
		old  string
		dir  string
	)

	BeforeEach(func() {
		repo = newGitRepo()
		old = repo.seed()
		repo.serveTrusted()
		dir = GinkgoT().TempDir()
	})

	Describe("a commit that only adds tests", func() {
		var next string

		BeforeEach(func() {
			repo.write("s3tests/functional/test_s3.py", fixtureTestS3+"\n\n@pytest.mark.fails_on_rgw\n@pytest.mark.new_marker\ndef test_bucket_new():\n"+
				"    assert get_prefix() is None\n\n\n@pytest.mark.fails_on_rgw # only supported for directory buckets\ndef test_param():\n    assert True\n")
			repo.insert("s3tests/functional/test_s3.py", "def test_with_fixture(tmp_path):",
				"def test_between():\n    # a comment in the body\n    assert True\n\n\n")
			repo.write("s3tests/functional/test_headers.py", "def test_headers_one():\n    assert True\n\n\ndef test_headers_two():\n    assert True\n")
			repo.write("pytest.ini", "[pytest]\nmarkers =\n    fails_on_rgw\n    new_marker\n    list_objects_v2\n")
			next = repo.commit("add tests")
		})

		It("moves the pin, appends an additive pin to each ledger and names the tests to record", func(ctx SpecContext) {
			opts := pinFiles(dir, repo.dir, old, next)
			res, err := parity.Repin(ctx, opts)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Admitted).To(Equal(parity.AdmitAdditive))
			Expect(res.Added).To(ConsistOf(
				"s3tests/functional/test_s3.py::test_bucket_new", "s3tests/functional/test_s3.py::test_param",
				"s3tests/functional/test_s3.py::test_between", "s3tests/functional/test_headers.py::test_headers_two"))

			pin, err := os.ReadFile(opts.PinFile)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(pin)).To(Equal(next + "\n"))
			info, err := os.Stat(opts.PinFile)
			Expect(err).NotTo(HaveOccurred())
			Expect(info.Mode().Perm()).To(Equal(os.FileMode(0o644)))
			for _, file := range opts.Ledgers {
				Expect(readLedger(file).Pins).To(Equal([]parity.Pin{
					{Source: old, Admitted: parity.AdmitBase}, {Source: next, Admitted: parity.AdmitAdditive},
				}))
			}
		})

		It("runs as the pin command, writing the tests to record", func(ctx SpecContext) {
			opts := pinFiles(dir, repo.dir, old, next)
			tests := filepath.Join(dir, "tests.txt")
			status, stdout, stderr := run(ctx, "pin", "--clone", repo.dir, "--new", next, "--run-sh", opts.RunSh,
				"--pin-file", opts.PinFile, "--ledger", opts.Ledgers[0], "--ledger", opts.Ledgers[1], "--tests", tests)
			Expect(status).To(Equal(0), stderr)
			Expect(stdout).To(ContainSubstring("additive: " + next + " over " + old))
			Expect(stdout).To(ContainSubstring("4 tests to record:\n"))
			data, err := os.ReadFile(tests)
			Expect(err).NotTo(HaveOccurred())
			Expect(strings.Fields(string(data))).To(HaveLen(4))
			Expect(string(data)).To(ContainSubstring("s3tests/functional/test_headers.py::test_headers_two\n"))
		})
	})

	// appended returns the fixture's test_s3.py with text after it.
	appended := func(text string) func() {
		return func() { repo.write("s3tests/functional/test_s3.py", fixtureTestS3+"\n\n"+text) }
	}

	DescribeTable("refuses a descendant that does more than add tests, naming the line and writing nothing",
		func(ctx SpecContext, want string, change func()) {
			change()
			next := repo.commit("change")
			opts := pinFiles(dir, repo.dir, old, next)
			before := snapshot(opts)
			_, err := parity.Repin(ctx, opts)
			Expect(err).To(MatchError(ContainSubstring(want)))
			Expect(err).To(MatchError(ContainSubstring("needs a full record, which appends a base pin")))
			Expect(snapshot(opts)).To(Equal(before))
		},
		Entry("a changed line", "test_s3.py: line 19 of ", func() {
			repo.write("s3tests/functional/test_s3.py", strings.Replace(fixtureTestS3, "assert True", "assert False", 1))
		}),
		Entry("a removed line", "is changed or removed:     assert tmp_path", func() {
			repo.write("s3tests/functional/test_s3.py", strings.Replace(fixtureTestS3, "    assert tmp_path\n", "", 1))
		}),
		Entry("a line added to an existing test", "line 14 is added inside the existing statement at line 13:     assert 1", func() {
			repo.insert("s3tests/functional/test_s3.py", "    assert get_prefix() is None", "    assert 1\n")
		}),
		Entry("a comment added to an existing test", "line 14 is added inside the existing statement at line 13:     # why", func() {
			repo.insert("s3tests/functional/test_s3.py", "    assert get_prefix() is None", "    # why\n")
		}),
		Entry("a def that takes in an existing test's body", "line 15, existing code, would join the statement added at line 14", func() {
			repo.insert("s3tests/functional/test_s3.py", "    assert get_prefix() is None", "def test_x():\n")
		}),
		Entry("a decorator on an existing test", "line 18, existing code, would join the statement added at line 17", func() {
			repo.insert("s3tests/functional/test_s3.py", "@pytest.mark.list_objects_v2", "@pytest.mark.skip\n")
		}),
		Entry("a line inside an existing string", "line 9 is added inside the existing statement at line 7: def test_in_string():", func() {
			repo.insert("s3tests/functional/test_s3.py", "b", "def test_in_string():\n")
		}),
		Entry("a blank line inside an existing string", "line 9 is added inside the existing statement at line 7: ", func() {
			repo.insert("s3tests/functional/test_s3.py", "b", "\n")
		}),
		Entry("a multi-line autouse fixture", "is a top-level line other than a @pytest.mark decorator or a def test_: @pytest.fixture(",
			appended("@pytest.fixture(\n    autouse=True,\n)\ndef everywhere():\n    yield\n")),
		Entry("a chained assignment that rebinds a test", "is a top-level line other than a @pytest.mark decorator or a def test_: "+
			"NEW_LIMIT = test_bucket_list_v2 = None", appended("NEW_LIMIT = test_bucket_list_v2 = None\n")),
		Entry("a def shadowing a builtin", "def test_: def len(x):", appended("def len(x):\n    return 0\n")),
		Entry("a fixture overriding one an existing test requests through usefixtures", "def test_: @pytest.fixture",
			appended("@pytest.fixture\ndef prefixed():\n    return 1\n")),
		Entry("__test__", "def test_: __test__ = False", appended("__test__ = False\n")),
		Entry("an assignment run at import", "def test_: LIMIT = print('import')", appended("LIMIT = print('import')\n")),
		Entry("a class whose body runs at import", "def test_: class Helper:", appended("class Helper:\n    print('import')\n")),
		Entry("a test under a decorator other than a mark", "def test_: @functools.wraps(print)",
			appended("@functools.wraps(print)\ndef test_wrapped():\n    assert True\n")),
		Entry("a test whose default runs at import", "line 31 adds test_default with a header other than def test_default(<bare names>): on one line",
			appended("def test_default(x=print('import')):\n    assert True\n")),
		Entry("a test whose annotation runs at import", "line 31 adds test_annotated with a header other than",
			appended("def test_annotated(x: print('import')):\n    assert True\n")),
		Entry("a test whose return annotation runs at import", "line 31 adds test_returning with a header other than",
			appended("def test_returning() -> print('import'):\n    assert True\n")),
		Entry("a test that rebinds an existing test's name", "adds test_bucket_list_empty, a name the module already holds",
			appended("def test_bucket_list_empty():\n    assert False\n")),
		Entry("an async test", "def test_: async def test_later():", appended("async def test_later():\n    assert True\n")),
		Entry("a top-level comment", "def test_: # a note", appended("# a note\n")),
		Entry("an import", "def test_: import os", appended("import os\n")),
		Entry("pytestmark", "def test_: pytestmark = pytest.mark.skip", appended("pytestmark = pytest.mark.skip\n")),
		Entry("a statement that binds nothing", "def test_: print('collected')", appended("print('collected')\n")),
		Entry("a star import", "def test_: from .utils import *", appended("from .utils import *\n")),
		Entry("a decorator left dangling", "adds a statement that is not a def test_: @pytest.mark.skip", appended("@pytest.mark.skip\n")),
		Entry("a test class", "def test_: class TestThing:", appended("class TestThing:\n    def test_it(self):\n        assert True\n")),
		Entry("a mark argument that calls a function at import", "is a @pytest.mark line with more than its name and a comment: @pytest.mark.parametrize('x', [setattr(pytest, 'approx', None)])",
			appended("@pytest.mark.parametrize('x', [setattr(pytest, 'approx', None)])\ndef test_p(x):\n    assert x is None\n")),
		Entry("a mark whose arguments run across lines", "is a @pytest.mark line with more than its name and a comment: @pytest.mark.skipif(__import__('os').environ.update(S3TEST_CONF='/x')",
			appended("@pytest.mark.skipif(__import__('os').environ.update(S3TEST_CONF='/x')\n    or False, reason='r')\ndef test_q():\n    assert True\n")),
		Entry("a mark argument that names a variable", "is a @pytest.mark line with more than its name and a comment: @pytest.mark.skipif(sys, reason='r')",
			appended("@pytest.mark.skipif(sys, reason='r')\ndef test_n():\n    assert True\n")),
		Entry("a mark argument that reads an attribute", "is a @pytest.mark line with more than its name and a comment: @pytest.mark.skipif(os.name == 'nt', reason='r')",
			appended("@pytest.mark.skipif(os.name == 'nt', reason='r')\ndef test_a2():\n    assert True\n")),
		Entry("a mark argument that is an f-string", "is a @pytest.mark line with more than its name and a comment: @pytest.mark.parametrize('x', [f'{print(1)}'])",
			appended("@pytest.mark.parametrize('x', [f'{print(1)}'])\ndef test_f(x):\n    assert x\n")),
		Entry("a mark argument that is a lambda", "is a @pytest.mark line with more than its name and a comment: @pytest.mark.parametrize('x', [lambda: 1])",
			appended("@pytest.mark.parametrize('x', [lambda: 1])\ndef test_l(x):\n    assert x\n")),
		Entry("a mark reached through an attribute", "is a @pytest.mark line with more than its name and a comment: @pytest.mark.parametrize.with_args('x')",
			appended("@pytest.mark.parametrize.with_args('x')\ndef test_w(x):\n    assert x\n")),
		Entry("two strings run together", "is a @pytest.mark line with more than its name and a comment: @pytest.mark.parametrize('x' 'y', [1])",
			appended("@pytest.mark.parametrize('x' 'y', [1])\ndef test_c(xy):\n    assert xy\n")),
		Entry("a mark with literal arguments", "is a @pytest.mark line with more than its name and a comment: @pytest.mark.skipif(True, reason='flaky')",
			appended("@pytest.mark.skipif(True, reason='flaky')\ndef test_lit():\n    assert True\n")),
		Entry("a one-line parametrize of literals", "is a @pytest.mark line with more than its name and a comment: @pytest.mark.parametrize('n', [1, 2])",
			appended("@pytest.mark.parametrize('n', [1, 2])\ndef test_par(n):\n    assert n\n")),
		Entry("a mark whose comment is not ASCII", "line 31 holds a character other than printable ASCII: \"@pytest.mark.fails_on_rgw  # caf\u00e9\"",
			appended("@pytest.mark.fails_on_rgw  # caf\u00e9\ndef test_cafe():\n    assert True\n")),
		Entry("a carriage return in a mark's comment, which Python reads as a line break", "holds a character Python or pytest may read as a line break, or that hides text: \"@pytest.mark.fails_on_rgw  # c\\r@__import__('builtins').print(1) or pytest.mark.fails_on_rgw\"",
			appended("@pytest.mark.fails_on_rgw  # c\r@__import__('builtins').print(1) or pytest.mark.fails_on_rgw\ndef test_cr():\n    assert True\n")),
		Entry("a carriage return in a test's body, which ends the test for Python", "holds a character Python or pytest may read as a line break, or that hides text: \"    pass\\rprint('import')\"",
			appended("def test_crbody():\n    pass\rprint('import')\n")),
		Entry("a vertical tab in a test's body, which Python does not compile", "holds a character Python or pytest may read as a line break, or that hides text: \"    pass\\vprint('import')\"",
			appended("def test_vt():\n    pass\vprint('import')\n")),
		Entry("a line separator in a mark's comment", "holds a character Python or pytest may read as a line break, or that hides text: \"@pytest.mark.fails_on_rgw  # c\\u2028x\"",
			appended("@pytest.mark.fails_on_rgw  # c\u2028x\ndef test_ls():\n    assert True\n")),
		Entry("a carriage return inside a line that looks blank", "holds a character Python or pytest may read as a line break, or that hides text: \"  \\r  \"",
			appended("  \r  \ndef test_blank_cr():\n    assert True\n")),
		Entry("a column-0 line PEP 701 hides from the scan in a triple-quoted string", "line 33 is at column 0 inside the test added at line 31, whose body must be indented to its end: print('import')",
			appended("def test_x():\n    s = f'{\"\"\"'}\"\"\"}'\nprint('import')\nx = 1  # \"\"\"\n")),
		Entry("a column-0 line PEP 701 hides from the scan in brackets", "line 33 is at column 0 inside the test added at line 31, whose body must be indented to its end: print('import')",
			appended("def test_x():\n    s = f'{'('}'\nprint('import')\ny = f'{')'}'\n")),
		Entry("a string that runs across lines in a test's body", "line 32 starts or ends inside a string that runs across lines or a backslash continuation, which the proof does not follow:     s = '''",
			appended("def test_x():\n    s = '''\n    text\n    '''\n")),
		Entry("a one-quote string left open at the end of its line, which PEP 701 continues", "line 32 starts or ends inside a string that runs across lines or a backslash continuation, which the proof does not follow:     s = f\"{",
			appended("def test_x():\n    s = f\"{\n    1}\"\n")),
		Entry("a line continued by a backslash", "line 32 ends in a backslash, which continues it onto the next: \"    x = 1 + \\\\\"",
			appended("def test_x():\n    x = 1 + \\\n        2\n")),
		Entry("a tab", "line 32 holds a tab; an added line indents with spaces: \"\\tassert True\"",
			appended("def test_x():\n\tassert True\n")),
		Entry("a semicolon in a test's body", "line 32 holds a \";\", which can put a second statement on a line: \"    assert True; print('x')\"",
			appended("def test_x():\n    assert True; print('x')\n")),
		Entry("a semicolon on a def line", "line 31 holds a \";\", which can put a second statement on a line: \"def test_x(): pass; import os\"",
			appended("def test_x(): pass; import os\n")),
		Entry("a body on the def line", "line 31 adds test_x with a header other than def test_x(<bare names>): on one line: def test_x(): pass",
			appended("def test_x(): pass\n    print('never')\n")),
		Entry("a def header across lines", "line 31 adds test_x with a header other than def test_x(<bare names>): on one line: def test_x(",
			appended("def test_x(\n    a,\n):\n    assert a\n")),
		Entry("a def that dedents early", "line 33 is a top-level line other than a @pytest.mark decorator or a def test_: print('import')",
			appended("def test_x():\n    assert True\nprint('import')\n")),
		Entry("a def with no body", "line 31 adds test_x with no indented body",
			appended("def test_x():  # nothing yet\n")),
		Entry("a non-ASCII identifier", "line 32 holds a character other than printable ASCII: \"    \u00e9 = 1\"",
			appended("def test_x():\n    \u00e9 = 1\n")),
		Entry("a CR LF line ending", "line 31 holds a character Python or pytest may read as a line break, or that hides text: \"def test_x():\\r\"",
			appended("def test_x():\r\n    assert True\n")),
		Entry("a bare parametrize, whose arguments pytest needs to collect the module", "line 31 is a @pytest.mark line whose marker parametrize pytest.ini does not register at the new pin: @pytest.mark.parametrize",
			appended("@pytest.mark.parametrize\ndef test_p():\n    assert True\n")),
		Entry("a marker pytest.ini does not register", "line 31 is a @pytest.mark line whose marker not_registered pytest.ini does not register at the new pin: @pytest.mark.not_registered",
			appended("@pytest.mark.not_registered\ndef test_u():\n    assert True\n")),
		Entry("a built-in marker pytest.ini does not register", "line 31 is a @pytest.mark line whose marker usefixtures pytest.ini does not register at the new pin: @pytest.mark.usefixtures",
			appended("@pytest.mark.usefixtures\ndef test_f():\n    assert True\n")),
		Entry("a helper module's new function", "s3tests/functional/utils.py: changed; only a test module or pytest.ini may change", func() {
			repo.write("s3tests/functional/utils.py", "def assert_raises(exc, fn, *args, **kwargs):\n    return fn(*args, **kwargs)\n\n\n"+
				"def new_helper():\n    return True\n")
		}),
		Entry("a new module at the suite's root, which could shadow a package", "boto3.py: added; only a test module or pytest.ini may change", func() {
			repo.write("boto3.py", "x = 1\n")
		}),
		Entry("a new conftest.py", "conftest.py: added; only a test module or pytest.ini may change", func() {
			repo.write("conftest.py", "import pytest\n")
		}),
		Entry("a requirement", "requirements.txt: changed; only a test module or pytest.ini may change", func() {
			repo.write("requirements.txt", "boto3\nmoto\n")
		}),
		Entry("a document", "README.rst: changed; only a test module or pytest.ini may change", func() {
			repo.write("README.rst", "s3-tests\nmore\n")
		}),
		Entry("pytest.ini options", "pytest.ini: line 2 adds more than a marker to [pytest] markers: addopts = -x", func() {
			repo.write("pytest.ini", "[pytest]\naddopts = -x\nmarkers =\n    fails_on_rgw\n    list_objects_v2\n")
		}),
		Entry("a carriage return in a marker's description, which pytest reads as a line break", "pytest.ini: line 5 holds a character Python or pytest may read as a line break, or that hides text: \"    fast: x\\raddopts = -k nothing\"", func() {
			repo.write("pytest.ini", "[pytest]\nmarkers =\n    fails_on_rgw\n    list_objects_v2\n    fast: x\raddopts = -k nothing\n")
		}),
		Entry("a line separator in a marker's description, which pytest reads as a line break", "pytest.ini: line 5 holds a character Python or pytest may read as a line break, or that hides text: \"    fast: x\\u2028addopts = -k nothing\"", func() {
			repo.write("pytest.ini", "[pytest]\nmarkers =\n    fails_on_rgw\n    list_objects_v2\n    fast: x\u2028addopts = -k nothing\n")
		}),
		Entry("a form feed before a marker", "pytest.ini: line 5 holds a character Python or pytest may read as a line break, or that hides text: \"\\ffast\"", func() {
			repo.write("pytest.ini", "[pytest]\nmarkers =\n    fails_on_rgw\n    list_objects_v2\n\ffast\n")
		}),
		Entry("a removed file", "README.rst: removed; only a test module or pytest.ini may change", func() {
			Expect(os.Remove(filepath.Join(repo.dir, suiteDir, "README.rst"))).To(Succeed())
		}),
		Entry("a new file of another kind", "s3tests/tests.json: added; only a test module or pytest.ini may change", func() {
			repo.write("s3tests/tests.json", "{}\n")
		}),
	)

	Describe("a reworded commit", func() {
		var x string

		BeforeEach(func() {
			x = old
			repo.write("s3tests/functional/test_headers.py", "def test_headers_one():\n    assert True\n\n\ndef test_headers_two():\n    assert 1\n")
			old = repo.commit("unsigned")
		})

		It("moves the pin when each commit's tree is the same, appending a reworded pin and no test", func(ctx SpecContext) {
			next := repo.git("commit-tree", old+"^{tree}", "-p", x, "-m", "signed")
			repo.trust(next)
			opts := pinFiles(dir, repo.dir, old, next)
			res, err := parity.Repin(ctx, opts)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.Admitted).To(Equal(parity.AdmitReworded))
			Expect(res.Added).To(BeEmpty())
			Expect(res.Proof).To(ConsistOf(ContainSubstring(old + " -> " + next)))
			Expect(readLedger(opts.Ledgers[1]).Pins[1]).To(Equal(parity.Pin{Source: next, Admitted: parity.AdmitReworded}))
		})

		It("refuses a commit whose tree differs", func(ctx SpecContext) {
			repo.git("checkout", "-q", "-b", "other", x)
			repo.write("s3tests/functional/test_headers.py", "def test_headers_one():\n    assert False\n")
			next := repo.commit("another change")
			repo.trust(next)
			opts := pinFiles(dir, repo.dir, old, next)
			before := snapshot(opts)
			_, err := parity.Repin(ctx, opts)
			Expect(err).To(MatchError(ContainSubstring("has the tree")))
			Expect(snapshot(opts)).To(Equal(before))
		})

		It("refuses lines of different lengths", func(ctx SpecContext) {
			repo.git("checkout", "-q", "-b", "other", x)
			repo.write("README.rst", "one\n")
			repo.commit("one")
			repo.write("README.rst", "two\n")
			next := repo.commit("two")
			repo.trust(next)
			_, err := parity.Repin(ctx, pinFiles(dir, repo.dir, old, next))
			Expect(err).To(MatchError(ContainSubstring("1 commits lead from " + x + " to the pin but 2")))
		})
	})

	DescribeTable("refuses before proving anything",
		func(ctx context.Context, want string, setUp func(opts *parity.RepinOptions)) {
			repo.write("s3tests/functional/test_headers.py", "def test_headers_one():\n    assert True\n\n\ndef test_headers_two():\n    assert True\n")
			next := repo.commit("add a test")
			opts := pinFiles(dir, repo.dir, old, next)
			setUp(&opts)
			_, err := parity.Repin(ctx, opts)
			Expect(err).To(MatchError(ContainSubstring(want)))
		},
		Entry("a commit that is not a full id", "is not a full commit id", func(opts *parity.RepinOptions) {
			opts.New = opts.New[:12]
		}),
		Entry("the pin itself", "the pin is already", func(opts *parity.RepinOptions) {
			opts.New = readLedger(opts.Ledgers[0]).Pins[0].Source
		}),
		Entry("a ledger whose chain ends elsewhere", "not at the pin", func(opts *parity.RepinOptions) {
			l := readLedger(opts.Ledgers[1])
			Expect(parity.AddPin(l, strings.Repeat("e", 40), parity.AdmitReworded)).To(Succeed())
			writeLedgerFile(opts.Ledgers[1], l)
		}),
		Entry("a run.sh that sets S3TESTS_COMMIT another way", "sets S3TESTS_COMMIT somewhere other than its one line reading", func(opts *parity.RepinOptions) {
			data, err := os.ReadFile(opts.RunSh)
			Expect(err).NotTo(HaveOccurred())
			Expect(os.WriteFile(opts.RunSh, append(data, "S3TESTS_COMMIT=main\n"...), 0o600)).To(Succeed())
		}),
		Entry("a PIN file that is not 40 hex digits and a newline", "is not exactly 40 lowercase hex digits and a newline",
			func(opts *parity.RepinOptions) {
				Expect(os.WriteFile(opts.PinFile, []byte(strings.ToUpper(readLedger(opts.Ledgers[0]).Pins[0].Source)+"\n"), 0o600)).To(Succeed())
			}),
		Entry("a clone without the commit", "does not hold the commit", func(opts *parity.RepinOptions) {
			opts.Clone = newGitRepo().dir
		}),
	)
})
