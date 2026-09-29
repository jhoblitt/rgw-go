// Package op is the protocol-neutral core every gateway operation runs on:
// the request and identity an op sees, radosgw's S3 error set, the op
// lifecycle and its runner, the authorization hook, and the store
// interfaces, split by concern, that the driver and memstore implement.
//
// The S3 and admin protocol layers parse HTTP into a Request, authenticate
// it, and hand it to an Op through Run, which drives radosgw's lifecycle
// (rgw_process_authenticated) in its order. Ops reach RADOS only through the
// store interfaces in Env; counterfeiter fakes of each live in opfakes.
package op

//go:generate go tool counterfeiter -generate
