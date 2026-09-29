// Package frontend is rgw-go's HTTP frontend: it reads radosgw's beast
// frontend configuration, the rgw_frontends option, into a Spec of the
// listeners, TLS settings and limits beast would apply, and serves a handler
// on those listeners with beast's per-operation timeouts.
package frontend
