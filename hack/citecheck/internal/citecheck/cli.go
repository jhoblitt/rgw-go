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
		Use:           "citecheck [--ceph DIR] [--fix | --form-only] FILE...",
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
		PreRunE: func(*cobra.Command, []string) error {
			if v.GetBool("fix") && v.GetBool("form-only") {
				return errors.New("--fix and --form-only cannot be combined: --fix checks every citation against the clone")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, files []string) error {
			m := checkAll
			switch {
			case v.GetBool("fix"):
				m = fix
			case v.GetBool("form-only"):
				m = formOnly
			}
			return check(cmd.Context(), v.GetString("ceph"), m, files, cmd.OutOrStdout())
		},
	}
	cmd.SetIn(stdin)
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)

	pf := cmd.PersistentFlags()
	pf.String("config", "", "config file to read after flags and environment")
	pf.String("log-level", "info", "log level: debug, info, warn, or error (CITECHECK_LOG_LEVEL)")
	pf.String("log-format", "json", "log format: json or text (CITECHECK_LOG_FORMAT)")
	f := cmd.Flags()
	f.String("ceph", defaultCeph, "the clone of ceph/ceph to check against (CITECHECK_CEPH)")
	f.Bool("fix", false, "rewrite each code span citation that holds into a link, in place (CITECHECK_FIX)")
	f.Bool("form-only", false, "check only the citations' form, without a clone (CITECHECK_FORM_ONLY)")
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
	for _, name := range []string{"ceph", "fix", "form-only"} {
		if err := v.BindPFlag(name, cmd.Flags().Lookup(name)); err != nil {
			return fmt.Errorf("binding --%s: %w", name, err)
		}
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

// mode is what a run does with the citations it finds.
type mode int

const (
	// checkAll checks every link against the clone, and fails every code
	// span citation as not a link.
	checkAll mode = iota
	// formOnly checks what needs no clone, and fails every code span
	// citation as not a link.
	formOnly
	// fix checks every citation against the clone, and rewrites each code
	// span citation that holds into a link.
	fix
)

// notALink is why a code span citation fails, unless --fix rewrites it.
const notALink = "not a link"

// check reads every file before it checks any, so that a file that cannot
// be read refuses the run before anything is printed, then prints each
// citation that does not hold and the count of those checked and failed.
// Under fix it then writes back each file it rewrote a code span citation
// in, and counts the citations rewritten.
func check(ctx context.Context, ceph string, m mode, files []string, out io.Writer) error {
	docs := make([][]byte, len(files))
	for i, name := range files {
		data, err := os.ReadFile(name) //nolint:gosec // reading the files named is the command's work
		if err != nil {
			return fmt.Errorf("reading %s: %w", name, err)
		}
		docs[i] = data
	}
	var c *Checker
	if m != formOnly {
		var err error
		if c, err = NewChecker(ctx, ceph); err != nil {
			return err
		}
	}
	checked, failed, rewritten := 0, 0, 0
	edits := make([][]edit, len(files))
	for i, name := range files {
		cites := Scan(docs[i])
		for k := range cites {
			cite := &cites[k]
			reason, err := judge(ctx, c, m, cite)
			if err != nil {
				return fmt.Errorf("%s:%d: %s: %w", name, cite.Line, cite.Text, err)
			}
			checked++
			if reason != "" {
				failed++
				fmt.Fprintf(out, "%s:%d: %s: %s\n", name, cite.Line, cite.Text, reason)
				continue
			}
			if m == fix && cite.Form == CodeSpan {
				link, err := linkFor(*cite)
				if err != nil {
					return fmt.Errorf("%s:%d: %w", name, cite.Line, err)
				}
				edits[i] = append(edits[i], edit{start: cite.Start, end: cite.End, text: link})
			}
		}
	}
	for i, name := range files {
		if len(edits[i]) == 0 {
			continue
		}
		// The file exists, so WriteFile keeps its mode.
		if err := os.WriteFile(name, rewrite(docs[i], edits[i]), 0o600); err != nil {
			return fmt.Errorf("writing %s: %w", name, err)
		}
		rewritten += len(edits[i])
		slog.InfoContext(ctx, "rewrote code span citations into links", slog.String("file", name),
			slog.Int("rewritten", len(edits[i])))
	}
	if m == fix {
		fmt.Fprintf(out, "citations checked: %d, failed: %d, rewritten: %d\n", checked, failed, rewritten)
	} else {
		fmt.Fprintf(out, "citations checked: %d, failed: %d\n", checked, failed)
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d %w", failed, checked, errFailed)
	}
	return nil
}

// judge returns why a citation fails in mode m, or "" when it holds. Under
// fix, a code span citation that holds fails still when it cannot be
// rewritten where it stands.
func judge(ctx context.Context, c *Checker, m mode, cite *Citation) (string, error) {
	switch {
	case cite.Form == CodeSpan && m != fix:
		return notALink, nil
	case m == formOnly:
		return formProblem(*cite), nil
	}
	reason, err := c.Check(ctx, *cite)
	if err != nil || reason != "" || cite.Form != CodeSpan {
		return reason, err
	}
	return cite.NoFix, nil
}
