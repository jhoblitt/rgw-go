// Package lifecycle is a bucket's lifecycle configuration: radosgw's
// RGWLifecycleConfiguration and the LCRule, LCFilter, LCExpiration and
// LCTransition it holds, in the stored form radosgw writes under the
// user.rgw.lc attr. The stored form is radosgw's at v19.2.7 and v20.2.4,
// whose src/rgw/rgw_lc.h agrees on every encoder; the one decoder that
// differs, LCFilter's, reads every encoding either release writes alike.
package lifecycle
