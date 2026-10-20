package op

import "github.com/jhoblitt/rgw-go/internal/denc"

// SetMetadataPutRuleForTest gives section the put rule of status and
// zeroVersion until restore runs, which puts back the row it replaced.
func SetMetadataPutRuleForTest(section string, status func(applied bool, rel denc.Release) string, zeroVersion bool) (restore func()) {
	old, had := metadataPutRules[section]
	metadataPutRules[section] = putRule{status: status, zeroVersion: zeroVersion}
	return func() {
		if had {
			metadataPutRules[section] = old
			return
		}
		delete(metadataPutRules, section)
	}
}
