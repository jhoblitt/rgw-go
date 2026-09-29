package cephconf

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/jhoblitt/rgw-go/internal/radosclient"
)

// Getter reads a Ceph option's current value by name; radosclient.Cluster satisfies it.
type Getter interface {
	ConfigGet(name string) (string, error)
}

// MapGetter is a Getter over a map, for tests and the driver skeleton.
type MapGetter map[string]string

// ConfigGet returns the mapped value, or ErrUnknownOption for a missing key.
func (m MapGetter) ConfigGet(name string) (string, error) {
	v, ok := m[name]
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrUnknownOption, name)
	}
	return v, nil
}

// ErrUnknownOption is returned for an option name librados does not know.
var ErrUnknownOption = errors.New("cephconf: unknown option")

// Options is typed access to rgw_* and other Ceph options through a Getter.
// librados applies the mon config store during connect, so an Options over a
// connected Cluster sees what Rook set centrally.
//
// rados_conf_get renders every option type as text through Option::to_str
// (src/common/options.cc:40-66 at v19.2.6): booleans as true or false, sizes
// and durations as their bare count of bytes, seconds or milliseconds, and
// strings as stored. The typed accessors parse exactly those forms, and every
// parse failure names the option.
type Options struct{ g Getter }

// NewOptions returns Options reading through g.
func NewOptions(g Getter) *Options { return &Options{g: g} }

// String returns the option's text as librados renders it. rados_conf_get
// answers ENOENT for a name it does not know, which String reports as
// ErrUnknownOption with the getter's error kept as the cause.
func (o *Options) String(name string) (string, error) {
	v, err := o.g.ConfigGet(name)
	switch {
	case err == nil:
		return v, nil
	case errors.Is(err, radosclient.ErrNotFound) && !errors.Is(err, ErrUnknownOption):
		return "", fmt.Errorf("%w: %w", ErrUnknownOption, err)
	default:
		return "", err
	}
}

// Int64 parses an int, uint or a duration option's count.
func (o *Options) Int64(name string) (int64, error) {
	return parse(o, name, func(s string) (int64, error) { return strconv.ParseInt(s, 10, 64) })
}

// Uint64 parses a uint or size option.
func (o *Options) Uint64(name string) (uint64, error) {
	return parse(o, name, func(s string) (uint64, error) { return strconv.ParseUint(s, 10, 64) })
}

// Bool parses "true" or "false".
func (o *Options) Bool(name string) (bool, error) {
	return parse(o, name, func(s string) (bool, error) {
		switch s {
		case "true":
			return true, nil
		case "false":
			return false, nil
		}
		return false, fmt.Errorf("%q is neither true nor false", s)
	})
}

// Size parses a size option, rendered by librados as a bare byte count.
func (o *Options) Size(name string) (uint64, error) { return o.Uint64(name) }

// Seconds parses a secs option or an int option counting seconds.
func (o *Options) Seconds(name string) (time.Duration, error) {
	return parse(o, name, func(s string) (time.Duration, error) { return duration(s, time.Second) })
}

// Millis parses a millisecs option.
func (o *Options) Millis(name string) (time.Duration, error) {
	return parse(o, name, func(s string) (time.Duration, error) { return duration(s, time.Millisecond) })
}

// listDelims are ceph::split's default delimiters, which rgw::AppMain splits
// rgw_enable_apis on (rgw_appmain.cc:391 at main; get_str_vec's ";,= \t" at
// v19.2.6 and v20.2.4). rgw_dns_name is split on ", " (rgw_rest.cc:217), which
// cuts every valid host name the same way.
const listDelims = ";,= \t\n"

// List splits a string option into its items on ceph::split's delimiters,
// dropping empty items, as radosgw splits rgw_enable_apis and rgw_dns_name.
func (o *Options) List(name string) ([]string, error) {
	s, err := o.String(name)
	if err != nil {
		return nil, err
	}
	return strings.FieldsFunc(s, func(r rune) bool { return strings.ContainsRune(listDelims, r) }), nil
}

// parse reads name and converts its text with conv, naming the option in a
// conversion failure.
func parse[T any](o *Options, name string, conv func(string) (T, error)) (T, error) {
	var zero T
	s, err := o.String(name)
	if err != nil {
		return zero, err
	}
	v, err := conv(s)
	if err != nil {
		return zero, fmt.Errorf("option %s: %w", name, err)
	}
	return v, nil
}

// duration converts a bare count of unit to a time.Duration, refusing one
// that time.Duration cannot hold.
func duration(s string, unit time.Duration) (time.Duration, error) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, err
	}
	if n > math.MaxInt64/int64(unit) || n < math.MinInt64/int64(unit) {
		return 0, fmt.Errorf("%d times %s overflows a duration", n, unit)
	}
	return time.Duration(n) * unit, nil
}
