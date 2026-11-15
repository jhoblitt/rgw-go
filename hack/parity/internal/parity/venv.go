package parity

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// The modes of venv. A record run is of the gateway whose outcomes a ledger
// records, and a record may move the venv, which the record decides. A
// compare run is of the gateway diff compares with the ledger, and must run
// under the venv the ledger's newest entries ran under.
const (
	venvRecord  = "record"
	venvCompare = "compare"
)

// A freeze is the text hack/s3tests/run.sh leaves beside a run's report, its
// .freeze file, and keeps in constraints.txt: the Python that built the venv
// as "# Python X.Y.Z", then each package at its version, name==version a
// line, as pip freeze lists them.
type freeze struct {
	// file names the freeze in messages.
	file string
	// python is the first line, without its "# ", and minor its major.minor.
	python, minor string
	// packages are the other lines.
	packages []string
	// digest is the first 12 hex digits of the sha256 of the whole text,
	// which a record carries as its pytest_freeze, and packagesDigest the
	// same of the package lines alone, which a candidate's record carries as
	// its pytest_packages.
	digest, packagesDigest string
}

var rePythonLine = regexp.MustCompile(`^# (Python (\d+\.\d+)[0-9A-Za-z.+-]*)$`)

// freezeDigest returns the pytest_freeze of the freeze whose text is data.
func freezeDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])[:12]
}

// parseFreeze reads a freeze's bytes; file names them in errors.
func parseFreeze(data []byte, file string) (freeze, error) {
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	m := rePythonLine.FindStringSubmatch(lines[0])
	if m == nil {
		return freeze{}, fmt.Errorf("%s does not start with a line \"# Python X.Y.Z\", as a venv's freeze does", file)
	}
	f := freeze{file: file, python: m[1], minor: m[2], digest: freezeDigest(data)}
	var text strings.Builder
	for _, l := range lines[1:] {
		if l != "" {
			f.packages = append(f.packages, l)
			text.WriteString(l + "\n")
		}
	}
	f.packagesDigest = freezeDigest([]byte(text.String()))
	return f, nil
}

// Constraints is the freeze of the venv the ledgers were last recorded under,
// hack/s3tests/constraints.txt, which a run's venv is built under.
type Constraints struct{ f freeze }

// ParseConstraints reads the constraints file whose bytes are data; file names
// it in errors and messages.
func ParseConstraints(data []byte, file string) (Constraints, error) {
	f, err := parseFreeze(data, file)
	return Constraints{f: f}, err
}

// runVenv is what a comparison knows of the venv a run ran under.
type runVenv struct {
	// digest is its pytest_freeze, and packages the digest of its package
	// lines alone.
	digest, packages string
	// python is the Python as far as the run names it, "Python X.Y.Z" for a
	// venv checked from its freeze and "Python X.Y (patch not recorded)" for
	// a candidate, whose record keeps the minor version alone, and minor is
	// its X.Y. Both are empty for a candidate whose record names none.
	python, minor string
}

// venvOf returns the venv whose freeze is f.
func venvOf(f freeze) runVenv {
	return runVenv{digest: f.digest, packages: f.packagesDigest, python: f.python, minor: f.minor}
}

// candidateVenv returns the venv a candidate's record names.
func candidateVenv(meta map[string]string) runVenv {
	v := runVenv{digest: meta[metaFreeze], packages: meta[metaPackages], minor: meta[metaPython]}
	if v.minor != "" {
		v.python = "Python " + v.minor + " (patch not recorded)"
	}
	return v
}

// builtBy says which Python built a venv and which the constraints c name.
func builtBy(python string, c freeze) string {
	return fmt.Sprintf("the venv was built by %s, %s names %s", python, c.file, c.python)
}

