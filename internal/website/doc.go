// Package website is the meaning of a bucket's static website configuration,
// meta.WebsiteConf: the PutBucketWebsite document read into one and the
// GetBucketWebsite document written from one, the key an index document turns
// a request into, and the redirect a routing rule or RedirectAllRequestsTo
// answers. The rules are radosgw's at v19.2.7 and v20.2.4, whose
// src/rgw/rgw_website.{h,cc} are the same at both tags, with the website ops
// of src/rgw/rgw_rest_s3.cc.
package website
