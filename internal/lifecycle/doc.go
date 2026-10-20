// Package lifecycle is a bucket's lifecycle configuration: radosgw's
// RGWLifecycleConfiguration and the LCRule, LCFilter, LCExpiration and
// LCTransition it holds, in the stored form radosgw writes under the
// user.rgw.lc attr; the LifecycleConfiguration document
// PutBucketLifecycleConfiguration reads and GetBucketLifecycleConfiguration
// writes, with the validation radosgw gives it; and the prefix map the
// lifecycle worker applies. Each is radosgw's at v19.2.7 and v20.2.4, but
// for the differences docs/exclusions.md records: the two agree on
// src/rgw/rgw_lc_s3.{h,cc}, rgw_lc.cc's rule checks and every encoder of
// src/rgw/rgw_lc.h, and the one decoder that differs, LCFilter's, reads
// every encoding either release writes alike.
package lifecycle