// freezeDrift judges the venv got against want, the pytest_freeze of the
// ledger's newest entries. It returns the line that says a run under got
// cannot be compared with the ledger, or "" when it can: got's whole freeze is
// want, or allow, diff's --allow-meta-drift, lets another through. A client
// release changes what the tests send, and so their outcomes, unseen, so the
// comparison holds the venv as it holds the s3-tests commit.
//
// A venv whose whole freeze is another is also accepted, with a note, when c is
// given, is the freeze the ledger's newest entries were recorded under, and
// got's package lines are c's and its Python has c's minor version: the runner
// image's python3 moves under its label, and a patch release must not fail
// every pull request until a full record, while a new minor version still
// does. Without c the whole freeze alone decides.
//
// Diff and venv both ask it, so that a run venv refuses at its start is one diff
// would refuse at its end.
func freezeDrift(want string, got runVenv, c *Constraints, allow bool) (drift, note string) {
	if got.digest == want || allow {
		return "", ""
	}
	drift = fmt.Sprintf("%s: baseline %s, candidate %s", metaFreeze, want, got.digest)
	if c == nil {
		return drift, ""
	}
	var why []string
	if c.f.digest != want {
		why = append(why, fmt.Sprintf("%s's %s %s is not the baseline's", c.f.file, metaFreeze, c.f.digest))
	}
	if got.packages != c.f.packagesDigest {
		why = append(why, fmt.Sprintf("%s: constraints %s, candidate %s", metaPackages, c.f.packagesDigest, cmp.Or(got.packages, "missing")))
	}
	switch {
	case got.minor == "":
		why = append(why, fmt.Sprintf("%s: constraints %s, candidate missing", metaPython, c.f.minor))
	case got.minor != c.f.minor:
		why = append(why, builtBy(got.python, c.f))
	}
	if len(why) > 0 {
		return drift + "; " + strings.Join(why, "; "), ""
	}
	return "", fmt.Sprintf("%s: baseline %s, candidate %s, accepted: the packages are %s's and the Python has its minor version: %s",
		metaFreeze, want, got.digest, c.f.file, builtBy(got.python, c.f))
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

// venvCheck is what the venv command checks: the constraints file and the
// .freeze file of the venv, in a mode, and for a compare run the ledger diff
// will compare it with.
type venvCheck struct {
	constraints, freeze string
	mode, baseline      string
	allowDrift          bool
}

func newVenvCmd(v *viper.Viper) *cobra.Command {
	var c venvCheck
	cmd := &cobra.Command{
		Use: "venv --constraints FILE --freeze FILE [--mode record|compare [--baseline LEDGER]] [--allow-meta-drift]",
		Short: "Check that a venv holds the packages its constraints name, and for a compare run " +
			"the venv the ledger was recorded under",
		Args: cobra.NoArgs,
		PreRunE: func(cmd *cobra.Command, _ []string) error {
			if err := bindFlags(v, cmd, "constraints", "freeze", "mode", "baseline", "allow-meta-drift"); err != nil {
				return err
			}
			if err := require(v, "constraints", "freeze"); err != nil {
				return err
			}
			c = venvCheck{
				constraints: v.GetString("constraints"), freeze: v.GetString("freeze"),
				mode: v.GetString("mode"), baseline: v.GetString("baseline"), allowDrift: v.GetBool("allow-meta-drift"),
			}
			switch {
			case c.mode != venvRecord && c.mode != venvCompare:
				return fmt.Errorf("--mode must be %s or %s, not %q", venvRecord, venvCompare, c.mode)
			case c.mode == venvCompare && c.baseline == "":
				return fmt.Errorf("--baseline is required with --mode %s", venvCompare)
			case c.mode == venvRecord && c.baseline != "":
				return fmt.Errorf("--baseline is for --mode %s", venvCompare)
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error { return checkVenv(cmd, c) },
	}
	f := cmd.Flags()
	f.String("constraints", "", "the constraints file, the freeze of the venv the ledgers were recorded under (PARITY_CONSTRAINTS)")
	f.String("freeze", "", "the .freeze file of the venv to check (PARITY_FREEZE)")
	f.String("mode", venvRecord, "record, a run a ledger will record, which may move the venv, or compare, a run diff will "+
		"compare with --baseline (PARITY_MODE)")
	f.String("baseline", "", "with --mode compare, the ledger diff will compare the run with (PARITY_BASELINE)")
	f.Bool("allow-meta-drift", false, "with --mode compare, accept a venv other than the one the ledger was recorded under (PARITY_ALLOW_META_DRIFT)")
	return cmd
}

// checkVenv fails, listing the difference, unless the venv holds the packages
// the constraints name and no others. Another Python is a move of the venv.
// A record run only warns of it, since the record decides the move. A compare
// run is refused, as diff would refuse it, unless the ledger's newest entries
// ran under this venv or freezeDrift accepts the Python.
func checkVenv(cmd *cobra.Command, c venvCheck) error {
	var fs [2]freeze
	for i, file := range []string{c.constraints, c.freeze} {
		data, err := readFile(file)
		if err != nil {
			return fmt.Errorf("reading the freeze: %w", err)
		}
		if fs[i], err = parseFreeze(data, file); err != nil {
			return err
		}
	}
	constraints, venv := fs[0], fs[1]
	if c.mode == venvRecord && constraints.python != venv.python {
		fmt.Fprintln(cmd.ErrOrStderr(), SafeText("parity: warning: "+builtBy(venv.python, constraints)+": this run's pytest_freeze will differ from the ledgers'"))
	}
	if lines := packageDrift(constraints, venv); len(lines) > 0 {
		return failed(fmt.Errorf("the venv holds other packages than %s names (< constraints, > venv):\n%s", c.constraints, strings.Join(lines, "\n")))
	}
	if c.mode != venvCompare {
		return nil
	}
	l, err := readLedger(c.baseline)
	if err != nil {
		return err
	}
	head, err := l.headFreeze()
	if err != nil {
		return err
	}
	if head == "" {
		return nil
	}
	drift, note := freezeDrift(head, venvOf(venv), &Constraints{f: constraints}, c.allowDrift)
	if note != "" {
		fmt.Fprintln(cmd.ErrOrStderr(), SafeText("parity: note: "+note))
	}
	if drift == "" {
		return nil
	}
	return failed(fmt.Errorf("diff would refuse this run against %s before it compared any outcome: %s", c.baseline, drift))
}
