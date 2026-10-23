package harness

import (
	"errors"
	"strings"

	"github.com/spf13/viper"
)

// BindEnv binds every key v knows to exactly one environment variable: the
// one names gives the key, else prefix, "_" and the key upper-cased with its
// dashes as underscores. viper's AutomaticEnv reads the prefixed name before
// a bound one, so a stray BENCH_GO_TAGS would override the GO_TAGS a Makefile
// passes; bound this way, no other name stands in for a documented one or
// overrides it. v's flags must be bound first, since they are the keys it
// knows.
func BindEnv(v *viper.Viper, prefix string, names map[string]string) error {
	var errs []error
	for _, key := range v.AllKeys() {
		name, ok := names[key]
		if !ok {
			name = prefix + "_" + strings.ToUpper(strings.ReplaceAll(key, "-", "_"))
		}
		errs = append(errs, v.BindEnv(key, name))
	}
	return errors.Join(errs...)
}
