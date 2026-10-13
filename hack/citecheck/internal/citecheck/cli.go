package citecheck

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/jhoblitt/rgw-go/internal/version"
)

// defaultCeph is the clone of ceph/ceph citations are checked against when
// neither --ceph, CITECHECK_CEPH nor the config file names one.
const defaultCeph = "/home/jhoblitt/github/ceph"

// errFailed is Run's error when a citation does not hold.
var errFailed = errors.New("citations failed")

// ExitStatus returns the exit status for an error Run returned: 0 for none,
// 1 when a citation does not hold, and 2 for anything else: a refused
// command line, a file that cannot be read, or a clone git cannot read.
func ExitStatus(err error) int {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, errFailed):
		return 1
	default:
		return 2
	}
}

// Run executes the citecheck command against args and returns the first
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
		Use:           "citecheck [--ceph DIR] FILE...",
		Short:         "Check the Ceph source citations in markdown files against a ceph/ceph clone",
		Version:       version.String(),
		SilenceUsage:  true,
		SilenceErrors: true,
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) == 0 {
				return errors.New("no markdown file given")
			}
			return nil
		},
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			return configure(cmd, v, stderr)
		},
		RunE: func(cmd *cobra.Command, files []string) error {
			return check(cmd.Context(), v.GetString("ceph"), files, cmd.OutOrStdout())
		},
	}
	cmd.SetIn(stdin)
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)

	pf := cmd.PersistentFlags()
	pf.String("config", "", "config file to read after flags and environment")
	pf.String("log-level", "info", "log level: debug, info, warn, or error (CITECHECK_LOG_LEVEL)")
	pf.String("log-format", "json", "log format: json or text (CITECHECK_LOG_FORMAT)")
	cmd.Flags().String("ceph", defaultCeph, "the clone of ceph/ceph to check against (CITECHECK_CEPH)")
	return cmd
}

// configure binds the flags and the environment into v, reads the config
// file when one is named, and installs the default logger. The prefix is the
// tool's own, so that no RGW_GO_ setting meant for rgw-go applies to it.
func configure(cmd *cobra.Command, v *viper.Viper, stderr io.Writer) error {
	v.SetEnvPrefix("CITECHECK")
	v.SetEnvKeyReplacer(strings.NewReplacer("-", "_", ".", "_"))
	v.AutomaticEnv()
	if err := v.BindPFlags(cmd.Root().PersistentFlags()); err != nil {
		return fmt.Errorf("binding flags: %w", err)
	}
	if err := v.BindPFlag("ceph", cmd.Flags().Lookup("ceph")); err != nil {
		return fmt.Errorf("binding --ceph: %w", err)
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

// check reads every file before it checks any, so that a file that cannot
// be read refuses the run before anything is printed, then prints each
// citation that does not hold and the count of those checked and failed.
func check(ctx context.Context, ceph string, files []string, out io.Writer) error {
	docs := make([][]byte, len(files))
	for i, name := range files {
		data, err := os.ReadFile(name) //nolint:gosec // reading the files named is the command's work
		if err != nil {
			return fmt.Errorf("reading %s: %w", name, err)
		}
		docs[i] = data
	}
	c, err := NewChecker(ctx, ceph)
	if err != nil {
		return err
	}
	checked, failed := 0, 0
	for i, name := range files {
		for _, cite := range Scan(docs[i]) {
			reason, err := c.Check(ctx, cite)
			if err != nil {
				return fmt.Errorf("%s:%d: %s: %w", name, cite.Line, cite.Text, err)
			}
			checked++
			if reason != "" {
				failed++
				fmt.Fprintf(out, "%s:%d: %s: %s\n", name, cite.Line, cite.Text, reason)
			}
		}
	}
	fmt.Fprintf(out, "citations checked: %d, failed: %d\n", checked, failed)
	if failed > 0 {
		return fmt.Errorf("%d of %d %w", failed, checked, errFailed)
	}
	return nil
}
