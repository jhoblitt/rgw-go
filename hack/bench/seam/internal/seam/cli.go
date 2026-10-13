package seam

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/jhoblitt/rgw-go/hack/bench/internal/harness"
	"github.com/jhoblitt/rgw-go/internal/version"
)

// envNames are the environment names of the keys that are not BENCH_ and the
// key upper-cased: the Makefile passes the cluster's ceph.conf, the rooket
// binary and the build tags under these names, and the sweep has always read
// BENCH_TIME, CONC and rooket's ROOKET_ENGINE. No other name reads them.
var envNames = map[string]string{
	"ceph-conf":  "RGW_GO_TEST_CEPH_CONF",
	"rooket":     "ROOKET",
	"go-tags":    "GO_TAGS",
	"bench-time": "BENCH_TIME",
	"conc":       "CONC",
	"engine":     "ROOKET_ENGINE",
}

// largeConcEnv is BENCH_LARGE_CONC, which counts when set, even when empty.
const largeConcEnv = "BENCH_LARGE_CONC"

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
		Use:           "seam squid|tentacle",
		Short:         "Sweep the seam microbenchmark and its rados bench floor, or re-run its over-budget cell under the in-flight limiter",
		Version:       version.String(),
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.ExactArgs(1),
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			return configure(cmd, v, stderr)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := settings(cmd, v, args[0]).Check()
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
	f.String("limiter", "", "1: re-run only the over-budget cell under the in-flight limiter (BENCH_LIMITER)")
	f.String("plan", "", "1: check every setting, print the schedule of benchmark processes and floor runs with their watchdog limits, and exit without touching a cluster (BENCH_PLAN)")
	f.String("quick", "", "1: a short run where unset otherwise: BENCH_TIME=5s, BENCH_FLOOR_SECONDS=5, CONC=16,256, BENCH_LARGE_CONC=16, "+
		"100 write4m objects and 128 over-budget objects (BENCH_QUICK)")
	// --plan and --quick alone mean 1, as BENCH_PLAN=1 and BENCH_QUICK=1 do;
	// any value but 1 is refused, as the environment's is.
	f.Lookup("plan").NoOptDefVal = "1"
	f.Lookup("quick").NoOptDefVal = "1"
	f.String("bench-time", "", "benchtime of each time-bounded cell, in whole hours, minutes and seconds (BENCH_TIME; 20s)")
	f.String("floor-seconds", "", "seconds of each time-bounded rados bench run (BENCH_FLOOR_SECONDS; 20)")
	f.String("conc", "", "concurrencies of the 4 KiB, head and index shapes, comma separated (CONC; 1,16,64,256,512)")
	f.String("large-conc", "", "concurrencies of read4m and write4m among 1, 4 and 16; empty skips them (BENCH_LARGE_CONC; 1,4,16)")
	f.String("write4m-objects", "", "objects per 4 MiB write cell and floor run (BENCH_WRITE4M_OBJECTS; 1000)")
	f.String("over-objects", "", "objects per over-budget cell (BENCH_OVER_OBJECTS; 640)")
	f.String("inflight-bytes", "", "the limiter re-run's byte bound, a positive whole number of bytes (BENCH_INFLIGHT_BYTES; 104857600)")
	f.String("min-avail-gib", "", "pool space each 4 MiB write waits for, in GiB (BENCH_MIN_AVAIL_GIB; 5)")
	f.String("timeout", "", "each benchmark process's limit, in seconds or as 1h30m (BENCH_TIMEOUT; its cell's planned duration plus 5m)")
	f.String("floor-timeout", "", "each floor run's limit, in seconds or as 1h30m (BENCH_FLOOR_TIMEOUT; its window plus 5m)")
	f.String("floor-image", "", "the image whose rados runs the floor, which must run the release of the librados the benchmark links (BENCH_FLOOR_IMAGE; quay.io/ceph/ceph:v<that release>)")
	f.String("engine", "", "the container engine that runs rados bench (ROOKET_ENGINE; podman)")
	f.String("ceph-conf", "", "unset, or the release's hack/rooket/out/<release>/ceph.conf (RGW_GO_TEST_CEPH_CONF)")
	f.String("rooket", "rooket", "the rooket binary (ROOKET)")
	f.String("go-tags", "ceph_preview", "build tags besides integration (GO_TAGS)")
	f.String("repo", ".", "the repository's root (BENCH_REPO)")
	return cmd
}

// configure binds the flags and the environment into v, each key to the one
// environment variable that names it, reads the config file when one is
// named, and installs the default logger.
func configure(cmd *cobra.Command, v *viper.Viper, stderr io.Writer) error {
	if err := v.BindPFlags(cmd.Flags()); err != nil {
		return fmt.Errorf("binding flags: %w", err)
	}
	if err := harness.BindEnv(v, "BENCH", envNames); err != nil {
		return fmt.Errorf("binding the environment: %w", err)
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

// settings reads the run's settings from cmd's flags and v.
func settings(cmd *cobra.Command, v *viper.Viper, release string) Settings {
	return Settings{
		Release:        release,
		Limiter:        v.GetString("limiter"),
		InflightBytes:  v.GetString("inflight-bytes"),
		OverObjects:    v.GetString("over-objects"),
		Timeout:        v.GetString("timeout"),
		MinAvailGiB:    v.GetString("min-avail-gib"),
		CephConf:       v.GetString("ceph-conf"),
		Rooket:         v.GetString("rooket"),
		GoTags:         v.GetString("go-tags"),
		Repo:           v.GetString("repo"),
		Plan:           v.GetString("plan"),
		Quick:          v.GetString("quick"),
		BenchTime:      v.GetString("bench-time"),
		FloorSeconds:   v.GetString("floor-seconds"),
		Conc:           v.GetString("conc"),
		LargeConc:      largeConc(cmd, v),
		Write4mObjects: v.GetString("write4m-objects"),
		FloorTimeout:   v.GetString("floor-timeout"),
		FloorImage:     v.GetString("floor-image"),
		Engine:         v.GetString("engine"),
		ConfigFile:     v.ConfigFileUsed(),
	}
}

// largeConc is --large-conc, BENCH_LARGE_CONC or the config file's
// large-conc, in that order, or nil when none is set. viper takes an empty
// environment variable for an unset one, which here would run the 4 MiB cells
// an empty value skips.
func largeConc(cmd *cobra.Command, v *viper.Viper) *string {
	if f := cmd.Flags().Lookup("large-conc"); f.Changed {
		value := f.Value.String()
		return &value
	}
	if value, ok := os.LookupEnv(largeConcEnv); ok {
		return &value
	}
	if v.InConfig("large-conc") {
		value := v.GetString("large-conc")
		return &value
	}
	return nil
}
