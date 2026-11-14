package parity

import (
	"cmp"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// A freeze is the text hack/s3tests/run.sh leaves beside a run's report, its
// .freeze file, and keeps in constraints.txt: the Python that built the venv
// as "# Python X.Y.Z", then each package at its version, name==version a
// line, as pip freeze lists them.
type freeze struct {
	// python is the first line, without its "# ".
	python string
	// packages are the other lines.
	packages []string
}

var rePythonLine = regexp.MustCompile(`^# (Python \d[0-9A-Za-z.+-]*)$`)

// parseFreeze reads a freeze's bytes; file names them in errors.
func parseFreeze(data []byte, file string) (freeze, error) {
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	m := rePythonLine.FindStringSubmatch(lines[0])
	if m == nil {
		return freeze{}, fmt.Errorf("%s does not start with a line \"# Python X.Y.Z\", as a venv's freeze does", file)
	}
	f := freeze{python: m[1]}
	for _, l := range lines[1:] {
		if l != "" {
			f.packages = append(f.packages, l)
		}
	}
	return f, nil
}

// packageKey is the name of the package a freeze line names, in lower case,
// which is how pip freeze sorts them.
func packageKey(line string) string {
	name, _, _ := strings.Cut(line, "==")
	return strings.ToLower(name)
}

// packageDrift returns how the venv's packages differ from the constraints',
// a line each, by package name: "< " for a line only the constraints hold,
// "> " for one only the venv does. A package at another version is both.
func packageDrift(constraints, venv freeze) []string {
	type drift struct{ key, side, line string }
	var ds []drift
	for _, l := range constraints.packages {
		if !slices.Contains(venv.packages, l) {
			ds = append(ds, drift{packageKey(l), "<", l})
		}
	}
	for _, l := range venv.packages {
		if !slices.Contains(constraints.packages, l) {
			ds = append(ds, drift{packageKey(l), ">", l})
		}
	}
	slices.SortFunc(ds, func(a, b drift) int { return cmp.Or(strings.Compare(a.key, b.key), strings.Compare(a.side, b.side)) })
	lines := make([]string, 0, len(ds))
	for _, d := range ds {
		lines = append(lines, d.side+" "+d.line)
	}
	return lines
}

func newVenvCmd(v *viper.Viper) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "venv --constraints FILE --freeze FILE",
		Short: "Check that a venv holds the packages its constraints name",
		Args:  cobra.NoArgs,
		PreRunE: func(cmd *cobra.Command, _ []string) error {
			if err := bindFlags(v, cmd, "constraints", "freeze"); err != nil {
				return err
			}
			return require(v, "constraints", "freeze")
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			return checkVenv(cmd, v.GetString("constraints"), v.GetString("freeze"))
		},
	}
	f := cmd.Flags()
	f.String("constraints", "", "the constraints file, the freeze of the venv the ledgers were recorded under (PARITY_CONSTRAINTS)")
	f.String("freeze", "", "the .freeze file of the venv to check (PARITY_FREEZE)")
	return cmd
}

// checkVenv fails, listing the difference, unless the freeze file holds the
// packages the constraints file names and no others, and warns when the
// Python that built the venv is another.
func checkVenv(cmd *cobra.Command, constraintsFile, freezeFile string) error {
	var fs [2]freeze
	for i, file := range []string{constraintsFile, freezeFile} {
		data, err := readFile(file)
		if err != nil {
			return fmt.Errorf("reading the freeze: %w", err)
		}
		if fs[i], err = parseFreeze(data, file); err != nil {
			return err
		}
	}
	constraints, venv := fs[0], fs[1]
	if constraints.python != venv.python {
		fmt.Fprintln(cmd.ErrOrStderr(), SafeText(fmt.Sprintf("parity: warning: the venv was built by %s, %s names %s: "+
			"this run's pytest_freeze will differ from the ledgers'", venv.python, constraintsFile, constraints.python)))
	}
	if lines := packageDrift(constraints, venv); len(lines) > 0 {
		return failed(fmt.Errorf("the venv holds other packages than %s names (< constraints, > venv):\n%s", constraintsFile, strings.Join(lines, "\n")))
	}
	return nil
}
