// Package version is the client of the RADOS object class "version"
// (src/cls/version), which keeps an obj_version, a counter and a random tag,
// in the object's ceph.objclass.version xattr. radosgw uses it as the
// RGWObjVersionTracker on metadata objects. Request functions add one exec
// step to an op; a method that replies returns a result to decode once the
// op has run. Every request and reply is (1,1) in Squid, Tentacle and main,
// so the release argument selects nothing yet.
package version
