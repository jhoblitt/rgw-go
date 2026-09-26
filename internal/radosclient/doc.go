// Package radosclient is the seam through which every RADOS operation flows.
//
// It defines the connected-object interfaces (Cluster, Pool, Watch) and the
// concrete compound-operation builders (ReadOp, WriteOp). A builder records
// its steps in call order; an implementation translates them when the op runs
// and fills each step's result afterwards. The package imports only the
// standard library so the driver and the object-class packages can depend on
// it without cgo.
//
// Read and Write return ctx.Err() when the context ends before the operation
// completes, but librados cannot cancel an operation in flight, so the
// implementation owns the step buffers until the completion fires. After a
// context error the op's results are undefined and callers must not read
// them: the implementation may still fill them when the completion fires.
package radosclient
