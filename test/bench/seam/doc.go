// Package seam benchmarks the RADOS client seam's submission paths: what an
// operation costs the Go side in each completion mode, as throughput,
// submit-to-wake latency, CPU time per operation and OS threads. rados bench
// from the cluster's own image is the floor it is read against.
//
// The shapes are radosgw's operation compositions. read4k, write4k, read4m and
// write4m mirror rados bench's rand and write modes; headread4k is a GET's
// head read and headwrite4k a new object's head write, both within the 4 MiB
// head; indexrtt is the bucket index prepare and complete around a PUT.
//
// BenchmarkSeam, behind the integration build tag, connects through
// RGW_GO_TEST_CEPH_CONF in the completion mode -mode names. Run one process
// per mode: the Go runtime keeps an idle OS thread rather than destroying it,
// so one mode's threads would count against the next, and go-ceph's
// completion notifier is process-wide. It works in the namespace bench-<pid>
// of the pool rgw-go-test and runs a sub-benchmark per shape and
// concurrency. It skips a cell whose concurrency times its shape's Size, the
// data extent an iteration writes or asks to read, passes -budget-bytes,
// which defaults to the bytes goceph's in-flight limiter admits under
// librados's default objecter byte throttle. The limiter parks a
// submission past its bounds in Go; -inflight-ops and -inflight-bytes set
// them, and bounds above what a cell reaches leave the submission to the
// objecter throttle, which blocks an OS thread inside C in every mode.
//
// testing.B calls a sub-benchmark more than once while it calibrates b.N, and
// every call prepares its objects and appends its own JSON line to -results.
// A cell therefore has several lines, and the one with the largest n is its
// measurement. Every line's idle thread count is taken before the cell's
// first call, so that peak minus idle is all the cell grew, and the cell's
// objects are removed once its last call has ended. A later cell reuses the
// threads earlier cells and their cleanups left, so its growth understates
// what it would grow from a cold start: in sync mode, which parks a thread
// per operation in flight, only a cell with more in flight than any earlier
// cell or cleanup had shows growth of its own.
package seam
