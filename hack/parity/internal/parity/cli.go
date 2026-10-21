package parity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/jhoblitt/rgw-go/internal/version"
)

// metaRecorded is the meta key the record command sets to the time it
// recorded the result.
const metaRecorded = "recorded"

// exitError carries the exit status for an error other than 2's.
type exitError struct {
	status int
	err    error
}

func (e *exitError) Error() string { return e.err.Error() }

func (e *exitError) Unwrap() error { return e.err }

// failed marks err as exit status 1: diff found differences, check or
// validate found problems, or a command could not do its work.
func failed(err error) error {
	return &exitError{status: 1, err: err}
}

// ExitStatus returns the exit status for an error Run returned: 0 for none,
// 1 when diff found differences, check or validate found problems, or a
// command could not do its work, and 2 for anything else, a refused command
// line or files a command cannot read or compare.
func ExitStatus(err error) int {
	if err == nil {
		return 0
	}
	if e, ok := errors.AsType[*exitError](err); ok {
		return e.status
	}
	return 2
}

// Run executes the parity command tree against args and returns the first
// error, which ExitStatus maps to the exit status. Everything it prints to
// stdout passes through SafeLine, since much of it is text a ledger holds,
// which a pull request writes and a CI job's log shows.
func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	// cobra reads os.Args[1:] in place of nil args.
	if args == nil {
		args = []string{}
	}
	out := &lineGuard{w: stdout}
	cmd := newRootCmd(stdin, out, stderr)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(ctx)
	if ferr := out.flush(); ferr != nil && err == nil {
		err = failed(ferr)
	}
	return err
}

func newRootCmd(stdin io.Reader, stdout, stderr io.Writer) *cobra.Command {
	v := viper.New()
	cmd := &cobra.Command{
		Use:           "parity",
		Short:         "Record test outcomes into ledgers and compare them with a ledger's",
		Version:       version.String(),
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			return configure(cmd, v, stderr)
		},
	}
	cmd.SetIn(stdin)
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)

	pf := cmd.PersistentFlags()
	pf.String("config", "", "config file to read after flags and environment")
	pf.String("log-level", "info", "log level: debug, info, warn, or error (PARITY_LOG_LEVEL)")
	pf.String("log-format", "json", "log format: json or text (PARITY_LOG_FORMAT)")
	cmd.AddCommand(newRecordCmd(v), newDiffCmd(v), newCheckCmd(v), newValidateCmd(), newPinCmd(v), newMigrateCmd(), newFmtCmd())
	return cmd
}

// configure binds the root's flags and the environment into v, reads the
// config file when one is named, and installs the default logger. It runs
// before every command; each command binds its own flags by name in its
// PreRunE, so that one it leaves out is read from the command line only.
func configure(cmd *cobra.Command, v *viper.Viper, stderr io.Writer) error {
	v.SetEnvPrefix("PARITY")
	v.SetEnvKeyReplacer(strings.NewReplacer("-", "_", ".", "_"))
	v.AutomaticEnv()
	if err := v.BindPFlags(cmd.Root().PersistentFlags()); err != nil {
		return fmt.Errorf("binding flags: %w", err)
	}
	if cfg := v.GetString("config"); cfg != "" {
		v.SetConfigFile(cfg)
		if err := v.ReadInConfig(); err != nil {
			return fmt.Errorf("reading config %s: %w", cfg, err)
		}
	}
	return setupLogging(v, stderr)
}

// bindFlags binds the named flags of cmd into v, so that each can also come
// from the environment or the config file.
func bindFlags(v *viper.Viper, cmd *cobra.Command, names ...string) error {
	for _, name := range names {
		if err := v.BindPFlag(name, cmd.Flags().Lookup(name)); err != nil {
			return fmt.Errorf("binding --%s: %w", name, err)
		}
	}
	return nil
}

func setupLogging(v *viper.Viper, w io.Writer) error {
	var level slog.Level
	if err := level.UnmarshalText([]byte(v.GetString("log-level"))); err != nil {
		return fmt.Errorf("parsing log level: %w", err)
	}
	opts := &slog.HandlerOptions{Level: level}
	var handler slog.Handler
	switch format := v.GetString("log-format"); format {
	case "json":
		handler = slog.NewJSONHandler(w, opts)
	case "text":
		handler = slog.NewTextHandler(w, opts)
	default:
		return fmt.Errorf("unknown log format %q", format)
	}
	slog.SetDefault(slog.New(handler))
	return nil
}

