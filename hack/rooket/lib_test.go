package rooket_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// libLines runs body in bash with lib.sh sourced, under errexit and pipefail
// as the scripts source it, with args as its positional parameters, and
// returns the lines it printed.
func libLines(ctx context.Context, body string, args ...string) []string {
	GinkgoHelper()
	lib, err := filepath.Abs("lib.sh")
	Expect(err).NotTo(HaveOccurred())
	cmd := exec.CommandContext(ctx, "bash", append([]string{
		"-c",
		`set -euo pipefail; script=spec; source "$0"; ` + body, lib,
	}, args...)...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	Expect(err).NotTo(HaveOccurred(), "%s: %s", body, stderr.String())
	return strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
}

var _ = Describe("entity_options, the parity options rgw-go-up.sh sets on each gateway's entity", func() {
	var parity []string

	BeforeEach(func(ctx SpecContext) {
		parity = libLines(ctx, `printf '%s\n' "${parity_options[@]}"`)
		Expect(parity).To(ContainElements("rgw_enable_lc_threads=false", "rgw_lc_debug_interval=10"))
	})

	DescribeTable("gives rgw-go every parity option, rgw_enable_lc_threads as the LC mode passes it",
		func(ctx SpecContext, lc string) {
			want := slices.Clone(parity)
			want[slices.Index(want, "rgw_enable_lc_threads=false")] = "rgw_enable_lc_threads=" + lc
			Expect(libLines(ctx, `entity_options rgw-go "$1"`, lc)).To(Equal(want))
		},
		Entry("off, as the parity options have it", "false"),
		Entry("on, for a mode that runs rgw-go's worker", "true"),
	)

	It("gives radosgw every parity option but rgw_enable_lc_threads, which its object store's rgwConfig holds",
		func(ctx SpecContext) {
			want := slices.DeleteFunc(slices.Clone(parity), func(kv string) bool {
				return strings.HasPrefix(kv, "rgw_enable_lc_threads=")
			})
			Expect(libLines(ctx, `entity_options radosgw`)).To(Equal(want))
		})

	It("is what rgw-go-up.sh converges each entity to, on lines the script runs", func() {
		data, err := os.ReadFile("rgw-go-up.sh")
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(SatisfyAll(
			MatchRegexp(`(?m)^mapfile -t rgw_go_options < <\(entity_options rgw-go "\$\{lc_threads\}"\)$`),
			MatchRegexp(`(?m)^mapfile -t radosgw_options < <\(entity_options radosgw\)$`),
			MatchRegexp(`(?m)^converge "\$\{rgw_go_entity\}" "\$\{rgw_go_options\[@\]\}"$`),
			MatchRegexp(`(?m)^converge "\$\{radosgw_entity\}" "\$\{radosgw_options\[@\]\}"$`),
		))
	})
})

var _ = Describe("rgw-go's serve flags, which make lc-mode restarts it with", func() {
	It("rgw-go-up.sh records the serve flags it starts rgw-go with, one a line, on a line it runs", func() {
		data, err := os.ReadFile("rgw-go-up.sh")
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(MatchRegexp(
			`(?m)^if \(\(\$#\)\); then printf '%s\\n' "\$@" >"\$\{flagsfile\}"; else : >"\$\{flagsfile\}"; fi\n` +
				`RGW_GO_LOG_FORMAT=json nohup "\$\{repo\}/bin/rgw-go" serve "--metrics-addr=127.0.0.1:\$\{metrics_port\}" "\$@"`))
	})

	It("rgw-go-down.sh removes the serve flags with the pid", func(ctx SpecContext) {
		script, _, exited := downTree(ctx, serving)
		flags := filepath.Join(filepath.Dir(script), "out", "squid", "rgw-go.flags")
		Expect(os.WriteFile(flags, []byte("--rados-completions=pipe\n"), 0o600)).To(Succeed())
		ok, stdout, stderr := runDown(ctx, script)
		Expect(ok).To(BeTrue(), stdout+stderr)
		Expect(flags).NotTo(BeAnExistingFile())
		Eventually(exited).WithTimeout(5 * time.Second).WithPolling(10 * time.Millisecond).Should(Receive())
	})
})
