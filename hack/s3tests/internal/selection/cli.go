package selection

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/jhoblitt/rgw-go/hack/s3tests/internal/clusterconf"
	"github.com/jhoblitt/rgw-go/internal/version"
)

// defaultLists is where the lists are when the command runs from the
// repository's root, as make runs it.
const defaultLists = "hack/s3tests"

// ExitStatus returns the exit status for an error Run returned: 0 for none,
// 1 when select refuses the set or expr the TESTS file, and 2 for anything
// else: a refused command line, or a list, a TESTS file or a collection file
// it cannot read.
func ExitStatus(err error) int {
	var refusals Refusals
	switch {
	case err == nil:
		return 0
	case errors.As(err, &refusals):
		return 1
	default:
		return 2
	}
}

// Run executes the s3tests command against args, reaching the cluster
// through the host, and returns the first error, which ExitStatus maps to
// the exit status.
func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	return RunWith(ctx, args, stdin, stdout, stderr, clusterconf.HostDeps())
}

// RunWith is Run with lc-mode reaching the cluster through deps.
func RunWith(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, deps clusterconf.Deps) error {
	// cobra reads os.Args[1:] in place of nil args.
	if args == nil {
		args = []string{}
	}
	cmd := newRootCmd(stdin, stdout, stderr, deps)
	cmd.SetArgs(args)
	return cmd.ExecuteContext(ctx)
}

func newRootCmd(stdin io.Reader, stdout, stderr io.Writer, deps clusterconf.Deps) *cobra.Command {
	v := viper.New()
	cmd := &cobra.Command{
		Use:           "s3tests",
		Short:         "Decide which s3-tests the parity set runs, and ready the cluster for its lifecycle tests",
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
	pf.String("log-level", "info", "log level: debug, info, warn, or error (S3TESTS_LOG_LEVEL)")
	pf.String("log-format", "json", "log format: json or text (S3TESTS_LOG_FORMAT)")
	pf.String("lists", defaultLists, "the directory of "+MarkersFile+", the deselect lists, the include lists and "+
		GateOnlyFile+" (S3TESTS_LISTS)")
	cmd.AddCommand(newExprCmd(v), newSelectCmd(v), newHashCmd(v), clusterconf.NewLCModeCmd(v, deps),
		clusterconf.NewClassesCmd(v, deps))
	return cmd
}

func newExprCmd(v *viper.Viper) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "expr [--lists DIR] [--set pr|gate] [--tests FILE]",
		Short: "Print the pytest -m expression that leaves out " + MarkersFile + "'s markers",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if _, err := readSet(v); err != nil {
				return err
			}
			in, err := readTestsInput(v)
			if err != nil {
				return err
			}
			if refusals := testsRefusals(in); refusals != nil {
				return refusals
			}
			markers, err := ReadList(filepath.Join(v.GetString("lists"), MarkersFile))
			if err != nil {
				return err
			}
			return write(cmd.OutOrStdout(), MarkerExpr(markers))
		},
	}
	cmd.Flags().String("tests", "", "a TESTS file to refuse now, as select would, when it is missing "+
		"or names no test (S3TESTS_TESTS)")
	cmd.Flags().String("set", string(SetGate), "a set to refuse now, as select would, when it is neither pr "+
		"nor gate (S3TESTS_SET)")
	return cmd
}

func newHashCmd(v *viper.Viper) *cobra.Command {
	return &cobra.Command{
		Use:   "hash [--lists DIR]",
		Short: "Print the digest of the deselect and include lists a recorded result carries",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			deselect, err := ReadDeselect(v.GetString("lists"))
			if err != nil {
				return err
			}
			include, err := ReadInclude(v.GetString("lists"))
			if err != nil {
				return err
			}
			return write(cmd.OutOrStdout(), Hash(deselect, include))
		},
	}
}

