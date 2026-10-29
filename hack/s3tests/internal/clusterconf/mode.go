// Package clusterconf readies a release's rooket cluster for the s3-tests
// lifecycle runs: the LC mode, which of the two gateways runs its lifecycle
// worker, and the two plain storage classes the transition tests move
// objects between.
//
// radosgw reads rgw_enable_lc_threads only when it builds its store, at
// startup and at each realm reload, and Rook rewrites the radosgw entity's
// options at every reconcile, the object store's rgwConfig over its own
// (https://github.com/rook/rook/blob/v1.21.1/pkg/operator/ceph/object/config.go#L236-L250,
// https://github.com/rook/rook/blob/v1.21.1/pkg/operator/ceph/object/config.go#L290-L297),
// so that radosgw's half of a mode goes through rgwConfig and then a
// restart: a ceph config set alone would not outlast the next reconcile.
// rgw-go reads the option once, at Open, from its own entity, which make
// rgw-go-up sets from RGW_GO_LC_THREADS before it starts rgw-go.
package clusterconf

import (
	"fmt"
	"slices"
)

// Mode is an LC mode: the gateways whose lifecycle workers run.
type Mode string

const (
	// ModeOff runs neither worker: the parity mode, under which phase 1's
	// set is recorded and compared.
	ModeOff Mode = "off"
	// ModeRadosgw runs radosgw's worker alone.
	ModeRadosgw Mode = "radosgw"
	// ModeRgwGo runs rgw-go's worker alone.
	ModeRgwGo Mode = "rgw-go"
	// ModeBoth runs both, so that they share the zone's lifecycle shards.
	ModeBoth Mode = "both"
)

// Modes are the LC modes.
var Modes = []Mode{ModeOff, ModeRadosgw, ModeRgwGo, ModeBoth}

// ParseMode returns the mode s names.
func ParseMode(s string) (Mode, error) {
	if m := Mode(s); slices.Contains(Modes, m) {
		return m, nil
	}
	return "", fmt.Errorf("--mode must be off, radosgw, rgw-go or both, not %q", s)
}

// Radosgw reports whether m runs radosgw's lifecycle worker.
func (m Mode) Radosgw() bool { return m == ModeRadosgw || m == ModeBoth }

// RgwGo reports whether m runs rgw-go's lifecycle worker.
func (m Mode) RgwGo() bool { return m == ModeRgwGo || m == ModeBoth }