func newRecordCmd(v *viper.Viper) *cobra.Command {
	var meta map[string]string
	var draft, pinReason string
	cmd := &cobra.Command{
		Use: "record --format junit|gotest --suite NAME (--out FILE | --ledger FILE [--subset] [--reasons FILE] " +
			"[--draft NOTE] [--pin-reason REASON]) [--pin-file FILE] [--meta k=v]... RUN-FILE [RUN-FILE...]",
		Short: "Record run files into a result file, or append them to a ledger",
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) == 0 {
				return errors.New("no run file given")
			}
			return nil
		},
		PreRunE: func(cmd *cobra.Command, files []string) error {
			if err := bindFlags(v, cmd, "format", "suite", "out", "ledger", "subset", "reasons", "pin-file"); err != nil {
				return err
			}
			if f := v.GetString("format"); f != string(FormatJunit) && f != string(FormatGoTest) {
				return fmt.Errorf("--format must be junit or gotest, not %q", f)
			}
			if err := require(v, "suite"); err != nil {
				return err
			}
			var err error
			if draft, err = cmd.Flags().GetString("draft"); err != nil {
				return err
			}
			if pinReason, err = cmd.Flags().GetString("pin-reason"); err != nil {
				return err
			}
			switch out, ledger := v.GetString("out"), v.GetString("ledger"); {
			case out == "" && ledger == "":
				return errors.New("--out or --ledger is required")
			case out != "" && ledger != "":
				return errors.New("--out and --ledger exclude each other")
			case out != "" && (v.GetBool("subset") || v.GetString("reasons") != "" || draft != "" || pinReason != ""):
				return errors.New("--subset, --reasons, --draft and --pin-reason are for --ledger")
			case ledger != "" && len(files) < minRuns:
				return fmt.Errorf("--ledger takes at least %d run files, so that a flaky test shows as unstable", minRuns)
			}
			pairs, err := cmd.Flags().GetStringArray("meta")
			if err != nil {
				return err
			}
			meta, err = parseMeta(pairs)
			return err
		},
		RunE: func(cmd *cobra.Command, files []string) error {
			res, err := Record(Format(v.GetString("format")), v.GetString("suite"), meta, files)
			if err != nil {
				return failed(err)
			}
			res.Meta[metaRecorded] = time.Now().UTC().Format(time.RFC3339)
			if file := v.GetString("pin-file"); file != "" {
				if perr := atPin(res, file); perr != nil {
					return failed(perr)
				}
			}
			if path := v.GetString("ledger"); path != "" {
				opts := RecordOptions{Subset: v.GetBool("subset"), Draft: draft, PinReason: pinReason}
				return failedIf(appendToLedger(cmd.OutOrStdout(), path, v.GetString("reasons"), res, len(files), opts))
			}
			out := v.GetString("out")
			data, err := json.MarshalIndent(res, "", "  ")
			if err != nil {
				return failed(fmt.Errorf("encoding the result: %w", err))
			}
			if err := os.WriteFile(out, append(data, '\n'), 0o600); err != nil {
				return failed(err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "recorded %d tests from %d runs, %d unstable, to %s\n",
				len(res.Outcomes), len(files), len(res.Unstable), out)
			return nil
		},
	}
	f := cmd.Flags()
	f.String("format", "", "the run files' format: junit or gotest (PARITY_FORMAT)")
	f.String("suite", "", "the suite the runs are of, such as s3tests (PARITY_SUITE)")
	f.String("out", "", "the result file to write (PARITY_OUT)")
	f.String("ledger", "", "the ledger to append to; a full record creates a missing one (PARITY_LEDGER)")
	f.Bool("subset", false, "the runs ran chosen tests, so leave the ledger's others alone rather than retire them (PARITY_SUBSET)")
	f.String("reasons", "", "a file of test ids, each followed by the reason for its appended entry (PARITY_REASONS)")
	f.String("pin-file", "", "the s3-tests PIN file the record's s3tests_commit must be, so that no run at another "+
		"commit, as S3TESTS_COMMIT in the environment sets, is recorded (PARITY_PIN_FILE)")
	// --meta, --draft and --pin-reason are never bound into viper, so neither
	// a stray PARITY_ variable nor a config file can add a pair to a recorded
	// result, turn a record into a draft or give the reason a full record at
	// a new source needs.
	f.String("draft", "", "make the ledger a draft: an appended entry that needs a reason --reasons does not give, "+
		"and a base pin --pin-reason gives none, gets \""+DraftMarker+" NOTE\", which diff accepts and validate and "+
		"check refuse; command line only")
	f.String("pin-reason", "", "why a full record at a source the ledger does not pin re-stamps every outcome there, "+
		"rather than a proven pin joining the chain; command line only")
	f.StringArray("meta", nil, "a k=v pair to record with the result; repeatable, command line only")
	return cmd
}

