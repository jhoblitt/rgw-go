// Package lifecycle is a bucket's lifecycle configuration: radosgw's
// RGWLifecycleConfiguration and the LCRule, LCFilter, LCExpiration and
// LCTransition it holds, in the stored form radosgw writes under the
// user.rgw.lc attr; the LifecycleConfiguration document
// PutBucketLifecycleConfiguration reads and GetBucketLifecycleConfiguration
// writes, with the validation radosgw gives it; the prefix map the
// lifecycle worker applies; the worker's decision for each object it lists
// and the notification of what it does; and the x-amz-expiration and
// abort-date headers the rules give. Each is
// radosgw's at v19.2.7 and v20.2.4, but for the differences
// docs/exclusions.md records: the two agree on src/rgw/rgw_lc_s3.{h,cc},
// rgw_lc.cc's rule checks and its checks of an object's actions, and every
// encoder of src/rgw/rgw_lc.h; of what differs, LCFilter's decoder reads every
// encoding either release writes alike, and the lister's page boundary and
// s3_expiration_header take one release's form on both.
package lifecycle
