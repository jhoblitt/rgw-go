// Package auth authenticates S3 requests as radosgw does. It transcribes
// radosgw's rgw::auth::s3 code (rgw_auth_s3.cc, and the abstractors and
// engines in rgw_rest_s3.cc) and rgw_auth.cc's LocalApplier, down to the
// encodings, date parsing and header lookup they rest on, so that canonical
// strings and every accept or reject decision match radosgw's. It never
// logs a credential: no secret key, signature, signing key, string to sign
// or canonical request, at any level.
package auth
