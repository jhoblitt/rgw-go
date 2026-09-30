// Package s3 is the S3 protocol layer: radosgw's request lifecycle and the
// responses it renders.
//
// ParseRequest reads a request's Host, path and query once, as radosgw's
// RGWREST::preprocess and RGWHandler_REST_S3::init_from_header do, and
// Dispatch selects the op that serves it by method, scope and subresource in
// the precedence of radosgw's op_* methods at the cluster's release.
// PostAuthInit, run once the request is authenticated as radosgw runs
// postauth_init, gives it the identity's tenant and checks its tenant, bucket
// and object names.
//
// Handler, which NewHandler builds, serves every request in the order of
// radosgw's process_request: the transaction id; the parse, with the
// refusals radosgw makes before it has an op; Dispatch; the
// rgw_max_concurrent_requests cap; authentication through an Authenticator,
// which sees the dispatched route's signed payload forms; PostAuthInit; the
// suspended-user check; and the route's HandlerFunc. Once the response is
// written, the handler observes the request through op.Metrics and logs its
// usage, refused requests included.
//
// Handlers bind per scope. serviceHandlers, bucketHandlers and
// objectHandlers, each in its own file, bind their entries to their own
// scope, and multipartHandlers binds each entry to the one scope Dispatch
// routes its name in. A route with no handler answers NotImplemented.
//
// A HandlerFunc renders through WriteError, radosgw's error document with its
// length and Accept-Ranges; WriteXML, a document carrying the length
// radosgw's frontend adds; SetContentLength, where radosgw's op names its
// length; sinkOf, the op.Sink a streaming op writes to; and startChunkedXML
// or writeChunkedXML, for the listings radosgw sends with chunked transfer
// encoding. Every string a document carries is an xmltext.Text, escaped as
// radosgw's XMLFormatter escapes it.
package s3