func newSelectCmd(v *viper.Viper) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "select [--lists DIR] [--set pr|gate] [--tests FILE] [--unfiltered FILE] <COLLECTION",
		Short: "Print the node ids of the set, from pytest --collect-only -q's output on stdin",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			set, err := readSet(v)
			if err != nil {
				return err
			}
			deselect, err := ReadDeselect(v.GetString("lists"))
			if err != nil {
				return err
			}
			include, err := ReadInclude(v.GetString("lists"))
			if err != nil {
				return err
			}
			gateOnly, err := ReadGateOnly(v.GetString("lists"))
			if err != nil {
				return err
			}
			in, err := readTestsInput(v)
			if err != nil {
				return err
			}
			in.Deselect, in.Include, in.GateOnly, in.Set = deselect, include, gateOnly, set
			if in.Unfiltered, err = readUnfiltered(v); err != nil {
				return err
			}
			collection, err := io.ReadAll(cmd.InOrStdin())
			if err != nil {
				return fmt.Errorf("reading the collection: %w", err)
			}
			in.Collected = strings.Split(string(collection), "\n")
			ids, gateOnlyIDs, err := SelectSets(in)
			if err != nil {
				return err
			}
			if path := v.GetString("gate-only-ids"); path != "" {
				if err := writeFile(path, gateOnlyIDs); err != nil {
					return err
				}
			}
			slog.InfoContext(cmd.Context(), "selected the parity set", slog.String("set", string(set)),
				slog.Int("selected", len(ids)), slog.Int("gate_only", len(gateOnlyIDs)),
				slog.Int("collected", len(collectedIDs(in.Collected))))
			return write(cmd.OutOrStdout(), ids...)
		},
	}
	cmd.Flags().String("gate-only-ids", "", "a file to write the node ids of the gate set's tests that "+GateOnlyFile+
		" names to, a line each, whichever set is selected (S3TESTS_GATE_ONLY_IDS)")
	cmd.Flags().String("set", string(SetGate), "the set: gate, or pr, the gate set less the tests "+GateOnlyFile+
		" names (S3TESTS_SET)")
	cmd.Flags().String("tests", "", "a file of the node ids or base ids to run alone, a line each, "+
		"each of which the set must hold (S3TESTS_TESTS)")
	cmd.Flags().String("unfiltered", "", "a file of pytest --collect-only -q's output without the marker "+
		"expression, which holds the tests the include lists name (S3TESTS_UNFILTERED)")
	return cmd
}

// readSet returns the set the set key names, pr or gate.
func readSet(v *viper.Viper) (Set, error) {
	switch set := Set(v.GetString("set")); set {
	case SetPR, SetGate:
		return set, nil
	default:
		return "", fmt.Errorf("--set must be %s or %s, not %q", SetPR, SetGate, string(set))
	}
}

// readUnfiltered returns the lines of the collection file the unfiltered key
// names, or none for an empty key.
func readUnfiltered(v *viper.Viper) ([]string, error) {
	path := v.GetString("unfiltered")
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path) //nolint:gosec // reading the collection named is the command's work
	if err != nil {
		return nil, fmt.Errorf("reading the collection without the marker expression: %w", err)
	}
	return strings.Split(string(data), "\n"), nil
}

// readTestsInput returns an Input holding the TESTS file the tests key names
// and its lines, or no file for an empty key. A name that is not a regular
// file is an error.
func readTestsInput(v *viper.Viper) (Input, error) {
	in := Input{TestsFile: v.GetString("tests")}
	if in.TestsFile == "" {
		return in, nil
	}
	if info, err := os.Stat(in.TestsFile); err != nil || !info.Mode().IsRegular() {
		return in, fmt.Errorf("no tests file %s", in.TestsFile)
	}
	var err error
	in.Tests, err = ReadList(in.TestsFile)
	return in, err
}

// write prints lines to w, each followed by a newline.
func write(w io.Writer, lines ...string) error {
	var b strings.Builder
	for _, line := range lines {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	if _, err := io.WriteString(w, b.String()); err != nil {
		return fmt.Errorf("writing the output: %w", err)
	}
	return nil
}

// writeFile writes lines to the file at path, each followed by a newline.
func writeFile(path string, lines []string) error {
	var b strings.Builder
	if err := write(&b, lines...); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil { //nolint:gosec // a run's output, read by all
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// configure binds the flags and the environment into v, reads the config
// file when one is named, and installs the default logger. The prefix is the
// tool's own, so that no RGW_GO_ setting meant for rgw-go applies to it.
func configure(cmd *cobra.Command, v *viper.Viper, stderr io.Writer) error {
	v.SetEnvPrefix("S3TESTS")
	v.SetEnvKeyReplacer(strings.NewReplacer("-", "_", ".", "_"))
	v.AutomaticEnv()
	if err := v.BindPFlags(cmd.Root().PersistentFlags()); err != nil {
		return fmt.Errorf("binding flags: %w", err)
	}
	if err := v.BindPFlags(cmd.LocalFlags()); err != nil {
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
