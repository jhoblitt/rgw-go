# Phase 1 Unit G: Gateway Core Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the gateway shell every other phase-1 unit plugs into: the frozen op contract and store interfaces with fakes, radosgw-argv configuration bridged through librados, the beast-spec HTTP and TLS frontend with per-request deadlines and drain, S3 request parsing and table dispatch rendering radosgw's error documents, minimal Prometheus metrics, the driver skeleton, and a `cli serve` that starts under Rook's exact argv, connects through librados, and answers Rook's readiness probe.

**Architecture:** `internal/op` owns the protocol-neutral request, identity, error set, op lifecycle and the store interfaces, declared at the consumer with counterfeiter fakes beside them and a working in-memory implementation in `internal/memstore` for every unit's specs. `internal/s3` parses Host, path and query once and dispatches on method, scope and subresource in radosgw's precedence to a handler table the other units fill; a route without a handler answers 501 NotImplemented in radosgw's XML. `internal/frontend` turns the beast frontend spec into `http.Server`s with zero server timeouts and per-request deadlines through `http.ResponseController`. `internal/cephconf` does what ceph's `global_init` does before librados takes over. `internal/driver` is a skeleton that implements every store interface with NotImplemented until M, R, W, P and N fill it in. Every op is a stub except ListBuckets, which is the pipeline no-op baseline and the probe path.

**Tech Stack:** Go 1.27, cgo through ceph/go-ceph replaced by the jhoblitt/go-ceph fork at cbf97f85fcf5 (branch `rgw-go`), cobra, viper, log/slog, net/http and crypto/tls, golang.org/x/sync (errgroup, semaphore), github.com/prometheus/client_golang, Ginkgo v2 and Gomega, counterfeiter, rooket disposable clusters (hack/rooket).

**Spec:** `docs/superpowers/specs/2026-09-25-rgw-go-design.md` (§4 architecture, §5 configuration and invocation, §6 data path, §7 concurrency, §2 priorities), read with the index's background in `docs/superpowers/plans/2026-09-29-phase-1/00-index.md` (unit G in "The nine units", "Shared interfaces and seam gaps", decisions D4, D8, D9 in "Spec ambiguities and decisions D1-D11") and `docs/exclusions.md` for scope and coexistence obligations. Every claim below about radosgw was checked against the ceph checkout at 7ed73efc1be (main, 2026-09-25) and about Rook against dc7829268; file:line references are to those trees.

## Global Constraints

