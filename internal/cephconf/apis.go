package cephconf

import "slices"

// APIs is rgw_enable_apis as rgw-go honors it.
type APIs struct {
	S3    bool
	Admin bool
	// Ignored lists, once each and in order, every other name the option
	// carried (swift, swift_auth, s3website, sts, iam, notifications,
	// s3control, s3vectors, ...), for the startup log line that reports them.
	Ignored []string
}

// EnabledAPIs reads rgw_enable_apis. s3website is a specialization of s3 and
// enables it, as rgw::AppMain::cond_init_apis does; names match exactly, as
// they do in its map. radosgw also leaves S3 off when rgw_swift_url_prefix
// is "/", which puts Swift at the root; rgw-go serves no Swift, so that
// option changes nothing here.
func EnabledAPIs(o *Options) (APIs, error) {
	names, err := o.List("rgw_enable_apis")
	if err != nil {
		return APIs{}, err
	}
	var a APIs
	for _, n := range names {
		switch n {
		case "s3":
			a.S3 = true
			continue
		case "admin":
			a.Admin = true
			continue
		case "s3website":
			a.S3 = true
		}
		if !slices.Contains(a.Ignored, n) {
			a.Ignored = append(a.Ignored, n)
		}
	}
	return a, nil
}
