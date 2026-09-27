// Package rgw marshals calls to the "rgw" RADOS object class, cls_rgw: the
// bucket index methods, the data object methods radosgw guards writes with,
// the usage log, and the omap-era garbage collection enqueue. It holds the
// class's request, reply and stored types with encoders and decoders that
// are byte-compatible with the C++ ones, each transcribing its C++ encode
// and decode bodies.
//
// A request function adds one exec step to an op; one whose method replies
// returns a result whose Result method decodes the reply once the op has
// run. Requests and stored types encode at the release given, and decoders
// accept every version from Squid through main. radosgw sends
// bucket_prepare_op, bucket_complete_op and the OLH methods behind
// guard_bucket_resharding on every release, and dir_suggest_changes too from
// Tentacle on, so callers add GuardBucketResharding to the op before them.
package rgw
