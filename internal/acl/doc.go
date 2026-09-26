// Package acl holds RGW's stored access control policy,
// RGWAccessControlPolicy, and the types it is built from, with encoders and
// decoders that are byte-compatible with radosgw's. As in package meta, each
// type transcribes its C++ encode and decode bodies, and its JSON form is the
// dump RGW writes for it as a field of a larger struct. Grant evaluation,
// canned ACLs and the S3 and Swift renderings live elsewhere.
package acl
