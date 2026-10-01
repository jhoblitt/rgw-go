package parity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
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

// failed marks err as exit status 1: diff found differences, or record could
// not record.
func failed(err error) error {
	return &exitError{status: 1, err: err}
}

// ExitStatus returns the exit status for an error Run returned: 0 for none,
// 1 when diff found differences or record could not record, and 2 for
// anything else, a refused command line or results diff cannot compare.
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
// error, which ExitStatus maps to the exit status.
func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	// cobra reads os.Args[1:] in place of nil args.
	if args == nil {
		args = []string{}
	}
	cmd := newRootCmd(stdin, stdout, stderr)
	cmd.SetArgs(args)
	return cmd.ExecuteContext(ctx)
}

func newRootCmd(stdin io.Reader, stdout, stderr io.Writer) *cobra.Command {
	v := viper.New()
	cmd := &cobra.Command{
		Use:           "parity",
		Short:         "Record test outcomes and compare them with a baseline's",
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
	cmd.AddCommand(newRecordCmd(v), newDiffCmd(v))
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
	cmd := &cobra.Command{
		Use:   "record --format junit|gotest --suite NAME --out FILE [--meta k=v]... RUN-FILE [RUN-FILE...]",
		Short: "Record a result file from run files",
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) == 0 {
				return errors.New("no run file given")
			}
			return nil
		},
		PreRunE: func(cmd *cobra.Command, _ []string) error {
			if err := bindFlags(v, cmd, "format", "suite", "out"); err != nil {
				return err
			}
			if f := v.GetString("format"); f != string(FormatJunit) && f != string(FormatGoTest) {
				return fmt.Errorf("--format must be junit or gotest, not %q", f)
			}
			if err := require(v, "suite", "out"); err != nil {
				return err
			}
			pairs, err := cmd.Flags().GetStringArray("meta")
			if err != nil {
				return err
			}
			meta, err = parseMeta(pairs)
			return err
		},
		RunE: func(cmd *cobra.Command, files []string) error {
			out := v.GetString("out")
			res, err := Record(Format(v.GetString("format")), v.GetString("suite"), meta, files)
			if err != nil {
				return failed(err)
			}
			res.Meta[metaRecorded] = time.Now().UTC().Format(time.RFC3339)
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
	// --meta is never bound into viper, so neither a stray PARITY_META nor a
	// config file's meta list can add a pair to a recorded result.
	f.StringArray("meta", nil, "a k=v pair to record with the result; repeatable, command line only")
	return cmd
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
		Use:   "diff --baseline FILE --candidate FILE [--known FILE] [--allow-meta-drift]",
		Short: "Compare a candidate result with a baseline",
		Args:  cobra.NoArgs,
		PreRunE: func(cmd *cobra.Command, _ []string) error {
			if err := bindFlags(v, cmd, "baseline", "candidate", "known", "allow-meta-drift"); err != nil {
				return err
			}
			return require(v, "baseline", "candidate")
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			baseline, candidate := v.GetString("baseline"), v.GetString("candidate")
			base, err := readResult(baseline)
			if err != nil {
				return err
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
			diffs, err := Diff(base, cand, v.GetBool("allow-meta-drift"), known)
			if err != nil {
				return err
			}
			for _, d := range diffs {
				fmt.Fprintln(cmd.OutOrStdout(), d)
			}
			if len(diffs) > 0 {
				return failed(fmt.Errorf("%d differences between %s and %s", len(diffs), baseline, candidate))
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.String("baseline", "", "the baseline result file (PARITY_BASELINE)")
	f.String("candidate", "", "the candidate result file (PARITY_CANDIDATE)")
	f.String("known", "", "a file of regular expressions over test ids whose outcomes are expected to differ (PARITY_KNOWN)")
	f.Bool("allow-meta-drift", false, "compare even when the results' meta values differ (PARITY_ALLOW_META_DRIFT)")
	return cmd
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
	if err := json.Unmarshal(data, &res); err != nil {
		return Result{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	if err := res.validate(); err != nil {
		return Result{}, fmt.Errorf("%s: %w", path, err)
	}
	return res, nil
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
