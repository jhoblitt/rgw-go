# Phase 1 Unit A: Authentication Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every S3 request resolves to an `op.Identity` or a radosgw-shaped error: AWS SigV4 in the header, in the query string and presigned; aws-chunked payloads with signed chunks and with signed or unsigned trailers, verified as the bytes stream; SigV2 in the header and the query string; anonymous; and radosgw's own quirks, above all a missing `x-amz-content-sha256` meaning `UNSIGNED-PAYLOAD`.

**Architecture:** One new package, `internal/auth`, implements `s3.Authenticator` through `auth.Verifier`. It transcribes radosgw's `rgw::auth::s3` engines: flavour discovery (`discover_aws_flavour`), the v4 and v2 abstractors (`AWSGeneralAbstractor::get_auth_data_v4/v2`), the local engine (`LocalEngine::authenticate`, credentials through `op.UserStore.GetUserByAccessKey`), the local applier (`LocalApplier`, which fixes what `op.Identity` carries) and the two completers (`AWSv4ComplSingle`, `AWSv4ComplMulti`) as `io.Reader` wrappers returned in `op.AuthResult.Body`. Verification of a payload happens as the op reads; the failure is reported by the `Read` that would otherwise return `io.EOF`. The package imports `op`, `meta`, `cephconf` and the standard library only; `s3` never imports `auth` (the `cli` wires them). Two small, additive contract changes carry what radosgw keeps in `req_state`: `op.AuthResult.ContentLength` (the aws-chunked decoded length) and `op.AccountStore` (the account an account user belongs to).

**Tech Stack:** Go 1.27; `crypto/hmac`, `crypto/sha256`, `crypto/sha1`, `crypto/subtle`, `encoding/hex`, `encoding/base64`, `bufio`; Ginkgo v2 and Gomega; counterfeiter fakes from `internal/op/opfakes`; `internal/memstore`; `github.com/aws/aws-sdk-go-v2` (`aws/signer/v4`, `service/s3`, `credentials`) as an independent test client; rooket disposable clusters for the gate.

