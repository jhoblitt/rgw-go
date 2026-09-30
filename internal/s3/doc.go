// Package s3 is the S3 protocol layer. ParseRequest reads a request's Host,
// path and query once, as radosgw's RGWREST::preprocess and
// RGWHandler_REST_S3::init_from_header do, and Dispatch selects the op that
// serves it by method, scope and subresource in the precedence of radosgw's
// op_* methods at the cluster's release. PostAuthInit, run once the request
// is authenticated as radosgw runs postauth_init, gives it the identity's
// tenant and checks its tenant, bucket and object names.
package s3
