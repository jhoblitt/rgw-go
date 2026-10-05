package authz

import (
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jhoblitt/rgw-go/internal/cephconf"
	"github.com/jhoblitt/rgw-go/internal/denc"
)

// aclGrantsMaxNum is ACL_GRANTS_MAX_NUM, which RGWPutACLs::execute uses for a
// negative rgw_acl_grants_max_num (rgw_op.cc:5867-5871 at v19.2.6,
// :6513-6517 at v20.2.4).
const aclGrantsMaxNum = 100

// Config is what the authorizer reads from Ceph configuration.
type Config struct {
	Release denc.Release
	// RejectInvalidPrincipals is rgw_policy_reject_invalid_principals
	// (default true).
	RejectInvalidPrincipals bool
	// EnforceSwiftACLs is rgw_enforce_swift_acls (default true).
	EnforceSwiftACLs bool
	// RemoteAddrParam is rgw_remote_addr_param (default "REMOTE_ADDR"): the
	// CGI-style name of the variable that carries the client address, a
	// request header's when it starts with HTTP_.
	RemoteAddrParam string
	// TrustForwardedHTTPS is rgw_trust_forwarded_https (default false).
	TrustForwardedHTTPS bool
	// ACLGrantsMaxNum is rgw_acl_grants_max_num (default 100). ConfigFrom
	// turns a negative value into 100.
	ACLGrantsMaxNum int
	// MaxListingResults is rgw_max_listing_results, the bound on a listing's
	// max-keys. radosgw's default is 1000 on Squid and 5000 on Tentacle
	// (rgw.yaml.in:3435 at v19.2.6, :3620 at v20.2.4), and that release
	// default applies here only when librados does not know the option, or
	// when the field is zero, which Ceph's minimum of 1 leaves free. Every
	// librados knows it, so ConfigFrom yields the configured value or the
	// linked librados's compiled default: the cluster's release's only when
	// the image links the cluster's librados.
	MaxListingResults int64
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// DefaultConfig is radosgw's defaults on release r.
func DefaultConfig(r denc.Release) Config {
	return Config{
		Release:                 r,
		RejectInvalidPrincipals: true,
		EnforceSwiftACLs:        true,
		RemoteAddrParam:         "REMOTE_ADDR",
		ACLGrantsMaxNum:         aclGrantsMaxNum,
		MaxListingResults:       defaultMaxListingResults(r),
	}
}

func defaultMaxListingResults(r denc.Release) int64 {
	if r >= denc.Tentacle {
		return 5000
	}
	return 1000
}

// ConfigFrom reads the options above through o for release r; an option
// librados does not know keeps its default.
func ConfigFrom(o *cephconf.Options, r denc.Release) (Config, error) {
	cfg := DefaultConfig(r)
	grants := int64(cfg.ACLGrantsMaxNum)
	listing := uint64(cfg.MaxListingResults) //nolint:gosec // a positive default
	err := errors.Join(
		read(o.Bool, "rgw_policy_reject_invalid_principals", &cfg.RejectInvalidPrincipals),
		read(o.Bool, "rgw_enforce_swift_acls", &cfg.EnforceSwiftACLs),
		read(o.String, "rgw_remote_addr_param", &cfg.RemoteAddrParam),
		read(o.Bool, "rgw_trust_forwarded_https", &cfg.TrustForwardedHTTPS),
		read(o.Int64, "rgw_acl_grants_max_num", &grants),
		read(o.Uint64, "rgw_max_listing_results", &listing),
	)
	if err != nil {
		return Config{}, fmt.Errorf("reading authorization config: %w", err)
	}
	if grants >= 0 {
		cfg.ACLGrantsMaxNum = int(min(grants, math.MaxInt32))
	} else {
		cfg.ACLGrantsMaxNum = aclGrantsMaxNum
	}
	cfg.MaxListingResults = int64(min(listing, math.MaxInt64)) //nolint:gosec // bounded by the min
	return cfg, nil
}

// read sets *dst to the option name as get reads it, leaving it alone for an
// option librados does not know.
func read[T any](get func(string) (T, error), name string, dst *T) error {
	v, err := get(name)
	switch {
	case err == nil:
		*dst = v
		return nil
	case errors.Is(err, cephconf.ErrUnknownOption):
		return nil
	default:
		return err
	}
}

// now is the clock's time.
func (c Config) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// maxListing is MaxListingResults, the release's default when zero.
func (c Config) maxListing() int64 {
	if c.MaxListingResults == 0 {
		return defaultMaxListingResults(c.Release)
	}
	return c.MaxListingResults
}
