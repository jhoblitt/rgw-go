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
  design target of GOMAXPROCS plus a small constant. Count process-wide: the
  `Threads` field of `/proc/<pid>/status`, which rgw-go exports as
  `rgw_go_process_threads`, not `go_threads`. `go_threads` counts the Go
  runtime's thread records, so it misses every librados thread that never
  calls into Go, the messenger's workers among them
  (`PosixNetworkStack::spawn_worker` starts them as `std::thread`s,
  [PosixStack.h:51](https://github.com/ceph/ceph/blob/v19.2.6/src/msg/async/PosixStack.h#L51)),
  and it counts the spare record cgo keeps for callbacks from C threads,
  which has no thread of its own
  ([`runtime.mstartm0`](https://github.com/golang/go/blob/go1.27.1/src/runtime/proc.go#L1964-L1973)).
  Under Go 1.27.1 a cgo binary with no C threads reports one more
  `go_threads` than the kernel does.

### A parked OS thread per synchronous operation

- **Evidence:** in sync mode each in-flight operation blocks one OS thread in
  a cgo call. The Task 12 integration run added 11 to 14 threads under its
  small load.
- **Mitigation:** the callback and pipe modes held the thread count flat.
- **Measure:** threads and throughput versus concurrency, sync against the two
  async modes. Count from a fresh process: an OS thread the Go runtime no
  longer needs is parked on its idle list for reuse rather than exited
  (`stopm` and `mput`,
  [proc.go:3005-3026](https://github.com/golang/go/blob/go1.27.1/src/runtime/proc.go#L3005-L3026)
  and [:7246-7257](https://github.com/golang/go/blob/go1.27.1/src/runtime/proc.go#L7246-L7257)
  at go1.27.1), so after a warm-up burst a later sync burst often shows no
  rise at all. A thread exits only when a goroutine exits while locked to it
  with `runtime.LockOSThread`: `gdestroy` then unwinds the thread to
  `mstart0`, whose `mexit` ends it rather than return a thread the goroutine
  may have left in an unusual state to the pool
  ([proc.go:4569-4583](https://github.com/golang/go/blob/go1.27.1/src/runtime/proc.go#L4569-L4583),
  [:1901-1910](https://github.com/golang/go/blob/go1.27.1/src/runtime/proc.go#L1901-L1910)).
  `mexit` has no other caller, and its doc names that unwind as the way a
  thread exits
  ([:1989-1993](https://github.com/golang/go/blob/go1.27.1/src/runtime/proc.go#L1989-L1993)).
  The thread ends after its goroutine has returned, so a count taken at that
  moment can still include it. Nothing on the seam's paths locks a thread:
  neither the go-ceph fork nor rgw-go's `internal/` calls `LockOSThread`, so
  there the count only rises, and the seam benchmark counts a cell's idle
  threads before its first call. In the final-review measurements, 512
  concurrent reads rose sync mode by 0 to 70 threads, with 62 to 512 reads in
  flight, while callback and pipe rose by 0 with 350 to 512 in flight.

### The objecter throttle blocks the submitting thread

- **Evidence:** librados budgets every operation against
  `objecter_inflight_ops` (default 1024) and `objecter_inflight_op_bytes`
  (default 100 MiB), both in `src/common/options/global.yaml.in`
  ([v19.2.6](https://github.com/ceph/ceph/blob/v19.2.6/src/common/options/global.yaml.in#L2378-L2389)).
  `Objecter::_op_submit_with_budget` takes the budget inside the submit call
  ([Objecter.cc:2295](https://github.com/ceph/ceph/blob/v19.2.6/src/osdc/Objecter.cc#L2295-L2315)),
  and since `RadosClient::connect` turns on the balanced budget
  ([RadosClient.cc:263](https://github.com/ceph/ceph/blob/v19.2.6/src/librados/RadosClient.cc#L263)),
  `_throttle_op` waits in `Throttle::get` until other operations complete
  ([Objecter.cc:3356-3381](https://github.com/ceph/ceph/blob/v19.2.6/src/osdc/Objecter.cc#L3356-L3381)).
  The asynchronous calls submit on the caller's thread too
  (`IoCtxImpl::aio_operate_read` and `aio_operate` call `op_submit`), so
  once the budget is spent a callback- or pipe-mode submission pins an OS
  thread in its cgo call just as a synchronous one does.

  The byte budget charges a read the length it asks for, whatever the
  object holds, and a class call nothing: `Objecter::calc_op_budget` adds a
  read's extent length and a write-mode step's input
  ([Objecter.cc:3338-3354](https://github.com/ceph/ceph/blob/v19.2.6/src/osdc/Objecter.cc#L3338-L3354)
  at v19.2.6, [:3502-3518](https://github.com/ceph/ceph/blob/v20.2.4/src/osdc/Objecter.cc#L3502-L3518)
  at v20.2.4), and `ceph_osd_op_mode_read` excludes `CEPH_OSD_OP_CALL`
  ([rados.h:401-405](https://github.com/ceph/ceph/blob/v19.2.6/src/include/rados.h#L401-L405),
  [:402-406](https://github.com/ceph/ceph/blob/v20.2.4/src/include/rados.h#L402-L406)).
  radosgw's head read asks for `rgw_max_chunk_size`
  ([rgw_rados.cc:8853](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L8853)
  at v19.2.6, [:9797](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9797)
  at v20.2.4), 4 MiB by default
  ([rgw.yaml.in:84-95](https://github.com/ceph/ceph/blob/v19.2.6/src/common/options/rgw.yaml.in#L84-L95),
  [:84-98](https://github.com/ceph/ceph/blob/v20.2.4/src/common/options/rgw.yaml.in#L84-L98)).
  Reading the head of a 4 KiB object therefore costs 4 MiB of the 100 MiB
  byte budget, and at most 25 head reads fit in flight per process. That is
  radosgw's ceiling as much as rgw-go's: radosgw raises
  `objecter_inflight_ops` to 24576 and leaves the byte budget at its default
  ([rgw_main.cc:83](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_main.cc#L83),
  [v20.2.4](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_main.cc#L83)).
  rgw-go's limiter, sized a sixteenth below the byte budget, allows at most
  23.
- **Status:** mitigated. goceph parks every Read and Write on the Cluster's
  in-flight limiter in Go, sized 16 operations and one sixteenth of the bytes
  below librados's own limits, which it reads through `rados_conf_get` after
  connect. The operation margin is for the calls that take the objecter's
  budget without passing the limiter: each lock call holds one operation
  while it runs, and an object listing one for the whole listing. Watch and
  notify register linger operations, which take none
  (`Objecter::take_linger_budget`), and mon traffic goes through the
  MonClient. The byte margin absorbs what librados budgets
  (`Objecter::calc_op_budget`) but the limiter's weight leaves out: xattr and
  omap payloads and xattr comparisons. The weight is read lengths, write and
  append data and exec inputs; librados budgets no class call at all. An
  abandoned operation keeps its share until its completion fires, since
  `rados_aio_cancel` is unbound.
- **Measure:** throttle waits and thread count under the seam
  microbenchmark, `BenchmarkSeam` in `test/bench/seam`; the head read's cost
  in its `shape=headread4k` cells, whose `budget_bytes` is the 4 MiB the head
  read asks for, so the default `-inflight-bytes` of 96 MiB skips those above
  24 in flight.

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
- **Status:** worked around by the design's read-op route, which depends on
  the OSD accepting a modifying method in a read op. The goceph integration
  spec confirms it: cls hello write_return_data run in a ReturnVec ReadOp
  returns its output and its xattr persists. The route is bounded: the OSD
  serves such an op as a write, so without ReturnVec it returns no step's
  output, and with ReturnVec it fails the op with EOVERFLOW, applying
  nothing, when any step's output passes `osd_max_write_op_reply_len`, 64
  bytes by default (PrimaryLogPG::execute_ctx, `src/osd/PrimaryLogPG.cc:4210-4246`
  at v19.2.6, `:4287-4323` at v20.2.4). Found in the M Task 1 review;
  fakerados models both.

### The locator is per I/O context, not per operation

- **Evidence:** `rados_ioctx_locator_set_key` sets the locator on an I/O
  context. RGW sets a locator on every object whose name starts with `_`, so
  the client juggles I/O contexts per operation. Task 12's first version cached
  one context per locator, which grew without bound under user control.
- **Status:** pooled contexts with the locator set per operation
  (goceph/pool.go).
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

### A read needs its buffer sized up front

- **Evidence:** `rados_read_op_read` reads into a buffer of the length its
  caller gives, and go-ceph passes a Go buffer of that length. librados's
  C++ `read(0, 0)`, with which radosgw reads a whole system object into a
  bufferlist (`driver/rados/config/impl.cc:46-60` at v19.2.6), has no C
  equivalent: a zero length reads into an empty buffer, which librados
  fails with ERANGE when data remains (`radosclient.ReadStep`). A caller
  reading an object of unknown size therefore stats it first or reads into
  a buffer as large as the object can be. The driver reads each realm,
  period, zonegroup and zone object into a 4 MiB buffer (`rootObjectMax`),
  which goceph allocates in full for every such read.
- **Status:** accepted for the few reads at startup; a path that reads
  small objects often needs a size it knows, a stat, or a pooled buffer.
- **Measure:** allocation per read against the object's size.

### Cancellation stops only the client

- **Evidence:** `rados_aio_cancel` makes the Objecter drop the operation with
  `-ECANCELED` (IoCtxImpl::aio_cancel). A write already sent to the OSD may
  still apply. Until the completion fires, Go buffers stay pinned and the I/O
  context must stay open. A pool closed with an abandoned operation in flight
  segfaulted in Task 12 before in-flight counting was added.
- **Note:** a pure-Go client has the same wire-level limit on sent writes, but
  owns its buffers and needs no pinning or reaper.

### The librados at run time sets which cephx keys authenticate

- **Evidence:** Ceph 19.2.6 and 20.2.4 are the first releases whose cephx
  can parse AES256KRB5 keys, and an older librados fails `rados_connect`
  with such a key (`input/output error`), which is what the first
  integration workflow runs hit with noble's librados 19.2.3. Such keys are
  the default: Ceph's mkfs at those releases allows only AES256KRB5, and
  Rook, which allows both AES and AES256KRB5, makes AES256KRB5 the
  preferred type that new daemon keys, the RGW's included, take unless
  `cephx.daemon.keyType` overrides it. rgw-go therefore supports only
  librados 19.2.6 or later on Squid, or 20.2.4 or later on Tentacle. That is
  a support policy; technically the floor binds only when the key presented
  is AES256KRB5. It is a client floor, not a cluster floor: upgraded
  clusters keep their AES keys working. Under Rook the derived image is
  every daemon's image, so rgw-go's librados is the cluster's own release
  and meets the floor whenever the cluster can issue such keys. The headers
  used for building are unaffected.
- **Note:** a pure-Go client removes the coupling to the installed library
  only by implementing the AES256KRB5 cipher itself, RFC 8009
  AES256-CTS-HMAC-SHA384-192, wherever cephx uses a key: entity keys,
  tickets, authorizers and session keys. It then carries that work, and
  every future cipher change, instead of taking it from Ceph.
- **Detection:** `rados_version` reports the librados API version (3.0.0
  on every release), not the Ceph release. `goceph.Connect` reads the release
  from libceph-common's `ceph_version_to_str` by its mangled C++ symbol. It
  refuses a pre-Squid library up front and otherwise uses the release only to
  explain a failed connect. If the symbol disappears, both are skipped with a
  warning; the connect itself is unaffected.
- **Status:** accepted; the integration workflow installs librados 20.2.4
  from download.ceph.com.

### Build and deployment cost

- **Evidence:** every build needs librados headers and the shared library.
  `CGO_ENABLED=0` and cross-compilation are impossible, so the goreleaser
  config must change once `cmd/rgw-go` imports the client. CI installs
  librados-dev. Local sandboxed builds need `CCACHE_DISABLE=1`. Building the
  derived image adds two more costs:
  - A builder image per Ceph release (`hack/image/Containerfile.builder`),
    because the binary must link against the librados and glibc of the Ceph
    image it runs in: that image plus the `librados-devel` build matching the
    image's librados2, `git-core` and the Go toolchain. Building one needs a
    container engine and download.ceph.com, and adds about 305 MB over the
    Ceph image: 304.8 MB over `quay.io/ceph/ceph:v19.2.6` and 305.1 MB over
    `v20.2.4`, of which the Go toolchain is 257 MB and the package install
    48.1 MB (measured with podman 5.8.4).
  - A Go build cache per librados. Go's build cache "does not detect changes
    to C libraries imported with cgo" (`go help cache`, go1.27.1), and both
    floor images carry the same gcc, 11.5.0-15.el9, so a cache shared by
    builds against Squid's and Tentacle's librados could hand one release
    go-ceph objects compiled against the other's headers.
    `hack/image/cgo-build.sh` keeps one cache per builder.
- **Note:** a pure-Go client removes both: one `CGO_ENABLED=0` binary, built
  on any host with an ordinary build cache, would serve every release. The
  derived image stays one per release, because under Rook it is the Ceph
  image every daemon runs.

## RADOS semantics that shape the client

These come from the OSD, not from cgo, so a pure-Go client has them too.

### A read in the same operation sees the object as it was before the operation

- **Evidence:** a ReadOp can run a modifying class method with RETURNVEC and
  the change persists, but a later read step in the same op returns the
  pre-op value (replicated pools read attrs from the object store;
  PrimaryLogPG getattr_maybe_cache). Class clients therefore rely on a
  method's own output, never on reading back its effect.
- **Status:** found in Task 12, confirmed on Squid and Tentacle.

### Two modifying class methods in one operation both see the pre-op state

- **Evidence:** the later one's write wins, e.g. two rgw_gc queue enqueues in
  one op keep only the second entry.
- **Status:** found in Task 14 (gc), confirmed on Squid.

### A class method run in a read operation leaves the object's mtime unchanged

- **Evidence:** radosgw runs reset_user_stats2 in an ObjectWriteOperation;
  rgw-go runs it in a ReturnVec ReadOp so it can return data, and a read op
  carries no mtime (PrimaryLogPG.cc). Harmless today because nothing reads
  `<user>.buckets`' mtime.
- **Status:** found in Task 14 (user) review.

## Binding

| Limitation | Found | Status |
|---|---|---|
| Empty or nil buffers panicked in `WriteOp.SetXattr` and the steps behind `Write`, `WriteFull`, `WriteSame`, `CmpExt` and `ReadOp.Read` (`&b[0]` with no length check) | Task 12 planning | Fixed on the fork's `rgw-go` branch by ceph/go-ceph#1345 (jhoblitt/go-ceph#5); upstream PR open |
| The same unguarded `&b[0]` in `IOContext.WriteFull`, `Append`, `GetXattr`, `SetXattr`, the ioctx buffer loops, the striper and `Checksum` | Fork follow-up | Fixed by the same ceph/go-ceph#1345, which also covers these sites and the striper |
| `ReadOp.Operate` reported a positive return as an error, breaking CmpXattr guards and discarding later steps' results | Task 11 review | Fixed on `rgw-go` by ceph/go-ceph#1344 (jhoblitt/go-ceph#6); upstream PR open |
| go-ceph's WriteOp reports a positive librados return as an error, although the OSD committed the write | Task 14 (rgw guard sign experiment) | Fixed on `rgw-go` by ceph/go-ceph#1344 (jhoblitt/go-ceph#6), sync and async; the goceph workaround stays harmless |
| `rados_read_op_stat2` needs Reef or later and was not version-gated | Task 11 review | Fixed |
| No async write with an mtime (`rados_aio_write_op_operate2` unbound), so mtime writes fall back to a blocking call | Task 12 review | Fixed on `rgw-go` (jhoblitt/go-ceph#12, fork-only): `WriteOp.OperateAsyncWithMtime`, Reef or later |
| The object-list iterator drops the locator (`rados_nobjects_list_next2` unbound) | Task 12 review | Fixed on `rgw-go` (jhoblitt/go-ceph#13, fork-only): `Iter.Locator()` |
| `rados_aio_cancel` is unbound | Registry audit | Open |
| `Iter.Token()` returns the listing cursor's hash position, which the Objecter moves past each fetched batch while librados hands the batch out one entry at a time, so a token taken mid-batch resumes after entries not yet returned; it is not a per-entry resume marker | Phase 1 planning (admin API) | Worked around: `Pool.ListObjectsFrom` seeks to the last object's placement hash and skips same-hash entries up to it |
| With several failing steps, the op-level errno is picked from a Go map, so it varies between runs | Task 12 review | Fixed on `rgw-go` by ceph/go-ceph#1344: step errors are reported in step order; the rgw-go workaround stays harmless |
| A failed unwatch removes the watcher but never closes its channels, leaking the dispatch goroutine | Task 12 review | Fixed on `rgw-go` by ceph/go-ceph#1341 (jhoblitt/go-ceph#2): `Delete` closes the channels even when the unwatch fails; the rgw-go workaround stays harmless |
| `OmapCmp` bound to `omap_cmp`, which truncates keys at a NUL byte | Task 11 | Fixed: binds `omap_cmp2` |
| Exec-step output parameters lived in Go memory written after the call returned | Task 11 ruling | Fixed on `rgw-go` by ceph/go-ceph#1343 (jhoblitt/go-ceph#8): C-allocated; the `ReadOp.Read` buffer is pinned too |
| After a successful unwatch, a watch callback that looked the watcher up before `Delete` can reach its `select` after `done` and `events` are closed and pick the send on the closed channel, which panics on a librados thread (go-ceph watcher.go:355-358); fix: close(done), rados_watch_flush, then close the channels. `watchErrorCb` has the same window on the errors channel | Task 12 review; final review | Fixed on `rgw-go` by ceph/go-ceph#1341 (jhoblitt/go-ceph#2): close `done`, `rados_watch_flush`, then close `events` and `errors` |
| (rgw-go seam, not go-ceph) `radosclient.ExecResult` reports a positive class return as success but does not surface the value | Task 12 | Open; add an accessor if a class client needs the value |
