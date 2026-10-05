package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/jhoblitt/rgw-go/internal/cephconf"
)

// Config is what the verifier reads from Ceph configuration and the
// zonegroup.
type Config struct {
	// UseRados is rgw_s3_auth_use_rados (default true). With it false, and
	// Keystone and LDAP excluded, radosgw has no backend and denies every
	// request, anonymous included (RGW_Auth_S3::authorize,
	// rgw_rest_s3.cc:5119-5125 at v19.2.6, :5679-5685 at v20.2.4).
	UseRados bool
	// DisablePresignedURLs is rgw_s3_auth_disable_signature_url (default
	// false). radosgw applies it to every signed request, header-signed
	// included (rgw_rest_s3.cc:5607-5610 at v19.2.6, :6163-6166 at v20.2.4),
	// though rgw.yaml.in:911-916 documents it as presigned-only; rgw-go
	// mirrors radosgw.
	DisablePresignedURLs bool
	// Insecure is rgw_sigv4_insecure (default false): it skips the checks
	// that host, a present content-type and every x-amz-* header are signed
	// (CVE-2026-54330, rgw_auth_s3.cc:788-820 at v19.2.6, :765-797 at
	// v20.2.4).
	Insecure bool
	// DNSNames is rgw_dns_name plus the zonegroup hostnames, the list
	// s3.Config.DNSNames also gets: the SigV2 canonical resource starts with
	// the bucket a virtual-hosted request names in its Host, as
	// RGWREST::preprocess prepends it (rgw_rest.cc:2154-2161 at v19.2.6,
	// :2171-2178 at v20.2.4).
	DNSNames []string
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// DefaultConfig is radosgw's defaults: UseRados true, everything else off.
func DefaultConfig() Config { return Config{UseRados: true} }

// ConfigFrom reads the three options through o; an option librados does not
// know keeps its default. dnsNames is passed through.
func ConfigFrom(o *cephconf.Options, dnsNames []string) (Config, error) {
	cfg := DefaultConfig()
	cfg.DNSNames = dnsNames
	for _, opt := range []struct {
		name string
		dst  *bool
	}{
		{"rgw_s3_auth_use_rados", &cfg.UseRados},
		{"rgw_s3_auth_disable_signature_url", &cfg.DisablePresignedURLs},
		{"rgw_sigv4_insecure", &cfg.Insecure},
	} {
		v, err := o.Bool(opt.name)
		switch {
		case errors.Is(err, cephconf.ErrUnknownOption):
			continue
		case err != nil:
			return Config{}, fmt.Errorf("%s: %w", opt.name, err)
		}
		*opt.dst = v
	}
	return cfg, nil
}
