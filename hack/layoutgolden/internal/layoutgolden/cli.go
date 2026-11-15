package layoutgolden

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

// defaultOut is where the goldens are written, relative to the repository's
// root, where the tool is run.
const defaultOut = "internal/driver/testdata/layout"

// Run executes the layoutgolden command against args.
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
		Use:           "layoutgolden --release squid|tentacle [--in FILE] [--out DIR]",
		Short:         "Write the ?layout goldens of a release from the gate's capture",
		Version:       version.String(),
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			return configure(cmd, v, stderr)
		},
		PreRunE: func(*cobra.Command, []string) error {
			if v.GetString("release") == "" {
				return errors.New("--release is required")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			release, out := v.GetString("release"), v.GetString("out")
			in := v.GetString("in")
			if in == "" {
				in = filepath.Join(out, release+".jsonl")
			}
			return generate(cmd.Context(), release, in, filepath.Join(out, release), cmd.OutOrStdout())
		},
	}
	cmd.SetIn(stdin)
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)

	pf := cmd.PersistentFlags()
	pf.String("config", "", "config file to read after flags and environment")
	pf.String("log-level", "info", "log level: debug, info, warn, or error (LAYOUTGOLDEN_LOG_LEVEL)")
	pf.String("log-format", "json", "log format: json or text (LAYOUTGOLDEN_LOG_FORMAT)")
	f := cmd.Flags()
	f.String("release", "", "the release the capture was run against, squid or tentacle (LAYOUTGOLDEN_RELEASE)")
	f.String("in", "", "the capture to read, <out>/<release>.jsonl by default (LAYOUTGOLDEN_IN)")
	f.String("out", defaultOut, "the directory holding a directory of goldens for each release (LAYOUTGOLDEN_OUT)")
	return cmd
}

// generate replaces the release's goldens in dir with those of the capture in.
func generate(ctx context.Context, release, in, dir string, w io.Writer) error {
	goldens, err := readCapture(in, release)
	if err != nil {
		return err
	}
	if err := Write(dir, goldens); err != nil {
		return err
	}
	for _, g := range goldens {
		slog.DebugContext(ctx, "wrote a golden", slog.String("shape", g.Shape), slog.Int("manifest_bytes", len(g.Manifest)),
			slog.Int("document_bytes", len(g.Document)))
	}
	fmt.Fprintf(w, "wrote %d goldens of %s into %s\n", len(goldens), release, dir)
	return nil
}

// readCapture parses the capture in the file in.
func readCapture(in, release string) (goldens []Golden, err error) {
	f, err := os.Open(in) //nolint:gosec // reading the file named is the command's work
	if err != nil {
		return nil, fmt.Errorf("opening the capture: %w", err)
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	goldens, err = Parse(f, release)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", in, err)
	}
	return goldens, nil
}

// configure binds the flags and the environment into v, reads the config
// file when one is named, and installs the default logger. The prefix is the
// tool's own, so that no RGW_GO_ setting meant for rgw-go applies to it.
func configure(cmd *cobra.Command, v *viper.Viper, stderr io.Writer) error {
	v.SetEnvPrefix("LAYOUTGOLDEN")
	v.SetEnvKeyReplacer(strings.NewReplacer("-", "_", ".", "_"))
	v.AutomaticEnv()
	if err := v.BindPFlags(cmd.Root().PersistentFlags()); err != nil {
		return fmt.Errorf("binding flags: %w", err)
	}
	if err := v.BindPFlags(cmd.Flags()); err != nil {
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
