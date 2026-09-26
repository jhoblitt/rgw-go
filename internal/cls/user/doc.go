// Package user is the client of the RADOS object class "user"
// (src/cls/user), which keeps a user's bucket list in the omap of the
// <user>.buckets object in the meta pool's users.uid namespace: one
// cls_user_bucket_entry per bucket name, and a cls_user_header of summed
// stats in the omap header. Request functions add one exec step to an op; a
// method that replies returns a result to decode once the op has run.
// radosgw-admin prints BucketEntry and Header, so they marshal to JSON as
// their C++ dump does. No request, reply or stored type differs between
// Squid, Tentacle and main, so the release argument selects nothing yet.
// The account-resource methods are out of scope.
package user
