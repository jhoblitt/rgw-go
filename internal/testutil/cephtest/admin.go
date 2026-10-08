package cephtest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// errNotHarness is a manifest that does not name a cluster hack/rooket
// brought up.
var errNotHarness = errors.New("not a hack/rooket cluster")

// RadosgwAdmin runs radosgw-admin in the cluster's toolbox through
// hack/rooket/admin.sh and returns its stdout; the release comes from the
// manifest. It never touches any cluster but the rooket one named by the
// manifest.
func RadosgwAdmin(ctx context.Context, conf string, args ...string) ([]byte, error) {
	return runHarness(ctx, conf, "admin.sh", args...)
}

// Endpoint returns the URL the host reaches the cluster's radosgw at,
// through hack/rooket/endpoint.sh.
func Endpoint(ctx context.Context, conf string) (string, error) {
	out, err := runHarness(ctx, conf, "endpoint.sh")
	return strings.TrimSpace(string(out)), err
}

// runHarness runs script from hack/rooket for the release the manifest
// beside conf names, with args after the release, and returns its stdout.
func runHarness(ctx context.Context, conf, script string, args ...string) ([]byte, error) {
	release, err := harnessRelease(conf)
	if err != nil {
		return nil, err
	}
	root, err := moduleRoot()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(root, "hack", "rooket", script)
	cmd := exec.CommandContext(ctx, path, append([]string{release}, args...)...) //nolint:gosec // the harness's own script, run with the specs' arguments
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("%s %s %s: %w: %s", script, release, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// harnessRelease is the release of the manifest beside conf, which must
// name the rooket cluster hack/rooket/lib.sh names for it.
func harnessRelease(conf string) (string, error) {
	b, err := os.ReadFile(filepath.Join(filepath.Dir(conf), "manifest.json"))
	if err != nil {
		return "", fmt.Errorf("reading the manifest: %w", err)
	}
	var m struct {
		Release    string `json:"release"`
		RooketName string `json:"rooket_name"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return "", fmt.Errorf("decoding the manifest: %w", err)
	}
	if (m.Release != "squid" && m.Release != "tentacle") || m.RooketName != "rgw-go-"+m.Release {
		return "", fmt.Errorf("manifest release %q, cluster %q: %w", m.Release, m.RooketName, errNotHarness)
	}
	return m.Release, nil
}

// moduleRoot is the nearest directory above the working directory holding
// go.mod.
func moduleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("finding the module root: %w", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no go.mod above the working directory: %w", os.ErrNotExist)
		}
		dir = parent
	}
}
