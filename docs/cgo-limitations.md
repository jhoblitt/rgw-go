# go-ceph and cgo limitations

This registry records every limitation rgw-go hits in its RADOS client that
comes from go-ceph, librados's C API, or the cgo boundary. It is the input to
the phase 1 question the design spec poses: whether a pure-Go RADOS client
would remove a real bottleneck or cost.

Each entry names its class:

- **Inherent** means the limit comes from librados, its C API, or cgo itself.
  Only a pure-Go client removes it.
- **Binding** means go-ceph's Go wrapper is missing something or gets it wrong.
  The jhoblitt/go-ceph fork can fix it, so it is not an argument for a rewrite.

Each entry also records how it was found, its status, and what the phase 1
benchmark should measure. Add an entry whenever a task, review or benchmark
finds a new limit, and update the status when one is fixed or measured.

## Inherent

### OS threads owned by librados

- **Evidence:** before issuing any operation, the Task 12 integration process
  had about 49 OS threads against Squid and 58 against Tentacle. librados runs
  its own messenger, finisher and timer threads.
- **Status:** accepted for now.
- **Measure:** thread count at idle and under load, per mode, against the
  design target of GOMAXPROCS plus a small constant.

### A parked OS thread per synchronous operation

- **Evidence:** in sync mode each in-flight operation blocks one OS thread in
  a cgo call. The Task 12 integration run added 11 to 14 threads under its
  small load.
- **Mitigation:** the callback and pipe modes held the thread count flat.
- **Measure:** threads and throughput versus concurrency, sync against the two
  async modes.

### Completions cross from C back to Go

- **Evidence:** an async completion must reach Go through a C-to-Go callback
  (a cgo callback on a librados finisher thread) or through a pipe the fork
  writes and a Go goroutine reads. The fork's aio mode is process-wide, so
  callback-mode and pipe-mode clusters cannot coexist.
- **Measure:** per-operation latency and CPU for callback and pipe against
  sync; this is the core of the cgo question.

### A write operation cannot return class-method output

- **Evidence:** `rados_write_op_exec` in librados.h has no output buffer,
  unlike `rados_read_op_exec`, although the OSD wire protocol can return data
  from a write. Modifying class methods that return data (2pc_queue reserve,
  user reset_user_stats2) must run inside a read operation with
  `LIBRADOS_OPERATION_RETURNVEC`.
- **Status:** worked around by the design's read-op route. It depends on the
  OSD accepting a modifying method in a read op, which Task 12's fix round is
  proving with an integration spec.

### The locator is per I/O context, not per operation

- **Evidence:** `rados_ioctx_locator_set_key` sets the locator on an I/O
  context. RGW sets a locator on every object whose name starts with `_`, so
  the client juggles I/O contexts per operation. Task 12's first version cached
  one context per locator, which grew without bound under user control.
- **Status:** Task 12 fix round moves to pooled contexts with a per-operation
  locator set.
- **Measure:** cost of the extra locator set and context borrow per operation.

### The object version is per I/O context

- **Evidence:** `rados_get_last_version` takes an I/O context and races
  between concurrent synchronous writes on it. Sync mode therefore borrows a
  private I/O context per write. Async completions carry their own version.
- **Measure:** included in sync-mode per-operation cost.

### Payload copies across the boundary

- **Evidence:** librados copies write payloads into its own buffers when the
  operation is built. Class-method output lands in C memory, and the fork copies
  it into Go. Reads land in pinned Go memory.
- **Measure:** allocation and copy cost on large objects; CPU profile with the
  cgo boundary attributed, as the spec's phase 1 section asks.

### Cancellation stops only the client

- **Evidence:** `rados_aio_cancel` makes the Objecter drop the operation with
  `-ECANCELED` (IoCtxImpl::aio_cancel). A write already sent to the OSD may
  still apply. Until the completion fires, Go buffers stay pinned and the I/O
  context must stay open. A pool closed with an abandoned operation in flight
  segfaulted in Task 12 before in-flight counting was added.
- **Note:** a pure-Go client has the same wire-level limit on sent writes, but
  owns its buffers and needs no pinning or reaper.

### Build and deployment cost

- **Evidence:** every build needs librados headers and the shared library.
  `CGO_ENABLED=0` and cross-compilation are impossible, so the goreleaser
  config must change once `cmd/rgw-go` imports the client. CI installs
  librados-dev. Local sandboxed builds need `CCACHE_DISABLE=1`.

## Binding

| Limitation | Found | Status |
|---|---|---|
| Empty or nil buffers panicked in `WriteOp.SetXattr` and the steps behind `Write`, `WriteFull`, `WriteSame`, `CmpExt` and `ReadOp.Read` (`&b[0]` with no length check) | Task 12 planning | Fixed in fork commit 18b1ab8 |
| The same unguarded `&b[0]` in `IOContext.WriteFull`, `Append`, `GetXattr`, `SetXattr`, the ioctx buffer loops, the striper and `Checksum` | Fork follow-up | Open; rgw-go uses only the op builders |
| `ReadOp.Operate` reported a positive return as an error, breaking CmpXattr guards and discarding later steps' results | Task 11 review | Fixed in fork commit 15b021a |
| `rados_read_op_stat2` needs Reef or later and was not version-gated | Task 11 review | Fixed |
| No async write with an mtime (`rados_aio_write_op_operate2` unbound), so mtime writes fall back to a blocking call | Task 12 review | Fork follow-up in progress |
| The object-list iterator drops the locator (`rados_nobjects_list_next2` unbound) | Task 12 review | Fork follow-up in progress |
| `rados_aio_cancel` is unbound | Registry audit | Open |
| With several failing steps, the op-level errno is picked from a Go map, so it varies between runs | Task 12 review | Worked around in rgw-go by preferring per-step rvals |
| A failed unwatch removes the watcher but never closes its channels, leaking the dispatch goroutine | Task 12 review | Worked around in rgw-go; fork fix open |
| `OmapCmp` bound to `omap_cmp`, which truncates keys at a NUL byte | Task 11 | Fixed: binds `omap_cmp2` |
| Exec-step output parameters lived in Go memory written after the call returned | Task 11 ruling | Fixed: C-allocated |
