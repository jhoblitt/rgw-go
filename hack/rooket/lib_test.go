package rooket_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// libRun runs body in bash with lib.sh sourced, under errexit and pipefail
// as the scripts source it, with args as its positional parameters, and
// returns what it printed and how it exited.
func libRun(ctx context.Context, body string, args ...string) (stdout, stderr string, err error) {
	GinkgoHelper()
	lib, err := filepath.Abs("lib.sh")
	Expect(err).NotTo(HaveOccurred())
	cmd := exec.CommandContext(ctx, "bash", append([]string{
		"-c",
		`set -euo pipefail; script=spec; source "$0"; ` + body, lib,
	}, args...)...)
	var errOut strings.Builder
	cmd.Stderr = &errOut
	out, err := cmd.Output()
	return string(out), errOut.String(), err
}

// libLines runs body as libRun does, and returns the lines it printed once
// it has exited 0.
func libLines(ctx context.Context, body string, args ...string) []string {
	GinkgoHelper()
	out, stderr, err := libRun(ctx, body, args...)
	Expect(err).NotTo(HaveOccurred(), "%s: %s", body, stderr)
	return strings.Split(strings.TrimSuffix(out, "\n"), "\n")
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
				`pid=\$\("\$\{here\}/rgw-go-start.sh" "\$\{release\}" "\$\{log\}" serve "--metrics-addr=127.0.0.1:\$\{metrics_port\}" "\$@"`))
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

// mapStubs, run once lib.sh is sourced, answer ceph_cmd's service dump with
// the file $1, and make sleep advance bash's SECONDS rather than wait, so
// that wait_rgw's 180 s pass at once.
const mapStubs = `dump=$1
ceph_cmd() { [[ "$*" == "service dump -f json" ]] && cat "${dump}"; }
sleep() { SECONDS=$((SECONDS + $1)); }
`

// radosgwEntry is the service map entry of Rook's radosgw under gid: Rook
// sets POD_NAME on it, which librados reports as pod_name.
func radosgwEntry(gid int) string {
	return podEntry(gid, "rook-ceph-rgw-ceph-objectstore-a-568b7fb698-z74ft")
}

// podEntry is radosgwEntry run with POD_NAME set to pod, which librados
// reports as pod_name whenever it is set, empty or not.
func podEntry(gid int, pod string) string {
	return fmt.Sprintf(`{"gid": %d, "start_stamp": "2026-11-14T12:00:00.000000+0000", "metadata": {
	  "id": "ceph.objectstore.a", "pod_name": %q,
	  "pod_namespace": "rook-ceph", "realm_name": "ceph-objectstore", "zonegroup_name": "ceph-objectstore",
	  "zone_name": "ceph-objectstore", "frontend_config#0": "beast port=80"}}`, gid, pod)
}

// rgwGoEntry is the service map entry, under gid, of an rgw-go the harness
// runs on the host as client.rgw.<id> or client.<id>: no pod sets its
// POD_NAME, so librados reports no pod_name.
func rgwGoEntry(gid int, id string) string {
	return fmt.Sprintf(`{"gid": %d, "start_stamp": "2026-11-14T12:05:00.000000+0000", "metadata": {
	  "id": %q, "realm_name": "ceph-objectstore", "zonegroup_name": "ceph-objectstore",
	  "zone_name": "ceph-objectstore", "frontend_config#0": "beast endpoint=127.0.0.1:7481"}}`, gid, id)
}

// serviceDump writes ceph service dump -f json's output listing entries
// under their gids, and returns its path.
func serviceDump(entries ...string) string {
	GinkgoHelper()
	daemons := map[string]json.RawMessage{"summary": json.RawMessage(`""`)}
	for _, e := range entries {
		var d struct {
			GID int64 `json:"gid"`
		}
		Expect(json.Unmarshal([]byte(e), &d)).To(Succeed())
		daemons[strconv.FormatInt(d.GID, 10)] = json.RawMessage(e)
	}
	data, err := json.Marshal(map[string]any{"services": map[string]any{"rgw": map[string]any{"daemons": daemons}}})
	Expect(err).NotTo(HaveOccurred())
	path := filepath.Join(GinkgoT().TempDir(), "service-dump.json")
	Expect(os.WriteFile(path, data, 0o600)).To(Succeed())
	return path
}

var _ = Describe("the radosgw entries wait_rgw reads, which skip every rgw-go's", func() {
	It("rgw_daemon prints the radosgw's entry from a map listing rgw-gos on either side of it", func(ctx SpecContext) {
		dump := serviceDump(rgwGoEntry(4100, "rgw-go"), radosgwEntry(4721), rgwGoEntry(4722, "admin"))
		Expect(libLines(ctx, mapStubs+`rgw_daemon`, dump)).To(HaveExactElements(MatchJSON(radosgwEntry(4721))))
	})

	It("wait_rgw keeps an entry whose pod_name is empty beside one whose pod_name is set", func(ctx SpecContext) {
		dump := serviceDump(radosgwEntry(4100), rgwGoEntry(4721, "rgw-go"), podEntry(4800, ""))
		Expect(libLines(ctx, mapStubs+`wait_rgw spec spec 'length > 0'`, dump)).To(HaveExactElements(
			MatchJSON("[" + radosgwEntry(4100) + ", " + podEntry(4800, "") + "]")))
	})

	Context("in populate.sh's wait for the radosgw's reload", func() {
		var waitReload string

		BeforeEach(func() {
			data, err := os.ReadFile("populate.sh")
			Expect(err).NotTo(HaveOccurred())
			waitReload = regexp.MustCompile(`(?ms)^wait_reload\(\) \{$.*?^\}$`).FindString(string(data))
			Expect(waitReload).To(ContainSubstring("wait_rgw "), "populate.sh's wait_reload")
		})

		// reload runs populate.sh's wait_reload after a use_site that read
		// the radosgw under gid 4100, over the map dump lists.
		reload := func(ctx context.Context, dump string) (stderr string, err error) {
			_, stderr, err = libRun(ctx, mapStubs+`ROOKET_NAME=rgw-go-squid; rgw='{"gid": 4100}'; eval "$2"; wait_reload`,
				dump, waitReload)
			return stderr, err
		}

		It("goes on waiting while only rgw-gos have new gids, and leaves them out of its message", func(ctx SpecContext) {
			var listed bytes.Buffer
			Expect(json.Compact(&listed, []byte(radosgwEntry(4100)))).To(Succeed())
			stderr, err := reload(ctx, serviceDump(radosgwEntry(4100), rgwGoEntry(4721, "rgw-go"), rgwGoEntry(4722, "admin")))
			Expect(err).To(HaveOccurred())
			Expect(stderr).To(HavePrefix("spec: the radosgw did not finish reloading the committed period in 180s " +
				"(radosgw entries: [" + listed.String() + "]); restart it with "))
		})

		It("ends once the radosgw has a new gid, its old one still listed beside an rgw-go", func(ctx SpecContext) {
			stderr, err := reload(ctx, serviceDump(radosgwEntry(4100), radosgwEntry(4800), rgwGoEntry(4721, "rgw-go")))
			Expect(err).NotTo(HaveOccurred(), stderr)
		})
	})
})
