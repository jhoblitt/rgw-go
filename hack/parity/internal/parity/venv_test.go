package parity_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	venvConstraints = "# Python 3.14.4\nboto3==1.44.3\nbotocore==1.44.3\nPyYAML==6.0.3\n"
	venvPackages    = "boto3==1.44.3\nbotocore==1.44.3\nPyYAML==6.0.3\n"
)

var _ = Describe("venv", func() {
	var dir string

	BeforeEach(func() {
		dir = GinkgoT().TempDir()
	})

	// write puts text in a file of dir and returns its path.
	write := func(name, text string) string {
		path := filepath.Join(dir, name)
		Expect(os.WriteFile(path, []byte(text), 0o600)).To(Succeed())
		return path
	}

	// check runs venv over a constraints file and a freeze file holding the
	// texts.
	check := func(ctx SpecContext, constraints, freeze string) (status int, stdout, stderr, constraintsFile string) {
		constraintsFile = write("constraints.txt", constraints)
		status, stdout, stderr = run(ctx, "venv", "--constraints", constraintsFile, "--freeze", write("run.freeze", freeze))
		return status, stdout, stderr, constraintsFile
	}

	It("is silent and succeeds for a venv that holds the packages and the Python the constraints name", func(ctx SpecContext) {
		status, stdout, stderr, _ := check(ctx, venvConstraints, "# Python 3.14.4\n"+venvPackages)
		Expect(status).To(Equal(0), stderr)
		Expect(stdout).To(BeEmpty())
		Expect(stderr).To(BeEmpty())
	})

	DescribeTable("stops, listing what differs, when the venv's packages are not the constraints'",
		func(ctx SpecContext, packages, want string) {
			status, stdout, stderr, constraints := check(ctx, venvConstraints, "# Python 3.14.4\n"+packages)
			Expect(status).To(Equal(1))
			Expect(stdout).To(BeEmpty())
			Expect(stderr).To(Equal("the venv holds other packages than " + constraints + " names (< constraints, > venv):\n" + want))
		},
		Entry("a package at another version", "boto3==1.44.3\nbotocore==1.44.4\nPyYAML==6.0.3\n",
			"< botocore==1.44.3\n> botocore==1.44.4"),
		Entry("a package the constraints do not name, as a new requirement installs at its latest version",
			"boto3==1.44.3\nbotocore==1.44.3\nmoto==5.2.0\nPyYAML==6.0.3\n", "> moto==5.2.0"),
		Entry("a package the venv lacks", "boto3==1.44.3\nPyYAML==6.0.3\n", "< botocore==1.44.3"),
		Entry("several, in the order of the package names, a name's constraints line before the venv's",
			"botocore==1.44.4\nmoto==5.2.0\nPyYAML==6.0.3\n",
			"< boto3==1.44.3\n< botocore==1.44.3\n> botocore==1.44.4\n> moto==5.2.0"),
	)

	It("warns, and succeeds, when only the Python is another, since it is no package", func(ctx SpecContext) {
		status, stdout, stderr, constraints := check(ctx, venvConstraints, "# Python 3.14.7\n"+venvPackages)
		Expect(status).To(Equal(0), stderr)
		Expect(stdout).To(BeEmpty())
		Expect(stderr).To(Equal("parity: warning: the venv was built by Python 3.14.7, " + constraints +
			" names Python 3.14.4: this run's pytest_freeze will differ from the ledgers'\n"))
	})

	It("warns of another Python and stops for the packages in the same run", func(ctx SpecContext) {
		status, _, stderr, _ := check(ctx, venvConstraints, "# Python 3.14.7\nboto3==1.44.3\nPyYAML==6.0.3\n")
		Expect(status).To(Equal(1))
		Expect(stderr).To(ContainSubstring("warning: the venv was built by Python 3.14.7"))
		Expect(stderr).To(ContainSubstring("< botocore==1.44.3"))
	})

	DescribeTable("exits 2 for a freeze it cannot read as one",
		func(ctx SpecContext, constraints, freeze, want string) {
			status, _, stderr, _ := check(ctx, constraints, freeze)
			Expect(status).To(Equal(2))
			Expect(stderr).To(ContainSubstring(want))
		},
		Entry("a venv's freeze with no Python line", venvConstraints, venvPackages,
			"run.freeze does not start with a line \"# Python X.Y.Z\", as a venv's freeze does"),
		Entry("constraints with no Python line", venvPackages, "# Python 3.14.4\n"+venvPackages,
			"constraints.txt does not start with a line \"# Python X.Y.Z\", as a venv's freeze does"),
		Entry("an empty freeze", venvConstraints, "", "run.freeze does not start with a line"),
	)

	It("exits 2 for a file that is not there, naming it", func(ctx SpecContext) {
		status, _, stderr := run(ctx, "venv", "--constraints", filepath.Join(dir, "none.txt"), "--freeze", write("run.freeze", "# Python 3.14.4\n"))
		Expect(status).To(Equal(2))
		Expect(stderr).To(ContainSubstring("reading the freeze: open " + filepath.Join(dir, "none.txt")))
	})

	DescribeTable("exits 2 without its flags",
		func(ctx SpecContext, args []string, want string) {
			status, _, stderr := run(ctx, append([]string{"venv"}, args...)...)
			Expect(status).To(Equal(2))
			Expect(stderr).To(ContainSubstring(want))
		},
		Entry("the constraints", []string{"--freeze", "x"}, "--constraints is required"),
		Entry("the freeze", []string{"--constraints", "x"}, "--freeze is required"),
	)
})
