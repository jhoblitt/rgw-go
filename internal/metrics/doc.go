// Package metrics is rgw-go's Prometheus surface, on a registry of its own
// rather than the process-wide default one: request counts and latency by
// radosgw op name, requests in flight, request and response body bytes, the
// RADOS transport's operation and byte counters, and the process's OS thread
// count, beside the Go runtime's and the process's own collectors. The thread
// count is the kernel's, librados's threads included, because go_threads
// counts only the Go runtime's thread records.
//
// It does not reproduce radosgw's perf counters or its ops log, which
// radosgw reports through its admin socket and ops log file. Labels are
// bounded: op takes radosgw's op names, status the hundreds class of the
// HTTP status rather than the code, mode the transport's completion mode.
package metrics
