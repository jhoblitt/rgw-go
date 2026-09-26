// Package refcount is the client of the RADOS object class "refcount"
// (src/cls/refcount), which keeps an obj_refcount, the set of tags holding an
// object, in its "refcount" xattr and removes the object when the last tag is
// put. radosgw takes a ref on every tail object a copy shares, and from
// Tentacle (v20.2.4, rgw_dedup.cc) its dedup takes refs on the tails it
// shares too; a plain upload writes its tail objects without one, so on
// Squid they read as the implicit wildcard ref unless copied. Tags are opaque bytes here: radosgw's are the NUL-terminated write
// tag, and adding the NUL is the caller's business. Every request and reply is
// (1,1), and obj_refcount (2,1), in Squid, Tentacle and main, so the release
// argument selects nothing yet.
package refcount
