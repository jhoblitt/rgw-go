package clusterconf

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/jhoblitt/rgw-go/hack/internal/harness"
)

// Deps are what lc-mode and classes reach the host and the cluster through.
type Deps struct {
	Exec  harness.Execer
	Proc  fs.FS
	Now   func() time.Time
	Sleep func(ctx context.Context, d time.Duration) error
}

// HostDeps are the host's own.
func HostDeps() Deps {
	return Deps{Exec: harness.NewExecer(harness.ExecWaitDelay), Proc: os.DirFS("/proc"), Now: time.Now, Sleep: harness.Sleep}
}

// configurer is the Configurer the flags v holds name, reaching the release
// release's cluster through deps.
func configurer(cmd *cobra.Command, v *viper.Viper, deps Deps, release string) *Configurer {
	return &Configurer{
		Cluster: harness.Cluster{Exec: deps.Exec, Rooket: v.GetString("rooket"), Release: release, CallTimeout: 3 * time.Minute},
		Root:    v.GetString("root"), Make: v.GetString("make"), Proc: deps.Proc, Now: deps.Now, Sleep: deps.Sleep,
		SiteWait: 3 * time.Minute, SitePoll: 5 * time.Second, RookWait: 5 * time.Minute,
		RestartTimeout: 15 * time.Minute, Out: cmd.ErrOrStderr(),
	}
}

// readRelease returns the release the release key names, squid or tentacle.
func readRelease(v *viper.Viper) (string, error) {
	release := v.GetString("release")
	if release != "squid" && release != "tentacle" {
		return "", fmt.Errorf("--release must be squid or tentacle, not %q", release)
	}
	return release, nil
}

// clusterFlags are the flags both cluster commands take.
func clusterFlags(cmd *cobra.Command, release string) {
	f := cmd.Flags()
	f.String("release", "", release)
	f.String("rooket", "rooket", "the rooket that reaches the cluster (S3TESTS_ROOKET)")
	f.String("root", ".", "the repository's root, whose Makefile runs rgw-go-up and whose hack/rooket/out "+
		"holds rgw-go's pid and serve flags (S3TESTS_ROOT)")
}

// NewLCModeCmd returns the lc-mode command, which reads its flags through v
// and reaches the cluster through deps.
func NewLCModeCmd(v *viper.Viper, deps Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use: "lc-mode --release squid|tentacle --mode off|radosgw|rgw-go|both [--rooket PATH] [--root DIR] " +
			"[--make PATH]",
		Short: "Run the lifecycle workers the mode names",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			release, err := readRelease(v)
			if err != nil {
				return err
			}
			mode, err := ParseMode(v.GetString("mode"))
			if err != nil {
				return err
			}
			st, err := configurer(cmd, v, deps, release).Apply(cmd.Context(), mode)
			if err != nil {
				return err
			}
			for _, line := range st.Lines() {
				fmt.Fprintln(cmd.OutOrStdout(), line)
			}
			return nil
		},
	}
	clusterFlags(cmd, "the release whose cluster to set, squid or tentacle (S3TESTS_RELEASE)")
	cmd.Flags().String("mode", "", "the LC mode: off, radosgw, rgw-go or both (S3TESTS_MODE)")
	cmd.Flags().String("make", "make", "the make that runs make rgw-go-up (S3TESTS_MAKE)")
	return cmd
}

// NewClassesCmd returns the classes command, which reads its flags through
// v and, given a release, reaches its cluster through deps.
func NewClassesCmd(v *viper.Viper, deps Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use: "classes [--release squid|tentacle [--rooket PATH] [--root DIR]]",
		Short: "Print the storage classes the s3-tests conf names, as its storage_classes takes them; " +
			"given a release, add them to its cluster",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			line := ConfValue()
			if v.GetString("release") != "" {
				release, err := readRelease(v)
				if err != nil {
					return err
				}
				st, err := configurer(cmd, v, deps, release).EnsureClasses(cmd.Context())
				if err != nil {
					return err
				}
				line = st.Line()
			}
			if _, err := fmt.Fprintln(cmd.OutOrStdout(), line); err != nil {
				return fmt.Errorf("writing the output: %w", err)
			}
			return nil
		},
	}
	clusterFlags(cmd, "the release whose cluster to add the classes to, squid or tentacle; none to print "+
		"their names alone (S3TESTS_RELEASE)")
	return cmd
}
