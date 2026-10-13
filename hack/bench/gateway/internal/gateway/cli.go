package gateway

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/jhoblitt/rgw-go/hack/bench/internal/harness"
	"github.com/jhoblitt/rgw-go/internal/version"
)

// envNames are the environment names of the keys that are not BENCH_ and the
// key upper-cased: the Makefile passes the rgw-go flags, the build tags and
// the rooket binary under these names, and no other name reads them.
var envNames = map[string]string{
	"rgw-go-flags": "RGW_GO_FLAGS",
	"go-tags":      "GO_TAGS",
	"rooket":       "ROOKET",
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
		Use:           "gateway squid|tentacle radosgw|rgw-go [PROFILE...]",
		Short:         "Run the gateway comparison's profiles against radosgw or rgw-go on a release's rooket cluster",
		Version:       version.String(),
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.MinimumNArgs(2),
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			return configure(cmd, v, stderr)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := settings(v, args).Check()
			if err != nil {
				return err
			}
			if cfg.Plan {
				if !cfg.WritePlan(cmd.OutOrStdout(), cfg.IdleAvail) {
					return fmt.Errorf("a profile's dataset is more than half of %d B: lower its threads or objects", cfg.IdleAvail)
				}
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
	f.String("smoke", "", "1: a run that only proves the tooling, its numbers measuring nothing: 5 s phases, BENCH_CONC=8, "+
		"50 objects per reader, 4 large threads and 16 keys per list thread where unset otherwise (BENCH_SMOKE)")
	f.String("plan", "", "1: check every setting, print each profile's dataset and the free space it needs against "+
		"BENCH_IDLE_AVAIL bytes free, and exit without touching a cluster, failing when one needs more (BENCH_PLAN)")
	// --smoke and --plan alone mean 1, as BENCH_SMOKE=1 and BENCH_PLAN=1 do;
	// any value but 1 is refused, as the environment's is.
	f.Lookup("smoke").NoOptDefVal = "1"
	f.Lookup("plan").NoOptDefVal = "1"
	f.String("idle-avail", "", "the data pool's free bytes a plan is judged against; only with BENCH_PLAN=1 (BENCH_IDLE_AVAIL)")
	f.String("seconds", "", "each measured phase's time limit, in seconds (BENCH_SECONDS; 60)")
	f.String("conc", "", `the small and noop profiles' concurrencies, space separated (BENCH_CONC; "1 8 32 64 128 256")`)
	f.String("put-objects", "", "small-put's objects per thread (BENCH_PUT_OBJECTS; 3000)")
	f.String("get-objects", "", "small-get's read set, objects per thread at the highest concurrency (BENCH_GET_OBJECTS; 400)")
	f.String("large-threads", "", "the large profiles' threads (BENCH_LARGE_THREADS; 32)")
	f.String("large-objects", "", "the large profiles' objects per thread (BENCH_LARGE_OBJECTS; 1)")
	f.String("list-threads", "", "the list profile's threads (BENCH_LIST_THREADS; 64)")
	f.String("list-objects", "", "the list profile's keys per thread (BENCH_LIST_OBJECTS; 1600)")
	f.String("rgw-go-flags", "", "rgw-go serve flags for rgw-go-up.sh, such as --rados-completions=pipe (RGW_GO_FLAGS)")
	f.String("go-tags", "ceph_preview", "build tags; CGO_* come from the environment (GO_TAGS)")
	f.String("rooket", "rooket", "the rooket binary (ROOKET)")
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

// settings reads the run's settings from args, the release, the gateway and
// the profiles, and from v.
func settings(v *viper.Viper, args []string) Settings {
	return Settings{
		Release:      args[0],
		Gateway:      args[1],
		Profiles:     args[2:],
		Smoke:        v.GetString("smoke"),
		Plan:         v.GetString("plan"),
		IdleAvail:    v.GetString("idle-avail"),
		Seconds:      v.GetString("seconds"),
		Conc:         v.GetString("conc"),
		PutObjects:   v.GetString("put-objects"),
		GetObjects:   v.GetString("get-objects"),
		LargeThreads: v.GetString("large-threads"),
		LargeObjects: v.GetString("large-objects"),
		ListThreads:  v.GetString("list-threads"),
		ListObjects:  v.GetString("list-objects"),
		RgwGoFlags:   v.GetString("rgw-go-flags"),
		GoTags:       v.GetString("go-tags"),
		Rooket:       v.GetString("rooket"),
		Repo:         v.GetString("repo"),
		ConfigFile:   v.ConfigFileUsed(),
	}
}
