// Package goceph implements the radosclient seam over go-ceph's rados
// package, built from the jhoblitt/go-ceph fork with the ceph_preview tag.
//
// Every Read and Write is a compound operation: the seam's steps are
// translated in order onto a go-ceph ReadOp or WriteOp, and the results are
// copied back into the seam's result structs once the operation completes and
// before its go-ceph resources are released.
//
// Mode selects how a Read or Write waits for librados:
//
//   - ModeSync calls the blocking Operate, which parks one OS thread per
//     operation in flight. The context is checked before the call only.
//   - ModeCallback and ModePipe call OperateAsync and wait on the
//     completion's Done channel or the context, so a waiting operation costs a
//     goroutine rather than a thread. In callback mode librados completes the
//     operation through a cgo callback on its own thread; in pipe mode it
//     writes the completion's id to a pipe that one goroutine drains.
//
// When the context ends first, Read and Write return ctx.Err() and a reaper
// goroutine keeps the completion, and with it every buffer the operation
// pinned, until librados reports it done, then releases it. The seam's
// results are left untouched in that case.
//
// A Pool keeps a bounded set of I/O contexts; each operation takes one for
// itself until its submission returns, setting the Pool's object locator on
// it. Closing a Pool, or the Cluster, refuses new operations, closes the
// Pool's watches and waits for its operations, abandoned ones included,
// before any I/O context is destroyed.
//
// go-ceph's completion notifier is process-wide, so every connected Cluster
// using an asynchronous mode must use the same one: Connect fails while a
// Cluster in the other asynchronous mode is open.
package goceph
