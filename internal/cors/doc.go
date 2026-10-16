// Package cors is a bucket's CORS configuration: radosgw's RGWCORSConfiguration
// and RGWCORSRule, their stored form under the user.rgw.cors attr, the
// CORSConfiguration document PutBucketCors reads and GetBucketCors writes, and
// the choice of the rule that answers a request and of the Access-Control
// headers that rule sends. The stored form and the document are radosgw's at
// v19.2.7 and v20.2.4, whose src/rgw/rgw_cors.{h,cc} and
// src/rgw/rgw_cors_s3.{h,cc} are identical. The rule is chosen as Amazon S3
// chooses it, which radosgw's main branch does and neither release does
// (docs/exclusions.md, "CORS rules are matched as Amazon S3 matches them").
package cors
