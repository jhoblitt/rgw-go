// Package acl holds RGW's stored access control policy,
// RGWAccessControlPolicy, and the types it is built from, with encoders and
// decoders that are byte-compatible with radosgw's. As in package meta, each
// type transcribes its C++ encode and decode bodies, and its JSON form is the
// dump RGW writes for it as a field of a larger struct. The package also
// evaluates a policy's grants for a requester as radosgw does, gives the ACL
// permission each IAM action needs, and holds the public-access block a
// bucket stores beside its policy.
package acl
