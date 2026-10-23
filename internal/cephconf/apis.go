package cephconf

import "slices"

// APIs is rgw_enable_apis as rgw-go honors it.
type APIs struct {
	S3 bool
	// Website is s3website, with which cond_init_apis gives RGWRESTMgr_S3
	// its website handler
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_appmain.cc#L301-L312 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_appmain.cc#L310-L321 at v20.2.4),
	// which serves the requests RGWREST::preprocess flags as website ones.
	Website bool
	// WebsiteFirst is s3website's priority above s3's. preprocess ranks each
	// name by its first place, an earlier one higher, and an absent one below
	// any named, so s3website outranks an s3 named after it or not at all
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L2038-L2051 at v19.2.7,
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L2050-L2063 at v20.2.4).
	// With it v19.2.7 flags every request with a Host as a website one
	// (https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L2150-L2156),
	// and v20.2.4 every request, one with no Host included
	// (https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L2162-L2168
	// and
	// https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L2193-L2198).
	// It is set only with Website.
	WebsiteFirst bool
	Admin        bool
	// Ignored lists, once each and in order, every other name the option
	// carried (swift, swift_auth, sts, iam, notifications, s3control,
	// s3vectors, ...), for the startup log line that reports them.
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
	s3Named := false
	for _, n := range names {
		switch n {
		case "s3":
			a.S3, s3Named = true, true
			continue
		case "admin":
			a.Admin = true
			continue
		case "s3website":
			if !a.Website {
				a.WebsiteFirst = !s3Named
			}
			a.S3, a.Website = true, true
			continue
		}
		if !slices.Contains(a.Ignored, n) {
			a.Ignored = append(a.Ignored, n)
		}
	}
	return a, nil
}