// atPin returns an error unless res, an s3-tests record, ran at the commit
// the PIN file at file holds.
func atPin(res Result, file string) error {
	commit, ok := res.Meta["s3tests_commit"]
	if !ok {
		return errors.New("--pin-file is for an s3-tests record, whose meta carries s3tests_commit")
	}
	pin, err := readPinFile(file)
	if err != nil {
		return err
	}
	if commit != pin {
		return fmt.Errorf("the record ran s3-tests at %s, not at %s's pin %s: a run at another commit, as "+
			"S3TESTS_COMMIT in the environment sets, is never recorded", commit, file, pin)
	}
	return nil
}

// failedIf marks a non-nil err as exit status 1.
func failedIf(err error) error {
	if err == nil {
		return nil
	}
	return failed(err)
}

// appendToLedger appends res, recorded from runs run files, to the ledger at
// path, starting one when there is none, with the reasons the file at
// reasons gives, and prints what it appended to w.
func appendToLedger(w io.Writer, path, reasons string, res Result, runs int, opts RecordOptions) error {
	var l *Ledger
	if _, err := os.Stat(path); err == nil {
		if l, err = readLedger(path); err != nil {
			return err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("reading the ledger: %w", err)
	}
	if reasons != "" {
		data, err := readFile(reasons)
		if err != nil {
			return fmt.Errorf("reading the reasons: %w", err)
		}
		if opts.Reasons, err = ParseReasons(bytes.NewReader(data)); err != nil {
			return fmt.Errorf("%s: %w", reasons, err)
		}
	}
	pins := 0
	if l != nil {
		pins = len(l.Pins)
	}
	out, appended, err := AppendRecord(l, res, opts)
	if err != nil {
		return err
	}
	if err := writeLedger(path, out); err != nil {
		return err
	}
	if len(out.Pins) > pins {
		p := out.Pins[len(out.Pins)-1]
		line := fmt.Sprintf("pinned %s, admitted %s", p.Source, p.Admitted)
		if p.Reason != "" {
			line += ": " + p.Reason
		}
		fmt.Fprintln(w, SafeLine(line))
	}
	var reasoned []string
	for i := range appended {
		if a := &appended[i]; a.Reasoned {
			reasoned = append(reasoned, fmt.Sprintf("%s: %s: %s", a.ID, a.Change, a.Entry.Reason))
		}
	}
	fmt.Fprintf(w, "appended %d entries, %d needing a reason, for the %d tests of %d runs, %d unstable, to %s\n",
		len(appended), len(reasoned), len(res.Outcomes), runs, len(res.Unstable), path)
	for _, line := range reasoned {
		fmt.Fprintln(w, SafeLine(line))
	}
	return nil
}

// parseMeta parses record's k=v pairs, refusing the keys record sets itself.
func parseMeta(pairs []string) (map[string]string, error) {
	meta := map[string]string{}
	for _, kv := range pairs {
		k, val, ok := strings.Cut(kv, "=")
		switch {
		case !ok || k == "" || val == "":
			return nil, fmt.Errorf("--meta %q is not k=v", kv)
		case k == metaRuns || k == metaRecorded:
			return nil, fmt.Errorf("--meta %s: record sets %s itself", kv, k)
		}
		if _, dup := meta[k]; dup {
			return nil, fmt.Errorf("--meta %s given twice", k)
		}
		meta[k] = val
	}
	return meta, nil
}

func newDiffCmd(v *viper.Viper) *cobra.Command {
	cmd := &cobra.Command{
		Use: "diff --baseline LEDGER --candidate FILE [--known FILE] [--selection FILE [--gate-only FILE]] " +
			"[--summary FILE] [--allow-meta-drift]",
		Short: "Compare a candidate result with a ledger",
		Args:  cobra.NoArgs,
		PreRunE: func(cmd *cobra.Command, _ []string) error {
			if err := bindFlags(v, cmd, "baseline", "candidate", "known", "allow-meta-drift", "summary"); err != nil {
				return err
			}
			return require(v, "baseline", "candidate")
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			baseline, candidate := v.GetString("baseline"), v.GetString("candidate")
			base, err := readLedger(baseline)
			if err != nil {
				return err
			}
			if problems := ValidateDraft(base); len(problems) > 0 {
				return fmt.Errorf("%s is not a valid ledger:\n%s", baseline, strings.Join(problems, "\n"))
			}
			cand, err := readResult(candidate)
			if err != nil {
				return err
			}
			var known KnownList
			if path := v.GetString("known"); path != "" {
				if known, err = readKnown(path); err != nil {
					return err
				}
			}
			sel, err := readSelectionFlags(cmd)
			if err != nil {
				return err
			}
			diffs, err := Diff(base, cand, v.GetBool("allow-meta-drift"), known, sel)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			for _, d := range diffs {
				fmt.Fprintln(out, SafeLine(d))
			}
			dropped := Dropped(base, cand, sel)
			if len(dropped) > 0 {
				fmt.Fprintln(out, droppedHeading(len(dropped)))
				for _, id := range dropped {
					fmt.Fprintln(out, SafeLine("  "+droppedLine(base, id)))
				}
			}
			if err := appendSummary(v.GetString("summary"), droppedSummary(base, dropped)); err != nil {
				return err
			}
			if len(diffs) > 0 {
				summary := fmt.Sprintf("%d differences between %s and %s", len(diffs), baseline, candidate)
				if len(dropped) > 0 {
					summary += fmt.Sprintf(", and %d ledger tests dropped from the pr set", len(dropped))
				}
				return failed(errors.New(summary))
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.String("baseline", "", "the baseline ledger (PARITY_BASELINE)")
	f.String("candidate", "", "the candidate result file (PARITY_CANDIDATE)")
	f.String("known", "", "a file of regular expressions over test ids whose outcomes are expected to differ, or after \"~ \" may differ (PARITY_KNOWN)")
	f.Bool("allow-meta-drift", false, "compare even when the candidate ran at another source, release or Ceph version (PARITY_ALLOW_META_DRIFT)")
	// --selection and --gate-only are never bound into viper, so that no
	// stray PARITY_ variable or config file narrows a comparison that names
	// neither.
	f.String("selection", "", "a file of the pytest node ids the candidate's run selected, one a line; a ledger "+
		"test it does not name, which the candidate lacks, is not compared; command line only")
	f.String("gate-only", "", "a file of the pytest node ids of the gate-only tests the --selection run left out, "+
		"one a line, which are not listed as dropped; command line only")
	f.String("summary", "", "a file to append a markdown list of the dropped tests to, when there are any, "+
		"such as a CI job's summary (PARITY_SUMMARY)")
	return cmd
}

// readSelectionFlags returns the selection diff's --selection and
// --gate-only files name, or the zero Selection for neither.
func readSelectionFlags(cmd *cobra.Command) (Selection, error) {
	path, err := cmd.Flags().GetString("selection")
	if err != nil {
		return Selection{}, err
	}
	gateOnly, err := cmd.Flags().GetString("gate-only")
	if err != nil {
		return Selection{}, err
	}
	if path == "" {
		if gateOnly != "" {
			return Selection{}, errors.New("--gate-only is for --selection")
		}
		return Selection{}, nil
	}
	sel, err := readSelection(path)
	if err != nil || gateOnly == "" {
		return sel, err
	}
	data, err := readFile(gateOnly)
	if err != nil {
		return Selection{}, fmt.Errorf("reading the gate-only tests: %w", err)
	}
	if sel, err = sel.WithGateOnly(bytes.NewReader(data)); err != nil {
		return Selection{}, fmt.Errorf("%s: %w", gateOnly, err)
	}
	return sel, nil
}

func newCheckCmd(v *viper.Viper) *cobra.Command {
	var draft bool
	cmd := &cobra.Command{
		Use: "check --old LEDGER --new LEDGER [--list-reasoned] [--draft]\n" +
			"  check --base COMMIT [--head COMMIT] [--repo DIR] [--run-sh FILE] [--pin-file FILE] [--clone DIR | --ceph-repo URL] [--summary FILE] [PATH...]",
		Short: "Check that ledgers only append to older versions of themselves",
		Args:  cobra.ArbitraryArgs,
		PreRunE: func(cmd *cobra.Command, paths []string) error {
			if err := bindFlags(v, cmd, "old", "new", "list-reasoned", "base", "head", "repo", "run-sh", "pin-file", "clone",
				"ceph-repo", "summary"); err != nil {
				return err
			}
			var err error
			if draft, err = cmd.Flags().GetBool("draft"); err != nil {
				return err
			}
			if v.GetString("head") != "" && v.GetString("base") == "" {
				return errors.New("--head is for --base")
			}
			if v.GetString("base") != "" {
				if v.GetString("old") != "" || v.GetString("new") != "" || v.GetBool("list-reasoned") || draft {
					return errors.New("--base excludes --old, --new, --list-reasoned and --draft")
				}
				return nil
			}
			if len(paths) > 0 {
				return fmt.Errorf("unexpected arguments %q: only --base takes paths", paths)
			}
			return require(v, "old", "new")
		},
		RunE: func(cmd *cobra.Command, paths []string) error {
			if base := v.GetString("base"); base != "" {
				return checkBase(cmd, v, base, paths)
			}
			oldPath, newPath := v.GetString("old"), v.GetString("new")
			old, err := readLedger(oldPath)
			if err != nil {
				return err
			}
			next, err := readLedger(newPath)
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			problems := CheckAppend(old, next, draft)
			for _, p := range problems {
				fmt.Fprintln(w, SafeLine(p))
			}
			if len(problems) > 0 {
				return failed(fmt.Errorf("%s does not only append to %s: %d problems", newPath, oldPath, len(problems)))
			}
			if v.GetBool("list-reasoned") {
				for _, line := range slices.Concat(ReasonedPins(old, next), Reasoned(old, next)) {
					fmt.Fprintln(w, SafeLine(line))
				}
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.String("old", "", "the ledger as it was (PARITY_OLD)")
	f.String("new", "", "the ledger as it is (PARITY_NEW)")
	f.Bool("list-reasoned", false, "print each appended pin that carries a reason and each appended entry that "+
		"needed one, with it (PARITY_LIST_REASONED)")
	// --draft is never bound into viper, so that nothing but the command
	// line lets a draft pass.
	f.Bool("draft", false, "accept a draft, whose reasons start \""+DraftMarker+"\"; command line only")
	f.String("base", "", "the commit whose ledgers the working tree's must only append to (PARITY_BASE)")
	f.String("head", "", "with --base, the commit whose ledgers to judge, read from git rather than the working tree (PARITY_HEAD)")
	f.String("repo", ".", "the repository, whose working tree holds the ledgers (PARITY_REPO)")
	f.String("run-sh", defaultRunSh, "the run.sh, under --repo, an s3-tests pin's proof reads (PARITY_RUN_SH)")
	f.String("pin-file", defaultPinFile, "the PIN file, under --repo, that holds the s3-tests pin (PARITY_PIN_FILE)")
	f.String("clone", "", "a clone of Ceph that holds the pins to prove, rather than a fetch (PARITY_CLONE)")
	f.String("ceph-repo", "", "the Ceph repository to fetch the pins to prove from; run.sh's S3TESTS_REPO by default (PARITY_CEPH_REPO)")
	f.String("summary", "", "a file to append a markdown summary of the appended pins and reasoned entries to (PARITY_SUMMARY)")
	return cmd
}

// appendSummary appends the markdown summary to the file, creating it,
// unless either is empty.
func appendSummary(file, summary string) error {
	if file == "" || summary == "" {
		return nil
	}
	f, err := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // the summary file the command line names
	if err != nil {
		return fmt.Errorf("writing the summary: %w", err)
	}
	_, werr := f.WriteString(summary)
	if err := errors.Join(werr, f.Close()); err != nil {
		return fmt.Errorf("writing the summary: %w", err)
	}
	return nil
}

// checkBase runs check --base: it prints each problem and appends the
// summary to the --summary file.
func checkBase(cmd *cobra.Command, v *viper.Viper, base string, paths []string) error {
	rep, err := CheckBase(cmd.Context(), BaseCheck{
		Repo: v.GetString("repo"), Base: base, Head: v.GetString("head"), Paths: paths, RunSh: v.GetString("run-sh"),
		PinFile: v.GetString("pin-file"), Clone: v.GetString("clone"), CephRepo: v.GetString("ceph-repo"),
	})
	if err != nil {
		return err
	}
	if err := appendSummary(v.GetString("summary"), rep.Summary); err != nil {
		return err
	}
	for _, p := range rep.Problems {
		fmt.Fprintln(cmd.OutOrStdout(), SafeLine(p))
	}
	if len(rep.Problems) > 0 {
		return failed(fmt.Errorf("the ledgers do not only append to %s's: %d problems", base, len(rep.Problems)))
	}
	return nil
}

func newValidateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate LEDGER...",
		Short: "Check ledgers against the schema and the reason rule",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, paths []string) error {
			n := 0
			for _, path := range paths {
				l, err := readLedger(path)
				if err != nil {
					return err
				}
				for _, p := range Validate(l) {
					fmt.Fprintln(cmd.OutOrStdout(), SafeLine(path+": "+p))
					n++
				}
			}
			if n > 0 {
				return failed(fmt.Errorf("%d problems", n))
			}
			return nil
		},
	}
}

// Where the pin command finds run.sh and the s3-tests ledgers by default:
// the repository root, where make runs it.
var (
	defaultRunSh   = "hack/s3tests/run.sh"
	defaultPinFile = "hack/s3tests/PIN"
	defaultLedgers = []string{"test/s3tests/baseline/squid.json", "test/s3tests/baseline/tentacle.json"}
)

func newPinCmd(v *viper.Viper) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pin --clone CLONE --new COMMIT [--run-sh FILE] [--pin-file FILE] [--ceph-repo URL] [--ledger FILE]... [--tests FILE]",
		Short: "Move the s3-tests pin to a commit proven to keep the ledgers' outcomes",
		Args:  cobra.NoArgs,
		PreRunE: func(cmd *cobra.Command, _ []string) error {
			if err := bindFlags(v, cmd, "clone", "new", "run-sh", "pin-file", "ceph-repo", "tests"); err != nil {
				return err
			}
			return require(v, "clone", "new", "run-sh")
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			ledgers, err := cmd.Flags().GetStringArray("ledger")
			if err != nil {
				return err
			}
			res, err := Repin(cmd.Context(), RepinOptions{
				Clone: v.GetString("clone"), New: v.GetString("new"), RunSh: v.GetString("run-sh"), Ledgers: ledgers,
				PinFile: v.GetString("pin-file"), CephRepo: v.GetString("ceph-repo"),
			})
			if err != nil {
				return failed(err)
			}
			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "%s: %s over %s\n", res.Admitted, res.New, res.Old)
			for _, line := range res.Proof {
				fmt.Fprintln(w, SafeLine("  "+line))
			}
			fmt.Fprintf(w, "pinned %s, admitted %s, in %s and %s\n", res.New, res.Admitted, v.GetString("pin-file"), strings.Join(ledgers, ", "))
			if res.Admitted != AdmitAdditive {
				return nil
			}
			fmt.Fprintf(w, "%d tests to record:\n", len(res.Added))
			for _, id := range res.Added {
				fmt.Fprintln(w, SafeLine(id))
			}
			if tests := v.GetString("tests"); tests != "" {
				data := strings.Join(res.Added, "\n")
				if data != "" {
					data += "\n"
				}
				if err := os.WriteFile(tests, []byte(data), 0o600); err != nil {
					return failed(fmt.Errorf("writing the tests to record: %w", err))
				}
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.String("clone", "", "a git clone of Ceph that holds the pinned commit and the new one (PARITY_CLONE)")
	f.String("new", "", "the commit to pin, a full commit id (PARITY_NEW)")
	f.String("run-sh", defaultRunSh, "the run.sh that names the suite's directory, test files and repository (PARITY_RUN_SH)")
	f.String("pin-file", defaultPinFile, "the PIN file that holds the pin (PARITY_PIN_FILE)")
	f.String("ceph-repo", "", "the repository whose "+TrustedBranch+" branch the new commit must be on; run.sh's S3TESTS_REPO by default (PARITY_CEPH_REPO)")
	f.String("tests", "", "a file to write the tests an additive pin adds, one node id a line, for make s3tests-record-tests (PARITY_TESTS)")
	f.StringArray("ledger", defaultLedgers, "an s3-tests ledger to pin; repeatable, command line only")
	return cmd
}

func newMigrateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "migrate BASELINE...",
		Short: "Convert baselines in the flat form to ledgers, in place",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, paths []string) error {
			ledgers := make([]*Ledger, 0, len(paths))
			for _, path := range paths {
				l, err := migrateFile(path)
				if err != nil {
					return failed(err)
				}
				ledgers = append(ledgers, l)
			}
			for i, path := range paths {
				if err := writeLedger(path, ledgers[i]); err != nil {
					return failed(err)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "converted %s: %d tests, pinned at %s\n", path, len(ledgers[i].Tests), ledgers[i].head())
			}
			return nil
		},
	}
}

// migrateFile reads the flat baseline at path and converts it to a ledger.
func migrateFile(path string) (*Ledger, error) {
	data, err := readFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading the baseline: %w", err)
	}
	switch _, perr := decodeLedger(data); {
	case perr == nil:
		return nil, fmt.Errorf("%s is a ledger already", path)
	case !errors.Is(perr, ErrFlatBaseline):
		return nil, fmt.Errorf("%s: %w", path, perr)
	}
	var res Result
	if err = decodeStrict(data, &res); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	l, err := Migrate(res)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return l, nil
}

