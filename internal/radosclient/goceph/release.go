package goceph

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// RequiredOSDRelease returns the OSD map's require_osd_release name.
func (c *cluster) RequiredOSDRelease(ctx context.Context) (string, error) {
	out, status, err := c.MonCommand(ctx, []byte(`{"prefix":"osd dump","format":"json"}`))
	if err != nil {
		return "", fmt.Errorf("goceph: osd dump: %s: %w", status, err)
	}
	var dump struct {
		RequireOSDRelease string `json:"require_osd_release"`
	}
	if err := json.Unmarshal(out, &dump); err != nil {
		return "", fmt.Errorf("goceph: decoding osd dump: %w", err)
	}
	if dump.RequireOSDRelease == "" {
		return "", fmt.Errorf("goceph: osd dump has no require_osd_release: %w", radosclient.ErrNoData)
	}
	return dump.RequireOSDRelease, nil
}

// Release reads the cluster's require_osd_release and maps it to the release
// denc encodes for. A name newer than every release denc knows maps to the
// newest one with a warning; a name below the Squid floor is an error.
func Release(ctx context.Context, c radosclient.Cluster) (denc.Release, error) {
	name, err := c.RequiredOSDRelease(ctx)
	if err != nil {
		return 0, err
	}
	return releaseFor(ctx, name)
}

func releaseFor(ctx context.Context, name string) (denc.Release, error) {
	r, ok := denc.ClusterRelease(ctx, name)
	if !ok {
		return 0, fmt.Errorf("goceph: require_osd_release %q is below the squid floor: %w", name, radosclient.ErrReleaseTooOld)
	}
	return r, nil
}