**Spec:** `docs/superpowers/specs/2026-09-25-rgw-go-design.md` (§6 data path: "auth resolves the credential through the metadata cache and, for chunked uploads, wraps the body in a reader that verifies each chunk signature as bytes stream"; §8 coexistence and radosgw's signature quirks; §9 phase 1: "SigV4 header, query and presigned with chunked and trailer payloads, SigV2, anonymous"; §2 priorities), read with unit A's scope in `docs/superpowers/plans/2026-09-29-phase-1/00-index.md` ("The nine units"), G's frozen interface contract in `docs/superpowers/plans/2026-09-29-phase-1/G-gateway-core.md` (built against verbatim), Z's contract additions and finding (d) in `docs/superpowers/plans/2026-09-29-phase-1/Z-authorization.md`, M's decisions in `docs/superpowers/plans/2026-09-29-phase-1/M-metadata-plane.md`, `docs/exclusions.md` ("Signature quirks are radosgw's, not AWS's"; Keystone, LDAP and Swift excluded; STS phase 3) and `docs/ceph-upstream-bugs.md`. Every radosgw claim below is verified against ceph `v19.2.6` (Squid floor) and `v20.2.4` (Tentacle floor, cited as `[T]`); file:line references without a tag are v19.2.6.

## Global Constraints

- Module `github.com/jhoblitt/rgw-go`; `go 1.27` in `go.mod`, minor only, no `toolchain` line; every `go` command carries `-tags=ceph_preview` (the Makefile's `GO_TAGS`); `make check` green before a task is called done.
- Layout per the Go canon: everything under `internal/`; package names one lowercase word; no `util`, `common` or `helpers`. `auth` imports `op`, `meta`, `cephconf` and the standard library; it never imports `s3`, `driver`, `memstore` or `radosclient` (its specs may import `memstore`, `s3` and `opfakes`).
- Tests are Ginkgo v2 and Gomega only, one `auth_suite_test.go` with `RandomizeAllSpecs` and `FailOnPending`; test doubles are `internal/op/opfakes` (counterfeiter) and `internal/memstore`; cluster specs carry `//go:build integration` and `Label("integration")` and are marked `[cluster]`.
- Logging is `log/slog`, JSON to stderr, static lowercase messages, snake_case keys, the `…Context` variants; **never a secret key, a signature, a signing key, a string-to-sign or a canonical request at any level** (radosgw logs them at 10+ through `crypt_sanitize`; rgw-go logs the access key id and the failure class only).
- Errors are `op.Error` values from G's table, wrapped with `%w`, lowercase, unpunctuated; `errors.Is`/`errors.As` only. Every signature comparison is `crypto/subtle.ConstantTimeCompare`.
- Byte-compatibility with radosgw is the acceptance test: canonical strings, error codes and accept/reject decisions are transcribed from the ceph sources cited in each task, not designed from the AWS documentation. Where radosgw and AWS differ, radosgw wins (`docs/exclusions.md`, "Signature quirks are radosgw's, not AWS's").
- Ceph floor: 19.2.6+ on Squid, 20.2.4+ on Tentacle; no handling for earlier point releases. What `auth` itself decides is the same on both releases (Decision D-A8); the one release difference it meets, the payload whitelist's four Tentacle ops, arrives as the dispatched route's payload forms from G's release-gated dispatch table (D-A1), so nothing in `auth` is gated on `denc.Release`.
- The verifier never allocates in proportion to a client-declared length: chunk sizes and the decoded content length are counters, a chunk header line is capped at 101 bytes (radosgw's `ChunkMeta::META_MAX_SIZE`, [`rgw_auth_s3.h:315-316`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_s3.h#L315-L316)) and the trailer section at 1024 bytes (D-A7: radosgw's own 256-byte trailer buffer truncates silently, [`rgw_auth_s3.cc:1589-1613`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_s3.cc#L1589-L1613)), and data passes through to the caller's buffer.
- Never touch the ambient Kubernetes or Ceph cluster. The one cluster task uses the rooket clusters through T's harness (`make cluster-up RELEASE=squid|tentacle`, `make s3tests ...`) and is marked `[cluster]`.
- The librados headers on this machine are absent: cgo builds use `CGO_CFLAGS=-I/home/jhoblitt/github/ceph/src/include` and `CGO_LDFLAGS=-L$HOME/.local/lib/rgw-go`; module downloads need the sandbox disabled.
- Commits are Conventional Commits with the two repository trailers; each task ends in a draft PR assigned to the author, CI watched, merged on green with a merge commit under the standing authorization. Additions to `docs/ceph-upstream-bugs.md` follow that file's entry format (kind, evidence at a named tag, releases, rgw-go's handling, upstream links). Every behaviour that differs from radosgw is recorded in `docs/exclusions.md` by the task that introduces it, in the same PR (Task 6 records the two in D-A7), and the change is reported so the rgw-rs session can be told.

## Review Focus

1. **A signed request without `x-amz-content-sha256`** (go-ceph's rgw/admin, hence Rook's operator). It must authenticate with `UNSIGNED-PAYLOAD` in the canonical request and no payload verification, on both releases ([`rgw_auth_s3.h:638-661`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_s3.h#L638-L661)). Pinned in Task 5 (`payload_test.go`, "absent header is UNSIGNED-PAYLOAD") and Task 9 (the go-ceph-style signed request through `Verifier`).
2. **A presigned URL used through a port-forward**, where the client signed `host:port` but the request arrives with a bare `Host`, or the reverse. radosgw accepts either through its boto2 fallback strategy ([`rgw_auth_registry.h:42-43`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_registry.h#L42-L43), [`rgw_auth_s3.cc:770-783`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_s3.cc#L770-L783)). Pinned in Task 4 ("accepts a presigned URL signed with host:port").
3. **A hostile aws-chunked body**: a 16-hex-digit chunk size, a size of `-1` or seventeen hex digits (which radosgw's `strtoull` wraps to 2^64-1, tracker #81123), a chunk header without `;chunk-signature=`, a missing CRLF, a trailer section longer than 1024 bytes, a stream that ends mid-chunk. Memory stays O(1) and the answer is 400, 403 or 409 (or `io.ErrUnexpectedEOF` to the op), never a hang, an allocation sized by the client or a desynchronised frame. Pinned in Task 6 (the malformed-size tables and the 1024/1025-byte bound) and Task 7 (the 290-byte signed SHA512 trailer).
4. **A signed request carrying an unsigned `x-amz-*` header or an unsigned `host`** (CVE-2026-54330, [`rgw_auth_s3.cc:788-820`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_s3.cc#L788-L820)). It is 403 AccessDenied unless `rgw_sigv4_insecure` is set. Pinned in Task 3 ("rejects an unsigned x-amz header").
5. **An op that reads exactly `ContentLength` bytes and never asks for EOF.** The last chunk signature, the trailer signature and the single-chunk hash are verified by the `Read` that returns EOF; an op that stops early commits an unverified body. Pinned in Tasks 5 and 6 (the "reports the failure on the EOF read" specs) and in `op.AuthResult`'s doc comment; W and P are told in the final report.
6. **A signed body on an op that takes none, or an aws-chunked body on anything but `put_obj`.** radosgw answers 501 NotImplemented from inside authentication, after the empty-payload rule and before it looks the access key up or compares the signature (`get_auth_data_v4`, [`rgw_rest_s3.cc:5899-5969`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L5899-L5969), [`[T]:6466-6540`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L6466-L6540)), per op type and per release (Tentacle adds the three bucket-logging ops and `restore_obj`). A check placed after the key lookup answers 403 where radosgw answers 501; a check that ignores the release refuses Tentacle's additions or accepts them on Squid. Pinned in Task 5 (`acceptedBy`) and Task 9 (the verifier table and the per-release handler table).

## Decisions this plan fixes

- **D-A1. The payload forms an op accepts are radosgw's, per op and per release.** radosgw dispatches before it authenticates (`get_op`, then `s->op_type`, then `verify_requester`: [`rgw_process.cc:325`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_process.cc#L325), [`:341`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_process.cc#L341), [`:345`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_process.cc#L345)), and its SigV4 abstractor picks the payload completer by that op type (`get_auth_data_v4`): a signed single-chunk payload (Task 5's `payloadSingle`: a non-empty body whose `x-amz-content-sha256` is not an unsigned form without a trailer and does not start with `STREAMING-`) is accepted only for a whitelist of 33 op types at v19.2.6 and 37 at v20.2.4, which adds `RGW_OP_GET_BUCKET_LOGGING`, `RGW_OP_PUT_BUCKET_LOGGING`, `RGW_OP_POST_BUCKET_LOGGING` and `RGW_OP_RESTORE_OBJ`; an aws-chunked payload (`STREAMING-…`, the unsigned-chunked form included) only for `RGW_OP_PUT_OBJ` (PutObject, UploadPart and UploadPartCopy) on both; anything else throws `ERR_NOT_IMPLEMENTED`, 501 NotImplemented ([`rgw_rest_s3.cc:5899-5969`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L5899-L5969), [`[T]:6466-6540`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L6466-L6540); [`rgw_common.cc:132`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L132), [`[T]:133`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L133)). The throw comes after the presigned-URL switch ([`:5607-5610`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5607-L5610)), credential parsing ([`:5783-5794`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5783-L5794)), the canonical-header checks ([`:5797-5805`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5797-L5805)) and the empty-payload rule ([`:5860-5866`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5860-L5866)), and before the empty-key check, the key lookup and the signature compare (`AWSEngine::authenticate`, [`:6179-6180`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L6179-L6180); `LocalEngine::authenticate`, [`:6325-6375`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L6325-L6375)); an empty body, `UNSIGNED-PAYLOAD`, an absent header, SigV2 and anonymous requests never reach it ([`:5879-5891`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L5879-L5891)). rgw-go reproduces it: G's dispatch table carries each route's forms per release (G Task 3's payload table, mapped from each route's `RGWOp::get_type()`), `s3.Authenticator.Authenticate` takes the dispatched route's `op.PayloadForms`, and `Verifier.Authenticate` answers `op.ErrNotImplemented` right after `authDataV4` returns (Task 5's `acceptedBy`, called in Task 9), which is radosgw's point in the flow. The switch's two STS labels are dead in radosgw (`is_non_s3_op` returns first, [`rgw_auth_s3.cc:472-473`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_s3.cc#L472-L473)), and phase 1 serves no STS, IAM or SNS op. Not a difference from radosgw, so no `docs/exclusions.md` entry.
- **D-A2. Verification is reported at EOF.** `op.AuthResult.Body` yields the decoded payload and returns the verification error (`op.ErrContentSHA256Mismatch`, `op.ErrSignatureDoesNotMatch`, `op.ErrLimitExceeded`, `op.ErrInvalidArgument`) instead of `io.EOF` from the read that ends the payload; the error is sticky. A truncated stream is `io.ErrUnexpectedEOF`. An op that writes a body must read it to EOF before it completes (memstore's `PutObject` already does; W's and P's drivers must). radosgw does the same thing explicitly with `do_aws4_auth_completion` after the body ([`rgw_op.cc:1360-1381`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L1360-L1381), [`:4439`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L4439)).
- **D-A3. `op.AuthResult` gains `ContentLength int64`.** `AWSv4ComplMulti::modify_request_state` replaces `s->content_length` with `x-amz-decoded-content-length` ([`rgw_auth_s3.cc:1532-1548`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_s3.cc#L1532-L1548)); G's handler copies `req.ContentLength` and has no way to learn the decoded length. The field is additive; `s3.Handler` applies it together with `Body` (Task 1).
- **D-A4. `op.AccountStore` is added, with `GetAccount` and `AccountName`.** `LocalEngine` loads the account of an account user before granting ([`rgw_auth.cc:135-155`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth.cc#L135-L155), [`rgw_rest_s3.cc:6339-6345`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L6339-L6345)) and M and Z read `Identity.Account`; G's contract has no account store. `op.AccountStore{GetAccount, AccountName}`, `op.AccountRecord`, `op.Env.Accounts` and a counterfeiter fake are added; `memstore` implements it with `AddAccount`; `driver` answers `op.ErrNotImplemented` until unit N fills it. `AccountName(ctx, id)` is `GetAccount(ctx, id).Info.Name` — the three lines N-D5 described — so that `op.AccountStore` satisfies Z's `authz.AccountLookup` from wave 1 and M, W and P can write `authz.UserResolver{Users: r.Env.Users, Accounts: r.Env.Accounts}` without waiting for N. An account user whose account cannot be loaded is `op.ErrAccessDenied` (radosgw's EPERM, [`:6343-6345`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L6343-L6345)), logged at error level, so until N lands account users get 403 on the real driver; ordinary users are unaffected. Reported to the caller.
- **D-A5. An inactive access key is `InvalidAccessKeyId`.** radosgw never checks `active` at authentication ([`rgw_rest_s3.cc:6347-6352`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L6347-L6352)); it writes a `users.keys` index only for active keys and removes it on deactivation ([`svc_user_rados.cc:259`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_user_rados.cc#L259), [`:425`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_user_rados.cc#L425)), so an inactive key fails the index lookup with `ERR_INVALID_ACCESS_KEY`. rgw-go checks `key.Active` after the lookup and answers the same error, which makes the behaviour independent of how a store indexes keys.
- **D-A6. The boto2 fallback is unconditional**, as it is in radosgw ([`rgw_auth_registry.h:28-49`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_registry.h#L28-L49), both tags; the `rgw_s3_auth_aws4_force_boto2_compat` option no longer exists in `rgw.yaml.in` at either tag). For a presigned v4 request whose `host` is signed and whose listener port is not the scheme default, a failed signature is retried with `host:port` (`SERVER_PORT_SECURE` under TLS, else `SERVER_PORT`) in the canonical headers; the error reported is the first attempt's ([`rgw_auth.cc:396-397`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth.cc#L396-L397)).
- **D-A7. radosgw quirks mirrored, two defects not.** Mirrored: a missing `x-amz-content-sha256` is `UNSIGNED-PAYLOAD`; the credential scope's date, region and service are never checked against the request, the zonegroup or `s3` (only used to derive the signing key, [`rgw_auth_s3.cc:1009-1033`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_s3.cc#L1009-L1033)); a signed header absent from the request is skipped, not rejected ([`:755-759`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_s3.cc#L755-L759)); a duplicate query key keeps arrival order in the canonical query string instead of sorting by value (`std::multimap`, [`:620-642`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_s3.cc#L620-L642)); a duplicate request header contributes its last value (`RGWEnv::set`, [`rgw_env.cc:22-25`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_env.cc#L22-L25)); `rgw_s3_auth_disable_signature_url` denies every signed request, header-signed included ([`rgw_rest_s3.cc:5607-5610`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L5607-L5610)); the declared signature of the final zero-length chunk is never compared ([`rgw_auth_s3.cc:1645-1655`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_s3.cc#L1645-L1655)); only a query key containing the exact spelling `X-Amz-` is lowercased ([`rgw_common.cc:870-878`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L870-L878)); `Expires` and `x-amz-expires` are read with `atoll` semantics. Not mirrored, two defects, each a difference from radosgw that Task 6 records in `docs/exclusions.md`'s coexistence section: (1) **the trailer bound.** radosgw reads the trailer section into a 256-byte buffer but caps each read at `256 - pos - 1` bytes ([`rgw_auth_s3.cc:1596-1599`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_s3.cc#L1596-L1599), [`[T]:1573-1576`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L1573-L1576)), so its size check (`:1608-1613`, [`[T]:1585-1590`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L1585-L1590)), which would throw `ERR_LIMIT_EXCEEDED`, 409 LimitExceeded ([`rgw_common.cc:86`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L86), [`[T]:87`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L87)), never fires and a longer section is truncated silently. Measured on both releases, counting from the CRLF that ends the last data chunk through the closing CRLF as the buffer counts: a signed section of up to 257 bytes passes (only the closing CRLF is lost) and a longer one fails with 403 SignatureDoesNotMatch, the 290-byte signed SHA512 trailer included; on Tentacle an unsigned section whose checksum line falls past the cut is accepted with the client's checksum never compared (tracker #81122). rgw-go bounds the section at 1024 bytes, counted the same way, and answers 409 LimitExceeded above it; the largest legitimate section, a signed SHA512 trailer, is 290 bytes. (2) **strict hex chunk sizes.** A chunk size is one to sixteen hex digits and nothing else; anything else is `op.ErrInvalidArgument` (EINVAL) before a byte of the chunk is delivered. radosgw parses it with an unchecked `strtoull` ([`rgw_auth_s3.cc:1127-1132`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_s3.cc#L1127-L1132), [`[T]:1104-1109`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L1104-L1109)) that takes leading blanks, a sign, a `0x` prefix and trailing bytes and saturates overlong values, so `-1` and seventeen hex digits both become 2^64-1: measured on both releases, an unsigned multi-chunk upload whose first chunk carries such a size is stored corrupted with 200, a signed multi-chunk one fails with 400 XAmzContentSHA256Mismatch and a single-chunk one is stored intact (tracker #81123). Each mirrored defect goes into `docs/ceph-upstream-bugs.md` in Task 10, which also appends rgw-go's handling to the #81122 and #81123 entries main carries.
- **D-A8. No release gate in this unit.** Diffing `rgw_auth_s3.cc/.h`, `rgw_auth.cc`, `rgw_auth_filters.h`, `rgw_auth_registry.h` and the auth parts of `rgw_rest_s3.cc` between v19.2.6 and v20.2.4 shows no change to flavour discovery, credential parsing, canonicalization, signing, skew, expiry, payload classification, chunk or trailer verification, SigV2, anonymous or the error mapping. The differences are: (a) `[T]` substitutes the CORS `Access-Control-Request-Method` for the method in the SigV2 string-to-sign too (`get_canonical_method`, phase 2, CORS); (b) `[T]` adds `rgwx-perm-check-uid` impersonation for system users (`rest_s3.diff @@ -6374,9 +6945,46 @@`, radosgw's own multisite client, excluded); (c) `[T]` adds three OIDC ops to `is_non_s3_op` (phase 3, IAM); (d) `[T]` adds `RGW_OP_RESTORE_OBJ` and the three bucket-logging op types to the single-chunk payload whitelist ([`rgw_rest_s3.cc [T]:6494`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L6494), [`:6508-6510`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L6508-L6510)), which reaches `auth` as the dispatched route's payload forms from G's release-gated table (D-A1). `Identity.Admin = admin || system` is what both releases' permission override checks ([`rgw_process.cc:228-236`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_process.cc#L228-L236) with `is_admin_of` = `admin || system`, [`rgw_auth.cc:1048-1051`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth.cc#L1048-L1051); `[T]` `is_admin()` alone). `auth.Config` therefore carries no `denc.Release` and no `Region` (the interface sketch G's contract replaced had a region radosgw never uses; `docs/superpowers/plans/2026-09-29-phase-1/00-index.md`, "Shared interfaces and seam gaps").
- **D-A9. The SigV2 virtual-hosted resource uses `Config.DNSNames`.** `rgw_create_s3_canonical_header` signs `info.request_uri` after `RGWREST::preprocess` prepended `/<bucket>` for a virtual-hosted request ([`rgw_rest.cc:2154-2161`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L2154-L2161), [`rgw_auth_s3.cc:249-258`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_s3.cc#L249-L258)). The verifier sees only the `*http.Request`, so it applies the same host rule (`rgw_find_host_in_domains` [`:260-287`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L260-L287), the CNAME fallback [`:2136-2143`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L2136-L2143)) from `rgw_dns_name` plus the zonegroup hostnames that `cli` also gives `s3.Config.DNSNames`. The rule is ~30 lines transcribed from the same C++ as G's Task 3 and pinned by the same cases.
- **D-A10. `rgwx-uid` is honoured for system users**, as `SysReqApplier::load_acct_info` does ([`rgw_auth_filters.h:282-317`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_filters.h#L282-L317)): the effective owner and tenant become the named user's or account's, an unknown one is 403 AccessDenied (EACCES). The other `rgwx-*` system parameters are multisite's and are not interpreted.
- **D-A11. Test vectors.** Positive SigV4 cases are signed by `aws-sdk-go-v2/aws/signer/v4` (`SignHTTP`, `PresignHTTP`, `StreamSigner`) in the specs, which makes the reference client the oracle; canonicalization edge cases, radosgw quirks, SigV2 and malformed inputs are hand-authored JSON vectors under `internal/auth/testdata/` that record the expected canonical string or string-to-sign (human-checkable) and the expected outcome; the AWS documentation's signed-streaming example (66560 bytes of `a`, three chunks) is transcribed as the signed-chunk chain vector. Vectors live as data, never as constants in `_test.go` files.

## File structure

```
internal/op/identity.go                  modify: AuthResult gains ContentLength (D-A3)
internal/op/account.go                   new: AccountRecord, AccountStore (D-A4)
internal/op/env.go                       modify: Env.Accounts
internal/op/opfakes/fake_account_store.go  generated
internal/memstore/account.go             new: AddAccount, GetAccount
internal/memstore/account_test.go
internal/driver/store.go                 modify: GetAccount stub, Env().Accounts
internal/s3/handler.go                   modify: apply res.ContentLength with res.Body
internal/s3/handler_test.go              modify: the decoded-length spec
internal/auth/doc.go
internal/auth/config.go                  Config, DefaultConfig, ConfigFrom
internal/auth/verifier.go                CredentialStore, Verifier, New, Authenticate, flavour, error mapping
internal/auth/request.go                 requestView: raw path, radosgw-style query, header lookup, port
internal/auth/encoding.go                urlDecode, aws4URIEncode, collapseSpace, atoll, parseISO8601Basic, parseRFC2616
internal/auth/sigv4.go                   v4 credentials (header and query), canonical request, signing key, signature
internal/auth/sigv2.go                   v2 credentials, canonical string, signature, host bucket rule (D-A9)
internal/auth/payload.go                 payload hash classes, empty-payload rule, hashReader (AWSv4ComplSingle)
internal/auth/chunked.go                 chunkedReader (AWSv4ComplMulti): chunks, final chunk, trailers
internal/auth/identity.go                identity from UserRecord, account, rgwx-uid (LocalApplier, SysReqApplier)
internal/auth/auth_suite_test.go
internal/auth/{encoding,request,sigv4,presign,payload,chunked,trailer,sigv2,identity,verifier}_test.go
internal/auth/vectors_test.go            loads testdata/*.json
internal/auth/testdata/v4/*.json         hand-authored SigV4 vectors
internal/auth/testdata/v2/*.json         hand-authored SigV2 vectors
internal/auth/testdata/chunked/*.json    chunk streams (AWS docs example, malformed streams)
internal/cli/serve.go                    modify: auth.New(...) replaces s3.AnonymousOnly{}
docs/exclusions.md                       modify: the trailer bound and strict chunk sizes (Task 6, D-A7)
docs/ceph-upstream-bugs.md               modify: the D-A7 entries; rgw-go's handling on the #81122 and #81123 entries (Task 10)
```

## Task index

| # | Task | Depends on | Cluster |
|---|---|---|---|
| 1 | Contract additions: `AuthResult.ContentLength`, `op.AccountStore`, memstore accounts, driver stub, handler applies the length | G Tasks 1, 4, 8 merged | no |
| 2 | `auth` skeleton: request view, radosgw encodings, time parsing, flavour discovery, `Config` | 1 | no |
| 3 | SigV4 header authentication: credentials, canonical request, signing key, signature, skew, the CVE-2026-54330 header checks | 2 | no |
| 4 | SigV4 presigned (query-string) authentication and the boto2 `host:port` fallback | 3 | no |
| 5 | Payload classes, the empty-payload rule, the per-op payload forms, the single-chunk hash reader, the decoded length | 3 | no |
| 6 | aws-chunked reader: signed chunks, the final chunk, strict chunk sizes and the 1024-byte trailer bound | 5 | no |
| 7 | aws-chunked trailers: `…-PAYLOAD-TRAILER` and `STREAMING-UNSIGNED-PAYLOAD-TRAILER`, the signed SHA512 trailer | 6 | no |
| 8 | SigV2 header and query-string authentication | 2 | no |
| 9 | Identity resolution and `Verifier.Authenticate` end to end, through `s3.Handler` on `memstore` | 3, 4, 5, 7, 8 | no |
| 10 | `cli serve` wiring, registry entries, the s3-tests auth groups on both releases | 9, G Task 9, T Task 4 | `[cluster]` |

Task 1 touches G's packages and is the gate for the rest; Tasks 2 to 8 build the pure functions and readers and are independent of any store; Task 9 assembles them; Task 10 is the only task that needs a cluster.

---

### Task 1: Contract additions: `AuthResult.ContentLength`, `op.AccountStore`, memstore accounts, driver stub, handler applies the length

**Files:**
- Modify: `internal/op/identity.go` (`AuthResult`), `internal/op/env.go` (`Env.Accounts`), `internal/op/doc.go` (or wherever G put the `//counterfeiter:generate` directives)
- Create: `internal/op/account.go`, `internal/memstore/account.go`, `internal/memstore/account_test.go`
- Modify: `internal/driver/store.go` (`GetAccount`, `Env().Accounts`), `internal/s3/handler.go` (step 5 of the lifecycle), `internal/s3/handler_test.go`
- Generated: `internal/op/opfakes/fake_account_store.go`

**Interfaces:**
- Consumes: G's `op.AuthResult`, `op.Env`, `op.UserRecord`, `memstore.Store` (its mutex, clock and version-tag helper as G Task 1 implemented them), `driver.Store.Env()`, `s3.Handler.ServeHTTP` step 5 ("Copy the identity into `r.Identity`; when `res.Body != nil`, `r.Body = res.Body`").
- Produces:

```go
package op

// AuthResult is what authentication resolves a request to.
type AuthResult struct {
	Identity Identity
	// Body, when non-nil, replaces the request body: the payload after
	// aws-chunked decoding, verified as it is read. The Read that ends the
	// payload returns the verification error instead of io.EOF (an
	// XAmzContentSHA256Mismatch, SignatureDoesNotMatch, LimitExceeded or
	// InvalidArgument *Error; a truncated stream is io.ErrUnexpectedEOF), so
	// an op that stores a body reads it to EOF before it completes.
	Body io.Reader
	// ContentLength is the length of Body when Body is non-nil: the
	// x-amz-decoded-content-length of an aws-chunked payload, otherwise the
	// request's own Content-Length; -1 when unknown. Ignored when Body is nil.
	ContentLength int64
	// PayloadSHA256 is the x-amz-content-sha256 value, "UNSIGNED-PAYLOAD" when
	// the header is absent, as radosgw takes it (rgw_auth_s3.h:638-661).
	PayloadSHA256 string
	// Presigned is set when the credentials came in the query string.
	Presigned bool
}

// AccountRecord is an account as the metadata store returns it:
// RGWAccountInfo with the object's xattrs and version.
type AccountRecord struct {
	Info    meta.AccountInfo
	Attrs   map[string][]byte
	Version meta.ObjVersion
	Mtime   time.Time
}

//counterfeiter:generate . AccountStore

// AccountStore reads accounts, rgw::sal::Driver::load_account_by_id.
type AccountStore interface {
	// GetAccount returns the account with id, or ErrNoSuchEntity.
	GetAccount(ctx context.Context, id string) (*AccountRecord, error)
	// AccountName is GetAccount(ctx, id).Info.Name: what an ACL owner or
	// grantee naming an account resolves to (authz.AccountLookup).
	AccountName(ctx context.Context, id string) (string, error)
}

// Env gains:
//	Accounts AccountStore
```

```go
package memstore

// AddAccount stores info with a fresh version, for test setup.
func (s *Store) AddAccount(info meta.AccountInfo) *op.AccountRecord
// GetAccount implements op.AccountStore; a missing id is op.ErrNoSuchEntity.
func (s *Store) GetAccount(ctx context.Context, id string) (*op.AccountRecord, error)
// AccountName implements op.AccountStore and authz.AccountLookup.
func (s *Store) AccountName(ctx context.Context, id string) (string, error)
```

`driver.Store.GetAccount` and `AccountName` return `op.ErrNotImplemented` until unit N; `Env()` sets `Accounts: s`. `s3.Handler` step 5 becomes: `r.Identity = res.Identity; if res.Body != nil { r.Body = res.Body; r.ContentLength = res.ContentLength }`.

- [ ] **Step 1: Write the failing memstore account spec**

`internal/memstore/account_test.go`:

```go
var _ = Describe("accounts", func() {
	var store *memstore.Store

	BeforeEach(func() {
		store = memstore.New(memstore.Config{Release: denc.Squid})
	})

	It("returns a copy of an added account and NoSuchEntity for a missing id", func(ctx SpecContext) {
		added := store.AddAccount(meta.AccountInfo{ID: "RGW00000000000000001", Tenant: "t1", Name: "acme", MaxBuckets: 7})
		Expect(added.Version.Ver).To(BeEquivalentTo(1))

		rec, err := store.GetAccount(ctx, "RGW00000000000000001")
		Expect(err).NotTo(HaveOccurred())
		Expect(rec.Info.Name).To(Equal("acme"))
		Expect(rec.Info.MaxBuckets).To(BeEquivalentTo(7))
		rec.Info.Name = "mutated"
		again, err := store.GetAccount(ctx, "RGW00000000000000001")
		Expect(err).NotTo(HaveOccurred())
		Expect(again.Info.Name).To(Equal("acme"), "GetAccount must return a copy")
		name, err := store.AccountName(ctx, "RGW00000000000000001")
		Expect(err).NotTo(HaveOccurred())
		Expect(name).To(Equal("acme"), "authz.AccountLookup through op.AccountStore")

		_, err = store.GetAccount(ctx, "RGW00000000000000002")
		Expect(err).To(MatchError(op.ErrNoSuchEntity))
		_, err = store.AccountName(ctx, "RGW00000000000000002")
		Expect(err).To(MatchError(op.ErrNoSuchEntity))
	})
})
```

- [ ] **Step 2: Write the failing handler spec**

Append to `internal/s3/handler_test.go`, beside G's "applies the authenticator's identity" spec and using G's `newHandler(store, auth, cfg)` helper and `alice` fixture:

```go
	It("gives the op the authenticator's body and content length", func() {
		alice := store.AddUser(meta.UserInfo{UserID: meta.UserID{ID: "alice"}, OpMask: op.OpTypeAll})
		as := s3.AuthenticatorFunc(func(context.Context, *http.Request, op.PayloadForms) (*op.AuthResult, error) {
			return &op.AuthResult{
				Identity:      op.Identity{User: &alice.Info, Owner: meta.UserOwner(alice.Info.UserID), OpMask: op.OpTypeAll},
				Body:          strings.NewReader("hello"),
				ContentLength: 5,
				PayloadSHA256: "STREAMING-AWS4-HMAC-SHA256-PAYLOAD",
			}, nil
		})
		h := newHandler(store, as, s3.Config{})
		var (
			gotBody []byte
			gotLen  int64
		)
		h.Register("put_obj", func(_ context.Context, w http.ResponseWriter, r *op.Request) error {
			var err error
			gotBody, err = io.ReadAll(r.Body)
			gotLen = r.ContentLength
			w.WriteHeader(http.StatusOK)
			return err
		})
		wire := "5;chunk-signature=0000000000000000000000000000000000000000000000000000000000000000\r\nhello\r\n0;chunk-signature=0000000000000000000000000000000000000000000000000000000000000000\r\n\r\n"
		req := httptest.NewRequest(http.MethodPut, "/plain/k", strings.NewReader(wire))
		req.Header.Set("X-Amz-Decoded-Content-Length", "5")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(string(gotBody)).To(Equal("hello"))
		Expect(gotLen).To(Equal(int64(5)))
	})
```

- [ ] **Step 3: Run both specs to verify they fail**

Run: `go test -tags=ceph_preview ./internal/memstore/... ./internal/s3/... -run 'TestMemstore|TestS3' 2>&1 | tail -20`
Expected: compile errors, `store.AddAccount undefined` and `unknown field ContentLength in struct literal of type op.AuthResult`.

- [ ] **Step 4: Add the `op` types and regenerate the fakes**

Create `internal/op/account.go` with the `AccountRecord`, `AccountStore` and the `//counterfeiter:generate . AccountStore` directive exactly as in Interfaces; edit `AuthResult` in `internal/op/identity.go` to the form above (keep G's field order, add `ContentLength` after `Body`); add `Accounts AccountStore` to `Env` after `Users`. Then:

```sh
make generate && git status --short internal/op/opfakes
```
Expected: `internal/op/opfakes/fake_account_store.go` appears; nothing else changes.

- [ ] **Step 5: Implement the memstore accounts**

`internal/memstore/account.go` (use the `Store`'s existing mutex, clock and version-tag helper — the same three G's `AddUser` uses; the names below assume `s.mu`, `s.now()` and `s.newTag()`, adapt to G's):

```go
package memstore

import (
	"context"
	"fmt"
	"maps"

	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

var _ op.AccountStore = (*Store)(nil)

// AddAccount stores info with a fresh version, for test setup.
func (s *Store) AddAccount(info meta.AccountInfo) *op.AccountRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := &op.AccountRecord{
		Info:    info,
		Attrs:   map[string][]byte{},
		Version: meta.ObjVersion{Ver: 1, Tag: s.newTag()},
		Mtime:   s.now(),
	}
	s.accounts[info.ID] = rec
	return copyAccount(rec)
}

// GetAccount implements op.AccountStore.
func (s *Store) GetAccount(_ context.Context, id string) (*op.AccountRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.accounts[id]
	if !ok {
		return nil, fmt.Errorf("%w: account %s", op.ErrNoSuchEntity, id)
	}
	return copyAccount(rec), nil
}

// AccountName implements op.AccountStore and authz.AccountLookup.
func (s *Store) AccountName(ctx context.Context, id string) (string, error) {
	rec, err := s.GetAccount(ctx, id)
	if err != nil {
		return "", err
	}
	return rec.Info.Name, nil
}

func copyAccount(rec *op.AccountRecord) *op.AccountRecord {
	out := *rec
	out.Attrs = maps.Clone(rec.Attrs)
	return &out
}
```

Add `accounts map[string]*op.AccountRecord` to `Store` and initialise it in `New`.

- [ ] **Step 6: Stub the driver and apply the length in the handler**

`internal/driver/store.go`: add

```go
var _ op.AccountStore = (*Store)(nil)

// GetAccount and AccountName are not implemented yet: every account lookup is
// NotImplemented, which auth reports as AccessDenied for account users and
// authz.UserResolver reports as an unresolvable account grantee.
func (s *Store) GetAccount(context.Context, string) (*op.AccountRecord, error) {
	return nil, op.ErrNotImplemented
}

func (s *Store) AccountName(context.Context, string) (string, error) {
	return "", op.ErrNotImplemented
}
```

and `Accounts: s` in the `op.Env` literal `Env()` returns. `internal/s3/handler.go`, step 5 of `ServeHTTP`:

```go
	r.Identity = res.Identity
	if res.Body != nil {
		r.Body = res.Body
		r.ContentLength = res.ContentLength
	}
```

If G's `r.BytesIn` counting wrapper is applied to `r.Body` after this point it counts decoded bytes; if it wraps `req.Body` before `Authenticate` it counts wire bytes. Either is acceptable; do not move it.

- [ ] **Step 7: Run the specs and the gate**

Run: `go test -tags=ceph_preview ./internal/op/... ./internal/memstore/... ./internal/driver/... ./internal/s3/...` then `make generate-check && make check`
Expected: PASS; no stale fake; lint clean.

- [ ] **Step 8: Commit**

```sh
git add internal/op internal/memstore internal/driver internal/s3
git commit -m "feat(op): add AccountStore and the decoded content length to AuthResult"
```

Open the draft PR `feat: auth contract additions (unit A task 1)`; Tasks 2 to 8 do not depend on its merge, Task 9 does.

---

### Task 2: `auth` skeleton: request view, radosgw encodings, time parsing, flavour discovery, `Config`

**Files:**
- Create: `internal/auth/doc.go`, `internal/auth/config.go`, `internal/auth/request.go`, `internal/auth/encoding.go`, `internal/auth/auth_suite_test.go`, `internal/auth/config_test.go`, `internal/auth/request_test.go`, `internal/auth/encoding_test.go`

**Interfaces:**
- Consumes: `cephconf.Options` (`Bool`, `ErrUnknownOption`), `net/http`.
- Produces (unexported helpers are named here because Tasks 3 to 9 call them):

```go
package auth

// Config is what the verifier reads from Ceph configuration and the zonegroup.
type Config struct {
	// UseRados is rgw_s3_auth_use_rados (default true). With it false, and
	// Keystone and LDAP excluded, radosgw has no backend and denies every
	// request, anonymous included (RGW_Auth_S3::authorize, rgw_rest_s3.cc:5119-5125).
	UseRados bool
	// DisablePresignedURLs is rgw_s3_auth_disable_signature_url (default
	// false). radosgw applies it to every signed request, header-signed
	// included (rgw_rest_s3.cc:5607-5610), though rgw.yaml.in:911-916
	// documents it as presigned-only; rgw-go mirrors radosgw.
	DisablePresignedURLs bool
	// Insecure is rgw_sigv4_insecure (default false): skips the checks that
	// host, a present content-type and every x-amz-* header are signed
	// (CVE-2026-54330, rgw_auth_s3.cc:788-820).
	Insecure bool
	// DNSNames is rgw_dns_name plus the zonegroup hostnames, the list
	// s3.Config.DNSNames also gets: the SigV2 canonical resource starts with
	// the bucket a virtual-hosted request names in its Host, as
	// RGWREST::preprocess prepends it (rgw_rest.cc:2154-2161).
	DNSNames []string
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// DefaultConfig is radosgw's defaults: UseRados true, everything else off.
func DefaultConfig() Config
// ConfigFrom reads the three options through o; an option librados does not
// know keeps its default. dnsNames is passed through.
func ConfigFrom(o *cephconf.Options, dnsNames []string) (Config, error)

// flavour is discover_aws_flavour (rgw_rest_s3.cc:5071-5105).
type version uint8
const ( versionUnknown version = iota; versionV2; versionV4 )
type route uint8
const ( routeQuery route = iota; routeHeaders )
func discoverFlavour(rv *requestView) (version, route)

// requestView is the part of req_info the engines read.
type requestView struct {
	req      *http.Request
	rawPath  string            // REQUEST_URI before '?', absolute-form reduced (req_info ctor, rgw_common.cc:232-245)
	rawQuery string            // request_params
	params   map[string]string // RGWHTTPArgs::val_map: url_decode(+ as space), first '=', X-Amz- keys lowercased except dashes, last wins (rgw_common.cc:848-893)
	sysParams map[string]string // RGWHTTPArgs::sys_val_map: the rgwx-* keys
	subres   map[string]string // RGWHTTPArgs::sub_resources (rgw_common.cc:926-975)
	order    []string          // param names in arrival order (for the admin sub-resource rule)
}
func newRequestView(req *http.Request) *requestView
func (rv *requestView) param(name string) (string, bool)        // RGWHTTPArgs::get
func (rv *requestView) header(name string) (string, bool)       // RGWEnv::get through beast's mapping: host, content-length, content-type, transfer-encoding special-cased; duplicates → last value
func (rv *requestView) hasHeader(name string) bool
func (rv *requestView) contentLength() int64                    // req.ContentLength
func (rv *requestView) chunkedTE() bool                         // len(req.TransferEncoding) > 0
func (rv *requestView) localPort() (port string, tls bool)      // SERVER_PORT / SERVER_PORT_SECURE from http.LocalAddrContextKey and req.TLS
func (rv *requestView) hostNoPort() string                      // req_info.host after RGWREST::preprocess (rgw_rest.cc:2048-2060)

// encoding.go
func urlDecode(s string, inQuery bool) string      // rgw_common.cc:1701-1734, including the empty result on a bad hex digit
func aws4URIEncode(s string, encodeSlash bool) string // rgw_auth_s3.h:555-590 with rgw_uri_escape_char's uppercase %XX
func aws4Recode(s string, encodeSlash bool) string   // aws4_uri_recode: aws4URIEncode(urlDecode(s, false), encodeSlash)
func trimSpace(s string) string                    // rgw_trim_whitespace: ASCII isspace both ends
func collapseSpace(s string) string                // boost::trim_all: trim, then runs of ASCII whitespace become one space
func isBase64Charset(s string) bool                // is_base64_for_content_md5: alnum, space, '+', '/', '='
func atoll(s string) int64                          // C atoll: leading whitespace, sign, digits, stop at the first non-digit; "" or no digits → 0
func parseISO8601Basic(s string) (time.Time, bool)  // parse_iso8601(..., extended=false): YYYYMMDDTHHMMSS then "", "Z" or ".<digits>Z" after trimming (rgw_common.cc:599-650)
func parseRFC2616(s string) (time.Time, bool)      // parse_rfc2616: RFC 850, asctime, RFC 1123 with GMT, RFC 1123 with a numeric zone (rgw_common.cc:566-597)
```

`header(name)` resolves a lower-case token to the request header the token names, which is how beast's `HTTP_` mapping composed with radosgw's bidirectional dash transform behaves (they cancel: a token matches the header whose name equals it case-insensitively; [`rgw_asio_client.cc:36-66`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_asio_client.cc#L36-L66), [`rgw_common.h:1990-2008`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.h#L1990-L2008)). `host` is `req.Host` (missing when empty); `content-length` is `req.Header["Content-Length"]` (Go keeps it); `transfer-encoding` is `strings.Join(req.TransferEncoding, ",")` when Go moved it out of the header map; every other name is `req.Header.Values(textproto.CanonicalMIMEHeaderKey(name))` with the LAST value (`RGWEnv::set` overwrites, [`rgw_env.cc:22-25`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_env.cc#L22-L25)).

- [ ] **Step 1: Write the failing encoding specs**

`internal/auth/auth_suite_test.go` is the canon bootstrap (`RandomizeAllSpecs`, `FailOnPending`, suite name `"auth suite"`). Then `internal/auth/encoding_test.go`, in package `auth` (internal test, the helpers are unexported):

```go
var _ = Describe("radosgw encodings", func() {
	DescribeTable("urlDecode is rgw_common.cc's url_decode",
		func(in string, inQuery bool, want string) { Expect(urlDecode(in, inQuery)).To(Equal(want)) },
		Entry("plain", "/a/b", false, "/a/b"),
		Entry("percent", "/a%2Fb%41", false, "/a/bA"),
		Entry("plus kept outside a query", "/a+b", false, "/a+b"),
		Entry("plus is a space in a query", "a+b", true, "a b"),
		Entry("a ? switches to query mode", "/p+q?r+s", false, "/p+q?r s"),
		Entry("a bad hex digit empties the result", "/a%zzb", false, ""),
		Entry("a truncated escape stops decoding", "/a%4", false, "/a"),
	)
	DescribeTable("aws4URIEncode is aws4_uri_encode",
		func(in string, slash bool, want string) { Expect(aws4URIEncode(in, slash)).To(Equal(want)) },
		Entry("unreserved kept", "AZaz09-_.~", true, "AZaz09-_.~"),
		Entry("slash encoded when asked", "/a/b", true, "%2Fa%2Fb"),
		Entry("slash kept for the uri", "/a/b", false, "/a/b"),
		Entry("space, plus, utf-8 are %XX uppercase", "a b+ü", true, "a%20b%2B%C3%BC"),
	)
	DescribeTable("aws4Recode decodes then encodes",
		func(in string, slash bool, want string) { Expect(aws4Recode(in, slash)).To(Equal(want)) },
		Entry("double-encoded key", "a%2Fb", true, "a%2Fb"),
		Entry("encoded slash becomes a slash in the uri", "/k%2Fv", false, "/k/v"),
		Entry("lowercase hex normalised", "%2f", true, "%2F"),
	)
	It("trims and collapses whitespace as boost::trim_all", func() {
		Expect(trimSpace(" \t a b  \r\n")).To(Equal("a b"))
		Expect(collapseSpace("  a \t  b\r\n c ")).To(Equal("a b c"))
	})
	It("checks the content-md5 charset", func() {
		Expect(isBase64Charset("rL0Y20zC+Fzt72VPzMSk2A==")).To(BeTrue())
		Expect(isBase64Charset("rL0Y20zC+Fzt72VPzMSk2A=!")).To(BeFalse())
	})
	DescribeTable("atoll",
		func(in string, want int64) { Expect(atoll(in)).To(Equal(want)) },
		Entry("digits", "604800", int64(604800)),
		Entry("trailing junk", "300abc", int64(300)),
		Entry("leading space and sign", "  -5", int64(-5)),
		Entry("no digits", "abc", int64(0)),
		Entry("empty", "", int64(0)),
	)
	DescribeTable("parseISO8601Basic is parse_iso8601 without the extended format",
		func(in string, ok bool, want time.Time) {
			t, got := parseISO8601Basic(in)
			Expect(got).To(Equal(ok))
			if ok {
				Expect(t).To(BeTemporally("==", want))
			}
		},
		Entry("with Z", "20150830T123600Z", true, time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)),
		Entry("without Z", "20150830T123600", true, time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)),
		Entry("fraction", "20150830T123600.123Z", true, time.Date(2015, 8, 30, 12, 36, 0, 123000000, time.UTC)),
		Entry("trailing space tolerated", "20150830T123600Z ", true, time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)),
		Entry("extended format rejected", "2015-08-30T12:36:00Z", false, time.Time{}),
		Entry("rfc 1123 rejected", "Sun, 30 Aug 2015 12:36:00 GMT", false, time.Time{}),
		Entry("fraction without Z rejected", "20150830T123600.123", false, time.Time{}),
	)
	DescribeTable("parseRFC2616 accepts the four HTTP date forms",
		func(in string, ok bool, want time.Time) {
			t, got := parseRFC2616(in)
			Expect(got).To(Equal(ok))
			if ok {
				Expect(t).To(BeTemporally("==", want))
			}
		},
		Entry("rfc 1123", "Sun, 30 Aug 2015 12:36:00 GMT", true, time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)),
		Entry("rfc 1123 numeric zone", "Sun, 30 Aug 2015 14:36:00 +0200", true, time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)),
		Entry("rfc 850", "Sunday, 30-Aug-15 12:36:00 GMT", true, time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)),
		Entry("asctime", "Sun Aug 30 12:36:00 2015", true, time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)),
		Entry("iso rejected", "20150830T123600Z", false, time.Time{}),
	)
})
```

- [ ] **Step 2: Write the failing request-view and flavour specs**

`internal/auth/request_test.go` (package `auth`):

```go
func rawRequest(method, target string, hdr map[string][]string) *http.Request {
	req := httptest.NewRequest(method, target, nil)
	for k, vs := range hdr {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	return req
}

var _ = Describe("requestView", func() {
	It("parses the query as RGWHTTPArgs does", func() {
		rv := newRequestView(rawRequest(http.MethodGet, "/b/k?X-Amz-Credential=AK%2F20150830%2Fus%2Fs3%2Faws4_request&acl&versionId=v1&rgwx-uid=u1&x=1&x=2&a+b=c+d&X-AMZ-Date=x", nil))
		v, ok := rv.param("x-amz-credential")
		Expect(ok).To(BeTrue())
		Expect(v).To(Equal("AK/20150830/us/s3/aws4_request"), "decoded once, key lowercased except dashes")
		_, ok = rv.param("X-Amz-Credential")
		Expect(ok).To(BeFalse())
		_, ok = rv.param("X-AMZ-Date")
		Expect(ok).To(BeTrue(), "only the exact spelling X-Amz- is lowercased (rgw_common.cc:870)")
		v, _ = rv.param("acl")
		Expect(v).To(BeEmpty())
		v, _ = rv.param("x")
		Expect(v).To(Equal("2"), "a repeated key keeps the last value")
		v, _ = rv.param("a b")
		Expect(v).To(Equal("c d"), "+ is a space in the query")
		Expect(rv.subres).To(HaveKeyWithValue("acl", ""))
		Expect(rv.subres).To(HaveKeyWithValue("versionId", "v1"))
		Expect(rv.subres).NotTo(HaveKey("x"))
		Expect(rv.sysParams).To(HaveKeyWithValue("rgwx-uid", "u1"))
		Expect(rv.params).NotTo(HaveKey("rgwx-uid"))
		Expect(rv.rawQuery).To(Equal("X-Amz-Credential=AK%2F20150830%2Fus%2Fs3%2Faws4_request&acl&versionId=v1&rgwx-uid=u1&x=1&x=2&a+b=c+d&X-AMZ-Date=x"))
	})

	It("keeps the raw path and reduces an absolute-form target", func() {
		rv := newRequestView(rawRequest(http.MethodGet, "/a%2Fb+c%41?x=1", nil))
		Expect(rv.rawPath).To(Equal("/a%2Fb+c%41"))
		abs := rawRequest(http.MethodGet, "/", nil)
		abs.RequestURI = "http://bkt.example.com:8080/k%20v?y"
		abs.Host = "bkt.example.com:8080"
		rv = newRequestView(abs)
		Expect(rv.rawPath).To(Equal("/k%20v"))
		Expect(rv.rawQuery).To(Equal("y"))
		Expect(rv.hostNoPort()).To(Equal("bkt.example.com"))
	})

	It("adds only the first admin sub-resource, with an empty value", func() {
		rv := newRequestView(rawRequest(http.MethodGet, "/b?quota=x&policy=y", nil))
		Expect(rv.subres).To(Equal(map[string]string{"quota": ""}))
	})

	It("resolves signed-header tokens the way beast's env does", func() {
		req := rawRequest(http.MethodPut, "/b/k", map[string][]string{
			"X-Amz-Date":         {"20150830T123600Z", "20150830T123700Z"},
			"x-amz-meta-foo_bar": {"v"},
			"Content-Type":       {"text/plain"},
			"Content-Length":     {"5"},
		})
		req.Host = "example.com:8080"
		rv := newRequestView(req)
		v, ok := rv.header("host")
		Expect(ok).To(BeTrue())
		Expect(v).To(Equal("example.com:8080"), "the raw Host, port included")
		v, _ = rv.header("x-amz-date")
		Expect(v).To(Equal("20150830T123700Z"), "the last of duplicate headers")
		v, ok = rv.header("x-amz-meta-foo_bar")
		Expect(ok).To(BeTrue())
		Expect(v).To(Equal("v"))
		v, _ = rv.header("content-type")
		Expect(v).To(Equal("text/plain"))
		v, _ = rv.header("content-length")
		Expect(v).To(Equal("5"))
		_, ok = rv.header("x-amz-missing")
		Expect(ok).To(BeFalse())
		Expect(rv.hostNoPort()).To(Equal("example.com"))
		Expect(rv.chunkedTE()).To(BeFalse())
	})

	It("reports a chunked transfer encoding and the empty host", func() {
		req := rawRequest(http.MethodPut, "/b/k", nil)
		req.TransferEncoding = []string{"chunked"}
		req.ContentLength = -1
		req.Host = ""
		rv := newRequestView(req)
		Expect(rv.chunkedTE()).To(BeTrue())
		v, ok := rv.header("transfer-encoding")
		Expect(ok).To(BeTrue())
		Expect(v).To(Equal("chunked"))
		_, ok = rv.header("host")
		Expect(ok).To(BeFalse())
	})

	It("strips a bracketed IPv6 literal for hostNoPort", func() {
		req := rawRequest(http.MethodGet, "/", nil)
		req.Host = "[::1]:8080"
		Expect(newRequestView(req).hostNoPort()).To(Equal("::1"))
	})

	DescribeTable("discoverFlavour is discover_aws_flavour",
		func(auth string, query string, wantV version, wantR route) {
			req := rawRequest(http.MethodGet, "/?"+query, nil)
			if auth != "" {
				req.Header.Set("Authorization", auth)
			}
			v, r := discoverFlavour(newRequestView(req))
			Expect(v).To(Equal(wantV))
			Expect(r).To(Equal(wantR))
		},
		Entry("v4 header", "AWS4-HMAC-SHA256 Credential=a/b/c/d/aws4_request, SignedHeaders=host, Signature=x", "", versionV4, routeHeaders),
		Entry("v2 header", "AWS AK:sig", "", versionV2, routeHeaders),
		Entry("unknown header scheme", "Bearer tok", "", versionUnknown, routeHeaders),
		Entry("empty header is the query route", "", "", versionUnknown, routeQuery),
		Entry("v4 query", "", "X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=x", versionV4, routeQuery),
		Entry("v2 query", "", "AWSAccessKeyId=AK&Signature=s&Expires=1", versionV2, routeQuery),
		Entry("header wins over query", "AWS AK:sig", "X-Amz-Algorithm=AWS4-HMAC-SHA256", versionV2, routeHeaders),
	)
})
```

- [ ] **Step 3: Write the failing config spec**

`internal/auth/config_test.go` (package `auth_test`), using G's `cephconf.MapGetter`:

```go
var _ = Describe("ConfigFrom", func() {
	It("reads the three options and keeps defaults for unknown ones", func() {
		o := cephconf.NewOptions(cephconf.MapGetter{
			"rgw_s3_auth_use_rados":             "false",
			"rgw_s3_auth_disable_signature_url": "true",
		})
		cfg, err := auth.ConfigFrom(o, []string{"s3.example.com"})
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg.UseRados).To(BeFalse())
		Expect(cfg.DisablePresignedURLs).To(BeTrue())
		Expect(cfg.Insecure).To(BeFalse(), "rgw_sigv4_insecure unknown to this getter keeps its default")
		Expect(cfg.DNSNames).To(Equal([]string{"s3.example.com"}))
	})

	It("has radosgw's defaults", func() {
		cfg := auth.DefaultConfig()
		Expect(cfg.UseRados).To(BeTrue())
		Expect(cfg.DisablePresignedURLs).To(BeFalse())
		Expect(cfg.Insecure).To(BeFalse())
	})
})
```

- [ ] **Step 4: Run the specs to verify they fail**

Run: `go test -tags=ceph_preview ./internal/auth/...`
Expected: compile errors for every undefined name.

- [ ] **Step 5: Implement `encoding.go`**

```go
package auth

import (
	"strings"
	"time"
)

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\v' || c == '\f' || c == '\r' }

// hexVal is HexTable::to_num: -1 for a non-hex byte.
func hexVal(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}

// urlDecode is url_decode (rgw_common.cc:1701-1734): '+' is a space only in
// query mode, which a '?' switches on; a bad hex digit empties the whole
// result; a truncated escape ends decoding.
func urlDecode(s string, inQuery bool) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '%' {
			if inQuery && c == '+' {
				b.WriteByte(' ')
				continue
			}
			if c == '?' {
				inQuery = true
			}
			b.WriteByte(c)
			continue
		}
		if len(s)-i < 3 {
			break
		}
		hi, lo := hexVal(s[i+1]), hexVal(s[i+2])
		if hi < 0 || lo < 0 {
			return ""
		}
		b.WriteByte(byte(hi<<4 | lo))
		i += 2
	}
	return b.String()
}

const upperHex = "0123456789ABCDEF"

// aws4URIEncode is aws4_uri_encode (rgw_auth_s3.h:555-590): unreserved bytes
// and, unless encodeSlash, '/' pass; every other byte is %XX in uppercase.
func aws4URIEncode(s string, encodeSlash bool) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		case c == '/' && !encodeSlash:
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(upperHex[c>>4])
			b.WriteByte(upperHex[c&0xf])
		}
	}
	return b.String()
}

// aws4Recode is aws4_uri_recode: decode, then encode.
func aws4Recode(s string, encodeSlash bool) string { return aws4URIEncode(urlDecode(s, false), encodeSlash) }

// trimSpace is rgw_trim_whitespace.
func trimSpace(s string) string {
	for len(s) > 0 && isSpace(s[0]) {
		s = s[1:]
	}
	for len(s) > 0 && isSpace(s[len(s)-1]) {
		s = s[:len(s)-1]
	}
	return s
}

// collapseSpace is boost::trim_all: trim, then each run of whitespace is one space.
func collapseSpace(s string) string {
	s = trimSpace(s)
	var b strings.Builder
	b.Grow(len(s))
	inRun := false
	for i := 0; i < len(s); i++ {
		if isSpace(s[i]) {
			if !inRun {
				b.WriteByte(' ')
				inRun = true
			}
			continue
		}
		inRun = false
		b.WriteByte(s[i])
	}
	return b.String()
}

// isBase64Charset is is_base64_for_content_md5 over every byte.
func isBase64Charset(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || isSpace(c) || c == '+' || c == '/' || c == '='
		if !ok {
			return false
		}
	}
	return true
}

// atoll is C's atoll on the value's prefix.
func atoll(s string) int64 {
	i := 0
	for i < len(s) && isSpace(s[i]) {
		i++
	}
	neg := false
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		neg = s[i] == '-'
		i++
	}
	var n int64
	for ; i < len(s) && s[i] >= '0' && s[i] <= '9'; i++ {
		n = n*10 + int64(s[i]-'0')
	}
	if neg {
		return -n
	}
	return n
}

// parseISO8601Basic is parse_iso8601 with extended_format false
// (rgw_common.cc:599-650): "%Y%m%dT%H%M%S", then after trimming nothing, "Z"
// or ".<digits>Z".
func parseISO8601Basic(s string) (time.Time, bool) {
	if len(s) < 15 {
		return time.Time{}, false
	}
	t, err := time.Parse("20060102T150405", s[:15])
	if err != nil {
		return time.Time{}, false
	}
	rest := trimSpace(s[15:])
	switch {
	case rest == "" || rest == "Z":
		return t, true
	case len(rest) >= 3 && rest[0] == '.' && rest[len(rest)-1] == 'Z':
		digits := rest[1 : len(rest)-1]
		for i := 0; i < len(digits); i++ {
			if digits[i] < '0' || digits[i] > '9' {
				return time.Time{}, false
			}
		}
		if len(digits) > 9 {
			digits = digits[:9]
		}
		ns := atoll(digits)
		for i := len(digits); i < 9; i++ {
			ns *= 10
		}
		return t.Add(time.Duration(ns)), true
	}
	return time.Time{}, false
}

// parseRFC2616 is parse_rfc2616 (rgw_common.cc:566-597): RFC 850, asctime,
// RFC 1123 with GMT, RFC 1123 with a numeric zone, in that order.
func parseRFC2616(s string) (time.Time, bool) {
	for _, layout := range []string{time.RFC850, time.ANSIC, time.RFC1123, time.RFC1123Z} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}
```

- [ ] **Step 6: Implement `request.go`**

```go
package auth

import (
	"net"
	"net/http"
	"net/textproto"
	"strings"
)

// sysParamPrefix is RGW_SYS_PARAM_PREFIX.
const sysParamPrefix = "rgwx-"

// subResources is the list RGWHTTPArgs::append files under sub_resources
// (rgw_common.cc:927-961); adminSubResources is the group of which only the
// first seen is added, with an empty value (:962-975).
var (
	subResources = map[string]bool{
		"acl": true, "cors": true, "notification": true, "location": true, "logging": true, "usage": true,
		"lifecycle": true, "delete": true, "uploads": true, "partNumber": true, "uploadId": true, "versionId": true,
		"start-date": true, "end-date": true, "versions": true, "versioning": true, "website": true,
		"requestPayment": true, "torrent": true, "tagging": true, "append": true, "position": true,
		"policyStatus": true, "publicAccessBlock": true,
		"response-content-type": true, "response-content-language": true, "response-expires": true,
		"response-cache-control": true, "response-content-disposition": true, "response-content-encoding": true,
	}
	adminSubResources = map[string]bool{
		"subuser": true, "key": true, "caps": true, "index": true, "policy": true, "quota": true,
		"list": true, "object": true, "sync": true,
	}
)

type requestView struct {
	req       *http.Request
	rawPath   string
	rawQuery  string
	params    map[string]string
	sysParams map[string]string
	subres    map[string]string
	order     []string
}

func newRequestView(req *http.Request) *requestView {
	rv := &requestView{req: req, params: map[string]string{}, sysParams: map[string]string{}, subres: map[string]string{}}
	target := req.RequestURI
	if target == "" {
		target = req.URL.RequestURI()
	}
	rv.rawPath, rv.rawQuery, _ = strings.Cut(target, "?")
	if !strings.HasPrefix(rv.rawPath, "/") { // get_abs_path, rgw_common.cc:222-230
		if i := strings.Index(rv.rawPath, "://"); i >= 0 {
			if j := strings.IndexByte(rv.rawPath[i+3:], '/'); j >= 0 {
				rv.rawPath = rv.rawPath[i+3+j:]
			}
		}
	}
	rv.parseQuery()
	return rv
}

// parseQuery is RGWHTTPArgs::parse and append (rgw_common.cc:848-975).
func (rv *requestView) parseQuery() {
	q := strings.TrimPrefix(rv.rawQuery, "?")
	if q == "" {
		return
	}
	adminAdded := false
	for _, part := range strings.Split(q, "&") {
		nameval := urlDecode(part, true)
		name, val, _ := strings.Cut(nameval, "=")
		if strings.Contains(name, "X-Amz-") {
			b := []byte(name)
			for i, c := range b {
				if c != '-' && c >= 'A' && c <= 'Z' {
					b[i] = c + 'a' - 'A'
				}
			}
			name = string(b)
		}
		rv.order = append(rv.order, name)
		if strings.HasPrefix(name, sysParamPrefix) {
			rv.sysParams[name] = val
		} else {
			rv.params[name] = val
		}
		switch {
		case subResources[name]:
			rv.subres[name] = val
		case adminSubResources[name]:
			if !adminAdded {
				rv.subres[name] = ""
				adminAdded = true
			}
		}
	}
}

func (rv *requestView) param(name string) (string, bool) {
	v, ok := rv.params[name]
	return v, ok
}

// header is RGWEnv::get for a lower-case header token: the header whose name
// equals it case-insensitively, the last value when repeated; host,
// content-length and transfer-encoding come from where net/http keeps them.
func (rv *requestView) header(name string) (string, bool) {
	switch strings.ToLower(name) {
	case "host":
		return rv.req.Host, rv.req.Host != ""
	case "transfer-encoding":
		if len(rv.req.TransferEncoding) > 0 {
			return strings.Join(rv.req.TransferEncoding, ","), true
		}
	}
	vs := rv.req.Header.Values(textproto.CanonicalMIMEHeaderKey(name))
	if len(vs) == 0 {
		return "", false
	}
	return vs[len(vs)-1], true
}

func (rv *requestView) hasHeader(name string) bool { _, ok := rv.header(name); return ok }

func (rv *requestView) contentLength() int64 { return rv.req.ContentLength }

func (rv *requestView) chunkedTE() bool { return len(rv.req.TransferEncoding) > 0 }

// localPort is SERVER_PORT, and whether SERVER_PORT_SECURE would be set.
func (rv *requestView) localPort() (string, bool) {
	addr, _ := rv.req.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if addr == nil {
		return "", rv.req.TLS != nil
	}
	_, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return "", rv.req.TLS != nil
	}
	return port, rv.req.TLS != nil
}

// hostNoPort is info.host after RGWREST::preprocess (rgw_rest.cc:2048-2060).
func (rv *requestView) hostNoPort() string {
	h := rv.req.Host
	if strings.HasPrefix(h, "[") {
		if i := strings.IndexByte(h, ']'); i >= 1 {
			return h[1:i]
		}
		return h
	}
	if i := strings.IndexByte(h, ':'); i >= 0 {
		return h[:i]
	}
	return h
}

// discoverFlavour is discover_aws_flavour (rgw_rest_s3.cc:5071-5105).
func discoverFlavour(rv *requestView) (version, route) {
	if a, ok := rv.header("authorization"); ok && a != "" {
		switch {
		case strings.HasPrefix(a, aws4Algorithm):
			return versionV4, routeHeaders
		case strings.HasPrefix(a, "AWS "):
			return versionV2, routeHeaders
		}
		return versionUnknown, routeHeaders
	}
	if alg, _ := rv.param("x-amz-algorithm"); alg == aws4Algorithm {
		return versionV4, routeQuery
	}
	if id, _ := rv.param("AWSAccessKeyId"); id != "" {
		return versionV2, routeQuery
	}
	return versionUnknown, routeQuery
}
```

`aws4Algorithm = "AWS4-HMAC-SHA256"` is declared in Task 3's `sigv4.go`; declare it in `request.go` now and leave it there. Note `httptest.NewRequest` sets `RequestURI`, so the specs exercise the same path as the server.

- [ ] **Step 7: Implement `config.go` and `doc.go`**

```go
package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/jhoblitt/rgw-go/internal/cephconf"
)

type Config struct { /* as in Interfaces, with the doc comments */ }

func DefaultConfig() Config { return Config{UseRados: true} }

func ConfigFrom(o *cephconf.Options, dnsNames []string) (Config, error) {
	cfg := DefaultConfig()
	cfg.DNSNames = dnsNames
	for _, opt := range []struct {
		name string
		dst  *bool
	}{
		{"rgw_s3_auth_use_rados", &cfg.UseRados},
		{"rgw_s3_auth_disable_signature_url", &cfg.DisablePresignedURLs},
		{"rgw_sigv4_insecure", &cfg.Insecure},
	} {
		v, err := o.Bool(opt.name)
		if errors.Is(err, cephconf.ErrUnknownOption) {
			continue
		}
		if err != nil {
			return Config{}, fmt.Errorf("reading %s: %w", opt.name, err)
		}
		*opt.dst = v
	}
	return cfg, nil
}
```

`doc.go`: one paragraph naming the radosgw sources transcribed (`rgw_auth_s3.cc`, `rgw_rest_s3.cc`'s `rgw::auth::s3` engines, `rgw_auth.cc`'s `LocalApplier`) and that the package never logs a credential.

- [ ] **Step 8: Run the specs and the gate; commit**

Run: `go test -tags=ceph_preview ./internal/auth/... && make check`
Expected: PASS.

```sh
git add internal/auth
git commit -m "feat(auth): add the request view, radosgw encodings and config"
```

---

### Task 3: SigV4 header authentication: credentials, canonical request, signing key, signature, skew, the CVE-2026-54330 header checks

**Files:**
- Create: `internal/auth/sigv4.go`, `internal/auth/sigv4_test.go`, `internal/auth/vectors_test.go`, `internal/auth/testdata/v4/*.json` (listed in Step 2)
- Modify: `go.mod` (`github.com/aws/aws-sdk-go-v2` and `github.com/aws/aws-sdk-go-v2/credentials` become test dependencies; run `go mod tidy` with the sandbox disabled)

**Interfaces:**
- Consumes: Task 2's `requestView`, encodings and `Config`.
- Produces:

```go
package auth

const (
	aws4Algorithm = "AWS4-HMAC-SHA256"      // AWS4_HMAC_SHA256_STR
	aws4Request   = "aws4_request"
	authGrace     = 15 * time.Minute        // RGW_AUTH_GRACE, rgw_auth_s3.h:30
)

// v4Credentials is what parse_v4_credentials yields (rgw_auth_s3.cc:538-584).
type v4Credentials struct {
	accessKey     string // before the first '/'
	scope         string // "YYYYMMDD/region/service/aws4_request", as sent
	signedHeaders string // as sent; it goes into the canonical request verbatim
	signature     string // hex, as sent
	date          string // the x-amz-date / Date / x-amz-date query value, as sent
	sessionToken  string // ignored by the local engine; STS is not implemented
}

// authData is AWSEngine::VersionAbstractor::auth_data_t: what an engine
// needs to compare signatures, plus the payload class.
type authData struct {
	accessKey        string
	signature        string // the client's
	canonicalRequest string // v4 only, kept for the vector specs; never logged
	stringToSign     string
	sign             func(secret string) (string, error) // signature_factory: the server signature for a secret
	altSign          func(secret string) (string, error) // the boto2 fallback; nil when not applicable
	v4               *v4Credentials                      // nil for v2
	presigned        bool
	payloadHash      string                              // x-amz-content-sha256 or UNSIGNED-PAYLOAD (v4)
}

// verify compares the client's signature with the server's for secret, then
// with the fallback's, in constant time.
func (d *authData) verify(secret string) (bool, error)

func parseV4Header(rv *requestView, now time.Time) (*v4Credentials, error)  // parse_v4_auth_header + parse_v4_credentials
func (c *v4Credentials) splitCredential(cred string) error                  // rgw_auth_s3.cc:565-581
func canonicalURIV4(rawPath string) string                                  // get_v4_canonical_uri, rgw_auth_s3.h:598-612
func canonicalQueryV4(rawQuery string, presigned bool) string               // get_v4_canonical_qs, rgw_auth_s3.cc:604-660
func canonicalHeadersV4(rv *requestView, signedHeaders string, insecure bool, hostPort string) (string, error) // get_v4_canonical_headers, :734-834
func canonicalRequestV4(method, uri, qs, hdrs, signedHeaders, payloadHash string) string // :902-930 before hashing
func stringToSignV4(date, scope, canonicalRequest string) string            // :937-959
func signingKeyV4(secret, scope string) []byte                              // :984-1033
func signatureV4(key []byte, stringToSign string) string                    // :1044-1066, lowercase hex
func expectedPayloadHash(rv *requestView) string                            // get_v4_exp_payload_hash: the header, or "UNSIGNED-PAYLOAD"
func timeSkewOK(now, t time.Time) bool                                      // is_time_skew_ok
func authDataV4(rv *requestView, presigned bool, cfg *Config, now time.Time) (*authData, error) // get_auth_data_v4 up to the string-to-sign
```

Every error is an `op.Error` from radosgw's errno: `EINVAL` → `op.ErrInvalidArgument`, `EPERM`/`EACCES` → `op.ErrAccessDenied`, `ERR_REQUEST_TIME_SKEWED` → `op.ErrRequestTimeTooSkewed` ([`rgw_common.cc:61`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L61), [`:88-94`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L88-L94)).

The transcription, line by line:

- `parseV4Header` ([`rgw_auth_s3.cc:390-467`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_s3.cc#L390-L467)): the `Authorization` value after `"AWS4-HMAC-SHA256 "` (shorter → `EINVAL`); split on `,` dropping empty tokens; each token split at its first `=`, both sides trimmed (`parse_key_value`, [`rgw_common.cc:681-694`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L681-L694)), a token without `=` is `EINVAL`; `Credential`, `SignedHeaders`, `Signature` required (`EINVAL`), other keys ignored, a repeated key keeps the last value; the date is `x-amz-date`, else `Date`; missing or not ISO 8601 basic → `EACCES`; skew over 15 minutes → `ERR_REQUEST_TIME_SKEWED`; `x-amz-security-token` optional.
- `splitCredential` ([`:565-581`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_s3.cc#L565-L581)): exactly four `/` and the substring `aws4_request`, else `EINVAL`; the access key is everything before the first `/`, the scope everything after.
- `canonicalURIV4`: `aws4Recode(rawPath, false)`, `/` when empty. radosgw then replaces `+` with `%20`, which is a no-op after `aws4_uri_encode` has turned every `+` into `%2B`; it is not transcribed. `rawPath` is `request_uri_aws4`, the path as the client sent it, captured before the virtual-host bucket is prepended ([`rgw_rest.cc:2024`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L2024)).
- `canonicalQueryV4`: empty raw query → `""`; every `+` becomes `%20` before splitting; split on `&` dropping empty tokens; a token with `=` is split at the first one with both sides trimmed, a token without `=` is a key with an empty value; when `presigned`, a key equal to `X-Amz-Signature` ignoring case is dropped; key and value are `aws4Recode(x, true)`; pairs are sorted by key only, stable, so a repeated key keeps arrival order (D-A7); joined `k=v&k=v`.
- `canonicalHeadersV4`: for each `;`-separated token of `signedHeaders` (empty tokens dropped): the request header it names (Task 2's `header`), absent → skipped; `content-md5` not base64 charset → `EPERM`; with `hostPort` non-empty and the token `host`, `":" + hostPort` is appended (Task 4); the value is `trimSpace`d into a map keyed by the token as sent. Unless `insecure`: `host` must be a key; if the request has `Content-Type`, `content-type` must be a key; for every request header whose lower-cased name starts with `x-amz-`, that lower-cased name must be a key; else `EPERM` ([`rgw_auth_s3.cc:788-820`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_s3.cc#L788-L820)). Output: keys in byte order, each `key:collapseSpace(value)\n`.
- `canonicalRequestV4`: `method\nuri\nqs\nhdrs\nsignedHeaders\npayloadHash` with `signedHeaders` as sent.
- `stringToSignV4`: `AWS4-HMAC-SHA256\ndate\nscope\nhex(sha256(canonicalRequest))`.
- `signingKeyV4`: the scope's first three `/`-separated fields are date, region and service (`parse_cred_scope`); `HMAC("AWS4"+secret, date)`, then region, then service, then the literal `aws4_request` — whatever the scope's fourth field says, and whatever region or service the client named (D-A7). radosgw UTF-8-encodes each secret byte (`transform_secret_key`); radosgw-generated secrets are ASCII, so the raw bytes are used.
- `signatureV4`: lowercase hex of `HMAC(key, stringToSign)`; the comparison is a byte compare of the client's string, so an uppercase client hex never matches (as in radosgw).
- `expectedPayloadHash`: the `x-amz-content-sha256` header, else `UNSIGNED-PAYLOAD` ([`rgw_auth_s3.h:638-661`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_s3.h#L638-L661)).
- `authDataV4`: parse, canonical headers (an error here is `EPERM`), payload hash, uri, qs, method = `req.Method` (radosgw substitutes `Access-Control-Request-Method` for OPTIONS CORS, phase 2), canonical request, string-to-sign; `sign` derives the signing key from the scope and returns `signatureV4`.

- [ ] **Step 1: Write the failing oracle specs**

`internal/auth/sigv4_test.go` (package `auth`), with `aws-sdk-go-v2`'s signer as the oracle:

```go
const (
	testAccessKey = "AKIDEXAMPLE"
	testSecret    = "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"
)

var signingTime = time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)

// sdkSign signs req the way an S3 client does: single-encoded path, the
// payload hash as given, service s3, region us-east-1.
func sdkSign(req *http.Request, payloadHash string) {
	signer := v4.NewSigner(func(o *v4.SignerOptions) { o.DisableURIPathEscaping = true })
	creds := aws.Credentials{AccessKeyID: testAccessKey, SecretAccessKey: testSecret}
	Expect(signer.SignHTTP(context.Background(), creds, req, payloadHash, "s3", "us-east-1", signingTime)).To(Succeed())
}

var _ = Describe("SigV4 header authentication", func() {
	cfg := DefaultConfig()

	It("verifies a request the reference client signed", func() {
		req := httptest.NewRequest(http.MethodGet, "http://s3.example.com/bucket/key?list-type=2&prefix=a%2Fb&max-keys=10", nil)
		sdkSign(req, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855")
		d, err := authDataV4(newRequestView(req), false, &cfg, signingTime)
		Expect(err).NotTo(HaveOccurred())
		Expect(d.accessKey).To(Equal(testAccessKey))
		Expect(d.v4.scope).To(Equal("20150830/us-east-1/s3/aws4_request"))
		ok, err := d.verify(testSecret)
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())
		ok, err = d.verify("not-the-secret")
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeFalse())
	})

	It("verifies a PUT with metadata, content-type and a signed payload hash", func() {
		body := "hello"
		req := httptest.NewRequest(http.MethodPut, "http://s3.example.com/bucket/key", strings.NewReader(body))
		req.Header.Set("Content-Type", "text/plain")
		req.Header.Set("X-Amz-Meta-Color", "  navy   blue ")
		sum := sha256.Sum256([]byte(body))
		req.Header.Set("X-Amz-Content-Sha256", hex.EncodeToString(sum[:]))
		sdkSign(req, hex.EncodeToString(sum[:]))
		d, err := authDataV4(newRequestView(req), false, &cfg, signingTime)
		Expect(err).NotTo(HaveOccurred())
		Expect(d.payloadHash).To(Equal(hex.EncodeToString(sum[:])))
		Expect(d.canonicalRequest).To(ContainSubstring("x-amz-meta-color:navy blue\n"))
		ok, err := d.verify(testSecret)
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())
	})

	It("takes a missing x-amz-content-sha256 as UNSIGNED-PAYLOAD, as go-ceph's admin client relies on", func() {
		req := httptest.NewRequest(http.MethodGet, "http://s3.example.com/admin/user?uid=alice", nil)
		sdkSign(req, "UNSIGNED-PAYLOAD")
		req.Header.Del("X-Amz-Content-Sha256") // the SDK adds it; go-ceph's SignHTTP call never does
		d, err := authDataV4(newRequestView(req), false, &cfg, signingTime)
		Expect(err).NotTo(HaveOccurred())
		Expect(d.payloadHash).To(Equal("UNSIGNED-PAYLOAD"))
		ok, err := d.verify(testSecret)
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())
	})

	It("rejects an unsigned x-amz header unless rgw_sigv4_insecure", func() {
		req := httptest.NewRequest(http.MethodGet, "http://s3.example.com/bucket/key", nil)
		sdkSign(req, "UNSIGNED-PAYLOAD")
		req.Header.Set("X-Amz-Meta-Injected", "after signing")
		_, err := authDataV4(newRequestView(req), false, &cfg, signingTime)
		Expect(err).To(MatchError(op.ErrAccessDenied))
		insecure := cfg
		insecure.Insecure = true
		_, err = authDataV4(newRequestView(req), false, &insecure, signingTime)
		Expect(err).NotTo(HaveOccurred())
	})

	It("rejects a skewed request with RequestTimeTooSkewed", func() {
		req := httptest.NewRequest(http.MethodGet, "http://s3.example.com/", nil)
		sdkSign(req, "UNSIGNED-PAYLOAD")
		_, err := authDataV4(newRequestView(req), false, &cfg, signingTime.Add(15*time.Minute+time.Second))
		Expect(err).To(MatchError(op.ErrRequestTimeTooSkewed))
		_, err = authDataV4(newRequestView(req), false, &cfg, signingTime.Add(-15*time.Minute))
		Expect(err).NotTo(HaveOccurred())
	})
})
```

Note the SDK strips the `Content-Length` and `Transfer-Encoding` headers from signing and adds `X-Amz-Date` itself; a `Content-Type` set before signing is signed, which the canonical-headers check requires.

- [ ] **Step 2: Write the vector runner and the hand-authored vectors**

`internal/auth/vectors_test.go` (package `auth`) runs every `testdata/<dir>/*.json`. The v4 schema:

```json
{
  "name": "query-order-and-encoding",
  "method": "GET",
  "target": "/b/k%2Fv+w%7e?b=2&a=1&a=0&c=%2F&d=a+b&acl",
  "host": "example.amazonaws.com",
  "headers": {"X-Amz-Date": ["20150830T123600Z"]},
  "presigned": false,
  "signed_headers": "host;x-amz-date",
  "scope": "20150830/us-east-1/s3/aws4_request",
  "secret": "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY",
  "now": "2015-08-30T12:36:00Z",
  "insecure": false,
  "signature": "",
  "expect": {
    "canonical_request": "GET\n/b/k/v%2Bw~\na=1&a=0&acl=&b=2&c=%2F&d=a%20b\nhost:example.amazonaws.com\nx-amz-date:20150830T123600Z\n\nhost;x-amz-date\nUNSIGNED-PAYLOAD",
    "date": "20150830T123600Z",
    "access_key": "AKIDEXAMPLE",
    "error": ""
  }
}
```

The runner builds the request (`httptest.NewRequest(method, "http://"+host+target, body)`, headers added in order), and for header vectors sets `Authorization: AWS4-HMAC-SHA256 Credential=<access_key>/<scope>, SignedHeaders=<signed_headers>, Signature=<sig>` where `<sig>` is the vector's `signature` when non-empty (negative vectors) or, when empty, the signature computed from the vector's own `expect.canonical_request`, `date`, `scope` and `secret` through `signatureV4(signingKeyV4(secret, scope), stringToSignV4(date, scope, canonical_request))` — so a canonicalization that differs from the recorded text fails on the `canonical_request` assertion, never silently. Then `authDataV4(newRequestView(req), presigned, &cfg, now)`: with `expect.error` empty, no error, `d.canonicalRequest == expect.canonical_request`, `d.v4.date == expect.date`, `d.accessKey == expect.access_key`, and `d.verify(secret)` true; with `expect.error` set (an `op.Error` code such as `"AccessDenied"`), `errors.As(err, &opErr)` and `opErr.Code == expect.error`. A vector may carry `"authorization"` verbatim instead of the three fields for malformed-header cases, and `"query_extra"` for Task 4.

Vectors to write under `testdata/v4/`, each a file, each pinning one rule (the canonical request text is written out in full in every file; the runner does not infer it):

| file | rule |
|---|---|
| `get-vanilla-unsigned.json` | `GET /`, host and x-amz-date signed, no payload header: canonical request `GET\n/\n\nhost:example.amazonaws.com\nx-amz-date:20150830T123600Z\n\nhost;x-amz-date\nUNSIGNED-PAYLOAD` |
| `query-order-and-encoding.json` | the example above: repeated key keeps arrival order, bare key gets `=`, `+` is a space, `%2F` stays encoded in the query, `%7e` and `%2F` recode in the path |
| `path-with-space-and-unicode.json` | target `/b/my%20k%C3%BCy`: canonical uri `/b/my%20k%C3%BCy`; a lowercase `%c3%bc` in the target recodes to uppercase |
| `signed-header-absent-is-skipped.json` | `signed_headers: host;x-amz-date;x-amz-foo`, no `x-amz-foo` header: canonical headers omit it, the signed-headers line still says `host;x-amz-date;x-amz-foo`, verification succeeds |
| `duplicate-header-last-wins.json` | two `X-Amz-Meta-A` values `first`, `second`: canonical header `x-amz-meta-a:second` |
| `header-value-collapsed.json` | `X-Amz-Meta-A: "  a \t  b  "`: `x-amz-meta-a:a b` |
| `date-header-basic-iso.json` | `Date: 20150830T123600Z` and no `X-Amz-Date`: accepted, `expect.date` is that value |
| `date-header-rfc1123-rejected.json` | `Date: Sun, 30 Aug 2015 12:36:00 GMT` and no `X-Amz-Date`: `AccessDenied` (EACCES) |
| `unsigned-host-rejected.json` | `signed_headers: x-amz-date`: `AccessDenied` |
| `unsigned-content-type-rejected.json` | request has `Content-Type`, signed headers `host;x-amz-date`: `AccessDenied` |
| `insecure-allows-unsigned-x-amz.json` | `insecure: true`, `X-Amz-Meta-Extra` unsigned: accepted |
| `content-md5-bad-charset.json` | `Content-MD5: "!!!"` signed: `AccessDenied` |
| `credential-three-slashes.json` | `scope: 20150830/us-east-1/s3` (three `/` in the credential): `InvalidArgument` |
| `credential-no-aws4-request.json` | scope ending `aws4_reques`: `InvalidArgument` |
| `authorization-missing-signature.json` | `authorization: AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/s3/aws4_request, SignedHeaders=host`: `InvalidArgument` |
| `authorization-token-without-equals.json` | `authorization: AWS4-HMAC-SHA256 Credential=…, junk, Signature=abc`: `InvalidArgument` |
| `region-and-service-unchecked.json` | scope `20150830/nowhere/sqs/aws4_request` with a wrong date field `19700101`: accepted (D-A7) |
| `skewed-16-minutes.json` | `now: 2015-08-30T12:52:01Z`: `RequestTimeTooSkewed` |
| `uppercase-hex-signature-rejected.json` | `signature` given as the correct value upper-cased: verify false (the runner asserts `verify` is false when `expect.verify_false` is true) |

- [ ] **Step 3: Run the specs to verify they fail**

Run: `go test -tags=ceph_preview ./internal/auth/...`
Expected: compile errors (`authDataV4` undefined) after `go mod tidy` has added the SDK test dependencies (`dangerouslyDisableSandbox` for the download).

- [ ] **Step 4: Implement `sigv4.go`**

```go
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jhoblitt/rgw-go/internal/op"
)

const (
	aws4Algorithm = "AWS4-HMAC-SHA256"
	aws4Request   = "aws4_request"
	authGrace     = 15 * time.Minute
)

type v4Credentials struct {
	accessKey, scope, signedHeaders, signature, date, sessionToken string
}

type authData struct {
	accessKey, signature, canonicalRequest, stringToSign string
	sign, altSign                                       func(secret string) (string, error)
	v4                                                  *v4Credentials
	presigned                                           bool
	payloadHash                                         string
}

func (d *authData) verify(secret string) (bool, error) {
	got, err := d.sign(secret)
	if err != nil {
		return false, err
	}
	if subtle.ConstantTimeCompare([]byte(got), []byte(d.signature)) == 1 {
		return true, nil
	}
	if d.altSign == nil {
		return false, nil
	}
	alt, err := d.altSign(secret)
	if err != nil {
		return false, err
	}
	return subtle.ConstantTimeCompare([]byte(alt), []byte(d.signature)) == 1, nil
}

func timeSkewOK(now, t time.Time) bool { return now.Sub(t).Abs() <= authGrace }

// parseV4Header is parse_v4_auth_header (rgw_auth_s3.cc:390-467) and the
// credential split of parse_v4_credentials (:565-581).
func parseV4Header(rv *requestView, now time.Time) (*v4Credentials, error) {
	a, _ := rv.header("authorization")
	if len(a) < len(aws4Algorithm)+1 {
		return nil, fmt.Errorf("%w: credentials string is too short", op.ErrInvalidArgument)
	}
	kv := map[string]string{}
	for _, tok := range strings.Split(a[len(aws4Algorithm)+1:], ",") {
		if tok == "" {
			continue
		}
		k, v, ok := strings.Cut(tok, "=")
		if !ok {
			return nil, fmt.Errorf("%w: malformed authorization header", op.ErrInvalidArgument)
		}
		kv[trimSpace(k)] = trimSpace(v)
	}
	for _, k := range []string{"Credential", "SignedHeaders", "Signature"} {
		if _, ok := kv[k]; !ok {
			return nil, fmt.Errorf("%w: authorization header missing %s", op.ErrInvalidArgument, k)
		}
	}
	c := &v4Credentials{signedHeaders: kv["SignedHeaders"], signature: kv["Signature"]}
	d, ok := rv.header("x-amz-date")
	if !ok {
		d, ok = rv.header("date")
	}
	t, parsed := parseISO8601Basic(d)
	if !ok || !parsed {
		return nil, fmt.Errorf("%w: missing or malformed request date", op.ErrAccessDenied)
	}
	c.date = d
	if !timeSkewOK(now, t) {
		return nil, op.ErrRequestTimeTooSkewed
	}
	c.sessionToken, _ = rv.header("x-amz-security-token")
	if err := c.splitCredential(kv["Credential"]); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *v4Credentials) splitCredential(cred string) error {
	if strings.Count(cred, "/") != 4 || !strings.Contains(cred, aws4Request) {
		return fmt.Errorf("%w: malformed credential", op.ErrInvalidArgument)
	}
	c.accessKey, c.scope, _ = strings.Cut(cred, "/")
	return nil
}

func canonicalURIV4(rawPath string) string {
	if u := aws4Recode(rawPath, false); u != "" {
		return u
	}
	return "/"
}

func canonicalQueryV4(rawQuery string, presigned bool) string {
	if rawQuery == "" {
		return ""
	}
	type pair struct{ k, v string }
	var pairs []pair
	for _, tok := range strings.Split(strings.ReplaceAll(rawQuery, "+", "%20"), "&") {
		if tok == "" {
			continue
		}
		k, v := tok, ""
		if i := strings.IndexByte(tok, '='); i >= 0 {
			k, v = trimSpace(tok[:i]), trimSpace(tok[i+1:])
		}
		if presigned && strings.EqualFold(k, "X-Amz-Signature") {
			continue
		}
		pairs = append(pairs, pair{aws4Recode(k, true), aws4Recode(v, true)})
	}
	sort.SliceStable(pairs, func(i, j int) bool { return pairs[i].k < pairs[j].k })
	var b strings.Builder
	for i, p := range pairs {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(p.k)
		b.WriteByte('=')
		b.WriteString(p.v)
	}
	return b.String()
}

func canonicalHeadersV4(rv *requestView, signedHeaders string, insecure bool, hostPort string) (string, error) {
	m := map[string]string{}
	for _, tok := range strings.Split(signedHeaders, ";") {
		if tok == "" {
			continue
		}
		v, ok := rv.header(tok)
		if !ok {
			continue
		}
		if tok == "content-md5" && !isBase64Charset(v) {
			return "", fmt.Errorf("%w: content-md5 is not base64", op.ErrAccessDenied)
		}
		if hostPort != "" && tok == "host" {
			v += ":" + hostPort
		}
		m[tok] = trimSpace(v)
	}
	if !insecure {
		if _, ok := m["host"]; !ok {
			return "", fmt.Errorf("%w: host is not signed", op.ErrAccessDenied)
		}
		if _, ok := m["content-type"]; rv.hasHeader("content-type") && !ok {
			return "", fmt.Errorf("%w: content-type is not signed", op.ErrAccessDenied)
		}
		for name := range rv.req.Header {
			lower := strings.ToLower(name)
			if _, ok := m[lower]; strings.HasPrefix(lower, "x-amz-") && !ok {
				return "", fmt.Errorf("%w: %s is not signed", op.ErrAccessDenied, lower)
			}
		}
	}
	var b strings.Builder
	for _, k := range slices.Sorted(maps.Keys(m)) {
		b.WriteString(k)
		b.WriteByte(':')
		b.WriteString(collapseSpace(m[k]))
		b.WriteByte('\n')
	}
	return b.String(), nil
}

func canonicalRequestV4(method, uri, qs, hdrs, signedHeaders, payloadHash string) string {
	return strings.Join([]string{method, uri, qs, hdrs, signedHeaders, payloadHash}, "\n")
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func hmacSHA256(key, msg []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(msg)
	return h.Sum(nil)
}

func stringToSignV4(date, scope, canonicalRequest string) string {
	return strings.Join([]string{aws4Algorithm, date, scope, sha256Hex(canonicalRequest)}, "\n")
}

func signingKeyV4(secret, scope string) []byte {
	f := strings.SplitN(scope, "/", 4)
	for len(f) < 3 {
		f = append(f, "")
	}
	k := hmacSHA256([]byte("AWS4"+secret), []byte(f[0]))
	k = hmacSHA256(k, []byte(f[1]))
	k = hmacSHA256(k, []byte(f[2]))
	return hmacSHA256(k, []byte(aws4Request))
}

func signatureV4(key []byte, stringToSign string) string {
	return hex.EncodeToString(hmacSHA256(key, []byte(stringToSign)))
}

const unsignedPayload = "UNSIGNED-PAYLOAD"

func expectedPayloadHash(rv *requestView) string {
	if h, ok := rv.header("x-amz-content-sha256"); ok {
		return h
	}
	return unsignedPayload
}

func authDataV4(rv *requestView, presigned bool, cfg *Config, now time.Time) (*authData, error) {
	var (
		c   *v4Credentials
		err error
	)
	if presigned {
		c, err = parseV4Query(rv, now)
	} else {
		c, err = parseV4Header(rv, now)
	}
	if err != nil {
		return nil, err
	}
	hdrs, err := canonicalHeadersV4(rv, c.signedHeaders, cfg.Insecure, "")
	if err != nil {
		return nil, err
	}
	payloadHash := expectedPayloadHash(rv)
	creq := canonicalRequestV4(rv.req.Method, canonicalURIV4(rv.rawPath), canonicalQueryV4(rv.rawQuery, presigned), hdrs, c.signedHeaders, payloadHash)
	sts := stringToSignV4(c.date, c.scope, creq)
	d := &authData{
		accessKey: c.accessKey, signature: c.signature, canonicalRequest: creq, stringToSign: sts,
		v4: c, presigned: presigned, payloadHash: payloadHash,
	}
	d.sign = func(secret string) (string, error) { return signatureV4(signingKeyV4(secret, c.scope), sts), nil }
	return d, nil
}
```

Until Task 4 lands, `parseV4Query` is a one-line stub returning `op.ErrNotImplemented` so the package compiles; Task 4 replaces it.

- [ ] **Step 5: Run the specs and the gate; commit**

Run: `go test -tags=ceph_preview ./internal/auth/... && make check`
Expected: PASS; every vector under `testdata/v4/` runs as its own `Entry`.

```sh
git add internal/auth go.mod go.sum
git commit -m "feat(auth): verify SigV4 header signatures as radosgw does"
```

---

### Task 4: SigV4 presigned (query-string) authentication and the boto2 `host:port` fallback

**Files:**
- Modify: `internal/auth/sigv4.go` (`parseV4Query`, the `altSign` branch of `authDataV4`), `internal/auth/vectors_test.go` (presigned vectors, `expect.message`, `expect.verify_false`)
- Create: `internal/auth/presign_test.go`, `internal/auth/testdata/v4/presigned-*.json`

**Interfaces:**
- Consumes: Task 3.
- Produces:

```go
// parseV4Query is parse_v4_query_string (rgw_auth_s3.cc:280-339) plus the
// credential split: x-amz-credential, x-amz-date (ISO 8601 basic), x-amz-expires
// in [1, 604800], x-amz-signedheaders and x-amz-signature required (EPERM);
// an x-amz-security-token present but empty is EPERM; now >= date+expires in
// whole seconds is ERR_PRESIGNED_URL_EXPIRED, which Strategy::apply turns
// into EPERM with the message "The pre-signed URL has expired"
// (rgw_auth.cc:500-504). There is no 15-minute skew check for presigned URLs.
func parseV4Query(rv *requestView, now time.Time) (*v4Credentials, error)

// boto2HostPort is the port the boto2 fallback appends to a signed host:
// SERVER_PORT_SECURE unless "443" under TLS, else SERVER_PORT unless "80"
// (rgw_auth_s3.cc:770-783); "" when nothing is appended.
func boto2HostPort(rv *requestView) string
```

`authDataV4` gains, for `presigned` requests whose `signedHeaders` contains the token `host` and whose `boto2HostPort` is non-empty: a second canonical headers string with the port appended, its own canonical request and string-to-sign, and `d.altSign` computing that signature. `verify` (Task 3) already tries `altSign` after `sign`. This is the `s3_main_strategy_boto2` FALLBACK engine of [`rgw_auth_registry.h:28-49`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_registry.h#L28-L49), whose only difference from the plain engine is `force_boto2_compat = true` in `get_v4_canonical_headers` ([`rgw_rest_s3.cc:6022-6030`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L6022-L6030)); with both failing, radosgw reports the plain engine's result ([`rgw_auth.cc:396-397`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth.cc#L396-L397)), so `verify` returning false yields `ErrSignatureDoesNotMatch` in Task 9 exactly as before.

- [ ] **Step 1: Write the failing oracle specs**

`internal/auth/presign_test.go` (package `auth`):

```go
// sdkPresign presigns req for 300 seconds and returns the request a client
// would then send: the presigned URL with the Host the signer signed.
func sdkPresign(req *http.Request) *http.Request {
	q := req.URL.Query()
	q.Set("X-Amz-Expires", "300")
	req.URL.RawQuery = q.Encode()
	signer := v4.NewSigner(func(o *v4.SignerOptions) { o.DisableURIPathEscaping = true })
	creds := aws.Credentials{AccessKeyID: testAccessKey, SecretAccessKey: testSecret}
	u, _, err := signer.PresignHTTP(context.Background(), creds, req, "UNSIGNED-PAYLOAD", "s3", "us-east-1", signingTime)
	Expect(err).NotTo(HaveOccurred())
	out := httptest.NewRequest(req.Method, u, nil)
	out.Host = req.Host
	return out
}

func withLocalPort(req *http.Request, port int, tls bool) *http.Request {
	ctx := context.WithValue(req.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
	req = req.WithContext(ctx)
	if tls {
		req.TLS = &tls.ConnectionState{}
	}
	return req
}

var _ = Describe("SigV4 presigned authentication", func() {
	cfg := DefaultConfig()

	It("verifies a URL the reference client presigned, inside its window, without a skew check", func() {
		req := sdkPresign(httptest.NewRequest(http.MethodGet, "http://s3.example.com/bucket/key?response-content-type=text%2Fplain", nil))
		d, err := authDataV4(newRequestView(req), true, &cfg, signingTime.Add(299*time.Second))
		Expect(err).NotTo(HaveOccurred())
		Expect(d.presigned).To(BeTrue())
		Expect(d.payloadHash).To(Equal("UNSIGNED-PAYLOAD"))
		ok, err := d.verify(testSecret)
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())
	})

	It("expires at date plus X-Amz-Expires, in whole seconds, with radosgw's message", func() {
		req := sdkPresign(httptest.NewRequest(http.MethodGet, "http://s3.example.com/bucket/key", nil))
		_, err := authDataV4(newRequestView(req), true, &cfg, signingTime.Add(300*time.Second))
		Expect(err).To(MatchError(op.ErrAccessDenied))
		var opErr *op.Error
		Expect(errors.As(err, &opErr)).To(BeTrue())
		Expect(opErr.Message).To(Equal("The pre-signed URL has expired"))
		_, err = authDataV4(newRequestView(req), true, &cfg, signingTime.Add(299*time.Second+999*time.Millisecond))
		Expect(err).NotTo(HaveOccurred())
	})

	It("accepts a presigned URL signed with host:port arriving with a bare Host (boto2 fallback)", func() {
		signed := httptest.NewRequest(http.MethodGet, "http://example.com:8080/bucket/key", nil)
		signed.Host = "example.com:8080"
		req := sdkPresign(signed)
		req.Host = "example.com" // a port-forward stripped it
		req = withLocalPort(req, 8080, false)
		d, err := authDataV4(newRequestView(req), true, &cfg, signingTime)
		Expect(err).NotTo(HaveOccurred())
		Expect(d.altSign).NotTo(BeNil())
		ok, err := d.verify(testSecret)
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue(), "the plain attempt fails, the host:8080 attempt matches")
	})

	It("has no fallback on the scheme's default port, under TLS on 443, or for header auth", func() {
		req := sdkPresign(httptest.NewRequest(http.MethodGet, "http://example.com/bucket/key", nil))
		d, err := authDataV4(newRequestView(withLocalPort(req, 80, false)), true, &cfg, signingTime)
		Expect(err).NotTo(HaveOccurred())
		Expect(d.altSign).To(BeNil())
		d, err = authDataV4(newRequestView(withLocalPort(req, 443, true)), true, &cfg, signingTime)
		Expect(err).NotTo(HaveOccurred())
		Expect(d.altSign).To(BeNil())
		d, err = authDataV4(newRequestView(withLocalPort(req, 8443, true)), true, &cfg, signingTime)
		Expect(err).NotTo(HaveOccurred())
		Expect(d.altSign).NotTo(BeNil())
		hdr := httptest.NewRequest(http.MethodGet, "http://example.com/bucket/key", nil)
		sdkSign(hdr, "UNSIGNED-PAYLOAD")
		d, err = authDataV4(newRequestView(withLocalPort(hdr, 8080, false)), false, &cfg, signingTime)
		Expect(err).NotTo(HaveOccurred())
		Expect(d.altSign).To(BeNil())
	})

	It("still fails when neither the bare host nor host:port matches", func() {
		req := sdkPresign(httptest.NewRequest(http.MethodGet, "http://example.com/bucket/key", nil))
		req.Host = "example.com:8080" // signed bare, arrives with a port: radosgw fails both attempts
		req = withLocalPort(req, 8080, false)
		d, err := authDataV4(newRequestView(req), true, &cfg, signingTime)
		Expect(err).NotTo(HaveOccurred())
		ok, err := d.verify(testSecret)
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeFalse())
	})
})
```

- [ ] **Step 2: Extend the runner and write the presigned vectors**

In `vectors_test.go`, a vector with `"presigned": true` gets its credentials as query parameters instead of an `Authorization` header: the runner appends `X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=<access_key>%2F<scope url-encoded>&X-Amz-Date=<date>&X-Amz-Expires=<expires>&X-Amz-SignedHeaders=<signed_headers>` to the target (after any `&` the target already has), plus `query_extra` verbatim when present, then `&X-Amz-Signature=<sig>` computed as in Task 3 (or the vector's `signature`). New optional fields: `"expires"` (string, default `"300"`), `"expect.message"` (asserted equal to `opErr.Message` when set), `"expect.verify_false"` (asserts `verify` is false instead of true). Vectors under `testdata/v4/`, each with its full canonical request written out:

| file | rule |
|---|---|
| `presigned-get.json` | target `/b/k?response-content-type=text%2Fplain`, now = date + 100 s: canonical request `GET\n/b/k\nX-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=AKIDEXAMPLE%2F20150830%2Fus-east-1%2Fs3%2Faws4_request&X-Amz-Date=20150830T123600Z&X-Amz-Expires=300&X-Amz-SignedHeaders=host&response-content-type=text%2Fplain\nhost:example.amazonaws.com\n\nhost\nUNSIGNED-PAYLOAD` (uppercase `X` sorts before lowercase `r`; the signature parameter is absent) |
| `presigned-signature-dropped-any-case.json` | `query_extra: x-amz-signature=deadbeef` in addition to the runner's signature: the canonical query has no signature parameter at all; accepted |
| `presigned-expired.json` | `expires: 300`, now = date + 300 s: `AccessDenied`, message `The pre-signed URL has expired` |
| `presigned-expires-too-large.json` | `expires: 604801`: `AccessDenied`, no message |
| `presigned-expires-zero.json` | `expires: 0`: `AccessDenied` |
| `presigned-expires-trailing-junk.json` | `expires: 300abc`, now = date + 100 s: accepted (`atoll`) |
| `presigned-max-window-no-skew.json` | `expires: 604800`, now = date + 6 days: accepted |
| `presigned-missing-signedheaders.json` | `signed_headers: ""`: `AccessDenied` |
| `presigned-empty-security-token.json` | `query_extra: X-Amz-Security-Token=`: `AccessDenied` |
| `presigned-date-malformed.json` | `date: 2015-08-30T12:36:00Z`: `AccessDenied` (EPERM, not EACCES as for header auth) |
| `presigned-payload-header-honoured.json` | headers `X-Amz-Content-Sha256: UNSIGNED-PAYLOAD` and `signed_headers: host;x-amz-content-sha256`: canonical headers include `x-amz-content-sha256:UNSIGNED-PAYLOAD`; accepted |

- [ ] **Step 3: Run the specs to verify they fail**

Run: `go test -tags=ceph_preview ./internal/auth/...`
Expected: FAIL, `parseV4Query` returns `NotImplemented`; the boto2 specs fail on `altSign` being nil.

- [ ] **Step 4: Implement `parseV4Query`, `boto2HostPort` and the `altSign` branch**

```go
const maxPresignExpires = 7 * 24 * 60 * 60 // seconds, rgw_auth_s3.cc:309

func parseV4Query(rv *requestView, now time.Time) (*v4Credentials, error) {
	denied := func(what string) error { return fmt.Errorf("%w: presigned url %s", op.ErrAccessDenied, what) }
	cred, _ := rv.param("x-amz-credential")
	if cred == "" {
		return nil, denied("without a credential")
	}
	c := &v4Credentials{}
	c.date, _ = rv.param("x-amz-date")
	t, ok := parseISO8601Basic(c.date)
	if !ok {
		return nil, denied("with a malformed date")
	}
	expires, _ := rv.param("x-amz-expires")
	if expires == "" {
		return nil, denied("without an expiry")
	}
	exp := atoll(expires)
	if exp < 1 || exp > maxPresignExpires {
		return nil, denied("with an expiry out of range")
	}
	if now.Unix() >= t.Unix()+exp {
		return nil, op.ErrAccessDenied.WithMessage("The pre-signed URL has expired")
	}
	if c.signedHeaders, _ = rv.param("x-amz-signedheaders"); c.signedHeaders == "" {
		return nil, denied("without signed headers")
	}
	if c.signature, _ = rv.param("x-amz-signature"); c.signature == "" {
		return nil, denied("without a signature")
	}
	if tok, present := rv.param("x-amz-security-token"); present {
		if tok == "" {
			return nil, denied("with an empty security token")
		}
		c.sessionToken = tok
	}
	if err := c.splitCredential(cred); err != nil {
		return nil, err
	}
	return c, nil
}

func boto2HostPort(rv *requestView) string {
	port, secure := rv.localPort()
	switch {
	case port == "":
		return ""
	case secure && port != "443":
		return port
	case !secure && port != "80":
		return port
	}
	return ""
}
```

In `authDataV4`, after `d.sign` is set:

```go
	if presigned && slices.Contains(strings.Split(c.signedHeaders, ";"), "host") {
		if port := boto2HostPort(rv); port != "" {
			if altHdrs, err := canonicalHeadersV4(rv, c.signedHeaders, cfg.Insecure, port); err == nil {
				altSts := stringToSignV4(c.date, c.scope, canonicalRequestV4(rv.req.Method, canonicalURIV4(rv.rawPath), canonicalQueryV4(rv.rawQuery, true), altHdrs, c.signedHeaders, payloadHash))
				d.altSign = func(secret string) (string, error) { return signatureV4(signingKeyV4(secret, c.scope), altSts), nil }
			}
		}
	}
```

- [ ] **Step 5: Run the specs and the gate; commit**

Run: `go test -tags=ceph_preview ./internal/auth/... && make check`
Expected: PASS.

```sh
git add internal/auth
git commit -m "feat(auth): verify presigned SigV4 URLs with radosgw's boto2 fallback"
```

---

### Task 5: Payload classes, the empty-payload rule, the per-op payload forms, the single-chunk hash reader, the decoded length

**Files:**
- Create: `internal/auth/payload.go`, `internal/auth/payload_test.go`
- Modify: `internal/auth/sigv4.go` (`authDataV4` fills `d.payload`)

**Interfaces:**
- Consumes: Tasks 2 and 3.
- Produces:

```go
package auth

const (
	unsignedPayload            = "UNSIGNED-PAYLOAD"                           // AWS4_UNSIGNED_PAYLOAD_HASH
	emptyPayloadHash           = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" // AWS4_EMPTY_PAYLOAD_HASH
	streamingPayload           = "STREAMING-AWS4-HMAC-SHA256-PAYLOAD"         // AWS4_STREAMING_PAYLOAD_HASH
	streamingUnsignedTrailer   = "STREAMING-UNSIGNED-PAYLOAD-TRAILER"         // AWS4_STREAMING_UNSIGNED_PAYLOAD_TRAILER
	streamingPayloadTrailer    = "STREAMING-AWS4-HMAC-SHA256-PAYLOAD-TRAILER" // AWS4_STREAMING_HMAC_SHA256_PAYLOAD_TRAILER
	decodedContentLengthHeader = "x-amz-decoded-content-length"
)

type payloadKind uint8

const (
	payloadNone    payloadKind = iota // no completer
	payloadSingle                     // AWSv4ComplSingle: sha256 of the body must equal the header
	payloadChunked                    // AWSv4ComplMulti: aws-chunked framing
)

// payloadClass is what get_auth_data_v4 decides from x-amz-content-sha256
// and the body's presence (rgw_rest_s3.cc:5855-6001, rgw_auth_s3.h:663-702).
type payloadClass struct {
	kind             payloadKind
	unsignedPayload  bool // FLAG_UNSIGNED_PAYLOAD: the hash contains UNSIGNED-PAYLOAD
	trailingChecksum bool // FLAG_TRAILING_CHECKSUM: the hash ends in TRAILER
	unsignedChunked  bool // FLAG_UNSIGNED_CHUNKED: STREAMING-UNSIGNED-PAYLOAD-TRAILER
	trailerSignature bool // FLAG_TRAILER_SIGNATURE: STREAMING-AWS4-HMAC-SHA256-PAYLOAD-TRAILER
}

// classifyPayload applies the rules; the empty-payload rule is the one error.
func classifyPayload(rv *requestView, hash string) (payloadClass, error)

// acceptedBy is get_auth_data_v4's per-op completer switch: a single-chunk
// payload needs PayloadSigned in forms, an aws-chunked one PayloadChunked;
// otherwise op.ErrNotImplemented. payloadNone is always accepted.
func (pc payloadClass) acceptedBy(forms op.PayloadForms) error

// hashReader is AWSv4ComplSingle: it hashes what passes through and, on the
// read that reaches EOF, compares the hex digest with the expected value.
type hashReader struct{ /* src io.Reader; h hash.Hash; expected string; err error */ }
func newHashReader(src io.Reader, expected string) *hashReader
func (r *hashReader) Read(p []byte) (int, error)

// completer builds the body reader for d.payload once the signature verified:
// nil for payloadNone, a hashReader for payloadSingle, a chunkedReader
// for payloadChunked. The returned length is what op.AuthResult
// .ContentLength carries: the request's own, or x-amz-decoded-content-length
// for a chunked payload (missing or negative → EINVAL, rgw_auth_s3.cc:1534-1548).
func (d *authData) completer(rv *requestView, secret string) (body io.Reader, contentLength int64, err error)
```

`authData` gains `payload payloadClass`, filled by `authDataV4` after the payload hash is known; `classifyPayload`'s error (the empty-payload mismatch, [`rgw_rest_s3.cc:5858-5866`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L5858-L5866)) is returned from `authDataV4` before any credential lookup, as radosgw throws it from the abstractor.

The rules, transcribed:

1. `empty` is `contentLength() == 0 && !chunkedTE()` (`is_v4_payload_empty`: `content_length == 0` and no `Transfer-Encoding` header; Go reports 0 both for no body and for `Content-Length: 0`, and -1 for a chunked transfer encoding).
2. `unsigned` is `strings.Contains(hash, "UNSIGNED-PAYLOAD")`; `traditional` is `hash == "UNSIGNED-PAYLOAD"`; `checksumTrailer` is `HasSuffix(hash, "TRAILER")`; `unsignedChunked` is `hash == streamingUnsignedTrailer`; `trailerSig` is `hash == streamingPayloadTrailer`; `streamed` is `HasPrefix(hash, "STREAMING-")`.
3. `empty && !unsigned && hash != emptyPayloadHash` → `op.ErrContentSHA256Mismatch`.
4. `traditional || (unsigned && !checksumTrailer) || empty` → `payloadNone`.
5. otherwise `!streamed` → `payloadSingle`.
6. otherwise `payloadChunked` with the four flags.

`acceptedBy` is the whitelist that follows the classification in `get_auth_data_v4` (D-A1): `payloadSingle` without `op.PayloadSigned` in the route's forms is `op.ErrNotImplemented` ([`rgw_rest_s3.cc:5899-5944`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L5899-L5944), [`[T]:6466-6515`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L6466-L6515)), `payloadChunked` without `op.PayloadChunked` likewise ([`:5957-5969`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5957-L5969), [`[T]:6528-6540`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L6528-L6540)), `payloadNone` passes whatever the forms. `authDataV4` does not call it, because it does not know the route; Task 9's `Authenticate` calls it on the returned `authData` before anything else, which keeps radosgw's order (the empty-payload rule, then the whitelist, then the key).

`hashReader.Read` forwards to the source, updates the SHA-256, and when the source returns `io.EOF` compares `hex(sum)` with `expected` byte for byte (radosgw's `payload_hash.compare(expected)`, [`rgw_auth_s3.cc:1737-1756`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_s3.cc#L1737-L1756); an uppercase expected hex never matches): a match keeps `io.EOF`, a mismatch makes the error `op.ErrContentSHA256Mismatch` (the `do_aws4_auth_completion` mapping, [`rgw_op.cc:1370-1371`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L1370-L1371)), sticky for every later `Read`. `n` bytes read on that call are still returned.

- [ ] **Step 1: Write the failing specs**

`internal/auth/payload_test.go` (package `auth`):

```go
func viewWithBody(hash string, contentLength int64, chunkedTE bool) *requestView {
	req := httptest.NewRequest(http.MethodPut, "http://h/b/k", nil)
	if hash != "" {
		req.Header.Set("X-Amz-Content-Sha256", hash)
	}
	req.ContentLength = contentLength
	if chunkedTE {
		req.TransferEncoding = []string{"chunked"}
	}
	return newRequestView(req)
}

var _ = Describe("payload classification", func() {
	const hello = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824" // sha256("hello")

	DescribeTable("classifyPayload is get_auth_data_v4's completer choice",
		func(hash string, cl int64, te bool, want payloadClass, wantErr error) {
			rv := viewWithBody(hash, cl, te)
			got, err := classifyPayload(rv, expectedPayloadHash(rv))
			if wantErr != nil {
				Expect(err).To(MatchError(wantErr))
				return
			}
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(want))
		},
		Entry("absent header with a body is unsigned", "", int64(5), false, payloadClass{kind: payloadNone}, nil),
		Entry("UNSIGNED-PAYLOAD", unsignedPayload, int64(5), false, payloadClass{kind: payloadNone}, nil),
		Entry("hex with a body is a single completer", hello, int64(5), false, payloadClass{kind: payloadSingle}, nil),
		Entry("hex with a chunked transfer encoding is not empty", hello, int64(-1), true, payloadClass{kind: payloadSingle}, nil),
		Entry("empty hash with no body", emptyPayloadHash, int64(0), false, payloadClass{kind: payloadNone}, nil),
		Entry("non-empty hash with no body is a mismatch now", hello, int64(0), false, payloadClass{}, op.ErrContentSHA256Mismatch),
		Entry("garbage hash with no body is a mismatch now", "not-a-hash", int64(0), false, payloadClass{}, op.ErrContentSHA256Mismatch),
		Entry("streaming signed chunks", streamingPayload, int64(100), false, payloadClass{kind: payloadChunked}, nil),
		Entry("streaming signed chunks with a signed trailer", streamingPayloadTrailer, int64(100), false,
			payloadClass{kind: payloadChunked, trailingChecksum: true, trailerSignature: true}, nil),
		Entry("streaming unsigned chunks with a trailer", streamingUnsignedTrailer, int64(100), false,
			payloadClass{kind: payloadChunked, unsignedPayload: true, trailingChecksum: true, unsignedChunked: true}, nil),
		Entry("streaming form with no body is empty", streamingPayload, int64(0), false, payloadClass{kind: payloadNone}, nil),
		Entry("UNSIGNED-PAYLOAD-TRAILER without STREAMING falls to a single completer", "UNSIGNED-PAYLOAD-TRAILER", int64(5), false,
			payloadClass{kind: payloadSingle, unsignedPayload: true, trailingChecksum: true}, nil),
	)

	DescribeTable("acceptedBy is get_auth_data_v4's per-op whitelist",
		func(kind payloadKind, forms op.PayloadForms, wantErr error) {
			err := payloadClass{kind: kind}.acceptedBy(forms)
			if wantErr == nil {
				Expect(err).NotTo(HaveOccurred())
				return
			}
			Expect(err).To(MatchError(wantErr))
		},
		Entry("no completer passes an op that takes nothing", payloadNone, op.PayloadForms(0), nil),
		Entry("a single chunk needs an op in the single-chunk whitelist", payloadSingle, op.PayloadForms(0), op.ErrNotImplemented),
		Entry("a single chunk on a whitelisted op", payloadSingle, op.PayloadSigned, nil),
		Entry("a single chunk on put_obj", payloadSingle, op.PayloadSigned|op.PayloadChunked, nil),
		Entry("aws-chunked on an op that takes only a single chunk", payloadChunked, op.PayloadSigned, op.ErrNotImplemented),
		Entry("aws-chunked on an op that takes nothing", payloadChunked, op.PayloadForms(0), op.ErrNotImplemented),
		Entry("aws-chunked on put_obj", payloadChunked, op.PayloadSigned|op.PayloadChunked, nil),
	)
})

var _ = Describe("hashReader", func() {
	const hello = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"

	It("passes a matching body through and ends with EOF", func() {
		r := newHashReader(strings.NewReader("hello"), hello)
		got, err := io.ReadAll(r)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(got)).To(Equal("hello"))
		n, err := r.Read(make([]byte, 1))
		Expect(n).To(BeZero())
		Expect(err).To(Equal(io.EOF))
	})

	It("reports a mismatch on the read that reaches EOF, and keeps reporting it", func() {
		r := newHashReader(iotest.OneByteReader(strings.NewReader("hellp")), hello)
		got, err := io.ReadAll(r)
		Expect(string(got)).To(Equal("hellp"), "the bytes are delivered before the verdict, as radosgw's completer runs after the body")
		Expect(err).To(MatchError(op.ErrContentSHA256Mismatch))
		_, err = r.Read(make([]byte, 1))
		Expect(err).To(MatchError(op.ErrContentSHA256Mismatch))
	})

	It("compares the hex byte for byte, so an uppercase expected value never matches", func() {
		r := newHashReader(strings.NewReader("hello"), strings.ToUpper(hello))
		_, err := io.ReadAll(r)
		Expect(err).To(MatchError(op.ErrContentSHA256Mismatch))
	})
})

var _ = Describe("completer", func() {
	cfg := DefaultConfig()

	signedPut := func(body, hash string, extra map[string]string) *requestView {
		req := httptest.NewRequest(http.MethodPut, "http://s3.example.com/b/k", strings.NewReader(body))
		if hash != "" {
			req.Header.Set("X-Amz-Content-Sha256", hash)
		}
		for k, v := range extra {
			req.Header.Set(k, v)
		}
		sdkSign(req, cmp.Or(hash, unsignedPayload))
		return newRequestView(req)
	}

	It("returns no reader and the request length for an unsigned payload", func() {
		rv := signedPut("hello", "", nil)
		d, err := authDataV4(rv, false, &cfg, signingTime)
		Expect(err).NotTo(HaveOccurred())
		body, n, err := d.completer(rv, testSecret)
		Expect(err).NotTo(HaveOccurred())
		Expect(body).To(BeNil())
		Expect(n).To(Equal(int64(5)))
	})

	It("returns a hash reader over the request body for a signed single chunk", func() {
		const hello = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
		rv := signedPut("hello", hello, nil)
		d, err := authDataV4(rv, false, &cfg, signingTime)
		Expect(err).NotTo(HaveOccurred())
		body, n, err := d.completer(rv, testSecret)
		Expect(err).NotTo(HaveOccurred())
		Expect(n).To(Equal(int64(5)))
		got, err := io.ReadAll(body)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(got)).To(Equal("hello"))
	})

	DescribeTable("requires a usable x-amz-decoded-content-length for a chunked payload",
		func(decoded string, wantErr error) {
			extra := map[string]string{}
			if decoded != "-" {
				extra["X-Amz-Decoded-Content-Length"] = decoded
			}
			rv := signedPut("0;chunk-signature=x\r\n\r\n", streamingPayload, extra)
			d, err := authDataV4(rv, false, &cfg, signingTime)
			Expect(err).NotTo(HaveOccurred())
			_, _, err = d.completer(rv, testSecret)
			Expect(err).To(MatchError(wantErr))
		},
		Entry("missing", "-", op.ErrInvalidArgument),
		Entry("negative", "-1", op.ErrInvalidArgument),
		Entry("not a number", "abc", op.ErrInvalidArgument),
	)
})
```

Until Task 6, a chunked payload with a valid decoded length returns `op.ErrNotImplemented` from `completer`; the table above only covers the invalid lengths so it stays green across Tasks 5 and 6.

- [ ] **Step 2: Run the specs to verify they fail**

Run: `go test -tags=ceph_preview ./internal/auth/...`
Expected: compile errors, `classifyPayload`, `acceptedBy`, `newHashReader`, `completer` undefined.

- [ ] **Step 3: Implement `payload.go` and wire it into `authDataV4`**

```go
package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"strconv"
	"strings"

	"github.com/jhoblitt/rgw-go/internal/op"
)

const ( /* the constants from Interfaces */ )

type payloadKind uint8

const (
	payloadNone payloadKind = iota
	payloadSingle
	payloadChunked
)

type payloadClass struct {
	kind                                                                payloadKind
	unsignedPayload, trailingChecksum, unsignedChunked, trailerSignature bool
}

func classifyPayload(rv *requestView, hash string) (payloadClass, error) {
	empty := rv.contentLength() == 0 && !rv.chunkedTE()
	unsigned := strings.Contains(hash, unsignedPayload)
	if empty && !unsigned && hash != emptyPayloadHash {
		return payloadClass{}, fmt.Errorf("%w: empty payload with a non-empty checksum", op.ErrContentSHA256Mismatch)
	}
	pc := payloadClass{
		unsignedPayload:  unsigned,
		trailingChecksum: strings.HasSuffix(hash, "TRAILER"),
		unsignedChunked:  hash == streamingUnsignedTrailer,
		trailerSignature: hash == streamingPayloadTrailer,
	}
	switch {
	case hash == unsignedPayload, unsigned && !pc.trailingChecksum, empty:
		return payloadClass{kind: payloadNone}, nil
	case !strings.HasPrefix(hash, "STREAMING-"):
		pc.kind = payloadSingle
	default:
		pc.kind = payloadChunked
	}
	return pc, nil
}

// acceptedBy refuses a payload form the op does not take, as radosgw's
// abstractor does once the empty-payload rule has passed: a single chunk
// only for the op types of its whitelist (rgw_rest_s3.cc:5899-5944 at
// v19.2.6, :6466-6515 at v20.2.4), a streamed payload only for
// RGW_OP_PUT_OBJ (:5957-5969, :6528-6540). The throw is ERR_NOT_IMPLEMENTED.
func (pc payloadClass) acceptedBy(forms op.PayloadForms) error {
	switch {
	case pc.kind == payloadSingle && forms&op.PayloadSigned == 0:
		return fmt.Errorf("%w: aws4 completion for this operation", op.ErrNotImplemented)
	case pc.kind == payloadChunked && forms&op.PayloadChunked == 0:
		return fmt.Errorf("%w: aws4 completion for this operation in streaming mode", op.ErrNotImplemented)
	}
	return nil
}

type hashReader struct {
	src      io.Reader
	h        hash.Hash
	expected string
	err      error
}

func newHashReader(src io.Reader, expected string) *hashReader {
	return &hashReader{src: src, h: sha256.New(), expected: expected}
}

func (r *hashReader) Read(p []byte) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	n, err := r.src.Read(p)
	r.h.Write(p[:n])
	if errors.Is(err, io.EOF) {
		if hex.EncodeToString(r.h.Sum(nil)) != r.expected {
			err = fmt.Errorf("%w: payload sha256 differs from x-amz-content-sha256", op.ErrContentSHA256Mismatch)
		}
		r.err = err
	}
	return n, err
}

func (d *authData) completer(rv *requestView, secret string) (io.Reader, int64, error) {
	switch d.payload.kind {
	case payloadSingle:
		return newHashReader(rv.req.Body, d.payloadHash), rv.contentLength(), nil
	case payloadChunked:
		v, ok := rv.header(decodedContentLengthHeader)
		if !ok {
			return nil, 0, fmt.Errorf("%w: aws-chunked payload without %s", op.ErrInvalidArgument, decodedContentLengthHeader)
		}
		decoded, err := strconv.ParseInt(trimSpace(v), 10, 64)
		if err != nil || decoded < 0 {
			return nil, 0, fmt.Errorf("%w: malformed %s", op.ErrInvalidArgument, decodedContentLengthHeader)
		}
		return nil, 0, op.ErrNotImplemented
	default:
		return nil, rv.contentLength(), nil
	}
}
```

In `authDataV4`, right after `payloadHash := expectedPayloadHash(rv)`:

```go
	pc, err := classifyPayload(rv, payloadHash)
	if err != nil {
		return nil, err
	}
```

and `payload: pc` in the `authData` literal.

- [ ] **Step 4: Run the specs and the gate; commit**

Run: `go test -tags=ceph_preview ./internal/auth/... && make check`
Expected: PASS.

```sh
git add internal/auth
git commit -m "feat(auth): classify SigV4 payloads, refuse the forms an op does not take, verify single-chunk hashes"
```

---

### Task 6: aws-chunked reader: signed chunks, the final chunk, strict chunk sizes and the 1024-byte trailer bound

**Files:**
- Create: `internal/auth/chunked.go`, `internal/auth/chunked_test.go`, `internal/auth/testdata/chunked/aws-docs-put-object.json`
- Modify: `internal/auth/payload.go` (`completer` returns `newChunkedReader` for `payloadChunked`), `internal/auth/vectors_test.go` (the chunked vector runner), `docs/exclusions.md` (coexistence section: the two differences of D-A7, Step 6)

**Interfaces:**
- Consumes: Tasks 3 and 5 (`signingKeyV4`, `hmacSHA256`, `sha256Hex`, `payloadClass`, `authData.completer`).
- Produces:

```go
package auth

const (
	payloadChunkAlgorithm = "AWS4-HMAC-SHA256-PAYLOAD" // AWS4_HMAC_SHA256_PAYLOAD_STR
	trailerAlgorithm      = "AWS4-HMAC-SHA256-TRAILER"
	chunkSigPrefix        = ";chunk-signature="
	chunkSigLen           = 64                                          // ChunkMeta::SIG_SIZE
	maxChunkHeader        = 2 + 16 + len(chunkSigPrefix) + chunkSigLen + 2 // ChunkMeta::META_MAX_SIZE = 101 (rgw_auth_s3.h:315-316): "\r\n" + 16 hex digits + ";chunk-signature=" + 64 + "\r\n"
	maxChunkSizeDigits    = 16                                          // sixteen hex digits hold 2^64-1; radosgw's strtoull saturates longer sizes (rgw_auth_s3.cc:1127, tracker #81123)
	maxTrailerSection     = 1024                                        // counted like radosgw's trailer buffer, from the CRLF ending the last data chunk; see finish
	chunkedBufferSize     = 4096                                        // the bufio buffer; bulk reads bypass it
)

// chunkedParams is what AWSv4ComplMulti::create receives (rgw_auth_s3.cc:1696-1720).
type chunkedParams struct {
	date, scope   string      // the request's, as sent
	seedSignature string      // the request signature: the chain's first previous-signature
	signingKey    []byte      // signingKeyV4(secret, scope)
	class         payloadClass
	trailerNames  []string    // x-amz-trailer split on ","; only these headers enter the trailer map
}

// chunkedReader is AWSv4ComplMulti as an io.Reader: it strips the aws-chunked
// framing, hashes each chunk's data, verifies a chunk's declared signature when
// its data has been consumed (radosgw does it when the next header is parsed,
// rgw_auth_s3.cc:1284-1291, and for the last data chunk in complete(),
// :1555-1564), computes the final zero-length chunk's signature for the
// trailer chain (:1566-1575), reads the trailer section within a 1024-byte
// bound, and reports the verdict from the Read that ends the payload, in
// place of io.EOF.
// Memory is O(1): a fixed bufio buffer, a chunk header of at most 101 bytes
// sliced from it, a 1025-byte trailer array and one SHA-256 state; chunk
// sizes and the decoded length are counters, never allocation sizes.
type chunkedReader struct{ /* br *bufio.Reader; p chunkedParams; prevSig, declared string; remaining uint64; pending, first, done bool; h hash.Hash; err error; consumedFinal int */ }
func newChunkedReader(src io.Reader, p chunkedParams) *chunkedReader
func (r *chunkedReader) Read(p []byte) (int, error)

// chunkStringToSign is calc_chunk_signature's string (rgw_auth_s3.cc:1210-1228).
func chunkStringToSign(date, scope, prevSig, dataHashHex string) string

// parseChunkSize reads a chunk size: one to sixteen hex digits, nothing else.
func parseChunkSize(s []byte) (uint64, bool)
```

Errors (`rgw::io::Exception` codes `recv_body` returns, [`rgw_rest.cc:812-821`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L812-L821)): framing, a malformed chunk size included → `op.ErrInvalidArgument` (EINVAL); a chunk or trailer signature → `op.ErrSignatureDoesNotMatch`; a trailer section over the 1024-byte bound → `op.ErrLimitExceeded`, 409 (D-A7); a stream that ends early → `io.ErrUnexpectedEOF`. Each is sticky.

Framing, transcribed from `ChunkMeta::create_next` ([`rgw_auth_s3.cc:1111-1208`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_s3.cc#L1111-L1208)) and `complete()` ([`:1555-1694`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_s3.cc#L1555-L1694)):

- The first chunk header starts the stream; every later header is preceded by the `\r\n` that ends the previous chunk's data (radosgw folds it into `strtoull`'s whitespace skipping; a missing CRLF is `EINVAL` here, where radosgw would misparse and fail later).
- A header line ends in `\r\n` and is at most 101 bytes including both CRLFs; longer, or no `\n` within the bufio buffer, is `EINVAL`.
- Signed form (`!class.unsignedChunked`): `<hex size>;chunk-signature=<64 hex>` exactly: no `;` ([`:1140-1145`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_s3.cc#L1140-L1145)), no `=` ([`:1149-1154`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_s3.cc#L1149-L1154)), a signature not 64 long ([`:1166-1169`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_s3.cc#L1166-L1169)) or a key other than `chunk-signature` are `EINVAL`. Unsigned form: `<hex size>` with anything after a `;` ignored ([`:1184-1207`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_s3.cc#L1184-L1207)).
- The size (the header before its `;`, or the whole header in the unsigned form) is `parseChunkSize`: one to sixteen hex digits, either case, and nothing else. Empty, a sign, a blank, a `0x` prefix, a byte after the digits, a non-hex byte or seventeen digits and more (leading zeros included) is `EINVAL`, before a byte of that chunk is delivered. This is D-A7's strict hex: radosgw's `strtoull` ([`:1127-1132`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_s3.cc#L1127-L1132)) rejects only a size with no digit at all and takes every other form, wrapping `-1` and saturating overlong values to 2^64-1 (tracker #81123). Sixteen digits bound the value to 2^64-1, so the parse cannot overflow.
- A data chunk delivers exactly `size` bytes to the caller as they arrive, hashing them (`calc_hash_sha256_update_stream`); the source ending first is `io.ErrUnexpectedEOF`.
- When a signed chunk's data is consumed and the next header is parsed (or the final chunk is reached), its declared signature is compared with `hex(HMAC(signingKey, chunkStringToSign(date, scope, prevSig, hex(sha256(data)))))`; a mismatch is `ErrSignatureDoesNotMatch` ([`:1230-1273`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_s3.cc#L1230-L1273)); a match makes the declared signature the new `prevSig`.
- Size 0 is the final chunk. Its declared signature is parsed and NOT compared (`:1645-1655`, D-A7). The final chunk signature `F = hex(HMAC(signingKey, chunkStringToSign(date, scope, prevSig, emptyPayloadHash)))` is computed for the trailer chain ([`:1566-1575`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_s3.cc#L1566-L1575)). In unsigned-chunked mode nothing is verified ([`:1238-1240`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_s3.cc#L1238-L1240)) and `F` is still computed (harmless).
- The trailer section is everything after the final chunk line up to EOF, bounded so that the final chunk line (with its leading CRLF) plus the section is at most 1024 bytes: one more byte available is `ErrLimitExceeded` (409). The count is the one radosgw's trailer buffer makes (`:1583-1606`), so the figures of D-A7 and tracker #81122 compare directly: a signed SHA256 trailer is 246 bytes, a signed SHA512 trailer 290. Task 6 reads and discards the section; Task 7 parses it.
- After the trailer section every `Read` returns `0, io.EOF`.

- [ ] **Step 1: Write the failing specs**

`internal/auth/chunked_test.go` (package `auth`). The signed stream builder uses the SDK's `StreamSigner`, whose event-stream string-to-sign with empty headers is exactly the S3 chunk formula (`hex(sha256(""))` in the headers slot; `aws/signer/v4/stream.go:76-79` in the module cache): 

```go
var streamTime = time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)

const (
	streamDate  = "20130524T000000Z"
	streamScope = "20130524/us-east-1/s3/aws4_request"
	streamSeed  = "4f232c4386841ef735655705268965c44a0e4690baa4adea153f7db9fa80a0a9"
)

// signedChunks frames data as signed aws-chunked chunks of at most size bytes
// (plus the final chunk) using the reference StreamSigner; the returned
// signatures are hex, one per chunk, the final chunk's last.
func signedChunks(data []byte, size int, seed string) (stream []byte, sigs []string) {
	seedRaw, err := hex.DecodeString(seed)
	Expect(err).NotTo(HaveOccurred())
	signer := v4.NewStreamSigner(aws.Credentials{AccessKeyID: testAccessKey, SecretAccessKey: testSecret}, "s3", "us-east-1", seedRaw)
	var b bytes.Buffer
	for first := true; first || len(data) > 0; first = false {
		n := min(size, len(data))
		chunk := data[:n]
		data = data[n:]
		sig, err := signer.GetSignature(context.Background(), nil, chunk, streamTime)
		Expect(err).NotTo(HaveOccurred())
		h := hex.EncodeToString(sig)
		sigs = append(sigs, h)
		if !first {
			b.WriteString("\r\n")
		}
		fmt.Fprintf(&b, "%x%s%s\r\n", n, chunkSigPrefix, h)
		b.Write(chunk)
		if n == 0 {
			break
		}
	}
	// the final chunk
	sig, err := signer.GetSignature(context.Background(), nil, nil, streamTime)
	Expect(err).NotTo(HaveOccurred())
	sigs = append(sigs, hex.EncodeToString(sig))
	fmt.Fprintf(&b, "\r\n0%s%s\r\n\r\n", chunkSigPrefix, sigs[len(sigs)-1])
	return b.Bytes(), sigs
}

func chunkedFor(src io.Reader, class payloadClass, trailer string) *chunkedReader {
	p := chunkedParams{date: streamDate, scope: streamScope, seedSignature: streamSeed, signingKey: signingKeyV4(testSecret, streamScope), class: class}
	if trailer != "" {
		p.trailerNames = strings.Split(trailer, ",")
	}
	return newChunkedReader(src, p)
}

var _ = Describe("chunkedReader, signed chunks", func() {
	signed := payloadClass{kind: payloadChunked}

	It("decodes a stream the reference signer produced and ends with EOF", func() {
		data := bytes.Repeat([]byte("abcdefgh"), 3000) // 24000 bytes, two 16 KiB chunks
		stream, _ := signedChunks(data, 16384, streamSeed)
		r := chunkedFor(bytes.NewReader(stream), signed, "")
		got, err := io.ReadAll(r)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(data))
		n, err := r.Read(make([]byte, 1))
		Expect(n).To(BeZero())
		Expect(err).To(Equal(io.EOF))
	})

	It("delivers a chunk's bytes before the rest of the stream arrives", func() {
		pr, pw := io.Pipe()
		r := chunkedFor(pr, signed, "")
		stream, _ := signedChunks([]byte("hello world"), 5, streamSeed)
		firstChunkEnd := bytes.Index(stream, []byte("\r\n")) + 2 + 5 // header line + 5 data bytes
		go func() { _, _ = pw.Write(stream[:firstChunkEnd]) }()
		buf := make([]byte, 64)
		n, err := r.Read(buf)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(buf[:n])).To(Equal("hello"), "no whole-body buffering")
		go func() { _, _ = pw.Write(stream[firstChunkEnd:]); _ = pw.Close() }()
		rest, err := io.ReadAll(r)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(rest)).To(Equal(" world"))
	})

	It("reports a corrupted chunk when its data has been consumed, after delivering it", func() {
		stream, _ := signedChunks([]byte("hello world"), 5, streamSeed)
		i := bytes.Index(stream, []byte("hello"))
		stream[i] = 'j'
		r := chunkedFor(bytes.NewReader(stream), signed, "")
		got, err := io.ReadAll(r)
		Expect(err).To(MatchError(op.ErrSignatureDoesNotMatch))
		Expect(string(got)).To(Equal("jello"), "the bad chunk was delivered, the next was not")
		_, err = r.Read(make([]byte, 1))
		Expect(err).To(MatchError(op.ErrSignatureDoesNotMatch), "sticky")
	})

	It("rejects a stream chained from a different seed signature", func() {
		stream, _ := signedChunks([]byte("hello"), 5, strings.Repeat("0", 64))
		_, err := io.ReadAll(chunkedFor(bytes.NewReader(stream), signed, ""))
		Expect(err).To(MatchError(op.ErrSignatureDoesNotMatch))
	})

	It("does not compare the final chunk's declared signature (radosgw never does)", func() {
		stream, sigs := signedChunks([]byte("hello"), 5, streamSeed)
		final := sigs[len(sigs)-1]
		stream = bytes.Replace(stream, []byte("0"+chunkSigPrefix+final), []byte("0"+chunkSigPrefix+strings.Repeat("f", 64)), 1)
		got, err := io.ReadAll(chunkedFor(bytes.NewReader(stream), signed, ""))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(got)).To(Equal("hello"))
	})

	It("accepts an empty payload as a lone final chunk", func() {
		stream, _ := signedChunks(nil, 5, streamSeed)
		got, err := io.ReadAll(chunkedFor(bytes.NewReader(stream), signed, ""))
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(BeEmpty())
	})

	DescribeTable("rejects malformed framing with InvalidArgument",
		func(stream string) {
			_, err := io.ReadAll(chunkedFor(strings.NewReader(stream), signed, ""))
			Expect(err).To(MatchError(op.ErrInvalidArgument))
		},
		Entry("no chunk-signature in signed mode", "5\r\nhello\r\n0\r\n\r\n"),
		Entry("signature not 64 hex", "5;chunk-signature=abc\r\nhello\r\n"),
		Entry("wrong key", "5;chunk-sig="+strings.Repeat("0", 64)+"\r\nhello\r\n"),
		Entry("size not hex", "zz;chunk-signature="+strings.Repeat("0", 64)+"\r\nhello\r\n"),
		Entry("a negative size, which strtoull wraps to 2^64-1", "-1;chunk-signature="+strings.Repeat("0", 64)+"\r\nhello\r\n"),
		Entry("seventeen hex digits, which strtoull saturates", "1ffffffffffffffff;chunk-signature="+strings.Repeat("0", 64)+"\r\nhello\r\n"),
		Entry("seventeen digits even when the value fits", "00000000000000005;chunk-signature="+strings.Repeat("0", 64)+"\r\nhello\r\n"),
		Entry("an empty size", ";chunk-signature="+strings.Repeat("0", 64)+"\r\nhello\r\n"),
		Entry("a 0x prefix", "0x5;chunk-signature="+strings.Repeat("0", 64)+"\r\nhello\r\n"),
		Entry("a leading blank", " 5;chunk-signature="+strings.Repeat("0", 64)+"\r\nhello\r\n"),
		Entry("a plus sign", "+5;chunk-signature="+strings.Repeat("0", 64)+"\r\nhello\r\n"),
		Entry("a byte after the digits", "5z;chunk-signature="+strings.Repeat("0", 64)+"\r\nhello\r\n"),
		Entry("missing CRLF after data", "5;chunk-signature="+strings.Repeat("0", 64)+"\r\nhello0;chunk-signature="+strings.Repeat("0", 64)+"\r\n"),
		Entry("header over 101 bytes", strings.Repeat("f", 40)+";chunk-signature="+strings.Repeat("0", 64)+"\r\n"),
		Entry("header without a newline in 4 KiB", strings.Repeat("f", 5000)),
	)

	DescribeTable("rejects a malformed size in the unsigned form, where radosgw stores a corrupted multi-chunk object",
		func(stream string) {
			unsignedChunked := payloadClass{kind: payloadChunked, unsignedPayload: true, trailingChecksum: true, unsignedChunked: true}
			got, err := io.ReadAll(chunkedFor(strings.NewReader(stream), unsignedChunked, ""))
			Expect(err).To(MatchError(op.ErrInvalidArgument))
			Expect(got).To(BeEmpty(), "nothing of the bad chunk is delivered")
		},
		Entry("a negative first size", "-1\r\nhello\r\n6\r\n world\r\n0\r\n\r\n"),
		Entry("seventeen hex digits", "1ffffffffffffffff\r\nhello\r\n6\r\n world\r\n0\r\n\r\n"),
		Entry("an empty size", "\r\nhello\r\n0\r\n\r\n"),
		Entry("a 0x prefix", "0x5\r\nhello\r\n0\r\n\r\n"),
	)

	It("reports a stream that ends inside a chunk or before the final chunk as an unexpected EOF, keeping memory flat", func() {
		huge := "ffffffffffffffff;chunk-signature=" + strings.Repeat("0", 64) + "\r\n0123456789"
		r := chunkedFor(strings.NewReader(huge), signed, "")
		got, err := io.ReadAll(r)
		Expect(err).To(MatchError(io.ErrUnexpectedEOF))
		Expect(string(got)).To(Equal("0123456789"), "the declared size is a counter, not an allocation")
		stream, _ := signedChunks([]byte("hello"), 5, streamSeed)
		cut := stream[:bytes.LastIndex(stream, []byte("\r\n0"))]
		_, err = io.ReadAll(chunkedFor(bytes.NewReader(cut), signed, ""))
		Expect(err).To(MatchError(io.ErrUnexpectedEOF))
	})

	It("completes a chunked payload through authData.completer with the decoded length", func() {
		data := []byte("hello world")
		stream, _ := signedChunks(data, 5, streamSeed)
		req := httptest.NewRequest(http.MethodPut, "http://s3.example.com/b/k", bytes.NewReader(stream))
		req.Header.Set("X-Amz-Content-Sha256", streamingPayload)
		req.Header.Set("X-Amz-Decoded-Content-Length", "11")
		sdkSign(req, streamingPayload)
		cfg := DefaultConfig()
		rv := newRequestView(req)
		d, err := authDataV4(rv, false, &cfg, signingTime)
		Expect(err).NotTo(HaveOccurred())
		// the reference signer produced this stream for the request signature it was seeded with,
		// so seed the completer's chain the same way
		d.signature = streamSeed
		d.v4.date, d.v4.scope = streamDate, streamScope
		body, n, err := d.completer(rv, testSecret)
		Expect(err).NotTo(HaveOccurred())
		Expect(n).To(Equal(int64(11)))
		got, err := io.ReadAll(body)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(data))
	})
})

var _ = Describe("chunkedReader, the trailer bound", func() {
	plain := payloadClass{kind: payloadChunked}

	// streamWithSection returns a signed one-chunk stream of "hello" whose
	// trailer count is total: from the CRLF that ends the data through the
	// closing CRLF, the span radosgw's 256-byte trailer buffer holds
	// (rgw_auth_s3.cc:1583-1606). The section is one unannounced header line,
	// which no trailer map takes and no trailer signature covers.
	streamWithSection := func(total int) []byte {
		stream, _ := signedChunks([]byte("hello"), 5, streamSeed)
		dataEnd := bytes.Index(stream, []byte("hello")) + len("hello")
		head := len(stream) - 2 - dataEnd // the CRLF and the final chunk line
		pad := total - head - len("x:\r\n\r\n")
		out := withTrailer(stream, "x:"+strings.Repeat("A", pad)+"\r\n\r\n")
		Expect(len(out) - dataEnd).To(Equal(total))
		return out
	}

	It("accepts a section of 1024 bytes, four times what radosgw's buffer holds", func() {
		got, err := io.ReadAll(chunkedFor(bytes.NewReader(streamWithSection(1024)), plain, ""))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(got)).To(Equal("hello"))
	})

	It("answers LimitExceeded at 1025 bytes, where radosgw truncates the section silently", func() {
		_, err := io.ReadAll(chunkedFor(bytes.NewReader(streamWithSection(1025)), plain, ""))
		Expect(err).To(MatchError(op.ErrLimitExceeded))
	})
})
```

`withTrailer` (replace the empty trailer section of a `signedChunks` stream, its final `"\r\n"`, with a section) is declared in `chunked_test.go` here rather than in Task 7's `trailer_test.go`, which uses it too. It copies, so two sections built from one stream never share bytes:

```go
// withTrailer replaces the empty trailer section of a signedChunks stream
// (the final "\r\n" after the 0 chunk line) with section.
func withTrailer(stream []byte, section string) []byte {
	out := make([]byte, 0, len(stream)-2+len(section))
	out = append(out, stream[:len(stream)-2]...)
	return append(out, section...)
}
```

- [ ] **Step 2: Write the AWS documentation vector and its runner**

`testdata/chunked/aws-docs-put-object.json` transcribes the "Example: PUT Object" of the AWS S3 API reference page *Signature Calculations for the Authorization Header: Transferring Payload in Multiple Chunks (Chunked Upload) (AWS Signature Version 4)*: access key `AKIAIOSFODNN7EXAMPLE`, secret `wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY`, date `20130524T000000Z`, scope `20130524/us-east-1/s3/aws4_request`, seed signature `4f232c4386841ef735655705268965c44a0e4690baa4adea153f7db9fa80a0a9`, payload 66560 bytes of `a`, chunks of 65536, 1024 and 0 bytes with signatures `ad80c730a21e5b8d04586a2213dd63b9a0e99e0e2307b0ade35a65485a288648`, `0055627c9e194cb4542bae2aa5492e3c1575bbb81b612b7d234b86a503ef5497` and `b6c6ea8a5354eaf15b3cb7646744f4275b71ea724fed81ceb9323e279d449df9`, `x-amz-decoded-content-length` 66560, `Content-Length` 66824:

```json
{
  "name": "aws-docs-put-object",
  "secret": "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
  "date": "20130524T000000Z",
  "scope": "20130524/us-east-1/s3/aws4_request",
  "seed_signature": "4f232c4386841ef735655705268965c44a0e4690baa4adea153f7db9fa80a0a9",
  "payload_repeat": {"byte": "a", "count": 66560},
  "chunk_sizes": [65536, 1024, 0],
  "chunk_signatures": ["ad80c730a21e5b8d04586a2213dd63b9a0e99e0e2307b0ade35a65485a288648", "0055627c9e194cb4542bae2aa5492e3c1575bbb81b612b7d234b86a503ef5497", "b6c6ea8a5354eaf15b3cb7646744f4275b71ea724fed81ceb9323e279d449df9"],
  "content_length": 66824
}
```

Before committing, recompute the three signatures independently and correct the file if the transcription slipped:

```sh
python3 - <<'PY'
import hashlib, hmac
secret=b"wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"; date="20130524T000000Z"; scope="20130524/us-east-1/s3/aws4_request"
def h(k,m): return hmac.new(k,m,hashlib.sha256).digest()
k=h(b"AWS4"+secret,b"20130524"); k=h(k,b"us-east-1"); k=h(k,b"s3"); k=h(k,b"aws4_request")
empty=hashlib.sha256(b"").hexdigest()
def sig(prev,data): return hmac.new(k,"\n".join(["AWS4-HMAC-SHA256-PAYLOAD",date,scope,prev,empty,hashlib.sha256(data).hexdigest()]).encode(),hashlib.sha256).hexdigest()
s1=sig("4f232c4386841ef735655705268965c44a0e4690baa4adea153f7db9fa80a0a9",b"a"*65536); s2=sig(s1,b"a"*1024); s3=sig(s2,b"")
print(s1); print(s2); print(s3)
PY
```

The runner in `vectors_test.go` builds the stream from the vector (`<size hex>;chunk-signature=<sig>\r\n<data>\r\n` per chunk, the final `0;chunk-signature=<sig>\r\n\r\n`), asserts the framed length equals `content_length`, decodes it with `chunkedFor`-equivalent params from the vector, asserts the payload round-trips and EOF, and asserts `chunkStringToSign`+HMAC reproduces each recorded chunk signature from the previous one (the chain check independent of the reader).

- [ ] **Step 3: Run the specs to verify they fail**

Run: `go test -tags=ceph_preview ./internal/auth/...`
Expected: compile errors (`newChunkedReader`, `chunkStringToSign` undefined).

- [ ] **Step 4: Implement `chunked.go`**

```go
package auth

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"strings"

	"github.com/jhoblitt/rgw-go/internal/op"
)

const ( /* the constants from Interfaces */ )

type chunkedParams struct {
	date, scope, seedSignature string
	signingKey                 []byte
	class                      payloadClass
	trailerNames               []string
}

type chunkedReader struct {
	br            *bufio.Reader
	p             chunkedParams
	prevSig       string
	declared      string
	remaining     uint64
	pending       bool // a signed data chunk awaits verification
	first         bool
	done          bool
	consumedFinal int // bytes of the final chunk line and its leading CRLF, charged to the trailer bound
	h             hash.Hash
	err           error
}

func newChunkedReader(src io.Reader, p chunkedParams) *chunkedReader {
	return &chunkedReader{br: bufio.NewReaderSize(src, chunkedBufferSize), p: p, prevSig: p.seedSignature, first: true, h: sha256.New()}
}

func chunkStringToSign(date, scope, prevSig, dataHashHex string) string {
	return strings.Join([]string{payloadChunkAlgorithm, date, scope, prevSig, emptyPayloadHash, dataHashHex}, "\n")
}

func (r *chunkedReader) chunkSignature(dataHashHex string) string {
	return hex.EncodeToString(hmacSHA256(r.p.signingKey, []byte(chunkStringToSign(r.p.date, r.p.scope, r.prevSig, dataHashHex))))
}

func (r *chunkedReader) fail(err error) (int, error) {
	r.err = err
	return 0, err
}

func (r *chunkedReader) Read(p []byte) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	if r.done {
		return 0, io.EOF
	}
	if len(p) == 0 {
		return 0, nil
	}
	for r.remaining == 0 {
		if err := r.nextChunk(); err != nil {
			return r.fail(err)
		}
		if r.done {
			return 0, io.EOF
		}
	}
	if uint64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.br.Read(p)
	if !r.p.class.unsignedChunked {
		r.h.Write(p[:n])
	}
	r.remaining -= uint64(n)
	if err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		r.err = err
		return n, err
	}
	return n, nil
}

// nextChunk consumes the CRLF that ends the previous chunk, verifies that
// chunk, and parses the next header; on the final chunk it runs finish.
func (r *chunkedReader) nextChunk() error {
	if !r.first {
		var crlf [2]byte
		if _, err := io.ReadFull(r.br, crlf[:]); err != nil {
			return unexpected(err)
		}
		if crlf != [2]byte{'\r', '\n'} {
			return fmt.Errorf("%w: aws-chunked data not followed by CRLF", op.ErrInvalidArgument)
		}
	}
	if r.pending {
		if r.declared != r.chunkSignature(hex.EncodeToString(r.h.Sum(nil))) {
			return fmt.Errorf("%w: aws-chunked chunk signature mismatch", op.ErrSignatureDoesNotMatch)
		}
		r.prevSig = r.declared
		r.h.Reset()
		r.pending = false
	}
	line, err := r.br.ReadSlice('\n')
	if err != nil {
		if errors.Is(err, bufio.ErrBufferFull) {
			return fmt.Errorf("%w: aws-chunked header too long", op.ErrInvalidArgument)
		}
		return unexpected(err)
	}
	if len(line) > maxChunkHeader || len(line) < 2 || line[len(line)-2] != '\r' {
		return fmt.Errorf("%w: malformed aws-chunked header", op.ErrInvalidArgument)
	}
	meta := line[:len(line)-2]
	sizeStr, declared := meta, ""
	if i := bytes.IndexByte(meta, ';'); i >= 0 {
		sizeStr = meta[:i]
		if !r.p.class.unsignedChunked {
			rest := meta[i:]
			if !bytes.HasPrefix(rest, []byte(chunkSigPrefix)) || len(rest) != len(chunkSigPrefix)+chunkSigLen {
				return fmt.Errorf("%w: malformed aws-chunked chunk signature", op.ErrInvalidArgument)
			}
			declared = string(rest[len(chunkSigPrefix):])
		}
	} else if !r.p.class.unsignedChunked {
		return fmt.Errorf("%w: aws-chunked header without a chunk signature", op.ErrInvalidArgument)
	}
	size, ok := parseChunkSize(sizeStr)
	if !ok {
		return fmt.Errorf("%w: malformed aws-chunked size", op.ErrInvalidArgument)
	}
	if size == 0 {
		r.consumedFinal = len(line)
		if !r.first {
			r.consumedFinal += 2
		}
		return r.finish(declared)
	}
	r.remaining, r.declared, r.pending, r.first = size, declared, !r.p.class.unsignedChunked, false
	return nil
}

func unexpected(err error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}

// parseChunkSize reads a chunk size as one to sixteen hex digits and nothing
// else. radosgw uses strtoull (rgw_auth_s3.cc:1127-1132 at v19.2.6,
// :1104-1109 at v20.2.4), which skips blanks, takes a sign, a 0x prefix and
// trailing bytes, and turns "-1" or a seventeen-digit size into 2^64-1, so
// the next chunk's framing is read as data (tracker #81123).
func parseChunkSize(s []byte) (uint64, bool) {
	if len(s) == 0 || len(s) > maxChunkSizeDigits {
		return 0, false
	}
	var n uint64
	for _, c := range s {
		var d byte
		switch {
		case '0' <= c && c <= '9':
			d = c - '0'
		case 'a' <= c && c <= 'f':
			d = c - 'a' + 10
		case 'A' <= c && c <= 'F':
			d = c - 'A' + 10
		default:
			return 0, false
		}
		n = n<<4 | uint64(d)
	}
	return n, true
}

// finish is complete() after the last data chunk was verified: the final
// chunk signature, then the trailer section. radosgw reads the section into
// a 256-byte buffer but asks for at most 256-pos-1 bytes per read, so its
// size check never fires and a longer section is cut short: a signed one
// then fails its trailer signature (rgw_auth_s3.cc:1583-1614 at v19.2.6,
// :1560-1591 at v20.2.4; tracker #81122). The bound here is maxTrailerSection
// bytes counted the same way, from the CRLF that ends the last data chunk,
// and a longer section is the LimitExceeded that radosgw's dead check names.
func (r *chunkedReader) finish(_ string) error {
	finalSig := r.chunkSignature(emptyPayloadHash)
	budget := maxTrailerSection - r.consumedFinal
	var buf [maxTrailerSection + 1]byte
	n, err := io.ReadFull(r.br, buf[:budget+1])
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return err
	}
	if n > budget {
		return fmt.Errorf("%w: aws-chunked trailer section exceeds %d bytes", op.ErrLimitExceeded, maxTrailerSection)
	}
	if err := r.trailers(buf[:n], finalSig); err != nil {
		return err
	}
	r.done = true
	return nil
}
```

`io.ReadFull` on `buf[:budget+1]` returns `io.ErrUnexpectedEOF` when the stream ends inside the budget, which is the normal case; only `n > budget` (the stream still had bytes) is the limit error. In Task 6 `trailers` is `func (r *chunkedReader) trailers([]byte, string) error { return nil }`.

In `payload.go`'s `completer`, replace the `ErrNotImplemented` return with:

```go
		var names []string
		if t, ok := rv.header("x-amz-trailer"); ok {
			names = strings.Split(t, ",")
		}
		return newChunkedReader(rv.req.Body, chunkedParams{
			date: d.v4.date, scope: d.v4.scope, seedSignature: d.signature,
			signingKey: signingKeyV4(secret, d.v4.scope), class: d.payload, trailerNames: names,
		}), decoded, nil
```

- [ ] **Step 5: Run the specs and the gate; commit the code**

Run: `go test -tags=ceph_preview ./internal/auth/... && make check`
Expected: PASS.

```sh
git add internal/auth
git commit -m "feat(auth): verify aws-chunked payloads chunk by chunk as they stream"
```

- [ ] **Step 6: Record the two differences in `docs/exclusions.md`; commit**

This task introduces both behaviours of D-A7 that differ from radosgw, so it records them, in the same PR. In `docs/exclusions.md`, "Coexistence obligations independent of any exclusion", insert after the bullet **"Signature quirks are radosgw's, not AWS's."**:

```markdown
- **aws-chunked trailer sections are accepted up to 1 KiB, and a longer
  one is refused with 409.** radosgw reads an aws-chunked upload's trailer
  section, counted from the CRLF that ends the last data chunk through the
  closing CRLF, into a 256-byte buffer whose reads stop one byte short of
  it, so its own size check, which would answer 409 LimitExceeded, never
  fires and a longer section is cut off silently (`rgw_auth_s3.cc:1583-1614`
  at v19.2.6). On both floors a signed section longer than 257 bytes then
  fails with 403 SignatureDoesNotMatch, every signed SHA512 trailer
  (290 bytes) included, and on Tentacle an unsigned section whose checksum
  line falls past the cut is accepted without that checksum being compared
  (tracker #81122). rgw-go counts the section the same way, accepts up to
  1024 bytes, which holds a signed SHA512 trailer with room to spare, and
  answers 409 LimitExceeded above that. A signed trailer that radosgw
  refuses with 403 therefore succeeds through rgw-go.
- **aws-chunked chunk sizes are one to sixteen hex digits.** radosgw parses
  a chunk size with an unchecked `strtoull` (`rgw_auth_s3.cc:1127-1132` at
  v19.2.6), which skips blanks and accepts a sign, a `0x` prefix, trailing
  bytes and more than sixteen digits, turning `-1` or seventeen hex digits
  into 2^64-1. On both floors an unsigned multi-chunk upload whose first
  chunk carries such a size is then stored corrupted with 200, because the
  bad chunk swallows the next chunk's framing; a signed multi-chunk one
  fails with 400 XAmzContentSHA256Mismatch and a single-chunk one is stored
  intact (tracker #81123). rgw-go answers any other size with 400
  InvalidArgument and stores nothing.
```

```sh
git add docs/exclusions.md
git commit -m "docs(exclusions): record the aws-chunked trailer bound and strict chunk sizes"
```

The PR description names both entries, and the change is reported so the rgw-rs session is told.

---

### Task 7: aws-chunked trailers: `…-PAYLOAD-TRAILER` and `STREAMING-UNSIGNED-PAYLOAD-TRAILER`, the signed SHA512 trailer

**Files:**
- Modify: `internal/auth/chunked.go` (`trailers`, `trailerStringToSign`)
- Create: `internal/auth/trailer_test.go`

**Interfaces:**
- Consumes: Task 6.
- Produces:

```go
// trailerStringToSign is calc_v4_trailer_signature's string (rgw_auth_s3.cc:1391-1418):
// AWS4-HMAC-SHA256-TRAILER, date, scope, the final chunk signature, and
// hex(sha256(canonical trailer headers)), where the canonical headers are
// "name:value\n" for each header named in x-amz-trailer, sorted by name,
// values untrimmed (get_canon_amz_hdrs, :66-86).
func trailerStringToSign(date, scope, finalChunkSig, canonicalTrailers string) string

// trailers parses the trailer section (complete(), :1616-1690): lines of
// "name:value" ending in CRLF; a line whose name is x-amz-trailer-signature
// is the declared trailer signature; a line whose name is in trailerNames
// enters the trailer map; every other line is ignored; a trailer that
// x-amz-trailer did not announce is ignored too. With class.trailerSignature
// the declared signature must be present and equal to the computed one, else
// ErrSignatureDoesNotMatch (:1686-1690); without it the section is parsed and
// nothing is required. The checksum values are neither validated nor kept:
// checksums are not implemented yet.
func (r *chunkedReader) trailers(section []byte, finalChunkSig string) error
```

Note the two forms that reach here: `STREAMING-AWS4-HMAC-SHA256-PAYLOAD-TRAILER` (signed chunks, `trailingChecksum` and `trailerSignature` set) and `STREAMING-UNSIGNED-PAYLOAD-TRAILER` (`unsignedChunked`: headers `<hex size>\r\n`, no chunk signatures, no trailer signature required; the aws-sdk-go-v2 S3 client sends this form whenever it computes a request checksum). A plain `STREAMING-AWS4-HMAC-SHA256-PAYLOAD` stream may carry a trailer section too; it is parsed and not required.

- [ ] **Step 1: Write the failing specs**

`internal/auth/trailer_test.go` (package `auth`):

```go
// trailerSig computes the trailer signature the way the AWS documentation
// describes it, independently of chunked.go's helpers.
func trailerSig(finalChunkSig string, canonical string) string {
	sum := sha256.Sum256([]byte(canonical))
	sts := "AWS4-HMAC-SHA256-TRAILER\n" + streamDate + "\n" + streamScope + "\n" + finalChunkSig + "\n" + hex.EncodeToString(sum[:])
	return hex.EncodeToString(hmacSHA256(signingKeyV4(testSecret, streamScope), []byte(sts)))
}

var _ = Describe("chunkedReader, trailers", func() {
	signedTrailer := payloadClass{kind: payloadChunked, trailingChecksum: true, trailerSignature: true}
	unsignedTrailer := payloadClass{kind: payloadChunked, unsignedPayload: true, trailingChecksum: true, unsignedChunked: true}
	plain := payloadClass{kind: payloadChunked}
	const crc = "x-amz-checksum-crc32c:sOO8/Q==\r\n"

	It("verifies a signed trailer and delivers the payload", func() {
		stream, sigs := signedChunks([]byte("hello"), 5, streamSeed)
		final := sigs[len(sigs)-1]
		stream = withTrailer(stream, crc+"x-amz-trailer-signature:"+trailerSig(final, "x-amz-checksum-crc32c:sOO8/Q==\n")+"\r\n\r\n")
		got, err := io.ReadAll(chunkedFor(bytes.NewReader(stream), signedTrailer, "x-amz-checksum-crc32c"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(got)).To(Equal("hello"))
	})

	It("rejects a wrong or missing trailer signature when the form requires one", func() {
		stream, _ := signedChunks([]byte("hello"), 5, streamSeed)
		bad := withTrailer(stream, crc+"x-amz-trailer-signature:"+strings.Repeat("0", 64)+"\r\n\r\n")
		_, err := io.ReadAll(chunkedFor(bytes.NewReader(bad), signedTrailer, "x-amz-checksum-crc32c"))
		Expect(err).To(MatchError(op.ErrSignatureDoesNotMatch))
		missing := withTrailer(stream, crc+"\r\n")
		_, err = io.ReadAll(chunkedFor(bytes.NewReader(missing), signedTrailer, "x-amz-checksum-crc32c"))
		Expect(err).To(MatchError(op.ErrSignatureDoesNotMatch))
	})

	It("signs only the trailers x-amz-trailer announced, in name order", func() {
		stream, sigs := signedChunks([]byte("hello"), 5, streamSeed)
		final := sigs[len(sigs)-1]
		section := "x-amz-checksum-sha256:LPJNul+wow4m6DsqxbninhsWHlwfp0JecwQzYpOLmCQ=\r\n" + crc + "x-amz-unlisted:ignored\r\n"
		canonical := "x-amz-checksum-crc32c:sOO8/Q==\nx-amz-checksum-sha256:LPJNul+wow4m6DsqxbninhsWHlwfp0JecwQzYpOLmCQ=\n"
		stream = withTrailer(stream, section+"x-amz-trailer-signature:"+trailerSig(final, canonical)+"\r\n\r\n")
		_, err := io.ReadAll(chunkedFor(bytes.NewReader(stream), signedTrailer, "x-amz-checksum-sha256,x-amz-checksum-crc32c"))
		Expect(err).NotTo(HaveOccurred())
	})

	It("signs an empty header set when x-amz-trailer is absent, as radosgw does", func() {
		stream, sigs := signedChunks([]byte("hello"), 5, streamSeed)
		stream = withTrailer(stream, crc+"x-amz-trailer-signature:"+trailerSig(sigs[len(sigs)-1], "")+"\r\n\r\n")
		_, err := io.ReadAll(chunkedFor(bytes.NewReader(stream), signedTrailer, ""))
		Expect(err).NotTo(HaveOccurred())
	})

	It("does not validate the checksum value and ignores a trailer on the plain streaming form", func() {
		stream, sigs := signedChunks([]byte("hello"), 5, streamSeed)
		wrongCRC := "x-amz-checksum-crc32c:AAAAAA==\r\n"
		signed := withTrailer(stream, wrongCRC+"x-amz-trailer-signature:"+trailerSig(sigs[len(sigs)-1], "x-amz-checksum-crc32c:AAAAAA==\n")+"\r\n\r\n")
		_, err := io.ReadAll(chunkedFor(bytes.NewReader(signed), signedTrailer, "x-amz-checksum-crc32c"))
		Expect(err).NotTo(HaveOccurred())
		unrequired := withTrailer(stream, wrongCRC+"x-amz-trailer-signature:"+strings.Repeat("0", 64)+"\r\n\r\n")
		_, err = io.ReadAll(chunkedFor(bytes.NewReader(unrequired), plain, "x-amz-checksum-crc32c"))
		Expect(err).NotTo(HaveOccurred())
	})

	It("decodes the unsigned aws-chunked form the Go SDK sends and requires no signatures", func() {
		stream := "5\r\nhello\r\n6\r\n world\r\n0\r\n" + crc + "\r\n"
		got, err := io.ReadAll(chunkedFor(strings.NewReader(stream), unsignedTrailer, "x-amz-checksum-crc32c"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(got)).To(Equal("hello world"))
		withExt := "5;ext=1\r\nhello\r\n0\r\n\r\n"
		got, err = io.ReadAll(chunkedFor(strings.NewReader(withExt), unsignedTrailer, ""))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(got)).To(Equal("hello"))
	})

	It("accepts a 290-byte signed SHA512 trailer section, which radosgw's 256-byte buffer cuts off", func() {
		stream, sigs := signedChunks([]byte("hello"), 5, streamSeed)
		const sha512 = "x-amz-checksum-sha512:m3HSJL1i83hdltRq0+o9czGb+8KJDKra4t/3JRlnPKcjI8PZm6XBHXx6zG4UuMXaDEZjR1wuXDre9G9zvN7AQw=="
		stream = withTrailer(stream, sha512+"\r\nx-amz-trailer-signature:"+trailerSig(sigs[len(sigs)-1], sha512+"\n")+"\r\n\r\n")
		dataEnd := bytes.Index(stream, []byte("hello")) + len("hello")
		Expect(len(stream) - dataEnd).To(Equal(290), "the CRLF, the final chunk line and the section, as radosgw counts them")
		got, err := io.ReadAll(chunkedFor(bytes.NewReader(stream), signedTrailer, "x-amz-checksum-sha512"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(got)).To(Equal("hello"))
	})
})
```

The SHA512 value is the real digest of `hello` (base64), though nothing validates it (D7).

- [ ] **Step 2: Run the specs to verify they fail**

Run: `go test -tags=ceph_preview ./internal/auth/...`
Expected: the signature specs fail (Task 6's `trailers` accepts everything); the SHA512 and unsigned-form specs already pass.

- [ ] **Step 3: Implement `trailers`**

```go
func trailerStringToSign(date, scope, finalChunkSig, canonicalTrailers string) string {
	return strings.Join([]string{trailerAlgorithm, date, scope, finalChunkSig, sha256Hex(canonicalTrailers)}, "\n")
}

func (r *chunkedReader) trailers(section []byte, finalChunkSig string) error {
	var declared string
	trailers := map[string]string{}
	for _, line := range bytes.Split(section, []byte("\r\n")) {
		name, value, ok := bytes.Cut(line, []byte(":"))
		if !ok {
			continue
		}
		switch n := string(name); {
		case n == "x-amz-trailer-signature":
			declared = string(value)
		case slices.Contains(r.p.trailerNames, n):
			trailers[n] = string(value)
		}
	}
	if !r.p.class.trailerSignature {
		return nil
	}
	var canonical strings.Builder
	for _, n := range slices.Sorted(maps.Keys(trailers)) {
		canonical.WriteString(n)
		canonical.WriteByte(':')
		canonical.WriteString(trailers[n])
		canonical.WriteByte('\n')
	}
	calc := hex.EncodeToString(hmacSHA256(r.p.signingKey, []byte(trailerStringToSign(r.p.date, r.p.scope, finalChunkSig, canonical.String()))))
	if declared == "" || subtle.ConstantTimeCompare([]byte(declared), []byte(calc)) != 1 {
		return fmt.Errorf("%w: aws-chunked trailer signature mismatch", op.ErrSignatureDoesNotMatch)
	}
	return nil
}
```

radosgw splits a trailer line at every `:` and takes the first two fields (`split_header`, [`rgw_auth_s3.cc:1470-1480`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_s3.cc#L1470-L1480)); a value containing `:` would lose its tail there, where `bytes.Cut` keeps it. Checksum values are base64 and never contain one.

- [ ] **Step 4: Run the specs and the gate; commit**

Run: `go test -tags=ceph_preview ./internal/auth/... && make check`
Expected: PASS.

```sh
git add internal/auth
git commit -m "feat(auth): verify aws-chunked trailer signatures"
```

---

### Task 8: SigV2 header and query-string authentication

**Files:**
- Create: `internal/auth/sigv2.go`, `internal/auth/sigv2_test.go`, `internal/auth/testdata/v2/*.json`
- Modify: `internal/auth/vectors_test.go` (the v2 runner)

**Interfaces:**
- Consumes: Task 2 (`requestView`, encodings), Task 3 (`authData`, `timeSkewOK`).
- Produces:

```go
package auth

// signedSubresourcesV2 is rgw_auth_s3.cc:31-60 in its order: the SigV2
// canonical resource appends the ones the request carries, in this order.
var signedSubresourcesV2 = []string{"acl", "cors", "delete", "encryption", "lifecycle", "location", "logging",
	"notification", "partNumber", "policy", "policyStatus", "publicAccessBlock", "requestPayment",
	"response-cache-control", "response-content-disposition", "response-content-encoding",
	"response-content-language", "response-content-type", "response-expires", "tagging", "torrent",
	"uploadId", "uploads", "versionId", "versioning", "versions", "website", "object-lock"}

// amzHeaderPrefixes is meta_prefixes (rgw_common.cc:413-420): a header with
// any of them is filed under x-amz- plus the rest of its name.
var amzHeaderPrefixes = []string{"x-amz-", "x-goog-", "x-dho-", "x-rgw-", "x-object-", "x-container-", "x-account-"}

func authDataV2(rv *requestView, cfg *Config, now time.Time) (*authData, error)                 // AWSGeneralAbstractor::get_auth_data_v2, rgw_rest_s3.cc:6033-6113
func canonicalStringV2(rv *requestView, presigned bool, dnsNames []string) (sts string, headerTime time.Time, err error) // rgw_create_s3_canonical_header, rgw_auth_s3.cc:130-260
func amzHeadersV2(rv *requestView) map[string]string                                             // req_info::init_meta_info's x_meta_map, rgw_common.cc:422-458
func qsMetaV2(rv *requestView) map[string]string                                                 // get_v2_qs_map, rgw_auth_s3.cc:175-187
func canonicalResourceV2(rv *requestView, dnsNames []string) string                              // get_canon_resource over info.request_uri, :91-124, with RGWREST::preprocess's bucket prefix
func hostBucket(host string, dnsNames []string) string                                           // rgw_find_host_in_domains + the CNAME fallback, rgw_rest.cc:260-287, :2136-2143
func validBucketName(s string) bool                                                              // RGWHandler_REST::validate_bucket_name, rgw_rest.cc:1815-1840
func looksLikeIP(s string) bool                                                                  // looks_like_ip_address, rgw_rest_s3.h:801-826
func signatureV2(secret, stringToSign string) (string, error)                                    // get_v2_signature, rgw_auth_s3.cc:1068-1093: base64(HMAC-SHA1); empty secret is EINVAL
```

The transcription:

- `authDataV2`: with an empty or absent `Authorization` the credentials are the query's `AWSAccessKeyId` and `Signature`, `Expires` is required and `now >= atoll(Expires)` (whole seconds) is `EPERM` with no message, an `x-amz-security-token` parameter present but empty is `EPERM`; otherwise the header after `"AWS "` is split at its LAST `:` into key and signature (no `:` leaves both empty, which Task 9 turns into `EINVAL`), and an `x-amz-security-token` header present but empty is `EPERM`. Then `canonicalStringV2`; its failure is `EPERM` ("failed to create the canonized auth header"); for header auth a skew over 15 minutes is `ERR_REQUEST_TIME_SKEWED`; presigned has no skew check. `sign` is `signatureV2`.
- `canonicalStringV2` ([`rgw_auth_s3.cc:193-260`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_s3.cc#L193-L260), then [`:130-169`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_s3.cc#L130-L169)): `Content-MD5` present but not base64 charset → failure; the four lines are `method`, `Content-MD5` (as sent, or empty), `Content-Type` (as sent, or empty), the date line; presigned: the date line is the `Expires` parameter and the query meta map is added; header auth: `x-amz-date` present → the date line is EMPTY and the request date is `x-amz-date`; else the `Date` header is both (missing → failure); the request date must parse as RFC 2616 or, failing that, ISO 8601 basic; a year before 1970 is a failure. Then `k:v\n` for every entry of the amz header map sorted by key, the same for the query meta map, then the canonical resource.
- `amzHeadersV2`: for every request header whose lower-cased name has one of `amzHeaderPrefixes`, the key is `x-amz-` + the lower-cased rest of the name (so `x-goog-meta-q` files under `x-amz-meta-q`, D-A7) and the value is the header's LAST value; when two header names map to the same key their values are joined with `,` after right-trimming the earlier one, in the order of their `HTTP_`-style env names (`RGWEnv`'s `std::map`); `x-amz-date` is in this map, which is how it enters the string-to-sign.
- `qsMetaV2`: every query parameter whose lower-cased name starts with `x-amz-meta-` (key lower-cased) and `x-amz-security-token`.
- `canonicalResourceV2`: `rv.rawPath` (the path as sent, encoding kept), prefixed with `/<bucket>` when `hostBucket(rv.hostNoPort(), dnsNames)` names one (`rgw_rest.cc:2154-2161`; a slash is inserted when the path does not start with one); then for each name of `signedSubresourcesV2` in order that is a key of `rv.subres`: `?` for the first, `&` after, the name, and `=value` when the value is non-empty. Only names in BOTH lists ever appear: `encryption` and `object-lock` are never in `rv.subres`; `policy` is, as a bare name, only when it is the first admin sub-resource the query mentioned (Task 2).
- `hostBucket`: names are `dnsNames` without empties, sorted (a `std::set`); the first name that is a case-insensitive suffix of the host wins: equal to the host → no bucket (path style); preceded by `.` → the prefix is the bucket; otherwise keep looking. No match: the host itself is the bucket when it is not an IP literal, `validBucketName` holds, and at least one name is configured.
- `validBucketName`: empty → true; shorter than 3 or longer than 255 bytes → false; a `/` or `0xff` byte → false.
- `looksLikeIP`: an IPv6 literal (`net.ParseIP` succeeds and the text has a `:`), or digits and exactly three `.`, each `.` preceded by a digit.
- `signatureV2`: empty secret → `op.ErrInvalidArgument`; else `base64(HMAC-SHA1(secret, stringToSign))`, compared byte for byte with the client's.

- [ ] **Step 1: Write the failing specs and vectors**

`internal/auth/sigv2_test.go` (package `auth`):

```go
func v2Sign(secret, sts string) string {
	h := hmac.New(sha1.New, []byte(secret))
	h.Write([]byte(sts))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

var _ = Describe("SigV2", func() {
	cfg := DefaultConfig()
	now := time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)

	It("verifies a header-signed request and rejects a wrong secret", func() {
		req := httptest.NewRequest(http.MethodGet, "http://s3.example.com/b/k?acl", nil)
		req.Header.Set("Date", "Sun, 30 Aug 2015 12:36:00 GMT")
		sts := "GET\n\n\nSun, 30 Aug 2015 12:36:00 GMT\n/b/k?acl"
		req.Header.Set("Authorization", "AWS "+testAccessKey+":"+v2Sign(testSecret, sts))
		d, err := authDataV2(newRequestView(req), &cfg, now)
		Expect(err).NotTo(HaveOccurred())
		Expect(d.accessKey).To(Equal(testAccessKey))
		Expect(d.stringToSign).To(Equal(sts))
		ok, err := d.verify(testSecret)
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())
		ok, err = d.verify("wrong")
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeFalse())
		_, err = d.verify("")
		Expect(err).To(MatchError(op.ErrInvalidArgument), "get_v2_signature throws EINVAL for an empty secret")
	})

	DescribeTable("hostBucket is rgw_find_host_in_domains with the CNAME fallback",
		func(host string, names []string, want string) { Expect(hostBucket(host, names)).To(Equal(want)) },
		Entry("subdomain", "bkt.s3.example.com", []string{"s3.example.com"}, "bkt"),
		Entry("case-insensitive suffix", "BKT.S3.Example.COM", []string{"s3.example.com"}, "BKT"),
		Entry("equal to a name is path style", "s3.example.com", []string{"s3.example.com"}, ""),
		Entry("cname fallback", "www.example.org", []string{"s3.example.com"}, "www.example.org"),
		Entry("ip literal is never a bucket", "10.0.0.1", []string{"s3.example.com"}, ""),
		Entry("ipv6 literal is never a bucket", "::1", []string{"s3.example.com"}, ""),
		Entry("too short for a bucket", "ab", []string{"s3.example.com"}, ""),
		Entry("no names configured", "bkt.s3.example.com", nil, ""),
		Entry("suffix without a dot keeps looking", "xs3.example.com", []string{"s3.example.com"}, "xs3.example.com"),
		Entry("first name in sorted order wins", "x.b.a", []string{"b.a", "a"}, "x.b"),
	)
})
```

The v2 runner in `vectors_test.go`: a vector under `testdata/v2/` carries `method`, `target`, `host`, `headers`, `presigned`, `dns_names`, `secret`, `now`, `expires` (presigned), `query_extra`, `signature` (negative cases) and `expect{string_to_sign, access_key, error}`. For header vectors it sets `Authorization: AWS <access_key>:<sig>`; for presigned it appends `AWSAccessKeyId=<access_key>&Expires=<expires>&Signature=<url-escaped sig>`; `<sig>` is `signature` when given, else `v2Sign(secret, expect.string_to_sign)`. It asserts `d.stringToSign == expect.string_to_sign`, `d.accessKey`, and `verify` true, or the `op.Error` code.

| file | rule |
|---|---|
| `v2-get-date-header.json` | `Date: Sun, 30 Aug 2015 12:36:00 GMT`: `GET\n\n\nSun, 30 Aug 2015 12:36:00 GMT\n/b/k` |
| `v2-put-md5-type-amz.json` | `PUT`, `Content-MD5: rL0Y20zC+Fzt72VPzMSk2A==`, `Content-Type: text/plain`, `X-Amz-Meta-B: 2`, `X-Amz-Meta-A: 1`, `X-Amz-Date: 20150830T123600Z`, `Date: Sun, 30 Aug 2015 12:36:00 GMT`: `PUT\nrL0Y20zC+Fzt72VPzMSk2A==\ntext/plain\n\nx-amz-date:20150830T123600Z\nx-amz-meta-a:1\nx-amz-meta-b:2\n/b/k` (x-amz-date empties the date line) |
| `v2-subresources-order.json` | target `/b/k?versionId=v1&acl&uploadId=u&partNumber=3&response-content-type=text%2Fplain&foo=bar&encryption`: resource `/b/k?acl&partNumber=3&response-content-type=text/plain&uploadId=u&versionId=v1` |
| `v2-policy-first-admin-subresource.json` | `/b?policy=x&quota=y`: `/b?policy`; and `v2-quota-before-policy.json`: `/b?quota=y&policy=x`: `/b` |
| `v2-virtual-host.json` | host `bkt.s3.example.com`, `dns_names: ["s3.example.com"]`, target `/k`: resource `/bkt/k` |
| `v2-virtual-host-cname.json` | host `www.example.org`, same names: `/www.example.org/k` |
| `v2-virtual-host-ip.json` | host `10.0.0.1`, same names: `/k` |
| `v2-raw-path-kept.json` | target `/b/k%20v+w`: `/b/k%20v+w` |
| `v2-goog-header-remapped.json` | `X-Goog-Meta-Q: 1` and `Date`: `…\nx-amz-meta-q:1\n/b/k` |
| `v2-duplicate-amz-header-last.json` | two `X-Amz-Meta-A` values `first`, `second`: `x-amz-meta-a:second` |
| `v2-iso-date-header.json` | `Date: 20150830T123600Z` (ISO basic accepted after RFC 2616 fails): date line is that value |
| `v2-presigned.json` | presigned, target `/b/k?x-amz-meta-x=1`, `expires: 1441000000`, now `2015-08-31T05:46:39Z`: `GET\n\n\n1441000000\nx-amz-meta-x:1\n/b/k` |
| `v2-presigned-expired.json` | now `2015-08-31T05:46:40Z` (== Expires): `AccessDenied`, no message |
| `v2-presigned-missing-expires.json` | `expires: ""`: `AccessDenied` |
| `v2-presigned-empty-token.json` | `query_extra: x-amz-security-token=`: `AccessDenied` |
| `v2-header-missing-date.json` | no `Date`, no `X-Amz-Date`: `AccessDenied` |
| `v2-header-skewed.json` | `Date` 16 minutes before now: `RequestTimeTooSkewed` |
| `v2-bad-md5-charset.json` | `Content-MD5: "!!!"`: `AccessDenied` |
| `v2-year-before-1970.json` | `Date: Thu, 01 Jan 1960 00:00:00 GMT`: `AccessDenied` |

- [ ] **Step 2: Run the specs to verify they fail**

Run: `go test -tags=ceph_preview ./internal/auth/...`
Expected: compile errors (`authDataV2`, `hostBucket` undefined).

- [ ] **Step 3: Implement `sigv2.go`**

```go
package auth

import (
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // SigV2 is HMAC-SHA1 by definition
	"encoding/base64"
	"fmt"
	"maps"
	"net"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jhoblitt/rgw-go/internal/op"
)

var ( /* signedSubresourcesV2, amzHeaderPrefixes as in Interfaces */ )

func authDataV2(rv *requestView, cfg *Config, now time.Time) (*authData, error) {
	denied := func(what string) error { return fmt.Errorf("%w: %s", op.ErrAccessDenied, what) }
	d := &authData{}
	if a, ok := rv.header("authorization"); !ok || a == "" {
		d.presigned = true
		d.accessKey, _ = rv.param("AWSAccessKeyId")
		d.signature, _ = rv.param("Signature")
		expires, _ := rv.param("Expires")
		if expires == "" {
			return nil, denied("presigned v2 url without Expires")
		}
		if now.Unix() >= atoll(expires) {
			return nil, denied("presigned v2 url expired")
		}
		if tok, present := rv.param("x-amz-security-token"); present && tok == "" {
			return nil, denied("empty security token")
		}
	} else {
		s := strings.TrimPrefix(a, "AWS ")
		if i := strings.LastIndexByte(s, ':'); i >= 0 {
			d.accessKey, d.signature = s[:i], s[i+1:]
		}
		if tok, present := rv.header("x-amz-security-token"); present && tok == "" {
			return nil, denied("empty security token")
		}
	}
	sts, headerTime, err := canonicalStringV2(rv, d.presigned, cfg.DNSNames)
	if err != nil {
		return nil, denied("failed to create the canonical string: " + err.Error())
	}
	if !d.presigned && !timeSkewOK(now, headerTime) {
		return nil, op.ErrRequestTimeTooSkewed
	}
	d.stringToSign = sts
	d.sign = func(secret string) (string, error) { return signatureV2(secret, sts) }
	return d, nil
}

func canonicalStringV2(rv *requestView, presigned bool, dnsNames []string) (string, time.Time, error) {
	md5, hasMD5 := rv.header("content-md5")
	if hasMD5 && !isBase64Charset(md5) {
		return "", time.Time{}, errors.New("content-md5 is not base64")
	}
	ctype, _ := rv.header("content-type")
	var (
		date       string
		headerTime time.Time
		qs         map[string]string
	)
	if presigned {
		qs = qsMetaV2(rv)
		date, _ = rv.param("Expires")
	} else {
		reqDate, ok := rv.header("x-amz-date")
		if !ok {
			if reqDate, ok = rv.header("date"); !ok {
				return "", time.Time{}, errors.New("missing date")
			}
			date = reqDate
		}
		t, parsed := parseRFC2616(reqDate)
		if !parsed {
			t, parsed = parseISO8601Basic(reqDate)
		}
		if !parsed || t.Year() < 1970 {
			return "", time.Time{}, errors.New("bad date")
		}
		headerTime = t
	}
	var b strings.Builder
	b.WriteString(rv.req.Method)
	b.WriteByte('\n')
	b.WriteString(md5)
	b.WriteByte('\n')
	b.WriteString(ctype)
	b.WriteByte('\n')
	b.WriteString(date)
	b.WriteByte('\n')
	writeCanonAmz(&b, amzHeadersV2(rv))
	writeCanonAmz(&b, qs)
	b.WriteString(canonicalResourceV2(rv, dnsNames))
	return b.String(), headerTime, nil
}

// writeCanonAmz is get_canon_amz_hdrs: k:v\n in key order.
func writeCanonAmz(b *strings.Builder, m map[string]string) {
	for _, k := range slices.Sorted(maps.Keys(m)) {
		b.WriteString(k)
		b.WriteByte(':')
		b.WriteString(m[k])
		b.WriteByte('\n')
	}
}

func amzHeadersV2(rv *requestView) map[string]string {
	type entry struct{ env, key, val string }
	var entries []entry
	for name := range rv.req.Header {
		lower := strings.ToLower(name)
		for _, prefix := range amzHeaderPrefixes {
			if strings.HasPrefix(lower, prefix) {
				v, _ := rv.header(name)
				entries = append(entries, entry{env: strings.ToUpper(strings.ReplaceAll(name, "-", "_")), key: "x-amz-" + lower[len(prefix):], val: v})
				break
			}
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].env < entries[j].env })
	m := map[string]string{}
	for _, e := range entries {
		if old, ok := m[e.key]; ok {
			m[e.key] = strings.TrimRight(old, " \t\n\v\f\r") + "," + e.val
		} else {
			m[e.key] = e.val
		}
	}
	return m
}

func qsMetaV2(rv *requestView) map[string]string {
	m := map[string]string{}
	for k, v := range rv.params {
		lower := strings.ToLower(k)
		switch {
		case strings.HasPrefix(lower, "x-amz-meta-"):
			if old, ok := m[lower]; ok {
				m[lower] = strings.TrimRight(old, " \t\n\v\f\r") + "," + v
			} else {
				m[lower] = v
			}
		case lower == "x-amz-security-token":
			m[lower] = v
		}
	}
	return m
}

func canonicalResourceV2(rv *requestView, dnsNames []string) string {
	var b strings.Builder
	if bucket := hostBucket(rv.hostNoPort(), dnsNames); bucket != "" {
		b.WriteByte('/')
		b.WriteString(bucket)
		if !strings.HasPrefix(rv.rawPath, "/") {
			b.WriteByte('/')
		}
	}
	b.WriteString(rv.rawPath)
	sep := byte('?')
	for _, name := range signedSubresourcesV2 {
		v, ok := rv.subres[name]
		if !ok {
			continue
		}
		b.WriteByte(sep)
		sep = '&'
		b.WriteString(name)
		if v != "" {
			b.WriteByte('=')
			b.WriteString(v)
		}
	}
	return b.String()
}

func hostBucket(host string, dnsNames []string) string {
	if host == "" {
		return ""
	}
	names := slices.Sorted(slices.Values(slices.DeleteFunc(slices.Clone(dnsNames), func(s string) bool { return s == "" })))
	if len(names) == 0 {
		return ""
	}
	for _, n := range names {
		if len(n) > len(host) || !strings.EqualFold(host[len(host)-len(n):], n) {
			continue
		}
		pos := len(host) - len(n)
		if pos == 0 {
			return ""
		}
		if host[pos-1] != '.' {
			continue
		}
		return host[:pos-1]
	}
	if !looksLikeIP(host) && validBucketName(host) {
		return host
	}
	return ""
}

func validBucketName(s string) bool {
	switch {
	case s == "":
		return true
	case len(s) < 3, len(s) > 255:
		return false
	}
	return !strings.ContainsAny(s, "/\xff")
}

func looksLikeIP(s string) bool {
	if strings.Contains(s, ":") && net.ParseIP(s) != nil {
		return true
	}
	periods, expect := 0, false
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '.':
			if !expect {
				return false
			}
			periods++
			if periods > 3 {
				return false
			}
			expect = false
		case c >= '0' && c <= '9':
			expect = true
		default:
			return false
		}
	}
	return periods == 3
}

func signatureV2(secret, stringToSign string) (string, error) {
	if secret == "" {
		return "", fmt.Errorf("%w: empty secret key", op.ErrInvalidArgument)
	}
	h := hmac.New(sha1.New, []byte(secret))
	h.Write([]byte(stringToSign))
	return base64.StdEncoding.EncodeToString(h.Sum(nil)), nil
}
```

`strings.ContainsAny(s, "/\xff")` tests bytes, which is what radosgw's loop over `unsigned char` does; `authData.verify` (Task 3) returns `signatureV2`'s error for the empty secret.

- [ ] **Step 4: Run the specs and the gate; commit**

Run: `go test -tags=ceph_preview ./internal/auth/... && make check`
Expected: PASS.

```sh
git add internal/auth
git commit -m "feat(auth): verify SigV2 header and query-string signatures"
```

---

### Task 9: Identity resolution and `Verifier.Authenticate` end to end, through `s3.Handler` on `memstore`

**Files:**
- Create: `internal/auth/verifier.go`, `internal/auth/identity.go`, `internal/auth/verifier_test.go`, `internal/auth/identity_test.go`
- Modify: `go.mod` (`github.com/aws/aws-sdk-go-v2/service/s3` becomes a test dependency)

**Interfaces:**
- Consumes: Tasks 1 to 8; `op.UserRecord`, `op.AccountStore`, `op.Anonymous`, `meta.ParseOwner`, `meta.UserOwner`, `meta.AccountOwner`; G's `op.PayloadForms`, `s3.NewHandler`, `s3.Authenticator` (with the payload-forms argument, G Task 4), the payload column of `s3.Dispatch` (G Task 3), `memstore`.
- Produces:

```go
package auth

// CredentialStore is the part of op.UserStore the verifier reads: the key
// index (LocalEngine::authenticate) and the user a system request names in
// rgwx-uid (SysReqApplier). op.UserStore satisfies it.
type CredentialStore interface {
	GetUser(ctx context.Context, id meta.UserID) (*op.UserRecord, error)
	GetUserByAccessKey(ctx context.Context, key string) (*op.UserRecord, error)
}

// Verifier implements s3.Authenticator: radosgw's s3_main strategy with the
// anonymous and local engines (Keystone, LDAP and STS are excluded).
type Verifier struct{ /* cfg Config; creds CredentialStore; accounts op.AccountStore; now func() time.Time */ }

// New builds a verifier. accounts may be nil: an account user is then denied,
// as radosgw denies one whose account does not load (rgw_rest_s3.cc:6343).
func New(cfg Config, creds CredentialStore, accounts op.AccountStore) *Verifier

// Authenticate resolves req to an identity or an op.Error. payloads is the
// dispatched route's forms: a signed payload in a form outside them is
// ErrNotImplemented, answered before the access key is looked up.
func (v *Verifier) Authenticate(ctx context.Context, req *http.Request, payloads op.PayloadForms) (*op.AuthResult, error)

// identity is LocalApplier wrapped in SysReqApplier: what op.Identity carries
// for a user record and the key that signed.
func (v *Verifier) identity(ctx context.Context, rv *requestView, rec *op.UserRecord, key meta.AccessKey, account *meta.AccountInfo) (op.Identity, error)
```

`Authenticate`, in `RGW_Auth_S3::authorize` → `Strategy::apply` → `AWSAuthStrategy` order:

1. `!cfg.UseRados` → `op.ErrAccessDenied` for every request, anonymous included ([`rgw_rest_s3.cc:5119-5125`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L5119-L5125)).
2. `discoverFlavour`. Anonymous (`S3AnonymousEngine::is_applicable`, [`:6614-6628`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L6614-L6628)): version unknown and (query route or `OPTIONS`) → `op.AuthResult{Identity: op.Anonymous(), ContentLength: req.ContentLength, PayloadSHA256: "UNSIGNED-PAYLOAD"}`. This is what makes a request with `X-AMZ-…` upper-cased presigned parameters, or none at all, anonymous.
3. `cfg.DisablePresignedURLs` → `op.ErrAccessDenied.WithMessage("Presigned URLs are disabled by admin")` for every signed request ([`:5607-5610`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L5607-L5610), [`rgw_auth.cc:506-509`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth.cc#L506-L509)).
4. v4 → `authDataV4(rv, route == routeQuery, &cfg, now)`; v2 → `authDataV2`; unknown header scheme (`Authorization: Bearer …`) → `op.ErrInvalidArgument` ([`:5616-5618`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L5616-L5618)). Parse errors, the canonical-header refusals and the empty-payload rule return here, before any lookup.
5. `d.payload.acceptedBy(payloads)` (D-A1, Task 5): a signed single chunk on a route without `op.PayloadSigned`, or an aws-chunked payload on a route without `op.PayloadChunked`, is `op.ErrNotImplemented` ([`rgw_rest_s3.cc:5899-5969`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L5899-L5969), [`[T]:6466-6540`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L6466-L6540)), thrown by radosgw at the end of `get_auth_data_v4` and so before step 6. A SigV2 `authData` carries `payloadNone` and always passes.
6. Empty access key or signature → `op.ErrInvalidArgument` (`AWSEngine::authenticate`, [`:6179-6180`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L6179-L6180)).
7. `creds.GetUserByAccessKey`: any error → `op.ErrInvalidAccessKeyID` (`:6325-6329`); an error other than `op.ErrNoSuchUser` is also logged at error level with the access key id (never the secret).
8. The key must be in `rec.Info.AccessKeys` (`op.ErrAccessDenied`, `:6347-6351`) and `Active` (`op.ErrInvalidAccessKeyID`, D-A5).
9. `rec.Info.AccountID != ""` → `accounts.GetAccount`; nil store or any error → `op.ErrAccessDenied`, logged (`:6339-6345`, [`rgw_auth.cc:143-155`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth.cc#L143-L155)).
10. `d.verify(key.Secret)`: an error is returned as is (v2 empty secret); false → `op.ErrSignatureDoesNotMatch` (`:6373-6375`). radosgw skips the signature for `RGW_OP_OPTIONS_CORS` ([`:6354-6360`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L6354-L6360)); phase 2 adds that with CORS.
11. `identity(...)`, then the result: `PayloadSHA256` is `d.payloadHash` for v4 and `UNSIGNED-PAYLOAD` for v2; for v4 `d.completer(rv, key.Secret)` supplies `Body` and `ContentLength` (`AWSv4ComplMulti::modify_request_state` runs after the grant, so a missing `x-amz-decoded-content-length` is reported after the signature verified, as in radosgw); `Presigned` is `d.presigned`.

`identity` ([`rgw_auth.cc:1014-1125`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth.cc#L1014-L1125), [`rgw_auth_filters.h:229-330`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_filters.h#L229-L330)): `User` is a copy of `rec.Info`; `Owner` is the account (`meta.AccountOwner(account.ID)`) when the user has one, else `meta.UserOwner(info.UserID)` (`get_aclowner`, [`:1019-1030`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth.cc#L1019-L1030)); `Account`; `SubUser` is `key.Subuser`; `Tenant` is `info.UserID.Tenant` (`get_tenant`, [`rgw_auth.h:740-742`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth.h#L740-L742)); `AccessKey`; `OpMask` is `info.OpMask`; `Caps`; `Admin` is `info.Admin != 0 || info.System != 0` (`is_admin_of`, [`:1048-1051`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth.cc#L1048-L1051), Z's finding (d)); `System` is `info.System != 0`; `Attrs` is `rec.Attrs` (Z decodes the identity policies from them). For a system user with a non-empty `rgwx-uid` system parameter (D-A10): `meta.ParseOwner` of it; a user id → `creds.GetUser` must succeed, then `Owner` and `Tenant` are that user's; an account id → the account must load, then `Owner` is `meta.AccountOwner(id)` and `Tenant` the account's; a failure is `op.ErrAccessDenied` (EACCES, [`rgw_auth_filters.h:299-311`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_filters.h#L299-L311)).

- [ ] **Step 1: Write the failing identity specs**

`internal/auth/identity_test.go` (package `auth_test`), on `memstore`:

```go
func fixedNow() time.Time { return time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC) }

// anyPayload is put_obj's forms, the only route radosgw lets carry both, so
// a spec about identities never trips the payload whitelist.
const anyPayload = op.PayloadSigned | op.PayloadChunked

func user(id string, mutate func(*meta.UserInfo)) meta.UserInfo {
	info := meta.NewUserInfo()
	info.UserID = meta.ParseUserID(id)
	info.DisplayName = id
	info.AccessKeys = map[string]meta.AccessKey{"AK" + strings.ToUpper(info.UserID.ID): {ID: "AK" + strings.ToUpper(info.UserID.ID), Secret: "secret-" + info.UserID.ID, Active: true}}
	if mutate != nil {
		mutate(&info)
	}
	return info
}

// signedAs signs req as the user with SigV4 header auth, the way the SDK does.
func signedAs(req *http.Request, id string) *http.Request {
	ak := "AK" + strings.ToUpper(meta.ParseUserID(id).ID)
	signer := v4.NewSigner(func(o *v4.SignerOptions) { o.DisableURIPathEscaping = true })
	Expect(signer.SignHTTP(context.Background(), aws.Credentials{AccessKeyID: ak, SecretAccessKey: "secret-" + meta.ParseUserID(id).ID}, req, "UNSIGNED-PAYLOAD", "s3", "us-east-1", fixedNow())).To(Succeed())
	return req
}

var _ = Describe("Verifier identities", func() {
	var (
		store *memstore.Store
		v     *auth.Verifier
	)

	BeforeEach(func() {
		store = memstore.New(memstore.Config{Release: denc.Squid, Now: fixedNow})
		store.AddUser(user("alice", func(u *meta.UserInfo) { u.OpMask = op.OpTypeRead }))
		store.AddUser(user("t1$bob", nil))
		store.AddUser(user("carol", func(u *meta.UserInfo) { u.System = 1 }))
		store.AddUser(user("dave", func(u *meta.UserInfo) { u.Admin = 1 }))
		store.AddUser(user("erin", func(u *meta.UserInfo) {
			u.AccessKeys["AKERINAPP"] = meta.AccessKey{ID: "AKERINAPP", Secret: "secret-erin-app", Subuser: "erin:app", Active: true}
			u.SubUsers = map[string]meta.SubUser{"erin:app": {Name: "erin:app", Perm: 0x1}}
		}))
		store.AddUser(user("frank", func(u *meta.UserInfo) { u.AccountID = "RGW00000000000000001" }))
		store.AddAccount(meta.AccountInfo{ID: "RGW00000000000000001", Tenant: "acct-tenant", Name: "acme", MaxBuckets: 7})
		store.AddUser(user("grace", func(u *meta.UserInfo) { k := u.AccessKeys["AKGRACE"]; k.Active = false; u.AccessKeys["AKGRACE"] = k }))
		store.AddUser(user("henry", func(u *meta.UserInfo) { u.AccountID = "RGW00000000000000009" })) // no such account
		cfg := auth.DefaultConfig()
		cfg.Now = fixedNow
		v = auth.New(cfg, store, store)
	})

	get := func(id string) *op.AuthResult {
		res, err := v.Authenticate(context.Background(), signedAs(httptest.NewRequest(http.MethodGet, "http://s3.example.com/", nil), id), anyPayload)
		Expect(err).NotTo(HaveOccurred())
		return res
	}

	It("fills the identity the way LocalApplier does", func(ctx SpecContext) {
		res := get("alice")
		rec, err := store.GetUser(ctx, meta.UserID{ID: "alice"})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Identity.Anonymous).To(BeFalse())
		Expect(res.Identity.User.UserID).To(Equal(meta.UserID{ID: "alice"}))
		Expect(res.Identity.Owner).To(Equal(meta.UserOwner(meta.UserID{ID: "alice"})))
		Expect(res.Identity.Account).To(BeNil())
		Expect(res.Identity.SubUser).To(BeEmpty())
		Expect(res.Identity.Tenant).To(BeEmpty())
		Expect(res.Identity.AccessKey).To(Equal("AKALICE"))
		Expect(res.Identity.OpMask).To(Equal(op.OpTypeRead))
		Expect(res.Identity.Admin).To(BeFalse())
		Expect(res.Identity.System).To(BeFalse())
		Expect(res.Identity.Attrs).To(Equal(rec.Attrs))
		Expect(res.PayloadSHA256).To(Equal("UNSIGNED-PAYLOAD"))
		Expect(res.Body).To(BeNil())
		Expect(res.Presigned).To(BeFalse())
	})

	It("carries the tenant, the subuser, and admin as admin-or-system", func() {
		Expect(get("t1$bob").Identity.Tenant).To(Equal("t1"))
		erin := get("erin")
		Expect(erin.Identity.SubUser).To(BeEmpty(), "the primary key names no subuser")
		req := httptest.NewRequest(http.MethodGet, "http://s3.example.com/", nil)
		signer := v4.NewSigner(func(o *v4.SignerOptions) { o.DisableURIPathEscaping = true })
		Expect(signer.SignHTTP(context.Background(), aws.Credentials{AccessKeyID: "AKERINAPP", SecretAccessKey: "secret-erin-app"}, req, "UNSIGNED-PAYLOAD", "s3", "us-east-1", fixedNow())).To(Succeed())
		res, err := v.Authenticate(context.Background(), req, anyPayload)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Identity.SubUser).To(Equal("erin:app"))
		Expect(res.Identity.AccessKey).To(Equal("AKERINAPP"))
		carol, dave := get("carol"), get("dave")
		Expect(carol.Identity.System).To(BeTrue())
		Expect(carol.Identity.Admin).To(BeTrue(), "is_admin_of is admin || system")
		Expect(dave.Identity.Admin).To(BeTrue())
		Expect(dave.Identity.System).To(BeFalse())
	})

	It("loads the account of an account user and owns as the account", func() {
		res := get("frank")
		Expect(res.Identity.Account).NotTo(BeNil())
		Expect(res.Identity.Account.Name).To(Equal("acme"))
		Expect(res.Identity.Owner).To(Equal(meta.AccountOwner("RGW00000000000000001")))
		Expect(res.Identity.Tenant).To(BeEmpty(), "get_tenant is the user's, not the account's")
	})

	It("denies an account user whose account cannot be loaded, or when there is no account store", func() {
		_, err := v.Authenticate(context.Background(), signedAs(httptest.NewRequest(http.MethodGet, "http://s3.example.com/", nil), "henry"), anyPayload)
		Expect(err).To(MatchError(op.ErrAccessDenied))
		cfg := auth.DefaultConfig()
		cfg.Now = fixedNow
		noAccounts := auth.New(cfg, store, nil)
		_, err = noAccounts.Authenticate(context.Background(), signedAs(httptest.NewRequest(http.MethodGet, "http://s3.example.com/", nil), "frank"), anyPayload)
		Expect(err).To(MatchError(op.ErrAccessDenied))
		_, err = noAccounts.Authenticate(context.Background(), signedAs(httptest.NewRequest(http.MethodGet, "http://s3.example.com/", nil), "alice"), anyPayload)
		Expect(err).NotTo(HaveOccurred())
	})

	It("answers InvalidAccessKeyId for an unknown or inactive key and AccessDenied for a key the user record lacks", func(ctx SpecContext) {
		req := httptest.NewRequest(http.MethodGet, "http://s3.example.com/", nil)
		signer := v4.NewSigner(func(o *v4.SignerOptions) { o.DisableURIPathEscaping = true })
		Expect(signer.SignHTTP(ctx, aws.Credentials{AccessKeyID: "AKNOBODY", SecretAccessKey: "x"}, req, "UNSIGNED-PAYLOAD", "s3", "us-east-1", fixedNow())).To(Succeed())
		_, err := v.Authenticate(ctx, req, anyPayload)
		Expect(err).To(MatchError(op.ErrInvalidAccessKeyID))
		_, err = v.Authenticate(ctx, signedAs(httptest.NewRequest(http.MethodGet, "http://s3.example.com/", nil), "grace"), anyPayload)
		Expect(err).To(MatchError(op.ErrInvalidAccessKeyID))
		// a key the index knows but the record lost: fake the store
		users := &opfakes.FakeUserStore{}
		rec, err := store.GetUser(ctx, meta.UserID{ID: "alice"})
		Expect(err).NotTo(HaveOccurred())
		rec.Info.AccessKeys = nil
		users.GetUserByAccessKeyReturns(rec, nil)
		cfg := auth.DefaultConfig()
		cfg.Now = fixedNow
		_, err = auth.New(cfg, users, nil).Authenticate(ctx, signedAs(httptest.NewRequest(http.MethodGet, "http://s3.example.com/", nil), "alice"), anyPayload)
		Expect(err).To(MatchError(op.ErrAccessDenied))
	})

	It("honours rgwx-uid for a system user only", func() {
		res, err := v.Authenticate(context.Background(), signedAs(httptest.NewRequest(http.MethodGet, "http://s3.example.com/?rgwx-uid=t1%24bob", nil), "carol"), anyPayload)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Identity.Owner).To(Equal(meta.UserOwner(meta.UserID{Tenant: "t1", ID: "bob"})))
		Expect(res.Identity.Tenant).To(Equal("t1"))
		Expect(res.Identity.User.UserID).To(Equal(meta.UserID{ID: "carol"}), "the identity is still carol's")
		res, err = v.Authenticate(context.Background(), signedAs(httptest.NewRequest(http.MethodGet, "http://s3.example.com/?rgwx-uid=RGW00000000000000001", nil), "carol"), anyPayload)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Identity.Owner).To(Equal(meta.AccountOwner("RGW00000000000000001")))
		Expect(res.Identity.Tenant).To(Equal("acct-tenant"))
		_, err = v.Authenticate(context.Background(), signedAs(httptest.NewRequest(http.MethodGet, "http://s3.example.com/?rgwx-uid=nobody", nil), "carol"), anyPayload)
		Expect(err).To(MatchError(op.ErrAccessDenied))
		res, err = v.Authenticate(context.Background(), signedAs(httptest.NewRequest(http.MethodGet, "http://s3.example.com/?rgwx-uid=t1%24bob", nil), "alice"), anyPayload)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Identity.Owner).To(Equal(meta.UserOwner(meta.UserID{ID: "alice"})), "ignored for a non-system user")
	})
})
```

- [ ] **Step 2: Write the failing verifier and handler specs**

`internal/auth/verifier_test.go` (package `auth_test`):

```go
var _ s3.Authenticator = (*auth.Verifier)(nil)

var _ = Describe("Verifier", func() {
	var (
		store *memstore.Store
		cfg   auth.Config
		v     *auth.Verifier
	)

	BeforeEach(func() {
		store = memstore.New(memstore.Config{Release: denc.Squid, Now: fixedNow})
		store.AddUser(user("alice", nil))
		cfg = auth.DefaultConfig()
		cfg.Now = fixedNow
		v = auth.New(cfg, store, store)
	})

	It("authenticates an unsigned request as anonymous, with an unsigned payload", func() {
		res, err := v.Authenticate(context.Background(), httptest.NewRequest(http.MethodGet, "http://s3.example.com/", nil), anyPayload)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Identity).To(Equal(op.Anonymous()))
		Expect(res.PayloadSHA256).To(Equal("UNSIGNED-PAYLOAD"))
		Expect(res.Body).To(BeNil())
	})

	It("treats presigned parameters not spelled X-Amz- as an unsigned request, as radosgw does", func() {
		req := httptest.NewRequest(http.MethodGet, "http://s3.example.com/?X-AMZ-Algorithm=AWS4-HMAC-SHA256&X-AMZ-Credential=AKALICE%2F20150830%2Fus-east-1%2Fs3%2Faws4_request&X-AMZ-Signature=00", nil)
		res, err := v.Authenticate(context.Background(), req, anyPayload)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Identity.Anonymous).To(BeTrue())
	})

	DescribeTable("maps radosgw's engine errors",
		func(mutate func(*http.Request), want error) {
			req := signedAs(httptest.NewRequest(http.MethodGet, "http://s3.example.com/", nil), "alice")
			mutate(req)
			_, err := v.Authenticate(context.Background(), req, anyPayload)
			Expect(err).To(MatchError(want))
		},
		Entry("wrong signature", func(r *http.Request) {
			a := r.Header.Get("Authorization")
			r.Header.Set("Authorization", a[:len(a)-1]+map[bool]string{true: "0", false: "1"}[a[len(a)-1] != '0'])
		}, op.ErrSignatureDoesNotMatch),
		Entry("unknown scheme", func(r *http.Request) { r.Header.Set("Authorization", "Bearer abc") }, op.ErrInvalidArgument),
		Entry("v2 header without a colon", func(r *http.Request) { r.Header.Set("Authorization", "AWS junk") }, op.ErrInvalidArgument),
		Entry("skewed", func(r *http.Request) { r.Header.Set("X-Amz-Date", "20150830T100000Z") }, op.ErrRequestTimeTooSkewed),
		Entry("unsigned x-amz header", func(r *http.Request) { r.Header.Set("X-Amz-Meta-Late", "x") }, op.ErrAccessDenied),
	)

	It("denies everything without a backend, and every signed request when presigned URLs are disabled", func() {
		off := cfg
		off.UseRados = false
		none := auth.New(off, store, store)
		_, err := none.Authenticate(context.Background(), httptest.NewRequest(http.MethodGet, "http://s3.example.com/", nil), anyPayload)
		Expect(err).To(MatchError(op.ErrAccessDenied))
		_, err = none.Authenticate(context.Background(), signedAs(httptest.NewRequest(http.MethodGet, "http://s3.example.com/", nil), "alice"), anyPayload)
		Expect(err).To(MatchError(op.ErrAccessDenied))

		noPresign := cfg
		noPresign.DisablePresignedURLs = true
		strict := auth.New(noPresign, store, store)
		_, err = strict.Authenticate(context.Background(), signedAs(httptest.NewRequest(http.MethodGet, "http://s3.example.com/", nil), "alice"), anyPayload)
		var opErr *op.Error
		Expect(errors.As(err, &opErr)).To(BeTrue())
		Expect(opErr.Code).To(Equal("AccessDenied"))
		Expect(opErr.Message).To(Equal("Presigned URLs are disabled by admin"))
		res, err := strict.Authenticate(context.Background(), httptest.NewRequest(http.MethodGet, "http://s3.example.com/", nil), anyPayload)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Identity.Anonymous).To(BeTrue())
	})

	It("verifies a SigV2 header and a SigV2 presigned request", func() {
		req := httptest.NewRequest(http.MethodGet, "http://s3.example.com/b/k", nil)
		req.Header.Set("Date", "Sun, 30 Aug 2015 12:36:00 GMT")
		h := hmac.New(sha1.New, []byte("secret-alice"))
		h.Write([]byte("GET\n\n\nSun, 30 Aug 2015 12:36:00 GMT\n/b/k"))
		req.Header.Set("Authorization", "AWS AKALICE:"+base64.StdEncoding.EncodeToString(h.Sum(nil)))
		res, err := v.Authenticate(context.Background(), req, anyPayload)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Identity.AccessKey).To(Equal("AKALICE"))
		Expect(res.Presigned).To(BeFalse())
		Expect(res.PayloadSHA256).To(Equal("UNSIGNED-PAYLOAD"))

		h = hmac.New(sha1.New, []byte("secret-alice"))
		h.Write([]byte("GET\n\n\n1441000000\n/b/k"))
		pre := httptest.NewRequest(http.MethodGet, "http://s3.example.com/b/k?AWSAccessKeyId=AKALICE&Expires=1441000000&Signature="+url.QueryEscape(base64.StdEncoding.EncodeToString(h.Sum(nil))), nil)
		res, err = v.Authenticate(context.Background(), pre, anyPayload)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Presigned).To(BeTrue())
	})

	It("authenticates the request go-ceph's admin client sends", func() {
		req := httptest.NewRequest(http.MethodGet, "http://s3.example.com/admin/user?uid=alice&format=json", nil)
		signer := v4.NewSigner()
		Expect(signer.SignHTTP(context.Background(), aws.Credentials{AccessKeyID: "AKALICE", SecretAccessKey: "secret-alice"}, req, "UNSIGNED-PAYLOAD", "s3", "default", time.Now())).To(Succeed())
		req.Header.Del("X-Amz-Content-Sha256") // go-ceph's SignHTTP call never sends it
		live := cfg
		live.Now = nil
		res, err := auth.New(live, store, store).Authenticate(context.Background(), req, anyPayload)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Identity.AccessKey).To(Equal("AKALICE"))
		Expect(res.PayloadSHA256).To(Equal("UNSIGNED-PAYLOAD"))
	})

	// The key is unknown in every entry: a refused form is answered before
	// the lookup, as get_auth_data_v4 throws before LocalEngine runs
	// (rgw_rest_s3.cc:5899-5969, :6325-6329); an accepted one reaches it.
	DescribeTable("refuses a payload form the dispatched op does not take, before the access key is looked up",
		func(hash, body string, forms op.PayloadForms, want error) {
			req := httptest.NewRequest(http.MethodPut, "http://s3.example.com/plain/k", strings.NewReader(body))
			req.Header.Set("X-Amz-Content-Sha256", hash)
			if strings.HasPrefix(hash, "STREAMING-") {
				req.Header.Set("X-Amz-Decoded-Content-Length", "5")
			}
			signer := v4.NewSigner(func(o *v4.SignerOptions) { o.DisableURIPathEscaping = true })
			Expect(signer.SignHTTP(context.Background(), aws.Credentials{AccessKeyID: "AKNOBODY", SecretAccessKey: "x"}, req, hash, "s3", "us-east-1", fixedNow())).To(Succeed())
			_, err := v.Authenticate(context.Background(), req, forms)
			Expect(err).To(MatchError(want))
		},
		Entry("a signed single chunk on an op that takes none", helloSHA256, "hello", op.PayloadForms(0), op.ErrNotImplemented),
		Entry("a signed single chunk on an op in the whitelist", helloSHA256, "hello", op.PayloadSigned, op.ErrInvalidAccessKeyID),
		Entry("aws-chunked on an op that takes a single chunk only", "STREAMING-AWS4-HMAC-SHA256-PAYLOAD",
			"5;chunk-signature="+strings.Repeat("0", 64)+"\r\nhello\r\n", op.PayloadSigned, op.ErrNotImplemented),
		Entry("unsigned aws-chunked with a trailer on an op that takes a single chunk only", "STREAMING-UNSIGNED-PAYLOAD-TRAILER",
			"5\r\nhello\r\n0\r\n\r\n", op.PayloadSigned, op.ErrNotImplemented),
		Entry("aws-chunked on put_obj", "STREAMING-AWS4-HMAC-SHA256-PAYLOAD",
			"5;chunk-signature="+strings.Repeat("0", 64)+"\r\nhello\r\n", anyPayload, op.ErrInvalidAccessKeyID),
		Entry("UNSIGNED-PAYLOAD with a body needs no completer", "UNSIGNED-PAYLOAD", "hello", op.PayloadForms(0), op.ErrInvalidAccessKeyID),
		Entry("the empty hash on an empty body needs no completer", emptySHA256, "", op.PayloadForms(0), op.ErrInvalidAccessKeyID),
	)
})

const (
	helloSHA256 = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824" // sha256("hello")
	emptySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" // sha256("")
)

var _ = Describe("Verifier through the s3 handler", func() {
	var (
		store *memstore.Store
		h     *s3.Handler
		srv   *httptest.Server
	)

	BeforeEach(func() {
		store = memstore.New(memstore.Config{Release: denc.Squid})
		store.AddUser(user("alice", nil))
		store.AddUser(user("sue", func(u *meta.UserInfo) { u.Suspended = 1 }))
		env := &op.Env{Zone: store, Users: store, Accounts: store, Buckets: store, Objects: store, Multipart: store, Stats: store, Usage: store, Metadata: store, Authz: op.OwnerOnly{}, Metrics: op.NopMetrics{}}
		cfg := auth.DefaultConfig()
		h = s3.NewHandler(env, auth.New(cfg, store, store), s3.Config{})
		h.Register("put_obj", func(_ context.Context, w http.ResponseWriter, r *op.Request) error {
			body, err := io.ReadAll(r.Body) // to EOF: the verification verdict arrives with the last read
			if err != nil {
				return err
			}
			w.Header().Set("X-Test-Body", string(body))
			w.Header().Set("X-Test-Length", strconv.FormatInt(r.ContentLength, 10))
			w.WriteHeader(http.StatusOK)
			return nil
		})
		srv = httptest.NewServer(h)
		DeferCleanup(srv.Close)
	})

	client := func() *awss3.Client {
		return awss3.New(awss3.Options{
			Region:       "us-east-1",
			BaseEndpoint: aws.String(srv.URL),
			UsePathStyle: true,
			Credentials:  credentials.NewStaticCredentialsProvider("AKALICE", "secret-alice", ""),
		})
	}

	It("serves ListBuckets to the reference client and refuses a wrong secret with radosgw's error document", func(ctx SpecContext) {
		out, err := client().ListBuckets(ctx, &awss3.ListBucketsInput{})
		Expect(err).NotTo(HaveOccurred())
		Expect(aws.ToString(out.Owner.ID)).To(Equal("alice"))

		bad := awss3.New(awss3.Options{Region: "us-east-1", BaseEndpoint: aws.String(srv.URL), UsePathStyle: true,
			Credentials: credentials.NewStaticCredentialsProvider("AKALICE", "wrong", "")})
		_, err = bad.ListBuckets(ctx, &awss3.ListBucketsInput{})
		var apiErr smithy.APIError
		Expect(errors.As(err, &apiErr)).To(BeTrue())
		Expect(apiErr.ErrorCode()).To(Equal("SignatureDoesNotMatch"))
	})

	It("decodes the aws-chunked trailer upload the reference client sends and hands the op the decoded length", func(ctx SpecContext) {
		_, err := client().PutObject(ctx, &awss3.PutObjectInput{Bucket: aws.String("plain"), Key: aws.String("k"), Body: strings.NewReader("hello world"), ContentLength: aws.Int64(11)})
		Expect(err).NotTo(HaveOccurred())
		// the handler recorded what the op saw; fetch it through a raw request the same way
		req, _ := http.NewRequestWithContext(ctx, http.MethodPut, srv.URL+"/plain/k2", strings.NewReader("hello world"))
		req.ContentLength = 11
		req.Header.Set("X-Amz-Decoded-Content-Length", "11")
		req.Header.Set("X-Amz-Content-Sha256", "STREAMING-UNSIGNED-PAYLOAD-TRAILER")
		req.Header.Set("X-Amz-Trailer", "x-amz-checksum-crc32")
		req.Body = io.NopCloser(strings.NewReader("b\r\nhello world\r\n0\r\nx-amz-checksum-crc32:DUoRhQ==\r\n\r\n"))
		req.ContentLength = -1
		signer := v4.NewSigner(func(o *v4.SignerOptions) { o.DisableURIPathEscaping = true })
		Expect(signer.SignHTTP(ctx, aws.Credentials{AccessKeyID: "AKALICE", SecretAccessKey: "secret-alice"}, req, "STREAMING-UNSIGNED-PAYLOAD-TRAILER", "s3", "us-east-1", time.Now())).To(Succeed())
		resp, err := http.DefaultClient.Do(req)
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		Expect(resp.Header.Get("X-Test-Body")).To(Equal("hello world"))
		Expect(resp.Header.Get("X-Test-Length")).To(Equal("11"))
	})

	It("renders auth failures and a suspended user as radosgw's error documents", func(ctx SpecContext) {
		do := func(req *http.Request) (int, string) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			return rec.Code, rec.Body.String()
		}
		code, body := do(signedAs(httptest.NewRequest(http.MethodGet, "http://s3.example.com/", nil), "sue"))
		Expect(code).To(Equal(http.StatusForbidden))
		Expect(body).To(ContainSubstring("<Code>UserSuspended</Code>"))
		req := signedAs(httptest.NewRequest(http.MethodGet, "http://s3.example.com/", nil), "alice")
		req.Header.Set("X-Amz-Date", "20150830T100000Z")
		code, body = do(req)
		Expect(code).To(Equal(http.StatusForbidden))
		Expect(body).To(ContainSubstring("<Code>RequestTimeTooSkewed</Code>"))
		code, body = do(httptest.NewRequest(http.MethodGet, "http://s3.example.com/?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=AKALICE%2F20150830%2Fus-east-1%2Fs3%2Faws4_request&X-Amz-Date=20150830T123600Z&X-Amz-Expires=1&X-Amz-SignedHeaders=host&X-Amz-Signature=00", nil))
		Expect(code).To(Equal(http.StatusForbidden))
		Expect(body).To(ContainSubstring("<Code>AccessDenied</Code><Message>The pre-signed URL has expired</Message>"))
	})

	// The key is unknown: a form the route refuses is 501 from authentication,
	// a form it takes goes on to the key lookup and is 403 InvalidAccessKeyId,
	// so the status shows which side of the whitelist the route sits on at
	// that release (rgw_rest_s3.cc:5899-5969 at v19.2.6, :6466-6540 at v20.2.4).
	DescribeTable("answers the dispatched route's payload forms at the cluster's release",
		func(rel denc.Release, method, target, hash string, wantStatus int, wantCode string) {
			rs := memstore.New(memstore.Config{Release: rel})
			env := &op.Env{Zone: rs, Users: rs, Accounts: rs, Buckets: rs, Objects: rs, Multipart: rs, Stats: rs, Usage: rs, Metadata: rs, Authz: op.OwnerOnly{}, Metrics: op.NopMetrics{}}
			rh := s3.NewHandler(env, auth.New(auth.DefaultConfig(), rs, rs), s3.Config{})
			req := httptest.NewRequest(method, "http://s3.example.com"+target, strings.NewReader("hello"))
			req.Header.Set("X-Amz-Content-Sha256", hash)
			if strings.HasPrefix(hash, "STREAMING-") {
				req.Header.Set("X-Amz-Decoded-Content-Length", "5")
			}
			signer := v4.NewSigner(func(o *v4.SignerOptions) { o.DisableURIPathEscaping = true })
			Expect(signer.SignHTTP(context.Background(), aws.Credentials{AccessKeyID: "AKNOBODY", SecretAccessKey: "x"}, req, hash, "s3", "us-east-1", time.Now())).To(Succeed())
			rec := httptest.NewRecorder()
			rh.ServeHTTP(rec, req)
			Expect(rec.Code).To(Equal(wantStatus), rec.Body.String())
			Expect(rec.Body.String()).To(ContainSubstring("<Code>" + wantCode + "</Code>"))
		},
		Entry("DELETE object takes no signed body on Squid", denc.Squid, http.MethodDelete, "/plain/k", helloSHA256, 501, "NotImplemented"),
		Entry("DELETE object takes no signed body on Tentacle", denc.Tentacle, http.MethodDelete, "/plain/k", helloSHA256, 501, "NotImplemented"),
		Entry("GET ?logging takes no signed body on Squid", denc.Squid, http.MethodGet, "/plain?logging", helloSHA256, 501, "NotImplemented"),
		Entry("GET ?logging takes one on Tentacle", denc.Tentacle, http.MethodGet, "/plain?logging", helloSHA256, 403, "InvalidAccessKeyId"),
		Entry("POST ?restore is a form upload on Squid and takes no signed body", denc.Squid, http.MethodPost, "/plain/k?restore", helloSHA256, 501, "NotImplemented"),
		Entry("POST ?restore is restore_obj on Tentacle and takes one", denc.Tentacle, http.MethodPost, "/plain/k?restore", helloSHA256, 403, "InvalidAccessKeyId"),
		Entry("DELETE ?website takes one, radosgw typing it as SET_BUCKET_WEBSITE", denc.Squid, http.MethodDelete, "/plain?website", helloSHA256, 403, "InvalidAccessKeyId"),
		Entry("POST ?delete takes one", denc.Squid, http.MethodPost, "/plain?delete", helloSHA256, 403, "InvalidAccessKeyId"),
		Entry("CompleteMultipartUpload takes no aws-chunked body", denc.Squid, http.MethodPost, "/plain/k?uploadId=u", "STREAMING-AWS4-HMAC-SHA256-PAYLOAD", 501, "NotImplemented"),
		Entry("UploadPart takes an aws-chunked body", denc.Tentacle, http.MethodPut, "/plain/k?uploadId=u&partNumber=1", "STREAMING-AWS4-HMAC-SHA256-PAYLOAD", 403, "InvalidAccessKeyId"),
	)
})
```

The handler specs rely on G's `ServeHTTP` flow (Task 4 of G: suspended user → `UserSuspended`, `WriteError`'s document) and on `memstore`'s `GetUserByAccessKey`. The `X-Amz-Date` set after signing is an unsigned x-amz header only if its value changed — the SDK signs `x-amz-date`, so the skew entry keeps the signed-header set and only the value moves; expect `RequestTimeTooSkewed` because the skew check runs before the signature compare ([`rgw_auth_s3.cc:457-459`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_s3.cc#L457-L459)).

- [ ] **Step 3: Run the specs to verify they fail**

Run: `go test -tags=ceph_preview ./internal/auth/...`
Expected: compile errors (`auth.New`, `auth.Verifier` undefined) after `go mod tidy` adds `service/s3`, `credentials` and `smithy-go` (sandbox disabled for the download).

- [ ] **Step 4: Implement `verifier.go` and `identity.go`**

```go
package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
)

type CredentialStore interface {
	GetUser(ctx context.Context, id meta.UserID) (*op.UserRecord, error)
	GetUserByAccessKey(ctx context.Context, key string) (*op.UserRecord, error)
}

type Verifier struct {
	cfg      Config
	creds    CredentialStore
	accounts op.AccountStore
	now      func() time.Time
}

func New(cfg Config, creds CredentialStore, accounts op.AccountStore) *Verifier {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Verifier{cfg: cfg, creds: creds, accounts: accounts, now: now}
}

func (v *Verifier) Authenticate(ctx context.Context, req *http.Request, payloads op.PayloadForms) (*op.AuthResult, error) {
	if !v.cfg.UseRados {
		return nil, fmt.Errorf("%w: no authentication backend enabled", op.ErrAccessDenied)
	}
	rv := newRequestView(req)
	ver, rt := discoverFlavour(rv)
	if ver == versionUnknown && (rt == routeQuery || req.Method == http.MethodOptions) {
		return &op.AuthResult{Identity: op.Anonymous(), ContentLength: req.ContentLength, PayloadSHA256: unsignedPayload}, nil
	}
	if v.cfg.DisablePresignedURLs {
		return nil, op.ErrAccessDenied.WithMessage("Presigned URLs are disabled by admin")
	}
	now := v.now()
	var (
		d   *authData
		err error
	)
	switch ver {
	case versionV4:
		d, err = authDataV4(rv, rt == routeQuery, &v.cfg, now)
	case versionV2:
		d, err = authDataV2(rv, &v.cfg, now)
	default:
		return nil, fmt.Errorf("%w: unknown authorization scheme", op.ErrInvalidArgument)
	}
	if err != nil {
		return nil, err
	}
	// get_auth_data_v4 refuses the op's payload form last, after the
	// empty-payload rule and before LocalEngine looks the key up
	// (rgw_rest_s3.cc:5860-5969 at v19.2.6).
	if err := d.payload.acceptedBy(payloads); err != nil {
		return nil, err
	}
	if d.accessKey == "" || d.signature == "" {
		return nil, fmt.Errorf("%w: missing access key or signature", op.ErrInvalidArgument)
	}
	rec, err := v.creds.GetUserByAccessKey(ctx, d.accessKey)
	if err != nil {
		if !errors.Is(err, op.ErrNoSuchUser) {
			slog.ErrorContext(ctx, "access key lookup failed", slog.String("access_key", d.accessKey), slog.Any("err", err))
		}
		return nil, fmt.Errorf("%w: %s", op.ErrInvalidAccessKeyID, d.accessKey)
	}
	key, ok := rec.Info.AccessKeys[d.accessKey]
	if !ok {
		return nil, fmt.Errorf("%w: access key not encoded in user info", op.ErrAccessDenied)
	}
	if !key.Active {
		return nil, fmt.Errorf("%w: %s is inactive", op.ErrInvalidAccessKeyID, d.accessKey)
	}
	var account *meta.AccountInfo
	if rec.Info.AccountID != "" {
		if account, err = v.loadAccount(ctx, rec.Info.AccountID); err != nil {
			return nil, err
		}
	}
	match, err := d.verify(key.Secret)
	if err != nil {
		return nil, err
	}
	if !match {
		return nil, op.ErrSignatureDoesNotMatch
	}
	id, err := v.identity(ctx, rv, rec, key, account)
	if err != nil {
		return nil, err
	}
	res := &op.AuthResult{Identity: id, ContentLength: req.ContentLength, PayloadSHA256: unsignedPayload, Presigned: d.presigned}
	if d.v4 != nil {
		res.PayloadSHA256 = d.payloadHash
		if res.Body, res.ContentLength, err = d.completer(rv, key.Secret); err != nil {
			return nil, err
		}
	}
	return res, nil
}

func (v *Verifier) loadAccount(ctx context.Context, id string) (*meta.AccountInfo, error) {
	if v.accounts == nil {
		slog.ErrorContext(ctx, "account lookup unavailable", slog.String("account_id", id))
		return nil, fmt.Errorf("%w: no account store", op.ErrAccessDenied)
	}
	rec, err := v.accounts.GetAccount(ctx, id)
	if err != nil {
		slog.ErrorContext(ctx, "account lookup failed", slog.String("account_id", id), slog.Any("err", err))
		return nil, fmt.Errorf("%w: account %s: %w", op.ErrAccessDenied, id, err)
	}
	return &rec.Info, nil
}
```

`identity.go`:

```go
package auth

func (v *Verifier) identity(ctx context.Context, rv *requestView, rec *op.UserRecord, key meta.AccessKey, account *meta.AccountInfo) (op.Identity, error) {
	info := rec.Info
	id := op.Identity{
		User:      &info,
		Owner:     meta.UserOwner(info.UserID),
		Account:   account,
		SubUser:   key.Subuser,
		Tenant:    info.UserID.Tenant,
		AccessKey: key.ID,
		OpMask:    info.OpMask,
		Caps:      info.Caps,
		Admin:     info.Admin != 0 || info.System != 0,
		System:    info.System != 0,
		Attrs:     rec.Attrs,
	}
	if account != nil {
		id.Owner = meta.AccountOwner(account.ID)
	}
	if !id.System {
		return id, nil
	}
	uid, ok := rv.sysParams[sysParamPrefix+"uid"]
	if !ok || uid == "" {
		return id, nil
	}
	owner := meta.ParseOwner(uid)
	if owner.User != nil {
		u, err := v.creds.GetUser(ctx, *owner.User)
		if err != nil {
			return op.Identity{}, fmt.Errorf("%w: rgwx-uid %s: %w", op.ErrAccessDenied, uid, err)
		}
		id.Owner, id.Tenant = meta.UserOwner(u.Info.UserID), u.Info.UserID.Tenant
		return id, nil
	}
	acct, err := v.loadAccount(ctx, owner.Account)
	if err != nil {
		return op.Identity{}, err
	}
	id.Owner, id.Tenant = meta.AccountOwner(owner.Account), acct.Tenant
	return id, nil
}
```

`key.ID` is the map key `d.accessKey` (radosgw stores `access_key_id` as the string it looked up); `memstore.AddUser` and M's decoder both fill `ID`. If `meta.AccessKey.ID` can be empty in a stored record, use `d.accessKey` instead: pass it as a parameter and set `AccessKey: accessKey`.

- [ ] **Step 5: Run the specs and the gate; commit**

Run: `go test -tags=ceph_preview ./internal/auth/... ./internal/s3/... && make check`
Expected: PASS.

```sh
git add internal/auth go.mod go.sum
git commit -m "feat(auth): resolve identities and authenticate requests as radosgw's local engine"
```

---

### Task 10: `cli serve` wiring, registry entries, the s3-tests auth groups on both releases `[cluster]`

**Files:**
- Create: `test/s3tests/a-groups.txt` (the s3-tests node-id patterns A is judged by, P Task 11's form: `^s3tests/functional/test_headers\.py::` and the `test_s3.py` names carrying the `auth_aws2`, `auth_aws4` and `auth_common` markers, listed by name from `pytest --collect-only -q -m 'auth_aws2 or auth_aws4 or auth_common'` in T Task 4's pinned checkout and checked against `test/s3tests/baseline/squid.json` in Step 5)
- Modify: `internal/cli/serve.go` (G Task 9 step 8: the authenticator), `internal/cli/serve_integration_test.go` (G's "GET / with Authorization is 501" expectation), `docs/ceph-upstream-bugs.md`

**Interfaces:**
- Consumes: Task 9's `auth.New`, `auth.ConfigFrom`; G's `cli serve` (`conf *cephconf.Options`, `store *driver.Store`, `cfg s3.Config`); T's harness (`make cluster-up`, `make rgw-go-up`, T Task 12's `make s3tests-parity RELEASE=<r>`, T Task 5's `go run ./hack/parity diff -baseline FILE -candidate FILE`).
- Produces: a binary that authenticates; five registry entries and rgw-go's handling on the #81122 and #81123 entries.

- [ ] **Step 1: Wire the verifier**

In `internal/cli/serve.go`, where G builds the handler (`handler := s3.NewHandler(env, s3.AnonymousOnly{}, cfg)`):

```go
	authCfg, err := auth.ConfigFrom(conf, cfg.DNSNames)
	if err != nil {
		return fmt.Errorf("reading auth options: %w", err)
	}
	handler := s3.NewHandler(env, auth.New(authCfg, store, store), cfg)
```

`store` is the `*driver.Store`, which implements `op.UserStore` (M) and `op.AccountStore` (Task 1's stub until N). `s3.AnonymousOnly` stays in the `s3` package for its specs; nothing else uses it.

- [ ] **Step 2: Update G's serve integration expectation**

G's Task 9 asserted `GET / with Authorization: AWS4-HMAC-SHA256 ... is 501`. With the verifier wired, a well-formed header naming an unknown key is `403 InvalidAccessKeyId`; edit the assertion in `internal/cli/serve_integration_test.go` to:

```go
	// a signed request with a key the cluster has never seen
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=AKNOBODY/20150830/us-east-1/s3/aws4_request, SignedHeaders=host;x-amz-date, Signature="+strings.Repeat("0", 64))
	req.Header.Set("X-Amz-Date", time.Now().UTC().Format("20060102T150405Z"))
	Expect(resp.StatusCode).To(Equal(http.StatusForbidden))
	Expect(body).To(ContainSubstring("<Code>InvalidAccessKeyId</Code>"))
```

and add, with the manifest user from `hack/rooket`'s populate output (`cephtest.ReadManifest`), a signed `GET /` through `aws-sdk-go-v2`'s S3 client (`UsePathStyle`, `BaseEndpoint` = the serve address) that returns 200 with the user's id as the owner; and a presigned `GET /` (`s3.NewPresignClient(client).PresignListBuckets`) fetched with `http.Get` that returns 200. Both run on the rooket cluster (`[cluster]`, `//go:build integration`).

- [ ] **Step 3: Add the registry entries**

Append to `docs/ceph-upstream-bugs.md`, in its entry format (kind; evidence at a named tag with file:line; releases; rgw-go's handling; how found; **Upstream** with tracker.ceph.com issues and ceph/ceph PRs). Before writing each **Upstream** line, search tracker.ceph.com (full text) and `gh search prs --repo ceph/ceph` for the identifiers named in the entry; record what is found, or "none found (searched YYYY-MM-DD for …)" with the terms:

1. **A missing `x-amz-content-sha256` is `UNSIGNED-PAYLOAD`** — Quirk. [`rgw_auth_s3.h:638-661`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_s3.h#L638-L661) (v19.2.6), unchanged at v20.2.4. Both releases. rgw-go mirrors it (`docs/exclusions.md` names go-ceph's rgw/admin client as the reason). Found while transcribing `get_v4_exp_payload_hash`.
2. **`rgw_s3_auth_disable_signature_url` denies header-signed requests too** — Defect. [`rgw_rest_s3.cc:5607-5610`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L5607-L5610) throws `ERR_PRESIGNED_URL_DISABLED` before the flavour is dispatched, so with the option on every SigV2 and SigV4 request, header-signed included, is 403 "Presigned URLs are disabled by admin"; [`rgw.yaml.in:911-916`](https://github.com/ceph/ceph/blob/v19.2.6/src/common/options/rgw.yaml.in#L911-L916) documents it as presigned-only. Both releases. rgw-go mirrors it (D-A7), so a cluster that set the option behaves the same with either gateway.
3. **The final zero-length chunk's declared signature is never compared** — Defect. [`rgw_auth_s3.cc:1645-1655`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_s3.cc#L1645-L1655) extracts it and only logs it; the trailer signature is verified against the server's own final chunk signature ([`:1674-1675`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_s3.cc#L1674-L1675)), so nothing is exposed. Both releases. rgw-go mirrors it.
4. **Repeated query keys keep arrival order in the SigV4 canonical query string** — Quirk. `get_v4_canonical_qs` inserts into a `std::multimap` ([`rgw_auth_s3.cc:620-642`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_s3.cc#L620-L642)), which orders by key only; AWS sorts by key then value, so a client sending `a=2&a=1` signs `a=1&a=2` and radosgw computes `a=2&a=1`. Both releases. rgw-go mirrors it (D-A7).
5. **The credential scope's date, region and service are not checked** — Quirk. `parse_cred_scope` feeds them to the signing key only ([`rgw_auth_s3.cc:962-1033`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_s3.cc#L962-L1033)); AWS rejects a scope whose region or service differs from the endpoint's and whose date differs from `x-amz-date`. Both releases. rgw-go mirrors it: a signature computed for any region verifies against the same secret.

The two defects rgw-go does not mirror already have entries on main, filed upstream as tracker #81122 (the aws-chunked trailer section truncated behind a dead size check) and #81123 (the chunk size parsed with an unchecked `strtoull`). Do not create or rewrite them; append to each entry's **rgw-go** line the sentence that says where rgw-go realizes its handling:

- #81122: "Realized in `internal/auth/chunked.go` (`finish`, `maxTrailerSection` = 1024): a trailer section longer than 1024 bytes, counted from the CRLF that ends the last data chunk, is 409 LimitExceeded; `docs/exclusions.md` records the difference."
- #81123: "Realized in `internal/auth/chunked.go` (`parseChunkSize`): a chunk size that is not one to sixteen hex digits is 400 InvalidArgument before any of the chunk is stored; `docs/exclusions.md` records the difference."

- [ ] **Step 4: Run the unit gate; commit**

Run: `make check`
Expected: PASS.

```sh
git add internal/cli docs/ceph-upstream-bugs.md
git commit -m "feat(cli): authenticate S3 requests with the auth verifier"
git commit -m "docs(registry): record radosgw's authentication quirks and defects" # split: docs in its own commit
```

- [ ] **Step 5: The s3-tests auth groups on Squid `[cluster]`**

With T's harness (T Tasks 4, 5, 12; the radosgw baselines for both releases recorded under `test/s3tests/baseline/`):

```sh
make cluster-up RELEASE=squid
make populate RELEASE=squid
make rgw-go-up RELEASE=squid
make s3tests-parity RELEASE=squid
go run ./hack/parity diff -baseline test/s3tests/baseline/squid.json -candidate hack/rooket/out/squid/s3tests-rgw-go.json | grep -E -f test/s3tests/a-groups.txt
```

`make s3tests-parity` (T Task 12) runs the set as `RUN=parity`, records `hack/rooket/out/squid/s3tests-rgw-go.json` and diffs it; it exits 1 while other units' differences remain. (T's `parity-check` target takes `BASELINE=` and `CANDIDATE=` — a recorded result JSON, never a raw junit file — and `run.sh` names its report `hack/s3tests/out/<release>-<gateway>-<run>.xml`; the two lines above are the interface T Task 12 defines, the same form P Task 11 uses.) Expected: the grep prints nothing — no difference for any test in `s3tests/functional/test_headers.py` and for the `auth_aws2`, `auth_aws4` and `auth_common` markers of `test_s3.py` (T's Task 12 names the files that are A's: a `test_headers.py` difference is A's). Every difference is a bug in this unit: reproduce it as a `testdata` vector or a `verifier_test.go` spec, fix, rerun. Tests of the parity set that fail on rgw-go because their op is not yet implemented (a 501 from a handler R, W or P has not registered) are not auth differences; they are listed in the PR description by name.

If the `aws` CLI is on the machine, also smoke the two request shapes no Go client produces, against rgw-go's endpoint with the manifest user's keys: `aws s3 cp <file> s3://<bucket>/<key>` (botocore sends `STREAMING-AWS4-HMAC-SHA256-PAYLOAD-TRAILER` with a CRC32 trailer over plain HTTP) and `aws s3 presign s3://<bucket>/<key>` fetched with `curl -sS -o /dev/null -w '%{http_code}'`; both must succeed and, run against the rooket radosgw's endpoint, behave the same.

- [ ] **Step 6: The same on Tentacle `[cluster]`**

```sh
make cluster-down RELEASE=squid
make cluster-up RELEASE=tentacle && make populate RELEASE=tentacle && make rgw-go-up RELEASE=tentacle
make s3tests-parity RELEASE=tentacle
go run ./hack/parity diff -baseline test/s3tests/baseline/tentacle.json -candidate hack/rooket/out/tentacle/s3tests-rgw-go.json | grep -E -f test/s3tests/a-groups.txt
```

Expected: the same empty difference list. D-A8 predicts identical auth behaviour on both releases apart from the four Tentacle entries of the payload whitelist, which G's dispatch table carries (D-A1); any other Tentacle-only difference here would be the first counter-example and goes in the PR description before any fix.

- [ ] **Step 7: Open the PR**

Draft PR `feat: S3 authentication (unit A)`, assigned to the author, CI watched, merged on green. The description states, in radosgw's terms: what is verified (SigV4 header, query, presigned; aws-chunked with signed chunks, signed trailers and unsigned trailers; SigV2 header and query; anonymous), the two contract additions (D-A3, D-A4) and that account users get 403 on the real driver until unit N, the per-op and per-release payload forms answered with radosgw's 501 (D-A1), the read-to-EOF requirement for W and P (D-A2), the five registry entries and rgw-go's handling on the #81122 and #81123 entries. It does not name the harness or these steps.

---

## Self-review

**Spec coverage.** §9 phase 1, "SigV4 header, query and presigned with chunked and trailer payloads, SigV2, anonymous": header (Task 3), query and presigned (Task 4), chunked (Tasks 5, 6), trailers in both forms (Task 7), SigV2 header and query (Task 8), anonymous (Task 9). §6, "auth resolves the credential through the metadata cache and, for chunked uploads, wraps the body in a reader that verifies each chunk signature as bytes stream": Task 9 resolves through `op.UserStore` (M's cache-backed driver) and Task 6's reader verifies at each chunk boundary with O(1) memory. §8, coexistence and radosgw's signature quirks: D-A7 lists each mirrored quirk with its source and Task 10 registers the five client-visible ones and appends rgw-go's handling to the #81122 and #81123 entries; the two defects not mirrored are differences Task 6 records in `docs/exclusions.md`; radosgw's per-op payload whitelist is reproduced per release (D-A1, Tasks 5 and 9 over G's dispatch column); `docs/exclusions.md`'s hard requirement (the missing header is unsigned) is pinned in Tasks 3, 5 and 9. §2's priorities: no allocation sized by the client, bulk reads bypass the bufio buffer, one SHA-256 per payload and one HMAC per chunk, which is radosgw's own cost. Unit A's gates (`00-index.md`, "The nine units"): AWS test-suite-style vectors and radosgw quirk cases (Tasks 3, 4, 8 vectors), chunk-signature vectors (Task 6, the AWS documentation example and the reference `StreamSigner`), s3-tests auth groups (Task 10), the go-ceph rgw/admin request shape (Tasks 3, 9; the suite itself runs when N's admin API exists). The brief's MUST: `Identity.Admin = admin || system` and `System` separate (Task 9, [`rgw_auth.cc:1048-1051`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth.cc#L1048-L1051)); D7 (Task 7: trailer and chunk signatures verified, checksum values neither validated nor stored); every rule verified at v19.2.6 with the v20.2.4 diff (D-A8).

**Placeholder scan.** No "TBD", "TODO", "similar to", "add validation" or unshown code step. Two places defer to code that does not exist yet and say exactly what to adapt: Task 1's memstore helpers name G's mutex, clock and tag helper by their likely names and tell the implementer to use G's; Task 10's `cli serve` edit names the line G's Task 9 writes. Task 6's AWS documentation vector carries the transcribed values and the command that recomputes them.

**Type consistency.** `authData` has the same fields in Tasks 3 (definition), 4 (`altSign`), 5 (`payload`), 8 and 9; `authData.verify(secret) (bool, error)` is called with that signature in Tasks 3, 4, 8 and 9; `authData.completer(rv, secret) (io.Reader, int64, error)` in Tasks 5, 6 and 9; `requestView.header` returns `(string, bool)` everywhere; `chunkedParams` is built the same way in Task 6's `completer` and its specs; `op.AuthResult.ContentLength` is set by Task 9 and applied by Task 1's handler edit; `CredentialStore` has the two getters Task 9's `identity` needs and `memstore.Store` and `opfakes.FakeUserStore` satisfy it; `op.AccountStore.GetAccount` returns `*op.AccountRecord` in Tasks 1 and 9; `Verifier.Authenticate(ctx, req, payloads op.PayloadForms)` is G's `s3.Authenticator` signature in Task 9's Interfaces, code and specs (`anyPayload` in the identity specs); `payloadClass.acceptedBy(op.PayloadForms) error` in Tasks 5 and 9; `parseChunkSize([]byte) (uint64, bool)` and `maxTrailerSection` = 1024 in Task 6's Interfaces, code and specs; `withTrailer` is declared once, in Task 6's `chunked_test.go`, and used by Task 7.

**Review Focus.** 1 → Task 3 ("takes a missing x-amz-content-sha256 as UNSIGNED-PAYLOAD"), Task 5 ("absent header with a body is unsigned"), Task 9 ("authenticates the request go-ceph's admin client sends"). 2 → Task 4 ("accepts a presigned URL signed with host:port"). 3 → Task 6 (the malformed framing and malformed size tables, the unsigned-form size table, the `ffffffffffffffff` chunk, the header without a newline, the 1024- and 1025-byte sections) and Task 7 (the 290-byte signed SHA512 trailer). 4 → Task 3 ("rejects an unsigned x-amz header unless rgw_sigv4_insecure", the `unsigned-host-rejected` and `unsigned-content-type-rejected` vectors). 5 → Task 5 ("reports a mismatch on the read that reaches EOF") and Task 6 ("reports a corrupted chunk when its data has been consumed"), plus the `op.AuthResult.Body` doc comment (Task 1) and the report to W and P. 6 → Task 5 ("acceptedBy is get_auth_data_v4's per-op whitelist") and Task 9 ("refuses a payload form the dispatched op does not take, before the access key is looked up" and "answers the dispatched route's payload forms at the cluster's release").

**Cluster tasks.** Only Task 10's Steps 5 and 6 (and its Step 2 integration spec) need a cluster; they use the rooket clusters through T's harness. Tasks 1 to 9 run on `memstore`, `opfakes` and the reference SDK.

**G- and Z-contract findings for the caller.** (a) `op.AuthResult` had no slot for the aws-chunked decoded length; added as `ContentLength` (D-A3), applied in `s3.Handler`. (b) No account store exists in G's contract while M and Z read `Identity.Account` and radosgw loads it at authentication; `op.AccountStore` added (D-A4); N owns the driver side; account users are 403 on the real driver until then. (c) `s3.Authenticator.Authenticate` takes the dispatched route's `op.PayloadForms` (G Tasks 1, 3 and 4), so radosgw's per-op 501s for unexpected payload forms are reproduced per release (D-A1); N's admin handler passes its own routes' forms (`op.PayloadSigned` for the metadata put, which radosgw types `RGW_OP_ADMIN_SET_METADATA`; none for every other admin op). (d) The interface sketch's `Verifier.Region` (`00-index.md`, "Shared interfaces and seam gaps") is dropped: radosgw never checks the credential scope. (e) Z's finding (d) is honoured (`Admin = admin || system`); `op.AccountStore` carries `AccountName` beside `GetAccount`, so it satisfies Z's `authz.AccountLookup` from wave 1 and N only replaces the driver stubs. (f) G's Task 9 integration spec expects 501 for a signed `GET /`; it becomes 403 InvalidAccessKeyId when this unit lands (Task 10 Step 2). (g) W and P must read `r.Body` to EOF before completing an object (D-A2); a body read error is an `op.Error` (mismatch, signature, limit, argument) or `io.ErrUnexpectedEOF` for a truncated stream, which W maps as radosgw maps a short body.
