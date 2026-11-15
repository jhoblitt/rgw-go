package parity

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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
	// python is the first line, without its "# ".
	python string
	// packages are the other lines.
	packages []string
	// digest is the first 12 hex digits of the sha256 of the whole text,
	// which a record carries as its pytest_freeze.
	digest string
}

var rePythonLine = regexp.MustCompile(`^# (Python \d[0-9A-Za-z.+-]*)$`)

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
	f := freeze{python: m[1], digest: freezeDigest(data)}
	for _, l := range lines[1:] {
		if l != "" {
			f.packages = append(f.packages, l)
		}
	}
	return f, nil
}

// freezeDrift returns the line that says a run under the venv whose
// pytest_freeze is got cannot be compared with a ledger whose newest entries
// ran under want, or "" when it can: got is want, or allow, diff's
// --allow-meta-drift, lets another through. A client release changes what the
// tests send, and so their outcomes, unseen, so the comparison holds the venv
// as it holds the s3-tests commit. Diff and venv both ask it, so that a run
// venv refuses at its start is one diff would refuse at its end.
func freezeDrift(want, got string, allow bool) string {
	if got == want || allow {
		return ""
	}
	return fmt.Sprintf("%s: baseline %s, candidate %s", metaFreeze, want, got)
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
// the constraints name and no others. Another Python is a venv move, which a
// record run only warns of, and a compare run is refused, as diff would
// refuse it, unless the ledger's newest entries ran under this venv.
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
	pythonNote := ""
	if constraints.python != venv.python {
		pythonNote = fmt.Sprintf("the venv was built by %s, %s names %s", venv.python, c.constraints, constraints.python)
	}
	if pythonNote != "" && c.mode == venvRecord {
		fmt.Fprintln(cmd.ErrOrStderr(), SafeText("parity: warning: "+pythonNote+": this run's pytest_freeze will differ from the ledgers'"))
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
	line := freezeDrift(head, venv.digest, c.allowDrift)
	if line == "" {
		return nil
	}
	msg := fmt.Sprintf("diff would refuse this run against %s before it compared any outcome: %s", c.baseline, line)
	if pythonNote != "" {
		msg += "; " + pythonNote
	}
	return failed(errors.New(msg))
}
