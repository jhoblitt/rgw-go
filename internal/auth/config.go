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
	// https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L5119-L5125 at v19.2.6,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5679-L5685 at v20.2.4).
	UseRados bool
	// DisablePresignedURLs is rgw_s3_auth_disable_signature_url (default
	// false). radosgw applies it to every signed request, header-signed
	// included
	// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L5607-L5610 at v19.2.6,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L6163-L6166 at v20.2.4),
	// though
	// https://github.com/ceph/ceph/blob/v19.2.6/src/common/options/rgw.yaml.in#L911-L916
	// documents it as presigned-only; rgw-go mirrors radosgw. A POST object
	// form is authenticated whatever it says, as radosgw's browser-upload
	// abstractor never reads it (Verifier.AuthenticateBrowserUpload).
	DisablePresignedURLs bool
	// Insecure is rgw_sigv4_insecure (default false): it skips the checks
	// that host and every x-amz-* header but x-amz-content-sha256 are signed
	// (CVE-2026-54330,
	// https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_auth_s3.cc#L788-L842 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L765-L797 at v20.2.4,
	// which also checks a present content-type and x-amz-content-sha256).
	Insecure bool
	// DNSNames is rgw_dns_name plus the zonegroup hostnames, the list
	// s3.Config.DNSNames also gets: the SigV2 canonical resource starts with
	// the bucket a virtual-hosted request names in its Host, as
	// RGWREST::preprocess prepends it
	// (https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L2154-L2161 at v19.2.6,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L2171-L2178 at v20.2.4).
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
