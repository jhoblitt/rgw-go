package limited

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/jhoblitt/rgw-go/internal/version"
)

// envNames binds the keys whose environment names make bench-seam's
// interface: the Makefile passes the cluster's ceph.conf, the rooket binary
// and the build tags under the names hack/bench/seam.sh reads.
var envNames = map[string]string{
	"ceph-conf": "RGW_GO_TEST_CEPH_CONF",
	"rooket":    "ROOKET",
	"go-tags":   "GO_TAGS",
}

// Run executes the command against args on this host and returns the first
// error.
func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	return run(ctx, args, stdin, stdout, stderr, func(*Runner) {})
}

// run is Run with adjust applied to the Runner before it runs, so that a spec
// can replace its seams.
func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, adjust func(*Runner)) error {
	// cobra reads os.Args[1:] in place of nil args.
	if args == nil {
		args = []string{}
	}
	cmd := newRootCmd(stdin, stdout, stderr, adjust)
	cmd.SetArgs(args)
	return cmd.ExecuteContext(ctx)
}

func newRootCmd(stdin io.Reader, stdout, stderr io.Writer, adjust func(*Runner)) *cobra.Command {
	v := viper.New()
	cmd := &cobra.Command{
		Use:           "limited squid|tentacle",
		Short:         "Re-run the seam microbenchmark's over-budget cell under the in-flight limiter",
		Version:       version.String(),
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.ExactArgs(1),
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			return configure(cmd, v, stderr)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := settings(v, args[0]).Check()
			if err != nil {
				return err
			}
			if cfg.Plan {
				cfg.WritePlan(cmd.OutOrStdout())
				return nil
			}
			handler, err := newHandler(v)
			if err != nil {
				return err
			}
			r := NewRunner(cmd.OutOrStdout(), stderr, handler)
			adjust(r)
			return r.Run(cmd.Context(), cfg)
		},
	}
	cmd.SetIn(stdin)
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)

	pf := cmd.PersistentFlags()
	pf.String("config", "", "config file to read after flags and environment (BENCH_CONFIG)")
	pf.String("log-level", "info", "log level: debug, info, warn, or error (BENCH_LOG_LEVEL)")
	pf.String("log-format", "json", "log format: json or text (BENCH_LOG_FORMAT)")
	f := cmd.Flags()
	f.String("limiter", "", "must be 1: bench-seam's switch to this run (BENCH_LIMITER)")
	f.String("plan", "", "1: check every setting, print the benchmark processes and their watchdog limits, and exit without touching a cluster (BENCH_PLAN)")
	f.String("inflight-bytes", "", "the limiter's byte bound, a positive whole number of bytes (BENCH_INFLIGHT_BYTES; 104857600)")
	f.String("over-objects", "", "objects per benchmark process (BENCH_OVER_OBJECTS; 640, or 128 with --quick)")
	f.String("quick", "", "1: a short run, 128 objects per process (BENCH_QUICK)")
	// --plan and --quick alone mean 1, as BENCH_PLAN=1 and BENCH_QUICK=1 do;
	// any value but 1 is refused, as the environment's is.
	f.Lookup("plan").NoOptDefVal = "1"
	f.Lookup("quick").NoOptDefVal = "1"
	f.String("timeout", "", "each benchmark process's limit, in seconds or as 1h30m (BENCH_TIMEOUT; a tenth of a second an object, plus 5m)")
	f.String("min-avail-gib", "", "pool space each process waits for, in GiB (BENCH_MIN_AVAIL_GIB; 5)")
	f.String("ceph-conf", "", "unset, or the release's hack/rooket/out/<release>/ceph.conf (RGW_GO_TEST_CEPH_CONF)")
	f.String("rooket", "rooket", "the rooket binary (ROOKET)")
	f.String("go-tags", "ceph_preview", "build tags besides integration (GO_TAGS)")
	f.String("repo", ".", "the repository's root (BENCH_REPO)")
	return cmd
}

// configure binds the flags and the environment into v, reads the config
// file when one is named, and installs the default logger.
func configure(cmd *cobra.Command, v *viper.Viper, stderr io.Writer) error {
	v.SetEnvPrefix("BENCH")
	v.SetEnvKeyReplacer(strings.NewReplacer("-", "_", ".", "_"))
	v.AutomaticEnv()
	for key, name := range envNames {
		if err := v.BindEnv(key, name); err != nil {
			return fmt.Errorf("binding %s: %w", name, err)
		}
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
	handler, err := newHandler(v)
	if err != nil {
		return err
	}
	slog.SetDefault(slog.New(handler(stderr)))
	return nil
}

// newHandler returns the log's handler for a writer, at the configured level
// and in the configured format.
func newHandler(v *viper.Viper) (func(io.Writer) slog.Handler, error) {
	var level slog.Level
	if err := level.UnmarshalText([]byte(v.GetString("log-level"))); err != nil {
		return nil, fmt.Errorf("parsing log level: %w", err)
	}
	opts := &slog.HandlerOptions{Level: level}
	switch format := v.GetString("log-format"); format {
	case "json":
		return func(w io.Writer) slog.Handler { return slog.NewJSONHandler(w, opts) }, nil
	case "text":
		return func(w io.Writer) slog.Handler { return slog.NewTextHandler(w, opts) }, nil
	default:
		return nil, fmt.Errorf("unknown log format %q", format)
	}
}

// settings reads the run's settings from v.
func settings(v *viper.Viper, release string) Settings {
	return Settings{
		Release:       release,
		Limiter:       v.GetString("limiter"),
		InflightBytes: v.GetString("inflight-bytes"),
		OverObjects:   v.GetString("over-objects"),
		Timeout:       v.GetString("timeout"),
		MinAvailGiB:   v.GetString("min-avail-gib"),
		CephConf:      v.GetString("ceph-conf"),
		Rooket:        v.GetString("rooket"),
		GoTags:        v.GetString("go-tags"),
		Repo:          v.GetString("repo"),
		Plan:          v.GetString("plan"),
		Quick:         v.GetString("quick"),
	}
}