- Module `github.com/jhoblitt/rgw-go`; `go 1.27` in `go.mod`, minor only, no `toolchain` line; every `go` command carries `-tags=ceph_preview` (the Makefile's `GO_TAGS`).
- Layout per the Go canon: `cmd/rgw-go/main.go` is `os.Exit(run())` plus the signal context and nothing else; everything else under `internal/`; package names one lowercase word; no `util`, `common` or `helpers`.
- Dependencies point downward and the third-party list is complete: go-ceph, cobra, viper, klauspost/compress, the Prometheus client, x/sync, Ginkgo and Gomega, counterfeiter, plus aws-sdk-go-v2 and go-ceph's rgw/admin as test clients. `radosclient/goceph` stays the only package importing go-ceph; `op` imports `meta`, `acl`, `policy`, `cephconf`, `radosclient` and nothing above them; `s3`, `frontend`, `driver`, `metrics` import `op`; `cli` imports all of them. `xmltext` (Task 4) imports only the standard library, so every package that writes XML, `meta` and N's `formatter` included, may import it.
- Store interfaces live in `op`, split by concern, with counterfeiter fakes generated into `internal/op/opfakes` and committed; `make generate-check` fails a stale fake.
- Tests are Ginkgo v2 and Gomega only, one `<pkg>_suite_test.go` per package with `RandomizeAllSpecs` and `FailOnPending`; integration specs carry `//go:build integration` and `Label("integration")` and read the cluster from `RGW_GO_TEST_CEPH_CONF` through `internal/testutil/cephtest`.
- Logging is `log/slog`, JSON to stderr, static lowercase messages, snake_case attribute keys, the `…Context` variants wherever a `ctx` is in scope; never a secret at any level.
- Errors wrap with `%w`, lowercase, unpunctuated; `errors.Is`/`errors.As` only; every goroutine has a stop condition; fan-out is `errgroup.WithContext`.
- Ceph config is the single source for everything radosgw configures. rgw-go's own flags are exactly `--config`, `--log-level`, `--log-format`, `--metrics-addr` and `--rados-completions`; every `rgw_*` value is read back through librados after connect.
- No path router. S3 dispatch is a table matched on method, scope and subresource in radosgw's precedence after Host, path and query are parsed once.
- `http.Server` runs with zero `ReadTimeout` and `WriteTimeout`; deadlines are per request through `http.ResponseController`, because objects are large. One goroutine per request.
- The in-flight RADOS limiter is transport-owned, in `radosclient/goceph`, sized under librados's objecter throttle (decision D8). `rgw_max_concurrent_requests` is the frontend's separate hard cap: excess requests answer 503 SlowDown.
- Metrics are the minimal Prometheus surface of decision D9: per-op counts and latency, in-flight requests, RADOS ops and bytes; asok and the ops log are phase 3.
- rgw-go never creates a pool (decision D4); a missing pool is a configuration error.
- Byte-compatibility with radosgw is the acceptance test: header names, error documents, transaction-id shape and dispatch precedence are copied from the C++ sources cited in each task, not designed.
- Startup order is radosgw's: connect to RADOS as the launching user, bind every listener (privileged ports included), then drop to `setuser`/`setgroup`, then serve. radosgw defers the drop with `CINIT_FLAG_DEFER_DROP_PRIVILEGES` ([`rgw_main.cc:100-102`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_main.cc#L100-L102)), connects in `init_storage` ([`:143`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_main.cc#L143)) before `init_frontends2` ([`:165`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_main.cc#L165)), and drops inside `AsioFrontend::init` after `bind` ([`rgw_asio_frontend.cc:566-590`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_asio_frontend.cc#L566-L590), `:756`). The spec's §5 sentence "privileges drop before connecting" is wrong and is being corrected; this plan follows the code.
- Never touch the ambient Kubernetes or Ceph cluster. Cluster tasks use the rooket clusters (`make cluster-up RELEASE=squid`, `make populate RELEASE=squid`) and are marked `[cluster]`.
- The librados headers on this machine are absent: cgo builds use `CGO_CFLAGS=-I/home/jhoblitt/github/ceph/src/include` and `CGO_LDFLAGS=-L$HOME/.local/lib/rgw-go`; module downloads need the sandbox disabled.
- Commits are Conventional Commits with the two repository trailers; each task ends in a draft PR assigned to the author, CI watched, merged on green with a merge commit under the standing authorization. Any material change to `docs/exclusions.md` is reported so the rgw-rs session can be told.

## Review Focus

1. **A Host header with a port, an IPv6 literal or a bare IP address.** radosgw strips `:port` and `[…]` before matching hostnames and never takes an IP literal as a virtual-hosted bucket (`rgw_rest.cc:2027-2040`, `:2112-2119`); a parser that matches the raw header serves the wrong bucket or 400s every request through a port-forward. Pinned in Task 3.
2. **A path with a NUL or a percent-encoding that must decode exactly once.** `%00` anywhere in the decoded path is 400 InvalidRequest (`ERR_ZERO_IN_URL`), `%2F` in a key is one key with a slash, and `+` in the path is a literal plus while `+` in the query is a space (`rgw_common.cc:1790-1822`). Pinned in Task 3.
3. **A client that stalls or disconnects mid-body or mid-response.** The per-request deadline must fire, the request context must cancel, and the handler goroutine must exit without leaking; with zero server timeouts nothing else bounds it. Pinned in Task 5.
4. **More concurrent requests than `rgw_max_concurrent_requests`.** The excess answers 503 SlowDown with the S3 error body and `x-amz-request-id`, never a bare 503 or a hang, because Rook's probe reads 503 as healthy throttling ([`rgw-probe.sh:42-49`](https://github.com/rook/rook/blob/v1.20.7/pkg/operator/ceph/object/rgw-probe.sh#L42-L49)). Pinned in Task 4.
5. **A signed request reaching the interim binary before unit A lands.** A request carrying `Authorization`, `X-Amz-Signature`, `X-Amz-Credential`, `AWSAccessKeyId` or `Signature` must never be served as anonymous; the stub answers 501 NotImplemented. Pinned in Task 4.
6. **XML text escaped by Go's rules instead of radosgw's.** `encoding/xml` writes `&#34;` and `&#39;`, escapes tab, newline and carriage return, and replaces other control bytes and invalid UTF-8 with U+FFFD, where radosgw's `XMLFormatter` writes `&quot;` and `&apos;`, copies tab and newline and every byte from 0x80 up, and writes `&#x01;` for a control byte (`xml_stream_escaper`, [`src/common/escape.cc:134-169`](https://github.com/ceph/ceph/blob/v19.2.6/src/common/escape.cc#L134-L169)); a key holding such a byte would be listed under another name, and every quoted ETag would differ. Pinned in Task 4: every string an S3 document carries is an `xmltext.Text`, hand-written markup escapes through `xmltext.Escape`, and `xmltext`'s specs cover each class of byte.
7. **`Accept-Ranges` on the wrong responses.** radosgw sends it only where `dump_content_length` runs: every error document, and a success whose op names its length (GET and HEAD of an object, a PUT of an object or part without a copy source); every other success, the XML documents included, carries at most the `Content-Length` radosgw's frontend adds and no `Accept-Ranges`. Pinned in Task 4: the response writer adds nothing, `WriteError` and `SetContentLength` are the only places the header is set, and the specs pin both sides.

---

## File structure

```
cmd/rgw-go/main.go                       modify: cli.Run(ctx, cli.Argv(os.Args), ...)
internal/cli/root.go                     modify: serve flags, Argv (radosgw-argv rewrite)
internal/cli/serve.go                    connect, options, driver, handler, frontend, metrics, drain
internal/cephconf/{doc,options,early,apis,privileges}.go
internal/frontend/{doc,spec,tls,server,deadline}.go
internal/policy/{doc,action}.go          the s3:* action enumeration; Z adds Parse and Eval
internal/op/{doc,identity,request,errors,op,sink,env,authz,transid}.go
internal/op/{zone,user,bucket,object,multipart,stats,usage,metadata}.go   store interfaces
internal/op/listbuckets.go               the one real op in G
internal/op/opfakes/                     counterfeiter fakes, committed
internal/memstore/{doc,store,user,bucket,object,multipart,stats,usage,metadata}.go
internal/s3/{doc,request,dispatch,handler,errors,headers,xml,listbuckets,auth}.go
internal/xmltext/xmltext.go              ceph's XML text escaper and the Text type every S3 document uses
internal/metrics/{doc,metrics}.go
internal/driver/{doc,store,run}.go       skeleton: Open, Store, Run
internal/radosclient/cluster.go          modify: InstanceID, Stats, StatsReporter
internal/radosclient/goceph/{cluster,pool}.go   modify: NoConfigFile, InstanceID, Stats
internal/radosclient/goceph/limiter.go   the in-flight op and byte limiter (D8)
docs/cgo-limitations.md                  modify: objecter throttle entry
docs/exclusions.md                       modify: frontend differences in the coexistence section
```

## Task index

| # | Task | Depends on | Cluster |
|---|---|---|---|
| 1 | Interface freeze: `op`, `policy.Action`, `cephconf.Options`, `frontend.Spec`, fakes, `memstore` | none | no |
| 2 | Seam prerequisites: `InstanceID`, `Stats`, `NoConfigFile`, the in-flight limiter | none | integration spec `[cluster]` |
| 3 | `s3` request parsing and the dispatch table | 1 | no |
| 4 | `s3` handler: lifecycle runner, error documents, headers, transaction ids, concurrency cap, `op.ListBuckets`; `xmltext` | 1, 3 | no |
| 5 | `frontend`: listeners, TLS, deadlines, drain | 1 | no |
| 6 | `cephconf` startup: early args, enabled APIs, privilege drop; `cli.Argv` | 1 | no |
| 7 | `metrics` | 1, 2 | no |
| 8 | `driver` skeleton: `Open`, `Store`, `Run` | 1, 2 | no |
| 9 | `cli serve` end to end | 2 to 8 | `[cluster]` |

Task 1 is the interface freeze the index's dependency order names as the gate for wave 1 (`00-index.md`, "Dependency order"): A, Z and M dispatch once it merges. Tasks 2 to 8 are independent of each other after Task 1 and run in parallel. The Frozen interface contract appendix at the end lists every exported name other units import; its text is normative for Tasks 1 to 8 and for the wave-1 plans.

---

### Task 1: Interface freeze

**Files:**
- Create: `internal/op/doc.go`, `internal/op/identity.go`, `internal/op/request.go`, `internal/op/errors.go`, `internal/op/op.go`, `internal/op/sink.go`, `internal/op/env.go`, `internal/op/authz.go`, `internal/op/transid.go`, `internal/op/zone.go`, `internal/op/user.go`, `internal/op/bucket.go`, `internal/op/object.go`, `internal/op/multipart.go`, `internal/op/stats.go`, `internal/op/usage.go`, `internal/op/metadata.go`, `internal/op/op_suite_test.go`, `internal/op/errors_test.go`, `internal/op/op_test.go`, `internal/op/request_test.go`, `internal/op/transid_test.go`, `internal/op/opfakes/` (generated)
- Create: `internal/policy/doc.go`, `internal/policy/action.go`, `internal/policy/policy_suite_test.go`, `internal/policy/action_test.go`
- Create: `internal/cephconf/doc.go`, `internal/cephconf/options.go`, `internal/cephconf/cephconf_suite_test.go`, `internal/cephconf/options_test.go`
- Create: `internal/frontend/doc.go`, `internal/frontend/spec.go`, `internal/frontend/frontend_suite_test.go`, `internal/frontend/spec_test.go`
- Create: `internal/memstore/doc.go`, `internal/memstore/store.go`, `internal/memstore/user.go`, `internal/memstore/bucket.go`, `internal/memstore/object.go`, `internal/memstore/multipart.go`, `internal/memstore/stats.go`, `internal/memstore/usage.go`, `internal/memstore/metadata.go`, `internal/memstore/memstore_suite_test.go`, `internal/memstore/user_test.go`, `internal/memstore/bucket_test.go`, `internal/memstore/object_test.go`, `internal/memstore/multipart_test.go`, `internal/memstore/stats_test.go`

**Interfaces:**
- Consumes: `meta` (UserInfo, AccountInfo, Owner, UserID, Caps, BucketID, BucketEntryPoint, BucketInfo, BucketEnt, ObjKey, Manifest, CompressionInfo, ObjVersion, Zone, ZoneGroup, ZoneParams, Realm, Period, PlacementRule, Pool, Quota, Time), `acl.Permission`, `denc.Release`, `radosclient` (the error sentinels and `*radosclient.Error`).
- Produces: every name in the Frozen interface contract appendix for packages `op`, `policy`, `cephconf` (`Getter`, `MapGetter`, `Options`), `frontend` (`Spec`, `ParseBeast`, `ParseFrontends`) and `memstore`. The appendix is the authoritative text; this task's code blocks are the same declarations with their bodies.

This task is the freeze the dependency order puts first (`00-index.md`, "The first unit"). It has two deliverables that land as two commits in one PR: the contract (`op`, `policy`, `cephconf.Options`, `frontend.Spec`, the fakes) and the in-memory store that implements the contract and proves it self-consistent. Wave 1 may dispatch as soon as the PR merges.

Design resolutions this task fixes, where the interface sketch that preceded this contract (`00-index.md`, "Shared interfaces and seam gaps") was open (each is repeated in the appendix so a wave-1 planner never has to read this task):

- `Identity` carries no parsed policies. radosgw parses a user's identity policies from the user's xattrs on every request (`rgw_common.cc`, `get_iam_identity_policies_from_attr`), so `Identity.Attrs` holds the user's stored xattrs and Z decodes them; `op` never imports a policy document type.
- The `Object` of a request is a `meta.ObjKey` (name plus the `versionId` instance), not a string, because every store method takes an `ObjKey`.
- Authorization is an interface, `op.Authorizer`, consumed by the ops through three package functions (`VerifyUserPermission`, `VerifyBucketPermission`, `VerifyObjectPermission`), which delegate to `Request.Env.Authz`. Z ships the implementation; G ships `OwnerOnly`, the permissive development stub the dependency order allows (`00-index.md`, "Dependency order": "ops run with a permissive stub").
- Authentication is consumed by `s3`, so `s3.Authenticator` is declared there (Task 4); its result type `op.AuthResult` lives in `op` so `auth` and `s3` share it without `s3` importing `auth`.
- Authentication takes the dispatched route's `op.PayloadForms`, because radosgw dispatches before it authenticates (`rgw_process.cc:325`, `:341`, [`:345`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_process.cc#L345) at v19.2.6) and refuses, inside authentication, a signed payload form the op type does not take (`get_auth_data_v4`, [`rgw_rest_s3.cc:5899-5969`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L5899-L5969); owner decision 8). The type lives in `op` so that `auth` never imports `s3` and the admin handler can pass its own routes' forms; Task 3's dispatch table carries each route's forms per release.
- `Op` gains `OpMask() uint32`, because radosgw checks the user's op mask between loading permissions and `verify_permission` (`rgw_process.cc`, `rgw_process_authenticated`), and the check belongs to the shared runner `op.Run`, not to each op.
- `ObjectState` carries its `Bucket *BucketRecord`, because `ReadObject` and `SetObjectAttrs` take only the state.
- Versions are `meta.ObjVersion` (the copy `meta` already keeps so it does not import `cls/version`), never `version.ObjVersion`.
- The op-level metrics hook is a two-method interface `op.Metrics` so `op` never imports Prometheus.

- [ ] **Step 1: Write the failing specs for the error set and `FromRADOS`**

`internal/op/op_suite_test.go` is the canon bootstrap (`TestOp`, `RandomizeAllSpecs`, `FailOnPending`). `internal/op/errors_test.go`, package `op_test`:

```go
var _ = Describe("Error", func() {
	It("renders the S3 code and message", func() {
		Expect(op.ErrNoSuchBucket.Error()).To(Equal("NoSuchBucket"), "bare")
		Expect(op.ErrNoSuchBucket.WithMessage("gone").Error()).To(Equal("NoSuchBucket: gone"), "with message")
	})
	It("matches by code and status through errors.Is, message aside", func() {
		err := fmt.Errorf("loading bucket: %w", op.ErrNoSuchBucket.WithMessage("x"))
		Expect(err).To(MatchError(op.ErrNoSuchBucket), "wrapped copy")
		Expect(errors.Is(op.ErrNoSuchKey, op.ErrNoSuchBucket)).To(BeFalse(), "different code")
	})
	DescribeTable("carries radosgw's status and rgw_err number",
		func(e *op.Error, code string, status, errno int) {
			Expect(e.Code).To(Equal(code))
			Expect(e.Status).To(Equal(status))
			Expect(e.Errno).To(Equal(errno))
		},
		Entry("NoSuchKey is ENOENT", op.ErrNoSuchKey, "NoSuchKey", 404, 2),
		Entry("NoSuchBucket is ERR_NO_SUCH_BUCKET", op.ErrNoSuchBucket, "NoSuchBucket", 404, 2002),
		Entry("AccessDenied is EACCES", op.ErrAccessDenied, "AccessDenied", 403, 13),
		Entry("SlowDown is ERR_RATE_LIMITED", op.ErrSlowDown, "SlowDown", 503, 2218),
		Entry("NotImplemented", op.ErrNotImplemented, "NotImplemented", 501, 2201),
		Entry("MethodNotAllowed", op.ErrMethodNotAllowed, "MethodNotAllowed", 405, 2003),
		Entry("the 408 RequestTimeout is ETIMEDOUT", op.ErrRequestTimedOut, "RequestTimeout", 408, 110),
		Entry("the 400 RequestTimeout is ERR_REQUEST_TIMEOUT", op.ErrRequestTimeout, "RequestTimeout", 400, 2010),
		Entry("BucketNotEmpty is ENOTEMPTY", op.ErrBucketNotEmpty, "BucketNotEmpty", 409, 39),
		Entry("UnknownError is radosgw's fallback", op.ErrUnknown, "UnknownError", 500, 0),
	)
	It("shares a code and status only where radosgw's own table has duplicate rows, and never an errno", func() {
		// rgw_http_s3_errors at v19.2.6 (rgw_common.cc:51-144; v20.2.4 :52-142 adds one
		// row) carries the same {status, code} under several ret values: InvalidRequest/400
		// for ERR_INVALID_REQUEST (:62), ERR_INVALID_CORS_RULES_ERROR (:82),
		// ERR_INVALID_WEBSITE_ROUTING_RULES_ERROR (:83) and ERR_ZERO_IN_URL (:136, folded
		// into ErrInvalidRequest here); AccessDenied/403 for EACCES (:88), EPERM (:89,
		// folded into ErrAccessDenied) and ERR_MFA_REQUIRED (:96); NoSuchEntity/404 for
		// ERR_NO_ROLE_FOUND (:105) and ERR_NO_SUCH_ENTITY (:108); NoSuchCORSConfiguration/404
		// for ERR_NO_CORS_FOUND (:106) and ERR_NO_SUCH_CORS_CONFIGURATION (:109).
		// ServiceUnavailable/503 (:133-134) and InsufficientCapacity/507 (:142-143) are
		// doubled there too, but FromRADOS folds EBUSY, EDQUOT and ENOSPC into one
		// sentinel each. Errors.Is matches within a duplicate group (it compares code and
		// status), so a spec that must tell two of them apart compares Errno or identity.
		want := map[string]int{"InvalidRequest/400": 3, "AccessDenied/403": 2, "NoSuchEntity/404": 2, "NoSuchCORSConfiguration/404": 2}
		got := map[string]int{}
		errnos := map[int]string{}
		for _, e := range op.Errors() {
			got[fmt.Sprintf("%s/%d", e.Code, e.Status)]++
			Expect(errnos).NotTo(HaveKey(e.Errno), "%s shares errno %d with %s", e.Code, e.Errno, errnos[e.Errno])
			errnos[e.Errno] = e.Code
		}
		for k, n := range got {
			Expect(n).To(Equal(max(1, want[k])), "%s appears %d times", k, n)
		}
	})
})

var _ = Describe("FromRADOS", func() {
	DescribeTable("maps a seam error to the S3 error the scope implies",
		func(in error, scope op.Scope, want *op.Error) {
			got := op.FromRADOS(in, scope)
			Expect(got).To(MatchError(want), "mapped")
			Expect(got).To(MatchError(in), "cause kept")
			Expect(op.AsError(got)).To(BeIdenticalTo(want), "AsError")
		},
		Entry("ENOENT on a bucket", radosclient.ErrNotFound, op.ScopeBucket, op.ErrNoSuchBucket),
		Entry("ENOENT on an object", radosclient.ErrNotFound, op.ScopeObject, op.ErrNoSuchKey),
		Entry("ENOENT on a user", radosclient.ErrNotFound, op.ScopeUser, op.ErrNoSuchUser),
		Entry("ENOENT on an upload", radosclient.ErrNotFound, op.ScopeUpload, op.ErrNoSuchUpload),
		Entry("ENOENT at service scope is radosgw's ENOENT row", radosclient.ErrNotFound, op.ScopeService, op.ErrNoSuchKey),
		Entry("EEXIST on a bucket", radosclient.ErrExists, op.ScopeBucket, op.ErrBucketAlreadyExists),
		Entry("EEXIST on a user", radosclient.ErrExists, op.ScopeUser, op.ErrUserAlreadyExists),
		Entry("EPERM", radosclient.ErrPermission, op.ScopeObject, op.ErrAccessDenied),
		Entry("EINVAL", radosclient.ErrInvalid, op.ScopeObject, op.ErrInvalidArgument),
		Entry("ERANGE", radosclient.ErrRange, op.ScopeObject, op.ErrInvalidRange),
		Entry("ETIMEDOUT", radosclient.ErrTimedOut, op.ScopeObject, op.ErrRequestTimedOut),
		Entry("ENOSPC", radosclient.ErrNoSpace, op.ScopeObject, op.ErrInsufficientCapacity),
		Entry("ECANCELED is radosgw's 409 ConcurrentModification row", radosclient.ErrCanceled, op.ScopeObject, op.ErrConcurrentModification),
		Entry("busy resharding is unmapped too", radosclient.ErrBusyResharding, op.ScopeObject, op.ErrUnknown),
		Entry("a raw ENOTEMPTY", &radosclient.Error{Errno: 39, Op: "remove"}, op.ScopeBucket, op.ErrBucketNotEmpty),
		Entry("a raw EBUSY", &radosclient.Error{Errno: 16}, op.ScopeBucket, op.ErrServiceUnavailable),
		Entry("a raw EDQUOT", &radosclient.Error{Errno: 122}, op.ScopeObject, op.ErrInsufficientCapacity),
		Entry("a raw EACCES", &radosclient.Error{Errno: 13}, op.ScopeObject, op.ErrAccessDenied),
		Entry("a closed seam is internal", radosclient.ErrClosed, op.ScopeObject, op.ErrInternalError),
		Entry("a cancelled context", context.Canceled, op.ScopeObject, op.ErrRequestTimedOut),
	)
	It("passes an S3 error through untouched", func() {
		err := fmt.Errorf("x: %w", op.ErrQuotaExceeded)
		Expect(op.FromRADOS(err, op.ScopeObject)).To(BeIdenticalTo(err))
	})
	It("maps nil to nil", func() {
		Expect(op.FromRADOS(nil, op.ScopeObject)).To(Succeed())
	})
	It("AsError turns an unknown error into InternalError", func() {
		Expect(op.AsError(errors.New("boom"))).To(BeIdenticalTo(op.ErrInternalError))
	})
})
```

- [ ] **Step 2: Run to verify the suite fails to compile**

```sh
go test -tags=ceph_preview ./internal/op/
```
Expected: FAIL, undefined `op.ErrNoSuchBucket` and friends.

- [ ] **Step 3: Implement `errors.go`**

The table is `rgw_http_s3_errors` in [`src/rgw/rgw_common.cc:46-146`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L46-L146) with the numbers from `src/rgw/rgw_common.h:284-380`; `Errno` is `rgw_err::ret`'s magnitude, kept so the ops log (phase 3) and this test can cross-check the table. Write every row; the names below are the frozen Go names.

```go
package op

// Error is radosgw's rgw_err: the S3 error code, its HTTP status and an
// optional message. Sentinels are compared with errors.Is, which matches the
// code and status and ignores the message, so an op may return
// ErrX.WithMessage("...") and callers still match ErrX.
type Error struct {
	Code    string
	Status  int
	Message string
	// Errno is the magnitude of rgw_err::ret radosgw would carry: an errno or
	// an ERR_* number from rgw_common.h. It is informational.
	Errno int
}

func (e *Error) Error() string {
	if e.Message == "" {
		return e.Code
	}
	return e.Code + ": " + e.Message
}

// Is reports whether target is an *Error with the same code and status. The
// sentinels that reproduce radosgw's duplicate table rows (InvalidRequest/400,
// AccessDenied/403, NoSuchEntity/404, NoSuchCORSConfiguration/404) therefore
// match one another; Errno tells them apart.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Code == e.Code && t.Status == e.Status
}

// WithMessage returns a copy of e carrying msg as its message.
func (e *Error) WithMessage(msg string) *Error {
	c := *e
	c.Message = msg
	return &c
}

// The S3 error set, one sentinel per row of radosgw's rgw_http_s3_errors at
// v19.2.6 (rgw_common.cc:51-144) and v20.2.4 (:52-142, which adds only
// RestoreAlreadyInProgress). Rows ceph main added later (ExpiredToken 2501,
// AccessControlListNotSupported 2227, InvalidBucketAclWithObjectOwnership 2228,
// BucketSuspended 2101, OwnershipControlsNotFoundError 2229) are absent: no
// floor release can emit them.
var (
	ErrPermanentRedirect            = &Error{Code: "PermanentRedirect", Status: 301, Errno: 2024}
	ErrWebsiteRedirect              = &Error{Code: "WebsiteRedirect", Status: 301, Errno: 2038}
	ErrNotModified                  = &Error{Code: "NotModified", Status: 304, Errno: 2016}
	ErrInvalidArgument              = &Error{Code: "InvalidArgument", Status: 400, Errno: 22}
	ErrInvalidRequest               = &Error{Code: "InvalidRequest", Status: 400, Errno: 2021}
	ErrInvalidDigest                = &Error{Code: "InvalidDigest", Status: 400, Errno: 2004}
	ErrBadDigest                    = &Error{Code: "BadDigest", Status: 400, Errno: 2005}
	ErrInvalidLocationConstraint    = &Error{Code: "InvalidLocationConstraint", Status: 400, Errno: 2208}
	ErrIllegalLocationConstraint    = &Error{Code: "IllegalLocationConstraintException", Status: 400, Errno: 2226}
	ErrZonegroupPlacementMisconfig  = &Error{Code: "ZonegroupDefaultPlacementMisconfiguration", Status: 400, Errno: 2213}
	ErrInvalidBucketName            = &Error{Code: "InvalidBucketName", Status: 400, Errno: 2000}
	ErrInvalidObjectName            = &Error{Code: "InvalidObjectName", Status: 400, Errno: 2001}
	ErrUnresolvableGrantByEmail     = &Error{Code: "UnresolvableGrantByEmailAddress", Status: 400, Errno: 2006}
	ErrInvalidPart                  = &Error{Code: "InvalidPart", Status: 400, Errno: 2007}
	ErrInvalidPartOrder             = &Error{Code: "InvalidPartOrder", Status: 400, Errno: 2008}
	ErrRequestTimeout               = &Error{Code: "RequestTimeout", Status: 400, Errno: 2010}
	ErrEntityTooLarge               = &Error{Code: "EntityTooLarge", Status: 400, Errno: 2019}
	ErrEntityTooSmall               = &Error{Code: "EntityTooSmall", Status: 400, Errno: 2022}
	ErrTooManyBuckets               = &Error{Code: "TooManyBuckets", Status: 400, Errno: 2020}
	ErrMalformedXML                 = &Error{Code: "MalformedXML", Status: 400, Errno: 2029}
	ErrContentSHA256Mismatch        = &Error{Code: "XAmzContentSHA256Mismatch", Status: 400, Errno: 2040}
	ErrMalformedPolicy              = &Error{Code: "MalformedPolicyDocument", Status: 400, Errno: 2204}
	ErrInvalidTag                   = &Error{Code: "InvalidTag", Status: 400, Errno: 2210}
	ErrMalformedACL                 = &Error{Code: "MalformedACLError", Status: 400, Errno: 2212}
	ErrInvalidCORSRules             = &Error{Code: "InvalidRequest", Status: 400, Errno: 2215}
	ErrInvalidWebsiteRoutingRules   = &Error{Code: "InvalidRequest", Status: 400, Errno: 2217}
	ErrInvalidEncryptionAlgorithm   = &Error{Code: "InvalidEncryptionAlgorithmError", Status: 400, Errno: 2214}
	ErrInvalidRetentionPeriod       = &Error{Code: "InvalidRetentionPeriod", Status: 400, Errno: 2047}
	ErrInvalidSecretKey             = &Error{Code: "InvalidSecretKey", Status: 400, Errno: 2034}
	ErrInvalidKeyType               = &Error{Code: "InvalidKeyType", Status: 400, Errno: 2035}
	ErrInvalidCapability            = &Error{Code: "InvalidCapability", Status: 400, Errno: 2036}
	ErrInvalidTenantName            = &Error{Code: "InvalidTenantName", Status: 400, Errno: 2037}
	ErrAccessDenied                 = &Error{Code: "AccessDenied", Status: 403, Errno: 13}
	ErrMFARequired                  = &Error{Code: "AccessDenied", Status: 403, Errno: 2044}
	ErrAuthorization                = &Error{Code: "AuthorizationError", Status: 403, Errno: 2225}
	ErrSignatureDoesNotMatch        = &Error{Code: "SignatureDoesNotMatch", Status: 403, Errno: 2027}
	ErrInvalidAccessKeyID           = &Error{Code: "InvalidAccessKeyId", Status: 403, Errno: 2028}
	ErrUserSuspended                = &Error{Code: "UserSuspended", Status: 403, Errno: 2100}
	ErrRequestTimeTooSkewed         = &Error{Code: "RequestTimeTooSkewed", Status: 403, Errno: 2012}
	ErrQuotaExceeded                = &Error{Code: "QuotaExceeded", Status: 403, Errno: 2026}
	ErrInvalidObjectState           = &Error{Code: "InvalidObjectState", Status: 403, Errno: 2222}
	ErrNoSuchKey                    = &Error{Code: "NoSuchKey", Status: 404, Errno: 2}
	ErrNoSuchBucket                 = &Error{Code: "NoSuchBucket", Status: 404, Errno: 2002}
	ErrNoSuchWebsiteConfiguration   = &Error{Code: "NoSuchWebsiteConfiguration", Status: 404, Errno: 2039}
	ErrNoSuchUpload                 = &Error{Code: "NoSuchUpload", Status: 404, Errno: 2009}
	ErrNotFound                     = &Error{Code: "NotFound", Status: 404, Errno: 2023}
	ErrNoSuchLifecycleConfiguration = &Error{Code: "NoSuchLifecycleConfiguration", Status: 404, Errno: 2041}
	ErrNoSuchBucketPolicy           = &Error{Code: "NoSuchBucketPolicy", Status: 404, Errno: 2207}
	ErrNoSuchUser                   = &Error{Code: "NoSuchUser", Status: 404, Errno: 2042}
	ErrNoRoleFound                  = &Error{Code: "NoSuchEntity", Status: 404, Errno: 2205}
	ErrNoCORSFound                  = &Error{Code: "NoSuchCORSConfiguration", Status: 404, Errno: 2216}
	ErrNoSuchSubUser                = &Error{Code: "NoSuchSubUser", Status: 404, Errno: 2043}
	ErrNoSuchEntity                 = &Error{Code: "NoSuchEntity", Status: 404, Errno: 2301}
	ErrNoSuchCORSConfiguration      = &Error{Code: "NoSuchCORSConfiguration", Status: 404, Errno: 2045}
	ErrNoSuchObjectLockConfig       = &Error{Code: "ObjectLockConfigurationNotFoundError", Status: 404, Errno: 2046}
	ErrNoSuchTagSet                 = &Error{Code: "NoSuchTagSet", Status: 404, Errno: 2402}
	ErrNoSuchBucketEncryption       = &Error{Code: "ServerSideEncryptionConfigurationNotFoundError", Status: 404, Errno: 2048}
	ErrNoSuchPublicAccessBlock      = &Error{Code: "NoSuchPublicAccessBlockConfiguration", Status: 404, Errno: 2049}
	ErrMethodNotAllowed             = &Error{Code: "MethodNotAllowed", Status: 405, Errno: 2003}
	ErrRequestTimedOut              = &Error{Code: "RequestTimeout", Status: 408, Errno: 110}
	ErrBucketAlreadyExists          = &Error{Code: "BucketAlreadyExists", Status: 409, Errno: 2013}
	ErrUserAlreadyExists            = &Error{Code: "UserAlreadyExists", Status: 409, Errno: 2030}
	ErrEmailExists                  = &Error{Code: "EmailExists", Status: 409, Errno: 2032}
	ErrKeyExists                    = &Error{Code: "KeyExists", Status: 409, Errno: 2033}
	ErrTagConflict                  = &Error{Code: "OperationAborted", Status: 409, Errno: 2209}
	ErrPositionNotEqualToLength     = &Error{Code: "PositionNotEqualToLength", Status: 409, Errno: 2219}
	ErrObjectNotAppendable          = &Error{Code: "ObjectNotAppendable", Status: 409, Errno: 2220}
	ErrInvalidBucketState           = &Error{Code: "InvalidBucketState", Status: 409, Errno: 2221}
	ErrBucketNotEmpty               = &Error{Code: "BucketNotEmpty", Status: 409, Errno: 39}
	ErrLimitExceeded                = &Error{Code: "LimitExceeded", Status: 409, Errno: 2302}
	ErrAccountAlreadyExists         = &Error{Code: "AccountAlreadyExists", Status: 409, Errno: 2403}
	ErrRestoreAlreadyInProgress     = &Error{Code: "RestoreAlreadyInProgress", Status: 409, Errno: 2500} // v20.2.4 only (rgw_common.h:364, table row :142); no Squid row
	ErrConcurrentModification       = &Error{Code: "ConcurrentModification", Status: 409, Errno: 125}
	ErrMissingContentLength         = &Error{Code: "MissingContentLength", Status: 411, Errno: 2011}
	ErrPreconditionFailed           = &Error{Code: "PreconditionFailed", Status: 412, Errno: 2015}
	ErrInvalidRange                 = &Error{Code: "InvalidRange", Status: 416, Errno: 34}
	ErrUnprocessableEntity          = &Error{Code: "UnprocessableEntity", Status: 422, Errno: 2018}
	ErrLocked                       = &Error{Code: "Locked", Status: 423, Errno: 2025}
	ErrInternalError                = &Error{Code: "InternalError", Status: 500, Errno: 2200}
	ErrUnknown                      = &Error{Code: "UnknownError", Status: 500, Errno: 0}
	ErrNotImplemented               = &Error{Code: "NotImplemented", Status: 501, Errno: 2201}
	ErrServiceUnavailable           = &Error{Code: "ServiceUnavailable", Status: 503, Errno: 2202}
	ErrSlowDown                     = &Error{Code: "SlowDown", Status: 503, Errno: 2218}
	ErrInsufficientCapacity         = &Error{Code: "InsufficientCapacity", Status: 507, Errno: 28}
)

// Errors returns every sentinel above, for tests and for the ops log's table.
func Errors() []*Error { return slices.Clone(errorSet) }

var errorSet = []*Error{ErrPermanentRedirect, ErrWebsiteRedirect /* ... every sentinel, in the order above ... */}

// AsError returns the *Error in err's chain. An error that carries none is a
// bug in an op and renders as InternalError; a context error renders as the
// 408 RequestTimeout, which is what a client that is still connected sees.
func AsError(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return ErrRequestTimedOut
	}
	return ErrInternalError
}

// FromRADOS maps a seam error to the S3 error radosgw's rgw_http_s3_errors
// table implies for scope: ENOENT is NoSuchBucket in a bucket op, NoSuchKey in
// an object op, NoSuchUser and NoSuchUpload likewise, and radosgw's own ENOENT
// row (NoSuchKey) otherwise. ECANCELED is the table's 409
// ConcurrentModification row (rgw_common.cc:141 at v19.2.6, :143 at v20.2.4).
// Errnos the table does not list, ERR_BUSY_RESHARDING among them, are
// UnknownError, as set_req_state_err resorts to. The result wraps both the S3 error and err, so errors.Is
// matches either and the RADOS cause reaches the log; an err that already
// carries an *Error is returned unchanged.
func FromRADOS(err error, scope Scope) error {
	if err == nil {
		return nil
	}
	var already *Error
	if errors.As(err, &already) {
		return err
	}
	var mapped *Error
	switch {
	case errors.Is(err, radosclient.ErrNotFound):
		mapped = notFound(scope)
	case errors.Is(err, radosclient.ErrExists):
		mapped = ErrBucketAlreadyExists
		if scope == ScopeUser {
			mapped = ErrUserAlreadyExists
		}
	case errors.Is(err, radosclient.ErrPermission):
		mapped = ErrAccessDenied
	case errors.Is(err, radosclient.ErrInvalid):
		mapped = ErrInvalidArgument
	case errors.Is(err, radosclient.ErrRange):
		mapped = ErrInvalidRange
	case errors.Is(err, radosclient.ErrTimedOut):
		mapped = ErrRequestTimedOut
	case errors.Is(err, radosclient.ErrNoSpace):
		mapped = ErrInsufficientCapacity
	case errors.Is(err, radosclient.ErrCanceled):
		mapped = ErrConcurrentModification
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		mapped = ErrRequestTimedOut
	case errors.Is(err, radosclient.ErrClosed), errors.Is(err, radosclient.ErrBadOp),
		errors.Is(err, radosclient.ErrReleaseTooOld), errors.Is(err, radosclient.ErrIncomplete):
		mapped = ErrInternalError
	default:
		mapped = ErrInternalError
		var re *radosclient.Error
		if errors.As(err, &re) {
			mapped = fromErrno(re.Errno)
		}
	}
	return fmt.Errorf("%w: %w", mapped, err)
}

func notFound(scope Scope) *Error {
	switch scope {
	case ScopeBucket:
		return ErrNoSuchBucket
	case ScopeUser:
		return ErrNoSuchUser
	case ScopeUpload:
		return ErrNoSuchUpload
	default:
		return ErrNoSuchKey
	}
}

// fromErrno maps the errnos rgw_http_s3_errors lists that have no seam sentinel.
func fromErrno(errno int32) *Error {
	if errno < 0 {
		errno = -errno
	}
	switch syscall.Errno(errno) {
	case syscall.EACCES, syscall.EPERM:
		return ErrAccessDenied
	case syscall.EBUSY:
		return ErrServiceUnavailable
	case syscall.ENOTEMPTY:
		return ErrBucketNotEmpty
	case syscall.EDQUOT, syscall.ENOSPC:
		return ErrInsufficientCapacity
	case syscall.EINVAL:
		return ErrInvalidArgument
	case syscall.ERANGE:
		return ErrInvalidRange
	case syscall.ETIMEDOUT:
		return ErrRequestTimedOut
	case syscall.ENOENT:
		return ErrNoSuchKey
	case syscall.EEXIST:
		return ErrBucketAlreadyExists
	}
	return ErrUnknown
}
```

`Scope` lives in `request.go` (Step 5). Run the specs; expected PASS for the error and FromRADOS groups.

- [ ] **Step 4: Write the failing specs for identity, request scope, transaction ids and the runner**

`internal/op/request_test.go`:

```go
var _ = Describe("Request scope", func() {
	DescribeTable("is decided by bucket and object",
		func(bucket, object string, want op.Scope) {
			r := &op.Request{Bucket: bucket, Object: meta.ObjKey{Name: object}}
			Expect(r.Scope()).To(Equal(want))
		},
		Entry("service", "", "", op.ScopeService),
		Entry("bucket", "b", "", op.ScopeBucket),
		Entry("object", "b", "k", op.ScopeObject),
	)
})

var _ = Describe("Anonymous", func() {
	It("is radosgw's anonymous user with the full op mask", func() {
		id := op.Anonymous()
		Expect(id.Anonymous).To(BeTrue())
		Expect(id.Owner.String()).To(Equal("anonymous"), "rgw_owner string")
		Expect(id.OpMask).To(Equal(op.OpTypeAll), "RGWUserInfo's default op_mask")
		Expect(id.User).NotTo(BeNil())
		Expect(id.User.UserID.ID).To(Equal(op.AnonymousUserID))
	})
})
```

`internal/op/transid_test.go`, from `svc_zone_utils.cc:22-27,37-42,55-63`:

```go
var _ = Describe("transaction ids", func() {
	It("formats tx, a 21-digit hex sequence, a dash, a 10-digit hex time and the suffix", func() {
		at := time.Unix(0x68d7a1b2, 0)
		Expect(op.TransID(1, at, "-4155-ceph-objectstore")).
			To(Equal("tx000000000000000000001-0068d7a1b2-4155-ceph-objectstore"))
	})
	It("builds the suffix as url-encoded -<instance>-<zone>", func() {
		Expect(op.TransIDSuffix(4155, "ceph-objectstore")).To(Equal("-4155-ceph-objectstore"))
		Expect(op.TransIDSuffix(7, "my zone")).To(Equal("-7-my%20zone"), "a space is percent-encoded")
	})
	It("builds the host id as <instance>-<zone>-<zonegroup>", func() {
		Expect(op.HostID(4155, "z", "zg")).To(Equal("4155-z-zg"))
	})
})
```

`internal/op/op_test.go` drives `op.Run` with a recording op:

```go
type recorder struct {
	calls   []string
	mask    uint32
	initErr, permErr, execErr error
}

func (r *recorder) Name() string                  { return "recorder" }
func (r *recorder) Action() policy.Action         { return policy.S3GetObject }
func (r *recorder) OpMask() uint32                { return r.mask }
func (r *recorder) Init(context.Context, *op.Request) error {
	r.calls = append(r.calls, "init")
	return r.initErr
}
func (r *recorder) VerifyPermission(context.Context, *op.Request) error {
	r.calls = append(r.calls, "verify")
	return r.permErr
}
func (r *recorder) Execute(context.Context, *op.Request) error {
	r.calls = append(r.calls, "execute")
	return r.execErr
}
func (r *recorder) Complete(context.Context, *op.Request) { r.calls = append(r.calls, "complete") }

var _ = Describe("Run", func() {
	var (
		rec *recorder
		req *op.Request
	)
	BeforeEach(func() {
		rec = &recorder{mask: op.OpTypeRead}
		req = &op.Request{Identity: op.Anonymous(), Env: &op.Env{Zone: memstore.New(memstore.Config{})}}
	})
	It("runs init, verify, execute and complete in radosgw's order", func(ctx SpecContext) {
		Expect(op.Run(ctx, rec, req)).To(Succeed())
		Expect(rec.calls).To(Equal([]string{"init", "verify", "execute", "complete"}))
	})
	It("stops at a failed init and never completes", func(ctx SpecContext) {
		rec.initErr = op.ErrNoSuchBucket
		Expect(op.Run(ctx, rec, req)).To(MatchError(op.ErrNoSuchBucket))
		Expect(rec.calls).To(Equal([]string{"init"}))
	})
	It("denies a user whose op mask lacks the op's type before verifying", func(ctx SpecContext) {
		req.Identity.OpMask = op.OpTypeRead
		rec.mask = op.OpTypeWrite
		Expect(op.Run(ctx, rec, req)).To(MatchError(op.ErrAccessDenied))
		Expect(rec.calls).To(Equal([]string{"init"}))
	})
	It("denies a modifying op on a read-only zone for a non-system identity", func(ctx SpecContext) {
		req.Env.Zone = memstore.New(memstore.Config{Zone: meta.Zone{Name: "ro", ReadOnly: true}})
		rec.mask = op.OpTypeDelete
		Expect(op.Run(ctx, rec, req)).To(MatchError(op.ErrAccessDenied))
		req.Identity.System = true
		rec.calls = nil
		Expect(op.Run(ctx, rec, req)).To(Succeed(), "a system identity may write a read-only zone")
	})
	It("lets an admin identity through a permission denial, as rgw_process_authenticated does", func(ctx SpecContext) {
		rec.permErr = op.ErrAccessDenied
		Expect(op.Run(ctx, rec, req)).To(MatchError(op.ErrAccessDenied), "plain identity")
		req.Identity.Admin = true
		rec.calls = nil
		Expect(op.Run(ctx, rec, req)).To(Succeed(), "admin identity")
		Expect(rec.calls).To(Equal([]string{"init", "verify", "execute", "complete"}))
	})
	It("lets an admin identity through any verify error, not only AccessDenied", func(ctx SpecContext) {
		// ErrQuotaExceeded, not ErrMFARequired: the latter shares AccessDenied/403 with
		// ErrAccessDenied and is indistinguishable from it under errors.Is.
		rec.permErr = op.ErrQuotaExceeded
		req.Identity.Admin = true
		Expect(op.Run(ctx, rec, req)).To(Succeed())
		Expect(rec.calls).To(Equal([]string{"init", "verify", "execute", "complete"}))
	})
	It("completes after a failed execute and returns the execute error", func(ctx SpecContext) {
		rec.execErr = op.ErrQuotaExceeded
		Expect(op.Run(ctx, rec, req)).To(MatchError(op.ErrQuotaExceeded))
		Expect(rec.calls).To(Equal([]string{"init", "verify", "execute", "complete"}))
	})
})
```

- [ ] **Step 5: Implement `identity.go`, `request.go`, `op.go`, `sink.go`, `env.go`, `authz.go`, `transid.go`**

```go
package op

// Scope is the resource level a request addresses.
type Scope uint8

// The scopes, from the URL: no bucket, a bucket, a bucket and an object; and
// the two FromRADOS distinguishes for metadata ops.
const (
	ScopeService Scope = iota
	ScopeBucket
	ScopeObject
	ScopeUser
	ScopeUpload
)

// AnonymousUserID is RGW_USER_ANON_ID, the user id every unauthenticated request runs as.
const AnonymousUserID = "anonymous"

// Identity is who the request is from, resolved by auth before dispatch.
type Identity struct {
	Anonymous bool
	// User is the resolved RGWUserInfo; for an anonymous request it is the
	// default user info under AnonymousUserID, as radosgw builds it.
	User *meta.UserInfo
	// Owner is the user, or the account for an account user: what buckets and
	// objects this identity creates are owned by.
	Owner meta.Owner
	// Account is set when the user belongs to an account.
	Account *meta.AccountInfo
	SubUser string
	// Tenant is the identity's tenant, the default tenant of the buckets it names.
	Tenant string
	// AccessKey is the key that signed the request, "" for anonymous.
	AccessKey string
	// OpMask is the RGW_OP_TYPE_* bits the user may perform; Run checks it.
	OpMask uint32
	Caps   meta.Caps
	// Admin is radosgw's is_admin(): Run lets such an identity through an
	// AccessDenied from VerifyPermission.
	Admin bool
	// System is a system user's request: no quota, and it may modify a
	// read-only zone.
	System bool
	// Attrs are the user's stored xattrs. Identity policies live in them and
	// the authorizer decodes them on demand, as radosgw does per request.
	Attrs map[string][]byte
}

// Anonymous is the identity of an unauthenticated request.
func Anonymous() Identity {
	uid := meta.UserID{ID: AnonymousUserID}
	return Identity{
		Anonymous: true,
		User:      &meta.UserInfo{UserID: uid, OpMask: OpTypeAll},
		Owner:     meta.UserOwner(uid),
		OpMask:    OpTypeAll,
	}
}

// Request is the protocol-neutral request the ops see. The protocol layer
// fills everything above Identity from the HTTP request, auth fills Identity,
// and the ops fill the rest.
type Request struct {
	// ID is radosgw's transaction id, the x-amz-request-id.
	ID   string
	Time time.Time

	Method string
	// Host is the Host header without its port, lowercased; "" when absent.
	Host string
	// Path is the request path decoded once; RawPath is as the client sent it,
	// which SigV4 canonicalizes.
	Path    string
	RawPath string
	// Query holds the decoded query; a key containing "X-Amz-" is lowercased
	// except for its dashes, as RGWHTTPArgs::parse does.
	Query    url.Values
	RawQuery string
	Header   http.Header
	// Body is the request body; auth may replace it with a chunk-verifying reader.
	Body io.Reader
	// ContentLength is the declared length, -1 when unknown.
	ContentLength int64
	RemoteAddr    string
	Referer       string
	TLS           bool

	// Tenant is the bucket's tenant: explicit from "tenant:bucket" in the URL,
	// otherwise the identity's tenant once auth has run. "" is the default tenant.
	Tenant string
	// Bucket is "" for service scope.
	Bucket string
	// Object has an empty Name for service and bucket scope; Instance carries
	// the versionId query parameter.
	Object meta.ObjKey

	Identity Identity
	Env      *Env

	// BucketRec and ObjState are loaded by the op's Init.
	BucketRec *BucketRecord
	ObjState  *ObjectState

	// Status and the byte counters are filled by the protocol layer as the
	// response goes out, for Complete, metrics and the usage log.
	Status   int
	BytesIn  int64
	BytesOut int64
}

// Scope reports the resource level the request addresses.
func (r *Request) Scope() Scope {
	switch {
	case r.Bucket == "":
		return ScopeService
	case r.Object.Name == "":
		return ScopeBucket
	default:
		return ScopeObject
	}
}

// AuthResult is what authentication resolves a request to.
type AuthResult struct {
	Identity Identity
	// Body replaces the request body when the payload is signed in chunks;
	// nil keeps the request's own body.
	Body io.Reader
	// PayloadSHA256 is the x-amz-content-sha256 value, "UNSIGNED-PAYLOAD" when
	// the header is absent, which is radosgw's fallback.
	PayloadSHA256 string
	Presigned     bool
}

// PayloadForms is the set of signed payload forms an op accepts. radosgw's
// SigV4 abstractor chooses the payload completer by op type and throws
// ERR_NOT_IMPLEMENTED for a form the op is not listed for, before the access
// key is looked up (get_auth_data_v4, rgw_rest_s3.cc:5899-5969 at v19.2.6,
// :6466-6540 at v20.2.4). An empty body and an unsigned payload need no form.
type PayloadForms uint8

const (
	// PayloadSigned is a non-empty body whose x-amz-content-sha256 is its
	// SHA-256, verified whole (AWSv4ComplSingle).
	PayloadSigned PayloadForms = 1 << iota
	// PayloadChunked is an aws-chunked body, x-amz-content-sha256 starting
	// "STREAMING-" (AWSv4ComplMulti); radosgw takes it for RGW_OP_PUT_OBJ only.
	PayloadChunked
)
```

```go
package op

// The RGW_OP_TYPE_* bits of a user's op mask (rgw_common.h:232-237 at v19.2.6, :254-259 at v20.2.4).
const (
	OpTypeRead   uint32 = 0x01
	OpTypeWrite  uint32 = 0x02
	OpTypeDelete uint32 = 0x04
	OpTypeModify uint32 = OpTypeWrite | OpTypeDelete
	OpTypeAll    uint32 = OpTypeRead | OpTypeWrite | OpTypeDelete
)

// Op is radosgw's op lifecycle in order. One type implements it per S3 or
// admin operation, with typed input fields the protocol layer fills after
// parsing and typed result fields it renders afterwards.
type Op interface {
	// Name is radosgw's RGWOp::name() ("get_obj", "list_buckets"), even where the
	// route name differs: the usage log files each request under it as its
	// category (rgw_log.cc:245, :556-559), and the ops log and metrics use it too.
	Name() string
	// Action is the s3:* action VerifyPermission evaluates.
	Action() policy.Action
	// OpMask is the RGW_OP_TYPE_* bits the identity must hold.
	OpMask() uint32
	// Init loads the bucket and object state the op needs into r.
	Init(ctx context.Context, r *Request) error
	// VerifyPermission is radosgw's verify_permission: op mask aside, which Run checks.
	VerifyPermission(ctx context.Context, r *Request) error
	Execute(ctx context.Context, r *Request) error
	// Complete records usage and stats; it never fails the request. It runs
	// once Execute has run, whatever Execute returned.
	Complete(ctx context.Context, r *Request)
}

// Run drives o through the lifecycle for r in rgw_process_authenticated's
// order: Init, the op-mask check, VerifyPermission, Execute, Complete. An
// Admin identity (auth sets Admin for admin and system users) overrides any
// error from VerifyPermission, as rgw_process_authenticated does for a system
// request or an is_admin_of identity (rgw_process.cc:228-236 at v19.2.6).
func Run(ctx context.Context, o Op, r *Request) error {
	if err := o.Init(ctx, r); err != nil {
		return err
	}
	if err := verifyOpMask(o, r); err != nil {
		return err
	}
	if err := o.VerifyPermission(ctx, r); err != nil {
		if !r.Identity.Admin {
			return err
		}
		slog.DebugContext(ctx, "overriding permissions for an admin identity", slog.String("op", o.Name()))
	}
	defer o.Complete(ctx, r)
	return o.Execute(ctx, r)
}

// verifyOpMask is RGWOp::verify_op_mask: the identity holds every bit the op
// needs, and a non-system identity never modifies a read-only zone.
func verifyOpMask(o Op, r *Request) error {
	required := o.OpMask()
	if r.Identity.OpMask&required != required {
		return ErrAccessDenied
	}
	if required&OpTypeModify != 0 && !r.Identity.System && r.Env != nil && r.Env.Zone != nil && r.Env.Zone.Zone().ReadOnly {
		return ErrAccessDenied
	}
	return nil
}

// Sink is where a streaming op writes its body: the push model radosgw uses
// in send_response_data, so tail reads go to the socket in order and the
// driver recycles its buffers after each Write returns instead of a reader
// chain holding them.
type Sink interface {
	// WriteHeader sends the status and headers; it is called once, before the first Write.
	WriteHeader(status int, h http.Header)
	io.Writer
	Flush() error
}

// Metrics is the op-level observation hook; the metrics package implements it.
type Metrics interface {
	// InFlight adds delta to the in-flight request gauge.
	InFlight(delta int)
	// Observe records one finished request.
	Observe(opName string, status int, elapsed time.Duration, bytesIn, bytesOut int64)
}

// NopMetrics discards every observation.
type NopMetrics struct{}

func (NopMetrics) InFlight(int)                                     {}
func (NopMetrics) Observe(string, int, time.Duration, int64, int64) {}

// Env is the process-wide environment a request runs in: the stores, the
// authorizer, configuration and clocks. It is radosgw's RGWProcessEnv.
type Env struct {
	Zone      ZoneInfo
	Users     UserStore
	Buckets   BucketStore
	Objects   ObjectStore
	Multipart MultipartStore
	Stats     StatsStore
	Usage     UsageLogger
	Metadata  MetadataStore
	Authz     Authorizer
	Conf      *cephconf.Options
	Metrics   Metrics
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// HostId is radosgw's host_id, rendered in error documents.
	HostID string
}

// Clock returns the current time from Now or time.Now.
func (e *Env) Clock() time.Time {
	if e != nil && e.Now != nil {
		return e.Now()
	}
	return time.Now()
}
```

```go
package op

// Authorizer is radosgw's verify_permission machinery: op mask aside, the
// ACL, bucket-policy and identity-policy evaluation for one action.
// authz.Evaluator implements it; OwnerOnly is the development stub.
type Authorizer interface {
	// VerifyUser authorizes an action with no bucket, such as ListAllMyBuckets
	// and CreateBucket, against the identity's own policies.
	VerifyUser(ctx context.Context, r *Request, a policy.Action) error
	// VerifyBucket authorizes a against r.BucketRec with perm as the ACL permission.
	VerifyBucket(ctx context.Context, r *Request, a policy.Action, perm acl.Permission) error
	// VerifyObject authorizes a against r.ObjState within r.BucketRec.
	VerifyObject(ctx context.Context, r *Request, a policy.Action, perm acl.Permission) error
	// VerifyBucketIn is VerifyBucket against an explicit bucket and key while
	// the request's own bucket keeps supplying the public-access block and the
	// requester-pays check, as radosgw's verify_bucket_permission(s, arn,
	// user_acl, bucket_acl, policy, ...) does with s still the request:
	// CopyObject's source (rgw_op.cc:5454-5457 at v19.2.6, :6020-6023 at
	// v20.2.4) and DeleteObjects' per-key check (:6829-6837). key.Name "" is
	// the bucket ARN.
	VerifyBucketIn(ctx context.Context, r *Request, a policy.Action, perm acl.Permission, bucket *BucketRecord, key meta.ObjKey) error
	// VerifyObjectIn is VerifyObject against an explicit object in an explicit
	// bucket: UploadPartCopy's source (rgw_op.cc:3955-3963 at v19.2.6).
	VerifyObjectIn(ctx context.Context, r *Request, a policy.Action, perm acl.Permission, bucket *BucketRecord, obj *ObjectState) error
}

// VerifyUserPermission authorizes a service-scope action through r.Env.Authz.
func VerifyUserPermission(ctx context.Context, r *Request, a policy.Action) error {
	return r.Env.Authz.VerifyUser(ctx, r, a)
}

// VerifyBucketPermission authorizes a bucket action through r.Env.Authz.
func VerifyBucketPermission(ctx context.Context, r *Request, a policy.Action, perm acl.Permission) error {
	return r.Env.Authz.VerifyBucket(ctx, r, a, perm)
}

// VerifyObjectPermission authorizes an object action through r.Env.Authz.
func VerifyObjectPermission(ctx context.Context, r *Request, a policy.Action, perm acl.Permission) error {
	return r.Env.Authz.VerifyObject(ctx, r, a, perm)
}

// VerifyBucketPermissionIn authorizes a against an explicit bucket and key
// (CopyObject's source, DeleteObjects' per-key check) through r.Env.Authz.
func VerifyBucketPermissionIn(ctx context.Context, r *Request, a policy.Action, perm acl.Permission, bucket *BucketRecord, key meta.ObjKey) error {
	return r.Env.Authz.VerifyBucketIn(ctx, r, a, perm, bucket, key)
}

// VerifyObjectPermissionIn authorizes a against an explicit object
// (UploadPartCopy's source) through r.Env.Authz.
func VerifyObjectPermissionIn(ctx context.Context, r *Request, a policy.Action, perm acl.Permission, bucket *BucketRecord, obj *ObjectState) error {
	return r.Env.Authz.VerifyObjectIn(ctx, r, a, perm, bucket, obj)
}

// OwnerOnly is the interim authorizer: service actions are allowed, as
// radosgw's verify_user_permission_no_policy allows them for a user with no
// user ACL, and bucket and object actions are allowed to the bucket owner
// alone. It evaluates no ACL and no policy; authz.Evaluator replaces it.
type OwnerOnly struct{}

func (OwnerOnly) VerifyUser(context.Context, *Request, policy.Action) error { return nil }

func (OwnerOnly) VerifyBucket(_ context.Context, r *Request, _ policy.Action, _ acl.Permission) error {
	return ownerOnly(r)
}

func (OwnerOnly) VerifyObject(_ context.Context, r *Request, _ policy.Action, _ acl.Permission) error {
	return ownerOnly(r)
}

func (OwnerOnly) VerifyBucketIn(_ context.Context, r *Request, _ policy.Action, _ acl.Permission, bucket *BucketRecord, _ meta.ObjKey) error {
	return ownerOnlyOf(r, bucket)
}

func (OwnerOnly) VerifyObjectIn(_ context.Context, r *Request, _ policy.Action, _ acl.Permission, bucket *BucketRecord, _ *ObjectState) error {
	return ownerOnlyOf(r, bucket)
}

func ownerOnly(r *Request) error { return ownerOnlyOf(r, r.BucketRec) }

func ownerOnlyOf(r *Request, rec *BucketRecord) error {
	if r.Identity.Anonymous || rec == nil || rec.Info.Owner.String() != r.Identity.Owner.String() {
		return ErrAccessDenied
	}
	return nil
}
```

```go
package op

// TransID is RGWSI_ZoneUtils::unique_trans_id: "tx", the sequence number as
// 21 hex digits, a dash, the Unix time as 10 hex digits, and the suffix.
func TransID(seq uint64, now time.Time, suffix string) string {
	return fmt.Sprintf("tx%021x-%010x%s", seq, now.Unix(), suffix)
}

// TransIDSuffix is radosgw's trans_id_suffix: "-<instance id>-<zone name>",
// URL-encoded with URLEncode(…, true), radosgw's url_encode (svc_zone_utils.cc:22-63).
func TransIDSuffix(instanceID uint64, zone string) string {
	return URLEncode(fmt.Sprintf("-%d-%s", instanceID, zone), true)
}

// HostID is RGWSI_ZoneUtils::gen_host_id: "<instance id>-<zone>-<zonegroup>".
func HostID(instanceID uint64, zone, zonegroup string) string {
	return fmt.Sprintf("%d-%s-%s", instanceID, zone, zonegroup)
}

// URLEncode is radosgw's url_encode(src, encode_slash) (rgw_common.cc:1775-1785
// at v19.2.6, :1838 at v20.2.4) over char_needs_url_encoding (:1743-1773;
// :1806): a byte <= 0x20 or >= 0x7F, or one of " # % & + , / : ; < = > ? @ [ \ ]
// ^ ` { }, is %XX; everything else — alphanumerics and ! $ ' ( ) * - . _ | ~ —
// passes through. With encodeSlash false '/' passes too, dump_urlsafe's form for
// listing prefixes and delimiters (rgw_rest.h:712, encode_slash defaults true).
// It serves the transaction-id suffix (encodeSlash true) and the object and
// multipart-upload listings' encoding-type=url; nothing else in rgw-go
// URL-encodes by hand.
func URLEncode(s string, encodeSlash bool) string {
	var b strings.Builder
	for i := range len(s) {
		c := s[i]
		if urlByteNeedsEncoding(c) && !(c == '/' && !encodeSlash) {
			fmt.Fprintf(&b, "%%%02X", c)
		} else {
			b.WriteByte(c)
		}
	}
	return b.String()
}

// urlByteNeedsEncoding is char_needs_url_encoding, the switch transcribed.
func urlByteNeedsEncoding(c byte) bool {
	if c <= 0x20 || c >= 0x7F {
		return true
	}
	switch c {
	case 0x22, 0x23, 0x25, 0x26, 0x2B, 0x2C, 0x2F, 0x3A, 0x3B, 0x3C, 0x3D, 0x3E, 0x3F, 0x40, 0x5B, 0x5C, 0x5D, 0x5E, 0x60, 0x7B, 0x7D:
		return true
	}
	return false
}
```

- [ ] **Step 6: Write the store interfaces and their types**

One file per concern. Every interface carries the counterfeiter directive; `doc.go` carries the single `//go:generate go tool counterfeiter -generate` line for the package.

`internal/op/zone.go`:

```go
// Placement is a placement rule resolved through the zone's parameters to
// the pools and namespace a bucket's data, index and multipart pieces use.
type Placement struct {
	Rule          meta.PlacementRule
	DataPool      meta.Pool // for the rule's storage class
	IndexPool     meta.Pool
	DataExtraPool meta.Pool
	// Compression is the storage class's compression type, "" for none.
	Compression string
	InlineData  bool
}

//counterfeiter:generate . ZoneInfo

// ZoneInfo is the zone this gateway serves, resolved once at startup.
type ZoneInfo interface {
	// Release is the encoding release the cluster's own radosgw writes.
	Release() denc.Release
	Zone() meta.Zone
	ZoneGroup() meta.ZoneGroup
	ZoneParams() meta.ZoneParams
	Realm() meta.Realm
	Period() meta.Period
	// Placement resolves rule; the zero rule means the zonegroup's default.
	Placement(rule meta.PlacementRule) (Placement, error)
}
```

`internal/op/user.go`:

```go
// UserRecord is a user as stored: the info object, its xattrs and the
// cls_version the metadata cache tracks.
type UserRecord struct {
	Info    meta.UserInfo
	Attrs   map[string][]byte
	Version meta.ObjVersion
	Mtime   time.Time
}

// PutUserOptions guards a PutUser.
type PutUserOptions struct {
	// Exclusive fails with ErrUserAlreadyExists when the user exists.
	Exclusive bool
	// IfVersion, when non-nil, fails with ErrConcurrentModification unless the
	// stored version matches.
	IfVersion *meta.ObjVersion
}

//counterfeiter:generate . UserStore

// UserStore reads and writes users and their index objects.
type UserStore interface {
	GetUser(ctx context.Context, id meta.UserID) (*UserRecord, error)
	GetUserByAccessKey(ctx context.Context, key string) (*UserRecord, error)
	GetUserByEmail(ctx context.Context, email string) (*UserRecord, error)
	// PutUser writes the user, maintains the access-key and email index
	// objects, bumps rec.Version, and sends the cache notify.
	PutUser(ctx context.Context, rec *UserRecord, opts PutUserOptions) error
	RemoveUser(ctx context.Context, rec *UserRecord) error
	// ListUserBuckets pages the owner's bucket list from marker, at most max
	// entries; more reports whether entries remain past next.
	ListUserBuckets(ctx context.Context, owner meta.Owner, marker string, max int) (ents []meta.BucketEnt, next string, more bool, err error)
}
```

`internal/op/bucket.go`:

```go
// BucketRecord is a bucket as stored: entry point, instance, the instance's
// xattrs (ACL, policy and tags among them) and both cls_versions.
type BucketRecord struct {
	EntryPoint meta.BucketEntryPoint
	Info       meta.BucketInfo
	Attrs      map[string][]byte
	Version    meta.ObjVersion // of the instance
	EPVersion  meta.ObjVersion // of the entry point
	Mtime      time.Time
}

// CreateBucketParams is what CreateBucket needs to lay a bucket out.
type CreateBucketParams struct {
	Tenant, Name string
	Owner        meta.Owner
	Zonegroup    string
	Placement    meta.PlacementRule
	// Attrs are the instance xattrs to write, the ACL among them.
	Attrs map[string][]byte
	Quota meta.Quota
	// Exclusive fails with ErrBucketAlreadyExists when the bucket exists.
	Exclusive bool
}

// ListObjectsParams is one page of a listing over the bucket index.
type ListObjectsParams struct {
	Prefix    string
	Delimiter string
	// Marker is the index key to start after; "" starts at the beginning.
	Marker  string
	MaxKeys int
	// NS is the index namespace: "" for objects, "multipart" for uploads.
	NS string
	// ListVersions returns every version and delete marker. Versioning is
	// not implemented yet.
	ListVersions bool
}

// ObjectEntry is one bucket index entry as a listing returns it.
type ObjectEntry struct {
	Key              meta.ObjKey
	Size             uint64
	Mtime            time.Time
	ETag             string
	Owner            meta.Owner
	OwnerDisplayName string
	StorageClass     string
	// IsLatest, DeleteMarker and Exists are the versioned-listing flags;
	// versioning is not implemented yet.
	IsLatest     bool
	DeleteMarker bool
	Exists       bool
}

// ListObjectsResult is one page of a listing.
type ListObjectsResult struct {
	Entries        []ObjectEntry
	CommonPrefixes []string
	Truncated      bool
	// NextMarker is the marker for the next page when Truncated.
	NextMarker string
}

//counterfeiter:generate . BucketStore

// BucketStore reads and writes bucket entry points and instances and lists
// the bucket index.
type BucketStore interface {
	GetBucket(ctx context.Context, tenant, name string) (*BucketRecord, error)
	GetBucketInstance(ctx context.Context, id meta.BucketID) (*BucketRecord, error)
	CreateBucket(ctx context.Context, p CreateBucketParams) (*BucketRecord, error)
	// DeleteBucket removes the instance, the entry point and the user's list
	// entry; a bucket with objects fails with ErrBucketNotEmpty.
	DeleteBucket(ctx context.Context, rec *BucketRecord) error
	// PutBucketInfo rewrites the instance from rec.Info, guarded by rec.Version.
	PutBucketInfo(ctx context.Context, rec *BucketRecord) error
	// PutBucketAttrs sets and removes instance xattrs, guarded by rec.Version.
	PutBucketAttrs(ctx context.Context, rec *BucketRecord, set map[string][]byte, rm []string) error
	ListObjects(ctx context.Context, rec *BucketRecord, p ListObjectsParams) (ListObjectsResult, error)
}
```

`internal/op/object.go`:

```go
// ObjectState is what one head read tells about an object.
type ObjectState struct {
	Bucket *BucketRecord
	Key    meta.ObjKey
	Exists bool
	Size   uint64
	Mtime  time.Time
	// Epoch is the version the head read returned.
	Epoch uint64
	Attrs map[string][]byte
	// Manifest is nil for an object held entirely in its head.
	Manifest    *meta.Manifest
	Compression *meta.CompressionInfo
	ETag        string
	// WriteTag is user.rgw.idtag.
	WriteTag     string
	StorageClass string
	ContentType  string
}

// ByteRange is a resolved range: Length bytes from Offset.
type ByteRange struct {
	Offset, Length uint64
}

// PutParams shapes a write.
type PutParams struct {
	// Attrs are the head xattrs to write: ACL, content type, user metadata.
	Attrs map[string][]byte
	// Size is the declared body size, -1 when unknown.
	Size int64
	// Mtime is the object mtime; zero means now.
	Mtime        time.Time
	StorageClass string
	// IfMatch and IfNoneMatch are ETags; "*" matches any object.
	IfMatch, IfNoneMatch string
	// ETag, when set, is stored instead of the computed MD5 (multipart completion).
	ETag string
}

// PutResult describes the object a write produced.
type PutResult struct {
	ETag    string
	Size    uint64
	Mtime   time.Time
	Epoch   uint64
	Version string
}

// DeleteParams shapes a delete.
type DeleteParams struct {
	IfMatch string
	// Mtime is the delete time; zero means now.
	Mtime time.Time
}

// CopyParams shapes a copy.
type CopyParams struct {
	// Attrs replace the destination's xattrs when ReplaceAttrs, else they are
	// merged over the source's.
	Attrs        map[string][]byte
	ReplaceAttrs bool
	Mtime        time.Time
	StorageClass string
	IfMatch, IfNoneMatch string
}

//counterfeiter:generate . ObjectStore

// ObjectStore reads and writes objects.
type ObjectStore interface {
	// StatObject reads the head: stat, xattrs and the first stripe's worth of
	// data in one op. A missing object returns a state with Exists false and
	// no error.
	StatObject(ctx context.Context, rec *BucketRecord, key meta.ObjKey) (*ObjectState, error)
	// ReadObject streams rng of st to sink, in order.
	ReadObject(ctx context.Context, st *ObjectState, rng ByteRange, sink io.Writer) error
	PutObject(ctx context.Context, rec *BucketRecord, key meta.ObjKey, body io.Reader, p PutParams) (*PutResult, error)
	// DeleteObject removes the object; a missing object is ErrNoSuchKey, which
	// the S3 op turns into 204 as radosgw does.
	DeleteObject(ctx context.Context, rec *BucketRecord, key meta.ObjKey, p DeleteParams) error
	CopyObject(ctx context.Context, src *ObjectState, dst *BucketRecord, dstKey meta.ObjKey, p CopyParams) (*PutResult, error)
	// SetObjectAttrs sets and removes head xattrs.
	SetObjectAttrs(ctx context.Context, st *ObjectState, set map[string][]byte, rm []string) error
}
```

`internal/op/multipart.go`:

```go
// Upload is an in-progress multipart upload.
type Upload struct {
	ID        string
	Bucket    *BucketRecord
	Key       meta.ObjKey
	Owner     meta.Owner
	OwnerName string
	Initiated time.Time
	Placement meta.PlacementRule
	// Attrs are the head xattrs the completed object receives.
	Attrs map[string][]byte
}

// UploadParams shapes CreateUpload.
type UploadParams struct {
	Owner     meta.Owner
	OwnerName string
	Placement meta.PlacementRule
	Attrs     map[string][]byte
}

// Part is one uploaded part.
type Part struct {
	Number int
	ETag   string
	Size   uint64
	Mtime  time.Time
}

// PartResult describes a written part.
type PartResult struct {
	ETag  string
	Size  uint64
	Mtime time.Time
}

// ListPartsResult is one page of ListParts.
type ListPartsResult struct {
	Parts      []Part
	NextMarker int
	Truncated  bool
}

// ListUploadsParams is one page of ListUploads.
type ListUploadsParams struct {
	Prefix, Delimiter          string
	KeyMarker, UploadIDMarker  string
	MaxUploads                 int
}

// ListUploadsResult is one page of in-progress uploads.
type ListUploadsResult struct {
	Uploads            []Upload
	CommonPrefixes     []string
	NextKeyMarker      string
	NextUploadIDMarker string
	Truncated          bool
}

// CompletePart names one part of a completion, in the client's order.
type CompletePart struct {
	Number int
	ETag   string
}

//counterfeiter:generate . MultipartStore

// MultipartStore implements the seven multipart operations over radosgw's layout.
type MultipartStore interface {
	CreateUpload(ctx context.Context, rec *BucketRecord, key meta.ObjKey, p UploadParams) (*Upload, error)
	GetUpload(ctx context.Context, rec *BucketRecord, key meta.ObjKey, uploadID string) (*Upload, error)
	PutPart(ctx context.Context, up *Upload, n int, body io.Reader, p PutParams) (*PartResult, error)
	CopyPart(ctx context.Context, up *Upload, n int, src *ObjectState, rng ByteRange) (*PartResult, error)
	ListParts(ctx context.Context, up *Upload, marker, max int) (ListPartsResult, error)
	ListUploads(ctx context.Context, rec *BucketRecord, p ListUploadsParams) (ListUploadsResult, error)
	// Complete assembles parts into the object under the RGWCompleteMultipart
	// lock and returns it; a part that does not match is ErrInvalidPart, an
	// out-of-order list ErrInvalidPartOrder.
	Complete(ctx context.Context, up *Upload, parts []CompletePart) (*PutResult, error)
	Abort(ctx context.Context, up *Upload) error
}
```

`internal/op/stats.go`, `usage.go`, `metadata.go`:

```go
// Stats are a bucket's or user's totals.
type Stats struct {
	Size        uint64
	SizeRounded uint64
	NumObjects  uint64
}

//counterfeiter:generate . StatsStore

// StatsStore serves bucket and user statistics with radosgw's cache TTLs and
// enforces quotas from them.
type StatsStore interface {
	BucketStats(ctx context.Context, rec *BucketRecord) (Stats, error)
	UserStats(ctx context.Context, owner meta.Owner) (Stats, error)
	// CheckQuota fails with ErrQuotaExceeded when adding addBytes and addObjs
	// to rec or to owner's totals would exceed an enabled quota.
	CheckQuota(ctx context.Context, rec *BucketRecord, owner meta.Owner, addBytes, addObjs int64) error
}

// UsageEntry is one op's contribution to the usage log, rgw_usage_data keyed
// as radosgw keys it.
type UsageEntry struct {
	Owner         meta.Owner
	Bucket        string
	Time          time.Time
	Category      string // the op name
	BytesSent     uint64
	BytesReceived uint64
	Ops           uint64
	SuccessfulOps uint64
}

//counterfeiter:generate . UsageLogger

// UsageLogger accumulates usage entries; the driver flushes them on its schedule.
type UsageLogger interface {
	Log(ctx context.Context, e UsageEntry)
}

// MetadataEntry is one entry of a metadata section as the admin API shows it.
type MetadataEntry struct {
	Key     string
	Data    json.RawMessage
	Version meta.ObjVersion
	Mtime   time.Time
}

// PutMetadataOptions guards a metadata Put.
type PutMetadataOptions struct {
	// IfVersion, when non-nil, fails with ErrConcurrentModification unless the
	// stored version matches.
	IfVersion *meta.ObjVersion
}

//counterfeiter:generate . MetadataStore

// MetadataStore is the admin API's metadata sections: user, bucket and
// bucket.instance.
type MetadataStore interface {
	Get(ctx context.Context, section, key string) (MetadataEntry, error)
	Put(ctx context.Context, section, key string, e MetadataEntry, opts PutMetadataOptions) error
	Remove(ctx context.Context, section, key string) error
	List(ctx context.Context, section, marker string, max int) (keys []string, next string, more bool, err error)
}
```

Then generate and commit the fakes:

```sh
go generate -tags=ceph_preview ./internal/op/
ls internal/op/opfakes/    # fake_zone_info.go ... fake_metadata_store.go, plus fake_authorizer.go and fake_metrics.go
```

Add `//counterfeiter:generate . Authorizer` and `//counterfeiter:generate . Metrics` too, so A, Z and the s3 specs can fake them.

- [ ] **Step 7: `policy.Action`**

`internal/policy/action.go` transcribes `rgw::IAM::action_t` from [`src/rgw/rgw_iam_policy.h:46-129`](https://github.com/ceph/ceph/blob/7ed73efc1beb034b76e150439972959857600920/src/rgw/rgw_iam_policy.h#L46-L129) (the s3 block through `s3All`) with the names from the `actpairs` table in `src/rgw/rgw_iam_policy.cc` (82 `s3:` rows). Z appends the iam, sts, sns and organizations blocks and adds `Parse` and `Eval`.

```go
package policy

// Action is one IAM action, numbered as rgw::IAM::action_t numbers them so
// a Policy's action bitset lines up with radosgw's.
type Action uint16

// The S3 actions, in action_t order.
const (
	S3GetObject Action = iota
	S3GetObjectVersion
	S3PutObject
	S3GetObjectAcl
	S3GetObjectVersionAcl
	S3PutObjectAcl
	S3PutObjectVersionAcl
	S3DeleteObject
	S3DeleteObjectVersion
	S3ListMultipartUploadParts
	S3AbortMultipartUpload
	S3GetObjectTorrent
	S3GetObjectVersionTorrent
	S3RestoreObject
	S3CreateBucket
	S3DeleteBucket
	S3ListBucket
	S3ListBucketVersions
	S3ListAllMyBuckets
	S3ListBucketMultipartUploads
	S3GetAccelerateConfiguration
	S3PutAccelerateConfiguration
	S3GetBucketAcl
	S3PutBucketAcl
	S3GetBucketOwnershipControls
	S3PutBucketOwnershipControls
	S3GetBucketCORS
	S3PutBucketCORS
	S3GetBucketVersioning
	S3PutBucketVersioning
	S3GetBucketRequestPayment
	S3PutBucketRequestPayment
	S3GetBucketLocation
	S3GetBucketPolicy
	S3DeleteBucketPolicy
	S3PutBucketPolicy
	S3GetBucketNotification
	S3PutBucketNotification
	S3GetBucketLogging
	S3PutBucketLogging
	S3PostBucketLogging
	S3GetBucketTagging
	S3PutBucketTagging
	S3GetBucketWebsite
	S3PutBucketWebsite
	S3DeleteBucketWebsite
	S3GetLifecycleConfiguration
	S3PutLifecycleConfiguration
	S3PutReplicationConfiguration
	S3GetReplicationConfiguration
	S3DeleteReplicationConfiguration
	S3GetObjectTagging
	S3PutObjectTagging
	S3DeleteObjectTagging
	S3GetObjectVersionTagging
	S3PutObjectVersionTagging
	S3DeleteObjectVersionTagging
	S3PutBucketObjectLockConfiguration
	S3GetBucketObjectLockConfiguration
	S3PutObjectRetention
	S3GetObjectRetention
	S3PutObjectLegalHold
	S3GetObjectLegalHold
	S3BypassGovernanceRetention
	S3GetBucketPolicyStatus
	S3PutPublicAccessBlock
	S3GetPublicAccessBlock
	S3DeletePublicAccessBlock
	S3GetBucketPublicAccessBlock
	S3PutBucketPublicAccessBlock
	S3DeleteBucketPublicAccessBlock
	S3GetBucketEncryption
	S3PutBucketEncryption
	S3DescribeJob
	S3GetObjectAttributes
	S3GetObjectVersionAttributes
	S3ReplicateDelete
	S3ReplicateObject
	S3GetObjectVersionForReplication
	S3ReplicateTags
	S3PutAccountPublicAccessBlock
	S3GetAccountPublicAccessBlock
	S3All
	// s3AllCount is where the next service's actions start.
	s3AllCount
)

// String returns the "s3:Name" form, or "s3:*" for S3All.
func (a Action) String() string

// ParseAction parses the "s3:Name" form; ok is false for a name radosgw does not know.
func ParseAction(s string) (a Action, ok bool)
```

The s3 block is the union of both floors plus ceph main's additions, not v19.2.6's table (index, "Z finding (a)"): `rgw_iam_policy.h`'s enum has 74 s3 rows at v19.2.6 and 81 at v20.2.4 — Tentacle adds `s3PostBucketLogging`, `s3GetObjectAttributes`, `s3GetObjectVersionAttributes`, `s3ReplicateDelete`, `s3ReplicateObject`, `s3GetObjectVersionForReplication` and `s3ReplicateTags` — and `S3PutAccountPublicAccessBlock`/`S3GetAccountPublicAccessBlock` exist at neither tag. The numbering is never persisted, so the superset is harmless as such; what gates it is Z Task 1's `policy.Known(a, release)`: `MatchAction` skips actions the release does not know and Z's parser answers `` `<s>` is not valid action `` for a name no known action matches (Z Task 3), as radosgw's `actpairs` loop does (rgw_iam_policy.cc:629-641 at v19.2.6). `String` and `ParseAction` share one `[...]string` table indexed by Action, built from the constant names by dropping the `S3` prefix and prefixing `s3:`; `S3All` is `s3:*`. The spec asserts `S3GetObject.String() == "s3:GetObject"`, `S3All.String() == "s3:*"`, `ParseAction("s3:ListBucket") == S3ListBucket`, `ParseAction("s3:Nope")` is not ok, and a round trip over every value below `s3AllCount`.

- [ ] **Step 8: `cephconf.Options`**

`internal/cephconf/options.go`. `rados_conf_get` renders every option type as text through `Option::to_str` ([`src/common/options.cc:40-66`](https://github.com/ceph/ceph/blob/v19.2.6/src/common/options.cc#L40-L66)): booleans as `true`/`false`, sizes and durations as their bare count (bytes, seconds or milliseconds), strings as stored. The typed accessors parse exactly those forms.

```go
package cephconf

// Getter reads a Ceph option's current value by name; radosclient.Cluster satisfies it.
type Getter interface {
	ConfigGet(name string) (string, error)
}

// MapGetter is a Getter over a map, for tests and the driver skeleton.
type MapGetter map[string]string

// ConfigGet returns the mapped value, or ErrUnknownOption for a missing key.
func (m MapGetter) ConfigGet(name string) (string, error) {
	v, ok := m[name]
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrUnknownOption, name)
	}
	return v, nil
}

// ErrUnknownOption is returned for an option name librados does not know.
var ErrUnknownOption = errors.New("cephconf: unknown option")

// Options is typed access to rgw_* and other Ceph options through a Getter.
// librados applies the mon config store during connect, so an Options over a
// connected Cluster sees what Rook set centrally.
type Options struct{ g Getter }

// NewOptions returns Options reading through g.
func NewOptions(g Getter) *Options { return &Options{g: g} }

// String returns the option's text as librados renders it.
func (o *Options) String(name string) (string, error)

// Int64 parses an int, uint or a duration option's count.
func (o *Options) Int64(name string) (int64, error)

// Uint64 parses a uint or size option.
func (o *Options) Uint64(name string) (uint64, error)

// Bool parses "true" or "false".
func (o *Options) Bool(name string) (bool, error)

// Size parses a size option, rendered by librados as a bare byte count.
func (o *Options) Size(name string) (uint64, error)

// Seconds parses a secs option or an int option counting seconds.
func (o *Options) Seconds(name string) (time.Duration, error)

// Millis parses a millisecs option.
func (o *Options) Millis(name string) (time.Duration, error)

// List splits a string option on commas and spaces, dropping empty items, as
// ceph::split and get_str_list(", ") do for rgw_enable_apis and rgw_dns_name.
func (o *Options) List(name string) ([]string, error)
```

Every parse failure is `fmt.Errorf("option %s: %w", name, err)`. Specs use a `MapGetter{"rgw_max_chunk_size": "4194304", "rgw_enable_apis": "s3, s3website, swift", "rgw_gc_processor_period": "3600", "rgw_cache_enabled": "true", "rgw_dns_name": ""}` and assert `Size` gives 4194304, `List` gives `["s3","s3website","swift"]` and `[]` for the empty value, `Seconds` gives an hour, `Bool` is true, an unknown name errors with `ErrUnknownOption`, and a non-numeric value errors naming the option.

- [ ] **Step 9: `frontend.Spec` and `ParseBeast`**

`internal/frontend/spec.go`. The grammar is `RGWFrontendConfig::parse_config` ([`src/rgw/rgw_frontend.cc:17-48`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_frontend.cc#L17-L48)): the `rgw_frontends` value splits on `,` into frontends ([`rgw_appmain.cc:195`](https://github.com/ceph/ceph/blob/7ed73efc1beb034b76e150439972959857600920/src/rgw/rgw_appmain.cc#L195)); one frontend splits on spaces, the first token is the framework name, each other token is `key=value` or a bare `key` with an empty value, and a key may repeat. The keys are `AsioFrontend::init` and `ssl_init` ([`rgw_asio_frontend.cc:592-740`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_asio_frontend.cc#L592-L740), `916-1045`).

```go
package frontend

// DefaultRequestTimeout is beast's REQUEST_TIMEOUT (rgw_asio_frontend.h:11).
const DefaultRequestTimeout = 65 * time.Second

// DefaultMaxHeaderSize and MaxHeaderSizeCap are beast's header_limit default
// and its parse-buffer ceiling (rgw_asio_frontend.cc:420, rgw_asio_frontend_connection.h:17).
const (
	DefaultMaxHeaderSize = 16384
	MaxHeaderSizeCap     = 65536
)

// Spec is one beast frontend's configuration.
type Spec struct {
	// Ports and SSLPorts listen on every address; Endpoints and SSLEndpoints
	// are "host:port" strings, the port defaulting to 80 and 443.
	Ports, SSLPorts         []int
	Endpoints, SSLEndpoints []string
	// SSLCertificate is a PEM file path holding the chain and, when
	// SSLPrivateKey is empty, the key too.
	SSLCertificate, SSLPrivateKey string
	// SSLOptions are the colon-separated ssl_options items; nil means radosgw's
	// default "no_sslv2:no_sslv3:no_tlsv1:no_tlsv1_1" when a certificate is set.
	SSLOptions []string
	// SSLCiphers is the colon-separated OpenSSL cipher list for TLS 1.2 and below.
	SSLCiphers []string
	// TCPNoDelay is nil when the key is absent; radosgw enables it only for "1".
	TCPNoDelay *bool
	RequestTimeout time.Duration
	// MaxConnectionBacklog is parsed and reported; Go's listener takes the
	// kernel's backlog, so frontend.New logs it as ignored.
	MaxConnectionBacklog int
	MaxHeaderSize        int
	// Unknown holds every key the parser did not recognise, for the caller to log.
	Unknown map[string][]string
}

// ParseBeast parses one frontend entry ("beast port=80 ssl_port=443 ...").
// A framework other than beast is ErrNotBeast; an unparsable port, endpoint,
// timeout or header size is an error naming the key, as radosgw refuses them.
func ParseBeast(entry string) (Spec, error)

// ParseFrontends parses an rgw_frontends value: the beast entry is returned
// and every other entry's framework name is listed in others for the caller
// to log; an empty value is radosgw's default "beast port=7480".
func ParseFrontends(value string) (spec Spec, others []string, err error)

// ErrNotBeast is returned for a frontend entry whose framework is not beast.
var ErrNotBeast = errors.New("frontend: not a beast frontend")
```

Rules, each pinned by a spec entry: `port` and `ssl_port` parse as 1-65535; `endpoint` parses `host:port`, `host` (port 80, 443 for `ssl_endpoint`), `[v6]:port` and `[v6]`; `request_timeout_ms` invalid keeps the default and is reported in `Unknown["request_timeout_ms"]` (radosgw warns and keeps the default); `max_header_size` above the cap is capped; `tcp_nodelay` is true only for `"1"`; `ssl_options` and `ssl_ciphers` split on `:`; `ssl_private_key` without `ssl_certificate` is an error ([`rgw_asio_frontend.cc:972-975`](https://github.com/ceph/ceph/blob/7ed73efc1beb034b76e150439972959857600920/src/rgw/rgw_asio_frontend.cc#L972-L975)); `prefix`, `so_reuseport`, `ssl_ciphersuites`, `tls_groups`, `ssl_reload` and anything else land in `Unknown`; Rook's exact strings parse: `beast port=80`, `beast port=8080 ssl_port=443 ssl_certificate=/etc/ceph/private/rgw-cert.pem ssl_private_key=/etc/ceph/private/rgw-key.pem`, `beast ssl_port=443 ssl_certificate=/etc/ceph/private/rgw-cert.pem ssl_options=no_compression:no_tlsv1_2` (`rook pkg/operator/ceph/object/config.go:67-125`).

- [ ] **Step 10: The in-memory store**

`internal/memstore` implements every interface in `op` over maps under one mutex, with radosgw's observable semantics and none of its layout. It is a regular package (not `_test`) so every unit's specs and the pipeline baseline import it.

```go
package memstore

// Config seeds a Store.
type Config struct {
	Release   denc.Release
	Zone      meta.Zone
	ZoneGroup meta.ZoneGroup
	Params    meta.ZoneParams
	Realm     meta.Realm
	Period    meta.Period
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// Store is an in-memory implementation of every store interface in op.
type Store struct {
	cfg Config
	mu  sync.Mutex
	ver uint64 // the next cls_version

	users     map[string]*op.UserRecord // by meta.UserID.String()
	keys      map[string]string         // access key -> user id string
	emails    map[string]string         // email -> user id string
	buckets   map[string]*bucket        // "tenant/name" -> bucket
	instances map[string]*bucket        // bucket id -> bucket
	uploads   map[string]*upload        // bucket id + "\x00" + key name + "\x00" + upload id
	usage     []op.UsageEntry
	metadata  map[string]map[string]op.MetadataEntry // section -> key -> entry
}

type bucket struct {
	rec     op.BucketRecord
	objects map[string]*object // index key name -> object
}

type object struct {
	state op.ObjectState
	data  []byte
}

type upload struct {
	up    op.Upload
	parts map[int]*part
}

type part struct {
	op.Part
	data []byte
}

// New returns an empty store. A zero Config is a Squid zone named "default" in
// a zonegroup named "default" whose one placement target, "default-placement",
// maps to default.rgw.buckets.{data,index,non-ec}.
func New(cfg Config) *Store

// AddUser stores info as a user with a fresh version and index entries, for test setup.
func (s *Store) AddUser(info meta.UserInfo) *op.UserRecord

// Usage returns a copy of every logged usage entry.
func (s *Store) Usage() []op.UsageEntry

var (
	_ op.ZoneInfo       = (*Store)(nil)
	_ op.UserStore      = (*Store)(nil)
	_ op.BucketStore    = (*Store)(nil)
	_ op.ObjectStore    = (*Store)(nil)
	_ op.MultipartStore = (*Store)(nil)
	_ op.StatsStore     = (*Store)(nil)
	_ op.UsageLogger    = (*Store)(nil)
	_ op.MetadataStore  = (*Store)(nil)
)
```

Semantics per method, each pinned by a spec in the file named:

- **users** (`user_test.go`): `GetUser` of a missing id is `op.ErrNoSuchUser`; `GetUserByAccessKey` and `GetUserByEmail` resolve through the index maps; `PutUser` with `Exclusive` on an existing user is `op.ErrUserAlreadyExists`, with a mismatched `IfVersion` is `op.ErrConcurrentModification`, otherwise stores a copy, bumps `rec.Version.Ver` and rewrites the key and email indexes from `Info.AccessKeys` and `Info.Email`; `RemoveUser` drops the user and its indexes; `ListUserBuckets` returns the owner's buckets sorted by name after `marker`, at most `max`, `more` when any remain, `next` the last name returned.
- **buckets** (`bucket_test.go`): `GetBucket` of a missing name is `op.ErrNoSuchBucket`; `CreateBucket` of an existing name with `Exclusive` is `op.ErrBucketAlreadyExists`, otherwise creates entry point and instance with `Bucket.ID` = `<zone id>.<counter>.1` and `Marker` equal to it (radosgw's shape), `Owner`, `PlacementRule`, `Zonegroup`, `Quota`, `Attrs`, `CreationTime` = now, `Version{Ver: 1, Tag: <24 random alphanumerics>}`; `DeleteBucket` with objects is `op.ErrBucketNotEmpty`; `PutBucketInfo` and `PutBucketAttrs` with a `Version` other than the stored one are `op.ErrConcurrentModification`, otherwise apply and bump; `ListObjects` sorts index key names, skips those not starting with `Prefix`, starts after `Marker`, rolls names containing `Delimiter` after the prefix into `CommonPrefixes` (one entry per distinct prefix through the delimiter, in order, each counting one toward `MaxKeys`), returns at most `MaxKeys` entries and prefixes together, sets `Truncated` and `NextMarker` to the last key or prefix returned; `NS` "multipart" lists the upload namespace instead.
- **objects** (`object_test.go`): `StatObject` of a missing key returns `Exists: false` and no error; `PutObject` reads the body, fails with `op.ErrEntityTooLarge` when `Size >= 0` and the body is longer, sets `ETag` to the hex MD5 unless `p.ETag` is set, stores `p.Attrs` plus `user.rgw.etag`, honours `IfMatch`/`IfNoneMatch` (`"*"` is any) with `op.ErrPreconditionFailed`, and returns `PutResult{ETag, Size, Mtime, Epoch}` with `Epoch` from the version counter; `ReadObject` writes `rng` of the data to the sink and is `op.ErrInvalidRange` when `Offset > Size`; `DeleteObject` of a missing key is `op.ErrNoSuchKey`; `CopyObject` copies data and attrs (`ReplaceAttrs` replaces, else merges `p.Attrs` over the source's) and computes a new ETag equal to the source's; `SetObjectAttrs` applies set then rm.
- **multipart** (`multipart_test.go`): `CreateUpload` returns an `Upload` with a 32-character alphanumeric `ID`; `GetUpload` of an unknown id is `op.ErrNoSuchUpload`; `PutPart` stores the part with its MD5 ETag, replacing an existing number; `CopyPart` copies `rng` of `src`; `ListParts` sorts by number after `marker`, at most `max`; `ListUploads` sorts by key then id with the two markers, `Prefix` and `Delimiter` as in `ListObjects`; `Complete` requires strictly increasing numbers (`op.ErrInvalidPartOrder`), each ETag matching (`op.ErrInvalidPart`), every part but the last at least 5 MiB when more than one part (`op.ErrEntityTooSmall`), concatenates the data, sets the ETag to `hex(md5(concatenated raw MD5 digests)) + "-" + count`, stores the object with the upload's attrs, and removes the upload; `Abort` removes the upload and its parts.
- **stats and quota** (`stats_test.go`): `BucketStats` sums the bucket's objects (`SizeRounded` rounds each object up to 4 KiB, as radosgw accounts); `UserStats` sums the owner's buckets; `CheckQuota` fails with `op.ErrQuotaExceeded` when `rec.Info.Quota.Enabled` and either `MaxSize >= 0 && Size+addBytes > MaxSize` or `MaxObjects >= 0 && NumObjects+addObjs > MaxObjects`, then the same against the owning user's `UserQuota`.
- **usage and metadata** (`stats_test.go`): `Log` appends; `Get` of a missing key is `op.ErrNotFound`; `Put` with a mismatched `IfVersion` is `op.ErrConcurrentModification`; `List` pages keys in sorted order after `marker`.
- **zone**: `Release`, `Zone`, `ZoneGroup`, `ZoneParams`, `Realm`, `Period` return the config; `Placement` resolves through `Params.PlacementPools[rule.Name]` and the storage class (`STANDARD` when empty), `op.ErrInvalidLocationConstraint` for an unknown rule.

Write the specs first (one `Describe` per file, each `It` naming its case with got/want in the message), run to see them fail, implement, run to see them pass.

- [ ] **Step 11: Gate and commit**

```sh
make generate-check && make check
```

Two commits: `feat(op): freeze the op contract, store interfaces and fakes` (`internal/op`, `internal/policy`, `internal/cephconf`, `internal/frontend`) and `feat(memstore): add the in-memory store implementing every op interface`. Open the draft PR titled `feat: phase 1 interface freeze (unit G task 1)`; wave 1 dispatches when it merges.

---

### Task 2: Seam prerequisites: `InstanceID`, `Stats`, `NoConfigFile`, the in-flight limiter

**Files:**
- Modify: `internal/radosclient/cluster.go` (`InstanceID` on `Cluster`; `Stats`, `StatsReporter`), `internal/radosclient/goceph/cluster.go` (`Config.NoConfigFile`, `Config.MaxInflightOps`, `Config.MaxInflightBytes`, `InstanceID`, `Stats`, the default-config-file tolerance), `internal/radosclient/goceph/pool.go` (acquire and release around every operate), `internal/radosclient/goceph/translate_test.go`, `internal/radosclient/goceph/goceph_integration_test.go`, `docs/cgo-limitations.md`
- Create: `internal/radosclient/goceph/limiter.go`, `internal/radosclient/goceph/limiter_test.go`
- Modify: `go.mod` (`golang.org/x/sync` becomes a direct requirement at v0.21.0, the version already in `go.sum`)

**Interfaces:**
- Consumes: the seam and goceph as phase 0 left them; go-ceph's `Conn.GetInstanceID`, `Conn.GetConfigOption`.
- Produces:

```go
package radosclient

// Cluster gains:
//	// InstanceID is the client's global id, rados_get_instance_id, which
//	// radosgw puts in transaction ids and the host id.
//	InstanceID() uint64

// Stats is a snapshot of the transport's operation counters.
type Stats struct {
	ReadOps, WriteOps       uint64 // operations submitted, cumulative
	ReadBytes, WriteBytes   uint64 // payload bytes submitted, cumulative
	InflightOps             int64  // operations submitted and not yet completed
	InflightBytes           int64
	ThrottleWaits           uint64 // submissions that parked on the in-flight limiter
}

// StatsReporter is satisfied by a Cluster that counts its operations; the
// metrics package type-asserts it.
type StatsReporter interface {
	Stats() Stats
}
```

```go
package goceph

// Config gains:
//	NoConfigFile     bool  // --no-config-file: read no ceph.conf at all
//	MaxInflightOps   int   // 0 derives from objecter_inflight_ops after connect
//	MaxInflightBytes int64 // 0 derives from objecter_inflight_op_bytes after connect
```

**Fork PRs this unit needs: none.** Every binding G uses is already in go-ceph upstream or on the fork's `rgw-go` branch at cbf97f85fcf5: `rados_get_instance_id` (`Conn.GetInstanceID`), `rados_conf_get` (`Conn.GetConfigOption`), the async operate and the op steps from phase 0. The pin stays. The seam gaps the index lists for other units (`00-index.md`, "Shared interfaces and seam gaps"), so their plans own them:

| Gap | Owner | What that plan does |
|---|---|---|
| `Cluster.FSID(ctx) (string, error)` for `/admin/info`'s `cluster_id` | N | seam method over go-ceph's `Conn.GetFSID` (upstream, [`rados/conn.go:249`](https://github.com/jhoblitt/go-ceph/blob/cbf97f85fcf5000ca4beca4a7fbf2ee746fa965d/rados/conn.go#L249)); no fork change |
| `OpFlagFullTry` for delete and GC under `pool_full_try` | W | seam constant `OpFlagFullTry OpFlags = 1 << 6` (`LIBRADOS_OPERATION_FULL_TRY = 64`) and its translation to go-ceph's `OperationFullTry` (upstream, [`rados/operation_flags.go:30`](https://github.com/jhoblitt/go-ceph/blob/cbf97f85fcf5000ca4beca4a7fbf2ee746fa965d/rados/operation_flags.go#L30)); no fork change |
| `ListObjectsFrom(ctx, token string, max int, fn) (next string, more bool, err error)` for admin listing markers | N | seam method over go-ceph's pg-hash `Iter.Token`/`Iter.Seek` (upstream, [`rados/object_iter.go:29-35`](https://github.com/jhoblitt/go-ceph/blob/cbf97f85fcf5000ca4beca4a7fbf2ee746fa965d/rados/object_iter.go#L29-L35)); markers are rgw-go's own opaque tokens (decision D5); no fork change |
| lock steps composed with `AssertExists` for CompleteMultipart | P | a new `internal/cls/lock` class package over `Execer`; no seam change |
| `rados_aio_cancel` | W (phase 1: none) | stays open in `docs/cgo-limitations.md`; the byte budget below counts an abandoned operation until its completion fires |

If any of those turns out to need the fork after all, the flow is: branch from `jhoblitt/rgw-go` in `/home/jhoblitt/github/go-ceph`, one `//go:build ceph_preview` file per C function with a testify test in go-ceph's style, `make api-update`, PR into the fork's `rgw-go` branch, then `go mod edit -replace github.com/ceph/go-ceph=github.com/jhoblitt/go-ceph@<new head>` and `go mod tidy` with the sandbox disabled.

The limiter (decision D8): librados's objecter throttle blocks the submitting OS thread inside C once `objecter_inflight_ops` (1024) or `objecter_inflight_op_bytes` (100 MiB) is reached, and a blocked cgo call pins a thread, which is what §7 forbids. The transport parks the goroutine in Go instead, on two weighted semaphores sized just under librados's own limits so librados's throttle is never reached, and counts every operation while it is in flight, abandoned ones included, since the completion still owns its buffers.

- [ ] **Step 1: Write the failing limiter specs (no cluster)**

`internal/radosclient/goceph/limiter_test.go`, package `goceph_test`, through `export_test.go` accessors:

```go
var _ = Describe("limiter", func() {
	It("admits up to the op limit and parks the next submission until a release", func(ctx SpecContext) {
		l := goceph.NewLimiter(2, 1<<20)
		Expect(l.Acquire(ctx, 10)).To(Succeed())
		Expect(l.Acquire(ctx, 10)).To(Succeed())
		parked := make(chan error, 1)
		go func() { parked <- l.Acquire(ctx, 10) }()
		Consistently(parked).WithTimeout(50 * time.Millisecond).ShouldNot(Receive(), "third op must wait")
		l.Release(10)
		Eventually(parked).WithTimeout(time.Second).Should(Receive(Succeed()), "released op admits the waiter")
		Expect(l.Stats().ThrottleWaits).To(BeEquivalentTo(1), "one wait counted")
	})
	It("parks on bytes when ops remain", func(ctx SpecContext) {
		l := goceph.NewLimiter(100, 1000)
		Expect(l.Acquire(ctx, 900)).To(Succeed())
		parked := make(chan error, 1)
		go func() { parked <- l.Acquire(ctx, 200) }()
		Consistently(parked).WithTimeout(50 * time.Millisecond).ShouldNot(Receive())
		l.Release(900)
		Eventually(parked).WithTimeout(time.Second).Should(Receive(Succeed()))
	})
	It("clamps a payload larger than the whole budget so it can never deadlock", func(ctx SpecContext) {
		l := goceph.NewLimiter(4, 1000)
		Expect(l.Acquire(ctx, 5000)).To(Succeed(), "oversized op takes the whole budget")
		Expect(l.Stats().InflightBytes).To(BeEquivalentTo(1000))
		l.Release(5000)
		Expect(l.Stats().InflightBytes).To(BeZero())
	})
	It("returns the context error while parked", func() {
		l := goceph.NewLimiter(1, 1000)
		Expect(l.Acquire(context.Background(), 1)).To(Succeed())
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		Expect(l.Acquire(ctx, 1)).To(MatchError(context.DeadlineExceeded))
		Expect(l.Stats().InflightOps).To(BeEquivalentTo(1), "a refused acquire holds nothing")
	})
	It("derives its limits from the objecter options with a margin", func() {
		ops, bytes := goceph.DeriveLimits(map[string]string{"objecter_inflight_ops": "1024", "objecter_inflight_op_bytes": "104857600"})
		Expect(ops).To(Equal(1008), "objecter_inflight_ops less 16 for librados's own operations")
		Expect(bytes).To(BeEquivalentTo(98304000), "fifteen sixteenths of objecter_inflight_op_bytes")
	})
})
```

`translate_test.go` gains a table for the payload weight of an op: a `ReadOp` with `Read(0, 4<<20)` and `Stat` weighs 4 MiB; a `WriteOp` with `WriteFull` of 3 bytes, `Append` of 2 and `Exec` with a 5-byte input weighs 10; an op with no payload weighs 0 and still counts one operation.

- [ ] **Step 2: Run to fail, implement `limiter.go`**

```go
package goceph

// limiter is the in-flight limiter: two weighted semaphores,
// operations and payload bytes, sized under librados's objecter throttle so a
// submission parks a goroutine here instead of an OS thread inside C.
type limiter struct {
	ops, bytes  *semaphore.Weighted
	maxBytes    int64
	inflightOps atomic.Int64
	inflightB   atomic.Int64
	waits       atomic.Uint64
}

func newLimiter(maxOps int, maxBytes int64) *limiter

// acquire takes one operation and n bytes, clamped to the budget, parking on
// ctx. It holds nothing when it returns an error.
func (l *limiter) acquire(ctx context.Context, n int64) error {
	n = min(n, l.maxBytes)
	if !l.ops.TryAcquire(1) {
		l.waits.Add(1)
		if err := l.ops.Acquire(ctx, 1); err != nil {
			return err
		}
	}
	if err := l.bytes.Acquire(ctx, n); err != nil {
		l.ops.Release(1)
		return err
	}
	l.inflightOps.Add(1)
	l.inflightB.Add(n)
	return nil
}

// release returns what acquire took for n bytes.
func (l *limiter) release(n int64)

// deriveLimits sizes the limiter from librados's own throttle: 16 operations
// are left for librados's watch, mon and osdmap traffic and one sixteenth of
// the byte budget for their payloads.
func deriveLimits(get func(string) (string, error)) (ops int, bytes int64)

// weight is an op's payload: read lengths, write and append data, exec inputs.
func weight(steps []radosclient.Step) int64
```

The byte semaphore's `Acquire` of a weight above its size blocks forever in x/sync, which is why `acquire` clamps first. `deriveLimits` falls back to 1008 and 98304000 when an option is unreadable, logging once.

In `cluster.go`: `Connect` builds the limiter after `conn.Connect()` succeeds, from `cfg.MaxInflightOps`/`MaxInflightBytes` when set and `deriveLimits(conn.GetConfigOption)` otherwise; `configure` skips `ReadDefaultConfigFile` when `cfg.NoConfigFile`, and when no file was named and the default read fails with ENOENT it logs `slog.Warn("no ceph.conf found, continuing with defaults")` and continues, as ceph's `parse_config_files` does for the default search path. Add `InstanceID()` returning `conn.GetInstanceID()` and `Stats()`.

In `pool.go`: `Read` and `Write` call `p.state.cluster.limit.acquire(ctx, weight(op.Steps()))` after `translateRead`/`translateWrite` and before `acquire`-ing the handle; the matching `release` runs where the handle is finished: in the sync branches after `Operate` returns, and in the async branches inside the `done` closure the reaper runs at completion (so an abandoned operation keeps its budget until librados is finished with its buffers). Bump `ReadOps`/`WriteOps` and the byte counters at submission.

- [ ] **Step 3: Seam and unit specs**

`internal/radosclient/cluster.go`: add `InstanceID() uint64` to `Cluster` with its doc comment, the `Stats` struct and `StatsReporter`. `ops_test.go` gains nothing; the phase 0 fakes of `Cluster` (if any test fake implements it) gain the method.

Run `go test -tags=ceph_preview ./internal/radosclient/...`; expected PASS.

- [ ] **Step 4: Integration spec `[cluster]`**

`goceph_integration_test.go` gains, in the per-mode `Context`:

```go
It("never lets more than the op limit into librados", func(ctx SpecContext) {
	cl, err := goceph.Connect(ctx, goceph.Config{ConfigFile: conf, Mode: mode, MaxInflightOps: 8, MaxInflightBytes: 1 << 20})
	Expect(err).NotTo(HaveOccurred())
	defer cl.Close()
	pool, err := cl.Pool(ctx, cephtest.TestPool, "")
	Expect(err).NotTo(HaveOccurred())
	defer pool.Close()
	// write a 64 KiB object once, then 256 concurrent reads of it
	var peak atomic.Int64
	var g errgroup.Group
	for i := range 256 {
		g.Go(func() error {
			op := radosclient.NewReadOp()
			op.Read(0, 64<<10)
			_, err := pool.Read(ctx, "limiter-"+strconv.Itoa(i%4), op, 0)
			s := cl.(radosclient.StatsReporter).Stats()
			for {
				cur := peak.Load()
				if s.InflightOps <= cur || peak.CompareAndSwap(cur, s.InflightOps) {
					break
				}
			}
			return err
		})
	}
	Expect(g.Wait()).To(Succeed())
	Expect(peak.Load()).To(BeNumerically("<=", 8), "in-flight ops observed")
	Expect(cl.(radosclient.StatsReporter).Stats().ThrottleWaits).To(BeNumerically(">", 0), "some reads parked")
})
It("reports a non-zero instance id", func(ctx SpecContext) {
	Expect(cluster.InstanceID()).NotTo(BeZero())
})
```

Run against the Squid cluster:

```sh
make cluster-up RELEASE=squid
make integration RELEASE=squid
```

- [ ] **Step 5: Record the limitation and commit**

Add to `docs/cgo-limitations.md` under Inherent: "The objecter throttle blocks the submitting thread" with the evidence (`objecter_inflight_ops` and `objecter_inflight_op_bytes` in `src/common/options/global.yaml.in`; `Objecter::_op_submit_with_budget` waits inside the call), the status (goceph parks submissions in Go under a limiter sized 16 ops and one sixteenth below librados's own), and the measure (throttle waits and thread count under the seam microbenchmark, T-a). `make check`, then commit `feat(goceph): add the in-flight limiter, instance id and stats to the seam`.

---

### Task 3: `s3` request parsing and the dispatch table

**Files:**
- Create: `internal/s3/doc.go`, `internal/s3/request.go`, `internal/s3/dispatch.go`, `internal/s3/s3_suite_test.go`, `internal/s3/request_test.go`, `internal/s3/dispatch_test.go`

**Interfaces:**
- Consumes: `op.Request`, `op.Scope`, `op.PayloadForms`, the `op.Err*` sentinels, `meta.ObjKey`.
- Produces:

```go
package s3

// Config is what the handler needs from Ceph configuration.
type Config struct {
	// DNSNames are rgw_dns_name's entries plus the zonegroup's hostnames;
	// empty means path-style addressing only.
	DNSNames []string
	// RelaxedBucketNames is rgw_relaxed_s3_bucket_names.
	RelaxedBucketNames bool
	// MaxConcurrent is rgw_max_concurrent_requests; 0 means unlimited.
	MaxConcurrent int
	// TransIDSuffix is op.TransIDSuffix(instance id, zone name).
	TransIDSuffix string
	// ServerHeader is the Server header value, "Ceph Object Gateway (<release>)".
	ServerHeader string
}

// Parsed is a request after Host, path and query are parsed, before auth.
type Parsed struct {
	Req *op.Request
	// ExplicitTenant is set when the URL named "tenant:bucket"; otherwise the
	// tenant is the identity's once auth has run.
	ExplicitTenant bool
}

// ParseRequest parses Host, path and query once, in RGWREST::preprocess and
// RGWHandler_REST_S3::init_from_header's order, and validates the tenant,
// bucket and object names. Errors are op.Error values.
func ParseRequest(req *http.Request, cfg Config, now time.Time) (Parsed, error)

// Route is one row of the dispatch table.
type Route struct {
	// Name is radosgw's op name, the handler key and the metrics label.
	Name  string
	Scope op.Scope
	// Payloads is the signed payload forms the op takes at the release
	// Dispatch ran for; the authenticator refuses any other form.
	Payloads op.PayloadForms
}

// Dispatch selects the route for r in radosgw's method, scope and subresource
// precedence at release rel: a table row that exists at one release only is
// skipped on the other, so the request falls through as that release's op_*
// does. A method or subresource radosgw has no op for is ErrMethodNotAllowed.
// The route carries its payload forms at rel.
func Dispatch(r *op.Request, rel denc.Release) (Route, error)
```

Parsing rules, each from the cited C++ and each pinned by a spec entry:

- **Method** (`rgw_rest.cc`, `op_from_method`): GET, PUT, DELETE, HEAD, POST, OPTIONS; anything else is `op.ErrMethodNotAllowed`.
- **Path** (`rgw_common.cc:1790-1822`, `RGWREST::preprocess`): Go's `URL.Path` is the path decoded once with `+` left alone, exactly `url_decode(in_query=false)`; `RawPath` is `req.URL.EscapedPath()`. A NUL byte anywhere in the decoded path is `op.ErrInvalidRequest` (`ERR_ZERO_IN_URL`).
- **Query** (`RGWHTTPArgs::parse`, `rgw_common.cc`): `url.ParseQuery` on `req.URL.RawQuery` (`+` is a space, `%XX` decodes); a key containing `X-Amz-` is lowercased except for its dashes; a malformed escape is `op.ErrInvalidArgument`.
- **Host** (`rgw_rest.cc:2027-2040`): drop everything from the first `:` unless the value starts with `[`, then take the bracketed literal; lowercase. Virtual hosting (`rgw_find_host_in_domains`, [`:216-240`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L216-L240), and `:2112-2119`): with `DNSNames` non-empty, a host equal to a name is path-style, a host ending in `.` + a name has the bucket in the subdomain, and a host that matches no name, is not an IP literal (`looks_like_ip_address`, `rgw_rest_s3.h`), and passes `validate_bucket_name` is itself the bucket (the CNAME case). With `DNSNames` empty, the Host never names a bucket.
- **Bucket and object** (`init_from_header`): the first path segment is the bucket token unless the Host supplied one, the rest is the object name (slashes included); `versionId` fills `Object.Instance`. The token `tenant:bucket` splits at the first `:` (`rgw_bucket.cc:112-130`); an empty bucket after the colon is `op.ErrInvalidBucketName`; an explicit empty tenant (`:bucket`) is the legacy tenant. The tenant is `[A-Za-z0-9_]*` or `op.ErrInvalidTenantName` (`rgw_user.cc:91-100`). The URL bucket passes `RGWHandler_REST::validate_bucket_name` ([`rgw_rest.cc:1788-1813`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L1788-L1813)): 1 or 2 characters, more than 255, a `/` or a `0xff` byte are `op.ErrInvalidBucketName`; the strict `valid_s3_bucket_name` is CreateBucket's (unit M). An object name over 1024 bytes or not valid UTF-8 is `op.ErrInvalidObjectName` (`valid_s3_object_name`).
- **Everything else**: `Method`, `Header`, `Body`, `ContentLength` (`req.ContentLength`, -1 when unknown), `RemoteAddr`, `Referer`, `TLS` (`req.TLS != nil`), `Time` = `now`, `Identity` left zero for auth.

Dispatch precedence, transcribed from `rgw_rest_s3.cc` (`RGWRESTMgr_S3::get_handler`, the `RGWHandler_REST_{Service,Bucket,Obj}_S3::op_*` methods; v19.2.6 `:4596-4881`, v20.2.4 `:5171-5436`) and `rgw_rest_s3.h` (the `is_*_op` predicates, all query-key presence; v19.2.6 [`:672-761`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_bucket.cc#L672-L761), v20.2.4 [`:696-791`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_bucket.cc#L696-L791)). A key tagged `[S]` or `[T]` is a row that exists at Squid or Tentacle only: `Dispatch` skips it on the other release and the walk continues to the next key, exactly as that release's `op_*` falls through. Untagged keys exist at both tags.

| Scope | Method | First matching query key, in order | Route |
|---|---|---|---|
| service | GET | `usage` | `get_usage` (phase 3) |
| service | GET | — | `list_buckets` |
| service | HEAD | — | `list_buckets` |
| service | POST, PUT, DELETE, OPTIONS | — | 405 (IAM, STS and topics are phase 3) |
| bucket | any | `append`, `torrent`, `uploadId`, `partNumber`, `versionId` present | 405 (`exist_obj_excl_sub_resource`, [`rgw_common.h:448-455`](https://github.com/ceph/ceph/blob/7ed73efc1beb034b76e150439972959857600920/src/rgw/rgw_common.h#L448-L455)) |
| bucket | GET | `logging`, `location`, `versioning`, `website`, `mdsearch`, `acl`, `cors`, `requestPayment`, `uploads`, `lifecycle`, `policy`, `tagging`, `object-lock`, `notification`, `replication`, `policyStatus`, `publicAccessBlock`, `encryption` | `get_bucket_logging`, `get_bucket_location`, `get_bucket_versioning`, `get_bucket_website`, `get_bucket_meta_search`, `get_acls`, `get_cors`, `get_request_payment`, `list_bucket_multiparts`, `get_lifecycle`, `get_bucket_policy`, `get_bucket_tags`, `get_bucket_object_lock`, `get_bucket_notification`, `get_bucket_replication`, `get_bucket_policy_status`, `get_bucket_public_access_block`, `get_bucket_encryption` |
| bucket | GET | — | `list_bucket` (`list-type=2` selects `list_bucket_v2`; any other value is v1) |
| bucket | HEAD | `acl`, `uploads` | `get_acls`, `list_bucket_multiparts` |
| bucket | HEAD | — | `stat_bucket` |
| bucket | PUT | `logging [S]` | 405 (`op_put` returns nullptr for `logging`, v19.2.6 [`:4697-4699`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L4697-L4699)) |
| bucket | PUT | `logging [T]`, `versioning`, `website`, `tagging`, `acl`, `cors`, `requestPayment`, `lifecycle`, `policy`, `object-lock`, `notification`, `replication`, `publicAccessBlock`, `encryption` | `put_bucket_logging` (v20.2.4 [`:5244-5245`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5244-L5245)), `set_bucket_versioning`, `set_bucket_website`, `put_bucket_tags`, `put_acls`, `put_cors`, `set_request_payment`, `put_lifecycle`, `put_bucket_policy`, `put_bucket_object_lock`, `put_bucket_notification`, `put_bucket_replication`, `put_bucket_public_access_block`, `put_bucket_encryption` |
| bucket | PUT | — | `create_bucket` |
| bucket | DELETE | `logging [S]` | 405 (`op_delete` returns nullptr for `logging`, v19.2.6 [`:4744-4746`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L4744-L4746); v20.2.4's `op_delete` has no `logging` branch, [`:5288-5326`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5288-L5326), so `DELETE /b?logging` is `delete_bucket` there) |
| bucket | DELETE | `tagging`, `cors`, `lifecycle`, `policy`, `notification`, `replication`, `publicAccessBlock`, `encryption`, `website`, `mdsearch` | `delete_bucket_tags`, `delete_cors`, `delete_lifecycle`, `delete_bucket_policy`, `delete_bucket_notification`, `delete_bucket_replication`, `delete_bucket_public_access_block`, `delete_bucket_encryption`, `delete_bucket_website`, `delete_bucket_meta_search` |
| bucket | DELETE | — | `delete_bucket` |
| bucket | POST | `delete`, `logging [T]`, `mdsearch` | `multi_object_delete`, `post_bucket_logging` (v20.2.4 [`:5334-5335`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5334-L5335); at v19.2.6 `POST /b?logging` is `post_obj`, [`:4780-4800`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L4780-L4800)), `config_bucket_meta_search` |
| bucket | POST | — | `post_obj` |
| bucket | OPTIONS | — | `options_cors` |
| object | GET | `acl`, `uploadId`, `layout`, `tagging`, `attributes [T; both under R-D1]`, `retention`, `legal-hold` | `get_acls`, `list_multipart`, `get_obj_layout`, `get_obj_tags`, `get_obj_attrs` (`is_attributes_op` exists at v20.2.4 only, rgw_rest_s3.h [`:713`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.h#L713), [`:774`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.h#L774); at v19.2.6 `GET /b/k?attributes` is `get_obj`), `get_obj_retention`, `get_obj_legal_hold` |
| object | GET | — | `get_obj` |
| object | HEAD | `acl`, `uploadId` | `get_acls`, `list_multipart` |
| object | HEAD | — | `get_obj` (the handler reads `Method` to skip the body) |
| object | PUT | `acl`, `tagging`, `retention`, `legal-hold` | `put_acls`, `put_obj_tags`, `put_obj_retention`, `put_obj_legal_hold` |
| object | PUT | header `x-amz-copy-source` present, and neither `x-amz-copy-source-range` nor query `uploadId` | `copy_obj` |
| object | PUT | — | `put_obj` (covers UploadPart and UploadPartCopy, which radosgw runs through RGWPutObj) |
| object | DELETE | `tagging`, `uploadId` | `delete_obj_tags`, `abort_multipart` |
| object | DELETE | — | `delete_obj` |
| object | POST | `uploadId`, `uploads`, `restore [T]`, `select-type` | `complete_multipart`, `init_multipart`, `restore_obj` (v20.2.4 [`:5429-5430`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5429-L5430); at v19.2.6 `POST /b/k?restore` is `post_obj`, [`:4864-4881`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L4864-L4881)), `select_obj` |
| object | POST | — | `post_obj` |
| object | OPTIONS | — | `options_cors` |

Two radosgw checks in `op_get`/`op_put`/`op_delete` are dead: `sub_resource_exists("encryption")` is always false because `encryption` is not in `RGWHTTPArgs`' subresource list, so `?encryption` reaches the `is_bucket_encryption_op` branch; the table above reflects the reachable behaviour. `website` is gated in radosgw on `rgw_enable_static_website` at both releases (v19.2.6 [`:4645`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L4645), [`:4703`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L4703), [`:4767`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L4767); v20.2.4 [`:5187`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5187), [`:5249`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5249), [`:5312`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5312)); `mdsearch` is gated on `rgw_enable_mdsearch` at v20.2.4 only ([`:5194`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5194), [`:5319`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5319), [`:5339`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5339)) and routed unconditionally at v19.2.6; the table routes both and their handlers, when a later phase writes them, apply the option per release.

Release differences the tags carry (verified at both tags): no `is_ownership_controls_op` exists at either tag, so `?ownershipControls` falls through to `list_bucket`, `create_bucket` and `delete_bucket` and the table has no such rows; `PUT` and `DELETE ?logging` are 405 at Squid and `POST ?logging` is a form upload there, while Tentacle routes all three to the bucket-logging ops (or, for DELETE, to `delete_bucket`); `restore` exists at Tentacle only. The `attributes` row is where owner decision 11 lands: R-D1 serves GetObjectAttributes on both releases (the spec's Tentacle feature level), so the row is registered for both although radosgw's own tag is `[T]` and a Squid radosgw answers `?attributes` as `get_obj` with the object body. R Task 7, which registers the route's handler, records that difference in `docs/exclusions.md`, and T Task 12's known-difference list carries the Squid `test_get_object_attributes` outcome.

**Payload forms.** Each route also carries the signed payload forms its op takes (`Route.Payloads`), which `s3.Handler` hands to the authenticator (Task 4, owner decision 8), because radosgw refuses a form the op type is not listed for inside authentication, with 501 NotImplemented, before it looks the access key up (`get_auth_data_v4`: the single-chunk whitelist at [`rgw_rest_s3.cc:5899-5944`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L5899-L5944) in v19.2.6 and [`:6466-6515`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L6466-L6515) in v20.2.4, the streamed one at `:5957-5969` and [`:6528-6540`](https://github.com/ceph/ceph/blob/7ed73efc1beb034b76e150439972959857600920/src/rgw/rgw_rest_s3.cc#L6528-L6540); unit A's D-A1). radosgw's whitelist lists op types; each route maps to its op's `RGWOp::get_type()`, read at both tags. A route not in this table takes neither form: a signed non-empty body or an aws-chunked body on it is 501.

| Route | radosgw op type | Signed single chunk | aws-chunked |
|---|---|---|---|
| `put_obj` (PutObject, UploadPart, UploadPartCopy) | `RGW_OP_PUT_OBJ` | both | both |
| `create_bucket` | `RGW_OP_CREATE_BUCKET` | both | — |
| `put_acls` (bucket and object) | `RGW_OP_PUT_ACLS` | both | — |
| `put_cors` | `RGW_OP_PUT_CORS` | both | — |
| `put_bucket_encryption`, `get_bucket_encryption`, `delete_bucket_encryption` | `RGW_OP_PUT_BUCKET_ENCRYPTION`, `RGW_OP_GET_BUCKET_ENCRYPTION`, `RGW_OP_DELETE_BUCKET_ENCRYPTION` | both | — |
| `init_multipart`, `complete_multipart` | `RGW_OP_INIT_MULTIPART`, `RGW_OP_COMPLETE_MULTIPART` | both | — |
| `set_bucket_versioning` | `RGW_OP_SET_BUCKET_VERSIONING` | both | — |
| `multi_object_delete` | `RGW_OP_DELETE_MULTI_OBJ` | both | — |
| `set_bucket_website`, `delete_bucket_website` | `RGW_OP_SET_BUCKET_WEBSITE` for both (`RGWDeleteBucketWebsite::get_type`, [`rgw_op.h:1090`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.h#L1090) at v19.2.6) | both | — |
| `put_bucket_policy` | `RGW_OP_PUT_BUCKET_POLICY` | both | — |
| `put_obj_tags`, `put_bucket_tags` | `RGW_OP_PUT_OBJ_TAGGING`, `RGW_OP_PUT_BUCKET_TAGGING` | both | — |
| `put_bucket_replication` | `RGW_OP_PUT_BUCKET_REPLICATION` | both | — |
| `put_lifecycle` | `RGW_OP_PUT_LC` | both | — |
| `set_request_payment` | `RGW_OP_SET_REQUEST_PAYMENT` | both | — |
| `put_bucket_notification`, `delete_bucket_notification`, `get_bucket_notification` | `RGW_OP_PUBSUB_NOTIF_CREATE`, `RGW_OP_PUBSUB_NOTIF_DELETE`, `RGW_OP_PUBSUB_NOTIF_LIST` ([`rgw_rest_pubsub.cc:1198`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_pubsub.cc#L1198), [`:1453`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_pubsub.cc#L1453), [`:1557`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_pubsub.cc#L1557) at v19.2.6) | both | — |
| `put_bucket_object_lock`, `put_obj_retention`, `put_obj_legal_hold` | `RGW_OP_PUT_BUCKET_OBJ_LOCK`, `RGW_OP_PUT_OBJ_RETENTION`, `RGW_OP_PUT_OBJ_LEGAL_HOLD` | both | — |
| `put_bucket_public_access_block`, `get_bucket_public_access_block`, `delete_bucket_public_access_block` | `RGW_OP_PUT_BUCKET_PUBLIC_ACCESS_BLOCK`, `RGW_OP_GET_BUCKET_PUBLIC_ACCESS_BLOCK`, `RGW_OP_DELETE_BUCKET_PUBLIC_ACCESS_BLOCK` | both | — |
| `get_obj` (GET and HEAD), `select_obj` | `RGW_OP_GET_OBJ` for both (`RGWSelectObj_ObjStore_S3` overrides no `get_type`, [`rgw_s3select_private.h:185`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_s3select_private.h#L185)) | both | — |
| `get_obj_attrs` | on Squid radosgw answers `?attributes` as `get_obj`, `RGW_OP_GET_OBJ`; at v20.2.4 `RGW_OP_GET_OBJ_ATTRS`, not listed | `[S]` | — |
| `get_bucket_logging` | `RGW_OP_GET_BUCKET_LOGGING` | `[T]` | — |
| `put_bucket_logging [T]`, `post_bucket_logging [T]` | `RGW_OP_PUT_BUCKET_LOGGING`, `RGW_OP_POST_BUCKET_LOGGING` | `[T]` | — |
| `restore_obj [T]` | `RGW_OP_RESTORE_OBJ` | `[T]` | — |

The single-chunk column is 28 op types through 31 route names at v19.2.6 and 32 through 34 at v20.2.4 (the four `[T]` rows: [`rgw_rest_s3.cc [T]:6494`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L6494), [`:6508-6510`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L6508-L6510)). The `[S]` row keeps, for the one route R-D1 dispatches differently from radosgw, the forms of the op a Squid radosgw runs for the same request, so the accept-or-501 answer is radosgw's on both releases. The other five of radosgw's 33 whitelisted op types (37 at v20.2.4) have no S3 route: `RGW_OP_ADMIN_SET_METADATA` is the admin metadata put (unit N's handler passes `op.PayloadSigned` for it), and `RGW_OP_SYNC_DATALOG_NOTIFY`, `RGW_OP_SYNC_DATALOG_NOTIFY2`, `RGW_OP_SYNC_MDLOG_NOTIFY` and `RGW_OP_PERIOD_POST` are multisite's, excluded. The switch also names `RGW_STS_GET_SESSION_TOKEN` and `RGW_STS_ASSUME_ROLE`, which never reach it (`is_non_s3_op` returns first, [`rgw_auth_s3.cc:472-473`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_s3.cc#L472-L473)). Every other route takes neither form: the listings, `stat_bucket`, `delete_bucket`, `get_acls`, the other bucket GETs and DELETEs, `get_obj_tags`, `delete_obj_tags`, `get_obj_attrs` on Tentacle, `get_obj_layout`, `get_obj_retention`, `get_obj_legal_hold`, `copy_obj`, `delete_obj`, `abort_multipart`, `list_multipart`, `list_bucket_multiparts`, `post_obj`, `options_cors`, the three meta-search routes and `get_usage`. Six route names are G's own where radosgw's `RGWOp::name()` differs, which the op-type column makes irrelevant here; each op's `Name()` still returns radosgw's name, because the usage log files every request under it (`rgw_log.cc:245`, [`:556-559`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_log.cc#L556-L559) at v19.2.6): `get_usage` (radosgw's `get_self_usage`, typed `RGW_OP_UNKNOWN`), `list_bucket_v2` (radosgw names both listings `list_bucket`), `select_obj` (a `get_obj`) and the three notification routes (`pubsub_notification_create_s3`, `pubsub_notification_delete_s3`, `pubsub_notifications_get_s3`).

- [ ] **Step 1: Write the failing request-parsing specs**

`internal/s3/request_test.go`:

```go
func parse(method, target string, hdr map[string]string, cfg s3.Config) (s3.Parsed, error) {
	req := httptest.NewRequest(method, target, nil)
	for k, v := range hdr {
		if k == "Host" {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	return s3.ParseRequest(req, cfg, time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC))
}

var _ = Describe("ParseRequest", func() {
	DescribeTable("splits tenant, bucket and object from the path",
		func(target, tenant, bucket, object, instance string, explicit bool) {
			p, err := parse("GET", target, nil, s3.Config{})
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Req.Tenant).To(Equal(tenant), "tenant")
			Expect(p.Req.Bucket).To(Equal(bucket), "bucket")
			Expect(p.Req.Object).To(Equal(meta.ObjKey{Name: object, Instance: instance}), "object")
			Expect(p.ExplicitTenant).To(Equal(explicit), "explicit tenant")
		},
		Entry("service", "/", "", "", "", "", false),
		Entry("bucket", "/plain", "", "plain", "", "", false),
		Entry("bucket with trailing slash", "/plain/", "", "plain", "", "", false),
		Entry("object with slashes", "/plain/a/b/c.txt", "", "plain", "a/b/c.txt", "", false),
		Entry("percent-encoded slash decodes once", "/plain/a%2Fb", "", "plain", "a/b", "", false),
		Entry("plus stays a plus in the path", "/plain/a+b", "", "plain", "a+b", "", false),
		Entry("tenanted bucket", "/t1:tenanted/k", "t1", "tenanted", "k", "", true),
		Entry("explicit legacy tenant", "/:plain/k", "", "plain", "k", "", true),
		Entry("versionId fills the instance", "/plain/k?versionId=v1", "", "plain", "k", "v1", false),
	)
	DescribeTable("rejects what radosgw rejects",
		func(target string, want *op.Error) {
			_, err := parse("GET", target, nil, s3.Config{})
			Expect(err).To(MatchError(want))
		},
		Entry("NUL in the path is ERR_ZERO_IN_URL", "/plain/a%00b", op.ErrInvalidRequest),
		Entry("empty bucket after the tenant colon", "/t1:/k", op.ErrInvalidBucketName),
		Entry("a two-character bucket", "/ab", op.ErrInvalidBucketName),
		Entry("a bucket over 255 characters", "/"+strings.Repeat("a", 256), op.ErrInvalidBucketName),
		Entry("a bad tenant character", "/t-1:b/k", op.ErrInvalidTenantName),
		Entry("an object name over 1024 bytes", "/plain/"+strings.Repeat("k", 1025), op.ErrInvalidObjectName),
		Entry("an object name that is not UTF-8", "/plain/%ff%fe", op.ErrInvalidObjectName),
	)
	It("rejects a method radosgw has no op for", func() {
		_, err := parse("PATCH", "/plain/k", nil, s3.Config{})
		Expect(err).To(MatchError(op.ErrMethodNotAllowed))
	})
	It("lowercases X-Amz- query keys except their dashes and decodes plus as space", func() {
		p, err := parse("GET", "/plain/k?X-Amz-Signature=abc&Prefix=a+b", nil, s3.Config{})
		Expect(err).NotTo(HaveOccurred())
		Expect(p.Req.Query.Get("x-amz-signature")).To(Equal("abc"))
		Expect(p.Req.Query.Has("X-Amz-Signature")).To(BeFalse())
		Expect(p.Req.Query.Get("Prefix")).To(Equal("a b"))
	})
	Describe("Host", func() {
		cfg := s3.Config{DNSNames: []string{"s3.example.com"}}
		DescribeTable("names the bucket only inside a configured domain",
			func(host, target, bucket, object string) {
				p, err := parse("GET", target, map[string]string{"Host": host}, cfg)
				Expect(err).NotTo(HaveOccurred())
				Expect(p.Req.Bucket).To(Equal(bucket), "bucket")
				Expect(p.Req.Object.Name).To(Equal(object), "object")
			},
			Entry("subdomain is the bucket", "plain.s3.example.com", "/k", "plain", "k"),
			Entry("port is stripped first", "plain.s3.example.com:7480", "/k", "plain", "k"),
			Entry("the bare domain is path style", "s3.example.com", "/plain/k", "plain", "k"),
			Entry("the bare domain with a port is path style", "s3.example.com:443", "/plain/k", "plain", "k"),
			Entry("an IPv4 literal is path style", "10.0.0.7:7480", "/plain/k", "plain", "k"),
			Entry("an IPv6 literal is path style", "[fd00::7]:7480", "/plain/k", "plain", "k"),
			Entry("an unknown host that is a valid bucket name is the bucket (CNAME)", "photos.example.org", "/k", "photos.example.org", "k"),
			Entry("an unknown host that is not a valid bucket name is path style", "ab", "/plain/k", "plain", "k"),
		)
		It("never takes the Host as a bucket when no names are configured", func() {
			p, err := parse("GET", "/plain/k", map[string]string{"Host": "photos.example.org"}, s3.Config{})
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Req.Bucket).To(Equal("plain"))
			Expect(p.Req.Host).To(Equal("photos.example.org"))
		})
	})
	It("fills the plain fields", func() {
		req := httptest.NewRequest("PUT", "/plain/k", strings.NewReader("body"))
		req.Header.Set("Referer", "http://ref/")
		req.RemoteAddr = "10.1.2.3:5000"
		p, err := s3.ParseRequest(req, s3.Config{}, time.Unix(1, 0))
		Expect(err).NotTo(HaveOccurred())
		Expect(p.Req.Method).To(Equal("PUT"))
		Expect(p.Req.ContentLength).To(BeEquivalentTo(4))
		Expect(p.Req.Referer).To(Equal("http://ref/"))
		Expect(p.Req.RemoteAddr).To(Equal("10.1.2.3:5000"))
		Expect(p.Req.RawPath).To(Equal("/plain/k"))
		Expect(p.Req.Time).To(Equal(time.Unix(1, 0)))
		Expect(p.Req.TLS).To(BeFalse())
	})
})
```

- [ ] **Step 2: Write the failing dispatch specs**

`internal/s3/dispatch_test.go`, one `Entry` per table row above plus the precedence cases that could regress:

```go
func request(method, target string, hdr ...string) *op.Request {
	req := httptest.NewRequest(method, target, nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	p, err := s3.ParseRequest(req, s3.Config{}, time.Now())
	Expect(err).NotTo(HaveOccurred())
	return p.Req
}

var _ = Describe("Dispatch", func() {
	DescribeTable("selects radosgw's op",
		func(r *op.Request, want string) {
			route, err := s3.Dispatch(r, denc.Squid) // release-independent rows: the same at denc.Tentacle
			Expect(err).NotTo(HaveOccurred())
			Expect(route.Name).To(Equal(want))
			Expect(route.Scope).To(Equal(r.Scope()))
		},
		Entry("GET / lists buckets", request("GET", "/"), "list_buckets"),
		Entry("HEAD / lists buckets", request("HEAD", "/"), "list_buckets"),
		Entry("GET /?usage", request("GET", "/?usage"), "get_usage"),
		Entry("GET bucket lists v1", request("GET", "/b"), "list_bucket"),
		Entry("GET bucket list-type=2", request("GET", "/b?list-type=2"), "list_bucket_v2"),
		Entry("GET bucket list-type=3 falls back to v1", request("GET", "/b?list-type=3"), "list_bucket"),
		Entry("GET bucket ?location", request("GET", "/b?location"), "get_bucket_location"),
		Entry("GET bucket ?acl", request("GET", "/b?acl"), "get_acls"),
		Entry("GET bucket ?uploads", request("GET", "/b?uploads"), "list_bucket_multiparts"),
		Entry("GET bucket ?policy", request("GET", "/b?policy"), "get_bucket_policy"),
		Entry("GET bucket ?tagging", request("GET", "/b?tagging"), "get_bucket_tags"),
		Entry("GET bucket ?encryption reaches the encryption op", request("GET", "/b?encryption"), "get_bucket_encryption"),
		Entry("location beats acl on GET", request("GET", "/b?acl&location"), "get_bucket_location"),
		Entry("HEAD bucket stats", request("HEAD", "/b"), "stat_bucket"),
		Entry("HEAD bucket ?acl", request("HEAD", "/b?acl"), "get_acls"),
		Entry("PUT bucket creates", request("PUT", "/b"), "create_bucket"),
		Entry("PUT bucket ?acl", request("PUT", "/b?acl"), "put_acls"),
		Entry("PUT bucket ?tagging beats acl", request("PUT", "/b?acl&tagging"), "put_bucket_tags"),
		Entry("PUT bucket ?policy", request("PUT", "/b?policy"), "put_bucket_policy"),
		Entry("DELETE bucket", request("DELETE", "/b"), "delete_bucket"),
		Entry("DELETE bucket ?policy", request("DELETE", "/b?policy"), "delete_bucket_policy"),
		Entry("DELETE bucket ?tagging", request("DELETE", "/b?tagging"), "delete_bucket_tags"),
		Entry("POST bucket ?delete", request("POST", "/b?delete"), "multi_object_delete"),
		Entry("POST bucket is a form upload", request("POST", "/b"), "post_obj"),
		Entry("OPTIONS bucket", request("OPTIONS", "/b"), "options_cors"),
		Entry("GET object", request("GET", "/b/k"), "get_obj"),
		Entry("HEAD object", request("HEAD", "/b/k"), "get_obj"),
		Entry("GET object ?acl", request("GET", "/b/k?acl"), "get_acls"),
		Entry("GET object ?uploadId lists parts", request("GET", "/b/k?uploadId=u"), "list_multipart"),
		Entry("GET object ?tagging", request("GET", "/b/k?tagging"), "get_obj_tags"),
		Entry("PUT object", request("PUT", "/b/k"), "put_obj"),
		Entry("PUT object with copy source copies", request("PUT", "/b/k", "x-amz-copy-source", "/a/b"), "copy_obj"),
		Entry("PUT part copy is put_obj", request("PUT", "/b/k?uploadId=u&partNumber=1", "x-amz-copy-source", "/a/b"), "put_obj"),
		Entry("PUT with a copy range is put_obj", request("PUT", "/b/k", "x-amz-copy-source", "/a/b", "x-amz-copy-source-range", "bytes=0-1"), "put_obj"),
		Entry("PUT part", request("PUT", "/b/k?uploadId=u&partNumber=1"), "put_obj"),
		Entry("PUT object ?acl", request("PUT", "/b/k?acl"), "put_acls"),
		Entry("PUT object ?tagging", request("PUT", "/b/k?tagging"), "put_obj_tags"),
		Entry("DELETE object", request("DELETE", "/b/k"), "delete_obj"),
		Entry("DELETE object ?uploadId aborts", request("DELETE", "/b/k?uploadId=u"), "abort_multipart"),
		Entry("DELETE object ?tagging", request("DELETE", "/b/k?tagging"), "delete_obj_tags"),
		Entry("POST object ?uploads initiates", request("POST", "/b/k?uploads"), "init_multipart"),
		Entry("POST object ?uploadId completes", request("POST", "/b/k?uploadId=u"), "complete_multipart"),
		Entry("POST object with both prefers uploadId", request("POST", "/b/k?uploads&uploadId=u"), "complete_multipart"),
		Entry("OPTIONS object", request("OPTIONS", "/b/k"), "options_cors"),
	)
	DescribeTable("follows the release's op_* tables where they differ",
		func(r *op.Request, rel denc.Release, want string) {
			route, err := s3.Dispatch(r, rel)
			Expect(err).NotTo(HaveOccurred())
			Expect(route.Name).To(Equal(want))
		},
		Entry("GET bucket ?ownershipControls lists on Squid", request("GET", "/b?ownershipControls"), denc.Squid, "list_bucket"),
		Entry("GET bucket ?ownershipControls lists on Tentacle", request("GET", "/b?ownershipControls"), denc.Tentacle, "list_bucket"),
		Entry("PUT bucket ?ownershipControls creates", request("PUT", "/b?ownershipControls"), denc.Tentacle, "create_bucket"),
		Entry("DELETE bucket ?ownershipControls deletes", request("DELETE", "/b?ownershipControls"), denc.Squid, "delete_bucket"),
		Entry("PUT bucket ?logging on Tentacle", request("PUT", "/b?logging"), denc.Tentacle, "put_bucket_logging"),
		Entry("DELETE bucket ?logging on Tentacle falls through to delete_bucket", request("DELETE", "/b?logging"), denc.Tentacle, "delete_bucket"),
		Entry("POST bucket ?logging on Tentacle", request("POST", "/b?logging"), denc.Tentacle, "post_bucket_logging"),
		Entry("POST bucket ?logging on Squid is a form upload", request("POST", "/b?logging"), denc.Squid, "post_obj"),
		Entry("POST object ?restore on Tentacle", request("POST", "/b/k?restore"), denc.Tentacle, "restore_obj"),
		Entry("POST object ?restore on Squid is a form upload", request("POST", "/b/k?restore"), denc.Squid, "post_obj"),
		Entry("GET object ?attributes on Tentacle", request("GET", "/b/k?attributes"), denc.Tentacle, "get_obj_attrs"),
		// A Squid radosgw answers ?attributes as GetObject (RGWGetObjAttrs exists
		// only at v20.2.4); rgw-go serves GetObjectAttributes on both releases.
		Entry("GET object ?attributes on Squid is GetObjectAttributes too", request("GET", "/b/k?attributes"), denc.Squid, "get_obj_attrs"),
	)
	// radosgw refuses a signed payload form by op type inside authentication:
	// the single-chunk whitelist and the streamed one of get_auth_data_v4
	// (rgw_rest_s3.cc:5899-5969 at v19.2.6, :6466-6540 at v20.2.4).
	DescribeTable("carries the payload forms radosgw's SigV4 completer takes for the op",
		func(r *op.Request, rel denc.Release, want op.PayloadForms) {
			route, err := s3.Dispatch(r, rel)
			Expect(err).NotTo(HaveOccurred())
			Expect(route.Payloads).To(Equal(want), route.Name)
		},
		Entry("PutObject takes both", request("PUT", "/b/k"), denc.Squid, op.PayloadSigned|op.PayloadChunked),
		Entry("UploadPart takes both", request("PUT", "/b/k?uploadId=u&partNumber=1"), denc.Tentacle, op.PayloadSigned|op.PayloadChunked),
		Entry("CopyObject takes neither", request("PUT", "/b/k", "x-amz-copy-source", "/a/b"), denc.Squid, op.PayloadForms(0)),
		Entry("CreateBucket takes a single chunk", request("PUT", "/b"), denc.Squid, op.PayloadSigned),
		Entry("PutObjectAcl takes a single chunk", request("PUT", "/b/k?acl"), denc.Tentacle, op.PayloadSigned),
		Entry("DeleteObjects takes a single chunk", request("POST", "/b?delete"), denc.Squid, op.PayloadSigned),
		Entry("CreateMultipartUpload takes a single chunk", request("POST", "/b/k?uploads"), denc.Squid, op.PayloadSigned),
		Entry("CompleteMultipartUpload takes a single chunk", request("POST", "/b/k?uploadId=u"), denc.Tentacle, op.PayloadSigned),
		Entry("GetObject takes a single chunk", request("GET", "/b/k"), denc.Squid, op.PayloadSigned),
		Entry("HeadObject is get_obj too", request("HEAD", "/b/k"), denc.Tentacle, op.PayloadSigned),
		Entry("DELETE ?website is typed SET_BUCKET_WEBSITE", request("DELETE", "/b?website"), denc.Squid, op.PayloadSigned),
		Entry("GET ?encryption takes a single chunk", request("GET", "/b?encryption"), denc.Squid, op.PayloadSigned),
		Entry("GET ?notification takes a single chunk", request("GET", "/b?notification"), denc.Tentacle, op.PayloadSigned),
		Entry("ListObjects takes neither", request("GET", "/b"), denc.Squid, op.PayloadForms(0)),
		Entry("ListObjectsV2 takes neither", request("GET", "/b?list-type=2"), denc.Tentacle, op.PayloadForms(0)),
		Entry("DeleteObject takes neither", request("DELETE", "/b/k"), denc.Tentacle, op.PayloadForms(0)),
		Entry("AbortMultipartUpload takes neither", request("DELETE", "/b/k?uploadId=u"), denc.Squid, op.PayloadForms(0)),
		Entry("GetObjectAttributes takes neither on Tentacle", request("GET", "/b/k?attributes"), denc.Tentacle, op.PayloadForms(0)),
		Entry("?attributes keeps get_obj's single chunk on Squid", request("GET", "/b/k?attributes"), denc.Squid, op.PayloadSigned),
		Entry("a form upload takes neither", request("POST", "/b"), denc.Squid, op.PayloadForms(0)),
		Entry("GET ?logging takes neither on Squid", request("GET", "/b?logging"), denc.Squid, op.PayloadForms(0)),
		Entry("GET ?logging takes a single chunk on Tentacle", request("GET", "/b?logging"), denc.Tentacle, op.PayloadSigned),
		Entry("PUT ?logging takes a single chunk on Tentacle", request("PUT", "/b?logging"), denc.Tentacle, op.PayloadSigned),
		Entry("POST ?logging is a form upload on Squid", request("POST", "/b?logging"), denc.Squid, op.PayloadForms(0)),
		Entry("POST ?logging takes a single chunk on Tentacle", request("POST", "/b?logging"), denc.Tentacle, op.PayloadSigned),
		Entry("POST ?restore is a form upload on Squid", request("POST", "/b/k?restore"), denc.Squid, op.PayloadForms(0)),
		Entry("POST ?restore takes a single chunk on Tentacle", request("POST", "/b/k?restore"), denc.Tentacle, op.PayloadSigned),
	)
	DescribeTable("answers MethodNotAllowed where radosgw has no handler",
		func(r *op.Request) {
			_, err := s3.Dispatch(r, denc.Squid)
			Expect(err).To(MatchError(op.ErrMethodNotAllowed))
		},
		Entry("POST at service scope", request("POST", "/")),
		Entry("PUT at service scope", request("PUT", "/")),
		Entry("DELETE at service scope", request("DELETE", "/")),
		Entry("OPTIONS at service scope", request("OPTIONS", "/")),
		Entry("an object-only subresource on a bucket", request("GET", "/b?uploadId=u")),
		Entry("versionId on a bucket", request("GET", "/b?versionId=v")),
		Entry("PUT bucket ?logging on Squid", request("PUT", "/b?logging")),
		Entry("DELETE bucket ?logging on Squid", request("DELETE", "/b?logging")),
	)
	It("routes the Squid 405s to the bucket-logging ops on Tentacle", func() {
		_, err := s3.Dispatch(request("PUT", "/b?logging"), denc.Tentacle)
		Expect(err).NotTo(HaveOccurred())
	})
})
```

The `GET object ?attributes on Squid` entry pins R-D1 as owner decision 11 settled it: rgw-go serves GetObjectAttributes on Squid, where radosgw serves `get_obj`. The payload table's entries cover each form, each release difference, the one op type radosgw shares between two routes of different methods (`delete_bucket_website`) and the Squid `?attributes` row, which keeps the forms of the `get_obj` a Squid radosgw runs for that request.

- [ ] **Step 3: Run to fail, implement `request.go` and `dispatch.go`**

`dispatch.go` holds the table as data: per scope and method an ordered slice of `{key string; name string; rel releaseSet}` rows and a default name, walked in order, where `releaseSet` is a two-bit set (`onSquid | onTentacle`, zero meaning both) and an empty `name` means `ErrMethodNotAllowed` (an `op_*` returning nullptr); `Dispatch(r, rel)` skips a row whose set excludes `rel` and returns the first match. The object PUT copy rule and the bucket `list-type` rule are the two conditions the table cannot express and are written as code. The `[S]`/`[T]` tags in the table above are exactly the rows with a non-zero `rel`. The payload table is a second piece of data in `dispatch.go`, keyed by route name, consulted once the name is chosen; `Dispatch` sets `route.Payloads = row.forms` when the name has a row whose set admits `rel` (the test the dispatch rows use) and leaves it zero otherwise:

```go
// payloadRow is the signed payload forms an op takes at the releases in rel,
// zero meaning both.
type payloadRow struct {
	forms op.PayloadForms
	rel   releaseSet
}

// payloadTable is radosgw's per-op completer whitelist, keyed by route name
// through each route's RGWOp::get_type(): get_auth_data_v4's single-chunk
// switch (rgw_rest_s3.cc:5899-5944 at v19.2.6, :6466-6515 at v20.2.4) and
// its streamed switch, RGW_OP_PUT_OBJ alone (:5957-5969, :6528-6540). A
// route missing from it takes neither form.
var payloadTable = map[string]payloadRow{
	"put_obj":                           {op.PayloadSigned | op.PayloadChunked, 0},
	"create_bucket":                     {op.PayloadSigned, 0},
	"put_acls":                          {op.PayloadSigned, 0},
	"put_cors":                          {op.PayloadSigned, 0},
	"put_bucket_encryption":             {op.PayloadSigned, 0},
	"get_bucket_encryption":             {op.PayloadSigned, 0},
	"delete_bucket_encryption":          {op.PayloadSigned, 0},
	"init_multipart":                    {op.PayloadSigned, 0},
	"complete_multipart":                {op.PayloadSigned, 0},
	"set_bucket_versioning":             {op.PayloadSigned, 0},
	"multi_object_delete":               {op.PayloadSigned, 0},
	"set_bucket_website":                {op.PayloadSigned, 0},
	"delete_bucket_website":             {op.PayloadSigned, 0}, // typed RGW_OP_SET_BUCKET_WEBSITE (rgw_op.h:1090 at v19.2.6)
	"put_bucket_policy":                 {op.PayloadSigned, 0},
	"put_obj_tags":                      {op.PayloadSigned, 0},
	"put_bucket_tags":                   {op.PayloadSigned, 0},
	"put_bucket_replication":            {op.PayloadSigned, 0},
	"put_lifecycle":                     {op.PayloadSigned, 0},
	"set_request_payment":               {op.PayloadSigned, 0},
	"put_bucket_notification":           {op.PayloadSigned, 0}, // RGW_OP_PUBSUB_NOTIF_CREATE
	"delete_bucket_notification":        {op.PayloadSigned, 0}, // RGW_OP_PUBSUB_NOTIF_DELETE
	"get_bucket_notification":           {op.PayloadSigned, 0}, // RGW_OP_PUBSUB_NOTIF_LIST
	"put_bucket_object_lock":            {op.PayloadSigned, 0},
	"put_obj_retention":                 {op.PayloadSigned, 0},
	"put_obj_legal_hold":                {op.PayloadSigned, 0},
	"put_bucket_public_access_block":    {op.PayloadSigned, 0},
	"get_bucket_public_access_block":    {op.PayloadSigned, 0},
	"delete_bucket_public_access_block": {op.PayloadSigned, 0},
	"get_obj":                           {op.PayloadSigned, 0},
	"select_obj":                        {op.PayloadSigned, 0}, // S3 Select's op keeps RGWGetObj's RGW_OP_GET_OBJ
	"get_obj_attrs":                     {op.PayloadSigned, onSquid},    // a Squid radosgw runs ?attributes as get_obj; RGW_OP_GET_OBJ_ATTRS (v20.2.4) is not listed
	"get_bucket_logging":                {op.PayloadSigned, onTentacle},
	"put_bucket_logging":                {op.PayloadSigned, onTentacle},
	"post_bucket_logging":               {op.PayloadSigned, onTentacle},
	"restore_obj":                       {op.PayloadSigned, onTentacle},
}
```

`request.go` implements the rules in the order listed above; `looksLikeIPAddress` ports `rgw_rest_s3.h`'s `looks_like_ip_address` (`net.ParseIP` for IPv6, and the digits-and-three-dots walk for IPv4).

Run `go test -tags=ceph_preview ./internal/s3/`; expected PASS.

- [ ] **Step 4: Commit**

`make check`, then `feat(s3): parse Host, path and query once and dispatch on radosgw's precedence with each op's payload forms`.

---

### Task 4: `s3` handler: lifecycle runner, error documents, headers, transaction ids, concurrency cap, `op.ListBuckets`; `xmltext`

**Files:**
- Create: `internal/s3/handler.go`, `internal/s3/errors.go`, `internal/s3/headers.go`, `internal/s3/xml.go`, `internal/s3/auth.go`, `internal/s3/listbuckets.go`, `internal/s3/handler_test.go`, `internal/s3/errors_test.go`, `internal/s3/listbuckets_test.go`, `internal/s3/export_test.go` (`SinkOfForTest`)
- Create: `internal/op/listbuckets.go`, `internal/op/listbuckets_test.go`
- Create: `internal/xmltext/xmltext.go`, `internal/xmltext/xmltext_suite_test.go`, `internal/xmltext/xmltext_test.go`

**Interfaces:**
- Consumes: Task 1's `op` and `memstore`, Task 3's `ParseRequest`, `Dispatch`, `Config`.
- Produces:

```go
package xmltext

// Escape is xml_stream_escaper (src/common/escape.cc:134-169 at v19.2.6 and
// v20.2.4), the escaping XMLFormatter gives every string it writes as element
// text: the five entities, a character reference for a control byte, every
// other byte copied.
func Escape(s string) string

// Text is element text that encoding/xml writes escaped as Escape escapes
// it; every string field of an S3 XML document is a Text.
type Text string

func (t Text) MarshalXML(e *xml.Encoder, start xml.StartElement) error
```

```go
package s3

// Authenticator resolves a request's identity. auth.Verifier implements it;
// AnonymousOnly is the interim implementation.
type Authenticator interface {
	// Authenticate returns the identity, or an op.Error such as
	// ErrInvalidAccessKeyID or ErrSignatureDoesNotMatch. payloads is the
	// dispatched route's Payloads: radosgw answers a signed payload in any
	// other form with ErrNotImplemented from inside authentication, before
	// the key is looked up (get_auth_data_v4, rgw_rest_s3.cc:5899-5969).
	Authenticate(ctx context.Context, r *http.Request, payloads op.PayloadForms) (*op.AuthResult, error)
}

// AuthenticatorFunc adapts a function to Authenticator.
type AuthenticatorFunc func(ctx context.Context, r *http.Request, payloads op.PayloadForms) (*op.AuthResult, error)

// AnonymousOnly authenticates unsigned requests as anonymous and answers
// ErrNotImplemented to any request carrying credentials: an Authorization
// header, or an X-Amz-Signature, X-Amz-Credential, AWSAccessKeyId or Signature
// query parameter. It never serves a signed request as anonymous. It ignores
// payloads: an anonymous request has no signed payload.
type AnonymousOnly struct{}

// HandlerFunc serves one route: it fills the op from r, runs it and renders
// the response. It returns an error only before anything was written.
type HandlerFunc func(ctx context.Context, w http.ResponseWriter, r *op.Request) error

// Handler is the S3 protocol layer as one http.Handler.
type Handler struct{ /* env, auth, cfg, routes, seq atomic.Uint64, inflight atomic.Int64 */ }

// NewHandler builds the handler with every implemented route registered and
// every other route answering NotImplemented.
func NewHandler(env *op.Env, auth Authenticator, cfg Config) *Handler

// Register installs fn for the route named name, replacing any handler.
// Each op family registers its routes from its own file's newXxxHandlers
// function, which NewHandler calls.
func (h *Handler) Register(name string, fn HandlerFunc)

func (h *Handler) ServeHTTP(w http.ResponseWriter, req *http.Request)

// WriteError renders err as radosgw's S3 error document with the common
// headers; it is what every HandlerFunc calls on failure and what other
// protocol layers (admin) reuse. It is end_header's error branch
// (rgw_rest.cc:620-624 at v19.2.6, :625-629 at v20.2.4), so the length goes
// out through SetContentLength, with Accept-Ranges. An InternalError or
// UnknownError is logged with its cause under ctx.
func WriteError(ctx context.Context, w http.ResponseWriter, r *op.Request, err error)

// WriteXML renders v with the XML header radosgw's formatter emits and the
// common headers, at status. Every string v carries is an xmltext.Text, so
// its text is escaped as radosgw's formatter escapes it. Its Content-Length
// is the one radosgw's frontend adds when a response whose end_header named
// no length completes (rgw_client_io_filters.h:221-253), so it carries no
// Accept-Ranges.
func WriteXML(w http.ResponseWriter, r *op.Request, status int, v any)

// SetCommonHeaders writes x-amz-request-id and Server on w, and
// x-amz-request-charged: requester when r.BucketRec is requester-pays and the
// identity is not its owner (end_header, rgw_rest.cc:597-601 at v19.2.6,
// :602-606 at v20.2.4; WriteError never adds it, end_header's !is_err()).
func SetCommonHeaders(w http.ResponseWriter, r *op.Request)

// ISO8601 formats t as radosgw's rgw_to_iso8601: "2006-01-02T15:04:05.000Z".
func ISO8601(t time.Time) string

// SetContentLength is dump_content_length (rgw_rest.cc:388-397 at v19.2.6
// and v20.2.4): the length and Accept-Ranges: bytes. radosgw calls it for
// every error document and for a success whose op names its length: the GET
// and HEAD of an object (rgw_rest_s3.cc:459 at v19.2.6, :495 at v20.2.4) and
// the PUT of an object or part without a copy source (:2747, :2911). Every
// other Content-Length radosgw sends is the one its frontend adds when the
// response completes, which carries no Accept-Ranges.
func SetContentLength(h http.Header, n uint64)

// sinkOf adapts w to the op.Sink a streaming op writes to. The writer a route
// receives cannot be that sink itself: http.ResponseWriter.WriteHeader(int)
// and op.Sink.WriteHeader(int, http.Header) share a name. The adapter's
// WriteHeader merges h into w.Header() and then calls w.WriteHeader(status);
// Write forwards; Flush goes through http.NewResponseController(w).
func sinkOf(w http.ResponseWriter) op.Sink

// chunkedXML is the body of a response radosgw sends with end_header(...,
// CHUNKED_TRANSFER_ENCODING) (rgw_rest.cc:626-627): add and encode collect the
// next chunk and flush sends it, where rgw_flush_formatter hands the
// formatter's output to the chunking filter. add writes its argument as is,
// so it takes markup and text already escaped with xmltext.Escape; encode
// takes a value whose strings are xmltext.Text. On a HEAD all three do nothing.
type chunkedXML struct{ /* w, rc, head bool, buf bytes.Buffer */ }

// startChunkedXML sends the common headers, Content-Type: application/xml
// and 200 without a Content-Length or Accept-Ranges, and flushes them;
// net/http then frames the body with Transfer-Encoding: chunked. A route
// calls it where radosgw's end_header runs, once the op has succeeded; from
// then on a failure can only end the response short, and the returned writer
// is never nil. A HEAD gets no Transfer-Encoding (dump_chunked_encoding skips
// it, rgw_rest.cc:399-411) and the Content-Length: 0 that radosgw's
// BufferingFilter completes it with, which carries no Accept-Ranges
// (rgw_client_io_filters.h:207-253).
func startChunkedXML(w http.ResponseWriter, r *op.Request) (*chunkedXML, error)

// writeChunkedXML is a whole document in the one flush that the listings
// rendered after execute end with (rgw_flush_formatter_and_reset after
// end_header): the headers, then the XML declaration and v. A failure after
// the headers is logged under ctx and the response ends short.
func writeChunkedXML(ctx context.Context, w http.ResponseWriter, r *op.Request, v any)
```

```go
package op

// ListBuckets is RGWListBuckets: the service-scope GET and HEAD. Execute
// hands the owner's buckets to Page one listing page at a time, as
// RGWListBuckets::execute hands each page to send_response_data
// (rgw_op.cc:2511-2607).
type ListBuckets struct {
	// Marker is where the listing starts; Limit caps it, -1 for no limit.
	// radosgw's S3 get_params sets only limit = -1 and no marker
	// (rgw_rest_s3.h:133-136).
	Marker string
	Limit  int

	// Begin is send_response_begin: called once, when the first page has
	// been read or at once when nothing is listed, before any Page. An error
	// Execute returns before Begin is the request's error.
	Begin func() error
	// Page is send_response_data: called with each page, in name order.
	Page func([]meta.BucketEnt) error
}

func (o *ListBuckets) Name() string          // "list_buckets"
func (o *ListBuckets) Action() policy.Action // policy.S3ListAllMyBuckets
func (o *ListBuckets) OpMask() uint32        // OpTypeRead
```

The request lifecycle in `ServeHTTP`, in `process_request`'s order ([`rgw_process.cc:313-470`](https://github.com/ceph/ceph/blob/7ed73efc1beb034b76e150439972959857600920/src/rgw/rgw_process.cc#L313-L470)) with the S3 rendering `abort_early` does (`rgw_rest.cc`):

1. Count the request in flight; when `cfg.MaxConcurrent > 0` and the count already exceeds it, answer `op.ErrSlowDown` (`SimpleThrottler` returns `-EAGAIN`, mapped to `ERR_RATE_LIMITED`; [`rgw_process.cc:379-381`](https://github.com/ceph/ceph/blob/7ed73efc1beb034b76e150439972959857600920/src/rgw/rgw_process.cc#L379-L381)). The count is released when the response is done.
2. `now := env.Clock()`; `r.ID = op.TransID(h.seq.Add(1), now, cfg.TransIDSuffix)`, `r.Env = env`, `r.Time = now`.
3. `ParseRequest`; on error `WriteError`.
4. `Dispatch(r, env.Zone.Release())`; on error `WriteError` (405 for what radosgw has no op for at that release).
5. `auth.Authenticate(ctx, req, route.Payloads)`; on error `WriteError`. The route's forms are what radosgw's authentication consults through `s->op_type`, which `process_request` sets from the op it dispatched before it authenticates (`rgw_process.cc:325`, [`:341`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_process.cc#L341), `:345` at v19.2.6). Copy the identity into `r.Identity`; when `res.Body != nil`, `r.Body = res.Body`. If the tenant was not explicit, `r.Tenant = r.Identity.Tenant` (`postauth_init`, `rgw_rest_s3.cc`). A suspended user (`r.Identity.User.Suspended != 0`) is `op.ErrUserSuspended` (`rgw_process.cc:401-404`).
6. Look the route's handler up; none registered is `op.ErrNotImplemented`.
7. Call the handler; on error `WriteError`.
8. Observe: `env.Metrics.Observe(route.Name, r.Status, elapsed, r.BytesIn, r.BytesOut)` and the in-flight decrement, in a `defer`. Log one line at debug: `slog.DebugContext(ctx, "request done", slog.String("request_id", r.ID), slog.String("op", route.Name), slog.Int("status", r.Status), slog.Duration("elapsed", elapsed))`.

`w` is wrapped in a `responseWriter` (Step 5) that records the status and byte count into `r.Status`/`r.BytesOut` and forwards `Flush` and `Unwrap` so `http.ResponseController` still reaches the connection for Task 5's deadlines; it adds no header of its own. It implements `http.ResponseWriter`, `Flush` and `Unwrap` and nothing else; it cannot also be an `op.Sink`, because `http.ResponseWriter.WriteHeader(int)` and `op.Sink.WriteHeader(int, http.Header)` share a name. A route that hands a streaming op an `op.Sink` passes `sinkOf(w)`, whose `WriteHeader(status, h)` merges `h` into `w.Header()` and then calls `w.WriteHeader(status)`; its `Write` forwards and its `Flush` goes through `http.NewResponseController(w).Flush()`. `Accept-Ranges: bytes` goes out exactly where radosgw's `dump_content_length` runs ([rgw_rest.cc:388-397](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L388-L397) at v19.2.6 and v20.2.4), which is only two places (R-D14). One is `end_header`'s error branch, for every error document ([rgw_rest.cc:620-624](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L620-L624) at v19.2.6, [:625-629](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L625-L629) at v20.2.4); `rgw_err::is_err` is false for 200-399 ([rgw_common.cc:203-207](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L203-L207)), so a 304 is not an error and gets none. The other is a success whose op passes its length itself, which in phase 1's routes is the GET and HEAD of an object ([`rgw_rest_s3.cc:459`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L459) at v19.2.6, [`:495`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L495) at v20.2.4) and the PUT of an object or part without a copy source (`:2747`, `:2911`); POST object (`:3402`, `:3546`), a static-website redirect (`:277`, `:282`) and a website error document (`rgw_rest.cc:738`, [`:743`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L743)) are phase 2's. No other `end_header` call in `rgw_rest_s3.cc` passes a length at either tag. So `WriteError` sets the length through `SetContentLength`, which is `dump_content_length`, and so do R's GET and HEAD and W's and P's PUT; every other success sets no `Accept-Ranges`: a `WriteXML` document carries the `Content-Length` radosgw's frontend adds when a response whose `end_header` named no length completes (`BufferingFilter::complete_request`, [rgw_client_io_filters.h:221-253](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_client_io_filters.h#L221-L253)), a body-less 200 carries that filter's `Content-Length: 0`, a 204 or 304 carries neither (radosgw's `ConLenControllingFilter` drops the length, rgw_client_io_filters.h:344-346 and :358 at both tags; net/http drops it too, transfer.go:482-495 in Go 1.27.1), and a chunked response carries neither. The listings radosgw sends with `end_header(..., CHUNKED_TRANSFER_ENCODING)` — ListBuckets ([`rgw_rest_s3.cc:1519`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L1519) at v19.2.6, [`:1630`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L1630) at v20.2.4), ListObjects v1 and v2 (`:1906`, `:2062`; `:2017`, `:2173`), ListParts (`:4128`; `:4664`) and ListMultipartUploads (`:4181`; `:4729`) — go through `startChunkedXML` (Step 5): Content-Type `application/xml`, no `Content-Length` and no `Accept-Ranges`, the headers flushed before the body so net/http frames it with `Transfer-Encoding: chunked`, then one chunk for each flush radosgw makes; W's DeleteObjects streams through `startChunkedXML` too, and its CopyObject frames its chunked response the same way through its own flushes (W Task 11). `r.Body` is wrapped to count `r.BytesIn`. `x-amz-request-charged` (R-D15) is G's too: `SetCommonHeaders` adds it and no route adds it itself. `handler_test.go` pins both: an error document carries `Accept-Ranges: bytes` with its `Content-Length`, a `WriteXML` success and a route's own `Content-Length` carry none, and `SetContentLength` sets the pair; a requester-pays bucket's 200 for a non-owner carries `x-amz-request-charged: requester`, its owner's and any error response do not.

The error document (`rgw_common.cc:395-418`, `dump`; `rgw_rest.cc`, `end_header`):

```
HTTP/1.1 404 Not Found
x-amz-request-id: tx000000000000000000001-0068d7a1b2-4155-ceph-objectstore
Content-Type: application/xml
Content-Length: <n>
Accept-Ranges: bytes
Server: Ceph Object Gateway (squid)

<?xml version="1.0" encoding="UTF-8"?><Error><Code>NoSuchBucket</Code><Message></Message><BucketName>plain</BucketName><RequestId>tx…</RequestId><HostId>4155-ceph-objectstore-ceph-objectstore</HostId></Error>
```

`Code` is omitted when empty, `Message` is always present (empty when unset), `BucketName` only when the request named a bucket, `RequestId` when the id is set, `HostId` always. The XML header is `<?xml version="1.0" encoding="UTF-8"?>` with no newline; Go's `encoding/xml` writes nothing for it, so `WriteXML` writes the header bytes then `xml.Marshal`'s output. Every string field of an S3 document is an `xmltext.Text`, so the code, message, bucket name and ids are escaped as `XMLFormatter::dump_string` escapes them (`xml_stream_escaper`, [src/common/escape.cc:134-169](https://github.com/ceph/ceph/blob/v19.2.6/src/common/escape.cc#L134-L169); [Formatter.cc:516-523](https://github.com/ceph/ceph/blob/v19.2.6/src/common/Formatter.cc#L516-L523) at v19.2.6, [:536-543](https://github.com/ceph/ceph/blob/v20.2.4/src/common/Formatter.cc#L536-L543) at v20.2.4), never with `encoding/xml`'s own rules, which write `&#34;` and `&#39;` and replace control bytes and invalid UTF-8 with U+FFFD (xml.go:1904-1912, :1971-2006 in Go 1.27.1). `Accept-Ranges: bytes` comes with the length because `end_header`'s error branch sets it through `dump_content_length` ([rgw_rest.cc:620-624](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L620-L624)). `Content-Type` is `application/xml` for every error and every XML body. `Date` comes from `net/http`, as beast adds it (`rgw_asio_client.cc:147-150`); beast also writes `Connection: Keep-Alive` or `Connection: close` on every response (`:152-158`), where net/http writes a `Connection` header only when it closes the connection or answers an HTTP/1.0 keep-alive, a frontend difference Task 5 records. No `Content-Type` on a body-less success, as `end_header` omits it when the length is zero and none was set.

- [ ] **Step 1: Write the failing `op.ListBuckets` spec**

`internal/op/listbuckets_test.go` with a `memstore.Store` seeded with user `alice` owning buckets `a` and `b` and `bob` owning `c`. The op hands pages to the handler, so the spec records what reaches `Begin` and `Page`:

```go
var _ = Describe("ListBuckets", func() {
	var (
		store *memstore.Store
		env   *op.Env
		alice *op.UserRecord
	)
	BeforeEach(func(ctx SpecContext) {
		store = memstore.New(memstore.Config{})
		env = &op.Env{Zone: store, Users: store, Buckets: store, Authz: op.OwnerOnly{}, Metrics: op.NopMetrics{}}
		alice = store.AddUser(meta.UserInfo{UserID: meta.UserID{ID: "alice"}, DisplayName: "Alice", OpMask: op.OpTypeAll})
		for _, name := range []string{"a", "b"} {
			_, err := store.CreateBucket(ctx, op.CreateBucketParams{Name: name, Owner: meta.UserOwner(alice.Info.UserID)})
			Expect(err).NotTo(HaveOccurred())
		}
		bob := store.AddUser(meta.UserInfo{UserID: meta.UserID{ID: "bob"}, OpMask: op.OpTypeAll})
		_, err := store.CreateBucket(ctx, op.CreateBucketParams{Name: "c", Owner: meta.UserOwner(bob.Info.UserID)})
		Expect(err).NotTo(HaveOccurred())
	})
	identity := func(rec *op.UserRecord) op.Identity {
		return op.Identity{User: &rec.Info, Owner: meta.UserOwner(rec.Info.UserID), OpMask: rec.Info.OpMask}
	}
	// run executes o and returns its Begin and Page calls in order: "begin",
	// then each page as its comma-joined bucket names.
	run := func(ctx context.Context, o *op.ListBuckets, r *op.Request) ([]string, error) {
		var calls []string
		o.Begin = func() error {
			calls = append(calls, "begin")
			return nil
		}
		o.Page = func(page []meta.BucketEnt) error {
			names := make([]string, 0, len(page))
			for _, b := range page {
				names = append(names, b.Bucket.Name)
			}
			calls = append(calls, strings.Join(names, ","))
			return nil
		}
		err := op.Run(ctx, o, r)
		return calls, err
	}
	It("hands the identity's own buckets to Page in name order, after Begin", func(ctx SpecContext) {
		calls, err := run(ctx, &op.ListBuckets{Limit: -1}, &op.Request{Env: env, Identity: identity(alice)})
		Expect(err).NotTo(HaveOccurred())
		Expect(calls).To(Equal([]string{"begin", "a,b"}))
	})
	It("reads rgw_list_buckets_max_chunk buckets at a time and hands on each page as it is read", func(ctx SpecContext) {
		env.Conf = cephconf.NewOptions(cephconf.MapGetter{"rgw_list_buckets_max_chunk": "1"})
		calls, err := run(ctx, &op.ListBuckets{Limit: -1}, &op.Request{Env: env, Identity: identity(alice)})
		Expect(err).NotTo(HaveOccurred())
		Expect(calls).To(Equal([]string{"begin", "a", "b"}))
	})
	It("stops at Limit and starts after Marker", func(ctx SpecContext) {
		calls, err := run(ctx, &op.ListBuckets{Limit: 1}, &op.Request{Env: env, Identity: identity(alice)})
		Expect(err).NotTo(HaveOccurred())
		Expect(calls).To(Equal([]string{"begin", "a"}))
		calls, err = run(ctx, &op.ListBuckets{Limit: -1, Marker: "a"}, &op.Request{Env: env, Identity: identity(alice)})
		Expect(err).NotTo(HaveOccurred())
		Expect(calls).To(Equal([]string{"begin", "b"}))
	})
	It("begins an empty listing for an anonymous identity without touching the store", func(ctx SpecContext) {
		users := &opfakes.FakeUserStore{}
		env.Users = users
		calls, err := run(ctx, &op.ListBuckets{Limit: -1}, &op.Request{Env: env, Identity: op.Anonymous()})
		Expect(err).NotTo(HaveOccurred())
		Expect(calls).To(Equal([]string{"begin"}))
		Expect(users.ListUserBucketsCallCount()).To(BeZero(), "radosgw skips list_buckets() for the anonymous user")
	})
	It("returns a failed first page before Begin and a failed later page after it", func(ctx SpecContext) {
		users := &opfakes.FakeUserStore{}
		env.Users = users
		users.ListUserBucketsReturnsOnCall(0, nil, "", false, op.ErrInternalError)
		calls, err := run(ctx, &op.ListBuckets{Limit: -1}, &op.Request{Env: env, Identity: identity(alice)})
		Expect(err).To(MatchError(op.ErrInternalError))
		Expect(calls).To(BeEmpty(), "send_response_begin renders the error document")
		users.ListUserBucketsReturnsOnCall(1, []meta.BucketEnt{{Bucket: meta.BucketID{Name: "a"}}}, "a", true, nil)
		users.ListUserBucketsReturnsOnCall(2, nil, "", false, op.ErrInternalError)
		calls, err = run(ctx, &op.ListBuckets{Limit: -1}, &op.Request{Env: env, Identity: identity(alice)})
		Expect(err).To(MatchError(op.ErrInternalError))
		Expect(calls).To(Equal([]string{"begin", "a"}), "the response is under way when the second page fails")
	})
	It("denies an identity whose op mask lacks read", func(ctx SpecContext) {
		id := identity(alice)
		id.OpMask = op.OpTypeWrite
		_, err := run(ctx, &op.ListBuckets{Limit: -1}, &op.Request{Env: env, Identity: id})
		Expect(err).To(MatchError(op.ErrAccessDenied))
	})
})
```

- [ ] **Step 2: Implement `op.ListBuckets`**

```go
package op

// ListBuckets is RGWListBuckets, the service-scope GET and HEAD.
type ListBuckets struct {
	Marker string
	Limit  int

	Begin func() error
	Page  func([]meta.BucketEnt) error
}

func (*ListBuckets) Name() string          { return "list_buckets" }
func (*ListBuckets) Action() policy.Action { return policy.S3ListAllMyBuckets }
func (*ListBuckets) OpMask() uint32        { return OpTypeRead }

func (*ListBuckets) Init(context.Context, *Request) error { return nil }

// VerifyPermission is RGWListBuckets::verify_permission: s3:ListAllMyBuckets
// against the identity's own policies; an anonymous identity passes because
// it has no user ACL to deny it (verify_user_permission_no_policy).
func (o *ListBuckets) VerifyPermission(ctx context.Context, r *Request) error {
	return VerifyUserPermission(ctx, r, o.Action())
}

// Execute pages the owner's buckets in chunks of rgw_list_buckets_max_chunk
// until Limit is reached, calling Begin once the first page is read and Page
// with every page, as RGWListBuckets::execute calls send_response_begin and
// send_response_data (rgw_op.cc:2550-2604). A zero Limit and an anonymous
// identity list nothing, and the scope guard still begins the response
// (:2522-2527, :2553-2565).
func (o *ListBuckets) Execute(ctx context.Context, r *Request) error {
	if o.Limit == 0 || r.Identity.Anonymous {
		return o.Begin()
	}
	chunk := 1000
	if r.Env.Conf != nil {
		if v, err := r.Env.Conf.Int64("rgw_list_buckets_max_chunk"); err == nil && v > 0 {
			chunk = int(v)
		}
	}
	marker, total, begun := o.Marker, 0, false
	for {
		want := chunk
		if o.Limit > 0 {
			want = min(chunk, o.Limit-total)
		}
		ents, next, more, err := r.Env.Users.ListUserBuckets(ctx, r.Identity.Owner, marker, want)
		if err != nil {
			// Before Begin this is the request's error; after it the listing
			// is under way and the handler closes it, as the scope guard's
			// send_response_end does (:2570-2576).
			return FromRADOS(err, ScopeUser)
		}
		total += len(ents)
		if !begun {
			if err := o.Begin(); err != nil {
				return err
			}
			begun = true
		}
		if err := o.Page(ents); err != nil {
			return err
		}
		if !more || (o.Limit > 0 && total >= o.Limit) {
			return nil
		}
		marker = next
	}
}

func (*ListBuckets) Complete(context.Context, *Request) {}
```

M Task 7 makes `ListBuckets.Complete` call `LogUsage(ctx, r, "list_buckets")`: radosgw usage-logs every op, bucketless ones under `"-"` (`rgw_log.cc:195-224`, [`:558-559`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_log.cc#L558-L559) at v19.2.6).

Run `go test -tags=ceph_preview ./internal/op/`; expected PASS.

- [ ] **Step 3: Write the failing handler specs**

`internal/s3/handler_test.go`:

```go
func newHandler(store *memstore.Store, auth s3.Authenticator, cfg s3.Config) *s3.Handler {
	env := &op.Env{Zone: store, Users: store, Buckets: store, Objects: store, Multipart: store, Stats: store,
		Usage: store, Metadata: store, Authz: op.OwnerOnly{}, Metrics: op.NopMetrics{},
		Now: func() time.Time { return time.Unix(0x68d7a1b2, 0) }, HostID: "4155-z-zg"}
	if cfg.TransIDSuffix == "" {
		cfg.TransIDSuffix = "-4155-z"
	}
	if cfg.ServerHeader == "" {
		cfg.ServerHeader = "Ceph Object Gateway (squid)"
	}
	return s3.NewHandler(env, auth, cfg)
}

var _ = Describe("Handler", func() {
	var (
		store *memstore.Store
		h     *s3.Handler
	)
	BeforeEach(func() {
		store = memstore.New(memstore.Config{})
		h = newHandler(store, s3.AnonymousOnly{}, s3.Config{})
	})
	get := func(target string, hdr ...string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", target, nil)
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	It("answers an anonymous GET / with 200 and an empty ListAllMyBucketsResult, the probe path", func() {
		rec := get("/")
		Expect(rec.Code).To(Equal(200))
		Expect(rec.Header().Get("Content-Type")).To(Equal("application/xml"))
		Expect(rec.Body.String()).To(Equal(`<?xml version="1.0" encoding="UTF-8"?>` +
			`<ListAllMyBucketsResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">` +
			`<Owner><ID>anonymous</ID></Owner><Buckets></Buckets></ListAllMyBucketsResult>`))
	})
	It("stamps x-amz-request-id and Server on every response", func() {
		rec := get("/")
		Expect(rec.Header().Get("x-amz-request-id")).To(Equal("tx000000000000000000001-0068d7a1b2-4155-z"))
		Expect(rec.Header().Get("Server")).To(Equal("Ceph Object Gateway (squid)"))
		Expect(get("/").Header().Get("x-amz-request-id")).To(HavePrefix("tx000000000000000000002-"), "sequence advances")
	})
	It("renders radosgw's error document for a route without a handler", func() {
		rec := get("/plain?location")
		Expect(rec.Code).To(Equal(501))
		Expect(rec.Header().Get("Content-Type")).To(Equal("application/xml"))
		Expect(rec.Body.String()).To(Equal(`<?xml version="1.0" encoding="UTF-8"?><Error><Code>NotImplemented</Code><Message></Message>` +
			`<BucketName>plain</BucketName><RequestId>tx000000000000000000001-0068d7a1b2-4155-z</RequestId><HostId>4155-z-zg</HostId></Error>`))
		Expect(rec.Header().Get("Content-Length")).To(Equal(strconv.Itoa(rec.Body.Len())))
		Expect(rec.Header().Get("Accept-Ranges")).To(Equal("bytes"), "end_header's error branch sets the length through dump_content_length, rgw_rest.cc:620-624")
	})
	It("answers 405 MethodNotAllowed where radosgw has no op", func() {
		req := httptest.NewRequest("POST", "/", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		Expect(rec.Code).To(Equal(405))
		Expect(rec.Body.String()).To(ContainSubstring("<Code>MethodNotAllowed</Code>"))
		Expect(rec.Body.String()).NotTo(ContainSubstring("<BucketName>"), "no bucket in a service-scope error")
	})
	It("answers 400 for a NUL in the path before dispatch", func() {
		Expect(get("/plain/a%00b").Code).To(Equal(400))
		Expect(get("/plain/a%00b").Body.String()).To(ContainSubstring("<Code>InvalidRequest</Code>"))
	})
	DescribeTable("never serves a signed request as anonymous",
		func(target string, hdr ...string) {
			rec := get(target, hdr...)
			Expect(rec.Code).To(Equal(501), rec.Body.String())
			Expect(rec.Body.String()).To(ContainSubstring("<Code>NotImplemented</Code>"))
		},
		Entry("Authorization header", "/", "Authorization", "AWS4-HMAC-SHA256 Credential=x"),
		Entry("SigV4 query signature", "/?X-Amz-Signature=abc"),
		Entry("SigV4 query credential", "/?X-Amz-Credential=abc"),
		Entry("SigV2 query key", "/?AWSAccessKeyId=abc"),
		Entry("SigV2 query signature", "/?Signature=abc"),
	)
	It("answers 503 SlowDown past rgw_max_concurrent_requests and stays healthy for the probe", func() {
		release := make(chan struct{})
		slow := newHandler(store, s3.AnonymousOnly{}, s3.Config{MaxConcurrent: 1})
		slow.Register("list_buckets", func(_ context.Context, w http.ResponseWriter, r *op.Request) error {
			<-release
			w.WriteHeader(200)
			return nil
		})
		first := make(chan int)
		go func() {
			rec := httptest.NewRecorder()
			slow.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
			first <- rec.Code
		}()
		Eventually(func() int64 { return slow.InFlight() }).Should(BeEquivalentTo(1))
		rec := httptest.NewRecorder()
		slow.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
		Expect(rec.Code).To(Equal(503))
		Expect(rec.Body.String()).To(ContainSubstring("<Code>SlowDown</Code>"))
		Expect(rec.Header().Get("x-amz-request-id")).NotTo(BeEmpty())
		close(release)
		Expect(<-first).To(Equal(200))
		Expect(slow.InFlight()).To(BeZero())
	})
	It("suspends a suspended user", func() {
		alice := store.AddUser(meta.UserInfo{UserID: meta.UserID{ID: "alice"}, Suspended: 1, OpMask: op.OpTypeAll})
		as := s3.AuthenticatorFunc(func(context.Context, *http.Request, op.PayloadForms) (*op.AuthResult, error) {
			return &op.AuthResult{Identity: op.Identity{User: &alice.Info, Owner: meta.UserOwner(alice.Info.UserID), OpMask: op.OpTypeAll}}, nil
		})
		h = newHandler(store, as, s3.Config{})
		rec := get("/")
		Expect(rec.Code).To(Equal(403))
		Expect(rec.Body.String()).To(ContainSubstring("<Code>UserSuspended</Code>"))
	})
	It("gives a bucket named in the URL the identity's tenant unless the URL named one", func() {
		var seen *op.Request
		h.Register("stat_bucket", func(_ context.Context, w http.ResponseWriter, r *op.Request) error {
			seen = r
			w.WriteHeader(200)
			return nil
		})
		as := s3.AuthenticatorFunc(func(context.Context, *http.Request, op.PayloadForms) (*op.AuthResult, error) {
			return &op.AuthResult{Identity: op.Identity{User: &meta.UserInfo{}, Tenant: "t1", OpMask: op.OpTypeAll}}, nil
		})
		h = newHandler(store, as, s3.Config{})
		h.Register("stat_bucket", func(_ context.Context, w http.ResponseWriter, r *op.Request) error {
			seen = r
			w.WriteHeader(200)
			return nil
		})
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("HEAD", "/plain", nil))
		Expect(seen.Tenant).To(Equal("t1"), "identity tenant")
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("HEAD", "/t2:plain", nil))
		Expect(seen.Tenant).To(Equal("t2"), "explicit tenant")
	})
	It("records the status and byte counts on the request for metrics", func() {
		m := &opfakes.FakeMetrics{}
		env := &op.Env{Zone: store, Users: store, Authz: op.OwnerOnly{}, Metrics: m}
		h = s3.NewHandler(env, s3.AnonymousOnly{}, s3.Config{})
		get("/")
		Expect(m.ObserveCallCount()).To(Equal(1))
		name, status, _, _, out := m.ObserveArgsForCall(0)
		Expect(name).To(Equal("list_buckets"))
		Expect(status).To(Equal(200))
		Expect(out).To(BeNumerically(">", 0))
		Expect(m.InFlightCallCount()).To(Equal(2), "one increment, one decrement")
	})
	// process_request dispatches, sets s->op_type and only then authenticates
	// (rgw_process.cc:325-345 at v19.2.6), so the authenticator sees the
	// dispatched op's payload forms at the cluster's release.
	DescribeTable("hands the authenticator the dispatched route's payload forms",
		func(rel denc.Release, method, target string, want op.PayloadForms) {
			var got op.PayloadForms
			as := s3.AuthenticatorFunc(func(_ context.Context, _ *http.Request, payloads op.PayloadForms) (*op.AuthResult, error) {
				got = payloads
				return &op.AuthResult{Identity: op.Anonymous(), PayloadSHA256: "UNSIGNED-PAYLOAD"}, nil
			})
			rs := memstore.New(memstore.Config{Release: rel})
			newHandler(rs, as, s3.Config{}).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(method, target, nil))
			Expect(got).To(Equal(want))
		},
		Entry("PUT object", denc.Squid, "PUT", "/plain/k", op.PayloadSigned|op.PayloadChunked),
		Entry("GET ?location", denc.Squid, "GET", "/plain?location", op.PayloadForms(0)),
		Entry("GET ?logging on Squid", denc.Squid, "GET", "/plain?logging", op.PayloadForms(0)),
		Entry("GET ?logging on Tentacle", denc.Tentacle, "GET", "/plain?logging", op.PayloadSigned),
	)
})

var _ = Describe("response writer", func() {
	var store *memstore.Store
	BeforeEach(func() { store = memstore.New(memstore.Config{}) })
	serve := func(fn s3.HandlerFunc) *httptest.ResponseRecorder {
		h := newHandler(store, s3.AnonymousOnly{}, s3.Config{})
		h.Register("list_buckets", fn)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
		return rec
	}
	It("adds no Accept-Ranges to a Content-Length the route set itself", func() {
		rec := serve(func(_ context.Context, w http.ResponseWriter, _ *op.Request) error {
			w.Header().Set("Content-Length", "2")
			w.WriteHeader(200)
			_, err := w.Write([]byte("ok"))
			return err
		})
		Expect(rec.Header().Get("Content-Length")).To(Equal("2"))
		Expect(rec.Header()).NotTo(HaveKey("Accept-Ranges"), "only dump_content_length adds it, rgw_rest.cc:388-397")
	})
	It("sets a length the op names together with Accept-Ranges, as dump_content_length does", func() {
		h := http.Header{}
		s3.SetContentLength(h, 1024)
		Expect(h).To(Equal(http.Header{"Content-Length": {"1024"}, "Accept-Ranges": {"bytes"}}))
	})
	It("renders a WriteXML success with its length, no Accept-Ranges and the formatter's escaping", func() {
		type result struct {
			XMLName xml.Name     `xml:"Result"`
			Name    xmltext.Text `xml:"Name"`
		}
		rec := serve(func(_ context.Context, w http.ResponseWriter, r *op.Request) error {
			s3.WriteXML(w, r, 200, result{Name: `a&b "c" 'd' <e>`})
			return nil
		})
		body := `<?xml version="1.0" encoding="UTF-8"?><Result><Name>a&amp;b &quot;c&quot; &apos;d&apos; &lt;e&gt;</Name></Result>`
		Expect(rec.Body.String()).To(Equal(body))
		Expect(rec.Header().Get("Content-Type")).To(Equal("application/xml"))
		Expect(rec.Header().Get("Content-Length")).To(Equal(strconv.Itoa(len(body))))
		Expect(rec.Header()).NotTo(HaveKey("Accept-Ranges"), "end_header named no length, so the frontend's buffering filter adds this one without it")
	})
	It("escapes an error message as dump_string does and sends the length with Accept-Ranges", func() {
		rec := serve(func(context.Context, http.ResponseWriter, *op.Request) error {
			return op.ErrInvalidArgument.WithMessage(`bad "x" & 'y'`)
		})
		Expect(rec.Code).To(Equal(400))
		Expect(rec.Body.String()).To(ContainSubstring(`<Message>bad &quot;x&quot; &amp; &apos;y&apos;</Message>`))
		Expect(rec.Header().Get("Content-Length")).To(Equal(strconv.Itoa(rec.Body.Len())))
		Expect(rec.Header().Get("Accept-Ranges")).To(Equal("bytes"))
	})
	It("gives a streaming op a sink that merges its headers before the status", func() {
		rec := serve(func(_ context.Context, w http.ResponseWriter, _ *op.Request) error {
			w.Header().Set("x-amz-request-id", "tx1")
			sink := s3.SinkOfForTest(w)
			h := http.Header{"Content-Range": {"bytes 0-1/4"}}
			s3.SetContentLength(h, 2)
			sink.WriteHeader(206, h)
			if _, err := sink.Write([]byte("ab")); err != nil {
				return err
			}
			return sink.Flush()
		})
		Expect(rec.Code).To(Equal(206))
		Expect(rec.Header().Get("Content-Range")).To(Equal("bytes 0-1/4"))
		Expect(rec.Header().Get("x-amz-request-id")).To(Equal("tx1"), "headers the route set first stay")
		Expect(rec.Header().Get("Accept-Ranges")).To(Equal("bytes"), "the op's header map reaches the response whole")
		Expect(rec.Body.String()).To(Equal("ab"))
		Expect(rec.Flushed).To(BeTrue(), "Flush reaches the connection through the wrapper")
	})
})
```

`export_test.go` is `package s3` with `var SinkOfForTest = sinkOf`; later units add their own `…ForTest` exports to it.

`internal/xmltext/xmltext_test.go`, package `xmltext_test` (with the canon's `xmltext_suite_test.go`), pins every class of byte `xml_stream_escaper` treats differently ([escape.cc:134-169](https://github.com/ceph/ceph/blob/v19.2.6/src/common/escape.cc#L134-L169)) and the `Text` marshaling every S3 document relies on:

```go
var _ = Describe("Escape", func() {
	DescribeTable("escapes as xml_stream_escaper does",
		func(in, want string) {
			Expect(xmltext.Escape(in)).To(Equal(want))
		},
		Entry("text without a special byte, returned as is", "plain/key-1_2.txt", "plain/key-1_2.txt"),
		Entry("the empty string", "", ""),
		Entry("the five markup characters", `&<>'"`, "&amp;&lt;&gt;&apos;&quot;"),
		Entry("a quoted ETag", `"9b2cf535f27731c974343645a3985328"`, "&quot;9b2cf535f27731c974343645a3985328&quot;"),
		Entry("tab and newline, copied", "a\tb\nc", "a\tb\nc"),
		Entry("carriage return and the other bytes below 0x20, as references", "\r\x01\x1f", "&#x0d;&#x01;&#x1f;"),
		Entry("NUL, as a reference", "a\x00b", "a&#x00;b"),
		Entry("DEL, as a reference", "\x7f", "&#x7f;"),
		Entry("space and tilde, copied", " ~", " ~"),
		Entry("UTF-8, copied", "é日本", "é日本"),
		Entry("bytes from 0x80 up, copied even when they are not UTF-8", "k\x80\xff\xc3", "k\x80\xff\xc3"),
	)
})

var _ = Describe("Text", func() {
	type contents struct {
		XMLName  xml.Name       `xml:"Contents"`
		Key      xmltext.Text   `xml:"Key"`
		ETag     xmltext.Text   `xml:"ETag"`
		Size     int64          `xml:"Size"`
		Owner    xmltext.Text   `xml:"Owner,omitempty"`
		Empty    xmltext.Text   `xml:"Empty"`
		Prefixes []xmltext.Text `xml:"Prefix"`
	}
	It("marshals through encoding/xml with the escaper's bytes, honouring omitempty", func() {
		b, err := xml.Marshal(contents{Key: "a\x01b\xffc\x7f", ETag: `"e1"`, Size: 1, Prefixes: []xmltext.Text{"p<", "q'"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(string(b)).To(Equal("<Contents><Key>a&#x01;b\xffc&#x7f;</Key><ETag>&quot;e1&quot;</ETag><Size>1</Size>" +
			"<Empty></Empty><Prefix>p&lt;</Prefix><Prefix>q&apos;</Prefix></Contents>"))
	})
})
```

`AuthenticatorFunc` is the usual function adapter (`func (f AuthenticatorFunc) Authenticate(ctx context.Context, r *http.Request, payloads op.PayloadForms) (*op.AuthResult, error) { return f(ctx, r, payloads) }`); add it to `auth.go`. `Handler.InFlight() int64` is exported for this spec and for metrics.

- [ ] **Step 4: Write the failing ListBuckets rendering and framing specs**

`internal/s3/listbuckets_test.go`, from `RGWListBuckets_ObjStore_S3` (`get_params`, [rgw_rest_s3.h:133-136](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.h#L133-L136); `send_response_begin`, `send_response_data`, `send_response_end`, [rgw_rest_s3.cc:1511-1547](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L1511-L1547) at v19.2.6, [:1622-1658](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L1622-L1658) at v20.2.4; `dump_bucket` [:91-97](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L91-L97), [:96-102](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L96-L102)) and `dump_owner` ([rgw_rest.cc:506-517](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L506-L517) at v19.2.6, [:511-522](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L511-L522) at v20.2.4). radosgw's S3 ListBuckets reads neither `max-buckets` nor `continuation-token` and writes no `ContinuationToken` at either tag, so rgw-go's does neither: by code reading radosgw fails s3-tests' `test_list_buckets_paginated` at its third listing, which expects one bucket for `MaxBuckets=1`, and rgw-go fails it at the same assertion. The framing specs run the handler behind `httptest.NewServer`, so they see what a client sees:

```go
var _ = Describe("list_buckets", func() {
	created := meta.Time{Time: time.Date(2026, 9, 27, 1, 2, 3, 456_000_000, time.UTC)}
	var (
		store *memstore.Store
		as    s3.Authenticator
	)
	BeforeEach(func(ctx SpecContext) {
		store = memstore.New(memstore.Config{Now: func() time.Time { return created.Time }})
		alice := store.AddUser(meta.UserInfo{UserID: meta.UserID{ID: "alice"}, DisplayName: "Alice", OpMask: op.OpTypeAll})
		for _, name := range []string{"plain", "second"} {
			_, err := store.CreateBucket(ctx, op.CreateBucketParams{Name: name, Owner: meta.UserOwner(alice.Info.UserID)})
			Expect(err).NotTo(HaveOccurred())
		}
		as = s3.AuthenticatorFunc(func(context.Context, *http.Request, op.PayloadForms) (*op.AuthResult, error) {
			return &op.AuthResult{Identity: op.Identity{User: &alice.Info, Owner: meta.UserOwner(alice.Info.UserID), OpMask: op.OpTypeAll}}, nil
		})
	})
	const (
		opened = `<?xml version="1.0" encoding="UTF-8"?>` +
			`<ListAllMyBucketsResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">` +
			`<Owner><ID>alice</ID><DisplayName>Alice</DisplayName></Owner><Buckets>`
		plain  = `<Bucket><Name>plain</Name><CreationDate>2026-09-27T01:02:03.456Z</CreationDate></Bucket>`
		second = `<Bucket><Name>second</Name><CreationDate>2026-09-27T01:02:03.456Z</CreationDate></Bucket>`
		closed = `</Buckets></ListAllMyBucketsResult>`
	)
	serve := func(h http.Handler, method, target string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
		return rec
	}
	It("renders the owner and every bucket in send_response_begin, _data and _end's order", func() {
		rec := serve(newHandler(store, as, s3.Config{}), "GET", "/")
		Expect(rec.Code).To(Equal(200))
		Expect(rec.Header().Get("Content-Type")).To(Equal("application/xml"))
		Expect(rec.Body.String()).To(Equal(opened + plain + second + closed))
	})
	It("escapes the owner's display name as dump_owner's dump_string does", func() {
		s := memstore.New(memstore.Config{Now: func() time.Time { return created.Time }})
		ann := s.AddUser(meta.UserInfo{UserID: meta.UserID{ID: "ann"}, DisplayName: `A&B "C" <D>`, OpMask: op.OpTypeAll})
		annAuth := s3.AuthenticatorFunc(func(context.Context, *http.Request, op.PayloadForms) (*op.AuthResult, error) {
			return &op.AuthResult{Identity: op.Identity{User: &ann.Info, Owner: meta.UserOwner(ann.Info.UserID), OpMask: op.OpTypeAll}}, nil
		})
		rec := serve(newHandler(s, annAuth, s3.Config{}), "GET", "/")
		Expect(rec.Body.String()).To(ContainSubstring(`<Owner><ID>ann</ID><DisplayName>A&amp;B &quot;C&quot; &lt;D&gt;</DisplayName></Owner>`))
	})
	It("ignores max-buckets and continuation-token, which radosgw's S3 get_params never reads", func() {
		h := newHandler(store, as, s3.Config{})
		for _, target := range []string{"/?max-buckets=1", "/?max-buckets=0", "/?max-buckets=x", "/?continuation-token=plain"} {
			rec := serve(h, "GET", target)
			Expect(rec.Code).To(Equal(200), target)
			Expect(rec.Body.String()).To(Equal(opened+plain+second+closed), target)
		}
	})
	It("answers HEAD / with the headers alone: Content-Length 0 and neither Accept-Ranges nor Transfer-Encoding", func() {
		rec := serve(newHandler(store, as, s3.Config{}), "HEAD", "/")
		Expect(rec.Code).To(Equal(200))
		Expect(rec.Header().Get("Content-Type")).To(Equal("application/xml"))
		Expect(rec.Header().Get("Content-Length")).To(Equal("0"), "BufferingFilter::complete_request, rgw_client_io_filters.h:221-253")
		Expect(rec.Header().Get("Accept-Ranges")).To(BeEmpty())
		Expect(rec.Header().Get("Transfer-Encoding")).To(BeEmpty(), "dump_chunked_encoding skips a HEAD, rgw_rest.cc:399-411")
		Expect(rec.Body.Len()).To(BeZero())
	})
	Describe("over a connection", func() {
		var (
			users   *opfakes.FakeUserStore
			release chan struct{}
			srv     *httptest.Server
		)
		page := func(name string) []meta.BucketEnt {
			return []meta.BucketEnt{{Bucket: meta.BucketID{Name: name}, CreationTime: created}}
		}
		BeforeEach(func() {
			users = &opfakes.FakeUserStore{}
			release = make(chan struct{})
			users.ListUserBucketsCalls(func(_ context.Context, _ meta.Owner, marker string, _ int) ([]meta.BucketEnt, string, bool, error) {
				if marker == "" {
					return page("plain"), "plain", true, nil
				}
				<-release
				return page("second"), "", false, nil
			})
			env := &op.Env{Zone: store, Users: users, Buckets: store, Authz: op.OwnerOnly{}, Metrics: op.NopMetrics{},
				Now: func() time.Time { return time.Unix(0x68d7a1b2, 0) }, HostID: "4155-z-zg",
				Conf: cephconf.NewOptions(cephconf.MapGetter{"rgw_list_buckets_max_chunk": "1"})}
			srv = httptest.NewServer(s3.NewHandler(env, as, s3.Config{TransIDSuffix: "-4155-z", ServerHeader: "Ceph Object Gateway (squid)"}))
			DeferCleanup(srv.Close)
			DeferCleanup(func() { // runs first: a blocked page must not hold Close
				select {
				case <-release:
				default:
					close(release)
				}
			})
		})
		It("frames the listing chunked and sends each page as it is read", func() {
			resp, err := http.Get(srv.URL + "/")
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(200))
			Expect(resp.TransferEncoding).To(Equal([]string{"chunked"}), "end_header(..., CHUNKED_TRANSFER_ENCODING), rgw_rest_s3.cc:1519")
			Expect(resp.ContentLength).To(BeEquivalentTo(-1))
			Expect(resp.Header.Get("Accept-Ranges")).To(BeEmpty(), "no Content-Length, so no dump_content_length")
			Expect(resp.Header.Get("Content-Type")).To(Equal("application/xml"))
			var (
				mu  sync.Mutex
				got bytes.Buffer
			)
			go func() { // the client's reader: whatever the server has flushed shows up in got
				defer GinkgoRecover()
				buf := make([]byte, 512)
				for {
					n, err := resp.Body.Read(buf)
					mu.Lock()
					got.Write(buf[:n])
					mu.Unlock()
					if err != nil {
						return
					}
				}
			}()
			read := func() string { mu.Lock(); defer mu.Unlock(); return got.String() }
			Eventually(read).Should(Equal(opened+plain), "send_response_data flushes the first page while the second is read, rgw_rest_s3.cc:1529-1538")
			Consistently(read).WithTimeout(50 * time.Millisecond).Should(Equal(opened + plain))
			close(release)
			Eventually(read).Should(Equal(opened + plain + second + closed))
		})
		It("closes the document when a later page fails, as the scope guard's send_response_end does", func() {
			users.ListUserBucketsCalls(func(_ context.Context, _ meta.Owner, marker string, _ int) ([]meta.BucketEnt, string, bool, error) {
				if marker == "" {
					return page("plain"), "plain", true, nil
				}
				return nil, "", false, op.ErrInternalError
			})
			resp, err := http.Get(srv.URL + "/")
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(200), "the status went out with the first page")
			b, err := io.ReadAll(resp.Body)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(b)).To(Equal(opened + plain + closed))
		})
		It("answers a failed first page with an ordinary error document", func() {
			users.ListUserBucketsReturns(nil, "", false, op.ErrInternalError)
			resp, err := http.Get(srv.URL + "/")
			Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			Expect(resp.StatusCode).To(Equal(500), "send_response_begin with op_ret set: end_header's error branch, rgw_rest.cc:620-624")
			Expect(resp.TransferEncoding).To(BeEmpty())
			Expect(resp.Header.Get("Accept-Ranges")).To(Equal("bytes"))
			b, err := io.ReadAll(resp.Body)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(b)).To(ContainSubstring("<Code>InternalError</Code>"))
		})
	})
})
```

- [ ] **Step 5: Implement `handler.go`, `errors.go`, `headers.go`, `xml.go`, `auth.go`, `listbuckets.go`**

`listbuckets.go`:

```go
package s3

func serviceHandlers() map[string]HandlerFunc {
	return map[string]HandlerFunc{"list_buckets": listBuckets}
}

// listOwner is dump_owner's section; DisplayName is written only when set.
type listOwner struct {
	XMLName     xml.Name     `xml:"Owner"`
	ID          xmltext.Text `xml:"ID"`
	DisplayName xmltext.Text `xml:"DisplayName,omitempty"`
}

// listBucket is dump_bucket's section.
type listBucket struct {
	XMLName      xml.Name     `xml:"Bucket"`
	Name         xmltext.Text `xml:"Name"`
	CreationDate xmltext.Text `xml:"CreationDate"`
}

// listBuckets is RGWListBuckets_ObjStore_S3. Its get_params sets only
// limit = -1 (rgw_rest_s3.h:133-136): max-buckets and continuation-token are
// never read, and no ContinuationToken is written. The document streams in
// radosgw's order: send_response_begin sends the headers and the declaration,
// then opens the result, the owner and Buckets once the first page is read
// (rgw_rest_s3.cc:1511-1527); send_response_data adds each page and flushes
// it (:1529-1538); send_response_end closes the two sections and flushes
// (:1540-1547), after a failed later page too.
func listBuckets(ctx context.Context, w http.ResponseWriter, r *op.Request) error {
	var cx *chunkedXML
	o := &op.ListBuckets{Limit: -1}
	o.Begin = func() error {
		var err error
		if cx, err = startChunkedXML(w, r); err != nil {
			return err
		}
		// dump_start runs before end_header, whose own flush sends the declaration
		cx.add(xmlHeader)
		if err := cx.flush(); err != nil {
			return err
		}
		cx.add(`<ListAllMyBucketsResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`)
		if err := cx.encode(listOwner{ID: xmltext.Text(r.Identity.Owner.String()), DisplayName: xmltext.Text(r.Identity.User.DisplayName)}); err != nil {
			return err
		}
		cx.add("<Buckets>")
		return nil
	}
	o.Page = func(page []meta.BucketEnt) error {
		for _, b := range page {
			if err := cx.encode(listBucket{Name: xmltext.Text(b.Bucket.Name), CreationDate: xmltext.Text(ISO8601(b.CreationTime.Time))}); err != nil {
				return err
			}
		}
		return cx.flush()
	}
	err := op.Run(ctx, o, r)
	if cx == nil {
		return err // nothing was sent: an ordinary error response
	}
	if err != nil {
		slog.WarnContext(ctx, "bucket listing failed after the response started", slog.Any("error", err))
	}
	cx.add("</Buckets></ListAllMyBucketsResult>")
	if err := cx.flush(); err != nil {
		slog.DebugContext(ctx, "bucket listing not delivered", slog.Any("error", err))
	}
	return nil
}
```

The handler-level specs' expected strings are the arbiter of the XML shape; the root element carries its namespace as `xmlns="..."`, as radosgw's `open_array_section_in_ns` writes it ([rgw_rest_s3.cc:81-84](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L81-L84)). Every string a document carries is an `xmltext.Text`, so it is escaped as `dump_string` escapes it; the fixed markup `add` writes needs no escaping.

`errors.go`:

```go
// errorDocument is radosgw's rgw_err dump (rgw_common.cc:395-418); every
// field is dump_string text.
type errorDocument struct {
	XMLName    xml.Name     `xml:"Error"`
	Code       xmltext.Text `xml:"Code,omitempty"`
	Message    xmltext.Text `xml:"Message"`
	BucketName xmltext.Text `xml:"BucketName,omitempty"`
	RequestID  xmltext.Text `xml:"RequestId,omitempty"`
	HostID     xmltext.Text `xml:"HostId"`
}

// WriteError renders err as the S3 error document: end_header's error branch
// (rgw_rest.cc:620-624 at v19.2.6, :625-629 at v20.2.4), whose
// dump_content_length sends the document's length with Accept-Ranges.
func WriteError(ctx context.Context, w http.ResponseWriter, r *op.Request, err error) {
	e := op.AsError(err)
	if e == op.ErrInternalError || e == op.ErrUnknown {
		slog.ErrorContext(ctx, "request failed", slog.String("request_id", r.ID), slog.Any("error", err))
	}
	writeXML(w, r, e.Status, errorDocumentFor(r, e), true)
}

func errorDocumentFor(r *op.Request, e *op.Error) errorDocument {
	doc := errorDocument{
		Code:       xmltext.Text(e.Code),
		Message:    xmltext.Text(e.Message),
		BucketName: xmltext.Text(r.Bucket),
		RequestID:  xmltext.Text(r.ID),
	}
	if r.Env != nil {
		doc.HostID = xmltext.Text(r.Env.HostID)
	}
	return doc
}
```

`xml.go` holds the declaration and the two document writers:

```go
// xmlHeader is the declaration XMLFormatter's output_header writes; radosgw
// sends no line break after it.
const xmlHeader = `<?xml version="1.0" encoding="UTF-8"?>`

func WriteXML(w http.ResponseWriter, r *op.Request, status int, v any) {
	writeXML(w, r, status, v, false)
}

// writeXML sends xmlHeader and v at status. A success's end_header names no
// length, so its Content-Length is the one radosgw's frontend adds when the
// response completes (rgw_client_io_filters.h:221-253), without
// Accept-Ranges. errDoc is end_header's error branch (rgw_rest.cc:620-624):
// the length goes out through dump_content_length, and x-amz-request-charged,
// which end_header adds only when !is_err() (:597-601), is withheld.
func writeXML(w http.ResponseWriter, r *op.Request, status int, v any, errDoc bool) {
	body, err := xml.Marshal(v)
	if err != nil {
		// xml.Marshal fails only for a value it cannot encode, a map, a channel
		// or a func, which no document type holds; answer as radosgw answers an
		// unexpected failure. An errorDocument holds only Text and always marshals.
		slog.Error("response document does not marshal", slog.String("request_id", r.ID), slog.Any("error", err))
		status, errDoc = op.ErrInternalError.Status, true
		body, _ = xml.Marshal(errorDocumentFor(r, op.ErrInternalError))
	}
	SetCommonHeaders(w, r)
	h := w.Header()
	h.Set("Content-Type", "application/xml")
	n := uint64(len(xmlHeader) + len(body))
	if errDoc {
		h.Del("x-amz-request-charged")
		SetContentLength(h, n)
	} else {
		h.Set("Content-Length", strconv.FormatUint(n, 10))
	}
	w.WriteHeader(status)
	// a failed write means the client is gone; there is no one left to tell
	_, _ = w.Write(append([]byte(xmlHeader), body...))
}

// ISO8601 is rgw_to_iso8601's millisecond form.
func ISO8601(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }
```

`headers.go` holds `SetCommonHeaders`, as specified above, and `SetContentLength`:

```go
// SetContentLength is dump_content_length (rgw_rest.cc:388-397 at v19.2.6
// and v20.2.4): the length and Accept-Ranges: bytes. radosgw calls it for
// every error document and for a success whose op names its length: the GET
// and HEAD of an object (rgw_rest_s3.cc:459 at v19.2.6, :495 at v20.2.4) and
// the PUT of an object or part without a copy source (:2747, :2911). Every
// other Content-Length radosgw sends is the one its frontend adds when the
// response completes, which carries no Accept-Ranges.
func SetContentLength(h http.Header, n uint64) {
	h.Set("Content-Length", strconv.FormatUint(n, 10))
	h.Set("Accept-Ranges", "bytes")
}
```

`xml.go` also holds the chunked framing the listings share:

```go
// chunkedXML is a response radosgw sends with end_header(...,
// CHUNKED_TRANSFER_ENCODING) (rgw_rest.cc:626-627). What add and encode
// collect leaves as one chunk at flush, where radosgw's rgw_flush_formatter
// hands the formatter's output to the chunking filter
// (rgw_client_io_filters.h:282-302).
type chunkedXML struct {
	w    http.ResponseWriter
	rc   *http.ResponseController
	head bool
	buf  bytes.Buffer
}

func startChunkedXML(w http.ResponseWriter, r *op.Request) (*chunkedXML, error) {
	c := &chunkedXML{w: w, rc: http.NewResponseController(w), head: r.Method == http.MethodHead}
	SetCommonHeaders(w, r)
	h := w.Header()
	h.Set("Content-Type", "application/xml")
	if c.head {
		// dump_chunked_encoding skips a HEAD (rgw_rest.cc:399-411), so the
		// buffering filter completes the response with its empty body's
		// length and no Accept-Ranges (rgw_client_io_filters.h:221-253);
		// net/http adds no length to a HEAD it did not write.
		h.Set("Content-Length", "0")
		w.WriteHeader(http.StatusOK)
		return c, nil
	}
	w.WriteHeader(http.StatusOK)
	return c, c.rc.Flush()
}

func (c *chunkedXML) add(s string) {
	if !c.head {
		c.buf.WriteString(s)
	}
}

func (c *chunkedXML) encode(v any) error {
	if c.head {
		return nil
	}
	return xml.NewEncoder(&c.buf).Encode(v)
}

// flush sends what was collected as one chunk and nothing when nothing was:
// rgw_flush_formatter skips empty output and every HEAD (rgw_rest.cc:316-324).
func (c *chunkedXML) flush() error {
	if c.head || c.buf.Len() == 0 {
		return nil
	}
	_, err := c.w.Write(c.buf.Bytes())
	c.buf.Reset()
	if err != nil {
		return err
	}
	return c.rc.Flush()
}

func writeChunkedXML(ctx context.Context, w http.ResponseWriter, r *op.Request, v any) {
	c, err := startChunkedXML(w, r)
	if err == nil {
		c.add(xmlHeader)
		err = c.encode(v)
	}
	if err == nil {
		err = c.flush()
	}
	if err != nil {
		slog.DebugContext(ctx, "chunked response not delivered", slog.Any("error", err))
	}
}
```

One write per flush keeps a flush one chunk: net/http's response buffer, empty after every flush, hands a write larger than itself straight to the chunk writer, and `rc.Flush` sends a smaller one whole.


`auth.go`: `AnonymousOnly.Authenticate(ctx, req, _ op.PayloadForms)` checks `Authorization` and the four query keys (after `ParseRequest`'s lowercasing the SigV4 ones are `x-amz-signature` and `x-amz-credential`; check both spellings on the raw `http.Request` since the authenticator sees `req.URL.Query()`), returns `op.ErrNotImplemented` for any, else `&op.AuthResult{Identity: op.Anonymous(), PayloadSHA256: "UNSIGNED-PAYLOAD"}`.

`handler.go` implements the lifecycle above, with `bucketHandlers()` and `objectHandlers()` returning empty maps in `bucket.go` and `object.go` for M, R, W and P to fill (one file each, so parallel units do not conflict).

The writer every route receives, and the sink a streaming op writes to, in `handler.go`:

```go
// responseWriter records what the metrics observe and lets
// http.ResponseController reach the connection through Flush and Unwrap. It
// adds no header: Accept-Ranges goes out only where a route calls
// SetContentLength or WriteError, as radosgw sends it only where
// dump_content_length runs.
type responseWriter struct {
	http.ResponseWriter
	r     *op.Request
	wrote bool
}

func (w *responseWriter) WriteHeader(status int) {
	if w.wrote {
		return
	}
	w.wrote = true
	w.r.Status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *responseWriter) Write(p []byte) (int, error) {
	w.WriteHeader(http.StatusOK)
	n, err := w.ResponseWriter.Write(p)
	w.r.BytesOut += int64(n)
	return n, err
}

func (w *responseWriter) Flush() {
	w.WriteHeader(http.StatusOK)
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *responseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// sink is the op.Sink sinkOf returns.
type sink struct {
	w  http.ResponseWriter
	rc *http.ResponseController
}

func sinkOf(w http.ResponseWriter) op.Sink {
	return &sink{w: w, rc: http.NewResponseController(w)}
}

// WriteHeader merges h into the response's headers and then sends the
// status; a route that names the length puts it in h through
// SetContentLength.
func (s *sink) WriteHeader(status int, h http.Header) {
	maps.Copy(s.w.Header(), h)
	s.w.WriteHeader(status)
}

func (s *sink) Write(p []byte) (int, error) { return s.w.Write(p) }

func (s *sink) Flush() error { return s.rc.Flush() }
```

`internal/xmltext/xmltext.go`, the leaf every XML writer shares (`s3` here; M, R, W, P and Z through their documents; N's `formatter` for the admin API's XML):

```go
// Package xmltext escapes XML element text as ceph's XMLFormatter does. It is
// the one escaper behind every XML document rgw-go writes: the S3 responses
// and the admin API's XML format. It imports nothing from rgw-go, so any
// package may use it.
package xmltext

import (
	"encoding/xml"
	"strings"
)

// Escape is xml_stream_escaper (src/common/escape.cc:134-169 at v19.2.6 and
// v20.2.4), which XMLFormatter applies to every string it writes as element
// text (Formatter.cc:516-571 at v19.2.6, :536-591 at v20.2.4): the
// ampersand, the angle brackets, the apostrophe and the double quote become
// &amp;, &lt;, &gt;, &apos; and &quot;; a byte below 0x20 other than tab and
// newline, and 0x7f, becomes a character reference with two lowercase hex
// digits, &#x01; for 0x01; every other byte is copied, 0x80 and above
// included, whether or not it is part of valid UTF-8.
func Escape(s string) string {
	i := 0
	for i < len(s) && !special(s[i]) {
		i++
	}
	if i == len(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 16)
	b.WriteString(s[:i])
	for ; i < len(s); i++ {
		switch c := s[i]; c {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '\'':
			b.WriteString("&apos;")
		case '"':
			b.WriteString("&quot;")
		default:
			if control(c) {
				b.WriteString("&#x")
				b.WriteByte(hexDigits[c>>4])
				b.WriteByte(hexDigits[c&0x0f])
				b.WriteByte(';')
			} else {
				b.WriteByte(c)
			}
		}
	}
	return b.String()
}

const hexDigits = "0123456789abcdef"

// control is the escaper's test for a byte it writes as a character
// reference (escape.cc:157).
func control(c byte) bool {
	return (c < 0x20 && c != '\t' && c != '\n') || c == 0x7f
}

func special(c byte) bool {
	switch c {
	case '&', '<', '>', '\'', '"':
		return true
	}
	return control(c)
}

// Text is element text that encoding/xml writes escaped as Escape escapes
// it. encoding/xml's own escaping is not radosgw's: it writes &#34; and &#39;
// for the quotes, escapes tab, newline and carriage return, copies 0x7f, and
// replaces every other control byte and every byte of invalid UTF-8 with
// U+FFFD, so a key holding such a byte would be listed under a name other
// than the one stored. omitempty applies to a Text field as to a string.
type Text string

// MarshalXML writes start, the escaped text and the end element.
func (t Text) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	return e.EncodeElement(struct {
		Raw string `xml:",innerxml"`
	}{Escape(string(t))}, start)
}
```

Run `go test -tags=ceph_preview ./internal/xmltext/ ./internal/s3/ ./internal/op/`; expected PASS.

- [ ] **Step 6: Commit**

`make check`, then `feat(xmltext): escape XML text as ceph's XMLFormatter does`, `feat(s3): run radosgw's request lifecycle with its error documents, headers and transaction ids` and `feat(op): add ListBuckets, streamed page by page, the pipeline baseline and probe path`.

---

### Task 5: `frontend`: listeners, TLS, deadlines, drain

**Files:**
- Create: `internal/frontend/server.go`, `internal/frontend/tls.go`, `internal/frontend/deadline.go`, `internal/frontend/server_test.go`, `internal/frontend/tls_test.go`, `internal/frontend/deadline_test.go`, `internal/frontend/testdata/` (a self-signed certificate and key generated by the suite into a temp dir, not committed)
- Modify: `docs/exclusions.md` (coexistence section: the frontend differences listed in Step 4)

**Interfaces:**
- Consumes: Task 1's `Spec`, `ParseBeast`, `DefaultRequestTimeout`.
- Produces:

```go
package frontend

// DrainTimeout bounds Serve's graceful shutdown; Rook's default termination
// grace period is 30 s.
const DrainTimeout = 30 * time.Second

// Server is every listener one beast spec asks for, serving one handler.
type Server struct{ /* spec, handler, listeners []net.Listener, tlsConfig, servers []*http.Server */ }

// New builds the server for spec around h. It loads the certificate when
// spec has TLS listeners and fails on an unusable one, as ssl_init does.
func New(spec Spec, h http.Handler) (*Server, error)

// Listen binds every port and endpoint. It runs before privileges drop, as
// AsioFrontend::init binds and then drop_privileges runs. A port listens on
// both address families; a family the host lacks is skipped with a warning,
// and no listener at all is an error, as radosgw refuses to start.
func (s *Server) Listen() error

// Addrs returns the bound addresses, for tests and the startup log.
func (s *Server) Addrs() []net.Addr

// Serve accepts on every listener until ctx ends, then drains: Shutdown with
// DrainTimeout, then Close. It returns nil on a clean drain.
func (s *Server) Serve(ctx context.Context) error

// TLSConfig builds crypto/tls's configuration from the spec's ssl_* keys.
func TLSConfig(spec Spec) (*tls.Config, error)

// Deadlines wraps h so every read of the body and every write of the response
// extends the connection's deadline by timeout, beast's per-operation
// request_timeout_ms, while the http.Server itself keeps zero timeouts.
func Deadlines(h http.Handler, timeout time.Duration) http.Handler
```

TLS mapping, from `AsioFrontend::ssl_reload` ([`rgw_asio_frontend.cc:967-1045`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_asio_frontend.cc#L967-L1045)): `ssl_certificate` is `tls.LoadX509KeyPair(cert, key)` with `key = cert` when `ssl_private_key` is empty (the PEM holds both, which is what Rook's `rgw-cert.pem` does); `config://` values are not supported and fail with an error naming the key (Rook writes files). `ssl_options`: `no_tlsv1` and `no_tlsv1_1` are already Go's floor (`MinVersion` 1.2); `no_tlsv1_2` raises `MinVersion` to 1.3; `no_tlsv1_3` caps `MaxVersion` at 1.2; `default_workarounds`, `no_compression`, `no_sslv2`, `no_sslv3` and `single_dh_use` are what Go always does and are accepted silently; any other item is an error, as radosgw's `else` branch is. `ssl_ciphers`: each item is looked up by its OpenSSL name and its IANA name against `tls.CipherSuites()` through a fixed table of Go's TLS 1.2 suites (`ECDHE-ECDSA-AES128-GCM-SHA256` ↔ `TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256`, and the other eight AEAD suites Go ships); an unknown item is logged and skipped; none selected is an error, as `SSL_CTX_set_cipher_list` failing is. TLS 1.3 suites are not configurable in Go; `ssl_ciphersuites` is reported unknown by the parser and logged.

- [ ] **Step 1: Write the failing specs**

`server_test.go` starts a `Server` on `Spec{Endpoints: []string{"127.0.0.1:0"}}` with a handler that echoes the method, asserts `Addrs()` has one address, that a GET round-trips, that cancelling the context makes `Serve` return within `DrainTimeout` while an in-flight slow request still completes (start a request whose handler blocks on a channel, cancel the context, release the handler, expect the client to get 200 and `Serve` to return nil), and that `Listen` on a `Spec{Ports: []int{1}}` fails as a non-root user with an error mentioning the port. `tls_test.go` generates a self-signed ECDSA certificate into `GinkgoT().TempDir()`, writes cert and key into one PEM and into two, asserts `TLSConfig` loads both layouts, that `ssl_private_key` without `ssl_certificate` fails, that `ssl_options=no_tlsv1_2` gives `MinVersion == tls.VersionTLS13`, that `ssl_options=bogus` fails, that `ssl_ciphers=ECDHE-RSA-AES128-GCM-SHA256:UNKNOWN` selects one suite, that `ssl_ciphers=UNKNOWN` fails, and that a `Server` on `SSLEndpoints` serves an `https` GET to a client trusting the certificate. `deadline_test.go` uses `httptest.NewUnstartedServer` with `Deadlines(h, 200*time.Millisecond)`: a client that opens a PUT, sends headers and then nothing sees the handler's `Read` fail within a second; a handler writing to a client that never reads (`net.Conn` with a tiny receive buffer and no reads) sees `Write` fail; a request that reads and writes normally, slower than the timeout overall but faster per operation, succeeds; and the handler's `r.Context()` is done after the client disconnects (pin with `Eventually` on a channel the handler closes when `<-ctx.Done()` fires).

- [ ] **Step 2: Run to fail, implement**

`server.go`: `Listen` iterates `Ports` (one `net.Listen("tcp", ":port")`, which binds both families on Linux; on `address family not supported` fall back to `"tcp4"` and warn), `Endpoints` (`net.Listen("tcp", ep)`), and the SSL variants wrapped with `tls.NewListener`. `Serve` creates one `http.Server{Handler: h, ReadTimeout: 0, WriteTimeout: 0, ReadHeaderTimeout: spec.RequestTimeout, IdleTimeout: spec.RequestTimeout, MaxHeaderBytes: spec.MaxHeaderSize, ConnContext: ...}` per listener (the `ConnContext` sets `TCP_NODELAY` false when `spec.TCPNoDelay` is `*false`; Go's default is on), runs each `Serve` in an errgroup, and on `ctx.Done()` calls `Shutdown` with a `DrainTimeout` context on every server, then `Close`. `http.ErrServerClosed` is not an error.

`deadline.go`: the body wrapper calls `rc.SetReadDeadline(time.Now().Add(timeout))` before each `Read`; the writer wrapper calls `rc.SetWriteDeadline(...)` before each `Write` and `Flush`, and implements `Unwrap` so a later `http.ResponseController` still works; `rc` is `http.NewResponseController(w)`. A `SetReadDeadline` error of `http.ErrNotSupported` (a test recorder) is ignored.

Run `go test -tags=ceph_preview -race ./internal/frontend/`; expected PASS.

- [ ] **Step 3: Log what the frontend ignores**

`New` logs, once, one line per: `Unknown` key (`slog.Warn("ignoring unknown frontend key", slog.String("key", k), slog.Any("values", v))`), `MaxConnectionBacklog` set (`"max_connection_backlog is not configurable in Go, the kernel backlog applies"`), and `ssl_ciphers` items skipped.

- [ ] **Step 4: Record the differences**

Append to the coexistence section of `docs/exclusions.md`, under a "Frontend differences" heading: TCP_NODELAY defaults on (radosgw: off unless `tcp_nodelay=1`); an HTTP/1.1 response on a connection that stays open carries no `Connection: Keep-Alive` header (beast writes `Connection: Keep-Alive` or `close` on every response, [`rgw_asio_client.cc:152-158`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_asio_client.cc#L152-L158) at both tags; net/http writes the header only to close or for an HTTP/1.0 keep-alive), which HTTP/1.1 clients treat alike; `max_connection_backlog`, `so_reuseport`, `ssl_ciphersuites` and `tls_groups` are accepted and ignored; TLS 1.3 cipher suites are Go's; `config://` certificate sources are unsupported; the `Server` header carries the cluster's release name as radosgw's does. Report the change so the rgw-rs session is told.

- [ ] **Step 5: Commit**

`make check`, then `feat(frontend): serve the beast spec with TLS, per-request deadlines and drain`.

---

### Task 6: `cephconf` startup: early arguments, enabled APIs, privilege drop; `cli.Argv`

**Files:**
- Create: `internal/cephconf/early.go`, `internal/cephconf/apis.go`, `internal/cephconf/privileges.go`, `internal/cephconf/early_test.go`, `internal/cephconf/apis_test.go`, `internal/cephconf/privileges_test.go`
- Modify: `internal/cli/root.go` (`Argv`), `internal/cli/root_test.go`, `cmd/rgw-go/main.go` (`cli.Run(ctx, cli.Argv(os.Args), ...)`)

**Interfaces:**
- Consumes: Task 1's `Options`, `MapGetter`.
- Produces:

```go
package cephconf

// EarlyArgs is what ceph_argparse_early_args consumes before any config is
// read (src/common/ceph_argparse.cc): the arguments that decide which
// cluster, entity name and config file the rest is parsed against.
type EarlyArgs struct {
	// Cluster is --cluster, default "ceph".
	Cluster string
	// Name is the entity name from -n/--name ("client.rgw.a") or from
	// -i/--id/--user ("rgw.a" becomes "client.rgw.a"); default "client.admin".
	Name string
	// ConfFile is -c/--conf, "" for the default search path.
	ConfFile string
	// NoConfigFile is --no-config-file.
	NoConfigFile bool
	// Version is -v/--version: print the version and exit before connecting.
	Version bool
	// Rest is every other argument, in order, for librados to parse.
	Rest []string
}

// ParseEarly consumes the early arguments from argv. A "--" ends the scan
// and stays in Rest. A -n/--name whose value is not TYPE.ID is an error.
func ParseEarly(argv []string) (EarlyArgs, error)

// APIs is rgw_enable_apis as rgw-go honours it.
type APIs struct {
	S3    bool
	Admin bool
	// Ignored lists every other name the option carried (swift, swift_auth,
	// s3website, sts, iam, notifications, s3control, s3vectors, ...), for the
	// one startup log line the spec asks for.
	Ignored []string
}

// EnabledAPIs reads rgw_enable_apis. s3website is a specialisation of s3 and
// enables it, as rgw::AppMain::cond_init_apis does.
func EnabledAPIs(o *Options) (APIs, error)

// DropPrivileges is global_init's setuser/setgroup handling followed by
// AsioFrontend's drop_privileges: as root, resolve setuser and setgroup (a
// number or a name), honour setuser_match_path, then setgid and setuid for
// every thread, librados's included. As a non-root user it logs and does
// nothing. It runs after the RADOS connect and after every listener is
// bound, so privileged ports bind as the launching user, and before serving;
// serve enforces that order.
func DropPrivileges(o *Options) error
```

```go
package cli

// Argv rewrites os.Args for cobra. When the binary was invoked as radosgw,
// which is how Rook launches it, everything after the program name is
// ceph's argv and becomes "serve -- <args>", so cobra stays the one entry
// point and rgw-go's own flags come from RGW_GO_* variables. Otherwise it is
// os.Args[1:] unchanged.
func Argv(argv []string) []string
```

Rook's exact argv (`rook pkg/operator/ceph/object/spec.go:449-460`, `controller/spec.go DaemonFlags`, `config/defaults.go`), which Task 9 replays: `radosgw --fsid=<fsid> --keyring=/etc/ceph/keyring-store/keyring --default-log-to-stderr=true --default-err-to-stderr=true --default-mon-cluster-log-to-stderr=true '--default-log-stderr-prefix=debug ' --default-log-to-file=false --default-mon-cluster-log-to-file=false --mon-host=$(ROOK_CEPH_MON_HOST) --mon-initial-members=$(ROOK_CEPH_MON_INITIAL_MEMBERS) --id=rgw.ceph.objectstore.a --setuser=ceph --setgroup=ceph --foreground '--rgw-frontends=beast port=8080' --rgw-mime-types-file=/etc/ceph/rgw/mime.types --rgw-realm=ceph-objectstore --rgw-zonegroup=ceph-objectstore --rgw-zone=ceph-objectstore` (`--crush-location=` and `--service-unique-id=` on some paths). Everything but `--id` is librados's: `md_config_t::parse_argv` (`src/common/config.cc`) handles `--foreground`/`-f`, `-d`, `--mon-host`, `--keyring`, `--service_unique_id`, and every `--<option>=<value>` including `--default-<option>` and `--rgw-frontends`, and leaves an unknown argument in place without failing.

- [ ] **Step 1: Write the failing specs**

`early_test.go`:

```go
var _ = Describe("ParseEarly", func() {
	It("takes ceph's early arguments and leaves the rest in order", func() {
		e, err := cephconf.ParseEarly([]string{"--foreground", "--id=rgw.a", "-c", "/etc/ceph/x.conf", "--cluster", "prod",
			"--rgw-frontends=beast port=8080", "--no-config-file", "--setuser=ceph"})
		Expect(err).NotTo(HaveOccurred())
		Expect(e.Name).To(Equal("client.rgw.a"))
		Expect(e.ConfFile).To(Equal("/etc/ceph/x.conf"))
		Expect(e.Cluster).To(Equal("prod"))
		Expect(e.NoConfigFile).To(BeTrue())
		Expect(e.Rest).To(Equal([]string{"--foreground", "--rgw-frontends=beast port=8080", "--setuser=ceph"}))
	})
	It("defaults to cluster ceph and client.admin", func() {
		e, err := cephconf.ParseEarly(nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(e.Cluster).To(Equal("ceph"))
		Expect(e.Name).To(Equal("client.admin"))
	})
	DescribeTable("accepts every spelling of the name",
		func(args []string, want string) {
			e, err := cephconf.ParseEarly(args)
			Expect(err).NotTo(HaveOccurred())
			Expect(e.Name).To(Equal(want))
		},
		Entry("--id=", []string{"--id=rgw.a"}, "client.rgw.a"),
		Entry("--id value", []string{"--id", "rgw.a"}, "client.rgw.a"),
		Entry("-i", []string{"-i", "rgw.a"}, "client.rgw.a"),
		Entry("--user", []string{"--user=rgw.a"}, "client.rgw.a"),
		Entry("-n type.id", []string{"-n", "client.rgw.a"}, "client.rgw.a"),
		Entry("--name=", []string{"--name=client.rgw.a"}, "client.rgw.a"),
		Entry("--conf= spelling", []string{"--conf=/x"}, "client.admin"),
	)
	It("rejects a name without a type", func() {
		_, err := cephconf.ParseEarly([]string{"-n", "rgw.a"})
		Expect(err).To(MatchError(ContainSubstring("TYPE.ID")))
	})
	It("stops at -- and keeps it", func() {
		e, err := cephconf.ParseEarly([]string{"--id=a", "--", "--id=b"})
		Expect(err).NotTo(HaveOccurred())
		Expect(e.Name).To(Equal("client.a"))
		Expect(e.Rest).To(Equal([]string{"--", "--id=b"}))
	})
	It("reports -v and --version", func() {
		for _, f := range []string{"-v", "--version"} {
			e, err := cephconf.ParseEarly([]string{f})
			Expect(err).NotTo(HaveOccurred())
			Expect(e.Version).To(BeTrue(), f)
		}
	})
})
```

`apis_test.go`: `MapGetter{"rgw_enable_apis": "s3, s3control, s3website, swift, swift_auth, admin, sts, iam, notifications, s3vectors"}` (radosgw's default) gives `S3 && Admin` with `Ignored` = the other eight in order; `"swift, admin"` gives `S3 false`; `"s3website"` alone gives `S3 true`; `"s3,admin"` without spaces parses.

`privileges_test.go`: as a non-root user, `DropPrivileges(MapGetter{"setuser": "ceph", "setgroup": "ceph", "setuser_match_path": ""})` returns nil and leaves `os.Getuid()` unchanged; `MapGetter{"setuser": "", "setgroup": ""}` returns nil; the resolution helpers are tested directly: `resolveUser("0")` is uid 0, `resolveUser(strconv.Itoa(os.Getuid()))` is the current uid, `resolveUser("no-such-user-xyz")` errors, `resolveGroup(strconv.Itoa(os.Getgid()))` is the current gid; `matchPathAllows(path, uid, gid)` is true for a temp file owned by the current user with the current ids and false with `uid+1`. The root path itself is exercised in Task 9's container run.

`root_test.go` gains:

```go
var _ = Describe("Argv", func() {
	It("passes rgw-go's own argv through", func() {
		Expect(cli.Argv([]string{"/usr/bin/rgw-go", "version"})).To(Equal([]string{"version"}))
	})
	It("rewrites radosgw's argv into serve -- args", func() {
		Expect(cli.Argv([]string{"/usr/bin/radosgw", "--foreground", "--id=rgw.a", "--rgw-frontends=beast port=8080"})).
			To(Equal([]string{"serve", "--", "--foreground", "--id=rgw.a", "--rgw-frontends=beast port=8080"}))
	})
	It("matches the base name only", func() {
		Expect(cli.Argv([]string{"radosgw"})).To(Equal([]string{"serve", "--"}))
		Expect(cli.Argv([]string{"/opt/radosgw-wrapper", "x"})).To(Equal([]string{"x"}))
	})
})
```

- [ ] **Step 2: Run to fail, implement**

`early.go` walks argv: `--` breaks; `--version`/`-v` sets `Version`; `--conf`/`-c`, `--cluster`, `--id`/`--user`, `-i`, `--name`/`-n` take a value from `=` or the next argument; `--no-config-file` sets the flag; `--show_args` is dropped; everything else appends to `Rest`. `-i` is accepted although ceph ignores it for client-type daemons (`ceph_argparse.cc`, the `module_type != CEPH_ENTITY_TYPE_CLIENT` check), because the spec lists it. A name from `--id` is `"client." + id`; `--name` must match `^[a-z-]+\.` with a known type (`client`, `mon`, `osd`, `mds`, `mgr`) or it is an error quoting ceph's "expected string of the form TYPE.ID".

`apis.go` uses `Options.List`; `privileges.go` resolves through `os/user` (`user.Lookup`, `user.LookupId`, `user.LookupGroup`, `user.LookupGroupId`), honours `setuser_match_path` exactly as [`global_init.cc:329-348`](https://github.com/ceph/ceph/blob/7ed73efc1beb034b76e150439972959857600920/src/global/global_init.cc#L329-L348) (mismatch logs a warning and drops nothing), then `syscall.Setgroups([]int{gid})`, `syscall.Setgid(gid)`, `syscall.Setuid(uid)`; with cgo linked those call libc, which applies them to every thread, librados's included. Log `slog.Info("set uid and gid", slog.Int("uid", uid), slog.Int("gid", gid))` as `drop_privileges` does at level 0.

`cli.Argv`: `filepath.Base(argv[0]) == "radosgw"` selects the rewrite.

Run `go test -tags=ceph_preview ./internal/cephconf/ ./internal/cli/`; expected PASS.

- [ ] **Step 3: Commit**

`make check`, then `feat(cephconf): parse ceph's early arguments, enabled APIs and privilege drop` and `feat(cli): rewrite radosgw's argv into serve`.

---

### Task 7: `metrics`

**Files:**
- Create: `internal/metrics/doc.go`, `internal/metrics/metrics.go`, `internal/metrics/metrics_suite_test.go`, `internal/metrics/metrics_test.go`
- Modify: `go.mod` (`github.com/prometheus/client_golang@v1.24.1`, the version in the module cache)

**Interfaces:**
- Consumes: `op.Metrics`, `radosclient.StatsReporter`, `version.Read`.
- Produces:

```go
package metrics

// Registry is rgw-go's Prometheus registry: a minimal surface (per-op counts
// and latency, in-flight requests, RADOS ops and bytes), nothing radosgw's
// perf counters or the ops log cover.
type Registry struct{ /* prometheus.Registry and the collectors below */ }

// New returns a registry with the build-info gauge set from version.Read.
func New() *Registry

// InFlight and Observe implement op.Metrics.
func (r *Registry) InFlight(delta int)
func (r *Registry) Observe(opName string, status int, elapsed time.Duration, bytesIn, bytesOut int64)

// WatchRADOS registers a collector that reads the transport's counters on
// every scrape.
func (r *Registry) WatchRADOS(mode string, s radosclient.StatsReporter)

// Handler serves the registry in the Prometheus text format.
func (r *Registry) Handler() http.Handler
```

Metric names and labels, fixed here so T's benchmarks can scrape them:

| Metric | Type | Labels |
|---|---|---|
| `rgw_go_build_info` | gauge, 1 | `version`, `revision`, `go_version` |
| `rgw_go_requests_total` | counter | `op`, `status` (the three-digit class, `2xx`, `4xx`, `5xx`, to bound cardinality) |
| `rgw_go_request_duration_seconds` | histogram, buckets 1 ms to 60 s doubling | `op` |
| `rgw_go_request_bytes_total` | counter | `direction` (`in`, `out`) |
| `rgw_go_requests_in_flight` | gauge | none |
| `rgw_go_rados_ops_total` | counter, from `Stats` | `mode`, `kind` (`read`, `write`) |
| `rgw_go_rados_bytes_total` | counter, from `Stats` | `mode`, `kind` |
| `rgw_go_rados_ops_in_flight` | gauge, from `Stats` | `mode` |
| `rgw_go_rados_bytes_in_flight` | gauge, from `Stats` | `mode` |
| `rgw_go_rados_throttle_waits_total` | counter, from `Stats` | `mode` |

- [ ] **Step 1: Write the failing spec**

`metrics_test.go`: `New()`, three `Observe` calls (`list_buckets` 200 twice, `get_obj` 404 once, with bytes), `InFlight(1)`, a fake `StatsReporter` returning fixed `Stats`, `WatchRADOS("callback", fake)`, then GET the `Handler()` through `httptest` and assert the text body contains `rgw_go_requests_total{op="list_buckets",status="2xx"} 2`, `rgw_go_requests_total{op="get_obj",status="4xx"} 1`, `rgw_go_requests_in_flight 1`, `rgw_go_request_bytes_total{direction="out"} <sum>`, `rgw_go_rados_ops_total{kind="read",mode="callback"} <value>`, `rgw_go_rados_throttle_waits_total{mode="callback"} <value>`, and `rgw_go_build_info{`; and that `promhttp` returns 200 with `Content-Type` starting `text/plain`. Assert through `prometheus/testutil.GatherAndCompare` for the exact families where the text form is awkward.

- [ ] **Step 2: Run to fail, implement, commit**

```sh
go get github.com/prometheus/client_golang@v1.24.1 && go mod tidy   # sandbox disabled for the download
```

Implement with `prometheus.NewRegistry()` (not the default registry, so the Go runtime collectors are added explicitly with `collectors.NewGoCollector()` and `collectors.NewProcessCollector`: thread and RSS numbers are what §11 records). `make check`, then `feat(metrics): add the minimal Prometheus surface`.

---

### Task 8: `driver` skeleton: `Open`, `Store`, `Run`

**Files:**
- Create: `internal/driver/doc.go`, `internal/driver/store.go`, `internal/driver/run.go`, `internal/driver/driver_suite_test.go`, `internal/driver/store_test.go`

**Interfaces:**
- Consumes: `radosclient.Cluster`, `cephconf.Options`, `denc.ParseRelease`, every store interface in `op`.
- Produces:

```go
package driver

// Options tunes Open.
type Options struct {
	// Release overrides the encoding release detected from the cluster's
	// required OSD release; nil detects.
	Release *denc.Release
}

// Store is the RADOS driver: one type implementing every store interface in
// op over the seam. A method that is not implemented yet returns
// op.ErrNotImplemented.
type Store struct{ /* cluster, conf, release, zone/zonegroup names, workers */ }

// Open connects the driver to cluster: detects the release, and reads
// rgw_zone, rgw_zonegroup and rgw_realm.
func Open(ctx context.Context, cluster radosclient.Cluster, conf *cephconf.Options, o Options) (*Store, error)

// Run runs the driver's background workers under one errgroup until ctx
// ends; each subsystem registers its worker with AddWorker. It returns the
// first worker error, or nil on ctx cancellation.
func (s *Store) Run(ctx context.Context) error

// AddWorker registers a worker Run starts; it must return when ctx ends.
func (s *Store) AddWorker(name string, fn func(ctx context.Context) error)

// Env returns an op.Env whose every store is s, with authz, metrics and the
// host id left for the caller.
func (s *Store) Env() *op.Env

var (
	_ op.ZoneInfo       = (*Store)(nil)
	_ op.UserStore      = (*Store)(nil)
	_ op.BucketStore    = (*Store)(nil)
	_ op.ObjectStore    = (*Store)(nil)
	_ op.MultipartStore = (*Store)(nil)
	_ op.StatsStore     = (*Store)(nil)
	_ op.UsageLogger    = (*Store)(nil)
	_ op.MetadataStore  = (*Store)(nil)
)
```

In G every method but the zone accessors returns `op.ErrNotImplemented`, and `Open` names the zone from configuration only; phase 1 units M, R, W, P and N fill the methods in, and M replaces the name-only zone with resolution from the root pool.

- [ ] **Step 1: Write the failing specs with a fake cluster**

`store_test.go` uses a counterfeiter fake of `radosclient.Cluster` (generate it into `internal/radosclient/radosclientfakes` with a `//counterfeiter:generate . Cluster` directive added to the seam, since the driver consumes it) whose `RequiredOSDRelease` returns `"squid"` and a `cephconf.MapGetter{"rgw_zone": "ceph-objectstore", "rgw_zonegroup": "ceph-objectstore", "rgw_realm": "ceph-objectstore"}`:

```go
It("detects the release and names the zone from configuration", func(ctx SpecContext) {
	s, err := driver.Open(ctx, cluster, conf, driver.Options{})
	Expect(err).NotTo(HaveOccurred())
	Expect(s.Release()).To(Equal(denc.Squid))
	Expect(s.Zone().Name).To(Equal("ceph-objectstore"))
	Expect(s.ZoneGroup().Name).To(Equal("ceph-objectstore"))
	Expect(s.Realm().Name).To(Equal("ceph-objectstore"))
})
It("honours a release override", func(ctx SpecContext) {
	t := denc.Tentacle
	s, err := driver.Open(ctx, cluster, conf, driver.Options{Release: &t})
	Expect(err).NotTo(HaveOccurred())
	Expect(s.Release()).To(Equal(denc.Tentacle))
	Expect(cluster.RequiredOSDReleaseCallCount()).To(BeZero())
})
It("refuses a cluster below the Squid floor", func(ctx SpecContext) {
	cluster.RequiredOSDReleaseReturns("reef", nil)
	_, err := driver.Open(ctx, cluster, conf, driver.Options{})
	Expect(err).To(MatchError(radosclient.ErrReleaseTooOld))
})
It("answers NotImplemented from every store method", func(ctx SpecContext) {
	s, _ := driver.Open(ctx, cluster, conf, driver.Options{})
	_, err := s.GetBucket(ctx, "", "plain")
	Expect(err).To(MatchError(op.ErrNotImplemented))
	_, err = s.GetUserByAccessKey(ctx, "AK")
	Expect(err).To(MatchError(op.ErrNotImplemented))
	// one assertion per interface method, each MatchError(op.ErrNotImplemented)
})
It("runs workers until the context ends and stops them together", func(ctx SpecContext) {
	s, _ := driver.Open(ctx, cluster, conf, driver.Options{})
	started := make(chan struct{}, 2)
	worker := func(ctx context.Context) error { started <- struct{}{}; <-ctx.Done(); return nil }
	s.AddWorker("a", worker)
	s.AddWorker("b", worker)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- s.Run(runCtx) }()
	Eventually(started).Should(HaveLen(2))
	cancel()
	Eventually(done).Should(Receive(Succeed()))
})
It("propagates a worker failure and cancels the others", func(ctx SpecContext) {
	// worker a returns errors.New("boom") after start; worker b blocks on ctx; Run returns the boom error
})
```

- [ ] **Step 2: Run to fail, implement**

`Open`: `o.Release` or `denc.ParseRelease(cluster.RequiredOSDRelease(ctx))`, with `ok == false` mapped to `fmt.Errorf("required osd release %q: %w", name, radosclient.ErrReleaseTooOld)`; the zone, zonegroup and realm names from `conf.String`. `Run`: `errgroup.WithContext`, one goroutine per worker logging `slog.InfoContext(ctx, "worker started", slog.String("worker", name))`, `g.Wait()` returning nil when the only error is the context's. Every store method returns `op.ErrNotImplemented` (a `Placement` call returns it too).

Run `go test -tags=ceph_preview ./internal/driver/`; expected PASS.

- [ ] **Step 3: Commit**

`make check`, then `feat(driver): add the driver skeleton with release detection and the worker group`.

---

### Task 9: `cli serve` end to end `[cluster]`

**Files:**
- Create: `internal/cli/serve.go`, `internal/cli/serve_integration_test.go`
- Modify: `internal/cli/root.go` (real `serve` command with `--metrics-addr` and `--rados-completions`), `internal/cli/root_test.go`, `hack/rooket/README.md` (the serve smoke test)

**Interfaces:**
- Consumes: everything above.
- Produces: the `serve` command:

```
rgw-go serve [--metrics-addr HOST:PORT] [--rados-completions sync|callback|pipe] -- <ceph argv>
```

`--metrics-addr` (`RGW_GO_METRICS_ADDR`, default "" for no metrics listener) and `--rados-completions` (`RGW_GO_RADOS_COMPLETIONS`, default `callback`) are the two flags the spec allots; everything after `--` is ceph's argv, which `cli.Argv` supplies when the binary runs as `radosgw`.

Startup order in `serve`, from `rgw_main.cc`/`rgw_appmain.cc` and `AsioFrontend::init`:

1. `early, err := cephconf.ParseEarly(args)`; `early.Version` prints `version.String()` and returns.
2. `cluster, err := goceph.Connect(ctx, goceph.Config{Cluster: early.Cluster, Name: early.Name, ConfigFile: early.ConfFile, NoConfigFile: early.NoConfigFile, Args: early.Rest, Mode: goceph.Mode(v.GetString("rados-completions"))})`; `defer cluster.Close()`.
3. `conf := cephconf.NewOptions(cluster)`; `apis := cephconf.EnabledAPIs(conf)`; log `slog.InfoContext(ctx, "ignoring apis", slog.Any("apis", apis.Ignored))` once when non-empty.
4. `spec, others, err := frontend.ParseFrontends(conf.String("rgw_frontends"))`; log the other frontends as ignored; `spec.RequestTimeout` defaults applied by the parser.
5. `store, err := driver.Open(ctx, cluster, conf, driver.Options{})`.
6. `reg := metrics.New()`; `reg.WatchRADOS(mode, cluster.(radosclient.StatsReporter))`.
7. `env := store.Env()`; `env.Authz = op.OwnerOnly{}`; `env.Metrics = reg`; `env.HostID = op.HostID(cluster.InstanceID(), store.Zone().Name, store.ZoneGroup().Name)`.
8. `cfg := s3.Config{DNSNames: conf.List("rgw_dns_name") + store.ZoneGroup().Hostnames, RelaxedBucketNames: conf.Bool("rgw_relaxed_s3_bucket_names"), MaxConcurrent: conf.Int64("rgw_max_concurrent_requests"), TransIDSuffix: op.TransIDSuffix(cluster.InstanceID(), store.Zone().Name), ServerHeader: "Ceph Object Gateway (" + releaseName + ")"}`; `handler := s3.NewHandler(env, s3.AnonymousOnly{}, cfg)` when `apis.S3`, else a handler answering `op.ErrMethodNotAllowed` through `s3.WriteError` for every request (radosgw with no default manager answers 405).
9. `fe, err := frontend.New(spec, frontend.Deadlines(handler, spec.RequestTimeout))`; `fe.Listen()`; log the bound addresses.
10. `cephconf.DropPrivileges(conf)`.
11. `errgroup.WithContext(ctx)`: `fe.Serve`, `store.Run`, and when `--metrics-addr` is set an `http.Server{ReadHeaderTimeout: 10 * time.Second}` on it whose `http.ServeMux` serves `reg.Handler()` at `/metrics` and `net/http/pprof` at `/debug/pprof/` (`pprof.Index`, `Cmdline`, `Profile`, `Symbol`, `Trace`, registered explicitly on the mux rather than through the package's `init` on `http.DefaultServeMux`), shut down with the group. This is the one observability listener T scrapes and profiles. `slog.InfoContext(ctx, "serving", slog.Any("addrs", fe.Addrs()), slog.String("zone", ...), slog.String("release", ...))`.
12. Return the group's error; `nil` on a clean signal shutdown.

- [ ] **Step 1: Unit specs for the command surface**

`root_test.go`: `serve --rados-completions=bogus -- --id=x` fails before connecting with an error naming the mode; `serve -- -v` prints the version and exits 0 without connecting (assert with an unset `CEPH_CONF` pointing at a missing file, so a connect attempt would fail loudly); `RGW_GO_METRICS_ADDR` is read through viper (assert via a `serve -- -v` run that logs the resolved flag at debug, or by exposing `serveConfig(v)`).

- [ ] **Step 2: Integration spec `[cluster]`**

`serve_integration_test.go`, `//go:build integration`, `Label("integration")`: build the binary with `gexec.Build("github.com/jhoblitt/rgw-go/cmd/rgw-go", "-tags=ceph_preview")`, copy it to `<tmp>/radosgw` so `Argv` takes the radosgw path, pick a free port, and start it with Rook's argv shape against the rooket cluster:

```
<tmp>/radosgw --foreground --name=client.admin -c <RGW_GO_TEST_CEPH_CONF> \
  --default-log-to-stderr=true --default-err-to-stderr=true \
  '--rgw-frontends=beast endpoint=127.0.0.1:<port>' \
  --rgw-realm=<realm> --rgw-zonegroup=<zonegroup> --rgw-zone=<zone>
```

with `RGW_GO_METRICS_ADDR=127.0.0.1:<port2>` and `RGW_GO_LOG_LEVEL=debug` in the environment, the realm, zonegroup and zone names from `manifest.json` (`cephtest.ReadManifest`). Then:

- `Eventually` GET `http://127.0.0.1:<port>/` is 200 with body exactly `<?xml version="1.0" encoding="UTF-8"?><ListAllMyBucketsResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Owner><ID>anonymous</ID></Owner><Buckets></Buckets></ListAllMyBucketsResult>` and headers `x-amz-request-id` matching `^tx[0-9a-f]{21}-[0-9a-f]{10}-[0-9]+-<zone>$` and `Server: Ceph Object Gateway (squid)`. This is Rook's probe (`rgw-probe.sh` accepts 200-399) and the pipeline no-op baseline.
- GET `/plain` is 501 NotImplemented with `<BucketName>plain</BucketName>` and a `HostId` of the form `<instance>-<zone>-<zonegroup>`.
- GET `/` with `Authorization: AWS4-HMAC-SHA256 ...` is 501.
- `POST /` is 405.
- GET `http://127.0.0.1:<port2>/metrics` contains `rgw_go_requests_total{op="list_buckets",status="2xx"}` and `rgw_go_rados_ops_in_flight{mode="callback"} 0`; GET `http://127.0.0.1:<port2>/debug/pprof/heap?debug=1` is 200 and its body starts with `heap profile:`.
- A second run with `--rgw-frontends=beast ssl_endpoint=127.0.0.1:<port3> ssl_certificate=<tmp>/rgw-cert.pem` (a self-signed certificate the suite writes, cert and key in one file as Rook lays it out) answers the same 200 over `https` to a client with `InsecureSkipVerify`, which is what `rgw-probe.sh --insecure` does.
- `SIGTERM` makes the process exit 0 within `DrainTimeout` while a request started just before is answered.
- A run with `--rgw-enable-apis=swift,admin` answers 405 on `/`.

Run:

```sh
make cluster-up RELEASE=squid && make populate RELEASE=squid
make integration RELEASE=squid
```

Privilege dropping cannot be exercised as a non-root user; the spec runs `serve` without `--setuser`, and the root path is verified when T-c's derived image runs under Rook (the container is root and Rook passes `--setuser=ceph --setgroup=ceph`). State this gap in the PR description.

- [ ] **Step 3: Implement `serve.go`, wire `main`, document the smoke test**

`main.go` becomes `cli.Run(ctx, cli.Argv(os.Args), os.Stdin, os.Stdout, os.Stderr)`. Add a "Running rgw-go against the cluster" section to `hack/rooket/README.md` with the argv above and the expected 200. Run the unit and integration suites, then `make check`.

- [ ] **Step 4: Commit**

`feat(cli): serve under radosgw's argv: connect, dispatch, answer the probe` and `docs(rooket): document running rgw-go against the cluster`. The PR description states the privilege-drop gap and that every op but ListBuckets answers 501 until the wave-1 units land.

---

## Frozen interface contract

Everything below is exported by G and imported by A, Z, M, R, W, P, N or T. The
names and signatures are normative: a wave-1 plan builds against them
verbatim. A later unit may add methods to a store interface (regenerating the
fakes and extending `memstore`) but never rename or remove one. Where this
appendix and a task body differ, the appendix wins.

### `internal/op`

```go
package op

// Scopes.
type Scope uint8
const (
	ScopeService Scope = iota
	ScopeBucket
	ScopeObject
	ScopeUser
	ScopeUpload
)

// Identity.
const AnonymousUserID = "anonymous"
type Identity struct {
	Anonymous bool
	User      *meta.UserInfo
	Owner     meta.Owner
	Account   *meta.AccountInfo
	SubUser   string
	Tenant    string
	AccessKey string
	OpMask    uint32
	Caps      meta.Caps
	Admin     bool
	System    bool
	Attrs     map[string][]byte // the user's stored xattrs; identity policies live here (authz decodes them)
}
func Anonymous() Identity

// Request.
type Request struct {
	ID            string
	Time          time.Time
	Method        string
	Host          string // without port, lowercased
	Path          string // decoded once
	RawPath       string
	Query         url.Values // keys containing "X-Amz-" lowercased except dashes
	RawQuery      string
	Header        http.Header
	Body          io.Reader
	ContentLength int64 // -1 when unknown
	RemoteAddr    string
	Referer       string
	TLS           bool
	Tenant        string
	Bucket        string
	Object        meta.ObjKey // Name "" for service and bucket scope; Instance from versionId
	Identity      Identity
	Env           *Env
	BucketRec     *BucketRecord // filled by Init
	ObjState      *ObjectState  // filled by Init
	Status        int           // filled by the protocol layer
	BytesIn       int64
	BytesOut      int64
}
func (r *Request) Scope() Scope

// Authentication result (auth.Verifier returns it; s3 consumes it).
type AuthResult struct {
	Identity      Identity
	Body          io.Reader // nil keeps the request body
	PayloadSHA256 string    // "UNSIGNED-PAYLOAD" when the header is absent
	Presigned     bool
}

// The signed payload forms an op takes (get_auth_data_v4's per-op-type
// completer whitelist); the protocol layer passes the dispatched route's to
// the authenticator.
type PayloadForms uint8
const (
	PayloadSigned PayloadForms = 1 << iota // a single-chunk signed body
	PayloadChunked                          // an aws-chunked body
)

// Environment.
type Env struct {
	Zone      ZoneInfo
	Users     UserStore
	Buckets   BucketStore
	Objects   ObjectStore
	Multipart MultipartStore
	Stats     StatsStore
	Usage     UsageLogger
	Metadata  MetadataStore
	Authz     Authorizer
	Conf      *cephconf.Options
	Metrics   Metrics
	Now       func() time.Time
	HostID    string
}
func (e *Env) Clock() time.Time

type Metrics interface {
	InFlight(delta int)
	Observe(opName string, status int, elapsed time.Duration, bytesIn, bytesOut int64)
}
type NopMetrics struct{}

// Errors.
type Error struct {
	Code    string
	Status  int
	Message string
	Errno   int
}
func (e *Error) Error() string
func (e *Error) Is(target error) bool         // same Code and Status
func (e *Error) WithMessage(msg string) *Error
func Errors() []*Error
func AsError(err error) *Error                 // ErrInternalError for a foreign error, ErrRequestTimedOut for a context error
func FromRADOS(err error, scope Scope) error   // wraps the mapped *Error and err
var (
	ErrPermanentRedirect, ErrWebsiteRedirect, ErrNotModified,
	ErrInvalidArgument, ErrInvalidRequest, ErrInvalidDigest, ErrBadDigest,
	ErrInvalidLocationConstraint, ErrIllegalLocationConstraint, ErrZonegroupPlacementMisconfig,
	ErrInvalidBucketName, ErrInvalidObjectName, ErrUnresolvableGrantByEmail,
	ErrInvalidPart, ErrInvalidPartOrder, ErrRequestTimeout, ErrEntityTooLarge, ErrEntityTooSmall,
	ErrTooManyBuckets, ErrMalformedXML, ErrContentSHA256Mismatch, ErrMalformedPolicy, ErrInvalidTag,
	ErrMalformedACL, ErrInvalidCORSRules, ErrInvalidWebsiteRoutingRules, ErrInvalidEncryptionAlgorithm,
	ErrInvalidRetentionPeriod, ErrInvalidSecretKey, ErrInvalidKeyType, ErrInvalidCapability,
	ErrInvalidTenantName,
	ErrAccessDenied, ErrMFARequired, ErrAuthorization, ErrSignatureDoesNotMatch, ErrInvalidAccessKeyID,
	ErrUserSuspended, ErrRequestTimeTooSkewed, ErrQuotaExceeded, ErrInvalidObjectState,
	ErrNoSuchKey, ErrNoSuchBucket, ErrNoSuchWebsiteConfiguration, ErrNoSuchUpload, ErrNotFound,
	ErrNoSuchLifecycleConfiguration, ErrNoSuchBucketPolicy, ErrNoSuchUser, ErrNoRoleFound, ErrNoCORSFound,
	ErrNoSuchSubUser, ErrNoSuchEntity, ErrNoSuchCORSConfiguration, ErrNoSuchObjectLockConfig,
	ErrNoSuchTagSet, ErrNoSuchBucketEncryption, ErrNoSuchPublicAccessBlock,
	ErrMethodNotAllowed, ErrRequestTimedOut,
	ErrBucketAlreadyExists, ErrUserAlreadyExists, ErrEmailExists, ErrKeyExists, ErrTagConflict,
	ErrPositionNotEqualToLength, ErrObjectNotAppendable, ErrInvalidBucketState, ErrBucketNotEmpty,
	ErrLimitExceeded, ErrAccountAlreadyExists, ErrRestoreAlreadyInProgress, ErrConcurrentModification,
	ErrMissingContentLength, ErrPreconditionFailed, ErrInvalidRange, ErrUnprocessableEntity, ErrLocked,
	ErrInternalError, ErrUnknown, ErrNotImplemented, ErrServiceUnavailable, ErrSlowDown,
	ErrInsufficientCapacity *Error
)
// The codes are radosgw's rgw_http_s3_errors.

// Op lifecycle.
const (
	OpTypeRead   uint32 = 0x01
	OpTypeWrite  uint32 = 0x02
	OpTypeDelete uint32 = 0x04
	OpTypeModify uint32 = OpTypeWrite | OpTypeDelete
	OpTypeAll    uint32 = OpTypeRead | OpTypeWrite | OpTypeDelete
)
type Op interface {
	Name() string
	Action() policy.Action
	OpMask() uint32
	Init(ctx context.Context, r *Request) error
	VerifyPermission(ctx context.Context, r *Request) error
	Execute(ctx context.Context, r *Request) error
	Complete(ctx context.Context, r *Request)
}
func Run(ctx context.Context, o Op, r *Request) error // Init, op mask, VerifyPermission (admin override), Execute, Complete

type Sink interface {
	WriteHeader(status int, h http.Header)
	io.Writer
	Flush() error
}

// Authorization (authz.Evaluator implements it; OwnerOnly is the interim stub).
type Authorizer interface {
	VerifyUser(ctx context.Context, r *Request, a policy.Action) error
	VerifyBucket(ctx context.Context, r *Request, a policy.Action, perm acl.Permission) error
	VerifyObject(ctx context.Context, r *Request, a policy.Action, perm acl.Permission) error
	// Explicit-resource forms: the request's bucket keeps the
	// public-access block and requester-pays; bucket/key or bucket/obj supply ACL, policy, ARN.
	VerifyBucketIn(ctx context.Context, r *Request, a policy.Action, perm acl.Permission, bucket *BucketRecord, key meta.ObjKey) error
	VerifyObjectIn(ctx context.Context, r *Request, a policy.Action, perm acl.Permission, bucket *BucketRecord, obj *ObjectState) error
}
func VerifyUserPermission(ctx context.Context, r *Request, a policy.Action) error
func VerifyBucketPermission(ctx context.Context, r *Request, a policy.Action, perm acl.Permission) error
func VerifyObjectPermission(ctx context.Context, r *Request, a policy.Action, perm acl.Permission) error
func VerifyBucketPermissionIn(ctx context.Context, r *Request, a policy.Action, perm acl.Permission, bucket *BucketRecord, key meta.ObjKey) error
func VerifyObjectPermissionIn(ctx context.Context, r *Request, a policy.Action, perm acl.Permission, bucket *BucketRecord, obj *ObjectState) error
type OwnerOnly struct{} // implements all five; the In forms check the explicit bucket's owner

// Transaction and host ids.
func TransID(seq uint64, now time.Time, suffix string) string      // "tx%021x-%010x%s"
func TransIDSuffix(instanceID uint64, zone string) string          // url-encoded "-<instance>-<zone>"
func URLEncode(s string, encodeSlash bool) string                 // radosgw's url_encode over char_needs_url_encoding; the one URL encoder (the encoding-type=url listings use it)
func HostID(instanceID uint64, zone, zonegroup string) string      // "<instance>-<zone>-<zonegroup>"

// Stores (counterfeiter fakes in internal/op/opfakes: FakeZoneInfo, FakeUserStore,
// FakeBucketStore, FakeObjectStore, FakeMultipartStore, FakeStatsStore,
// FakeUsageLogger, FakeMetadataStore, FakeAuthorizer, FakeMetrics).
type Placement struct {
	Rule          meta.PlacementRule
	DataPool      meta.Pool
	IndexPool     meta.Pool
	DataExtraPool meta.Pool
	Compression   string
	InlineData    bool
}
type ZoneInfo interface {
	Release() denc.Release
	Zone() meta.Zone
	ZoneGroup() meta.ZoneGroup
	ZoneParams() meta.ZoneParams
	Realm() meta.Realm
	Period() meta.Period
	Placement(rule meta.PlacementRule) (Placement, error)
}

type UserRecord struct {
	Info    meta.UserInfo
	Attrs   map[string][]byte
	Version meta.ObjVersion
	Mtime   time.Time
}
type PutUserOptions struct {
	Exclusive bool
	IfVersion *meta.ObjVersion
}
type UserStore interface {
	GetUser(ctx context.Context, id meta.UserID) (*UserRecord, error)
	GetUserByAccessKey(ctx context.Context, key string) (*UserRecord, error)
	GetUserByEmail(ctx context.Context, email string) (*UserRecord, error)
	PutUser(ctx context.Context, rec *UserRecord, opts PutUserOptions) error
	RemoveUser(ctx context.Context, rec *UserRecord) error
	ListUserBuckets(ctx context.Context, owner meta.Owner, marker string, max int) (ents []meta.BucketEnt, next string, more bool, err error)
}

type BucketRecord struct {
	EntryPoint meta.BucketEntryPoint
	Info       meta.BucketInfo
	Attrs      map[string][]byte
	Version    meta.ObjVersion
	EPVersion  meta.ObjVersion
	Mtime      time.Time
}
type CreateBucketParams struct {
	Tenant, Name string
	Owner        meta.Owner
	Zonegroup    string
	Placement    meta.PlacementRule
	Attrs        map[string][]byte
	Quota        meta.Quota
	Exclusive    bool
}
type ListObjectsParams struct {
	Prefix       string
	Delimiter    string
	Marker       string
	MaxKeys      int
	NS           string
	ListVersions bool
}
type ObjectEntry struct {
	Key              meta.ObjKey
	Size             uint64
	Mtime            time.Time
	ETag             string
	Owner            meta.Owner
	OwnerDisplayName string
	StorageClass     string
	IsLatest         bool
	DeleteMarker     bool
	Exists           bool
}
type ListObjectsResult struct {
	Entries        []ObjectEntry
	CommonPrefixes []string
	Truncated      bool
	NextMarker     string
}
type BucketStore interface {
	GetBucket(ctx context.Context, tenant, name string) (*BucketRecord, error)
	GetBucketInstance(ctx context.Context, id meta.BucketID) (*BucketRecord, error)
	CreateBucket(ctx context.Context, p CreateBucketParams) (*BucketRecord, error)
	DeleteBucket(ctx context.Context, rec *BucketRecord) error
	PutBucketInfo(ctx context.Context, rec *BucketRecord) error
	PutBucketAttrs(ctx context.Context, rec *BucketRecord, set map[string][]byte, rm []string) error
	ListObjects(ctx context.Context, rec *BucketRecord, p ListObjectsParams) (ListObjectsResult, error)
}

type ObjectState struct {
	Bucket       *BucketRecord
	Key          meta.ObjKey
	Exists       bool
	Size         uint64
	Mtime        time.Time
	Epoch        uint64
	Attrs        map[string][]byte
	Manifest     *meta.Manifest
	Compression  *meta.CompressionInfo
	ETag         string
	WriteTag     string
	StorageClass string
	ContentType  string
}
type ByteRange struct{ Offset, Length uint64 }
type PutParams struct {
	Attrs                map[string][]byte
	Size                 int64
	Mtime                time.Time
	StorageClass         string
	IfMatch, IfNoneMatch string
	ETag                 string
}
type PutResult struct {
	ETag    string
	Size    uint64
	Mtime   time.Time
	Epoch   uint64
	Version string
}
type DeleteParams struct {
	IfMatch string
	Mtime   time.Time
}
type CopyParams struct {
	Attrs                map[string][]byte
	ReplaceAttrs         bool
	Mtime                time.Time
	StorageClass         string
	IfMatch, IfNoneMatch string
}
type ObjectStore interface {
	StatObject(ctx context.Context, rec *BucketRecord, key meta.ObjKey) (*ObjectState, error) // Exists false, no error, for a missing key
	ReadObject(ctx context.Context, st *ObjectState, rng ByteRange, sink io.Writer) error
	PutObject(ctx context.Context, rec *BucketRecord, key meta.ObjKey, body io.Reader, p PutParams) (*PutResult, error)
	DeleteObject(ctx context.Context, rec *BucketRecord, key meta.ObjKey, p DeleteParams) error // ErrNoSuchKey for a missing key
	CopyObject(ctx context.Context, src *ObjectState, dst *BucketRecord, dstKey meta.ObjKey, p CopyParams) (*PutResult, error)
	SetObjectAttrs(ctx context.Context, st *ObjectState, set map[string][]byte, rm []string) error
}

type Upload struct {
	ID        string
	Bucket    *BucketRecord
	Key       meta.ObjKey
	Owner     meta.Owner
	OwnerName string
	Initiated time.Time
	Placement meta.PlacementRule
	Attrs     map[string][]byte
}
type UploadParams struct {
	Owner     meta.Owner
	OwnerName string
	Placement meta.PlacementRule
	Attrs     map[string][]byte
}
type Part struct {
	Number int
	ETag   string
	Size   uint64
	Mtime  time.Time
}
type PartResult struct {
	ETag  string
	Size  uint64
	Mtime time.Time
}
type ListPartsResult struct {
	Parts      []Part
	NextMarker int
	Truncated  bool
}
type ListUploadsParams struct {
	Prefix, Delimiter         string
	KeyMarker, UploadIDMarker string
	MaxUploads                int
}
type ListUploadsResult struct {
	Uploads            []Upload
	CommonPrefixes     []string
	NextKeyMarker      string
	NextUploadIDMarker string
	Truncated          bool
}
type CompletePart struct {
	Number int
	ETag   string
}
type MultipartStore interface {
	CreateUpload(ctx context.Context, rec *BucketRecord, key meta.ObjKey, p UploadParams) (*Upload, error)
	GetUpload(ctx context.Context, rec *BucketRecord, key meta.ObjKey, uploadID string) (*Upload, error)
	PutPart(ctx context.Context, up *Upload, n int, body io.Reader, p PutParams) (*PartResult, error)
	CopyPart(ctx context.Context, up *Upload, n int, src *ObjectState, rng ByteRange) (*PartResult, error)
	ListParts(ctx context.Context, up *Upload, marker, max int) (ListPartsResult, error)
	ListUploads(ctx context.Context, rec *BucketRecord, p ListUploadsParams) (ListUploadsResult, error)
	Complete(ctx context.Context, up *Upload, parts []CompletePart) (*PutResult, error)
	Abort(ctx context.Context, up *Upload) error
}

type Stats struct {
	Size        uint64
	SizeRounded uint64
	NumObjects  uint64
}
type StatsStore interface {
	BucketStats(ctx context.Context, rec *BucketRecord) (Stats, error)
	UserStats(ctx context.Context, owner meta.Owner) (Stats, error)
	CheckQuota(ctx context.Context, rec *BucketRecord, owner meta.Owner, addBytes, addObjs int64) error // ErrQuotaExceeded
}

type UsageEntry struct {
	Owner         meta.Owner
	Bucket        string
	Time          time.Time
	Category      string
	BytesSent     uint64
	BytesReceived uint64
	Ops           uint64
	SuccessfulOps uint64
}
type UsageLogger interface {
	Log(ctx context.Context, e UsageEntry)
}

type MetadataEntry struct {
	Key     string
	Data    json.RawMessage
	Version meta.ObjVersion
	Mtime   time.Time
}
type PutMetadataOptions struct {
	IfVersion *meta.ObjVersion
}
type MetadataStore interface {
	Get(ctx context.Context, section, key string) (MetadataEntry, error)
	Put(ctx context.Context, section, key string, e MetadataEntry, opts PutMetadataOptions) error
	Remove(ctx context.Context, section, key string) error
	List(ctx context.Context, section, marker string, max int) (keys []string, next string, more bool, err error)
}

// ListBuckets is RGWListBuckets over UserStore.ListUserBuckets; Execute hands
// each page to Page after calling Begin once, as radosgw streams the listing.
type ListBuckets struct {
	Marker string
	Limit  int // -1 for no limit
	Begin  func() error
	Page   func([]meta.BucketEnt) error
}
```

Status and Errno per sentinel are in Task 1 Step 3.

### `internal/policy` (G freezes the type; Z adds `Policy`, `Parse`, `Eval`, `Effect`, `Env`, `Principal`, `ARN` and the iam/sts/sns actions)

```go
package policy

type Action uint16
const ( S3GetObject Action = iota /* ... in rgw::IAM::action_t order ... */ S3All )
func (a Action) String() string                 // "s3:GetObject", "s3:*" for S3All
func ParseAction(s string) (a Action, ok bool)
```

### `internal/cephconf`

```go
package cephconf

type Getter interface{ ConfigGet(name string) (string, error) } // radosclient.Cluster satisfies it
type MapGetter map[string]string
var ErrUnknownOption error
type Options struct{ /* g Getter */ }
func NewOptions(g Getter) *Options
func (o *Options) String(name string) (string, error)
func (o *Options) Int64(name string) (int64, error)
func (o *Options) Uint64(name string) (uint64, error)
func (o *Options) Bool(name string) (bool, error)
func (o *Options) Size(name string) (uint64, error)
func (o *Options) Seconds(name string) (time.Duration, error)
func (o *Options) Millis(name string) (time.Duration, error)
func (o *Options) List(name string) ([]string, error)

type EarlyArgs struct {
	Cluster, Name, ConfFile string
	NoConfigFile, Version   bool
	Rest                    []string
}
func ParseEarly(argv []string) (EarlyArgs, error)
type APIs struct {
	S3, Admin bool
	Ignored   []string
}
func EnabledAPIs(o *Options) (APIs, error)
func DropPrivileges(o *Options) error // after connect and after every listener is bound
```

Option values as librados renders them: booleans `true`/`false`; `size`, `secs`, `millisecs`, `int` and `uint` as bare decimal counts; strings as stored; a name librados does not know is `ErrUnknownOption`. The rgw options G reads (and every unit may read through `Env.Conf`): `rgw_frontends`, `rgw_enable_apis`, `rgw_dns_name`, `rgw_relaxed_s3_bucket_names`, `rgw_max_concurrent_requests`, `rgw_list_buckets_max_chunk`, `rgw_zone`, `rgw_zonegroup`, `rgw_realm`, `setuser`, `setgroup`, `setuser_match_path`, `objecter_inflight_ops`, `objecter_inflight_op_bytes`.

### `internal/frontend`

```go
package frontend

const DefaultRequestTimeout = 65 * time.Second
const DefaultMaxHeaderSize = 16384
const MaxHeaderSizeCap = 65536
const DrainTimeout = 30 * time.Second
var ErrNotBeast error

type Spec struct {
	Ports, SSLPorts               []int
	Endpoints, SSLEndpoints       []string
	SSLCertificate, SSLPrivateKey string
	SSLOptions                    []string
	SSLCiphers                    []string
	TCPNoDelay                    *bool
	RequestTimeout                time.Duration
	MaxConnectionBacklog          int
	MaxHeaderSize                 int
	Unknown                       map[string][]string
}
func ParseBeast(entry string) (Spec, error)
func ParseFrontends(value string) (spec Spec, others []string, err error)

type Server struct{ /* ... */ }
func New(spec Spec, h http.Handler) (*Server, error)
func (s *Server) Listen() error   // binds every port, privileged ones included, before privileges drop
func (s *Server) Addrs() []net.Addr
func (s *Server) Serve(ctx context.Context) error
func TLSConfig(spec Spec) (*tls.Config, error)
func Deadlines(h http.Handler, timeout time.Duration) http.Handler
```

### `internal/s3`

```go
package s3

type Config struct {
	DNSNames           []string
	RelaxedBucketNames bool
	MaxConcurrent      int
	TransIDSuffix      string
	ServerHeader       string
}
type Parsed struct {
	Req            *op.Request
	ExplicitTenant bool
}
func ParseRequest(req *http.Request, cfg Config, now time.Time) (Parsed, error)
type Route struct {
	Name     string
	Scope    op.Scope
	Payloads op.PayloadForms // the op's signed payload forms at the release Dispatch ran for
}
func Dispatch(r *op.Request, rel denc.Release) (Route, error)

type Authenticator interface {
	Authenticate(ctx context.Context, r *http.Request, payloads op.PayloadForms) (*op.AuthResult, error)
}
type AuthenticatorFunc func(ctx context.Context, r *http.Request, payloads op.PayloadForms) (*op.AuthResult, error)
type AnonymousOnly struct{}

type HandlerFunc func(ctx context.Context, w http.ResponseWriter, r *op.Request) error
type Handler struct{ /* ... */ }
func NewHandler(env *op.Env, auth Authenticator, cfg Config) *Handler
func (h *Handler) Register(name string, fn HandlerFunc)
func (h *Handler) InFlight() int64
func (h *Handler) ServeHTTP(w http.ResponseWriter, req *http.Request)
func WriteError(ctx context.Context, w http.ResponseWriter, r *op.Request, err error)
func WriteXML(w http.ResponseWriter, r *op.Request, status int, v any)
func SetCommonHeaders(w http.ResponseWriter, r *op.Request)
func ISO8601(t time.Time) string // "2006-01-02T15:04:05.000Z"

func SetContentLength(h http.Header, n uint64) // dump_content_length: Content-Length and Accept-Ranges: bytes

// Unexported, for the handlers other units add to package s3:
const xmlHeader = `<?xml version="1.0" encoding="UTF-8"?>`
func sinkOf(w http.ResponseWriter) op.Sink // the op.Sink adapter over a route's writer
type chunkedXML struct{ /* ... */ }
func startChunkedXML(w http.ResponseWriter, r *op.Request) (*chunkedXML, error)
func (c *chunkedXML) add(s string)
func (c *chunkedXML) encode(v any) error
func (c *chunkedXML) flush() error
func writeChunkedXML(ctx context.Context, w http.ResponseWriter, r *op.Request, v any)
```

Route names are radosgw's op names, listed in Task 3's table (six are G's own where radosgw's `RGWOp::name()` differs; Task 3 lists them). `Route.Payloads` comes from Task 3's payload table; `s3.Handler` passes it to the authenticator, and any other protocol layer that authenticates through an `s3.Authenticator` (the admin handler) passes its own route's forms: `op.PayloadSigned` where radosgw's op type is in the single-chunk whitelist, otherwise none. A unit registers its handlers by editing the map returned by `bucketHandlers()` (`internal/s3/bucket.go`, unit M), `objectHandlers()` (`internal/s3/object.go`, units R and W) or `multipartHandlers()` (`internal/s3/multipart.go`, unit P), one file per unit so parallel work does not conflict; `NewHandler` merges them with `serviceHandlers()`. The wrapped `http.ResponseWriter` a `HandlerFunc` receives records `r.Status` and `r.BytesOut`, adds no header of its own, and forwards `Flush` and `Unwrap`; it is not an `op.Sink` (the two `WriteHeader` methods cannot share a type), so a route that hands a streaming op a sink passes `sinkOf(w)`. `Accept-Ranges: bytes` goes out only where radosgw's `dump_content_length` runs: `WriteError` sends it with every error document, and a route whose radosgw op names its length (GET and HEAD of an object, the PUT of an object or part without a copy source) sets the length through `SetContentLength`; `WriteXML` and every other success send none. A response radosgw sends with chunked transfer encoding goes through `startChunkedXML` (streamed pieces) or `writeChunkedXML` (one document): no `Content-Length`, no `Accept-Ranges`, headers flushed first; on a HEAD, `Content-Length: 0` and no body. Every string an S3 document carries is an `xmltext.Text`, and markup a route writes by hand (`chunkedXML.add`, a pre-rendered document) escapes its text with `xmltext.Escape`, so every document escapes as radosgw's `XMLFormatter` does.

### `internal/xmltext`

```go
package xmltext

func Escape(s string) string // xml_stream_escaper, src/common/escape.cc:134-169
type Text string             // element text encoding/xml writes through Escape
func (t Text) MarshalXML(e *xml.Encoder, start xml.StartElement) error
```

A leaf: it imports only `encoding/xml` and `strings`, so `meta`, `acl`, `tags`, `s3` and N's `formatter` may all use it.

### `internal/memstore`

```go
package memstore

type Config struct {
	Release   denc.Release
	Zone      meta.Zone
	ZoneGroup meta.ZoneGroup
	Params    meta.ZoneParams
	Realm     meta.Realm
	Period    meta.Period
	Now       func() time.Time
}
type Store struct{ /* ... */ } // implements every store interface in op
func New(cfg Config) *Store
func (s *Store) AddUser(info meta.UserInfo) *op.UserRecord
func (s *Store) Usage() []op.UsageEntry
```

### `internal/driver`

```go
package driver

type Options struct{ Release *denc.Release }
type Store struct{ /* ... */ } // implements every store interface in op
func Open(ctx context.Context, cluster radosclient.Cluster, conf *cephconf.Options, o Options) (*Store, error)
func (s *Store) Run(ctx context.Context) error
func (s *Store) AddWorker(name string, fn func(ctx context.Context) error)
func (s *Store) Env() *op.Env
```

Unit M replaces `Open`'s name-only zone with root-pool resolution and fills the user, bucket, listing, stats and usage methods; R fills `StatObject` and `ReadObject`; W fills `PutObject`, `DeleteObject`, `CopyObject`, `SetObjectAttrs`; P fills `MultipartStore`; N fills `MetadataStore`. Each adds its workers with `AddWorker`.

### `internal/metrics`

```go
package metrics

type Registry struct{ /* ... */ } // implements op.Metrics
func New() *Registry
func (r *Registry) InFlight(delta int)
func (r *Registry) Observe(opName string, status int, elapsed time.Duration, bytesIn, bytesOut int64)
func (r *Registry) WatchRADOS(mode string, s radosclient.StatsReporter)
func (r *Registry) Handler() http.Handler
```

Metric names are Task 7's table.

### `internal/radosclient` additions (the seam)

```go
package radosclient

// Cluster gains InstanceID() uint64.
type Stats struct {
	ReadOps, WriteOps     uint64
	ReadBytes, WriteBytes uint64
	InflightOps           int64
	InflightBytes         int64
	ThrottleWaits         uint64
}
type StatsReporter interface{ Stats() Stats }
```

```go
package goceph

// Config gains NoConfigFile bool, MaxInflightOps int, MaxInflightBytes int64.
```

The seam gaps other units own (Task 2's table): `Cluster.FSID` (N), `OpFlagFullTry` (W), `ListObjectsFrom` (N), `cls/lock` (P). None needs a fork PR.

### CLI and observability surface (what T builds against)

- **Entry point.** `rgw-go serve [--metrics-addr HOST:PORT] [--rados-completions sync|callback|pipe] -- <ceph argv>`. When the binary is invoked as `radosgw` (Rook's launch, or a copy named `radosgw`), `cli.Argv` rewrites `os.Args[1:]` into `serve -- <args>`, so `radosgw --foreground --id=rgw.a '--rgw-frontends=beast port=8080' ...` is the same invocation. Everything after `--` is ceph's argv: `cephconf.ParseEarly` takes `-c/--conf`, `--cluster`, `-i/--id/--user`, `-n/--name`, `--no-config-file`, `-v/--version`, and librados parses the rest (`--foreground`, `--mon-host`, `--keyring`, `--setuser`, `--setgroup`, every `--rgw-*` and `--default-*` option).
- **Startup order.** Connect to RADOS as the launching user, bind every frontend listener (privileged ports included), then drop to `setuser`/`setgroup`, then serve. That is radosgw's order: [`rgw_main.cc:100-102`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_main.cc#L100-L102) sets `CINIT_FLAG_DEFER_DROP_PRIVILEGES` so `global_init` skips the drop, `init_storage` connects at [`:143`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_main.cc#L143) before `init_frontends2` at [`:165`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_main.cc#L165), and `AsioFrontend::init` binds and then calls `drop_privileges` ([`rgw_asio_frontend.cc:566-590`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_asio_frontend.cc#L566-L590), [`:756`](https://github.com/ceph/ceph/blob/7ed73efc1beb034b76e150439972959857600920/src/rgw/rgw_asio_frontend.cc#L756)). The spec's §5 sentence "privileges drop before connecting" is being corrected to this.
- **Completion mode.** `--rados-completions` (`RGW_GO_RADOS_COMPLETIONS`), values `sync`, `callback`, `pipe`, default `callback`; it is `goceph.Mode` and is the `mode` label on every `rgw_go_rados_*` metric.
- **Observability listener.** `--metrics-addr` (`RGW_GO_METRICS_ADDR`, default empty for none) serves, on one plain-HTTP listener, `/metrics` (Prometheus text format, Task 7's families plus the Go runtime and process collectors) and `/debug/pprof/` (`net/http/pprof`: `/debug/pprof/profile`, `/heap`, `/goroutine`, `/trace` and the rest). GC tracing is `GODEBUG=gctrace=1` in the environment, not a flag.
- **Pipeline baseline.** There is no `--baseline-handler` flag: §5 fixes rgw-go's own flags to the five above, and the two baseline handlers §11 names are ordinary S3 requests. The **no-op handler** is an anonymous `GET /`: zero RADOS operations, radosgw's own anonymous ListBuckets short-circuit (`rgw_op.cc`, "skipping list_buckets() for anonymous user"), a 200 with the empty `ListAllMyBucketsResult`, available from G. The **one-round-trip handler** is a signed `GET /<bucket>/<key>` of an object held within its 4 MiB head: one composed RADOS read (stat, xattrs, data), available when R lands and gated there. T-a and T-d drive those two request shapes against the real binary.
- **Rook probe.** `rgw-probe.sh` curls `/` anonymously (`--insecure` under TLS) and accepts 200-399 and 503; the no-op handler above is that path, and `rgw_max_concurrent_requests` overflow answers 503 SlowDown.

---

## Self-review

- **Spec coverage.** §5 in full: radosgw argv and early arguments (Task 6), the config bridge and mon config store (Tasks 1, 9), `rgw_enable_apis` with swift ignored in one log line (Tasks 6, 9), own flags limited to five (Task 9), the beast keys with unknown ones logged (Tasks 1, 5), logging (canon), privileges dropped after connect and bind (Tasks 5, 6, 9). §6's transaction id, dispatcher, lifecycle order and per-request deadlines: Tasks 1, 3, 4, 5. §7's in-flight limiter under the objecter throttle and `rgw_max_concurrent_requests` as a hard cap: Tasks 2, 4; drain on shutdown: Task 5. §4's no path router and zero server timeouts: Tasks 3, 5; the store interfaces at the consumer with counterfeiter fakes: Task 1; minimal Prometheus (D9): Task 7; transport-owned limiter (D8): Task 2; no pool creation (D4): the driver never creates one, stated in Task 8's doc.go. §9's "TLS through the beast spec": Task 5; the pipeline baseline no-op handler: Task 4; Rook's probe: Task 9. §2's priorities: the limiter and deadlines keep threads flat under load, and the handler table adds no allocation beyond the request itself.
- **Type consistency.** `op.Request.Object` is `meta.ObjKey` everywhere; `WriteError` takes `ctx` first in Task 4, the appendix and Task 9; `frontend.ParseFrontends` returns `(Spec, []string, error)` in Tasks 1, 5 and 9; `s3.Config` has the same five fields in Tasks 3, 4, 9 and the appendix; `radosclient.StatsReporter` is what Tasks 7 and 9 assert; `goceph.Config.NoConfigFile` is used by Task 9 as Task 2 defines it; `op.ListBuckets.Limit` is `-1` for unlimited in Task 4's op, handler and appendix, and `Begin`/`Page` are what `listBuckets` sets and the op spec records; `sinkOf(w)` is the only way a route hands an op an `op.Sink` (R Task 7's `getObject`), and `startChunkedXML`/`writeChunkedXML` are what `listBuckets`, M Task 8's listings, P Task 9's two listings and W Task 11's `deleteObjects` call; `s3.Authenticator.Authenticate` and `AuthenticatorFunc` take `(ctx, req, op.PayloadForms)` in Task 4, its specs and the appendix, and `Dispatch(r, rel)` fills `Route.Payloads` from Task 3's payload table, which Task 4's lifecycle step 5 passes on; `op.PayloadForms` is declared once, in Task 1's `identity.go`; `xmltext.Text` is the type of every string field in an S3 document and `xmltext.Escape` the escaper for hand-written markup, in Task 4, its specs, the appendix and the units that render documents; `SetContentLength(h, n uint64)` is the one place besides `WriteError` that sets `Accept-Ranges`, called by R's GET and HEAD and by W's and P's PUT.
- **Review Focus.** Line 1 (Host forms) is Task 3's Host table; line 2 (NUL and once-decoding) is Task 3's rejection and path tables; line 3 (stalled client) is Task 5's deadline specs; line 4 (SlowDown) is Task 4's concurrency spec; line 5 (signed request under the stub) is Task 4's `never serves a signed request as anonymous` table; line 6 (escaping) is Task 4's `xmltext` specs, its WriteXML and error-message specs and the ListBuckets display-name spec; line 7 (`Accept-Ranges`) is Task 4's response-writer specs, the error-document spec and the ListBuckets HEAD and framing specs.
- **Placeholder scan.** No TBD, no "similar to", no "add error handling"; the one place a body is elided ("one assertion per interface method") names exactly what to write and the expected values.
- **Cluster tasks.** Task 2 Step 4 and Task 9 Step 2 need the rooket Squid cluster; everything else runs on fakes and `memstore`. The privilege-drop root path is the one behaviour no task verifies; Task 9's PR description says so.
