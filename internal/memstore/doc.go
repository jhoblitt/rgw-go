// Package memstore is an in-memory implementation of every store interface
// in op, with radosgw's observable semantics and none of its layout: users
// and their indexes, buckets, objects, multipart uploads, stats and quota,
// the usage log and the metadata sections, all held in maps under one lock.
// It backs the ops' specs and anything else that needs a working store
// without a cluster.
package memstore