// require reports the first of keys that has no value.
func require(v *viper.Viper, keys ...string) error {
	for _, k := range keys {
		if v.GetString(k) == "" {
			return fmt.Errorf("--%s is required", k)
		}
	}
	return nil
}

func readResult(path string) (Result, error) {
	data, err := readFile(path)
	if err != nil {
		return Result{}, fmt.Errorf("reading the result file: %w", err)
	}
	var res Result
	if err := decodeStrict(data, &res); err != nil {
		return Result{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	if err := res.validate(); err != nil {
		return Result{}, fmt.Errorf("%s: %w", path, err)
	}
	return res, nil
}

// readLedger reads the ledger at path, and refuses a baseline in the flat
// form or a ledger not in the form parity writes, naming the command that
// converts or rewrites it.
func readLedger(path string) (*Ledger, error) {
	data, err := readFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading the ledger: %w", err)
	}
	l, err := ParseLedger(data)
	switch {
	case errors.Is(err, ErrFlatBaseline):
		return nil, fmt.Errorf("%s is %w: convert it with go run ./hack/parity migrate %s", path, err, path)
	case errors.Is(err, ErrNotCanonical):
		return nil, fmt.Errorf("%s is %w: go run ./hack/parity fmt %s rewrites it so", path, err, path)
	case err != nil:
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return l, nil
}

func newFmtCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "fmt LEDGER...",
		Short: "Rewrite ledgers in the form parity writes",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, paths []string) error {
			ledgers := make([]*Ledger, 0, len(paths))
			for _, path := range paths {
				data, err := readFile(path)
				if err != nil {
					return fmt.Errorf("reading the ledger: %w", err)
				}
				l, err := decodeLedger(data)
				if err != nil {
					return fmt.Errorf("%s: %w", path, err)
				}
				ledgers = append(ledgers, l)
			}
			for i, path := range paths {
				if err := writeLedger(path, ledgers[i]); err != nil {
					return failed(err)
				}
			}
			return nil
		},
	}
}

func readSelection(path string) (Selection, error) {
	data, err := readFile(path)
	if err != nil {
		return Selection{}, fmt.Errorf("reading the selection: %w", err)
	}
	sel, err := ParseSelection(bytes.NewReader(data))
	if err != nil {
		return Selection{}, fmt.Errorf("%s: %w", path, err)
	}
	return sel, nil
}

func readKnown(path string) (KnownList, error) {
	data, err := readFile(path)
	if err != nil {
		return KnownList{}, fmt.Errorf("reading the known-difference list: %w", err)
	}
	known, err := ParseKnown(bytes.NewReader(data))
	if err != nil {
		return KnownList{}, fmt.Errorf("%s: %w", path, err)
	}
	return known, nil
}
