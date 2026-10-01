package report

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

	"github.com/jhoblitt/rgw-go/internal/version"
)

// Run executes the report command tree against args and returns the first
// error.
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
		Use:           "report",
		Short:         "Render benchmark results into markdown reports",
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
	pf.String("log-level", "info", "log level: debug, info, warn, or error (REPORT_LOG_LEVEL)")
	pf.String("log-format", "json", "log format: json or text (REPORT_LOG_FORMAT)")
	cmd.AddCommand(newSeamCmd(v))
	return cmd
}

// configure binds the root's flags and the environment into v, reads the
// config file when one is named, and installs the default logger. It runs
// before every command; each command binds its own flags in its PreRunE.
func configure(cmd *cobra.Command, v *viper.Viper, stderr io.Writer) error {
	v.SetEnvPrefix("REPORT")
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

func newSeamCmd(v *viper.Viper) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "seam --dir DIR",
		Short: "Render a seam sweep directory into its REPORT.md",
		Args:  cobra.NoArgs,
		PreRunE: func(cmd *cobra.Command, _ []string) error {
			if err := v.BindPFlag("dir", cmd.Flags().Lookup("dir")); err != nil {
				return fmt.Errorf("binding --dir: %w", err)
			}
			if v.GetString("dir") == "" {
				return errors.New("--dir is required")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir := v.GetString("dir")
			r, err := Seam(cmd.Context(), dir)
			if err != nil {
				return err
			}
			path := filepath.Join(dir, "REPORT.md")
			if err := os.WriteFile(path, []byte(r.Markdown), 0o600); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n", path, r.Answer())
			return nil
		},
	}
	cmd.Flags().String("dir", "", "the sweep directory hack/bench/seam.sh wrote (REPORT_DIR)")
	return cmd
}
